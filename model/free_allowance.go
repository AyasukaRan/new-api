package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrNoFreeAllowance = errors.New("no matching free allowance")
var ErrFreeAllowanceWalletInsufficient = errors.New("free allowance wallet quota insufficient")

// ValidateFreeAllowance keeps gifts separate from purchasable subscriptions.
func (p *SubscriptionPlan) ValidateFreeAllowance() error {
	if !p.FreeAllowance {
		if p.DurationUnit == SubscriptionDurationForever {
			return common.NewMessage("Unlimited duration is only available for free allowances")
		}
		p.ApplicableModels = ""
		return nil
	}
	patterns := strings.FieldsFunc(p.ApplicableModels, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	if p.TotalAmount <= 0 || p.TotalAmount > common.MaxWalletQuota || len(patterns) == 0 || len(p.ApplicableModels) > 4096 {
		return common.NewMessage("Free allowance requires a positive quota and at least one model pattern")
	}
	for i, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		prefix := strings.TrimSuffix(pattern, "*")
		if prefix == "" || strings.ContainsAny(prefix, "*?[]") || strings.ContainsAny(pattern, " \t") {
			return common.NewMessage("Model patterns only support a trailing *")
		}
		patterns[i] = pattern
	}
	if strings.TrimSpace(p.UpgradeGroup) != "" || strings.TrimSpace(p.DowngradeGroup) != "" {
		return common.NewMessage("Free allowances cannot change user groups")
	}
	p.ApplicableModels = strings.Join(patterns, ",")
	p.PriceAmount = 0
	p.AllowBalancePay = common.GetPointer(false)
	return nil
}

func (s *UserSubscription) MatchesFreeAllowanceModel(modelName string) bool {
	if !s.FreeAllowance {
		return false
	}
	for pattern := range strings.SplitSeq(s.ApplicableModels, ",") {
		pattern = strings.TrimSpace(pattern)
		if prefix, wildcard := strings.CutSuffix(pattern, "*"); wildcard {
			if prefix != "" && strings.HasPrefix(modelName, prefix) {
				return true
			}
		} else if pattern != "" && modelName == pattern {
			return true
		}
	}
	return false
}

func HasMatchingFreeAllowance(userID int, modelName string) (bool, error) {
	var subscriptions []UserSubscription
	err := DB.Select("free_allowance", "applicable_models").Where("user_id = ? AND status = ? AND end_time > ? AND free_allowance = ?", userID, "active", common.GetTimestamp(), true).Find(&subscriptions).Error
	if err != nil {
		return false, err
	}
	for _, sub := range subscriptions {
		if sub.MatchesFreeAllowanceModel(modelName) {
			return true, nil
		}
	}
	return false, nil
}

type FreeAllowanceResult struct {
	SubscriptionID int
	PlanID         int
	PlanTitle      string
	FreeQuota      int64
	WalletQuota    int64
	AmountTotal    int64
	AmountUsed     int64
	Settled        bool
}

// UpdateFreeAllowanceFunding uses target (not delta) so reserve, settle and
// refund can be retried without charging or crediting a request twice.
// action is reserve, settle or refund. Existing paid-subscription paths never enter here.
func UpdateFreeAllowanceFunding(requestID string, userID int, modelName string, target int64, action string) (*FreeAllowanceResult, error) {
	if requestID == "" || userID <= 0 || target < 0 || target > common.MaxQuota {
		return nil, errors.New("invalid free allowance funding request")
	}
	if action != "reserve" && action != "settle" && action != "refund" {
		return nil, errors.New("invalid free allowance funding action")
	}
	if action == "refund" {
		target = 0
	}
	if common.RedisEnabled {
		if _, err := GetUserCache(userID); err != nil {
			return nil, err
		}
	}
	result := &FreeAllowanceResult{}
	var cacheDebit, cacheCredit int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Match the user -> subscription lock order used by grant/revoke operations.
		var user User
		if err := lockForUpdate(tx).Select("id", "quota").First(&user, userID).Error; err != nil {
			return err
		}
		var record SubscriptionPreConsumeRecord
		query := lockForUpdate(tx).Where("request_id = ?", requestID).Limit(1).Find(&record)
		if query.Error != nil {
			return query.Error
		}
		exists := query.RowsAffected > 0
		var sub UserSubscription
		now := GetDBTimestampTx(tx)
		if exists {
			if !record.FreeAllowance || record.UserId != userID {
				return errors.New("request funding owner does not match")
			}
			if record.Status == "settled" || record.Status == "refunded" {
				if record.Status == "settled" && action == "settle" && record.SettledQuota != target {
					return errors.New("request was settled with a different amount")
				}
				if record.Status == "refunded" && action != "refund" {
					return errors.New("free allowance request was already refunded")
				}
				result.SubscriptionID = record.UserSubscriptionId
				result.FreeQuota, result.WalletQuota = record.PreConsumed, record.WalletQuota
				result.Settled = record.Status == "settled"
				// Completed funding remains idempotent even after its subscription is deleted.
				if err := tx.Where("id = ?", record.UserSubscriptionId).Find(&sub).Error; err != nil {
					return err
				}
				result.PlanID, result.AmountTotal, result.AmountUsed = sub.PlanId, sub.AmountTotal, sub.AmountUsed
				if plan, planErr := getSubscriptionPlanByIdTx(tx, sub.PlanId); planErr == nil {
					result.PlanTitle = plan.Title
				}
				return nil
			}
			if err := lockForUpdate(tx).First(&sub, record.UserSubscriptionId).Error; err != nil {
				return err
			}
		} else {
			if action != "reserve" {
				return ErrNoFreeAllowance
			}
			var candidates []UserSubscription
			if err := lockForUpdate(tx).Where("user_id = ? AND status = ? AND end_time > ? AND free_allowance = ?", userID, "active", now, true).Order("end_time asc, id asc").Find(&candidates).Error; err != nil {
				return err
			}
			for _, candidate := range candidates {
				if !candidate.MatchesFreeAllowanceModel(modelName) {
					continue
				}
				plan, err := getSubscriptionPlanByIdTx(tx, candidate.PlanId)
				if err != nil {
					return err
				}
				if err := maybeResetUserSubscriptionWithPlanTx(tx, &candidate, plan, now); err != nil {
					return err
				}
				if sub.Id == 0 || candidate.AmountUsed < candidate.AmountTotal {
					sub = candidate
				}
				if candidate.AmountUsed < candidate.AmountTotal {
					break
				}
			}
			if sub.Id == 0 {
				return ErrNoFreeAllowance
			}
			if sub.AmountUsed >= sub.AmountTotal {
				var paid int64
				if err := tx.Model(&UserSubscription{}).Where("user_id = ? AND status = ? AND end_time > ?", userID, "active", now).Where("free_allowance = ? OR free_allowance IS NULL", false).Count(&paid).Error; err != nil {
					return err
				}
				if paid > 0 {
					return ErrNoFreeAllowance
				}
			}
			record = SubscriptionPreConsumeRecord{RequestId: requestID, UserId: userID, UserSubscriptionId: sub.Id, FreeAllowance: true, PeriodStart: sub.LastResetTime, PeriodGeneration: sub.ResetGeneration, Status: "reserved"}
		}
		plan, err := getSubscriptionPlanByIdTx(tx, sub.PlanId)
		if err != nil {
			return err
		}
		result.SubscriptionID, result.PlanID, result.PlanTitle = sub.Id, sub.PlanId, plan.Title
		if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
			return err
		}
		samePeriod := record.PeriodStart == sub.LastResetTime && record.PeriodGeneration == sub.ResetGeneration
		available := record.PreConsumed
		if samePeriod && sub.Status == "active" && sub.EndTime > now {
			available = max(sub.AmountTotal-sub.AmountUsed+record.PreConsumed, 0)
		}
		free := min(target, available)
		wallet := target - free
		if wallet > 0 && !sub.AllowWalletOverflow {
			return ErrFreeAllowanceWalletInsufficient
		}
		walletDelta := wallet - record.WalletQuota
		if walletDelta > 0 {
			cached := false
			if common.RedisEnabled {
				var result cacheQuotaResult
				if action == "reserve" {
					result, err = cacheTryReserveUserQuota(userID, walletDelta)
				} else {
					result, err = cacheApplyUserQuotaDelta(userID, -walletDelta)
				}
				if err != nil {
					return fmt.Errorf("free allowance wallet cache unavailable: %w", err)
				}
				if result == cacheQuotaInsufficient {
					return ErrFreeAllowanceWalletInsufficient
				}
				cached = result == cacheQuotaOK
				if cached {
					cacheDebit = walletDelta
				}
			}
			update := tx.Model(&User{}).Where("id = ?", userID)
			if action == "reserve" && !cached {
				update = update.Where("quota >= ?", walletDelta)
			}
			changed := update.Update("quota", gorm.Expr("quota - ?", walletDelta))
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrFreeAllowanceWalletInsufficient
			}
		} else if walletDelta < 0 {
			credit := -walletDelta
			changed := tx.Model(&User{}).Where("id = ? AND quota <= ?", userID, common.MaxWalletQuota-credit).Update("quota", gorm.Expr("quota + ?", credit))
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrWalletQuotaLimitExceeded
			}
			cacheCredit = credit
		}
		if samePeriod {
			sub.AmountUsed = max(sub.AmountUsed-record.PreConsumed+free, 0)
			if sub.AmountUsed > sub.AmountTotal {
				return errors.New("free allowance exceeds total")
			}
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
		}
		record.PreConsumed, record.WalletQuota = free, wallet
		if action == "settle" {
			record.Status, record.SettledQuota = "settled", target
		}
		if action == "refund" {
			record.Status = "refunded"
		}
		if exists {
			err = tx.Save(&record).Error
		} else {
			err = tx.Create(&record).Error
		}
		if err != nil {
			return err
		}
		result.FreeQuota, result.WalletQuota = free, wallet
		result.AmountTotal, result.AmountUsed = sub.AmountTotal, sub.AmountUsed
		result.Settled = action == "settle"
		return nil
	})
	if common.RedisEnabled {
		delta := cacheCredit
		outcome := "database committed"
		if err != nil {
			delta = cacheDebit
			outcome = "database rolled back"
		}
		if delta > 0 {
			// Match the existing wallet cache contract: never replay an ambiguous
			// increment or discard unrelated batched debits by deleting the hash.
			if cacheResult, cacheErr := cacheApplyUserQuotaDelta(userID, delta); cacheErr != nil {
				common.SysError(common.LogText("free allowance wallet cache sync failed: request_id=%s user_id=%d delta=%d outcome=%s error=%v", requestID, userID, delta, outcome, cacheErr))
			} else if cacheResult != cacheQuotaOK && cacheResult != cacheQuotaMiss {
				common.SysError(common.LogText("free allowance wallet cache sync rejected: request_id=%s user_id=%d delta=%d outcome=%s result=%d", requestID, userID, delta, outcome, cacheResult))
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
