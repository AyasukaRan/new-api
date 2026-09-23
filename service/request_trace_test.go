package service

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceBufferKeepsWholePayloadUnderCap(t *testing.T) {
	buffer := newTraceBuffer(1024)
	buffer.Write([]byte("hello "))
	buffer.Write([]byte("world"))

	payload, total, truncated := buffer.Payload()
	assert.Equal(t, "hello world", payload)
	assert.Equal(t, int64(11), total)
	assert.False(t, truncated)
}

// A stream carries its usage block and finish_reason at the very end, so an
// oversized payload has to keep its tail, not only its head.
func TestTraceBufferKeepsHeadAndTailWhenOversized(t *testing.T) {
	buffer := newTraceBuffer(1024)
	buffer.Write([]byte("HEAD" + strings.Repeat("x", 4096) + "TAIL"))

	payload, total, truncated := buffer.Payload()
	require.True(t, truncated)
	assert.Equal(t, int64(4104), total)
	assert.True(t, strings.HasPrefix(payload, "HEAD"))
	assert.True(t, strings.HasSuffix(payload, "TAIL"))
	assert.Contains(t, payload, "bytes elided")
	assert.Less(t, len(payload), 2048)
}

func TestTraceBufferPreservesUTF8AcrossElisionAndWrites(t *testing.T) {
	for _, test := range []struct {
		name, original, expected string
		truncated                bool
	}{
		{name: "whole Chinese text split inside a character", original: strings.Repeat("中", 300), expected: strings.Repeat("中", 300)},
		{name: "Chinese elision counts incomplete boundary bytes", original: strings.Repeat("中", 1000), expected: strings.Repeat("中", 170) + "\n\n... [1980 bytes elided] ...\n\n" + strings.Repeat("中", 170), truncated: true},
		{name: "four-byte characters at both boundaries", original: "前" + strings.Repeat("🙂", 400) + "尾", expected: "前" + strings.Repeat("🙂", 127) + "\n\n... [584 bytes elided] ...\n\n" + strings.Repeat("🙂", 127) + "尾", truncated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, chunked := range []bool{false, true} {
				buffer := newTraceBuffer(1024)
				original := []byte(test.original)
				if chunked {
					// Neither transport chunk boundaries nor lazy tail compaction
					// may change what the administrator can read.
					for _, chunk := range [][]byte{original[:511], original[511:513], original[513:700], original[700:]} {
						buffer.Write(chunk)
					}
				} else {
					buffer.Write(original)
				}
				payload, size, truncated := buffer.Payload()
				assert.Equal(t, int64(len(original)), size)
				assert.Equal(t, test.truncated, truncated)
				assert.True(t, utf8.ValidString(payload))
				assert.Equal(t, test.expected, payload)
			}
		})
	}
}

func TestTraceTextNormalizationDoesNotModifyRelayBytes(t *testing.T) {
	for _, test := range []struct {
		name     string
		original []byte
		expected string
	}{
		{name: "invalid UTF8", original: []byte("before\xff\xfemiddle\xe5\x93"), expected: "before�middle�"},
		{name: "PostgreSQL NUL restriction", original: []byte("before\x00after"), expected: "before�after"},
		{name: "JSON escaped NUL is valid text", original: []byte(`{"value":"\u0000"}`), expected: `{"value":"\u0000"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			buffer := newTraceBuffer(1024)
			tee := &traceBodyTee{ReadCloser: io.NopCloser(bytes.NewReader(test.original)), collector: &requestTraceCollector{}, sink: buffer}
			relayed, err := io.ReadAll(tee)
			require.NoError(t, err)
			assert.Equal(t, test.original, relayed, "tracing must not change upstream response bytes")
			payload, size, truncated := buffer.Payload()
			assert.Equal(t, int64(len(test.original)), size)
			assert.False(t, truncated, "normalizing invalid text is distinct from dropping the middle of an oversized payload")
			assert.True(t, utf8.ValidString(payload))
			assert.NotContains(t, payload, "\x00")
			assert.Equal(t, test.expected, payload)
		})
	}
}

// Credentials reach the trace from both directions and under operator-defined
// header names, so a name allowlist alone is not enough: the values themselves
// have to be scrubbed wherever they appear.
func TestTraceRedactsCredentialsByNameAndByValue(t *testing.T) {
	collector := &requestTraceCollector{}
	collector.addSecret("sk-super-secret-user-key")
	collector.addSecret("upstream-channel-key-value")
	collector.addSecret("short")

	header := http.Header{}
	header.Set("Authorization", "Bearer sk-super-secret-user-key")
	header.Set("X-Custom-Auth", "prefix upstream-channel-key-value suffix")
	header.Add("Accept", "application/json")

	encoded := collector.encodeHeaders(header, nil)
	assert.NotContains(t, encoded, "sk-super-secret-user-key")
	assert.NotContains(t, encoded, "upstream-channel-key-value")
	assert.Contains(t, encoded, "application/json")

	body := collector.scrub(`{"key":"upstream-channel-key-value","note":"short"}`)
	assert.NotContains(t, body, "upstream-channel-key-value")
	// Short strings are left alone; scrubbing them would corrupt ordinary text.
	assert.Contains(t, body, "short")
}

// A channel key interpolated into a URL arrives percent-encoded, so matching
// only the raw spelling would let it through in the recorded request URL.
func TestTraceRedactsPercentEncodedCredentials(t *testing.T) {
	collector := &requestTraceCollector{}
	collector.addSecret("aBc+dEf/gHi=jKl")

	scrubbed := collector.scrub(
		"https://upstream.example.com/v1/chat?subscription-key=" + url.QueryEscape("aBc+dEf/gHi=jKl"))
	assert.NotContains(t, scrubbed, "aBc%2BdEf")
	assert.NotContains(t, scrubbed, "aBc+dEf")
	assert.Contains(t, scrubbed, redactedPlaceholder)
}

// An operator can put a provider key in a channel's header override under any
// vendor-specific name, so the name list cannot cover it and the value has to go.
func TestTraceRedactsHeaderOverrideValues(t *testing.T) {
	info := &relaycommon.RelayInfo{
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"Ocp-Apim-Subscription-Key": "3f8c1a9b2d4e6f80",
			"*":                         true,
			"re:^x-trace-":              true,
		},
	}
	names := headerOverrideNames(info)
	assert.Contains(t, names, "ocp-apim-subscription-key")
	// Passthrough directives are wildcards, not header names.
	assert.NotContains(t, names, "*")
	assert.NotContains(t, names, "re:^x-trace-")

	header := http.Header{}
	header.Set("Ocp-Apim-Subscription-Key", "3f8c1a9b2d4e6f80")
	header.Set("Accept", "application/json")

	encoded := (&requestTraceCollector{}).encodeHeaders(header, names)
	assert.NotContains(t, encoded, "3f8c1a9b2d4e6f80")
	assert.Contains(t, encoded, "application/json")
}

func TestBodyCapturableRejectsBinaryPayloads(t *testing.T) {
	assert.True(t, bodyCapturable("application/json; charset=utf-8"))
	assert.True(t, bodyCapturable("text/event-stream"))
	assert.True(t, bodyCapturable(""))
	assert.False(t, bodyCapturable("audio/mpeg"))
	assert.False(t, bodyCapturable("multipart/form-data; boundary=x"))
}

func TestRenderTraceResponseAcrossFormats(t *testing.T) {
	cases := []struct {
		name         string
		format       string
		body         string
		reasoning    string
		content      string
		toolName     string
		toolArgs     string
		finishReason string
		stream       bool
	}{
		{
			name:   "openai chat stream reassembles deltas and tool call fragments",
			format: "openai",
			body: "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think \"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"harder\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"SH\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
			reasoning:    "think harder",
			content:      "Hi",
			toolName:     "get_weather",
			toolArgs:     `{"city":"SH"}`,
			finishReason: "tool_calls",
			stream:       true,
		},
		{
			name:         "openai chat non-stream reads the message block",
			format:       "openai",
			body:         `{"choices":[{"message":{"content":"Done","reasoning_content":"why","tool_calls":[{"id":"c1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
			reasoning:    "why",
			content:      "Done",
			toolName:     "lookup",
			toolArgs:     "{}",
			finishReason: "stop",
		},
		{
			name:   "claude stream separates thinking from text and accumulates tool input",
			format: "claude",
			body: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"pondering\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"search\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":1}\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n",
			reasoning:    "pondering",
			content:      "Hello",
			toolName:     "search",
			toolArgs:     `{"q":1}`,
			finishReason: "tool_use",
			stream:       true,
		},
		{
			name:         "claude non-stream reads every content block",
			format:       "claude",
			body:         `{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"Answer"},{"type":"tool_use","id":"t1","name":"calc","input":{"a":1}}],"stop_reason":"end_turn"}`,
			reasoning:    "hmm",
			content:      "Answer",
			toolName:     "calc",
			toolArgs:     `{"a":1}`,
			finishReason: "end_turn",
		},
		{
			name:   "gemini stream keeps thought parts out of the reply text",
			format: "gemini",
			body: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"reasoning\",\"thought\":true}]}}]}\n\n" +
				"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"visible\"}]},\"finishReason\":\"STOP\"}]}\n\n",
			reasoning:    "reasoning",
			content:      "visible",
			finishReason: "STOP",
			stream:       true,
		},
		{
			name:         "gemini function call becomes a tool call",
			format:       "gemini",
			body:         `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_time","args":{"tz":"UTC"}}}]},"finishReason":"STOP"}]}`,
			toolName:     "get_time",
			toolArgs:     `{"tz":"UTC"}`,
			finishReason: "STOP",
		},
		{
			name:         "openai responses prefers the completed event over the deltas",
			format:       "openai_responses",
			body:         "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"summary\":[{\"text\":\"plan\"}]},{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"final\"}]},{\"type\":\"function_call\",\"call_id\":\"fc1\",\"name\":\"run\",\"arguments\":\"{}\"}]}}\n\n",
			reasoning:    "plan",
			content:      "final",
			toolName:     "run",
			toolArgs:     "{}",
			finishReason: "completed",
			stream:       true,
		},
		{
			name:      "openai responses falls back to deltas when the stream never completed",
			format:    "openai_responses",
			body:      "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"half \"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"cut\"}\n\n",
			reasoning: "half ",
			content:   "cut",
			stream:    true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rendered := RenderTraceResponse(testCase.format, testCase.body)
			require.NotNil(t, rendered)
			assert.Equal(t, testCase.reasoning, rendered.Reasoning)
			assert.Equal(t, testCase.content, rendered.Content)
			assert.Equal(t, testCase.finishReason, rendered.FinishReason)
			assert.Equal(t, testCase.stream, rendered.Stream)
			if testCase.toolName == "" {
				assert.Empty(t, rendered.ToolCalls)
				return
			}
			require.Len(t, rendered.ToolCalls, 1)
			assert.Equal(t, testCase.toolName, rendered.ToolCalls[0].Name)
			assert.Equal(t, testCase.toolArgs, rendered.ToolCalls[0].Arguments)
		})
	}
}

// Alternatives from an n>1 request must not be interleaved into the primary
// turn, and a provider that answers with array-shaped content must not cost the
// event its tool calls and finish_reason.
func TestRenderTraceResponseHandlesAlternativesAndArrayContent(t *testing.T) {
	rendered := RenderTraceResponse("openai",
		`{"choices":[`+
			`{"index":0,"message":{"content":[{"type":"text","text":"primary"}],`+
			`"tool_calls":[{"id":"c1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"},`+
			`{"index":1,"message":{"content":"alternative",`+
			`"tool_calls":[{"id":"c2","function":{"name":"other","arguments":"{}"}}]},"finish_reason":"stop"}]}`)
	require.NotNil(t, rendered)
	assert.Equal(t, "primary", rendered.Content)
	assert.Equal(t, "tool_calls", rendered.FinishReason)
	require.Len(t, rendered.ToolCalls, 1)
	assert.Equal(t, "lookup", rendered.ToolCalls[0].Name)

	gemini := RenderTraceResponse("gemini",
		`{"candidates":[{"index":0,"content":{"parts":[{"text":"primary"}]},"finishReason":"STOP"},`+
			`{"index":1,"content":{"parts":[{"text":"alternative"}]},"finishReason":"STOP"}]}`)
	require.NotNil(t, gemini)
	assert.Equal(t, "primary", gemini.Content)
}

func TestRenderTraceResponseIgnoresUnusablePayloads(t *testing.T) {
	assert.Nil(t, RenderTraceResponse("openai", ""))
	assert.Nil(t, RenderTraceResponse("openai", "not json at all"))
	// An error body is valid JSON but carries no assistant turn; the raw panel
	// is the right place to read it.
	assert.Nil(t, RenderTraceResponse("openai", `{"error":{"message":"boom"}}`))
}

// A media payload is kept whole or not at all: half an mp3 will not play, so
// exceeding the budget must drop what was collected rather than store a stub.
func TestTraceObjectBufferKeepsWholePayloadOrNothing(t *testing.T) {
	within := newTraceObjectBuffer(1024)
	within.Write([]byte("ID3"))
	within.Write(bytes.Repeat([]byte("a"), 100))
	assert.False(t, within.overflow)
	assert.Equal(t, int64(103), within.total)
	assert.Len(t, within.data, 103)

	over := newTraceObjectBuffer(1024)
	over.Write(bytes.Repeat([]byte("a"), 900))
	over.Write(bytes.Repeat([]byte("b"), 900))
	// The real size is still reported so the viewer can say why nothing is there.
	assert.True(t, over.overflow)
	assert.Equal(t, int64(1800), over.total)
	assert.Empty(t, over.data)

	// Later writes on an overflowed buffer keep counting but allocate nothing.
	over.Write(bytes.Repeat([]byte("c"), 100))
	assert.Equal(t, int64(1900), over.total)
	assert.Empty(t, over.data)
}

// Binary offload only engages when object storage is actually configured;
// otherwise a binary body is dropped as before rather than buffered for nothing.
func TestObjectCapturableRequiresAConfiguredStore(t *testing.T) {
	assert.False(t, ObjectStoreEnabled(), "test process has no object store configured")
	assert.False(t, objectCapturable("audio/mpeg"))
	assert.False(t, objectCapturable("application/json"))
}
