package helper

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelPriceHelperTieredUsesPreloadedRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-test-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"tiered-test-model":"param(\"stream\") == true ? tier(\"stream\", p * 3) : tier(\"base\", p * 2)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-test-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"stream":true}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{
		BillingRatios: map[string]float64{"n": 3},
	})
	require.NoError(t, err)
	require.Equal(t, 1500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "stream", info.TieredBillingSnapshot.EstimatedTier)
	require.Equal(t, billing_setting.BillingModeTieredExpr, info.TieredBillingSnapshot.BillingMode)
	require.Equal(t, common.QuotaPerUnit, info.TieredBillingSnapshot.QuotaPerUnit)
}

func TestFixedPricePreConsumeAndRealtimeRejection(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"fixed-test":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"fixed-test":"len <= 32000 ? tier(\"short\", fixed(0.01)) : tier(\"long\", p * 2)"}`,
		"group_ratio_setting.group_ratio": `{"default":1.5}`,
	}))
	for _, tc := range []struct {
		name      string
		format    types.RelayFormat
		prompt    int
		wantError bool
	}{
		{"HTTP charges once", types.RelayFormatOpenAI, 0, false},
		{"Realtime rejects even unselected fixed branch", types.RelayFormatOpenAIRealtime, 50000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{OriginModelName: "fixed-test", UserGroup: "default", UsingGroup: "default", RelayFormat: tc.format, BillingRequestInput: &billingexpr.RequestInput{}}
			price, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{})
			if tc.wantError {
				require.ErrorContains(t, err, "Realtime")
				assert.Nil(t, info.TieredBillingSnapshot)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 7500, price.QuotaToPreConsume)
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, billingexpr.BillingUnitRequest, info.TieredBillingSnapshot.EstimatedBillingUnit)
			require.NotNil(t, info.TieredBillingSnapshot.EstimatedFixedPrice)
			assert.Equal(t, 0.01, *info.TieredBillingSnapshot.EstimatedFixedPrice)
		})
	}
}

func TestModelPriceHelperTieredPreConsumeMaxTokensFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-fallback-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-fallback-model":"tier(\"base\", p * 3 + c * 15)"}`,
		"group_ratio_setting.group_ratio": `{"default":1,"free":0}`,
	}))

	const promptTokens = 1000

	cases := []struct {
		name      string
		group     string
		maxTokens int
		expected  int
	}{
		{
			// max_tokens omitted in a paid group -> fall back to 8192 completion tokens.
			// p*3 + c*15 = 1000*3 + 8192*15 = 125880 -> /1e6 * 500000 = 62940
			name:      "non-free group falls back to 8192 completion tokens",
			group:     "default",
			maxTokens: 0,
			expected:  62940,
		},
		{
			// explicit max_tokens is used verbatim, no fallback.
			// 1000*3 + 100*15 = 4500 -> /1e6 * 500000 = 2250
			name:      "explicit max_tokens is used verbatim",
			group:     "default",
			maxTokens: 100,
			expected:  2250,
		},
		{
			// free group (ratio 0) stays zero; fallback is gated on non-zero group ratio.
			name:      "free group stays zero without fallback",
			group:     "free",
			maxTokens: 0,
			expected:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			ctx.Set("group", tc.group)

			info := &relaycommon.RelayInfo{
				OriginModelName: "tiered-fallback-model",
				UserGroup:       tc.group,
				UsingGroup:      tc.group,
				RequestHeaders:  map[string]string{"Content-Type": "application/json"},
				BillingRequestInput: &billingexpr.RequestInput{
					Headers: map[string]string{"Content-Type": "application/json"},
					Body:    []byte(`{}`),
				},
			}

			priceData, err := ModelPriceHelper(ctx, info, promptTokens, &types.TokenCountMeta{MaxTokens: tc.maxTokens})
			require.NoError(t, err)
			require.Equal(t, tc.expected, priceData.QuotaToPreConsume)
		})
	}
}

func TestModelPriceHelperTieredRejectsPreConsumeOverflow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-overflow-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-overflow-model":"tier(\"overflow\", p * 100000000000000000)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-overflow-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{}`),
		},
	}

	_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})

	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaRound", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
}

func TestModelPriceHelperRequestBillingRatiosOnlyApplyToFixedPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedModelPrices := ratio_setting.ModelPrice2JSONString()
	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
	})

	modelPrices, err := common.Marshal(map[string]float64{
		"fixed-image-price":      0.04,
		"fractional-image-price": 0.0000012,
		"overflow-image-price":   float64(common.MaxQuota) / common.QuotaPerUnit / 2,
	})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(modelPrices)))
	modelRatios, err := common.Marshal(map[string]float64{"ratio-image-price": 15})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(modelRatios)))

	tests := []struct {
		name           string
		model          string
		wantQuota      int
		wantUsePrice   bool
		wantImageCount bool
	}{
		{
			name:           "fixed price applies image count",
			model:          "fixed-image-price",
			wantQuota:      180000,
			wantUsePrice:   true,
			wantImageCount: true,
		},
		{
			name:         "ratio price ignores request billing ratios",
			model:        "ratio-image-price",
			wantQuota:    15000,
			wantUsePrice: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("group", "default")
			info := &relaycommon.RelayInfo{
				OriginModelName: tt.model,
				UserGroup:       "default",
				UsingGroup:      "default",
			}
			meta := &types.TokenCountMeta{
				ImagePriceRatio: 3,
				BillingRatios:   map[string]float64{"n": 3},
			}

			priceData, err := ModelPriceHelper(ctx, info, 1000, meta)

			require.NoError(t, err)
			require.Equal(t, tt.wantQuota, priceData.QuotaToPreConsume)
			require.Equal(t, tt.wantUsePrice, priceData.UsePrice)
			require.Equal(t, tt.wantImageCount, priceData.HasOtherRatio("n"))
			require.Equal(t, priceData.OtherRatios(), info.PriceData.OtherRatios())
		})
	}

	newInfo := func(model string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set("group", "default")
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       "default",
			UsingGroup:      "default",
		}
	}
	meta := &types.TokenCountMeta{BillingRatios: map[string]float64{"n": 3}}

	ctx, info := newInfo("fractional-image-price")
	priceData, err := ModelPriceHelper(ctx, info, 0, meta)
	require.NoError(t, err)
	// 0.0000012 * 500000 * 3 = 1.8, then truncate once to 1.
	require.Equal(t, 1, priceData.QuotaToPreConsume)

	ctx, info = newInfo("overflow-image-price")
	_, err = ModelPriceHelper(ctx, info, 0, meta)
	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaFromFloat", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
	require.Nil(t, info.Billing)
}

// Pricing identity is resolved once in ModelPriceHelper via the candidate
// ladder: raw name (only when it has no @ modifiers) → canonical
// base@effort:E@thinking:S → base@thinking:S → base. Each level is looked up
// after FormatMatchingModelName wildcard normalization. A hit on the raw
// gemini-2.5-flash-thinking-* wildcard must keep the client origin as the
// consume-log name.
func TestModelPriceHelperUsesSuffixedOriginLikeMain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gemini-2.5-flash"] = 0.15
	ratios["gemini-2.5-flash-thinking-*"] = 0.075
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")

	suffixed := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	suffixedPrice, err := ModelPriceHelper(ctx, suffixed, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, suffixed.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash-thinking-8192", suffixed.GetBillingModelName())
	assert.Equal(t, 0.075, suffixedPrice.ModelRatio)

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	adapterOn := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	adapterOnPrice, err := ModelPriceHelper(ctx, adapterOn, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, adapterOn.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash-thinking-8192", adapterOn.GetBillingModelName())
	assert.Equal(t, 0.075, adapterOnPrice.ModelRatio)

	base := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	basePrice, err := ModelPriceHelper(ctx, base, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, base.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash", base.GetBillingModelName())
	assert.Equal(t, 0.15, basePrice.ModelRatio)
}

func TestModelPriceHelperHonorsCustomClaudeThinkingAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["claude-3-7-sonnet"] = 1.5
	ratios["claude-3-7-sonnet-thinking"] = 3.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	claudeSettings := model_setting.GetClaudeSettings()
	oldThinking := claudeSettings.ThinkingAdapterEnabled
	claudeSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { claudeSettings.ThinkingAdapterEnabled = oldThinking })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "claude-3-7-sonnet-thinking", info.GetBillingModelName())
	assert.Equal(t, 3.0, priceData.ModelRatio)
}

func TestModelPriceHelperCanonicalBillingLadder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")

	t.Run("level2 full form", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max")
		ratios["qwen3-max@effort:high@thinking:on"] = 4.0
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:on@effort:high@temperature:0.2",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@effort:high@thinking:on", info.BillingModelName)
		assert.Equal(t, 4.0, priceData.ModelRatio)
	})

	t.Run("level3 thinking form shuffled budget", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max")
		delete(ratios, "qwen3-max@effort:high@thinking:on")
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@temperature:0.3@thinking:8192",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@thinking:on", info.BillingModelName)
		assert.Equal(t, 3.0, priceData.ModelRatio)
	})

	t.Run("level4 base fallback", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max@thinking:on")
		delete(ratios, "qwen3-max@effort:high@thinking:on")
		ratios["qwen3-max"] = 1.25
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:off",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max", info.BillingModelName)
		assert.Equal(t, 1.25, priceData.ModelRatio)
	})

	t.Run("thinking minus one bills as on", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:-1",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@thinking:on", info.BillingModelName)
		assert.Equal(t, 3.0, priceData.ModelRatio)
	})
}

func TestModelPriceHelperMigratesLegacyGeminiWildcardToCanonical(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	delete(ratios, "gemini-2.5-flash-thinking-*")
	ratios["gemini-2.5-flash"] = 0.15
	ratios["gemini-2.5-flash@thinking:on"] = 0.09
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, "gemini-2.5-flash@thinking:on", info.BillingModelName)
	assert.Equal(t, 0.09, priceData.ModelRatio)
}

func TestModelPriceHelperModifierNameFallsBackToBase(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["qwen3.8-max"] = 2.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-max@thinking:on@temperature:0.2",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, "qwen3.8-max", info.BillingModelName)
	assert.Equal(t, 2.0, priceData.ModelRatio)
}

func TestModelPriceHelperExemptAtNameBillsVerbatim(t *testing.T) {
	gin.SetMode(gin.TestMode)

	settings := model_setting.GetGlobalSettings()
	originalBlacklist := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = originalBlacklist })
	settings.ThinkingModelBlacklist = append(originalBlacklist, "re:.*@sha256:.*")

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["opaque"] = 1.0
	ratios["opaque@sha256:deadbeef"] = 7.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "opaque@sha256:deadbeef",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "opaque@sha256:deadbeef", info.GetBillingModelName())
	assert.Equal(t, 7.0, priceData.ModelRatio)
}

func TestModelPriceHelperPreservesGpt51CodexMaxIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gpt-5.1-codex-max"] = 1.75
	ratios["gpt-5.1-codex"] = 9.9
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1-codex-max",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "gpt-5.1-codex-max", info.GetBillingModelName())
	assert.Equal(t, 1.75, priceData.ModelRatio)
}

func TestModelPriceHelperNativeGeminiNoThinkingDoesNotAliasBillingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gemini-3-pro"] = 1.25
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	budget := 0
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-pro",
		UserGroup:       "default",
		UsingGroup:      "default",
		Request: &dto.GeminiChatRequest{
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{
					ThinkingBudget: &budget,
				},
			},
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "gemini-3-pro", info.GetBillingModelName())
	assert.Equal(t, 1.25, priceData.ModelRatio)
	assert.NotEqual(t, 37.5, priceData.ModelRatio)
}

func withChannelModelPricing(t *testing.T, jsonStr string) {
	t.Helper()
	original := ratio_setting.ChannelModelPricing2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(original))
	})
	require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(jsonStr))
}

func channelPricingContext(t *testing.T, channelId int) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if channelId > 0 {
		common.SetContextKey(ctx, constant.ContextKeyChannelId, channelId)
	}
	return ctx
}

// Each lane resolves on its own: an operator has to be able to make one
// channel's output dearer without touching its input or cache price.
func TestApplyChannelPricingResolvesLanesIndependently(t *testing.T) {
	withChannelModelPricing(t, `{"lane-test-model":{"7":{"completion_ratio":2}}}`)
	info := &relaycommon.RelayInfo{OriginModelName: "lane-test-model"}

	price := hosttypes.PriceData{ModelRatio: 0.9, CompletionRatio: 0.5, CacheRatio: 0.1}
	applyChannelPricing(channelPricingContext(t, 7), info, "lane-test-model", &price)

	assert.Equal(t, 0.9, price.ModelRatio, "an unset lane keeps the global price")
	assert.Equal(t, 2.0, price.CompletionRatio)
	assert.Equal(t, 0.1, price.CacheRatio, "an unset lane keeps the global price")
}

// Zero is a real price meaning free, which is exactly why the stored lanes are
// pointers rather than a zero-means-unset float.
func TestApplyChannelPricingTreatsZeroAsFreeNotUnset(t *testing.T) {
	withChannelModelPricing(t, `{"lane-test-model":{"7":{"model_ratio":0,"cache_ratio":0}}}`)
	info := &relaycommon.RelayInfo{OriginModelName: "lane-test-model"}

	price := hosttypes.PriceData{ModelRatio: 0.9, CompletionRatio: 0.5, CacheRatio: 0.1}
	applyChannelPricing(channelPricingContext(t, 7), info, "lane-test-model", &price)

	assert.Zero(t, price.ModelRatio)
	assert.Equal(t, 0.5, price.CompletionRatio)
	assert.Zero(t, price.CacheRatio)
}

func TestApplyChannelPricingFallsBackToGlobal(t *testing.T) {
	withChannelModelPricing(t, `{"lane-test-model":{"7":{"model_ratio":3}}}`)
	info := &relaycommon.RelayInfo{OriginModelName: "lane-test-model"}

	cases := map[string]struct {
		channelId int
		model     string
	}{
		"another channel":  {channelId: 8, model: "lane-test-model"},
		"another model":    {channelId: 7, model: "other-model"},
		"no channel known": {channelId: 0, model: "lane-test-model"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			price := hosttypes.PriceData{ModelRatio: 0.9, CompletionRatio: 0.5, CacheRatio: 0.1}
			applyChannelPricing(channelPricingContext(t, tc.channelId), info, tc.model, &price)
			assert.Equal(t, 0.9, price.ModelRatio)
		})
	}
}

// A stored value that would turn a charge into a credit must be refused at save
// time and ignored at request time, never applied.
func TestChannelModelPricingRejectsUnsafeValues(t *testing.T) {
	for name, jsonStr := range map[string]string{
		"negative":          `{"m":{"7":{"model_ratio":-1}}}`,
		"above the ceiling": `{"m":{"7":{"model_ratio":10001}}}`,
		"empty model name":  `{"":{"7":{"model_ratio":2}}}`,
		"invalid channel":   `{"m":{"0":{"model_ratio":2}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ratio_setting.ValidateChannelModelPricingJSONString(jsonStr))
			require.Error(t, ratio_setting.UpdateChannelModelPricingByJSONString(jsonStr))
		})
	}

	// A value that slipped in before the bound existed must not reach quota math.
	withChannelModelPricing(t, `{}`)
	assert.Equal(t, 0.9, ratio_setting.ResolveRatio(ptrFloat(-1), 0.9))
	assert.Equal(t, 0.9, ratio_setting.ResolveRatio(ptrFloat(math.NaN()), 0.9))
	assert.Equal(t, 0.9, ratio_setting.ResolveRatio(nil, 0.9))
	assert.Equal(t, 2.0, ratio_setting.ResolveRatio(ptrFloat(2), 0.9))
}

func ptrFloat(v float64) *float64 { return &v }

// A retry onto another channel must settle at that channel's prices, and
// overrides must not compound across attempts.
func TestRefreshPricingForSelectedChannelReresolvesLanes(t *testing.T) {
	withChannelModelPricing(t, `{"refresh-test-model":{"9":{"model_ratio":4}}}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"refresh-test-model":1}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(`{}`) })

	info := &relaycommon.RelayInfo{OriginModelName: "refresh-test-model", UsingGroup: "default"}
	info.PriceData.ModelRatio = 999

	RefreshPricingForSelectedChannel(channelPricingContext(t, 9), info)
	assert.Equal(t, 4.0, info.PriceData.ModelRatio)

	// Running twice must not stack the override on top of itself.
	RefreshPricingForSelectedChannel(channelPricingContext(t, 9), info)
	assert.Equal(t, 4.0, info.PriceData.ModelRatio)

	// Moving to a channel without an override returns to the global price.
	RefreshPricingForSelectedChannel(channelPricingContext(t, 10), info)
	assert.Equal(t, 1.0, info.PriceData.ModelRatio)
}

// Retained inactive lanes on another channel must not affect affordability.
func TestChannelReservationIgnoresOtherChannelsInactivePrices(t *testing.T) {
	originalRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalRatios)) })
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"current-channel-estimate":1}`))
	withChannelModelPricing(t, `{"current-channel-estimate":{
		"1":{"billing_mode":"per_request","model_price":0.1,"model_ratio":10000},
		"2":{"billing_mode":"tiered_expr","billing_expr":"p","model_ratio":9999},
		"3":{"billing_mode":"per_token","model_ratio":8000}
	}}`)
	info := &relaycommon.RelayInfo{OriginModelName: "current-channel-estimate", UserGroup: "default", UsingGroup: "default"}
	price, err := ModelPriceHelper(channelPricingContext(t, 9), info, 1000, &types.TokenCountMeta{MaxTokens: 100})
	require.NoError(t, err)
	assert.Equal(t, 1100, price.QuotaToPreConsume)
	assert.Equal(t, 1.0, price.ModelRatio)
}

// Pricing is resolved once, before the retry loop, but a retry can land on a
// channel that prices the model in the other shape. Settlement dispatches on
// the snapshot, so the snapshot has to follow the channel.
func TestRefreshPricingFollowsTheChannelAcrossBillingModes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelName = "mixed-mode-model"
	const exprChannel = 5101
	const ratioChannel = 5102
	const channelExpr = `tier("channel", p * 7)`

	originalRatios := ratio_setting.ModelRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"mixed-mode-model":1}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalRatios)) })

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
		_ = ratio_setting.UpdateChannelModelPricingByJSONString("{}")
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"` + modelName + `":"ratio"}`,
		"billing_setting.billing_expr": `{}`,
	}))
	require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(
		`{"`+modelName+`":{"`+strconv.Itoa(exprChannel)+`":{"billing_mode":"tiered_expr","billing_expr":"`+
			`tier(\"channel\", p * 7)"},"`+strconv.Itoa(ratioChannel)+`":{"billing_mode":"ratio","model_ratio":2}}}`))

	newInfo := func(snapshot *billingexpr.BillingSnapshot) *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			OriginModelName:       modelName,
			UserGroup:             "default",
			UsingGroup:            "default",
			RequestHeaders:        map[string]string{"Content-Type": "application/json"},
			BillingRequestInput:   &billingexpr.RequestInput{Headers: map[string]string{}, Body: []byte(`{}`)},
			TieredBillingSnapshot: snapshot,
		}
	}
	contextForChannel := func(channelId int) *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		ctx.Set("group", "default")
		common.SetContextKey(ctx, constant.ContextKeyChannelId, channelId)
		return ctx
	}
	snapshotFor := func(expr string) *billingexpr.BillingSnapshot {
		return &billingexpr.BillingSnapshot{
			BillingMode:               "tiered_expr",
			ModelName:                 modelName,
			ExprString:                expr,
			GroupRatio:                1,
			EstimatedPromptTokens:     100,
			EstimatedCompletionTokens: 50,
			QuotaPerUnit:              common.QuotaPerUnit,
		}
	}

	t.Run("a retry onto a ratio channel drops the expression", func(t *testing.T) {
		// Left in place it would bill this channel with the price of the one
		// that just failed.
		info := newInfo(snapshotFor(channelExpr))
		RefreshPricingForSelectedChannel(contextForChannel(ratioChannel), info)
		assert.Nil(t, info.TieredBillingSnapshot)
		assert.EqualValues(t, 2, info.PriceData.ModelRatio)
	})

	t.Run("a retry onto an expression channel adopts its expression", func(t *testing.T) {
		info := newInfo(nil)
		RefreshPricingForSelectedChannel(contextForChannel(exprChannel), info)
		require.NotNil(t, info.TieredBillingSnapshot)
		assert.Equal(t, channelExpr, info.TieredBillingSnapshot.ExprString)
		assert.Equal(t, "channel", info.TieredBillingSnapshot.EstimatedTier)
	})

	t.Run("staying on the same expression keeps the original estimate", func(t *testing.T) {
		// Rebuilding here would discard the tokens the first attempt priced.
		existing := snapshotFor(channelExpr)
		existing.EstimatedQuotaAfterGroup = 4242
		info := newInfo(existing)
		RefreshPricingForSelectedChannel(contextForChannel(exprChannel), info)
		require.NotNil(t, info.TieredBillingSnapshot)
		assert.EqualValues(t, 4242, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
	})

	t.Run("a channel with no override follows the model", func(t *testing.T) {
		info := newInfo(snapshotFor(channelExpr))
		RefreshPricingForSelectedChannel(contextForChannel(5199), info)
		assert.Nil(t, info.TieredBillingSnapshot, "the model is ratio-billed globally")
	})
}

// The same model can have three pricing shapes simultaneously. Preserve the
// existing legacy ratio meaning while explicit choices take precedence.
func TestChannelPricingExplicitShapesAndAllLanes(t *testing.T) {
	const modelName = "channel-complete-pricing"
	originalPrices := ratio_setting.ModelPrice2JSONString()
	originalRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(originalPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalRatios))
	})
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"channel-complete-pricing":3}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"channel-complete-pricing":1}`))
	withChannelModelPricing(t, `{"channel-complete-pricing":{
		"1":{"billing_mode":"ratio","model_ratio":2},
		"2":{"billing_mode":"per_token","model_ratio":2,"completion_ratio":3,"cache_ratio":0,"create_cache_ratio":4,"image_ratio":5,"audio_ratio":6,"audio_completion_ratio":7},
		"3":{"billing_mode":"per_request","model_price":0},
		"4":{"billing_mode":"per_request"},
		"5":{"billing_mode":"tiered_expr","billing_expr":"tier(\"channel\", p * 2 + c * 8)"}
	}}`)
	for _, test := range []struct {
		name       string
		channel    int
		fixed      bool
		price      float64
		expression bool
	}{
		{"legacy fixed precedence", 1, true, 3, false},
		{"explicit token precedence", 2, false, -1, false},
		{"free fixed channel", 3, true, 0, false},
		{"fixed inheritance", 4, true, 3, false},
		{"expression over fixed global", 5, false, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{OriginModelName: modelName, UserGroup: "default", UsingGroup: "default"}
			price, err := ModelPriceHelper(channelPricingContext(t, test.channel), info, 1000, &types.TokenCountMeta{MaxTokens: 100})
			require.NoError(t, err)
			assert.Equal(t, test.fixed, price.UsePrice)
			assert.Equal(t, test.price, price.ModelPrice)
			assert.Equal(t, test.expression, info.TieredBillingSnapshot != nil)
			if test.channel == 2 {
				assert.Equal(t, 2.0, price.ModelRatio)
				assert.Equal(t, 3.0, price.CompletionRatio)
				assert.Zero(t, price.CacheRatio)
				assert.Equal(t, 4.0, price.CacheCreationRatio)
				assert.Equal(t, 4.0, price.CacheCreation5mRatio)
				assert.InDelta(t, 6.4, price.CacheCreation1hRatio, 0.00001)
				assert.Equal(t, 5.0, price.ImageRatio)
				assert.Equal(t, 6.0, price.AudioRatio)
				assert.Equal(t, 7.0, price.AudioCompletionRatio)
				assert.True(t, price.AudioPricingEnabled)
				// 1,000 input at the worst input lane (6.4) + 100
				// output audio at 6*7, all scaled by model ratio 2.
				assert.GreaterOrEqual(t, price.QuotaToPreConsume, 21200)
			}
		})
	}
}

type channelPricingReservation struct {
	quota int
	err   error
}

func (b *channelPricingReservation) Settle(int) error         { return nil }
func (b *channelPricingReservation) Refund(*gin.Context)      {}
func (b *channelPricingReservation) NeedsRefund() bool        { return false }
func (b *channelPricingReservation) GetPreConsumedQuota() int { return b.quota }
func (b *channelPricingReservation) Reserve(target int) error {
	if b.err != nil {
		return b.err
	}
	if target > b.quota {
		b.quota = target
	}
	return nil
}

func TestChannelPricingRetryRebuildsModesAndReservesBeforeSending(t *testing.T) {
	withChannelModelPricing(t, `{"retry-complete-pricing":{
		"1":{"billing_mode":"per_request","model_price":0.1},
		"2":{"billing_mode":"per_token","model_ratio":2,"create_cache_ratio":3,"audio_ratio":4},
		"3":{"billing_mode":"tiered_expr","billing_expr":"param(\"service_tier\") == \"fast\" ? tier(\"fast\", p * 9 + c * 20) : tier(\"normal\", p)"},
		"4":{"billing_mode":"per_request","model_price":0},
		"5":{"billing_mode":"per_request","model_price":1}
	}}`)
	info := &relaycommon.RelayInfo{
		OriginModelName: "retry-complete-pricing", UserGroup: "default", UsingGroup: "default",
		BillingRequestInput: &billingexpr.RequestInput{Body: []byte(`{"service_tier":"fast"}`)},
	}
	_, err := ModelPriceHelper(channelPricingContext(t, 1), info, 100, &types.TokenCountMeta{MaxTokens: 50, ImagePriceRatio: 2, BillingRatios: map[string]float64{"n": 3}})
	require.NoError(t, err)
	assert.InDelta(t, 0.2, info.PriceData.ModelPrice, 0.000001)
	reservation := &channelPricingReservation{quota: info.PriceData.QuotaToPreConsume}
	info.Billing = reservation
	for _, channel := range []int{2, 3, 1, 4} {
		require.NoError(t, RefreshPricingForSelectedChannel(channelPricingContext(t, channel), info))
		previousReservation := reservation.quota
		require.Nil(t, service.PrepareBillingForSelectedChannel(nil, info))
		assert.GreaterOrEqual(t, reservation.quota, previousReservation)
		assert.Equal(t, reservation.quota, info.FinalPreConsumedQuota)
		switch channel {
		case 2:
			assert.False(t, info.PriceData.UsePrice)
			assert.Nil(t, info.TieredBillingSnapshot)
			assert.Equal(t, 3.0, info.PriceData.CacheCreationRatio)
			assert.Equal(t, 4.0, info.PriceData.AudioRatio)
		case 3:
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, "fast", info.TieredBillingSnapshot.EstimatedTier)
			assert.Equal(t, 50, info.TieredBillingSnapshot.EstimatedCompletionTokens)
			ok, quota, _ := service.TryTieredSettle(info, billingexpr.TokenParams{P: 100, C: 50, Len: 100})
			assert.True(t, ok)
			assert.Equal(t, 950, quota)
		case 1:
			assert.True(t, info.PriceData.UsePrice)
			assert.Nil(t, info.TieredBillingSnapshot)
			assert.InDelta(t, 0.2, info.PriceData.ModelPrice, 0.000001, "image multiplier is applied once after every channel change")
			assert.Equal(t, 3.0, info.PriceData.OtherRatioMultiplier())
		case 4:
			assert.True(t, info.PriceData.UsePrice)
			assert.Zero(t, info.PriceData.ModelPrice)
			assert.Zero(t, info.PriceData.QuotaToPreConsume)
		}
	}
	reservation.err = errors.New("insufficient quota")
	require.NoError(t, RefreshPricingForSelectedChannel(channelPricingContext(t, 5), info))
	assert.NotNil(t, service.PrepareBillingForSelectedChannel(nil, info), "an unfunded retry must stop before upstream execution")
}

func TestChannelPricingMissingFixedPriceAndBadRuntimeExpressionFail(t *testing.T) {
	withChannelModelPricing(t, `{"unpriced-channel-model":{
		"1":{"billing_mode":"per_request"},
		"2":{"billing_mode":"tiered_expr","billing_expr":"param(\"bad\") == true ? -1 : 1"},
		"3":{"billing_mode":"per_token","model_ratio":10000,"audio_ratio":10000,"audio_completion_ratio":10000}
	}}`)
	for _, channel := range []int{1, 2, 3} {
		info := &relaycommon.RelayInfo{OriginModelName: "unpriced-channel-model", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{Body: []byte(`{"bad":true}`)}}
		_, err := ModelPriceHelper(channelPricingContext(t, channel), info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
		require.Error(t, err, "missing, negative or overflowing channel charges cannot be forwarded")
	}
}

func TestTaskChannelPricingFixedAndTokenChoices(t *testing.T) {
	withChannelModelPricing(t, `{"task-channel-price":{"1":{"billing_mode":"per_request","model_price":0},"2":{"billing_mode":"per_token","model_ratio":2}}}`)
	for _, channel := range []int{1, 2} {
		info := &relaycommon.RelayInfo{OriginModelName: "task-channel-price", UserGroup: "default", UsingGroup: "default"}
		price, err := ModelPriceHelperPerCall(channelPricingContext(t, channel), info, info.OriginModelName)
		require.NoError(t, err)
		if channel == 1 {
			assert.True(t, price.UsePrice)
			assert.Zero(t, price.ModelPrice)
			assert.Zero(t, price.Quota)
		} else {
			assert.False(t, price.UsePrice)
			assert.Equal(t, 2.0, price.ModelRatio)
			assert.Equal(t, int(common.QuotaPerUnit), price.Quota)
		}
	}
}
