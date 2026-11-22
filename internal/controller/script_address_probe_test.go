package controller

import (
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

func TestScriptAddressHeadProbeIsReadOnly(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "probe-owner", "user")
	admin := seedControllerUser(t, db, 0, "probe-admin", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 100, "phone": "13800000000", "wechat_id": "probe-fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	policy := service.ScriptDomainSettings{Domains: []string{"*.probe.test", "shared.probe.test/*"}}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, policy); err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "probe-fixture", "fixture.exe", []byte("MZ"), true)
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{Name: "probe", VersionID: version.ID, DomainFormat: "*.probe.test", LauncherOrigin: &service.ScriptLauncherOrigin{Host: "site.probe.test", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	long, err := service.ConfigureScriptLauncherAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 5, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	short, err := service.ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "shared.probe.test/probe", DurationMinutes: 5, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(ScriptProxyHTTPS([]string{"127.0.0.1"}), scriptHostBoundary(db, func(*gin.Context) bool {
		t.Fatal("HEAD entered the launcher grant handler")
		return false
	}))
	router.HEAD("/api/ordinary", func(c *gin.Context) { c.Status(http.StatusOK) })
	call := func(address, peer, forwardedProto string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodHead, address, nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-Proto", forwardedProto)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	check := func(address string, want int) {
		t.Helper()
		w := call(address, "192.0.2.1:5000", "")
		if w.Code != want || (w.Header().Get("X-Tiku-Script-Address") == "available") != (want == http.StatusNoContent) || w.Header().Get("Location") != "" || (want == http.StatusNoContent && w.Body.Len() != 0) {
			t.Fatalf("HEAD %s: code %d, headers %v, body length %d; want %d", address, w.Code, w.Header(), w.Body.Len(), want)
		}
	}
	for range 2 {
		check(long.Address, http.StatusNoContent)
		check(short.Address, http.StatusNoContent)
	}
	check("https://shared.probe.test/not-probe", http.StatusNotFound)
	check(short.Address+"?unused=1", http.StatusNotFound)
	check(short.Address+"/", http.StatusNotFound)
	check("https://shared.probe.test/%70robe", http.StatusNotFound)
	check("https://shared.probe.test/api/ordinary", http.StatusOK)
	check("https://other.probe.test/probe", http.StatusNotFound)
	httpLong := strings.Replace(long.Address, "https:", "http:", 1)
	if w := call(httpLong, "192.0.2.1:5000", "https"); w.Code != http.StatusNotFound {
		t.Fatalf("untrusted proxy scheme accepted: %d", w.Code)
	}
	if w := call(httpLong, "127.0.0.1:5000", "https"); w.Code != http.StatusNoContent || w.Header().Get("X-Tiku-Script-Address") != "available" {
		t.Fatalf("trusted proxy scheme ignored: %d", w.Code)
	}
	var stored entity.UserScript
	if err := db.First(&stored, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LauncherUsedCount != 0 || stored.ShortEntryUsedCount != 0 || stored.ShortEntryUsedAt != nil {
		t.Fatalf("HEAD consumed authorization: long=%d short=%d", stored.LauncherUsedCount, stored.ShortEntryUsedCount)
	}
	for _, model := range []any{&entity.ScriptExchangeToken{}, &entity.ScriptRunSession{}} {
		var count int64
		if err := db.Model(model).Where("script_id = ?", item.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("HEAD created %T: count %d err %v", model, count, err)
		}
	}
	if err := db.Model(&stored).Update("short_entry_used_count", 1).Error; err != nil {
		t.Fatal(err)
	}
	check(short.Address, http.StatusNotFound)
	if err := db.Model(&stored).Update("short_entry_used_count", 0).Error; err != nil {
		t.Fatal(err)
	}
	// A real pending exchange or active lease makes the other address
	// unavailable without the probe claiming or changing either record.
	longURL, err := url.Parse(long.Address)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.IssuePublicScriptExchangeTokenByLocatorWithDB(db, longURL.Host, longURL.Path, 0)
	if err != nil {
		t.Fatal(err)
	}
	check(short.Address, http.StatusNotFound)
	exchanged, err := service.ExchangeScriptTokenWithDB(db, item.ShortCode, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	check(short.Address, http.StatusNoContent)
	identity := strings.Repeat("1", 64)
	lease, err := service.ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, exchanged.ActivationToken, identity, "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	check(short.Address, http.StatusNotFound)
	if _, _, err := service.ResolveScriptRunSessionWithDB(db, lease.Token, identity, "127.0.0.1"); err != nil {
		t.Fatalf("HEAD invalidated an existing lease: %v", err)
	}
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).Update("last_heartbeat_at", time.Now().Add(-service.ScriptHeartbeatTimeout-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	check(short.Address, http.StatusNoContent)
	policy.DeniedDomains = []string{"shared.probe.test"}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, policy); err != nil {
		t.Fatal(err)
	}
	check(short.Address, http.StatusNotFound)
	if err := db.Model(&stored).Updates(map[string]any{"launcher_expires_at": time.Now().Add(-time.Minute), "launcher_used_count": 0}).Error; err != nil {
		t.Fatal(err)
	}
	check(long.Address, http.StatusNotFound)
}
