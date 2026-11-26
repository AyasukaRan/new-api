package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
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
	// A fresh connection checks role and token scopes again. Bound the
	// database query lifetime too, so rolling deployments can drain.
	lifetime := 45 * time.Second
	if expires := middleware.DashboardAccessTokenExpiry(c); expires > 0 {
		lifetime = min(lifetime, time.Until(time.Unix(expires, 0)))
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), lifetime)
	defer cancel()
	filter := logFilterFromQuery(c, all)
	if err := filter.Validate(); err != nil {
		common.ApiError(c, err)
		return
	}
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
		if ctx.Err() != nil {
			return false
		}
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
	var activeRevision uint64
	activeSent := false
	lastActive := ""
	for {
		refs, next, reset := model.LiveLogChanges(cursor, ownUserID)
		if reset {
			write("reset", struct{}{})
			return
		}
		if len(refs) > 0 {
			page, err := model.GetLogChangePage(ctx, filter, ownUserID, role, compact, pageSize, refs)
			if err != nil {
				write("reset", struct{}{})
				return
			}
			if (len(page.Items) > 0 || page.Replace) && !write("logs", gin.H{"items": page.Items, "omitted": page.Omitted, "replace": page.Replace, "total": page.Total, "cursor": next}) {
				return
			}
		}
		// Deliver committed rows first; the authoritative active snapshot
		// then removes its temporary counterpart without a blank transition.
		revision := model.ActiveLogRevision()
		if !activeSent || revision != activeRevision {
			items, err := model.ActiveLogSnapshot(ctx, filter, ownUserID, role)
			if err != nil {
				write("reset", struct{}{})
				return
			}
			encoded, err := common.Marshal(items)
			if err != nil {
				return
			}
			// A different user's activity must not reveal its timing through
			// repeated empty snapshots on a personal or filtered stream.
			if !activeSent || string(encoded) != lastActive {
				if !write("active", gin.H{"items": items}) {
					return
				}
				lastActive = string(encoded)
			}
			activeRevision, activeSent = revision, true
		}
		cursor = next
		select {
		case <-ctx.Done():
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
