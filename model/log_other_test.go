package model

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogOtherScopesAndMerges(t *testing.T) {
	var other LogOther

	assert.True(t, other.SetPublic("request_path", "/v1/chat/completions"))
	other.MergePublic(map[string]any{
		"zero": 0,
	})
	assert.True(t, other.SetAdmin("use_channel", []string{"channel-a"}))
	other.MergeAdmin(map[string]any{
		"rejected": false,
	})
	assert.True(t, other.SetRoot("upstream_request_id", "upstream-private"))
	other.MergeRoot(map[string]any{
		"generation": 0,
	})
	assert.True(t, other.SetAudit("method", "POST"))
	other.MergeAudit(map[string]any{
		"success": false,
	})

	require.JSONEq(t, `{
		"request_path": "/v1/chat/completions",
		"zero": 0,
		"admin_info": {
			"use_channel": ["channel-a"],
			"rejected": false
		},
		"root_info": {
			"upstream_request_id": "upstream-private",
			"generation": 0
		},
		"audit_info": {
			"method": "POST",
			"success": false
		}
	}`, other.JSONString())
}

func TestLogOtherRejectsSensitivePublicFields(t *testing.T) {
	other := NewLogOther()

	for _, key := range []string{
		"admin_info",
		"root_info",
		"audit_info",
		"channel_id",
		"channel_name",
		"channel_type",
		"reject_reason",
	} {
		assert.False(t, other.SetPublic(key, "must-not-leak"), key)
	}
	other.MergePublic(map[string]any{
		"request_path": "/v1/responses",
		"channel_name": "still-must-not-leak",
		"admin_info":   map[string]any{"secret": true},
	})

	require.JSONEq(t, `{"request_path":"/v1/responses"}`, other.JSONString())
	require.JSONEq(t, `{}`, NewLogOther().JSONString())
}

func TestLogOtherJSONStringDoesNotMutateReceiver(t *testing.T) {
	other := NewLogOther()
	require.True(t, other.SetPublic("request_path", "/v1/chat/completions"))
	require.True(t, other.SetAdmin("rejected", false))

	before := other.Snapshot()
	first := other.JSONString()
	after := other.Snapshot()
	second := other.JSONString()

	require.Equal(t, before, after)
	require.Equal(t, first, second)
}

func newRequestBodyContext(t *testing.T, contentType string, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Content-Type", contentType)
	storage, err := common.CreateBodyStorage([]byte(body))
	require.NoError(t, err)
	t.Cleanup(func() { storage.Close() })
	ctx.Set(common.KeyBodyStorage, storage)
	return ctx
}

func enableRequestBodyLogging(t *testing.T) {
	t.Helper()
	original := common.LogRequestBodyEnabled
	t.Cleanup(func() { common.LogRequestBodyEnabled = original })
	common.LogRequestBodyEnabled = true
}

// Admins asked to see what users send; nesting the payload under admin_info is
// what keeps it out of every user-facing log projection.
func TestAttachRequestBodyIsAdminOnly(t *testing.T) {
	enableRequestBodyLogging(t)
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`

	other := NewLogOther()
	attachRequestBody(newRequestBodyContext(t, "application/json", payload), other)

	assert.Equal(t, payload, other.Snapshot()[logOtherAdminInfoKey].(map[string]any)["request_body"])
	assert.NotContains(t, formatLogOtherJSON(other.JSONString(), logOtherVisibilityUser), "request_body")
	assert.Contains(t, formatLogOtherJSON(other.JSONString(), logOtherVisibilityAdmin), "request_body")
}

func TestAttachRequestBodySkipsWhatItMustNotStore(t *testing.T) {
	payload := `{"model":"gpt-4o"}`

	t.Run("capture disabled", func(t *testing.T) {
		other := NewLogOther()
		attachRequestBody(newRequestBodyContext(t, "application/json", payload), other)
		assert.Empty(t, other.Snapshot())
	})

	// Uploads would put binary in the log and blow the size cap for no audit value.
	t.Run("non-json content type", func(t *testing.T) {
		enableRequestBodyLogging(t)
		other := NewLogOther()
		attachRequestBody(newRequestBodyContext(t, "multipart/form-data; boundary=x", payload), other)
		assert.Empty(t, other.Snapshot())
	})

	// The body storage is only present once the relay has read the request; a
	// log written before that must not try to drain a consumed http.Request.
	t.Run("no cached body storage", func(t *testing.T) {
		enableRequestBodyLogging(t)
		gin.SetMode(gin.TestMode)
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		ctx.Request.Header.Set("Content-Type", "application/json")
		other := NewLogOther()
		attachRequestBody(ctx, other)
		assert.Empty(t, other.Snapshot())
	})
}

// Bodies live inline in logs.other, which the admin log list returns for every
// row, so an oversized payload is head-truncated rather than stored whole.
func TestAttachRequestBodyTruncatesOversizedPayload(t *testing.T) {
	enableRequestBodyLogging(t)
	payload := `{"p":"` + strings.Repeat("x", maxLoggedRequestBodyBytes) + `"}`

	other := NewLogOther()
	attachRequestBody(newRequestBodyContext(t, "application/json", payload), other)

	adminInfo := other.Snapshot()[logOtherAdminInfoKey].(map[string]any)
	assert.Len(t, adminInfo["request_body"], maxLoggedRequestBodyBytes)
	assert.Equal(t, int64(len(payload)), adminInfo["request_body_truncated"])
}
