package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func tasklistHealthRouter(check func(context.Context) error) *gin.Engine {
	router := gin.New()
	SetHealthRouter(router, check)
	SetWebRouter(embed.FS{}, []byte("<html>spa-fixture</html>"), router)
	return router
}

func assertTasklistHealth(t *testing.T, router http.Handler, req *http.Request, status int, state string) {
	t.Helper()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("health must not be SPA HTML: %q body=%s", response.Header().Get("Content-Type"), response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["status"] != state {
		t.Fatalf("unexpected or leaking health response: %#v", body)
	}
}

func TestTasklistAPIHealthLivenessDoesNotCheckDatabase(t *testing.T) {
	calls := 0
	router := tasklistHealthRouter(func(context.Context) error {
		calls++
		return errors.New("private database unavailable")
	})
	assertTasklistHealth(t, router, httptest.NewRequest(http.MethodGet, "/healthz", nil), 200, "ok")
	if calls != 0 {
		t.Fatalf("liveness called database %d times", calls)
	}
}

func TestTasklistAPIHealthReadinessFailureIsSanitized(t *testing.T) {
	for _, check := range []func(context.Context) error{nil, func(context.Context) error { return errors.New("password=private DSN") }} {
		assertTasklistHealth(t, tasklistHealthRouter(check), httptest.NewRequest(http.MethodGet, "/readyz", nil), 503, "not_ready")
	}
}

func TestTasklistAPIHealthReadinessContextIsBounded(t *testing.T) {
	called := false
	router := tasklistHealthRouter(func(ctx context.Context) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
			t.Errorf("readiness requires a positive deadline of at most one second")
		}
		return nil
	})
	assertTasklistHealth(t, router, httptest.NewRequest(http.MethodGet, "/readyz", nil), 200, "ready")
	if !called {
		t.Fatal("readiness did not check its dependency")
	}
}

func TestTasklistAPIHealthCanceledRequestCannotReportReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx)
	assertTasklistHealth(t, tasklistHealthRouter(func(context.Context) error { return nil }), request, 503, "not_ready")
}

func TestTasklistAPIHealthReadinessReflectsRealDatabaseClosure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/readiness.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	router := tasklistHealthRouter(sqlDB.PingContext)
	assertTasklistHealth(t, router, httptest.NewRequest(http.MethodGet, "/readyz", nil), 200, "ready")
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	assertTasklistHealth(t, router, httptest.NewRequest(http.MethodGet, "/readyz", nil), 503, "not_ready")
	assertTasklistHealth(t, router, httptest.NewRequest(http.MethodGet, "/healthz", nil), 200, "ok")
}
