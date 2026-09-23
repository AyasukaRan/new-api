package operation_setting

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes float64 `json:"auto_test_channel_minutes"`
	ChannelTestMode        string  `json:"channel_test_mode"`
	ChannelTestConcurrency int     `json:"channel_test_concurrency"`
	// 定时向上游查询渠道余额。余额接口通常有配额，默认关闭，间隔也比测活长得多。
	AutoUpdateBalanceEnabled bool    `json:"auto_update_balance_enabled"`
	AutoUpdateBalanceMinutes float64 `json:"auto_update_balance_minutes"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModeAutoBanOnly     = "auto_ban_only"
	ChannelTestModePassiveRecovery = "passive_recovery"

	ChannelTestConcurrencyOptionKey = "monitor_setting.channel_test_concurrency"
	DefaultChannelTestConcurrency   = 1
	MaxChannelTestConcurrency       = 32

	DefaultAutoUpdateBalanceMinutes   = 60
	MaxAutoUpdateBalanceMinutes       = 7 * 24 * 60
	AutoUpdateBalanceMinutesOptionKey = "monitor_setting.auto_update_balance_minutes"
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:   false,
	AutoTestChannelMinutes:   10,
	ChannelTestMode:          ChannelTestModeScheduledAll,
	ChannelTestConcurrency:   DefaultChannelTestConcurrency,
	AutoUpdateBalanceEnabled: false,
	AutoUpdateBalanceMinutes: DefaultAutoUpdateBalanceMinutes,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
			monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			monitorSetting.AutoTestChannelEnabled = parsed
		}
	}
	// CHANNEL_UPDATE_FREQUENCY 是余额定时刷新最早的开关，保留它以免升级后
	// 既有部署的余额刷新突然停掉。
	if os.Getenv("CHANNEL_UPDATE_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_UPDATE_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoUpdateBalanceEnabled = true
			monitorSetting.AutoUpdateBalanceMinutes = float64(frequency)
		}
	}
	if math.IsNaN(monitorSetting.AutoUpdateBalanceMinutes) || math.IsInf(monitorSetting.AutoUpdateBalanceMinutes, 0) || monitorSetting.AutoUpdateBalanceMinutes < 1 || monitorSetting.AutoUpdateBalanceMinutes > MaxAutoUpdateBalanceMinutes {
		monitorSetting.AutoUpdateBalanceMinutes = DefaultAutoUpdateBalanceMinutes
	}
	switch monitorSetting.ChannelTestMode {
	case ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
	default:
		monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	monitorSetting.ChannelTestConcurrency = NormalizeChannelTestConcurrency(monitorSetting.ChannelTestConcurrency)
	return &monitorSetting
}

// ChannelTestInterval is shared by idle deadlines and request activity leases.
func ChannelTestInterval() time.Duration {
	// Request activity reads this concurrently. Do not call the legacy getter,
	// which also normalizes and writes unrelated monitor settings on each call.
	minutes := float64(10)
	if frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY")); err == nil && frequency > 0 {
		minutes = float64(frequency)
	} else {
		config.GlobalConfig.Read("monitor_setting", func(value interface{}) {
			minutes = value.(*MonitorSetting).AutoTestChannelMinutes
		})
	}
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 || minutes >= float64(math.MaxInt64)/float64(time.Minute) {
		return 10 * time.Minute
	}
	return max(time.Second, time.Duration(minutes*float64(time.Minute)))
}

func ValidateAutoUpdateBalanceMinutes(value string) error {
	minutes, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 1 || minutes > MaxAutoUpdateBalanceMinutes {
		return fmt.Errorf("balance update interval must be between 1 and %d minutes", MaxAutoUpdateBalanceMinutes)
	}
	return nil
}

func NormalizeChannelTestConcurrency(concurrency int) int {
	if concurrency < 1 {
		return DefaultChannelTestConcurrency
	}
	if concurrency > MaxChannelTestConcurrency {
		return MaxChannelTestConcurrency
	}
	return concurrency
}

func ValidateChannelTestConcurrency(value string) error {
	concurrency, err := strconv.Atoi(value)
	if err != nil || concurrency < 1 || concurrency > MaxChannelTestConcurrency {
		return fmt.Errorf("channel test concurrency must be between 1 and %d", MaxChannelTestConcurrency)
	}
	return nil
}
