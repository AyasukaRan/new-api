package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/keyusage"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sub2APIBillingRef is frozen before sending an attempt. Key indices are not
// identities: an administrator can reorder them while a request is running.
type Sub2APIBillingRef struct {
	AccountID   int64  `json:"account_id"`
	APIKeyID    int64  `json:"api_key_id"`
	KeyIdentity string `json:"key_identity"`
	RequestID   string `json:"request_id"`
	ResponseID  string `json:"response_id,omitempty"`
}

// Sub2APIBill lives in the main database. Source logs (including ClickHouse)
// remain immutable; nil CostQuota means not yet reconciled, never a free call.
type Sub2APIBill struct {
	ID              int64  `gorm:"primaryKey"`
	MatchID         string `gorm:"type:varchar(64);uniqueIndex"`
	AccountID       int64  `gorm:"index"`
	APIKeyID        int64
	KeyIdentity     string  `gorm:"type:varchar(64)"`
	RequestID       string  `gorm:"type:varchar(160)"`
	ResponseID      string  `gorm:"type:varchar(160)"`
	InvoiceIdentity *string `gorm:"type:varchar(64);uniqueIndex"`
	ChannelID       int
	LogID           int
	LogRequestID    string `gorm:"type:varchar(128)"`
	CreatedAt       int64  `gorm:"index"`
	NextSyncAt      int64  `gorm:"index"`
	SyncedAt        int64
	UsageID         int64
	ActualCostUSD   string `gorm:"type:varchar(64)"`
	CostQuota       *float64
	RateMultiplier  *float64
	BillingType     int
}

type Sub2APIBillStatus struct {
	KeyIdentity  string
	Pending      int64
	Synced       int64
	LastSyncedAt int64
}

func GetSub2APIBillStatus(ctx context.Context, channelID int) (map[string]Sub2APIBillStatus, error) {
	result := make(map[string]Sub2APIBillStatus)
	if !Sub2APIStorageReady() {
		return result, nil
	}
	var rows []Sub2APIBillStatus
	err := DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("channel_id = ?", channelID).
		Select("key_identity, COUNT(*) - COUNT(cost_quota) AS pending, COUNT(cost_quota) AS synced, COALESCE(MAX(synced_at),0) AS last_synced_at").Group("key_identity").Scan(&rows).Error
	for _, row := range rows {
		result[row.KeyIdentity] = row
	}
	return result, err
}

func sub2APIBillMatchID(channelID int, ref Sub2APIBillingRef) string {
	data, _ := common.Marshal([]any{channelID, ref.KeyIdentity, ref.RequestID})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func sub2APILogBillingRef(log *Log) (Sub2APIBillingRef, bool) {
	var metadata struct {
		AdminInfo struct {
			Ref Sub2APIBillingRef `json:"sub2api_billing"`
		} `json:"admin_info"`
	}
	if log == nil || common.UnmarshalJsonStr(log.Other, &metadata) != nil {
		return Sub2APIBillingRef{}, false
	}
	ref := metadata.AdminInfo.Ref
	if ref.RequestID == "" {
		ref.RequestID = ref.ResponseID
	}
	return ref, ref.AccountID > 0 && ref.APIKeyID > 0 && len(ref.KeyIdentity) == 64 && len(ref.RequestID) > 0 && len(ref.RequestID) <= 160
}

// QueueSub2APIBill can be replayed after a crash between the log and main DB
// commits. An upstream charge has one owner even if an upstream reuses its ID.
func QueueSub2APIBill(ctx context.Context, log *Log) error {
	if !Sub2APIStorageReady() || log == nil || log.Id <= 0 || log.RequestId == "" || (log.Type != LogTypeConsume && log.Type != LogTypeError) {
		return nil
	}
	ref, ok := sub2APILogBillingRef(log)
	if !ok {
		return nil
	}
	row := Sub2APIBill{MatchID: sub2APIBillMatchID(log.ChannelId, ref), AccountID: ref.AccountID, APIKeyID: ref.APIKeyID,
		KeyIdentity: ref.KeyIdentity, RequestID: ref.RequestID, ResponseID: ref.ResponseID, ChannelID: log.ChannelId, LogID: log.Id, LogRequestID: log.RequestId,
		CreatedAt: log.CreatedAt, NextSyncAt: common.GetTimestamp() + 30}
	return DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// SettleSub2APIBill never adjusts a user's quota, wallet or token balance.
// actual_cost already includes the upstream group/key/peak multiplier.
func SettleSub2APIBill(ctx context.Context, pending Sub2APIBill, usageID, apiKeyID int64, requestID, actualUSD string, billingType int, rate *float64) error {
	if pending.AccountID <= 0 || usageID <= 0 || pending.APIKeyID != apiKeyID || requestID == "" || (pending.RequestID != requestID && pending.ResponseID != requestID) || (billingType != 0 && billingType != 1) {
		return errors.New("invalid upstream bill identity")
	}
	if len(actualUSD) > 64 || !(common.QuotaPerUnit > 0) || math.IsInf(common.QuotaPerUnit, 0) || math.IsNaN(common.QuotaPerUnit) {
		return errors.New("invalid upstream bill amount")
	}
	bounded, err := strconv.ParseFloat(actualUSD, 64)
	if err != nil || math.IsNaN(bounded) || math.IsInf(bounded, 0) || bounded < 0 || bounded > float64(common.MaxQuota)/common.QuotaPerUnit {
		return errors.New("invalid upstream bill amount")
	}
	amount, err := decimal.NewFromString(actualUSD)
	if err != nil || amount.IsNegative() || amount.Exponent() < -100 || amount.Exponent() > 100 {
		return errors.New("invalid upstream bill amount")
	}
	quota := amount.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	if quota.GreaterThan(decimal.NewFromInt(common.MaxQuota)) {
		return errors.New("upstream bill amount exceeds quota bounds")
	}
	if rate != nil && (math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0 || *rate > 10000) {
		return errors.New("invalid upstream bill multiplier")
	}
	// Keep fractional quota: real small charges must not round down to free.
	cost := quota.InexactFloat64()
	now := common.GetTimestamp()
	var log Log
	if err := LOG_DB.WithContext(ctx).Where("id = ? AND request_id = ? AND channel_id = ?", pending.LogID, pending.LogRequestID, pending.ChannelID).Take(&log).Error; err != nil {
		return err
	}
	if !pending.matches(&log) {
		return errors.New("upstream bill source no longer matches")
	}
	account, err := GetSub2APIAccount(pending.AccountID)
	if err != nil {
		return err
	}
	if account.SharedIdentity == nil && account.ChannelID != pending.ChannelID || account.Origin == "" || account.UpstreamUserID <= 0 {
		return errors.New("invalid upstream bill account")
	}
	changed := false
	// HTTP Responses may be forwarded over WebSocket by Sub2API. Both IDs
	// are observed, never inferred; a matched supplier invoice has one owner.
	invoiceSource, _ := common.Marshal([]any{account.Origin, account.UpstreamUserID, usageID})
	invoiceHash := sha256.Sum256(invoiceSource)
	invoiceIdentity := hex.EncodeToString(invoiceHash[:])
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Sub2APIBill{}).Where("id = ? AND account_id = ? AND api_key_id = ? AND request_id = ? AND cost_quota IS NULL", pending.ID, pending.AccountID, apiKeyID, pending.RequestID).
			Updates(map[string]any{"usage_id": usageID, "invoice_identity": invoiceIdentity, "actual_cost_usd": amount.String(), "cost_quota": cost, "billing_type": billingType, "rate_multiplier": rate, "synced_at": now, "next_sync_at": now})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected > 0
		if !changed || log.Type != LogTypeError || cost == 0 {
			return nil
		}
		// A paid upstream attempt may fail locally. Give cost-only report
		// dimensions a zero-usage row, without increasing user fees or calls.
		var metadata struct {
			ClientTool string `json:"client_tool"`
			RootInfo   struct {
				NodeName string `json:"node_name"`
			} `json:"root_info"`
		}
		if common.UnmarshalJsonStr(log.Other, &metadata) != nil {
			return errors.New("invalid upstream bill source metadata")
		}
		return tx.Create(&QuotaData{UserID: log.UserId, Username: log.Username, ModelName: log.ModelName, CreatedAt: log.CreatedAt - log.CreatedAt%3600,
			UseGroup: log.Group, TokenID: log.TokenId, ChannelID: log.ChannelId, NodeName: metadata.RootInfo.NodeName, ClientTool: metadata.ClientTool}).Error
	})
	if err != nil {
		return err
	}
	if changed {
		usageLiveFeed.committed(&log)
	}
	// Use the persisted invoice on replay; a changed caller payload must never
	// replace an already reconciled supplier amount in routing statistics.
	var settled Sub2APIBill
	if err := DB.WithContext(ctx).First(&settled, pending.ID).Error; err != nil {
		common.SysError("Sub2API routing cost reconciliation deferred; supplier bill retained")
	} else if err := correctSub2APIKeyUsage(ctx, settled, &log); err != nil {
		if errors.Is(err, keyusage.ErrCostOutOfRange) {
			common.SysError("Sub2API routing cost projection out of range; supplier bill retained")
		} else {
			common.SysError("Sub2API routing cost reconciliation deferred; supplier bill retained")
		}
	}
	return nil
}

// correctSub2APIKeyUsage is idempotent and only runs after the invoice commits.
// NextSyncAt also retains failed routing projections of already settled bills.
func correctSub2APIKeyUsage(ctx context.Context, bill Sub2APIBill, log *Log) error {
	if bill.CostQuota == nil {
		return nil
	}
	if !bill.matches(log) {
		return errors.New("upstream routing bill source no longer matches")
	}
	var metadata struct {
		AdminInfo struct {
			Routing struct {
				ID          string `json:"id"`
				ChannelID   int    `json:"channel_id"`
				KeyIdentity string `json:"key_identity"`
			} `json:"key_routing"`
		} `json:"admin_info"`
	}
	if err := common.UnmarshalJsonStr(log.Other, &metadata); err != nil {
		return err
	}
	routing := metadata.AdminInfo.Routing
	if routing.ID != "" && routing.ChannelID == bill.ChannelID && routing.KeyIdentity == bill.KeyIdentity {
		actualUSD, err := strconv.ParseFloat(bill.ActualCostUSD, 64)
		if err != nil || math.IsNaN(actualUSD) || math.IsInf(actualUSD, 0) || actualUSD < 0 {
			return errors.New("invalid stored upstream bill amount")
		}
		if err := keyusage.CorrectCost(ctx, bill.ChannelID, routing.ID, actualUSD); err != nil {
			if errors.Is(err, keyusage.ErrCostOutOfRange) {
				// The durable invoice remains authoritative. Its amount cannot
				// fit the routing ledger, and retrying cannot change that limit.
				if clearErr := DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("id = ?", bill.ID).Update("next_sync_at", 0).Error; clearErr != nil {
					return clearErr
				}
				return err
			}
			if errors.Is(err, keyusage.ErrNotFound) {
				common.SysError("Sub2API routing history unavailable; supplier bill retained")
				return DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("id = ?", bill.ID).Update("next_sync_at", 0).Error
			}
			// A replay may have previously cleared this marker before Redis
			// became unavailable, so retain it again for the next sync pass.
			_ = DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("id = ?", bill.ID).Update("next_sync_at", common.GetTimestamp()).Error
			return err
		}
	}
	return DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("id = ? AND cost_quota IS NOT NULL", bill.ID).Update("next_sync_at", 0).Error
}

// RepairSub2APIKeyUsage retries a failed cache projection without fetching or
// changing the supplier invoice, including the transaction-to-cache crash gap.
func RepairSub2APIKeyUsage(ctx context.Context, channelID int) error {
	if !Sub2APIStorageReady() {
		return nil
	}
	var cursor int64
	var firstErr error
	for {
		var bills []Sub2APIBill
		query := DB.WithContext(ctx).Where("cost_quota IS NOT NULL AND next_sync_at > 0 AND created_at >= ? AND id > ?", time.Now().Add(-30*24*time.Hour).Unix(), cursor)
		if channelID > 0 {
			query = query.Where("channel_id = ?", channelID)
		}
		if err := query.Order("id").Limit(300).Find(&bills).Error; err != nil {
			return err
		}
		for _, bill := range bills {
			cursor = bill.ID
			var log Log
			if err := LOG_DB.WithContext(ctx).Where("id = ? AND request_id = ? AND channel_id = ?", bill.LogID, bill.LogRequestID, bill.ChannelID).Take(&log).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					err = DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("id = ?", bill.ID).Update("next_sync_at", 0).Error
				}
				if err != nil && firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := correctSub2APIKeyUsage(ctx, bill, &log); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if len(bills) < 300 {
			return firstErr
		}
	}
}

func (bill Sub2APIBill) matches(log *Log) bool {
	ref, ok := sub2APILogBillingRef(log)
	return ok && bill.LogID == log.Id && bill.ChannelID == log.ChannelId && bill.LogRequestID == log.RequestId && bill.CreatedAt == log.CreatedAt && bill.AccountID == ref.AccountID && bill.APIKeyID == ref.APIKeyID && bill.ResponseID == ref.ResponseID && bill.MatchID == sub2APIBillMatchID(log.ChannelId, ref)
}

func (bill Sub2APIBill) project(log *Log) bool {
	if bill.CostQuota == nil || !bill.matches(log) || math.IsNaN(*bill.CostQuota) || math.IsInf(*bill.CostQuota, 0) || *bill.CostQuota < 0 || *bill.CostQuota > common.MaxQuota {
		return false
	}
	var metadata map[string]any
	if common.UnmarshalJsonStr(log.Other, &metadata) != nil {
		return false
	}
	admin, ok := metadata["admin_info"].(map[string]any)
	if !ok {
		return false
	}
	var free int64
	if value, exists := metadata["free_quota"].(float64); exists {
		if value < 0 || value > float64(log.Quota) || value != math.Trunc(value) {
			return false
		}
		free = int64(value)
	}
	if log.Quota < 0 || log.Quota > common.MaxQuota {
		return false
	}
	revenue := float64(int64(log.Quota) - free)
	if prior, exists := admin["cost"]; exists {
		if snapshot, ok := prior.(map[string]any); ok && snapshot["source"] != "upstream_bill" {
			admin["cost_estimate"] = prior
		}
	}
	admin["cost"] = map[string]any{"source": "upstream_bill", "estimated": false, "cost_quota": *bill.CostQuota, "revenue_quota": revenue,
		"profit_quota": revenue - *bill.CostQuota, "free_quota": free, "actual_cost_usd": bill.ActualCostUSD, "billing_type": bill.BillingType,
		"rate_multiplier": bill.RateMultiplier, "synced_at": bill.SyncedAt, "upstream_usage_id": bill.UsageID}
	encoded, err := common.Marshal(metadata)
	if err != nil {
		return false
	}
	log.Other = string(encoded)
	return true
}

// AttachSub2APIBills is for administrator projections only. It runs before
// display-only IDs are assigned, and self APIs never receive either snapshot.
func AttachSub2APIBills(ctx context.Context, logs []*Log) error {
	if !Sub2APIStorageReady() {
		return nil
	}
	byMatch := make(map[string][]*Log)
	ids := make([]string, 0, len(logs))
	for _, log := range logs {
		if ref, ok := sub2APILogBillingRef(log); ok {
			id := sub2APIBillMatchID(log.ChannelId, ref)
			byMatch[id] = append(byMatch[id], log)
			ids = append(ids, id)
		}
	}
	for batch := range slices.Chunk(ids, 300) {
		var bills []Sub2APIBill
		if err := DB.WithContext(ctx).Where("match_id IN ? AND cost_quota IS NOT NULL", batch).Find(&bills).Error; err != nil {
			return err
		}
		for _, bill := range bills {
			for _, log := range byMatch[bill.MatchID] {
				bill.project(log)
			}
		}
	}
	return nil
}

// Visit exact source rows with settled overlays, in bounded batches. The log
// filter still runs in its own database; no cross-database SQL join is assumed.
func visitSub2APIBills(ctx context.Context, query *gorm.DB, start, end int64, visit func(*Log, Sub2APIBill) error) error {
	if !Sub2APIStorageReady() {
		return nil
	}
	billsQuery := DB.WithContext(ctx).Where("cost_quota IS NOT NULL")
	if start > 0 {
		billsQuery = billsQuery.Where("created_at >= ?", start)
	}
	if end > 0 {
		billsQuery = billsQuery.Where("created_at <= ?", end)
	}
	var cursor int64
	for {
		var bills []Sub2APIBill
		if err := billsQuery.Session(&gorm.Session{}).Where("id > ?", cursor).Order("id").Limit(300).Find(&bills).Error; err != nil {
			return err
		}
		if len(bills) == 0 {
			return nil
		}
		ids := make([]int, 0, len(bills))
		byLog := make(map[int]Sub2APIBill, len(bills))
		for _, bill := range bills {
			ids = append(ids, bill.LogID)
			byLog[bill.LogID] = bill
		}
		var logs []*Log
		if err := query.Session(&gorm.Session{}).Select("logs.*").Where("logs.id IN ?", ids).Find(&logs).Error; err != nil {
			return err
		}
		for _, log := range logs {
			if bill := byLog[log.Id]; bill.matches(log) {
				if err := visit(log, bill); err != nil {
					return err
				}
			}
		}
		cursor = bills[len(bills)-1].ID
	}
}

func sub2APIBillFinanceDelta(log *Log, bill Sub2APIBill) DashboardFinance {
	before, _ := recordedLogFinanceJSON(log.Quota, log.Other)
	copy := *log
	if !bill.project(&copy) {
		return DashboardFinance{}
	}
	after, valid := recordedLogFinanceJSON(log.Quota, copy.Other)
	if !valid {
		return DashboardFinance{}
	}
	if log.Type == LogTypeError {
		return DashboardFinance{CostQuota: after.CostQuota, ProfitQuota: -after.CostQuota}
	}
	return DashboardFinance{CostQuota: after.CostQuota - before.CostQuota, ProfitQuota: after.ProfitQuota - before.ProfitQuota,
		CoveredRevenueQuota: after.CoveredRevenueQuota - before.CoveredRevenueQuota, RecordedCount: after.RecordedCount - before.RecordedCount,
		ActualCount: after.ActualCount - before.ActualCount, EstimatedCount: after.EstimatedCount - before.EstimatedCount}
}

func applySub2APIBillStat(ctx context.Context, query *gorm.DB, stat *Stat, start, end int64) error {
	if stat.Finance == nil {
		return nil
	}
	return visitSub2APIBills(ctx, query, start, end, func(log *Log, bill Sub2APIBill) error {
		delta := sub2APIBillFinanceDelta(log, bill)
		stat.Finance.add(delta)
		stat.Finance.UnrecordedCount -= delta.RecordedCount
		return nil
	})
}

func applySub2APIDashboardBills(ctx context.Context, costs map[dashboardFinanceKey]DashboardFinance, start, end int64, username string) error {
	query := applyLogSourceFilter(LOG_DB.WithContext(ctx).Model(&Log{})).Where("type IN ? AND created_at BETWEEN ? AND ?", []int{LogTypeConsume, LogTypeError}, start, end)
	if username != "" {
		query = query.Where("username = ?", username)
	}
	return visitSub2APIBills(ctx, query, start, end, func(log *Log, bill Sub2APIBill) error {
		// Reuse the existing report dimensions (including node-known and source).
		original := make(map[dashboardFinanceKey]DashboardFinance)
		projectDashboardLogFinance(log, original)
		copy := *log
		if !bill.project(&copy) {
			return nil
		}
		updated := make(map[dashboardFinanceKey]DashboardFinance)
		projectDashboardLogFinance(&copy, updated)
		for key, after := range updated {
			if log.Type == LogTypeError {
				sum := costs[key]
				sum.add(DashboardFinance{CostQuota: after.CostQuota})
				costs[key] = sum
				continue
			}
			before := original[key]
			delta := DashboardFinance{CostQuota: after.CostQuota - before.CostQuota, CoveredRevenueQuota: after.CoveredRevenueQuota - before.CoveredRevenueQuota,
				RecordedCount: after.RecordedCount - before.RecordedCount, ActualCount: after.ActualCount - before.ActualCount, EstimatedCount: after.EstimatedCount - before.EstimatedCount}
			sum := costs[key]
			sum.add(delta)
			costs[key] = sum
		}
		return nil
	})
}

// Charge-bearing failed attempts remain separately visible. A zero user fee
// must not hide an actual paid supplier attempt inside an unbilled retry.
func sub2APIPaidFailureIDs(ctx context.Context, ids []int) (map[int]bool, error) {
	result := make(map[int]bool)
	if !Sub2APIStorageReady() {
		return result, nil
	}
	for batch := range slices.Chunk(ids, 300) {
		var paid []int
		if err := DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("log_id IN ? AND cost_quota > 0", batch).Pluck("log_id", &paid).Error; err != nil {
			return nil, err
		}
		for _, id := range paid {
			result[id] = true
		}
	}
	return result, nil
}

// Pending work is retried with backoff even if the upstream has not published
// the usage record yet. A revoked account never clears the recorded estimate.
func PendingSub2APIBills(ctx context.Context, channelID, limit int) ([]Sub2APIBill, error) {
	var rows []Sub2APIBill
	if !Sub2APIStorageReady() {
		return rows, nil
	}
	query := DB.WithContext(ctx).Where("cost_quota IS NULL")
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	} else {
		query = query.Where("next_sync_at <= ?", common.GetTimestamp())
	}
	err := query.Order("next_sync_at,id").Limit(limit).Find(&rows).Error
	return rows, err
}

func DeferSub2APIBills(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return DB.WithContext(ctx).Model(&Sub2APIBill{}).Where("id IN ? AND cost_quota IS NULL", ids).Update("next_sync_at", time.Now().Add(5*time.Minute).Unix()).Error
}

// Recover the narrow log->queue crash window without rewriting source logs.
func RepairSub2APIBillQueue(ctx context.Context, since int64, channelIDs []int) error {
	if !Sub2APIStorageReady() || len(channelIDs) == 0 {
		return nil
	}
	var cursor int
	for {
		var logs []*Log
		err := LOG_DB.WithContext(ctx).Where("channel_id IN ? AND created_at >= ? AND id > ? AND type IN ? AND other LIKE ?", channelIDs, since, cursor, []int{LogTypeConsume, LogTypeError}, `%"sub2api_billing"%`).
			Select("id,request_id,channel_id,created_at,type,other").Order("id").Limit(300).Find(&logs).Error
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			return nil
		}
		for _, log := range logs {
			if err := QueueSub2APIBill(ctx, log); err != nil {
				return err
			}
		}
		cursor = logs[len(logs)-1].Id
	}
}
