package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
)

const (
	searchTaskStatusRunning           = "running"
	searchTaskStatusSucceeded         = "succeeded"
	searchTaskStatusFailed            = "failed"
	searchTaskDefaultTTL              = 15 * time.Minute
	searchTaskMaxIdempotencyKeyLength = 128
)

var (
	errSearchTaskNotFound            = errors.New("搜题任务不存在")
	errSearchTaskForbidden           = errors.New("无权限查看搜题任务")
	errSearchTaskIdempotencyConflict = errors.New("幂等键已用于不同的搜题请求")
)

type searchTask struct {
	ID                 string
	UserID             int32
	Status             string
	Result             model.SearchResponse
	Error              *model.SearchError
	CreatedAt          time.Time
	UpdatedAt          time.Time
	IdempotencyKey     string
	RequestFingerprint string
}

type searchTaskIdempotencyIndex struct {
	UserID int32
	Key    string
}

type searchTaskStore struct {
	mu            sync.RWMutex
	tasks         map[string]searchTask
	terminalTasks map[string]struct{}
	idempotency   map[searchTaskIdempotencyIndex]string
	healthTasks   map[int32]string
	ttl           time.Duration
	now           func() time.Time
}

type searchTaskResponse struct {
	Success   bool                  `json:"success"`
	Status    string                `json:"status"`
	TaskID    string                `json:"taskId,omitempty"`
	StatusURL string                `json:"statusUrl,omitempty"`
	Result    *model.SearchResponse `json:"result,omitempty"`
	Error     *model.SearchError    `json:"error,omitempty"`
}

func newSearchTaskStore(ttl time.Duration, now func() time.Time) *searchTaskStore {
	if ttl <= 0 {
		ttl = searchTaskDefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &searchTaskStore{
		tasks:         make(map[string]searchTask),
		terminalTasks: make(map[string]struct{}),
		idempotency:   make(map[searchTaskIdempotencyIndex]string),
		healthTasks:   make(map[int32]string),
		ttl:           ttl,
		now:           now,
	}
}

var defaultSearchTaskStore = newSearchTaskStore(searchTaskDefaultTTL, time.Now)

func (s *searchTaskStore) create(userID int32, idempotencyKey string, fingerprint string, run func() (model.SearchResponse, error)) (string, bool, error) {
	now := s.now()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	if idempotencyKey != "" {
		index := searchTaskIdempotencyIndex{UserID: userID, Key: idempotencyKey}
		if existingID, ok := s.idempotency[index]; ok {
			existing, found := s.tasks[existingID]
			if found {
				if existing.RequestFingerprint != fingerprint {
					s.mu.Unlock()
					return "", false, errSearchTaskIdempotencyConflict
				}
				s.mu.Unlock()
				return existingID, true, nil
			}
			delete(s.idempotency, index)
		}
	}

	taskID := uuid.NewString()
	task := searchTask{
		ID:                 taskID,
		UserID:             userID,
		Status:             searchTaskStatusRunning,
		CreatedAt:          now,
		UpdatedAt:          now,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
	}
	s.tasks[taskID] = task
	if idempotencyKey != "" {
		s.idempotency[searchTaskIdempotencyIndex{UserID: userID, Key: idempotencyKey}] = taskID
	}
	s.mu.Unlock()

	go func() {
		result, err := runSearchTaskSafely(run)
		s.complete(taskID, result, err)
	}()
	return taskID, false, nil
}

func (s *searchTaskStore) get(taskID string, userID int32) (searchTask, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	task, ok := s.tasks[taskID]
	if !ok {
		return searchTask{}, errSearchTaskNotFound
	}
	if task.UserID != userID {
		return searchTask{}, errSearchTaskForbidden
	}
	return task, nil
}

func (s *searchTaskStore) healthProbe(userID int32) searchTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupExpiredLocked(now)
	if taskID := s.healthTasks[userID]; taskID != "" {
		if task, ok := s.tasks[taskID]; ok {
			return task
		}
		delete(s.healthTasks, userID)
	}
	task := searchTask{
		ID:        uuid.NewString(),
		UserID:    userID,
		Status:    searchTaskStatusSucceeded,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.tasks[task.ID] = task
	s.terminalTasks[task.ID] = struct{}{}
	s.healthTasks[userID] = task.ID
	return task
}

func (s *searchTaskStore) complete(taskID string, result model.SearchResponse, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return
	}
	task.Status = searchTaskStatusSucceeded
	task.Result = result
	if err != nil {
		task.Status = searchTaskStatusFailed
		task.Error = &model.SearchError{Code: "UPSTREAM_ERROR", Message: "搜题任务执行失败"}
	} else if result.Error != nil {
		task.Status = searchTaskStatusFailed
		task.Error = result.Error
	}
	task.UpdatedAt = s.now()
	s.tasks[taskID] = task
	s.terminalTasks[taskID] = struct{}{}
}

func (s *searchTaskStore) cleanupExpiredLocked(now time.Time) {
	for taskID := range s.terminalTasks {
		task, ok := s.tasks[taskID]
		if !ok || task.Status == searchTaskStatusRunning {
			delete(s.terminalTasks, taskID)
			continue
		}
		if now.Sub(task.UpdatedAt) < s.ttl {
			continue
		}
		delete(s.tasks, taskID)
		delete(s.terminalTasks, taskID)
		if s.healthTasks[task.UserID] == taskID {
			delete(s.healthTasks, task.UserID)
		}
		if task.IdempotencyKey != "" {
			index := searchTaskIdempotencyIndex{UserID: task.UserID, Key: task.IdempotencyKey}
			if s.idempotency[index] == taskID {
				delete(s.idempotency, index)
			}
		}
	}
}

func runSearchTaskSafely(run func() (model.SearchResponse, error)) (result model.SearchResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.SysError(fmt.Sprintf("异步搜题任务 panic: %s", logger.RedactSecrets(fmt.Sprint(recovered))))
			result = model.SearchResponse{}
			err = fmt.Errorf("search task panic")
		}
	}()
	return run()
}

// CreateSearchTask queues the existing search handler in a detached request context.
func CreateSearchTask(c *gin.Context) {
	rawBody := readAndRestoreRequestBody(c)
	var input model.SearchRequest
	if err := json.Unmarshal([]byte(rawBody), &input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if c.Request == nil || c.Request.URL == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求地址无效"})
		return
	}

	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len([]rune(idempotencyKey)) > searchTaskMaxIdempotencyKeyLength {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key 长度不能超过 128 个字符"})
		return
	}
	fingerprint := fingerprintSearchTaskRequest(c.Request, rawBody)
	requestSnapshot := cloneSearchTaskRequest(c.Request, rawBody)
	userID := currentSearchTaskUserID(c)
	taskID, _, err := defaultSearchTaskStore.create(userID, idempotencyKey, fingerprint, func() (model.SearchResponse, error) {
		return executeSearchTask(requestSnapshot)
	})
	if errors.Is(err, errSearchTaskIdempotencyConflict) {
		c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "无法创建搜题任务"})
		return
	}
	task, err := defaultSearchTaskStore.get(taskID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "搜题任务暂不可用"})
		return
	}
	c.JSON(http.StatusAccepted, buildSearchTaskResponse(task))
}

// GetSearchTaskHealth provides a real, owner-scoped status URL without executing a search.
func GetSearchTaskHealth(c *gin.Context) {
	userID := currentSearchTaskUserID(c)
	if userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请提供有效访问令牌"})
		return
	}
	c.JSON(http.StatusOK, buildSearchTaskResponse(defaultSearchTaskStore.healthProbe(userID)))
}

// GetSearchTaskStatus returns a task snapshot without executing search work.
func GetSearchTaskStatus(c *gin.Context) {
	taskID := strings.TrimSpace(c.Query("taskId"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "taskId 不能为空"})
		return
	}
	task, err := defaultSearchTaskStore.get(taskID, currentSearchTaskUserID(c))
	if err != nil {
		if errors.Is(err, errSearchTaskForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"message": "无权限查看该任务"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"message": "任务不存在或已过期"})
		return
	}
	c.JSON(http.StatusOK, buildSearchTaskResponse(task))
}

func executeSearchTask(request *http.Request) (model.SearchResponse, error) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	Search(ctx)
	if recorder.Code < http.StatusOK || recorder.Code >= http.StatusMultipleChoices {
		var unauthorized searchUnauthorizedResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &unauthorized); err == nil && strings.TrimSpace(unauthorized.Message) != "" {
			return model.SearchResponse{Error: &model.SearchError{Code: unauthorized.Code, Message: unauthorized.Message}}, nil
		}
		return model.SearchResponse{Error: &model.SearchError{Code: "UPSTREAM_ERROR", Message: "搜题请求失败"}}, nil
	}
	var result model.SearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		return model.SearchResponse{}, fmt.Errorf("decode search response: %w", err)
	}
	return result, nil
}

func cloneSearchTaskRequest(request *http.Request, rawBody string) *http.Request {
	cloned := request.Clone(context.Background())
	urlCopy := *request.URL
	cloned.URL = &urlCopy
	cloned.Header = request.Header.Clone()
	cloned.Body = io.NopCloser(bytes.NewReader([]byte(rawBody)))
	cloned.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte(rawBody))), nil
	}
	return cloned
}

func fingerprintSearchTaskRequest(request *http.Request, rawBody string) string {
	path := request.URL.EscapedPath()
	if path == "" {
		path = request.URL.Path
	}
	value := strings.Join([]string{request.Method, path, request.URL.RawQuery, request.Header.Get("Authorization"), rawBody}, "\n")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func buildSearchTaskResponse(task searchTask) searchTaskResponse {
	response := searchTaskResponse{
		Success:   task.Status == searchTaskStatusSucceeded,
		Status:    task.Status,
		TaskID:    task.ID,
		StatusURL: searchTaskStatusURL(task.ID),
	}
	if task.Status != searchTaskStatusRunning {
		if task.Status == searchTaskStatusSucceeded {
			result := task.Result
			response.Result = &result
		}
		response.Error = task.Error
	}
	return response
}

func searchTaskStatusURL(taskID string) string {
	return "/adapter-service/search/status?taskId=" + url.QueryEscape(taskID)
}

func currentSearchTaskUserID(c *gin.Context) int32 {
	value, ok := c.Get("user")
	if !ok {
		return 0
	}
	user, ok := value.(*entity.User)
	if !ok || user == nil {
		return 0
	}
	return user.ID
}
