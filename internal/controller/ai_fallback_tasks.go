package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

const (
	aiFallbackTaskStatusRunning   = "running"
	aiFallbackTaskStatusSucceeded = "succeeded"
	aiFallbackTaskStatusFailed    = "failed"
	aiFallbackTaskDefaultTTL      = 15 * time.Minute
)

var (
	errAIFallbackTaskNotFound  = errors.New("AI 兜底任务不存在")
	errAIFallbackTaskForbidden = errors.New("无权限查看 AI 兜底任务")
)

type aiFallbackTask struct {
	ID        string
	UserID    int32
	Status    string
	Result    service.AIFallbackResult
	CreatedAt time.Time
	UpdatedAt time.Time
}

type aiFallbackTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]aiFallbackTask
	ttl   time.Duration
	now   func() time.Time
}

type aiFallbackTaskResponse struct {
	Success   bool                      `json:"success"`
	Status    string                    `json:"status"`
	TaskID    string                    `json:"taskId,omitempty"`
	StatusURL string                    `json:"statusUrl,omitempty"`
	Result    *service.AIFallbackAnswer `json:"result,omitempty"`
	Error     *service.AIFallbackError  `json:"error,omitempty"`
}

func newAIFallbackTaskStore(ttl time.Duration, now func() time.Time) *aiFallbackTaskStore {
	if ttl <= 0 {
		ttl = aiFallbackTaskDefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &aiFallbackTaskStore{
		tasks: make(map[string]aiFallbackTask),
		ttl:   ttl,
		now:   now,
	}
}

var defaultAIFallbackTaskStore = newAIFallbackTaskStore(aiFallbackTaskDefaultTTL, time.Now)

func (s *aiFallbackTaskStore) create(userID int32, run func() service.AIFallbackResult) string {
	taskID := uuid.NewString()
	now := s.now()

	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	s.tasks[taskID] = aiFallbackTask{
		ID:        taskID,
		UserID:    userID,
		Status:    aiFallbackTaskStatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Unlock()

	go func() {
		s.complete(taskID, runAIFallbackTaskSafely(run))
	}()

	return taskID
}

func (s *aiFallbackTaskStore) get(taskID string, userID int32) (aiFallbackTask, error) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	task, ok := s.tasks[taskID]
	if !ok {
		return aiFallbackTask{}, errAIFallbackTaskNotFound
	}
	if task.UserID != userID {
		return aiFallbackTask{}, errAIFallbackTaskForbidden
	}
	return task, nil
}

func (s *aiFallbackTaskStore) complete(taskID string, result service.AIFallbackResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return
	}
	task.Status = aiFallbackTaskStatusFailed
	if result.Success {
		task.Status = aiFallbackTaskStatusSucceeded
	}
	task.Result = result
	task.UpdatedAt = s.now()
	s.tasks[taskID] = task
}

func (s *aiFallbackTaskStore) cleanupExpiredLocked(now time.Time) {
	for taskID, task := range s.tasks {
		if now.Sub(task.UpdatedAt) >= s.ttl {
			delete(s.tasks, taskID)
		}
	}
}

func runAIFallbackTaskSafely(run func() service.AIFallbackResult) (result service.AIFallbackResult) {
	defer func() {
		if recover() != nil {
			result = service.AIFallbackResult{
				Success: false,
				Error: &service.AIFallbackError{
					Code:    service.AIFallbackErrorCodeUpstreamError,
					Message: "AI 兜底任务执行失败",
				},
			}
		}
	}()
	return run()
}

// CreateAIFallbackTask queues an AI fallback request and returns its task ID.
func CreateAIFallbackTask(c *gin.Context) {
	var input aiFallbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusOK, service.BuildAIFallbackInvalidInputResult("参数错误"))
		return
	}
	if strings.TrimSpace(input.Question) == "" {
		c.JSON(http.StatusOK, service.BuildAIFallbackInvalidInputResult("题目不能为空"))
		return
	}

	userID := currentAIFallbackUserID(c)
	taskID := defaultAIFallbackTaskStore.create(userID, func() service.AIFallbackResult {
		return runAIFallbackService(input.Question, input.Type, input.Options)
	})

	c.JSON(http.StatusAccepted, aiFallbackTaskResponse{
		Success:   false,
		Status:    aiFallbackTaskStatusRunning,
		TaskID:    taskID,
		StatusURL: aiFallbackStatusURL(taskID),
	})
}

// GetAIFallbackTaskStatus returns the current state of an AI fallback task.
func GetAIFallbackTaskStatus(c *gin.Context) {
	taskID := strings.TrimSpace(c.Query("taskId"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "taskId 不能为空"})
		return
	}

	task, err := defaultAIFallbackTaskStore.get(taskID, currentAIFallbackUserID(c))
	if err != nil {
		switch {
		case errors.Is(err, errAIFallbackTaskForbidden):
			c.JSON(http.StatusForbidden, gin.H{"message": "无权限查看该任务"})
		default:
			c.JSON(http.StatusNotFound, gin.H{"message": "任务不存在或已过期"})
		}
		return
	}

	c.JSON(http.StatusOK, buildAIFallbackTaskResponse(task))
}

func buildAIFallbackTaskResponse(task aiFallbackTask) aiFallbackTaskResponse {
	response := aiFallbackTaskResponse{
		Success:   task.Status == aiFallbackTaskStatusSucceeded && task.Result.Success,
		Status:    task.Status,
		TaskID:    task.ID,
		StatusURL: aiFallbackStatusURL(task.ID),
	}
	if task.Status != aiFallbackTaskStatusRunning {
		response.Result = task.Result.Result
		response.Error = task.Result.Error
	}
	return response
}

func aiFallbackStatusURL(taskID string) string {
	return "/adapter-service/ai-fallback/status?taskId=" + url.QueryEscape(taskID)
}

func currentAIFallbackUserID(c *gin.Context) int32 {
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
