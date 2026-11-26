package perfmetrics

import (
	"maps"
	"slices"
	"sync"
	"time"
)

const (
	liveRetentionMinutes     = 24 * 60
	liveModelLimit           = 32
	liveModelHistogramLimit  = 256
	liveGlobalHistogramLimit = 1024
	// Query-time histograms also have a cap: a long window must not multiply
	// retained histogram memory for every simultaneous dashboard reader.
	liveWindowHistogramLimit = 16384
)

// LivePoint reports only observed synchronous relay work. A missing measurement
// is null, including model concurrency, which is not measured independently.
type LivePoint struct {
	Ts           int64    `json:"ts"`
	Model        string   `json:"model"`
	Requests     int64    `json:"requests"`
	SuccessCount int64    `json:"success_count"`
	LatencyMs    *float64 `json:"latency_ms"`
	TtftMs       *float64 `json:"ttft_ms"`
	Tps          *float64 `json:"tps"`
	P95Ms        *int64   `json:"p95_ms"`
	Concurrency  *int64   `json:"concurrency"`
}

type LiveDashboardResponse struct {
	Instance        string      `json:"instance"`
	ObservedFrom    int64       `json:"observed_from"`
	WindowStart     int64       `json:"window_start"`
	WindowEnd       int64       `json:"window_end"`
	StepSeconds     int64       `json:"step_seconds"`
	Active          int64       `json:"active"`
	Series          []LivePoint `json:"series"`
	Totals          []LivePoint `json:"totals"`
	Summary         []LivePoint `json:"summary"`
	Total           LivePoint   `json:"total"`
	ModelsTruncated bool        `json:"models_truncated"`
	P95Limited      bool        `json:"p95_limited"`
}

type liveCounters struct {
	requests, successes        int64
	latencySum, latencyCount   int64
	ttftSum, ttftCount         int64
	outputTokens, generationMs int64
	hist                       latencyHistogram
	histLimited                bool
}

func (c *liveCounters) add(sample Sample, histogramLimit int) {
	c.requests++
	if sample.Success {
		c.successes++
	}
	if sample.LatencyMs >= 0 {
		c.latencySum += sample.LatencyMs
		c.latencyCount++
		if !c.histLimited {
			if c.hist == nil {
				c.hist = make(latencyHistogram)
			}
			if c.hist[sample.LatencyMs] == 0 && len(c.hist) == histogramLimit {
				c.histLimited, c.hist = true, nil
			} else {
				c.hist[sample.LatencyMs]++
			}
		}
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		c.ttftSum += sample.TtftMs
		c.ttftCount++
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		c.outputTokens += sample.OutputTokens
		c.generationMs += sample.GenerationMs
	}
}

func (c *liveCounters) merge(other liveCounters) {
	c.requests += other.requests
	c.successes += other.successes
	c.latencySum += other.latencySum
	c.latencyCount += other.latencyCount
	c.ttftSum += other.ttftSum
	c.ttftCount += other.ttftCount
	c.outputTokens += other.outputTokens
	c.generationMs += other.generationMs
	if other.histLimited {
		c.histLimited, c.hist = true, nil
	}
	if c.histLimited {
		return
	}
	if c.hist == nil && len(other.hist) > 0 {
		c.hist = make(latencyHistogram)
	}
	for ms, count := range other.hist {
		if c.hist[ms] == 0 && len(c.hist) == liveWindowHistogramLimit {
			c.histLimited, c.hist = true, nil
			return
		}
		c.hist[ms] += count
	}
}

func (c liveCounters) point(ts int64, model string) LivePoint {
	p := LivePoint{Ts: ts, Model: model, Requests: c.requests, SuccessCount: c.successes}
	if c.latencyCount > 0 {
		value := float64(c.latencySum) / float64(c.latencyCount)
		p.LatencyMs = &value
	}
	if c.ttftCount > 0 {
		value := float64(c.ttftSum) / float64(c.ttftCount)
		p.TtftMs = &value
	}
	if c.generationMs > 0 {
		value := float64(c.outputTokens) * 1000 / float64(c.generationMs)
		p.Tps = &value
	}
	if !c.histLimited {
		_, p.P95Ms = c.hist.p95()
	}
	return p
}

type liveMinute struct {
	global    liveCounters
	models    map[string]*liveCounters
	peak      int64
	truncated bool
}

type liveCollector struct {
	mu           sync.Mutex
	now          func() time.Time
	instance     string
	observedFrom int64
	minute       int64
	active       int64
	buckets      map[int64]*liveMinute
	models       map[string]int64
}

var liveMetrics = &liveCollector{now: time.Now, instance: gatewayLoad.instance}

func (g *liveCollector) start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buckets == nil {
		g.observedFrom = g.now().Unix()
		g.minute = g.observedFrom / 60 * 60
		g.buckets = make(map[int64]*liveMinute)
		g.models = make(map[string]int64)
		g.buckets[g.minute] = &liveMinute{}
	}
}

func (g *liveCollector) advanceLocked() int64 {
	minute := max(g.minute, g.now().Unix()/60*60)
	oldest := minute - (liveRetentionMinutes-1)*60
	for ts := range g.buckets {
		if ts < oldest {
			delete(g.buckets, ts)
		}
	}
	for name, last := range g.models {
		if last < oldest {
			delete(g.models, name)
		}
	}
	// Carry the actual active count across quiet minute boundaries, including
	// a stream open for hours with no completed requests or dashboard reads.
	if g.active > 0 {
		for ts := max(g.minute+60, oldest); ts <= minute; ts += 60 {
			g.buckets[ts] = &liveMinute{peak: g.active}
		}
	}
	if g.buckets[minute] == nil {
		g.buckets[minute] = &liveMinute{}
	}
	g.minute = minute
	return minute
}

func (g *liveCollector) begin() func() {
	g.mu.Lock()
	if g.buckets == nil {
		g.mu.Unlock()
		return func() {}
	}
	minute := g.advanceLocked()
	g.active++
	g.buckets[minute].peak = max(g.buckets[minute].peak, g.active)
	g.mu.Unlock()
	return sync.OnceFunc(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.advanceLocked()
		g.active--
	})
}

func (g *liveCollector) record(sample Sample) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buckets == nil {
		return
	}
	minute := g.advanceLocked()
	bucket := g.buckets[minute]
	bucket.global.add(sample, liveGlobalHistogramLimit)
	if sample.Model == "" || len(sample.Model) > 512 {
		bucket.truncated = true
		return
	}
	if _, known := g.models[sample.Model]; !known && len(g.models) == liveModelLimit {
		bucket.truncated = true
		return
	}
	g.models[sample.Model] = minute
	if bucket.models == nil {
		bucket.models = make(map[string]*liveCounters)
	}
	if bucket.models[sample.Model] == nil {
		bucket.models[sample.Model] = &liveCounters{}
	}
	bucket.models[sample.Model].add(sample, liveModelHistogramLimit)
}

var liveQuerySlots = make(chan struct{}, 2)

// TryQueryLive is an in-memory, single-instance view, independent of database
// monitoring retention and group/pricing configuration. Windows include the
// current minute and only data observed since this process initialized.
func TryQueryLive(seconds, step int64, model string) (LiveDashboardResponse, bool) {
	select {
	case liveQuerySlots <- struct{}{}:
		defer func() { <-liveQuerySlots }()
		return liveMetrics.query(seconds, step, model), true
	default:
		return LiveDashboardResponse{}, false
	}
}

type liveSnapshot struct {
	result  LiveDashboardResponse
	minutes map[int64]liveMinute
}

func (g *liveCollector) query(seconds, step int64, model string) LiveDashboardResponse {
	return g.snapshot(seconds, step, model).summarize(model)
}

func (g *liveCollector) snapshot(seconds, step int64, model string) liveSnapshot {
	g.mu.Lock()
	end := g.now().Unix()
	start := end/60*60 + 60 - seconds
	result := LiveDashboardResponse{
		Instance: g.instance, ObservedFrom: g.observedFrom,
		WindowStart: start, WindowEnd: end, StepSeconds: step, Active: g.active,
		Series: []LivePoint{}, Totals: []LivePoint{}, Summary: []LivePoint{},
		Total: LivePoint{Ts: start, Model: model},
	}
	if g.buckets == nil {
		g.mu.Unlock()
		return liveSnapshot{result: result}
	}
	g.advanceLocked()
	// record always writes the current monotonic minute. Sealed minutes are
	// immutable, so readers can safely retain them after eviction from the ring.
	// Only the open minute needs a deep copy under the relay mutex.
	snapshots := make(map[int64]liveMinute)
	for ts, bucket := range g.buckets {
		if ts < start || ts > end {
			continue
		}
		if ts < g.minute {
			snapshots[ts] = *bucket
			continue
		}
		copyBucket := liveMinute{peak: bucket.peak, truncated: bucket.truncated, models: make(map[string]*liveCounters)}
		if model == "" {
			copyBucket.global = bucket.global
			copyBucket.global.hist = maps.Clone(bucket.global.hist)
		}
		for name, counters := range bucket.models {
			if model != "" && model != name {
				continue
			}
			copyCounters := *counters
			copyCounters.hist = maps.Clone(counters.hist)
			copyBucket.models[name] = &copyCounters
		}
		snapshots[ts] = copyBucket
	}
	g.mu.Unlock()
	return liveSnapshot{result: result, minutes: snapshots}
}

func (snapshot liveSnapshot) summarize(model string) LiveDashboardResponse {
	result := snapshot.result
	if snapshot.minutes == nil {
		return result
	}
	start, end, step := result.WindowStart, result.WindowEnd, result.StepSeconds
	summary := make(map[string]*liveCounters)
	total := liveCounters{}
	var peak *int64
	for from := start; from <= end; from += step {
		group := liveCounters{}
		models := make(map[string]*liveCounters)
		var groupPeak *int64
		observed := false
		for ts := from; ts < from+step && ts <= end; ts += 60 {
			if ts < result.ObservedFrom/60*60 {
				continue
			}
			// No event in an observed minute means zero active work; quiet
			// minutes do not need an allocated bucket to preserve that fact.
			bucket := snapshot.minutes[ts]
			observed = true
			result.ModelsTruncated = result.ModelsTruncated || bucket.truncated
			if model == "" {
				group.merge(bucket.global)
				total.merge(bucket.global)
				if groupPeak == nil || bucket.peak > *groupPeak {
					value := bucket.peak
					groupPeak = &value
				}
			}
			for name, counters := range bucket.models {
				if model != "" && model != name {
					continue
				}
				if models[name] == nil {
					models[name] = &liveCounters{}
				}
				if summary[name] == nil {
					summary[name] = &liveCounters{}
				}
				models[name].merge(*counters)
				summary[name].merge(*counters)
				if model != "" {
					group.merge(*counters)
					total.merge(*counters)
				}
			}
		}
		if !observed {
			continue
		}
		point := group.point(from, model)
		point.Concurrency = groupPeak
		result.Totals = append(result.Totals, point)
		result.P95Limited = result.P95Limited || group.histLimited
		if groupPeak != nil && (peak == nil || *groupPeak > *peak) {
			peak = groupPeak
		}
		for _, name := range slices.Sorted(maps.Keys(models)) {
			result.Series = append(result.Series, models[name].point(from, name))
			result.P95Limited = result.P95Limited || models[name].histLimited
		}
	}
	for _, name := range slices.Sorted(maps.Keys(summary)) {
		result.Summary = append(result.Summary, summary[name].point(start, name))
		result.P95Limited = result.P95Limited || summary[name].histLimited
	}
	result.Total = total.point(start, model)
	result.Total.Concurrency = peak
	result.P95Limited = result.P95Limited || total.histLimited
	return result
}
