package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAgreementRoutes(t *testing.T) {
	r := gin.New()
	SetAPIRouter(r)
	got := map[string]bool{}
	for _, v := range r.Routes() {
		got[v.Method+" "+v.Path] = true
	}
	for _, path := range []string{"POST /api/agreements/devices", "GET /api/agreements/current", "GET /api/agreements/status", "POST /api/agreements/consents", "POST /api/admin/agreements/drafts", "GET /api/admin/agreements/drafts", "GET /api/admin/agreements/drafts/:id", "PUT /api/admin/agreements/drafts/:id", "DELETE /api/admin/agreements/drafts/:id", "POST /api/admin/agreements/drafts/:id/publish", "GET /api/admin/agreements/releases", "GET /api/admin/agreements/records", "POST /api/admin/agreements/drafts/batch-delete"} {
		if !got[path] {
			t.Errorf("missing %s", path)
		}
	}
}

func TestAgreementHeaderScopedToClientRoutes(t *testing.T) {
	r := gin.New()
	SetAPIRouter(r)
	for _, path := range []string{"/api/agreements/status", "/api/agreements/current", "/api/agreements/consents", "/api/search"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("OPTIONS", path, nil)
		req.Header.Set("Origin", "https://fixture.invalid")
		r.ServeHTTP(w, req)
		want := path != "/api/search"
		if w.Code != 204 || strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Agreement-Device") != want || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("scope mismatch: %s", path)
		}
	}
}
