package test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// These fixtures terminate every provider request locally. There is no dial,
// forwarding transport, insecure certificate bypass, or real provider credential.
var providerHosts = map[string]bool{
	"cx.icodef.com":        true,
	"lyck6.cn":             true,
	"tk.enncy.cn":          true,
	"easylearn.baidu.com":  true,
	"new.api.51aidian.com": true,
	"api.lemtk.xyz":        true,
	"api.tikuhai.com":      true,
}

const syntheticProviderToken = "SYNTHETIC-PROVIDER-261025-NOT-A-CREDENTIAL"

type providerFixture struct {
	name, scheme, host, path, method string
	query, form                      url.Values
	jsonBody                         string
	headers                          map[string]string
	response                         string
}

type providerObservation struct {
	Name              string   `json:"name"`
	Requests          int      `json:"requests"`
	Connects          int      `json:"connects"`
	Unknown           int      `json:"unknown"`
	OutboundForwarded int      `json:"outboundForwarded"`
	Problems          []string `json:"problems"`
}

type providerProxy struct {
	server     *httptest.Server
	cert       tls.Certificate
	caFile     string
	mu         sync.Mutex
	active     *providerFixture
	observed   providerObservation
	allUnknown int
	handlers   sync.WaitGroup
}

var localProviders *providerProxy

func TestMain(m *testing.M) {
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE"}
	type envValue struct {
		value   string
		present bool
	}
	initial := map[string]envValue{}
	for _, key := range keys {
		value, present := os.LookupEnv(key)
		initial[key] = envValue{value, present}
	}
	dir, err := os.MkdirTemp("", "tiku-offline-providers-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "local provider fixture temp directory failed")
		os.Exit(1)
	}
	p, err := newProviderProxy(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "local provider TLS fixture initialization failed")
		os.Exit(1)
	}
	localProviders = p
	code := m.Run()
	p.server.Close()
	p.handlers.Wait()
	p.mu.Lock()
	unknown := p.allUnknown
	active := p.active != nil
	p.mu.Unlock()
	if unknown != 0 || active {
		fmt.Fprintln(os.Stderr, "local provider fixture unexpected request or active case at cleanup")
		code = 1
	}
	for _, key := range keys {
		value, present := os.LookupEnv(key)
		if initial[key] != (envValue{value, present}) {
			fmt.Fprintln(os.Stderr, "local provider fixture environment not restored:", key)
			code = 1
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "local provider fixture temp cleanup failed")
		code = 1
	}
	os.Exit(code)
}

func newProviderProxy(dir string) (*providerProxy, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(providerHosts))
	for host := range providerHosts {
		names = append(names, host)
	}
	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(261025), Subject: pkix.Name{CommonName: "synthetic offline provider fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: names, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return nil, err
	}
	caFile := filepath.Join(dir, "synthetic-ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		return nil, err
	}
	p := &providerProxy{cert: cert, caFile: caFile}
	p.server = httptest.NewServer(http.HandlerFunc(p.serveProxy))
	return p, nil
}

func useProviderFixture(t *testing.T, fixture providerFixture) {
	t.Helper()
	p := localProviders
	if p == nil {
		t.Fatal("local provider fixture missing")
	}
	p.mu.Lock()
	if p.active != nil {
		p.mu.Unlock()
		t.Fatal("provider cases must remain sequential")
	}
	p.active = &fixture
	p.observed = providerObservation{Name: fixture.name}
	p.mu.Unlock()
	// t.Setenv restores each value after this one non-parallel test. The proxy and
	// CA live only for this test binary, not the host or another test package.
	t.Setenv("HTTP_PROXY", p.server.URL)
	t.Setenv("HTTPS_PROXY", p.server.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("SSL_CERT_FILE", p.caFile)
	probe, err := http.NewRequest(http.MethodGet, fixture.scheme+"://"+fixture.host+fixture.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := http.ProxyFromEnvironment(probe)
	if err != nil || proxy == nil || proxy.String() != p.server.URL {
		t.Fatal("standard transport did not bind the private local proxy")
	}
	t.Cleanup(func() {
		p.mu.Lock()
		observed := p.observed
		p.active = nil
		p.mu.Unlock()
		raw, _ := json.Marshal(observed)
		t.Logf("OFFLINE_PROVIDER_OBSERVATION %s", raw)
		wantConnects := 0
		if fixture.scheme == "https" {
			wantConnects = 1
		}
		if observed.Requests != 1 || observed.Connects != wantConnects || observed.Unknown != 0 || observed.OutboundForwarded != 0 || len(observed.Problems) != 0 {
			t.Errorf("local provider protocol/count contract failed: %s", raw)
		}
	})
}

func (p *providerProxy) reject(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allUnknown++
	p.observed.Unknown++
	p.observed.Problems = append(p.observed.Problems, message)
}

func (p *providerProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		if r.URL.Scheme != "http" || r.Host != r.URL.Host {
			p.reject("invalid plaintext proxy target")
			http.Error(w, "local fixture rejected target", http.StatusForbidden)
			return
		}
		body, ok := p.reply(r, "http", r.URL.Host)
		if !ok {
			http.Error(w, "local fixture protocol mismatch", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
		return
	}
	p.mu.Lock()
	fixture := p.active
	allowed := fixture != nil && fixture.scheme == "https" && providerHosts[fixture.host] && r.Host == fixture.host+":443"
	if allowed {
		p.observed.Connects++
	}
	p.mu.Unlock()
	if !allowed {
		p.reject("unknown CONNECT authority")
		http.Error(w, "local fixture rejected CONNECT", http.StatusForbidden)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.reject("local proxy cannot hijack")
		http.Error(w, "local fixture failed", 500)
		return
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		p.reject("local CONNECT hijack failed")
		return
	}
	p.handlers.Add(1)
	defer p.handlers.Done()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		p.reject("CONNECT acknowledgement failed")
		return
	}
	if err = buffer.Flush(); err != nil {
		p.reject("CONNECT flush failed")
		return
	}
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{p.cert}, MinVersion: tls.VersionTLS12})
	defer secure.Close()
	if err = secure.Handshake(); err != nil {
		p.reject("local TLS handshake failed")
		return
	}
	request, err := http.ReadRequest(bufio.NewReader(secure))
	if err != nil {
		p.reject("local TLS request read failed")
		return
	}
	defer request.Body.Close()
	if request.Host != fixture.host || request.URL.IsAbs() {
		p.reject("TLS authority mismatch")
		return
	}
	body, valid := p.reply(request, "https", fixture.host)
	status := http.StatusOK
	if !valid {
		status = http.StatusForbidden
		body = `{"error":"local fixture protocol mismatch"}`
	}
	response := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Close: true}
	if err = response.Write(secure); err != nil {
		p.reject("local TLS response write failed")
	}
}

func (p *providerProxy) reply(r *http.Request, scheme, host string) (string, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.active
	if f == nil || !providerHosts[host] || f.host != host || f.scheme != scheme || r.URL.Path != f.path || r.Method != f.method || err != nil || len(raw) > 65536 {
		p.allUnknown++
		p.observed.Unknown++
		p.observed.Problems = append(p.observed.Problems, "unknown host/path/method/body")
		return "", false
	}
	p.observed.Requests++
	problem := func(message string) { p.observed.Problems = append(p.observed.Problems, message) }
	if !reflect.DeepEqual(r.URL.Query(), f.query) {
		problem("query mismatch")
	}
	for key, value := range f.headers {
		if r.Header.Get(key) != value {
			problem("header mismatch: " + key)
		}
	}
	if f.form != nil {
		form, parseErr := url.ParseQuery(string(raw))
		if parseErr != nil || !reflect.DeepEqual(form, f.form) {
			problem("form mismatch")
		}
	} else if f.jsonBody != "" {
		var actual, expected any
		if json.Unmarshal(raw, &actual) != nil || json.Unmarshal([]byte(f.jsonBody), &expected) != nil || !reflect.DeepEqual(actual, expected) {
			problem("JSON body mismatch")
		}
	} else if len(raw) != 0 {
		problem("unexpected request body")
	}
	return f.response, len(p.observed.Problems) == 0
}

func assertNoProviderRequests(t *testing.T, control string) {
	t.Helper()
	p := localProviders
	p.mu.Lock()
	count := p.observed.Requests
	p.mu.Unlock()
	if count != 0 {
		t.Fatalf("%s must not issue HTTP; got %d", control, count)
	}
	t.Logf("OFFLINE_PROVIDER_CONTROL %s requests=0", control)
}

func fixtureJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertProviderAnswers(t *testing.T, got [][]string, err error, want [][]string) {
	t.Helper()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("actual provider parser answers=%v error=%v; want=%v", got, err, want)
	}
}

// Reverse the documented production decrypt transform for synthetic bytes only.
func syntheticBuguakeEnvelope(t *testing.T, question string) string {
	t.Helper()
	decoded := fixtureJSON(t, map[string]any{"que_stem": []any{map[string]any{"c": []any{map[string]string{"c": question}}}}, "que_answer": []any{map[string]any{"c": []any{map[string]string{"c": "黄州"}}}}})
	encoded := []byte(url.QueryEscape(decoded))
	actk := "SYNTHETIC_ACTK"
	key := []byte("34cab29ef956d78afd" + actk)
	ciphertext := make([]byte, len(encoded)*2)
	for i, b := range encoded {
		ciphertext[2*i] = key[2*i%len(key)]
		ciphertext[2*i+1] = b ^ key[(2*i+1)%len(key)]
	}
	return fixtureJSON(t, map[string]any{"data": map[string]any{"list": []any{map[string]string{"bdjson": base64.StdEncoding.EncodeToString(ciphertext), "actk": actk}}}})
}
