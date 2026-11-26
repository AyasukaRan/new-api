package model

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// Inventory data contains no usable credentials. Fingerprints remain private
// and are never included in the management API's snapshot projection.
type Sub2APIInventoryKey struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	MaskedKey string     `json:"masked_key"`
	GroupID   *int64     `json:"group_id"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type Sub2APIInventoryGroup struct {
	ID                     int64        `json:"id"`
	Name                   string       `json:"name"`
	Description            string       `json:"description"`
	Platform               string       `json:"platform"`
	GroupRateMultiplier    float64      `json:"group_rate_multiplier"`
	UserRateMultiplier     *float64     `json:"user_rate_multiplier"`
	ResolvedRateMultiplier float64      `json:"resolved_rate_multiplier"`
	PeakRateEnabled        bool         `json:"peak_rate_enabled"`
	PeakStart              string       `json:"peak_start"`
	PeakEnd                string       `json:"peak_end"`
	PeakRateMultiplier     float64      `json:"peak_rate_multiplier"`
	Rate                   *Sub2APIRate `json:"rate,omitempty"`
}

type Sub2APIInventorySnapshot struct {
	SyncedAt int64                   `json:"synced_at"`
	Keys     []Sub2APIInventoryKey   `json:"keys"`
	Groups   []Sub2APIInventoryGroup `json:"groups"`
}

type Sub2APIInventory struct {
	Channels        []Sub2APIInventoryChannel `json:"-"`
	Version         int                       `json:"version"`
	AccountRevision int64                     `json:"account_revision"`
	Snapshot        Sub2APIInventorySnapshot  `json:"snapshot"`
	KeyIdentities   map[int64]string          `json:"key_identities"`
}

type Sub2APIInventoryChannel struct {
	Channel  *Channel
	Bindings []Sub2APIKeyBinding
	Rates    map[int64]*Sub2APIRate
}

func storeSub2APIInventoryRates(tx *gorm.DB, channel *Channel, bindings []Sub2APIKeyBinding, rates map[int64]*Sub2APIRate) error {
	for _, binding := range bindings {
		rate := rates[binding.UpstreamGroupID]
		if rate == nil {
			continue
		}
		copy := *rate
		copy.SourceIdentity = channel.Sub2APIConnectionIdentity()
		encoded, err := common.Marshal(copy)
		if err != nil {
			return err
		}
		if err := tx.Model(&Sub2APIKeyBinding{}).Where("channel_id = ? AND key_identity = ?", channel.Id, binding.KeyIdentity).Updates(map[string]any{"rate_json": string(encoded), "last_sync_at": time.Now().Unix(), "last_error": ""}).Error; err != nil {
			return err
		}
	}
	return nil
}

type Sub2APISyncRecord struct {
	At             int64  `json:"at"`
	Success        bool   `json:"success"`
	ErrorCode      string `json:"error_code,omitempty"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	Keys           int    `json:"keys"`
	Groups         int    `json:"groups"`
}

// Small coordination state is stored separately from the potentially large
// complete inventory, so each paginated request does not rewrite the inventory.
type Sub2APISyncState struct {
	LastAttemptAt   int64               `json:"last_attempt_at"`
	NextAttemptAt   int64               `json:"next_attempt_at"`
	BlockedUntil    int64               `json:"blocked_until"`
	Failures        int                 `json:"failures"`
	WindowAt        int64               `json:"window_at"`
	Requests        int                 `json:"requests"`
	UsageRequests   int                 `json:"usage_requests"`
	LedgerPages     map[string]int      `json:"ledger_pages,omitempty"`
	LedgerCheckedAt map[string]int64    `json:"ledger_checked_at,omitempty"`
	Records         []Sub2APISyncRecord `json:"records"`
}

func ReadSub2APISyncState(account *Sub2APIAccount) (Sub2APISyncState, error) {
	var state Sub2APISyncState
	if account.SyncStateJSON != "" {
		if err := common.UnmarshalJsonStr(account.SyncStateJSON, &state); err != nil {
			return state, err
		}
	}
	return state, nil
}

func WithSub2APISyncLock(ctx context.Context, accountID int64, kind string, operation func() error) error {
	return withSub2APICredentialLock(ctx, "sync:"+kind+":"+strconv.FormatInt(accountID, 10), operation)
}

// All writers merge the small state under a row lock. Credential rotation and
// detach never overwrite it, and disconnect prevents a late snapshot publish.
func UpdateSub2APIInventory(ctx context.Context, accountID int64, inventory *Sub2APIInventory, change func(*Sub2APISyncState) error) error {
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account Sub2APIAccount
		if err := lockForUpdate(tx).Select("id", "channel_id", "credential", "revision", "sync_state_json").Where("id = ? AND credential <> ?", accountID, "").First(&account).Error; err != nil {
			return err
		}
		state, err := ReadSub2APISyncState(&account)
		if err != nil {
			return err
		}
		previousRecord := Sub2APISyncRecord{}
		if len(state.Records) > 0 {
			previousRecord = state.Records[len(state.Records)-1]
		}
		if change != nil {
			if err = change(&state); err != nil {
				return err
			}
		}
		encoded, err := common.Marshal(state)
		if err != nil {
			return err
		}
		values := map[string]any{"sync_state_json": string(encoded)}
		if len(state.Records) > 0 && state.Records[len(state.Records)-1] != previousRecord {
			values["last_error"] = state.Records[len(state.Records)-1].ErrorCode
		}
		if inventory != nil {
			if inventory.AccountRevision != account.Revision {
				return gorm.ErrRecordNotFound
			}
			for _, plan := range inventory.Channels {
				var current Channel
				if err := lockForUpdate(tx).First(&current, plan.Channel.Id).Error; err != nil {
					return err
				}
				if current.Type != constant.ChannelTypeSub2API || current.Key != plan.Channel.Key || current.Sub2APIConnectionIdentity() != plan.Channel.Sub2APIConnectionIdentity() {
					return gorm.ErrRecordNotFound
				}
				var associated int64
				if err := tx.Model(&Sub2APIKeyBinding{}).Where("account_id = ? AND channel_id = ?", accountID, current.Id).Count(&associated).Error; err != nil {
					return err
				}
				if associated == 0 {
					return gorm.ErrRecordNotFound
				}
				if err := replaceSub2APIBindings(tx, &account, current.Id, plan.Bindings); err != nil {
					return err
				}
				if err := storeSub2APIInventoryRates(tx, &current, plan.Bindings, plan.Rates); err != nil {
					return err
				}
			}
			encoded, err = common.Marshal(inventory)
			if err != nil {
				return err
			}
			values["inventory_json"], values["last_sync_at"], values["last_error"] = string(encoded), inventory.Snapshot.SyncedAt, ""
		}
		return tx.Model(&Sub2APIAccount{}).Where("id = ?", accountID).Updates(values).Error
	})
	if err == nil && inventory != nil {
		for _, plan := range inventory.Channels {
			InvalidateSub2APIBindings(plan.Channel.Id)
		}
	}
	return err
}
