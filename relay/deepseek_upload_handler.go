package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// DeepSeekUploadHelper preserves multipart bytes and settles successful OCR/ASR
// by measured pages/seconds. A submitted upload is never replayed automatically.
func DeepSeekUploadHelper(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	info.InitChannelMeta(c)
	if info.ChannelType != constant.ChannelTypeDeepSeek {
		return types.NewError(errors.New("channel does not support DeepSeek OCR or ASR"), types.ErrorCodeInvalidRequest)
	}
	request, ok := info.Request.(*dto.DeepSeekUploadRequest)
	if !ok {
		return types.NewError(errors.New("invalid DeepSeek upload request"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if len(info.ParamOverride) > 0 {
		return types.NewErrorWithStatusCode(errors.New("parameter overrides are not supported for DeepSeek multipart tools"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if err := helper.ModelMappedHelper(c, info, request); err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}
	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(errors.New("invalid API type"), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}
	body, err := storage.NewReader()
	if err != nil {
		return types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}
	defer body.Close()
	upstreamURL, err := adaptor.GetRequestURL(info)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	// Recognition continues and bills upstream after the downstream disconnects.
	// Bound the independent operation so its final quantity can still be settled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 200*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, body)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	upstream.ContentLength = storage.Size()
	// Do not attach GetBody: an ambiguous transport failure must not replay a billable upload.
	if err := adaptor.SetupRequestHeader(c, &upstream.Header, info); err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	if err := channel.ApplyHeaderOverride(info, c, upstream); err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	response, err := channel.DoRequest(c, upstream, info)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		apiErr := service.RelayErrorHandler(c.Request.Context(), response, false)
		return types.NewError(apiErr, apiErr.GetErrorCode(), types.ErrOptionWithSkipRetry())
	}
	reader := bufio.NewReader(response.Body)
	if info.RelayMode == relayconstant.RelayModeToolsOCR {
		for range helper.DefaultMaxScannerBufferSize {
			first, err := reader.ReadByte()
			if err != nil {
				return types.NewError(err, types.ErrorCodeReadResponseBodyFailed, types.ErrOptionWithSkipRetry())
			}
			if first != ' ' && first != '\n' && first != '\r' && first != '\t' {
				_ = reader.UnreadByte()
				break
			}
			if c.Request.Context().Err() == nil {
				c.Header("Content-Type", "application/json")
				c.Header("X-Accel-Buffering", "no")
				_, _ = c.Writer.Write([]byte{first})
				_ = helper.FlushWriter(c)
			}
		}
	}
	raw, err := io.ReadAll(io.LimitReader(reader, helper.DefaultMaxScannerBufferSize+1))
	if err != nil {
		return types.NewError(err, types.ErrorCodeReadResponseBodyFailed, types.ErrOptionWithSkipRetry())
	}
	if len(raw) > helper.DefaultMaxScannerBufferSize {
		return types.NewError(errors.New("DeepSeek response is too large"), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	var result struct {
		Object     string              `json:"object"`
		PageCount  *int                `json:"page_count"`
		Pages      []common.RawMessage `json:"pages"`
		DurationMS *int64              `json:"duration_ms"`
		Text       *string             `json:"text"`
		Error      any                 `json:"error"`
	}
	if err := common.Unmarshal(raw, &result); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	if result.Error != nil {
		var failure dto.OpenAITextResponse
		_ = common.Unmarshal(raw, &failure)
		if providerErr := failure.GetOpenAIError(); providerErr != nil {
			return types.NewError(types.WithOpenAIError(*providerErr, http.StatusBadGateway), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		}
		return types.NewError(errors.New("DeepSeek recognition failed"), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	usage := billingexpr.MeteredUsage{Unit: billingexpr.BillingUnitPage}
	if info.RelayMode == relayconstant.RelayModeToolsOCR {
		if result.Object != "ocr.result" || result.PageCount == nil || *result.PageCount < 1 || *result.PageCount > 1000 || len(result.Pages) != *result.PageCount {
			return types.NewError(errors.New("invalid OCR page count"), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		}
		usage.Count = *result.PageCount
	} else {
		if result.Text == nil || result.DurationMS == nil || *result.DurationMS <= 0 || *result.DurationMS > 120000 {
			return types.NewError(errors.New("invalid ASR duration"), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		}
		usage.Unit, usage.Count = billingexpr.BillingUnitSecond, int((*result.DurationMS+999)/1000)
	}
	if err := usage.Validate(); err != nil {
		return types.NewError(fmt.Errorf("invalid provider usage: %w", err), types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}
	info.BillingMeteredUsage = &usage
	service.PostTextConsumeQuota(c, info, &dto.Usage{}, nil)
	if c.Request.Context().Err() == nil {
		service.IOCopyBytesGracefully(c, response, raw)
	}
	return nil
}
