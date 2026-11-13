package model

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type GroupKeyRoutingItem struct {
	ChannelID         int    `json:"channel_id"`
	ChannelName       string `json:"channel_name"`
	KeyIndex          int    `json:"key_index"`
	KeyIdentity       string `json:"key_identity"`
	KeyPreview        string `json:"key_preview"`
	ChannelPriority   int64  `json:"channel_priority"`
	Priority          *int64 `json:"priority"`
	EffectivePriority int64  `json:"effective_priority"`
	Status            int    `json:"status"`
}

type GroupKeyRoutingOrder struct {
	Group    string                `json:"group"`
	Revision string                `json:"revision"`
	Items    []GroupKeyRoutingItem `json:"items"`
}

type GroupKeyRoutingChange struct {
	ChannelID   int    `json:"channel_id"`
	KeyIdentity string `json:"key_identity"`
	Priority    *int64 `json:"priority"`
}

type GroupKeyRoutingUpdate struct {
	Group    string                  `json:"group"`
	Revision string                  `json:"revision"`
	Items    []GroupKeyRoutingChange `json:"items"`
}

// relayKeys keeps single composite credentials intact.
func (channel *Channel) relayKeys() []string {
	if !channel.ChannelInfo.IsMultiKey {
		return []string{channel.Key}
	}
	return channel.GetKeys()
}

func (channel *Channel) KeyPriorityForGroup(key, group string) int64 {
	if priority, exists := channel.ChannelInfo.KeyGroupPriorities[channel.KeyIdentity(key)][group]; exists {
		return priority
	}
	return channel.GetPriority()
}

func (channel *Channel) retainKeyGroupPriorities(keys []string) {
	retained := make(map[string]map[string]int64)
	for _, key := range keys {
		identity := channel.KeyIdentity(key)
		if priorities := channel.ChannelInfo.KeyGroupPriorities[identity]; len(priorities) > 0 {
			retained[identity] = maps.Clone(priorities)
		}
	}
	channel.ChannelInfo.KeyGroupPriorities = retained
}

func groupKeyRoutingOrder(group string, channels []*Channel) (*GroupKeyRoutingOrder, error) {
	order := &GroupKeyRoutingOrder{Group: group, Items: []GroupKeyRoutingItem{}}
	// Include permissions and mode, but never credential material or volatile
	// balance/probe/cost data, in optimistic concurrency validation.
	type revisionChannel struct {
		ID            int
		Groups        string
		Multi         bool
		Status        int
		KeyGroups     map[string][]string
		KeyIdentities []string
		KeyStatuses   map[int]int
	}
	revisions := make([]revisionChannel, 0, len(channels))
	for _, channel := range channels {
		identities := make([]string, 0, len(channel.relayKeys()))
		for _, key := range channel.relayKeys() {
			identities = append(identities, channel.KeyIdentity(key))
		}
		revisions = append(revisions, revisionChannel{channel.Id, channel.Group, channel.ChannelInfo.IsMultiKey, channel.Status, channel.ChannelInfo.KeyGroups, identities, channel.ChannelInfo.MultiKeyStatusList})
		seen := make(map[string]bool)
		for index, key := range channel.relayKeys() {
			identity := channel.KeyIdentity(key)
			if seen[identity] || !channel.KeyAllowedForGroup(key, group) {
				continue
			}
			seen[identity] = true
			preview := MaskTokenKey(key)
			if len(key) <= 10 || strings.ContainsAny(key, "{|[") {
				preview = "********"
			}
			status := channel.Status
			if status == common.ChannelStatusEnabled && channel.ChannelInfo.IsMultiKey {
				if configured, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists {
					status = configured
				}
			}
			var override *int64
			if priority, exists := channel.ChannelInfo.KeyGroupPriorities[identity][group]; exists {
				override = &priority
			}
			order.Items = append(order.Items, GroupKeyRoutingItem{channel.Id, channel.Name, index, identity, preview, channel.GetPriority(), override, channel.KeyPriorityForGroup(key, group), status})
		}
	}
	encoded, err := common.Marshal([]any{group, order.Items, revisions})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	order.Revision = hex.EncodeToString(digest[:])
	return order, nil
}

func GetGroupKeyRouting(group string) (*GroupKeyRoutingOrder, error) {
	if strings.TrimSpace(group) != group || group == "" || len(group) > 64 {
		return nil, common.NewMessage("Invalid parameters")
	}
	var channels []*Channel
	if err := DB.Where(channelGroupFilterCondition(), channelGroupFilterPattern(group)).Order("id").Find(&channels).Error; err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, common.NewMessage("Invalid parameters")
	}
	return groupKeyRoutingOrder(group, channels)
}

// SetGroupKeyRouting updates only submitted overrides. Locking each current
// channel row in ID order preserves concurrent status, permission and cost edits.
func SetGroupKeyRouting(update GroupKeyRoutingUpdate) (*GroupKeyRoutingOrder, error) {
	if update.Group == "" || strings.TrimSpace(update.Group) != update.Group || len(update.Group) > 64 || update.Revision == "" || len(update.Items) > 10000 {
		return nil, common.NewMessage("Invalid parameters")
	}
	var result *GroupKeyRoutingOrder
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channels []*Channel
		if err := lockForUpdate(tx).Where(channelGroupFilterCondition(), channelGroupFilterPattern(update.Group)).Order("id").Find(&channels).Error; err != nil {
			return err
		}
		current, err := groupKeyRoutingOrder(update.Group, channels)
		if err != nil {
			return err
		}
		if len(channels) == 0 || current.Revision != update.Revision {
			return common.NewMessage("The routing configuration changed. Refresh and try again.")
		}
		byID := make(map[int]*Channel, len(channels))
		for _, channel := range channels {
			byID[channel.Id] = channel
		}
		changed := make(map[int]bool)
		seen := make(map[int]map[string]bool)
		for _, item := range update.Items {
			channel := byID[item.ChannelID]
			if channel == nil || item.KeyIdentity == "" || item.Priority != nil && (*item.Priority < -2147483647 || *item.Priority > 2147483647) {
				return common.NewMessage("Invalid parameters")
			}
			if seen[item.ChannelID] == nil {
				seen[item.ChannelID] = make(map[string]bool)
			}
			if seen[item.ChannelID][item.KeyIdentity] {
				return common.NewMessage("Invalid parameters")
			}
			seen[item.ChannelID][item.KeyIdentity] = true
			valid := slices.ContainsFunc(channel.relayKeys(), func(key string) bool {
				return channel.KeyIdentity(key) == item.KeyIdentity && channel.KeyAllowedForGroup(key, update.Group)
			})
			if !valid {
				return common.NewMessage("The routing configuration changed. Refresh and try again.")
			}
			if channel.ChannelInfo.KeyGroupPriorities == nil {
				channel.ChannelInfo.KeyGroupPriorities = make(map[string]map[string]int64)
			}
			priorities := channel.ChannelInfo.KeyGroupPriorities[item.KeyIdentity]
			if item.Priority == nil {
				delete(priorities, update.Group)
				if len(priorities) == 0 {
					delete(channel.ChannelInfo.KeyGroupPriorities, item.KeyIdentity)
				}
			} else {
				if priorities == nil {
					priorities = make(map[string]int64)
				}
				priorities[update.Group] = *item.Priority
				channel.ChannelInfo.KeyGroupPriorities[item.KeyIdentity] = priorities
			}
			changed[channel.Id] = true
		}
		for _, channel := range channels {
			if changed[channel.Id] {
				if err := tx.Model(channel).Update("channel_info", channel.ChannelInfo).Error; err != nil {
					return err
				}
			}
		}
		result, err = groupKeyRoutingOrder(update.Group, channels)
		return err
	})
	return result, err
}
