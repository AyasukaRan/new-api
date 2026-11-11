package channel

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekPrivateResponseTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	service.InitHttpClient()
	for _, tc := range []struct {
		name, body, contentType, path, prefix string
		format                                types.RelayFormat
		mode, status                          int
		stream, compressed                    bool
	}{
		{name: "chat JSON", body: `{"id":"provider-response","object":"chat.completion","request_id":"provider-request","trace_id":"provider-trace","choices":[{"message":{"content":"literal provider-response","tool_calls":[{"id":"call-functional","function":{"name":"lookup","arguments":"{\"id\":\"not-a-trace\"}"}}]}}],"usage":{"prompt_tokens":17,"completion_tokens":3},"extension":9007199254740993}`, path: "id", prefix: "chatcmpl-", format: types.RelayFormatOpenAI},
		{name: "responses JSON", body: `{"id":"provider-response","object":"response","output":[{"id":"item-functional","type":"function_call","call_id":"call-functional"}],"usage":{"input_tokens":17,"output_tokens":3}}`, path: "id", prefix: "resp_", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses},
		{name: "claude JSON", body: `{"id":"provider-response","type":"message","content":[{"type":"tool_use","id":"call-functional","name":"lookup","input":{"id":"not-a-trace"}}],"usage":{"input_tokens":17,"output_tokens":3}}`, path: "id", prefix: "msg_", format: types.RelayFormatClaude},
		{name: "FIM JSON", body: `{"id":"provider-response","object":"text_completion","choices":[{"text":"result"}]}`, path: "id", prefix: "cmpl-", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeCompletions},
		{name: "search", body: `{"request_id":"provider-response","results":[{"title":"title","content":"provider-response","url":"https://example.com/?id=literal"}]}`, path: "request_id", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeToolsSearch},
		{name: "fetch", body: `{"trace_id":"provider-response","title":"title","content":"provider-response"}`, path: "trace_id", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeToolsFetch},
		{name: "gzip", body: `{"id":"provider-response","choices":[],"usage":{"prompt_tokens":17}}`, path: "id", prefix: "chatcmpl-", compressed: true, format: types.RelayFormatOpenAI},
		{name: "error", body: `{"error":{"message":"failure provider-header-id / provider-request / provider-trace","type":"invalid_request_error","code":"invalid_request","metadata":{"request_id":"provider-request","nested":{"trace_id":"provider-trace"}}}}`, status: 400, format: types.RelayFormatOpenAI},
		{name: "chat SSE", contentType: "text/event-stream", body: "id: provider-event\r\ndata: {\"id\":\"provider-response\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\ndata: {\"id\":\"provider-response\",\"choices\":[],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n", path: "id", prefix: "chatcmpl-", stream: true, format: types.RelayFormatOpenAI},
		{name: "responses SSE", contentType: "text/event-stream", body: "event: response.created\ndata: {\"type\":\"response.created\",\n" + "data: \"sequence_number\":0,\"response\":{\"id\":\"provider-response\",\"output\":[]}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"item\":{\"id\":\"item-functional\",\"call_id\":\"call-functional\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"provider-response\",\"usage\":{\"input_tokens\":17}}}\n\n", path: "response.id", prefix: "resp_", stream: true, format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses},
		{name: "claude SSE", contentType: "text/event-stream", body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"provider-response\",\"usage\":{\"input_tokens\":17}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-functional\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", path: "message.id", prefix: "msg_", stream: true, format: types.RelayFormatClaude},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				contentType := tc.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("X-Request-Id", "provider-header-id")
				w.Header().Set("X-Ds-Trace-Id", "provider-private-trace")
				w.Header().Set("Cf-Ray", "provider-edge-id")
				w.Header().Set("ETag", "provider-cache-id")
				w.Header().Set("Retry-After", "2")
				if tc.compressed {
					w.Header().Set("Content-Encoding", "gzip")
				}
				status := tc.status
				if status == 0 {
					status = 200
				}
				w.WriteHeader(status)
				if tc.compressed {
					compressed := gzip.NewWriter(w)
					_, _ = io.WriteString(compressed, tc.body)
					_ = compressed.Close()
				} else {
					_, _ = io.WriteString(w, tc.body)
				}
			}))
			defer upstream.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
			c.Set(common.RequestIdKey, "local-request")
			request, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader("{}"))
			require.NoError(t, err)
			if tc.compressed {
				request.Header.Set("Accept-Encoding", "gzip")
			}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek}, RelayFormat: tc.format, RelayMode: tc.mode, IsStream: tc.stream, DisablePing: true}
			response, err := doRequest(c, request, info)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, "provider-header-id", c.GetString(common.UpstreamRequestIdKey))
			assert.Equal(t, "local-request", response.Header.Get("X-Request-Id"))
			assert.Empty(t, response.Header.Get("X-Ds-Trace-Id"))
			assert.Empty(t, response.Header.Get("Cf-Ray"))
			assert.Empty(t, response.Header.Get("ETag"))
			assert.Empty(t, response.Header.Get("Content-Length"))
			assert.Empty(t, response.Header.Get("Content-Encoding"))
			assert.Equal(t, "2", response.Header.Get("Retry-After"))
			if tc.stream {
				assert.NotContains(t, string(body), "provider-response")
				assert.NotContains(t, string(body), "provider-event")
				found := 0
				for line := range strings.SplitSeq(string(body), "\n") {
					if data, ok := strings.CutPrefix(line, "data:"); ok && strings.TrimSpace(data) != "[DONE]" {
						require.True(t, ojson.Valid(data), data)
						if id := ojson.Get(data, tc.path); id.Exists() {
							assert.Equal(t, tc.prefix+"local-request", id.String())
							found++
						}
					}
				}
				assert.Positive(t, found)
				if strings.Contains(tc.body, "[DONE]") {
					assert.Contains(t, string(body), "[DONE]")
				}
			} else if tc.path != "" {
				assert.Equal(t, tc.prefix+"local-request", ojson.GetBytes(body, tc.path).String())
				for _, path := range []string{"choices", "output", "content", "results", "usage", "extension"} {
					before := ojson.Get(tc.body, path)
					if before.Exists() {
						assert.JSONEq(t, before.Raw, ojson.GetBytes(body, path).Raw, path)
					}
				}
				if tc.name == "chat JSON" {
					assert.Equal(t, "9007199254740993", ojson.GetBytes(body, "extension").Raw)
				}
			} else {
				assert.NotContains(t, string(body), "provider-")
				assert.Equal(t, "invalid_request", ojson.GetBytes(body, "error.code").String())
				assert.Contains(t, ojson.GetBytes(body, "error.message").String(), "local-request")
			}
			for _, reference := range []string{"call-functional", "item-functional"} {
				if strings.Contains(tc.body, reference) {
					assert.Contains(t, string(body), reference)
				}
			}
		})
	}
}

type privacyTestBody struct {
	io.Reader
	closed bool
}

func (b *privacyTestBody) Close() error { b.closed = true; return nil }

func TestDeepSeekPrivacyReadBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	for _, tc := range []struct {
		name, body, contentType string
		invalid                 bool
	}{
		{"missing content type", `{"id":"provider-body-id","choices":[]}`, "", false},
		{"invalid JSON", `{"id":"provider-body-id",`, "application/json", true},
		{"non JSON error", "<html>provider-body-id</html>", "text/html", true},
		{"truncated SSE", "data: {\"id\":\"provider-body-id\",\n\n", "text/event-stream", true},
		{"SSE final event without blank line", "data: {\"id\":\"provider-body-id\",\"choices\":[]}", "text/event-stream", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
			c.Set(common.RequestIdKey, "local-request")
			var originalCapture bytes.Buffer
			source := &privacyTestBody{Reader: io.TeeReader(iotest.OneByteReader(strings.NewReader(tc.body)), &originalCapture)}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: source}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek}, RelayFormat: types.RelayFormatOpenAI}
			err := protectDeepSeekResponse(c, info, response)
			var body []byte
			if err == nil {
				body, err = io.ReadAll(response.Body)
			}
			if tc.invalid {
				require.Error(t, err)
				assert.NotContains(t, string(body), "provider-body-id")
				assert.NotContains(t, err.Error(), "provider-body-id")
			} else {
				require.NoError(t, err)
				assert.Contains(t, string(body), "chatcmpl-local-request")
				assert.Equal(t, "provider-body-id", c.GetString(common.UpstreamRequestIdKey))
				assert.Equal(t, tc.body, originalCapture.String(), "the upstream trace must retain the original bytes")
			}
			require.NoError(t, response.Body.Close())
			assert.True(t, source.closed)
		})
	}

	t.Run("stream is incremental and closes upstream", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
		c.Set(common.RequestIdKey, "local-request")
		input, writer := io.Pipe()
		defer input.Close()
		defer writer.Close()
		first := "data: {\"id\":\"provider-body-id\",\"choices\":[]}\n\n"
		done := make(chan error, 1)
		go func() { _, err := io.WriteString(writer, first); done <- err }()
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: input}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek}, RelayFormat: types.RelayFormatOpenAI, IsStream: true}
		require.NoError(t, protectDeepSeekResponse(c, info, response))
		reader := bufio.NewReader(response.Body)
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		assert.Contains(t, line, "chatcmpl-local-request")
		require.NoError(t, <-done, "the first event must arrive before the upstream stream ends")
		require.NoError(t, response.Body.Close())
		_, err = writer.Write([]byte("next event"))
		require.ErrorIs(t, err, io.ErrClosedPipe)
	})

	t.Run("other providers are unchanged", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
		c.Set(common.RequestIdKey, "local-request")
		original := []byte(`{"id":"provider-body-id","previous_response_id":"previous-id"}`)
		source := &privacyTestBody{Reader: bytes.NewReader(original)}
		response := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"provider-header-id"}}, Body: source}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI}}
		require.NoError(t, protectDeepSeekResponse(c, info, response))
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		assert.Equal(t, original, body)
		assert.Equal(t, "provider-header-id", response.Header.Get("X-Request-Id"))
		assert.Same(t, source, response.Body)
	})

	t.Run("diagnostic ID present only in error text", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
		c.Set(common.RequestIdKey, "local-request")
		response := &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"Rejected (request_id: provider-only-id); trace_id=trace-only-id"}}`))}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek}}
		require.NoError(t, protectDeepSeekResponse(c, info, response))
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		assert.NotContains(t, string(body), "provider-only-id")
		assert.NotContains(t, string(body), "trace-only-id")
		assert.Contains(t, string(body), "local-request")
	})

	t.Run("closing a blocked stream interrupts the read", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", nil)
		c.Set(common.RequestIdKey, "local-request")
		input, writer := io.Pipe()
		defer writer.Close()
		defer input.Close()
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: input}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek}, IsStream: true}
		require.NoError(t, protectDeepSeekResponse(c, info, response))
		readResult := make(chan error, 1)
		go func() { _, err := io.ReadAll(response.Body); readResult <- err }()
		_, err := io.WriteString(writer, "data: {\"id\":")
		require.NoError(t, err, "the response reader has consumed the partial event")
		require.NoError(t, response.Body.Close())
		require.Error(t, <-readResult)
	})
}

func TestNewTaskAPIRequestInheritsClientCancellation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestContext)

	upstream, err := newTaskAPIRequest(c, "https://provider.example/tasks", nil)
	require.NoError(t, err)
	cancel()

	require.ErrorIs(t, upstream.Context().Err(), context.Canceled)
}

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}

func TestToWebSocketURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://api.openai.com/v1/responses":             "wss://api.openai.com/v1/responses",
		"http://127.0.0.1:3000/v1/responses":              "ws://127.0.0.1:3000/v1/responses",
		"wss://chatgpt.com/backend-api/codex/responses":   "wss://chatgpt.com/backend-api/codex/responses",
		"ws://127.0.0.1:3000/backend-api/codex/responses": "ws://127.0.0.1:3000/backend-api/codex/responses",
	} {
		assert.Equal(t, want, toWebSocketURL(input), input)
	}
}
