package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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

	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
		return service.BuildAIFallbackUnavailableResult()
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
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
