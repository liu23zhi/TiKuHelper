package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func riskOffhoursAdmin(c *gin.Context) (*gorm.DB, int32, bool) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return nil, 0, false
	}
	operator, ok := currentOperator(c)
	if !ok {
		return nil, 0, false
	}
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "获取风控时段失败")
		return nil, 0, false
	}
	return db, operator.ID, true
}

func RiskOffhoursWindowGet(c *gin.Context) {
	db, _, ok := riskOffhoursAdmin(c)
	if !ok {
		return
	}
	window, err := service.GetRiskOffhoursWindow(db)
	if err != nil {
		writeRiskAdminError(c, err, "获取风控时段失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": window})
}

func RiskOffhoursWindowPut(c *gin.Context) {
	db, actor, ok := riskOffhoursAdmin(c)
	if !ok {
		return
	}
	var input struct {
		StartHour *int `json:"startHour"`
		EndHour   *int `json:"endHour"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.StartHour == nil || input.EndHour == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "必须同时提供整数 startHour 和 endHour"})
		return
	}
	window, err := service.UpdateRiskOffhoursWindow(db, *input.StartHour, *input.EndHour, actor)
	if err != nil {
		writeRiskAdminError(c, err, "保存风控时段失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": window})
}

func RiskOffhoursWindowReset(c *gin.Context) {
	db, actor, ok := riskOffhoursAdmin(c)
	if !ok {
		return
	}
	window, err := service.ResetRiskOffhoursWindow(db, actor)
	if err != nil {
		writeRiskAdminError(c, err, "重置风控时段失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": window})
}
