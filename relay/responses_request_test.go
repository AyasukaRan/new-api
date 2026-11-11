package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/deepseek"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type deepSeekResponsesCaptureAdaptor struct {
	deepseek.Adaptor
	body []byte
}

func (a *deepSeekResponsesCaptureAdaptor) DoRequest(_ *gin.Context, _ *relaycommon.RelayInfo, body io.Reader) (any, error) {
	var err error
	a.body, err = io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return nil, errors.New("captured test request without upstream access")
}

func TestPrepareResponsesRequestDeepSeekEffectiveReasoning(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	previous := settings.PassThroughRequestEnabled
	t.Cleanup(func() { settings.PassThroughRequestEnabled = previous })
	settings.PassThroughRequestEnabled = false
	const input = `[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]`
	const realReasoning = `{"type":"reasoning","id":"rs_client_history","summary":[],"content":[{"type":"reasoning_text","text":"Existing thoughts"}],"encrypted_content":"opaque","provider_metadata":{"trace_id":"trace-private","number":9007199254740993}}`
	const assistant = `{"role":"assistant","content":"Continue"}`
	const history = `[` + realReasoning + `,{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"Result"},` + assistant + `]`
	const repairedHistory = `[` + realReasoning + `,{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"Result"},{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":" "}]},` + assistant + `]`
	for _, tc := range []struct {
		name, effort, override, endpoint         string
		history, wantInput                       string
		passthrough, replaceInput, wantReasoning bool
		globalPassthrough                        bool
	}{
		{name: "override enables thinking", effort: "none", override: "high", wantReasoning: true},
		{name: "override disables thinking", effort: "high", override: "none"},
		{name: "override replaces history", effort: "high", override: "high", history: history, replaceInput: true, wantReasoning: true},
		{name: "override disables repair but keeps existing reasoning", effort: "high", override: "none", history: history, wantInput: history},
		{name: "override enables repair and preserves opaque history", effort: "none", override: "high", history: history, wantInput: repairedHistory},
		{name: "no override", effort: "high", wantReasoning: true},
		{name: "thinking disabled", effort: "none"},
		{name: "third party endpoint", effort: "none", override: "high", endpoint: "https://other.example"},
		{name: "third party endpoint with thinking already enabled", effort: "high", endpoint: "https://other.example"},
		{name: "passthrough bypasses overrides and normalization", effort: "none", override: "high", passthrough: true},
		{name: "global passthrough bypasses normalization", effort: "high", globalPassthrough: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings.PassThroughRequestEnabled = tc.globalPassthrough
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = "https://API.DEEPSEEK.COM:443/v1"
			}
			requestInput := tc.history
			if requestInput == "" {
				requestInput = input
			}
			raw := `{"model":"deepseek-flash","reasoning":{"effort":"` + tc.effort + `"},"input":` + requestInput + `}`
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "deepseek-flash")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeDeepSeek)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, endpoint)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passthrough})
			if tc.override != "" {
				ops := []any{map[string]any{"path": "reasoning.effort", "mode": "set", "value": tc.override}, map[string]any{"path": "vendor_extension", "mode": "set", "value": "preserved"}}
				if tc.replaceInput {
					var replacement any
					require.NoError(t, common.Unmarshal([]byte(input), &replacement))
					ops = append(ops, map[string]any{"path": "input", "mode": "set", "value": replacement})
				}
				common.SetContextKey(c, constant.ContextKeyChannelParamOverride, map[string]any{"operations": ops})
			}
			var req dto.OpenAIResponsesRequest
			require.NoError(t, common.Unmarshal([]byte(raw), &req))
			info := relaycommon.GenRelayInfoResponses(c, &req)
			_, body, closer, apiErr := PrepareResponsesRequest(c, info, &req)
			require.Nil(t, apiErr)
			t.Cleanup(func() { require.NoError(t, closer.Close()) })
			wire, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.Equal(t, requestInput, string(req.Input), "retry input must remain untouched")
			if tc.passthrough || tc.globalPassthrough {
				assert.JSONEq(t, raw, string(wire))
				return
			}
			wantEffort := tc.effort
			if tc.override != "" {
				wantEffort = tc.override
				assert.Equal(t, "preserved", gjson.GetBytes(wire, "vendor_extension").String())
			}
			assert.Equal(t, wantEffort, gjson.GetBytes(wire, "reasoning.effort").String())
			if tc.wantInput != "" {
				assert.JSONEq(t, tc.wantInput, gjson.GetBytes(wire, "input").Raw)
				assert.Equal(t, realReasoning, gjson.GetBytes(wire, "input.0").Raw, "client history identifiers and opaque fields remain byte identical")
			} else if tc.wantReasoning {
				assert.Equal(t, "reasoning", gjson.GetBytes(wire, "input.0.type").String())
				assert.Equal(t, " ", gjson.GetBytes(wire, "input.0.content.0.text").String())
				assert.Equal(t, "function_call", gjson.GetBytes(wire, "input.1.type").String())
			} else {
				assert.JSONEq(t, input, gjson.GetBytes(wire, "input").Raw)
			}
		})
	}
}

func TestClaudeViaResponsesFinalizesDeepSeekAfterOverrides(t *testing.T) {
	const raw = `{"model":"deepseek-flash","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"Result"}]}]}`
	for _, effort := range []string{"high", "none"} {
		t.Run(effort, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(raw))
			var request dto.ClaudeRequest
			require.NoError(t, common.Unmarshal([]byte(raw), &request))
			before, err := common.Marshal(request)
			require.NoError(t, err)
			info := &relaycommon.RelayInfo{
				RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatClaude,
				RequestURLPath: "/v1/messages", OriginModelName: "deepseek-flash",
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelType: constant.ChannelTypeDeepSeek, ChannelBaseUrl: "https://api.deepseek.com",
					UpstreamModelName: "deepseek-flash",
					ParamOverride: map[string]any{"operations": []any{
						map[string]any{"path": "reasoning.effort", "mode": "set", "value": effort},
					}},
				},
			}
			adaptor := &deepSeekResponsesCaptureAdaptor{}
			adaptor.Init(info)
			_, apiErr := textRequestViaResponses(c, info, adaptor, &request)
			require.NotNil(t, apiErr)
			require.Equal(t, types.ErrorCodeDoRequestFailed, apiErr.GetErrorCode())
			require.NotEmpty(t, adaptor.body)
			assert.Equal(t, effort, gjson.GetBytes(adaptor.body, "reasoning.effort").String())
			if effort == "none" {
				assert.Equal(t, "function_call", gjson.GetBytes(adaptor.body, "input.0.type").String())
			} else {
				assert.Equal(t, "reasoning", gjson.GetBytes(adaptor.body, "input.0.type").String())
				assert.Equal(t, " ", gjson.GetBytes(adaptor.body, "input.0.content.0.text").String())
				assert.Equal(t, "function_call", gjson.GetBytes(adaptor.body, "input.1.type").String())
			}
			after, err := common.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, relayconstant.RelayModeChatCompletions, info.RelayMode)
			assert.Equal(t, "/v1/messages", info.RequestURLPath)
		})
	}
}
