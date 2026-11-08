package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"gorm.io/gorm"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
)

type s03RT func(*http.Request) (*http.Response, error)

func (f s03RT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type s03Run struct {
	ledger         map[string]any
	globalsEqual   func() map[string]bool
	restoreGlobals func()
}

func s03NewRun() *s03Run {
	return &s03Run{ledger: map[string]any{"schema": "S03_REAL_PRODUCTION_ROUTE_AUTH_V2", "fixtureOnly": true, "realDialAllowed": false, "observations": []any{}, "calls": []any{}, "authCases": []any{}, "resources": map[string]any{}}}
}
func (run *s03Run) append(k string, v any) { run.ledger[k] = append(run.ledger[k].([]any), v) }
func s03AllTrue(values map[string]bool) bool {
	for _, v := range values {
		if !v {
			return false
		}
	}
	return true
}
func s03Begin(t *testing.T) *s03Run {
	t.Helper()
	run := s03NewRun()
	originalQ := dao.Q
	var originalQuery dao.Query
	if originalQ != nil {
		originalQuery = *originalQ
	}
	originalLog, originalTiku, originalUser := dao.Log, dao.Tiku, dao.User
	originalMode, originalTransport := gin.Mode(), http.DefaultTransport
	token := util.GenerateJwtForUser(990, "S03_PREEXISTING_KEY_SENTINEL")
	if _, err := util.ParseJwtWithClaims(token); err != nil {
		t.Fatal(err)
	}
	run.globalsEqual = func() map[string]bool {
		valueEqual := dao.Q == nil && originalQ == nil
		if dao.Q != nil && originalQ != nil {
			valueEqual = reflect.DeepEqual(*dao.Q, originalQuery)
		}
		_, tokenErr := util.ParseJwtWithClaims(token)
		return map[string]bool{"queryPointer": dao.Q == originalQ, "queryValue": valueEqual, "logAlias": dao.Log == originalLog, "tikuAlias": dao.Tiku == originalTiku, "userAlias": dao.User == originalUser, "ginMode": gin.Mode() == originalMode, "defaultTransport": reflect.ValueOf(http.DefaultTransport) == reflect.ValueOf(originalTransport), "preexistingJWTStillValid": tokenErr == nil}
	}
	run.restoreGlobals = func() {
		dao.Q = originalQ
		if originalQ != nil {
			*originalQ = originalQuery
		}
		dao.Log, dao.Tiku, dao.User = originalLog, originalTiku, originalUser
		gin.SetMode(originalMode)
		http.DefaultTransport = originalTransport
	}
	t.Cleanup(func() {
		run.restoreGlobals()
		equal := run.globalsEqual()
		run.ledger["globalRestoration"] = equal
		run.ledger["resources"].(map[string]any)["transportRestored"] = equal["defaultTransport"]
		if !s03AllTrue(equal) {
			t.Errorf("S03_GLOBAL_RESTORATION_FAILED: %v", equal)
		}
		run.save()
	})
	return run
}
func (run *s03Run) openMemory(t *testing.T, migrate func(*gorm.DB) error) (*gorm.DB, error) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:s03-production-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// Cleanup is registered before migration and before DAO/global mutation.
	run.ledger["resources"].(map[string]any)["sqlCleanupRegisteredBeforeMigration"] = true
	t.Cleanup(func() {
		closeErr := sqlDB.Close()
		pingErr := sqlDB.Ping()
		run.ledger["resources"].(map[string]any)["memorySQLiteClosed"] = closeErr == nil
		run.ledger["resources"].(map[string]any)["closedSQLPingRejected"] = pingErr != nil
		if closeErr != nil || pingErr == nil {
			t.Errorf("S03_SQL_CLOSE_FAILED close=%v ping=%v", closeErr, pingErr)
		}
	})
	if migrate != nil {
		if err = migrate(db); err != nil {
			return nil, err
		}
	}
	if dao.Q == nil {
		dao.Q = new(dao.Query)
	}
	dao.SetDefault(db)
	return db, nil
}
func s03Serve(r *gin.Engine, w *httptest.ResponseRecorder, req *http.Request) {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	r.ServeHTTP(w, req.WithContext(ctx))
}
func s03SHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func (run *s03Run) save() {
	p := os.Getenv("S03_LEDGER")
	if p != "" {
		b, _ := json.MarshalIndent(run.ledger, "", "  ")
		_ = os.WriteFile(p, append(b, '\n'), 0600)
	}
}
func (run *s03Run) transport(t *testing.T, counter *int) s03RT {
	s03Ledger, s03Append := run.ledger, run.append
	return func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Host != "mooc2-ans.chaoxing.com" || r.Host != "mooc2-ans.chaoxing.com" {
			t.Errorf("unexpected fixed target; no Dial possible")
			return nil, fmt.Errorf("S03_FORBIDDEN_TARGET")
		}
		var b []byte
		var err error
		if r.Body != nil {
			b, err = io.ReadAll(r.Body)
		}
		if err != nil {
			return nil, err
		}
		*counter++
		response := []byte(`{"paper":[{"content":"SYNTHETIC 中文 + &","answer":["A"]}],"extra":"KEEP_RAW"}`)
		if r.URL.Path == "/sqp/api/upload" {
			response = []byte(`{"crc":"S03_SYNTHETIC_CRC","extra":"KEEP_RAW"}`)
		}
		s03Append("calls", map[string]any{"id": len(s03Ledger["calls"].([]any)) + 1, "method": r.Method, "scheme": r.URL.Scheme, "host": r.URL.Host, "requestHost": r.Host, "path": r.URL.Path, "rawPath": r.URL.RawPath, "rawQuery": r.URL.RawQuery, "contentType": r.Header.Get("Content-Type"), "body": string(b), "bodySHA256": s03SHA(b), "authorizationPresent": r.Header.Get("Authorization") != "", "proxyAuthorizationPresent": r.Header.Get("Proxy-Authorization") != "", "authorizationSHA256": s03SHA([]byte(r.Header.Get("Authorization"))), "cookie": r.Header.Get("Cookie"), "origin": r.Header.Get("Origin"), "forwardedHost": r.Header.Get("X-Forwarded-Host"), "responseBody": string(response)})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "X-S03-Synthetic": []string{"true"}, "Set-Cookie": []string{"S03_VENDOR_FIXTURE=synthetic; Path=/"}}, Body: io.NopCloser(bytes.NewReader(response)), Request: r}, nil
	}
}

func TestS03ProductionRouteAuth(t *testing.T) {
	run := s03Begin(t)
	s03Ledger, s03Append := run.ledger, run.append
	gin.SetMode(gin.TestMode)
	db, err := run.openMemory(t, func(db *gorm.DB) error {
		return db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Log{}, &entity.BillingRecord{})
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	http.DefaultTransport = run.transport(t, &count)
	password, err := util.HashPassword("S03Fixture!Pass9")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []entity.User{{ID: 901, Username: "s03-admin", Password: password, Role: "admin", Status: "enabled", RegisterSource: "public"}, {ID: 902, Username: "s03-user", Password: password, Role: "user", Status: "enabled", RegisterSource: "public"}, {ID: 903, Username: "s03-disabled", Password: password, Role: "admin", Status: "disabled", RegisterSource: "public"}, {ID: 904, Username: "s03-revoked", Password: password, Role: "admin", Status: "enabled", RegisterSource: "public"}} {
		if err = db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	admin, user, disabled, revoked := util.GenerateJwtForUser(901, password), util.GenerateJwtForUser(902, password), util.GenerateJwtForUser(903, password), util.GenerateJwtForUser(904, password)
	newpass, err := util.HashPassword("S03Changed!Pass8")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&entity.User{}).Where("id = ?", 904).Update("password", newpass).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	SetAPIRouter(r)
	cases := []struct {
		name, header, cookie string
		want                 int
	}{{"anonymous", "", "", 401}, {"invalid-header", "S03_INVALID_SYNTHETIC", "", 401}, {"user-raw-header", user, "", 403}, {"user-cookie", "", user, 403}, {"admin-raw-header", admin, "", 200}, {"admin-cookie", "", admin, 200}, {"bearer-prefix-not-current-contract", "Bearer " + admin, "", 401}, {"invalid-header-beats-valid-cookie", "S03_INVALID_SYNTHETIC", admin, 401}, {"disabled-admin", disabled, "", 401}, {"credential-version-revoked", revoked, "", 401}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/sqp/api/parse?file=protected-synthetic", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			if c.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "tiku_token", Value: c.cookie})
			}
			req.Header.Set("Proxy-Authorization", "S03_PROXY_SYNTHETIC")
			beforeHeaders := req.Header.Clone()
			w := httptest.NewRecorder()
			before := count
			s03Serve(r, w, req)
			delta := count - before
			wantCalls := 0
			if c.want == 200 {
				wantCalls = 1
			}
			pass := w.Code == c.want && delta == wantCalls
			unchanged := reflect.DeepEqual(beforeHeaders, req.Header)
			strip := true
			if c.want == 200 && delta == 1 {
				last := s03Ledger["calls"].([]any)[len(s03Ledger["calls"].([]any))-1].(map[string]any)
				strip = last["authorizationPresent"] == false && last["proxyAuthorizationPresent"] == false && last["cookie"] == "UID=1"
			}
			s03Append("authCases", map[string]any{"case": c.name, "route": "real SetAPIRouter", "expectedStatus": c.want, "actualStatus": w.Code, "dispatchDelta": delta, "statusDispatchPass": pass, "originalAllHeadersUnchanged": unchanged, "credentialStripPass": strip, "responseBody": w.Body.String()})
			if !pass {
				t.Errorf("AUTH_BOUNDARY_RED: want%d/%d actual%d/%d", c.want, wantCalls, w.Code, delta)
			}
			if !unchanged {
				t.Error("INCOMING_HEADERS_MUTATED")
			}
			if !strip {
				t.Error("OUTGOING_CREDENTIAL_RED")
			}
		})
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.SetBoundary("S03_SYNTHETIC_BOUNDARY")
	part, _ := mw.CreateFormFile("file", "synthetic.docx")
	_, _ = part.Write([]byte("PK\x03\x04SYNTHETIC_ONLY_NOT_A_REAL_DOCUMENT"))
	_ = mw.Close()
	wires := []struct {
		name, method, target, ctype string
		body                        []byte
	}{{"upload", http.MethodPost, "/sqp/api/upload", mw.FormDataContentType(), buf.Bytes()}, {"parse-get", http.MethodGet, "/sqp/api/parse?file=fixture%2B%E4%B8%AD%2Fvalue%25", "", nil}, {"parse-post", http.MethodPost, "/sqp/api/parse", "application/x-www-form-urlencoded", []byte(url.Values{"html": []string{"<p>中文 + & SYNTHETIC</p>"}}.Encode())}}
	for _, c := range wires {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.target, bytes.NewReader(c.body))
			req.Header.Set("Authorization", admin)
			req.Header.Set("Proxy-Authorization", "S03_PROXY_SYNTHETIC")
			req.Header.Set("Cookie", "tiku_token=S03_SYNTHETIC_COOKIE; custom=SYNTHETIC")
			req.Header.Set("Content-Type", c.ctype)
			beforeHeaders := req.Header.Clone()
			w := httptest.NewRecorder()
			before := count
			s03Serve(r, w, req)
			if count != before+1 || w.Code != 200 {
				t.Fatalf("positive wire dispatch/status mismatch")
			}
			last := s03Ledger["calls"].([]any)[len(s03Ledger["calls"].([]any))-1].(map[string]any)
			wire := last["path"] == req.URL.Path && last["rawQuery"] == req.URL.RawQuery && last["bodySHA256"] == s03SHA(c.body) && last["contentType"] == c.ctype && w.Body.String() == last["responseBody"]
			unchanged := reflect.DeepEqual(beforeHeaders, req.Header)
			strip := last["authorizationPresent"] == false && last["proxyAuthorizationPresent"] == false && last["cookie"] == "UID=1"
			s03Append("observations", map[string]any{"case": c.name, "wirePass": wire, "incomingHeadersUnchanged": unchanged, "outgoingCredentialStrip": strip})
			if !wire {
				t.Error("WIRE_DRIFT")
			}
			if !unchanged {
				t.Error("INCOMING_HEADERS_MUTATED")
			}
			if !strip {
				t.Error("OUTGOING_CREDENTIAL_RED")
			}
		})
	}
}
