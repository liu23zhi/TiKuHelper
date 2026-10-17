package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type lockedAIFallbackTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newLockedAIFallbackTestClock(now time.Time) *lockedAIFallbackTestClock {
	return &lockedAIFallbackTestClock{now: now}
}

func (c *lockedAIFallbackTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lockedAIFallbackTestClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestAIFallbackTaskStoreRunsTaskAndScopesResultToOwner(t *testing.T) {
	release := make(chan struct{})
	store := newAIFallbackTaskStore(time.Minute, time.Now)

	taskID := store.create(7, func(string) service.AIFallbackResult {
		<-release
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: "1+1=?",
				Answer:   "2",
			},
		}
	})

	initial, err := store.get(taskID, 7)
	if err != nil {
		t.Fatalf("get initial task: %v", err)
	}
	if initial.Status != aiFallbackTaskStatusRunning {
		t.Fatalf("expected running status, got %q", initial.Status)
	}

	if _, err := store.get(taskID, 8); !errors.Is(err, errAIFallbackTaskForbidden) {
		t.Fatalf("expected forbidden task access, got %v", err)
	}

	close(release)
	eventuallyAIFallbackTaskStatus(t, store, taskID, 7, aiFallbackTaskStatusSucceeded)

	completed, err := store.get(taskID, 7)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if !completed.Result.Success || completed.Result.Result == nil || completed.Result.Result.Answer != "2" {
		t.Fatalf("unexpected completed task result: %+v", completed.Result)
	}
}

func TestAIFallbackTaskStoreExpiresTasks(t *testing.T) {
	clock := newLockedAIFallbackTestClock(time.Now())
	release := make(chan struct{})
	store := newAIFallbackTaskStore(time.Minute, clock.Now)

	taskID := store.create(7, func(string) service.AIFallbackResult {
		<-release
		return service.BuildAIFallbackUnavailableResult()
	})
	clock.Advance(2 * time.Minute)

	running, err := store.get(taskID, 7)
	if err != nil {
		t.Fatalf("expected running task to remain visible after TTL: %v", err)
	}
	if running.Status != aiFallbackTaskStatusRunning {
		t.Fatalf("expected running status, got %q", running.Status)
	}

	close(release)
	eventuallyAIFallbackTaskStatus(t, store, taskID, 7, aiFallbackTaskStatusFailed)
	clock.Advance(2 * time.Minute)

	if _, err := store.get(taskID, 7); !errors.Is(err, errAIFallbackTaskNotFound) {
		t.Fatalf("expected expired task to be missing, got %v", err)
	}
}

func TestAIFallbackTaskStoreKeepsRunningTaskWhileConcurrentOwnerTaskCompletes(t *testing.T) {
	clock := newLockedAIFallbackTestClock(time.Now())
	store := newAIFallbackTaskStore(time.Minute, clock.Now)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstID := store.create(7, func(string) service.AIFallbackResult {
		close(firstStarted)
		<-firstRelease
		return service.AIFallbackResult{Success: true}
	})
	<-firstStarted

	clock.Advance(2 * time.Minute)
	secondID := store.create(8, func(string) service.AIFallbackResult {
		return service.AIFallbackResult{Success: true}
	})
	eventuallyAIFallbackTaskStatus(t, store, secondID, 8, aiFallbackTaskStatusSucceeded)

	first, err := store.get(firstID, 7)
	if err != nil {
		t.Fatalf("expected running task to remain visible while AI call is pending: %v", err)
	}
	if first.Status != aiFallbackTaskStatusRunning {
		t.Fatalf("expected first task to remain running, got %q", first.Status)
	}

	close(firstRelease)
	eventuallyAIFallbackTaskStatus(t, store, firstID, 7, aiFallbackTaskStatusSucceeded)
}

func TestCreateAIFallbackTaskReturnsRunningTaskAndStatusCanComplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 7, "ai-task-running-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	originalStore := defaultAIFallbackTaskStore
	originalRun := runAIFallbackService
	defer func() {
		defaultAIFallbackTaskStore = originalStore
		runAIFallbackService = originalRun
	}()

	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: req.Question,
				Answer:   "2",
			},
		}
	}

	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Set("user", &user)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	createContext.Request.Header.Set("Content-Type", "application/json")

	CreateAIFallbackTask(createContext)

	if createRecorder.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d with body %s", createRecorder.Code, createRecorder.Body.String())
	}
	var created aiFallbackTaskResponse
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if created.TaskID == "" || created.Status != aiFallbackTaskStatusRunning || created.StatusURL == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	var completed aiFallbackTaskResponse
	eventuallyAIFallbackResponse(t, 7, created.TaskID, func(response aiFallbackTaskResponse) bool {
		completed = response
		return response.Status == aiFallbackTaskStatusSucceeded
	})

	if !completed.Success || completed.Result == nil || completed.Result.Answer != "2" {
		t.Fatalf("unexpected completed response: %+v", completed)
	}
}

func TestGetAIFallbackTaskStatusRejectsOtherUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalStore := defaultAIFallbackTaskStore
	defer func() {
		defaultAIFallbackTaskStore = originalStore
	}()

	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	taskID := defaultAIFallbackTaskStore.create(7, func(string) service.AIFallbackResult {
		return service.BuildAIFallbackUnavailableResult()
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("user", &entity.User{ID: 8})
	context.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-fallback/status?taskId="+taskID, nil)

	GetAIFallbackTaskStatus(context)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateAIFallbackTaskRejectsZeroBalanceBeforeQueueing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 303, "zero-balance-ai-task-user", "user")
	token := seedControllerTokenForUser(t, db, user, "zero-balance-ai-task-token", 0)

	originalStore := defaultAIFallbackTaskStore
	originalRun := runAIFallbackService
	defer func() {
		defaultAIFallbackTaskStore = originalStore
		runAIFallbackService = originalRun
	}()
	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	aiCalls := 0
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		aiCalls++
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: req.Question,
				Answer:   "2",
			},
		}
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback/tasks", AuthOrAccessToken, CreateAIFallbackTask)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var got aiFallbackTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Success {
		t.Fatalf("expected zero-balance task creation to fail, got %+v", got)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeInsufficientBalance {
		t.Fatalf("expected INSUFFICIENT_BALANCE, got %+v", got.Error)
	}
	if aiCalls != 0 {
		t.Fatalf("expected no AI call for zero balance, got %d", aiCalls)
	}
	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback precheck log: %v", err)
	}
	for _, check := range []string{"status=failed", "stage=billing_precheck", "mode=async", "code=INSUFFICIENT_BALANCE"} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
}

func TestCreateAIFallbackTaskBillsSuccessfulAIUsageOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 304, "billed-ai-task-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "billed-ai-task-token", 0)
	// 1000 输入 × 0.10 + 500 输出 × 0.50（元 / 百万 token）= 0.0001 + 0.00025 → 0.00
	seedControllerAIModelPrice(t, db, "fallback-billing-model", 0.10, 0.01, 0.50)
	enableStagedBilling := true
	if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &enableStagedBilling, nil); err != nil {
		t.Fatalf("enable staged AI billing: %v", err)
	}

	originalStore := defaultAIFallbackTaskStore
	originalRun := runAIFallbackService
	defer func() {
		defaultAIFallbackTaskStore = originalStore
		runAIFallbackService = originalRun
	}()
	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: req.Question,
				Answer:   "2",
			},
			Model: "fallback-billing-model",
			Usage: searchpkg.AISearchUsage{
				InputTokens:  1000000,
				OutputTokens: 500000,
				TotalTokens:  1500000,
				ModelCalls: []searchpkg.AIModelCallUsage{{
					Index:        1,
					Model:        "fallback-billing-model",
					InputTokens:  1000000,
					OutputTokens: 500000,
					TotalTokens:  1500000,
				}},
				Trace: []searchpkg.AISearchTrace{{
					Stage:          "model_response",
					Model:          "fallback-billing-model",
					ModelCallIndex: 1,
					InputTokens:    1000000,
					OutputTokens:   500000,
					TotalTokens:    1500000,
				}},
			},
		}
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback/tasks", AuthOrAccessToken, CreateAIFallbackTask)
	router.GET("/adapter-service/ai-fallback/status", AuthOrAccessToken, GetAIFallbackTaskStatus)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var created aiFallbackTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if created.TaskID == "" {
		t.Fatalf("expected task id, got %+v", created)
	}

	deadline := time.Now().Add(time.Second)
	for {
		statusRecorder := httptest.NewRecorder()
		statusRequest := httptest.NewRequest(http.MethodGet, "/adapter-service/ai-fallback/status?taskId="+created.TaskID, nil)
		statusRequest.Header.Set("Authorization", "Bearer "+token.TokenValue)
		router.ServeHTTP(statusRecorder, statusRequest)
		if statusRecorder.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d with body %s", statusRecorder.Code, statusRecorder.Body.String())
		}
		var status aiFallbackTaskResponse
		if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("unmarshal status response: %v", err)
		}
		if status.Status == aiFallbackTaskStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not complete: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}

	var records []entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly one billing record, got %+v", records)
	}
	record := records[0]
	if record.Provider != searchModeAI || record.ModelName != "fallback-billing-model" {
		t.Fatalf("unexpected billing provider/model: %+v", record)
	}
	if record.ClientIP != "192.0.2.1" {
		t.Fatalf("expected async fallback request IP in billing record, got %q", record.ClientIP)
	}
	if record.Amount != 0.105 || record.TotalTokens != 1500000 {
		t.Fatalf("unexpected billing amount/tokens: %+v", record)
	}
	if record.BalanceBefore != 10 || record.BalanceAfter != 9.895 {
		t.Fatalf("unexpected billing balance: %+v", record)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != 9.895 {
		t.Fatalf("expected user balance 9.895, got %v", storedUser.Balance)
	}
	var storedToken entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&storedToken).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if storedToken.TotalAmountUsed != 0.105 {
		t.Fatalf("expected token usage 0.105, got %v", storedToken.TotalAmountUsed)
	}

	statusAgain := httptest.NewRecorder()
	statusAgainRequest := httptest.NewRequest(http.MethodGet, "/adapter-service/ai-fallback/status?taskId="+created.TaskID, nil)
	statusAgainRequest.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(statusAgain, statusAgainRequest)
	if statusAgain.Code != http.StatusOK {
		t.Fatalf("expected repeated status 200, got %d", statusAgain.Code)
	}
	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).Count(&count).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected repeated status polling not to double bill, got %d records", count)
	}
	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback task success log: %v", err)
	}
	for _, check := range []string{"status=success", "billingProvider=ai", "taskId=" + created.TaskID} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
	for _, check := range []string{`"stageBillingMode":"fixed"`, `"stageMultiplier":0.3`, `"finalMultiplier":0.3`, `"multiplierApplicable":true`} {
		if !strings.Contains(entry.NewAnswer, check) {
			t.Fatalf("expected async fallback audit snapshot %q to contain %q", entry.NewAnswer, check)
		}
	}
}

func TestCreateAIFallbackTaskWritesFailureAuditLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 308, "failed-ai-task-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "failed-ai-task-token", 0)

	originalStore := defaultAIFallbackTaskStore
	originalRun := runAIFallbackService
	defer func() {
		defaultAIFallbackTaskStore = originalStore
		runAIFallbackService = originalRun
	}()
	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.BuildAIFallbackUnavailableResult()
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback/tasks", AuthOrAccessToken, CreateAIFallbackTask)
	router.GET("/adapter-service/ai-fallback/status", AuthOrAccessToken, GetAIFallbackTaskStatus)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var created aiFallbackTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		statusRecorder := httptest.NewRecorder()
		statusRequest := httptest.NewRequest(http.MethodGet, "/adapter-service/ai-fallback/status?taskId="+created.TaskID, nil)
		statusRequest.Header.Set("Authorization", "Bearer "+token.TokenValue)
		router.ServeHTTP(statusRecorder, statusRequest)
		var status aiFallbackTaskResponse
		if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("unmarshal status response: %v", err)
		}
		if status.Status == aiFallbackTaskStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not fail: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}

	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback task failure log: %v", err)
	}
	for _, check := range []string{"status=failed", "stage=result", "code=AI_UNAVAILABLE", "taskId=" + created.TaskID} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
}

func eventuallyAIFallbackTaskStatus(t *testing.T, store *aiFallbackTaskStore, taskID string, userID int32, expected string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := store.get(taskID, userID)
		if err == nil && task.Status == expected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not reach status %s", taskID, expected)
}

func eventuallyAIFallbackResponse(t *testing.T, userID int32, taskID string, done func(aiFallbackTaskResponse) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("user", &entity.User{ID: userID})
		context.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-fallback/status?taskId="+taskID, nil)

		GetAIFallbackTaskStatus(context)
		if recorder.Code == http.StatusOK {
			var response aiFallbackTaskResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("unmarshal status response: %v", err)
			}
			if done(response) {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not complete", taskID)
}
