package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/extensionlogs"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// Real first-party token resolution, script lease and file store. No native
// executable is launched and neither an external service nor a real exam is involved.
func TestDiagnosticHTTPThreeModesReadOnlyAuthAndFilteredFiles(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "diagnostic-owner", "user")
	admin := seedControllerUser(t, db, 0, "diagnostic-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "synthetic-identity"}).Error; err != nil {
		t.Fatal(err)
	}
	v, err := service.UploadScriptVersionWithDB(db, admin, "diagnostic-fixture", "fixture.exe", []byte("MZsynthetic"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "log fixture", BaseDomain: "fixture.invalid", VersionID: v.ID})
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := service.IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := service.ExchangeScriptTokenWithDB(db, item.ShortCode, exchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("ab", 32)
	const ip = "127.0.0.1"
	grant, err := service.ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, ticket.ActivationToken, identity, ip, 5)
	if err != nil {
		t.Fatal(err)
	}
	const normal = "diagnostic-normal-example"
	sum := sha256.Sum256([]byte(normal))
	key := entity.AccessToken{UserID: owner.ID, TokenHash: hex.EncodeToString(sum[:]), Status: "enabled", TotalQuotaLimit: 5}
	if err = db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&entity.AccountDeviceBinding{}); err != nil {
		t.Fatal(err)
	}
	previousDB, previousFiles := diagnosticDB, extensionLogFiles
	dir := t.TempDir()
	diagnosticDB = func() *gorm.DB { return db }
	extensionLogFiles = extensionlogs.New(dir)
	t.Cleanup(func() { diagnosticDB, extensionLogFiles = previousDB, previousFiles })
	r := gin.New()
	if err = r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	const base = "/api/extension-logs"
	r.GET(base+"/policy", GetExtensionIngestPolicy)
	r.POST(base+"/start", StartExtensionLogs)
	r.POST(base, UploadExtensionLogs)
	r.GET("/fixture-admin", AdminExtensionLogs) // production admin gate is separately tested
	call := func(method, path, key, identity string, data any) (int, []byte) {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		q := httptest.NewRequest(method, path, bytes.NewReader(raw))
		q.RemoteAddr = ip + ":12345"
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Authorization", "Bearer "+key)
		if identity != "" {
			q.Header.Set("X-Script-Identity", identity)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return w.Code, w.Body.Bytes()
	}
	snapshot := func() (entity.ScriptRunSession, entity.AccessToken, int64, int64) {
		t.Helper()
		var a entity.ScriptRunSession
		var k entity.AccessToken
		var devices, bills int64
		if err := db.Where("script_id = ?", item.ID).First(&a).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&k, key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&entity.AccountDeviceBinding{}).Count(&devices).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
			t.Fatal(err)
		}
		return a, k, devices, bills
	}
	before, k, dev, bills := snapshot()
	cases := []struct {
		mode, browser, key, identity string
		want                         int
	}{
		{"", "", normal, "", 201}, {"native_browser", "native-profile-1", normal, "", 201}, {"native_browser", "native-profile-2", normal, "", 201}, {"native_browser", "native-profile-3", normal, "", 201},
		{"native_exam", "", grant.Token, identity, 201},
		{"native_exam", "", normal, "", 401}, {"native_exam", "", normal, identity, 401}, {"native_exam", "", grant.Token, strings.Repeat("cd", 32), 401},
		{"extension", "", grant.Token, identity, 401}, {"native_browser", "other", grant.Token, identity, 401}, {"", "", normal, identity, 401},
		{"native_browser", "", normal, "", 400}, {"extension", "unexpected", normal, "", 400}, {"native_exam", "unexpected", grant.Token, identity, 400}, {"native_browser", "../x", normal, "", 400}, {"other", "", normal, "", 400},
	}
	for _, c := range cases {
		meta := map[string]any{"device_id": "host-fixture", "launch_id": "11111111-1111-4111-8111-111111111111", "version": "1.2.3"}
		if c.mode != "" {
			meta["mode"] = c.mode
		}
		if c.browser != "" {
			meta["browser_id"] = c.browser
		}
		status, _ := call("POST", base+"/start", c.key, c.identity, meta)
		if status != c.want {
			t.Fatalf("%s browser present=%v: start status %d want %d", c.mode, c.browser != "", status, c.want)
		}
		meta["entries"] = []map[string]any{{"time": time.Now().UTC(), "event": "sync.success", "level": "info"}}
		status, _ = call("POST", base, c.key, c.identity, meta)
		if status != c.want {
			t.Fatalf("%s upload status=%d want=%d", c.mode, status, c.want)
		}
	}
	for _, p := range []struct {
		mode, key, identity string
		want                int
	}{{"", normal, "", 200}, {"native_browser", normal, "", 200}, {"native_exam", grant.Token, identity, 200}, {"native_exam", normal, identity, 401}, {"native_exam", grant.Token, "", 401}, {"", grant.Token, identity, 401}, {"typo", normal, "", 400}} {
		status, raw := call("GET", base+"/policy?mode="+p.mode, p.key, p.identity, nil)
		if status != p.want || (status == 200 && string(raw) != "{\"enabled\":true}") {
			t.Fatalf("policy mode=%s status=%d want=%d", p.mode, status, p.want)
		}
	}
	for _, q := range []struct {
		query, mode, browser string
		count                int
	}{{"mode=extension", "extension", "", 1}, {"mode=native_exam", "native_exam", "", 1}, {"browser_id=native-profile-2", "native_browser", "native-profile-2", 1}, {"mode=native_browser", "native_browser", "", 3}} {
		status, raw := call("GET", "/fixture-admin?"+q.query, "", "", nil)
		var page extensionlogs.Page
		if status != 200 || json.Unmarshal(raw, &page) != nil || len(page.Entries) != q.count {
			t.Fatalf("filter %s failed", q.query)
		}
		for _, e := range page.Entries {
			if e.UserID != owner.ID || e.Mode != q.mode || q.browser != "" && e.BrowserID != q.browser {
				t.Fatal("entry provenance or filters lost")
			}
		}
	}
	after, k2, dev2, bills2 := snapshot()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(k, k2) || dev != dev2 || bills != bills2 {
		t.Fatal("telemetry mutated a script session, token usage, device binding or billing")
	}
	var files []string
	for destination, want := range map[string]int{
		filepath.Join("client", "Browser_extensions"):                                       1,
		filepath.Join("client", "Browser_extensions", "Multi-browser_mode", "host-fixture"): 3,
		filepath.Join("client", "Exam_Client_Injection"):                                    1,
	} {
		matches, err := filepath.Glob(filepath.Join(dir, destination, "*.jsonl"))
		if err != nil || len(matches) != want {
			t.Fatalf("%s: got %d files, want %d (%v)", destination, len(matches), want, err)
		}
		files = append(files, matches...)
	}
	for _, p := range files {
		b, e := os.ReadFile(p)
		if e != nil || bytes.Contains(b, []byte(normal)) || bytes.Contains(b, []byte(identity)) || bytes.Contains(b, []byte(grant.Token)) {
			t.Fatal("authentication material reached the diagnostic store")
		}
	}
}
