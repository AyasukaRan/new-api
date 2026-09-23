package billing_setting

import (
	"fmt"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmokeTestTaskExprValidatesDeclaredUsageVectors(t *testing.T) {
	videoSchema := map[string]jsplugin.UsageFieldSchema{
		"seconds": {Type: "number", Unit: "second"},
		"mode":    {Enum: []string{"std", "pro"}},
		"quality": {Enum: []string{"sd", "hd"}},
	}

	tests := []struct {
		name          string
		schema        map[string]jsplugin.UsageFieldSchema
		expression    string
		expectedError string
	}{
		{
			name:          "fixed prices are not task usage prices",
			schema:        videoSchema,
			expression:    `true ? tier("normal", u("seconds") * 0.4) : tier("fixed", fixed(0.01))`,
			expectedError: "fixed pricing is not supported for task usage expressions",
		},
		{
			name:       "declared numeric and enum facts",
			schema:     videoSchema,
			expression: `u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)`,
		},
		{
			name:          "undeclared literal key",
			schema:        videoSchema,
			expression:    `tier("base", u("clips") * 0.1)`,
			expectedError: `usage key "clips" is not declared`,
		},
		{
			name:          "negative duration boundary",
			schema:        videoSchema,
			expression:    fmt.Sprintf(`u("seconds") == %d ? -1 : 0`, relaycommon.MaxTaskDurationSeconds),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative count boundary",
			schema:        map[string]jsplugin.UsageFieldSchema{"clips": {Type: "number", Unit: "count"}},
			expression:    fmt.Sprintf(`u("clips") == %d ? -1 : 0`, dto.MaxImageN),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative token boundary",
			schema:        map[string]jsplugin.UsageFieldSchema{"tokens": {Type: "number", Unit: "token"}},
			expression:    fmt.Sprintf(`u("tokens") == %d ? -1 : 0`, common.MaxQuota),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative credit boundary",
			schema:        map[string]jsplugin.UsageFieldSchema{"units": {Type: "number", Unit: "credit"}},
			expression:    fmt.Sprintf(`u("units") == %d ? -1 : 0`, common.MaxQuota),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative enum combination",
			schema:        videoSchema,
			expression:    `u("mode") == "pro" && u("quality") == "hd" ? -1 : 0`,
			expectedError: "result must be finite and non-negative",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := SmokeTestTaskExpr(testCase.expression, testCase.schema)
			if testCase.expectedError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, testCase.expectedError)
		})
	}
}

func TestSmokeTestTaskExprCapsOversizedEnumProductsAtLastCombination(t *testing.T) {
	schema := make(map[string]jsplugin.UsageFieldSchema, 7)
	condition := ""
	for index := range 7 {
		schema[fmt.Sprintf("enum_%d", index)] = jsplugin.UsageFieldSchema{Enum: []string{"first", "middle", "last"}}
		if condition != "" {
			condition += " && "
		}
		condition += fmt.Sprintf(`u("enum_%d") == "last"`, index)
	}

	err := SmokeTestTaskExpr(condition+" ? -1 : 0", schema)
	require.ErrorContains(t, err, "result must be finite and non-negative")
}

func TestSmokeTestExprRejectsTaskUsageWithoutSchema(t *testing.T) {
	err := SmokeTestExpr(`u("mode") == "std" ? 1 : 2`)
	require.Error(t, err)
	assert.ErrorContains(t, err, "mode")
	assert.ErrorContains(t, err, "no task plugin usage schema")

	require.NoError(t, SmokeTestExpr(`tier("base", p * 2 + c * 8)`))
}

// The same model can be served by upstreams that charge in different shapes, so
// the mode has to be resolved per channel rather than once per model.
func TestChannelBillingModeOverridesTheModelsGlobalMode(t *testing.T) {
	const model = "shared-model"
	const exprChannel = 4101
	const ratioChannel = 4102
	const channelExpr = `tier("base", p * 9 + c * 9)`

	require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(fmt.Sprintf(
		`{"%s":{"%d":{"billing_mode":"tiered_expr","billing_expr":%q},"%d":{"billing_mode":"ratio","model_ratio":2}}}`,
		model, exprChannel, channelExpr, ratioChannel)))
	t.Cleanup(func() {
		_ = ratio_setting.UpdateChannelModelPricingByJSONString("{}")
		billingSetting.BillingMode = map[string]string{}
		billingSetting.BillingExpr = map[string]string{}
	})

	t.Run("a channel pinned to expressions bills with its own", func(t *testing.T) {
		assert.Equal(t, BillingModeTieredExpr, GetChannelBillingMode(model, exprChannel))
		expr, ok := GetChannelBillingExpr(model, exprChannel)
		require.True(t, ok)
		assert.Equal(t, channelExpr, expr)
	})

	t.Run("a channel pinned to ratios bills by ratio on the same model", func(t *testing.T) {
		assert.Equal(t, BillingModeRatio, GetChannelBillingMode(model, ratioChannel))
		_, ok := GetChannelBillingExpr(model, ratioChannel)
		assert.False(t, ok, "a ratio channel must not inherit an expression")
	})

	t.Run("a channel that pins nothing follows the model", func(t *testing.T) {
		billingSetting.BillingMode = map[string]string{model: BillingModeTieredExpr}
		billingSetting.BillingExpr = map[string]string{model: `tier("base", p * 1)`}
		assert.Equal(t, BillingModeTieredExpr, GetChannelBillingMode(model, 4103))
		expr, ok := GetChannelBillingExpr(model, 4103)
		require.True(t, ok)
		assert.Equal(t, `tier("base", p * 1)`, expr)
	})

	t.Run("a pinned ratio channel is not dragged back by the global mode", func(t *testing.T) {
		// Without the per-channel check this channel would bill with the
		// model's expression, which is the price of a different upstream.
		billingSetting.BillingMode = map[string]string{model: BillingModeTieredExpr}
		billingSetting.BillingExpr = map[string]string{model: `tier("base", p * 1)`}
		assert.Equal(t, BillingModeRatio, GetChannelBillingMode(model, ratioChannel))
	})
}

func TestChannelBillingOverrideRejectsWhatCannotBeBilled(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"an expression channel with no expression", `{"m":{"7":{"billing_mode":"tiered_expr"}}}`},
		{"an expression that does not compile", `{"m":{"7":{"billing_mode":"tiered_expr","billing_expr":"tier(\"a\", p *"}}}`},
		{"an expression that can return a negative price", `{"m":{"7":{"billing_mode":"tiered_expr","billing_expr":"tier(\"a\", p * -5)"}}}`},
		{"an unknown mode", `{"m":{"7":{"billing_mode":"per_call"}}}`},
		{"an expression on a ratio channel", `{"m":{"7":{"billing_mode":"ratio","billing_expr":"tier(\"a\", p * 1)"}}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name+" is refused", func(t *testing.T) {
			structural := ratio_setting.ValidateChannelModelPricingJSONString(testCase.payload)
			semantic := ValidateChannelBillingExpressions(testCase.payload)
			assert.True(t, structural != nil || semantic != nil,
				"save-time validation accepted a pricing override that cannot bill")
		})
	}

	t.Run("a usable expression is accepted", func(t *testing.T) {
		payload := `{"m":{"7":{"billing_mode":"tiered_expr","billing_expr":"tier(\"base\", p * 3 + c * 6)"}}}`
		require.NoError(t, ratio_setting.ValidateChannelModelPricingJSONString(payload))
		require.NoError(t, ValidateChannelBillingExpressions(payload))
	})
}

func TestChannelPricingStoresExplicitModesAndFreeLanes(t *testing.T) {
	previous := ratio_setting.ChannelModelPricing2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(previous)) })

	for _, mode := range []string{"ratio", "per_token", "per_request"} {
		t.Run(mode, func(t *testing.T) {
			payload := fmt.Sprintf(`{"channel-storage":{"7":{"billing_mode":%q,"model_price":0,"model_ratio":0,"completion_ratio":0,"cache_ratio":0,"create_cache_ratio":0,"image_ratio":0,"audio_ratio":0,"audio_completion_ratio":0}}}`, mode)
			require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(payload))
			pricing, ok := ratio_setting.GetChannelModelPricing("channel-storage", 7)
			require.True(t, ok)
			assert.False(t, pricing.IsEmpty())
			assert.JSONEq(t, payload, ratio_setting.ChannelModelPricing2JSONString())
			assert.Equal(t, mode, GetChannelBillingMode("channel-storage", 7))
			_, hasExpression := GetChannelBillingExpr("channel-storage", 7)
			assert.False(t, hasExpression)
		})
	}

	for _, lane := range []string{"model_price", "create_cache_ratio", "image_ratio", "audio_ratio", "audio_completion_ratio"} {
		t.Run(lane+" alone is an override", func(t *testing.T) {
			payload := fmt.Sprintf(`{"channel-storage":{"7":{%q:0}}}`, lane)
			require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(payload))
			_, found := ratio_setting.GetChannelModelPricing("channel-storage", 7)
			assert.True(t, found)
			assert.JSONEq(t, payload, ratio_setting.ChannelModelPricing2JSONString())
		})
	}
	require.NoError(t, ratio_setting.ValidateChannelModelPricingJSONString(`{"channel-storage":{"7":{"billing_mode":"per_request"}}}`))
	require.NoError(t, ratio_setting.ValidateChannelModelPricingJSONString(`{"channel-storage":{"7":{"billing_mode":"per_token","model_price":2,"model_ratio":1}}}`))
}

func TestChannelPricingValidatesEveryPriceLane(t *testing.T) {
	for _, lane := range []string{"model_ratio", "completion_ratio", "cache_ratio", "create_cache_ratio", "image_ratio", "audio_ratio", "audio_completion_ratio", "model_price"} {
		maximum := 10000.0
		if lane == "model_price" {
			maximum = 1000
		}
		for _, value := range []float64{0, maximum, -0.1, maximum + 0.1} {
			t.Run(fmt.Sprintf("%s=%g", lane, value), func(t *testing.T) {
				payload := fmt.Sprintf(`{"m":{"7":{%q:%g}}}`, lane, value)
				err := ratio_setting.ValidateChannelModelPricingJSONString(payload)
				if value >= 0 && value <= maximum {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, lane)
				}
			})
		}
		t.Run(lane+" overflow", func(t *testing.T) {
			require.Error(t, ratio_setting.ValidateChannelModelPricingJSONString(fmt.Sprintf(`{"m":{"7":{%q:1e309}}}`, lane)))
		})
	}
	for _, mode := range []string{"per_token", "per_request"} {
		t.Run(mode+" rejects an expression", func(t *testing.T) {
			require.Error(t, ratio_setting.ValidateChannelModelPricingJSONString(fmt.Sprintf(`{"m":{"7":{"billing_mode":%q,"billing_expr":"p"}}}`, mode)))
		})
	}
}

func TestChannelFixedPricePreservesInheritanceAndRejectsUnsafeStoredValues(t *testing.T) {
	assert.Equal(t, 2.5, ratio_setting.ResolveModelPrice(nil, 2.5))
	for _, testCase := range []struct {
		name  string
		value float64
		want  float64
	}{
		{"explicit free", 0, 0},
		{"upper boundary", 1000, 1000},
		{"negative", -1, 2.5},
		{"over boundary", 1000.1, 2.5},
		{"non-finite", math.Inf(1), 2.5},
		{"not a number", math.NaN(), 2.5},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, ratio_setting.ResolveModelPrice(&testCase.value, 2.5))
		})
	}
}

func TestChannelTaskBillingUsesDeclaredUsageSchema(t *testing.T) {
	previous := jsplugin.DefaultRegistry
	jsplugin.DefaultRegistry = jsplugin.NewRegistry()
	t.Cleanup(func() { jsplugin.DefaultRegistry = previous })
	_, err := jsplugin.DefaultRegistry.Register(`
export const meta = {
  apiVersion: 1, key: "channel-task-pricing", name: "Channel Task Pricing", version: "1.0.0", author: {name: "Test"},
  models: ["channel-task"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}, mode: {enum: ["std", "pro"]}}
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, jsplugin.Options{})
	require.NoError(t, err)

	for _, testCase := range []struct {
		name          string
		model         string
		expression    string
		expectedError string
	}{
		{"declared usage", "channel-task", `u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("base", u("seconds") * 0.4)`, ""},
		{"undeclared usage", "channel-task", `tier("base", u("clips") * 0.4)`, `usage key "clips" is not declared`},
		{"negative task boundary", "channel-task", fmt.Sprintf(`u("seconds") == %d ? -1 : 0`, relaycommon.MaxTaskDurationSeconds), "result must be finite and non-negative"},
		{"ordinary model has no schema", "ordinary-model", `tier("base", u("seconds") * 0.4)`, "no task plugin usage schema"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload, marshalErr := common.Marshal(map[string]map[int]ratio_setting.ChannelModelPricing{
				testCase.model: {7: {BillingMode: "tiered_expr", BillingExpr: testCase.expression}},
			})
			require.NoError(t, marshalErr)
			err := ValidateChannelBillingExpressions(string(payload))
			if testCase.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, testCase.expectedError)
				assert.ErrorContains(t, err, "channel 7")
			}
		})
	}

	t.Run("mapped model resolves the task schema through the supplied lookup", func(t *testing.T) {
		payload := `{"task-alias":{"7":{"billing_mode":"tiered_expr","billing_expr":"tier(\"base\", u(\"seconds\") * 0.4)"}}}`
		require.ErrorContains(t, ValidateChannelBillingExpressions(payload), "no task plugin usage schema")
		plugin, found := jsplugin.DefaultRegistry.Generation().GetByModel("channel-task")
		require.True(t, found)
		err := ValidateChannelBillingExpressionsWithSchema(payload, func(name string) (map[string]jsplugin.UsageFieldSchema, bool) {
			assert.Equal(t, "task-alias", name)
			return plugin.Meta.UsageSchema, true
		})
		require.NoError(t, err)
	})
}
