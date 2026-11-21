package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestExistingResourcePrefixShortGrantSurvivesButCannotBeNewlyAllocated(t *testing.T) {
	db, owner, _, item := shortAuthorizationFixture(t)
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/old-entry", DurationMinutes: 5}); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"css", "js", "img", "fonts", "adapter"} {
		path := "/" + prefix + "/private-entry"
		address, key := "https://muioi.com"+path, shortEntryAddressKey("muioi.com", path)
		// These paths were valid before the new registration policy. Model an
		// existing unexpired grant without bypassing the new-write validator.
		if err := reserveScriptAddress(db, address, scriptAddressShort, item.ID); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).Updates(map[string]any{"short_entry_address": address, "short_entry_address_key": key}).Error; err != nil {
			t.Fatal(err)
		}
		_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", path)
		if err != nil {
			t.Fatalf("existing /%s capability was broken on upgrade: %v", prefix, err)
		}
		if _, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token); err != nil {
			t.Fatalf("existing /%s ticket cannot exchange: %v", prefix, err)
		}
		if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: address, DurationMinutes: 5}); !errors.Is(err, ErrScriptReservedPrefix) {
			t.Fatalf("new grant used reserved /%s prefix: %v", prefix, err)
		}
	}
	path := "/api/private-entry"
	key := shortEntryAddressKey("muioi.com", path)
	if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).Updates(map[string]any{"short_entry_address": "https://muioi.com" + path, "short_entry_address_key": key}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", path); err == nil {
		t.Fatal("legacy exception admitted a real API prefix")
	}
}
