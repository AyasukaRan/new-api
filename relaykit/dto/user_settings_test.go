package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IP 记录默认开启，但用户关掉之后必须一直保持关闭。这两件事只有在
// “缺失”和“显式 false” 可区分时才能同时成立，所以这里覆盖三种取值。
func TestShouldRecordIpDefaultsOnAndHonoursAnExplicitOptOut(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		expected bool
	}{
		{name: "从未设置过的账号默认记录", settings: `{}`, expected: true},
		{name: "显式关闭的账号不记录", settings: `{"record_ip_log":false}`, expected: false},
		{name: "显式开启的账号记录", settings: `{"record_ip_log":true}`, expected: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var settings UserSetting
			require.NoError(t, json.Unmarshal([]byte(testCase.settings), &settings))
			assert.Equal(t, testCase.expected, settings.ShouldRecordIp())
		})
	}
}

// 关闭状态要能存活一次写回：omitempty 作用在指针上，&false 会被写成 false，
// 而不是像普通 bool 那样被整个丢掉、下次读出来又变回默认开启。
func TestExplicitOptOutSurvivesAMarshalRoundTrip(t *testing.T) {
	disabled := false
	encoded, err := json.Marshal(UserSetting{RecordIpLog: &disabled})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"record_ip_log":false`)

	var decoded UserSetting
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.False(t, decoded.ShouldRecordIp())
}
