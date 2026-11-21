package service

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestCompactScriptCommandKeepsSeparateCapabilityAndRedirectProtection(t *testing.T) {
	locator := "11111111-2222-4333-8444-555555555555"
	script := entity.UserScript{ShortCode: "fixture", AccessKey: "synthetic-private-key", Scheme: "https", BaseDomain: "fixture.invalid", LauncherLocator: &locator, UseWildcardDomain: true}
	command := BuildScriptCommand(script)
	if len(command) > 280 || !strings.HasPrefix(command, `powershell -c "iex(irm https://`) || !strings.Contains(command, "-MaximumRedirection 0 -TimeoutSec 15") {
		t.Fatalf("new launcher should be compact with bounded non-redirecting transport: %s", command)
	}
	if strings.Contains(command, script.AccessKey) || strings.Contains(command, "$") {
		t.Fatal("compact command exposed runtime key or needs parent-shell variable expansion")
	}
	start := strings.Index(command, "https://")
	end := strings.Index(command[start:], " ")
	parsed, err := url.Parse(command[start : start+end])
	match := regexp.MustCompile(`-Headers @\{'X-Script-Launcher'='([A-Za-z0-9_-]{43})'\}`).FindStringSubmatch(command)
	if err != nil || parsed.RawQuery != "" || len(match) != 2 || match[1] != scriptLauncherCapability(script) {
		t.Fatalf("compact command must carry only its separate header capability: %v", err)
	}
	if strings.Contains(BuildScriptCommandURL(script), "?") {
		t.Fatal("public locator must not reveal full command authorization")
	}
	script.LauncherGeneration++
	if BuildScriptCommand(script) == command {
		t.Fatal("reconfigured authorization did not retire copied compact command")
	}
	script.LauncherLocator = nil
	if !strings.HasPrefix(BuildScriptCommand(script), `powershell -c "iex(irm `) {
		t.Fatal("existing scripts should also receive compact commands when re-read")
	}
	if !strings.HasPrefix(BuildScriptCompatibilityCommand(script), "powershell -EncodedCommand ") {
		t.Fatal("PowerShell 2 compatibility was lost")
	}
}
