package model

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Keys and types are fixed by the log schema. Missing and malformed fields have
// the same defaults as the transport badge in the usage log.
func logTransportFact(kind string, keys ...string) string {
	switch common.LogDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		// The PostgreSQL filter function parses the document once per row.
		path := "'{" + strings.Join(keys, ",") + "}'"
		scalar := "(facts #>> " + path + ")"
		raw := "(facts #> " + path + ")"
		switch kind {
		case "bool":
			return "(" + raw + " = 'true'::jsonb)"
		case "number":
			return "(CASE WHEN jsonb_typeof(" + raw + ") = 'number' THEN " + scalar + "::numeric ELSE 0 END)"
		default:
			return "COALESCE(" + scalar + ", '')"
		}
	case common.DatabaseTypeClickHouse:
		fn := "JSONExtractString"
		if kind == "bool" {
			fn = "JSONExtractBool"
		} else if kind == "number" {
			fn = "JSONExtractFloat"
		}
		return fn + "(ifNull(logs.other, ''), '" + strings.Join(keys, "', '") + "')"
	default:
		path := "'$." + strings.Join(keys, ".") + "'"
		const doc = "(CASE WHEN JSON_VALID(logs.other) THEN logs.other ELSE '{}' END)"
		raw := "JSON_EXTRACT(" + doc + ", " + path + ")"
		if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
			switch kind {
			case "bool":
				return "(" + raw + " = CAST('true' AS JSON))"
			case "number":
				return "(CASE WHEN JSON_TYPE(" + raw + ") IN ('INTEGER', 'DOUBLE') THEN " + raw + " + 0 ELSE 0 END)"
			default:
				return "(CASE WHEN JSON_TYPE(" + raw + ") = 'STRING' THEN BINARY JSON_UNQUOTE(" + raw + ") ELSE '' END)"
			}
		}
		typ := "JSON_TYPE(" + doc + ", " + path + ")"
		switch kind {
		case "bool":
			return "(" + typ + " = 'true')"
		case "number":
			return "(CASE WHEN " + typ + " IN ('integer','real') THEN " + raw + " ELSE 0 END)"
		default:
			return "COALESCE(" + raw + ", '')"
		}
	}
}

func logEndpointWithoutQuerySQL(path string) string {
	switch common.LogDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		return "split_part(" + path + ", chr(63), 1)"
	case common.DatabaseTypeMySQL:
		return "SUBSTRING_INDEX(" + path + ", CHAR(63), 1)"
	case common.DatabaseTypeClickHouse:
		return "splitByChar(char(63), " + path + ")[1]"
	default:
		return "substr(" + path + ", 1, instr(" + path + " || char(63), char(63)) - 1)"
	}
}

func logRequestPathSQL() string {
	if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		return logEndpointWithoutQuerySQL("new_api_log_text_v1(logs.other, 'request_path')")
	}
	return logEndpointWithoutQuerySQL(logTransportFact("string", "request_path"))
}

func logTransportFilterSQL() string {
	if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		return "new_api_log_transport_v1(logs.other, logs.type, logs.is_stream)"
	}
	return logTransportCaseSQL("logs.type", "logs.is_stream")
}

// Match StreamModeCell, including the dashed SSE badge for prepared headers.
// Successful historical Messages streams need both a normal end and output.
func logTransportCaseSQL(logType, streaming string) string {
	fact := logTransportFact
	responseStatus := fact("string", "stream_status", "response_status")
	path := logEndpointWithoutQuerySQL(fact("string", "request_path"))
	return "CASE WHEN " + fact("bool", "is_task") + " THEN CASE WHEN " + fact("bool", "task_sync") + " THEN 'sync' ELSE 'async' END" +
		" ELSE CASE " + fact("string", "response_transport") + " WHEN 'ws' THEN 'ws' WHEN 'sse' THEN 'sse' WHEN 'http' THEN 'http' ELSE CASE" +
		" WHEN " + fact("bool", "ws") + " THEN 'ws'" +
		" WHEN " + fact("string", "prepared_response_transport") + " = 'sse' THEN 'sse'" +
		" WHEN " + streaming + " AND " + logType + " IN (" + strconv.Itoa(LogTypeConsume) + ", " + strconv.Itoa(LogTypeTestConsume) + ")" +
		" AND " + path + " = '/v1/messages' AND " + fact("string", "stream_status", "status") + " = 'ok'" +
		" AND " + fact("string", "stream_status", "end_error") + " = '' AND " + fact("number", "stream_status", "error_count") + " = 0" +
		" AND " + fact("string", "stream_status", "end_reason") + " IN ('', 'done', 'eof')" +
		" AND (" + responseStatus + " = 'completed' OR (" + responseStatus + " = '' AND " + fact("number", "frt") + " > 0)) THEN 'sse'" +
		" WHEN " + streaming + " THEN 'unknown' ELSE 'non_streamed' END END END"
}

func filterLogRequestPath(tx *gorm.DB, value string) (*gorm.DB, error) {
	path := logRequestPathSQL()
	if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		tx = tx.Where("left("+path+", 128) = left(CAST(? AS text), 128)", value)
		if len(value) < 128 {
			return tx, nil
		}
	}
	return tx.Where(path+" = ?", value), nil
}
