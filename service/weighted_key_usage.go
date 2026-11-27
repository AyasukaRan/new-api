package service

import (
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/keyusage"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const keyRoutingEstimatedTokensContext = "key_routing_estimated_tokens"

// MarkKeyRoutingTokensEstimated records local estimation for this attempt only;
// the legacy billing flag retains its existing request-level semantics.
func MarkKeyRoutingTokensEstimated(c *gin.Context) {
	c.Set(keyRoutingEstimatedTokensContext, true)
}

// KeyRoutingEstimate is independent of wallet reservations and group pricing.
type KeyRoutingEstimate struct {
	InputTokens  int64
	OutputTokens int64
	BaseCostUSD  *float64
}

type keyRoutingTokenEstimate struct {
	Request      dto.Request
	InputTokens  int64
	OutputTokens int64
}

func (estimate KeyRoutingEstimate) CostUSD(ratio *float64) *float64 {
	if estimate.BaseCostUSD == nil || math.IsNaN(*estimate.BaseCostUSD) || math.IsInf(*estimate.BaseCostUSD, 0) || *estimate.BaseCostUSD < 0 || ratio == nil || math.IsNaN(*ratio) || math.IsInf(*ratio, 0) || *ratio < 0 || *ratio > 10000 {
		return nil
	}
	cost := *estimate.BaseCostUSD * *ratio
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || cost > keyusage.MaxCostUSD || common.QuotaPerUnit > 0 && cost > float64(common.MaxQuota)/common.QuotaPerUnit {
		return nil
	}
	return &cost
}

// EstimateKeyRoutingUsage counts the request even when billing token estimation
// is disabled. No billing snapshot, charge or pre-consumption value is changed.
func EstimateKeyRoutingUsage(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) (KeyRoutingEstimate, error) {
	if info != nil && request == nil && info.RelayFormat == relaytypes.RelayFormatOpenAIRealtime {
		// The session's messages arrive only after the socket is selected.
		return KeyRoutingEstimate{OutputTokens: 1024}, nil
	}
	if c == nil || info == nil || request == nil {
		return KeyRoutingEstimate{}, errors.New("missing request for key usage estimation")
	}
	const cacheKey = "key_routing_token_estimate"
	cached, _ := c.Get(cacheKey)
	tokens, _ := cached.(*keyRoutingTokenEstimate)
	if tokens == nil || tokens.Request != request {
		meta := request.GetTokenCountMeta()
		// The counter's context metadata belongs to this estimate, not billing.
		input, err := CountRequestToken(c.Copy(), meta, info)
		if err != nil {
			return KeyRoutingEstimate{}, err
		}
		tokens = &keyRoutingTokenEstimate{Request: request, InputTokens: int64(min(max(input, 0), common.MaxQuota)), OutputTokens: 1024}
		if meta.MaxTokens > 0 {
			tokens.OutputTokens = min(tokens.OutputTokens, int64(meta.MaxTokens))
		}
		// Embeddings and rerank requests do not generate completion tokens.
		var outputLimits []*uint
		switch typed := request.(type) {
		case *dto.EmbeddingRequest, *dto.GeminiEmbeddingRequest, *dto.GeminiBatchEmbeddingRequest, *dto.RerankRequest:
			tokens.OutputTokens = 0
		case *dto.GeneralOpenAIRequest:
			outputLimits = []*uint{typed.MaxTokens, typed.MaxCompletionTokens}
		case *dto.OpenAIResponsesRequest:
			outputLimits = []*uint{typed.MaxOutputTokens}
		case *dto.ClaudeRequest:
			outputLimits = []*uint{typed.MaxTokens}
		case *dto.GeminiChatRequest:
			outputLimits = []*uint{typed.GenerationConfig.MaxOutputTokens}
		}
		for _, limit := range outputLimits {
			if limit != nil {
				tokens.OutputTokens = min(tokens.OutputTokens, int64(min(*limit, uint(1024))))
			}
		}
		c.Set(cacheKey, tokens)
	}
	estimate := KeyRoutingEstimate{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens}
	if snapshot := info.TieredBillingSnapshot; snapshot != nil {
		if snapshot.TaskUsageBilling {
			return estimate, nil
		}
		requestInput := billingexpr.RequestInput{}
		if info.BillingRequestInput != nil {
			requestInput = *info.BillingRequestInput
		}
		if info.BillingImageCount != nil {
			requestInput.ImageCount = info.BillingImageCount
		} else if snapshot.EstimatedImageCount != nil {
			requestInput.ImageCount = snapshot.EstimatedImageCount
		}
		if info.BillingMeteredUsage != nil {
			requestInput.MeteredUsage = info.BillingMeteredUsage
		} else if snapshot.EstimatedMeteredUsage != nil {
			requestInput.MeteredUsage = snapshot.EstimatedMeteredUsage
		}
		raw, _, runErr := billingexpr.RunExprByHashWithRequest(snapshot.ExprString, snapshot.ExprHash, billingexpr.TokenParams{
			P: float64(estimate.InputTokens), C: float64(estimate.OutputTokens), Len: float64(estimate.InputTokens),
		}, requestInput)
		if runErr == nil && !math.IsNaN(raw) && !math.IsInf(raw, 0) && raw >= 0 {
			cost := raw / 1_000_000
			estimate.BaseCostUSD = &cost
		}
		return estimate, nil
	}
	if info.PricingTokenCountMeta == nil || !(common.QuotaPerUnit > 0) || math.IsInf(common.QuotaPerUnit, 0) {
		return estimate, nil
	}
	price := info.PriceData
	cost := price.ModelPrice
	if !price.UsePrice {
		cost = (float64(estimate.InputTokens) + float64(estimate.OutputTokens)*price.CompletionRatio) * price.ModelRatio / common.QuotaPerUnit
	}
	cost = price.ApplyOtherRatiosToFloat(cost)
	if !math.IsNaN(cost) && !math.IsInf(cost, 0) && cost >= 0 {
		estimate.BaseCostUSD = &cost
	}
	return estimate, nil
}

// observeKeyRoutingUsage accumulates measured settlement events for one attempt.
// Unknown usage leaves the reservation releasable; it is never promoted to actual.
func observeKeyRoutingUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, baseQuota *float64) {
	if info == nil || info.KeyUsageReservation == nil || info.IsChannelTest || usage == nil {
		return
	}
	estimated := c.GetBool(keyRoutingEstimatedTokensContext) || usage.BillingUsage != nil && usage.BillingUsage.Estimated
	canonical := effectiveBillingUsage(usage)
	input := int64(min(max(canonical.PromptTokens, 0), common.MaxQuota))
	output := int64(min(max(canonical.CompletionTokens, 0), common.MaxQuota))
	if usageSemanticFromUsage(info, canonical) == dto.BillingUsageSemanticAnthropic {
		cacheRead := int64(min(max(canonical.PromptTokensDetails.CachedTokens, 0), common.MaxQuota))
		cacheWrite := int64(min(max(canonical.PromptTokensDetails.CacheCreationTokensTotal(), 0), common.MaxQuota))
		splitWrite := int64(min(max(canonical.ClaudeCacheCreation5mTokens, 0), common.MaxQuota)) + int64(min(max(canonical.ClaudeCacheCreation1hTokens, 0), common.MaxQuota))
		input += cacheRead + max(cacheWrite, splitWrite)
	} else if canonical.PromptTokens == 0 && canonical.InputTokens > 0 {
		input = int64(min(canonical.InputTokens, common.MaxQuota))
	}
	input = min(input, common.MaxQuota)
	if output == 0 && canonical.OutputTokens > 0 {
		output = int64(min(canonical.OutputTokens, common.MaxQuota))
	}
	var cost *float64
	if baseQuota != nil && info.QuotaClamp == nil && common.QuotaPerUnit > 0 && !math.IsInf(common.QuotaPerUnit, 0) {
		baseUSD := *baseQuota / common.QuotaPerUnit
		cost = (KeyRoutingEstimate{BaseCostUSD: &baseUSD}).CostUSD(info.UpstreamCostRatio)
	}
	observation := info.KeyUsageObservation
	if observation == nil {
		info.KeyUsageObservation = &keyusage.Observation{InputTokens: input, OutputTokens: output, CostUSD: cost, TokensEstimated: estimated}
		return
	}
	observation.InputTokens += min(input, common.MaxQuota-observation.InputTokens)
	observation.OutputTokens += min(output, common.MaxQuota-observation.OutputTokens)
	observation.TokensEstimated = observation.TokensEstimated || estimated
	if observation.CostUSD == nil || cost == nil || math.IsInf(*observation.CostUSD+*cost, 0) || *observation.CostUSD+*cost > keyusage.MaxCostUSD {
		observation.CostUSD = nil
	} else {
		total := *observation.CostUSD + *cost
		observation.CostUSD = &total
	}
}

// AppendKeyRoutingMetadata lets an asynchronously reconciled supplier bill find
// its original attempt, including failed attempts and reordered credentials.
func AppendKeyRoutingMetadata(info *relaycommon.RelayInfo, other *model.LogOther) {
	if info == nil || info.KeyUsageReservation == nil || other == nil || info.IsChannelTest {
		return
	}
	reservation := info.KeyUsageReservation
	other.SetAdmin("key_routing", map[string]any{
		"id": reservation.ID, "channel_id": reservation.ChannelID,
		"key_identity": reservation.KeyIdentity, "started_at": reservation.StartedAt,
	})
}
