package controller

import (
	"errors"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetPersonalDashboard(c *gin.Context) {
	result, err := model.GetPersonalDashboard(c.GetInt("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.ApiErrorStatus(c, http.StatusUnauthorized, common.NewMessage("Unauthorized"), nil)
			return
		}
		common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Failed to load dashboard configuration"), nil)
		return
	}
	common.ApiSuccess(c, result)
}

func SavePersonalDashboard(c *gin.Context) {
	if common.GetEnvOrDefaultBool("DASHBOARD_CONFIG_READ_ONLY", false) {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Please try again later."), nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, dto.MaxDashboardConfigBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid dashboard configuration"), nil)
		return
	}
	var input map[string]common.RawMessage
	if common.Unmarshal(body, &input) != nil || len(input) != 2 || len(input["revision"]) == 0 || len(input["config"]) == 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid dashboard configuration"), nil)
		return
	}
	var revision *int64
	var config *dto.DashboardConfig
	if common.Unmarshal(input["revision"], &revision) != nil || revision == nil || common.Unmarshal(input["config"], &config) != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid dashboard configuration"), nil)
		return
	}
	result, err := model.SavePersonalDashboard(c.GetInt("id"), *revision, config)
	if err != nil {
		status, message := http.StatusInternalServerError, "Failed to save dashboard configuration"
		switch {
		case errors.Is(err, dto.ErrDashboardConfigInvalid):
			status, message = http.StatusBadRequest, "Invalid dashboard configuration"
		case errors.Is(err, model.ErrDashboardRevisionConflict):
			status, message = http.StatusConflict, "Dashboard changed in another tab. Reload before saving."
		case errors.Is(err, gorm.ErrRecordNotFound):
			status, message = http.StatusUnauthorized, "Unauthorized"
		}
		common.ApiErrorStatus(c, status, common.NewMessage(message), nil)
		return
	}
	common.ApiSuccess(c, result)
}
