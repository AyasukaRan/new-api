package controller

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
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
	usable := service.GetUserVisibleGroups(c.GetString("user_group"))
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
	scope := monitorScopeForViewer(c, hours)
	var result perfmetrics.SummaryAllResult
	var err error
	if c.GetInt("role") >= common.RoleAdminUser {
		result, err = perfmetrics.QuerySummaryAll(hours, scope.AllowedGroups, recentModels...)
	} else {
		result, err = perfmetrics.QueryVisibleSummary(scope, recentModels...)
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
