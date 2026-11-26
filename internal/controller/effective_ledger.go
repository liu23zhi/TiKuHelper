package controller

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"net/http"
	"strconv"
)

func EffectiveLedger(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	actor, _ := currentOperator(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"message": "无效用户编号"})
		return
	}
	var data any
	if c.Request.Method == http.MethodPost {
		var in service.EffectiveCorrection
		if c.ShouldBindJSON(&in) != nil {
			c.JSON(400, gin.H{"message": "账务修正参数错误"})
			return
		}
		data, err = service.CorrectEffectiveLedger(userLevelDB(c), actor, int32(id), in)
	} else {
		cursor, e := strconv.ParseInt(c.DefaultQuery("beforeBill", "0"), 10, 32)
		if e != nil || cursor < 0 {
			c.JSON(400, gin.H{"message": "账单游标无效"})
			return
		}
		data, err = service.ReviewEffectiveLedger(userLevelDB(c), actor, int32(id), int32(cursor))
	}
	writeEffectiveLedgerResult(c, data, err)
}

func EffectiveLedgerRebuild(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	actor, _ := currentOperator(c)
	var in service.EffectiveRebuildInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"message": "重建参数错误"})
		return
	}
	data, err := service.RebuildEffectiveLevels(userLevelDB(c), actor, in)
	writeEffectiveLedgerResult(c, data, err)
}
func writeEffectiveLedgerResult(c *gin.Context, data any, err error) {

	if err != nil {
		status, msg := 500, "读取或保存有效金额失败"
		switch {
		case errors.Is(err, service.ErrLedgerInput):
			status, msg = 400, err.Error()
		case errors.Is(err, service.ErrLedgerStale):
			status, msg = 409, err.Error()
		case errors.Is(err, gorm.ErrRecordNotFound):
			status, msg = 404, "找不到用户或指定来源"
		case errors.Is(err, service.ErrTicketRefundForbidden):
			status, msg = 403, "仅管理员可操作"
		}
		c.JSON(status, gin.H{"message": msg})
		return
	}
	c.JSON(200, gin.H{"data": data})
}
