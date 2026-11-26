package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Sub2APIAccount contains a management login, never a relay credential. Secret
// ciphertext is excluded even when callers accidentally marshal the DB model.
type Sub2APIAccount struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	ChannelID       int    `json:"channel_id" gorm:"not null;index"`
	Identity        string `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	Origin          string `json:"origin" gorm:"type:varchar(512);not null"`
	ProxyIdentity   string `json:"-" gorm:"type:char(64);not null"`
	UpstreamUserID  int64  `json:"upstream_user_id" gorm:"not null"`
	Email           string `json:"email" gorm:"type:varchar(320)"`
	Credential      string `json:"-" gorm:"type:text;not null"`
	ExpiresAt       int64  `json:"expires_at"`
	HasRefreshToken bool   `json:"has_refresh_token"`
	NeedsLogin      bool   `json:"needs_login"`
	Revision        int64  `json:"-"`
	LastSyncAt      int64  `json:"last_sync_at"`
	LastError       string `json:"last_error" gorm:"type:varchar(128)"`
}

// A binding is verified by comparing the upstream account's own key list with
// the current physical key. Numeric key indexes are deliberately not stored.
type Sub2APIKeyBinding struct {
	ID               int64  `json:"-" gorm:"primaryKey"`
	ChannelID        int    `json:"channel_id" gorm:"not null;uniqueIndex:idx_sub2api_channel_key"`
	KeyIdentity      string `json:"key_identity" gorm:"type:char(64);not null;uniqueIndex:idx_sub2api_channel_key"`
	AccountID        int64  `json:"account_id" gorm:"index"`
	UpstreamAPIKeyID int64  `json:"upstream_api_key_id"`
	UpstreamGroupID  int64  `json:"upstream_group_id"`
	SyncRates        bool   `json:"sync_rates"`
	SyncBills        bool   `json:"sync_bills"`
	RateJSON         string `json:"-" gorm:"type:text"`
	LastSyncAt       int64  `json:"last_sync_at"`
	LastError        string `json:"last_error" gorm:"type:varchar(128)"`
}

type Sub2APIRate struct {
	SourceIdentity          string  `json:"source_identity,omitempty"`
	ResolvedRateMultiplier  float64 `json:"resolved_rate_multiplier"`
	EffectiveRateMultiplier float64 `json:"effective_rate_multiplier"`
	PeakRateEnabled         bool    `json:"peak_rate_enabled"`
	PeakStart               string  `json:"peak_start,omitempty"`
	PeakEnd                 string  `json:"peak_end,omitempty"`
	PeakRateMultiplier      float64 `json:"peak_rate_multiplier,omitempty"`
	Timezone                string  `json:"timezone,omitempty"`
	ObservedAt              string  `json:"observed_at"`
}

// EffectiveAt uses the supplier's timezone and same-day peak window.
// A missing/invalid schedule never silently becomes an off-peak discount.
func (rate Sub2APIRate) EffectiveAt(now time.Time) (float64, bool) {
	valid := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 10000 }
	if !valid(rate.ResolvedRateMultiplier) || !valid(rate.EffectiveRateMultiplier) {
		return 0, false
	}
	ratio := rate.ResolvedRateMultiplier
	if rate.PeakRateEnabled {
		start, errStart := time.Parse("15:04", rate.PeakStart)
		end, errEnd := time.Parse("15:04", rate.PeakEnd)
		location, errZone := time.LoadLocation(rate.Timezone)
		if errStart != nil || errEnd != nil || errZone != nil || rate.Timezone == "" || !valid(rate.PeakRateMultiplier) {
			return 0, false
		}
		minute := now.In(location).Hour()*60 + now.In(location).Minute()
		from, to := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
		if from >= to {
			return 0, false
		}
		peak := minute >= from && minute < to
		if peak {
			ratio *= rate.PeakRateMultiplier
		}
	}
	return ratio, valid(ratio)
}

// A rate snapshot must not survive a change to the supplier or its connection.
func (channel *Channel) Sub2APIConnectionIdentity() string {
	hash := sha256.Sum256([]byte(strings.TrimSuffix(channel.GetBaseURL(), "/") + "\x00" + channel.GetSetting().Proxy))
	return hex.EncodeToString(hash[:])
}

type sub2APIBindingsCacheEntry struct {
	items     []Sub2APIKeyBinding
	expiresAt time.Time
}

var sub2APIBindingsCache sync.Map
var sub2APIStorageDB atomic.Pointer[gorm.DB]

func MigrateSub2APIStorage(db *gorm.DB) error {
	if err := db.AutoMigrate(&Sub2APIAccount{}, &Sub2APIKeyBinding{}, &Sub2APIBill{}); err != nil {
		return err
	}
	sub2APIBindingsCache.Clear()
	sub2APIStorageDB.Store(db)
	return nil
}

func Sub2APIStorageReady() bool { return DB != nil && sub2APIStorageDB.Load() == DB }

func detectSub2APIStorage(db *gorm.DB) {
	if db.Migrator().HasTable(&Sub2APIAccount{}) && db.Migrator().HasTable(&Sub2APIKeyBinding{}) && db.Migrator().HasTable(&Sub2APIBill{}) {
		sub2APIBindingsCache.Clear()
		sub2APIStorageDB.Store(db)
	}
}

func InvalidateSub2APIBindings(channelID int) { sub2APIBindingsCache.Delete(channelID) }

func ListSub2APIAccounts(channelID int) ([]Sub2APIAccount, error) {
	query := DB.Model(&Sub2APIAccount{})
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	var rows []Sub2APIAccount
	err := query.Order("id").Find(&rows).Error
	return rows, err
}

func GetSub2APIAccount(id int64) (*Sub2APIAccount, error) {
	if id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var account Sub2APIAccount
	err := DB.First(&account, id).Error
	return &account, err
}

func ListSub2APIKeyBindings(channelID int) ([]Sub2APIKeyBinding, error) {
	query := DB.Model(&Sub2APIKeyBinding{})
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	var rows []Sub2APIKeyBinding
	err := query.Order("id").Find(&rows).Error
	return rows, err
}

func GetSub2APIKeyBinding(channelID int, keyIdentity string) (*Sub2APIKeyBinding, error) {
	if value, ok := sub2APIBindingsCache.Load(channelID); ok {
		cached := value.(sub2APIBindingsCacheEntry)
		if time.Now().Before(cached.expiresAt) {
			for _, binding := range cached.items {
				if binding.KeyIdentity == keyIdentity {
					return &binding, nil
				}
			}
			return nil, gorm.ErrRecordNotFound
		}
	}
	rows, err := ListSub2APIKeyBindings(channelID)
	if err != nil {
		return nil, err
	}
	sub2APIBindingsCache.Store(channelID, sub2APIBindingsCacheEntry{rows, time.Now().Add(10 * time.Second)})
	for _, binding := range rows {
		if binding.KeyIdentity == keyIdentity {
			return &binding, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

// StoreSub2APIAccount preserves explicit sync choices while replacing verified
// ownership. Network calls have already completed before this transaction starts.
func StoreSub2APIAccount(ctx context.Context, account *Sub2APIAccount, bindings []Sub2APIKeyBinding, authorization *AuthFlowAuthorization) error {
	if authorization == nil {
		return ErrUserSessionInactive
	}
	err := DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "identity"}}, DoUpdates: clause.AssignmentColumns([]string{"credential", "expires_at", "has_refresh_token", "needs_login", "email", "proxy_identity", "revision", "last_error"})}).Create(account).Error; err != nil {
			return err
		}
		account.ID = 0
		if err := tx.Where("identity = ?", account.Identity).First(account).Error; err != nil {
			return err
		}
		return replaceSub2APIBindings(tx, account, bindings)
	})
	InvalidateSub2APIBindings(account.ChannelID)
	return err
}

func replaceSub2APIBindings(tx *gorm.DB, account *Sub2APIAccount, bindings []Sub2APIKeyBinding) error {
	var current Sub2APIAccount
	if err := lockForUpdate(tx).Select("id").Where("id = ? AND channel_id = ? AND revision = ?", account.ID, account.ChannelID, account.Revision).First(&current).Error; err != nil {
		return err
	}
	if err := tx.Model(&Sub2APIKeyBinding{}).Where("account_id = ?", account.ID).
		Updates(map[string]any{"account_id": 0, "upstream_api_key_id": 0, "upstream_group_id": 0}).Error; err != nil {
		return err
	}
	for _, binding := range bindings {
		binding.AccountID, binding.ChannelID = account.ID, account.ChannelID
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_identity"}}, DoUpdates: clause.AssignmentColumns([]string{"account_id", "upstream_api_key_id", "upstream_group_id"})}).Create(&binding).Error; err != nil {
			return err
		}
	}
	return nil
}

func ReplaceSub2APIAccountBindings(account *Sub2APIAccount, bindings []Sub2APIKeyBinding) error {
	err := DB.Transaction(func(tx *gorm.DB) error { return replaceSub2APIBindings(tx, account, bindings) })
	InvalidateSub2APIBindings(account.ChannelID)
	return err
}

func UpdateSub2APICredential(account *Sub2APIAccount, previousRevision int64) (bool, error) {
	result := DB.Session(&gorm.Session{Logger: logger.Discard}).Model(&Sub2APIAccount{}).
		Where("id = ? AND revision = ?", account.ID, previousRevision).
		Updates(map[string]any{"credential": account.Credential, "expires_at": account.ExpiresAt, "has_refresh_token": account.HasRefreshToken, "needs_login": account.NeedsLogin, "revision": previousRevision + 1, "last_error": account.LastError})
	return result.RowsAffected == 1, result.Error
}

func DeleteSub2APIAccount(channelID int, accountID int64, authorization *AuthFlowAuthorization) error {
	if authorization == nil {
		return ErrUserSessionInactive
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		result := tx.Where("id = ? AND channel_id = ?", accountID, channelID).Delete(&Sub2APIAccount{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&Sub2APIKeyBinding{}).Where("account_id = ? AND channel_id = ?", accountID, channelID).
			Updates(map[string]any{"account_id": 0, "upstream_api_key_id": 0, "upstream_group_id": 0, "sync_bills": false}).Error
	})
	InvalidateSub2APIBindings(channelID)
	return err
}

func SetSub2APIKeySync(channelID int, identity string, rates, bills bool, authorization *AuthFlowAuthorization) error {
	if authorization == nil || len(identity) != 64 {
		return ErrUserSessionInactive
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.Type != constant.ChannelTypeSub2API {
			return gorm.ErrRecordNotFound
		}
		found := false
		for _, key := range channel.GetKeys() {
			found = found || channel.KeyIdentity(key) == identity
		}
		if !found {
			return gorm.ErrRecordNotFound
		}
		var binding Sub2APIKeyBinding
		err := tx.Where("channel_id = ? AND key_identity = ?", channelID, identity).First(&binding).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if bills && (binding.AccountID == 0 || binding.UpstreamAPIKeyID == 0) {
			return common.NewMessage("Connect the account that owns this key before syncing bills.")
		}
		binding.ChannelID, binding.KeyIdentity, binding.SyncRates, binding.SyncBills = channelID, identity, rates, bills
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_identity"}}, DoUpdates: clause.AssignmentColumns([]string{"sync_rates", "sync_bills"})}).Create(&binding).Error
	})
	InvalidateSub2APIBindings(channelID)
	return err
}

func UpdateSub2APIAccountSync(accountID int64, errorCode string) error {
	return DB.Model(&Sub2APIAccount{}).Where("id = ?", accountID).
		Updates(map[string]any{"last_sync_at": time.Now().Unix(), "last_error": errorCode}).Error
}

func StoreSub2APIRate(channelID int, identity string, rate *Sub2APIRate, errorCode string) error {
	binding := Sub2APIKeyBinding{ChannelID: channelID, KeyIdentity: identity, LastError: strings.TrimSpace(errorCode)}
	columns := []string{"last_error"}
	if rate != nil {
		encoded, err := common.Marshal(rate)
		if err != nil {
			return err
		}
		binding.RateJSON, binding.LastSyncAt = string(encoded), time.Now().Unix()
		columns = append(columns, "rate_json", "last_sync_at")
	}
	err := DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_identity"}}, DoUpdates: clause.AssignmentColumns(columns)}).Create(&binding).Error
	InvalidateSub2APIBindings(channelID)
	return err
}
