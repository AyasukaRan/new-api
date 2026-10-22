package deepseek_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/deepseek"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestCompletesDeepSeekThinkingToolHistory(t *testing.T) {
	for _, test := range []struct {
		name         string
		model        string
		upstream     string
		thinking     string
		effort       string
		historyCalls string
		tools        bool
		wantFilled   bool
	}{
		{name: "reported explicit thinking with prior calls", model: "deepseek-v4-pro", thinking: `{"type":"enabled"}`, historyCalls: `[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]`, wantFilled: true},
		{name: "tool definitions also require non-tool assistant history", model: "deepseek-v4-pro", thinking: `{"type":"enabled"}`, tools: true, wantFilled: true},
		{name: "v4 defaults to thinking", model: "deepseek-v4-flash", tools: true, wantFilled: true},
		{name: "current flash defaults to thinking", model: "deepseek-flash", tools: true, wantFilled: true},
		{name: "reasoner defaults to thinking", model: "deepseek-reasoner", tools: true, wantFilled: true},
		{name: "null thinking retains provider default", model: "deepseek-v4-pro", thinking: `null`, tools: true, wantFilled: true},
		{name: "empty thinking retains provider default", model: "deepseek-v4-pro", thinking: `{}`, tools: true, wantFilled: true},
		{name: "none effort does not repair history", model: "deepseek-v4-pro", effort: "none", tools: true},
		{name: "mapped upstream selects default mode", model: "application-model", upstream: "deepseek-v4-pro", tools: true, wantFilled: true},
		{name: "explicit thinking supports provider alias", model: "provider-alias", thinking: `{"type":"enabled"}`, tools: true, wantFilled: true},
		{name: "disabled overrides v4 default", model: "deepseek-v4-pro", thinking: `{"type":"disabled"}`, tools: true},
		{name: "none model suffix disables thinking", model: "deepseek-v4-pro-none", tools: true},
		{name: "max model suffix enables thinking", model: "deepseek-v4-pro-max", tools: true, wantFilled: true},
		{name: "legacy nonthinking model", model: "deepseek-chat", tools: true},
		{name: "unknown alias does not prove thinking", model: "provider-alias", tools: true},
		{name: "plain thinking chat stays untouched", model: "deepseek-v4-pro"},
		{name: "empty history calls are not tools", model: "deepseek-v4-pro", historyCalls: `[]`},
		{name: "null history calls are not tools", model: "deepseek-v4-pro", historyCalls: `null`},
		{name: "invalid history call object is not repaired", model: "deepseek-v4-pro", historyCalls: `{}`},
		{name: "unknown thinking mode is not guessed", model: "deepseek-v4-pro", thinking: `{"type":"custom"}`, tools: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{
				Model:           test.model,
				THINKING:        json.RawMessage(test.thinking),
				ReasoningEffort: test.effort,
				Messages: []dto.Message{
					{Role: "system", Content: "System instructions"},
					{Role: "user", Content: "Look this up"},
					{Role: "assistant", Content: "Previous answer without tools"},
					{Role: "assistant", Content: nil, ToolCalls: json.RawMessage(test.historyCalls)},
					{Role: "assistant", Content: "Real reasoning", ReasoningContent: common.GetPointer("keep exact reasoning\n思考")},
					{Role: "assistant", Content: "Explicit empty", ReasoningContent: common.GetPointer(""), Reasoning: common.GetPointer("canonical empty wins")},
					{Role: "assistant", Content: "Reasoning alias", Reasoning: common.GetPointer("keep original alias")},
					{Role: "tool", Content: "Tool result", ToolCallId: "call_1"},
				},
			}
			if test.tools {
				request.Tools = []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}}
			}
			upstream := test.upstream
			if upstream == "" {
				upstream = test.model
			}
			info := &relaycommon.RelayInfo{OriginModelName: test.model, ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeDeepSeek, UpstreamModelName: upstream}}
			before, err := common.DeepCopy(request)
			require.NoError(t, err)
			converted, err := (&deepseek.Adaptor{}).ConvertOpenAIRequest(nil, info, request)
			require.NoError(t, err)
			actual, ok := converted.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			want := before.Messages
			if test.wantFilled {
				want[2].ReasoningContent = common.GetPointer("")
				want[3].ReasoningContent = common.GetPointer("")
				want[6].ReasoningContent = common.GetPointer("keep original alias")
			}
			assert.Equal(t, want, actual.Messages, "all existing fields and non-assistant messages stay intact")
			encoded, err := common.Marshal(actual)
			require.NoError(t, err)
			var decoded struct {
				Messages []map[string]json.RawMessage `json:"messages"`
			}
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			if test.wantFilled {
				assert.Equal(t, json.RawMessage(`""`), decoded.Messages[2]["reasoning_content"], "empty reasoning must be present on the wire")
			} else {
				assert.NotContains(t, decoded.Messages[2], "reasoning_content")
			}
		})
	}
}

func TestDeepSeekThinkingHistoryKeepsPassthroughAndOtherProvidersUntouched(t *testing.T) {
	service.InitHttpClient()
	settings := model_setting.GetGlobalSettings()
	original := *settings
	t.Cleanup(func() { *settings = original })
	settings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{}
	for _, test := range []struct {
		name        string
		channelType int
		globalPass  bool
		channelPass bool
		wantFilled  bool
	}{
		{name: "DeepSeek converted body", channelType: constant.ChannelTypeDeepSeek, wantFilled: true},
		{name: "global raw passthrough", channelType: constant.ChannelTypeDeepSeek, globalPass: true},
		{name: "channel raw passthrough", channelType: constant.ChannelTypeDeepSeek, channelPass: true},
		{name: "OpenAI provider", channelType: constant.ChannelTypeOpenAI},
		{name: "iFlytek provider", channelType: constant.ChannelTypeIFlytekMaaS},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := `{"model":"application-model","thinking":{"type":"enabled"},"reasoning_effort":"max","messages":[{"role":"assistant","content":"Previous answer"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","content":"Result","tool_call_id":"call_1"}]}`
			received := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				received <- body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture stops before accounting","type":"invalid_request_error"}}`)
			}))
			defer server.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			common.SetContextKey(c, constant.ContextKeyChannelType, test.channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "application-model")
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: test.channelPass})
			common.SetContextKey(c, constant.ContextKeyChannelModelMapping, `{"application-model":"deepseek-v4-pro"}`)
			request := &dto.GeneralOpenAIRequest{}
			require.NoError(t, common.UnmarshalJsonStr(payload, request))
			info := &relaycommon.RelayInfo{OriginModelName: "application-model", RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, RequestURLPath: "/v1/chat/completions", Request: request}
			settings.PassThroughRequestEnabled = test.globalPass
			apiErr := relay.TextHelper(c, info)
			require.NotNil(t, apiErr)
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			var body []byte
			select {
			case body = <-received:
			default:
				t.Fatal("the fixture upstream did not receive a request")
			}
			if test.globalPass || test.channelPass {
				assert.Equal(t, payload, string(body), "raw passthrough remains byte-identical")
			}
			var actual struct {
				Messages []map[string]json.RawMessage `json:"messages"`
			}
			require.NoError(t, common.Unmarshal(body, &actual))
			require.Len(t, actual.Messages, 3)
			for _, index := range []int{0, 1} {
				if test.wantFilled {
					assert.Equal(t, json.RawMessage(`""`), actual.Messages[index]["reasoning_content"])
				} else {
					assert.NotContains(t, actual.Messages[index], "reasoning_content")
				}
				assert.Nil(t, request.Messages[index].ReasoningContent, "the original request must remain reusable for a retry")
			}
			assert.NotContains(t, actual.Messages[2], "reasoning_content")
		})
	}
}

// The -none/-low/-max tails of deepseek-flash and deepseek-v4-* reach DeepSeek
// as its own thinking switch and effort in each upstream format, with the base
// name; a bare name keeps DeepSeek's default, and a blacklisted name is sent
// verbatim.
func TestThinkingSuffixUpstreamFields(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(original, "deepseek-v4-pro-max")

	tests := []struct {
		model, upstream, thinking, effort string
	}{
		{model: "deepseek-flash", upstream: "deepseek-flash"},
		{model: "deepseek-flash-none", upstream: "deepseek-flash", thinking: "disabled"},
		{model: "deepseek-flash-low", upstream: "deepseek-flash", thinking: "enabled", effort: "low"},
		{model: "deepseek-flash-max", upstream: "deepseek-flash", thinking: "enabled", effort: "max"},
		{model: "deepseek-v4-pro-none", upstream: "deepseek-v4-pro", thinking: "disabled"},
		{model: "deepseek-v4-pro-low", upstream: "deepseek-v4-pro", thinking: "enabled", effort: "low"},
		{model: "deepseek-v4-pro-max", upstream: "deepseek-v4-pro-max"},
	}
	newInfo := func(model string) *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{OriginModelName: model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model}}
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			chat := &dto.GeneralOpenAIRequest{Model: tt.model}
			converted, err := (&deepseek.Adaptor{}).ConvertOpenAIRequest(nil, newInfo(tt.model), chat)
			require.NoError(t, err)
			chat = converted.(*dto.GeneralOpenAIRequest)
			assert.Equal(t, tt.upstream, chat.Model)
			assert.Equal(t, tt.effort, chat.ReasoningEffort)

			claude := &dto.ClaudeRequest{Model: tt.model}
			convertedClaude, err := (&deepseek.Adaptor{}).ConvertClaudeRequest(nil, newInfo(tt.model), claude)
			require.NoError(t, err)
			claude = convertedClaude.(*dto.ClaudeRequest)
			assert.Equal(t, tt.upstream, claude.Model)

			converted, err = (&deepseek.Adaptor{}).ConvertOpenAIResponsesRequest(nil, newInfo(tt.model), dto.OpenAIResponsesRequest{Model: tt.model})
			require.NoError(t, err)
			responses := converted.(dto.OpenAIResponsesRequest)
			assert.Equal(t, tt.upstream, responses.Model)

			if tt.thinking == "" {
				assert.Empty(t, chat.THINKING)
				assert.Nil(t, claude.Thinking)
				assert.Nil(t, claude.OutputConfig)
				assert.Nil(t, responses.Reasoning)
				return
			}
			assert.JSONEq(t, `{"type":"`+tt.thinking+`"}`, string(chat.THINKING))
			require.NotNil(t, claude.Thinking)
			assert.Equal(t, tt.thinking, claude.Thinking.Type)
			require.NotNil(t, responses.Reasoning)
			if tt.thinking == "disabled" {
				assert.Nil(t, claude.OutputConfig)
				assert.Equal(t, "none", responses.Reasoning.Effort)
				return
			}
			assert.JSONEq(t, `{"effort":"`+tt.effort+`"}`, string(claude.OutputConfig))
			assert.Equal(t, tt.effort, responses.Reasoning.Effort)
		})
	}
}

// DeepSeek runs Claude Code and Codex searches server-side and bills them as
// Search and Fetch API calls, whatever model name the client sent. On the
// Responses API the completed web_search_call items of the whole response
// replace the items counted while streaming: open_page is a Fetch call, a
// failed call is free, and an empty output keeps the streamed count.
func TestServerSideSearchBillingKeys(t *testing.T) {
	for _, tc := range []struct {
		format types.RelayFormat
		mode   int
	}{
		{types.RelayFormatClaude, relayconstant.RelayModeUnknown},
		{types.RelayFormatOpenAI, relayconstant.RelayModeChatCompletions},
		{types.RelayFormatOpenAIResponses, relayconstant.RelayModeResponses},
	} {
		info := &relaycommon.RelayInfo{RelayFormat: tc.format, RelayMode: tc.mode, ChannelMeta: &relaycommon.ChannelMeta{}}
		(&deepseek.Adaptor{}).Init(info)
		assert.Equal(t, deepseek.SearchToolPriceKey, info.WebSearchBillingKey, tc.format)
		assert.Equal(t, deepseek.FetchToolPriceKey, info.WebFetchBillingKey, tc.format)
		assert.Equal(t, tc.mode == relayconstant.RelayModeResponses, info.VendorToolUsage != nil, tc.format)
	}

	const output = `[
		{"type":"web_search_call","status":"completed","action":{"type":"search","queries":["deepseek","ws_call_id=call_01"]}},
		{"type":"web_search_call","status":"completed","action":{"type":"open_page","url":"https://example.com/"}},
		{"type":"web_search_call","status":"failed","action":{"type":"open_page","url":"https://api-docs.deepseek.com/"}},
		{"type":"message","status":"completed"}]`
	for _, raw := range []string{
		`{"status":"completed","output":` + output + `}`,
		`{"type":"response.completed","response":{"status":"completed","output":` + output + `}}`,
	} {
		info := &relaycommon.RelayInfo{
			OriginModelName: "gpt-5.1-codex",
			RelayFormat:     types.RelayFormatOpenAIResponses,
			RelayMode:       relayconstant.RelayModeResponses,
			ChannelMeta:     &relaycommon.ChannelMeta{},
		}
		(&deepseek.Adaptor{}).Init(info)
		for range 3 {
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		}
		assert.Equal(t, map[string]int{deepseek.SearchToolPriceKey: 3}, toolCounts(info), "streamed items bill under the Search key")

		info.ApplyVendorToolUsage([]byte(`{"type":"response.failed","response":{"status":"failed","output":[]}}`))
		assert.Equal(t, map[string]int{deepseek.SearchToolPriceKey: 3}, toolCounts(info), "an empty output keeps the streamed count")

		info.ApplyVendorToolUsage([]byte(raw))
		assert.Equal(t, map[string]int{deepseek.SearchToolPriceKey: 1, deepseek.FetchToolPriceKey: 1}, toolCounts(info), raw)
	}
}

func toolCounts(info *relaycommon.RelayInfo) map[string]int {
	counts := map[string]int{}
	for name, tool := range info.ResponsesUsageInfo.BuiltInTools {
		counts[name] = tool.CallCount
	}
	return counts
}

// Explicit @ modifiers reach DeepSeek as its own thinking switch and effort in
// every format and override the client's reasoning fields: no budget_tokens,
// adaptive thinking, max_tokens raise or sampling change. An @ effort wins
// over a -none/-low/-max tail, a budget maps to an effort, and a request
// without a modifier passes through unchanged. The usage log records the
// effort DeepSeek runs at.
func TestModelModifiersUpstreamFields(t *testing.T) {
	type client struct{ chat, claude, responses string }
	tests := []struct {
		name, model      string
		client           client
		thinking, effort string // thinking "" = untouched
		logEffort        string
	}{
		{name: "thinking on", model: "deepseek-flash@thinking:on", thinking: "enabled", logEffort: "high"},
		{name: "thinking off", model: "deepseek-flash@thinking:off", thinking: "disabled", logEffort: "none"},
		{name: "thinking zero budget", model: "deepseek-flash@thinking:0", thinking: "disabled", logEffort: "none"},
		{name: "effort none", model: "deepseek-flash@effort:none", thinking: "disabled", logEffort: "none"},
		{name: "effort low", model: "deepseek-flash@effort:low", thinking: "enabled", effort: "low", logEffort: "low"},
		{name: "effort medium", model: "deepseek-flash@effort:medium", thinking: "enabled", effort: "medium", logEffort: "high"},
		{name: "effort xhigh", model: "deepseek-flash@effort:xhigh", thinking: "enabled", effort: "xhigh", logEffort: "high"},
		{name: "effort max on pro", model: "deepseek-v4-pro@effort:max", thinking: "enabled", effort: "max", logEffort: "max"},
		{name: "budget maps to an effort", model: "deepseek-flash@thinking:8192", thinking: "enabled", effort: "medium", logEffort: "high"},
		{name: "adaptive", model: "deepseek-flash@thinking:adaptive", thinking: "enabled", logEffort: "high"},
		{name: "temperature with effort", model: "deepseek-flash@temperature:0.3@effort:low", thinking: "enabled", effort: "low", logEffort: "low"},
		{name: "effort overrides client thinking off", model: "deepseek-flash@effort:max", thinking: "enabled", effort: "max", logEffort: "max",
			client: client{chat: `"thinking":{"type":"disabled"}`, claude: `"thinking":{"type":"disabled"}`, responses: `"reasoning":{"effort":"none"}`}},
		{name: "thinking off overrides client thinking", model: "deepseek-flash@thinking:off", thinking: "disabled", logEffort: "none",
			client: client{chat: `"thinking":{"type":"enabled"},"reasoning_effort":"high"`,
				claude:    `"thinking":{"type":"enabled","budget_tokens":2048},"output_config":{"effort":"high","format":{"type":"json_schema"}}`,
				responses: `"reasoning":{"effort":"high"}`}},
		{name: "budget overrides client effort none", model: "deepseek-flash@thinking:8192", thinking: "enabled", effort: "medium", logEffort: "high",
			client: client{responses: `"reasoning":{"effort":"none"}`}},
		{name: "@ effort wins over a tail", model: "deepseek-flash-max@effort:low", thinking: "enabled", effort: "low", logEffort: "low"},
		{name: "no modifier", model: "deepseek-flash",
			client: client{chat: `"thinking":{"type":"disabled"},"reasoning_effort":"low"`, claude: `"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`, responses: `"reasoning":{"effort":"low"}`}},
	}
	extra := func(members string) string {
		if members == "" {
			return ""
		}
		return "," + members
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodies := map[types.RelayFormat]string{
				types.RelayFormatOpenAI:          fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]%s}`, tt.model, extra(tt.client.chat)),
				types.RelayFormatClaude:          fmt.Sprintf(`{"model":%q,"max_tokens":64,"temperature":0.7,"messages":[{"role":"user","content":"hi"}]%s}`, tt.model, extra(tt.client.claude)),
				types.RelayFormatOpenAIResponses: fmt.Sprintf(`{"model":%q,"input":"hi"%s}`, tt.model, extra(tt.client.responses)),
			}
			for format, body := range bodies {
				info := &relaycommon.RelayInfo{
					OriginModelName: tt.model,
					RelayFormat:     format,
					ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tt.model},
				}
				var sent []byte
				switch format {
				case types.RelayFormatOpenAI:
					original, outbound := &dto.GeneralOpenAIRequest{}, &dto.GeneralOpenAIRequest{}
					require.NoError(t, common.UnmarshalJsonStr(body, original))
					require.NoError(t, common.UnmarshalJsonStr(body, outbound))
					info.Request = original
					require.NoError(t, helper.ApplyReasoningModelSuffix(nil, info, outbound))
					converted, err := (&deepseek.Adaptor{}).ConvertOpenAIRequest(nil, info, outbound)
					require.NoError(t, err)
					sent, err = common.Marshal(converted)
					require.NoError(t, err)
				case types.RelayFormatClaude:
					original, outbound := &dto.ClaudeRequest{}, &dto.ClaudeRequest{}
					require.NoError(t, common.UnmarshalJsonStr(body, original))
					require.NoError(t, common.UnmarshalJsonStr(body, outbound))
					info.Request = original
					require.NoError(t, helper.ApplyReasoningModelSuffix(nil, info, outbound))
					converted, err := (&deepseek.Adaptor{}).ConvertClaudeRequest(nil, info, outbound)
					require.NoError(t, err)
					sent, err = common.Marshal(converted)
					require.NoError(t, err)
				default:
					original, outbound := &dto.OpenAIResponsesRequest{}, &dto.OpenAIResponsesRequest{}
					require.NoError(t, common.UnmarshalJsonStr(body, original))
					require.NoError(t, common.UnmarshalJsonStr(body, outbound))
					info.Request = original
					require.NoError(t, helper.ApplyReasoningModelSuffix(nil, info, outbound))
					converted, err := (&deepseek.Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, *outbound)
					require.NoError(t, err)
					sent, err = common.Marshal(converted)
					require.NoError(t, err)
				}
				wire := gjson.ParseBytes(sent)
				wantModel := "deepseek-flash"
				if strings.HasPrefix(tt.model, "deepseek-v4-pro") {
					wantModel = "deepseek-v4-pro"
				}
				assert.Equal(t, wantModel, wire.Get("model").String(), format)
				if tt.thinking == "" {
					// No modifier: the client's reasoning fields go out as sent.
					for _, member := range []string{"thinking", "reasoning_effort", "output_config", "reasoning"} {
						assert.Equal(t, gjson.Get(body, member).Raw, wire.Get(member).Raw, "%s %s", format, member)
					}
					continue
				}
				switch format {
				case types.RelayFormatOpenAI:
					assert.JSONEq(t, `{"type":"`+tt.thinking+`"}`, wire.Get("thinking").Raw, format)
					assert.Equal(t, tt.effort, wire.Get("reasoning_effort").String(), format)
					assert.False(t, wire.Get("reasoning").Exists(), format)
				case types.RelayFormatClaude:
					assert.JSONEq(t, `{"type":"`+tt.thinking+`"}`, wire.Get("thinking").Raw, format)
					assert.Equal(t, tt.effort, wire.Get("output_config.effort").String(), format)
					assert.Equal(t, gjson.Get(body, "output_config.format").Raw, wire.Get("output_config.format").Raw, format)
					assert.Equal(t, int64(64), wire.Get("max_tokens").Int(), format)
					wantTemperature := 0.7
					if strings.Contains(tt.model, "@temperature:0.3") {
						wantTemperature = 0.3
					}
					assert.Equal(t, wantTemperature, wire.Get("temperature").Float(), format)
				default:
					wantEffort := tt.effort
					if tt.thinking == "disabled" {
						wantEffort = "none"
					}
					assert.Equal(t, wantEffort, wire.Get("reasoning.effort").String(), format)
				}
				assert.Equal(t, tt.logEffort, info.ReasoningEffort, format)
			}
		})
	}
}
