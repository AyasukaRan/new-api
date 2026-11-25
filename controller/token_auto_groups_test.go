package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureTokenAutoGroupsTest(t *testing.T, maxCount string, autoGroups string) {
	t.Helper()
	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateMaxTokenAutoGroups(maxCount))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(autoGroups))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(stringInt(originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
}

func stringInt(value int) string {
	return fmt.Sprintf("%d", value)
}

func setupTokenAutoGroupsControllerTest(t *testing.T) *model.User {
	t.Helper()
	db := setupTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	user := &model.User{
		Id:       101,
		Username: "token-auto-user",
		Password: "password",
		Group:    "default",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func baseAutoTokenRequest(name string) map[string]any {
	return map[string]any{
		"name":              name,
		"expired_time":      -1,
		"remain_quota":      0,
		"unlimited_quota":   true,
		"group":             "auto",
		"cross_group_retry": true,
	}
}

func newTokenAutoGroupsAuthenticatedContext(t *testing.T, method string, target string, body any, userID int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	ctx, recorder := newAuthenticatedContext(t, method, target, body, userID)
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	return ctx, recorder
}

func TestAddTokenEmptyAutoGroupsInheritGlobalAuto(t *testing.T) {
	tests := []struct {
		name         string
		includeField bool
		value        any
	}{
		{name: "omitted"},
		{name: "null", includeField: true, value: nil},
		{name: "empty array", includeField: true, value: []string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			request := baseAutoTokenRequest("create-" + test.name)
			if test.includeField {
				request["auto_groups"] = test.value
			}

			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
			AddToken(ctx)

			response := decodeAPIResponse(t, recorder)
			require.True(t, response.Success, response.Message)
			var token model.Token
			require.NoError(t, model.DB.Where("name = ?", request["name"]).First(&token).Error)
			assert.Empty(t, token.AutoGroups)
			assert.True(t, token.CrossGroupRetry)
			payload, err := common.Marshal(buildMaskedTokenResponse(&token))
			require.NoError(t, err)
			var responseData map[string]any
			require.NoError(t, common.Unmarshal(payload, &responseData))
			assert.Nil(t, responseData["auto_groups"])
		})
	}
}

func TestAddTokenPersistsOrderedAutoGroupsSnapshot(t *testing.T) {
	configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
	user := setupTokenAutoGroupsControllerTest(t)
	request := baseAutoTokenRequest("ordered-snapshot")
	request["auto_groups"] = []string{"vip", "default"}

	ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
	AddToken(ctx)
	require.True(t, decodeAPIResponse(t, recorder).Success)

	var token model.Token
	require.NoError(t, model.DB.Where("name = ?", "ordered-snapshot").First(&token).Error)
	assert.JSONEq(t, `["vip","default"]`, token.AutoGroups)

	getCtx, getRecorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodGet, "/api/token/"+stringInt(token.Id), nil, user.Id)
	getCtx.Params = append(getCtx.Params, gin.Param{Key: "id", Value: stringInt(token.Id)})
	GetToken(getCtx)
	getResponse := decodeAPIResponse(t, getRecorder)
	require.True(t, getResponse.Success)
	var data struct {
		AutoGroups []string `json:"auto_groups"`
	}
	require.NoError(t, common.Unmarshal(getResponse.Data, &data))
	assert.Equal(t, []string{"vip", "default"}, data.AutoGroups)
}

func TestUpdateTokenAutoGroupsTriStateAndNonAutoCleanup(t *testing.T) {
	tests := []struct {
		name               string
		includeField       bool
		value              any
		group              string
		expectedAutoGroups string
		expectedRetry      bool
	}{
		{name: "omitted preserves", group: "auto", expectedAutoGroups: `["vip","default"]`, expectedRetry: true},
		{name: "null inherits", includeField: true, value: nil, group: "auto", expectedRetry: true},
		{name: "empty inherits", includeField: true, value: []string{}, group: "auto", expectedRetry: true},
		{name: "non auto clears and disables retry", includeField: true, value: []string{"vip"}, group: "default"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			token := seedToken(t, model.DB, user.Id, "update-auto", "update-auto-key")
			token.Group = "auto"
			token.CrossGroupRetry = true
			require.NoError(t, token.SetAutoGroups([]string{"vip", "default"}))
			require.NoError(t, model.DB.Save(token).Error)

			request := baseAutoTokenRequest("updated-auto")
			request["id"] = token.Id
			request["status"] = common.TokenStatusEnabled
			request["group"] = test.group
			if test.includeField {
				request["auto_groups"] = test.value
			}
			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPut, "/api/token/", request, user.Id)
			UpdateToken(ctx)
			response := decodeAPIResponse(t, recorder)
			require.True(t, response.Success, response.Message)

			var updated model.Token
			require.NoError(t, model.DB.First(&updated, token.Id).Error)
			if test.expectedAutoGroups == "" {
				assert.Empty(t, updated.AutoGroups)
			} else {
				assert.JSONEq(t, test.expectedAutoGroups, updated.AutoGroups)
			}
			assert.Equal(t, test.expectedRetry, updated.CrossGroupRetry)
		})
	}
}

func TestAddTokenRejectsInvalidAutoGroups(t *testing.T) {
	tests := []struct {
		name     string
		maxCount string
		groups   []string
	}{
		{name: "over limit", maxCount: "1", groups: []string{"default", "vip"}},
		{name: "duplicate", maxCount: "5", groups: []string{"default", "default"}},
		{name: "auto pseudo group", maxCount: "5", groups: []string{"auto"}},
		{name: "unavailable", maxCount: "5", groups: []string{"missing"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureTokenAutoGroupsTest(t, test.maxCount, `["default","vip"]`)
			user := setupTokenAutoGroupsControllerTest(t)
			request := baseAutoTokenRequest("invalid-" + test.name)
			request["auto_groups"] = test.groups

			ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
			AddToken(ctx)

			response := decodeAPIResponse(t, recorder)
			assert.False(t, response.Success)
			var count int64
			require.NoError(t, model.DB.Model(&model.Token{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestGetTokenAutoGroupsReturnsFullFilteredGlobalOrderAndLimit(t *testing.T) {
	configureTokenAutoGroupsTest(t, "1", `["vip","missing","default"]`)
	user := setupTokenAutoGroupsControllerTest(t)

	ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodGet, "/api/token/auto-groups", nil, user.Id)
	GetTokenAutoGroups(ctx)

	response := decodeAPIResponse(t, recorder)
	require.True(t, response.Success, response.Message)
	var data struct {
		Groups   []string `json:"groups"`
		MaxCount int      `json:"max_count"`
	}
	require.NoError(t, common.Unmarshal(response.Data, &data))
	assert.Equal(t, []string{"vip", "default"}, data.Groups)
	assert.Equal(t, 1, data.MaxCount)
}

func restrictTestAccountGroups(t *testing.T, userID int, groups ...string) {
	t.Helper()
	access := ratio_setting.GetGroupRatioSetting().UserAllowedGroups
	original := access.ReadAll()
	access.Set(userID, groups)
	t.Cleanup(func() {
		access.Clear()
		access.AddAll(original)
	})
}

func TestRestrictedAccountTokenGroupAssignment(t *testing.T) {
	configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
	user := setupTokenAutoGroupsControllerTest(t)
	restrictTestAccountGroups(t, user.Id, "vip")
	for _, group := range []string{"", "default", "auto", "vip"} {
		request := baseAutoTokenRequest("restricted-" + group)
		request["group"] = group
		ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPost, "/api/token/", request, user.Id)
		AddToken(ctx)
		assert.Equal(t, group == "vip", decodeAPIResponse(t, recorder).Success, "create group %q", group)
	}
	var token model.Token
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).Take(&token).Error)
	require.Equal(t, "vip", token.Group)
	request := baseAutoTokenRequest("reassign")
	request["id"], request["group"] = token.Id, "default"
	ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodPut, "/api/token/", request, user.Id)
	UpdateToken(ctx)
	assert.False(t, decodeAPIResponse(t, recorder).Success)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, "vip", token.Group)

	legacy := seedToken(t, model.DB, user.Id, "old default", "restricted-test-key")
	ctx, recorder = newTokenAutoGroupsAuthenticatedContext(t, http.MethodPut, "/api/token/?status_only=true",
		map[string]any{"id": legacy.Id, "status": common.TokenStatusDisabled}, user.Id)
	UpdateToken(ctx)
	require.True(t, decodeAPIResponse(t, recorder).Success)
	require.NoError(t, model.DB.First(legacy, legacy.Id).Error)
	assert.Equal(t, common.TokenStatusDisabled, legacy.Status)
}

func TestRestrictedAccountMarketplaceAndModelDiscovery(t *testing.T) {
	const userID = 101
	db := setupModelListControllerTestDB(t)
	configureTokenAutoGroupsTest(t, "5", `["public","official"]`)
	restrictTestAccountGroups(t, userID, "official")
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"official":"Official","public":"Also public","auto":"Auto"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"official":1,"public":1}`))
	withSelfUseModeEnabled(t)
	oldPrices := ratio_setting.ChannelModelPricing2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(oldPrices))
		model.InvalidatePricingCache()
	})
	require.NoError(t, ratio_setting.UpdateChannelModelPricingByJSONString(`{"shared-scope-model":{"1":{"model_price":1},"2":{"model_price":99}}}`))
	for _, channel := range []model.Channel{
		{Id: 1, Name: "official", Group: "official", Models: "shared-scope-model", Key: "fixture", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled},
		{Id: 2, Name: "other", Group: "public", Models: "shared-scope-model,gpt-4", Key: "fixture", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled},
	} {
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, channel.AddAbilities(db))
	}
	model.InvalidatePricingCache()
	ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodGet, "/api/pricing", nil, userID)
	GetPricing(ctx)
	var response struct {
		Success bool               `json:"success"`
		Data    []model.Pricing    `json:"data"`
		Groups  map[string]string  `json:"usable_group"`
		Ratios  map[string]float64 `json:"group_ratio"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, map[string]string{"official": "Official"}, response.Groups)
	assert.Equal(t, map[string]float64{"official": 1}, response.Ratios)
	require.Len(t, response.Data, 1)
	row := response.Data[0]
	assert.Equal(t, "shared-scope-model", row.ModelName)
	assert.Equal(t, []string{"official"}, row.EnableGroup)
	require.Len(t, row.ChannelPricingVariants, 1)
	assert.Equal(t, []string{"official"}, row.ChannelPricingVariants[0].EnableGroup)
	assert.Equal(t, 1.0, row.ChannelPricingVariants[0].ModelPrice)

	for _, name := range []string{"shared-scope-model", "gpt-4"} {
		ctx, recorder := newTokenAutoGroupsAuthenticatedContext(t, http.MethodGet, "/v1/models/"+name, nil, userID)
		common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "official")
		ctx.Params = gin.Params{{Key: "model", Value: name}}
		RetrieveModel(ctx, constant.ChannelTypeOpenAI)
		var item struct {
			ID    string `json:"id"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &item))
		if name == "gpt-4" {
			assert.Empty(t, item.ID)
			assert.Equal(t, "model_not_found", item.Error.Code)
		} else {
			assert.Equal(t, name, item.ID)
		}
	}
}
