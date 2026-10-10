package controller

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
