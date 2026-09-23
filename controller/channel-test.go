package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/samber/lo"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

type testResult struct {
	context      *gin.Context
	localErr     error
	newAPIError  *types.NewAPIError
	summary      channelTestSummary
	responseTime int64
	// A nil sample means setup failed before an upstream request was attempted.
	sample *perfmetrics.Sample
}

func normalizeChannelTestEndpoint(channel *model.Channel, testModel string, endpointType string) string {
	normalized := strings.TrimSpace(endpointType)
	if normalized != "" {
		return normalized
	}
	if channel != nil && channel.Type == constant.ChannelTypeCodex {
		return string(constant.EndpointTypeOpenAIResponse)
	}
	name := strings.ToLower(testModel)
	switch {
	case strings.Contains(name, "codex"):
		return string(constant.EndpointTypeOpenAIResponse)
	case channel != nil && channel.Type == constant.ChannelTypeVolcEngine && strings.Contains(name, "seedream"):
		return string(constant.EndpointTypeImageGeneration)
	case strings.Contains(name, "rerank"):
		return string(constant.EndpointTypeJinaRerank)
	case strings.Contains(name, "embed"), strings.HasPrefix(name, "m3e"), strings.Contains(name, "bge-"), channel != nil && channel.Type == constant.ChannelTypeMokaAI:
		return string(constant.EndpointTypeEmbeddings)
	default:
		return ""
	}
}

func resolveChannelTestUserID(c *gin.Context) (int, error) {
	if c != nil {
		if userID := c.GetInt("id"); userID > 0 {
			return userID, nil
		}
	}

	var rootUser model.User
	if err := model.DB.Select("id").Where("role = ?", common.RoleRootUser).First(&rootUser).Error; err != nil {
		return 0, fmt.Errorf("failed to resolve channel test user: %w", err)
	}
	if rootUser.Id == 0 {
		return 0, errors.New("failed to resolve channel test user")
	}
	return rootUser.Id, nil
}

func testChannel(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if channel == nil {
		return testResult{localErr: errors.New("channel is required")}
	}
	models := []string{strings.TrimSpace(testModel)}
	if models[0] == "" {
		models = nil
		seen := make(map[string]bool)
		for _, name := range channel.GetModels() {
			name = strings.TrimSpace(name)
			if name != "" && !seen[name] {
				models = append(models, name)
				seen[name] = true
			}
		}
	}
	if len(models) == 0 {
		return testResult{localErr: errors.New("channel has no configured models")}
	}

	result := testResult{}
	var nextProbeAt time.Time
models:
	for _, name := range models {
		for {
			if err := ctx.Err(); err != nil {
				result.localErr = err
				return result
			}
			if interval, scheduled := ctx.Value(channelIdleProbeIntervalKey{}).(time.Duration); scheduled {
				now := time.Now()
				activity, err := model.GetChannelModelActivity(channel.Id, name)
				if err != nil {
					result.localErr = fmt.Errorf("failed to read channel activity: %w", err)
					result.summary.Skipped++
					continue models
				}
				if !channelModelProbeDue(activity, channel.TestTime, now, interval) {
					result.summary.Skipped++
					// Cached outcomes only assist a new probe's model-wide result.
					// Read them when the round ends, so a request completing while
					// another channel is tested cannot leave a stale snapshot here.
					if round, ok := ctx.Value(channelAvailabilityRoundKey{}).(*channelAvailabilityRound); ok {
						round.mu.Lock()
						round.skipped[channelModelProbe{channel.Id, name}] = true
						round.mu.Unlock()
					}
					continue models
				}
			}
			// Skipped models consume no upstream rate limit. If a real call
			// arrives during this wait, recheck its deadline before probing.
			delay := time.Until(nextProbeAt)
			if delay <= 0 {
				break
			}
			select {
			case <-ctx.Done():
				result.localErr = ctx.Err()
				return result
			case <-time.After(delay):
			}
		}

		started := time.Now()
		modelResult := testChannelModel(ctx, channel, testUserID, name, endpointType, isStream)
		nextProbeAt = time.Now().Add(common.RequestInterval)
		if err := ctx.Err(); err != nil {
			result.localErr = err
			return result
		}
		result.responseTime += time.Since(started).Milliseconds()
		result.summary.Tested++
		if modelResult.localErr == nil && modelResult.newAPIError == nil {
			result.summary.Succeeded++
		} else {
			result.summary.Failed++
		}
		// Keep the first failure for the manual test response while still probing
		// every configured model and recording each model's own monitoring sample.
		if result.localErr == nil && result.newAPIError == nil {
			result.context = modelResult.context
			result.localErr = modelResult.localErr
			result.newAPIError = modelResult.newAPIError
			if result.localErr != nil && len(models) > 1 {
				result.localErr = fmt.Errorf("model %s: %w", name, result.localErr)
			}
		}
	}
	if result.summary.Tested > 0 {
		result.responseTime /= int64(result.summary.Tested)
	}
	return result
}

func testChannelModel(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult {
	if err := model.RecordChannelModelProbe(channel.Id, testModel, time.Now().UnixMilli(), nil); err != nil {
		common.SysError("failed to record channel check time: " + err.Error())
	}
	type testKey struct {
		value string
		index int
	}
	// Manual checks can receive a cached channel whose key states are shared
	// with routing. Take the snapshot under the same lock as status updates,
	// then release it before making any upstream requests.
	lock := model.GetChannelPollingLock(channel.Id)
	lock.Lock()
	snapshot := *channel
	keys := []testKey{{value: channel.Key, index: -1}}
	if channel.ChannelInfo.IsMultiKey {
		keys = nil
		for index, key := range channel.GetKeys() {
			status, configured := channel.ChannelInfo.MultiKeyStatusList[index]
			if !configured || status == common.ChannelStatusEnabled {
				keys = append(keys, testKey{value: key, index: index})
			}
		}
	}
	snapshot.ChannelInfo = model.ChannelInfo{}
	snapshot.Keys = nil
	lock.Unlock()
	if len(keys) == 0 {
		err := types.NewError(errors.New("no enabled keys"), types.ErrorCodeChannelNoAvailableKey)
		return testResult{localErr: err, newAPIError: err}
	}
	var result testResult
	var successfulResult *testResult
	var hasNeutralResult bool
	var latencyMs int64
	var successfulLatencyMs int64
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return testResult{localErr: err}
		}
		// Pin only this test attempt to a key. Calling the routing selector on
		// the original multi-key channel would advance its persistent cursor.
		probe := snapshot
		probe.Key, probe.Keys = key.value, nil
		probe.ChannelInfo.IsMultiKey = false
		result = testChannelModelAttempt(ctx, &probe, testUserID, testModel, endpointType, isStream, key.index)
		if err := ctx.Err(); err != nil {
			return testResult{localErr: err}
		}
		if result.sample == nil {
			// A local setup failure leaves this model's check incomplete. It
			// cannot turn earlier failed keys into a completed failed round.
			return result
		}
		latencyMs += result.sample.LatencyMs
		if perfmetrics.IsContentModerationError(result.newAPIError) {
			// A refused prompt says nothing about this key's availability. Keep
			// its prior health and continue checking the remaining credentials.
			hasNeutralResult = true
			continue
		}
		statusCode, errorMessage := http.StatusOK, ""
		if result.newAPIError != nil {
			statusCode, errorMessage = result.newAPIError.StatusCode, result.newAPIError.MaskSensitiveError()
		} else if result.localErr != nil {
			statusCode, errorMessage = 0, result.localErr.Error()
		}
		if err := model.RecordChannelKeyObservation(&snapshot, testModel, key.value, time.Now().UnixMilli(), &result.sample.Success, statusCode, errorMessage, perfmetrics.SourceProbe); err != nil {
			common.SysError(fmt.Sprintf("channel key observation persistence failed: channel_id=%d model=%q", channel.Id, testModel))
		}
		if result.sample.Success {
			if successfulResult == nil {
				observed := result
				successfulResult = &observed
				successfulLatencyMs = latencyMs
			}
			continue
		}
		if key.index >= 0 {
			common.SysLog(fmt.Sprintf("channel test key failed: channel_id=%d key_index=%d model=%s", channel.Id, key.index, testModel))
		}
	}
	if successfulResult != nil {
		result = *successfulResult
		latencyMs = successfulLatencyMs
	} else if hasNeutralResult {
		// Without a success, every key needs a real failure before this round
		// can be declared unavailable. Preserve the test error for diagnostics.
		result.sample = nil
		return result
	}
	// Availability is one result for this channel/model, even if several
	// credentials were checked. A later failed key cannot replace a success.
	sample := *result.sample
	if sample.HasTtft {
		sample.TtftMs += latencyMs - sample.LatencyMs
	}
	sample.LatencyMs = latencyMs
	completedAt := time.Now().UnixMilli()
	if err := model.RecordChannelModelProbe(channel.Id, testModel, completedAt, &sample.Success); err != nil {
		common.SysError("failed to record channel check result: " + err.Error())
	}
	perfmetrics.Record(sample)
	sample.Group = ""
	perfmetrics.RecordChannelSample(sample, perfmetrics.SourceProbe)
	if round, ok := ctx.Value(channelAvailabilityRoundKey{}).(*channelAvailabilityRound); ok {
		round.mu.Lock()
		round.probes[channelModelProbe{channel.Id, sample.Model}] = channelProbeObservation{success: sample.Success, completedAt: completedAt}
		round.mu.Unlock()
	} else {
		// Direct manual checks must update the same channel availability
		// series as scheduled assessments, even within an existing hour.
		perfmetrics.RecordChannelSample(sample, perfmetrics.SourceRouteState)
	}
	return result
}

func testChannelModelAttempt(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool, keyIndex int) (result testResult) {
	tik := time.Now()
	var unsupportedTestChannelTypes = []int{
		constant.ChannelTypeMidjourney,
		constant.ChannelTypeMidjourneyPlus,
		constant.ChannelTypeSunoAPI,
		constant.ChannelTypeKling,
		constant.ChannelTypeJimeng,
		constant.ChannelTypeDoubaoVideo,
		constant.ChannelTypeVidu,
		constant.ChannelTypeTaskPlugin,
	}
	if lo.Contains(unsupportedTestChannelTypes, channel.Type) {
		channelTypeName := constant.GetChannelTypeName(channel.Type)
		return testResult{
			localErr: fmt.Errorf("%s channel test is not supported", channelTypeName),
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	endpointType = normalizeChannelTestEndpoint(channel, testModel, endpointType)

	requestPath := "/v1/chat/completions"

	// 如果指定了端点类型，使用指定的端点类型
	if endpointType != "" {
		if endpointInfo, ok := common.GetDefaultEndpointInfo(constant.EndpointType(endpointType)); ok {
			requestPath = endpointInfo.Path
		}
	}
	// Gemini 原生流式通过 URL action（:streamGenerateContent）表达而非请求体字段，
	// GeminiChatRequest.IsStream 依据请求 URL 判定，合成请求路径需与生产入口保持一致
	if isStream && constant.EndpointType(endpointType) == constant.EndpointTypeGemini {
		requestPath = strings.Replace(requestPath, ":generateContent", ":streamGenerateContent", 1)
	}
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, requestPath, nil)

	cache, err := model.GetUserCache(testUserID)
	if err != nil {
		return testResult{
			localErr:    err,
			newAPIError: nil,
		}
	}
	cache.WriteContext(c)
	c.Set("id", testUserID)

	//c.Request.Header.Set("Authorization", "Bearer "+channel.Key)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("channel", channel.Type)
	c.Set("base_url", channel.GetBaseURL())
	group, _ := model.GetUserGroup(testUserID, false)
	c.Set("group", group)

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, testModel, true)
	if newAPIError != nil {
		return testResult{
			context:     c,
			localErr:    newAPIError,
			newAPIError: newAPIError,
		}
	}
	if keyIndex >= 0 {
		// The private copy uses single-key selection, while diagnostics and
		// consume logs retain the key's original position in the saved channel.
		common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, true)
		common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, keyIndex)
	}

	// Determine relay format based on endpoint type or request path
	var relayFormat types.RelayFormat
	if endpointType != "" {
		// 根据指定的端点类型设置 relayFormat
		switch constant.EndpointType(endpointType) {
		case constant.EndpointTypeOpenAI:
			relayFormat = types.RelayFormatOpenAI
		case constant.EndpointTypeOpenAIResponse:
			relayFormat = types.RelayFormatOpenAIResponses
		case constant.EndpointTypeOpenAIResponseCompact:
			relayFormat = types.RelayFormatOpenAIResponsesCompaction
		case constant.EndpointTypeAnthropic:
			relayFormat = types.RelayFormatClaude
		case constant.EndpointTypeGemini:
			relayFormat = types.RelayFormatGemini
		case constant.EndpointTypeJinaRerank:
			relayFormat = types.RelayFormatRerank
		case constant.EndpointTypeImageGeneration:
			relayFormat = types.RelayFormatOpenAIImage
		case constant.EndpointTypeEmbeddings:
			relayFormat = types.RelayFormatEmbedding
		default:
			relayFormat = types.RelayFormatOpenAI
		}
	} else {
		// 根据请求路径自动检测
		relayFormat = types.RelayFormatOpenAI
		if c.Request.URL.Path == "/v1/embeddings" {
			relayFormat = types.RelayFormatEmbedding
		}
		if c.Request.URL.Path == "/v1/images/generations" {
			relayFormat = types.RelayFormatOpenAIImage
		}
		if c.Request.URL.Path == "/v1/messages" {
			relayFormat = types.RelayFormatClaude
		}
		if strings.Contains(c.Request.URL.Path, "/v1beta/models") {
			relayFormat = types.RelayFormatGemini
		}
		if c.Request.URL.Path == "/v1/rerank" || c.Request.URL.Path == "/rerank" {
			relayFormat = types.RelayFormatRerank
		}
		if c.Request.URL.Path == "/v1/responses" {
			relayFormat = types.RelayFormatOpenAIResponses
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v1/responses/compact") {
			relayFormat = types.RelayFormatOpenAIResponsesCompaction
		}
	}

	request := buildTestRequest(testModel, endpointType, channel, isStream)

	info, err := relaycommon.GenRelayInfo(c, relayFormat, request, nil)

	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeGenRelayInfoFailed),
		}
	}

	info.IsChannelTest = true
	info.InitChannelMeta(c)

	err = attachTestBillingRequestInput(info, request)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeJsonMarshalFailed),
		}
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeChannelModelMappedError),
		}
	}
	if err := helper.ApplyReasoningModelSuffix(c, info, request); err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry()),
		}
	}

	testModel = info.UpstreamModelName
	// 更新请求中的模型名称
	request.SetModelName(testModel)

	apiType, _ := common.ChannelType2APIType(channel.Type)
	if info.RelayMode == relayconstant.RelayModeResponsesCompact &&
		!common.SupportsResponsesCompact(channel.Type, apiType) {
		return testResult{
			context:     c,
			localErr:    fmt.Errorf("responses compaction test is not supported for api type %d", apiType),
			newAPIError: types.NewError(fmt.Errorf("unsupported api type: %d", apiType), types.ErrorCodeInvalidApiType),
		}
	}
	adaptor := relay.GetAdaptor(apiType)
	if adaptor == nil {
		return testResult{
			context:     c,
			localErr:    fmt.Errorf("invalid api type: %d, adaptor is nil", apiType),
			newAPIError: types.NewError(fmt.Errorf("invalid api type: %d, adaptor is nil", apiType), types.ErrorCodeInvalidApiType),
		}
	}

	//// 创建一个用于日志的 info 副本，移除 ApiKey
	//logInfo := info
	//logInfo.ApiKey = ""
	common.SysLog(fmt.Sprintf("testing channel %d with model %s , info %+v ", channel.Id, testModel, info.ToString()))

	priceData, err := helper.ModelPriceHelper(c, info, 0, request.GetTokenCountMeta())
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest)),
		}
	}

	adaptor.Init(info)

	var convertedRequest any
	// 根据 RelayMode 选择正确的转换函数
	switch info.RelayMode {
	case relayconstant.RelayModeEmbeddings:
		// Embedding 请求 - request 已经是正确的类型
		if embeddingReq, ok := request.(*dto.EmbeddingRequest); ok {
			convertedRequest, err = adaptor.ConvertEmbeddingRequest(c, info, *embeddingReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid embedding request type"),
				newAPIError: types.NewError(errors.New("invalid embedding request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeImagesGenerations:
		// 图像生成请求 - request 已经是正确的类型
		if imageReq, ok := request.(*dto.ImageRequest); ok {
			convertedRequest, err = adaptor.ConvertImageRequest(c, info, *imageReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid image request type"),
				newAPIError: types.NewError(errors.New("invalid image request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeRerank:
		// Rerank 请求 - request 已经是正确的类型
		if rerankReq, ok := request.(*dto.RerankRequest); ok {
			convertedRequest, err = adaptor.ConvertRerankRequest(c, info.RelayMode, *rerankReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid rerank request type"),
				newAPIError: types.NewError(errors.New("invalid rerank request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeResponses:
		// Response 请求 - request 已经是正确的类型
		if responseReq, ok := request.(*dto.OpenAIResponsesRequest); ok {
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, *responseReq)
		} else {
			return testResult{
				context:     c,
				localErr:    errors.New("invalid response request type"),
				newAPIError: types.NewError(errors.New("invalid response request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	case relayconstant.RelayModeResponsesCompact:
		// Response compaction request - convert to OpenAIResponsesRequest before adapting
		switch req := request.(type) {
		case *dto.OpenAIResponsesCompactionRequest:
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
				Model:              req.Model,
				Input:              req.Input,
				Instructions:       req.Instructions,
				PreviousResponseID: req.PreviousResponseID,
			})
		case *dto.OpenAIResponsesRequest:
			convertedRequest, err = adaptor.ConvertOpenAIResponsesRequest(c, info, *req)
		default:
			return testResult{
				context:     c,
				localErr:    errors.New("invalid response compaction request type"),
				newAPIError: types.NewError(errors.New("invalid response compaction request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	default:
		switch req := request.(type) {
		case *dto.GeneralOpenAIRequest:
			convertedRequest, err = adaptor.ConvertOpenAIRequest(c, info, req)
		case *dto.ClaudeRequest:
			convertedRequest, err = adaptor.ConvertClaudeRequest(c, info, req)
		case *dto.GeminiChatRequest:
			convertedRequest, err = adaptor.ConvertGeminiRequest(c, info, req)
		default:
			return testResult{
				context:     c,
				localErr:    errors.New("invalid chat request type"),
				newAPIError: types.NewError(errors.New("invalid chat request type"), types.ErrorCodeConvertRequestFailed),
			}
		}
	}

	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed),
		}
	}
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewError(err, types.ErrorCodeJsonMarshalFailed),
		}
	}

	//jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings)
	//if err != nil {
	//	return testResult{
	//		context:     c,
	//		localErr:    err,
	//		newAPIError: types.NewError(err, types.ErrorCodeConvertRequestFailed),
	//	}
	//}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			if fixedErr, ok := relaycommon.AsParamOverrideReturnError(err); ok {
				return testResult{
					context:     c,
					localErr:    fixedErr,
					newAPIError: relaycommon.NewAPIErrorFromParamOverride(fixedErr),
				}
			}
			return testResult{
				context:     c,
				localErr:    err,
				newAPIError: types.NewError(err, types.ErrorCodeChannelParamOverrideInvalid),
			}
		}
	}

	requestBody := bytes.NewBuffer(jsonData)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(jsonData))
	// Return an observation only for an attempted upstream request. The caller
	// combines key attempts before publishing a single channel/model outcome.
	testSucceeded := false
	var outputTokens int64
	defer func() {
		if ctx.Err() != nil {
			return
		}
		sample := perfmetrics.RelaySample(info, testSucceeded, outputTokens)
		result.sample = &sample
	}()
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError),
		}
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		if httpResp.StatusCode != http.StatusOK {
			err := service.RelayErrorHandler(c.Request.Context(), httpResp, true)
			common.SysError(fmt.Sprintf(
				"channel test bad response: channel_id=%d name=%s type=%d model=%s endpoint_type=%s status=%d err=%v",
				channel.Id,
				channel.Name,
				channel.Type,
				testModel,
				endpointType,
				httpResp.StatusCode,
				model.SanitizeChannelObservationError(channel, channel.Key, err.Error()),
			))
			return testResult{
				context:     c,
				localErr:    err,
				newAPIError: types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError),
			}
		}
	}
	usageA, respErr := adaptor.DoResponse(c, httpResp, info)
	if respErr != nil {
		return testResult{
			context:     c,
			localErr:    respErr,
			newAPIError: respErr,
		}
	}
	usage, usageErr := coerceTestUsage(usageA, isStream, info.GetEstimatePromptTokens())
	if usageErr != nil {
		return testResult{
			context:     c,
			localErr:    usageErr,
			newAPIError: types.NewOpenAIError(usageErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError),
		}
	}
	response := w.Result()
	respBody, err := readTestResponseBody(response.Body, isStream)
	if err != nil {
		return testResult{
			context:     c,
			localErr:    err,
			newAPIError: types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError),
		}
	}
	if bodyErr := validateTestResponseBody(respBody, isStream); bodyErr != nil {
		return testResult{
			context:     c,
			localErr:    bodyErr,
			newAPIError: types.NewOpenAIError(bodyErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError),
		}
	}
	info.SetEstimatePromptTokens(usage.PromptTokens)
	testSucceeded = true
	outputTokens = int64(usage.CompletionTokens)

	quota, tieredResult := settleTestQuota(info, priceData, usage)
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	consumedTime := float64(milliseconds) / 1000.0
	other := buildTestLogOther(c, info, priceData, usage, tieredResult)
	model.RecordConsumeLog(c, testUserID, model.RecordConsumeLogParams{
		ChannelId:        channel.Id,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ModelName:        info.OriginModelName,
		TokenName:        "模型测试",
		IsChannelTest:    true,
		Quota:            quota,
		Content:          "模型测试",
		UseTimeSeconds:   int(consumedTime),
		IsStream:         info.IsStream,
		Group:            info.UsingGroup,
		Other:            other,
	})
	common.SysLog(fmt.Sprintf("testing channel #%d, response: \n%s", channel.Id, model.SanitizeChannelObservationError(channel, channel.Key, string(respBody))))
	return testResult{
		context:     c,
		localErr:    nil,
		newAPIError: nil,
	}
}

func attachTestBillingRequestInput(info *relaycommon.RelayInfo, request dto.Request) error {
	if info == nil {
		return nil
	}

	input, err := helper.BuildBillingExprRequestInputFromRequest(request, info.RequestHeaders)
	if err != nil {
		return err
	}
	info.BillingRequestInput = &input
	return nil
}

func settleTestQuota(info *relaycommon.RelayInfo, priceData hosttypes.PriceData, usage *dto.Usage) (int, *billingexpr.TieredResult) {
	if usage != nil && info != nil && info.TieredBillingSnapshot != nil {
		isClaudeUsageSemantic := usage.UsageSemantic == "anthropic" || info.GetFinalRequestRelayFormat() == types.RelayFormatClaude
		usedVars := billingexpr.UsedVars(info.TieredBillingSnapshot.ExprString)
		if ok, quota, result := service.TryTieredSettle(info, service.BuildTieredTokenParams(usage, isClaudeUsageSemantic, usedVars)); ok {
			return quota, result
		}
	}

	quota := 0
	if !priceData.UsePrice {
		completionQuota := common.QuotaRound(float64(usage.CompletionTokens) * priceData.CompletionRatio)
		quota = common.QuotaRound(float64(usage.PromptTokens) + float64(completionQuota))
		quota = common.QuotaRound(float64(quota) * priceData.ModelRatio)
		if priceData.ModelRatio != 0 && quota <= 0 {
			quota = 1
		}
		return quota, nil
	}

	return common.QuotaFromFloat(priceData.ModelPrice * common.QuotaPerUnit), nil
}

func buildTestLogOther(c *gin.Context, info *relaycommon.RelayInfo, priceData hosttypes.PriceData, usage *dto.Usage, tieredResult *billingexpr.TieredResult) *model.LogOther {
	other := service.GenerateTextOtherInfo(c, info, priceData.ModelRatio, priceData.GroupRatioInfo.GroupRatio, priceData.CompletionRatio,
		usage.PromptTokensDetails.CachedTokens, priceData.CacheRatio, priceData.ModelPrice, priceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		service.InjectTieredBillingInfo(other, info, tieredResult)
	}
	return other
}

func coerceTestUsage(usageAny any, isStream bool, estimatePromptTokens int) (*dto.Usage, error) {
	switch u := usageAny.(type) {
	case *dto.Usage:
		return u, nil
	case dto.Usage:
		return &u, nil
	case nil:
		if !isStream {
			return nil, errors.New("usage is nil")
		}
		usage := &dto.Usage{
			PromptTokens: estimatePromptTokens,
		}
		usage.TotalTokens = usage.PromptTokens
		return usage, nil
	default:
		if !isStream {
			return nil, fmt.Errorf("invalid usage type: %T", usageAny)
		}
		usage := &dto.Usage{
			PromptTokens: estimatePromptTokens,
		}
		usage.TotalTokens = usage.PromptTokens
		return usage, nil
	}
}

func readTestResponseBody(body io.ReadCloser, isStream bool) ([]byte, error) {
	defer func() { _ = body.Close() }()
	const maxStreamLogBytes = 8 << 10
	if isStream {
		return io.ReadAll(io.LimitReader(body, maxStreamLogBytes))
	}
	return io.ReadAll(body)
}

func detectErrorFromTestResponseBody(respBody []byte) error {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return nil
	}
	if message := detectErrorMessageFromJSONBytes(b); message != "" {
		return fmt.Errorf("upstream error: %s", message)
	}

	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if message := detectErrorMessageFromJSONBytes(payload); message != "" {
			return fmt.Errorf("upstream error: %s", message)
		}
	}

	return nil
}

func validateStreamTestResponseBody(respBody []byte) error {
	b := bytes.TrimSpace(respBody)
	if len(b) == 0 {
		return errors.New("stream response body is empty")
	}

	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		return nil
	}

	return errors.New("stream response body does not contain a valid stream event")
}

func validateTestResponseBody(respBody []byte, isStream bool) error {
	if bodyErr := detectErrorFromTestResponseBody(respBody); bodyErr != nil {
		return bodyErr
	}
	if isStream {
		return validateStreamTestResponseBody(respBody)
	}
	return nil
}

func shouldUseStreamForAutomaticChannelTest(channel *model.Channel) bool {
	return channel != nil && channel.Type == constant.ChannelTypeCodex
}

func detectErrorMessageFromJSONBytes(jsonBytes []byte) string {
	if len(jsonBytes) == 0 {
		return ""
	}
	if jsonBytes[0] != '{' && jsonBytes[0] != '[' {
		return ""
	}
	errVal := gjson.GetBytes(jsonBytes, "error")
	if !errVal.Exists() || errVal.Type == gjson.Null {
		return ""
	}

	message := gjson.GetBytes(jsonBytes, "error.message").String()
	if message == "" {
		message = gjson.GetBytes(jsonBytes, "error.error.message").String()
	}
	if message == "" && errVal.Type == gjson.String {
		message = errVal.String()
	}
	if message == "" {
		message = errVal.Raw
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "upstream returned error payload"
	}
	return message
}

func buildTestRequest(model string, endpointType string, channel *model.Channel, isStream bool) dto.Request {
	testResponsesInput := json.RawMessage(`[{"role":"user","content":"hi"}]`)
	endpointType = normalizeChannelTestEndpoint(channel, model, endpointType)

	// 根据端点类型构建不同的测试请求
	if endpointType != "" {
		switch constant.EndpointType(endpointType) {
		case constant.EndpointTypeEmbeddings:
			// 返回 EmbeddingRequest
			return &dto.EmbeddingRequest{
				Model: model,
				Input: []any{"hello world"},
			}
		case constant.EndpointTypeImageGeneration:
			// 返回 ImageRequest
			return &dto.ImageRequest{
				Model:  model,
				Prompt: "a cute cat",
				N:      lo.ToPtr(uint(1)),
				Size:   "1024x1024",
			}
		case constant.EndpointTypeJinaRerank:
			// 返回 RerankRequest
			return &dto.RerankRequest{
				Model:     model,
				Query:     "What is Deep Learning?",
				Documents: []any{"Deep Learning is a subset of machine learning.", "Machine learning is a field of artificial intelligence."},
				TopN:      lo.ToPtr(2),
			}
		case constant.EndpointTypeOpenAIResponse:
			// 返回 OpenAIResponsesRequest
			return &dto.OpenAIResponsesRequest{
				Model:  model,
				Input:  json.RawMessage(`[{"role":"user","content":"hi"}]`),
				Stream: lo.ToPtr(isStream),
			}
		case constant.EndpointTypeOpenAIResponseCompact:
			// 返回 OpenAIResponsesCompactionRequest
			return &dto.OpenAIResponsesCompactionRequest{
				Model: model,
				Input: testResponsesInput,
			}
		case constant.EndpointTypeAnthropic:
			return &dto.ClaudeRequest{
				Model:     model,
				Stream:    lo.ToPtr(isStream),
				MaxTokens: lo.ToPtr(uint(16)),
				Messages: []dto.ClaudeMessage{
					{
						Role:    "user",
						Content: "hi",
					},
				},
			}
		case constant.EndpointTypeGemini:
			return &dto.GeminiChatRequest{
				Contents: []dto.GeminiChatContent{
					{
						Role:  "user",
						Parts: []dto.GeminiPart{{Text: "hi"}},
					},
				},
				GenerationConfig: dto.GeminiChatGenerationConfig{
					MaxOutputTokens: lo.ToPtr(uint(3000)),
				},
			}
		case constant.EndpointTypeOpenAI:
			req := &dto.GeneralOpenAIRequest{
				Model:  model,
				Stream: lo.ToPtr(isStream),
				Messages: []dto.Message{
					{
						Role:    "user",
						Content: "hi",
					},
				},
				MaxTokens: lo.ToPtr(uint(16)),
			}
			if isStream {
				req.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
			}
			return req
		}
	}

	// Chat/Completion 请求 - 返回 GeneralOpenAIRequest
	testRequest := &dto.GeneralOpenAIRequest{
		Model:  model,
		Stream: lo.ToPtr(isStream),
		Messages: []dto.Message{
			{
				Role:    "user",
				Content: "hi",
			},
		},
	}
	if isStream {
		testRequest.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
	}

	if dto.IsOpenAIReasoningOModel(model) {
		testRequest.MaxCompletionTokens = lo.ToPtr(uint(16))
	} else if strings.Contains(model, "thinking") {
		if !strings.Contains(model, "claude") {
			testRequest.MaxTokens = lo.ToPtr(uint(50))
		}
	} else if strings.Contains(model, "gemini") {
		testRequest.MaxTokens = lo.ToPtr(uint(3000))
	} else {
		testRequest.MaxTokens = lo.ToPtr(uint(16))
	}

	return testRequest
}

func TestChannel(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	channel, err := model.CacheGetChannel(channelId)
	if err != nil {
		channel, err = model.GetChannelById(channelId, true)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}
	//defer func() {
	//	if channel.ChannelInfo.IsMultiKey {
	//		go func() { _ = channel.SaveChannelInfo() }()
	//	}
	//}()
	testModel := c.Query("model")
	endpointType := c.Query("endpoint_type")
	isStream, _ := strconv.ParseBool(c.Query("stream"))
	testUserID, err := resolveChannelTestUserID(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	tik := time.Now()
	requestCtx := context.Background()
	if c.Request != nil {
		requestCtx = c.Request.Context()
	}
	result := testChannel(requestCtx, channel, testUserID, testModel, endpointType, isStream)
	if err := perfmetrics.Flush(); err != nil {
		common.SysError("failed to persist manual channel monitoring: " + err.Error())
	}
	if result.localErr != nil {
		resp := gin.H{
			"success": false,
			"message": result.localErr.Error(),
			"time":    0.0,
		}
		if result.newAPIError != nil {
			resp["error_code"] = result.newAPIError.GetErrorCode()
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	tok := time.Now()
	milliseconds := tok.Sub(tik).Milliseconds()
	go channel.UpdateResponseTime(result.responseTime)
	consumedTime := float64(milliseconds) / 1000.0
	if result.newAPIError != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"message":    result.newAPIError.Error(),
			"time":       consumedTime,
			"error_code": result.newAPIError.GetErrorCode(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"time":    consumedTime,
		"data": gin.H{
			"response_time": result.responseTime,
			"tested":        result.summary.Tested,
			"succeeded":     result.summary.Succeeded,
			"failed":        result.summary.Failed,
		},
	})
}

// channelTestSummary counts model checks in a monitoring cycle.
type channelTestSummary struct {
	Tested    int `json:"tested"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped,omitempty"`
}

type channelAvailabilityRoundKey struct{}
type channelIdleProbeIntervalKey struct{}

func channelModelProbeDue(activity model.ChannelModelActivity, previousChannelTest int64, now time.Time, interval time.Duration) bool {
	lastObserved := max(activity.LastRequestAt, activity.LastProbeAt)
	if lastObserved == 0 {
		// Preserve the previous release's channel-level deadline until this
		// particular model has its own durable activity state.
		lastObserved = previousChannelTest * 1000
	}
	return now.UnixMilli() >= activity.RequestActiveUntil && now.UnixMilli()-lastObserved >= interval.Milliseconds()
}

type channelModelProbe struct {
	channelID int
	model     string
}

type channelAvailabilityRound struct {
	mu      sync.Mutex
	probes  map[channelModelProbe]channelProbeObservation
	skipped map[channelModelProbe]bool
}

type channelProbeObservation struct {
	success     bool
	completedAt int64
}

func testChannelForHealthCheck(ctx context.Context, channel *model.Channel, testUserID int) channelTestSummary {
	result := testChannel(ctx, channel, testUserID, "", "", shouldUseStreamForAutomaticChannelTest(channel))
	if result.summary.Tested > 0 && (ctx == nil || ctx.Err() == nil) {
		channel.UpdateResponseTime(result.responseTime)
	}
	return result.summary
}

// runChannelTestWorkers executes independent channel tests with bounded
// concurrency. Results and progress are reduced by the caller goroutine, so
// summary counts and the progress reporter remain serialized.
func runChannelTestWorkers(
	ctx context.Context,
	channels []*model.Channel,
	concurrency int,
	run func(context.Context, *model.Channel) channelTestSummary,
	report func(processed, total int),
) channelTestSummary {
	if ctx == nil {
		ctx = context.Background()
	}
	total := len(channels)
	if report != nil {
		report(0, total)
	}
	if total == 0 {
		return channelTestSummary{}
	}

	workerCount := min(operation_setting.NormalizeChannelTestConcurrency(concurrency), total)
	jobs := make(chan *model.Channel)
	results := make(chan channelTestSummary)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case channel, ok := <-jobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}

					result := channelTestSummary{}
					if channel != nil && channel.Status != common.ChannelStatusManuallyDisabled {
						result = run(ctx, channel)
					}

					results <- result

					if result.Tested > 0 && common.RequestInterval > 0 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(common.RequestInterval):
						}
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, channel := range channels {
			select {
			case <-ctx.Done():
				return
			case jobs <- channel:
			}
		}
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	summary := channelTestSummary{}
	processed := 0
	for result := range results {
		summary.Tested += result.Tested
		summary.Succeeded += result.Succeeded
		summary.Failed += result.Failed
		summary.Skipped += result.Skipped
		processed++
		if report != nil && ctx.Err() == nil {
			report(processed, total)
		}
	}
	return summary
}

// performChannelTests runs channel health checks with the configured bounded
// concurrency and honors cancellation when a system-task runner loses its
// lease.
func performChannelTests(ctx context.Context, channels []*model.Channel, testUserID int, concurrency int, report func(processed, total int)) channelTestSummary {
	if ctx == nil {
		ctx = context.Background()
	}
	round := &channelAvailabilityRound{
		probes:  make(map[channelModelProbe]channelProbeObservation),
		skipped: make(map[channelModelProbe]bool),
	}
	ctx = context.WithValue(ctx, channelAvailabilityRoundKey{}, round)
	summary := runChannelTestWorkers(
		ctx,
		channels,
		concurrency,
		func(ctx context.Context, channel *model.Channel) channelTestSummary {
			return testChannelForHealthCheck(ctx, channel, testUserID)
		},
		report,
	)
	if ctx.Err() != nil || len(round.probes) == 0 {
		return summary
	}
	// The end-of-round configuration determines which routes can currently
	// serve traffic. A disabled route's successful probe is not usable failover.
	var current []*model.Channel
	if err := model.DB.Select("id", "models", "group", "status").Where("status = ?", common.ChannelStatusEnabled).Find(&current).Error; err != nil {
		common.SysError("failed to resolve model availability routes: " + err.Error())
		return summary
	}
	if ctx.Err() != nil {
		return summary
	}
	channelIDs := make([]int, 0, len(current))
	for _, channel := range current {
		channelIDs = append(channelIDs, channel.Id)
	}
	activities, err := model.GetChannelModelActivities(channelIDs)
	if err != nil {
		common.SysError("failed to refresh model availability observations: " + err.Error())
		return summary
	}
	if ctx.Err() != nil {
		return summary
	}
	latest := make(map[channelModelProbe]model.ChannelModelActivity, len(activities))
	for _, activity := range activities {
		latest[channelModelProbe{activity.ChannelID, activity.ModelName}] = activity
	}
	interval, scheduled := ctx.Value(channelIdleProbeIntervalKey{}).(time.Duration)
	if !scheduled {
		interval = operation_setting.ChannelTestInterval()
	}
	now := time.Now().UnixMilli()
	type availabilityKey struct{ model, group string }
	type availabilityOutcome struct{ complete, success, probed bool }
	outcomes := make(map[availabilityKey]availabilityOutcome)
	activeGroups := ratio_setting.GetGroupRatioCopy()
	for _, channel := range current {
		groups := make(map[string]bool)
		for _, group := range channel.GetGroups() {
			if _, active := activeGroups[group]; active || group == "auto" {
				groups[group] = true
			}
		}
		if len(groups) == 0 {
			continue
		}
		groups[""] = true // The model-wide outcome spans each route exactly once.
		seenModels := make(map[string]bool)
		for _, name := range channel.GetModels() {
			name = strings.TrimSpace(name)
			if name == "" || seenModels[name] {
				continue
			}
			seenModels[name] = true
			pair := channelModelProbe{channel.Id, name}
			probe, probed := round.probes[pair]
			success, observed := probe.success, probed
			activity := latest[pair]
			if (probed || round.skipped[pair]) && activity.LastResultAt > 0 && activity.LastResultAt <= now && now-activity.LastResultAt <= interval.Milliseconds() && activity.LastResultAt >= probe.completedAt {
				success, observed = activity.LastResultSuccess, true
			}
			if probed {
				perfmetrics.RecordChannelSample(perfmetrics.Sample{ChannelID: channel.Id, Model: name, Success: success}, perfmetrics.SourceRouteState)
			}
			for group := range groups {
				key := availabilityKey{name, group}
				outcome, exists := outcomes[key]
				if !exists {
					outcome.complete = true
				}
				outcome.complete = outcome.complete && observed
				outcome.success = outcome.success || success
				outcome.probed = outcome.probed || probed
				outcomes[key] = outcome
			}
		}
	}
	for key, outcome := range outcomes {
		if !outcome.complete || !outcome.probed {
			continue
		}
		source := perfmetrics.SourceAvailability
		if key.group == "" {
			source = perfmetrics.SourceAvailabilityAll
		}
		perfmetrics.RecordChannelSample(perfmetrics.Sample{Model: key.model, Group: key.group, Success: outcome.success}, source)
	}
	if err := perfmetrics.Flush(); err != nil {
		common.SysError("failed to persist channel monitoring round: " + err.Error())
	}
	return summary
}

// runChannelTestTask runs one synchronous channel test cycle for the system task
// runner (both the scheduled job and the manual "test all channels" trigger go
// through here). It honors ctx cancellation so a runner that loses its lease
// stops promptly. The legacy mode argument is accepted for queued tasks but no
// longer restricts monitoring to channels eligible for automatic status changes.
// When notify is set the root user is notified on completion. Cross-instance execution is
// guarded by the system task per-type lock, so no process-local guard is needed.
func runChannelTestTask(ctx context.Context, _ string, notify bool, report func(processed, total int), scheduled ...bool) (channelTestSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(scheduled) > 0 && scheduled[0] {
		ctx = context.WithValue(ctx, channelIdleProbeIntervalKey{}, operation_setting.ChannelTestInterval())
	}
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return channelTestSummary{}, err
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return channelTestSummary{}, err
	}
	selected := selectChannelsForAutomaticTest(channels)
	concurrency := operation_setting.GetMonitorSetting().ChannelTestConcurrency
	summary := performChannelTests(ctx, selected, testUserID, concurrency, report)
	if notify && (ctx == nil || ctx.Err() == nil) {
		service.NotifyRootUser(dto.NotifyTypeChannelTest, "通道测试完成", "所有通道测试已完成")
	}
	return summary, nil
}

func selectChannelsForAutomaticTest(channels []*model.Channel) []*model.Channel {
	selected := make([]*model.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel == nil || channel.Status == common.ChannelStatusManuallyDisabled {
			continue
		}
		selected = append(selected, channel)
	}
	return selected
}

// TestAllChannels enqueues a channel_test system task instead of running the
// test loop inline. If any channel_test task is already active, the manual run is
// rejected so the caller does not mistake a scheduled run for this manual one.
func TestAllChannels(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelTest, channelTestTaskPayload{
		Mode:   operation_setting.ChannelTestModeScheduledAll,
		Notify: true,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "已有通道测试任务正在运行或等待中，不能启动本次手动任务",
			"data": gin.H{
				"task_id": task.TaskID,
				"status":  task.Status,
				"type":    task.Type,
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"task_id": task.TaskID,
			"status":  task.Status,
		},
	})
}
