package controller

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexModel struct {
	Slug                              string                `json:"slug"`
	DisplayName                       string                `json:"display_name"`
	DefaultReasoningLevel             string                `json:"default_reasoning_level,omitempty"`
	SupportedReasoningLevels          []codexReasoningLevel `json:"supported_reasoning_levels"`
	ShellType                         string                `json:"shell_type"`
	Visibility                        string                `json:"visibility"`
	SupportedInAPI                    bool                  `json:"supported_in_api"`
	Priority                          int                   `json:"priority"`
	SupportVerbosity                  bool                  `json:"support_verbosity"`
	DefaultVerbosity                  string                `json:"default_verbosity,omitempty"`
	SupportsReasoningSummaryParameter bool                  `json:"supports_reasoning_summary_parameter"`
	DefaultReasoningSummary           string                `json:"default_reasoning_summary,omitempty"`
	ApplyPatchToolType                string                `json:"apply_patch_tool_type,omitempty"`
	ContextWindow                     int                   `json:"context_window,omitempty"`
	MaxContextWindow                  int                   `json:"max_context_window,omitempty"`
	BaseInstructions                  string                `json:"base_instructions"`
	TruncationPolicy                  struct {
		Mode  string `json:"mode"`
		Limit int    `json:"limit"`
	} `json:"truncation_policy"`
	ExperimentalSupportedTools []string `json:"experimental_supported_tools"`
	InputModalities            []string `json:"input_modalities"`
}

// buildCodexModels only projects models already authorized by ListModels.
func buildCodexModels(models []dto.OpenAIModels) []codexModel {
	models = slices.Clone(models)
	slices.SortFunc(models, func(a, b dto.OpenAIModels) int { return strings.Compare(a.Id, b.Id) })
	catalog := make([]codexModel, 0, len(models))
	for _, model := range models {
		if common.IsImageGenerationModel(model.Id) {
			continue
		}
		if len(model.SupportedEndpointTypes) > 0 && !slices.ContainsFunc(model.SupportedEndpointTypes, func(endpoint constant.EndpointType) bool {
			return endpoint == constant.EndpointTypeOpenAI || endpoint == constant.EndpointTypeOpenAIResponse ||
				endpoint == constant.EndpointTypeAnthropic || endpoint == constant.EndpointTypeGemini
		}) {
			continue
		}
		entry := codexModel{
			Slug:                       model.Id,
			DisplayName:                model.Id,
			SupportedReasoningLevels:   []codexReasoningLevel{},
			ShellType:                  "default",
			Visibility:                 "list",
			SupportedInAPI:             true,
			Priority:                   len(catalog),
			BaseInstructions:           "You are a coding assistant working with the user in their project. Complete the work the user authorizes using the available tools. Read the relevant code and project instructions before making changes. Prefer focused, maintainable changes that fit existing conventions. Preserve unrelated user edits and avoid destructive actions without authorization. Investigate tool errors and correct their causes when possible. Verify the affected behavior with appropriate checks. Do not expose credentials or secrets. Ask for clarification when a necessary decision cannot be inferred safely. Report what changed, what you verified, and any remaining limitations honestly; never claim that a check ran or succeeded without evidence.",
			ExperimentalSupportedTools: []string{},
			InputModalities:            []string{"text"},
		}
		entry.TruncationPolicy.Mode = "bytes"
		entry.TruncationPolicy.Limit = 10000

		// Exact IDs from Codex 0.154.0/0.167.0's bundled catalogs; aliases and unknown models
		// keep conservative defaults instead of inheriting capabilities by prefix.
		reasoningLevels := 0
		switch model.Id {
		case "gpt-5.2", "gpt-5.4", "gpt-5.4-mini", "gpt-5.5":
			reasoningLevels, entry.DefaultReasoningLevel = 4, "medium"
		case "gpt-5.6-sol", "gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-daybreak-blue-latest":
			reasoningLevels, entry.DefaultReasoningLevel = 5, "low"
		case "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-sol", "gpt-6-luna", "gpt-daybreak-red-latest", "codex-auto-review":
			reasoningLevels, entry.DefaultReasoningLevel = 5, "medium"
		}
		if reasoningLevels > 0 {
			// Ultra requires Codex delegation metadata and is not a direct upstream effort.
			entry.SupportedReasoningLevels = []codexReasoningLevel{
				{Effort: "low", Description: "Faster responses with less reasoning"},
				{Effort: "medium", Description: "Balanced reasoning and speed"},
				{Effort: "high", Description: "More reasoning for complex tasks"},
				{Effort: "xhigh", Description: "Extra reasoning for difficult tasks"},
				{Effort: "max", Description: "Maximum reasoning depth"},
			}[:reasoningLevels]
			entry.SupportVerbosity = true
			entry.DefaultVerbosity = "low"
			entry.SupportsReasoningSummaryParameter = true
			entry.DefaultReasoningSummary = "none"
			entry.ApplyPatchToolType = "freeform"
			entry.InputModalities = []string{"text", "image"}
			entry.ContextWindow = 272000
			entry.MaxContextWindow = 872000
			entry.TruncationPolicy.Mode = "tokens"
			switch model.Id {
			case "gpt-5.2":
				entry.MaxContextWindow = 272000
				entry.DefaultReasoningSummary = "auto"
				entry.TruncationPolicy.Mode = "bytes"
			case "gpt-5.4":
				entry.MaxContextWindow = 1000000
			case "gpt-5.4-mini":
				entry.MaxContextWindow = 272000
				entry.DefaultVerbosity = "medium"
			case "gpt-5.5":
				entry.MaxContextWindow = 272000
			case "gpt-6.1-terra":
				entry.ContextWindow = 500000
			case "gpt-daybreak-red-latest":
				entry.ContextWindow = 372000
				entry.MaxContextWindow = 372000
				entry.DefaultVerbosity = "high"
			}
		}
		catalog = append(catalog, entry)
	}
	return catalog
}
