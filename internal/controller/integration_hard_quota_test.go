package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func integrationQuotaFixture(t *testing.T, limit, used, balance float64) (*gorm.DB, entity.User, entity.AccessToken) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// If the baseline wrongly admits a new operation, keep it on synthetic providers
	// so the contract assertion (not an uninitialized manager or network) fails.
	oldBindings := buildSearchClientBindings
	oldLocal := defaultDBSearchProvider
	oldFallback := runAIFallbackService
	buildSearchClientBindings = func(resolvedSearchRequestContext, string) []searchClientBinding { return nil }
	defaultDBSearchProvider = func() func(model.SearchRequest) ([][]string, error) { return (&stubSearchClient{}).SearchAnswer }
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Question: req.Question, Answer: "synthetic-baseline-admission"}}
	}
	t.Cleanup(func() {
		buildSearchClientBindings = oldBindings
		defaultDBSearchProvider = oldLocal
		runAIFallbackService = oldFallback
	})
	db := newSearchControllerTestDB(t)
	oldResolve := resolveSearchRequestContext
	resolveSearchRequestContext = func(a, ip string, q url.Values) (resolvedSearchRequestContext, error) {
		return resolveSearchRequestContextWithDB(db, a, ip, q)
	}
	t.Cleanup(func() { resolveSearchRequestContext = oldResolve })
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	user := seedControllerUser(t, db, 0, "integration-hard-owner", "user")
	if err := db.Model(&user).Update("balance", balance).Error; err != nil {
		t.Fatal(err)
	}
	user.Balance = balance
	token := seedControllerTokenForUser(t, db, user, "integration-hard-key", used)
	if err := db.Model(&token).Update("total_quota_limit", limit).Error; err != nil {
		t.Fatal(err)
	}
	token.TotalQuotaLimit = limit
	return db, user, token
}
func integrationNoFinancialChange(t *testing.T, db *gorm.DB, user entity.User, token entity.AccessToken) {
	t.Helper()
	var u entity.User
	var k entity.AccessToken
	var bills int64
	if err := db.First(&u, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&k, token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if u.Balance != user.Balance || k.TotalAmountUsed != token.TotalAmountUsed || bills != 0 {
		t.Fatalf("rejected write mutated ledger: balance=%v used=%v bills=%d", u.Balance, k.TotalAmountUsed, bills)
	}
}
func TestIntegrationHardQuotaFreshNormalContext(t *testing.T) {
	db, _, token := integrationQuotaFixture(t, 1, 1, 10)
	if _, err := resolveSearchRequestContextWithDB(db, "Bearer "+token.TokenValue, "127.0.0.1", url.Values{}); err == nil || !strings.Contains(err.Error(), "额度") {
		t.Fatalf("HARD_QUOTA_CONTEXT: exhausted key admitted: %v", err)
	}
}
func TestIntegrationHardQuotaFinalAtomicGuard(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		limit, used, balance, charge float64
	}{{"quota", 1, .5, 10, .6}, {"wallet", 0, 0, .5, .6}} {
		t.Run(tc.name, func(t *testing.T) {
			db, u, k := integrationQuotaFixture(t, tc.limit, tc.used, tc.balance)
			err := persistSearchBillingRecordsWithDB(db, searchBillingContext{UserID: u.ID, AccessTokenID: k.ID, RequestID: "hard-final", SearchMode: searchModeNormal, Billable: true}, []searchBillingCharge{{Provider: "local", Amount: tc.charge, Snapshot: "question=fixture"}})
			if err == nil {
				t.Errorf("HARD_FINAL_GUARD: %s overrun committed", tc.name)
			}
			integrationNoFinancialChange(t, db, u, k)
		})
	}
}
func TestIntegrationHardQuotaZeroUnlimitedAndExactBoundary(t *testing.T) {
	for _, limit := range []float64{0, 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			db, u, k := integrationQuotaFixture(t, limit, 0, 1)
			if err := persistSearchBillingRecordsWithDB(db, searchBillingContext{UserID: u.ID, AccessTokenID: k.ID, RequestID: "boundary", SearchMode: searchModeNormal, Billable: true}, []searchBillingCharge{{Provider: "local", Amount: 1}}); err != nil {
				t.Fatal(err)
			}
			var got entity.AccessToken
			if err := db.First(&got, k.ID).Error; err != nil || got.TotalAmountUsed != 1 {
				t.Fatalf("exact boundary/unlimited: %+v %v", got, err)
			}
		})
	}
}
func TestIntegrationHardQuotaCacheAndSkipCannotBypass(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			db, _, k := integrationQuotaFixture(t, 1, 0, 10)
			seedControllerDefaultSearchProviderConfig(t, db, "icodef", 0)
			searchResultCache.clear()
			t.Cleanup(searchResultCache.clear)
			calls := 0
			withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer, func(a, ip string, q url.Values) (resolvedSearchRequestContext, error) {
				return resolveSearchRequestContextWithDB(db, a, ip, q)
			}, func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
				return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) { calls++; return [][]string{{"合成答案"}}, nil }}
			}, nil, func() {
				invoke := func() *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/adapter-service/search?use=icodef&noRecord=1", bytes.NewBufferString(fmt.Sprintf(`{"question":"quota-cache","type":4,"options":[],"skipCache":%t}`, skip)))
					c.Request.Header.Set("Authorization", "Bearer "+k.TokenValue)
					c.Request.Header.Set("Content-Type", "application/json")
					Search(c)
					return w
				}
				first := invoke()
				if first.Code != 200 {
					t.Fatalf("seed result: %d %s", first.Code, first.Body.String())
				}
				if err := db.Model(&entity.AccessToken{}).Where("id = ?", k.ID).Update("total_amount_used", 1).Error; err != nil {
					t.Fatal(err)
				}
				second := invoke()
				if second.Code != 401 || calls != 1 {
					t.Fatalf("HARD_CACHE_GATE: skip=%t status=%d providerCalls=%d body=%s", skip, second.Code, calls, second.Body.String())
				}
			})
		})
	}
}
func TestIntegrationHardQuotaFallbackPrecheckFreshToken(t *testing.T) {
	db, u, k := integrationQuotaFixture(t, 1, 1, 10)
	old := runAIFallbackService
	calls := 0
	runAIFallbackService = func(model.SearchRequest) service.AIFallbackResult {
		calls++
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Answer: "synthetic"}}
	}
	t.Cleanup(func() { runAIFallbackService = old })
	fallbackResultCache.clear()
	// Middleware's token object can be stale; persisted usage is authoritative.
	k.TotalAmountUsed = 0
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &u)
	c.Set("access_token", &k)
	c.Request = httptest.NewRequest("POST", "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"fallback-quota","type":4}`))
	c.Request.Header.Set("Content-Type", "application/json")
	AIFallback(c)
	var got service.AIFallbackResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Success || got.Error == nil || !strings.Contains(got.Error.Message, "额度") || calls != 0 {
		t.Fatalf("HARD_FALLBACK_GATE: %+v calls=%d", got, calls)
	}
	_ = db
}
func TestIntegrationHardQuotaSyncIdempotentDeliveryVersusNewExecution(t *testing.T) {
	db, u, k := integrationQuotaFixture(t, 1, 1, 10)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	body := `{"question":"replayed-quota","type":4}`
	path := "/adapter-service/search?use=icodef&noRecord=1"
	fingerprint := []byte("POST\n/adapter-service/search\nuse=icodef&noRecord=1\n" + body)
	record, _, err := service.ClaimIdempotencyWithDB(db, fmt.Sprintf("sync-search:user:%d:token:%d", u.ID, k.ID), "search", "same-key", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteIdempotencyWithDB(db, record.ID, 200, []byte(`{"qid":"paid-old-result"}`)); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/adapter-service/search", SearchWithIdempotency)
	w := performIdempotentSearchRequest(router, path, body, "same-key", k.TokenValue)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "paid-old-result") {
		t.Fatalf("PAID_IDEMPOTENT_DELIVERY: existing operation not delivered: %d %s", w.Code, w.Body.String())
	}
	oldResolve := resolveSearchRequestContext
	resolveSearchRequestContext = func(a, ip string, q url.Values) (resolvedSearchRequestContext, error) {
		return resolveSearchRequestContextWithDB(db, a, ip, q)
	}
	defer func() { resolveSearchRequestContext = oldResolve }()
	newRequest := performIdempotentSearchRequest(router, path, body, "new-operation", k.TokenValue)
	if newRequest.Code != 401 || !strings.Contains(newRequest.Body.String(), "ACCESS_TOKEN_QUOTA_EXCEEDED") {
		t.Fatalf("NEW_IDEMPOTENT_EXECUTION: new operation bypassed hard quota: %d %s", newRequest.Code, newRequest.Body.String())
	}
	integrationNoFinancialChange(t, db, u, k)
}
func TestIntegrationHardQuotaAsyncDeliveryVersusNewExecution(t *testing.T) {
	db, u, k := integrationQuotaFixture(t, 1, 1, 10)
	old := defaultAIFallbackTaskStore
	store := newAIFallbackTaskStore(time.Minute, time.Now)
	defaultAIFallbackTaskStore = store
	t.Cleanup(func() { defaultAIFallbackTaskStore = old })
	body := `{"question":"async-replayed-quota","type":4}`
	sum := sha256.Sum256([]byte("POST\n/adapter-service/ai-fallback/tasks\n\n" + body))
	fp := hex.EncodeToString(sum[:])
	id, _, err := store.createIdempotent(u.ID, "same-key", fp, func(string) service.AIFallbackResult {
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Answer: "paid"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	eventuallyAIFallbackTaskStatus(t, store, id, u.ID, aiFallbackTaskStatusSucceeded)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &u)
	c.Set("access_token", &k)
	c.Request = httptest.NewRequest("POST", "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Idempotency-Key", "same-key")
	CreateAIFallbackTask(c)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["taskId"] != id || w.Code != 202 {
		t.Fatalf("PAID_ASYNC_DELIVERY: existing operation not delivered: %d %s", w.Code, w.Body.String())
	}
	fresh := httptest.NewRecorder()
	freshContext, _ := gin.CreateTestContext(fresh)
	freshContext.Set("user", &u)
	freshContext.Set("access_token", &k)
	freshContext.Request = httptest.NewRequest("POST", "/adapter-service/ai-fallback/tasks", bytes.NewBufferString(body))
	freshContext.Request.Header.Set("Content-Type", "application/json")
	freshContext.Request.Header.Set("Idempotency-Key", "new-operation")
	CreateAIFallbackTask(freshContext)
	var unexpectedlyCreated aiFallbackTaskResponse
	if json.Unmarshal(fresh.Body.Bytes(), &unexpectedlyCreated) == nil && unexpectedlyCreated.TaskID != "" {
		eventuallyAIFallbackTaskStatus(t, store, unexpectedlyCreated.TaskID, u.ID, aiFallbackTaskStatusSucceeded)
	}
	if !strings.Contains(fresh.Body.String(), "ACCESS_TOKEN_QUOTA_EXCEEDED") || strings.Contains(fresh.Body.String(), "taskId") {
		t.Fatalf("NEW_ASYNC_EXECUTION: new operation bypassed hard quota: %s", fresh.Body.String())
	}
	integrationNoFinancialChange(t, db, u, k)
}
func TestIntegrationHardQuotaPaidStatusStillDelivered(t *testing.T) {
	db, u, k := integrationQuotaFixture(t, 1, 1, 0)
	old := defaultAIFallbackTaskStore
	store := newAIFallbackTaskStore(time.Minute, time.Now)
	defaultAIFallbackTaskStore = store
	t.Cleanup(func() { defaultAIFallbackTaskStore = old })
	id := store.create(u.ID, func(string) service.AIFallbackResult {
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Answer: "already-paid"}}
	})
	eventuallyAIFallbackTaskStatus(t, store, id, u.ID, aiFallbackTaskStatusSucceeded)
	r := gin.New()
	r.GET("/adapter-service/ai-fallback/status", AuthOrAccessToken, GetAIFallbackTaskStatus)
	invoke := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := httptest.NewRequest("GET", "/adapter-service/ai-fallback/status?taskId="+id, nil)
		q.Header.Set("Authorization", "Bearer "+k.TokenValue)
		r.ServeHTTP(w, q)
		return w
	}
	if w := invoke(); w.Code != 200 || !strings.Contains(w.Body.String(), "already-paid") {
		t.Fatalf("paid delivery lost: %d %s", w.Code, w.Body.String())
	}
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", k.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if w := invoke(); w.Code != 401 || strings.Contains(w.Body.String(), "already-paid") {
		t.Fatalf("revoked key got result: %d %s", w.Code, w.Body.String())
	}
}

func TestIntegrationHardQuotaFallbackSettlementErrorIsActionable(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		limit, used, balance float64
	}{{"quota", 1, .5, 10}, {"wallet", 0, 0, .5}} {
		t.Run(tc.name, func(t *testing.T) {
			db, u, k := integrationQuotaFixture(t, tc.limit, tc.used, tc.balance)
			err := persistSearchBillingRecordsWithDB(db, searchBillingContext{UserID: u.ID, AccessTokenID: k.ID, RequestID: "fallback-settlement", SearchMode: searchModeAI, Billable: true}, []searchBillingCharge{{Provider: "ai", Amount: .6}})
			if err == nil {
				t.Fatal("expected rejected settlement")
			}
			got := buildAIFallbackBillingFailureResult(err)
			wantCode := "INSUFFICIENT_BALANCE"
			if tc.name == "quota" {
				wantCode = "ACCESS_TOKEN_QUOTA_EXCEEDED"
			}
			if got.Success || got.Error == nil || string(got.Error.Code) != wantCode {
				t.Fatalf("HARD_ACTIONABLE_BILLING: %+v", got)
			}
			integrationNoFinancialChange(t, db, u, k)
		})
	}
}

func TestIntegrationHardQuotaSearchTaskMissingCredentialCannotStart(t *testing.T) {
	_, u, _ := integrationQuotaFixture(t, 1, 1, 0)
	old := defaultSearchTaskStore
	defaultSearchTaskStore = newSearchTaskStore(time.Minute, time.Now)
	t.Cleanup(func() { defaultSearchTaskStore = old })
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer, func(string, string, url.Values) (resolvedSearchRequestContext, error) {
		return resolvedSearchRequestContext{SearchMode: searchModeNormal}, nil
	}, func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) { return nil }, nil, func() {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("user", &u)
		c.Request = httptest.NewRequest("POST", "/adapter-service/search/tasks?noRecord=1", bytes.NewBufferString(`{"question":"missing-key-task","type":4}`))
		c.Request.Header.Set("Content-Type", "application/json")
		CreateSearchTask(c)
		if w.Code == 202 {
			var created searchTaskResponse
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			_ = waitForSearchTask(t, u.ID, created.TaskID)
		}
		if w.Code != 401 {
			t.Fatalf("HARD_TASK_NO_TEST_BYPASS: missing credential started work: %d %s", w.Code, w.Body.String())
		}
	})
}

func TestIntegrationHardQuotaMachineCodeAcrossNewSpendingEndpoints(t *testing.T) {
	for _, path := range []string{"/adapter-service/search", "/adapter-service/ai-fallback", "/adapter-service/ai-fallback/tasks"} {
		t.Run(path, func(t *testing.T) {
			db, u, k := integrationQuotaFixture(t, 1, 1, 10)
			oldResolve := resolveSearchRequestContext
			resolveSearchRequestContext = func(a, ip string, q url.Values) (resolvedSearchRequestContext, error) {
				return resolveSearchRequestContextWithDB(db, a, ip, q)
			}
			t.Cleanup(func() { resolveSearchRequestContext = oldResolve })
			oldFallback := runAIFallbackService
			runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
				return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Answer: "synthetic-provider-must-not-be-used"}}
			}
			t.Cleanup(func() { runAIFallbackService = oldFallback })
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("user", &u)
			c.Set("access_token", &k)
			c.Request = httptest.NewRequest("POST", path, bytes.NewBufferString(`{"question":"quota-machine-code","type":4}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Authorization", "Bearer "+k.TokenValue)
			if path == "/adapter-service/search" {
				Search(c)
			} else if path == "/adapter-service/ai-fallback" {
				AIFallback(c)
			} else {
				CreateAIFallbackTask(c)
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			code, _ := got["code"].(string)
			if path != "/adapter-service/search" {
				if detail, ok := got["error"].(map[string]any); ok {
					code, _ = detail["code"].(string)
				}
				if id, ok := got["taskId"].(string); ok && id != "" {
					eventuallyAIFallbackTaskStatus(t, defaultAIFallbackTaskStore, id, u.ID, aiFallbackTaskStatusSucceeded)
					t.Fatal("rejected spending created task")
				}
			}
			if code != "ACCESS_TOKEN_QUOTA_EXCEEDED" {
				t.Fatalf("HARD_MACHINE_CODE: %s HTTP%d got=%s", path, w.Code, w.Body.String())
			}
		})
	}
}

func TestIntegrationHardQuotaNormalTaskPaidDeliveryAndNewAdmission(t *testing.T) {
	db, u, k := integrationQuotaFixture(t, 1, 1, 0)
	old := defaultSearchTaskStore
	store := newSearchTaskStore(time.Minute, time.Now)
	defaultSearchTaskStore = store
	t.Cleanup(func() { defaultSearchTaskStore = old })
	body := `{"question":"same-task","type":4}`
	path := "/adapter-service/search/tasks?noRecord=1"
	original := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	original.Header.Set("Authorization", "Bearer "+k.TokenValue)
	id, _, err := store.create(u.ID, "same-key", fingerprintSearchTaskRequest(original, body), func() (model.SearchResponse, error) {
		var result model.SearchResponse
		result.Question = "paid-normal"
		return result, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = waitForSearchTask(t, u.ID, id)
	r := gin.New()
	r.POST("/adapter-service/search/tasks", AuthAccessToken, CreateSearchTask)
	r.GET("/adapter-service/search/status", AuthAccessToken, GetSearchTaskStatus)
	submit := func(key string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		q.Header.Set("Authorization", "Bearer "+k.TokenValue)
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Idempotency-Key", key)
		r.ServeHTTP(w, q)
		if w.Code == 202 {
			var response searchTaskResponse
			if json.Unmarshal(w.Body.Bytes(), &response) == nil && response.TaskID != "" {
				_ = waitForSearchTask(t, u.ID, response.TaskID)
			}
		}
		return w
	}
	if w := submit("same-key"); w.Code != 202 || !strings.Contains(w.Body.String(), id) || !strings.Contains(w.Body.String(), "paid-normal") {
		t.Fatalf("paid task replay lost at zero budget: %d %s", w.Code, w.Body.String())
	}
	if w := submit("new-wallet-operation"); w.Code != 401 || !strings.Contains(w.Body.String(), "INSUFFICIENT_BALANCE") || strings.Contains(w.Body.String(), "taskId") {
		t.Fatalf("new task bypassed empty wallet: %d %s", w.Code, w.Body.String())
	}
	if err := db.Model(&entity.User{}).Where("id = ?", u.ID).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	if w := submit("new-quota-operation"); w.Code != 401 || !strings.Contains(w.Body.String(), "ACCESS_TOKEN_QUOTA_EXCEEDED") || strings.Contains(w.Body.String(), "taskId") {
		t.Fatalf("new task bypassed key cap: %d %s", w.Code, w.Body.String())
	}
	get := func(secret string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := httptest.NewRequest("GET", "/adapter-service/search/status?taskId="+id, nil)
		q.Header.Set("Authorization", "Bearer "+secret)
		r.ServeHTTP(w, q)
		return w
	}
	if err := db.Model(&entity.User{}).Where("id = ?", u.ID).Update("balance", 0).Error; err != nil {
		t.Fatal(err)
	}
	if w := get(k.TokenValue); w.Code != 200 || !strings.Contains(w.Body.String(), "paid-normal") {
		t.Fatalf("paid status lost: %d %s", w.Code, w.Body.String())
	}
	other := seedControllerUser(t, db, 0, "other-paid-owner", "user")
	otherKey := seedControllerTokenForUser(t, db, other, "other-paid-token", 0)
	if w := get(otherKey.TokenValue); w.Code != 403 || strings.Contains(w.Body.String(), "paid-normal") {
		t.Fatalf("outsider got paid result: %d %s", w.Code, w.Body.String())
	}
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", k.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if w := get(k.TokenValue); w.Code != 401 || strings.Contains(w.Body.String(), "paid-normal") {
		t.Fatalf("revoked token got paid result: %d %s", w.Code, w.Body.String())
	}
	store.mu.Lock()
	count := len(store.tasks)
	store.mu.Unlock()
	if count != 1 {
		t.Fatalf("rejected new submissions added tasks: %d", count)
	}
}

func TestIntegrationHardQuotaZeroLimitAllowsNewExecution(t *testing.T) {
	for _, path := range []string{"/adapter-service/search", "/adapter-service/ai-fallback", "/adapter-service/ai-fallback/tasks"} {
		t.Run(path, func(t *testing.T) {
			db, u, k := integrationQuotaFixture(t, 0, 100, 10)
			seedControllerDefaultSearchProviderConfig(t, db, "icodef", 0)
			searchResultCache.clear()
			fallbackResultCache.clear()
			oldFallback := runAIFallbackService
			calls := 0
			runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
				calls++
				return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Question: req.Question, Answer: "unlimited"}}
			}
			t.Cleanup(func() { runAIFallbackService = oldFallback })
			oldStore := defaultAIFallbackTaskStore
			defaultAIFallbackTaskStore = newAIFallbackTaskStore(time.Minute, time.Now)
			t.Cleanup(func() { defaultAIFallbackTaskStore = oldStore })
			withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer, func(a, ip string, q url.Values) (resolvedSearchRequestContext, error) {
				return resolveSearchRequestContextWithDB(db, a, ip, q)
			}, func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
				return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) { calls++; return [][]string{{"unlimited"}}, nil }}
			}, nil, func() {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Set("user", &u)
				c.Set("access_token", &k)
				c.Request = httptest.NewRequest("POST", path+"?use=icodef&noRecord=1", bytes.NewBufferString(`{"question":"zero-limit","type":4}`))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Request.Header.Set("Authorization", "Bearer "+k.TokenValue)
				if path == "/adapter-service/search" {
					Search(c)
				} else if path == "/adapter-service/ai-fallback" {
					AIFallback(c)
				} else {
					CreateAIFallbackTask(c)
				}
				if path == "/adapter-service/ai-fallback/tasks" {
					var created aiFallbackTaskResponse
					if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 202 || created.TaskID == "" {
						t.Fatalf("0 means unlimited task: %d %s %v", w.Code, w.Body.String(), err)
					}
					eventuallyAIFallbackTaskStatus(t, defaultAIFallbackTaskStore, created.TaskID, u.ID, aiFallbackTaskStatusSucceeded)
				} else if w.Code != 200 || !strings.Contains(w.Body.String(), "unlimited") {
					t.Fatalf("0 means unlimited: %d %s", w.Code, w.Body.String())
				}
				if calls != 1 {
					t.Fatalf("unlimited new execution providerCalls=%d", calls)
				}
			})
		})
	}
}
