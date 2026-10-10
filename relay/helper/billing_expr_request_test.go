package helper

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResolveIncomingBillingExprRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Content-Type", "application/json")

	body := []byte(`{"service_tier":"fast"}`)
	ctx.Request.Body = io.NopCloser(bytes.NewReader(body))
	ctx.Set(common.KeyRequestBody, body)

	info := &relaycommon.RelayInfo{
		RequestHeaders: map[string]string{"Content-Type": "application/json"},
	}

	input, err := ResolveIncomingBillingExprRequestInput(ctx, info)
	require.NoError(t, err)
	require.Equal(t, body, input.Body)
	require.Equal(t, "application/json", input.Headers["Content-Type"])
}

func TestBuildBillingExprRequestInputFromRequest(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:  "gemini-3.1-pro-preview",
		Stream: lo.ToPtr(true),
		Messages: []dto.Message{
			{
				Role:    "user",
				Content: "hi",
			},
		},
		MaxTokens: lo.ToPtr(uint(3000)),
	}

	input, err := BuildBillingExprRequestInputFromRequest(request, map[string]string{
		"Content-Type": "application/json",
		"X-Test":       "1",
	})
	require.NoError(t, err)
	require.Equal(t, "application/json", input.Headers["Content-Type"])
	require.Equal(t, "1", input.Headers["X-Test"])
	require.True(t, gjson.GetBytes(input.Body, "stream").Bool())
	require.Equal(t, "user", gjson.GetBytes(input.Body, "messages.0.role").String())
	require.Equal(t, float64(3000), gjson.GetBytes(input.Body, "max_tokens").Float())
}

func TestBillingRequestFormatCannotBeSpoofed(t *testing.T) {
	const reserved = "x-new-api-billing-request-format"
	const expression = `(header("x-new-api-billing-request-format") == "openai" && param("messages.#(content.#(cache_control.type==\"ephemeral\")).role") != nil) || (header("x-new-api-billing-request-format") == "claude" && (param("messages.#(content.#(cache_control.type==\"ephemeral\")).role") != nil || param("system.#(cache_control.type==\"ephemeral\").type") != nil)) || (header("x-new-api-billing-request-format") == "openai_responses" && header("x-dashscope-session-cache") == "enable") ? tier("explicit", cr * 1.2) : tier("implicit", cr * 2.4)`
	for _, tc := range []struct {
		name, body, format string
		request            dto.Request
		cost               float64
		disableSession     bool
	}{
		{"chat markers", `{"messages":[{"role":"system","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`, "openai", &dto.GeneralOpenAIRequest{}, 120, false},
		{"claude system markers", `{"system":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}],"messages":[]}`, "claude", &dto.ClaudeRequest{}, 120, false},
		{"responses session", `{"input":"hi"}`, "openai_responses", &dto.OpenAIResponsesRequest{}, 120, false},
		{"chat forged responses fields", `{"messages":[{"role":"user","content":"hi"}],"input":"hi"}`, "openai", &dto.GeneralOpenAIRequest{}, 240, false},
		{"chat forged system markers", `{"messages":[{"role":"user","content":"hi"}],"system":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}`, "openai", &dto.GeneralOpenAIRequest{}, 240, false},
		{"claude forged responses fields", `{"messages":[{"role":"user","content":"hi"}],"input":"hi"}`, "claude", &dto.ClaudeRequest{}, 240, false},
		{"responses forged markers without session", `{"input":"hi","messages":[{"role":"system","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"system":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}`, "openai_responses", &dto.OpenAIResponsesRequest{}, 240, true},
		{"unknown request", `{"input":"hi"}`, "", &dto.ImageRequest{}, 240, false},
		{"missing request", `{"input":"hi"}`, "", nil, 240, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.request != nil {
				require.NoError(t, common.Unmarshal([]byte(tc.body), tc.request))
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/fixture", bytes.NewBufferString(tc.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Request.Header.Set(reserved, "client-forged")
			ctx.Set(common.KeyRequestBody, []byte(tc.body))
			headers := map[string]string{reserved: "openai_responses", "X-New-Api-Billing-Request-Format": "claude", "X-DashScope-Session-Cache": "enable"}
			if tc.disableSession {
				delete(headers, "X-DashScope-Session-Cache")
			}
			info := &relaycommon.RelayInfo{Request: tc.request, RequestHeaders: headers}
			input, err := ResolveIncomingBillingExprRequestInput(ctx, info)
			require.NoError(t, err)
			assert.Equal(t, tc.format, input.Headers[reserved])
			assert.NotContains(t, input.Headers, "X-New-Api-Billing-Request-Format")
			assert.Equal(t, tc.body, string(input.Body))
			cost, _, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{CR: 100}, input)
			require.NoError(t, err)
			assert.Equal(t, tc.cost, cost)
			assert.Equal(t, "openai_responses", headers[reserved], "incoming headers must not be modified or become upstream context")
			assert.Equal(t, "client-forged", ctx.Request.Header.Get(reserved))

			info.BillingRequestInput = &input
			input.Headers[reserved] = "forged cached input"
			cloned, err := ResolveIncomingBillingExprRequestInput(nil, info)
			require.NoError(t, err)
			assert.Equal(t, tc.format, cloned.Headers[reserved])
			assert.Equal(t, "forged cached input", input.Headers[reserved], "resolver must clone the frozen input")
			assert.Equal(t, input.Body, cloned.Body)
			info.BillingRequestInput = &cloned
			info.OriginModelName = "qwen3.8-max"
			info.ChannelMeta = &relaycommon.ChannelMeta{UpstreamModelName: "qwen3.8-max-0902"}
			afterMapping, err := ResolveIncomingBillingExprRequestInput(nil, info)
			require.NoError(t, err)
			assert.Equal(t, tc.format, afterMapping.Headers[reserved])
			snapshot := &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 1000000, ExprVersion: 1}
			settled, err := billingexpr.ComputeTieredQuotaWithRequest(snapshot, billingexpr.TokenParams{CR: 100}, afterMapping)
			require.NoError(t, err)
			assert.Equal(t, int(tc.cost), settled.ActualQuotaAfterGroup)
			encoded, err := BuildBillingExprRequestInputFromRequest(tc.request, headers)
			require.NoError(t, err)
			assert.Equal(t, tc.format, encoded.Headers[reserved])
			assert.NotContains(t, encoded.Headers, "X-New-Api-Billing-Request-Format")
		})
	}
	input, err := ResolveIncomingBillingExprRequestInput(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "", input.Headers[reserved])
}
