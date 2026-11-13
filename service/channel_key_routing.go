package service

import (
	"errors"
	"math/rand"
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const groupKeyRouteContext = "group_key_routing"

type groupKeyRoute struct {
	Group     string
	ChannelID int
	Allowed   map[string]bool
	Tried     map[string]bool
}

func HasGroupKeyRouting(c *gin.Context) bool {
	value, exists := c.Get(groupKeyRouteContext)
	return exists && value != nil
}

func MarkGroupKeyAttempt(c *gin.Context, channel *model.Channel, key string) {
	if value, exists := c.Get(groupKeyRouteContext); exists && value != nil {
		state := value.(*groupKeyRoute)
		state.Tried[channel.KeyIdentity(key)] = true
	}
}

// SelectGroupKeyRetry keeps the concrete group and channel constraints selected
// for this request. It never resets the ordinary retry budget.
func SelectGroupKeyRetry(c *gin.Context, modelName string) (*model.Channel, error) {
	value, exists := c.Get(groupKeyRouteContext)
	if !exists || value == nil {
		return nil, nil
	}
	state := value.(*groupKeyRoute)
	channel, _, err := selectGroupKeyTier(c, state.Group, modelName, state.ChannelID, 0)
	return channel, err
}

func selectGroupKeyTier(c *gin.Context, group, modelName string, channelID, preferredID int) (*model.Channel, bool, error) {
	var state *groupKeyRoute
	if value, exists := c.Get(groupKeyRouteContext); exists && value != nil {
		state = value.(*groupKeyRoute)
		group, channelID = state.Group, state.ChannelID
	}
	candidates, enabled, err := model.GetGroupKeyCandidates(group, modelName, GetChannelConstraints(c).Filters, channelID)
	if err != nil || !enabled {
		// A policy removed midway through a request must not restore legacy
		// cross-group retry or reuse credentials already attempted.
		return nil, enabled || state != nil, err
	}
	if state == nil {
		if len(candidates) == 0 {
			return nil, true, nil
		}
		state = &groupKeyRoute{Group: group, ChannelID: channelID, Allowed: make(map[string]bool), Tried: make(map[string]bool)}
		for _, candidate := range candidates {
			state.Allowed[candidate.RelayCredentialIdentity] = true
		}
		c.Set(groupKeyRouteContext, state)
	}
	var highest int64
	var tier []*model.Channel
	var eligible []*model.Channel
	hasExact := false
	endpointStates, _ := c.Get(channelEndpointContext)
	endpoints, _ := endpointStates.(map[int]*channelEndpointCandidates)
	for _, candidate := range candidates {
		if mirrors := endpoints[candidate.Id]; mirrors != nil && len(mirrors.failed) == len(mirrors.urls) {
			continue
		}
		identity := candidate.RelayCredentialIdentity
		if !state.Allowed[identity] || state.Tried[identity] {
			continue
		}
		eligible = append(eligible, candidate)
		hasExact = hasExact || candidate.RelayModelExact
	}
	for _, candidate := range eligible {
		if hasExact && !candidate.RelayModelExact {
			continue
		}
		if len(tier) == 0 || candidate.RelayKeyPriority > highest {
			highest, tier = candidate.RelayKeyPriority, nil
		}
		if candidate.RelayKeyPriority == highest {
			tier = append(tier, candidate)
		}
	}
	if len(tier) == 0 {
		return nil, true, nil
	}
	// Weight channels once, regardless of how many keys they contribute.
	channels := make(map[int]int)
	ids := make([]int, 0)
	for _, candidate := range tier {
		if _, exists := channels[candidate.Id]; !exists {
			ids = append(ids, candidate.Id)
			channels[candidate.Id] = candidate.GetWeight()
		}
	}
	slices.Sort(ids)
	selectedID := preferredID
	if _, exists := channels[selectedID]; !exists {
		total := 0
		for _, weight := range channels {
			total += weight
		}
		if total == 0 {
			selectedID = ids[rand.Intn(len(ids))]
		} else {
			choice := rand.Intn(total)
			for _, id := range ids {
				choice -= channels[id]
				if choice < 0 {
					selectedID = id
					break
				}
			}
		}
	}
	var keys []*model.Channel
	for _, candidate := range tier {
		if candidate.Id == selectedID {
			keys = append(keys, candidate)
		}
	}
	slices.SortFunc(keys, func(a, b *model.Channel) int { return a.RelayKeyIndex - b.RelayKeyIndex })
	selected := keys[0]
	if selected.ChannelInfo.MultiKeyMode == constant.MultiKeyModeRandom {
		selected = keys[rand.Intn(len(keys))]
	} else if selected.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
		cursor := selected.ChannelInfo.MultiKeyPollingIndex
		for _, candidate := range keys {
			if candidate.RelayKeyIndex >= cursor {
				selected = candidate
				break
			}
		}
	}
	if common.GetContextKeyString(c, constant.ContextKeyUsingGroup) == "auto" {
		common.SetContextKey(c, constant.ContextKeyAutoGroup, group)
	}
	return selected, true, nil
}

func groupKeySelectionError(err error) *ChannelSelectError {
	var probeErr *model.ChannelProbeError
	if errors.As(err, &probeErr) {
		return channelProbeSelectionError(probeErr)
	}
	return &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Code: types.ErrorCodeChannelNoAvailableKey, MessageID: i18n.MsgRelayChannelNoGroupKey}
}

// BindGroupKeyChannel follows an explicit origin-task channel binding. Only
// that authorized channel participates; an old preliminary selection is discarded.
func BindGroupKeyChannel(c *gin.Context, channel *model.Channel, modelName string) (*model.Channel, error) {
	c.Set(groupKeyRouteContext, nil)
	selected, enabled, err := selectGroupKeyTier(c, GetSelectedRequestGroup(c), modelName, channel.Id, channel.Id)
	if err != nil {
		return nil, err
	}
	if enabled {
		if selected == nil {
			return nil, model.ErrChannelDisabled
		}
		return selected, nil
	}
	return channel, nil
}

func LockGroupKeyRouting(c *gin.Context, channelID int) {
	if value, exists := c.Get(groupKeyRouteContext); exists && value != nil {
		value.(*groupKeyRoute).ChannelID = channelID
	}
}
