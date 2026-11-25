package controller

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type requestOriginPoint struct {
	common.GeoIPLocation
	LocationID string `json:"location_id"`
	Count      int    `json:"count"`
	LastSeen   int64  `json:"last_seen"`
}

type requestOriginSnapshot struct {
	Points         []requestOriginPoint `json:"points"`
	Total          int                  `json:"total"`
	Located        int                  `json:"located"`
	Unmapped       int                  `json:"unmapped"`
	Private        int                  `json:"private"`
	Invalid        int                  `json:"invalid"`
	Truncated      bool                 `json:"truncated"`
	SampleStart    int64                `json:"sample_start"`
	Cursor         string               `json:"cursor"`
	GeoIPAvailable bool                 `json:"geoip_available"`
	GeoIPUpdatedAt int64                `json:"geoip_updated_at"`
}

func aggregateRequestOrigins(rows []model.RequestOriginRow, resolver *common.GeoIPResolver) requestOriginSnapshot {
	result := requestOriginSnapshot{Points: make([]requestOriginPoint, 0), Total: len(rows)}
	result.GeoIPAvailable, result.GeoIPUpdatedAt = resolver.Status()
	points := make(map[common.GeoIPLocation]int)
	for _, row := range rows {
		if result.SampleStart == 0 || row.CreatedAt < result.SampleStart {
			result.SampleStart = row.CreatedAt
		}
		location, status := resolver.Lookup(row.Ip)
		if status != "located" {
			result.Unmapped++
			if status == "private" {
				result.Private++
			}
			if status == "invalid" {
				result.Invalid++
			}
			continue
		}
		result.Located++
		index, exists := points[location]
		if !exists {
			key := fmt.Sprintf("%s/%s/%s/%g/%g", location.CountryCode, location.Region, location.City, location.Latitude, location.Longitude)
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:16]
			index = len(result.Points)
			points[location] = index
			result.Points = append(result.Points, requestOriginPoint{GeoIPLocation: location, LocationID: id})
		}
		result.Points[index].Count++
		result.Points[index].LastSeen = max(result.Points[index].LastSeen, row.CreatedAt)
	}
	slices.SortFunc(result.Points, func(a, b requestOriginPoint) int {
		if order := cmp.Compare(b.Count, a.Count); order != 0 {
			return order
		}
		return cmp.Compare(a.LocationID, b.LocationID)
	})
	return result
}

func requestOriginsFilter(c *gin.Context, all, live bool) (model.LogFilter, int, bool) {
	userID := c.GetInt("id")
	if userID <= 0 || (all && c.GetInt("role") < common.RoleAdminUser) {
		c.AbortWithStatus(http.StatusForbidden)
		return model.LogFilter{}, 0, false
	}
	if all {
		userID = 0
	}
	now := time.Now().Unix()
	filter := model.LogFilter{Category: "model", Source: model.LogSourceUsage, StartTimestamp: now - 86400, EndTimestamp: now}
	if live {
		filter.EndTimestamp = 0
	}
	for key, value := range map[string]*int64{"start_timestamp": &filter.StartTimestamp, "end_timestamp": &filter.EndTimestamp} {
		if raw, exists := c.GetQuery(key); exists {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed <= 0 {
				common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
				return model.LogFilter{}, 0, false
			}
			*value = parsed
		}
	}
	end := filter.EndTimestamp
	if end == 0 {
		end = now
	}
	if filter.StartTimestamp > end || end-filter.StartTimestamp > 366*86400 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return model.LogFilter{}, 0, false
	}
	if c.GetInt("role") < common.RoleAdminUser {
		start, err := limitLogTimeRange(filter.StartTimestamp, end)
		if err != nil {
			common.ApiError(c, err)
			return model.LogFilter{}, 0, false
		}
		filter.StartTimestamp = start
	}
	if all {
		filter.Username = c.Query("username")
	}
	return filter, userID, true
}

func GetAllRequestOrigins(c *gin.Context)  { getRequestOrigins(c, true) }
func GetUserRequestOrigins(c *gin.Context) { getRequestOrigins(c, false) }

func getRequestOrigins(c *gin.Context, all bool) {
	filter, userID, ok := requestOriginsFilter(c, all, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	cursor := model.LiveLogCursor()
	rows, truncated, err := model.GetRequestOriginRows(ctx, filter, userID, nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	result := aggregateRequestOrigins(rows, common.DefaultGeoIPResolver())
	result.Truncated = truncated
	// Activity committed while loading the snapshot must still animate. Live
	// pulses never increment snapshot totals, so this overlap cannot double-count.
	result.Cursor = cursor
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, result)
}

func StreamAllRequestOrigins(c *gin.Context)  { streamRequestOrigins(c, true) }
func StreamUserRequestOrigins(c *gin.Context) { streamRequestOrigins(c, false) }

func streamRequestOrigins(c *gin.Context, all bool) {
	filter, userID, ok := requestOriginsFilter(c, all, true)
	if !ok {
		return
	}
	lifetime := 45 * time.Second
	if expires := middleware.DashboardAccessTokenExpiry(c); expires > 0 {
		lifetime = min(lifetime, time.Until(time.Unix(expires, 0)))
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), lifetime)
	defer cancel()
	cursor := c.Query("cursor")
	if cursor == "" {
		cursor = c.GetHeader("Last-Event-ID")
	}
	if cursor == "" {
		cursor = model.LiveLogCursor()
	}
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
	seen := make(map[string]bool)
	for {
		refs, next, reset := model.LiveLogChanges(cursor, userID)
		if reset {
			write("reset", struct{}{})
			return
		}
		if len(refs) > 0 {
			rows, _, err := model.GetRequestOriginRows(ctx, filter, userID, refs)
			if err != nil {
				write("reset", struct{}{})
				return
			}
			fresh := rows[:0]
			for _, row := range rows {
				key := fmt.Sprintf("%d/%s", row.UserId, row.RequestId)
				if row.RequestId == "" {
					key = fmt.Sprintf("%d/%d/%d", row.UserId, row.CreatedAt, row.Id)
				}
				if !seen[key] {
					seen[key] = true
					fresh = append(fresh, row)
				}
			}
			result := aggregateRequestOrigins(fresh, common.DefaultGeoIPResolver())
			if len(result.Points) > 0 && !write("origins", gin.H{"points": result.Points, "cursor": next, "emitted_at": time.Now().Unix()}) {
				return
			}
			if len(seen) > 8192 {
				write("reset", struct{}{})
				return
			}
		}
		cursor = next
		select {
		case <-ctx.Done():
			return
		case <-check.C:
		case <-heartbeat.C:
			if !write("origins", gin.H{"points": []any{}, "cursor": cursor, "emitted_at": time.Now().Unix()}) {
				return
			}
		}
	}
}
