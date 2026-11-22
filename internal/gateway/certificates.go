// Package gateway manages the container's private Nginx configuration and TLS snapshots.
package gateway

import (
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// ScanCertificates reads regular PEM files only. Paths never enter Nginx syntax;
// the generated configuration uses private filenames derived from content hashes.
func ScanCertificates(root string, now time.Time, report func(string)) ([]Certificate, error) {
	type bundle struct {
		path  string
		certs []byte
	}
	type privateKey struct {
		path string
		pem  []byte
	}
	var bundles []bundle
	keys := map[string]privateKey{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && os.IsNotExist(walkErr) {
				return fs.SkipDir
			}
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if info.Size() > 16<<20 {
			diagnostic(report, "certificate scan: %s exceeds 16 MiB", path)
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		relative = filepath.ToSlash(relative)
		var chain []byte
		for len(raw) > 0 {
			block, rest := pem.Decode(raw)
			if block == nil {
				break
			}
			raw = rest
			if block.Type == "CERTIFICATE" {
				chain = append(chain, pem.EncodeToMemory(block)...)
				continue
			}
			if strings.Contains(block.Type, "ENCRYPTED") || x509.IsEncryptedPEMBlock(block) {
				diagnostic(report, "certificate scan: %s encrypted private key skipped", relative)
				continue
			}
			var key any
			switch block.Type {
			case "PRIVATE KEY":
				key, e = x509.ParsePKCS8PrivateKey(block.Bytes)
			case "RSA PRIVATE KEY":
				key, e = x509.ParsePKCS1PrivateKey(block.Bytes)
			case "EC PRIVATE KEY":
				key, e = x509.ParseECPrivateKey(block.Bytes)
			default:
				continue
			}
			if e != nil {
				diagnostic(report, "certificate scan: %s invalid private key", relative)
				continue
			}
			signer, ok := key.(crypto.Signer)
			if !ok {
				diagnostic(report, "certificate scan: %s unsupported private key", relative)
				continue
			}
			public, e := x509.MarshalPKIXPublicKey(signer.Public())
			if e != nil {
				continue
			}
			digest := sha256.Sum256(public)
			id := hex.EncodeToString(digest[:])
			// A conventional default key wins only among keys with the same public key.
			previous, exists := keys[id]
			if !exists || relative == "privkey.key" || previous.path != "privkey.key" && relative < previous.path {
				keys[id] = privateKey{relative, pem.EncodeToMemory(block)}
			}
		}
		if len(chain) > 0 {
			bundles = append(bundles, bundle{relative, chain})
		}
		return nil
	})
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
