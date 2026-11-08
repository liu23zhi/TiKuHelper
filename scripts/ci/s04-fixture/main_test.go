package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func fixturePath(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".s04-fixture"), []byte(fixtureMarker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "data", "fixture.db")
}
func TestFixturePathFailsClosed(t *testing.T) {
	p := fixturePath(t)
	if _, err := validateFixturePath(p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative/fixture.db", filepath.Join(filepath.Dir(p), "tiku.db"), filepath.Join(t.TempDir(), "data", "fixture.db")} {
		if _, err := validateFixturePath(bad); err == nil {
			t.Fatalf("accepted unsafe path %q", bad)
		}
	}
	outside := t.TempDir()
	os.Remove(filepath.Dir(p))
	if err := os.Symlink(outside, filepath.Dir(p)); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFixturePath(p); err == nil {
		t.Fatal("accepted symlink data directory")
	}
}
func TestSeedIsExclusiveAndVerifierCannotUpgradeLegacyCredential(t *testing.T) {
	p := fixturePath(t)
	if _, err := seedLegacy(p); err != nil {
		t.Fatal(err)
	}
	if _, err := seedLegacy(p); err == nil {
		t.Fatal("seed overwrote existing file")
	}
	if _, err := verifyDatabase(p, "legacy"); err == nil {
		t.Fatal("verifier accepted plaintext without real main upgrade")
	}
	db, closeDB, err := openFixtureDB(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	var row entity.User
	if err = db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Password != legacyPassword || row.Balance != legacyBalance {
		t.Fatal("verification modified synthetic legacy row")
	}
	hash, err := util.HashPassword(legacyPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&row).Update("password", hash).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	report, err := verifyDatabase(p, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if !report.Bcrypt || !report.ExpectedPasswordMatches || report.EnvironmentPasswordMatches || string(raw) == "" {
		t.Fatalf("invalid safe report %s", raw)
	}
	var count int64
	if err = db.Model(&entity.SystemSetting{}).Where("key LIKE ?", "_s04_fixture_probe_%").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("probe leaked writes: %d %v", count, err)
	}
}
func TestHealthUsesActualHTTPAndRejectsWrongStatusBody(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"actual", 200, `{"status":"ok"}`, false},
		{"server-error", 503, `{"status":"ok"}`, true},
		{"body-error", 200, `{"status":"not_ready"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receivedHeaders := make(chan [2]string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedHeaders <- [2]string{r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-IP")}
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			observed, err := probeEndpoint(server.URL, "/healthz", "ok")
			select {
			case received := <-receivedHeaders:
				if received != [2]string{"203.0.113.7", "198.51.100.9"} {
					t.Fatalf("actual incoming forwarding headers = %q", received)
				}
			default:
				t.Fatal("health fixture did not observe an HTTP request")
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("status=%d error=%v", observed.Status, err)
			}
			if observed.Status != tc.status || observed.Body != tc.body {
				t.Fatal("not literal HTTP observation")
			}
		})
	}
}

func TestOwnerPreflightRejectsArbitraryIDsLinksAndUnexpectedPaths(t *testing.T) {
	p := fixturePath(t)
	root := filepath.Dir(filepath.Dir(p))
	if err := os.WriteFile(filepath.Join(root, ".s04-host-owner.json"), []byte(`{"uid":1001,"gid":1001}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "logs", "Tiku.txt"), []byte("synthetic literal log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "logs", "archive"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "logs", "archive", "20261029_000000.zip"), []byte("synthetic archived fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	entries, err := ownershipEntries(p, 1000, 1000, root)
	if err != nil || len(entries) != 8 {
		t.Fatalf("valid ownership preflight: %d %v", len(entries), err)
	}
	if _, err = ownershipEntries(p, 2222, 2222, root); err == nil {
		t.Fatal("arbitrary owner accepted")
	}
	if _, err = ownershipEntries(p, 1000, 1000, "/fixture"); err == nil {
		t.Fatal("nonfixed root accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "logs", "Tiku01.txt")
	if err = os.Link(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err = ownershipEntries(p, 1000, 1000, root); err == nil {
		t.Fatal("hardlinked regular file accepted")
	}
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err = ownershipEntries(p, 1000, 1000, root); err == nil {
		t.Fatal("symlink accepted")
	}
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "unexpected"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ownershipEntries(p, 1000, 1000, root); err == nil {
		t.Fatal("unexpected fixture path accepted")
	}
}
