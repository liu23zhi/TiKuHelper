package controller

import (
	"strings"
	"testing"
)

func TestScriptLaunchersStayPowerShell2Compatible(t *testing.T) {
	bootstrap := buildScriptBootstrap("https://a.a.com", "a", "short-lived")
	for _, fragment := range []string{"New-Object Net.WebClient", "DownloadString", "X-Script-Token"} {
		if !strings.Contains(bootstrap, fragment) {
			t.Fatalf("bootstrap missing %q: %s", fragment, bootstrap)
		}
	}
	for _, incompatible := range []string{"Invoke-RestMethod", "ConvertFrom-Json", "ConvertTo-Json"} {
		if strings.Contains(bootstrap, incompatible) {
			t.Fatalf("PowerShell 2 bootstrap must not use %q: %s", incompatible, bootstrap)
		}
	}

	payload := buildScriptPayload("https://a.a.com", "a", "/adapter-service/script-command/a/file", "tk_secret", "eyJzZXR0aW5ncyI6e30=")
	for _, fragment := range []string{"DownloadFile", "CXPLUS_SCRIPT_BOOTSTRAP_B64", "Start-Process", "WaitForExit", "Remove-Item", "X-Script-Activation"} {
		if !strings.Contains(payload, fragment) {
			t.Fatalf("payload missing %q: %s", fragment, payload)
		}
	}
	for _, incompatible := range []string{"Invoke-RestMethod", "ConvertFrom-Json", "ConvertTo-Json"} {
		if strings.Contains(payload, incompatible) {
			t.Fatalf("PowerShell 2 payload must not use %q: %s", incompatible, payload)
		}
	}
}
