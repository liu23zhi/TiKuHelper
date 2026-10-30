package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareExtensionVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"6.2.0", "6.2.0.0", 0},
		{"6.2.1", "6.2.0", 1},
		{"6.1.9", "6.2.0", -1},
		{"", "6.2.0", -2},
		{"1.01", "1.1", -2},
		{"+1.2", "1.2", -2},
		{"0.0.0", "0", -2},
		{"65536", "1", -2},
	}
	for _, test := range tests {
		if got := compareExtensionVersions(test.left, test.right); got != test.want {
			t.Errorf("compareExtensionVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestExtensionURLAllowlist(t *testing.T) {
	clearExtensionSettingsEnvironment(t)
	t.Setenv("EXTENSION_EDGE_ID", DefaultEdgeExtensionID)
	t.Setenv("EXTENSION_UPDATE_INTERVAL", "6h")
	t.Setenv("EXTENSION_UPDATE_ENABLED", "true")
	config, err := ExtensionStoreConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !validExtensionID(config.IDs["edge"]) {
		t.Fatalf("default Edge extension ID is invalid: %q", config.IDs["edge"])
	}
	for _, target := range []string{
		"https://edge.microsoft.com/extensionwebstorebase/v1/crx",
		"https://msedgeextensions.b.tlu.dl.delivery.mp.microsoft.com/filestreamingservice/files/pkg",
		"http://msedgeextensions.b.tlu.dl.delivery.mp.microsoft.com/filestreamingservice/files/pkg",
	} {
		if !allowedExtensionURL(mustParseExtensionURL(t, target), "edge") {
			t.Errorf("expected allowlisted URL: %s", target)
		}
	}
	for _, target := range []string{
		"http://edge.microsoft.com/pkg",
		"https://example.invalid/pkg",
		"https://edge.microsoft.com.evil.invalid/pkg",
		"https://edge.microsoft.com:8443/pkg",
		"https://user:secret@edge.microsoft.com/pkg",
		"https://edge.microsoft.com/pkg#fragment",
		"file:///package.crx",
		"https://127.0.0.1/pkg",
	} {
		if allowedExtensionURL(mustParseExtensionURL(t, target), "edge") {
			t.Errorf("expected rejected URL: %s", target)
		}
	}
}

func TestExtensionIntervalEnvironmentAcceptsLongDuration(t *testing.T) {
	clearExtensionSettingsEnvironment(t)
	t.Setenv("EXTENSION_CACHE_DIR", t.TempDir())
	t.Setenv("EXTENSION_UPDATE_INTERVAL", "241h")
	config, err := ExtensionStoreConfigFromEnv()
	if err != nil || config.Interval != 241*time.Hour {
		t.Fatalf("long interval: %v %v", config.Interval, err)
	}
	t.Setenv("EXTENSION_UPDATE_INTERVAL", "153722868m")
	if _, err := ExtensionStoreConfigFromEnv(); err == nil {
		t.Fatal("duration overflow was accepted")
	}
}

type extensionFixture struct {
	id  string
	key *rsa.PrivateKey
}

func newExtensionFixture(t *testing.T) extensionFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(publicKey)
	return extensionFixture{id: extensionID(hash[:16]), key: key}
}

func crxProtoField(number uint64, data []byte) []byte {
	prefix := binary.AppendUvarint(nil, number<<3|2)
	prefix = binary.AppendUvarint(prefix, uint64(len(data)))
	return append(prefix, data...)
}

func (f extensionFixture) build(t *testing.T, version string, extra map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	files := map[string]string{"manifest.json": fmt.Sprintf(`{"name":"test extension","manifest_version":3,"version":%q}`, version), "popup.html": "<html>extension</html>"}
	for name, data := range extra {
		files[name] = data
	}
	for name, data := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keyHash := sha256.Sum256(publicKey)
	signed := crxProtoField(1, keyHash[:16])
	payload := append([]byte("CRX3 SignedData\x00"), binary.LittleEndian.AppendUint32(nil, uint32(len(signed)))...)
	payload = append(payload, signed...)
	payload = append(payload, archive.Bytes()...)
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	proof := append(crxProtoField(1, publicKey), crxProtoField(2, signature)...)
	header := append(crxProtoField(2, proof), crxProtoField(10000, signed)...)
	crx := append([]byte("Cr24"), binary.LittleEndian.AppendUint32(nil, 3)...)
	crx = binary.LittleEndian.AppendUint32(crx, uint32(len(header)))
	crx = append(crx, header...)
	return append(crx, archive.Bytes()...)
}

func TestValidateExtensionCRXGeneratedAndTampered(t *testing.T) {
	fixture := newExtensionFixture(t)
	valid := fixture.build(t, "6.2.0", nil)
	manifest, err := validateExtensionCRX(valid, fixture.id)
	if err != nil || manifest.Version != "6.2.0" {
		t.Fatalf("valid signed package: %+v, %v", manifest, err)
	}
	tampered := bytes.Clone(valid)
	tampered[len(tampered)-1] ^= 1
	cases := []struct {
		name string
		data []byte
		id   string
	}{
		{"signature", tampered, fixture.id},
		{"identity", valid, DefaultEdgeExtensionID},
		{"short", []byte("Cr24"), fixture.id},
		{"oversizedHeader", append([]byte("Cr24\x03\x00\x00\x00"), bytes.Repeat([]byte{0xff}, 16)...), fixture.id},
		{"traversal", fixture.build(t, "6.2.0", map[string]string{"../outside.js": "bad"}), fixture.id},
		{"windowsPath", fixture.build(t, "6.2.0", map[string]string{`C:\outside.js`: "bad"}), fixture.id},
		{"manifestVersion", fixture.build(t, "6.2.0", map[string]string{"manifest.json": `{"name":"bad","manifest_version":2,"version":"6.2.0"}`}), fixture.id},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateExtensionCRX(test.data, test.id); err == nil {
				t.Fatal("invalid package was accepted")
			}
		})
	}
}

type extensionHTTPFixture struct {
	mu                                         sync.Mutex
	id, version, status, appID, codebase, hash string
	data                                       []byte
	code                                       int
	checks, downloads                          int
	gate                                       <-chan struct{}
	entered                                    chan struct{}
	seenOnce                                   sync.Once
}

func (upstream *extensionHTTPFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upstream.mu.Lock()
	if r.URL.Path == "/package" {
		upstream.downloads++
		data := bytes.Clone(upstream.data)
		upstream.mu.Unlock()
		_, _ = w.Write(data)
		return
	}
	upstream.checks++
	id, version, status, appID, codebase, hash, code, gate := upstream.id, upstream.version, upstream.status, upstream.appID, upstream.codebase, upstream.hash, upstream.code, upstream.gate
	upstream.mu.Unlock()
	upstream.seenOnce.Do(func() {
		if upstream.entered != nil {
			close(upstream.entered)
		}
	})
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	if code != 0 {
		w.WriteHeader(code)
		return
	}
	if r.URL.Query().Get("response") != "updatecheck" || !strings.HasPrefix(r.URL.Query().Get("x"), "id="+id+"&v=") {
		w.WriteHeader(400)
		return
	}
	if appID == "" {
		appID = id
	}
	if status == "" {
		status = "ok"
	}
	if codebase == "" {
		codebase = "http://msedgeextensions.b.tlu.dl.delivery.mp.microsoft.com/package"
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = fmt.Fprintf(w, `<gupdate><app appid="%s" status="ok"><updatecheck status="%s" version="%s" codebase="%s" hash_sha256="%s"/></app></gupdate>`, appID, status, version, xmlEscape(codebase), hash)
}

func xmlEscape(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}

type extensionRoundTripper func(*http.Request) (*http.Response, error)

func (f extensionRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newExtensionHTTPStore(t *testing.T, upstream *extensionHTTPFixture, directory string) *ExtensionStore {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	store, err := NewExtensionStore(ExtensionStoreConfig{Directory: directory, Interval: time.Hour, AutoUpdate: true, IDs: map[string]string{"edge": upstream.id}})
	if err != nil {
		t.Fatal(err)
	}
	local, _ := url.Parse(server.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = local.Scheme, local.Host
		res, err := transport.RoundTrip(copy)
		if res != nil {
			res.Request = r
		}
		return res, err
	})
	return store
}

func TestExtensionUpdateLifecycleAndRestart(t *testing.T) {
	fixture := newExtensionFixture(t)
	first := fixture.build(t, "6.2.0", nil)
	upstream := &extensionHTTPFixture{id: fixture.id, version: "6.2.0.0", data: first, hash: extensionSHA256(first)}
	directory := t.TempDir()
	store := newExtensionHTTPStore(t, upstream, directory)
	if _, err := store.Latest("edge"); !errors.Is(err, ErrExtensionUnavailable) {
		t.Fatalf("empty cache: %v", err)
	}
	if _, err := store.Latest("chrome"); !errors.Is(err, ErrExtensionSourceNotConfigured) {
		t.Fatalf("unconfigured Chrome: %v", err)
	}
	if err := store.Refresh(context.Background(), "chrome"); !errors.Is(err, ErrExtensionSourceNotConfigured) {
		t.Fatal(err)
	}
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Latest("edge")
	if err != nil || initial.ExtensionID != fixture.id || initial.Version != "6.2.0" || initial.Size != int64(len(first)) || initial.SHA256 != extensionSHA256(first) || !strings.Contains(initial.DownloadURL, "?sha256="+initial.SHA256) {
		t.Fatalf("initial release: %+v %v", initial, err)
	}
	_, downloaded, err := store.Download("edge", initial.SHA256)
	if err != nil || !bytes.Equal(first, downloaded) {
		t.Fatalf("download mismatch: %v", err)
	}
	downloaded[0] ^= 1
	_, downloaded, _ = store.Download("edge", initial.SHA256)
	if !bytes.Equal(first, downloaded) {
		t.Fatal("consumer mutated cached package")
	}
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	upstream.mu.Lock()
	upstream.status = "noupdate"
	upstream.mu.Unlock()
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := store.Latest("edge")
	if !unchanged.UpdatedAt.Equal(initial.UpdatedAt) || unchanged.CheckedAt.Before(initial.CheckedAt) {
		t.Fatalf("no-update timestamps: %+v", unchanged)
	}
	upstream.mu.Lock()
	if upstream.downloads != 1 {
		t.Errorf("unchanged package downloaded %d times", upstream.downloads)
	}
	second := fixture.build(t, "6.3.0", nil)
	upstream.status, upstream.version, upstream.data, upstream.hash = "ok", "6.3.0", second, extensionSHA256(second)
	upstream.mu.Unlock()
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	release, _ := store.Latest("edge")
	if release.Version != "6.3.0" {
		t.Fatalf("not updated: %+v", release)
	}
	if _, _, err := store.Download("edge", initial.SHA256); !errors.Is(err, ErrExtensionChanged) {
		t.Fatalf("stale package requested: %v", err)
	}
	restarted, err := NewExtensionStore(store.config)
	if err != nil {
		t.Fatal(err)
	}
	loaded, data, err := restarted.Download("edge", release.SHA256)
	loadedJSON, _ := json.Marshal(loaded)
	releaseJSON, _ := json.Marshal(release)
	if err != nil || !bytes.Equal(loadedJSON, releaseJSON) || !bytes.Equal(data, second) {
		t.Fatalf("restart lost cache: %+v, %v", loaded, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "edge-"+release.SHA256+".crx"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, _ = NewExtensionStore(store.config)
	if _, err := restarted.Latest("edge"); !errors.Is(err, ErrExtensionUnavailable) {
		t.Fatalf("corrupt persisted cache was public: %v", err)
	}
}

func TestExtensionUpdateFailuresPreserveLastGood(t *testing.T) {
	fixture := newExtensionFixture(t)
	first := fixture.build(t, "1.0", nil)
	second := fixture.build(t, "2.0", nil)
	brokenSignature := bytes.Clone(second)
	brokenSignature[len(brokenSignature)-1] ^= 1
	other := newExtensionFixture(t)
	otherData := other.build(t, "2.0", nil)
	upstream := &extensionHTTPFixture{id: fixture.id, version: "1.0", data: first, hash: extensionSHA256(first)}
	store := newExtensionHTTPStore(t, upstream, t.TempDir())
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	good, _ := store.Latest("edge")
	for _, failure := range []string{"http", "identity", "hash", "missing-hash", "signature", "package-id", "version", "downgrade", "private-url", "redirect", "filesystem"} {
		t.Run(failure, func(t *testing.T) {
			upstream.mu.Lock()
			upstream.appID, upstream.code, upstream.codebase, upstream.status = "", 0, "", "ok"
			upstream.version, upstream.data, upstream.hash = "2.0", second, extensionSHA256(second)
			switch failure {
			case "http":
				upstream.code = 503
			case "identity":
				upstream.appID = other.id
			case "hash":
				upstream.hash = strings.Repeat("0", 64)
			case "missing-hash":
				upstream.hash = ""
			case "signature":
				upstream.data = brokenSignature
				upstream.hash = extensionSHA256(brokenSignature)
			case "package-id":
				upstream.data = otherData
				upstream.hash = extensionSHA256(otherData)
			case "version":
				upstream.version = "2.1"
			case "downgrade":
				upstream.version = "0.9"
			case "private-url":
				upstream.codebase = "http://127.0.0.1/private"
			case "redirect":
				upstream.codebase = "https://edge.microsoft.com.evil.invalid/package"
			case "filesystem":
				if err := os.Remove(filepath.Join(store.config.Directory, "edge-latest.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(store.config.Directory, "edge-latest.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			upstream.mu.Unlock()
			if err := store.Refresh(context.Background(), "edge"); err == nil {
				t.Fatal("invalid update accepted")
			}
			release, data, err := store.Download("edge", good.SHA256)
			if err != nil || release != good || !bytes.Equal(first, data) {
				t.Fatalf("failed update replaced good cache: %+v %v", release, err)
			}
		})
	}
}

func TestExtensionRedirectIsRejectedBeforeContact(t *testing.T) {
	store, err := NewExtensionStore(ExtensionStoreConfig{Directory: t.TempDir(), Interval: time.Hour, IDs: map[string]string{"edge": DefaultEdgeExtensionID}})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://private.invalid/secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if err := store.Refresh(context.Background(), "edge"); err == nil {
		t.Fatal("untrusted redirect accepted")
	}
	if requests.Load() != 1 {
		t.Fatalf("contacted redirect host: %d requests", requests.Load())
	}
}

func TestExtensionHTTPResponseLimitAndDeadline(t *testing.T) {
	store, err := NewExtensionStore(ExtensionStoreConfig{Directory: t.TempDir(), Interval: time.Hour, IDs: map[string]string{"edge": DefaultEdgeExtensionID}})
	if err != nil {
		t.Fatal(err)
	}
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: -1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1))), Request: r}, nil
	})
	if err := store.Refresh(context.Background(), "edge"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unbounded response: %v", err)
	}
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.Refresh(ctx, "edge"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline ignored: %v", err)
	}
}

func TestConfiguredChromeSourceUsesOwnUpdateEndpoint(t *testing.T) {
	fixture := newExtensionFixture(t)
	data := fixture.build(t, "1.2.0", nil)
	upstream := &extensionHTTPFixture{id: fixture.id, version: "1.2", data: data, hash: extensionSHA256(data), codebase: "https://clients2.googleusercontent.com/package"}
	store := newExtensionHTTPStore(t, upstream, t.TempDir())
	store.config.IDs = map[string]string{"chrome": fixture.id}
	originalTransport := store.client.Transport
	store.client.Transport = extensionRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/package" && r.URL.Host != "clients2.google.com" {
			t.Errorf("wrong Chrome upstream: %s", r.URL)
		}
		return originalTransport.RoundTrip(r)
	})
	if err := store.Refresh(context.Background(), "chrome"); err != nil {
		t.Fatal(err)
	}
	release, err := store.Latest("chrome")
	if err != nil || release.Source != "chrome" || !strings.Contains(release.DownloadURL, "/chrome/download") {
		t.Fatalf("Chrome release contract: %+v %v", release, err)
	}
}

func TestExtensionConcurrentRefreshAndCancellation(t *testing.T) {
	fixture := newExtensionFixture(t)
	data := fixture.build(t, "1.0", nil)
	gate := make(chan struct{})
	upstream := &extensionHTTPFixture{id: fixture.id, version: "1.0", data: data, hash: extensionSHA256(data), gate: gate, entered: make(chan struct{})}
	store := newExtensionHTTPStore(t, upstream, t.TempDir())
	first := make(chan error, 1)
	go func() { first <- store.Refresh(context.Background(), "edge") }()
	select {
	case <-upstream.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not start")
	}
	// A waiter that cancels must not cancel the owner or start another request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Refresh(ctx, "edge"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	var waiting sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 8; i++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			if store.Refresh(context.Background(), "edge") == nil {
				successes.Add(1)
			}
		}()
	}
	// All operations remain blocked at the single owner until the response is released.
	time.Sleep(50 * time.Millisecond)
	upstream.mu.Lock()
	checks := upstream.checks
	upstream.mu.Unlock()
	if checks != 1 {
		t.Fatalf("expected one in-flight request, got %d", checks)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	waiting.Wait()
	if successes.Load() != 8 {
		t.Fatalf("waiter successes %d", successes.Load())
	}
	upstream.mu.Lock()
	checks, downloads := upstream.checks, upstream.downloads
	upstream.mu.Unlock()
	if checks != 1 || downloads != 1 {
		t.Fatalf("duplicate requests checks=%d downloads=%d", checks, downloads)
	}
}

func TestExtensionRunCancelsInFlightAndStops(t *testing.T) {
	fixture := newExtensionFixture(t)
	upstream := &extensionHTTPFixture{id: fixture.id, version: "1.0", gate: make(chan struct{}), entered: make(chan struct{})}
	store := newExtensionHTTPStore(t, upstream, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); store.Run(ctx, nil) }()
	select {
	case <-upstream.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("startup check did not begin")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not join updater")
	}
	upstream.mu.Lock()
	checks := upstream.checks
	upstream.mu.Unlock()
	if checks != 1 {
		t.Fatalf("cancelled run made %d checks", checks)
	}
	store.config.AutoUpdate = false
	disabledContext, stop := context.WithCancel(context.Background())
	stop()
	store.Run(disabledContext, nil)
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if upstream.checks != checks {
		t.Fatal("disabled updater made requests")
	}
}

func TestExtensionConfigValidation(t *testing.T) {
	for _, test := range []struct{ name, value string }{{"EXTENSION_UPDATE_INTERVAL", ""}, {"EXTENSION_UPDATE_INTERVAL", "1s"}, {"EXTENSION_UPDATE_INTERVAL", "153722868m"}, {"EXTENSION_UPDATE_ENABLED", ""}, {"EXTENSION_UPDATE_ENABLED", "unknown"}, {"EXTENSION_EDGE_ID", "../../bad"}, {"EXTENSION_CHROME_ID", strings.Repeat("z", 32)}} {
		t.Run(test.name+"="+test.value, func(t *testing.T) {
			clearExtensionSettingsEnvironment(t)
			t.Setenv(test.name, test.value)
			if _, err := ExtensionStoreConfigFromEnv(); err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("accepted %s=%s or reported wrong variable: %v", test.name, test.value, err)
			}
		})
	}
	if _, err := NewExtensionStore(ExtensionStoreConfig{Directory: t.TempDir(), Interval: time.Hour, IDs: map[string]string{"edge": "../../bad"}}); err == nil {
		t.Fatal("accepted invalid store ID")
	}
}

func TestValidateExtensionCRXFixture(t *testing.T) {
	fixture := os.Getenv("CX_EXTENSION_CRX_FIXTURE")
	if fixture == "" {
		t.Skip("CX_EXTENSION_CRX_FIXTURE is not set")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := validateExtensionCRX(data, DefaultEdgeExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestVersion != 3 || manifest.Version == "" || manifest.Name == "" {
		t.Fatalf("unexpected extension manifest: %+v", manifest)
	}
}

func TestExtensionOfficialStoreLive(t *testing.T) {
	if os.Getenv("CX_EXTENSION_LIVE") != "1" {
		t.Skip("set CX_EXTENSION_LIVE=1 to verify the official Edge store")
	}
	store, err := NewExtensionStore(ExtensionStoreConfig{Directory: t.TempDir(), Interval: time.Hour, IDs: map[string]string{"edge": DefaultEdgeExtensionID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh(context.Background(), "edge"); err != nil {
		t.Fatal(err)
	}
	release, data, err := store.Download("edge", "")
	if err != nil {
		t.Fatal(err)
	}
	if extensionSHA256(data) != release.SHA256 {
		t.Fatal("live download digest mismatch")
	}
	t.Logf("official Edge package ID=%s version=%s bytes=%d sha256=%s", release.ExtensionID, release.Version, len(data), release.SHA256)
}

func mustParseExtensionURL(t *testing.T, value string) *url.URL {
	t.Helper()
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
