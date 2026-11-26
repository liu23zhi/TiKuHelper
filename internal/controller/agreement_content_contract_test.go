package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestAgreementClientPinnedBodyDigest(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()
	if err := registry.EnsureAgreementSchema(db); err != nil {
		t.Fatal(err)
	}
	previous := agreementDB
	agreementDB = func() *gorm.DB { return db }
	defer func() { agreementDB = previous }()
	st := service.AgreementStore{DB: db}
	_, proof, err := st.Enroll()
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.GET("/api/agreements/current", CurrentAgreement)
	r.GET("/api/agreements/status", AgreementDeviceStatus)
	r.POST("/api/agreements/consents", SubmitAgreement)
	type response struct {
		Version         string `json:"version"`
		Body            string `json:"body"`
		ContentHash     string `json:"content_hash"`
		Code            string `json:"code"`
		NeedsAcceptance *bool  `json:"needsAcceptance"`
	}
	serve := func(t *testing.T, req *http.Request, credential string, status int) response {
		t.Helper()
		method, path := req.Method, req.URL.RequestURI()
		req.RemoteAddr = "127.0.0.3:14004"
		if credential != "" {
			req.Header.Set(agreementDeviceHeader, credential)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != status {
			t.Errorf("%s %s: status=%d, want %d", method, path, w.Code, status)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s unexpectedly cacheable", method, path)
		}
		if status >= 400 {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"body", "summary", "content_hash"} {
				if _, present := fields[key]; present {
					t.Errorf("%s %s: error response contains %q", method, path, key)
				}
			}
		}
		var got response
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	call := func(t *testing.T, method, path, credential, requestBody string, status int) response {
		t.Helper()
		req := httptest.NewRequest(method, "https://fixture.invalid/api/agreements/"+path, strings.NewReader(requestBody))
		return serve(t, req, credential, status)
	}
	publish := func(version, body string) {
		t.Helper()
		d, err := st.SaveDraft(0, 0, entity.AgreementContent{Version: version, Summary: "fixture summary", Body: body})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = st.Publish(d.ID, d.Revision); err != nil {
			t.Fatal(err)
		}
	}

	if unavailable := call(t, "GET", "status", proof, "", 404); unavailable.ContentHash != "" {
		t.Error("unpublished agreement unexpectedly has a digest")
	}
	const originalBody = "协议内容\n安全确认"
	const originalDigest = "bdf2cd9b4fb3eb55e863914ec15cc46154c6fbb210f08aacfc21a68a431d5426"
	publish("v1", originalBody)
	light := call(t, "GET", "status", proof, "", 200)
	if light.Version != "v1" || light.ContentHash != originalDigest || light.Body != "" || light.NeedsAcceptance == nil || !*light.NeedsAcceptance {
		t.Errorf("status does not bind digest and required acceptance to v1: %+v", light)
	}
	pinned := call(t, "GET", "current?version=v1", proof, "", 200)
	if pinned.Version != "v1" || pinned.Body != originalBody || pinned.ContentHash != originalDigest || pinned.NeedsAcceptance == nil || !*pinned.NeedsAcceptance {
		t.Errorf("pinned body does not match the authenticated v1 status: %+v", pinned)
	}
	legacy := call(t, "GET", "current", "", "", 200)
	if legacy.Version != "v1" || legacy.Body != originalBody || legacy.ContentHash != originalDigest || legacy.NeedsAcceptance != nil {
		t.Error("anonymous unpinned fetch no longer returns the public agreement without inventing consent")
	}
	t.Run("legacy_with_unrelated_query", func(t *testing.T) {
		got := call(t, "GET", "current?label=a%20b", "", "", 200)
		if got != legacy {
			t.Errorf("unrelated query changes anonymous fetch: %+v", got)
		}
	})
	t.Run("proof_and_tls_take_precedence", func(t *testing.T) {
		const path = "current?version=%zz"
		if got := call(t, "GET", path, "invalid-logical-device-proof", "", 401); got.Code != "proof_required" {
			t.Errorf("bad proof returned %q", got.Code)
		}
		req := httptest.NewRequest("GET", "http://fixture.invalid/api/agreements/"+path, nil)
		if got := serve(t, req, proof, 403); got.Code != "https_required" {
			t.Errorf("authenticated request without TLS returned %q", got.Code)
		}
	})
	if got := call(t, "GET", "status", "", "", 401); got.Code != "proof_required" {
		t.Errorf("status without credentials returned %q", got.Code)
	}
	call(t, "POST", "consents", proof, `{"version":"v1"}`, 200)

	const newBody = "协议内容\n新版"
	const newDigest = "954b7d9949b9073d8b9b33d5f66242684eeb050c281b88af99d34b63f3662ba9"
	publish("v2", newBody)
	stale := call(t, "GET", "current?version=v1", proof, "", 409)
	if stale.Code != "version_conflict" || stale.Body != "" || stale.ContentHash != "" {
		t.Error("a mismatched pin does not fail closed")
	}
	for _, tc := range []struct {
		name, query, credential string
	}{
		{"malformed_version", "version=%zz", ""},
		{"malformed_duplicate", "version=v2&version=%zz", proof},
		{"malformed_unrelated_parameter", "version=v2&label=%zz", proof},
		{"empty_version", "version=", proof},
		{"different_values", "version=v2&version=v1", proof},
		{"repeated_value", "version=v2&version=v2", proof},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(t, "GET", "current?"+tc.query, tc.credential, "", 400); got.Code != "invalid_request" {
				t.Errorf("invalid query returned %q", got.Code)
			}
		})
	}
	now := call(t, "GET", "status", proof, "", 200)
	next := call(t, "GET", "current?version=v2", proof, "", 200)
	if now.Version != "v2" || now.ContentHash != newDigest || now.NeedsAcceptance == nil || !*now.NeedsAcceptance ||
		next.Version != now.Version || next.ContentHash != now.ContentHash || next.Body != newBody {
		t.Error("v1 consent is not a v2 receipt and v2 body must match the new digest")
	}
}
