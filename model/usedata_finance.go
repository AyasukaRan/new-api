package model

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
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

var dashboardFinanceCache = hot.NewHotCache[string, map[dashboardFinanceKey]DashboardFinance](hot.LRU, 8).
	WithTTL(15 * time.Second).Build()
var dashboardFinanceLoads singleflight.Group

// getDashboardFinance reads only candidate snapshots, in keyset batches. The
// bounded short-lived cache shares one scan between the dashboard's charts.
// Parsing in Go also handles legacy invalid JSON without database-specific casts.
func getDashboardFinance(ctx context.Context, startTime, endTime int64, username string) (map[dashboardFinanceKey]DashboardFinance, error) {
	if startTime < 0 || endTime < startTime || endTime > math.MaxInt64-3599 {
		return nil, fmt.Errorf("invalid time range")
	}
	// quota_data stores hourly bucket starts, including the entire end bucket.
	startTime = ((startTime + 3599) / 3600) * 3600
	endTime = (endTime/3600)*3600 + 3599
	cacheKey := fmt.Sprintf("%p:%d:%d:%s", LOG_DB, startTime, endTime, username)
	if cached, found, _ := dashboardFinanceCache.Get(cacheKey); found {
		return cached, nil
	}
	result, err, _ := dashboardFinanceLoads.Do(cacheKey, func() (any, error) {
		if cached, found, _ := dashboardFinanceCache.Get(cacheKey); found {
			return cached, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		costs := make(map[dashboardFinanceKey]DashboardFinance)
		var lastTime int64
		var lastID int
		var lastRequestID string
		for {
			query := applyLogSourceFilter(LOG_DB.WithContext(ctx).Model(&Log{})).
				Select([]string{"id", "created_at", "user_id", "username", "model_name", "channel_id", "token_id", "group", "quota", "request_id", "other"}).
				Where("type = ? AND created_at >= ? AND created_at <= ?", LogTypeConsume, startTime, endTime).
				Where("other LIKE ?", `%"cost"%`)
			if username != "" {
				query = query.Where("username = ?", username)
			}
			if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
				if lastTime > 0 {
					query = query.Where("created_at > ? OR (created_at = ? AND request_id > ?)", lastTime, lastTime, lastRequestID)
				}
				query = query.Order("created_at ASC, request_id ASC")
			} else {
				if lastTime > 0 {
					query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", lastTime, lastTime, lastID)
				}
				query = query.Order("created_at ASC, id ASC")
			}
			var logs []Log
			if err := query.Limit(128).Find(&logs).Error; err != nil {
				return nil, err
			}
			for _, log := range logs {
				var metadata struct {
					ClientTool string `json:"client_tool"`
					AdminInfo  struct {
						Cost *dashboardCostSnapshot `json:"cost"`
					} `json:"admin_info"`
					RootInfo struct {
						NodeName *string `json:"node_name"`
					} `json:"root_info"`
				}
				if common.UnmarshalJsonStr(log.Other, &metadata) != nil {
					continue
				}
				recorded, valid := recordedLogFinance(log.Quota, metadata.AdminInfo.Cost)
				if !valid {
					continue
				}
				other := NewLogOther()
				other.SetPublic("client_tool", metadata.ClientTool)
				key := dashboardFinanceKey{CreatedAt: log.CreatedAt - log.CreatedAt%3600, UserID: log.UserId, Username: log.Username, ModelName: log.ModelName, ClientTool: other.ClientTool(), UseGroup: log.Group, TokenID: log.TokenId, ChannelID: log.ChannelId, NodeKnown: metadata.RootInfo.NodeName != nil}
				if key.NodeKnown {
					key.NodeName = *metadata.RootInfo.NodeName
				}
				finance := costs[key]
				finance.add(recorded)
				costs[key] = finance
			}
			if len(logs) < 128 {
				break
			}
			last := logs[len(logs)-1]
			lastTime, lastID, lastRequestID = last.CreatedAt, last.Id, last.RequestId
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
