package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/keyusage"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const channelKeyUsageStopContext = "channel_key_usage_stop"

// PrepareChannelKeyUsage runs after request validation and pricing, before any
// bytes reach the upstream. Other modes keep their selected key and only record usage.
func PrepareChannelKeyUsage(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel, pinned bool) (*model.Channel, *types.NewAPIError) {
	FinishChannelKeyUsage(c, info)
	info.KeyUsageReservation, info.KeyUsageObservation = nil, nil
	c.Set(keyRoutingEstimatedTokensContext, false)
	if channel == nil || info.IsChannelTest {
		return channel, nil
	}
	weighted := channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeWeightedUsage
	if info.TaskRelayInfo != nil || channel.Type == constant.ChannelTypeTaskPlugin || GetChannelConstraints(c).HasFilter(dto.FilterBatchCapable) {
		if weighted {
			return channel, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayWeightedKeyUsageUnsupported)), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return channel, nil
	}
	if !common.RedisEnabled || common.RDB == nil {
		if weighted {
			return channel, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayWeightedKeyUsageStatsUnavailable)), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
		return channel, nil
	}
	// Passive reporting does not need to tokenize the request again. Only
	// weighted admission needs a forecast before selecting a credential.
	estimate := KeyRoutingEstimate{InputTokens: int64(max(info.GetEstimatePromptTokens(), 1))}
	if weighted {
		var err error
		estimate, err = EstimateKeyRoutingUsage(c, info, info.Request)
		if err != nil {
			return channel, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayWeightedKeyUsageEstimateFailed)), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	usingKey := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	group := GetSelectedRequestGroup(c)
	selections := make(map[string]model.RelayKeySelection)
	candidates := make([]keyusage.Candidate, 0)
	preferred := ""
	if weighted {
		current, loadErr := model.GetChannelById(channel.Id, true)
		if loadErr != nil || current.Status != common.ChannelStatusEnabled || current.ChannelInfo.MultiKeyMode != constant.MultiKeyModeWeightedUsage || !slices.Contains(current.GetGroups(), group) {
			return channel, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayWeightedKeyUsageChannelUnavailable)), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
		channel = current
		// An established socket or file remains bound to the same credential.
		pinned = pinned || c.GetString("relay_file_key_identity") != ""
		value, _ := c.Get("weighted_key_tier")
		tier, _ := value.(map[string]bool)
		value, _ = c.Get(groupKeyRouteContext)
		groupState, _ := value.(*groupKeyRoute)
		value, _ = c.Get(channelKeyFailoverContext)
		failover, _ := value.(*channelKeyFailover)
		binding := prepareChannelKeyAffinity(c, channel.Id, group)
		if binding != nil && RequestPolicy(c).Attempts <= 1 {
			preferred = binding.Expected
		}
		if groupState != nil && tier == nil {
			pinned = true
		}
		for _, selected := range channel.RelayKeyCandidatesForModel(info.OriginModelName, group) {
			key, identity := selected.Key, selected.Channel.RelayKeyIdentity
			if pinned && key != usingKey {
				continue
			}
			if !pinned && (tier != nil && !tier[identity] || groupState != nil && (!groupState.Allowed[identity] || groupState.Tried[identity])) {
				continue
			}
			if !pinned && failover != nil && failover.Allowed[channel.Id] != nil && (!failover.Allowed[channel.Id][identity] || failover.Tried[channel.Id][identity]) {
				continue
			}
			selected.UpstreamCostRatio = selected.Channel.UpstreamCostRatioForKey(key)
			selections[identity] = selected
			candidates = append(candidates, keyusage.Candidate{Identity: identity, Weight: channel.RoutingWeightForKey(key), InputTokens: estimate.InputTokens, OutputTokens: estimate.OutputTokens, CostUSD: estimate.CostUSD(selected.UpstreamCostRatio)})
		}
	} else {
		identity := channel.KeyIdentity(usingKey)
		candidates = append(candidates, keyusage.Candidate{Identity: identity, Weight: channel.RoutingWeightForKey(usingKey), InputTokens: estimate.InputTokens, OutputTokens: estimate.OutputTokens, CostUSD: estimate.CostUSD(channel.UpstreamCostRatioForKey(usingKey))})
	}
	if len(candidates) == 0 {
		return channel, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayChannelNoGroupKey)), types.ErrorCodeChannelNoAvailableKey, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	if preferred != "" {
		if _, eligible := selections[preferred]; !eligible {
			preferred = ""
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	reservation, err := keyusage.Reserve(ctx, channel.Id, common.NewRequestId(), candidates, preferred)
	cancel()
	if err != nil {
		if weighted {
			return channel, types.NewErrorWithStatusCode(errors.New(i18n.T(c, i18n.MsgRelayWeightedKeyUsageStatsUnavailable)), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
		common.SysError("channel key usage reservation failed")
		return channel, nil
	}
	info.KeyUsageReservation = reservation
	stop := make(chan struct{})
	var once sync.Once
	c.Set(channelKeyUsageStopContext, func() { once.Do(func() { close(stop) }) })
	if weighted {
		selected := selections[reservation.KeyIdentity]
		verified, verifyErr := selected.Channel.SelectRelayKeyForModel(info.OriginModelName, false, group)
		if verifyErr != nil {
			FinishChannelKeyUsage(c, info)
			return channel, verifyErr
		}
		selected = verified
		channel = selected.Channel
		common.SetContextKey(c, constant.ContextKeyChannelKey, selected.Key)
		common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, selected.Index)
		common.SetContextKey(c, constant.ContextKeyChannelUpstreamCostRatio, selected.UpstreamCostRatio)
		c.Set(ChannelKeySelectionContext, channel)
		if info.ChannelMeta != nil {
			info.ApiKey, info.ChannelMultiKeyIndex = selected.Key, selected.Index
		}
		info.UpstreamCostRatio = selected.UpstreamCostRatio
		if binding := prepareChannelKeyAffinity(c, channel.Id, group); binding != nil {
			binding.Selected = reservation.KeyIdentity
			if binding.InitialSelected == "" || binding.Expected == "" {
				binding.InitialSelected = binding.Selected
			}
		}
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := keyusage.Renew(ctx, reservation); err != nil {
					common.SysError("channel key usage renewal failed")
				}
				cancel()
			}
		}
	}()
	return channel, nil
}

// FinishChannelKeyUsage is also safe in the relay's panic/cancellation defer.
// Keep the ticket on RelayInfo until error logging has recorded the attempt.
func FinishChannelKeyUsage(c *gin.Context, info *relaycommon.RelayInfo) {
	value, _ := c.Get(channelKeyUsageStopContext)
	stop, _ := value.(func())
	if stop == nil || info == nil || info.KeyUsageReservation == nil {
		return
	}
	stop()
	c.Set(channelKeyUsageStopContext, nil)
	// Capture values before RelayInfo is reused for the next attempt. A brief
	// Redis outage must not discard this attempt's final usage or hold up relay.
	reservation := *info.KeyUsageReservation
	var observation *keyusage.Observation
	if info.KeyUsageObservation != nil {
		snapshot := *info.KeyUsageObservation
		if snapshot.CostUSD != nil {
			cost := *snapshot.CostUSD
			snapshot.CostUSD = &cost
		}
		observation = &snapshot
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := keyusage.Finish(ctx, &reservation, observation)
	cancel()
	if err == nil || errors.Is(err, keyusage.ErrNotFound) {
		return
	}
	common.RelayCtxGo(context.Background(), func() {
		for _, delay := range []time.Duration{time.Second, 5 * time.Second, 15 * time.Second, time.Minute, 2 * time.Minute} {
			time.Sleep(delay)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := keyusage.Finish(ctx, &reservation, observation)
			cancel()
			if err == nil || errors.Is(err, keyusage.ErrNotFound) {
				return
			}
		}
		common.SysError("channel key usage settlement failed after retries")
	})
}
