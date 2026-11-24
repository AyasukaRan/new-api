package controller

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// callLogHandler runs a log handler as the given user and returns the JSON
// lines of the response.
func callLogHandler(t *testing.T, handler gin.HandlerFunc, userId int, role int, query string) []map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log?"+query, nil)
	ctx.Set("id", userId)
	ctx.Set("role", role)
	handler(ctx)
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(recorder.Body.String()), "\n") {
		var value map[string]any
		require.NoError(t, common.UnmarshalJsonStr(line, &value))
		lines = append(lines, value)
	}
	return lines
}

func exportedRequestIds(lines []map[string]any) []any {
	ids := make([]any, 0, len(lines))
	for _, line := range lines[:len(lines)-1] {
		ids = append(ids, line["request_id"])
	}
	return ids
}

// seedExportLogs stores three recent usage records, one old record, and
// current and legacy probe records that must stay separate from usage.
func seedExportLogs(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, now int64) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.Channel{Id: 9, Name: "primary"}).Error)
	logs := []model.Log{
		{UserId: 1, Username: "alice", CreatedAt: now - 3600, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 9, TokenId: 23, Quota: 10, RequestId: "a-1", UpstreamRequestId: "upstream-a-1", Other: `{"admin_info":{"use_channel":["9"]}}`},
		{UserId: 2, Username: "bob", CreatedAt: now - 1800, Type: model.LogTypeConsume, ModelName: "gpt-b", ChannelId: 9, Quota: 20, RequestId: "b-1"},
		{UserId: 1, Username: "alice", CreatedAt: now - 60, Type: model.LogTypeError, ModelName: "gpt-a", RequestId: "a-2"},
		{UserId: 1, Username: "alice", CreatedAt: now - 400*24*3600, Type: model.LogTypeConsume, ModelName: "gpt-a", RequestId: "a-old"},
		{UserId: 1, Username: "alice", CreatedAt: now - 10, Type: model.LogTypeTestConsume, ModelName: "gpt-a", ChannelId: 9, TokenName: "Model test", Content: "Model test", RequestId: "probe-current"},
		{UserId: 1, Username: "alice", CreatedAt: now - 20, Type: model.LogTypeConsume, ModelName: "gpt-a", ChannelId: 9, TokenName: "模型测试", Content: "模型测试", RequestId: "probe-legacy"},
	}
	for i := range logs {
		require.NoError(t, logDB.Create(&logs[i]).Error)
	}
}

func useLogExportDatabases(t *testing.T, mainDB *gorm.DB, logDB *gorm.DB, mainType common.DatabaseType, logType common.DatabaseType) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousMemory := common.RedisEnabled, common.MemoryCacheEnabled
	model.DB, model.LOG_DB = mainDB, logDB
	common.SetDatabaseTypes(mainType, logType)
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled, common.MemoryCacheEnabled = previousRedis, previousMemory
	})
}

func TestLogExportDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, tc := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
		{"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, isolatedDSN := newAuditTestDatabase(t, tc.kind, dsn)
			mainDB, mainType := db, tc.typ
			if tc.kind == "clickhouse" {
				mainDB, _ = newAuditTestDatabase(t, "sqlite", "")
				mainType = common.DatabaseTypeSQLite
			}
			useLogExportDatabases(t, mainDB, db, mainType, tc.typ)
			require.NoError(t, mainDB.AutoMigrate(&model.Channel{}, &model.User{}))
			if tc.kind == "clickhouse" {
				t.Setenv("LOG_SQL_DSN", isolatedDSN)
				require.NoError(t, model.InitLogDB())
			} else {
				require.NoError(t, db.AutoMigrate(&model.Log{}))
			}
			versionSQL := "SELECT version()"
			if tc.kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, model.LOG_DB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database: %s %s", tc.kind, version)
			now := time.Now().Unix()
			// Verify a fresh install, then restore the previous released log schema
			// so old and malformed rows exercise the online upgrade below.
			if tc.kind == "postgres" {
				for range 2 {
					require.NoError(t, model.MigrateLogFilterIndexes())
				}
				require.NoError(t, model.LOG_DB.Exec("DROP INDEX idx_logs_client_tool_time_v1, idx_logs_reasoning_effort_time_v1, idx_logs_transport_time_v1, idx_logs_request_path_time_v1").Error)
				require.NoError(t, model.LOG_DB.Exec("DROP FUNCTION new_api_log_text_v1(text,text)").Error)
				require.NoError(t, model.LOG_DB.Exec("DROP FUNCTION new_api_log_transport_v1(text,bigint,boolean)").Error)
			}
			seedExportLogs(t, mainDB, model.LOG_DB, now)
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now+3600)

			listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, query)[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])
			for _, row := range listed["data"].(map[string]any)["items"].([]any) {
				assert.NotContains(t, row.(map[string]any), "upstream_request_id")
			}
			// An unprivileged filter must not become an oracle for upstream IDs.
			userFiltered := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, query+"&upstream_request_id=nonmatching")[0]
			assert.EqualValues(t, 2, userFiltered["data"].(map[string]any)["total"])
			adminFiltered := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&upstream_request_id=upstream-a-1")[0]
			adminData := adminFiltered["data"].(map[string]any)
			require.EqualValues(t, 1, adminData["total"])
			assert.Equal(t, "upstream-a-1", adminData["items"].([]any)[0].(map[string]any)["upstream_request_id"])
			listed = callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&username=alice")[0]
			require.Equal(t, true, listed["success"])
			assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

			own := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, query)
			require.Len(t, own, 3)
			assert.Equal(t, []any{"a-2", "a-1"}, exportedRequestIds(own))
			assert.Equal(t, map[string]any{"success": true, "message": "", "data": map[string]any{"count": float64(2)}}, own[2])
			assert.Equal(t, float64(1), own[0]["id"], "user exports carry list positions, not row ids")
			assert.Empty(t, own[1]["channel_name"])
			assert.NotContains(t, own[1]["other"], "admin_info")
			assert.NotContains(t, own[1], "upstream_request_id")

			all := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&model_name=gpt-%25")
			require.Len(t, all, 4)
			assert.Equal(t, []any{"a-2", "b-1", "a-1"}, exportedRequestIds(all))
			assert.Equal(t, "primary", all[2]["channel_name"])
			assert.Contains(t, all[2]["other"], "admin_info")
			assert.Equal(t, "upstream-a-1", all[2]["upstream_request_id"])
			assert.Equal(t, true, all[3]["success"])

			probes := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&source=test&type=2")
			require.Len(t, probes, 3)
			assert.Equal(t, []any{"probe-current", "probe-legacy"}, exportedRequestIds(probes))
			assert.Equal(t, true, probes[2]["success"])
			userProbes := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, query+"&source=test")
			require.Len(t, userProbes, 3)
			assert.Equal(t, []any{"a-2", "a-1"}, exportedRequestIds(userProbes), "user exports always retain usage isolation")

			// The captured name belongs to a different account now. Searches,
			// displayed rows and exported data must resolve the original user ID.
			require.NoError(t, mainDB.Create(&model.User{Id: 1, Username: "463360046", AffCode: "rename1"}).Error)
			require.NoError(t, mainDB.Create(&model.User{Id: 3, Username: "alice", AffCode: "reuse1"}).Error)
			for _, name := range []string{"463360046", "46336%25"} {
				matched := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&username="+name)[0]
				require.Equal(t, true, matched["success"])
				result := matched["data"].(map[string]any)
				require.EqualValues(t, 2, result["total"])
				for _, row := range result["items"].([]any) {
					assert.Equal(t, "463360046", row.(map[string]any)["username"])
				}
			}
			renamed := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&username=463360046")
			require.Len(t, renamed, 3)
			assert.Equal(t, "463360046", renamed[1]["username"])
			own = callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, query)
			require.Len(t, own, 3)
			assert.Equal(t, "463360046", own[0]["username"])
			tokenLogs, err := model.GetLogByTokenId(23)
			require.NoError(t, err)
			require.Len(t, tokenLogs, 1)
			assert.Equal(t, "463360046", tokenLogs[0].Username)
			assert.Empty(t, tokenLogs[0].UpstreamRequestId)

			stats, err := model.SumUsedQuota(context.Background(), model.LogFilter{Username: "463360046", StartTimestamp: now - 2*24*3600}, nil, false)
			require.NoError(t, err)
			assert.Equal(t, 10, stats.Quota)
			reassigned := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&username=alice")
			require.Len(t, reassigned, 1)
			assert.Equal(t, true, reassigned[0]["success"])
			// Deleted or imported accounts without a current user retain their label.
			orphan := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&username=bob")
			require.Len(t, orphan, 2)
			assert.Equal(t, "bob", orphan[0]["username"])

			for _, log := range []model.Log{
				{UserId: 1, Username: "alice", CreatedAt: now - 3600, Type: model.LogTypeConsume, ModelName: "gpt-a", RequestId: "backfill-tie"},
				{UserId: 1, Username: "alice", CreatedAt: now - 7200, Type: model.LogTypeConsume, ModelName: "gpt-a", RequestId: "backfill-older"},
			} {
				require.NoError(t, model.LOG_DB.Create(&log).Error)
			}
			filter := model.LogFilter{StartTimestamp: now - 2*24*3600, EndTimestamp: now + 3600}
			page, total, err := model.GetUserLogs(1, filter, 0, 2)
			require.NoError(t, err)
			assert.EqualValues(t, 4, total)
			require.Len(t, page, 2)
			assert.Equal(t, []string{"a-2", "backfill-tie"}, []string{page[0].RequestId, page[1].RequestId}, "backfilled records must not displace newer activity")
			page, total, err = model.GetUserLogs(1, filter, 2, 2)
			require.NoError(t, err)
			assert.EqualValues(t, 4, total)
			require.Len(t, page, 2)
			assert.Equal(t, []string{"a-1", "backfill-older"}, []string{page[0].RequestId, page[1].RequestId}, "equal timestamps use a stable tie-breaker across pages")
			t.Run("legacy audit isolation", func(t *testing.T) {
				for _, row := range []model.Log{
					{UserId: 77, TokenId: 778, Type: model.LogTypeManage, RequestId: "legacy-operation", CreatedAt: now - 2},
					{UserId: 77, TokenId: 778, Type: model.LogTypeLogin, RequestId: "legacy-signin", CreatedAt: now - 1},
					{UserId: 77, TokenId: 778, Type: model.LogTypeConsume, RequestId: "normal-usage", CreatedAt: now},
				} {
					require.NoError(t, model.LOG_DB.Create(&row).Error)
				}
				for _, logType := range []int{0, model.LogTypeManage, model.LogTypeLogin} {
					forged := fmt.Sprintf("%s&type=%d&role=100&source=test&admin_self_view=true", query, logType)
					list := callLogHandler(t, GetUserLogs, 77, common.RoleCommonUser, forged)[0]
					require.Equal(t, true, list["success"])
					data := list["data"].(map[string]any)
					export := callLogHandler(t, ExportUserLogs, 77, common.RoleCommonUser, forged)
					wanted := []any{}
					if logType == 0 {
						wanted = append(wanted, "normal-usage")
					}
					assert.EqualValues(t, len(wanted), data["total"])
					require.Len(t, data["items"], len(wanted))
					if len(wanted) != 0 {
						assert.Equal(t, wanted[0], data["items"].([]any)[0].(map[string]any)["request_id"])
					}
					assert.Equal(t, wanted, exportedRequestIds(export))
					assert.Equal(t, true, export[len(export)-1]["success"])
				}
				tokenRows, err := model.GetLogByTokenId(778)
				require.NoError(t, err)
				require.Len(t, tokenRows, 1)
				assert.Equal(t, "normal-usage", tokenRows[0].RequestId)
				ownAdmin := callLogHandler(t, ExportUserLogs, 77, common.RoleAdminUser, query)
				assert.Equal(t, []any{"normal-usage", "legacy-signin", "legacy-operation"}, exportedRequestIds(ownAdmin))
			})
			t.Run("deferred request body", func(t *testing.T) {
				const requestID = "same-request-multiple-attempts"
				bodies := map[string]string{"attempt-a": "第一轮\n中文", "attempt-b": `{"input":"another payload"}`}
				for name, body := range bodies {
					other := common.MapToJsonStr(map[string]any{
						"admin_info": map[string]any{"request_body": body, "request_body_truncated": 123, "use_channel": []string{"9"}},
						"root_info":  map[string]any{"private": true},
					})
					require.NoError(t, model.LOG_DB.Create(&model.Log{
						UserId: 77, CreatedAt: now, Type: model.LogTypeConsume, ModelName: name, RequestId: requestID, Other: other,
					}).Error)
				}
				filter := query + "&request_id=" + requestID
				compact := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, filter+"&compact=1")[0]
				require.Equal(t, true, compact["success"])
				items := compact["data"].(map[string]any)["items"].([]any)
				require.Len(t, items, len(bodies))
				for _, item := range items {
					row := item.(map[string]any)
					var meta map[string]map[string]any
					require.NoError(t, common.UnmarshalJsonStr(row["other"].(string), &meta))
					require.NotContains(t, meta, "root_info")
					admin := meta["admin_info"]
					assert.NotContains(t, admin, "request_body")
					assert.EqualValues(t, 123, admin["request_body_truncated"])
					assert.Equal(t, []any{"9"}, admin["use_channel"])
					ref := admin["request_body_ref"].(string)
					args := fmt.Sprintf("created_at=%d&request_id=%s&ref=%s", now, requestID, ref)
					got := callLogHandler(t, GetLogRequestBody, 3, common.RoleAdminUser, args)[0]
					require.Equal(t, true, got["success"])
					assert.Equal(t, bodies[row["model_name"].(string)], got["data"])
				}
				missing := callLogHandler(t, GetLogRequestBody, 3, common.RoleAdminUser, fmt.Sprintf("created_at=%d&request_id=%s&ref=%s", now, requestID, strings.Repeat("0", 64)))[0]
				assert.Equal(t, false, missing["success"])
				root := callLogHandler(t, GetAllLogs, 1, common.RoleRootUser, filter+"&compact=1")[0]
				assert.Contains(t, root["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["other"], "root_info")
				legacy := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, filter)[0]
				export := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, filter+"&compact=1")
				require.Len(t, export, 3)
				for _, row := range []map[string]any{legacy["data"].(map[string]any)["items"].([]any)[0].(map[string]any), export[0]} {
					assert.Contains(t, row["other"], `"request_body":`)
					assert.NotContains(t, row["other"], "request_body_ref")
				}
				self := callLogHandler(t, GetUserLogs, 77, common.RoleCommonUser, filter+"&compact=1")[0]
				for _, item := range self["data"].(map[string]any)["items"].([]any) {
					assert.NotContains(t, item.(map[string]any)["other"], "request_body")
				}

				// SQL databases may contain NULL IDs from older records. ClickHouse
				// stores an empty String instead. Both appear empty in the list.
				legacyCapture := model.Log{CreatedAt: now - 1, Type: model.LogTypeConsume, ModelName: "legacy-no-request-id", Other: `{"admin_info":{"request_body":"legacy body"}}`}
				require.NoError(t, model.LOG_DB.Create(&legacyCapture).Error)
				if tc.kind != "clickhouse" {
					require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("id = ?", legacyCapture.Id).UpdateColumn("request_id", nil).Error)
				}
				legacyResponse := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&model_name=legacy-no-request-id&compact=1")[0]
				legacyRows := legacyResponse["data"].(map[string]any)["items"].([]any)
				require.Len(t, legacyRows, 1)
				legacyRow := legacyRows[0].(map[string]any)
				assert.Empty(t, legacyRow["request_id"])
				var legacyMeta map[string]map[string]string
				require.NoError(t, common.UnmarshalJsonStr(legacyRow["other"].(string), &legacyMeta))
				args := fmt.Sprintf("created_at=%d&request_id=&ref=%s", now-1, legacyMeta["admin_info"]["request_body_ref"])
				gotLegacy := callLogHandler(t, GetLogRequestBody, 3, common.RoleAdminUser, args)[0]
				assert.Equal(t, "legacy body", gotLegacy["data"])
				wrongTime := fmt.Sprintf("created_at=%d&request_id=&ref=%s", now, legacyMeta["admin_info"]["request_body_ref"])
				assert.Equal(t, false, callLogHandler(t, GetLogRequestBody, 3, common.RoleAdminUser, wrongTime)[0]["success"])

			})
			t.Run("header filters", func(t *testing.T) {
				const addr = "2001:db8::42"
				const client = "CLI_100%"
				const query = "model_name=header-fixture"
				rows := []model.Log{
					{UserId: 78, RequestId: "before-upgrade", Other: `{"reasoning_effort":"high","client_tool":"CLI_100%"}`},
					{UserId: 78, RequestId: "nested-only", Other: `{"reasoning_effort":"low","admin_info":{"client_tool":"CLI_100%"}}`},
					{UserId: 78, RequestId: "broken-json", Other: `{"reasoning_effort":"high"`},
					{UserId: 79, RequestId: "other-account", Other: `{"client_tool":"cli_100%","reasoning_effort":"high"}`},
				}
				var refs []model.LiveLogRef
				for i := range rows {
					row := &rows[i]
					row.Id = 90051 + i
					row.Type, row.ModelName, row.CreatedAt, row.Ip = model.LogTypeConsume, "header-fixture", now, addr
					require.NoError(t, model.LOG_DB.Create(row).Error)
					refs = append(refs, model.LiveLogRef{ID: row.Id, RequestID: row.RequestId, CreatedAt: row.CreatedAt, UserID: row.UserId})
				}
				for range 2 {
					require.NoError(t, model.MigrateLogFilterIndexes())
				}
				match := query + "&client_tool=CLI_100%25&reasoning_effort=high&ip=" + addr
				filtered := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, match)[0]
				require.Equal(t, true, filtered["success"])
				data := filtered["data"].(map[string]any)
				require.EqualValues(t, 1, data["total"])
				assert.Equal(t, "before-upgrade", data["items"].([]any)[0].(map[string]any)["request_id"])
				export := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, match)
				require.Len(t, export, 2)
				assert.Equal(t, []any{"before-upgrade"}, exportedRequestIds(export))
				miss := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&ip=203.0.113.88")
				require.Len(t, miss, 1)
				assert.Equal(t, true, miss[0]["success"])
				stats := callLogHandler(t, GetLogsStat, 3, common.RoleAdminUser, match+"&live_total=1")[0]
				require.Equal(t, true, stats["success"])
				assert.EqualValues(t, 1, stats["data"].(map[string]any)["live_total"])
				changes, omitted, err := model.GetChangedLogs(t.Context(), model.LogFilter{ClientTool: client, ReasoningEffort: "high", IP: addr}, 0, common.RoleAdminUser, true, 10, refs)
				require.NoError(t, err)
				require.Len(t, changes, 1)
				assert.Equal(t, 0, omitted)
				assert.Equal(t, "before-upgrade", changes[0].RequestId)
				// An owner can filter their IP and effort, but cannot infer client
				// classification through list, stats or export.
				private := query + "&reasoning_effort=high&ip=" + addr + "&client_tool=nonexistent"
				self := callLogHandler(t, GetUserLogs, 78, common.RoleCommonUser, private)[0]
				require.Equal(t, true, self["success"])
				assert.EqualValues(t, 1, self["data"].(map[string]any)["total"])
				own := callLogHandler(t, ExportUserLogs, 78, common.RoleCommonUser, private)
				require.Len(t, own, 2)
				assert.Equal(t, []any{"before-upgrade"}, exportedRequestIds(own))
				assert.NotContains(t, own[0]["other"], "client_tool")
				ownStats := callLogHandler(t, GetLogsSelfStat, 78, common.RoleCommonUser, private+"&live_total=1")[0]
				require.Equal(t, true, ownStats["success"])
				assert.EqualValues(t, 1, ownStats["data"].(map[string]any)["live_total"])
			})
			t.Run("transport and endpoint filters", func(t *testing.T) {
				require.NoError(t, model.MigrateLogFilterIndexes())
				const userID = 82
				const query = "model_name=transport-fixture"
				rows := []model.Log{
					{RequestId: "observed", Other: `{"request_path":"/v1/responses","response_transport":"sse"}`},
					{RequestId: "prepared", Type: model.LogTypeError, Other: `{"request_path":"/v1/responses?beta=true","prepared_response_transport":"sse"}`},
					{RequestId: "observed-first", Other: `{"response_transport":"http","prepared_response_transport":"sse","ws":true}`},
					{RequestId: "ws", Other: `{"ws":true,"prepared_response_transport":"sse"}`},
					{RequestId: "legacy", Other: `{"request_path":"/v1/messages?beta=true","frt":1.5,"stream_status":{"status":"ok","end_reason":"eof"}}`},
					{RequestId: "incomplete", Other: `{"request_path":"/v1/messages","frt":15,"stream_status":{"status":"ok","response_status":"incomplete"}}`},
					{RequestId: "legacy-completed", Other: `{"request_path":"/v1/messages","stream_status":{"status":"ok","response_status":"completed"}}`},
					{RequestId: "errored", Other: `{"request_path":"/v1/messages","frt":4,"stream_status":{"status":"ok","error_count":1}}`},
					{RequestId: "nested-only", Other: `{"admin_info":{"response_transport":"sse","request_path":"/v1/responses"}}`},
					{RequestId: "broken", Other: `{"response_transport":"sse"`},
					{RequestId: "other-owner", UserId: userID + 1, Other: `{"response_transport":"sse","request_path":"/v1/responses_extra"}`},
					{RequestId: "nonstream"}, {RequestId: "async", Other: `{"is_task":true,"response_transport":"sse"}`},
					{RequestId: "sync", Other: `{"is_task":true,"task_sync":true,"response_transport":"http"}`},
					{RequestId: "refund", Type: model.LogTypeRefund, Other: `{"response_transport":"sse","request_path":"/v1/responses"}`},
					{RequestId: "other-path", Other: `{"ws":true,"request_path":"/v1/responsesXextra"}`},
				}
				var refs []model.LiveLogRef
				for i := range rows {
					row := &rows[i]
					if row.UserId == 0 {
						row.UserId = userID
					}
					if row.Type == 0 {
						row.Type = model.LogTypeConsume
					}
					row.IsStream = row.RequestId != "nonstream"
					row.Id, row.CreatedAt, row.ModelName = 91500+i, now, "transport-fixture"
					require.NoError(t, model.LOG_DB.Create(row).Error)
					refs = append(refs, model.LiveLogRef{ID: row.Id, UserID: row.UserId, RequestID: row.RequestId, CreatedAt: now})
				}
				for transport, want := range map[string][]string{
					"sse":  {"observed", "prepared", "legacy", "legacy-completed", "other-owner"},
					"http": {"observed-first"}, "ws": {"ws", "other-path"},
					"unknown":      {"broken", "incomplete", "nested-only", "errored"},
					"non_streamed": {"nonstream"}, "sync": {"sync"}, "async": {"async"},
				} {
					data := callLogHandler(t, GetAllLogs, 3, common.RoleAdminUser, query+"&response_transport="+transport)[0]
					require.Equal(t, true, data["success"], data)
					page := data["data"].(map[string]any)
					assert.EqualValues(t, len(want), page["total"], transport)
					var ids []string
					for _, item := range page["items"].([]any) {
						ids = append(ids, item.(map[string]any)["request_id"].(string))
					}
					assert.ElementsMatch(t, want, ids, transport)
				}
				filtered := query + "&response_transport=sse&request_path=/v1/responses"
				exported := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, filtered)
				assert.ElementsMatch(t, []any{"observed", "prepared"}, exportedRequestIds(exported))
				// A literal underscore in a path is never a wildcard.
				exactPath := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, query+"&request_path=/v1/responses_extra")
				assert.Equal(t, []any{"other-owner"}, exportedRequestIds(exactPath))
				stat := callLogHandler(t, GetLogsStat, 3, common.RoleAdminUser, filtered+"&live_total=1")[0]
				require.Equal(t, true, stat["success"], stat)
				assert.EqualValues(t, 2, stat["data"].(map[string]any)["live_total"])
				changes, omitted, err := model.GetChangedLogs(t.Context(), model.LogFilter{ResponseTransport: "sse", RequestPath: "/v1/responses"}, 0, common.RoleAdminUser, true, 10, refs)
				require.NoError(t, err)
				assert.Len(t, changes, 2)
				assert.Zero(t, omitted)
				// The personal API accepts the visible mode, ignores the admin-only endpoint,
				// and never returns rows belonging to a different user.
				own := callLogHandler(t, ExportUserLogs, userID, common.RoleCommonUser, filtered)
				assert.ElementsMatch(t, []any{"observed", "prepared", "legacy", "legacy-completed"}, exportedRequestIds(own))
			})
			t.Run("live changes", func(t *testing.T) {
				first := model.Log{Id: 88001, UserId: 81, CreatedAt: now - 1, ModelName: "live-test", Type: model.LogTypeError, RequestId: "multi-attempt", ChannelId: 9, UpstreamRequestId: "private-upstream", Other: `{"admin_info":{"request_body":"sensitive input"},"root_info":{"secret":"root-only"}}`}
				retry := first
				retry.Id++
				retry.CreatedAt++
				retry.Type = model.LogTypeConsume
				another := retry
				another.Id++
				another.UserId++
				another.RequestId = "someone-else"
				probe := first
				probe.Id += 3
				probe.Type = model.LogTypeTestConsume
				audit := first
				audit.Id += 4
				audit.Type = model.LogTypeManage
				var refs []model.LiveLogRef
				for _, log := range []model.Log{first, retry, another, probe, audit} {
					require.NoError(t, model.LOG_DB.Create(&log).Error)
					refs = append(refs, model.LiveLogRef{ID: log.Id, UserID: log.UserId, CreatedAt: log.CreatedAt, RequestID: log.RequestId})
				}
				filter := model.LogFilter{Source: model.LogSourceUsage, ModelName: "live-test"}
				userRows, omitted, err := model.GetChangedLogs(t.Context(), filter, 81, common.RoleCommonUser, true, 10, append(refs, refs[0]))
				require.NoError(t, err)
				require.Len(t, userRows, 2)
				assert.Zero(t, omitted)
				for i, row := range userRows {
					assert.Equal(t, 81, row.UserId)
					assert.Equal(t, i+1, row.Id)
					assert.Empty(t, row.UpstreamRequestId)
					assert.NotContains(t, row.Other, "sensitive input")
					assert.NotContains(t, row.Other, "root-only")
				}
				assert.Equal(t, model.LogTypeConsume, userRows[0].Type)
				assert.NotEqual(t, userRows[0].LiveKey, userRows[1].LiveKey)
				// Only the newly committed attempt is sent, even when its request
				// has other attempts in the same ClickHouse partition.
				exact, _, err := model.GetChangedLogs(t.Context(), filter, 81, common.RoleCommonUser, true, 10, refs[:1])
				require.NoError(t, err)
				require.Len(t, exact, 1)
				assert.Equal(t, userRows[1].LiveKey, exact[0].LiveKey)
				adminRows, omitted, err := model.GetChangedLogs(t.Context(), filter, 0, common.RoleAdminUser, true, 1, refs)
				require.NoError(t, err)
				require.Len(t, adminRows, 1)
				assert.Equal(t, 3, omitted)
				filter.UpstreamRequestId = "private-upstream"
				adminRows, _, err = model.GetChangedLogs(t.Context(), filter, 0, common.RoleAdminUser, true, 10, refs[:1])
				require.NoError(t, err)
				require.Len(t, adminRows, 1)
				assert.Equal(t, "primary", adminRows[0].ChannelName)
				assert.Contains(t, adminRows[0].Other, "request_body_ref")
				assert.NotContains(t, adminRows[0].Other, "sensitive input")
				assert.NotContains(t, adminRows[0].Other, "root-only")
				// Self snapshots and streamed rows have the same key on every
				// page, independent of privacy-preserving display IDs.
				page := callLogHandler(t, GetUserLogs, 81, common.RoleCommonUser, query+"&live=1&p=2&page_size=1&model_name=live-test")[0]["data"].(map[string]any)
				require.NotEmpty(t, page["live_cursor"])
				items := page["items"].([]any)
				require.Len(t, items, 1)
				assert.Equal(t, exact[0].LiveKey, items[0].(map[string]any)["live_key"])
				stat := callLogHandler(t, GetLogsSelfStat, 81, common.RoleCommonUser, query+"&live_total=1&model_name=live-test")[0]["data"].(map[string]any)
				assert.EqualValues(t, 2, stat["live_total"])
				empty := callLogHandler(t, GetLogsSelfStat, 81, common.RoleCommonUser, query+"&live_total=1&model_name=absent-live-model")[0]["data"].(map[string]any)
				assert.EqualValues(t, 0, empty["live_total"])
			})

		})
	}
}

func TestLogExportRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.QuotaData{}, &model.User{}))
	now := time.Now().Unix()
	seedExportLogs(t, db, db, now)
	logSetting := operation_setting.GetLogSetting()
	previousSetting := *logSetting
	t.Cleanup(func() { *logSetting = previousSetting })
	recent := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-2*24*3600, now)
	tooLong := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-366*24*3600, now)

	t.Run("time range", func(t *testing.T) {
		rejected := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, tooLong)
		require.Len(t, rejected, 1)
		assert.Equal(t, false, rejected[0]["success"])
		assert.Equal(t, "The time range cannot exceed {{days}} days", rejected[0]["message_key"])
		assert.Equal(t, map[string]any{"days": float64(365)}, rejected[0]["message_params"])

		assert.Equal(t, false, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, false, callLogHandler(t, GetLogsSelfStat, 1, common.RoleCommonUser, tooLong)[0]["success"])
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 3, common.RoleAdminUser, tooLong)[0]["success"], "administrators search their own logs without a limit")

		// The dashboard follows the same range instead of its old one-month limit.
		fortyDays := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-40*24*3600, now)
		for _, handler := range []gin.HandlerFunc{GetUserQuotaDates, GetUserFlowQuotaDates} {
			assert.Equal(t, "The time range cannot exceed {{days}} days", callLogHandler(t, handler, 1, common.RoleCommonUser, tooLong)[0]["message_key"])
			assert.Equal(t, true, callLogHandler(t, handler, 1, common.RoleCommonUser, fortyDays)[0]["success"])
		}

		// Without a start, a user's search reaches back the whole range.
		listed := callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, "")[0]
		assert.Equal(t, true, listed["success"])
		assert.EqualValues(t, 2, listed["data"].(map[string]any)["total"])

		logSetting.MaxRangeDays = 400
		assert.Equal(t, true, callLogHandler(t, GetUserLogs, 1, common.RoleCommonUser, tooLong)[0]["success"])
		logSetting.MaxRangeDays = previousSetting.MaxRangeDays
	})

	t.Run("shared database with one connection", func(t *testing.T) {
		connection, err := db.DB()
		require.NoError(t, err)
		previousLimit := connection.Stats().MaxOpenConnections
		connection.SetMaxOpenConns(1)
		t.Cleanup(func() { connection.SetMaxOpenConns(previousLimit) })
		all := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, recent)
		require.Len(t, all, 4)
		assert.Equal(t, []any{"a-2", "b-1", "a-1"}, exportedRequestIds(all))
		assert.Equal(t, "primary", all[2]["channel_name"])
		assert.Equal(t, true, all[3]["success"])
	})

	t.Run("user export switch", func(t *testing.T) {
		logSetting.UserExportEnabled = false
		t.Cleanup(func() { logSetting.UserExportEnabled = previousSetting.UserExportEnabled })
		disabled := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, disabled, 1)
		assert.Equal(t, "Log export is disabled", disabled[0]["message_key"])
		assert.Equal(t, true, callLogHandler(t, ExportUserLogs, 3, common.RoleAdminUser, recent)[0]["success"], "administrators export their own logs even when users cannot")
	})

	t.Run("one export per user", func(t *testing.T) {
		release, ok, err := service.AcquireLogExportLock(1)
		require.NoError(t, err)
		require.True(t, ok)
		busy := callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent)
		require.Len(t, busy, 1)
		assert.Equal(t, "Another log export is running. Try again after it finishes.", busy[0]["message_key"])
		other := callLogHandler(t, ExportUserLogs, 2, common.RoleCommonUser, recent)
		assert.Equal(t, true, other[len(other)-1]["success"], "other users are not blocked")
		release()
		assert.Len(t, callLogHandler(t, ExportUserLogs, 1, common.RoleCommonUser, recent), 3)
	})
}

func TestAcquireLogExportLock(t *testing.T) {
	for _, useRedis := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%v", useRedis), func(t *testing.T) {
			previousRedis, previousClient := common.RedisEnabled, common.RDB
			common.RedisEnabled = useRedis
			if useRedis {
				server := miniredis.RunT(t)
				common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
				require.NoError(t, common.RDB.Ping(context.Background()).Err())
			}
			t.Cleanup(func() { common.RedisEnabled, common.RDB = previousRedis, previousClient })

			release, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			require.True(t, ok)
			_, ok, err = service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.False(t, ok, "a second export of the same user must wait")
			releaseOther, ok, err := service.AcquireLogExportLock(2)
			require.NoError(t, err)
			assert.True(t, ok)
			releaseOther()
			release()
			releaseAgain, ok, err := service.AcquireLogExportLock(1)
			require.NoError(t, err)
			assert.True(t, ok, "a finished export frees the user")
			releaseAgain()
		})
	}
}

func TestUsageLogClientIPDatabaseMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousLog, previousExport, previousMaster := common.LogConsumeEnabled, common.DataExportEnabled, common.IsMasterNode
	common.LogConsumeEnabled, common.DataExportEnabled, common.IsMasterNode = true, false, true
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.DataExportEnabled, common.IsMasterNode = previousLog, previousExport, previousMaster
	})
	for _, dialect := range []struct {
		name, env string
		kind      common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite}, {"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL}, {"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL}, {"clickhouse", "AUDIT_CLICKHOUSE_DSN", common.DatabaseTypeClickHouse},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			dsn := os.Getenv(dialect.env)
			if dialect.env != "" && dsn == "" {
				t.Skip(dialect.env + " not configured")
			}
			logDB, logDSN := newAuditTestDatabase(t, dialect.name, dsn)
			mainKind := dialect.kind
			mainName, mainDSN := dialect.name, dsn
			if mainKind == common.DatabaseTypeClickHouse {
				mainKind = common.DatabaseTypeSQLite
				mainName, mainDSN = "sqlite", ""
			}
			mainDB, _ := newAuditTestDatabase(t, mainName, mainDSN)
			useLogExportDatabases(t, mainDB, logDB, mainKind, dialect.kind)
			if dialect.kind == common.DatabaseTypeClickHouse {
				t.Setenv("LOG_SQL_DSN", logDSN)
				require.NoError(t, model.InitLogDB())
				logDB = model.LOG_DB
			} else {
				require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			}
			versionSQL := "SELECT version()"
			if dialect.name == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, logDB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("log database %s %s", dialect.name, version)

			require.NoError(t, mainDB.AutoMigrate(&model.User{}, &model.Task{}, &model.Midjourney{}, &model.Channel{}))
			// Recreate the previous released table (the same columns minus client_ip)
			// with a real task row; a second startup must preserve data and indexes.
			old := model.Midjourney{MjId: "existing", Prompt: "preserved", Quota: 17, UserId: 9}
			require.NoError(t, mainDB.Create(&old).Error)
			require.NoError(t, mainDB.Migrator().DropColumn(&model.Midjourney{}, "ClientIP"))
			require.False(t, mainDB.Migrator().HasColumn(&model.Midjourney{}, "ClientIP"))
			for range 2 {
				require.NoError(t, mainDB.AutoMigrate(&model.Midjourney{}))
			}
			require.True(t, mainDB.Migrator().HasIndex(&model.Midjourney{}, "idx_midjourneys_mj_id"))
			var migrated model.Midjourney
			require.NoError(t, mainDB.First(&migrated, old.Id).Error)
			assert.Equal(t, "preserved", migrated.Prompt)
			assert.Equal(t, 17, migrated.Quota)
			assert.Empty(t, migrated.ClientIP)

			user := model.User{Username: "ip-fixture", AffCode: "fixture-ip", Setting: `{"record_ip_log":false,"language":"vi"}`, Quota: 1000}
			require.NoError(t, mainDB.Create(&user).Error)
			channel := model.Channel{Name: "ip", Key: "fixture-key"}
			require.NoError(t, mainDB.Create(&channel).Error)
			router := gin.New()
			require.NoError(t, router.SetTrustedProxies([]string{"127.0.0.1"}))
			router.POST("/", func(c *gin.Context) {
				model.RecordConsumeLog(c, user.Id, model.RecordConsumeLogParams{ModelName: "usage", Quota: 1})
				model.RecordErrorLog(c, user.Id, 0, "bad", "", "upstream error", 0, 0, false, "", nil)
			})
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.RemoteAddr = "127.0.0.1:10001"
			const ip = "2001:db8::123"
			req.Header.Set("X-Forwarded-For", ip)
			router.ServeHTTP(httptest.NewRecorder(), req)
			req.RemoteAddr = "198.51.100.19:10001"
			router.ServeHTTP(httptest.NewRecorder(), req)
			req.RemoteAddr = ""
			router.ServeHTTP(httptest.NewRecorder(), req)
			for _, kind := range []int{model.LogTypeError, model.LogTypeConsume} {
				var ips []string
				require.NoError(t, logDB.Model(&model.Log{}).Where("type = ?", kind).Pluck("ip", &ips).Error)
				assert.ElementsMatch(t, []string{ip, "198.51.100.19", ""}, ips)
			}

			task := model.Task{TaskID: "ip-job", UserId: user.Id, ChannelId: channel.Id, Quota: 20, Properties: model.Properties{OriginModelName: "ip-task"}, PrivateData: model.TaskPrivateData{ClientIP: ip}}
			require.NoError(t, mainDB.Create(&task).Error)
			var loaded model.Task
			require.NoError(t, mainDB.First(&loaded, task.ID).Error)
			require.Equal(t, ip, loaded.PrivateData.ClientIP)
			assert.NotContains(t, common.GetJsonString(loaded), ip)
			stored := loaded.PrivateData
			for _, oldValue := range []any{nil, `{"upstream_task_id":"legacy"}`} {
				stored.ClientIP = ip
				require.NoError(t, stored.Scan(oldValue))
				assert.Empty(t, stored.ClientIP, "old rows must not inherit the preceding task IP")
			}
			service.RecalculateTaskQuota(t.Context(), &loaded, 23, common.NewMessage("task delta"))
			require.True(t, service.RefundTaskQuota(t.Context(), &loaded, "task failed"))
			mj := model.Midjourney{MjId: "ip-mj", ClientIP: ip, UserId: user.Id, ChannelId: channel.Id, Quota: 37}
			require.NoError(t, mainDB.Create(&mj).Error)
			var loadedMJ model.Midjourney
			require.NoError(t, mainDB.First(&loadedMJ, mj.Id).Error)
			assert.NotContains(t, common.GetJsonString(loadedMJ), ip)
			require.True(t, service.RefundMidjourneyQuota(t.Context(), &loadedMJ, "failed"))
			model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{UserId: user.Id, LogType: model.LogTypeRefund, Quota: 41})
			for _, q := range []int{3, 23, 37, 41} {
				var rows []model.Log
				require.NoError(t, logDB.Where("quota = ?", q).Find(&rows).Error)
				require.Len(t, rows, 1)
				if q == 41 {
					assert.Empty(t, rows[0].Ip)
				} else {
					assert.Equal(t, ip, rows[0].Ip)
				}
			}
		})
	}
}

// Exercise the snapshot/stream handoff and replay through the actual log
// writer, including local delivery and resynchronization across a Redis outage.
func TestLiveLogStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.User{}))
	require.NoError(t, db.Create(&model.User{Id: 81, Username: "stream-reader", AffCode: "stream1"}).Error)
	redisServer := miniredis.RunT(t)
	oldClient := common.RDB
	defer func() { common.RDB = oldClient }()
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), MaxRetries: -1})
	defer client.Close()
	common.RedisEnabled = true
	common.RDB = client
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	model.StartLiveLogFeed(ctx)
	engine := gin.New()
	engine.GET("/api/log/self/stream", func(c *gin.Context) { c.Set("id", 81); c.Set("role", common.RoleCommonUser); StreamUserLogChanges(c) })
	server := httptest.NewServer(engine)
	defer server.Close()
	type packet struct {
		Items  []model.Log
		Cursor string
	}
	// Open (or resume) an HTTP stream, verify immediate flushing, read a
	// single named event and disconnect. Only the user's visible rows count.
	read := func(cursor, want string) packet {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/log/self/stream?compact=1&cursor="+cursor, nil)
		require.NoError(t, err)
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
		s := bufio.NewScanner(resp.Body)
		for _, line := range []string{"event: ready", "data: {}", "", "event: " + want} {
			require.True(t, s.Scan(), "missing %q: %v", line, s.Err())
			require.Equal(t, line, s.Text())
		}
		require.True(t, s.Scan())
		payload, ok := strings.CutPrefix(s.Text(), "data: ")
		require.True(t, ok)
		var value packet
		require.NoError(t, common.UnmarshalJsonStr(payload, &value))
		return value
	}
	record := func(id int, message string) { model.RecordLog(id, model.LogTypeTopup, common.NewMessage(message)) }
	cursor := model.LiveLogCursor()
	record(82, "another account")
	record(81, "first")
	first := read(cursor, "logs")
	require.Len(t, first.Items, 1)
	assert.Contains(t, first.Items[0].Content, "first")
	assert.NotEmpty(t, first.Items[0].LiveKey)
	replay := read(cursor, "logs")
	require.Len(t, replay.Items, 1)
	assert.Equal(t, first.Cursor, replay.Cursor)
	assert.Equal(t, first.Items[0].LiveKey, replay.Items[0].LiveKey)
	read("expired", "reset")

	redisServer.Close()
	record(81, "boundary")
	read(first.Cursor, "reset")
	cursor = model.LiveLogCursor()
	record(81, "offline")
	offline := read(cursor, "logs")
	require.Len(t, offline.Items, 1)
	assert.Contains(t, offline.Items[0].Content, "offline")
	require.NoError(t, redisServer.Restart())
	read(offline.Cursor, "reset")
	cursor = model.LiveLogCursor()
	record(81, "recovered")
	restored := read(cursor, "logs")
	require.Len(t, restored.Items, 1)
	assert.Contains(t, restored.Items[0].Content, "recovered")
}
