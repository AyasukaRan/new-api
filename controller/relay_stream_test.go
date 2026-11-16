package controller

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayMultiKeyFailoverResponseHeaderTimeout(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			testRelayMultiKeyFailoverResponseHeaderTimeout(t, dialect.kind, os.Getenv(dialect.env))
		})
	}
}

func testRelayMultiKeyFailoverResponseHeaderTimeout(t *testing.T, kind, dsn string) {
	require.NoError(t, i18n.Init())
	db := modelManagementDB(t, kind, dsn)
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelBalanceSample{}, &model.Log{}))
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": previousConfig["perf_metrics_setting.enabled"], "quota_setting.enable_free_model_pre_consume": previousConfig["quota_setting.enable_free_model_pre_consume"]}))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false", "quota_setting.enable_free_model_pre_consume": "false"}))
	previousRetries, previousDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
	previousHeader, previousFailover, previousTimeout := common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout
	previousStreamingTimeout := constant.StreamingTimeout
	previousCount, previousLogs, previousTrace, previousErrors := constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled
	previousGroups := ratio_setting.GroupRatio2JSONString()
	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	general := operation_setting.GetGeneralSetting()
	previousPing := general.PingIntervalEnabled
	affinity.Enabled, general.PingIntervalEnabled = false, false
	common.AutomaticDisableChannelEnabled = false
	common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout = 0, 1, 0
	constant.StreamingTimeout = 30
	constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = false, false, false, true
	t.Cleanup(func() {
		common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable
		common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout = previousHeader, previousFailover, previousTimeout
		constant.StreamingTimeout = previousStreamingTimeout
		constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = previousCount, previousLogs, previousTrace, previousErrors
		*affinity, general.PingIntervalEnabled = previousAffinity, previousPing
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		service.ResetProxyClientCache()
		service.InitHttpClient()
	})
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"claude-header-fixture":0}`))
	service.ResetProxyClientCache()
	service.InitHttpClient()
	user := &model.User{Username: "header-failover-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Quota: 1000, AffCode: "headerfailover"}
	require.NoError(t, db.Create(user).Error)

	for _, test := range []struct {
		name                              string
		retries                           int
		cancel, written                   bool
		once, ping, thinking, streamError bool
		change                            string
		wantKeys                          []string
		wantStatus                        int
		responseStatus                    int
	}{
		{name: "global_header_timeout_does_not_retry_same_key", retries: 3, wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "timeout_uses_next_ordered_key", retries: 3, wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "upstream_unavailable_uses_next_ordered_key", retries: 3, responseStatus: 503, wantKeys: []string{"header-primary", "header-primary", "header-backup"}, wantStatus: 200},
		{name: "temporary_failure_retries_same_key_once", retries: 3, responseStatus: 503, once: true, wantKeys: []string{"header-primary", "header-primary"}, wantStatus: 200},
		{name: "ping_only_keeps_same_key_replayable", retries: 3, responseStatus: 503, once: true, ping: true, wantKeys: []string{"header-primary", "header-primary"}, wantStatus: 200},
		{name: "error_only_frame_is_not_first_token", retries: 3, streamError: true, once: true, wantKeys: []string{"header-primary", "header-primary"}, wantStatus: 200},
		{name: "same_key_retry_consumes_global_budget", retries: 1, responseStatus: 503, wantKeys: []string{"header-primary", "header-primary"}, wantStatus: 503},
		{name: "invalid_credential_skips_same_key_retry", retries: 3, responseStatus: 401, wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "probe_failure_before_retry_is_skipped", retries: 3, responseStatus: 503, change: "probe", wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "key_disabled_before_retry_is_skipped", retries: 3, responseStatus: 503, change: "disable", wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "key_group_revoked_before_retry_is_skipped", retries: 3, responseStatus: 503, change: "group", wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "key_removed_before_retry_is_skipped", retries: 3, responseStatus: 503, change: "remove", wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "thinking_failure_preserves_first_token_and_reason", retries: 3, written: true, thinking: true, wantKeys: []string{"header-primary"}, wantStatus: 200},
		{name: "client_validation_rejection_not_quarantined", retries: 3, responseStatus: 400, wantKeys: []string{"header-primary"}, wantStatus: 400},
		{name: "timeout_respects_retry_budget", retries: 0, wantKeys: []string{"header-primary"}, wantStatus: 500},
		{name: "client_cancel_stops_upstream_and_retry", retries: 3, cancel: true, wantKeys: []string{"header-primary"}, wantStatus: 500},
		{name: "business_output_is_never_replayed", retries: 3, written: true, wantKeys: []string{"header-primary"}, wantStatus: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			common.RetryTimes = test.retries
			if test.name == "global_header_timeout_does_not_retry_same_key" {
				common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout = 1, 120
				service.ResetProxyClientCache()
				service.InitHttpClient()
				defer func() {
					common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout = 0, 1
					service.ResetProxyClientCache()
					service.InitHttpClient()
				}()
			}
			affinity.Enabled = test.name == "timeout_uses_next_ordered_key" || test.once
			affinity.Rules = []operation_setting.ChannelAffinityRule{{
				Name: "header-failover-affinity", ModelRegex: []string{"^claude-header-fixture$"},
				SessionMode: "prefer", TTLSeconds: 3600, IncludeModelName: true, IncludeUsingGroup: true, IncludeRuleName: true,
				KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Fixture-Session"}},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var mutex sync.Mutex
			var used []string
			var ch *model.Channel
			upstreamCanceled := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				key := r.Header.Get("X-Api-Key")
				mutex.Lock()
				used = append(used, key)
				attempt := len(used)
				mutex.Unlock()
				if key == "header-primary" && attempt == 1 && test.change != "" {
					changed, loadErr := model.GetChannelById(ch.Id, true)
					if loadErr != nil {
						t.Error(loadErr)
						w.WriteHeader(500)
						return
					}
					switch test.change {
					case "probe":
						if healthErr := model.RecordChannelKeyRoutingFailure(changed, ch.Models, key, time.Now().UnixMilli(), 503, "concurrent probe failure"); healthErr != nil {
							t.Error(healthErr)
						}
					case "disable":
						changed.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
					case "group":
						changed.ChannelInfo.KeyGroups = map[string][]string{changed.KeyIdentity(key): {"other"}}
					case "remove":
						changed.Key = "header-backup\nheader-last"
						changed.ChannelInfo.MultiKeySize = 2
					}
					if updateErr := db.Model(changed).Updates(map[string]any{"key": changed.Key, "channel_info": changed.ChannelInfo}).Error; updateErr != nil {
						t.Error(updateErr)
					}
				}
				if key == "header-primary" && test.streamError && attempt == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"temporary fixture failure\"}}\n\n")
					return
				}
				if key == "header-primary" && test.responseStatus > 0 && (!test.once || attempt == 1) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.responseStatus)
					errorType := "api_error"
					if test.responseStatus == http.StatusBadRequest {
						errorType = "invalid_request_error"
					}
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"`+errorType+`","message":"fixture rejection"}}`)
					return
				}
				if key == "header-primary" && !test.written && test.responseStatus == 0 && !test.streamError {
					if test.cancel {
						cancel()
					}
					select {
					case <-r.Context().Done():
						upstreamCanceled <- struct{}{}
					case <-ctx.Done():
						// Bound a failed regression without leaving a hung server.
						select {
						case <-r.Context().Done():
							upstreamCanceled <- struct{}{}
						case <-time.After(time.Second):
							w.WriteHeader(http.StatusGatewayTimeout)
						}
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set(common.RequestIdKey, "private-upstream-id")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-header-fixture\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
				if test.thinking || test.once {
					_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"checking fixture\"}}\n\n")
				}
				if test.written {
					w.Header().Set("X-Request-Id", "private-upstream-id")
					_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"fixture failed after output header-primary private-upstream-id\"}}\n\n")
					return
				}
				_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer upstream.Close()
			ch = &model.Channel{Name: test.name, Type: constant.ChannelTypeAnthropic, Key: "header-backup\nheader-primary\nheader-last", Models: "claude-header-fixture", Group: "default", Status: common.ChannelStatusEnabled, BaseURL: common.GetPointer(upstream.URL), ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModeFailover}}
			require.NoError(t, ch.Insert())
			t.Cleanup(func() { require.NoError(t, ch.Delete()) })
			ch.ChannelInfo.KeyOrder = []string{ch.KeyIdentity("header-primary"), ch.KeyIdentity("header-backup"), ch.KeyIdentity("header-last")}
			require.NoError(t, db.Model(ch).Update("channel_info", ch.ChannelInfo).Error)
			c, recorder := probeRoutingContext(ch.Models, "default")
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-header-fixture","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("X-Fixture-Session", "session-before-failure")
			common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			selected, _, selectErr := service.SelectChannelForRequest(c, ch.Models, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: ch.Models})
			require.Nil(t, selectErr)
			require.NotNil(t, selected)
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, selected, ch.Models))
			common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now().Add(-500*time.Millisecond))
			c.Set(common.RequestIdKey, "local-fixture-request")
			c.Set(common.UpstreamRequestIdKey, "private-upstream-id")
			if test.ping {
				helper.SetEventStreamHeaders(c)
				require.NoError(t, helper.PingData(c))
			}
			Relay(c, types.RelayFormatClaude)
			var logs []model.Log
			require.NoError(t, db.Where("channel_id = ? AND type = ?", ch.Id, model.LogTypeError).Order("id").Find(&logs).Error)
			require.NotEmpty(t, logs)
			var firstOther map[string]any
			require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &firstOther))
			if test.thinking {
				assert.GreaterOrEqual(t, firstOther["frt"].(float64), float64(500))
				assert.Equal(t, map[string]any{"action": "stop", "reason": "response_already_started"}, firstOther["retry_decision"])
				assert.Contains(t, logs[0].Other, "fixture failed after output")
				assert.NotContains(t, logs[0].Content, "header-primary")
				assert.NotContains(t, logs[0].Content, "private-upstream-id")
				streamJSON, marshalErr := common.Marshal(firstOther["stream_status"])
				require.NoError(t, marshalErr)
				assert.NotContains(t, string(streamJSON), "header-primary")
				assert.NotContains(t, string(streamJSON), "private-upstream-id")
				assert.Contains(t, string(streamJSON), "local-fixture-request")
			} else {
				assert.NotContains(t, firstOther, "frt")
			}
			if test.once {
				assert.Equal(t, map[string]any{"action": "retry_same_key", "reason": "same_key_transient_retry"}, firstOther["retry_decision"])
			}
			mutex.Lock()
			assert.Equal(t, test.wantKeys, used)
			mutex.Unlock()
			assert.Equal(t, test.wantStatus, recorder.Code, recorder.Body.String())
			if test.cancel {
				assert.ErrorIs(t, ctx.Err(), context.Canceled)
			} else {
				assert.NoError(t, ctx.Err(), "a transport header timeout must not cancel the inbound request")
			}
			if !test.written && test.responseStatus == 0 && !test.streamError {
				select {
				case <-upstreamCanceled:
				case <-time.After(time.Second):
					t.Fatal("the failed upstream HTTP request was not canceled")
				}
			}
			if len(test.wantKeys) > 1 && test.wantStatus == 200 {
				assert.Contains(t, recorder.Body.String(), "event: message_stop")
				assert.NotContains(t, recorder.Body.String(), "event: error")
			} else if test.written {
				assert.Contains(t, recorder.Body.String(), "event: message_start")
				assert.Contains(t, recorder.Body.String(), "event: error")
			}
			nextKey := func(session string) string {
				next, _ := probeRoutingContext(ch.Models, "default")
				next.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				next.Request.Header.Set("X-Fixture-Session", session)
				common.SetContextKey(next, constant.ContextKeyUserId, user.Id)
				common.SetContextKey(next, constant.ContextKeyTokenGroup, "default")
				selected, _, selectionErr := service.SelectChannelForRequest(next, ch.Models, &service.RetryParam{Ctx: next, TokenGroup: "default", ModelName: ch.Models})
				require.Nil(t, selectionErr)
				require.NotNil(t, selected)
				require.Nil(t, middleware.SetupContextForSelectedChannel(next, selected, ch.Models))
				return common.GetContextKeyString(next, constant.ContextKeyChannelKey)
			}
			if test.change != "" {
				return
			}
			if test.once {
				assert.Nil(t, ch.RelayCredentialProbeError(ch.Models, "header-primary"))
				assert.Equal(t, "header-primary", nextKey("fresh-session"))
				return
			}
			if test.cancel || test.responseStatus == http.StatusBadRequest {
				assert.Nil(t, ch.RelayCredentialProbeError(ch.Models, "header-primary"), "a client cancellation must not quarantine a healthy credential")
				assert.Equal(t, "header-primary", nextKey("fresh-session"))
				return
			}
			failure := ch.RelayCredentialProbeError(ch.Models, "header-primary")
			require.NotNil(t, failure, "failure must be remembered even without remaining retries")
			assert.Equal(t, "header-backup", nextKey("fresh-session"), "later requests must skip the failed primary immediately")
			require.NoError(t, model.RecordChannelKeyObservation(ch, ch.Models, "header-primary", failure.ObservedAt+1, common.GetPointer(true), http.StatusOK, "", "probe"))
			assert.Nil(t, ch.RelayCredentialProbeError(ch.Models, "header-primary"))
			assert.Equal(t, "header-primary", nextKey("session-after-recovery"), "new sessions prefer the recovered primary")
			if affinity.Enabled {
				assert.Equal(t, "header-backup", nextKey("session-before-failure"), "recovery must not move the successful fallback session back to the primary")
			}
		})
	}
}

func TestFailoverRecoveryDoesNotProbeNonConversationRequests(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	for _, test := range []struct {
		path, model string
		format      types.RelayFormat
	}{
		{"/v1/images/generations", "gpt-image-1", types.RelayFormatOpenAI},
		{"/v1/audio/speech", "tts-1", types.RelayFormatOpenAI},
		{"/v1/embeddings", "text-embedding-3-small", types.RelayFormatOpenAI},
		{"/v1/tools/search", "deepseek-search", types.RelayFormatOpenAI},
		{"/v1beta/models/gemini-image:generateContent", "gemini-image", types.RelayFormatGemini},
	} {
		t.Run(test.path, func(t *testing.T) {
			ch := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: test.model, Models: test.model, Group: "default", Key: "scope-primary\nscope-backup", Status: common.ChannelStatusEnabled,
				ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModeFailover}}
			require.NoError(t, db.Create(ch).Error)
			c, _ := probeRoutingContext(test.model, "default")
			c.Request = httptest.NewRequest(http.MethodPost, test.path, nil)
			service.MarkGroupKeyAttempt(c, ch, "scope-primary")
			info := &relaycommon.RelayInfo{OriginModelName: test.model, RelayFormat: test.format, RelayMode: relayconstant.Path2RelayMode(test.path), ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "scope-primary"}}
			apiErr := types.NewOpenAIError(errors.New("upstream unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
			recordChannelKeyFailoverFailure(c, ch, info, apiErr, common.GetPointer(false))
			assert.Nil(t, ch.RelayCredentialProbeError(test.model, "scope-primary"), "an unprobeable endpoint must not create a permanent recovery gate")
		})
	}
}

func TestMultiKeyFailoverHeaderTimeoutScope(t *testing.T) {
	previousHeader, previousFailover, previousTimeout := common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout
	common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout = 0, 1, 0
	service.ResetProxyClientCache()
	service.InitHttpClient()
	t.Cleanup(func() {
		common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout = previousHeader, previousFailover, previousTimeout
		service.ResetProxyClientCache()
		service.InitHttpClient()
	})
	for _, test := range []struct {
		name    string
		mode    constant.MultiKeyMode
		multi   bool
		keyTest bool
	}{
		{name: "polling", mode: constant.MultiKeyModePolling, multi: true},
		{name: "random", mode: constant.MultiKeyModeRandom, multi: true},
		{name: "single_key", mode: constant.MultiKeyModeFailover},
		{name: "specified_key_test", mode: constant.MultiKeyModeFailover, multi: true, keyTest: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			arrived, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path == "/bounded" {
					select {
					case <-r.Context().Done():
					case <-ctx.Done():
					}
					close(release)
					return
				}
				close(arrived)
				select {
				case <-release:
					_, _ = io.WriteString(w, "scope preserved")
				case <-ctx.Done():
				}
			}))
			defer upstream.Close()
			policy := service.NormalizeHTTPTransportPolicy(dto.ChannelSettings{})
			policy.ResponseHeaderTimeoutSeconds = 1
			bounded, err := service.GetHttpClientWithProxyPolicy("", policy)
			require.NoError(t, err)
			boundedResult := make(chan error, 1)
			go func() {
				select {
				case <-arrived:
				case <-ctx.Done():
					boundedResult <- ctx.Err()
					return
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/bounded", nil)
				response, requestErr := bounded.Do(request)
				if response != nil {
					response.Body.Close()
				}
				boundedResult <- requestErr
			}()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil).WithContext(ctx)
			selected := &model.Channel{Id: 1, Key: "scope-key\nother-key", ChannelInfo: model.ChannelInfo{IsMultiKey: test.multi, MultiKeyMode: test.mode}}
			service.MarkGroupKeyAttempt(c, selected, "scope-key")
			info := &relaycommon.RelayInfo{IsChannelTest: test.keyTest, ChannelMeta: &relaycommon.ChannelMeta{ChannelIsMultiKey: test.multi}}
			request, err := http.NewRequest(http.MethodPost, upstream.URL+"/scope", strings.NewReader("{}"))
			require.NoError(t, err)
			response, err := channel.DoRequest(c, request, info)
			require.NoError(t, err, "ordinary relays and specified-key tests must outlive the failover-only deadline")
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, "scope preserved", string(body))
			require.Error(t, <-boundedResult, "a real header timeout provides the timing boundary")
			assert.NoError(t, ctx.Err())
		})
	}
}

func TestRelayKeepAliveDuringImagePreparation(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	previousCount, previousMedia := constant.CountToken, constant.GetMediaToken
	fetchSettings := system_setting.GetFetchSetting()
	previousFetchSettings := *fetchSettings
	fetchSettings.AllowPrivateIp = true
	fetchSettings.AllowedPorts = []string{"1-65535"}
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
	constant.CountToken, constant.GetMediaToken = true, true
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
		constant.CountToken, constant.GetMediaToken = previousCount, previousMedia
		*fetchSettings = previousFetchSettings
	})
	service.InitHttpClient()

	imageRequested := make(chan struct{})
	releaseImage := make(chan struct{})
	var releaseOnce sync.Once
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(imageRequested)
		<-releaseImage
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer imageServer.Close()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "claude-sonnet-4-20250514")
		Relay(c, types.RelayFormatClaude)
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	defer releaseOnce.Do(func() { close(releaseImage) })

	body := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"` + imageServer.URL + `"}}]}]}`
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader(body))
	require.NoError(t, err, "the gateway must send keepalive while its image download is blocked")
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	reader := bufio.NewReader(response.Body)
	for range 2 {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, ": PING\n", line)
		line, readErr = reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, "\n", line)
	}
	select {
	case <-imageRequested:
	default:
		t.Fatal("the request never reached image preparation")
	}
	releaseOnce.Do(func() { close(releaseImage) })
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(remaining), "event: error\n")
	require.Contains(t, string(remaining), `"type":"error"`)
	require.Contains(t, string(remaining), "data: ")
}

func TestRelayKeepAliveAcrossUpstreamRetries(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 1
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})
	service.InitHttpClient()
	var attempts atomic.Int32
	retryStarted, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		close(retryStarted)
		select {
		case <-release:
			_, _ = io.WriteString(w, "data: ok\n\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		stop := helper.StartStreamKeepAlive(c, 10*time.Millisecond)
		defer stop()
		if !assert.NoError(t, http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(5*time.Second))) {
			return
		}
		info := &relaycommon.RelayInfo{IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
		for range 2 {
			request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
			if !assert.NoError(t, err) {
				return
			}
			response, err := channel.DoRequest(c, request, info)
			if !assert.NoError(t, err) {
				return
			}
			if response.StatusCode == http.StatusTooManyRequests {
				response.Body.Close()
				continue
			}
			_, _ = io.Copy(c.Writer, response.Body)
			response.Body.Close()
		}
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	defer releaseOnce.Do(func() { close(release) })
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer response.Body.Close()
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("the upstream retry did not start")
	}
	reader := bufio.NewReader(response.Body)
	for range 2 {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, ": PING\n", line)
		line, readErr = reader.ReadString('\n')
		require.NoError(t, readErr)
		require.Equal(t, "\n", line)
	}
	releaseOnce.Do(func() { close(release) })
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(body), "data: ok\n\n")
	require.Equal(t, int32(2), attempts.Load())
}

func TestRelayKeepAlivePreservesValidationErrors(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled := settings.PingIntervalEnabled
	settings.PingIntervalEnabled = true
	t.Cleanup(func() { settings.PingIntervalEnabled = previousEnabled })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-20250514","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	Relay(c, types.RelayFormatClaude)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, recorder.Body.String(), ": PING")
}

func TestStreamKeepAlivePreservesFastHTTPFailures(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 60
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})
	service.InitHttpClient()
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"request rejected"}`)
			}))
			defer upstream.Close()
			engine := gin.New()
			engine.POST("/v1/messages", func(c *gin.Context) {
				stop := helper.StartStreamKeepAlive(c, time.Minute)
				defer stop()
				request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
				if !assert.NoError(t, err) {
					return
				}
				info := &relaycommon.RelayInfo{IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{}}
				response, err := channel.DoRequest(c, request, info)
				if !assert.NoError(t, err) {
					return
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if assert.NoError(t, err) {
					c.Header("Content-Type", "application/json")
					c.Data(response.StatusCode, "application/json", body)
				}
			})
			gateway := httptest.NewServer(engine)
			defer gateway.Close()
			client := &http.Client{Timeout: 4 * time.Second}
			response, err := client.Post(gateway.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, status, response.StatusCode)
			require.Equal(t, "application/json", response.Header.Get("Content-Type"))
			require.JSONEq(t, `{"error":"request rejected"}`, string(body))
		})
	}
}

func TestRelayKeepAliveHonorsStreamSettings(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled := settings.PingIntervalEnabled
	previousCount, previousMedia, previousNonStreamMedia := constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream
	constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream = true, true, true
	t.Cleanup(func() {
		settings.PingIntervalEnabled = previousEnabled
		constant.CountToken, constant.GetMediaToken, constant.GetMediaTokenNotStream = previousCount, previousMedia, previousNonStreamMedia
	})
	for _, test := range []struct {
		name    string
		enabled bool
		stream  bool
	}{
		{name: "ping disabled", enabled: false, stream: true},
		{name: "non-streaming", enabled: true, stream: false},
		{name: "fast streaming failure", enabled: true, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings.PingIntervalEnabled = test.enabled
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := `{"model":"claude-sonnet-4-20250514","max_tokens":16,"stream":` + strconv.FormatBool(test.stream) + `,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!"}}]}]}`
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "claude-sonnet-4-20250514")
			Relay(c, types.RelayFormatClaude)
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
			require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
			require.NotContains(t, recorder.Body.String(), ": PING")
			var response map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, "error", response["type"])
		})
	}
}

func TestRelayStreamErrorAfterKeepAlive(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(format), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
			helper.SetEventStreamHeaders(c)
			require.NoError(t, helper.PingData(c))
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://provider.example")
			common.SetContextKey(c, constant.ContextKeyUpstreamRequestOrigin, "https://provider.example")
			apiError := types.NewErrorWithStatusCode(errors.New("upstream rejected input at https://provider.example/v1/messages"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
			writeRelayStreamError(c, format, apiError)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
			require.Contains(t, recorder.Body.String(), "upstream rejected input")
			require.NotContains(t, recorder.Body.String(), "provider.example")
			status, ok := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
			require.True(t, ok)
			require.True(t, status.ResponseFailed(), "stream errors must not consume the successful-request rate limit")
			require.Equal(t, http.StatusBadRequest, status.OutcomeSnapshot().ErrorStatus)
			if format == types.RelayFormatClaude {
				require.Contains(t, recorder.Body.String(), "event: error\n")
			} else {
				require.Contains(t, recorder.Body.String(), "data: [DONE]")
			}
		})
	}
}

func TestStreamKeepAliveCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil).WithContext(requestContext)
	stop := helper.StartStreamKeepAlive(c, time.Hour)
	cancel()
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not stop after client cancellation")
	}
	_, err := c.Writer.WriteString("data: done\n\n")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(recorder.Body.String(), "data: done\n\n"))
}

// These are real scanner callbacks, including native Baidu/Dify bodies and
// OpenAI's one-frame buffering pattern. Metadata/errors must stay unknown.
func TestRelayMeaningfulFirstResponse(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	for _, tc := range []struct {
		name, data string
		want       bool
	}{
		{"claude_metadata", `{"type":"message_start","message":{"role":"assistant"}}`, false},
		{"claude_error", `{"type":"error","error":{"message":"unavailable"}}`, false},
		{"claude_thinking", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"work"}}`, true},
		{"chat_role", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"chat_text_buffered", `{"choices":[{"delta":{"content":"hello"}}]}`, true},
		{"responses_metadata", `{"type":"response.created","response":{"status":"in_progress"}}`, false},
		{"responses_text", `{"type":"response.output_text.delta","delta":"hello"}`, true},
		{"baidu_text", `{"result":"hello","is_end":false}`, true},
		{"baidu_error", `{"error_code":3,"error_msg":"unavailable"}`, false},
		{"dify_message", `{"event":"message","answer":"hello"}`, true},
		{"gemini_text", `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{StartTime: time.Now().Add(-500 * time.Millisecond), IsStream: true}
			info.ResetFirstResponseTime()
			info.SetFirstResponseTime()
			info.ResetFirstResponseTime() // a previous attempt must not supply this attempt's FRT
			helper.SetEventStreamHeaders(c)
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + tc.data + "\n\ndata: [DONE]\n\n"))}
			helper.StreamScannerHandler(c, response, info, func(data string, sr *helper.StreamResult) {
				if tc.name != "chat_text_buffered" {
					require.NoError(t, helper.StringData(c, data))
				}
			})
			assert.Equal(t, tc.want, info.HasSendResponse())
			if tc.want {
				assert.GreaterOrEqual(t, info.FirstResponseTime.Sub(info.StartTime).Milliseconds(), int64(500))
			}
		})
	}
}

func TestRelayKeyHeaderBudgetEndsBeforeResponseBody(t *testing.T) {
	previous := common.MultiKeyFailoverResponseHeaderTimeout
	common.MultiKeyFailoverResponseHeaderTimeout = 1
	service.ResetProxyClientCache()
	t.Cleanup(func() { common.MultiKeyFailoverResponseHeaderTimeout = previous; service.ResetProxyClientCache() })
	for _, winner := range []string{"headers", "deadline"} {
		t.Run(winner, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
			ch := &model.Channel{Id: 919, Key: "primary\nbackup", Group: "default", ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeFailover}}
			service.MarkGroupKeyAttempt(c, ch, "primary")
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAnthropic, ChannelIsMultiKey: true}, KeyResponseDeadline: time.Now().Add(50 * time.Millisecond)}
			policy := service.NormalizeHTTPTransportPolicy(info.ChannelSetting)
			policy.ResponseHeaderTimeoutSeconds = 1
			client, err := service.GetHttpClientWithProxyPolicy("", policy)
			require.NoError(t, err)
			original := client.Transport
			t.Cleanup(func() { client.Transport = original })
			var requestContext context.Context
			client.Transport = channelProbeTestTransport(func(req *http.Request) (*http.Response, error) {
				requestContext = req.Context()
				if winner == "deadline" {
					<-req.Context().Done()
				}
				// Even a transport returning headers after cancellation must not leak a
				// response body whose context is already canceled to the stream handler.
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("long response")), Request: req}, nil
			})
			req := httptest.NewRequest(http.MethodPost, "http://fixture.invalid/v1/messages", strings.NewReader("{}"))
			req.RequestURI = ""
			response, requestErr := channel.DoRequest(c, req, info)
			if winner == "deadline" {
				require.Error(t, requestErr)
				assert.Nil(t, response)
				assert.True(t, info.UpstreamRequestTimedOut)
				return
			}
			require.NoError(t, requestErr)
			require.NotNil(t, response)
			timer := time.NewTimer(time.Until(info.KeyResponseDeadline.Add(time.Millisecond)))
			defer timer.Stop()
			<-timer.C
			assert.NoError(t, requestContext.Err(), "the original header deadline must not cancel an established body")
			body, readErr := io.ReadAll(response.Body)
			require.NoError(t, readErr)
			assert.Equal(t, "long response", string(body))
			require.NoError(t, response.Body.Close())
			assert.ErrorIs(t, requestContext.Err(), context.Canceled)
		})
	}
}

func TestRelayHeadersWithoutKeepaliveAreNotReplayable(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	helper.SetEventStreamHeaders(c)
	c.Writer.WriteHeaderNow()
	assert.False(t, helper.RelayResponseReplayable(c), "headers committed outside our keepalive path do not prove replay is safe")
}
