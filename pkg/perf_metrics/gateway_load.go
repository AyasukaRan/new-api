package perfmetrics

import (
	"encoding/binary"
	"errors"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

// Keep the counts of each distinct millisecond duration, not request bodies,
// IDs, per-request timestamps or an average of hourly percentiles.
type latencyHistogram map[int64]int64

func (h latencyHistogram) encode() []byte {
	var data []byte
	var prev int64
	for _, ms := range slices.Sorted(maps.Keys(h)) {
		data = binary.AppendUvarint(data, uint64(ms-prev))
		data = binary.AppendUvarint(data, uint64(h[ms]))
		prev = ms
	}
	return data
}

func (h latencyHistogram) mergeEncoded(data []byte) error {
	var ms int64
	first := true
	for len(data) > 0 {
		delta, n := binary.Uvarint(data)
		if n <= 0 || delta > uint64(math.MaxInt64-ms) || (!first && delta == 0) {
			return errors.New("invalid gateway latency duration")
		}
		ms += int64(delta)
		count, m := binary.Uvarint(data[n:])
		if m <= 0 || count == 0 || count > uint64(math.MaxInt64-h[ms]) {
			return errors.New("invalid gateway latency count")
		}
		first = false
		h[ms] += int64(count)
		data = data[n+m:]
	}
	return nil
}

func (h latencyHistogram) p95() (sampleCount int64, latency *int64) {
	for _, n := range h {
		sampleCount += n
	}
	// ceil(0.95*N), written without floating point or multiplication overflow.
	rank := sampleCount - sampleCount/20
	for _, ms := range slices.Sorted(maps.Keys(h)) {
		rank -= h[ms]
		if rank <= 0 {
			return sampleCount, &ms
		}
	}
	return sampleCount, nil
}

type gatewayLoadBucket struct {
	from   int64
	peak   int64
	counts latencyHistogram
}

type gatewayLoadCollector struct {
	// Never hold mu for SQL, sorting or encoding on the relay path.
	mu        sync.Mutex
	storeMu   sync.RWMutex
	instance  string
	now       func() time.Time
	running   bool
	observing bool
	epoch     uint64
	active    int64
	hour      int64
	buckets   map[int64]*gatewayLoadBucket
}

var gatewayLoad = gatewayLoadCollector{now: time.Now, instance: common.GetUUID()}

func (g *gatewayLoadCollector) start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return
	}
	g.running = true
	g.buckets = make(map[int64]*gatewayLoadBucket)
	if perf_metrics_setting.GetSetting().Enabled {
		g.observeLocked(g.now())
	}
}

func (g *gatewayLoadCollector) observeLocked(now time.Time) *gatewayLoadBucket {
	hour := max(now.Truncate(time.Hour).Unix(), g.hour)
	from := now.Unix()
	if g.observing && hour != g.hour {
		// An open stream remains active across every intervening hour, even
		// with no new requests, finishes, or dashboard refreshes.
		for ts := g.hour + 3600; ts < hour; ts += 3600 {
			g.buckets[ts] = &gatewayLoadBucket{from: ts, peak: g.active}
		}
		from = hour
	}
	g.observing = true
	g.hour = hour
	if b := g.buckets[hour]; b != nil {
		return b
	}
	g.buckets[hour] = &gatewayLoadBucket{from: from, peak: g.active}
	return g.buckets[hour]
}

// BeginGatewayRequest measures a logical synchronous relay, outside its retry
// loop. For WebSocket Responses it is one response.create, not the socket.
func BeginGatewayRequest(startedAt time.Time) func() {
	finishLoad := gatewayLoad.begin(startedAt)
	finishLive := liveMetrics.begin()
	return func() {
		finishLoad()
		finishLive()
	}
}

func (g *gatewayLoadCollector) pauseLocked() {
	if g.observing {
		g.epoch++
		g.active = 0
		g.observing = false
	}
}

func (g *gatewayLoadCollector) begin(startedAt time.Time) func() {
	g.mu.Lock()
	if !g.running || !perf_metrics_setting.GetSetting().Enabled {
		g.pauseLocked()
		g.mu.Unlock()
		return func() {}
	}
	now := g.now()
	if startedAt.IsZero() || startedAt.After(now) {
		startedAt = now
	}
	epoch := g.epoch
	b := g.observeLocked(now)
	g.active++
	b.peak = max(b.peak, g.active)
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !perf_metrics_setting.GetSetting().Enabled {
			g.pauseLocked()
		}
		if epoch != g.epoch {
			return // Monitoring was disabled while this request was running.
		}
		end := g.now()
		b := g.observeLocked(end)
		g.active--
		if b.counts == nil {
			b.counts = make(latencyHistogram)
		}
		b.counts[max(int64(0), end.Sub(startedAt).Milliseconds())]++
	}
}

func (g *gatewayLoadCollector) snapshot(start, end int64) (int64, map[int64]gatewayLoadBucket) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running && perf_metrics_setting.GetSetting().Enabled {
		g.observeLocked(g.now())
	} else {
		g.pauseLocked()
	}
	buckets := make(map[int64]gatewayLoadBucket)
	for hour, bucket := range g.buckets {
		if hour >= start && (end == 0 || hour <= end) {
			buckets[hour] = gatewayLoadBucket{bucket.from, bucket.peak, maps.Clone(bucket.counts)}
		}
	}
	return g.hour, buckets
}

func (g *gatewayLoadCollector) flush() error {
	g.storeMu.Lock()
	defer g.storeMu.Unlock()
	current, snapshots := g.snapshot(0, 0)
	for hour, b := range snapshots {
		if err := model.SaveGatewayLoadHour(&model.GatewayLoadHour{
			BucketTs: hour, InstanceID: g.instance, ObservedFrom: b.from,
			PeakConcurrency: b.peak, LatencyCounts: b.counts.encode(),
		}); err != nil {
			return err // Retain all unpersisted snapshots for a later retry.
		}
		if hour < current {
			g.mu.Lock()
			delete(g.buckets, hour)
			g.mu.Unlock()
		}
	}
	return nil
}

func (g *gatewayLoadCollector) fillDiagnostics(d *GatewayDiagnostics, start, end int64) error {
	g.storeMu.RLock()
	defer g.storeMu.RUnlock()
	_, local := g.snapshot(start, end)
	total := make(latencyHistogram)
	hist := make(latencyHistogram)
	i := 0
	setPeak := func(hour *HourDiagnostics, peak, from int64) {
		if hour.PeakConcurrency == nil || peak > *hour.PeakConcurrency {
			hour.PeakConcurrency = &peak
		}
		if d.Total.PeakConcurrency == nil || peak > *d.Total.PeakConcurrency {
			d.Total.PeakConcurrency = &peak
		}
		if d.ObservedFrom == nil || from < *d.ObservedFrom {
			d.ObservedFrom = &from
		}
	}
	finishHour := func() {
		hour := &d.Hours[i]
		if b, ok := local[hour.Ts]; ok {
			setPeak(hour, b.peak, b.from)
			for ms, n := range b.counts {
				hist[ms] += n
			}
		}
		hour.LatencySampleCount, hour.P95LatencyMs = hist.p95()
		for ms, n := range hist {
			total[ms] += n
		}
		clear(hist)
		i++
	}
	err := model.WalkGatewayLoadHours(start, end, func(row model.GatewayLoadHour) error {
		for i < len(d.Hours) && d.Hours[i].Ts < row.BucketTs {
			finishHour()
		}
		if i == len(d.Hours) || d.Hours[i].Ts != row.BucketTs {
			return errors.New("misaligned gateway load hour")
		}
		if _, ok := local[row.BucketTs]; ok && row.InstanceID == g.instance {
			return nil // Local cumulative snapshot supersedes this process's persisted copy.
		}
		setPeak(&d.Hours[i], row.PeakConcurrency, row.ObservedFrom)
		return hist.mergeEncoded(row.LatencyCounts)
	})
	if err != nil {
		return err
	}
	for i < len(d.Hours) {
		finishHour()
	}
	d.Total.LatencySampleCount, d.Total.P95LatencyMs = total.p95()
	return nil
}
