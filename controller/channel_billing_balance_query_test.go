package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNegativeBalancesAreExcludedFromRelay(t *testing.T) {
	t.Run("task_routes_without_initial_channel", func(t *testing.T) {
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/mj/notify"},
			{http.MethodGet, "/mj/task/example/fetch"},
			{http.MethodGet, "/v1/videos/example"},
			{http.MethodPost, "/v1/videos/example/remix"},
		} {
			engine := gin.New()
			engine.Handle(route.method, route.path, middleware.Distribute(), func(c *gin.Context) {
				assert.False(t, common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime).IsZero())
				c.Status(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
			assert.Equal(t, http.StatusNoContent, recorder.Code, "%s %s: %s", route.method, route.path, recorder.Body.String())
		}
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ChannelBalanceSample{}))
			var debtCents, queries atomic.Int64
			var rejectDebt atomic.Bool
			debtCents.Store(-68088)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries.Add(1)
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if key == "unknown" || rejectDebt.Load() && strings.HasPrefix(key, "debt") {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				balance := float64(25)
				if strings.HasPrefix(key, "debt") {
					balance = float64(debtCents.Load()) / 100
				} else if key == "zero" {
					balance = 0
				}
				if strings.HasSuffix(r.URL.Path, "/subscription") {
					fmt.Fprintf(w, `{"hard_limit_usd":%v}`, balance)
				} else {
					fmt.Fprint(w, `{"total_usage":0}`)
				}
			}))
			t.Cleanup(server.Close)
			channels := make([]*model.Channel, 0, 4)
			for i, keys := range []string{"debt-a", "debt-a\nfunds\ndebt-b", "zero", "unknown"} {
				priority := int64(100 - 10*i)
				channel := &model.Channel{Name: fmt.Sprintf("balance-route-%d", i), Type: constant.ChannelTypeOpenAI, Key: keys, BaseURL: &server.URL, Models: "balance-model", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority}
				if i == 1 {
					channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling}
				}
				require.NoError(t, db.Create(channel).Error)
				require.NoError(t, channel.UpdateAbilities(db))
				_, err := updateChannelBalance(channel)
				if i == 3 {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				channels = append(channels, channel)
			}
			single, multi := channels[0], channels[1]
			assert.False(t, single.HasRelayBalance())
			assert.True(t, multi.HasRelayBalance())
			assert.True(t, channels[2].HasRelayBalance(), "zero does not establish a negative balance")
			assert.True(t, channels[3].HasRelayBalance(), "a failed query does not establish a negative balance")
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("memory_cache_%t", cached), func(t *testing.T) {
					common.MemoryCacheEnabled = cached
					model.InitChannelCache()
					selected, err := model.GetRandomSatisfiedChannel("default", "balance-model", 0, nil)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, multi.Id, selected.Id, "skip the higher-priority exhausted channel")
					for range 3 {
						key, index, apiErr := selected.GetNextRelayKey()
						require.Nil(t, apiErr)
						assert.Equal(t, "funds", key)
						assert.Equal(t, 1, index)
					}
				})
			}
			common.MemoryCacheEnabled = false
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			apiErr := middleware.SetupContextForSelectedChannel(context, single, "balance-model")
			require.NotNil(t, apiErr, "explicitly selected channels must enforce the same balance rule")
			assert.NotContains(t, context.GetHeader("Authorization"), "debt")
			key, _, apiErr := single.GetNextEnabledKey()
			require.Nil(t, apiErr, "administrative balance reads must remain available for recovery")
			assert.Equal(t, "debt-a", key)

			for _, test := range []struct {
				name    string
				channel *model.Channel
				setting dto.ChannelSettings
				blocked bool
				key     string
			}{
				{name: "batch_reuses_main_account", channel: single, blocked: true},
				{name: "batch_independent_key", channel: single, setting: dto.ChannelSettings{BatchKey: "batch-funds"}, key: "batch-funds"},
				{name: "batch_same_debt_key", channel: single, setting: dto.ChannelSettings{BatchKey: "debt-a"}, blocked: true},
				{name: "batch_independent_account_host", channel: single, setting: dto.ChannelSettings{BatchBaseURL: "https://batch-account.invalid", BatchKey: "batch-funds"}, key: "batch-funds"},
				{name: "batch_overrides_to_negative_key", channel: multi, setting: dto.ChannelSettings{BatchKey: "debt-a"}, blocked: true},
				{name: "batch_overrides_to_funded_key", channel: multi, setting: dto.ChannelSettings{BatchKey: "funds"}, key: "funds"},
			} {
				t.Run(test.name, func(t *testing.T) {
					batchChannel := *test.channel
					test.setting.BatchEnabled = true
					batchChannel.SetSetting(test.setting)
					filters := []taskdto.ChannelFilter{{Kind: taskdto.FilterBatchCapable}}
					allowed, _ := model.ChannelSatisfiesFilters(&batchChannel, "balance-model", filters)
					assert.Equal(t, !test.blocked, allowed, "candidate filtering must evaluate the credential actually sent by batch")
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/batches", nil)
					service.GetChannelConstraints(ctx).AddFilter(filters[0])
					setupErr := middleware.SetupContextForSelectedChannel(ctx, &batchChannel, "balance-model")
					if test.blocked {
						require.NotNil(t, setupErr)
						assert.Equal(t, model.ErrorCodeChannelBalanceUnavailable, setupErr.GetErrorCode())
						assert.False(t, service.ShouldDisableChannel(setupErr), "balance filtering must not trigger automatic disablement")
					} else {
						require.Nil(t, setupErr)
						assert.Equal(t, test.key, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
					}
				})
			}

			// A manually disabled funded key is never used to rescue an exhausted channel.
			multi.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
			require.NoError(t, multi.SaveChannelInfo())
			assert.False(t, multi.HasRelayBalance())
			for _, cached := range []bool{false, true} {
				common.MemoryCacheEnabled = cached
				model.InitChannelCache()
				selected, err := model.GetRandomSatisfiedChannel("default", "balance-model", 0, nil)
				require.NoError(t, err)
				require.NotNil(t, selected)
				assert.Equal(t, channels[2].Id, selected.Id)
			}

			// Disabling balance queries stops upstream reads while retaining the known debt.
			setting := `{"balance_query_disabled":true}`
			single.Setting = &setting
			require.NoError(t, db.Model(single).Update("setting", setting).Error)
			beforeQueries := queries.Load()
			_, err := updateChannelBalance(single)
			require.ErrorIs(t, err, model.ErrChannelBalanceQueryDisabled)
			assert.Equal(t, beforeQueries, queries.Load())
			assert.False(t, single.HasRelayBalance())
			setting = `{}`
			require.NoError(t, db.Model(single).Update("setting", setting).Error)

			// A completed positive reading restores routing immediately, including cached channels.
			debtCents.Store(4200)
			_, err = updateChannelBalance(single)
			require.NoError(t, err)
			assert.True(t, single.HasRelayBalance())
			selected, err := model.GetRandomSatisfiedChannel("default", "balance-model", 0, nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
			assert.Equal(t, single.Id, selected.Id)

			// Reordered/replaced credentials cannot inherit another key's balance by position.
			debtCents.Store(-100)
			_, err = updateChannelBalance(single)
			require.NoError(t, err)
			assert.False(t, single.HasRelayBalance())
			single.Key = "replacement"
			require.NoError(t, db.Model(single).Update("key", single.Key).Error)
			assert.True(t, single.HasRelayBalance())
			multi.Key = "funds\ndebt-a\ndebt-b"
			multi.Keys = nil
			require.NoError(t, db.Model(multi).Update("key", multi.Key).Error)
			key, index, apiErr := multi.GetNextRelayKey()
			require.Nil(t, apiErr)
			assert.NotEqual(t, 1, index, "manual status remains bound to its configured position")
			assert.NotEmpty(t, key)

			// Partial readings filter known debt while an unqueried key remains eligible.
			debtCents.Store(-100)
			multi.Key = "debt-a\nunknown\ndebt-b"
			multi.Keys = nil
			multi.ChannelInfo.MultiKeyStatusList = nil
			require.NoError(t, db.Model(multi).Update("key", multi.Key).Error)
			require.NoError(t, multi.SaveChannelInfo())
			_, err = updateChannelBalance(multi)
			require.NoError(t, err)
			key, index, apiErr = multi.GetNextRelayKey()
			require.Nil(t, apiErr)
			assert.Equal(t, "unknown", key)
			assert.Equal(t, 1, index)
			// Upgrade an old JSON observation without a last-known field.
			var previousSample model.ChannelBalanceSample
			require.NoError(t, db.Where("channel_id = ?", multi.Id).Order("id DESC").First(&previousSample).Error)
			require.NoError(t, db.Model(&previousSample).Update("key_balances_json", `[{"index":0,"balance":-1},{"index":1,"balance":null,"error":"balance_query_failed"},{"index":2,"balance":-1}]`).Error)
			rejectDebt.Store(true)
			_, err = updateChannelBalance(multi)
			require.Error(t, err)
			assert.True(t, multi.HasRelayBalance(), "the never-observed key remains available while known debt stays excluded")
			monitor, err := model.GetChannelBalanceMonitor(multi.Id)
			require.NoError(t, err)
			require.Len(t, monitor.KeyBalances, 3)
			assert.False(t, monitor.Success)
			assert.Nil(t, monitor.KeyBalances[0].Balance, "the current failed reading must remain visible as unknown")
			assert.NotEmpty(t, monitor.KeyBalances[0].Error)
			require.NotNil(t, monitor.KeyBalances[0].LastKnownBalance)
			assert.Equal(t, float64(-1), *monitor.KeyBalances[0].LastKnownBalance)
			assert.Nil(t, monitor.KeyBalances[1].LastKnownBalance)
			multi.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom
			multi.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled, 2: common.ChannelStatusManuallyDisabled}
			require.NoError(t, multi.SaveChannelInfo())
			for _, mode := range []constant.MultiKeyMode{constant.MultiKeyModeRandom, constant.MultiKeyModePolling, ""} {
				multi.ChannelInfo.MultiKeyMode = mode
				_, _, apiErr = multi.GetNextRelayKey()
				require.NotNil(t, apiErr, "a failed query cannot make the only enabled, previously negative key eligible")
				assert.Equal(t, model.ErrorCodeChannelBalanceUnavailable, apiErr.GetErrorCode())
			}
			model.InvalidateChannelBalanceRouting(multi.Id)
			assert.False(t, multi.HasRelayBalance(), "a cold cache must recover the retained negative value from SQL")
			_, err = updateChannelBalance(multi)
			require.Error(t, err)
			assert.False(t, multi.HasRelayBalance(), "consecutive failures must retain the same last-known debt")
			rejectDebt.Store(false)
			for _, cents := range []int64{0, 2500} {
				debtCents.Store(cents)
				_, err = updateChannelBalance(multi)
				require.NoError(t, err)
				assert.True(t, multi.HasRelayBalance(), "a new zero or positive observation restores the enabled key immediately")
				key, index, apiErr = multi.GetNextRelayKey()
				require.Nil(t, apiErr)
				assert.Equal(t, "debt-a", key)
				assert.Equal(t, 0, index)
				monitor, err = model.GetChannelBalanceMonitor(multi.Id)
				require.NoError(t, err)
				require.NotNil(t, monitor.KeyBalances[0].LastKnownBalance)
				assert.Equal(t, float64(cents)/100, *monitor.KeyBalances[0].LastKnownBalance)
			}
			for _, channel := range channels {
				var stored model.Channel
				require.NoError(t, db.First(&stored, channel.Id).Error)
				assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "balance filtering must not change channel enablement")
			}
		})
	}
}

func channelWithSetting(channelType int, setting string) *model.Channel {
	channel := &model.Channel{Type: channelType}
	if setting != "" {
		channel.Setting = &setting
	}
	return channel
}

// A gateway can relay one provider's protocol while billing through another's
// API, so the channel's own type must not be the only thing that decides how
// its balance is read.
func TestResolveBalanceQueryHonoursTheChannelOverride(t *testing.T) {
	cases := []struct {
		name        string
		channelType int
		setting     string
		wantType    int
		wantBaseURL string
	}{
		{
			name:        "no override follows the channel type",
			channelType: constant.ChannelTypeGemini,
			wantType:    constant.ChannelTypeGemini,
		},
		{
			name:        "a Gemini channel can be billed through the OpenAI API",
			channelType: constant.ChannelTypeGemini,
			setting:     `{"balance_query_type":"openai"}`,
			wantType:    constant.ChannelTypeOpenAI,
		},
		{
			name:        "the query address overrides the relay address",
			channelType: constant.ChannelTypeGemini,
			setting:     `{"balance_query_type":"openai","balance_query_base_url":"https://billing.example.com/"}`,
			wantType:    constant.ChannelTypeOpenAI,
			wantBaseURL: "https://billing.example.com",
		},
		{
			name:        "an unknown override is ignored rather than failing the query",
			channelType: constant.ChannelTypeGemini,
			setting:     `{"balance_query_type":"not-a-provider"}`,
			wantType:    constant.ChannelTypeGemini,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			queryType, baseURL := resolveBalanceQuery(
				channelWithSetting(testCase.channelType, testCase.setting))
			assert.Equal(t, testCase.wantType, queryType)
			assert.Equal(t, testCase.wantBaseURL, baseURL)
		})
	}
}

// The override is rejected at save time so an unusable value cannot be stored,
// while an empty one keeps meaning "use the channel's type".
func TestValidateSettingsRejectsAnUnknownBalanceQuery(t *testing.T) {
	require.True(t, constant.IsValidBalanceQueryType(""))
	require.True(t, constant.IsValidBalanceQueryType("OpenAI"))
	require.False(t, constant.IsValidBalanceQueryType("not-a-provider"))

	err := channelWithSetting(constant.ChannelTypeGemini,
		`{"balance_query_type":"not-a-provider"}`).ValidateSettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "balance_query_type")

	err = channelWithSetting(constant.ChannelTypeGemini,
		`{"balance_query_base_url":"ftp://billing.example.com"}`).ValidateSettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "balance_query_base_url")

	require.NoError(t, channelWithSetting(constant.ChannelTypeGemini,
		`{"balance_query_type":"openai","balance_query_base_url":"https://billing.example.com"}`).ValidateSettings())

}

// billingServerForKeys serves the OpenAI billing endpoints, giving each key the
// hard limit registered for it and rejecting any key that has none.
func billingServerForKeys(t *testing.T, hardLimits map[string]float64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		hardLimit, ok := hardLimits[key]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/subscription") {
			fmt.Fprintf(w, `{"has_payment_method":true,"hard_limit_usd":%v}`, hardLimit)
			return
		}
		fmt.Fprint(w, `{"total_usage":0}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func multiKeyChannelForBalance(t *testing.T, baseURL string, keys ...string) *model.Channel {
	t.Helper()
	originalDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.ChannelBalanceSample{}))
	sqlDB, err := database.DB()
	require.NoError(t, err)
	originalType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	model.DB = database
	t.Cleanup(func() {
		model.DB = originalDB
		common.SetMainDatabaseType(originalType)
		require.NoError(t, sqlDB.Close())
	})

	channel := &model.Channel{
		Type:    constant.ChannelTypeOpenAI,
		Name:    "multi-key",
		Key:     strings.Join(keys, "\n"),
		BaseURL: &baseURL,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: len(keys),
		},
	}
	require.NoError(t, database.Create(channel).Error)
	return channel
}

func TestMultiKeyBalanceSumsEveryKey(t *testing.T) {
	server := billingServerForKeys(t, map[string]float64{
		"key-a": 3, "key-b": 4, "key-c": 5,
	})
	channel := multiKeyChannelForBalance(t, server.URL, "key-a", "key-b", "key-c")
	// Disabled keys still own upstream funds, and their state must survive.
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
	require.NoError(t, channel.SaveChannelInfo())

	result, err := updateChannelBalance(channel)
	require.NoError(t, err)
	assert.Equal(t, float64(12), result.Balance)
	assert.False(t, result.Partial)
	require.Len(t, result.KeyBalances, 3)
	for index, balance := range []float64{3, 4, 5} {
		assert.Equal(t, index, result.KeyBalances[index].Index)
		require.NotNil(t, result.KeyBalances[index].Balance)
		assert.Equal(t, balance, *result.KeyBalances[index].Balance)
	}

	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, float64(12), stored.Balance)
	assert.Equal(t, channel.ChannelInfo.MultiKeyStatusList, stored.ChannelInfo.MultiKeyStatusList)
	monitor, err := model.GetChannelBalanceMonitor(channel.Id)
	require.NoError(t, err)
	require.NotNil(t, monitor)
	assert.Equal(t, result.Monitor, monitor)
	assert.Equal(t, result.KeyBalances, monitor.KeyBalances)
}

func TestMultiKeyBalanceDoesNotOffsetAvailableFunds(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ChannelBalanceSample{}))
			balances := map[string]float64{"debt-a": -680.88, "funds": 101158.01, "debt-b": -680.88, "zero": 0}
			server := billingServerForKeys(t, balances)
			for _, tc := range []struct {
				name           string
				keys           []string
				multi, partial bool
				want           float64
			}{
				{"mixed", []string{"debt-a", "funds", "debt-b"}, true, false, 101158.01},
				{"nonpositive", []string{"debt-a", "debt-b", "zero"}, true, false, 0},
				{"partial", []string{"debt-a", "funds", "revoked"}, true, true, 101158.01},
				{"negative_and_failed", []string{"debt-a", "revoked"}, true, true, 0},
				{"one_multi_key", []string{"debt-a"}, true, false, 0},
				{"single_account", []string{"debt-a"}, false, false, -680.88},
			} {
				t.Run(tc.name, func(t *testing.T) {
					channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: tc.name, Key: strings.Join(tc.keys, "\n"), BaseURL: &server.URL, Balance: 40, BalanceUpdatedTime: 100, ChannelInfo: model.ChannelInfo{IsMultiKey: tc.multi, MultiKeySize: len(tc.keys)}}
					require.NoError(t, db.Create(channel).Error)
					result, err := updateChannelBalance(channel)
					require.NoError(t, err)
					assert.Equal(t, tc.want, result.Balance)
					assert.Equal(t, tc.partial, result.Partial)
					assert.Equal(t, !tc.partial, result.Monitor.Success, "negative numbers are successful balance readings")
					for i, key := range tc.keys {
						if value, ok := balances[key]; ok {
							require.NotNil(t, result.KeyBalances[i].Balance)
							assert.Equal(t, value, *result.KeyBalances[i].Balance)
							assert.Empty(t, result.KeyBalances[i].Error)
						}
					}
					monitor, err := model.GetChannelBalanceMonitor(channel.Id)
					require.NoError(t, err)
					assert.Equal(t, result.Monitor, monitor)
					assert.Equal(t, tc.want, monitor.KnownBalance)
					stored, err := model.GetChannelById(channel.Id, false)
					require.NoError(t, err)
					wantComplete := tc.want
					if tc.partial {
						wantComplete = 40
						assert.EqualValues(t, 100, monitor.BalanceUpdatedTime)
					}
					assert.Equal(t, wantComplete, stored.Balance)
					require.NotNil(t, monitor.Balance)
					assert.Equal(t, wantComplete, *monitor.Balance)
					history, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
					require.NoError(t, err)
					require.Len(t, history, 1)
					assert.Equal(t, tc.want, history[0].KnownBalance)
					assert.Equal(t, result.KeyBalances, history[0].KeyBalances)
				})
			}
		})
	}
}

func TestAdvancedCustomNegativeBalanceIsNumeric(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"object":"credit_summary","total_available":-680.88}`)
	}))
	t.Cleanup(server.Close)
	other := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: dto.AdvancedCustomBalancePath, UpstreamPath: "/balance"}}}}
	encoded, err := common.Marshal(other)
	require.NoError(t, err)
	channel := &model.Channel{Type: constant.ChannelTypeAdvancedCustom, Key: "fixture", BaseURL: &server.URL, OtherSettings: string(encoded)}
	result, err := fetchAdvancedCustomBalance(context.Background(), channel)
	require.NoError(t, err)
	assert.Equal(t, -680.88, result.Balance)
	assert.Empty(t, result.RawResponse)
}

func TestMultiKeyBalancePartialFailurePreservesCompleteBalance(t *testing.T) {
	server := billingServerForKeys(t, map[string]float64{"key-a": 7})
	channel := multiKeyChannelForBalance(t, server.URL, "key-a", "revoked")
	channel.Balance = 20
	channel.BalanceUpdatedTime = 100
	require.NoError(t, model.DB.Save(channel).Error)

	legacy, err := model.GetChannelBalanceMonitor(channel.Id)
	require.NoError(t, err)
	require.NotNil(t, legacy)
	require.NotNil(t, legacy.Balance)
	assert.Equal(t, float64(20), *legacy.Balance)
	assert.Equal(t, int64(100), legacy.BalanceUpdatedTime)

	result, err := updateChannelBalance(channel)
	require.NoError(t, err)
	assert.Equal(t, float64(7), result.Balance)
	assert.True(t, result.Partial)
	require.Len(t, result.KeyBalances, 2)
	require.NotNil(t, result.KeyBalances[0].Balance)
	assert.Equal(t, float64(7), *result.KeyBalances[0].Balance)
	assert.Nil(t, result.KeyBalances[1].Balance)
	assert.Equal(t, "balance_query_failed", result.KeyBalances[1].Error)

	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, float64(20), stored.Balance)
	assert.Equal(t, int64(100), stored.BalanceUpdatedTime)
	monitor, err := model.GetChannelBalanceMonitor(channel.Id)
	require.NoError(t, err)
	require.NotNil(t, monitor.Balance)
	assert.Equal(t, float64(20), *monitor.Balance)
	assert.Equal(t, float64(7), monitor.KnownBalance)
	assert.True(t, monitor.Partial)
	assert.False(t, monitor.Success)
}

func TestMultiKeyBalanceAllFailuresRemainObservable(t *testing.T) {
	server := billingServerForKeys(t, map[string]float64{})
	channel := multiKeyChannelForBalance(t, server.URL, "revoked-a", "revoked-b")
	channel.Balance = 9
	channel.BalanceUpdatedTime = 100
	require.NoError(t, model.DB.Save(channel).Error)

	result, err := updateChannelBalance(channel)
	require.Error(t, err)
	require.NotNil(t, result.Monitor)
	assert.False(t, result.Monitor.Success)
	assert.False(t, result.Monitor.Partial)
	assert.Equal(t, int64(100), result.Monitor.BalanceUpdatedTime)

	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, float64(9), stored.Balance)
	assert.Equal(t, int64(100), stored.BalanceUpdatedTime)
	samples, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.False(t, samples[0].Success)
	require.Len(t, samples[0].KeyBalances, 2)
	assert.Nil(t, samples[0].KeyBalances[0].Balance)
	assert.Nil(t, samples[0].KeyBalances[1].Balance)
	encoded, err := common.Marshal(samples)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "revoked-a")
	assert.NotContains(t, string(encoded), "revoked-b")
	assert.NotContains(t, string(encoded), "ConfigurationHash")
}

func TestMultiKeyBalanceFirstPartialResultHasNoCompleteTotal(t *testing.T) {
	server := billingServerForKeys(t, map[string]float64{"key-a": 7})
	channel := multiKeyChannelForBalance(t, server.URL, "key-a", "revoked")
	result, err := updateChannelBalance(channel)
	require.NoError(t, err)
	require.NotNil(t, result.Monitor)
	assert.Nil(t, result.Monitor.Balance)
	assert.Zero(t, result.Monitor.BalanceUpdatedTime)
	assert.Equal(t, float64(7), result.Monitor.KnownBalance)
	assert.True(t, result.Monitor.Partial)
}

func TestBalanceErrorsDoNotExposeUpstreamCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a gateway echoing credentials inside a non-numeric value.
		fmt.Fprintf(w, `{"hard_limit_usd":%q}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(server.Close)
	channel := multiKeyChannelForBalance(t, server.URL, "secret-billing-key-a", "secret-billing-key-b")
	result, err := updateChannelBalance(channel)
	require.Error(t, err)
	encoded, marshalErr := common.Marshal(result.Monitor)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(encoded), "secret-billing-key")
	assert.NotContains(t, err.Error(), "secret-billing-key")
	assert.NotContains(t, string(encoded), "Bearer")
	assert.Contains(t, string(encoded), "balance_query_failed")
}

func TestMultiKeyBalanceInvalidatesReorderedAndReplacedKeys(t *testing.T) {
	server := billingServerForKeys(t, map[string]float64{"key-a": 3, "key-b": 4})
	channel := multiKeyChannelForBalance(t, server.URL, "key-a", "key-b")
	_, err := updateChannelBalance(channel)
	require.NoError(t, err)

	// Background model discovery and relay-only settings do not change which
	// upstream accounts produced the balances.
	require.NoError(t, model.DB.Model(channel).Updates(map[string]any{
		"settings": `{"upstream_model_update_last_check_time":1234}`,
		"setting":  `{"system_prompt":"Updated prompt"}`,
	}).Error)
	unchanged, err := model.GetChannelBalanceMonitor(channel.Id)
	require.NoError(t, err)
	assert.False(t, unchanged.ConfigurationChanged)
	require.NotNil(t, unchanged.Balance)
	assert.Equal(t, float64(7), *unchanged.Balance)

	for _, keys := range []string{"key-b\nkey-a", "new-key\nkey-b"} {
		require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("key", keys).Error)
		monitor, err := model.GetChannelBalanceMonitor(channel.Id)
		require.NoError(t, err)
		require.NotNil(t, monitor)
		assert.True(t, monitor.ConfigurationChanged)
		assert.Nil(t, monitor.Balance)
		assert.Empty(t, monitor.KeyBalances)
		_, err = updateChannelBalance(channel) // old in-flight configuration
		require.ErrorIs(t, err, model.ErrChannelBalanceConfigurationChanged)
	}
	samples, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
	require.NoError(t, err)
	assert.Len(t, samples, 1)
}

func TestChannelBalanceHistoryAndNewerCheckProtection(t *testing.T) {
	channel := multiKeyChannelForBalance(t, "https://example.invalid", "key-a")
	channel.UsedQuota = 1234
	require.NoError(t, model.DB.Save(channel).Error)
	first := &model.ChannelBalanceSample{StartedAt: 10, CheckedAt: 100, Success: true, KnownBalance: 12, KeyBalances: []model.ChannelKeyBalance{}}
	require.NoError(t, model.RecordChannelBalanceSample(channel, first))
	second := &model.ChannelBalanceSample{StartedAt: 20, CheckedAt: 200, Partial: true, KeyBalances: []model.ChannelKeyBalance{{Index: 0, Error: "balance_query_failed"}}}
	require.NoError(t, model.RecordChannelBalanceSample(channel, second))
	stale := &model.ChannelBalanceSample{StartedAt: 15, CheckedAt: 250, Success: true, KnownBalance: 99}
	require.Error(t, model.RecordChannelBalanceSample(channel, stale))
	samples, err := model.ListChannelBalanceSamples(channel.Id, 0, 200, 100)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, int64(100), samples[0].CheckedAt)
	assert.Equal(t, int64(200), samples[1].CheckedAt)
	assert.Equal(t, int64(1234), samples[1].UsedQuota)
	require.NotNil(t, samples[1].Balance)
	assert.Equal(t, float64(12), *samples[1].Balance)
	assert.Equal(t, int64(100), samples[1].BalanceUpdatedTime)
	samples, err = model.ListChannelBalanceSamples(channel.Id, 150, 300, 1)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, int64(200), samples[0].CheckedAt)
}

func TestBalanceSweepMonitorsDisabledChannelsWithoutChangingStatus(t *testing.T) {
	server := billingServerForKeys(t, map[string]float64{"key-a": 0})
	channel := multiKeyChannelForBalance(t, server.URL, "key-a")
	channel.Status = common.ChannelStatusManuallyDisabled
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusManuallyDisabled}
	require.NoError(t, model.DB.Save(channel).Error)
	summary, err := updateAllChannelsBalance(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, channelBalanceSummary{Total: 1, Succeeded: 1}, summary)
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, channel.ChannelInfo.MultiKeyStatusList, stored.ChannelInfo.MultiKeyStatusList)
	assert.Zero(t, stored.Balance)
}

func TestBalanceSweepCancellationDoesNotWriteFailure(t *testing.T) {
	channel := multiKeyChannelForBalance(t, "https://example.invalid", "key-a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := updateChannelBalanceWithContext(ctx, channel)
	require.ErrorIs(t, err, context.Canceled)
	samples, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
	require.NoError(t, err)
	assert.Empty(t, samples)
}

func TestChannelBalanceQuerySwitch(t *testing.T) {
	previousInterval := common.RequestInterval
	common.RequestInterval = 0
	t.Cleanup(func() { common.RequestInterval = previousInterval })
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			database := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, database.AutoMigrate(&model.ChannelBalanceSample{}))
			var requests, disableAt atomic.Int64
			var channelID int
			var disabledSetting string
			disableResults := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == disableAt.Load() {
					// Close the switch after a query has started but before its
					// response is available to the balance collector.
					disableResults <- database.Model(&model.Channel{}).Where("id = ?", channelID).Update("setting", disabledSetting).Error
				}
				if strings.HasSuffix(r.URL.Path, "/subscription") {
					fmt.Fprint(w, `{"has_payment_method":true,"hard_limit_usd":5}`)
					return
				}
				fmt.Fprint(w, `{"total_usage":0}`)
			}))
			t.Cleanup(server.Close)
			channel := &model.Channel{
				Type: constant.ChannelTypeOpenAI, Name: "balance query switch", Key: "key-a\nkey-b", BaseURL: &server.URL,
				Status: common.ChannelStatusManuallyDisabled, Balance: 17, BalanceUpdatedTime: 100, UsedQuota: 1234,
				ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyPollingIndex: 1, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}},
			}
			setting := dto.ChannelSettings{SystemPrompt: "preserve prompt", HTTPProtocol: "http1", BalanceQueryType: "openai", BatchEnabled: true}
			channel.SetSetting(setting)
			require.NoError(t, channel.ValidateSettings())
			require.NoError(t, database.Create(channel).Error)
			channelID = channel.Id
			staleEnabled := *channel

			result, err := updateChannelBalance(channel)
			require.NoError(t, err, "old configurations without the flag allow balance queries")
			assert.Equal(t, int64(4), requests.Load())
			assert.Equal(t, float64(10), result.Balance)
			beforeMonitor := result.Monitor
			var before model.Channel
			require.NoError(t, database.First(&before, channel.Id).Error)
			beforeSamples, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
			require.NoError(t, err)
			require.Len(t, beforeSamples, 1)

			setting.BalanceQueryDisabled = true
			channel.SetSetting(setting)
			disabledSetting = *channel.Setting
			require.NoError(t, channel.ValidateSettings())
			require.NoError(t, database.Model(channel).Update("setting", disabledSetting).Error)
			staleDisabled := *channel
			engine := gin.New()
			engine.GET("/balance/:id", UpdateChannelBalance)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/balance/%d", channel.Id), nil))
			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.False(t, response.Success)
			assert.Equal(t, model.ErrChannelBalanceQueryDisabled.Error(), response.Message)
			_, err = updateChannelBalance(&staleEnabled)
			require.ErrorIs(t, err, model.ErrChannelBalanceQueryDisabled)
			summary, err := updateAllChannelsBalance(context.Background(), nil)
			require.NoError(t, err)
			assert.Equal(t, channelBalanceSummary{Total: 1, Skipped: 1}, summary)
			require.ErrorIs(t, model.RecordChannelBalanceSample(&staleEnabled, &model.ChannelBalanceSample{
				StartedAt: time.Now().UnixNano(), CheckedAt: common.GetTimestamp(), Success: true, KnownBalance: 99,
			}), model.ErrChannelBalanceQueryDisabled)
			assert.Equal(t, int64(4), requests.Load(), "disabled manual and batch calls must issue no upstream requests")
			afterMonitor, err := model.GetChannelBalanceMonitor(channel.Id)
			require.NoError(t, err)
			assert.Equal(t, beforeMonitor, afterMonitor, "the switch must not invalidate the prior balance or refresh its timestamp")
			afterSamples, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
			require.NoError(t, err)
			assert.Equal(t, beforeSamples, afterSamples, "skipped queries must not add failure records")
			var stored model.Channel
			require.NoError(t, database.First(&stored, channel.Id).Error)
			before.Setting = stored.Setting
			assert.Equal(t, before, stored, "changing the switch must preserve keys, routing state, polling state and historical usage")
			var restoredSetting dto.ChannelSettings
			require.NoError(t, common.UnmarshalJsonStr(*stored.Setting, &restoredSetting))
			assert.Equal(t, setting, restoredSetting, "the flag round-trip must preserve other channel settings")

			setting.BalanceQueryDisabled = false
			channel.SetSetting(setting)
			require.NoError(t, database.Model(channel).Update("setting", *channel.Setting).Error)
			result, err = updateChannelBalance(&staleDisabled)
			require.NoError(t, err, "reopening takes effect even when the caller holds a disabled snapshot")
			assert.Equal(t, int64(8), requests.Load())
			assert.Equal(t, float64(10), result.Balance)

			first := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "first in sweep", Key: "key-c", BaseURL: &server.URL, Priority: common.GetPointer(int64(10))}
			require.NoError(t, database.Create(first).Error)
			summary, err = updateAllChannelsBalance(context.Background(), func(processed, total int) {
				if processed == 1 {
					require.NoError(t, database.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("setting", disabledSetting).Error)
				}
			})
			require.NoError(t, err)
			assert.Equal(t, channelBalanceSummary{Total: 2, Succeeded: 1, Skipped: 1}, summary)
			assert.Equal(t, int64(10), requests.Load(), "a sweep must recheck a queued channel after fetching its old snapshot")

			for _, keyNumber := range []int64{1, 2} {
				require.NoError(t, database.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("setting", *channel.Setting).Error)
				beforeSamples, err = model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
				require.NoError(t, err)
				beforeMonitor, err = model.GetChannelBalanceMonitor(channel.Id)
				require.NoError(t, err)
				beforeRequests := requests.Load()
				disableAt.Store(beforeRequests + keyNumber*2)
				_, err = updateChannelBalance(channel)
				require.ErrorIs(t, err, model.ErrChannelBalanceQueryDisabled)
				require.NoError(t, <-disableResults)
				assert.Equal(t, beforeRequests+keyNumber*2, requests.Load(), "closing during a check must stop any remaining keys")
				afterSamples, err = model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
				require.NoError(t, err)
				assert.Equal(t, beforeSamples, afterSamples, "an in-flight response must not persist after the switch closes")
				afterMonitor, err = model.GetChannelBalanceMonitor(channel.Id)
				require.NoError(t, err)
				assert.Equal(t, beforeMonitor, afterMonitor)
			}
		})
	}
}

func TestBalanceScheduleIntervalValidation(t *testing.T) {
	t.Setenv("CHANNEL_UPDATE_FREQUENCY", "")
	setting := operation_setting.GetMonitorSetting()
	original := *setting
	t.Cleanup(func() { *setting = original })
	setting.AutoUpdateBalanceEnabled = true
	setting.AutoUpdateBalanceMinutes = 15
	assert.True(t, (channelBalanceHandler{}).Enabled())
	assert.Equal(t, 15*time.Minute, (channelBalanceHandler{}).Interval())
	for _, value := range []string{"0", "-1", "NaN", "+Inf", "10081"} {
		require.Error(t, operation_setting.ValidateAutoUpdateBalanceMinutes(value))
	}
	require.NoError(t, operation_setting.ValidateAutoUpdateBalanceMinutes("1"))
	require.NoError(t, operation_setting.ValidateAutoUpdateBalanceMinutes("60"))
	t.Setenv("CHANNEL_UPDATE_FREQUENCY", "90")
	assert.Equal(t, 90*time.Minute, (channelBalanceHandler{}).Interval())
}

// The existing Channel schema is unchanged: BalanceMonitor is gorm:"-".
// Upgrades start with that released schema and populated channel state; fresh
// installs create both tables together. Both paths migrate twice with data.
func TestChannelBalanceDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		for _, upgrade := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/upgrade=%t", dialect.kind, upgrade), func(t *testing.T) {
				if dialect.env != "" && os.Getenv(dialect.env) == "" {
					t.Skip("set " + dialect.env + " to run this database")
				}
				database, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
				originalDB, originalKind := model.DB, common.MainDatabaseType()
				model.DB = database
				common.SetMainDatabaseType(common.DatabaseType(dialect.kind))
				t.Cleanup(func() {
					model.DB = originalDB
					common.SetMainDatabaseType(originalKind)
				})
				versionQuery := "SELECT version()"
				if dialect.kind == "sqlite" {
					versionQuery = "SELECT sqlite_version()"
				}
				var version string
				require.NoError(t, database.Raw(versionQuery).Scan(&version).Error)
				t.Logf("database version: %s", version)
				server := billingServerForKeys(t, map[string]float64{"key-a": 5, "key-b": 8})
				channel := &model.Channel{
					Type: constant.ChannelTypeOpenAI, Name: "balance migration fixture",
					Key: "key-a\nkey-b", BaseURL: &server.URL,
					Balance: 42, BalanceUpdatedTime: 100, UsedQuota: 9876,
					Status:      common.ChannelStatusManuallyDisabled,
					ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusManuallyDisabled}},
				}
				if upgrade {
					require.NoError(t, database.AutoMigrate(&model.Channel{}))
					require.False(t, database.Migrator().HasTable(&model.ChannelBalanceSample{}))
					require.NoError(t, database.Create(channel).Error)
				}
				for pass := 0; pass < 2; pass++ {
					require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.ChannelBalanceSample{}))
				}
				if !upgrade {
					require.NoError(t, database.Create(channel).Error)
				}
				var persisted model.Channel
				require.NoError(t, database.First(&persisted, channel.Id).Error)
				assert.Equal(t, channel.Key, persisted.Key)
				assert.Equal(t, channel.UsedQuota, persisted.UsedQuota)
				assert.Equal(t, channel.Balance, persisted.Balance)
				assert.Equal(t, channel.ChannelInfo, persisted.ChannelInfo)
				require.True(t, database.Migrator().HasIndex(&model.ChannelBalanceSample{}, "idx_channel_balance_time"))
				result, err := updateChannelBalance(channel)
				require.NoError(t, err)
				assert.Equal(t, float64(13), result.Balance)
				require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.ChannelBalanceSample{}))
				require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.ChannelBalanceSample{}))
				monitor, err := model.GetChannelBalanceMonitor(channel.Id)
				require.NoError(t, err)
				require.NotNil(t, monitor)
				require.NotNil(t, monitor.Balance)
				assert.Equal(t, float64(13), *monitor.Balance)
				assert.True(t, monitor.Success)
				assert.False(t, monitor.Partial)
				assert.Equal(t, result.KeyBalances, monitor.KeyBalances)

				failed := &model.ChannelBalanceSample{StartedAt: time.Now().UnixNano(), CheckedAt: result.Monitor.CheckedAt + 1, Partial: true, KnownBalance: 5,
					KeyBalances: []model.ChannelKeyBalance{{Index: 0, Balance: common.GetPointer(float64(5))}, {Index: 1, Error: "balance_query_failed"}}}
				require.NoError(t, model.RecordChannelBalanceSample(channel, failed))
				samples, err := model.ListChannelBalanceSamples(channel.Id, 0, 0, 100)
				require.NoError(t, err)
				require.Len(t, samples, 2)
				assert.Equal(t, channel.UsedQuota, samples[1].UsedQuota)
				assert.Equal(t, monitor.BalanceUpdatedTime, samples[1].BalanceUpdatedTime)
				require.NotNil(t, samples[1].Balance)
				assert.Equal(t, float64(13), *samples[1].Balance)
				require.NoError(t, database.First(&persisted, channel.Id).Error)
				assert.Equal(t, common.ChannelStatusManuallyDisabled, persisted.Status)
				assert.Equal(t, channel.ChannelInfo, persisted.ChannelInfo)
				require.NoError(t, database.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("key", "key-b\nkey-a").Error)
				monitor, err = model.GetChannelBalanceMonitor(channel.Id)
				require.NoError(t, err)
				assert.True(t, monitor.ConfigurationChanged)
				assert.Nil(t, monitor.Balance)
				require.ErrorIs(t, model.RecordChannelBalanceSample(channel, &model.ChannelBalanceSample{StartedAt: time.Now().UnixNano()}), model.ErrChannelBalanceConfigurationChanged)
			})
		}
	}
}

func TestChannelMonitoringReadAPIs(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.ChannelBalanceSample{}, &model.ChannelPerfMetric{}))
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Name: "monitored", UsedQuota: 1234,
		Key: "private-balance-key-a\nprivate-balance-key-b", BaseURL: common.GetPointer("https://billing.example.invalid"),
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
	}
	require.NoError(t, database.Create(channel).Error)
	originalLogDB, originalLogType := model.LOG_DB, common.LogDatabaseType()
	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDB.AutoMigrate(&model.Log{}))
	logConnection, err := logDB.DB()
	require.NoError(t, err)
	model.LOG_DB = logDB
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.LOG_DB = originalLogDB
		common.SetLogDatabaseType(originalLogType)
		require.NoError(t, logConnection.Close())
	})
	now := time.Now().Unix()
	sample := &model.ChannelBalanceSample{
		StartedAt: time.Now().UnixNano(), CheckedAt: now - 10, Success: true, KnownBalance: 12,
		KeyBalances: []model.ChannelKeyBalance{{Index: 0, Balance: common.GetPointer(float64(7))}, {Index: 1, Balance: common.GetPointer(float64(5))}},
	}
	require.NoError(t, model.RecordChannelBalanceSample(channel, sample))
	legacy := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "legacy", Key: "private-legacy-key", Balance: 27.5, BalanceUpdatedTime: now - 60}
	require.NoError(t, model.DB.Create(legacy).Error)
	require.NoError(t, model.UpsertChannelPerfMetric(&model.ChannelPerfMetric{
		ModelName: "monitor-api-model", Group: "default", ChannelID: channel.Id,
		Source: perfmetrics.SourceRequest, BucketTs: now / 3600 * 3600, RequestCount: 2, SuccessCount: 1,
	}))
	require.NoError(t, logDB.Create(&[]model.Log{
		{ChannelId: channel.Id, CreatedAt: now, Type: model.LogTypeConsume, Quota: 500, PromptTokens: 20, CompletionTokens: 10},
		{ChannelId: channel.Id, CreatedAt: now, Type: model.LogTypeRefund, Quota: 50},
		{ChannelId: channel.Id, CreatedAt: now, Type: model.LogTypeConsume, Quota: 100, TokenId: 0, TokenName: "模型测试", Content: "模型测试"},
		{ChannelId: 999, CreatedAt: now, Type: model.LogTypeConsume, Quota: 999},
	}).Error)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/channel/monitoring/:id", GetChannelMonitoring)
	engine.GET("/api/channel/", GetAllChannels)
	engine.GET("/api/channel/search", SearchChannels)
	for _, test := range []struct {
		name    string
		channel *model.Channel
		history int
		balance float64
	}{
		{"monitored", channel, 1, 12},
		{"legacy balance without monitoring history", legacy, 0, 27.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/channel/monitoring/%d?hours=24", test.channel.Id), nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool `json:"success"`
				Data    struct {
					Balance   *model.ChannelBalanceMonitor   `json:"balance"`
					History   []model.ChannelBalanceSample   `json:"balance_history"`
					Usage     perfmetrics.ChannelUsageResult `json:"usage"`
					UsedQuota int64                          `json:"used_quota"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success)
			require.NotNil(t, response.Data.Balance)
			require.NotNil(t, response.Data.Balance.Balance)
			assert.Equal(t, test.balance, *response.Data.Balance.Balance)
			assert.Len(t, response.Data.History, test.history)
			assert.Equal(t, test.channel.Id, response.Data.Usage.ChannelID)
			if test.history > 0 {
				assert.EqualValues(t, 1234, response.Data.UsedQuota)
				assert.EqualValues(t, 2, response.Data.Usage.RequestCount)
				assert.EqualValues(t, 1, response.Data.Usage.SuccessCount)
				assert.EqualValues(t, 450, response.Data.Usage.RecordedUsedQuota)
				assert.EqualValues(t, 20, response.Data.Usage.RecordedInputTokens)
				assert.EqualValues(t, 10, response.Data.Usage.RecordedOutputTokens)
				assert.EqualValues(t, 2, response.Data.Usage.BillingRecordCount)
				assert.Equal(t, sample.KeyBalances, response.Data.Balance.KeyBalances)
			} else {
				assert.Equal(t, legacy.BalanceUpdatedTime, response.Data.Balance.BalanceUpdatedTime)
				assert.Zero(t, response.Data.Usage.RecordedUsedQuota)
			}
			for _, secret := range []string{"private-balance-key-a", "private-balance-key-b", "private-legacy-key", sample.ConfigurationHash, "configuration_hash", "key_balances_json"} {
				assert.NotContains(t, recorder.Body.String(), secret)
			}
		})
	}
	for _, path := range []string{"/api/channel/", "/api/channel/search?keyword=monitored"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			var response struct {
				Success bool `json:"success"`
				Data    struct {
					Items []model.Channel `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success)
			require.NotEmpty(t, response.Data.Items)
			for _, item := range response.Data.Items {
				assert.Empty(t, item.Key)
				require.NotNil(t, item.BalanceMonitor)
			}
			for _, secret := range []string{"private-balance-key-a", "private-balance-key-b", "private-legacy-key", sample.ConfigurationHash, "configuration_hash"} {
				assert.NotContains(t, recorder.Body.String(), secret)
			}
		})
	}
	for _, path := range []string{"bad", "0", "-1", "1?hours=169", "1?hours=0", "1?hours=invalid"} {
		t.Run("rejects "+path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/channel/monitoring/"+path, nil))
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
		})
	}
}
