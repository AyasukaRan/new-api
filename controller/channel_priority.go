package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetChannelPriorityOrder(c *gin.Context) {
	order, err := model.GetChannelPriorityOrder()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": order})
}

func ReorderChannelPriority(c *gin.Context) {
	var move model.ChannelPriorityMove
	if err := common.DecodeJson(c.Request.Body, &move); err != nil {
		common.ApiErrorT(c, "Invalid channel priority move")
		return
	}
	order, err := model.ReorderChannelPriority(move)
	if err != nil {
		if errors.Is(err, model.ErrChannelPriorityConflict) || errors.Is(err, model.ErrChannelPriorityInvalid) || errors.Is(err, model.ErrChannelPriorityRange) {
			common.ApiErrorT(c, err.Error())
		} else {
			common.ApiError(c, err)
		}
		return
	}
	if !move.Preview && len(order.Changes) > 0 {
		model.InitChannelCache()
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": order})
}

func GetGroupKeyRouting(c *gin.Context) {
	order, err := model.GetGroupKeyRouting(c.Query("group"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, order)
}

func SetGroupKeyRouting(c *gin.Context) {
	var update model.GroupKeyRoutingUpdate
	if err := common.DecodeJson(c.Request.Body, &update); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	order, err := model.SetGroupKeyRouting(update)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "channel.group_key_routing", map[string]any{
		"group": update.Group,
		"count": len(update.Items),
	})
	model.InitChannelCache()
	common.ApiSuccess(c, order)
}
