package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var listBillingRecords = func(user entity.User, page int, pageSize int) (service.BillingRecordListResult, error) {
	return service.ListBillingRecordsWithDB(manager.GetManager().GetDB(), user, page, pageSize)
}

func BillingRecordList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}

	if strings.EqualFold(strings.TrimSpace(c.Query("scope")), "self") {
		operator.Role = "user"
	}

	result, err := listBillingRecords(operator, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取账单列表失败",
		})
		return
	}

	c.JSON(http.StatusOK, result)
}
