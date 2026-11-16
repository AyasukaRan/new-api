package helper

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMaxTokensBounds guards the billing invariant that user-supplied max
// token fields are bounded on every relay format. These values feed
// pre-consume quota math (preConsumedTokens * ratio); a huge or
// wrapped-negative value (e.g. 18446744073686646784 parsed into *uint) must
// be rejected at validation instead of corrupting the pre-charge.
func TestMaxTokensBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newJSONContext := func(t *testing.T, body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}

	const hugeN = "18446744073686646784"

	t.Run("openai max_tokens overflow rejected", func(t *testing.T) {
		c := newJSONContext(t, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":`+hugeN+`}`)
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_tokens is invalid")
	})

	t.Run("openai max_completion_tokens overflow rejected", func(t *testing.T) {
		c := newJSONContext(t, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":`+hugeN+`}`)
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_tokens is invalid")
	})

	t.Run("claude max_tokens overflow rejected", func(t *testing.T) {
		c := newJSONContext(t, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"max_tokens":`+hugeN+`}`)
		_, err := GetAndValidateClaudeRequest(c)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_tokens is invalid")
	})

	t.Run("claude normal max_tokens accepted", func(t *testing.T) {
		c := newJSONContext(t, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"max_tokens":8192}`)
		req, err := GetAndValidateClaudeRequest(c)
		require.NoError(t, err)
		require.EqualValues(t, 8192, *req.MaxTokens)
	})

	t.Run("gemini maxOutputTokens overflow rejected", func(t *testing.T) {
		c := newJSONContext(t, `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":`+hugeN+`}}`)
		_, err := GetAndValidateGeminiRequest(c)
		require.Error(t, err)
		require.Contains(t, err.Error(), "maxOutputTokens is invalid")
	})

	t.Run("responses max_output_tokens overflow rejected", func(t *testing.T) {
		c := newJSONContext(t, `{"model":"gpt-4o","input":"hi","max_output_tokens":`+hugeN+`}`)
		_, err := GetAndValidateResponsesRequest(c)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_output_tokens is invalid")
	})
}

func TestResponsesInstructionsOnlyRequest(t *testing.T) {
	for _, tc := range []struct {
		name, fields, wantError string
	}{
		{name: "instructions only", fields: `"instructions":"Say hello"`},
		{name: "instructions with zero output limit", fields: `"instructions":"Say hello","max_output_tokens":0`},
		{name: "instructions with excessive output limit", fields: `"instructions":"Say hello","max_output_tokens":18446744073686646784`, wantError: "max_output_tokens is invalid"},
		{name: "missing input and instructions", fields: `"stream":false`, wantError: "input is required"},
		{name: "null instructions", fields: `"instructions":null`, wantError: "input is required"},
		{name: "blank instructions", fields: `"instructions":"  "`, wantError: "input is required"},
		{name: "non-string instructions", fields: `"instructions":{"text":"Say hello"}`, wantError: "input is required"},
		{name: "ordinary input", fields: `"input":"Hello"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"deepseek-flash",`+tc.fields+`}`))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			request, err := GetAndValidateResponsesRequest(c)
			if tc.wantError != "" {
				require.EqualError(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			wire, err := common.Marshal(request)
			require.NoError(t, err)
			assert.JSONEq(t, `{"model":"deepseek-flash",`+tc.fields+`}`, string(wire))
		})
	}
}

func TestSGLangMinTokensBounds(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"0", true}, {"8192", true}, {"-1", false}, {"18446744073686646784", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"served-model","messages":[{"role":"user","content":"hi"}],"min_tokens":`+tc.value+`}`))
			c.Request.Header.Set("Content-Type", "application/json")
			request, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, request.MinTokens)
		})
	}
}
