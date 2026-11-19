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

type SourceReviewPosition struct {
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

func sourceReviewPage(db, groups *gorm.DB, cursor SourceReviewPosition, limit int) *gorm.DB {
	query := db.Table("(?) AS source_groups", groups)
	rid, tid := "request_id", "trace_id"
	switch {
	case common.UsingLogDatabase(common.DatabaseTypeMySQL):
		rid, tid = "BINARY request_id", "BINARY trace_id"
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		rid, tid = `request_id COLLATE "C"`, `trace_id COLLATE "C"`
	}
	if cursor.Time > 0 {
		traceOp := " < ?"
		if cursor.Skipped > 0 {
			traceOp = " <= ?"
		}
		query = query.Where("created_at < ? OR (created_at = ? AND ("+rid+" < ? OR ("+rid+" = ? AND "+tid+traceOp+")))", cursor.Time, cursor.Time, cursor.RequestID, cursor.RequestID, cursor.TraceID)
	}
	return query.Order("created_at DESC, " + rid + " DESC, " + tid + " DESC").Limit(limit + 1)
}

func RecentUnidentifiedSourceRequests(ctx context.Context, since, until int64, cursor SourceReviewPosition, limit int) ([]UnidentifiedSourceRequest, *SourceReviewPosition, error) {
	db := LOG_DB.WithContext(ctx)
	var captures []UnidentifiedSourceRequest
	const rid = "COALESCE(request_id, '')"
	const unnamedTrace = "CASE WHEN " + rid + " = '' THEN trace_id ELSE '' END"
	window := db.Model(&RequestTrace{}).Where("created_at BETWEEN ? AND ? AND direction = ?", since, until, TraceDirectionClientRequest)
	traceGroups := window.Session(&gorm.Session{}).Where("conversation_client = ?", "").
		Select(rid + " AS request_id, " + unnamedTrace + " AS trace_id, MAX(created_at) AS created_at, 1 AS count").
		Group(rid + ", " + unnamedTrace)
	if err := sourceReviewPage(db, traceGroups, cursor, limit).Scan(&captures).Error; err != nil {
		return nil, nil, err
	}

	// The inbound capture is the source of truth when indexed. Test the small
	// index projection before accessing logs.other, which can contain a large
	// inline body on nearly every usage record. Retained captures can outlive
	// their logs and imported logs may have no capture.
	indexed := window.Session(&gorm.Session{}).Where("conversation_client IS NOT NULL AND request_id <> ''").
		Select("DISTINCT request_id, 1 AS present")
	logs := applyLogSourceFilter(db.Model(&Log{})).Where("logs.created_at BETWEEN ? AND ? AND logs.type IN ?", since, until, []int{LogTypeConsume, LogTypeError}).
		Joins("LEFT JOIN (?) AS capture_index ON logs.request_id = capture_index.request_id", indexed)
	var client string
	switch {
	case common.UsingLogDatabase(common.DatabaseTypeClickHouse):
		client = "JSONExtractString(logs.other, 'client_tool')"
	case common.UsingLogDatabase(common.DatabaseTypeMySQL):
		const value = "JSON_EXTRACT(logs.other, '$.client_tool')"
		client = "CASE WHEN JSON_VALID(logs.other) THEN CASE WHEN JSON_TYPE(" + value + ") = 'STRING' THEN JSON_UNQUOTE(" + value + ") END END"
	case common.UsingLogDatabase(common.DatabaseTypePostgreSQL):
		client = `CASE WHEN logs.other LIKE '%"client_tool"%' THEN CAST(logs.other AS json)->>'client_tool' END`
	default:
		client = "CASE WHEN json_valid(logs.other) THEN json_extract(logs.other, '$.client_tool') END"
	}
	const logRID = "COALESCE(logs.request_id, '')"
	const anonTime = "CASE WHEN " + logRID + " = '' THEN logs.created_at ELSE 0 END"
	logGroups := logs.Session(&gorm.Session{}).
		Where("CASE WHEN COALESCE(capture_index.present, 0) = 0 THEN COALESCE(" + client + ", '') ELSE 'indexed' END = ''").
		Select(logRID + " AS request_id, '' AS trace_id, MAX(logs.created_at) AS created_at, CASE WHEN " + logRID + " = '' THEN count(*) ELSE 1 END AS count").
		Group(logRID + ", " + anonTime)
	var orphaned []UnidentifiedSourceRequest
	err := sourceReviewPage(db, logGroups, cursor, limit).Scan(&orphaned).Error
	if err != nil {
		var pgErr interface{ SQLState() string }
		if !errors.As(err, &pgErr) || pgErr.SQLState() != "22P02" && pgErr.SQLState() != "22P05" && pgErr.SQLState() != "22021" {
			return nil, nil, err
		}
		// PostgreSQL 9.6 lacks a JSON-validity predicate. If an old orphan log
		// is malformed, inspect the unindexed rows one at a time. Never expose
		// metadata/body text, or let a corrupt row hide the rest of the window.
		orphaned, err = sourceReviewLegacyLogs(logs, cursor)
		if err != nil {
			return nil, nil, err
		}
	}
	records := append(captures, orphaned...)
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
	// Load original headers for the selected page only. Exclude unbounded
	// header blocks and upstream legs. Unindexed non-conversation captures can
	// still provide evidence for a usage/error log.
	requestIDs, traceIDs := []string{}, []string{}
	type identity struct{ request, trace string }
	index := make(map[identity]int)
	for i, row := range records {
		if row.RequestID != "" {
			requestIDs = append(requestIDs, row.RequestID)
			index[identity{request: row.RequestID}] = i
		} else if row.TraceID != "" {
			traceIDs = append(traceIDs, row.TraceID)
			index[identity{trace: row.TraceID}] = i
		}
	}
	if len(index) > 0 {
		var evidence []UnidentifiedSourceRequest
		err := db.Model(&RequestTrace{}).
			Select("request_id, trace_id, CASE WHEN length(headers) <= 65536 THEN headers ELSE '{}' END AS headers").
			Where("direction = ? AND (request_id IN ? OR trace_id IN ?)", TraceDirectionClientRequest, requestIDs, traceIDs).
			Order("created_at DESC, seq ASC, trace_id DESC").Scan(&evidence).Error
		if err != nil {
			return nil, nil, err
		}
		for _, leg := range evidence {
			key := identity{request: leg.RequestID}
			if leg.RequestID == "" {
				key.trace = leg.TraceID
			}
			if i, ok := index[key]; ok && records[i].Headers == "" {
				records[i].Headers = leg.Headers
			}
		}
	}
	return records, next, nil
}

func sourceReviewLegacyLogs(logs *gorm.DB, cursor SourceReviewPosition) ([]UnidentifiedSourceRequest, error) {
	rows, err := logs.Where("COALESCE(capture_index.present, 0) = 0").Select("COALESCE(logs.request_id,''), logs.created_at, COALESCE(logs.other,'')").Rows()
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
