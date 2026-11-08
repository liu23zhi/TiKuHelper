package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
)

// The upstream feature must be registered and protected, not a SPA 200,
// anonymous data response, or a missing-route 404. The real manager is isolated
// in a child with a synthetic temporary database; no provider is configured.
func TestLatestUpstreamSessionAndOCRRoutesRequireAuthentication(t *testing.T) {
	if os.Getenv("UPSTREAM_ROUTE_CONTRACT_CHILD") != "1" {
		dir := t.TempDir()
		config, err := json.Marshal(map[string]any{"database": map[string]any{"path": filepath.Join(dir, "synthetic.db")}, "limit": map[string]any{"enable": true, "duration": 60, "requests": 120}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), config, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestLatestUpstreamSessionAndOCRRoutesRequireAuthentication$", "-test.v")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "UPSTREAM_ROUTE_CONTRACT_CHILD=1", "SQL_DSN=", "JWT_SECRET=RouteContractSyntheticSecret246!ABC", "TIKU_ADMIN_PASSWORD=RouteFixture246!", "TIKU_APP_MODE=production", "EXTENSION_UPDATE_ENABLED=false", "EXTENSION_EDGE_ID=", "EXTENSION_CHROME_ID=")
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		if err != nil {
			t.Errorf("isolated real-router contract failed: %v", err)
		}
		return
	}
	m, err := manager.CreateManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.CloseManager(); err != nil {
			t.Error(err)
		}
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/adapter-service/user/session"},
		{http.MethodGet, "/adapter-service/ai-search-config/ocr-cache"},
		{http.MethodGet, "/adapter-service/ai-search-config/ocr-cache/entries"},
		{http.MethodDelete, "/adapter-service/ai-search-config/ocr-cache"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			out := httptest.NewRecorder()
			router.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
			t.Logf("%s %s => %d", tc.method, tc.path, out.Code)
			if out.Code != http.StatusUnauthorized {
				t.Errorf("upstream protected endpoint status=%d want=401", out.Code)
			}
			if out.Header().Get("Cache-Control") != "no-store" {
				t.Error("private auth response lacks no-store")
			}
			for _, secret := range []string{`"jwt"`, `"ocrText"`, `"urlHash"`, `"apiKey"`} {
				if strings.Contains(out.Body.String(), secret) {
					t.Errorf("anonymous response leaks %s", secret)
				}
			}
		})
	}
	// Existing strict token-only observer auth stays protected in both versions.
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/adapter-service/search/health", nil))
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("existing observer guard=%d want=401", out.Code)
	}
	t.Log("UPSTREAM_ROUTE_CONTROL_AUTH=401; all four subcases reached tail")
}
