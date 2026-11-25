package model

import (
	"database/sql"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// The only keys passed here are hardcoded log fields. User input is always bound.
func filterLogMetadataText(tx *gorm.DB, key, value string) *gorm.DB {
	var field string
	switch common.LogDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		field = "new_api_log_text_v1(logs.other, '" + key + "')"
		// Index a bounded prefix so an oversized imported label cannot break writes.
		// Short filters match exactly on the index. Long labels also compare in full.
		tx = tx.Where("left("+field+", 128) = left(CAST(? AS text), 128)", value)
		if len(value) < 128 {
			return tx
		}
	case common.DatabaseTypeMySQL:
		path := "'$." + key + "'"
		raw := "JSON_EXTRACT(logs.other, " + path + ")"
		field = "CASE WHEN JSON_VALID(logs.other) THEN CASE WHEN JSON_TYPE(" + raw + ") = 'STRING' THEN BINARY TRIM(JSON_UNQUOTE(" + raw + ")) END END"
	case common.DatabaseTypeClickHouse:
		field = "trimBoth(JSONExtractString(logs.other, '" + key + "'))"
	default:
		path := "'$." + key + "'"
		field = "CASE WHEN json_valid(logs.other) THEN CASE WHEN json_type(logs.other, " + path + ") = 'text' THEN trim(json_extract(logs.other, " + path + ")) END END"
	}
	return tx.Where(field+" = ?", value)
}

// MigrateLogFilterIndexes indexes existing PostgreSQL records while the previous
// instance keeps writing. Other dialects extract the same labels during the query.
func MigrateLogFilterIndexes() error {
	if !common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		return nil
	}
	return LOG_DB.Connection(func(db *gorm.DB) error {
		// Concurrent index DDL cannot run inside a transaction. Serialize separate
		// migrators on this connection so repair never drops another's active build.
		const lock = "hashtext('new_api_log_filter_indexes_v1')"
		if err := db.Exec("SELECT pg_advisory_lock(" + lock + ")").Error; err != nil {
			return err
		}
		defer db.Exec("SELECT pg_advisory_unlock(" + lock + ")")

		var installed bool
		if err := db.Raw("SELECT to_regprocedure('new_api_log_text_v1(text,text)') IS NOT NULL").Scan(&installed).Error; err != nil {
			return err
		}
		if !installed {
			const extract = `CREATE FUNCTION new_api_log_text_v1(metadata text, field text)
RETURNS text LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE value json;
BEGIN
	IF metadata = '' THEN RETURN NULL; END IF;
	value := metadata::json -> field;
	IF json_typeof(value) = 'string' THEN RETURN btrim(value #>> '{}'); END IF;
	RETURN NULL;
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN RETURN NULL;
END; $$`
			if err := db.Exec(extract).Error; err != nil {
				return err
			}
		}
		if err := db.Raw("SELECT to_regprocedure('new_api_log_transport_v1(text,bigint,boolean)') IS NOT NULL").Scan(&installed).Error; err != nil {
			return err
		}
		if !installed {
			query := `CREATE FUNCTION new_api_log_transport_v1(metadata text, log_type bigint, streaming boolean)
RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE facts jsonb;
BEGIN
	facts := NULLIF(metadata, '')::jsonb;
	RETURN ` + logTransportCaseSQL("log_type", "streaming") + `;
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN
	RETURN CASE WHEN streaming THEN 'unknown' ELSE 'non_streamed' END;
END; $$`
			if err := db.Exec(query).Error; err != nil {
				return err
			}
		}
		if err := db.Raw("SELECT to_regprocedure('new_api_log_result_v1(text,bigint)') IS NOT NULL").Scan(&installed).Error; err != nil {
			return err
		}
		if !installed {
			query := `CREATE FUNCTION new_api_log_result_v1(metadata text, log_type bigint)
RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE facts jsonb;
BEGIN
	facts := NULLIF(metadata, '')::jsonb;
	RETURN ` + logResultCaseSQL("log_type") + `;
EXCEPTION WHEN data_exception OR program_limit_exceeded THEN
	RETURN CASE WHEN log_type = 5 THEN 'failure' ELSE 'success' END;
END; $$`
			if err := db.Exec(query).Error; err != nil {
				return err
			}
		}
		indexes := []struct{ name, expression string }{
			{"idx_logs_reasoning_effort_time_v1", "left(new_api_log_text_v1(other, 'reasoning_effort'), 128)"},
			{"idx_logs_client_tool_time_v1", "left(new_api_log_text_v1(other, 'client_tool'), 128)"},
			{"idx_logs_transport_time_v1", "new_api_log_transport_v1(other, type, is_stream)"},
			{"idx_logs_request_path_time_v1", "left(split_part(new_api_log_text_v1(other, 'request_path'), chr(63), 1), 128)"},
		}
		for _, index := range indexes {
			name := index.name
			var valid sql.NullBool
			if err := db.Raw("SELECT (SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass(?))", name).Row().Scan(&valid); err != nil {
				return err
			}
			if valid.Bool {
				continue
			}
			if valid.Valid {
				if err := db.Exec("DROP INDEX CONCURRENTLY " + name).Error; err != nil {
					return err
				}
			}
			if err := db.Exec("CREATE INDEX CONCURRENTLY IF NOT EXISTS " + name + " ON logs (" + index.expression + ", created_at, id)").Error; err != nil {
				return err
			}
		}
		return nil
	})
}
