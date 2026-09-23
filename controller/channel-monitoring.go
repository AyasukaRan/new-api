package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/gin-gonic/gin"
)

// GetChannelMonitoring combines account balances with gateway usage. It is
// restricted to administrators with channel read permission by the router.
func GetChannelMonitoring(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	hours, err := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if err != nil || hours < 1 || hours > 168 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "hours must be between 1 and 168"})
		return
	}
	channel, err := model.GetChannelById(id, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.PopulateChannelBalanceMonitors([]*model.Channel{channel}); err != nil {
		common.ApiError(c, err)
		return
	}
	end := time.Now().Unix()
	history, err := model.ListChannelBalanceSamples(id, end-int64(hours)*3600, end, 1000)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	usage, err := perfmetrics.QueryChannelUsage(id, hours)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"balance": channel.BalanceMonitor, "balance_history": history, "usage": usage, "used_quota": channel.UsedQuota})
}
