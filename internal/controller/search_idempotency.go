package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

var searchIdempotencyWaitLimit = 30 * time.Second

// SearchWithIdempotency preserves keyless search semantics and durable keyed execution.
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
	auth, session, err := service.ResolveSearchTokenWithScriptIdentity(db, c.GetHeader("Authorization"), c.GetHeader("X-Script-Identity"), c.ClientIP())
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请提供有效访问令牌"})
		return
	}
	rawBody := readAndRestoreRequestBody(c)
	path := c.Request.URL.EscapedPath()
	requestFingerprint := []byte(strings.Join([]string{c.Request.Method, path, c.Request.URL.RawQuery, rawBody}, "\n"))
	scope := fmt.Sprintf("sync-search:user:%d:token:%d", auth.User.ID, auth.AccessToken.ID)
	if session.ID > 0 {
		scope += ":script-identity:" + session.IdentityHash
	}
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

	// Preserve the existing keyed deadline behavior on the real HTTP writer.
	clearAIResponseWriteDeadline(c)
	worker, recorder, _ := snapshotSearchExecution(c, rawBody, context.WithoutCancel(c.Request.Context()))
	// The admitted producer must not inherit the observer-bound DB session.
	executionDB := withDatabasePriority(db, worker.Request.Context(), dbqueue.PriorityHigh)
	observeSearchExecution(c, nil, func() searchExecutionResult {
		completed := false
		defer func() {
			if !completed {
				if err := service.MarkIdempotencyUnknownWithDB(executionDB, record.ID); err != nil {
					logger.SysError("搜题幂等结果待核对状态保存失败")
				}
			}
		}()
		executeSearch(worker)
		result := bufferedSearchExecutionResult(recorder)
		// A detached observer must never commit a recorder's empty default200.
		if len(result.body) == 0 {
			return unavailableSearchExecutionResult()
		}
		if err := service.CompleteIdempotencyWithDB(executionDB, record.ID, result.status, result.body); err != nil {
			return unavailableSearchExecutionResult()
		}
		completed = true
		return result
	})
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
