package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestRegistrationBrowserFromStatusToCommittedAccountsAndAlert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newRegisterLevelTestDB(t)
	if err := registry.EnsureRiskSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id <> ?", "device_register_burst").UpdateColumn("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	orig := loadPublicAuthSettings
	t.Cleanup(func() { loadPublicAuthSettings = orig })
	loadPublicAuthSettings = func() (service.PublicAuthSettings, error) {
		return service.PublicAuthSettings{RegisterEnabled: true}, nil
	}
	stubRegisterVerification(t)
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	router.GET("/api/public/register-status", PublicRegisterStatus)
	router.GET("/api/user/register/status", PublicRegistrationBrowserStatus)
	router.POST("/api/user/register", UserRegister)
	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://fixture.invalid"+path, strings.NewReader(body))
		req.RemoteAddr = net.JoinHostPort("203.0.113.13", "12200")
		// Untrusted forwarded values must not be persisted as registration evidence.
		req.Header.Set("X-Forwarded-For", "203.0.113.99")
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	if c := request("GET", "/api/public/register-status", "", nil).Result().Cookies(); len(c) != 0 {
		t.Fatal("ordinary login/site status should not issue registration cookie")
	}
	begin := func() *http.Cookie {
		t.Helper()
		res := request("GET", "/api/user/register/status", "", nil)
		if res.Code != 200 || !strings.Contains(res.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("bootstrap not private: %d %v", res.Code, res.Header())
		}
		cs := res.Result().Cookies()
		if len(cs) != 1 || !cs[0].HttpOnly || !cs[0].Secure || cs[0].Path != "/api/user/register" || cs[0].SameSite != http.SameSiteStrictMode || cs[0].Domain != "" || cs[0].MaxAge != 86400 {
			t.Fatalf("registration cookie scope or lifetime incorrect: %d cookies; body=%s", len(cs), res.Body.String())
		}
		if strings.Contains(res.Body.String(), cs[0].Value) {
			t.Fatal("browser identifier exposed in JSON")
		}
		return cs[0]
	}
	a, b := begin(), begin()
	if a.Value == b.Value {
		t.Fatal("new browsers received same identifier")
	}
	if len(request("GET", "/api/user/register/status", "", a).Result().Cookies()) != 0 {
		t.Fatal("returning registration browser was rotated prematurely")
	}
	submit := func(n int, c *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"username":"browser_fixture_%d","password":"Abc12345!","phone":"1380000%04d","smsCode":"123456","captchaToken":"fixture"}`, n, n)
		res := request("POST", "/api/user/register", body, c)
		if res.Code != want {
			t.Fatalf("registration %d status %d, want %d: %s", n, res.Code, want, res.Body.String())
		}
		return res
	}
	eval := func(want int) {
		t.Helper()
		r, err := service.EvaluateDebtAlerts(db, time.Now())
		if err != nil || r.Created != want {
			t.Fatalf("alerts created=%d, want %d, err=%v", r.Created, want, err)
		}
	}
	submit(1, a, 200)
	submit(2, a, 200)
	submit(3, b, 200)
	// A repeated username is rejected, a legacy client works without cookies,
	// a forged cookie is replaced, and none is included in the count for a.
	submit(1, a, 400)
	if len(submit(4, nil, 200).Result().Cookies()) != 1 {
		t.Fatal("legacy client should receive a fresh cookie without a gate")
	}
	forged := *a
	forged.Value = "invalid"
	if len(submit(5, &forged, 200).Result().Cookies()) != 1 {
		t.Fatal("invalid token not replaced")
	}
	var events int64
	if err := db.Table("registration_browser_events").Count(&events).Error; err != nil || events != 3 {
		t.Fatalf("ineligible registrations counted: %d, %v", events, err)
	}
	eval(0)
	// Third account for a uses the other real public registration transaction.
	// Values are local test fixtures; both captcha and SMS evidence are consumed in SQLite.
	oldStore := wechatRegisterFlowStore
	t.Cleanup(func() { wechatRegisterFlowStore = oldStore })
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	now := time.Now()
	flow, err := wechatRegisterFlowStore.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := wechatRegisterFlowStore.Stage(flow.Token, "synthetic-browser-openid", now); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.CaptchaToken{Token: "fixture-wechat-captcha", Scene: service.CaptchaSceneRegisterSubmit, ChallengeID: "browser-staged", Status: service.CaptchaTokenStatusActive, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SMSVerificationCode{Phone: "13800000006", Scene: service.SMSSceneRegister, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"username":"browser_fixture_6","password":"Abc12345!","phone":"13800000006","smsCode":"123456","captchaToken":"fixture-wechat-captcha","registrationFlowToken":%q}`, flow.Token)
	if res := request("POST", "/api/user/register", body, a); res.Code != 200 {
		t.Fatalf("wechat registration failed: %d %s", res.Code, res.Body.String())
	}
	eval(1)
	eval(0)
	var w entity.RiskWarning
	if err := db.Where("rule_id = ?", "device_register_burst").First(&w).Error; err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(w.Detail), &d); err != nil || w.IP != "203.0.113.13" || d["ipAttribution"] != "single" || d["accountCount"] != float64(3) {
		t.Fatalf("wrong committed source or count: %+v, %v", w, err)
	}
	var hash string
	if err := db.Table("registration_browser_events").Select("browser_hash").Order("user_id").Limit(1).Scan(&hash).Error; err != nil || len(hash) != 64 || hash == a.Value || strings.Contains(w.SubjectKey+w.Detail, a.Value) {
		t.Fatal("persisted/exposed raw cookie or missing hashed key")
	}
	// Recording failure after commit must not turn the real account into an HTTP failure.
	if err := db.Migrator().DropTable("registration_browser_events"); err != nil {
		t.Fatal(err)
	}
	submit(7, a, 200)
	var count int64
	if err := db.Model(&entity.User{}).Where("username = ?", "browser_fixture_7").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("lost a committed user: %d %v", count, err)
	}
}
