package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestAgreementPublicContactsAndOfficialPublish(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rawDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	rawDB.SetMaxOpenConns(1)
	defer rawDB.Close()
	if err := registry.EnsureAgreementSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.Log{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: "unrelated_fixture_config", Value: "must-not-be-public"}).Error; err != nil {
		t.Fatal(err)
	}
	oldDB := agreementDB
	agreementDB = func() *gorm.DB { return db }
	defer func() { agreementDB = oldDB }()
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	role := ""
	r.Use(func(c *gin.Context) {
		if role != "" {
			c.Set("user", &entity.User{ID: 9, Username: "test admin", Role: role})
		}
		c.Next()
	})
	r.GET("/contacts", AgreementPublicContacts)
	r.GET("/admin/contacts", AgreementAdminContacts)
	r.PUT("/admin/contacts", UpdateAgreementAdminContacts)
	r.GET("/admin/template", AgreementDiagnosticTemplate)
	r.GET("/current", CurrentAgreement)
	r.POST("/drafts", CreateAgreementDraft)
	r.POST("/drafts/:id/publish", PublishAgreementDraft)
	call := func(method, path, body string, code int) string {
		t.Helper()
		req := httptest.NewRequest(method, "https://fixture.invalid"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.7:5099"
		got := httptest.NewRecorder()
		r.ServeHTTP(got, req)
		if got.Code != code {
			t.Fatalf("%s %s status = %d, want %d (%s)", method, path, got.Code, code, got.Body)
		}
		if method == "GET" && path == "/contacts" && got.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("public catalog may not cache removed contact information")
		}
		return got.Body.String()
	}
	empty := `{"channels":[]}`
	if got := call("GET", "/contacts", "", 200); got != empty {
		t.Fatal("no contact should be invented", got)
	}
	call("PUT", "/contacts", empty, 404)
	call("PUT", "/admin/contacts", empty, 401)
	call("GET", "/admin/template", "", 401)
	role = "user"
	call("GET", "/admin/contacts", "", 403)
	call("PUT", "/admin/contacts", empty, 403)
	role = "admin"
	body := call("GET", "/admin/template", "", 200)
	var tpl entity.AgreementContent
	if json.Unmarshal([]byte(body), &tpl) != nil ||
		!service.IsApprovedDiagnosticAgreement(tpl.Version, service.AgreementContentHash(tpl.Body)) ||
		service.IsApprovedDiagnosticAgreement(tpl.Version, service.AgreementContentHash(tpl.Body+"\n")) ||
		service.IsApprovedDiagnosticAgreement("unapproved", service.AgreementContentHash(tpl.Body)) {
		t.Fatal("template's exact version and body hash must bind the T08 authorization gate")
	}
	for _, rule := range []string{"故障", "24 小时", "7×24 小时", "10 GB", "/agreement-support", "received_at", "不用于营销"} {
		if !strings.Contains(tpl.Body, rule) {
			t.Fatalf("approved notice is missing %s", rule)
		}
	}
	call("GET", "/current", "", 404)
	save := func(content entity.AgreementContent) string {
		t.Helper()
		raw, e := json.Marshal(content)
		if e != nil {
			t.Fatal(e)
		}
		var d entity.AgreementDraft
		if json.Unmarshal([]byte(call("POST", "/drafts", string(raw), 200)), &d) != nil || d.ID == 0 || d.Revision != 1 {
			t.Fatal("missing saved draft")
		}
		return "/drafts/" + strconv.FormatInt(d.ID, 10) + "/publish"
	}
	mutated := tpl
	mutated.Body += "different text"
	const pub = `{"revision":1}`
	call("POST", save(mutated), pub, 400)
	publish := save(tpl)
	if msg := call("POST", publish, pub, 409); !strings.Contains(msg, "contact_required") {
		t.Fatal("missing precise publication blocker", msg)
	}
	call("GET", "/current", "", 404)
	for _, invalid := range []string{
		`{"channels":null}`,
		`{"channels":[],"token":"no"}`,
		`{"channels":[{"kind":"wechat_official","value":"<img>"}]}`,
		`{"channels":[{"kind":"wecom","value":"https://example.invalid/webhook"}]}`,
		`{"channels":[{"kind":"email","value":"good@example.invalid\r\nBCC: bad@example.invalid"}]}`,
		`{"channels":[{"kind":"wecom","value":"客服"},{"kind":"wecom","value":"重复"}]}`,
	} {
		call("PUT", "/admin/contacts", invalid, 400)
	}
	contacts := `{"channels":[{"kind":"email","value":"fixture@example.invalid"},{"kind":"wecom","value":"企业客服"},{"kind":"wechat_official","value":"公众号客服"}]}`
	if wrote := call("PUT", "/admin/contacts", contacts, 200); wrote != contacts {
		t.Fatal("unexpected write receipt")
	}
	call("POST", publish, pub, 200)
	role = ""
	if got := call("GET", "/contacts", "", 200); got != contacts {
		t.Fatal("anonymous response must contain the three configured channels and nothing else")
	}
	current := call("GET", "/current", "", 200)
	var shown struct {
		Version string `json:"version"`
		Body    string `json:"body"`
		Hash    string `json:"content_hash"`
	}
	if json.Unmarshal([]byte(current), &shown) != nil || shown.Body != tpl.Body ||
		!service.IsApprovedDiagnosticAgreement(shown.Version, shown.Hash) ||
		!strings.Contains(current, `"needsAcceptance":null`) {
		t.Fatal("published body is shared, not an automatic consent")
	}
	role = "admin"
	call("PUT", "/admin/contacts", empty, 409)
	if err := db.Migrator().DropTable(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/admin/contacts", `{"channels":[{"kind":"email","value":"uncommitted@example.invalid"}]}`, 503)
	role = ""
	if got := call("GET", "/contacts", "", 200); got != contacts {
		t.Fatal("failed admin audit must roll back contact mutation")
	}
}
