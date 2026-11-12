package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func probeRoutingContext(modelName, group string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, modelName)))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(c, constant.ContextKeyTokenAutoGroups, []string{"default", "vip"})
	return c, recorder
}

func requireProbeRoutingError(t *testing.T, apiErr *types.NewAPIError, message string) {
	t.Helper()
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.Equal(t, model.ErrorCodeChannelProbeUnavailable, apiErr.GetErrorCode())
	assert.Contains(t, apiErr.Error(), message)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.False(t, service.ShouldDisableChannel(apiErr))
	info := &relaycommon.RelayInfo{}
	assert.Equal(t, perfmetrics.OutcomeIgnored, perfmetrics.ClassifyRelayOutcome(nil, info, apiErr))
	assert.Nil(t, perfmetrics.RelayObservation(nil, info, apiErr))
}

func TestDeepSeekInvalidKeyFailoverDatabaseMatrix(t *testing.T) {
	require.NoError(t, i18n.Init())
	previousRetries, previousDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
	previousCount, previousLogs, previousTrace := constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled
	previousErrorLogs := constant.ErrorLogEnabled
	common.RetryTimes, common.AutomaticDisableChannelEnabled = 0, false
	constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = false, false, false, false
	t.Cleanup(func() {
		common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable
		constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = previousCount, previousLogs, previousTrace, previousErrorLogs
	})
	service.InitHttpClient()
	client := service.GetHttpClient()
	previousTransport := client.Transport
	t.Cleanup(func() { client.Transport = previousTransport })
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelBalanceSample{}, &model.Log{}))
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false", "quota_setting.enable_free_model_pre_consume": "false"}))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"fallback":1}`))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"deepseek-flash":0}`))
			user := &model.User{Username: "invalid-key-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Quota: 1000, AffCode: "badkey"}
			require.NoError(t, db.Create(user).Error)
			for _, cached := range []bool{false, true} {
				for _, test := range []struct {
					name                                            string
					chat, duplicate, allBad, single, strict         bool
					cancel, written, changed, custom, otherProvider bool
					groupRestricted                                 bool
					wantCalls, wantStatus, wantDisabled, retries    int
				}{
					{name: "responses_zero_global_retries", wantCalls: 2, wantStatus: 200, wantDisabled: 1},
					{name: "chat_zero_global_retries", chat: true, wantCalls: 2, wantStatus: 200, wantDisabled: 1},
					{name: "duplicate_physical_key", duplicate: true, wantCalls: 2, wantStatus: 200, wantDisabled: 2},
					{name: "all_keys_rejected", allBad: true, wantCalls: 2, wantStatus: 503, wantDisabled: 2},
					{name: "exhausted_keys_use_cross_channel_budget", allBad: true, retries: 3, wantCalls: 3, wantStatus: 200, wantDisabled: 2},
					{name: "single_attempt_pin", single: true, wantCalls: 1, wantStatus: 503, wantDisabled: 1},
					{name: "strict_same_channel", strict: true, wantCalls: 2, wantStatus: 200, wantDisabled: 1},
					{name: "remaining_key_not_allowed_for_group", groupRestricted: true, wantCalls: 1, wantStatus: 503, wantDisabled: 1},
					{name: "client_cancelled", cancel: true, wantCalls: 1, wantStatus: 503, wantDisabled: 1},
					{name: "response_already_written", written: true, wantCalls: 1, wantStatus: 200, wantDisabled: 1},
					{name: "account_changed_during_request", changed: true, wantCalls: 1, wantStatus: 503},
					{name: "custom_endpoint_unchanged", custom: true, wantCalls: 1, wantStatus: 401},
					{name: "other_provider_unchanged", otherProvider: true, wantCalls: 1, wantStatus: 401},
				} {
					t.Run(fmt.Sprintf("cache=%t/%s", cached, test.name), func(t *testing.T) {
						common.MemoryCacheEnabled = cached
						common.RetryTimes = test.retries
						keys := "fixture-invalid-key\nfixture-healthy-key"
						if test.duplicate {
							keys = "fixture-invalid-key\nfixture-invalid-key\nfixture-healthy-key"
						}
						channel := &model.Channel{Type: constant.ChannelTypeDeepSeek, Key: keys, Models: "deepseek-flash", Group: "default", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1), BaseURL: common.GetPointer("https://api.deepseek.com"), ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: len(strings.Split(keys, "\n")), MultiKeyMode: constant.MultiKeyModePolling}}
						if test.custom {
							channel.BaseURL = common.GetPointer("https://custom.invalid")
						}
						if test.otherProvider {
							channel.Type = constant.ChannelTypeOpenAI
						}
						if test.groupRestricted {
							channel.Group = "default,vip"
						}
						if test.retries > 0 {
							channel.Group = "fallback"
						}
						require.NoError(t, channel.Insert())
						if test.groupRestricted {
							require.NoError(t, model.SetChannelKeyGroups(channel.Id, 1, channel.KeyIdentity("fixture-healthy-key"), []string{"vip"}))
						}
						if test.retries > 0 {
							fallback := &model.Channel{Type: constant.ChannelTypeDeepSeek, Key: "fixture-fallback-key", Models: "deepseek-flash", Group: channel.Group, Status: common.ChannelStatusEnabled, BaseURL: channel.BaseURL}
							require.NoError(t, fallback.Insert())
							t.Cleanup(func() {
								require.NoError(t, db.Delete(fallback).Error)
								require.NoError(t, db.Where("channel_id = ?", fallback.Id).Delete(&model.Ability{}).Error)
							})
						}
						model.InitChannelCache()
						t.Cleanup(func() { model.InvalidateChannelProbeRouting(channel.Id) })
						usingGroup := channel.GetGroups()[0]
						c, recorder := probeRoutingContext("deepseek-flash", usingGroup)
						format := types.RelayFormatOpenAI
						if !test.chat {
							format = types.RelayFormatOpenAIResponses
							c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-flash","input":"hello"}`))
							c.Request.Header.Set("Content-Type", "application/json")
						}
						ctx, cancel := context.WithCancel(c.Request.Context())
						defer cancel()
						c.Request = c.Request.WithContext(ctx)
						common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
						common.SetContextKey(c, constant.ContextKeyTokenGroup, usingGroup)
						if test.single {
							service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
						}
						if test.strict {
							service.RequestPolicy(c).SessionMode = "strict"
							c.Set("channel_affinity_skip_retry_on_failure", true)
							require.True(t, service.ShouldSkipRetryAfterChannelAffinityFailure(c))
						}
						var used []string
						client.Transport = channelProbeTestTransport(func(request *http.Request) (*http.Response, error) {
							key := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
							used = append(used, key)
							if len(used) == 1 {
								if test.changed {
									require.NoError(t, db.Model(channel).Update("base_url", "https://replacement.invalid").Error)
								}
								if test.cancel {
									cancel()
								}
								if test.written {
									c.Writer.WriteHeaderNow()
								}
							}
							status := http.StatusOK
							body := `{"id":"resp_fixture","object":"response","status":"completed","model":"deepseek-flash","output":[{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`
							if test.chat {
								body = `{"id":"chat_fixture","model":"deepseek-flash","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
							}
							if key == "fixture-invalid-key" || (test.allBad && key != "fixture-fallback-key") {
								status = http.StatusUnauthorized
								body = `{"error":{"message":"Authentication Fails, Your api key: ****test is invalid","type":"authentication_error","code":"invalid_request_error"}}`
							}
							return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
						})
						require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "deepseek-flash"))
						Relay(c, format)
						assert.Equal(t, test.wantStatus, recorder.Code, recorder.Body.String())
						require.Len(t, used, test.wantCalls)
						assert.Equal(t, "fixture-invalid-key", used[0])
						if len(used) > 1 {
							assert.Equal(t, "fixture-healthy-key", used[1])
						}
						if test.retries > 0 {
							assert.Equal(t, "fixture-fallback-key", used[2])
						}
						stored, err := model.GetChannelById(channel.Id, true)
						require.NoError(t, err)
						assert.Len(t, stored.ChannelInfo.MultiKeyStatusList, test.wantDisabled)
						if test.allBad {
							assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
						} else {
							assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
						}
						if test.wantStatus == 200 && !test.written && !test.allBad {
							assert.Contains(t, recorder.Body.String(), "OK")
							assert.Equal(t, []string{fmt.Sprint(channel.Id), fmt.Sprint(channel.Id)}, c.GetStringSlice("use_channel"))
							for range 2 {
								key, _, apiErr := stored.GetNextRelayKeyForModel("deepseek-flash", false, "default")
								require.Nil(t, apiErr)
								assert.Equal(t, "fixture-healthy-key", key, "later requests must not revive the invalid credential")
							}
						}
						assert.NotContains(t, recorder.Body.String(), "fixture-invalid-key")
					})
				}
			}
		})
	}
}

func TestChannelEndpointExhaustionLanguage(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, test := range []struct{ language, message string }{
		{"en", "No channel request address is available"},
		{"zh-CN", "没有可用的渠道请求地址"},
		{"zh-TW", "沒有可用的渠道請求位址"},
	} {
		t.Run(test.language, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Request.Header.Set("Accept-Language", test.language)
			channel := &model.Channel{Id: -1, BaseURL: common.GetPointer("https://exhaustion-primary.invalid")}
			channel.SetSetting(kitdto.ChannelSettings{EndpointMode: "failover", AdditionalBaseURLs: []string{"https://exhaustion-backup.invalid"}})
			common.SetContextKey(c, constant.ContextKeyChannelId, channel.Id)
			for attempt := range 2 {
				endpoint, err := service.SelectChannelEndpoint(c, channel)
				require.NoError(t, err)
				target, err := url.Parse(endpoint + "/v1/chat/completions")
				require.NoError(t, err)
				service.RecordChannelEndpointRequest(c, target)
				failed, retry := service.FailChannelEndpoint(c, channel, types.NewErrorWithStatusCode(errors.New("fixture mirror unavailable"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway))
				assert.True(t, failed)
				assert.Equal(t, attempt == 0, retry)
			}
			_, err := service.SelectChannelEndpoint(c, channel)
			assert.EqualError(t, err, test.message)
		})
	}
}

func TestChannelEndpointFailover(t *testing.T) {
	require.NoError(t, i18n.Init())
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelBalanceSample{}, &model.Log{}))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false", "quota_setting.enable_free_model_pre_consume": "false"}))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"deepseek-flash":0}`))
	previousRetries, previousDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
	previousCount, previousLogs, previousTrace, previousErrors := constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled
	common.RetryTimes, common.AutomaticDisableChannelEnabled = 0, false
	constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = false, false, false, false
	t.Cleanup(func() {
		common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable
		constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = previousCount, previousLogs, previousTrace, previousErrors
	})
	service.InitHttpClient()
	client := service.GetHttpClient()
	previousTransport := client.Transport
	t.Cleanup(func() { client.Transport = previousTransport })
	user := &model.User{Username: "mirror-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Quota: 1000, AffCode: "mirror"}
	require.NoError(t, db.Create(user).Error)
	for _, test := range []struct {
		name                                                                    string
		status                                                                  int
		network, allFailed, single, cancel, written, absolute, probe, scheduled bool
		wantCalls, wantStatus                                                   int
	}{
		{name: "primary_503", status: 503, wantCalls: 2, wantStatus: 200},
		{name: "network_error", network: true, wantCalls: 2, wantStatus: 200},
		{name: "all_mirrors_failed", status: 502, allFailed: true, wantCalls: 2, wantStatus: 502},
		{name: "rate_limit_not_mirror_failure", status: 429, wantCalls: 1, wantStatus: 429},
		{name: "business_error_not_mirror_failure", status: 400, wantCalls: 1, wantStatus: 400},
		{name: "explicit_single_attempt", status: 503, single: true, wantCalls: 1, wantStatus: 503},
		{name: "cancelled", status: 503, cancel: true, wantCalls: 1, wantStatus: 503},
		{name: "already_written", status: 503, written: true, wantCalls: 1, wantStatus: 200},
		{name: "absolute_route_not_attributed", status: 503, absolute: true, wantCalls: 1, wantStatus: 503},
		{name: "manual_probe_uses_backup", status: 503, probe: true, wantCalls: 2, wantStatus: 200},
		{name: "scheduled_probe_uses_backup", status: 503, probe: true, scheduled: true, wantCalls: 2, wantStatus: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary, backup := "https://"+test.name+".primary.invalid", "https://"+test.name+".backup.invalid"
			settings := kitdto.ChannelSettings{EndpointMode: "failover", AdditionalBaseURLs: []string{backup}}
			channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "mirror-key\nmirror-key", Models: "deepseek-flash", Group: "default", Status: common.ChannelStatusEnabled, BaseURL: &primary, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}}
			if test.probe {
				channel.Key, channel.ChannelInfo = "mirror-key", model.ChannelInfo{}
			}
			if test.absolute {
				channel.Type = constant.ChannelTypeAdvancedCustom
				channel.SetOtherSettings(kitdto.ChannelOtherSettings{AdvancedCustom: &kitdto.AdvancedCustomConfig{Routes: []kitdto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: primary + "/v1/chat/completions", Converter: "none"}}}})
			}
			encoded, err := common.Marshal(settings)
			require.NoError(t, err)
			channel.Setting = common.GetPointer(string(encoded))
			require.NoError(t, channel.Insert())
			model.InitChannelCache()
			c, recorder := probeRoutingContext("deepseek-flash", "default")
			common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			if test.single {
				service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt})
			}
			var targets, keys []string
			client.Transport = channelProbeTestTransport(func(request *http.Request) (*http.Response, error) {
				targets = append(targets, request.URL.Scheme+"://"+request.URL.Host)
				keys = append(keys, request.Header.Get("Authorization"))
				if test.cancel {
					cancel()
				}
				if test.written {
					c.Writer.WriteHeaderNow()
				}
				status := http.StatusOK
				body := `{"id":"chat_fixture","model":"deepseek-flash","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
				if request.URL.Host == strings.TrimPrefix(primary, "https://") || test.allFailed {
					if test.network {
						return nil, errors.New("fixture connection failure")
					}
					status, body = test.status, `{"error":{"type":"server_error","message":"fixture upstream unavailable"}}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			if test.probe {
				probeCtx := context.Background()
				if test.scheduled {
					probeCtx = context.WithValue(probeCtx, channelIdleProbeIntervalKey{}, time.Minute)
				}
				result := testChannelModel(probeCtx, channel, user.Id, "deepseek-flash", "", false)
				require.NoError(t, result.localErr)
				require.Nil(t, result.newAPIError)
				require.NotNil(t, result.sample)
				assert.True(t, result.sample.Success, "only the final successful mirror outcome may be published")
				assert.Nil(t, channel.RelayCredentialProbeError("deepseek-flash", "mirror-key"), "the failed primary must not gate the healthy credential")
			} else {
				require.Nil(t, middleware.SetupContextForSelectedChannel(c, channel, "deepseek-flash"))
				Relay(c, types.RelayFormatOpenAI)
				assert.Equal(t, test.wantStatus, recorder.Code, recorder.Body.String())
			}
			require.Len(t, targets, test.wantCalls)
			assert.Equal(t, primary, targets[0])
			if test.wantCalls == 2 {
				assert.Equal(t, backup, targets[1])
				assert.Equal(t, keys[0], keys[1], "the same key may be used at a different mirror")
			}
			if test.wantStatus == 200 && !test.written {
				next, _ := probeRoutingContext("deepseek-flash", "default")
				selected, err := service.SelectChannelEndpoint(next, channel)
				require.NoError(t, err)
				assert.Equal(t, backup, selected, "new requests temporarily avoid the failed primary")
			}
		})
	}
}

func TestConcurrentKeySuccessRespectsProbe(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "fixture-key-success", Models: "flash"}
			require.NoError(t, db.Create(channel).Error)
			t.Cleanup(func() { model.InvalidateChannelProbeRouting(channel.Id) })
			const alias = "flash@effort:high"
			const newest = 1000
			results := make(chan error, newest+1)
			start := make(chan struct{})
			for call := 1; call <= newest; call++ {
				go func() {
					<-start
					code := 200
					if call == newest {
						code = 201
					}
					results <- model.RecordChannelKeyObservation(channel, alias, channel.Key, int64(call), common.GetPointer(true), code, "", "request")
				}()
			}
			go func() {
				<-start
				results <- model.RecordChannelKeyObservation(channel, "flash", channel.Key, newest, common.GetPointer(false), 503, "unavailable", "probe")
			}()
			close(start)
			var firstError error
			for range cap(results) {
				if err := <-results; err != nil && firstError == nil {
					firstError = err
				}
			}
			require.NoError(t, firstError)
			rows, err := model.GetChannelKeyObservations([]int{channel.Id}, alias)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.EqualValues(t, newest, rows[0].ObservedAt)
			assert.Equal(t, 201, rows[0].StatusCode)
			model.InvalidateChannelProbeRouting(channel.Id)
			require.NotNil(t, channel.RelayProbeError(alias, nil), "the probe wins the tie regardless of commit order")
			require.NoError(t, model.RecordChannelKeyObservation(channel, alias, channel.Key, newest+1, common.GetPointer(false), 502, "request failed", "request"))
			require.NotNil(t, channel.RelayProbeError(alias, nil))
			require.NoError(t, model.RecordChannelKeyObservation(channel, alias, channel.Key, newest+2, common.GetPointer(true), 200, "", "request"))
			require.Nil(t, channel.RelayProbeError(alias, nil), "later success immediately recovers the shared route")
		})
	}
}

func TestSQLiteKeyObservationPreservesConcurrentProbe(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	replica, err := gorm.Open(sqlite.Open(common.SQLitePath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := replica.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "fixture-key", Models: "flash"}
	require.NoError(t, db.Create(channel).Error)
	t.Cleanup(func() { model.InvalidateChannelProbeRouting(channel.Id) })
	id, err := model.ChannelKeyObservationID(channel, "flash", channel.Key)
	require.NoError(t, err)

	// An older replica commits its probe between the compatibility read and
	// the first write. SQLite must discard that read snapshot and see the probe.
	wroteProbe := false
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:concurrent_probe", func(tx *gorm.DB) {
		if _, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter); !inTransaction || wroteProbe {
			return
		}
		wroteProbe = true
		tx.AddError(replica.Create(&model.ChannelKeyObservation{
			ID: id, ChannelID: channel.Id, ModelName: "flash", ObservedAt: 10,
			Success: false, StatusCode: 503, ErrorText: "probe unavailable", Source: "probe",
		}).Error)
	}))
	require.NoError(t, model.RecordChannelKeyObservation(channel, "flash", channel.Key, 20, common.GetPointer(false), 502, "request failed", "request"))
	require.True(t, wroteProbe)
	rows, err := model.GetChannelKeyObservations([]int{channel.Id}, "flash")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 20, rows[0].ObservedAt)
	assert.Equal(t, "request", rows[0].Source)
	model.InvalidateChannelProbeRouting(channel.Id)
	blocked := channel.RelayCredentialProbeError("flash", channel.Key)
	require.NotNil(t, blocked)
	assert.EqualValues(t, 10, blocked.ObservedAt)
	assert.Equal(t, 503, blocked.StatusCode)
}

func TestSQLiteKeyObservationRetriesBusyCommit(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	require.NoError(t, db.Exec("PRAGMA busy_timeout=0").Error)
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "fixture-key", Models: "flash"}
	require.NoError(t, db.Create(channel).Error)
	t.Cleanup(func() { model.InvalidateChannelProbeRouting(channel.Id) })

	replica, err := gorm.Open(sqlite.Open(common.SQLitePath), &gorm.Config{})
	require.NoError(t, err)
	readerPool, err := replica.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, readerPool.Close()) })
	reader := replica.Begin()
	require.NoError(t, reader.Error)
	defer reader.Rollback()
	var rows int64
	require.NoError(t, reader.Model(&model.ChannelKeyObservation{}).Count(&rows).Error)

	// A rollback-journal reader can coexist with the write, but prevents its
	// commit. Release the reader once the observation retries on the same pool.
	writes := 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:release_reader", func(tx *gorm.DB) {
		writes++
		if writes == 2 {
			tx.AddError(reader.Rollback().Error)
		}
	}))
	require.NoError(t, model.RecordChannelKeyObservation(channel, "flash", channel.Key, 20, common.GetPointer(false), 502, "request failed", "request"))
	observations, err := model.GetChannelKeyObservations([]int{channel.Id}, "flash")
	require.NoError(t, err)
	require.Len(t, observations, 1)
	assert.EqualValues(t, 20, observations[0].ObservedAt)
	assert.Equal(t, 502, observations[0].StatusCode)
}

func TestChannelProbeRoutingDatabaseMatrix(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dialect.env != "" && dsn == "" {
				t.Skipf("%s not configured", dialect.env)
			}
			db := modelManagementDB(t, dialect.kind, dsn)
			require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}))
			groups, ratios := setting.UserUsableGroups2JSONString(), ratio_setting.GroupRatio2JSONString()
			previousMaxGroups := setting.GetMaxTokenAutoGroups()
			affinity := operation_setting.GetChannelAffinitySetting()
			previousAffinity, previousDisable := *affinity, common.AutomaticDisableChannelEnabled
			t.Cleanup(func() {
				require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(groups))
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(ratios))
				require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprint(previousMaxGroups)))
				*affinity, common.AutomaticDisableChannelEnabled = previousAffinity, previousDisable
			})
			require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1}`))
			require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))
			affinity.Enabled = false
			common.AutomaticDisableChannelEnabled = true
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("cache=%t", cached), func(t *testing.T) {
					common.MemoryCacheEnabled = cached
					name := fmt.Sprintf("probe-routing-%t", cached)
					otherModel := name + "-other"
					channels := []model.Channel{
						{Name: "private-high-channel", Key: "private-high-key", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: name + "," + otherModel, Priority: common.GetPointer(int64(20)), AutoBan: common.GetPointer(1)},
						{Name: "private-multi-channel", Key: "private-failed-key\nprivate-healthy-key", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: name + "," + otherModel, Priority: common.GetPointer(int64(10)), ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}},
						{Name: "private-vip-channel", Key: "private-vip-key", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "vip", Models: name, Priority: common.GetPointer(int64(1))},
					}
					for index := range channels {
						require.NoError(t, db.Create(&channels[index]).Error)
						require.NoError(t, channels[index].AddAbilities(db))
						t.Cleanup(func() { model.InvalidateChannelProbeRouting(channels[index].Id) })
					}
					high, multi, vip := &channels[0], &channels[1], &channels[2]
					model.InitChannelCache()
					// An old failure must not expire just because the probe interval elapsed.
					at := time.Now().Add(-24 * time.Hour).UnixMilli()
					require.NoError(t, model.RecordChannelKeyObservation(high, name, high.Key, at+100, common.GetPointer(false), 429, "rate limited: "+high.Key, "probe"))
					require.NoError(t, model.RecordChannelKeyObservation(multi, name, "private-failed-key", at+101, common.GetPointer(false), 502, "first key unavailable", "probe"))

					t.Run("candidate_and_key_selection", func(t *testing.T) {
						selected, err := model.GetRandomSatisfiedChannel("default", name, 0, nil)
						require.NoError(t, err)
						require.NotNil(t, selected)
						assert.Equal(t, multi.Id, selected.Id, "skip the failed higher-priority channel")
						for range 2 {
							key, index, apiErr := selected.GetNextRelayKeyForModel(name, false)
							require.Nil(t, apiErr)
							assert.Equal(t, "private-healthy-key", key)
							assert.Equal(t, 1, index)
						}
						c, _ := probeRoutingContext(name, "default")
						requireProbeRoutingError(t, middleware.SetupContextForSelectedChannel(c, high, name), "rate limited: [redacted]")
						assert.False(t, service.ShouldRetryRelayError(c, high.RelayProbeError(name, nil).NewAPIError(), 2))
						require.Nil(t, middleware.SetupContextForSelectedChannel(c, high, name, true), "manual testing must be able to recover the blocked key")
						assert.Equal(t, high.Key, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
						key, _, apiErr := high.GetNextRelayKeyForModel(name, true)
						require.Nil(t, apiErr, "an ordinary text probe does not describe the batch endpoint")
						assert.Equal(t, high.Key, key)
						assert.Nil(t, high.RelayProbeError(name, []dto.ChannelFilter{{Kind: dto.FilterBatchCapable}}))
						selected, err = model.GetRandomSatisfiedChannel("default", otherModel, 0, nil)
						require.NoError(t, err)
						require.NotNil(t, selected)
						assert.Equal(t, high.Id, selected.Id, "failure is scoped to the requested model")
						assert.NotNil(t, high.RelayProbeError(name+"@effort:high", nil), "modifiers must not bypass the base model's probe")
						exactModel := *high
						exactModel.Models += "," + name + "@effort:high"
						assert.Nil(t, exactModel.RelayProbeError(name+"@effort:high", nil), "an explicitly configured variant has its own probe identity")
						require.NoError(t, model.RecordChannelKeyObservation(multi, otherModel, "private-healthy-key", at+105, common.GetPointer(false), 500, "ordinary failure", "request"))
						assert.Nil(t, multi.RelayCredentialProbeError(otherModel, "private-healthy-key"), "request failures alone do not establish a probe gate")
						blockedCopy := *multi
						blockedCopy.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
						_, _, apiErr = blockedCopy.GetNextRelayKeyForModel(name, false)
						requireProbeRoutingError(t, apiErr, "first key unavailable")
					})

					t.Run("pins_and_affinity", func(t *testing.T) {
						for _, pin := range []dto.ChannelPin{
							{ChannelId: high.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken, RetryMode: dto.PinRetrySingleAttempt},
							{ChannelId: high.Id, Source: dto.PinSourceOriginTask, Rank: dto.PinRankOriginTask, RetryMode: dto.PinRetrySameChannel},
						} {
							c, _ := probeRoutingContext(name, "default")
							service.GetChannelConstraints(c).AddPin(pin)
							selected, _, err := service.SelectChannelForRequest(c, name, &service.RetryParam{Ctx: c, ModelName: name, TokenGroup: "default", Retry: common.GetPointer(0)})
							assert.Nil(t, selected)
							require.NotNil(t, err)
							assert.Equal(t, http.StatusServiceUnavailable, err.StatusCode)
							assert.Equal(t, model.ErrorCodeChannelProbeUnavailable, err.Code)
						}
						for _, mode := range []string{"prefer", "strict"} {
							snapshot, err := model.BuildRequestPolicy(map[string]string{
								"channel_affinity_setting.enabled": "true", "channel_affinity_setting.session_mode": mode,
								"channel_affinity_setting.keep_on_channel_disabled": "false",
								"channel_affinity_setting.rules":                    `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":"inherit"}]`,
							})
							require.NoError(t, err)
							*affinity = snapshot.Affinity
							seed, _ := probeRoutingContext(name, "default")
							seed.Request.Header.Set("X-Session", t.Name()+mode)
							_, found := service.GetPreferredChannelByAffinity(seed, name, "default")
							require.False(t, found)
							seed.Set("channel_id", high.Id)
							service.RecordChannelAffinity(seed, high.Id)
							c, recorder := probeRoutingContext(name, "default")
							c.Request.Header.Set("X-Session", t.Name()+mode)
							middleware.Distribute()(c)
							if mode == "strict" {
								assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
								assert.Contains(t, recorder.Body.String(), string(model.ErrorCodeChannelProbeUnavailable))
								assert.Contains(t, recorder.Body.String(), "rate limited: [redacted]")
							} else {
								assert.False(t, c.IsAborted())
								assert.Equal(t, multi.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
							}
							service.ClearCurrentChannelAffinityCache(seed)
						}
						*affinity = previousAffinity
						affinity.Enabled = false
					})

					t.Run("all_blocked_boundaries_and_auto_groups", func(t *testing.T) {
						require.NoError(t, model.RecordChannelKeyObservation(multi, name, "private-healthy-key", at+102, common.GetPointer(false), 503, "latest probe unavailable", "probe"))
						var relays atomic.Int32
						engine := gin.New()
						engine.POST("/v1/chat/completions", func(c *gin.Context) {
							common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
							common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
						}, middleware.Distribute(), func(c *gin.Context) {
							relays.Add(1)
							finish := perfmetrics.BeginChannelRequest(c.Request.Context(), &relaycommon.RelayInfo{OriginModelName: name}, c.GetInt("channel_id"))
							finish(common.GetPointer(true))
							c.Status(http.StatusOK)
						})
						c, recorder := probeRoutingContext(name, "default")
						engine.ServeHTTP(recorder, c.Request)
						assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
						var response struct {
							Error types.OpenAIError `json:"error"`
						}
						require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
						assert.Equal(t, string(model.ErrorCodeChannelProbeUnavailable), response.Error.Code)
						assert.Contains(t, response.Error.Message, "latest probe unavailable")
						assert.Zero(t, relays.Load(), "a local rejection must not enter the relay or record activity")
						for _, retry := range []bool{false, true} {
							c, _ := probeRoutingContext(name, "default")
							info := &relaycommon.RelayInfo{OriginModelName: name, UsingGroup: "default"}
							message := "rate limited: [redacted]"
							if retry {
								info.ChannelMeta = &relaycommon.ChannelMeta{}
								message = "latest probe unavailable"
							} else {
								common.SetContextKey(c, constant.ContextKeyChannelId, high.Id)
								common.SetContextKey(c, constant.ContextKeyChannelKey, high.Key)
							}
							selected, apiErr := getChannel(c, info, &service.RetryParam{Ctx: c, ModelName: name, TokenGroup: "default", Retry: common.GetPointer(0)})
							assert.Nil(t, selected)
							requireProbeRoutingError(t, apiErr, message)
						}
						activities, err := model.GetChannelModelActivities([]int{high.Id, multi.Id})
						require.NoError(t, err)
						assert.Empty(t, activities)
						c, _ = probeRoutingContext(name, "auto")
						selected, group, selectErr := service.SelectChannelForRequest(c, name, &service.RetryParam{Ctx: c, ModelName: name, TokenGroup: "auto", Retry: common.GetPointer(0)})
						require.Nil(t, selectErr)
						require.NotNil(t, selected)
						assert.Equal(t, vip.Id, selected.Id)
						assert.Equal(t, "vip", group)
						require.NoError(t, model.RecordChannelKeyObservation(vip, name, vip.Key, at+103, common.GetPointer(false), 401, "vip unavailable", "probe"))
						c, _ = probeRoutingContext(name, "auto")
						selected, _, selectErr = service.SelectChannelForRequest(c, name, &service.RetryParam{Ctx: c, ModelName: name, TokenGroup: "auto", Retry: common.GetPointer(0)})
						assert.Nil(t, selected)
						require.NotNil(t, selectErr)
						assert.Equal(t, http.StatusServiceUnavailable, selectErr.StatusCode)
						assert.Equal(t, model.ErrorCodeChannelProbeUnavailable, selectErr.Code)
						assert.Contains(t, selectErr.Message, "vip unavailable")
					})

					t.Run("recovery_ordering_and_identity", func(t *testing.T) {
						for _, event := range []struct {
							at      int64
							source  string
							success *bool
							blocked bool
						}{
							{120, "request", common.GetPointer(false), true},
							{110, "request", common.GetPointer(true), false},
							{130, "probe", common.GetPointer(false), true},
							{125, "request", common.GetPointer(true), true},
							{135, "request", nil, true},
							{140, "probe", common.GetPointer(true), false},
							{150, "request", common.GetPointer(false), false},
							{135, "probe", common.GetPointer(false), false},
							{160, "probe", common.GetPointer(false), true},
							{160, "request", common.GetPointer(true), true},
							{161, "request", common.GetPointer(true), false},
							{162, "probe", common.GetPointer(false), true},
						} {
							require.NoError(t, model.RecordChannelKeyObservation(high, name, high.Key, at+event.at, event.success, 503, "recovery sequence failure", event.source))
							assert.Equal(t, event.blocked, high.RelayProbeError(name, nil) != nil, "event %s at %d", event.source, event.at)
						}
						observations, err := model.GetChannelKeyObservations([]int{high.Id}, name)
						require.NoError(t, err)
						require.Len(t, observations, 1, "private gate rows must not appear as extra monitor keys")
						assert.Equal(t, at+162, observations[0].ObservedAt)
						assert.Equal(t, "probe", observations[0].Source)
						replacement := *high
						replacement.Key, replacement.Keys = "replacement-key", nil
						assert.Nil(t, replacement.RelayProbeError(name, nil), "new credentials must not inherit an old failure")
						remapped := *high
						remapped.ModelMapping = common.GetPointer(fmt.Sprintf(`{%q:"different-upstream"}`, name))
						assert.Nil(t, remapped.RelayProbeError(name, nil))
						remapped.ModelMapping = common.GetPointer(fmt.Sprintf(`{%q:"different-upstream"}`, otherModel))
						assert.NotNil(t, remapped.RelayProbeError(name, nil), "editing an unrelated mapping must not recover this model")
						reordered := *multi
						reordered.Key, reordered.Keys = "private-healthy-key\nprivate-failed-key", nil
						require.NotNil(t, reordered.RelayCredentialProbeError(name, reordered.GetKeys()[1]))
						assert.Contains(t, reordered.RelayCredentialProbeError(name, reordered.GetKeys()[1]).Message, "first key unavailable")
						// Upgrade an existing monitor row that predates independent probe state.
						legacyID, err := model.ChannelKeyObservationID(high, otherModel, high.Key)
						require.NoError(t, err)
						require.NoError(t, db.Create(&model.ChannelKeyObservation{ID: legacyID, ChannelID: high.Id, ModelName: otherModel, ObservedAt: at + 100, Success: false, StatusCode: 429, ErrorText: "pre-upgrade probe failure", Source: "probe"}).Error)
						model.InvalidateChannelProbeRouting(high.Id)
						require.NotNil(t, high.RelayProbeError(otherModel, nil))
						require.NoError(t, model.RecordChannelKeyObservation(high, otherModel, high.Key, at+120, common.GetPointer(false), 502, "later request failure", "request"))
						model.InvalidateChannelProbeRouting(high.Id)
						require.NotNil(t, high.RelayProbeError(otherModel, nil))
						assert.Equal(t, "pre-upgrade probe failure", high.RelayProbeError(otherModel, nil).Message)
						var persisted model.Channel
						require.NoError(t, db.First(&persisted, high.Id).Error)
						assert.Equal(t, common.ChannelStatusEnabled, persisted.Status, "probe gating must not disable the channel")
					})
				})
			}
		})
	}
}

func TestResponsesWebSocketProbeGateUsesLockedCredential(t *testing.T) {
	var requests atomic.Int32
	fixture := newResponsesWSBillingTest(t, `c * 2`, func(ws *websocket.Conn, _ *http.Request) {
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
			requests.Add(1)
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"before-probe","usage":{"input_tokens":1,"output_tokens":1}}}`))) {
				return
			}
		}
	})
	require.NoError(t, model.RecordChannelKeyObservation(fixture.channel, "ws-other", "upstream-first", time.Now().UnixMilli(), common.GetPointer(false), 429, "other model unavailable", "probe"))
	first := []byte(`{"type":"response.create","model":"ws-billing","input":"hi","max_output_tokens":1}`)
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, first))
	require.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
	before, err := model.GetChannelModelActivity(fixture.channel.Id, "ws-billing")
	require.NoError(t, err)
	require.NoError(t, model.RecordChannelKeyObservation(fixture.channel, "ws-billing", "upstream-first", time.Now().UnixMilli(), common.GetPointer(false), 429, "model temporarily unavailable", "probe"))
	assert.Nil(t, fixture.channel.RelayProbeError("ws-billing", nil), "another key remains eligible for a new connection")
	require.NotNil(t, fixture.channel.RelayCredentialProbeError("ws-billing", "upstream-first"))
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, first))
	rejection := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", rejection["type"])
	assert.Equal(t, float64(http.StatusServiceUnavailable), rejection["status"])
	encoded, err := common.Marshal(rejection)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), string(model.ErrorCodeChannelProbeUnavailable))
	assert.Contains(t, string(encoded), "model temporarily unavailable")
	fixture.closeAndWait(t)
	assert.EqualValues(t, 1, requests.Load(), "a locked connection must not send the blocked model to its credential")
	assertResponsesWSAccounting(t, fixture, []int{1})
	activity, err := model.GetChannelModelActivity(fixture.channel.Id, "ws-billing")
	require.NoError(t, err)
	assert.Equal(t, before, activity, "local rejection must not postpone automatic probing")
	observations, err := model.GetChannelKeyObservations([]int{fixture.channel.Id}, "ws-billing")
	require.NoError(t, err)
	require.Len(t, observations, 1)
	assert.Equal(t, "probe", observations[0].Source, "local rejection must not create a request failure sample")
}
