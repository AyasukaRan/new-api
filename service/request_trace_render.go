package service

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// TraceToolCall is one tool invocation a model asked for, normalized across the
// four wire formats.
type TraceToolCall struct {
	Id        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// TraceRenderedResponse is the assistant turn recovered from a captured
// response, whether that response arrived as one JSON document or as a stream
// of fragments.
type TraceRenderedResponse struct {
	Reasoning    string          `json:"reasoning,omitempty"`
	Content      string          `json:"content,omitempty"`
	ToolCalls    []TraceToolCall `json:"tool_calls,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	// Stream reports whether the payload was reassembled from fragments, which
	// is worth showing because a truncated stream renders as a partial turn.
	Stream bool `json:"stream"`
}

func (r *TraceRenderedResponse) isEmpty() bool {
	return r.Reasoning == "" && r.Content == "" && len(r.ToolCalls) == 0 && r.FinishReason == ""
}

// RenderTraceResponse recovers the reasoning, reply text and tool calls from a
// captured response payload. It returns nil when the format is unsupported or
// nothing recognizable was found, and the caller falls back to the raw panel.
//
// Only the primary choice or candidate is rendered. Merging the alternatives an
// n>1 or multi-candidate request produces would interleave their text and
// collide their tool-call fragments; the raw panel stays authoritative for
// those responses.
//
// Parsing happens on read rather than on capture so the stored trace stays the
// bytes that crossed the wire, and so a parser fix applies to traces that were
// already recorded.
func RenderTraceResponse(format string, body string) *TraceRenderedResponse {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	events, isStream := splitStreamPayload(body)

	var rendered *TraceRenderedResponse
	switch types.RelayFormat(format) {
	case types.RelayFormatClaude:
		rendered = renderClaudeResponse(events)
	case types.RelayFormatGemini:
		rendered = renderGeminiResponse(events)
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		rendered = renderOpenAIResponsesResponse(events)
	default:
		rendered = renderOpenAIChatResponse(events)
	}
	if rendered == nil || rendered.isEmpty() {
		return nil
	}
	rendered.Stream = isStream
	return rendered
}

// splitStreamPayload turns a captured payload into the list of JSON documents
// it contains: the `data:` frames of an SSE transcript, or the single document
// of a non-streamed response.
//
// `event:` lines are dropped deliberately — all four formats repeat the event
// name inside the JSON, so the frame names add nothing the parsers need.
func splitStreamPayload(body string) ([]string, bool) {
	if !strings.Contains(body, "data:") {
		return []string{body}, false
	}
	var events []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		events = append(events, payload)
	}
	if len(events) == 0 {
		return []string{body}, false
	}
	return events, true
}

// toolCallAccumulator collects tool calls that arrive in fragments, keyed by
// whatever index or id the format uses, and preserves arrival order.
type toolCallAccumulator struct {
	order []string
	calls map[string]*TraceToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{calls: make(map[string]*TraceToolCall)}
}

func (a *toolCallAccumulator) at(key string) *TraceToolCall {
	if call, ok := a.calls[key]; ok {
		return call
	}
	call := &TraceToolCall{}
	a.calls[key] = call
	a.order = append(a.order, key)
	return call
}

func (a *toolCallAccumulator) result() []TraceToolCall {
	calls := make([]TraceToolCall, 0, len(a.order))
	for _, key := range a.order {
		call := a.calls[key]
		if call.Name == "" && call.Arguments == "" && call.Id == "" {
			continue
		}
		calls = append(calls, *call)
	}
	if len(calls) == 0 {
		return nil
	}
	return calls
}

type openAIChatToolCall struct {
	Index    *int   `json:"index"`
	Id       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIChatTurn is shared by the streamed `delta` and the whole `message`,
// which carry the same fields. Content and reasoning are raw because several
// OpenAI-compatible providers answer with an array of typed parts instead of a
// plain string, and a mistyped field would abort the whole event — losing the
// tool calls and finish_reason recorded alongside it.
type openAIChatTurn struct {
	Content          json.RawMessage      `json:"content"`
	ReasoningContent json.RawMessage      `json:"reasoning_content"`
	Reasoning        json.RawMessage      `json:"reasoning"`
	ToolCalls        []openAIChatToolCall `json:"tool_calls"`
}

type openAIChatChoice struct {
	Index        int             `json:"index"`
	FinishReason string          `json:"finish_reason"`
	Message      *openAIChatTurn `json:"message"`
	Delta        *openAIChatTurn `json:"delta"`
}

// openAIContentText reads a content field that may be a plain string or the
// array of typed parts some providers emit.
func openAIContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := common.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := common.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" || part.Type == "output_text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func renderOpenAIChatResponse(events []string) *TraceRenderedResponse {
	rendered := &TraceRenderedResponse{}
	var content, reasoning strings.Builder
	tools := newToolCallAccumulator()

	for _, event := range events {
		var payload struct {
			Choices []openAIChatChoice `json:"choices"`
		}
		if err := common.UnmarshalJsonStr(event, &payload); err != nil {
			continue
		}
		for _, choice := range payload.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != "" {
				rendered.FinishReason = choice.FinishReason
			}
			for _, turn := range []*openAIChatTurn{choice.Message, choice.Delta} {
				if turn == nil {
					continue
				}
				content.WriteString(openAIContentText(turn.Content))
				reasoning.WriteString(firstNonEmpty(
					openAIContentText(turn.ReasoningContent),
					openAIContentText(turn.Reasoning),
				))
				appendOpenAIChatToolCalls(tools, turn.ToolCalls)
			}
		}
	}

	rendered.Content = content.String()
	rendered.Reasoning = reasoning.String()
	rendered.ToolCalls = tools.result()
	return rendered
}

// appendOpenAIChatToolCalls merges streamed fragments. Streaming keys a call by
// its index, which repeats across chunks; a non-streamed response omits the
// index, so the id keeps separate calls apart.
func appendOpenAIChatToolCalls(tools *toolCallAccumulator, calls []openAIChatToolCall) {
	for _, call := range calls {
		key := call.Id
		if call.Index != nil {
			key = "index:" + strconv.Itoa(*call.Index)
		}
		if key == "" {
			key = "index:" + strconv.Itoa(len(tools.order))
		}
		target := tools.at(key)
		if call.Id != "" {
			target.Id = call.Id
		}
		if call.Function.Name != "" {
			target.Name = call.Function.Name
		}
		target.Arguments += call.Function.Arguments
	}
}

func renderClaudeResponse(events []string) *TraceRenderedResponse {
	rendered := &TraceRenderedResponse{}
	var content, reasoning strings.Builder
	tools := newToolCallAccumulator()

	for _, event := range events {
		var payload struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
				Id       string `json:"id"`
				Name     string `json:"name"`
				Input    any    `json:"input"`
			} `json:"content"`
			StopReason   string `json:"stop_reason"`
			ContentBlock *struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
				Id       string `json:"id"`
				Name     string `json:"name"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJson string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := common.UnmarshalJsonStr(event, &payload); err != nil {
			continue
		}

		// A non-streamed message carries every block at once.
		for _, block := range payload.Content {
			switch block.Type {
			case "text":
				content.WriteString(block.Text)
			case "thinking", "redacted_thinking":
				reasoning.WriteString(block.Thinking)
			case "tool_use":
				call := tools.at("block:" + block.Id)
				call.Id = block.Id
				call.Name = block.Name
				if encoded, err := common.Marshal(block.Input); err == nil {
					call.Arguments = string(encoded)
				}
			}
		}
		if payload.StopReason != "" {
			rendered.FinishReason = payload.StopReason
		}

		if block := payload.ContentBlock; block != nil && payload.Type == "content_block_start" {
			switch block.Type {
			case "text":
				content.WriteString(block.Text)
			case "thinking", "redacted_thinking":
				reasoning.WriteString(block.Thinking)
			case "tool_use":
				call := tools.at("index:" + strconv.Itoa(payload.Index))
				call.Id = block.Id
				call.Name = block.Name
			}
		}
		if delta := payload.Delta; delta != nil {
			switch delta.Type {
			case "text_delta":
				content.WriteString(delta.Text)
			case "thinking_delta":
				reasoning.WriteString(delta.Thinking)
			case "input_json_delta":
				tools.at("index:" + strconv.Itoa(payload.Index)).Arguments += delta.PartialJson
			}
			if delta.StopReason != "" {
				rendered.FinishReason = delta.StopReason
			}
		}
	}

	rendered.Content = content.String()
	rendered.Reasoning = reasoning.String()
	rendered.ToolCalls = tools.result()
	return rendered
}

func renderGeminiResponse(events []string) *TraceRenderedResponse {
	rendered := &TraceRenderedResponse{}
	var content, reasoning strings.Builder
	tools := newToolCallAccumulator()

	for _, event := range events {
		// A non-streamed Gemini call can answer with a bare array of candidate
		// documents, so try both shapes.
		var documents []struct {
			Candidates []geminiCandidate `json:"candidates"`
		}
		if err := common.UnmarshalJsonStr(event, &documents); err != nil {
			var single struct {
				Candidates []geminiCandidate `json:"candidates"`
			}
			if err := common.UnmarshalJsonStr(event, &single); err != nil {
				continue
			}
			documents = append(documents, single)
		}
		for _, document := range documents {
			for _, candidate := range document.Candidates {
				if candidate.Index != 0 {
					continue
				}
				if candidate.FinishReason != "" {
					rendered.FinishReason = candidate.FinishReason
				}
				for _, part := range candidate.Content.Parts {
					if part.FunctionCall != nil && part.FunctionCall.Name != "" {
						call := tools.at("call:" + strconv.Itoa(len(tools.order)))
						call.Name = part.FunctionCall.Name
						if encoded, err := common.Marshal(part.FunctionCall.Args); err == nil {
							call.Arguments = string(encoded)
						}
						continue
					}
					// Gemini marks a reasoning part with `thought`; the text
					// itself sits in the same field as ordinary output.
					if part.Thought {
						reasoning.WriteString(part.Text)
						continue
					}
					content.WriteString(part.Text)
				}
			}
		}
	}

	rendered.Content = content.String()
	rendered.Reasoning = reasoning.String()
	rendered.ToolCalls = tools.result()
	return rendered
}

type geminiCandidate struct {
	Index   int `json:"index"`
	Content struct {
		Parts []struct {
			Text         string `json:"text"`
			Thought      bool   `json:"thought"`
			FunctionCall *struct {
				Name string `json:"name"`
				Args any    `json:"args"`
			} `json:"functionCall"`
		} `json:"parts"`
	} `json:"content"`
	FinishReason string `json:"finishReason"`
}

func renderOpenAIResponsesResponse(events []string) *TraceRenderedResponse {
	// A completed stream repeats the whole response object in its final event,
	// so prefer that over reassembling deltas: it is exact and already carries
	// the finished tool arguments.
	for i := len(events) - 1; i >= 0; i-- {
		var envelope struct {
			Type     string                `json:"type"`
			Response *openAIResponsesShape `json:"response"`
		}
		if err := common.UnmarshalJsonStr(events[i], &envelope); err != nil {
			continue
		}
		if envelope.Type == "response.completed" && envelope.Response != nil {
			return envelope.Response.render()
		}
	}

	// A non-streamed call is the response object itself.
	if len(events) == 1 {
		var response openAIResponsesShape
		if err := common.UnmarshalJsonStr(events[0], &response); err == nil && len(response.Output) > 0 {
			return response.render()
		}
	}

	return renderOpenAIResponsesDeltas(events)
}

type openAIResponsesShape struct {
	Status string `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		CallId  string `json:"call_id"`
		Id      string `json:"id"`
		Args    string `json:"arguments"`
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (s *openAIResponsesShape) render() *TraceRenderedResponse {
	rendered := &TraceRenderedResponse{FinishReason: s.Status}
	if s.IncompleteDetails != nil && s.IncompleteDetails.Reason != "" {
		rendered.FinishReason = s.IncompleteDetails.Reason
	}
	var content, reasoning strings.Builder
	for _, item := range s.Output {
		switch item.Type {
		case "reasoning":
			for _, summary := range item.Summary {
				reasoning.WriteString(summary.Text)
			}
			for _, part := range item.Content {
				reasoning.WriteString(part.Text)
			}
		case "message":
			for _, part := range item.Content {
				content.WriteString(part.Text)
			}
		case "function_call", "custom_tool_call":
			rendered.ToolCalls = append(rendered.ToolCalls, TraceToolCall{
				Id:        firstNonEmpty(item.CallId, item.Id),
				Name:      item.Name,
				Arguments: item.Args,
			})
		}
	}
	rendered.Content = content.String()
	rendered.Reasoning = reasoning.String()
	return rendered
}

// renderOpenAIResponsesDeltas reassembles a stream that never completed, which
// is exactly the case an administrator opens a trace for.
func renderOpenAIResponsesDeltas(events []string) *TraceRenderedResponse {
	rendered := &TraceRenderedResponse{}
	var content, reasoning strings.Builder
	tools := newToolCallAccumulator()
	names := make(map[string]string)

	for _, event := range events {
		var payload struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			ItemId string `json:"item_id"`
			Item   *struct {
				Type   string `json:"type"`
				Id     string `json:"id"`
				CallId string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
		}
		if err := common.UnmarshalJsonStr(event, &payload); err != nil {
			continue
		}
		switch payload.Type {
		case "response.output_text.delta":
			content.WriteString(payload.Delta)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(payload.Delta)
		case "response.output_item.added":
			if payload.Item != nil && payload.Item.Type == "function_call" {
				call := tools.at(payload.Item.Id)
				call.Id = firstNonEmpty(payload.Item.CallId, payload.Item.Id)
				call.Name = payload.Item.Name
				names[payload.Item.Id] = payload.Item.Name
			}
		case "response.function_call_arguments.delta":
			call := tools.at(payload.ItemId)
			if call.Name == "" {
				call.Name = names[payload.ItemId]
			}
			call.Arguments += payload.Delta
		}
	}

	rendered.Content = content.String()
	rendered.Reasoning = reasoning.String()
	rendered.ToolCalls = tools.result()
	return rendered
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
