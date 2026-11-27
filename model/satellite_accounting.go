package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SatelliteAccountingDelta is a cumulative, signed counter, not one charge.
// A higher revision replaces the sender's previous cumulative observation.
type SatelliteAccountingDelta struct {
	Entity            string `json:"entity"`
	EntityID          int    `json:"entity_id"`
	Generation        int64  `json:"generation"`
	Revision          int64  `json:"revision"`
	QuotaDelta        int64  `json:"quota_delta"`
	UsedQuotaDelta    int64  `json:"used_quota_delta"`
	RequestCountDelta int64  `json:"request_count_delta"`
}

// SatelliteAccountingState is the sender's durable journal and the receiver's
// durable receipt. The local external baseline excludes this node's own deltas.
type SatelliteAccountingState struct {
	NodeID               string `gorm:"type:varchar(64);primaryKey"`
	Entity               string `gorm:"type:varchar(16);primaryKey"`
	EntityID             int    `gorm:"primaryKey;autoIncrement:false"`
	Generation           int64  `gorm:"type:bigint;primaryKey;autoIncrement:false"`
	Revision             int64  `gorm:"type:bigint;not null"`
	QuotaDelta           int64  `gorm:"type:bigint;not null"`
	UsedQuotaDelta       int64  `gorm:"type:bigint;not null"`
	RequestCountDelta    int64  `gorm:"type:bigint;not null"`
	BaselineInitialized  bool
	ExternalQuota        int64 `gorm:"type:bigint;not null"`
	ExternalUsedQuota    int64 `gorm:"type:bigint;not null"`
	ExternalRequestCount int64 `gorm:"type:bigint;not null"`
	AcknowledgedRevision int64 `gorm:"type:bigint;not null"`
}

type SatelliteAccountingAnchor struct {
	Entity       string                   `json:"entity"`
	EntityID     int                      `json:"entity_id"`
	Generation   int64                    `json:"generation"`
	Acknowledged SatelliteAccountingDelta `json:"acknowledged"`
	Quota        int64                    `json:"quota"`
	UsedQuota    int64                    `json:"used_quota"`
	RequestCount int64                    `json:"request_count"`
}

var ErrSatelliteAccounting = errors.New("invalid satellite accounting state")

// PostConsumeUserSubscriptionDeltaForRequest preserves the period selected at
// pre-consume. A late refund must never credit the next subscription period.
func PostConsumeUserSubscriptionDeltaForRequest(subscriptionID int, requestID string, delta int64) error {
	if !common.IsSatellite() {
		return PostConsumeUserSubscriptionDelta(subscriptionID, delta)
	}
	if requestID == "" || subscriptionID <= 0 {
		return ErrSatelliteAccounting
	}
	if delta == 0 {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).First(&sub, subscriptionID).Error; err != nil {
			return err
		}
		var record SubscriptionPreConsumeRecord
		if err := tx.Where("request_id = ? AND user_subscription_id = ?", requestID, subscriptionID).First(&record).Error; err != nil {
			return err
		}
		if record.FreeAllowance || record.PeriodGeneration > sub.ResetGeneration {
			return ErrSatelliteAccounting
		}
		return applySatelliteSubscriptionDelta(tx, &sub, record.PeriodGeneration, delta)
	})
}

func applySatelliteSubscriptionDelta(tx *gorm.DB, sub *UserSubscription, generation, delta int64) error {
	before := SatelliteAccountingAnchor{Entity: "subscription", EntityID: sub.Id, Generation: generation, UsedQuota: sub.AmountUsed}
	if generation == sub.ResetGeneration {
		used, err := satelliteAccountingAdd(sub.AmountUsed, delta)
		if err != nil {
			return err
		}
		used = max(used, 0)
		if sub.AmountTotal > 0 && used > sub.AmountTotal && used > sub.AmountUsed {
			return errors.New("subscription quota insufficient")
		}
		delta = used - sub.AmountUsed
		sub.AmountUsed = used
		if err := tx.Save(sub).Error; err != nil {
			return err
		}
	}
	return recordSatelliteAccountingMutation(tx, before, 0, delta, 0)
}

func refundSatelliteSubscription(requestID string) error {
	// Read only to locate the parent lock. The record is re-read after locking
	// the subscription, matching normal settlement's lock order.
	var initial SubscriptionPreConsumeRecord
	if err := DB.Where("request_id = ?", requestID).First(&initial).Error; err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).First(&sub, initial.UserSubscriptionId).Error; err != nil {
			return err
		}
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&record).Error; err != nil {
			return err
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.FreeAllowance || record.UserSubscriptionId != sub.Id || record.PeriodGeneration > sub.ResetGeneration {
			return ErrSatelliteAccounting
		}
		if record.PreConsumed > 0 {
			if err := applySatelliteSubscriptionDelta(tx, &sub, record.PeriodGeneration, -record.PreConsumed); err != nil {
				return err
			}
		}
		return tx.Model(&record).Update("status", "refunded").Error
	})
}

func (s SatelliteAccountingState) delta() SatelliteAccountingDelta {
	return SatelliteAccountingDelta{Entity: s.Entity, EntityID: s.EntityID, Generation: s.Generation, Revision: s.Revision, QuotaDelta: s.QuotaDelta, UsedQuotaDelta: s.UsedQuotaDelta, RequestCountDelta: s.RequestCountDelta}
}

func validSatelliteNodeID(node string) bool {
	return node != "" && len(node) <= 64 && strings.TrimSpace(node) == node && !strings.ContainsAny(node, "\x00\r\n")
}

// Bound all operands before addition, including receiver-controlled snapshots.
// Wallet counters use the existing exact-integer limit, avoiding float or int
// conversion and preventing a wrapped deduction from becoming a credit.
func satelliteAccountingAdd(a, b int64) (int64, error) {
	limit := int64(common.MaxWalletQuota)
	if a < -limit || a > limit || b < -limit || b > limit || (b > 0 && a > limit-b) || (b < 0 && a < -limit-b) {
		return 0, ErrSatelliteAccounting
	}
	return a + b, nil
}

func validateSatelliteDelta(d SatelliteAccountingDelta) error {
	if d.EntityID <= 0 || d.Generation < 0 || d.Generation > int64(common.MaxWalletQuota) || d.Revision < 0 || d.Revision > int64(common.MaxWalletQuota) {
		return ErrSatelliteAccounting
	}
	for _, value := range []int64{d.QuotaDelta, d.UsedQuotaDelta, d.RequestCountDelta} {
		if _, err := satelliteAccountingAdd(0, value); err != nil {
			return err
		}
	}
	if d.Entity != "subscription" && d.Generation != 0 {
		return ErrSatelliteAccounting
	}
	switch d.Entity {
	case "user":
	case "token":
		if d.UsedQuotaDelta != -d.QuotaDelta || d.RequestCountDelta != 0 {
			return ErrSatelliteAccounting
		}
	case "channel", "subscription":
		if d.QuotaDelta != 0 || d.RequestCountDelta != 0 {
			return ErrSatelliteAccounting
		}
	default:
		return ErrSatelliteAccounting
	}
	return nil
}

func satelliteAccountingState(tx *gorm.DB, node, entity string, id int, generation int64) (*SatelliteAccountingState, error) {
	state := SatelliteAccountingState{NodeID: node, Entity: entity, EntityID: id, Generation: generation}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return nil, err
	}
	err := lockForUpdate(tx).Where("node_id = ? AND entity = ? AND entity_id = ? AND generation = ?", node, entity, id, generation).First(&state).Error
	return &state, err
}

// Generation zero is a valid part of the composite identity. GORM Save would
// treat that zero primary-key component as a new row; update the locked receipt
// explicitly so the first generation has the same semantics as later ones.
func saveSatelliteAccountingState(tx *gorm.DB, state *SatelliteAccountingState) error {
	return tx.Model(&SatelliteAccountingState{}).
		Where("node_id = ? AND entity = ? AND entity_id = ? AND generation = ?", state.NodeID, state.Entity, state.EntityID, state.Generation).
		Select("revision", "quota_delta", "used_quota_delta", "request_count_delta", "baseline_initialized", "external_quota", "external_used_quota", "external_request_count", "acknowledged_revision").Updates(state).Error
}

// Read/lock the entity before the receipt everywhere, giving concurrent local
// charges, imports and reconciliation one consistent lock order.
func satelliteAccountingEntity(tx *gorm.DB, entity string, id int, locked bool) (SatelliteAccountingAnchor, string, error) {
	anchor := SatelliteAccountingAnchor{Entity: entity, EntityID: id, Acknowledged: SatelliteAccountingDelta{Entity: entity, EntityID: id}}
	query := tx
	if locked {
		query = lockForUpdate(query)
	}
	switch entity {
	case "user":
		var row User
		err := query.Unscoped().Select("id", "quota", "used_quota", "request_count").First(&row, id).Error
		anchor.Quota, anchor.UsedQuota, anchor.RequestCount = int64(row.Quota), int64(row.UsedQuota), int64(row.RequestCount)
		return anchor, "", err
	case "token":
		var row Token
		err := query.Unscoped().Select("id", commonKeyCol, "remain_quota", "used_quota").First(&row, id).Error
		anchor.Quota, anchor.UsedQuota = int64(row.RemainQuota), int64(row.UsedQuota)
		return anchor, row.Key, err
	case "channel":
		var row Channel
		err := query.Select("id", "used_quota").First(&row, id).Error
		anchor.UsedQuota = row.UsedQuota
		return anchor, "", err
	case "subscription":
		var row UserSubscription
		err := query.Select("id", "amount_used", "reset_generation").First(&row, id).Error
		anchor.UsedQuota, anchor.Generation = row.AmountUsed, row.ResetGeneration
		anchor.Acknowledged.Generation = row.ResetGeneration
		return anchor, "", err
	default:
		return anchor, "", ErrSatelliteAccounting
	}
}

func writeSatelliteAccountingEntity(tx *gorm.DB, before SatelliteAccountingAnchor, quota, used, count int64) error {
	q, err := satelliteAccountingAdd(before.Quota, quota)
	if err != nil {
		return err
	}
	u, err := satelliteAccountingAdd(before.UsedQuota, used)
	if err != nil {
		return err
	}
	r, err := satelliteAccountingAdd(before.RequestCount, count)
	if err != nil || r < 0 {
		return ErrSatelliteAccounting
	}
	var result *gorm.DB
	switch before.Entity {
	case "user":
		result = tx.Unscoped().Model(&User{}).Where("id = ?", before.EntityID).Updates(map[string]any{"quota": q, "used_quota": u, "request_count": r})
	case "token":
		result = tx.Unscoped().Model(&Token{}).Where("id = ?", before.EntityID).Updates(map[string]any{"remain_quota": q, "used_quota": u, "accessed_time": common.GetTimestamp()})
	case "channel":
		result = tx.Model(&Channel{}).Where("id = ?", before.EntityID).Update("used_quota", u)
	case "subscription":
		if q != 0 || r != 0 || u < 0 {
			return ErrSatelliteAccounting
		}
		result = tx.Model(&UserSubscription{}).Where("id = ? AND reset_generation = ?", before.EntityID, before.Generation).Update("amount_used", u)
	default:
		return ErrSatelliteAccounting
	}
	// The entity was locked and found above; MySQL may report zero changed
	// rows for a zero delta. Existence must not depend on changed-row counts.
	return result.Error
}

func mutateSatelliteAccounting(entity string, id int, quota, used, count int64, reserve bool) (bool, error) {
	node := common.SatelliteNodeID()
	delta := SatelliteAccountingDelta{Entity: entity, EntityID: id, QuotaDelta: quota, UsedQuotaDelta: used, RequestCountDelta: count}
	if !validSatelliteNodeID(node) || validateSatelliteDelta(delta) != nil {
		return false, ErrSatelliteAccounting
	}
	changed := false
	var tokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		before, key, err := satelliteAccountingEntity(tx, entity, id, true)
		if err != nil {
			return err
		}
		tokenKey = key
		if reserve && (quota > 0 || before.Quota < -quota) {
			return nil
		}
		if err := recordSatelliteAccountingMutation(tx, before, quota, used, count); err != nil {
			return err
		}
		if err := writeSatelliteAccountingEntity(tx, before, quota, used, count); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && changed {
		invalidateSatelliteAccountingCache(entity, id, tokenKey)
	}
	return changed, err
}

// recordSatelliteAccountingMutation joins an already locked billing transaction,
// including free-allowance transactions that change wallet and gift together.
func recordSatelliteAccountingMutation(tx *gorm.DB, before SatelliteAccountingAnchor, quota, used, count int64) error {
	if !common.IsSatellite() || (quota == 0 && used == 0 && count == 0) {
		return nil
	}
	node := common.SatelliteNodeID()
	delta := SatelliteAccountingDelta{Entity: before.Entity, EntityID: before.EntityID, Generation: before.Generation, QuotaDelta: quota, UsedQuotaDelta: used, RequestCountDelta: count}
	if !validSatelliteNodeID(node) || validateSatelliteDelta(delta) != nil {
		return ErrSatelliteAccounting
	}
	state, err := satelliteAccountingState(tx, node, before.Entity, before.EntityID, before.Generation)
	if err != nil {
		return err
	}
	if !state.BaselineInitialized {
		state.ExternalQuota, state.ExternalUsedQuota, state.ExternalRequestCount = before.Quota, before.UsedQuota, before.RequestCount
		state.BaselineInitialized = true
	}
	if state.QuotaDelta, err = satelliteAccountingAdd(state.QuotaDelta, quota); err != nil {
		return err
	}
	if state.UsedQuotaDelta, err = satelliteAccountingAdd(state.UsedQuotaDelta, used); err != nil {
		return err
	}
	if state.RequestCountDelta, err = satelliteAccountingAdd(state.RequestCountDelta, count); err != nil {
		return err
	}
	if state.Revision, err = satelliteAccountingAdd(state.Revision, 1); err != nil {
		return err
	}
	return saveSatelliteAccountingState(tx, state)
}

func invalidateSatelliteAccountingCache(entity string, id int, tokenKey string) {
	var err error
	if entity == "user" {
		err = invalidateUserCache(id)
	}
	if entity == "token" {
		err = invalidateTokenCacheForMutation(tokenKey)
	}
	if err != nil {
		common.SysError(fmt.Sprintf("satellite accounting cache invalidation failed: entity=%s id=%d", entity, id))
	}
}

func GetSatelliteAccountingDeltas(ctx context.Context) ([]SatelliteAccountingDelta, error) {
	var rows []SatelliteAccountingState
	if !validSatelliteNodeID(common.SatelliteNodeID()) {
		return nil, ErrSatelliteAccounting
	}
	err := DB.WithContext(ctx).Where("node_id = ? AND revision > 0", common.SatelliteNodeID()).Order("entity, entity_id, generation").Find(&rows).Error
	result := make([]SatelliteAccountingDelta, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.delta())
	}
	return result, err
}

// ApplySatelliteAccountingDeltas atomically receives one bounded batch. Only
// existing entities may be debited; repeats, old revisions and lost ACKs cannot
// charge again. The caller authenticates node and must not trust a body node ID.
func ApplySatelliteAccountingDeltas(ctx context.Context, node string, deltas []SatelliteAccountingDelta) error {
	if !validSatelliteNodeID(node) || len(deltas) > 10000 {
		return ErrSatelliteAccounting
	}
	deltas = slices.Clone(deltas)
	order := map[string]int{"user": 0, "subscription": 1, "token": 2, "channel": 3}
	slices.SortFunc(deltas, func(a, b SatelliteAccountingDelta) int {
		if order[a.Entity] != order[b.Entity] {
			return order[a.Entity] - order[b.Entity]
		}
		if a.EntityID < b.EntityID {
			return -1
		}
		if a.EntityID > b.EntityID {
			return 1
		}
		if a.Generation < b.Generation {
			return -1
		}
		if a.Generation > b.Generation {
			return 1
		}
		return 0
	})
	for i, d := range deltas {
		if validateSatelliteDelta(d) != nil || d.Revision == 0 || (i > 0 && d.Entity == deltas[i-1].Entity && d.EntityID == deltas[i-1].EntityID && d.Generation == deltas[i-1].Generation) {
			return ErrSatelliteAccounting
		}
	}
	type cacheDelta struct {
		entity string
		id     int
		key    string
		quota  int64
	}
	var cacheChanges []cacheDelta
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, d := range deltas {
			before, key, err := satelliteAccountingEntity(tx, d.Entity, d.EntityID, true)
			deleted := errors.Is(err, gorm.ErrRecordNotFound)
			if err != nil && !deleted {
				return err
			}
			var state *SatelliteAccountingState
			if deleted {
				// Only a previously issued identity can acknowledge a late mutation
				// after deletion. Never create a receipt from an unknown incoming ID.
				state = &SatelliteAccountingState{}
				err = lockForUpdate(tx).Where("node_id = ? AND entity = ? AND entity_id = ? AND generation = ?", node, d.Entity, d.EntityID, d.Generation).First(state).Error
			} else {
				state, err = satelliteAccountingState(tx, node, d.Entity, d.EntityID, d.Generation)
			}
			if err != nil {
				return err
			}
			if d.Revision < state.Revision {
				continue
			}
			if d.Revision == state.Revision {
				if d != state.delta() {
					return ErrSatelliteAccounting
				}
				continue
			}
			q, err := satelliteAccountingAdd(d.QuotaDelta, -state.QuotaDelta)
			if err != nil {
				return err
			}
			u, err := satelliteAccountingAdd(d.UsedQuotaDelta, -state.UsedQuotaDelta)
			if err != nil {
				return err
			}
			r, err := satelliteAccountingAdd(d.RequestCountDelta, -state.RequestCountDelta)
			if err != nil {
				return err
			}
			if !deleted {
				if d.Entity != "subscription" || d.Generation == before.Generation {
					if err := writeSatelliteAccountingEntity(tx, before, q, u, r); err != nil {
						return err
					}
				} else if d.Generation > before.Generation {
					return ErrSatelliteAccounting
				}
			}
			state.Revision, state.QuotaDelta, state.UsedQuotaDelta, state.RequestCountDelta = d.Revision, d.QuotaDelta, d.UsedQuotaDelta, d.RequestCountDelta
			if err := saveSatelliteAccountingState(tx, state); err != nil {
				return err
			}
			if !deleted {
				cacheChanges = append(cacheChanges, cacheDelta{d.Entity, d.EntityID, key, q})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Primary Redis may include a still-in-flight reservation. Apply only the
	// received difference, never replace that cache with the database snapshot.
	for _, change := range cacheChanges {
		var cacheErr error
		if change.entity == "user" {
			cacheErr = cacheIncrUserQuota(change.id, change.quota)
		}
		if change.entity == "token" && common.RedisEnabled {
			_, cacheErr = cacheApplyTokenQuotaDelta(change.id, change.key, change.quota)
		}
		if cacheErr != nil {
			common.SysError("satellite accounting committed; cache update failed")
		}
	}
	return nil
}

// ExportSatelliteAccountingAnchors belongs in the same repeatable-read/config
// transaction as the user, token and channel snapshot. Missing receipts mean 0.
func ExportSatelliteAccountingAnchors(tx *gorm.DB, node string) ([]SatelliteAccountingAnchor, error) {
	if !validSatelliteNodeID(node) {
		return nil, ErrSatelliteAccounting
	}
	var states []SatelliteAccountingState
	if err := tx.Where("node_id = ?", node).Find(&states).Error; err != nil {
		return nil, err
	}
	receipts := make(map[string]SatelliteAccountingDelta, len(states))
	for _, state := range states {
		receipts[fmt.Sprintf("%s:%d:%d", state.Entity, state.EntityID, state.Generation)] = state.delta()
	}
	var result []SatelliteAccountingAnchor
	for _, entity := range []string{"user", "token", "channel", "subscription"} {
		var ids []int
		var table any
		switch entity {
		case "user":
			table = &User{}
		case "token":
			table = &Token{}
		case "channel":
			table = &Channel{}
		case "subscription":
			table = &UserSubscription{}
		}
		if err := tx.Unscoped().Model(table).Order("id").Pluck("id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			anchor, _, err := satelliteAccountingEntity(tx, entity, id, false)
			if err != nil {
				return nil, err
			}
			if receipt, ok := receipts[fmt.Sprintf("%s:%d:%d", entity, id, anchor.Generation)]; ok {
				anchor.Acknowledged = receipt
			} else {
				// Record which identity/generation was issued before it can be
				// removed administratively while that node is disconnected.
				state := SatelliteAccountingState{NodeID: node, Entity: entity, EntityID: id, Generation: anchor.Generation}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
					return nil, err
				}
			}
			result = append(result, anchor)
		}
	}
	return result, nil
}

// ReconcileSatelliteAccountingAnchors changes only the external contribution:
// M - acknowledged_local_delta. Any local charges after upload remain intact.
// The caller serializes sync responses and invalidates caches after COMMIT.
func ReconcileSatelliteAccountingAnchors(tx *gorm.DB, anchors []SatelliteAccountingAnchor) error {
	node := common.SatelliteNodeID()
	if !validSatelliteNodeID(node) {
		return ErrSatelliteAccounting
	}
	for _, anchor := range anchors {
		ack := anchor.Acknowledged
		if anchor.Entity != ack.Entity || anchor.EntityID != ack.EntityID || anchor.Generation != ack.Generation || validateSatelliteDelta(ack) != nil {
			return ErrSatelliteAccounting
		}
		before, _, err := satelliteAccountingEntity(tx, anchor.Entity, anchor.EntityID, true)
		if err != nil {
			return err
		}
		if before.Generation != anchor.Generation {
			return ErrSatelliteAccounting
		}
		state, err := satelliteAccountingState(tx, node, anchor.Entity, anchor.EntityID, anchor.Generation)
		if err != nil {
			return err
		}
		if ack.Revision < state.AcknowledgedRevision {
			continue
		}
		if ack.Revision > state.Revision {
			return ErrSatelliteAccounting
		}
		if ack.Revision == state.Revision && ack != state.delta() {
			return ErrSatelliteAccounting
		}
		q, err := satelliteAccountingAdd(anchor.Quota, -ack.QuotaDelta)
		if err != nil {
			return err
		}
		u, err := satelliteAccountingAdd(anchor.UsedQuota, -ack.UsedQuotaDelta)
		if err != nil {
			return err
		}
		r, err := satelliteAccountingAdd(anchor.RequestCount, -ack.RequestCountDelta)
		if err != nil {
			return err
		}
		if !state.BaselineInitialized {
			if state.Revision != 0 {
				return ErrSatelliteAccounting
			}
			state.ExternalQuota, state.ExternalUsedQuota, state.ExternalRequestCount = before.Quota, before.UsedQuota, before.RequestCount
		}
		dq, err := satelliteAccountingAdd(q, -state.ExternalQuota)
		if err != nil {
			return err
		}
		du, err := satelliteAccountingAdd(u, -state.ExternalUsedQuota)
		if err != nil {
			return err
		}
		dr, err := satelliteAccountingAdd(r, -state.ExternalRequestCount)
		if err != nil {
			return err
		}
		if err := writeSatelliteAccountingEntity(tx, before, dq, du, dr); err != nil {
			return err
		}
		state.ExternalQuota, state.ExternalUsedQuota, state.ExternalRequestCount = q, u, r
		state.BaselineInitialized, state.AcknowledgedRevision = true, ack.Revision
		if err := saveSatelliteAccountingState(tx, state); err != nil {
			return err
		}
	}
	return nil
}
