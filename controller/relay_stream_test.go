package controller

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	require.NoError(t, i18n.Init())
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelBalanceSample{}, &model.Log{}))
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": previousConfig["perf_metrics_setting.enabled"], "quota_setting.enable_free_model_pre_consume": previousConfig["quota_setting.enable_free_model_pre_consume"]}))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false", "quota_setting.enable_free_model_pre_consume": "false"}))
	previousRetries, previousDisable := common.RetryTimes, common.AutomaticDisableChannelEnabled
	previousHeader, previousFailover, previousTimeout := common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout
	previousCount, previousLogs, previousTrace, previousErrors := constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled
	previousGroups := ratio_setting.GroupRatio2JSONString()
	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	general := operation_setting.GetGeneralSetting()
	previousPing := general.PingIntervalEnabled
	affinity.Enabled, general.PingIntervalEnabled = false, false
	common.AutomaticDisableChannelEnabled = false
	common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout = 0, 1, 0
	constant.CountToken, common.LogConsumeEnabled, common.RequestTraceEnabled, constant.ErrorLogEnabled = false, false, false, false
	t.Cleanup(func() {
		common.RetryTimes, common.AutomaticDisableChannelEnabled = previousRetries, previousDisable
		common.RelayResponseHeaderTimeout, common.MultiKeyFailoverResponseHeaderTimeout, common.RelayTimeout = previousHeader, previousFailover, previousTimeout
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
		name            string
		retries         int
		cancel, written bool
		wantKeys        []string
		wantStatus      int
	}{
		{name: "timeout_uses_next_ordered_key", retries: 3, wantKeys: []string{"header-primary", "header-backup"}, wantStatus: 200},
		{name: "timeout_respects_retry_budget", retries: 0, wantKeys: []string{"header-primary"}, wantStatus: 500},
		{name: "client_cancel_stops_upstream_and_retry", retries: 3, cancel: true, wantKeys: []string{"header-primary"}, wantStatus: 500},
		{name: "business_output_is_never_replayed", retries: 3, written: true, wantKeys: []string{"header-primary"}, wantStatus: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			common.RetryTimes = test.retries
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var mutex sync.Mutex
			var used []string
			upstreamCanceled := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				key := r.Header.Get("X-Api-Key")
				mutex.Lock()
				used = append(used, key)
				mutex.Unlock()
				if key == "header-primary" && !test.written {
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
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-header-fixture\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
				if test.written {
					_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"fixture failed after output\"}}\n\n")
					return
				}
				_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer upstream.Close()
			ch := &model.Channel{Name: test.name, Type: constant.ChannelTypeAnthropic, Key: "header-backup\nheader-primary\nheader-last", Models: "claude-header-fixture", Group: "default", Status: common.ChannelStatusEnabled, BaseURL: common.GetPointer(upstream.URL), ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModeFailover}}
			require.NoError(t, ch.Insert())
			t.Cleanup(func() { require.NoError(t, ch.Delete()) })
			ch.ChannelInfo.KeyOrder = []string{ch.KeyIdentity("header-primary"), ch.KeyIdentity("header-backup"), ch.KeyIdentity("header-last")}
			require.NoError(t, db.Model(ch).Update("channel_info", ch.ChannelInfo).Error)
			c, recorder := probeRoutingContext(ch.Models, "default")
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-header-fixture","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			selected, _, selectErr := service.SelectChannelForRequest(c, ch.Models, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: ch.Models})
			require.Nil(t, selectErr)
			require.NotNil(t, selected)
			require.Nil(t, middleware.SetupContextForSelectedChannel(c, selected, ch.Models))
			Relay(c, types.RelayFormatClaude)
			mutex.Lock()
			assert.Equal(t, test.wantKeys, used)
			mutex.Unlock()
			assert.Equal(t, test.wantStatus, recorder.Code, recorder.Body.String())
			if test.cancel {
				assert.ErrorIs(t, ctx.Err(), context.Canceled)
			} else {
				assert.NoError(t, ctx.Err(), "a transport header timeout must not cancel the inbound request")
			}
			if !test.written {
				select {
				case <-upstreamCanceled:
				case <-time.After(time.Second):
					t.Fatal("the failed upstream HTTP request was not canceled")
				}
			}
			if len(test.wantKeys) > 1 {
				assert.Contains(t, recorder.Body.String(), "event: message_stop")
				assert.NotContains(t, recorder.Body.String(), "event: error")
			} else if test.written {
				assert.Contains(t, recorder.Body.String(), "event: message_start")
				assert.Contains(t, recorder.Body.String(), "event: error")
			}
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
