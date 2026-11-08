package controller

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestScriptLaunchersStayPowerShell2Compatible(t *testing.T) {
	bootstrap := buildScriptBootstrap("https://a.a.com", "a", "short-lived")
	for _, fragment := range []string{"New-Object Net.WebClient", "DownloadString", "X-Script-Token"} {
		if !strings.Contains(bootstrap, fragment) {
			t.Fatalf("bootstrap missing %q: %s", fragment, bootstrap)
		}
	}
	for _, incompatible := range []string{"Invoke-RestMethod", "ConvertFrom-Json", "ConvertTo-Json"} {
		if strings.Contains(bootstrap, incompatible) {
			t.Fatalf("PowerShell 2 bootstrap must not use %q: %s", incompatible, bootstrap)
		}
	}

	payload := buildScriptPayload("https://a.a.com", "a", "/adapter-service/script-command/a/file", "tk_secret", "eyJzZXR0aW5ncyI6e30=")
	for _, fragment := range []string{"DownloadFile", "CXPLUS_SCRIPT_BOOTSTRAP_B64", "Start-Process", "WaitForExit", "Remove-Item", "X-Script-Activation"} {
		if !strings.Contains(payload, fragment) {
			t.Fatalf("payload missing %q: %s", fragment, payload)
		}
	}
	for _, incompatible := range []string{"Invoke-RestMethod", "ConvertFrom-Json", "ConvertTo-Json"} {
		if strings.Contains(payload, incompatible) {
			t.Fatalf("PowerShell 2 payload must not use %q: %s", incompatible, payload)
		}
	}
}

func TestScriptBootstrapUsesConfiguredHTTPSOriginAndHeaderCapability(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "launcher-http-fixture", "user")
	admin := seedControllerUser(t, db, 0, "launcher-admin-fixture", "admin")
	if err := db.Model(&owner).Update("balance", 20).Error; err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "fixture-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "fixture", VersionID: version.ID, BaseDomain: "fixture.test"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, ok := strings.CutPrefix(item.Command, "powershell -EncodedCommand ")
	if !ok {
		t.Fatal("owner launcher must be PowerShell encoded")
	}
	wire, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(wire)%2 != 0 {
		t.Fatal("launcher is not UTF-16LE Base64")
	}
	units := make([]uint16, len(wire)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(wire[i*2:])
	}
	command := string(utf16.Decode(units))
	match := regexp.MustCompile(`Headers.Add\('X-Script-Launcher','([A-Za-z0-9_-]+)'\)`).FindStringSubmatch(command)
	if len(match) != 2 || strings.Contains(item.DownloadURL, match[1]) || strings.Contains(command, item.AccessKey) {
		t.Fatal("script command must carry only the purpose-separated capability in a header")
	}
	previousDB := scriptDB
	scriptDB = func() *gorm.DB { return db }
	defer func() { scriptDB = previousDB }()
	router := gin.New()
	router.GET("/adapter-service/script-command/:shortCode/bootstrap", ScriptBootstrap)
	route := "/adapter-service/script-command/" + item.ShortCode + "/bootstrap"
	call := func(host, capability string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		req.Host = host
		req.Header.Set("X-Forwarded-Proto", "http")
		req.Header.Set(service.ScriptLauncherHeader, capability)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	host := item.ShortCode + ".fixture.test"
	if result := call(host, ""); result.Code != http.StatusNotFound || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("the known Host/path must not mint a ticket: HTTP %d", result.Code)
	}
	if result := call("spoofed.test", match[1]); result.Code != http.StatusNotFound {
		t.Fatalf("the wrong Host must not mint a ticket: HTTP %d", result.Code)
	}
	result := call(host, match[1])
	if result.Code != http.StatusOK || result.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(result.Body.String(), "https://"+host+"/adapter-service/script-command/") ||
		strings.Contains(result.Body.String(), "http://") {
		t.Fatalf("validated launch must use stored HTTPS origin and no-store, HTTP %d", result.Code)
	}
}
