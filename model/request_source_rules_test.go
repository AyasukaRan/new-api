package model

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

func TestRequestSourceRulesOptionDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			if dialect != "sqlite" {
				name := "TEST_" + strings.ToUpper(dialect) + "_DSN"
				dsn = strings.TrimSpace(os.Getenv(name))
				if dsn == "" {
					t.Skip(name + " is not configured")
				}
			}
			oldPath := common.SQLitePath
			common.SQLitePath = filepath.Join(t.TempDir(), "request-sources.db")
			t.Cleanup(func() { common.SQLitePath = oldPath })
			t.Setenv("REQUEST_SOURCE_TEST_DSN", dsn)
			db, dbType, err := chooseDB("REQUEST_SOURCE_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			db.NamingStrategy = schema.NamingStrategy{TablePrefix: fmt.Sprintf("request_source_%d_", time.Now().UnixNano())}
			require.NoError(t, db.AutoMigrate(&Option{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&Option{})) })
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database: %s", version)
			columns, err := db.Migrator().ColumnTypes(&Option{})
			require.NoError(t, err)
			valueColumnFound := false
			for _, column := range columns {
				if column.Name() == "value" {
					length, hasLength := column.Length()
					t.Logf("Option.Value: type=%s length=%d has_length=%t", column.DatabaseTypeName(), length, hasLength)
					valueColumnFound = true
				}
			}
			require.True(t, valueColumnFound)

			oldDB, oldOptions := DB, common.OptionMap
			oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			oldRules := operation_setting.RequestSourceRulesJSONString()
			oldPolicy := requestPolicySnapshot.Load()
			oldPasskey, oldServerAddress := *system_setting.GetPasskeySettings(), system_setting.ServerAddress
			passkey := system_setting.GetPasskeySettings()
			passkey.RPID, passkey.LegacyRPIDs, passkey.Origins = "source.example.test", "", "https://source.example.test"
			system_setting.ServerAddress = "https://source.example.test"
			DB, common.OptionMap = db, map[string]string{}
			common.SetDatabaseTypes(dbType, oldLogType)
			initCol()
			t.Cleanup(func() {
				DB, common.OptionMap = oldDB, oldOptions
				common.SetDatabaseTypes(oldMainType, oldLogType)
				initCol()
				requestPolicySnapshot.Store(oldPolicy)
				*system_setting.GetPasskeySettings(), system_setting.ServerAddress = oldPasskey, oldServerAddress
				require.NoError(t, operation_setting.LoadRequestSourceRulesJSON(oldRules))
			})
			require.NoError(t, operation_setting.LoadRequestSourceRulesJSON(`[]`))
			const key = operation_setting.RequestSourceRulesOptionKey
			const builtinKey = operation_setting.BuiltinRequestSourceOptionPrefix + "Codex CLI"
			originalBuiltin, err := common.Marshal(operation_setting.BuiltinRequestSourceSettingFor("Codex CLI"))
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, operation_setting.LoadBuiltinRequestSourceOption(builtinKey, string(originalBuiltin)))
			})
			require.NoError(t, UpdateOption(builtinKey, `{"enabled":false,"display_name":"CLI"}`))
			require.NoError(t, operation_setting.LoadBuiltinRequestSourceOption(builtinKey, `{"enabled":true}`))
			loadOptionsFromDatabase()
			assert.Equal(t, operation_setting.BuiltinRequestSourceSetting{Enabled: false, DisplayName: "CLI"}, operation_setting.BuiltinRequestSourceSettingFor("Codex CLI"))
			require.Error(t, UpdateOption(builtinKey, `{"enabled":true,"display_name":"Bearer token"}`))
			assert.False(t, operation_setting.BuiltinRequestSourceSettingFor("Codex CLI").Enabled)
			require.NoError(t, UpdateOption(builtinKey, `{"enabled":true,"display_name":"Codex CLI"}`))
			assert.Equal(t, operation_setting.BuiltinRequestSourceSetting{Enabled: true}, operation_setting.BuiltinRequestSourceSettingFor("Codex CLI"))
			const first = `[{"enabled":true,"name":"测试客户端","conditions":[{"header":"User-Agent","match":"prefix","value":"source-test/","case_sensitive":false}]}]`
			header := http.Header{"User-Agent": []string{"source-test/1.0"}}
			require.NoError(t, UpdateOption("request_source_unrelated", "preserved"))
			require.NoError(t, UpdateOption(key, first))
			assert.Equal(t, "测试客户端", operation_setting.IdentifyConfiguredRequestSource(header))
			var row Option
			require.NoError(t, db.First(&row, Option{Key: key}).Error)
			assert.JSONEq(t, first, row.Value)
			for range 2 {
				require.NoError(t, operation_setting.LoadRequestSourceRulesJSON(`[]`))
				loadOptionsFromDatabase()
				assert.Equal(t, "测试客户端", operation_setting.IdentifyConfiguredRequestSource(header))
				assert.JSONEq(t, first, common.OptionMap[key])
				assert.Equal(t, "preserved", common.OptionMap["request_source_unrelated"])
			}

			const invalid = `[{"enabled":true,"name":"Bad","conditions":[{"header":"User-Agent","match":"regex","value":"["}]}]`
			require.Error(t, UpdateOptionsBulk(map[string]string{key: invalid, "request_source_unrelated": "must-not-save"}))
			require.NoError(t, db.First(&row, Option{Key: key}).Error)
			assert.JSONEq(t, first, row.Value)
			assert.Equal(t, "测试客户端", operation_setting.IdentifyConfiguredRequestSource(header))
			assert.Equal(t, "preserved", common.OptionMap["request_source_unrelated"])

			// A bad external database edit must not replace the last good runtime rules.
			require.NoError(t, db.Model(&Option{}).Where(&Option{Key: key}).Update("value", invalid).Error)
			loadOptionsFromDatabase()
			assert.Equal(t, "测试客户端", operation_setting.IdentifyConfiguredRequestSource(header))
			assert.JSONEq(t, first, common.OptionMap[key])

			second := strings.Replace(first, "测试客户端", "Updated client", 1)
			require.NoError(t, db.Model(&Option{}).Where(&Option{Key: key}).Update("value", second).Error)
			loadOptionsFromDatabase()
			assert.Equal(t, "Updated client", operation_setting.IdentifyConfiguredRequestSource(header))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			DB = db.WithContext(ctx)
			err = UpdateOption(key, first)
			DB = db
			require.Error(t, err, "failed persistence must not report a successful setting update")
			assert.Equal(t, "Updated client", operation_setting.IdentifyConfiguredRequestSource(header))
			require.NoError(t, db.First(&row, Option{Key: key}).Error)
			assert.JSONEq(t, second, row.Value)

			mixed := map[string]string{"passkey.rp_id": "source.example.test", key: first}
			var beforeCount int64
			require.NoError(t, db.Model(&Option{}).Count(&beforeCount).Error)
			preview, err := UpdatePasskeyDomainOptions(mixed, true, "")
			require.NoError(t, err)
			require.NotNil(t, preview)
			assert.Equal(t, "source.example.test", preview.EffectiveRPID)
			assert.False(t, preview.ConfirmationRequired)
			assert.Equal(t, "Updated client", operation_setting.IdentifyConfiguredRequestSource(header))
			assert.JSONEq(t, second, common.OptionMap[key])
			require.NoError(t, db.First(&row, Option{Key: key}).Error)
			assert.JSONEq(t, second, row.Value)
			var afterCount int64
			require.NoError(t, db.Model(&Option{}).Count(&afterCount).Error)
			assert.Equal(t, beforeCount, afterCount, "preview must not materialize Passkey defaults or publish source rules")
			require.NoError(t, UpdateOptionsBulk(mixed))
			assert.Equal(t, "测试客户端", operation_setting.IdentifyConfiguredRequestSource(header))
			assert.JSONEq(t, first, common.OptionMap[key])
			require.NoError(t, db.First(&row, Option{Key: key}).Error)
			assert.JSONEq(t, first, row.Value)
			assert.Equal(t, "source.example.test", system_setting.GetPasskeySettings().RPID)

			largeRules := make([]operation_setting.RequestSourceRule, 70)
			for i := range largeRules {
				largeRules[i] = operation_setting.RequestSourceRule{Enabled: true, Name: fmt.Sprintf("Bulk client %d", i), Conditions: []operation_setting.RequestSourceCondition{
					{Header: "User-Agent", Match: "exact", Value: strings.Repeat("a", 512)},
					{Header: "X-Client-Version", Match: "prefix", Value: strings.Repeat("b", 512)},
				}}
			}
			encoded, err := common.Marshal(largeRules)
			require.NoError(t, err)
			require.Greater(t, len(encoded), 64*1024)
			require.Less(t, len(encoded), 128*1024)
			require.NoError(t, UpdateOption(key, string(encoded)))
			require.NoError(t, db.First(&row, Option{Key: key}).Error)
			assert.Equal(t, string(encoded), row.Value, "large valid rules must be stored without truncation")
			t.Logf("large source rules roundtrip: %d bytes", len(encoded))
			require.NoError(t, operation_setting.LoadRequestSourceRulesJSON(`[]`))
			loadOptionsFromDatabase()
			assert.Equal(t, string(encoded), common.OptionMap[key])
			assert.Equal(t, "Bulk client 0", operation_setting.IdentifyConfiguredRequestSource(http.Header{"User-Agent": {strings.Repeat("a", 512)}, "X-Client-Version": {strings.Repeat("b", 512)}}))

			require.NoError(t, UpdateOption(key, `[]`))
			loadOptionsFromDatabase()
			assert.Empty(t, operation_setting.IdentifyConfiguredRequestSource(header))
			assert.JSONEq(t, `[]`, common.OptionMap[key])
			var unrelated Option
			require.NoError(t, db.First(&unrelated, Option{Key: "request_source_unrelated"}).Error)
			assert.Equal(t, "preserved", unrelated.Value)
		})
	}
}
