package controller

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelManagementTest(t *testing.T) int {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousCache, previousRedis, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath = previousMaster, previousCache, previousRedis, previousSQLite
	})
	t.Setenv("SQL_DSN", os.Getenv("TEST_CHANNEL_SQL_DSN"))
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "channel.db")
	require.NoError(t, model.InitDB())
	database := model.DB
	sqlDB, err := database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.LOG_DB = database
	common.SetLogDatabaseType(common.MainDatabaseType())
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.Log{}, &model.AuditLog{}))
	root := &model.User{Username: "multi-key-review-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, database.Create(root).Error)
	t.Cleanup(func() { require.NoError(t, database.Unscoped().Delete(root).Error) })
	versionQuery := "SELECT VERSION()"
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, database.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database=%s version=%s", common.MainDatabaseType(), version)
	return root.Id
}

func TestMultiKeyStatusReturnsMatchingBalancesAndMaskedCredentials(t *testing.T) {
	rootID := setupChannelManagementTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ChannelBalanceSample{}))
	keys := []string{"abc", "sk-fixture-long-key-secret", "account-part|secret-part", `{"private_key":"private-value"}`}
	channel := &model.Channel{Name: t.Name(), Type: 1, Key: strings.Join(keys, "\n"), Models: "test-model", Group: "default", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: len(keys),
			MultiKeyStatusList:     map[int]int{1: common.ChannelStatusManuallyDisabled, 2: common.ChannelStatusAutoDisabled},
			MultiKeyDisabledReason: map[int]string{1: "rejected " + keys[1], 2: "account-part and secret-part rejected"},
		},
	}
	require.NoError(t, channel.Insert())
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Delete(&model.ChannelBalanceSample{}).Error)
		require.NoError(t, channel.Delete())
	})
	queryStatus := func(request MultiKeyManageRequest) MultiKeyStatusResponse {
		t.Helper()
		request.ChannelId, request.Action = channel.Id, "get_key_status"
		payload, err := common.Marshal(request)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", rootID)
		c.Set("role", common.RoleRootUser)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		ManageMultiKeys(c)
		var result struct {
			Success bool                   `json:"success"`
			Data    MultiKeyStatusResponse `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
		require.True(t, result.Success, recorder.Body.String())
		for _, secret := range append(keys, "account-part", "secret-part", "private-value") {
			assert.NotContains(t, recorder.Body.String(), secret)
		}
		assert.NotContains(t, recorder.Body.String(), "configuration_hash")
		assert.NotContains(t, recorder.Body.String(), "key_balances_json")
		assert.Contains(t, recorder.Body.String(), `"balance_monitor":`)
		return result.Data
	}
	initial := queryStatus(MultiKeyManageRequest{})
	assert.Nil(t, initial.BalanceMonitor, "never queried must remain unknown, not zero")
	assert.False(t, initial.BalanceQueryDisabled)
	require.Len(t, initial.Keys, 4)
	assert.Equal(t, "********", initial.Keys[0].KeyPreview)
	assert.Equal(t, model.MaskTokenKey(keys[1]), initial.Keys[1].KeyPreview)
	assert.Equal(t, "********", initial.Keys[2].KeyPreview)
	assert.Equal(t, "********", initial.Keys[3].KeyPreview)
	assert.Contains(t, initial.Keys[1].Reason, "[redacted]")
	assert.Contains(t, initial.Keys[2].Reason, "[redacted]")

	complete := &model.ChannelBalanceSample{StartedAt: 100, CheckedAt: 100, KnownBalance: 5.5, Success: true,
		KeyBalances: []model.ChannelKeyBalance{
			{Index: 0, Balance: common.GetPointer(1.5)}, {Index: 1, Balance: common.GetPointer(float64(0))},
			{Index: 2, Balance: common.GetPointer(float64(-3))}, {Index: 3, Balance: common.GetPointer(float64(4))},
		},
	}
	require.NoError(t, model.RecordChannelBalanceSample(channel, complete))
	filtered := queryStatus(MultiKeyManageRequest{Status: common.GetPointer(common.ChannelStatusManuallyDisabled), Page: 20, PageSize: 1})
	require.Len(t, filtered.Keys, 1)
	assert.Equal(t, 1, filtered.Keys[0].Index)
	assert.Equal(t, 1, filtered.Page)
	assert.Equal(t, 1, filtered.Total)
	assert.Equal(t, 2, filtered.EnabledCount)
	assert.Equal(t, 1, filtered.ManualDisabledCount)
	assert.Equal(t, 1, filtered.AutoDisabledCount)
	require.NotNil(t, filtered.BalanceMonitor)
	assert.Equal(t, complete.KeyBalances, filtered.BalanceMonitor.KeyBalances, "pagination must preserve original key indexes in the snapshot")
	assert.Equal(t, common.GetPointer(5.5), filtered.BalanceMonitor.Balance)

	partial := &model.ChannelBalanceSample{StartedAt: 200, CheckedAt: 200, KnownBalance: 4, Partial: true,
		KeyBalances: []model.ChannelKeyBalance{
			{Index: 0, Error: "balance_query_failed"}, {Index: 1, Balance: common.GetPointer(float64(0))},
			{Index: 2, Balance: common.GetPointer(float64(-3))}, {Index: 3, Balance: common.GetPointer(float64(4))},
		},
	}
	require.NoError(t, model.RecordChannelBalanceSample(channel, partial))
	require.NoError(t, model.DB.Model(channel).Update("setting", `{"balance_query_disabled":true}`).Error)
	failed := queryStatus(MultiKeyManageRequest{Page: 2, PageSize: 2})
	assert.True(t, failed.BalanceQueryDisabled)
	require.Len(t, failed.Keys, 2)
	assert.Equal(t, 2, failed.Keys[0].Index)
	require.NotNil(t, failed.BalanceMonitor)
	assert.True(t, failed.BalanceMonitor.Partial)
	assert.EqualValues(t, 200, failed.BalanceMonitor.CheckedAt)
	assert.Equal(t, common.GetPointer(5.5), failed.BalanceMonitor.Balance, "failed refresh keeps only the last complete channel total")
	require.Len(t, failed.BalanceMonitor.KeyBalances, 4)
	assert.Nil(t, failed.BalanceMonitor.KeyBalances[0].Balance, "previous key balance must not masquerade as a current successful read")
	assert.Equal(t, common.GetPointer(1.5), failed.BalanceMonitor.KeyBalances[0].LastKnownBalance)
	assert.Equal(t, "balance_query_failed", failed.BalanceMonitor.KeyBalances[0].Error)
	assert.Equal(t, initial.Keys[2].Status, failed.Keys[0].Status, "balance refresh cannot revive a disabled key")

	// An old key snapshot may race a replacement and its first completed read.
	// Neither the API nor a full-key caller may match that newer sample by index.
	keys[0], keys[1] = keys[1], keys[0]
	require.NoError(t, model.DB.Model(channel).Updates(map[string]any{"key": strings.Join(keys, "\n"), "setting": "{}"}).Error)
	changed := queryStatus(MultiKeyManageRequest{})
	require.NotNil(t, changed.BalanceMonitor)
	assert.True(t, changed.BalanceMonitor.ConfigurationChanged)
	assert.Empty(t, changed.BalanceMonitor.KeyBalances)
	assert.Nil(t, changed.BalanceMonitor.Balance)
	current, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	require.NoError(t, model.RecordChannelBalanceSample(current, &model.ChannelBalanceSample{StartedAt: 300, CheckedAt: 300, KnownBalance: 9, Success: true,
		KeyBalances: []model.ChannelKeyBalance{{Index: 0, Balance: common.GetPointer(float64(9))}},
	}))
	stale := *channel
	stale.Key = strings.Join([]string{keys[1], keys[0], keys[2], keys[3]}, "\n")
	listItem := model.Channel{Id: channel.Id}
	require.NoError(t, model.PopulateChannelBalanceMonitors([]*model.Channel{&stale, &listItem}))
	require.NotNil(t, stale.BalanceMonitor)
	assert.True(t, stale.BalanceMonitor.ConfigurationChanged)
	assert.Empty(t, stale.BalanceMonitor.KeyBalances)
	assert.Nil(t, stale.BalanceMonitor.Balance)
	require.NotNil(t, listItem.BalanceMonitor)
	assert.False(t, listItem.BalanceMonitor.ConfigurationChanged, "keyless channel lists still use the private current configuration read")
	assert.Equal(t, common.GetPointer(float64(9)), listItem.BalanceMonitor.Balance)
	updated := queryStatus(MultiKeyManageRequest{})
	require.NotNil(t, updated.BalanceMonitor)
	assert.False(t, updated.BalanceMonitor.ConfigurationChanged)
	assert.Equal(t, model.MaskTokenKey(keys[0]), updated.Keys[0].KeyPreview)
	assert.Equal(t, common.GetPointer(float64(9)), updated.BalanceMonitor.KeyBalances[0].Balance)
}

func TestModelSquareHidesModelsWithoutEnabledChannels(t *testing.T) {
	setupChannelManagementTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Model{}, &model.Vendor{}))
	channels := []model.Channel{
		{Name: "catalog-enabled", Type: 1, Status: common.ChannelStatusEnabled, Key: "test-key", Models: "shared,enabled-only", Group: "default"},
		{Name: "catalog-disabled", Type: 1, Status: common.ChannelStatusManuallyDisabled, Key: "test-key", Models: "shared,disabled-only", Group: "disabled-group"},
		{Name: "catalog-auto-disabled", Type: 1, Status: common.ChannelStatusAutoDisabled, Key: "test-key", Models: "auto-disabled-only", Group: "default"},
	}
	for i := range channels {
		require.NoError(t, channels[i].Insert())
		t.Cleanup(func() { require.NoError(t, channels[i].Delete()) })
	}
	// A stale ability must never override the actual channel status.
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id IN ?", []int{channels[1].Id, channels[2].Id}).Update("enabled", true).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channels[2].Id + 1000, Model: "orphan-model", Group: "default", Enabled: true}).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("model = ?", "orphan-model").Delete(&model.Ability{}).Error)
		model.InvalidatePricingCache()
	})
	model.InvalidatePricingCache()
	visible := make(map[string]model.Pricing)
	for _, item := range model.GetPricing() {
		visible[item.ModelName] = item
	}
	assert.Contains(t, visible, "shared")
	assert.ElementsMatch(t, []string{"default"}, visible["shared"].EnableGroup)
	assert.Contains(t, visible, "enabled-only")
	assert.NotContains(t, visible, "disabled-only")
	assert.NotContains(t, visible, "auto-disabled-only")
	assert.NotContains(t, visible, "orphan-model")

	changed, err := model.UpdateChannelStatusWithError(channels[0].Id, "", common.ChannelStatusManuallyDisabled, "manual")
	require.NoError(t, err)
	require.True(t, changed)
	assert.Empty(t, model.GetPricing(), "disabling the final route invalidates the catalog immediately")
	changed, err = model.UpdateChannelStatusWithError(channels[1].Id, "", common.ChannelStatusEnabled, "manual")
	require.NoError(t, err)
	require.True(t, changed)
	visible = make(map[string]model.Pricing)
	for _, item := range model.GetPricing() {
		visible[item.ModelName] = item
	}
	assert.Len(t, visible, 2)
	assert.ElementsMatch(t, []string{"disabled-group"}, visible["shared"].EnableGroup)
	assert.Contains(t, visible, "disabled-only")
}

func TestMultiKeyEnableRestoresOnlyExhaustedChannels(t *testing.T) {
	rootID := setupChannelManagementTest(t)
	database := model.DB
	for _, cacheEnabled := range []bool{false, true} {
		for _, action := range []string{"enable_key", "enable_all_keys"} {
			for _, tc := range []struct {
				name           string
				initialStatus  int
				manualOverride string
				wantStatus     int
			}{
				{name: "key exhaustion restores", initialStatus: common.ChannelStatusEnabled, wantStatus: common.ChannelStatusEnabled},
				{name: "manual disable is preserved", initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "manual disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "status", wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "tag disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "tag", wantStatus: common.ChannelStatusManuallyDisabled},
			} {
				t.Run(fmt.Sprintf("cache=%t/%s/%s", cacheEnabled, action, tc.name), func(t *testing.T) {
					common.MemoryCacheEnabled = cacheEnabled
					tag := t.Name()
					channel := &model.Channel{Name: t.Name(), Type: 1, Key: "key-one\nkey-two", Status: tc.initialStatus, Models: "test-model", Group: "default", Tag: &tag,
						ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}},
					}
					require.NoError(t, channel.Insert())
					t.Cleanup(func() {
						require.NoError(t, channel.Delete())
						model.InitChannelCache()
					})
					for _, operation := range []string{"disable_key", action} {
						if operation == action {
							if tc.manualOverride == "status" {
								model.UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual operation")
							} else if tc.manualOverride == "tag" {
								require.NoError(t, model.DisableChannelByTag(tag))
							}
						}
						payload, err := common.Marshal(MultiKeyManageRequest{ChannelId: channel.Id, Action: operation, KeyIndex: common.GetPointer(0)})
						require.NoError(t, err)
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Set("id", rootID)
						c.Set("role", common.RoleRootUser)
						c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key", bytes.NewReader(payload))
						c.Request.Header.Set("Content-Type", "application/json")
						ManageMultiKeys(c)
						var result struct {
							Success bool `json:"success"`
						}
						require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
						require.True(t, result.Success, recorder.Body.String())
					}
					loaded, err := model.GetChannelById(channel.Id, true)
					require.NoError(t, err)
					assert.Equal(t, tc.wantStatus, loaded.Status)
					assert.NotContains(t, loaded.ChannelInfo.MultiKeyStatusList, 0)
					assert.NotContains(t, loaded.ChannelInfo.MultiKeyDisabledReason, 0)
					assert.NotContains(t, loaded.ChannelInfo.MultiKeyDisabledTime, 0)
					var ability model.Ability
					require.NoError(t, database.Where("channel_id = ?", channel.Id).First(&ability).Error)
					assert.Equal(t, tc.wantStatus == common.ChannelStatusEnabled, ability.Enabled)
				})
			}
		}
	}
}

func TestChannelStatusUpdatesImmediately(t *testing.T) {
	rootID := setupChannelManagementTest(t)
	common.MemoryCacheEnabled = true
	model.InitChannelCache()

	for _, tc := range []struct {
		name         string
		createViaAPI bool
		multiKey     bool
	}{
		{name: "new single-key channel", createViaAPI: true},
		{name: "new multi-key channel", createViaAPI: true, multiKey: true},
		{name: "single-key cache miss"},
		{name: "multi-key cache miss", multiKey: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := model.Channel{Name: t.Name(), Type: 1, Key: "test-channel-key", Status: common.ChannelStatusEnabled, Models: "test-model", Group: "default"}
			mode := "single"
			if tc.multiKey {
				channel.Key += "\nsecond-test-key"
				channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}
				mode = "multi_to_single"
			}
			if tc.createViaAPI {
				payload, err := common.Marshal(AddChannelRequest{Mode: mode, Channel: &channel})
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Set("id", rootID)
				c.Set("role", common.RoleRootUser)
				c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/", bytes.NewReader(payload))
				c.Request.Header.Set("Content-Type", "application/json")
				AddChannel(c)
				var result struct {
					Success bool `json:"success"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
				require.True(t, result.Success, recorder.Body.String())
				require.NoError(t, model.DB.Where("name = ?", channel.Name).First(&channel).Error)
			} else {
				require.NoError(t, channel.Insert())
			}
			t.Cleanup(func() {
				require.NoError(t, channel.Delete())
				model.InitChannelCache()
			})
			if tc.createViaAPI {
				cached, err := model.CacheGetChannel(channel.Id)
				assert.NoError(t, err, "created channel must be routable before the next periodic sync")
				assert.NotNil(t, cached)
			} else {
				_, err := model.CacheGetChannel(channel.Id)
				require.Error(t, err, "exercise a channel missing from the routing cache")
			}

			for _, expectedChange := range []bool{true, false} {
				payload, err := common.Marshal(ChannelStatusRequest{Status: common.ChannelStatusManuallyDisabled})
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Set("id", rootID)
				c.Set("role", common.RoleRootUser)
				c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
				c.Request = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/channel/%d/status", channel.Id), bytes.NewReader(payload))
				c.Request.Header.Set("Content-Type", "application/json")
				UpdateChannelStatus(c)
				var result struct {
					Success bool `json:"success"`
					Changed bool `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
				require.True(t, result.Success, recorder.Body.String())
				assert.Equal(t, expectedChange, result.Changed)
				stored, err := model.GetChannelById(channel.Id, true)
				require.NoError(t, err)
				assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
				var abilities []model.Ability
				require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
				require.NotEmpty(t, abilities)
				for _, ability := range abilities {
					assert.False(t, ability.Enabled)
				}
				cached, err := model.CacheGetChannel(channel.Id)
				if assert.NoError(t, err) {
					assert.Equal(t, common.ChannelStatusManuallyDisabled, cached.Status)
				}
			}
		})
	}

	t.Run("restore disabled key in enabled channel", func(t *testing.T) {
		channel := model.Channel{Name: t.Name(), Type: 1, Key: "key-one\nkey-two", Status: common.ChannelStatusEnabled, Models: "test-model", Group: "default",
			ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}},
		}
		require.NoError(t, channel.Insert())
		model.InitChannelCache()
		t.Cleanup(func() {
			require.NoError(t, channel.Delete())
			model.InitChannelCache()
		})
		changed, err := model.UpdateChannelStatusWithError(channel.Id, "key-two", common.ChannelStatusEnabled, "")
		require.NoError(t, err)
		assert.True(t, changed)
		stored, err := model.GetChannelById(channel.Id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
		assert.NotContains(t, stored.ChannelInfo.MultiKeyStatusList, 1)
		cached, err := model.CacheGetChannel(channel.Id)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, cached.Status)
		assert.NotContains(t, cached.ChannelInfo.MultiKeyStatusList, 1)
	})
}

func TestChannelStatusFailureDoesNotReportSuccess(t *testing.T) {
	rootID := setupChannelManagementTest(t)
	common.MemoryCacheEnabled = true
	for _, batch := range []bool{false, true} {
		for _, failure := range []string{"missing", "channel_write", "ability_write"} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, failure), func(t *testing.T) {
				missing := failure == "missing"
				const callback = "test:reject_channel_status_write"
				callbackRegistered := false
				channel := model.Channel{Name: t.Name(), Type: 1, Key: "test-channel-key", Status: common.ChannelStatusEnabled, Models: "test-model", Group: "default"}
				require.NoError(t, channel.Insert())
				model.InitChannelCache()
				if missing {
					require.NoError(t, channel.Delete())
				} else {
					t.Cleanup(func() { require.NoError(t, channel.Delete()) })
					rejectedTable := "channels"
					if failure == "ability_write" {
						rejectedTable = "abilities"
					}
					require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == rejectedTable {
							tx.AddError(errors.New("channel status write rejected"))
						}
					}))
					callbackRegistered = true
					t.Cleanup(func() {
						if callbackRegistered {
							require.NoError(t, model.DB.Callback().Update().Remove(callback))
						}
					})
				}
				t.Cleanup(model.InitChannelCache)
				var request any = ChannelStatusRequest{Status: common.ChannelStatusManuallyDisabled}
				var successfulChannel *model.Channel
				if batch {
					ids := []int{channel.Id}
					if missing {
						successfulChannel = &model.Channel{Name: t.Name() + "-existing", Type: 1, Key: "test-key", Status: common.ChannelStatusEnabled, Models: "test-model", Group: "default"}
						require.NoError(t, successfulChannel.Insert())
						t.Cleanup(func() { require.NoError(t, successfulChannel.Delete()) })
						ids = append(ids, successfulChannel.Id)
					}
					request = ChannelStatusBatchRequest{Ids: ids, Status: common.ChannelStatusManuallyDisabled}
				}
				payload, err := common.Marshal(request)
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Set("id", rootID)
				c.Set("role", common.RoleRootUser)
				c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
				c.Request = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/channel/%d/status", channel.Id), bytes.NewReader(payload))
				c.Request.Header.Set("Content-Type", "application/json")
				if batch {
					BatchUpdateChannelStatus(c)
				} else {
					UpdateChannelStatus(c)
				}
				var result struct {
					Success bool `json:"success"`
					Data    any  `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
				assert.False(t, result.Success, recorder.Body.String())
				if batch {
					changed := float64(0)
					if successfulChannel != nil {
						changed = 1
						stored, err := model.GetChannelById(successfulChannel.Id, true)
						require.NoError(t, err)
						assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
						cached, err := model.CacheGetChannel(successfulChannel.Id)
						require.NoError(t, err)
						assert.Equal(t, common.ChannelStatusManuallyDisabled, cached.Status)
					}
					assert.Equal(t, changed, result.Data, "partial failures retain the number of changed channels")
				}
				if !missing {
					stored, err := model.GetChannelById(channel.Id, true)
					require.NoError(t, err)
					assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
					cached, err := model.CacheGetChannel(channel.Id)
					require.NoError(t, err)
					assert.Equal(t, common.ChannelStatusEnabled, cached.Status, "failed persistence must not publish a disabled cache state")
					var ability model.Ability
					require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
					assert.True(t, ability.Enabled, "failed persistence must preserve routing abilities")

					require.NoError(t, model.DB.Callback().Update().Remove(callback))
					callbackRegistered = false
					changed, err := model.UpdateChannelStatusWithError(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual retry")
					require.NoError(t, err)
					assert.True(t, changed, "a rolled-back update remains retryable")
					stored, err = model.GetChannelById(channel.Id, true)
					require.NoError(t, err)
					assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
					cached, err = model.CacheGetChannel(channel.Id)
					require.NoError(t, err)
					assert.Equal(t, common.ChannelStatusManuallyDisabled, cached.Status)
					require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
					assert.False(t, ability.Enabled)
				}
			})
		}
	}
}
