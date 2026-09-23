package perfmetrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// AdminKeyResult is deliberately separate from public performance DTOs.
// ObservedAt uses seconds; Success preserves an expired diagnostic observation,
// whereas CurrentAvailable is absent when a key is disabled or stale.
type AdminKeyResult struct {
	KeyIndex         int    `json:"key_index"`
	KeyHint          string `json:"key_hint"`
	Enabled          bool   `json:"enabled"`
	CurrentAvailable *bool  `json:"current_available,omitempty"`
	ObservedAt       int64  `json:"observed_at"`
	Success          *bool  `json:"success,omitempty"`
	Source           string `json:"source,omitempty"`
	StatusCode       int    `json:"status_code,omitempty"`
	Error            string `json:"error,omitempty"`
}

type AdminChannelResult struct {
	ChannelID         int              `json:"channel_id"`
	ChannelName       string           `json:"channel_name"`
	Status            int              `json:"status"`
	CurrentAvailable  *bool            `json:"current_available,omitempty"`
	CurrentObservedAt int64            `json:"current_observed_at,omitempty"`
	AvailabilityRate  *float64         `json:"availability_rate,omitempty"`
	Keys              []AdminKeyResult `json:"keys"`
}

type AdminModelResult struct {
	ModelName string               `json:"model_name"`
	Channels  []AdminChannelResult `json:"channels"`
}

// matchingChannelKeyObservation accepts legacy identities only when they bind
// to the current complete configuration. Mixed-version nodes can still write
// either format, so the latest observation wins; equal times prefer v2.
func matchingChannelKeyObservation(channel *model.Channel, modelName, key string, observations map[string]model.ChannelKeyObservation) (model.ChannelKeyObservation, bool) {
	id, err := model.ChannelKeyObservationID(channel, modelName, key)
	if err != nil {
		return model.ChannelKeyObservation{}, false
	}
	row, found := observations[id]
	legacyID, err := model.LegacyChannelKeyObservationID(channel, modelName, key)
	if err == nil {
		if legacyRow, legacyFound := observations[legacyID]; legacyFound && (!found || legacyRow.ObservedAt > row.ObservedAt) {
			return legacyRow, true
		}
	}
	return row, found
}

// currentChannelKeyAvailability shares the same three-state OR for both views.
// Every enabled key must have a fresh failure to declare the channel failed;
// one fresh success is sufficient, and unknown keys never become failures.
func currentChannelKeyAvailability(channel *model.Channel, modelName string, observations map[string]model.ChannelKeyObservation, now int64) currentAvailability {
	if channel.Status != common.ChannelStatusEnabled {
		return currentAvailability{}
	}
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	cutoff := now - operation_setting.ChannelTestInterval().Milliseconds()
	states := []currentAvailability{}
	for index, key := range keys {
		status, exists := channel.ChannelInfo.MultiKeyStatusList[index]
		if key == "" || (channel.ChannelInfo.IsMultiKey && exists && status != common.ChannelStatusEnabled) {
			continue
		}
		state := currentAvailability{}
		if row, found := matchingChannelKeyObservation(channel, modelName, key, observations); found {
			state.observedAt = row.ObservedAt / 1000
			if row.ObservedAt >= cutoff && row.ObservedAt <= now {
				state.available = common.GetPointer(row.Success)
			}
		}
		states = append(states, state)
	}
	return modelCurrentAvailability(states)
}

// RecordChannelKeyResult is independent of the optional hourly metrics switch.
// Call at each actual relay attempt's completion, before retry metadata changes.
func RecordChannelKeyResult(info *relaycommon.RelayInfo, channel *model.Channel, success *bool, statusCode int, errorMessage string) {
	if info == nil || info.ChannelMeta == nil || info.ApiKey == "" || info.IsChannelTest || channel == nil || success == nil {
		return
	}
	// A failed pre-request configuration snapshot leaves diagnostics unknown.
	// Loading current configuration here could misattribute an in-flight result
	// after an administrator changed its mapping, endpoint, or credentials.
	if channel.Key == "" {
		return
	}
	if err := model.RecordChannelKeyObservation(channel, info.OriginModelName, info.ApiKey, time.Now().UnixMilli(), success, statusCode, errorMessage, SourceRequest); err != nil {
		common.SysError(fmt.Sprintf("channel key observation persistence failed: channel_id=%d model=%q", channel.Id, info.OriginModelName))
	}
}

// QueryAdminModel requires server-side administrator authorization at its route.
// It fetches credentials solely to match opaque identities and mask their hints;
// neither raw channel models nor observation fingerprints enter the response.
func QueryAdminModel(modelName string, hours int) (AdminModelResult, error) {
	result := AdminModelResult{ModelName: modelName, Channels: []AdminChannelResult{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var channels []model.Channel
	if err := model.DB.WithContext(ctx).Order("id ASC").Find(&channels).Error; err != nil {
		return result, err
	}
	configured := channels[:0]
	ids := []int{}
	for _, channel := range channels {
		for _, name := range channel.GetModels() {
			if strings.TrimSpace(name) == modelName {
				configured = append(configured, channel)
				ids = append(ids, channel.Id)
				break
			}
		}
	}
	channels = configured
	observations, err := model.GetChannelKeyObservations(ids, modelName)
	if err != nil {
		return result, err
	}
	byID := make(map[string]model.ChannelKeyObservation, len(observations))
	for _, row := range observations {
		byID[row.ID] = row
	}
	currentStates, err := queryCurrentAvailability(channels)
	if err != nil {
		return result, err
	}
	buckets, err := queryChannelBuckets(modelName, 0, nil, hours, SourceRequest, SourceProbe, SourceRouteState)
	if err != nil {
		return result, err
	}
	observedSeries, fallbackSeries := map[int]map[int64]counters{}, map[int]map[int64]counters{}
	for key, value := range buckets {
		if key.source != SourceRequest && key.group != "" {
			continue
		}
		series := observedSeries
		if key.source == SourceRouteState {
			series = fallbackSeries
		}
		if series[key.channelID] == nil {
			series[key.channelID] = map[int64]counters{}
		}
		series[key.channelID][key.bucketTs] = addCounterValues(series[key.channelID][key.bucketTs], value)
	}
	now := time.Now().UnixMilli()
	cutoff := now - operation_setting.ChannelTestInterval().Milliseconds()
	for _, channel := range channels {
		entry := AdminChannelResult{ChannelID: channel.Id, ChannelName: channel.Name, Status: channel.Status, Keys: []AdminKeyResult{}}
		entry.AvailabilityRate, _ = mergeAvailabilityHistory(fallbackSeries[channel.Id], observedSeries[channel.Id])
		keys := []string{channel.Key}
		if channel.ChannelInfo.IsMultiKey {
			keys = channel.GetKeys()
		}
		for index, key := range keys {
			if key == "" {
				continue
			}
			keyStatus, exists := channel.ChannelInfo.MultiKeyStatusList[index]
			enabled := !channel.ChannelInfo.IsMultiKey || !exists || keyStatus == common.ChannelStatusEnabled
			keyResult := AdminKeyResult{KeyIndex: index, KeyHint: model.MaskTokenKey(key), Enabled: enabled}
			if row, found := matchingChannelKeyObservation(&channel, modelName, key, byID); found {
				keyResult.ObservedAt, keyResult.Success = row.ObservedAt/1000, common.GetPointer(row.Success)
				keyResult.Source, keyResult.StatusCode, keyResult.Error = row.Source, row.StatusCode, row.ErrorText
				if enabled && channel.Status == common.ChannelStatusEnabled && row.ObservedAt >= cutoff && row.ObservedAt <= now {
					keyResult.CurrentAvailable = keyResult.Success
				}
			}
			entry.Keys = append(entry.Keys, keyResult)
		}
		if channel.Status == common.ChannelStatusEnabled {
			state := currentStates[channel.Id][modelName]
			entry.CurrentAvailable, entry.CurrentObservedAt = state.available, state.observedAt
		}
		result.Channels = append(result.Channels, entry)
	}
	return result, nil
}
