package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PricingValues is one model's configuration, keyed by the existing option
// names. A missing key inherits the engine's default; an explicit zero is free.
type PricingValues map[string]any

type ModelPricingChange struct {
	ModelName       string        `json:"model_name"`
	ExpectedVersion string        `json:"expected_version"`
	Pricing         PricingValues `json:"pricing"`
	Reset           bool          `json:"reset,omitempty"`
}

type ModelPricingEntry struct {
	ModelPricingDescription
	PluginVariants []ModelPricingPluginVariant          `json:"plugin_variants,omitempty"`
	ModelName      string                               `json:"model_name"`
	Version        string                               `json:"version"`
	Configured     PricingValues                        `json:"configured"`
	UsageSchema    map[string]jsplugin.UsageFieldSchema `json:"usage_schema,omitempty"`
}

type ModelPricingPluginVariant struct {
	PluginKey     string                               `json:"plugin_key"`
	PluginName    string                               `json:"plugin_name"`
	Icon          string                               `json:"icon,omitempty"`
	UsageSchema   map[string]jsplugin.UsageFieldSchema `json:"usage_schema"`
	UsageExamples []jsplugin.UsageExample              `json:"usage_examples,omitempty"`
	Configured    string                               `json:"configured"`
	Effective     string                               `json:"effective"`
	Compatible    bool                                 `json:"compatible"`
	Stale         bool                                 `json:"stale,omitempty"`
}

type ModelPricingSnapshot struct {
	Entries      []ModelPricingEntry `json:"entries"`
	Options      map[string]string   `json:"options"`
	EmptyVersion string              `json:"empty_version"`
}

var ErrModelPricingConflict = errors.New("model pricing changed; reload before saving")

// Lock order is stable across instances. Creating missing option rows inside
// the transaction also serializes the first write to an unconfigured database.
var modelPricingOptionKeys = []string{
	"AudioCompletionRatio", "AudioRatio", "CacheRatio", "CompletionRatio",
	"CreateCacheRatio", "ImageRatio", "ModelPrice", "ModelRatio",
	"billing_setting.billing_expr", "billing_setting.billing_mode", billing_setting.PluginBillingExprOption,
	billing_setting.DeletedModelsOption,
}

var modelPricingMutationMu sync.Mutex

func IsModelPricingOption(key string) bool {
	return key != billing_setting.DeletedModelsOption && slices.Contains(modelPricingOptionKeys, key)
}

func ModelPricingVersion(values PricingValues) string {
	encoded, _ := common.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func modelPricingVersion(values map[string]map[string]any, name string) string {
	configured := modelPricingValues(values, name)
	// An inherited built-in is a real price, distinct from an empty/deleted
	// draft. Keep empty_version usable for recreating a deleted model while
	// rejecting a stale save opened before that built-in was deleted.
	if len(configured) == 0 && values[billing_setting.DeletedModelsOption][name] != true {
		if expression, ok := billing_setting.GetBuiltinBillingExpr(name); ok {
			return ModelPricingVersion(PricingValues{"billing_setting.billing_mode": billing_setting.BillingModeTieredExpr, "billing_setting.billing_expr": expression})
		}
	}
	return ModelPricingVersion(configured)
}

func defaultPricingMaps() map[string]map[string]any {
	result := make(map[string]map[string]any, len(modelPricingOptionKeys))
	for _, key := range modelPricingOptionKeys {
		result[key] = make(map[string]any)
	}
	for key, values := range ratio_setting.GetDefaultPricingMaps() {
		for name, value := range values {
			result[key][name] = value
		}
	}
	return result
}

func readModelPricingMaps(db *gorm.DB) (map[string]map[string]any, map[string]bool, []string, error) {
	var rows []Option
	if err := db.Where(commonKeyCol+" IN ?", modelPricingOptionKeys).Find(&rows).Error; err != nil {
		return nil, nil, nil, err
	}
	values := defaultPricingMaps()
	existing := make(map[string]bool)
	counts := make(map[string]int)
	for _, row := range rows {
		var entries map[string]any
		if err := common.UnmarshalJsonStr(row.Value, &entries); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", row.Key, err)
		}
		if entries == nil {
			return nil, nil, nil, fmt.Errorf("%s must be a JSON object", row.Key)
		}
		if row.Key == billing_setting.DeletedModelsOption {
			for name, value := range entries {
				if _, ok := value.(bool); !ok {
					return nil, nil, nil, fmt.Errorf("%s: %s must be a boolean", row.Key, name)
				}
			}
		}
		values[row.Key] = entries
		existing[row.Key] = true
		counts[row.Key]++
	}
	var duplicated []string
	for _, key := range modelPricingOptionKeys {
		if counts[key] > 1 {
			duplicated = append(duplicated, key)
		}
	}
	return values, existing, duplicated, nil
}

func modelPricingValues(values map[string]map[string]any, name string) PricingValues {
	result := make(PricingValues)
	for _, key := range modelPricingOptionKeys {
		if key == billing_setting.DeletedModelsOption {
			continue
		}
		if key == billing_setting.PluginBillingExprOption {
			variants := make(map[string]any)
			for variant, expression := range values[key] {
				if plugin, model, ok := billing_setting.SplitPluginBillingExprKey(variant); ok && model == name {
					variants[plugin] = expression
				}
			}
			if len(variants) > 0 {
				result[key] = variants
			}
			continue
		}
		if value, exists := values[key][name]; exists {
			result[key] = value
		}
	}
	return result
}

// replaceModelPricing writes one complete model draft into the option maps.
// Plugin expressions are grouped in the draft and flattened only in storage.
func replaceModelPricing(values map[string]map[string]any, name string, draft PricingValues) {
	for _, key := range modelPricingOptionKeys {
		if key == billing_setting.DeletedModelsOption {
			continue
		}
		if key == billing_setting.PluginBillingExprOption {
			for variant := range values[key] {
				if _, model, ok := billing_setting.SplitPluginBillingExprKey(variant); ok && model == name {
					delete(values[key], variant)
				}
			}
			variants, _ := draft[key].(map[string]any)
			for plugin, expr := range variants {
				values[key][billing_setting.PluginBillingExprKey(plugin, name)] = expr
			}
			continue
		}
		delete(values[key], name)
		if value, exists := draft[key]; exists {
			values[key][name] = value
		}
	}
	if len(draft) == 0 {
		values[billing_setting.DeletedModelsOption][name] = true
	} else {
		delete(values[billing_setting.DeletedModelsOption], name)
	}
}

func effectiveModelPricing(values map[string]map[string]any, name string) PricingValues {
	result := modelPricingValues(values, name)
	// Legacy wildcard aliases are resolved by the same normalization as relay.
	alias := ratio_setting.FormatMatchingModelName(name)
	for _, key := range []string{"ModelPrice", "ModelRatio", "CompletionRatio", "AudioRatio", "AudioCompletionRatio"} {
		delete(result, key)
		if value, exists := values[key][alias]; exists {
			result[key] = value
		}
	}
	mode, _ := result["billing_setting.billing_mode"].(string)
	if mode == "" && values[billing_setting.DeletedModelsOption][name] != true {
		_, hasPrice := result["ModelPrice"]
		_, hasRatio := result["ModelRatio"]
		if _, builtin := billing_setting.GetBuiltinBillingExpr(name); builtin && !hasPrice && !hasRatio {
			mode = "tiered_expr"
		}
	}
	if mode == "tiered_expr" {
		result["billing_setting.billing_mode"] = mode
		if _, exists := result["billing_setting.billing_expr"]; !exists {
			if expression, ok := billing_setting.GetBuiltinBillingExpr(name); ok {
				result["billing_setting.billing_expr"] = expression
			}
		}
		return result
	}
	if _, exists := result["ModelPrice"]; exists {
		return result
	}
	if _, exists := result["ModelRatio"]; !exists && operation_setting.SelfUseModeEnabled {
		result["ModelRatio"] = float64(37.5)
	}
	// Completion ratios include engine-enforced model defaults. Expose their
	// effective value without persisting them into the editable configuration.
	var configuredCompletion *float64
	if ratio, exists := result["CompletionRatio"].(float64); exists {
		configuredCompletion = &ratio
	}
	result["CompletionRatio"] = ratio_setting.ResolveCompletionRatio(name, configuredCompletion).Ratio
	// The supplied snapshot (or replaced draft) is authoritative. Reading the
	// process-wide ratio maps here would restore deleted prices during preview.
	// In particular, a fallback audio multiplier of 1 does not configure an
	// audio lane: its absence determines which legacy settlement path applies.
	for key, fallback := range map[string]float64{
		"CacheRatio":       ratio_setting.DefaultCacheRatio,
		"CreateCacheRatio": ratio_setting.DefaultCreateCacheRatio,
		"ImageRatio":       ratio_setting.DefaultImageRatio,
	} {
		if _, exists := result[key]; !exists {
			result[key] = fallback
		}
	}
	return result
}

// PreviewModelPricing resolves a complete editable draft using the same defaults
// as the saved-price display and conversion. It has no write side effects.
func PreviewModelPricing(name string, draft PricingValues) (PricingValues, error) {
	if draft == nil {
		return nil, errors.New("pricing draft is required")
	}
	values, _, _, err := readModelPricingMaps(DB)
	if err != nil {
		return nil, err
	}
	if err := validateModelPricing(DB, name, draft, modelPricingValues(values, name)); err != nil {
		return nil, err
	}
	replaceModelPricing(values, name, draft)
	return effectiveModelPricing(values, name), nil
}

// pluginPricingModels returns the declared usage profiles a provider-specific
// price must support. An alias is editable only where enabled channel abilities
// actually bind its plugin. Several channels may map it to different profiles.
func pluginPricingModels(db *gorm.DB, g *jsplugin.RoutingGeneration, name string) (map[string][]string, error) {
	targets := make(map[string][]string)
	if declared, ok := g.CanonicalModel(name); ok {
		for _, plugin := range g.PluginsByModel(declared) {
			targets[plugin.Meta.Key] = []string{declared}
		}
		return targets, nil
	}
	if db == nil {
		return targets, nil
	}
	ids := db.Model(&Ability{}).Select("channel_id").Where("model = ? AND enabled = ?", name, true)
	var channels []Channel
	if err := db.Select("id", "type", "setting", "model_mapping").Where("status = ? AND id IN (?) AND model_mapping IS NOT NULL", common.ChannelStatusEnabled, ids).Find(&channels).Error; err != nil {
		return nil, err
	}
	for _, channel := range channels {
		var mapping map[string]string
		if common.UnmarshalJsonStr(channel.GetModelMapping(), &mapping) != nil {
			continue
		}
		mapped, cyclic := followChannelModelMapping(mapping, name)
		declared, found := g.CanonicalModel(mapped)
		if cyclic || !found {
			continue
		}
		for _, plugin := range pricingChannelProviders(g, &channel, name, mapped) {
			key := plugin.Meta.Key
			if !slices.Contains(targets[key], declared) {
				targets[key] = append(targets[key], declared)
			}
		}
	}
	for _, models := range targets {
		slices.Sort(models)
	}
	return targets, nil
}

func GetModelPricingSnapshot(names []string) (*ModelPricingSnapshot, error) {
	values, _, _, err := readModelPricingMaps(DB)
	if err != nil {
		return nil, err
	}
	builtins := billing_setting.GetBuiltinBillingExprCopy()
	if len(names) == 0 {
		nameSet := make(map[string]bool)
		for key, entries := range values {
			if key == billing_setting.DeletedModelsOption {
				continue
			}
			for name := range entries {
				if key == billing_setting.PluginBillingExprOption {
					_, model, ok := billing_setting.SplitPluginBillingExprKey(name)
					if !ok {
						continue
					}
					name = model
				}
				nameSet[name] = true
			}
		}
		// A built-in price is listed once it is saved or an enabled channel
		// serves its model; unused built-ins stay out of the full snapshot.
		for _, name := range GetEnabledModels() {
			if _, builtin := builtins[name]; builtin {
				nameSet[name] = true
			}
		}
		maps.DeleteFunc(builtins, func(name, _ string) bool { return !nameSet[name] })
		for name := range nameSet {
			if values[billing_setting.DeletedModelsOption][name] != true {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	result := &ModelPricingSnapshot{Entries: make([]ModelPricingEntry, 0, len(names)), Options: make(map[string]string), EmptyVersion: ModelPricingVersion(PricingValues{})}
	generation := jsplugin.DefaultRegistry.Generation()
	for _, name := range names {
		configured := modelPricingValues(values, name)
		entry := ModelPricingEntry{ModelName: name, Version: modelPricingVersion(values, name), Configured: configured,
			ModelPricingDescription: ModelPricingDescription{Effective: effectiveModelPricing(values, name)}}
		entry.CacheWriteMode = ResolveCacheWriteMode(name, configured)
		entry.BillingDetails = ResolveLegacyBillingDetails(name, entry.Effective, configured)
		if plugin, ok := generation.GetByModel(name); ok {
			entry.UsageSchema, _ = plugin.Meta.UsageForModel(name)
		} else if target, ok := ResolveTaskModelAlias(generation, name); ok {
			if plugin, ok := generation.Get(target.PluginKey); ok {
				entry.UsageSchema, _ = plugin.Meta.UsageForModel(target.Declared)
			}
		}
		plugins := generation.PluginsByModel(name)
		configuredVariants, _ := configured[billing_setting.PluginBillingExprOption].(map[string]any)
		if len(plugins) > 0 || len(configuredVariants) > 0 {
			targets, err := pluginPricingModels(DB, generation, name)
			if err != nil {
				return nil, err
			}
			keys := make(map[string]bool, len(plugins)+len(configuredVariants))
			for _, plugin := range plugins {
				keys[plugin.Meta.Key] = true
			}
			for key := range configuredVariants {
				keys[key] = true
			}
			for _, key := range slices.Sorted(maps.Keys(keys)) {
				configuredValue, overridden := configuredVariants[key]
				configuredExpr, _ := configuredValue.(string)
				plugin, exists := generation.Get(key)
				if !exists || len(targets[key]) == 0 {
					variant := ModelPricingPluginVariant{
						PluginKey: key, PluginName: key, Configured: configuredExpr,
						UsageSchema: map[string]jsplugin.UsageFieldSchema{}, Stale: true,
					}
					if exists {
						variant.PluginName, variant.Icon = plugin.Meta.Name, plugin.Meta.Icon
					}
					entry.PluginVariants = append(entry.PluginVariants, variant)
					continue
				}
				schema, examples := plugin.Meta.UsageForModel(targets[key][0])
				if schema == nil {
					schema = map[string]jsplugin.UsageFieldSchema{}
				}
				expression := configuredExpr
				if !overridden && entry.Effective["billing_setting.billing_mode"] == billing_setting.BillingModeTieredExpr {
					expression, _ = entry.Effective["billing_setting.billing_expr"].(string)
				}
				compatible := !slices.ContainsFunc(targets[key], func(declared string) bool {
					profile, _ := plugin.Meta.UsageForModel(declared)
					return !billing_setting.TaskExprCompatible(expression, profile)
				})
				entry.PluginVariants = append(entry.PluginVariants, ModelPricingPluginVariant{
					PluginKey: plugin.Meta.Key, PluginName: plugin.Meta.Name, Icon: plugin.Meta.Icon,
					UsageSchema: schema, UsageExamples: examples, Configured: configuredExpr, Effective: expression,
					Compatible: compatible,
				})
			}
		}
		result.Entries = append(result.Entries, entry)
	}
	// Preserve the existing settings editor's full-map interface. Built-in
	// expressions are display defaults only; per-model writes do not persist them.
	for name, expression := range builtins {
		effective := effectiveModelPricing(values, name)
		if effective["billing_setting.billing_mode"] != "tiered_expr" {
			continue
		}
		if _, ok := values["billing_setting.billing_mode"][name]; !ok {
			values["billing_setting.billing_mode"][name] = "tiered_expr"
		}
		if _, ok := values["billing_setting.billing_expr"][name]; !ok {
			values["billing_setting.billing_expr"][name] = expression
		}
	}
	for key, entries := range values {
		if key == billing_setting.DeletedModelsOption {
			continue
		}
		encoded, err := common.Marshal(entries)
		if err != nil {
			return nil, err
		}
		result.Options[key] = string(encoded)
	}
	return result, nil
}

// Reasons the configured price cannot be used.
const (
	ModelPricingIssueUnconfigured      = "unconfigured"
	ModelPricingIssueIncompatible      = "incompatible"
	ModelPricingIssueStale             = "stale"
	ModelPricingIssueInvalidExpression = "invalid_expression"
	ModelPricingIssueInvalidTaskPrice  = "invalid_task_price"
)

type ModelPricingIssue struct {
	PluginBillingModel          string                               `json:"plugin_billing_model,omitempty"`
	ChannelBillingOverrideModel string                               `json:"channel_billing_override_model,omitempty"`
	UsageSchema                 map[string]jsplugin.UsageFieldSchema `json:"usage_schema,omitempty"`
	UsageExamples               []jsplugin.UsageExample              `json:"usage_examples,omitempty"`
	ChannelSharedByPlugins      bool                                 `json:"channel_shared_by_plugins,omitempty"`
	ChannelID                   int                                  `json:"channel_id,omitempty"`
	ChannelName                 string                               `json:"channel_name,omitempty"`
	ModelName                   string                               `json:"model_name"`
	PluginKey                   string                               `json:"plugin_key"`
	PluginName                  string                               `json:"plugin_name"`
	Reason                      string                               `json:"reason"`
}

// GetRoutedModelPricingIssues reports the effective prices of enabled routes.
// Different groups sharing a channel/model have the same price. The plugin
// maintenance API below separately checks the entire plugin catalog.
func GetRoutedModelPricingIssues() ([]ModelPricingIssue, error) {
	var channels []Channel
	if err := DB.Select("id", "type", "name", "setting", "model_mapping").Where("status = ?", common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}
	var routes []Ability
	if err := DB.Model(&Ability{}).Distinct("channel_id", "model").Where("enabled = ?", true).Order("channel_id, model").Find(&routes).Error; err != nil {
		return nil, err
	}
	modelsByChannel := make(map[int][]string)
	for _, route := range routes {
		modelsByChannel[route.ChannelId] = append(modelsByChannel[route.ChannelId], route.Model)
	}
	generation := jsplugin.DefaultRegistry.Generation()
	issues := make([]ModelPricingIssue, 0)
	for _, channel := range channels {
		models := modelsByChannel[channel.Id]
		if len(models) == 0 {
			continue
		}
		var mapping map[string]string
		if raw := channel.GetModelMapping(); raw != "" && common.UnmarshalJsonStr(raw, &mapping) != nil {
			continue // malformed relay mapping cannot serve a model
		}
		for _, name := range models {
			mapped, cyclic := followChannelModelMapping(mapping, name)
			if cyclic {
				continue
			}
			declared, direct := generation.CanonicalModel(name)
			providers := pricingChannelProviders(generation, &channel, name, mapped)
			if !direct && mapped != name {
				declared, _ = generation.CanonicalModel(mapped)
				if declared != "" {
					if alias, found := ResolveTaskModelAlias(generation, name); !found {
						providers = nil
					} else if alias.Declared == "" {
						// The generic /v1/tasks/:key entry still accepts an alias
						// resolving to different models of this same plugin.
						providers = slices.DeleteFunc(providers, func(plugin *jsplugin.LoadedPlugin) bool {
							return plugin.Meta.Key != alias.PluginKey
						})
					} else if alias.Declared != declared {
						providers = nil
					}
				}
			}
			if len(providers) == 0 {
				if channel.Type == constant.ChannelTypeTaskPlugin {
					continue
				}
				if channel.Type == constant.ChannelTypeNewAPI && declared != "" {
					// A plugin-native route requires a matching binding. Shared
					// host protocols can still execute on ordinary relay.
					if len(ordinaryEndpointPlugins(generation, declared)) == 0 {
						continue
					}
				}
				providers = []*jsplugin.LoadedPlugin{nil}
			}
			for _, provider := range providers {
				billingName := name
				if provider != nil && direct {
					billingName = declared
				}
				pricing, reason := resolveChannelPricing(Pricing{ModelName: billingName}, channel.Id, provider, mapped, generation.SharedModel(billingName) || generation.SharedModel(mapped))
				if reason == "" {
					continue
				}
				issue := ModelPricingIssue{ModelName: name, ChannelID: channel.Id, ChannelName: channel.Name, Reason: reason}
				if provider != nil {
					issue.PluginKey, issue.PluginName = provider.Meta.Key, provider.Meta.Name
					issue.UsageSchema, issue.UsageExamples = pricing.BillingUsageSchema, pricing.BillingUsageExamples
					issue.PluginBillingModel = billingName
					if _, pinsClient := billing_setting.GetPluginBillingExpr(provider.Meta.Key, billingName); !pinsClient {
						if tail, ok := generation.CanonicalModel(mapped); ok && slices.Contains(provider.Meta.Models, tail) {
							if _, pinsMapped := billing_setting.GetPluginBillingExpr(provider.Meta.Key, mapped); pinsMapped {
								issue.PluginBillingModel = mapped
							} else if !direct {
								issue.PluginBillingModel = tail
							}
						}
					}
					issue.ChannelSharedByPlugins = len(providers) > 1
					issue.ChannelBillingOverrideModel, _, _, _ = taskChannelBillingMode(billingName, mapped, channel.Id)
				}
				issues = append(issues, issue)
			}
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].ModelName != issues[j].ModelName {
			return issues[i].ModelName < issues[j].ModelName
		}
		if issues[i].ChannelID != issues[j].ChannelID {
			return issues[i].ChannelID < issues[j].ChannelID
		}
		return issues[i].PluginKey < issues[j].PluginKey
	})
	return issues, nil
}

// saveChannelModelPricing validates only changed channel prices against the
// providers that will actually charge them, using the persisted option as its
// baseline. Holding the row lock through the write keeps concurrent saves
// from exempting an override that is no longer present.
func saveChannelModelPricing(tx *gorm.DB, value string) error {
	row := Option{Key: "ChannelModelPricing"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := lockForUpdate(tx).Where(&Option{Key: row.Key}).First(&row).Error; err != nil {
		return err
	}
	entries, err := ratio_setting.ChangedChannelBillingExpressions(row.Value, value)
	if err != nil {
		return err
	}
	ids := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		ids[entry.ChannelId] = struct{}{}
	}
	if len(ids) > 0 {
		var channels []Channel
		if err = tx.Select("id", "type", "setting", "model_mapping").Where("id IN ?", slices.Collect(maps.Keys(ids))).Find(&channels).Error; err != nil {
			return err
		}
		byID := make(map[int]*Channel, len(channels))
		for i := range channels {
			byID[channels[i].Id] = &channels[i]
		}
		generation := jsplugin.DefaultRegistry.Generation()
		var aliases *taskAliasView
		for _, entry := range entries {
			var providers []*jsplugin.LoadedPlugin
			mapped := entry.Model
			if channel := byID[entry.ChannelId]; channel != nil {
				var mapping map[string]string
				if raw := channel.GetModelMapping(); raw != "" {
					if err := common.UnmarshalJsonStr(raw, &mapping); err != nil {
						return fmt.Errorf("invalid model mapping on channel %d: %w", channel.Id, err)
					}
					var cyclic bool
					if mapped, cyclic = followChannelModelMapping(mapping, entry.Model); cyclic {
						return fmt.Errorf("cyclic model mapping for model %s on channel %d", entry.Model, channel.Id)
					}
				}
				providers = pricingChannelProviders(generation, channel, entry.Model, mapped)
			}
			if len(providers) == 0 {
				if !billingexpr.UsesFixedPricing(entry.Expr) {
					// Load the alias view through this transaction, including on
					// SQLite installations with a single database connection.
					if mapped != entry.Model && aliases == nil {
						aliases, err = readTaskAliasView(generation, tx)
						if err != nil {
							return err
						}
					}
					if ordinaryRouteSharesTaskPrice(entry.Model, mapped, ResolveBillingModelName(entry.Model, entry.ChannelId), aliases) {
						return fmt.Errorf("billing expression for model %s on channel %d: configure a separate plugin price or use fixed pricing on this shared endpoint", entry.Model, entry.ChannelId)
					}
				}
				if err := billing_setting.SmokeTestExpr(entry.Expr); err != nil {
					return fmt.Errorf("billing expression for model %s on channel %d: %w", entry.Model, entry.ChannelId, err)
				}
			}
			for _, provider := range providers {
				schema, _ := provider.Meta.UsageForModels(mapped, entry.Model)
				if err := billing_setting.SmokeTestTaskExpr(entry.Expr, schema); err != nil {
					return fmt.Errorf("billing expression for model %s on channel %d, plugin %s: %w", entry.Model, entry.ChannelId, provider.Meta.Key, err)
				}
			}
		}
	}
	return tx.Model(&row).Update("value", value).Error
}

// GetModelPricingIssues reads the provider variants of the pricing snapshot for
// every model a live plugin declares and an enabled channel serves, and every
// model a saved provider price names. A channel serves a declared model under
// its own spelling, a case variant or a channel alias. A provider of a served
// model with neither an expression nor a per-call price is unconfigured, and
// one whose expression reads usage its schema lacks is incompatible. A saved
// provider price whose plugin is not installed or whose installed version no
// longer declares the model is stale, whether or not a channel serves it. A
// plugin switched off keeps its prices for when it is switched back on. An
// empty pluginKey covers every plugin.
func GetModelPricingIssues(pluginKey string) ([]ModelPricingIssue, error) {
	generation := jsplugin.DefaultRegistry.Generation()
	served := make(map[string]bool)
	for _, name := range GetEnabledModels() {
		if declared, ok := generation.CanonicalModel(name); ok {
			served[declared] = true
		} else if target, ok := ResolveTaskModelAlias(generation, name); ok && target.Declared != "" {
			served[target.Declared] = true
		}
	}
	names := make(map[string]bool)
	for _, plugin := range generation.Plugins() {
		if pluginKey == "" || plugin.Meta.Key == pluginKey {
			for _, name := range plugin.Meta.Models {
				if served[name] {
					names[name] = true
				}
			}
		}
	}
	// Models of the installed versions of plugins with saved prices that are
	// missing from the live generation, such as plugins switched off.
	stopped := make(map[string][]string)
	for key := range billing_setting.GetPluginBillingExprCopy() {
		plugin, name, ok := billing_setting.SplitPluginBillingExprKey(key)
		if !ok || (pluginKey != "" && plugin != pluginKey) {
			continue
		}
		names[name] = true
		if _, live := generation.Get(plugin); live {
			continue
		}
		if _, read := stopped[plugin]; read {
			continue
		}
		models, err := installedTaskPluginModels(plugin)
		if err != nil {
			return nil, err
		}
		stopped[plugin] = models
	}
	issues := make([]ModelPricingIssue, 0)
	if len(names) == 0 {
		return issues, nil
	}
	snapshot, err := GetModelPricingSnapshot(slices.Collect(maps.Keys(names)))
	if err != nil {
		return nil, err
	}
	for _, entry := range snapshot.Entries {
		// A model in expression mode never falls back to its per-call price.
		_, perCall := entry.Effective["ModelPrice"]
		perCall = perCall && entry.Effective["billing_setting.billing_mode"] != billing_setting.BillingModeTieredExpr
		for _, variant := range entry.PluginVariants {
			if pluginKey != "" && variant.PluginKey != pluginKey {
				continue
			}
			reason := ModelPricingIssueUnconfigured
			switch {
			case variant.Stale && slices.Contains(stopped[variant.PluginKey], entry.ModelName):
				continue
			case variant.Stale:
				reason = ModelPricingIssueStale
			case !served[entry.ModelName], variant.Compatible:
				continue
			// fixed() marks ordinary channel pricing: the provider has no task price.
			case variant.Effective != "" && !billingexpr.UsesFixedPricing(variant.Effective):
				reason = ModelPricingIssueIncompatible
			case perCall:
				continue
			}
			issues = append(issues, ModelPricingIssue{ModelName: entry.ModelName, PluginKey: variant.PluginKey, PluginName: variant.PluginName, Reason: reason})
		}
	}
	return issues, nil
}

// installedTaskPluginModels returns the models declared by the installed version
// of a plugin: its active override row, or else its built-in. A plugin that is
// not installed declares none, and neither does a stored source that no longer
// compiles.
func installedTaskPluginModels(key string) ([]string, error) {
	row, err := GetTaskPluginVersion(key, "")
	if err == nil {
		loaded, compileErr := jsplugin.CompilePlugin(string(row.Source), jsplugin.Options{Key: row.Key, Version: row.Version})
		if compileErr != nil {
			return nil, nil
		}
		return loaded.Meta.Models, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	for _, meta := range jsplugin.DefaultRegistry.Snapshot().Factory {
		if meta.Key == key {
			return meta.Models, nil
		}
	}
	return nil, nil
}

func ValidateModelPricing(name string, values PricingValues) error {
	variants := make(map[string]any)
	for key, expression := range billing_setting.GetPluginBillingExprCopy() {
		if plugin, model, ok := billing_setting.SplitPluginBillingExprKey(key); ok && model == name {
			variants[plugin] = expression
		}
	}
	previous := PricingValues{billing_setting.PluginBillingExprOption: variants}
	if expression, ok := billing_setting.GetBillingExpr(name); ok {
		previous["billing_setting.billing_expr"] = expression
	}
	return validateModelPricing(DB, name, values, previous)
}

// Writes pass the locked database snapshot here, so allowing an unchanged stale
// override cannot bypass validation through an out-of-date process-local cache.
func validateModelPricing(db *gorm.DB, name string, values, previous PricingValues) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("model name is required")
	}
	generation := jsplugin.DefaultRegistry.Generation()
	previousVariants, _ := previous[billing_setting.PluginBillingExprOption].(map[string]any)
	variants := map[string]any{}
	if value, exists := values[billing_setting.PluginBillingExprOption]; exists {
		var ok bool
		variants, ok = value.(map[string]any)
		if !ok || variants == nil {
			return errors.New("plugin billing expressions must be a plugin-to-expression object")
		}
	}
	var targets map[string][]string
	if _, hasExpression := values["billing_setting.billing_expr"]; len(variants) > 0 || hasExpression {
		var err error
		targets, err = pluginPricingModels(db, generation, name)
		if err != nil {
			return err
		}
	}
	for key, value := range variants {
		expression, ok := value.(string)
		if !ok || strings.TrimSpace(expression) == "" {
			return fmt.Errorf("model %s: plugin %s: billing expression is required", name, key)
		}
		plugin, exists := generation.Get(key)
		models := targets[key]
		declared := exists && len(models) > 0
		// An unchanged override stays saveable after its plugin disappears or
		// stops declaring the model. A sole provider also keeps it after
		// narrowing its usage profile; relay checks shared models against
		// each profile, so their prices must follow it.
		if previousVariants[key] == expression && (!declared || len(targets) == 1 && !generation.SharedModel(models[0])) {
			continue
		}
		if !declared {
			return fmt.Errorf("model %s: plugin %s does not declare this model", name, key)
		}
		for _, target := range models {
			schema, _ := plugin.Meta.UsageForModel(target)
			if err := billing_setting.SmokeTestTaskExpr(expression, schema); err != nil {
				return fmt.Errorf("model %s: plugin %s: %w", name, key, err)
			}
		}
	}
	for key, value := range values {
		if key == billing_setting.PluginBillingExprOption {
			continue
		}
		if !IsModelPricingOption(key) {
			return fmt.Errorf("unsupported pricing field: %s", key)
		}
		if key == "billing_setting.billing_mode" {
			if value != "ratio" && value != "tiered_expr" {
				return errors.New("invalid billing mode")
			}
			continue
		}
		if key == "billing_setting.billing_expr" {
			expression, ok := value.(string)
			if !ok || strings.TrimSpace(expression) == "" {
				return errors.New("billing expression is required")
			}
			// Even a model expression currently shadowed by every provider must
			// compile; only its schema-specific smoke tests can be skipped.
			if _, err := billingexpr.CompileFromCache(expression); err != nil {
				return fmt.Errorf("model %s: %w", name, err)
			}
			// An unchanged expression a sole provider has been charging with keeps
			// its price after the plugin narrows its usage profile; relay checks
			// shared models against each profile, so their prices must follow it.
			unchanged := previous[key] == expression
			var err error
			if len(targets) == 0 && generation != nil {
				// Global alias prices can be prepared before the channel's
				// abilities or plugin binding exist. Provider overrides above
				// still require an enabled, bound route.
				aliases := taskAliasViewPtr.Load()
				if !taskAliasViewFresh(aliases, generation.Number) {
					aliases, err = readTaskAliasView(generation, db)
					if err != nil {
						return err
					}
				}
				if alias, found := aliases.byFold[jsplugin.ASCIIFold(name)]; found {
					targets = map[string][]string{alias.PluginKey: {alias.Declared}}
				}
			}
			if len(targets) > 0 {
				for pluginKey, models := range targets {
					_, overridden := variants[pluginKey]
					_, wasOverridden := previousVariants[pluginKey]
					if overridden {
						continue
					}
					plugin, _ := generation.Get(pluginKey)
					for _, declared := range models {
						if unchanged && !wasOverridden && len(targets) == 1 && !generation.SharedModel(declared) {
							continue
						}
						schema, _ := plugin.Meta.UsageForModel(declared)
						if err := billing_setting.SmokeTestTaskExpr(expression, schema); err != nil {
							return fmt.Errorf("model %s: plugin %s: %w", name, pluginKey, err)
						}
					}
				}
			} else if !unchanged || len(billingexpr.UsedUsageKeys(expression)) == 0 {
				err = billing_setting.SmokeTestExpr(expression)
			}
			// With no remaining plugin, an unchanged stored usage expression has
			// no schema to test. Preserve it so removing stale overrides or saving
			// other model prices does not become impossible.
			if err != nil {
				return fmt.Errorf("model %s: %w", name, err)
			}
			continue
		}
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return fmt.Errorf("%s must be a finite, non-negative number", key)
		}
	}
	if values["billing_setting.billing_mode"] == "tiered_expr" {
		if _, exists := values["billing_setting.billing_expr"]; !exists {
			if _, builtin := billing_setting.GetBuiltinBillingExpr(name); !builtin {
				return errors.New("billing expression is required")
			}
		}
	}
	return nil
}

func UpdateModelPricing(changes []ModelPricingChange) error {
	if len(changes) == 0 {
		return errors.New("select model pricing changes before saving")
	}
	seen := make(map[string]bool)
	for _, change := range changes {
		if seen[change.ModelName] {
			return errors.New("duplicate model pricing change")
		}
		seen[change.ModelName] = true
		if change.ExpectedVersion == "" {
			return ErrModelPricingConflict
		}
	}
	return mutateModelPricingOptions(func(tx *gorm.DB, values map[string]map[string]any) error {
		defaults := defaultPricingMaps()
		for _, change := range changes {
			previous := modelPricingValues(values, change.ModelName)
			if modelPricingVersion(values, change.ModelName) != change.ExpectedVersion {
				return fmt.Errorf("%w: %s", ErrModelPricingConflict, change.ModelName)
			}
			pricing := change.Pricing
			if change.Reset {
				pricing = modelPricingValues(defaults, change.ModelName)
			}
			if err := validateModelPricing(tx, change.ModelName, pricing, previous); err != nil {
				return err
			}
			replaceModelPricing(values, change.ModelName, pricing)
			if change.Reset {
				delete(values[billing_setting.DeletedModelsOption], change.ModelName)
			}
		}
		return nil
	})
}

// UpdateModelPricingOptions keeps legacy single-option callers on the same
// locking, validation and transaction path as the model-level API.
func UpdateModelPricingOptions(updates map[string]string) error {
	return mutateModelPricingOptions(func(tx *gorm.DB, values map[string]map[string]any) error {
		previous := maps.Clone(values)
		names := make(map[string]bool)
		for key, raw := range updates {
			if !IsModelPricingOption(key) {
				return fmt.Errorf("unsupported pricing field: %s", key)
			}
			var entries map[string]any
			if err := common.UnmarshalJsonStr(raw, &entries); err != nil {
				return err
			}
			if entries == nil {
				return fmt.Errorf("%s must be a JSON object", key)
			}
			for _, entriesForKey := range []map[string]any{values[key], entries} {
				for name := range entriesForKey {
					if key == billing_setting.PluginBillingExprOption {
						_, model, ok := billing_setting.SplitPluginBillingExprKey(name)
						if !ok {
							return fmt.Errorf("invalid plugin billing expression key: %s", name)
						}
						name = model
					}
					names[name] = true
				}
			}
			values[key] = entries
		}
		for name := range names {
			pricing, before := modelPricingValues(values, name), modelPricingValues(previous, name)
			if err := validateModelPricing(tx, name, pricing, before); err != nil {
				return err
			}
			if len(pricing) > 0 {
				delete(values[billing_setting.DeletedModelsOption], name)
			} else if len(before) > 0 {
				values[billing_setting.DeletedModelsOption][name] = true
			}
		}
		return nil
	})
}

func mutateModelPricingOptions(mutate func(*gorm.DB, map[string]map[string]any) error) error {
	modelPricingMutationMu.Lock()
	defer modelPricingMutationMu.Unlock()
	var committed map[string]map[string]any
	err := DB.Transaction(func(tx *gorm.DB) error {
		values, existing, duplicated, err := readModelPricingMaps(lockForUpdate(tx))
		if err != nil {
			return err
		}
		if len(duplicated) > 0 {
			common.SysError(common.LogText("options table has duplicate pricing keys [%s]; the table is missing a primary key", strings.Join(duplicated, ", ")))
		}
		defaults := defaultPricingMaps()
		for _, key := range modelPricingOptionKeys {
			if existing[key] {
				continue
			}
			encoded, err := common.Marshal(defaults[key])
			if err != nil {
				return err
			}
			row := Option{Key: key, Value: string(encoded)}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		if err := mutate(tx, values); err != nil {
			return err
		}
		for _, key := range modelPricingOptionKeys {
			encoded, err := common.Marshal(values[key])
			if err != nil {
				return err
			}
			if err := tx.Model(&Option{}).Where(commonKeyCol+" = ?", key).Update("value", string(encoded)).Error; err != nil {
				return err
			}
		}
		committed = values
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range modelPricingOptionKeys {
		encoded, _ := common.Marshal(committed[key])
		if err := updateOptionMap(key, string(encoded)); err != nil {
			return err
		}
	}
	RefreshPricing()
	ratio_setting.InvalidateExposedDataCache()
	return nil
}
