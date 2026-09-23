package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

func TestTaskPluginEnabledOptionUpdatesRegistry(t *testing.T) {
	originalEnabled := constant.TaskPluginEnabled
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	const key = "option-master-off"
	source := `
export const meta = {apiVersion: 1, key: "option-master-off", name: "Option Master", version: "1.0.0", author: {name: "Test"}, models: ["option-master-model"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		constant.TaskPluginEnabled = originalEnabled
		jsplugin.DefaultRegistry.SetEnabled(originalEnabled)
		common.OptionMap = originalMap
	})

	_, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)

	require.NoError(t, updateOptionMap("TaskPluginEnabled", "false"))

	assert.False(t, constant.TaskPluginEnabled)
	assert.Equal(t, "false", common.OptionMap["TaskPluginEnabled"])
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)

	require.NoError(t, updateOptionMap("TaskPluginEnabled", "true"))
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.True(t, ok)
}

func TestChannelModelPricingOptionDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			if dialect != "sqlite" {
				env := "TEST_" + strings.ToUpper(dialect) + "_DSN"
				dsn = strings.TrimSpace(os.Getenv(env))
				if dsn == "" {
					t.Skip(env + " is not configured")
				}
			}
			oldPath := common.SQLitePath
			common.SQLitePath = filepath.Join(t.TempDir(), "channel-pricing.db")
			t.Cleanup(func() { common.SQLitePath = oldPath })
			t.Setenv("CHANNEL_PRICING_TEST_DSN", dsn)
			db, dbType, err := chooseDB("CHANNEL_PRICING_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			db.NamingStrategy = schema.NamingStrategy{TablePrefix: fmt.Sprintf("channel_pricing_%d_", time.Now().UnixNano())}
			require.NoError(t, db.AutoMigrate(&Option{}, &Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&Option{}, &Channel{})) })
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
			}
			t.Logf("database: %s", version)

			oldDB, oldOptions := DB, common.OptionMap
			oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			oldPricing := ratio_setting.ChannelModelPricing2JSONString()
			oldAliasView := taskAliasViewPtr.Load()
			oldPluginEnabled := constant.TaskPluginEnabled
			oldDisabledFactory := jsplugin.DefaultRegistry.Snapshot().DisabledFactory
			DB, common.OptionMap = db, map[string]string{}
			common.SetDatabaseTypes(dbType, oldLogType)
			initCol()
			t.Cleanup(func() {
				DB, common.OptionMap = oldDB, oldOptions
				common.SetDatabaseTypes(oldMainType, oldLogType)
				initCol()
				taskAliasViewPtr.Store(oldAliasView)
				constant.TaskPluginEnabled = oldPluginEnabled
				jsplugin.DefaultRegistry.SetEnabled(oldPluginEnabled)
				jsplugin.DefaultRegistry.SetDisabledFactoryKeys(oldDisabledFactory)
				require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(oldPricing))
			})
			constant.TaskPluginEnabled = true
			jsplugin.DefaultRegistry.SetEnabled(true)
			_, err = jsplugin.DefaultRegistry.Register(pricingUsagePluginSource("1.0.0", `{seconds:{type:"number",unit:"second"}}`), jsplugin.Options{})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister("pricing-usage-probe")) })
			mapping := `{"pricing-alias":"pricing-usage-model"}`
			require.NoError(t, DB.Create(&Channel{
				Id: 901, Type: constant.ChannelTypeTaskPlugin, Key: "test-only", Status: common.ChannelStatusEnabled,
				Name: "pricing-alias", Models: "pricing-alias,pricing-usage-model", ModelMapping: &mapping,
			}).Error)
			rebuildTaskAliasView()

			const preserved = `{"preserved-model":{"903":{"billing_mode":"per_request","model_price":0.7}}}`
			require.NoError(t, UpdateOption("ChannelModelPricing", preserved))
			require.NoError(t, UpdateOption("channel_pricing_unrelated_option", "preserved"))
			var values map[string]map[int]ratio_setting.ChannelModelPricing
			require.NoError(t, common.UnmarshalJsonStr(preserved, &values))
			const additions = `{
				"all-lanes":{"901":{"billing_mode":"per_token","model_price":1,"model_ratio":2,"completion_ratio":3,"cache_ratio":0.2,"create_cache_ratio":1.25,"image_ratio":4,"audio_ratio":5,"audio_completion_ratio":6}},
				"free-model":{"901":{"billing_mode":"per_request","model_price":0,"model_ratio":0,"completion_ratio":0,"cache_ratio":0,"create_cache_ratio":0,"image_ratio":0,"audio_ratio":0,"audio_completion_ratio":0}},
				"inherit-model":{"901":{"billing_mode":"per_request"}},
				"pricing-alias":{"901":{"billing_mode":"tiered_expr","billing_expr":"u(\"seconds\") * 0.2"}}
			}`
			var added map[string]map[int]ratio_setting.ChannelModelPricing
			require.NoError(t, common.UnmarshalJsonStr(additions, &added))
			for name, channels := range added {
				values[name] = channels
			}
			encoded, err := common.Marshal(values)
			require.NoError(t, err)
			require.NoError(t, UpdateOption("ChannelModelPricing", string(encoded)))
			invalid := strings.Replace(string(encoded), `u(\"seconds\")`, `u(\"unknown\")`, 1)
			require.NotEqual(t, string(encoded), invalid)
			require.Error(t, UpdateOption("ChannelModelPricing", invalid), "task aliases must validate against the resolved plugin schema")
			for range 2 {
				require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(`{}`))
				InitOptionMap()
				assert.Equal(t, values, ratio_setting.GetChannelModelPricingCopy())
				assert.Equal(t, string(encoded), common.OptionMap["ChannelModelPricing"])
				assert.Equal(t, "preserved", common.OptionMap["channel_pricing_unrelated_option"])
				var row Option
				require.NoError(t, DB.Where(&Option{Key: "ChannelModelPricing"}).First(&row).Error)
				assert.Equal(t, string(encoded), row.Value, "initialization and rejected writes must preserve stored JSON")
			}
		})
	}
}

func TestEffectiveModelPricingUsesSnapshotWithoutChangingConfiguration(t *testing.T) {
	const modelName = "effective-channel-lanes"
	lanes := []struct {
		key     string
		value   float64
		current string
		update  func(string) error
	}{
		{"CacheRatio", 0.25, ratio_setting.CacheRatio2JSONString(), ratio_setting.UpdateCacheRatioByJSONString},
		{"CreateCacheRatio", 1.25, ratio_setting.CreateCacheRatio2JSONString(), ratio_setting.UpdateCreateCacheRatioByJSONString},
		{"ImageRatio", 2, ratio_setting.ImageRatio2JSONString(), ratio_setting.UpdateImageRatioByJSONString},
		{"AudioRatio", 3, ratio_setting.AudioRatio2JSONString(), ratio_setting.UpdateAudioRatioByJSONString},
		{"AudioCompletionRatio", 4, ratio_setting.AudioCompletionRatio2JSONString(), ratio_setting.UpdateAudioCompletionRatioByJSONString},
	}
	for _, lane := range lanes {
		t.Cleanup(func() { require.NoError(t, lane.update(lane.current)) })
		encoded, err := common.Marshal(map[string]float64{modelName: lane.value})
		require.NoError(t, err)
		require.NoError(t, lane.update(string(encoded)))
	}
	for _, mode := range []string{"token", "defaults", "zero", "expression", "fixed"} {
		t.Run(mode, func(t *testing.T) {
			values := map[string]map[string]any{"ModelRatio": {modelName: float64(1)}}
			switch mode {
			case "token":
				// readModelPricingMaps supplies the complete option snapshot.
				// Configured lanes must come from that snapshot, not a process cache.
				for _, lane := range lanes {
					values[lane.key] = map[string]any{modelName: lane.value}
				}
			case "zero":
				for _, lane := range lanes {
					values[lane.key] = map[string]any{modelName: float64(0)}
				}
			case "expression":
				values["billing_setting.billing_mode"] = map[string]any{modelName: "tiered_expr"}
				values["billing_setting.billing_expr"] = map[string]any{modelName: "p * 2"}
			case "fixed":
				values["ModelPrice"] = map[string]any{modelName: float64(0)}
			}
			before, err := common.Marshal(values)
			require.NoError(t, err)
			effective := effectiveModelPricing(values, modelName)
			for _, lane := range lanes {
				switch mode {
				case "expression", "fixed":
					assert.NotContains(t, effective, lane.key)
				case "zero":
					assert.Equal(t, float64(0), effective[lane.key], lane.key)
				case "defaults":
					switch lane.key {
					case "CacheRatio", "ImageRatio":
						assert.Equal(t, float64(1), effective[lane.key], "omitted snapshot prices must not read stale process settings")
					case "CreateCacheRatio":
						assert.Equal(t, ratio_setting.DefaultCreateCacheRatio, effective[lane.key])
					default:
						assert.NotContains(t, effective, lane.key, "an omitted audio lane must remain unconfigured")
					}
				default:
					assert.Equal(t, lane.value, effective[lane.key], lane.key)
				}
			}
			after, err := common.Marshal(values)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "effective defaults must not become explicit saved prices")
		})
	}
}

func TestTaskPluginDisabledFactoryKeysOptionUpdatesRegistry(t *testing.T) {
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	const key = "option-factory-off"
	source := `
export const meta = {apiVersion: 1, key: "option-factory-off", name: "Option Factory", version: "1.0.0", author: {name: "Test"}, models: ["option-factory-model"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		jsplugin.DefaultRegistry.SetDisabledFactoryKeys(nil)
		common.OptionMap = originalMap
	})

	_, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)

	require.NoError(t, updateOptionMap(setting.TaskPluginDisabledFactoryKeysKey, `["option-factory-off"]`))

	assert.Equal(t, `["option-factory-off"]`, common.OptionMap[setting.TaskPluginDisabledFactoryKeysKey])
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)
	assert.Equal(t, []string{key}, jsplugin.DefaultRegistry.Snapshot().DisabledFactory)
}
