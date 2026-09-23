package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetChannelBaseURLIsBoundsSafe(t *testing.T) {
	assert.Empty(t, GetChannelBaseURL(ChannelTypeTaskPlugin))
	assert.Empty(t, GetChannelBaseURL(9999))
}

func TestStoredMaaSChannelRemainsDistinctFromInferenceProviders(t *testing.T) {
	// Type 62 already exists in deployed channel records.
	assert.Equal(t, "iFlytek MaaS", GetChannelTypeName(62))
	assert.Equal(t, "https://maas-api.cn-huabei-1.xf-yun.com", GetChannelBaseURL(62))
	assert.Equal(t, "https://spark-api-open.xf-yun.com", GetChannelBatchBaseURL(62))
	assert.False(t, IsAdvancedCustomChannel(62))
	for _, channelType := range []int{ChannelTypeVLLM, ChannelTypeSGLang} {
		assert.True(t, IsAdvancedCustomChannel(channelType))
		assert.Empty(t, GetChannelBatchBaseURL(channelType))
	}
	assert.NotEqual(t, ChannelTypeVLLM, ChannelTypeSGLang)
}
