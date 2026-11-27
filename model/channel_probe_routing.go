package model

import (
	"cmp"
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/modelmapping"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

const ErrorCodeChannelProbeUnavailable types.ErrorCode = "channel_probe_unavailable"

const channelProbeStateSource = "probe_state"

// ChannelProbeError contains only the sanitized observation, never a channel
// name, credential, or configuration digest. StatusCode is the upstream code;
// rejecting a request locally reports service unavailable instead.
type ChannelProbeError struct {
	ObservedAt int64
	StatusCode int
	Message    string
}

func (e *ChannelProbeError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "channel is unavailable according to its last probe"
}

func (e *ChannelProbeError) NewAPIError() *types.NewAPIError {
	return types.NewError(e, ErrorCodeChannelProbeUnavailable,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithStatusCode(http.StatusServiceUnavailable))
}

type channelProbeModelObservations struct {
	expires time.Time
	rows    map[string]ChannelKeyObservation
}

type channelProbeRoutingEntry struct {
	mu       sync.Mutex
	database *gorm.DB
	models   map[string]channelProbeModelObservations
}

var channelProbeRoutingCache sync.Map

func InvalidateChannelProbeRouting(channelID int) {
	if value, ok := channelProbeRoutingCache.Load(channelID); ok {
		entry := value.(*channelProbeRoutingEntry)
		entry.mu.Lock()
		entry.models = nil
		entry.mu.Unlock()
	}
}

func channelProbeStateID(observationID string) string {
	return common.GenerateHMAC("channel-probe-state:v1:" + observationID)
}

// Match the configured model actually used by routing. An explicitly configured
// alias remains independent, as does a modifier with a different upstream map.
func (channel *Channel) probeModelName(modelName string) string {
	models := channel.GetModels()
	if slices.Contains(models, modelName) {
		return modelName
	}
	normalized := ratio_setting.RoutingMatchModelName(modelName)
	if normalized == modelName || !slices.Contains(models, normalized) {
		return modelName
	}
	mapping := ""
	if channel.ModelMapping != nil {
		mapping = *channel.ModelMapping
	}
	originalUpstream, _, originalErr := modelmapping.ResolveMappedModel(mapping, modelName)
	normalizedUpstream, _, normalizedErr := modelmapping.ResolveMappedModel(mapping, normalized)
	if originalErr != nil || normalizedErr != nil || ratio_setting.RoutingMatchModelName(originalUpstream) != ratio_setting.RoutingMatchModelName(normalizedUpstream) {
		return modelName
	}
	return normalized
}

func channelProbeObservations(channel *Channel, modelName string) map[string]ChannelKeyObservation {
	if channel.Id <= 0 || modelName == "" || DB == nil {
		return nil
	}
	value, _ := channelProbeRoutingCache.LoadOrStore(channel.Id, &channelProbeRoutingEntry{})
	entry := value.(*channelProbeRoutingEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.database != DB || entry.models == nil {
		entry.database = DB
		entry.models = make(map[string]channelProbeModelObservations)
	}
	cached := entry.models[modelName]
	if !time.Now().Before(cached.expires) {
		ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
		defer cancel()
		var observations []ChannelKeyObservation
		if err := DB.WithContext(ctx).Where("channel_id = ? AND model_name = ?", channel.Id, modelName).Find(&observations).Error; err == nil {
			cached.rows = make(map[string]ChannelKeyObservation, len(observations))
			for _, observation := range observations {
				cached.rows[observation.ID] = observation
			}
		}
		// This bounds cross-instance refresh only. A failure stays blocked until
		// a newer success or a credential/configuration change establishes recovery.
		cached.expires = time.Now().Add(15 * time.Second)
		// An explicitly pinned request can name an arbitrary model. Do not
		// retain unlimited unknown names; configured models still cache misses.
		_, alreadyCached := entry.models[modelName]
		if (alreadyCached || len(entry.models) < 256) && (len(cached.rows) > 0 || slices.Contains(channel.GetModels(), modelName)) {
			entry.models[modelName] = cached
		}
	}
	return cached.rows
}

func cacheCommittedChannelObservations(database *gorm.DB, observations []ChannelKeyObservation) {
	if len(observations) == 0 {
		return
	}
	value, ok := channelProbeRoutingCache.Load(observations[0].ChannelID)
	if !ok {
		return
	}
	entry := value.(*channelProbeRoutingEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.database != database {
		return
	}
	for _, row := range observations {
		cached, ok := entry.models[row.ModelName]
		if !ok {
			continue
		}
		previous, exists := cached.rows[row.ID]
		if exists && (previous.ObservedAt > row.ObservedAt || (row.Source == channelProbeStateSource && row.Success && previous.ObservedAt == row.ObservedAt)) {
			continue
		}
		// Readers hold immutable snapshots after releasing the entry lock.
		updated := make(map[string]ChannelKeyObservation, len(cached.rows)+1)
		maps.Copy(updated, cached.rows)
		updated[row.ID] = row
		cached.rows = updated
		entry.models[row.ModelName] = cached
	}
}

func channelProbeFailureForKey(channel *Channel, modelName, key string, rows map[string]ChannelKeyObservation) *ChannelProbeError {
	id, err := ChannelKeyObservationID(channel, modelName, key)
	if err != nil {
		return nil
	}
	if state, exists := rows[channelProbeStateID(id)]; exists && channel.ChannelInfo.MultiKeyMode.UsesKeyFailover() {
		// The state uses the start of successful attempts; a later diagnostic
		// completion timestamp must not bypass its recovery ordering.
		if state.Success {
			return nil
		}
		return &ChannelProbeError{ObservedAt: state.ObservedAt, StatusCode: state.StatusCode, Message: state.ErrorText}
	}
	legacyID, _ := LegacyChannelKeyObservationID(channel, modelName, key)
	var latest *ChannelKeyObservation
	// Include old observations for upgrades and mixed-version deployments.
	// Normal failures cannot overwrite a probe's state or undo later recovery.
	for _, candidateID := range []string{channelProbeStateID(id), id, legacyID} {
		row, ok := rows[candidateID]
		if !ok || (row.Source != channelProbeStateSource && row.Source != "probe" && !(row.Source == "request" && row.Success)) {
			continue
		}
		if latest == nil || row.ObservedAt > latest.ObservedAt || (row.ObservedAt == latest.ObservedAt && !row.Success) {
			latest = &row
		}
	}
	if latest == nil || latest.Success {
		return nil
	}
	return &ChannelProbeError{ObservedAt: latest.ObservedAt, StatusCode: latest.StatusCode, Message: latest.ErrorText}
}

type ChannelKeyRecoveryCandidate struct {
	Channel     *Channel
	ModelName   string
	KeyIndex    int
	KeyIdentity string
	ObservedAt  int64
}

// GetChannelKeyRecoveryCandidates reads durable state, so a busy healthy key
// cannot postpone recovery of another key and another node can run the probe.
func GetChannelKeyRecoveryCandidates(now time.Time, interval time.Duration) ([]ChannelKeyRecoveryCandidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
	defer cancel()
	var channels []*Channel
	if err := DB.WithContext(ctx).Where("status = ?", common.ChannelStatusEnabled).Order("id ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	ids := make([]int, 0)
	for _, channel := range channels {
		if channel.ChannelInfo.IsMultiKey && channel.ChannelInfo.MultiKeyMode.UsesKeyFailover() {
			ids = append(ids, channel.Id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []ChannelKeyObservation
	if err := DB.WithContext(ctx).Where("channel_id IN ? AND source = ? AND success = ? AND observed_at <= ?", ids, channelProbeStateSource, false, now.Add(-interval).UnixMilli()).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	byChannel := make(map[int]map[string]map[string]ChannelKeyObservation)
	for _, row := range rows {
		if byChannel[row.ChannelID] == nil {
			byChannel[row.ChannelID] = make(map[string]map[string]ChannelKeyObservation)
		}
		if byChannel[row.ChannelID][row.ModelName] == nil {
			byChannel[row.ChannelID][row.ModelName] = make(map[string]ChannelKeyObservation)
		}
		byChannel[row.ChannelID][row.ModelName][row.ID] = row
	}
	var candidates []ChannelKeyRecoveryCandidate
	for _, channel := range channels {
		if len(byChannel[channel.Id]) == 0 || !channel.ChannelInfo.IsMultiKey || !channel.ChannelInfo.MultiKeyMode.UsesKeyFailover() {
			continue
		}
		for _, name := range channel.GetModels() {
			observations := byChannel[channel.Id][name]
			if len(observations) == 0 {
				continue
			}
			seen := make(map[string]bool)
			for index, key := range channel.GetKeys() {
				identity := channel.KeyIdentity(key)
				if seen[identity] || key == "" {
					continue
				}
				if status, configured := channel.ChannelInfo.MultiKeyStatusList[index]; configured && status != common.ChannelStatusEnabled {
					continue
				}
				seen[identity] = true
				if failure := channelProbeFailureForKey(channel, name, key, observations); failure != nil {
					candidates = append(candidates, ChannelKeyRecoveryCandidate{channel, name, index, identity, failure.ObservedAt})
				}
			}
		}
	}
	// Slow failures move to the back after their completed probe, so a bounded
	// round cannot starve credentials later in the channel/key order.
	slices.SortStableFunc(candidates, func(a, b ChannelKeyRecoveryCandidate) int { return cmp.Compare(a.ObservedAt, b.ObservedAt) })
	return candidates, nil
}

// RelayCredentialProbeError checks a credential already bound to a relay.
// Callers validate that it remains enabled before using a persistent binding.
func (channel *Channel) RelayCredentialProbeError(modelName, key string) *ChannelProbeError {
	if channel == nil {
		return nil
	}
	modelName = channel.probeModelName(modelName)
	return channelProbeFailureForKey(channel, modelName, key, channelProbeObservations(channel, modelName))
}

func (channel *Channel) probeKeyFailures(modelName string) map[int]*ChannelProbeError {
	modelName = channel.probeModelName(modelName)
	rows := channelProbeObservations(channel, modelName)
	if len(rows) == 0 {
		return nil
	}
	keys := []string{channel.Key}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
	}
	failures := make(map[int]*ChannelProbeError)
	for index, key := range keys {
		if failure := channelProbeFailureForKey(channel, modelName, key, rows); failure != nil {
			failures[index] = failure
		}
	}
	return failures
}

func (channel *Channel) relayProbeError(failures map[int]*ChannelProbeError, negativeKeys map[int]bool) *ChannelProbeError {
	if len(failures) == 0 {
		return nil
	}
	if !channel.ChannelInfo.IsMultiKey {
		return failures[0]
	}
	lock := GetChannelPollingLock(channel.Id)
	lock.Lock()
	defer lock.Unlock()
	var latest *ChannelProbeError
	for index := range channel.GetKeys() {
		if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
			continue
		}
		failure := failures[index]
		if failure == nil && !negativeKeys[index] {
			return nil
		}
		if failure != nil && (latest == nil || failure.ObservedAt > latest.ObservedAt) {
			latest = failure
		}
	}
	return latest
}

// RelayProbeError rejects only when no enabled credential remains usable after
// combining probe failures with known negative balances. Batch requests use a
// different upstream operation and do not inherit chat-probe failures.
func (channel *Channel) RelayProbeError(modelName string, filters []dto.ChannelFilter) *ChannelProbeError {
	if channel == nil {
		return nil
	}
	for _, filter := range filters {
		if filter.Kind == dto.FilterBatchCapable {
			return nil
		}
	}
	return channel.relayProbeError(channel.probeKeyFailures(modelName), channelNegativeBalanceKeys(channel))
}
