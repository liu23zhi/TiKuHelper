package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestAIFallbackTaskStoreRunsTaskAndScopesResultToOwner(t *testing.T) {
	release := make(chan struct{})
	store := newAIFallbackTaskStore(time.Minute, time.Now)

	taskID := store.create(7, func() service.AIFallbackResult {
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
	now := time.Now()
	release := make(chan struct{})
	store := newAIFallbackTaskStore(time.Minute, func() time.Time {
		return now
	})

	taskID := store.create(7, func() service.AIFallbackResult {
		<-release
		return service.BuildAIFallbackUnavailableResult()
	})
	now = now.Add(2 * time.Minute)

	if _, err := store.get(taskID, 7); !errors.Is(err, errAIFallbackTaskNotFound) {
		t.Fatalf("expected expired task to be missing, got %v", err)
	}

	close(release)
}

func TestCreateAIFallbackTaskReturnsRunningTaskAndStatusCanComplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalStore := defaultAIFallbackTaskStore
	originalRun := runAIFallbackService
	defer func() {
		defaultAIFallbackTaskStore = originalStore
		runAIFallbackService = originalRun
	}()

	defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: question,
				Answer:   "2",
			},
		}
	}

	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Set("user", &entity.User{ID: 7})
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
	taskID := defaultAIFallbackTaskStore.create(7, func() service.AIFallbackResult {
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
