package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Imported options tables can lack their original primary key. Repair must
// preserve configuration and retain the original rows for recovery.
func TestOptionPrimaryKeyMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
			if dialect == "sqlite" {
				dsn = "local"
				previousPath := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "options.db")
				t.Cleanup(func() { common.SQLitePath = previousPath })
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("OPTIONS_MIGRATION_TEST_DSN", dsn)
			db, _, err := chooseDB("OPTIONS_MIGRATION_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.False(t, db.Migrator().HasTable(&Option{}), "use an isolated migration test database")
			t.Cleanup(func() {
				tables, err := db.Migrator().GetTables()
				require.NoError(t, err)
				for _, table := range tables {
					if table == "options" || strings.HasPrefix(table, optionLegacyTablePrefix) {
						require.NoError(t, db.Migrator().DropTable(table))
					}
				}
			})

			t.Run("fresh", func(t *testing.T) {
				require.NoError(t, migrateOptionPrimaryKey(db))
				require.NoError(t, db.AutoMigrate(&Option{}))
				require.NoError(t, db.Create(&Option{Key: "SystemName", Value: "preserved"}).Error)
				recorder := &migrationSQLRecorder{}
				for range 2 {
					require.NoError(t, migrateOptionPrimaryKey(db.Session(&gorm.Session{Logger: recorder})))
					require.NoError(t, db.Session(&gorm.Session{Logger: recorder}).AutoMigrate(&Option{}))
				}
				assert.Empty(t, recorder.schemaMutations())
				assert.Error(t, db.Create(&Option{Key: "SystemName", Value: "duplicate"}).Error)
				require.NoError(t, db.Migrator().DropTable(&Option{}))
			})

			t.Run("legacy_without_primary_key", func(t *testing.T) {
				// SQL_MAX_OPEN_CONNS=1 is supported; the advisory lock must not
				// retain the only connection while repair waits for another one.
				sqlDB.SetMaxOpenConns(1)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				db := db.WithContext(ctx)
				type importedOption struct {
					Key   string `gorm:"size:191"`
					Value string `gorm:"type:text"`
				}
				require.NoError(t, db.Table("options").Migrator().CreateTable(&importedOption{}))
				rows := []importedOption{{"SystemName", "retained"}, {"SystemName", "retained"}, {"ModelRatio", `{"custom":1.5}`}, {"", "invalid"}}
				require.NoError(t, db.Table("options").Create(&rows).Error)
				require.NoError(t, migrateOptionPrimaryKey(db))
				var saved []Option
				require.NoError(t, db.Order("value").Find(&saved).Error)
				assert.ElementsMatch(t, []Option{{Key: "SystemName", Value: "retained"}, {Key: "ModelRatio", Value: `{"custom":1.5}`}}, saved)
				assert.Error(t, db.Create(&Option{Key: "SystemName", Value: "duplicate"}).Error)
				tables, err := db.Migrator().GetTables()
				require.NoError(t, err)
				var backups []string
				for _, table := range tables {
					if strings.HasPrefix(table, optionLegacyTablePrefix) {
						backups = append(backups, table)
					}
				}
				require.Len(t, backups, 1)
				var original []importedOption
				require.NoError(t, db.Table(backups[0]).Find(&original).Error)
				assert.ElementsMatch(t, rows, original)
				recorder := &migrationSQLRecorder{}
				for range 2 {
					require.NoError(t, migrateOptionPrimaryKey(db.Session(&gorm.Session{Logger: recorder})))
					require.NoError(t, db.Session(&gorm.Session{Logger: recorder}).AutoMigrate(&Option{}))
				}
				assert.Empty(t, recorder.schemaMutations())
			})
		})
	}
}
