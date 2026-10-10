package relayconvert

import (
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupBuiltinResponseConverters(t *testing.T) {
	tests := []struct {
		lookupID       string
		id             string
		from           types.RelayFormat
		to             types.RelayFormat
		quality        ResponseConverterQuality
		stepConverters []string
	}{
		{lookupID: ResponseConverterOAIChatToOAIResponses, id: ConverterOpenAIChatToOpenAIResponses, from: types.RelayFormatOpenAI, to: types.RelayFormatOpenAIResponses, quality: ResponseConverterQualityGood},
		{lookupID: ResponseConverterOAIResponsesToOAIChat, id: ConverterOpenAIResponsesToOpenAIChat, from: types.RelayFormatOpenAIResponses, to: types.RelayFormatOpenAI, quality: ResponseConverterQualityGood},
		{lookupID: ResponseConverterOAIChatToClaudeMessages, id: ConverterOpenAIChatToClaudeMessages, from: types.RelayFormatOpenAI, to: types.RelayFormatClaude, quality: ResponseConverterQualityFair},
		{lookupID: ResponseConverterOAIChatToGeminiChat, id: ConverterOpenAIChatToGeminiContent, from: types.RelayFormatOpenAI, to: types.RelayFormatGemini, quality: ResponseConverterQualityFair},
		{lookupID: ResponseConverterClaudeMessagesToOAIChat, id: ConverterClaudeMessagesToOpenAIChat, from: types.RelayFormatClaude, to: types.RelayFormatOpenAI, quality: ResponseConverterQualityFair},
		{lookupID: ResponseConverterGeminiChatToOAIChat, id: ConverterGeminiContentToOpenAIChat, from: types.RelayFormatGemini, to: types.RelayFormatOpenAI, quality: ResponseConverterQualityFair},
		{
			lookupID: responseConverterClaudeToGemini,
			id:       requestConverterClaudeToGemini,
			from:     types.RelayFormatClaude,
			to:       types.RelayFormatGemini,
			quality:  ResponseConverterQualityDiscouraged,
			stepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
		},
		{
			lookupID: responseConverterClaudeToResponses,
			id:       requestConverterClaudeToResponses,
			from:     types.RelayFormatClaude,
			to:       types.RelayFormatOpenAIResponses,
			quality:  ResponseConverterQualityFair,
			stepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
		},
		{
			lookupID: responseConverterGeminiToClaude,
			id:       requestConverterGeminiToClaude,
			from:     types.RelayFormatGemini,
			to:       types.RelayFormatClaude,
			quality:  ResponseConverterQualityDiscouraged,
			stepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
		},
		{
			lookupID: responseConverterGeminiToResponses,
			id:       requestConverterGeminiToResponses,
			from:     types.RelayFormatGemini,
			to:       types.RelayFormatOpenAIResponses,
			quality:  ResponseConverterQualityFair,
			stepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
		},
		{
			lookupID: responseConverterResponsesToClaude,
			id:       requestConverterResponsesToClaude,
			from:     types.RelayFormatOpenAIResponses,
			to:       types.RelayFormatClaude,
			quality:  ResponseConverterQualityFair,
		},
		{
			lookupID: responseConverterResponsesToGemini,
			id:       ConverterOpenAIResponsesToGemini,
			from:     types.RelayFormatOpenAIResponses,
			to:       types.RelayFormatGemini,
			quality:  ResponseConverterQualityFair,
			stepConverters: []string{
				ConverterOpenAIResponsesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.lookupID, func(t *testing.T) {
			spec, ok := LookupResponseConverter(tt.lookupID)
			require.True(t, ok)
			assert.Equal(t, tt.id, spec.ID)
			assert.Equal(t, tt.from, spec.From)
			assert.Equal(t, tt.to, spec.To)
			assert.Equal(t, tt.quality, spec.Quality)
			assert.Equal(t, tt.stepConverters, spec.StepConverters)
			if len(tt.stepConverters) == 0 {
				assert.NotNil(t, spec.Convert)
			} else {
				assert.Nil(t, spec.Convert)
			}
		})
	}

	_, ok := LookupResponseConverter("missing")
	assert.False(t, ok)
}

func TestConvertResponseRejectsNilAndUnsupportedRoute(t *testing.T) {
	_, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, (*dto.OpenAITextResponse)(nil))
	require.Error(t, err)

	_, err = ConvertResponse(nil, nil, types.RelayFormatEmbedding, &dto.OpenAITextResponse{})
	require.Error(t, err)
}

func TestConvertResponseDirectConverters(t *testing.T) {
	chat := textRegistryChatResponse()
	info := &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}

	toResponses, err := ConvertResponse(nil, info, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIChatToOpenAIResponses, toResponses.Converter)
	assert.Equal(t, ResponseConverterQualityGood, toResponses.Quality)
	assert.Equal(t, types.RelayFormatOpenAI, toResponses.From)
	assert.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), toResponses.To)
	assert.Equal(t, []ResponseStep{{Converter: ConverterOpenAIChatToOpenAIResponses, From: types.RelayFormatOpenAI, To: types.RelayFormatOpenAIResponses}}, toResponses.Steps)
	require.IsType(t, &dto.OpenAIResponsesResponse{}, toResponses.Value)
	assert.Equal(t, 9, toResponses.Usage.TotalTokens)
	require.NotNil(t, toResponses.Usage.BillingUsage)
	require.NotNil(t, toResponses.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIChat, toResponses.Usage.BillingUsage.Source)
	assert.Equal(t, 4, toResponses.Usage.BillingUsage.OpenAIUsage.PromptTokens)

	responses := &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		CreatedAt: 123,
		Model:     "gpt-test",
		Status:    []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "hello"},
				},
			},
		},
		Usage: &dto.Usage{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
	}
	toChat, err := ConvertResponse(nil, info, types.RelayFormatOpenAI, responses)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIResponsesToOpenAIChat, toChat.Converter)
	assert.Equal(t, ResponseConverterQualityGood, toChat.Quality)
	require.IsType(t, &dto.OpenAITextResponse{}, toChat.Value)
	assert.Equal(t, 10, toChat.Usage.TotalTokens)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIResponses, toChat.Usage.BillingUsage.Source)
	assert.Equal(t, 4, toChat.Usage.BillingUsage.OpenAIUsage.InputTokens)

	toClaude, err := ConvertResponse(nil, info, types.RelayFormatClaude, chat)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIChatToClaudeMessages, toClaude.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toClaude.Quality)
	require.IsType(t, &dto.ClaudeResponse{}, toClaude.Value)
	assert.Equal(t, 9, toClaude.Usage.TotalTokens)
	require.NotNil(t, toClaude.Usage.BillingUsage)
	require.NotNil(t, toClaude.Usage.BillingUsage.OpenAIUsage)
	claudeValue := toClaude.Value.(*dto.ClaudeResponse)
	require.NotNil(t, claudeValue.Usage)
	require.NotNil(t, claudeValue.Usage.BillingUsage)
	require.NotNil(t, claudeValue.Usage.BillingUsage.OpenAIUsage)

	toGemini, err := ConvertResponse(nil, info, types.RelayFormatGemini, chat)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIChatToGeminiContent, toGemini.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toGemini.Quality)
	require.IsType(t, &dto.GeminiChatResponse{}, toGemini.Value)
	assert.Equal(t, 9, toGemini.Usage.TotalTokens)
	require.NotNil(t, toGemini.Usage.BillingUsage)
	require.NotNil(t, toGemini.Usage.BillingUsage.OpenAIUsage)
	geminiValue := toGemini.Value.(*dto.GeminiChatResponse)
	require.NotNil(t, geminiValue.UsageMetadata.BillingUsage)
	require.NotNil(t, geminiValue.UsageMetadata.BillingUsage.OpenAIUsage)
}

func TestConvertResponseDirectAndMultiHopConverters(t *testing.T) {
	responses := textRegistryResponsesResponse()

	toClaude, err := ConvertResponse(nil, &convmeta.Values{}, types.RelayFormatClaude, responses)
	require.NoError(t, err)
	assert.Equal(t, requestConverterResponsesToClaude, toClaude.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toClaude.Quality)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToClaudeMessages, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatClaude},
	}, toClaude.Steps)
	require.IsType(t, &dto.ClaudeResponse{}, toClaude.Value)
	claudeValue := toClaude.Value.(*dto.ClaudeResponse)
	require.Len(t, claudeValue.Content, 2)
	assert.Equal(t, "text", claudeValue.Content[0].Type)
	assert.Equal(t, "tool_use", claudeValue.Content[1].Type)
	assert.Equal(t, "lookup", claudeValue.Content[1].Name)
	assert.Equal(t, map[string]any{"q": "x"}, claudeValue.Content[1].Input)
	assert.Equal(t, 11, toClaude.Usage.TotalTokens)

	toGemini, err := ConvertResponse(nil, &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}, types.RelayFormatGemini, responses)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, toGemini.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toGemini.Quality)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToOpenAIChat, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatOpenAI},
		{Converter: ConverterOpenAIChatToGeminiContent, From: types.RelayFormatOpenAI, To: types.RelayFormatGemini},
	}, toGemini.Steps)
	require.IsType(t, &dto.GeminiChatResponse{}, toGemini.Value)
	geminiValue := toGemini.Value.(*dto.GeminiChatResponse)
	require.Len(t, geminiValue.Candidates, 1)
	require.Len(t, geminiValue.Candidates[0].Content.Parts, 2)
	assert.Equal(t, "hello", geminiValue.Candidates[0].Content.Parts[0].Text)
	require.NotNil(t, geminiValue.Candidates[0].Content.Parts[1].FunctionCall)
	assert.Equal(t, "lookup", geminiValue.Candidates[0].Content.Parts[1].FunctionCall.FunctionName)
	assert.Equal(t, map[string]any{"q": "x"}, geminiValue.Candidates[0].Content.Parts[1].FunctionCall.Arguments)
	assert.Equal(t, 11, toGemini.Usage.TotalTokens)
}

func TestConvertResponsePreservesInterleavedResponsesBlocksForClaude(t *testing.T) {
	responses := &dto.OpenAIResponsesResponse{
		ID:     "resp_1",
		Model:  "gpt-test",
		Status: []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{Type: "reasoning", Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "**Planning file inspection**"}}},
			{Type: "message", Role: "assistant", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "I’ll inspect the starter repository."}}},
			{Type: "reasoning", Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "**Clarifying environment task requirements**"}}},
			{Type: "message", Role: "assistant", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "What would you like me to build?"}}},
		},
	}

	result, err := ConvertResponse(nil, nil, types.RelayFormatClaude, responses)
	require.NoError(t, err)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToClaudeMessages, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatClaude},
	}, result.Steps)
	claudeResponse := result.Value.(*dto.ClaudeResponse)
	require.Len(t, claudeResponse.Content, 4)
	assert.Equal(t, []string{"thinking", "text", "thinking", "text"}, []string{
		claudeResponse.Content[0].Type,
		claudeResponse.Content[1].Type,
		claudeResponse.Content[2].Type,
		claudeResponse.Content[3].Type,
	})
	require.NotNil(t, claudeResponse.Content[0].Thinking)
	require.NotNil(t, claudeResponse.Content[2].Thinking)
	assert.Equal(t, "**Planning file inspection**", *claudeResponse.Content[0].Thinking)
	assert.Equal(t, "I’ll inspect the starter repository.", claudeResponse.Content[1].GetText())
	assert.Equal(t, "**Clarifying environment task requirements**", *claudeResponse.Content[2].Thinking)
	assert.Equal(t, "What would you like me to build?", claudeResponse.Content[3].GetText())
}

func TestConvertResponseByIDExecutesMultiHopAndChecksSource(t *testing.T) {
	responses := textRegistryResponsesResponse()

	result, err := ConvertResponseByID(nil, nil, responseConverterResponsesToGemini, responses)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, result.Converter)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToOpenAIChat, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatOpenAI},
		{Converter: ConverterOpenAIChatToGeminiContent, From: types.RelayFormatOpenAI, To: types.RelayFormatGemini},
	}, result.Steps)

	_, err = ConvertResponseByID(nil, nil, responseConverterResponsesToGemini, textRegistryChatResponse())
	require.Error(t, err)
}

func TestConvertResponseProviderToOAIChatUsage(t *testing.T) {
	claude := &dto.ClaudeResponse{
		Id:         "msg_1",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-test",
		StopReason: "end_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "tool_use", Id: "toolu_1", Name: "lookup", Input: map[string]any{"q": "x"}},
		},
		Usage: &dto.ClaudeUsage{
			InputTokens:              10,
			CacheReadInputTokens:     3,
			CacheCreationInputTokens: 4,
			OutputTokens:             5,
			CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 1,
				Ephemeral1hInputTokens: 3,
			},
		},
	}
	toChat, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, claude)
	require.NoError(t, err)
	assert.Equal(t, ConverterClaudeMessagesToOpenAIChat, toChat.Converter)
	require.IsType(t, &dto.OpenAITextResponse{}, toChat.Value)
	assert.Equal(t, 17, toChat.Usage.PromptTokens)
	assert.Equal(t, 5, toChat.Usage.CompletionTokens)
	assert.Equal(t, 22, toChat.Usage.TotalTokens)
	assert.Equal(t, 3, toChat.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 4, toChat.Usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 4, toChat.Usage.PromptTokensDetails.CacheWriteTokens)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, dto.BillingUsageSourceClaudeMessages, toChat.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticAnthropic, toChat.Usage.BillingUsage.Semantic)
	assert.Equal(t, 10, toChat.Usage.BillingUsage.ClaudeUsage.InputTokens)
	assert.Equal(t, 3, toChat.Usage.BillingUsage.ClaudeUsage.CacheReadInputTokens)
	assert.Equal(t, 4, toChat.Usage.BillingUsage.ClaudeUsage.CacheCreationInputTokens)
	assert.Equal(t, 5, toChat.Usage.BillingUsage.ClaudeUsage.OutputTokens)
	chatValue := toChat.Value.(*dto.OpenAITextResponse)
	require.Len(t, chatValue.Choices, 1)
	require.Len(t, chatValue.Choices[0].Message.ParseToolCalls(), 1)
	assert.JSONEq(t, `{"q":"x"}`, chatValue.Choices[0].Message.ParseToolCalls()[0].Function.Arguments)

	gemini := &dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Parts: []dto.GeminiPart{
						{Text: "hello"},
						{FunctionCall: &dto.FunctionCall{FunctionName: "lookup", Arguments: map[string]any{"q": "x"}}},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        7,
			ToolUsePromptTokenCount: 2,
			CandidatesTokenCount:    5,
			ThoughtsTokenCount:      3,
			TotalTokenCount:         17,
			CachedContentTokenCount: 4,
			PromptTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 5},
				{Modality: "IMAGE", TokenCount: 1},
			},
			ToolUsePromptTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "AUDIO", TokenCount: 3},
			},
			CandidatesTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 4},
				{Modality: "IMAGE", TokenCount: 1},
			},
		},
	}
	toChat, err = ConvertResponse(nil, &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}, types.RelayFormatOpenAI, gemini)
	require.NoError(t, err)
	assert.Equal(t, ConverterGeminiContentToOpenAIChat, toChat.Converter)
	require.IsType(t, &dto.OpenAITextResponse{}, toChat.Value)
	assert.Equal(t, 9, toChat.Usage.PromptTokens)
	assert.Equal(t, 8, toChat.Usage.CompletionTokens)
	assert.Equal(t, 17, toChat.Usage.TotalTokens)
	assert.Equal(t, 3, toChat.Usage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 4, toChat.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 5, toChat.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 3, toChat.Usage.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 1, toChat.Usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 4, toChat.Usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 1, toChat.Usage.CompletionTokenDetails.ImageTokens)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, dto.BillingUsageSourceGeminiChat, toChat.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticGemini, toChat.Usage.BillingUsage.Semantic)
	assert.Equal(t, 7, toChat.Usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	assert.Equal(t, 2, toChat.Usage.BillingUsage.GeminiUsageMetadata.ToolUsePromptTokenCount)
	assert.Equal(t, 17, toChat.Usage.BillingUsage.GeminiUsageMetadata.TotalTokenCount)
}

func TestConvertResponsePreservesBillingUsageAcrossChatResponsesBridge(t *testing.T) {
	chat := textRegistryChatResponse()
	chat.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
		InputTokens:              10,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 4,
		OutputTokens:             5,
	})

	toResponses, err := ConvertResponse(nil, nil, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	require.NotNil(t, toResponses.Usage.BillingUsage)
	require.NotNil(t, toResponses.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, 10, toResponses.Usage.BillingUsage.ClaudeUsage.InputTokens)

	responsesValue := toResponses.Value.(*dto.OpenAIResponsesResponse)
	toChat, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, responsesValue)
	require.NoError(t, err)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, 4, toChat.Usage.BillingUsage.ClaudeUsage.CacheCreationInputTokens)
}

func TestConvertResponseUsesBillingUsageWhenRestoringNativeTargets(t *testing.T) {
	chat := textRegistryChatResponse()
	chat.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
		InputTokens:              10,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 4,
		OutputTokens:             5,
	})

	toClaude, err := ConvertResponse(nil, nil, types.RelayFormatClaude, chat)
	require.NoError(t, err)
	claudeValue := toClaude.Value.(*dto.ClaudeResponse)
	require.NotNil(t, claudeValue.Usage)
	assert.Equal(t, 10, claudeValue.Usage.InputTokens)
	assert.Equal(t, 3, claudeValue.Usage.CacheReadInputTokens)
	assert.Equal(t, 4, claudeValue.Usage.CacheCreationInputTokens)
	assert.Equal(t, 5, claudeValue.Usage.OutputTokens)

	chat.Usage.BillingUsage = dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
		PromptTokenCount:        7,
		ToolUsePromptTokenCount: 2,
		CandidatesTokenCount:    5,
		ThoughtsTokenCount:      3,
		TotalTokenCount:         17,
	})

	toGemini, err := ConvertResponse(nil, nil, types.RelayFormatGemini, chat)
	require.NoError(t, err)
	geminiValue := toGemini.Value.(*dto.GeminiChatResponse)
	assert.Equal(t, 7, geminiValue.UsageMetadata.PromptTokenCount)
	assert.Equal(t, 2, geminiValue.UsageMetadata.ToolUsePromptTokenCount)
	assert.Equal(t, 5, geminiValue.UsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 3, geminiValue.UsageMetadata.ThoughtsTokenCount)
	assert.Equal(t, 17, geminiValue.UsageMetadata.TotalTokenCount)
}

func TestGeminiResponsePreservesTextPartBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		parts      []dto.GeminiPart
		want       string
		wantStream string
	}{
		{
			name: "JSON split inside keys values and escapes",
			parts: []dto.GeminiPart{
				{Text: `[{"used_`}, {Text: `tag`}, {Text: `":"cli`},
				{Text: `ent","text":"line`}, {Text: `\`}, {Text: `nnext`},
				{Text: `","ok":`}, {Text: `true}`}, {Text: `]`},
			},
			want: `[{"used_tag":"client","text":"line\nnext","ok":true}]`,
		},
		{
			name: "source whitespace and empty parts",
			parts: []dto.GeminiPart{
				{Text: ""}, {Text: " first "}, {Text: ""}, {Text: "line"},
				{Text: "\n"}, {Text: "\n"}, {Text: "second\n"}, {Text: ""},
			},
			want: " first line\n\nsecond\n",
		},
		{
			name: "rendered image and code keep their block separators",
			parts: []dto.GeminiPart{
				{Text: "before"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "aW1hZ2U="}},
				{Text: "after"},
				{ExecutableCode: &dto.GeminiPartExecutableCode{Language: "python", Code: "print(1)"}},
				{CodeExecutionResult: &dto.GeminiPartCodeExecutionResult{Output: "1"}},
			},
			want:       "before\n![image](data:image/png;base64,aW1hZ2U=)\nafter\n```python\nprint(1)\n```\n```output\n1\n```",
			wantStream: "before\n![image](data:image/png;base64,aW1hZ2U=)\nafter\n```python\nprint(1)\n```\n\n```output\n1\n```\n",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := &dto.GeminiChatResponse{
				Candidates: []dto.GeminiChatCandidate{{
					Content: dto.GeminiChatContent{Parts: testCase.parts},
				}},
			}
			nonStream := ResponseGeminiChat2OpenAI("chatcmpl-parts", 1, response)
			require.Len(t, nonStream.Choices, 1)
			assert.Equal(t, testCase.want, nonStream.Choices[0].Message.StringContent())

			stream, _ := StreamResponseGeminiChat2OpenAI(response)
			require.Len(t, stream.Choices, 1)
			wantStream := testCase.wantStream
			if wantStream == "" {
				wantStream = testCase.want
			}
			assert.Equal(t, wantStream, stream.Choices[0].Delta.GetContentString())
		})
	}
}

func TestGeminiStreamGroundingPreservesStandaloneNewline(t *testing.T) {
	state, err := NewResponseStreamState(types.RelayFormatGemini, types.RelayFormatOpenAI, ResponseStreamOptions{
		ID: "chatcmpl-grounding", Model: "gemini-test",
	})
	require.NoError(t, err)
	var content strings.Builder
	var annotations []byte
	for _, text := range []string{"hello", "\n", "world"} {
		candidate := dto.GeminiChatCandidate{
			Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{Text: text}}},
		}
		if text == "world" {
			candidate.GroundingMetadata = &dto.GeminiGroundingMetadata{
				GroundingChunks:   []byte(`[{"web":{"uri":"https://example.com/source","title":"Source"}}]`),
				GroundingSupports: []byte(`[{"segment":{"partIndex":0,"startIndex":5,"endIndex":11,"text":"\nworld"},"groundingChunkIndices":[0]}]`),
			}
		}
		results, err := ConvertStreamResponseChunk(nil, nil, state, &dto.GeminiChatResponse{
			Candidates: []dto.GeminiChatCandidate{candidate},
		})
		require.NoError(t, err)
		require.Len(t, results, 1)
		chunk, ok := results[0].Value.(*dto.ChatCompletionsStreamResponse)
		require.True(t, ok)
		require.Len(t, chunk.Choices, 1)
		content.WriteString(chunk.Choices[0].Delta.GetContentString())
		if len(chunk.Choices[0].Delta.Annotations) > 0 {
			annotations = chunk.Choices[0].Delta.Annotations
		}
	}
	assert.Equal(t, "hello\nworld", content.String())
	assert.JSONEq(t, `[{"type":"url_citation","url_citation":{"start_index":5,"end_index":11,"url":"https://example.com/source","title":"Source"}}]`, string(annotations))
}

func TestConvertStreamResponseDirectConverters(t *testing.T) {
	info := &convmeta.Values{
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeNone,
		},
	}
	info.SendResponseCount = 1
	finishReason := "stop"
	result, err := ConvertStreamResponse(nil, info, types.RelayFormatClaude, &dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				FinishReason: &finishReason,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: respPtr("hello"),
				},
			},
		},
		Usage: &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	})
	require.NoError(t, err)
	assert.True(t, result.Stream)
	assert.Equal(t, ConverterOpenAIChatToClaudeMessages, result.Converter)
	require.IsType(t, []*dto.ClaudeResponse{}, result.Value)
	assert.Equal(t, 5, result.Usage.TotalTokens)

	result, err = ConvertStreamResponse(nil, &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}, types.RelayFormatOpenAI, &dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{Text: "hello"}}}}},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     1,
			CandidatesTokenCount: 2,
			TotalTokenCount:      3,
		},
	})
	require.NoError(t, err)
	assert.True(t, result.Stream)
	assert.Equal(t, ConverterGeminiContentToOpenAIChat, result.Converter)
	require.IsType(t, &dto.ChatCompletionsStreamResponse{}, result.Value)
	assert.Equal(t, 3, result.Usage.TotalTokens)
}

func TestConvertStreamResponseStatefulDirectConverters(t *testing.T) {
	chatState, err := NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, ResponseStreamOptions{
		ID:    "resp_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)
	chatResults, err := ConvertStreamResponseChunk(nil, nil, chatState, &dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: respPtr("hello")}},
		},
		Usage: &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	})
	require.NoError(t, err)
	require.NotEmpty(t, chatResults)
	assert.Equal(t, ConverterOpenAIChatToOpenAIResponses, chatResults[0].Converter)
	assert.Equal(t, []ResponseStep{{Converter: ConverterOpenAIChatToOpenAIResponses, From: types.RelayFormatOpenAI, To: types.RelayFormatOpenAIResponses}}, chatResults[0].Steps)
	assert.Equal(t, 5, chatState.Usage().TotalTokens)

	finalResults, err := FinalizeStreamResponse(nil, nil, chatState)
	require.NoError(t, err)
	require.NotEmpty(t, finalResults)
	lastEvent, ok := finalResults[len(finalResults)-1].Value.(ChatToResponsesStreamEvent)
	require.True(t, ok)
	assert.Equal(t, "response.completed", lastEvent.Type)

	responsesState, err := NewResponseStreamState(types.RelayFormatOpenAIResponses, types.RelayFormatOpenAI, ResponseStreamOptions{
		ID:    "chatcmpl_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)
	responsesResults, err := ConvertStreamResponseChunk(nil, nil, responsesState, &dto.ResponsesStreamResponse{
		Type:  "response.output_text.delta",
		Delta: "hello",
	})
	require.NoError(t, err)
	require.NotEmpty(t, responsesResults)
	assert.Equal(t, ConverterOpenAIResponsesToOpenAIChat, responsesResults[0].Converter)
	assert.Equal(t, []ResponseStep{{Converter: ConverterOpenAIResponsesToOpenAIChat, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatOpenAI}}, responsesResults[0].Steps)
	require.IsType(t, dto.ChatCompletionsStreamResponse{}, responsesResults[len(responsesResults)-1].Value)
}

func TestConvertStreamResponseStatefulDirectResponsesToClaude(t *testing.T) {
	info := &convmeta.Values{
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeNone,
		},
	}
	state, err := NewResponseStreamState(types.RelayFormatOpenAIResponses, types.RelayFormatClaude, ResponseStreamOptions{
		ID:    "chatcmpl_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)

	results, err := ConvertStreamResponseChunk(nil, info, state, &dto.ResponsesStreamResponse{
		Type:  "response.output_text.delta",
		Delta: "hello",
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, requestConverterResponsesToClaude, results[0].Converter)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToClaudeMessages, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatClaude},
	}, results[0].Steps)

	var sawTextDelta bool
	for _, result := range results {
		claudeResponse, ok := result.Value.(*dto.ClaudeResponse)
		if !ok || claudeResponse == nil {
			continue
		}
		if claudeResponse.Type == "content_block_delta" && claudeResponse.Delta != nil && claudeResponse.Delta.Text != nil && *claudeResponse.Delta.Text == "hello" {
			sawTextDelta = true
		}
	}
	assert.True(t, sawTextDelta)

	state.SetUsage(&dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	_, err = FinalizeStreamResponse(nil, info, state)
	require.NoError(t, err)
	assert.Equal(t, 5, state.Usage().TotalTokens)
}

func TestResponseUsageMatrixChatAndResponsesDetails(t *testing.T) {
	chat := textRegistryChatResponse()
	chat.Usage = dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         3,
			CachedTokensDetails:  &dto.CachedTokenDetails{TextTokens: lo.ToPtr(1), AudioTokens: lo.ToPtr(1), ImageTokens: lo.ToPtr(1)},
			CachedCreationTokens: 2,
			CacheWriteTokens:     6,
			TextTokens:           4,
			AudioTokens:          1,
			ImageTokens:          5,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: 2,
			TextTokens:      2,
			AudioTokens:     1,
			ImageTokens:     2,
		},
	}
	result, err := ConvertResponse(nil, nil, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	assert.Equal(t, 10, result.Usage.InputTokens)
	assert.Equal(t, 5, result.Usage.OutputTokens)
	assert.Equal(t, 20, result.Usage.TotalTokens)
	require.NotNil(t, result.Usage.InputTokensDetails)
	assert.Equal(t, 3, result.Usage.InputTokensDetails.CachedTokens)
	assert.Equal(t, chat.Usage.PromptTokensDetails.CachedTokensDetails, result.Usage.InputTokensDetails.CachedTokensDetails)
	assert.NotSame(t, chat.Usage.PromptTokensDetails.CachedTokensDetails, result.Usage.InputTokensDetails.CachedTokensDetails)
	assert.Equal(t, 2, result.Usage.InputTokensDetails.CachedCreationTokens)
	assert.Equal(t, 6, result.Usage.InputTokensDetails.CacheWriteTokens)
	assert.Equal(t, 4, result.Usage.InputTokensDetails.TextTokens)
	assert.Equal(t, 1, result.Usage.InputTokensDetails.AudioTokens)
	assert.Equal(t, 5, result.Usage.InputTokensDetails.ImageTokens)
	assert.Equal(t, 2, result.Usage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 2, result.Usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 1, result.Usage.CompletionTokenDetails.AudioTokens)
	assert.Equal(t, 2, result.Usage.CompletionTokenDetails.ImageTokens)

	responses := &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		Status:    []byte(`"completed"`),
		Model:     "gpt-test",
		Output:    []dto.ResponsesOutput{},
		CreatedAt: 123,
		Usage: &dto.Usage{
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  21,
			InputTokensDetails: &dto.InputTokenDetails{
				CachedTokens:         4,
				CachedTokensDetails:  &dto.CachedTokenDetails{TextTokens: lo.ToPtr(1), AudioTokens: lo.ToPtr(2), ImageTokens: lo.ToPtr(1)},
				CachedCreationTokens: 1,
				CacheWriteTokens:     7,
				TextTokens:           5,
				AudioTokens:          2,
				ImageTokens:          1,
			},
			CompletionTokenDetails: dto.OutputTokenDetails{
				ReasoningTokens: 3,
				TextTokens:      4,
				AudioTokens:     1,
				ImageTokens:     3,
			},
		},
	}
	result, err = ConvertResponse(nil, nil, types.RelayFormatOpenAI, responses)
	require.NoError(t, err)
	assert.Equal(t, 12, result.Usage.PromptTokens)
	assert.Equal(t, 8, result.Usage.CompletionTokens)
	assert.Equal(t, 21, result.Usage.TotalTokens)
	assert.Equal(t, 4, result.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, responses.Usage.InputTokensDetails.CachedTokensDetails, result.Usage.PromptTokensDetails.CachedTokensDetails)
	assert.NotSame(t, responses.Usage.InputTokensDetails.CachedTokensDetails, result.Usage.PromptTokensDetails.CachedTokensDetails)
	assert.Equal(t, 1, result.Usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 7, result.Usage.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, 5, result.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 2, result.Usage.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 1, result.Usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 3, result.Usage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 4, result.Usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 1, result.Usage.CompletionTokenDetails.AudioTokens)
	assert.Equal(t, 3, result.Usage.CompletionTokenDetails.ImageTokens)
}

func TestCachedTokenDetailsSurviveUsageSnapshotsAndUnknownEvents(t *testing.T) {
	var event dto.RealtimeUsage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"input_tokens":10,"input_token_details":{"cached_tokens":6,"cached_tokens_details":{"text_tokens":1,"audio_tokens":3,"image_tokens":2}}}`), &event))
	want := &dto.CachedTokenDetails{TextTokens: lo.ToPtr(1), AudioTokens: lo.ToPtr(3), ImageTokens: lo.ToPtr(2)}
	assert.Equal(t, want, event.InputTokenDetails.CachedTokensDetails)
	encoded, err := kitutil.Marshal(event)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"cached_tokens_details"`)
	legacy, err := kitutil.Marshal(dto.InputTokenDetails{CachedTokens: 6})
	require.NoError(t, err)
	assert.NotContains(t, string(legacy), "cached_tokens_details", "missing breakdown stays unknown")

	usage := &dto.Usage{InputTokens: 10, InputTokensDetails: &event.InputTokenDetails}
	snapshot := dto.NewOpenAIResponsesBillingUsage(usage)
	canonical, ok := snapshot.CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, want, canonical.PromptTokensDetails.CachedTokensDetails)
	*event.InputTokenDetails.CachedTokensDetails.AudioTokens = 99
	assert.Equal(t, 3, *snapshot.OpenAIUsage.InputTokensDetails.CachedTokensDetails.AudioTokens)
	*canonical.PromptTokensDetails.CachedTokensDetails.AudioTokens = 88
	assert.Equal(t, 3, *snapshot.OpenAIUsage.InputTokensDetails.CachedTokensDetails.AudioTokens, "canonical reads must not change the billing snapshot")

	current := &dto.Usage{PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 6, CachedTokensDetails: want}}
	dto.MergeUsageNonZero(current, &dto.Usage{PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 6}})
	assert.Equal(t, want, current.PromptTokensDetails.CachedTokensDetails, "a partial chunk may omit unchanged breakdowns")
	dto.MergeUsageNonZero(current, &dto.Usage{PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 7}})
	assert.Nil(t, current.PromptTokensDetails.CachedTokensDetails, "changed totals invalidate an older breakdown")
	dto.MergeUsageNonZero(current, &dto.Usage{PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 7, CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: lo.ToPtr(7), AudioTokens: lo.ToPtr(0)}}})
	assert.Zero(t, *current.PromptTokensDetails.CachedTokensDetails.AudioTokens, "whole-object replacement retains explicit zero")
	dto.MergeUsageNonZero(current, &dto.Usage{PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 8, CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: lo.ToPtr(0)}}})
	assert.Nil(t, current.PromptTokensDetails.CachedTokensDetails.TextTokens, "changed totals cannot inherit an omitted cached modality")
	assert.Zero(t, *current.PromptTokensDetails.CachedTokensDetails.ImageTokens, "the new snapshot retains explicit zero")

	for _, unknownFirst := range []bool{false, true} {
		known := dto.InputTokenDetails{CachedTokens: 6, CachedTokensDetails: want}
		var total dto.InputTokenDetails
		if unknownFirst {
			total.Add(dto.InputTokenDetails{CachedTokens: 2})
		}
		total.Add(known)
		total.Add(known)
		if !unknownFirst {
			assert.Equal(t, &dto.CachedTokenDetails{TextTokens: lo.ToPtr(2), AudioTokens: lo.ToPtr(6), ImageTokens: lo.ToPtr(4)}, total.CachedTokensDetails)
			total.Add(dto.InputTokenDetails{CachedTokens: 2})
		}
		total.Add(known)
		assert.Equal(t, 20, total.CachedTokens)
		assert.Nil(t, total.CachedTokensDetails, "one unknown cached event keeps the aggregate unknown")
		assert.Equal(t, want, known.CachedTokensDetails, "aggregation must not mutate the source event")
	}
}

func TestCachedTokenDetailsAccumulationDoesNotWrapOrSubtract(t *testing.T) {
	maximum := dto.InputTokenDetails{
		CachedTokens: math.MaxInt, CachedCreationTokens: math.MaxInt, CacheWriteTokens: math.MaxInt,
		CacheCreationInputTokens: math.MaxInt,
		TextTokens:               math.MaxInt, AudioTokens: math.MaxInt, ImageTokens: math.MaxInt,
		CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: lo.ToPtr(math.MaxInt), AudioTokens: lo.ToPtr(math.MaxInt), ImageTokens: lo.ToPtr(math.MaxInt)},
	}
	total := maximum.Clone()
	for _, count := range []int{1, -1} {
		total.Add(dto.InputTokenDetails{
			CachedTokens: count, CachedCreationTokens: count, CacheWriteTokens: count,
			CacheCreationInputTokens: count,
			TextTokens:               count, AudioTokens: count, ImageTokens: count,
			CachedTokensDetails: &dto.CachedTokenDetails{TextTokens: lo.ToPtr(count), AudioTokens: lo.ToPtr(count), ImageTokens: lo.ToPtr(count)},
		})
		assert.Equal(t, maximum, total, "large accumulated usage stays positive and negative input cannot reduce billing counters")
	}
}

func TestDashScopeCacheCreationSurvivesUsageAndProtocolConversion(t *testing.T) {
	for _, detailsKey := range []string{"prompt_tokens_details", "input_tokens_details"} {
		t.Run(detailsKey, func(t *testing.T) {
			// DashScope sends this field on both compatible Chat and Responses
			// usage. Later streaming frames may omit unchanged usage details.
			var usage dto.Usage
			require.NoError(t, kitutil.Unmarshal([]byte(`{"`+detailsKey+`":{"cache_creation_input_tokens":40}}`), &usage))
			assert.True(t, dto.HasOpenAIUsageTokens(&usage))
			merged := dto.MergeUsageNonZero(nil, &usage)
			dto.MergeUsageNonZero(merged, &dto.Usage{PromptTokens: 100, CompletionTokens: 10})
			dto.MergeUsageNonZero(merged, &dto.Usage{})
			canonical, ok := dto.NewOpenAIChatBillingUsage(merged).CanonicalUsage()
			require.True(t, ok)
			assert.Equal(t, 40, canonical.PromptTokensDetails.CacheCreationTokensTotal())
			encoded, err := kitutil.Marshal(canonical)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"cache_creation_input_tokens":40`)

			chat := textRegistryChatResponse()
			chat.Usage = *canonical
			responses, err := ConvertResponse(nil, nil, types.RelayFormatOpenAIResponses, chat)
			require.NoError(t, err)
			require.NotNil(t, responses.Usage.InputTokensDetails)
			assert.Equal(t, 40, responses.Usage.InputTokensDetails.CacheCreationInputTokens)
			roundTrip, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, responses.Value)
			require.NoError(t, err)
			assert.Equal(t, 40, roundTrip.Usage.PromptTokensDetails.CacheCreationTokensTotal())

			claude, err := ConvertResponse(nil, nil, types.RelayFormatClaude, chat)
			require.NoError(t, err)
			require.IsType(t, &dto.ClaudeResponse{}, claude.Value)
			claudeUsage := claude.Value.(*dto.ClaudeResponse).Usage
			require.NotNil(t, claudeUsage)
			assert.Equal(t, 40, claudeUsage.CacheCreationInputTokens)
			assert.Equal(t, 60, claudeUsage.InputTokens, "cache creation is split from the OpenAI input total exactly once")
		})
	}

	for _, test := range []struct {
		name    string
		details dto.InputTokenDetails
		want    int
	}{
		{"aliases are not summed", dto.InputTokenDetails{CachedCreationTokens: 30, CacheWriteTokens: 40, CacheCreationInputTokens: 40}, 40},
		{"negative counts do not reduce charges", dto.InputTokenDetails{CachedCreationTokens: -3, CacheWriteTokens: -2, CacheCreationInputTokens: -1}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, test.details.CacheCreationTokensTotal())
		})
	}

	var total dto.InputTokenDetails
	total.Add(dto.InputTokenDetails{CacheCreationInputTokens: 40})
	total.Add(dto.InputTokenDetails{CacheCreationInputTokens: 20})
	assert.Equal(t, 60, total.CacheCreationTokensTotal(), "independent usage events add their cache writes")
}

func textRegistryChatResponse() *dto.OpenAITextResponse {
	msg := dto.Message{
		Role:    "assistant",
		Content: "hello",
	}
	msg.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call_1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "lookup",
				Arguments: `{"q":"x"}`,
			},
		},
	})
	return &dto.OpenAITextResponse{
		Id:      "chatcmpl_1",
		Model:   "gpt-test",
		Created: 123,
		Choices: []dto.OpenAITextResponseChoice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: "tool_calls",
			},
		},
		Usage: dto.Usage{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9},
	}
}

func textRegistryResponsesResponse() *dto.OpenAIResponsesResponse {
	return &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		CreatedAt: 123,
		Model:     "gpt-test",
		Status:    []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "hello"},
				},
			},
			{
				Type:      "function_call",
				ID:        "call_1",
				CallId:    "call_1",
				Name:      "lookup",
				Arguments: []byte(`{"q":"x"}`),
			},
		},
		Usage: &dto.Usage{InputTokens: 4, OutputTokens: 7, TotalTokens: 11},
	}
}

func respPtr[T any](value T) *T {
	return &value
}

func TestConvertResponseToResponsesRestoresRecordedCustomTools(t *testing.T) {
	info := &convmeta.Values{ResponsesTools: &convmeta.ResponsesToolState{CustomToolNames: map[string]struct{}{"exec": {}}}}
	chatMessage := dto.Message{Role: "assistant"}
	chatMessage.SetToolCalls([]dto.ToolCallRequest{{ID: "call_exec", Type: "function", Function: dto.FunctionRequest{Name: "exec", Arguments: `{"input":"ls"}`}}})
	geminiExecCall := `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"exec","args":{"input":"ls"}}}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`
	tests := []struct {
		name     string
		from     types.RelayFormat
		response any
		stream   []any
	}{
		{
			name: "chat",
			from: types.RelayFormatOpenAI,
			response: &dto.OpenAITextResponse{
				Id:      "chatcmpl_1",
				Model:   "gpt-test",
				Choices: []dto.OpenAITextResponseChoice{{Message: chatMessage, FinishReason: "tool_calls"}},
			},
			stream: []any{
				chatStreamChunk(`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"input\":"}}]}}]}`),
				chatStreamChunk(`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`),
			},
		},
		{
			name: "claude",
			from: types.RelayFormatClaude,
			response: &dto.ClaudeResponse{
				Id:         "msg_1",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-test",
				StopReason: "tool_use",
				Content:    []dto.ClaudeMediaMessage{{Type: "tool_use", Id: "toolu_exec", Name: "exec", Input: map[string]any{"input": "ls"}}},
			},
			stream: []any{
				claudeStreamChunk(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":4,"output_tokens":0}}}`),
				claudeStreamChunk(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_exec","name":"exec","input":{}}}`),
				claudeStreamChunk(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":"}}`),
				claudeStreamChunk(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`),
				claudeStreamChunk(`{"type":"content_block_stop","index":0}`),
				claudeStreamChunk(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`),
				claudeStreamChunk(`{"type":"message_stop"}`),
			},
		},
		{
			name:     "gemini",
			from:     types.RelayFormatGemini,
			response: geminiStreamChunk(geminiExecCall),
			stream:   []any{geminiStreamChunk(geminiExecCall)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ConvertResponse(nil, info, types.RelayFormatOpenAIResponses, tt.response)
			require.NoError(t, err)
			responses, ok := result.Value.(*dto.OpenAIResponsesResponse)
			require.True(t, ok)
			require.Len(t, responses.Output, 1)
			assert.Equal(t, "custom_tool_call", responses.Output[0].Type)
			assert.Equal(t, "exec", responses.Output[0].Name)
			assert.Equal(t, `"ls"`, string(responses.Output[0].Input))

			state, err := NewResponseStreamState(tt.from, types.RelayFormatOpenAIResponses, ResponseStreamOptions{ID: "resp_1", Model: "model-test"})
			require.NoError(t, err)
			var results []ResponseResult
			for _, chunk := range tt.stream {
				chunkResults, err := ConvertStreamResponseChunk(nil, info, state, chunk)
				require.NoError(t, err)
				results = append(results, chunkResults...)
			}
			finals, err := FinalizeStreamResponse(nil, info, state)
			require.NoError(t, err)
			results = append(results, finals...)

			var toolEvents []string
			for _, result := range results {
				event, ok := result.Value.(ChatToResponsesStreamEvent)
				require.True(t, ok)
				switch {
				case event.Payload.Item != nil && event.Payload.Item.Type != "message":
					toolEvents = append(toolEvents, event.Type+" "+event.Payload.Item.Type+" "+string(event.Payload.Item.Input))
				case event.Type == "response.custom_tool_call_input.delta":
					toolEvents = append(toolEvents, event.Type+" "+event.Payload.Delta)
				case event.Type == "response.custom_tool_call_input.done":
					require.NotNil(t, event.Payload.Input)
					toolEvents = append(toolEvents, event.Type+" "+*event.Payload.Input)
				case strings.Contains(event.Type, "function_call_arguments"):
					toolEvents = append(toolEvents, event.Type)
				}
			}
			assert.Equal(t, []string{
				`response.output_item.added custom_tool_call ""`,
				"response.custom_tool_call_input.delta ls",
				"response.custom_tool_call_input.done ls",
				`response.output_item.done custom_tool_call "ls"`,
			}, toolEvents)
		})
	}
}
