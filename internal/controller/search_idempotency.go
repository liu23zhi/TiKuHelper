package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

var searchIdempotencyWaitLimit = 30 * time.Second

// SearchWithIdempotency leaves the legacy keyless endpoint untouched.
func SearchWithIdempotency(c *gin.Context) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		Search(c)
		return
	}
	if len([]rune(key)) > searchTaskMaxIdempotencyKeyLength {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key 长度不能超过 128 个字符"})
		return
	}
	db := withDatabasePriority(resolveSearchPersistenceDB(), databaseRequestContext(c), dbqueue.PriorityHigh)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "搜题服务暂不可用"})
		return
	}
	var auth service.AccessTokenAuthContext
	var err error
	identity := strings.TrimSpace(c.GetHeader("X-Script-Identity"))
	if identity != "" {
		auth, _, err = service.ResolveScriptRunSessionWithDB(db, c.GetHeader("Authorization"), identity)
	} else {
		auth, err = service.ResolveAccessTokenFromBasicAuth(db, c.GetHeader("Authorization"))
		if err == nil && auth.AccessToken.ScriptID > 0 {
			err = service.ErrInvalidAccessTokenAuthorization
		}
	}
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请提供有效访问令牌"})
		return
	}
	rawBody := readAndRestoreRequestBody(c)
	path := c.Request.URL.EscapedPath()
	requestFingerprint := []byte(strings.Join([]string{c.Request.Method, path, c.Request.URL.RawQuery, rawBody}, "\n"))
	scope := fmt.Sprintf("sync-search:user:%d:token:%d", auth.User.ID, auth.AccessToken.ID)
	record, created, err := service.ClaimIdempotencyWithDB(db, scope, "search", key, requestFingerprint)
	if err != nil {
		switch err {
		case service.ErrIdempotencyConflict:
			c.JSON(http.StatusConflict, gin.H{"message": "幂等键已用于不同请求"})
		case service.ErrIdempotencyKeyInvalid:
			c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key 无效"})
		default:
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "搜题请求暂不可用"})
		}
		return
	}
	if !created {
		waitForIdempotentSearchResult(c, db, record)
		return
	}

	completed := false
	defer func() {
		if !completed {
			_ = service.MarkIdempotencyUnknownWithDB(db, record.ID)
		}
	}()
	clearAIResponseWriteDeadline(c)
	recorder := httptest.NewRecorder()
	bufferContext, _ := gin.CreateTestContext(recorder)
	searchContext := c.Copy()
	searchContext.Writer = bufferContext.Writer
	Search(searchContext)

	status, body := recorder.Code, recorder.Body.Bytes()
	if err := service.CompleteIdempotencyWithDB(db, record.ID, status, body); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "搜题已执行，但结果暂无法确认，请勿重复提交"})
		return
	}
	completed = true
	writeIdempotentSearchResponse(c, status, body)
}

func waitForIdempotentSearchResult(c *gin.Context, db *gorm.DB, record entity.IdempotencyRecord) {
	if record.Status != service.IdempotencyStatusProcessing {
		writeExistingIdempotentSearchResult(c, record)
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(searchIdempotencyWaitLimit)
	defer timer.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-timer.C:
			c.Header("Retry-After", "1")
			c.JSON(http.StatusTooEarly, gin.H{"message": "相同搜题请求仍在处理，请稍后重试"})
			return
		case <-ticker.C:
			if err := db.Session(&gorm.Session{NewDB: true}).First(&record, record.ID).Error; err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"message": "搜题结果暂不可查询"})
				return
			}
			if record.Status != service.IdempotencyStatusProcessing {
				writeExistingIdempotentSearchResult(c, record)
				return
			}
		}
	}
}

func writeExistingIdempotentSearchResult(c *gin.Context, record entity.IdempotencyRecord) {
	switch record.Status {
	case service.IdempotencyStatusSucceeded, service.IdempotencyStatusFailed:
		if record.HTTPStatus > 0 {
			writeIdempotentSearchResponse(c, record.HTTPStatus, []byte(record.ResponseJSON))
			return
		}
	case service.IdempotencyStatusProcessing:
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooEarly, gin.H{"message": "相同搜题请求仍在处理，请稍后重试"})
		return
	}
	c.JSON(http.StatusConflict, gin.H{"message": "搜题结果待核对，请勿重复提交"})
}

func writeIdempotentSearchResponse(c *gin.Context, status int, body []byte) {
	c.Data(status, "application/json; charset=utf-8", body)
}
