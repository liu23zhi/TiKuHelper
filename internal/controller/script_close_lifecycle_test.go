package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestManagedScriptCloseConfirmsRevokesThenSettles(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.ScriptEscrow{}, &entity.ScriptEscrowEntry{}, &entity.ScriptEscrowWork{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "managed-close-owner", "user")
	admin := seedControllerUser(t, db, 0, "managed-close-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "fixture-close-wechat"}).Error; err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "close-fixture", "fixture.exe", []byte("MZsynthetic"), true)
	if err != nil {
		t.Fatal(err)
	}
	script, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{
		Name: "fixture", BaseDomain: "fixture.invalid", VersionID: version.ID, BillingMode: entity.ScriptBillingModeEscrow,
	})
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := service.IssueScriptExchangeTokenWithDB(db, owner, script.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := service.ExchangeScriptTokenWithDB(db, script.ShortCode, exchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("ab", 32)
	grant, err := service.ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, ticket.ActivationToken, identity, "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var session entity.ScriptRunSession
	if err := db.Where("script_id = ?", script.ID).First(&session).Error; err != nil {
		t.Fatal(err)
	}
	const requestID = "fixture-already-executing"
	if err := service.BeginScriptEscrowWorkWithDB(db, script.ID, requestID, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.BeginScriptEscrowWorkWithDB(db, script.ID, requestID, session.ID); !errors.Is(err, service.ErrScriptEscrowWorkDuplicate) {
		t.Fatalf("repeated admission: %v", err)
	}

	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.DELETE("/adapter-service/scripts/:id", func(c *gin.Context) { c.Set("user", &owner) }, DeleteScript)
	r.POST("/adapter-service/script-command/:shortCode/heartbeat", HeartbeatScriptRunSession)
	closeScript := func(confirmation string) *httptest.ResponseRecorder {
		target := fmt.Sprintf("/adapter-service/scripts/%d", script.ID)
		if confirmation != "" {
			target += "?confirmActiveSessionId=" + confirmation
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest(http.MethodDelete, target, nil))
		return out
	}
	heartbeat := func() int {
		out := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, grant.HeartbeatURL, nil)
		request.RemoteAddr = "127.0.0.1:1234"
		request.Header.Set("Authorization", "Bearer "+grant.Token)
		request.Header.Set("X-Script-Identity", identity)
		r.ServeHTTP(out, request)
		return out.Code
	}
	out := closeScript("")
	var confirmation struct {
		Code               string `json:"code"`
		ActiveRunSessionID string `json:"activeRunSessionId"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &confirmation); err != nil || out.Code != http.StatusConflict ||
		confirmation.Code != "SCRIPT_ACTIVE_LEASE_CONFIRM_REQUIRED" || confirmation.ActiveRunSessionID != fmt.Sprint(session.ID) {
		t.Fatalf("unexpected first confirmation response: %d %s", out.Code, out.Body)
	}
	if status := heartbeat(); status != http.StatusOK {
		t.Fatalf("first confirmation must not revoke a client: %d", status)
	}
	if out := closeScript(confirmation.ActiveRunSessionID); out.Code != http.StatusAccepted ||
		!strings.Contains(out.Body.String(), `"state":"settling"`) {
		t.Fatalf("expected pending close: %d %s", out.Code, out.Body)
	}
	if status := heartbeat(); status != http.StatusUnauthorized {
		t.Fatalf("next heartbeat must receive terminal unauthorized: %d", status)
	}
	if err := service.BeginScriptEscrowWorkWithDB(db, script.ID, "fixture-after-close", session.ID); !errors.Is(err, service.ErrScriptEscrowWorkDenied) {
		t.Fatalf("close admitted new work: %v", err)
	}
	if err := service.FinalizeClosingScriptEscrowsWithDB(db, 16); err != nil {
		t.Fatal(err)
	}
	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil || fresh.Balance != 20 {
		t.Fatalf("refund moved wallet while an execution remained: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := service.SettleScriptEscrowMicrosInTx(tx, script.ID, owner.ID, requestID, 12_340_000); err != nil {
			return err
		}
		return service.CompleteScriptEscrowWorkInTx(tx, script.ID, requestID)
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.FinalizeClosingScriptEscrowsWithDB(db, 16); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&fresh, owner.ID).Error; err != nil || fresh.Balance != 87.66 {
		t.Fatalf("expected one residual refund; balance=%v err=%v", fresh.Balance, err)
	}
	if out := closeScript(""); out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"state":"closed"`) {
		t.Fatalf("expected idempotent closed result: %d %s", out.Code, out.Body)
	}
}
