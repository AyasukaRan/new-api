package helper

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func modelPriceNotConfiguredError(modelName string, userId int) error {
	if model.IsAdmin(userId) {
		return fmt.Errorf(
			"模型 %s 的价格未配置。请前往「系统设置 → 运营设置」开启自用模式，或在「系统设置 → 分组与模型定价设置」中为该模型配置价格；"+
				"Model %s price not configured. Go to System Settings → Operation Settings to enable self-use mode, or configure the model price in System Settings → Group & Model Pricing.",
			modelName, modelName,
		)
	}
	return fmt.Errorf(
		"模型 %s 的价格尚未由管理员配置，暂时无法使用，请联系站点管理员开启该模型；"+
			"Model %s has not been priced by the administrator yet. Please contact the site administrator to enable this model.",
		modelName, modelName,
	)
}

// https://docs.claude.com/en/docs/build-with-claude/prompt-caching#1-hour-cache-duration
const claudeCacheCreation1hMultiplier = 6 / 3.75

// defaultTieredPreConsumeMaxTokens is the fallback completion-token estimate
// used for tiered expression pre-consume when the client omits max_tokens, so
// the pre-consumed quota still reflects a plausible output cost in paid groups.
const defaultTieredPreConsumeMaxTokens = 8192

// servingChannelId reports which channel will actually run the request.
//
// A pinned channel wins over the context: ApplyChannelPin and ResolveOriginTask
// bind the request to a channel without rewriting the channel context keys, so
// on those paths the context still describes whichever channel the distributor
// happened to pick first. Returns 0 when no channel is known yet, which callers
// must treat as "no override".
func servingChannelId(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) int {
	// LockedChannel is promoted through the embedded *TaskRelayInfo, which is
	// nil on every non-task relay.
	if relayInfo != nil && relayInfo.TaskRelayInfo != nil {
		if locked, ok := relayInfo.LockedChannel.(*model.Channel); ok && locked != nil {
			return locked.Id
		}
	}
	if ctx == nil {
		return 0
	}
	// Not relayInfo.ChannelId: ChannelMeta is a nil embedded pointer until a
	// handler calls InitChannelMeta, which happens after pricing.
	return common.GetContextKeyInt(ctx, constant.ContextKeyChannelId)
}

// applyChannelPricing resolves every token lane independently. These values are
// frozen in PriceData for settlement; no settlement path should reread a global
// lane after a channel-specific value has been selected.
func applyChannelPricing(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, billingModelName string, price *hosttypes.PriceData) {
	pricing, ok := ratio_setting.GetChannelModelPricing(billingModelName, servingChannelId(ctx, relayInfo))
	if !ok {
		return
	}
	price.ChannelPricing = true
	price.ModelRatio = ratio_setting.ResolveRatio(pricing.ModelRatio, price.ModelRatio)
	price.CompletionRatio = ratio_setting.ResolveRatio(pricing.CompletionRatio, price.CompletionRatio)
	price.CacheRatio = ratio_setting.ResolveRatio(pricing.CacheRatio, price.CacheRatio)
	price.CacheCreationRatio = ratio_setting.ResolveRatio(pricing.CreateCacheRatio, price.CacheCreationRatio)
	price.CacheCreation5mRatio = price.CacheCreationRatio
	price.CacheCreation1hRatio = price.CacheCreationRatio * claudeCacheCreation1hMultiplier
	price.ImageRatio = ratio_setting.ResolveRatio(pricing.ImageRatio, price.ImageRatio)
	price.AudioRatio = ratio_setting.ResolveRatio(pricing.AudioRatio, price.AudioRatio)
	price.AudioCompletionRatio = ratio_setting.ResolveRatio(pricing.AudioCompletionRatio, price.AudioCompletionRatio)
	price.AudioPricingEnabled = price.AudioPricingEnabled || pricing.AudioRatio != nil || pricing.AudioCompletionRatio != nil
}

// resolveChannelModelPrice preserves the legacy ratio mode's ModelPrice
// precedence. Explicit per_token/per_request choices override that precedence.
func resolveChannelModelPrice(ctx *gin.Context, info *relaycommon.RelayInfo, modelName string, task bool) (float64, bool, error) {
	mode := billing_setting.GetChannelBillingMode(modelName, servingChannelId(ctx, info))
	if mode == ratio_setting.ChannelBillingModePerToken {
		return -1, false, nil
	}
	price, configured := ratio_setting.GetModelPrice(modelName, false)
	if !configured && task {
		price, configured = ratio_setting.GetDefaultModelPriceMap()[modelName]
	}
	if override, ok := ratio_setting.GetChannelModelPricing(modelName, servingChannelId(ctx, info)); ok && override.ModelPrice != nil {
		price = ratio_setting.ResolveModelPrice(override.ModelPrice, -1)
		configured = price >= 0
	}
	if mode == ratio_setting.ChannelBillingModePerRequest && !configured {
		return 0, false, fmt.Errorf("model %s is configured as per_request but has no fixed price", modelName)
	}
	if configured && (price < 0 || math.IsNaN(price) || math.IsInf(price, 0)) {
		return 0, false, fmt.Errorf("model %s has an invalid fixed price", modelName)
	}
	return price, configured, nil
}

// RefreshPricingForSelectedChannel rebuilds the full effective pricing shape
// before a retry. Failure is returned before sending upstream, never replaced
// with the failed channel's expression or stale fixed price.
func RefreshPricingForSelectedChannel(ctx *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil {
		return nil
	}
	previous := info.TieredBillingSnapshot
	meta := info.PricingTokenCountMeta
	promptTokens := info.GetEstimatePromptTokens()
	if meta == nil {
		meta = &types.TokenCountMeta{}
		if previous != nil {
			promptTokens = previous.EstimatedPromptTokens
			meta.MaxTokens = previous.EstimatedCompletionTokens
		}
	}
	_, err := ModelPriceHelper(ctx, info, promptTokens, meta)
	if err != nil {
		return err
	}
	current := info.TieredBillingSnapshot
	if previous != nil && current != nil && previous.ExprString == current.ExprString &&
		previous.GroupRatio == current.GroupRatio && previous.EstimatedPromptTokens == current.EstimatedPromptTokens &&
		previous.EstimatedCompletionTokens == current.EstimatedCompletionTokens {
		info.TieredBillingSnapshot = previous
		info.PriceData.QuotaToPreConsume = previous.EstimatedQuotaAfterGroup
	}
	return nil
}

// HandleGroupRatio checks for "auto_group" in the context and updates the group ratio and relayInfo.UsingGroup if present
func HandleGroupRatio(ctx *gin.Context, relayInfo *relaycommon.RelayInfo) hosttypes.GroupRatioInfo {
	groupRatioInfo := hosttypes.GroupRatioInfo{
		GroupRatio:        1.0, // default ratio
		GroupSpecialRatio: -1,
	}

	// check auto group
	autoGroup, exists := ctx.Get("auto_group")
	if exists {
		logger.LogDebug(ctx, "final group: %s", autoGroup)
		relayInfo.UsingGroup = autoGroup.(string)
	}

	// check user group special ratio
	userGroupRatio, ok := ratio_setting.GetGroupGroupRatio(relayInfo.UserGroup, relayInfo.UsingGroup)
	if ok {
		// user group special ratio
		groupRatioInfo.GroupSpecialRatio = userGroupRatio
		groupRatioInfo.GroupRatio = userGroupRatio
		groupRatioInfo.HasSpecialRatio = true
	} else {
		// normal group ratio
		groupRatioInfo.GroupRatio = ratio_setting.GetGroupRatio(relayInfo.UsingGroup)
	}

	return groupRatioInfo
}

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (hosttypes.PriceData, error) {
	if info != nil {
		info.BillingModelName = ""
		if matched := resolveBillingModelName(info.GetOriginModelName(), servingChannelId(c, info)); matched != "" && matched != info.OriginModelName {
			info.BillingModelName = matched
		}
	}
	if meta == nil {
		meta = &types.TokenCountMeta{}
	}
	info.PricingTokenCountMeta = meta
	info.SetEstimatePromptTokens(promptTokens)
	billingModelName := info.GetBillingModelName()
	if info.BillingRequestInput == nil {
		requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		info.BillingRequestInput = &requestInput
	}
	groupRatioInfo := HandleGroupRatio(c, info)

	// The mode is resolved against the serving channel, because one channel may
	// price this model by expression while another prices it by token ratio.
	if billing_setting.GetChannelBillingMode(billingModelName, servingChannelId(c, info)) == billing_setting.BillingModeTieredExpr {
		return modelPriceHelperTiered(c, info, billingModelName, promptTokens, meta, groupRatioInfo)
	}

	modelPrice, usePrice, err := resolveChannelModelPrice(c, info, billingModelName, false)
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	info.TieredBillingSnapshot = nil
	var preConsumedQuota int
	var modelRatio float64
	var completionRatio float64
	var cacheRatio float64
	var imageRatio float64
	var cacheCreationRatio float64
	var cacheCreationRatio5m float64
	var cacheCreationRatio1h float64
	var audioRatio float64
	var audioCompletionRatio float64
	var freeModel bool
	if !usePrice {
		preConsumedTokens := common.Max(promptTokens, common.PreConsumedQuota)
		if meta.MaxTokens != 0 {
			preConsumedTokens += meta.MaxTokens
		}
		var success bool
		var matchName string
		modelRatio, success, matchName = ratio_setting.GetModelRatio(billingModelName)
		if override, ok := ratio_setting.GetChannelModelPricing(billingModelName, servingChannelId(c, info)); ok && override.ModelRatio != nil {
			success = true
		}
		if !success {
			acceptUnsetRatio := false
			if info.UserSetting.AcceptUnsetRatioModel {
				acceptUnsetRatio = true
			}
			if !acceptUnsetRatio {
				return hosttypes.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
			}
		}
		completionRatio = ratio_setting.GetCompletionRatio(billingModelName)
		cacheRatio, _ = ratio_setting.GetCacheRatio(billingModelName)
		cacheCreationRatio, _ = ratio_setting.GetCreateCacheRatio(billingModelName)
		cacheCreationRatio5m = cacheCreationRatio
		// 固定1h和5min缓存写入价格的比例
		cacheCreationRatio1h = cacheCreationRatio * claudeCacheCreation1hMultiplier
		imageRatio, _ = ratio_setting.GetImageRatio(billingModelName)
		audioRatio = ratio_setting.GetAudioRatio(billingModelName)
		audioCompletionRatio = ratio_setting.GetAudioCompletionRatio(billingModelName)
		lanes := hosttypes.PriceData{
			ModelRatio: modelRatio, CompletionRatio: completionRatio, CacheRatio: cacheRatio,
			CacheCreationRatio: cacheCreationRatio, CacheCreation5mRatio: cacheCreationRatio5m,
			CacheCreation1hRatio: cacheCreationRatio1h, ImageRatio: imageRatio,
			AudioRatio: audioRatio, AudioCompletionRatio: audioCompletionRatio,
		}
		applyChannelPricing(c, info, billingModelName, &lanes)
		modelRatio, completionRatio, cacheRatio = lanes.ModelRatio, lanes.CompletionRatio, lanes.CacheRatio
		cacheCreationRatio, cacheCreationRatio5m, cacheCreationRatio1h = lanes.CacheCreationRatio, lanes.CacheCreation5mRatio, lanes.CacheCreation1hRatio
		imageRatio, audioRatio, audioCompletionRatio = lanes.ImageRatio, lanes.AudioRatio, lanes.AudioCompletionRatio
		// Reserve this channel's effective estimate. A retry reserves any
		// increase before sending, so inactive prices on other channels must
		// not block an otherwise affordable request.
		estimate := float64(preConsumedTokens) * modelRatio
		if lanes.ChannelPricing {
			// Any input token may belong to a priced subcategory; output audio
			// uses its input audio multiplier followed by audio completion.
			inputMultiplier := math.Max(1, math.Max(cacheRatio, math.Max(cacheCreationRatio1h, math.Max(imageRatio, audioRatio))))
			outputMultiplier := math.Max(completionRatio, audioRatio*audioCompletionRatio)
			estimatedCompletion := meta.MaxTokens
			if estimatedCompletion == 0 {
				estimatedCompletion = defaultTieredPreConsumeMaxTokens
			}
			channelEstimate := (float64(common.Max(promptTokens, common.PreConsumedQuota))*inputMultiplier + float64(estimatedCompletion)*outputMultiplier) * modelRatio
			estimate = math.Max(estimate, channelEstimate)
		}
		quota, err := common.QuotaFromFloatStrict(estimate * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		preConsumedQuota = quota
		if _, image := info.Request.(*dto.ImageRequest); image {
			info.ImageQuotaBeforeGroup = float64(preConsumedTokens) * modelRatio
		}
	} else {
		if meta.ImagePriceRatio != 0 {
			modelPrice = modelPrice * meta.ImagePriceRatio
		}
		if _, image := info.Request.(*dto.ImageRequest); image {
			info.ImageQuotaBeforeGroup = modelPrice * common.QuotaPerUnit
		}
	}

	// check if free model pre-consume is disabled
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		// if model price or ratio is 0, do not pre-consume quota
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		} else if usePrice {
			if modelPrice == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		} else {
			if modelRatio == 0 {
				preConsumedQuota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:            freeModel,
		ModelPrice:           modelPrice,
		ModelRatio:           modelRatio,
		CompletionRatio:      completionRatio,
		GroupRatioInfo:       groupRatioInfo,
		UsePrice:             usePrice,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		CacheCreationRatio:   cacheCreationRatio,
		CacheCreation5mRatio: cacheCreationRatio5m,
		CacheCreation1hRatio: cacheCreationRatio1h,
		QuotaToPreConsume:    preConsumedQuota,
	}
	_, priceData.ChannelPricing = ratio_setting.GetChannelModelPricing(billingModelName, servingChannelId(c, info))
	priceData.AudioPricingEnabled = ratio_setting.ContainsAudioRatio(billingModelName) || ratio_setting.ContainsAudioCompletionRatio(billingModelName)
	if override, ok := ratio_setting.GetChannelModelPricing(billingModelName, servingChannelId(c, info)); ok {
		priceData.AudioPricingEnabled = priceData.AudioPricingEnabled || override.AudioRatio != nil || override.AudioCompletionRatio != nil
	}
	if usePrice {
		for name, ratio := range meta.BillingRatios {
			priceData.AddOtherRatio(name, ratio)
		}
	}
	if request, image := info.Request.(*dto.ImageRequest); image {
		channelType := common.GetContextKeyInt(c, constant.ContextKeyChannelType)
		count, err := request.ImageCount(channelType == constant.ChannelTypeAli)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if usePrice || channelType == constant.ChannelTypeAli {
			priceData.AddOtherRatio("n", float64(count))
		}
		if channelType == constant.ChannelTypeAli && request.BillingParameters != nil && request.BillingParameters.PromptExtend != nil && *request.BillingParameters.PromptExtend {
			// Resolve only routing identity; do not initialize ChannelMeta on the
			// real request, which also distinguishes the first channel attempt.
			mapped := &relaycommon.RelayInfo{OriginModelName: info.OriginModelName, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: info.OriginModelName}}
			if err := ModelMappedHelper(c, mapped, nil); err != nil {
				return hosttypes.PriceData{}, err
			}
			if strings.Contains(mapped.UpstreamModelName, "z-image") {
				priceData.AddOtherRatio("prompt_extend", common.ZImagePromptExtendMultiplier)
			}
		}
		if !usePrice {
			quota, err := common.QuotaFromFloatStrict(priceData.ApplyOtherRatiosToFloat(info.ImageQuotaBeforeGroup * groupRatioInfo.GroupRatio))
			if err != nil {
				return hosttypes.PriceData{}, err
			}
			priceData.QuotaToPreConsume = quota
		}
	}
	if usePrice {
		quotaToPreConsume := priceData.ApplyOtherRatiosToFloat(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		quota, err := common.QuotaFromFloatStrict(quotaToPreConsume)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		priceData.QuotaToPreConsume = quota
	}

	if common.DebugEnabled {
		logger.LogDebug(c, "model_price_helper result: %s", priceData.ToSetting())
	}
	info.PriceData = priceData
	info.UsePrice = priceData.UsePrice
	return priceData, nil
}

// ModelPriceHelperPerCall 按次/按量计费的 PriceHelper (MJ、Task)
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo, billingModelName string) (hosttypes.PriceData, error) {
	groupRatioInfo := HandleGroupRatio(c, info)

	modelPrice, usePrice, err := resolveChannelModelPrice(c, info, billingModelName, true)
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	var modelRatio float64
	if !usePrice {
		var ratioSuccess bool
		var matchName string
		modelRatio, ratioSuccess, matchName = ratio_setting.GetModelRatio(billingModelName)
		if override, ok := ratio_setting.GetChannelModelPricing(billingModelName, servingChannelId(c, info)); ok && override.ModelRatio != nil {
			ratioSuccess = true
		}
		if !ratioSuccess && !info.UserSetting.AcceptUnsetRatioModel {
			return hosttypes.PriceData{}, modelPriceNotConfiguredError(matchName, info.UserId)
		}
		lanes := hosttypes.PriceData{ModelRatio: modelRatio}
		applyChannelPricing(c, info, billingModelName, &lanes)
		modelRatio = lanes.ModelRatio
	}

	var quota int
	freeModel := false

	if usePrice {
		var err error
		quota, err = common.QuotaFromFloatStrict(modelPrice * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelPrice == 0 {
				quota = 0
				freeModel = true
			}
		}
	} else {
		// 按量计费：以模型倍率的一半作为预扣额度
		var err error
		quota, err = common.QuotaFromFloatStrict(modelRatio / 2 * common.QuotaPerUnit * groupRatioInfo.GroupRatio)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
		modelPrice = -1
		if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
			if groupRatioInfo.GroupRatio == 0 || modelRatio == 0 {
				quota = 0
				freeModel = true
			}
		}
	}

	priceData := hosttypes.PriceData{
		FreeModel:      freeModel,
		ModelPrice:     modelPrice,
		ModelRatio:     modelRatio,
		UsePrice:       usePrice,
		Quota:          quota,
		GroupRatioInfo: groupRatioInfo,
	}
	return priceData, nil
}

func HasModelBillingConfig(modelName string) bool {
	if _, ok := ratio_setting.GetModelPrice(modelName, false); ok {
		return true
	}
	if _, ok, _ := ratio_setting.GetModelRatio(modelName); ok {
		return true
	}
	if billing_setting.GetBillingMode(modelName) != billing_setting.BillingModeTieredExpr {
		return false
	}
	expr, ok := billing_setting.GetBillingExpr(modelName)
	return ok && strings.TrimSpace(expr) != ""
}

// HasPriceOrRatioEntry reports whether name has a configured price, ratio, or
// tiered billing-mode entry after a single wildcard normalization. Self-use
// fallback does not count as a configured ratio.
func HasPriceOrRatioEntry(name string) bool {
	formatted := ratio_setting.FormatMatchingModelName(name)
	if _, ok := ratio_setting.GetModelPrice(formatted, false); ok {
		return true
	}
	if ratio_setting.HasConfiguredModelRatio(formatted) {
		return true
	}
	return billing_setting.GetBillingMode(formatted) == billing_setting.BillingModeTieredExpr
}

func resolveBillingModelName(origin string, channelId int) string {
	var candidates []string
	if !reasoning.ParseModelModifiers(origin).HasModifiers() {
		candidates = append(candidates, origin)
	}
	candidates = append(candidates, hostreasoning.CanonicalBillingModelNames(origin)...)
	base := hostreasoning.BaseModelName(origin)
	candidates = append(candidates, base)

	seen := make(map[string]struct{}, len(candidates))
	matched := ""
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		_, channelConfigured := ratio_setting.GetChannelModelPricing(name, channelId)
		if HasPriceOrRatioEntry(name) || channelConfigured {
			matched = name
			break
		}
	}
	if matched == "" {
		matched = base
	}
	return matched
}

func modelPriceHelperTiered(c *gin.Context, info *relaycommon.RelayInfo, billingModelName string, promptTokens int, meta *types.TokenCountMeta, groupRatioInfo hosttypes.GroupRatioInfo) (hosttypes.PriceData, error) {
	exprStr, ok := billing_setting.GetChannelBillingExpr(billingModelName, servingChannelId(c, info))
	if !ok {
		return hosttypes.PriceData{}, fmt.Errorf("model %s is configured as tiered_expr but has no billing expression", billingModelName)
	}
	exprHash := billingexpr.ExprHashString(exprStr)
	if info.RelayFormat == types.RelayFormatOpenAIRealtime && billingexpr.UsesFixedPricingByHash(exprStr, exprHash) {
		return hosttypes.PriceData{}, fmt.Errorf("fixed pricing is not supported for Realtime requests")
	}

	estimatedCompletionTokens := meta.MaxTokens
	if estimatedCompletionTokens == 0 && groupRatioInfo.GroupRatio != 0 {
		estimatedCompletionTokens = defaultTieredPreConsumeMaxTokens
	}

	requestInput, err := ResolveIncomingBillingExprRequestInput(c, info)
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	if billingexpr.UsedVarsByHash(exprStr, exprHash)["image_count"] {
		requestInput, err = ResolveImageBillingRequestInput(c, info, requestInput)
		if err != nil {
			return hosttypes.PriceData{}, err
		}
	}

	rawCost, trace, err := billingexpr.RunExprByHashWithRequest(exprStr, exprHash, billingexpr.TokenParams{
		P:   float64(promptTokens),
		C:   float64(estimatedCompletionTokens),
		Len: float64(promptTokens),
	}, requestInput)
	if err != nil {
		return hosttypes.PriceData{}, fmt.Errorf("model %s tiered expr run failed: %w", billingModelName, err)
	}
	if rawCost < 0 || math.IsNaN(rawCost) || math.IsInf(rawCost, 0) {
		return hosttypes.PriceData{}, fmt.Errorf("model %s billing expression must return a finite, non-negative price", billingModelName)
	}

	// Expression coefficients are $/1M tokens prices; convert to quota the same way per-call billing does.
	quotaBeforeGroup := rawCost / 1_000_000 * common.QuotaPerUnit
	preConsumedQuota, err := billingexpr.QuotaRoundStrict(quotaBeforeGroup * groupRatioInfo.GroupRatio)
	if err != nil {
		return hosttypes.PriceData{}, err
	}

	freeModel := false
	if !operation_setting.GetQuotaSetting().EnableFreeModelPreConsume {
		if groupRatioInfo.GroupRatio == 0 {
			preConsumedQuota = 0
			freeModel = true
		}
	}

	snapshot := &billingexpr.BillingSnapshot{
		EstimatedImageCount:       trace.ImageCount,
		BillingMode:               billing_setting.BillingModeTieredExpr,
		ModelName:                 billingModelName,
		ExprString:                exprStr,
		ExprHash:                  exprHash,
		GroupRatio:                groupRatioInfo.GroupRatio,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: estimatedCompletionTokens,
		EstimatedQuotaBeforeGroup: quotaBeforeGroup,
		EstimatedQuotaAfterGroup:  preConsumedQuota,
		EstimatedTier:             trace.MatchedTier,
		EstimatedBillingUnit:      trace.BillingUnit,
		EstimatedFixedPrice:       trace.FixedPrice,
		QuotaPerUnit:              common.QuotaPerUnit,
		ExprVersion:               billingexpr.ExprVersion(exprStr),
	}
	info.TieredBillingSnapshot = snapshot
	info.BillingRequestInput = &requestInput
	info.UsePrice = false

	priceData := hosttypes.PriceData{
		FreeModel:         freeModel,
		GroupRatioInfo:    groupRatioInfo,
		QuotaToPreConsume: preConsumedQuota,
	}
	_, priceData.ChannelPricing = ratio_setting.GetChannelModelPricing(billingModelName, servingChannelId(c, info))

	logger.LogDebug(c, "model_price_helper_tiered result: model=%s preConsume=%d quotaBeforeGroup=%.2f groupRatio=%.2f tier=%s", billingModelName, preConsumedQuota, quotaBeforeGroup, groupRatioInfo.GroupRatio, trace.MatchedTier)

	info.PriceData = priceData
	return priceData, nil
}
