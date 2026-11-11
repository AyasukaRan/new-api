package deepseek_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
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

func TestDeepSeekResponsesCompletesThinkingToolHistory(t *testing.T) {
	const blank = `{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":" "}]}`
	const user = `{"role":"user","content":"Find this"}`
	const assistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking"}],"unknown":9007199254740993}`
	const call = `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"你好\\nworld\"}","extra":{"keep":false}}`
	const output = `{"type":"function_call_output","call_id":"call_1","output":"Result"}`
	const call2 = `{"type":"function_call","call_id":"call_2","name":"lookup","arguments":"{}"}`
	const output2 = `{"type":"function_call_output","call_id":"call_2","output":"Second result"}`
	const real = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Summary"}],"content":[{"type":"reasoning_text","text":"Original thoughts\n思考"}],"encrypted_content":"opaque"}`
	const summary = `{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"Keep summary"}],"encrypted_content":"opaque"}`
	const summaryCompleted = `{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"Keep summary"}],"encrypted_content":"opaque","content":[{"type":"reasoning_text","text":" "}]}`
	for _, test := range []struct {
		name     string
		model    string
		upstream string
		effort   string
		tools    string
		input    string
		want     string
	}{
		{name: "reported function history without tool definitions", input: `[` + user + `,` + assistant + `,` + call + `,` + output + `]`, want: `[` + user + `,` + blank + `,` + assistant + `,` + call + `,` + output + `]`},
		{name: "parallel calls share one reasoning item", input: `[` + call + `,` + call2 + `,` + output + `,` + output2 + `]`, want: `[` + blank + `,` + call + `,` + call2 + `,` + output + `,` + output2 + `]`},
		{name: "tool outputs begin another assistant turn", input: `[` + call + `,` + output + `,` + assistant + `,` + call2 + `,` + output2 + `]`, want: `[` + blank + `,` + call + `,` + output + `,` + blank + `,` + assistant + `,` + call2 + `,` + output2 + `]`},
		{name: "keep real reasoning and repair only missing round", input: `[` + real + `,` + call + `,` + output + `,` + assistant + `]`, want: `[` + real + `,` + call + `,` + output + `,` + blank + `,` + assistant + `]`},
		{name: "complete existing summary and encrypted item", input: `[` + summary + `,` + call + `]`, want: `[` + summaryCompleted + `,` + call + `]`},
		{name: "complete empty reasoning text", input: `[{"type":"reasoning","content":[{"type":"reasoning_text","text":"","extra":true}]},` + call + `]`, want: `[{"type":"reasoning","content":[{"type":"reasoning_text","text":" ","extra":true}]},` + call + `]`},
		{name: "complete missing text and retain other content", input: `[{"type":"reasoning","content":[{"type":"other","keep":true},{"type":"reasoning_text"}]},` + call + `]`, want: `[{"type":"reasoning","content":[{"type":"other","keep":true},{"type":"reasoning_text","text":" "}]},` + call + `]`},
		{name: "complete multiple empty parts without replacing real text", input: `[{"type":"reasoning","id":"rs_many","content":[{"type":"reasoning_text","text":"","id":"part_1"},{"type":"other","keep":{"number":9007199254740993}},{"type":"reasoning_text","text":"Exact thoughts\n思考"},{"type":"reasoning_text","text":null,"extra":false},{"type":"reasoning_text"}]},` + call + `]`, want: `[{"type":"reasoning","id":"rs_many","content":[{"type":"reasoning_text","text":" ","id":"part_1"},{"type":"other","keep":{"number":9007199254740993}},{"type":"reasoning_text","text":"Exact thoughts\n思考"},{"type":"reasoning_text","text":" ","extra":false},{"type":"reasoning_text","text":" "}]},` + call + `]`},
		{name: "append reasoning text to existing content array", input: `[{"type":"reasoning","content":[{"type":"other","text":"Keep"}]},` + call + `]`, want: `[{"type":"reasoning","content":[{"type":"other","text":"Keep"},{"type":"reasoning_text","text":" "}]},` + call + `]`},
		{name: "complete null content", input: `[{"type":"reasoning","content":null},` + call + `]`, want: `[{"type":"reasoning","content":[{"type":"reasoning_text","text":" "}]},` + call + `]`},
		{name: "all real reasoning remains byte identical", input: `[ ` + real + `, ` + call + ` ]`},
		{name: "tool definitions require prior assistant reasoning", tools: `[{"type":"function","name":"lookup"}]`, input: `[{"role":"assistant","content":"Earlier answer"},` + user + `]`, want: `[` + blank + `,{"role":"assistant","content":"Earlier answer"},` + user + `]`},
		{name: "tool history requires earlier assistant reasoning without definitions", input: `[` + assistant + `,` + user + `,` + call + `,` + output + `]`, want: `[` + blank + `,` + assistant + `,` + user + `,` + blank + `,` + call + `,` + output + `]`},
		{name: "custom tool turns", input: `[{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"custom_tool_call_output","call_id":"c1","output":"Done"},` + assistant + `]`, want: `[` + blank + `,{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"custom_tool_call_output","call_id":"c1","output":"Done"},` + blank + `,` + assistant + `]`},
		{name: "mapped model enables repair", model: "application-model", upstream: "deepseek-flash", input: `[` + call + `]`, want: `[` + blank + `,` + call + `]`},
		{name: "explicit effort enables alias", model: "provider-alias", effort: "max", input: `[` + call + `]`, want: `[` + blank + `,` + call + `]`},
		{name: "none effort stays unchanged", effort: "none", input: `[` + call + `]`},
		{name: "none suffix stays unchanged", model: "deepseek-flash-none", input: `[` + call + `]`},
		{name: "legacy nonthinking model stays unchanged", model: "deepseek-chat", input: `[` + call + `]`},
		{name: "unknown alias stays unchanged", model: "provider-alias", input: `[` + call + `]`},
		{name: "plain chat without tools stays unchanged", input: `[` + user + `,` + assistant + `]`},
		{name: "empty tool definitions are not tools", tools: `[]`, input: `[` + assistant + `]`},
		{name: "only user input stays unchanged", tools: `[{"type":"function","name":"lookup"}]`, input: `[` + user + `]`},
		{name: "string input stays unchanged", tools: `[{"type":"function","name":"lookup"}]`, input: `"Hello"`},
		{name: "invalid input item is not rewritten", tools: `[{"type":"function","name":"lookup"}]`, input: `[42,` + assistant + `]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := test.model
			if model == "" {
				model = "deepseek-flash"
			}
			upstream := test.upstream
			if upstream == "" {
				upstream = model
			}
			request := dto.OpenAIResponsesRequest{Model: upstream, Input: json.RawMessage(test.input), Tools: json.RawMessage(test.tools)}
			if test.effort != "" {
				request.Reasoning = &dto.Reasoning{Effort: test.effort}
			}
			info := &relaycommon.RelayInfo{OriginModelName: model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: upstream}}
			converted, err := (&deepseek.Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
			require.NoError(t, err)
			actual, ok := converted.(dto.OpenAIResponsesRequest)
			require.True(t, ok)
			assert.Equal(t, request.Input, actual.Input, "conversion must defer history repair until after parameter overrides")
			wire, err := common.Marshal(actual)
			require.NoError(t, err)
			inputBeforeFinalization := ojson.GetBytes(wire, "input").Raw
			wire, err = (&deepseek.Adaptor{}).FinalizeResponsesRequest(info, wire)
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(wire, &actual))
			if test.want == "" {
				assert.Equal(t, inputBeforeFinalization, string(actual.Input), "history that needs no repair keeps its wire bytes")
			} else {
				assert.JSONEq(t, test.want, string(actual.Input))
			}
			assert.Equal(t, test.input, string(request.Input), "retry input must remain untouched")
			assert.Equal(t, test.tools, string(actual.Tools))
			if strings.Contains(test.input, `9007199254740993`) {
				assert.Contains(t, string(actual.Input), `9007199254740993`, "unknown integer precision must survive repair")
			}
			again, err := (&deepseek.Adaptor{}).FinalizeResponsesRequest(info, wire)
			require.NoError(t, err)
			assert.Equal(t, wire, again, "repeated finalization must not duplicate reasoning")
		})
	}
}

func TestCodexLiveWebSearchUsesNativeDeepSeekResponses(t *testing.T) {
	const call = `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","queries":["DeepSeek","ws_call_id=call_01"]}}`
	const tools = `[{"type":"web_search","external_web_access":true}]`
	const input = `[` + call + `,{"role":"user","content":"Open the result"}]`
	const payload = `{"model":"deepseek-v4-pro","stream":true,"input":` + input + `,"tools":` + tools + `}`
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, constant.ChannelBaseURLs[constant.ChannelTypeDeepSeek])
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "deepseek-v4-pro")
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(payload, &request))
	info := &relaycommon.RelayInfo{OriginModelName: request.Model, RelayFormat: types.RelayFormatOpenAIResponses,
		RelayMode: relayconstant.RelayModeResponses, RequestURLPath: "/v1/responses", Request: &request, IsStream: true, DisablePing: true}
	adaptor, body, closer, apiErr := relay.PrepareResponsesRequest(c, info, &request)
	require.Nil(t, apiErr)
	defer closer.Close()
	upstreamURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepseek.com/responses", upstreamURL)
	wire, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.JSONEq(t, payload, string(wire))

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	var sse strings.Builder
	for _, event := range []string{
		`{"type":"response.web_search_call.searching","output_index":0,"item_id":"ws_1","sequence_number":1}`,
		`{"type":"response.output_item.done","output_index":0,"sequence_number":2,"item":` + call + `}`,
		`{"type":"response.completed","sequence_number":3,"response":{"status":"completed","output":[` + call + `],"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25,"server_tool_use":{"tool_search":1,"tool_fetch":0}}}}`,
	} {
		fmt.Fprintf(&sse, "event: %s\ndata: %s\n\n", ojson.Get(event, "type").String(), event)
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse.String())), Header: http.Header{"Content-Type": {"text/event-stream"}}}
	_, apiErr = adaptor.DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	assert.Equal(t, sse.String(), writer.Body.String())
}

func TestDeepSeekResponsesHistoryRespectsProviderAndPassthrough(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := *settings
	t.Cleanup(func() { *settings = original })
	for _, test := range []struct {
		name        string
		channelType int
		globalPass  bool
		channelPass bool
		wantFilled  bool
	}{
		{name: "DeepSeek converted body", channelType: constant.ChannelTypeDeepSeek, wantFilled: true},
		{name: "global passthrough", channelType: constant.ChannelTypeDeepSeek, globalPass: true},
		{name: "channel passthrough", channelType: constant.ChannelTypeDeepSeek, channelPass: true},
		{name: "OpenAI provider", channelType: constant.ChannelTypeOpenAI},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := `{"model":"application-model","reasoning":{"effort":"max"},"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"Result"}]}`
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			common.SetContextKey(c, constant.ContextKeyChannelType, test.channelType)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "application-model")
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: test.channelPass})
			common.SetContextKey(c, constant.ContextKeyChannelModelMapping, `{"application-model":"deepseek-flash"}`)
			request := &dto.OpenAIResponsesRequest{}
			require.NoError(t, common.UnmarshalJsonStr(payload, request))
			input := string(request.Input)
			info := &relaycommon.RelayInfo{OriginModelName: "application-model", RelayFormat: types.RelayFormatOpenAIResponses, RelayMode: relayconstant.RelayModeResponses, RequestURLPath: "/v1/responses", Request: request}
			settings.PassThroughRequestEnabled = test.globalPass
			_, body, closer, apiErr := relay.PrepareResponsesRequest(c, info, request)
			require.Nil(t, apiErr)
			defer closer.Close()
			wire, err := io.ReadAll(body)
			require.NoError(t, err)
			if test.globalPass || test.channelPass {
				assert.Equal(t, payload, string(wire))
			}
			if test.wantFilled {
				assert.Equal(t, "reasoning", ojson.GetBytes(wire, "input.0.type").String())
				assert.Equal(t, " ", ojson.GetBytes(wire, "input.0.content.0.text").String())
				assert.Equal(t, "deepseek-flash", ojson.GetBytes(wire, "model").String())
			} else {
				assert.JSONEq(t, input, ojson.GetBytes(wire, "input").Raw)
			}
			assert.Equal(t, input, string(request.Input))
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

	const usage = `"usage":{"server_tool_use":{"tool_search":3,"tool_fetch":2}}`
	const output = `"output":[{"type":"web_search_call","status":"completed","action":{"type":"search"}},{"type":"tool_search_call"}]`
	for _, tc := range []struct {
		name, raw string
		want      map[string]int
	}{
		{"nonstream", `{` + output + `,` + usage + `}`, map[string]int{deepseek.SearchToolPriceKey: 3, deepseek.FetchToolPriceKey: 2}},
		{"stream complete", `{"type":"response.completed","response":{` + usage + `}}`, map[string]int{deepseek.SearchToolPriceKey: 3, deepseek.FetchToolPriceKey: 2}},
		{"stream incomplete", `{"type":"response.incomplete","response":{` + usage + `}}`, map[string]int{deepseek.SearchToolPriceKey: 3, deepseek.FetchToolPriceKey: 2}},
		{"stream failed", `{"type":"response.failed","response":{` + usage + `}}`, map[string]int{deepseek.SearchToolPriceKey: 3, deepseek.FetchToolPriceKey: 2}},
		{"no successful operations", `{` + output + `,"usage":{"total_tokens":64}}`, map[string]int{}},
		{"stream ended without usage", "", map[string]int{}},
		{"zero search", `{"usage":{"server_tool_use":{"tool_search":0,"tool_fetch":2}}}`, map[string]int{deepseek.SearchToolPriceKey: 0, deepseek.FetchToolPriceKey: 2}},
		{"overflowing integer", `{"usage":{"server_tool_use":{"tool_search":99999999999999999999999}}}`, map[string]int{deepseek.SearchToolPriceKey: math.MaxInt}},
		{"large mantissa with exponent", `{"usage":{"server_tool_use":{"tool_search":100000000000000000000e-20}}}`, map[string]int{}},
		{"invalid counts", `{"usage":{"server_tool_use":{"tool_search":-1,"tool_fetch":1.5}}}`, map[string]int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIResponses, RelayMode: relayconstant.RelayModeResponses, ChannelMeta: &relaycommon.ChannelMeta{}}
			(&deepseek.Adaptor{}).Init(info)
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
			info.CountBillableToolCall("tool_search_call", "")
			info.ApplyVendorToolUsage([]byte(tc.raw))
			assert.Equal(t, tc.want, toolCounts(info))
		})
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
				wire := ojson.ParseBytes(sent)
				wantModel := "deepseek-flash"
				if strings.HasPrefix(tt.model, "deepseek-v4-pro") {
					wantModel = "deepseek-v4-pro"
				}
				assert.Equal(t, wantModel, wire.Get("model").String(), format)
				if tt.thinking == "" {
					// No modifier: the client's reasoning fields go out as sent.
					for _, member := range []string{"thinking", "reasoning_effort", "output_config", "reasoning"} {
						assert.Equal(t, ojson.Get(body, member).Raw, wire.Get(member).Raw, "%s %s", format, member)
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
					assert.Equal(t, ojson.Get(body, "output_config.format").Raw, wire.Get("output_config.format").Raw, format)
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

// Synthetic shapes reproduced against DeepSeek's native Responses endpoint on
// 2026-10-28; see also https://github.com/yetone/magpie/issues/1822.
// An empty string fails while a space before the assistant step succeeds.
func TestResponsesReasoningReplay(t *testing.T) {
	// Exercise the final wire-body hook without dropping unknown JSON fields.
	finalize := func(info *relaycommon.RelayInfo, req dto.OpenAIResponsesRequest) (any, error) {
		data, err := common.Marshal(req)
		if err != nil {
			return nil, err
		}
		data, err = (&deepseek.Adaptor{}).FinalizeResponsesRequest(info, data)
		if err != nil {
			return nil, err
		}
		var out dto.OpenAIResponsesRequest
		err = common.Unmarshal(data, &out)
		return out, err
	}
	const r = `{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":" "}]}`
	const note = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking."}]}`
	const call = `{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}`
	const output = `{"type":"function_call_output","call_id":"c1","output":"7"}`
	const call2 = `{"type":"function_call","call_id":"c2","name":"probe","arguments":"{}"}`
	const output2 = `{"type":"function_call_output","call_id":"c2","output":"8"}`
	const customCall = `{"type":"custom_tool_call","call_id":"custom1","name":"apply_patch","input":"synthetic"}`
	const customOutput = `{"type":"custom_tool_call_output","call_id":"custom1","output":"ok"}`
	const actual = `{"type":"reasoning","id":"r1","summary":[],"encrypted_content":"opaque","content":[{"type":"reasoning_text","text":"Existing reasoning"}],"extra":9007199254740993}`
	tests := []struct{ name, input, want string }{
		{"missing", "[" + note + "," + call + "," + output + "]", "[" + r + "," + note + "," + call + "," + output + "]"},
		{"empty", `[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":""}]},` + call + "]", "[" + r + "," + call + "]"},
		{"null content", `[{"type":"reasoning","summary":[],"content":null},` + call + "]", "[" + r + "," + call + "]"},
		{"missing content", `[{"type":"reasoning","summary":[]},` + call + "]", "[" + r + "," + call + "]"},
		{"empty content", `[{"type":"reasoning","summary":[],"content":[]},` + call + "]", "[" + r + "," + call + "]"},
		{"move existing", "[" + note + "," + actual + "," + call + "," + output + "]", "[" + actual + "," + note + "," + call + "," + output + "]"},
		{"preserve", "[" + actual + "," + note + "," + call + "," + output + "]", "[" + actual + "," + note + "," + call + "," + output + "]"},
		{"interleaved", "[" + call + "," + output + "," + call2 + "," + output2 + "]", "[" + r + "," + call + "," + output + "," + r + "," + call2 + "," + output2 + "]"},
		{"after tool", "[" + call + "," + output + "," + note + "]", "[" + r + "," + call + "," + output + "," + r + "," + note + "]"},
		{"new user boundary", "[" + call + "," + output + `,{"role":"user","content":"Next"},` + note + "]", "[" + r + "," + call + "," + output + `,{"role":"user","content":"Next"},` + r + "," + note + "]"},
		{"parallel", "[" + note + "," + call + "," + call2 + "," + output + "," + output2 + "]", "[" + r + "," + note + "," + call + "," + call2 + "," + output + "," + output2 + "]"},
		{"missing text", `[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text"}]},` + call + "]", "[" + r + "," + call + "]"},
		{"null text", `[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":null}]},` + call + "]", "[" + r + "," + call + "]"},
		{"preserve whitespace", "[" + r + "," + call + "]", "[" + r + "," + call + "]"},
		{"unknown boundary", "[" + call + "," + output + `,{"type":"future_item"},` + note + "]", "[" + r + "," + call + "," + output + `,{"type":"future_item"},` + r + "," + note + "]"},
		{"custom tool", "[" + note + "," + customCall + "," + customOutput + "," + note + "]", "[" + r + "," + note + "," + customCall + "," + customOutput + "," + r + "," + note + "]"},
		{"message only", "[" + note + "]", "[" + note + "]"},
		{"string input", `"hello"`, `"hello"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","input":`+tt.input+`}`), &req))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.deepseek.com"}}
			converted, err := finalize(info, req)
			require.NoError(t, err)
			out := converted.(dto.OpenAIResponsesRequest)
			assert.JSONEq(t, tt.want, string(out.Input))
			if strings.Contains(tt.input, `"extra":9007199254740993`) {
				assert.Contains(t, string(out.Input), `"extra":9007199254740993`)
			}
			assert.Equal(t, tt.input, string(req.Input), "input belongs to the caller")
			again, err := finalize(info, out)
			require.NoError(t, err)
			assert.Equal(t, string(out.Input), string(again.(dto.OpenAIResponsesRequest).Input))
		})
	}
	for _, endpoint := range []string{
		"", "https://api.deepseek.com/v1", "https://api.deepseek.com:443",
		"https://API.DEEPSEEK.COM", "https://Api.DeepSeek.Com:443/v1",
	} {
		t.Run("official "+endpoint, func(t *testing.T) {
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","input":[`+call+`]}`), &req))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: endpoint}}
			converted, err := finalize(info, req)
			require.NoError(t, err)
			assert.JSONEq(t, "["+r+","+call+"]", string(converted.(dto.OpenAIResponsesRequest).Input))
		})
	}
	for _, endpoint := range []string{
		"https://other.example", "https://api.deepseek.com.example", "http://api.deepseek.com",
		"https://API.DEEPSEEK.COM:8443", "https://api.deepseek.com:invalid",
	} {
		t.Run(endpoint, func(t *testing.T) {
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","input":[`+call+`]}`), &req))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: endpoint}}
			converted, err := finalize(info, req)
			require.NoError(t, err)
			assert.Equal(t, req.Input, converted.(dto.OpenAIResponsesRequest).Input)
		})
	}
	t.Run("thinking disabled", func(t *testing.T) {
		var req dto.OpenAIResponsesRequest
		require.NoError(t, common.Unmarshal([]byte(`{"model":"deepseek-flash","reasoning":{"effort":"none"},"input":[`+call+`]}`), &req))
		converted, err := finalize(nil, req)
		require.NoError(t, err)
		assert.Equal(t, req.Input, converted.(dto.OpenAIResponsesRequest).Input)
	})
}

// Long tool sessions are dominated by tool-result payloads, not item count.
// Keep their bodies opaque while measuring clean and repair paths.
func BenchmarkResponsesReasoningReplay(b *testing.B) {
	for _, rounds := range []int{4, 256} {
		for _, repair := range []bool{false, true} {
			b.Run(fmt.Sprintf("rounds=%d/repair=%t", rounds, repair), func(b *testing.B) {
				var input strings.Builder
				input.WriteByte('[')
				for i := range rounds {
					if i > 0 {
						input.WriteByte(',')
					}
					fmt.Fprintf(&input, `{"role":"user","content":"Inspect result %d"},`, i)
					if !repair || i != rounds-1 {
						input.WriteString(`{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Check the result."}]},`)
					}
					fmt.Fprintf(&input, `{"type":"function_call","name":"probe","call_id":"c%d","arguments":"{}"},{"type":"function_call_output","call_id":"c%d","output":"%s"}`, i, i, strings.Repeat("synthetic result ", 256))
				}
				input.WriteByte(']')
				data := []byte(`{"model":"deepseek-flash","input":` + input.String() + `}`)
				adaptor := &deepseek.Adaptor{}
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				b.ResetTimer()
				for b.Loop() {
					_, err := adaptor.FinalizeResponsesRequest(nil, data)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
