package model

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSatelliteSyncLockDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useSystemTaskMatrixDB(t, dialect)
			previousKind := common.MainDatabaseType()
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() { common.SetMainDatabaseType(previousKind) })
			t.Setenv("SATELLITE_NODE_ID", "lock-test-node")
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(4)
			peer := db
			if dialect != "sqlite" {
				var driver gorm.Dialector
				if dialect == "mysql" {
					driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
				} else {
					driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
				}
				peer, err = gorm.Open(driver, &gorm.Config{})
				require.NoError(t, err)
				peerSQL, err := peer.DB()
				require.NoError(t, err)
				require.NotSame(t, sqlDB, peerSQL, "overlapping gateways use independent connection pools")
				t.Cleanup(func() { assert.NoError(t, peerSQL.Close()) })
			}
			for _, scope := range []struct {
				name, key string
				acquire   func(context.Context, func() error) error
			}{
				{"satellite-sync", "lock-test-node", WithSatelliteSyncLock},
				{"sub2api", "account:41", func(ctx context.Context, operation func() error) error {
					return WithSub2APIAccountLock(ctx, 41, operation)
				}},
			} {
				t.Run(scope.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					entered, release := make(chan struct{}), make(chan struct{})
					finished := make(chan error, 1)
					operationErr := errors.New("sync exchange failed")
					go func() {
						finished <- withDatabaseOperationLock(peer, ctx, scope.name, scope.key, func() error {
							close(entered)
							select {
							case <-release:
								return operationErr
							case <-ctx.Done():
								return ctx.Err()
							}
						})
					}()
					defer func() {
						close(release)
						assert.ErrorIs(t, <-finished, operationErr)
						called := false
						assert.NoError(t, scope.acquire(ctx, func() error { called = true; return nil }))
						assert.True(t, called, "a failed exchange releases the lock for the next process")
					}()
					select {
					case <-entered:
					case err := <-finished:
						// Keep the result available to the cleanup above.
						finished <- err
						require.NoError(t, err)
					case <-ctx.Done():
						t.Fatal("lock owner did not start")
					}
					waiting, stopWaiting := context.WithTimeout(ctx, 100*time.Millisecond)
					called := false
					err := scope.acquire(waiting, func() error { called = true; return nil })
					stopWaiting()
					assert.Error(t, err)
					assert.False(t, called, "a second gateway must not read or apply another sync snapshot while one is in flight")
					if dialect != "postgres" && scope.name == "sub2api" {
						assert.ErrorIs(t, err, ErrPluginRefreshLockTimeout, "credential refresh keeps its timeout contract")
					}
					require.NoError(t, withDatabaseOperationLock(db, ctx, scope.name, scope.key+"-other", func() error { return nil }), "unrelated nodes and accounts are not blocked")
				})
			}
		})
	}
}

func TestSatelliteLogDeliveryDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useSystemTaskMatrixDB(t, dialect)
			previousLogDB, previousKind := LOG_DB, common.LogDatabaseType()
			previousSub2Storage := sub2APIStorageDB.Load()
			previousDataExport := common.DataExportEnabled
			previousNodeName := common.NodeName
			common.NodeName = "test-us2"
			common.DataExportEnabled = true
			LOG_DB = db
			common.SetLogDatabaseType(common.DatabaseType(dialect))
			t.Setenv("SATELLITE_ROLE", "satellite")
			t.Setenv("SATELLITE_NODE_ID", "test-us2")
			t.Cleanup(func() {
				assert.NoError(t, db.Migrator().DropTable(&SatelliteLogOutbox{}, &SatelliteLogReceipt{}, &Log{}, &RequestTrace{}, &RequestTracePayloadChunk{}, &Sub2APIBill{}, &QuotaData{}))
				LOG_DB = previousLogDB
				common.SetLogDatabaseType(previousKind)
				sub2APIStorageDB.Store(previousSub2Storage)
				common.DataExportEnabled = previousDataExport
				common.NodeName = previousNodeName
			})
			require.NoError(t, db.AutoMigrate(&Log{}, &RequestTrace{}, &RequestTracePayloadChunk{}, &Sub2APIBill{}, &QuotaData{}))
			sub2APIStorageDB.Store(db)
			legacy := Log{UserId: 7, RequestId: "legacy", Type: LogTypeConsume, CreatedAt: 100, Quota: 123}
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, MigrateSatelliteLogSync())
			require.NoError(t, MigrateSatelliteLogSync())
			var preserved Log
			require.NoError(t, db.First(&preserved, legacy.Id).Error)
			assert.Equal(t, legacy.Quota, preserved.Quota)

			// Failing the queue write must roll back the source row as well.
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("satellite_test_reject_outbox", func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == db.NamingStrategy.TableName("satellite_log_outboxes") {
					tx.AddError(errors.New("delivery queue unavailable"))
				}
			}))
			failed := Log{UserId: 7, RequestId: "must-roll-back", Type: LogTypeConsume, CreatedAt: 101}
			require.Error(t, createLog(&failed))
			var count int64
			require.NoError(t, db.Model(&Log{}).Where("request_id = ?", failed.RequestId).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, db.Callback().Create().Remove("satellite_test_reject_outbox"))

			metadata, err := common.Marshal(map[string]any{"admin_info": map[string]any{"sub2api_billing": Sub2APIBillingRef{
				AccountID: 1, APIKeyID: 2, KeyIdentity: strings.Repeat("a", 64), RequestID: "supplier-request",
			}}})
			require.NoError(t, err)
			log := Log{UserId: 7, TokenId: 4, ChannelId: 2, RequestId: "satellite-request", Type: LogTypeConsume, CreatedAt: 200, Quota: 456, Other: string(metadata)}
			require.NoError(t, createLog(&log))
			require.NoError(t, db.Model(&Sub2APIBill{}).Count(&count).Error)
			assert.Zero(t, count, "only the primary reconciles supplier bills")
			ctx := context.Background()
			events, err := PendingSatelliteLogEvents(ctx, 64, 4<<20)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, strconv.Itoa(log.Id), events[0].SourceID)
			cutoff, err := SatelliteTraceCleanupCutoff(ctx, 1000)
			require.NoError(t, err)
			assert.EqualValues(t, log.CreatedAt, cutoff, "pending source age, not enqueue time, protects object payloads")
			require.NoError(t, db.Delete(&log).Error)
			// Force the destination's generated IDs to differ from the sender's.
			anchor := Log{Id: log.Id + 100, RequestId: "receiver-existing", CreatedAt: 150}
			require.NoError(t, db.Create(&anchor).Error)
			if dialect == "postgres" {
				// Explicit IDs do not advance PostgreSQL's sequence; ordinary
				// generated rows already establish a different destination ID.
				require.NoError(t, db.Create(&Log{RequestId: "receiver-sequence", CreatedAt: 151}).Error)
			}
			t.Setenv("SATELLITE_ROLE", "primary")
			common.NodeName = "test-home"
			require.NoError(t, ImportSatelliteLogEvent(ctx, "test-us2", events[0]))
			require.NoError(t, ImportSatelliteLogEvent(ctx, "test-us2", events[0]))
			var imported Log
			require.NoError(t, db.Where("request_id = ?", log.RequestId).First(&imported).Error)
			assert.NotEqual(t, log.Id, imported.Id)
			assert.Equal(t, log.Quota, imported.Quota)
			var origin struct {
				Root struct {
					NodeName string `json:"node_name"`
				} `json:"root_info"`
			}
			require.NoError(t, common.UnmarshalJsonStr(imported.Other, &origin))
			assert.Equal(t, "test-us2", origin.Root.NodeName, "import preserves the request origin, not the receiving node")
			require.NoError(t, db.Model(&Log{}).Where("request_id = ?", log.RequestId).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			var bills []Sub2APIBill
			require.NoError(t, db.Find(&bills).Error)
			require.Len(t, bills, 1)
			assert.Equal(t, imported.Id, bills[0].LogID)
			var quotaRows []QuotaData
			require.NoError(t, db.Find(&quotaRows).Error)
			require.Len(t, quotaRows, 1)
			assert.Equal(t, 1, quotaRows[0].Count)
			assert.Equal(t, log.Quota, quotaRows[0].Quota)
			assert.Equal(t, "test-us2", quotaRows[0].NodeName)
			conflicting := events[0]
			changed := log
			changed.Quota++
			conflicting.Payload, err = common.Marshal(changed)
			require.NoError(t, err)
			require.Error(t, ImportSatelliteLogEvent(ctx, "test-us2", conflicting))
			require.NoError(t, AcknowledgeSatelliteLogEvents(ctx, []int64{events[0].ID}))
			require.NoError(t, AcknowledgeSatelliteLogEvents(ctx, []int64{events[0].ID}))

			// The asynchronously captured trace can arrive after its usage log.
			t.Setenv("SATELLITE_ROLE", "satellite")
			cutoff, err = SatelliteTraceCleanupCutoff(ctx, 1000)
			require.NoError(t, err)
			assert.EqualValues(t, 1000, cutoff, "an empty queue must allow normal cleanup")
			// Older/non-HTTP records may lack a request ID. Exercise the storage
			// contract directly because createLog fills one for current writers.
			taskLog := Log{UserId: 7, Type: LogTypeConsume, CreatedAt: 250, Quota: 9}
			require.NoError(t, createSatelliteLog(&taskLog))
			taskEvents, err := PendingSatelliteLogEvents(ctx, 64, 4<<20)
			require.NoError(t, err)
			require.Len(t, taskEvents, 1)
			require.NoError(t, db.Delete(&taskLog).Error)
			t.Setenv("SATELLITE_ROLE", "primary")
			require.NoError(t, ImportSatelliteLogEvent(ctx, "test-us2", taskEvents[0]))
			require.NoError(t, ImportSatelliteLogEvent(ctx, "test-us2", taskEvents[0]))
			require.NoError(t, db.Model(&Log{}).Where("request_id = ?", "").Count(&count).Error)
			assert.EqualValues(t, 1, count)
			require.NoError(t, AcknowledgeSatelliteLogEvents(ctx, []int64{taskEvents[0].ID}))
			var legacyImported Log
			require.NoError(t, db.Where("request_id = ?", "").First(&legacyImported).Error)
			require.NoError(t, common.UnmarshalJsonStr(legacyImported.Other, &origin))
			assert.Equal(t, "test-us2", origin.Root.NodeName, "missing historical metadata uses authenticated source node")
			t.Setenv("SATELLITE_ROLE", "satellite")
			client := "claude_code"
			trace := &RequestTrace{TraceId: "late-trace", RequestId: log.RequestId, Direction: TraceDirectionClientRequest,
				Seq: 1, Attempt: 1, CreatedAt: 200, ConversationKey: "conversation", ConversationParentKey: "parent",
				ConversationTurnKey: "turn", ConversationParentTurnKey: "parent-turn", ConversationAgent: "worker",
				ConversationUserId: 7, ConversationClient: &client, ConversationTitle: "hello", ConversationModel: "model"}
			body := strings.Repeat("long conversation message\n", 1000)
			require.NoError(t, StoreRequestTraceBody(trace, body, 7))
			require.NoError(t, SaveRequestTraces([]*RequestTrace{trace}))
			traceEvents, err := PendingSatelliteLogEvents(ctx, 128, 4<<20)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(traceEvents), 2)
			require.NoError(t, db.Where("id = ?", trace.Id).Delete(&RequestTrace{}).Error)
			require.NoError(t, db.Where("owner = ?", "u:7").Delete(&RequestTracePayloadChunk{}).Error)
			t.Setenv("SATELLITE_ROLE", "primary")
			var ack []int64
			for _, event := range traceEvents {
				require.NoError(t, ImportSatelliteLogEvent(ctx, "test-us2", event))
				require.NoError(t, ImportSatelliteLogEvent(ctx, "test-us2", event))
				ack = append(ack, event.ID)
			}
			stored, err := GetRequestTraceLegContext(ctx, trace.TraceId, trace.Seq)
			require.NoError(t, err)
			assert.Equal(t, body, stored.Body)
			assert.Equal(t, trace.ConversationParentKey, stored.ConversationParentKey)
			assert.Equal(t, trace.ConversationTurnKey, stored.ConversationTurnKey)
			assert.Equal(t, trace.ConversationClient, stored.ConversationClient)
			assert.Equal(t, trace.ConversationUserId, stored.ConversationUserId)
			require.NoError(t, db.Model(&RequestTrace{}).Where("trace_id = ?", trace.TraceId).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			require.NoError(t, AcknowledgeSatelliteLogEvents(ctx, ack))
			pending, err := PendingSatelliteLogEvents(ctx, 64, 4<<20)
			require.NoError(t, err)
			assert.Empty(t, pending)

			// A separately configured log database cannot atomically commit the
			// dashboard with it. Replaying repairs only the failed primary side.
			primary, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			primarySQL, err := primary.DB()
			require.NoError(t, err)
			primarySQL.SetMaxOpenConns(1)
			oldMainKind := common.MainDatabaseType()
			t.Cleanup(func() { DB = db; common.SetMainDatabaseType(oldMainKind); assert.NoError(t, primarySQL.Close()) })
			DB = primary
			common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			require.NoError(t, primary.AutoMigrate(&SatelliteLogReceipt{}, &QuotaData{}, &Sub2APIBill{}))
			sub2APIStorageDB.Store(primary)
			require.NoError(t, primary.Callback().Create().Before("gorm:create").Register("satellite_test_reject_projection", func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "quota_data" {
					tx.AddError(errors.New("dashboard temporarily unavailable"))
				}
			}))
			splitLog := log
			splitLog.Id, splitLog.RequestId, splitLog.Other = 9001, "separate-log-db", "{}"
			payload, err := common.Marshal(splitLog)
			require.NoError(t, err)
			splitEvent := SatelliteLogEvent{ID: 9001, SourceID: "9001", Kind: "log", Payload: payload}
			require.Error(t, ImportSatelliteLogEvent(ctx, "split-node", splitEvent))
			require.NoError(t, db.Model(&Log{}).Where("request_id = ?", splitLog.RequestId).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			require.NoError(t, primary.Model(&QuotaData{}).Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, primary.Callback().Create().Remove("satellite_test_reject_projection"))
			require.NoError(t, ImportSatelliteLogEvent(ctx, "split-node", splitEvent))
			require.NoError(t, ImportSatelliteLogEvent(ctx, "split-node", splitEvent))
			require.NoError(t, db.Model(&Log{}).Where("request_id = ?", splitLog.RequestId).Count(&count).Error)
			assert.EqualValues(t, 1, count)
			quotaRows = nil
			require.NoError(t, primary.Find(&quotaRows).Error)
			require.Len(t, quotaRows, 1)
			assert.Equal(t, 1, quotaRows[0].Count)
			assert.Equal(t, splitLog.Quota, quotaRows[0].Quota)
		})
	}
}

func TestSatelliteConfigurationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useSystemTaskMatrixDB(t, dialect)
			oldMain, oldLog, oldRedis := common.MainDatabaseType(), common.LogDatabaseType(), common.RedisEnabled
			common.SetDatabaseTypes(common.DatabaseType(dialect), common.DatabaseType(dialect))
			common.RedisEnabled = false
			initCol()
			t.Setenv("SATELLITE_ROLE", "primary")
			t.Setenv("SATELLITE_NODE_ID", "config-test-node")
			var tables []any
			for _, spec := range satelliteConfigSpecs() {
				tables = append(tables, spec.model)
			}
			tables = append(tables, &SatelliteReplicaState{}, &SatelliteAccountingState{})
			t.Cleanup(func() {
				assert.NoError(t, db.Migrator().DropTable(tables...))
				common.SetDatabaseTypes(oldMain, oldLog)
				common.RedisEnabled = oldRedis
				initCol()
			})
			require.NoError(t, db.AutoMigrate(tables...))
			user := User{Id: 41, Username: "config-user", Password: "private-password-hash", AffCode: "config-aff", Quota: 500, UsedQuota: 30, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			token := Token{Id: 42, UserId: user.Id, Key: "private-relay-key", Name: "relay", RemainQuota: 200, UsedQuota: 20,
				AutoGroups: `["official","backup"]`, ModelLimitsEnabled: true, ModelLimits: "test-model"}
			require.NoError(t, db.Create(&token).Error)
			require.NoError(t, db.Model(&token).Update("expired_time", 0).Error)
			channelSetting := `{"deepseek_stable_tool_schema_groups":["official"]}`
			channel := Channel{Id: 43, Name: "original", Key: "private-upstream-key", Group: "official", Models: "test-model", UsedQuota: 10, Setting: &channelSetting,
				ChannelInfo: ChannelInfo{KeyWeights: map[string]int{"physical-key": 7}}}
			require.NoError(t, db.Create(&channel).Error)
			require.NoError(t, db.Create(&Ability{Group: "official", Model: "test-model", ChannelId: channel.Id, Enabled: true}).Error)
			plan := SubscriptionPlan{Id: 44, Title: "disabled-plan", Enabled: true, DurationValue: 1}
			require.NoError(t, db.Create(&plan).Error)
			require.NoError(t, db.Model(&plan).Update("enabled", false).Error)
			deletedToken := Token{Id: 45, UserId: user.Id, Key: "revoked-key", Name: "revoked", RemainQuota: 100}
			require.NoError(t, db.Create(&deletedToken).Error)
			require.NoError(t, db.Delete(&deletedToken).Error)
			account := Sub2APIAccount{ID: 46, ChannelID: channel.Id, Origin: "https://supplier.example", Identity: strings.Repeat("b", 64),
				Credential: "private-login-token", ProxyCredential: "private-proxy-auth", InventoryJSON: "private-inventory", SyncStateJSON: "private-sync-state"}
			require.NoError(t, db.Create(&account).Error)
			require.NoError(t, db.Create(&Sub2APIKeyBinding{ID: 47, ChannelID: channel.Id, AccountID: account.ID, KeyIdentity: strings.Repeat("c", 64), UpstreamAPIKeyID: 9, SyncBills: true}).Error)
			for _, key := range []string{"SMTPToken", "ResendApiKey", "GitHubClientSecret", "oidc.client_secret", "telegram.client_secret", "StripeApiSecret", "payment_setting.epay_merchant_private_key", "WaffoSandboxPrivateKey"} {
				require.NoError(t, db.Create(&Option{Key: key, Value: "console-secret-" + key}).Error)
			}
			require.NoError(t, db.Create(&Option{Key: "WorkerValidKey", Value: "relay-worker-secret"}).Error)
			require.NoError(t, db.Create(&Option{Key: "task_plugin.private_key", Value: "relay-plugin-secret"}).Error)
			ctx := context.Background()
			snapshot, err := ExportSatelliteConfig(ctx, common.SatelliteNodeID())
			require.NoError(t, err)
			encoded, err := common.Marshal(snapshot)
			require.NoError(t, err)
			for _, secret := range []string{"private-password-hash", "private-login-token", "private-proxy-auth", "private-inventory", "private-sync-state"} {
				assert.NotContains(t, string(encoded), secret)
			}
			assert.Contains(t, string(encoded), "private-relay-key")
			assert.Contains(t, string(encoded), "private-upstream-key")
			assert.NotContains(t, string(encoded), "console-secret-")
			assert.Contains(t, string(encoded), "relay-worker-secret")
			assert.Contains(t, string(encoded), "relay-plugin-secret")
			// Start the recipient with empty configuration, preserving only the
			// received bytes as if the databases were on different machines.
			for _, spec := range satelliteConfigSpecs() {
				require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Unscoped().Delete(spec.model).Error)
			}
			t.Setenv("SATELLITE_ROLE", "satellite")
			changed, err := ApplySatelliteConfig(ctx, snapshot)
			require.NoError(t, err)
			assert.True(t, changed)
			var gotUser User
			require.NoError(t, db.First(&gotUser, user.Id).Error)
			assert.Empty(t, gotUser.Password)
			assert.Equal(t, user.Quota, gotUser.Quota)
			var gotToken Token
			require.NoError(t, db.First(&gotToken, token.Id).Error)
			assert.Equal(t, token.Key, gotToken.Key)
			assert.Equal(t, token.AutoGroups, gotToken.AutoGroups)
			assert.Zero(t, gotToken.ExpiredTime, "an explicit expired token must not become unlimited")
			var gotPlan SubscriptionPlan
			require.NoError(t, db.First(&gotPlan, plan.Id).Error)
			assert.False(t, gotPlan.Enabled)
			var gotChannel Channel
			require.NoError(t, db.First(&gotChannel, channel.Id).Error)
			assert.Equal(t, channel.ChannelInfo.KeyWeights, gotChannel.ChannelInfo.KeyWeights)
			assert.Equal(t, []string{"official"}, gotChannel.GetSetting().DeepSeekStableToolSchemaGroups, "selected tool-schema groups survive storage and satellite configuration sync")
			var abilities []Ability
			require.NoError(t, db.Find(&abilities).Error)
			require.Len(t, abilities, 1)
			assert.Equal(t, channel.Id, abilities[0].ChannelId)
			var revoked Token
			require.ErrorIs(t, db.First(&revoked, deletedToken.Id).Error, gorm.ErrRecordNotFound)
			require.NoError(t, db.Unscoped().First(&revoked, deletedToken.Id).Error)
			assert.True(t, revoked.DeletedAt.Valid)
			bindings, err := ListSub2APIKeyBindings(channel.Id)
			require.NoError(t, err)
			require.Len(t, bindings, 1)
			assert.Equal(t, account.Origin, bindings[0].AccountOrigin)
			require.NoError(t, DecreaseUserQuota(user.Id, 7, true))
			changed, err = ApplySatelliteConfig(ctx, snapshot)
			require.NoError(t, err)
			assert.False(t, changed)
			require.NoError(t, db.First(&gotUser, user.Id).Error)
			assert.Equal(t, user.Quota-7, gotUser.Quota, "replayed configuration must preserve unsynced local consumption")

			// Synchronizing a revocation must fence a reader that captured old
			// metadata before the commit, including hard-deleted identities.
			useUserCacheMiniRedis(t)
			require.NoError(t, populateUserCache(gotUser))
			staleUser := gotUser
			warmBinding, err := GetSub2APIKeyBinding(channel.Id, strings.Repeat("c", 64))
			require.NoError(t, err)
			assert.EqualValues(t, 9, warmBinding.UpstreamAPIKeyID)
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]any{"status": common.UserStatusDisabled, "auth_version": 2}).Error)
			require.NoError(t, db.Model(&Sub2APIKeyBinding{}).Where("id = ?", 47).Update("upstream_api_key_id", 11).Error)
			revokedSnapshot, err := ExportSatelliteConfig(ctx, common.SatelliteNodeID())
			require.NoError(t, err)
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]any{"status": staleUser.Status, "auth_version": staleUser.AuthVersion}).Error)
			require.NoError(t, db.Model(&Sub2APIKeyBinding{}).Where("id = ?", 47).Update("upstream_api_key_id", 9).Error)
			orphan := User{Id: 90, Username: "removed-user", AffCode: "removed-aff", Status: common.UserStatusEnabled, AuthVersion: 5}
			require.NoError(t, db.Create(&orphan).Error)
			require.NoError(t, populateUserCache(orphan))
			changed, err = ApplySatelliteConfig(ctx, revokedSnapshot)
			require.NoError(t, err)
			assert.True(t, changed)
			require.ErrorIs(t, populateUserCache(staleUser), ErrUserAuthCachePending)
			var tombstone User
			require.NoError(t, db.First(&tombstone, orphan.Id).Error)
			assert.Equal(t, common.UserStatusDisabled, tombstone.Status)
			assert.EqualValues(t, 6, tombstone.AuthVersion)
			require.ErrorIs(t, populateUserCache(orphan), ErrUserAuthCachePending)
			freshBinding, err := GetSub2APIKeyBinding(channel.Id, strings.Repeat("c", 64))
			require.NoError(t, err)
			assert.EqualValues(t, 11, freshBinding.UpstreamAPIKeyID)
			// A later changed snapshot must not keep advancing tombstone versions.
			for index := range revokedSnapshot.Tables {
				if revokedSnapshot.Tables[index].Name == db.NamingStrategy.TableName("options") {
					value, err := common.Marshal("updated-relay-secret")
					require.NoError(t, err)
					revokedSnapshot.Tables[index].Rows[0]["value"] = value
				}
			}
			payload, err := common.Marshal(revokedSnapshot.Tables)
			require.NoError(t, err)
			revokedSnapshot.Digest = requestTraceDigest(payload)
			_, err = ApplySatelliteConfig(ctx, revokedSnapshot)
			require.NoError(t, err)
			require.NoError(t, db.First(&tombstone, orphan.Id).Error)
			assert.EqualValues(t, 6, tombstone.AuthVersion)
		})
	}
}
