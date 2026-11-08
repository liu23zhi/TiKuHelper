package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestAIFallbackReturnsInvalidInputWhenQuestionMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"   ","type":0,"options":["A","B"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	AIFallback(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Success {
		t.Fatalf("expected success=false, got %+v", got)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeInvalidInput {
		t.Fatalf("expected INVALID_INPUT, got %+v", got.Error)
	}
}

func TestResolveAIFallbackBillingContextPreservesClientIP(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 319, "fallback-source-ip", "user")
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", nil)
	ctx.Request.RemoteAddr = "8.8.8.8:1234"
	ctx.Set("user", &user)
	billing, err := resolveAIFallbackBillingContext(ctx)
	if err != nil || billing.ClientIP != "8.8.8.8" {
		t.Fatalf("expected billing context to retain request IP, got %+v err=%v", billing, err)
	}
}

func TestAIFallbackReturnsInvalidInputWhenJSONMalformed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	AIFallback(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Success {
		t.Fatalf("expected success=false, got %+v", got)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeInvalidInput {
		t.Fatalf("expected INVALID_INPUT, got %+v", got.Error)
	}
}

func TestAIFallbackReturnsAIUnavailableWhenServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 305, "ai-unavailable-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	var receivedRequest model.SearchRequest
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		receivedRequest = req
		return service.BuildAIFallbackUnavailableResult()
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &user)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"],"deepThinking":true}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	AIFallback(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Success {
		t.Fatalf("expected success=false, got %+v", got)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeAIUnavailable {
		t.Fatalf("expected AI_UNAVAILABLE, got %+v", got.Error)
	}
	if !receivedRequest.DeepThinking || receivedRequest.RequestID == "" {
		t.Fatalf("expected deepThinking and requestId to reach fallback service, got %+v", receivedRequest)
	}
}

func TestAIFallbackAcceptsAccessTokenAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "bearer-ai-fallback-token")

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: req.Question,
				Answer:   "2",
			},
		}
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !got.Success || got.Result == nil || got.Result.Answer != "2" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestAIFallbackAcceptsLoginJWTAndContinuesHandlerChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "jwt-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	if err := util.ConfigureJWT("integration-test-secret-at-least-32-bytes-long"); err != nil {
		t.Fatalf("configure jwt: %v", err)
	}
	jwtToken := util.GenerateJwtForUser(user.ID, user.Password)

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.BuildAIFallbackUnavailableResult()
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", jwtToken)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeAIUnavailable {
		t.Fatalf("expected AI_UNAVAILABLE, got %+v", got)
	}
}

func TestAIFallbackRejectsZeroBalanceBeforeCallingAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 301, "zero-balance-ai-fallback-user", "user")
	token := seedControllerTokenForUser(t, db, user, "zero-balance-ai-fallback-token", 0)

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
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
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Success {
		t.Fatalf("expected zero-balance request to fail, got %+v", got)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeInsufficientBalance {
		t.Fatalf("expected INSUFFICIENT_BALANCE, got %+v", got.Error)
	}
	if aiCalls != 0 {
		t.Fatalf("expected no AI call for zero balance, got %d", aiCalls)
	}
	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback failure log: %v", err)
	}
	if entry.UserID != user.ID || entry.Username != user.Username {
		t.Fatalf("unexpected AI fallback log identity: %+v", entry)
	}
	for _, check := range []string{"status=failed", "stage=billing_precheck", "code=INSUFFICIENT_BALANCE"} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
	if strings.TrimSpace(entry.OldAnswer) == "" || strings.TrimSpace(entry.NewAnswer) == "" {
		t.Fatalf("expected request and response snapshots, got %+v", entry)
	}
}

// Wallet balance cannot bypass the user-confirmed Key hard limit.
func TestAIFallbackRejectsExhaustedTokenQuotaWhenBalancePositive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 302, "quota-exhausted-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "quota-exhausted-ai-fallback-token", 1)
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", token.ID).Update("total_quota_limit", 1).Error; err != nil {
		t.Fatalf("seed token quota: %v", err)
	}

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
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
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Success || got.Error == nil || !strings.Contains(got.Error.Message, "额度") {
		t.Fatalf("expected quota-exhausted request to be rejected, got %+v", got)
	}
	if aiCalls != 0 {
		t.Fatalf("expected zero AI calls, got %d", aiCalls)
	}
}

func TestAIFallbackBillsSuccessfulAIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 306, "billed-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "billed-ai-fallback-token", 0)
	// GPT-6 Luna：输入 0.10 / 缓存 0.01 / 输出 0.50（元 / 百万 token）。
	seedControllerAIModelPrice(t, db, "sync-fallback-billing-model", 0.10, 0.01, 0.50)
	enableStagedBilling := true
	if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &enableStagedBilling, nil); err != nil {
		t.Fatalf("enable staged AI billing: %v", err)
	}

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: req.Question,
				Answer:   "2",
			},
			Model: "sync-fallback-billing-model",
			Usage: searchpkg.AISearchUsage{
				InputTokens:  1000000,
				OutputTokens: 500000,
				TotalTokens:  1500000,
				ModelCalls: []searchpkg.AIModelCallUsage{{
					Index:        1,
					Model:        "sync-fallback-billing-model",
					InputTokens:  1000000,
					OutputTokens: 500000,
					TotalTokens:  1500000,
				}},
				Trace: []searchpkg.AISearchTrace{{
					Stage:          "model_response",
					Model:          "sync-fallback-billing-model",
					ModelCallIndex: 1,
					InputTokens:    1000000,
					OutputTokens:   500000,
					TotalTokens:    1500000,
				}},
			},
		}
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !got.Success {
		t.Fatalf("expected successful response, got %+v", got)
	}

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).First(&record).Error; err != nil {
		t.Fatalf("load billing record: %v", err)
	}
	if record.Provider != searchModeAI || record.ModelName != "sync-fallback-billing-model" {
		t.Fatalf("unexpected billing provider/model: %+v", record)
	}
	if record.Amount != 0.105 || record.TotalTokens != 1500000 {
		t.Fatalf("unexpected billing amount/tokens: %+v", record)
	}
	if record.BalanceBefore != 10 || record.BalanceAfter != 9.895 {
		t.Fatalf("unexpected billing balance: %+v", record)
	}
	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback success log: %v", err)
	}
	for _, check := range []string{"status=success", "billingProvider=ai", "model=sync-fallback-billing-model"} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
	for _, check := range []string{`"stageBillingMode":"fixed"`, `"stageMultiplier":0.3`, `"finalMultiplier":0.3`, `"multiplierApplicable":true`} {
		if !strings.Contains(entry.NewAnswer, check) {
			t.Fatalf("expected fallback audit snapshot %q to contain %q", entry.NewAnswer, check)
		}
	}
}

func TestAIFallbackWritesFailureAuditLogWhenAIUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 307, "failed-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "failed-ai-fallback-token", 0)

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.BuildAIFallbackUnavailableResult()
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback failure log: %v", err)
	}
	for _, check := range []string{"status=failed", "stage=result", "code=AI_UNAVAILABLE"} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
}

func TestAIFallbackWritesFailureAuditLogForInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fallbackResultCache.clear()
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 309, "invalid-input-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "invalid-input-ai-fallback-token", 0)

	router := gin.New()
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"   ","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "ai_fallback", 11).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load AI fallback invalid-input log: %v", err)
	}
	for _, check := range []string{"status=failed", "stage=input", "code=INVALID_INPUT"} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
}
