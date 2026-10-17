package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newSearchAccountConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.SearchAccountConfig{}, &entity.AISearchConfig{}, &entity.User{}, &entity.AccessToken{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func TestListSearchAccountConfigsWithDBReturnsSupportedProviders(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	configs, err := ListSearchAccountConfigsWithDB(db)
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}

	if len(configs) != 8 {
		t.Fatalf("expected 8 providers, got %d", len(configs))
	}

	if configs[0].Provider != "icodef" {
		t.Fatalf("expected first provider to be icodef, got %s", configs[0].Provider)
	}

	if configs[0].TokenKey != "icodefToken" {
		t.Fatalf("expected icodef token key, got %s", configs[0].TokenKey)
	}
}

func TestListSearchAccountConfigsWithDBIncludesLocalProvider(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	configs, err := ListSearchAccountConfigsWithDB(db)
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}

	for _, item := range configs {
		if item.Provider != "local" {
			continue
		}
		if item.ProviderLabel != "本地题库" {
			t.Fatalf("expected local provider label 本地题库, got %s", item.ProviderLabel)
		}
		if item.TokenKey != "" {
			t.Fatalf("expected local provider token key to be empty, got %q", item.TokenKey)
		}
		return
	}

	t.Fatal("expected local provider in config list")
}

func TestListSearchAccountConfigsWithDBReturnsSavedFlatPrice(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	if err := db.Create(&entity.SearchAccountConfig{
		Provider:   "icodef",
		TokenKey:   "icodefToken",
		TokenValue: "saved-token",
		Enabled:    true,
		Remark:     "已有价格",
		FlatPrice:  2.5,
	}).Error; err != nil {
		t.Fatalf("seed config: %v", err)
	}

	configs, err := ListSearchAccountConfigsWithDB(db)
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}

	for _, item := range configs {
		if item.Provider != "icodef" {
			continue
		}
		if item.FlatPrice != 2.5 {
			t.Fatalf("expected flat price 2.5, got %v", item.FlatPrice)
		}
		return
	}

	t.Fatal("expected icodef config in result")
}

func TestUpdateSearchAccountConfigWithDBCreatesRecord(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	view, err := UpdateSearchAccountConfigWithDB(db, "icodef", UpdateSearchAccountConfigInput{
		TokenValue: "system-default",
		Enabled:    true,
		Remark:     "主账号",
		FlatPrice:  1.88,
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	if view.Provider != "icodef" {
		t.Fatalf("expected provider icodef, got %s", view.Provider)
	}

	if view.TokenKey != "icodefToken" {
		t.Fatalf("expected token key icodefToken, got %s", view.TokenKey)
	}
	if view.FlatPrice != 1.88 {
		t.Fatalf("expected flat price 1.88, got %v", view.FlatPrice)
	}

	var saved entity.SearchAccountConfig
	if err := db.Where("provider = ?", "icodef").First(&saved).Error; err != nil {
		t.Fatalf("load saved config: %v", err)
	}

	if saved.TokenValue != "system-default" || !saved.Enabled || saved.Remark != "主账号" || saved.FlatPrice != 1.88 {
		t.Fatalf("unexpected saved config: %+v", saved)
	}
}

func TestUpdateSearchAccountConfigWithDBAllowsDisablingDefaultAccount(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateSearchAccountConfigWithDB(db, "icodef", UpdateSearchAccountConfigInput{
		TokenValue: "db-token",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("seed config: %v", err)
	}

	view, err := UpdateSearchAccountConfigWithDB(db, "icodef", UpdateSearchAccountConfigInput{
		TokenValue: "db-token",
		Enabled:    false,
		Remark:     "关闭默认账号",
	})
	if err != nil {
		t.Fatalf("disable config: %v", err)
	}

	if view.Enabled {
		t.Fatalf("expected config to be disabled, got %+v", view)
	}

	var saved entity.SearchAccountConfig
	if err := db.Where("provider = ?", "icodef").First(&saved).Error; err != nil {
		t.Fatalf("load saved config: %v", err)
	}

	if saved.Enabled {
		t.Fatalf("expected saved config to be disabled, got %+v", saved)
	}
}

func TestUpdateSearchAccountConfigWithDBRejectsUnsupportedProvider(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	if _, err := UpdateSearchAccountConfigWithDB(db, "unsupported-provider", UpdateSearchAccountConfigInput{}); err == nil {
		t.Fatal("expected invalid provider error")
	}
}

func TestBuildSearchTokensWithDBUsesRequestTokenFirst(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateSearchAccountConfigWithDB(db, "icodef", UpdateSearchAccountConfigInput{
		TokenValue: "system-default",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("seed config: %v", err)
	}

	resolved, err := BuildSearchTokensWithDB(db, url.Values{
		"icodefToken": []string{"request-token"},
	}, true)
	if err != nil {
		t.Fatalf("build tokens: %v", err)
	}

	if resolved.Tokens["icodef"] != "request-token" {
		t.Fatalf("expected request token, got %s", resolved.Tokens["icodef"])
	}
	if resolved.UsedDefaultAccount {
		t.Fatal("expected explicit request token not to use default account")
	}
}

func TestBuildSearchTokensWithDBFallsBackToEnabledDefault(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateSearchAccountConfigWithDB(db, "wanneng", UpdateSearchAccountConfigInput{
		TokenValue: "db-token",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("seed config: %v", err)
	}

	resolved, err := BuildSearchTokensWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("build tokens: %v", err)
	}

	if resolved.Tokens["wanneng"] != "db-token" {
		t.Fatalf("expected db fallback token, got %s", resolved.Tokens["wanneng"])
	}
	if !resolved.UsedDefaultAccount {
		t.Fatal("expected default account usage to be recorded")
	}
	if len(resolved.UsedDefaultProviders) != 1 || resolved.UsedDefaultProviders[0] != "wanneng" {
		t.Fatalf("expected used default providers [wanneng], got %v", resolved.UsedDefaultProviders)
	}
}

func TestBuildSearchTokensWithDBRejectsDefaultFallbackForAnonymousRequests(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateSearchAccountConfigWithDB(db, "wanneng", UpdateSearchAccountConfigInput{
		TokenValue: "db-token",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("seed config: %v", err)
	}

	resolved, err := BuildSearchTokensWithDB(db, url.Values{}, false)
	if err != nil {
		t.Fatalf("build tokens: %v", err)
	}

	if resolved.Tokens["wanneng"] != "" {
		t.Fatalf("expected anonymous request to skip default token, got %s", resolved.Tokens["wanneng"])
	}
	if resolved.UsedDefaultAccount {
		t.Fatal("expected anonymous request not to use default account")
	}
	if len(resolved.UsedDefaultProviders) != 0 {
		t.Fatalf("expected no used default providers, got %v", resolved.UsedDefaultProviders)
	}
}

func TestBuildSearchTokensWithDBDoesNotInjectDisabledProviderDefaultForExplicitUse(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateSearchAccountConfigWithDB(db, "icodef", UpdateSearchAccountConfigInput{
		TokenValue: "system-icodef-token",
		Enabled:    false,
	})
	if err != nil {
		t.Fatalf("seed disabled icodef config: %v", err)
	}
	_, err = UpdateSearchAccountConfigWithDB(db, "wanneng", UpdateSearchAccountConfigInput{
		TokenValue: "system-wanneng-token",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("seed enabled wanneng config: %v", err)
	}

	resolved, err := BuildSearchTokensWithDB(db, url.Values{
		"use": []string{"icodef"},
	}, true)
	if err != nil {
		t.Fatalf("build tokens: %v", err)
	}

	if resolved.Tokens["icodef"] != "" {
		t.Fatalf("expected disabled icodef default not to be injected, got %q", resolved.Tokens["icodef"])
	}
	if resolved.Tokens["wanneng"] != "system-wanneng-token" {
		t.Fatalf("expected other enabled default token to remain injectable under current rules, got %q", resolved.Tokens["wanneng"])
	}
	if len(resolved.UsedDefaultProviders) != 1 || resolved.UsedDefaultProviders[0] != "wanneng" {
		t.Fatalf("expected only wanneng default usage to be recorded, got %v", resolved.UsedDefaultProviders)
	}
}

func TestSearchAccountProviderRequiresTokenUsesProviderMetadata(t *testing.T) {
	if !SearchAccountProviderRequiresToken("icodef") {
		t.Fatal("expected icodef to require token based on provider metadata")
	}
	if !SearchAccountProviderRequiresToken("enncy") {
		t.Fatal("expected enncy to require token based on provider metadata")
	}
	if SearchAccountProviderRequiresToken("local") {
		t.Fatal("expected local not to require token based on provider metadata")
	}
	if SearchAccountProviderRequiresToken("ai") {
		t.Fatal("expected ai not to be treated as normal search token provider")
	}
}

func TestBuildSearchTokensWithDBKeepsExplicitRequestTokensWithoutMarkingDefaultUsage(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateSearchAccountConfigWithDB(db, "icodef", UpdateSearchAccountConfigInput{
		TokenValue: "system-default",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("seed config: %v", err)
	}

	resolved, err := BuildSearchTokensWithDB(db, url.Values{
		"icodefToken": []string{"request-token"},
	}, true)
	if err != nil {
		t.Fatalf("build tokens: %v", err)
	}

	if resolved.Tokens["icodef"] != "request-token" {
		t.Fatalf("expected request token, got %s", resolved.Tokens["icodef"])
	}
	if resolved.UsedDefaultAccount {
		t.Fatal("expected explicit token not to count as default account usage")
	}
}

func TestResolveAccessTokenFromBasicAuthAcceptsBearerAuthorization(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	seedAccessToken(t, db, "bearer-secret", "enabled", "enabled")

	ctx, err := ResolveAccessTokenFromBasicAuth(db, "Bearer bearer-secret")
	if err != nil {
		t.Fatalf("expected bearer token to be accepted, got %v", err)
	}
	if ctx.AccessToken.TokenPrefix != "bearer-secret" {
		t.Fatalf("expected bearer access token to resolve, got %+v", ctx.AccessToken)
	}
	if ctx.User.Username != "user-bearer-secret" {
		t.Fatalf("expected bearer token owner to resolve, got %+v", ctx.User)
	}
}

func TestResolveAccessTokenFromBasicAuthAcceptsBearerAuthorizationCaseInsensitiveTokenValue(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	seedAccessToken(t, db, "tk_9c347abcdef88d82c", "enabled", "enabled")

	ctx, err := ResolveAccessTokenFromBasicAuth(db, "Bearer TK_9C347ABCDEF88D82C")
	if err != nil {
		t.Fatalf("expected bearer token with different case to be accepted, got %v", err)
	}
	if ctx.AccessToken.TokenPrefix != "tk_9c347abcdef88d82c" {
		t.Fatalf("expected stored token to resolve despite case difference, got %+v", ctx.AccessToken)
	}
}

func TestResolveAccessTokenFromBasicAuthRequiresValidToken(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	seedAccessToken(t, db, "plain-secret", "enabled", "enabled")

	if _, err := ResolveAccessTokenFromBasicAuth(db, "Basic "); err == nil {
		t.Fatal("expected empty basic token to fail")
	}
	if _, err := ResolveAccessTokenFromBasicAuth(db, "Bearer "); err == nil {
		t.Fatal("expected empty bearer token to fail")
	}
	if _, err := ResolveAccessTokenFromBasicAuth(db, "Basic invalid-token"); err == nil {
		t.Fatal("expected invalid basic token to fail")
	}
}

func TestResolveAccessTokenFromBasicAuthReturnsDetailedReasonWhenTokenNotFound(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := ResolveAccessTokenFromBasicAuth(db, "Basic not-found-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}
	if !strings.Contains(err.Error(), "访问令牌不存在") {
		t.Fatalf("expected token-not-found detail, got %q", err.Error())
	}
}

func TestResolveAccessTokenFromBasicAuthReturnsDetailedReasonWhenTokenDisabled(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	seedAccessToken(t, db, "disabled-token", "disabled", "enabled")

	_, err := ResolveAccessTokenFromBasicAuth(db, "Basic disabled-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}
	if !strings.Contains(err.Error(), "访问令牌已禁用") {
		t.Fatalf("expected token-disabled detail, got %q", err.Error())
	}
}

func TestResolveAccessTokenFromBasicAuthReturnsDetailedReasonWhenOwnerDisabled(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	seedAccessToken(t, db, "disabled-user-token", "enabled", "disabled")

	_, err := ResolveAccessTokenFromBasicAuth(db, "Basic disabled-user-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}
	if !strings.Contains(err.Error(), "访问令牌所属用户已禁用") {
		t.Fatalf("expected owner-disabled detail, got %q", err.Error())
	}
}

func TestResolveAccessTokenFromBasicAuthRejectsDisabledTokenAndUser(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	seedAccessToken(t, db, "disabled-token", "disabled", "enabled")
	seedAccessToken(t, db, "disabled-user-token", "enabled", "disabled")

	if _, err := ResolveAccessTokenFromBasicAuth(db, "Basic disabled-token"); err == nil {
		t.Fatal("expected disabled token to fail")
	}
	if _, err := ResolveAccessTokenFromBasicAuth(db, "Basic disabled-user-token"); err == nil {
		t.Fatal("expected disabled user token to fail")
	}
}

func seedAccessToken(t *testing.T, db *gorm.DB, plainToken string, tokenStatus string, userStatus string) entity.AccessToken {
	t.Helper()

	user := entity.User{
		Username:       "user-" + plainToken,
		Password:       "123456",
		Role:           "user",
		Status:         userStatus,
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	hash := sha256.Sum256([]byte(plainToken))
	token := entity.AccessToken{
		UserID:      user.ID,
		Name:        "token-" + plainToken,
		TokenHash:   hex.EncodeToString(hash[:]),
		TokenPrefix: plainToken,
		Status:      tokenStatus,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create access token: %v", err)
	}
	return token
}

func TestListSearchAccountConfigsWithDBIncludesAIProvider(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	configs, err := ListSearchAccountConfigsWithDB(db)
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}

	var ai *SearchAccountConfigView
	for i := range configs {
		if configs[i].Provider == "ai" {
			ai = &configs[i]
			break
		}
	}

	if ai == nil {
		t.Fatal("expected ai provider in config list")
	}

	if ai.ProviderLabel != "AI 搜索" {
		t.Fatalf("expected ai provider label, got %s", ai.ProviderLabel)
	}

	if ai.TokenValue != "" {
		t.Fatalf("expected ai token value to start empty, got %s", ai.TokenValue)
	}
}

func TestUpdateSearchAccountConfigWithDBSupportsAIProvider(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	view, err := UpdateSearchAccountConfigWithDB(db, "ai", UpdateSearchAccountConfigInput{
		Enabled: true,
		Remark:  "启用 AI 搜索",
	})
	if err != nil {
		t.Fatalf("update ai config: %v", err)
	}

	if view.Provider != "ai" {
		t.Fatalf("expected ai provider, got %s", view.Provider)
	}

	if !view.Enabled {
		t.Fatalf("expected ai config to be enabled, got %+v", view)
	}
}
