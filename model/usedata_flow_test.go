package model

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedFlowQuotaData(t *testing.T, quotaData QuotaData) {
	t.Helper()
	require.NoError(t, DB.Create(&quotaData).Error)
}

func seedFlowLookupData(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Create(&Channel{Id: 1, Name: "east"}).Error)
	require.NoError(t, DB.Create(&Channel{Id: 2, Name: "west"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 11, UserId: 1, Key: "sk-primary", Name: "primary"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 22, UserId: 2, Key: "sk-backup", Name: "backup"}).Error)
	require.NoError(t, DB.Delete(&Token{Id: 11}).Error)
}

func TestGetFlowQuotaDataUsesQuotaDataRoleSpecificDimensions(t *testing.T) {
	truncateTables(t)
	seedFlowLookupData(t)

	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1000,
		Count:     2,
		Quota:     100,
		TokenUsed: 40,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1100,
		Count:     1,
		Quota:     50,
		TokenUsed: 20,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 2,
		CreatedAt: 1200,
		Count:     1,
		Quota:     25,
		TokenUsed: 10,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    2,
		Username:  "bob",
		NodeName:  "node-b",
		TokenID:   22,
		UseGroup:  "default",
		ModelName: "gpt-b",
		ChannelID: 1,
		CreatedAt: 1300,
		Count:     3,
		Quota:     70,
		TokenUsed: 30,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		ModelName: "legacy",
		CreatedAt: 1400,
		Count:     99,
		Quota:     999,
		TokenUsed: 999,
	})

	rootRows, err := GetFlowQuotaData(900, 2000, "", 0, common.RoleRootUser)
	require.NoError(t, err)
	require.Len(t, rootRows, 3)
	// Token 11 was soft-deleted, so its name is intentionally left empty for the
	// frontend to render a localized "deleted (id)" label instead.
	require.Equal(t, FlowQuotaData{
		UserID:      1,
		Username:    "alice",
		NodeName:    "node-a",
		TokenID:     11,
		TokenName:   "",
		UseGroup:    "vip",
		ChannelID:   1,
		ChannelName: "east",
		ModelName:   "gpt-a",
		TokenUsed:   60,
		Count:       3,
		Quota:       150,
	}, *rootRows[0])
	// A token that still exists resolves to its current name.
	require.Equal(t, 22, rootRows[1].TokenID)
	require.Equal(t, "backup", rootRows[1].TokenName)

	adminRows, err := GetFlowQuotaData(900, 2000, "alice", 0, common.RoleAdminUser)
	require.NoError(t, err)
	require.Len(t, adminRows, 2)
	require.Equal(t, 0, adminRows[0].TokenID)
	require.Empty(t, adminRows[0].TokenName)
	require.Empty(t, adminRows[0].NodeName)
	require.Equal(t, "alice", adminRows[0].Username)
	require.Equal(t, "vip", adminRows[0].UseGroup)
	require.Equal(t, "east", adminRows[0].ChannelName)
	require.Equal(t, 150, adminRows[0].Quota)

	selfRows, err := GetFlowQuotaData(900, 2000, "", 1, common.RoleCommonUser)
	require.NoError(t, err)
	require.Len(t, selfRows, 1)
	require.Empty(t, selfRows[0].Username)
	require.Equal(t, 0, selfRows[0].ChannelID)
	require.Empty(t, selfRows[0].ChannelName)
	require.Empty(t, selfRows[0].TokenName)
	require.Equal(t, "vip", selfRows[0].UseGroup)
	require.Equal(t, 175, selfRows[0].Quota)
}

func TestLogQuotaDataSplitsRowsByUseGroupTokenChannelAndNode(t *testing.T) {
	truncateTables(t)
	CacheQuotaDataLock.Lock()
	CacheQuotaData = make(map[string]*QuotaData)
	CacheQuotaDataLock.Unlock()

	LogQuotaData(QuotaDataLogParams{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-a",
		CreatedAt: 3661,
		UseGroup:  "vip",
		TokenID:   11,
		ChannelID: 1,
		NodeName:  "node-a",
		Quota:     100,
		TokenUsed: 40,
	})
	LogQuotaData(QuotaDataLogParams{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-a",
		CreatedAt: 3700,
		UseGroup:  "vip",
		TokenID:   11,
		ChannelID: 1,
		NodeName:  "node-a",
		Quota:     50,
		TokenUsed: 20,
	})
	LogQuotaData(QuotaDataLogParams{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-a",
		CreatedAt: 3700,
		UseGroup:  "default",
		TokenID:   11,
		ChannelID: 1,
		NodeName:  "node-a",
		Quota:     25,
		TokenUsed: 10,
	})

	SaveQuotaDataCache()

	var rows []QuotaData
	require.NoError(t, DB.Order("quota DESC").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, int64(3600), rows[0].CreatedAt)
	require.Equal(t, "vip", rows[0].UseGroup)
	require.Equal(t, 11, rows[0].TokenID)
	require.Equal(t, 1, rows[0].ChannelID)
	require.Equal(t, "node-a", rows[0].NodeName)
	require.Equal(t, 2, rows[0].Count)
	require.Equal(t, 150, rows[0].Quota)
	require.Equal(t, 60, rows[0].TokenUsed)
	require.Equal(t, "default", rows[1].UseGroup)
	require.Equal(t, 25, rows[1].Quota)
}

// This is the released schema before request-source aggregation was added.
type quotaDataBeforeSource struct {
	Id        int    `json:"id"`
	UserID    int    `gorm:"index"`
	Username  string `gorm:"index:idx_qdt_model_user_name,priority:2;size:64;default:''"`
	ModelName string `gorm:"index:idx_qdt_model_user_name,priority:1;size:64;default:''"`
	CreatedAt int64  `gorm:"bigint;index:idx_qdt_created_at,priority:2"`
	UseGroup  string `gorm:"index;size:64;default:''"`
	TokenID   int    `gorm:"index;default:0"`
	ChannelID int    `gorm:"index;default:0"`
	NodeName  string `gorm:"index;size:64;default:''"`
	TokenUsed int    `gorm:"default:0"`
	Count     int    `gorm:"default:0"`
	Quota     int    `gorm:"default:0"`
}

func TestSourceQuotaDataMigrationAndAggregation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
			if dialect == "sqlite" {
				dsn = "local"
				previousPath := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "source-quota.db")
				t.Cleanup(func() { common.SQLitePath = previousPath })
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("SOURCE_QUOTA_TEST_DSN", dsn)
			db, _, err := chooseDB("SOURCE_QUOTA_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.False(t, db.Migrator().HasTable(&QuotaData{}), "use an isolated source migration database")
			versionQuery := "SELECT VERSION()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database: %s %s", dialect, version)
			previousDB := DB
			DB = db
			t.Cleanup(func() { DB = previousDB })
			for _, scenario := range []string{"fresh", "upgrade"} {
				t.Run(scenario, func(t *testing.T) {
					t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&QuotaData{})) })
					legacy := quotaDataBeforeSource{UserID: 1, Username: "alice", ModelName: "model", CreatedAt: 3600, UseGroup: "default", TokenID: 2, ChannelID: 3, NodeName: "node", Count: 2, TokenUsed: 50, Quota: 500}
					if scenario == "upgrade" {
						require.NoError(t, db.Table("quota_data").AutoMigrate(&quotaDataBeforeSource{}))
						require.NoError(t, db.Table("quota_data").Create(&legacy).Error)
					}
					require.NoError(t, db.AutoMigrate(&QuotaData{}))
					if scenario == "fresh" {
						require.NoError(t, db.Table("quota_data").Create(&legacy).Error)
					}
					recorder := &migrationSQLRecorder{}
					for range 2 {
						require.NoError(t, db.Session(&gorm.Session{Logger: recorder}).AutoMigrate(&QuotaData{}))
					}
					assert.Empty(t, recorder.schemaMutations(), "restarting must not keep altering the schema")
					var saved QuotaData
					require.NoError(t, db.First(&saved, legacy.Id).Error)
					assert.Equal(t, QuotaData{Id: legacy.Id, UserID: 1, Username: "alice", ModelName: "model", CreatedAt: 3600, UseGroup: "default", TokenID: 2, ChannelID: 3, NodeName: "node", Count: 2, TokenUsed: 50, Quota: 500}, saved)
					for _, name := range []string{"idx_qdt_model_user_name", "idx_qdt_created_at"} {
						assert.True(t, db.Migrator().HasIndex(&QuotaData{}, name))
					}
					require.NoError(t, db.Model(&QuotaData{}).Where("id = ?", legacy.Id).Update("client_tool", nil).Error)
					CacheQuotaDataLock.Lock()
					CacheQuotaData = make(map[string]*QuotaData)
					CacheQuotaDataLock.Unlock()
					params := QuotaDataLogParams{UserID: 1, Username: "alice", ModelName: "model", CreatedAt: 3601, UseGroup: "default", TokenID: 2, ChannelID: 3, NodeName: "node", ClientTool: "DeepSeek Harness", TokenUsed: 15, Quota: 100}
					LogQuotaData(params)
					LogQuotaData(params)
					SaveQuotaDataCache()
					LogQuotaData(params)
					params.ClientTool = "OpenAI Python SDK"
					params.TokenUsed, params.Quota = 25, 200
					LogQuotaData(params)
					params.UserID, params.Username = 2, "bob"
					params.ModelName = "second-model"
					LogQuotaData(params)
					params.CreatedAt = 7201
					LogQuotaData(params)
					SaveQuotaDataCache()
					rows, err := GetSourceQuotaData(3600, 7199, "", 0, common.RoleAdminUser, false)
					require.NoError(t, err)
					assert.Equal(t, []SourceQuotaData{{ClientTool: "DeepSeek Harness", Count: 3, TokenUsed: 45, Quota: 300}, {ClientTool: "", Count: 2, TokenUsed: 50, Quota: 500}, {ClientTool: "OpenAI Python SDK", Count: 2, TokenUsed: 50, Quota: 400}}, rows)
					selfRows, err := GetSourceQuotaData(3600, 7199, "bob", 1, common.RoleCommonUser, false)
					require.NoError(t, err)
					assert.Equal(t, []SourceQuotaData{{ClientTool: "DeepSeek Harness", Count: 3, TokenUsed: 45, Quota: 300}, {ClientTool: "", Count: 2, TokenUsed: 50, Quota: 500}, {ClientTool: "OpenAI Python SDK", Count: 1, TokenUsed: 25, Quota: 200}}, selfRows)
					filtered, err := GetSourceQuotaData(3600, 7199, "bob", 0, common.RoleAdminUser, false)
					require.NoError(t, err)
					assert.Equal(t, []SourceQuotaData{{ClientTool: "OpenAI Python SDK", Count: 1, TokenUsed: 25, Quota: 200}}, filtered)
					for _, test := range []struct {
						name     string
						username string
						userID   int
						role     int
						want     []SourceQuotaData
					}{
						{name: "all", role: common.RoleAdminUser, want: []SourceQuotaData{
							{ClientTool: "DeepSeek Harness", CreatedAt: 3600, Count: 3, TokenUsed: 45, Quota: 300},
							{ClientTool: "", CreatedAt: 3600, Count: 2, TokenUsed: 50, Quota: 500},
							{ClientTool: "OpenAI Python SDK", CreatedAt: 3600, Count: 2, TokenUsed: 50, Quota: 400},
							{ClientTool: "OpenAI Python SDK", CreatedAt: 7200, Count: 1, TokenUsed: 25, Quota: 200},
						}},
						{name: "self", username: "bob", userID: 1, role: common.RoleCommonUser, want: []SourceQuotaData{
							{ClientTool: "DeepSeek Harness", CreatedAt: 3600, Count: 3, TokenUsed: 45, Quota: 300},
							{ClientTool: "", CreatedAt: 3600, Count: 2, TokenUsed: 50, Quota: 500},
							{ClientTool: "OpenAI Python SDK", CreatedAt: 3600, Count: 1, TokenUsed: 25, Quota: 200},
						}},
						{name: "admin username", username: "bob", role: common.RoleAdminUser, want: []SourceQuotaData{
							{ClientTool: "OpenAI Python SDK", CreatedAt: 3600, Count: 1, TokenUsed: 25, Quota: 200},
							{ClientTool: "OpenAI Python SDK", CreatedAt: 7200, Count: 1, TokenUsed: 25, Quota: 200},
						}},
					} {
						t.Run("hourly/"+test.name, func(t *testing.T) {
							series, err := GetSourceQuotaData(3600, 10799, test.username, test.userID, test.role, true)
							require.NoError(t, err)
							assert.Equal(t, test.want, series)
							summary, err := GetSourceQuotaData(3600, 10799, test.username, test.userID, test.role, false)
							require.NoError(t, err)
							totals := map[string]SourceQuotaData{}
							for _, row := range series {
								total := totals[row.ClientTool]
								total.ClientTool = row.ClientTool
								total.Count += row.Count
								total.TokenUsed += row.TokenUsed
								total.Quota += row.Quota
								totals[row.ClientTool] = total
							}
							require.Len(t, totals, len(summary))
							for _, row := range summary {
								assert.Equal(t, row, totals[row.ClientTool])
							}
							payload, err := common.Marshal(summary)
							require.NoError(t, err)
							assert.NotContains(t, string(payload), `"created_at":`)
						})
					}
					modelRows, err := GetAllQuotaDates(3600, 7199, "")
					require.NoError(t, err)
					require.Len(t, modelRows, 2)
					var modelTotal QuotaData
					for _, row := range modelRows {
						modelTotal.Count += row.Count
						modelTotal.TokenUsed += row.TokenUsed
						modelTotal.Quota += row.Quota
					}
					assert.Equal(t, 7, modelTotal.Count)
					assert.Equal(t, 145, modelTotal.TokenUsed)
					assert.Equal(t, 1200, modelTotal.Quota)
					var total int64
					require.NoError(t, db.Model(&QuotaData{}).Count(&total).Error)
					assert.EqualValues(t, 5, total, "sources and models must remain separate across cache flushes")
				})
			}
		})
	}
}

func TestSourceQuotaLabelValidation(t *testing.T) {
	var absent *LogOther
	assert.Empty(t, absent.ClientTool())
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"known", "DeepSeek Harness", "DeepSeek Harness"},
		{"declared", "CI-regression", "CI-regression"},
		{"boundary", strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{"oversized", strings.Repeat("a", 65), ""},
		{"blank", " ", ""},
		{"wrong type", 42, ""},
		{"nul", "client\x00private", ""},
		{"invalid utf8", string([]byte{0xff}), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := NewLogOther()
			other.SetPublic("client_tool", test.value)
			assert.Equal(t, test.want, other.ClientTool())
		})
	}
}

func TestSourceQuotaRecordingExcludesProbesAndErrors(t *testing.T) {
	truncateTables(t)
	previousExport, previousConsume := common.DataExportEnabled, common.LogConsumeEnabled
	common.DataExportEnabled, common.LogConsumeEnabled = true, true
	t.Cleanup(func() { common.DataExportEnabled, common.LogConsumeEnabled = previousExport, previousConsume })
	CacheQuotaDataLock.Lock()
	CacheQuotaData = make(map[string]*QuotaData)
	CacheQuotaDataLock.Unlock()
	user := &User{Username: "source-owner", Group: "default", AffCode: "source"}
	require.NoError(t, DB.Create(user).Error)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("username", user.Username)
	other := NewLogOther()
	other.SetPublic("client_tool", "curl")
	other.SetAdmin("secret", "never-in-source-response")
	params := RecordConsumeLogParams{ModelName: "model", Quota: 20, PromptTokens: 7, CompletionTokens: 3, Other: other}
	RecordConsumeLog(ctx, user.Id, params)
	params.IsChannelTest = true
	RecordConsumeLog(ctx, user.Id, params)
	RecordErrorLog(ctx, user.Id, 0, "model", "", "failed", 0, 0, false, "default", other)
	RecordTaskBillingLog(RecordTaskBillingLogParams{UserId: user.Id, ModelName: "task", Quota: 30, LogType: LogTypeConsume, Other: other})
	RecordTaskBillingLog(RecordTaskBillingLogParams{UserId: user.Id, ModelName: "task", Quota: 30, LogType: LogTypeRefund, Other: other})
	params.IsChannelTest, params.Other = false, nil
	RecordConsumeLog(ctx, user.Id, params)
	SaveQuotaDataCache()
	rows, err := GetSourceQuotaData(1, time.Now().Unix(), "", user.Id, common.RoleCommonUser, false)
	require.NoError(t, err)
	assert.Equal(t, []SourceQuotaData{{ClientTool: "curl", Count: 2, TokenUsed: 10, Quota: 50}, {ClientTool: "", Count: 1, TokenUsed: 10, Quota: 20}}, rows)
	payload, err := common.Marshal(rows)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "never-in-source-response")
}
