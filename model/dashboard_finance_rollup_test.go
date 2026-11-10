package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Dedicated DSNs must point to empty test databases. The log handle can be
// separate from the primary database in production, so leave DB untouched.
func TestDashboardFinanceRollup(t *testing.T) {
	for _, dialect := range []struct{ name, env string }{
		{"sqlite", ""}, {"mysql", "FINANCE_TEST_MYSQL_DSN"}, {"postgres", "FINANCE_TEST_POSTGRES_DSN"},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			dsn := "local"
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skipf("%s is not configured", dialect.env)
				}
			} else {
				prev := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "finance.db")
				t.Cleanup(func() { common.SQLitePath = prev })
			}
			t.Setenv("FINANCE_MATRIX_DSN", dsn)
			db, kind, err := chooseDB("FINANCE_MATRIX_DSN", true)
			require.NoError(t, err)
			conn, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			for _, table := range []string{"logs", "dashboard_finance_hours", "dashboard_finance_entries"} {
				require.False(t, db.Migrator().HasTable(table), "use an empty, dedicated test database")
			}
			prevDB, prevKind := LOG_DB, common.LogDatabaseType()
			LOG_DB = db
			common.SetLogDatabaseType(kind)
			initCol()
			t.Cleanup(func() {
				LOG_DB = prevDB
				common.SetLogDatabaseType(prevKind)
				initCol()
			})
			require.NoError(t, db.AutoMigrate(&Log{}))
			t.Cleanup(func() {
				assert.NoError(t, db.Migrator().DropTable("logs", "dashboard_finance_hours", "dashboard_finance_entries"))
			})

			const first int64 = 24 * 3600
			const next = first + 3600
			metadata := func(cost int) string {
				return fmt.Sprintf(`{"client_tool":"curl","root_info":{"node_name":"node"},"admin_info":{"cost":{"cost_ratio":1,"base_quota":100,"revenue_quota":100,"estimated":true,"cost_quota":%d,"profit_quota":%d}}}`, cost, 100-cost)
			}
			a := Log{Id: 100, CreatedAt: first + 10, Type: LogTypeConsume, Quota: 100, Username: "alice", ModelName: "model", Other: metadata(25)}
			require.NoError(t, db.Create(&a).Error)
			require.NoError(t, MigrateDashboardFinance())
			require.NoError(t, MigrateDashboardFinance())

			check := func(user string, hour int64, n int64, cost float64) {
				t.Helper()
				rows, err := loadDashboardFinanceHours(t.Context(), hour, hour, user)
				require.NoError(t, err)
				var recorded DashboardFinance
				for _, row := range rows {
					recorded.add(row)
				}
				assert.Equal(t, n, recorded.RecordedCount)
				assert.Equal(t, cost, recorded.CostQuota)
				assert.Equal(t, float64(n)*100-cost, recorded.ProfitQuota)
			}
			check("alice", first, 1, 25)
			// Upgrade the released projection table, retaining its existing
			// facts and treating absent allowance deductions as zero.
			type legacyFinanceEntry struct {
				LogID     int `gorm:"primaryKey;autoIncrement:false"`
				CostQuota *float64
				Estimated bool
			}
			require.NoError(t, db.Migrator().DropTable(&DashboardFinanceEntry{}))
			require.NoError(t, db.Table("dashboard_finance_entries").Migrator().CreateTable(&legacyFinanceEntry{}))
			legacyCost := float64(25)
			require.NoError(t, db.Table("dashboard_finance_entries").Create(&legacyFinanceEntry{LogID: a.Id, CostQuota: &legacyCost, Estimated: true}).Error)
			require.NoError(t, MigrateDashboardFinance())
			require.NoError(t, MigrateDashboardFinance())
			var upgraded DashboardFinanceEntry
			require.NoError(t, db.First(&upgraded, "log_id = ?", a.Id).Error)
			assert.Nil(t, upgraded.FreeQuota)
			require.NotNil(t, upgraded.CostQuota)
			assert.Equal(t, legacyCost, *upgraded.CostQuota)
			assert.Error(t, db.Create(&DashboardFinanceEntry{LogID: a.Id}).Error, "migration must retain log ID uniqueness")
			upgradeStat, err := SumUsedQuota(t.Context(), LogFilter{StartTimestamp: first, EndTimestamp: first + 3599}, nil, true)
			require.NoError(t, err)
			assert.Equal(t, &DashboardFinance{CostQuota: 25, CoveredRevenueQuota: 100, ProfitQuota: 75, RecordedCount: 1, EstimatedCount: 1}, upgradeStat.Finance)
			if kind == common.DatabaseTypeMySQL {
				check("ALICE", first, 1, 25)
			} else {
				check("ALICE", first, 0, 0)
			}
			b := Log{Id: 300, CreatedAt: first + 20, Type: LogTypeConsume, Quota: 100, Username: "bob", ModelName: "model", Other: metadata(0)}
			require.NoError(t, db.Create(&b).Error)
			check("", first, 2, 25)
			check("bob", first, 1, 0)

			// A transaction with an older allocated ID can commit after the
			// higher watermark was read. The maximum ID is unchanged.
			late := Log{Id: 200, CreatedAt: first + 15, Type: LogTypeConsume, Quota: 100, Username: "bob", ModelName: "model", Other: metadata(5)}
			require.NoError(t, db.Create(&late).Error)
			check("bob", first, 2, 5)
			require.NoError(t, db.Delete(&late).Error)

			// Historical billing and source backfills can write directly with SQL.
			require.NoError(t, db.Table("logs").Where("id = ?", a.Id).Update("other", metadata(130)).Error)
			check("alice", first, 1, 130)
			require.NoError(t, db.Table("logs").Where("id = ?", a.Id).Update("created_at", next+1).Error)
			check("alice", first, 0, 0)
			check("alice", next, 1, 130)
			require.NoError(t, db.Delete(&b).Error)
			check("", first, 0, 0)
			require.NoError(t, MigrateDashboardFinance())
			check("", next, 1, 130)

			// Legacy dashboards skipped malformed client metadata. Log stats
			// validate only the cost snapshot, including after a warm read.
			invalidSource := strings.Replace(metadata(20), `"client_tool":"curl"`, `"client_tool":{}`, 1)
			require.NoError(t, db.Table("logs").Where("id = ?", a.Id).Update("other", invalidSource).Error)
			for range 2 {
				stat, err := SumUsedQuota(t.Context(), LogFilter{Source: LogSourceUsage, StartTimestamp: next, EndTimestamp: next + 3599}, nil, true)
				require.NoError(t, err)
				require.NotNil(t, stat.Finance)
				assert.EqualValues(t, 1, stat.Finance.RecordedCount)
				assert.Equal(t, 20.0, stat.Finance.CostQuota)
				check("", next, 0, 0)
			}
			// Remove a guard as can happen after restoring a backup under an
			// account that cannot create triggers. A later migration must
			// discard facts calculated before the unguarded edit.
			drop := "DROP TRIGGER dashboard_finance_log_update_v1"
			if kind == common.DatabaseTypePostgreSQL {
				drop += " ON logs"
			}
			_, err = conn.ExecContext(t.Context(), drop)
			require.NoError(t, err)
			require.NoError(t, db.Model(&a).Update("other", metadata(23)).Error)
			require.NoError(t, MigrateDashboardFinance())
			require.NoError(t, MigrateDashboardFinance())
			check("", next, 1, 23)
			stat, err := SumUsedQuota(t.Context(), LogFilter{StartTimestamp: next, EndTimestamp: next + 3599}, nil, true)
			require.NoError(t, err)
			assert.Equal(t, 23.0, stat.Finance.CostQuota)

			// Move that already-warm bill into another hour precisely as its
			// pending neighbour is being refreshed. A report must count it once.
			last := next + 3600
			require.NoError(t, db.Create(&Log{CreatedAt: last + 1, Type: LogTypeConsume, Quota: 100, Other: metadata(7)}).Error)
			moved := false
			hook := "test:move_bill_during_rollup"
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
				if moved || tx.Statement.Table != "dashboard_finance_hours" {
					return
				}
				moved = true
				tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&Log{}).Where("id = ?", a.Id).Update("created_at", last+2).Error)
			}))
			t.Cleanup(func() { _ = db.Callback().Create().Remove(hook) })
			cells, err := loadDashboardFinanceHours(t.Context(), next, last, "")
			require.NoError(t, err)
			require.True(t, moved)
			var total DashboardFinance
			for key, cell := range cells {
				assert.Equal(t, last, key.CreatedAt)
				total.add(cell)
			}
			assert.EqualValues(t, 2, total.RecordedCount)
			assert.Equal(t, 30.0, total.CostQuota)

		})
	}
}

func TestRecordedLogFinanceAllowance(t *testing.T) {
	const cost = `"admin_info":{"cost":{"cost_ratio":0,"base_quota":100,"free_quota":100,"cost_quota":0,"revenue_quota":0,"profit_quota":0,"estimated":true}}`
	for _, tc := range []struct {
		name, other string
		free        int64
		known       bool
	}{
		{"fully free", `{"free_quota":100,` + cost + `}`, 100, true},
		{"missing cost", `{"free_quota":100}`, 100, false},
		{"invalid cost", `{"free_quota":100,"admin_info":{"cost":"invalid"}}`, 100, false},
		{"cost mismatch", `{"free_quota":50,` + cost + `}`, 50, false},
		{"cost cannot imply free", `{` + cost + `}`, 0, false},
		{"negative allowance", `{"free_quota":-1}`, 0, false},
		{"allowance exceeds usage", `{"free_quota":101}`, 0, false},
		{"fractional allowance", `{"free_quota":0.5}`, 0, false},
		{"invalid source metadata", `{"client_tool":{},"free_quota":100,` + cost + `}`, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finance, known := recordedLogFinanceJSON(100, tc.other)
			assert.Equal(t, tc.known, known)
			assert.Equal(t, tc.free, finance.FreeQuota)
			assert.Zero(t, finance.ProfitQuota)
			entry := projectDashboardLogFinance(&Log{Quota: 100, Other: tc.other}, make(map[dashboardFinanceKey]DashboardFinance))
			require.NotNil(t, entry.FreeQuota)
			assert.Equal(t, tc.free, *entry.FreeQuota)
			assert.Equal(t, tc.known, entry.CostQuota != nil)
		})
	}
}
