package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Manager struct {
	Root, CertificateDir, ConfigDir     string
	ProxyConfig                         string
	Check                               func(string) error
	Reload                              func() error
	Report                              func(string)
	active                              []Certificate
	activeDir, rollbackDir, fingerprint string
	uncertainDirs                       []string
	restored                            bool
}

func (m *Manager) ActiveConfig() string { return filepath.Join(m.Root, "active.conf") }
func (m *Manager) TLSReady() bool       { return len(m.active) > 0 }

func nginxQuote(path string) string {
	return `"` + strings.NewReplacer(`\`, `/`, `"`, `\"`, `$`, `\$`).Replace(path) + `"`
}

// Refresh validates an entirely private candidate before changing the active
// file. A failed check/reload leaves the preceding file and certificate bytes intact.
func (m *Manager) Refresh(now time.Time) (bool, error) {
	if !m.restored {
		directory, pairs, _, restoreErr := readGeneration(m.Root, now, m.Report)
		if restoreErr != nil {
			diagnostic(m.Report, "private certificate snapshot restore failed: %v", restoreErr)
		} else {
			m.activeDir = directory
			m.active = pairs
		}
		m.restored = true
	}
	pairs, err := ScanCertificates(m.CertificateDir, now, m.Report)
	if err != nil {
		return false, err
	}
	// A source may disappear mid-renewal. Keep still-valid, previously accepted
	// bytes until replaced by a preferred certificate or naturally expired.
	ids := map[string]bool{}
	for _, p := range pairs {
		ids[p.ID] = true
	}
	for _, p := range m.active {
		if !ids[p.ID] && !now.Before(p.ValidFrom) && now.Before(p.ValidUntil) {
			pairs = append(pairs, p)
			ids[p.ID] = true
		}
	}
	fallback, selected := SelectCertificates(pairs)
	effective := map[string]Certificate{}
	if fallback.ID != "" {
		effective[fallback.ID] = fallback
	}
	for _, p := range selected {
		effective[p.ID] = p
	}
	var ordered []Certificate
	for _, p := range effective {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	files := map[string][]byte{}
	if strings.TrimSpace(m.ConfigDir) != "" {
		err = filepath.WalkDir(m.ConfigDir, func(path string, entry fs.DirEntry, e error) error {
			if e != nil {
				if path == m.ConfigDir && os.IsNotExist(e) {
					return fs.SkipDir
				}
				return e
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			relative, e := filepath.Rel(m.ConfigDir, path)
			if e != nil {
				return e
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if strings.HasSuffix(strings.ToLower(relative), ".conf") || strings.HasSuffix(strings.ToLower(relative), ".inc") {
				files[relative] = data
			}
			return nil
		})
	}
	if err != nil {
		return false, err
	}
	digest := sha256.New()
	digest.Write([]byte(m.ProxyConfig))
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(digest, "%s\x00%s\x00", name, files[name])
	}
	for _, p := range ordered {
		fmt.Fprintf(digest, "%s\x00", p.ID)
	}
	fmt.Fprintf(digest, "default:%s\x00", fallback.ID)
	var selectedNames []string
	for name := range selected {
		selectedNames = append(selectedNames, name)
	}
	sort.Strings(selectedNames)
	for _, name := range selectedNames {
		fmt.Fprintf(digest, "%s:%s\x00", name, selected[name].ID)
	}
	fingerprint := hex.EncodeToString(digest.Sum(nil))
	if fingerprint == m.fingerprint {
		return false, nil
	}
	if err = os.MkdirAll(m.Root, 0700); err != nil {
		return false, err
	}
	candidate, err := os.MkdirTemp(m.Root, "generation-")
	if err != nil {
		return false, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(candidate)
		}
	}()
	configRoot := filepath.Join(candidate, "conf.d")
	if err = os.MkdirAll(configRoot, 0700); err != nil {
		return false, err
	}
	for _, template := range []string{"tiku-adapter.conf", "tiku-proxy.inc"} {
		data, readErr := embeddedTemplate(template)
		if readErr != nil {
			return false, readErr
		}
		if err = os.WriteFile(filepath.Join(configRoot, template), data, 0600); err != nil {
			return false, err
		}
	}
	for _, p := range ordered {
		if err = os.WriteFile(filepath.Join(candidate, "cert-"+p.ID+".pem"), p.CertificatePEM, 0600); err != nil {
			return false, err
		}
		if err = os.WriteFile(filepath.Join(candidate, "cert-"+p.ID+".key"), p.KeyPEM, 0600); err != nil {
			return false, err
		}
	}
	manifest, err := json.Marshal(ordered)
	if err != nil {
		return false, err
	}
	if err = os.WriteFile(filepath.Join(candidate, "certificates.json"), manifest, 0600); err != nil {
		return false, err
	}
	servers := renderServers(candidate, configRoot, fallback, selected)
	if err = os.WriteFile(filepath.Join(candidate, "servers.conf"), []byte(servers), 0600); err != nil {
		return false, err
	}
	if err = os.WriteFile(filepath.Join(candidate, "trusted.conf"), []byte(m.ProxyConfig), 0600); err != nil {
		return false, err
	}
	config := fmt.Sprintf("# tiku-gateway-generation: %s\nworker_processes auto;\npid %s;\nerror_log /dev/stderr warn;\nevents { worker_connections 1024; }\nhttp {\n include /etc/nginx/mime.types;\n default_type application/octet-stream;\n include %s;\n include %s;\n include %s;\n include %s;\n include %s;\n}\n", filepath.Base(candidate), nginxQuote(filepath.Join(m.Root, "nginx.pid")), nginxQuote(filepath.Join(candidate, "trusted.conf")), nginxQuote(filepath.Join(configRoot, "*.conf")), nginxQuote(filepath.Join(candidate, "extra", "*.conf")), nginxQuote(filepath.Join(candidate, "extra", "*.inc")), nginxQuote(filepath.Join(candidate, "servers.conf")))
	candidateConfig := filepath.Join(candidate, "nginx.conf")
	if err = os.WriteFile(candidateConfig, []byte(config), 0600); err != nil {
		return false, err
	}
	if m.Check == nil {
		return false, fmt.Errorf("Nginx candidate checker is required")
	}
	// External files are optional extensions. Validate each one in isolation so
	// a bad operator file cannot disable the generated base configuration.
	for _, name := range names {
		if reason := reservedExternalConfig(files[name]); reason != "" {
			diagnostic(m.Report, "external Nginx config %s skipped: %s", name, reason)
			continue
		}
		relative := filepath.ToSlash(name)
		path := filepath.Join(configRoot, "extra", filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return false, err
		}
		data := strings.ReplaceAll(string(files[name]), "/etc/nginx/conf.d/", filepath.ToSlash(configRoot)+"/")
		if err = os.WriteFile(path, []byte(data), 0600); err != nil {
			return false, err
		}
		if strings.HasSuffix(strings.ToLower(name), ".conf") || strings.HasSuffix(strings.ToLower(name), ".inc") {
			if checkErr := m.Check(candidateConfig); checkErr != nil {
				diagnostic(m.Report, "external Nginx config %s skipped: %v", name, checkErr)
				_ = os.Remove(path)
				continue
			}
		}
	}
	if err = m.Check(candidateConfig); err != nil {
		return false, fmt.Errorf("candidate nginx -t failed: %w", err)
	}
	previous, readErr := os.ReadFile(m.ActiveConfig())
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, readErr
	}
	if err = atomicWrite(m.ActiveConfig(), []byte(config)); err != nil {
		return false, err
	}
	if len(previous) > 0 && m.Reload != nil {
		if err = m.Reload(); err != nil {
			rollbackErr := atomicWrite(m.ActiveConfig(), previous)
			// Reload may have partially succeeded. Re-apply the old, valid configuration.
			if rollbackErr == nil {
				rollbackErr = m.Reload()
			}
			if rollbackErr != nil {
				// A signal/reload can fail after the master has consumed bytes.
				// Keep both generations until a later reload is acknowledged.
				keep = true
				m.uncertainDirs = append(m.uncertainDirs, candidate)
			}
			return false, fmt.Errorf("Nginx reload failed: %w (rollback: %v)", err, rollbackErr)
		}
	}
	m.rollbackDir = m.activeDir
	m.activeDir = candidate
	m.active = ordered
	m.fingerprint = fingerprint
	keep = true
	cleanupGenerations(m.Root, m.activeDir, m.rollbackDir)
	m.uncertainDirs = nil
	if m.TLSReady() {
		diagnostic(m.Report, "HTTPS enabled: %d verified certificate snapshots", len(ordered))
	} else {
		diagnostic(m.Report, "HTTPS 未启用：未找到有效证书；HTTP 入口继续运行")
	}
	return true, nil
}

func reservedExternalConfig(data []byte) string {
	text := string(data)
	checks := []struct {
		pattern *regexp.Regexp
		reason  string
	}{
		{regexp.MustCompile(`(?im)\bupstream\s+[^\s{;]*tiku_adapter\b`), "reserved upstream tiku_adapter"},
		{regexp.MustCompile(`(?im)\blog_format\s+tiku_safe\b`), "reserved log format tiku_safe"},
		{regexp.MustCompile(`(?im)\bmap\s+\$http_upgrade\b`), "reserved upgrade map"},
		{regexp.MustCompile(`(?im)\blocation\s+(?:=|\^~|~\*?|\s)*\s*/api/Wechat_qc_code\b`), "reserved QR-code location"},
		{regexp.MustCompile(`(?im)\blisten\s+(?:[^;]*\s)?(?:\b8060\b|\b8680\b|\b9043\b)(?:\s|:|;)`), "reserved generated listener"},
		{regexp.MustCompile(`(?im)\bdefault_server\b`), "reserved default listener"},
		{regexp.MustCompile(`(?im)\bserver_name\s+_\s*;`), "reserved catch-all server"},
		{regexp.MustCompile(`(?im)\binclude\s+["']?/etc/nginx/conf\.d/`), "reserved generated config include"},
	}
	for _, check := range checks {
		if check.pattern.MatchString(text) {
			return check.reason
		}
	}
	return ""
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".active-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func renderServers(directory, confRoot string, fallback Certificate, selected map[string]Certificate) string {
	common := fmt.Sprintf(`
 access_log /var/log/nginx/access.log tiku_safe;
 client_header_timeout 3600s;
 client_body_timeout 3600s;
 send_timeout 3600s;
 keepalive_timeout 3600s;
 location = /api/Wechat_qc_code {
  alias /usr/share/nginx/html/Wechat_qc_code.jpg;
  default_type image/jpeg;
  add_header Cache-Control "no-cache";
 }
 location / {
  proxy_pass http://tiku_adapter;
  include %s;
 }
`, nginxQuote(filepath.Join(confRoot, "tiku-proxy.inc")))
	var b strings.Builder
	fmt.Fprintf(&b, "server {\n listen 8060 default_server;\n listen 8680 default_server;\n server_name _;\n%s}\n", common)
	if fallback.ID == "" {
		return b.String()
	}
	tlsServer := func(name string, p Certificate, defaultServer bool) {
		suffix := ""
		if defaultServer {
			suffix = " default_server"
		}
		fmt.Fprintf(&b, "server {\n listen 9043 ssl%s;\n server_name %s;\n ssl_certificate %s;\n ssl_certificate_key %s;\n ssl_protocols TLSv1.2 TLSv1.3;\n ssl_session_cache shared:SSL:10m;\n ssl_session_timeout 10m;\n%s}\n", suffix, name, nginxQuote(filepath.Join(directory, "cert-"+p.ID+".pem")), nginxQuote(filepath.Join(directory, "cert-"+p.ID+".key")), common)
	}
	tlsServer("_", fallback, true)
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		serverName := name
		// Nginx's native wildcard also matches deeper subdomains; use a regex so
		// SNI routing agrees with TLS's one-label wildcard coverage.
		if strings.HasPrefix(name, "*.") {
			// Nginx accepts the `~` regex marker; use a scoped PCRE modifier
			// because `~*` is parsed as part of the expression by some builds.
			serverName = "~^(?i:[^.]+\\." + regexp.QuoteMeta(name[2:]) + ")$"
		}
		tlsServer(serverName, selected[name], false)
	}
	return b.String()
}
