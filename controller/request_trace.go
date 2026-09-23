package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

type requestTraceLeg struct {
	Seq       int                 `json:"seq"`
	Attempt   int                 `json:"attempt"`
	Direction string              `json:"direction"`
	ChannelId int                 `json:"channel_id"`
	Format    string              `json:"format"`
	Method    string              `json:"method"`
	Url       string              `json:"url"`
	Status    int                 `json:"status"`
	Headers   map[string][]string `json:"headers"`
	Body      string              `json:"body"`
	BodySize  int64               `json:"body_size"`
	Truncated bool                `json:"truncated"`
	// HasObject marks a binary payload held in object storage rather than
	// inline, which the viewer fetches separately to preview or download.
	HasObject   bool   `json:"has_object"`
	ContentType string `json:"content_type,omitempty"`
	// Rendered is the parsed assistant turn, present only on response legs and
	// only when the payload was recognizable.
	Rendered *service.TraceRenderedResponse `json:"rendered,omitempty"`
}

// GetRequestTrace returns the captured exchange behind one usage log. The trace
// id comes from the log's admin-only scope, so a caller who cannot see
// admin_info cannot construct a request for someone else's payloads.
func GetRequestTrace(c *gin.Context) {
	traceId := c.Query("trace_id")
	if traceId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "缺少 trace_id"})
		return
	}
	traces, err := model.GetRequestTraces(traceId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	legs := make([]requestTraceLeg, 0, len(traces))
	for _, trace := range traces {
		leg := requestTraceLeg{
			Seq:         trace.Seq,
			Attempt:     trace.Attempt,
			Direction:   trace.Direction,
			ChannelId:   trace.ChannelId,
			Format:      trace.Format,
			Method:      trace.Method,
			Url:         trace.Url,
			Status:      trace.Status,
			Body:        trace.Body,
			BodySize:    trace.BodySize,
			Truncated:   trace.Truncated,
			HasObject:   trace.ObjectKey != "",
			ContentType: trace.ContentType,
		}
		if trace.Headers != "" {
			headers := make(map[string][]string)
			if err := common.UnmarshalJsonStr(trace.Headers, &headers); err == nil {
				leg.Headers = headers
			}
		}
		if trace.Direction == model.TraceDirectionUpstreamResponse || trace.Direction == model.TraceDirectionClientResponse {
			leg.Rendered = service.RenderTraceResponse(trace.Format, trace.Body)
		}
		legs = append(legs, leg)
	}

	common.ApiSuccess(c, gin.H{"trace_id": traceId, "legs": legs})
}

// traceObjectDispositions maps the viewer's intent onto a Content-Disposition.
// Anything not explicitly previewable is sent as an attachment so a stored
// payload cannot be rendered inline by the browser.
func traceObjectDisposition(mode string, contentType string) string {
	previewable := strings.HasPrefix(contentType, "audio/") ||
		strings.HasPrefix(contentType, "image/") ||
		strings.HasPrefix(contentType, "video/")
	if mode == "preview" && previewable {
		return "inline"
	}
	return "attachment"
}

// GetRequestTraceObject streams a binary payload held in object storage.
//
// The object key is read from the trace row rather than taken from the caller,
// so an administrator cannot walk the bucket through this endpoint.
func GetRequestTraceObject(c *gin.Context) {
	traceId := c.Query("trace_id")
	seq, err := strconv.Atoi(c.Query("seq"))
	if traceId == "" || err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "缺少 trace_id 或 seq"})
		return
	}
	trace, err := model.GetRequestTraceLeg(traceId, seq)
	if err != nil || trace == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "追踪记录不存在或已过期"})
		return
	}
	if trace.ObjectKey == "" {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "该环节没有存储的报文内容"})
		return
	}

	body, stored, err := service.GetObject(c.Request.Context(), trace.ObjectKey)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	defer body.Close()

	contentType := trace.ContentType
	if contentType == "" {
		contentType = stored.ContentType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	filename := fmt.Sprintf("trace-%s-%d%s", traceId, seq, traceObjectExtension(contentType))
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q",
		traceObjectDisposition(c.Query("mode"), contentType), filename))
	c.Header("Cache-Control", "private, no-store")
	if stored.Size > 0 {
		c.Header("Content-Length", strconv.FormatInt(stored.Size, 10))
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, body); err != nil {
		logger.LogWarn(c.Request.Context(), "request trace payload download interrupted: "+err.Error())
	}
}

// traceObjectExtension gives a downloaded payload a name the operating system
// can open. Only the media types a relay actually returns are mapped.
func traceObjectExtension(contentType string) string {
	base, _, _ := strings.Cut(contentType, ";")
	switch strings.TrimSpace(strings.ToLower(base)) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	case "audio/opus":
		return ".opus"
	case "audio/aac":
		return ".aac"
	case "audio/flac":
		return ".flac"
	case "audio/pcm":
		return ".pcm"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "multipart/form-data":
		return ".multipart"
	default:
		return ".bin"
	}
}
