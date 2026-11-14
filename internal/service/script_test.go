package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newScriptTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:script-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.ScriptVersion{}, &entity.UserScript{}, &entity.ScriptExchangeToken{}, &entity.ScriptRunSession{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestScriptCreateExchangeAndDeleteRefundAreTransactionalAndIdempotent(t *testing.T) {
	db := newScriptTestDB(t)
	owner := entity.User{Username: "student", Role: "user", Balance: 20}
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "2026.11", "answer.exe", []byte("MZbinary"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "my script", VersionID: version.ID, BaseDomain: "a.com", Parameters: map[string]any{"course": "math", "retry": 2}})
	if err != nil {
		t.Fatal(err)
	}
	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Balance != 10 {
		t.Fatalf("expected reservation to deduct 10, got %v", fresh.Balance)
	}
	if created.AccessTokenID == 0 {
		t.Fatal("expected a dedicated access token")
	}
	grant, err := IssueScriptExchangeTokenWithDB(db, owner, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if result.ActivationToken == "" || result.ActivationExpiresAt.IsZero() || result.Parameters["course"] != "math" {
		t.Fatalf("unexpected exchange result: %+v", result)
	}
	session, err := ActivateScriptRunSessionWithDB(db, grant.ShortCode, result.ActivationToken, "identity-1")
	if err != nil {
		t.Fatalf("activate run session: %v", err)
	}
	if session.Token == "" || session.ExpiresAt.Before(time.Now().Add(149*time.Minute)) {
		t.Fatalf("unexpected run session grant: %+v", session)
	}
	if _, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("expected one-time token rejection, got %v", err)
	}
	if err := DeleteUserScriptWithDB(db, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteUserScriptWithDB(db, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Balance != 20 {
		t.Fatalf("expected idempotent refund to restore 20, got %v", fresh.Balance)
	}
	var token entity.AccessToken
	if err := db.First(&token, created.AccessTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if token.Status != "disabled" {
		t.Fatalf("expected script token disabled, got %q", token.Status)
	}
	var records []entity.BillingRecord
	if err := db.Where("user_id = ?", owner.ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Type == records[1].Type {
		t.Fatalf("expected reservation and refund records, got %+v", records)
	}
}

func TestScriptDeleteWorksWhenBalanceIsInsufficient(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 10}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "x.exe", []byte("MZx"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "x", VersionID: version.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", owner.ID).Update("balance", -100).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteUserScriptWithDB(db, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Balance != -90 {
		t.Fatalf("expected refund despite negative balance, got %v", fresh.Balance)
	}
}

func TestScriptRefundIsIncomeAndDoesNotIncreaseExpenseSummary(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 10}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "x.exe", []byte("MZx"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "x", VersionID: version.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteUserScriptWithDB(db, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Summary.TotalAmount != ScriptReservationFee {
		t.Fatalf("script refund must be income outside expense summary: %+v", result)
	}
}

func TestUploadScriptVersionRejectsNonExecutableAndKeepsCurrentVersion(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	first, err := UploadScriptVersionWithDB(db, admin, "v1", "first.exe", []byte("MZfirst"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UploadScriptVersionWithDB(db, admin, "v2", "bad.txt", []byte("MZbad")); !errors.Is(err, ErrScriptVersionInvalid) {
		t.Fatalf("expected exe validation error, got %v", err)
	}
	second, err := UploadScriptVersionWithDB(db, admin, "v2", "second.exe", []byte("MZsecond"))
	if err != nil {
		t.Fatal(err)
	}
	var current []entity.ScriptVersion
	if err := db.Where("is_current = ?", true).Find(&current).Error; err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0].ID != second.ID {
		t.Fatalf("expected one current version, got %+v", current)
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("expected distinct executable hashes")
	}
}

func TestScriptExchangeTicketKeepsVersionAcrossUpload(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 20}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	first, err := UploadScriptVersionWithDB(db, admin, "v1", "first.exe", []byte("MZfirst"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "answer", VersionID: first.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	oldGrant, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := UploadScriptVersionWithDB(db, admin, "v2", "second.exe", []byte("MZsecond"))
	if err != nil {
		t.Fatal(err)
	}
	oldExchange, err := ExchangeScriptTokenWithDB(db, item.ShortCode, oldGrant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if oldExchange.Version != first.Version || oldExchange.Filename != first.Filename {
		t.Fatalf("old exchange ticket switched to new version: %+v", oldExchange)
	}
	oldDownload, err := DownloadScriptVersionByActivationTokenWithDB(db, item.ShortCode, oldExchange.ActivationToken)
	if err != nil || oldDownload.ID != first.ID {
		t.Fatalf("old activation ticket did not keep v1: version=%d err=%v", oldDownload.ID, err)
	}
	newGrant, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), 0)
	if err != nil {
		t.Fatal(err)
	}
	newExchange, err := ExchangeScriptTokenWithDB(db, item.ShortCode, newGrant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if newExchange.Version != second.Version || newExchange.Filename != second.Filename {
		t.Fatalf("new exchange ticket did not use v2: %+v", newExchange)
	}
}

func TestScriptCommandsAllocateShortestCodesAndResolveBothDomainModes(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 100}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "answer.exe", []byte("MZbinary"))
	if err != nil {
		t.Fatal(err)
	}
	wildcard := true
	first, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "wild", VersionID: version.ID, BaseDomain: "a.com", UseWildcardDomain: &wildcard})
	if err != nil {
		t.Fatal(err)
	}
	if first.ShortCode != "a" || first.DownloadURL != "https://a.a.com" || !strings.HasPrefix(first.Command, "powershell -EncodedCommand ") {
		t.Fatalf("expected shortest wildcard command, got code=%q command=%q", first.ShortCode, first.Command)
	}

	nonWildcard := false
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "path", VersionID: version.ID, BaseDomain: "a.com", UseWildcardDomain: &nonWildcard})
	if err != nil {
		t.Fatal(err)
	}
	if second.ShortCode != "b" || second.DownloadURL != "https://b.a.com/b" || !strings.HasPrefix(second.Command, "powershell -EncodedCommand ") {
		t.Fatalf("expected shortest path command, got code=%q command=%q", second.ShortCode, second.Command)
	}
	if _, err := ResolvePublicScriptWithDB(db, "a.a.com:443", "/"); err != nil {
		t.Fatalf("wildcard host should resolve: %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "b.a.com", "/b"); err != nil {
		t.Fatalf("path host should resolve: %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "b.a.com", "/"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("path mode root should not resolve, got %v", err)
	}
}

func TestPublicScriptExchangeTokenIsShortLivedAndOneTime(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 20}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "answer.exe", []byte("MZbinary"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "answer", VersionID: version.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), ScriptExchangeTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if result.ActivationToken == "" || result.DownloadURL != "/api/script-command/a/file" {
		t.Fatalf("unexpected public exchange result: %+v", result)
	}
	if _, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("expected one-time token rejection, got %v", err)
	}
}

func TestScriptExchangeTokenRequiresNineAvailableBalance(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 19}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "answer.exe", []byte("MZbinary"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "answer", VersionID: version.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), ScriptExchangeTokenTTL); err != nil {
		t.Fatalf("balance exactly 9 should issue a token: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", owner.ID).Update("balance", 8.999999).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), ScriptExchangeTokenTTL); !errors.Is(err, ErrScriptTokenBalanceInsufficient) {
		t.Fatalf("balance below 9 must reject token issuance, got %v", err)
	}
}

func TestScriptRunSessionIsIdentityBoundShortLivedAndStoresOnlyHashes(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 20}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "answer.exe", []byte("MZbinary"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "answer", VersionID: version.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), ScriptExchangeTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if exchange.ActivationToken == "" {
		t.Fatal("exchange must return an activation ticket")
	}
	// Once the one-time ticket has been issued, the runtime session remains
	// usable even if subsequent answer billing drives the account negative.
	if err := db.Model(&owner).Update("balance", -15).Error; err != nil {
		t.Fatal(err)
	}
	run, err := ActivateScriptRunSessionWithDB(db, item.ShortCode, exchange.ActivationToken, "machine-identity")
	if err != nil {
		t.Fatalf("activate run session with debt: %v", err)
	}
	if run.Token == "" || !run.ExpiresAt.After(time.Now().Add(149*time.Minute)) || !run.ExpiresAt.Before(time.Now().Add(151*time.Minute)) {
		t.Fatalf("expected a 150-minute session, got %+v", run)
	}
	var stored entity.ScriptRunSession
	if err := db.Where("script_id = ?", item.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == run.Token || stored.IdentityHash == "machine-identity" || stored.TokenHash == "" || stored.IdentityHash == "" {
		t.Fatal("session secrets must only be persisted as hashes")
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, "Bearer "+run.Token, "different-machine"); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected identity mismatch rejection, got %v", err)
	}
	context, resolved, err := ResolveScriptRunSessionWithDB(db, "Bearer "+run.Token, "machine-identity")
	if err != nil {
		t.Fatalf("resolve valid session: %v", err)
	}
	if context.User.ID != owner.ID || context.AccessToken.ID != item.AccessTokenID || resolved.ScriptID != item.ID {
		t.Fatalf("session binding was not preserved: user=%d token=%d session=%+v", context.User.ID, context.AccessToken.ID, resolved)
	}
	download, err := DownloadScriptVersionByRunSessionWithDB(db, item.ShortCode, "Bearer "+run.Token, "machine-identity")
	if err != nil || download.ID != version.ID {
		t.Fatalf("download with run session: version=%d err=%v", download.ID, err)
	}
	if _, err := ActivateScriptRunSessionWithDB(db, item.ShortCode, exchange.ActivationToken, "machine-identity"); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("activation ticket must be single-use, got %v", err)
	}
	if err := db.Model(&entity.ScriptRunSession{}).Where("id = ?", stored.ID).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, run.Token, "machine-identity"); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected expired session rejection, got %v", err)
	}
}

func TestScriptExchangeTokenRequiresNineBalanceAndRejectsDuplicateOutstandingGrant(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 20}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	bindSyntheticScriptOwner(&owner)
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "v1", "x.exe", []byte("MZx"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "x", VersionID: version.ID, BaseDomain: "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if grant, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0); err != nil || grant.Token == "" {
		t.Fatalf("expected first grant with ¥10 spendable balance, grant=%+v err=%v", grant, err)
	}
	if _, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0); !errors.Is(err, ErrScriptExchangeAlreadyIssued) {
		t.Fatalf("expected duplicate outstanding grant rejection, got %v", err)
	}
	var exchange entity.ScriptExchangeToken
	if err := db.Where("script_id = ? AND purpose = ?", item.ID, "exchange").First(&exchange).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&exchange).Update("used_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", owner.ID).Update("balance", 8).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0); !errors.Is(err, ErrScriptTokenBalanceInsufficient) {
		t.Fatalf("expected ¥9 balance floor rejection, got %v", err)
	}
}
