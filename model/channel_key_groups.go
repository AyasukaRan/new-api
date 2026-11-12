package model

import (
	"crypto/hmac"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"gorm.io/gorm"
)

// KeyIdentity identifies a credential without disclosing it. The credential is
// the HMAC key so rotating a server secret cannot erase saved group restrictions.
func (channel *Channel) KeyIdentity(key string) string {
	return common.GenerateHMACWithKey([]byte(key), "new-api:channel-key:v1:"+strconv.Itoa(channel.Id))
}

func (channel *Channel) KeyAllowedForGroup(key, group string) bool {
	if !slices.Contains(channel.GetGroups(), group) {
		return false
	}
	groups := channel.ChannelInfo.KeyGroups[channel.KeyIdentity(key)]
	return len(groups) == 0 || slices.Contains(groups, group)
}

// HasKeyForGroup is the catalog permission view; transient key health does not
// change the advertised models or require rebuilding abilities on recovery.
func (channel *Channel) HasKeyForGroup(group string) bool {
	if !channel.ChannelInfo.IsMultiKey || len(channel.ChannelInfo.KeyGroups) == 0 {
		return slices.Contains(channel.GetGroups(), group)
	}
	for _, key := range channel.GetKeys() {
		if channel.KeyAllowedForGroup(key, group) {
			return true
		}
	}
	return false
}

func (channel *Channel) HasEnabledKeyForGroup(group string) bool {
	for index, key := range channel.GetKeys() {
		status, explicit := channel.ChannelInfo.MultiKeyStatusList[index]
		if (!explicit || status == common.ChannelStatusEnabled) && channel.KeyAllowedForGroup(key, group) {
			return true
		}
	}
	return false
}

// RelayKeyAvailabilityForGroup applies health only to credentials this group
// may use. Catalogs keep using HasKeyForGroup, independently of transient health.
func (channel *Channel) RelayKeyAvailabilityForGroup(group, modelName string, filters []dto.ChannelFilter) (bool, *ChannelProbeError) {
	lock := GetChannelPollingLock(channel.Id)
	lock.Lock()
	snapshot := *channel
	snapshot.ChannelInfo.MultiKeyStatusList = maps.Clone(channel.ChannelInfo.MultiKeyStatusList)
	lock.Unlock()
	if !snapshot.HasKeyForGroup(group) {
		return false, nil
	}
	if snapshot.ChannelInfo.IsMultiKey {
		if snapshot.ChannelInfo.MultiKeyStatusList == nil {
			snapshot.ChannelInfo.MultiKeyStatusList = make(map[int]int)
		}
		for index, key := range snapshot.GetKeys() {
			if !snapshot.KeyAllowedForGroup(key, group) {
				snapshot.ChannelInfo.MultiKeyStatusList[index] = common.ChannelStatusManuallyDisabled
			}
		}
		if !hasEnabledMultiKey(snapshot.GetKeys(), snapshot.ChannelInfo.MultiKeyStatusList) {
			return false, nil
		}
		for _, filter := range filters {
			if filter.Kind == dto.FilterBatchCapable {
				_, batchKey := snapshot.GetBatchEndpoint("")
				if batchKey != "" && !snapshot.KeyAllowedForGroup(batchKey, group) {
					return false, nil
				}
			}
		}
	}
	if failure := snapshot.RelayProbeError(modelName, filters); failure != nil {
		return false, failure
	}
	return snapshot.HasRelayBalanceForRequest(filters), nil
}

// retainKeyGroups removes only deleted credentials. Removing a channel group
// must never turn an explicit restriction into inheritance of every group.
func (channel *Channel) retainKeyGroups(keys []string) {
	retained := make(map[string][]string)
	for _, key := range keys {
		identity := channel.KeyIdentity(key)
		if groups := channel.ChannelInfo.KeyGroups[identity]; len(groups) > 0 {
			retained[identity] = slices.Clone(groups)
		}
	}
	channel.ChannelInfo.KeyGroups = retained
}

// SetChannelKeyGroups is called under the channel polling lock. The database
// lock also serializes this permission change with other gateway instances.
func SetChannelKeyGroups(channelID, index int, identity string, groups []string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if !channel.ChannelInfo.IsMultiKey {
			return common.NewMessage("This channel is not in multi-key mode")
		}
		keys := channel.GetKeys()
		if index < 0 || index >= len(keys) {
			return common.NewMessage("Key index out of range")
		}
		if !hmac.Equal([]byte(identity), []byte(channel.KeyIdentity(keys[index]))) {
			return common.NewMessage("The key list changed. Refresh and try again.")
		}
		allowed := channel.GetGroups()
		normalized := make([]string, 0, len(groups))
		for _, group := range groups {
			group = strings.TrimSpace(group)
			if group == "" || !slices.Contains(allowed, group) {
				return common.NewMessage("Key groups must be a subset of the channel groups.")
			}
			if !slices.Contains(normalized, group) {
				normalized = append(normalized, group)
			}
		}
		channel.retainKeyGroups(keys)
		if len(normalized) == 0 {
			delete(channel.ChannelInfo.KeyGroups, identity)
		} else {
			channel.ChannelInfo.KeyGroups[identity] = normalized
		}
		if err := tx.Model(&channel).Update("channel_info", channel.ChannelInfo).Error; err != nil {
			return err
		}
		return channel.UpdateAbilities(tx)
	})
}
