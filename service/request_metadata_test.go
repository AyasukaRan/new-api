package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestMetadataObservesOnlyCurrentResponseTools(t *testing.T) {
	for _, test := range []struct {
		name   string
		format types.RelayFormat
		mode   int
		stream bool
		body   string
		tools  []string
		state  string
	}{
		{
			name: "chat all choices and legacy calls", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions,
			body:  `{"choices":[{"index":0,"message":{"tool_calls":[{"id":"a","function":{"name":"lookup","arguments":"secret arguments"}},{"id":"b","function":{"name":"lookup","arguments":"{}"}}]}},{"index":1,"message":{"function_call":{"name":"search","arguments":"{}"}}}]}`,
			tools: []string{"lookup", "search"}, state: "complete",
		},
		{
			name: "chat streamed name fragments and parallel calls", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions, stream: true,
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"get_\"}},{\"index\":1,\"function\":{\"name\":\"search\"}}]}}]}\r\n\r\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"weather\",\"arguments\":\"secret\"}}]}}]}\n\n" + "data: [DONE]\n\n",
			tools: []string{"get_weather", "search"}, state: "complete",
		},
		{
			name: "chat unfinished stream", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions, stream: true,
			body:  "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"lookup\"}}]}}]}\n\n",
			tools: []string{"lookup"}, state: "partial",
		},
		{
			name: "declarations and history are not invocations", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions,
			body:  `{"tools":[{"function":{"name":"declared"}}],"messages":[{"tool_calls":[{"function":{"name":"old_call"}}]}],"choices":[{"message":{"content":"No tool needed"}}]}`,
			tools: []string{}, state: "complete",
		},
		{
			name: "responses completed deduplicates events and builtins", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses, stream: true,
			body: "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"a\",\"type\":\"function_call\",\"name\":\"lookup\"}}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"a\",\"type\":\"function_call\",\"name\":\"lookup\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"a\",\"type\":\"function_call\",\"name\":\"lookup\"},{\"type\":\"web_search_call\"},{\"type\":\"mcp_call\",\"name\":\"mcp_lookup\"}]}}\n\n",
			tools: []string{"lookup", "mcp_lookup", "web_search"}, state: "complete",
		},
		{
			name: "responses nonstream custom and hosted tools", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses,
			body:  `{"output":[{"type":"custom_tool_call","name":"write_file","input":"secret"},{"type":"code_interpreter_call"},{"type":"mcp_list_tools","tools":[{"name":"uninvoked"}]}]}`,
			tools: []string{"code_interpreter", "write_file"}, state: "complete",
		},
		{
			name: "responses incomplete snapshot", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses, stream: true,
			body:  "data: {\"type\":\"response.incomplete\",\"response\":{\"output\":[{\"type\":\"function_call\",\"name\":\"lookup\"}]}}\n\n",
			tools: []string{"lookup"}, state: "partial",
		},
		{
			name: "responses nonstream incomplete", format: types.RelayFormatOpenAIResponses, mode: relayconstant.RelayModeResponses,
			body:  `{"status":"incomplete","output":[{"type":"function_call","name":"lookup"}]}`,
			tools: []string{"lookup"}, state: "partial",
		},
		{
			name: "claude tool and server tool", format: types.RelayFormatClaude,
			body:  `{"type":"message","role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"secret":"not retained"}},{"type":"server_tool_use","name":"web_search"},{"type":"tool_result","name":"not_current"}]}`,
			tools: []string{"Read", "web_search"}, state: "complete",
		},
		{
			name: "claude streamed tools", format: types.RelayFormatClaude, stream: true,
			body: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"Bash\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"secret\"}}\n\n" + "data: {\"type\":\"message_stop\"}\n\n",
			tools: []string{"Bash"}, state: "complete",
		},
		{
			name: "gemini nonstream all candidates", format: types.RelayFormatGemini, mode: relayconstant.RelayModeGemini,
			body:  `[{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"lookup","args":{"secret":"not retained"}}}]}},{"index":1,"content":{"parts":[{"functionCall":{"name":"search"}}]}}]}]`,
			tools: []string{"lookup", "search"}, state: "complete",
		},
		{
			name: "gemini streamed function call", format: types.RelayFormatGemini, mode: relayconstant.RelayModeGemini, stream: true,
			body:  "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"lookup\"}}]},\"finishReason\":\"STOP\"}]}\n\n",
			tools: []string{"lookup"}, state: "complete",
		},
		{
			name: "gemini hosted execution and observed grounding", format: types.RelayFormatGemini, mode: relayconstant.RelayModeGemini,
			body:  `{"candidates":[{"content":{"parts":[{"executableCode":{"language":"PYTHON","code":"secret"}},{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"secret"}}]},"groundingMetadata":{"webSearchQueries":["secret search"]}}]}`,
			tools: []string{"code_execution", "web_search"}, state: "complete",
		},
		{
			name: "unsupported format stays unknown", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeEmbeddings,
			body: `{"data":[{"embedding":[1,2]}]}`,
		},
		{
			name: "upstream error does not mean no tools", format: types.RelayFormatOpenAI, mode: relayconstant.RelayModeChatCompletions,
			body: `{"error":{"message":"unavailable"}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"tools":[{"function":{"name":"never_called"}}]}`))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/1.2.3")
			original := c.Writer
			info := &relaycommon.RelayInfo{RelayFormat: test.format, RelayMode: test.mode, IsStream: test.stream, ReasoningEffort: "high", Request: &dto.GeneralOpenAIRequest{ReasoningEffort: "high"}}
			finish := BeginRequestMetadata(c, info)
			if test.stream {
				c.Writer.Header().Set("Content-Type", "text/event-stream")
			}
			// A frame and its UTF-8/name boundaries may cross arbitrary writes.
			cut := min(17, len(test.body))
			_, err := c.Writer.WriteString(test.body[:cut])
			require.NoError(t, err)
			_, err = c.Writer.Write([]byte(test.body[cut:]))
			require.NoError(t, err)
			other := model.NewLogOther()
			AppendRequestMetadata(c, info, other)
			values := other.Snapshot()
			assert.Equal(t, "Codex CLI", values["client_tool"])
			assert.Equal(t, "high", values["reasoning_effort"])
			if test.state == "" {
				assert.NotContains(t, values, "tool_observation")
				assert.NotContains(t, values, "invoked_tools")
			} else {
				assert.Equal(t, test.state, values["tool_observation"])
				assert.Equal(t, test.tools, values["invoked_tools"])
			}
			assert.NotContains(t, other.JSONString(), "secret")
			assert.NotContains(t, other.JSONString(), "never_called")
			assert.Equal(t, test.body, recorder.Body.String(), "observation must never change the client response")
			finish()
			assert.Same(t, original, c.Writer)
		})
	}
}

func TestRequestMetadataBoundsAndStreamingRecovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream bool
		body   string
		tools  []string
	}{
		{name: "oversized JSON", body: `{"choices":[{"message":{"content":"` + strings.Repeat("x", 2<<20) + `","tool_calls":[{"function":{"name":"not_observed"}}]}}]}`, tools: []string{}},
		{name: "oversized stream event recovers next frame", stream: true, body: "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 2<<20) + "\"}}]}\n\n" + "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"later_tool\"},\"index\":0}]}}]}\n\n" + "data: [DONE]\n\n", tools: []string{"later_tool"}},
		{name: "control characters are not labels", body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"bad\nname"}},{"function":{"name":"safe_tool"}}]}}]}`, tools: []string{"safe_tool"}},
		{name: "overflowed name never leaks its prefix", stream: true, body: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"prefix_\"}}]}}]}\n\n" + "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"" + strings.Repeat("x", 512) + "\"}}]}}]}\n\n" + "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"suffix\"}}]}}]}\n\n" + "data: [DONE]\n\n", tools: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: test.stream}
			defer BeginRequestMetadata(c, info)()
			_, err := c.Writer.WriteString(test.body)
			require.NoError(t, err)
			other := model.NewLogOther()
			AppendRequestMetadata(c, info, other)
			assert.Equal(t, "partial", other.Snapshot()["tool_observation"])
			assert.Equal(t, test.tools, other.Snapshot()["invoked_tools"])
		})
	}
}

func TestRequestMetadataIsolatesRetriedChannelAttempts(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("User-Agent", "opencode/1.2.3")
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: true}
	defer BeginRequestMetadata(c, info)()
	_, err := c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"failed_attempt_tool\"}}]}}]}\n\n")
	require.NoError(t, err)
	failed := model.NewLogOther()
	AppendRequestMetadata(c, info, failed)
	assert.Equal(t, []string{"failed_attempt_tool"}, failed.Snapshot()["invoked_tools"])
	assert.Equal(t, "partial", failed.Snapshot()["tool_observation"])
	ResetRequestToolObservation(c)
	_, err = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"successful_tool\"}}]}}]}\n\n" + "data: [DONE]\n\n")
	require.NoError(t, err)
	succeeded := model.NewLogOther()
	AppendRequestMetadata(c, info, succeeded)
	assert.Equal(t, []string{"successful_tool"}, succeeded.Snapshot()["invoked_tools"])
	assert.Equal(t, "complete", succeeded.Snapshot()["tool_observation"])
	assert.Equal(t, "OpenCode", succeeded.Snapshot()["client_tool"])
	assert.Equal(t, []string{"failed_attempt_tool"}, failed.Snapshot()["invoked_tools"])
}

func TestRequestMetadataClientIdentityUsesExplicitOriginalHeaders(t *testing.T) {
	for _, test := range []struct {
		name         string
		headers      map[string]string
		headerValues http.Header
		want         string
	}{
		{name: "Codex", headers: map[string]string{"User-Agent": "codex_cli_rs/0.100 (Darwin)"}, want: "Codex CLI"},
		{name: "Codex originator", headers: map[string]string{"originator": "codex_cli_rs"}, want: "Codex CLI"},
		{name: "Claude", headers: map[string]string{"User-Agent": "claude-cli/2.1.0 (external, cli)"}, want: "Claude Code"},
		{name: "Gemini", headers: map[string]string{"User-Agent": "GeminiCLI/1.0.0/gemini-3"}, want: "Gemini CLI"},
		{name: "Gemini variant", headers: map[string]string{"User-Agent": "GeminiCLI-server/1.0.0"}, want: "Gemini CLI"},
		{name: "OpenCode", headers: map[string]string{"User-Agent": "opencode/1.2.3"}, want: "OpenCode"},
		{name: "DSH", headers: map[string]string{"User-Agent": "deepseek-harness/0.1.5-rc.1 (+https://github.com/deepseek-ai/deepseek-harness)", "x-deepseek-harness-user-id": "not retained"}, want: "DeepSeek Harness"},
		{name: "Cline", headers: map[string]string{"User-Agent": "Cline/3.0.0"}, want: "Cline"},
		{name: "Cline attribution pair", headers: map[string]string{"X-Title": "Cline", "HTTP-Referer": "https://cline.bot"}, want: "Cline"},
		{name: "Aider", headers: map[string]string{"Editor-Version": "aider/0.86.0"}, want: "Aider"},
		{name: "UA takes priority", headers: map[string]string{"User-Agent": "claude-cli/2.1.0", "originator": "codex_cli_rs"}, want: "Claude Code"},
		{name: "OpenAI SDK not guessed as CLI", headers: map[string]string{"User-Agent": "OpenAI/Python 1.1.0", "x-app": "cli"}, want: "OpenAI Python SDK"},
		{name: "async OpenAI SDK", headers: map[string]string{"User-Agent": "AsyncOpenAI/Python 1.1.0"}, want: "OpenAI Python SDK"},
		{name: "Azure OpenAI SDK", headers: map[string]string{"User-Agent": "AzureOpenAI/Python 1.1.0"}, want: "OpenAI Python SDK"},
		{name: "async Azure OpenAI SDK", headers: map[string]string{"User-Agent": "AsyncAzureOpenAI/Python 1.1.0"}, want: "OpenAI Python SDK"},
		{name: "Anthropic SDK", headers: map[string]string{"User-Agent": "Anthropic/Python 0.70.0"}, want: "Anthropic Python SDK"},
		{name: "async Anthropic SDK", headers: map[string]string{"User-Agent": "AsyncAnthropic/Python 0.70.0"}, want: "Anthropic Python SDK"},
		{name: "requests automation", headers: map[string]string{"User-Agent": "python-requests/2.32.5"}, want: "Python Requests"},
		{name: "HTTPX automation", headers: map[string]string{"User-Agent": "python-httpx/0.28.1"}, want: "Python HTTPX"},
		{name: "aiohttp automation", headers: map[string]string{"User-Agent": "Python/3.13 aiohttp/3.12.0"}, want: "Python aiohttp"},
		{name: "urllib automation", headers: map[string]string{"User-Agent": "Python-urllib/3.13"}, want: "Python urllib"},
		{name: "Python runtime only", headers: map[string]string{"User-Agent": "Python/3.13"}, want: "Python"},
		{name: "custom SDK only identifies language", headers: map[string]string{"User-Agent": "CustomClient/Python 1.0", "X-Stainless-Lang": " Python "}, want: "Python"},
		{name: "SDK wins over HTTP client", headers: map[string]string{"User-Agent": "python-httpx/0.28.1 OpenAI/Python 1.1.0"}, want: "OpenAI Python SDK"},
		{name: "DSH wins over SDK", headers: map[string]string{"User-Agent": "OpenAI/Python 1.1.0 deepseek-harness/0.1.5", "X-Stainless-Lang": "python"}, want: "DeepSeek Harness"},
		{name: "explicit application wins over SDK", headers: map[string]string{"User-Agent": "OpenAI/Python 1.1.0", "Editor-Version": "aider/0.86.0"}, want: "Aider"},
		{name: "language is not a substring", headers: map[string]string{"X-Stainless-Lang": "python-custom"}},
		{name: "Python product is not a substring", headers: map[string]string{"User-Agent": "not-python-requests/2.32.5"}},
		{name: "SDK product is not a substring", headers: map[string]string{"User-Agent": "NotOpenAI/Python 1.1.0"}},
		{name: "JavaScript SDK is not Python", headers: map[string]string{"User-Agent": "OpenAI/JS 1.1.0", "X-Stainless-Lang": "js"}, want: "OpenAI JavaScript SDK"},
		{name: "Anthropic JavaScript", headers: map[string]string{"User-Agent": "Anthropic/JS 0.70.0"}, want: "Anthropic JavaScript SDK"},
		{name: "OpenAI Go", headers: map[string]string{"User-Agent": "OpenAI/Go 2.0.0"}, want: "OpenAI Go SDK"},
		{name: "Anthropic Go", headers: map[string]string{"User-Agent": "Anthropic/Go 1.0.0"}, want: "Anthropic Go SDK"},
		{name: "OpenAI async Java", headers: map[string]string{"User-Agent": "OpenAIClientAsyncImpl/Java 3.0.0"}, want: "OpenAI Java SDK"},
		{name: "Anthropic Java", headers: map[string]string{"User-Agent": "AnthropicClientImpl/Java 1.0.0"}, want: "Anthropic Java SDK"},
		{name: "OpenAI Ruby", headers: map[string]string{"User-Agent": "OpenAI::Client/Ruby 0.1.0"}, want: "OpenAI Ruby SDK"},
		{name: "Anthropic Ruby", headers: map[string]string{"User-Agent": "Anthropic::Client/Ruby 0.1.0"}, want: "Anthropic Ruby SDK"},
		{name: "Anthropic PHP", headers: map[string]string{"User-Agent": "anthropic/PHP 0.1.0"}, want: "Anthropic PHP SDK"},
		{name: "OpenAI dotnet", headers: map[string]string{"User-Agent": "OpenAI/2.1.0 (.NET 8.0.0; Microsoft Windows 10.0.0)"}, want: "OpenAI .NET SDK"},
		{name: "Anthropic dotnet", headers: map[string]string{"User-Agent": "AnthropicClient/C# 1.0.0"}, want: "Anthropic .NET SDK"},
		{name: "OpenAI version does not prove language", headers: map[string]string{"User-Agent": "OpenAI/2.1.0"}},
		{name: "curl", headers: map[string]string{"User-Agent": "curl/8.12.0"}, want: "curl"},
		{name: "Wget", headers: map[string]string{"User-Agent": "Wget/1.25.0"}, want: "Wget"},
		{name: "Postman", headers: map[string]string{"User-Agent": "PostmanRuntime/7.43.0"}, want: "Postman"},
		{name: "Insomnia", headers: map[string]string{"User-Agent": "insomnia/10.0.0"}, want: "Insomnia"},
		{name: "HTTPie", headers: map[string]string{"User-Agent": "HTTPie/3.2.4"}, want: "HTTPie"},
		{name: "Axios", headers: map[string]string{"User-Agent": "axios/1.9.0"}, want: "Axios"},
		{name: "node-fetch bare UA", headers: map[string]string{"User-Agent": "node-fetch"}, want: "node-fetch"},
		{name: "Undici bare UA", headers: map[string]string{"User-Agent": "undici"}, want: "Undici"},
		{name: "Go HTTP", headers: map[string]string{"User-Agent": "Go-http-client/1.1"}, want: "Go HTTP Client"},
		{name: "Java HTTP", headers: map[string]string{"User-Agent": "Java-http-client/21.0.2"}, want: "Java HTTP Client"},
		{name: "OkHttp", headers: map[string]string{"User-Agent": "okhttp/4.12.0"}, want: "OkHttp"},
		{name: "Apache HTTP", headers: map[string]string{"User-Agent": "Apache-HttpClient/4.5.14 (Java/17.0.2)"}, want: "Apache HttpClient"},
		{name: "Guzzle", headers: map[string]string{"User-Agent": "GuzzleHttp/7"}, want: "Guzzle"},
		{name: "Ruby bare UA", headers: map[string]string{"User-Agent": "Ruby"}, want: "Ruby"},
		{name: "Node fetch runtime", headers: map[string]string{"User-Agent": "node"}, want: "Node.js"},
		{name: "PowerShell", headers: map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0) WindowsPowerShell/5.1.19041.1"}, want: "PowerShell"},
		{name: "n8n bare UA", headers: map[string]string{"User-Agent": "n8n"}, want: "n8n"},
		{name: "n8n compatible UA", headers: map[string]string{"User-Agent": "Mozilla/5.0 (compatible; n8n/1.123.0; +https://n8n.io/)"}, want: "n8n"},
		{name: "CodeWhale compatible UA", headers: map[string]string{"User-Agent": "Mozilla/5.0 (compatible; codewhale/0.9.11; +https://github.com/Hmbown/CodeWhale)"}, want: "CodeWhale"},
		{name: "declared Dify product", headers: map[string]string{"User-Agent": "OpenAI/Python 1.1.0 Dify/1.0.0"}, want: "Dify"},
		{name: "declared Flowise product", headers: map[string]string{"User-Agent": "axios/1.9.0 Flowise/3.0.0"}, want: "Flowise"},
		{name: "LangChain product", headers: map[string]string{"User-Agent": "langchain-js/0-ChatConnection google-api-nodejs-client/8.9.0"}, want: "LangChain"},
		{name: "declared custom automation", headers: map[string]string{"X-Client-Name": " 自动化平台 nightly-v2.1 ", "User-Agent": "python-requests/2.32.5"}, want: "自动化平台 nightly-v2.1"},
		{name: "custom app wins over platform", headers: map[string]string{"X-Client-Name": "Regression Runner", "User-Agent": "n8n"}, want: "Regression Runner"},
		{name: "CLI wins over explicit name", headers: map[string]string{"X-Client-Name": "Custom Runner", "User-Agent": "deepseek-harness/0.1.5 n8n/1.0.0"}, want: "DeepSeek Harness"},
		{name: "runtime only from compatible language", headers: map[string]string{"X-Stainless-Lang": "js", "X-Stainless-Runtime": "node"}, want: "Node.js"},
		{name: "language without runtime", headers: map[string]string{"X-Stainless-Lang": "go"}, want: "Go"},
		{name: "inconsistent runtime does not override language", headers: map[string]string{"X-Stainless-Lang": "python", "X-Stainless-Runtime": "node"}, want: "Python"},
		{name: "unknown runtime never becomes a label", headers: map[string]string{"X-Stainless-Lang": "js", "X-Stainless-Runtime": "internal-secret"}, want: "JavaScript"},
		{name: "platform substring not trusted", headers: map[string]string{"User-Agent": "not-n8n/1.0.0"}},
		{name: "CodeWhale substring not trusted", headers: map[string]string{"User-Agent": "Mozilla/5.0 (compatible; not-codewhale/0.9.11; +https://github.com/Hmbown/CodeWhale)"}},
		{name: "browser does not imply automation", headers: map[string]string{"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) Chrome/130.0.0.0 Safari/537.36"}},
		{name: "unknown product is not copied", headers: map[string]string{"User-Agent": "private-product-secret/1.0"}},
		{name: "URL is not a client name", headers: map[string]string{"X-Client-Name": "https://private.example/path", "User-Agent": "curl/8.12.0"}, want: "curl"},
		{name: "credentials are not a client name", headers: map[string]string{"X-Client-Name": "sk-proj-not-retained"}},
		{name: "bearer credentials are not a client name", headers: map[string]string{"X-Client-Name": "Bearer not-retained"}},
		{name: "JWT is not a client name", headers: map[string]string{"X-Client-Name": "eyJhbGciOiJIUzI1NiJ9.e30.fake"}},
		{name: "email is not a client name", headers: map[string]string{"X-Client-Name": "private@example.com"}},
		{name: "control characters are not trimmed away", headers: map[string]string{"X-Client-Name": "Runner\n"}},
		{name: "name length is bounded", headers: map[string]string{"X-Client-Name": strings.Repeat("a", 65)}},
		{name: "nil headers are normal HTTP", want: "Normal HTTP"},
		{name: "empty headers are normal HTTP", headers: map[string]string{}, want: "Normal HTTP"},
		{
			name: "inbound HTTP request without source declarations",
			headers: map[string]string{
				"Accept": "*/*", "Accept-Encoding": "gzip", "Authorization": "Bearer not-retained",
				"Content-Length": "123", "Content-Type": "application/json",
				"X-Forwarded-For": "192.0.2.10", "X-Forwarded-Host": "api.example.com", "X-Forwarded-Proto": "https",
			},
			want: "Normal HTTP",
		},
		{
			name: "empty source declarations are normal HTTP",
			headers: map[string]string{
				"User-Agent": "", "Originator": "", "Editor-Version": "", "X-Title": "",
				"HTTP-Referer": "", "X-Client-Name": "", "X-Stainless-Lang": "", "X-Stainless-Runtime": "",
			},
			want: "Normal HTTP",
		},
		{
			name: "whitespace source declarations are normal HTTP",
			headers: map[string]string{
				"User-Agent": " \t ", "Originator": " \t ", "Editor-Version": " \t ", "X-Title": " \t ",
				"HTTP-Referer": " \t ", "X-Client-Name": " \t ", "X-Stainless-Lang": " \t ", "X-Stainless-Runtime": " \t ",
			},
			want: "Normal HTTP",
		},
		{name: "unknown originator stays unknown", headers: map[string]string{"Originator": "private-originator"}},
		{name: "unknown editor stays unknown", headers: map[string]string{"Editor-Version": "private-editor/1.0"}},
		{name: "referer alone insufficient", headers: map[string]string{"HTTP-Referer": "https://cline.bot"}},
		{name: "runtime alone stays unknown", headers: map[string]string{"X-Stainless-Runtime": "private-runtime"}},
		{name: "later source declaration stays unknown", headerValues: http.Header{"User-Agent": {" \t ", "private-product/1.0"}}},
		{name: "missing product version", headers: map[string]string{"User-Agent": "python-requests/"}},
		{name: "unrelated SDK", headers: map[string]string{"User-Agent": "anthropic-python/1.0.0", "x-opencode-session": "arbitrary"}},
		{name: "title alone insufficient", headers: map[string]string{"X-Title": "Cline"}},
		{name: "substring not product", headers: map[string]string{"User-Agent": "not-claude-cli/1.0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Request.Header = test.headerValues.Clone()
			if test.headers != nil {
				c.Request.Header = make(http.Header)
			}
			for key, value := range test.headers {
				c.Request.Header.Set(key, value)
			}
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions}
			defer BeginRequestMetadata(c, info)()
			// Later upstream header overrides must not replace the original source.
			c.Request.Header = make(http.Header)
			c.Request.Header.Set("User-Agent", "opencode/99.0.0")
			other := model.NewLogOther()
			AppendRequestMetadata(c, info, other)
			if test.want == "" {
				assert.Empty(t, other.Snapshot())
			} else {
				assert.Equal(t, map[string]any{"client_tool": test.want}, other.Snapshot())
			}
		})
	}
}

func TestRequestMetadataReasoningLabelsDoNotChangeRelayParameters(t *testing.T) {
	adaptiveBudget, zeroBudget, fixedBudget := -1, 0, 8192
	for _, test := range []struct {
		name    string
		request dto.Request
		effort  string
		want    string
	}{
		{name: "explicit effort", request: &dto.GeneralOpenAIRequest{ReasoningEffort: "high"}, effort: "high", want: "high"},
		{name: "channel override does not replace original", request: &dto.GeneralOpenAIRequest{ReasoningEffort: "low"}, effort: "max", want: "low"},
		{name: "explicit effort remains raw with mode", request: &dto.GeneralOpenAIRequest{ReasoningEffort: "high", THINKING: json.RawMessage(`{"type":"disabled"}`)}, effort: "high", want: "high"},
		{name: "enabled has no invented level", request: &dto.GeneralOpenAIRequest{THINKING: json.RawMessage(`{"type":"enabled"}`)}, want: "enabled"},
		{name: "boolean is not an explicit effort", request: &dto.GeneralOpenAIRequest{Reasoning: json.RawMessage(`{"enabled":false}`)}},
		{name: "budget is not an effort", request: &dto.GeneralOpenAIRequest{Reasoning: json.RawMessage(`{"max_tokens":8192}`)}, effort: "medium"},
		{name: "Claude adaptive", request: &dto.ClaudeRequest{Thinking: &dto.Thinking{Type: "adaptive"}}, effort: "high", want: "adaptive"},
		{name: "Claude disabled stays raw", request: &dto.ClaudeRequest{Thinking: &dto.Thinking{Type: "disabled"}}, effort: "none", want: "disabled"},
		{name: "Gemini adaptive budget", request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: &adaptiveBudget}}}, effort: "high"},
		{name: "Gemini zero budget", request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: &zeroBudget}}}, effort: "none"},
		{name: "Gemini fixed budget", request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: &fixedBudget}}}, effort: "medium"},
		{name: "Gemini original case", request: &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "HIGH"}}}, effort: "high", want: "HIGH"},
		{name: "unspecified remains unknown", request: &dto.GeneralOpenAIRequest{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{Request: test.request, ReasoningEffort: test.effort}
			assert.Equal(t, test.want, requestReasoningLabel(info))
			assert.Equal(t, test.effort, info.ReasoningEffort)
		})
	}
}
