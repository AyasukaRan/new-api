package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChannelKeyAffinityTest(t *testing.T, redisAddress string) *model.Channel {
	t.Helper()
	db := setupChannelSelectAutoGroupsTest(t)
	common.MemoryCacheEnabled = false
	require.NoError(t, db.AutoMigrate(&model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}))
	previousDBType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, previousLogType)
	previousRedis, previousRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = redisAddress != ""
	if redisAddress != "" {
		client := redis.NewClient(&redis.Options{Addr: redisAddress})
		require.NoError(t, client.Ping(context.Background()).Err())
		common.RDB = client
		t.Cleanup(func() { require.NoError(t, client.Close()) })
	}
	channelKeyAffinityCacheOnce = sync.Once{}
	channelKeyAffinityCache = nil
	settings := operation_setting.GetChannelAffinitySetting()
	previousSettings := *settings
	*settings = operation_setting.ChannelAffinitySetting{Enabled: true, DefaultTTLSeconds: 60, MaxEntries: 100, Rules: []operation_setting.ChannelAffinityRule{{
		Name: t.Name(), ModelRegex: []string{"^affinity-model$"}, PathRegex: []string{"^/v1/messages$"}, SessionMode: "prefer",
		KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Affinity-Key"}},
	}}}
	t.Cleanup(func() {
		_, err := getChannelKeyAffinityCache().DeleteByPrefix(common.GenerateHMAC(t.Name()))
		require.NoError(t, err)
		*settings = previousSettings
		common.RedisEnabled, common.RDB = previousRedis, previousRDB
		common.SetDatabaseTypes(previousDBType, previousLogType)
		channelKeyAffinityCacheOnce = sync.Once{}
		channelKeyAffinityCache = nil
	})
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: t.Name(), Models: "affinity-model", Group: "default,vip", Status: common.ChannelStatusEnabled, Key: "affinity-primary-secret\naffinity-backup-secret"}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeFailover
	require.NoError(t, db.Create(channel).Error)
	return channel
}

func newChannelKeyAffinityContext(t *testing.T, session string, userID, tokenID int, group string, channelID int) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Affinity-Key", session)
	c.Set("id", userID)
	c.Set("token_id", tokenID)
	c.Set("channel_id", channelID)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
	GetPreferredChannelByAffinity(c, "affinity-model", group)
	return c
}

func TestChannelKeyAffinityFailover(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			address := ""
			if backend == "redis" {
				address = os.Getenv("TEST_REDIS_ADDR")
				if address == "" {
					t.Skip("TEST_REDIS_ADDR is not configured")
				}
			}
			channel := setupChannelKeyAffinityTest(t, address)
			keys := channel.GetKeys()
			initial := newChannelKeyAffinityContext(t, "existing-session", 1, 11, "default", channel.Id)
			selected, apiErr := SelectChannelKeyForRequest(initial, channel, channel.Models, false)
			require.Nil(t, apiErr)
			assert.Equal(t, keys[0], selected.Key)
			MarkRequestPolicySuccess(initial, nil)
			RecordChannelKeyAffinity(initial)
			late := newChannelKeyAffinityContext(t, "existing-session", 1, 11, "default", channel.Id)
			_, apiErr = SelectChannelKeyForRequest(late, channel, channel.Models, false)
			require.Nil(t, apiErr)
			retrying := newChannelKeyAffinityContext(t, "existing-session", 1, 11, "default", channel.Id)
			_, apiErr = SelectChannelKeyForRequest(retrying, channel, channel.Models, false)
			require.Nil(t, apiErr)
			RequestPolicy(retrying).BeginAttempt(channel, "default")
			now := time.Now().UnixMilli()
			require.NoError(t, model.RecordChannelKeyObservation(channel, channel.Models, keys[0], now, common.GetPointer(false), http.StatusBadGateway, "upstream unavailable", "probe"))
			selected, apiErr = SelectChannelKeyForRequest(retrying, channel, channel.Models, false)
			require.Nil(t, apiErr)
			assert.Equal(t, keys[1], selected.Key)
			MarkRequestPolicySuccess(retrying, nil)
			RecordChannelKeyAffinity(retrying)
			require.NoError(t, model.RecordChannelKeyObservation(channel, channel.Models, keys[0], now+1, common.GetPointer(true), http.StatusOK, "", "probe"))
			MarkRequestPolicySuccess(late, nil)
			RecordChannelKeyAffinity(late)
			for _, tc := range []struct {
				name, session, group string
				user, token, want    int
			}{
				{name: "existing session after recovery and late success", session: "existing-session", group: "default", user: 1, token: 11, want: 1},
				{name: "new session", session: "new-session", group: "default", user: 1, token: 11},
				{name: "different account", session: "existing-session", group: "default", user: 2, token: 11},
				{name: "different token", session: "existing-session", group: "default", user: 1, token: 12},
				{name: "different group", session: "existing-session", group: "vip", user: 1, token: 11},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c := newChannelKeyAffinityContext(t, tc.session, tc.user, tc.token, tc.group, channel.Id)
					selected, err := SelectChannelKeyForRequest(c, channel, channel.Models, false)
					require.Nil(t, err)
					assert.Equal(t, keys[tc.want], selected.Key)
				})
			}
			// Both requests missed a cold cache. An old primary success can fill
			// it before the request that actually failed over finishes.
			coldOld := newChannelKeyAffinityContext(t, "cold-session", 1, 11, "default", channel.Id)
			coldLate := newChannelKeyAffinityContext(t, "cold-session", 1, 11, "default", channel.Id)
			coldRetry := newChannelKeyAffinityContext(t, "cold-session", 1, 11, "default", channel.Id)
			for _, c := range []*gin.Context{coldOld, coldLate, coldRetry} {
				selected, apiErr := SelectChannelKeyForRequest(c, channel, channel.Models, false)
				require.Nil(t, apiErr)
				assert.Equal(t, keys[0], selected.Key)
			}
			MarkRequestPolicySuccess(coldOld, nil)
			RecordChannelKeyAffinity(coldOld)
			RequestPolicy(coldRetry).BeginAttempt(channel, "default")
			backup := *channel
			backup.RelayKeyIdentity = channel.KeyIdentity(keys[1])
			selected, apiErr = SelectChannelKeyForRequest(coldRetry, &backup, channel.Models, false)
			require.Nil(t, apiErr)
			assert.Equal(t, keys[1], selected.Key)
			MarkRequestPolicySuccess(coldRetry, nil)
			RecordChannelKeyAffinity(coldRetry)
			MarkRequestPolicySuccess(coldLate, nil)
			RecordChannelKeyAffinity(coldLate)
			coldNext := newChannelKeyAffinityContext(t, "cold-session", 1, 11, "default", channel.Id)
			selected, apiErr = SelectChannelKeyForRequest(coldNext, channel, channel.Models, false)
			require.Nil(t, apiErr)
			assert.Equal(t, keys[1], selected.Key, "the successful fallback must replace an intervening original-key binding")
			cacheKeys, err := getChannelKeyAffinityCache().Keys()
			require.NoError(t, err)
			for _, key := range cacheKeys {
				if !strings.Contains(key, common.GenerateHMAC(t.Name())+":") {
					continue
				}
				assert.NotContains(t, key, "existing-session")
				value, _, err := getChannelKeyAffinityCache().Get(key)
				require.NoError(t, err)
				assert.Equal(t, channel.KeyIdentity(keys[1]), value)
				assert.NotContains(t, value, "secret")
			}
		})
	}
}

func TestChannelKeyAffinityRejectsFailedOutcomesAndStaleKeys(t *testing.T) {
	channel := setupChannelKeyAffinityTest(t, "")
	keys := channel.GetKeys()
	for _, outcome := range []string{"failure", "stream failure", "client cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			c := newChannelKeyAffinityContext(t, outcome, 1, 11, "default", channel.Id)
			backup := *channel
			backup.RelayKeyIdentity = channel.KeyIdentity(keys[1])
			_, apiErr := SelectChannelKeyForRequest(c, &backup, channel.Models, false)
			require.Nil(t, apiErr)
			if outcome == "stream failure" {
				stream := relaycommon.NewStreamStatus()
				stream.MarkFailed("upstream_error", "upstream", 502)
				MarkRequestPolicySuccess(c, stream)
			} else if outcome == "client cancellation" {
				MarkRequestPolicySuccess(c, nil)
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			RecordChannelKeyAffinity(c)
			next := newChannelKeyAffinityContext(t, outcome, 1, 11, "default", channel.Id)
			selected, apiErr := SelectChannelKeyForRequest(next, channel, channel.Models, false)
			require.Nil(t, apiErr)
			assert.Equal(t, keys[0], selected.Key)
		})
	}
	for _, change := range []string{"group permission", "disabled key", "removed key", "reordered keys", "failed health", "negative balance", "disabled channel"} {
		t.Run(change, func(t *testing.T) {
			current := *channel
			current.Id = 0
			current.Keys = nil
			require.NoError(t, model.DB.Create(&current).Error)
			c := newChannelKeyAffinityContext(t, change, 1, 11, "default", current.Id)
			backup := current
			backup.RelayKeyIdentity = current.KeyIdentity(keys[1])
			_, apiErr := SelectChannelKeyForRequest(c, &backup, current.Models, false)
			require.Nil(t, apiErr)
			MarkRequestPolicySuccess(c, nil)
			RecordChannelKeyAffinity(c)
			switch change {
			case "group permission":
				current.ChannelInfo.KeyGroups = map[string][]string{current.KeyIdentity(keys[1]): {"vip"}}
			case "disabled key":
				current.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
			case "removed key":
				current.Key = keys[0]
			case "reordered keys":
				current.Key = keys[1] + "\n" + keys[0]
			case "failed health":
				require.NoError(t, model.RecordChannelKeyObservation(&current, current.Models, keys[1], time.Now().UnixMilli(), common.GetPointer(false), 502, "unavailable", "probe"))
			case "negative balance":
				require.NoError(t, model.RecordChannelBalanceSample(&current, &model.ChannelBalanceSample{CheckedAt: time.Now().UnixMilli(), KeyBalances: []model.ChannelKeyBalance{{Index: 1, Balance: common.GetPointer(-1.0)}}}))
			case "disabled channel":
				current.Status = common.ChannelStatusManuallyDisabled
			}
			require.NoError(t, model.DB.Model(&current).Select("key", "channel_info", "status").Updates(&current).Error)
			next := newChannelKeyAffinityContext(t, change, 1, 11, "default", current.Id)
			stale := *channel
			stale.Id = current.Id
			selected, apiErr := SelectChannelKeyForRequest(next, &stale, current.Models, false)
			if change == "disabled channel" {
				require.NotNil(t, apiErr)
				return
			}
			require.Nil(t, apiErr)
			want := keys[0]
			if change == "reordered keys" {
				want = keys[1]
			}
			assert.Equal(t, want, selected.Key, "cached affinity must not bypass current eligibility")
		})
	}
}

func TestChannelKeyAffinityGroupTier(t *testing.T) {
	channel := setupChannelKeyAffinityTest(t, "")
	keys := channel.GetKeys()
	channel.ChannelInfo.KeyGroupPriorities = map[string]map[string]int64{
		channel.KeyIdentity(keys[0]): {"default": 100},
		channel.KeyIdentity(keys[1]): {"default": 1},
	}
	channel.ChannelInfo.KeyCostRatios = map[string]float64{channel.KeyIdentity(keys[0]): 1, channel.KeyIdentity(keys[1]): 2}
	require.NoError(t, model.DB.Model(channel).Update("channel_info", channel.ChannelInfo).Error)
	c := newChannelKeyAffinityContext(t, "tier-session", 1, 11, "default", channel.Id)
	backup := *channel
	backup.RelayKeyIdentity = channel.KeyIdentity(keys[1])
	selected, apiErr := SelectChannelKeyForRequest(c, &backup, channel.Models, false)
	require.Nil(t, apiErr)
	require.NotNil(t, selected.UpstreamCostRatio)
	assert.Equal(t, 2.0, *selected.UpstreamCostRatio)
	MarkRequestPolicySuccess(c, nil)
	RecordChannelKeyAffinity(c)
	for _, session := range []string{"tier-session", "new-tier-session"} {
		next := newChannelKeyAffinityContext(t, session, 1, 11, "default", channel.Id)
		candidate, enabled, err := selectGroupKeyTier(next, "default", channel.Models, 0, channel.Id)
		require.NoError(t, err)
		require.True(t, enabled)
		require.NotNil(t, candidate)
		selected, apiErr = SelectChannelKeyForRequest(next, candidate, channel.Models, false)
		require.Nil(t, apiErr)
		want, ratio := keys[0], 1.0
		if session == "tier-session" {
			want, ratio = keys[1], 2
		}
		assert.Equal(t, want, selected.Key)
		require.NotNil(t, selected.UpstreamCostRatio)
		assert.Equal(t, ratio, *selected.UpstreamCostRatio, "affinity must retain the selected physical credential's cost")
	}
	// The recovered primary may belong to a different, higher-priority channel.
	higher := &model.Channel{Type: channel.Type, Name: "recovered higher tier", Models: channel.Models, Group: channel.Group,
		Status: common.ChannelStatusEnabled, Key: "recovered-primary-secret", Priority: common.GetPointer(int64(200))}
	higher.ChannelInfo.IsMultiKey, higher.ChannelInfo.MultiKeyMode = true, constant.MultiKeyModeFailover
	require.NoError(t, model.DB.Create(higher).Error)
	for _, session := range []string{"tier-session", "new-tier-session"} {
		next := newChannelKeyAffinityContext(t, session, 1, 11, "default", channel.Id)
		candidate, enabled, err := selectGroupKeyTier(next, "default", channel.Models, 0, channel.Id)
		require.NoError(t, err)
		require.True(t, enabled)
		require.NotNil(t, candidate)
		selected, apiErr = SelectChannelKeyForRequest(next, candidate, channel.Models, false)
		require.Nil(t, apiErr)
		want := higher.Key
		if session == "tier-session" {
			want = keys[1]
		}
		assert.Equal(t, want, selected.Key, "healthy established sessions survive recovery of a higher channel tier")
	}
}

func buildChannelAffinityTemplateContextForTest(meta channelAffinityMeta) *gin.Context {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	setChannelAffinityContext(ctx, meta)
	return ctx
}

func TestApplyChannelAffinityOverrideTemplate_NoTemplate(t *testing.T) {
	ctx := buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
		RuleName: "rule-no-template",
	})
	base := map[string]any{
		"temperature": 0.7,
	}

	merged, applied := ApplyChannelAffinityOverrideTemplate(ctx, base)
	require.False(t, applied)
	require.Equal(t, base, merged)
}

func TestApplyChannelAffinityOverrideTemplate_MergeTemplate(t *testing.T) {
	ctx := buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
		RuleName: "rule-with-template",
		ParamTemplate: map[string]any{
			"temperature": 0.2,
			"top_p":       0.95,
		},
		UsingGroup:     "default",
		ModelName:      "gpt-4.1",
		RequestPath:    "/v1/responses",
		KeySourceType:  "gjson",
		KeySourcePath:  "prompt_cache_key",
		KeyHint:        "abcd...wxyz",
		KeyFingerprint: "abcd1234",
	})
	base := map[string]any{
		"temperature": 0.7,
		"max_tokens":  2000,
	}

	merged, applied := ApplyChannelAffinityOverrideTemplate(ctx, base)
	require.True(t, applied)
	require.Equal(t, 0.7, merged["temperature"])
	require.Equal(t, 0.95, merged["top_p"])
	require.Equal(t, 2000, merged["max_tokens"])
	require.Equal(t, 0.7, base["temperature"])

	anyInfo, ok := ctx.Get(ginKeyChannelAffinityLogInfo)
	require.True(t, ok)
	info, ok := anyInfo.(map[string]any)
	require.True(t, ok)
	overrideInfoAny, ok := info["override_template"]
	require.True(t, ok)
	overrideInfo, ok := overrideInfoAny.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, overrideInfo["applied"])
	require.Equal(t, "rule-with-template", overrideInfo["rule_name"])
	require.EqualValues(t, 2, overrideInfo["param_override_keys"])
}

func TestApplyChannelAffinityOverrideTemplate_MergeOperations(t *testing.T) {
	ctx := buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
		RuleName: "rule-with-ops-template",
		ParamTemplate: map[string]any{
			"operations": []map[string]any{
				{
					"mode":  "pass_headers",
					"value": []string{"Originator"},
				},
			},
		},
	})
	base := map[string]any{
		"temperature": 0.7,
		"operations": []map[string]any{
			{
				"path":  "model",
				"mode":  "trim_prefix",
				"value": "openai/",
			},
		},
	}

	merged, applied := ApplyChannelAffinityOverrideTemplate(ctx, base)
	require.True(t, applied)
	require.Equal(t, 0.7, merged["temperature"])

	opsAny, ok := merged["operations"]
	require.True(t, ok)
	ops, ok := opsAny.([]any)
	require.True(t, ok)
	require.Len(t, ops, 2)

	firstOp, ok := ops[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "pass_headers", firstOp["mode"])

	secondOp, ok := ops[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "trim_prefix", secondOp["mode"])
}

func TestShouldSkipRetryAfterChannelAffinityFailure(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() *gin.Context
		want bool
	}{
		{
			name: "nil context",
			ctx: func() *gin.Context {
				return nil
			},
			want: false,
		},
		{
			name: "explicit skip retry flag in context",
			ctx: func() *gin.Context {
				ctx := buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
					RuleName:   "rule-explicit-flag",
					SkipRetry:  false,
					UsingGroup: "default",
					ModelName:  "gpt-5",
				})
				ctx.Set(ginKeyChannelAffinitySkipRetry, true)
				return ctx
			},
			want: true,
		},
		{
			name: "fallback to matched rule meta",
			ctx: func() *gin.Context {
				return buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
					RuleName:   "rule-skip-retry",
					SkipRetry:  true,
					UsingGroup: "default",
					ModelName:  "gpt-5",
				})
			},
			want: true,
		},
		{
			name: "no flag and no skip retry meta",
			ctx: func() *gin.Context {
				return buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
					RuleName:   "rule-no-skip-retry",
					SkipRetry:  false,
					UsingGroup: "default",
					ModelName:  "gpt-5",
				})
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ShouldSkipRetryAfterChannelAffinityFailure(tt.ctx()))
		})
	}
}

func TestExtractChannelAffinityValue_RequestHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("X-Affinity-Key", " tenant-123 ")

	value := extractChannelAffinityValue(ctx, operation_setting.ChannelAffinityKeySource{
		Type: "request_header",
		Key:  "X-Affinity-Key",
	})

	require.Equal(t, "tenant-123", value)
}

func TestGetPreferredChannelByAffinity_RequestHeaderKeySource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rule := operation_setting.ChannelAffinityRule{
		Name:       "header-affinity",
		ModelRegex: []string{"^gpt-.*$"},
		PathRegex:  []string{"/v1/responses"},
		KeySources: []operation_setting.ChannelAffinityKeySource{
			{Type: "request_header", Key: "X-Affinity-Key"},
		},
		IncludeRuleName:  true,
		IncludeModelName: true,
	}

	affinityValue := fmt.Sprintf("header-hit-%d", time.Now().UnixNano())
	cacheKeySuffix := buildChannelAffinityCacheKeySuffix(rule, "gpt-5", "default", affinityValue)

	cache := getChannelAffinityCache()
	require.NoError(t, cache.SetWithTTL(cacheKeySuffix, 9528, time.Minute))
	t.Cleanup(func() {
		_, _ = cache.DeleteMany([]string{cacheKeySuffix})
	})

	setting := operation_setting.GetChannelAffinitySetting()
	originalRules := setting.Rules
	setting.Rules = append([]operation_setting.ChannelAffinityRule{rule}, originalRules...)
	t.Cleanup(func() {
		setting.Rules = originalRules
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("X-Affinity-Key", affinityValue)

	channelID, found := GetPreferredChannelByAffinity(ctx, "gpt-5", "default")
	require.True(t, found)
	require.Equal(t, 9528, channelID)

	meta, ok := getChannelAffinityMeta(ctx)
	require.True(t, ok)
	require.Equal(t, "request_header", meta.KeySourceType)
	require.Equal(t, "X-Affinity-Key", meta.KeySourceKey)
	require.Equal(t, buildChannelAffinityKeyHint(affinityValue), meta.KeyHint)
}

func TestClearCurrentChannelAffinityCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cacheKeySuffix := fmt.Sprintf("codex cli trace:default:clear-current-%d", time.Now().UnixNano())
	cacheKeyFull := channelAffinityCacheNamespace + ":" + cacheKeySuffix
	cache := getChannelAffinityCache()
	require.NoError(t, cache.SetWithTTL(cacheKeySuffix, 9527, time.Minute))
	t.Cleanup(func() {
		_, _ = cache.DeleteMany([]string{cacheKeySuffix})
	})

	ctx := buildChannelAffinityTemplateContextForTest(channelAffinityMeta{
		CacheKey:   cacheKeyFull,
		TTLSeconds: 60,
		RuleName:   "codex cli trace",
		SkipRetry:  true,
	})
	require.True(t, ShouldSkipRetryAfterChannelAffinityFailure(ctx))

	deleted := ClearCurrentChannelAffinityCache(ctx)
	require.True(t, deleted)
	_, found, err := cache.Get(cacheKeySuffix)
	require.NoError(t, err)
	require.False(t, found)
	require.False(t, ShouldSkipRetryAfterChannelAffinityFailure(ctx))
}

func TestChannelAffinityHitCodexTemplatePassHeadersEffective(t *testing.T) {
	gin.SetMode(gin.TestMode)

	setting := operation_setting.GetChannelAffinitySetting()
	require.NotNil(t, setting)

	var codexRule *operation_setting.ChannelAffinityRule
	for i := range setting.Rules {
		rule := &setting.Rules[i]
		if strings.EqualFold(strings.TrimSpace(rule.Name), "codex cli trace") {
			codexRule = rule
			break
		}
	}
	require.NotNil(t, codexRule)

	affinityValue := fmt.Sprintf("pc-hit-%d", time.Now().UnixNano())
	cacheKeySuffix := buildChannelAffinityCacheKeySuffix(*codexRule, "gpt-5", "default", affinityValue)

	cache := getChannelAffinityCache()
	require.NoError(t, cache.SetWithTTL(cacheKeySuffix, 9527, time.Minute))
	t.Cleanup(func() {
		_, _ = cache.DeleteMany([]string{cacheKeySuffix})
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"prompt_cache_key":"%s"}`, affinityValue)))
	ctx.Request.Header.Set("Content-Type", "application/json")

	channelID, found := GetPreferredChannelByAffinity(ctx, "gpt-5", "default")
	require.True(t, found)
	require.Equal(t, 9527, channelID)

	baseOverride := map[string]any{
		"temperature": 0.2,
	}
	mergedOverride, applied := ApplyChannelAffinityOverrideTemplate(ctx, baseOverride)
	require.True(t, applied)
	require.Equal(t, 0.2, mergedOverride["temperature"])

	info := &relaycommon.RelayInfo{
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
			"User-Agent": "codex-cli-test",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: mergedOverride,
			HeadersOverride: map[string]any{
				"X-Static": "legacy-static",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-5"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)

	require.Equal(t, "legacy-static", info.RuntimeHeadersOverride["x-static"])
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	require.Equal(t, "codex-cli-test", info.RuntimeHeadersOverride["user-agent"])

	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	_, exists = info.RuntimeHeadersOverride["x-codex-turn-metadata"]
	require.False(t, exists)
}

func TestMidjourneyPolicyAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		code       int
		properties map[string]any
		requestErr error
		accepted   bool
		reason     string
	}{
		{name: "submitted", status: 200, code: 1, accepted: true},
		{name: "existing task", status: 200, code: 21, accepted: true},
		{name: "queued", status: 200, code: 22, accepted: true},
		{name: "HTTP 200 with rejected prompt", status: 200, code: 24, reason: "non_retryable_error"},
		{name: "failed existing task", status: 200, code: 21, properties: map[string]any{"status": "FAILURE"}, reason: "task_accepted"},
		{name: "HTTP failure", status: 502, code: 1, reason: "non_retryable_error"},
		{name: "ambiguous transport failure", status: 502, requestErr: errors.New("connection reset"), reason: "non_retryable_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/imagine", strings.NewReader(`{}`))
			ctx.Set("channel_id", 7)
			state := RequestPolicy(ctx)
			state.BeginAttempt(&model.Channel{Id: 7}, "default")
			response := &dto.MidjourneyResponseWithStatusCode{StatusCode: tc.status, Response: dto.MidjourneyResponse{Code: tc.code, Properties: tc.properties}}
			accepted := RecordMidjourneyPolicyResponse(ctx, response, tc.requestErr)
			assert.Equal(t, tc.accepted, accepted)
			assert.False(t, state.Successful, "acceptance alone must not bind before local processing finishes")
			if accepted {
				MarkRequestPolicySuccess(ctx, nil)
				assert.True(t, state.Successful)
				return
			}
			events := state.Events()
			require.Len(t, events, 3)
			assert.Equal(t, PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: "upstream"}, events[1].Decision)
			assert.Equal(t, tc.status, events[1].Status)
			assert.Equal(t, PolicyDecision{Action: "stop", Reason: tc.reason, Source: "system"}, events[2].Decision, "submissions are never replayed")
		})
	}
}

func TestSessionRulesInheritOrOverrideGlobalDefault(t *testing.T) {
	setting := operation_setting.GetChannelAffinitySetting()
	previous := *setting
	t.Cleanup(func() { *setting = previous })
	for _, globalMode := range []string{"", "off", "prefer", "strict"} {
		for _, tc := range []struct {
			name, mode, expected string
			skipRetry            bool
		}{
			{name: "inherit", mode: "inherit", expected: globalMode},
			{name: "override-strict", mode: "strict", expected: "strict"},
			{name: "override-prefer", mode: "prefer", expected: "prefer", skipRetry: true},
			{name: "override-off", mode: "off", expected: "off", skipRetry: true},
			{name: "legacy-strict", expected: "strict", skipRetry: true},
			{name: "legacy-prefer", expected: "prefer"},
		} {
			t.Run(globalMode+"/"+tc.name, func(t *testing.T) {
				expected := tc.expected
				if expected == "" {
					expected = "prefer"
				}
				source := "session_rule"
				if tc.mode == "inherit" {
					source = "global"
				}
				rule := operation_setting.ChannelAffinityRule{
					Name: tc.name, ModelRegex: []string{"^test-model$"},
					KeySources:  []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Session"}},
					SessionMode: tc.mode, SkipRetryOnFailure: tc.skipRetry,
					ParamOverrideTemplate: map[string]any{"temperature": 0}, IncludeRuleName: true,
				}
				encoded, err := common.Marshal([]operation_setting.ChannelAffinityRule{rule})
				require.NoError(t, err)
				snapshot, err := model.BuildRequestPolicy(map[string]string{"RetryTimes": "2", "channel_affinity_setting.enabled": "true", "channel_affinity_setting.session_mode": globalMode, "channel_affinity_setting.rules": string(encoded)})
				require.NoError(t, err)
				*setting = snapshot.Affinity
				key := t.Name()
				cacheKey := buildChannelAffinityCacheKeySuffix(rule, "test-model", "default", key)
				cache := getChannelAffinityCache()
				require.NoError(t, cache.SetWithTTL(cacheKey, 1, time.Minute))
				t.Cleanup(func() { _, err := cache.DeleteMany([]string{cacheKey}); assert.NoError(t, err) })
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				ctx.Request.Header.Set("X-Session", key)
				state := RequestPolicy(ctx)
				channelID, found := GetPreferredChannelByAffinity(ctx, "test-model", "default")
				assert.Equal(t, expected != "off", found)
				if found {
					assert.Equal(t, 1, channelID)
				}
				assert.Equal(t, expected, state.SessionMode)
				assert.Equal(t, source, state.SessionModeSource)
				assert.Equal(t, expected == "strict", ShouldSkipRetryAfterChannelAffinityFailure(ctx))
				decision := DecideRelayRetry(ctx, types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests), 2)
				if expected == "strict" {
					assert.Equal(t, PolicyDecision{Action: "stop", Reason: "strict_session", Source: source}, decision)
				} else {
					assert.Equal(t, "retry", decision.Action)
				}
				events := state.Events()
				require.Len(t, events, 1)
				assert.Equal(t, PolicyDecision{Action: "match", Reason: "session_rule_matched", Source: "session_rule"}, events[0].Decision)
				assert.Equal(t, tc.name, events[0].Rule)
				transformed, applied := ApplyChannelAffinityOverrideTemplate(ctx, nil)
				assert.True(t, applied, "session behavior does not disable request transforms")
				assert.EqualValues(t, 0, transformed["temperature"])
				if expected == "off" {
					ctx.Set("channel_id", 9)
					RecordChannelAffinity(ctx, 9)
					cached, found, err := cache.Get(cacheKey)
					require.NoError(t, err)
					assert.True(t, found)
					assert.Equal(t, 1, cached, "off does not refresh session bindings")
					_, err = cache.DeleteMany([]string{cacheKey})
					require.NoError(t, err)
					RecordChannelAffinity(ctx, 9)
					_, found, err = cache.Get(cacheKey)
					require.NoError(t, err)
					assert.False(t, found, "off does not establish session bindings")
				}
			})
		}
	}
}
