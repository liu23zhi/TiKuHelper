package service

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// Administrators maintain DNS deny rules. Local machine names and loopback /
// unspecified IPs are always reserved for the site, including non-root paths.
var ErrScriptDomainDenied = fmt.Errorf("%w：域名禁止用于脚本", ErrScriptDomainUnavailable)

func isBuiltinReservedScriptHostname(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "localhost.localdomain" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func parseScriptDeniedDomain(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	host := strings.TrimPrefix(value, "*.")
	if !validScriptDomainHost(host) {
		return "", fmt.Errorf("禁止设置的域名 %q 格式不合法：只支持 admin.a.com 或 *.internal.a.com，不接受协议、端口、路径或 IP", raw)
	}
	return value, nil
}

func normalizeScriptDeniedDomains(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		rule, err := parseScriptDeniedDomain(value)
		if err != nil {
			continue // Keep the settings GET repairable for old/corrupt rows.
		}
		if _, exists := seen[rule]; !exists {
			seen[rule] = struct{}{}
			result = append(result, rule)
		}
	}
	sort.Strings(result)
	return result
}

// validateHostNotDenied accepts an actual host (including a development port).
// Matching ignores ports, so :443/:8443 cannot bypass a prohibition.
// Callers share their loaded settings with allow checks to avoid extra queries.
func (settings ScriptDomainSettings) validateHostNotDenied(host string) error {
	normalized := normalizeScriptLauncherHost(host)
	parsed, err := url.Parse("https://" + normalized)
	if err != nil || normalized == "" {
		return nil // The existing address/allow validator handles invalid syntax.
	}
	hostname := parsed.Hostname()
	if isBuiltinReservedScriptHostname(hostname) {
		return fmt.Errorf("%w：本机名称、回环和未指定地址不能用于脚本", ErrScriptDomainDenied)
	}
	for _, rule := range settings.DeniedDomains {
		matches := hostname == rule
		if strings.HasPrefix(rule, "*.") {
			matches = scriptDomainWildcardCovers(ScriptDomainFormat{Kind: ScriptDomainFormatWildcard, Host: strings.TrimPrefix(rule, "*.")}, hostname)
		}
		if matches {
			return fmt.Errorf("%w：%s 命中禁止项 %s", ErrScriptDomainDenied, hostname, rule)
		}
	}
	return nil
}

func (settings ScriptDomainSettings) validateAddressNotDenied(address string) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return ErrScriptDomainUnavailable
	}
	if IsReservedScriptHostRoot(parsed.Host, parsed.Path) {
		return fmt.Errorf("%w：主站域名保留，不能分配为脚本子域名", ErrScriptDomainDenied)
	}
	return settings.validateHostNotDenied(parsed.Host)
}
