package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestSearchTasksCreateReturnsAcceptedTaskEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	started := make(chan struct{})
	release := make(chan struct{})
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"stub"},
				Tokens:                 map[string]string{"stub": "token"},
				RequestID:              "req-async-search",
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					close(started)
					<-release
					return [][]string{{"异步答案"}}, nil
				},
			}
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return nil, searchpkg.AISearchUsage{}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/search/tasks?use=stub&noRecord=1", bytes.NewBufferString(`{"question":"题目","options":["A","B"]}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			authorizeSearchTaskFixture(t, ctx, 17)

			CreateSearchTask(ctx)

			if recorder.Code != http.StatusAccepted {
				t.Fatalf("expected HTTP 202 for async search creation, got %d with body %s", recorder.Code, recorder.Body.String())
			}
			var created searchTaskResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode create response: %v", err)
			}
			if created.Success || created.Status != searchTaskStatusRunning || created.TaskID == "" {
				t.Fatalf("expected running task envelope, got %+v", created)
			}
			if created.StatusURL != searchTaskStatusURL(created.TaskID) {
				t.Fatalf("unexpected status URL %q", created.StatusURL)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("search task did not start")
			}
			close(release)
			completed := waitForSearchTask(t, 17, created.TaskID)
			if completed.Status != searchTaskStatusSucceeded || completed.Result == nil || len(completed.Result.Answer.AllAnswer) != 1 || completed.Result.Answer.AllAnswer[0][0] != "异步答案" {
				t.Fatalf("expected completed search response, got %+v", completed)
			}
		})
}

func TestStrictAccessTokenRoutesCreateAndReadTasksAndRejectLoginAuthWithoutSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "strict-task-owner", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 12.5).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "strict-task-access-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2.5)
	var providerCalls atomic.Int32

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-strict-async-search",
				AccessTokenCtx:             &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				providerCalls.Add(1)
				return [][]string{{"有效令牌答案"}}, nil
			}}
		},
		nil,
		func() {
			router := gin.New()
			router.GET("/api/search/health", AuthAccessToken, GetSearchTaskHealth)
			router.POST("/api/search/tasks", AuthAccessToken, CreateSearchTask)
			router.GET("/api/search/status", AuthAccessToken, GetSearchTaskStatus)

			var probeID string
			for i := 0; i < 2; i++ {
				probe := httptest.NewRecorder()
				probeRequest := httptest.NewRequest(http.MethodGet, "/api/search/health", nil)
				probeRequest.Header.Set("Authorization", "Bearer "+token.TokenValue)
				router.ServeHTTP(probe, probeRequest)
				if probe.Code != http.StatusOK {
					t.Fatalf("valid token should read health probe, got %d: %s", probe.Code, probe.Body.String())
				}
				var response searchTaskResponse
				if err := json.Unmarshal(probe.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode health probe: %v", err)
				}
				if !response.Success || response.Status != searchTaskStatusSucceeded || response.TaskID == "" || response.StatusURL != searchTaskStatusURL(response.TaskID) {
					t.Fatalf("invalid health probe: %+v", response)
				}
				if probeID != "" && response.TaskID != probeID {
					t.Fatalf("health probe should reuse task, first=%q second=%q", probeID, response.TaskID)
				}
				probeID = response.TaskID
				status := httptest.NewRecorder()
				statusRequest := httptest.NewRequest(http.MethodGet, response.StatusURL, nil)
				statusRequest.Header.Set("Authorization", "Bearer "+token.TokenValue)
				router.ServeHTTP(status, statusRequest)
				if status.Code != http.StatusOK {
					t.Fatalf("health probe status should be readable, got %d: %s", status.Code, status.Body.String())
				}
				var statusResponse searchTaskResponse
				if err := json.Unmarshal(status.Body.Bytes(), &statusResponse); err != nil {
					t.Fatalf("decode health probe status: %v", err)
				}
				if statusResponse.TaskID != response.TaskID || statusResponse.Status != searchTaskStatusSucceeded || !statusResponse.Success {
					t.Fatalf("health probe status does not match successful task: %+v", statusResponse)
				}
			}
			if got := providerCalls.Load(); got != 0 {
				t.Fatalf("health probe must not call providers, got %d calls", got)
			}
			var probeBillingCount int64
			if err := db.Model(&entity.BillingRecord{}).Count(&probeBillingCount).Error; err != nil || probeBillingCount != 0 {
				t.Fatalf("health probe must not create billing records: count=%d err=%v", probeBillingCount, err)
			}

			create := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/search/tasks?use=icodef", bytes.NewBufferString(`{"question":"有效访问令牌题目","options":["A","B"],"skipCache":true}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token.TokenValue)
			router.ServeHTTP(create, request)
			if create.Code != http.StatusAccepted {
				t.Fatalf("valid access token should create task, got %d: %s", create.Code, create.Body.String())
			}
			var created searchTaskResponse
			if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode create response: %v", err)
			}
			completed := waitForSearchTask(t, user.ID, created.TaskID)
			if completed.Status != searchTaskStatusSucceeded {
				t.Fatalf("expected successful task, got %+v", completed)
			}

			for i := 0; i < 3; i++ {
				status := httptest.NewRecorder()
				statusRequest := httptest.NewRequest(http.MethodGet, searchTaskStatusURL(created.TaskID), nil)
				statusRequest.Header.Set("Authorization", "Bearer "+token.TokenValue)
				router.ServeHTTP(status, statusRequest)
				if status.Code != http.StatusOK {
					t.Fatalf("valid access token should read own task, got %d: %s", status.Code, status.Body.String())
				}
			}

			jwt := util.GenerateJwtForUser(user.ID, user.Password)
			authCases := []struct {
				name   string
				auth   string
				cookie bool
			}{
				{name: "missing"},
				{name: "login jwt", auth: jwt},
				{name: "login cookie", cookie: true},
			}
			for _, endpoint := range []struct {
				name   string
				method string
				path   string
				body   string
			}{
				{name: "health", method: http.MethodGet, path: "/api/search/health"},
				{name: "create", method: http.MethodPost, path: "/api/search/tasks?use=stub", body: `{"question":"不得执行"}`},
				{name: "status", method: http.MethodGet, path: searchTaskStatusURL(created.TaskID)},
			} {
				for _, tc := range authCases {
					t.Run(endpoint.name+"-"+tc.name, func(t *testing.T) {
						denied := httptest.NewRecorder()
						deniedRequest := httptest.NewRequest(endpoint.method, endpoint.path, bytes.NewBufferString(endpoint.body))
						if endpoint.body != "" {
							deniedRequest.Header.Set("Content-Type", "application/json")
						}
						if tc.auth != "" {
							deniedRequest.Header.Set("Authorization", tc.auth)
						}
						if tc.cookie {
							deniedRequest.AddCookie(&http.Cookie{Name: authCookieName, Value: jwt})
						}
						router.ServeHTTP(denied, deniedRequest)
						if denied.Code != http.StatusUnauthorized {
							t.Fatalf("expected 401, got %d: %s", denied.Code, denied.Body.String())
						}
					})
				}
			}
			if got := providerCalls.Load(); got != 1 {
				t.Fatalf("rejected login requests reached search provider: calls=%d", got)
			}
			var asyncBillingCount int64
			if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", "req-strict-async-search").Count(&asyncBillingCount).Error; err != nil {
				t.Fatalf("count async search billing records: %v", err)
			}
			if asyncBillingCount != 1 {
				t.Fatalf("repeated status reads should retain one billing record, got %d", asyncBillingCount)
			}
			var balance float64
			if err := db.Model(&entity.User{}).Select("balance").Where("id = ?", user.ID).Scan(&balance).Error; err != nil {
				t.Fatalf("read user balance: %v", err)
			}
			if balance != 10 {
				t.Fatalf("expected one async search charge (12.5 -> 10), got balance %v", balance)
			}
			var billingCount int64
			if err := db.Model(&entity.BillingRecord{}).Count(&billingCount).Error; err != nil {
				t.Fatalf("count billing records: %v", err)
			}
			if billingCount != 1 {
				t.Fatalf("expected only the authorized async search to create a billing record, got %d", billingCount)
			}
		},
	)
}

func TestSearchTaskNoAnswerSucceedsAndStatusReadsDoNotRepeatSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, EnabledSearchProviders: []string{"stub"}, RequestID: "req-no-answer"}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				calls.Add(1)
				return nil, nil
			}}
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return nil, searchpkg.AISearchUsage{}, nil
		},
		func() {
			created := createSearchTaskRequest(t, 23, "", `{"question":"无答案题目","options":["A","B"],"skipCache":true}`)
			task := waitForSearchTask(t, 23, created.TaskID)
			if task.Status != searchTaskStatusSucceeded || !task.Success {
				t.Fatalf("no-answer task should succeed, got %+v", task)
			}
			if task.Result == nil || task.Result.Error != nil || len(task.Result.Answer.BestAnswer) != 0 {
				t.Fatalf("expected a successful empty-answer result, got %+v", task.Result)
			}
			for i := 0; i < 3; i++ {
				got := getSearchTaskStatus(t, 23, created.TaskID, http.StatusOK)
				if got.Status != searchTaskStatusSucceeded {
					t.Fatalf("status read %d changed task state: %+v", i, got)
				}
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("status reads repeated search execution: calls=%d", got)
			}
		})
}

func TestSearchTaskIdempotencyKeyIsAppliedByCreateHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, EnabledSearchProviders: []string{"stub"}, RequestID: "req-idempotency"}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				calls.Add(1)
				close(started)
				<-release
				return [][]string{{"答案"}}, nil
			}}
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return nil, searchpkg.AISearchUsage{}, nil
		},
		func() {
			body := `{"question":"幂等题目","options":["A","B"],"skipCache":true}`
			first := createSearchTaskRequest(t, 31, "stable-request-key", body)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("search task did not start")
			}
			second := createSearchTaskRequest(t, 31, "stable-request-key", body)
			if second.TaskID != first.TaskID {
				t.Fatalf("same key and request should reuse task: first=%q second=%q", first.TaskID, second.TaskID)
			}
			conflict := createSearchTaskRequestWithStatus(t, 31, "stable-request-key", `{"question":"不同题目"}`, http.StatusConflict)
			if conflict.TaskID != "" {
				t.Fatalf("conflicting request unexpectedly returned task ID %q", conflict.TaskID)
			}
			close(release)
			waitForSearchTask(t, 31, first.TaskID)
			if got := calls.Load(); got != 1 {
				t.Fatalf("idempotent handler retry executed search %d times", got)
			}
		})
}

func TestSearchTaskExplicitSearchErrorFailsTask(t *testing.T) {
	store := newSearchTaskStore(15*time.Minute, time.Now)
	taskID, _, err := store.create(40, "", "", func() (model.SearchResponse, error) {
		return model.SearchResponse{Error: &model.SearchError{Code: "UPSTREAM_ERROR", Message: "搜题失败"}}, nil
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	task := waitForStoreSearchTask(t, store, taskID, 40)
	response := buildSearchTaskResponse(task)
	if response.Status != searchTaskStatusFailed || response.Success || response.Error == nil || response.Error.Code != "UPSTREAM_ERROR" {
		t.Fatalf("expected explicit search error to fail task, got %+v", response)
	}
}

func TestSearchTaskStatusIsOwnerScoped(t *testing.T) {
	store := newSearchTaskStore(15*time.Minute, time.Now)
	taskID, _, err := store.create(10, "", "", func() (model.SearchResponse, error) {
		return model.SearchResponse{}, nil
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.get(taskID, 11); !errors.Is(err, errSearchTaskForbidden) {
		t.Fatalf("expected owner mismatch to be forbidden, got %v", err)
	}
}

func TestGetSearchTaskStatusRejectsOtherOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	taskID, _, err := defaultSearchTaskStore.create(301, "", "", func() (model.SearchResponse, error) {
		return model.SearchResponse{Question: "私有结果"}, nil
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	waitForSearchTask(t, 301, taskID)
	getSearchTaskStatus(t, 302, taskID, http.StatusForbidden)
}

func TestSearchTaskIdempotencyKeyReusesOnlyMatchingRequest(t *testing.T) {
	store := newSearchTaskStore(15*time.Minute, time.Now)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	run := func() (model.SearchResponse, error) {
		calls.Add(1)
		close(started)
		<-release
		return model.SearchResponse{}, nil
	}
	firstID, reused, err := store.create(10, "key-1", "fingerprint-a", run)
	if err != nil || reused {
		t.Fatalf("first create should start a task, id=%q reused=%t err=%v", firstID, reused, err)
	}
	secondID, reused, err := store.create(10, "key-1", "fingerprint-a", run)
	if err != nil || !reused || secondID != firstID {
		t.Fatalf("matching idempotency key should reuse task, first=%q second=%q reused=%t err=%v", firstID, secondID, reused, err)
	}
	if _, _, err := store.create(10, "key-1", "fingerprint-b", run); !errors.Is(err, errSearchTaskIdempotencyConflict) {
		t.Fatalf("different request with same key should conflict, got %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("idempotent task did not start")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("idempotent retries should execute once, calls=%d", got)
	}
	close(release)
	waitForStoreSearchTask(t, store, firstID, 10)
}

func TestSearchTaskStoreRetainsRunningTasksPastTTL(t *testing.T) {
	now := time.Now()
	var nowMu sync.Mutex
	store := newSearchTaskStore(time.Minute, func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	})
	started := make(chan struct{})
	release := make(chan struct{})
	taskID, _, err := store.create(1, "", "", func() (model.SearchResponse, error) {
		close(started)
		<-release
		return model.SearchResponse{}, nil
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()
	if _, err := store.get(taskID, 1); err != nil {
		t.Fatalf("running task expired after TTL: %v", err)
	}
	close(release)
	waitForStoreSearchTask(t, store, taskID, 1)
	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()
	if _, err := store.get(taskID, 1); !errors.Is(err, errSearchTaskNotFound) {
		t.Fatalf("terminal task should expire after TTL, got %v", err)
	}
}

func TestSearchTaskStoreIndexesOnlyTerminalTasksForCleanup(t *testing.T) {
	now := time.Now()
	store := newSearchTaskStore(time.Minute, func() time.Time { return now })

	for i := 0; i < 1000; i++ {
		taskID := fmt.Sprintf("running-%d", i)
		store.tasks[taskID] = searchTask{ID: taskID, Status: searchTaskStatusRunning, UpdatedAt: now.Add(-2 * time.Minute)}
	}
	store.tasks["expired"] = searchTask{
		ID:             "expired",
		Status:         searchTaskStatusSucceeded,
		UpdatedAt:      now.Add(-2 * time.Minute),
		UserID:         7,
		IdempotencyKey: "expired-key",
	}
	store.tasks["fresh"] = searchTask{ID: "fresh", Status: searchTaskStatusFailed, UpdatedAt: now}
	store.terminalTasks["expired"] = struct{}{}
	store.terminalTasks["fresh"] = struct{}{}
	expiredIndex := searchTaskIdempotencyIndex{UserID: 7, Key: "expired-key"}
	store.idempotency[expiredIndex] = "expired"

	store.mu.Lock()
	store.cleanupExpiredLocked(now)
	store.mu.Unlock()

	if _, ok := store.tasks["running-0"]; !ok {
		t.Fatal("running task should not expire or participate in terminal cleanup")
	}
	if _, ok := store.tasks["expired"]; ok {
		t.Fatal("expired terminal task should be removed")
	}
	if _, ok := store.idempotency[expiredIndex]; ok {
		t.Fatal("expired terminal task idempotency entry should be removed")
	}
	if _, ok := store.tasks["fresh"]; !ok {
		t.Fatal("terminal task younger than TTL should be retained")
	}
	if len(store.terminalTasks) != 1 {
		t.Fatalf("cleanup should retain only fresh terminal index entries, got %d", len(store.terminalTasks))
	}
	if _, ok := store.terminalTasks["fresh"]; !ok {
		t.Fatal("fresh terminal task should remain indexed")
	}
}

func TestSearchTaskStoreIndexesTaskWhenItBecomesTerminal(t *testing.T) {
	now := time.Now()
	store := newSearchTaskStore(time.Minute, func() time.Time { return now })
	store.tasks["task"] = searchTask{ID: "task", Status: searchTaskStatusRunning, UpdatedAt: now}

	store.complete("task", model.SearchResponse{Question: "题目"}, nil)

	if task := store.tasks["task"]; task.Status != searchTaskStatusSucceeded {
		t.Fatalf("expected successful terminal task, got status %q", task.Status)
	}
	if _, ok := store.terminalTasks["task"]; !ok {
		t.Fatal("completed task should be indexed for terminal cleanup")
	}
}

func TestSearchHealthProbeReusesOwnerScopedTaskUntilExpiry(t *testing.T) {
	clock := newLockedAIFallbackTestClock(time.Now())
	store := newSearchTaskStore(time.Minute, clock.Now)
	first := store.healthProbe(71)
	if first.Status != searchTaskStatusSucceeded || first.ID == "" {
		t.Fatalf("expected a completed probe task, got %+v", first)
	}
	if again := store.healthProbe(71); again.ID != first.ID {
		t.Fatalf("probe should be reused during retention, first=%q second=%q", first.ID, again.ID)
	}
	if other := store.healthProbe(72); other.ID == first.ID {
		t.Fatal("different users must not share a probe task")
	}
	if _, err := store.get(first.ID, 72); !errors.Is(err, errSearchTaskForbidden) {
		t.Fatalf("other user must not query probe task: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := store.get(first.ID, 71); !errors.Is(err, errSearchTaskNotFound) {
		t.Fatalf("expired probe should be gone: %v", err)
	}
	if next := store.healthProbe(71); next.ID == first.ID {
		t.Fatal("expired probe must be recreated")
	}
}

func createSearchTaskRequest(t *testing.T, userID int32, idempotencyKey string, body string) searchTaskResponse {
	return createSearchTaskRequestWithStatus(t, userID, idempotencyKey, body, http.StatusAccepted)
}

func createSearchTaskRequestWithStatus(t *testing.T, userID int32, idempotencyKey string, body string, wantStatus int) searchTaskResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/search/tasks?use=stub&noRecord=1", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		ctx.Request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	authorizeSearchTaskFixture(t, ctx, userID)
	CreateSearchTask(ctx)
	if recorder.Code != wantStatus {
		t.Fatalf("create search task: expected HTTP %d, got %d with body %s", wantStatus, recorder.Code, recorder.Body.String())
	}
	var created searchTaskResponse
	if wantStatus == http.StatusAccepted {
		if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
	} else if wantStatus == http.StatusConflict {
		var conflict struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &conflict); err != nil {
			t.Fatalf("decode conflict response: %v", err)
		}
		if conflict.Message == "" {
			t.Fatalf("expected conflict message, got %s", recorder.Body.String())
		}
	}
	return created
}

func waitForSearchTask(t *testing.T, userID int32, taskID string) searchTaskResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := getSearchTaskStatus(t, userID, taskID, http.StatusOK)
		if got.Status != searchTaskStatusRunning {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("search task did not reach a terminal state")
	return searchTaskResponse{}
}

func getSearchTaskStatus(t *testing.T, userID int32, taskID string, wantStatus int) searchTaskResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, searchTaskStatusURL(taskID), nil)
	ctx.Set("user", &entity.User{ID: userID})
	GetSearchTaskStatus(ctx)
	if recorder.Code != wantStatus {
		t.Fatalf("get search task status: expected HTTP %d, got %d with body %s", wantStatus, recorder.Code, recorder.Body.String())
	}
	var response searchTaskResponse
	if wantStatus == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode task status: %v", err)
		}
	}
	return response
}

func waitForStoreSearchTask(t *testing.T, store *searchTaskStore, taskID string, userID int32) searchTask {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := store.get(taskID, userID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.Status != searchTaskStatusRunning {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not complete")
	return searchTask{}
}

// Handler tests use real synthetic credentials rather than a production bypass.
var searchTaskFixtureDBs sync.Map

func authorizeSearchTaskFixture(t *testing.T, c *gin.Context, userID int32) {
	t.Helper()
	value, ok := searchTaskFixtureDBs.Load(t.Name())
	if !ok {
		db := newSearchControllerTestDB(t)
		value, _ = searchTaskFixtureDBs.LoadOrStore(t.Name(), db)
		t.Cleanup(func() { searchTaskFixtureDBs.Delete(t.Name()) })
	}
	db := value.(*gorm.DB)
	var user entity.User
	if err := db.First(&user, userID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		user = seedControllerUser(t, db, userID, fmt.Sprintf("task-owner-%d", userID), "user")
		if err := db.Model(&user).Update("balance", 100).Error; err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	plain := fmt.Sprintf("synthetic-task-key-%d", userID)
	var token entity.AccessToken
	if err := db.Where("user_id = ? AND token_value = ?", userID, plain).First(&token).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		token = seedControllerTokenForUser(t, db, user, plain, 0)
	} else if err != nil {
		t.Fatal(err)
	}
	c.Set("user", &user)
	c.Set("access_token", &token)
	c.Request.Header.Set("Authorization", "Bearer "+plain)
}
