package controller

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestLongLocatorStartsWithoutHeaderAndLegacyHeaderRemainsCompatible(t *testing.T) {
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
		t.Fatalf("expected bearer UUID URL, got %s", item.Command)
	}
	address, err := url.Parse(match[1])
	if err != nil || address.RawQuery != "" || strings.Contains(item.Command, "X-Script-Launcher") {
		t.Fatal("new long command leaked its credential outside the random UUID address")
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
	callAtHost := func(raw, host, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		req.Host = host
		req.Header.Set(service.ScriptLauncherHeader, header)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	call := func(raw, header string) *httptest.ResponseRecorder { return callAtHost(raw, address.Host, header) }
	public := *address
	public.RawQuery = ""
	wrongHost := callAtHost(public.String(), "wrong.fixture.invalid", "")
	if wrongHost.Code != http.StatusNotFound {
		t.Fatalf("wrong host started locator: %d", wrongHost.Code)
	}
	response := call(public.String(), "")
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("UUID bearer locator should start without a credential header: %d", response.Code)
	}
	if strings.Contains(response.Body.String(), item.AccessKey) {
		t.Fatal("bootstrap response exposed the script access key")
	}
	// Retire the issued exchange so the same authorization can test the old header path.
	if err := db.Model(&entity.ScriptExchangeToken{}).Where("script_id = ?", item.ID).Update("used_at", db.NowFunc()).Error; err != nil {
		t.Fatal(err)
	}
	var stored entity.UserScript
	if err := db.First(&stored, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	compatibility := strings.TrimPrefix(service.BuildScriptCompatibilityCommand(stored), "powershell -EncodedCommand ")
	wire, err := base64.StdEncoding.DecodeString(compatibility)
	if err != nil || len(wire)%2 != 0 {
		t.Fatal("legacy compatibility command must be UTF-16LE encoded")
	}
	units := make([]uint16, len(wire)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(wire[i*2:])
	}
	match = regexp.MustCompile(`Headers.Add\('X-Script-Launcher','([A-Za-z0-9_-]+)'\)`).FindStringSubmatch(string(utf16.Decode(units)))
	if len(match) != 2 {
		t.Fatal("legacy compatibility command lost its header capability")
	}
	legacyCapability := match[1]
	if response = call(public.String(), legacyCapability); response.Code != 200 {
		t.Fatalf("legacy header authorization rejected: %d", response.Code)
	}
	if _, err := service.ConfigureScriptLauncherAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if response = call(public.String(), ""); response.Code != 404 {
		t.Fatalf("revoked UUID URL survived: %d", response.Code)
	}
}
