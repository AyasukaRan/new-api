package model

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// ListTraceConversationTreeTurns pages the selected thread and its recorded
// descendants together. MySQL 5.7 has no recursive CTEs, so walk indexed
// parent links one level at a time. Batch size limits SQL parameters, never
// the number or depth of branches an administrator can read.
func ListTraceConversationTreeTurns(ctx context.Context, id string, cursor TraceConversationCursor, limit int) ([]TraceConversationTurn, error) {
	if !strings.HasPrefix(id, "c_") {
		// Old captures without a known thread are independent records.
		return ListTraceConversationTurns(ctx, id, cursor, limit)
	}
	db := LOG_DB.WithContext(ctx)
	var userID int
	err := db.Model(&RequestTrace{}).
		Where("direction = ? AND conversation_key = ? AND created_at <= ?", TraceDirectionClientRequest, id, cursor.Snapshot).
		Limit(1).Pluck("conversation_user_id", &userID).Error
	if err != nil || userID == 0 {
		return []TraceConversationTurn{}, err
	}
	scoped := db.Model(&RequestTrace{}).
		Where("direction = ? AND conversation_user_id = ? AND created_at <= ?", TraceDirectionClientRequest, userID, cursor.Snapshot).
		Session(&gorm.Session{})
	const batchSize = 256
	parents := map[string]string{id: ""}
	keys := []string{id}
	for start := 0; start < len(keys); {
		end := len(keys)
		for batch := range slices.Chunk(keys[start:end], batchSize) {
			var children []struct {
				ConversationKey string
				AgreedParent    string
			}
			candidates := scoped.Select("conversation_key").Where("conversation_parent_key IN ? AND conversation_key <> ''", batch)
			err := scoped.Select("conversation_key, MIN(conversation_parent_key) AS agreed_parent").
				Where("conversation_key IN (?) AND conversation_parent_key <> ''", candidates).
				Group("conversation_key").Having("MIN(conversation_parent_key) = MAX(conversation_parent_key)").
				Order("conversation_key").Scan(&children).Error
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if _, seen := parents[child.ConversationKey]; seen {
					continue
				}
				parents[child.ConversationKey] = child.AgreedParent
				keys = append(keys, child.ConversationKey)
			}
		}
		start = end
	}

	items := make([]TraceConversationTurn, 0, limit)
	for batch := range slices.Chunk(keys, batchSize) {
		query := scoped.Where("conversation_key IN ?", batch)
		if cursor.ID != "" {
			query = query.Where("created_at < ? OR (created_at = ? AND trace_id < ?)", cursor.Time, cursor.Time, cursor.ID)
		}
		var turns []TraceConversationTurn
		err := query.Select("trace_id, request_id, created_at, conversation_key AS conversation_id, conversation_parent_key AS parent_id, conversation_agent AS agent_name, conversation_model AS model_name, conversation_turn_key AS turn_id, conversation_parent_turn_key AS parent_turn_id").
			Order("created_at DESC, trace_id DESC").Limit(limit).Scan(&turns).Error
		if err != nil {
			return nil, err
		}
		items = append(items, turns...)
		if len(items) > limit || len(keys) > batchSize {
			slices.SortFunc(items, func(a, b TraceConversationTurn) int {
				return cmp.Or(cmp.Compare(b.CreatedAt, a.CreatedAt), strings.Compare(b.TraceID, a.TraceID))
			})
			items = items[:min(limit, len(items))]
		}
	}
	for i := range items {
		turn := &items[i]
		turn.StartedAtMs = TraceStartMilliseconds(turn.TraceID, turn.CreatedAt)
		if turn.ConversationID != id {
			turn.ParentID = parents[turn.ConversationID]
		} else if _, cycle := parents[turn.ParentID]; cycle {
			turn.ParentID = ""
		}
	}
	return items, nil
}
