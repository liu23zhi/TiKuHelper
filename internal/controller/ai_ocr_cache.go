package controller

import (
	"bytes"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/util"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"gorm.io/gorm"
)

const (
	maxAIOCRCacheImageBytes  = 10 * 1024 * 1024
	maxAIOCRCacheImageSide   = 32768
	maxAIOCRCacheImagePixels = 25_000_000
)

var newAIOCRCacheImageHTTPClient = func() *http.Client { return util.NewSafeHTTPClient(15 * time.Second) }

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

// AIOCRCacheImage serves only the image referenced by a stored OCR cache row.
// The URL comes from the database, is revalidated against SSRF, and is fetched
// with the same bounded client and source headers as question-image downloads.
func AIOCRCacheImage(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	urlHash := strings.TrimSpace(c.Param("url_hash"))
	if len(urlHash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "图片缓存标识无效"})
		return
	}
	if _, err := hex.DecodeString(urlHash); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "图片缓存标识无效"})
		return
	}
	db := aiOCRCacheDatabase(c)
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "图片缓存数据库不可用"})
		return
	}
	var entry entity.AIOCRCache
	if err := db.Where("url_hash = ?", urlHash).First(&entry).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"message": "OCR 缓存记录不存在或未保存图片 URL"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "读取 OCR 缓存图片失败"})
		}
		return
	}
	if strings.TrimSpace(entry.URL) == "" {
		c.JSON(http.StatusNotFound, gin.H{"message": "该历史缓存记录未保存图片 URL"})
		return
	}
	address, err := uapi.ValidateWebURL(entry.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "缓存中的图片 URL 不安全，无法读取"})
		return
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, address, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "缓存中的图片 URL 无效"})
		return
	}
	request.Header.Set("Referer", "https://mooc1.chaoxing.com/")
	request.Header.Set("Origin", "https://mooc1.chaoxing.com")
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	response, err := newAIOCRCacheImageHTTPClient().Do(request)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "图片源暂不可用"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		c.JSON(http.StatusBadGateway, gin.H{"message": "图片源暂不可用"})
		return
	}
	if response.ContentLength > maxAIOCRCacheImageBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "图片大小超过预览限制"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAIOCRCacheImageBytes+1))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "读取图片源失败"})
		return
	}
	if len(body) == 0 {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"message": "图片源未返回有效图片"})
		return
	}
	if len(body) > maxAIOCRCacheImageBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "图片大小超过预览限制"})
		return
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxAIOCRCacheImageSide || config.Height > maxAIOCRCacheImageSide || int64(config.Width)*int64(config.Height) > maxAIOCRCacheImagePixels {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"message": "图片源未返回有效图片"})
		return
	}
	contentType := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp", "bmp": "image/bmp", "tiff": "image/tiff"}[format]
	if contentType == "" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"message": "图片格式不支持预览"})
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", strconv.Itoa(len(body)))
	c.Header("Content-Disposition", "inline")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, contentType, body)
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

func AIOCRCacheBatchDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var request service.AIOCRCacheBatchDeleteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "批量删除参数无效"})
		return
	}
	deleted, err := service.DeleteAIOCRCacheBatchWithDB(aiOCRCacheDatabase(c), request)
	if err != nil {
		if errors.Is(err, service.ErrAIOCRCacheFilterInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "批量删除图片 OCR 缓存失败"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}
