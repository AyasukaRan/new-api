package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The released schema intentionally omits body_encoding and the chunk table.
type releasedRequestTrace struct {
	Id          int
	TraceId     string `gorm:"type:varchar(64);index:idx_request_traces_trace,priority:1"`
	Seq         int    `gorm:"index:idx_request_traces_trace,priority:2"`
	Attempt     int
	Direction   string `gorm:"type:varchar(24)"`
	CreatedAt   int64  `gorm:"type:bigint;index:idx_request_traces_created_at"`
	RequestId   string `gorm:"type:varchar(64);index:idx_request_traces_request_id"`
	ChannelId   int
	Format      string `gorm:"type:varchar(32)"`
	Method      string `gorm:"type:varchar(16)"`
	Url         string `gorm:"type:text"`
	Status      int
	Headers     string `gorm:"type:text"`
	Body        string `gorm:"type:text"`
	BodySize    int64
	Truncated   bool
	ObjectKey   string `gorm:"type:varchar(255)"`
	ContentType string `gorm:"type:varchar(128)"`
}

func (releasedRequestTrace) TableName() string { return "request_traces" }

// Exercise collection and retrieval against the same database engines that
// reject malformed text or impose a smaller TEXT-column limit than the trace
// collector's default one-megabyte budget.
func TestRequestTraceLargeTextDatabaseMatrix(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousEnabled, previousLimit := common.RequestTraceEnabled, common.RequestTraceMaxBytes
	common.RequestTraceEnabled, common.RequestTraceMaxBytes = true, 32<<20
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RequestTraceEnabled, common.RequestTraceMaxBytes = previousEnabled, previousLimit
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}, {"clickhouse", "TEST_CLICKHOUSE_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			common.SetDatabaseTypes(common.DatabaseType(dialect.kind), common.DatabaseType(dialect.kind))
			for _, layout := range []string{"shared", "separate"} {
				if dialect.kind == "clickhouse" && layout == "shared" {
					continue // ClickHouse is supported only as a separate log database.
				}
				for _, schema := range []string{"fresh", "upgrade"} {
					t.Run(layout+"/"+schema, func(t *testing.T) {
						mainKind, mainDSN := dialect.kind, os.Getenv(dialect.env)
						if dialect.kind == "clickhouse" {
							mainKind, mainDSN = "sqlite", ""
						}
						common.SetDatabaseTypes(common.DatabaseType(mainKind), common.DatabaseType(dialect.kind))
						mainDB, _ := newAuditTestDatabase(t, mainKind, mainDSN)
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
						oldLog := model.Log{RequestId: "historical-trace-request", Type: model.LogTypeConsume, Other: `{"admin_info":{"trace_id":"historical-trace"}}`}
						if dialect.kind != "clickhouse" {
							require.NoError(t, logDB.AutoMigrate(&model.Log{}))
							require.NoError(t, logDB.Create(&oldLog).Error)
						}
						oldTrace := model.RequestTrace{TraceId: "historical-trace", RequestId: oldLog.RequestId, Direction: model.TraceDirectionClientRequest, Body: "历史请求🙂", BodySize: 16, CreatedAt: time.Now().Unix()}
						if schema == "upgrade" {
							if dialect.kind == "clickhouse" {
								require.NoError(t, logDB.Exec(`CREATE TABLE request_traces (
									id Int64 DEFAULT 0, trace_id String, seq Int32, attempt Int32, direction String,
									created_at Int64, request_id String, channel_id Int32, format String,
									method String, url String, status Int32, headers String, body String,
									body_size Int64, truncated UInt8, object_key String, content_type String
								) ENGINE = MergeTree() PARTITION BY toYYYYMM(toDateTime(created_at))
								ORDER BY (created_at, trace_id)`).Error)
							} else {
								require.NoError(t, logDB.AutoMigrate(&releasedRequestTrace{}))
							}
							require.False(t, logDB.Migrator().HasColumn(&model.RequestTrace{}, "body_encoding"))
							require.NoError(t, logDB.Omit("BodyEncoding").Create(&oldTrace).Error)
						}
						for range 2 {
							require.NoError(t, model.MigrateRequestTraces())
						}
						if dialect.kind != "clickhouse" {
							for _, index := range []string{"idx_request_traces_trace", "idx_request_traces_created_at", "idx_request_traces_request_id"} {
								assert.True(t, logDB.Migrator().HasIndex(&model.RequestTrace{}, index), index)
							}
						}
						assert.True(t, logDB.Migrator().HasColumn(&model.RequestTrace{}, "body_encoding"))
						assert.True(t, logDB.Migrator().HasTable(&model.RequestTracePayloadChunk{}))
						if layout == "separate" {
							assert.False(t, mainDB.Migrator().HasTable(&model.RequestTrace{}), "trace migration belongs to the log database")
							assert.False(t, mainDB.Migrator().HasTable(&model.RequestTracePayloadChunk{}))
						}
						if schema == "upgrade" {
							var loaded model.RequestTrace
							require.NoError(t, logDB.Where("trace_id = ?", oldTrace.TraceId).First(&loaded).Error)
							assert.Equal(t, oldTrace, loaded)
						}
						if dialect.kind != "clickhouse" {
							var loadedLog model.Log
							require.NoError(t, logDB.First(&loadedLog, oldLog.Id).Error)
							assert.Equal(t, oldLog, loadedLog)
						}

						payload := `{"text":"` + strings.Repeat("中文🙂", 140000) + `"}`
						storage, err := common.CreateBodyStorage([]byte(payload))
						require.NoError(t, err)
						t.Cleanup(func() { require.NoError(t, storage.Close()) })
						recorder := httptest.NewRecorder()
						ctx, _ := gin.CreateTestContext(recorder)
						ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
						ctx.Request.Header.Set("Content-Type", "application/json")
						ctx.Set(common.RequestIdKey, "large-unicode-trace")
						ctx.Set("id", 41)
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
							assert.False(t, leg.Truncated)
							assert.EqualValues(t, len(payload), leg.BodySize)
							assert.Greater(t, len(leg.Body), 65535, "trace text must exceed the old MySQL TEXT limit")
							assert.Equal(t, payload, leg.Body)
						}
						var metadata struct {
							Success bool
							Data    struct{ Legs []requestTraceLeg }
						}
						metadataResponse := modelManagementRequest(t, GetRequestTrace, http.MethodGet, "/api/log/trace?metadata_only=true&trace_id="+traceID, nil, &metadata)
						require.True(t, metadata.Success)
						require.Len(t, metadata.Data.Legs, 4)
						assert.Less(t, metadataResponse.Body.Len(), 4096, "opening a trace must not transfer the large conversation bodies")
						for _, leg := range metadata.Data.Legs {
							assert.True(t, leg.HasBody)
							assert.Empty(t, leg.Body)
							assert.Nil(t, leg.Rendered)
						}
						bodyResponse := modelManagementRequest(t, GetRequestTraceBody, http.MethodGet, "/api/log/trace/body?trace_id="+traceID+"&seq=0", nil, nil)
						require.Equal(t, http.StatusOK, bodyResponse.Code)
						assert.Equal(t, payload, bodyResponse.Body.String())
						assert.Equal(t, "text/plain; charset=utf-8", bodyResponse.Header().Get("Content-Type"))
						assert.Equal(t, "nosniff", bodyResponse.Header().Get("X-Content-Type-Options"))
						assert.Contains(t, bodyResponse.Header().Get("Cache-Control"), "no-store")
						assert.True(t, bodyResponse.Flushed)
						verifyRequestTraceChunkStorage(t)
					})
				}
			}
		})
	}
}

func persistRequestTraceBody(t *testing.T, traceID, body string, userID int, createdAt int64) *model.RequestTrace {
	t.Helper()
	trace := &model.RequestTrace{TraceId: traceID, Direction: model.TraceDirectionClientRequest, CreatedAt: createdAt, BodySize: int64(len(body))}
	require.NoError(t, model.StoreRequestTraceBody(trace, body, userID))
	require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{trace}))
	return trace
}

func requestTraceStoredBytes(t *testing.T, owner string) int64 {
	t.Helper()
	var size int64
	require.NoError(t, model.LOG_DB.Model(&model.RequestTracePayloadChunk{}).Where("owner = ?", owner).
		Select("COALESCE(SUM(size), 0)").Scan(&size).Error)
	return size
}

func verifyRequestTraceChunkStorage(t *testing.T) {
	t.Helper()
	// A deterministic long conversation with distinct tool-result content,
	// rather than one repeated byte that could hide boundary-shifting bugs.
	var messages strings.Builder
	for i := range 1024 {
		if i > 0 {
			messages.WriteByte(',')
		}
		digest := sha256.Sum256([]byte(strconv.Itoa(i)))
		fmt.Fprintf(&messages, `{"role":"user","content":"请求 %d: 请检查工具输出并保留原始上下文 %s"}`, i, hex.EncodeToString(digest[:]))
	}
	body := `{"messages":[` + messages.String() + `],"stream":true}`
	now := time.Now().Unix()
	first := persistRequestTraceBody(t, "chunk-round-1", body, 42, now)
	assert.NotEmpty(t, first.BodyEncoding)
	assert.Less(t, len(first.Body), len(body)/4, "stored rows should contain manifests, not repeated conversation history")
	firstSize := requestTraceStoredBytes(t, "u:42")
	require.Positive(t, firstSize)
	persistRequestTraceBody(t, "chunk-identical", body, 42, now)
	assert.Equal(t, firstSize, requestTraceStoredBytes(t, "u:42"), "identical history reuses all chunks")

	secondBody := `{"messages":[` + messages.String() + `,{"role":"assistant","content":"已完成"},{"role":"user","content":"继续"}],"stream":true}`
	persistRequestTraceBody(t, "chunk-round-2", secondBody, 42, now)
	secondSize := requestTraceStoredBytes(t, "u:42")
	assert.Less(t, secondSize-firstSize, int64(len(body)/2), "a new turn stores only the changed tail")
	thirdBody := strings.Replace(secondBody, `{"messages":[`, `{"metadata":{"session":"new"},"messages":[`, 1)
	persistRequestTraceBody(t, "chunk-round-3", thirdBody, 42, now)
	assert.Less(t, requestTraceStoredBytes(t, "u:42")-secondSize, int64(len(body)/2), "insertions before history must resynchronize with existing content")
	for traceID, expected := range map[string]string{"chunk-round-1": body, "chunk-identical": body, "chunk-round-2": secondBody, "chunk-round-3": thirdBody} {
		trace, err := model.GetRequestTraceLeg(traceID, 0)
		require.NoError(t, err)
		assert.Equal(t, expected, trace.Body)
		traces, err := model.GetRequestTraces(traceID)
		require.NoError(t, err)
		require.Len(t, traces, 1)
		assert.Equal(t, expected, traces[0].Body)
	}

	persistRequestTraceBody(t, "other-user", body, 43, now)
	assert.Equal(t, firstSize, requestTraceStoredBytes(t, "u:43"), "different users must not share stored payloads")
	for _, traceID := range []string{"anonymous-a", "anonymous-b"} {
		trace := persistRequestTraceBody(t, traceID, body, 0, now)
		var manifest struct{ Owner string }
		require.NoError(t, common.UnmarshalJsonStr(trace.Body, &manifest))
		assert.Equal(t, firstSize, requestTraceStoredBytes(t, manifest.Owner), "unattributed traces have separate namespaces")
	}

	start := make(chan struct{})
	results := make(chan error, 4)
	var workers sync.WaitGroup
	for i := range 4 {
		workers.Go(func() {
			<-start
			trace := &model.RequestTrace{TraceId: fmt.Sprintf("concurrent-%d", i), CreatedAt: now}
			err := model.StoreRequestTraceBody(trace, body, 44)
			if err == nil {
				err = model.SaveRequestTraces([]*model.RequestTrace{trace})
			}
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	for i := range 4 {
		trace, err := model.GetRequestTraceLeg(fmt.Sprintf("concurrent-%d", i), 0)
		require.NoError(t, err)
		assert.Equal(t, body, trace.Body, "concurrent writers must publish complete manifests")
	}
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		assert.Equal(t, firstSize, requestTraceStoredBytes(t, "u:44"), "SQL uniqueness must prevent duplicate chunks")
	}

	// Retries may mix many small legs with an oversized legacy/diagnostic
	// preview. Every leg must survive packet-safe batching in sequence.
	legs := make([]*model.RequestTrace, 23)
	for i := range legs {
		payload := fmt.Sprintf("leg %d 含 'quote' and \\ escape", i)
		if i == 2 {
			payload = strings.Repeat(payload, 14_000)
		}
		legs[i] = &model.RequestTrace{TraceId: "mixed-retry", Seq: i, Body: payload, BodySize: int64(len(payload)), CreatedAt: now}
	}
	require.NoError(t, model.SaveRequestTraces(legs))
	mixed, err := model.GetRequestTraces("mixed-retry")
	require.NoError(t, err)
	require.Len(t, mixed, len(legs))
	for i, leg := range mixed {
		assert.Equal(t, i, leg.Seq)
		assert.Equal(t, legs[i].Body, leg.Body)
	}

	// Simulate database failure without changing any body or encoding fallback.
	require.NoError(t, model.LOG_DB.Callback().Create().Before("gorm:create").Register("trace_payload_test_failure", func(db *gorm.DB) {
		if db.Statement.Table == "request_trace_payload_chunks" {
			db.AddError(errors.New("simulated payload write failure"))
		}
	}))
	fallback := model.RequestTrace{TraceId: "failed-chunks", Body: "retained preview", Truncated: true}
	err = model.StoreRequestTraceBody(&fallback, body, 45)
	require.NoError(t, model.LOG_DB.Callback().Create().Remove("trace_payload_test_failure"))
	require.Error(t, err)
	assert.Equal(t, "retained preview", fallback.Body)
	assert.Empty(t, fallback.BodyEncoding)
	assert.True(t, fallback.Truncated)

	corrupt := persistRequestTraceBody(t, "corrupted-body", body, 46, now)
	var chunk model.RequestTracePayloadChunk
	require.NoError(t, model.LOG_DB.Where("owner = ?", "u:46").First(&chunk).Error)
	var replacement model.RequestTracePayloadChunk
	require.NoError(t, model.LOG_DB.Where("owner = ? AND digest <> ?", "u:46", chunk.Digest).First(&replacement).Error)
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		require.NoError(t, model.LOG_DB.Exec("ALTER TABLE request_trace_payload_chunks UPDATE data = ?, size = ? WHERE owner = ? AND digest = ? SETTINGS mutations_sync = 1", replacement.Data, replacement.Size, chunk.Owner, chunk.Digest).Error)
	} else {
		require.NoError(t, model.LOG_DB.Model(&chunk).Updates(map[string]any{"data": replacement.Data, "size": replacement.Size}).Error)
	}
	_, err = model.GetRequestTraceLeg(corrupt.TraceId, 0)
	assert.Error(t, err, "corrupt chunks must not masquerade as an empty or partial conversation")
	_, err = model.GetRequestTraces(corrupt.TraceId)
	assert.Error(t, err)
	metadata, err := model.GetRequestTraceMetadata(corrupt.TraceId)
	require.NoError(t, err, "metadata remains available when a body cannot be reconstructed")
	require.Len(t, metadata, 1)
	assert.True(t, metadata[0].HasBody)
	assert.Empty(t, metadata[0].Body)
	corruptResponse := modelManagementRequest(t, GetRequestTraceBody, http.MethodGet, "/api/log/trace/body?trace_id="+corrupt.TraceId+"&seq=0", nil, nil)
	assert.Equal(t, http.StatusInternalServerError, corruptResponse.Code)
	assert.NotContains(t, corruptResponse.Body.String(), "请求 0")

	missing := persistRequestTraceBody(t, "missing-body", body, 47, now)
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		require.NoError(t, model.LOG_DB.Exec("ALTER TABLE request_trace_payload_chunks DELETE WHERE owner = ? SETTINGS mutations_sync = 1", "u:47").Error)
	} else {
		require.NoError(t, model.LOG_DB.Where("owner = ?", "u:47").Delete(&model.RequestTracePayloadChunk{}).Error)
	}
	_, err = model.GetRequestTraceLeg(missing.TraceId, 0)
	assert.Error(t, err, "a missing chunk must be reported")

	for _, mutation := range []string{"length", "digest", "encoding"} {
		trace := &model.RequestTrace{TraceId: "invalid-manifest-" + mutation, CreatedAt: now}
		require.NoError(t, model.StoreRequestTraceBody(trace, body, 42))
		var manifest map[string]any
		require.NoError(t, common.UnmarshalJsonStr(trace.Body, &manifest))
		switch mutation {
		case "length":
			manifest["size"] = len(body) + 1
		case "digest":
			manifest["digest"] = strings.Repeat("0", 64)
		case "encoding":
			trace.BodyEncoding = "unknown"
		}
		encoded, err := common.Marshal(manifest)
		require.NoError(t, err)
		trace.Body = string(encoded)
		require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{trace}))
		_, err = model.GetRequestTraceLeg(trace.TraceId, 0)
		assert.Error(t, err, mutation)
	}

	inline := persistRequestTraceBody(t, "inline-body", "短消息🙂", 42, now)
	assert.Empty(t, inline.BodyEncoding)
	loaded, err := model.GetRequestTraceLeg(inline.TraceId, 0)
	require.NoError(t, err)
	assert.Equal(t, "短消息🙂", loaded.Body)

	// A short streamed reply needs no chunk lookups. Requests of the same size
	// must still deduplicate conversation history, and larger replies must round trip.
	const responseLimit = 32 << 10
	for _, tc := range []struct {
		direction string
		size      int
		inline    bool
	}{
		{model.TraceDirectionClientResponse, responseLimit, true},
		{model.TraceDirectionUpstreamResponse, responseLimit, true},
		{model.TraceDirectionUpstreamResponse, responseLimit + 1, false},
		{model.TraceDirectionClientRequest, responseLimit, false},
		{model.TraceDirectionUpstreamRequest, responseLimit, false},
	} {
		payload := strings.Repeat("x", tc.size)
		trace := &model.RequestTrace{TraceId: fmt.Sprintf("boundary-%s-%d", tc.direction, tc.size), Direction: tc.direction, BodySize: int64(tc.size), CreatedAt: now}
		require.NoError(t, model.StoreRequestTraceBody(trace, payload, 49))
		assert.Equal(t, tc.inline, trace.BodyEncoding == "", trace.TraceId)
		require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{trace}))
		stored, err := model.GetRequestTraceLeg(trace.TraceId, 0)
		require.NoError(t, err)
		assert.Equal(t, payload, stored.Body)
	}

	// Future, deterministic buckets avoid depending on when midnight falls
	// while proving that an expired ancestor never owns a survivor's payload.
	bucket := (now/86400 + 2) * 86400
	persistRequestTraceBody(t, "expired-ancestor", body, 48, bucket+1)
	persistRequestTraceBody(t, "surviving-branch", thirdBody, 48, bucket+120)
	persistRequestTraceBody(t, "next-day", body, 48, bucket+86401)
	var buckets []int64
	require.NoError(t, model.LOG_DB.Model(&model.RequestTracePayloadChunk{}).Where("owner = ?", "u:48").Distinct("bucket").Pluck("bucket", &buckets).Error)
	assert.ElementsMatch(t, []int64{bucket, bucket + 86400}, buckets)
	require.NoError(t, model.DeleteRequestTracesBefore(bucket+60))
	expired, err := model.GetRequestTraces("expired-ancestor")
	require.NoError(t, err)
	assert.Empty(t, expired)
	loaded, err = model.GetRequestTraceLeg("surviving-branch", 0)
	require.NoError(t, err)
	assert.Equal(t, thirdBody, loaded.Body)
	require.NoError(t, model.DeleteRequestTracesBefore(bucket+86401))
	var oldChunks int64
	require.NoError(t, model.LOG_DB.Model(&model.RequestTracePayloadChunk{}).Where("bucket = ?", bucket).Count(&oldChunks).Error)
	assert.Zero(t, oldChunks, "expired buckets must be reclaimed after their traces")
	loaded, err = model.GetRequestTraceLeg("next-day", 0)
	require.NoError(t, err)
	assert.Equal(t, body, loaded.Body)
}

type canceledTraceResponse struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *canceledTraceResponse) Flush() {
	w.ResponseRecorder.Flush()
	w.cancel()
}

func TestRequestTraceReadAccessAndStreaming(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	require.NoError(t, model.MigrateRequestTraces())
	_, adminSession := createAccessTokenTestSession(t, admin.Id, "trace-admin")
	adminPAT, _ := createScopedAccessToken(t, admin.Id, time.Now().Add(time.Hour).Unix(), "log:read")
	wrongScopePAT, _ := createScopedAccessToken(t, admin.Id, time.Now().Add(time.Hour).Unix(), "usage:read")
	user := &model.User{Username: "trace-user", Password: "placeholder", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "trace-user"}
	require.NoError(t, model.DB.Create(user).Error)
	_, userSession := createAccessTokenTestSession(t, user.Id, "trace-user")
	_, revokedSession := createAccessTokenTestSession(t, admin.Id, "trace-revoked")
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", "trace-revoked").Update("status", model.UserSessionStatusRevoked).Error)

	legacyBody := "original prefix\n... [120 bytes elided] ...\noriginal tail"
	legacy := &model.RequestTrace{TraceId: "restricted-trace", CreatedAt: time.Now().Unix(), Seq: 0, Direction: model.TraceDirectionClientRequest, Body: legacyBody, Truncated: true}
	reply := strings.Repeat("private response🙂\n", 6000)
	encoded, err := common.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": "stop"}}})
	require.NoError(t, err)
	response := &model.RequestTrace{TraceId: legacy.TraceId, CreatedAt: legacy.CreatedAt, Seq: 1, Direction: model.TraceDirectionClientResponse, Format: "openai", BodySize: int64(len(encoded))}
	require.NoError(t, model.StoreRequestTraceBody(response, string(encoded), admin.Id))
	binary := &model.RequestTrace{TraceId: legacy.TraceId, CreatedAt: legacy.CreatedAt, Seq: 2, Direction: model.TraceDirectionClientResponse, ObjectKey: "audio-object", ContentType: "audio/wav", BodySize: 100}
	require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{legacy, response, binary}))

	router := gin.New()
	router.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPathsRegexs([]string{`^/api/log/trace/body$`})))
	router.GET("/api/log/trace", middleware.DisableCache(), middleware.AdminAuth(), GetRequestTrace)
	router.GET("/api/log/trace/body", middleware.DisableCache(), middleware.AdminAuth(), GetRequestTraceBody)
	for _, test := range []struct {
		name, credential string
		status           int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"ordinary user", userSession, http.StatusForbidden},
		{"administrator missing scope", wrongScopePAT, http.StatusForbidden},
		{"revoked session", revokedSession, http.StatusUnauthorized},
		{"administrator session", adminSession, http.StatusOK},
		{"administrator scoped token", adminPAT, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, path := range []string{
				"/api/log/trace?trace_id=restricted-trace&metadata_only=true",
				"/api/log/trace/body?trace_id=restricted-trace&seq=1",
				"/api/log/trace/body?trace_id=restricted-trace&seq=1&view=rendered",
			} {
				result := accessTokenRequest(router, http.MethodGet, path, test.credential, "", "")
				require.Equal(t, test.status, result.Code, result.Body.String())
				assert.Contains(t, result.Header().Get("Cache-Control"), "no-store")
				if test.status != http.StatusOK {
					assert.NotContains(t, result.Body.String(), "private response")
					assert.NotContains(t, result.Body.String(), "original prefix")
					assert.NotContains(t, result.Body.String(), "has_body")
				}
			}
		})
	}

	var metadata struct {
		Success bool
		Data    struct{ Legs []requestTraceLeg }
	}
	modelManagementRequest(t, GetRequestTrace, http.MethodGet, "/api/log/trace?trace_id=restricted-trace&metadata_only=true", nil, &metadata)
	require.True(t, metadata.Success)
	require.Len(t, metadata.Data.Legs, 3)
	assert.True(t, metadata.Data.Legs[0].HasBody, "old inline records with body_size=0 are still loadable")
	assert.True(t, metadata.Data.Legs[0].Truncated)
	assert.Empty(t, metadata.Data.Legs[0].Body)
	assert.Nil(t, metadata.Data.Legs[1].Rendered)
	assert.True(t, metadata.Data.Legs[2].HasObject)
	assert.False(t, metadata.Data.Legs[2].HasBody, "binary object size does not imply a text body")
	legacyResponse := accessTokenRequest(router, http.MethodGet, "/api/log/trace/body?trace_id=restricted-trace&seq=0", adminSession, "", "")
	assert.Equal(t, legacyBody, legacyResponse.Body.String(), "historical truncated text must remain unchanged")

	var rendered struct {
		Success bool
		Data    struct {
			Rendered *service.TraceRenderedResponse
		}
	}
	modelManagementRequest(t, GetRequestTraceBody, http.MethodGet, "/api/log/trace/body?trace_id=restricted-trace&seq=1&view=rendered", nil, &rendered)
	require.True(t, rendered.Success)
	require.NotNil(t, rendered.Data.Rendered)
	assert.Equal(t, reply, rendered.Data.Rendered.Content)
	assert.Equal(t, "stop", rendered.Data.Rendered.FinishReason)
	for _, test := range []struct {
		query  string
		status int
	}{
		{"trace_id=restricted-trace", http.StatusBadRequest},
		{"trace_id=restricted-trace&seq=-1", http.StatusBadRequest},
		{"trace_id=restricted-trace&seq=1&view=unknown", http.StatusBadRequest},
		{"trace_id=restricted-trace&seq=0&view=rendered", http.StatusBadRequest},
		{"trace_id=missing-trace&seq=0", http.StatusNotFound},
		{"trace_id=restricted-trace&seq=3", http.StatusNotFound},
	} {
		result := accessTokenRequest(router, http.MethodGet, "/api/log/trace/body?"+test.query, adminSession, "", "")
		assert.Equal(t, test.status, result.Code, test.query)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/log/trace/body?trace_id=restricted-trace&seq=1", nil)
	request.Header.Set("Authorization", "Bearer "+adminSession)
	request.Header.Set("Accept-Encoding", "gzip")
	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, request)
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Empty(t, plain.Header().Get("Content-Encoding"), "gzip middleware must not buffer this streaming endpoint")
	assert.Equal(t, string(encoded), plain.Body.String())
	assert.True(t, plain.Flushed)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := &canceledTraceResponse{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	ginContext, _ := gin.CreateTestContext(canceled)
	ginContext.Request = request.WithContext(ctx)
	GetRequestTraceBody(ginContext)
	assert.True(t, canceled.Flushed)
	assert.Positive(t, canceled.Body.Len())
	assert.Less(t, canceled.Body.Len(), len(encoded), "client cancellation stops subsequent writes")
	assert.Equal(t, string(encoded[:canceled.Body.Len()]), canceled.Body.String())

	readContext, stopRead := context.WithCancel(context.Background())
	defer stopRead()
	require.NoError(t, model.LOG_DB.Callback().Query().Before("gorm:query").Register("cancel_trace_body_read", func(db *gorm.DB) {
		if db.Statement.Table == "request_trace_payload_chunks" {
			stopRead()
		}
	}))
	_, err = model.GetRequestTraceLegContext(readContext, response.TraceId, response.Seq)
	require.NoError(t, model.LOG_DB.Callback().Query().Remove("cancel_trace_body_read"))
	assert.ErrorIs(t, err, context.Canceled, "canceling a body request also cancels its chunk queries")
}
