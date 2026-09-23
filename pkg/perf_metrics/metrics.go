package perfmetrics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

var hotBuckets sync.Map

// Protects the SQL/memory handoff so a flush cannot hide or double-count a
// bucket in queries, or discard a sample added while that bucket is removed.
var metricsMu sync.RWMutex

// seriesSchema is a stable client cache/schema marker. Do not change it when
// hiding fields or making response-only privacy hardening changes.
const seriesSchema = "dbcd0a3c01b55203"

func Init() {
	go flushLoop()
}

// RecordRelayResult samples one finished relay exactly once at the request
// boundary and publishes the final availability result after all retries.
func RecordRelayResult(ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) {
	outcome := ClassifyRelayOutcome(ctx, info, apiErr)
	if outcome == OutcomeIgnored {
		return
	}
	Record(RelaySample(info, outcome == OutcomeSuccess, info.PerformanceOutputTokens))
	RecordChannelRelayResult(info, outcome == OutcomeSuccess)
}

func RecordRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) {
	if info == nil {
		return
	}
	Record(RelaySample(info, success, outputTokens))
}

// RelayObservation applies the same health classification to each upstream
// attempt as the final request. A nil observation does not change availability.
func RelayObservation(ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) *bool {
	outcome := ClassifyRelayOutcome(ctx, info, apiErr)
	if outcome == OutcomeIgnored {
		return nil
	}
	return common.GetPointer(outcome == OutcomeSuccess)
}

// RecordChannelRelayResult records one final upstream outcome after retries. Billing
// samples can be asynchronous or repeated during settlement, so callers must
// record this at the relay boundary, never at a settlement call site.
func RecordChannelRelayResult(info *relaycommon.RelayInfo, success bool) {
	if info == nil || info.IsChannelTest {
		return
	}
	RecordChannelSample(RelaySample(info, success, 0), SourceRelayResult)
}

// RecordChannelUsage observes settlement data without counting a new request.
// It runs at the settlement call site, before a retry can change RelayInfo.
func RecordChannelUsage(info *relaycommon.RelayInfo, inputTokens, outputTokens, usedQuota int64) {
	if info == nil || info.IsChannelTest {
		return
	}
	sample := ChannelAttemptSample(info, info.GetChannelID(), true, time.Now())
	sample.InputTokens = inputTokens
	sample.OutputTokens = outputTokens
	sample.UsedQuota = usedQuota
	RecordChannelSample(sample, SourceUsage)
}

func RecordChannelAttempt(info *relaycommon.RelayInfo, channelID int, success bool) {
	if info == nil || info.IsChannelTest {
		return
	}
	RecordChannelSample(ChannelAttemptSample(info, channelID, success, time.Now()), SourceRequest)
}

func ChannelAttemptSample(info *relaycommon.RelayInfo, channelID int, success bool, completedAt time.Time) Sample {
	startedAt := info.ChannelAttemptStartTime
	if startedAt.IsZero() {
		startedAt = info.StartTime
	}
	latencyMs := max(int64(0), completedAt.Sub(startedAt).Milliseconds())
	sample := Sample{Model: info.OriginModelName, Group: info.UsingGroup, ChannelID: channelID, Success: success, LatencyMs: latencyMs, GenerationMs: latencyMs}
	if info.IsStream && info.HasSendResponse() && !info.FirstResponseTime.Before(startedAt) {
		sample.HasTtft = true
		sample.TtftMs = info.FirstResponseTime.Sub(startedAt).Milliseconds()
		sample.GenerationMs = max(int64(0), completedAt.Sub(info.FirstResponseTime).Milliseconds())
	}
	return sample
}

func RelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) Sample {
	now := time.Now()
	hasTtft := info.IsStream && info.HasSendResponse()
	ttftMs := int64(0)
	if hasTtft {
		ttftMs = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
	}
	latencyMs := now.Sub(info.StartTime).Milliseconds()
	generationMs := latencyMs
	if hasTtft {
		generationMs = now.Sub(info.FirstResponseTime).Milliseconds()
	}
	if generationMs <= 0 {
		generationMs = latencyMs
	}
	return Sample{
		Model:        info.OriginModelName,
		Group:        info.UsingGroup,
		ChannelID:    info.GetChannelID(),
		LatencyMs:    latencyMs,
		TtftMs:       ttftMs,
		HasTtft:      hasTtft,
		Success:      success,
		OutputTokens: outputTokens,
		GenerationMs: generationMs,
	}
}

func Record(sample Sample) {
	setting := perf_metrics_setting.GetSetting()
	if !setting.Enabled || sample.Model == "" {
		return
	}
	if sample.Group == "" {
		sample.Group = "default"
	}
	if sample.LatencyMs < 0 {
		sample.LatencyMs = 0
	}

	key := bucketKey{
		model:    sample.Model,
		group:    sample.Group,
		bucketTs: bucketStart(time.Now().Unix()),
	}
	metricsMu.RLock()
	actual, _ := hotBuckets.LoadOrStore(key, &atomicBucket{})
	actual.(*atomicBucket).add(sample)
	metricsMu.RUnlock()
	gopool.Go(func() {
		recordRedis(key, sample)
	})
}

// queryWindow covers the current partial hour plus the preceding complete
// hours, so every reader shares the same hourly buckets.
func queryWindow(now time.Time, hours int) (int64, int64) {
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, 24*30)
	endTs := now.Unix()
	return endTs - endTs%3600 - int64(hours-1)*3600, endTs
}

func Query(params QueryParams) (QueryResult, error) {
	startTs, endTs := queryWindow(time.Now(), params.Hours)
	allowedGroups := allowedGroupSet(params.AllowedGroups)

	merged := map[bucketKey]counters{}
	metricsMu.RLock()
	rows, err := model.GetPerfMetrics(params.Model, params.Group, startTs, endTs)
	if err != nil {
		metricsMu.RUnlock()
		return QueryResult{}, err
	}
	for _, row := range rows {
		if allowedGroups != nil {
			if _, ok := allowedGroups[row.Group]; !ok {
				continue
			}
		}
		mergeCounters(merged, bucketKey{
			model:    row.ModelName,
			group:    row.Group,
			bucketTs: row.BucketTs,
		}, counters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			ttftSumMs:      row.TtftSumMs,
			ttftCount:      row.TtftCount,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		})
	}

	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.model != params.Model || k.bucketTs < startTs || k.bucketTs > endTs {
			return true
		}
		if params.Group != "" && k.group != params.Group {
			return true
		}
		if allowedGroups != nil {
			if _, ok := allowedGroups[k.group]; !ok {
				return true
			}
		}
		mergeCounters(merged, k, value.(*atomicBucket).snapshot())
		return true
	})
	metricsMu.RUnlock()

	result := buildQueryResult(params.Model, merged)
	if err := attachModelAvailability(&result, params, merged, startTs); err != nil {
		return QueryResult{}, err
	}
	result.WindowStart, result.WindowEnd = startTs, endTs
	return result, nil
}

func QuerySummaryAll(hours int, groups []string) (SummaryAllResult, error) {
	startTs, endTs := queryWindow(time.Now(), hours)
	allowedGroups := allowedGroupSet(groups)

	metricsMu.RLock()
	rows, err := model.GetPerfMetricsSummaryBucketsAll(startTs, endTs, groups)
	if err != nil {
		metricsMu.RUnlock()
		return SummaryAllResult{}, err
	}

	modelBuckets := map[string]map[int64]counters{}
	for _, row := range rows {
		value := counters{
			requestCount:   row.RequestCount,
			successCount:   row.SuccessCount,
			totalLatencyMs: row.TotalLatencyMs,
			outputTokens:   row.OutputTokens,
			generationMs:   row.GenerationMs,
		}
		mergeModelBucket(modelBuckets, row.ModelName, row.BucketTs, value)
	}

	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs < startTs || k.bucketTs > endTs {
			return true
		}
		if allowedGroups != nil {
			if _, ok := allowedGroups[k.group]; !ok {
				return true
			}
		}
		snap := value.(*atomicBucket).snapshot()
		if snap.requestCount == 0 {
			return true
		}
		mergeModelBucket(modelBuckets, k.model, k.bucketTs, snap)
		return true
	})
	metricsMu.RUnlock()

	all := counters{}
	models := make([]ModelSummary, 0, len(modelBuckets))
	for name, buckets := range modelBuckets {
		for _, value := range buckets {
			all = addCounterValues(all, value)
		}
		item := modelPerformanceSummary(name, buckets)
		if item.RequestCount > 0 {
			models = append(models, item)
		}
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].RequestCount > models[j].RequestCount
	})

	result := SummaryAllResult{Summary: summarize(all), WindowStart: startTs, WindowEnd: endTs, Models: models}
	if err := attachSummaryAvailability(&result, hours, groups, modelBuckets, startTs); err != nil {
		return SummaryAllResult{}, err
	}
	return result, nil
}

func modelPerformanceSummary(modelName string, buckets map[int64]counters) ModelSummary {
	total := counters{}
	for _, value := range buckets {
		total = addCounterValues(total, value)
	}
	return ModelSummary{
		ModelName:           modelName,
		AvgLatencyMs:        avg(total.totalLatencyMs, total.requestCount),
		SuccessRate:         math.Round(successRate(total)*100) / 100,
		AvgTps:              math.Round(avgTps(total)*100) / 100,
		RecentSuccessSeries: recentSuccessSeries(buckets),
		RequestCount:        total.requestCount,
	}
}

func mergeModelBucket(modelBuckets map[string]map[int64]counters, modelName string, bucketTs int64, value counters) {
	if value.requestCount == 0 {
		return
	}
	if _, ok := modelBuckets[modelName]; !ok {
		modelBuckets[modelName] = map[int64]counters{}
	}
	current := modelBuckets[modelName][bucketTs]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	modelBuckets[modelName][bucketTs] = current
}

func recentSuccessSeries(buckets map[int64]counters) []SuccessRatePoint {
	if len(buckets) == 0 {
		return nil
	}
	hourly := map[int64]counters{}
	for ts, value := range buckets {
		hourTs := ts - ts%3600
		merged := hourly[hourTs]
		merged.requestCount += value.requestCount
		merged.successCount += value.successCount
		hourly[hourTs] = merged
	}
	timestamps := make([]int64, 0, len(hourly))
	for hourTs, value := range hourly {
		if value.requestCount == 0 {
			continue
		}
		timestamps = append(timestamps, hourTs)
	}
	if len(timestamps) == 0 {
		return nil
	}
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i] < timestamps[j]
	})
	points := make([]SuccessRatePoint, 0, len(timestamps))
	for _, hourTs := range timestamps {
		points = append(points, SuccessRatePoint{
			Ts:          hourTs,
			SuccessRate: math.Round(successRate(hourly[hourTs])*100) / 100,
		})
	}
	return points
}

func allowedGroupSet(groups []string) map[string]struct{} {
	if groups == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		allowed[group] = struct{}{}
	}
	return allowed
}

func bucketStart(ts int64) int64 {
	bucketSeconds := perf_metrics_setting.GetBucketSeconds()
	if bucketSeconds <= 0 {
		bucketSeconds = 3600
	}
	return ts - (ts % bucketSeconds)
}

func mergeCounters(merged map[bucketKey]counters, key bucketKey, value counters) {
	if value.requestCount == 0 {
		return
	}
	current := merged[key]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	merged[key] = current
}

func buildQueryResult(modelName string, merged map[bucketKey]counters) QueryResult {
	groupBuckets := map[string]map[int64]counters{}
	modelBuckets := map[string]map[int64]counters{}
	for key, value := range merged {
		if value.requestCount == 0 && (value.outputTokens <= 0 || value.generationMs <= 0) {
			continue
		}
		if _, ok := groupBuckets[key.group]; !ok {
			groupBuckets[key.group] = map[int64]counters{}
		}
		groupBuckets[key.group][key.bucketTs] = value
		mergeModelBucket(modelBuckets, modelName, key.bucketTs, value)
	}

	groups := make([]string, 0, len(groupBuckets))
	for group := range groupBuckets {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	all := counters{}
	results := make([]GroupResult, 0, len(groups))
	for _, group := range groups {
		buckets := groupBuckets[group]
		timestamps := make([]int64, 0, len(buckets))
		for ts := range buckets {
			timestamps = append(timestamps, ts)
		}
		sort.Slice(timestamps, func(i, j int) bool {
			return timestamps[i] < timestamps[j]
		})

		total := counters{}
		series := make([]BucketPoint, 0, len(timestamps))
		for _, ts := range timestamps {
			value := buckets[ts]
			total.requestCount += value.requestCount
			total.successCount += value.successCount
			total.totalLatencyMs += value.totalLatencyMs
			total.ttftSumMs += value.ttftSumMs
			total.ttftCount += value.ttftCount
			total.outputTokens += value.outputTokens
			total.generationMs += value.generationMs
			if value.requestCount > 0 {
				series = append(series, bucketPoint(ts, value))
			}
		}
		// Settlements can finish in a later bucket than their request, but
		// settlement data alone cannot create a monitored group or outcome.
		if total.requestCount == 0 {
			continue
		}
		all.requestCount += total.requestCount
		all.successCount += total.successCount
		all.totalLatencyMs += total.totalLatencyMs
		all.outputTokens += total.outputTokens
		all.generationMs += total.generationMs

		results = append(results, GroupResult{
			Group:        group,
			AvgTtftMs:    avg(total.ttftSumMs, total.ttftCount),
			AvgLatencyMs: avg(total.totalLatencyMs, total.requestCount),
			SuccessRate:  successRate(total),
			AvgTps:       avgTps(total),
			Series:       series,
		})
	}

	modelSeries := make([]BucketPoint, 0, len(modelBuckets[modelName]))
	for ts, value := range modelBuckets[modelName] {
		modelSeries = append(modelSeries, bucketPoint(ts, value))
	}
	sort.Slice(modelSeries, func(i, j int) bool {
		return modelSeries[i].Ts < modelSeries[j].Ts
	})

	return QueryResult{
		ModelName:    modelName,
		SeriesSchema: seriesSchema,
		Summary:      summarize(all),
		Series:       modelSeries,
		Groups:       results,
	}
}

// summarize returns nil when nothing was sampled, so clients can show
// "no data" instead of a fabricated 0% success rate.
func summarize(total counters) *Summary {
	if total.requestCount <= 0 {
		return nil
	}
	return &Summary{
		AvgLatencyMs: avg(total.totalLatencyMs, total.requestCount),
		SuccessRate:  math.Round(successRate(total)*100) / 100,
		AvgTps:       math.Round(avgTps(total)*100) / 100,
	}
}

func bucketPoint(ts int64, value counters) BucketPoint {
	return BucketPoint{
		Ts:           ts,
		AvgTtftMs:    avg(value.ttftSumMs, value.ttftCount),
		AvgLatencyMs: avg(value.totalLatencyMs, value.requestCount),
		SuccessRate:  successRate(value),
		AvgTps:       avgTps(value),
	}
}

func avg(sum int64, count int64) int64 {
	if count <= 0 {
		return 0
	}
	return sum / count
}

func successRate(value counters) float64 {
	if value.requestCount <= 0 {
		return 0
	}
	return float64(value.successCount) / float64(value.requestCount) * 100
}

func avgTps(value counters) float64 {
	if value.outputTokens <= 0 || value.generationMs <= 0 {
		return 0
	}
	return float64(value.outputTokens) / (float64(value.generationMs) / 1000)
}

func recordRedis(key bucketKey, sample Sample) {
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	redisKey := redisBucketKey(key)
	pipe := common.RDB.TxPipeline()
	pipe.HIncrBy(ctx, redisKey, "req", 1)
	if sample.Success {
		pipe.HIncrBy(ctx, redisKey, "ok", 1)
	}
	if sample.LatencyMs > 0 {
		pipe.HIncrBy(ctx, redisKey, "lat", sample.LatencyMs)
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		pipe.HIncrBy(ctx, redisKey, "ttft", sample.TtftMs)
		pipe.HIncrBy(ctx, redisKey, "ttft_n", 1)
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		pipe.HIncrBy(ctx, redisKey, "out", sample.OutputTokens)
		pipe.HIncrBy(ctx, redisKey, "gen_ms", sample.GenerationMs)
	}
	pipe.Expire(ctx, redisKey, time.Hour)
	_, _ = pipe.Exec(ctx)
}

func mergeRedisActiveBuckets(merged map[bucketKey]counters, params QueryParams, startTs int64, endTs int64) {
	if !common.RedisEnabled || common.RDB == nil || params.Model == "" || params.Group == "" {
		return
	}
	active := bucketStart(time.Now().Unix())
	if active < startTs || active > endTs {
		return
	}
	key := bucketKey{model: params.Model, group: params.Group, bucketTs: active}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	values, err := common.RDB.HGetAll(ctx, redisBucketKey(key)).Result()
	if err != nil || len(values) == 0 {
		return
	}
	mergeCounters(merged, key, redisCounters(values))
}

func redisBucketKey(key bucketKey) string {
	return fmt.Sprintf("perf:%s:%s:%d", key.model, key.group, key.bucketTs)
}
