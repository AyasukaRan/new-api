package model

import (
	"crypto/hmac"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

// UpstreamCostRatioForKey freezes the actual credential's override, including
// zero. An absent override inherits the channel's optional estimate.
func (channel *Channel) UpstreamCostRatioForKey(key string) *float64 {
	ratio, exists := channel.ChannelInfo.KeyCostRatios[channel.KeyIdentity(key)]
	if !exists {
		configured := channel.GetSetting().UpstreamCostRatio
		if configured == nil {
			return nil
		}
		ratio = *configured
	}
	settings := dto.ChannelSettings{UpstreamCostRatio: &ratio}
	if settings.ValidateUpstreamCostRatio() != nil {
		return nil
	}
	return &ratio
}

func (channel *Channel) retainKeyCostRatios(keys []string) {
	retained := make(map[string]float64)
	for _, key := range keys {
		identity := channel.KeyIdentity(key)
		if ratio, exists := channel.ChannelInfo.KeyCostRatios[identity]; exists {
			retained[identity] = ratio
		}
	}
	channel.ChannelInfo.KeyCostRatios = retained
}

// SetChannelKeyCostRatio is called under the channel polling lock. The row
// lock preserves concurrent key permissions and status changes across nodes.
func SetChannelKeyCostRatio(channelID, index int, identity string, ratio *float64) error {
	settings := dto.ChannelSettings{UpstreamCostRatio: ratio}
	if err := settings.ValidateUpstreamCostRatio(); err != nil {
		return common.NewMessage("Invalid parameters")
	}
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
		channel.retainKeyCostRatios(keys)
		if ratio == nil {
			delete(channel.ChannelInfo.KeyCostRatios, identity)
		} else {
			channel.ChannelInfo.KeyCostRatios[identity] = *ratio
		}
		return tx.Model(&channel).Update("channel_info", channel.ChannelInfo).Error
	})
}
