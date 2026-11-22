package main

import (
	"os"
	"strings"
	"testing"
)

func TestGatewayUsesMountedProxySource(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("TRUSTED_PROXIES_FILE", "")
	for _, tc := range []struct {
		name, content    string
		fail, restricted bool
	}{
		{"restricted default", "198.51.100.42\n", false, true},
		{"comments", "\ufeff# shipped template\r\n // comment\n", false, false},
		{"bad entry", "198.51.100.42\nbad\n", true, false},
		{"not just comments", "[]", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile("trusted_proxies.txt", []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			actual, err := loadGatewayProxyConfig()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			if err != nil {
				return
			}
			if strings.Contains(actual, "set_real_ip_from 198.51.100.42;") != tc.restricted || strings.Contains(actual, " 198.51.100.42/32 1;") != tc.restricted || strings.Contains(actual, "set_real_ip_from 0.0.0.0/0;") == tc.restricted {
				t.Fatal(actual)
			}
			if strings.Contains(actual, " 0.0.0.0/0 1;") {
				t.Fatal("client IP defaults must not grant protocol trust")
			}
		})
	}
}
