package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestCompactCommandRequiresCapabilityAndRetainsHeaderCompatibility(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "compact-owner", "user")
	admin := seedControllerUser(t, db, 0, "compact-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "compact-fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "compact-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "compact", VersionID: version.ID, LauncherOrigin: &service.ScriptLauncherOrigin{Host: "fixture.invalid", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`iex\(irm (https://[^ ]+) `).FindStringSubmatch(item.Command)
	if len(match) != 2 {
		t.Fatalf("expected compact credential URL, got %s", item.Command)
	}
	address, err := url.Parse(match[1])
	header := regexp.MustCompile(`X-Script-Launcher'='([A-Za-z0-9_-]{43})'`).FindStringSubmatch(item.Command)
	if err != nil || len(header) != 2 {
		t.Fatal("missing launcher capability")
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	router := gin.New()
	router.NoRoute(func(c *gin.Context) {
		if !servePublicScriptBootstrap(c) {
			c.Status(404)
		}
	})
	call := func(raw, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		req.Host = address.Host
		req.Header.Set(service.ScriptLauncherHeader, header)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	public := *address
	public.RawQuery = ""
	for _, raw := range []string{public.String(), public.String() + "?l=" + strings.Repeat("a", 43)} {
		if response := call(raw, ""); response.Code != http.StatusNotFound {
			t.Fatalf("unauthorized request issued grant: %d", response.Code)
		}
	}
	response := call(address.String(), header[1])
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(response.Body.String(), header[1]) {
		t.Fatalf("compact capability rejected or leaked: %d", response.Code)
	}
	// Retire the issued exchange so the same authorization can test the old header path.
	if err := db.Model(&entity.ScriptExchangeToken{}).Where("script_id = ?", item.ID).Update("used_at", db.NowFunc()).Error; err != nil {
		t.Fatal(err)
	}
	if response = call(public.String()+"?l=ignored-legacy-query", header[1]); response.Code != 200 {
		t.Fatalf("legacy header authorization rejected: %d", response.Code)
	}
	var stored entity.UserScript
	if err := db.First(&stored, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfigureScriptLauncherAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if response = call(address.String(), header[1]); response.Code != 404 {
		t.Fatalf("revoked compact URL survived: %d", response.Code)
	}
}
