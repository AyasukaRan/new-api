package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const channelKeyFailoverContext = "channel_key_failover"
const channelKeyRetryOffset = "channel_key_retry_offset"
const ChannelKeySelectionContext = "channel_key_selection"

type channelKeyFailover struct {
	ChannelID int
	Group     string
	Allowed   map[int]map[string]bool
	Tried     map[int]map[string]bool
	Exhausted map[string]map[int]bool
}

func HasChannelKeyFailover(c *gin.Context) bool {
	value, _ := c.Get(channelKeyFailoverContext)
	state, _ := value.(*channelKeyFailover)
	return state != nil && state.ChannelID > 0
}

func markChannelKeyAttempt(c *gin.Context, channel *model.Channel, key string) {
	if value, exists := c.Get(ChannelKeySelectionContext); exists {
		if selected, ok := value.(*model.Channel); ok && selected != nil && selected.Id == channel.Id {
			channel = selected
		}
	}
	value, _ := c.Get(channelKeyFailoverContext)
	state, _ := value.(*channelKeyFailover)
	if channel.ChannelInfo.MultiKeyMode != constant.MultiKeyModeFailover {
		if state != nil {
			state.ChannelID = 0
		}
		return
	}
	if state == nil {
		state = &channelKeyFailover{Allowed: make(map[int]map[string]bool), Tried: make(map[int]map[string]bool), Exhausted: make(map[string]map[int]bool)}
		c.Set(channelKeyFailoverContext, state)
	}
	state.ChannelID, state.Group = channel.Id, GetSelectedRequestGroup(c)
	if state.Exhausted[state.Group] == nil {
		state.Exhausted[state.Group] = make(map[int]bool)
	}
	if state.Allowed[channel.Id] == nil {
		state.Allowed[channel.Id] = make(map[string]bool)
		state.Tried[channel.Id] = make(map[string]bool)
		keys := []string{channel.Key}
		if channel.ChannelInfo.IsMultiKey {
			keys = channel.GetKeys()
		}
		for _, candidate := range keys {
			state.Allowed[channel.Id][channel.KeyIdentity(candidate)] = true
		}
		// Batch credentials can differ from the ordinary channel key list.
		state.Allowed[channel.Id][channel.KeyIdentity(key)] = true
	}
	state.Tried[channel.Id][channel.KeyIdentity(key)] = true
}

// SelectChannelKeyRetry only orders keys inside the already selected channel.
// The caller still owns the ordinary retry budget and replay checks.
func SelectChannelKeyRetry(c *gin.Context, modelName string) (*model.Channel, error) {
	value, _ := c.Get(channelKeyFailoverContext)
	state, _ := value.(*channelKeyFailover)
	if state == nil || state.ChannelID == 0 || HasGroupKeyRouting(c) {
		return nil, nil
	}
	endpointStates, _ := c.Get(channelEndpointContext)
	endpoints, _ := endpointStates.(map[int]*channelEndpointCandidates)
	if mirrors := endpoints[state.ChannelID]; mirrors != nil && len(mirrors.failed) == len(mirrors.urls) {
		state.Exhausted[state.Group][state.ChannelID] = true
		return nil, nil
	}
	channel, err := model.GetChannelById(state.ChannelID, true)
	if err != nil {
		return nil, err
	}
	if channel.Status != common.ChannelStatusEnabled || channel.ChannelInfo.MultiKeyMode != constant.MultiKeyModeFailover {
		state.Exhausted[state.Group][channel.Id] = true
		return nil, nil
	}
	constraints := GetChannelConstraints(c)
	if ok, _ := model.ChannelSatisfiesFilters(channel, modelName, constraints.Filters); !ok {
		state.Exhausted[state.Group][channel.Id] = true
		return nil, nil
	}
	batch := constraints.HasFilter(dto.FilterBatchCapable)
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	for _, index := range channel.OrderedKeyIndices() {
		identity := channel.KeyIdentity(keys[index])
		if !state.Allowed[channel.Id][identity] || state.Tried[channel.Id][identity] {
			continue
		}
		candidate := *channel
		candidate.RelayKeyIdentity, candidate.RelayKeyIndex = identity, index
		selected, selectErr := candidate.SelectRelayKeyForModel(modelName, batch, state.Group)
		if selectErr != nil {
			continue
		}
		actualIdentity := channel.KeyIdentity(selected.Key)
		if !state.Allowed[channel.Id][actualIdentity] || state.Tried[channel.Id][actualIdentity] {
			continue
		}
		return &candidate, nil
	}
	state.Exhausted[state.Group][channel.Id] = true
	return nil, nil
}
