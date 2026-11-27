package model

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestSatelliteAccountingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
			if dialect != "sqlite" && dsn == "" {
				t.Skip("test database is not configured")
			}
			oldDB, oldRedis, oldBatch := DB, common.RedisEnabled, common.BatchUpdateEnabled
			oldMain, oldLog := common.UsingMainDatabase(common.DatabaseTypeMySQL), common.UsingMainDatabase(common.DatabaseTypePostgreSQL)
			common.RedisEnabled, common.BatchUpdateEnabled = false, true
			t.Setenv("SATELLITE_ROLE", "satellite")
			t.Setenv("SATELLITE_NODE_ID", "accounting-test-node")
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				DB, common.RedisEnabled, common.BatchUpdateEnabled = oldDB, oldRedis, oldBatch
				kind := common.DatabaseTypeSQLite
				if oldMain {
					kind = common.DatabaseTypeMySQL
				}
				if oldLog {
					kind = common.DatabaseTypePostgreSQL
				}
				common.SetDatabaseTypes(kind, kind)
				initCol()
			})
			open := func(prefix string) *gorm.DB {
				var driver gorm.Dialector
				switch dialect {
				case "sqlite":
					driver = sqlite.Open(":memory:")
				case "mysql":
					driver = mysql.Open(dsn)
				case "postgres":
					driver = postgres.Open(dsn)
				}
				db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: prefix}})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				sqlDB.SetMaxOpenConns(1)
				tables := []any{&SatelliteAccountingState{}, &SubscriptionPreConsumeRecord{}, &UserSubscription{}, &SubscriptionPlan{}, &Channel{}, &Token{}, &User{}}
				require.NoError(t, db.Migrator().DropTable(tables...))
				// The representative pre-feature schema/data survive the additive migration.
				require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{}))
				require.NoError(t, db.Create(&User{Id: 1, Username: "satellite-user", Password: "hash", Quota: 1000, UsedQuota: 10, RequestCount: 2, AffCode: "satellite-aff", AuthVersion: 1}).Error)
				require.NoError(t, db.Create(&Token{Id: 1, UserId: 1, Key: "satellite-test-key", Name: "test", RemainQuota: 500, UsedQuota: 20}).Error)
				require.NoError(t, db.Create(&Channel{Id: 1, Name: "satellite-channel", Key: "upstream-test", UsedQuota: 30}).Error)
				for range 2 {
					require.NoError(t, db.AutoMigrate(&SatelliteAccountingState{}))
				}
				t.Cleanup(func() { assert.NoError(t, db.Migrator().DropTable(tables...)); assert.NoError(t, sqlDB.Close()) })
				return db
			}
			primary, satellite := open("satacct_primary_"), open("satacct_local_")
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, primary.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			anchors := func() []SatelliteAccountingAnchor {
				var result []SatelliteAccountingAnchor
				require.NoError(t, primary.Transaction(func(tx *gorm.DB) error {
					var err error
					result, err = ExportSatelliteAccountingAnchors(tx, "accounting-test-node")
					return err
				}))
				return result
			}
			reconcile := func(a []SatelliteAccountingAnchor) {
				require.NoError(t, satellite.Transaction(func(tx *gorm.DB) error { return ReconcileSatelliteAccountingAnchors(tx, a) }))
			}
			reconcile(anchors())
			DB = satellite
			redisAddress := os.Getenv("TEST_SATELLITE_REDIS_ADDR")
			if redisAddress != "" {
				oldRDB := common.RDB
				client := redis.NewClient(&redis.Options{Addr: redisAddress, DB: 14, MaxRetries: -1})
				require.NoError(t, client.Ping(t.Context()).Err())
				require.NoError(t, client.FlushDB(t.Context()).Err())
				common.RDB, common.RedisEnabled = client, true
				t.Cleanup(func() { assert.NoError(t, client.Close()); common.RDB = oldRDB })
				_, err := GetUserCache(1)
				require.NoError(t, err)
				_, err = cacheInitToken(getTokenFromDB(t, 1))
				require.NoError(t, err)
				t.Log("real Redis enabled: satellite database reservations and primary in-flight cache deltas")
			}
			reserved, err := TryReserveUserQuota(1, 100)
			require.NoError(t, err)
			require.True(t, reserved)
			reserved, err = TryReserveTokenQuota(1, "satellite-test-key", 100, false)
			require.NoError(t, err)
			require.True(t, reserved)
			require.NoError(t, IncreaseUserQuota(1, 40, false))
			require.NoError(t, IncreaseTokenQuota(1, "satellite-test-key", 40))
			UpdateUserUsedQuotaAndRequestCount(1, 60)
			UpdateChannelUsedQuota(1, 60)
			uploaded, err := GetSatelliteAccountingDeltas(context.Background())
			require.NoError(t, err)
			require.Len(t, uploaded, 3)
			assert.Equal(t, 940, getUserQuotaFromDB(t, 1), "satellite bypasses memory batching")
			reserved, err = TryReserveUserQuota(1, 941)
			require.NoError(t, err)
			assert.False(t, reserved)
			unchanged, err := GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			assert.Equal(t, uploaded, unchanged)
			// Losing the journal write rolls the balance back in the same transaction.
			const hook = "satellite_accounting_reject_journal"
			require.NoError(t, satellite.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
				if strings.HasSuffix(tx.Statement.Table, "satellite_accounting_states") {
					tx.AddError(errors.New("journal unavailable"))
				}
			}))
			assert.Error(t, DecreaseUserQuota(1, 7, false))
			assert.Equal(t, 940, getUserQuotaFromDB(t, 1))
			require.NoError(t, satellite.Callback().Create().Remove(hook))
			// An in-flight local reservation after upload must survive the returned anchor.
			require.NoError(t, DecreaseUserQuota(1, 30, false))
			require.NoError(t, DecreaseTokenQuota(1, "satellite-test-key", 30))
			DB = primary
			if redisAddress != "" {
				// The two deployments have independent Redis stores. Simulate the
				// primary cache with its own outstanding reservation not yet in SQL.
				require.NoError(t, common.RDB.FlushDB(t.Context()).Err())
				_, err := GetUserCache(1)
				require.NoError(t, err)
				_, err = cacheInitToken(getTokenFromDB(t, 1))
				require.NoError(t, err)
				reserved, err := cacheTryReserveUserQuota(1, 25)
				require.NoError(t, err)
				require.Equal(t, cacheQuotaOK, reserved)
				reserved, err = cacheTryReserveTokenQuota(1, "satellite-test-key", 25)
				require.NoError(t, err)
				require.Equal(t, cacheQuotaOK, reserved)
			}
			require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", uploaded))
			require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", uploaded))
			assert.Equal(t, 940, getUserQuotaFromDB(t, 1))
			token := getTokenFromDB(t, 1)
			assert.Equal(t, 440, token.RemainQuota)
			assert.Equal(t, 80, token.UsedQuota)
			if redisAddress != "" {
				cachedUser, err := GetUserCache(1)
				require.NoError(t, err)
				assert.Equal(t, 915, cachedUser.Quota, "duplicate import preserves the primary's outstanding 25-unit reservation")
				cachedToken, err := cacheGetTokenByKey("satellite-test-key")
				require.NoError(t, err)
				assert.Equal(t, 415, cachedToken.RemainQuota)
				assert.Equal(t, 105, cachedToken.UsedQuota)
				common.RedisEnabled = false
			}
			var user User
			require.NoError(t, primary.First(&user, 1).Error)
			assert.Equal(t, 70, user.UsedQuota)
			assert.Equal(t, 3, user.RequestCount)
			// Local primary recharge is independent of the satellite's cumulative debit.
			require.NoError(t, primary.Model(&User{}).Where("id = ?", 1).Update("quota", 990).Error)
			response := anchors()
			reconcile(response)
			reconcile(response)
			DB = satellite
			assert.Equal(t, 960, getUserQuotaFromDB(t, 1))
			assert.Equal(t, 410, getTokenFromDB(t, 1).RemainQuota)
			current, err := GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			DB = primary
			require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", current))
			assert.Equal(t, 960, getUserQuotaFromDB(t, 1))
			require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", uploaded))
			assert.Equal(t, 960, getUserQuotaFromDB(t, 1), "old revisions cannot undo settled charges")
			bad := append([]SatelliteAccountingDelta(nil), current...)
			for i := range bad {
				if bad[i].Entity == "user" {
					bad[i].QuotaDelta--
				}
			}
			assert.ErrorIs(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", bad), ErrSatelliteAccounting)
			assert.Equal(t, 960, getUserQuotaFromDB(t, 1))
			assert.Error(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", []SatelliteAccountingDelta{{Entity: "user", EntityID: 999, Revision: 1, QuotaDelta: -1}}))
			assert.ErrorIs(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", []SatelliteAccountingDelta{{Entity: "user", EntityID: 1, Revision: 99, QuotaDelta: math.MinInt64}}), ErrSatelliteAccounting)
			// Subscription generation boundaries remain authoritative on the primary.
			now := time.Now().Unix()
			for _, db := range []*gorm.DB{primary, satellite} {
				require.NoError(t, db.Create(&SubscriptionPlan{Id: 1, Title: "paid", QuotaResetPeriod: SubscriptionResetNever}).Error)
				require.NoError(t, db.Create(&UserSubscription{Id: 1, UserId: 1, PlanId: 1, AmountTotal: 1000, Status: "active", StartTime: now - 3600, EndTime: now + 3600, LastResetTime: now - 3600}).Error)
			}
			reconcile(anchors())
			DB = satellite
			paid, err := PreConsumeUserSubscription("sat-paid", 1, "test", 0, 40)
			require.NoError(t, err)
			assert.Equal(t, int64(40), paid.PreConsumed)
			require.NoError(t, PostConsumeUserSubscriptionDeltaForRequest(1, "sat-paid", -10))
			var sub UserSubscription
			require.NoError(t, satellite.First(&sub, 1).Error)
			assert.Equal(t, int64(30), sub.AmountUsed)
			current, err = GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			DB = primary
			require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", current))
			require.NoError(t, primary.First(&sub, 1).Error)
			assert.Equal(t, int64(30), sub.AmountUsed)
			require.NoError(t, primary.Model(&UserSubscription{}).Where("id = ?", 1).Updates(map[string]any{"reset_generation": 1, "amount_used": 0}).Error)
			require.NoError(t, satellite.Model(&UserSubscription{}).Where("id = ?", 1).Update("reset_generation", 1).Error)
			reconcile(anchors())
			DB = satellite
			require.NoError(t, PostConsumeUserSubscriptionDeltaForRequest(1, "sat-paid", 5))
			require.NoError(t, satellite.First(&sub, 1).Error)
			assert.Zero(t, sub.AmountUsed, "old-period settlement must not touch the new period")
			current, err = GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			DB = primary
			require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", current))
			require.NoError(t, primary.First(&sub, 1).Error)
			assert.Zero(t, sub.AmountUsed, "late old-generation deltas are acknowledged only")
			DB = satellite
			_, err = PreConsumeUserSubscription("sat-refund", 1, "test", 0, 20)
			require.NoError(t, err)
			require.NoError(t, RefundSubscriptionPreConsume("sat-refund"))
			require.NoError(t, RefundSubscriptionPreConsume("sat-refund"))
			require.NoError(t, satellite.First(&sub, 1).Error)
			assert.Zero(t, sub.AmountUsed)
			assert.Error(t, PostConsumeUserSubscriptionDeltaForRequest(1, "missing-request", 1))
			// A gift and its wallet overflow are journaled within their existing one
			// transaction; replaying settle must not add either amount a second time.
			for _, db := range []*gorm.DB{primary, satellite} {
				require.NoError(t, db.Create(&SubscriptionPlan{Id: 2, Title: "gift", FreeAllowance: true, TotalAmount: 100, ApplicableModels: "gift-model", QuotaResetPeriod: SubscriptionResetNever}).Error)
				require.NoError(t, db.Create(&UserSubscription{Id: 2, UserId: 1, PlanId: 2, FreeAllowance: true, ApplicableModels: "gift-model", AmountTotal: 100, Status: "active", StartTime: now - 3600, EndTime: now + 3600, LastResetTime: now - 3600, AllowWalletOverflow: true}).Error)
			}
			reconcile(anchors())
			DB = satellite
			beforeQuota := getUserQuotaFromDB(t, 1)
			gift, err := UpdateFreeAllowanceFunding("sat-gift", 1, "gift-model", 150, "reserve")
			require.NoError(t, err)
			assert.Equal(t, int64(100), gift.FreeQuota)
			assert.Equal(t, int64(50), gift.WalletQuota)
			gift, err = UpdateFreeAllowanceFunding("sat-gift", 1, "gift-model", 120, "settle")
			require.NoError(t, err)
			assert.Equal(t, int64(20), gift.WalletQuota)
			beforeReplay, err := GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			_, err = UpdateFreeAllowanceFunding("sat-gift", 1, "gift-model", 120, "settle")
			require.NoError(t, err)
			afterReplay, err := GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			assert.Equal(t, beforeReplay, afterReplay)
			assert.Equal(t, beforeQuota-20, getUserQuotaFromDB(t, 1))
			// Delayed cross-node totals can exceed a period cap. Reconciliation
			// must not make refunds fail while the reduced total is still over it.
			_, err = PreConsumeUserSubscription("sat-overage-refund", 1, "test", 0, 20)
			require.NoError(t, err)
			require.NoError(t, primary.Model(&UserSubscription{}).Where("id = ?", 2).Updates(map[string]any{"reset_generation": 1, "amount_used": 0}).Error)
			require.NoError(t, satellite.Model(&UserSubscription{}).Where("id = ?", 2).Update("reset_generation", 1).Error)
			reconcile(anchors())
			_, err = UpdateFreeAllowanceFunding("sat-gift-overage", 1, "gift-model", 150, "reserve")
			require.NoError(t, err)
			require.NoError(t, primary.Model(&UserSubscription{}).Where("id = ?", 1).Update("amount_used", 1100).Error)
			require.NoError(t, primary.Model(&UserSubscription{}).Where("id = ?", 2).Update("amount_used", 150).Error)
			var overage []SatelliteAccountingAnchor
			for _, anchor := range anchors() {
				if anchor.Entity == "subscription" {
					overage = append(overage, anchor)
				}
			}
			reconcile(overage)
			require.NoError(t, RefundSubscriptionPreConsume("sat-overage-refund"))
			require.NoError(t, satellite.First(&sub, 1).Error)
			assert.Equal(t, int64(1100), sub.AmountUsed)
			assert.ErrorContains(t, PostConsumeUserSubscriptionDeltaForRequest(1, "sat-overage-refund", 1), "quota insufficient")
			_, err = UpdateFreeAllowanceFunding("sat-gift-overage", 1, "gift-model", 0, "refund")
			require.NoError(t, err)
			var giftSub UserSubscription
			require.NoError(t, satellite.First(&giftSub, 2).Error)
			assert.Equal(t, int64(150), giftSub.AmountUsed)
			assert.Equal(t, beforeQuota-20, getUserQuotaFromDB(t, 1))
			afterReplay, err = GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			// A rollover may be performed only by the primary, never by a partitioned node.
			require.NoError(t, satellite.Model(&SubscriptionPlan{}).Where("id = ?", 1).Update("quota_reset_period", SubscriptionResetDaily).Error)
			require.NoError(t, satellite.Model(&UserSubscription{}).Where("id = ?", 1).Updates(map[string]any{"last_reset_time": now - 172800, "next_reset_time": now - 86400}).Error)
			InvalidateSubscriptionPlanCache(1)
			_, err = PreConsumeUserSubscription("sat-period-wait", 1, "test", 0, 1)
			require.ErrorContains(t, err, "awaiting primary synchronization")
			// Issued identities retain a receipt after hard deletion. Late charges
			// must finish syncing without recreating a deleted account or channel.
			DB = primary
			require.NoError(t, primary.Create(&User{Id: 2, Username: "deleted-user", Password: "hash", AffCode: "deleted-aff"}).Error)
			require.NoError(t, primary.Create(&Token{Id: 2, UserId: 2, Key: "deleted-token"}).Error)
			require.NoError(t, primary.Create(&Channel{Id: 2, Name: "deleted-channel", Key: "deleted-upstream"}).Error)
			require.NoError(t, primary.Create(&UserSubscription{Id: 3, UserId: 2, PlanId: 1, ResetGeneration: 2}).Error)
			anchors()
			for _, item := range []struct {
				entity     string
				id         int
				generation int64
				row        any
			}{
				{"user", 2, 0, &User{}}, {"token", 2, 0, &Token{}},
				{"channel", 2, 0, &Channel{}}, {"subscription", 3, 2, &UserSubscription{}},
			} {
				require.NoError(t, primary.Unscoped().Delete(item.row, item.id).Error)
				delta := SatelliteAccountingDelta{Entity: item.entity, EntityID: item.id, Generation: item.generation, Revision: 1, UsedQuotaDelta: 7}
				if item.entity == "user" || item.entity == "token" {
					delta.QuotaDelta = -7
				}
				require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", []SatelliteAccountingDelta{delta}))
				require.NoError(t, ApplySatelliteAccountingDeltas(t.Context(), "accounting-test-node", []SatelliteAccountingDelta{delta}))
				var receipt SatelliteAccountingState
				require.NoError(t, primary.Where("node_id = ? AND entity = ? AND entity_id = ? AND generation = ?", "accounting-test-node", item.entity, item.id, item.generation).First(&receipt).Error)
				assert.Equal(t, delta, receipt.delta())
				var count int64
				require.NoError(t, primary.Unscoped().Model(item.row).Where("id = ?", item.id).Count(&count).Error)
				assert.Zero(t, count)
				assert.Error(t, ApplySatelliteAccountingDeltas(t.Context(), "unregistered-node", []SatelliteAccountingDelta{delta}))
			}
			DB = satellite
			// Re-running the migration preserves receipts, keys, data and idempotence.
			for range 2 {
				require.NoError(t, satellite.AutoMigrate(&SatelliteAccountingState{}))
			}
			remaining, err := GetSatelliteAccountingDeltas(t.Context())
			require.NoError(t, err)
			assert.Equal(t, afterReplay, remaining)
		})
	}
}
