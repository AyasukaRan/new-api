package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise collection and retrieval against the same database engines that
// reject malformed text or impose a smaller TEXT-column limit than the trace
// collector's default one-megabyte budget.
func TestRequestTraceLargeTextDatabaseMatrix(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousEnabled, previousLimit := common.RequestTraceEnabled, common.RequestTraceMaxBytes
	common.RequestTraceEnabled, common.RequestTraceMaxBytes = true, 1<<20
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RequestTraceEnabled, common.RequestTraceMaxBytes = previousEnabled, previousLimit
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			common.SetDatabaseTypes(common.DatabaseType(dialect.kind), common.DatabaseType(dialect.kind))
			for _, layout := range []string{"shared", "separate"} {
				for _, schema := range []string{"fresh", "upgrade"} {
					t.Run(layout+"/"+schema, func(t *testing.T) {
						mainDB, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
						logDB := mainDB
						if layout == "separate" {
							logDB, _ = newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
						}
						model.DB, model.LOG_DB = mainDB, logDB
						var version string
						versionQuery := "SELECT version()"
						if dialect.kind == "sqlite" {
							versionQuery = "SELECT sqlite_version()"
						}
						require.NoError(t, logDB.Raw(versionQuery).Scan(&version).Error)
						t.Logf("database version: %s", version)
						require.NoError(t, logDB.AutoMigrate(&model.Log{}))
						oldLog := model.Log{RequestId: "historical-trace-request", Type: model.LogTypeConsume, Other: `{"admin_info":{"trace_id":"historical-trace"}}`}
						require.NoError(t, logDB.Create(&oldLog).Error)
						oldTrace := model.RequestTrace{TraceId: "historical-trace", RequestId: oldLog.RequestId, Direction: model.TraceDirectionClientRequest, Body: "历史请求🙂", BodySize: 16, CreatedAt: time.Now().Unix()}
						if schema == "upgrade" {
							// The previously released schema used TEXT on all engines.
							require.NoError(t, logDB.AutoMigrate(&model.RequestTrace{}))
							require.NoError(t, logDB.Create(&oldTrace).Error)
						}
						for range 2 {
							require.NoError(t, model.MigrateRequestTraces())
						}
						for _, index := range []string{"idx_request_traces_trace", "idx_request_traces_created_at", "idx_request_traces_request_id"} {
							assert.True(t, logDB.Migrator().HasIndex(&model.RequestTrace{}, index), index)
						}
						if layout == "separate" {
							assert.False(t, mainDB.Migrator().HasTable(&model.RequestTrace{}), "trace migration belongs to the log database")
						}
						if schema == "upgrade" {
							var loaded model.RequestTrace
							require.NoError(t, logDB.First(&loaded, oldTrace.Id).Error)
							assert.Equal(t, oldTrace, loaded)
						}
						var loadedLog model.Log
						require.NoError(t, logDB.First(&loadedLog, oldLog.Id).Error)
						assert.Equal(t, oldLog, loadedLog)

						payload := `{"text":"` + strings.Repeat("中文🙂", 140000) + `"}`
						storage, err := common.CreateBodyStorage([]byte(payload))
						require.NoError(t, err)
						t.Cleanup(func() { require.NoError(t, storage.Close()) })
						recorder := httptest.NewRecorder()
						ctx, _ := gin.CreateTestContext(recorder)
						ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
						ctx.Request.Header.Set("Content-Type", "application/json")
						ctx.Set(common.RequestIdKey, "large-unicode-trace")
						ctx.Set(common.KeyBodyStorage, storage)
						traceID := service.BeginRequestTrace(ctx, "openai")
						require.NotEmpty(t, traceID)
						upstream, err := http.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", strings.NewReader(payload))
						require.NoError(t, err)
						upstream.Header.Set("Content-Type", "application/json")
						service.CaptureUpstreamRequest(ctx, nil, upstream)
						response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}
						service.CaptureUpstreamResponse(ctx, nil, response)
						_, err = io.Copy(ctx.Writer, response.Body)
						require.NoError(t, err)
						require.NoError(t, response.Body.Close())
						service.FinishRequestTrace(ctx)
						assert.Equal(t, payload, recorder.Body.String(), "trace sanitization must not change the client response")
						require.Eventually(t, func() bool {
							var count int64
							return logDB.Model(&model.RequestTrace{}).Where("trace_id = ?", traceID).Count(&count).Error == nil && count == 4
						}, 5*time.Second, 10*time.Millisecond, "all four exchange legs must be persisted")

						var result struct {
							Success bool `json:"success"`
							Data    struct {
								TraceID string            `json:"trace_id"`
								Legs    []requestTraceLeg `json:"legs"`
							} `json:"data"`
						}
						modelManagementRequest(t, GetRequestTrace, http.MethodGet, "/api/log/trace?trace_id="+traceID, nil, &result)
						require.True(t, result.Success)
						assert.Equal(t, traceID, result.Data.TraceID)
						require.Len(t, result.Data.Legs, 4)
						for i, leg := range result.Data.Legs {
							assert.Equal(t, i, leg.Seq)
							assert.True(t, utf8.ValidString(leg.Body))
							assert.NotContains(t, leg.Body, "�", "valid source text must retain whole characters")
							assert.True(t, leg.Truncated)
							assert.EqualValues(t, len(payload), leg.BodySize)
							assert.Greater(t, len(leg.Body), 65535, "trace text must exceed the old MySQL TEXT limit")
							assert.Contains(t, leg.Body, "bytes elided")
						}
					})
				}
			}
		})
	}
}
