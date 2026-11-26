package controller

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reconciliation changes administrator cost projections, never the original
// request charges. The immutable log store may be a different database engine.
func TestSub2APIBillFinanceDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousUnit })
	for _, dialect := range []struct {
		name, env string
		kind      common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
		{"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip(dialect.env + " is not configured")
			}
			mainName, mainDSN, mainKind := dialect.name, os.Getenv(dialect.env), dialect.kind
			if dialect.kind == common.DatabaseTypeClickHouse {
				mainName, mainDSN, mainKind = "sqlite", "", common.DatabaseTypeSQLite
			}
			mainDB, _ := newAuditTestDatabase(t, mainName, mainDSN)
			logDB, logDSN := newAuditTestDatabase(t, dialect.name, os.Getenv(dialect.env))
			useLogExportDatabases(t, mainDB, logDB, mainKind, dialect.kind)
			require.NoError(t, mainDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.QuotaData{}))
			require.NoError(t, model.MigrateSub2APIStorage(mainDB))
			previousMaster, previousSQLite := common.IsMasterNode, common.SQLitePath
			t.Cleanup(func() { common.IsMasterNode, common.SQLitePath = previousMaster, previousSQLite })
			common.IsMasterNode = dialect.kind == common.DatabaseTypeClickHouse
			if dialect.name == "sqlite" {
				common.SQLitePath, logDSN = logDSN, "local"
			}
			t.Setenv("LOG_SQL_DSN", logDSN)
			t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "0")
			require.NoError(t, model.InitLogDB())
			logDB = model.LOG_DB
			connection, err := logDB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close() })
			if dialect.kind != common.DatabaseTypeClickHouse {
				require.NoError(t, logDB.AutoMigrate(&model.Log{}))
				require.NoError(t, model.MigrateDashboardFinance())
			}
			user := model.User{Id: 1, Username: "bill-owner", Quota: 10000, UsedQuota: 300}
			token := model.Token{Id: 11, UserId: 1, Key: "fixture-only", Name: "bill-token", RemainQuota: 9700, UsedQuota: 300}
			require.NoError(t, mainDB.Create(&user).Error)
			require.NoError(t, mainDB.Create(&token).Error)
			require.NoError(t, mainDB.Create(&model.Channel{Id: 1, Name: "supplier"}).Error)
			require.NoError(t, mainDB.Create(&model.Sub2APIAccount{ID: 7, ChannelID: 1, Origin: "https://fixture.example", UpstreamUserID: 7, Identity: strings.Repeat("a", 64), ProxyIdentity: strings.Repeat("p", 64), Credential: "sealed-fixture", NeedsLogin: true}).Error)
			const hour int64 = 3600
			require.NoError(t, mainDB.Create(&model.QuotaData{UserID: 1, Username: user.Username, ModelName: "bill-model", CreatedAt: hour, TokenID: 11, ChannelID: 1, UseGroup: "bill-group", Count: 3, Quota: 300}).Error)
			var logs []model.Log
			for i, name := range []string{"wallet-zero", "subscription-fraction", "pending"} {
				metadata, err := common.Marshal(map[string]any{"admin_info": map[string]any{
					"sub2api_billing": model.Sub2APIBillingRef{AccountID: 7, APIKeyID: 17, KeyIdentity: strings.Repeat("a", 64), RequestID: "client:" + name},
					"cost":            map[string]any{"cost_ratio": 0.1, "base_quota": 100, "cost_quota": 10, "revenue_quota": 100, "profit_quota": 90, "estimated": true},
				}})
				require.NoError(t, err)
				row := model.Log{UserId: 1, Username: user.Username, CreatedAt: hour + 10 + int64(i), Type: model.LogTypeConsume, ModelName: "bill-model", ChannelId: 1, TokenId: 11, TokenName: token.Name, Group: "bill-group", Quota: 100, RequestId: name, Other: string(metadata)}
				if dialect.kind == common.DatabaseTypeClickHouse {
					// Production createLog assigns IDs for the non-auto-increment log store.
					row.Id = 100 + i
				}
				require.NoError(t, logDB.Create(&row).Error)
				logs = append(logs, row)
				require.NoError(t, model.QueueSub2APIBill(t.Context(), &row))
				require.NoError(t, model.QueueSub2APIBill(t.Context(), &row))
			}
			require.NoError(t, model.MigrateSub2APIStorage(mainDB), "repeated startup preserves queued bills and uniqueness")
			var bills []model.Sub2APIBill
			require.NoError(t, mainDB.Order("created_at").Find(&bills).Error)
			require.Len(t, bills, 3)
			for _, bill := range bills {
				assert.Nil(t, bill.CostQuota)
			}
			filter := model.LogFilter{StartTimestamp: hour, EndTimestamp: hour + 3599}
			require.NoError(t, model.AttachQuotaFinance(t.Context(), nil, hour, hour+3599, "", false))
			warm, err := model.SumUsedQuota(t.Context(), filter, nil, true)
			require.NoError(t, err)
			assert.Equal(t, &model.DashboardFinance{CostQuota: 30, ProfitQuota: 270, CoveredRevenueQuota: 300, RecordedCount: 3, EstimatedCount: 3}, warm.Finance)
			for _, amount := range []string{"-1", "NaN", "+Inf", "1e30", "not-money"} {
				assert.Error(t, model.SettleSub2APIBill(t.Context(), bills[0], 101, 17, bills[0].RequestID, amount, 0, nil), amount)
			}
			assert.Error(t, model.SettleSub2APIBill(t.Context(), bills[0], 101, 18, bills[0].RequestID, "1", 0, nil))
			assert.Error(t, model.SettleSub2APIBill(t.Context(), bills[0], 101, 17, "client:other", "1", 0, nil))
			assert.Error(t, model.SettleSub2APIBill(t.Context(), bills[0], 101, 17, bills[0].RequestID, "1", 9, nil))
			for _, rate := range []float64{-1, math.NaN(), math.Inf(1), 10001} {
				assert.Error(t, model.SettleSub2APIBill(t.Context(), bills[0], 101, 17, bills[0].RequestID, "1", 0, &rate))
			}
			rate := 23.0
			require.NoError(t, model.SettleSub2APIBill(t.Context(), bills[0], 101, 17, bills[0].RequestID, "0", 0, &rate))
			require.NoError(t, model.SettleSub2APIBill(t.Context(), bills[1], 102, 17, bills[1].RequestID, "0.0000005", 1, &rate))
			require.NoError(t, model.SettleSub2APIBill(t.Context(), bills[1], 102, 17, bills[1].RequestID, "9", 1, &rate), "replay cannot overwrite an already reconciled charge")
			var settled []model.Sub2APIBill
			require.NoError(t, mainDB.Order("created_at").Find(&settled).Error)
			require.Len(t, settled, 3)
			require.NotNil(t, settled[0].CostQuota)
			assert.Zero(t, *settled[0].CostQuota)
			require.NotNil(t, settled[1].CostQuota)
			assert.Equal(t, 0.25, *settled[1].CostQuota, "actual cost already contains the upstream rate")
			assert.Nil(t, settled[2].CostQuota, "a missing bill is not zero cost")
			status, err := model.GetSub2APIBillStatus(t.Context(), 1)
			require.NoError(t, err)
			require.Len(t, status, 1)
			assert.Equal(t, int64(2), status[strings.Repeat("a", 64)].Synced)
			assert.Equal(t, int64(1), status[strings.Repeat("a", 64)].Pending)
			assert.Positive(t, status[strings.Repeat("a", 64)].LastSyncedAt)
			otherStatus, err := model.GetSub2APIBillStatus(t.Context(), 2)
			require.NoError(t, err)
			assert.Empty(t, otherStatus)
			pending, err := model.PendingSub2APIBills(t.Context(), 1, 10)
			require.NoError(t, err)
			require.Len(t, pending, 1, "manual sync includes the selected channel despite backoff")
			assert.Equal(t, bills[2].ID, pending[0].ID)
			require.NoError(t, mainDB.Delete(&settled[2]).Error)
			require.NoError(t, model.RepairSub2APIBillQueue(t.Context(), hour, []int{1}))
			var restored []model.Sub2APIBill
			require.NoError(t, mainDB.Order("created_at").Find(&restored).Error)
			require.Len(t, restored, 3, "repair restores an interrupted enqueue without duplicating settled bills")
			assert.Nil(t, restored[2].CostQuota)
			want := model.DashboardFinance{CostQuota: 10.25, ProfitQuota: 289.75, CoveredRevenueQuota: 300, RecordedCount: 3, ActualCount: 2, EstimatedCount: 1}
			for range 2 {
				stat, err := model.SumUsedQuota(t.Context(), filter, nil, true)
				require.NoError(t, err)
				assert.Equal(t, 300, stat.Quota)
				assert.Equal(t, &want, stat.Finance)
				rows, err := model.GetAllQuotaDates(hour, hour+3599, user.Username)
				require.NoError(t, err)
				require.NoError(t, model.AttachQuotaFinance(t.Context(), rows, hour, hour+3599, user.Username, false))
				require.Len(t, rows, 1)
				assert.Equal(t, &want, rows[0].Finance)
			}
			adminLogs, total, err := model.GetAllLogs(filter, 0, 10)
			require.NoError(t, err)
			assert.Equal(t, int64(3), total)
			for _, row := range adminLogs {
				if row.RequestId == "pending" {
					assert.NotContains(t, row.Other, "upstream_bill")
					continue
				}
				var metadata struct {
					Admin struct {
						Estimate map[string]any `json:"cost_estimate"`
						Cost     struct {
							Source      string  `json:"source"`
							BillingType int     `json:"billing_type"`
							CostQuota   float64 `json:"cost_quota"`
						} `json:"cost"`
					} `json:"admin_info"`
				}
				require.NoError(t, common.UnmarshalJsonStr(row.Other, &metadata))
				assert.Equal(t, "upstream_bill", metadata.Admin.Cost.Source)
				assert.Equal(t, float64(10), metadata.Admin.Estimate["cost_quota"])
				if row.RequestId == "wallet-zero" {
					assert.Zero(t, metadata.Admin.Cost.BillingType)
					assert.Zero(t, metadata.Admin.Cost.CostQuota)
				} else {
					assert.Equal(t, 1, metadata.Admin.Cost.BillingType)
					assert.Equal(t, 0.25, metadata.Admin.Cost.CostQuota)
				}
			}
			selfLogs, _, err := model.GetUserLogs(1, filter, 0, 10)
			require.NoError(t, err)
			require.Len(t, selfLogs, 3)
			for _, row := range selfLogs {
				assert.NotContains(t, row.Other, "admin_info")
				assert.NotContains(t, row.Other, "cost_quota")
			}
			for _, original := range logs {
				var raw model.Log
				require.NoError(t, logDB.Where("request_id = ?", original.RequestId).First(&raw).Error)
				assert.Equal(t, original.Other, raw.Other, "the source log is immutable, including ClickHouse")
				assert.Equal(t, original.Quota, raw.Quota)
			}

			t.Run("exact source ownership", func(t *testing.T) {
				copy := logs[1]
				copy.Id += 100000
				require.NoError(t, model.AttachSub2APIBills(t.Context(), []*model.Log{&copy}))
				assert.Equal(t, logs[1].Other, copy.Other, "same request metadata on another log cannot acquire its invoice")
				missing := bills[2]
				missing.LogID += 100000
				assert.Error(t, model.SettleSub2APIBill(t.Context(), missing, 103, 17, missing.RequestID, "0.1", 0, nil))
				var unchanged model.Sub2APIBill
				require.NoError(t, mainDB.Where("match_id = ?", missing.MatchID).Take(&unchanged).Error)
				assert.Nil(t, unchanged.CostQuota)
			})

			t.Run("response identity matches one invoice once", func(t *testing.T) {
				var pending []model.Sub2APIBill
				require.NoError(t, mainDB.Create(&model.Sub2APIAccount{ID: 8, ChannelID: 2, Origin: "https://fixture.example", UpstreamUserID: 7, Identity: strings.Repeat("b", 64), ProxyIdentity: strings.Repeat("p", 64), Credential: "sealed-fixture", NeedsLogin: true}).Error)
				for i, primary := range []string{"client:ws-envelope", "resp-exact", "client:other-channel"} {
					accountID, channelID := int64(7), 1
					if i == 2 {
						accountID, channelID = 8, 2
					}
					ref := model.Sub2APIBillingRef{AccountID: accountID, APIKeyID: 17, KeyIdentity: strings.Repeat("a", 64), RequestID: primary, ResponseID: "resp-exact"}
					metadata, err := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": ref, "cost": map[string]any{"cost_ratio": 0.1, "base_quota": 100, "cost_quota": 10, "revenue_quota": 100, "profit_quota": 90, "estimated": true}}})
					require.NoError(t, err)
					row := model.Log{UserId: 1, Username: user.Username, CreatedAt: hour + 4000 + int64(i), Type: model.LogTypeConsume, ModelName: "response-identity", ChannelId: channelID, TokenId: 11, Quota: 100, RequestId: fmt.Sprintf("response-local-%d", i), Other: string(metadata)}
					if dialect.kind == common.DatabaseTypeClickHouse {
						row.Id = 300 + i
					}
					require.NoError(t, logDB.Create(&row).Error)
					require.NoError(t, model.QueueSub2APIBill(t.Context(), &row))
					var bill model.Sub2APIBill
					require.NoError(t, mainDB.Where("log_request_id = ?", row.RequestId).Take(&bill).Error)
					assert.Nil(t, bill.InvoiceIdentity, "distinct pending records may share no assigned invoice")
					pending = append(pending, bill)
				}
				assert.Error(t, model.SettleSub2APIBill(t.Context(), pending[0], 110, 17, "resp-wrong", "0.000002", 0, nil))
				require.NoError(t, model.SettleSub2APIBill(t.Context(), pending[0], 110, 17, "resp-exact", "0.000002", 0, nil))
				for _, bill := range pending[1:] {
					assert.Error(t, model.SettleSub2APIBill(t.Context(), bill, 110, 17, "resp-exact", "0.000002", 0, nil), "the same upstream invoice cannot be assigned to two local logs, including another channel")
				}
				var duplicate model.Sub2APIBill
				require.NoError(t, mainDB.First(&duplicate, pending[1].ID).Error)
				assert.Nil(t, duplicate.CostQuota)
				assert.Nil(t, duplicate.InvoiceIdentity)
				items, _, err := model.GetAllLogs(model.LogFilter{ModelName: "response-identity"}, 0, 10)
				require.NoError(t, err)
				require.Len(t, items, 3)
				for _, row := range items {
					if row.RequestId == "response-local-0" {
						assert.Contains(t, row.Other, "upstream_bill")
					} else {
						assert.NotContains(t, row.Other, "upstream_bill")
						assert.Contains(t, row.Other, "estimated")
					}
				}
			})

			t.Run("paid failure costs never become user charges or hidden retries", func(t *testing.T) {
				metadata, err := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": model.Sub2APIBillingRef{AccountID: 7, APIKeyID: 17, KeyIdentity: strings.Repeat("a", 64), RequestID: "client:paid-failure"}}})
				require.NoError(t, err)
				failure := model.Log{UserId: 1, Username: user.Username, CreatedAt: hour + 20, Type: model.LogTypeError, ModelName: "bill-error-only", ChannelId: 1, TokenId: 11, Group: "bill-group", RequestId: "paid-failed-retry", Other: string(metadata)}
				if dialect.kind == common.DatabaseTypeClickHouse {
					failure.Id = 200
				}
				require.NoError(t, logDB.Create(&failure).Error)
				require.NoError(t, model.QueueSub2APIBill(t.Context(), &failure))
				var bill model.Sub2APIBill
				require.NoError(t, mainDB.Where("log_id = ?", failure.Id).Take(&bill).Error)
				for range 2 {
					require.NoError(t, model.SettleSub2APIBill(t.Context(), bill, 104, 17, bill.RequestID, "0.000004", 0, nil))
				}
				stat, err := model.SumUsedQuota(t.Context(), filter, nil, true)
				require.NoError(t, err)
				assert.Equal(t, 300, stat.Quota)
				wantPaid := want
				wantPaid.CostQuota += 2
				wantPaid.ProfitQuota -= 2
				assert.Equal(t, &wantPaid, stat.Finance)
				successFilter := filter
				successFilter.Type = model.LogTypeConsume
				successStat, err := model.SumUsedQuota(t.Context(), successFilter, nil, true)
				require.NoError(t, err)
				assert.Equal(t, &want, successStat.Finance)
				rows, err := model.GetAllQuotaDates(hour, hour+3599, user.Username)
				require.NoError(t, err)
				require.NoError(t, model.AttachQuotaFinance(t.Context(), rows, hour, hour+3599, user.Username, false))
				require.Len(t, rows, 2)
				for _, row := range rows {
					if row.ModelName != failure.ModelName {
						continue
					}
					assert.Zero(t, row.Count)
					assert.Zero(t, row.Quota)
					require.NotNil(t, row.Finance)
					assert.Equal(t, 2.0, row.Finance.CostQuota)
					assert.Equal(t, -2.0, row.Finance.ProfitQuota)
				}
				var reportRows int64
				require.NoError(t, mainDB.Model(&model.QuotaData{}).Where("model_name = ?", failure.ModelName).Count(&reportRows).Error)
				assert.Equal(t, int64(1), reportRows, "settlement replay cannot create another cost-only report row")
				success := failure
				success.Id, success.Type, success.CreatedAt, success.Other = 0, model.LogTypeConsume, hour+21, "{}"
				if dialect.kind == common.DatabaseTypeClickHouse {
					success.Id = 201
				}
				require.NoError(t, logDB.Create(&success).Error)
				listed, total, err := model.GetAllLogs(model.LogFilter{Category: "model", RequestId: failure.RequestId}, 0, 10)
				require.NoError(t, err)
				assert.Equal(t, int64(2), total)
				require.Len(t, listed, 2, "paid upstream attempts must remain visible alongside the later success")
				for _, row := range listed {
					if row.Type == model.LogTypeError {
						assert.Contains(t, row.Other, "upstream_bill")
					}
				}
			})
			var afterUser model.User
			var afterToken model.Token
			require.NoError(t, mainDB.First(&afterUser, user.Id).Error)
			require.NoError(t, mainDB.First(&afterToken, token.Id).Error)
			assert.Equal(t, user.Quota, afterUser.Quota)
			assert.Equal(t, user.UsedQuota, afterUser.UsedQuota)
			assert.Equal(t, token.RemainQuota, afterToken.RemainQuota)
			assert.Equal(t, token.UsedQuota, afterToken.UsedQuota)

			t.Run("bounded export preserves same-second records and invoice overlays", func(t *testing.T) {
				// SQLite shares one connection in normal installations. Export must
				// release its source cursor before loading main-database invoices.
				exportDB := logDB
				if dialect.kind == common.DatabaseTypeSQLite {
					exportDB = mainDB
					model.LOG_DB = mainDB
					t.Cleanup(func() { model.LOG_DB = logDB })
					require.NoError(t, mainDB.AutoMigrate(&model.Log{}))
					conn, err := mainDB.DB()
					require.NoError(t, err)
					conn.SetMaxOpenConns(1)
				}
				rows := make([]model.Log, 301)
				wantRequests := make([]string, len(rows))
				for i := range rows {
					wantRequests[i] = fmt.Sprintf("export-boundary-%03d", i)
					rows[i] = model.Log{UserId: 1, Username: user.Username, CreatedAt: hour + 100, Type: model.LogTypeConsume, ModelName: "export-boundary", ChannelId: 1, RequestId: wantRequests[i], Other: "{}"}
				}
				// Old ClickHouse rows can share ID=0 and the same second. Their
				// request IDs, not the later synthetic display IDs, preserve order.
				if dialect.kind == common.DatabaseTypeClickHouse {
					rows[0].Id = 1000
				}
				metadata, err := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": model.Sub2APIBillingRef{AccountID: 7, APIKeyID: 17, KeyIdentity: strings.Repeat("a", 64), RequestID: "client:export-boundary"}}})
				require.NoError(t, err)
				rows[0].Other = string(metadata)
				if dialect.kind == common.DatabaseTypeClickHouse {
					require.NoError(t, exportDB.Create(&rows[0]).Error)
					legacy := rows[1:]
					require.NoError(t, exportDB.Create(&legacy).Error)
				} else {
					require.NoError(t, exportDB.Create(&rows).Error)
				}
				require.NoError(t, model.QueueSub2APIBill(t.Context(), &rows[0]))
				var bill model.Sub2APIBill
				require.NoError(t, mainDB.Where("log_request_id = ?", rows[0].RequestId).Take(&bill).Error)
				require.NoError(t, model.SettleSub2APIBill(t.Context(), bill, 105, 17, bill.RequestID, "0.000002", 0, nil))
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				var exported []string
				actualCount := 0
				require.NoError(t, model.StreamAllLogs(ctx, model.LogFilter{ModelName: "export-boundary"}, common.RoleRootUser, func(row *model.Log) error {
					exported = append(exported, row.RequestId)
					if strings.Contains(row.Other, "upstream_bill") {
						actualCount++
					}
					return nil
				}))
				assert.ElementsMatch(t, wantRequests, exported)
				assert.Equal(t, 1, actualCount)
				if dialect.kind == common.DatabaseTypeClickHouse {
					assert.Equal(t, "export-boundary-300", exported[0])
					assert.Equal(t, "export-boundary-000", exported[len(exported)-1])
				}
			})
		})
	}
}

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
		{"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip(dialect.env + " is not configured")
			}
			mainName, mainDSN, mainKind := dialect.name, os.Getenv(dialect.env), dialect.kind
			if dialect.kind == common.DatabaseTypeClickHouse {
				mainName, mainDSN, mainKind = "sqlite", "", common.DatabaseTypeSQLite
			}
			mainDB, _ := newAuditTestDatabase(t, mainName, mainDSN)
			logDB, logDSN := newAuditTestDatabase(t, dialect.name, os.Getenv(dialect.env))
			useLogExportDatabases(t, mainDB, logDB, mainKind, dialect.kind)
			require.NoError(t, mainDB.AutoMigrate(&model.QuotaData{}, &model.Channel{}, &model.Token{}))
			if dialect.kind == common.DatabaseTypeClickHouse {
				previousMaster := common.IsMasterNode
				common.IsMasterNode = true
				t.Cleanup(func() { common.IsMasterNode = previousMaster })
				t.Setenv("LOG_SQL_DSN", logDSN)
				t.Setenv("LOG_SQL_CLICKHOUSE_TTL_DAYS", "0")
				require.NoError(t, model.InitLogDB())
				logDB = model.LOG_DB
				conn, err := logDB.DB()
				require.NoError(t, err)
				t.Cleanup(func() { _ = conn.Close() })
			} else {
				require.NoError(t, logDB.AutoMigrate(&model.Log{}))
				require.NoError(t, model.MigrateDashboardFinance())
			}
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

			// Imported events can share both a timestamp and an empty request ID.
			// Retain them all even if the hourly usage table lags one request.
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

			// The original usage is retained, while only the paid remainder is
			// revenue. Missing cost must not turn an allowance into paid income.
			const allowanceHour int64 = 14400
			free := `{"client_tool":"curl","root_info":{"node_name":"allowance-node"},"billing_source":"free_allowance","free_quota":100,"admin_info":{"cost":{"cost_ratio":0,"base_quota":100,"free_quota":100,"cost_quota":0,"revenue_quota":0,"profit_quota":0,"estimated":true}}}`
			mixed := `{"client_tool":"curl","root_info":{"node_name":"allowance-node"},"billing_source":"free_allowance","free_quota":60,"admin_info":{"cost":{"cost_ratio":0.1,"base_quota":100,"free_quota":60,"cost_quota":10,"revenue_quota":40,"profit_quota":30,"estimated":true}}}`
			missingCost := `{"client_tool":"curl","root_info":{"node_name":"allowance-node"},"billing_source":"free_allowance","free_quota":25}`
			paid := `{"client_tool":"curl","root_info":{"node_name":"allowance-node"},"billing_source":"subscription","admin_info":{"cost":{"cost_ratio":0.1,"base_quota":100,"cost_quota":10,"revenue_quota":100,"profit_quota":90,"estimated":true}}}`
			allowanceLogs := make([]model.Log, 0, 4)
			for i, other := range []string{free, mixed, missingCost, paid} {
				allowanceLogs = append(allowanceLogs, model.Log{UserId: 7, Username: "allowance", ModelName: "deepseek-flash", CreatedAt: allowanceHour + int64(i), Type: model.LogTypeConsume, Quota: 100, ChannelId: 1, TokenId: 11, Group: "deepseek", Other: other})
			}
			require.NoError(t, logDB.Create(&allowanceLogs).Error)
			require.NoError(t, mainDB.Create(&model.QuotaData{UserID: 7, Username: "allowance", ModelName: "deepseek-flash", CreatedAt: allowanceHour, UseGroup: "deepseek", TokenID: 11, ChannelID: 1, NodeName: "allowance-node", ClientTool: "curl", Count: 4, Quota: 400}).Error)
			wantAllowance := model.DashboardFinance{FreeQuota: 185, CostQuota: 20, CoveredRevenueQuota: 140, ProfitQuota: 120, RecordedCount: 3, EstimatedCount: 3, UnrecordedCount: 1}
			filter := model.LogFilter{StartTimestamp: allowanceHour, EndTimestamp: allowanceHour + 3599}
			pending, err := model.SumUsedQuota(t.Context(), filter, nil, true)
			require.NoError(t, err)
			assert.Equal(t, 400, pending.Quota)
			assert.Equal(t, &wantAllowance, pending.Finance)
			for _, handler := range []gin.HandlerFunc{GetAllQuotaDates, GetQuotaDatesByUser, GetAllSourceQuotaDates, GetAllFlowQuotaDates} {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Set("role", common.RoleRootUser)
				ctx.Set("id", 7)
				ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data?start_timestamp=14400&end_timestamp=17999&username=allowance&time_series=true", nil)
				handler(ctx)
				var payload struct {
					Success bool `json:"success"`
					Data    []struct {
						Quota   int                     `json:"quota"`
						Finance *model.DashboardFinance `json:"finance"`
					} `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
				require.True(t, payload.Success, recorder.Body.String())
				require.Len(t, payload.Data, 1)
				assert.Equal(t, 400, payload.Data[0].Quota)
				assert.Equal(t, &wantAllowance, payload.Data[0].Finance)
			}
			warmAllowance, err := model.SumUsedQuota(t.Context(), filter, nil, true)
			require.NoError(t, err)
			assert.Equal(t, pending, warmAllowance)
			if dialect.kind != common.DatabaseTypeClickHouse {
				lateAllowance := allowanceLogs[2]
				lateAllowance.Id = 0
				require.NoError(t, logDB.Create(&lateAllowance).Error)
				wantAllowance.FreeQuota += 25
				wantAllowance.UnrecordedCount++
				mixedAllowance, err := model.SumUsedQuota(t.Context(), filter, nil, true)
				require.NoError(t, err)
				assert.Equal(t, 500, mixedAllowance.Quota)
				assert.Equal(t, &wantAllowance, mixedAllowance.Finance)
				require.NoError(t, logDB.Model(&allowanceLogs[0]).Update("other", paid).Error)
				wantAllowance.FreeQuota -= 100
				wantAllowance.CostQuota += 10
				wantAllowance.CoveredRevenueQuota += 100
				wantAllowance.ProfitQuota += 90
				for range 2 {
					updated, err := model.SumUsedQuota(t.Context(), filter, nil, true)
					require.NoError(t, err)
					assert.Equal(t, &wantAllowance, updated.Finance)
					require.NoError(t, model.AttachQuotaFinance(t.Context(), nil, allowanceHour, allowanceHour+3599, "", false))
				}
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
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}, &model.User{}))
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			require.NoError(t, model.MigrateDashboardFinance())
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
			// A report must include pending logs alongside already indexed ones. The
			// same filter remains exact after an operator revises a historical bill.
			hour := start - start%3600
			require.NoError(t, model.AttachQuotaFinance(context.Background(), nil, hour, hour+7199, "", false))
			filter := model.LogFilter{StartTimestamp: start, EndTimestamp: start + 4}
			warm, err := model.SumUsedQuota(context.Background(), filter, nil, true)
			require.NoError(t, err)
			assert.Equal(t, allFinance, warm.Finance)
			late := model.Log{CreatedAt: start + 2, Type: model.LogTypeConsume, Quota: 50, Other: zero}
			require.NoError(t, logDB.Create(&late).Error)
			require.NoError(t, logDB.Model(&model.Log{}).Where("request_id = ?", "loss").Update("other", "{}").Error)
			mixed, err := model.SumUsedQuota(context.Background(), filter, nil, true)
			require.NoError(t, err)
			assert.Equal(t, 470, mixed.Quota)
			assert.Equal(t, &model.DashboardFinance{CostQuota: 65, CoveredRevenueQuota: 200, ProfitQuota: 135, RecordedCount: 3, ActualCount: 1, EstimatedCount: 2, UnrecordedCount: 3}, mixed.Finance)
			require.NoError(t, logDB.Delete(&late).Error)
			require.NoError(t, model.AttachQuotaFinance(context.Background(), nil, hour, hour+7199, "", false))
			repaired, err := model.SumUsedQuota(context.Background(), filter, nil, true)
			require.NoError(t, err)
			assert.Equal(t, 420, repaired.Quota)
			assert.Equal(t, &model.DashboardFinance{CostQuota: 65, CoveredRevenueQuota: 150, ProfitQuota: 85, RecordedCount: 2, ActualCount: 1, EstimatedCount: 1, UnrecordedCount: 3}, repaired.Finance)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			stat, err := model.SumUsedQuota(ctx, model.LogFilter{Source: model.LogSourceUsage, StartTimestamp: start, EndTimestamp: start + 4}, nil, true)
			require.Error(t, err)
			assert.Equal(t, model.Stat{}, stat, "failed queries must not return partial finance")
		})
	}
}
