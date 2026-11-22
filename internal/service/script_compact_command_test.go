package service

import (
	"net/url"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestLongScriptCommandIsBearerLocatorAndLegacyCompatibilityRemainsAvailable(t *testing.T) {
	locator := "11111111-2222-4333-8444-555555555555"
	script := entity.UserScript{ShortCode: "fixture", AccessKey: "synthetic-private-key", Scheme: "https", BaseDomain: "fixture.invalid", LauncherLocator: &locator, UseWildcardDomain: true}
	if !strings.HasPrefix(BuildScriptCommand(script), "powershell -EncodedCommand ") {
		t.Fatal("previously public UUID locators must not become bearer credentials on upgrade")
	}
	script.LauncherBearerEnabled = true
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
	if err != nil || parsed.RawQuery != "" || strings.Contains(command, "X-Script-Launcher") {
		t.Fatalf("new long command must rely on the UUID address only: %v", err)
	}
	if strings.Contains(BuildScriptCommandURL(script), "?") {
		t.Fatal("public locator must not reveal full command authorization")
	}
	script.LauncherLocator = nil
	if !strings.HasPrefix(BuildScriptCommand(script), "powershell -EncodedCommand ") {
		t.Fatal("low entropy legacy short codes must retain header authorization")
	}
	legacy := BuildScriptCompatibilityCommand(script)
	if !strings.HasPrefix(legacy, "powershell -EncodedCommand ") {
		t.Fatal("PowerShell 2 header compatibility must remain available")
	}
}
