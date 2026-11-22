package service

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestScriptShortAddressConcurrentClaimIsExclusiveAcrossAccounts(t *testing.T) {
	dsn := util.SQLiteProductionDSN(util.SQLiteConcurrentDSN(filepath.Join(t.TempDir(), "script-address.db")))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.ScriptVersion{}, &entity.UserScript{}, &entity.ScriptAddressReservation{}, &entity.ScriptExchangeToken{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.zelly.cn"}}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{Username: "claim-admin", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "claim-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	type claimant struct {
		owner   entity.User
		before  entity.UserScript
		oldHost string
		input   string
	}
	claimants := []claimant{
		{owner: entity.User{Username: "claim-one", Role: "user", Balance: 30}, oldHost: "one.zelly.cn", input: "a.zelly.cn"},
		{owner: entity.User{Username: "claim-two", Role: "user", Balance: 30}, oldHost: "two.zelly.cn", input: " https://A.ZELLY.CN.:443/ "},
	}
	for i := range claimants {
		claim := &claimants[i]
		bindSyntheticScriptOwner(&claim.owner)
		phone, wechat := []string{"13800000001", "13800000002"}[i], claim.owner.Username
		claim.owner.Phone, claim.owner.WechatID = &phone, &wechat
		if err := db.Create(&claim.owner).Error; err != nil {
			t.Fatal(err)
		}
		item, err := CreateUserScriptWithDB(db, claim.owner, CreateUserScriptInput{Name: claim.owner.Username, VersionID: version.ID, BaseDomain: claim.oldHost})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ConfigureScriptShortAuthorizationWithDB(db, claim.owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: claim.oldHost, DurationMinutes: 10, MaxUses: 3}); err != nil {
			t.Fatal(err)
		}
		_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, claim.oldHost, "/")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token); err != nil {
			t.Fatal(err)
		}
		if err := db.First(&claim.before, item.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	type claimResult struct {
		index int
		view  ScriptShortAuthorizationView
		err   error
	}
	ready, start := make(chan struct{}, 2), make(chan struct{})
	results := make(chan claimResult, 2)
	for i, claim := range claimants {
		go func() {
			ready <- struct{}{}
			<-start
			view, err := ConfigureScriptShortAuthorizationWithDB(db, claim.owner, claim.before.ID,
				ConfigureScriptShortAuthorizationInput{Enabled: true, Address: claim.input, DurationMinutes: 15, MaxUses: 6})
			results <- claimResult{index: i, view: view, err: err}
		}()
	}
	<-ready
	<-ready
	close(start)
	winner, loser := -1, -1
	for range claimants {
		result := <-results
		switch {
		case result.err == nil:
			if result.view.Address != "https://a.zelly.cn" || result.view.MaxUses != 6 || result.view.UsedCount != 0 {
				t.Fatalf("winning authorization has wrong address or budget: %+v", result.view)
			}
			winner = result.index
		case errors.Is(result.err, ErrScriptShortAddressInUse):
			loser = result.index
		default:
			t.Fatalf("claim %d returned %v, expected a business conflict", result.index, result.err)
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatalf("expected one winner and one address conflict, winner=%d loser=%d", winner, loser)
	}
	winningClaim, losingClaim := claimants[winner], claimants[loser]
	var after entity.UserScript
	if err := db.First(&after, losingClaim.before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(losingClaim.before, after) {
		t.Fatalf("declined claim changed existing script %d", after.ID)
	}
	var reservation entity.ScriptAddressReservation
	if err := db.First(&reservation, "address_key = ?", shortEntryAddressKey("a.zelly.cn", "")).Error; err != nil || reservation.ScriptID != winningClaim.before.ID {
		t.Fatalf("normalized address belongs to %d, wanted %d: %v", reservation.ScriptID, winningClaim.before.ID, err)
	}
	view, err := ConfigureScriptShortAuthorizationWithDB(db, winningClaim.owner, winningClaim.before.ID, ConfigureScriptShortAuthorizationInput{
		Enabled: true, Address: winningClaim.input, DurationMinutes: 20, MaxUses: 7,
	})
	if err != nil || view.Address != "https://a.zelly.cn" || view.MaxUses != 7 {
		t.Fatalf("winner could not update its live address: %+v, %v", view, err)
	}
	for _, route := range []struct {
		host string
		id   int32
	}{
		{"a.zelly.cn", winningClaim.before.ID},
		{losingClaim.oldHost, losingClaim.before.ID},
	} {
		resolved, _, err := ConsumeScriptShortAuthorizationWithDB(db, route.host, "/")
		if err != nil || resolved.ID != route.id {
			t.Fatalf("address %s resolved to %d, wanted %d: %v", route.host, resolved.ID, route.id, err)
		}
	}
}
