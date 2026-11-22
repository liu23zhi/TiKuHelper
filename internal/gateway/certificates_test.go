package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youmark/pkcs8"
	"software.sslmate.com/src/go-pkcs12"
)

func fixtureCertificate(t *testing.T, names []string, before, after time.Time, rsaKey bool) (Certificate, []byte, []byte) {
	t.Helper()
	var key any
	var public any
	if rsaKey {
		k, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			t.Fatal(e)
		}
		key = k
		public = &k.PublicKey
	} else {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		key = k
		public = &k.PublicKey
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "CN-is-not-a-SAN"}, DNSNames: names, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, public, key)
	if e != nil {
		t.Fatal(e)
	}
	keyDER, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	priv := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return Certificate{}, cert, priv
}
func writeFixture(t *testing.T, dir, name string, value []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, value, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestScanCertificatesMatchesContentsPreservesChainAndRejectsInvalid(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	_, cert, key := fixtureCertificate(t, []string{"A.Example.com", "*.example.net"}, now.Add(-time.Hour), now.Add(time.Hour), true)
	_, chain, _ := fixtureCertificate(t, []string{"intermediate.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, dir, "arbitrary", append(cert, chain...))
	writeFixture(t, dir, "not-a-key.crt", key)
	_, expired, expiredKey := fixtureCertificate(t, []string{"expired.example"}, now.Add(-2*time.Hour), now.Add(-time.Hour), false)
	writeFixture(t, dir, "expired", append(expired, expiredKey...))
	_, future, futureKey := fixtureCertificate(t, []string{"future.example"}, now.Add(time.Hour), now.Add(2*time.Hour), false)
	writeFixture(t, dir, "future", append(future, futureKey...))
	_, cn, cnKey := fixtureCertificate(t, nil, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, dir, "cn", append(cn, cnKey...))
	_, mismatch, _ := fixtureCertificate(t, []string{"mismatch.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, dir, "mismatch", mismatch)
	writeFixture(t, dir, "encrypted", pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("encrypted")}))
	writeFixture(t, dir, "ignored.txt", []byte("not PEM"))
	var messages []string
	pairs, err := ScanCertificates(dir, now, func(s string) { messages = append(messages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 {
		t.Fatalf("want one valid pair, got %+v; %v", pairs, messages)
	}
	if len(pairs[0].DNSNames) != 2 || !strings.Contains(strings.Join(pairs[0].DNSNames, ","), "a.example.com") {
		t.Fatalf("SAN normalization: %+v", pairs[0].DNSNames)
	}
	if strings.Count(string(pairs[0].CertificatePEM), "BEGIN CERTIFICATE") != 2 {
		t.Fatal("full chain lost")
	}
	if !strings.Contains(strings.Join(messages, "\n"), "encrypted") {
		t.Fatal("encrypted key diagnostic missing")
	}
}

func TestSelectCertificatesNewestSANDeduplicatedAndFixedDefault(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	_, older, oldKey := fixtureCertificate(t, []string{"same.example", "*.example.net"}, now.Add(-2*time.Hour), now.Add(4*time.Hour), false)
	_, newer, newKey := fixtureCertificate(t, []string{"same.example"}, now.Add(-time.Hour), now.Add(2*time.Hour), false)
	writeFixture(t, dir, "fullchain.pem", older)
	writeFixture(t, dir, "privkey.key", oldKey)
	writeFixture(t, dir, "new-cert", newer)
	writeFixture(t, dir, "new-key", newKey)
	pairs, e := ScanCertificates(dir, now, nil)
	if e != nil {
		t.Fatal(e)
	}
	fallback, selected := SelectCertificates(pairs)
	if fallback.CertPath != "fullchain.pem" {
		t.Fatalf("default = %s", fallback.CertPath)
	}
	if len(selected) != 2 || selected["same.example"].CertPath != "new-cert" || selected["*.example.net"].CertPath != "fullchain.pem" {
		t.Fatalf("selection: %+v", selected)
	}
}

func TestScanDoesNotFollowOutsideSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	now := time.Now()
	_, cert, key := fixtureCertificate(t, []string{"outside.example"}, now.Add(-time.Hour), now.Add(time.Hour), false)
	writeFixture(t, outside, "combined", append(cert, key...))
	if e := os.Symlink(filepath.Join(outside, "combined"), filepath.Join(dir, "linked")); e != nil {
		t.Skip("symlinks unavailable")
	}
	pairs, e := ScanCertificates(dir, now, nil)
	if e != nil || len(pairs) != 0 {
		t.Fatalf("outside symlink scanned: %+v %v", pairs, e)
	}
}

func TestScanSupportsPKCS1RSAAndSEC1ECPrivateKeys(t *testing.T) {
	now := time.Now()
	for _, rsaKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "SEC1", true: "PKCS1"}[rsaKey], func(t *testing.T) {
			dir := t.TempDir()
			_, cert, key := fixtureCertificate(t, []string{"format.example"}, now.Add(-time.Hour), now.Add(time.Hour), rsaKey)
			block, _ := pem.Decode(key)
			parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
			if e != nil {
				t.Fatal(e)
			}
			if rsaKey {
				key = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(parsed.(*rsa.PrivateKey))})
			} else {
				der, e := x509.MarshalECPrivateKey(parsed.(*ecdsa.PrivateKey))
				if e != nil {
					t.Fatal(e)
				}
				key = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
			}
			writeFixture(t, dir, "combined-without-extension", append(cert, key...))
			pairs, e := ScanCertificates(dir, now, nil)
			if e != nil || len(pairs) != 1 {
				t.Fatalf("unsupported key: %d %v", len(pairs), e)
			}
		})
	}
}

func TestScanSupportsDERAndPasswordProtectedPKCS12WithoutExtensions(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	_, certPEM, keyPEM := fixtureCertificate(t, []string{"der.example"}, now.Add(-time.Hour), now.Add(time.Hour), true)
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, "certificate-without-crt", certBlock.Bytes)
	writeFixture(t, dir, "private-without-key", keyDER)
	pairs, err := ScanCertificates(dir, now, nil)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("DER pair: %d %v", len(pairs), err)
	}

	pfx, err := pkcs12.Encode(rand.Reader, key, certificate, nil, "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	pfxDir := t.TempDir()
	writeFixture(t, pfxDir, "certificate-container", pfx)
	pairs, err = ScanCertificatesWithPasswords(pfxDir, now, []string{"wrong", "fixture-password"}, nil)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("password protected PKCS#12: %d %v", len(pairs), err)
	}

	pkcs8DER, err := pkcs8.MarshalPrivateKey(key, []byte("fixture-key-password"), nil)
	if err != nil {
		t.Fatal(err)
	}
	keyDir := t.TempDir()
	writeFixture(t, keyDir, "encrypted-certificate", certBlock.Bytes)
	writeFixture(t, keyDir, "encrypted-private", pkcs8DER)
	pairs, err = ScanCertificatesWithPasswords(keyDir, now, []string{"fixture-key-password"}, nil)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("password protected PKCS#8: %d %v", len(pairs), err)
	}
}

func TestScanCertificateBudgetsBoundFilesAndBytes(t *testing.T) {
	now := time.Now()
	t.Run("file-count", func(t *testing.T) {
		dir := t.TempDir()
		var messages []string
		for i := 0; i < maxCertificateScanFiles+1; i++ {
			writeFixture(t, dir, filepath.Join("nested", fmt.Sprintf("%03d", i)), []byte("ignored"))
		}
		_, err := ScanCertificates(dir, now, func(message string) { messages = append(messages, message) })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(messages, "\n"), "stopped after") {
			t.Fatalf("missing file budget diagnostic: %v", messages)
		}
	})
	t.Run("total-bytes", func(t *testing.T) {
		dir := t.TempDir()
		var messages []string
		payload := make([]byte, 1<<20)
		for i := 0; i < maxCertificateScanBytes/(1<<20)+1; i++ {
			writeFixture(t, dir, fmt.Sprintf("%03d.bin", i), payload)
		}
		_, err := ScanCertificates(dir, now, func(message string) { messages = append(messages, message) })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(messages, "\n"), "64 MiB total budget") {
			t.Fatalf("missing total budget diagnostic: %v", messages)
		}
	})
}
