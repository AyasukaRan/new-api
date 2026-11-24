package service

import (
	"context"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/tidwall/gjson"
)

type InputAnalysisCategory struct {
	Count           int      `json:"count"`
	EstimatedTokens int      `json:"estimated_tokens"`
	Texts           []string `json:"texts,omitempty"`
}

type InputAnalysis struct {
	Status          string                            `json:"status"`
	Source          string                            `json:"source"`
	Model           string                            `json:"model"`
	Method          string                            `json:"method"`
	Family          tokenkit.Family                   `json:"family"`
	EstimatedTokens int                               `json:"estimated_tokens"`
	Reasons         []string                          `json:"reasons"`
	Categories      map[string]*InputAnalysisCategory `json:"categories"`
	ctx             context.Context
}

func (a *InputAnalysis) HasReason(reason string) bool { return slices.Contains(a.Reasons, reason) }

func (a *InputAnalysis) incomplete(reason string) {
	if !a.HasReason(reason) {
		a.Reasons = append(a.Reasons, reason)
	}
	a.Status = "partial"
}

func (a *InputAnalysis) text(role, value string) {
	if value == "" || a.ctx.Err() != nil {
		return
	}
	if len(value) >= 5 && strings.EqualFold(value[:5], "data:") && strings.Contains(strings.ToLower(value[:min(len(value), 100)]), ";base64,") {
		a.incomplete("non_text")
		return
	}
	category := a.Categories[role]
	tokens := tokenkit.EstimateFamily(a.Family, value)
	category.EstimatedTokens += tokens
	a.EstimatedTokens += tokens
	if role == "system" || role == "developer" {
		category.Texts = append(category.Texts, value)
	}
}

// Structured tool arguments, results and declarations can contain nested media.
// In the usual JSON-only case count the captured serialization, retaining schema
// keys as well as values. If media is present, include just the textual siblings.
func (a *InputAnalysis) structured(role string, value gjson.Result) {
	if !value.Exists() {
		return
	}
	if value.Type == gjson.String {
		a.text(role, value.Str)
		return
	}
	if value.IsObject() || value.IsArray() {
		for _, marker := range []string{`"inlineData"`, `"inline_data"`, `"image_url"`, `"input_audio"`, `"image_data"`, `"b64_json"`, `"base64"`, `"mime_type"`, `"mimeType"`, `"media_type"`, ";base64,"} {
			if strings.Contains(value.Raw, marker) {
				value.ForEach(func(key, child gjson.Result) bool {
					switch key.Str {
					case "inlineData", "inline_data", "image_url", "image_data", "input_audio", "b64_json":
						a.incomplete("non_text")
					case "data":
						if value.Get("type").Str == "base64" || value.Get("mime_type").Exists() || value.Get("mimeType").Exists() || value.Get("media_type").Exists() {
							a.incomplete("non_text")
						} else {
							a.structured(role, child)
						}
					default:
						a.text(role, key.Str)
						a.structured(role, child)
					}
					return a.ctx.Err() == nil
				})
				return
			}
		}
	}
	a.text(role, value.Raw)
}

// content returns whether this message contains its own role's content.
// Anthropic and Gemini can instead put tool results inside a user message.
func (a *InputAnalysis) content(role string, value gjson.Result) bool {
	if a.ctx.Err() != nil || !value.Exists() {
		return false
	}
	if value.Type == gjson.String {
		// Responses and Chat tools can return a JSON-encoded result containing
		// image bytes. Inspect it only when there is a media marker.
		if role == "tool_results" && strings.Contains(value.Str, "base64") {
			candidate := strings.TrimSpace(value.Str)
			if strings.HasPrefix(candidate, "{") {
				if parsed, ok := inputJSONPrefix(candidate, false); ok {
					a.structured(role, gjson.Parse(parsed))
					return true
				}
			}
		}
		a.text(role, value.Str)
		return value.Str != ""
	}
	if value.IsArray() {
		own := false
		value.ForEach(func(_, part gjson.Result) bool {
			if a.content(role, part) {
				own = true
			}
			return a.ctx.Err() == nil
		})
		return own
	}
	kind := value.Get("type").Str
	switch {
	case kind == "tool_result" || kind == "function_call_output" || kind == "custom_tool_call_output":
		a.Categories["tool_results"].Count++
		output := value.Get("content")
		if !output.Exists() {
			output = value.Get("output")
		}
		a.content("tool_results", output)
		return false
	case value.Get("functionResponse").Exists() || value.Get("function_response").Exists():
		result := value.Get("functionResponse")
		if !result.Exists() {
			result = value.Get("function_response")
		}
		a.Categories["tool_results"].Count++
		a.structured("tool_results", result.Get("response"))
		return false
	case kind == "tool_use" || kind == "server_tool_use" || kind == "function_call" || kind == "custom_tool_call" || value.Get("functionCall").Exists() || value.Get("function_call").Exists():
		call := value
		if nested := value.Get("functionCall"); nested.Exists() {
			call = nested
		}
		if nested := value.Get("function_call"); nested.Exists() {
			call = nested
		}
		a.text("assistant", call.Get("name").Str)
		for _, field := range []string{"arguments", "args", "input"} {
			a.structured("assistant", call.Get(field))
		}
		return true
	case kind == "text" || kind == "input_text" || kind == "output_text" || kind == "summary_text" || (kind == "" && value.Get("text").Type == gjson.String):
		text := value.Get("text")
		if text.Type == gjson.String {
			return a.content(role, text)
		}
	case kind == "thinking":
		return a.content(role, value.Get("thinking"))
	case kind == "reasoning":
		if value.Get("encrypted_content").Exists() {
			a.incomplete("non_text")
		}
		return a.content(role, value.Get("summary"))
	case kind == "refusal":
		return a.content(role, value.Get("refusal"))
	case value.Get("parts").IsArray():
		return a.content(role, value.Get("parts"))
	case value.Get("inlineData").Exists() || value.Get("inline_data").Exists() || value.Get("fileData").Exists() || value.Get("file_data").Exists() ||
		slices.Contains([]string{"image", "image_url", "input_image", "input_audio", "audio", "input_file", "file", "document", "video", "video_url", "redacted_thinking"}, kind):
		a.incomplete("non_text")
		return true
	case role == "tool_results":
		a.structured(role, value)
		return true
	}
	a.incomplete("unrecognized_content")
	return false
}

func (a *InputAnalysis) message(item gjson.Result, gemini bool) {
	role := item.Get("role").Str
	body := item.Get("content")
	if gemini {
		body = item.Get("parts")
		if role == "" {
			role = "user"
		}
		if role == "model" {
			role = "assistant"
		}
	}
	switch role {
	case "system", "developer", "user", "assistant":
	case "tool", "function":
		role = "tool_results"
	default:
		switch item.Get("type").Str {
		case "function_call", "custom_tool_call", "reasoning":
			role, body = "assistant", item
		case "function_call_output", "custom_tool_call_output":
			a.content("tool_results", item)
			return
		case "item_reference", "compaction":
			a.incomplete("referenced_history")
			return
		default:
			a.incomplete("unrecognized_content")
			return
		}
	}
	own := a.content(role, body)
	if role == "assistant" {
		for _, field := range []string{"reasoning_content", "refusal"} {
			if text := item.Get(field); text.Type == gjson.String && text.Str != "" {
				a.text(role, text.Str)
				own = true
			}
		}
		if audio := item.Get("audio"); audio.IsObject() {
			a.incomplete("non_text")
			own = true
		}
		if calls := item.Get("tool_calls"); calls.IsArray() {
			calls.ForEach(func(_, call gjson.Result) bool {
				own = true
				a.structured(role, call.Get("function"))
				return a.ctx.Err() == nil
			})
		}
		if call := item.Get("function_call"); call.IsObject() {
			own = true
			a.structured(role, call)
		}
	}
	if own {
		a.Categories[role].Count++
	}
}

func (a *InputAnalysis) tools(items gjson.Result) {
	items.ForEach(func(_, tool gjson.Result) bool {
		declarations := tool.Get("functionDeclarations")
		if !declarations.Exists() {
			declarations = tool.Get("function_declarations")
		}
		if declarations.IsArray() {
			a.tools(declarations)
		} else if tool.IsObject() {
			a.Categories["tool_definitions"].Count++
			a.structured("tool_definitions", tool)
		} else {
			a.incomplete("unrecognized_content")
		}
		return a.ctx.Err() == nil
	})
}

// inputJSONPrefix bounds nesting before using JSON queries and finds the last
// complete top-level property or top-level array member in an incomplete body.
func inputJSONPrefix(body string, truncated bool) (string, bool) {
	if truncated {
		for _, marker := range []string{"\n\n... [", "\n... ["} {
			body, _, _ = strings.Cut(body, marker)
		}
	}
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "{") {
		return "", false
	}
	depth, end, suffix := 0, 1, "}"
	quoted, escaped, array := false, false, false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if quoted {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth == 2 {
				array = ch == '['
			}
			if depth > 128 {
				return "", false
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return "", false
			}
			if depth == 1 {
				end, suffix = i+1, "}"
			}
			if depth == 2 && array {
				end, suffix = i+1, "]}"
			}
		case ',':
			if depth == 1 {
				end, suffix = i, "}"
			}
			if depth == 2 && array {
				end, suffix = i, "]}"
			}
		}
	}
	if gjson.Valid(body) {
		return body, true
	}
	if truncated {
		prefix := body[:end] + suffix
		if gjson.Valid(prefix) {
			return prefix, true
		}
	}
	return "", false
}

func AnalyzeCapturedInput(ctx context.Context, body, format, path, modelName, source string, truncated bool) *InputAnalysis {
	a := &InputAnalysis{
		Status: "complete", Source: source, Model: modelName, Method: "tokenkit_estimate",
		Family: tokenkit.ModelFamily(modelName), Reasons: []string{},
		Categories: make(map[string]*InputAnalysisCategory, 6), ctx: ctx,
	}
	for _, role := range []string{"system", "developer", "user", "assistant", "tool_results", "tool_definitions"} {
		a.Categories[role] = &InputAnalysisCategory{}
	}
	if truncated {
		a.incomplete("truncated")
	}
	if body == "" {
		if !truncated {
			a.incomplete("not_recorded")
			a.Status = "unavailable"
		}
		return a
	}
	var valid bool
	body, valid = inputJSONPrefix(body, truncated)
	if !valid {
		a.incomplete("unrecognized_content")
		if !truncated {
			a.Status = "unavailable"
		}
		return a
	}
	v := gjson.Parse(body)
	if v.Get("type").Str == "response.create" && v.Get("response").IsObject() {
		v = v.Get("response")
	}
	path, _, _ = strings.Cut(path, "?")
	if model := v.Get("model"); model.Type == gjson.String && len(model.Str) <= 192 && model.Str != "" {
		a.Model = model.Str
	} else if _, name, found := strings.Cut(path, "/models/"); found {
		name, _, _ = strings.Cut(name, ":")
		if len(name) <= 192 && name != "" {
			a.Model = name
		}
	}
	a.Family = tokenkit.ModelFamily(a.Model)
	if format == "" {
		switch {
		case strings.HasSuffix(path, "/responses"), strings.HasSuffix(path, "/responses/compact"):
			format = types.RelayFormatOpenAIResponses
		case strings.HasSuffix(path, "/messages"):
			format = types.RelayFormatClaude
		case strings.HasSuffix(path, "/chat/completions"):
			format = string(types.RelayFormatOpenAI)
		case strings.HasSuffix(path, ":generateContent"), strings.HasSuffix(path, ":streamGenerateContent"):
			format = types.RelayFormatGemini
		case path != "":
		case v.Get("messages").IsArray():
			format = string(types.RelayFormatOpenAI)
		case v.Get("contents").IsArray() || v.Get("systemInstruction").Exists() || v.Get("system_instruction").Exists():
			format = types.RelayFormatGemini
		case v.Get("instructions").Exists() || v.Get("previous_response_id").Exists() || v.Get("input.0.role").Exists():
			format = types.RelayFormatOpenAIResponses
		}
	}
	var instructions, messages gjson.Result
	switch format {
	case string(types.RelayFormatOpenAI), types.RelayFormatClaude:
		instructions, messages = v.Get("system"), v.Get("messages")
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		instructions, messages = v.Get("instructions"), v.Get("input")
	case types.RelayFormatGemini:
		instructions, messages = v.Get("systemInstruction"), v.Get("contents")
		if !instructions.Exists() {
			instructions = v.Get("system_instruction")
		}
	default:
		if !truncated || format != "" {
			a.incomplete("unsupported")
			a.Status = "unavailable"
		}
		return a
	}
	for _, key := range []string{"previous_response_id", "conversation", "cachedContent", "cached_content", "prompt.id"} {
		if v.Get(key).Exists() {
			a.incomplete("referenced_history")
			break
		}
	}
	// Responses accepts role-bearing input messages as instructions. Claude
	// also accepts an array here, but its text blocks form one system message.
	plainInstructions := false
	if instructions.IsArray() {
		instructions.ForEach(func(_, item gjson.Result) bool {
			if item.Get("role").Exists() {
				a.message(item, false)
			} else if a.content("system", item) {
				plainInstructions = true
			}
			return ctx.Err() == nil
		})
	} else {
		plainInstructions = a.content("system", instructions)
	}
	if plainInstructions {
		a.Categories["system"].Count++
	}
	if messages.Type == gjson.String && format == types.RelayFormatOpenAIResponses {
		if a.content("user", messages) {
			a.Categories["user"].Count++
		}
	} else if messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			a.message(message, format == types.RelayFormatGemini)
			return ctx.Err() == nil
		})
	} else if messages.Exists() {
		a.incomplete("unrecognized_content")
	}
	for _, name := range []string{"tools", "functions"} {
		if definitions := v.Get(name); definitions.IsArray() {
			a.tools(definitions)
		}
	}
	if a.EstimatedTokens == 0 && len(a.Reasons) == 0 {
		a.incomplete("unrecognized_content")
		a.Status = "unavailable"
	}
	return a
}
