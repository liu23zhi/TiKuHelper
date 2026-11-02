package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

var aiOCRCacheDatabase = func(c *gin.Context) *gorm.DB {
	if currentManager := manager.GetManager(); currentManager != nil {
		return withDatabasePriority(currentManager.GetDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	}
	return nil
}

func aiOCRCacheFilters(c *gin.Context) service.AIOCRCacheListQuery {
	return service.AIOCRCacheListQuery{
		Keyword: c.Query("keyword"), ContentType: c.Query("contentType"),
		UpdatedFrom: c.Query("updatedFrom"), UpdatedTo: c.Query("updatedTo"),
	}
}

func aiOCRCacheListQuery(c *gin.Context) (service.AIOCRCacheListQuery, int, int, error) {
	filters := aiOCRCacheFilters(c)
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, pageSizeErr := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if pageErr != nil || pageSizeErr != nil || page < 1 || pageSize < 1 || pageSize > 100 || page-1 > int(^uint(0)>>1)/pageSize {
		return filters, 0, 0, errors.New("分页参数格式无效")
	}
	return filters, page, pageSize, nil
}

func AIOCRCacheList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	filters, page, pageSize, err := aiOCRCacheListQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	result, err := service.ListAIOCRCacheWithDB(aiOCRCacheDatabase(c), filters, page, pageSize)
	if err != nil {
		if errors.Is(err, service.ErrAIOCRCacheFilterInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "获取图片 OCR 缓存列表失败"})
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

func AIOCRCacheStatus(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	status, err := service.GetAIOCRCacheStatusWithDB(aiOCRCacheDatabase(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取图片 OCR 缓存状态失败"})
		return
	}
	c.JSON(http.StatusOK, status)
}

func ClearAIOCRCache(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	deleted, err := service.ClearAIOCRCacheWithDB(aiOCRCacheDatabase(c), aiOCRCacheFilters(c))
	if err != nil {
		if errors.Is(err, service.ErrAIOCRCacheFilterInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "清空图片 OCR 缓存失败"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}
