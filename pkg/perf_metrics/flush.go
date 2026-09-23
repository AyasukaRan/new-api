package perfmetrics

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

func flushLoop() {
	for {
		interval := perf_metrics_setting.GetFlushIntervalMinutes()
		time.Sleep(time.Duration(interval) * time.Minute)
		setting := perf_metrics_setting.GetSetting()
		if !setting.Enabled {
			continue
		}
		if err := Flush(); err != nil {
			common.SysError("failed to flush performance metrics: " + err.Error())
		}
		cleanupExpiredMetrics(setting.RetentionDays)
	}
}

// Flush persists both aggregate and channel observations, including the current
// bucket. Failed writes remain buffered so a later flush can retry them.
func Flush() error {
	return errors.Join(flushAggregateMetrics(), FlushChannelMetrics())
}

func flushAggregateMetrics() error {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	var firstErr error
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)

		bucket := value.(*atomicBucket)
		drained := bucket.drain()
		if drained.requestCount == 0 {
			hotBuckets.Delete(key)
			return true
		}

		err := model.UpsertPerfMetric(&model.PerfMetric{
			ModelName:      k.model,
			Group:          k.group,
			BucketTs:       k.bucketTs,
			RequestCount:   drained.requestCount,
			SuccessCount:   drained.successCount,
			TotalLatencyMs: drained.totalLatencyMs,
			TtftSumMs:      drained.ttftSumMs,
			TtftCount:      drained.ttftCount,
			OutputTokens:   drained.outputTokens,
			GenerationMs:   drained.generationMs,
		})
		if err != nil {
			bucket.addCounters(drained)
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to flush perf metric bucket model=%s group=%s bucket=%d: %w", k.model, k.group, k.bucketTs, err)
			}
			return true
		}

		hotBuckets.Delete(key)
		return true
	})
	return firstErr
}

func cleanupExpiredMetrics(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	if err := model.DeletePerfMetricsBefore(cutoff); err != nil {
		common.SysError("failed to cleanup expired perf metrics: " + err.Error())
	}
	if err := model.DeleteChannelPerfMetricsBefore(cutoff); err != nil {
		common.SysError("failed to cleanup expired channel perf metrics: " + err.Error())
	}
}

func redisCounters(values map[string]string) counters {
	return counters{
		requestCount:   parseRedisInt(values["req"]),
		successCount:   parseRedisInt(values["ok"]),
		totalLatencyMs: parseRedisInt(values["lat"]),
		ttftSumMs:      parseRedisInt(values["ttft"]),
		ttftCount:      parseRedisInt(values["ttft_n"]),
		outputTokens:   parseRedisInt(values["out"]),
		generationMs:   parseRedisInt(values["gen_ms"]),
	}
}

func parseRedisInt(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}
