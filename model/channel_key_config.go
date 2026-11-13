package model

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

type ChannelKeyConfig struct {
	KeyIdentity       string   `json:"key_identity,omitempty"`
	KeyIndex          *int     `json:"key_index,omitempty"`
	Key               string   `json:"key,omitempty"`
	Groups            []string `json:"groups"`
	UpstreamCostRatio *float64 `json:"upstream_cost_ratio"`
	Enabled           *bool    `json:"enabled,omitempty"`
}

type ChannelKeyConfigView struct {
	KeyIdentity                string   `json:"key_identity"`
	KeyIndex                   int      `json:"key_index"`
	KeyPreview                 string   `json:"key_preview"`
	Groups                     []string `json:"groups"`
	UpstreamCostRatio          *float64 `json:"upstream_cost_ratio"`
	EffectiveUpstreamCostRatio *float64 `json:"effective_upstream_cost_ratio"`
	Status                     int      `json:"status"`
}

type ChannelKeyConfigUpdate struct {
	Items    []ChannelKeyConfig
	Revision string
}

// OrderedKeyIndices keeps credential storage indexes stable for health records.
func (channel *Channel) OrderedKeyIndices() []int {
	keys := channel.relayKeys()
	indices := make([]int, 0, len(keys))
	byIdentity := make(map[string]int, len(keys))
	for index, key := range keys {
		identity := channel.KeyIdentity(key)
		previous, exists := byIdentity[identity]
		previousStatus := channel.ChannelInfo.MultiKeyStatusList[previous]
		status := channel.ChannelInfo.MultiKeyStatusList[index]
		if !exists || previousStatus > common.ChannelStatusEnabled && status <= common.ChannelStatusEnabled {
			byIdentity[identity] = index
		}
	}
	for _, identity := range channel.ChannelInfo.KeyOrder {
		if index, exists := byIdentity[identity]; exists {
			indices = append(indices, index)
			delete(byIdentity, identity)
		}
	}
	for index, key := range keys {
		identity := channel.KeyIdentity(key)
		if selected, exists := byIdentity[identity]; exists && selected == index {
			indices = append(indices, index)
			delete(byIdentity, identity)
		}
	}
	return indices
}

func (channel *Channel) KeyConfiguration() ([]ChannelKeyConfigView, string, error) {
	// Validate without GetSetting's legacy repair/write path in this read view.
	var setting dto.ChannelSettings
	if channel.Setting != nil && *channel.Setting != "" {
		_ = common.UnmarshalJsonStr(*channel.Setting, &setting)
	}
	keys := channel.relayKeys()
	identities := make([]string, len(keys))
	for index, key := range keys {
		identities[index] = channel.KeyIdentity(key)
	}
	items := make([]ChannelKeyConfigView, 0, len(keys))
	for _, index := range channel.OrderedKeyIndices() {
		key, identity := keys[index], identities[index]
		preview := MaskTokenKey(key)
		if len(key) <= 10 || strings.ContainsAny(key, "{|[") {
			preview = "********"
		}
		status := common.ChannelStatusEnabled
		if configured, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists {
			status = configured
		}
		var cost *float64
		if configured, exists := channel.ChannelInfo.KeyCostRatios[identity]; exists {
			cost = &configured
		}
		effectiveCost := cost
		if effectiveCost == nil {
			effectiveCost = setting.UpstreamCostRatio
		}
		costSetting := dto.ChannelSettings{UpstreamCostRatio: effectiveCost}
		if costSetting.ValidateUpstreamCostRatio() != nil {
			effectiveCost = nil
		}
		items = append(items, ChannelKeyConfigView{identity, index, preview, append([]string{}, channel.ChannelInfo.KeyGroups[identity]...), cost, effectiveCost, status})
	}
	encoded, err := common.Marshal([]any{channel.Id, channel.Type, channel.Group, channel.Status, channel.ChannelInfo.IsMultiKey, channel.ChannelInfo.MultiKeyMode, channel.ChannelInfo.KeyOrder, channel.ChannelInfo.MultiKeyStatusList, identities, items, setting.UpstreamCostRatio})
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return items, hex.EncodeToString(digest[:]), nil
}

// ApplyKeyConfigs replaces the editor snapshot while retaining surviving keys'
// physical order. New credentials append; drag order lives only in KeyOrder.
func (channel *Channel) ApplyKeyConfigs(configs []ChannelKeyConfig, creating bool) error {
	if channel.Setting != nil && *channel.Setting != "" {
		var setting dto.ChannelSettings
		if err := common.UnmarshalJsonStr(*channel.Setting, &setting); err != nil {
			return common.NewMessage("Invalid parameters")
		}
	}
	var otherSettings dto.ChannelOtherSettings
	if channel.OtherSettings != "" {
		if err := common.UnmarshalJsonStr(channel.OtherSettings, &otherSettings); err != nil {
			return common.NewMessage("Invalid parameters")
		}
	}
	if len(configs) == 0 || len(configs) > 10000 || channel.Type == constant.ChannelTypeCodex || channel.Type == constant.ChannelTypeTaskPlugin || channel.Type == constant.ChannelTypeVertexAi && otherSettings.VertexKeyType != dto.VertexKeyTypeAPIKey {
		return common.NewMessage("Invalid parameters")
	}
	original := channel.relayKeys()
	if creating {
		original = nil
	}
	selected := make(map[string]ChannelKeyConfig, len(configs))
	order := make([]string, 0, len(configs))
	newKeys := make([]string, 0)
	for _, config := range configs {
		identity := config.KeyIdentity
		if identity != "" {
			if creating || config.Key != "" || config.KeyIndex == nil || *config.KeyIndex < 0 || *config.KeyIndex >= len(original) || channel.KeyIdentity(original[*config.KeyIndex]) != identity {
				return common.NewMessage("The key list changed. Refresh and try again.")
			}
		} else {
			key := strings.TrimSpace(config.Key)
			if config.KeyIndex != nil || key == "" || strings.ContainsAny(key, "\r\n") || strings.HasPrefix(key, "{") || strings.HasPrefix(key, "[") {
				return common.NewMessage("Invalid parameters")
			}
			identity = channel.KeyIdentity(key)
			if slices.Contains(original, key) {
				return common.NewMessage("Invalid parameters")
			}
			newKeys = append(newKeys, key)
		}
		if _, exists := selected[identity]; exists {
			return common.NewMessage("Invalid parameters")
		}
		costSetting := dto.ChannelSettings{UpstreamCostRatio: config.UpstreamCostRatio}
		if err := costSetting.ValidateUpstreamCostRatio(); err != nil {
			return common.NewMessage("Invalid parameters")
		}
		groups := make([]string, 0, len(config.Groups))
		for _, group := range config.Groups {
			if strings.TrimSpace(group) != group || group == "" || !slices.Contains(channel.GetGroups(), group) {
				return common.NewMessage("Key groups must be a subset of the channel groups.")
			}
			if !slices.Contains(groups, group) {
				groups = append(groups, group)
			}
		}
		config.Groups = groups
		selected[identity] = config
		order = append(order, identity)
	}
	keys := make([]string, 0, len(configs))
	statuses, reasons, times := make(map[int]int), make(map[int]string), make(map[int]int64)
	for index, key := range original {
		identity := channel.KeyIdentity(key)
		config, keep := selected[identity]
		if !keep || config.KeyIndex == nil || *config.KeyIndex != index {
			continue
		}
		next := len(keys)
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists {
			statuses[next] = status
		}
		if reason, exists := channel.ChannelInfo.MultiKeyDisabledReason[index]; exists {
			reasons[next] = reason
		}
		if timestamp, exists := channel.ChannelInfo.MultiKeyDisabledTime[index]; exists {
			times[next] = timestamp
		}
		keys = append(keys, key)
	}
	keys = append(keys, newKeys...)
	channel.ChannelInfo.KeyGroups = make(map[string][]string)
	channel.ChannelInfo.KeyCostRatios = make(map[string]float64)
	channel.ChannelInfo.KeyGroupPriorities = maps.Clone(channel.ChannelInfo.KeyGroupPriorities)
	for index, key := range keys {
		identity := channel.KeyIdentity(key)
		config := selected[identity]
		if len(config.Groups) > 0 {
			channel.ChannelInfo.KeyGroups[identity] = config.Groups
		}
		if config.UpstreamCostRatio != nil {
			channel.ChannelInfo.KeyCostRatios[identity] = *config.UpstreamCostRatio
		}
		if config.Enabled != nil {
			delete(reasons, index)
			delete(times, index)
			if *config.Enabled {
				delete(statuses, index)
			} else {
				statuses[index] = common.ChannelStatusManuallyDisabled
				times[index] = common.GetTimestamp()
			}
		}
	}
	channel.Key, channel.Keys = strings.Join(keys, "\n"), nil
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = len(keys)
	channel.ChannelInfo.KeyOrder = order
	channel.ChannelInfo.MultiKeyStatusList = statuses
	channel.ChannelInfo.MultiKeyDisabledReason = reasons
	channel.ChannelInfo.MultiKeyDisabledTime = times
	channel.retainKeyGroupPriorities(keys)
	info := channel.GetOtherInfo()
	if info == nil {
		info = make(map[string]any)
	}
	if !hasEnabledMultiKey(keys, statuses) && channel.Status == common.ChannelStatusEnabled {
		channel.Status = common.ChannelStatusManuallyDisabled
		info["status_reason"] = ChannelStatusReasonAllKeysDisabled
		info["status_time"] = common.GetTimestamp()
		channel.SetOtherInfo(info)
	} else if hasEnabledMultiKey(keys, statuses) && (channel.Status == common.ChannelStatusManuallyDisabled || channel.Status == common.ChannelStatusAutoDisabled) && info["status_reason"] == ChannelStatusReasonAllKeysDisabled {
		channel.Status = common.ChannelStatusEnabled
		info["status_reason"] = ""
		info["status_time"] = common.GetTimestamp()
		channel.SetOtherInfo(info)
	}
	return nil
}
