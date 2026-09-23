package perfmetrics

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

const (
	SourceRequest         = "request"
	SourceRelayResult     = "relay_result"
	SourceUsage           = "usage"
	SourceProbe           = "probe"
	SourceRouteState      = "route_state"
	SourceAvailability    = "availability"
	SourceAvailabilityAll = "availability_all"
)

// ChannelResult deliberately excludes internal channel IDs, names and usage.
// Route numbers are assigned within each model's currently configured channels.
// The admin channel monitor has a separate authenticated usage response.
type ChannelResult struct {
	CurrentAvailable  *bool              `json:"current_available,omitempty"`
	CurrentObservedAt int64              `json:"current_observed_at,omitempty"`
	ChannelIndex      int                `json:"channel_index"`
	SuccessRate       *float64           `json:"success_rate,omitempty"`
	AvailabilityRate  *float64           `json:"availability_rate,omitempty"`
	AvgLatencyMs      int64              `json:"avg_latency_ms"`
	AvgTps            float64            `json:"avg_tps"`
	Series            []SuccessRatePoint `json:"series"`
}

type ChannelUsageResult struct {
	model.ChannelRecordedUsage
	ChannelID        int                `json:"channel_id"`
	Hours            int                `json:"hours"`
	RequestCount     int64              `json:"request_count"`
	SuccessCount     int64              `json:"success_count"`
	SuccessRate      *float64           `json:"success_rate,omitempty"`
	ProbeCount       int64              `json:"probe_count"`
	AvailabilityRate *float64           `json:"availability_rate,omitempty"`
	InputTokens      int64              `json:"input_tokens"`
	OutputTokens     int64              `json:"output_tokens"`
	UsedQuota        int64              `json:"used_quota"`
	AvgLatencyMs     int64              `json:"avg_latency_ms"`
	Series           []SuccessRatePoint `json:"series"`
}

type channelBucketKey struct {
	bucketKey
	channelID int
	source    string
}

var channelBuckets sync.Map

// Serializes the persistence handoff with reads so a drained bucket is never
// omitted or counted twice while an API response combines SQL and memory.
var channelMetricsMu sync.RWMutex

func RecordChannelSample(sample Sample, source string) {
	if !perf_metrics_setting.GetSetting().Enabled || sample.Model == "" {
		return
	}
	if source != SourceRelayResult && source != SourceRequest && source != SourceUsage && source != SourceProbe && source != SourceRouteState && source != SourceAvailability && source != SourceAvailabilityAll {
		return
	}
	if source != SourceAvailability && source != SourceAvailabilityAll && sample.ChannelID <= 0 {
		return
	}
	if sample.Group == "" && (source == SourceRelayResult || source == SourceRequest || source == SourceUsage || source == SourceAvailability) {
		sample.Group = "default"
	}
	if source == SourceRouteState {
		// A monitoring round observes each physical channel/model once,
		// independently of how many routing groups contain it.
		sample.Group = ""
	}
	if sample.OutputTokens > 0 && sample.GenerationMs <= 0 {
		sample.GenerationMs = 1
	}
	key := channelBucketKey{
		bucketKey: bucketKey{model: sample.Model, group: sample.Group, bucketTs: bucketStart(time.Now().Unix())},
		channelID: sample.ChannelID,
		source:    source,
	}
	channelMetricsMu.RLock()
	defer channelMetricsMu.RUnlock()
	actual, _ := channelBuckets.LoadOrStore(key, &atomicBucket{})
	actual.(*atomicBucket).add(sample)
}

// FlushChannelMetrics also flushes current buckets. A completed health-check
// round is made durable immediately rather than waiting until the next hour.
func FlushChannelMetrics() error {
	channelMetricsMu.Lock()
	defer channelMetricsMu.Unlock()
	var firstErr error
	channelBuckets.Range(func(rawKey, rawBucket any) bool {
		key := rawKey.(channelBucketKey)
		bucket := rawBucket.(*atomicBucket)
		value := bucket.drain()
		if value.requestCount == 0 {
			channelBuckets.Delete(rawKey)
			return true
		}
		err := model.UpsertChannelPerfMetric(&model.ChannelPerfMetric{
			ModelName: key.model, Group: key.group, ChannelID: key.channelID, Source: key.source, BucketTs: key.bucketTs,
			RequestCount: value.requestCount, SuccessCount: value.successCount, TotalLatencyMs: value.totalLatencyMs,
			TtftSumMs: value.ttftSumMs, TtftCount: value.ttftCount, InputTokens: value.inputTokens,
			OutputTokens: value.outputTokens, GenerationMs: value.generationMs, UsedQuota: value.usedQuota,
		})
		if err != nil {
			bucket.addCounters(value)
			if firstErr == nil {
				firstErr = err
			}
			return true
		}
		channelBuckets.Delete(rawKey)
		return true
	})
	return firstErr
}

func queryChannelBuckets(modelName string, channelID int, groups []string, hours int, sources ...string) (map[channelBucketKey]counters, error) {
	startTs, endTs := queryWindow(time.Now(), hours)
	allowed := allowedGroupSet(groups)
	allowedSources := allowedGroupSet(sources)
	channelMetricsMu.RLock()
	defer channelMetricsMu.RUnlock()
	rows, err := model.GetChannelPerfMetrics(modelName, channelID, groups, startTs, endTs, sources...)
	if err != nil {
		return nil, err
	}
	merged := make(map[channelBucketKey]counters, len(rows))
	for _, row := range rows {
		key := channelBucketKey{bucketKey: bucketKey{model: row.ModelName, group: row.Group, bucketTs: row.BucketTs}, channelID: row.ChannelID, source: row.Source}
		merged[key] = counters{requestCount: row.RequestCount, successCount: row.SuccessCount, totalLatencyMs: row.TotalLatencyMs, ttftSumMs: row.TtftSumMs, ttftCount: row.TtftCount, inputTokens: row.InputTokens, outputTokens: row.OutputTokens, generationMs: row.GenerationMs, usedQuota: row.UsedQuota}
	}
	channelBuckets.Range(func(rawKey, rawBucket any) bool {
		key := rawKey.(channelBucketKey)
		if len(allowedSources) > 0 {
			if _, ok := allowedSources[key.source]; !ok {
				return true
			}
		}
		if key.bucketTs < startTs || key.bucketTs > endTs || (modelName != "" && key.model != modelName) || (channelID > 0 && key.channelID != channelID) {
			return true
		}
		if allowed != nil {
			if _, ok := allowed[key.group]; !ok {
				return true
			}
		}
		value := rawBucket.(*atomicBucket).snapshot()
		merged[key] = addCounterValues(merged[key], value)
		return true
	})
	return merged, nil
}

func addCounterValues(a, b counters) counters {
	return counters{
		requestCount: a.requestCount + b.requestCount, successCount: a.successCount + b.successCount,
		totalLatencyMs: a.totalLatencyMs + b.totalLatencyMs, ttftSumMs: a.ttftSumMs + b.ttftSumMs, ttftCount: a.ttftCount + b.ttftCount,
		inputTokens: a.inputTokens + b.inputTokens, outputTokens: a.outputTokens + b.outputTokens, generationMs: a.generationMs + b.generationMs, usedQuota: a.usedQuota + b.usedQuota,
	}
}

func ratePointer(value counters) *float64 {
	if value.requestCount <= 0 {
		return nil
	}
	rate := math.Round(successRate(value)*100) / 100
	return &rate
}

func QueryChannelUsage(channelID, hours int) (ChannelUsageResult, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	rows, err := queryChannelBuckets("", channelID, nil, hours)
	if err != nil {
		return ChannelUsageResult{}, err
	}
	endTs := time.Now().Unix()
	recorded, err := model.GetChannelRecordedUsage(channelID, endTs-int64(hours)*3600, endTs)
	if err != nil {
		return ChannelUsageResult{}, err
	}
	requests, probes, usage := counters{}, counters{}, counters{}
	series := map[int64]counters{}
	type routeModel struct {
		channelID int
		model     string
	}
	histories := map[routeModel]map[string]map[int64]counters{}
	// The empty probe group is the channel total; public copies are scoped to
	// configured groups and must not multiply the admin's probe count.
	for key, value := range rows {
		switch key.source {
		case SourceRequest:
			requests = addCounterValues(requests, value)
		case SourceUsage:
			usage = addCounterValues(usage, value)
			continue
		case SourceProbe:
			if key.group != "" {
				continue
			}
			probes = addCounterValues(probes, value)
		case SourceRouteState:
			if key.group != "" {
				continue
			}
		default:
			continue
		}
		route := routeModel{key.channelID, key.model}
		if histories[route] == nil {
			histories[route] = map[string]map[int64]counters{}
		}
		if histories[route][key.source] == nil {
			histories[route][key.source] = map[int64]counters{}
		}
		histories[route][key.source][key.bucketTs] = addCounterValues(histories[route][key.source][key.bucketTs], value)
	}
	// Combine physical observations per model before combining channel totals.
	// Legacy route states only fill hours without real requests or probes.
	for _, sources := range histories {
		selected := selectAvailabilityHistory(sources[SourceRouteState], combineAvailabilityObservations(sources[SourceRequest], sources[SourceProbe]))
		for ts, value := range selected {
			series[ts] = addCounterValues(series[ts], value)
		}
	}
	availability, history := mergeAvailabilityHistory(series)
	return ChannelUsageResult{ChannelRecordedUsage: recorded, ChannelID: channelID, Hours: hours, RequestCount: requests.requestCount, SuccessCount: requests.successCount, SuccessRate: ratePointer(requests), ProbeCount: probes.requestCount, AvailabilityRate: availability, InputTokens: usage.inputTokens, OutputTokens: usage.outputTokens, UsedQuota: usage.usedQuota, AvgLatencyMs: avg(requests.totalLatencyMs, requests.requestCount), Series: history}, nil
}

// selectAvailabilityHistory selects exactly one source per displayed hour.
// Later histories take precedence, while hours absent from them retain their
// earlier observations. Counters within the selected hour stay weighted by
// observation count instead of averaging percentages.
func selectAvailabilityHistory(histories ...map[int64]counters) map[int64]counters {
	selected := make(map[int64]counters)
	for _, history := range histories {
		preferred := make(map[int64]counters)
		for ts, value := range history {
			if value.requestCount <= 0 {
				continue
			}
			hour := ts - ts%3600
			preferred[hour] = addCounterValues(preferred[hour], value)
		}
		for ts, value := range preferred {
			selected[ts] = value
		}
	}
	return selected
}

func mergeAvailabilityHistory(histories ...map[int64]counters) (*float64, []SuccessRatePoint) {
	selected := selectAvailabilityHistory(histories...)
	total := counters{}
	for _, value := range selected {
		total = addCounterValues(total, value)
	}
	return ratePointer(total), recentSuccessSeries(selected)
}

// combineAvailabilityObservations adds distinct observations in each hour.
// Requests and physical probes must not overwrite each other's outcomes.
func combineAvailabilityObservations(histories ...map[int64]counters) map[int64]counters {
	combined := map[int64]counters{}
	for _, history := range histories {
		for ts, value := range history {
			if value.requestCount > 0 {
				hour := ts - ts%3600
				combined[hour] = addCounterValues(combined[hour], value)
			}
		}
	}
	return combined
}

// Model rounds already combine physical probes with OR across channels. Add
// final real requests to those rounds instead of losing them to precedence.
// Legacy hours already contain request samples; retain them without adding
// those requests twice. Final outcomes also cover formats without legacy usage.
func modelAvailabilityHistory(legacy, rounds, relays map[int64]counters) map[int64]counters {
	selected := selectAvailabilityHistory(relays, legacy, rounds)
	relayHours := combineAvailabilityObservations(relays)
	for ts := range combineAvailabilityObservations(rounds) {
		selected[ts] = addCounterValues(selected[ts], relayHours[ts])
	}
	return selected
}

type currentAvailability struct {
	available  *bool
	observedAt int64
}

func visibleCurrentChannels(channels []model.Channel, modelName, group string, groups []string) []model.Channel {
	allowed := allowedGroupSet(groups)
	visible := []model.Channel{}
	for _, channel := range channels {
		configured := modelName == ""
		for _, name := range channel.GetModels() {
			configured = configured || strings.TrimSpace(name) == modelName
		}
		if !configured {
			continue
		}
		for _, routeGroup := range channel.GetGroups() {
			if group != "" && routeGroup != group {
				continue
			}
			if _, ok := allowed[routeGroup]; allowed != nil && !ok {
				continue
			}
			visible = append(visible, channel)
			break
		}
	}
	return visible
}

// Current status uses only fresh completed observations, independently of
// historical percentages. Expired or missing observations remain unknown.
func queryCurrentAvailability(channels []model.Channel) (map[int]map[string]currentAvailability, error) {
	states := map[int]map[string]currentAvailability{}
	if len(channels) == 0 {
		return states, nil
	}
	ids := make([]int, 0, len(channels))
	for _, channel := range channels {
		ids = append(ids, channel.Id)
	}
	rows, err := model.GetChannelModelActivities(ids)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	cutoff := now - operation_setting.ChannelTestInterval().Milliseconds()
	for _, row := range rows {
		if row.LastResultAt <= 0 || row.LastResultAt < cutoff || row.LastResultAt > now {
			continue
		}
		if states[row.ChannelID] == nil {
			states[row.ChannelID] = map[string]currentAvailability{}
		}
		success := row.LastResultSuccess
		states[row.ChannelID][row.ModelName] = currentAvailability{available: &success, observedAt: row.LastResultAt / 1000}
	}
	// Only read configurations and observations for channels already selected by
	// the caller's visibility rules. Credentials stay inside this calculation;
	// public results carry only the boolean and observation time.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var observations []model.ChannelKeyObservation
	if err := model.DB.WithContext(ctx).Select("id", "channel_id", "model_name", "observed_at", "success").Where("channel_id IN ?", ids).Find(&observations).Error; err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return states, nil
	}
	byID := make(map[string]model.ChannelKeyObservation, len(observations))
	observedModels := map[int]map[string]bool{}
	for _, row := range observations {
		byID[row.ID] = row
		if observedModels[row.ChannelID] == nil {
			observedModels[row.ChannelID] = map[string]bool{}
		}
		observedModels[row.ChannelID][row.ModelName] = true
	}
	var configured []model.Channel
	if err := model.DB.WithContext(ctx).Where("id IN ?", ids).Find(&configured).Error; err != nil {
		return nil, err
	}
	for _, channel := range configured {
		if states[channel.Id] == nil {
			states[channel.Id] = map[string]currentAvailability{}
		}
		for modelName := range observedModels[channel.Id] {
			// Old-config rows still suppress the activity fallback. Otherwise a
			// credential replacement could inherit its predecessor's success.
			states[channel.Id][modelName] = currentChannelKeyAvailability(&channel, modelName, byID, now)
		}
	}
	return states, nil
}

func modelCurrentAvailability(channels []currentAvailability) currentAvailability {
	result := currentAvailability{}
	unknown := len(channels) == 0
	available := false
	for _, channel := range channels {
		result.observedAt = max(result.observedAt, channel.observedAt)
		if channel.available == nil {
			unknown = true
		} else if *channel.available {
			available = true
		}
	}
	if available || !unknown {
		result.available = &available
	}
	return result
}

// performanceObservations separates request timing from settlement throughput.
// A physical probe contributes to each group that can use its channel, but only
// once to the model total. Cached availability rounds carry no performance.
func performanceObservations(channels []model.Channel, rows map[channelBucketKey]counters, group string, allowedGroups map[string]struct{}, startTs int64) (map[bucketKey]counters, map[string]map[int64]counters) {
	routes := map[int]map[string][]string{}
	for _, channel := range channels {
		groups := []string{}
		seen := map[string]bool{}
		for _, routeGroup := range channel.GetGroups() {
			if group != "" && routeGroup != group {
				continue
			}
			if _, allowed := allowedGroups[routeGroup]; allowedGroups != nil && !allowed {
				continue
			}
			if !seen[routeGroup] {
				groups = append(groups, routeGroup)
				seen[routeGroup] = true
			}
		}
		if len(groups) == 0 {
			continue
		}
		routes[channel.Id] = map[string][]string{}
		for _, name := range channel.GetModels() {
			if name = strings.TrimSpace(name); name != "" {
				routes[channel.Id][name] = groups
			}
		}
	}
	grouped := map[bucketKey]counters{}
	models := map[string]map[int64]counters{}
	for key, value := range rows {
		if key.bucketTs < startTs || key.channelID <= 0 {
			continue
		}
		var groups []string
		switch key.source {
		case SourceProbe:
			if key.group != "" {
				continue
			}
			groups = routes[key.channelID][key.model]
			if len(groups) == 0 {
				continue
			}
		case SourceRequest, SourceUsage:
			if group != "" && key.group != group {
				continue
			}
			if _, allowed := allowedGroups[key.group]; allowedGroups != nil && !allowed {
				continue
			}
			groups = []string{key.group}
			if key.source == SourceRequest {
				// Settlement is authoritative for tokens. Legacy request copies
				// must not duplicate the same throughput a second time.
				value.outputTokens, value.generationMs = 0, 0
			} else {
				value = counters{outputTokens: value.outputTokens, generationMs: value.generationMs}
			}
		default:
			continue
		}
		for _, routeGroup := range groups {
			bucket := bucketKey{model: key.model, group: routeGroup, bucketTs: key.bucketTs}
			grouped[bucket] = addCounterValues(grouped[bucket], value)
		}
		if models[key.model] == nil {
			models[key.model] = map[int64]counters{}
		}
		models[key.model][key.bucketTs] = addCounterValues(models[key.model][key.bucketTs], value)
	}
	return grouped, models
}

// Timing observations replace their legacy bucket. A settlement without a
// request in that bucket replaces only throughput, retaining legacy outcomes
// and avoiding double-counting tokens already present in the legacy aggregate.
func performanceBucket(legacy, observation counters) counters {
	if observation.requestCount > 0 {
		return observation
	}
	if observation.outputTokens > 0 && observation.generationMs > 0 {
		legacy.outputTokens, legacy.generationMs = observation.outputTokens, observation.generationMs
	}
	return legacy
}

// Older buckets with no physical observations survive. Usage-only buckets
// contribute throughput without inventing request counts or success outcomes.
func performanceHistory(legacy, observations map[int64]counters) map[int64]counters {
	merged := make(map[int64]counters, len(legacy)+len(observations))
	for ts, value := range legacy {
		merged[ts] = value
	}
	for ts, value := range observations {
		merged[ts] = performanceBucket(merged[ts], value)
	}
	return merged
}

func attachModelAvailability(result *QueryResult, params QueryParams, legacy map[bucketKey]counters, startTs int64) error {
	rows, err := queryChannelBuckets(params.Model, 0, nil, params.Hours)
	if err != nil {
		return err
	}
	groupSeries := map[string]map[int64]counters{}
	allowedGroups := allowedGroupSet(params.AllowedGroups)
	legacyGroups := map[string]map[int64]counters{}
	legacyOverall := map[int64]counters{}
	for key, value := range legacy {
		if allowedGroups != nil {
			if _, allowed := allowedGroups[key.group]; !allowed {
				continue
			}
		}
		if legacyGroups[key.group] == nil {
			legacyGroups[key.group] = map[int64]counters{}
		}
		legacyGroups[key.group][key.bucketTs] = addCounterValues(legacyGroups[key.group][key.bucketTs], value)
		legacyOverall[key.bucketTs] = addCounterValues(legacyOverall[key.bucketTs], value)
	}
	type routeKey struct {
		channelID int
		group     string
	}
	routeRequests := map[routeKey]counters{}
	physicalRequestSeries := map[int]map[int64]counters{}
	channelProbes := map[int]counters{}
	routeUsage := map[routeKey]counters{}
	channelProbeSeries := map[int]map[int64]counters{}
	channelStateSeries := map[int]map[int64]counters{}
	overallSeries := map[int64]counters{}
	relaySeries := map[int64]counters{}
	relayGroups := map[string]map[int64]counters{}
	for key, value := range rows {
		if key.source == SourceRelayResult {
			if key.bucketTs < startTs || value.requestCount <= 0 {
				continue
			}
			if allowedGroups != nil {
				if _, allowed := allowedGroups[key.group]; !allowed {
					continue
				}
			}
			relaySeries[key.bucketTs] = addCounterValues(relaySeries[key.bucketTs], value)
			if relayGroups[key.group] == nil {
				relayGroups[key.group] = map[int64]counters{}
			}
			relayGroups[key.group][key.bucketTs] = addCounterValues(relayGroups[key.group][key.bucketTs], value)
			continue
		}
		if key.source == SourceAvailabilityAll {
			if key.bucketTs < startTs || key.channelID != 0 || key.group != "" {
				continue
			}
			overallSeries[key.bucketTs] = addCounterValues(overallSeries[key.bucketTs], value)
			continue
		}
		if key.source == SourceProbe || key.source == SourceRouteState {
			// Physical probes and route-state observations are independent of
			// groups. Legacy per-group copies cannot reweight their history.
			if key.group != "" {
				continue
			}
			series := channelStateSeries
			if key.source == SourceProbe {
				channelProbes[key.channelID] = addCounterValues(channelProbes[key.channelID], value)
				series = channelProbeSeries
			}
			if series[key.channelID] == nil {
				series[key.channelID] = map[int64]counters{}
			}
			series[key.channelID][key.bucketTs] = addCounterValues(series[key.channelID][key.bucketTs], value)
			continue
		}
		if key.source == SourceRequest {
			_, allowed := allowedGroups[key.group]
			if allowedGroups == nil || allowed {
				if physicalRequestSeries[key.channelID] == nil {
					physicalRequestSeries[key.channelID] = map[int64]counters{}
				}
				physicalRequestSeries[key.channelID][key.bucketTs] = addCounterValues(physicalRequestSeries[key.channelID][key.bucketTs], value)
			}
		}
		if params.Group != "" && key.group != params.Group {
			continue
		}
		if key.source == SourceAvailability {
			if key.bucketTs < startTs || key.channelID != 0 {
				continue
			}
			if allowedGroups != nil {
				if _, allowed := allowedGroups[key.group]; !allowed {
					continue
				}
			}
			if groupSeries[key.group] == nil {
				groupSeries[key.group] = map[int64]counters{}
			}
			groupSeries[key.group][key.bucketTs] = addCounterValues(groupSeries[key.group][key.bucketTs], value)
			continue
		}
		route := routeKey{key.channelID, key.group}
		if key.source == SourceRequest {
			routeRequests[route] = addCounterValues(routeRequests[route], value)
		}
		if key.source == SourceUsage {
			routeUsage[route] = addCounterValues(routeUsage[route], value)
		}
	}
	// Select only public configuration fields. Never load a channel key here.
	var channels []model.Channel
	if err := model.DB.Select("id", "models", "group", "status").Where("status = ?", common.ChannelStatusEnabled).Order("id ASC").Find(&channels).Error; err != nil {
		return err
	}
	groupPerformance, modelPerformance := performanceObservations(channels, rows, params.Group, allowedGroups, startTs)
	for key, value := range legacy {
		if _, allowed := allowedGroups[key.group]; allowedGroups != nil && !allowed {
			continue
		}
		groupPerformance[key] = performanceBucket(value, groupPerformance[key])
	}
	result.Groups = buildQueryResult(params.Model, groupPerformance).Groups
	// Timing uses physical request/probe observations without group copies.
	// Request success still describes the final outcome after retries.
	requestSummary := result.Summary
	totalPerformance := counters{}
	result.Series = []BucketPoint{}
	for ts, value := range performanceHistory(legacyOverall, modelPerformance[params.Model]) {
		totalPerformance = addCounterValues(totalPerformance, value)
		if value.requestCount > 0 {
			point := bucketPoint(ts, value)
			if final := legacyOverall[ts]; final.requestCount > 0 {
				point.SuccessRate = successRate(final)
			}
			result.Series = append(result.Series, point)
		}
	}
	sort.Slice(result.Series, func(i, j int) bool { return result.Series[i].Ts < result.Series[j].Ts })
	result.Summary = summarize(totalPerformance)
	if requestSummary != nil && result.Summary != nil {
		result.Summary.SuccessRate = requestSummary.SuccessRate
	}
	result.AvgLatencyMs, result.AvgTps = avg(totalPerformance.totalLatencyMs, totalPerformance.requestCount), math.Round(avgTps(totalPerformance)*100)/100
	currentStates, err := queryCurrentAvailability(visibleCurrentChannels(channels, params.Model, params.Group, params.AllowedGroups))
	if err != nil {
		return err
	}
	modelStates := []currentAvailability{}
	result.Channels = []ChannelResult{}
	index := 0
	for _, channel := range channels {
		configured := false
		for _, name := range channel.GetModels() {
			if strings.TrimSpace(name) == params.Model {
				configured = true
				break
			}
		}
		if !configured {
			continue
		}
		index++
		visibleGroups := make(map[string]bool)
		request, throughput := counters{}, counters{}
		for _, group := range channel.GetGroups() {
			if params.Group != "" && group != params.Group {
				continue
			}
			if allowedGroups != nil {
				if _, allowed := allowedGroups[group]; !allowed {
					continue
				}
			}
			if visibleGroups[group] {
				continue
			}
			visibleGroups[group] = true
			route := routeKey{channel.Id, group}
			request = addCounterValues(request, routeRequests[route])
			throughput = addCounterValues(throughput, routeUsage[route])
		}
		if len(visibleGroups) == 0 {
			continue
		}
		probe := channelProbes[channel.Id]
		performance := addCounterValues(request, probe)
		throughput = addCounterValues(throughput, probe)
		availability, history := mergeAvailabilityHistory(channelStateSeries[channel.Id], combineAvailabilityObservations(physicalRequestSeries[channel.Id], channelProbeSeries[channel.Id]))
		current := currentStates[channel.Id][params.Model]
		modelStates = append(modelStates, current)
		result.Channels = append(result.Channels, ChannelResult{CurrentAvailable: current.available, CurrentObservedAt: current.observedAt, ChannelIndex: index, SuccessRate: ratePointer(request), AvailabilityRate: availability, AvgLatencyMs: avg(performance.totalLatencyMs, performance.requestCount), AvgTps: avgTps(throughput), Series: history})
	}
	current := modelCurrentAvailability(modelStates)
	result.CurrentAvailable, result.CurrentObservedAt = current.available, current.observedAt
	visibleModel := len(result.Channels) > 0 || len(relaySeries) > 0
	for _, value := range legacyOverall {
		if value.requestCount > 0 {
			visibleModel = true
			break
		}
	}
	if visibleModel {
		result.AvailabilityRate, result.AvailabilitySeries = mergeAvailabilityHistory(modelAvailabilityHistory(legacyOverall, overallSeries, relaySeries))
	}
	if params.Group != "" {
		result.AvailabilityRate, result.AvailabilitySeries = mergeAvailabilityHistory(modelAvailabilityHistory(legacyGroups[params.Group], groupSeries[params.Group], relayGroups[params.Group]))
	}
	seenGroups := map[string]bool{}
	for i := range result.Groups {
		group := &result.Groups[i]
		seenGroups[group.Group] = true
		group.AvailabilityRate, group.AvailabilitySeries = mergeAvailabilityHistory(modelAvailabilityHistory(legacyGroups[group.Group], groupSeries[group.Group], relayGroups[group.Group]))
	}
	for group, series := range groupSeries {
		if !seenGroups[group] {
			rate, history := mergeAvailabilityHistory(modelAvailabilityHistory(legacyGroups[group], series, relayGroups[group]))
			result.Groups = append(result.Groups, GroupResult{Group: group, Series: []BucketPoint{}, AvailabilityRate: rate, AvailabilitySeries: history})
		}
	}
	sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].Group < result.Groups[j].Group })
	return nil
}

func attachSummaryAvailability(result *SummaryAllResult, hours int, groups []string, legacy map[string]map[int64]counters, startTs int64) error {
	if groups != nil && len(groups) == 0 {
		return nil
	}
	allowedGroups := allowedGroupSet(groups)
	var channels []model.Channel
	if err := model.DB.Select("id", "models", "group").Where("status = ?", common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return err
	}
	currentStates, err := queryCurrentAvailability(visibleCurrentChannels(channels, "", "", groups))
	if err != nil {
		return err
	}
	modelStates := map[string][]currentAvailability{}
	visibleModels := make(map[string]bool)
	// Allowed historical observations keep a model visible after its current
	// channels are disabled or moved, just as they do in the detail response.
	for _, item := range result.Models {
		visibleModels[item.ModelName] = true
	}
	for _, channel := range channels {
		visible := allowedGroups == nil
		for _, group := range channel.GetGroups() {
			if _, ok := allowedGroups[group]; ok {
				visible = true
			}
		}
		if !visible {
			continue
		}
		for _, name := range channel.GetModels() {
			if name = strings.TrimSpace(name); name != "" {
				visibleModels[name] = true
				modelStates[name] = append(modelStates[name], currentStates[channel.Id][name])
			}
		}
	}
	if groups != nil {
		groups = append(append([]string{}, groups...), "")
	}
	rows, err := queryChannelBuckets("", 0, groups, hours, SourceAvailabilityAll, SourceRelayResult, SourceRequest, SourceUsage, SourceProbe)
	if err != nil {
		return err
	}
	_, modelPerformance := performanceObservations(channels, rows, "", allowedGroups, startTs)
	// Authorized final request outcomes preserve visibility even after a
	// channel is removed, including task formats with no legacy usage rows.
	for key, value := range rows {
		if key.source == SourceRelayResult && key.bucketTs >= startTs && value.requestCount > 0 {
			visibleModels[key.model] = true
		}
	}
	series := map[string]map[int64]counters{}
	relays := map[string]map[int64]counters{}
	for key, value := range rows {
		if key.source == SourceRelayResult && visibleModels[key.model] && key.bucketTs >= startTs {
			if relays[key.model] == nil {
				relays[key.model] = map[int64]counters{}
			}
			relays[key.model][key.bucketTs] = addCounterValues(relays[key.model][key.bucketTs], value)
			continue
		}
		if key.source != SourceAvailabilityAll || !visibleModels[key.model] || key.bucketTs < startTs || key.channelID != 0 || key.group != "" {
			continue
		}
		if series[key.model] == nil {
			series[key.model] = map[int64]counters{}
		}
		series[key.model][key.bucketTs] = addCounterValues(series[key.model][key.bucketTs], value)
	}
	allPerformance := counters{}
	for name := range visibleModels {
		for _, value := range performanceHistory(legacy[name], modelPerformance[name]) {
			allPerformance = addCounterValues(allPerformance, value)
		}
	}
	requestSummary := result.Summary
	result.Summary = summarize(allPerformance)
	if requestSummary != nil && result.Summary != nil {
		result.Summary.SuccessRate = requestSummary.SuccessRate
	}
	seen := map[string]bool{}
	for i := range result.Models {
		item := &result.Models[i]
		seen[item.ModelName] = true
		performance := modelPerformanceSummary(item.ModelName, performanceHistory(legacy[item.ModelName], modelPerformance[item.ModelName]))
		item.AvgLatencyMs, item.AvgTps = performance.AvgLatencyMs, performance.AvgTps
		item.AvailabilityRate, item.AvailabilitySeries = mergeAvailabilityHistory(modelAvailabilityHistory(legacy[item.ModelName], series[item.ModelName], relays[item.ModelName]))
		current := modelCurrentAvailability(modelStates[item.ModelName])
		item.CurrentAvailable, item.CurrentObservedAt = current.available, current.observedAt
	}
	for name := range visibleModels {
		if seen[name] {
			continue
		}
		current := modelCurrentAvailability(modelStates[name])
		rate, history := mergeAvailabilityHistory(modelAvailabilityHistory(legacy[name], series[name], relays[name]))
		item := modelPerformanceSummary(name, performanceHistory(legacy[name], modelPerformance[name]))
		if rate == nil && current.available == nil {
			continue
		}
		item.CurrentAvailable, item.CurrentObservedAt = current.available, current.observedAt
		item.AvailabilityRate, item.AvailabilitySeries = rate, history
		result.Models = append(result.Models, item)
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].RequestCount > result.Models[j].RequestCount })
	return nil
}
