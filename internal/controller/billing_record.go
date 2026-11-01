package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
)

var listBillingRecords = func(user entity.User, query service.BillingRecordListQuery, page int, pageSize int) (service.BillingRecordListResult, error) {
	return service.ListBillingRecordsWithDB(manager.GetManager().GetDB(), user, query, page, pageSize)
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

	filter := service.BillingRecordListQuery{
		RequestContext: c.Request.Context(),
		Keyword:        util.NormalizeSearchKeyword(c.Query("keyword")),
		Type:           strings.TrimSpace(c.Query("type")),
		RequestID:      strings.TrimSpace(c.Query("requestId")),
		Provider:       strings.TrimSpace(c.Query("provider")),
		SearchMode:     strings.TrimSpace(c.Query("searchMode")),
		ClientIP:       strings.TrimSpace(c.Query("clientIp")),
	}
	createdFrom, createdFromErr := parseListFilterTime(c.Query("createdFrom"), false)
	createdTo, createdToErr := parseListFilterTime(c.Query("createdTo"), true)
	userID, userIDErr := parseOptionalPositiveInt32(c.Query("userId"))
	if createdFromErr != nil || createdToErr != nil || userIDErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数格式无效"})
		return
	}
	if err := validateListFilterTimeRange(createdFrom, createdTo, "创建时间"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	filter.UserID, filter.CreatedFrom, filter.CreatedTo = userID, createdFrom, createdTo

	result, err := listBillingRecords(operator, filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取账单列表失败",
		})
		return
	}

	c.JSON(http.StatusOK, result)
}
