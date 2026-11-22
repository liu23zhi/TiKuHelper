// Package gateway manages the container's private Nginx configuration and TLS snapshots.
package gateway

import (
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/youmark/pkcs8"
	"software.sslmate.com/src/go-pkcs12"
)

type Certificate struct {
	ID                    string
	CertPath, KeyPath     string
	DNSNames              []string
	CertificatePEM        []byte `json:"-"`
	KeyPEM                []byte `json:"-"`
	NotBefore, NotAfter   time.Time
	ValidFrom, ValidUntil time.Time // Intersection of the leaf and supplied chain validity windows.
}

func diagnostic(report func(string), format string, args ...any) {
	if report != nil {
		report(fmt.Sprintf(format, args...))
	}
}

func validDNSName(name string) bool {
	if strings.HasPrefix(name, "*.") {
		name = name[2:]
	}
	if len(name) > 253 || net.ParseIP(name) != nil || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

const (
	maxCertificateFileBytes = 16 << 20
	maxCertificateScanBytes = 64 << 20
	maxCertificateScanFiles = 256
)

var errCertificateScanFileBudget = errors.New("certificate scan file budget reached")

type certificateBundle struct {
	path  string
	certs []byte
}

type privateKeyCandidate struct {
	path string
	pem  []byte
}

func signerPEM(key any) ([]byte, crypto.Signer, error) {
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("private key does not implement crypto.Signer")
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), signer, nil
}

func parsePrivateKeyDER(der []byte, passwords []string) (any, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	for _, password := range passwords {
		if key, err := pkcs8.ParsePKCS8PrivateKey(der, []byte(password)); err == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("unsupported or encrypted private key")
}

func appendPrivateKey(path string, key any, keys map[string]privateKeyCandidate) error {
	canonical, signer, err := signerPEM(key)
	if err != nil {
		return err
	}
	public, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return err
	}
	digest := sha256.Sum256(public)
	id := hex.EncodeToString(digest[:])
	previous, exists := keys[id]
	if !exists || path == "privkey.key" || previous.path != "privkey.key" && path < previous.path {
		keys[id] = privateKeyCandidate{path: path, pem: canonical}
	}
	return nil
}

func appendCertificateChain(path string, certificates []*x509.Certificate, bundles *[]certificateBundle) {
	if len(certificates) == 0 {
		return
	}
	var chain []byte
	for _, certificate := range certificates {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})...)
	}
	*bundles = append(*bundles, certificateBundle{path: path, certs: chain})
}

func parseCertificateFile(path string, raw []byte, passwords []string, bundles *[]certificateBundle, keys map[string]privateKeyCandidate, report func(string)) {
	relative := path
	var pemFound bool
	var pemCertificates []*x509.Certificate
	remaining := raw
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		pemFound = true
		remaining = rest
		switch block.Type {
		case "CERTIFICATE":
			certificate, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				diagnostic(report, "certificate scan: %s invalid PEM certificate", relative)
				continue
			}
			pemCertificates = append(pemCertificates, certificate)
		default:
			key, err := parsePrivateKeyDER(block.Bytes, passwords)
			if err != nil && (strings.Contains(block.Type, "ENCRYPTED") || x509.IsEncryptedPEMBlock(block)) {
				for _, password := range passwords {
					decrypted, decryptErr := x509.DecryptPEMBlock(block, []byte(password))
					if decryptErr != nil {
						continue
					}
					key, err = parsePrivateKeyDER(decrypted, passwords)
					if err == nil {
						break
					}
				}
			}
			if err == nil {
				if keyErr := appendPrivateKey(relative, key, keys); keyErr != nil {
					diagnostic(report, "certificate scan: %s unsupported private key: %v", relative, keyErr)
				}
			} else if strings.Contains(block.Type, "PRIVATE") || strings.Contains(block.Type, "ENCRYPTED") {
				diagnostic(report, "certificate scan: %s private key could not be decoded", relative)
			}
		}
	}
	if pemFound {
		appendCertificateChain(relative, pemCertificates, bundles)
		return
	}

	// Non-PEM inputs are deliberately parsed by content, so a .crt/.key/.pfx
	// rename never changes the result.
	if certificates, err := x509.ParseCertificates(raw); err == nil {
		appendCertificateChain(relative, certificates, bundles)
	}
	for _, password := range append([]string{""}, passwords...) {
		key, certificate, caCertificates, err := pkcs12.DecodeChain(raw, password)
		if err != nil {
			continue
		}
		if certificate == nil {
			diagnostic(report, "certificate scan: %s PKCS#12 container has no leaf certificate", relative)
			return
		}
		if appendErr := appendPrivateKey(relative, key, keys); appendErr != nil {
			diagnostic(report, "certificate scan: %s unsupported PKCS#12 private key: %v", relative, appendErr)
			return
		}
		appendCertificateChain(relative, append([]*x509.Certificate{certificate}, caCertificates...), bundles)
		return
	}
	if key, err := parsePrivateKeyDER(raw, passwords); err == nil {
		if appendErr := appendPrivateKey(relative, key, keys); appendErr != nil {
			diagnostic(report, "certificate scan: %s unsupported DER private key: %v", relative, appendErr)
		}
	}
}

// ScanCertificates reads regular certificate/key files by content. Paths never
// enter Nginx syntax; generated configuration uses private filenames derived
// from content hashes. The legacy entry point intentionally has no passwords.
func ScanCertificates(root string, now time.Time, report func(string)) ([]Certificate, error) {
	return ScanCertificatesWithPasswords(root, now, nil, report)
}

// ScanCertificatesWithPasswords accepts plaintext password candidates for
// encrypted PKCS#8, legacy PEM and PKCS#12/PFX files. Passwords are never
// included in diagnostics. The scanner is bounded to avoid a misplaced large
// file or an unbounded directory blocking the gateway refresh loop.
func ScanCertificatesWithPasswords(root string, now time.Time, passwords []string, report func(string)) ([]Certificate, error) {
	var bundles []certificateBundle
	keys := map[string]privateKeyCandidate{}
	readBytes := int64(0)
	readFiles := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && os.IsNotExist(walkErr) {
				return fs.SkipDir
			}
			diagnostic(report, "certificate scan: skipped unreadable path %s: %v", path, walkErr)
			return fs.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			diagnostic(report, "certificate scan: skipped unreadable file metadata %s: %v", path, e)
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if readFiles >= maxCertificateScanFiles {
			diagnostic(report, "certificate scan: stopped after %d files", maxCertificateScanFiles)
			return errCertificateScanFileBudget
		}
		readFiles++
		if info.Size() > maxCertificateFileBytes {
			diagnostic(report, "certificate scan: %s exceeds 16 MiB", path)
			return nil
		}
		if readBytes+info.Size() > maxCertificateScanBytes {
			diagnostic(report, "certificate scan: skipped %s because the 64 MiB total budget would be exceeded", path)
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			diagnostic(report, "certificate scan: skipped unreadable file %s: %v", path, e)
			return nil
		}
		readBytes += int64(len(raw))
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		relative = filepath.ToSlash(relative)
		parseCertificateFile(relative, raw, passwords, &bundles, keys, report)
		return nil
	})
	if errors.Is(err, errCertificateScanFileBudget) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	var result []Certificate
	for _, b := range bundles {
		block, _ := pem.Decode(b.certs)
		leaf, e := x509.ParseCertificate(block.Bytes)
		if e != nil {
			diagnostic(report, "certificate scan: %s invalid certificate", b.path)
			continue
		}
		if leaf.IsCA || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
			diagnostic(report, "certificate scan: %s certificate is CA, expired or not yet valid", b.path)
			continue
		}
		server := len(leaf.ExtKeyUsage) == 0
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				server = true
			}
		}
		if !server || leaf.KeyUsage != 0 && leaf.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment) == 0 {
			diagnostic(report, "certificate scan: %s certificate is not usable for TLS server authentication", b.path)
			continue
		}
		var names []string
		seen := map[string]bool{}
		for _, name := range leaf.DNSNames {
			name = strings.ToLower(strings.TrimSuffix(name, "."))
			if validDNSName(name) && !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
		if len(names) == 0 {
			diagnostic(report, "certificate scan: %s has no valid DNS SAN (CN is not used)", b.path)
			continue
		}
		public, e := x509.MarshalPKIXPublicKey(leaf.PublicKey)
		if e != nil {
			continue
		}
		digest := sha256.Sum256(public)
		key, ok := keys[hex.EncodeToString(digest[:])]
		if !ok {
			diagnostic(report, "certificate scan: %s has no matching private key", b.path)
			continue
		}
		if _, e = tls.X509KeyPair(b.certs, key.pem); e != nil {
			diagnostic(report, "certificate scan: %s invalid certificate/key pair: %v", b.path, e)
			continue
		}
		validChain := true
		notBefore, notAfter := leaf.NotBefore, leaf.NotAfter
		for remaining := b.certs; len(remaining) > 0; {
			p, rest := pem.Decode(remaining)
			if p == nil {
				break
			}
			remaining = rest
			c, parseErr := x509.ParseCertificate(p.Bytes)
			if parseErr != nil || now.Before(c.NotBefore) || !now.Before(c.NotAfter) {
				validChain = false
				break
			}
			if c.NotBefore.After(notBefore) {
				notBefore = c.NotBefore
			}
			if c.NotAfter.Before(notAfter) {
				notAfter = c.NotAfter
			}
		}
		if !validChain {
			diagnostic(report, "certificate scan: %s contains an invalid or expired chain certificate", b.path)
			continue
		}
		content := sha256.New()
		content.Write(b.certs)
		content.Write(key.pem)
		sort.Strings(names)
		result = append(result, Certificate{ID: hex.EncodeToString(content.Sum(nil)), CertPath: b.path, KeyPath: key.path, DNSNames: names, CertificatePEM: b.certs, KeyPEM: key.pem, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, ValidFrom: notBefore, ValidUntil: notAfter})
	}
	sort.Slice(result, func(i, j int) bool { return certificateStableLess(result[i], result[j]) })
	return result, nil
}

func certificateStableLess(a, b Certificate) bool {
	if a.CertPath != b.CertPath {
		return a.CertPath < b.CertPath
	}
	if a.KeyPath != b.KeyPath {
		return a.KeyPath < b.KeyPath
	}
	return a.ID < b.ID
}

func SelectCertificates(pairs []Certificate) (Certificate, map[string]Certificate) {
	selected := map[string]Certificate{}
	var fallback Certificate
	for _, pair := range pairs {
		conventional := pair.CertPath == "fullchain.pem" && pair.KeyPath == "privkey.key"
		previousConventional := fallback.CertPath == "fullchain.pem" && fallback.KeyPath == "privkey.key"
		if fallback.ID == "" || conventional && !previousConventional || conventional == previousConventional && certificateStableLess(pair, fallback) {
			fallback = pair
		}
		for _, name := range pair.DNSNames {
			old, ok := selected[name]
			if !ok || pair.NotBefore.After(old.NotBefore) || pair.NotBefore.Equal(old.NotBefore) && (pair.NotAfter.After(old.NotAfter) || pair.NotAfter.Equal(old.NotAfter) && pair.ID < old.ID) {
				selected[name] = pair
			}
		}
	}
	return fallback, selected
}
