package model

import (
	"maps"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// GetGroupKeyCandidates is a runtime view only: permissions, credential health
// and request constraints are applied before comparing effective priorities.
// Catalog/ability rows continue to describe configured access, not health.
func GetGroupKeyCandidates(group, modelName string, filters []dto.ChannelFilter, channelID int) ([]*Channel, bool, error) {
	var channels []*Channel
	configured := false
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		configured = groupKeyRoutingEnabled[group]
		if configured {
			if channelID != 0 {
				if channel := channelsIDM[channelID]; channel != nil && slices.Contains(channel.GetGroups(), group) {
					channels = append(channels, channel)
				}
			} else {
				ids := append([]int{}, group2model2channels[group][modelName]...)
				ids = append(ids, group2model2channels[group][ratio_setting.RoutingMatchModelName(modelName)]...)
				seen := make(map[int]bool)
				for _, id := range ids {
					if channel := channelsIDM[id]; channel != nil && !seen[id] {
						channels = append(channels, channel)
						seen[id] = true
					}
				}
			}
		}
		channelSyncLock.RUnlock()
	} else {
		if err := DB.Where(channelGroupFilterCondition(), channelGroupFilterPattern(group)).Find(&channels).Error; err != nil {
			return nil, false, err
		}
		for _, channel := range channels {
			for _, priorities := range channel.ChannelInfo.KeyGroupPriorities {
				if _, exists := priorities[group]; exists {
					configured = true
					break
				}
			}
		}
	}
	if !configured {
		return nil, false, nil
	}
	var exact, normalized []*Channel
	var probeError *ChannelProbeError
	batch := slices.ContainsFunc(filters, func(filter dto.ChannelFilter) bool { return filter.Kind == dto.FilterBatchCapable })
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled || channelID != 0 && channel.Id != channelID {
			continue
		}
		models := channel.GetModelsForGroup(group)
		isExact := slices.Contains(models, modelName)
		if channelID != 0 && !channel.HasGroupModelLimits() {
			isExact = true
		}
		if !isExact && !slices.Contains(models, ratio_setting.RoutingMatchModelName(modelName)) {
			continue
		}
		if ok, _ := ChannelSatisfiesFilters(channel, modelName, filters); !ok {
			continue
		}
		lock := GetChannelPollingLock(channel.Id)
		lock.Lock()
		snapshot := *channel
		snapshot.ChannelInfo.MultiKeyStatusList = maps.Clone(channel.ChannelInfo.MultiKeyStatusList)
		lock.Unlock()
		negative := channelNegativeBalanceKeysForRelay(&snapshot, batch)
		var failures map[int]*ChannelProbeError
		if !batch {
			failures = snapshot.probeKeyFailures(modelName)
		}
		seen := make(map[string]bool)
		for index, key := range snapshot.relayKeys() {
			if !snapshot.KeyAllowedForGroup(key, group) || negative[index] {
				continue
			}
			if snapshot.ChannelInfo.IsMultiKey {
				if status, exists := snapshot.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
					continue
				}
			}
			if failure := failures[index]; failure != nil {
				if probeError == nil || failure.ObservedAt > probeError.ObservedAt {
					probeError = failure
				}
				continue
			}
			actualKey := key
			if batch {
				_, actualKey = snapshot.GetBatchEndpoint(key)
				if !snapshot.KeyAllowedForGroup(actualKey, group) {
					continue
				}
			}
			identity := snapshot.KeyIdentity(actualKey)
			if seen[identity] {
				continue
			}
			seen[identity] = true
			candidate := snapshot
			candidate.RelayKeyIdentity = snapshot.KeyIdentity(key)
			candidate.RelayKeyPriority = snapshot.KeyPriorityForGroup(actualKey, group)
			candidate.RelayKeyIndex = index
			candidate.RelayCredentialIdentity = identity
			candidate.RelayModelExact = isExact
			if isExact {
				exact = append(exact, &candidate)
			} else {
				normalized = append(normalized, &candidate)
			}
		}
	}
	if len(exact)+len(normalized) > 0 {
		return append(exact, normalized...), true, nil
	}
	if probeError != nil {
		return nil, true, probeError
	}
	return nil, true, nil
}

// ReserveGroupKeyCandidate advances the existing polling cursor atomically
// within a selected tier. Setup later validates this reservation but does not
// allocate again, so interleaved requests cannot reuse or rewind its position.
func ReserveGroupKeyCandidate(candidates []*Channel) (*Channel, error) {
	channel := candidates[0]
	excluded := make(map[int]bool)
	for index := range channel.relayKeys() {
		excluded[index] = true
	}
	for _, candidate := range candidates {
		delete(excluded, candidate.RelayKeyIndex)
	}
	_, index, err := channel.nextEnabledKey(excluded)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.RelayKeyIndex == index {
			return candidate, nil
		}
	}
	return nil, ErrChannelDisabled
}
