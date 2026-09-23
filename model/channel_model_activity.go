package model

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChannelModelActivity is durable scheduling state, independent of optional
// performance metrics. Times are Unix milliseconds and routes ignore groups
// and credential indexes because either can serve the same channel/model.
type ChannelModelActivity struct {
	ChannelID          int    `gorm:"primaryKey;autoIncrement:false"`
	ModelName          string `gorm:"size:128;primaryKey"`
	LastRequestAt      int64  `gorm:"type:bigint;not null"`
	LastProbeAt        int64  `gorm:"type:bigint;not null"`
	RequestActiveUntil int64  `gorm:"type:bigint;not null"`
	LastResultAt       int64  `gorm:"type:bigint;not null"`
	LastResultSuccess  bool   `gorm:"not null"`
}

const channelActivityDatabaseTimeout = 2 * time.Second

func RecordChannelModelRequest(channelID int, modelName string, at, activeUntil int64, success *bool) error {
	row := ChannelModelActivity{ChannelID: channelID, ModelName: modelName, LastRequestAt: at, RequestActiveUntil: activeUntil}
	updates := map[string]any{
		"last_request_at":      gorm.Expr("CASE WHEN channel_model_activities.last_request_at < ? THEN ? ELSE channel_model_activities.last_request_at END", at, at),
		"request_active_until": gorm.Expr("CASE WHEN channel_model_activities.request_active_until < ? THEN ? ELSE channel_model_activities.request_active_until END", activeUntil, activeUntil),
	}
	return recordChannelModelActivity(row, at, success, updates)
}

func RecordChannelModelProbe(channelID int, modelName string, at int64, success *bool) error {
	row := ChannelModelActivity{ChannelID: channelID, ModelName: modelName, LastProbeAt: at}
	updates := map[string]any{
		"last_probe_at": gorm.Expr("CASE WHEN channel_model_activities.last_probe_at < ? THEN ? ELSE channel_model_activities.last_probe_at END", at, at),
	}
	return recordChannelModelActivity(row, at, success, updates)
}

func recordChannelModelActivity(row ChannelModelActivity, at int64, success *bool, updates map[string]any) error {
	if DB == nil {
		return errors.New("channel activity database is unavailable")
	}
	if row.ChannelID <= 0 || strings.TrimSpace(row.ModelName) == "" || utf8.RuneCountInString(row.ModelName) > 128 || at <= 0 || row.RequestActiveUntil < 0 {
		return errors.New("invalid channel model activity")
	}
	if success != nil {
		row.LastResultAt, row.LastResultSuccess = at, *success
		updates["last_result_at"] = gorm.Expr("CASE WHEN channel_model_activities.last_result_at < ? THEN ? ELSE channel_model_activities.last_result_at END", at, at)
		// <= also works with MySQL's left-to-right assignment evaluation:
		// last_result_at may already have advanced to this observation's time.
		updates["last_result_success"] = gorm.Expr("CASE WHEN channel_model_activities.last_result_at <= ? THEN ? ELSE channel_model_activities.last_result_success END", at, *success)
	}
	ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
	defer cancel()
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "model_name"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&row).Error
}

func GetChannelModelActivities(channelIDs []int) ([]ChannelModelActivity, error) {
	rows := []ChannelModelActivity{}
	if len(channelIDs) == 0 {
		return rows, nil
	}
	if DB == nil {
		return nil, errors.New("channel activity database is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
	defer cancel()
	err := DB.WithContext(ctx).Where("channel_id IN ?", channelIDs).Order("channel_id ASC, model_name ASC").Find(&rows).Error
	return rows, err
}

func GetChannelModelActivity(channelID int, modelName string) (ChannelModelActivity, error) {
	var row ChannelModelActivity
	if DB == nil {
		return row, errors.New("channel activity database is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), channelActivityDatabaseTimeout)
	defer cancel()
	err := DB.WithContext(ctx).Where("channel_id = ? AND model_name = ?", channelID, modelName).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ChannelModelActivity{}, nil
	}
	return row, err
}
