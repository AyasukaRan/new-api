package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Both projections live beside their source logs, even with a separate log DB.
// A NULL cost distinguishes a checked, invalid/missing snapshot from a free call.
type DashboardFinanceEntry struct {
	LogID     int `gorm:"primaryKey;autoIncrement:false"`
	CostQuota *float64
	FreeQuota *int64
	Estimated bool
}

type DashboardFinanceHour struct {
	Hour     int64 `gorm:"primaryKey;autoIncrement:false"`
	Revision int64
	LogCount int64
	LastID   int
	Payload  []byte
}

type dashboardFinanceCell struct {
	Key     dashboardFinanceKey
	Finance DashboardFinance
}

type financeHourSource struct {
	DashboardFinanceHour `gorm:"embedded"`
	SourceCount          int64
	SourceMax            int
}

var financeHourLoads singleflight.Group
var errFinanceHourChanged = errors.New("finance hour changed while updating")

// A count in addition to the watermark detects a transaction that allocated its
// ID before a higher ID but only finished after the higher ID was aggregated.
// This query reads no request payloads. Its signature and saved projection are
// from the same database statement, including when old logs were edited.
func financeHoursInRange(db *gorm.DB, start, end int64) ([]financeHourSource, error) {
	if start < 0 || end < start || start%3600 != 0 || end%3600 != 0 || end > math.MaxInt64-3599 {
		return nil, fmt.Errorf("invalid finance hour range")
	}
	var rows []financeHourSource
	hourExpr := "created_at - (created_at % 3600)"
	source := db.Table("logs").Select(hourExpr+" AS bucket, COUNT(*) AS source_count, MAX(id) AS source_max").
		Where("type = ? AND created_at BETWEEN ? AND ?", LogTypeConsume, start, end+3599).Group(hourExpr)
	err := db.Table("(?) AS src", source).
		Select("src.bucket AS hour, src.source_count, src.source_max, h.revision, h.log_count, h.last_id, h.payload").
		Joins("LEFT JOIN dashboard_finance_hours AS h ON h.hour = src.bucket").Order("src.bucket").
		Scan(&rows).Error
	return rows, err
}

func (row financeHourSource) complete() bool {
	return len(row.Payload) > 0 && row.LogCount == row.SourceCount && row.LastID == row.SourceMax
}

func ensureDashboardFinanceHours(ctx context.Context, start, end int64) error {
	rows, err := financeHoursInRange(LOG_DB.WithContext(ctx), start, end)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.complete() {
			continue
		}
		key := fmt.Sprintf("%p:%d:%d:%d:%d", LOG_DB, row.Hour, row.SourceCount, row.SourceMax, row.Revision)
		for {
			loaded := financeHourLoads.DoChan(key, func() (any, error) {
				return nil, refreshDashboardFinanceHour(ctx, row.Hour)
			})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case r := <-loaded:
				// A different reader may have canceled the shared refresh. A
				// waiter with a live context can finish it from the last batch.
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if r.Shared && (errors.Is(r.Err, context.Canceled) || errors.Is(r.Err, context.DeadlineExceeded)) {
					continue
				}
				if r.Err != nil {
					return r.Err
				}
			}
			break
		}
	}
	return nil
}

func loadDashboardFinanceHours(ctx context.Context, start, end int64, username string) (map[dashboardFinanceKey]DashboardFinance, error) {
	// A secondary can read the published facts and pending logs without
	// acquiring write access to the log database.
	if dashboardFinanceWriter.Load() == LOG_DB {
		if err := ensureDashboardFinanceHours(ctx, start, end); err != nil {
			return nil, err
		}
	}
	result := make(map[dashboardFinanceKey]DashboardFinance)
	// Freeze all hours and any tail reads together. A concurrent edit can move
	// a bill across hours while the refresh above is in progress.
	opts := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if common.UsingLogDatabase(common.DatabaseTypeSQLite) {
		opts.Isolation = sql.LevelSerializable
	}
	err := LOG_DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := financeHoursInRange(tx, start, end)
		if err != nil {
			return err
		}
		var matching map[string]bool
		if username != "" {
			// Match using the log column's collation. Distinguish its actual
			// spellings when looking up keys in the stored JSON afterwards.
			label := "username COLLATE BINARY"
			switch common.LogDatabaseType() {
			case common.DatabaseTypeMySQL:
				label = "BINARY username"
			case common.DatabaseTypePostgreSQL:
				label = `username COLLATE "C"`
			}
			var names []string
			if err := tx.Table("logs").Select("DISTINCT "+label+" AS name").
				Where("username = ? AND type = ? AND created_at BETWEEN ? AND ?", username, LogTypeConsume, start, end+3599).Scan(&names).Error; err != nil {
				return err
			}
			matching = make(map[string]bool, len(names))
			for _, name := range names {
				matching[name] = true
			}
		}
		for _, row := range rows {
			if !row.complete() {
				// Normally just the new tail arrived since the refresh. If a
				// lower ID committed late, rebuild this hour in this snapshot.
				logs := tx.Model(&Log{}).Where("type = ? AND created_at BETWEEN ? AND ?", LogTypeConsume, row.Hour, row.Hour+3599)
				var prefix int64
				if len(row.Payload) != 0 {
					if err := logs.Session(&gorm.Session{}).Where("id <= ?", row.LastID).Count(&prefix).Error; err != nil {
						return err
					}
				}
				if len(row.Payload) == 0 || prefix != row.LogCount {
					row.LastID, row.Payload = 0, nil
				}
				tail := applyLogSourceFilter(logs.Where("id > ?", row.LastID)).
					Select([]string{"created_at", "user_id", "username", "model_name", "channel_id", "token_id", "group", "quota", "other"})
				if username != "" {
					tail = tail.Where("username = ?", username)
				}
				if err := streamLogs(tail, "logs.id", func(log *Log) error { projectDashboardLogFinance(log, result); return nil }); err != nil {
					return err
				}
			}
			if len(row.Payload) == 0 {
				continue
			}
			var cells []dashboardFinanceCell
			if err := common.Unmarshal(row.Payload, &cells); err != nil {
				return err
			}
			for _, cell := range cells {
				if matching != nil && !matching[cell.Key.Username] {
					continue
				}
				sum := result[cell.Key]
				sum.add(cell.Finance)
				result[cell.Key] = sum
			}
		}
		return nil
	}, opts)
	return result, err
}

func projectDashboardLogFinance(log *Log, costs map[dashboardFinanceKey]DashboardFinance) DashboardFinanceEntry {
	entry := DashboardFinanceEntry{LogID: log.Id}
	var metadata struct {
		FreeQuota int64 `json:"free_quota"`
		AdminInfo struct {
			Cost json.RawMessage `json:"cost"`
		} `json:"admin_info"`
		ClientTool string `json:"client_tool"`
		RootInfo   struct {
			NodeName *string `json:"node_name"`
		} `json:"root_info"`
	}
	if common.UnmarshalJsonStr(log.Other, &metadata) != nil {
		finance, valid := recordedLogFinanceJSON(log.Quota, log.Other)
		entry.FreeQuota = &finance.FreeQuota
		if valid {
			entry.CostQuota = &finance.CostQuota
			entry.Estimated = finance.EstimatedCount == 1
		}
		return entry
	}
	var cost *dashboardCostSnapshot
	if len(metadata.AdminInfo.Cost) > 0 && common.Unmarshal(metadata.AdminInfo.Cost, &cost) != nil {
		cost = nil
	}
	finance, valid := recordedLogFinance(log.Quota, cost, metadata.FreeQuota)
	entry.FreeQuota = &finance.FreeQuota
	if !valid && finance.FreeQuota == 0 {
		return entry
	}
	if valid {
		entry.CostQuota = cost.CostQuota
		entry.Estimated = *cost.Estimated
	}
	other := NewLogOther()
	other.SetPublic("client_tool", metadata.ClientTool)
	key := dashboardFinanceKey{CreatedAt: log.CreatedAt - log.CreatedAt%3600, UserID: log.UserId, Username: log.Username, ModelName: log.ModelName, ClientTool: other.ClientTool(), UseGroup: log.Group, TokenID: log.TokenId, ChannelID: log.ChannelId, NodeKnown: metadata.RootInfo.NodeName != nil}
	if key.NodeKnown {
		key.NodeName = *metadata.RootInfo.NodeName
	}
	sum := costs[key]
	sum.add(finance)
	costs[key] = sum
	return entry
}

// Source reads and JSON decoding happen outside the write transaction. Each
// bounded batch atomically advances the hour revision and publishes its entries.
// Edits or competing workers change the revision, so a stale batch cannot win.
func refreshDashboardFinanceHour(ctx context.Context, hour int64) error {
	db := LOG_DB.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&DashboardFinanceHour{Hour: hour}).Error; err != nil {
		return err
	}
	for {
		rows, err := financeHoursInRange(db, hour, hour)
		if err != nil || len(rows) == 0 {
			return err
		}
		state := rows[0]
		if state.complete() {
			return nil
		}
		var cells []dashboardFinanceCell
		if len(state.Payload) != 0 {
			if err := common.Unmarshal(state.Payload, &cells); err != nil {
				return err
			}
		}
		if state.LogCount < 0 {
			state.LogCount = 0
		}
		costs := make(map[dashboardFinanceKey]DashboardFinance, len(cells))
		for _, cell := range cells {
			costs[cell.Key] = cell.Finance
		}
		for {
			// Complete the fixed snapshot rather than chasing a live stream forever.
			type input struct {
				Log        `gorm:"embedded"`
				LegacyTest bool
			}
			var batch []input
			err := db.Table("logs").
				Select("id, user_id, username, model_name, channel_id, token_id, "+db.Statement.Quote("group")+", quota, other, CASE WHEN COALESCE(token_id, 0) = 0 AND token_name = ? AND content = ? THEN 1 ELSE 0 END AS legacy_test", "模型测试", "模型测试").
				Where("type = ? AND created_at BETWEEN ? AND ? AND id > ? AND id <= ?", LogTypeConsume, hour, hour+3599, state.LastID, state.SourceMax).
				Order("id").Limit(256).Scan(&batch).Error
			if err != nil {
				return err
			}
			entries := make([]DashboardFinanceEntry, 0, len(batch))
			for _, log := range batch {
				state.LastID = log.Id
				state.LogCount++
				entry := DashboardFinanceEntry{LogID: log.Id}
				if !log.LegacyTest {
					log.CreatedAt = hour
					entry = projectDashboardLogFinance(&log.Log, costs)
				}
				entries = append(entries, entry)
			}

			done := len(batch) == 0 || state.LastID >= state.SourceMax
			reset := done && (state.LogCount != state.SourceCount || state.LastID != state.SourceMax)
			cells = cells[:0]
			if !reset {
				for k, v := range costs {
					cells = append(cells, dashboardFinanceCell{k, v})
				}
			}
			payload, err := common.Marshal(cells)
			if err != nil {
				return err
			}
			if reset {
				// A lower ID became visible after a previous batch. Rebuild this
				// hour only; Never advance past a row that wasn't accounted for.
				state.LastID, state.LogCount, payload = 0, -1, nil
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				updated := tx.Model(&DashboardFinanceHour{}).Where("hour = ? AND revision = ?", hour, state.Revision).
					Updates(map[string]any{"revision": state.Revision + 1, "log_count": state.LogCount, "last_id": state.LastID, "payload": payload})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return errFinanceHourChanged
				}
				if len(entries) == 0 || reset {
					return nil
				}
				return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "log_id"}}, DoUpdates: clause.AssignmentColumns([]string{"cost_quota", "free_quota", "estimated"})}).Create(&entries).Error
			})
			if errors.Is(err, errFinanceHourChanged) {
				break
			}
			if err != nil {
				return err
			}
			state.Revision++
			if reset {
				break
			}
			if done {
				return nil
			}
		}
	}
}

// Revisit the live hour, and backfill historical hours newest first at a
// bounded pace. Reports can prepare a requested hour before this worker does.
func WarmDashboardFinanceHours(ctx context.Context) {
	var before int64
	var idleUntil time.Time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastLive int64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			hour := now.Unix() / 3600 * 3600
			if now.Unix()-lastLive >= 15 {
				lastLive = now.Unix()
				if err := ensureDashboardFinanceHours(ctx, hour, hour); err != nil && ctx.Err() == nil {
					common.SysError(common.LogText("failed to warm finance hour: %s", err.Error()))
				}
			}
			if now.Before(idleUntil) {
				continue
			}
			if before == 0 {
				before = hour
			}
			var timestamps []int64
			err := LOG_DB.WithContext(ctx).Table("logs").Where("type = ? AND created_at < ?", LogTypeConsume, before).Order("created_at DESC").Limit(1).Pluck("created_at", &timestamps).Error
			if err == nil && len(timestamps) > 0 {
				h := timestamps[0] / 3600 * 3600
				err = ensureDashboardFinanceHours(ctx, h, h)
				if err == nil {
					before = h
				}
			} else if err == nil {
				idleUntil = now.Truncate(time.Hour).Add(time.Hour)
				before = 0
			}
			if err != nil && ctx.Err() == nil {
				common.SysError(common.LogText("failed to backfill finance hour: %s", err.Error()))
				idleUntil = now.Add(time.Minute)
			}
		}
	}
}
