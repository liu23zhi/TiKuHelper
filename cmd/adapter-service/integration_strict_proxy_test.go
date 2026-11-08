package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIntegrationStrictProxyPolicy(t *testing.T) {
	cases := []struct {
		name, raw string
		want      []string
		fail      bool
	}{
		{name: "missing", fail: true}, {name: "none", raw: "none"}, {name: "exact_ip", raw: "192.0.2.9", want: []string{"192.0.2.9"}},
		{name: "partial_invalid", raw: "192.0.2.9,not-an-ip", fail: true}, {name: "wildcard", raw: "*", fail: true},
		{name: "whole_v4", raw: "0.0.0.0/0", fail: true}, {name: "whole_v6", raw: "::/0", fail: true},
		{name: "whole_v4_union", raw: "0.0.0.0/1,128.0.0.0/1", fail: true},
		{name: "mapped_whole_v4", raw: "::ffff:0.0.0.0/96", fail: true},
		{name: "netmask", raw: "192.0.2.0/255.255.255.0", want: []string{"192.0.2.0/24"}},
		{name: "rich_list", raw: "trusted_proxies: [192.0.2.9, 198.51.100.0/24]", want: []string{"192.0.2.9", "198.51.100.0/24"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TIKU_APP_MODE", "")
			t.Setenv(trustedProxiesEnv, c.raw)
			t.Setenv(trustedProxiesFileEnv, "")
			got, err := loadTrustedProxies()
			if (err != nil) != c.fail {
				t.Fatalf("expected failure=%v got=%v config=%v", c.fail, err, got)
			}
			if !c.fail && !reflect.DeepEqual(got.List, c.want) {
				t.Fatalf("explicit list changed: got %v want %v", got.List, c.want)
			}
		})
	}
	t.Run("explicit_file", func(t *testing.T) {
		t.Setenv("TIKU_APP_MODE", "production")
		t.Setenv(trustedProxiesEnv, "")
		p := filepath.Join(t.TempDir(), "proxy.txt")
		if err := os.WriteFile(p, []byte("192.0.2.9\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(trustedProxiesFileEnv, p)
		got, err := loadTrustedProxies()
		if err != nil || !reflect.DeepEqual(got.List, []string{"192.0.2.9"}) {
			t.Fatalf("explicit file failed: %v %+v", err, got)
		}
	})
	t.Run("missing_explicit_file", func(t *testing.T) {
		t.Setenv("TIKU_APP_MODE", "production")
		t.Setenv(trustedProxiesEnv, "")
		t.Setenv(trustedProxiesFileEnv, filepath.Join(t.TempDir(), "missing.txt"))
		if _, err := loadTrustedProxies(); err == nil {
			t.Fatal("missing explicit file must fail")
		}
	})
	t.Run("development_legacy_explicit", func(t *testing.T) {
		t.Setenv("TIKU_APP_MODE", "development")
		t.Setenv(trustedProxiesEnv, "")
		t.Setenv(trustedProxiesFileEnv, filepath.Join(t.TempDir(), "missing.txt"))
		got, err := loadTrustedProxies()
		if err != nil || !reflect.DeepEqual(got.List, []string{trustAllProxiesIPv4, trustAllProxiesIPv6}) {
			t.Fatal("explicit dev compatibility changed")
		}
	})
}
func TestIntegrationProxyBeforeManager(t *testing.T) {
	f, e := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	var proxy, manager token.Pos
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "loadTrustedProxies" {
				proxy = c.Pos()
			}
			if s, ok := c.Fun.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "manager" && s.Sel.Name == "CreateManager" {
					manager = c.Pos()
				}
			}
			return true
		})
	}
	if proxy == 0 || manager == 0 || proxy >= manager {
		t.Fatal("proxy validation must precede manager/database initialization")
	}
}
func TestIntegrationProxyRangeChild(t *testing.T) {
	raw := os.Getenv("QA_PROXY_RANGE")
	if raw == "" {
		t.Skip("subprocess helper")
	}
	values, invalid := parseTrustedProxyEntries(raw)
	b, _ := json.Marshal(struct {
		Entries []string
		Invalid []string
	}{values, invalid})
	os.Stdout.Write(append([]byte("QA_RANGE="), append(b, '\n')...))
}
func TestIntegrationProxyUpperRangeTerminates(t *testing.T) {
	for _, raw := range []string{"0.0.0.0-255.255.255.255", "255.255.255.254-255.255.255.255"} {
		t.Run(raw, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIntegrationProxyRangeChild$")
			c.Env = append(os.Environ(), "QA_PROXY_RANGE="+raw)
			out, e := c.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("IPv4 upper-bound range parser failed to terminate")
			}
			if e != nil || !strings.Contains(string(out), "QA_RANGE=") {
				t.Fatalf("child failed %v %s", e, out)
			}
		})
	}
}
