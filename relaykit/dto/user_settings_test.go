package dto

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyIPPreferenceIsDiscarded(t *testing.T) {
	var settings UserSetting
	require.NoError(t, kitutil.Unmarshal([]byte(`{"record_ip_log":false,"language":"vi"}`), &settings))
	raw, err := kitutil.Marshal(settings)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "record_ip_log")
	assert.Contains(t, string(raw), `"language":"vi"`)
}
