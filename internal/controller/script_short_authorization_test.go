package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestScriptShortAuthorizationHTTPUsesExplicitOwnerGrantOnly(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "short-http-owner", "user")
	admin := seedControllerUser(t, db, 0, "short-http-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 40, "phone": "13800000000", "wechat_id": "short-http"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"*.muioi.com", "muioi.com/*"}}); err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "short-http-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "short-http", VersionID: version.ID, BaseDomain: "a.muioi.com"})
	if err != nil {
		t.Fatal(err)
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	actor := &owner
	router := gin.New()
	router.Use(ScriptPublicHostMiddleware)
	router.PUT("/api/scripts/:id/short-authorization", func(c *gin.Context) { c.Set("user", actor) }, ConfigureScriptShortAuthorization)
	router.NoRoute(func(c *gin.Context) {
		if !ScriptPublicPath(c) {
			c.Status(http.StatusNotFound)
		}
	})
	call := func(method, host, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	path := fmt.Sprintf("/api/scripts/%d/short-authorization", script.ID)
	if res := call("GET", "a.muioi.com", "/", ""); res.Code != http.StatusNotFound {
		t.Fatalf("default public root accepted: %d", res.Code)
	}
	actor = &admin
	if res := call("PUT", "console.test", path, `{"enabled":true,"address":"a.muioi.com","durationMinutes":5,"maxUses":1}`); res.Code != http.StatusNotFound {
		t.Fatalf("admin minted owner's short grant: %d", res.Code)
	}
	actor = &owner
	res := call("PUT", "console.test", path, `{"enabled":true,"address":"a.muioi.com","durationMinutes":5,"maxUses":1}`)
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("owner grant failed: %d %s", res.Code, res.Body.String())
	}
	var grant service.ScriptShortAuthorizationView
	if err := json.Unmarshal(res.Body.Bytes(), &grant); err != nil || grant.Address != "https://a.muioi.com" {
		t.Fatalf("grant address incorrect: %v", err)
	}
	res = call("GET", "a.muioi.com", "/", "")
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" || !strings.Contains(res.Body.String(), "https://a.muioi.com/api/script-command/") || strings.Contains(res.Body.String(), script.AccessKey) {
		t.Fatalf("one-use short bootstrap invalid: %d", res.Code)
	}
	if res := call("GET", "a.muioi.com", "/", ""); res.Code != http.StatusNotFound {
		t.Fatalf("short grant replay accepted: %d", res.Code)
	}
}
