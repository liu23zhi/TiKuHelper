package api

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTasklistAPIUnknownRoutesReturnJSON404(t *testing.T) {
	router := gin.New()
	SetWebRouter(embed.FS{}, []byte("<html>spa-fixture</html>"), router)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{"/adapter-service", "/adapter-service/__missing__", "/adapter-service/questions/unknown/path"} {
			t.Run(method+path, func(t *testing.T) {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
				if response.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404; body=%s", response.Code, response.Body.String())
				}
				if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("Content-Type = %q, want JSON", response.Header().Get("Content-Type"))
				}
				var body map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["code"] != "NOT_FOUND" || body["message"] == "" {
					t.Fatalf("unexpected error contract: %#v", body)
				}
			})
		}
	}
}

func TestTasklistAPISPAFallbackPreservesClientRoutes(t *testing.T) {
	router := gin.New()
	index := "<html>spa-fixture</html>"
	SetWebRouter(embed.FS{}, []byte(index), router)
	for _, path := range []string{"/login", "/adapter/user-usage", "/adapter/tiku", "/adapter-service-preview"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.String() != index {
				t.Fatalf("SPA refresh failed: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
