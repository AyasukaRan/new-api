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
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pricingUsagePluginSource(version, usageSchema string) string {
	return fmt.Sprintf(`
export const meta = {
  apiVersion: 1, key: "pricing-usage-probe", name: "Pricing Usage Probe", version: %q, author: {name: "Test"},
  models: ["pricing-usage-model"], fetchMode: "per_task", usageSchema: %s
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, version, usageSchema)
}

func TestPricingChannelOverrideModesAndFreePrices(t *testing.T) {
	savedConfig := config.GlobalConfig.ExportAllConfigs()
	savedChannels := ratio_setting.ChannelModelPricing2JSONString()
	savedPrices := ratio_setting.ModelPrice2JSONString()
	savedRatios := ratio_setting.ModelRatio2JSONString()
	savedCompletion := ratio_setting.CompletionRatio2JSONString()
	savedSelfUse := operation_setting.SelfUseModeEnabled
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig))
		require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(savedChannels))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(savedCompletion))
		operation_setting.SelfUseModeEnabled = savedSelfUse
	})
	operation_setting.SelfUseModeEnabled = false
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"catalog-model":0.25}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"catalog-model":2}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"catalog-model":3}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"catalog-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"catalog-model":"tier(\"base\", p * 99)"}`,
	}))
	require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(`{
		"catalog-model":{
			"1":{"billing_mode":"per_token","model_ratio":0,"completion_ratio":0,"cache_ratio":0},
			"2":{"billing_mode":"per_request","model_price":0},
			"3":{"billing_mode":"tiered_expr","billing_expr":"tier(\"base\", p * 7 + c * 8)"},
			"4":{"billing_mode":"per_token","completion_ratio":4},
			"5":{"billing_mode":"ratio"}
		},
		"catalog-unpriced":{"1":{"billing_mode":"per_request"},"2":{"billing_mode":"per_token"}}
	}`))
	for _, tc := range []struct {
		name, model              string
		channel                  int
		mode                     string
		quotaType                int
		ratio, price, completion float64
		expression               string
		configured               bool
	}{
		{"free token price overrides global expression and fixed price", "catalog-model", 1, "per_token", 0, 0, 0, 0, "", true},
		{"free request price", "catalog-model", 2, "per_request", 1, 0, 0, 0, "", true},
		{"channel expression", "catalog-model", 3, "tiered_expr", 0, 0, 0, 0, `tier("base", p * 7 + c * 8)`, true},
		{"per token inherits missing lanes", "catalog-model", 4, "per_token", 0, 2, 0, 4, "", true},
		{"ratio preserves fixed precedence", "catalog-model", 5, "ratio", 1, 0, .25, 0, "", true},
		{"global expression inheritance", "catalog-model", 6, "tiered_expr", 0, 0, 0, 0, `tier("base", p * 99)`, true},
		{"reasoning modifier uses the same channel price as relay", "catalog-model@effort:high", 3, "tiered_expr", 0, 0, 0, 0, `tier("base", p * 7 + c * 8)`, true},
		{"missing fixed price is unconfigured", "catalog-unpriced", 1, "per_request", 1, 0, 0, 0, "", false},
		{"missing ratio is unconfigured", "catalog-unpriced", 2, "per_token", 0, 0, 0, 1, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := resolveChannelPricing(Pricing{ModelName: tc.model}, tc.channel, nil, tc.model, false)
			assert.Equal(t, tc.mode, actual.BillingMode)
			assert.Equal(t, tc.quotaType, actual.QuotaType)
			assert.Equal(t, tc.configured, actual.PricingConfigured)
			assert.Equal(t, tc.ratio, actual.ModelRatio)
			assert.Equal(t, tc.price, actual.ModelPrice)
			assert.Equal(t, tc.completion, actual.CompletionRatio)
			assert.Equal(t, tc.expression, actual.BillingExpr)
		})
	}
	free := resolveChannelPricing(Pricing{ModelName: "catalog-model"}, 1, nil, "catalog-model", false)
	require.NotNil(t, free.CacheRatio)
	assert.Zero(t, *free.CacheRatio)
}

func TestPricingChannelVariantsFilterBeforeMerging(t *testing.T) {
	base := Pricing{ModelName: "catalog-filter", BillingMode: "tiered_expr", BillingExpr: "global-hidden", EnableGroup: []string{"visible", "private", "paid"}}
	base.ChannelPricingVariants = []Pricing{
		{ModelName: base.ModelName, BillingMode: "tiered_expr", BillingExpr: "private-price", PricingConfigured: true, EnableGroup: []string{"private"}},
		{ModelName: base.ModelName, BillingMode: "tiered_expr", BillingExpr: "visible-price", PricingConfigured: true, EnableGroup: []string{"visible", "private"}},
		{ModelName: base.ModelName, BillingMode: "tiered_expr", BillingExpr: "visible-price", PricingConfigured: true, EnableGroup: []string{"paid"}},
	}
	before, err := common.Marshal(base)
	require.NoError(t, err)
	filtered := FilterPricingByUsableGroups([]Pricing{base}, map[string]string{"visible": "Visible", "paid": "Paid"})
	require.Len(t, filtered, 1)
	require.Len(t, filtered[0].ChannelPricingVariants, 1)
	assert.Equal(t, "visible-price", filtered[0].BillingExpr)
	assert.Equal(t, []string{"paid", "visible"}, filtered[0].EnableGroup)
	assert.Equal(t, []string{"paid", "visible"}, filtered[0].ChannelPricingVariants[0].EnableGroup)
	encoded, err := common.Marshal(filtered)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private")
	assert.NotContains(t, string(encoded), "global-hidden")
	after, err := common.Marshal(base)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "per-user filtering must not mutate the shared cache")
	assert.Empty(t, FilterPricingByUsableGroups([]Pricing{base}, nil))
	public := Pricing{ModelName: "public", EnableGroup: []string{"all"}, PricingConfigured: true}
	visible := FilterPricingByUsableGroups([]Pricing{public}, map[string]string{"visible": "Visible"})
	require.Len(t, visible, 1)
	assert.Equal(t, []string{"visible"}, visible[0].EnableGroup)
}

func TestPricingChannelTaskSchemaCompatibilityMatchesRelay(t *testing.T) {
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"catalog-narrowed":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"catalog-narrowed":"u(\"retired\") * 0.2"}`,
	}))
	provider := &jsplugin.LoadedPlugin{Meta: jsplugin.Meta{
		Key: "catalog-narrowed-provider", Models: []string{"catalog-narrowed"},
		UsageSchema: map[string]jsplugin.UsageFieldSchema{"current": {Type: "number", Unit: "count"}},
	}}
	base := Pricing{ModelName: "catalog-narrowed"}
	single := resolveChannelPricing(base, 0, provider, base.ModelName, false)
	assert.True(t, single.PricingConfigured, "single-plugin stored prices stay valid after a schema narrows")
	assert.Equal(t, `u("retired") * 0.2`, single.BillingExpr)
	shared := resolveChannelPricing(base, 0, provider, base.ModelName, true)
	assert.False(t, shared.PricingConfigured, "shared providers must reject incompatible usage facts")
	assert.Empty(t, shared.BillingExpr)
}

func TestPricingChannelCatalogDatabase(t *testing.T) {
	savedConfig := config.GlobalConfig.ExportAllConfigs()
	savedChannels := ratio_setting.ChannelModelPricing2JSONString()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig))
		require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(savedChannels))
		InvalidatePricingCache()
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":          `{"catalog-db":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"catalog-db":"tier(\"global\", p * 3)"}`,
		billing_setting.PluginBillingExprOption: `{"catalog-pricing-task::catalog-image":"u(\"images\") * 0.2","catalog-pricing-task::catalog-video":"u(\"seconds\") * 0.3","catalog-pricing-task::catalog-shared":"u(\"images\") * 0.5","catalog-pricing-other::catalog-shared":"u(\"seconds\") * 0.6"}`,
	}))
	_, err := jsplugin.DefaultRegistry.Register(`
export const meta = {
 apiVersion:1,key:"catalog-pricing-task",name:"Catalog Task",version:"1.0.0",author:{name:"Test"},
 models:["catalog-image","catalog-video","catalog-shared"],fetchMode:"per_task",upstreams:["new_api"],
 usageSchema:{images:{type:"number",unit:"count"}},
 usageProfiles:[
  {models:["catalog-image"],schema:{images:{type:"number",unit:"count"}}},
  {models:["catalog-video"],schema:{seconds:{type:"number",unit:"second"}}}
 ]
};
export function buildSubmitRequest(){return {};}
export function parseSubmitResponse(){return {};}
export function buildQueryRequest(){return {};}
export function parseTaskResult(){return {};}
`, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister("catalog-pricing-task")) })
	_, err = jsplugin.DefaultRegistry.Register(`
export const meta = {apiVersion:1,key:"catalog-pricing-other",name:"Catalog Other",version:"1.0.0",author:{name:"Test"},models:["catalog-shared"],fetchMode:"per_task",upstreams:["new_api"],usageSchema:{seconds:{type:"number",unit:"second"}}};
export function buildSubmitRequest(){return {};}
export function parseSubmitResponse(){return {};}
export function buildQueryRequest(){return {};}
export function parseTaskResult(){return {};}
`, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister("catalog-pricing-other")) })
	require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(`{
		"catalog-db":{
			"7101":{"billing_mode":"tiered_expr","billing_expr":"tier(\"channel\", p * 1)"},
			"7102":{"billing_mode":"tiered_expr","billing_expr":"tier(\"private\", p * 999)"},
			"7103":{"billing_mode":"tiered_expr","billing_expr":"tier(\"disabled\", p * 100)"}
		},
		"catalog-alias":{"7111":{"billing_mode":"tiered_expr","billing_expr":"u(\"images\") * 0.4"}}
	}`))
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
			oldDB, oldPath := DB, common.SQLitePath
			oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			t.Cleanup(func() {
				DB, common.SQLitePath = oldDB, oldPath
				common.SetDatabaseTypes(oldType, oldLogType)
				initCol()
				InvalidatePricingCache()
			})
			common.SQLitePath = filepath.Join(t.TempDir(), "pricing-catalog.db")
			t.Setenv("PRICING_CATALOG_TEST_DSN", dsn)
			db, dbType, err := chooseDB("PRICING_CATALOG_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("SELECT version()").Scan(&version).Error)
			}
			t.Logf("database: %s", version)
			DB = db
			common.SetDatabaseTypes(dbType, oldLogType)
			initCol()
			resetPricingEndpointTestTables(t)
			for _, spec := range []struct {
				id, status int
				group      string
			}{
				{7101, 1, "visible"}, {7102, 1, "private"}, {7103, 2, "visible"}, {7104, 1, "visible"},
			} {
				require.NoError(t, DB.Create(&Channel{Id: spec.id, Name: fmt.Sprintf("secret-channel-%d", spec.id), Key: "secret-key", Status: spec.status, Type: constant.ChannelTypeOpenAI, Models: "catalog-db", Group: spec.group}).Error)
				require.NoError(t, DB.Create(&Ability{ChannelId: spec.id, Model: "catalog-db", Group: spec.group, Enabled: true}).Error)
			}
			for _, spec := range []struct {
				id            int
				group, mapped string
			}{
				{7111, "visible", "catalog-image"}, {7112, "private", "catalog-video"},
			} {
				mapping := fmt.Sprintf(`{"catalog-alias":%q}`, spec.mapped)
				setting := `{"task_plugin_key":"catalog-pricing-task"}`
				require.NoError(t, DB.Create(&Channel{Id: spec.id, Name: "secret-task-channel", Key: "secret-task-key", Status: 1, Type: constant.ChannelTypeTaskPlugin, Models: "catalog-alias", ModelMapping: &mapping, Setting: &setting, Group: spec.group}).Error)
				require.NoError(t, DB.Create(&Ability{ChannelId: spec.id, Model: "catalog-alias", Group: spec.group, Enabled: true}).Error)
			}
			sharedSetting := `{"task_extend_plugin_keys":["catalog-pricing-task","catalog-pricing-other"]}`
			require.NoError(t, DB.Create(&Channel{Id: 7113, Name: "secret-shared-channel", Key: "secret-shared-key", Status: 1, Type: constant.ChannelTypeNewAPI, Models: "catalog-shared", Setting: &sharedSetting, Group: "visible"}).Error)
			require.NoError(t, DB.Create(&Ability{ChannelId: 7113, Model: "catalog-shared", Group: "visible", Enabled: true}).Error)
			for _, memoryCache := range []bool{true, false} {
				t.Run(fmt.Sprintf("memory-cache-%t", memoryCache), func(t *testing.T) {
					common.MemoryCacheEnabled = memoryCache
					InitChannelCache()
					items := FilterPricingByUsableGroups(GetPricing(), map[string]string{"visible": "Visible"})
					require.Len(t, items, 3)
					byModel := pricingByModel(items)
					require.Len(t, byModel["catalog-db"].ChannelPricingVariants, 2)
					assert.Equal(t, `tier("channel", p * 1)`, byModel["catalog-db"].BillingExpr)
					assert.Equal(t, `tier("global", p * 3)`, byModel["catalog-db"].ChannelPricingVariants[1].BillingExpr)
					alias := byModel["catalog-alias"]
					require.Len(t, alias.ChannelPricingVariants, 1)
					assert.True(t, alias.PricingConfigured)
					assert.Equal(t, `u("images") * 0.4`, alias.BillingExpr)
					assert.Contains(t, alias.BillingUsageSchema, "images")
					assert.NotContains(t, alias.BillingUsageSchema, "seconds")
					shared := byModel["catalog-shared"]
					require.Len(t, shared.ChannelPricingVariants, 2)
					assert.ElementsMatch(t, []string{`u("images") * 0.5`, `u("seconds") * 0.6`}, []string{shared.ChannelPricingVariants[0].BillingExpr, shared.ChannelPricingVariants[1].BillingExpr})
					encoded, err := common.Marshal(items)
					require.NoError(t, err)
					for _, private := range []string{"private", "disabled", "secret-channel", "secret-key", "channel_id"} {
						assert.NotContains(t, string(encoded), private)
					}
				})
			}
		})
	}
}

func TestPricingCarriesTaskUsageSchemaAndRefreshesWithPluginGeneration(t *testing.T) {
	resetPricingEndpointTestTables(t)
	const pluginKey = "pricing-usage-probe"
	initialSource := pricingUsagePluginSource("1.0.0", `{
  seconds: {type: "number", unit: "second", description: "Estimated duration."},
  action: {enum:["video"],enumLabels:{video:{en:"Generate video",zh:"生成视频"}}}
}`)
	_, err := jsplugin.DefaultRegistry.Register(initialSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	insertPricingEndpointChannel(t, 901, constant.ChannelTypeTaskPlugin, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 901, "pricing-usage-model")
	insertPricingEndpointAbility(t, 901, "ordinary-model")

	initialPricing := pricingByModel(GetPricing())
	require.Contains(t, initialPricing, "pricing-usage-model")
	require.Contains(t, initialPricing, "ordinary-model")
	assert.Equal(t, "second", initialPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Unit)
	assert.Equal(t, "Estimated duration.", initialPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Description["en"])
	assert.Equal(t, "生成视频", initialPricing["pricing-usage-model"].BillingUsageSchema["action"].EnumLabels["video"]["zh"])
	assert.Nil(t, initialPricing["ordinary-model"].BillingUsageSchema)

	updatedSource := pricingUsagePluginSource("1.1.0", `{
  seconds: {type: "number", unit: "second", description: "Measured duration."},
  clips: {type: "number", unit: "count", description: "Generated clip count."}
}`)
	_, err = jsplugin.DefaultRegistry.Register(updatedSource, jsplugin.Options{})
	require.NoError(t, err)
	lastGetPricingTime = time.Now().Add(-2 * time.Minute)

	refreshedPricing := pricingByModel(GetPricing())
	require.Len(t, refreshedPricing["pricing-usage-model"].BillingUsageSchema, 2)
	assert.Equal(t, "Measured duration.", refreshedPricing["pricing-usage-model"].BillingUsageSchema["seconds"].Description["en"])
	assert.Equal(t, "count", refreshedPricing["pricing-usage-model"].BillingUsageSchema["clips"].Unit)
}

func TestPricingAliasCarriesPluginUsageSchemaAndTailExpr(t *testing.T) {
	resetPricingEndpointTestTables(t)
	const pluginKey = "pricing-usage-probe"
	source := pricingUsagePluginSource("1.0.0", `{
  seconds: {type: "number", unit: "second", description: "Estimated duration."}
}`)
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	mapping := `{"alias-model":"pricing-usage-model"}`
	channel := &Channel{
		Id:           910,
		Type:         constant.ChannelTypeTaskPlugin,
		Key:          "key-910",
		Status:       1,
		Name:         "channel-910",
		Models:       "alias-model,pricing-usage-model",
		ModelMapping: &mapping,
	}
	require.NoError(t, DB.Create(channel).Error)
	insertPricingEndpointAbility(t, 910, "alias-model")
	insertPricingEndpointAbility(t, 910, "pricing-usage-model")
	InitChannelCache()

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"pricing-usage-model":"tiered_expr","alias-own-expr":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"pricing-usage-model":"u(\"seconds\")","alias-own-expr":"u(\"seconds\") * 2"}`,
	}))
	InvalidatePricingCache()

	pricing := pricingByModel(GetPricing())
	require.Contains(t, pricing, "alias-model")
	require.Contains(t, pricing, "pricing-usage-model")
	assert.Equal(t, "second", pricing["alias-model"].BillingUsageSchema["seconds"].Unit)
	assert.Equal(t, "Estimated duration.", pricing["alias-model"].BillingUsageSchema["seconds"].Description["en"])
	assert.Equal(t, "tiered_expr", pricing["alias-model"].BillingMode)
	assert.Equal(t, `u("seconds")`, pricing["alias-model"].BillingExpr)
	assert.Equal(t, "tiered_expr", pricing["pricing-usage-model"].BillingMode)
	assert.Equal(t, `u("seconds")`, pricing["pricing-usage-model"].BillingExpr)

	ownMapping := `{"alias-own-expr":"pricing-usage-model"}`
	own := &Channel{
		Id:           911,
		Type:         constant.ChannelTypeTaskPlugin,
		Key:          "key-911",
		Status:       1,
		Name:         "channel-911",
		Models:       "alias-own-expr,pricing-usage-model",
		ModelMapping: &ownMapping,
	}
	require.NoError(t, DB.Create(own).Error)
	insertPricingEndpointAbility(t, 911, "alias-own-expr")
	InitChannelCache()
	InvalidatePricingCache()

	refreshed := pricingByModel(GetPricing())
	assert.Equal(t, `u("seconds") * 2`, refreshed["alias-own-expr"].BillingExpr)
	assert.Equal(t, "second", refreshed["alias-own-expr"].BillingUsageSchema["seconds"].Unit)
}

func pricingByModel(pricings []Pricing) map[string]Pricing {
	result := make(map[string]Pricing, len(pricings))
	for _, pricing := range pricings {
		result[pricing.ModelName] = pricing
	}
	return result
}

func TestPricingUsageProfilesAndAliases(t *testing.T) {
	resetPricingEndpointTestTables(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	const key = "pricing-profiles"
	const source = `
export const meta = {
 apiVersion:1,key:"pricing-profiles",name:"Pricing Profiles",version:"1.0.0",author:{name:"Test"},
 models:["profile-image","profile-video"],fetchMode:"per_task",
 usageSchema:{fallback:{type:"number",unit:"count"}},
 usageExamples:[{label:"default",facts:{fallback:1}}],
 usageProfiles:[
  {models:["profile-image"],schema:{image_count:{type:"number",unit:"count"}}},
  {models:["profile-video"],schema:{seconds:{type:"number",unit:"second"}},examples:[{label:"5 seconds",facts:{seconds:5}}]}
 ]
};
export function buildSubmitRequest(){return {};}
export function parseSubmitResponse(){return {};}
export function buildQueryRequest(){return {};}
export function parseTaskResult(){return {};}
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })
	for index, mapping := range []string{
		`{"image-alias":"profile-image","video-alias":"profile-video","ambiguous-alias":"profile-image"}`,
		`{"ambiguous-alias":"profile-video"}`,
	} {
		id := 920 + index
		require.NoError(t, DB.Create(&Channel{
			Id: id, Type: constant.ChannelTypeTaskPlugin, Status: 1, Name: fmt.Sprintf("channel-%d", id),
			Models: "profile-image,profile-video,image-alias,video-alias,ambiguous-alias", ModelMapping: &mapping,
		}).Error)
		for _, name := range []string{"profile-image", "profile-video", "image-alias", "video-alias", "ambiguous-alias"} {
			insertPricingEndpointAbility(t, id, name)
		}
	}
	InitChannelCache()
	pricing := pricingByModel(GetPricing())
	names := []string{"profile-image", "profile-video", "image-alias", "video-alias", "ambiguous-alias"}
	snapshot, err := GetModelPricingSnapshot(names)
	require.NoError(t, err)
	entries := make(map[string]ModelPricingEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.ModelName] = entry
	}
	for _, tc := range []struct {
		name, field, unit string
		wantExamples      int
	}{
		{"profile-image", "image_count", "count", 0},
		{"profile-video", "seconds", "second", 1},
		{"image-alias", "image_count", "count", 0},
		{"video-alias", "seconds", "second", 1},
		{"ambiguous-alias", "fallback", "count", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := map[string]jsplugin.UsageFieldSchema{tc.field: {Type: "number", Unit: tc.unit}}
			assert.Equal(t, expected, pricing[tc.name].BillingUsageSchema)
			assert.Equal(t, expected, entries[tc.name].UsageSchema)
			assert.Len(t, pricing[tc.name].BillingUsageExamples, tc.wantExamples)
			require.NoError(t, ValidateModelPricing(tc.name, PricingValues{"billing_setting.billing_expr": `u("` + tc.field + `")`}))
			require.ErrorContains(t, ValidateModelPricing(tc.name, PricingValues{"billing_setting.billing_expr": `u("missing")`}), "not declared")
		})
	}
	require.ErrorContains(t, ValidateModelPricing("profile-image", PricingValues{"billing_setting.billing_expr": `u("seconds")`}), "not declared")
	require.ErrorContains(t, ValidateModelPricing("video-alias", PricingValues{"billing_setting.billing_expr": `u("image_count")`}), "not declared")

	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_expr":          `{"profile-image":"u(\"image_count\")"}`,
		billing_setting.PluginBillingExprOption: `{"pricing-profiles::profile-image":"u(\"image_count\") * 2"}`,
	}))
	updated := strings.Replace(source, `version:"1.0.0"`, `version:"1.1.0"`, 1)
	updated = strings.Replace(updated, `image_count:{type:"number",unit:"count"}`, `images:{type:"number",unit:"count"}`, 1)
	_, err = jsplugin.DefaultRegistry.Register(updated, jsplugin.Options{})
	require.NoError(t, err)
	// Stored prices that read a fact the narrowed profile dropped stay saveable
	// while unchanged; edited ones must follow the new profile.
	stored := PricingValues{"billing_setting.billing_expr": `u("image_count")`, billing_setting.PluginBillingExprOption: map[string]any{key: `u("image_count") * 2`}}
	require.NoError(t, ValidateModelPricing("profile-image", stored))
	require.ErrorContains(t, ValidateModelPricing("profile-image", PricingValues{"billing_setting.billing_expr": `u("image_count") * 3`}), "not declared")
	stored[billing_setting.PluginBillingExprOption] = map[string]any{key: `u("image_count") * 3`}
	require.ErrorContains(t, ValidateModelPricing("profile-image", stored), "not declared")
	InvalidatePricingCache()
	refreshed := pricingByModel(GetPricing())
	assert.Equal(t, map[string]jsplugin.UsageFieldSchema{"images": {Type: "number", Unit: "count"}}, refreshed["image-alias"].BillingUsageSchema)
	assert.Equal(t, "second", refreshed["profile-video"].BillingUsageSchema["seconds"].Unit)
}

func TestPricingSharedPluginVariantsUseEachSchemaAndExpression(t *testing.T) {
	resetPricingEndpointTestTables(t)
	saved := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	for _, spec := range []struct{ key, field, unit string }{{"pricing-alpha", "seconds", "second"}, {"pricing-beta", "credits", "credit"}} {
		source := strings.ReplaceAll(pricingUsagePluginSource("1.0.0", `{`+spec.field+`:{type:"number",unit:"`+spec.unit+`"}}`), "pricing-usage-probe", spec.key)
		_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(spec.key) })
	}
	insertPricingEndpointChannel(t, 930, constant.ChannelTypeTaskPlugin, dto.ChannelOtherSettings{})
	insertPricingEndpointAbility(t, 930, "pricing-usage-model")
	const expression = `tier("base", u("seconds") * 0.4)`
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":          `{"pricing-usage-model":"tiered_expr"}`,
		"billing_setting.billing_expr":          `{"pricing-usage-model":"tier(\"base\", u(\"seconds\") * 0.4)"}`,
		billing_setting.PluginBillingExprOption: `{}`,
	}))
	prices := pricingByModel(GetPricing())
	variants := prices["pricing-usage-model"].BillingPluginVariants
	require.Len(t, variants, 2)
	assert.Equal(t, "pricing-alpha", variants[0].PluginKey)
	assert.Equal(t, "pricing-beta", variants[1].PluginKey)
	assert.Equal(t, expression, variants[0].BillingExpr)
	assert.Empty(t, variants[1].BillingExpr)
	assert.Equal(t, "credit", variants[1].BillingUsageSchema["credits"].Unit)
	assert.Equal(t, expression, prices["pricing-usage-model"].BillingExpr)
	assert.Contains(t, prices["pricing-usage-model"].BillingUsageSchema, "seconds")
	draft := PricingValues{"billing_setting.billing_expr": expression}
	require.ErrorContains(t, ValidateModelPricing("pricing-usage-model", draft), "plugin pricing-beta")
	draft[billing_setting.PluginBillingExprOption] = map[string]any{"pricing-beta": `tier("beta", u("credits") * 2)`}
	require.NoError(t, ValidateModelPricing("pricing-usage-model", draft))
	for _, invalid := range []any{map[string]any{"missing": "1"}, map[string]any{"pricing-beta": float64(1)}, map[string]any{"pricing-beta": `u("seconds")`}, map[string]any(nil), []any{}, "invalid"} {
		draft[billing_setting.PluginBillingExprOption] = invalid
		assert.Error(t, ValidateModelPricing("pricing-usage-model", draft))
	}
	draft = PricingValues{
		"billing_setting.billing_expr":          "invalid expression(",
		billing_setting.PluginBillingExprOption: map[string]any{"pricing-alpha": expression, "pricing-beta": `u("credits")`},
	}
	require.ErrorContains(t, ValidateModelPricing("pricing-usage-model", draft), "compile")
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		billing_setting.PluginBillingExprOption: `{"pricing-beta::pricing-usage-model":"tier(\"beta\", u(\"credits\") * 2)"}`,
	}))
	InvalidatePricingCache()
	priced := pricingByModel(GetPricing())["pricing-usage-model"].BillingPluginVariants
	require.Len(t, priced, 2)
	assert.Equal(t, `tier("beta", u("credits") * 2)`, priced[1].BillingExpr)
	previousPrice := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrice)) })
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"pricing-usage-model":0.25}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"pricing-usage-model":"ratio"}`}))
	InvalidatePricingCache()
	mixed := pricingByModel(GetPricing())["pricing-usage-model"].BillingPluginVariants
	require.Len(t, mixed, 2)
	assert.Equal(t, billing_setting.BillingModeRatio, mixed[0].BillingMode)
	assert.Equal(t, billing_setting.BillingModeTieredExpr, mixed[1].BillingMode)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{billing_setting.PluginBillingExprOption: `{}`}))
	InvalidatePricingCache()
	perCall := pricingByModel(GetPricing())["pricing-usage-model"]
	assert.Empty(t, perCall.BillingPluginVariants)
	assert.Equal(t, 1, perCall.QuotaType)
	assert.Equal(t, 0.25, perCall.ModelPrice)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		billing_setting.PluginBillingExprOption: `{"pricing-beta::pricing-usage-model":"tier(\"beta\", u(\"credits\") * 2)"}`,
	}))
	require.NoError(t, jsplugin.DefaultRegistry.Unregister("pricing-alpha"))
	InvalidatePricingCache()
	single := pricingByModel(GetPricing())["pricing-usage-model"]
	require.Len(t, single.BillingPluginVariants, 1)
	assert.Equal(t, "pricing-beta", single.BillingPluginVariants[0].PluginKey)
	assert.Equal(t, `tier("beta", u("credits") * 2)`, single.BillingPluginVariants[0].BillingExpr)
	encoded, err := common.Marshal(single)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "billing_plugin_variants")
}
