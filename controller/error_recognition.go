package controller

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func PreviewErrorRecognition(c *gin.Context) {
	var input struct {
		Rules      common.RawMessage `json:"rules"`
		Error      string            `json:"error"`
		StatusCode int               `json:"status_code"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Error) > 16384 || (input.StatusCode != 0 && (input.StatusCode < 400 || input.StatusCode > 599)) {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	var rules *string
	if len(input.Rules) > 0 {
		value := string(input.Rules)
		rules = &value
	}
	result, err := operation_setting.PreviewErrorRecognition(rules, input.Error, input.StatusCode)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	common.ApiSuccess(c, result)
}
func GetErrorRecognitionInventory(c *gin.Context) {
	days, err := strconv.Atoi(c.DefaultQuery("days", "7"))
	now := time.Now().Unix()
	cursor := model.ErrorInventoryCursor{Snapshot: now, Days: days}
	valid := err == nil && (days == 0 || days == 1 || days == 7 || days == 30)
	if token := c.Query("cursor"); token != "" {
		if len(token) > 512 {
			valid = false
		} else {
			raw, e := base64.RawURLEncoding.DecodeString(token)
			valid = valid && e == nil && common.Unmarshal(raw, &cursor) == nil && cursor.Days == days && cursor.Snapshot > 0 && cursor.Snapshot <= now && cursor.Time > 0 && cursor.Time <= cursor.Snapshot && len(cursor.RequestID) <= 64 && cursor.ID >= 0 && cursor.Skipped >= 0 && cursor.Skipped <= 10000000
		}
	}
	if !valid {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	report, _, err := model.ReviewRequestErrors(c.Request.Context(), cursor, 200)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, report)
}
