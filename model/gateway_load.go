package model

import "gorm.io/gorm/clause"

// Each process owns its row, so re-flushing a complete snapshot is idempotent
// and restarting a worker never overwrites another worker's samples.
type GatewayLoadHour struct {
	BucketTs        int64  `gorm:"primaryKey;autoIncrement:false"`
	InstanceID      string `gorm:"primaryKey;size:32"`
	ObservedFrom    int64
	PeakConcurrency int64
	LatencyCounts   []byte
}

func SaveGatewayLoadHour(row *GatewayLoadHour) error {
	return DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "bucket_ts"}, {Name: "instance_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"observed_from", "peak_concurrency", "latency_counts"}),
	}).Create(row).Error
}

// Scan one hour/worker at a time; a busy week need not be held in memory.
func WalkGatewayLoadHours(start, end int64, visit func(GatewayLoadHour) error) error {
	rows, err := DB.Model(&GatewayLoadHour{}).
		Select("bucket_ts, instance_id, observed_from, peak_concurrency, latency_counts").
		Where("bucket_ts >= ? AND bucket_ts <= ?", start, end).Order("bucket_ts").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row GatewayLoadHour
		if err := rows.Scan(&row.BucketTs, &row.InstanceID, &row.ObservedFrom, &row.PeakConcurrency, &row.LatencyCounts); err != nil {
			return err
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

func DeleteGatewayLoadHoursBefore(cutoff int64) error {
	return DB.Where("bucket_ts < ?", cutoff).Delete(&GatewayLoadHour{}).Error
}
