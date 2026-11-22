package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestGatewayCertificatePasswordStateDoesNotExposeCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := loadGatewayCertificatePasswordState
	t.Cleanup(func() { loadGatewayCertificatePasswordState = original })
	loadGatewayCertificatePasswordState = func() (service.GatewayCertificatePasswordState, error) {
		return service.GatewayCertificatePasswordState{Count: 2}, nil
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/admin/system-settings/certificate-passwords", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	GatewayCertificatePasswordState(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("secret")) {
		t.Fatalf("response unexpectedly contains a password: %s", recorder.Body.String())
	}
	var response service.GatewayCertificatePasswordState
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Count != 2 {
		t.Fatalf("unexpected metadata response: %s, %v", recorder.Body.String(), err)
	}
}

func TestUpdateGatewayCertificatePasswordsValidatesAndDoesNotEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := saveGatewayCertificatePasswords
	t.Cleanup(func() { saveGatewayCertificatePasswords = original })
	var received []string
	saveGatewayCertificatePasswords = func(passwords []string) (service.GatewayCertificatePasswordState, error) {
		received = passwords
		return service.GatewayCertificatePasswordState{Count: len(passwords)}, nil
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/admin/system-settings/certificate-passwords", bytes.NewBufferString(`{"passwords":["secret-one","secret-two"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	UpdateGatewayCertificatePasswords(ctx)
	if recorder.Code != http.StatusOK || len(received) != 2 {
		t.Fatalf("unexpected update response: %d %s %#v", recorder.Code, recorder.Body.String(), received)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("secret")) {
		t.Fatalf("update response unexpectedly contains a password: %s", recorder.Body.String())
	}
}
