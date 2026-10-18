package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestPublicWechatRegisterSessionDoesNotAcceptJWTAsFlowToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := wechatRegisterFlowStore
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	defer func() { wechatRegisterFlowStore = previous }()
	router := gin.New()
	router.POST("/adapter-service/public/wechat-register/session", PublicWechatRegisterSession)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-register/session", bytes.NewBufferString(`{"registrationFlowToken":"jwt-looking-token"}`))
	request.Header.Set("Authorization", "Bearer login-jwt")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected session creation, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["wechatId"]; ok {
		t.Fatal("registration response must not expose wechatId")
	}
	if _, ok := payload["registrationFlowToken"]; !ok {
		t.Fatalf("expected opaque registration token in response: %s", recorder.Body.String())
	}
}

func TestPublicWechatRegisterRoutesRejectClientWechatIDAndExposeOpaqueStatusOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := wechatRegisterFlowStore
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	defer func() { wechatRegisterFlowStore = previous }()
	router := gin.New()
	router.POST("/adapter-service/public/wechat-register/session", PublicWechatRegisterSession)
	router.POST("/adapter-service/public/wechat-register/code", PublicWechatRegisterCode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-register/code", bytes.NewBufferString(`{"registrationFlowToken":"bad","wechatId":"client-supplied"}`))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid flow token rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("client-supplied")) {
		t.Fatal("client wechatId must not be echoed")
	}
}
