package model

import (
	"errors"
	"math"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

// ChannelKeyBalance identifies a credential only by its position. Never store
// credentials, upstream response bodies, or upstream errors in monitoring data.
type ChannelKeyBalance struct {
	Index            int      `json:"index"`
	Balance          *float64 `json:"balance"`
	Error            string   `json:"error,omitempty"`
	LastKnownBalance *float64 `json:"last_known_balance,omitempty"`
}

type ChannelBalanceMonitor struct {
	CheckedAt            int64               `json:"checked_at"`
	Balance              *float64            `json:"balance"`
	BalanceUpdatedTime   int64               `json:"balance_updated_time"`
	KnownBalance         float64             `json:"known_balance"`
	Partial              bool                `json:"partial"`
	Success              bool                `json:"success"`
	ConfigurationChanged bool                `json:"configuration_changed"`
	KeyBalances          []ChannelKeyBalance `json:"key_balances"`
}

// ChannelBalanceSample is a monitoring observation, independent of channel
// configuration and routing state. Balance carries the last complete reading;
// KnownBalance is the sum of the keys that answered this particular check.
type ChannelBalanceSample struct {
	ID                 int64               `json:"id" gorm:"primaryKey"`
	ChannelID          int                 `json:"channel_id" gorm:"index:idx_channel_balance_time,priority:1"`
	CheckedAt          int64               `json:"checked_at" gorm:"bigint;index:idx_channel_balance_time,priority:2"`
	StartedAt          int64               `json:"-" gorm:"bigint"`
	ConfigurationHash  string              `json:"-" gorm:"type:varchar(64)"`
	Balance            *float64            `json:"balance"`
	BalanceUpdatedTime int64               `json:"balance_updated_time" gorm:"bigint"`
	KnownBalance       float64             `json:"known_balance"`
	Partial            bool                `json:"partial"`
	Success            bool                `json:"success"`
	UsedQuota          int64               `json:"used_quota" gorm:"bigint"`
	KeyBalances        []ChannelKeyBalance `json:"key_balances" gorm:"-"`
	KeyBalancesJSON    string              `json:"-" gorm:"type:text"`
}

var ErrChannelBalanceConfigurationChanged = errors.New("channel balance configuration changed; retry the balance query")
var ErrChannelBalanceQueryDisabled = errors.New("channel balance query is disabled")

// CheckBalanceQueryEnabled reads the setting without modifying malformed
// configuration. Callers starting a query must use a freshly loaded channel.
func (channel *Channel) CheckBalanceQueryEnabled() error {
	setting := dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		if err := common.UnmarshalJsonStr(*channel.Setting, &setting); err != nil {
			return err
		}
	}
	if setting.BalanceQueryDisabled {
		return ErrChannelBalanceQueryDisabled
	}
	return nil
}

// channelBalanceConfigurationHash ties indexed readings to the exact ordered
// keys and billing configuration. The HMAC is internal and cannot reveal keys.
func channelBalanceConfigurationHash(channel *Channel) (string, error) {
	// Only query inputs identify an account snapshot. Background model-list
	// timestamps and unrelated relay preferences must not invalidate balances.
	setting := dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		if err := common.UnmarshalJsonStr(*channel.Setting, &setting); err != nil {
			return "", err
		}
	}
	other := dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		if err := common.UnmarshalJsonStr(channel.OtherSettings, &other); err != nil {
			return "", err
		}
	}
	balanceRoute, _ := other.AdvancedCustom.BalanceRoute()
	configuration := struct {
		Key                 string
		Type                int
		BaseURL             string
		Proxy               string
		BalanceQueryType    string
		BalanceQueryBaseURL string
		BalanceRoute        dto.AdvancedCustomRoute
		HeaderOverride      *string
		IsMultiKey          bool
	}{channel.Key, channel.Type, channel.GetBaseURL(), setting.Proxy,
		setting.BalanceQueryType, setting.BalanceQueryBaseURL, balanceRoute,
		channel.HeaderOverride, channel.ChannelInfo.IsMultiKey}
	encoded, err := common.Marshal(configuration)
	if err != nil {
		return "", err
	}
	return common.GenerateHMAC(string(encoded)), nil
}

// RecordChannelBalanceSample merges only balance fields under the channel row
// lock. An in-flight query cannot overwrite a replacement/reordering of keys,
// key status, polling state, or a newer balance check.
func RecordChannelBalanceSample(channel *Channel, sample *ChannelBalanceSample) error {
	if channel == nil || channel.Id <= 0 || sample == nil {
		return errors.New("a saved channel is required for balance monitoring")
	}
	if math.IsNaN(sample.KnownBalance) || math.IsInf(sample.KnownBalance, 0) {
		return errors.New("invalid channel balance")
	}
	configurationHash, err := channelBalanceConfigurationHash(channel)
	if err != nil {
		return err
	}
	for _, key := range sample.KeyBalances {
		if key.Balance != nil && (math.IsNaN(*key.Balance) || math.IsInf(*key.Balance, 0)) {
			return errors.New("invalid key balance")
		}
	}
	sample.ChannelID = channel.Id
	sample.ConfigurationHash = configurationHash

	err = DB.Transaction(func(tx *gorm.DB) error {
		var current Channel
		if err := lockForUpdate(tx).First(&current, channel.Id).Error; err != nil {
			return err
		}
		if err := current.CheckBalanceQueryEnabled(); err != nil {
			return err
		}
		currentHash, err := channelBalanceConfigurationHash(&current)
		if err != nil {
			return err
		}
		if currentHash != configurationHash {
			return ErrChannelBalanceConfigurationChanged
		}
		var previous ChannelBalanceSample
		previousErr := tx.Where("channel_id = ?", channel.Id).Order("id DESC").First(&previous).Error
		if previousErr != nil && !errors.Is(previousErr, gorm.ErrRecordNotFound) {
			return previousErr
		}
		if previousErr == nil && previous.StartedAt > sample.StartedAt {
			return errors.New("a newer channel balance check has completed; retry the balance query")
		}
		previousBalances := make(map[int]*float64)
		if previousErr == nil && previous.ConfigurationHash == configurationHash {
			sample.Balance = previous.Balance
			sample.BalanceUpdatedTime = previous.BalanceUpdatedTime
			if previous.KeyBalancesJSON != "" {
				var keys []ChannelKeyBalance
				if err := common.UnmarshalJsonStr(previous.KeyBalancesJSON, &keys); err != nil {
					return err
				}
				for _, key := range keys {
					known := key.LastKnownBalance
					if key.Balance != nil && key.Error == "" {
						known = key.Balance
					}
					previousBalances[key.Index] = known
				}
			}
		} else if errors.Is(previousErr, gorm.ErrRecordNotFound) && current.BalanceUpdatedTime > 0 {
			// Preserve the last known full balance when upgrading an existing
			// installation, including if its first monitored check fails.
			balance := current.Balance
			sample.Balance = &balance
			sample.BalanceUpdatedTime = current.BalanceUpdatedTime
		}
		for index := range sample.KeyBalances {
			key := &sample.KeyBalances[index]
			known := previousBalances[key.Index]
			if key.Balance != nil && key.Error == "" {
				known = key.Balance
			}
			key.LastKnownBalance = nil
			if known != nil {
				value := *known
				key.LastKnownBalance = &value
			}
		}
		keyJSON, err := common.Marshal(sample.KeyBalances)
		if err != nil {
			return err
		}
		sample.KeyBalancesJSON = string(keyJSON)
		sample.UsedQuota = current.UsedQuota
		if sample.Success && !sample.Partial {
			balance := sample.KnownBalance
			sample.Balance = &balance
			sample.BalanceUpdatedTime = sample.CheckedAt
			if err := tx.Model(&current).Updates(map[string]any{
				"balance": balance, "balance_updated_time": sample.CheckedAt,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Create(sample).Error
	})
	if err == nil {
		InvalidateChannelBalanceRouting(channel.Id)
	}
	return err
}

func GetChannelBalanceMonitor(channelID int) (*ChannelBalanceMonitor, error) {
	channel, err := GetChannelById(channelID, true)
	if err != nil {
		return nil, err
	}
	if err := PopulateChannelBalanceMonitors([]*Channel{channel}); err != nil {
		return nil, err
	}
	return channel.BalanceMonitor, nil
}

// PopulateChannelBalanceMonitors uses two batched queries, including a private
// configuration read because admin channel lists intentionally omit keys.
func PopulateChannelBalanceMonitors(channels []*Channel) error {
	if len(channels) == 0 {
		return nil
	}
	ids := make([]int, 0, len(channels))
	for _, channel := range channels {
		if channel != nil {
			ids = append(ids, channel.Id)
		}
	}
	var configurations []*Channel
	if err := DB.Select("id", "key", "type", "base_url", "setting", "settings", "header_override", "channel_info").Where("id IN ?", ids).Find(&configurations).Error; err != nil {
		return err
	}
	configurationHashes := make(map[int]string, len(configurations))
	for _, channel := range configurations {
		configurationHash, err := channelBalanceConfigurationHash(channel)
		if err != nil {
			return err
		}
		configurationHashes[channel.Id] = configurationHash
	}
	var samples []ChannelBalanceSample
	latestIDs := DB.Model(&ChannelBalanceSample{}).Select("MAX(id)").Where("channel_id IN ?", ids).Group("channel_id")
	if err := DB.Where("id IN (?)", latestIDs).Find(&samples).Error; err != nil {
		return err
	}
	monitors := make(map[int]*ChannelBalanceMonitor, len(samples))
	for _, sample := range samples {
		monitor := &ChannelBalanceMonitor{KeyBalances: []ChannelKeyBalance{}}
		if configurationHashes[sample.ChannelID] != sample.ConfigurationHash {
			monitor.ConfigurationChanged = true
		} else {
			monitor.CheckedAt = sample.CheckedAt
			monitor.Balance = sample.Balance
			monitor.BalanceUpdatedTime = sample.BalanceUpdatedTime
			monitor.KnownBalance = sample.KnownBalance
			monitor.Partial = sample.Partial
			monitor.Success = sample.Success
			if err := common.UnmarshalJsonStr(sample.KeyBalancesJSON, &monitor.KeyBalances); err != nil {
				return err
			}
		}
		monitors[sample.ChannelID] = monitor
	}
	for _, channel := range channels {
		if channel != nil {
			channel.BalanceMonitor = monitors[channel.Id]
			if channel.BalanceMonitor == nil && channel.BalanceUpdatedTime > 0 {
				balance := channel.Balance
				channel.BalanceMonitor = &ChannelBalanceMonitor{
					Balance: &balance, BalanceUpdatedTime: channel.BalanceUpdatedTime,
					CheckedAt: channel.BalanceUpdatedTime, KnownBalance: balance,
					Success: true, KeyBalances: []ChannelKeyBalance{},
				}
			}
		}
	}
	return nil
}

// ListChannelBalanceSamples returns the most recent bounded observations in
// chronological order. It deliberately includes failed checks and old channel
// configurations so the historical chart does not erase outages or edits.
func ListChannelBalanceSamples(channelID int, start, end int64, limit int) ([]ChannelBalanceSample, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	samples := make([]ChannelBalanceSample, 0)
	query := DB.Where("channel_id = ?", channelID)
	if start > 0 {
		query = query.Where("checked_at >= ?", start)
	}
	if end > 0 {
		query = query.Where("checked_at <= ?", end)
	}
	if err := query.Order("id DESC").Limit(limit).Find(&samples).Error; err != nil {
		return nil, err
	}
	slices.Reverse(samples)
	for index := range samples {
		if err := common.UnmarshalJsonStr(samples[index].KeyBalancesJSON, &samples[index].KeyBalances); err != nil {
			return nil, err
		}
	}
	return samples, nil
}
