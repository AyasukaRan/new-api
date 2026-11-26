package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
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
	ID              int64   `json:"id" gorm:"primaryKey"`
	ChannelID       int     `json:"channel_id" gorm:"not null;index"`
	SharedIdentity  *string `json:"-" gorm:"type:char(64);uniqueIndex"`
	ProxyCredential string  `json:"-" gorm:"type:text"`
	Identity        string  `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	Origin          string  `json:"origin" gorm:"type:varchar(512);not null"`
	ProxyIdentity   string  `json:"-" gorm:"type:char(64);not null"`
	UpstreamUserID  int64   `json:"upstream_user_id" gorm:"not null"`
	Email           string  `json:"email" gorm:"type:varchar(320)"`
	Credential      string  `json:"-" gorm:"type:text;not null"`
	ExpiresAt       int64   `json:"expires_at"`
	HasRefreshToken bool    `json:"has_refresh_token"`
	NeedsLogin      bool    `json:"needs_login"`
	Revision        int64   `json:"-"`
	InventoryJSON   string  `json:"-"`
	SyncStateJSON   string  `json:"-"`
	LastSyncAt      int64   `json:"last_sync_at"`
	LastError       string  `json:"last_error" gorm:"type:varchar(128)"`
}

// A binding is verified by comparing the upstream account's own key list with
// the current physical key. Numeric key indexes are deliberately not stored.
type Sub2APIKeyBinding struct {
	AccountOrigin    string `json:"-" gorm:"-"`
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
	if db.Migrator().HasTable(&Sub2APIAccount{}) && db.Migrator().HasTable(&Sub2APIKeyBinding{}) && db.Migrator().HasTable(&Sub2APIBill{}) && db.Migrator().HasColumn(&Sub2APIAccount{}, "shared_identity") && db.Migrator().HasColumn(&Sub2APIAccount{}, "proxy_credential") && db.Migrator().HasColumn(&Sub2APIAccount{}, "inventory_json") && db.Migrator().HasColumn(&Sub2APIAccount{}, "sync_state_json") {
		sub2APIBindingsCache.Clear()
		sub2APIStorageDB.Store(db)
	}
}

func InvalidateSub2APIBindings(channelID int) { sub2APIBindingsCache.Delete(channelID) }

func ListSub2APIAccounts(channelID int) ([]Sub2APIAccount, error) {
	query := DB.Model(&Sub2APIAccount{}).Where("credential <> ?", "")
	if channelID > 0 {
		query = query.Where("id IN (?)", DB.Model(&Sub2APIKeyBinding{}).Select("account_id").Where("channel_id = ? AND account_id > 0", channelID))
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
	if err != nil {
		return nil, err
	}
	accountIDs := make([]int64, 0)
	for _, binding := range rows {
		if binding.AccountID > 0 {
			accountIDs = append(accountIDs, binding.AccountID)
		}
	}
	if len(accountIDs) > 0 {
		var accounts []Sub2APIAccount
		if err := DB.Select("id", "origin").Where("id IN ?", accountIDs).Find(&accounts).Error; err != nil {
			return nil, err
		}
		origins := make(map[int64]string, len(accounts))
		for _, account := range accounts {
			origins[account.ID] = account.Origin
		}
		for i := range rows {
			rows[i].AccountOrigin = origins[rows[i].AccountID]
		}
	}
	return rows, nil
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
		var existing Sub2APIAccount
		lookup := lockForUpdate(tx).Where("identity = ?", account.Identity).First(&existing).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if lookup == nil && existing.SharedIdentity != nil {
			return errors.New("Sub2API account requires independent management")
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "identity"}}, DoUpdates: clause.AssignmentColumns([]string{"credential", "expires_at", "has_refresh_token", "needs_login", "email", "proxy_identity", "revision", "last_error"})}).Create(account).Error; err != nil {
			return err
		}
		account.ID = 0
		if err := tx.Where("identity = ?", account.Identity).First(account).Error; err != nil {
			return err
		}
		return replaceSub2APIBindings(tx, account, account.ChannelID, bindings)
	})
	InvalidateSub2APIBindings(account.ChannelID)
	return err
}

func replaceSub2APIBindings(tx *gorm.DB, account *Sub2APIAccount, channelID int, bindings []Sub2APIKeyBinding) error {
	var current Sub2APIAccount
	if err := lockForUpdate(tx).Select("id").Where("id = ? AND channel_id = ? AND revision = ?", account.ID, account.ChannelID, account.Revision).First(&current).Error; err != nil {
		return err
	}
	if err := tx.Model(&Sub2APIKeyBinding{}).Where("account_id = ? AND channel_id = ?", account.ID, channelID).
		Updates(map[string]any{"account_id": 0, "upstream_api_key_id": 0, "upstream_group_id": 0}).Error; err != nil {
		return err
	}
	for _, binding := range bindings {
		binding.AccountID, binding.ChannelID = account.ID, channelID
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "channel_id"}, {Name: "key_identity"}}, DoUpdates: clause.AssignmentColumns([]string{"account_id", "upstream_api_key_id", "upstream_group_id"})}).Create(&binding).Error; err != nil {
			return err
		}
	}
	return nil
}

func ReplaceSub2APIAccountBindings(account *Sub2APIAccount, channel *Channel, bindings []Sub2APIKeyBinding) error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		var currentAccount Sub2APIAccount
		if err := lockForUpdate(tx).Where("id = ? AND revision = ?", account.ID, account.Revision).First(&currentAccount).Error; err != nil {
			return err
		}
		var associated int64
		if err := tx.Model(&Sub2APIKeyBinding{}).Where("account_id = ? AND channel_id = ?", account.ID, channel.Id).Count(&associated).Error; err != nil {
			return err
		}
		if associated == 0 {
			return gorm.ErrRecordNotFound
		}
		var current Channel
		if err := lockForUpdate(tx).First(&current, channel.Id).Error; err != nil {
			return err
		}
		if current.Type != constant.ChannelTypeSub2API || current.Sub2APIConnectionIdentity() != channel.Sub2APIConnectionIdentity() || current.Key != channel.Key {
			return gorm.ErrRecordNotFound
		}
		return replaceSub2APIBindings(tx, account, channel.Id, bindings)
	})
	InvalidateSub2APIBindings(channel.Id)
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
		result := tx.Where("id = ? AND channel_id = ? AND shared_identity IS NULL", accountID, channelID).Delete(&Sub2APIAccount{})
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

// Independent accounts keep their original ID and AAD when promoted from a
// channel login. NULL shared identities leave legacy duplicates untouched.
func StoreIndependentSub2APIAccount(ctx context.Context, account *Sub2APIAccount, previousRevision int64, authorization *AuthFlowAuthorization) error {
	if authorization == nil {
		return ErrUserSessionInactive
	}
	return DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		if account.ID == 0 {
			return tx.Create(account).Error
		}
		result := tx.Model(&Sub2APIAccount{}).Where("id = ? AND revision = ? AND origin = ? AND upstream_user_id = ?", account.ID, previousRevision, account.Origin, account.UpstreamUserID).Updates(map[string]any{
			"shared_identity": account.SharedIdentity, "proxy_credential": account.ProxyCredential, "proxy_identity": account.ProxyIdentity,
			"credential": account.Credential, "expires_at": account.ExpiresAt, "has_refresh_token": account.HasRefreshToken,
			"needs_login": false, "email": account.Email, "revision": account.Revision, "last_error": "",
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func FindIndependentSub2APIAccount(identity string) (*Sub2APIAccount, error) {
	var account Sub2APIAccount
	err := DB.Where("shared_identity = ?", identity).First(&account).Error
	return &account, err
}

// Binding writes recheck the exact channel snapshot after upstream ownership
// verification, and never erase another channel's association or key settings.
func AttachSub2APIAccount(channel *Channel, account *Sub2APIAccount, bindings []Sub2APIKeyBinding, rates map[int64]*Sub2APIRate, authorization *AuthFlowAuthorization) error {
	if authorization == nil {
		return ErrUserSessionInactive
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		var currentAccount Sub2APIAccount
		if err := lockForUpdate(tx).Where("id = ? AND revision = ?", account.ID, account.Revision).First(&currentAccount).Error; err != nil {
			return err
		}
		var current Channel
		if err := lockForUpdate(tx).First(&current, channel.Id).Error; err != nil {
			return err
		}
		if current.Type != constant.ChannelTypeSub2API || current.Sub2APIConnectionIdentity() != channel.Sub2APIConnectionIdentity() {
			return gorm.ErrRecordNotFound
		}
		keys := make(map[string]bool)
		for _, key := range current.GetKeys() {
			keys[current.KeyIdentity(key)] = true
		}
		for _, binding := range bindings {
			if !keys[binding.KeyIdentity] {
				return gorm.ErrRecordNotFound
			}
		}
		if err := replaceSub2APIBindings(tx, account, channel.Id, bindings); err != nil {
			return err
		}
		return storeSub2APIInventoryRates(tx, channel, bindings, rates)
	})
	InvalidateSub2APIBindings(channel.Id)
	return err
}

func DetachSub2APIAccount(channelID int, accountID int64, authorization *AuthFlowAuthorization) error {
	if authorization == nil {
		return ErrUserSessionInactive
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		var account Sub2APIAccount
		if err := lockForUpdate(tx).First(&account, accountID).Error; err != nil {
			return err
		}
		return tx.Model(&Sub2APIKeyBinding{}).Where("channel_id = ? AND account_id = ?", channelID, accountID).Updates(map[string]any{"account_id": 0, "upstream_api_key_id": 0, "upstream_group_id": 0, "sync_bills": false}).Error
	})
	InvalidateSub2APIBindings(channelID)
	return err
}

var ErrSub2APIAccountLinked = errors.New("Sub2API account is still linked")

func DisconnectIndependentSub2APIAccount(accountID int64, authorization *AuthFlowAuthorization) error {
	if authorization == nil {
		return ErrUserSessionInactive
	}
	var channelIDs []int
	err := DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		if err := ValidateAuthSessionWithTx(tx, authorization.AuthSessionIdentity); err != nil {
			return err
		}
		var account Sub2APIAccount
		if err := lockForUpdate(tx).First(&account, accountID).Error; err != nil {
			return err
		}
		if err := tx.Model(&Sub2APIKeyBinding{}).Where("account_id = ?", accountID).Pluck("channel_id", &channelIDs).Error; err != nil {
			return err
		}
		if len(channelIDs) > 0 {
			// Disabled Sub2API channels remain linked. Deleted channels and
			// channels changed to another provider no longer offer a detach UI.
			var channels []Channel
			if err := lockForUpdate(tx).Select("id", "type").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
				return err
			}
			for _, channel := range channels {
				if channel.Type == constant.ChannelTypeSub2API {
					return ErrSub2APIAccountLinked
				}
			}
			if err := tx.Where("account_id = ?", accountID).Delete(&Sub2APIKeyBinding{}).Error; err != nil {
				return err
			}
		}
		// Preserve invoice attribution, but destroy usable credentials and remove
		// this connection from management lists. Reconnecting may reuse its ID.
		return tx.Model(&account).Updates(map[string]any{"credential": "", "proxy_credential": "", "has_refresh_token": false, "needs_login": true, "revision": account.Revision + 1}).Error
	})
	if err == nil {
		for _, channelID := range channelIDs {
			InvalidateSub2APIBindings(channelID)
		}
	}
	return err
}

var sub2APIRefreshLocks sync.Map

// Account-scoped advisory locks serialize refresh-token rotation across channels
// and gateway nodes. SQLite deployments use the same process-local fallback.
func WithSub2APIAccountLock(ctx context.Context, accountID int64, operation func() error) error {
	return withSub2APICredentialLock(ctx, fmt.Sprintf("account:%d", accountID), operation)
}

func WithSub2APIIdentityLock(ctx context.Context, identity string, operation func() error) error {
	return withSub2APICredentialLock(ctx, "identity:"+identity, operation)
}

func withSub2APICredentialLock(ctx context.Context, key string, operation func() error) error {
	digest := sha256.Sum256([]byte("new-api:sub2api:" + key))
	name := "sub2api:" + hex.EncodeToString(digest[:24])
	switch {
	case common.UsingMainDatabase(common.DatabaseTypeMySQL):
		wait := 0
		if deadline, ok := ctx.Deadline(); ok {
			wait = max(int(time.Until(deadline)/time.Second), 0)
		}
		return DB.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var acquired sql.NullInt64
			if err := conn.Raw("SELECT GET_LOCK(?, ?)", name, wait).Row().Scan(&acquired); err != nil {
				return err
			}
			if acquired.Int64 != 1 {
				return ErrPluginRefreshLockTimeout
			}
			defer conn.WithContext(context.Background()).Exec("DO RELEASE_LOCK(?)", name)
			return operation()
		})
	case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
		advisoryKey := int64(binary.BigEndian.Uint64(digest[:8]))
		return DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", advisoryKey).Error; err != nil {
				return err
			}
			return operation()
		})
	default:
		value, _ := sub2APIRefreshLocks.LoadOrStore(name, make(chan struct{}, 1))
		lock := value.(chan struct{})
		select {
		case lock <- struct{}{}:
		case <-ctx.Done():
			return ErrPluginRefreshLockTimeout
		}
		defer func() { <-lock }()
		return operation()
	}
}
