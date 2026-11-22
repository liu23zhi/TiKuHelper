package gateway

import (
	"errors"
	"os"
	"path/filepath"
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
