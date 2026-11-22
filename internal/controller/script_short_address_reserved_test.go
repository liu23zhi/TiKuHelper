package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestScriptShortAddressHTTPRejectsReservedPaths(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "short-address.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.ScriptVersion{}, &entity.UserScript{}, &entity.ScriptAddressReservation{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "reserved-short-owner", "user")
	admin := seedControllerUser(t, db, 0, "reserved-short-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 20, "phone": "13800000000", "wechat_id": "reserved-short"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"zelly.cn/*"}}); err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "reserved-short-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "fixture", VersionID: version.ID, BaseDomain: "zelly.cn"})
	if err != nil {
		t.Fatal(err)
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	router := gin.New()
	router.PUT("/api/scripts/:id/short-authorization", func(c *gin.Context) { c.Set("user", &owner) }, ConfigureScriptShortAuthorization)
	configure := func(address string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: address, DurationMinutes: 10})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("https://zelly.cn/api/scripts/%d/short-authorization", script.ID), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	for _, path := range []string{
		"/api", "/api/scripts", "/ApI/scripts", "/adapter-service/scripts",
		"/healthz", "/readyz", "/css/app.css", "/js/app.js", "/img/logo.png",
		"/fonts/site.woff2", "/adapter/chaoxing", "/index.html", "/theme-init.js",
	} {
		address := "zelly.cn" + path
		input := service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: address, DurationMinutes: 10}
		if _, err := service.ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); !errors.Is(err, service.ErrScriptReservedPrefix) {
			t.Errorf("reserved address %s returned %v, want ErrScriptReservedPrefix", address, err)
		}
		assertShortAddressBadRequest(t, address, configure(address))
	}
	for _, path := range []string{"/%61pi/scripts", "/api%2Fscripts", "/ok/%2e%2e/api/scripts", "/ok/../api/scripts"} {
		address := "zelly.cn" + path
		assertShortAddressBadRequest(t, address, configure(address))
	}
	var saved entity.UserScript
	if err := db.First(&saved, script.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ShortEntryAddress != "" || saved.ShortEntryAddressKey != nil || saved.ShortEntryGeneration != 0 {
		t.Fatal("reserved path request changed the short authorization")
	}
	if res := configure("zelly.cn/api-tools/scripts"); res.Code != http.StatusOK {
		t.Fatalf("unreserved path returned %d: %s", res.Code, res.Body)
	}
}

func assertShortAddressBadRequest(t *testing.T, address string, res *httptest.ResponseRecorder) {
	t.Helper()
	if res.Code != http.StatusBadRequest {
		t.Errorf("reserved address %s returned %d instead of 400: %s", address, res.Code, res.Body)
	}
}
