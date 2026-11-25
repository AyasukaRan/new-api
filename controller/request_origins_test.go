package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestOriginsDatabaseMatrix(t *testing.T) {
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
			require.NoError(t, mainDB.AutoMigrate(&model.User{}))
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
			require.NoError(t, mainDB.Create(&model.User{Id: 1, Username: "alice", AffCode: "geo-alice"}).Error)
			require.NoError(t, mainDB.Create(&model.User{Id: 2, Username: "bob", AffCode: "geo-bob"}).Error)
			now := time.Now().Unix()
			rows := []model.Log{
				{Id: 101, UserId: 1, Username: "alice", Ip: "8.8.8.8", CreatedAt: now - 20, RequestId: "alice-success", Type: model.LogTypeConsume},
				{Id: 102, UserId: 2, Username: "bob", Ip: "1.1.1.1", CreatedAt: now - 15, RequestId: "bob-success", Type: model.LogTypeConsume},
				{Id: 103, UserId: 1, Username: "alice", Ip: "192.168.1.1", CreatedAt: now - 10, RequestId: "alice-error", Type: model.LogTypeError},
				{Id: 104, UserId: 1, Username: "alice", CreatedAt: now - 10, Type: model.LogTypeManage},
				{Id: 105, UserId: 1, Username: "alice", CreatedAt: now - 10, Type: model.LogTypeTestConsume},
				{Id: 106, UserId: 1, Username: "alice", CreatedAt: now - 10, Type: model.LogTypeConsume, TokenName: "模型测试", Content: "模型测试"},
				{Id: 107, UserId: 1, Username: "alice", CreatedAt: now - 10000, Type: model.LogTypeConsume},
			}
			for i := range rows {
				require.NoError(t, model.LOG_DB.Create(&rows[i]).Error)
			}
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", now-100, now)
			self := callLogHandler(t, GetUserRequestOrigins, 1, common.RoleCommonUser, query+"&username=bob&user_id=2")[0]
			require.Equal(t, true, self["success"])
			data := self["data"].(map[string]any)
			assert.EqualValues(t, 2, data["total"])
			assert.EqualValues(t, 1, data["private"])
			assert.Equal(t, false, data["truncated"])
			assert.NotEmpty(t, data["cursor"])
			encoded, err := common.Marshal(data)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "8.8.8.8")
			assert.NotContains(t, string(encoded), "192.168.1.1")
			assert.NotContains(t, string(encoded), "alice")
			all := callLogHandler(t, GetAllRequestOrigins, 3, common.RoleAdminUser, query)[0]
			assert.EqualValues(t, 3, all["data"].(map[string]any)["total"])
			filtered := callLogHandler(t, GetAllRequestOrigins, 3, common.RoleAdminUser, query+"&username=alice")[0]
			assert.EqualValues(t, 2, filtered["data"].(map[string]any)["total"])
			refs := []model.LiveLogRef{
				{ID: 101, UserID: 1, CreatedAt: rows[0].CreatedAt, RequestID: rows[0].RequestId},
				{ID: 102, UserID: 2, CreatedAt: rows[1].CreatedAt, RequestID: rows[1].RequestId},
				{ID: 101, UserID: 1, CreatedAt: rows[0].CreatedAt, RequestID: rows[0].RequestId},
			}
			changed, truncated, err := model.GetRequestOriginRows(context.Background(), model.LogFilter{StartTimestamp: now - 100, Username: "bob"}, 1, refs)
			require.NoError(t, err)
			assert.False(t, truncated)
			require.Len(t, changed, 1)
			assert.Equal(t, "alice-success", changed[0].RequestId)
		})
	}
}

func TestRequestOriginsAccessAndParameters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		handler gin.HandlerFunc
		id      int
		role    int
		query   string
		status  int
	}{
		{"anonymous self", GetUserRequestOrigins, 0, 0, "", http.StatusForbidden},
		{"unprivileged all", GetAllRequestOrigins, 1, common.RoleCommonUser, "", http.StatusForbidden},
		{"unprivileged all stream", StreamAllRequestOrigins, 1, common.RoleCommonUser, "", http.StatusForbidden},
		{"malformed start", GetUserRequestOrigins, 1, common.RoleCommonUser, "start_timestamp=invalid", http.StatusBadRequest},
		{"reversed range", GetUserRequestOrigins, 1, common.RoleCommonUser, "start_timestamp=100&end_timestamp=99", http.StatusBadRequest},
		{"negative end", StreamUserRequestOrigins, 1, common.RoleCommonUser, "end_timestamp=-1", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/origins?"+tc.query, nil)
			ctx.Set("id", tc.id)
			ctx.Set("role", tc.role)
			tc.handler(ctx)
			assert.Equal(t, tc.status, recorder.Code)
			assert.NotContains(t, recorder.Body.String(), "event:")
		})
	}
	t.Run("expired stream cursor requests a snapshot without replaying logs", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/origins/self/stream?cursor=expired", nil)
		ctx.Set("id", 1)
		ctx.Set("role", common.RoleCommonUser)
		StreamUserRequestOrigins(ctx)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "event: ready")
		assert.Contains(t, recorder.Body.String(), "event: reset")
		assert.NotContains(t, recorder.Body.String(), "event: origins")
	})
}

func TestRequestOriginsGeoIP(t *testing.T) {
	missing := common.NewGeoIPResolver(filepath.Join(t.TempDir(), "absent.mmdb"))
	t.Cleanup(func() { require.NoError(t, missing.Close()) })
	available, updatedAt := missing.Status()
	assert.False(t, available)
	assert.Zero(t, updatedAt)
	for _, tc := range []struct{ ip, status string }{
		{"", "invalid"}, {"not-an-ip", "invalid"}, {"8.8.8.8:443", "invalid"},
		{"127.0.0.1", "private"}, {"::1", "private"}, {"10.0.0.2", "private"},
		{"192.168.2.1", "private"}, {"100.64.0.1", "private"}, {"fc00::1", "private"},
		{"::ffff:192.168.1.1", "private"}, {"192.0.2.1", "private"}, {"2001:db8::1", "private"},
		{"8.8.8.8", "unmapped"},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			_, status := missing.Lookup(tc.ip)
			assert.Equal(t, tc.status, status)
		})
	}
	path := os.Getenv("GEOIP_TEST_DATABASE_PATH")
	if path == "" {
		t.Skip("GEOIP_TEST_DATABASE_PATH is not configured for verified database checks")
	}
	synctest.Test(t, func(t *testing.T) {
		active := filepath.Join(t.TempDir(), "city.mmdb")
		require.NoError(t, os.Symlink(path, active))
		resolver := common.NewGeoIPResolver(active)
		t.Cleanup(func() { require.NoError(t, resolver.Close()) })
		available, updatedAt = resolver.Status()
		require.True(t, available)
		assert.Positive(t, updatedAt)
		first, status := resolver.Lookup("8.8.8.8")
		require.Equal(t, "located", status)
		assert.Len(t, first.CountryCode, 2)
		mapped, status := resolver.Lookup("::ffff:8.8.8.8")
		assert.Equal(t, "located", status)
		assert.Equal(t, first, mapped)
		_, status = resolver.Lookup("2606:4700:4700::1111")
		assert.Equal(t, "located", status)
		result := aggregateRequestOrigins([]model.RequestOriginRow{
			{Ip: "8.8.8.8", CreatedAt: 1}, {Ip: "::ffff:8.8.8.8", CreatedAt: 2},
			{Ip: "127.0.0.1", CreatedAt: 3}, {Ip: "invalid", CreatedAt: 4},
		}, resolver)
		require.Len(t, result.Points, 1)
		assert.Equal(t, 4, result.Total)
		assert.Equal(t, 2, result.Located)
		assert.Equal(t, 2, result.Unmapped)
		assert.Equal(t, 1, result.Private)
		assert.Equal(t, 1, result.Invalid)
		assert.Equal(t, 2, result.Points[0].Count)
		assert.EqualValues(t, 2, result.Points[0].LastSeen)
		bad := active + ".replacement"
		require.NoError(t, os.WriteFile(bad, []byte("incomplete update"), 0600))
		require.NoError(t, os.Rename(bad, active))
		// Virtual time advances the reload clock without delaying the test.
		time.Sleep(31 * time.Second)
		available, _ = resolver.Status()
		assert.True(t, available)
		after, status := resolver.Lookup("8.8.8.8")
		assert.Equal(t, "located", status)
		assert.Equal(t, first, after, "a corrupt replacement must retain the last verified database")
	})
}
