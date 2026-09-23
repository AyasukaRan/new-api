package model

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PerfMetric stores aggregated relay performance metrics for the model square.
type PerfMetric struct {
	Id             int    `json:"id" gorm:"primaryKey"`
	ModelName      string `json:"model_name" gorm:"size:128;uniqueIndex:idx_perf_model_group_bucket,priority:1"`
	Group          string `json:"group" gorm:"column:group;size:64;uniqueIndex:idx_perf_model_group_bucket,priority:2"`
	BucketTs       int64  `json:"bucket_ts" gorm:"uniqueIndex:idx_perf_model_group_bucket,priority:3;index:idx_perf_bucket_ts"`
	RequestCount   int64  `json:"-" gorm:"default:0"`
	SuccessCount   int64  `json:"-" gorm:"default:0"`
	TotalLatencyMs int64  `json:"-" gorm:"default:0"`
	TtftSumMs      int64  `json:"-" gorm:"default:0"`
	TtftCount      int64  `json:"-" gorm:"default:0"`
	OutputTokens   int64  `json:"-" gorm:"default:0"`
	GenerationMs   int64  `json:"-" gorm:"default:0"`
}

func (PerfMetric) TableName() string {
	return "perf_metrics"
}

func UpsertPerfMetric(metric *PerfMetric) error {
	if metric == nil || metric.RequestCount == 0 {
		return nil
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "model_name"},
			{Name: "group"},
			{Name: "bucket_ts"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"request_count":    gorm.Expr("perf_metrics.request_count + ?", metric.RequestCount),
			"success_count":    gorm.Expr("perf_metrics.success_count + ?", metric.SuccessCount),
			"total_latency_ms": gorm.Expr("perf_metrics.total_latency_ms + ?", metric.TotalLatencyMs),
			"ttft_sum_ms":      gorm.Expr("perf_metrics.ttft_sum_ms + ?", metric.TtftSumMs),
			"ttft_count":       gorm.Expr("perf_metrics.ttft_count + ?", metric.TtftCount),
			"output_tokens":    gorm.Expr("perf_metrics.output_tokens + ?", metric.OutputTokens),
			"generation_ms":    gorm.Expr("perf_metrics.generation_ms + ?", metric.GenerationMs),
		}),
	}).Create(metric).Error
}

func GetPerfMetrics(modelName string, group string, startTs int64, endTs int64) ([]PerfMetric, error) {
	var metrics []PerfMetric
	query := DB.Model(&PerfMetric{}).
		Where("model_name = ? AND bucket_ts >= ? AND bucket_ts <= ?", modelName, startTs, endTs)
	if group != "" {
		query = query.Where(commonGroupCol+" = ?", group)
	}
	err := query.Order("bucket_ts ASC").Find(&metrics).Error
	return metrics, err
}

type PerfMetricSummary struct {
	ModelName      string `json:"model_name"`
	RequestCount   int64  `json:"request_count"`
	SuccessCount   int64  `json:"success_count"`
	TotalLatencyMs int64  `json:"total_latency_ms"`
	OutputTokens   int64  `json:"output_tokens"`
	GenerationMs   int64  `json:"generation_ms"`
}

type PerfMetricSummaryBucket struct {
	ModelName      string `json:"model_name"`
	BucketTs       int64  `json:"bucket_ts"`
	RequestCount   int64  `json:"request_count"`
	SuccessCount   int64  `json:"success_count"`
	TotalLatencyMs int64  `json:"total_latency_ms"`
	OutputTokens   int64  `json:"output_tokens"`
	GenerationMs   int64  `json:"generation_ms"`
}

func GetPerfMetricsSummaryAll(startTs int64, endTs int64, groups []string) ([]PerfMetricSummary, error) {
	var summaries []PerfMetricSummary
	query := DB.Model(&PerfMetric{}).
		Select("model_name, SUM(request_count) as request_count, SUM(success_count) as success_count, SUM(total_latency_ms) as total_latency_ms, SUM(output_tokens) as output_tokens, SUM(generation_ms) as generation_ms").
		Where("bucket_ts >= ? AND bucket_ts <= ?", startTs, endTs)
	if groups != nil {
		if len(groups) == 0 {
			return summaries, nil
		}
		query = query.Where(commonGroupCol+" IN ?", groups)
	}
	err := query.
		Group("model_name").
		Having("SUM(request_count) > 0").
		Find(&summaries).Error
	return summaries, err
}

func GetPerfMetricsSummaryBucketsAll(startTs int64, endTs int64, groups []string) ([]PerfMetricSummaryBucket, error) {
	var summaries []PerfMetricSummaryBucket
	query := DB.Model(&PerfMetric{}).
		Select("model_name, bucket_ts, SUM(request_count) as request_count, SUM(success_count) as success_count, SUM(total_latency_ms) as total_latency_ms, SUM(output_tokens) as output_tokens, SUM(generation_ms) as generation_ms").
		Where("bucket_ts >= ? AND bucket_ts <= ?", startTs, endTs)
	if groups != nil {
		if len(groups) == 0 {
			return summaries, nil
		}
		query = query.Where(commonGroupCol+" IN ?", groups)
	}
	err := query.
		Group("model_name, bucket_ts").
		Having("SUM(request_count) > 0").
		Order("bucket_ts ASC").
		Find(&summaries).Error
	return summaries, err
}

func DeletePerfMetricsBefore(cutoffTs int64) error {
	if cutoffTs <= 0 {
		return nil
	}
	return DB.Where("bucket_ts < ?", cutoffTs).Delete(&PerfMetric{}).Error
}

func PerfMetricStartTime(hours int) int64 {
	if hours <= 0 {
		hours = 24
	}
	return time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
}

// ChannelPerfMetric keeps channel observations separate from legacy aggregate
// metrics. Availability rows have channel_id=0 and count observed test rounds,
// while request and probe rows describe one actual channel.
type ChannelPerfMetric struct {
	Id             int    `json:"-" gorm:"primaryKey"`
	ModelName      string `json:"-" gorm:"size:128;uniqueIndex:idx_channel_perf_bucket,priority:1"`
	Group          string `json:"-" gorm:"column:group;size:64;uniqueIndex:idx_channel_perf_bucket,priority:2"`
	ChannelID      int    `json:"-" gorm:"uniqueIndex:idx_channel_perf_bucket,priority:3;index:idx_channel_perf_channel"`
	Source         string `json:"-" gorm:"size:16;uniqueIndex:idx_channel_perf_bucket,priority:4"`
	BucketTs       int64  `json:"-" gorm:"uniqueIndex:idx_channel_perf_bucket,priority:5;index:idx_channel_perf_ts"`
	RequestCount   int64  `json:"-" gorm:"default:0"`
	SuccessCount   int64  `json:"-" gorm:"default:0"`
	TotalLatencyMs int64  `json:"-" gorm:"default:0"`
	TtftSumMs      int64  `json:"-" gorm:"default:0"`
	TtftCount      int64  `json:"-" gorm:"default:0"`
	InputTokens    int64  `json:"-" gorm:"default:0"`
	OutputTokens   int64  `json:"-" gorm:"default:0"`
	GenerationMs   int64  `json:"-" gorm:"default:0"`
	UsedQuota      int64  `json:"-" gorm:"default:0"`
}

func UpsertChannelPerfMetric(metric *ChannelPerfMetric) error {
	if metric == nil || metric.RequestCount <= 0 {
		return nil
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "model_name"}, {Name: "group"}, {Name: "channel_id"}, {Name: "source"}, {Name: "bucket_ts"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"request_count":    gorm.Expr("channel_perf_metrics.request_count + ?", metric.RequestCount),
			"success_count":    gorm.Expr("channel_perf_metrics.success_count + ?", metric.SuccessCount),
			"total_latency_ms": gorm.Expr("channel_perf_metrics.total_latency_ms + ?", metric.TotalLatencyMs),
			"ttft_sum_ms":      gorm.Expr("channel_perf_metrics.ttft_sum_ms + ?", metric.TtftSumMs),
			"ttft_count":       gorm.Expr("channel_perf_metrics.ttft_count + ?", metric.TtftCount),
			"input_tokens":     gorm.Expr("channel_perf_metrics.input_tokens + ?", metric.InputTokens),
			"output_tokens":    gorm.Expr("channel_perf_metrics.output_tokens + ?", metric.OutputTokens),
			"generation_ms":    gorm.Expr("channel_perf_metrics.generation_ms + ?", metric.GenerationMs),
			"used_quota":       gorm.Expr("channel_perf_metrics.used_quota + ?", metric.UsedQuota),
		}),
	}).Create(metric).Error
}

func GetChannelPerfMetrics(modelName string, channelID int, groups []string, startTs, endTs int64, sources ...string) ([]ChannelPerfMetric, error) {
	rows := make([]ChannelPerfMetric, 0)
	query := DB.Where("bucket_ts >= ? AND bucket_ts <= ?", startTs, endTs)
	if modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	if len(sources) > 0 {
		query = query.Where("source IN ?", sources)
	}
	if groups != nil {
		if len(groups) == 0 {
			return rows, nil
		}
		query = query.Where(commonGroupCol+" IN ?", groups)
	}
	err := query.Order("bucket_ts ASC").Find(&rows).Error
	return rows, err
}

func DeleteChannelPerfMetricsBefore(cutoffTs int64) error {
	if cutoffTs <= 0 {
		return nil
	}
	return DB.Where("bucket_ts < ?", cutoffTs).Delete(&ChannelPerfMetric{}).Error
}

// ChannelRecordedUsage is the net consumption represented by retained billing
// logs, including asynchronous adjustments and refunds on the log database.
// Billing records are not request counts: a task or realtime connection can
// produce several settlement records.
type ChannelRecordedUsage struct {
	BillingRecordCount   int64 `json:"billing_record_count"`
	RecordedUsedQuota    int64 `json:"recorded_used_quota"`
	RecordedInputTokens  int64 `json:"recorded_input_tokens"`
	RecordedOutputTokens int64 `json:"recorded_output_tokens"`
}

func GetChannelRecordedUsage(channelID int, startTs, endTs int64) (ChannelRecordedUsage, error) {
	var usage ChannelRecordedUsage
	if channelID <= 0 {
		return usage, nil
	}
	err := applyLogSourceFilter(LOG_DB.Model(&Log{})).
		Select("COUNT(*) AS billing_record_count, COALESCE(SUM(CASE WHEN type = ? THEN -quota ELSE quota END), 0) AS recorded_used_quota, COALESCE(SUM(prompt_tokens), 0) AS recorded_input_tokens, COALESCE(SUM(completion_tokens), 0) AS recorded_output_tokens", LogTypeRefund).
		Where("channel_id = ? AND created_at >= ? AND created_at <= ? AND type IN ?", channelID, startTs, endTs, []int{LogTypeConsume, LogTypeRefund}).
		Scan(&usage).Error
	return usage, err
}
