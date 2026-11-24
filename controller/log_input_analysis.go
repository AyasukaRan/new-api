package controller

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetLogInputAnalysis reads only the recorded ingress. A relay can append
// private instructions upstream, so an upstream leg is never a substitute.
func GetLogInputAnalysis(c *gin.Context) {
	traceID, ref := c.Query("trace_id"), c.Query("ref")
	createdAt, err := strconv.ParseInt(c.Query("created_at"), 10, 64)
	decoded, refErr := hex.DecodeString(ref)
	if len(traceID) > 64 || (ref == "" && traceID == "") || (ref != "" && (refErr != nil || len(decoded) != 32 || err != nil || createdAt <= 0)) {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	ctx := c.Request.Context()
	originalModel := c.Query("model")
	if len(originalModel) > 192 {
		originalModel = ""
	}
	analysis := service.AnalyzeCapturedInput(ctx, "", "", "", originalModel, "none", false)
	if traceID != "" {
		// FinishRequestTrace always records the client request at sequence 0.
		leg, err := model.GetRequestTraceLegContext(ctx, traceID, 0)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			common.ApiError(c, err)
			return
		}
		if leg != nil && leg.Direction == model.TraceDirectionClientRequest {
			if leg.ConversationModel != "" {
				originalModel = leg.ConversationModel
			}
			analysis = service.AnalyzeCapturedInput(ctx, leg.Body, leg.Format, leg.Url, originalModel, "trace", leg.Truncated)
		}
	}
	if ref != "" && (analysis.Source == "none" || analysis.Status == "unavailable" || analysis.HasReason("truncated")) && ctx.Err() == nil {
		capture, err := model.GetLogRequestBodyCapture(ctx, createdAt, c.Query("request_id"), ref)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if capture.Body != "" {
			path := capture.RequestPath
			if path == "" {
				path = c.Query("request_path")
			}
			fallback := service.AnalyzeCapturedInput(ctx, capture.Body, "", path, originalModel, "log_body", capture.Truncated)
			if analysis.Status == "unavailable" || fallback.Status == "complete" || (!capture.Truncated && fallback.Status == "partial") || (analysis.HasReason("truncated") && fallback.EstimatedTokens > analysis.EstimatedTokens) {
				analysis = fallback
			}
		}
	}
	if ctx.Err() == nil {
		common.ApiSuccess(c, analysis)
	}
}
