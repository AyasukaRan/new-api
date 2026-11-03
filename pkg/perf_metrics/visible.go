package perfmetrics

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

// Round signatures are internal bucket keys. Identical physical outcomes in the
// same bucket coalesce, while opposite outcomes in different rounds stay paired.
const sourceProbedRound = "round_probed"
const sourceCachedRound = "round_cached"
const sourceCoveredProbe = "covered_probe"

type RoundRoute struct {
	ChannelID        int
	Success          bool
	ProbeCompletedAt int64 // milliseconds; zero when the observation was cached
	ProbeSuccess     bool
}

func RecordVisibleRound(name string, routes []RoundRoute) {
	if !perf_metrics_setting.GetSetting().Enabled || !slices.ContainsFunc(routes, func(r RoundRoute) bool { return r.ProbeCompletedAt > 0 }) {
		return
	}
	slices.SortFunc(routes, func(a, b RoundRoute) int { return cmp.Compare(a.ChannelID, b.ChannelID) })
	hash := sha256.New()
	for _, route := range routes {
		fmt.Fprintf(hash, "%d:%t:%t;", route.ChannelID, route.Success, route.ProbeCompletedAt > 0)
	}
	signature := hex.EncodeToString(hash.Sum(nil))
	ts := bucketStart(time.Now().Unix())
	channelMetricsMu.Lock()
	defer channelMetricsMu.Unlock()
	for _, route := range routes {
		source := sourceCachedRound
		if route.ProbeCompletedAt > 0 {
			source = sourceProbedRound
			stateKey := channelBucketKey{bucketKey: bucketKey{model: name, bucketTs: ts}, channelID: route.ChannelID, source: SourceRouteState}
			state, _ := channelBuckets.LoadOrStore(stateKey, &atomicBucket{})
			state.(*atomicBucket).add(Sample{Success: route.Success})
			// Keep the raw observation in its actual timing bucket and mark it
			// as covered by this round even when the completed round is later
			// or a newer request superseded the probe's outcome.
			physicalKey := channelBucketKey{bucketKey: bucketKey{model: name, bucketTs: bucketStart(route.ProbeCompletedAt / 1000)}, channelID: route.ChannelID, source: sourceCoveredProbe}
			covered, _ := channelBuckets.LoadOrStore(physicalKey, &atomicBucket{})
			covered.(*atomicBucket).add(Sample{Success: route.ProbeSuccess})
		}
		key := channelBucketKey{bucketKey: bucketKey{model: name, group: signature, bucketTs: ts}, channelID: route.ChannelID, source: source}
		bucket, _ := channelBuckets.LoadOrStore(key, &atomicBucket{})
		bucket.(*atomicBucket).add(Sample{Success: route.Success})
	}
}

type routeRound struct {
	observed        int
	repetitions     int64
	probed, success bool
}

type visibleBucket struct {
	performance, attempts, final        counters
	probes, states, covered, rawCovered map[int]counters
	rounds                              map[string]routeRound
}

type visibleScope struct {
	buckets map[int64]*visibleBucket
	current []currentAvailability
}

func (s *visibleScope) observe(k channelBucketKey, v counters) {
	if s.buckets == nil {
		s.buckets = make(map[int64]*visibleBucket)
	}
	b := s.buckets[k.bucketTs]
	if b == nil {
		b = &visibleBucket{}
		s.buckets[k.bucketTs] = b
	}
	switch k.source {
	case sourceTaskResult:
		b.performance = addCounterValues(b.performance, v)
		b.final = addCounterValues(b.final, v)
		b.attempts = addCounterValues(b.attempts, v)
	case sourceTaskAccepted:
		b.final.requestCount -= v.requestCount
		b.final.successCount -= v.successCount
		b.final.totalLatencyMs -= v.totalLatencyMs
	case sourceTaskAttempt:
		for _, counts := range []*counters{&b.attempts, &b.performance} {
			counts.requestCount -= v.requestCount
			counts.successCount -= v.successCount
			counts.totalLatencyMs -= v.totalLatencyMs
			counts.ttftSumMs -= v.ttftSumMs
			counts.ttftCount -= v.ttftCount
		}
	case SourceRequest:
		v.outputTokens, v.generationMs = 0, 0
		b.attempts = addCounterValues(b.attempts, v)
		b.performance = addCounterValues(b.performance, v)
	case SourceRelayResult:
		b.final = addCounterValues(b.final, v)
	case SourceUsage:
		b.performance.outputTokens += v.outputTokens
		b.performance.generationMs += v.generationMs
	case SourceProbe:
		if b.probes == nil {
			b.probes = map[int]counters{}
		}
		b.probes[k.channelID] = v
		b.performance = addCounterValues(b.performance, v)
	case SourceRouteState:
		if b.states == nil {
			b.states = map[int]counters{}
		}
		b.states[k.channelID] = v
	case sourceCoveredProbe:
		if b.rawCovered == nil {
			b.rawCovered = map[int]counters{}
		}
		b.rawCovered[k.channelID] = v
	case sourceProbedRound, sourceCachedRound:
		if b.rounds == nil {
			b.rounds = map[string]routeRound{}
		}
		r := b.rounds[k.group]
		r.observed++
		r.repetitions = max(r.repetitions, v.requestCount)
		if k.source == sourceProbedRound {
			r.probed = true
			if b.covered == nil {
				b.covered = map[int]counters{}
			}
			b.covered[k.channelID] = addCounterValues(b.covered[k.channelID], v)
		}
		r.success = r.success || v.successCount > 0
		b.rounds[k.group] = r
	}
}

// Legacy physical counters have no correlation ID. An OR is exact when every
// route failed, one always succeeded, or only one route has mixed outcomes.
// Never import an all-site success rate to fill an ambiguous bucket.
func (b *visibleBucket) probeAvailability(channelCount int) counters {
	var outcome counters
	if len(b.rounds) > 0 {
		for _, r := range b.rounds {
			if !r.probed || (!r.success && r.observed != channelCount) {
				continue
			}
			outcome.requestCount += r.repetitions
			if r.success {
				outcome.successCount += r.repetitions
			}
		}
	}
	// A rollout or a manual check may share a bucket with correlated rounds.
	// Subtract the route states recorded for those rounds before recovering
	// older physical observations, so neither side is dropped or duplicated.
	data := make(map[int]counters, len(b.probes)+len(b.states))
	for id, v := range b.probes {
		v.requestCount -= b.rawCovered[id].requestCount
		v.successCount -= b.rawCovered[id].successCount
		if v.requestCount > 0 {
			data[id] = v
		}
	}
	for id, v := range b.states {
		v.requestCount -= b.covered[id].requestCount
		v.successCount -= b.covered[id].successCount
		if v.requestCount > data[id].requestCount {
			data[id] = v
		}
	}
	var count, best, sum int64
	for _, v := range data {
		if count != 0 && v.requestCount != count {
			return outcome
		}
		count = v.requestCount
		best = max(best, v.successCount)
		sum += v.successCount
	}
	if best == count || (len(data) == channelCount && best == min(count, sum)) {
		outcome.requestCount += count
		outcome.successCount += best
	}
	return outcome
}

type visibleStats struct {
	performance, availability map[int64]counters
	total, outcomes, attempts counters
}

func (s *visibleScope) stats(start, end int64, channel bool) visibleStats {
	stats := visibleStats{performance: map[int64]counters{}, availability: map[int64]counters{}}
	for ts, b := range s.buckets {
		if ts < start || ts > end {
			continue
		}
		real := b.final
		if channel || real.requestCount == 0 {
			real = b.attempts
		}
		rounds := b.probeAvailability(len(s.current))
		availability := addCounterValues(rounds, real)
		if availability.requestCount > 0 {
			stats.availability[ts] = availability
		}
		perf := b.performance
		if b.attempts.requestCount == 0 && real.requestCount > 0 {
			perf = addCounterValues(perf, real)
		}
		stats.performance[ts] = perf
		stats.total = addCounterValues(stats.total, perf)
		stats.attempts = addCounterValues(stats.attempts, b.attempts)
		stats.outcomes = addCounterValues(stats.outcomes, availability)
	}
	return stats
}

type visibleModel struct {
	model    visibleScope
	groups   map[string]*visibleScope
	channels map[int]*visibleScope
	routes   map[int][]string
}

func loadVisibleMetrics(params QueryParams, hours int, detail bool) (map[string]*visibleModel, error) {
	models := map[string]*visibleModel{}
	if len(params.AllowedGroups) == 0 || len(params.VisibleModels) == 0 || (params.Group != "" && !slices.Contains(params.AllowedGroups, params.Group)) {
		return models, nil
	}
	var channels []model.Channel
	if err := model.DB.Select("id", "models", "group", "setting").Where("status = ?", common.ChannelStatusEnabled).Order("id").Find(&channels).Error; err != nil {
		return nil, err
	}
	visibleChannels := make([]model.Channel, 0, len(channels))
	var ids []int
	for _, ch := range channels {
		used := false
		for _, g := range ch.GetGroups() {
			if (params.Group != "" && g != params.Group) || !slices.Contains(params.AllowedGroups, g) {
				continue
			}
			for _, name := range ch.GetModelsForGroup(g) {
				name = strings.TrimSpace(name)
				if !params.VisibleModels[name] || (params.Model != "" && params.Model != name) {
					continue
				}
				m := models[name]
				if m == nil {
					m = &visibleModel{routes: map[int][]string{}, groups: map[string]*visibleScope{}, channels: map[int]*visibleScope{}}
					models[name] = m
				}
				if slices.Contains(m.routes[ch.Id], g) {
					continue
				}
				m.routes[ch.Id] = append(m.routes[ch.Id], g)
				used = true
			}
		}
		if used {
			ids = append(ids, ch.Id)
			visibleChannels = append(visibleChannels, ch)
		}
	}
	if len(ids) == 0 {
		return models, nil
	}
	states, err := queryCurrentAvailability(visibleChannels)
	if err != nil {
		return nil, err
	}
	for name, m := range models {
		for id, groups := range m.routes {
			state := states[id][name]
			m.model.current = append(m.model.current, state)
			if detail {
				m.channels[id] = &visibleScope{current: []currentAvailability{state}}
				for _, g := range groups {
					if m.groups[g] == nil {
						m.groups[g] = &visibleScope{}
					}
					m.groups[g].current = append(m.groups[g].current, state)
				}
			}
		}
	}
	rows, err := queryChannelBucketsForRoutes(params.Model, ids, nil, hours, SourceRequest, SourceRelayResult, SourceUsage, SourceProbe, SourceRouteState, sourceProbedRound, sourceCachedRound, sourceCoveredProbe, sourceTaskResult, sourceTaskAttempt, sourceTaskAccepted)
	if err != nil {
		return nil, err
	}
	for k, v := range rows {
		m := models[k.model]
		if m == nil || len(m.routes[k.channelID]) == 0 {
			continue
		}
		groups := m.routes[k.channelID]
		switch k.source {
		case SourceProbe, SourceRouteState, sourceCoveredProbe:
			if k.group != "" {
				continue
			}
		case sourceProbedRound, sourceCachedRound:
			if k.group == "" {
				continue
			}
		default:
			if !slices.Contains(groups, k.group) {
				continue
			}
			groups = []string{k.group}
		}
		m.model.observe(k, v)
		if detail {
			m.channels[k.channelID].observe(k, v)
			for _, g := range groups {
				m.groups[g].observe(k, v)
			}
		}
	}
	return models, nil
}

func QueryVisible(params QueryParams) (QueryResult, error) {
	start, end := queryWindow(time.Now(), params.Hours)
	result := QueryResult{ModelName: params.Model, SeriesSchema: seriesSchema, WindowStart: start, WindowEnd: end, Groups: []GroupResult{}, Channels: []ChannelResult{}, Series: []BucketPoint{}}
	models, err := loadVisibleMetrics(params, params.Hours, true)
	if err != nil {
		return result, err
	}
	m := models[params.Model]
	if m == nil {
		return result, nil
	}
	stats := m.model.stats(start, end, false)
	result.Summary = summarize(stats.total)
	if result.Summary != nil {
		result.AvgLatencyMs, result.AvgTps = result.Summary.AvgLatencyMs, result.Summary.AvgTps
		if stats.outcomes.requestCount > 0 {
			result.Summary.SuccessRate = successRate(stats.outcomes)
		}
	}
	for ts, v := range stats.performance {
		if v.requestCount > 0 {
			p := bucketPoint(ts, v)
			if o := stats.availability[ts]; o.requestCount > 0 {
				p.SuccessRate = successRate(o)
			}
			result.Series = append(result.Series, p)
		}
	}
	slices.SortFunc(result.Series, func(a, b BucketPoint) int { return cmp.Compare(a.Ts, b.Ts) })
	state := modelCurrentAvailability(m.model.current)
	result.CurrentAvailable, result.CurrentObservedAt = state.available, state.observedAt
	result.AvailabilityRate, result.AvailabilitySeries = mergeAvailabilityHistory(stats.availability)
	for g, s := range m.groups {
		stats := s.stats(start, end, false)
		values := map[bucketKey]counters{}
		for ts, v := range stats.performance {
			values[bucketKey{model: params.Model, group: g, bucketTs: ts}] = v
		}
		group := GroupResult{Group: g, Series: []BucketPoint{}}
		if built := buildQueryResult(params.Model, values); len(built.Groups) > 0 {
			group = built.Groups[0]
		}
		group.AvailabilityRate, group.AvailabilitySeries = mergeAvailabilityHistory(stats.availability)
		if group.AvailabilityRate != nil {
			group.SuccessRate = *group.AvailabilityRate
			for i := range group.Series {
				if v := stats.availability[group.Series[i].Ts]; v.requestCount > 0 {
					group.Series[i].SuccessRate = successRate(v)
				}
			}
		}
		result.Groups = append(result.Groups, group)
	}
	slices.SortFunc(result.Groups, func(a, b GroupResult) int { return cmp.Compare(a.Group, b.Group) })
	ids := make([]int, 0, len(m.channels))
	for id := range m.channels {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for i, id := range ids {
		s := m.channels[id]
		stats := s.stats(start, end, true)
		state := s.current[0]
		rate, history := mergeAvailabilityHistory(stats.availability)
		result.Channels = append(result.Channels, ChannelResult{ChannelIndex: i + 1, CurrentAvailable: state.available, CurrentObservedAt: state.observedAt, SuccessRate: ratePointer(stats.attempts), AvailabilityRate: rate, Series: history, AvgLatencyMs: avg(stats.total.totalLatencyMs, stats.total.requestCount), AvgTps: avgTps(stats.total)})
	}
	return result, nil
}

func QueryVisibleSummary(params QueryParams, recentModels ...string) (SummaryAllResult, error) {
	if len(recentModels) > MaxRecentModels {
		return SummaryAllResult{}, common.NewMessage("Invalid parameters")
	}
	hours := params.Hours
	if hours <= 0 {
		hours = 24
	}
	readHours := hours
	if len(recentModels) > 0 {
		readHours = max(hours, recentSuccessHistoryHours)
	}
	models, err := loadVisibleMetrics(params, readHours, false)
	if err != nil {
		return SummaryAllResult{}, err
	}
	now := time.Now()
	start, end := queryWindow(now, hours)
	recentStart, _ := queryWindow(now, recentSuccessHistoryHours)
	result := SummaryAllResult{WindowStart: start, WindowEnd: end, Models: []ModelSummary{}, RecentSuccessSeries: map[string][]SuccessRatePoint{}}
	var total, outcomes counters
	for name, m := range models {
		stats := m.model.stats(start, end, false)
		total = addCounterValues(total, stats.total)
		outcomes = addCounterValues(outcomes, stats.outcomes)
		item := modelPerformanceSummary(name, stats.performance)
		if stats.outcomes.requestCount > 0 {
			item.SuccessRate = math.Round(successRate(stats.outcomes)*100) / 100
		}
		item.AvailabilityRate, item.AvailabilitySeries = mergeAvailabilityHistory(stats.availability)
		item.RecentSuccessSeries = item.AvailabilitySeries
		state := modelCurrentAvailability(m.model.current)
		item.CurrentAvailable, item.CurrentObservedAt = state.available, state.observedAt
		if item.RequestCount > 0 || item.AvailabilityRate != nil || item.CurrentObservedAt != 0 {
			result.Models = append(result.Models, item)
		}
		if slices.Contains(recentModels, name) {
			history := recentSuccessSeries(m.model.stats(recentStart, end, false).availability)
			if len(history) > 0 {
				result.RecentSuccessSeries[name] = history[max(0, len(history)-recentSuccessSampleLimit):]
			}
		}
	}
	slices.SortFunc(result.Models, func(a, b ModelSummary) int {
		if a.RequestCount == b.RequestCount {
			return cmp.Compare(a.ModelName, b.ModelName)
		}
		return cmp.Compare(b.RequestCount, a.RequestCount)
	})
	result.Summary = summarize(total)
	if result.Summary != nil && outcomes.requestCount > 0 {
		result.Summary.SuccessRate = math.Round(successRate(outcomes)*100) / 100
	}
	return result, nil
}
