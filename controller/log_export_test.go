package controller

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
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
			require.NoError(t, model.MigrateRequestTraces())
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

			t.Run("model result and audit separation", func(t *testing.T) {
				require.NoError(t, model.MigrateLogFilterIndexes())
				require.NoError(t, model.MigrateLogFilterIndexes(), "result function migration is repeatable")
				require.NoError(t, model.MigrateAuditLogs())
				const userID = 731
				fixtures := []struct {
					id, result, other string
					typ               int
				}{
					{"plain", "success", `{}`, model.LogTypeConsume},
					{"completed", "success", `{"stream_status":{"status":"error","response_status":"completed","end_error":"recovered"}}`, model.LogTypeConsume},
					{"normal", "success", `{"stream_status":{"status":"ok","end_error":"recovered"}}`, model.LogTypeConsume},
					{"soft", "success", `{"stream_status":{"status":"error","error_count":2}}`, model.LogTypeConsume},
					{"broken-metadata", "success", `{`, model.LogTypeConsume},
					{"upstream-failed", "failure", `{}`, model.LogTypeError},
					{"broken-failure", "failure", `{"admin_info":{"secret":"private-fragment"},"broken":`, model.LogTypeError},
					{"partial", "failure", `{"stream_status":{"response_status":"incomplete"}}`, model.LogTypeConsume},
					{"normal-transport-incomplete", "failure", `{"stream_status":{"status":"ok","response_status":"incomplete"}}`, model.LogTypeConsume},
					{"normal-transport-failed", "failure", `{"stream_status":{"status":"ok","response_status":"failed"}}`, model.LogTypeConsume},
					{"timeout", "failure", `{"stream_status":{"end_reason":"timeout"}}`, model.LogTypeConsume},
					{"cancelled", "failure", `{"stream_status":{"response_status":"cancelled"}}`, model.LogTypeConsume},
					{"topup", "", `{"content_parts":[{"key":"Credit {{value}}","params":{"value":9007199254740993}}],"root_info":{"secret":"private-root"},"admin_info":{"admin_username":"private-admin"}}`, model.LogTypeTopup},
					{"system", "", `{}`, model.LogTypeSystem},
					{"refund", "", `{}`, model.LogTypeRefund},
					{"manage", "", `{}`, model.LogTypeManage},
					{"login", "", `{}`, model.LogTypeLogin},
					{"unknown", "", `{}`, model.LogTypeUnknown},
					{"probe", "", `{}`, model.LogTypeTestConsume},
					{"old-probe", "", `{}`, model.LogTypeConsume},
				}
				var refs []model.LiveLogRef
				var malformedLog model.Log
				wantIDs := map[string][]any{"": {}, "success": {}, "failure": {}}
				quota := map[string]int{}
				for i, fixture := range fixtures {
					row := model.Log{Id: 973100 + i, UserId: userID, Username: "model-audit-owner", Type: fixture.typ, CreatedAt: now - int64(i), ModelName: "model-audit-fixture", RequestId: "model-audit-" + fixture.id, Other: fixture.other, Quota: 7, Content: "account event " + fixture.id}
					if fixture.id == "old-probe" {
						row.TokenName, row.Content = "模型测试", "模型测试"
					}
					if fixture.id == "broken-failure" {
						row.Content = "status_code=429, rate limit"
						malformedLog = row
					}
					require.NoError(t, model.LOG_DB.Create(&row).Error)
					refs = append(refs, model.LiveLogRef{ID: row.Id, UserID: userID, CreatedAt: row.CreatedAt, RequestID: row.RequestId})
					if fixture.result != "" {
						wantIDs[""] = append(wantIDs[""], row.RequestId)
						wantIDs[fixture.result] = append(wantIDs[fixture.result], row.RequestId)
						if fixture.typ == model.LogTypeConsume {
							quota[""] += row.Quota
							quota[fixture.result] += row.Quota
						}
					}
				}
				for _, result := range []string{"", "success", "failure"} {
					params := query + "&category=model&model_name=model-audit-fixture&result=" + result
					for _, endpoint := range []gin.HandlerFunc{GetAllLogs, GetUserLogs} {
						response := callLogHandler(t, endpoint, userID, common.RoleAdminUser, params)[0]
						require.Equal(t, true, response["success"], response)
						page := response["data"].(map[string]any)
						assert.EqualValues(t, len(wantIDs[result]), page["total"], result)
						for _, value := range page["items"].([]any) {
							entry := value.(map[string]any)
							failed := slices.Contains(wantIDs["failure"], entry["request_id"])
							assert.Equal(t, failed, strings.Contains(entry["other"].(string), `"error_summary"`), entry["request_id"])
						}
					}
					for _, endpoint := range []gin.HandlerFunc{ExportAllLogs, ExportUserLogs} {
						response := callLogHandler(t, endpoint, userID, common.RoleAdminUser, params)
						assert.ElementsMatch(t, wantIDs[result], exportedRequestIds(response), result)
					}
					for _, endpoint := range []gin.HandlerFunc{GetLogsStat, GetLogsSelfStat} {
						response := callLogHandler(t, endpoint, userID, common.RoleAdminUser, params+"&live_total=1")[0]
						require.Equal(t, true, response["success"], response)
						data := response["data"].(map[string]any)
						assert.EqualValues(t, quota[result], data["quota"], result)
						assert.EqualValues(t, len(wantIDs[result]), data["live_total"], result)
					}
					filter := model.LogFilter{Category: "model", Result: result}
					for _, owner := range []int{0, userID} {
						changes, omitted, err := model.GetChangedLogs(t.Context(), filter, owner, common.RoleAdminUser, true, 100, refs)
						require.NoError(t, err)
						assert.Zero(t, omitted)
						ids := make([]any, 0, len(changes))
						for _, entry := range changes {
							ids = append(ids, entry.RequestId)
						}
						assert.ElementsMatch(t, wantIDs[result], ids, result)
					}
				}
				previousRules := operation_setting.ErrorRecognitionRulesJSONString()
				t.Cleanup(func() { require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(previousRules)) })
				for _, ruleCase := range []struct {
					rules, label string
					matched      bool
				}{
					{`[]`, "Request failed", false},
					{`[{"name":"custom-limit","enabled":true,"label":"Configured limit","match":"contains","pattern":"rate limit"}]`, "Configured limit", true},
				} {
					require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(ruleCase.rules))
					var projected []model.Log
					for _, endpoint := range []gin.HandlerFunc{GetAllLogs, GetUserLogs} {
						response := callLogHandler(t, endpoint, userID, common.RoleRootUser, "category=model&request_id="+malformedLog.RequestId)[0]
						require.Equal(t, true, response["success"], response)
						items := response["data"].(map[string]any)["items"].([]any)
						require.Len(t, items, 1)
						entry := items[0].(map[string]any)
						projected = append(projected, model.Log{Content: entry["content"].(string), Other: entry["other"].(string)})
					}
					for _, owner := range []int{0, userID} {
						changed, _, err := model.GetChangedLogs(t.Context(), model.LogFilter{Category: "model", RequestId: malformedLog.RequestId}, owner, common.RoleRootUser, true, 10, refs)
						require.NoError(t, err)
						require.Len(t, changed, 1)
						projected = append(projected, *changed[0])
					}
					for _, entry := range projected {
						var metadata map[string]common.RawMessage
						require.NoError(t, common.UnmarshalJsonStr(entry.Other, &metadata))
						require.Len(t, metadata, 1, "malformed fragments cannot survive the response projection")
						var summary operation_setting.ErrorSummary
						require.NoError(t, common.Unmarshal(metadata["error_summary"], &summary))
						assert.Equal(t, ruleCase.label, summary.Label)
						assert.Equal(t, ruleCase.matched, summary.Matched)
						assert.Equal(t, malformedLog.Content, entry.Content)
					}
				}
				require.NoError(t, operation_setting.LoadErrorRecognitionRulesJSON(previousRules))
				var storedMalformed model.Log
				require.NoError(t, model.LOG_DB.Where("request_id = ?", malformedLog.RequestId).Take(&storedMalformed).Error)
				assert.Equal(t, malformedLog.Other, storedMalformed.Other)
				assert.Equal(t, malformedLog.Content, storedMalformed.Content)
				for _, endpoint := range []gin.HandlerFunc{GetAllLogs, GetUserLogs, ExportAllLogs, ExportUserLogs, GetLogsStat, GetLogsSelfStat, StreamAllLogChanges, StreamUserLogChanges} {
					response := callLogHandler(t, endpoint, userID, common.RoleAdminUser, "category=model&result=invalid")[0]
					assert.Equal(t, false, response["success"], response)
				}
				conflictingSource := callLogHandler(t, GetAllLogs, userID, common.RoleAdminUser, "category=model&source=test")[0]
				assert.Equal(t, false, conflictingSource["success"], "model requests cannot opt back into legacy probes")
				model.RecordAuditLog(nil, model.AuditLog{UserId: userID, Username: "model-audit-owner", ActorRole: common.RoleCommonUser, CreatedAt: now - 10, EventId: "native-audit", Category: model.AuditCategoryOperation, Content: "native audit", Success: true})
				for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
					filter := model.AuditLogFilter{UserId: userID}
					first, total, err := model.GetAuditLogs(filter, 0, 2, role)
					require.NoError(t, err)
					wantTotal := 4
					if role == common.RoleRootUser {
						wantTotal = 7
					}
					assert.EqualValues(t, wantTotal, total)
					second, _, err := model.GetAuditLogs(filter, 2, 20, role)
					require.NoError(t, err)
					seen := map[string]bool{}
					for _, entry := range append(first, second...) {
						assert.False(t, seen[entry.EventId], entry.EventId)
						seen[entry.EventId] = true
						assert.Equal(t, userID, entry.UserId)
						assert.NotContains(t, entry.Content, "probe")
					}
					assert.Len(t, seen, wantTotal)
				}
				filter := model.AuditLogFilter{UserId: userID, Category: model.AuditCategoryBilling}
				billing, total, err := model.GetAuditLogs(filter, 0, 20, common.RoleCommonUser)
				require.NoError(t, err)
				assert.EqualValues(t, 2, total)
				assert.Len(t, billing, 2)
				for _, entry := range billing {
					require.NotNil(t, entry.SuccessRecorded)
					assert.False(t, *entry.SuccessRecorded)
					assert.Nil(t, entry.Other.RootInfo)
					assert.Nil(t, entry.Other.AdminInfo)
					if entry.RequestId == "model-audit-topup" {
						assert.Contains(t, string(entry.Other.ContentParts), "9007199254740993")
					}
				}
				for _, success := range []bool{false, true} {
					filter.Success = &success
					_, total, err = model.GetAuditLogs(filter, 0, 20, common.RoleRootUser)
					require.NoError(t, err)
					assert.Zero(t, total, "unknown legacy outcomes do not match either result")
				}
				filter.Success = nil
				filter.TokenRef = model.AccessTokenFingerprint("unrelated")
				_, total, err = model.GetAuditLogs(filter, 0, 20, common.RoleRootUser)
				require.NoError(t, err)
				assert.Zero(t, total)
			})
			t.Run("nickname privacy", func(t *testing.T) {
				const username = "nick-fixture"
				const nickname = "Private alias"
				user := model.User{Id: 701, Username: username, DisplayName: "Public profile", Remark: " \r\n  Private alias  \r\nInternal notes", AffCode: "nick701"}
				require.NoError(t, mainDB.Create(&user).Error)
				row := model.Log{Id: 77701, UserId: user.Id, Username: "old-name", ModelName: "nickname-model", RequestId: "nickname-fixture", CreatedAt: now, Type: model.LogTypeConsume, TokenId: 77701}
				orphan := model.Log{Id: 77702, UserId: 702, Username: "archived", ModelName: row.ModelName, RequestId: "nickname-orphan", CreatedAt: now - 1, Type: model.LogTypeConsume}
				require.NoError(t, model.LOG_DB.Create(&row).Error)
				require.NoError(t, model.LOG_DB.Create(&orphan).Error)
				matched, total, err := model.SearchUsers(nickname, "", nil, nil, 0, 5)
				require.NoError(t, err)
				require.EqualValues(t, 1, total)
				require.Len(t, matched, 1)
				assert.Equal(t, username, matched[0].Username)

				filter := model.LogFilter{ModelName: row.ModelName}
				admin, _, err := model.GetAllLogs(filter, 0, 20)
				require.NoError(t, err)
				require.Len(t, admin, 2)
				assert.Equal(t, nickname, admin[0].UserAdminNickname)
				assert.Equal(t, username, admin[0].Username)
				assert.Equal(t, "archived", admin[1].Username)
				assert.Empty(t, admin[1].UserAdminNickname)
				encoded, err := common.Marshal(admin)
				require.NoError(t, err)
				assert.NotContains(t, string(encoded), "Internal notes")

				ref := []model.LiveLogRef{{ID: row.Id, CreatedAt: row.CreatedAt, RequestID: row.RequestId}}
				require.NoError(t, mainDB.Model(&user).UpdateColumn("remark", "Renamed").Error)
				live, _, err := model.GetChangedLogs(t.Context(), filter, 0, common.RoleAdminUser, true, 10, ref)
				require.NoError(t, err)
				require.Len(t, live, 1)
				assert.Equal(t, "Renamed", live[0].UserAdminNickname)
				for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
					self := callLogHandler(t, GetUserLogs, user.Id, role, "model_name="+row.ModelName)[0]
					require.Equal(t, true, self["success"])
					items := self["data"].(map[string]any)["items"].([]any)
					require.Len(t, items, 1)
					assert.NotContains(t, items[0].(map[string]any), "user_admin_nickname")
					live, _, err := model.GetChangedLogs(t.Context(), filter, user.Id, role, true, 10, ref)
					require.NoError(t, err)
					require.Len(t, live, 1)
					assert.Empty(t, live[0].UserAdminNickname)
				}
				tokenRows, err := model.GetLogByTokenId(row.TokenId)
				require.NoError(t, err)
				require.Len(t, tokenRows, 1)
				assert.Empty(t, tokenRows[0].UserAdminNickname)
				own := callLogHandler(t, ExportUserLogs, user.Id, common.RoleCommonUser, "model_name="+row.ModelName)
				require.Len(t, own, 2)
				assert.NotContains(t, own[0], "user_admin_nickname")
				export := callLogHandler(t, ExportAllLogs, 3, common.RoleAdminUser, "model_name="+row.ModelName)
				require.Len(t, export, 3)
				assert.NotContains(t, export[0], "user_admin_nickname")
			})

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

			t.Run("retry groups preserve final results across pages", func(t *testing.T) {
				const owner = 775
				const name = "retry-group-contract"
				rows := []model.Log{
					{Id: 977501, UserId: owner, TokenId: 75, CreatedAt: now - 180, Type: model.LogTypeError, ModelName: name, RequestId: "retry-client-first", Content: "status_code=429, rate limit", Other: `{}`},
					{Id: 977502, UserId: owner, TokenId: 75, CreatedAt: now - 120, Type: model.LogTypeConsume, ModelName: name, RequestId: "retry-client-final", UseTime: 10, Quota: 7, Other: `{}`},
					{Id: 977503, UserId: owner, TokenId: 75, CreatedAt: now - 90, Type: model.LogTypeError, ModelName: name, RequestId: "retry-internal", Other: `{}`},
					{Id: 977504, UserId: owner, TokenId: 75, CreatedAt: now - 80, Type: model.LogTypeConsume, ModelName: name, RequestId: "retry-internal", Quota: 9, Other: `{}`},
					{Id: 977505, UserId: owner, TokenId: 75, CreatedAt: now - 60, Type: model.LogTypeConsume, ModelName: name, RequestId: "retry-new-success", Quota: 11, Other: `{}`},
				}
				for i := range rows {
					require.NoError(t, model.LOG_DB.Create(&rows[i]).Error)
				}
				for _, index := range []int{0, 1, 4} {
					row := rows[index]
					require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{{TraceId: row.RequestId + "-trace", RequestId: row.RequestId, CreatedAt: row.CreatedAt, Direction: model.TraceDirectionClientRequest, ConversationUserId: owner, ConversationKey: "c_explicit-retry-session", Format: "claude", Method: "POST", Url: "/v1/messages", Body: `{"model":"retry-group-contract","messages":[{"role":"user","content":"same full request"}]}`, BodySize: int64(len(`{"model":"retry-group-contract","messages":[{"role":"user","content":"same full request"}]}`))}}))
				}
				params := fmt.Sprintf("category=model&model_name=%s&start_timestamp=%d&end_timestamp=%d&page_size=1", name, now-300, now)
				for _, handler := range []gin.HandlerFunc{GetAllLogs, GetUserLogs} {
					for page, request := range []string{"retry-new-success", "retry-internal", "retry-client-final"} {
						response := callLogHandler(t, handler, owner, common.RoleAdminUser, params+fmt.Sprintf("&p=%d", page+1))[0]
						require.Equal(t, true, response["success"], response)
						data := response["data"].(map[string]any)
						assert.EqualValues(t, 3, data["total"])
						items := data["items"].([]any)
						require.Len(t, items, 1)
						item := items[0].(map[string]any)
						assert.Equal(t, request, item["request_id"])
						if page > 0 {
							assert.EqualValues(t, 1, item["retry_count"])
						}
					}
					failed := callLogHandler(t, handler, owner, common.RoleAdminUser, params+"&result=failure")[0]["data"].(map[string]any)
					assert.EqualValues(t, 0, failed["total"])
				}
				var rawCount int64
				require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("model_name = ?", name).Count(&rawCount).Error)
				assert.EqualValues(t, 5, rawCount, "raw attempts remain intact")
				var rawQuota int64
				require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("model_name = ?", name).Select("sum(quota)").Scan(&rawQuota).Error)
				assert.EqualValues(t, 27, rawQuota, "billing is never consolidated or rewritten")
				filter := model.LogFilter{Category: "model", ModelName: name, StartTimestamp: now - 300, EndTimestamp: now, Live: true}
				ownerID := owner
				stat, err := model.SumUsedQuota(t.Context(), filter, &ownerID, false)
				require.NoError(t, err)
				assert.Equal(t, 27, stat.Quota)
				require.NotNil(t, stat.LiveTotal)
				assert.EqualValues(t, 3, *stat.LiveTotal)
				searchedFilter := filter
				searchedFilter.RequestId = "retry-client-first"
				searchedStat, err := model.SumUsedQuota(t.Context(), searchedFilter, &ownerID, false)
				require.NoError(t, err)
				assert.Equal(t, 7, searchedStat.Quota, "original request lookup uses the final result for list and spending")
				export := callLogHandler(t, ExportAllLogs, owner, common.RoleAdminUser, params)
				assert.ElementsMatch(t, []any{"retry-new-success", "retry-internal", "retry-client-final"}, exportedRequestIds(export))
				searched := callLogHandler(t, GetAllLogs, owner, common.RoleAdminUser, params+"&request_id=retry-client-first")[0]["data"].(map[string]any)
				assert.EqualValues(t, 1, searched["total"])
				assert.Equal(t, "retry-client-final", searched["items"].([]any)[0].(map[string]any)["request_id"])

				detailQuery := fmt.Sprintf("request_id=retry-client-final&start_timestamp=%d&end_timestamp=%d", now-180, now-120)
				for _, handler := range []gin.HandlerFunc{GetAllLogRetryAttempts, GetSelfLogRetryAttempts} {
					response := callLogHandler(t, handler, owner, common.RoleCommonUser, detailQuery)[0]
					require.Equal(t, true, response["success"], response)
					attempts := response["data"].([]any)
					require.Len(t, attempts, 2)
					assert.Equal(t, "retry-client-first", attempts[0].(map[string]any)["request_id"])
					assert.Equal(t, "retry-client-final", attempts[1].(map[string]any)["request_id"])
					assert.NotEmpty(t, attempts[0].(map[string]any)["live_key"])
					assert.NotEqual(t, attempts[0].(map[string]any)["live_key"], attempts[1].(map[string]any)["live_key"])
				}
				denied := callLogHandler(t, GetSelfLogRetryAttempts, owner+1, common.RoleCommonUser, detailQuery)[0]
				assert.Equal(t, false, denied["success"], "attempt lookup cannot cross owners")
				refs := []model.LiveLogRef{{ID: rows[1].Id, UserID: owner, CreatedAt: rows[1].CreatedAt, RequestID: rows[1].RequestId}}
				page, err := model.GetLogChangePage(t.Context(), filter, owner, common.RoleCommonUser, true, 20, refs)
				require.NoError(t, err)
				assert.True(t, page.Replace)
				assert.EqualValues(t, 3, page.Total)
				require.Len(t, page.Items, 3)
				assert.NotEmpty(t, page.Items[0].LiveKey)
				filter.Result = "failure"
				page, err = model.GetLogChangePage(t.Context(), filter, owner, common.RoleCommonUser, true, 20, refs)
				require.NoError(t, err)
				assert.True(t, page.Replace, "success removes a previous error from the failure view")
				assert.Zero(t, page.Total)
				assert.NotNil(t, page.Items)
				assert.Empty(t, page.Items)
				// A long overlapping sibling can finish later. Its delayed trace must restore
				// a previously folded failure rather than merely append another success.
				parallel := model.Log{Id: 977506, UserId: owner, TokenId: 75, CreatedAt: now - 110, Type: model.LogTypeConsume, ModelName: name, RequestId: "retry-parallel", UseTime: 100, Quota: 1, Other: `{}`}
				require.NoError(t, model.LOG_DB.Create(&parallel).Error)
				body := `{"model":"retry-group-contract","messages":[{"role":"user","content":"same full request"}]}`
				require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{{TraceId: parallel.RequestId, RequestId: parallel.RequestId, CreatedAt: parallel.CreatedAt, Direction: model.TraceDirectionClientRequest, ConversationUserId: owner, ConversationKey: "c_explicit-retry-session", Method: "POST", Url: "/v1/messages", Body: body, BodySize: int64(len(body))}}))
				filter.Result = ""
				page, err = model.GetLogChangePage(t.Context(), filter, owner, common.RoleCommonUser, true, 20, []model.LiveLogRef{{ID: parallel.Id, UserID: owner, CreatedAt: parallel.CreatedAt, RequestID: parallel.RequestId}})
				require.NoError(t, err)
				assert.True(t, page.Replace)
				assert.EqualValues(t, 5, page.Total)
				assert.True(t, slices.ContainsFunc(page.Items, func(row *model.Log) bool { return row.RequestId == "retry-client-first" }))
			})

			t.Run("retry grouping rejects uncertain client matches", func(t *testing.T) {
				for index, reason := range []string{"different body", "different token", "different model", "different path", "different method", "missing session", "truncated", "incomplete body", "too late", "overlap", "unknown duration", "billed error", "partial consume", "independent success", "intervening request"} {
					t.Run(reason, func(t *testing.T) {
						owner := 1780 + index
						name := fmt.Sprintf("retry-guard-%d", index)
						first := model.Log{Id: 978000 + index*10, UserId: owner, TokenId: 95, CreatedAt: now - 400, Type: model.LogTypeError, ModelName: name, RequestId: name + "-first", Other: `{}`}
						next := first
						next.Id++
						next.CreatedAt, next.Type, next.RequestId, next.UseTime, next.Quota = now-200, model.LogTypeConsume, name+"-next", 10, 5
						body := `{"messages":[{"role":"user","content":"hello"}]}`
						original := model.RequestTrace{TraceId: first.RequestId, RequestId: first.RequestId, CreatedAt: first.CreatedAt, ConversationKey: "c_" + name, ConversationUserId: owner, Direction: model.TraceDirectionClientRequest, Method: "POST", Url: "/v1/messages", Body: body, BodySize: int64(len(body))}
						candidate := original
						candidate.TraceId, candidate.RequestId, candidate.CreatedAt = next.RequestId, next.RequestId, next.CreatedAt
						switch reason {
						case "different body":
							candidate.Body = strings.ReplaceAll(body, "hello", "world")
						case "different token":
							next.TokenId++
						case "different model":
							next.ModelName += "-changed"
						case "different path":
							candidate.Url = "/v1/responses"
						case "different method":
							candidate.Method = "PUT"
						case "missing session":
							original.ConversationKey, candidate.ConversationKey = first.RequestId, next.RequestId
						case "truncated":
							candidate.Truncated = true
						case "incomplete body":
							candidate.BodySize++
						case "too late":
							first.CreatedAt, original.CreatedAt = now-800, now-800
						case "overlap":
							next.UseTime = 250
						case "unknown duration":
							next.UseTime = 0
						case "billed error":
							first.Quota = 1
						case "partial consume":
							first.Type, first.PromptTokens = model.LogTypeConsume, 20
							first.Other = `{"stream_status":{"response_status":"incomplete"}}`
						case "independent success":
							first.Type, first.Quota = model.LogTypeConsume, 1
						}
						require.NoError(t, model.LOG_DB.Create(&first).Error)
						require.NoError(t, model.LOG_DB.Create(&next).Error)
						require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{&original, &candidate}))
						want := int64(2)
						if reason == "intervening request" {
							barrier := next
							barrier.Id, barrier.CreatedAt, barrier.RequestId = next.Id+1, now-300, name+"-barrier"
							trace := candidate
							trace.Id = 0
							trace.TraceId, trace.RequestId, trace.CreatedAt, trace.Body = barrier.RequestId, barrier.RequestId, barrier.CreatedAt, strings.ReplaceAll(body, "hello", "world")
							require.NoError(t, model.LOG_DB.Create(&barrier).Error)
							require.NoError(t, model.SaveRequestTraces([]*model.RequestTrace{&trace}))
							want++
						}
						rows, total, err := model.GetUserLogs(owner, model.LogFilter{Category: "model", StartTimestamp: now - 1000, EndTimestamp: now}, 0, 20)
						require.NoError(t, err)
						assert.Equal(t, want, total)
						for _, row := range rows {
							assert.Zero(t, row.RetryCount)
						}
					})
				}
			})

			t.Run("retry attempt order uses recorded clock", func(t *testing.T) {
				firstID, finalID := 979001, 979002
				if tc.kind == "clickhouse" {
					firstID, finalID = finalID, firstID // ClickHouse IDs do not encode chronology.
				}
				for _, row := range []model.Log{
					{Id: firstID, UserId: 1799, CreatedAt: now - 30, Type: model.LogTypeError, RequestId: "retry-same-second", Other: `{"duration_ms":100,"admin_info":{"request_policy":[{"attempt":1,"elapsed_ms":100}]}}`},
					{Id: finalID, UserId: 1799, CreatedAt: now - 30, Type: model.LogTypeError, RequestId: "retry-same-second", Content: "final failure", Other: `{"duration_ms":200,"admin_info":{"request_policy":[{"attempt":2,"elapsed_ms":200}]}}`},
				} {
					require.NoError(t, model.LOG_DB.Create(&row).Error)
				}
				rows, total, err := model.GetUserLogs(1799, model.LogFilter{Category: "model", StartTimestamp: now - 60, EndTimestamp: now, Result: "failure"}, 0, 20)
				require.NoError(t, err)
				assert.EqualValues(t, 1, total)
				require.Len(t, rows, 1)
				assert.Equal(t, "final failure", rows[0].Content)
				assert.Equal(t, 1, rows[0].RetryCount)
				attempts, err := model.GetLogRetryAttempts(t.Context(), "retry-same-second", now-30, now-30, 1799, common.RoleCommonUser)
				require.NoError(t, err)
				require.Len(t, attempts, 2)
				assert.Empty(t, attempts[0].Content)
				assert.Equal(t, "final failure", attempts[1].Content)
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
		for _, line := range []string{"event: ready", "data: {}", ""} {
			require.True(t, s.Scan(), "missing %q: %v", line, s.Err())
			require.Equal(t, line, s.Text())
		}
		found := false
		for s.Scan() {
			if s.Text() == "event: "+want {
				found = true
				break
			}
		}
		require.True(t, found, "missing %s event: %v", want, s.Err())
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

// A streaming response must be visible while the upstream is still open. It
// remains ephemeral until the normal writer records its final usage.
func TestActiveLogStreamLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.User{}))
	require.NoError(t, db.Create(&model.User{Id: 881, Username: "active-reader", AffCode: "active1"}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 889, Name: "active-channel"}).Error)
	oldEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	defer func() { common.LogConsumeEnabled = oldEnabled }()
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	engine := gin.New()
	engine.GET("/self", func(c *gin.Context) { c.Set("id", 881); c.Set("role", common.RoleCommonUser); StreamUserLogChanges(c) })
	engine.GET("/other", func(c *gin.Context) { c.Set("id", 882); c.Set("role", common.RoleCommonUser); StreamUserLogChanges(c) })
	engine.GET("/admin", func(c *gin.Context) { c.Set("role", common.RoleAdminUser); StreamAllLogChanges(c) })
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		defer close(finished)
		c.Set("id", 881)
		c.Set("username", "active-reader")
		c.Set("token_name", "active-token")
		c.Set(common.RequestIdKey, "active-stream-request")
		common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
		info := &relaycommon.RelayInfo{UserId: 881, TokenId: 883, RequestId: "active-stream-request", OriginModelName: "active-model", UsingGroup: "active-group", StartTime: time.Now(), IsStream: true, RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 889}, Request: &dto.GeneralOpenAIRequest{ReasoningEffort: "high"}}
		defer service.BeginRequestMetadata(c, info)()
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"private response\"}}]}\n\n")
		c.Writer.Flush()
		<-release
		_, _ = c.Writer.WriteString("data: [DONE]\n\n")
		model.RecordConsumeLog(c, 881, model.RecordConsumeLogParams{ChannelId: 889, ModelName: "active-model", TokenName: "active-token", TokenId: 883, Group: "active-group", IsStream: true, PromptTokens: 20, CompletionTokens: 3, Quota: 7})
	})
	server := httptest.NewServer(engine)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		server.Close()
	}()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"content":"private prompt"}]}`))
	require.NoError(t, err)
	req.Header.Set("User-Agent", "codex_cli_rs/1.2.3")
	response, err := server.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	readActive := func(path string) []map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path+"&cursor="+model.LiveLogCursor(), nil)
		require.NoError(t, err)
		res, err := server.Client().Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		scan := bufio.NewScanner(res.Body)
		for scan.Scan() {
			if scan.Text() != "event: active" {
				continue
			}
			require.True(t, scan.Scan())
			var packet struct {
				Items []map[string]any `json:"items"`
			}
			require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(scan.Text(), "data: "), &packet))
			return packet.Items
		}
		require.FailNow(t, "active snapshot missing before the upstream stream completed", "%v", scan.Err())
		return nil
	}
	self := readActive("/self?category=model")
	require.Len(t, self, 1)
	assert.Equal(t, true, self[0]["in_progress"])
	assert.Equal(t, "active-stream-request", self[0]["request_id"])
	assert.NotEmpty(t, self[0]["live_key"])
	assert.Equal(t, float64(0), self[0]["quota"])
	assert.Equal(t, float64(0), self[0]["prompt_tokens"])
	assert.NotContains(t, self[0]["other"], "user_agent")
	assert.NotContains(t, self[0]["other"], "private")
	assert.Empty(t, self[0]["channel_name"])
	assert.Empty(t, readActive("/other?category=model"))
	assert.Empty(t, readActive("/self?category=model&result=success"))
	assert.Empty(t, readActive("/self?category=model&result=failure"))
	assert.Empty(t, readActive("/self?category=model&model_name=other"))
	admin := readActive("/admin?category=model&model_name=active-model&group=active-group&token_name=active-token&channel=889&reasoning_effort=high&response_transport=sse")
	require.Len(t, admin, 1)
	assert.Contains(t, admin[0]["other"], "codex_cli_rs/1.2.3")
	assert.Equal(t, self[0]["live_key"], admin[0]["live_key"])
	reconnected := readActive("/self?category=model")
	require.Len(t, reconnected, 1)
	assert.Equal(t, self[0]["live_key"], reconnected[0]["live_key"])
	var count int64
	require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count, "in-progress data must not enter accounting or history")
	// An intermediate retry error is committed, but only the request lifetime
	// can remove the authoritative active projection.
	require.NoError(t, db.Create(&model.Log{UserId: 881, RequestId: "active-stream-request", CreatedAt: time.Now().Unix(), Type: model.LogTypeError, ModelName: "active-model"}).Error)
	retrying := readActive("/self?category=model")
	require.Len(t, retrying, 1)
	assert.Equal(t, self[0]["live_key"], retrying[0]["live_key"])
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not finish")
	}
	assert.Empty(t, readActive("/self?category=model"))
	var logs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, "active-stream-request", logs[0].RequestId)
	assert.Equal(t, 7, logs[0].Quota)
	assert.Equal(t, 20, logs[0].PromptTokens)
}

func TestActiveLogRedisPeersAndCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := newAuditTestDatabase(t, "sqlite", "")
	useLogExportDatabases(t, db, db, common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.User{}))
	require.NoError(t, db.Create(&model.User{Id: 901, Username: "active-owner", AffCode: "active-owner"}).Error)
	require.NoError(t, db.Create(&model.User{Id: 902, Username: "active-other", AffCode: "active-other"}).Error)
	previousClient, previousSecret := common.RDB, common.SessionSecret
	defer func() { common.RDB, common.SessionSecret = previousClient, previousSecret }()
	common.SessionSecret = "isolated-active-log-peer-regression"
	options := &redis.Options{Addr: os.Getenv("TEST_REDIS_ADDR"), Password: os.Getenv("TEST_REDIS_PASSWORD"), MaxRetries: -1}
	if options.Addr == "" {
		options.Addr = miniredis.RunT(t).Addr()
	}
	client := redis.NewClient(options)
	defer client.Close()
	require.NoError(t, client.Ping(t.Context()).Err())
	common.RDB, common.RedisEnabled = client, true
	key := fmt.Sprintf("usage_log_events:%x:active", sha256.Sum256([]byte(common.SessionSecret)))
	require.NoError(t, client.Del(t.Context(), key).Err())
	defer client.Del(context.Background(), key)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model.StartLiveLogFeed(ctx)
	peer := func(expires int64, rows []model.Log) {
		t.Helper()
		data, err := common.Marshal(map[string]any{"expires": expires, "items": rows})
		require.NoError(t, err)
		require.NoError(t, client.HSet(t.Context(), key, "other-gateway", string(data)).Err())
	}
	snapshot := func(owner int) []*model.Log {
		t.Helper()
		rows, err := model.ActiveLogSnapshot(t.Context(), model.LogFilter{Source: model.LogSourceUsage, Category: "model"}, owner, common.RoleCommonUser)
		require.NoError(t, err)
		return rows
	}
	now := time.Now().Unix()
	peer(now+30, []model.Log{
		{LiveKey: "peer-a", InProgress: true, UserId: 901, CreatedAt: now, RequestId: "peer-request", IsStream: true, Other: `{"response_transport":"sse","admin_info":{"user_agent":"private peer UA"}}`},
		{LiveKey: "peer-b", InProgress: true, UserId: 902, CreatedAt: now, RequestId: "other-account", IsStream: true},
	})
	require.Eventually(t, func() bool { return len(snapshot(901)) == 1 }, 4*time.Second, 20*time.Millisecond)
	rows := snapshot(901)
	assert.Equal(t, "peer-request", rows[0].RequestId)
	assert.NotContains(t, rows[0].Other, "private peer UA")
	peer(now+30, []model.Log{})
	require.Eventually(t, func() bool { return len(snapshot(901)) == 0 }, 4*time.Second, 20*time.Millisecond, "a peer's replacement removes completed requests")
	peer(now-1, []model.Log{{LiveKey: "peer-stale", InProgress: true, UserId: 901, CreatedAt: now, RequestId: "expired-request", IsStream: true}})
	require.Eventually(t, func() bool { return !client.HExists(t.Context(), key, "other-gateway").Val() }, 4*time.Second, 20*time.Millisecond)
	assert.Empty(t, snapshot(901), "crashed peers expire instead of leaving permanent pending rows")
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	defer cancelRequest()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	c.Set("username", "active-owner")
	info := &relaycommon.RelayInfo{UserId: 901, RequestId: "cancelled-active-request", OriginModelName: "model", IsStream: true, StartTime: time.Now(), RelayFormat: types.RelayFormatOpenAIResponses, RelayMode: relayconstant.RelayModeResponses}
	finish := service.BeginRequestMetadata(c, info)
	defer finish()
	require.Len(t, snapshot(901), 1)
	require.Eventually(t, func() bool {
		for _, raw := range client.HGetAll(t.Context(), key).Val() {
			if strings.Contains(raw, "cancelled-active-request") {
				return true
			}
		}
		return false
	}, 4*time.Second, 20*time.Millisecond, "another gateway can discover the active local request")
	cancelRequest()
	require.Eventually(t, func() bool { return len(snapshot(901)) == 0 }, time.Second, 10*time.Millisecond)
	c.Set("channel_id", 7)
	service.ResetRequestToolObservation(c)
	assert.Empty(t, snapshot(901), "a late retry update cannot resurrect cancellation")
	finish()
	require.Eventually(t, func() bool {
		for _, raw := range client.HGetAll(t.Context(), key).Val() {
			if strings.Contains(raw, "cancelled-active-request") {
				return false
			}
		}
		return true
	}, 4*time.Second, 20*time.Millisecond, "completion is removed from the shared snapshot")
	var count int64
	require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
}
