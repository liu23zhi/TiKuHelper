// Package proxysource selects the same trusted-proxy input for PID 1 and the
// standalone adapter. The consumers enforce their own IP and protocol policies.
package proxysource

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const DefaultFile = "trusted_proxies.txt"

// Read selects the first nonblank environment value, otherwise the named file,
// otherwise the implicit working-directory file. Only the last may be absent.
func Read(value, file string) (raw, source string, err error) {
	if strings.TrimSpace(value) != "" {
		source = "TRUSTED_PROXIES"
	} else {
		source = strings.TrimSpace(file)
		implicit := source == ""
		if implicit {
			source = DefaultFile
		}
		data, e := os.ReadFile(source)
		if e != nil {
			if implicit && errors.Is(e, os.ErrNotExist) {
				return "", "", nil
			}
			return "", "", fmt.Errorf("cannot read TRUSTED_PROXIES_FILE %q: %w", source, e)
		}
		value = string(data)
	}
	if raw = Clean(value); raw == "" {
		return "", "", nil
	}
	return raw, source, nil
}

// Clean removes only comments, BOMs and whitespace, never invalid list entries.
func Clean(value string) string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "\ufeff")
		line, _, _ = strings.Cut(line, "#")
		line, _, _ = strings.Cut(line, "//")
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
