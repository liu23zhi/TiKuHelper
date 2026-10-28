package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestSearchPassesHTTPRequestContextToAISearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newSearchControllerTestDB(t)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{RequestID: "context-pass", AIConfig: service.AISearchRequestConfig{BaseURL: "https://example.com", APIKey: "test", Model: "test"}}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) { return nil },
		func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			if resolved.Context != requestContext {
				t.Error("AI search did not receive the HTTP request context")
			}
			cancel()
			if resolved.Context.Err() != context.Canceled {
				t.Error("AI search context did not observe cancellation")
			}
			return nil, searchpkg.AISearchUsage{}, context.Canceled
		}, func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewBufferString(`{"question":"question","type":2}`)).WithContext(requestContext)
			ctx.Request.Header.Set("Content-Type", "application/json")
			Search(ctx)
		})
}

func TestAdminAISearchTestPassesHTTPRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := runAISearchDebug
	t.Cleanup(func() { runAISearchDebug = original })
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	runAISearchDebug = func(input aiSearchTestInput) ([][]string, searchpkg.AISearchDebugResult, error) {
		if input.requestContext != requestContext {
			t.Error("admin test lost its request context")
		}
		cancel()
		return nil, searchpkg.AISearchDebugResult{}, input.requestContext.Err()
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-search-config/test", bytes.NewBufferString(`{"model":"test","requestContext":{}}`)).WithContext(requestContext)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	TestAISearchConfig(ctx)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected canceled test failure, got %d", recorder.Code)
	}
}

func TestFallbackRequestContextIsSynchronousOnly(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			fallbackResultCache.clear()
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 709, "context-fallback", "user")
			if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			originalRun, originalStore := runAIFallbackService, defaultAIFallbackTaskStore
			t.Cleanup(func() { runAIFallbackService, defaultAIFallbackTaskStore = originalRun, originalStore })
			defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
			received := make(chan context.Context, 1)
			runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
				received <- req.RequestContext
				return service.BuildAIFallbackUnavailableResult()
			}
			requestContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("user", &user)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"new context question","type":2,"requestContext":{}}`)).WithContext(requestContext)
			ctx.Request.Header.Set("Content-Type", "application/json")
			if async {
				CreateAIFallbackTask(ctx)
				cancel()
			} else {
				AIFallback(ctx)
			}
			select {
			case actual := <-received:
				if async && actual != nil {
					t.Fatal("async fallback inherited the submission request context")
				}
				if !async && actual != requestContext {
					t.Fatal("synchronous fallback did not inherit the request context")
				}
			case <-time.After(time.Second):
				t.Fatal("fallback service was not called")
			}
			if async {
				var created aiFallbackTaskResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				eventuallyAIFallbackResponse(t, user.ID, created.TaskID, func(response aiFallbackTaskResponse) bool { return response.Status == aiFallbackTaskStatusFailed })
			}
		})
	}
}
