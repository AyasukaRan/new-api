package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The database handle is set only after both invalidation triggers are installed.
// A replica can serve accurate reports while the master is still migrating.
var dashboardFinanceDB atomic.Pointer[gorm.DB]
var dashboardFinanceWriter atomic.Pointer[gorm.DB]

// This key can never be an hourly bucket. Its presence means that old facts
// were invalidated after both triggers became available.
const financeRollupReadyHour int64 = -1

func DashboardFinanceRollupEnabled() bool {
	return LOG_DB != nil && dashboardFinanceDB.Load() == LOG_DB
}

func dashboardFinanceTriggers() ([]string, error) {
	var query string
	switch common.LogDatabaseType() {
	case common.DatabaseTypeSQLite:
		query = "SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='logs' AND name IN ?"
	case common.DatabaseTypeMySQL:
		query = "SELECT trigger_name FROM information_schema.triggers WHERE trigger_schema=DATABASE() AND event_object_table='logs' AND trigger_name IN ?"
	case common.DatabaseTypePostgreSQL:
		query = "SELECT tgname FROM pg_trigger WHERE tgrelid=to_regclass('logs') AND NOT tgisinternal AND tgname IN ?"
	default:
		return nil, nil
	}
	var names []string
	err := LOG_DB.Raw(query, []string{"dashboard_finance_log_update_v1", "dashboard_finance_log_delete_v1"}).Scan(&names).Error
	return names, err
}

func financeRollupReady() (bool, error) {
	var count int64
	err := LOG_DB.Model(&DashboardFinanceHour{}).Where("hour = ?", financeRollupReadyHour).Count(&count).Error
	return count == 1, err
}

func detectDashboardFinanceRollup() {
	dashboardFinanceDB.Store(nil)
	dashboardFinanceWriter.Store(nil)
	names, err := dashboardFinanceTriggers()
	if err != nil {
		common.SysError(common.LogText("failed to verify finance triggers; using raw logs: %s", err.Error()))
		return
	}
	if len(names) == 2 && LOG_DB.Migrator().HasTable(&DashboardFinanceHour{}) && LOG_DB.Migrator().HasTable(&DashboardFinanceEntry{}) {
		ready, err := financeRollupReady()
		if err == nil && ready {
			dashboardFinanceDB.Store(LOG_DB)
		}
	}
}

// MigrateDashboardFinance runs on the log database after logs exists. Direct
// historical SQL repairs must invalidate the same projections as application
// edits, in the transaction that changed the authoritative log. Append traffic
// has no trigger: its watermark is advanced in batches by the projector.
func MigrateDashboardFinance() error {
	dashboardFinanceDB.Store(nil)
	dashboardFinanceWriter.Store(nil)
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return nil
	}
	err := migrateDashboardFinanceRollup()
	if err == nil {
		dashboardFinanceWriter.Store(LOG_DB)
		dashboardFinanceDB.Store(LOG_DB)
		return nil
	}
	// Database-scoped MySQL accounts may migrate tables but lack TRIGGER, or
	// the server may require global privileges when binary logging is enabled.
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && slices.Contains([]uint16{1044, 1142, 1143, 1227, 1419}, mysqlErr.Number) {
		common.SysError(common.LogText("finance rollup unavailable; using raw logs: %s", err.Error()))
		return nil
	}
	return err
}

func migrateDashboardFinanceRollup() error {
	if err := LOG_DB.AutoMigrate(&DashboardFinanceHour{}, &DashboardFinanceEntry{}); err != nil {
		return err
	}
	installed, err := dashboardFinanceTriggers()
	if err != nil {
		return err
	}
	if len(installed) == 2 {
		ready, err := financeRollupReady()
		if err != nil || ready {
			return err
		}
	} else {
		// Persist the incomplete state across a crash between trigger DDL
		// and invalidation. A replica never enables a half-installed rollup.
		if err := LOG_DB.Where("hour = ?", financeRollupReadyHour).Delete(&DashboardFinanceHour{}).Error; err != nil {
			return err
		}
	}
	// MySQL 5.7 cannot prepare CREATE TRIGGER even when the application's
	// regular query pool uses prepared statements. All DDL fragments below
	// are constants or generated from this fixed schema.
	conn, err := LOG_DB.DB()
	if err != nil {
		return err
	}
	// Only these log fields affect source inclusion, dimensions or a cost fact.
	fields := []string{"id", "created_at", "type", "user_id", "username", "model_name", "token_id", "token_name", "channel_id", "group", "content", "quota", "other"}
	dbType := common.LogDatabaseType()
	changed := make([]string, 0, len(fields))
	for i, name := range fields {
		if name == "group" {
			if dbType == common.DatabaseTypeMySQL {
				name = "`group`"
			} else {
				name = `"group"`
			}
			fields[i] = name
		}
		switch dbType {
		case common.DatabaseTypePostgreSQL:
			changed = append(changed, "OLD."+name+" IS DISTINCT FROM NEW."+name)
		case common.DatabaseTypeMySQL:
			// MySQL's usual case-insensitive text collation would miss a label
			// correction differing only in case.
			changed = append(changed, "NOT (BINARY OLD."+name+" <=> BINARY NEW."+name+")")
		case common.DatabaseTypeSQLite:
			changed = append(changed, "OLD."+name+" IS NOT NEW."+name)
		default:
			return fmt.Errorf("unsupported finance projection dialect %s", dbType)
		}
	}
	updates := strings.Join(changed, " OR ")
	const oldHour = "(OLD.created_at - (OLD.created_at % 3600))"
	const newHour = "(NEW.created_at - (NEW.created_at % 3600))"
	// Create a revision even when the worker has not seen this hour yet. A
	// simultaneous historical edit can then never be overwritten by a stale scan.
	mark := func(hour, condition string) string {
		sql := "INSERT INTO dashboard_finance_hours (hour,revision,log_count,last_id) SELECT " + hour + ",1,-1,0 WHERE " + condition + " "
		if dbType == common.DatabaseTypeMySQL {
			return sql + "ON DUPLICATE KEY UPDATE revision=revision+1,log_count=-1,last_id=0,payload=NULL;"
		}
		return sql + "ON CONFLICT (hour) DO UPDATE SET revision=dashboard_finance_hours.revision+1, log_count=-1,last_id=0,payload=NULL;"
	}
	markOld := mark(oldHour, "OLD.type=2")
	markNew := mark(newHour, "NEW.type=2 AND (COALESCE(OLD.type,0)<>2 OR "+oldHour+"<>"+newHour+")")
	const dropEntry = "DELETE FROM dashboard_finance_entries WHERE log_id=OLD.id;"
	// Acquire the hour revision before touching its entries, in the same order
	// as the projector.
	for _, event := range []string{"UPDATE", "DELETE"} {
		name := "dashboard_finance_log_" + strings.ToLower(event) + "_v1"
		if slices.Contains(installed, name) {
			continue
		}
		condition := "OLD.type=2"
		body := markOld
		if event == "UPDATE" {
			condition = "(OLD.type=2 OR NEW.type=2) AND (" + updates + ")"
			body += markNew
		}
		body += dropEntry
		switch dbType {
		case common.DatabaseTypeSQLite:
			sqlEvent := event
			if event == "UPDATE" {
				sqlEvent += " OF " + strings.Join(fields, ",")
			}
			if _, err := conn.ExecContext(context.Background(), "CREATE TRIGGER IF NOT EXISTS "+name+" AFTER "+sqlEvent+" ON logs FOR EACH ROW WHEN "+condition+" BEGIN "+body+" END"); err != nil {
				return err
			}
		case common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL:
			if dbType == common.DatabaseTypeMySQL {
				if _, err := conn.ExecContext(context.Background(), "CREATE TRIGGER "+name+" AFTER "+event+" ON logs FOR EACH ROW BEGIN IF "+condition+" THEN "+body+" END IF; END"); err != nil {
					return err
				}
			} else {
				if _, err := conn.ExecContext(context.Background(), "CREATE OR REPLACE FUNCTION "+name+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN "+body+" RETURN NULL; END; $$"); err != nil {
					return err
				}
				sqlEvent := event
				if event == "UPDATE" {
					sqlEvent += " OF " + strings.Join(fields, ",")
				}
				if _, err := conn.ExecContext(context.Background(), "CREATE TRIGGER "+name+" AFTER "+sqlEvent+" ON logs FOR EACH ROW WHEN ("+condition+") EXECUTE PROCEDURE "+name+"()"); err != nil {
					return err
				}
			}
		}
	}
	// Never publish facts built without both triggers. Advancing the existing
	// revision also prevents an in-flight projector from publishing an old batch.
	return LOG_DB.Transaction(func(tx *gorm.DB) error {
		tx = tx.Session(&gorm.Session{AllowGlobalUpdate: true})
		if err := tx.Model(&DashboardFinanceHour{}).Where("hour <> ?", financeRollupReadyHour).Updates(map[string]any{
			"revision": gorm.Expr("revision + 1"), "log_count": -1, "last_id": 0, "payload": nil,
		}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&DashboardFinanceEntry{}).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&DashboardFinanceHour{Hour: financeRollupReadyHour}).Error
	})
}
