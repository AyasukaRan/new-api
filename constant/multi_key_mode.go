package constant

type MultiKeyMode string

const (
	MultiKeyModeRandom   MultiKeyMode = "random"   // 随机
	MultiKeyModePolling  MultiKeyMode = "polling"  // 轮询
	MultiKeyModeFailover MultiKeyMode = "failover" // 按保存顺序主备降级
)
