package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/extensionlogs"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestExtensionLogsUploadReadAndConfigure(t *testing.T) {
	keepFiles, keepOwner := extensionLogFiles, extensionLogOwner
	t.Cleanup(func() { extensionLogFiles, extensionLogOwner = keepFiles, keepOwner })
	extensionLogFiles = extensionlogs.New(t.TempDir())
	extensionLogOwner = func(a string) (int32, error) {
		if a == "Bearer fixture-only" {
			return 17, nil
		}
		return 0, service.ErrInvalidAccessTokenAuthorization
	}
	r := gin.New()
	r.POST("/api/extension-logs", UploadExtensionLogs)
	r.POST("/api/extension-logs/start", StartExtensionLogs)
	r.GET("/api/extension-logs/policy", GetExtensionIngestPolicy)
	admin := r.Group("/api/admin", func(c *gin.Context) {
		if role := c.GetHeader("X-Test-Role"); role != "" {
			c.Set("user", &entity.User{ID: 9, Role: role})
		}
	}, middleware.RequireAdmin)
	admin.GET("/extension-logs", AdminExtensionLogs)
	admin.GET("/extension-logs/settings", GetExtensionLogPolicy)
	admin.PUT("/extension-logs/settings", SetExtensionLogPolicy)
	call := func(method, path, body, key, role string) (int, string) {
		t.Helper()
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Authorization", key)
		q.Header.Set("X-Test-Role", role)
		res := httptest.NewRecorder()
		r.ServeHTTP(res, q)
		return res.Code, res.Body.String()
	}
	batch := `{"version":"1.2.3","device_id":"fixture-device","launch_id":"11111111-1111-4111-8111-111111111111","entries":[{"time":"2026-11-21T08:00:00Z","level":"warn","event":"network.offline"}]}`
	base := "/api/extension-logs"
	adm := "/api/admin/extension-logs"
	for _, key := range []string{"", "Bearer invalid"} {
		if code, _ := call("POST", base, batch, key, ""); code != 401 {
			t.Fatalf("without token %d", code)
		}
	}
	for _, b := range []string{strings.Replace(batch, `"warn"`, `"warn","email":"do-not-store"`, 1), batch + " {}", strings.Replace(batch, "network.offline", "unsupported", 1)} {
		if code, _ := call("POST", base, b, "Bearer fixture-only", ""); code != 400 {
			t.Fatalf("invalid event accepted %d", code)
		}
	}
	meta := `{"version":"1.2.3","device_id":"fixture-device","launch_id":"11111111-1111-4111-8111-111111111111"}`
	if code, _ := call("GET", base+"/policy", "", "", ""); code != 401 {
		t.Fatalf("anonymous policy %d", code)
	}
	if code, body := call("GET", base+"/policy", "", "Bearer fixture-only", ""); code != 200 || body != `{"enabled":true}` {
		t.Fatalf("policy leaks fields: %d %s", code, body)
	}
	if code, body := call("POST", base, batch, "Bearer fixture-only", ""); code != 409 || !strings.Contains(body, "need_start") {
		t.Fatalf("missing launch not detected: %d %s", code, body)
	}
	for _, code := range []int{201, 200} {
		if c, _ := call("POST", base+"/start", meta, "Bearer fixture-only", ""); c != code {
			t.Fatalf("idempotent start %d, want %d", c, code)
		}
	}
	if c, _ := call("POST", base, batch, "Bearer fixture-only", ""); c != 201 {
		t.Fatalf("ingest %d", c)
	}
	if c, _ := call("GET", adm, "", "Bearer fixture-only", "user"); c != 403 {
		t.Fatalf("regular user could list all %d", c)
	}
	if c, raw := call("GET", adm, "", "", "admin"); c != 200 || !strings.Contains(raw, `"user_id":17`) || !strings.Contains(raw, "network.offline") || strings.Contains(raw, "fixture-only") {
		t.Fatalf("admin log not scoped or sanitized (%d)", c)
	}
	c, raw := call("GET", adm+"/settings", "", "", "admin")
	var p extensionlogs.Policy
	if c != 200 || json.Unmarshal([]byte(raw), &p) != nil || p != (extensionlogs.Policy{Enabled: true, RetentionDays: 7, MaxGB: 10}) {
		t.Fatalf("defaults %d %s", c, raw)
	}
	if c, _ = call("PUT", adm+"/settings", `{"enabled":false,"retention_days":5,"max_gb":3}`, "", "user"); c != 403 {
		t.Fatalf("nonadmin edit %d", c)
	}
	if c, _ = call("PUT", adm+"/settings", `{"enabled":false,"retention_days":0,"max_gb":3}`, "", "admin"); c != 400 {
		t.Fatalf("invalid policy %d", c)
	}
	if c, raw = call("PUT", adm+"/settings", `{"enabled":false,"retention_days":5,"max_gb":3}`, "", "admin"); c != 200 || !strings.Contains(raw, `"max_gb":3`) {
		t.Fatalf("policy update %d %s", c, raw)
	}
	if c, _ = call("POST", base, batch, "Bearer fixture-only", ""); c != 403 {
		t.Fatalf("disabled ingest %d", c)
	}
	if c, _ = call("POST", base+"/start", meta, "Bearer fixture-only", ""); c != 403 {
		t.Fatalf("disabled start %d", c)
	}
	if c, raw = call("GET", adm+"?user_id=17&device_id=fixture-device&launch_id=11111111-1111-4111-8111-111111111111", "", "", "admin"); c != 200 || !strings.Contains(raw, "session.start") {
		t.Fatalf("missing launch: %d", c)
	}
	if c, raw = call("GET", adm+"?user_id=18", "", "", "admin"); c != 200 || strings.Contains(raw, "fixture-device") {
		t.Fatalf("filter ignored: %d", c)
	}
	if c, _ = call("GET", adm+"?user_id=-1", "", "", "admin"); c != 400 {
		t.Fatal("invalid account id")
	}
	if c, _ = call("GET", adm, "", "Bearer fixture-only", ""); c != 403 {
		t.Fatal("extension bearer allowed log read")
	}

}
