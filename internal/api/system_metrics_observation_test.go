package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type b04RouteObservation struct {
	Case         string          `json:"case"`
	Status       int             `json:"status"`
	CacheControl string          `json:"cacheControl"`
	Body         json.RawMessage `json:"body"`
}

// The real API group requires its production Manager. A subprocess ensures
// Manager, Viper, JWT, logger and DAO state cannot escape this temporary fixture.
func b04RunIsolatedManager(t *testing.T) bool {
	t.Helper()
	if os.Getenv("B04_METRICS_CHILD") == t.Name() {
		http.DefaultTransport = s03RT(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("B04_REAL_NETWORK_FORBIDDEN") })
		m, err := manager.CreateManager()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := m.CloseManager(); err != nil {
				t.Error(err)
			}
		})
		return false
	}
	dir := t.TempDir()
	config := fmt.Sprintf("database:\n  path: %q\nlimit:\n  enable: false\n", filepath.Join(dir, "b04-only-fixture.sqlite"))
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	child.Dir = dir
	child.Env = append(os.Environ(), "B04_METRICS_CHILD="+t.Name(), "TIKU_APP_MODE=production", "TIKU_ADMIN_PASSWORD=B04SyntheticPassword!29", "SQL_DSN=")
	output, err := child.CombinedOutput()
	fmt.Print(string(output))
	if err != nil {
		t.Fatalf("isolated fixture: %v", err)
	}
	return true
}

func b04ObserveProductionRoute(t *testing.T) []b04RouteObservation {
	t.Helper()
	run := s03Begin(t)
	gin.SetMode(gin.TestMode)
	db, err := run.openMemory(t, func(db *gorm.DB) error { return db.AutoMigrate(&entity.User{}) })
	if err != nil {
		t.Fatal(err)
	}
	password := "B04_SYNTHETIC_CREDENTIAL_VERSION"
	for _, u := range []entity.User{
		{ID: 951, Username: "b04-admin", Password: password, Role: "admin", Status: "enabled"},
		{ID: 952, Username: "b04-user", Password: password, Role: "user", Status: "enabled"},
		{ID: 953, Username: "b04-disabled", Password: password, Role: "admin", Status: "disabled"},
	} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	http.DefaultTransport = s03RT(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("B04_REAL_NETWORK_FORBIDDEN")
	})
	router := gin.New()
	SetAPIRouter(router)
	SetWebRouter(embed.FS{}, []byte("<html>b04-spa-fixture</html>"), router)
	cases := []struct{ name, token, cookie string }{
		{"anonymous", "", ""},
		{"user-query-admin", util.GenerateJwtForUser(952, password), ""},
		{"admin", util.GenerateJwtForUser(951, password), ""},
		{"admin-cookie", "", util.GenerateJwtForUser(951, password)},
		{"disabled-admin", util.GenerateJwtForUser(953, password), ""},
		{"invalid-header-beats-cookie", "B04_INVALID_SYNTHETIC", util.GenerateJwtForUser(951, password)},
	}
	observed := make([]b04RouteObservation, 0, len(cases))
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/adapter-service/system-metrics?role=admin&owner=B04_QUERY_SENTINEL&requestId=B04_QUERY_SENTINEL", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", tc.token)
		}
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "tiku_token", Value: tc.cookie})
		}
		req.Header.Set("X-Request-ID", "B04_QUERY_SENTINEL")
		w := httptest.NewRecorder()
		s03Serve(router, w, req)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("%s must use the real JSON API boundary, status=%d body=%q", tc.name, w.Code, w.Body.String())
		}
		observed = append(observed, b04RouteObservation{tc.name, w.Code, w.Header().Get("Cache-Control"), append([]byte(nil), w.Body.Bytes()...)})
	}
	if calls != 0 {
		t.Fatalf("read-only metrics made %d outbound requests", calls)
	}
	return observed
}

// This unchanged driver is also executed against the pristine and rollback routes.
// It reports actual routing behavior; feature acceptance lives in the strict test.
func TestB04SystemMetricsRouteObservation(t *testing.T) {
	if b04RunIsolatedManager(t) {
		return
	}
	b, err := json.Marshal(b04ObserveProductionRoute(t))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("B04_ROUTE_OBSERVATION=%s\n", b)
}
