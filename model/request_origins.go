package model

import (
	"context"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const RequestOriginsSampleLimit = 50000

// RequestOriginRow is deliberately limited to the columns needed for GeoIP;
// request bodies, credentials and billing metadata never enter this query.
type RequestOriginRow struct {
	Id        int
	UserId    int
	Ip        string
	CreatedAt int64
	RequestId string
}

func GetRequestOriginRows(ctx context.Context, filter LogFilter, userID int, refs []LiveLogRef) ([]RequestOriginRow, bool, error) {
	filter.Category = "model"
	filter.Source = LogSourceUsage
	tx := LOG_DB.WithContext(ctx).Model(&Log{})
	var err error
	if userID > 0 {
		filter.Username = ""
		tx, err = filter.forUser(tx, userID)
	} else {
		tx, err = filter.apply(tx)
	}
	if err != nil {
		return nil, false, err
	}
	tx = tx.Select("logs.id, logs.user_id, logs.ip, logs.created_at, logs.request_id")
	order := "logs.created_at desc, logs.id desc"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = clickHouseLogOrder("logs.")
	}
	rows := make([]RequestOriginRow, 0)
	if refs == nil {
		if err := tx.Order(order).Limit(RequestOriginsSampleLimit + 1).Find(&rows).Error; err != nil {
			return nil, false, err
		}
		truncated := len(rows) > RequestOriginsSampleLimit
		return rows[:min(len(rows), RequestOriginsSampleLimit)], truncated, nil
	}
	// Journal batches are bounded. Split binds for SQLite and keep ClickHouse
	// reads on its time/request sorting key without selecting log bodies.
	seen := make(map[LiveLogRef]bool, len(refs))
	for batch := range slices.Chunk(refs, 300) {
		ids := make([]int, 0, len(batch))
		times := make([]int64, 0, len(batch))
		requests := make([]string, 0, len(batch))
		for _, ref := range batch {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			ids = append(ids, ref.ID)
			times = append(times, ref.CreatedAt)
			requests = append(requests, ref.RequestID)
		}
		if len(ids) == 0 {
			continue
		}
		query := tx.Session(&gorm.Session{}).Where("logs.id IN ?", ids)
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			query = query.Where("logs.created_at IN ? AND logs.request_id IN ?", times, requests)
		}
		var part []RequestOriginRow
		if err := query.Order(order).Find(&part).Error; err != nil {
			return nil, false, err
		}
		rows = append(rows, part...)
	}
	return rows, false, nil
}
