package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
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

	// Warnings 透传「模型未配置价格」这类非阻断提示。
	Warnings []string `json:"warnings,omitempty"`
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

func (s *aiFallbackTaskStore) create(userID int32, run func(taskID string) service.AIFallbackResult) string {
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
		s.complete(taskID, runAIFallbackTaskSafely(func() service.AIFallbackResult {
			return run(taskID)
		}))
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
		if task.Status == aiFallbackTaskStatusRunning {
			continue
		}
		if now.Sub(task.UpdatedAt) >= s.ttl {
			delete(s.tasks, taskID)
		}
	}
}

func runAIFallbackTaskSafely(run func() service.AIFallbackResult) (result service.AIFallbackResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = buildAIFallbackPanicResult(recovered)
		}
	}()
	return run()
}

func buildAIFallbackPanicResult(recovered any) service.AIFallbackResult {
	return service.AIFallbackResult{
		Error:         &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: "AI 兜底任务执行失败"},
		FailureDetail: "panic: " + logger.RedactSecrets(fmt.Sprint(recovered)),
	}
}

// CreateAIFallbackTask queues an AI fallback request and returns its task ID.
func CreateAIFallbackTask(c *gin.Context) {
	var input aiFallbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		result := service.BuildAIFallbackInvalidInputResult("参数错误")
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "input", "async", model.SearchRequest{}, result)
		c.JSON(http.StatusOK, result)
		return
	}
	if strings.TrimSpace(input.Question) == "" {
		result := service.BuildAIFallbackInvalidInputResult("题目不能为空")
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "input", "async", model.SearchRequest{
			Type:    input.Type,
			Options: input.Options,
		}, result)
		c.JSON(http.StatusOK, result)
		return
	}
	billingCtx, err := resolveAIFallbackBillingContext(c)
	if err != nil {
		result := service.BuildAIFallbackInsufficientBalanceResult(err.Error())
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "billing_precheck", "async", model.SearchRequest{
			Question: strings.TrimSpace(input.Question),
			Type:     input.Type,
			Options:  input.Options,
		}, result)
		c.JSON(http.StatusOK, result)
		return
	}

	userID := currentAIFallbackUserID(c)
	requestID := uuid.NewString()
	auditIdentity := newAIFallbackAuditIdentity(c)
	req := model.SearchRequest{
		Question:     strings.TrimSpace(input.Question),
		Type:         input.Type,
		Options:      input.Options,
		SkipCache:    input.SkipCache,
		DeepThinking: input.DeepThinking,
		RequestID:    requestID,
	}
	taskID := defaultAIFallbackTaskStore.create(userID, func(taskID string) (result service.AIFallbackResult) {
		audited := false
		auditResult := func(stage string, outcome service.AIFallbackResult) {
			if audited {
				return
			}
			audited = true
			writeAIFallbackAuditLog(auditIdentity, requestID, taskID, stage, "async", req, outcome)
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				result = buildAIFallbackPanicResult(recovered)
				auditResult("panic", result)
			}
		}()
		db := resolveSearchPersistenceDB()
		currentBillingCtx, err := loadAIFallbackBillingContextWithDB(db, billingCtx.UserID, billingCtx.AccessTokenID)
		if err != nil {
			result := service.BuildAIFallbackInsufficientBalanceResult(err.Error())
			auditResult("billing_precheck", result)
			return result
		}

		// 缓存命中：与搜题一致 —— 不再调用 AI、不再计费。
		// 只有显式 skipCache:true 才跳过读取；跳过读仍会把新结果写回（刷新语义）。
		if !shouldBypassFallbackResultCache(req) {
			if cached, ok := loadCachedAIFallbackResult(billingCtx.UserID, req); ok {
				logger.SysLog(fmt.Sprintf("AI 兜底缓存命中 taskId=%s requestId=%s", taskID, requestID))
				auditResult("result", cached)
				return cached
			}
		}

		// 并发合并：兜底同样是「一次上游调用 = 一次计费」，缓存写入又在上游返回之后，
		// 因此并发提交完全相同题目的请求会各自调用一次 AI 并各自扣费。
		// 只有「用户 + 题目 + 类型 + 选项」完全一致才合并。
		flightKey := buildFallbackFlightKey(billingCtx.UserID, req)
		isLeader, borrowed, leader := beginFallbackFlight(flightKey)
		if !isLeader && borrowed != nil {
			// 搭车：复用他人结果，**不重复计费**（费用已由发起者结算）。
			// 整份拷贝而不是逐字段复制：AIFallbackResult 后续新增字段时
			// 逐字段写法会静默漏掉（例如 UnpricedModel 就曾被漏掉）。
			result := cloneAIFallbackResult(*borrowed)
			logger.SysLog(fmt.Sprintf("AI 兜底并发合并命中 taskId=%s requestId=%s 复用进行中的相同请求", taskID, requestID))
			auditResult("result", result)
			return result
		}
		if isLeader {
			// 兜底释放：任何提前 return 都要唤醒搭车者，避免它们白等到超时。
			defer func() {
				finishFallbackFlight(flightKey, leader, nil)
			}()
		}

		result = runAIFallbackService(req)
		if !result.Success {
			warning, err := persistAIFallbackBillingWithWarning(currentBillingCtx, requestID, req, result)
			if err != nil {
				failure := buildAIFallbackBillingFailureResult(err)
				if isLeader {
					finishFallbackFlight(flightKey, leader, &failure)
				}
				auditResult("billing", failure)
				return failure
			}
			if strings.TrimSpace(warning) != "" {
				result.Warnings = append(result.Warnings, warning)
			}
			if isLeader {
				finishFallbackFlight(flightKey, leader, &result)
			}
			auditResult("result", result)
			return result
		}
		warning, err := persistAIFallbackBillingWithWarning(currentBillingCtx, requestID, req, result)
		if err != nil {
			failure := buildAIFallbackBillingFailureResult(err)
			if isLeader {
				finishFallbackFlight(flightKey, leader, &failure)
			}
			auditResult("billing", failure)
			return failure
		}
		if strings.TrimSpace(warning) != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		storeCachedAIFallbackResult(billingCtx.UserID, req, result)
		if isLeader {
			finishFallbackFlight(flightKey, leader, &result)
		}
		auditResult("result", result)
		return result
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
		response.Warnings = task.Result.Warnings
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
