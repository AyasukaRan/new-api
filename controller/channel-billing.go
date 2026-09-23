package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/advancedcustom"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"

	"github.com/gin-gonic/gin"
)

// https://github.com/songquanpeng/one-api/issues/79

type OpenAISubscriptionResponse struct {
	Object             string  `json:"object"`
	HasPaymentMethod   bool    `json:"has_payment_method"`
	SoftLimitUSD       float64 `json:"soft_limit_usd"`
	HardLimitUSD       float64 `json:"hard_limit_usd"`
	SystemHardLimitUSD float64 `json:"system_hard_limit_usd"`
	AccessUntil        int64   `json:"access_until"`
}

type OpenAIUsageDailyCost struct {
	Timestamp float64 `json:"timestamp"`
	LineItems []struct {
		Name string  `json:"name"`
		Cost float64 `json:"cost"`
	}
}

type OpenAICreditGrants struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalAvailable float64 `json:"total_available"`
}

const maxAdvancedCustomBalanceResponseBytes = 256 << 10

type channelBalanceResult struct {
	Balance     float64
	RawResponse string
	KeyBalances []model.ChannelKeyBalance
	Partial     bool
	Monitor     *model.ChannelBalanceMonitor
}

type OpenAIUsageResponse struct {
	Object string `json:"object"`
	//DailyCosts []OpenAIUsageDailyCost `json:"daily_costs"`
	TotalUsage float64 `json:"total_usage"` // unit: 0.01 dollar
}

type OpenAISBUsageResponse struct {
	Msg  string `json:"msg"`
	Data *struct {
		Credit string `json:"credit"`
	} `json:"data"`
}

type AIProxyUserOverviewResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	ErrorCode int    `json:"error_code"`
	Data      struct {
		TotalPoints float64 `json:"totalPoints"`
	} `json:"data"`
}

type API2GPTUsageResponse struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalRemaining float64 `json:"total_remaining"`
}

type APGC2DGPTUsageResponse struct {
	//Grants         interface{} `json:"grants"`
	Object         string  `json:"object"`
	TotalAvailable float64 `json:"total_available"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
}

type SiliconFlowUsageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  bool   `json:"status"`
	Data    struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Image         string `json:"image"`
		Email         string `json:"email"`
		IsAdmin       bool   `json:"isAdmin"`
		Balance       string `json:"balance"`
		Status        string `json:"status"`
		Introduction  string `json:"introduction"`
		Role          string `json:"role"`
		ChargeBalance string `json:"chargeBalance"`
		TotalBalance  string `json:"totalBalance"`
		Category      string `json:"category"`
	} `json:"data"`
}

type DeepSeekUsageResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

type OpenRouterCreditResponse struct {
	Data struct {
		TotalCredits float64 `json:"total_credits"`
		TotalUsage   float64 `json:"total_usage"`
	} `json:"data"`
}

// GetAuthHeader get auth header
func GetAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	return h
}

// GetClaudeAuthHeader get claude auth header
func GetClaudeAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("x-api-key", token)
	h.Add("anthropic-version", "2023-06-01")
	return h
}

func GetResponseBody(ctx context.Context, method, url string, channel *model.Channel, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	for k := range headers {
		req.Header.Add(k, headers.Get(k))
	}
	client, err := service.GetHttpClientWithProxy(channel.GetSetting().Proxy)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code: %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAdvancedCustomBalanceResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAdvancedCustomBalanceResponseBytes {
		return nil, errors.New("balance response too large")
	}
	return body, nil
}

func updateChannelCloseAIBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := fmt.Sprintf("%s/dashboard/billing/credit_grants", channel.GetBaseURL())
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := OpenAICreditGrants{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	return response.TotalAvailable, nil
}

func updateChannelOpenAISBBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := fmt.Sprintf("https://api.openai-sb.com/sb-api/user/status?api_key=%s", channel.Key)
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := OpenAISBUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Data == nil {
		return 0, errors.New(response.Msg)
	}
	balance, err := strconv.ParseFloat(response.Data.Credit, 64)
	if err != nil {
		return 0, err
	}
	return balance, nil
}

func updateChannelAIProxyBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://aiproxy.io/api/report/getUserOverview"
	headers := http.Header{}
	headers.Add("Api-Key", channel.Key)
	body, err := GetResponseBody(ctx, "GET", url, channel, headers)
	if err != nil {
		return 0, err
	}
	response := AIProxyUserOverviewResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Success {
		return 0, fmt.Errorf("code: %d, message: %s", response.ErrorCode, response.Message)
	}
	return response.Data.TotalPoints, nil
}

func updateChannelAPI2GPTBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://api.api2gpt.com/dashboard/billing/credit_grants"
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := API2GPTUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	return response.TotalRemaining, nil
}

func updateChannelSiliconFlowBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://api.siliconflow.cn/v1/user/info"
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := SiliconFlowUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Code != 20000 {
		return 0, fmt.Errorf("code: %d, message: %s", response.Code, response.Message)
	}
	balance, err := strconv.ParseFloat(response.Data.TotalBalance, 64)
	if err != nil {
		return 0, err
	}
	return balance, nil
}

// DeepSeek balances have historically been stored in CNY in this fork.
// Prefer that balance, and convert USD-only accounts without changing the
// monitoring history's unit. Negative balances identify exhausted credentials.
func getDeepSeekBalanceCNY(response DeepSeekUsageResponse, usdExchangeRate float64) (float64, error) {
	var usdBalance, cnyBalance *string
	for i := range response.BalanceInfos {
		info := &response.BalanceInfos[i]
		switch info.Currency {
		case "CNY":
			if cnyBalance == nil {
				cnyBalance = &info.TotalBalance
			}
		case "USD":
			if usdBalance == nil {
				usdBalance = &info.TotalBalance
			}
		}
	}
	value, currency := cnyBalance, "CNY"
	if value == nil {
		value, currency = usdBalance, "USD"
	}
	if value == nil {
		return 0, errors.New("currency USD or CNY not found")
	}
	balance, err := strconv.ParseFloat(*value, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(balance) || math.IsInf(balance, 0) {
		return 0, fmt.Errorf("%s balance must be finite", currency)
	}
	if currency == "CNY" {
		return balance, nil
	}
	if math.IsNaN(usdExchangeRate) || math.IsInf(usdExchangeRate, 0) {
		return 0, errors.New("USD exchange rate must be finite")
	}
	if usdExchangeRate <= 0 {
		return 0, errors.New("USD exchange rate must be greater than zero")
	}
	balance = decimal.NewFromFloat(balance).Mul(decimal.NewFromFloat(usdExchangeRate)).InexactFloat64()
	if math.IsNaN(balance) || math.IsInf(balance, 0) {
		return 0, errors.New("converted CNY balance must be finite")
	}
	return balance, nil
}

func updateChannelDeepSeekBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://api.deepseek.com/user/balance"
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := DeepSeekUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	balance, err := getDeepSeekBalanceCNY(response, operation_setting.USDExchangeRate)
	if err != nil {
		return 0, err
	}
	return balance, nil
}

func updateChannelAIGC2DBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://api.aigc2d.com/dashboard/billing/credit_grants"
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := APGC2DGPTUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	return response.TotalAvailable, nil
}

func updateChannelOpenRouterBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://openrouter.ai/api/v1/credits"
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := OpenRouterCreditResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	balance := response.Data.TotalCredits - response.Data.TotalUsage
	return balance, nil
}

func updateChannelMoonshotBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	url := "https://api.moonshot.cn/v1/users/me/balance"
	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}

	type MoonshotBalanceData struct {
		AvailableBalance float64 `json:"available_balance"`
		VoucherBalance   float64 `json:"voucher_balance"`
		CashBalance      float64 `json:"cash_balance"`
	}

	type MoonshotBalanceResponse struct {
		Code   int                 `json:"code"`
		Data   MoonshotBalanceData `json:"data"`
		Scode  string              `json:"scode"`
		Status bool                `json:"status"`
	}

	response := MoonshotBalanceResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Status || response.Code != 0 {
		return 0, fmt.Errorf("failed to update moonshot balance, status: %v, code: %d, scode: %s", response.Status, response.Code, response.Scode)
	}
	availableBalanceCny := response.Data.AvailableBalance
	availableBalanceUsd := decimal.NewFromFloat(availableBalanceCny).Div(decimal.NewFromFloat(operation_setting.Price)).InexactFloat64()
	return availableBalanceUsd, nil
}

func fetchAdvancedCustomBalance(ctx context.Context, channel *model.Channel) (channelBalanceResult, error) {
	key := strings.TrimSpace(channel.Key)
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RelayMode:      relayconstant.RelayModeUnknown,
		RequestURLPath: dto.AdvancedCustomBalancePath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeAdvancedCustom,
			ChannelBaseUrl:       channel.GetBaseURL(),
			ApiKey:               key,
			ChannelOtherSettings: channel.GetOtherSettings(),
		},
	}
	requestURL, headers, err := (&advancedcustom.Adaptor{}).BuildBalanceRequest(info)
	if err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	if err := applyFetchModelsHeaderOverrides(channel, key, headers); err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
		if strings.EqualFold(name, "Host") {
			request.Host = headers.Get(name)
		}
	}
	client, err := service.GetHttpClientWithProxy(channel.GetSetting().Proxy)
	if err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	response, err := client.Do(request)
	if err != nil {
		return channelBalanceResult{}, sanitizeAdvancedCustomRequestError(err, key, requestURL)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return channelBalanceResult{}, fmt.Errorf("status code: %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAdvancedCustomBalanceResponseBytes+1))
	if err != nil {
		return channelBalanceResult{}, sanitizeAdvancedCustomRequestError(err, key, requestURL)
	}
	if len(body) > maxAdvancedCustomBalanceResponseBytes {
		return channelBalanceResult{}, fmt.Errorf("balance response exceeds %d bytes", maxAdvancedCustomBalanceResponseBytes)
	}

	var validated json.RawMessage
	if err := common.Unmarshal(body, &validated); err != nil {
		return channelBalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
	}
	if common.GetJsonType(validated) == "object" {
		var creditSummary struct {
			Object         string          `json:"object"`
			TotalAvailable json.RawMessage `json:"total_available"`
		}
		if err := common.Unmarshal(body, &creditSummary); err != nil {
			return channelBalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
		}
		if creditSummary.Object == "credit_summary" &&
			common.GetJsonType(creditSummary.TotalAvailable) == "number" {
			var balance float64
			if err := common.Unmarshal(creditSummary.TotalAvailable, &balance); err == nil &&
				!math.IsNaN(balance) &&
				!math.IsInf(balance, 0) {
				return channelBalanceResult{Balance: balance}, nil
			}
		}
	}

	formatted, err := common.IndentJson(body)
	if err != nil {
		return channelBalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
	}
	return channelBalanceResult{RawResponse: string(formatted)}, nil
}

func updateChannelBalance(channel *model.Channel) (channelBalanceResult, error) {
	return updateChannelBalanceWithContext(context.Background(), channel)
}

func updateChannelBalanceWithContext(ctx context.Context, channel *model.Channel) (channelBalanceResult, error) {
	startedAt := time.Now().UnixNano()
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	result := channelBalanceResult{KeyBalances: make([]model.ChannelKeyBalance, 0, len(keys))}
	succeeded := 0
	for index, key := range keys {
		if err := ctx.Err(); err != nil {
			return channelBalanceResult{}, err
		}
		// A batch may hold an older snapshot, and an operator can stop checks
		// between keys. Read the current switch before issuing each key query.
		current, err := model.GetChannelById(channel.Id, false)
		if err != nil {
			return channelBalanceResult{}, err
		}
		if err := current.CheckBalanceQueryEnabled(); err != nil {
			return channelBalanceResult{}, err
		}
		// Probes read upstream only. They never write channel configuration,
		// key status, or intermediate balances to the database.
		probe := *channel
		probe.Key = key
		probe.Keys = nil
		probe.ChannelInfo.IsMultiKey = false
		keyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		keyResult, err := updateSingleKeyChannelBalance(keyCtx, &probe)
		cancel()
		if ctx.Err() != nil {
			return channelBalanceResult{}, ctx.Err()
		}
		if keyResult.RawResponse != "" && !channel.ChannelInfo.IsMultiKey {
			result.RawResponse = keyResult.RawResponse
		}
		entry := model.ChannelKeyBalance{Index: index}
		if err != nil {
			// Provider errors may echo authorization headers, query credentials,
			// or proxy URLs. Monitoring stores and exposes only stable codes.
			entry.Error = "balance_query_failed"
		} else if keyResult.RawResponse != "" {
			entry.Error = "non_numeric_balance"
		} else if math.IsNaN(keyResult.Balance) || math.IsInf(keyResult.Balance, 0) ||
			math.IsInf(result.Balance+keyResult.Balance, 0) {
			entry.Error = "invalid_balance"
		} else {
			balance := keyResult.Balance
			entry.Balance = &balance
			// Each key owns a separate account. An exhausted account cannot
			// reduce the funds available from another account.
			if !channel.ChannelInfo.IsMultiKey || balance > 0 {
				result.Balance += balance
			}
			succeeded++
		}
		result.KeyBalances = append(result.KeyBalances, entry)
	}
	result.Partial = succeeded > 0 && succeeded < len(keys)
	sample := &model.ChannelBalanceSample{
		StartedAt: startedAt, CheckedAt: common.GetTimestamp(),
		KnownBalance: result.Balance, Partial: result.Partial,
		Success: len(keys) > 0 && succeeded == len(keys), KeyBalances: result.KeyBalances,
	}
	if err := model.RecordChannelBalanceSample(channel, sample); err != nil {
		return channelBalanceResult{}, err
	}
	result.Monitor = &model.ChannelBalanceMonitor{
		CheckedAt: sample.CheckedAt, Balance: sample.Balance,
		BalanceUpdatedTime: sample.BalanceUpdatedTime, KnownBalance: sample.KnownBalance,
		Partial: sample.Partial, Success: sample.Success, KeyBalances: sample.KeyBalances,
	}
	if succeeded == 0 && result.RawResponse == "" {
		return result, errors.New("balance query failed for all keys")
	}
	return result, nil
}

func updateSingleKeyChannelBalance(ctx context.Context, channel *model.Channel) (channelBalanceResult, error) {
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		return fetchAdvancedCustomBalance(ctx, channel)
	}
	balance, err := updateStandardChannelBalance(ctx, channel)
	return channelBalanceResult{Balance: balance}, err
}

// resolveBalanceQuery reports which billing API to query and where to send it.
// An unrecognized override is ignored rather than failing the query, so a
// setting written by a newer release cannot break balance checks on an older one.
func resolveBalanceQuery(channel *model.Channel) (int, string) {
	queryType := channel.Type
	baseURL := ""
	settings := channel.GetSetting()
	if mapped, ok := constant.BalanceQueryChannelType(settings.BalanceQueryType); ok {
		queryType = mapped
	}
	if override := strings.TrimRight(strings.TrimSpace(settings.BalanceQueryBaseURL), "/"); override != "" {
		baseURL = override
	}
	return queryType, baseURL
}

func updateStandardChannelBalance(ctx context.Context, channel *model.Channel) (float64, error) {
	queryType, overrideBaseURL := resolveBalanceQuery(channel)
	baseURL := constant.GetChannelBaseURL(channel.Type)
	if channel.GetBaseURL() == "" {
		channel.BaseURL = &baseURL
	}
	switch queryType {
	case constant.ChannelTypeOpenAI:
		if channel.GetBaseURL() != "" {
			baseURL = channel.GetBaseURL()
		}
	case constant.ChannelTypeAzure:
		return 0, errors.New("尚未实现")
	case constant.ChannelTypeCustom:
		baseURL = channel.GetBaseURL()
	//case common.ChannelTypeOpenAISB:
	//	return updateChannelOpenAISBBalance(ctx, channel)
	case constant.ChannelTypeAIProxy:
		return updateChannelAIProxyBalance(ctx, channel)
	case constant.ChannelTypeAPI2GPT:
		return updateChannelAPI2GPTBalance(ctx, channel)
	case constant.ChannelTypeAIGC2D:
		return updateChannelAIGC2DBalance(ctx, channel)
	case constant.ChannelTypeSiliconFlow:
		return updateChannelSiliconFlowBalance(ctx, channel)
	case constant.ChannelTypeDeepSeek:
		return updateChannelDeepSeekBalance(ctx, channel)
	case constant.ChannelTypeOpenRouter:
		return updateChannelOpenRouterBalance(ctx, channel)
	case constant.ChannelTypeMoonshot:
		return updateChannelMoonshotBalance(ctx, channel)
	default:
		return 0, errors.New("尚未实现")
	}
	// The override wins over both the channel's relay base URL and the
	// provider default: it exists precisely for a gateway whose billing API
	// does not sit where its relay endpoint does.
	if overrideBaseURL != "" {
		baseURL = overrideBaseURL
	}
	url := fmt.Sprintf("%s/v1/dashboard/billing/subscription", baseURL)

	body, err := GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	subscription := OpenAISubscriptionResponse{}
	err = common.Unmarshal(body, &subscription)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	startDate := fmt.Sprintf("%s-01", now.Format("2006-01"))
	endDate := now.Format("2006-01-02")
	if !subscription.HasPaymentMethod {
		startDate = now.AddDate(0, 0, -100).Format("2006-01-02")
	}
	url = fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", baseURL, startDate, endDate)
	body, err = GetResponseBody(ctx, "GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	usage := OpenAIUsageResponse{}
	err = common.Unmarshal(body, &usage)
	if err != nil {
		return 0, err
	}
	balance := subscription.HardLimitUSD - usage.TotalUsage/100
	return balance, nil
}

func UpdateChannelBalance(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if channel.Type == constant.ChannelTypeTaskPlugin {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Task Plugin channels do not support balance queries"})
		return
	}
	result, err := updateChannelBalanceWithContext(c.Request.Context(), channel)
	if err != nil {
		if result.Monitor != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error(), "balance_monitor": result.Monitor})
			return
		}
		common.ApiError(c, err)
		return
	}
	response := gin.H{
		"success": true,
		"message": "",
	}
	if result.RawResponse == "" {
		response["balance"] = result.Monitor.Balance
		response["known_balance"] = result.Balance
		response["partial"] = result.Partial
	} else {
		response["raw_response"] = result.RawResponse
	}
	response["balance_monitor"] = result.Monitor
	if len(result.KeyBalances) > 0 {
		response["key_balances"] = result.KeyBalances
	}
	c.JSON(http.StatusOK, response)
}

// channelBalanceSummary 记录一轮余额刷新的结果，写进 system task 的 result 里，
// 管理员在任务列表就能看到这次刷新覆盖了多少渠道、有多少家上游没答复。
type channelBalanceSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Partial   int `json:"partial"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

func updateAllChannelsBalance(ctx context.Context, report func(processed, total int)) (channelBalanceSummary, error) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return channelBalanceSummary{}, err
	}
	summary := channelBalanceSummary{Total: len(channels)}
	for index, channel := range channels {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if channel.Type == constant.ChannelTypeTaskPlugin {
			summary.Skipped++
		} else {
			// A disabled channel can still have upstream funds. Balance checks
			// include every channel and never enable/disable channels or keys.
			result, err := updateChannelBalanceWithContext(ctx, channel)
			if errors.Is(err, model.ErrChannelBalanceQueryDisabled) {
				summary.Skipped++
			} else if err != nil {
				if ctx.Err() != nil {
					return summary, ctx.Err()
				}
				summary.Failed++
			} else if result.RawResponse != "" {
				summary.Skipped++
			} else if result.Partial {
				summary.Partial++
			} else {
				summary.Succeeded++
			}
		}
		if report != nil {
			report(index+1, len(channels))
		}
		if index+1 < len(channels) && common.RequestInterval > 0 {
			timer := time.NewTimer(common.RequestInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return summary, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return summary, nil
}

// UpdateAllChannelsBalance enqueues a channel_balance system task instead of
// holding the request open for the whole sweep: one upstream billing call per
// channel adds up well past any sane HTTP timeout.
func UpdateAllChannelsBalance(c *gin.Context) {
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeChannelBalance, nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "已有余额刷新任务正在运行或等待中，不能启动本次手动任务",
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
