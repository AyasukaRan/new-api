package controller

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorRecognitionRules(t *testing.T) {
	previous := operation_setting.ErrorRecognitionRulesJSONString()
	t.Cleanup(func() { require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(previous)) })
	const rule = `[{"name":"context","enabled":true,"label":"Context too long","match":"contains","pattern":"maximum context length","status_codes":[400]}]`
	require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(rule))
	content := "status_code=400, This model's maximum context length is 1048576 tokens. However, you requested 1770481 tokens"
	got := operation_setting.ClassifyRequestError(content, `{"admin_info":{"request_body":"never-match-private"}}`)
	assert.True(t, got.Matched)
	assert.Equal(t, "context", got.RuleID)
	assert.EqualValues(t, 1048576, got.ContextLimit)
	assert.EqualValues(t, 1770481, got.RequestedTokens)
	assert.False(t, operation_setting.ClassifyRequestError("", `{"admin_info":{"request_body":"maximum context length"}}`).Matched)
	assert.False(t, operation_setting.ClassifyRequestError(content, `{"status_code":500}`).Matched)
	for _, invalid := range []string{`null`, `{}`, `[{"name":"x","label":"X","match":"contains","pattern":"x"}]`, strings.Replace(rule, `"contains"`, `"invalid"`, 1), strings.Replace(rule, `"maximum context length"`, `"["`, 1), strings.Replace(rule, `[400]`, `[200]`, 1)} {
		if strings.Contains(invalid, `"["`) {
			invalid = strings.Replace(invalid, `"contains"`, `"regex"`, 1)
		}
		assert.Error(t, operation_setting.LoadErrorRecognitionRulesJSON(invalid))
		assert.Equal(t, "context", operation_setting.ClassifyRequestError(content, "").RuleID, "rejected updates retain last working rules")
	}
	require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(`[]`))
	assert.False(t, operation_setting.ClassifyRequestError(content, "").Matched, "empty rules must not revive builtins")
	// Preview uses draft rules without changing the active setting.
	draft := `[{"name":"literal","enabled":true,"label":"Matched","match":"exact","pattern":"bad request"}]`
	preview, err := operation_setting.PreviewErrorRecognition(&draft, "bad request", 400)
	require.NoError(t, err)
	assert.True(t, preview.Matched)
	assert.False(t, operation_setting.ClassifyRequestError("bad request", "").Matched)
	require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(draft))
	assert.True(t, operation_setting.ClassifyRequestError(`{"error":{"message":"bad request"}}`, "").Matched)
	require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(previous))
	assert.Equal(t, "Response incomplete", operation_setting.ClassifyRequestError("", `{"stream_status":{"status":"ok","response_status":"incomplete"}}`).Label)
}

func TestErrorRecognitionUpstreamStatusFallback(t *testing.T) {
	previous := operation_setting.ErrorRecognitionRulesJSONString()
	t.Cleanup(func() { require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(previous)) })
	for _, tc := range []struct {
		name, content, metadata, label string
		status                         int
		matched                        bool
	}{
		{"generic 502 content identifies upstream unavailability", "status_code=502, Upstream request failed", "", "Upstream unavailable", 502, true},
		{"503 metadata identifies upstream unavailability", "Upstream request failed", `{"status_code":503}`, "Upstream unavailable", 503, true},
		{"529 metadata identifies upstream unavailability", "Upstream request failed", `{"status_code":529}`, "Upstream unavailable", 529, true},
		{"specific connection error takes precedence over 502", "connection reset by peer", `{"status_code":502}`, "Upstream connection failed", 502, true},
		{"unrelated status remains unmatched", "Upstream request failed", `{"status_code":500}`, "Request failed", 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := operation_setting.ClassifyRequestError(tc.content, tc.metadata)
			assert.Equal(t, tc.label, got.Label)
			assert.Equal(t, tc.status, got.StatusCode)
			assert.Equal(t, tc.matched, got.Matched)
		})
	}
	for _, tc := range []struct {
		name, rules, label string
		matched            bool
	}{
		{"empty rules keep 502 unmatched", `[]`, "Request failed", false},
		{"disabled status rule keeps 502 unmatched", `[{"name":"unavailable","enabled":false,"label":"Upstream unavailable","match":"regex","pattern":".*","status_codes":[502,503,529]}]`, "Request failed", false},
		{"custom rules control 502 classification", `[{"name":"custom","enabled":true,"label":"Custom upstream failure","match":"contains","pattern":"Upstream request failed"}]`, "Custom upstream failure", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(tc.rules))
			got := operation_setting.ClassifyRequestError("status_code=502, Upstream request failed", "")
			assert.Equal(t, tc.label, got.Label)
			assert.Equal(t, 502, got.StatusCode)
			assert.Equal(t, tc.matched, got.Matched)
		})
	}
}

func TestErrorRecognitionPreviewValidation(t *testing.T) {
	for _, input := range []string{`{"error":"x","status_code":200}`, `{"rules":null,"error":"x"}`, `{"rules":[{"pattern":"["}],"error":"x"}`} {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(input))
		ctx.Request.Header.Set("Content-Type", "application/json")
		PreviewErrorRecognition(ctx)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
}

func TestErrorRecognitionInventoryDatabaseMatrix(t *testing.T) {
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, tc := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite}, {"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL}, {"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL}, {"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, isolatedDSN := newAuditTestDatabase(t, tc.kind, dsn)
			mainDB, mainType := db, tc.typ
			if tc.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, tc.typ)
			if tc.kind == "clickhouse" {
				t.Setenv("LOG_SQL_DSN", isolatedDSN)
				require.NoError(t, model.InitLogDB())
			} else {
				require.NoError(t, db.AutoMigrate(&model.Log{}))
			}
			require.NoError(t, model.MigrateLogFilterIndexes())
			require.NoError(t, model.MigrateLogFilterIndexes())
			require.NoError(t, mainDB.AutoMigrate(&model.Option{}, &model.SystemTask{}))
			previousOptions := common.OptionMap
			common.OptionMap = map[string]string{}
			t.Cleanup(func() { common.OptionMap = previousOptions })
			old := operation_setting.ErrorRecognitionRulesJSONString()
			t.Cleanup(func() { require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(old)) })
			rules := `[{"name":"limit","enabled":true,"label":"Rate limited","match":"contains","pattern":"rate limit"}]`
			require.NoError(t, model.UpdateOption(operation_setting.ErrorRecognitionRulesOptionKey, rules))
			var stored model.Option
			require.NoError(t, mainDB.Where(&model.Option{Key: operation_setting.ErrorRecognitionRulesOptionKey}).First(&stored).Error)
			assert.JSONEq(t, rules, stored.Value)
			now := time.Now().Unix() - 5
			fixtures := []model.Log{
				{CreatedAt: now, Type: model.LogTypeError, RequestId: "error-a", Content: "status_code=429, rate limit 123 api_key=secret-value (request_id: private-id)", Other: `{"admin_info":{"request_body":"private prompt","request_headers":{"Authorization":"secret-header"}}}`},
				{CreatedAt: now - 1, Type: model.LogTypeError, RequestId: "error-b", Content: "status_code=429, rate limit 456 api_key=another-secret (request_id: another-id)", Other: `{}`},
				{CreatedAt: now - 2, Type: model.LogTypeConsume, RequestId: "incomplete", Other: `{"stream_status":{"status":"ok","response_status":"incomplete"}}`},
				{CreatedAt: now - 3, Type: model.LogTypeConsume, RequestId: "ok", Other: `{"stream_status":{"status":"ok"}}`},
				{CreatedAt: now - 4, Type: model.LogTypeTopup, RequestId: "topup", Content: "rate limit"},
			}
			require.NoError(t, db.Create(&fixtures).Error)
			cursor := model.ErrorInventoryCursor{Snapshot: now + 1, Days: 0}
			page, more, err := model.ReviewRequestErrors(t.Context(), cursor, 2)
			require.NoError(t, err)
			assert.True(t, more)
			assert.Equal(t, 2, page.Reviewed)
			require.Len(t, page.Groups, 1)
			assert.Equal(t, 2, page.Groups[0].Count)
			assert.True(t, page.Groups[0].Matched)
			encoded, err := common.Marshal(page)
			require.NoError(t, err)
			for _, secret := range []string{"secret-value", "another-secret", "private-id", "another-id", "private prompt", "secret-header"} {
				assert.NotContains(t, string(encoded), secret)
			}
			suggestion := page.Groups[0].SuggestedPattern
			assert.Equal(t, "rate limit", suggestion)
			assert.Contains(t, fixtures[0].Content, suggestion)
			rawCursor, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(rawCursor, &cursor))
			require.NoError(t, db.Create(&model.Log{CreatedAt: now, Type: model.LogTypeError, RequestId: "newer", Content: "new error", Other: `{}`}).Error)
			page, more, err = model.ReviewRequestErrors(t.Context(), cursor, 2)
			require.NoError(t, err)
			assert.False(t, more)
			assert.Equal(t, 1, page.Reviewed)
			assert.False(t, page.Groups[0].Matched)
			// Historic classification changes immediately; content remains intact.
			require.NoError(t, model.UpdateOption(operation_setting.ErrorRecognitionRulesOptionKey, `[]`))
			cursor = model.ErrorInventoryCursor{Snapshot: now + 1, Days: 0}
			page, _, err = model.ReviewRequestErrors(t.Context(), cursor, 2)
			require.NoError(t, err)
			assert.False(t, page.Groups[0].Matched)
			response := callLogHandler(t, GetErrorRecognitionInventory, 1, common.RoleRootUser, "days=0")[0]
			assert.Equal(t, true, response["success"], fmt.Sprint(response))
		})
	}
}
