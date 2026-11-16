package model

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/shopspring/decimal"
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

// ChannelKeyAccount is the current display metadata, never a persisted reading.
type ChannelKeyAccount struct {
	Index  int      `json:"index"`
	Groups []string `json:"groups"`
	Status int      `json:"status"`
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
	KeyAccounts          []ChannelKeyAccount `json:"key_accounts"`
}

func (channel *Channel) BalanceKeyAccounts() []ChannelKeyAccount {
	keys := channel.relayKeys()
	channelGroups := channel.GetGroups()
	accounts := make([]ChannelKeyAccount, 0, len(keys))
	for _, index := range channel.OrderedKeyIndices() {
		configuredGroups := channel.ChannelInfo.KeyGroups[channel.KeyIdentity(keys[index])]
		groups := make([]string, 0, len(channelGroups))
		for _, group := range channelGroups {
			if len(configuredGroups) == 0 || slices.Contains(configuredGroups, group) {
				groups = append(groups, group)
			}
		}
		status := common.ChannelStatusEnabled
		if configured, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists {
			status = configured
		}
		accounts = append(accounts, ChannelKeyAccount{Index: index, Groups: groups, Status: status})
	}
	return accounts
}

// ChannelBalanceSample is a monitoring observation, independent of channel
// configuration and routing state. Balance carries the last complete reading;
// KnownBalance aggregates the keys that answered this particular check.
type ChannelBalanceSample struct {
	EmailState         *channelBalanceEmailState `json:"-" gorm:"type:text;serializer:json"`
	Scheduled          bool                      `json:"-" gorm:"-"`
	ID                 int64                     `json:"id" gorm:"primaryKey"`
	ChannelID          int                       `json:"channel_id" gorm:"index:idx_channel_balance_time,priority:1"`
	CheckedAt          int64                     `json:"checked_at" gorm:"bigint;index:idx_channel_balance_time,priority:2"`
	StartedAt          int64                     `json:"-" gorm:"bigint"`
	ConfigurationHash  string                    `json:"-" gorm:"type:varchar(64)"`
	Balance            *float64                  `json:"balance"`
	BalanceUpdatedTime int64                     `json:"balance_updated_time" gorm:"bigint"`
	KnownBalance       float64                   `json:"known_balance"`
	Partial            bool                      `json:"partial"`
	Success            bool                      `json:"success"`
	UsedQuota          int64                     `json:"used_quota" gorm:"bigint"`
	KeyBalances        []ChannelKeyBalance       `json:"key_balances" gorm:"-"`
	KeyBalancesJSON    string                    `json:"-" gorm:"type:text"`
}

var ErrChannelBalanceConfigurationChanged = errors.New("channel balance configuration changed; retry the balance query")
var ErrChannelBalanceQueryDisabled = errors.New("channel balance query is disabled")

func (channel *Channel) balanceAggregationMode() string {
	if channel.ChannelInfo.IsMultiKey && channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeFailover {
		return "distinct"
	}
	return "sum"
}

// AggregateKeyBalances uses normalized numeric readings, never rounded display
// values or last-known values from different checks. Exhausted multi-key
// accounts cannot reduce the funds available from another account.
func (channel *Channel) AggregateKeyBalances(keys []ChannelKeyBalance) (float64, error) {
	var available decimal.Decimal
	seen := make(map[float64]bool)
	distinct := channel.balanceAggregationMode() == "distinct"
	for _, key := range keys {
		if key.Balance == nil || key.Error != "" {
			continue
		}
		balance := *key.Balance
		if math.IsNaN(balance) || math.IsInf(balance, 0) {
			return 0, errors.New("invalid key balance")
		}
		if channel.ChannelInfo.IsMultiKey && balance <= 0 || distinct && seen[balance] {
			continue
		}
		seen[balance] = true
		available = available.Add(decimal.NewFromFloat(balance))
	}
	balance := available.InexactFloat64()
	if math.IsInf(balance, 0) {
		return 0, errors.New("invalid channel balance")
	}
	return balance, nil
}

// completeBalance projects one complete observation through the current mode.
// Legacy aggregates without raw readings cannot establish a distinct total.
func (channel *Channel) completeBalance(sample *ChannelBalanceSample) (*float64, int64, error) {
	if sample == nil || !sample.Success || sample.Partial {
		return nil, 0, nil
	}
	var keys []ChannelKeyBalance
	if sample.KeyBalancesJSON != "" {
		if err := common.UnmarshalJsonStr(sample.KeyBalancesJSON, &keys); err != nil {
			return nil, 0, err
		}
	}
	if len(keys) == 0 {
		if channel.balanceAggregationMode() == "distinct" {
			return nil, 0, nil
		}
		return sample.Balance, sample.BalanceUpdatedTime, nil
	}
	balance, err := channel.AggregateKeyBalances(keys)
	if err != nil {
		return nil, 0, err
	}
	return &balance, sample.CheckedAt, nil
}

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
		// Mode is a presentation/aggregation choice, not account identity. A
		// concurrent mode edit must use the locked current choice at commit.
		if len(sample.KeyBalances) > 0 {
			sample.KnownBalance, err = current.AggregateKeyBalances(sample.KeyBalances)
			if err != nil {
				return err
			}
		} else if current.balanceAggregationMode() == "distinct" && sample.Success {
			return errors.New("a complete key balance snapshot is required")
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
			if current.balanceAggregationMode() != "distinct" {
				sample.Balance = previous.Balance
				sample.BalanceUpdatedTime = previous.BalanceUpdatedTime
			}
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
		} else if errors.Is(previousErr, gorm.ErrRecordNotFound) && current.BalanceUpdatedTime > 0 && current.balanceAggregationMode() != "distinct" {
			// Preserve the last known full balance when upgrading an existing
			// installation, including if its first monitored check fails.
			balance := current.Balance
			sample.Balance = &balance
			sample.BalanceUpdatedTime = current.BalanceUpdatedTime
		}
		if !sample.Success || sample.Partial {
			complete := previous
			if previousErr != nil || previous.ConfigurationHash != configurationHash || !previous.Success || previous.Partial {
				complete = ChannelBalanceSample{}
				if err := tx.Where("channel_id = ? AND configuration_hash = ? AND success = ? AND partial = ?", channel.Id, configurationHash, true, false).
					Order("id DESC").Limit(1).Find(&complete).Error; err != nil {
					return err
				}
			}
			if complete.ID > 0 {
				sample.Balance, sample.BalanceUpdatedTime, err = current.completeBalance(&complete)
				if err != nil {
					return err
				}
			}
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
		sample.EmailState = nextChannelBalanceEmailState(current.GetSetting(), current.balanceAggregationMode(), sample, &previous)
		return tx.Create(sample).Error
	})
	if err == nil {
		InvalidateChannelBalanceRouting(channel.Id)
	}
	return err
}

// The latest observation carries successful recipients across restarts and failed checks.
// A positive recipient value is a short delivery lease; -1 marks successful delivery.
type channelBalanceEmailState struct {
	Generation  int64         `json:"generation"`
	Threshold   *float64      `json:"threshold,omitempty"`
	Recipients  map[int]int64 `json:"recipients,omitempty"`
	Aggregation string        `json:"aggregation,omitempty"`
}

func sameBalanceThreshold(a, b *float64) bool {
	return a == b || (a != nil && b != nil && *a == *b)
}

func nextChannelBalanceEmailState(setting dto.ChannelSettings, aggregation string, current, previous *ChannelBalanceSample) *channelBalanceEmailState {
	if !setting.BalanceEmailEnabled || setting.BalanceQueryDisabled || setting.BalanceEmailThreshold == nil {
		return nil
	}
	threshold := setting.BalanceEmailThreshold
	state := previous.EmailState
	previousAggregation := "sum"
	if state != nil && state.Aggregation != "" {
		previousAggregation = state.Aggregation
	}
	// Older releases could notify at equality. Balance retains the last full
	// reading through partial checks, so equality re-arms those old episodes too.
	if state != nil && (aggregation != previousAggregation || current.ConfigurationHash != previous.ConfigurationHash || !sameBalanceThreshold(state.Threshold, threshold) ||
		(previous.Balance != nil && *previous.Balance >= *threshold)) {
		state = nil
	}
	if !current.Success || current.Partial {
		return state
	}
	if current.KnownBalance >= *threshold {
		return nil
	}
	if state == nil {
		state = &channelBalanceEmailState{Generation: current.StartedAt, Threshold: threshold, Aggregation: aggregation}
	}
	return state
}

// DeliverBalanceEmail claims one recipient of the latest confirmed reading,
// sends outside the database lock, and leaves failed deliveries retryable.
func (sample *ChannelBalanceSample) DeliverBalanceEmail(userID int, send func(*Channel) error) (bool, error) {
	if sample.EmailState == nil || !sample.Scheduled || !sample.Success || sample.Partial {
		return false, nil
	}
	var claimed *Channel
	var lease int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, sample.ChannelID).Error; err != nil {
			return err
		}
		if err := channel.CheckBalanceQueryEnabled(); err != nil {
			if errors.Is(err, ErrChannelBalanceQueryDisabled) {
				return nil
			}
			return err
		}
		setting := channel.GetSetting()
		aggregation := sample.EmailState.Aggregation
		if aggregation == "" {
			aggregation = "sum"
		}
		if aggregation != channel.balanceAggregationMode() {
			return nil
		}
		if !setting.BalanceEmailEnabled || setting.BalanceEmailThreshold == nil || sample.KnownBalance >= *setting.BalanceEmailThreshold || !sameBalanceThreshold(setting.BalanceEmailThreshold, sample.EmailState.Threshold) {
			return nil
		}
		hash, err := channelBalanceConfigurationHash(&channel)
		if err != nil || hash != sample.ConfigurationHash {
			return err
		}
		var latest ChannelBalanceSample
		if err := tx.Where("channel_id = ?", channel.Id).Order("id DESC").First(&latest).Error; err != nil {
			return err
		}
		if latest.ID != sample.ID || latest.EmailState == nil || latest.EmailState.Generation != sample.EmailState.Generation {
			return nil
		}
		now := time.Now()
		value := latest.EmailState.Recipients[userID]
		if value == -1 || value > now.UnixNano() {
			return nil
		}
		if latest.EmailState.Recipients == nil {
			latest.EmailState.Recipients = make(map[int]int64)
		}
		lease = now.Add(10 * time.Minute).UnixNano()
		latest.EmailState.Recipients[userID] = lease
		claimed = &channel
		return tx.Model(&latest).Select("EmailState").Updates(&latest).Error
	})
	if err != nil || claimed == nil {
		return false, err
	}

	sendErr := send(claimed)
	err = DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).Select("id").First(&channel, sample.ChannelID).Error; err != nil {
			return err
		}
		var latest ChannelBalanceSample
		if err := tx.Where("channel_id = ?", sample.ChannelID).Order("id DESC").First(&latest).Error; err != nil {
			return err
		}
		state := latest.EmailState
		if state == nil || state.Generation != sample.EmailState.Generation || state.Recipients[userID] != lease {
			return nil
		}
		if sendErr == nil {
			state.Recipients[userID] = -1
		} else {
			delete(state.Recipients, userID)
		}
		return tx.Model(&latest).Select("EmailState").Updates(&latest).Error
	})
	return sendErr == nil, errors.Join(sendErr, err)
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

// PopulateChannelBalanceMonitors batches configuration and latest observations,
// with one additional complete-snapshot query when readings are incomplete.
// Configuration is read privately because admin channel lists omit keys.
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
	if err := DB.Select("id", "key", "group", "type", "base_url", "setting", "settings", "header_override", "channel_info").Where("id IN ?", ids).Find(&configurations).Error; err != nil {
		return err
	}
	configurationHashes := make(map[int]string, len(configurations))
	currentChannels := make(map[int]*Channel, len(configurations))
	keyAccounts := make(map[int][]ChannelKeyAccount, len(configurations))
	for _, channel := range configurations {
		configurationHash, err := channelBalanceConfigurationHash(channel)
		if err != nil {
			return err
		}
		configurationHashes[channel.Id] = configurationHash
		currentChannels[channel.Id] = channel
		keyAccounts[channel.Id] = channel.BalanceKeyAccounts()
	}
	var samples []ChannelBalanceSample
	latestIDs := DB.Model(&ChannelBalanceSample{}).Select("MAX(id)").Where("channel_id IN ?", ids).Group("channel_id")
	if err := DB.Where("id IN (?)", latestIDs).Find(&samples).Error; err != nil {
		return err
	}
	// Partial readings retain a single earlier complete snapshot, not a mix
	// of each key's most recent successful value. Fetch all fallbacks together.
	incompleteIDs := make([]int, 0)
	incompleteHashes := make([]string, 0)
	for _, sample := range samples {
		if sample.ConfigurationHash == configurationHashes[sample.ChannelID] && (!sample.Success || sample.Partial) {
			incompleteIDs = append(incompleteIDs, sample.ChannelID)
			incompleteHashes = append(incompleteHashes, sample.ConfigurationHash)
		}
	}
	completeSamples := make(map[int]ChannelBalanceSample)
	if len(incompleteIDs) > 0 {
		completeIDs := DB.Model(&ChannelBalanceSample{}).Select("MAX(id)").
			Where("channel_id IN ? AND configuration_hash IN ? AND success = ? AND partial = ?", incompleteIDs, incompleteHashes, true, false).
			Group("channel_id, configuration_hash")
		var complete []ChannelBalanceSample
		if err := DB.Where("id IN (?)", completeIDs).Find(&complete).Error; err != nil {
			return err
		}
		for _, sample := range complete {
			if sample.ConfigurationHash == configurationHashes[sample.ChannelID] {
				completeSamples[sample.ChannelID] = sample
			}
		}
	}
	monitors := make(map[int]*ChannelBalanceMonitor, len(samples))
	for _, sample := range samples {
		monitor := &ChannelBalanceMonitor{KeyBalances: []ChannelKeyBalance{}}
		if currentChannels[sample.ChannelID] == nil || configurationHashes[sample.ChannelID] != sample.ConfigurationHash {
			monitor.ConfigurationChanged = true
		} else {
			current := currentChannels[sample.ChannelID]
			monitor.CheckedAt = sample.CheckedAt
			monitor.KnownBalance = sample.KnownBalance
			monitor.Partial = sample.Partial
			monitor.Success = sample.Success
			if sample.KeyBalancesJSON != "" {
				if err := common.UnmarshalJsonStr(sample.KeyBalancesJSON, &monitor.KeyBalances); err != nil {
					return err
				}
			}
			if len(monitor.KeyBalances) > 0 {
				var err error
				monitor.KnownBalance, err = current.AggregateKeyBalances(monitor.KeyBalances)
				if err != nil {
					return err
				}
			} else if current.balanceAggregationMode() == "distinct" {
				monitor.KnownBalance = 0
				if sample.Success && !sample.Partial {
					monitor.Success = false
					monitor.CheckedAt = 0
				}
			}
			complete := sample
			if !sample.Success || sample.Partial {
				complete = completeSamples[sample.ChannelID]
				if complete.ID == 0 && current.balanceAggregationMode() != "distinct" {
					monitor.Balance, monitor.BalanceUpdatedTime = sample.Balance, sample.BalanceUpdatedTime
				}
			}
			if complete.ID > 0 {
				var err error
				monitor.Balance, monitor.BalanceUpdatedTime, err = current.completeBalance(&complete)
				if err != nil {
					return err
				}
			}
		}
		monitors[sample.ChannelID] = monitor
	}
	for _, channel := range channels {
		if channel != nil {
			accounts := keyAccounts[channel.Id]
			if accounts == nil {
				accounts = []ChannelKeyAccount{}
			}
			// Full-key callers render indexed credentials from this snapshot.
			// A concurrent replacement must not attach newer indexed balances
			// to those older keys. List callers intentionally omit credentials.
			if channel.Key != "" {
				configurationHash, err := channelBalanceConfigurationHash(channel)
				if err != nil {
					return err
				}
				if configurationHash != configurationHashes[channel.Id] {
					channel.BalanceMonitor = &ChannelBalanceMonitor{ConfigurationChanged: true, KeyBalances: []ChannelKeyBalance{}, KeyAccounts: accounts}
					channel.Balance, channel.BalanceUpdatedTime = 0, 0
					continue
				}
			}
			channel.BalanceMonitor = monitors[channel.Id]
			current := currentChannels[channel.Id]
			if channel.BalanceMonitor == nil && channel.BalanceUpdatedTime > 0 && current != nil && current.balanceAggregationMode() != "distinct" {
				balance := channel.Balance
				channel.BalanceMonitor = &ChannelBalanceMonitor{
					Balance: &balance, BalanceUpdatedTime: channel.BalanceUpdatedTime,
					CheckedAt: channel.BalanceUpdatedTime, KnownBalance: balance,
					Success: true, KeyBalances: []ChannelKeyBalance{},
				}
			}
			if channel.BalanceMonitor == nil {
				channel.BalanceMonitor = &ChannelBalanceMonitor{KeyBalances: []ChannelKeyBalance{}}
			}
			channel.BalanceMonitor.KeyAccounts = accounts
			channel.Balance, channel.BalanceUpdatedTime = 0, 0
			if channel.BalanceMonitor.Balance != nil {
				channel.Balance = *channel.BalanceMonitor.Balance
				channel.BalanceUpdatedTime = channel.BalanceMonitor.BalanceUpdatedTime
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
		if samples[index].KeyBalancesJSON == "" {
			samples[index].KeyBalances = []ChannelKeyBalance{}
		} else if err := common.UnmarshalJsonStr(samples[index].KeyBalancesJSON, &samples[index].KeyBalances); err != nil {
			return nil, err
		}
	}
	return samples, nil
}
