package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogStatDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, dialect := range []struct {
		name, env string
		kind      common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip(dialect.env + " is not configured")
			}
			mainDB, _ := newAuditTestDatabase(t, dialect.name, os.Getenv(dialect.env))
			logDB, logDSN := newAuditTestDatabase(t, dialect.name, os.Getenv(dialect.env))
			useLogExportDatabases(t, mainDB, logDB, dialect.kind, dialect.kind)
			previousMaster, previousSQLite := common.IsMasterNode, common.SQLitePath
			t.Cleanup(func() {
				common.IsMasterNode, common.SQLitePath = previousMaster, previousSQLite
			})
			common.IsMasterNode = false
			if dialect.name == "sqlite" {
				common.SQLitePath, logDSN = logDSN, "local"
			}
			t.Setenv("LOG_SQL_DSN", logDSN)
			// Initialize the dialect's reserved-column quoting as production does.
			require.NoError(t, model.InitLogDB())
			logDB = model.LOG_DB
			t.Cleanup(func() {
				connection, err := logDB.DB()
				if err == nil {
					_ = connection.Close()
				}
			})
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}, &model.User{}))
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			now := common.GetTimestamp()
			start := now - 600
			actual := `{"admin_info":{"cost":{"cost_ratio":1.3,"base_quota":50,"cost_quota":65,"revenue_quota":100,"profit_quota":35,"estimated":false,"source":"upstream_bill"}}}`
			loss := `{"admin_info":{"cost":{"cost_ratio":1.3,"base_quota":200,"cost_quota":260,"revenue_quota":200,"profit_quota":-60,"estimated":true}}}`
			zero := `{"admin_info":{"cost":{"cost_ratio":0,"base_quota":50,"cost_quota":0,"revenue_quota":50,"profit_quota":50,"estimated":true}}}`
			logs := []model.Log{
				{UserId: 1, Username: "alice", CreatedAt: start, Type: model.LogTypeConsume, ModelName: "model-a", ChannelId: 1, TokenId: 11, TokenName: "token-a", Group: "default", Quota: 100, RequestId: "actual", UpstreamRequestId: "up-a", Other: actual},
				{UserId: 1, Username: "alice", CreatedAt: start + 1, Type: model.LogTypeConsume, ModelName: "model-a", ChannelId: 1, TokenId: 11, TokenName: "token-a", Group: "default", Quota: 200, RequestId: "loss", UpstreamRequestId: "up-b", Other: loss},
				{UserId: 2, Username: "alice", CreatedAt: start + 2, Type: model.LogTypeConsume, ModelName: "model-b", ChannelId: 2, TokenId: 22, TokenName: "token-b", Group: "vip", Quota: 30, RequestId: "unknown"},
				{UserId: 1, Username: "alice", CreatedAt: start + 3, Type: model.LogTypeConsume, ModelName: "model-a", ChannelId: 1, TokenId: 11, TokenName: "token-a", Group: "default", Quota: 40, RequestId: "invalid", Other: `{"admin_info":{"cost":`},
				{UserId: 2, Username: "bob", CreatedAt: start + 4, Type: model.LogTypeConsume, ModelName: "model-b", ChannelId: 2, TokenId: 22, TokenName: "token-b", Group: "vip", Quota: 50, RequestId: "zero", Other: zero},
				{UserId: 3, Username: "outside", CreatedAt: start - 1, Type: model.LogTypeConsume, Quota: 100, RequestId: "before", Other: actual},
				{UserId: 3, Username: "outside", CreatedAt: start + 5, Type: model.LogTypeConsume, Quota: 100, RequestId: "after", Other: actual},
				{UserId: 1, Username: "alice", CreatedAt: start + 1, Type: model.LogTypeError, Quota: 999, RequestId: "error"},
				{UserId: 1, Username: "alice", CreatedAt: start + 1, Type: model.LogTypeTestConsume, Quota: 100, RequestId: "probe", Other: actual},
				{UserId: 1, Username: "alice", CreatedAt: start + 1, Type: model.LogTypeConsume, Quota: 100, RequestId: "legacy-probe", TokenName: "模型测试", Content: "模型测试", Other: actual},
				{UserId: 1, Username: "alice", CreatedAt: now - 10, Type: model.LogTypeConsume, ModelName: "model-a", ChannelId: 1, TokenId: 11, TokenName: "token-a", Group: "default", Quota: 40, PromptTokens: 10, CompletionTokens: 2, RequestId: "recent", UpstreamRequestId: "up-recent"},
				{UserId: 2, Username: "alice", CreatedAt: now - 10, Type: model.LogTypeConsume, Quota: 50, PromptTokens: 20, CompletionTokens: 3, RequestId: "other-user-recent"},
				{UserId: 1, Username: "alice", CreatedAt: now - 120, Type: model.LogTypeConsume, Quota: 10, PromptTokens: 90, RequestId: "old-rate"},
				{UserId: 1, Username: "alice", CreatedAt: now + 60, Type: model.LogTypeConsume, Quota: 20, PromptTokens: 90, RequestId: "future"},
			}
			require.NoError(t, logDB.Create(&logs).Error)
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", start, start+4)
			for _, tc := range []struct {
				name, extra     string
				quota, rpm, tpm int
			}{
				{"all results ignore pagination", "&p=2&page_size=1", 420, 2, 35},
				{"consume type", "&type=2", 420, 2, 35},
				{"error type", "&type=5", 0, 0, 0},
				{"exact model", "&model_name=model-a", 340, 1, 12},
				{"model wildcard", "&model_name=model-%25", 420, 1, 12},
				{"username wildcard", "&username=bo%25", 50, 0, 0},
				{"exact username", "&username=alice", 370, 2, 35},
				{"token", "&token_name=token-a", 340, 1, 12},
				{"channel", "&channel=1", 340, 1, 12},
				{"group", "&group=default", 340, 1, 12},
				{"request", "&request_id=actual", 100, 0, 0},
				{"upstream request", "&upstream_request_id=up-b", 200, 0, 0},
				{"combined", "&type=2&model_name=model-a&username=alice&channel=1&token_name=token-a&group=default&request_id=actual&upstream_request_id=up-a", 100, 0, 0},
				{"test records", "&source=test&type=2", 200, 0, 0},
				{"rolling rates retain request filter", "&request_id=recent", 0, 1, 12},
				{"rolling rates retain upstream filter", "&upstream_request_id=up-recent", 0, 1, 12},
			} {
				t.Run(tc.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Set("role", common.RoleAdminUser)
					ctx.Set("id", 1)
					ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/stat?"+query+tc.extra, nil)
					GetLogsStat(ctx)
					var payload struct {
						Success bool       `json:"success"`
						Data    model.Stat `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
					require.True(t, payload.Success, recorder.Body.String())
					assert.Equal(t, model.Stat{Quota: tc.quota, Rpm: tc.rpm, Tpm: tc.tpm}, payload.Data)
					assert.NotContains(t, recorder.Body.String(), `"finance"`)
				})
			}
			for _, tc := range []struct {
				name                      string
				id, role, quota, rpm, tpm int
			}{
				{"normal self", 1, common.RoleCommonUser, 340, 1, 12},
				{"admin self", 1, common.RoleAdminUser, 340, 1, 12},
				{"same historical username", 2, common.RoleCommonUser, 80, 1, 23},
				{"missing identity", 0, common.RoleCommonUser, 0, 0, 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Set("role", tc.role)
					ctx.Set("id", tc.id)
					ctx.Set("username", "bob")
					ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/self/stat?"+query+"&username=outside&channel=2&source=test&finance=true", nil)
					GetLogsSelfStat(ctx)
					var payload struct {
						Success bool       `json:"success"`
						Data    model.Stat `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
					require.True(t, payload.Success, recorder.Body.String())
					assert.Equal(t, model.Stat{Quota: tc.quota, Rpm: tc.rpm, Tpm: tc.tpm}, payload.Data)
					assert.NotContains(t, recorder.Body.String(), `"finance"`)
				})
			}
		})
	}
}
