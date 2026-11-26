package controller

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func monitorScopeForViewer(c *gin.Context, hours int) perfmetrics.QueryParams {
	params := perfmetrics.QueryParams{Hours: hours}
	if c.GetInt("role") >= common.RoleAdminUser {
		params.AllowedGroups = append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
		return params
	}
	usable := service.GetAccountVisibleGroups(c.GetInt("id"), common.GetContextKeyString(c, constant.ContextKeyUserGroup))
	params.AllowedGroups = lo.Keys(usable)
	if len(params.AllowedGroups) == 0 {
		return params
	}
	params.VisibleModels = make(map[string]bool)
	catalog := model.GetPricing()
	for i := range catalog {
		item := &catalog[i]
		for _, group := range item.EnableGroup {
			if _, ok := usable[group]; ok || group == "all" {
				params.VisibleModels[item.ModelName] = true
				break
			}
		}
	}
	return params
}

func GetAdminPerfMetrics(c *gin.Context) {
	modelName := strings.TrimSpace(c.Query("model"))
	if modelName == "" || utf8.RuneCountInString(modelName) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "a valid model is required"})
		return
	}
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	result, err := perfmetrics.QueryAdminModel(modelName, hours)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "unable to read channel monitoring"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func GetPerfMetricsSummary(c *gin.Context) {
	getPerfMetricsSummary(c, c.GetInt("role") >= common.RoleAdminUser)
}

func GetDashboardPerfMetricsSummary(c *gin.Context) {
	getPerfMetricsSummary(c, true)
}

func getPerfMetricsSummary(c *gin.Context, dashboard bool) {
	hours := 24
	if rawHours := c.Query("hours"); rawHours != "" {
		if parsed, err := strconv.Atoi(rawHours); err == nil {
			hours = parsed
		}
	}

	recentModels := c.QueryArray("recent_model")
	if len(recentModels) > perfmetrics.MaxRecentModels {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	var result perfmetrics.SummaryAllResult
	var err error
	if dashboard {
		diagnostics, _ := strconv.ParseBool(c.Query("diagnostics"))
		groups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
		result, err = perfmetrics.QueryAdminSummary(hours, groups, diagnostics, recentModels...)
	} else {
		result, err = perfmetrics.QueryVisibleSummary(monitorScopeForViewer(c, hours), recentModels...)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

func GetPerfMetrics(c *gin.Context) {
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "model is required",
		})
		return
	}

	hours := 24
	if rawHours := c.Query("hours"); rawHours != "" {
		if parsed, err := strconv.Atoi(rawHours); err == nil {
			hours = parsed
		}
	}

	params := monitorScopeForViewer(c, hours)
	params.Model = modelName
	params.Group = c.Query("group")
	query := perfmetrics.QueryVisible
	if c.GetInt("role") >= common.RoleAdminUser {
		query = perfmetrics.Query
	}
	result, err := query(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	if params.Group != "" && !slices.Contains(params.AllowedGroups, params.Group) {
		result.AvailabilityRate = nil
		result.AvailabilitySeries = nil
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

// GetLiveDashboardMetrics exposes only administrator-visible single-instance
// measurements. The route also enforces AdminAuth and the log:read PAT scope.
func GetLiveDashboardMetrics(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("Unauthorized, insufficient privileges"))
		return
	}
	seconds, secondsErr := strconv.ParseInt(c.DefaultQuery("seconds", "900"), 10, 64)
	step, stepErr := strconv.ParseInt(c.DefaultQuery("step", "60"), 10, 64)
	modelName := strings.TrimSpace(c.Query("model"))
	if secondsErr != nil || stepErr != nil || !slices.Contains([]int64{900, 3600, 21600, 86400}, seconds) ||
		!slices.Contains([]int64{60, 300, 3600}, step) || utf8.RuneCountInString(modelName) > 128 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	result, accepted := perfmetrics.TryQueryLive(seconds, step, modelName)
	if !accepted {
		c.Header("Retry-After", "1")
		common.ApiErrorStatus(c, http.StatusTooManyRequests, common.NewMessage("Rate limited"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
