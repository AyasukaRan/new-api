package service

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DSNs and Redis must target isolated test instances. Redis database 15 is
// cleared between dialects; these tests never connect to deployment defaults.
func TestFreeAllowanceFundingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct {
		name common.DatabaseType
		env  string
	}{
		{common.DatabaseTypeSQLite, ""}, {common.DatabaseTypeMySQL, "TEST_FREE_MYSQL_DSN"}, {common.DatabaseTypePostgreSQL, "TEST_FREE_POSTGRES_DSN"},
	} {
		t.Run(string(dialect.name), func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if dialect.env != "" {
				dsn := os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip(dialect.env + " is not configured")
				}
				if dialect.name == common.DatabaseTypeMySQL {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(4)
			if dialect.name == common.DatabaseTypeSQLite {
				sqlDB.SetMaxOpenConns(1)
			}
			oldDB, oldLogDB := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			oldRedis, oldBatch, oldRDB := common.RedisEnabled, common.BatchUpdateEnabled, common.RDB
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect.name, dialect.name)
			common.RedisEnabled = false
			common.BatchUpdateEnabled = false
			t.Cleanup(func() {
				model.DB, model.LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMain, oldLog)
				common.RedisEnabled, common.BatchUpdateEnabled, common.RDB = oldRedis, oldBatch, oldRDB
				require.NoError(t, sqlDB.Close())
			})
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
			}
			versionQuery := "select version()"
			if dialect.name == common.DatabaseTypeSQLite {
				versionQuery = "select sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database: %s", version)
			for _, useRedis := range []bool{false, true} {
				t.Run(fmt.Sprintf("redis_%t", useRedis), func(t *testing.T) {
					if useRedis {
						address := os.Getenv("TEST_FREE_REDIS_ADDR")
						if address == "" {
							t.Skip("TEST_FREE_REDIS_ADDR is not configured")
						}
						rdb := redis.NewClient(&redis.Options{Addr: address, DB: 15, MaxRetries: -1})
						require.NoError(t, rdb.Ping(context.Background()).Err())
						require.NoError(t, rdb.FlushDB(context.Background()).Err())
						common.RedisEnabled, common.RDB = true, rdb
						t.Cleanup(func() { common.RedisEnabled = false; require.NoError(t, rdb.Close()) })
					}
					runFreeAllowanceFundingCases(t, db, useRedis)
				})
			}
		})
	}
}

func newFreeAllowanceFixture(t *testing.T, db *gorm.DB) (*model.User, *model.Token, *model.SubscriptionPlan, *model.UserSubscription) {
	t.Helper()
	name := fmt.Sprintf("gift-%x", time.Now().UnixNano())
	user := &model.User{Username: name, AffCode: name, Quota: 1000, Group: "preserved", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	token := &model.Token{UserId: user.Id, Key: fmt.Sprintf("free-test-%d", user.Id), RemainQuota: 10000, Status: common.TokenStatusEnabled}
	require.NoError(t, db.Create(token).Error)
	plan := &model.SubscriptionPlan{Title: "DeepSeek gift", DurationUnit: model.SubscriptionDurationForever, FreeAllowance: true, ApplicableModels: "deepseek-*", TotalAmount: 100, QuotaResetPeriod: model.SubscriptionResetMonthly, AllowWalletOverflow: common.GetPointer(true)}
	require.NoError(t, plan.ValidateFreeAllowance())
	require.NoError(t, db.Create(plan).Error)
	model.InvalidateSubscriptionPlanCache(plan.Id)
	var sub *model.UserSubscription
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		sub, err = model.CreateUserSubscriptionFromPlanTx(tx, user.Id, plan, "admin")
		return err
	}))
	start := time.Unix(sub.StartTime, 0)
	assert.Equal(t, time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, start.Location()).Unix(), sub.NextResetTime)
	assert.EqualValues(t, 253402300799, sub.EndTime)
	t.Cleanup(func() {
		db.Where("user_id = ?", user.Id).Delete(&model.SubscriptionPreConsumeRecord{})
		db.Where("user_id = ?", user.Id).Delete(&model.UserSubscription{})
		db.Where("user_id = ?", user.Id).Delete(&model.Token{})
		db.Delete(&model.SubscriptionPlan{}, plan.Id)
		db.Unscoped().Delete(&model.User{}, user.Id)
		model.InvalidateSubscriptionPlanCache(plan.Id)
	})
	return user, token, plan, sub
}

func runFreeAllowanceFundingCases(t *testing.T, db *gorm.DB, useRedis bool) {
	t.Helper()
	for _, tc := range []struct {
		name                               string
		estimate, actual, used, reserve    int
		nonDS, tokenFail, reserveTokenFail bool
		forcePreConsume, asyncTask         bool
		wantFree, wantWallet               int64
	}{
		{name: "equal estimate still settles", estimate: 40, actual: 40, wantFree: 40},
		{name: "forced synchronous preconsume still uses gift", estimate: 40, actual: 40, forcePreConsume: true, wantFree: 40},
		{name: "async funding remains on its existing path", estimate: 20, actual: 20, forcePreConsume: true, asyncTask: true, wantWallet: 20},
		{name: "overestimate refunds wallet first", estimate: 150, actual: 75, wantFree: 75},
		{name: "actual crosses allowance boundary", estimate: 40, actual: 140, wantFree: 100, wantWallet: 40},
		{name: "exhausted allowance uses wallet", estimate: 10, actual: 10, used: 100, wantWallet: 10},
		{name: "unrelated model never uses gift", estimate: 20, actual: 20, nonDS: true, wantWallet: 20},
		{name: "token failure leaves funding unchanged", estimate: 20, tokenFail: true},
		{name: "retry reserve splits and settles", estimate: 40, reserve: 150, actual: 120, wantFree: 100, wantWallet: 20},
		{name: "retry token failure restores both portions", estimate: 40, reserve: 150, actual: 40, reserveTokenFail: true, wantFree: 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, token, _, sub := newFreeAllowanceFixture(t, db)
			if tc.used > 0 {
				require.NoError(t, db.Model(sub).Update("amount_used", tc.used).Error)
			}
			if tc.tokenFail || tc.reserveTokenFail {
				limit := 5
				if tc.reserveTokenFail {
					limit = 50
				}
				require.NoError(t, db.Model(token).Update("remain_quota", limit).Error)
			}
			info := &relaycommon.RelayInfo{UserId: user.Id, RequestId: fmt.Sprintf("free-case-%d", user.Id), OriginModelName: "deepseek-flash", TokenId: token.Id, TokenKey: token.Key}
			info.ForcePreConsume = tc.forcePreConsume
			if tc.asyncTask {
				info.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
			}
			if tc.nonDS {
				info.OriginModelName = "gpt-6"
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			session, apiErr := NewBillingSession(c, info, tc.estimate)
			if tc.tokenFail {
				require.Error(t, apiErr)
				require.Nil(t, session)
			} else {
				require.Nil(t, apiErr)
				require.NotNil(t, session)
				assert.Zero(t, info.FreeQuotaConsumed)
				if tc.reserve > 0 {
					err := session.ReserveBeforeSubmit(tc.reserve)
					if tc.reserveTokenFail {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				}
				require.NoError(t, session.Settle(tc.actual))
				require.NoError(t, session.Settle(tc.actual))
				assert.Equal(t, tc.wantFree, info.FreeQuotaConsumed)
				if !tc.nonDS && !tc.asyncTask {
					assert.Equal(t, tc.wantWallet, info.WalletQuotaDeducted)
				}
			}
			var after model.User
			require.NoError(t, db.First(&after, user.Id).Error)
			assert.EqualValues(t, 1000-tc.wantWallet, after.Quota)
			assert.Equal(t, "preserved", after.Group)
			var used model.UserSubscription
			require.NoError(t, db.First(&used, sub.Id).Error)
			assert.EqualValues(t, int64(tc.used)+tc.wantFree, used.AmountUsed)
			if useRedis {
				cached, err := model.GetUserQuota(user.Id, false)
				require.NoError(t, err)
				assert.Equal(t, after.Quota, cached)
			}
		})
	}
	t.Run("idempotent reserve settle and refund", func(t *testing.T) {
		user, _, _, sub := newFreeAllowanceFixture(t, db)
		id := fmt.Sprintf("gift-idempotent-%d", user.Id)
		for range 2 {
			r, err := model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 150, "reserve")
			require.NoError(t, err)
			assert.EqualValues(t, 100, r.FreeQuota)
			assert.EqualValues(t, 50, r.WalletQuota)
		}
		for range 2 {
			_, err := model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 0, "refund")
			require.NoError(t, err)
		}
		var after model.User
		require.NoError(t, db.First(&after, user.Id).Error)
		assert.Equal(t, 1000, after.Quota)
		require.NoError(t, db.First(sub, sub.Id).Error)
		assert.Zero(t, sub.AmountUsed)
		_, err := model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 10, "reserve")
		require.Error(t, err)
		if useRedis {
			cached, err := model.GetUserQuota(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, 1000, cached)
		}
	})
	t.Run("cross month and manual reset isolate old reservations", func(t *testing.T) {
		user, _, plan, sub := newFreeAllowanceFixture(t, db)
		prefix := fmt.Sprintf("gift-period-%d-", user.Id)
		for _, suffix := range []string{"refund", "settle"} {
			_, err := model.UpdateFreeAllowanceFunding(prefix+suffix, user.Id, "deepseek-flash", 30, "reserve")
			require.NoError(t, err)
		}
		oldMonth := time.Now().AddDate(0, -1, 0).Unix()
		require.NoError(t, db.Model(sub).Updates(map[string]any{"last_reset_time": oldMonth, "next_reset_time": time.Now().Unix() - 1}).Error)
		_, err := model.ResetDueSubscriptions(300)
		require.NoError(t, err)
		_, err = model.UpdateFreeAllowanceFunding(prefix+"new", user.Id, "deepseek-flash", 10, "reserve")
		require.NoError(t, err)
		_, err = model.UpdateFreeAllowanceFunding(prefix+"refund", user.Id, "deepseek-flash", 0, "refund")
		require.NoError(t, err)
		r, err := model.UpdateFreeAllowanceFunding(prefix+"settle", user.Id, "deepseek-flash", 40, "settle")
		require.NoError(t, err)
		assert.EqualValues(t, 30, r.FreeQuota)
		assert.EqualValues(t, 10, r.WalletQuota)
		require.NoError(t, db.First(sub, sub.Id).Error)
		assert.EqualValues(t, 10, sub.AmountUsed)
		resetTime := sub.NextResetTime
		_, err = model.AdminResetUserSubscriptionsByPlan(user.Id, plan.Id, false)
		require.NoError(t, err)
		_, err = model.UpdateFreeAllowanceFunding(prefix+"newer", user.Id, "deepseek-flash", 5, "reserve")
		require.NoError(t, err)
		_, err = model.UpdateFreeAllowanceFunding(prefix+"new", user.Id, "deepseek-flash", 0, "refund")
		require.NoError(t, err)
		require.NoError(t, db.First(sub, sub.Id).Error)
		assert.EqualValues(t, 5, sub.AmountUsed)
		assert.Equal(t, resetTime, sub.NextResetTime)
	})
	t.Run("concurrent last allowance is not overspent", func(t *testing.T) {
		user, _, _, sub := newFreeAllowanceFixture(t, db)
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for i := range 20 {
			wg.Go(func() {
				_, err := model.UpdateFreeAllowanceFunding(fmt.Sprintf("gift-concurrent-%d-%d", user.Id, i), user.Id, "deepseek-flash", 10, "reserve")
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		var after model.User
		require.NoError(t, db.First(&after, user.Id).Error)
		assert.Equal(t, 900, after.Quota)
		require.NoError(t, db.First(sub, sub.Id).Error)
		assert.EqualValues(t, 100, sub.AmountUsed)
	})
	t.Run("gifts cannot be self purchased and ordinary subscriptions remain eligible", func(t *testing.T) {
		user, _, plan, _ := newFreeAllowanceFixture(t, db)
		require.Error(t, model.PurchaseSubscriptionWithBalance(user.Id, plan.Id))
		for _, source := range []string{"order", "auto", "balance"} {
			require.Error(t, db.Transaction(func(tx *gorm.DB) error {
				_, err := model.CreateUserSubscriptionFromPlanTx(tx, user.Id, plan, source)
				return err
			}))
		}
		previousDefault := common.DefaultSubscriptionPlanId
		common.DefaultSubscriptionPlanId = plan.Id
		err := db.Transaction(func(tx *gorm.DB) error { return model.GrantDefaultSubscriptionTx(tx, user.Id) })
		common.DefaultSubscriptionPlanId = previousDefault
		require.NoError(t, err)
		var granted int64
		require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&granted).Error)
		assert.EqualValues(t, 1, granted)
		paid := &model.SubscriptionPlan{Title: "paid", DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, TotalAmount: 200}
		require.NoError(t, db.Create(paid).Error)
		model.InvalidateSubscriptionPlanCache(paid.Id)
		t.Cleanup(func() { db.Delete(paid); model.InvalidateSubscriptionPlanCache(paid.Id) })
		var paidSub *model.UserSubscription
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			paidSub, err = model.CreateUserSubscriptionFromPlanTx(tx, user.Id, paid, "admin")
			return err
		}))
		r, err := model.PreConsumeUserSubscription(fmt.Sprintf("legacy-%d", user.Id), user.Id, "gpt-6", 0, 25)
		require.NoError(t, err)
		assert.Equal(t, paidSub.Id, r.UserSubscriptionId)
	})
	t.Run("pending gift can be cancelled but not deleted before refund", func(t *testing.T) {
		user, _, _, sub := newFreeAllowanceFixture(t, db)
		id := fmt.Sprintf("gift-delete-%d", user.Id)
		_, err := model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 150, "reserve")
		require.NoError(t, err)
		_, err = model.AdminDeleteUserSubscription(sub.Id)
		require.ErrorContains(t, err, "pending requests")
		_, err = model.AdminInvalidateUserSubscription(sub.Id)
		require.NoError(t, err)
		_, err = model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 0, "refund")
		require.NoError(t, err)
		_, err = model.AdminDeleteUserSubscription(sub.Id)
		require.NoError(t, err)
		_, err = model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 0, "refund")
		require.NoError(t, err)
		var after model.User
		require.NoError(t, db.First(&after, user.Id).Error)
		assert.Equal(t, 1000, after.Quota)
	})
	if useRedis {
		t.Run("database rollback restores Redis reservation", func(t *testing.T) {
			user, _, _, sub := newFreeAllowanceFixture(t, db)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("free_allowance_test_failure", func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "subscription_pre_consume_records" {
					tx.AddError(errors.New("injected ledger failure"))
				}
			}))
			_, err := model.UpdateFreeAllowanceFunding(fmt.Sprintf("gift-rollback-%d", user.Id), user.Id, "deepseek-flash", 150, "reserve")
			require.NoError(t, db.Callback().Create().Remove("free_allowance_test_failure"))
			require.Error(t, err)
			var after model.User
			require.NoError(t, db.First(&after, user.Id).Error)
			assert.Equal(t, 1000, after.Quota)
			require.NoError(t, db.First(sub, sub.Id).Error)
			assert.Zero(t, sub.AmountUsed)
			cached, err := model.GetUserQuota(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, 1000, cached)
		})
		t.Run("unavailable Redis cannot bypass wallet reservation", func(t *testing.T) {
			user, _, _, sub := newFreeAllowanceFixture(t, db)
			previous := common.RDB
			unavailable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
			common.RDB = unavailable
			_, err := model.UpdateFreeAllowanceFunding(fmt.Sprintf("gift-cache-down-%d", user.Id), user.Id, "deepseek-flash", 150, "reserve")
			common.RDB = previous
			require.NoError(t, unavailable.Close())
			require.Error(t, err)
			var after model.User
			require.NoError(t, db.First(&after, user.Id).Error)
			assert.Equal(t, 1000, after.Quota)
			require.NoError(t, db.First(sub, sub.Id).Error)
			assert.Zero(t, sub.AmountUsed)
		})
		t.Run("cache credit failure preserves database refund and never double refunds", func(t *testing.T) {
			user, _, _, sub := newFreeAllowanceFixture(t, db)
			id := fmt.Sprintf("gift-credit-failure-%d", user.Id)
			_, err := model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 150, "reserve")
			require.NoError(t, err)
			previous := common.RDB
			unavailable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
			require.NoError(t, db.Callback().Update().After("gorm:update").Register("free_allowance_credit_failure", func(tx *gorm.DB) {
				if record, ok := tx.Statement.Dest.(*model.SubscriptionPreConsumeRecord); ok && record.Status == "refunded" {
					common.RDB = unavailable
				}
			}))
			_, err = model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 0, "refund")
			common.RDB = previous
			require.NoError(t, db.Callback().Update().Remove("free_allowance_credit_failure"))
			require.NoError(t, unavailable.Close())
			require.NoError(t, err)
			_, err = model.UpdateFreeAllowanceFunding(id, user.Id, "deepseek-flash", 0, "refund")
			require.NoError(t, err)
			var after model.User
			require.NoError(t, db.First(&after, user.Id).Error)
			assert.Equal(t, 1000, after.Quota)
			require.NoError(t, db.First(sub, sub.Id).Error)
			assert.Zero(t, sub.AmountUsed)
			cached, err := previous.HGet(t.Context(), fmt.Sprintf("user:%d", user.Id), "Quota").Int()
			require.NoError(t, err)
			assert.Equal(t, 950, cached)
			// An expired hash is hydrated from the committed refund. Do not blindly
			// replay a timed-out increment: it may already have reached Redis.
			require.NoError(t, previous.Del(t.Context(), fmt.Sprintf("user:%d", user.Id)).Err())
			cached, err = model.GetUserQuota(user.Id, false)
			require.NoError(t, err)
			assert.Equal(t, 1000, cached)
		})
	}
}
