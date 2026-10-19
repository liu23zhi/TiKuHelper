package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAIFallbackNonUpstreamOutcomeDoesNotUseErrorLevel(t *testing.T) {
	newSearchControllerTestDB(t)
	var output, errors bytes.Buffer
	originalOutput, originalErrors := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &output, &errors
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = originalOutput, originalErrors })
	writeAIFallbackAuditLog(aiFallbackAuditIdentity{}, "req-unconfigured", "", "result", "sync", model.SearchRequest{Question: "not configured"}, service.BuildAIFallbackUnavailableResult())
	if errors.Len() > 0 {
		t.Fatalf("unconfigured service incorrectly classified as interface error: %s", errors.String())
	}
	if !strings.Contains(output.String(), "[WARN]") || !strings.Contains(output.String(), "AI_UNAVAILABLE") {
		t.Fatalf("non-upstream result missing warning log: %s", output.String())
	}
}

func TestAIFallbackWritesPrivateFailureDetailToAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 325, "detail-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	original := runAIFallbackService
	t.Cleanup(func() { runAIFallbackService = original })
	detail := strings.Repeat("真实HTTP 503错误，", 50)
	runAIFallbackService = func(model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{Error: &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: detail}, FailureDetail: detail}
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &user)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"同步失败详情","type":0}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	AIFallback(ctx)
	if strings.Contains(recorder.Body.String(), "真实HTTP") {
		t.Fatal("upstream details leaked into public API")
	}
	var publicResult service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &publicResult); err != nil {
		t.Fatal(err)
	}
	if publicResult.Error == nil || publicResult.Error.Message != "服务器内部错误，搜题失败。" {
		t.Fatalf("expected generic public failure, got %s", recorder.Body.String())
	}
	var entry entity.Log
	if err := db.Where("module = ?", aiFallbackAuditModule).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		FailureDetail string `json:"failureDetail"`
	}
	if err := json.Unmarshal([]byte(entry.NewAnswer), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.FailureDetail != detail {
		t.Fatalf("audit missing complete detail: %q", snapshot.FailureDetail)
	}
	if len([]rune(entry.Description)) > 240 {
		t.Fatalf("description too long: %d", len([]rune(entry.Description)))
	}
}

func TestAIFallbackAuditsPanicWithoutChangingHTTPFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 327, "sync-panic-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	original := runAIFallbackService
	t.Cleanup(func() { runAIFallbackService = original })
	runAIFallbackService = func(model.SearchRequest) service.AIFallbackResult { panic("sync upstream panic") }
	router := gin.New()
	router.Use(gin.Recovery())
	router.POST("/adapter-service/ai-fallback", func(c *gin.Context) { c.Set("user", &user); AIFallback(c) })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"同步panic","type":4}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected existing panic HTTP 500 behavior, got %d", recorder.Code)
	}
	var entries []entity.Log
	if err := db.Where("module = ?", aiFallbackAuditModule).Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Description, "stage=panic") || !strings.Contains(entries[0].NewAnswer, "sync upstream panic") {
		t.Fatalf("missing single sync panic audit: %+v", entries)
	}
}

func TestCreateAIFallbackTaskAuditsFailureDetailsExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name         string
		panicFailure bool
		stage        string
	}{
		{name: "panic", panicFailure: true, stage: "panic"},
		{name: "upstream error", stage: "result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			fallbackResultCache.clear()
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 326, "panic-fallback-user", "user")
			if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			originalRun, originalStore := runAIFallbackService, defaultAIFallbackTaskStore
			t.Cleanup(func() { runAIFallbackService = originalRun; defaultAIFallbackTaskStore = originalStore })
			defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
			runAIFallbackService = func(model.SearchRequest) service.AIFallbackResult {
				if tc.panicFailure {
					panic("panic upstream Authorization: Bearer panic-secret")
				}
				return service.AIFallbackResult{Error: &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: "AI 上游请求失败"}, FailureDetail: "panic upstream Authorization: Bearer panic-secret"}
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("user", &user)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(`{"question":"异步异常详情","type":4}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			CreateAIFallbackTask(ctx)
			var created aiFallbackTaskResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				task, err := defaultAIFallbackTaskStore.get(created.TaskID, user.ID)
				if err != nil {
					t.Fatal(err)
				}
				if task.Status == aiFallbackTaskStatusFailed {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("task failed to finish")
				}
				time.Sleep(time.Millisecond)
			}
			var entries []entity.Log
			if err := db.Where("module = ?", aiFallbackAuditModule).Find(&entries).Error; err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("expected exactly one panic audit, got %d", len(entries))
			}
			entry := entries[0]
			if !strings.Contains(entry.Description, "stage="+tc.stage) || !strings.Contains(entry.NewAnswer, "panic upstream") || strings.Contains(entry.NewAnswer, "panic-secret") {
				t.Fatalf("missing or unredacted panic audit: %+v", entry)
			}
		})
	}
}
