package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestScriptHostRoutesAreSeparateFromOrdinarySite(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := service.ConfigureSiteHosts([]string{"console.independent.test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.ConfigureSiteHosts(nil) })
	policy := service.ScriptDomainSettings{Domains: []string{"*.example.test", "entry.example.test/*"}, DeniedDomains: []string{"tiku.example.test", "blocked.example.test"}}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, policy); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "boundary-owner", "user")
	admin := seedControllerUser(t, db, 0, "boundary-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "boundary-owned-fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "boundary", "fixture.exe", []byte("MZ"), true)
	if err != nil {
		t.Fatal(err)
	}
	makeEntry := func(name, format, address string) service.ScriptListItem {
		t.Helper()
		item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: name, VersionID: version.ID, DomainFormat: format, LauncherOrigin: &service.ScriptLauncherOrigin{Host: "console.independent.test", HTTPS: true}})
		if err != nil {
			t.Fatal(err)
		}
		view, err := service.ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: address, DurationMinutes: 10})
		if err != nil {
			t.Fatal(err)
		}
		if view.Address != "https://"+address {
			t.Fatalf("unexpected address %s", view.Address)
		}
		return item
	}
	a := makeEntry("a", "*.example.test", "a.example.test")
	b := makeEntry("b", "entry.example.test/*", "entry.example.test/old")
	if _, err := service.ConfigureScriptShortAuthorizationWithDB(db, owner, a.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "tiku.example.test", DurationMinutes: 10}); err == nil {
		t.Fatal("allocated the platform host")
	}
	// Use actual short-address ownership and quotas, but return only an inert marker.
	// No launcher bytes, clients, upstream services or executable payloads are used.
	captured := map[int32]service.ScriptExchangeGrant{}
	entry := func(c *gin.Context) bool {
		s, grant, err := service.ConsumeScriptShortAuthorizationWithDB(db, c.Request.Host, c.Request.URL.Path)
		if err != nil {
			if errors.Is(err, service.ErrScriptShortAuthorizationInactive) {
				c.String(404, "unavailable")
				return true
			}
			return false
		}
		captured[s.ID] = grant
		c.String(200, "entry:"+s.Name)
		return true
	}
	r := gin.New()
	r.Use(scriptHostBoundary(db, entry), middleware.CORS)
	r.GET("/readyz", func(c *gin.Context) { c.String(200, "ready") })
	r.GET("/api/normal", func(c *gin.Context) { c.String(200, "normal API") })
	r.GET("/api/Wechat_qc_code", func(c *gin.Context) { c.String(200, "QR") })
	r.HEAD("/api/Wechat_qc_code", func(c *gin.Context) { c.Status(200) })
	r.POST("/api/script-command/:shortCode/heartbeat", func(c *gin.Context) { c.String(401, "existing credential check") })
	r.GET("/api/extension-logs/policy", func(c *gin.Context) { c.String(200, "existing scoped handler") })
	r.NoRoute(func(c *gin.Context) { c.String(200, "normal page") })
	req := func(method, host, path, forwarded, auth, identity string) (int, string) {
		t.Helper()
		q := httptest.NewRequest(method, path, nil)
		q.Host = host
		q.RemoteAddr = "127.0.0.1:10001"
		q.Header.Set("X-Forwarded-Host", forwarded)
		q.Header.Set("Authorization", auth)
		q.Header.Set("X-Script-Identity", identity)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return w.Code, w.Body.String()
	}
	for _, tc := range []struct {
		method, host, path, forwarded string
		code                          int
		body                          string
	}{
		{"GET", "A.Example.Test.:443", "/", "", 200, "entry:a"},
		{"GET", "a.example.test", "/aaa", "", 404, ""},
		{"GET", "entry.example.test", "/old", "", 200, "entry:b"},
		{"GET", "TiKu.Example.Test.:9043", "/", "", 200, "normal page"},
		{"GET", "tiku.example.test", "/api/normal", "", 200, "normal API"},
		{"GET", "tiku.example.test", "/api/Wechat_qc_code", "", 200, "QR"},
		{"GET", "console.independent.test", "/settings", "", 200, "normal page"},
		{"GET", "localhost:8060", "/api/normal", "", 404, ""},
		{"GET", "localhost:8061", "/readyz", "", 200, "ready"},
		{"GET", "a.example.test", "/api/normal", "", 404, ""},
		{"GET", "a.example.test", "/api/Wechat_qc_code", "", 404, ""},
		{"HEAD", "a.example.test", "/api/Wechat_qc_code", "", 404, ""},
		{"GET", "free.example.test", "/", "tiku.example.test", 404, ""},
		{"GET", "not-configured.invalid", "/settings", "tiku.example.test", 404, ""},
		{"GET", "tiku.unknown.invalid", "/api/normal", "", 404, ""},
		{"GET", "blocked.example.test", "/", "", 404, ""},
		{"POST", "a.example.test", "/api/script-command/" + a.ShortCode + "/heartbeat", "", 401, "existing credential check"},
		{"POST", "a.example.test", "/api/script-command/" + b.ShortCode + "/heartbeat", "", 404, ""},
	} {
		if status, body := req(tc.method, tc.host, tc.path, tc.forwarded, "", ""); status != tc.code || body != tc.body {
			t.Errorf("%s %s%s: got %d %q; want %d %q", tc.method, tc.host, tc.path, status, body, tc.code, tc.body)
		}
	}
	if status, _ := req("GET", "a.example.test", "/api/extension-logs/policy", "", "Bearer ordinary-fixture", ""); status != 404 {
		t.Fatalf("ordinary key crossed hosted boundary: %d", status)
	}
	// Gin's historic same-origin slash redirect remains available to ordinary
	// clients. On secondary/unknown hosts it cannot return the ordinary resource.
	for _, tc := range []struct {
		host string
		want int
	}{{"tiku.example.test", 200}, {"a.example.test", 404}, {"free.example.test", 404}} {
		q := httptest.NewRequest("GET", "/api/normal/", nil)
		q.Host = tc.host
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if w.Code != 301 || w.Header().Get("Location") != "/api/normal" {
			t.Fatalf("noncanonical route: %s %d %q", tc.host, w.Code, w.Header().Get("Location"))
		}
		if status, _ := req("GET", tc.host, w.Header().Get("Location"), "", "", ""); status != tc.want {
			t.Fatalf("same-host redirect: %s %d", tc.host, status)
		}
	}
	// Browser preflight carries no Authorization; verify metadata only, no business callback.
	for _, tc := range []struct {
		host, path, method string
		want               int
	}{{"tiku.example.test", "/api/normal", "GET", 204}, {"a.example.test", "/api/search/health", "GET", 204}, {"entry.example.test", "/api/search/health", "GET", 204}, {"free.example.test", "/api/search/health", "GET", 404}, {"a.example.test", "/api/normal", "GET", 404}, {"a.example.test", "/api/Wechat_qc_code", "GET", 404}, {"a.example.test", "/api/search/health", "DELETE", 404}} {
		q := httptest.NewRequest("OPTIONS", tc.path, nil)
		q.Host = tc.host
		q.Header.Set("Origin", "https://console.independent.test")
		q.Header.Set("Access-Control-Request-Method", tc.method)
		q.Header.Set("Access-Control-Request-Headers", "Authorization, X-Script-Identity")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if w.Code != tc.want || w.Body.Len() != 0 || ((w.Header().Get("Access-Control-Allow-Origin") != "") != (tc.want == 204)) {
			t.Errorf("metadata %s %s %s: status %d", tc.host, tc.method, tc.path, w.Code)
		}
	}
	// A genuine first-party grant authenticates only the bound script on this origin.
	exchange, err := service.ExchangeScriptTokenWithDB(db, a.ShortCode, captured[a.ID].Token)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	lease, err := service.ActivateLeasedScriptRunSessionWithDB(db, a.ShortCode, exchange.ActivationToken, identity, "127.0.0.1", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host, id string
		want     int
	}{{"a.example.test", identity, 200}, {"entry.example.test", identity, 404}, {"foreign.invalid", identity, 404}, {"a.example.test", strings.Repeat("b", 64), 404}} {
		if status, _ := req("GET", tc.host, "/api/extension-logs/policy", "", "Bearer "+lease.Token, tc.id); status != tc.want {
			t.Fatalf("bound legitimate lease routing on %s: got %d want %d", tc.host, status, tc.want)
		}
	}
	policy.DeniedDomains = append(policy.DeniedDomains, "a.example.test")
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, policy); err != nil {
		t.Fatal(err)
	}
	if code, _ := req(http.MethodGet, "a.example.test", "/", "", "", ""); code != 404 {
		t.Fatalf("new deny not observed: %d", code)
	}
	if code, _ := req("GET", "a.example.test", "/api/extension-logs/policy", "", "Bearer "+lease.Token, identity); code != 404 {
		t.Fatalf("new deny did not stop even bound authenticated route: %d", code)
	}
	if code, _ := req(http.MethodPost, "a.example.test", "/api/script-command/"+a.ShortCode+"/heartbeat", "", "", ""); code != 404 {
		t.Fatalf("new deny on registered route not observed: %d", code)
	}
}
