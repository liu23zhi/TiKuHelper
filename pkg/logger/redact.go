package logger

import (
	"regexp"
	"sort"
	"strings"
)

var (
	bearerSecretPattern  = regexp.MustCompile(`(?i)(\bbearer\s+)[a-z0-9._~+/=-]+`)
	authorizationPattern = regexp.MustCompile(`(?i)(["']?authorization["']?\s*[:=]\s*["']?(?:(?:bearer|basic)\s+)?)[^"'\s,;&}]+`)
	urlUserinfoPattern   = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/?#\s@"']+@`)
	prefixedKeyPattern   = regexp.MustCompile(`\b(?:sk|uapi)-[a-zA-Z0-9_-]+`)
	namedSecretPattern   = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|key|access[_-]?token|refresh[_-]?token|token|password|secret)["']?\s*[:=]\s*["']?)([^"'\s,;&}]+)`)
)

func RedactSecrets(text string, secrets ...string) string {
	// Replace longer configured secrets first so a common prefix cannot expose a suffix.
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	text = bearerSecretPattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = authorizationPattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = urlUserinfoPattern.ReplaceAllString(text, "${1}[REDACTED]@")
	text = prefixedKeyPattern.ReplaceAllString(text, "[REDACTED]")
	return namedSecretPattern.ReplaceAllString(text, "${1}[REDACTED]")
}
