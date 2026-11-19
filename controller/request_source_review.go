package controller

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetBuiltinRequestSourceRules(c *gin.Context) {
	common.ApiSuccess(c, service.BuiltinRequestSourceRules())
}

func UpdateBuiltinRequestSourceRule(c *gin.Context) {
	var input struct {
		Name        string `json:"name"`
		Enabled     *bool  `json:"enabled"`
		DisplayName string `json:"display_name"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil || !slices.ContainsFunc(service.BuiltinRequestSourceRules(), func(rule service.BuiltinRequestSourceRule) bool {
		return rule.Name == input.Name
	}) {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	value, err := common.Marshal(operation_setting.BuiltinRequestSourceSetting{Enabled: *input.Enabled, DisplayName: input.DisplayName})
	if err == nil {
		err = model.UpdateOption(operation_setting.BuiltinRequestSourceOptionPrefix+input.Name, string(value))
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func GetUnidentifiedRequestSources(c *gin.Context) {
	days, err := strconv.Atoi(c.DefaultQuery("days", "7"))
	now := time.Now().Unix()
	cursor := service.SourceReviewCursor{Days: days, Snapshot: now}
	valid := err == nil && days >= 1 && days <= 30
	if token := c.Query("cursor"); token != "" {
		if len(token) > 512 {
			valid = false
		} else {
			raw, e := base64.RawURLEncoding.DecodeString(token)
			valid = valid && e == nil && common.Unmarshal(raw, &cursor) == nil && cursor.Days == days && cursor.Snapshot > 0 &&
				cursor.Snapshot <= now && cursor.Time >= cursor.Snapshot-int64(days)*86400 && cursor.Time > 0 && cursor.Time <= cursor.Snapshot &&
				len(cursor.RequestID) <= 64 && len(cursor.TraceID) <= 64 && cursor.Skipped >= 0 && (cursor.Skipped == 0 || cursor.RequestID == "" && cursor.TraceID == "")
		}
	}
	if !valid {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	result, err := service.ReviewUnidentifiedRequestSources(c.Request.Context(), cursor.Snapshot, days, 500, cursor.SourceReviewPosition)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}
