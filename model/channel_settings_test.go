package model

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	filterdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestChannelBalanceRechargeMultiplier(t *testing.T) {
	const invalidMultiplier = "Enter a finite recharge multiplier between 0.000001 and 1000000."
	for _, tc := range []struct {
		name, setting string
		valid         bool
		want          *float64
	}{
		{"legacy default", `{}`, true, nil},
		{"null default", `{"balance_recharge_multiplier":null}`, true, nil},
		{"one", `{"balance_recharge_multiplier":1}`, true, common.GetPointer(1.0)},
		{"fraction", `{"balance_recharge_multiplier":0.3}`, true, common.GetPointer(0.3)},
		{"lower boundary", `{"balance_recharge_multiplier":0.000001}`, true, common.GetPointer(0.000001)},
		{"upper boundary", `{"balance_recharge_multiplier":1000000}`, true, common.GetPointer(1000000.0)},
		{"zero", `{"balance_recharge_multiplier":0}`, false, nil},
		{"negative", `{"balance_recharge_multiplier":-1}`, false, nil},
		{"below range", `{"balance_recharge_multiplier":0.0000001}`, false, nil},
		{"above range", `{"balance_recharge_multiplier":1000001}`, false, nil},
		{"overflow", `{"balance_recharge_multiplier":1e999}`, false, nil},
		{"string", `{"balance_recharge_multiplier":"2"}`, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &Channel{Type: constant.ChannelTypeOpenAI, Setting: &tc.setting}
			err := channel.ValidateSettings()
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.want, channel.GetSetting().BalanceRechargeMultiplier)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, tc.setting, *channel.Setting, "validation must not rewrite stored JSON")
		})
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			setting := dto.ChannelSettings{BalanceRechargeMultiplier: &value}
			require.EqualError(t, setting.ValidateBalanceQuery(), invalidMultiplier)
		})
	}
	t.Run("display setting preserves balances and probe identity", func(t *testing.T) {
		channel := &Channel{Id: 123, Type: constant.ChannelTypeOpenAI, Key: "fixture-a\nfixture-b\nfixture-c", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModeFailover}}
		originalHash, err := channelBalanceConfigurationHash(channel)
		require.NoError(t, err)
		observation, err := ChannelKeyObservationID(channel, "fixture-model", "fixture-a")
		require.NoError(t, err)
		legacyObservation, err := LegacyChannelKeyObservationID(channel, "fixture-model", "fixture-a")
		require.NoError(t, err)
		channel.SetSetting(dto.ChannelSettings{BalanceRechargeMultiplier: common.GetPointer(10.0)})
		require.NoError(t, channel.ValidateSettings())
		actualHash, err := channelBalanceConfigurationHash(channel)
		require.NoError(t, err)
		assert.Equal(t, originalHash, actualHash, "existing raw balance snapshots remain usable")
		actualObservation, err := ChannelKeyObservationID(channel, "fixture-model", "fixture-a")
		require.NoError(t, err)
		assert.Equal(t, observation, actualObservation, "display edits must not invalidate key health")
		actualLegacyObservation, err := LegacyChannelKeyObservationID(channel, "fixture-model", "fixture-a")
		require.NoError(t, err)
		assert.Equal(t, legacyObservation, actualLegacyObservation)
		keys := []ChannelKeyBalance{{Index: 0, Balance: common.GetPointer(100.0)}, {Index: 1, Balance: common.GetPointer(100.0)}, {Index: 2, Balance: common.GetPointer(200.0)}}
		for _, tc := range []struct {
			mode constant.MultiKeyMode
			want float64
		}{{constant.MultiKeyModeFailover, 300}, {constant.MultiKeyModePolling, 400}, {constant.MultiKeyModeRandom, 400}} {
			channel.ChannelInfo.MultiKeyMode = tc.mode
			balance, err := channel.AggregateKeyBalances(keys)
			require.NoError(t, err)
			assert.Equal(t, tc.want, balance, "stored balances retain original aggregation units")
		}
	})
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "balance-recharge.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			table := db.Table("balance_recharge_channels")
			require.NoError(t, table.AutoMigrate(&Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("balance_recharge_channels")) })
			var version string
			versionQuery := "select version()"
			if dialect == "sqlite" {
				versionQuery = "select sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			const original = `{"system_prompt":"preserved","balance_email_enabled":true,"balance_email_threshold":100,"future":{"number":9007199254740993}}`
			channel := &Channel{Type: constant.ChannelTypeOpenAI, Name: "recharge fixture", Key: "fixture", Setting: common.GetPointer(original), Balance: 400, UsedQuota: 1234}
			require.NoError(t, channel.ValidateSettings())
			require.NoError(t, table.Create(channel).Error)
			const updated = `{"system_prompt":"preserved","balance_email_enabled":true,"balance_email_threshold":100,"future":{"number":9007199254740993},"balance_recharge_multiplier":10}`
			channel.Setting = common.GetPointer(updated)
			require.NoError(t, channel.ValidateSettings())
			require.NoError(t, table.Where("id = ?", channel.Id).Update("setting", channel.Setting).Error)
			var loaded Channel
			require.NoError(t, table.First(&loaded, channel.Id).Error)
			assert.Equal(t, updated, *loaded.Setting, "save preserves unrelated and future JSON fields")
			assert.Equal(t, common.GetPointer(10.0), loaded.GetSetting().BalanceRechargeMultiplier)
			assert.Equal(t, common.GetPointer(100.0), loaded.GetSetting().BalanceEmailThreshold)
			assert.Equal(t, float64(400), loaded.Balance)
			assert.Equal(t, int64(1234), loaded.UsedQuota)
		})
	}
}

func TestChannelValidateSettingsRejectsInvalidHTTPTransport(t *testing.T) {
	tests := []struct {
		name    string
		setting dto.ChannelSettings
		wantErr string
	}{
		{
			name:    "auto with shards is valid",
			setting: dto.ChannelSettings{HTTPProtocol: "auto", HTTP2ConnectionShards: 4},
		},
		{
			name:    "http1 with shards greater than one rejected",
			setting: dto.ChannelSettings{HTTPProtocol: "http1", HTTP2ConnectionShards: 2},
			wantErr: "http2_connection_shards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{}
			channel.SetSetting(tt.setting)
			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestChannelEndpointSettings(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		valid         bool
	}{
		{"legacy single address", `{}`, true},
		{"shared mirror pool", `{"endpoint_mode":"failover","additional_base_urls":["https://backup.example/v1"]}`, true},
		{"unknown mode", `{"endpoint_mode":"other","additional_base_urls":["https://backup.example/v1"]}`, false},
		{"pool requires mirrors", `{"endpoint_mode":"failover"}`, false},
		{"mirror requires a mode", `{"additional_base_urls":["https://backup.example/v1"]}`, false},
		{"duplicate primary", `{"endpoint_mode":"load_balance","additional_base_urls":["https://primary.example/v1/"]}`, false},
		{"credential in URL", `{"endpoint_mode":"failover","additional_base_urls":["https://user:secret@backup.example/v1"]}`, false},
		{"query in URL", `{"endpoint_mode":"failover","additional_base_urls":["https://backup.example/v1?api_key=secret"]}`, false},
		{"fragment in URL", `{"endpoint_mode":"failover","additional_base_urls":["https://backup.example/v1#fragment"]}`, false},
		{"empty mirror", `{"endpoint_mode":"failover","additional_base_urls":["  "]}`, false},
		{"non HTTP URL", `{"endpoint_mode":"failover","additional_base_urls":["file:///etc/passwd"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseURL := "https://primary.example/v1"
			channel := &Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL, Setting: &tc.setting}
			err := channel.ValidateSettings()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	t.Run("bounded address pool", func(t *testing.T) {
		settings := dto.ChannelSettings{EndpointMode: "load_balance"}
		for index := range 15 {
			settings.AdditionalBaseURLs = append(settings.AdditionalBaseURLs, fmt.Sprintf("https://mirror%d.example/v1", index))
		}
		urls, err := settings.EndpointURLs("https://primary.example/v1")
		require.NoError(t, err)
		assert.Len(t, urls, 16)
		settings.AdditionalBaseURLs = append(settings.AdditionalBaseURLs, "https://overflow.example/v1")
		_, err = settings.EndpointURLs("https://primary.example/v1")
		require.Error(t, err)
	})
}

func TestAdvancedCustomChannelRequiresModelListRouteOnlyWhenUpdateChecksEnabled(t *testing.T) {
	inferenceRoute := dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
		Converter:    "none",
	}

	tests := []struct {
		name          string
		checksEnabled bool
		routes        []dto.AdvancedCustomRoute
		wantErr       string
	}{
		{
			name:   "legacy channel without discovery route remains valid",
			routes: []dto.AdvancedCustomRoute{inferenceRoute},
		},
		{
			name:          "enabled checks require discovery route",
			checksEnabled: true,
			routes:        []dto.AdvancedCustomRoute{inferenceRoute},
			wantErr:       dto.AdvancedCustomModelListPath,
		},
		{
			name:          "enabled checks accept discovery route",
			checksEnabled: true,
			routes: []dto.AdvancedCustomRoute{
				inferenceRoute,
				{
					IncomingPath: dto.AdvancedCustomModelListPath,
					UpstreamPath: dto.AdvancedCustomModelListPath,
					Converter:    "none",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{Type: constant.ChannelTypeAdvancedCustom}
			channel.SetOtherSettings(dto.ChannelOtherSettings{
				UpstreamModelUpdateCheckEnabled: tt.checksEnabled,
				AdvancedCustom: &dto.AdvancedCustomConfig{
					Routes: tt.routes,
				},
			})

			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInferencePresetSettingsAndDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "presets.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			table := db.Table("inference_preset_channels").Session(&gorm.Session{})
			require.NoError(t, table.AutoMigrate(&Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("inference_preset_channels")) })
			var version string
			if dialect == "sqlite" {
				require.NoError(t, db.Raw("select sqlite_version()").Scan(&version).Error)
			} else {
				require.NoError(t, db.Raw("select version()").Scan(&version).Error)
			}
			t.Logf("%s version: %s", dialect, version)
			for _, channelType := range []int{constant.ChannelTypeVLLM, constant.ChannelTypeSGLang} {
				t.Run(fmt.Sprint(channelType), func(t *testing.T) {
					channel := &Channel{Type: channelType, Key: "EMPTY", Name: "inference", Status: common.ChannelStatusEnabled}
					require.NoError(t, channel.ValidateSettings())
					require.NotNil(t, channel.GetOtherSettings().AdvancedCustom)
					require.NoError(t, table.Create(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						assert.Equal(t, channelType, loaded.Type)
						assert.Empty(t, loaded.OtherSettings)
						defaults := loaded.GetOtherSettings().AdvancedCustom
						require.NotNil(t, defaults)
						assert.True(t, defaults.SupportsPath("/v1/messages"))
						assert.Empty(t, loaded.OtherSettings, "reading defaults must not rewrite saved settings")
						defaults.Routes[0].UpstreamPath = "/changed-locally"
						assert.Equal(t, "/v1/chat/completions", loaded.GetOtherSettings().AdvancedCustom.Routes[0].UpstreamPath)
					}
					settings := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/custom/chat", Models: []string{"allowed"}, Auth: &dto.AdvancedCustomRouteAuth{Type: "none"}}}}}
					channel.SetOtherSettings(settings)
					require.NoError(t, channel.ValidateSettings())
					require.NoError(t, table.Save(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						actual := loaded.GetOtherSettings().AdvancedCustom
						require.Equal(t, common.GetAdvancedCustomPreset(channelType), actual, "named channels must ignore editable advanced_custom overrides")
						assert.Equal(t, channel.OtherSettings, loaded.OtherSettings)
						for _, tc := range []struct {
							path, model string
							allowed     bool
						}{
							{"/v1/chat/completions", "allowed", true},
							{"/v1/chat/completions", "other", true},
							{"/v1/messages", "allowed", true},
							{"/v1/images/generations", "allowed", false},
						} {
							ok, _ := ChannelSatisfiesFilters(&loaded, tc.model, []filterdto.ChannelFilter{{Kind: filterdto.FilterRequestPath, RequestPath: tc.path}})
							assert.Equal(t, tc.allowed, ok)
						}
						endpoints := getPricingEndpointTypesForAbility(AbilityWithChannel{ChannelType: channelType, Ability: Ability{Model: "allowed", ChannelId: loaded.Id}}, map[int]*dto.AdvancedCustomConfig{loaded.Id: actual})
						expectedEndpoints := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeEmbeddings}
						if channelType == constant.ChannelTypeSGLang {
							expectedEndpoints = append(expectedEndpoints, constant.EndpointTypeJinaRerank)
						}
						assert.ElementsMatch(t, expectedEndpoints, endpoints)
					}
				})
			}
		})
	}
}
