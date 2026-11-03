package perfmetrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyRelayOutcome(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  *types.NewAPIError
		want Outcome
	}{
		{"success", context.Background(), nil, OutcomeSuccess},
		{"upstream bad request", context.Background(), types.InitOpenAIError("unknown", 400), OutcomeIgnored},
		{"upstream credentials returned as 400", context.Background(), types.InitOpenAIError("invalid_api_key", 400), OutcomeFailure},
		{"wrapped credentials keep their cause", context.Background(), types.NewErrorWithStatusCode(types.InitOpenAIError("invalid_api_key", 400), types.ErrorCodeInvalidRequest, 400), OutcomeFailure},
		{"upstream context limit returned as 500", context.Background(), types.InitOpenAIError("context_length_exceeded", 500), OutcomeIgnored},
		{"local rate limit", context.Background(), types.NewErrorWithStatusCode(errors.New("limited"), types.ErrorCodeInvalidRequest, 429), OutcomeIgnored},
		{"upstream rate limit", context.Background(), types.InitOpenAIError("rate_limit_exceeded", 429), OutcomeFailure},
		{"local quota", context.Background(), types.NewError(errors.New("quota"), types.ErrorCodeInsufficientUserQuota), OutcomeIgnored},
		{"local violation fee", context.Background(), types.NewError(errors.New("csam"), types.ErrorCodeViolationFeeGrokCSAM), OutcomeIgnored},
		{"upstream quota", context.Background(), types.InitOpenAIError("insufficient_quota", 429), OutcomeFailure},
		{"upstream gateway quota", context.Background(), types.InitOpenAIError(types.ErrorCodeInsufficientUserQuota, 403), OutcomeFailure},
		{"unavailable channel", context.Background(), types.NewErrorWithStatusCode(errors.New("disabled"), types.ErrorCodeGetChannelFailed, 403), OutcomeFailure},
		{"empty upstream response", context.Background(), types.NewError(errors.New("empty"), types.ErrorCodeEmptyResponse), OutcomeFailure},
		{"network failure", context.Background(), types.NewOpenAIError(errors.New("connection refused"), types.ErrorCodeDoRequestFailed, 500), OutcomeFailure},
		{"connection closed after non-stream response", canceled, nil, OutcomeSuccess},
		{"client cancellation", canceled, types.NewOpenAIError(errors.New("context canceled"), types.ErrorCodeDoRequestFailed, 500), OutcomeIgnored},
		{"upstream deadline", context.Background(), types.NewOpenAIError(context.DeadlineExceeded, types.ErrorCodeDoRequestFailed, 504), OutcomeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ClassifyRelayOutcome(tc.ctx, &relaycommon.RelayInfo{}, tc.err))
		})
	}
	assert.Equal(t, OutcomeIgnored, ClassifyRelayOutcome(context.Background(), &relaycommon.RelayInfo{PerformanceBusinessRejection: true}, nil))
}

func TestStreamOutcomeClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark func(*relaycommon.StreamStatus)
		end  relaycommon.StreamEndReason
		want Outcome
	}{
		{"completed", (*relaycommon.StreamStatus).MarkCompleted, relaycommon.StreamEndReasonEOF, OutcomeSuccess},
		{"business rejection", func(s *relaycommon.StreamStatus) { s.MarkFailed("context_length_exceeded", "", 0) }, relaycommon.StreamEndReasonEOF, OutcomeIgnored},
		{"service error", func(s *relaycommon.StreamStatus) { s.MarkFailed("server_error", "", 0) }, relaycommon.StreamEndReasonEOF, OutcomeFailure},
		{"error after completion", func(s *relaycommon.StreamStatus) { s.MarkCompleted(); s.MarkFailed("", "server_error", 0) }, relaycommon.StreamEndReasonEOF, OutcomeFailure},
		{"output limit", func(s *relaycommon.StreamStatus) { s.MarkIncomplete("max_output_tokens") }, relaycommon.StreamEndReasonEOF, OutcomeSuccess},
		{"content filter", func(s *relaycommon.StreamStatus) { s.MarkIncomplete("content_filter") }, relaycommon.StreamEndReasonEOF, OutcomeIgnored},
		{"client cancel", (*relaycommon.StreamStatus).MarkCancelled, relaycommon.StreamEndReasonEOF, OutcomeIgnored},
		{"client gone", nil, relaycommon.StreamEndReasonClientGone, OutcomeIgnored},
		{"timeout", nil, relaycommon.StreamEndReasonTimeout, OutcomeFailure},
		{"missing terminal", nil, relaycommon.StreamEndReasonEOF, OutcomeFailure},
		{"done marker without terminal", nil, relaycommon.StreamEndReasonDone, OutcomeSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := relaycommon.NewStreamStatus()
			stream.RequireTerminal()
			if tc.mark != nil {
				tc.mark(stream)
			}
			stream.SetEndReason(tc.end, nil)
			assert.Equal(t, tc.want, ClassifyRelayOutcome(context.Background(), &relaycommon.RelayInfo{StreamStatus: stream}, nil))
		})
	}
}

func TestClientCancellationDuringUpstreamRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := relaycommon.NewStreamStatus()
	stream.RequireTerminal()
	stream.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("reader closed"))
	assert.Equal(t, OutcomeIgnored, ClassifyRelayOutcome(ctx, &relaycommon.RelayInfo{StreamStatus: stream}, nil))

	// Completion and explicit upstream failures remain observed even if the
	// client closes immediately after the terminal event.
	completed := relaycommon.NewStreamStatus()
	completed.RequireTerminal()
	completed.MarkCompleted()
	completed.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	assert.Equal(t, OutcomeSuccess, ClassifyRelayOutcome(ctx, &relaycommon.RelayInfo{IsStream: true, StreamStatus: completed}, nil))
	failed := relaycommon.NewStreamStatus()
	failed.MarkFailed("invalid_api_key", "authentication_error", 401)
	failed.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	assert.Equal(t, OutcomeFailure, ClassifyRelayOutcome(ctx, &relaycommon.RelayInfo{IsStream: true, StreamStatus: failed}, nil))

	deadline := relaycommon.NewStreamStatus()
	deadline.SetEndReason(relaycommon.StreamEndReasonClientGone, context.DeadlineExceeded)
	assert.Equal(t, OutcomeFailure, ClassifyRelayOutcome(context.Background(), &relaycommon.RelayInfo{StreamStatus: deadline}, nil))
}

func TestPerformanceWindowIncludesCurrentHour(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 37, 0, 0, time.UTC)
	start, end := queryWindow(now, 24)
	assert.Equal(t, time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC).Unix(), start)
	assert.Equal(t, now.Unix(), end)
}

func TestHourlySuccessSeriesWeightsSmallerBuckets(t *testing.T) {
	points := recentSuccessSeries(map[int64]counters{
		3600: {requestCount: 100, successCount: 100},
		3900: {requestCount: 1},
		7200: {requestCount: 1, successCount: 1},
	})
	assert.Equal(t, []SuccessRatePoint{{Ts: 3600, SuccessRate: 99.01}, {Ts: 7200, SuccessRate: 100}}, points)
}

func TestRecentHistoryRejectsOversizedModelLists(t *testing.T) {
	_, err := QuerySummaryAll(24, nil, make([]string, 51)...)
	require.EqualError(t, err, "Invalid parameters")
}

// Terminal task sampling: success/failure counts, end-to-end latency, and
// token throughput only for successful tasks that report tokens.
func TestRecordTaskResultSamplesTerminalTasks(t *testing.T) {
	hotBuckets.Clear()
	t.Cleanup(func() { hotBuckets.Clear() })
	now := time.Now().Unix()
	RecordTaskResult(&model.Task{
		Status:     model.TaskStatusSuccess,
		Group:      "a",
		SubmitTime: now - 120,
		StartTime:  now - 100,
		FinishTime: now,
		Properties: model.Properties{OriginModelName: "video-model"},
	}, &relaycommon.TaskInfo{TotalTokens: 5000})
	RecordTaskResult(&model.Task{
		Status:     model.TaskStatusFailure,
		Group:      "a",
		SubmitTime: now - 60,
		FinishTime: now,
		Properties: model.Properties{OriginModelName: "video-model"},
	}, relaycommon.FailTaskInfo("boom"))
	RecordTaskResult(&model.Task{Status: model.TaskStatusSuccess, FinishTime: now}, nil)

	assert.Equal(t, map[bucketKey]counters{
		{model: "video-model", group: "a"}: {
			requestCount:   2,
			successCount:   1,
			totalLatencyMs: 180000,
			outputTokens:   5000,
			generationMs:   100000,
		},
	}, mergedHotBuckets())
}

// Immediate submit results: only successes are sampled, timed from relay start.
func TestRecordImmediateTaskResultSamplesSuccessOnly(t *testing.T) {
	hotBuckets.Clear()
	t.Cleanup(func() { hotBuckets.Clear() })
	info := &relaycommon.RelayInfo{OriginModelName: "image-model", UsingGroup: "a", StartTime: time.Now().Add(-2 * time.Second)}
	RecordImmediateTaskResult(info, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 4096})
	RecordImmediateTaskResult(info, relaycommon.FailTaskInfo("boom"))
	RecordImmediateTaskResult(info, &relaycommon.TaskInfo{Status: model.TaskStatusInProgress})
	RecordImmediateTaskResult(info, nil)

	merged := mergedHotBuckets()
	require.Len(t, merged, 1)
	got := merged[bucketKey{model: "image-model", group: "a"}]
	assert.GreaterOrEqual(t, got.totalLatencyMs, int64(2000))
	assert.Equal(t, counters{
		requestCount:   1,
		successCount:   1,
		totalLatencyMs: got.totalLatencyMs,
		outputTokens:   4096,
		generationMs:   got.totalLatencyMs,
	}, got)
}

func mergedHotBuckets() map[bucketKey]counters {
	merged := map[bucketKey]counters{}
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		k.bucketTs = 0
		mergeCounters(merged, k, value.(*atomicBucket).snapshot())
		return true
	})
	return merged
}

// TEST_PERF_MYSQL_DSN / TEST_PERF_POSTGRES_DSN optionally run the aggregation
// against isolated real MySQL/PostgreSQL databases.
func TestPerformanceAggregationAndFlush(t *testing.T) {
	for _, dialect := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "TEST_PERF_MYSQL_DSN"}, {"postgres", "TEST_PERF_POSTGRES_DSN"},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip("isolated test database DSN is not configured")
				}
			}
			t.Setenv("SQL_DSN", dsn)
			oldDB, oldPath, oldMaster, oldRedis := model.DB, common.SQLitePath, common.IsMasterNode, common.RedisEnabled
			oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			common.SQLitePath, common.IsMasterNode, common.RedisEnabled = filepath.Join(t.TempDir(), "perf.db"), false, false
			hotBuckets.Clear()
			channelBuckets.Clear()
			t.Cleanup(func() {
				model.DB, common.SQLitePath, common.IsMasterNode, common.RedisEnabled = oldDB, oldPath, oldMaster, oldRedis
				common.SetDatabaseTypes(oldType, oldLogType)
				hotBuckets.Clear()
				channelBuckets.Clear()
			})
			require.NoError(t, model.InitDB())
			db := model.DB
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.Migrator().DropTable(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.Channel{}))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.Channel{}))
			versionQuery := "SELECT version()"
			if dialect.name == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)

			now := time.Now()
			start, _ := queryWindow(now, 24)
			hour := now.Unix() - now.Unix()%3600 - 3600
			// Historical counters remain usable without reclassification or migration.
			for _, row := range []model.PerfMetric{
				{ModelName: "test-model", Group: "a", BucketTs: hour, RequestCount: 100, SuccessCount: 100, TotalLatencyMs: 100000, TtftCount: 100, TtftSumMs: 10000, OutputTokens: 200, GenerationMs: 40000},
				{ModelName: "test-model", Group: "inactive", BucketTs: hour, RequestCount: 100},
				{ModelName: "test-model", Group: "a", BucketTs: start - 3600, RequestCount: 100},
			} {
				require.NoError(t, model.UpsertPerfMetric(&row))
			}
			groups := []string{"a", "b"}

			RecordRelayResult(context.Background(), &relaycommon.RelayInfo{OriginModelName: "test-model", UsingGroup: "b", StartTime: now}, types.InitOpenAIError("unknown", 400))
			businessRejected, err := QuerySummaryAll(24, groups)
			require.NoError(t, err)
			require.NotNil(t, businessRejected.Summary)
			assert.Equal(t, 100.0, businessRejected.Summary.SuccessRate)

			failure := &atomicBucket{}
			failure.add(Sample{LatencyMs: 2000})
			hotBuckets.Store(bucketKey{model: "test-model", group: "b", bucketTs: hour}, failure)
			before, err := Query(QueryParams{Model: "test-model", Hours: 24, AllowedGroups: groups})
			require.NoError(t, err)
			require.NotNil(t, before.Summary)
			assert.Equal(t, Summary{SuccessRate: 99.01, AvgLatencyMs: 1009, AvgTps: 5}, *before.Summary)
			assert.Equal(t, start, before.WindowStart)
			require.Len(t, before.Series, 1)
			assert.Equal(t, hour, before.Series[0].Ts)
			assert.InDelta(t, 99.01, before.Series[0].SuccessRate, 0.01)
			require.Len(t, before.Groups, 2)

			summary, err := QuerySummaryAll(24, groups)
			require.NoError(t, err)
			assert.Equal(t, before.Summary, summary.Summary)
			require.Len(t, summary.Models, 1)
			assert.Equal(t, 99.01, summary.Models[0].SuccessRate)
			assert.Equal(t, 99.01, summary.Models[0].RecentSuccessSeries[0].SuccessRate)
			encoded, err := common.Marshal(summary)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "request_count")
			assert.NotContains(t, string(encoded), "success_count")

			require.NoError(t, Flush())
			require.NoError(t, Flush())
			after, err := Query(QueryParams{Model: "test-model", Hours: 24, AllowedGroups: groups})
			require.NoError(t, err)
			assert.Equal(t, before.Summary, after.Summary)
			assert.Equal(t, before.Series, after.Series)

			RecordRelayResult(context.Background(), &relaycommon.RelayInfo{OriginModelName: "test-model", UsingGroup: "a", StartTime: now}, types.InitOpenAIError("context_length_exceeded", 400))
			after, err = Query(QueryParams{Model: "test-model", Hours: 24, AllowedGroups: groups})
			require.NoError(t, err)
			assert.Equal(t, before.Summary, after.Summary)
			onlyA, err := Query(QueryParams{Model: "test-model", Group: "a", Hours: 24, AllowedGroups: groups})
			require.NoError(t, err)
			assert.Equal(t, 100.0, onlyA.Summary.SuccessRate)
			empty, err := Query(QueryParams{Model: "missing", Hours: 24, AllowedGroups: groups})
			require.NoError(t, err)
			assert.Nil(t, empty.Summary)
			assert.Empty(t, empty.Series)

			require.NoError(t, model.UpsertPerfMetric(&model.PerfMetric{ModelName: "second-model", Group: "a", BucketTs: hour, RequestCount: 1}))
			combined, err := QuerySummaryAll(24, groups)
			require.NoError(t, err)
			assert.Equal(t, 98.04, combined.Summary.SuccessRate)
			assert.Equal(t, 99.01, combined.Models[0].SuccessRate)

			t.Run("recent history does not change window summaries", func(t *testing.T) {
				for i := range 26 {
					require.NoError(t, model.UpsertPerfMetric(&model.PerfMetric{
						ModelName: "history-model", Group: "a", BucketTs: hour - int64(i+1)*6*3600,
						RequestCount: 1, SuccessCount: 1,
					}))
				}
				for _, row := range []model.PerfMetric{
					{ModelName: "history-model", Group: "a", BucketTs: hour + 60, RequestCount: 100, SuccessCount: 100},
					{ModelName: "history-model", Group: "inactive", BucketTs: hour + 120, RequestCount: 100},
					{ModelName: "history-model", Group: "a", BucketTs: hour + 2*3600, RequestCount: 1},
					{ModelName: "history-idle", Group: "a", BucketTs: hour - 3*24*3600, RequestCount: 1},
					{ModelName: "history-hidden", Group: "inactive", BucketTs: hour - 3*24*3600, RequestCount: 1},
				} {
					require.NoError(t, model.UpsertPerfMetric(&row))
				}
				require.NoError(t, db.Create(&model.PerfMetric{ModelName: "history-model", Group: "a", BucketTs: hour - 3600}).Error)
				hotFailure := &atomicBucket{}
				hotFailure.add(Sample{})
				hotBuckets.Store(bucketKey{model: "history-model", group: "b", bucketTs: hour + 120}, hotFailure)
				hotSuccess := &atomicBucket{}
				hotSuccess.add(Sample{Success: true})
				hotBuckets.Store(bucketKey{model: "history-hot", group: "a", bucketTs: hour}, hotSuccess)
				hiddenFailure := &atomicBucket{}
				hiddenFailure.add(Sample{})
				hotBuckets.Store(bucketKey{model: "history-hidden", group: "inactive", bucketTs: hour}, hiddenFailure)

				window, err := QuerySummaryAll(24, groups)
				require.NoError(t, err)
				history, err := QuerySummaryAll(24, groups, "history-model", "history-idle", "history-hidden", "history-hot", "missing")
				require.NoError(t, err)
				assert.Equal(t, window.Summary, history.Summary)
				assert.ElementsMatch(t, window.Models, history.Models)
				assert.Equal(t, window.WindowStart, history.WindowStart)
				points := history.RecentSuccessSeries["history-model"]
				require.Len(t, points, 24)
				expected := make([]SuccessRatePoint, 0, 24)
				for i := 23; i > 0; i-- {
					expected = append(expected, SuccessRatePoint{Ts: hour - int64(i)*6*3600, SuccessRate: 100})
				}
				expected = append(expected, SuccessRatePoint{Ts: hour, SuccessRate: 99.01})
				assert.Equal(t, expected, points)
				assert.Equal(t, []SuccessRatePoint{{Ts: hour - 3*24*3600, SuccessRate: 0}}, history.RecentSuccessSeries["history-idle"])
				assert.Equal(t, []SuccessRatePoint{{Ts: hour, SuccessRate: 100}}, history.RecentSuccessSeries["history-hot"])
				assert.Empty(t, history.RecentSuccessSeries["history-hidden"])
				assert.Empty(t, history.RecentSuccessSeries["missing"])

				denied, err := QuerySummaryAll(24, []string{}, "history-model", "history-hidden")
				require.NoError(t, err)
				assert.Empty(t, denied.RecentSuccessSeries)
				require.NoError(t, Flush())
				persisted, err := QuerySummaryAll(24, groups, "history-model", "history-hot")
				require.NoError(t, err)
				assert.Equal(t, points, persisted.RecentSuccessSeries["history-model"])
				assert.Equal(t, history.RecentSuccessSeries["history-hot"], persisted.RecentSuccessSeries["history-hot"])
			})

			t.Run("recent history stops at the 7 day boundary even with fewer than 24 samples", func(t *testing.T) {
				historyStart, _ := queryWindow(now, 7*24)
				for _, row := range []model.PerfMetric{
					{ModelName: "history-boundary", Group: "a", BucketTs: historyStart, RequestCount: 1, SuccessCount: 1},
					{ModelName: "history-boundary", Group: "a", BucketTs: historyStart - 3600, RequestCount: 1},
					{ModelName: "history-expired", Group: "a", BucketTs: historyStart - 3600, RequestCount: 1},
				} {
					require.NoError(t, model.UpsertPerfMetric(&row))
				}
				hotExpired := &atomicBucket{}
				hotExpired.add(Sample{Success: true})
				hotBuckets.Store(bucketKey{model: "history-hot-expired", group: "a", bucketTs: historyStart - 3600}, hotExpired)
				hotBoundary := &atomicBucket{}
				hotBoundary.add(Sample{Success: true})
				hotBuckets.Store(bucketKey{model: "history-hot-boundary", group: "a", bucketTs: historyStart}, hotBoundary)

				history, err := QuerySummaryAll(24, groups, "history-boundary", "history-expired", "history-hot-expired", "history-hot-boundary")
				require.NoError(t, err)
				assert.Equal(t, []SuccessRatePoint{{Ts: historyStart, SuccessRate: 100}}, history.RecentSuccessSeries["history-boundary"])
				assert.Equal(t, []SuccessRatePoint{{Ts: historyStart, SuccessRate: 100}}, history.RecentSuccessSeries["history-hot-boundary"])
				assert.Empty(t, history.RecentSuccessSeries["history-expired"])
				assert.Empty(t, history.RecentSuccessSeries["history-hot-expired"])
			})
			t.Run("visible routes isolate probe rounds and requests", func(t *testing.T) {
				require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}, &model.ChannelKeyObservation{}))
				const shared = "scope-shared"
				const hidden = "scope-hidden"
				const archived = "scope-archived"
				settings := `{"group_models":{"ds":["scope-shared","scope-archived"],"other":["scope-hidden"]}}`
				channels := []model.Channel{
					{Name: "mixed", Key: "test", Group: "ds,other,also-ds", Models: shared + "," + hidden + "," + archived, Setting: &settings, Status: common.ChannelStatusEnabled},
					{Name: "second", Key: "test", Group: "ds", Models: shared, Status: common.ChannelStatusEnabled},
					{Name: "hidden", Key: "test", Group: "other", Models: shared, Status: common.ChannelStatusEnabled},
					{Name: "disabled", Key: "test", Group: "ds", Models: shared, Status: common.ChannelStatusManuallyDisabled},
				}
				require.NoError(t, db.Create(&channels).Error)
				first, second, private := channels[0].Id, channels[1].Id, channels[2].Id
				access := QueryParams{Model: shared, Hours: 24, AllowedGroups: []string{"ds", "also-ds"}, VisibleModels: map[string]bool{shared: true}}
				row := func(id int, name, g, source string, window int64, n, ok, latency int64) {
					t.Helper()
					require.NoError(t, model.UpsertChannelPerfMetric(&model.ChannelPerfMetric{ChannelID: id, ModelName: name, Group: g, Source: source, BucketTs: window, RequestCount: n, SuccessCount: ok, TotalLatencyMs: latency}))
				}
				// Neither a final outcome from a currently invisible route nor an
				// unscoped legacy/global history row can affect this account.
				row(private, shared, "ds", SourceRelayResult, hour, 900, 900, 900000)
				row(channels[3].Id, shared, "ds", SourceRequest, hour, 900, 900, 900000)
				row(0, shared, "", SourceAvailabilityAll, hour, 900, 900, 900000)
				row(0, shared, "ds", SourceAvailability, hour, 900, 900, 900000)
				row(0, shared, "ds", SourceRelayResult, hour, 900, 900, 900000)
				row(first, hidden, "", SourceProbe, hour, 4, 4, 40000)
				row(first, archived, "", SourceProbe, hour, 4, 4, 40000)
				row(first, shared, "other", SourceRequest, hour, 19, 19, 190000)
				require.NoError(t, model.UpsertPerfMetric(&model.PerfMetric{ModelName: shared, Group: "ds", BucketTs: hour, RequestCount: 90, SuccessCount: 90}))
				row(first, shared, "ds", SourceRequest, hour, 1, 0, 10)
				row(first, shared, "ds", SourceRelayResult, hour, 1, 0, 10)
				// Two uncorrelated old probes both failed. This is one exact OR.
				row(first, shared, "", SourceProbe, hour, 1, 0, 10)
				row(second, shared, "", SourceProbe, hour, 1, 0, 10)
				// Three rounds in the same bucket. Channel A succeeded in two,
				// channel B in the other one, and the unrelated channel always
				// succeeded. The first two visible round signatures also repeat.
				for _, one := range []bool{true, true, false} {
					RecordVisibleRound(shared, []RoundRoute{{first, one, now.UnixMilli(), one}, {second, !one, now.UnixMilli(), !one}, {private, true, now.UnixMilli(), true}})
					RecordChannelSample(Sample{ChannelID: first, Model: shared, Success: one, LatencyMs: 10}, SourceProbe)
					RecordChannelSample(Sample{ChannelID: second, Model: shared, Success: !one, LatencyMs: 10}, SourceProbe)
				}
				// A pre-upgrade pair of failures in this same bucket survives.
				RecordChannelSample(Sample{ChannelID: first, Model: shared, LatencyMs: 10}, SourceProbe)
				RecordChannelSample(Sample{ChannelID: second, Model: shared, LatencyMs: 10}, SourceProbe)
				// Only old, attributable requests belong in the seven-day series.
				row(first, shared, "ds", SourceRelayResult, hour-4*86400, 1, 1, 30)
				row(private, shared, "other", SourceRelayResult, hour-4*86400, 9, 0, 90)
				success := true
				require.NoError(t, model.RecordChannelModelProbe(private, shared, now.UnixMilli(), &success))
				failure := false
				require.NoError(t, model.RecordChannelModelProbe(first, shared, now.UnixMilli(), &failure))
				require.NoError(t, model.RecordChannelModelProbe(second, shared, now.UnixMilli(), &failure))
				for _, persisted := range []bool{false, true} {
					if persisted {
						require.NoError(t, FlushChannelMetrics())
					}
					detail, err := QueryVisible(access)
					require.NoError(t, err)
					require.NotNil(t, detail.AvailabilityRate)
					assert.Equal(t, 50.0, *detail.AvailabilityRate, "three available rounds, two failed rounds, one failed real request")
					require.NotNil(t, detail.CurrentAvailable)
					assert.False(t, *detail.CurrentAvailable)
					require.Len(t, detail.Channels, 2)
					assert.Equal(t, 1, detail.Channels[0].ChannelIndex)
					require.Len(t, detail.Groups, 2)
					assert.Equal(t, "ds", detail.Groups[1].Group)
					assert.Equal(t, 50.0, detail.Groups[1].SuccessRate)
					require.NotNil(t, detail.Summary)
					assert.Equal(t, int64(10), detail.AvgLatencyMs)
					summaryAccess := access
					summaryAccess.Model = ""
					summary, err := QueryVisibleSummary(summaryAccess, shared, hidden, archived)
					require.NoError(t, err)
					require.Len(t, summary.Models, 1)
					assert.Equal(t, shared, summary.Models[0].ModelName)
					assert.Equal(t, int64(11), summary.Models[0].RequestCount, "multi-group probes are counted once")
					assert.Equal(t, detail.AvailabilityRate, summary.Models[0].AvailabilityRate)
					assert.Equal(t, detail.AvailabilitySeries, summary.Models[0].RecentSuccessSeries)
					assert.Len(t, summary.RecentSuccessSeries[shared], 3)
					assert.NotContains(t, summary.RecentSuccessSeries, hidden)
					assert.NotContains(t, summary.RecentSuccessSeries, archived)
				}
				// A long probe is timed in the earlier bucket but its correlated
				// OR outcome is counted only once at the end of the round.
				crossStart := (hour - 3600) * 1000
				RecordPhysicalProbe(Sample{ChannelID: first, Model: shared, Success: false, LatencyMs: 10}, crossStart)
				RecordVisibleRound(shared, []RoundRoute{{ChannelID: first, ProbeCompletedAt: crossStart}})
				onlyFirst := access
				onlyFirst.Group = "also-ds"
				crossed, err := QueryVisible(onlyFirst)
				require.NoError(t, err)
				require.NotNil(t, crossed.AvailabilityRate)
				assert.InDelta(t, 100.0/3, *crossed.AvailabilityRate, .01, "two successes out of six physical rounds")
				for _, pt := range crossed.AvailabilitySeries {
					assert.NotEqual(t, hour-3600, pt.Ts, "timing of an earlier probe must not duplicate its outcome")
				}
				// Both terminal task paths publish a real, attributed result.
				// Submitting an accepted job does not count it twice, including
				// when asynchronous completion falls in a different time bucket.
				taskAccess := QueryParams{Model: archived, Hours: 24, AllowedGroups: []string{"ds"}, VisibleModels: map[string]bool{archived: true}}
				taskInfo := &relaycommon.RelayInfo{OriginModelName: archived, UsingGroup: "ds", StartTime: now.Add(-time.Second), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: first}}
				RecordTaskSubmission(taskInfo, first, true, false)
				RecordTaskSubmission(taskInfo, first, true, true)
				RecordImmediateTaskResult(taskInfo, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 50})
				row(first, archived, "ds", SourceRequest, hour-2*3600, 1, 1, 60)
				row(first, archived, "ds", sourceTaskAttempt, hour-2*3600, 1, 1, 60)
				row(first, archived, "ds", SourceRelayResult, hour-2*3600, 1, 1, 60)
				row(first, archived, "ds", sourceTaskAccepted, hour-2*3600, 1, 1, 60)
				task := &model.Task{ChannelId: first, Group: "ds", Properties: model.Properties{OriginModelName: archived}, Status: model.TaskStatusFailure, SubmitTime: hour - 2*3600, FinishTime: now.Unix()}
				RecordTaskResult(task, nil) // a pre-upgrade task keeps its original recorded submission
				task.PrivateData.TrackTerminalMetric = true
				RecordTaskResult(task, nil)
				taskSummary, err := QueryVisibleSummary(taskAccess)
				require.NoError(t, err)
				require.Len(t, taskSummary.Models, 1)
				assert.Equal(t, int64(6), taskSummary.Models[0].RequestCount, "4 pre-existing probes and 2 terminal task results")
				require.NotNil(t, taskSummary.Models[0].AvailabilityRate)
				assert.InDelta(t, 100*5.0/6, *taskSummary.Models[0].AvailabilityRate, .01)

				for _, params := range []QueryParams{
					{Model: hidden, AllowedGroups: []string{"ds"}, VisibleModels: map[string]bool{hidden: true}},
					{Model: shared, Group: "other", AllowedGroups: []string{"ds"}, VisibleModels: access.VisibleModels},
					{Model: archived, AllowedGroups: access.AllowedGroups, VisibleModels: access.VisibleModels},
					{Model: shared, AllowedGroups: access.AllowedGroups},
					{Model: shared, VisibleModels: access.VisibleModels},
				} {
					result, err := QueryVisible(params)
					require.NoError(t, err)
					assert.Nil(t, result.Summary)
					assert.Empty(t, result.Groups)
					assert.Empty(t, result.Channels)
					assert.Nil(t, result.CurrentAvailable)
				}
			})
		})
	}
}
