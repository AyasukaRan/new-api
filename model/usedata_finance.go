package model

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// DashboardFinance contains only recorded costs. Unrecorded calls never count
// as zero-cost sales, and profit uses only the revenue of covered calls.
type DashboardFinance struct {
	CostQuota           float64 `json:"cost_quota"`
	ProfitQuota         float64 `json:"profit_quota"`
	CoveredRevenueQuota float64 `json:"covered_revenue_quota"`
	RecordedCount       int64   `json:"recorded_count"`
	ActualCount         int64   `json:"actual_count"`
	EstimatedCount      int64   `json:"estimated_count"`
	UnrecordedCount     int64   `json:"unrecorded_count"`
}

func (f *DashboardFinance) add(other DashboardFinance) {
	f.CostQuota += other.CostQuota
	f.CoveredRevenueQuota += other.CoveredRevenueQuota
	f.ProfitQuota = f.CoveredRevenueQuota - f.CostQuota
	f.RecordedCount += other.RecordedCount
	f.ActualCount += other.ActualCount
	f.EstimatedCount += other.EstimatedCount
}

type dashboardFinanceKey struct {
	CreatedAt  int64
	UserID     int
	Username   string
	ModelName  string
	ClientTool string
	UseGroup   string
	TokenID    int
	ChannelID  int
	NodeName   string
	NodeKnown  bool
}

type dashboardCostSnapshot struct {
	CostRatio    *float64 `json:"cost_ratio"`
	BaseQuota    *float64 `json:"base_quota"`
	CostQuota    *float64 `json:"cost_quota"`
	RevenueQuota *float64 `json:"revenue_quota"`
	ProfitQuota  *float64 `json:"profit_quota"`
	Estimated    *bool    `json:"estimated"`
	Source       string   `json:"source"`
}

// recordedLogFinance validates a stored cost against the fee actually recorded
// on that request. Reports never infer missing costs from today's prices.
func recordedLogFinance(quota int, cost *dashboardCostSnapshot) (DashboardFinance, bool) {
	if cost == nil || cost.Estimated == nil || (!*cost.Estimated && cost.Source != "upstream_bill") {
		return DashboardFinance{}, false
	}
	for _, amount := range []*float64{cost.CostRatio, cost.BaseQuota, cost.CostQuota, cost.RevenueQuota} {
		if amount == nil || math.IsNaN(*amount) || math.IsInf(*amount, 0) || *amount < 0 || *amount > common.MaxQuota {
			return DashboardFinance{}, false
		}
	}
	if *cost.CostRatio > 10000 || quota < 0 || quota > common.MaxQuota || *cost.RevenueQuota != float64(quota) {
		return DashboardFinance{}, false
	}
	profit := float64(quota) - *cost.CostQuota
	if cost.ProfitQuota == nil || math.IsNaN(*cost.ProfitQuota) || math.IsInf(*cost.ProfitQuota, 0) || math.Abs(*cost.ProfitQuota-profit) > 0.000001 {
		return DashboardFinance{}, false
	}
	finance := DashboardFinance{CostQuota: *cost.CostQuota, ProfitQuota: profit, CoveredRevenueQuota: float64(quota), RecordedCount: 1}
	if *cost.Estimated {
		finance.EstimatedCount = 1
	} else {
		finance.ActualCount = 1
	}
	return finance, true
}

func recordedLogFinanceJSON(quota int, raw string) (DashboardFinance, bool) {
	var metadata struct {
		AdminInfo struct {
			Cost *dashboardCostSnapshot `json:"cost"`
		} `json:"admin_info"`
	}
	if common.UnmarshalJsonStr(raw, &metadata) != nil {
		return DashboardFinance{}, false
	}
	return recordedLogFinance(quota, metadata.AdminInfo.Cost)
}

var dashboardFinanceCache = hot.NewHotCache[string, map[dashboardFinanceKey]DashboardFinance](hot.LRU, 8).
	WithTTL(15 * time.Second).Build()
var dashboardFinanceLoads singleflight.Group

func getDashboardFinance(ctx context.Context, startTime, endTime int64, username string) (map[dashboardFinanceKey]DashboardFinance, error) {
	if startTime < 0 || endTime < startTime || endTime > math.MaxInt64-3599 {
		return nil, fmt.Errorf("invalid time range")
	}
	// quota_data stores hourly bucket starts, including the entire end bucket.
	startTime = ((startTime + 3599) / 3600) * 3600
	endTime -= endTime % 3600
	if startTime > endTime {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if DashboardFinanceRollupEnabled() {
		return loadDashboardFinanceHours(ctx, startTime, endTime, username)
	}

	endTime += 3599
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return scanDashboardFinance(ctx, startTime, endTime, username)
	}

	// ClickHouse keeps its append-only log schema and uses a shared short-lived result.
	cacheKey := fmt.Sprintf("%p:%d:%d:%s", LOG_DB, startTime, endTime, username)
	if cached, found, _ := dashboardFinanceCache.Get(cacheKey); found {
		return cached, nil
	}
	result, err, _ := dashboardFinanceLoads.Do(cacheKey, func() (any, error) {
		if cached, found, _ := dashboardFinanceCache.Get(cacheKey); found {
			return cached, nil
		}
		costs, err := scanDashboardFinance(ctx, startTime, endTime, username)
		if err != nil {
			return nil, err
		}
		// Large reports are still accurate, but should not occupy all cache memory.
		if len(costs) <= 25000 {
			dashboardFinanceCache.Set(cacheKey, costs)
		}
		return costs, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(map[dashboardFinanceKey]DashboardFinance), nil
}

// Scan the authoritative logs when the database cannot maintain projections.
func scanDashboardFinance(ctx context.Context, startTime, endTime int64, username string) (map[dashboardFinanceKey]DashboardFinance, error) {
	costs := make(map[dashboardFinanceKey]DashboardFinance)
	query := applyLogSourceFilter(LOG_DB.WithContext(ctx).Model(&Log{})).
		Select([]string{"created_at", "user_id", "username", "model_name", "channel_id", "token_id", "group", "quota", "other"}).
		Where("type = ? AND created_at >= ? AND created_at <= ?", LogTypeConsume, startTime, endTime).
		Where("other LIKE ?", `%"cost"%`)
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := streamLogs(query, "logs.created_at ASC, logs.id ASC", func(log *Log) error {
		projectDashboardLogFinance(log, costs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return costs, nil
}

// logFinanceStat preserves the exact log filters, including partial hours. A single
// statement sees both indexed and pending logs in one snapshot. Only pending
// rows need their original JSON, including while an earlier hour is being repaired.
func logFinanceStat(tx *gorm.DB) (Stat, error) {
	const pendingColumns = "1 AS calls, COALESCE(logs.quota,0) AS quota, 0 AS cost, 0 AS revenue, 0 AS recorded, 0 AS estimated, "
	var query *gorm.DB
	if !DashboardFinanceRollupEnabled() {
		query = tx.Session(&gorm.Session{}).Select(pendingColumns+"CASE WHEN logs.other LIKE ? THEN logs.other ELSE '' END AS raw", `%"cost"%`)
	} else {
		ready := tx.Session(&gorm.Session{}).
			Joins("JOIN dashboard_finance_entries f ON f.log_id = logs.id").
			Select(`COUNT(*) AS calls, COALESCE(SUM(logs.quota),0) AS quota,
				COALESCE(SUM(f.cost_quota),0) AS cost,
				COALESCE(SUM(CASE WHEN f.cost_quota IS NOT NULL THEN logs.quota ELSE 0 END),0) AS revenue,
				COUNT(f.cost_quota) AS recorded,
				COUNT(CASE WHEN f.cost_quota IS NOT NULL AND f.estimated THEN 1 END) AS estimated, NULL AS raw`)
		pending := tx.Session(&gorm.Session{}).Select(pendingColumns + "logs.other AS raw").
			Where("NOT EXISTS (SELECT 1 FROM dashboard_finance_entries f WHERE f.log_id = logs.id)")
		query = tx.Session(&gorm.Session{NewDB: true}).Raw("? UNION ALL ?", ready, pending)
	}
	rows, err := query.Rows()
	if err != nil {
		return Stat{}, err
	}
	defer rows.Close()
	stat := Stat{Finance: &DashboardFinance{}}
	var calls int64
	for rows.Next() {
		var n int64
		var quota int
		var raw sql.NullString
		var finance DashboardFinance
		if err := rows.Scan(&n, &quota, &finance.CostQuota, &finance.CoveredRevenueQuota, &finance.RecordedCount, &finance.EstimatedCount, &raw); err != nil {
			return Stat{}, err
		}
		if (quota > 0 && stat.Quota > math.MaxInt-quota) || (quota < 0 && stat.Quota < math.MinInt-quota) {
			return Stat{}, common.NewMessage("Failed to query statistics")
		}
		if raw.Valid {
			finance, _ = recordedLogFinanceJSON(quota, raw.String)
		}
		finance.ActualCount = finance.RecordedCount - finance.EstimatedCount
		stat.Finance.add(finance)
		stat.Quota += quota
		calls += n
	}
	if err := rows.Err(); err != nil {
		return Stat{}, err
	}
	stat.Finance.UnrecordedCount = calls - stat.Finance.RecordedCount
	return stat, nil
}

// AttachQuotaFinance is used only by administrator model/user report handlers.
func AttachQuotaFinance(ctx context.Context, rows []*QuotaData, startTime, endTime int64, username string, byUser bool) error {
	costs, err := getDashboardFinance(ctx, startTime, endTime, username)
	if err != nil {
		return err
	}
	grouped := make(map[dashboardFinanceKey]DashboardFinance)
	for key, finance := range costs {
		groupKey := dashboardFinanceKey{CreatedAt: key.CreatedAt}
		if byUser {
			groupKey.Username = key.Username
		} else {
			groupKey.ModelName = key.ModelName
			if username != "" {
				groupKey.UserID = key.UserID
			}
		}
		group := grouped[groupKey]
		group.add(finance)
		grouped[groupKey] = group
	}
	for _, row := range rows {
		key := dashboardFinanceKey{CreatedAt: row.CreatedAt}
		if byUser {
			key.Username = row.Username
		} else {
			key.ModelName = row.ModelName
			if username != "" {
				key.UserID = row.UserID
			}
		}
		finance := grouped[key]
		finance.UnrecordedCount = max(0, int64(row.Count)-finance.RecordedCount)
		row.Finance = &finance
	}
	return nil
}

func AttachSourceFinance(ctx context.Context, rows []SourceQuotaData, startTime, endTime int64, username string, timeSeries bool) error {
	costs, err := getDashboardFinance(ctx, startTime, endTime, username)
	if err != nil {
		return err
	}
	grouped := make(map[dashboardFinanceKey]DashboardFinance)
	for key, finance := range costs {
		groupKey := dashboardFinanceKey{ClientTool: key.ClientTool}
		if timeSeries {
			groupKey.CreatedAt = key.CreatedAt
		}
		group := grouped[groupKey]
		group.add(finance)
		grouped[groupKey] = group
	}
	for i := range rows {
		finance := grouped[dashboardFinanceKey{CreatedAt: rows[i].CreatedAt, ClientTool: rows[i].ClientTool}]
		finance.UnrecordedCount = max(0, rows[i].Count-finance.RecordedCount)
		rows[i].Finance = &finance
	}
	return nil
}

func AttachFlowFinance(ctx context.Context, rows []*FlowQuotaData, startTime, endTime int64, username string, role int) error {
	costs, err := getDashboardFinance(ctx, startTime, endTime, username)
	if err != nil {
		return err
	}
	byRoute := make(map[dashboardFinanceKey][]*FlowQuotaData)
	for _, row := range rows {
		row.Finance = &DashboardFinance{UnrecordedCount: int64(row.Count)}
		key := dashboardFinanceKey{UserID: row.UserID, Username: row.Username, ModelName: row.ModelName, UseGroup: row.UseGroup, ChannelID: row.ChannelID}
		if role >= common.RoleRootUser {
			key.TokenID = row.TokenID
		}
		byRoute[key] = append(byRoute[key], row)
	}
	for key, finance := range costs {
		route := dashboardFinanceKey{UserID: key.UserID, Username: key.Username, ModelName: key.ModelName, UseGroup: key.UseGroup, ChannelID: key.ChannelID}
		if role >= common.RoleRootUser {
			route.TokenID = key.TokenID
		}
		matches := byRoute[route]
		for _, row := range matches {
			// Legacy logs have no node identity. Assign them only if the route
			// has one node, rather than inventing an allocation or double-counting.
			if role >= common.RoleRootUser && ((!key.NodeKnown && len(matches) != 1) || (key.NodeKnown && key.NodeName != row.NodeName)) {
				continue
			}
			row.Finance.add(finance)
			row.Finance.UnrecordedCount = max(0, int64(row.Count)-row.Finance.RecordedCount)
		}
	}
	return nil
}
