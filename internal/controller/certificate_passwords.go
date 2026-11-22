package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

type gatewayCertificatePasswordsRequest struct {
	Passwords *[]string `json:"passwords"`
}

var loadGatewayCertificatePasswordState = func() (service.GatewayCertificatePasswordState, error) {
	db := withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow)
	return service.GetGatewayCertificatePasswordStateWithDB(db)
}

var saveGatewayCertificatePasswords = func(passwords []string) (service.GatewayCertificatePasswordState, error) {
	return service.SetGatewayCertificatePasswordsWithDB(manager.GetManager().GetDB(), passwords)
}

func GatewayCertificatePasswordState(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	state, err := loadGatewayCertificatePasswordState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取证书密码候选失败"})
		return
	}
	c.JSON(http.StatusOK, state)
}

func UpdateGatewayCertificatePasswords(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var request gatewayCertificatePasswordsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "证书密码候选参数错误"})
		return
	}
	if request.Passwords == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "证书密码候选必须是数组；清空时请提交空数组"})
		return
	}
	state, err := saveGatewayCertificatePasswords(*request.Passwords)
	if err != nil {
		status := http.StatusInternalServerError
		message := "保存证书密码候选失败"
		if errors.Is(err, service.ErrTooManyGatewayCertificatePasswords) || errors.Is(err, service.ErrGatewayCertificatePasswordTooLong) {
			status = http.StatusBadRequest
			message = err.Error()
		}
		c.JSON(status, gin.H{"message": message})
		return
	}
	c.JSON(http.StatusOK, state)
}
