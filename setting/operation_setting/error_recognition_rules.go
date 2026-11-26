package operation_setting

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
)

const ErrorRecognitionRulesOptionKey = "ErrorRecognitionRules"

type ErrorRecognitionRule struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Enabled     bool   `json:"enabled"`
	Match       string `json:"match"`
	Pattern     string `json:"pattern"`
	StatusCodes []int  `json:"status_codes,omitempty"`
}

type ErrorSummary struct {
	Label           string `json:"label"`
	RuleID          string `json:"rule_id,omitempty"`
	Matched         bool   `json:"matched"`
	StatusCode      int    `json:"status_code,omitempty"`
	ContextLimit    int64  `json:"context_limit,omitempty"`
	RequestedTokens int64  `json:"requested_tokens,omitempty"`
}

type compiledErrorRule struct {
	rule  ErrorRecognitionRule
	regex *regexp.Regexp
}
type errorRulesSnapshot struct {
	json  string
	rules []compiledErrorRule
}

var errorRecognitionRules atomic.Pointer[errorRulesSnapshot]
var errorRuleNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var defaultErrorRules = sync.OnceValue(func() *errorRulesSnapshot {
	definitions := [][3]string{
		{"context_length", "Context length exceeded", `context[_ ](?:length[_ ]exceeded|window[_ ]exceeded)|maximum context length|prompt is too long|input (?:is |tokens? .{0,40})?too (?:long|large)|上下文.{0,20}(?:超|过长)|(?:输入|提示词).{0,12}(?:过长|超出.*限制)`},
		{"thinking_replay", "Thinking replay mismatch", "invalid.{0,16}signature.{0,24}thinking|思考回放关联不匹配"},
		{"image_format", "Unsupported image format", "unsupported image_url format|uploaded an unsupported image"},
		{"resource_fetch", "Resource fetch failed", "failed to fetch url"},
		{"tool_choice", "Unsupported tool choice", "thinking mode does not support.{0,30}tool_choice|思考模式.{0,20}(?:tool_choice|工具选择)"},
		{"reasoning_content", "Missing reasoning content", "reasoning_(?:text|content).{0,100}(?:must|missing|required)|思考内容.{0,12}(?:缺失|回传)"},
		{"tool_definition", "Invalid tool definition", "invalid schema for function|invalid.{0,20}(?:tool|function).{0,20}schema"},
		{"response_format", "Unsupported response format", "response_format.{0,60}(?:unavailable|not supported|unsupported)"},
		{"accounts", "No available upstream accounts", "no available accounts|无可用账号"},
		{"balance", "Insufficient upstream balance", "insufficient[_ ](?:quota|(?:account )?balance|credits)|(?:balance|credit).{0,20}(?:insufficient|exhaust|deplet)|billing_hard_limit|余额不足|额度(?:不足|耗尽)"},
		{"authentication", "Upstream authentication failed", "authentication fails|invalid[_ ](?:api[_ ]?key|authentication)|api key.{0,60}(?:invalid|expired)|unauthorized|密钥.{0,12}(?:无效|失效)|认证失败"},
		{"tool_mismatch", "Tool call mismatch", "duplicate .?call_id|no tool call found for tool output|missing field.{0,20}tool_call_id|tool_use.{0,120}tool_result|tool_result.{0,120}tool_use|tool[_ ]call.{0,80}(?:not found|missing|must|match)|工具.{0,20}(?:不匹配|缺失|不完整)"},
		{"content_blocked", "Content blocked", "content exists risk|content[_ ](?:policy|filter)|safety.{0,20}(?:block|violation)|内容.{0,12}(?:违规|拦截)"},
		{"security_challenge", "Upstream security challenge", "cf-chl-|challenge-platform|security verification|just a moment|captcha|安全验证|人机验证"},
		{"model", "Model unavailable", "unknown provider for model|supported API model names are|model[_ ]not[_ ]found|model.{0,60}(?:not exist|not available|not supported|does not exist)|no available channel|模型.{0,12}(?:不存在|不可用|不支持)|无可用渠道"},
		{"client_disconnected", "Client disconnected", "stream_end_reason=client_gone|client (?:disconnected|connection closed)|客户端.{0,12}(?:断开|取消)"},
		{"request_cancelled", "Request canceled", "request context done: context canceled"},
		{"model_tools", "Model tools unavailable", "server-side tool calls.{0,180}unavailable for model"},
		{"response_incomplete", "Response incomplete", "stream_response_status=incomplete"},
		{"timeout", "Upstream timed out", "timeout|timed out|deadline exceeded|超时"},
		{"rate_limit", "Rate limited", "rate[_ -]limit|too many requests|请求.{0,8}频繁|限流"},
		{"stream_interrupted", "Stream interrupted", "broken pipe|stream_end_reason=scanner_error|upstream stream disconnected|unexpected eof|stream.{0,30}(?:interrupt|incomplete|closed)|流.{0,8}(?:中断|不完整)"},
		{"connection", "Upstream connection failed", "connection (?:refused|reset)|dial tcp|do request failed|no such host|tls handshake|network is unreachable|连接.{0,12}(?:拒绝|重置|失败)"},
		{"access", "Upstream access denied", "access forbidden|access denied|permission denied|拒绝访问|权限不足"},
		{"unavailable", "Upstream unavailable", "service is too busy|Google服务波动|overload|temporarily unavailable|service unavailable|origin web server.{0,100}(?:invalid|incomplete)|服务.{0,10}(?:不可用|过载)"},
		{"invalid_request", "Invalid request", "either input or instructions must be provided|invalid[_ ](?:request|parameter|max_tokens)|failed to deserialize|unknown variant|invalid.*(?:request|parameter)|invalid temperature|completion limit must|missing (?:required|field)"},
		{"internal_error", "Upstream error", "internal server error|无法安全处理上游响应"},
		{"status_rejected", "Upstream request rejected", "bad response status code [0-9]+"},
	}
	rules := make([]ErrorRecognitionRule, 0, len(definitions))
	for _, d := range definitions {
		rules = append(rules, ErrorRecognitionRule{Name: d[0], Label: d[1], Enabled: true, Match: "regex", Pattern: d[2]})
	}
	rules = append(rules, ErrorRecognitionRule{
		Name: "upstream_unavailable_status", Label: "Upstream unavailable",
		Enabled: true, Match: "regex", Pattern: ".*", StatusCodes: []int{502, 503, 529},
	})
	raw, err := common.Marshal(rules)
	if err != nil {
		panic(err)
	}
	snapshot, err := compileErrorRecognitionRules(string(raw))
	if err != nil {
		panic(err)
	}
	return snapshot
})

func ErrorRecognitionRulesJSONString() string {
	if snapshot := errorRecognitionRules.Load(); snapshot != nil {
		return snapshot.json
	}
	return defaultErrorRules().json
}
func ValidateErrorRecognitionRulesJSON(value string) error {
	_, err := compileErrorRecognitionRules(value)
	return err
}
func LoadErrorRecognitionRulesJSON(value string) error {
	snapshot, err := compileErrorRecognitionRules(value)
	if err != nil {
		return err
	}
	errorRecognitionRules.Store(snapshot)
	return nil
}
func compileErrorRecognitionRules(value string) (*errorRulesSnapshot, error) {
	invalid := func() (*errorRulesSnapshot, error) { return nil, common.NewMessage("Invalid error recognition rules") }
	if len(value) > 128*1024 || !utf8.ValidString(value) {
		return invalid()
	}
	var rawRules []map[string]common.RawMessage
	if common.UnmarshalJsonStr(value, &rawRules) != nil || rawRules == nil || len(rawRules) > 100 {
		return invalid()
	}
	rules := make([]ErrorRecognitionRule, 0, len(rawRules))
	snapshot := &errorRulesSnapshot{}
	names := map[string]bool{}
	for _, raw := range rawRules {
		for key, v := range raw {
			kind := common.GetJsonType(v)
			if (key == "name" || key == "label" || key == "match" || key == "pattern") && kind == "string" || key == "enabled" && kind == "boolean" || key == "status_codes" && kind == "array" {
				continue
			}
			return invalid()
		}
		if _, ok := raw["enabled"]; !ok {
			return invalid()
		}
		encoded, err := common.Marshal(raw)
		if err != nil {
			return invalid()
		}
		var rule ErrorRecognitionRule
		if common.Unmarshal(encoded, &rule) != nil {
			return invalid()
		}
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Label = strings.TrimSpace(rule.Label)
		if !errorRuleNamePattern.MatchString(rule.Name) || names[rule.Name] || rule.Label == "" || len(rule.Label) > 128 || strings.IndexFunc(rule.Name+rule.Label, unicode.IsControl) >= 0 || len(rule.Pattern) == 0 || len(rule.Pattern) > 2048 || len(rule.StatusCodes) > 100 {
			return invalid()
		}
		for _, c := range rule.StatusCodes {
			if c < 400 || c > 599 {
				return invalid()
			}
		}
		names[rule.Name] = true
		compiled := compiledErrorRule{rule: rule}
		switch rule.Match {
		case "regex":
			compiled.regex, err = regexp.Compile("(?i:" + rule.Pattern + ")")
			if err != nil {
				return invalid()
			}
		case "contains", "exact":
		default:
			return invalid()
		}
		rules = append(rules, rule)
		if rule.Enabled {
			snapshot.rules = append(snapshot.rules, compiled)
		}
	}
	encoded, err := common.Marshal(rules)
	if err != nil {
		return invalid()
	}
	snapshot.json = string(encoded)
	return snapshot, nil
}

var errorStatusPattern = regexp.MustCompile(`(?i)(?:status_code["']?\s*[=:]|"status"\s*:|HTTP/?[\d.]*\s+)\s*([45]\d{2})\b`)
var contextLimitPattern = regexp.MustCompile(`(?i)maximum context length (?:is|of)\s*([\d,]+)\s*tokens`)
var requestedTokensPattern = regexp.MustCompile(`(?i)(?:you requested|requested)\s*([\d,]+)\s*tokens`)

// Only explicit public error fields participate; prompts, headers and routing
// diagnostics never become matching input or output.
func RequestErrorText(content, otherJSON string) (string, int) {
	text := content
	if gjson.Valid(content) {
		for _, field := range []string{"error.message", "message"} {
			if value := gjson.Get(content, field); value.Type == gjson.String {
				text = value.String()
				break
			}
		}
	}
	if end := gjson.Get(otherJSON, "stream_status.end_error"); end.Type == gjson.String && end.String() != "" {
		text = end.String()
	}
	if text == "" {
		text = requestErrorCodeHint(otherJSON)
		for _, part := range gjson.Get(otherJSON, "content_parts").Array() {
			if key := part.Get("key"); key.Type == gjson.String {
				text += "\n" + key.String()
			}
			if len(text) > 16384 {
				break
			}
		}
	}
	if len(text) > 16384 {
		text = text[:16384]
	}
	status := int(gjson.Get(otherJSON, "status_code").Int())
	if status < 400 || status > 599 {
		status = 0
	}
	if status == 0 {
		for _, field := range []string{"status_code", "status", "error.status_code"} {
			value := int(gjson.Get(content, field).Int())
			if value >= 400 && value <= 599 {
				status = value
				break
			}
		}
	}
	if status == 0 {
		if match := errorStatusPattern.FindStringSubmatch(text); len(match) > 1 {
			status, _ = strconv.Atoi(match[1])
		}
	}
	return text, status
}
func requestErrorCodeHint(otherJSON string) string {
	code := gjson.Get(otherJSON, "error_code").String()
	switch code {
	case "invalid_request_error", "api_error", "openai_error", "upstream_error":
		return ""
	default:
		return code
	}
}

func ClassifyRequestError(content, otherJSON string) ErrorSummary {
	snapshot := errorRecognitionRules.Load()
	if snapshot == nil {
		snapshot = defaultErrorRules()
	}
	text, status := RequestErrorText(content, otherJSON)
	return snapshot.classify(text, status, "stream_end_reason="+gjson.Get(otherJSON, "stream_status.end_reason").String(), requestErrorCodeHint(otherJSON), "stream_response_status="+gjson.Get(otherJSON, "stream_status.response_status").String())
}
func PreviewErrorRecognition(rules *string, content string, status int) (ErrorSummary, error) {
	snapshot := errorRecognitionRules.Load()
	if snapshot == nil {
		snapshot = defaultErrorRules()
	}
	if rules != nil {
		var err error
		snapshot, err = compileErrorRecognitionRules(*rules)
		if err != nil {
			return ErrorSummary{}, err
		}
	}
	text, parsed := RequestErrorText(content, "")
	if status == 0 {
		status = parsed
	}
	return snapshot.classify(text, status), nil
}
func (snapshot *errorRulesSnapshot) classify(text string, status int, hints ...string) ErrorSummary {
	matchText := text + "\n" + strings.Join(hints, "\n")
	result := ErrorSummary{Label: "Request failed", StatusCode: status}
	for _, compiled := range snapshot.rules {
		rule := compiled.rule
		if len(rule.StatusCodes) > 0 && !slices.Contains(rule.StatusCodes, status) {
			continue
		}
		matched := false
		switch rule.Match {
		case "regex":
			matched = compiled.regex.MatchString(matchText)
		case "contains":
			matched = strings.Contains(strings.ToLower(matchText), strings.ToLower(rule.Pattern))
		case "exact":
			matched = strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(rule.Pattern))
		}
		if !matched {
			continue
		}
		result.Label, result.RuleID, result.Matched = rule.Label, rule.Name, true
		if match := contextLimitPattern.FindStringSubmatch(text); len(match) > 1 {
			result.ContextLimit, _ = strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
		}
		if match := requestedTokensPattern.FindStringSubmatch(text); len(match) > 1 {
			result.RequestedTokens, _ = strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
		}
		return result
	}
	return result
}
