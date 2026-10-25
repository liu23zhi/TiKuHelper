package manager

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationConfigStartupChild(t *testing.T) {
	if os.Getenv("QA_CONFIG_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	m, e := CreateManager()
	if m != nil {
		defer m.CloseManager()
	}
	_, s := os.Stat("tiku.db")
	b, _ := json.Marshal(struct{ Failed, Created bool }{e != nil, s == nil})
	os.Stdout.Write(append([]byte("QA_CONFIG="), append(b, '\n')...))
}
func TestIntegrationConfigStartupBeforeDB(t *testing.T) {
	for _, c := range []struct {
		name, raw     string
		missing, fail bool
	}{{name: "invalid_yaml", raw: "limit: [\n", fail: true}, {name: "invalid_type", raw: "limit:\n  duration: [1,2]\n", fail: true}, {name: "missing_uses_defaults", missing: true}, {name: "valid", raw: "limit:\n  requests: 120\n"}} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if !c.missing {
				if e := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(c.raw), 0600); e != nil {
					t.Fatal(e)
				}
			}
			p := exec.Command(os.Args[0], "-test.run=^TestIntegrationConfigStartupChild$")
			p.Dir = dir
			p.Env = append(os.Environ(), "QA_CONFIG_CHILD=1", "TIKU_APP_MODE=production", "TIKU_ADMIN_PASSWORD=QaSynthetic246!", "SQL_DSN=")
			out, e := p.CombinedOutput()
			if e != nil {
				t.Fatalf("child exit %v output=%s", e, out)
			}
			var result struct{ Failed, Created bool }
			found := false
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "QA_CONFIG=") {
					if e := json.Unmarshal([]byte(strings.TrimPrefix(line, "QA_CONFIG=")), &result); e != nil {
						t.Fatal(e)
					}
					found = true
				}
			}
			if !found || result.Failed != c.fail || result.Created == c.fail {
				t.Fatalf("expected failure=%v no DB on error; got %+v output=%s", c.fail, result, out)
			}
		})
	}
}
