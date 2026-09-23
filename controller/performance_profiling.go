package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	profile "github.com/google/pprof/profile"
)

const profilingByteLimit = 8 << 20
const profilingNodeLimit = 4096

var profilingQuerySlots = make(chan struct{}, 2)
var profilingCaptureLock sync.Mutex
var profilingDownloads = struct {
	sync.Mutex
	items map[string]profilingDownload
}{items: make(map[string]profilingDownload)}

var profilingHTTPClient = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

type profilingDownload struct {
	body    []byte
	name    string
	expires time.Time
}

type profilingType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

type profilingFrame struct {
	Name  string `json:"name"`
	Depth int    `json:"depth"`
	Start int64  `json:"start"`
	Total int64  `json:"total"`
	Self  int64  `json:"self"`
}

type profilingHotspot struct {
	Name  string `json:"name"`
	Self  int64  `json:"self"`
	Total int64  `json:"total"`
}

type profilingPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

type profilingResult struct {
	Source      string             `json:"source"`
	ProfileType string             `json:"profile_type"`
	Unit        string             `json:"unit"`
	Start       int64              `json:"start"`
	End         int64              `json:"end"`
	Total       int64              `json:"total"`
	Flamegraph  []profilingFrame   `json:"flamegraph"`
	Hotspots    []profilingHotspot `json:"hotspots"`
	Timeline    []profilingPoint   `json:"timeline"`
	Truncated   bool               `json:"truncated"`
	DownloadID  string             `json:"download_id,omitempty"`
}

// Profile types are accepted only from the Go runtime metrics we collect. The
// application selector is always server-owned and cannot be supplied by clients.
func profilingTypeInfo(id string) (profilingType, bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 5 || len(id) > 200 {
		return profilingType{}, false
	}
	for _, part := range parts {
		if part == "" {
			return profilingType{}, false
		}
		for _, char := range part {
			if char != '_' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
				return profilingType{}, false
			}
		}
	}
	name := ""
	switch parts[1] {
	case "cpu":
		name = "CPU"
	case "alloc_objects":
		name = "Allocated objects"
	case "alloc_space":
		name = "Allocated memory"
	case "inuse_objects":
		name = "Live objects"
	case "inuse_space":
		name = "Live memory"
	case "goroutine", "goroutines":
		name = "Goroutines"
	case "contentions":
		if parts[0] == "mutex" {
			name = "Mutex contention"
		}
		if parts[0] == "block" {
			name = "Blocking events"
		}
	case "delay":
		if parts[0] == "mutex" {
			name = "Mutex wait"
		}
		if parts[0] == "block" {
			name = "Blocking time"
		}
	}
	return profilingType{ID: id, Name: name, Unit: parts[2]}, name != "" && slices.Contains([]string{"nanoseconds", "bytes", "count"}, parts[2])
}

// Only fixed RPC names used below reach the configured service. Redirects are
// disabled, and credentials and upstream response bodies never reach clients.
func profilingRPC(ctx context.Context, method string, payload, result any) error {
	base, err := url.Parse(os.Getenv("PYROSCOPE_URL"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return errors.New("profiling service is not configured")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/querier.v1.QuerierService/" + method
	base.RawQuery, base.Fragment = "", ""
	body, err := common.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("profiling service is unavailable")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	if user := os.Getenv("PYROSCOPE_BASIC_AUTH_USER"); user != "" {
		request.SetBasicAuth(user, os.Getenv("PYROSCOPE_BASIC_AUTH_PASSWORD"))
	}
	response, err := profilingHTTPClient.Do(request)
	if err != nil {
		return errors.New("profiling service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("profiling service is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, profilingByteLimit+1))
	if err != nil || len(data) > profilingByteLimit {
		return errors.New("profiling response exceeded the limit")
	}
	if err = common.Unmarshal(data, result); err != nil {
		return errors.New("invalid profiling response")
	}
	return nil
}

func GetProfilingStatus(c *gin.Context) {
	app := common.GetEnvOrDefaultString("PYROSCOPE_APP_NAME", "new-api")
	configured := os.Getenv("PYROSCOPE_URL") != ""
	types := make([]profilingType, 0)
	available := false
	if configured {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		var response struct {
			ProfileTypes []struct {
				ID string `json:"ID"`
			} `json:"profileTypes"`
		}
		err := profilingRPC(ctx, "ProfileTypes", map[string]int64{"start": time.Now().Add(-24 * time.Hour).UnixMilli(), "end": time.Now().UnixMilli()}, &response)
		available = err == nil
		if available {
			for _, entry := range response.ProfileTypes {
				if item, ok := profilingTypeInfo(entry.ID); ok {
					types = append(types, item)
				}
			}
		}
	}
	captureTypes := []string{"heap", "allocs", "goroutine", "mutex", "block"}
	pprofEnabled := os.Getenv("ENABLE_PPROF") == "true"
	cpuAvailable := pprofEnabled && !common.PyroscopeRunning()
	if cpuAvailable {
		captureTypes = append([]string{"cpu"}, captureTypes...)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"pyroscope_configured": configured, "pyroscope_running": common.PyroscopeRunning(),
		"pyroscope_available": available, "app_name": app, "profile_types": types,
		"pprof_enabled": pprofEnabled, "cpu_capture_available": cpuAvailable,
		"capture_types": captureTypes, "max_capture_seconds": 15,
	}})
}

func QueryProfiling(c *gin.Context) {
	var query struct {
		ProfileType string `json:"profile_type"`
		Start       int64  `json:"start"`
		End         int64  `json:"end"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := common.DecodeJson(c.Request.Body, &query); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid profiling query"})
		return
	}
	kind, valid := profilingTypeInfo(query.ProfileType)
	now := time.Now().UnixMilli()
	if !valid || query.Start <= 0 || query.End <= query.Start || query.End-query.Start > int64(24*time.Hour/time.Millisecond) || query.End > now+60_000 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Select a valid profile and a time range of at most 24 hours"})
		return
	}
	select {
	case profilingQuerySlots <- struct{}{}:
		defer func() { <-profilingQuerySlots }()
	default:
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "message": "Another profiling query is running"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	selector := `{service_name=` + strconv.Quote(common.GetEnvOrDefaultString("PYROSCOPE_APP_NAME", "new-api")) + `}`
	payload := map[string]any{"profileTypeID": query.ProfileType, "labelSelector": selector, "start": query.Start, "end": query.End, "maxNodes": profilingNodeLimit}
	var response struct {
		Flamegraph struct {
			Names  []string `json:"names"`
			Levels []struct {
				Values []json.Number `json:"values"`
			} `json:"levels"`
			Total json.Number `json:"total"`
		} `json:"flamegraph"`
	}
	if err := profilingRPC(ctx, "SelectMergeStacktraces", payload, &response); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Profiling service is unavailable"})
		return
	}
	result := profilingResult{Source: "pyroscope", ProfileType: query.ProfileType, Unit: kind.Unit, Start: query.Start, End: query.End, Flamegraph: []profilingFrame{}, Timeline: []profilingPoint{}}
	if response.Flamegraph.Total != "" {
		var err error
		result.Total, err = response.Flamegraph.Total.Int64()
		if err != nil || result.Total < 0 {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Invalid profiling response"})
			return
		}
	}
	for depth, level := range response.Flamegraph.Levels {
		if len(level.Values)%4 != 0 || depth > 256 {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Invalid profiling response"})
			return
		}
		var offset int64
		for i := 0; i < len(level.Values); i += 4 {
			delta, e1 := level.Values[i].Int64()
			total, e2 := level.Values[i+1].Int64()
			self, e3 := level.Values[i+2].Int64()
			nameIndex, e4 := level.Values[i+3].Int64()
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || delta < 0 || total < 0 || self < 0 || self > total || nameIndex < 0 || nameIndex >= int64(len(response.Flamegraph.Names)) || delta > result.Total-offset || total > result.Total-offset-delta {
				c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Invalid profiling response"})
				return
			}
			offset += delta
			if len(result.Flamegraph) < profilingNodeLimit {
				result.Flamegraph = append(result.Flamegraph, profilingFrame{Name: response.Flamegraph.Names[nameIndex], Depth: depth, Start: offset, Total: total, Self: self})
			} else {
				result.Truncated = true
			}
			offset += total
		}
	}
	// The graph is still useful when time-series data is temporarily unavailable.
	delete(payload, "maxNodes")
	payload["step"] = max(10, (query.End-query.Start)/1000/240)
	var series struct {
		Series []struct {
			Points []struct {
				Timestamp json.Number `json:"timestamp"`
				Value     float64     `json:"value"`
			} `json:"points"`
		} `json:"series"`
	}
	if err := profilingRPC(ctx, "SelectSeries", payload, &series); err == nil {
		points := map[int64]float64{}
		for _, entry := range series.Series {
			for _, point := range entry.Points {
				stamp, err := point.Timestamp.Int64()
				if err == nil && stamp >= query.Start && stamp <= query.End {
					points[stamp] += point.Value
				}
			}
		}
		for stamp, value := range points {
			result.Timeline = append(result.Timeline, profilingPoint{Timestamp: stamp, Value: value})
		}
		slices.SortFunc(result.Timeline, func(a, b profilingPoint) int {
			if a.Timestamp < b.Timestamp {
				return -1
			}
			if a.Timestamp > b.Timestamp {
				return 1
			}
			return 0
		})
		if len(result.Timeline) > 250 {
			result.Timeline = result.Timeline[:250]
			result.Truncated = true
		}
	}
	result.Hotspots = profilingHotspots(result.Flamegraph)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func profilingHotspots(frames []profilingFrame) []profilingHotspot {
	byName := map[string][]profilingFrame{}
	for _, frame := range frames {
		if frame.Depth != 0 {
			byName[frame.Name] = append(byName[frame.Name], frame)
		}
	}
	result := make([]profilingHotspot, 0, len(byName))
	for name, entries := range byName {
		// Recursive calls occupy overlapping sample intervals. Count that
		// interval once, as pprof does for a function's cumulative cost.
		slices.SortFunc(entries, func(a, b profilingFrame) int {
			if a.Start < b.Start {
				return -1
			}
			if a.Start > b.Start {
				return 1
			}
			return 0
		})
		item := profilingHotspot{Name: name}
		var end int64
		for _, entry := range entries {
			item.Self += entry.Self
			if next := entry.Start + entry.Total; next > end {
				item.Total += next - max(end, entry.Start)
				end = next
			}
		}
		result = append(result, item)
	}
	slices.SortFunc(result, func(a, b profilingHotspot) int {
		if a.Self != b.Self {
			if a.Self > b.Self {
				return -1
			}
			return 1
		}
		if a.Total != b.Total {
			if a.Total > b.Total {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result[:min(100, len(result))]
}

type profilingBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *profilingBuffer) Write(data []byte) (int, error) {
	if len(data) > profilingByteLimit-b.Len() {
		b.exceeded = true
		return 0, errors.New("profile exceeded the size limit")
	}
	return b.Buffer.Write(data)
}

func CaptureProfiling(c *gin.Context) {
	if os.Getenv("ENABLE_PPROF") != "true" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Instant profiling is not enabled"})
		return
	}
	var request struct {
		ProfileType string `json:"profile_type"`
		Seconds     int    `json:"seconds"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || !slices.Contains([]string{"cpu", "heap", "allocs", "goroutine", "mutex", "block"}, request.ProfileType) || request.Seconds < 0 || request.Seconds > 15 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid profiling capture"})
		return
	}
	if request.Seconds == 0 {
		request.Seconds = 10
	}
	if request.ProfileType == "cpu" && common.PyroscopeRunning() {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "CPU profiling is already provided by continuous profiling"})
		return
	}
	if !profilingCaptureLock.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Another profiling capture is running"})
		return
	}
	defer profilingCaptureLock.Unlock()
	var body profilingBuffer
	start := time.Now()
	if request.ProfileType == "cpu" {
		if err := pprof.StartCPUProfile(&body); err != nil {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Another CPU profiler is running"})
			return
		}
		timer := time.NewTimer(time.Duration(request.Seconds) * time.Second)
		select {
		case <-timer.C:
		case <-c.Request.Context().Done():
		}
		timer.Stop()
		pprof.StopCPUProfile()
	} else if err := pprof.Lookup(request.ProfileType).WriteTo(&body, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to capture profile"})
		return
	}
	if c.Request.Context().Err() != nil {
		return
	}
	if body.exceeded {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"success": false, "message": "Profile exceeded the size limit"})
		return
	}
	parsed, err := profile.ParseData(body.Bytes())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to read profile"})
		return
	}
	result := profilingFromPprof(parsed, request.ProfileType)
	result.Start, result.End = start.UnixMilli(), time.Now().UnixMilli()
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to store profile"})
		return
	}
	result.DownloadID = hex.EncodeToString(randomID[:])
	profilingDownloads.Lock()
	for id, item := range profilingDownloads.items {
		if time.Now().After(item.expires) {
			delete(profilingDownloads.items, id)
		}
	}
	if len(profilingDownloads.items) >= 4 {
		oldestID := ""
		var oldest time.Time
		for id, item := range profilingDownloads.items {
			if oldestID == "" || item.expires.Before(oldest) {
				oldestID, oldest = id, item.expires
			}
		}
		delete(profilingDownloads.items, oldestID)
	}
	profilingDownloads.items[result.DownloadID] = profilingDownload{body: bytes.Clone(body.Bytes()), name: request.ProfileType, expires: time.Now().Add(5 * time.Minute)}
	profilingDownloads.Unlock()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// Build one bounded tree, merging identical stacks. Deeper or excess frames are
// accounted for at their nearest retained ancestor, preserving the total.
func profilingFromPprof(parsed *profile.Profile, kind string) profilingResult {
	result := profilingResult{Source: "pprof", ProfileType: kind, Flamegraph: []profilingFrame{}, Hotspots: []profilingHotspot{}, Timeline: []profilingPoint{}}
	sampleType := map[string]string{"cpu": "cpu", "heap": "inuse_space", "allocs": "alloc_space", "goroutine": "goroutine", "mutex": "delay", "block": "delay"}[kind]
	index := -1
	for i, entry := range parsed.SampleType {
		if entry.Type == sampleType {
			index = i
			result.Unit = entry.Unit
			break
		}
	}
	if index < 0 {
		return result
	}
	type treeNode struct {
		frame    profilingFrame
		children map[string]*treeNode
	}
	root := &treeNode{frame: profilingFrame{Name: "total"}, children: map[string]*treeNode{}}
	nodeCount := 1
	for _, sample := range parsed.Sample {
		if index >= len(sample.Value) || sample.Value[index] <= 0 {
			continue
		}
		value := sample.Value[index]
		root.frame.Total += value
		node := root
		frames := []string{}
		for i := len(sample.Location) - 1; i >= 0; i-- {
			location := sample.Location[i]
			for j := len(location.Line) - 1; j >= 0; j-- {
				if location.Line[j].Function != nil {
					frames = append(frames, location.Line[j].Function.Name)
				}
			}
			if len(location.Line) == 0 {
				frames = append(frames, fmt.Sprintf("0x%x", location.Address))
			}
		}
		for depth, name := range frames {
			child := node.children[name]
			if depth >= 128 || (child == nil && nodeCount >= profilingNodeLimit) {
				result.Truncated = true
				break
			}
			if child == nil {
				child = &treeNode{frame: profilingFrame{Name: name, Depth: depth + 1}, children: map[string]*treeNode{}}
				node.children[name] = child
				nodeCount++
			}
			child.frame.Total += value
			node = child
		}
		node.frame.Self += value
	}
	queue := []*treeNode{root}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result.Flamegraph = append(result.Flamegraph, node.frame)
		children := make([]*treeNode, 0, len(node.children))
		for _, child := range node.children {
			children = append(children, child)
		}
		slices.SortFunc(children, func(a, b *treeNode) int { return strings.Compare(a.frame.Name, b.frame.Name) })
		offset := node.frame.Start
		for _, child := range children {
			child.frame.Start = offset
			offset += child.frame.Total
			queue = append(queue, child)
		}
	}
	result.Total = root.frame.Total
	result.Hotspots = profilingHotspots(result.Flamegraph)
	return result
}

func DownloadProfiling(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	profilingDownloads.Lock()
	entry, found := profilingDownloads.items[c.Param("id")]
	if found && time.Now().After(entry.expires) {
		delete(profilingDownloads.items, c.Param("id"))
		found = false
	}
	profilingDownloads.Unlock()
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Profile download has expired"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.pprof"`, entry.name, c.Param("id")))
	c.Data(http.StatusOK, "application/octet-stream", entry.body)
}
