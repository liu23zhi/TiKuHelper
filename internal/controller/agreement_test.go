package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"gorm.io/gorm"
)

func TestAgreementHTTPAuthorityAndAudit(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	defer sql.Close()
	if e = registry.EnsureAgreementSchema(db); e != nil {
		t.Fatal(e)
	}
	if e = db.AutoMigrate(&entity.Log{}); e != nil {
		t.Fatal(e)
	}
	oldBudget := enrollmentBudget
	enrollmentBudget = newAgreementEnrollmentBudget()
	defer func() { enrollmentBudget = oldBudget }()
	prev := agreementDB
	agreementDB = func() *gorm.DB { return db }
	defer func() { agreementDB = prev }()
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	role := "user"
	r.Use(func(c *gin.Context) { c.Set("user", &entity.User{ID: 9, Username: "fixture", Role: role}); c.Next() })
	r.POST("/device", EnrollAgreementDevice)
	r.GET("/current", CurrentAgreement)
	r.POST("/accept", SubmitAgreement)
	r.GET("/status", AgreementDeviceStatus)
	r.POST("/drafts", CreateAgreementDraft)
	r.POST("/batch", BatchDeleteAgreementDrafts)
	r.POST("/drafts/:id/publish", PublishAgreementDraft)
	r.GET("/records", AgreementAuditRecords)
	secret := ""
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		q := httptest.NewRequest(method, "https://fixture.invalid"+path, strings.NewReader(body))
		q.RemoteAddr = "127.0.0.7:5099"
		q.Header.Set("X-Forwarded-For", "198.51.100.99")
		q.Header.Set("X-Device-Id", "not-proof")
		q.Header.Set("X-Client-Kind", "exam_client")
		q.Header.Set(agreementDeviceHeader, secret)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if w.Code != want {
			t.Fatalf("%s %s: code %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	plain := httptest.NewRequest("POST", "http://fixture.invalid/device", nil)
	plain.Header.Set("X-Forwarded-Proto", "https")
	plainResponse := httptest.NewRecorder()
	r.ServeHTTP(plainResponse, plain)
	if plainResponse.Code != 403 {
		t.Fatal("untrusted HTTPS proxy header accepted")
	}
	draft := `{"version":"2026.1","summary":"summary","body":"release fixture"}`
	call("POST", "/drafts", draft, 403)
	call("GET", "/records", "", 403)
	role = "admin"
	call("POST", "/drafts", draft, 200)
	call("GET", "/current", "", 404)
	call("POST", "/drafts/1/publish", `{"revision":1,"body":"forged"}`, 400)
	call("POST", "/drafts/1/publish", `{"revision":1}`, 200)
	fetched := call("GET", "/current", "", 200)
	if !strings.Contains(fetched.Body.String(), `"needsAcceptance":null`) {
		t.Fatal("anonymous fetch invented approval")
	}
	call("POST", "/device", `{"deviceId":"other"}`, 400)
	issued := call("POST", "/device", "", 201)
	var d struct{ DeviceID, DeviceCredential string }
	if e = json.Unmarshal(issued.Body.Bytes(), &d); e != nil || d.DeviceID == "" || d.DeviceCredential == "" {
		t.Fatal("missing device pair")
	}
	secret = d.DeviceID
	call("GET", "/status", "", 401)
	secret = d.DeviceCredential
	call("GET", "/status?deviceId=other", "", 400)
	for _, input := range []string{`"userId":9`, `"ip":"198.51.100.99"`, `"acceptedAt":"1970-01-01T00:00:00Z"`, `"deviceId":"other"`, `"clientKind":"exam_client"`} {
		call("POST", "/accept", `{"version":"2026.1",`+input+`}`, 400)
	}
	pending := call("GET", "/records?accepted=false&userId=unbound", "", 200)
	if !strings.Contains(pending.Body.String(), `"acceptedAt":null`) {
		t.Fatal(pending.Body.String())
	}
	call("POST", "/accept", `{"version":"2026.1"}`, 200)
	records := call("GET", "/records?version=2026.1&clientKind=unknown&accepted=true&userId=unbound&deviceId="+d.DeviceID, "", 200)
	if !strings.Contains(records.Body.String(), "127.0.0.7") || strings.Contains(records.Body.String(), "198.51.100.99") || strings.Contains(records.Body.String(), secret) {
		t.Fatal(records.Body.String())
	}
	missing := call("GET", "/records?userId=999&accepted=true", "", 200)
	if missing.Body.String() != "[]" {
		t.Fatal(missing.Body.String())
	}
	call("POST", "/batch", `{"ids":[1,9999]}`, 404)
	call("POST", "/device", "", 201)
	call("POST", "/device", "", 201)
	call("POST", "/device", "", 429)
	var count int64
	db.Model(&entity.Log{}).Where("module = ? AND user_id = ?", "agreement", 9).Count(&count)
	if count != 5 {
		t.Fatalf("admin audit rows %d", count)
	}
	if e = db.Migrator().DropTable(&entity.Log{}); e != nil {
		t.Fatal(e)
	}
	call("POST", "/drafts", `{"version":"v2","summary":"two","body":"must roll back"}`, 503)
	db.Model(&entity.AgreementDraft{}).Count(&count)
	if count != 1 {
		t.Fatal("unaudited draft committed")
	}
}
