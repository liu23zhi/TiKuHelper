package service

import (
	"net/url"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestSearchProviderCallPermissionCannotBeBypassedByRequestToken(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	blocked := false
	if _, err := UpdateSearchAccountConfigWithDB(db, "enncy", UpdateSearchAccountConfigInput{Enabled: true, TokenValue: "default-secret", CallEnabled: &blocked}); err != nil {
		t.Fatal(err)
	}
	resolved, err := BuildSearchTokensWithDB(db, url.Values{"enncyToken": {"custom-secret"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Tokens["enncy"] != "" {
		t.Fatal("disabled channel leaked a usable token")
	}
	if len(resolved.DisabledProviders) != 1 || resolved.DisabledProviders[0] != "enncy" {
		t.Fatalf("permission snapshot missing: %+v", resolved)
	}
	allowed := true
	if _, err := UpdateSearchAccountConfigWithDB(db, "enncy", UpdateSearchAccountConfigInput{CallEnabled: &allowed}); err != nil {
		t.Fatal(err)
	}
	resolved, err = BuildSearchTokensWithDB(db, url.Values{"enncyToken": {"custom-secret"}}, true)
	if err != nil || resolved.Tokens["enncy"] != "custom-secret" {
		t.Fatalf("default credentials off must still allow client credentials: %+v %v", resolved, err)
	}
}

func TestAISearchLocalTariffUsesDefaultAccountAndIgnoresLegacyPayload(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if _, err := UpdateSearchAccountConfigWithDB(db, "local", UpdateSearchAccountConfigInput{Enabled: true, FlatPrice: 0.125}); err != nil {
		t.Fatal(err)
	}
	legacyPrice := 8.0
	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{TikuSearchPricePerCall: &legacyPrice})
	if err != nil {
		t.Fatal(err)
	}
	if view.TikuSearchPricePerCall != 0.125 || GetTikuSearchPricePerCallWithDB(db) != 0.125 {
		t.Fatalf("AI legacy payload overwrote unified tariff: %+v", view)
	}
	blocked := false
	if _, err := UpdateSearchAccountConfigWithDB(db, "ai", UpdateSearchAccountConfigInput{CallEnabled: &blocked}); err != nil {
		t.Fatal(err)
	}
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{"url": {"https://example.com"}, "key": {"custom"}, "model": {"model"}}, true)
	if err != nil || IsAISearchConfigUsable(resolved.Config) {
		t.Fatalf("disabled AI accepted explicit credentials: %+v %v", resolved, err)
	}
	if err := db.AutoMigrate(&entity.Tiku{}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateSearchAccountConfigWithDB(db, "local", UpdateSearchAccountConfigInput{CallEnabled: &blocked}); err != nil {
		t.Fatal(err)
	}
	if BuildAITikuSearchToolWithDB(db) != nil {
		t.Fatal("disabled local channel still exposed its AI tool")
	}
}

func TestLocalCallPermissionPreservesLegacyEnabledUntilExplicitOverride(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if _, err := UpdateSearchAccountConfigWithDB(db, "local", UpdateSearchAccountConfigInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if IsSearchProviderCallEnabledWithDB(db, "local") {
		t.Fatal("legacy local disable was ignored")
	}
	allowed := true
	if _, err := UpdateSearchAccountConfigWithDB(db, "local", UpdateSearchAccountConfigInput{Enabled: false, CallEnabled: &allowed}); err != nil {
		t.Fatal(err)
	}
	resolved, err := BuildSearchTokensWithDB(db, url.Values{"use": {"local"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range resolved.EnabledProviders {
		if provider == "local" {
			return
		}
	}
	t.Fatal("explicit local call permission must supersede legacy enabled value")
}
