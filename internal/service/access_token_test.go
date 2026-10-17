package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newAccessTokenTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:access-token-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func seedAccessTokenOwner(t *testing.T, db *gorm.DB, id int32, role string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       "user-" + role,
		Password:       "123456",
		Role:           role,
		Status:         "enabled",
		RegisterSource: "admin",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func TestCreateAccessTokenWithDBStoresPlainTokenForDisplay(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner := seedAccessTokenOwner(t, db, 1, "user")

	created, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{
		Name:            " 默认令牌 ",
		Remark:          " 首次创建 ",
		TotalQuotaLimit: 12.5,
	})
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}

	if strings.TrimSpace(created.Token) == "" {
		t.Fatal("expected plain token to be returned once")
	}
	if created.AccessToken.Name != "默认令牌" {
		t.Fatalf("expected trimmed token name, got %s", created.AccessToken.Name)
	}
	if created.AccessToken.Remark != "首次创建" {
		t.Fatalf("expected trimmed token remark, got %s", created.AccessToken.Remark)
	}
	if created.AccessToken.Status != "enabled" {
		t.Fatalf("expected enabled status, got %s", created.AccessToken.Status)
	}
	if created.AccessToken.TotalQuotaLimit != 12.5 {
		t.Fatalf("expected quota 12.5, got %v", created.AccessToken.TotalQuotaLimit)
	}
	if created.AccessToken.TokenHash == created.Token {
		t.Fatal("expected stored token hash instead of plain token")
	}
	if !strings.HasPrefix(created.Token, created.AccessToken.TokenPrefix) {
		t.Fatalf("expected token prefix %s to match plain token %s", created.AccessToken.TokenPrefix, created.Token)
	}

	var stored entity.AccessToken
	if err := db.Where("id = ?", created.AccessToken.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored access token: %v", err)
	}
	if stored.TokenHash == "" || stored.TokenHash == created.Token {
		t.Fatalf("expected hashed token to be stored, got %s", stored.TokenHash)
	}
	if stored.UserID != owner.ID {
		t.Fatalf("expected token owner %d, got %d", owner.ID, stored.UserID)
	}

	var storedDisplayValue struct {
		TokenValue string `gorm:"column:token_value"`
	}
	if err := db.Table(entity.TableNameAccessToken).Select("token_value").Where("id = ?", created.AccessToken.ID).Scan(&storedDisplayValue).Error; err != nil {
		t.Fatalf("load stored token display value: %v", err)
	}
	if storedDisplayValue.TokenValue != created.Token {
		t.Fatalf("expected stored token display value %s, got %s", created.Token, storedDisplayValue.TokenValue)
	}
}

func TestCreateAccessTokenWithDBRejectsInvalidAndOverlongNames(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner := seedAccessTokenOwner(t, db, 1, "user")

	for _, name := range []string{
		"https://example.com/course?id=1",
		"token-name",
		strings.Repeat("令", 129),
	} {
		if _, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: name}); err == nil {
			t.Errorf("expected invalid token name to be rejected: %q", name)
		}
	}

	created, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: strings.Repeat("令", 128)})
	if err != nil {
		t.Fatalf("expected 128-character token name to be accepted: %v", err)
	}
	if len([]rune(created.AccessToken.Name)) != 128 {
		t.Fatalf("expected 128-character name, got %d", len([]rune(created.AccessToken.Name)))
	}
}

func TestListAccessTokensWithDBAlwaysScopesToOperatorAndReturnsStoredTokenValue(t *testing.T) {
	db := newAccessTokenTestDB(t)
	admin := seedAccessTokenOwner(t, db, 1, "admin")
	owner := seedAccessTokenOwner(t, db, 2, "user")
	other := seedAccessTokenOwner(t, db, 3, "user")

	adminToken, err := CreateAccessTokenWithDB(db, admin, CreateAccessTokenInput{Name: "admin_token"})
	if err != nil {
		t.Fatalf("create admin token: %v", err)
	}
	ownerToken, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: "owner_token"})
	if err != nil {
		t.Fatalf("create owner token: %v", err)
	}
	otherToken, err := CreateAccessTokenWithDB(db, other, CreateAccessTokenInput{Name: "other_token"})
	if err != nil {
		t.Fatalf("create other token: %v", err)
	}

	ownerList, err := ListAccessTokensWithDB(db, owner)
	if err != nil {
		t.Fatalf("list owner tokens: %v", err)
	}
	if len(ownerList) != 1 || ownerList[0].ID != ownerToken.AccessToken.ID {
		t.Fatalf("expected owner to see only own token, got %+v", ownerList)
	}
	ownerPayload, err := json.Marshal(ownerList[0])
	if err != nil {
		t.Fatalf("marshal owner token payload: %v", err)
	}
	if !strings.Contains(string(ownerPayload), ownerToken.Token) || !strings.Contains(string(ownerPayload), "tokenValue") {
		t.Fatalf("expected owner token payload to include stored token value, got %s", string(ownerPayload))
	}

	adminList, err := ListAccessTokensWithDB(db, admin)
	if err != nil {
		t.Fatalf("list admin tokens: %v", err)
	}
	if len(adminList) != 1 || adminList[0].ID != adminToken.AccessToken.ID {
		t.Fatalf("expected admin to see only own token by default, got %+v", adminList)
	}
	if adminList[0].ID == ownerToken.AccessToken.ID || adminList[0].ID == otherToken.AccessToken.ID {
		t.Fatalf("expected admin default list not to include other users' tokens, got %+v", adminList)
	}
}

func TestListAllAccessTokensWithDBRequiresAdminAndReturnsUsername(t *testing.T) {
	db := newAccessTokenTestDB(t)
	admin := seedAccessTokenOwner(t, db, 1, "admin")
	owner := seedAccessTokenOwner(t, db, 2, "user")
	other := seedAccessTokenOwner(t, db, 3, "user")

	if _, err := CreateAccessTokenWithDB(db, admin, CreateAccessTokenInput{Name: "admin_token"}); err != nil {
		t.Fatalf("create admin token: %v", err)
	}
	if _, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: "owner_token"}); err != nil {
		t.Fatalf("create owner token: %v", err)
	}
	if _, err := CreateAccessTokenWithDB(db, other, CreateAccessTokenInput{Name: "other_token"}); err != nil {
		t.Fatalf("create other token: %v", err)
	}

	userList, err := ListAllAccessTokensWithDB(db, owner)
	if err != nil {
		t.Fatalf("list all tokens as user: %v", err)
	}
	if len(userList) != 1 || userList[0].Name != "owner-token" {
		t.Fatalf("expected non-admin all-scope request to remain scoped to self, got %+v", userList)
	}

	adminList, err := ListAllAccessTokensWithDB(db, admin)
	if err != nil {
		t.Fatalf("list all tokens as admin: %v", err)
	}
	if len(adminList) != 3 {
		t.Fatalf("expected admin all-scope request to return every token, got %+v", adminList)
	}
	usernames := make(map[string]string, len(adminList))
	for _, token := range adminList {
		usernames[token.Name] = token.Username
	}
	if usernames["admin-token"] != admin.Username || usernames["owner-token"] != owner.Username || usernames["other-token"] != other.Username {
		t.Fatalf("expected all-scope tokens to include owner usernames, got %+v", usernames)
	}
}

func TestUpdateAccessTokenWithDBRejectsNonOwnerAndUpdatesAllowedFields(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner := seedAccessTokenOwner(t, db, 2, "user")
	other := seedAccessTokenOwner(t, db, 3, "user")

	created, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: "token_a"})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if _, err := UpdateAccessTokenWithDB(db, other, created.AccessToken.ID, UpdateAccessTokenInput{Name: "forbidden"}); err == nil {
		t.Fatal("expected non-owner update to fail")
	}

	updated, err := UpdateAccessTokenWithDB(db, owner, created.AccessToken.ID, UpdateAccessTokenInput{
		Name:            " token-b ",
		Status:          "disabled",
		Remark:          " manual stop ",
		TotalQuotaLimit: 20,
	})
	if err != nil {
		t.Fatalf("update token: %v", err)
	}
	if updated.Name != "token-b" {
		t.Fatalf("expected updated name token-b, got %s", updated.Name)
	}
	if updated.Status != "disabled" {
		t.Fatalf("expected updated status disabled, got %s", updated.Status)
	}
	if updated.Remark != "manual stop" {
		t.Fatalf("expected updated remark manual stop, got %s", updated.Remark)
	}
	if updated.TotalQuotaLimit != 20 {
		t.Fatalf("expected updated quota 20, got %v", updated.TotalQuotaLimit)
	}
}

func TestUpdateAccessTokenWithDBRejectsInvalidAndOverlongNames(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner := seedAccessTokenOwner(t, db, 2, "user")
	created, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: "token_a"})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	for _, name := range []string{
		"https://example.com/course?id=1",
		"token-name",
		strings.Repeat("令", 129),
	} {
		if _, err := UpdateAccessTokenWithDB(db, owner, created.AccessToken.ID, UpdateAccessTokenInput{Name: name}); err == nil {
			t.Errorf("expected invalid token name to be rejected: %q", name)
		}
	}

	var stored entity.AccessToken
	if err := db.First(&stored, created.AccessToken.ID).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	if stored.Name != "token_a" {
		t.Fatalf("invalid updates changed token name to %q", stored.Name)
	}
}

func TestDeleteAccessTokenWithDBRejectsNonOwnerAndRemovesOwnedToken(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner := seedAccessTokenOwner(t, db, 2, "user")
	other := seedAccessTokenOwner(t, db, 3, "user")

	created, err := CreateAccessTokenWithDB(db, owner, CreateAccessTokenInput{Name: "token_a"})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if err := DeleteAccessTokenWithDB(db, other, created.AccessToken.ID); err == nil {
		t.Fatal("expected non-owner delete to fail")
	}

	if err := DeleteAccessTokenWithDB(db, owner, created.AccessToken.ID); err != nil {
		t.Fatalf("delete token: %v", err)
	}

	var count int64
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", created.AccessToken.ID).Count(&count).Error; err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected token to be deleted, count=%d", count)
	}
}
