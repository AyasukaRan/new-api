package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kittypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelTestLogsAreSeparatedFromUsage(t *testing.T) {
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	t.Cleanup(func() { common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport })
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			user := &model.User{Username: "log-test-owner", Group: "default", AffCode: "logtest"}
			require.NoError(t, db.Create(user).Error)
			channel := &model.Channel{Name: "test-record-channel"}
			require.NoError(t, db.Create(channel).Error)
			logDB, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			model.LOG_DB = logDB
			t.Cleanup(func() {
				model.LOG_DB = db
				connection, err := logDB.DB()
				require.NoError(t, err)
				require.NoError(t, connection.Close())
			})
			now := time.Now().Unix()
			logs := []model.Log{
				{Type: model.LogTypeConsume, TokenName: "模型测试", Content: "模型测试", Quota: 900, PromptTokens: 90, CompletionTokens: 9, RequestId: "legacy-test"},
				{Type: model.LogTypeConsume, TokenId: 7, TokenName: "模型测试", Content: "模型测试", Quota: 1000, PromptTokens: 10, CompletionTokens: 2, RequestId: "real-named-token"},
				{Type: model.LogTypeConsume, TokenName: "模型测试", Content: "real request", Quota: 2000, PromptTokens: 20, CompletionTokens: 3, RequestId: "real-no-token"},
				{Type: model.LogTypeConsume, TokenName: "playground", Content: "模型测试", Quota: 3000, PromptTokens: 30, CompletionTokens: 4, RequestId: "real-similar-content"},
				{Type: model.LogTypeConsume, Quota: 4000, PromptTokens: 40, CompletionTokens: 5, RequestId: "real-null-metadata"},
				{Type: model.LogTypeRefund, TokenName: "模型测试", Content: "模型测试", Quota: 100, RequestId: "real-refund"},
				{Type: model.LogTypeError, TokenName: "模型测试", Content: "模型测试", RequestId: "real-error"},
			}
			for i := range logs {
				logs[i].UserId, logs[i].Username = user.Id, user.Username
				logs[i].ChannelId, logs[i].Group, logs[i].ModelName = channel.Id, user.Group, "test-log-model"
				logs[i].CreatedAt = now - 1
			}
			require.NoError(t, logDB.Create(&logs).Error)
			require.NoError(t, logDB.Model(&model.Log{}).Where("request_id = ?", "real-null-metadata").Updates(map[string]any{"token_id": nil, "token_name": nil, "content": nil}).Error)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test", nil)
			ctx.Set("username", user.Username)
			ctx.Set(common.RequestIdKey, "new-test")
			// The server type remains authoritative even if labels or token
			// metadata differ from the historical probe signature.
			model.RecordConsumeLog(ctx, user.Id, model.RecordConsumeLogParams{
				ChannelId: channel.Id, ModelName: "test-log-model", Group: user.Group,
				TokenId: 7, TokenName: "renamed-test", Content: "probe", IsChannelTest: true,
				Quota: 500, PromptTokens: 50, CompletionTokens: 5,
			})
			var newLog model.Log
			require.NoError(t, logDB.Where("request_id = ?", "new-test").First(&newLog).Error)
			assert.Equal(t, model.LogTypeTestConsume, newLog.Type)
			for range 2 {
				require.NoError(t, logDB.AutoMigrate(&model.Log{}))
			}

			query := fmt.Sprintf("?start_timestamp=%d&model_name=test-log-model&group=default&channel=%d", now-60, channel.Id)
			for _, test := range []struct {
				name    string
				handler gin.HandlerFunc
				params  string
				total   int
				count   int
				ids     []string
			}{
				{name: "admin_usage", handler: GetAllLogs, total: 6, count: 6},
				{name: "admin_usage_consume", handler: GetAllLogs, params: "&type=2", total: 4, count: 4},
				{name: "admin_test", handler: GetAllLogs, params: "&source=test", total: 2, count: 2, ids: []string{"new-test", "legacy-test"}},
				{name: "test_consume_filter", handler: GetAllLogs, params: "&source=test&type=2", total: 2, count: 2, ids: []string{"new-test", "legacy-test"}},
				{name: "test_type_filter", handler: GetAllLogs, params: "&source=test&type=8", total: 2, count: 2, ids: []string{"new-test", "legacy-test"}},
				{name: "test_page_two", handler: GetAllLogs, params: "&source=test&page_size=1&p=2", total: 2, count: 1, ids: []string{"legacy-test"}},
				{name: "test_request_filter", handler: GetAllLogs, params: "&source=test&request_id=new-test", total: 1, count: 1, ids: []string{"new-test"}},
				{name: "self_cannot_select_tests", handler: GetUserLogs, params: "&source=test", total: 6, count: 6},
				{name: "self_cannot_select_test_type", handler: GetUserLogs, params: "&source=test&type=8", total: 0, count: 0},
			} {
				t.Run(test.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/"+query+test.params, nil)
					ctx.Set("id", user.Id)
					ctx.Set("username", user.Username)
					ctx.Set("role", common.RoleAdminUser)
					test.handler(ctx)
					var response struct {
						Success bool `json:"success"`
						Data    struct {
							Total int         `json:"total"`
							Items []model.Log `json:"items"`
						} `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
					require.True(t, response.Success, recorder.Body.String())
					assert.Equal(t, test.total, response.Data.Total)
					require.Len(t, response.Data.Items, test.count)
					var ids []string
					for _, row := range response.Data.Items {
						ids = append(ids, row.RequestId)
					}
					if test.ids != nil {
						assert.Equal(t, test.ids, ids)
					} else {
						assert.NotContains(t, ids, "new-test")
						assert.NotContains(t, ids, "legacy-test")
					}
				})
			}
			for _, test := range []struct {
				name    string
				handler gin.HandlerFunc
				params  string
				stat    model.Stat
			}{
				{name: "usage_stat", handler: GetLogsStat, stat: model.Stat{Quota: 10000, Rpm: 4, Tpm: 114}},
				{name: "test_stat", handler: GetLogsStat, params: "&source=test", stat: model.Stat{Quota: 1400, Rpm: 2, Tpm: 154}},
				{name: "self_stat_cannot_select_test", handler: GetLogsSelfStat, params: "&source=test", stat: model.Stat{Quota: 10000, Rpm: 4, Tpm: 114}},
			} {
				t.Run(test.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Request = httptest.NewRequest(http.MethodGet, "/api/log/stat"+query+test.params, nil)
					ctx.Set("username", user.Username)
					test.handler(ctx)
					var response struct {
						Success bool       `json:"success"`
						Data    model.Stat `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
					require.True(t, response.Success, recorder.Body.String())
					assert.Equal(t, test.stat, response.Data)
				})
			}
			for _, handler := range []gin.HandlerFunc{GetAllLogs, GetLogsStat} {
				recorder := modelManagementRequest(t, handler, http.MethodGet, "/api/log/?source=all", nil, nil)
				assert.Equal(t, http.StatusBadRequest, recorder.Code)
			}
			byToken, err := model.GetLogByTokenId(7)
			require.NoError(t, err)
			require.Len(t, byToken, 1)
			assert.Equal(t, "real-named-token", byToken[0].RequestId)
			assert.Equal(t, 114, model.SumUsedToken(model.LogTypeConsume, now-60, 0, "test-log-model", user.Username, ""))
			usage, err := model.GetChannelRecordedUsage(channel.Id, now-60, now+60)
			require.NoError(t, err)
			assert.EqualValues(t, 5, usage.BillingRecordCount)
			assert.EqualValues(t, 9900, usage.RecordedUsedQuota)
			var total int64
			require.NoError(t, logDB.Model(&model.Log{}).Count(&total).Error)
			assert.EqualValues(t, 8, total, "separation must preserve historical and new records")
		})
	}
}

func TestGetChannelDefaultBaseURLsUsesBuiltInDefaults(t *testing.T) {
	originalBaseURLs := constant.ChannelBaseURLs
	constant.ChannelBaseURLs = append([]string(nil), originalBaseURLs...)
	constant.ChannelBaseURLs[constant.ChannelTypeDeepSeek] = "https://deepseek.server.example"
	t.Cleanup(func() {
		constant.ChannelBaseURLs = originalBaseURLs
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/channel/default_base_urls", nil)
	GetChannelDefaultBaseURLs(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    map[int]string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, "https://deepseek.server.example", response.Data[constant.ChannelTypeDeepSeek])
	assert.Equal(t, "https://api.openai.com", response.Data[constant.ChannelTypeOpenAI])
	assert.NotContains(t, response.Data, constant.ChannelTypeAzure)
	assert.NotContains(t, response.Data, constant.ChannelTypeNewAPI)
	assert.NotContains(t, response.Data, constant.ChannelTypeTaskPlugin)
}

func TestValidateChannelProxy(t *testing.T) {
	tests := []struct {
		name    string
		proxy   string
		wantErr bool
	}{
		{name: "empty"},
		{name: "http", proxy: "http://proxy.example:8080"},
		{name: "https", proxy: "https://proxy.example:8443"},
		{name: "socks5", proxy: "socks5://proxy.example"},
		{name: "socks5h", proxy: "socks5h://proxy.example:1080/"},
		{name: "unsupported", proxy: "ftp://proxy.example", wantErr: true},
		{name: "path", proxy: "socks5://proxy.example:1080/path", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setting, err := common.Marshal(dto.ChannelSettings{Proxy: test.proxy})
			require.NoError(t, err)
			channel := &model.Channel{
				Type:    constant.ChannelTypeOpenAI,
				Setting: common.GetPointer(string(setting)),
			}

			err = validateChannel(channel, false)

			if test.wantErr {
				require.ErrorContains(t, err, "invalid channel proxy")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateChannelRequiresNewAPIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL *string
		wantErr bool
	}{
		{name: "missing", wantErr: true},
		{name: "blank", baseURL: common.GetPointer("  "), wantErr: true},
		{name: "configured", baseURL: common.GetPointer("https://new-api.example")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := &model.Channel{
				Type:    constant.ChannelTypeNewAPI,
				BaseURL: test.baseURL,
			}

			err := validateChannel(channel, false)

			if test.wantErr {
				require.ErrorContains(t, err, "New API channel base URL cannot be empty")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNewAPIChannelRegistration(t *testing.T) {
	apiType, ok := common.ChannelType2APIType(constant.ChannelTypeNewAPI)

	require.True(t, ok)
	assert.Equal(t, constant.APITypeNewAPI, apiType)
	assert.Equal(t, "New API", constant.GetChannelTypeName(constant.ChannelTypeNewAPI))
	require.Greater(t, len(constant.ChannelBaseURLs), constant.ChannelTypeNewAPI)
	assert.Empty(t, constant.ChannelBaseURLs[constant.ChannelTypeNewAPI])
}

func TestResponsesCompactChannelSupport(t *testing.T) {
	tests := []struct {
		name        string
		channelType int
		apiType     int
		want        bool
	}{
		{name: "OpenAI", channelType: constant.ChannelTypeOpenAI, apiType: constant.APITypeOpenAI, want: true},
		{name: "Azure", channelType: constant.ChannelTypeAzure, apiType: constant.APITypeOpenAI, want: true},
		{name: "Codex", channelType: constant.ChannelTypeCodex, apiType: constant.APITypeCodex, want: true},
		{name: "Advanced Custom", channelType: constant.ChannelTypeAdvancedCustom, apiType: constant.APITypeAdvancedCustom, want: true},
		{name: "Sub2API", channelType: constant.ChannelTypeSub2API, apiType: constant.APITypeSub2API, want: true},
		{name: "New API", channelType: constant.ChannelTypeNewAPI, apiType: constant.APITypeNewAPI, want: true},
		{name: "Anthropic", channelType: constant.ChannelTypeAnthropic, apiType: constant.APITypeAnthropic, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, common.SupportsResponsesCompact(test.channelType, test.apiType))
		})
	}
}

func TestMultiprotocolGatewayEndpointTypes(t *testing.T) {
	want := []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeOpenAIResponseCompact,
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeGemini,
		constant.EndpointTypeOpenAIAlphaSearch,
	}

	assert.Equal(t, want, common.GetEndpointTypesByChannelType(constant.ChannelTypeNewAPI, "gpt-5"))
	assert.Equal(t, want, common.GetEndpointTypesByChannelType(constant.ChannelTypeSub2API, "gpt-5"))
}

func TestCopyChannelRejectsInvalidLegacyProxySettings(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	settingBytes, err := common.Marshal(dto.ChannelSettings{
		Proxy: "socks5://proxy.example/legacy-path",
	})
	require.NoError(t, err)
	setting := string(settingBytes)
	origin := &model.Channel{
		Type:    constant.ChannelTypeOpenAI,
		Name:    "legacy proxy channel",
		Key:     "test-key",
		Models:  "gpt-test",
		Group:   "default",
		Setting: &setting,
	}
	require.NoError(t, db.Create(origin).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", origin.Id)}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/copy", nil)

	CopyChannel(ctx)

	assert.Contains(t, recorder.Body.String(), "invalid channel settings")
	var channelCount int64
	require.NoError(t, db.Model(&model.Channel{}).Count(&channelCount).Error)
	assert.Equal(t, int64(1), channelCount)
}

func TestDeleteChannelResetsProxyCacheWhenPreReadFails(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	service.ResetProxyClientCache()
	t.Cleanup(service.ResetProxyClientCache)

	proxyURL := "http://proxy.example:8080"
	beforeDelete, err := service.GetHttpClientWithProxy(proxyURL)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "999999"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/channel/999999", nil)

	DeleteChannel(ctx)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	afterDelete, err := service.GetHttpClientWithProxy(proxyURL)
	require.NoError(t, err)
	assert.NotSame(t, beforeDelete, afterDelete)
}

func TestDeleteChannelBatchReportsAndAuditsActualDeletedCount(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	channel := &model.Channel{Name: "existing", Key: "test-key"}
	require.NoError(t, db.Create(channel).Error)

	requestBody, err := common.Marshal(ChannelBatch{Ids: []int{channel.Id, 999999}})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/channel/batch", bytes.NewReader(requestBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	DeleteChannelBatch(ctx)

	var response struct {
		Success bool  `json:"success"`
		Data    int64 `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, int64(1), response.Data)

	var auditLog model.AuditLog
	require.NoError(t, db.Order("id desc").First(&auditLog).Error)
	var auditData struct {
		Operation struct {
			Params map[string]any `json:"params"`
		} `json:"op"`
	}
	encodedAudit, err := common.Marshal(auditLog.Other)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(encodedAudit, &auditData))
	assert.Equal(t, float64(1), auditData.Operation.Params["count"])
}

func TestSettleTestQuotaUsesTieredBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:   "tiered_expr",
			ExprString:    `param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`,
			ExprHash:      billingexpr.ExprHashString(`param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`),
			GroupRatio:    1,
			EstimatedTier: "stream",
			QuotaPerUnit:  common.QuotaPerUnit,
			ExprVersion:   1,
		},
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{"stream":true}`),
		},
	}

	quota, result := settleTestQuota(info, types.PriceData{
		ModelRatio:      1,
		CompletionRatio: 2,
	}, &dto.Usage{
		PromptTokens: 1000,
	})

	require.Equal(t, 1500, quota)
	require.NotNil(t, result)
	require.Equal(t, "stream", result.MatchedTier)
}

func TestBuildTestLogOtherInjectsTieredInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode: "tiered_expr",
			ExprString:  `tier("base", p * 2)`,
		},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	priceData := types.PriceData{
		GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 12,
		},
	}

	requestRules := []billingexpr.RequestRuleTrace{{
		Cond:       `param("service_tier") == "fast"`,
		Multiplier: 2,
		Matched:    true,
	}}
	other := buildTestLogOther(ctx, info, priceData, usage, &billingexpr.TieredResult{
		MatchedTier:  "base",
		RequestRules: requestRules,
	})

	fields := other.Snapshot()
	require.Equal(t, "tiered_expr", fields["billing_mode"])
	require.Equal(t, "base", fields["matched_tier"])
	require.Equal(t, requestRules, fields["request_rules"])
	require.NotEmpty(t, fields["expr_b64"])
}

func TestResolveChannelTestUserIDUsesRequestUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("id", 2)

	userID, err := resolveChannelTestUserID(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, userID)
}

func TestSelectChannelsForAutomaticTestSkipsOnlyUnavailableChannels(t *testing.T) {
	autoBanEnabled, autoBanDisabled := 1, 0
	channels := []*model.Channel{
		nil,
		{Id: 1, Status: common.ChannelStatusEnabled, AutoBan: &autoBanEnabled},
		{Id: 2, Status: common.ChannelStatusEnabled, AutoBan: &autoBanDisabled},
		{Id: 3, Status: common.ChannelStatusAutoDisabled, AutoBan: &autoBanEnabled},
		{Id: 4, Status: common.ChannelStatusManuallyDisabled, AutoBan: &autoBanEnabled},
		{Id: 5, Status: common.ChannelStatusEnabled},
	}

	selected := selectChannelsForAutomaticTest(channels)

	assert.Equal(t, []*model.Channel{channels[1], channels[2], channels[3], channels[5]}, selected)
}

func TestRunChannelTestWorkersHonorsConfiguredConcurrency(t *testing.T) {
	originalInterval := common.RequestInterval
	common.RequestInterval = 0
	t.Cleanup(func() { common.RequestInterval = originalInterval })

	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Status: common.ChannelStatusEnabled},
		{Id: 3, Status: common.ChannelStatusEnabled},
		{Id: 4, Status: common.ChannelStatusEnabled},
	}
	started := make(chan struct{}, len(channels))
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	progress := make([]int, 0, len(channels)+1)
	summaryResult := make(chan channelTestSummary, 1)

	go func() {
		summaryResult <- runChannelTestWorkers(
			context.Background(),
			channels,
			2,
			func(_ context.Context, _ *model.Channel) channelTestSummary {
				current := active.Add(1)
				defer active.Add(-1)
				for {
					observed := maxActive.Load()
					if current <= observed || maxActive.CompareAndSwap(observed, current) {
						break
					}
				}
				started <- struct{}{}
				<-release
				return channelTestSummary{Tested: 1, Succeeded: 1}
			},
			func(processed, _ int) {
				progress = append(progress, processed)
			},
		)
	}()

	<-started
	<-started
	select {
	case <-started:
		t.Fatal("started more channel tests than the configured concurrency")
	default:
	}
	close(release)

	summary := <-summaryResult

	assert.Equal(t, int32(2), maxActive.Load())
	assert.Equal(t, channelTestSummary{Tested: 4, Succeeded: 4}, summary)
	assert.Equal(t, []int{0, 1, 2, 3, 4}, progress)
}

func TestRunChannelTestWorkersStopsAfterCancellation(t *testing.T) {
	originalInterval := common.RequestInterval
	common.RequestInterval = 0
	t.Cleanup(func() { common.RequestInterval = originalInterval })

	ctx, cancel := context.WithCancel(context.Background())
	channels := []*model.Channel{
		{Id: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Status: common.ChannelStatusEnabled},
		{Id: 3, Status: common.ChannelStatusEnabled},
		{Id: 4, Status: common.ChannelStatusEnabled},
	}
	started := make(chan struct{}, len(channels))
	progress := make([]int, 0, 1)
	summaryResult := make(chan channelTestSummary, 1)

	go func() {
		summaryResult <- runChannelTestWorkers(
			ctx,
			channels,
			2,
			func(ctx context.Context, _ *model.Channel) channelTestSummary {
				started <- struct{}{}
				<-ctx.Done()
				return channelTestSummary{Tested: 1, Succeeded: 1}
			},
			func(processed, _ int) {
				progress = append(progress, processed)
			},
		)
	}()

	<-started
	<-started
	cancel()

	summary := <-summaryResult

	select {
	case <-started:
		t.Fatal("started another channel test after cancellation")
	default:
	}
	assert.Equal(t, channelTestSummary{Tested: 2, Succeeded: 2}, summary)
	assert.Equal(t, []int{0}, progress)
}

func TestTestAllChannelsRejectsExistingActiveTask(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))

	existing, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test", nil)

	TestAllChannels(ctx)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), existing.TaskID)
	require.Contains(t, recorder.Body.String(), "已有通道测试任务正在运行或等待中")
}

func TestChannelTestPublishesModelMonitoring(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	previousTimeout := constant.StreamingTimeout
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	constant.StreamingTimeout = 10
	withSelfUseModeDisabled(t)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"monitor-vip":1}`))
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport
		constant.StreamingTimeout = previousTimeout
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
			"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled),
		}))
	})

	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}, &model.Log{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			user := &model.User{Username: "monitor-test-user", Password: "unused", Group: "monitor-vip", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "monitor"}
			require.NoError(t, db.Create(user).Error)

			for _, test := range []struct {
				name       string
				stream     bool
				disabled   bool
				beforeSend bool
				wantError  bool
			}{
				{name: "nonstream"},
				{name: "stream", stream: true},
				{name: "http_error", wantError: true},
				{name: "invalid_body", wantError: true},
				{name: "network_error", wantError: true},
				{name: "invalid_mapping", beforeSend: true, wantError: true},
				{name: "unsupported_conversion", beforeSend: true, wantError: true},
				{name: "disabled", disabled: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					// Unique model names isolate process-wide hot buckets, including -count runs.
					modelName := fmt.Sprintf("monitor-%s-%s-%d", dialect.kind, test.name, time.Now().UnixNano())
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
						"perf_metrics_setting.enabled": strconv.FormatBool(!test.disabled),
					}))
					prices, err := common.Marshal(map[string]float64{modelName: 1})
					require.NoError(t, err)
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(prices)))
					var upstreamRequests atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						upstreamRequests.Add(1)
						var request dto.GeneralOpenAIRequest
						if !assert.NoError(t, common.DecodeJson(r.Body, &request)) {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						assert.Equal(t, "gpt-4o-mini", request.Model)
						assert.Equal(t, test.stream, request.Stream != nil && *request.Stream)
						w.Header().Set("Content-Type", "application/json")
						switch test.name {
						case "http_error":
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = fmt.Fprint(w, `{"error":{"message":"upstream unavailable","type":"server_error"}}`)
						case "invalid_body":
							_, _ = fmt.Fprint(w, `{"invalid_json":`)
						case "stream":
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"model\":\"gpt-4o-mini\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"}}]}\n\n")
							w.(http.Flusher).Flush()
							_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"model\":\"gpt-4o-mini\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\ndata: [DONE]\n\n")
						default:
							_, _ = fmt.Fprint(w, `{"id":"chatcmpl-test","model":"gpt-4o-mini","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
						}
					}))
					t.Cleanup(upstream.Close)
					mapping, err := common.Marshal(map[string]string{modelName: "gpt-4o-mini"})
					require.NoError(t, err)
					channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "monitor fixture", Key: "local-test-key", Models: modelName, Group: user.Group, BaseURL: common.GetPointer(upstream.URL), ModelMapping: common.GetPointer(string(mapping))}
					endpoint := string(constant.EndpointTypeOpenAI)
					switch test.name {
					case "network_error":
						upstream.Close()
					case "invalid_mapping":
						channel.ModelMapping = common.GetPointer(`{"broken":`)
					case "unsupported_conversion":
						channel.Type = constant.ChannelTypeAnthropic
						endpoint = string(constant.EndpointTypeEmbeddings)
					}
					require.NoError(t, db.Create(channel).Error)

					result := testChannel(context.Background(), channel, user.Id, modelName, endpoint, test.stream)
					if test.wantError {
						require.Error(t, result.localErr)
					} else {
						require.NoError(t, result.localErr)
						require.Nil(t, result.newAPIError)
						var log model.Log
						require.NoError(t, db.Where("model_name = ?", modelName).First(&log).Error)
						assert.Equal(t, user.Group, log.Group)
						assert.Equal(t, model.LogTypeTestConsume, log.Type, "channel probes must be stored separately from real usage")
						assert.Equal(t, test.stream, log.IsStream)
						assert.Equal(t, 2, log.CompletionTokens)
					}
					if test.beforeSend || test.name == "network_error" {
						assert.Zero(t, upstreamRequests.Load())
					} else {
						assert.Equal(t, int32(1), upstreamRequests.Load())
					}

					metrics, err := perfmetrics.Query(perfmetrics.QueryParams{Model: modelName, Group: user.Group, Hours: 24})
					require.NoError(t, err)
					var detail struct {
						Success bool                    `json:"success"`
						Data    perfmetrics.QueryResult `json:"data"`
					}
					recorder := modelManagementRequest(t, GetPerfMetrics, http.MethodGet, "/api/perf_metrics?model="+url.QueryEscape(modelName)+"&group="+url.QueryEscape(user.Group), nil, &detail)
					require.Equal(t, http.StatusOK, recorder.Code)
					require.True(t, detail.Success)
					assert.Equal(t, metrics, detail.Data)
					var summary struct {
						Success bool                         `json:"success"`
						Data    perfmetrics.SummaryAllResult `json:"data"`
					}
					recorder = modelManagementRequest(t, GetPerfMetricsSummary, http.MethodGet, "/api/perf_metrics/summary", nil, &summary)
					require.Equal(t, http.StatusOK, recorder.Code)
					require.True(t, summary.Success)
					summaries := map[string]perfmetrics.ModelSummary{}
					for _, item := range summary.Data.Models {
						summaries[item.ModelName] = item
					}
					counts, err := perfmetrics.QuerySummaryAll(24, []string{user.Group})
					require.NoError(t, err)
					var requestCount int64
					for _, item := range counts.Models {
						if item.ModelName == modelName {
							requestCount = item.RequestCount
						}
					}
					if test.beforeSend || test.disabled {
						assert.Zero(t, requestCount)
						assert.Empty(t, metrics.Groups)
						if test.disabled {
							assert.Nil(t, summaries[modelName].AvailabilityRate)
							assert.Equal(t, common.GetPointer(true), summaries[modelName].CurrentAvailable)
						} else {
							assert.NotContains(t, summaries, modelName)
						}
						return
					}
					assert.EqualValues(t, 1, requestCount, "each dispatched test contributes exactly one sample")
					require.Len(t, metrics.Groups, 1, "a dispatched model test must be visible in model-square monitoring")
					group := metrics.Groups[0]
					assert.Equal(t, user.Group, group.Group)
					require.Len(t, group.Series, 1)
					wantSuccess := float64(100)
					if test.wantError {
						wantSuccess = 0
					}
					assert.Equal(t, wantSuccess, group.SuccessRate)
					assert.Equal(t, wantSuccess, group.Series[0].SuccessRate)
					require.Contains(t, summaries, modelName)
					assert.Equal(t, wantSuccess, summaries[modelName].SuccessRate)
					if !test.stream {
						assert.Zero(t, group.AvgTtftMs)
					}
					if test.wantError {
						assert.Zero(t, group.AvgTps)
					}
					wrongGroup, err := perfmetrics.Query(perfmetrics.QueryParams{Model: modelName, Group: "default", Hours: 24})
					require.NoError(t, err)
					assert.Empty(t, wrongGroup.Groups, "custom-group tests must not leak into the default group")
					upstreamModel, err := perfmetrics.Query(perfmetrics.QueryParams{Model: "gpt-4o-mini", Group: user.Group, Hours: 24})
					require.NoError(t, err)
					assert.Empty(t, upstreamModel.Groups, "monitoring must retain the model-square alias")
				})
			}
		})
	}
}

func TestChannelMultiKeyProbeUsesAvailableKey(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	previousInterval := common.RequestInterval
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	common.RequestInterval = 0
	withSelfUseModeDisabled(t)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	t.Cleanup(func() {
		common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport
		common.RequestInterval = previousInterval
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}, &model.Log{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			user := &model.User{Username: "multi-key-monitor-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "multikeymonitor"}
			require.NoError(t, db.Create(user).Error)
			for _, test := range []struct {
				name        string
				healthyKey  int
				statuses    map[int]int
				neutralKeys map[int]bool
				setupError  bool
				cancel      bool
				completed   bool
				wantKeys    []int
			}{
				{name: "insufficient_balance_then_success", healthyKey: 1, completed: true, wantKeys: []int{0, 1, 2}},
				{name: "all_keys_fail", healthyKey: -1, completed: true, wantKeys: []int{0, 1, 2}},
				{name: "skip_disabled_key", healthyKey: 2, statuses: map[int]int{0: common.ChannelStatusAutoDisabled}, completed: true, wantKeys: []int{1, 2}},
				{name: "no_enabled_key", healthyKey: 1, statuses: map[int]int{0: common.ChannelStatusAutoDisabled, 1: common.ChannelStatusManuallyDisabled, 2: common.ChannelStatusAutoDisabled}, wantKeys: []int{}},
				{name: "setup_error", healthyKey: 1, setupError: true, wantKeys: []int{}},
				{name: "cancelled_after_first_key", healthyKey: 1, cancel: true, wantKeys: []int{0}},
				{name: "success_with_neutral_keys", healthyKey: 1, neutralKeys: map[int]bool{0: true, 2: true}, completed: true, wantKeys: []int{0, 1, 2}},
				{name: "failure_with_neutral_key", healthyKey: -1, neutralKeys: map[int]bool{1: true}, wantKeys: []int{0, 1, 2}},
				{name: "all_keys_neutral", healthyKey: -1, neutralKeys: map[int]bool{0: true, 1: true, 2: true}, wantKeys: []int{0, 1, 2}},
			} {
				t.Run(test.name, func(t *testing.T) {
					name := fmt.Sprintf("key-probe-%s-%s-%d", dialect.kind, test.name, time.Now().UnixNano())
					prices, err := common.Marshal(map[string]float64{name: 1})
					require.NoError(t, err)
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(prices)))
					ctx, cancel := context.WithCancel(context.Background())
					t.Cleanup(cancel)
					requests := make(chan int, 3)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						index := map[string]int{"Bearer fixture-key-0": 0, "Bearer fixture-key-1": 1, "Bearer fixture-key-2": 2}[r.Header.Get("Authorization")]
						select {
						case requests <- index:
						default:
							t.Error("a key was retried more than once")
						}
						if test.cancel {
							cancel()
						}
						w.Header().Set("Content-Type", "application/json")
						if test.neutralKeys[index] {
							w.WriteHeader(http.StatusBadRequest)
							if index == 1 {
								_, _ = fmt.Fprint(w, `{"error":{"message":"根据相关法律法规，有关信息不予显示。","type":"invalid_request_error"}}`)
							} else {
								_, _ = fmt.Fprint(w, `{"error":{"message":"content was rejected","type":"content_filter_error","code":"content_filter"}}`)
							}
							return
						}
						if index != test.healthyKey {
							w.WriteHeader(http.StatusPaymentRequired)
							_, _ = fmt.Fprint(w, `{"error":{"message":"insufficient balance","type":"insufficient_quota"}}`)
							return
						}
						_, _ = fmt.Fprint(w, `{"id":"test","model":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
					}))
					t.Cleanup(upstream.Close)
					channel := &model.Channel{
						Type: constant.ChannelTypeOpenAI, Key: "fixture-key-0\nfixture-key-1\nfixture-key-2", Status: common.ChannelStatusEnabled,
						Models: name, Group: user.Group, BaseURL: common.GetPointer(upstream.URL), TestTime: 100,
						ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyPollingIndex: 2, MultiKeyStatusList: test.statuses},
					}
					if test.setupError {
						channel.ModelMapping = common.GetPointer(`{"broken":`)
					}
					require.NoError(t, db.Create(channel).Error)
					previousObservedAt := time.Now().Add(-time.Minute).UnixMilli()
					for index := range test.neutralKeys {
						key := fmt.Sprintf("fixture-key-%d", index)
						require.NoError(t, model.RecordChannelKeyObservation(channel, name, key, previousObservedAt, common.GetPointer(true), http.StatusOK, "", perfmetrics.SourceProbe))
					}
					originalInfo, err := common.Marshal(channel.ChannelInfo)
					require.NoError(t, err)

					summary := performChannelTests(ctx, []*model.Channel{channel}, user.Id, 1, nil)
					require.NoError(t, perfmetrics.Flush())
					actualKeys := []int{}
					for len(requests) > 0 {
						actualKeys = append(actualKeys, <-requests)
					}
					assert.Equal(t, test.wantKeys, actualKeys)
					for index := range test.neutralKeys {
						id, err := model.ChannelKeyObservationID(channel, name, fmt.Sprintf("fixture-key-%d", index))
						require.NoError(t, err)
						var observation model.ChannelKeyObservation
						require.NoError(t, db.First(&observation, "id = ?", id).Error)
						assert.True(t, observation.Success, "a policy rejection cannot replace a key's previous health result")
						assert.Equal(t, previousObservedAt, observation.ObservedAt)
						assert.Equal(t, http.StatusOK, observation.StatusCode)
						assert.Empty(t, observation.ErrorText)
					}
					var stored model.Channel
					require.NoError(t, db.First(&stored, channel.Id).Error)
					for _, info := range []model.ChannelInfo{channel.ChannelInfo, stored.ChannelInfo} {
						encoded, err := common.Marshal(info)
						require.NoError(t, err)
						assert.JSONEq(t, string(originalInfo), string(encoded), "monitoring must not mutate key states or polling cursor in memory or SQL")
					}
					assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
					assert.Equal(t, channel.Key, stored.Key)
					var legacy []model.PerfMetric
					require.NoError(t, db.Where("model_name = ?", name).Find(&legacy).Error)
					var observations []model.ChannelPerfMetric
					require.NoError(t, db.Where("model_name = ?", name).Find(&observations).Error)
					if !test.completed {
						assert.Empty(t, legacy, "cancelled and locally incomplete checks have no completed observation")
						assert.Empty(t, observations)
						if len(test.neutralKeys) > 0 {
							activity, err := model.GetChannelModelActivity(channel.Id, name)
							require.NoError(t, err)
							assert.Zero(t, activity.LastResultAt, "failed and neutral keys leave the aggregate health unknown")
						}
						if test.cancel {
							assert.Zero(t, summary.Tested)
							assert.EqualValues(t, 100, stored.TestTime)
						}
						return
					}
					wantSuccess := int64(0)
					if test.healthyKey >= 0 {
						wantSuccess = 1
					}
					assert.Equal(t, channelTestSummary{Tested: 1, Succeeded: int(wantSuccess), Failed: 1 - int(wantSuccess)}, summary)
					require.Len(t, legacy, 1)
					assert.EqualValues(t, 1, legacy[0].RequestCount)
					assert.Equal(t, wantSuccess, legacy[0].SuccessCount)
					require.Len(t, observations, 4, "one physical probe, one channel observation, one group round and one model-wide round")
					for _, observation := range observations {
						assert.Contains(t, []string{perfmetrics.SourceProbe, perfmetrics.SourceRouteState, perfmetrics.SourceAvailability, perfmetrics.SourceAvailabilityAll}, observation.Source)
						assert.EqualValues(t, 1, observation.RequestCount, "failed key attempts cannot dilute a recovered channel's availability")
						assert.Equal(t, wantSuccess, observation.SuccessCount)
					}
					if wantSuccess > 0 {
						var log model.Log
						require.NoError(t, db.Where("model_name = ?", name).First(&log).Error)
						var other struct {
							AdminInfo struct {
								IsMultiKey bool `json:"is_multi_key"`
								KeyIndex   int  `json:"multi_key_index"`
							} `json:"admin_info"`
						}
						require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
						assert.True(t, other.AdminInfo.IsMultiKey)
						assert.Equal(t, test.healthyKey, other.AdminInfo.KeyIndex, "successful test logs must retain the original key index")
					}
				})
			}
		})
	}
}

func TestChannelHealthChecksAllConfiguredModelsMonitoringOnly(t *testing.T) {
	previousInterval := common.RequestInterval
	previousDisable, previousEnable := common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled
	previousPerf := perf_metrics_setting.GetSetting()
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.RequestInterval = 0
	common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = true, true
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	withSelfUseModeDisabled(t)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	t.Cleanup(func() {
		common.RequestInterval = previousInterval
		common.AutomaticDisableChannelEnabled, common.AutomaticEnableChannelEnabled = previousDisable, previousEnable
		common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})

	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}, &model.Log{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			user := &model.User{Username: "all-model-test-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "allmodel"}
			require.NoError(t, db.Create(user).Error)
			models := make([]string, 5)
			prices := map[string]float64{}
			for index, name := range []string{"first", "failed", "last", "recovered", "other"} {
				models[index] = fmt.Sprintf("health-%s-%s-%d", dialect.kind, name, time.Now().UnixNano())
				prices[models[index]] = 1
			}
			encodedPrices, err := common.Marshal(prices)
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encodedPrices)))
			var requestsMu sync.Mutex
			requested := make(map[string][]string)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request dto.GeneralOpenAIRequest
				if !assert.NoError(t, common.DecodeJson(r.Body, &request)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requestsMu.Lock()
				key := r.Header.Get("Authorization")
				requested[key] = append(requested[key], request.Model)
				requestsMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if request.Model == models[1] {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = fmt.Fprint(w, `{"error":{"message":"bad key","type":"invalid_api_key"}}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"id":"chatcmpl-test","model":"health","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
			}))
			t.Cleanup(upstream.Close)
			autoBan := 1
			channels := []*model.Channel{
				{Type: constant.ChannelTypeOpenAI, Name: "enabled multi key", Key: "live-key\nblocked-key", Status: common.ChannelStatusEnabled, AutoBan: &autoBan, Models: " " + strings.Join(models[:3], ",") + ", ," + models[0] + ",", TestModel: common.GetPointer(models[0]), Group: user.Group, BaseURL: common.GetPointer(upstream.URL), TestTime: 100,
					ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModeRandom, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusAutoDisabled}, MultiKeyDisabledReason: map[int]string{1: "previous failure"}, MultiKeyDisabledTime: map[int]int64{1: 100}}},
				{Type: constant.ChannelTypeOpenAI, Name: "auto disabled", Key: "recovery-key", Status: common.ChannelStatusAutoDisabled, AutoBan: &autoBan, Models: strings.Join(models[3:], ","), TestModel: common.GetPointer(models[3]), Group: user.Group, BaseURL: common.GetPointer(upstream.URL), TestTime: 100},
			}
			for _, channel := range channels {
				require.NoError(t, db.Create(channel).Error)
			}

			t.Run("round_tests_every_model_once_and_preserves_status", func(t *testing.T) {
				summary := performChannelTests(context.Background(), channels, user.Id, 2, nil)

				assert.Equal(t, channelTestSummary{Tested: 5, Succeeded: 4, Failed: 1}, summary)
				requestsMu.Lock()
				assert.Equal(t, map[string][]string{"Bearer live-key": models[:3], "Bearer recovery-key": models[3:]}, requested, "configured order, trimming, deduplication, and continuation after a failed model")
				requestsMu.Unlock()
				for _, channel := range channels {
					var stored model.Channel
					require.NoError(t, db.First(&stored, channel.Id).Error)
					assert.Equal(t, channel.Status, stored.Status, "monitoring must never enable or disable a channel")
					assert.Equal(t, channel.ChannelInfo, stored.ChannelInfo, "monitoring must preserve key status and disable metadata")
					assert.Greater(t, stored.TestTime, int64(100), "a completed round updates monitoring time")
				}
				counts, err := perfmetrics.QuerySummaryAll(24, []string{user.Group})
				require.NoError(t, err)
				modelCounts := map[string]int64{}
				for _, item := range counts.Models {
					modelCounts[item.ModelName] = item.RequestCount
				}
				for index, name := range models {
					assert.EqualValues(t, 1, modelCounts[name], "each model contributes exactly one monitoring sample")
					metrics, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: user.Group, Hours: 24})
					require.NoError(t, err)
					require.Len(t, metrics.Groups, 1)
					wantSuccess := float64(100)
					if index == 1 {
						wantSuccess = 0
					}
					assert.Equal(t, wantSuccess, metrics.Groups[0].SuccessRate)
				}
			})

			t.Run("explicit_model_empty_configuration_and_local_failures", func(t *testing.T) {
				result := testChannel(context.Background(), channels[0], user.Id, " "+models[2]+" ", "", false)
				require.NoError(t, result.localErr)
				assert.Equal(t, channelTestSummary{Tested: 1, Succeeded: 1}, result.summary)
				requestsMu.Lock()
				assert.Equal(t, []string{models[0], models[1], models[2], models[2]}, requested["Bearer live-key"])
				requestsMu.Unlock()

				empty := *channels[0]
				empty.Models = " , , "
				result = testChannel(context.Background(), &empty, user.Id, "", "", false)
				require.Error(t, result.localErr, "an empty model list must not fall back to TestModel or a default model")
				assert.Zero(t, result.summary.Tested)

				unsupported := *channels[0]
				unsupported.Type = constant.ChannelTypeSunoAPI
				unsupported.Models = strings.Join(models[:2], ",")
				result = testChannel(context.Background(), &unsupported, user.Id, "", "", false)
				require.Error(t, result.localErr)
				assert.Nil(t, result.newAPIError, "this fixture exercises a local-error-only failure")
				assert.Equal(t, channelTestSummary{Tested: 2, Failed: 2}, result.summary)
				assert.Equal(t, result.summary, testChannelForHealthCheck(context.Background(), &unsupported, user.Id))
				requestsMu.Lock()
				assert.Len(t, requested["Bearer live-key"], 4, "empty and unsupported configurations must not dispatch requests")
				assert.Len(t, requested["Bearer recovery-key"], 2)
				requestsMu.Unlock()
			})

			t.Run("mixed_chat_embedding_and_rerank_models", func(t *testing.T) {
				suffix := fmt.Sprintf("%s-%d", dialect.kind, time.Now().UnixNano())
				mixedModels := []string{"health-chat-" + suffix, "nomic-embed-text-" + suffix, "bge-reranker-" + suffix}
				for _, name := range mixedModels {
					prices[name] = 1
				}
				encoded, err := common.Marshal(prices)
				require.NoError(t, err)
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
				requests := make(chan string, len(mixedModels))
				mixedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request map[string]any
					if !assert.NoError(t, common.DecodeJson(r.Body, &request)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					name, _ := request["model"].(string)
					select {
					case requests <- name:
					default:
						t.Errorf("unexpected extra model request %q", name)
					}
					w.Header().Set("Content-Type", "application/json")
					switch name {
					case mixedModels[0]:
						assert.Equal(t, "/v1/chat/completions", r.URL.Path)
						assert.NotEmpty(t, request["messages"])
						_, _ = fmt.Fprint(w, `{"id":"chatcmpl-test","model":"health","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
					case mixedModels[1]:
						assert.Equal(t, "/v1/embeddings", r.URL.Path)
						assert.Equal(t, []any{"hello world"}, request["input"])
						assert.NotContains(t, request, "messages")
						_, _ = fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"embedding","usage":{"prompt_tokens":5,"total_tokens":5}}`)
					case mixedModels[2]:
						assert.Equal(t, "/v1/rerank", r.URL.Path)
						assert.NotEmpty(t, request["query"])
						assert.Len(t, request["documents"], 2)
						assert.NotContains(t, request, "input")
						_, _ = fmt.Fprint(w, `{"results":[{"index":0,"relevance_score":0.9}],"usage":{"total_tokens":5}}`)
					default:
						t.Errorf("unexpected model %q", name)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				t.Cleanup(mixedUpstream.Close)
				channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "mixed model endpoints", Key: "mixed-key", Status: common.ChannelStatusEnabled, Models: strings.Join(mixedModels, ","), Group: user.Group, BaseURL: common.GetPointer(mixedUpstream.URL)}
				require.NoError(t, db.Create(channel).Error)

				result := testChannel(context.Background(), channel, user.Id, "", "", false)

				require.NoError(t, result.localErr)
				assert.Equal(t, channelTestSummary{Tested: 3, Succeeded: 3}, result.summary)
				require.Len(t, requests, 3, "every model uses its own endpoint and request body")
				for _, name := range mixedModels {
					assert.Equal(t, name, <-requests)
				}
				counts, err := perfmetrics.QuerySummaryAll(24, []string{user.Group})
				require.NoError(t, err)
				modelCounts := map[string]int64{}
				for _, item := range counts.Models {
					modelCounts[item.ModelName] = item.RequestCount
				}
				for _, name := range mixedModels {
					assert.EqualValues(t, 1, modelCounts[name])
				}
			})

			t.Run("cancellation_stops_remaining_models", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				var calls atomic.Int32
				cancelUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					cancel()
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				t.Cleanup(cancelUpstream.Close)
				channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "cancel remaining models", Key: "cancel-key", Status: common.ChannelStatusEnabled, Models: strings.Join(models[:3], ","), Group: user.Group, BaseURL: common.GetPointer(cancelUpstream.URL), TestTime: 100}
				require.NoError(t, db.Create(channel).Error)

				_ = testChannelForHealthCheck(ctx, channel, user.Id)

				require.ErrorIs(t, ctx.Err(), context.Canceled)
				assert.Equal(t, int32(1), calls.Load())
				var stored model.Channel
				require.NoError(t, db.First(&stored, channel.Id).Error)
				assert.Equal(t, int64(100), stored.TestTime, "an interrupted round must not update the channel's completed test time")
				assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
			})
		})
	}
}

func TestChannelTestBuildsSeedreamImageRequest(t *testing.T) {
	channel := &model.Channel{Type: constant.ChannelTypeVolcEngine}
	endpoint := normalizeChannelTestEndpoint(channel, "doubao-seedream-4-0", "")
	assert.Equal(t, string(constant.EndpointTypeImageGeneration), endpoint)
	request, ok := buildTestRequest("doubao-seedream-4-0", "", channel, false).(*dto.ImageRequest)
	require.True(t, ok, "automatic endpoint selection must produce an image request before adapting it")
	assert.Equal(t, "doubao-seedream-4-0", request.Model)
	assert.NotEmpty(t, request.Prompt)
	require.NotNil(t, request.N)
	assert.Equal(t, uint(1), *request.N)
}

func TestPerformanceMetricsCurrentBucketPersistence(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}, &model.Log{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			name := "current-bucket-" + dialect.kind
			channel := model.Channel{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: name, Group: "default"}
			require.NoError(t, db.Create(&channel).Error)
			for _, success := range []bool{true, false} {
				sample := perfmetrics.Sample{Model: name, Group: "default", ChannelID: channel.Id, Success: success, LatencyMs: 200, OutputTokens: 10, GenerationMs: 100}
				perfmetrics.Record(sample)
				perfmetrics.RecordChannelSample(sample, perfmetrics.SourceRequest)
			}
			before, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "default", Hours: 24})
			require.NoError(t, err)
			require.Len(t, before.Groups, 1)
			assert.Equal(t, float64(50), before.Groups[0].SuccessRate)
			require.NotNil(t, before.AvailabilityRate)
			assert.Equal(t, float64(50), *before.AvailabilityRate, "legacy observations remain visible before complete check rounds exist")
			var failWrites atomic.Bool
			failWrites.Store(true)
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:perf_write_failure", func(tx *gorm.DB) {
				if failWrites.Load() && (tx.Statement.Table == "perf_metrics" || tx.Statement.Table == "channel_perf_metrics") {
					tx.AddError(errors.New("monitor storage unavailable"))
				}
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove("test:perf_write_failure")) })
			require.ErrorContains(t, perfmetrics.Flush(), "monitor storage unavailable")
			failed, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "default", Hours: 24})
			require.NoError(t, err)
			assert.Equal(t, before, failed, "failed writes must restore every buffered observation")
			var missing int64
			require.NoError(t, db.Model(&model.PerfMetric{}).Where("model_name = ?", name).Count(&missing).Error)
			assert.Zero(t, missing)
			failWrites.Store(false)
			for range 2 {
				require.NoError(t, perfmetrics.Flush())
				var saved []model.PerfMetric
				require.NoError(t, db.Where("model_name = ?", name).Find(&saved).Error)
				require.Len(t, saved, 1, "the current bucket must be persisted before the hour ends")
				assert.EqualValues(t, 2, saved[0].RequestCount)
				assert.EqualValues(t, 1, saved[0].SuccessCount)
				assert.EqualValues(t, 400, saved[0].TotalLatencyMs)
				assert.EqualValues(t, 20, saved[0].OutputTokens)
				assert.EqualValues(t, 200, saved[0].GenerationMs)
				var channelSaved []model.ChannelPerfMetric
				require.NoError(t, db.Where("model_name = ?", name).Find(&channelSaved).Error)
				require.Len(t, channelSaved, 1)
				assert.EqualValues(t, 2, channelSaved[0].RequestCount)
				assert.Equal(t, perfmetrics.SourceRequest, channelSaved[0].Source, "historical fallback must not manufacture completed probe rounds")
				// Successful flushes remove the hot buffers. These responses must
				// now be fully reproducible from SQL, including after a restart.
				after, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "default", Hours: 24})
				require.NoError(t, err)
				assert.Equal(t, before, after)
				summary, err := perfmetrics.QuerySummaryAll(24, []string{"default"})
				require.NoError(t, err)
				found := false
				for _, item := range summary.Models {
					if item.ModelName == name {
						found = true
						assert.EqualValues(t, 2, item.RequestCount, "SQL and memory must not count a flushed sample twice")
						assert.Equal(t, float64(50), item.SuccessRate)
						assert.Equal(t, before.AvailabilityRate, item.AvailabilityRate)
					}
				}
				assert.True(t, found, "persisted history must remain visible in the model summary")
			}
			// A sample recorded after the first handoff is added once to the
			// same persisted bucket, rather than disappearing with its old buffer.
			perfmetrics.Record(perfmetrics.Sample{Model: name, Group: "default", Success: true, LatencyMs: 200})
			require.NoError(t, perfmetrics.Flush())
			var final model.PerfMetric
			require.NoError(t, db.Where("model_name = ?", name).First(&final).Error)
			assert.EqualValues(t, 3, final.RequestCount)
			assert.EqualValues(t, 2, final.SuccessCount)
		})
	}
}

func TestModelAvailabilityMergesHistoricalBuckets(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true", "perf_metrics_setting.bucket_time": "hour"}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
			"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled), "perf_metrics_setting.bucket_time": previousPerf.BucketTime,
		}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			name := "merged-availability-" + dialect.kind
			legacyOnly, roundsOnly, unknown := name+"-legacy", name+"-rounds", name+"-unknown"
			channel := model.Channel{Status: common.ChannelStatusEnabled, Models: strings.Join([]string{name, legacyOnly, roundsOnly, unknown}, ","), Group: "default,monitor-vip"}
			require.NoError(t, db.Create(&channel).Error)
			now := time.Now().Unix()
			hour := now - now%3600
			legacy := []model.PerfMetric{
				{ModelName: name, Group: "default", BucketTs: hour - 3*3600, RequestCount: 10, SuccessCount: 8},
				{ModelName: name, Group: "monitor-vip", BucketTs: hour - 3*3600, RequestCount: 5, SuccessCount: 5},
				{ModelName: name, Group: "default", BucketTs: hour - 2*3600, RequestCount: 100, SuccessCount: 100},
				{ModelName: name, Group: "monitor-vip", BucketTs: hour - 2*3600, RequestCount: 100, SuccessCount: 0},
				{ModelName: name, Group: "default", BucketTs: hour, RequestCount: 20, SuccessCount: 0},
				{ModelName: name, Group: "retired-private", BucketTs: hour - 3*3600, RequestCount: 10000, SuccessCount: 0},
				{ModelName: name, Group: "default", BucketTs: now - 24*3600 - 1, RequestCount: 10000, SuccessCount: 10000},
				{ModelName: legacyOnly, Group: "default", BucketTs: hour - 3600, RequestCount: 10, SuccessCount: 7},
			}
			require.NoError(t, db.Create(&legacy).Error)
			rounds := []model.ChannelPerfMetric{
				{ModelName: name, Source: perfmetrics.SourceAvailabilityAll, BucketTs: hour - 2*3600 + 300, RequestCount: 1, SuccessCount: 1},
				{ModelName: name, Source: perfmetrics.SourceAvailabilityAll, BucketTs: hour - 2*3600 + 600, RequestCount: 1, SuccessCount: 0},
				{ModelName: name, Source: perfmetrics.SourceAvailabilityAll, BucketTs: hour - 3600, RequestCount: 2, SuccessCount: 2},
				{ModelName: name, Source: perfmetrics.SourceAvailabilityAll, BucketTs: now - 24*3600 - 1, RequestCount: 10000, SuccessCount: 10000},
				{ModelName: name, Group: "default", Source: perfmetrics.SourceAvailability, BucketTs: hour - 2*3600, RequestCount: 2, SuccessCount: 0},
				{ModelName: name, Group: "default", Source: perfmetrics.SourceAvailability, BucketTs: hour - 3600, RequestCount: 2, SuccessCount: 2},
				{ModelName: name, Group: "monitor-vip", Source: perfmetrics.SourceAvailability, BucketTs: hour - 2*3600, RequestCount: 2, SuccessCount: 1},
				{ModelName: name, Group: "default", ChannelID: channel.Id, Source: perfmetrics.SourceProbe, BucketTs: hour, RequestCount: 100, SuccessCount: 0},
				{ModelName: name, ChannelID: channel.Id, Source: perfmetrics.SourceProbe, BucketTs: hour, RequestCount: 2, SuccessCount: 1},
				{ModelName: roundsOnly, Source: perfmetrics.SourceAvailabilityAll, BucketTs: hour, RequestCount: 1, SuccessCount: 1},
				{ModelName: unknown, ChannelID: channel.Id, Source: perfmetrics.SourceProbe, BucketTs: hour, RequestCount: 1, SuccessCount: 1},
				{ModelName: unknown, Group: "default", ChannelID: channel.Id, Source: perfmetrics.SourceRequest, BucketTs: hour, RequestCount: 1, SuccessCount: 1},
			}
			require.NoError(t, db.Create(&rounds).Error)
			for _, success := range []bool{true, false} {
				perfmetrics.RecordChannelSample(perfmetrics.Sample{Model: name, Success: success}, perfmetrics.SourceAvailabilityAll)
				perfmetrics.RecordChannelSample(perfmetrics.Sample{Model: name, Group: "default", Success: success}, perfmetrics.SourceAvailability)
			}
			groups := []string{"default", "monitor-vip"}
			wantSeries := []perfmetrics.SuccessRatePoint{
				{Ts: hour - 3*3600, SuccessRate: 86.67}, {Ts: hour - 2*3600, SuccessRate: 50},
				{Ts: hour - 3600, SuccessRate: 100}, {Ts: hour, SuccessRate: 50},
			}
			for _, state := range []string{"buffered", "persisted", "repeated-flush"} {
				if state != "buffered" {
					require.NoError(t, perfmetrics.Flush())
				}
				detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24, AllowedGroups: groups})
				require.NoError(t, err)
				require.NotNil(t, detail.AvailabilityRate)
				assert.Equal(t, 80.95, *detail.AvailabilityRate, state+": use 17 successful observations / 21 selected observations, without averaging percentages")
				assert.Equal(t, wantSeries, detail.AvailabilitySeries, state+": complete rounds replace every legacy group within the same displayed hour")
				require.Len(t, detail.Channels, 1)
				require.NotNil(t, detail.Channels[0].AvailabilityRate)
				assert.Equal(t, float64(50), *detail.Channels[0].AvailabilityRate, "physical probes remain separate and ignore legacy group copies")
				filtered, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "default", Hours: 24, AllowedGroups: groups})
				require.NoError(t, err)
				require.NotNil(t, filtered.AvailabilityRate)
				assert.Equal(t, 68.75, *filtered.AvailabilityRate, "group-specific history must use that group's rounds and legacy counts")
				assert.Equal(t, []perfmetrics.SuccessRatePoint{
					{Ts: hour - 3*3600, SuccessRate: 80}, {Ts: hour - 2*3600, SuccessRate: 0},
					{Ts: hour - 3600, SuccessRate: 100}, {Ts: hour, SuccessRate: 50},
				}, filtered.AvailabilitySeries)
				summary, err := perfmetrics.QuerySummaryAll(24, groups)
				require.NoError(t, err)
				byName := map[string]perfmetrics.ModelSummary{}
				for _, item := range summary.Models {
					byName[item.ModelName] = item
				}
				require.Contains(t, byName, name)
				assert.Equal(t, detail.AvailabilityRate, byName[name].AvailabilityRate)
				assert.Equal(t, detail.AvailabilitySeries, byName[name].AvailabilitySeries)
				assert.Equal(t, 48.09, byName[name].SuccessRate, "the old API field remains compatible")
				assert.EqualValues(t, 235, byName[name].RequestCount)
				for modelName, want := range map[string]float64{legacyOnly: 70, roundsOnly: 100} {
					one, err := perfmetrics.Query(perfmetrics.QueryParams{Model: modelName, Hours: 24, AllowedGroups: groups})
					require.NoError(t, err)
					require.NotNil(t, one.AvailabilityRate)
					assert.Equal(t, want, *one.AvailabilityRate)
					assert.Equal(t, one.AvailabilityRate, byName[modelName].AvailabilityRate)
					assert.Equal(t, one.AvailabilitySeries, byName[modelName].AvailabilitySeries)
				}
				unmonitored, err := perfmetrics.Query(perfmetrics.QueryParams{Model: unknown, Hours: 24, AllowedGroups: groups})
				require.NoError(t, err)
				assert.Nil(t, unmonitored.AvailabilityRate, "individual request or probe samples cannot invent a completed round")
				assert.Empty(t, unmonitored.AvailabilitySeries)
				assert.NotContains(t, byName, unknown)
			}
			var unchanged []model.PerfMetric
			require.NoError(t, db.Order("id ASC").Find(&unchanged).Error)
			assert.Equal(t, legacy, unchanged, "merging the API timeline must leave all historical database records intact")

			// Public historical observations keep the same timeline when the
			// current route disappears. An empty historical bucket cannot make
			// an otherwise private model's global rounds publicly visible.
			require.NoError(t, db.Create(&model.PerfMetric{ModelName: roundsOnly, Group: "default", BucketTs: hour}).Error)
			for _, visibility := range []struct {
				status int
				group  string
			}{{common.ChannelStatusManuallyDisabled, "default,monitor-vip"}, {common.ChannelStatusEnabled, "retired-private"}} {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{"status": visibility.status, "group": visibility.group}).Error)
				detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24, AllowedGroups: groups})
				require.NoError(t, err)
				require.NotNil(t, detail.AvailabilityRate)
				assert.Equal(t, 80.95, *detail.AvailabilityRate)
				assert.Empty(t, detail.Channels)
				hidden, err := perfmetrics.Query(perfmetrics.QueryParams{Model: roundsOnly, Hours: 24, AllowedGroups: groups})
				require.NoError(t, err)
				assert.Nil(t, hidden.AvailabilityRate)
				assert.Empty(t, hidden.AvailabilitySeries)
				summary, err := perfmetrics.QuerySummaryAll(24, groups)
				require.NoError(t, err)
				found := false
				for _, item := range summary.Models {
					assert.NotEqual(t, roundsOnly, item.ModelName)
					if item.ModelName == name {
						found = true
						assert.Equal(t, detail.AvailabilityRate, item.AvailabilityRate)
						assert.Equal(t, detail.AvailabilitySeries, item.AvailabilitySeries)
					}
				}
				assert.True(t, found)
			}
		})
	}
}

func TestModelMonitoringPerformanceMergesSources(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"monitor-vip":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			probeOnly, mixed := "probe-performance-"+dialect.kind, "mixed-performance-"+dialect.kind
			channels := []model.Channel{
				{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: probeOnly + "," + mixed, Group: "default,monitor-vip", Key: "performance-fixture"},
				{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: probeOnly + "," + mixed, Group: "private", Key: "private-performance-fixture"},
			}
			require.NoError(t, db.Create(&channels).Error)
			now := time.Now()
			hour := now.Truncate(time.Hour).Unix()
			for _, name := range []string{probeOnly, mixed} {
				for _, latency := range []int64{200, 400} {
					perfmetrics.RecordChannelSample(perfmetrics.Sample{ChannelID: channels[0].Id, Model: name, Success: true, LatencyMs: latency, OutputTokens: 20, GenerationMs: 1000}, perfmetrics.SourceProbe)
				}
				require.NoError(t, model.RecordChannelKeyObservation(&channels[0], name, channels[0].Key, now.UnixMilli(), common.GetPointer(true), 200, "", perfmetrics.SourceProbe))
				require.NoError(t, db.Create(&model.ChannelPerfMetric{ChannelID: channels[0].Id, ModelName: name, Source: perfmetrics.SourceProbe, BucketTs: hour - 24*3600, RequestCount: 100, TotalLatencyMs: 900000}).Error)
				// Legacy per-group copies and an unauthorized physical channel
				// must not multiply a model's two real probe samples.
				for _, group := range []string{"default", "monitor-vip"} {
					require.NoError(t, db.Create(&model.ChannelPerfMetric{ChannelID: channels[0].Id, ModelName: name, Group: group, Source: perfmetrics.SourceProbe, BucketTs: hour, RequestCount: 2, SuccessCount: 2, TotalLatencyMs: 600, OutputTokens: 40, GenerationMs: 2000}).Error)
				}
				require.NoError(t, db.Create(&model.ChannelPerfMetric{ChannelID: channels[1].Id, ModelName: name, Source: perfmetrics.SourceProbe, BucketTs: hour, RequestCount: 100, SuccessCount: 100, TotalLatencyMs: 900000, OutputTokens: 90000, GenerationMs: 1000}).Error)
			}
			require.NoError(t, db.Create(&[]model.PerfMetric{
				{ModelName: mixed, Group: "default", BucketTs: hour, RequestCount: 10, SuccessCount: 8, TotalLatencyMs: 1000, OutputTokens: 100, GenerationMs: 1000},
				{ModelName: mixed, Group: "default", BucketTs: hour - 3600, RequestCount: 2, SuccessCount: 2, TotalLatencyMs: 400, OutputTokens: 20, GenerationMs: 1000},
			}).Error)
			require.NoError(t, db.Create(&[]model.ChannelPerfMetric{
				{ChannelID: channels[0].Id, ModelName: mixed, Group: "default", Source: perfmetrics.SourceRequest, BucketTs: hour, RequestCount: 10, SuccessCount: 8, TotalLatencyMs: 1000},
				{ChannelID: channels[0].Id, ModelName: mixed, Group: "default", Source: perfmetrics.SourceUsage, BucketTs: hour, RequestCount: 8, SuccessCount: 8, OutputTokens: 100, GenerationMs: 1000},
				{ChannelID: channels[0].Id, ModelName: mixed, Group: "default", Source: perfmetrics.SourceUsage, BucketTs: hour - 2*3600, RequestCount: 1, SuccessCount: 1, OutputTokens: 100, GenerationMs: 1000},
				{ChannelID: channels[0].Id, ModelName: mixed, Group: "private", Source: perfmetrics.SourceRequest, BucketTs: hour, RequestCount: 100, TotalLatencyMs: 900000},
			}).Error)
			for _, phase := range []string{"before_flush", "after_flush"} {
				t.Run(phase, func(t *testing.T) {
					if phase == "after_flush" {
						require.NoError(t, perfmetrics.Flush())
					}
					allowed := []string{"default", "monitor-vip"}
					summary, err := perfmetrics.QuerySummaryAll(24, allowed)
					require.NoError(t, err)
					require.NotNil(t, summary.Summary)
					assert.EqualValues(t, 162, summary.Summary.AvgLatencyMs, "physical probes count once across groups")
					assert.Equal(t, 42.86, summary.Summary.AvgTps)
					assert.Equal(t, 83.33, summary.Summary.SuccessRate, "the request success rate keeps final outcomes after retries")
					byName := make(map[string]perfmetrics.ModelSummary)
					for _, item := range summary.Models {
						byName[item.ModelName] = item
					}
					for _, want := range []struct {
						name    string
						latency int64
						tps     float64
					}{{probeOnly, 300, 20}, {mixed, 142, 52}} {
						detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: want.name, Hours: 24, AllowedGroups: allowed})
						require.NoError(t, err)
						require.Contains(t, byName, want.name)
						assert.Equal(t, want.latency, byName[want.name].AvgLatencyMs)
						assert.Equal(t, want.tps, byName[want.name].AvgTps)
						assert.Equal(t, want.latency, detail.AvgLatencyMs, "detail and card use the same weighted physical samples")
						assert.Equal(t, want.tps, detail.AvgTps)
						require.NotNil(t, detail.Summary)
						assert.Equal(t, want.latency, detail.Summary.AvgLatencyMs)
						assert.Equal(t, want.tps, detail.Summary.AvgTps)
						if want.name == mixed {
							assert.Equal(t, 83.33, detail.Summary.SuccessRate)
						}
						require.NotEmpty(t, detail.Series, "the combined chart includes probe-only models")
						assert.EqualValues(t, hour, detail.Series[len(detail.Series)-1].Ts)
						require.Len(t, detail.Channels, 1)
						require.Len(t, detail.Channels[0].Series, 1, "channel and model windows exclude the same expired hour")
						assert.EqualValues(t, hour, detail.Channels[0].Series[0].Ts)
						require.Len(t, detail.Groups, 2)
						for _, group := range detail.Groups {
							latency, tps := want.latency, want.tps
							if group.Group == "monitor-vip" {
								latency, tps = 300, 20
							}
							assert.Equal(t, latency, group.AvgLatencyMs)
							assert.Equal(t, tps, group.AvgTps)
							assert.NotEmpty(t, group.Series, "probe-only models also have performance history")
						}
					}
					filtered, err := perfmetrics.Query(perfmetrics.QueryParams{Model: mixed, Group: "monitor-vip", Hours: 24, AllowedGroups: allowed})
					require.NoError(t, err)
					assert.EqualValues(t, 300, filtered.AvgLatencyMs)
					assert.Equal(t, float64(20), filtered.AvgTps)
					require.Len(t, filtered.Groups, 1)
					hidden, err := perfmetrics.QuerySummaryAll(24, []string{"unrelated"})
					require.NoError(t, err)
					assert.Empty(t, hidden.Models, "private physical probes cannot expose a model or change authorized metrics")
				})
			}
		})
	}
}

func TestChannelAvailabilityIndependentOfGroups(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"monitor-vip":1,"added-public":1}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			name := "physical-channel-" + dialect.kind
			channels := []model.Channel{
				{Type: constant.ChannelTypeOpenAI, Name: "private-channel-name", Key: "private-channel-key", Status: common.ChannelStatusEnabled, Models: name, Group: "default,monitor-vip,retired-private,default"},
				{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: name, Group: "retired-private"},
				{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: name, Group: "auto"},
			}
			require.NoError(t, db.Create(&channels).Error)
			for _, probe := range []struct {
				group   string
				success bool
			}{{"", true}, {"", false}, {"default", false}, {"default", false}, {"monitor-vip", true}} {
				perfmetrics.RecordChannelSample(perfmetrics.Sample{Model: name, ChannelID: channels[0].Id, Group: probe.group, Success: probe.success, LatencyMs: 30}, perfmetrics.SourceProbe)
			}
			perfmetrics.RecordChannelSample(perfmetrics.Sample{Model: name, ChannelID: channels[1].Id, Success: true}, perfmetrics.SourceProbe)
			for _, request := range []struct {
				group   string
				success bool
				latency int64
				tokens  int64
			}{{"default", true, 100, 10}, {"monitor-vip", false, 300, 30}, {"retired-private", false, 9000, 90000}} {
				sample := perfmetrics.Sample{Model: name, ChannelID: channels[0].Id, Group: request.group, Success: request.success, LatencyMs: request.latency}
				perfmetrics.RecordChannelSample(sample, perfmetrics.SourceRequest)
				sample.OutputTokens, sample.GenerationMs = request.tokens, request.latency
				perfmetrics.RecordChannelSample(sample, perfmetrics.SourceUsage)
			}
			require.NoError(t, perfmetrics.Flush())
			var originalHistory []model.ChannelPerfMetric
			require.NoError(t, db.Where("model_name = ?", name).Order("id").Find(&originalHistory).Error)
			var response struct {
				Success bool                    `json:"success"`
				Data    perfmetrics.QueryResult `json:"data"`
			}
			recorder := modelManagementRequest(t, GetPerfMetrics, http.MethodGet, "/api/perf_metrics?model="+name, nil, &response)
			require.True(t, response.Success)
			require.Len(t, response.Data.Channels, 2, "one row per physical channel; hidden-only channels stay hidden")
			first := response.Data.Channels[0]
			assert.Equal(t, 1, first.ChannelIndex)
			require.NotNil(t, first.AvailabilityRate)
			assert.Equal(t, float64(50), *first.AvailabilityRate, "legacy per-group probe copies must not reweight the two actual probes")
			require.NotNil(t, first.SuccessRate)
			assert.Equal(t, float64(50), *first.SuccessRate)
			assert.EqualValues(t, 115, first.AvgLatencyMs, "the two visible requests and two physical probes contribute latency; hidden-group requests do not")
			assert.Equal(t, float64(100), first.AvgTps)
			assert.Equal(t, 3, response.Data.Channels[1].ChannelIndex)
			assert.Nil(t, response.Data.Channels[1].AvailabilityRate, "untested auto-group channels remain unknown")
			assert.NotContains(t, recorder.Body.String(), "private-channel-name")
			assert.NotContains(t, recorder.Body.String(), "private-channel-key")
			encodedChannels, err := common.Marshal(response.Data.Channels)
			require.NoError(t, err)
			assert.NotContains(t, string(encodedChannels), `"group"`)
			assert.NotContains(t, string(encodedChannels), `"channel_id"`)
			for _, scope := range []struct {
				group string
				rate  float64
			}{{"default", 100}, {"monitor-vip", 0}} {
				modelManagementRequest(t, GetPerfMetrics, http.MethodGet, "/api/perf_metrics?model="+name+"&group="+scope.group, nil, &response)
				require.True(t, response.Success)
				require.Len(t, response.Data.Channels, 1)
				assert.Equal(t, first.AvailabilityRate, response.Data.Channels[0].AvailabilityRate)
				assert.Equal(t, first.Series, response.Data.Channels[0].Series)
				require.NotNil(t, response.Data.Channels[0].SuccessRate)
				assert.Equal(t, scope.rate, *response.Data.Channels[0].SuccessRate)
			}
			for _, groups := range []string{"default,added-public", "monitor-vip"} {
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("group", groups).Error)
				modelManagementRequest(t, GetPerfMetrics, http.MethodGet, "/api/perf_metrics?model="+name, nil, &response)
				require.Len(t, response.Data.Channels, 2)
				assert.Equal(t, first.AvailabilityRate, response.Data.Channels[0].AvailabilityRate, "adding or removing groups must not change physical channel health")
				assert.Equal(t, first.Series, response.Data.Channels[0].Series)
			}
			modelManagementRequest(t, GetPerfMetrics, http.MethodGet, "/api/perf_metrics?model="+name+"&group=retired-private", nil, &response)
			assert.Empty(t, response.Data.Channels)
			var savedHistory []model.ChannelPerfMetric
			require.NoError(t, db.Where("model_name = ?", name).Order("id").Find(&savedHistory).Error)
			assert.Equal(t, originalHistory, savedHistory, "legacy grouped samples remain stored unchanged")
		})
	}
}

func TestChannelAvailabilityRoundsAndUsage(t *testing.T) {
	previousInterval := common.RequestInterval
	previousPerf := perf_metrics_setting.GetSetting()
	previousGroups := ratio_setting.GroupRatio2JSONString()
	previousConsume, previousExport := common.LogConsumeEnabled, common.DataExportEnabled
	common.RequestInterval = 0
	common.LogConsumeEnabled, common.DataExportEnabled = true, false
	withSelfUseModeDisabled(t)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"monitor-vip":1}`))
	t.Cleanup(func() {
		common.RequestInterval = previousInterval
		common.LogConsumeEnabled, common.DataExportEnabled = previousConsume, previousExport
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})

	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			// The legacy schema and data exist before the additive channel table.
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.Log{}))
			legacy := model.PerfMetric{ModelName: "legacy-metric", Group: "default", BucketTs: time.Now().Unix() / 3600 * 3600, RequestCount: 3, SuccessCount: 2, OutputTokens: 11}
			require.NoError(t, model.UpsertPerfMetric(&legacy))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}))
				var preserved model.PerfMetric
				require.NoError(t, db.First(&preserved, legacy.Id).Error)
				assert.Equal(t, legacy, preserved)
				assert.True(t, db.Migrator().HasIndex(&model.PerfMetric{}, "idx_perf_model_group_bucket"))
				assert.True(t, db.Migrator().HasIndex(&model.ChannelPerfMetric{}, "idx_channel_perf_bucket"))
			}
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			user := &model.User{Username: "availability-test-user", Password: "unused", Group: "default", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "availability"}
			require.NoError(t, db.Create(user).Error)
			name := fmt.Sprintf("availability-%s-%d", dialect.kind, time.Now().UnixNano())
			prices, err := common.Marshal(map[string]float64{name: 1})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(prices)))
			var firstHealthy atomic.Bool
			firstHealthy.Store(true)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if (r.Header.Get("Authorization") == "Bearer first-key" && firstHealthy.Load()) || r.Header.Get("Authorization") == "Bearer disabled-key" {
					_, _ = fmt.Fprint(w, `{"id":"test","model":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, `{"error":{"message":"unavailable","type":"server_error"}}`)
			}))
			t.Cleanup(upstream.Close)
			channels := []*model.Channel{
				{Type: constant.ChannelTypeOpenAI, Name: "private-first-name", Key: "first-key", Status: common.ChannelStatusEnabled, Models: name, Group: "default", BaseURL: common.GetPointer(upstream.URL)},
				{Type: constant.ChannelTypeOpenAI, Name: "private-second-name", Key: "second-key", Status: common.ChannelStatusEnabled, Models: name, Group: "monitor-vip", BaseURL: common.GetPointer(upstream.URL)},
				{Type: constant.ChannelTypeOpenAI, Name: "private-disabled-name", Key: "disabled-key", Status: common.ChannelStatusAutoDisabled, Models: name, Group: "default", BaseURL: common.GetPointer(upstream.URL)},
			}
			for _, channel := range channels {
				require.NoError(t, db.Create(channel).Error)
			}

			manual := testChannel(context.Background(), channels[0], user.Id, name, "", false)
			require.NoError(t, manual.localErr)
			require.NoError(t, perfmetrics.Flush())
			var probeRows []model.ChannelPerfMetric
			require.NoError(t, db.Where("model_name = ? AND source = ?", name, perfmetrics.SourceProbe).Find(&probeRows).Error)
			require.Len(t, probeRows, 1, "one dispatched test must produce only one physical-channel probe sample")
			assert.Empty(t, probeRows[0].Group)
			detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24})
			require.NoError(t, err)
			require.NotNil(t, detail.AvailabilityRate)
			assert.Equal(t, float64(100), *detail.AvailabilityRate, "the legacy observation is used while complete round data is absent")
			var completedRounds int64
			require.NoError(t, db.Model(&model.ChannelPerfMetric{}).Where("model_name = ? AND source = ?", name, perfmetrics.SourceAvailabilityAll).Count(&completedRounds).Error)
			assert.Zero(t, completedRounds, "a manual single-channel test must not manufacture a completed model-wide round")
			require.Len(t, detail.Channels, 2)
			require.NotNil(t, detail.Channels[0].AvailabilityRate)
			assert.Equal(t, float64(100), *detail.Channels[0].AvailabilityRate)
			assert.Nil(t, detail.Channels[1].AvailabilityRate, "untested routes remain unknown")

			for round, want := range []float64{100, 50, 33.33} {
				if round > 0 {
					firstHealthy.Store(false)
				}
				performChannelTests(context.Background(), channels, user.Id, 2, nil)
				detail, err = perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24})
				require.NoError(t, err)
				require.NotNil(t, detail.AvailabilityRate)
				assert.Equal(t, want, *detail.AvailabilityRate, "OR within each completed round, then success rounds / total rounds")
				require.Len(t, detail.Channels, 2, "an auto-disabled channel cannot provide usable failover")
				var vip *float64
				for _, group := range detail.Groups {
					if group.Group == "monitor-vip" {
						vip = group.AvailabilityRate
					}
				}
				require.NotNil(t, vip)
				assert.Zero(t, *vip)
				summary, err := perfmetrics.QuerySummaryAll(24, []string{"default", "monitor-vip"})
				require.NoError(t, err)
				for _, item := range summary.Models {
					if item.ModelName == name {
						assert.Equal(t, detail.AvailabilityRate, item.AvailabilityRate)
					}
				}
			}
			before := *detail.AvailabilityRate
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			performChannelTests(ctx, channels, user.Id, 2, nil)
			channels[0].ModelMapping = common.GetPointer(`{"invalid":`)
			performChannelTests(context.Background(), channels, user.Id, 2, nil)
			detail, err = perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24})
			require.NoError(t, err)
			assert.Equal(t, before, *detail.AvailabilityRate, "cancelled and locally incomplete rounds must not create uptime samples")
			vipDetail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "monitor-vip", Hours: 24})
			require.NoError(t, err)
			require.NotNil(t, vipDetail.AvailabilityRate)
			assert.Zero(t, *vipDetail.AvailabilityRate, "a group filter must select that group's availability, not the global OR")
			for _, point := range vipDetail.AvailabilitySeries {
				assert.Zero(t, point.SuccessRate)
			}

			info := &relaycommon.RelayInfo{OriginModelName: name, UsingGroup: "default", StartTime: time.Now().Add(-time.Second), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channels[0].Id}}
			perfmetrics.RecordChannelUsage(info, 20, 10, 250)
			perfmetrics.RecordChannelAttempt(info, channels[0].Id, true)
			perfmetrics.RecordChannelSample(perfmetrics.RelaySample(info, false, 0), perfmetrics.SourceRequest)
			usage, err := perfmetrics.QueryChannelUsage(channels[0].Id, 24)
			require.NoError(t, err)
			assert.EqualValues(t, 2, usage.RequestCount)
			assert.EqualValues(t, 1, usage.SuccessCount)
			assert.EqualValues(t, 20, usage.InputTokens)
			assert.EqualValues(t, 10, usage.OutputTokens)
			assert.EqualValues(t, 250, usage.UsedQuota)
			assert.EqualValues(t, 4, usage.ProbeCount, "manual and three completed probes are counted once each")
			require.NotNil(t, usage.SuccessRate)
			assert.Equal(t, float64(50), *usage.SuccessRate)
			require.NoError(t, perfmetrics.FlushChannelMetrics())
			after, err := perfmetrics.QueryChannelUsage(channels[0].Id, 24)
			require.NoError(t, err)
			assert.Equal(t, usage, after, "flush must preserve totals without double-counting")
			var rowCount int64
			require.NoError(t, db.Model(&model.ChannelPerfMetric{}).Where("channel_id = ? AND source = ?", channels[0].Id, perfmetrics.SourceRequest).Count(&rowCount).Error)
			assert.EqualValues(t, 1, rowCount, "upsert preserves per-channel source bucket uniqueness")
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}))
			}
			after, err = perfmetrics.QueryChannelUsage(channels[0].Id, 24)
			require.NoError(t, err)
			assert.Equal(t, usage, after, "repeat migrations preserve channel counters")

			var response struct {
				Success bool                    `json:"success"`
				Data    perfmetrics.QueryResult `json:"data"`
			}
			recorder := modelManagementRequest(t, GetPerfMetrics, http.MethodGet, "/api/perf-metrics?model="+url.QueryEscape(name), nil, &response)
			require.True(t, response.Success)
			for _, private := range []string{"private-first-name", "private-second-name", "first-key", "second-key", "channel_id", "request_count", "input_tokens", "output_tokens", "used_quota"} {
				assert.NotContains(t, recorder.Body.String(), private)
			}
			assert.Equal(t, []int{1, 2}, []int{response.Data.Channels[0].ChannelIndex, response.Data.Channels[1].ChannelIndex})
			privateModel := name + "-private"
			require.NoError(t, db.Create(&model.Channel{Status: common.ChannelStatusEnabled, Models: privateModel, Group: "retired-private", Name: "private-model-route"}).Error)
			perfmetrics.RecordChannelSample(perfmetrics.Sample{Model: privateModel, Success: true}, perfmetrics.SourceAvailabilityAll)
			privateSummary, err := perfmetrics.QuerySummaryAll(24, []string{"retired-private"})
			require.NoError(t, err)
			require.Len(t, privateSummary.Models, 1)
			assert.Equal(t, privateModel, privateSummary.Models[0].ModelName)
			publicSummary, err := perfmetrics.QuerySummaryAll(24, []string{"default", "monitor-vip"})
			require.NoError(t, err)
			for _, item := range publicSummary.Models {
				assert.NotEqual(t, privateModel, item.ModelName, "historical global availability must not reintroduce a model from a hidden group")
			}

			// Billing totals also cover sources outside synchronous relay metrics,
			// and must query a separately configured log database when present.
			separateLogDB, _ := newAuditTestDatabase(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, separateLogDB.AutoMigrate(&model.Log{}))
			require.NoError(t, separateLogDB.AutoMigrate(&model.Log{}))
			originalLogDB := model.LOG_DB
			model.LOG_DB = separateLogDB
			t.Cleanup(func() {
				model.LOG_DB = originalLogDB
				connection, err := separateLogDB.DB()
				if err == nil {
					require.NoError(t, connection.Close())
				}
			})
			now := time.Now().Unix()
			billingRecords := []model.Log{
				{ChannelId: channels[0].Id, CreatedAt: now, Type: model.LogTypeConsume, Quota: 500, PromptTokens: 20, CompletionTokens: 10, TokenId: 7, TokenName: "模型测试", Content: "模型测试"},
				{ChannelId: channels[0].Id, CreatedAt: now, Type: model.LogTypeConsume, Quota: 100, PromptTokens: 3, CompletionTokens: 2, TokenId: 0, TokenName: "playground", Content: "real request"},
				{ChannelId: channels[0].Id, CreatedAt: now, Type: model.LogTypeRefund, Quota: 50},
				{ChannelId: channels[0].Id, CreatedAt: now, Type: model.LogTypeConsume, Quota: 900, TokenId: 0, TokenName: "模型测试", Content: "模型测试"},
				{ChannelId: channels[0].Id, CreatedAt: now - 25*3600, Type: model.LogTypeConsume, Quota: 1000},
				{ChannelId: channels[1].Id, CreatedAt: now, Type: model.LogTypeConsume, Quota: 2000},
				{ChannelId: channels[0].Id, CreatedAt: now, Type: model.LogTypeError, Quota: 3000},
			}
			require.NoError(t, separateLogDB.Create(&billingRecords).Error)
			withBilling, err := perfmetrics.QueryChannelUsage(channels[0].Id, 24)
			require.NoError(t, err)
			assert.EqualValues(t, 3, withBilling.BillingRecordCount)
			assert.EqualValues(t, 550, withBilling.RecordedUsedQuota)
			assert.EqualValues(t, 23, withBilling.RecordedInputTokens)
			assert.EqualValues(t, 12, withBilling.RecordedOutputTokens)
			assert.Equal(t, usage.RequestCount, withBilling.RequestCount, "settlement records do not inflate channel request attempts")
		})
	}
}

func TestChannelRequestAttemptsSeparateUsageAndRetryTiming(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	previousRetries := common.RetryTimes
	common.RetryTimes = 0
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true"}))
	t.Cleanup(func() {
		common.RetryTimes = previousRetries
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled)}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.Log{}, &model.Task{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			name := fmt.Sprintf("attempt-%s-%d", dialect.kind, time.Now().UnixNano())
			channels := []*model.Channel{
				{Type: constant.ChannelTypeOpenAI, Name: "slow route", Status: common.ChannelStatusEnabled, Group: "default", Models: name},
				{Type: constant.ChannelTypeOpenAI, Name: "fast route", Status: common.ChannelStatusEnabled, Group: "default", Models: name},
				{Type: constant.ChannelTypeTaskPlugin, Name: "task route", Status: common.ChannelStatusEnabled, Group: "default", Models: name},
			}
			for _, channel := range channels {
				require.NoError(t, db.Create(channel).Error)
			}
			started := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
			info := &relaycommon.RelayInfo{OriginModelName: name, UsingGroup: "default", RelayFormat: "openai-realtime", StartTime: started, ChannelAttemptStartTime: started, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channels[0].Id}}
			first := perfmetrics.ChannelAttemptSample(info, channels[0].Id, false, started.Add(10*time.Second))
			assert.EqualValues(t, 10_000, first.LatencyMs)
			perfmetrics.RecordChannelSample(first, perfmetrics.SourceRequest)
			info.ChannelAttemptStartTime = started.Add(10 * time.Second)
			info.ChannelMeta.ChannelId = channels[1].Id
			second := perfmetrics.ChannelAttemptSample(info, channels[1].Id, true, started.Add(10*time.Second+100*time.Millisecond))
			assert.EqualValues(t, 100, second.LatencyMs, "successful failover must not inherit the previous channel's latency")
			assert.Equal(t, started, info.StartTime, "legacy end-to-end timing is unchanged")
			perfmetrics.RecordChannelSample(second, perfmetrics.SourceRequest)
			// Realtime or split settlement can emit several usage records for one
			// attempt. None of those records contributes another request outcome.
			perfmetrics.RecordChannelUsage(info, 20, 10, 100)
			perfmetrics.RecordChannelUsage(info, 5, 3, 25)
			second.Success = false
			perfmetrics.RecordChannelSample(second, perfmetrics.SourceRequest)
			usage, err := perfmetrics.QueryChannelUsage(channels[1].Id, 24)
			require.NoError(t, err)
			assert.EqualValues(t, 2, usage.RequestCount)
			assert.EqualValues(t, 1, usage.SuccessCount)
			require.NotNil(t, usage.SuccessRate)
			assert.Equal(t, float64(50), *usage.SuccessRate)
			assert.EqualValues(t, 100, usage.AvgLatencyMs)
			assert.EqualValues(t, 25, usage.InputTokens)
			assert.EqualValues(t, 13, usage.OutputTokens)
			assert.EqualValues(t, 125, usage.UsedQuota)

			for _, succeeded := range []bool{true, false} {
				ctx := taskSubmissionTestContext()
				taskInfo := taskSubmissionRelayInfo(nil)
				taskInfo.OriginModelName = name
				taskInfo.StartTime = started
				taskInfo.LockedChannel = channels[2]
				taskInfo.ChannelMeta.ChannelId = channels[2].Id
				_, taskErr := executeTaskSubmissionWith(ctx, taskInfo, func(_ *gin.Context, attempt *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *taskdto.TaskError) {
					assert.True(t, attempt.ChannelAttemptStartTime.After(started), "the actual submission loop resets its attempt timer")
					if !succeeded {
						return nil, service.TaskErrorWrapper(errors.New("upstream unavailable"), "upstream_unavailable", http.StatusServiceUnavailable)
					}
					return &relay.TaskSubmitResult{UpstreamTaskID: "accepted", Platform: constant.TaskPlatform("plugin")}, nil
				})
				if succeeded {
					require.Nil(t, taskErr)
				} else {
					require.NotNil(t, taskErr)
				}
			}
			taskUsage, err := perfmetrics.QueryChannelUsage(channels[2].Id, 24)
			require.NoError(t, err)
			assert.EqualValues(t, 2, taskUsage.RequestCount)
			assert.EqualValues(t, 1, taskUsage.SuccessCount)
			require.NotNil(t, taskUsage.SuccessRate)
			assert.Equal(t, float64(50), *taskUsage.SuccessRate)
		})
	}
}

func TestRelayHandlerRecordsFinalMonitoringResult(t *testing.T) {
	previousRetries, previousCountTokens := common.RetryTimes, constant.CountToken
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 10
	previousLogs, previousTrace := common.LogConsumeEnabled, common.RequestTraceEnabled
	common.RetryTimes, constant.CountToken = 1, false
	common.LogConsumeEnabled, common.RequestTraceEnabled = false, false
	t.Cleanup(func() {
		constant.StreamingTimeout = previousTimeout
		common.RetryTimes, constant.CountToken = previousRetries, previousCountTokens
		common.LogConsumeEnabled, common.RequestTraceEnabled = previousLogs, previousTrace
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			for _, scenario := range []struct {
				name                          string
				stream, cancelDone, cancelMid bool
				invalid                       bool
				retry                         bool
				wantCalls                     int
			}{{name: "success", wantCalls: 1}, {name: "stream-done-then-disconnect", stream: true, cancelDone: true, wantCalls: 1}, {name: "stream-client-aborts", stream: true, cancelMid: true, wantCalls: 1}, {name: "validation-failure", invalid: true}, {name: "retry-success", retry: true, wantCalls: 2}} {
				t.Run(scenario.name, func(t *testing.T) {
					db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
					require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}, &model.Log{}))
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true", "quota_setting.enable_free_model_pre_consume": "false"}))
					require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
					t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
					name := "relay-final-" + dialect.kind + "-" + scenario.name
					prices, err := common.Marshal(map[string]float64{name: 0})
					require.NoError(t, err)
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(prices)))
					user := &model.User{Username: name, Password: "unused", Group: "default", Status: common.UserStatusEnabled, Quota: 1000, AffCode: scenario.name}
					require.NoError(t, db.Create(user).Error)
					var upstreamCalls atomic.Int64
					var firstCalls, fallbackCalls atomic.Int64
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						upstreamCalls.Add(1)
						first := r.Header.Get("Authorization") == "Bearer first-fixture-key"
						if first {
							firstCalls.Add(1)
						} else {
							fallbackCalls.Add(1)
						}
						w.Header().Set("Content-Type", "application/json")
						if scenario.retry && first {
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = fmt.Fprint(w, `{"error":{"message":"fixture upstream unavailable","type":"server_error"}}`)
							return
						}
						if scenario.stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "data: {\"id\":\"test\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"}}]}\n\n")
							w.(http.Flusher).Flush()
							if scenario.cancelMid {
								// OpenAI forwarding buffers one event; a second event
								// makes the first visible before the client disconnects.
								_, _ = fmt.Fprint(w, "data: {\"id\":\"test\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"more\"}}]}\n\n")
								w.(http.Flusher).Flush()
								<-r.Context().Done()
								return
							}
							_, _ = fmt.Fprint(w, "data: {\"id\":\"test\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\ndata: [DONE]\n\n")
							return
						}
						_, _ = fmt.Fprint(w, `{"id":"test","model":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
					}))
					t.Cleanup(upstream.Close)
					channels := []*model.Channel{
						{Type: constant.ChannelTypeOpenAI, Key: "first-fixture-key", Models: name, Group: "default", Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(2)), AutoBan: common.GetPointer(0), BaseURL: common.GetPointer(upstream.URL)},
						{Type: constant.ChannelTypeOpenAI, Key: "fallback-fixture-key", Models: name, Group: "default", Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(1)), AutoBan: common.GetPointer(0), BaseURL: common.GetPointer(upstream.URL)},
					}
					for _, channel := range channels {
						require.NoError(t, channel.Insert())
					}
					body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, name, scenario.stream)
					if scenario.invalid {
						body = fmt.Sprintf(`{"model":%q,"messages":[]}`, name)
					}
					recorder := httptest.NewRecorder()
					ctx, cancel := context.WithCancel(context.Background())
					t.Cleanup(cancel)
					var writer http.ResponseWriter = recorder
					if scenario.cancelDone || scenario.cancelMid {
						event := "data: [DONE]"
						if scenario.cancelMid {
							event = `"content":"OK"`
						}
						writer = &cancelOnStreamEventWriter{ResponseWriter: recorder, cancel: cancel, event: event}
					}
					c, _ := gin.CreateTestContext(writer)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
					c.Request.Header.Set("Content-Type", "application/json")
					common.SetContextKey(c, constant.ContextKeyUserId, user.Id)
					common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
					common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
					common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
					require.Nil(t, middleware.SetupContextForSelectedChannel(c, channels[0], name))
					Relay(c, kittypes.RelayFormatOpenAI)
					assert.EqualValues(t, scenario.wantCalls, upstreamCalls.Load())
					if scenario.invalid {
						assert.Equal(t, http.StatusBadRequest, recorder.Code)
					} else {
						require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
						assert.Contains(t, recorder.Body.String(), `"content":"OK"`)
						assert.EqualValues(t, 1, firstCalls.Load())
						assert.Equal(t, scenario.retry, fallbackCalls.Load() == 1)
						// Health is recorded at the relay boundary, independently of
						// settlement. Abandoned streams must not become successes.
						result, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "default", Hours: 24})
						require.NoError(t, err)
						if scenario.cancelMid {
							assert.Empty(t, result.Groups)
							assert.Nil(t, result.Summary)
						} else {
							require.Len(t, result.Groups, 1)
							assert.Len(t, result.Groups[0].Series, 1)
						}
					}
					require.NoError(t, perfmetrics.Flush())
					var metrics []model.ChannelPerfMetric
					require.NoError(t, db.Where("model_name = ? AND source IN ?", name, []string{perfmetrics.SourceRequest, perfmetrics.SourceRelayResult}).Find(&metrics).Error)
					if scenario.invalid || scenario.cancelMid {
						assert.Empty(t, metrics, "validation failures and abandoned streams must not create upstream observations")
					} else {
						var requestCount, successCount, finalCount, finalSuccess int64
						for _, row := range metrics {
							if row.Source == perfmetrics.SourceRelayResult {
								finalCount += row.RequestCount
								finalSuccess += row.SuccessCount
							} else {
								requestCount += row.RequestCount
								successCount += row.SuccessCount
							}
						}
						assert.EqualValues(t, scenario.wantCalls, requestCount)
						assert.EqualValues(t, 1, successCount)
						assert.EqualValues(t, 1, finalCount, "all retries share one final model observation")
						assert.EqualValues(t, 1, finalSuccess, "a failed attempt followed by success is a successful model request")
					}
					if scenario.stream {
						assert.ErrorIs(t, ctx.Err(), context.Canceled)
						var activity model.ChannelModelActivity
						require.NoError(t, db.Where("channel_id = ? AND model_name = ?", channels[0].Id, name).First(&activity).Error)
						if scenario.cancelMid {
							assert.Zero(t, activity.LastResultAt, "an abandoned stream gives no upstream health result")
						} else {
							assert.True(t, activity.LastResultSuccess, "DONE before disconnect remains successful during usage settlement")
							assert.Positive(t, activity.LastResultAt)
						}
					}
					var savedUser model.User
					require.NoError(t, db.First(&savedUser, user.Id).Error)
					assert.Equal(t, user.Quota, savedUser.Quota, "the free fixture must not charge the user")
				})
			}
		})
	}
}

func TestAvailabilityFreshResultsAndHistory(t *testing.T) {
	previousPerf := perf_metrics_setting.GetSetting()
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": "true", "perf_metrics_setting.bucket_time": "hour"}))
	t.Setenv("CHANNEL_TEST_FREQUENCY", "10")
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"perf_metrics_setting.enabled": strconv.FormatBool(previousPerf.Enabled), "perf_metrics_setting.bucket_time": previousPerf.BucketTime}))
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.PerfMetric{}, &model.ChannelPerfMetric{}, &model.ChannelModelActivity{}, &model.ChannelKeyObservation{}, &model.ChannelBalanceSample{}, &model.Log{}))
			t.Cleanup(func() { require.NoError(t, perfmetrics.Flush()) })
			name := "fresh-outcome-" + dialect.kind
			onlyTraffic := name + "-traffic"
			channels := []model.Channel{
				{Status: common.ChannelStatusEnabled, Models: name + "," + onlyTraffic, Group: "default,monitor-vip"},
				{Status: common.ChannelStatusEnabled, Models: name, Group: "default"},
			}
			require.NoError(t, db.Create(&channels).Error)
			now := time.Now()
			hour := now.Unix() - now.Unix()%3600
			legacy := []model.PerfMetric{
				{ModelName: name, Group: "default", BucketTs: hour, RequestCount: 20, SuccessCount: 0},
				{ModelName: onlyTraffic, Group: "default", BucketTs: hour, RequestCount: 2, SuccessCount: 1},
			}
			require.NoError(t, db.Create(&legacy).Error)
			old := []model.ChannelPerfMetric{
				{ModelName: name, Source: perfmetrics.SourceAvailabilityAll, BucketTs: hour, RequestCount: 12, SuccessCount: 0},
				{ModelName: name, Source: perfmetrics.SourceAvailability, Group: "default", BucketTs: hour, RequestCount: 12, SuccessCount: 0},
				{ModelName: name, ChannelID: channels[0].Id, Source: perfmetrics.SourceProbe, BucketTs: hour, RequestCount: 1, SuccessCount: 0},
				{ModelName: name, ChannelID: channels[0].Id, Source: perfmetrics.SourceRouteState, BucketTs: hour, RequestCount: 12, SuccessCount: 0},
			}
			require.NoError(t, db.Create(&old).Error)
			// An unsuccessful upstream attempt followed by a successful fallback
			// is one successful model request, while both channels keep their own outcomes.
			info := &relaycommon.RelayInfo{OriginModelName: name, UsingGroup: "default", StartTime: now.Add(-time.Second), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channels[1].Id}}
			perfmetrics.RecordChannelAttempt(info, channels[1].Id, false)
			info.ChannelMeta.ChannelId = channels[0].Id
			perfmetrics.RecordChannelAttempt(info, channels[0].Id, true)
			perfmetrics.RecordChannelRelayResult(info, true)
			info.OriginModelName = onlyTraffic
			perfmetrics.RecordChannelRelayResult(info, true)
			info.IsChannelTest = true
			perfmetrics.RecordChannelRelayResult(info, false)
			require.NoError(t, model.RecordChannelModelRequest(channels[0].Id, name, now.UnixMilli(), 0, common.GetPointer(true)))
			require.NoError(t, model.RecordChannelModelProbe(channels[1].Id, name, now.Add(-time.Second).UnixMilli(), common.GetPointer(false)))
			for _, state := range []string{"buffered", "persisted", "repeated-flush"} {
				if state != "buffered" {
					require.NoError(t, perfmetrics.Flush())
				}
				detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24, AllowedGroups: []string{"default", "monitor-vip"}})
				require.NoError(t, err)
				require.NotNil(t, detail.CurrentAvailable)
				assert.True(t, *detail.CurrentAvailable, state+": one currently successful channel makes the model available")
				assert.Equal(t, now.Unix(), detail.CurrentObservedAt)
				require.NotNil(t, detail.AvailabilityRate)
				assert.Equal(t, 7.69, *detail.AvailabilityRate, "one final success is added to 12 old failed rounds, without retry failures or cached states")
				require.Len(t, detail.Channels, 2, "groups cannot duplicate physical channels")
				require.NotNil(t, detail.Channels[0].AvailabilityRate)
				assert.Equal(t, float64(50), *detail.Channels[0].AvailabilityRate, "a real success and a physical failed probe both count; cached route states do not overwrite them")
				assert.Equal(t, common.GetPointer(true), detail.Channels[0].CurrentAvailable)
				assert.Equal(t, common.GetPointer(false), detail.Channels[1].CurrentAvailable)
				filtered, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Group: "default", Hours: 24, AllowedGroups: []string{"default"}})
				require.NoError(t, err)
				assert.Equal(t, detail.AvailabilityRate, filtered.AvailabilityRate)
				summary, err := perfmetrics.QuerySummaryAll(24, []string{"default", "monitor-vip"})
				require.NoError(t, err)
				byName := map[string]perfmetrics.ModelSummary{}
				for _, item := range summary.Models {
					byName[item.ModelName] = item
				}
				assert.Equal(t, detail.AvailabilityRate, byName[name].AvailabilityRate)
				assert.Equal(t, detail.CurrentAvailable, byName[name].CurrentAvailable)
				require.NotNil(t, byName[onlyTraffic].AvailabilityRate)
				assert.Equal(t, float64(50), *byName[onlyTraffic].AvailabilityRate, "legacy hours already include real traffic and must not count it twice")
			}
			for _, scenario := range []struct {
				name                string
				firstSuccess        bool
				firstAge, secondAge time.Duration
				want                *bool
			}{
				{"all fresh failures", false, 0, 0, common.GetPointer(false)},
				{"unknown channel prevents all-failed claim", false, 0, 11 * time.Minute, nil},
				{"one fresh success is enough", true, 0, 11 * time.Minute, common.GetPointer(true)},
				{"expired results are unknown", true, 11 * time.Minute, 11 * time.Minute, nil},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					require.NoError(t, db.Model(&model.ChannelModelActivity{}).Where("channel_id = ? AND model_name = ?", channels[0].Id, name).Updates(map[string]any{"last_result_success": scenario.firstSuccess, "last_result_at": now.Add(-scenario.firstAge).UnixMilli()}).Error)
					require.NoError(t, db.Model(&model.ChannelModelActivity{}).Where("channel_id = ? AND model_name = ?", channels[1].Id, name).Update("last_result_at", now.Add(-scenario.secondAge).UnixMilli()).Error)
					detail, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 24, AllowedGroups: []string{"default"}})
					require.NoError(t, err)
					assert.Equal(t, scenario.want, detail.CurrentAvailable)
				})
			}
			var unchangedLegacy []model.PerfMetric
			require.NoError(t, db.Where("model_name IN ?", []string{name, onlyTraffic}).Order("id ASC").Find(&unchangedLegacy).Error)
			assert.Equal(t, legacy, unchangedLegacy)
			ids := []int{}
			for _, row := range old {
				ids = append(ids, row.Id)
			}
			var unchanged []model.ChannelPerfMetric
			require.NoError(t, db.Where("id IN ?", ids).Order("id ASC").Find(&unchanged).Error)
			assert.Equal(t, old, unchanged, "reading corrected status must preserve historical rows")

			relayOnly, emptyOnly := name+"-relay-only", name+"-empty-only"
			removedChannel := &model.Channel{Status: common.ChannelStatusEnabled, Models: relayOnly, Group: "default"}
			hiddenChannel := &model.Channel{Status: common.ChannelStatusEnabled, Models: emptyOnly, Group: "retired-private"}
			require.NoError(t, db.Create(removedChannel).Error)
			require.NoError(t, db.Create(hiddenChannel).Error)
			require.NoError(t, db.Create(&[]model.ChannelPerfMetric{
				{ModelName: relayOnly, ChannelID: removedChannel.Id, Source: perfmetrics.SourceRelayResult, Group: "default", BucketTs: hour, RequestCount: 1, SuccessCount: 1},
				{ModelName: emptyOnly, ChannelID: hiddenChannel.Id, Source: perfmetrics.SourceRelayResult, Group: "default", BucketTs: hour},
				{ModelName: emptyOnly, Source: perfmetrics.SourceAvailabilityAll, BucketTs: hour, RequestCount: 1, SuccessCount: 1},
			}).Error)
			require.NoError(t, db.Delete(removedChannel).Error)
			relayHistory, err := perfmetrics.Query(perfmetrics.QueryParams{Model: relayOnly, Hours: 24, AllowedGroups: []string{"default"}})
			require.NoError(t, err)
			assert.Empty(t, relayHistory.Channels)
			assert.Equal(t, common.GetPointer(float64(100)), relayHistory.AvailabilityRate, "authorized final-request history remains visible after its channel is removed")
			emptyHistory, err := perfmetrics.Query(perfmetrics.QueryParams{Model: emptyOnly, Hours: 24, AllowedGroups: []string{"default"}})
			require.NoError(t, err)
			assert.Empty(t, emptyHistory.Channels)
			assert.Nil(t, emptyHistory.AvailabilityRate, "a zero-request bucket cannot reveal hidden model rounds")
			summary, err := perfmetrics.QuerySummaryAll(24, []string{"default"})
			require.NoError(t, err)
			byName := map[string]perfmetrics.ModelSummary{}
			for _, item := range summary.Models {
				byName[item.ModelName] = item
			}
			require.Contains(t, byName, relayOnly)
			assert.Equal(t, relayHistory.AvailabilityRate, byName[relayOnly].AvailabilityRate)
			assert.NotContains(t, byName, emptyOnly)
		})
	}
}

// The downstream client closes immediately after receiving the chosen SSE event.
type cancelOnStreamEventWriter struct {
	http.ResponseWriter
	cancel context.CancelFunc
	event  string
}

func (w *cancelOnStreamEventWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if bytes.Contains(p, []byte(w.event)) {
		w.cancel()
	}
	return n, err
}
func (w *cancelOnStreamEventWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

func TestRelayObservationDistinguishesStreamCompletionAndCancellation(t *testing.T) {
	streamModerationErr := kittypes.WithOpenAIError(kittypes.OpenAIError{Type: "content_filter", Message: "content rejected"}, http.StatusBadRequest)
	for _, tc := range []struct {
		name                     string
		reason                   relaycommon.StreamEndReason
		cancelled, softError     bool
		endErr                   error
		apiErr                   *kittypes.NewAPIError
		want                     *bool
		code, errorType, message string
		statusCode               int
		moderation               bool
	}{
		{name: "complete-then-cancel", reason: relaycommon.StreamEndReasonDone, cancelled: true, want: common.GetPointer(true)},
		{name: "client-cancel-before-terminal", reason: relaycommon.StreamEndReasonClientGone, cancelled: true},
		{name: "cancelled-transport", cancelled: true, apiErr: kittypes.NewError(context.Canceled, kittypes.ErrorCodeBadResponse)},
		{name: "real-upstream-error-after-client-cancel", cancelled: true, apiErr: kittypes.NewErrorWithStatusCode(errors.New("insufficient balance"), kittypes.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired), want: common.GetPointer(false)},
		{name: "scanner-cancel-race", reason: relaycommon.StreamEndReasonScannerErr, cancelled: true, endErr: context.Canceled},
		{name: "downstream-ping-failure", reason: relaycommon.StreamEndReasonPingFail, endErr: errors.New("broken pipe")},
		{name: "stream-timeout", reason: relaycommon.StreamEndReasonTimeout, want: common.GetPointer(false)},
		{name: "stream-error-before-done", reason: relaycommon.StreamEndReasonDone, softError: true, want: common.GetPointer(false)},
		{name: "typed-stream-moderation", reason: relaycommon.StreamEndReasonHandlerStop, endErr: kittypes.WithOpenAIError(kittypes.OpenAIError{Code: "content_filter", Message: "content rejected"}, http.StatusForbidden)},
		{name: "wrapped-stream-moderation", reason: relaycommon.StreamEndReasonHandlerStop, endErr: fmt.Errorf("stream stopped: %w", kittypes.WithOpenAIError(kittypes.OpenAIError{Type: "prompt_blocked", Message: "content rejected"}, http.StatusBadRequest))},
		{name: "typed-stream-gateway-failure", reason: relaycommon.StreamEndReasonHandlerStop, endErr: kittypes.WithOpenAIError(kittypes.OpenAIError{Code: "content_filter", Message: "filter service unavailable"}, http.StatusBadGateway), want: common.GetPointer(false)},
		{name: "moderation-after-stream-error-remains-failure", reason: relaycommon.StreamEndReasonHandlerStop, softError: true, endErr: kittypes.WithOpenAIError(kittypes.OpenAIError{Code: "content_filter", Message: "content rejected"}, http.StatusBadRequest), want: common.GetPointer(false)},
		{name: "moderation-after-scanner-error-remains-failure", reason: relaycommon.StreamEndReasonScannerErr, endErr: kittypes.WithOpenAIError(kittypes.OpenAIError{Code: "content_filter", Message: "content rejected"}, http.StatusBadRequest), want: common.GetPointer(false)},
		{name: "direct-moderation-does-not-hide-stream-error", statusCode: 400, code: "content_filter", softError: true, moderation: true, want: common.GetPointer(false)},
		{name: "returned-stream-moderation-remains-neutral", reason: relaycommon.StreamEndReasonHandlerStop, apiErr: streamModerationErr, endErr: streamModerationErr, moderation: true},
		{name: "returned-stream-moderation-with-earlier-error-remains-failure", reason: relaycommon.StreamEndReasonHandlerStop, apiErr: streamModerationErr, endErr: streamModerationErr, softError: true, moderation: true, want: common.GetPointer(false)},
		{name: "filter-code-ok", statusCode: 200, code: "content_filter", moderation: true},
		{name: "filter-type-bad-request", statusCode: 400, errorType: "content_filter", moderation: true},
		{name: "policy-code-bad-request", statusCode: 400, code: "content_policy_violation", moderation: true},
		{name: "policy-type-forbidden", statusCode: 403, errorType: "content_policy_violation", moderation: true},
		{name: "responsible-ai-code-forbidden", statusCode: 403, code: "ResponsibleAIPolicyViolation", moderation: true},
		{name: "responsible-ai-type-unprocessable", statusCode: 422, errorType: "ResponsibleAIPolicyViolation", moderation: true},
		{name: "filter-error-code-unprocessable", statusCode: 422, code: "content_filter_error", moderation: true},
		{name: "filter-error-type-ok", statusCode: 200, errorType: "content_filter_error", moderation: true},
		{name: "sensitive-words-code", statusCode: 400, code: "sensitive_words_detected", moderation: true},
		{name: "sensitive-words-type", statusCode: 400, errorType: "sensitive_words_detected", moderation: true},
		{name: "blocked-prompt-code", statusCode: 403, code: "prompt_blocked", moderation: true},
		{name: "blocked-prompt-type", statusCode: 422, errorType: "prompt_blocked", moderation: true},
		{name: "ifly-message-bad-request", statusCode: 400, errorType: "invalid_request_error", message: "根据相关法律法规，有关信息不予显示。", moderation: true},
		{name: "ifly-message-forbidden", statusCode: 403, errorType: "invalid_request_error", message: "根据相关法律法规，此次请求有关信息不予显示。", moderation: true},
		{name: "ifly-message-unprocessable", statusCode: 422, errorType: "invalid_request_error", message: "根据相关法律法规，有关信息不予显示。", moderation: true},
		{name: "ifly-message-ok-is-not-a-refusal", statusCode: 200, message: "根据相关法律法规，有关信息不予显示。", want: common.GetPointer(false)},
		{name: "partial-ifly-message", statusCode: 403, message: "根据相关法律法规，请稍后再试。", want: common.GetPointer(false)},
		{name: "unrelated-policy-message", statusCode: 400, message: "This request violates policy"},
		{name: "nonexact-code", statusCode: 400, code: "content_filter_timeout"},
		{name: "nonexact-type", statusCode: 403, errorType: "prompt_blocked_extra", want: common.GetPointer(false)},
		{name: "unauthorized-cannot-be-hidden-by-policy", statusCode: 401, code: "content_filter", errorType: "content_filter_error", message: "根据相关法律法规，有关信息不予显示。", want: common.GetPointer(false)},
		{name: "balance-cannot-be-hidden-by-policy", statusCode: 402, code: "content_filter", errorType: "content_filter_error", message: "根据相关法律法规，有关信息不予显示。", want: common.GetPointer(false)},
		{name: "rate-limit-cannot-be-hidden-by-policy", statusCode: 429, code: "content_filter", errorType: "content_filter_error", message: "根据相关法律法规，有关信息不予显示。", want: common.GetPointer(false)},
		{name: "server-error-cannot-be-hidden-by-policy", statusCode: 500, code: "content_filter", errorType: "content_filter_error", message: "根据相关法律法规，有关信息不予显示。", want: common.GetPointer(false)},
		{name: "service-unavailable-cannot-be-hidden-by-policy", statusCode: 503, code: "content_filter", errorType: "content_filter_error", message: "根据相关法律法规，有关信息不予显示。", want: common.GetPointer(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			status := relaycommon.NewStreamStatus()
			status.SetEndReason(tc.reason, tc.endErr)
			if tc.reason == relaycommon.StreamEndReasonHandlerStop && tc.endErr != nil {
				// StreamResult.Stop records its terminal error in addition to EndError.
				status.RecordError(tc.endErr.Error())
			}
			if tc.softError {
				status.RecordError("upstream error")
			}
			info := &relaycommon.RelayInfo{IsStream: true, StreamStatus: status}
			apiErr := tc.apiErr
			if tc.statusCode > 0 {
				apiErr = kittypes.WithOpenAIError(kittypes.OpenAIError{Code: tc.code, Type: tc.errorType, Message: tc.message}, tc.statusCode)
			}
			assert.Equal(t, tc.moderation, perfmetrics.IsContentModerationError(apiErr))
			assert.Equal(t, tc.want, perfmetrics.RelayObservation(ctx, info, apiErr))
		})
	}
}
