package service

import (
	"bytes"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const requestMetadataContextKey = "relay_request_metadata"

// Only names are retained, never tool arguments, response text or credentials.
// A cap applies to each SSE event, not the whole response, so long streams do
// not lose later tool calls when the independent request trace is truncated.
const requestMetadataMaxFrameBytes = 1 << 20
const requestMetadataMaxTools = 128
const requestMetadataMaxNameBytes = 256

type requestMetadata struct {
	mu               sync.Mutex
	format           types.RelayFormat
	stream           bool
	clientTool       string
	reasoningLabel   string
	buffer           []byte
	event            []byte
	droppingLine     bool
	droppingEvent    bool
	partial          bool
	seen             bool
	terminal         bool
	names            []string
	chatNames        map[string]string
	droppedChatNames map[string]bool
}

// BeginRequestMetadata observes the client-facing response independently of
// trace retention. Install after request parsing and before any relay attempt;
// successful handlers finish writing their response before recording usage.
func BeginRequestMetadata(c *gin.Context, info *relaycommon.RelayInfo) func() {
	if c == nil || c.Request == nil || info == nil {
		return func() {}
	}
	metadata := &requestMetadata{format: info.RelayFormat, stream: info.IsStream, clientTool: identifyClientTool(c.Request.Header), reasoningLabel: strings.Clone(requestReasoningLabel(info))}
	c.Set(requestMetadataContextKey, metadata)
	supported := info.RelayFormat == types.RelayFormatClaude ||
		info.RelayFormat == types.RelayFormatGemini && info.RelayMode != relayconstant.RelayModeEmbeddings ||
		info.RelayFormat == types.RelayFormatOpenAIResponses && info.RelayMode == relayconstant.RelayModeResponses ||
		info.RelayFormat == types.RelayFormatOpenAI && info.RelayMode == relayconstant.RelayModeChatCompletions
	if !supported || info.ClientWs != nil {
		return func() {}
	}
	writer := &requestMetadataWriter{ResponseWriter: c.Writer, metadata: metadata}
	c.Writer = writer
	return func() {
		if c.Writer == writer {
			c.Writer = writer.ResponseWriter
		}
	}
}

type requestMetadataWriter struct {
	gin.ResponseWriter
	metadata *requestMetadata
}

// Each usage/error log belongs to one channel attempt. A retry must not inherit
// tool names or an incomplete observation from an earlier failed attempt.
func ResetRequestToolObservation(c *gin.Context) {
	value, _ := c.Get(requestMetadataContextKey)
	metadata, _ := value.(*requestMetadata)
	if metadata == nil {
		return
	}
	metadata.mu.Lock()
	defer metadata.mu.Unlock()
	metadata.buffer, metadata.event, metadata.names = nil, nil, nil
	metadata.chatNames, metadata.droppedChatNames = nil, nil
	metadata.droppingLine, metadata.droppingEvent, metadata.partial = false, false, false
	metadata.seen, metadata.terminal = false, false
}

// ObserveRequestMetadataEvent records one accepted Responses WebSocket event.
// Socket relays bypass Gin's writer, so they use the same bounded parser here
// before settling the current response.create request.
func ObserveRequestMetadataEvent(c *gin.Context, data []byte) {
	value, _ := c.Get(requestMetadataContextKey)
	metadata, _ := value.(*requestMetadata)
	if metadata == nil {
		return
	}
	metadata.mu.Lock()
	defer metadata.mu.Unlock()
	if len(data) > requestMetadataMaxFrameBytes {
		metadata.partial = true
		return
	}
	metadata.acceptResponse(data)
}

func (w *requestMetadataWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *requestMetadataWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.metadata.observe(data[:n], w.Header().Get("Content-Type"), err != nil)
	return n, err
}

func (w *requestMetadataWriter) WriteString(data string) (int, error) {
	n, err := w.ResponseWriter.WriteString(data)
	w.metadata.observe([]byte(data[:n]), w.Header().Get("Content-Type"), err != nil)
	return n, err
}

func (m *requestMetadata) observe(data []byte, contentType string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partial = m.partial || failed
	if strings.Contains(contentType, "text/event-stream") {
		m.stream = true
	} else if strings.Contains(contentType, "application/json") {
		m.stream = false
	}
	if !m.stream {
		if len(m.buffer)+len(data) > requestMetadataMaxFrameBytes {
			m.partial = true
			m.buffer = nil
			m.droppingEvent = true
		}
		if !m.droppingEvent {
			m.buffer = append(m.buffer, data...)
		}
		return
	}
	for len(data) > 0 {
		line, rest, complete := bytes.Cut(data, []byte{'\n'})
		if !m.droppingLine && len(m.buffer)+len(line) <= requestMetadataMaxFrameBytes {
			m.buffer = append(m.buffer, line...)
		} else {
			m.partial, m.droppingLine, m.droppingEvent = true, true, true
			m.buffer = nil
		}
		if !complete {
			return
		}
		if !m.droppingLine {
			m.acceptSSELine(m.buffer)
		}
		m.buffer = m.buffer[:0]
		m.droppingLine = false
		data = rest
	}
}

func (m *requestMetadata) acceptSSELine(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		if !m.droppingEvent && len(m.event) > 0 {
			m.acceptResponse(bytes.TrimSpace(m.event))
		}
		m.event = m.event[:0]
		m.droppingEvent = false
		return
	}
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok && !m.droppingEvent {
		data = bytes.TrimPrefix(data, []byte{' '})
		if len(m.event)+len(data)+1 > requestMetadataMaxFrameBytes {
			m.partial, m.droppingEvent = true, true
			m.event = nil
			return
		}
		m.event = append(m.event, data...)
		m.event = append(m.event, '\n')
	}
}

func (m *requestMetadata) addTool(name string) {
	if name == "" {
		return
	}
	if len(name) > requestMetadataMaxNameBytes || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		m.partial = true
		return
	}
	if slices.Contains(m.names, name) {
		return
	}
	if len(m.names) >= requestMetadataMaxTools {
		m.partial = true
		return
	}
	m.names = append(m.names, strings.Clone(name))
}

func (m *requestMetadata) acceptResponse(data []byte) {
	if bytes.Equal(data, []byte("[DONE]")) {
		m.terminal = true
		return
	}
	if !gjson.ValidBytes(data) {
		m.partial = true
		return
	}
	payload := gjson.ParseBytes(data)
	switch m.format {
	case types.RelayFormatOpenAI:
		choices := payload.Get("choices")
		if !choices.IsArray() {
			return
		}
		m.seen = true
		for i, choice := range choices.Array() {
			index := choice.Get("index")
			choiceKey := strconv.Itoa(i)
			if index.Type == gjson.Number {
				if len(index.Raw) > 20 {
					m.partial = true
					continue
				}
				choiceKey = index.Raw
			}
			for _, field := range []string{"message", "delta"} {
				turn := choice.Get(field)
				calls := turn.Get("tool_calls").Array()
				if legacy := turn.Get("function_call"); legacy.IsObject() {
					m.acceptChatName(choiceKey+":legacy", legacy.Get("name"), field == "delta")
				}
				for j, call := range calls {
					key := strconv.Itoa(j)
					if index := call.Get("index"); index.Type == gjson.Number {
						if len(index.Raw) > 20 {
							m.partial = true
							continue
						}
						key = index.Raw
					}
					m.acceptChatName(choiceKey+":"+key, call.Get("function.name"), field == "delta")
				}
			}
		}
	case types.RelayFormatClaude:
		switch payload.Get("type").String() {
		case "message":
			m.seen = true
			m.terminal = true
			for _, block := range payload.Get("content").Array() {
				m.acceptClaudeTool(block)
			}
		case "message_start", "content_block_delta", "message_delta", "content_block_stop":
			m.seen = true
		case "content_block_start":
			m.seen = true
			m.acceptClaudeTool(payload.Get("content_block"))
		case "message_stop":
			m.seen, m.terminal = true, true
		}
	case types.RelayFormatGemini:
		documents := []gjson.Result{payload}
		if payload.IsArray() {
			documents = payload.Array()
		}
		for _, document := range documents {
			if document.Get("candidates").IsArray() {
				m.seen = true
			}
			for _, candidate := range document.Get("candidates").Array() {
				if candidate.Get("finishReason").String() != "" {
					m.terminal = true
				}
				for _, part := range candidate.Get("content.parts").Array() {
					if function := part.Get("functionCall"); function.IsObject() {
						if name := function.Get("name"); name.Type == gjson.String && name.String() != "" {
							m.addTool(name.String())
						} else {
							m.partial = true
						}
					}
					if part.Get("executableCode").IsObject() || part.Get("codeExecutionResult").IsObject() {
						m.addTool("code_execution")
					}
				}
				if len(candidate.Get("groundingMetadata.webSearchQueries").Array()) > 0 {
					m.addTool("web_search")
				}
			}
		}
	case types.RelayFormatOpenAIResponses:
		eventType := payload.Get("type").String()
		if strings.HasPrefix(eventType, "response.") {
			m.seen = true
		}
		if eventType == "response.output_item.added" || eventType == "response.output_item.done" {
			m.acceptResponsesTool(payload.Get("item"), eventType == "response.output_item.done")
		}
		if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" || eventType == "response.failed" || eventType == "response.cancelled" || eventType == "response.canceled" {
			m.terminal = true
			m.partial = m.partial || eventType != "response.completed" && eventType != "response.done"
			payload = payload.Get("response")
		}
		if output := payload.Get("output"); output.IsArray() {
			m.seen = true
			if status := payload.Get("status").String(); status == "incomplete" || status == "failed" {
				m.partial = true
			}
			for _, item := range output.Array() {
				m.acceptResponsesTool(item, true)
			}
		}
	}
}

func (m *requestMetadata) acceptChatName(key string, name gjson.Result, fragment bool) {
	if len(key) > 64 {
		m.partial = true
		return
	}
	if m.droppedChatNames[key] {
		return
	}
	if m.chatNames == nil {
		m.chatNames = make(map[string]string)
		m.droppedChatNames = make(map[string]bool)
	}
	if _, exists := m.chatNames[key]; !exists {
		if len(m.chatNames)+len(m.droppedChatNames) >= requestMetadataMaxTools {
			m.partial = true
			return
		}
		m.chatNames[key] = ""
	}
	if name.Type != gjson.String || name.String() == "" {
		return
	}
	value := name.String()
	if fragment {
		value = m.chatNames[key] + value
	}
	if len(value) > requestMetadataMaxNameBytes || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		m.partial = true
		delete(m.chatNames, key)
		m.droppedChatNames[key] = true
		return
	}
	m.chatNames[key] = strings.Clone(value)
}

func (m *requestMetadata) acceptClaudeTool(block gjson.Result) {
	if kind := block.Get("type").String(); kind == "tool_use" || kind == "server_tool_use" {
		if name := block.Get("name"); name.Type == gjson.String && name.String() != "" {
			m.addTool(name.String())
		} else {
			m.partial = true
		}
	}
}

func (m *requestMetadata) acceptResponsesTool(item gjson.Result, complete bool) {
	kind := item.Get("type").String()
	switch kind {
	case "function_call", "custom_tool_call", "mcp_call":
		if name := item.Get("name"); name.Type == gjson.String && name.String() != "" {
			m.addTool(name.String())
		} else if complete {
			m.partial = true
		}
	case "web_search_call", "file_search_call", "code_interpreter_call", "computer_call", "image_generation_call", "local_shell_call", "shell_call", "apply_patch_call":
		m.addTool(strings.TrimSuffix(kind, "_call"))
	}
}

// AppendRequestMetadata uses public log metadata: owners can see their own
// client and requested tool names, but no raw headers or tool arguments.
func AppendRequestMetadata(c *gin.Context, info *relaycommon.RelayInfo, other *model.LogOther) {
	if other == nil || c == nil {
		return
	}
	value, _ := c.Get(requestMetadataContextKey)
	metadata, _ := value.(*requestMetadata)
	if metadata == nil {
		if effort := requestReasoningLabel(info); effort != "" {
			other.SetPublic("reasoning_effort", effort)
		}
		return
	}
	metadata.mu.Lock()
	defer metadata.mu.Unlock()
	if metadata.reasoningLabel != "" {
		other.SetPublic("reasoning_effort", metadata.reasoningLabel)
	}
	if metadata.clientTool != "" {
		other.SetPublic("client_tool", metadata.clientTool)
	}
	if !metadata.stream {
		if len(metadata.buffer) > 0 {
			metadata.acceptResponse(metadata.buffer)
			metadata.buffer = nil
		}
	} else {
		if len(metadata.buffer) > 0 && !metadata.droppingLine {
			metadata.acceptSSELine(metadata.buffer)
			metadata.buffer = nil
		}
		metadata.acceptSSELine(nil)
	}
	for _, name := range metadata.chatNames {
		if name == "" {
			metadata.partial = true
			continue
		}
		metadata.addTool(name)
	}
	if metadata.seen || metadata.partial {
		state := "complete"
		if metadata.partial || metadata.stream && !metadata.terminal {
			state = "partial"
		}
		names := append([]string{}, metadata.names...)
		slices.Sort(names)
		other.SetPublic("invoked_tools", names)
		other.SetPublic("tool_observation", state)
	}
}

// Preserve explicit client values for display. Provider effort vocabularies and
// budgets are not interchangeable; RelayInfo can contain a derived or overridden
// value used by adaptors, which must not replace the original request here.
func requestReasoningLabel(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	var mode, effort string
	switch request := info.Request.(type) {
	case *dto.ClaudeRequest:
		if request != nil {
			effort = request.GetEfforts()
			if request.Thinking != nil {
				mode = request.Thinking.Type
			}
		}
	case *dto.GeneralOpenAIRequest:
		if request != nil {
			effort = request.ReasoningEffort
			if effort == "" {
				if value := gjson.GetBytes(request.Reasoning, "effort"); value.Type == gjson.String {
					effort = value.String()
				}
			}
			if value := gjson.GetBytes(request.THINKING, "type"); value.Type == gjson.String {
				mode = value.String()
			}
		}
	case *dto.OpenAIResponsesRequest:
		if request != nil && request.Reasoning != nil {
			effort = request.Reasoning.Effort
		}
	case *dto.GeminiChatRequest:
		if request != nil && request.GenerationConfig.ThinkingConfig != nil {
			effort = request.GenerationConfig.ThinkingConfig.ThinkingLevel
		}
	}
	if effort == "" {
		effort = mode
	}
	if len(effort) > 32 || strings.IndexFunc(effort, unicode.IsControl) >= 0 {
		return ""
	}
	return effort
}

func identifyClientTool(header http.Header) string {
	// Inspect original request headers only. Adaptors can inject a different
	// User-Agent/originator upstream, which does not identify the caller.
	userAgent := strings.ToLower(header.Get("User-Agent"))
	for token := range strings.FieldsSeq(userAgent) {
		product, version, ok := strings.Cut(token, "/")
		if !ok || version == "" {
			continue
		}
		switch {
		case product == "codex_cli_rs", product == "codex-cli", product == "codex-tui":
			return "Codex CLI"
		case product == "codex_vscode":
			return "Codex VS Code"
		case product == "claude-cli":
			return "Claude Code"
		case product == "geminicli", strings.HasPrefix(product, "geminicli-"):
			return "Gemini CLI"
		case product == "opencode":
			return "OpenCode"
		case product == "deepseek-harness":
			return "DeepSeek Harness"
		case product == "cline":
			return "Cline"
		}
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("originator"))) {
	case "codex_cli_rs", "codex-tui":
		return "Codex CLI"
	case "codex_vscode":
		return "Codex VS Code"
	case "cline":
		return "Cline"
	}
	if editor := strings.ToLower(header.Get("Editor-Version")); strings.HasPrefix(editor, "aider/") && len(editor) > len("aider/") {
		return "Aider"
	}
	title := strings.ToLower(strings.TrimSpace(header.Get("X-Title")))
	referer := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(header.Get("HTTP-Referer"))), "/")
	if title == "cline" && referer == "https://cline.bot" {
		return "Cline"
	}
	if title == "aider" && referer == "https://aider.chat" {
		return "Aider"
	}
	if name := requestClientName(header.Get("X-Client-Name")); name != "" {
		return name
	}

	// Product names are allowlisted: a generic HTTP client cannot identify the
	// application built on top of it. Keep the most specific observed source.
	application, sdk, httpClient, runtime := "", "", "", ""
	for field := range strings.FieldsSeq(userAgent) {
		token := strings.Trim(field, "();,")
		product, version, hasVersion := strings.Cut(token, "/")
		if hasVersion && version == "" {
			continue
		}
		if !hasVersion && token != "n8n" && token != "node-fetch" && token != "undici" && token != "node" && token != "ruby" {
			continue
		}
		if application == "" {
			application = requestSourceApplications[product]
		}
		if sdk == "" {
			sdk = requestSourceSDKs[token]
			// The .NET SDK uses the assembly version rather than a language
			// suffix, accompanied by its framework in the platform comment.
			if product == "openai" && version[0] >= '0' && version[0] <= '9' && strings.Contains(userAgent, "(.net ") {
				sdk = "OpenAI .NET SDK"
			}
		}
		if httpClient == "" {
			httpClient = requestSourceHTTPClients[product]
		}
		if runtime == "" {
			runtime = requestSourceRuntimes[product]
		}
	}
	if application != "" {
		return application
	}
	if sdk != "" {
		return sdk
	}
	if httpClient != "" {
		return httpClient
	}
	if runtime != "" {
		return runtime
	}
	language := strings.ToLower(strings.TrimSpace(header.Get("X-Stainless-Lang")))
	if language == "js" || language == "ts" {
		switch strings.ToLower(strings.TrimSpace(header.Get("X-Stainless-Runtime"))) {
		case "node":
			return "Node.js"
		case "deno":
			return "Deno"
		case "bun":
			return "Bun"
		}
	}
	if source := requestSourceLanguages[language]; source != "" {
		return source
	}
	// Unrecognized declarations stay unknown; only requests without any source
	// declaration are ordinary HTTP requests.
	for _, key := range []string{"User-Agent", "Originator", "Editor-Version", "X-Title", "HTTP-Referer", "X-Client-Name", "X-Stainless-Lang", "X-Stainless-Runtime"} {
		for _, value := range header.Values(key) {
			if strings.TrimSpace(value) != "" {
				return ""
			}
		}
	}
	return "Normal HTTP"
}

// These names describe an explicitly declared product, not a guess from request
// content. Applications which hide behind an SDK can opt in with X-Client-Name.
var requestSourceApplications = map[string]string{
	"codewhale":          "CodeWhale",
	"n8n":                "n8n",
	"dify":               "Dify",
	"flowise":            "Flowise",
	"langchain":          "LangChain",
	"langchain-js":       "LangChain",
	"langchain-python":   "LangChain",
	"langchain-azure-ai": "LangChain",
}

var requestSourceSDKs = map[string]string{
	"openai/python":                 "OpenAI Python SDK",
	"asyncopenai/python":            "OpenAI Python SDK",
	"azureopenai/python":            "OpenAI Python SDK",
	"asyncazureopenai/python":       "OpenAI Python SDK",
	"anthropic/python":              "Anthropic Python SDK",
	"asyncanthropic/python":         "Anthropic Python SDK",
	"openai/js":                     "OpenAI JavaScript SDK",
	"azureopenai/js":                "OpenAI JavaScript SDK",
	"anthropic/js":                  "Anthropic JavaScript SDK",
	"openai/go":                     "OpenAI Go SDK",
	"anthropic/go":                  "Anthropic Go SDK",
	"openaiclientimpl/java":         "OpenAI Java SDK",
	"openaiclientasyncimpl/java":    "OpenAI Java SDK",
	"anthropicclientimpl/java":      "Anthropic Java SDK",
	"anthropicclientasyncimpl/java": "Anthropic Java SDK",
	"openai::client/ruby":           "OpenAI Ruby SDK",
	"anthropic::client/ruby":        "Anthropic Ruby SDK",
	"anthropic/php":                 "Anthropic PHP SDK",
	"anthropicclient/c#":            "Anthropic .NET SDK",
}

var requestSourceHTTPClients = map[string]string{
	"python-requests":   "Python Requests",
	"python-httpx":      "Python HTTPX",
	"aiohttp":           "Python aiohttp",
	"python-urllib":     "Python urllib",
	"curl":              "curl",
	"wget":              "Wget",
	"postmanruntime":    "Postman",
	"insomnia":          "Insomnia",
	"httpie":            "HTTPie",
	"axios":             "Axios",
	"node-fetch":        "node-fetch",
	"undici":            "Undici",
	"go-http-client":    "Go HTTP Client",
	"java-http-client":  "Java HTTP Client",
	"okhttp":            "OkHttp",
	"apache-httpclient": "Apache HttpClient",
	"guzzlehttp":        "Guzzle",
}

var requestSourceRuntimes = map[string]string{
	"python":            "Python",
	"node":              "Node.js",
	"ruby":              "Ruby",
	"java":              "Java",
	"powershell":        "PowerShell",
	"windowspowershell": "PowerShell",
}

var requestSourceLanguages = map[string]string{
	"python": "Python",
	"js":     "JavaScript",
	"ts":     "TypeScript",
	"go":     "Go",
	"java":   "Java",
	"kotlin": "Kotlin",
	"ruby":   "Ruby",
	"php":    "PHP",
	"csharp": ".NET",
	"c#":     ".NET",
	"dotnet": ".NET",
	"rust":   "Rust",
}

// X-Client-Name is a public, caller-declared display name, never an identity or
// authorization signal. Do not retain arbitrary header values as log labels.
func requestClientName(value string) string {
	if len(value) > 64 || !utf8.ValidString(value) {
		return ""
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune(" ._-", character) {
			return ""
		}
	}
	name := strings.TrimSpace(value)
	if name == "" {
		return ""
	}
	for word := range strings.FieldsSeq(strings.ToLower(name)) {
		if word == "bearer" || word == "basic" {
			return ""
		}
		for _, prefix := range []string{"sk-", "sk_", "ak-", "ak_", "ghp_", "github_pat_", "xoxb-", "xoxp-", "gsk_", "aiza", "eyj"} {
			if strings.HasPrefix(word, prefix) {
				return ""
			}
		}
	}
	return name
}
