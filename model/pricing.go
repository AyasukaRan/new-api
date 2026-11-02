package model

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/QuantumNous/new-api/types"
)

type PricingPluginVariant struct {
	PluginKey            string                               `json:"plugin_key"`
	PluginName           string                               `json:"plugin_name"`
	Icon                 string                               `json:"icon,omitempty"`
	BillingExpr          string                               `json:"billing_expr"`
	BillingMode          string                               `json:"billing_mode"`
	BillingUsageSchema   map[string]jsplugin.UsageFieldSchema `json:"billing_usage_schema"`
	BillingUsageExamples []jsplugin.UsageExample              `json:"billing_usage_examples,omitempty"`
}

type Pricing struct {
	ChannelPricingVariants []Pricing                            `json:"channel_pricing_variants,omitempty"`
	PricingConfigured      bool                                 `json:"pricing_configured"`
	BillingPluginVariants  []PricingPluginVariant               `json:"billing_plugin_variants,omitempty"`
	ModelName              string                               `json:"model_name"`
	Description            string                               `json:"description,omitempty"`
	Icon                   string                               `json:"icon,omitempty"`
	Tags                   string                               `json:"tags,omitempty"`
	VendorID               int                                  `json:"vendor_id,omitempty"`
	QuotaType              int                                  `json:"quota_type"`
	ModelRatio             float64                              `json:"model_ratio"`
	ModelPrice             float64                              `json:"model_price"`
	OwnerBy                string                               `json:"owner_by"`
	CompletionRatio        float64                              `json:"completion_ratio"`
	CacheRatio             *float64                             `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64                             `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64                             `json:"image_ratio,omitempty"`
	AudioRatio             *float64                             `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64                             `json:"audio_completion_ratio,omitempty"`
	EnableGroup            []string                             `json:"enable_groups"`
	SupportedEndpointTypes []constant.EndpointType              `json:"supported_endpoint_types"`
	BillingMode            string                               `json:"billing_mode,omitempty"`
	BillingExpr            string                               `json:"billing_expr,omitempty"`
	BillingUsageSchema     map[string]jsplugin.UsageFieldSchema `json:"billing_usage_schema,omitempty"`
	BillingUsageExamples   []jsplugin.UsageExample              `json:"billing_usage_examples,omitempty"`
	PricingVersion         string                               `json:"pricing_version,omitempty"`
}

type PricingVendor struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

var (
	pricingMap           []Pricing
	vendorsList          []PricingVendor
	supportedEndpointMap map[string]common.EndpointInfo
	lastGetPricingTime   time.Time
	updatePricingLock    sync.Mutex

	// 缓存映射：模型名 -> 启用分组 / 计费类型
	modelEnableGroups     = make(map[string][]string)
	modelQuotaTypeMap     = make(map[string]int)
	modelEnableGroupsLock = sync.RWMutex{}
)

var (
	modelSupportEndpointTypes = make(map[string][]constant.EndpointType)
	modelSupportEndpointsLock = sync.RWMutex{}
)

func GetPricing() []Pricing {
	if time.Since(lastGetPricingTime) > time.Minute*1 || len(pricingMap) == 0 {
		updatePricingLock.Lock()
		defer updatePricingLock.Unlock()
		// Double check after acquiring the lock
		if time.Since(lastGetPricingTime) > time.Minute*1 || len(pricingMap) == 0 {
			modelSupportEndpointsLock.Lock()
			defer modelSupportEndpointsLock.Unlock()
			updatePricing()
		}
	}
	return pricingMap
}

func InvalidatePricingCache() {
	updatePricingLock.Lock()
	defer updatePricingLock.Unlock()

	pricingMap = nil
	vendorsList = nil
	lastGetPricingTime = time.Time{}
}

// GetVendors 返回当前定价接口使用到的供应商信息
func GetVendors() []PricingVendor {
	if time.Since(lastGetPricingTime) > time.Minute*1 || len(pricingMap) == 0 {
		// 保证先刷新一次
		GetPricing()
	}
	return vendorsList
}

func GetModelSupportEndpointTypes(model string) []constant.EndpointType {
	if model == "" {
		return make([]constant.EndpointType, 0)
	}
	modelSupportEndpointsLock.RLock()
	defer modelSupportEndpointsLock.RUnlock()
	if endpoints, ok := modelSupportEndpointTypes[model]; ok {
		return endpoints
	}
	return make([]constant.EndpointType, 0)
}

func getPricingEndpointTypesForAbility(ability AbilityWithChannel, advancedCustomConfigs map[int]*dto.AdvancedCustomConfig) []constant.EndpointType {
	if ability.ChannelType != constant.ChannelTypeAdvancedCustom {
		return common.GetEndpointTypesByChannelType(ability.ChannelType, ability.Model)
	}
	if config := advancedCustomConfigs[ability.ChannelId]; config != nil {
		return config.SupportedEndpointTypesForModel(ability.Model)
	}
	return common.GetEndpointTypesByChannelType(ability.ChannelType, ability.Model)
}

// loadPricingAdvancedCustomConfigs runs inside updatePricing while
// updatePricingLock is held, and nests channelSyncLock.RLock. This defines the
// global lock order updatePricingLock -> channelSyncLock: any code path holding
// channelSyncLock must release it before touching the pricing cache (see
// InitChannelCache / CacheUpdateChannel), otherwise it deadlocks.
// The returned configs are pointers shared with the channel cache; they are
// replaced wholesale on update and never mutated in place, so reading them after
// RUnlock is safe.
func loadPricingAdvancedCustomConfigs(enableAbilities []AbilityWithChannel) map[int]*dto.AdvancedCustomConfig {
	channelIDs := make([]int, 0)
	seen := make(map[int]struct{})
	for _, ability := range enableAbilities {
		if ability.ChannelType != constant.ChannelTypeAdvancedCustom {
			continue
		}
		if _, exists := seen[ability.ChannelId]; exists {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIDs = append(channelIDs, ability.ChannelId)
	}
	if len(channelIDs) == 0 {
		return nil
	}

	configs := make(map[int]*dto.AdvancedCustomConfig, len(channelIDs))
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		for _, channelID := range channelIDs {
			if config := channel2advancedCustomConfig[channelID]; config != nil {
				configs[channelID] = config
			}
		}
		return configs
	}

	for _, channelID := range channelIDs {
		channel, err := CacheGetChannel(channelID)
		if err != nil {
			common.SysLog(common.LogText("load advanced custom channel settings error: channel_id=%d, error=%v", channelID, err))
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			continue
		}
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			configs[channelID] = config
		}
	}
	return configs
}

func appendPricingEndpoint(endpoints []string, endpoint string) []string {
	if endpoint == "" || common.StringsContains(endpoints, endpoint) {
		return endpoints
	}
	return append(endpoints, endpoint)
}

// HasPriceOrRatioEntry reports an explicit price after wildcard normalization;
// the self-use fallback is not a configured model price.
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

// ResolveBillingModelName is shared by HTTP relay and public pricing, so a
// reasoning modifier never changes the price displayed for the billed model.
func ResolveBillingModelName(origin string, channelID int) string {
	var candidates []string
	if !reasoning.ParseModelModifiers(origin).HasModifiers() {
		candidates = append(candidates, origin)
	}
	candidates = append(candidates, hostreasoning.CanonicalBillingModelNames(origin)...)
	base := hostreasoning.BaseModelName(origin)
	// DeepSeek thinking suffixes share the base model price unless preserved.
	if deepSeekBase, _, _, ok := reasoning.ParseDeepSeekV4ThinkingSuffix(base); ok && !model_setting.ShouldPreserveThinkingSuffix(base) {
		candidates = append(candidates, deepSeekBase)
	}
	candidates = append(candidates, base)
	seen := make(map[string]struct{}, len(candidates))
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, found := seen[name]; found {
			continue
		}
		seen[name] = struct{}{}
		_, channelConfigured := ratio_setting.GetChannelModelPricing(name, channelID)
		if HasPriceOrRatioEntry(name) || channelConfigured {
			return name
		}
	}
	return base
}

func taskChannelBillingMode(name, mapped string, channelID int) (string, string, string, bool) {
	for _, candidate := range []string{name, mapped} {
		if mode, expr, ok := ratio_setting.ChannelBillingMode(candidate, channelID); ok {
			return candidate, mode, expr, true
		}
	}
	return "", "", "", false
}

// resolveChannelPricing describes the same channel override precedence used by
// relay, without evaluating a request or changing any stored billing settings.
func resolveChannelPricing(base Pricing, channelID int, provider *jsplugin.LoadedPlugin, mappedModel string, sharedModel bool) (Pricing, string) {
	pricing := base
	pricing.ChannelPricingVariants = nil
	pricing.BillingPluginVariants = nil
	pricing.BillingUsageSchema = nil
	pricing.BillingUsageExamples = nil
	pricing.BillingMode = ""
	pricing.BillingExpr = ""
	pricing.ModelPrice = 0
	pricing.ModelRatio = 0
	pricing.CompletionRatio = 0
	pricing.CacheRatio = nil
	pricing.CreateCacheRatio = nil
	pricing.ImageRatio = nil
	pricing.AudioRatio = nil
	pricing.AudioCompletionRatio = nil
	pricing.QuotaType = 0
	pricing.PricingConfigured = false
	name := base.ModelName
	if provider == nil {
		name = ResolveBillingModelName(name, channelID)
	}
	mode := billing_setting.GetChannelBillingMode(name, channelID)
	expression, _ := billing_setting.GetChannelBillingExpr(name, channelID)
	if provider != nil {
		schema, examples := provider.Meta.UsageForModels(mappedModel, name)
		pricing.BillingUsageSchema = jsplugin.CloneUsageSchema(schema)
		pricing.BillingUsageExamples = jsplugin.CloneUsageExamples(examples)
		var found bool
		expression, found = billing_setting.ResolveTaskBillingExpr(provider.Meta.Key, name, mappedModel)
		mode = billing_setting.BillingModeRatio
		if found || billing_setting.GetBillingMode(name) == billing_setting.BillingModeTieredExpr {
			mode = billing_setting.BillingModeTieredExpr
		}
		if target, pinnedMode, pinnedExpr, pinned := taskChannelBillingMode(name, mappedModel, channelID); pinned {
			name, mode, expression = target, pinnedMode, pinnedExpr
		}
	}
	pricing.BillingMode = mode
	if mode == billing_setting.BillingModeTieredExpr {
		pricing.BillingExpr = expression
		_, compileErr := billingexpr.CompileFromCache(expression)
		pricing.PricingConfigured = strings.TrimSpace(expression) != "" && compileErr == nil
		switch {
		case strings.TrimSpace(expression) == "":
			return pricing, ModelPricingIssueUnconfigured
		case compileErr != nil:
			return pricing, ModelPricingIssueInvalidExpression
		case provider == nil && (billingexpr.UsedVars(expression)["u"] ||
			(!billingexpr.UsesFixedPricing(expression) && ordinaryRouteSharesTaskPrice(base.ModelName, mappedModel, name, nil))):
			pricing.BillingExpr, pricing.PricingConfigured = "", false
			return pricing, ModelPricingIssueInvalidExpression
		case provider != nil && billingexpr.UsesFixedPricing(expression):
			pricing.BillingExpr, pricing.PricingConfigured = "", false
			return pricing, ModelPricingIssueInvalidTaskPrice
		case provider != nil && !billing_setting.TaskExprCompatible(expression, pricing.BillingUsageSchema):
			if sharedModel {
				pricing.BillingExpr, pricing.PricingConfigured = "", false
			}
			return pricing, ModelPricingIssueIncompatible
		}
		return pricing, ""
	}

	override, _ := ratio_setting.GetChannelModelPricing(name, channelID)
	price, hasPrice := ratio_setting.GetModelPrice(name, false)
	if !hasPrice && provider != nil {
		price, hasPrice = ratio_setting.GetDefaultModelPriceMap()[name]
	}
	if override.ModelPrice != nil {
		price = ratio_setting.ResolveModelPrice(override.ModelPrice, -1)
		hasPrice = price >= 0
	}
	if mode != ratio_setting.ChannelBillingModePerToken && (hasPrice || mode == ratio_setting.ChannelBillingModePerRequest) {
		pricing.QuotaType = 1
		pricing.PricingConfigured = hasPrice
		if hasPrice {
			pricing.ModelPrice = price
		}
		if !hasPrice {
			return pricing, ModelPricingIssueUnconfigured
		}
		return pricing, ""
	}
	modelRatio, hasRatio, _ := ratio_setting.GetModelRatio(name)
	pricing.PricingConfigured = hasRatio || (override.ModelRatio != nil && ratio_setting.ResolveRatio(override.ModelRatio, -1) >= 0)
	if pricing.PricingConfigured {
		pricing.ModelRatio = ratio_setting.ResolveRatio(override.ModelRatio, modelRatio)
	}
	pricing.CompletionRatio = ratio_setting.ResolveRatio(override.CompletionRatio, ratio_setting.GetCompletionRatio(name))
	cache, hasCache := ratio_setting.GetCacheRatio(name)
	if hasCache || override.CacheRatio != nil {
		value := ratio_setting.ResolveRatio(override.CacheRatio, cache)
		pricing.CacheRatio = &value
	}
	createCache, hasCreateCache := ratio_setting.GetCreateCacheRatio(name)
	if hasCreateCache || override.CreateCacheRatio != nil {
		value := ratio_setting.ResolveRatio(override.CreateCacheRatio, createCache)
		pricing.CreateCacheRatio = &value
	}
	image, hasImage := ratio_setting.GetImageRatio(name)
	if hasImage || override.ImageRatio != nil {
		value := ratio_setting.ResolveRatio(override.ImageRatio, image)
		pricing.ImageRatio = &value
	}
	if ratio_setting.ContainsAudioRatio(name) || override.AudioRatio != nil {
		value := ratio_setting.ResolveRatio(override.AudioRatio, ratio_setting.GetAudioRatio(name))
		pricing.AudioRatio = &value
	}
	if ratio_setting.ContainsAudioCompletionRatio(name) || override.AudioCompletionRatio != nil {
		value := ratio_setting.ResolveRatio(override.AudioCompletionRatio, ratio_setting.GetAudioCompletionRatio(name))
		pricing.AudioCompletionRatio = &value
	}
	if !pricing.PricingConfigured {
		return pricing, ModelPricingIssueUnconfigured
	}
	return pricing, ""
}

// Models claimed by an ordinary host protocol can be served by an unbound
// channel at that same endpoint. Plugin-native paths require a matching binding.
func ordinaryEndpointPlugins(g *jsplugin.RoutingGeneration, declared string) []*jsplugin.LoadedPlugin {
	return slices.DeleteFunc(g.PluginsByModel(declared), func(plugin *jsplugin.LoadedPlugin) bool {
		return !slices.ContainsFunc(plugin.Meta.Protocols, func(claim jsplugin.ProtocolClaim) bool {
			return len(claim.Models) == 0 || slices.Contains(claim.Models, declared)
		})
	})
}

func ordinaryRouteSharesTaskPrice(clientModel, mapped, billingName string, aliases *taskAliasView) bool {
	g := jsplugin.DefaultRegistry.Generation()
	pin, direct := g.CanonicalModel(clientModel)
	declared, mappedPin := pin, ""
	if !direct {
		if _, known := g.CanonicalModel(mapped); !known || clientModel == mapped {
			return false
		}
		var alias TaskAliasTarget
		var ok bool
		if aliases != nil {
			alias, ok = aliases.byFold[jsplugin.ASCIIFold(clientModel)]
		} else {
			alias, ok = ResolveTaskModelAlias(g, clientModel)
		}
		if !ok || alias.Declared == "" {
			return false
		}
		pin, declared, mappedPin = alias.Alias, alias.Declared, alias.Declared
	}
	for _, plugin := range ordinaryEndpointPlugins(g, declared) {
		if billing_setting.SharesTaskBillingModel(plugin.Meta.Key, pin, mappedPin, billingName) {
			return true
		}
	}
	return false
}

// pricingChannelProviders looks up the task providers this channel could actually
// execute for a client model. Other enabled channels may use ordinary relay for
// the same model even when a task plugin also declares its name.
func pricingChannelProviders(g *jsplugin.RoutingGeneration, channel *Channel, model, mapped string) []*jsplugin.LoadedPlugin {
	declared, found := g.CanonicalModel(model)
	if !found && model != mapped {
		declared, found = g.CanonicalModel(mapped)
	}
	if !found {
		return nil
	}
	candidates := g.PluginsByModel(declared)
	if channel.Type == constant.ChannelTypeTaskPlugin || channel.Type == constant.ChannelTypeNewAPI {
		var setting dto.ChannelSettings
		if channel.Setting == nil || common.UnmarshalJsonStr(*channel.Setting, &setting) != nil {
			return nil
		}
		return slices.DeleteFunc(candidates, func(candidate *jsplugin.LoadedPlugin) bool {
			return !setting.BindsTaskPlugin(candidate.Meta.Key) || (channel.Type == constant.ChannelTypeNewAPI && !candidate.Meta.SupportsUpstream(jsplugin.UpstreamKindNewAPI))
		})
	}
	provider, ok := g.GetByChannelType(channel.Type)
	if ok && slices.Contains(candidates, provider) {
		return []*jsplugin.LoadedPlugin{provider}
	}
	return nil
}

// FilterPricingByUsableGroups filters before merging equal prices, so neither a
// price nor a group belonging only to an inaccessible channel can escape the
// cached, site-wide catalog. The returned slices do not mutate that cache.
func FilterPricingByUsableGroups(pricing []Pricing, usableGroup map[string]string) []Pricing {
	filtered := make([]Pricing, 0, len(pricing))
	for _, item := range pricing {
		candidates := item.ChannelPricingVariants
		if len(candidates) == 0 {
			candidates = []Pricing{item}
		}
		variants := make([]Pricing, 0, len(candidates))
		byPrice := make(map[string]int)
		allGroups := make(map[string]struct{})
		for _, candidate := range candidates {
			groups := make([]string, 0)
			for group := range usableGroup {
				if slices.Contains(candidate.EnableGroup, "all") || slices.Contains(candidate.EnableGroup, group) {
					groups = append(groups, group)
					allGroups[group] = struct{}{}
				}
			}
			if len(groups) == 0 {
				continue
			}
			slices.Sort(groups)
			candidate.EnableGroup = nil
			candidate.ChannelPricingVariants = nil
			encoded, err := common.Marshal(candidate)
			if err != nil {
				continue
			}
			key := string(encoded)
			if index, found := byPrice[key]; found {
				variants[index].EnableGroup = append(variants[index].EnableGroup, groups...)
				slices.Sort(variants[index].EnableGroup)
				variants[index].EnableGroup = slices.Compact(variants[index].EnableGroup)
				continue
			}
			candidate.EnableGroup = groups
			byPrice[key] = len(variants)
			variants = append(variants, candidate)
		}
		if len(variants) == 0 {
			continue
		}
		visible := variants[0]
		visible.PricingVersion = item.PricingVersion
		visible.EnableGroup = make([]string, 0, len(allGroups))
		for group := range allGroups {
			visible.EnableGroup = append(visible.EnableGroup, group)
		}
		slices.Sort(visible.EnableGroup)
		visible.ChannelPricingVariants = variants
		filtered = append(filtered, visible)
	}
	return filtered
}

func updatePricing() {
	//modelRatios := common.GetModelRatios()
	enableAbilities, err := GetAllEnableAbilityWithChannels()
	if err != nil {
		common.SysLog(common.LogText("GetAllEnableAbilityWithChannels error: %v", err))
		return
	}
	// 预加载模型元数据与供应商一次，避免循环查询
	var allMeta []Model
	_ = DB.Find(&allMeta).Error
	names := make([]string, 0, len(enableAbilities))
	for _, ability := range enableAbilities {
		names = append(names, ability.Model)
	}
	metaMap := resolveModelMetadata(allMeta, names)

	// 预加载供应商
	var vendors []Vendor
	_ = DB.Find(&vendors).Error
	vendorMap := make(map[int]*Vendor)
	for i := range vendors {
		vendorMap[vendors[i].Id] = &vendors[i]
	}

	// 初始化默认供应商映射
	initDefaultVendorMapping(metaMap, vendorMap, enableAbilities)

	// 构建对前端友好的供应商列表
	vendorsList = make([]PricingVendor, 0, len(vendorMap))
	for _, v := range vendorMap {
		vendorsList = append(vendorsList, PricingVendor{
			ID:          v.Id,
			Name:        v.Name,
			Description: v.Description,
			Icon:        v.Icon,
		})
	}

	modelGroupsMap := make(map[string]*types.Set[string])
	modelChannelGroups := make(map[string]map[int][]string)
	pricingChannels := make(map[int]*Channel)
	pricingModelMappings := make(map[int]map[string]string)

	for _, ability := range enableAbilities {
		groups, ok := modelGroupsMap[ability.Model]
		if !ok {
			groups = types.NewSet[string]()
			modelGroupsMap[ability.Model] = groups
		}
		groups.Add(ability.Group)
		if modelChannelGroups[ability.Model] == nil {
			modelChannelGroups[ability.Model] = make(map[int][]string)
		}
		modelChannelGroups[ability.Model][ability.ChannelId] = append(modelChannelGroups[ability.Model][ability.ChannelId], ability.Group)
		if _, loaded := pricingChannels[ability.ChannelId]; !loaded {
			channel, loadErr := CacheGetChannel(ability.ChannelId)
			if loadErr != nil {
				common.SysError(fmt.Sprintf("load pricing channel: channel_id=%d, error=%v", ability.ChannelId, loadErr))
			}
			pricingChannels[ability.ChannelId] = channel
			if channel != nil {
				mapping := make(map[string]string)
				if raw := channel.GetModelMapping(); raw != "" && raw != "{}" {
					if parseErr := common.UnmarshalJsonStr(raw, &mapping); parseErr != nil {
						common.SysError(fmt.Sprintf("load pricing model mapping: channel_id=%d, error=%v", channel.Id, parseErr))
						mapping = nil
					}
				}
				pricingModelMappings[ability.ChannelId] = mapping
			}
		}
	}

	//这里使用切片而不是Set，因为一个模型可能支持多个端点类型，并且第一个端点是优先使用端点
	modelSupportEndpointsStr := make(map[string][]string)
	advancedCustomConfigs := loadPricingAdvancedCustomConfigs(enableAbilities)

	// 先根据已有能力填充原生端点
	for _, ability := range enableAbilities {
		endpoints := modelSupportEndpointsStr[ability.Model]
		channelTypes := getPricingEndpointTypesForAbility(ability, advancedCustomConfigs)
		for _, channelType := range channelTypes {
			if !common.StringsContains(endpoints, string(channelType)) {
				endpoints = append(endpoints, string(channelType))
			}
		}
		modelSupportEndpointsStr[ability.Model] = endpoints
	}

	// 再补充模型自定义端点：若配置有效则追加到已有推断，不再裁剪渠道真实能力
	for modelName, meta := range metaMap {
		if strings.TrimSpace(meta.Endpoints) == "" {
			continue
		}
		var raw map[string]any
		if err := common.Unmarshal([]byte(meta.Endpoints), &raw); err == nil {
			endpoints := modelSupportEndpointsStr[modelName]
			for k, v := range raw {
				switch v.(type) {
				case string, map[string]any:
					endpoints = appendPricingEndpoint(endpoints, k)
				}
			}
			if len(endpoints) > 0 {
				modelSupportEndpointsStr[modelName] = endpoints
			}
		}
	}

	modelSupportEndpointTypes = make(map[string][]constant.EndpointType)
	for model, endpoints := range modelSupportEndpointsStr {
		supportedEndpoints := make([]constant.EndpointType, 0)
		for _, endpointStr := range endpoints {
			endpointType := constant.EndpointType(endpointStr)
			supportedEndpoints = append(supportedEndpoints, endpointType)
		}
		modelSupportEndpointTypes[model] = supportedEndpoints
	}

	// 构建全局 supportedEndpointMap（默认 + 自定义覆盖）
	supportedEndpointMap = make(map[string]common.EndpointInfo)
	// 1. 默认端点
	for _, endpoints := range modelSupportEndpointTypes {
		for _, et := range endpoints {
			if info, ok := common.GetDefaultEndpointInfo(et); ok {
				if _, exists := supportedEndpointMap[string(et)]; !exists {
					supportedEndpointMap[string(et)] = info
				}
			}
		}
	}
	// 2. 自定义端点（models 表）覆盖默认
	for _, meta := range metaMap {
		if strings.TrimSpace(meta.Endpoints) == "" {
			continue
		}
		var raw map[string]any
		if err := common.Unmarshal([]byte(meta.Endpoints), &raw); err == nil {
			for k, v := range raw {
				switch val := v.(type) {
				case string:
					supportedEndpointMap[k] = common.EndpointInfo{Path: val, Method: "POST"}
				case map[string]any:
					ep := common.EndpointInfo{Method: "POST"}
					if p, ok := val["path"].(string); ok {
						ep.Path = p
					}
					if m, ok := val["method"].(string); ok {
						ep.Method = strings.ToUpper(m)
					}
					supportedEndpointMap[k] = ep
				default:
					// ignore unsupported types
				}
			}
		}
	}

	pricingMap = make([]Pricing, 0)
	pluginGeneration := jsplugin.DefaultRegistry.Generation()
	for model, groups := range modelGroupsMap {
		pricing := Pricing{
			ModelName:              model,
			EnableGroup:            groups.Items(),
			SupportedEndpointTypes: modelSupportEndpointTypes[model],
		}

		// 补充模型元数据（描述、标签、供应商、状态）
		if meta, ok := metaMap[model]; ok {
			// 若模型被禁用(status!=1)，则直接跳过，不返回给前端
			if meta.Status != 1 {
				continue
			}
			pricing.Description = meta.Description
			pricing.Icon = meta.Icon
			pricing.Tags = meta.Tags
			pricing.VendorID = meta.VendorID
		}
		modelPrice, findPrice := ratio_setting.GetModelPrice(model, false)
		if findPrice {
			pricing.ModelPrice = modelPrice
			pricing.QuotaType = 1
			pricing.PricingConfigured = true
		} else {
			modelRatio, configured, _ := ratio_setting.GetModelRatio(model)
			pricing.ModelRatio = modelRatio
			pricing.PricingConfigured = configured
			pricing.CompletionRatio = ratio_setting.GetCompletionRatio(model)
			pricing.QuotaType = 0
		}
		if cacheRatio, ok := ratio_setting.GetCacheRatio(model); ok {
			pricing.CacheRatio = &cacheRatio
		}
		if createCacheRatio, ok := ratio_setting.GetCreateCacheRatio(model); ok {
			pricing.CreateCacheRatio = &createCacheRatio
		}
		if imageRatio, ok := ratio_setting.GetImageRatio(model); ok {
			pricing.ImageRatio = &imageRatio
		}
		if ratio_setting.ContainsAudioRatio(model) {
			audioRatio := ratio_setting.GetAudioRatio(model)
			pricing.AudioRatio = &audioRatio
		}
		if ratio_setting.ContainsAudioCompletionRatio(model) {
			audioCompletionRatio := ratio_setting.GetAudioCompletionRatio(model)
			pricing.AudioCompletionRatio = &audioCompletionRatio
		}
		if billingMode := billing_setting.GetBillingMode(model); billingMode == "tiered_expr" {
			if expr, ok := billing_setting.GetBillingExpr(model); ok && strings.TrimSpace(expr) != "" {
				pricing.BillingMode = billingMode
				pricing.BillingExpr = expr
				pricing.PricingConfigured = true
			}
		} else if target, resolved := ResolveTaskModelAlias(pluginGeneration, model); resolved && target.Declared != "" {
			if tailMode := billing_setting.GetBillingMode(target.Declared); tailMode == "tiered_expr" {
				if expr, ok := billing_setting.GetBillingExpr(target.Declared); ok && strings.TrimSpace(expr) != "" {
					pricing.BillingMode = tailMode
					pricing.BillingExpr = expr
					pricing.PricingConfigured = true
				}
			}
		}
		usageModel := model
		plugin, ok := pluginGeneration.GetByModel(model)
		if !ok {
			if target, resolved := ResolveTaskModelAlias(pluginGeneration, model); resolved {
				plugin, ok = pluginGeneration.Get(target.PluginKey)
				usageModel = target.Declared
			}
		}
		if ok && plugin != nil {
			usageSchema, usageExamples := plugin.Meta.UsageForModel(usageModel)
			pricing.BillingUsageSchema = jsplugin.CloneUsageSchema(usageSchema)
			pricing.BillingUsageExamples = jsplugin.CloneUsageExamples(usageExamples)
		}
		providers := pluginGeneration.PluginsByModel(model)
		hasProviderOverride := false
		for _, provider := range providers {
			if _, configured := billing_setting.GetPluginBillingExpr(provider.Meta.Key, model); configured {
				hasProviderOverride = true
				break
			}
		}
		if hasProviderOverride || (len(providers) >= 2 && pricing.BillingMode == billing_setting.BillingModeTieredExpr) {
			for _, provider := range providers {
				schema, examples := provider.Meta.UsageForModel(model)
				if schema == nil {
					schema = map[string]jsplugin.UsageFieldSchema{}
				}
				expression, hasExpression := billing_setting.ResolveTaskBillingExpr(provider.Meta.Key, model, "")
				mode := billing_setting.BillingModeRatio
				if hasExpression || billing_setting.GetBillingMode(model) == billing_setting.BillingModeTieredExpr {
					mode = billing_setting.BillingModeTieredExpr
				}
				if mode == billing_setting.BillingModeTieredExpr && !billing_setting.TaskExprCompatible(expression, schema) {
					expression = ""
				}
				pricing.BillingPluginVariants = append(pricing.BillingPluginVariants, PricingPluginVariant{
					PluginKey: provider.Meta.Key, PluginName: provider.Meta.Name, Icon: provider.Meta.Icon,
					BillingExpr: expression, BillingMode: mode,
					BillingUsageSchema: jsplugin.CloneUsageSchema(schema), BillingUsageExamples: jsplugin.CloneUsageExamples(examples),
				})
			}
		}
		channelIDs := make([]int, 0, len(modelChannelGroups[model]))
		for channelID := range modelChannelGroups[model] {
			channelIDs = append(channelIDs, channelID)
		}
		slices.Sort(channelIDs)
		for _, channelID := range channelIDs {
			channel := pricingChannels[channelID]
			variant := Pricing{
				ModelName: model, Description: pricing.Description, Icon: pricing.Icon, Tags: pricing.Tags,
				VendorID: pricing.VendorID, SupportedEndpointTypes: pricing.SupportedEndpointTypes,
				EnableGroup: modelChannelGroups[model][channelID],
			}
			if channel == nil {
				// A cache miss must not turn an unknown task/plugin binding into
				// a configured text price. Keep the model visible as unconfigured.
				pricing.ChannelPricingVariants = append(pricing.ChannelPricingVariants, variant)
				continue
			}
			mappedModel := model
			if target, cyclic := followChannelModelMapping(pricingModelMappings[channelID], model); !cyclic {
				mappedModel = target
			}
			providers := pricingChannelProviders(pluginGeneration, channel, model, mappedModel)
			for _, provider := range providers {
				variant, _ = resolveChannelPricing(pricing, channelID, provider, mappedModel, pluginGeneration.SharedModel(model) || pluginGeneration.SharedModel(mappedModel))
				variant.EnableGroup = modelChannelGroups[model][channelID]
				pricing.ChannelPricingVariants = append(pricing.ChannelPricingVariants, variant)
			}
			if len(providers) > 0 {
				continue
			}
			if channel.Type == constant.ChannelTypeTaskPlugin {
				pricing.ChannelPricingVariants = append(pricing.ChannelPricingVariants, variant)
				continue
			}
			variant, _ = resolveChannelPricing(pricing, channelID, nil, mappedModel, false)
			variant.EnableGroup = modelChannelGroups[model][channelID]
			pricing.ChannelPricingVariants = append(pricing.ChannelPricingVariants, variant)
		}
		pricingMap = append(pricingMap, pricing)
	}

	// 防止大更新后数据不通用
	if len(pricingMap) > 0 {
		pricingMap[0].PricingVersion = "5a90f2b86c08bd983a9a2e6d66c255f4eaef9c4bc934386d2b6ae84ef0ff1f1f"
	}

	// 刷新缓存映射，供高并发快速查询
	modelEnableGroupsLock.Lock()
	modelEnableGroups = make(map[string][]string)
	modelQuotaTypeMap = make(map[string]int)
	for _, p := range pricingMap {
		modelEnableGroups[p.ModelName] = p.EnableGroup
		modelQuotaTypeMap[p.ModelName] = p.QuotaType
	}
	modelEnableGroupsLock.Unlock()

	lastGetPricingTime = time.Now()
}

// GetSupportedEndpointMap 返回全局端点到路径的映射
func GetSupportedEndpointMap() map[string]common.EndpointInfo {
	return supportedEndpointMap
}
