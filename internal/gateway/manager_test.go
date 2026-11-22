package gateway

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagerSnapshotsSurviveCorruptSourcesRollbackAndExpire(t *testing.T) {
	dir := t.TempDir()
	source := t.TempDir()
	conf := t.TempDir()
	now := time.Now()
	writeFixture(t, conf, "base.conf", []byte("# initial\n"))
	_, cert, key := fixtureCertificate(t, []string{"one.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, source, "cert", cert)
	writeFixture(t, source, "key", key)
	checks, reloads := 0, 0
	reject := false
	reloadFail := false
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: conf, Check: func(path string) error {
		checks++
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !strings.Contains(string(data), filepath.ToSlash(dir)) {
			t.Fatal("candidate does not reference private directory")
		}
		if reject {
			return errors.New("invalid config")
		}
		return nil
	}, Reload: func() error {
		reloads++
		if reloadFail {
			return errors.New("reload failed")
		}
		return nil
	}}
	if changed, e := manager.Refresh(now); e != nil || !changed {
		t.Fatalf("initial: %v %v", changed, e)
	}
	first, _ := os.ReadFile(manager.ActiveConfig())
	writeFixture(t, source, "key", []byte("broken"))
	writeFixture(t, conf, "base.conf", []byte("# second\n"))
	if changed, e := manager.Refresh(now); e != nil || !changed {
		t.Fatalf("corrupt source should retain snapshots while config reloads: %v %v", changed, e)
	}
	if !manager.TLSReady() {
		t.Fatal("lost valid snapshot")
	}
	for _, p := range manager.active {
		info, e := os.Stat(filepath.Join(manager.activeDir, "cert-"+p.ID+".key"))
		if e != nil {
			t.Fatal(e)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("private key permissions must be 0600")
		}
	}
	before, _ := os.ReadFile(manager.ActiveConfig())
	reject = true
	writeFixture(t, conf, "base.conf", []byte("invalid config"))
	if _, e := manager.Refresh(now); e == nil {
		t.Fatal("expected nginx -t rejection")
	}
	after, _ := os.ReadFile(manager.ActiveConfig())
	if string(before) != string(after) {
		t.Fatal("check failure replaced active config")
	}
	reject = false
	reloadFail = true
	beforeFailure, _ := filepath.Glob(filepath.Join(dir, "generation-*"))
	if _, e := manager.Refresh(now); e == nil {
		t.Fatal("expected reload rejection")
	}
	after, _ = os.ReadFile(manager.ActiveConfig())
	if string(before) != string(after) {
		t.Fatal("reload failure replaced active config")
	}
	afterFailure, _ := filepath.Glob(filepath.Join(dir, "generation-*"))
	if len(afterFailure) <= len(beforeFailure) {
		t.Fatal("uncertain reload/rollback deleted candidate certificate files that a master may still reference")
	}
	reloadFail = false
	writeFixture(t, conf, "base.conf", []byte("# good again\n"))
	if _, e := manager.Refresh(now.Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	if manager.TLSReady() {
		t.Fatal("expired snapshots still served")
	}
	if checks < 4 || reloads < 2 || len(first) == 0 {
		t.Fatalf("checks %d reloads %d", checks, reloads)
	}
}

func TestManagerExpiresSnapshotWhenIntermediateExpiresFirst(t *testing.T) {
	now := time.Now()
	dir, source, conf := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, conf, "base.conf", []byte("# fixture"))
	_, leaf, key := fixtureCertificate(t, []string{"leaf.example"}, now.Add(-time.Hour), now.Add(2*time.Hour), false)
	_, chain, _ := fixtureCertificate(t, []string{"issuer.example"}, now.Add(-2*time.Hour), now.Add(time.Minute), false)
	writeFixture(t, source, "leaf", append(leaf, chain...))
	writeFixture(t, source, "key", key)
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: conf, Check: func(string) error { return nil }, Reload: func() error { return nil }}
	if _, e := manager.Refresh(now); e != nil {
		t.Fatal(e)
	}
	if !manager.TLSReady() {
		t.Fatal("valid chain rejected")
	}
	writeFixture(t, source, "key", []byte("corrupt source"))
	if _, e := manager.Refresh(now.Add(2 * time.Minute)); e != nil {
		t.Fatal(e)
	}
	if manager.TLSReady() {
		t.Fatal("expired intermediate kept TLS enabled until leaf expiry")
	}
}

func TestManagerRestoresPrivateSnapshotsAfterRestartDuringIncompleteRenewal(t *testing.T) {
	now := time.Now()
	dir, source, conf := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, conf, "base.conf", []byte("# fixture"))
	_, leaf, key := fixtureCertificate(t, []string{"retained.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, source, "fullchain.pem", leaf)
	writeFixture(t, source, "privkey.key", key)
	first := &Manager{Root: dir, CertificateDir: source, ConfigDir: conf, Check: func(string) error { return nil }}
	if _, e := first.Refresh(now); e != nil {
		t.Fatal(e)
	}
	id := first.active[0].ID
	writeFixture(t, source, "privkey.key", []byte("incomplete renewal"))
	// A previous interrupted candidate is owned by the gateway and can be collected.
	if e := os.Mkdir(filepath.Join(dir, "generation-123456"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(dir, "operator-owned"), 0700); e != nil {
		t.Fatal(e)
	}
	restarted := &Manager{Root: dir, CertificateDir: source, ConfigDir: conf, Check: func(string) error { return nil }}
	if _, e := restarted.Refresh(now); e != nil {
		t.Fatal(e)
	}
	if !restarted.TLSReady() || len(restarted.active) != 1 || restarted.active[0].ID != id {
		t.Fatal("restart discarded previously validated certificate snapshot")
	}
	if restarted.active[0].CertPath != "fullchain.pem" {
		t.Fatal("restart lost conventional default priority")
	}
	generations, _ := filepath.Glob(filepath.Join(dir, "generation-*"))
	if len(generations) != 2 {
		t.Fatalf("want active + rollback generations, got %d", len(generations))
	}
	if _, e := os.Stat(filepath.Join(dir, "operator-owned")); e != nil {
		t.Fatal("cleanup removed unrelated runtime directory")
	}
}

func TestCurrentTLSStatusChecksActualTimeAfterFailedExpiryRefresh(t *testing.T) {
	now := time.Now()
	dir, source, conf := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, conf, "base.conf", []byte("# fixture"))
	_, leaf, key := fixtureCertificate(t, []string{"expiry.example"}, now.Add(-time.Hour), now.Add(time.Minute), false)
	writeFixture(t, source, "leaf", append(leaf, key...))
	reject := false
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: conf, Check: func(string) error {
		if reject {
			return errors.New("invalid mounted config")
		}
		return nil
	}}
	if _, e := manager.Refresh(now); e != nil {
		t.Fatal(e)
	}
	reject = true
	writeFixture(t, conf, "base.conf", []byte("invalid;"))
	if _, e := manager.Refresh(now.Add(2 * time.Minute)); e == nil {
		t.Fatal("expected candidate rejection")
	}
	status, e := CurrentTLSStatus(dir, now.Add(2*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if status.Ready || status.Active != 1 || status.Valid != 0 {
		t.Fatalf("expired TLS advertised as ready: %+v", status)
	}
	if !strings.Contains(status.Message(), "HTTPS 未就绪") {
		t.Fatalf("missing TLS unready signal: %s", status.Message())
	}
}

func TestManagerReloadsWhenDefaultPriorityChangesWithoutNewCertificateBytes(t *testing.T) {
	now := time.Now()
	dir, source, conf := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, conf, "base.conf", []byte("# fixture"))
	_, a, ak := fixtureCertificate(t, []string{"a.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	_, b, bk := fixtureCertificate(t, []string{"b.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, source, "a", a)
	writeFixture(t, source, "a-key", ak)
	writeFixture(t, source, "b", b)
	writeFixture(t, source, "b-key", bk)
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: conf, Check: func(string) error { return nil }, Reload: func() error { return nil }}
	if _, e := manager.Refresh(now); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(filepath.Join(source, "b"), filepath.Join(source, "fullchain.pem")); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(filepath.Join(source, "b-key"), filepath.Join(source, "privkey.key")); e != nil {
		t.Fatal(e)
	}
	if changed, e := manager.Refresh(now); e != nil || !changed {
		t.Fatalf("default selection change must reload: %v %v", changed, e)
	}
}

func TestManagerGeneratesEmbeddedBaseWithoutExternalConfigDirectory(t *testing.T) {
	dir, source := t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "nginx-extra")
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: missing, Check: func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), filepath.ToSlash(dir)) {
			t.Fatalf("generated config must use the private runtime directory: %s", path)
		}
		return nil
	}}
	if changed, err := manager.Refresh(time.Now()); err != nil || !changed {
		t.Fatalf("missing external directory must still generate a config: changed=%v err=%v", changed, err)
	}
	active, err := os.ReadFile(manager.ActiveConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), "generation-") {
		t.Fatalf("active config must point at a generated private configuration: %s", active)
	}
	for _, name := range []string{"tiku-adapter.conf", "tiku-proxy.inc"} {
		if _, err := os.Stat(filepath.Join(manager.activeDir, "conf.d", name)); err != nil {
			t.Fatalf("embedded template %s was not materialized: %v", name, err)
		}
	}
}

func TestManagerEnablesHTTPSOnlyAfterFindingValidCertificatePair(t *testing.T) {
	now := time.Now()
	_, cert, key := fixtureCertificate(t, []string{"gateway.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	_, _, unrelatedKey := fixtureCertificate(t, []string{"other.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	for _, tc := range []struct {
		name  string
		files map[string][]byte // A nil map leaves the source directory absent.
	}{
		{name: "missing directory"},
		{name: "empty directory", files: map[string][]byte{}},
		{name: "invalid certificate", files: map[string][]byte{"fullchain.pem": []byte("invalid certificate"), "privkey.key": key}},
		{name: "certificate without key", files: map[string][]byte{"fullchain.pem": cert}},
		{name: "key without certificate", files: map[string][]byte{"privkey.key": key}},
		{name: "mismatched key", files: map[string][]byte{"fullchain.pem": cert, "privkey.key": unrelatedKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "ssl")
			if tc.files != nil {
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				for name, data := range tc.files {
					writeFixture(t, source, name, data)
				}
			}
			manager := &Manager{Root: t.TempDir(), CertificateDir: source, Check: func(string) error { return nil }}
			refreshAndCheckListeners := func(wantTLS bool) {
				t.Helper()
				if changed, err := manager.Refresh(now); err != nil || !changed {
					t.Fatalf("refresh with TLS=%v: changed=%v err=%v", wantTLS, changed, err)
				}
				data, err := os.ReadFile(filepath.Join(manager.activeDir, "servers.conf"))
				if err != nil {
					t.Fatal(err)
				}
				servers := string(data)
				for _, listener := range []string{"listen 8060 default_server;", "listen 8680 default_server;"} {
					if !strings.Contains(servers, listener) {
						t.Errorf("missing HTTP listener %q", listener)
					}
				}
				if got := strings.Contains(servers, "listen 9043 ssl"); got != wantTLS {
					t.Errorf("HTTPS listener present = %v, want %v", got, wantTLS)
				}
				if manager.TLSReady() != wantTLS {
					t.Errorf("TLSReady = %v, want %v", manager.TLSReady(), wantTLS)
				}
			}

			refreshAndCheckListeners(false)
			writeFixture(t, source, "fullchain.pem", cert)
			writeFixture(t, source, "privkey.key", key)
			refreshAndCheckListeners(true)
		})
	}
}

func TestAllGatewayListenersForwardQRCodeAndOriginalHost(t *testing.T) {
	p := Certificate{ID: "test"}
	for _, servers := range []string{renderServers("/tmp/test", "/tmp/test/conf", Certificate{}, nil), renderServers("/tmp/test", "/tmp/test/conf", p, map[string]Certificate{"normal.example": p})} {
		if strings.Contains(servers, "alias ") || strings.Contains(servers, "/api/Wechat_qc_code") ||
			strings.Count(servers, "proxy_pass http://tiku_adapter;") != strings.Count(servers, "server {") {
			t.Fatalf("all requests must reach backend host routing: %s", servers)
		}
	}
	include, err := embeddedTemplate("tiku-proxy.inc")
	if err != nil || !regexp.MustCompile(`(?m)^proxy_set_header\s+Host\s+\$http_host;`).Match(include) {
		t.Fatalf("forward original Host: %v %s", err, include)
	}
}

func TestManagerMergesValidExternalServer(t *testing.T) {
	dir, source, extra := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, extra, "custom.conf", []byte("server { listen 127.0.0.1:18080; server_name extra.example; return 204; }\n"))
	writeFixture(t, extra, "headers.inc", []byte("add_header X-External-Config yes;\n"))
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: extra, Check: func(string) error { return nil }}
	if _, err := manager.Refresh(time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(manager.activeDir, "conf.d", "extra", "custom.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "extra.example") {
		t.Fatalf("valid external server was not merged: %s", data)
	}
	if _, err := os.Stat(filepath.Join(manager.activeDir, "conf.d", "extra", "headers.inc")); err != nil {
		t.Fatalf("valid external include was not merged: %v", err)
	}
	active, err := os.ReadFile(filepath.Join(manager.activeDir, "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), "extra/*.inc") {
		t.Fatalf("generated config must include optional snippets: %s", active)
	}
}

func TestManagerRejectsReservedExternalDirectivesWithoutBreakingBase(t *testing.T) {
	reserved := []struct {
		name string
		body string
	}{
		{"upstream", "upstream tiku_adapter { server 127.0.0.1:19001; }\n"},
		{"upgrade-map", "map $http_upgrade $custom_upgrade { default upgrade; }\n"},
		{"qr-location", "server { location = /api/Wechat_qc_code { return 418; } }\n"},
		{"default-listener", "server { listen 8680 default_server; }\n"},
	}
	for _, testCase := range reserved {
		t.Run(testCase.name, func(t *testing.T) {
			dir, source, extra := t.TempDir(), t.TempDir(), t.TempDir()
			writeFixture(t, extra, "reserved.conf", []byte(testCase.body))
			manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: extra, Check: func(string) error { return nil }}
			if _, err := manager.Refresh(time.Now()); err != nil {
				t.Fatal(err)
			}
			active, err := os.ReadFile(manager.ActiveConfig())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(active), testCase.body) {
				t.Fatalf("reserved external directive was included: %s", active)
			}
			if _, err := os.Stat(filepath.Join(manager.activeDir, "conf.d", "tiku-adapter.conf")); err != nil {
				t.Fatalf("base template missing after rejecting %s: %v", testCase.name, err)
			}
		})
	}
}

func TestManagerSkipsInvalidExternalSyntaxAndKeepsValidExtra(t *testing.T) {
	dir, source, extra := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, extra, "invalid.conf", []byte("invalid_nginx_directive;\n"))
	writeFixture(t, extra, "invalid.inc", []byte("invalid_nginx_include;\n"))
	writeFixture(t, extra, "valid.conf", []byte("# valid optional snippet\n"))
	check := func(path string) error {
		root := filepath.Dir(path)
		var invalid bool
		_ = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
			if err == nil && entry != nil && !entry.IsDir() {
				data, readErr := os.ReadFile(current)
				if readErr == nil && (strings.Contains(string(data), "invalid_nginx_directive") || strings.Contains(string(data), "invalid_nginx_include")) {
					invalid = true
				}
			}
			return nil
		})
		if invalid {
			return errors.New("synthetic nginx syntax error")
		}
		return nil
	}
	manager := &Manager{Root: dir, CertificateDir: source, ConfigDir: extra, Check: check}
	if _, err := manager.Refresh(time.Now()); err != nil {
		t.Fatalf("invalid optional file must not abort startup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.activeDir, "conf.d", "extra", "invalid.conf")); !os.IsNotExist(err) {
		t.Fatalf("invalid external file should be skipped, stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.activeDir, "conf.d", "extra", "invalid.inc")); !os.IsNotExist(err) {
		t.Fatalf("invalid external include should be skipped, stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.activeDir, "conf.d", "extra", "valid.conf")); err != nil {
		t.Fatalf("valid external file should remain active: %v", err)
	}
}

func TestReservedExternalConfigRejectsGeneratedIncludePaths(t *testing.T) {
	for _, body := range []string{
		`include /etc/nginx/conf.d/tiku-adapter.conf;`,
		`include /etc/nginx/conf.d/*.conf;`,
	} {
		if reason := reservedExternalConfig([]byte(body)); reason == "" {
			t.Fatalf("external include path must be rejected: %s", body)
		}
	}
}
