package model

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

// Raw headers are classified on the server; the settings page receives only
// aggregated rule predicates. Internal request IDs also serve as cursor tie-breakers.
type UnidentifiedSourceRequest struct {
	RequestID string
	TraceID   string
	CreatedAt int64
	Headers   string
	Count     int
}

type SourceReviewWindow struct {
	Start, End                 int64
	SnapshotStart, SnapshotEnd int64
}

type SourceReviewPosition struct {
	WindowEnd int64  `json:"w,omitempty"`
	Time      int64  `json:"t"`
	RequestID string `json:"r,omitempty"`
	TraceID   string `json:"i,omitempty"`
	// Anonymous log records have no stable ID (even id=0 on ClickHouse). Count
	// them in a single timestamp bucket and resume partway through that bucket.
	Skipped int `json:"n,omitempty"`
}

func compareSourceReview(a, b UnidentifiedSourceRequest) int {
	return cmp.Or(cmp.Compare(b.CreatedAt, a.CreatedAt), cmp.Compare(b.RequestID, a.RequestID), cmp.Compare(b.TraceID, a.TraceID))
}

func sourceReviewPage(db, groups *gorm.DB, window SourceReviewWindow, cursor SourceReviewPosition, limit int) *gorm.DB {
	query := db.Table("(?) AS source_groups", groups).
		Select("source_rid AS request_id, source_tid AS trace_id, last_seen AS created_at, row_count AS count").
		Where("last_seen BETWEEN ? AND ?", window.Start, window.End)
	rid, tid := "source_rid", "source_tid"
	switch {
	case common.UsingLogDatabase(common.DatabaseTypeMySQL):
		rid, tid = "BINARY source_rid", "BINARY source_tid"
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		rid, tid = `source_rid COLLATE "C"`, `source_tid COLLATE "C"`
	}
	if cursor.Time > 0 {
		traceOp := " < ?"
		if cursor.Skipped > 0 {
			traceOp = " <= ?"
		}
		query = query.Where("last_seen < ? OR (last_seen = ? AND ("+rid+" < ? OR ("+rid+" = ? AND "+tid+traceOp+")))", cursor.Time, cursor.Time, cursor.RequestID, cursor.RequestID, cursor.TraceID)
	}
	return query.Order("last_seen DESC, " + rid + " DESC, " + tid + " DESC").Limit(limit + 1)
}

func recordedRequestSourceSQL() string {
	switch {
	case common.UsingLogDatabase(common.DatabaseTypeClickHouse):
		return "JSONExtractString(logs.other, 'client_tool')"
	case common.UsingLogDatabase(common.DatabaseTypeMySQL):
		const value = "JSON_EXTRACT(logs.other, '$.client_tool')"
		return "CASE WHEN JSON_VALID(logs.other) THEN CASE WHEN JSON_TYPE(" + value + ") = 'STRING' THEN JSON_UNQUOTE(" + value + ") END END"
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		return `CASE WHEN logs.other LIKE '%"client_tool"%' THEN CAST(logs.other AS json)->>'client_tool' END`
	default:
		return "CASE WHEN json_valid(logs.other) THEN json_extract(logs.other, '$.client_tool') END"
	}
}

type RequestSourceEvidence struct {
	RequestID, TraceID, Headers, URL, RecordedSource string
	CreatedAt                                        int64
}

func LookupRequestSource(ctx context.Context, id string) (*RequestSourceEvidence, error) {
	db := LOG_DB.WithContext(ctx)
	arg := "?"
	if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
		arg = "BINARY ?" // Request IDs remain case-sensitive on case-insensitive columns.
	}
	logs := applyLogSourceFilter(db.Model(&Log{})).Where("request_id = "+arg, id).Where("type IN ?", []int{LogTypeConsume, LogTypeError}).
		Order("created_at DESC, id DESC").Limit(1)
	var log RequestSourceEvidence
	// Project only the source. Legacy metadata can contain request bodies.
	read := logs.Session(&gorm.Session{}).Select("request_id, created_at, " + recordedRequestSourceSQL() + " AS recorded_source").Scan(&log)
	var pgErr interface{ SQLState() string }
	if errors.As(read.Error, &pgErr) && (pgErr.SQLState() == "22P02" || pgErr.SQLState() == "22P05" || pgErr.SQLState() == "22021") {
		read = logs.Select("request_id, created_at").Scan(&log)
	}
	if read.Error != nil {
		return nil, read.Error
	}
	if read.RowsAffected > 0 {
		return &log, nil
	}
	return nil, nil
}

func RecentUnidentifiedSourceRequests(ctx context.Context, bounds SourceReviewWindow, cursor SourceReviewPosition, limit int) ([]UnidentifiedSourceRequest, *SourceReviewPosition, error) {
	db := LOG_DB.WithContext(ctx)
	logs := applyLogSourceFilter(db.Model(&Log{})).Where("logs.created_at BETWEEN ? AND ? AND logs.type IN ?", bounds.Start, bounds.End, []int{LogTypeConsume, LogTypeError})
	emptyID := "''"
	if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		emptyID = "CAST('' AS text)" // An unresolved literal in a derived table is invalid on PostgreSQL 9.6.
	}
	client := recordedRequestSourceSQL()
	const logRID = "COALESCE(logs.request_id, '')"
	const anonTime = "CASE WHEN " + logRID + " = '' THEN logs.created_at ELSE 0 END"
	logGroups := logs.Session(&gorm.Session{}).
		Where("COALESCE(" + client + ", '') = ''").
		Select(logRID + " AS source_rid, " + emptyID + " AS source_tid, MAX(logs.created_at) AS last_seen, count(*) AS row_count").
		Group(logRID + ", " + anonTime)
	var records []UnidentifiedSourceRequest
	err := sourceReviewPage(db, logGroups, bounds, cursor, limit).Scan(&records).Error
	if err != nil {
		var pgErr interface{ SQLState() string }
		if !errors.As(err, &pgErr) || pgErr.SQLState() != "22P02" && pgErr.SQLState() != "22P05" && pgErr.SQLState() != "22021" {
			return nil, nil, err
		}
		// PostgreSQL 9.6 lacks a JSON-validity predicate. If an old usage log
		// is malformed, inspect the rows one at a time. Never expose
		// metadata/body text, or let a corrupt row hide the rest of the window.
		records, err = sourceReviewLegacyLogs(logs, cursor)
		if err != nil {
			return nil, nil, err
		}
	}
	for i := range records {
		if records[i].RequestID != "" {
			records[i].Count = 1
		}
	}
	slices.SortFunc(records, compareSourceReview)
	var next *SourceReviewPosition
	remaining := limit
	for i := range records {
		row := &records[i]
		skipped := 0
		if cursor.Skipped > 0 && row.CreatedAt == cursor.Time && row.RequestID == "" && row.TraceID == "" {
			skipped = cursor.Skipped
			row.Count = max(0, row.Count-skipped)
		}
		if row.Count > remaining || row.Count == remaining && i+1 < len(records) {
			next = &SourceReviewPosition{Time: row.CreatedAt, RequestID: row.RequestID, TraceID: row.TraceID}
			if row.Count > remaining {
				next.Skipped = skipped + remaining
			}
			row.Count = remaining
			records = records[:i+1]
			break
		}
		remaining -= row.Count
	}
	// An error/retry can straddle two review windows.
	// Check only candidate request IDs, using the log database's request_id index.
	if bounds.End < bounds.SnapshotEnd && len(records) > 0 {
		candidates := make([]string, 0, len(records))
		for _, row := range records {
			if row.RequestID != "" {
				candidates = append(candidates, row.RequestID)
			}
		}
		if len(candidates) > 0 {
			later, err := applyLogSourceFilter(db.Model(&Log{})).
				Where("request_id IN ? AND created_at > ? AND created_at <= ? AND type IN ?", candidates, bounds.End, bounds.SnapshotEnd, []int{LogTypeConsume, LogTypeError}).
				Select("request_id, COALESCE(other, '')").Rows()
			if err != nil {
				return nil, nil, err
			}
			defer later.Close()
			seen := make(map[string]bool)
			for later.Next() {
				var id, other string
				if err := later.Scan(&id, &other); err != nil {
					return nil, nil, err
				}
				if !gjson.Valid(other) || gjson.Get(other, "client_tool").String() == "" {
					seen[id] = true
				}
			}
			if err := later.Err(); err != nil {
				return nil, nil, err
			}
			records = slices.DeleteFunc(records, func(item UnidentifiedSourceRequest) bool { return seen[item.RequestID] })
		}
	}
	return records, next, nil
}

func sourceReviewLegacyLogs(logs *gorm.DB, cursor SourceReviewPosition) ([]UnidentifiedSourceRequest, error) {
	rows, err := logs.Select("COALESCE(logs.request_id,''), logs.created_at, COALESCE(logs.other,'')").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type identity struct {
		request string
		stamp   int64
	}
	groups := make(map[identity]UnidentifiedSourceRequest)
	for rows.Next() {
		var id, other string
		var stamp int64
		if err := rows.Scan(&id, &stamp, &other); err != nil {
			return nil, err
		}
		if gjson.Valid(other) && gjson.Get(other, "client_tool").String() != "" {
			continue
		}
		key := identity{request: id}
		if id == "" {
			key.stamp = stamp
		}
		record := groups[key]
		record.CreatedAt, record.RequestID = max(record.CreatedAt, stamp), id
		if id != "" {
			record.Count = 1
		} else {
			record.Count++
		}
		groups[key] = record
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]UnidentifiedSourceRequest, 0, len(groups))
	for _, record := range groups {
		order := compareSourceReview(record, UnidentifiedSourceRequest{CreatedAt: cursor.Time, RequestID: cursor.RequestID, TraceID: cursor.TraceID})
		if cursor.Time == 0 || order > 0 || order == 0 && cursor.Skipped > 0 {
			result = append(result, record)
		}
	}
	return result, nil
}
