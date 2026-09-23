package ratio_setting

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/types"
)

// Billing modes a channel may pin. They mirror billing_setting's constants,
// which this package cannot import: billing_setting depends on it.
const (
	ChannelBillingModeRatio      = "ratio"
	ChannelBillingModePerToken   = "per_token"
	ChannelBillingModePerRequest = "per_request"
	ChannelBillingModeTieredExpr = "tiered_expr"
)

// MaxChannelPricingRatio bounds an operator-typed lane override. The value
// reaches quota arithmetic, so it needs a ceiling well below anything that
// could saturate a quota conversion.
const MaxChannelPricingRatio = 10000

// MaxChannelModelPrice bounds an explicit USD charge per request. Group and
// usage multipliers still pass through the common saturating quota helpers.
const MaxChannelModelPrice = 1000

// ChannelModelPricing overrides the global per-lane prices of one model on one
// channel, so the same model can cost different amounts depending on which
// upstream served it.
//
// A nil lane inherits the global value. An explicit 0 is a real price meaning
// "free", which is why these are pointers rather than a zero-means-unset
// float: an operator must be able to make one channel's output free without
// that being indistinguishable from leaving the field blank.
type ChannelModelPricing struct {
	ModelPrice           *float64 `json:"model_price,omitempty"`
	ModelRatio           *float64 `json:"model_ratio,omitempty"`
	CompletionRatio      *float64 `json:"completion_ratio,omitempty"`
	CacheRatio           *float64 `json:"cache_ratio,omitempty"`
	CreateCacheRatio     *float64 `json:"create_cache_ratio,omitempty"`
	ImageRatio           *float64 `json:"image_ratio,omitempty"`
	AudioRatio           *float64 `json:"audio_ratio,omitempty"`
	AudioCompletionRatio *float64 `json:"audio_completion_ratio,omitempty"`
	// BillingMode lets one channel price a model by expression while another
	// prices the same model by token ratio. Empty inherits the model's global
	// mode, which is how every channel behaved before this existed. Legacy ratio
	// keeps fixed-price precedence; per_token and per_request explicitly choose
	// the shape. Inactive price fields are retained for later mode switches.
	BillingMode string `json:"billing_mode,omitempty"`
	// BillingExpr is the expression this channel bills with. It is only read
	// when BillingMode selects expression billing, and it is required then:
	// falling back to the global expression would silently bill a channel at a
	// price the operator did not choose for it.
	BillingExpr string `json:"billing_expr,omitempty"`
}

// IsEmpty reports whether the entry overrides nothing, in which case it should
// not be stored at all.
func (p ChannelModelPricing) IsEmpty() bool {
	return p.ModelPrice == nil && p.ModelRatio == nil && p.CompletionRatio == nil && p.CacheRatio == nil &&
		p.CreateCacheRatio == nil && p.ImageRatio == nil && p.AudioRatio == nil && p.AudioCompletionRatio == nil &&
		strings.TrimSpace(p.BillingMode) == "" && strings.TrimSpace(p.BillingExpr) == ""
}

// channelModelPricingMap is keyed model name -> channel id -> overrides.
// Model-outer matches how the pricing UI edits (one model at a time) and keeps
// the option sparse: only explicit overrides are ever written.
var channelModelPricingMap = types.NewRWMap[string, map[int]ChannelModelPricing]()

// GetChannelModelPricing resolves the override for a model on a channel.
//
// It tries the model name as configured and then the wildcard-normalized form,
// because every global ratio getter normalizes through FormatMatchingModelName
// while the pricing UI stores whatever name the admin typed. Without both
// lookups an override on, say, a Gemini wildcard entry would silently never
// match the request that it was configured for.
func GetChannelModelPricing(modelName string, channelId int) (ChannelModelPricing, bool) {
	if channelId <= 0 || modelName == "" {
		return ChannelModelPricing{}, false
	}
	if pricing, ok := lookupChannelModelPricing(modelName, channelId); ok {
		return pricing, true
	}
	normalized := FormatMatchingModelName(modelName)
	if normalized == modelName {
		return ChannelModelPricing{}, false
	}
	return lookupChannelModelPricing(normalized, channelId)
}

func lookupChannelModelPricing(modelName string, channelId int) (ChannelModelPricing, bool) {
	byChannel, ok := channelModelPricingMap.Get(modelName)
	if !ok {
		return ChannelModelPricing{}, false
	}
	pricing, ok := byChannel[channelId]
	if !ok || pricing.IsEmpty() {
		return ChannelModelPricing{}, false
	}
	return pricing, true
}

// ResolveRatio returns the lane value to bill with: the override when it is
// present and safe, otherwise the global value. An unsafe stored value is
// ignored rather than applied, so a hand-edited option row can never turn a
// charge into a credit.
func ResolveRatio(override *float64, global float64) float64 {
	if override == nil || !isValidChannelPricingRatio(*override) {
		return global
	}
	return *override
}

// ResolveModelPrice applies a valid fixed USD override, including an explicit
// zero. Missing or unsafe stored overrides leave the global price unchanged.
func ResolveModelPrice(override *float64, global float64) float64 {
	if override == nil || !(*override >= 0 && *override <= MaxChannelModelPrice) {
		return global
	}
	return *override
}

func isValidChannelPricingRatio(ratio float64) bool {
	// NaN fails every comparison, so the range test covers it.
	return ratio >= 0 && ratio <= MaxChannelPricingRatio
}

func ChannelModelPricing2JSONString() string {
	return channelModelPricingMap.MarshalJSONString()
}

func UpdateChannelModelPricingByJSONString(jsonStr string) error {
	if err := ValidateChannelModelPricingJSONString(jsonStr); err != nil {
		return err
	}
	return types.LoadFromJsonStringWithCallback(channelModelPricingMap, jsonStr, InvalidateExposedDataCache)
}

// GetChannelModelPricingCopy returns a detached snapshot for the settings API.
func GetChannelModelPricingCopy() map[string]map[int]ChannelModelPricing {
	return channelModelPricingMap.ReadAll()
}

// ValidateChannelModelPricingJSONString rejects at save time what the request
// path would otherwise silently ignore, so an operator sees the mistake instead
// of wondering why the override never applied.
func ValidateChannelModelPricingJSONString(jsonStr string) error {
	if jsonStr == "" {
		return nil
	}
	var parsed map[string]map[int]ChannelModelPricing
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return err
	}
	for modelName, byChannel := range parsed {
		if modelName == "" {
			return fmt.Errorf("channel pricing contains an empty model name")
		}
		for channelId, pricing := range byChannel {
			if channelId <= 0 {
				return fmt.Errorf("invalid channel id %d for model %s", channelId, modelName)
			}
			for lane, value := range map[string]*float64{
				"model_ratio":            pricing.ModelRatio,
				"completion_ratio":       pricing.CompletionRatio,
				"cache_ratio":            pricing.CacheRatio,
				"create_cache_ratio":     pricing.CreateCacheRatio,
				"image_ratio":            pricing.ImageRatio,
				"audio_ratio":            pricing.AudioRatio,
				"audio_completion_ratio": pricing.AudioCompletionRatio,
			} {
				if value == nil {
					continue
				}
				if math.IsNaN(*value) || !isValidChannelPricingRatio(*value) {
					return fmt.Errorf("invalid %s for model %s on channel %d: %v (must be between 0 and %d)",
						lane, modelName, channelId, *value, MaxChannelPricingRatio)
				}
			}
			if price := pricing.ModelPrice; price != nil && !(*price >= 0 && *price <= MaxChannelModelPrice) {
				return fmt.Errorf("invalid model_price for model %s on channel %d: %v (must be between 0 and %d)",
					modelName, channelId, *price, MaxChannelModelPrice)
			}
			if err := validateChannelBillingMode(modelName, channelId, pricing); err != nil {
				return err
			}
		}
	}
	return nil
}

// ChannelBillingMode reports how a channel prices a model: the override when
// one is configured, otherwise empty so the caller falls back to the model's
// global mode.
func ChannelBillingMode(modelName string, channelId int) (mode string, expr string, ok bool) {
	pricing, found := GetChannelModelPricing(modelName, channelId)
	if !found {
		return "", "", false
	}
	mode = strings.TrimSpace(pricing.BillingMode)
	if mode == "" {
		return "", "", false
	}
	return mode, strings.TrimSpace(pricing.BillingExpr), true
}

// validateChannelBillingMode rejects a per-channel billing override that could
// not be billed with. An expression is compiled here rather than at first use
// because the alternative is discovering it mid-request, after the upstream
// call has already been paid for.
func validateChannelBillingMode(modelName string, channelId int, pricing ChannelModelPricing) error {
	mode := strings.TrimSpace(pricing.BillingMode)
	expr := strings.TrimSpace(pricing.BillingExpr)
	switch mode {
	case "":
		if expr != "" {
			return fmt.Errorf("model %s on channel %d has a billing expression but no billing mode", modelName, channelId)
		}
		return nil
	case ChannelBillingModeRatio, ChannelBillingModePerToken, ChannelBillingModePerRequest:
		if expr != "" {
			return fmt.Errorf("model %s on channel %d bills by %s and must not carry an expression", modelName, channelId, mode)
		}
		return nil
	case ChannelBillingModeTieredExpr:
		if expr == "" {
			return fmt.Errorf("model %s on channel %d bills by expression but has none", modelName, channelId)
		}
		if _, err := billingexpr.CompileFromCache(expr); err != nil {
			return fmt.Errorf("invalid billing expression for model %s on channel %d: %w", modelName, channelId, err)
		}
		return nil
	default:
		return fmt.Errorf("invalid billing mode %q for model %s on channel %d", pricing.BillingMode, modelName, channelId)
	}
}

// ChannelBillingExprEntry names one per-channel expression in a pricing payload.
type ChannelBillingExprEntry struct {
	Model     string
	ChannelId int
	Expr      string
}

// ChannelBillingExpressions lists the expressions a pricing payload would
// install, so a caller that owns expression semantics can validate them without
// this package reaching back into it.
func ChannelBillingExpressions(jsonStr string) []ChannelBillingExprEntry {
	if jsonStr == "" {
		return nil
	}
	var parsed map[string]map[int]ChannelModelPricing
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return nil
	}
	var entries []ChannelBillingExprEntry
	for modelName, byChannel := range parsed {
		for channelId, pricing := range byChannel {
			if strings.TrimSpace(pricing.BillingMode) != ChannelBillingModeTieredExpr {
				continue
			}
			if expr := strings.TrimSpace(pricing.BillingExpr); expr != "" {
				entries = append(entries, ChannelBillingExprEntry{Model: modelName, ChannelId: channelId, Expr: expr})
			}
		}
	}
	return entries
}
