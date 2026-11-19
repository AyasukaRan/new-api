package model

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"gorm.io/gorm"
)

// Chat and Gemini share their wire formats with non-conversational endpoints.
var traceConversationPaths = map[string][]string{
	string(types.RelayFormatOpenAI): {"/chat/completions"},
	types.RelayFormatGemini:         {":generateContent", ":streamGenerateContent"},
}

func IsConversationTrace(format, rawURL string) bool {
	if format == types.RelayFormatClaude || format == types.RelayFormatOpenAIResponses {
		return true
	}
	path, _, _ := strings.Cut(rawURL, "?")
	for _, suffix := range traceConversationPaths[format] {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// ConversationTraceQuery includes retained conversations from before the metadata index.
func ConversationTraceQuery(db *gorm.DB) *gorm.DB {
	legacy := db.Session(&gorm.Session{NewDB: true}).Where("format IN ?", []string{types.RelayFormatClaude, types.RelayFormatOpenAIResponses})
	for format, suffixes := range traceConversationPaths {
		for _, suffix := range suffixes {
			legacy = legacy.Or("(format = ? AND (url LIKE ? OR url LIKE ?) AND url NOT LIKE ?)",
				format, "%"+suffix, "%"+suffix+"?%", "%?%"+suffix+"%")
		}
	}
	return db.Model(&RequestTrace{}).Where("direction = ?", TraceDirectionClientRequest).
		Where(db.Session(&gorm.Session{NewDB: true}).Where("conversation_key <> ''").Or(legacy))
}

type TraceConversation struct {
	ConversationID string `json:"conversation_id"`
	ParentID       string `json:"parent_id"`
	AgentName      string `json:"agent_name"`
	Title          string `json:"title"`
	ModelName      string `json:"model_name"`
	UserID         int    `json:"user_id"`
	StartedAt      int64  `json:"started_at"`
	UpdatedAt      int64  `json:"updated_at"`
	TurnCount      int    `json:"turn_count"`
}

type TraceConversationTurn struct {
	TurnID         string `json:"turn_id,omitempty"`
	ParentTurnID   string `json:"parent_turn_id,omitempty"`
	ConversationID string `json:"conversation_id"`
	ParentID       string `json:"parent_id"`
	AgentName      string `json:"agent_name"`
	StartedAtMs    int64  `json:"started_at_ms"`
	TraceID        string `json:"trace_id"`
	RequestID      string `json:"request_id"`
	ModelName      string `json:"model_name"`
	CreatedAt      int64  `json:"created_at"`
}

// The cursor's snapshot prevents incoming turns from moving an existing thread across pages.
type TraceConversationCursor struct {
	Snapshot int64  `json:"s"`
	Time     int64  `json:"t"`
	ID       string `json:"i"`
}

type conversationSummary struct {
	TraceConversation
	FirstTrace string
}

func ListTraceConversations(ctx context.Context, cursor TraceConversationCursor, limit int) ([]TraceConversation, error) {
	db := LOG_DB.WithContext(ctx)
	const key = "COALESCE(NULLIF(conversation_key, ''), trace_id)"
	const summaryFields = key + " AS conversation_id, MIN(trace_id) AS first_trace, MIN(created_at) AS started_at, MAX(created_at) AS updated_at, COUNT(*) AS turn_count, MAX(conversation_user_id) AS user_id"
	base := ConversationTraceQuery(db).Where("created_at <= ?", cursor.Snapshot).Session(&gorm.Session{})
	// Only threads with an explicit parent claim need graph resolution. Ordinary
	// one-request conversations stay on the database's bounded pagination path.
	linked := db.Model(&RequestTrace{}).Where("direction = ? AND created_at <= ? AND conversation_user_id > 0 AND conversation_parent_key <> ''",
		TraceDirectionClientRequest, cursor.Snapshot).Session(&gorm.Session{})
	var edges []struct {
		ConversationKey       string
		ConversationParentKey string
	}
	if err := linked.Distinct("conversation_key", "conversation_parent_key").
		Order("conversation_key, conversation_parent_key").Find(&edges).Error; err != nil {
		return nil, err
	}
	summaries := base.Select(summaryFields).Group(key)
	if len(edges) > 0 {
		summaries = summaries.Where(key+" NOT IN (?) AND "+key+" NOT IN (?)", linked.Select("conversation_key"), linked.Select("conversation_parent_key"))
	}
	page := db.Table("(?) AS grouped", summaries)
	if cursor.ID != "" {
		page = page.Where("updated_at < ? OR (updated_at = ? AND conversation_id < ?)", cursor.Time, cursor.Time, cursor.ID)
	}
	var rows []conversationSummary
	if err := page.Order("updated_at DESC, conversation_id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(edges) > 0 {
		keys := make([]string, 0, len(edges)*2)
		for _, edge := range edges {
			keys = append(keys, edge.ConversationKey, edge.ConversationParentKey)
		}
		slices.Sort(keys)
		nodes := make(map[string]conversationSummary)
		for batch := range slices.Chunk(slices.Compact(keys), 256) {
			var matches []conversationSummary
			if err := base.Where("conversation_key IN ?", batch).Select(summaryFields).Group(key).Scan(&matches).Error; err != nil {
				return nil, err
			}
			for _, node := range matches {
				nodes[node.ConversationID] = node
			}
		}
		parents := make(map[string]string)
		for _, edge := range edges {
			if previous, seen := parents[edge.ConversationKey]; seen && previous != edge.ConversationParentKey {
				// Conflicting claims cannot link this thread to either parent.
				parents[edge.ConversationKey] = ""
				continue
			}
			parents[edge.ConversationKey] = edge.ConversationParentKey
		}
		for child, parent := range parents {
			if nodes[child].UserID <= 0 || nodes[child].UserID != nodes[parent].UserID {
				// The parent is absent from the retained records or belongs to another account.
				delete(parents, child)
			}
		}
		totals := make(map[string]conversationSummary)
		roots := make(map[string]string)
		for id, node := range nodes {
			root := traceConversationRoot(id, parents, roots)
			total, ok := totals[root]
			if !ok {
				total = nodes[root]
				total.TurnCount = 0
			}
			total.TurnCount += node.TurnCount
			total.StartedAt = min(total.StartedAt, node.StartedAt)
			total.UpdatedAt = max(total.UpdatedAt, node.UpdatedAt)
			totals[root] = total
		}
		for _, total := range totals {
			if cursor.ID == "" || total.UpdatedAt < cursor.Time || total.UpdatedAt == cursor.Time && total.ConversationID < cursor.ID {
				rows = append(rows, total)
			}
		}
		slices.SortFunc(rows, func(a, b conversationSummary) int {
			return cmp.Or(cmp.Compare(b.UpdatedAt, a.UpdatedAt), strings.Compare(b.ConversationID, a.ConversationID))
		})
		rows = rows[:min(len(rows), limit)]
	}

	items := make([]TraceConversation, 0, len(rows))
	if len(rows) == 0 {
		return items, nil
	}
	// Project labels only for the current page, including the original title of
	// the parent thread when a child produced the latest activity.
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.FirstTrace)
	}
	var labels []RequestTrace
	if err := db.Select("trace_id, conversation_title, conversation_model, conversation_parent_key, conversation_agent").
		Where("trace_id IN ? AND direction = ?", ids, TraceDirectionClientRequest).Find(&labels).Error; err != nil {
		return nil, err
	}
	byTrace := make(map[string]RequestTrace, len(labels))
	for _, label := range labels {
		byTrace[label.TraceId] = label
	}
	for _, row := range rows {
		label := byTrace[row.FirstTrace]
		row.Title, row.ModelName = label.ConversationTitle, label.ConversationModel
		row.ParentID, row.AgentName = label.ConversationParentKey, label.ConversationAgent
		items = append(items, row.TraceConversation)
	}
	return items, nil
}

// Resolve a recorded thread ancestry once. If a client supplied a cycle, keep
// its smallest key as one browseable root, independent of map iteration order.
func traceConversationRoot(id string, parents, roots map[string]string) string {
	var path []string
	seen := make(map[string]int)
	root := id
	for {
		if known := roots[root]; known != "" {
			root = known
			break
		}
		if start, ok := seen[root]; ok {
			root = slices.Min(path[start:])
			break
		}
		seen[root] = len(path)
		path = append(path, root)
		parent := parents[root]
		if parent == "" {
			break
		}
		root = parent
	}
	for _, id := range path {
		roots[id] = root
	}
	return root
}

func ListTraceConversationTurns(ctx context.Context, id string, cursor TraceConversationCursor, limit int) ([]TraceConversationTurn, error) {
	query := ConversationTraceQuery(LOG_DB.WithContext(ctx)).Where("created_at <= ?", cursor.Snapshot).
		Where("(conversation_key = ? OR ((conversation_key IS NULL OR conversation_key = '') AND trace_id = ?))", id, id)
	if cursor.ID != "" {
		query = query.Where("created_at < ? OR (created_at = ? AND trace_id < ?)", cursor.Time, cursor.Time, cursor.ID)
	}
	items := make([]TraceConversationTurn, 0, limit)
	err := query.Select("trace_id, request_id, conversation_model AS model_name, created_at, conversation_parent_key AS parent_id, conversation_agent AS agent_name, conversation_turn_key AS turn_id, conversation_parent_turn_key AS parent_turn_id").
		Order("created_at DESC, trace_id DESC").Limit(limit).Scan(&items).Error
	for i := range items {
		items[i].ConversationID = id
		items[i].StartedAtMs = TraceStartMilliseconds(items[i].TraceID, items[i].CreatedAt)
	}
	return items, err
}

// NewRequestId prefixes the trace ID with the gateway's UTC start time and nanoseconds.
func TraceStartMilliseconds(traceID string, createdAt int64) int64 {
	if len(traceID) >= 23 {
		stamp, err := time.Parse("20060102150405.000000000", traceID[:14]+"."+traceID[14:23])
		if err == nil {
			return stamp.UnixMilli()
		}
	}
	return createdAt * 1000
}
