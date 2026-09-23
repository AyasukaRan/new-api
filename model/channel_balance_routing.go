package model

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

// The routing cache contains only configuration digests and key positions.
// Other instances see completed checks within fifteen seconds; the instance
// recording a check invalidates its entry immediately after committing it.
type channelBalanceRoutingEntry struct {
	mu            sync.Mutex
	database      *gorm.DB
	expires       time.Time
	configuration string
	negativeKeys  map[int]bool
}

var channelBalanceRoutingCache sync.Map

func InvalidateChannelBalanceRouting(channelID int) {
	if value, ok := channelBalanceRoutingCache.Load(channelID); ok {
		entry := value.(*channelBalanceRoutingEntry)
		entry.mu.Lock()
		entry.expires = time.Time{}
		entry.mu.Unlock()
	}
}

func channelNegativeBalanceKeys(channel *Channel) map[int]bool {
	if channel == nil || channel.Id <= 0 || DB == nil {
		return nil
	}
	configuration, err := channelBalanceConfigurationHash(channel)
	if err != nil {
		return nil
	}
	value, _ := channelBalanceRoutingCache.LoadOrStore(channel.Id, &channelBalanceRoutingEntry{})
	entry := value.(*channelBalanceRoutingEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.database != DB || !time.Now().Before(entry.expires) {
		if entry.database != DB {
			entry.configuration = ""
			entry.negativeKeys = nil
		}
		entry.database = DB
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var sample ChannelBalanceSample
		err := DB.WithContext(ctx).Select("id", "configuration_hash", "key_balances_json").
			Where("channel_id = ?", channel.Id).Order("id DESC").Limit(1).Find(&sample).Error
		if err == nil {
			entry.configuration = ""
			entry.negativeKeys = nil
			var balances []ChannelKeyBalance
			if sample.ID > 0 && common.UnmarshalJsonStr(sample.KeyBalancesJSON, &balances) == nil {
				entry.configuration = sample.ConfigurationHash
				entry.negativeKeys = make(map[int]bool)
				for _, balance := range balances {
					known := balance.LastKnownBalance
					if balance.Balance != nil && balance.Error == "" {
						known = balance.Balance
					}
					if balance.Index >= 0 && known != nil &&
						!math.IsNaN(*known) && !math.IsInf(*known, 0) && *known < 0 {
						entry.negativeKeys[balance.Index] = true
					}
				}
			}
		}
		entry.expires = time.Now().Add(15 * time.Second)
	}
	if entry.configuration != configuration {
		return nil
	}
	return entry.negativeKeys
}

// HasRelayBalance excludes a channel only when every enabled credential has a
// confirmed negative balance for this exact configuration. Unknown readings
// and zero balances do not establish that a credential is unusable.
func (channel *Channel) HasRelayBalance() bool {
	return channel.hasRelayBalance(channelNegativeBalanceKeys(channel))
}

// HasRelayKeyBalance checks a credential already bound to a persistent
// connection without rotating the channel's key selection cursor.
func (channel *Channel) HasRelayKeyBalance(index int) bool {
	if channel == nil || index < 0 {
		return false
	}
	if channel.ChannelInfo.IsMultiKey {
		if index >= len(channel.GetKeys()) {
			return false
		}
	} else if index != 0 {
		return false
	}
	return !channelNegativeBalanceKeys(channel)[index]
}

func (channel *Channel) HasRelayBalanceForRequest(filters []dto.ChannelFilter) bool {
	for _, filter := range filters {
		if filter.Kind == dto.FilterBatchCapable {
			return channel.hasRelayBalance(channelNegativeBalanceKeysForRelay(channel, true))
		}
	}
	return channel.HasRelayBalance()
}

// A separate batch account has no reading in the main credential snapshot.
// When its override reuses a monitored key, check that actual key instead of
// the preliminary key selected before the batch override is applied.
func channelNegativeBalanceKeysForRelay(channel *Channel, batch bool) map[int]bool {
	if channel == nil {
		return nil
	}
	if !batch {
		return channelNegativeBalanceKeys(channel)
	}
	setting := kitdto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		if common.UnmarshalJsonStr(*channel.Setting, &setting) != nil {
			return nil
		}
	}
	if baseURL := strings.TrimRight(strings.TrimSpace(setting.BatchBaseURL), "/"); baseURL != "" && baseURL != strings.TrimRight(channel.GetBaseURL(), "/") {
		return nil
	}
	negativeKeys := channelNegativeBalanceKeys(channel)
	if key := strings.TrimSpace(setting.BatchKey); key != "" {
		keys := []string{channel.Key}
		if channel.ChannelInfo.IsMultiKey {
			keys = channel.GetKeys()
		}
		for index, candidate := range keys {
			if candidate == key && negativeKeys[index] {
				blocked := make(map[int]bool, len(keys))
				for index := range keys {
					blocked[index] = true
				}
				return blocked
			}
		}
		return nil
	}
	return negativeKeys
}

func (channel *Channel) hasRelayBalance(negativeKeys map[int]bool) bool {
	if channel == nil {
		return false
	}
	if len(negativeKeys) == 0 {
		return true
	}
	if !channel.ChannelInfo.IsMultiKey {
		return !negativeKeys[0]
	}
	lock := GetChannelPollingLock(channel.Id)
	lock.Lock()
	defer lock.Unlock()
	for index := range channel.GetKeys() {
		status, exists := channel.ChannelInfo.MultiKeyStatusList[index]
		if (!exists || status == common.ChannelStatusEnabled) && !negativeKeys[index] {
			return true
		}
	}
	return false
}
