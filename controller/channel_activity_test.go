package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelModelActivityDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			for _, mode := range []string{"fresh", "upgrade"} {
				t.Run(mode, func(t *testing.T) {
					db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
					legacy := model.PerfMetric{ModelName: "historical-model", Group: "default", BucketTs: 100, RequestCount: 5, SuccessCount: 3}
					if mode == "upgrade" {
						require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}))
						require.NoError(t, db.Create(&legacy).Error)
					}
					require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}))
					missing, err := model.GetChannelModelActivity(1, "model-a")
					require.NoError(t, err)
					assert.Zero(t, missing)
					succeeded, failed := true, false
					require.NoError(t, model.RecordChannelModelRequest(1, "model-a", 100, 150, nil))
					require.NoError(t, model.RecordChannelModelProbe(1, "model-a", 200, &succeeded))
					require.NoError(t, model.RecordChannelModelRequest(1, "model-a", 300, 400, &failed))
					require.NoError(t, model.RecordChannelModelProbe(1, "model-a", 250, &succeeded))
					require.NoError(t, model.RecordChannelModelRequest(1, "model-a", 150, 180, &succeeded))
					require.NoError(t, model.RecordChannelModelProbe(1, "model-a", 350, nil))
					require.NoError(t, model.RecordChannelModelRequest(1, "model-a", 360, 390, nil))
					state, err := model.GetChannelModelActivity(1, "model-a")
					require.NoError(t, err)
					assert.Equal(t, model.ChannelModelActivity{ChannelID: 1, ModelName: "model-a", LastRequestAt: 360, LastProbeAt: 350, RequestActiveUntil: 400, LastResultAt: 300, LastResultSuccess: false}, state,
						"older observations cannot replace the newest outcome, and heartbeat/setup-only events must preserve it")

					// Simultaneous upserts on one pair must neither lose a newer
					// deadline nor overwrite its result with an older success.
					start := make(chan struct{})
					errors := make(chan error, 3)
					var writers sync.WaitGroup
					for _, at := range []int64{410, 430, 420} {
						writers.Add(1)
						go func(at int64) {
							defer writers.Done()
							<-start
							if at == 430 {
								errors <- model.RecordChannelModelProbe(1, "model-a", at, &failed)
								return
							}
							errors <- model.RecordChannelModelRequest(1, "model-a", at, at+50, &succeeded)
						}(at)
					}
					close(start)
					writers.Wait()
					close(errors)
					for err := range errors {
						require.NoError(t, err)
					}
					state, err = model.GetChannelModelActivity(1, "model-a")
					require.NoError(t, err)
					assert.Equal(t, model.ChannelModelActivity{ChannelID: 1, ModelName: "model-a", LastRequestAt: 420, LastProbeAt: 430, RequestActiveUntil: 470, LastResultAt: 430, LastResultSuccess: false}, state)
					require.NoError(t, model.RecordChannelModelRequest(1, "model-b", 500, 600, nil))
					require.NoError(t, model.RecordChannelModelRequest(2, "model-a", 700, 800, &succeeded))
					all, err := model.GetChannelModelActivities([]int{1, 2})
					require.NoError(t, err)
					require.Len(t, all, 3, "models and channels have independent deadlines")
					empty, err := model.GetChannelModelActivities(nil)
					require.NoError(t, err)
					assert.Empty(t, empty, "an empty filter must not enumerate every channel")
					for range 2 {
						require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}))
						after, err := model.GetChannelModelActivities([]int{1, 2})
						require.NoError(t, err)
						assert.Equal(t, all, after, "repeat migrations preserve every counter and false result")
					}
					require.Error(t, db.Create(&model.ChannelModelActivity{ChannelID: 1, ModelName: "model-a"}).Error, "composite uniqueness must remain enforced after migration")
					if mode == "upgrade" {
						var preserved model.PerfMetric
						require.NoError(t, db.First(&preserved, legacy.Id).Error)
						assert.Equal(t, legacy, preserved)
						assert.True(t, db.Migrator().HasIndex(&model.PerfMetric{}, "idx_perf_model_group_bucket"))
						assert.True(t, db.Migrator().HasIndex(&model.ChannelPerfMetric{}, "idx_channel_perf_bucket"))
					}
					// A separate database connection reads the same state without
					// any in-process activity cache, as a restarted node would.
					reopened, err := gorm.Open(db.Dialector, &gorm.Config{})
					require.NoError(t, err)
					connection, err := reopened.DB()
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, connection.Close()) })
					model.DB = reopened
					t.Cleanup(func() { model.DB = db })
					afterRestart, err := model.GetChannelModelActivities([]int{1, 2})
					require.NoError(t, err)
					assert.Equal(t, all, afterRestart)
				})
			}
		})
	}
}

func TestChannelKeyMonitoringDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			for _, mode := range []string{"fresh", "upgrade"} {
				t.Run(mode, func(t *testing.T) {
					db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
					require.NoError(t, db.Migrator().DropTable(&model.ChannelKeyObservation{}))
					require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}))
					legacy := model.PerfMetric{ModelName: "key-history", Group: "default", BucketTs: 100, RequestCount: 3, SuccessCount: 2}
					if mode == "upgrade" {
						require.NoError(t, db.Create(&legacy).Error)
					}
					logDB, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
					connection, err := logDB.DB()
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, connection.Close()) })
					require.NoError(t, logDB.AutoMigrate(&model.Log{}))
					log := model.Log{Type: model.LogTypeConsume, Content: "existing separate log", ModelName: "key-history"}
					require.NoError(t, logDB.Create(&log).Error)
					model.LOG_DB = logDB
					t.Cleanup(func() { model.LOG_DB = db })
					require.NoError(t, db.AutoMigrate(&model.ChannelKeyObservation{}))
					channel := &model.Channel{Name: "private-channel-name", Type: constant.ChannelTypeOpenAI, Key: "credential-first-private\ncredential-second-private", Models: "key-model", Group: "default,vip", BaseURL: common.GetPointer("https://fixture.invalid"), Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}, HeaderOverride: common.GetPointer(`{"X-Secret":"private-header-secret","X-Authorization":"Bearer independent-auth-secret"}`), OtherSettings: `{"advanced_custom":{"advanced_routes":[{"auth":{"type":"header","name":"X-Custom","value":"private-auth-{api_key}"}}]}}`}
					require.NoError(t, db.Create(channel).Error)
					now := time.Now().UnixMilli()
					assertCurrent := func(name string, want *bool) {
						t.Helper()
						admin, err := perfmetrics.QueryAdminModel(name, 24)
						require.NoError(t, err)
						require.Len(t, admin.Channels, 1)
						assert.Equal(t, want, admin.Channels[0].CurrentAvailable)
						public, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24, AllowedGroups: []string{"default"}})
						require.NoError(t, err)
						require.Len(t, public.Channels, 1)
						assert.Equal(t, want, public.Channels[0].CurrentAvailable)
						assert.Equal(t, admin.Channels[0].CurrentObservedAt, public.Channels[0].CurrentObservedAt)
						assert.Equal(t, want, public.CurrentAvailable)
						summary, err := perfmetrics.QuerySummaryAll(24, []string{"default"})
						require.NoError(t, err)
						var summarized *bool
						for _, entry := range summary.Models {
							if entry.ModelName == name {
								summarized = entry.CurrentAvailable
							}
						}
						assert.Equal(t, want, summarized)
						encoded, err := common.Marshal(public)
						require.NoError(t, err)
						for _, private := range []string{channel.Name, "channel_id", "key_hint", "private-header-secret", "credential-first-private"} {
							assert.NotContains(t, string(encoded), private)
						}
					}
					failure := "Authorization: Bearer unrelated-bearer-secret X-Custom: private-auth-credential-first-private X-Secret: private-header-secret credential-first-private upstream echoed independent-auth-secret"
					require.NoError(t, model.RecordChannelKeyObservation(channel, "key-model", "credential-first-private", now-100, common.GetPointer(false), 402, failure, "probe"))
					require.NoError(t, model.RecordChannelKeyObservation(channel, "key-model", "credential-second-private", now-200, common.GetPointer(true), 200, "", "request"))
					// Out-of-order and unknown outcomes must not replace a newer result.
					require.NoError(t, model.RecordChannelKeyObservation(channel, "key-model", "credential-first-private", now-300, common.GetPointer(true), 200, "", "request"))
					require.NoError(t, model.RecordChannelKeyObservation(channel, "key-model", "credential-first-private", now, nil, 0, "cancelled", "request"))
					rows, err := model.GetChannelKeyObservations([]int{channel.Id}, "key-model")
					require.NoError(t, err)
					require.Len(t, rows, 2)
					for _, row := range rows {
						assert.Len(t, row.ID, 64)
						for _, secret := range []string{"credential-first-private", "private-header-secret", "private-auth-", "unrelated-bearer-secret", "independent-auth-secret"} {
							assert.NotContains(t, row.ErrorText, secret)
						}
					}
					admin, err := perfmetrics.QueryAdminModel("key-model", 24)
					require.NoError(t, err)
					require.Len(t, admin.Channels, 1)
					require.Len(t, admin.Channels[0].Keys, 2)
					assert.Equal(t, common.GetPointer(false), admin.Channels[0].Keys[0].Success)
					assert.Equal(t, 402, admin.Channels[0].Keys[0].StatusCode)
					assert.Equal(t, common.GetPointer(true), admin.Channels[0].Keys[1].Success)
					// A newer failure on one key must not hide a still-fresh success
					// on another key, regardless of the viewer's role.
					require.NoError(t, model.RecordChannelModelRequest(channel.Id, "key-model", now-50, 0, common.GetPointer(false)))
					assertCurrent("key-model", common.GetPointer(true))
					require.NoError(t, model.RecordChannelKeyObservation(channel, "key-model", "credential-second-private", now-150, common.GetPointer(false), 503, "probe failed", "probe"))
					assertCurrent("key-model", common.GetPointer(false))
					require.NoError(t, model.RecordChannelKeyObservation(channel, "key-model", "credential-second-private", now-75, common.GetPointer(true), 200, "", "request"))
					assertCurrent("key-model", common.GetPointer(true))
					channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
					require.NoError(t, db.Save(channel).Error)
					assertCurrent("key-model", common.GetPointer(false))
					channel.Key += "\nnew-unobserved-key"
					require.NoError(t, db.Save(channel).Error)
					assertCurrent("key-model", nil)
					channel.Key = "credential-first-private\ncredential-second-private"
					channel.ChannelInfo.MultiKeyStatusList = nil
					channel.Models += ",key-legacy"
					require.NoError(t, db.Save(channel).Error)
					require.NoError(t, model.RecordChannelModelRequest(channel.Id, "key-legacy", now-50, 0, common.GetPointer(true)))
					assertCurrent("key-legacy", common.GetPointer(true))
					require.NoError(t, db.Model(&model.ChannelKeyObservation{}).Where("channel_id = ?", channel.Id).Update("observed_at", now-2*operation_setting.ChannelTestInterval().Milliseconds()).Error)
					assertCurrent("key-model", nil)
					for i := range rows {
						require.NoError(t, db.Save(&rows[i]).Error)
					}
					channel.Key = "credential-second-private\ncredential-first-private"
					channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusManuallyDisabled}
					require.NoError(t, db.Save(channel).Error)
					admin, err = perfmetrics.QueryAdminModel("key-model", 24)
					require.NoError(t, err)
					assert.Equal(t, common.GetPointer(true), admin.Channels[0].Keys[0].Success, "reordering keys must follow the credential, not the old index")
					assert.Equal(t, common.GetPointer(false), admin.Channels[0].Keys[1].Success)
					assert.False(t, admin.Channels[0].Keys[1].Enabled)
					assert.Nil(t, admin.Channels[0].Keys[1].CurrentAvailable)
					previousOtherSettings := channel.OtherSettings
					channel.OtherSettings = strings.ReplaceAll(channel.OtherSettings, "private-auth-", "changed-auth-")
					require.NoError(t, db.Save(channel).Error)
					admin, err = perfmetrics.QueryAdminModel("key-model", 24)
					require.NoError(t, err)
					assert.Nil(t, admin.Channels[0].Keys[0].Success, "independent upstream auth changes invalidate a key's observation")
					assert.Nil(t, admin.Channels[0].CurrentAvailable)
					assertCurrent("key-model", nil)
					channel.OtherSettings = previousOtherSettings
					channel.BaseURL = common.GetPointer("https://new-fixture.invalid")
					require.NoError(t, db.Save(channel).Error)
					admin, err = perfmetrics.QueryAdminModel("key-model", 24)
					require.NoError(t, err)
					assert.Nil(t, admin.Channels[0].Keys[0].Success, "a new upstream configuration cannot inherit an old success")
					assertCurrent("key-model", nil)
					for range 2 {
						require.NoError(t, db.AutoMigrate(&model.ChannelKeyObservation{}))
						after, err := model.GetChannelKeyObservations([]int{channel.Id}, "key-model")
						require.NoError(t, err)
						assert.ElementsMatch(t, rows, after)
					}
					require.Error(t, db.Create(&rows[0]).Error, "repeat migrations preserve observation uniqueness")
					assert.True(t, db.Migrator().HasIndex(&model.ChannelKeyObservation{}, "idx_channel_key_model"))
					assert.True(t, db.Migrator().HasIndex(&model.PerfMetric{}, "idx_perf_model_group_bucket"))
					if mode == "upgrade" {
						var preserved model.PerfMetric
						require.NoError(t, db.First(&preserved, legacy.Id).Error)
						assert.Equal(t, legacy, preserved)
					}
					assert.False(t, logDB.Migrator().HasTable(&model.ChannelKeyObservation{}), "private key diagnostics belong only to the main database")
					var preservedLog model.Log
					require.NoError(t, logDB.First(&preservedLog, log.Id).Error)
					assert.Equal(t, log, preservedLog)
					reopened, err := gorm.Open(db.Dialector, &gorm.Config{})
					require.NoError(t, err)
					reopenedConnection, err := reopened.DB()
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, reopenedConnection.Close()) })
					var restarted []model.ChannelKeyObservation
					require.NoError(t, reopened.Find(&restarted).Error)
					assert.ElementsMatch(t, rows, restarted)
				})
			}
		})
	}
}

func TestChannelKeyObservationModelMappingIndependence(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "mapping-monitor-fixture-secret"
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	t.Setenv("CHANNEL_TEST_FREQUENCY", "20")
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}))
			channel := &model.Channel{Id: 73, Type: constant.ChannelTypeOpenAI, Key: "mapping-fixture-key", Models: "flash,pro", Group: "default", Status: common.ChannelStatusEnabled, BaseURL: common.GetPointer("https://mapping-fixture.invalid"), ModelMapping: common.GetPointer(`{"flash":"target","pro":"old"}`)}
			require.NoError(t, db.Create(channel).Error)
			now := time.Now().UnixMilli()
			// Generated by the previous release with this exact configuration and
			// fixture secret: compatibility must read genuinely pre-v2 data.
			legacy := model.ChannelKeyObservation{ID: "9df63b69446dd51ee196f4fc4451112ea598969bfe3db76a8b001749083574ef", ChannelID: channel.Id, ModelName: "flash", ObservedAt: now - 3000, Success: true, StatusCode: 200, Source: "request"}
			require.NoError(t, db.Create(&legacy).Error)
			assertCurrent := func(t *testing.T, name string, want *bool) {
				t.Helper()
				admin, err := perfmetrics.QueryAdminModel(name, 24)
				require.NoError(t, err)
				require.Len(t, admin.Channels, 1)
				require.Len(t, admin.Channels[0].Keys, 1)
				assert.Equal(t, want, admin.Channels[0].Keys[0].CurrentAvailable)
				assert.Equal(t, want, admin.Channels[0].CurrentAvailable)
				public, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24, AllowedGroups: []string{"default"}})
				require.NoError(t, err)
				assert.Equal(t, want, public.CurrentAvailable)
				assert.Equal(t, admin.Channels[0].CurrentObservedAt, public.CurrentObservedAt)
				summary, err := perfmetrics.QuerySummaryAll(24, []string{"default"})
				require.NoError(t, err)
				found := false
				for _, entry := range summary.Models {
					if entry.ModelName == name {
						found = true
						assert.Equal(t, want, entry.CurrentAvailable)
					}
				}
				if want != nil {
					require.True(t, found)
				}
			}
			assertCurrent(t, "flash", common.GetPointer(true))
			require.NoError(t, model.RecordChannelKeyObservation(channel, "flash", channel.Key, now-2000, common.GetPointer(false), 503, "fixture failure", "probe"))
			assertCurrent(t, "flash", common.GetPointer(false))
			// A remaining old-version node can finish a later request during a
			// rollout; both versions must follow the newest matching observation.
			legacy.ObservedAt = now - 1000
			require.NoError(t, db.Save(&legacy).Error)
			assertCurrent(t, "flash", common.GetPointer(true))
			legacy.ObservedAt = now - 2000
			require.NoError(t, db.Save(&legacy).Error)
			assertCurrent(t, "flash", common.GetPointer(false))
			require.NoError(t, model.RecordChannelKeyObservation(channel, "flash", channel.Key, now-500, common.GetPointer(true), 200, "", "request"))
			require.NoError(t, model.RecordChannelKeyObservation(channel, "pro", channel.Key, now-500, common.GetPointer(true), 200, "", "probe"))
			require.NoError(t, model.RecordChannelModelProbe(channel.Id, "flash", now-500, common.GetPointer(true)))
			var original []model.ChannelKeyObservation
			require.NoError(t, db.Order("id").Find(&original).Error)
			for _, scenario := range []struct {
				name, mapping string
				want          *bool
			}{
				{"unrelated model edited", `{"flash":"target","pro":"new"}`, common.GetPointer(true)},
				{"equivalent chain and reformatted mapping", "{\n  \"pro\": \"new\", \"hop\": \"target\", \"flash\": \"hop\"\n}", common.GetPointer(true)},
				{"own upstream changed", `{"flash":"different-target","pro":"old"}`, nil},
				{"own mapping removed", `{"pro":"old"}`, nil},
				{"own mapping became a cycle", `{"flash":"hop","hop":"flash","pro":"old"}`, nil},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					channel.ModelMapping = common.GetPointer(scenario.mapping)
					require.NoError(t, db.Save(channel).Error)
					assertCurrent(t, "flash", scenario.want)
					if scenario.name == "unrelated model edited" {
						assertCurrent(t, "pro", nil)
					}
				})
			}
			// Explicit effort suffixes use the same base-name fallback as relay
			// model mapping, including chained aliases and self-mapping terminals.
			name := "flash@effort:high"
			channel.Models += "," + name
			channel.ModelMapping = common.GetPointer(`{"flash":"hop","hop":"target","target":"target","pro":"old"}`)
			require.NoError(t, db.Save(channel).Error)
			require.NoError(t, model.RecordChannelKeyObservation(channel, name, channel.Key, now-500, common.GetPointer(true), 200, "", "probe"))
			channel.ModelMapping = common.GetPointer(`{"flash":"target","pro":"new"}`)
			require.NoError(t, db.Save(channel).Error)
			assertCurrent(t, name, common.GetPointer(true))
			channel.ModelMapping = common.GetPointer(`{"flash":"different-target","pro":"new"}`)
			require.NoError(t, db.Save(channel).Error)
			assertCurrent(t, name, nil)
			var preserved []model.ChannelKeyObservation
			ids := make([]string, len(original))
			for i, row := range original {
				ids[i] = row.ID
			}
			require.NoError(t, db.Where("id IN ?", ids).Order("id").Find(&preserved).Error)
			assert.Equal(t, original, preserved, "configuration edits must not delete historical observations")
		})
	}
}

func TestChannelKeyMonitoringAdminAPI(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.Log{}, &model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}))
	previousPerf, previousInterval := perf_metrics_setting.GetSetting(), common.RequestInterval
	previousGroups := ratio_setting.GroupRatio2JSONString()
	common.RequestInterval = 0
	withSelfUseModeDisabled(t)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"admin-key-model":1}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	t.Cleanup(func() {
		require.NoError(t, perfmetrics.Flush())
		common.RequestInterval = previousInterval
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})
	admin := &model.User{Username: "key-monitor-admin", Password: "unused", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "keyadmin"}
	user := &model.User{Username: "key-monitor-user", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "keyuser"}
	require.NoError(t, db.Create(admin).Error)
	require.NoError(t, db.Create(user).Error)
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		w.Header().Set("Content-Type", "application/json")
		if key != "private-key-success" {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = fmt.Fprintf(w, `{"error":{"message":"insufficient balance for %s","type":"insufficient_quota"}}`, key)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	t.Cleanup(upstream.Close)
	channel := &model.Channel{Name: "administrator-visible-provider", Type: constant.ChannelTypeOpenAI, Key: "private-key-fail\nprivate-key-success\nprivate-key-last-fail\nprivate-key-disabled", Models: "admin-key-model", Group: "default", BaseURL: common.GetPointer(upstream.URL), Status: common.ChannelStatusEnabled, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 4, MultiKeyPollingIndex: 2, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyStatusList: map[int]int{3: common.ChannelStatusManuallyDisabled}}}
	require.NoError(t, db.Create(channel).Error)
	result := testChannelModel(context.Background(), channel, admin.Id, "admin-key-model", "", false)
	require.NoError(t, result.localErr)
	require.NotNil(t, result.sample)
	assert.True(t, result.sample.Success, "a final failed key must not replace an earlier successful key")
	assert.Equal(t, []string{"private-key-fail", "private-key-success", "private-key-last-fail"}, seen)
	require.NoError(t, perfmetrics.Flush())
	var probe model.ChannelPerfMetric
	require.NoError(t, db.Where("channel_id = ? AND source = ?", channel.Id, perfmetrics.SourceProbe).First(&probe).Error)
	assert.EqualValues(t, 1, probe.RequestCount)
	assert.EqualValues(t, 1, probe.SuccessCount)
	assert.Equal(t, 2, channel.ChannelInfo.MultiKeyPollingIndex)
	router := gin.New()
	router.GET("/api/perf-metrics/admin", middleware.DisableCache(), middleware.AdminAuth(), GetAdminPerfMetrics)
	for _, test := range []struct {
		name   string
		user   *model.User
		status int
	}{{"anonymous", nil, http.StatusUnauthorized}, {"ordinary user", user, http.StatusForbidden}, {"administrator", admin, http.StatusOK}} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/perf-metrics/admin?model=admin-key-model", nil)
			if test.user != nil {
				session, err := service.CreateLoginSession(test.user.Id, "password", "127.0.0.1", "key-monitor-test")
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer "+session.AccessToken)
				request.Header.Set("X-Auth-Session", session.Session.SID)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assert.Equal(t, test.status, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")
			for _, key := range channel.GetKeys() {
				assert.NotContains(t, recorder.Body.String(), key)
			}
			if test.status != http.StatusOK {
				assert.NotContains(t, recorder.Body.String(), channel.Name)
				return
			}
			var payload struct {
				Success bool                         `json:"success"`
				Data    perfmetrics.AdminModelResult `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.True(t, payload.Success)
			require.Len(t, payload.Data.Channels, 1)
			entry := payload.Data.Channels[0]
			assert.Equal(t, channel.Name, entry.ChannelName)
			assert.Equal(t, channel.Id, entry.ChannelID)
			require.Len(t, entry.Keys, 4)
			assert.Equal(t, common.GetPointer(false), entry.Keys[0].Success)
			assert.Equal(t, 402, entry.Keys[0].StatusCode)
			assert.Contains(t, entry.Keys[0].Error, "insufficient balance")
			assert.Equal(t, common.GetPointer(true), entry.Keys[1].CurrentAvailable)
			assert.Equal(t, common.GetPointer(false), entry.Keys[2].Success)
			assert.False(t, entry.Keys[3].Enabled)
			assert.Nil(t, entry.Keys[3].Success)
		})
	}
	public, err := perfmetrics.Query(perfmetrics.QueryParams{Model: "admin-key-model", Hours: 24, AllowedGroups: []string{"default"}})
	require.NoError(t, err)
	encoded, err := common.Marshal(public)
	require.NoError(t, err)
	for _, private := range []string{channel.Name, "channel_id", "key_hint", "insufficient balance", "private-key-fail"} {
		assert.NotContains(t, string(encoded), private)
	}
	// Real observations still update administrator diagnostics with hourly metrics off.
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false"}))
	info := &relaycommon.RelayInfo{OriginModelName: "admin-key-model", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ChannelType: channel.Type, ChannelBaseUrl: channel.GetBaseURL(), ApiKey: "private-key-fail"}}
	perfmetrics.RecordChannelKeyResult(info, channel, common.GetPointer(true), 200, "")
	private, err := perfmetrics.QueryAdminModel("admin-key-model", 24)
	require.NoError(t, err)
	assert.Equal(t, common.GetPointer(true), private.Channels[0].Keys[0].Success)
	assert.Empty(t, private.Channels[0].Keys[0].Error)
}

func TestChannelRequestActivityLifecycle(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousMinutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "false", "monitor_setting.auto_test_channel_minutes": "0.016666666666666666"}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
			"perf_metrics_setting.enabled":              strconv.FormatBool(previousPerf.Enabled),
			"monitor_setting.auto_test_channel_minutes": strconv.FormatFloat(previousMinutes, 'g', -1, 64),
		}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}, &model.ChannelPerfMetric{}))
			// Observe actual heartbeat writes, without sleeping or testing
			// scheduling speed. Register before requests start, and remove
			// after they finish. Buffered notices cannot block relay callbacks.
			heartbeats := make(chan model.ChannelModelActivity, 8)
			var initialRequestAt atomic.Int64
			require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:channel_activity_events", func(tx *gorm.DB) {
				row, ok := tx.Statement.Dest.(*model.ChannelModelActivity)
				if !ok || tx.Error != nil || row.ChannelID != 11 || row.ModelName != "active-model" {
					return
				}
				baseline := initialRequestAt.Load()
				if baseline == 0 || (row.LastResultAt == 0 && row.LastRequestAt <= baseline) {
					return
				}
				if row.LastResultAt > 0 {
					return
				}
				select {
				case heartbeats <- *row:
				default:
				}
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove("test:channel_activity_events")) })
			info := &relaycommon.RelayInfo{OriginModelName: "active-model", IsStream: true}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			finishFirst := perfmetrics.BeginChannelRequest(ctx, info, 11)
			finishSecond := perfmetrics.BeginChannelRequest(context.Background(), info, 11)
			t.Cleanup(func() { finishFirst(nil); finishSecond(nil) })
			started, err := model.GetChannelModelActivity(11, "active-model")
			require.NoError(t, err)
			assert.Positive(t, started.LastRequestAt)
			assert.Greater(t, started.RequestActiveUntil, started.LastRequestAt)
			assert.Zero(t, started.LastResultAt)
			initialRequestAt.Store(started.LastRequestAt)
			info.OriginModelName = "retry-other-model"

			select {
			case renewed := <-heartbeats:
				assert.Greater(t, renewed.RequestActiveUntil, started.RequestActiveUntil, "an open stream must extend its activity lease")
			case <-time.After(5 * time.Second):
				t.Fatal("active stream did not renew its lease")
			}
			cancel()
			finishFirst(nil)
			cancelled, err := model.GetChannelModelActivity(11, "active-model")
			require.NoError(t, err)
			assert.Zero(t, cancelled.LastResultAt, "client cancellation is not an upstream failure")
			select {
			case <-heartbeats:
			case <-time.After(5 * time.Second):
				t.Fatal("ending one request stopped the other active request's heartbeat")
			}
			finishSecond(common.GetPointer(true))
			finished, err := model.GetChannelModelActivity(11, "active-model")
			require.NoError(t, err)
			assert.True(t, finished.LastResultSuccess)
			assert.GreaterOrEqual(t, finished.LastRequestAt, cancelled.LastRequestAt)
			assert.Equal(t, finished.LastRequestAt, finished.LastResultAt)
			assert.Zero(t, finished.LastProbeAt, "real traffic must not be recorded as an active probe")
			finishSecond(common.GetPointer(false))
			repeated, err := model.GetChannelModelActivity(11, "active-model")
			require.NoError(t, err)
			assert.Equal(t, finished, repeated)
			mutated, err := model.GetChannelModelActivity(11, info.OriginModelName)
			require.NoError(t, err)
			assert.Zero(t, mutated, "retry metadata must not change the captured pair")
			cancelledCtx, cancelBeforeFinish := context.WithCancel(context.Background())
			previousResultAt := time.Now().Add(-time.Second).UnixMilli()
			require.NoError(t, model.RecordChannelModelProbe(11, "cancel-before-finish", previousResultAt, common.GetPointer(true)))
			finishCancelled := perfmetrics.BeginChannelRequest(cancelledCtx, &relaycommon.RelayInfo{OriginModelName: "cancel-before-finish"}, 11)
			cancelBeforeFinish()
			finishCancelled(nil)
			cancelledBeforeFinish, err := model.GetChannelModelActivity(11, "cancel-before-finish")
			require.NoError(t, err)
			assert.Equal(t, previousResultAt, cancelledBeforeFinish.LastResultAt, "cancellation must not overwrite the last completed upstream observation")
			assert.True(t, cancelledBeforeFinish.LastResultSuccess)
			perfmetrics.BeginChannelRequest(context.Background(), &relaycommon.RelayInfo{OriginModelName: "manual-probe", IsChannelTest: true}, 12)(common.GetPointer(true))
			perfmetrics.BeginChannelRequest(context.Background(), nil, 12)(common.GetPointer(true))
			all, err := model.GetChannelModelActivities([]int{11, 12})
			require.NoError(t, err)
			require.Len(t, all, 2, "tests and nil relay metadata do not postpone active checks")
			var probes int64
			require.NoError(t, db.Model(&model.ChannelPerfMetric{}).Count(&probes).Error)
			assert.Zero(t, probes, "activity is independent of the disabled metrics feature")
		})
	}
}

func TestChannelIdleMonitoringUsesRealRequests(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousMinutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	previousInterval := common.RequestInterval
	common.RequestInterval = 0
	withSelfUseModeDisabled(t)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true", "monitor_setting.auto_test_channel_minutes": "10"}))
	t.Cleanup(func() {
		common.RequestInterval = previousInterval
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
			"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled), "monitor_setting.auto_test_channel_minutes": strconv.FormatFloat(previousMinutes, 'g', -1, 64),
		}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}, &model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.Log{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			user := &model.User{Username: "idle-monitor-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "idlemonitor"}
			require.NoError(t, db.Create(user).Error)
			shared, idle, failed, unknown := "idle-shared-"+dialect.kind, "idle-other-"+dialect.kind, "idle-failed-"+dialect.kind, "idle-unknown-"+dialect.kind
			prices, err := common.Marshal(map[string]float64{shared: 1, idle: 1, failed: 1, unknown: 1})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(prices)))
			var forceFirstFailure atomic.Bool
			var recoverFirstOnSecondProbe atomic.Bool
			var firstChannelID atomic.Int64
			var requestsMu sync.Mutex
			requests := map[string]int{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body dto.GeneralOpenAIRequest
				if !assert.NoError(t, common.DecodeJson(r.Body, &body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				first := r.Header.Get("Authorization") == "Bearer first-fixture-key"
				route := "second/"
				if first {
					route = "first/"
				}
				requestsMu.Lock()
				requests[route+body.Model]++
				requestsMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if !first && recoverFirstOnSecondProbe.Load() {
					success := true
					assert.NoError(t, model.RecordChannelModelRequest(int(firstChannelID.Load()), shared, time.Now().UnixMilli(), 0, &success))
				}
				if !first || forceFirstFailure.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = fmt.Fprint(w, `{"error":{"message":"fixture unavailable","type":"server_error"}}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"id":"test","model":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
			}))
			t.Cleanup(upstream.Close)
			now := time.Now()
			interval := operation_setting.ChannelTestInterval()
			channels := []*model.Channel{
				{Type: constant.ChannelTypeOpenAI, Key: "first-fixture-key", Status: common.ChannelStatusEnabled, Models: strings.Join([]string{shared, idle, failed, unknown}, ","), Group: "default", BaseURL: common.GetPointer(upstream.URL), TestTime: now.Add(-2 * interval).Unix()},
				{Type: constant.ChannelTypeOpenAI, Key: "second-fixture-key", Status: common.ChannelStatusEnabled, Models: shared, Group: "default", BaseURL: common.GetPointer(upstream.URL), TestTime: now.Add(-2 * interval).Unix()},
			}
			for _, channel := range channels {
				require.NoError(t, db.Create(channel).Error)
			}
			firstChannelID.Store(int64(channels[0].Id))
			succeeded, failure := true, false
			require.NoError(t, model.RecordChannelModelRequest(channels[0].Id, shared, now.Add(-time.Minute).UnixMilli(), 0, &succeeded))
			require.NoError(t, model.RecordChannelModelRequest(channels[0].Id, failed, now.Add(-time.Minute).UnixMilli(), 0, &failure))
			require.NoError(t, model.RecordChannelModelRequest(channels[0].Id, unknown, now.UnixMilli(), now.Add(interval).UnixMilli(), nil))
			require.NoError(t, model.RecordChannelModelRequest(channels[1].Id, shared, now.Add(-2*interval).UnixMilli(), 0, &succeeded))
			for _, request := range []struct {
				name    string
				success bool
			}{{shared, true}, {failed, false}} {
				info := &relaycommon.RelayInfo{OriginModelName: request.name, UsingGroup: user.Group, StartTime: now, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channels[0].Id}}
				perfmetrics.RecordChannelAttempt(info, channels[0].Id, request.success)
				perfmetrics.RecordRelaySample(info, request.success, 0)
				perfmetrics.RecordChannelRelayResult(info, request.success)
			}
			summary, err := runChannelTestTask(context.Background(), "", false, nil, true)
			require.NoError(t, err)
			assert.Equal(t, channelTestSummary{Tested: 2, Succeeded: 1, Failed: 1, Skipped: 3}, summary)
			requestsMu.Lock()
			assert.Equal(t, map[string]int{"first/" + idle: 1, "second/" + shared: 1}, requests, "activity skips only its own channel/model, including recent failures")
			requestsMu.Unlock()
			sharedDetail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: shared, Hours: 24})
			require.NoError(t, err)
			require.NotNil(t, sharedDetail.AvailabilityRate)
			assert.Equal(t, float64(100), *sharedDetail.AvailabilityRate, "one fresh real success keeps the model available while another channel's active probe fails")
			require.Len(t, sharedDetail.Channels, 2)
			require.NotNil(t, sharedDetail.Channels[0].AvailabilityRate)
			require.NotNil(t, sharedDetail.Channels[1].AvailabilityRate)
			assert.Equal(t, float64(100), *sharedDetail.Channels[0].AvailabilityRate)
			assert.Zero(t, *sharedDetail.Channels[1].AvailabilityRate)
			failedDetail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: failed, Hours: 24})
			require.NoError(t, err)
			require.NotNil(t, failedDetail.AvailabilityRate)
			assert.Zero(t, *failedDetail.AvailabilityRate, "skipping a fresh failed request must not turn it into a success")
			unknownDetail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: unknown, Hours: 24})
			require.NoError(t, err)
			assert.Nil(t, unknownDetail.AvailabilityRate, "an unfinished request is activity, not an observed successful result")
			require.Len(t, unknownDetail.Channels, 1)
			assert.Nil(t, unknownDetail.Channels[0].AvailabilityRate)
			var physical []model.ChannelPerfMetric
			require.NoError(t, db.Where("source = ?", perfmetrics.SourceProbe).Find(&physical).Error)
			require.Len(t, physical, 2, "borrowed real outcomes do not manufacture physical probe samples")
			for _, name := range []string{shared, failed, unknown} {
				activity, err := model.GetChannelModelActivity(channels[0].Id, name)
				require.NoError(t, err)
				assert.Zero(t, activity.LastProbeAt)
			}

			forceFirstFailure.Store(true)
			manual := testChannel(context.Background(), channels[0], user.Id, shared, "", false)
			require.Error(t, manual.localErr)
			assert.Equal(t, channelTestSummary{Tested: 1, Failed: 1}, manual.summary)
			require.NoError(t, perfmetrics.Flush())
			var manualState model.ChannelPerfMetric
			require.NoError(t, db.Where("channel_id = ? AND model_name = ? AND source = ?", channels[0].Id, shared, perfmetrics.SourceRouteState).First(&manualState).Error)
			assert.EqualValues(t, 1, manualState.RequestCount, "a skipped real request must not manufacture an extra route-state observation")
			assert.Zero(t, manualState.SuccessCount, "the physical manual failure is recorded once")
			sharedDetail, err = perfmetrics.Query(perfmetrics.QueryParams{Model: shared, Hours: 24})
			require.NoError(t, err)
			require.Len(t, sharedDetail.Channels, 2)
			require.NotNil(t, sharedDetail.Channels[0].AvailabilityRate)
			assert.Equal(t, float64(50), *sharedDetail.Channels[0].AvailabilityRate)
			forceFirstFailure.Store(false)
			manualAll, err := runChannelTestTask(context.Background(), operation_setting.ChannelTestModeScheduledAll, false, nil, false)
			require.NoError(t, err)
			assert.Equal(t, channelTestSummary{Tested: 5, Succeeded: 4, Failed: 1}, manualAll, "manual test-all ignores request/probe deadlines and in-flight leases")
			requestsMu.Lock()
			assert.Equal(t, map[string]int{"first/" + shared: 2, "first/" + idle: 2, "first/" + failed: 1, "first/" + unknown: 1, "second/" + shared: 2}, requests)
			requestsMu.Unlock()

			// Scheduled jobs queued by the previous release carried no payload.
			// Exercise the real handler and persisted task result so decoding
			// must preserve idle skipping rather than silently forcing probes.
			var beforeSkipped []model.ChannelPerfMetric
			require.NoError(t, db.Order("id ASC").Find(&beforeSkipped).Error)
			require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
			legacyTask, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
			require.NoError(t, err)
			claimed, ok, err := model.ClaimSystemTask(legacyTask.ID, legacyTask.Type, "idle-compat-runner", common.GetTimestamp()+60)
			require.NoError(t, err)
			require.True(t, ok)
			(channelTestHandler{}).Run(context.Background(), claimed, "idle-compat-runner")
			finishedTask, err := model.GetSystemTaskByTaskID(legacyTask.TaskID)
			require.NoError(t, err)
			require.NotNil(t, finishedTask)
			assert.Equal(t, model.SystemTaskStatusSucceeded, finishedTask.Status)
			var legacySummary channelTestSummary
			require.NoError(t, common.UnmarshalJsonStr(finishedTask.Result, &legacySummary))
			assert.Equal(t, channelTestSummary{Skipped: 5}, legacySummary)
			var afterSkipped []model.ChannelPerfMetric
			require.NoError(t, db.Order("id ASC").Find(&afterSkipped).Error)
			assert.Equal(t, beforeSkipped, afterSkipped, "a round with only cached outcomes must not repeat historical successes or failures")
			requestsMu.Lock()
			assert.Equal(t, map[string]int{"first/" + shared: 2, "first/" + idle: 2, "first/" + failed: 1, "first/" + unknown: 1, "second/" + shared: 2}, requests, "legacy scheduled payloads must not dispatch duplicate active probes")
			requestsMu.Unlock()

			for _, probeFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("real_success_during_round_probe_first_%t", probeFirst), func(t *testing.T) {
					recoverFirstOnSecondProbe.Store(true)
					forceFirstFailure.Store(true)
					now := time.Now()
					firstRequestAt := now.UnixMilli()
					if probeFirst {
						firstRequestAt = now.Add(-2 * interval).UnixMilli()
					}
					require.NoError(t, db.Model(&model.ChannelModelActivity{}).Where("channel_id = ? AND model_name = ?", channels[0].Id, shared).Updates(map[string]any{
						"last_request_at": firstRequestAt, "last_probe_at": firstRequestAt, "request_active_until": 0,
						"last_result_at": firstRequestAt, "last_result_success": false,
					}).Error)
					require.NoError(t, db.Model(&model.ChannelModelActivity{}).Where("channel_id = ? AND model_name = ?", channels[1].Id, shared).Updates(map[string]any{
						"last_request_at": now.Add(-2 * interval).UnixMilli(), "last_probe_at": now.Add(-2 * interval).UnixMilli(), "request_active_until": 0,
					}).Error)
					var beforeRound, beforeFirst, beforeUnrelated model.ChannelPerfMetric
					require.NoError(t, db.Where("model_name = ? AND source = ?", shared, perfmetrics.SourceAvailabilityAll).First(&beforeRound).Error)
					require.NoError(t, db.Where("model_name = ? AND channel_id = ? AND source = ?", shared, channels[0].Id, perfmetrics.SourceRouteState).First(&beforeFirst).Error)
					require.NoError(t, db.Where("model_name = ? AND source = ?", failed, perfmetrics.SourceAvailabilityAll).First(&beforeUnrelated).Error)
					ctx := context.WithValue(context.Background(), channelIdleProbeIntervalKey{}, interval)
					// One worker fixes the order: A is skipped/probed first, then
					// a real success for A finishes while B's probe is in flight.
					result := performChannelTests(ctx, channels, user.Id, 1, nil)
					want := channelTestSummary{Tested: 1, Failed: 1, Skipped: 4}
					if probeFirst {
						want = channelTestSummary{Tested: 2, Failed: 2, Skipped: 3}
					}
					assert.Equal(t, want, result)
					var afterRound, afterFirst, afterUnrelated model.ChannelPerfMetric
					require.NoError(t, db.First(&afterRound, beforeRound.Id).Error)
					require.NoError(t, db.First(&afterFirst, beforeFirst.Id).Error)
					require.NoError(t, db.First(&afterUnrelated, beforeUnrelated.Id).Error)
					assert.Equal(t, beforeRound.RequestCount+1, afterRound.RequestCount)
					assert.Equal(t, beforeRound.SuccessCount+1, afterRound.SuccessCount, "the latest completed real success must win over the earlier failure snapshot")
					if probeFirst {
						assert.Equal(t, beforeFirst.RequestCount+1, afterFirst.RequestCount)
						assert.Equal(t, beforeFirst.SuccessCount+1, afterFirst.SuccessCount)
					} else {
						assert.Equal(t, beforeFirst, afterFirst, "a skipped channel's cached result assists OR without adding another route-state sample")
					}
					assert.Equal(t, beforeUnrelated, afterUnrelated, "another model's probe cannot repeat this model's cached history")
				})
			}
		})
	}
}

func TestChannelIdleScheduleUsesPairDeadline(t *testing.T) {
	previousMinutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"monitor_setting.auto_test_channel_minutes": "10"}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"monitor_setting.auto_test_channel_minutes": strconv.FormatFloat(previousMinutes, 'g', -1, 64)}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ChannelModelActivity{}))
			channel := &model.Channel{Status: common.ChannelStatusEnabled, Models: "deadline-model", Group: "default"}
			require.NoError(t, db.Create(channel).Error)
			start := time.Now().Truncate(time.Second)
			interval := operation_setting.ChannelTestInterval()
			success, failure := true, false
			require.NoError(t, model.RecordChannelModelRequest(channel.Id, channel.Models, start.UnixMilli(), 0, &success))
			latest := &model.SystemTask{UpdatedAt: start.Add(interval - 10*time.Second).Unix(), Status: model.SystemTaskStatusSucceeded}
			for _, event := range []struct {
				name string
				at   time.Time
				want bool
			}{{"before_request_deadline", start.Add(interval - time.Millisecond), false}, {"at_request_deadline", start.Add(interval), true}} {
				due, err := (channelTestHandler{}).ShouldSchedule(event.at, latest)
				require.NoError(t, err)
				assert.Equal(t, event.want, due, event.name+": a recent global sweep cannot delay this pair by another full interval")
			}
			failedAt := start.Add(interval / 2)
			require.NoError(t, model.RecordChannelModelRequest(channel.Id, channel.Models, failedAt.UnixMilli(), 0, &failure))
			for _, event := range []struct {
				at   time.Time
				want bool
			}{{start.Add(interval), false}, {failedAt.Add(interval - time.Millisecond), false}, {failedAt.Add(interval), true}} {
				due, err := (channelTestHandler{}).ShouldSchedule(event.at, latest)
				require.NoError(t, err)
				assert.Equal(t, event.want, due, "failed real calls restart exactly the same idle interval as successful calls")
			}
			checkedAt := failedAt.Add(interval)
			require.NoError(t, model.RecordChannelModelProbe(channel.Id, channel.Models, checkedAt.UnixMilli(), nil))
			latest.UpdatedAt = checkedAt.Unix()
			due, err := (channelTestHandler{}).ShouldSchedule(checkedAt.Add(15*time.Second), latest)
			require.NoError(t, err)
			assert.False(t, due, "even a setup-only check backs off instead of creating a task on every scheduler tick")
			inflight := model.ChannelModelActivity{LastRequestAt: start.UnixMilli(), RequestActiveUntil: checkedAt.Add(interval).UnixMilli()}
			assert.False(t, channelModelProbeDue(inflight, 0, checkedAt, interval), "an active stream is not idle after its initial start timestamp ages")
			assert.True(t, channelModelProbeDue(inflight, 0, checkedAt.Add(interval), interval), "expired leases cannot keep a crashed request active forever")
		})
	}
}

func TestChannelAvailabilityCombinesObservations(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.ChannelPerfMetric{}, &model.PerfMetric{}, &model.ChannelModelActivity{}, &model.Log{}))
			channel := &model.Channel{Status: common.ChannelStatusEnabled, Models: "source-a,source-b,source-c", Group: "default"}
			require.NoError(t, db.Create(channel).Error)
			hour := time.Now().Unix() / 3600 * 3600
			rows := []model.ChannelPerfMetric{
				{ModelName: "source-a", Group: "default", Source: perfmetrics.SourceRequest, BucketTs: hour - 7200, RequestCount: 10, SuccessCount: 8, TotalLatencyMs: 1000, OutputTokens: 9999, GenerationMs: 1000},
				{ModelName: "source-a", Group: "default", Source: perfmetrics.SourceUsage, BucketTs: hour - 7200, RequestCount: 10, SuccessCount: 8, OutputTokens: 100, GenerationMs: 1000},
				{ModelName: "source-a", Source: perfmetrics.SourceProbe, BucketTs: hour - 7200, RequestCount: 2, SuccessCount: 1, TotalLatencyMs: 600, OutputTokens: 40, GenerationMs: 2000},
				{ModelName: "source-a", Source: perfmetrics.SourceRouteState, BucketTs: hour - 7200 + 60, RequestCount: 1, SuccessCount: 1, TotalLatencyMs: 999999, OutputTokens: 99999, GenerationMs: 1},
				{ModelName: "source-a", Group: "default", Source: perfmetrics.SourceRequest, BucketTs: hour - 3600, RequestCount: 10, SuccessCount: 10, TotalLatencyMs: 2000},
				{ModelName: "source-a", Source: perfmetrics.SourceProbe, BucketTs: hour - 3600, RequestCount: 2, SuccessCount: 0, TotalLatencyMs: 1200, OutputTokens: 60, GenerationMs: 1000},
				{ModelName: "source-a", Group: "default", Source: perfmetrics.SourceRequest, BucketTs: hour, RequestCount: 4, SuccessCount: 3, TotalLatencyMs: 800},
				{ModelName: "source-b", Group: "default", Source: perfmetrics.SourceRequest, BucketTs: hour - 7200, RequestCount: 4, SuccessCount: 0},
				{ModelName: "source-b", Source: perfmetrics.SourceProbe, BucketTs: hour - 3600, RequestCount: 1, SuccessCount: 1},
				{ModelName: "source-c", Group: "default", Source: perfmetrics.SourceRequest, BucketTs: hour, RequestCount: 2, SuccessCount: 1},
			}
			for i := range rows {
				rows[i].ChannelID = channel.Id
			}
			require.NoError(t, db.Create(&rows).Error)
			require.NoError(t, db.Create(&model.PerfMetric{ModelName: "source-c", Group: "default", BucketTs: hour, RequestCount: 2, SuccessCount: 1}).Error)
			usage, err := perfmetrics.QueryChannelUsage(channel.Id, 24)
			require.NoError(t, err)
			require.NotNil(t, usage.AvailabilityRate)
			assert.Equal(t, 68.57, *usage.AvailabilityRate, "combine actual requests and probes without repeating cached route states: 24 successes / 35 observations")
			assert.Equal(t, []perfmetrics.SuccessRatePoint{{Ts: hour - 7200, SuccessRate: 56.25}, {Ts: hour - 3600, SuccessRate: 84.62}, {Ts: hour, SuccessRate: 66.67}}, usage.Series)
			assert.EqualValues(t, 30, usage.RequestCount, "monitor assessments cannot inflate real request counts")
			assert.EqualValues(t, 5, usage.ProbeCount)
			for name, expected := range map[string]float64{"source-a": 78.57, "source-b": 20, "source-c": 50} {
				detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24})
				require.NoError(t, err)
				require.Len(t, detail.Channels, 1)
				require.NotNil(t, detail.Channels[0].AvailabilityRate)
				assert.Equal(t, expected, *detail.Channels[0].AvailabilityRate)
				if name == "source-a" {
					assert.EqualValues(t, 200, detail.Channels[0].AvgLatencyMs, "3800 request milliseconds + 1800 probe milliseconds over 28 actual observations")
					assert.Equal(t, float64(50), detail.Channels[0].AvgTps, "100 settled request tokens + 100 probe tokens over 4 seconds; request copies and route states do not double-count tokens")
				}
				if name == "source-c" {
					require.NotNil(t, detail.AvailabilityRate)
					assert.Equal(t, expected, *detail.AvailabilityRate, "only real requests still provide historical monitoring when no active checks ran")
				}
			}
		})
	}
}
