package openai

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	if info.GetChannelType() == constant.ChannelTypeDeepSeek && ojson.GetBytes(responseBody, "status").String() == "failed" {
		// DeepSeek reports generation failure inside HTTP 200. Preserve that
		// Responses envelope, while recording failure and its actual bill.
		// Decode the outcome independently: malformed usage is not an estimate.
		var outcome struct {
			Status common.RawMessage `json:"status"`
			Error  any               `json:"error"`
			Model  string            `json:"model"`
		}
		if err := common.Unmarshal(responseBody, &outcome); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		info.StreamStatus = relaycommon.NewStreamStatus()
		service.ObserveResponsesOutcome(info, &dto.ResponsesStreamResponse{Type: "response.failed", Response: &dto.OpenAIResponsesResponse{
			Status: outcome.Status, Error: outcome.Error,
		}})
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
		info.ObserveResponseModel(outcome.Model)
		info.ApplyVendorToolUsage(responseBody)
		service.IOCopyBytesGracefully(c, resp, responseBody)
		return vendorBilledResponsesUsage(info, responseBody), nil
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.CaptureSub2APIResponseID(c, responsesResponse.ID, false)
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}
	if info.GetChannelType() == constant.ChannelTypeDeepSeek {
		info.StreamStatus = relaycommon.NewStreamStatus()
		service.ObserveResponsesOutcome(info, &dto.ResponsesStreamResponse{Response: &responsesResponse})
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	}

	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)
	var terminalUsage *dto.Usage
	deepSeekTerminal := false

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if info.GetChannelType() == constant.ChannelTypeDeepSeek {
			kind := ojson.Get(data, "type").String()
			if kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" {
				var event struct {
					Response *struct {
						Status            common.RawMessage      `json:"status"`
						Error             any                    `json:"error"`
						Model             string                 `json:"model"`
						IncompleteDetails *dto.IncompleteDetails `json:"incomplete_details"`
					} `json:"response"`
				}
				if err := common.UnmarshalJsonStr(data, &event); err != nil || event.Response == nil {
					sr.Stop(fmt.Errorf("invalid DeepSeek Responses terminal event"))
					return
				}
				terminal := dto.ResponsesStreamResponse{Type: kind, Response: &dto.OpenAIResponsesResponse{
					Status: event.Response.Status, Error: event.Response.Error, Model: event.Response.Model,
					IncompleteDetails: event.Response.IncompleteDetails,
				}}
				accumulator.Observe(&terminal, common.StringToByteSlice(data))
				terminalUsage = vendorBilledResponsesUsage(info, common.StringToByteSlice(data))
				deepSeekTerminal = true
				if err := helper.ResponseChunkData(c, terminal, data); err != nil {
					sr.Stop(err)
					return
				}
				// All three are terminal in the official protocol; a failed or
				// incomplete response must not wait for a nonexistent [DONE].
				sr.Done()
				return
			}
		}

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			sr.Error(err)
			return
		}
		if streamResponse.Response != nil {
			service.CaptureSub2APIResponseID(c, streamResponse.Response.ID, false)
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
		if err := helper.ResponseChunkData(c, streamResponse, data); err != nil {
			sr.Stop(err)
			return
		}
		if (streamResponse.Type == "response.completed" || streamResponse.Type == "response.done") &&
			streamResponse.Response != nil && info.StreamStatus.ResponseOutcome() == string(relaycommon.ResponseOutcomeCompleted) {
			sr.Done()
		}
	})

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	if deepSeekTerminal && (terminalUsage != nil || info.StreamStatus.ResponseFailed()) {
		return terminalUsage, nil
	}
	return accumulator.Finish(), nil
}

// vendorBilledResponsesUsage accepts only actual bounded final usage. Explicit
// zero counts stay zero; a failed response cannot inherit a prompt estimate or
// an earlier event's counters. A valid tool-only bill is still billable.
func vendorBilledResponsesUsage(info *relaycommon.RelayInfo, body []byte) *dto.Usage {
	raw := ojson.GetBytes(body, "usage")
	if !raw.Exists() {
		raw = ojson.GetBytes(body, "response.usage")
	}
	if !raw.IsObject() {
		return nil
	}
	var reported dto.Usage
	if common.UnmarshalJsonStr(raw.Raw, &reported) != nil {
		return nil
	}
	for _, value := range []int{reported.InputTokens, reported.OutputTokens, reported.TotalTokens} {
		if value < 0 || value > common.MaxQuota {
			return nil
		}
	}
	if reported.InputTokens > common.MaxQuota-reported.OutputTokens {
		return nil
	}
	if details := reported.InputTokensDetails; details != nil && (details.CachedTokens < 0 || details.CachedTokens > reported.InputTokens) {
		return nil
	}
	if details := reported.OutputTokensDetails; details != nil && (details.ReasoningTokens < 0 || details.ReasoningTokens > reported.OutputTokens) {
		return nil
	}
	valid := raw.Get("input_tokens").Type == ojson.Number && raw.Get("output_tokens").Type == ojson.Number
	if info.VendorToolUsage != nil {
		for _, count := range info.VendorToolUsage(body) {
			valid = valid || count > 0
		}
	}
	if !valid {
		return nil
	}
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, &reported)
	usage.UsageSource = "upstream"
	return usage
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !ojson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := ojson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}
