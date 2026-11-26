package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestReviewRebateScreenshotProductionRoute(t *testing.T) {
	// The real router requires the application manager. Run it in its own
	// process and fresh directory without inheriting any business environment.
	if os.Getenv("REVIEW_REBATE_SCREENSHOT_TEST_WORKER") == "" {
		dir := t.TempDir()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestReviewRebateScreenshotProductionRoute$", "-test.v")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "REVIEW_REBATE_SCREENSHOT_TEST_WORKER=" + dir}
		output, err := cmd.CombinedOutput()
		t.Logf("synthetic route worker:\n%s", output)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	root, err := os.Getwd()
	if err != nil || root != os.Getenv("REVIEW_REBATE_SCREENSHOT_TEST_WORKER") {
		t.Fatal("worker must run in its owned fixture directory")
	}
	var outbound atomic.Int64
	http.DefaultTransport = s01CompositionNoDial{&outbound}
	t.Cleanup(func() {
		if n := outbound.Load(); n != 0 {
			t.Errorf("unexpected external transport calls: %d", n)
		}
	})
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(root, "claims.db")
	if err := os.WriteFile("config.yaml", []byte(fmt.Sprintf("database:\n  path: %q\nmysql: \"\"\napi: []\nelasticsearch:\n  addresses: []\noss: {}\nlimit:\n  enable: false\njwtSecret: \"REVIEW_REBATE_SCREENSHOT_TEST_FIXTURE_ONLY\"\n", dbPath)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := registry.ConfigWithError()
	if err != nil || cfg.Database.Path != dbPath || cfg.Mysql != "" {
		t.Fatal("unexpected test database configuration")
	}
	seed, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := seed.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := seed.AutoMigrate(&entity.User{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 101, Role: "admin", Status: "enabled", RegisterSource: "system", Username: "fixture-admin", Password: "fixture-credential-version"}
	member := entity.User{ID: 102, Role: "user", Status: "enabled", RegisterSource: "system", Username: "fixture-member", Password: "fixture-credential-version"}
	if err := seed.Create([]entity.User{admin, member}).Error; err != nil {
		t.Fatal(err)
	}
	*logger.LogDir = filepath.Join(root, "logs")
	mgr, err := manager.CreateManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mgr.CloseManager(); err != nil {
			t.Error(err)
		}
		if err := logger.Close(); err != nil {
			t.Error(err)
		}
	})
	if mgr.GetDB().Dialector.Name() != "sqlite" {
		t.Fatal("expected isolated SQLite fixture")
	}
	screenshot := "review-rebate/fixture.png"
	screenshotFile := filepath.Join("data", screenshot)
	if err := os.MkdirAll(filepath.Dir(screenshotFile), 0700); err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(screenshotFile, pngBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	claim := entity.ReviewRebateClaim{ID: 42, ProgramID: 1, UserID: member.ID, ScreenshotPath: screenshot, ScreenshotSHA256: "fixture-sha"}
	if err := mgr.GetDB().Create(&claim).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	SetAPIRouter(router)
	get := func(path string, actor *entity.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if actor != nil {
			req.AddCookie(&http.Cookie{Name: "tiku_token", Value: util.GenerateJwtForUser(actor.ID, actor.Password)})
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	const canonical = "/api/admin/review-rebate/claims/42/screenshot"
	const legacy = "/adapter-service/admin/review-rebate/claims/42/screenshot"
	for _, test := range []struct {
		name, path string
		actor      *entity.User
		want       int
	}{
		{"legacy path", legacy, &admin, http.StatusNotFound},
		{"anonymous", canonical, nil, http.StatusUnauthorized},
		{"ordinary owner", canonical, &member, http.StatusForbidden},
		{"private disk path", "/data/" + screenshot, nil, http.StatusNotFound},
		{"missing record", "/api/admin/review-rebate/claims/43/screenshot", &admin, http.StatusNotFound},
	} {
		out := get(test.path, test.actor)
		t.Logf("%s: HTTP %d", test.name, out.Code)
		if out.Code != test.want || bytes.Equal(out.Body.Bytes(), pngBytes.Bytes()) {
			t.Errorf("%s: wrong status or disclosed image; got HTTP %d, want %d", test.name, out.Code, test.want)
		}
	}
	readList := func(path string, actor *entity.User) map[string]json.RawMessage {
		t.Helper()
		out := get(path, actor)
		var result struct{ Data []map[string]json.RawMessage }
		if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &result) != nil || len(result.Data) != 1 || string(result.Data[0]["id"]) != "42" {
			t.Fatalf("list %s: expected HTTP 200 with the single fixture claim, got HTTP %d", path, out.Code)
		}
		return result.Data[0]
	}
	own := readList("/api/user/review-rebate/claims", &member)
	for key := range own {
		if strings.HasPrefix(strings.ToLower(key), "screenshot") {
			t.Errorf("ordinary list leaked a screenshot field: %s", key)
		}
	}
	t.Log("ordinary list: HTTP 200, no screenshot fields")
	row := readList("/api/admin/review-rebate/claims", &admin)
	var issued string
	if err := json.Unmarshal(row["screenshotUrl"], &issued); err != nil {
		t.Fatal("admin list omitted screenshotUrl")
	}
	switch issued {
	case legacy, canonical:
	default:
		t.Fatal("admin list issued an unexpected screenshot destination")
	}
	out := get(issued, &admin)
	t.Logf("admin list-issued route: %s -> HTTP %d", issued, out.Code)
	if issued != canonical || out.Code != http.StatusOK || !bytes.Equal(out.Body.Bytes(), pngBytes.Bytes()) ||
		out.Header().Get("Content-Type") != "image/png" || out.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("admin list must issue the canonical private PNG route, got %s HTTP %d", issued, out.Code)
	}
	if err := os.Remove(screenshotFile); err != nil {
		t.Fatal(err)
	}
	out = get(canonical, &admin)
	t.Logf("missing screenshot: HTTP %d", out.Code)
	if out.Code != http.StatusNotFound || bytes.Equal(out.Body.Bytes(), pngBytes.Bytes()) {
		t.Errorf("missing screenshot: got %d, want 404 without image bytes", out.Code)
	}
}
