package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestSQPDoesNotForwardAgreementCredential(t *testing.T) {
	seen := 0
	p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{}), sqpTestTransport(func(q *http.Request) (*http.Response, error) {
		seen++
		if q.Header.Get("X-Agreement-Device") != "" {
			t.Error("local agreement credential escaped to SQP transport")
		}
		return sqpTestResponse(q), nil
	}), nil)
	q := httptest.NewRequest("GET", "/sqp/api/parse", nil)
	q.Header.Set("X-Agreement-Device", "synthetic-local-fixture")
	w := sqpTestServe(sqpTestRouter(p), q)
	if w.Code != 200 || seen != 1 {
		t.Fatalf("dispatch %d/%d", w.Code, seen)
	}
}

func TestAgreementStrictSourceDespiteGlobalCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name            string
		explicit        []string
		peer, xff, want string
	}{
		{"empty-none", nil, "192.0.2.8", "198.51.100.88", "192.0.2.8"},
		{"explicit", []string{"192.0.2.8"}, "192.0.2.8", "198.51.100.88", "198.51.100.88"},
		{"implicit-lan-rejected", []string{"192.0.2.8"}, "10.1.2.3", "198.51.100.88", "10.1.2.3"},
		{"multi-valued-not-a-single-attestation", []string{"192.0.2.8"}, "192.0.2.8", "198.51.100.88, 198.51.100.77", "192.0.2.8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			if e := r.SetTrustedProxies([]string{"0.0.0.0/0", "::/0"}); e != nil {
				t.Fatal(e)
			}
			r.Use(ScriptProxyHTTPS(tc.explicit), AgreementSourcePolicy(tc.explicit, false))
			r.GET("/api/agreements/status", func(c *gin.Context) { c.String(200, agreementProof(c).IP) })
			q := httptest.NewRequest("GET", "/api/agreements/status", nil)
			q.RemoteAddr = tc.peer + ":8081"
			q.Header.Set("X-Forwarded-For", tc.xff)
			q.Header.Set("X-Real-IP", "203.0.113.19")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			if w.Body.String() != tc.want {
				t.Fatalf("selected %s, want %s", w.Body.String(), tc.want)
			}
		})
	}
}

func TestAgreementPlatformOriginWithProductionHostBoundary(t *testing.T) {
	db := newSearchControllerTestDB(t)
	sql, _ := db.DB()
	t.Cleanup(func() { _ = sql.Close() })
	if e := registry.EnsureAgreementSchema(db); e != nil {
		t.Fatal(e)
	}
	if e := service.ConfigureSiteHosts([]string{"platform.example.test"}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = service.ConfigureSiteHosts(nil) })
	if e := db.Create(&entity.ScriptAddressReservation{AddressKey: fmt.Sprintf("%x", sha256.Sum256([]byte("dedicated.fixture.test\x00"))), Kind: "short", ScriptID: 12}).Error; e != nil {
		t.Fatal(e)
	}
	role, err := service.ScriptWebHostRoleWithDB(db, "dedicated.fixture.test")
	if err != nil || role != service.EntryOnlyWebHost {
		t.Fatalf("fixture not assigned: %v %v", role, err)
	}
	s := service.AgreementStore{DB: db}
	d, e := s.SaveDraft(0, 0, entity.AgreementContent{Version: "fixture", Summary: "fixture", Body: "first-party text"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Publish(d.ID, 1); e != nil {
		t.Fatal(e)
	}
	old := agreementDB
	agreementDB = func() *gorm.DB { return db }
	defer func() { agreementDB = old }()
	r := gin.New()
	r.Use(ScriptProxyHTTPS(nil), ScriptHostBoundary(db), middleware.CORS)
	r.GET("/api/agreements/current", CurrentAgreement)
	for _, test := range []struct {
		host     string
		get, opt int
	}{
		{"platform.example.test", 200, 204}, {"PLATFORM.Example.Test.:443", 200, 204},
		{"dedicated.fixture.test", 404, 404}, {"unlisted.fixture.test", 404, 404},
		{"localhost", 404, 404}, {"alias.localhost", 404, 404}, {"127.0.0.1", 404, 404},
		{"[::1]", 404, 404}, {"0.0.0.0", 404, 404}, {"[::]", 404, 404},
	} {
		for _, method := range []string{"GET", "OPTIONS"} {
			q := httptest.NewRequest(method, "https://"+test.host+"/api/agreements/current", nil)
			q.Header.Set("Origin", "https://platform.example.test")
			q.Header.Set("X-Forwarded-Host", "platform.example.test")
			q.Header.Set("Access-Control-Request-Method", "GET")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			want := test.get
			if method == "OPTIONS" {
				want = test.opt
			}
			if w.Code != want {
				t.Errorf("%s %s: %d want %d", test.host, method, w.Code, want)
			}
			if (want == 200) != strings.Contains(w.Body.String(), "first-party text") {
				t.Error("published body exposure mismatch")
			}
			if method == "OPTIONS" && (want == 204) != strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Agreement-Device") {
				t.Error("credential preflight exposure mismatch")
			}
		}
	}
	if e := service.ConfigureSiteHosts(nil); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{"GET", "OPTIONS"} {
		req := httptest.NewRequest(method, "https://platform.example.test/api/agreements/current", nil)
		req.Header.Set("Origin", "https://platform.example.test")
		req.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 404 || w.Header().Get("Access-Control-Allow-Headers") != "" {
			t.Fatalf("empty siteHosts %s got %d", method, w.Code)
		}
	}

}

func TestAgreementRateReceiptAuditUseOneVerifiedIP(t *testing.T) {
	for _, a := range []struct {
		name, peer, verified string
		trusted              bool
	}{{"no-policy", "192.0.2.8", "192.0.2.8", false}, {"trusted-edge", "192.0.2.33", "203.0.113.9", true}} {
		t.Run(a.name, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			sql, _ := db.DB()
			t.Cleanup(func() { _ = sql.Close() })
			if e := registry.EnsureAgreementSchema(db); e != nil {
				t.Fatal(e)
			}
			oldDB, oldBudget := agreementDB, enrollmentBudget
			agreementDB = func() *gorm.DB { return db }
			enrollmentBudget = newAgreementEnrollmentBudget()
			defer func() { agreementDB = oldDB; enrollmentBudget = oldBudget }()
			r := gin.New()
			_ = r.SetTrustedProxies([]string{"0.0.0.0/0", "::/0"})
			var explicit []string
			if a.trusted {
				explicit = []string{a.peer}
			}
			r.Use(AgreementSourcePolicy(explicit, false), func(c *gin.Context) { c.Set("user", &entity.User{ID: 1, Role: "admin"}); c.Next() })
			r.POST("/api/admin/agreements/drafts", CreateAgreementDraft)
			r.POST("/api/admin/agreements/drafts/:id/publish", PublishAgreementDraft)
			r.POST("/api/agreements/devices", EnrollAgreementDevice)
			r.POST("/api/agreements/consents", SubmitAgreement)
			callNo := 0
			secret := ""
			post := func(path, payload string, status int) *httptest.ResponseRecorder {
				t.Helper()
				callNo++
				q := httptest.NewRequest("POST", "https://platform.test"+path, strings.NewReader(payload))
				q.RemoteAddr = a.peer + ":2222"
				xff := fmt.Sprintf("198.51.100.%d", callNo)
				if a.trusted {
					xff = a.verified
				}
				q.Header.Set("X-Forwarded-For", xff)
				q.Header.Set("X-Real-IP", "198.51.100.99")
				q.Header.Set(agreementDeviceHeader, secret)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, q)
				if w.Code != status {
					t.Fatalf("%s: %d want %d", path, w.Code, status)
				}
				return w
			}
			post("/api/admin/agreements/drafts", `{"version":"t1","summary":"fixture","body":"fixture"}`, 200)
			post("/api/admin/agreements/drafts/1/publish", `{"revision":1}`, 200)
			enrolled := post("/api/agreements/devices", "", 201)
			var fields struct{ DeviceCredential string }
			if e := json.Unmarshal(enrolled.Body.Bytes(), &fields); e != nil || fields.DeviceCredential == "" {
				t.Fatal("missing device grant")
			}
			post("/api/agreements/devices", "", 201)
			post("/api/agreements/devices", "", 201)
			post("/api/agreements/devices", "", 429)
			secret = fields.DeviceCredential
			post("/api/agreements/consents", `{"version":"t1"}`, 200)
			var row entity.AgreementConsent
			if e := db.First(&row).Error; e != nil || row.IP != a.verified {
				t.Fatalf("wrong receipt IP %q err=%v", row.IP, e)
			}
			var logs []entity.Log
			if e := db.Where("module=?", "agreement").Find(&logs).Error; e != nil || len(logs) != 2 {
				t.Fatal("missing administrator audit")
			}
			for _, l := range logs {
				if l.ClientIP != a.verified {
					t.Fatalf("wrong audit IP %q", l.ClientIP)
				}
			}
		})
	}
}
func TestAgreementGatewayMissingOrUntrustedProvenanceFailsClosed(t *testing.T) {
	for _, v := range []struct{ name, peer, mark, xff, want string }{
		{"gateway", "127.0.0.1", agreementGatewayMark, "192.0.2.3", "192.0.2.3"},
		{"old-generation", "127.0.0.1", "", "192.0.2.3", ""},
		{"other-peer", "192.0.2.99", agreementGatewayMark, "192.0.2.3", ""},
		{"ambiguous", "127.0.0.1", agreementGatewayMark, "192.0.2.3, 192.0.2.4", ""},
	} {
		t.Run(v.name, func(t *testing.T) {
			r := gin.New()
			r.Use(AgreementSourcePolicy([]string{"127.0.0.1/32"}, true))
			r.GET("/api/agreements/status", func(c *gin.Context) { c.String(200, agreementProof(c).IP) })
			r.POST("/api/agreements/devices", EnrollAgreementDevice)
			q := httptest.NewRequest("GET", "/api/agreements/status", nil)
			q.RemoteAddr = v.peer + ":9999"
			q.Header.Set("Forwarded", v.mark)
			q.Header.Set("X-Forwarded-For", v.xff)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			if w.Body.String() != v.want {
				t.Fatalf("source=%s want %s", w.Body.String(), v.want)
			}
			if v.want == "" {
				q := httptest.NewRequest("POST", "https://fixture.invalid/api/agreements/devices", nil)
				q.RemoteAddr = v.peer + ":9999"
				q.Header.Set("Forwarded", v.mark)
				q.Header.Set("X-Forwarded-For", v.xff)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, q)
				if w.Code != 503 || !strings.Contains(w.Body.String(), "source_unavailable") {
					t.Fatalf("unsafe enrollment code %d", w.Code)
				}
			}
		})
	}
}
