package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestManagedScriptTLSActivationAndHeartbeatWire(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "native-tls-owner", "user")
	admin := seedControllerUser(t, db, 0, "native-tls-admin", "admin")
	phone, wechat := "13800000000", "synthetic-tls-account"
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": phone, "wechat_id": wechat}).Error; err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "tls-fixture", "fixture.exe", []byte("MZsynthetic"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{
		Name: "tls fixture", BaseDomain: "fixture.invalid", VersionID: version.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := service.IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := service.ExchangeScriptTokenWithDB(db, item.ShortCode, exchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.POST("/api/script-command/:shortCode/activate", ActivateScriptRunSession)
	r.POST("/api/script-command/:shortCode/heartbeat", HeartbeatScriptRunSession)
	server := httptest.NewTLSServer(r)
	defer server.Close()

	identity := strings.Repeat("ab", 32)
	body, _ := json.Marshal(map[string]any{"activationToken": ticket.ActivationToken, "identity": identity, "durationMinutes": 1})
	response, err := server.Client().Post(server.URL+"/api/script-command/"+item.ShortCode+"/activate",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var grant service.ScriptRunSessionGrant
	err = json.NewDecoder(response.Body).Decode(&grant)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || response.TLS == nil || grant.Token == "" ||
		!strings.HasSuffix(grant.HeartbeatURL, "/heartbeat") || time.Until(grant.ExpiresAt) > time.Minute {
		t.Fatalf("invalid native TLS activation status=%d err=%v", response.StatusCode, err)
	}
	sendHeartbeat := func(proof string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, server.URL+grant.HeartbeatURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+grant.Token)
		req.Header.Set("X-Script-Identity", proof)
		req.Header.Set("X-Forwarded-For", "10.255.255.8") // ignored by the untrusted-peer policy
		return server.Client().Do(req)
	}
	response, err = sendHeartbeat(identity)
	if err != nil {
		t.Fatal(err)
	}
	var heartbeat struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}
	err = json.NewDecoder(response.Body).Decode(&heartbeat)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !heartbeat.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatalf("invalid native TLS heartbeat status=%d err=%v", response.StatusCode, err)
	}
	response, err = sendHeartbeat(strings.Repeat("bc", 32))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("wrong run identity returned %d", response.StatusCode)
	}
}
