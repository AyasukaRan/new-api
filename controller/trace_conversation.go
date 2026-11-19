package controller

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func traceConversationPagination(c *gin.Context) (model.TraceConversationCursor, int, bool) {
	cursor := model.TraceConversationCursor{Snapshot: time.Now().Unix()}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "30"))
	valid := err == nil && limit > 0 && limit <= 100
	if token := c.Query("cursor"); token != "" {
		if len(token) > 256 {
			valid = false
		} else {
			raw, err := base64.RawURLEncoding.DecodeString(token)
			valid = valid && err == nil && common.Unmarshal(raw, &cursor) == nil &&
				cursor.Snapshot > 0 && cursor.Time > 0 && cursor.Time <= cursor.Snapshot && cursor.ID != "" && len(cursor.ID) <= 64
		}
	}
	if !valid {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
	}
	return cursor, limit, valid
}

func traceNextPage(cursor model.TraceConversationCursor, timestamp int64, id string) string {
	cursor.Time, cursor.ID = timestamp, id
	data, _ := common.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func GetTraceConversations(c *gin.Context) {
	cursor, limit, valid := traceConversationPagination(c)
	if !valid {
		return
	}
	items, err := model.ListTraceConversations(c.Request.Context(), cursor, limit+1)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = traceNextPage(cursor, last.UpdatedAt, last.ConversationID)
	}
	common.ApiSuccess(c, gin.H{"items": items, "next_cursor": next})
}

func GetTraceConversationTurns(c *gin.Context) {
	id := c.Query("conversation_id")
	if id == "" || len(id) > 64 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	cursor, limit, valid := traceConversationPagination(c)
	if !valid {
		return
	}
	items, err := model.ListTraceConversationTreeTurns(c.Request.Context(), id, cursor, limit+1)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = traceNextPage(cursor, last.CreatedAt, last.TraceID)
	}
	common.ApiSuccess(c, gin.H{"items": items, "next_cursor": next})
}
