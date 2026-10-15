package controller

import (
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
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
