package service

import (
	"maps"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// GetSelectedRequestGroup returns the concrete routing group without replacing
// the token's auto selector. An unresolved auto selection stays empty.
func GetSelectedRequestGroup(c *gin.Context) string {
	group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if group == "auto" {
		return common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	}
	return group
}

func GetUserUsableGroups(userGroup string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" {
		specialSettings, b := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup.Get(userGroup)
		if b {
			// 处理特殊可用分组
			for specialGroup, desc := range specialSettings {
				if after, ok := strings.CutPrefix(specialGroup, "-:"); ok {
					// 移除分组
					groupToRemove := after
					delete(groupsCopy, groupToRemove)
				} else if after, ok := strings.CutPrefix(specialGroup, "+:"); ok {
					// 添加分组
					groupToAdd := after
					groupsCopy[groupToAdd] = desc
				} else {
					// 直接添加分组
					groupsCopy[specialGroup] = desc
				}
			}
		}
		// 如果userGroup不在UserUsableGroups中，返回UserUsableGroups + userGroup。
		// 描述是英文源文本，由 web 控制台翻译；管理员配置的描述原样显示。
		if _, ok := groupsCopy[userGroup]; !ok {
			groupsCopy[userGroup] = "User group"
		}
	}
	return groupsCopy
}

// An omitted account inherits its normal grants; a present empty list denies all groups.
func AccountGroupAllowed(userID int, group string) bool {
	allowed, restricted := ratio_setting.GetGroupRatioSetting().UserAllowedGroups.Get(userID)
	return !restricted || slices.Contains(allowed, group)
}

func HasAccountGroupRestriction(userID int) bool {
	_, restricted := ratio_setting.GetGroupRatioSetting().UserAllowedGroups.Get(userID)
	return restricted
}

func GetAccountUsableGroups(userID int, userGroup string) map[string]string {
	groups := GetUserUsableGroups(userGroup)
	if allowed, restricted := ratio_setting.GetGroupRatioSetting().UserAllowedGroups.Get(userID); restricted {
		maps.DeleteFunc(groups, func(group, _ string) bool { return !slices.Contains(allowed, group) })
	}
	return groups
}

// GetAccountVisibleGroups returns the routes offered by the catalog and group
// pickers. The optional auto selector has no group ratio of its own.
func GetAccountVisibleGroups(userID int, userGroup string) map[string]string {
	groups := GetAccountUsableGroups(userID, userGroup)
	for group := range groups {
		if group != "auto" && !ratio_setting.ContainsGroupRatio(group) {
			delete(groups, group)
		}
	}
	return groups
}

func GroupInUserUsableGroups(userGroup, groupName string) bool {
	_, ok := GetUserUsableGroups(userGroup)[groupName]
	return ok
}

func IsAccountSelectableGroup(userID int, userGroup, groupName string) bool {
	if groupName == "" || groupName == "auto" {
		return false
	}
	return AccountGroupAllowed(userID, groupName) && GroupInUserUsableGroups(userGroup, groupName) && ratio_setting.ContainsGroupRatio(groupName)
}

func GetAccountAutoGroups(userID int, userGroup string) []string {
	groups := setting.GetAutoGroups()
	return accountAutoGroups(userID, userGroup, groups, len(groups))
}

func accountAutoGroups(userID int, userGroup string, groups []string, limit int) []string {
	visible := GetAccountVisibleGroups(userID, userGroup)
	filtered := make([]string, 0, min(len(groups), limit))
	for _, group := range groups {
		if _, ok := visible[group]; !ok || group == "auto" || group == "" {
			continue
		}
		filtered = append(filtered, group)
		delete(visible, group)
		if len(filtered) == limit {
			break
		}
	}
	return filtered
}

// GetRequestAutoGroups resolves the ordered Auto groups for the current token.
// The absence of the context value means that the token inherits the complete
// global Auto list; a present (even empty) value is an explicit token snapshot.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return GetAccountAutoGroups(c.GetInt("id"), userGroup)
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	return accountAutoGroups(c.GetInt("id"), userGroup, groups, setting.GetMaxTokenAutoGroups())
}

// GetGroupsEnabledModels 按 groups 顺序获取各分组启用的模型并去重
func GetGroupsEnabledModels(groups []string) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}

// GetUserGroupRatio 获取用户使用某个分组的倍率
// userGroup 用户分组
// group 需要获取倍率的分组
func GetUserGroupRatio(userGroup, group string) float64 {
	ratio, ok := ratio_setting.GetGroupGroupRatio(userGroup, group)
	if ok {
		return ratio
	}
	return ratio_setting.GetGroupRatio(group)
}
