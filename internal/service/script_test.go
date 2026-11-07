package service

import (
	"errors"
	"testing"

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
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.ScriptVersion{}, &entity.UserScript{}, &entity.ScriptExchangeToken{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestScriptCreateExchangeAndDeleteRefundAreTransactionalAndIdempotent(t *testing.T) {
	db := newScriptTestDB(t)
	owner := entity.User{Username: "student", Role: "user", Balance: 10}
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
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
	if fresh.Balance != 0 {
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
	if result.AccessKey == "" || result.Parameters["course"] != "math" {
		t.Fatalf("unexpected exchange result: %+v", result)
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
	if fresh.Balance != 10 {
		t.Fatalf("expected idempotent refund to restore 10, got %v", fresh.Balance)
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

func TestScriptCommandsAllocateShortestCodesAndResolveBothDomainModes(t *testing.T) {
	db := newScriptTestDB(t)
	admin := entity.User{Username: "admin", Role: "admin", Balance: 100}
	owner := entity.User{Username: "student", Role: "user", Balance: 100}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
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
	if first.ShortCode != "a" || first.Command != `powershell -c "iex (New-Object Net.WebClient).DownloadString('https://a.a.com')"` {
		t.Fatalf("expected shortest wildcard command, got code=%q command=%q", first.ShortCode, first.Command)
	}

	nonWildcard := false
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "path", VersionID: version.ID, BaseDomain: "a.com", UseWildcardDomain: &nonWildcard})
	if err != nil {
		t.Fatal(err)
	}
	if second.ShortCode != "b" || second.Command != `powershell -c "iex (New-Object Net.WebClient).DownloadString('https://b.a.com/b')"` {
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
	grant, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, ScriptExchangeTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessKey == "" || result.DownloadURL != "/adapter-service/script-command/a/file" {
		t.Fatalf("unexpected public exchange result: %+v", result)
	}
	if _, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("expected one-time token rejection, got %v", err)
	}
}
