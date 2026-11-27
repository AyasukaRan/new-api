package model

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm/clause"
)

type SatelliteSyncNonce struct {
	NodeID    string `gorm:"primaryKey;size:48"`
	Nonce     string `gorm:"primaryKey;size:48"`
	CreatedAt int64  `gorm:"index"`
}

func ClaimSatelliteNonce(ctx context.Context, nodeID, nonce string, now int64) error {
	result := DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&SatelliteSyncNonce{NodeID: nodeID, Nonce: nonce, CreatedAt: now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("satellite nonce already consumed")
	}
	// Nonces outlive the complete accepted timestamp window.
	if err := DB.WithContext(ctx).Where("created_at < ?", now-300).Delete(&SatelliteSyncNonce{}).Error; err != nil {
		common.SysError("satellite nonce cleanup failed")
	}
	return nil
}
