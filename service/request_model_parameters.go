package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/tidwall/gjson"
)

// Only fixed parameter names and explicitly supplied scalar values reach the
// public log. In particular, response schemas, tool names, prompts, and
// arbitrary provider extensions are never copied.
type requestModelParameters map[string]any

func recordParameter[T bool | int | uint | float32 | float64](p requestModelParameters, key string, value *T) {
	if value != nil {
		p[key] = *value
	}
}

// Nested extensions may also contain full JSON schemas. Bound inspection and
// retain only the selected, typed leaf.
func parameterLeaf(raw []byte, path string) gjson.Result {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return gjson.Result{}
	}
	if path != "" {
		return gjson.GetBytes(raw, path)
	}
	if len(raw) > 128 {
		return gjson.Result{}
	}
	return gjson.ParseBytes(raw)
}

func (p requestModelParameters) number(key string, value gjson.Result) {
	if value.Type == gjson.Number && !math.IsInf(value.Num, 0) && !math.IsNaN(value.Num) {
		p[key] = value.Num
	}
}

func (p requestModelParameters) boolean(key string, value gjson.Result) {
	if value.Type == gjson.True || value.Type == gjson.False {
		p[key] = value.Bool()
	}
}

func (p requestModelParameters) choice(key, value string, choices ...string) {
	for _, choice := range choices {
		if strings.EqualFold(value, choice) {
			p[key] = choice
			return
		}
	}
}

func (p requestModelParameters) effort(value string) {
	p.choice("reasoning_effort", value, "none", "off", "minimal", "low", "medium", "high", "xhigh", "max", "deep", "balanced", "ultra")
}

func (p requestModelParameters) toolChoice(value any) {
	var mode string
	switch v := value.(type) {
	case string:
		mode = v
	case map[string]any:
		mode, _ = v["type"].(string)
	}
	p.choice("tool_choice", mode, "none", "auto", "any", "validated", "required", "function", "tool", "allowed_tools", "custom", "web_search", "web_search_preview", "file_search", "image_generation", "code_interpreter", "mcp", "computer_use_preview", "apply_patch", "shell")
}

func (p requestModelParameters) format(value string) {
	p.choice("response_format", value, "text", "json_object", "json_schema", "text/plain", "application/json", "text/x.enum")
}

func (p requestModelParameters) tier(value string) {
	p.choice("service_tier", value, "auto", "default", "flex", "priority", "scale", "standard_only")
}

func captureRequestModelParameters(request dto.Request) requestModelParameters {
	p := requestModelParameters{}
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if req == nil {
			break
		}
		recordParameter(p, "temperature", req.Temperature)
		recordParameter(p, "top_p", req.TopP)
		recordParameter(p, "top_k", req.TopK)
		recordParameter(p, "n", req.N)
		recordParameter(p, "max_tokens", req.MaxTokens)
		recordParameter(p, "max_completion_tokens", req.MaxCompletionTokens)
		recordParameter(p, "frequency_penalty", req.FrequencyPenalty)
		recordParameter(p, "presence_penalty", req.PresencePenalty)
		recordParameter(p, "parallel_tool_calls", req.ParallelTooCalls)
		// The Chat DTO decodes seed into a float64. Only expose values whose
		// original integer can still be represented unambiguously.
		if req.Seed != nil && math.Abs(*req.Seed) <= 1<<53-1 {
			p["seed"] = *req.Seed
		}
		if req.ReasoningEffort != "" {
			p.effort(req.ReasoningEffort)
		} else {
			p.effort(parameterLeaf(req.Reasoning, "effort").Str)
		}
		p.choice("reasoning_summary", parameterLeaf(req.Reasoning, "summary").Str, "auto", "concise", "detailed", "none")
		p.choice("thinking_mode", parameterLeaf(req.THINKING, "type").Str, "enabled", "disabled", "adaptive", "auto")
		p.boolean("enable_thinking", parameterLeaf(req.Reasoning, "enabled"))
		p.boolean("enable_thinking", parameterLeaf(req.EnableThinking, ""))
		p.number("thinking_budget", parameterLeaf(req.Reasoning, "max_tokens"))
		p.number("thinking_budget", parameterLeaf(req.THINKING, "budget_tokens"))
		p.number("thinking_budget", parameterLeaf(req.ThinkingBudget, ""))
		p.choice("verbosity", parameterLeaf(req.Verbosity, "").Str, "low", "medium", "high")
		p.toolChoice(req.ToolChoice)
		p.tier(parameterLeaf(req.ServiceTier, "").Str)
		if req.ResponseFormat != nil {
			p.format(req.ResponseFormat.Type)
		}
	case *dto.OpenAIResponsesRequest:
		if req == nil {
			break
		}
		recordParameter(p, "temperature", req.Temperature)
		recordParameter(p, "top_p", req.TopP)
		recordParameter(p, "max_output_tokens", req.MaxOutputTokens)
		recordParameter(p, "max_tool_calls", req.MaxToolCalls)
		p.number("top_k", parameterLeaf(req.TopK, ""))
		p.number("frequency_penalty", parameterLeaf(req.FrequencyPenalty, ""))
		p.number("presence_penalty", parameterLeaf(req.PresencePenalty, ""))
		p.boolean("parallel_tool_calls", parameterLeaf(req.ParallelToolCalls, ""))
		p.boolean("enable_thinking", parameterLeaf(req.EnableThinking, ""))
		p.number("thinking_budget", parameterLeaf(req.ThinkingBudget, ""))
		if req.Reasoning != nil {
			p.effort(req.Reasoning.Effort)
			p.choice("reasoning_summary", req.Reasoning.Summary, "auto", "concise", "detailed", "none")
		}
		p.choice("verbosity", parameterLeaf(req.Text, "verbosity").Str, "low", "medium", "high")
		p.format(parameterLeaf(req.Text, "format.type").Str)
		mode := parameterLeaf(req.ToolChoice, "").Str
		if mode == "" {
			mode = parameterLeaf(req.ToolChoice, "type").Str
		}
		p.toolChoice(mode)
		p.tier(req.ServiceTier)
	case *dto.ClaudeRequest:
		if req == nil {
			break
		}
		recordParameter(p, "temperature", req.Temperature)
		recordParameter(p, "top_p", req.TopP)
		recordParameter(p, "top_k", req.TopK)
		recordParameter(p, "max_tokens", req.MaxTokens)
		recordParameter(p, "max_tokens_to_sample", req.MaxTokensToSample)
		p.effort(parameterLeaf(req.OutputConfig, "effort").Str)
		if req.Thinking != nil {
			p.choice("thinking_mode", req.Thinking.Type, "enabled", "disabled", "adaptive")
			recordParameter(p, "thinking_budget", req.Thinking.BudgetTokens)
		}
		p.format(parameterLeaf(req.OutputConfig, "format.type").Str)
		if _, exists := p["response_format"]; !exists {
			p.format(parameterLeaf(req.OutputFormat, "type").Str)
		}
		p.toolChoice(req.ToolChoice)
		if choice, ok := req.ToolChoice.(map[string]any); ok {
			if disabled, supplied := choice["disable_parallel_tool_use"].(bool); supplied {
				p["parallel_tool_calls"] = !disabled
			}
		}
		p.tier(req.ServiceTier)
	case *dto.GeminiChatRequest:
		if req == nil {
			break
		}
		cfg := &req.GenerationConfig
		recordParameter(p, "temperature", cfg.Temperature)
		recordParameter(p, "top_p", cfg.TopP)
		recordParameter(p, "top_k", cfg.TopK)
		recordParameter(p, "max_output_tokens", cfg.MaxOutputTokens)
		recordParameter(p, "candidate_count", cfg.CandidateCount)
		recordParameter(p, "frequency_penalty", cfg.FrequencyPenalty)
		recordParameter(p, "presence_penalty", cfg.PresencePenalty)
		if cfg.Seed != nil {
			if *cfg.Seed < -(1<<53-1) || *cfg.Seed > 1<<53-1 {
				p["seed"] = strconv.FormatInt(*cfg.Seed, 10)
			} else {
				p["seed"] = *cfg.Seed
			}
		}
		p.format(cfg.ResponseMimeType)
		if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil {
			p.toolChoice(string(req.ToolConfig.FunctionCallingConfig.Mode))
		}
		if cfg.ThinkingConfig != nil {
			p.effort(cfg.ThinkingConfig.ThinkingLevel)
			recordParameter(p, "include_thoughts", cfg.ThinkingConfig.IncludeThoughts)
			recordParameter(p, "thinking_budget", cfg.ThinkingConfig.ThinkingBudget)
		}
	}
	return p
}
