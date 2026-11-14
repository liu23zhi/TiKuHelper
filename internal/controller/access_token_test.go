package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestAccessTokenListReturnsScopedTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAccessTokens
	defer func() {
		listAccessTokens = original
	}()

	listAccessTokens = func(user entity.User, includeAll bool) ([]service.AccessTokenListItem, error) {
		if user.ID != 7 {
			t.Fatalf("expected user id 7, got %d", user.ID)
		}
		if includeAll {
			t.Fatal("expected regular token list request to stay scoped to self")
		}
		return []service.AccessTokenListItem{{
			AccessToken: entity.AccessToken{ID: 1, UserID: 7, Name: "token-a", Status: "enabled"},
		}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/access-tokens", nil)
	ctx.Set("user", &entity.User{ID: 7, Role: "user", Status: "enabled"})

	AccessTokenList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var tokens []entity.AccessToken
	if err := json.Unmarshal(recorder.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "token-a" {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

func TestAccessTokenListAllowsAdminAllScopeWithUsernames(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAccessTokens
	defer func() {
		listAccessTokens = original
	}()

	listAccessTokens = func(user entity.User, includeAll bool) ([]service.AccessTokenListItem, error) {
		if user.ID != 1 || user.Role != "admin" {
			t.Fatalf("expected admin operator, got %+v", user)
		}
		if !includeAll {
			t.Fatal("expected admin all-scope request to enable all tokens")
		}
		return []service.AccessTokenListItem{{
			AccessToken: entity.AccessToken{ID: 2, UserID: 9, Name: "owner-token", Status: "enabled"},
			Username:    "owner-user",
		}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/access-tokens?scope=all", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	AccessTokenList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var tokens []service.AccessTokenListItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Username != "owner-user" {
		t.Fatalf("expected all-scope response to include username, got %+v", tokens)
	}
}

func TestAccessTokenListIgnoresAllScopeForNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAccessTokens
	defer func() {
		listAccessTokens = original
	}()

	listAccessTokens = func(user entity.User, includeAll bool) ([]service.AccessTokenListItem, error) {
		if user.ID != 7 || user.Role != "user" {
			t.Fatalf("expected regular operator, got %+v", user)
		}
		if includeAll {
			t.Fatal("expected non-admin all-scope request to be ignored")
		}
		return []service.AccessTokenListItem{{
			AccessToken: entity.AccessToken{ID: 1, UserID: 7, Name: "token-a", Status: "enabled"},
		}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/access-tokens?scope=all", nil)
	ctx.Set("user", &entity.User{ID: 7, Role: "user", Status: "enabled"})

	AccessTokenList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateAccessTokenReturnsPlainTokenOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createAccessToken
	defer func() {
		createAccessToken = original
	}()

	createAccessToken = func(user entity.User, input service.CreateAccessTokenInput) (service.CreatedAccessToken, error) {
		if user.ID != 9 {
			t.Fatalf("expected user id 9, got %d", user.ID)
		}
		if input.Name != "demo-token" {
			t.Fatalf("expected name demo-token, got %s", input.Name)
		}
		if input.ClientIP != "192.0.2.9" {
			t.Fatalf("expected server-observed client IP, got %q", input.ClientIP)
		}
		return service.CreatedAccessToken{
			AccessToken: entity.AccessToken{ID: 11, UserID: 9, Name: input.Name, TokenPrefix: "tk_demo", Status: "enabled"},
			Token:       "tk_demo_secret",
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"name":"demo-token","remark":"hello","totalQuotaLimit":6.5,"clientIP":"spoofed"}`)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/access-tokens", bytes.NewReader(payload))
	ctx.Request.RemoteAddr = "192.0.2.9:12345"
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 9, Role: "user", Status: "enabled"})

	CreateAccessToken(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response service.CreatedAccessToken
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Token != "tk_demo_secret" {
		t.Fatalf("expected plain token in response, got %s", response.Token)
	}
}

func TestCreateAccessTokenReturnsBadRequestForOutOfRangeQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createAccessToken
	defer func() { createAccessToken = original }()
	createAccessToken = func(entity.User, service.CreateAccessTokenInput) (service.CreatedAccessToken, error) {
		return service.CreatedAccessToken{}, service.ErrAccessTokenQuotaLimitInvalid
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/access-tokens", bytes.NewBufferString(`{"name":"demo-token","totalQuotaLimit":1000000000000}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 9, Role: "user", Status: "enabled"})

	CreateAccessToken(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(service.ErrAccessTokenQuotaLimitInvalid.Error())) {
		t.Fatalf("expected explicit quota validation error, got %s", recorder.Body.String())
	}
}

func TestUpdateAccessTokenRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/access-tokens/3", bytes.NewBufferString("not-json"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: "3"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	UpdateAccessToken(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateAccessTokenPassesOnlyServerClientIPToAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := updateAccessToken
	defer func() { updateAccessToken = original }()
	called := false
	updateAccessToken = func(user entity.User, tokenID int32, input service.UpdateAccessTokenInput) (entity.AccessToken, error) {
		called = true
		if user.ID != 1 || tokenID != 3 || input.ClientIP != "192.0.2.10" {
			t.Fatalf("incorrect audit operator, target or client IP: user=%d target=%d ip=%q", user.ID, tokenID, input.ClientIP)
		}
		return entity.AccessToken{ID: tokenID, Name: input.Name}, nil
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/access-tokens/3", bytes.NewBufferString(`{"name":"已修改","clientIP":"spoofed"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = "192.0.2.10:12345"
	ctx.Params = gin.Params{{Key: "id", Value: "3"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	UpdateAccessToken(ctx)
	if !called || recorder.Code != http.StatusOK {
		t.Fatalf("update failed: called=%v status=%d body=%s", called, recorder.Code, recorder.Body.String())
	}
}

func TestDeleteAccessTokenDeletesOwnedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := deleteAccessToken
	defer func() {
		deleteAccessToken = original
	}()

	called := false
	deleteAccessToken = func(user entity.User, tokenID int32, clientIP string) error {
		called = true
		if user.ID != 5 {
			t.Fatalf("expected user id 5, got %d", user.ID)
		}
		if tokenID != 13 {
			t.Fatalf("expected token id 13, got %d", tokenID)
		}
		if clientIP != "192.0.2.11" {
			t.Fatalf("expected server-observed client IP, got %q", clientIP)
		}
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/access-tokens/13", nil)
	ctx.Request.RemoteAddr = "192.0.2.11:12345"
	ctx.Params = gin.Params{{Key: "id", Value: "13"}}
	ctx.Set("user", &entity.User{ID: 5, Role: "user", Status: "enabled"})

	DeleteAccessToken(ctx)

	if !called {
		t.Fatal("expected delete seam to be called")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}
