package controller

import (
	"context"
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

// Exercise the public report handlers against separate real main/log databases.
func TestDashboardFinanceDatabaseMatrix(t *testing.T) {
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
			logDB, _ := newAuditTestDatabase(t, dialect.name, os.Getenv(dialect.env))
			useLogExportDatabases(t, mainDB, logDB, dialect.kind, dialect.kind)
			require.NoError(t, mainDB.AutoMigrate(&model.QuotaData{}, &model.Channel{}, &model.Token{}))
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			var version string
			versionSQL := "SELECT version()"
			if dialect.name == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			require.NoError(t, logDB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database: %s %s", dialect.name, version)
			require.NoError(t, mainDB.Create(&model.Channel{Id: 1, Name: "primary"}).Error)
			quotaRows := []model.QuotaData{
				{UserID: 1, Username: "alice", ModelName: "gpt-a", CreatedAt: 3600, UseGroup: "default", TokenID: 11, ChannelID: 1, NodeName: "node-a", ClientTool: "curl", Count: 4, Quota: 430},
				{UserID: 1, Username: "alice", ModelName: "gpt-a", CreatedAt: 7200, UseGroup: "default", TokenID: 11, ChannelID: 1, NodeName: "node-a", ClientTool: "curl", Count: 1, Quota: 30},
				{UserID: 2, Username: "bob", ModelName: "gpt-b", CreatedAt: 3600, UseGroup: "vip", TokenID: 22, ChannelID: 1, NodeName: "node-b", Count: 1, Quota: 70},
				{UserID: 1, Username: "alice", ModelName: "invalid", CreatedAt: 3600, UseGroup: "default", TokenID: 11, ChannelID: 1, NodeName: "node-a", Count: 7, Quota: 70},
			}
			require.NoError(t, mainDB.Create(&quotaRows).Error)
			actual := `{"client_tool":"curl","admin_info":{"cost":{"cost_ratio":1.3,"base_quota":50,"cost_quota":65.25,"revenue_quota":100,"profit_quota":34.75,"estimated":false,"source":"upstream_bill"}}}`
			estimated := `{"client_tool":"curl","root_info":{"node_name":"node-a"},"admin_info":{"cost":{"cost_ratio":1.3,"base_quota":100,"cost_quota":130,"revenue_quota":200,"profit_quota":70,"estimated":true}}}`
			loss := `{"client_tool":"curl","admin_info":{"cost":{"cost_ratio":2,"base_quota":25,"cost_quota":50,"revenue_quota":30,"profit_quota":-20,"estimated":true}}}`
			bob := `{"admin_info":{"cost":{"cost_ratio":0,"base_quota":70,"cost_quota":0,"revenue_quota":70,"profit_quota":70,"estimated":true}}}`
			logs := []model.Log{
				{UserId: 1, Username: "alice", CreatedAt: 3610, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 1, TokenId: 11, Group: "default", Quota: 100, RequestId: "actual", Other: actual},
				{UserId: 1, Username: "alice", CreatedAt: 3620, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 1, TokenId: 11, Group: "default", Quota: 200, RequestId: "estimated", Other: estimated},
				{UserId: 1, Username: "alice", CreatedAt: 3630, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 1, TokenId: 11, Group: "default", Quota: 30, RequestId: "loss", Other: loss},
				{UserId: 1, Username: "alice", CreatedAt: 3640, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 1, TokenId: 11, Group: "default", Quota: 100, RequestId: "unknown"},
				{UserId: 1, Username: "alice", CreatedAt: 7210, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 1, TokenId: 11, Group: "default", Quota: 30, RequestId: "next-hour", Other: loss},
				{UserId: 2, Username: "bob", CreatedAt: 3610, Type: model.LogTypeConsume, ModelName: "gpt-b", ChannelId: 1, TokenId: 22, Group: "vip", Quota: 70, RequestId: "zero-cost", Other: bob},
				{UserId: 1, Username: "alice", CreatedAt: 3610, Type: model.LogTypeTestConsume, ModelName: "gpt-a", ChannelId: 1, TokenId: 11, Group: "default", Quota: 100, RequestId: "probe", Other: actual},
				{UserId: 1, Username: "alice", CreatedAt: 3610, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 1, Group: "default", Quota: 100, TokenName: "模型测试", Content: "模型测试", RequestId: "legacy-probe", Other: actual},
			}
			for i, invalid := range []string{
				`{"admin_info":{"cost":`,
				`{"admin_info":{"cost":{"cost_ratio":1,"base_quota":1,"cost_quota":5,"revenue_quota":10,"profit_quota":5,"estimated":false}}}`,
				`{"admin_info":{"cost":{"cost_ratio":1,"base_quota":1,"cost_quota":-1,"revenue_quota":10,"profit_quota":11,"estimated":true}}}`,
				`{"admin_info":{"cost":{"cost_ratio":1,"base_quota":1,"cost_quota":5,"revenue_quota":11,"profit_quota":6,"estimated":true}}}`,
				`{"admin_info":{"cost":{"cost_ratio":1,"base_quota":1,"cost_quota":5,"revenue_quota":10,"profit_quota":8,"estimated":true}}}`,
				`{"admin_info":{"cost":{"cost_ratio":1,"base_quota":1,"cost_quota":"5","revenue_quota":10,"profit_quota":5,"estimated":true}}}`,
				`{"admin_info":{"cost":{"cost_ratio":1,"base_quota":1,"cost_quota":null,"revenue_quota":10,"profit_quota":5,"estimated":true}}}`,
			} {
				logs = append(logs, model.Log{UserId: 1, Username: "alice", CreatedAt: 3650, Type: model.LogTypeConsume, ModelName: "invalid", ChannelId: 1, TokenId: 11, Group: "default", Quota: 10, RequestId: fmt.Sprintf("invalid-%d", i), Other: invalid})
			}
			require.NoError(t, logDB.Create(&logs).Error)
			wantFirst := model.DashboardFinance{CostQuota: 245.25, ProfitQuota: 84.75, CoveredRevenueQuota: 330, RecordedCount: 3, ActualCount: 1, EstimatedCount: 2, UnrecordedCount: 1}
			for _, endpoint := range []struct {
				name    string
				handler gin.HandlerFunc
				query   string
				role    int
				target  string
				want    model.DashboardFinance
			}{
				{"model", GetAllQuotaDates, "", common.RoleAdminUser, "gpt-a", wantFirst},
				{"model username", GetAllQuotaDates, "&username=alice", common.RoleRootUser, "gpt-a", wantFirst},
				{"user", GetQuotaDatesByUser, "", common.RoleAdminUser, "alice", model.DashboardFinance{CostQuota: 245.25, ProfitQuota: 84.75, CoveredRevenueQuota: 330, RecordedCount: 3, ActualCount: 1, EstimatedCount: 2, UnrecordedCount: 8}},
				{"source", GetAllSourceQuotaDates, "&time_series=true", common.RoleAdminUser, "curl", wantFirst},
				{"source summary", GetAllSourceQuotaDates, "", common.RoleRootUser, "curl", wantFirst},
				{"admin flow", GetAllFlowQuotaDates, "&username=alice", common.RoleAdminUser, "gpt-a", wantFirst},
				{"root flow", GetAllFlowQuotaDates, "&username=alice", common.RoleRootUser, "gpt-a", wantFirst},
			} {
				t.Run(endpoint.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Set("role", endpoint.role)
					ctx.Set("id", 1)
					ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data?start_timestamp=3600&end_timestamp=7199"+endpoint.query, nil)
					endpoint.handler(ctx)
					var payload struct {
						Success bool   `json:"success"`
						Message string `json:"message"`
						Data    []struct {
							ModelName  string                  `json:"model_name"`
							Username   string                  `json:"username"`
							ClientTool string                  `json:"client_tool"`
							Finance    *model.DashboardFinance `json:"finance"`
						} `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
					require.True(t, payload.Success, payload.Message)
					found := false
					for _, row := range payload.Data {
						if row.ModelName == endpoint.target || row.Username == endpoint.target || row.ClientTool == endpoint.target {
							found = true
							require.NotNil(t, row.Finance)
							assert.Equal(t, endpoint.want, *row.Finance)
						}
						if row.ModelName == "invalid" {
							require.NotNil(t, row.Finance)
							assert.Equal(t, model.DashboardFinance{UnrecordedCount: 7}, *row.Finance)
						}
					}
					assert.True(t, found)
				})
			}

			later, err := model.GetAllQuotaDates(7200, 10799, "alice")
			require.NoError(t, err)
			require.NoError(t, model.AttachQuotaFinance(context.Background(), later, 7200, 10799, "alice", false))
			require.Len(t, later, 1)
			assert.Equal(t, model.DashboardFinance{CostQuota: 50, ProfitQuota: -20, CoveredRevenueQuota: 30, RecordedCount: 1, EstimatedCount: 1}, *later[0].Finance)

			// Two historical users may share a username after deletion/recreation.
			// Filtered model reports retain their distinct user_id rows.
			for _, userID := range []int{3, 4} {
				require.NoError(t, mainDB.Create(&model.QuotaData{UserID: userID, Username: "reused", ModelName: "same", CreatedAt: 3600, Count: 1, Quota: 100}).Error)
				require.NoError(t, logDB.Create(&model.Log{UserId: userID, Username: "reused", ModelName: "same", CreatedAt: 3800, Type: model.LogTypeConsume, Quota: 100, Other: actual}).Error)
			}
			reused, err := model.GetAllQuotaDates(3600, 7199, "reused")
			require.NoError(t, err)
			require.NoError(t, model.AttachQuotaFinance(context.Background(), reused, 3600, 7199, "reused", false))
			require.Len(t, reused, 2)
			for _, row := range reused {
				require.NotNil(t, row.Finance)
				assert.Equal(t, model.DashboardFinance{CostQuota: 65.25, ProfitQuota: 34.75, CoveredRevenueQuota: 100, RecordedCount: 1, ActualCount: 1}, *row.Finance)
			}

			// Cross a full keyset batch with identical timestamps. The dashboard
			// can lag one request without dropping cost or producing negative unknowns.
			batch := make([]model.Log, 129)
			for i := range batch {
				batch[i] = model.Log{UserId: 5, Username: "batch", ModelName: "batch", CreatedAt: 3900, Type: model.LogTypeConsume, Quota: 100, Other: actual}
			}
			require.NoError(t, logDB.Create(&batch).Error)
			require.NoError(t, mainDB.Create(&model.QuotaData{UserID: 5, Username: "batch", ModelName: "batch", CreatedAt: 3600, Count: 128, Quota: 12800}).Error)
			batchRows, err := model.GetAllQuotaDates(3600, 7199, "batch")
			require.NoError(t, err)
			require.NoError(t, model.AttachQuotaFinance(context.Background(), batchRows, 3600, 7199, "batch", false))
			require.Len(t, batchRows, 1)
			assert.Equal(t, model.DashboardFinance{CostQuota: 8417.25, ProfitQuota: 4482.75, CoveredRevenueQuota: 12900, RecordedCount: 129, ActualCount: 129}, *batchRows[0].Finance)

			ambiguousLogs := []model.Log{
				{UserId: 6, Username: "ambiguous", ModelName: "ambiguous", CreatedAt: 4000, Type: model.LogTypeConsume, Quota: 100, Group: "default", ChannelId: 1, Other: actual},
				{UserId: 6, Username: "ambiguous", ModelName: "ambiguous", CreatedAt: 4001, Type: model.LogTypeConsume, Quota: 200, Group: "default", ChannelId: 1, Other: estimated},
				{UserId: 6, Username: "ambiguous", ModelName: "ambiguous", CreatedAt: 4002, Type: model.LogTypeConsume, Quota: 100, Group: "default", ChannelId: 1, Other: `{"root_info":{"node_name":""},` + actual[1:]},
			}
			require.NoError(t, logDB.Create(&ambiguousLogs).Error)
			for _, node := range []string{"node-a", "node-b", ""} {
				require.NoError(t, mainDB.Create(&model.QuotaData{UserID: 6, Username: "ambiguous", ModelName: "ambiguous", CreatedAt: 3600, Count: 1, Quota: 200, UseGroup: "default", ChannelID: 1, NodeName: node}).Error)
			}
			flowRows, err := model.GetFlowQuotaData(3600, 7199, "ambiguous", 0, common.RoleRootUser)
			require.NoError(t, err)
			require.NoError(t, model.AttachFlowFinance(context.Background(), flowRows, 3600, 7199, "ambiguous", common.RoleRootUser))
			require.Len(t, flowRows, 3)
			for _, row := range flowRows {
				switch row.NodeName {
				case "node-a":
					assert.Equal(t, int64(1), row.Finance.EstimatedCount)
					assert.Equal(t, float64(130), row.Finance.CostQuota)
				case "":
					assert.Equal(t, int64(1), row.Finance.ActualCount)
					assert.Equal(t, 65.25, row.Finance.CostQuota)
				case "node-b":
					assert.Equal(t, model.DashboardFinance{UnrecordedCount: 1}, *row.Finance)
				}
			}

			for _, handler := range []gin.HandlerFunc{GetUserQuotaDates, GetUserFlowQuotaDates, GetUserSourceQuotaDates} {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Set("role", common.RoleRootUser)
				ctx.Set("id", 1)
				ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/self?start_timestamp=3600&end_timestamp=7199&username=bob&finance=true", nil)
				handler(ctx)
				assert.NotContains(t, recorder.Body.String(), `"finance"`, "self APIs never expose finance even to an administrator")
				assert.NotContains(t, recorder.Body.String(), "cost_quota")
			}

			for _, handler := range []gin.HandlerFunc{GetAllQuotaDates, GetQuotaDatesByUser, GetAllSourceQuotaDates, GetAllFlowQuotaDates} {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Set("role", common.RoleCommonUser)
				ctx.Set("id", 1)
				ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data?start_timestamp=3600&end_timestamp=7199&finance=true", nil)
				handler(ctx)
				assert.NotContains(t, recorder.Body.String(), `"finance"`)
			}
		})
	}
}

func TestLogStatFinanceDatabaseMatrix(t *testing.T) {
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
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}))
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
			allFinance := &model.DashboardFinance{CostQuota: 325, ProfitQuota: 25, CoveredRevenueQuota: 350, RecordedCount: 3, ActualCount: 1, EstimatedCount: 2, UnrecordedCount: 2}
			modelFinance := &model.DashboardFinance{CostQuota: 325, ProfitQuota: -25, CoveredRevenueQuota: 300, RecordedCount: 2, ActualCount: 1, EstimatedCount: 1, UnrecordedCount: 1}
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", start, start+4)
			for _, tc := range []struct {
				name, extra     string
				quota, rpm, tpm int
				finance         *model.DashboardFinance
			}{
				{"all results ignore pagination", "&p=2&page_size=1", 420, 2, 35, allFinance},
				{"consume type", "&type=2", 420, 2, 35, allFinance},
				{"error type", "&type=5", 0, 0, 0, &model.DashboardFinance{}},
				{"exact model", "&model_name=model-a", 340, 1, 12, modelFinance},
				{"model wildcard", "&model_name=model-%25", 420, 1, 12, allFinance},
				{"username wildcard", "&username=bo%25", 50, 0, 0, &model.DashboardFinance{ProfitQuota: 50, CoveredRevenueQuota: 50, RecordedCount: 1, EstimatedCount: 1}},
				{"exact username", "&username=alice", 370, 2, 35, &model.DashboardFinance{CostQuota: 325, ProfitQuota: -25, CoveredRevenueQuota: 300, RecordedCount: 2, ActualCount: 1, EstimatedCount: 1, UnrecordedCount: 2}},
				{"token", "&token_name=token-a", 340, 1, 12, modelFinance},
				{"channel", "&channel=1", 340, 1, 12, modelFinance},
				{"group", "&group=default", 340, 1, 12, modelFinance},
				{"request", "&request_id=actual", 100, 0, 0, &model.DashboardFinance{CostQuota: 65, ProfitQuota: 35, CoveredRevenueQuota: 100, RecordedCount: 1, ActualCount: 1}},
				{"upstream request", "&upstream_request_id=up-b", 200, 0, 0, &model.DashboardFinance{CostQuota: 260, ProfitQuota: -60, CoveredRevenueQuota: 200, RecordedCount: 1, EstimatedCount: 1}},
				{"combined", "&type=2&model_name=model-a&username=alice&channel=1&token_name=token-a&group=default&request_id=actual&upstream_request_id=up-a", 100, 0, 0, &model.DashboardFinance{CostQuota: 65, ProfitQuota: 35, CoveredRevenueQuota: 100, RecordedCount: 1, ActualCount: 1}},
				{"test records", "&source=test&type=2", 200, 0, 0, nil},
				{"rolling rates retain request filter", "&request_id=recent", 0, 1, 12, &model.DashboardFinance{}},
				{"rolling rates retain upstream filter", "&upstream_request_id=up-recent", 0, 1, 12, &model.DashboardFinance{}},
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
					assert.Equal(t, model.Stat{Quota: tc.quota, Rpm: tc.rpm, Tpm: tc.tpm, Finance: tc.finance}, payload.Data)
					if tc.finance == nil {
						assert.NotContains(t, recorder.Body.String(), `"finance"`)
					}
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
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			stat, err := model.SumUsedQuota(ctx, model.LogFilter{Source: model.LogSourceUsage, StartTimestamp: start, EndTimestamp: start + 4}, nil, true)
			require.Error(t, err)
			assert.Equal(t, model.Stat{}, stat, "failed queries must not return partial finance")
		})
	}
}
