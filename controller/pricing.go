package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	return model.FilterPricingByUsableGroups(pricing, usableGroup)
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	// The pricing route's auth middleware supplies the current account group.
	group := c.GetString("user_group")
	groupRatio := ratio_setting.GetGroupRatioCopy()
	usableGroup := service.GetUserVisibleGroups(group)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	for g := range groupRatio {
		if _, ok := usableGroup[g]; !ok {
			delete(groupRatio, g)
			continue
		}
		if ratio, ok := ratio_setting.GetGroupGroupRatio(group, g); ok && group != "" {
			groupRatio[g] = ratio
		}
	}
	vendors := model.GetVendors()
	endpoints := model.GetSupportedEndpointMap()
	if c.GetInt("role") < common.RoleAdminUser {
		vendorIDs := make(map[int]bool)
		visibleEndpoints := make(map[string]common.EndpointInfo)
		for _, item := range pricing {
			vendorIDs[item.VendorID] = true
			for _, kind := range item.SupportedEndpointTypes {
				if info, exists := endpoints[string(kind)]; exists {
					visibleEndpoints[string(kind)] = info
				}
			}
		}
		visibleVendors := make([]model.PricingVendor, 0, len(vendorIDs))
		for _, vendor := range vendors {
			if vendorIDs[vendor.ID] {
				visibleVendors = append(visibleVendors, vendor)
			}
		}
		vendors, endpoints = visibleVendors, visibleEndpoints
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            vendors,
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": endpoints,
		"auto_groups":        service.GetUserAutoGroup(group),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
