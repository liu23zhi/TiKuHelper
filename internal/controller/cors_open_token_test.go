package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

type openTokenPermanentNoDial struct{ calls *int }

func (n openTokenPermanentNoDial) RoundTrip(*http.Request) (*http.Response, error) {
	*n.calls++
	return nil, fmt.Errorf("S01_OPEN_TOKEN_FORBIDDEN_NETWORK")
}
func TestOpenTokenCORSHeadersAndAuth(t *testing.T) {
	ledger := map[string]any{"schema": "S01_OPEN_TOKEN_HEADER_AUTH_V1", "observations": []any{}, "cookieObservations": []any{}, "realDB": false, "realCredentials": false, "nativeBrowser": false, "CSRFProof": false, "actualAuthFunctions": true, "protectedHandler": "synthetic echo, not production API business handler"}
	t.Cleanup(func() {
		ledger["testFailed"] = t.Failed()
		if p := os.Getenv("S01_MATRIX"); p != "" {
			b, e := json.MarshalIndent(ledger, "", "  ")
			if e != nil {
				t.Error(e)
				return
			}
			if e = os.WriteFile(p, append(b, '\n'), 0600); e != nil {
				t.Error(e)
			}
		}
	})
	priorMode := gin.Mode()
	priorTransport := http.DefaultTransport
	priorQ := dao.Q
	var priorQuery dao.Query
	if priorQ != nil {
		priorQuery = *priorQ
	}
	priorLog, priorTiku, priorUser := dao.Log, dao.Tiku, dao.User
	dialCalls := 0
	t.Cleanup(func() {
		dao.Q = priorQ
		if priorQ != nil {
			*priorQ = priorQuery
		}
		dao.Log, dao.Tiku, dao.User = priorLog, priorTiku, priorUser
		gin.SetMode(priorMode)
		http.DefaultTransport = priorTransport
		checks := map[string]bool{"QPointer": dao.Q == priorQ, "QValue": dao.Q == nil || reflect.DeepEqual(*dao.Q, priorQuery), "aliases": dao.Log == priorLog && dao.Tiku == priorTiku && dao.User == priorUser, "ginMode": gin.Mode() == priorMode, "transport": reflect.ValueOf(http.DefaultTransport) == reflect.ValueOf(priorTransport), "noDial": dialCalls == 0}
		ledger["globalRestoration"] = checks
		for n, v := range checks {
			if !v {
				t.Errorf("restore %s", n)
			}
		}
	})
	gin.SetMode(gin.TestMode)
	http.DefaultTransport = openTokenPermanentNoDial{&dialCalls}
	db, e := gorm.Open(sqlite.Open("file:s01-open-token-contract?mode=memory&cache=shared"), &gorm.Config{Logger: glog.Default.LogMode(glog.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	sqlDB, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		e := sqlDB.Close()
		ping := sqlDB.Ping()
		ledger["databaseCleanup"] = map[string]any{"closeError": fmt.Sprint(e), "closedPingRejected": ping != nil}
		if e != nil || ping == nil {
			t.Error("synthetic SQL close")
		}
	})
	if e = db.AutoMigrate(&entity.User{}, &entity.AccessToken{}); e != nil {
		t.Fatal(e)
	}
	password := "S01_OPEN_TOKEN_SYNTHETIC_STORED_HASH"
	user := entity.User{ID: 901, Username: "open-token-synthetic", Password: password, Role: "user", Status: "enabled", RegisterSource: "public"}
	if e = db.Create(&user).Error; e != nil {
		t.Fatal(e)
	}
	value := "S01_OPEN_TOKEN_SYNTHETIC_ACCESS"
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(value))))
	token := entity.AccessToken{ID: 801, UserID: 901, Name: "open-token-fixture", TokenHash: hex.EncodeToString(sum[:]), TokenPrefix: "S01_OPEN_SYNTHETIC", Status: "enabled"}
	if e = db.Create(&token).Error; e != nil {
		t.Fatal(e)
	}
	if dao.Q == nil {
		dao.Q = new(dao.Query)
	}
	dao.SetDefault(db)
	login := util.GenerateJwtForUser(901, password)
	if login == "" {
		t.Fatal("synthetic JWT")
	}
	authCalls, handlerCalls := 0, 0
	engine := func(seed bool) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Header("Vary", "Accept-Encoding")
			if seed {
				c.Header("Access-Control-Allow-Credentials", "true")
			}
			c.Next()
		}, middleware.CORS)
		r.Any("/fixture/auth", func(c *gin.Context) { authCalls++; Auth(c) }, func(c *gin.Context) { handlerCalls++; c.String(200, "OPEN_TOKEN_SYNTHETIC_OK") })
		r.Any("/fixture/access", func(c *gin.Context) { authCalls++; AuthOrAccessToken(c) }, func(c *gin.Context) { handlerCalls++; c.String(200, "OPEN_TOKEN_SYNTHETIC_OK") })
		return r
	}
	observe := func(label, origin, method, path, credential, authorization string, cookie, seed bool, want int) {
		beforeA, beforeH := authCalls, handlerCalls
		q := httptest.NewRequest(method, "https://s01-open.invalid"+path, nil)
		if origin != "" {
			q.Header.Set("Origin", origin)
		}
		if authorization != "" {
			q.Header.Set("Authorization", authorization)
		}
		if cookie {
			q.AddCookie(&http.Cookie{Name: authCookieName, Value: login})
		}
		if method == "OPTIONS" {
			q.Header.Set("Access-Control-Request-Method", "POST")
			q.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
		}
		w := httptest.NewRecorder()
		engine(seed).ServeHTTP(w, q)
		expected := ""
		if strings.TrimSpace(origin) != "" {
			expected = "*"
		}
		checks := map[string]bool{"status": w.Code == want, "wildcardInsteadOfReflection": w.Header().Get("Access-Control-Allow-Origin") == expected, "noCredentialSharingHeader": w.Header().Get("Access-Control-Allow-Credentials") == "", "existingVaryPreservedNoOriginAdded": w.Header().Get("Vary") == "Accept-Encoding", "sameAllowHeaders": w.Header().Get("Access-Control-Allow-Headers") == "Authorization, Content-Type, Idempotency-Key", "sameFiveMethods": w.Header().Get("Access-Control-Allow-Methods") == "GET, POST, PUT, DELETE, OPTIONS"}
		if method == "OPTIONS" {
			checks["preflightAuthNotCalled"] = authCalls == beforeA
			checks["preflightHandlerNotCalled"] = handlerCalls == beforeH
			checks["preflightBodyEmpty"] = w.Body.Len() == 0
		} else {
			checks["actualAuthCalled"] = authCalls == beforeA+1
			expectedCalls := 0
			if want == 200 {
				expectedCalls = 1
			}
			checks["handlerGatedByUnchangedAuth"] = handlerCalls-beforeH == expectedCalls
		}
		headers := map[string]string{}
		for _, n := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Vary"} {
			headers[n] = w.Header().Get(n)
		}
		ledger["observations"] = append(ledger["observations"].([]any), map[string]any{"case": label, "origin": origin, "method": method, "path": path, "credentialControl": credential, "cookieManuallyInjected": cookie, "preseedCredentials": seed, "status": w.Code, "responseHeaders": headers, "body": w.Body.String(), "authCallsDelta": authCalls - beforeA, "handlerCallsDelta": handlerCalls - beforeH, "checks": checks})
		for n, v := range checks {
			if !v {
				t.Errorf("%s %s", label, n)
			}
		}
	}
	origins := []struct{ name, value string }{{"same-origin", "https://s01-open.invalid"}, {"empty-origin", ""}, {"foreign-origin", "https://s01-foreign.invalid"}, {"null-origin", "null"}, {"extension-origin", "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {"malformed-origin", "not-an-origin"}, {"star-origin", "*"}}
	controls := []struct {
		name, path, authorization string
		cookie                    bool
		want                      int
	}{{"no-auth", "/fixture/access", "", false, 401}, {"bearer-access-token", "/fixture/access", "Bearer " + value, false, 200}, {"cookie-login-jwt", "/fixture/auth", "", true, 200}, {"raw-login-jwt", "/fixture/auth", login, false, 200}, {"invalid-bearer-with-valid-cookie", "/fixture/access", "Bearer S01_INVALID", true, 401}, {"bearer-login-jwt-not-supported", "/fixture/auth", "Bearer " + login, false, 401}}
	for _, o := range origins {
		for _, c := range controls {
			want := c.want
			if c.name == "cookie-login-jwt" && o.value != "" && o.name != "same-origin" {
				want = http.StatusForbidden
			}
			observe(o.name+"/"+c.name, o.value, "GET", c.path, c.name, c.authorization, c.cookie, false, want)
		}
		observe(o.name+"/preflight", o.value, "OPTIONS", "/fixture/access", "none", "", false, false, 204)
	}
	observe("foreign/preseed-credentials-cleared", "https://s01-foreign.invalid", "GET", "/fixture/access", "bearer-access-token", "Bearer "+value, false, true, 200)
	observe("empty/preseed-credentials-cleared", "", "GET", "/fixture/auth", "cookie-login-jwt", "", true, true, 200)
	for _, mode := range []struct {
		name, target string
		forward      bool
		secure       bool
	}{{"plain-http", "http://s01-open.invalid/", false, false}, {"direct-https", "https://s01-open.invalid/", false, true}, {"forwarded-https-cookie-existing-behavior", "http://s01-open.invalid/", true, true}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", mode.target, nil)
		if mode.forward {
			c.Request.Header.Set("X-Forwarded-Proto", "https")
		}
		setAuthCookie(c, "OPEN_TOKEN_SYNTHETIC_COOKIE")
		cs := w.Result().Cookies()
		if len(cs) != 1 {
			t.Error("cookie count")
			continue
		}
		cookie := cs[0]
		checks := map[string]bool{"sameSiteLax": cookie.SameSite == http.SameSiteLaxMode, "httpOnly": cookie.HttpOnly, "secureUnchanged": cookie.Secure == mode.secure, "hostOnly": cookie.Domain == "", "pathRoot": cookie.Path == "/", "ttl24h": cookie.MaxAge == 86400}
		ledger["cookieObservations"] = append(ledger["cookieObservations"].([]any), map[string]any{"mode": mode.name, "literalSetCookie": w.Header().Get("Set-Cookie"), "checks": checks})
		for n, v := range checks {
			if !v {
				t.Errorf("cookie %s %s", mode.name, n)
			}
		}
	}
	ledger["requestCases"] = len(ledger["observations"].([]any))
	ledger["forbiddenDialCalls"] = dialCalls
	t.Logf("OPEN_TOKEN_HEADER_AUTH requests=%d cookies=3 noBrowserCSRFProof=true", ledger["requestCases"])
}
