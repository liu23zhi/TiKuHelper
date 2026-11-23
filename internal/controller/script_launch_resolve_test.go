package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestResolveScriptLaunchThroughHostBoundary(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "resolve-owner", "user")
	admin := seedControllerUser(t, db, 0, "resolve-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "resolve-fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"*.resolve.test"}}); err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "resolve-fixture", "fixture.exe", []byte("MZ"), true)
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "resolve", VersionID: version.ID, DomainFormat: "*.resolve.test", LauncherOrigin: &service.ScriptLauncherOrigin{Host: "site.resolve.test", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	full, err := service.ConfigureScriptLauncherAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 5, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(ScriptHostBoundary(db))
	router.POST("/api/script-launch/resolve", resolveScriptLaunch(db))
	identity := strings.Repeat("1", 64)
	origin := strings.TrimSuffix(full.Address, "/")
	call := func(requestOrigin, address, identity string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(gin.H{"address": address, "identity": identity})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, requestOrigin+"/api/script-launch/resolve", bytes.NewReader(body))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := call("https://site.resolve.test", full.Address, identity); response.Code == http.StatusOK {
		t.Fatal("cross-origin address was accepted")
	}
	if response := call(origin, full.Address, "bad"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid identity status = %d", response.Code)
	}
	response := call(origin, full.Address, identity)
	var grant struct {
		Origin       string
		Identity     string
		Token        string
		ExpiresAt    string
		HeartbeatURL string
		Parameters   map[string]any
	}
	if err := json.Unmarshal(response.Body.Bytes(), &grant); err != nil || response.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s, error = %v", response.Code, response.Body, err)
	}
	if grant.Origin != origin || grant.Identity != identity || grant.Token == "" || grant.ExpiresAt == "" || grant.HeartbeatURL != "/api/script-command/"+item.ShortCode+"/heartbeat" {
		t.Fatalf("unexpected grant metadata: origin=%q identity=%q heartbeat=%q", grant.Origin, grant.Identity, grant.HeartbeatURL)
	}
	if _, _, err := service.ResolveScriptRunSessionWithDB(db, grant.Token, identity, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if response := call(origin, full.Address, strings.Repeat("2", 64)); response.Code == http.StatusOK {
		t.Fatal("second launch ignored the authorization limit")
	}
}

func TestResolveScriptLaunchShortEntriesAndBusyRollback(t *testing.T) {
	for _, shortAddress := range []string{"https://quick.resolve.test", "https://site.resolve.test/quick"} {
		t.Run(shortAddress, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			owner := seedControllerUser(t, db, 0, "short-owner", "user")
			admin := seedControllerUser(t, db, 0, "short-admin", "admin")
			if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "short-fixture"}).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"*.resolve.test", "site.resolve.test/*"}}); err != nil {
				t.Fatal(err)
			}
			version, err := service.UploadScriptVersionWithDB(db, admin, "short-fixture", "fixture.exe", []byte("MZ"), true)
			if err != nil {
				t.Fatal(err)
			}
			item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "short", VersionID: version.ID, DomainFormat: "*.resolve.test", LauncherOrigin: &service.ScriptLauncherOrigin{Host: "site.resolve.test", HTTPS: true}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: shortAddress, DurationMinutes: 5, MaxUses: 2}); err != nil {
				t.Fatal(err)
			}
			address, _ := url.Parse(shortAddress)
			router := gin.New()
			router.Use(ScriptHostBoundary(db))
			router.POST(scriptLaunchResolvePath, resolveScriptLaunch(db))
			call := func(identity string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(gin.H{"address": shortAddress, "identity": identity})
				request := httptest.NewRequest(http.MethodPost, address.Scheme+"://"+address.Host+scriptLaunchResolvePath, bytes.NewReader(body))
				request.RemoteAddr = "127.0.0.1:12345"
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			first := call(strings.Repeat("1", 64))
			if first.Code != http.StatusOK {
				t.Fatalf("valid short entry rejected: status=%d body=%s", first.Code, first.Body)
			}
			second := call(strings.Repeat("2", 64))
			if second.Code == http.StatusOK {
				t.Fatal("short entry replaced an online client")
			}
			var stored entity.UserScript
			if err := db.First(&stored, item.ID).Error; err != nil || stored.ShortEntryUsedCount != 1 {
				t.Fatalf("rejected launch consumed a short command use: count=%d error=%v", stored.ShortEntryUsedCount, err)
			}
			if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).Update("last_heartbeat_at", time.Now().Add(-6*time.Minute)).Error; err != nil {
				t.Fatal(err)
			}
			if response := call(strings.Repeat("2", 64)); response.Code != http.StatusOK {
				t.Fatalf("unused short command budget did not survive busy rejection: status=%d body=%s", response.Code, response.Body)
			}
			if response := call(strings.Repeat("3", 64)); response.Code == http.StatusOK {
				t.Fatal("exhausted short authorization fell back to a full launcher")
			}
		})
	}
}
