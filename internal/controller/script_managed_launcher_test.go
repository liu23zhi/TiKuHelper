package controller

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/service"
)

func TestManagedScriptLauncherLimitsCredentialsAndVerifiesEXE(t *testing.T) {
	const origin = "https://fixture.invalid"
	const path = "/adapter-service/script-command/fixture/file"
	ticket := "synthetic-ticket"
	hash := strings.Repeat("a1", 32)
	payload, err := buildManagedScriptPayload(origin, path, hash, ticket, "fixture-config")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AllowAutoRedirect=$false", "X-Script-Activation", hash, "67108864", "CreateNew", "finally", "Remove-Item -LiteralPath $e"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("missing launcher guard %q", want)
		}
	}
	if strings.Contains(payload, origin+path+"?") || strings.Contains(payload, "wipe") {
		t.Fatal("managed launcher must not put a ticket in its URL or wipe unrelated data")
	}
	if _, err := buildManagedScriptPayload("http://fixture.invalid", path, hash, ticket, "fixture-config"); err == nil {
		t.Fatal("cleartext EXE transport should be rejected")
	}
	if _, err := buildManagedScriptPayload(origin, path, "invalid-digest", ticket, "fixture-config"); err == nil {
		t.Fatal("unknown EXE digest should be rejected")
	}
	bootstrap := service.BuildManagedScriptTextLoader(origin, service.ScriptLauncherHeader, "synthetic-owner-capability")
	if !strings.Contains(bootstrap, "AllowAutoRedirect=$false") {
		t.Fatal("bootstrap must reject redirect before carrying the owner capability")
	}
	// These are synthetic scripts only. An external fixture can parse their
	// PowerShell syntax without executing any downloads or child programs.
	t.Log("MANAGED_PAYLOAD_B64", base64.StdEncoding.EncodeToString([]byte(payload)))
	t.Log("MANAGED_BOOTSTRAP_B64", base64.StdEncoding.EncodeToString([]byte(bootstrap)))
}
