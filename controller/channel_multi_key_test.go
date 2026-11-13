package controller

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
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

func TestMultiKeyConfigurationAtomicSave(t *testing.T) {
	rootID := setupChannelManagementTest(t)
	const keyA, keyB, keyC = "config-key-a-secret", "config-key-b-secret", "config-key-c-secret"
	type response struct {
		Success bool `json:"success"`
		Data    struct {
			ID         int                          `json:"id"`
			Key        string                       `json:"key"`
			KeyConfigs []model.ChannelKeyConfigView `json:"key_configs"`
			Revision   string                       `json:"key_config_revision"`
		} `json:"data"`
	}
	role := common.RoleRootUser
	request := func(handler gin.HandlerFunc, method string, id int, body any) response {
		t.Helper()
		encoded, err := common.Marshal(body)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", rootID)
		c.Set("role", role)
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(id)}}
		c.Request = httptest.NewRequest(method, "/api/channel/", bytes.NewReader(encoded))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		var result response
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
		for _, secret := range []string{keyA, keyB, keyC} {
			assert.NotContains(t, recorder.Body.String(), secret)
		}
		return result
	}
	channel := &model.Channel{Name: t.Name(), Type: constant.ChannelTypeOpenAI, Models: "config-model", Group: "default,vip", Status: common.ChannelStatusEnabled}
	for _, other := range []bool{false, true} {
		invalid := *channel
		invalid.Name += fmt.Sprintf(" invalid other=%t", other)
		if other {
			invalid.Type, invalid.OtherSettings = constant.ChannelTypeVertexAi, "{malformed"
		} else {
			invalid.Setting = common.GetPointer("{malformed")
		}
		assert.False(t, request(AddChannel, http.MethodPost, 0, AddChannelRequest{Mode: "multi_to_single", MultiKeyMode: constant.MultiKeyModeFailover, Channel: &invalid, KeyConfigs: []model.ChannelKeyConfig{{Key: keyA}}}).Success)
		var invalidCount int64
		require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", invalid.Name).Count(&invalidCount).Error)
		assert.Zero(t, invalidCount, "invalid settings must not create a partial channel")
	}
	for _, channelType := range []int{constant.ChannelTypeCodex, constant.ChannelTypeVertexAi, constant.ChannelTypeTaskPlugin} {
		structured := &model.Channel{Type: channelType, Key: "{\n  \"private_key\": \"fixture-only\"\n}", Group: "default"}
		original := structured.Key
		assert.Error(t, structured.ApplyKeyConfigs([]model.ChannelKeyConfig{{KeyIdentity: structured.KeyIdentity(original), KeyIndex: common.GetPointer(0)}}, false))
		assert.Equal(t, original, structured.Key, "structured credentials must remain on the legacy editing path")
		assert.False(t, structured.ChannelInfo.IsMultiKey)
	}
	newAPI := &model.Channel{Type: constant.ChannelTypeNewAPI, Group: "default"}
	require.NoError(t, newAPI.ApplyKeyConfigs([]model.ChannelKeyConfig{{Key: "gateway-api-key"}}, true), "New API wrappers still use simple API keys")
	channel.SetSetting(kitdto.ChannelSettings{UpstreamCostRatio: common.GetPointer(1.5)})
	created := request(AddChannel, http.MethodPost, 0, AddChannelRequest{Mode: "multi_to_single", MultiKeyMode: constant.MultiKeyModeFailover, Channel: channel, KeyConfigs: []model.ChannelKeyConfig{
		{Key: keyA, Groups: []string{"default"}, UpstreamCostRatio: common.GetPointer(0.0)},
		{Key: keyB},
	}})
	require.True(t, created.Success)
	require.Positive(t, created.Data.ID)
	id := created.Data.ID
	t.Cleanup(func() { require.NoError(t, (&model.Channel{Id: id}).Delete()) })
	view := request(GetChannel, http.MethodGet, id, nil)
	require.True(t, view.Success)
	require.Len(t, view.Data.KeyConfigs, 2)
	assert.Empty(t, view.Data.Key)
	require.NotEmpty(t, view.Data.Revision)
	assert.Equal(t, (&model.Channel{Id: id}).KeyIdentity(keyA), view.Data.KeyConfigs[0].KeyIdentity, "identity must use the allocated channel ID")
	require.NotNil(t, view.Data.KeyConfigs[0].EffectiveUpstreamCostRatio)
	assert.Zero(t, *view.Data.KeyConfigs[0].EffectiveUpstreamCostRatio)
	require.NotNil(t, view.Data.KeyConfigs[1].EffectiveUpstreamCostRatio)
	assert.Equal(t, 1.5, *view.Data.KeyConfigs[1].EffectiveUpstreamCostRatio)
	items := []model.ChannelKeyConfig{
		{KeyIdentity: view.Data.KeyConfigs[1].KeyIdentity, KeyIndex: common.GetPointer(1)},
		{KeyIdentity: view.Data.KeyConfigs[0].KeyIdentity, KeyIndex: common.GetPointer(0), Groups: []string{"default"}, UpstreamCostRatio: common.GetPointer(0.0), Enabled: common.GetPointer(false)},
	}
	patch := map[string]any{"id": id, "name": "saved ordered config", "multi_key_mode": "failover", "key_configs": items, "key_config_revision": view.Data.Revision}
	role = common.RoleAdminUser
	assert.False(t, request(UpdateChannel, http.MethodPut, id, patch).Success)
	role = common.RoleRootUser
	require.True(t, request(UpdateChannel, http.MethodPut, id, patch).Success)
	stored, err := model.GetChannelById(id, true)
	require.NoError(t, err)
	assert.Equal(t, keyA+"\n"+keyB, stored.Key, "dragging must not reorder physical credentials")
	assert.Equal(t, []string{stored.KeyIdentity(keyB), stored.KeyIdentity(keyA)}, stored.ChannelInfo.KeyOrder)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	for range 2 {
		key, index, apiErr := stored.GetNextEnabledKey()
		require.Nil(t, apiErr)
		assert.Equal(t, keyB, key)
		assert.Equal(t, 1, index)
	}
	cloned := request(CopyChannel, http.MethodPost, id, nil)
	require.True(t, cloned.Success)
	clone, err := model.GetChannelById(cloned.Data.ID, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, clone.Delete()) })
	assert.Equal(t, []string{clone.KeyIdentity(keyB), clone.KeyIdentity(keyA)}, clone.ChannelInfo.KeyOrder)
	assert.NotEqual(t, stored.ChannelInfo.KeyOrder, clone.ChannelInfo.KeyOrder)
	require.True(t, request(UpdateChannel, http.MethodPut, id, map[string]any{"id": id, "multi_key_mode": "random"}).Success)
	require.True(t, request(UpdateChannel, http.MethodPut, id, map[string]any{"id": id, "multi_key_mode": "failover"}).Success)
	assert.False(t, request(UpdateChannel, http.MethodPut, id, map[string]any{"id": id, "multi_key_mode": "unknown"}).Success)
	stored, err = model.GetChannelById(id, true)
	require.NoError(t, err)
	assert.Equal(t, []string{stored.KeyIdentity(keyB), stored.KeyIdentity(keyA)}, stored.ChannelInfo.KeyOrder)

	current := request(GetChannel, http.MethodGet, id, nil)
	require.True(t, current.Success)
	require.NoError(t, model.SetChannelKeyCostRatio(id, 1, stored.KeyIdentity(keyB), common.GetPointer(0.4)))
	patch["key_config_revision"], patch["name"] = current.Data.Revision, "must not persist"
	assert.False(t, request(UpdateChannel, http.MethodPut, id, patch).Success)
	unchanged, err := model.GetChannelById(id, true)
	require.NoError(t, err)
	assert.Equal(t, "saved ordered config", unchanged.Name, "stale key settings must roll back the channel edit")
	require.NotNil(t, unchanged.UpstreamCostRatioForKey(keyB))
	assert.Equal(t, 0.4, *unchanged.UpstreamCostRatioForKey(keyB))

	current = request(GetChannel, http.MethodGet, id, nil)
	for _, invalid := range [][]model.ChannelKeyConfig{{}, {items[0], items[0]}, {{Key: keyB}}, {{Key: keyC, Groups: []string{"forbidden"}}}} {
		patch["key_config_revision"], patch["key_configs"] = current.Data.Revision, invalid
		assert.False(t, request(UpdateChannel, http.MethodPut, id, patch).Success)
	}
	patch["key_configs"] = []model.ChannelKeyConfig{items[0], {Key: keyC, Groups: []string{"vip"}}}
	require.True(t, request(UpdateChannel, http.MethodPut, id, patch).Success)
	stored, err = model.GetChannelById(id, true)
	require.NoError(t, err)
	assert.Equal(t, keyB+"\n"+keyC, stored.Key)
	assert.NotContains(t, stored.ChannelInfo.KeyGroups, stored.KeyIdentity(keyA))
	assert.NotContains(t, stored.ChannelInfo.KeyCostRatios, stored.KeyIdentity(keyA))
	assert.NotContains(t, stored.ChannelInfo.KeyOrder, stored.KeyIdentity(keyA))
	assert.True(t, stored.KeyAllowedForGroup(keyC, "vip"))
	assert.False(t, stored.KeyAllowedForGroup(keyC, "default"))

	var audits []model.AuditLog
	require.NoError(t, model.DB.Where("user_id = ?", rootID).Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	for _, secret := range []string{keyA, keyB, keyC} {
		assert.NotContains(t, string(encoded), secret)
	}
}

func TestMultiKeyConfigurationLegacyDuplicatesAndSingleKey(t *testing.T) {
	setupChannelManagementTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ChannelBalanceSample{}, &model.ChannelKeyObservation{}))
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			channel := &model.Channel{Name: t.Name(), Type: constant.ChannelTypeOpenAI, Key: "legacy-config-key", Models: "config-model", Group: "default,vip", Status: common.ChannelStatusEnabled}
			if duplicate {
				channel.Key += "\nlegacy-config-key\nlegacy-config-other"
				channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModeFailover, MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled}}
			}
			require.NoError(t, channel.Insert())
			t.Cleanup(func() {
				require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Delete(&model.ChannelBalanceSample{}).Error)
				require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Delete(&model.ChannelKeyObservation{}).Error)
				require.NoError(t, channel.Delete())
				model.InvalidateChannelProbeRouting(channel.Id)
			})
			if duplicate {
				require.NoError(t, model.RecordChannelBalanceSample(channel, &model.ChannelBalanceSample{Success: true, KnownBalance: 3, KeyBalances: []model.ChannelKeyBalance{{Index: 0, Balance: common.GetPointer(-1.0)}, {Index: 1, Balance: common.GetPointer(2.0)}, {Index: 2, Balance: common.GetPointer(2.0)}}}))
				require.NoError(t, model.RecordChannelKeyObservation(channel, "config-model", "legacy-config-key", time.Now().UnixMilli(), common.GetPointer(false), 503, "fixture probe failure", "probe"))
			}
			views, revision, err := channel.KeyConfiguration()
			require.NoError(t, err)
			configs := make([]model.ChannelKeyConfig, 0, len(views))
			for _, item := range views {
				configs = append(configs, model.ChannelKeyConfig{KeyIdentity: item.KeyIdentity, KeyIndex: common.GetPointer(item.KeyIndex), Groups: []string{"default"}, UpstreamCostRatio: common.GetPointer(0.0)})
			}
			if duplicate {
				require.Len(t, views, 2)
				assert.Equal(t, 1, views[0].KeyIndex)
				key, index, apiErr := channel.GetNextEnabledKey()
				require.Nil(t, apiErr)
				assert.Equal(t, "legacy-config-key", key)
				assert.Equal(t, 1, index)
			} else {
				require.Len(t, views, 1)
				assert.Nil(t, views[0].EffectiveUpstreamCostRatio)
				configs[0].Enabled = common.GetPointer(false)
			}
			channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeFailover
			require.NoError(t, channel.UpdateWithKeyConfigs(&model.ChannelKeyConfigUpdate{Items: configs, Revision: revision}))
			assert.True(t, channel.ChannelInfo.IsMultiKey)
			assert.True(t, channel.KeyAllowedForGroup("legacy-config-key", "default"))
			assert.False(t, channel.HasKeyForGroup("vip"))
			require.NotNil(t, channel.UpstreamCostRatioForKey("legacy-config-key"))
			assert.Zero(t, *channel.UpstreamCostRatioForKey("legacy-config-key"))
			if duplicate {
				assert.Equal(t, "legacy-config-key\nlegacy-config-other", channel.Key)
				assert.Empty(t, channel.ChannelInfo.MultiKeyStatusList)
				monitor, err := model.GetChannelBalanceMonitor(channel.Id)
				require.NoError(t, err)
				require.NotNil(t, monitor)
				assert.True(t, monitor.ConfigurationChanged)
				assert.NotNil(t, channel.RelayCredentialProbeError("config-model", "legacy-config-key"), "the same credential must retain its real probe failure after duplicate removal")
			} else {
				assert.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
				_, revision, err = channel.KeyConfiguration()
				require.NoError(t, err)
				configs[0].Enabled = common.GetPointer(true)
				require.NoError(t, channel.UpdateWithKeyConfigs(&model.ChannelKeyConfigUpdate{Items: configs, Revision: revision}))
				assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
				_, err = model.UpdateChannelStatusWithError(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual channel disable")
				require.NoError(t, err)
				channel, err = model.GetChannelById(channel.Id, true)
				require.NoError(t, err)
				_, revision, err = channel.KeyConfiguration()
				require.NoError(t, err)
				require.NoError(t, channel.UpdateWithKeyConfigs(&model.ChannelKeyConfigUpdate{Items: configs, Revision: revision}))
				assert.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status, "enabling a key must preserve a separate manual channel disable")
			}
		})
	}
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

func TestMultiKeyGroupPermissions(t *testing.T) {
	require.NoError(t, i18n.Init())
	rootID := setupChannelManagementTest(t)
	previousGroups, previousRatios := setting.UserUsableGroups2JSONString(), ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"public":"Public","private":"Private","blocked":"Blocked"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"public":1,"private":1,"blocked":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousRatios))
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.ChannelBalanceSample{}))
	const keyA, keyB = "sk-group-a-private-key", "sk-group-b-private-key"
	channel := &model.Channel{Name: t.Name(), Type: 1, Key: keyA + "\n" + keyB, Models: "group-scoped-model", Group: "public,private,blocked", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
	}
	require.NoError(t, channel.Insert())
	require.NoError(t, model.RecordChannelBalanceSample(channel, &model.ChannelBalanceSample{
		Success: true, KnownBalance: 2, Balance: common.GetPointer(float64(2)),
		KeyBalances: []model.ChannelKeyBalance{{Index: 0, Balance: common.GetPointer(float64(1))}, {Index: 1, Balance: common.GetPointer(float64(1))}},
	}))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Delete(&model.ChannelBalanceSample{}).Error)
		require.NoError(t, channel.Delete())
		model.InitChannelCache()
	})
	manageRole := common.RoleRootUser
	manage := func(request MultiKeyManageRequest, wantSuccess bool) MultiKeyStatusResponse {
		t.Helper()
		request.ChannelId = channel.Id
		payload, err := common.Marshal(request)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", rootID)
		c.Set("role", manageRole)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		ManageMultiKeys(c)
		var result struct {
			Success bool                   `json:"success"`
			Data    MultiKeyStatusResponse `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
		require.Equal(t, wantSuccess, result.Success, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), keyA)
		assert.NotContains(t, recorder.Body.String(), keyB)
		return result.Data
	}
	setGroups := func(index int, identity string, groups []string, success bool) {
		t.Helper()
		manage(MultiKeyManageRequest{Action: "set_key_groups", KeyIndex: &index, KeyIdentity: identity, Groups: &groups}, success)
	}
	status := manage(MultiKeyManageRequest{Action: "get_key_status"}, true)
	require.Len(t, status.Keys, 2)
	assert.ElementsMatch(t, []string{"public", "private", "blocked"}, status.ChannelGroups)
	assert.Empty(t, status.Keys[0].Groups, "an unconfigured key inherits all channel groups")
	identityA, identityB := status.Keys[0].KeyIdentity, status.Keys[1].KeyIdentity
	require.NotEmpty(t, identityA)
	assert.NotEqual(t, identityA, identityB)
	for _, groups := range [][]string{{"outside", "private"}, {"outside"}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "auto")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "public")
		common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, groups)
		service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
		selected, selectedGroup, selectErr := service.SelectChannelForRequest(c, "unlisted-pinned-model", &service.RetryParam{Ctx: c, TokenGroup: "auto"})
		if len(groups) == 1 {
			require.NotNil(t, selectErr, "an auto pin cannot invent permission to the channel's first group")
			continue
		}
		require.Nil(t, selectErr)
		assert.Equal(t, "private", selectedGroup)
		require.Nil(t, middleware.SetupContextForSelectedChannel(c, selected, "unlisted-pinned-model"), "an unscoped auto pin keeps its existing model behavior")
		assert.Equal(t, "auto", common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
	}
	require.NoError(t, model.DB.Model(channel).Update("group", "private,blocked").Error)
	_, _, removedGroupErr := channel.GetNextRelayKeyForModel("group-scoped-model", false, "public")
	require.NotNil(t, removedGroupErr, "stale cache cannot retain a deleted group when all keys inherit")
	require.NoError(t, model.DB.Model(channel).Update("group", "public,private,blocked").Error)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	require.NoError(t, channel.SaveChannelInfo())
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	first, _, firstErr := channel.GetNextRelayKeyForModel("group-scoped-model", false, "public")
	require.Nil(t, firstErr)
	second, _, secondErr := channel.GetNextRelayKeyForModel("group-scoped-model", false, "public")
	require.Nil(t, secondErr)
	assert.Equal(t, keyA, first)
	assert.Equal(t, keyB, second, "the authoritative permission snapshot must preserve round-robin progress")
	common.MemoryCacheEnabled = false
	_, err := model.UpdateChannelStatusWithError(channel.Id, keyB, common.ChannelStatusAutoDisabled, "fixture invalid credential")
	require.NoError(t, err)
	_, _, pollingErr := channel.GetNextEnabledKey()
	require.Nil(t, pollingErr)
	afterPolling, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, afterPolling.ChannelInfo.MultiKeyStatusList[1], "a stale polling snapshot must preserve newly quarantined keys")
	_, err = model.UpdateChannelStatusWithError(channel.Id, keyB, common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	setGroups(0, identityA, []string{"public"}, true)
	setGroups(1, identityB, []string{"private"}, true)
	manageRole = common.RoleCommonUser
	setGroups(1, identityB, []string{"blocked"}, false)
	manageRole = common.RoleRootUser
	setGroups(0, identityA, []string{"outside"}, false)
	setGroups(0, identityA, []string{""}, false)
	manage(MultiKeyManageRequest{Action: "set_key_groups", KeyIndex: common.GetPointer(0), KeyIdentity: identityA}, false)
	batchSnapshot := *channel
	batchSettings := batchSnapshot.GetSetting()
	batchSettings.BatchKey = keyB
	batchSnapshot.SetSetting(batchSettings)
	require.NoError(t, model.DB.Model(channel).Update("setting", batchSnapshot.Setting).Error)
	_, _, batchErr := batchSnapshot.GetNextRelayKeyForModel("group-scoped-model", true, "public")
	require.NotNil(t, batchErr, "batch override cannot use another group's restricted key")
	_, _, batchErr = batchSnapshot.GetNextRelayKeyForModel("group-scoped-model", true, "private")
	require.Nil(t, batchErr)
	require.NoError(t, model.DB.Model(channel).Update("setting", channel.Setting).Error)
	autoContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	autoContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(autoContext, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(autoContext, constant.ContextKeyUserGroup, "public")
	common.SetContextKey(autoContext, constant.ContextKeyTokenAutoGroups, []string{"private"})
	autoChannel, autoGroup, selectErr := service.SelectChannelForRequest(autoContext, "group-scoped-model", &service.RetryParam{Ctx: autoContext, ModelName: "group-scoped-model", TokenGroup: "auto"})
	require.Nil(t, selectErr)
	assert.Equal(t, "private", autoGroup)
	require.Nil(t, middleware.SetupContextForSelectedChannel(autoContext, autoChannel, "group-scoped-model"))
	assert.Equal(t, keyB, common.GetContextKeyString(autoContext, constant.ContextKeyChannelKey))
	assert.Equal(t, "auto", common.GetContextKeyString(autoContext, constant.ContextKeyUsingGroup))
	for _, test := range []struct{ tokenGroup, selectedGroup, key string }{
		{"public", "private", keyA},
		{"auto", "public", keyA},
		{"auto", "private", keyB},
		{"auto", "blocked", ""},
		{"auto", "", ""},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, test.tokenGroup)
		common.SetContextKey(c, constant.ContextKeyAutoGroup, test.selectedGroup)
		apiErr := middleware.SetupContextForSelectedChannel(c, channel, "group-scoped-model")
		if test.key == "" {
			require.NotNil(t, apiErr)
			assert.Equal(t, model.ErrorCodeChannelGroupUnavailable, apiErr.GetErrorCode())
		} else {
			require.Nil(t, apiErr)
			assert.Equal(t, test.key, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
		}
		assert.Equal(t, test.tokenGroup, common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
	}
	staleSingle := *channel
	staleSingle.Key, staleSingle.Keys, staleSingle.ChannelInfo = keyB, nil, model.ChannelInfo{}
	key, _, staleModeErr := staleSingle.GetNextRelayKeyForModel("group-scoped-model", false, "public")
	require.Nil(t, staleModeErr)
	assert.Equal(t, keyA, key, "an old single-key cache must honor newly configured multi-key permissions")
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.ChannelKeyObservation{}))
	originTask := &model.Task{TaskID: "key-groups-origin", UserId: rootID, ChannelId: channel.Id}
	require.NoError(t, model.DB.Create(originTask).Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Delete(originTask).Error) })
	originChannel, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	originSettings := originChannel.GetSetting()
	originSettings.GroupModels = map[string][]string{"public": {}, "private": {"group-scoped-model"}}
	originChannel.SetSetting(originSettings)
	require.NoError(t, model.DB.Model(originChannel).Update("setting", originChannel.Setting).Error)
	require.NoError(t, originChannel.UpdateAbilities(model.DB))
	for _, groups := range [][]string{{"private"}, {"public", "private"}, {"blocked"}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/continue", nil)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "auto")
		common.SetContextKey(c, constant.ContextKeyAutoGroup, "public")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "public")
		common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, groups)
		info := &relaycommon.RelayInfo{UserId: rootID, OriginModelName: "group-scoped-model", ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: originTask.TaskID}}
		taskErr := relay.ResolveOriginTask(c, info)
		if groups[0] == "blocked" {
			require.NotNil(t, taskErr, "the previous channel's auto group must not authorize an unavailable origin group")
		} else {
			require.Nil(t, taskErr)
			assert.Equal(t, keyB, info.ApiKey)
			assert.Equal(t, "private", common.GetContextKeyString(c, constant.ContextKeyAutoGroup))
		}
		assert.Equal(t, "auto", common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
	}
	t.Run("single_origin_tier_resolves_actual_auto_group", func(t *testing.T) {
		previousCache := common.MemoryCacheEnabled
		t.Cleanup(func() { common.MemoryCacheEnabled = previousCache; model.InitChannelCache() })
		single := &model.Channel{Name: "single origin tier", Type: constant.ChannelTypeOpenAI, Key: "single-origin-private-key", Models: "group-scoped-model", Group: "private", Status: common.ChannelStatusEnabled}
		require.NoError(t, single.Insert())
		t.Cleanup(func() { require.NoError(t, single.Delete()) })
		order, err := model.GetGroupKeyRouting("private")
		require.NoError(t, err)
		_, err = model.SetGroupKeyRouting(model.GroupKeyRoutingUpdate{Group: "private", Revision: order.Revision, Items: []model.GroupKeyRoutingChange{{ChannelID: single.Id, KeyIdentity: single.KeyIdentity(single.Key), Priority: common.GetPointer(int64(20))}}})
		require.NoError(t, err)
		task := &model.Task{TaskID: "single-key-tier-origin", UserId: rootID, ChannelId: single.Id}
		require.NoError(t, model.DB.Create(task).Error)
		t.Cleanup(func() { require.NoError(t, model.DB.Delete(task).Error) })
		for _, cached := range []bool{false, true} {
			common.MemoryCacheEnabled = cached
			model.InitChannelCache()
			for _, groups := range [][]string{{"private"}, {"public", "private"}, {"blocked"}} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/continue", nil)
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "auto")
				common.SetContextKey(c, constant.ContextKeyAutoGroup, "public")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "public")
				common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, groups)
				info := &relaycommon.RelayInfo{UserId: rootID, OriginModelName: "group-scoped-model", ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: task.TaskID}}
				taskErr := relay.ResolveOriginTask(c, info)
				if groups[0] == "blocked" {
					require.NotNil(t, taskErr)
					assert.Empty(t, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
				} else {
					require.Nil(t, taskErr)
					assert.Equal(t, single.Key, info.ApiKey)
					assert.Equal(t, "private", common.GetContextKeyString(c, constant.ContextKeyAutoGroup))
					assert.True(t, service.HasGroupKeyRouting(c))
				}
				assert.Equal(t, "auto", common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
			}
		}
	})
	require.NoError(t, model.RecordChannelKeyObservation(originChannel, "group-scoped-model", keyB, 1, common.GetPointer(false), 502, "origin probe unavailable", "probe"))
	originContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	originContext.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/continue", nil)
	common.SetContextKey(originContext, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(originContext, constant.ContextKeyUserGroup, "public")
	common.SetContextKey(originContext, constant.ContextKeyTokenAutoGroups, []string{"private"})
	originInfo := &relaycommon.RelayInfo{UserId: rootID, OriginModelName: "group-scoped-model", ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{OriginTaskID: originTask.TaskID}}
	originError := relay.ResolveOriginTask(originContext, originInfo)
	require.NotNil(t, originError)
	assert.Equal(t, http.StatusServiceUnavailable, originError.StatusCode)
	assert.Equal(t, string(model.ErrorCodeChannelProbeUnavailable), originError.Code)
	assert.Contains(t, originError.Message, "origin probe unavailable")
	require.NoError(t, model.RecordChannelKeyObservation(originChannel, "group-scoped-model", keyB, 2, common.GetPointer(true), 200, "", "probe"))
	require.NoError(t, model.DB.Model(channel).Update("setting", channel.Setting).Error)
	currentChannel, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	require.NoError(t, currentChannel.UpdateAbilities(model.DB))
	for _, test := range []struct{ language, noGroupKey, disabled string }{
		{"en", "No channel key is available for this group", "This channel has been disabled"},
		{"zh-CN", "当前分组没有可用的渠道密钥", "该渠道已被禁用"},
		{"zh-TW", "目前分組沒有可用的渠道金鑰", "該管道已被禁用"},
	} {
		t.Run(test.language, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Request.Header.Set("Accept-Language", test.language)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "blocked")
			apiErr := middleware.SetupContextForSelectedChannel(c, channel, "group-scoped-model")
			require.NotNil(t, apiErr)
			assert.EqualError(t, apiErr, test.noGroupKey)
			assert.Equal(t, model.ErrorCodeChannelGroupUnavailable, apiErr.GetErrorCode())
			assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
			require.NoError(t, model.DB.Model(channel).Update("status", common.ChannelStatusManuallyDisabled).Error)
			t.Cleanup(func() {
				require.NoError(t, model.DB.Model(channel).Update("status", common.ChannelStatusEnabled).Error)
			})
			apiErr = middleware.SetupContextForSelectedChannel(c, channel, "group-scoped-model")
			require.NotNil(t, apiErr)
			assert.EqualError(t, apiErr, test.disabled)
			assert.Equal(t, "get_channel_failed", string(apiErr.GetErrorCode()))
			missing := *channel
			missing.Id = -1
			apiErr = middleware.SetupContextForSelectedChannel(c, &missing, "group-scoped-model")
			require.NotNil(t, apiErr)
			assert.ErrorIs(t, apiErr, gorm.ErrRecordNotFound, "database errors must not be mislabeled as a disabled channel")
		})
	}

	// Keep the original pre-edit snapshot to exercise a stale instance cache.
	for _, mode := range []constant.MultiKeyMode{constant.MultiKeyModeRandom, constant.MultiKeyModePolling} {
		current, err := model.GetChannelById(channel.Id, true)
		require.NoError(t, err)
		current.ChannelInfo.MultiKeyMode = mode
		require.NoError(t, current.SaveChannelInfo())
		for group, expected := range map[string]string{"public": keyA, "private": keyB} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "group-scoped-model"))
			assert.Equal(t, expected, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
		}
		_, _, apiErr := channel.GetNextRelayKeyForModel("group-scoped-model", false, "blocked")
		require.NotNil(t, apiErr)
		assert.Equal(t, "channel_no_group_key", string(apiErr.GetErrorCode()))
	}
	loaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Empty(t, loaded.GetModelsForGroup("blocked"))
	for _, cache := range []bool{false, true} {
		common.MemoryCacheEnabled = cache
		model.InitChannelCache()
		selected, err := model.GetRandomSatisfiedChannel("blocked", "group-scoped-model", 0, nil)
		require.NoError(t, err)
		assert.Nil(t, selected)
		assert.False(t, model.IsChannelEnabledForGroupModel("blocked", "group-scoped-model", channel.Id))
	}
	pricing := model.GetPricing()
	require.Len(t, pricing, 1)
	assert.ElementsMatch(t, []string{"public", "private"}, pricing[0].EnableGroup)
	common.MemoryCacheEnabled = false

	// A disabled authorized key cannot fall through to another group's key.
	loaded.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusManuallyDisabled}
	require.NoError(t, loaded.SaveChannelInfo())
	_, _, apiErr := channel.GetNextRelayKeyForModel("group-scoped-model", false, "public")
	require.NotNil(t, apiErr)
	assert.ElementsMatch(t, []string{"group-scoped-model"}, loaded.GetModelsForGroup("public"), "temporary health does not erase catalog permissions")
	loaded.ChannelInfo.MultiKeyStatusList = nil
	require.NoError(t, loaded.SaveChannelInfo())
	setGroups(0, identityA, []string{}, true)
	key, _, apiErr = channel.GetNextRelayKeyForModel("group-scoped-model", false, "blocked")
	require.Nil(t, apiErr)
	assert.Equal(t, keyA, key, "explicit clearing restores inheritance")
	setGroups(0, identityA, []string{"public"}, true)

	// Group deletion cannot convert a saved explicit restriction to inheritance.
	loaded, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	loaded.Group = "private,blocked"
	require.Error(t, loaded.Update())
	require.NoError(t, model.DB.Model(loaded).Update("group", "private,blocked").Error)
	_, _, apiErr = channel.GetNextRelayKeyForModel("group-scoped-model", false, "blocked")
	require.NotNil(t, apiErr, "legacy inconsistent scopes fail closed")
	require.NoError(t, model.DB.Model(loaded).Update("group", "public,private,blocked").Error)

	// Reordering follows the physical credential, and stale index writes fail.
	loaded, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	loaded.Key = keyB + "\n" + keyA
	require.NoError(t, loaded.Update())
	status = manage(MultiKeyManageRequest{Action: "get_key_status"}, true)
	assert.Equal(t, identityB, status.Keys[0].KeyIdentity)
	assert.Equal(t, []string{"private"}, status.Keys[0].Groups)
	assert.Equal(t, identityA, status.Keys[1].KeyIdentity)
	assert.Equal(t, []string{"public"}, status.Keys[1].Groups)
	setGroups(0, identityA, []string{"blocked"}, false)

	// Copying must rebind identities without widening the copied permissions.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("id", rootID)
	c.Set("role", common.RoleRootUser)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/copy", nil)
	CopyChannel(c)
	var copied struct {
		Success bool `json:"success"`
		Data    struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &copied))
	require.True(t, copied.Success, recorder.Body.String())
	clone, err := model.GetChannelById(copied.Data.ID, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, clone.Delete()) })
	assert.NotEqual(t, identityA, clone.KeyIdentity(keyA))
	assert.Equal(t, []string{"public"}, clone.ChannelInfo.KeyGroups[clone.KeyIdentity(keyA)])
	assert.Empty(t, clone.GetModelsForGroup("blocked"))

	manage(MultiKeyManageRequest{Action: "delete_key", KeyIndex: common.GetPointer(0)}, true)
	loaded, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.NotContains(t, loaded.ChannelInfo.KeyGroups, identityB)
	assert.Equal(t, []string{"public"}, loaded.ChannelInfo.KeyGroups[identityA])
	assert.Empty(t, loaded.GetModelsForGroup("private"))
}

func TestMultiKeyCostRatios(t *testing.T) {
	rootID := setupChannelManagementTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.ChannelBalanceSample{}, &model.ChannelKeyObservation{}))
	const keyA, keyB = "cost-fixture-key-a", "cost-fixture-key-b"
	channel := &model.Channel{Name: t.Name(), Type: constant.ChannelTypeOpenAI, Key: keyA + "\n" + keyB, Models: "cost-model", Group: "default", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
	channel.SetSetting(kitdto.ChannelSettings{UpstreamCostRatio: common.GetPointer(1.5)})
	require.NoError(t, channel.Insert())
	t.Cleanup(func() {
		require.NoError(t, channel.Delete())
		model.InitChannelCache()
	})
	identityA, identityB := channel.KeyIdentity(keyA), channel.KeyIdentity(keyB)
	manageRole := common.RoleRootUser
	var accessTokenScopes []string
	manage := func(body map[string]any, wantSuccess bool) MultiKeyStatusResponse {
		t.Helper()
		body["channel_id"] = channel.Id
		payload, err := common.Marshal(body)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", rootID)
		c.Set("role", manageRole)
		if accessTokenScopes != nil {
			c.Set("access_token_id", 1)
			c.Set("access_token_scopes", accessTokenScopes)
		}
		c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/multi_key/manage", bytes.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		ManageMultiKeys(c)
		var response struct {
			Success bool                   `json:"success"`
			Data    MultiKeyStatusResponse `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.Equal(t, wantSuccess, response.Success, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), keyA)
		assert.NotContains(t, recorder.Body.String(), keyB)
		if body["action"] == "get_key_status" && wantSuccess {
			assert.Contains(t, recorder.Body.String(), `"channel_upstream_cost_ratio":`)
			assert.Contains(t, recorder.Body.String(), `"upstream_cost_ratio":`)
		}
		return response.Data
	}
	status := manage(map[string]any{"action": "get_key_status"}, true)
	require.Len(t, status.Keys, 2)
	assert.Nil(t, status.Keys[0].UpstreamCostRatio)
	require.NotNil(t, status.ChannelUpstreamCostRatio)
	assert.Equal(t, 1.5, *status.ChannelUpstreamCostRatio)
	manageRole = common.RoleAdminUser
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0}, false)
	manageRole = common.RoleRootUser
	accessTokenScopes = []string{"channel:operate"}
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0}, false)
	accessTokenScopes = []string{"channel:operate", "channel:sensitive_write"}
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0}, true)
	accessTokenScopes = nil
	for _, fields := range []map[string]any{
		{}, {"upstream_cost_ratio": nil}, {"clear_cost_ratio": false},
		{"upstream_cost_ratio": 1, "clear_cost_ratio": true},
		{"upstream_cost_ratio": -1}, {"upstream_cost_ratio": 10001}, {"upstream_cost_ratio": "0"},
	} {
		fields["action"], fields["key_index"], fields["key_identity"] = "set_key_cost_ratio", 0, identityA
		manage(fields, false)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		require.Error(t, model.SetChannelKeyCostRatio(channel.Id, 0, identityA, &value))
	}
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0}, true)
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 1, "key_identity": identityB, "upstream_cost_ratio": 2}, true)
	// Stale status and ordinary edits must preserve another node's cost writes.
	require.NoError(t, channel.SaveChannelInfo())
	require.NoError(t, channel.Update())
	_, err := model.UpdateChannelStatusWithError(channel.Id, keyB, common.ChannelStatusAutoDisabled, "fixture unavailable")
	require.NoError(t, err)
	require.NoError(t, model.SetChannelKeyGroups(channel.Id, 0, identityA, []string{"default"}))
	stored, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{identityA: 0, identityB: 2}, stored.ChannelInfo.KeyCostRatios)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[1])
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0.25}, true)
	stored, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, stored.ChannelInfo.KeyGroups[identityA])
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[1])
	// Request selection uses the current physical key's cost, not stale cache metadata.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "cost-model"))
	frozen, exists := common.GetContextKeyType[*float64](c, constant.ContextKeyChannelUpstreamCostRatio)
	require.True(t, exists)
	require.NotNil(t, frozen)
	assert.Equal(t, 0.25, *frozen)
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0}, true)
	assert.Equal(t, 0.25, *frozen, "an in-flight request retains its selected cost")
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "cost-model"))
	frozen, _ = common.GetContextKeyType[*float64](c, constant.ContextKeyChannelUpstreamCostRatio)
	require.NotNil(t, frozen)
	assert.Zero(t, *frozen, "retrying the same channel captures the replacement attempt's cost")
	_, err = model.UpdateChannelStatusWithError(channel.Id, keyB, common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	_, err = model.UpdateChannelStatusWithError(channel.Id, keyA, common.ChannelStatusAutoDisabled, "fixture unavailable")
	require.NoError(t, err)
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "cost-model"))
	assert.Equal(t, keyB, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
	frozen, _ = common.GetContextKeyType[*float64](c, constant.ContextKeyChannelUpstreamCostRatio)
	require.NotNil(t, frozen)
	assert.Equal(t, 2.0, *frozen, "a replacement credential must not inherit the failed key's free estimate")
	_, err = model.UpdateChannelStatusWithError(channel.Id, keyA, common.ChannelStatusEnabled, "")
	require.NoError(t, err)
	_, err = model.UpdateChannelStatusWithError(channel.Id, keyB, common.ChannelStatusAutoDisabled, "fixture unavailable")
	require.NoError(t, err)
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "clear_cost_ratio": true}, true)
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "cost-model"))
	frozen, _ = common.GetContextKeyType[*float64](c, constant.ContextKeyChannelUpstreamCostRatio)
	require.NotNil(t, frozen)
	assert.Equal(t, 1.5, *frozen)
	require.NoError(t, model.DB.Model(channel).Update("setting", "{}").Error)
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "cost-model"))
	frozen, exists = common.GetContextKeyType[*float64](c, constant.ContextKeyChannelUpstreamCostRatio)
	require.True(t, exists)
	assert.Nil(t, frozen, "a stale channel setting must not resurrect cleared costs")
	require.NoError(t, model.DB.Model(channel).Update("setting", channel.Setting).Error)
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 0}, true)
	// Destination and credential changes must be taken from the same saved batch settings.
	staleBatch := *channel
	staleBatch.SetSetting(kitdto.ChannelSettings{BatchEnabled: true, BatchKey: "cost-old-batch-key", BatchBaseURL: "https://old-batch.invalid"})
	updatedBatch := *channel
	updatedBatch.SetSetting(kitdto.ChannelSettings{BatchEnabled: true, BatchKey: keyA, BatchBaseURL: "https://new-batch.invalid", UpstreamCostRatio: common.GetPointer(1.5)})
	require.NoError(t, model.DB.Model(channel).Update("setting", updatedBatch.Setting).Error)
	batchContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	batchContext.Request = httptest.NewRequest(http.MethodPost, "/v1/batches", nil)
	common.SetContextKey(batchContext, constant.ContextKeyUsingGroup, "default")
	service.GetChannelConstraints(batchContext).AddFilter(dto.ChannelFilter{Kind: dto.FilterBatchCapable})
	require.Nil(t, middleware.SetupContextForSelectedChannel(batchContext, &staleBatch, "cost-model"))
	assert.Equal(t, keyA, common.GetContextKeyString(batchContext, constant.ContextKeyChannelKey))
	assert.Equal(t, "https://new-batch.invalid", common.GetContextKeyString(batchContext, constant.ContextKeyChannelBaseUrl))
	batchCost, _ := common.GetContextKeyType[*float64](batchContext, constant.ContextKeyChannelUpstreamCostRatio)
	require.NotNil(t, batchCost)
	assert.Zero(t, *batchCost)
	require.NoError(t, model.DB.Model(channel).Update("setting", channel.Setting).Error)
	stored, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	stored.Key = keyB + "\n" + keyA
	require.NoError(t, stored.Update())
	status = manage(map[string]any{"action": "get_key_status"}, true)
	assert.Equal(t, identityB, status.Keys[0].KeyIdentity)
	require.NotNil(t, status.Keys[0].UpstreamCostRatio)
	assert.Equal(t, 2.0, *status.Keys[0].UpstreamCostRatio)
	require.NotNil(t, status.Keys[1].UpstreamCostRatio)
	assert.Zero(t, *status.Keys[1].UpstreamCostRatio)
	manage(map[string]any{"action": "set_key_cost_ratio", "key_index": 0, "key_identity": identityA, "upstream_cost_ratio": 3}, false)
	// Copies use the new channel's identities and preserve an explicit free key.
	recorder := httptest.NewRecorder()
	copyContext, _ := gin.CreateTestContext(recorder)
	copyContext.Set("id", rootID)
	copyContext.Set("role", common.RoleRootUser)
	copyContext.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
	copyContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel/copy", nil)
	CopyChannel(copyContext)
	var copied struct {
		Success bool `json:"success"`
		Data    struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &copied))
	require.True(t, copied.Success, recorder.Body.String())
	clone, err := model.GetChannelById(copied.Data.ID, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, clone.Delete()) })
	assert.Equal(t, map[string]float64{clone.KeyIdentity(keyA): 0, clone.KeyIdentity(keyB): 2}, clone.ChannelInfo.KeyCostRatios)
	manage(map[string]any{"action": "delete_key", "key_index": 0}, true)
	stored, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{identityA: 0}, stored.ChannelInfo.KeyCostRatios)
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
				autoExhaust    bool
				manualOverride string
				wantStatus     int
			}{
				{name: "key exhaustion restores", initialStatus: common.ChannelStatusEnabled, wantStatus: common.ChannelStatusEnabled},
				{name: "manual disable is preserved", initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "manual disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "status", wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "tag disable after exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, manualOverride: "tag", wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "auto key exhaustion restores", initialStatus: common.ChannelStatusEnabled, autoExhaust: true, wantStatus: common.ChannelStatusEnabled},
				{name: "auto disable for another reason is preserved", initialStatus: common.ChannelStatusAutoDisabled, wantStatus: common.ChannelStatusAutoDisabled},
				{name: "manual disable after auto exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, autoExhaust: true, manualOverride: "status", wantStatus: common.ChannelStatusManuallyDisabled},
				{name: "tag disable after auto exhaustion is preserved", initialStatus: common.ChannelStatusEnabled, autoExhaust: true, manualOverride: "tag", wantStatus: common.ChannelStatusManuallyDisabled},
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
					operations := []string{"disable_key", action}
					if tc.autoExhaust {
						// Key two is already disabled, so a request failure on key
						// one disables the whole channel automatically.
						model.InitChannelCache()
						require.True(t, model.UpdateChannelStatus(channel.Id, "key-one", common.ChannelStatusAutoDisabled, "provider rejected key"))
						exhausted, err := model.GetChannelById(channel.Id, true)
						require.NoError(t, err)
						require.Equal(t, common.ChannelStatusAutoDisabled, exhausted.Status)
						operations = []string{action}
					}
					for _, operation := range operations {
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
					if tc.wantStatus == common.ChannelStatusEnabled {
						assert.Equal(t, "", loaded.GetOtherInfo()["status_reason"])
					}
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
