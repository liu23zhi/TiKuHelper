package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestSearchKeepsAdmittedWorkContextPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 7090, "context-owner", "user")
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	user.Balance = 10
	token := seedControllerTokenForUser(t, db, user, "context-token", 0)
	tracker := scopedProducerTracker(t)
	type traceKey struct{}
	requestContext, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, "owned-trace"))
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{RequestID: "context-pass", UserID: user.ID, AccessTokenID: token.ID, SearchMode: searchModeAI, UsedDefaultAI: true,
				AccessTokenCtx: &service.AccessTokenAuthContext{User: user, AccessToken: token}, AIConfig: service.AISearchRequestConfig{BaseURL: "https://example.invalid", APIKey: "synthetic", Model: "test"}}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) { return nil },
		func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			if calls.Add(1) != 1 {
				t.Error("live observer executed a second model call")
				return nil, searchpkg.AISearchUsage{}, nil
			}
			if resolved.Context == nil || resolved.Context == requestContext || resolved.Context.Value(traceKey{}) != "owned-trace" {
				t.Error("execution owner lost values or inherited the observer context")
			}
			cancel()
			if resolved.Context != nil && resolved.Context.Err() != nil {
				t.Error("observer cancellation poisoned admitted work")
			}
			close(entered)
			<-release
			return [][]string{{"fulfilled after observer cancellation"}}, searchpkg.AISearchUsage{}, nil
		}, func() {
			defer func() {
				unblock()
				ctx, done := context.WithTimeout(context.Background(), time.Second)
				defer done()
				if err := WaitForSearchExecutions(ctx); err != nil {
					t.Error("owned producer cleanup did not drain")
				}
			}()
			// Forward the real entry context instead of the generic legacy mock's
			// discarded parameter; this observes the actual snapshot ownership.
			legacy := resolveSearchRequestContext
			resolveSearchRequestContext = func(ctx context.Context, a, ip string, q url.Values) (resolvedSearchRequestContext, error) {
				r, err := legacy(ctx, a, ip, q)
				r.Context = ctx
				return r, err
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewBufferString(`{"question":"question","type":2}`)).WithContext(requestContext)
			ctx.Request.Header.Set("Content-Type", "application/json")
			Search(ctx)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("admitted owner did not enter model mock")
			}
			if recorder.Body.Len() != 0 {
				t.Fatal("canceled HTTP observer unexpectedly received a producer body")
			}
			live := httptest.NewRecorder()
			liveContext, _ := gin.CreateTestContext(live)
			liveContext.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewBufferString(`{"question":"question","type":2}`))
			liveContext.Request.Header.Set("Content-Type", "application/json")
			liveDone := make(chan struct{})
			go func() { defer close(liveDone); Search(liveContext) }()
			deadline := time.Now().Add(time.Second)
			for producerTrackerActive(tracker) < 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if producerTrackerActive(tracker) < 2 {
				t.Error("live observer did not join before release")
			}
			unblock()
			select {
			case <-liveDone:
			case <-time.After(time.Second):
				t.Fatal("live observer did not receive admitted result")
			}
			var response model.SearchResponse
			if err := json.Unmarshal(live.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if live.Code != http.StatusOK || len(response.Answer.AllAnswer) != 1 || len(response.Answer.AllAnswer[0]) != 1 || response.Answer.AllAnswer[0][0] != "fulfilled after observer cancellation" || calls.Load() != 1 {
				t.Fatal("admitted result was not delivered once to live observer")
			}
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

func TestFallbackKeepsAdmittedWorkContextPolicy(t *testing.T) {
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
			type fallbackTraceKey struct{}
			requestContext, cancel := context.WithCancel(context.WithValue(context.Background(), fallbackTraceKey{}, "fallback-trace"))
			defer cancel()
			runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
				if !async {
					cancel()
				}
				received <- req.RequestContext
				return service.BuildAIFallbackUnavailableResult()
			}
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
				if async {
					if actual != nil {
						t.Fatal("async fallback inherited an HTTP observer context")
					}
				} else if actual == nil || actual == requestContext || actual.Value(fallbackTraceKey{}) != "fallback-trace" || actual.Err() != nil {
					t.Fatal("sync execution lost values or inherited observer cancellation")
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
