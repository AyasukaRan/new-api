package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type liveLogPage struct {
	*common.PageInfo
	LiveCursor string `json:"live_cursor,omitempty"`
}

func StreamAllLogChanges(c *gin.Context)  { streamLogChanges(c, true) }
func StreamUserLogChanges(c *gin.Context) { streamLogChanges(c, false) }

func streamLogChanges(c *gin.Context, all bool) {
	ctx := c.Request.Context()
	filter := logFilterFromQuery(c, all)
	if filter.Source != model.LogSourceUsage {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid log source"})
		return
	}
	ownUserID := 0
	role := c.GetInt("role")
	if !all {
		ownUserID = c.GetInt("id")
		if ownUserID <= 0 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if role < common.RoleAdminUser {
			start, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp)
			if err != nil {
				common.ApiError(c, err)
				return
			}
			filter.StartTimestamp = start
		}
	}
	cursor := c.Query("cursor")
	if cursor == "" {
		cursor = c.GetHeader("Last-Event-ID")
	}
	pageSize := common.GetPageQuery(c).GetPageSize()
	compact := c.Query("compact") == "1"
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store, no-transform")
	c.Header("X-Accel-Buffering", "no")
	writer := http.NewResponseController(c.Writer)
	write := func(event string, data any) bool {
		encoded, err := common.Marshal(data)
		if err != nil {
			return false
		}
		_ = writer.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, encoded); err != nil {
			return false
		}
		err = writer.Flush()
		_ = writer.SetWriteDeadline(time.Time{})
		return err == nil
	}
	if !write("ready", struct{}{}) {
		return
	}
	check := time.NewTicker(time.Second)
	defer check.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	// A fresh HTTP request must pass the dashboard role and token-scope
	// middleware again; bounded connections also let rolling updates drain.
	lifetime := time.NewTimer(45 * time.Second)
	defer lifetime.Stop()
	for {
		refs, next, reset := model.LiveLogChanges(cursor, ownUserID)
		if reset {
			write("reset", struct{}{})
			return
		}
		if len(refs) > 0 {
			items, omitted, err := model.GetChangedLogs(ctx, filter, ownUserID, role, compact, pageSize, refs)
			if err != nil {
				write("reset", struct{}{})
				return
			}
			if len(items) > 0 && !write("logs", gin.H{"items": items, "omitted": omitted, "cursor": next}) {
				return
			}
		}
		cursor = next
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			return
		case <-check.C:
		case <-heartbeat.C:
			// Advance quiet filters on a fixed cadence, without revealing
			// whether another account has recently received traffic.
			if !write("logs", gin.H{"items": []any{}, "omitted": 0, "cursor": cursor}) {
				return
			}
		}
	}
}
