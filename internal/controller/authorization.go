package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func requireAdmin(c *gin.Context) bool {
	operator, ok := currentOperator(c)
	if !ok {
		return false
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限执行该操作"})
		return false
	}
	return true
}
