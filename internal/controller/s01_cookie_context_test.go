package controller

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCookieContextSourceAndBoundary(t *testing.T) {
	restoreS01CookieLoginGlobals(t)
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	closeS01CookieLoginDB(t, db)
	if err := db.AutoMigrate(&entity.AccessToken{}); err != nil {
		t.Fatal(err)
	}
	password, err := util.HashPassword("Cookie-Fixture-123!")
	if err != nil {
		t.Fatal(err)
	}
	user := entity.User{ID: 701, Username: "cookie-fixture", Password: password, Role: "user", Status: "enabled", RegisterSource: "system"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := util.GenerateJwtForUser(user.ID, user.Password)
	access := "S01-ONLY-SYNTHETIC-ACCESS-TOKEN"
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(access))))
	if err := db.Create(&entity.AccessToken{UserID: user.ID, TokenHash: hex.EncodeToString(sum[:]), Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, method, origin, site, authorization, host string
		tls, accessRoute                                bool
		status                                          int
	}{
		{name: "legacy-no-metadata", method: "GET", status: 200},
		{name: "same-http", method: "GET", origin: "http://service.fixture.invalid", status: 200},
		{name: "foreign-get", method: "GET", origin: "https://foreign.fixture.invalid", status: 403},
		{name: "foreign-post", method: "POST", origin: "https://foreign.fixture.invalid", status: 403},
		{name: "cross-site-without-origin", method: "GET", site: "cross-site", status: 403},
		{name: "contradictory-cross-site", method: "GET", origin: "http://service.fixture.invalid", site: "cross-site", status: 403},
		{name: "null-origin", method: "GET", origin: "null", status: 403},
		{name: "extension-origin", method: "GET", origin: "chrome-extension://fixture", status: 403},
		{name: "origin-path", method: "GET", origin: "http://service.fixture.invalid/path", status: 403},
		{name: "foreign-port", method: "GET", origin: "http://service.fixture.invalid:8080", status: 403},
		{name: "same-tls", method: "GET", origin: "https://service.fixture.invalid", tls: true, status: 200},
		{name: "direct-tls-scheme-mismatch", method: "GET", origin: "http://service.fixture.invalid", tls: true, status: 403},
		{name: "nil-tls-same-host-https-compatibility-boundary", method: "GET", origin: "https://service.fixture.invalid", status: 200},
		{name: "same-site-no-origin-denied", method: "GET", site: "same-site", status: 403},
		{name: "same-site-conflicting-same-origin-denied", method: "GET", origin: "http://service.fixture.invalid", site: "same-site", status: 403},
		{name: "invalid-metadata-no-origin-kept", method: "GET", site: "future-unknown", status: 200},
		{name: "raw-JWT-same-site-unaffected", method: "GET", site: "same-site", authorization: token, status: 200},
		{name: "none-no-origin-kept", method: "GET", site: "none", status: 200},
		{name: "raw-JWT-header-priority", method: "GET", origin: "https://foreign.fixture.invalid", site: "cross-site", authorization: token, status: 200},
		{name: "bad-header-no-cookie-fallback", method: "GET", origin: "https://foreign.fixture.invalid", authorization: "bad", status: 401},
		{name: "Bearer-loginJWT-remains-unsupported", method: "GET", authorization: "Bearer " + token, status: 401},
		{name: "Bearer-access-token-priority", method: "GET", origin: "https://foreign.fixture.invalid", site: "cross-site", authorization: "Bearer " + access, accessRoute: true, status: 200},
		{name: "Basic-access-token-priority", method: "GET", origin: "https://foreign.fixture.invalid", site: "cross-site", authorization: "Basic " + access, accessRoute: true, status: 200},
		{name: "raw-access-token-priority", method: "GET", origin: "https://foreign.fixture.invalid", site: "cross-site", authorization: access, accessRoute: true, status: 200},
	}
	cases = append(cases,
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "origin-empty-fragment", method: "GET", origin: "http://service.fixture.invalid#", status: 403},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "target-empty-fragment", method: "GET", host: "service.fixture.invalid#", origin: "http://service.fixture.invalid", status: 403},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "origin-userinfo", method: "GET", origin: "http://user@service.fixture.invalid", status: 403},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "origin-query-empty", method: "GET", origin: "http://service.fixture.invalid?", status: 403},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "origin-port-zero", method: "GET", origin: "http://service.fixture.invalid:0", status: 403},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "normalized-default-port-case", method: "GET", host: "SERVICE.FIXTURE.INVALID:80", origin: "http://service.fixture.invalid", status: 200},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "ipv6-same-host", method: "GET", host: "[::1]", origin: "http://[::1]", status: 200},
		struct {
			name, method, origin, site, authorization, host string
			tls, accessRoute                                bool
			status                                          int
		}{name: "mapped-ipv6-same-host", method: "GET", host: "[::ffff:127.0.0.1]", origin: "http://[::ffff:127.0.0.1]", status: 200},
	)

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			router := gin.New()
			auth := Auth
			if tt.accessRoute {
				auth = AuthOrAccessToken
			}
			router.Any("/private", auth, func(c *gin.Context) { calls++; c.JSON(200, gin.H{"ok": true}) })
			req := httptest.NewRequest(tt.method, "http://service.fixture.invalid/private", nil)
			req.Host = "service.fixture.invalid"
			if tt.host != "" {
				req.Host = tt.host
			}
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			req.AddCookie(&http.Cookie{Name: "tiku_token", Value: token})
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.site != "" {
				req.Header.Set("Sec-Fetch-Site", tt.site)
			}
			req.Header.Set("Forwarded", "proto=https;host=foreign.fixture.invalid")
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", "foreign.fixture.invalid")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			t.Logf("cookieCase=%s status=%d handlerCalls=%d", tt.name, rec.Code, calls)
			if rec.Code != tt.status {
				t.Errorf("status=%d want=%d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			if tt.status != 200 && calls != 0 {
				t.Errorf("denied request reached side-effect handler: %d", calls)
			}
		})
	}
}

type s01CookieLoginNoDial struct{ calls *int }

func (n s01CookieLoginNoDial) RoundTrip(*http.Request) (*http.Response, error) {
	*n.calls++
	return nil, fmt.Errorf("S01_COOKIE_LOGIN_UNEXPECTED_NETWORK")
}
func restoreS01CookieLoginGlobals(t *testing.T) {
	priorQ := dao.Q
	var priorValue dao.Query
	if priorQ != nil {
		priorValue = *priorQ
	}
	priorLog, priorTiku, priorUser := dao.Log, dao.Tiku, dao.User
	mode := gin.Mode()
	transport := http.DefaultTransport
	calls := 0
	http.DefaultTransport = s01CookieLoginNoDial{&calls}
	t.Cleanup(func() {
		dao.Q = priorQ
		if priorQ != nil {
			*priorQ = priorValue
		}
		dao.Log, dao.Tiku, dao.User = priorLog, priorTiku, priorUser
		gin.SetMode(mode)
		http.DefaultTransport = transport
		if dao.Q != priorQ || priorQ != nil && !reflect.DeepEqual(*dao.Q, priorValue) || dao.Log != priorLog || dao.Tiku != priorTiku || dao.User != priorUser || gin.Mode() != mode || reflect.ValueOf(http.DefaultTransport) != reflect.ValueOf(transport) || calls != 0 {
			t.Errorf("DAO/transport/mode restoration or no-dial invariant failed")
		}
	})
}

// TestCookieContextMetadataCombinations exercises real authentication entry
// points: a recognized cross-origin token in any field rejects only Cookie auth.
func TestCookieContextMetadataCombinations(t *testing.T) {
	restoreS01CookieLoginGlobals(t)
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	closeS01CookieLoginDB(t, db)
	if err := db.AutoMigrate(&entity.AccessToken{}); err != nil {
		t.Fatal(err)
	}
	password, err := util.HashPassword("Metadata-Fixture-123!")
	if err != nil {
		t.Fatal(err)
	}
	user := entity.User{ID: 702, Username: "metadata-fixture", Password: password, Role: "user", Status: "enabled", RegisterSource: "system"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := util.GenerateJwtForUser(user.ID, user.Password)
	access := "S01-ONLY-SYNTHETIC-METADATA-ACCESS"
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(access))))
	if err := db.Create(&entity.AccessToken{UserID: user.ID, TokenHash: hex.EncodeToString(sum[:]), Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name          string
		values        []string
		authorization string
		accessRoute   bool
		status        int
	}{
		{name: "duplicate-cross-site-last", values: []string{"same-origin", "cross-site"}, status: 403},
		{name: "duplicate-cross-site-first", values: []string{"cross-site", "same-origin"}, status: 403},
		{name: "comma-cross-site-first", values: []string{"cross-site, same-origin"}, status: 403},
		{name: "comma-same-site-last", values: []string{"same-origin, same-site"}, status: 403},
		{name: "OWS-case-recognized", values: []string{"future-unknown,  SaMe-SiTe \t"}, status: 403},
		{name: "multiple-fields-comma-recognized", values: []string{"future-unknown", "none, CROSS-SITE"}, status: 403},
		{name: "duplicate-same-origin-compatible", values: []string{"same-origin", "same-origin"}, status: 200},
		{name: "comma-unknown-compatible", values: []string{"future-one, future-two"}, status: 200},
		{name: "general-invalid-compatible", values: []string{"not a token;"}, status: 200},
		{name: "empty-metadata-compatible", values: []string{"", " , "}, status: 200},
		{name: "no-metadata-compatible", status: 200},
		{name: "raw-JWT-header-priority-unaffected", values: []string{"cross-site, same-site"}, authorization: token, status: 200},
		{name: "Bearer-access-token-priority-unaffected", values: []string{"cross-site", "same-site"}, authorization: "Bearer " + access, accessRoute: true, status: 200},
		{name: "bad-header-no-cookie-fallback", values: []string{"cross-site, same-origin"}, authorization: "bad", status: 401},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			router := gin.New()
			auth := Auth
			if tt.accessRoute {
				auth = AuthOrAccessToken
			}
			router.GET("/private", auth, func(c *gin.Context) { calls++; c.JSON(200, gin.H{"ok": true}) })
			req := httptest.NewRequest("GET", "http://service.fixture.invalid/private", nil)
			req.AddCookie(&http.Cookie{Name: "tiku_token", Value: token})
			for _, v := range tt.values {
				req.Header.Add("Sec-Fetch-Site", v)
			}
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			t.Logf("metadataCase=%s status=%d handlerCalls=%d", tt.name, rec.Code, calls)
			if rec.Code != tt.status {
				t.Errorf("status=%d want=%d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			if tt.status != 200 && calls != 0 {
				t.Errorf("denied GET reached side-effect handler: %d", calls)
			}
		})
	}
}
