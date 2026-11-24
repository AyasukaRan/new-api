package controller

import (
	"context"

	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel/providerplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// balanceQueryTimeout bounds the balance query of one key in an update of all
// balances, and of one plugin key line anywhere.
const balanceQueryTimeout = 30 * time.Second

// maxBalanceErrorRunes bounds the failure message a balance detail keeps.
const maxBalanceErrorRunes = 512

// emptyBalance reports a definite balance that is used up: a finite amount of
// 0 or less, not unlimited.
func emptyBalance(result jsplugin.BalanceResult) bool {
	return result.Amount != nil && !result.Unlimited && !math.IsNaN(*result.Amount) && !math.IsInf(*result.Amount, 0) && *result.Amount <= 0
}

// channelBalanceUnit describes the numeric value stored by each balance query.
func channelBalanceUnit(channel *model.Channel) string {
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		return ""
	}
	if channel.Type == constant.ChannelTypeTaskPlugin {
		// Plugin samples normalize monetary readings to USD. The detail retains the original currency.
		return "USD"
	}
	queryType, _ := resolveBalanceQuery(channel)
	switch queryType {
	case constant.ChannelTypeDeepSeek, constant.ChannelTypeSiliconFlow:
		return "CNY"
	case constant.ChannelTypeOpenAI, constant.ChannelTypeCustom, constant.ChannelTypeOpenRouter, constant.ChannelTypeMoonshot, constant.ChannelTypeNewAPI, constant.ChannelTypeSub2API:
		return "USD"
	case constant.ChannelTypeAIProxy:
		return "points"
	default:
		return ""
	}
}

// pluginReportsBalance reports whether a plugin exports the balance hooks.
func pluginReportsBalance(plugin *jsplugin.LoadedPlugin) bool {
	return plugin.HasProviderHook("buildBalanceRequest") && plugin.HasProviderHook("parseBalance")
}

// queryPluginKeyBalance queries the balance of one key line of a task plugin
// channel through the channel's proxy, TLS setting and header overrides.
func queryPluginKeyBalance(ctx context.Context, channel *model.Channel, plugin *jsplugin.LoadedPlugin, index int, key string) (jsplugin.BalanceResult, error) {
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RelayMode:      relayconstant.RelayModeUnknown,
		RequestURLPath: dto.AdvancedCustomBalancePath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          channel.Type,
			ChannelId:            channel.Id,
			ChannelIsMultiKey:    channel.ChannelInfo.IsMultiKey,
			ChannelMultiKeyIndex: index,
			ChannelBaseUrl:       channel.GetBaseURL(),
			ApiKey:               key,
			ChannelSetting:       channel.GetSetting(),
			ChannelOtherSettings: channel.GetOtherSettings(),
		},
	}
	return providerplugin.QueryBalance(ctx, plugin, info, providerplugin.ManagementClient{
		Override: func(headers http.Header, apiKey string) error {
			return applyFetchModelsHeaderOverrides(channel, apiKey, headers)
		},
		Send: func(request *http.Request) (*http.Response, error) {
			return sendChannelRequest(channel, request)
		},
	})
}

// balanceErrorText is the failure message a balance detail keeps: without
// the request URL, the key line, and a credential line's header and query
// values, and bounded in length.
func balanceErrorText(err error, key string) string {
	message := sanitizeFetchModelsError(err, key).Error()
	if credential, ok := jsplugin.ParseCredential(key); ok {
		secrets := make([]string, 0, len(credential.Headers)+len(credential.Query))
		for _, value := range credential.Headers {
			secrets = append(secrets, value)
		}
		for _, value := range credential.Query {
			secrets = append(secrets, value)
		}
		for _, secret := range secrets {
			if strings.TrimSpace(secret) == "" {
				continue
			}
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
			message = strings.ReplaceAll(message, url.QueryEscape(secret), "[REDACTED]")
		}
	}
	if runes := []rune(message); len(runes) > maxBalanceErrorRunes {
		message = string(runes[:maxBalanceErrorRunes]) + "..."
	}
	return message
}

// updateBalanceInRun applies plugin automatic disabling only to enabled keys with a definite, exhausted amount.
func updateBalanceInRun(ctx context.Context, channel *model.Channel, scheduled bool) (channelBalanceResult, error) {
	return queryChannelBalanceKeys(ctx, channel, scheduled, true)
}
