package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var sqliteDatabaseOperationLocks sync.Map
var errDatabaseOperationLockTimeout = errors.New("timed out waiting for a database operation lock")

// WithSatelliteSyncLock orders complete sync exchanges across overlapping node
// processes on PostgreSQL/MySQL. SQLite supports a single gateway process only.
func WithSatelliteSyncLock(ctx context.Context, operation func() error) error {
	return withDatabaseOperationLock(DB, ctx, "satellite-sync", common.SatelliteNodeID(), operation)
}

func withDatabaseOperationLock(db *gorm.DB, ctx context.Context, namespace, key string, operation func() error) error {
	digest := sha256.Sum256([]byte("new-api:" + namespace + ":" + key))
	name := namespace + ":" + hex.EncodeToString(digest[:24])
	switch {
	case common.UsingMainDatabase(common.DatabaseTypeMySQL):
		wait := 0
		if deadline, ok := ctx.Deadline(); ok {
			wait = max(int(time.Until(deadline)/time.Second), 0)
		}
		return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var acquired sql.NullInt64
			if err := conn.Raw("SELECT GET_LOCK(?, ?)", name, wait).Row().Scan(&acquired); err != nil {
				return err
			}
			if acquired.Int64 != 1 {
				return errDatabaseOperationLockTimeout
			}
			defer conn.WithContext(context.Background()).Exec("DO RELEASE_LOCK(?)", name)
			return operation()
		})
	case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
		advisoryKey := int64(binary.BigEndian.Uint64(digest[:8]))
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", advisoryKey).Error; err != nil {
				return err
			}
			return operation()
		})
	default:
		value, _ := sqliteDatabaseOperationLocks.LoadOrStore(name, make(chan struct{}, 1))
		lock := value.(chan struct{})
		select {
		case lock <- struct{}{}:
		case <-ctx.Done():
			return errDatabaseOperationLockTimeout
		}
		defer func() { <-lock }()
		return operation()
	}
}
