package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestConsumedActivationScriptListClearsStartupWithoutDisablingCurrentRun(t *testing.T) {
	db, owner, _, version := newManagedScriptFixture(t, 200)
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "once", VersionID: version.ID, LauncherOrigin: &ScriptLauncherOrigin{Host: "fixture.invalid", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).UpdateColumn("billing_mode", entity.ScriptBillingModeActivation).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&entity.ScriptActivationEntitlement{UserID: owner.ID, ScriptID: &item.ID, RunConsumedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := ListUserScriptsWithDB(db, owner, false)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	if rows[0].Command != "" || rows[0].DownloadURL != "" || rows[0].LauncherAuthorization.Status != "consumed" {
		t.Fatalf("consumed one-use entry still shown available: %+v", rows[0])
	}
	if !rows[0].Enabled {
		t.Fatal("presentation disabled the script and would terminate its existing client")
	}
	updated, err := UpdateUserScriptWithDB(db, owner, item.ID, UpdateUserScriptInput{Name: "renamed"})
	if err != nil || updated.Command != "" || updated.LauncherAuthorization.Status != "consumed" {
		t.Fatalf("metadata update restored consumed entry: %v", err)
	}
	replayed, err := replayScriptCreationReceiptWithDB(db, owner.ID, entity.IdempotencyRecord{Status: IdempotencyStatusSucceeded, ResponseJSON: fmt.Sprintf(`{"ID":%d}`, item.ID)})
	if err != nil || replayed.RunConsumedAt == nil || replayed.Command != "" || replayed.LauncherAuthorization.Status != "consumed" {
		t.Fatalf("creation receipt replay restored consumed entry: %v", err)
	}
}
