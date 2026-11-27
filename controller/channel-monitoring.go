package controller

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/keyusage"
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
	if err != nil || hours < 1 || hours > 720 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "hours must be between 1 and 720"})
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.PopulateChannelBalanceMonitors([]*model.Channel{channel}); err != nil {
		common.ApiError(c, err)
		return
	}
	end := time.Now().Unix()
	history, err := model.ListChannelBalanceSamples(id, end-int64(hours)*3600, end, 10000)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	segment := 0
	previousConfiguration := ""
	for i := range history {
		if i == 0 || history[i].ConfigurationHash != previousConfiguration {
			segment++
		}
		history[i].ConfigurationSegment = segment
		previousConfiguration = history[i].ConfigurationHash
	}
	usage, err := perfmetrics.QueryChannelUsage(id, hours)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	report, reportErr := keyusage.Query(ctx, id, hours)
	keyReport := channelKeyUsageReport{WindowStart: report.WindowStart, WindowEnd: report.WindowEnd, ObservedSince: report.ObservedSince, Keys: []channelKeyUsageView{}, Series: report.Series}
	if keyReport.Series == nil {
		keyReport.Series = []keyusage.SeriesPoint{}
	}
	if reportErr != nil {
		keyReport.Error = "Key usage statistics are temporarily unavailable."
		keyReport.WindowStart, keyReport.WindowEnd = end-int64(hours)*3600, end
	}
	configuration, _, configErr := channel.KeyConfiguration()
	if configErr != nil {
		common.ApiError(c, configErr)
		return
	}
	stats := make(map[string]keyusage.KeyStats)
	for _, row := range report.Keys {
		stats[row.KeyIdentity] = row
	}
	var totalWeight int
	for _, key := range configuration {
		if key.Status == common.ChannelStatusEnabled {
			totalWeight += key.RoutingWeight
		}
	}
	for _, key := range configuration {
		row := stats[key.KeyIdentity]
		row.KeyIdentity = key.KeyIdentity
		share := 0.0
		if totalWeight > 0 && key.Status == common.ChannelStatusEnabled {
			share = float64(key.RoutingWeight) / float64(totalWeight)
		}
		keyReport.Keys = append(keyReport.Keys, channelKeyUsageView{KeyStats: row, KeyIndex: key.KeyIndex, KeyPreview: key.KeyPreview, Weight: key.RoutingWeight, TargetShare: share})
		delete(stats, key.KeyIdentity)
	}
	for _, row := range report.Keys {
		if _, exists := stats[row.KeyIdentity]; exists {
			keyReport.Keys = append(keyReport.Keys, channelKeyUsageView{KeyStats: row, KeyIndex: -1})
		}
	}
	multiplier := 1.0
	if configured := channel.GetSetting().BalanceRechargeMultiplier; configured != nil && *configured > 0 && !math.IsNaN(*configured) && !math.IsInf(*configured, 0) {
		multiplier = *configured
	}
	common.ApiSuccess(c, gin.H{"balance": channel.BalanceMonitor, "balance_history": history, "usage": usage, "used_quota": channel.UsedQuota, "key_usage": keyReport, "balance_currency": channelBalanceUnit(channel), "balance_recharge_multiplier": multiplier, "balance_detail": channel.GetBalanceDetail()})
}

type channelKeyUsageView struct {
	keyusage.KeyStats
	KeyIndex    int     `json:"key_index"`
	KeyPreview  string  `json:"key_preview"`
	Weight      int     `json:"weight"`
	TargetShare float64 `json:"target_share"`
}

type channelKeyUsageReport struct {
	WindowStart   int64                  `json:"window_start"`
	WindowEnd     int64                  `json:"window_end"`
	ObservedSince int64                  `json:"observed_since"`
	Keys          []channelKeyUsageView  `json:"keys"`
	Series        []keyusage.SeriesPoint `json:"series"`
	Error         string                 `json:"error,omitempty"`
}
