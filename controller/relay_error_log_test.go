package controller

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProcessChannelErrorUsesSnapshotWithoutLeakingChannelMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, database.Create(&model.User{Id: 7, Username: "log-owner", Group: "default"}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 7)
	ctx.Set("username", "log-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 11)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	ctx.Set("channel_id", 202)
	ctx.Set("channel_name", "mutable-context-channel")
	ctx.Set("channel_type", 9)
	ctx.Set("use_channel", []string{"101"})
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	channelSnapshot := types.ChannelError{
		ChannelId:   101,
		ChannelType: 1,
		ChannelName: "snapshot-channel",
		AutoBan:     false,
	}
	apiErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	processChannelError(ctx, channelSnapshot, apiErr, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, channelSnapshot.ChannelId, stored.ChannelId)
	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, float64(http.StatusBadGateway), storedOther["status_code"])
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, storedOther, key)
	}
	adminInfo, ok := storedOther["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"101"}, adminInfo["use_channel"])

	logs, total, err := model.GetUserLogs(7, model.LogFilter{Type: model.LogTypeError}, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, channelSnapshot.ChannelId, logs[0].ChannelId)
	assert.Empty(t, logs[0].ChannelName)
	userOther, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	assert.NotContains(t, userOther, "admin_info")
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, userOther, key)
	}
}

// The final response must not outlive its only visible Processing row without
// first recording the failure, including failures before channel selection.
func TestRelayFinalErrorLogDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, dialect := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{{"sqlite", "", common.DatabaseTypeSQLite}, {"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL}, {"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL}, {"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse}} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dialect.env != "" && dsn == "" {
				t.Skip("set " + dialect.env)
			}
			db, isolatedDSN := newAuditTestDatabase(t, dialect.kind, dsn)
			mainDB, mainType := db, dialect.typ
			if dialect.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, dialect.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.User{}, &model.Channel{}))
			if dialect.kind == "clickhouse" {
				t.Setenv("LOG_SQL_DSN", isolatedDSN)
				require.NoError(t, model.InitLogDB())
			} else {
				require.NoError(t, db.AutoMigrate(&model.Log{}))
			}
			require.NoError(t, mainDB.Create(&model.User{Id: 773, Username: "terminal-owner", AffCode: "terminal", Group: "default"}).Error)
			oldCount, oldMedia, oldNonStreamMedia, oldTrace, oldErrors, oldConsume := constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream, common.RequestTraceEnabled, constant.ErrorLogEnabled, common.LogConsumeEnabled
			settings := operation_setting.GetGeneralSetting()
			oldPing := settings.PingIntervalEnabled
			fetch := system_setting.GetFetchSetting()
			oldFetch := *fetch
			constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream, common.RequestTraceEnabled, constant.ErrorLogEnabled, common.LogConsumeEnabled = true, true, true, false, true, true
			settings.PingIntervalEnabled = false
			fetch.AllowPrivateIp, fetch.AllowedPorts = true, []string{"1-65535"}
			t.Cleanup(func() {
				constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream, common.RequestTraceEnabled, constant.ErrorLogEnabled, common.LogConsumeEnabled = oldCount, oldMedia, oldNonStreamMedia, oldTrace, oldErrors, oldConsume
				settings.PingIntervalEnabled = oldPing
				*fetch = oldFetch
			})
			service.InitHttpClient()
			for _, fixture := range []struct {
				path   string
				format types.RelayFormat
				stream bool
			}{
				{"/v1/chat/completions", types.RelayFormatOpenAI, true},
				{"/v1/responses", types.RelayFormatOpenAIResponses, true},
				{"/v1/messages", types.RelayFormatClaude, true},
				{"/v1/chat/completions", types.RelayFormatOpenAI, false},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t", fixture.format, fixture.stream), func(t *testing.T) {
					requestID := fmt.Sprintf("terminal-%s-%t", fixture.format, fixture.stream)
					arrived, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var releaseOnce sync.Once
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						close(arrived)
						<-release
						w.WriteHeader(http.StatusBadGateway)
					}))
					defer upstream.Close()
					defer releaseOnce.Do(func() { close(release) })
					payload := map[string]any{"model": "gpt-4o", "stream": fixture.stream, "max_tokens": 16}
					switch fixture.format {
					case types.RelayFormatOpenAIResponses:
						payload["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": upstream.URL}}}}
					case types.RelayFormatClaude:
						payload["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": upstream.URL}}}}}
					default:
						payload["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": upstream.URL}}}}}
					}
					body, err := common.Marshal(payload)
					require.NoError(t, err)
					engine := gin.New()
					engine.POST(fixture.path, func(c *gin.Context) {
						c.Set("id", 773)
						c.Set("username", "terminal-owner")
						c.Set("token_id", 774)
						c.Set("token_name", "terminal-token")
						c.Set("group", "default")
						c.Set(common.RequestIdKey, requestID)
						common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-4o")
						common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
						Relay(c, fixture.format)
					})
					var loggedBeforeResponse bool
					var activeAtResponse bool
					recorder := &terminalLogRecorder{ResponseRecorder: httptest.NewRecorder(), beforeHeader: func() {
						var n int64
						if model.LOG_DB.Model(&model.Log{}).Where("request_id = ? AND type = ?", requestID, model.LogTypeError).Count(&n).Error == nil {
							loggedBeforeResponse = n == 1
						}
						rows, readErr := model.ActiveLogSnapshot(t.Context(), model.LogFilter{RequestId: requestID}, 773, common.RoleCommonUser)
						activeAtResponse = readErr == nil && len(rows) == 1
					}}
					req := httptest.NewRequest(http.MethodPost, fixture.path, bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					go func() { defer close(finished); engine.ServeHTTP(recorder, req) }()
					select {
					case <-arrived:
					case <-time.After(3 * time.Second):
						t.Fatal("media estimation did not reach the isolated upstream")
					}
					rows, err := model.ActiveLogSnapshot(t.Context(), model.LogFilter{RequestId: requestID}, 773, common.RoleCommonUser)
					require.NoError(t, err)
					if fixture.stream {
						require.Len(t, rows, 1)
					} else {
						require.Empty(t, rows)
					}
					releaseOnce.Do(func() { close(release) })
					select {
					case <-finished:
					case <-time.After(3 * time.Second):
						t.Fatal("relay did not finish")
					}
					assert.Equal(t, http.StatusInternalServerError, recorder.Code)
					assert.Contains(t, recorder.Body.String(), "failed to download file")
					assert.True(t, loggedBeforeResponse, "the original terminal failure must be persisted before its response and active cleanup")
					assert.Equal(t, fixture.stream, activeAtResponse)
					rows, err = model.ActiveLogSnapshot(t.Context(), model.LogFilter{RequestId: requestID}, 773, common.RoleCommonUser)
					require.NoError(t, err)
					assert.Empty(t, rows)
					logs, total, err := model.GetUserLogs(773, model.LogFilter{RequestId: requestID}, 0, 10)
					require.NoError(t, err)
					require.EqualValues(t, 1, total)
					require.Len(t, logs, 1)
					assert.Equal(t, model.LogTypeError, logs[0].Type)
					assert.Equal(t, 774, logs[0].TokenId)
					assert.Equal(t, "default", logs[0].Group)
					assert.Zero(t, logs[0].Quota)
					assert.Zero(t, logs[0].PromptTokens)
					assert.Zero(t, logs[0].CompletionTokens)
					assert.Zero(t, logs[0].ChannelId)
					assert.Contains(t, logs[0].Other, "count_token_failed")
					assert.Contains(t, logs[0].Content, "failed to download file")
				})
			}
			t.Run("final error policy and attempt deduplication", func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				c.Set("id", 773)
				c.Set("username", "terminal-owner")
				c.Set(common.RequestIdKey, "final-policy")
				info := &relaycommon.RelayInfo{OriginModelName: "gpt-4o", ChannelMeta: &relaycommon.ChannelMeta{}}
				first := types.NewError(errors.New("first failed attempt"), types.ErrorCodeBadResponse)
				service.RecordRelayErrorLog(c, 0, first, info)
				service.RecordFinalRelayErrorLog(c, first, info)
				info.RetryIndex++
				final := types.NewError(errors.New("final local body read failure"), types.ErrorCodeReadRequestBodyFailed)
				service.RecordFinalRelayErrorLog(c, final, info)
				service.RecordFinalRelayErrorLog(c, final, info)
				var logs []model.Log
				require.NoError(t, model.LOG_DB.Where("request_id = ?", "final-policy").Find(&logs).Error)
				require.Len(t, logs, 2)
				for _, fixture := range []struct {
					requestID string
					enabled   bool
					noRecord  bool
				}{{"logging-off", false, false}, {"policy-off", true, true}} {
					c.Set(common.RequestIdKey, fixture.requestID)
					constant.ErrorLogEnabled = fixture.enabled
					opts := []types.NewAPIErrorOptions{}
					if fixture.noRecord {
						opts = append(opts, types.ErrOptionWithNoRecordErrorLog())
					}
					service.RecordFinalRelayErrorLog(c, types.NewError(errors.New("hidden by policy"), types.ErrorCodeBadResponse, opts...), info)
					var count int64
					require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("request_id = ?", fixture.requestID).Count(&count).Error)
					assert.Zero(t, count)
				}
				constant.ErrorLogEnabled = true
				c.Set(common.RequestIdKey, "settled-request")
				model.RecordConsumeLog(c, 773, model.RecordConsumeLogParams{ModelName: "gpt-4o", Quota: 23, PromptTokens: 17, CompletionTokens: 2})
				service.RecordFinalRelayErrorLog(c, final, info)
				require.NoError(t, model.LOG_DB.Where("request_id = ?", "settled-request").Find(&logs).Error)
				require.Len(t, logs, 1)
				assert.Equal(t, model.LogTypeConsume, logs[0].Type)
				assert.Equal(t, 23, logs[0].Quota)
				c.Set(common.RequestIdKey, "next-websocket-turn")
				service.RecordFinalRelayErrorLog(c, final, info)
				require.NoError(t, model.LOG_DB.Where("request_id = ?", "next-websocket-turn").Find(&logs).Error)
				require.Len(t, logs, 1)
				assert.Equal(t, model.LogTypeError, logs[0].Type)
			})
		})
	}
}

type terminalLogRecorder struct {
	*httptest.ResponseRecorder
	beforeHeader func()
}

func (w *terminalLogRecorder) WriteHeader(code int) {
	w.beforeHeader()
	w.ResponseRecorder.WriteHeader(code)
}
