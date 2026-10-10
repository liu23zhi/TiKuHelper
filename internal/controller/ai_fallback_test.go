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
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestAIFallbackReturnsInvalidInputWhenQuestionMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

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

func TestAIFallbackReturnsInvalidInputWhenJSONMalformed(t *testing.T) {
	gin.SetMode(gin.TestMode)

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
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 305, "ai-unavailable-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		return service.BuildAIFallbackUnavailableResult()
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &user)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
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
}

func TestAIFallbackAcceptsAccessTokenAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "bearer-ai-fallback-token")

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: question,
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
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
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
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 301, "zero-balance-ai-fallback-user", "user")
	token := seedControllerTokenForUser(t, db, user, "zero-balance-ai-fallback-token", 0)

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	aiCalls := 0
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		aiCalls++
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: question,
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

func TestAIFallbackRejectsExhaustedTokenQuotaBeforeCallingAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		aiCalls++
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: question,
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
		t.Fatalf("expected quota-exhausted request to fail, got %+v", got)
	}
	if got.Error == nil || got.Error.Code != service.AIFallbackErrorCodeInsufficientBalance {
		t.Fatalf("expected INSUFFICIENT_BALANCE, got %+v", got.Error)
	}
	if aiCalls != 0 {
		t.Fatalf("expected no AI call for exhausted quota, got %d", aiCalls)
	}
}

func TestAIFallbackBillsSuccessfulAIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 306, "billed-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "billed-ai-fallback-token", 0)
	// GPT-6 Luna：输入 0.10 / 缓存 0.01 / 输出 0.50（元 / 百万 token）。
	seedControllerAIModelPrice(t, db, "sync-fallback-billing-model", 0.10, 0.01, 0.50)

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result: &service.AIFallbackAnswer{
				Question: question,
				Answer:   "2",
			},
			Model: "sync-fallback-billing-model",
			Usage: searchpkg.AISearchUsage{
				InputTokens:  1000000,
				OutputTokens: 500000,
				TotalTokens:  1500000,
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
	if record.Amount != 0.35 || record.TotalTokens != 1500000 {
		t.Fatalf("unexpected billing amount/tokens: %+v", record)
	}
	if record.BalanceBefore != 10 || record.BalanceAfter != 9.65 {
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
}

func TestAIFallbackWritesFailureAuditLogWhenAIUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 307, "failed-ai-fallback-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "failed-ai-fallback-token", 0)

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
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
