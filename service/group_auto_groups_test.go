package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRequestAutoGroupsTest(t *testing.T) {
	t.Helper()
	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","default","svip"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","svip":"SVIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1,"svip":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
}

func newRequestAutoGroupsContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestGetRequestAutoGroupsInheritedListIsNotLimited(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"vip", "default", "svip"}, groups)
}

func TestGetRequestAutoGroupsFiltersBeforeApplyingCurrentLimit(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"revoked", "vip", "default", "svip"})
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"vip", "default"}, groups)
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("1"))
	assert.Equal(t, []string{"vip"}, GetRequestAutoGroups(ctx, "default"))
}

func TestGetRequestAutoGroupsDoesNotFallBackAfterPermissionChange(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip"})
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Empty(t, groups)
}

func TestAccountGroupRestrictionsApplyToCatalogAndAutoRouting(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	const restrictedID, blockedID = 504, 505
	policy := ratio_setting.GetGroupRatioSetting().UserAllowedGroups
	original := policy.MarshalJSONString()
	t.Cleanup(func() { require.NoError(t, policy.UnmarshalJSON([]byte(original))) })
	raw := `{"504":["vip","svip","private"],"505":[]}`
	require.NoError(t, ratio_setting.CheckUserAllowedGroups([]byte(raw)))
	require.NoError(t, config.UpdateConfigFromMap(ratio_setting.GetGroupRatioSetting(), map[string]string{"user_allowed_groups": raw}))
	exported, err := config.ConfigToMap(ratio_setting.GetGroupRatioSetting())
	require.NoError(t, err)
	require.JSONEq(t, raw, exported["user_allowed_groups"])
	ctx := newRequestAutoGroupsContext()
	ctx.Set("id", restrictedID)

	assert.Equal(t, map[string]string{"vip": "VIP", "svip": "SVIP"}, GetAccountVisibleGroups(restrictedID, "default"))
	assert.False(t, IsAccountSelectableGroup(restrictedID, "default", "private"))
	assert.False(t, AccountGroupAllowed(restrictedID, "default"))
	assert.False(t, AccountGroupAllowed(restrictedID, "auto"))
	assert.True(t, HasAccountGroupRestriction(blockedID))
	assert.Empty(t, GetAccountVisibleGroups(blockedID, "default"))
	assert.False(t, HasAccountGroupRestriction(0))
	assert.Len(t, GetAccountVisibleGroups(0, "default"), 3)
	assert.True(t, IsAccountSelectableGroup(506, "default", "default"))
	assert.Equal(t, []string{"vip", "svip"}, GetRequestAutoGroups(ctx, "default"))

	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"private", "default", "svip", "svip", "vip"})
	assert.Equal(t, []string{"svip", "vip"}, GetRequestAutoGroups(ctx, "default"))
	require.NoError(t, policy.UnmarshalJSON([]byte(`{"504":[]}`)))
	assert.Empty(t, GetRequestAutoGroups(ctx, "default"))
	for _, invalid := range []string{`null`, `{"504":42}`, `{"504":null}`} {
		require.Error(t, ratio_setting.CheckUserAllowedGroups([]byte(invalid)))
		require.NoError(t, config.UpdateConfigFromMap(ratio_setting.GetGroupRatioSetting(), map[string]string{"user_allowed_groups": invalid}))
		assert.Empty(t, GetAccountVisibleGroups(restrictedID, "default"))
	}
	require.NoError(t, policy.UnmarshalJSON([]byte(`{}`)))
	assert.Equal(t, []string{"default", "svip"}, GetRequestAutoGroups(ctx, "default"))
}
