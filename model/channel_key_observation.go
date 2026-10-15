package model

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/modelmapping"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChannelKeyObservation contains no credential. Its HMAC primary key binds an
// observation to a physical credential and relay configuration, not its index.
// Reordered keys retain their results; replaced credentials never inherit them.
type ChannelKeyObservation struct {
	ID         string `json:"-" gorm:"primaryKey;size:64"`
	ChannelID  int    `json:"-" gorm:"index:idx_channel_key_model,priority:1"`
	ModelName  string `json:"-" gorm:"size:128;index:idx_channel_key_model,priority:2"`
	ObservedAt int64  `json:"-" gorm:"type:bigint;not null"`
	Success    bool   `json:"-" gorm:"not null"`
	StatusCode int    `json:"-"`
	ErrorText  string `json:"-" gorm:"type:text"`
	Source     string `json:"-" gorm:"size:16"`
}

// ChannelKeyObservationID is independent of key ordering and routing groups.
// It shares the balance monitor's keyed account fingerprint, then also binds
// relay-specific options and this model's effective upstream mapping. Changes
// to unrelated models and discovery timestamps cannot invalidate observations.
func ChannelKeyObservationID(channel *Channel, modelName, key string) (string, error) {
	return channelKeyObservationID(channel, modelName, key, false)
}

// LegacyChannelKeyObservationID matches observations written before per-model
// mapping identities. Only the current complete configuration may match a
// legacy observation; it must never recover results from a different account.
func LegacyChannelKeyObservationID(channel *Channel, modelName, key string) (string, error) {
	return channelKeyObservationID(channel, modelName, key, true)
}

func channelKeyObservationID(channel *Channel, modelName, key string, legacy bool) (string, error) {
	if channel == nil || channel.Id <= 0 || strings.TrimSpace(modelName) == "" || utf8.RuneCountInString(modelName) > 128 || key == "" {
		return "", errors.New("invalid channel key observation")
	}
	mapping := channel.ModelMapping
	namespace := "channel-key-observation:v1:"
	if !legacy {
		rawMapping := ""
		if mapping != nil {
			rawMapping = *mapping
		}
		upstreamModel, _, err := modelmapping.ResolveMappedModel(rawMapping, modelName)
		if err != nil {
			return "", errors.New("invalid channel model mapping")
		}
		mapping = &upstreamModel
		namespace = "channel-key-observation:v2:"
	}
	snapshot := *channel
	snapshot.Key, snapshot.Keys = key, nil
	snapshot.ChannelInfo = ChannelInfo{}
	accountHash, err := channelBalanceConfigurationHash(&snapshot)
	if err != nil {
		return "", errors.New("invalid channel key configuration")
	}
	setting := dto.ChannelSettings{}
	if channel.Setting != nil && *channel.Setting != "" {
		if err := common.UnmarshalJsonStr(*channel.Setting, &setting); err != nil {
			return "", errors.New("invalid channel key configuration")
		}
	}
	setting.BalanceQueryDisabled = false
	other := dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" {
		if err := common.UnmarshalJsonStr(channel.OtherSettings, &other); err != nil {
			return "", errors.New("invalid channel key configuration")
		}
	}
	other.UpstreamModelUpdateCheckEnabled, other.UpstreamModelUpdateAutoSyncEnabled = false, false
	other.UpstreamModelUpdateLastCheckTime = 0
	other.UpstreamModelUpdateLastDetectedModels, other.UpstreamModelUpdateLastRemovedModels, other.UpstreamModelUpdateIgnoredModels = nil, nil, nil
	configuration := struct {
		ChannelID                         int
		Model, AccountHash, Other         string
		Organization, Mapping, Parameters *string
		Setting                           dto.ChannelSettings
		OtherSetting                      dto.ChannelOtherSettings
	}{channel.Id, modelName, accountHash, channel.Other, channel.OpenAIOrganization, mapping, channel.ParamOverride, setting, other}
	encoded, err := common.Marshal(configuration)
	if err != nil {
		return "", errors.New("invalid channel key configuration")
	}
	return common.GenerateHMAC(namespace + string(encoded)), nil
}

var channelObservationAuthPattern = regexp.MustCompile(`(?i)(authorization|api[-_]?key|access[-_]?token|secret|password)(["'\s:=]+)([^\s,"'}]+)`)
var channelObservationBearerPattern = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[^\s,"'}]+`)

// SanitizeChannelObservationError removes the actual credentials before generic
// masking. This also handles JSON service accounts and header overrides, whose
// secrets need not use an sk- prefix. Never persist raw upstream response bodies.
func SanitizeChannelObservationError(channel *Channel, key, message string) string {
	secrets := []string{key}
	if channel != nil {
		snapshot := *channel
		snapshot.Keys = nil
		secrets = append(secrets, snapshot.GetKeys()...)
		if channel.HeaderOverride != nil {
			secrets = append(secrets, *channel.HeaderOverride)
		}
		if channel.Setting != nil {
			secrets = append(secrets, *channel.Setting)
		}
		secrets = append(secrets, channel.OtherSettings)
	}
	// Composite credentials can be echoed one field at a time by providers.
	var values []any
	for _, secret := range secrets {
		var value any
		if common.UnmarshalJsonStr(secret, &value) == nil {
			values = append(values, value)
		}
	}
	for len(values) > 0 {
		value := values[len(values)-1]
		values = values[:len(values)-1]
		switch value := value.(type) {
		case string:
			secrets = append(secrets, value)
		case map[string]any:
			for _, child := range value {
				values = append(values, child)
			}
		case []any:
			values = append(values, value...)
		}
	}
	// AWS and similar composite credentials contain independently usable parts.
	for _, part := range strings.Split(key, "|") {
		secrets = append(secrets, part)
	}
	for _, secret := range append([]string(nil), secrets...) {
		if strings.Contains(secret, "{api_key}") {
			secrets = append(secrets, strings.ReplaceAll(secret, "{api_key}", key))
		}
		fields := strings.Fields(secret)
		if len(fields) == 2 && (strings.EqualFold(fields[0], "Bearer") || strings.EqualFold(fields[0], "Basic")) {
			secrets = append(secrets, fields[1])
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
		message = strings.ReplaceAll(message, url.QueryEscape(secret), "[redacted]")
		encoded, _ := common.Marshal(secret)
		if len(encoded) > 2 {
			message = strings.ReplaceAll(message, string(encoded[1:len(encoded)-1]), "[redacted]")
		}
	}
	message = channelObservationBearerPattern.ReplaceAllString(message, "$1 [redacted]")
	message = channelObservationAuthPattern.ReplaceAllString(message, "$1$2[redacted]")
	message = common.MaskSensitiveInfo(message)
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	runes := []rune(strings.TrimSpace(message))
	if len(runes) > 1024 {
		runes = append(runes[:1024], '…')
	}
	return string(runes)
}

func RecordChannelKeyObservation(channel *Channel, modelName, key string, observedAt int64, success *bool, statusCode int, errorMessage, source string) error {
	if success == nil {
		return nil
	}
	id, err := ChannelKeyObservationID(channel, modelName, key)
	if err != nil {
		return err
	}
	if DB == nil {
		return errors.New("channel observation database is unavailable")
	}
	if observedAt <= 0 || (source != "request" && source != "probe") {
		return errors.New("invalid channel key observation")
	}
	if *success {
		errorMessage = ""
	}
	row := ChannelKeyObservation{ID: id, ChannelID: channel.Id, ModelName: modelName, ObservedAt: observedAt, Success: *success, StatusCode: statusCode, ErrorText: SanitizeChannelObservationError(channel, key, errorMessage), Source: source}
	probeModel := channel.probeModelName(modelName)
	probeObservationID, err := ChannelKeyObservationID(channel, probeModel, key)
	if err != nil {
		return err
	}
	stateID := channelProbeStateID(probeObservationID)
	_, stateKnown := channelProbeObservations(channel, probeModel)[stateID]
	committed := []ChannelKeyObservation{row}
	database := DB
	ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
	defer cancel()
	err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous []ChannelKeyObservation
		if !stateKnown {
			// Preserve pre-upgrade probe state before a first in-flight request
			// overwrites the normal observation. Once dedicated state exists,
			// the cache avoids this compatibility read on the hot path.
			legacyID, _ := LegacyChannelKeyObservationID(channel, probeModel, key)
			if err := tx.Where("id IN ?", []string{id, probeObservationID, legacyID}).Find(&previous).Error; err != nil {
				return err
			}
		}
		if err := upsertChannelKeyObservation(tx, row, false); err != nil {
			return err
		}
		for _, existing := range previous {
			if existing.Source != "probe" && !(existing.Source == "request" && existing.Success) {
				continue
			}
			existing.ID, existing.ModelName, existing.Source = stateID, probeModel, channelProbeStateSource
			if err := upsertChannelKeyObservation(tx, existing, existing.Success); err != nil {
				return err
			}
			committed = append(committed, existing)
		}
		if source != "probe" && !row.Success {
			return nil
		}
		// Keep probe/recovery state independent from newer request failures.
		// Both rows commit together so cache invalidation never exposes a
		// partially recorded result. Ordinary monitoring retains its raw name.
		row.ID, row.ModelName, row.Source = stateID, probeModel, channelProbeStateSource
		committed = append(committed, row)
		return upsertChannelKeyObservation(tx, row, row.Success)
	})
	if err == nil {
		cacheCommittedChannelObservations(database, committed)
	}
	return err
}

func upsertChannelKeyObservation(tx *gorm.DB, row ChannelKeyObservation, strictlyNewer bool) error {
	comparison := "<="
	if strictlyNewer {
		// A successful request must be newer than a failed probe to recover;
		// failure wins an equal-millisecond tie regardless of arrival order.
		comparison = "<"
	}
	values := []struct {
		column string
		value  any
	}{
		{"error_text", row.ErrorText}, {"source", row.Source},
		{"status_code", row.StatusCode}, {"success", row.Success},
		{"observed_at", row.ObservedAt},
	}
	updates := make(clause.Set, 0, len(values))
	for _, value := range values {
		updates = append(updates, clause.Assignment{
			Column: clause.Column{Name: value.column},
			Value:  gorm.Expr("CASE WHEN channel_key_observations.observed_at "+comparison+" ? THEN ? ELSE channel_key_observations."+value.column+" END", row.ObservedAt, value.value),
		})
	}
	// MySQL evaluates assignments left-to-right. Updating observed_at last
	// keeps every conditional assignment against the original timestamp.
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: updates}).Create(&row).Error
}

func GetChannelKeyObservations(channelIDs []int, modelName string) ([]ChannelKeyObservation, error) {
	rows := []ChannelKeyObservation{}
	if len(channelIDs) == 0 {
		return rows, nil
	}
	if DB == nil {
		return nil, errors.New("channel observation database is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
	defer cancel()
	err := DB.WithContext(ctx).Where("channel_id IN ? AND model_name = ?", channelIDs, modelName).
		Where("source <> ? OR source IS NULL", channelProbeStateSource).Find(&rows).Error
	return rows, err
}
