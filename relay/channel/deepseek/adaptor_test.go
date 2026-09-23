package deepseek_test

import (
	"encoding/json"
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
