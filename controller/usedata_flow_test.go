package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type flowQuotaResponse struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    []model.FlowQuotaData `json:"data"`
}

func setupFlowControllerTestDB(t *testing.T) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.QuotaData{}))
	require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Name: "east"}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 11, UserId: 1, Key: "sk-primary", Name: "primary"}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 22, UserId: 2, Key: "sk-backup", Name: "backup"}).Error)
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "default",
		ChannelID: 1,
		ModelName: "gpt-a",
		CreatedAt: 1100,
		Count:     2,
		Quota:     100,
		TokenUsed: 40,
	}).Error)
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID:    2,
		Username:  "bob",
		NodeName:  "node-b",
		TokenID:   22,
		UseGroup:  "vip",
		ChannelID: 1,
		ModelName: "gpt-b",
		CreatedAt: 1200,
		Count:     1,
		Quota:     70,
		TokenUsed: 30,
	}).Error)
}

func decodeFlowQuotaResponse(t *testing.T, recorder *httptest.ResponseRecorder) flowQuotaResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload flowQuotaResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success, payload.Message)
	return payload
}

func TestGetAllFlowQuotaDatesUsesAdminDimensions(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", common.RoleAdminUser)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow?start_timestamp=1000&end_timestamp=2000&username=bob", nil)

	GetAllFlowQuotaDates(ctx)

	payload := decodeFlowQuotaResponse(t, recorder)
	require.Len(t, payload.Data, 1)
	require.Equal(t, "bob", payload.Data[0].Username)
	require.Equal(t, "vip", payload.Data[0].UseGroup)
	require.Equal(t, "east", payload.Data[0].ChannelName)
	require.Empty(t, payload.Data[0].TokenName)
	require.Empty(t, payload.Data[0].NodeName)
}

func TestGetAllFlowQuotaDatesUsesRootDimensions(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", common.RoleRootUser)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow?start_timestamp=1000&end_timestamp=2000&username=alice", nil)

	GetAllFlowQuotaDates(ctx)

	payload := decodeFlowQuotaResponse(t, recorder)
	require.Len(t, payload.Data, 1)
	require.Equal(t, "alice", payload.Data[0].Username)
	require.Equal(t, "node-a", payload.Data[0].NodeName)
	require.Equal(t, "primary", payload.Data[0].TokenName)
	require.Equal(t, "default", payload.Data[0].UseGroup)
	require.Equal(t, "east", payload.Data[0].ChannelName)
}

func TestGetUserFlowQuotaDatesRestrictsToAuthenticatedUser(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow/self?start_timestamp=1000&end_timestamp=2000", nil)

	GetUserFlowQuotaDates(ctx)

	payload := decodeFlowQuotaResponse(t, recorder)
	require.Len(t, payload.Data, 1)
	require.Empty(t, payload.Data[0].Username)
	require.Equal(t, "primary", payload.Data[0].TokenName)
	require.Equal(t, "default", payload.Data[0].UseGroup)
	require.Empty(t, payload.Data[0].ChannelName)
}

func TestGetUserFlowQuotaDatesRejectsInvalidTimeRange(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow/self?start_timestamp=bad&end_timestamp=2000", nil)

	GetUserFlowQuotaDates(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload flowQuotaResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.False(t, payload.Success)
	require.Equal(t, "invalid start_timestamp", payload.Message)
}

func TestSourceQuotaEndpointsScopeAndTimeRange(t *testing.T) {
	setupFlowControllerTestDB(t)
	require.NoError(t, model.DB.Model(&model.QuotaData{}).Where("user_id = ?", 1).Update("client_tool", "curl").Error)
	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
		query   string
		want    []model.SourceQuotaData
		message string
	}{
		{name: "admin all", handler: GetAllSourceQuotaDates, query: "start_timestamp=1000&end_timestamp=2000", want: []model.SourceQuotaData{{ClientTool: "curl", Count: 2, TokenUsed: 40, Quota: 100}, {ClientTool: "", Count: 1, TokenUsed: 30, Quota: 70}}},
		{name: "admin username", handler: GetAllSourceQuotaDates, query: "start_timestamp=1000&end_timestamp=2000&username=bob", want: []model.SourceQuotaData{{ClientTool: "", Count: 1, TokenUsed: 30, Quota: 70}}},
		{name: "self ignores another user and test selector", handler: GetUserSourceQuotaDates, query: "start_timestamp=1000&end_timestamp=2000&username=bob&user_id=2&source=test", want: []model.SourceQuotaData{{ClientTool: "curl", Count: 2, TokenUsed: 40, Quota: 100}}},
		{name: "empty range is array", handler: GetUserSourceQuotaDates, query: "start_timestamp=2001&end_timestamp=2002", want: []model.SourceQuotaData{}},
		{name: "invalid start", handler: GetAllSourceQuotaDates, query: "start_timestamp=bad&end_timestamp=2000", message: "invalid start_timestamp"},
		{name: "invalid end", handler: GetUserSourceQuotaDates, query: "start_timestamp=1000&end_timestamp=0", message: "invalid end_timestamp"},
		{name: "reversed range", handler: GetAllSourceQuotaDates, query: "start_timestamp=2000&end_timestamp=1000", message: "invalid time range"},
		{name: "self range limit", handler: GetUserSourceQuotaDates, query: "start_timestamp=1000&end_timestamp=2593001", message: "时间跨度不能超过 1 个月"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 1)
			ctx.Set("role", common.RoleAdminUser)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/sources?"+test.query, nil)
			test.handler(ctx)
			var response struct {
				Success bool                    `json:"success"`
				Message string                  `json:"message"`
				Data    []model.SourceQuotaData `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, test.message == "", response.Success)
			assert.Equal(t, test.message, response.Message)
			assert.Equal(t, test.want, response.Data)
		})
	}
}

func TestSourceQuotaTimeSeriesPreservesHourlyTotalsAndScope(t *testing.T) {
	setupFlowControllerTestDB(t)
	require.NoError(t, model.DB.Where("id > 0").Delete(&model.QuotaData{}).Error)
	rows := []model.QuotaData{
		{UserID: 1, Username: "alice", ModelName: "first", CreatedAt: 3600, ClientTool: "OpenAI Python SDK", Count: 2, TokenUsed: 20, Quota: 200},
		{UserID: 1, Username: "alice", ModelName: "second", CreatedAt: 3600, ClientTool: "OpenAI Python SDK", Count: 3, TokenUsed: 30, Quota: 300},
		{UserID: 2, Username: "bob", ModelName: "first", CreatedAt: 3600, ClientTool: "OpenAI Python SDK", Count: 7, TokenUsed: 70, Quota: 700},
		{UserID: 1, Username: "alice", ModelName: "first", CreatedAt: 7200, ClientTool: "OpenAI Python SDK", Count: 11, TokenUsed: 110, Quota: 1100},
		{UserID: 1, Username: "alice", ModelName: "legacy", CreatedAt: 3600, Count: 5, TokenUsed: 50, Quota: 500},
		{UserID: 1, Username: "alice", ModelName: "second", CreatedAt: 3600, Count: 8, TokenUsed: 80, Quota: 800},
		{UserID: 1, Username: "alice", ModelName: "first", CreatedAt: 7200, Count: 17, TokenUsed: 170, Quota: 1700},
		{UserID: 2, Username: "bob", ModelName: "first", CreatedAt: 7200, ClientTool: "curl", Count: 19, TokenUsed: 190, Quota: 1900},
		{UserID: 1, Username: "alice", ModelName: "first", CreatedAt: 10800, ClientTool: "OpenAI Python SDK", Count: 23, TokenUsed: 230, Quota: 2300},
	}
	require.NoError(t, model.DB.Create(&rows).Error)
	require.NoError(t, model.DB.Model(&model.QuotaData{}).Where("model_name = ?", "legacy").Update("client_tool", nil).Error)
	// Decode the public API rather than depending on the production DTO's
	// fields, so this regression fails on behavior before implementation.
	type sourceRow struct {
		ClientTool string `json:"client_tool"`
		CreatedAt  int64  `json:"created_at"`
		Count      int64  `json:"count"`
		TokenUsed  int64  `json:"token_used"`
		Quota      int64  `json:"quota"`
	}
	allSummary := []sourceRow{
		{ClientTool: "", Count: 30, TokenUsed: 300, Quota: 3000},
		{ClientTool: "OpenAI Python SDK", Count: 23, TokenUsed: 230, Quota: 2300},
		{ClientTool: "curl", Count: 19, TokenUsed: 190, Quota: 1900},
	}
	allSeries := []sourceRow{
		{ClientTool: "", CreatedAt: 3600, Count: 13, TokenUsed: 130, Quota: 1300},
		{ClientTool: "OpenAI Python SDK", CreatedAt: 3600, Count: 12, TokenUsed: 120, Quota: 1200},
		{ClientTool: "curl", CreatedAt: 7200, Count: 19, TokenUsed: 190, Quota: 1900},
		{ClientTool: "", CreatedAt: 7200, Count: 17, TokenUsed: 170, Quota: 1700},
		{ClientTool: "OpenAI Python SDK", CreatedAt: 7200, Count: 11, TokenUsed: 110, Quota: 1100},
	}
	selfSeries := []sourceRow{
		{ClientTool: "", CreatedAt: 3600, Count: 13, TokenUsed: 130, Quota: 1300},
		{ClientTool: "OpenAI Python SDK", CreatedAt: 3600, Count: 5, TokenUsed: 50, Quota: 500},
		{ClientTool: "", CreatedAt: 7200, Count: 17, TokenUsed: 170, Quota: 1700},
		{ClientTool: "OpenAI Python SDK", CreatedAt: 7200, Count: 11, TokenUsed: 110, Quota: 1100},
	}
	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
		role    int
		query   string
		want    []sourceRow
		series  bool
	}{
		{name: "default summary", handler: GetAllSourceQuotaDates, role: common.RoleAdminUser, want: allSummary},
		{name: "explicit false keeps summary", handler: GetAllSourceQuotaDates, role: common.RoleAdminUser, query: "&time_series=false", want: allSummary},
		{name: "admin hourly all users and models", handler: GetAllSourceQuotaDates, role: common.RoleAdminUser, query: "&time_series=true", want: allSeries, series: true},
		{name: "root hourly", handler: GetAllSourceQuotaDates, role: common.RoleRootUser, query: "&time_series=true", want: allSeries, series: true},
		{name: "admin hourly username filter", handler: GetAllSourceQuotaDates, role: common.RoleAdminUser, query: "&time_series=true&username=bob", want: []sourceRow{
			{ClientTool: "OpenAI Python SDK", CreatedAt: 3600, Count: 7, TokenUsed: 70, Quota: 700},
			{ClientTool: "curl", CreatedAt: 7200, Count: 19, TokenUsed: 190, Quota: 1900},
		}, series: true},
		{name: "self ignores user overrides", handler: GetUserSourceQuotaDates, role: common.RoleRootUser, query: "&time_series=true&username=bob&user_id=2&source=test", want: selfSeries, series: true},
		{name: "non admin remains scoped", handler: GetAllSourceQuotaDates, role: common.RoleCommonUser, query: "&time_series=true&username=bob", want: selfSeries, series: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 1)
			ctx.Set("role", test.role)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/sources?start_timestamp=3600&end_timestamp=10799"+test.query, nil)
			test.handler(ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool        `json:"success"`
				Data    []sourceRow `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success)
			assert.ElementsMatch(t, test.want, response.Data)
			if test.series {
				assert.Contains(t, recorder.Body.String(), `"created_at":`)
				for i := 1; i < len(response.Data); i++ {
					assert.LessOrEqual(t, response.Data[i-1].CreatedAt, response.Data[i].CreatedAt)
				}
			} else {
				assert.NotContains(t, recorder.Body.String(), `"created_at":`, "the existing summary response must not acquire timestamp fields")
			}
		})
	}
}

func TestSourceQuotaTimeSeriesRejectsInvalidBoolean(t *testing.T) {
	setupFlowControllerTestDB(t)
	for _, handler := range []struct {
		name   string
		handle gin.HandlerFunc
	}{
		{name: "admin", handle: GetAllSourceQuotaDates},
		{name: "self", handle: GetUserSourceQuotaDates},
	} {
		for _, value := range []string{"invalid", "", "2"} {
			t.Run(handler.name+"/"+value, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Set("id", 1)
				ctx.Set("role", common.RoleAdminUser)
				ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/sources?start_timestamp=1000&end_timestamp=2000&time_series="+value, nil)
				handler.handle(ctx)
				assert.Equal(t, http.StatusBadRequest, recorder.Code)
				var response struct {
					Success bool   `json:"success"`
					Message string `json:"message"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				assert.False(t, response.Success)
				assert.Equal(t, "invalid time_series", response.Message)
			})
		}
	}
}
