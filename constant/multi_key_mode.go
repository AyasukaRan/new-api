package constant

type MultiKeyMode string

const (
	MultiKeyModeWeightedUsage MultiKeyMode = "weighted_usage"
	MultiKeyModeRandom        MultiKeyMode = "random"   // 随机
	MultiKeyModePolling       MultiKeyMode = "polling"  // 轮询
	MultiKeyModeFailover      MultiKeyMode = "failover" // 按保存顺序主备降级
)

// UsesKeyFailover keeps healthy sessions pinned and skips failed credentials.
func (mode MultiKeyMode) UsesKeyFailover() bool {
	return mode == MultiKeyModeFailover || mode == MultiKeyModeWeightedUsage
}
