package common

import (
	"math"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

type GeoIPLocation struct {
	CountryCode string  `json:"country_code"`
	Country     string  `json:"country"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type geoIPCacheEntry struct {
	location GeoIPLocation
	found    bool
}

// GeoIPResolver reads an operator-managed local MMDB. A failed or incomplete
// update leaves the last verified database active; no IP is sent off-server.
type GeoIPResolver struct {
	mu        sync.Mutex
	path      string
	reader    *maxminddb.Reader
	file      os.FileInfo
	nextCheck time.Time
	cache     map[netip.Addr]geoIPCacheEntry
}

func NewGeoIPResolver(path string) *GeoIPResolver {
	return &GeoIPResolver{path: path, cache: make(map[netip.Addr]geoIPCacheEntry)}
}

var defaultGeoIP = sync.OnceValue(func() *GeoIPResolver {
	path := strings.TrimSpace(os.Getenv("GEOIP_DATABASE_PATH"))
	if path == "" {
		path = "/data/geoip/dbip-city-lite.mmdb"
	}
	return NewGeoIPResolver(path)
})

func DefaultGeoIPResolver() *GeoIPResolver { return defaultGeoIP() }

func (r *GeoIPResolver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reader == nil {
		return nil
	}
	err := r.reader.Close()
	r.reader, r.file = nil, nil
	clear(r.cache)
	return err
}

func (r *GeoIPResolver) reloadLocked() {
	now := time.Now()
	if now.Before(r.nextCheck) {
		return
	}
	r.nextCheck = now.Add(30 * time.Second)
	file, err := os.Stat(r.path)
	if err != nil || !file.Mode().IsRegular() || file.Size() > 512<<20 {
		return
	}
	if r.file != nil && os.SameFile(r.file, file) && r.file.ModTime() == file.ModTime() && r.file.Size() == file.Size() {
		return
	}
	reader, err := maxminddb.Open(r.path)
	if err != nil {
		return
	}
	if err := reader.Verify(); err != nil {
		_ = reader.Close()
		return
	}
	if r.reader != nil {
		_ = r.reader.Close()
	}
	r.reader, r.file = reader, file
	clear(r.cache)
}

func (r *GeoIPResolver) Status() (available bool, updatedAt int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	if r.reader == nil {
		return false, 0
	}
	return true, int64(r.reader.Metadata.BuildEpoch)
}

// Lookup returns an approximate database location, never a guessed location
// for private, documentation or otherwise non-public address space.
func (r *GeoIPResolver) Lookup(raw string) (GeoIPLocation, string) {
	address, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return GeoIPLocation{}, "invalid"
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || geoIPReserved(address) {
		return GeoIPLocation{}, "private"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	if cached, ok := r.cache[address]; ok {
		if cached.found {
			return cached.location, "located"
		}
		return GeoIPLocation{}, "unmapped"
	}
	if r.reader == nil {
		return GeoIPLocation{}, "unmapped"
	}
	var record struct {
		Country struct {
			ISOCode string            `maxminddb:"iso_code"`
			Names   map[string]string `maxminddb:"names"`
		} `maxminddb:"country"`
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
		Subdivisions []struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"subdivisions"`
		Location struct {
			Latitude  *float64 `maxminddb:"latitude"`
			Longitude *float64 `maxminddb:"longitude"`
		} `maxminddb:"location"`
	}
	err = r.reader.Lookup(address).Decode(&record)
	entry := geoIPCacheEntry{}
	if err == nil && record.Location.Latitude != nil && record.Location.Longitude != nil {
		lat, lon := *record.Location.Latitude, *record.Location.Longitude
		if !math.IsNaN(lat) && !math.IsNaN(lon) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180 {
			entry.found = true
			entry.location = GeoIPLocation{
				CountryCode: record.Country.ISOCode, Country: record.Country.Names["en"],
				City: record.City.Names["en"], Latitude: lat, Longitude: lon,
			}
			if len(record.Subdivisions) > 0 {
				entry.location.Region = record.Subdivisions[0].Names["en"]
			}
		}
	}
	if len(r.cache) >= 8192 {
		clear(r.cache)
	}
	r.cache[address] = entry
	if entry.found {
		return entry.location, "located"
	}
	return GeoIPLocation{}, "unmapped"
}

var geoIPReservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("100::/64"),
}

func geoIPReserved(address netip.Addr) bool {
	for _, prefix := range geoIPReservedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
