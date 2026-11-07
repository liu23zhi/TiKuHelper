package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-resty/resty/v2"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func testBoolPtr(value bool) *bool {
	return &value
}

func newSearchControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.SearchAccountConfig{}, &entity.AISearchConfig{}, &entity.AIModelPrice{}, &entity.SystemSetting{}, &entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.Log{}, &entity.Tiku{}, &entity.TikuAnswerEvidence{}, &entity.ScriptVersion{}, &entity.UserScript{}, &entity.ScriptExchangeToken{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	dao.SetDefault(db)
	return db
}

func seedControllerUser(t *testing.T, db *gorm.DB, id int32, username string, role string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       username,
		Password:       "123456",
		Role:           role,
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedControllerTokenForUser(t *testing.T, db *gorm.DB, user entity.User, plainToken string, totalAmountUsed float64) entity.AccessToken {
	t.Helper()

	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(plainToken))))
	token := entity.AccessToken{
		UserID:          user.ID,
		Name:            "token-" + plainToken,
		TokenHash:       hex.EncodeToString(hash[:]),
		TokenPrefix:     plainToken,
		TokenValue:      plainToken,
		Status:          "enabled",
		TotalAmountUsed: totalAmountUsed,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create access token: %v", err)
	}
	return token
}

func seedControllerDefaultSearchProviderConfig(t *testing.T, db *gorm.DB, provider string, flatPrice float64) entity.SearchAccountConfig {
	t.Helper()

	config := entity.SearchAccountConfig{
		Provider:   provider,
		TokenKey:   provider + "Token",
		TokenValue: "default-" + provider,
		Enabled:    true,
		FlatPrice:  flatPrice,
	}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("create search account config: %v", err)
	}
	return config
}

func seedControllerAccessToken(t *testing.T, db *gorm.DB, plainToken string) entity.AccessToken {
	t.Helper()

	user := seedControllerUser(t, db, 0, "user-"+plainToken, "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 100).Error; err != nil {
		t.Fatalf("seed access token user balance: %v", err)
	}
	return seedControllerTokenForUser(t, db, user, plainToken, 0)
}

func ensureControllerTikuSourceColumn(t *testing.T, db *gorm.DB) {
	t.Helper()

	if err := db.Exec("ALTER TABLE tiku ADD COLUMN source_name TEXT").Error; err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			t.Fatalf("add tiku source_name column: %v", err)
		}
	}
}

func seedControllerLocalTiku(t *testing.T, db *gorm.DB, question string, options []string, answers []string, courseName string, extra string, sourceName string) entity.Tiku {
	t.Helper()

	optionPayload, err := json.Marshal(options)
	if err != nil {
		t.Fatalf("marshal tiku options: %v", err)
	}
	answerPayload, err := json.Marshal(answers)
	if err != nil {
		t.Fatalf("marshal tiku answers: %v", err)
	}

	tiku := entity.Tiku{
		Question:   question,
		Type:       0,
		Options:    string(optionPayload),
		Answer:     string(answerPayload),
		CourseName: courseName,
		Extra:      extra,
	}
	middleware.FillHash(&tiku)
	if err := db.Create(&tiku).Error; err != nil {
		t.Fatalf("create local tiku: %v", err)
	}
	if sourceName != "" {
		if err := db.Exec("UPDATE tiku SET source_name = ? WHERE id = ?", sourceName, tiku.ID).Error; err != nil {
			t.Fatalf("update local tiku source_name: %v", err)
		}
	}
	return tiku
}

type stubSearchClient struct {
	enable bool
	answer [][]string
	err    error
}

func (s *stubSearchClient) getHTTPClient() *resty.Client {
	return nil
}

func (s *stubSearchClient) SearchAnswer(req model.SearchRequest) ([][]string, error) {
	if !s.enable {
		return [][]string{}, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.answer, nil
}

func withStubSearchDependencies(
	t *testing.T,
	local func(model.SearchRequest) ([][]string, error),
	resolver func(string, string, url.Values) (resolvedSearchRequestContext, error),
	remoteBuilder func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error),
	aiSearcher func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error),
	run func(),
) {
	t.Helper()

	originalDBSearch := defaultDBSearchProvider
	originalResolver := resolveSearchRequestContext
	originalClientBuilder := buildSearchClients
	originalClientBindingBuilder := buildSearchClientBindings
	originalAISearcher := searchAIAnswer

	defaultDBSearchProvider = func() func(model.SearchRequest) ([][]string, error) {
		return local
	}
	// 就地清空而不是替换实例：缓存实例被多处并发引用，
	// 中途替换会让「读到的实例」与「持锁的实例」不一致，留下永不释放的锁。
	searchResultCache.clear()
	fallbackResultCache.clear()
	resolveSearchRequestContext = func(_ context.Context, authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
		return resolver(authorization, clientIP, query)
	}
	buildSearchClients = remoteBuilder
	buildSearchClientBindings = func(resolved resolvedSearchRequestContext, useQuery string) []searchClientBinding {
		clients := remoteBuilder(resolved, useQuery)
		bindings := make([]searchClientBinding, 0, len(clients))
		for idx := range clients {
			provider := fmt.Sprintf("stub-%d", idx)
			if useQuery != "" {
				provider = strings.TrimSpace(strings.Split(useQuery, ",")[0])
			}
			bindings = append(bindings, searchClientBinding{
				Provider: provider,
				Search:   clients[idx],
			})
		}
		return bindings
	}
	if aiSearcher != nil {
		searchAIAnswer = aiSearcher
	}
	defer func() {
		defaultDBSearchProvider = originalDBSearch
		resolveSearchRequestContext = originalResolver
		buildSearchClients = originalClientBuilder
		buildSearchClientBindings = originalClientBindingBuilder
		searchAIAnswer = originalAISearcher
		// 清空而不是还原实例：实例必须始终是同一个（见上方注释）。
		searchResultCache.clear()
		fallbackResultCache.clear()
	}()

	run()
}

func TestSearchReturnsUnauthorizedDetailAndRequestSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalResolver := resolveSearchRequestContext
	defer func() {
		resolveSearchRequestContext = originalResolver
	}()
	resolveSearchRequestContext = func(context.Context, string, string, url.Values) (resolvedSearchRequestContext, error) {
		return resolvedSearchRequestContext{}, fmt.Errorf("%w: Authorization 必须使用 Basic 方案", service.ErrInvalidAccessTokenAuthorization)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	rawBody := `{"question":"题目","options":["A","B"]}`
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/search", bytes.NewReader([]byte(rawBody)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://mooc1.chaoxing.com")
	request.Header.Set("Authorization", "Bearer debug-secret-token-123456")
	ctx.Request = request

	Search(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got struct {
		ErrCode int    `json:"errCode"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Request struct {
			Method        string `json:"method"`
			Path          string `json:"path"`
			Origin        string `json:"origin"`
			ContentType   string `json:"contentType"`
			Authorization string `json:"authorization"`
			Body          string `json:"body"`
		} `json:"request"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, recorder.Body.String())
	}

	if got.ErrCode != http.StatusUnauthorized {
		t.Fatalf("expected errCode %d, got %d", http.StatusUnauthorized, got.ErrCode)
	}
	if got.Message != "访问令牌鉴权失败" {
		t.Fatalf("expected message %q, got %q", "访问令牌鉴权失败", got.Message)
	}
	if !strings.Contains(got.Detail, "Authorization") || !strings.Contains(got.Detail, "Basic") {
		t.Fatalf("expected detail to mention Basic authorization, got %q", got.Detail)
	}
	if got.Request.Method != http.MethodPost {
		t.Fatalf("expected method POST, got %q", got.Request.Method)
	}
	if got.Request.Path != "/adapter-service/search" {
		t.Fatalf("expected path /adapter-service/search, got %q", got.Request.Path)
	}
	if got.Request.Origin != "https://mooc1.chaoxing.com" {
		t.Fatalf("expected origin %q, got %q", "https://mooc1.chaoxing.com", got.Request.Origin)
	}
	if got.Request.ContentType != "application/json" {
		t.Fatalf("expected content type application/json, got %q", got.Request.ContentType)
	}
	if got.Request.Body != rawBody {
		t.Fatalf("expected raw body %q, got %q", rawBody, got.Request.Body)
	}
	if got.Request.Authorization == "Bearer debug-secret-token-123456" {
		t.Fatalf("authorization should be masked, got %q", got.Request.Authorization)
	}
	if !strings.Contains(got.Request.Authorization, "Bearer") {
		t.Fatalf("expected masked authorization to retain scheme, got %q", got.Request.Authorization)
	}
}

func TestSearchUnauthorizedWritesAuditLogWithResolvedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 310, "unauthorized-search-log-user", "user")
	token := seedControllerTokenForUser(t, db, user, "unauthorized-search-log-token", 0)

	originalResolver := resolveSearchRequestContext
	defer func() { resolveSearchRequestContext = originalResolver }()
	resolveSearchRequestContext = func(_ context.Context, authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
		return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"余额拒绝日志题目","options":["A","B"]}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	ctx.Request = request

	Search(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var entry entity.Log
	if err := db.Where("module = ? AND user_id = ?", "search", user.ID).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load search failure audit log: %v", err)
	}
	for _, check := range []string{"搜索失败", "当前余额不足", "method=POST", "path=/adapter-service/search"} {
		if !strings.Contains(entry.Description, check) {
			t.Fatalf("expected log description %q to contain %q", entry.Description, check)
		}
	}
	if strings.TrimSpace(entry.OldAnswer) == "" || strings.TrimSpace(entry.NewAnswer) == "" {
		t.Fatalf("expected request and response snapshots, got %+v", entry)
	}
	if strings.Contains(entry.OldAnswer, token.TokenValue) || strings.Contains(entry.NewAnswer, token.TokenValue) {
		t.Fatalf("expected audit snapshots to mask the access token")
	}
}

func TestMaskAuthorizationHeaderKeepsSchemeAndMasksSecret(t *testing.T) {
	got := maskAuthorizationHeader("Bearer debug-secret-token-123456")
	if got == "Bearer debug-secret-token-123456" {
		t.Fatalf("authorization should be masked, got %q", got)
	}
	if !strings.Contains(got, "Bearer") {
		t.Fatalf("expected scheme to be retained, got %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("expected masked ellipsis, got %q", got)
	}
}

func TestExtractUnauthorizedDetailRemovesSentinelPrefix(t *testing.T) {
	err := fmt.Errorf("%w: Authorization 必须使用 Basic 方案", service.ErrInvalidAccessTokenAuthorization)
	got := extractUnauthorizedDetail(err)
	if got != "Authorization 必须使用 Basic 方案" {
		t.Fatalf("unexpected detail: %q", got)
	}
}

func TestSanitizeSearchUnauthorizedBodyTruncatesLongBody(t *testing.T) {
	raw := strings.Repeat("a", maxSearchUnauthorizedBodyLength+10)
	got := sanitizeSearchUnauthorizedBody(raw)

	if !strings.Contains(got, "...(truncated)") {
		t.Fatalf("expected truncated marker, got %q", got)
	}
}

func TestSanitizeSearchUnauthorizedBodyKeepsShortBody(t *testing.T) {
	raw := `{"query":"测试题目"}`
	got := sanitizeSearchUnauthorizedBody(raw)

	if got != raw {
		t.Fatalf("expected body unchanged, got %q", got)
	}
}

func TestSearchReportsCompletedNormalDefaultBillingContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := searchBillingReporter
	defer func() {
		searchBillingReporter = original
	}()

	var reported searchBillingContext
	searchBillingReporter = func(ctx searchBillingContext) {
		reported = ctx
	}

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     12,
				AccessTokenID:              34,
				RequestID:                  "req-handler-normal",
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	if !reported.UsedDefaultAccount {
		t.Fatal("expected reported normal search billing context to preserve default account usage")
	}
	if !reported.Billable {
		t.Fatal("expected reported normal search billing context with answers to be billable")
	}
	if reported.UserID != 12 || reported.AccessTokenID != 34 || reported.RequestID != "req-handler-normal" {
		t.Fatalf("expected reported normal billing context identity to be preserved, got %+v", reported)
	}
}

func TestSearchReportsDowngradedAIDefaultBillingContextWithoutUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := searchBillingReporter
	defer func() {
		searchBillingReporter = original
	}()

	var reported searchBillingContext
	searchBillingReporter = func(ctx searchBillingContext) {
		reported = ctx
	}

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeAI,
				UsedDefaultAI: true,
				UserID:        56,
				AccessTokenID: 78,
				RequestID:     "req-handler-ai",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "default-model",
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer}
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"答案A"}}, searchpkg.AISearchUsage{}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	if !reported.UsedDefaultAccount {
		t.Fatal("expected reported ai search billing context to preserve default account usage")
	}
	if reported.Billable {
		t.Fatal("expected reported ai search billing context without usage not to be billable")
	}
	if reported.UserID != 56 || reported.AccessTokenID != 78 || reported.RequestID != "req-handler-ai" {
		t.Fatalf("expected reported ai billing context identity to be preserved, got %+v", reported)
	}
}

func TestSearchReportsCompletedAIDefaultBillingContextWithUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := searchBillingReporter
	defer func() {
		searchBillingReporter = original
	}()

	var reported searchBillingContext
	searchBillingReporter = func(ctx searchBillingContext) {
		reported = ctx
	}

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeAI,
				UsedDefaultAI: true,
				UserID:        90,
				AccessTokenID: 91,
				RequestID:     "req-handler-ai-usage",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "default-model",
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	if !reported.UsedDefaultAccount {
		t.Fatal("expected reported ai search billing context to preserve default account usage")
	}
	if !reported.Billable {
		t.Fatal("expected reported ai search billing context with usage to be billable")
	}
	if reported.UserID != 90 || reported.AccessTokenID != 91 || reported.RequestID != "req-handler-ai-usage" {
		t.Fatalf("expected reported ai billing context identity to be preserved, got %+v", reported)
	}
}

func TestSearchPersistsBillingRecordAndAccessTokenUsageForDefaultProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 201, "search-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10.5).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-billing-token", 1.25)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2.5)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-search-billing-normal",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var records []entity.BillingRecord
	if err := db.Order("id asc").Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 billing record, got %d", len(records))
	}
	if records[0].UserID != user.ID || records[0].AccessTokenID != token.ID {
		t.Fatalf("expected billing identity user=%d token=%d, got %+v", user.ID, token.ID, records[0])
	}
	if records[0].Type != "normal_search" {
		t.Fatalf("expected billing type normal_search, got %q", records[0].Type)
	}
	if !records[0].UsedDefaultAccount {
		t.Fatalf("expected billing record to mark default account usage, got %+v", records[0])
	}
	if records[0].Provider != "icodef" {
		t.Fatalf("expected billing provider icodef, got %q", records[0].Provider)
	}
	if records[0].Amount != 2.5 {
		t.Fatalf("expected billing amount 2.5, got %v", records[0].Amount)
	}
	if records[0].RequestID != "req-search-billing-normal" {
		t.Fatalf("expected billing request id req-search-billing-normal, got %q", records[0].RequestID)
	}
	if records[0].BalanceBefore != 10.5 || records[0].BalanceAfter != 8 {
		t.Fatalf("expected billing balance 10.5 -> 8.0, got %+v", records[0])
	}

	var stored entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&stored).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if stored.TotalAmountUsed != 3.75 {
		t.Fatalf("expected total amount used 3.75, got %v", stored.TotalAmountUsed)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != 8 {
		t.Fatalf("expected user balance 8.0, got %v", storedUser.Balance)
	}
}

func TestSearchPersistsZeroAmountBillingRecordForFreeDefaultProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 311, "zero-price-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "zero-price-billing-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 0)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-search-zero-price",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"免费渠道答案"}}}).SearchAnswer}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"免费渠道题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var records []entity.BillingRecord
	if err := db.Order("id asc").Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected zero-price provider to create one billing detail row, got %+v", records)
	}
	if records[0].Provider != "icodef" || records[0].Amount != 0 {
		t.Fatalf("unexpected zero-price billing record: %+v", records[0])
	}
	if records[0].BalanceBefore != 10 || records[0].BalanceAfter != 10 {
		t.Fatalf("expected zero-price billing not to change balance, got %+v", records[0])
	}
	if records[0].RequestID != "req-search-zero-price" {
		t.Fatalf("expected request id to be recorded, got %+v", records[0])
	}

	var entry entity.Log
	if err := db.Where("module = ? AND user_id = ?", "search", user.ID).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load search billing audit log: %v", err)
	}
	if strings.Contains(entry.Description, "billingProvider=billing_zero_amount") {
		t.Fatalf("expected zero-price provider to be recorded as a provider charge, got %q", entry.Description)
	}
	if !strings.Contains(entry.Description, "billingProvider=icodef") {
		t.Fatalf("expected billing provider icodef in audit log, got %q", entry.Description)
	}
}

func TestSearchPersistsBillingRecordAndAccessTokenUsageForLocalSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 204, "search-local-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-local-billing-token", 0.4)
	seedControllerDefaultSearchProviderConfig(t, db, "local", 0.6)

	withStubSearchDependencies(t,
		func(model.SearchRequest) ([][]string, error) {
			return [][]string{{"本地答案"}}, nil
		},
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"local"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              "req-search-billing-local",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=local&noRecord=1", bytes.NewReader([]byte(`{"question":"本地题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-local-billing-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var records []entity.BillingRecord
	if err := db.Order("id asc").Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 billing record for local search, got %d", len(records))
	}
	if records[0].Provider != "local" {
		t.Fatalf("expected local billing provider, got %+v", records[0])
	}
	if records[0].Amount != 0.6 {
		t.Fatalf("expected local billing amount 0.6, got %v", records[0].Amount)
	}
	if records[0].RequestID != "req-search-billing-local" {
		t.Fatalf("expected local billing request id req-search-billing-local, got %q", records[0].RequestID)
	}

	var stored entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&stored).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if stored.TotalAmountUsed != 1 {
		t.Fatalf("expected local total amount used 1.0, got %v", stored.TotalAmountUsed)
	}
}

// AI 计费只以模型价格表为准：模型未配置价格时金额为 0，并写入未定价标记。
func TestSearchRecordsZeroAmountForModelMissingFromPriceTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 212, "search-ai-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10.0).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-ai-billing-token", 0.25)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeAI,
				UsedDefaultAI: true,
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-search-billing-ai",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "gpt-billing",
				},
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).First(&record).Error; err != nil {
		t.Fatalf("load ai billing record: %v", err)
	}
	if record.Provider != searchModeAI || record.Amount != 0 {
		t.Fatalf("expected zero-amount ai billing record, got %+v", record)
	}
	if record.BalanceBefore != 10 || record.BalanceAfter != 10 {
		t.Fatalf("expected zero-amount billing to keep balance at 10, got %+v", record)
	}
	for _, part := range []string{
		"provider=ai",
		"model=gpt-billing",
		"pricing=unpriced_model",
		"inputTokens=1000",
		"outputTokens=500",
		"amount=0.00",
	} {
		if !strings.Contains(record.UnitPriceSnapshot, part) {
			t.Fatalf("expected ai billing snapshot to contain %q, got %q", part, record.UnitPriceSnapshot)
		}
	}
	// 旧的倍率计费字段不允许再出现在新账单快照里。
	if strings.Contains(record.UnitPriceSnapshot, "billingMultiplier") {
		t.Fatalf("expected multiplier billing to be gone, got %q", record.UnitPriceSnapshot)
	}

	var stored entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&stored).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if stored.TotalAmountUsed != 0.25 {
		t.Fatalf("expected total amount used to stay 0.25, got %v", stored.TotalAmountUsed)
	}
}

func TestSearchPersistsAIBillingDuringDefaultMixedSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 215, "search-ai-mixed-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10.0).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-ai-mixed-billing-token", 0)
	// GPT-6 Luna：输入 0.10 / 缓存 0.01 / 输出 0.50（元 / 百万 token）。
	seedControllerAIModelPrice(t, db, "GPT-6 Luna", 0.10, 0.01, 0.50)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1.0)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				UsedDefaultAI:              true,
				EnabledSearchProviders:     []string{"icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-search-ai-mixed-billing",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "GPT-6 Luna",
				},
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"普通答案"}}}).SearchAnswer,
			}
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1000000, OutputTokens: 500000, TotalTokens: 1500000}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var records []entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).Find(&records).Error; err != nil {
		t.Fatalf("load mixed ai billing records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected separate normal and ai billing records, got %+v", records)
	}
	recordsByProvider := make(map[string]entity.BillingRecord, len(records))
	for _, record := range records {
		recordsByProvider[record.Provider] = record
	}
	normalRecord, ok := recordsByProvider["icodef"]
	if !ok {
		t.Fatalf("expected separate normal billing record, got %+v", records)
	}
	if normalRecord.Amount != 1 || normalRecord.BalanceBefore != 10 || normalRecord.BalanceAfter != 9 {
		t.Fatalf("expected separate normal billing balance 10 -> 9, got %+v", normalRecord)
	}
	aiRecord, ok := recordsByProvider[searchModeAI]
	if !ok {
		t.Fatalf("expected separate ai billing record, got %+v", records)
	}
	// 1M 输入 × 0.10 + 0.5M 输出 × 0.50 = 0.10 + 0.25 = 0.35
	if aiRecord.Amount != 0.35 || aiRecord.BalanceBefore != 9 || aiRecord.BalanceAfter != 8.65 {
		t.Fatalf("expected separate ai billing balance 9 -> 8.65, got %+v", aiRecord)
	}
	if aiRecord.ModelName != "GPT-6 Luna" ||
		aiRecord.InputTokens != 1000000 ||
		aiRecord.OutputTokens != 500000 ||
		aiRecord.TotalTokens != 1500000 {
		t.Fatalf("expected mixed ai billing token metadata, got %+v", aiRecord)
	}
	for _, part := range []string{"provider=ai", "model=GPT-6 Luna", "pricing=model_price_table", "inputTokens=1000000", "outputTokens=500000"} {
		if !strings.Contains(aiRecord.UnitPriceSnapshot, part) {
			t.Fatalf("expected mixed ai snapshot to contain %q, got %q", part, aiRecord.UnitPriceSnapshot)
		}
	}

	var storedToken entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&storedToken).Error; err != nil {
		t.Fatalf("load mixed billing access token: %v", err)
	}
	if storedToken.TotalAmountUsed != 1.35 {
		t.Fatalf("expected mixed billing total amount used 1.35, got %v", storedToken.TotalAmountUsed)
	}
}

func TestSearchPersistsAIBillingDuringDefaultAIThatRunsInNormalMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 216, "search-ai-only-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 0.5).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-ai-only-billing-token", 0)
	// 用高价模型让 AI 扣费明显大于余额，覆盖「允许扣成负余额」的场景。
	seedControllerAIModelPrice(t, db, "GPT-6 Astra Law *", 12.50, 1.25, 62.50)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				UsedDefaultAI: true,
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-search-ai-only-billing",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "GPT-6 Astra Law *",
				},
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1000000, OutputTokens: 500000, TotalTokens: 1500000}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).First(&record).Error; err != nil {
		t.Fatalf("load ai-only billing record: %v", err)
	}
	if record.Provider != searchModeAI || record.Amount != 43.75 {
		t.Fatalf("expected ai-only default search to bill ai token usage 43.75, got %+v", record)
	}
	if record.ClientIP != "192.0.2.1" {
		t.Fatalf("expected search request source IP on billing record, got %q", record.ClientIP)
	}
	if record.BalanceBefore != 0.5 || record.BalanceAfter != -43.25 {
		t.Fatalf("expected ai-only billing balance 0.5 -> -43.25, got %+v", record)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load ai-only billing user: %v", err)
	}
	if storedUser.Balance != -43.25 {
		t.Fatalf("expected user balance -43.25 after ai billing, got %v", storedUser.Balance)
	}

	billingList, err := service.ListBillingRecordsWithDB(db, user, service.BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list ai-only billing records: %v", err)
	}
	if billingList.Total != 1 || len(billingList.Data) != 1 {
		t.Fatalf("expected one ai-only billing row, got %+v", billingList)
	}
	if billingList.Data[0].ProviderLabel != "AI 模型调用" || billingList.Data[0].Amount != 43.75 {
		t.Fatalf("expected ai-only billing row to expose AI provider and amount, got %+v", billingList.Data[0])
	}
	if len(billingList.Data[0].FeeDetails) != 1 ||
		billingList.Data[0].FeeDetails[0].ProviderLabel != "AI 模型调用" ||
		billingList.Data[0].FeeDetails[0].Amount != 43.75 {
		t.Fatalf("expected ai-only billing row fee details, got %+v", billingList.Data[0].FeeDetails)
	}
	if billingList.Data[0].QuestionText != "题目" || billingList.Data[0].Subject != "搜索答案，题目：题目" {
		t.Fatalf("expected ai-only billing row to preserve the question, got %+v", billingList.Data[0])
	}
}

// 账单里要能看出这次消费是深度思考还是普通搜索（前端「模式」列的数据来源）。
func TestPersistSearchBillingRecordsWithDBStoresSearchMode(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 218, "search-mode-user", "user")

	cases := []struct {
		requestID    string
		deepThinking bool
		want         string
	}{
		{requestID: "req-search-mode-deep", deepThinking: true, want: "deep"},
		{requestID: "req-search-mode-normal", deepThinking: false, want: "normal"},
	}
	for _, testCase := range cases {
		if err := persistSearchBillingRecordsWithDB(db, searchBillingContext{
			UserID:       user.ID,
			RequestID:    testCase.requestID,
			DeepThinking: testCase.deepThinking,
		}, []searchBillingCharge{{Provider: searchModeAI, Amount: 0.5}}); err != nil {
			t.Fatalf("persist billing records (%s): %v", testCase.requestID, err)
		}

		var stored entity.BillingRecord
		if err := db.Where("request_id = ?", testCase.requestID).First(&stored).Error; err != nil {
			t.Fatalf("load billing record (%s): %v", testCase.requestID, err)
		}
		if stored.SearchMode != testCase.want {
			t.Fatalf("expected search_mode %q for %s, got %q", testCase.want, testCase.requestID, stored.SearchMode)
		}
	}
}

func TestPersistSearchBillingRecordsWithDBAllowsBillingToCreateNegativeBalance(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 217, "search-insufficient-balance-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 1).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-insufficient-balance-token", 0)

	err := persistSearchBillingRecordsWithDB(db, searchBillingContext{
		UserID:        user.ID,
		AccessTokenID: token.ID,
		RequestID:     "req-search-insufficient-balance",
	}, []searchBillingCharge{
		{Provider: "icodef", Amount: 1},
		{Provider: searchModeAI, Amount: 1.25},
	})
	if err != nil {
		t.Fatalf("persist billing records: %v", err)
	}

	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", "req-search-insufficient-balance").Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if billingCount != 2 {
		t.Fatalf("expected both billing records, got %d", billingCount)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != -1.25 {
		t.Fatalf("expected balance -1.25 after billing, got %v", storedUser.Balance)
	}

	var storedToken entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&storedToken).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if storedToken.TotalAmountUsed != 2.25 {
		t.Fatalf("expected token usage 2.25, got %v", storedToken.TotalAmountUsed)
	}
}

func TestResolveCompletedSearchBillingChargesAddsSuccessfulAISearchOnly(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := service.SetAISearchPricePerCallWithDB(db, 0.125); err != nil {
		t.Fatalf("set AI search price: %v", err)
	}
	resolved := resolvedSearchRequestContext{SearchMode: searchModeAI}
	request := model.SearchRequest{Question: "题目"}
	usage := searchpkg.AISearchUsage{SearchCalls: []searchpkg.AISearchCallUsage{
		{API: "https://search.example/api", Query: "搜索词一", Status: "success", ResultCount: 2, TotalResults: 8, DurationMs: 34, SourceDetails: `[{"domain":"example.com"}]`},
		{API: "https://search.example/api", Query: "搜索词二", Status: "error", DurationMs: 51, Error: "服务不可用"},
	}}

	charges, _, err := resolveCompletedSearchBillingCharges(db, resolved, "ai", request, usage, false, true)
	if err != nil {
		t.Fatalf("resolve search charges: %v", err)
	}
	if len(charges) != 1 {
		t.Fatalf("expected only successful search API calls to be billed, got %+v", charges)
	}
	for index, charge := range charges {
		if charge.Provider != "ai_search" || charge.Amount != 0.125 {
			t.Fatalf("unexpected search fee charge %d: %+v", index, charge)
		}
	}
	if got := service.ExtractBillingSnapshotField(charges[0].Snapshot, "searchQuery"); got != "搜索词一" {
		t.Fatalf("expected first query in billing snapshot, got %q", got)
	}
	if err := service.SetAISearchPricePerCallWithDB(db, 0); err != nil {
		t.Fatalf("disable AI search price: %v", err)
	}
	charges, _, err = resolveCompletedSearchBillingCharges(db, resolved, "ai", request, usage, false, true)
	if err != nil {
		t.Fatalf("resolve zero-priced search calls: %v", err)
	}
	if len(charges) != 1 {
		t.Fatalf("zero-priced successful search calls must remain visible as a fee row, got %+v", charges)
	}
	for index, charge := range charges {
		if charge.Provider != "ai_search" || charge.Amount != 0 {
			t.Fatalf("unexpected zero-priced search fee %d: %+v", index, charge)
		}
		if got := service.ExtractBillingSnapshotField(charge.Snapshot, "unitPrice"); got != "0.000000" {
			t.Fatalf("expected zero unit price in snapshot, got %q", got)
		}
	}
}

func TestBuildAIBillingSequenceUsesTraceOrder(t *testing.T) {
	usage := searchpkg.AISearchUsage{
		Trace: []searchpkg.AISearchTrace{
			{Stage: "模型响应", ModelCallIndex: 1},
			{Stage: "搜索 API 请求开始", SearchCount: 1},
			{Stage: "搜索 API 响应", SearchCount: 1},
			{Stage: "模型响应", ModelCallIndex: 2},
			{Stage: "网页读取 API 请求开始", Round: 2},
			{Stage: "网页读取 API 响应", Round: 2},
			{Stage: "模型响应", ModelCallIndex: 3},
		},
	}

	sequence := buildAIBillingSequence(usage)
	if sequence.Models[1] != 1 || sequence.Models[2] != 4 || sequence.Models[3] != 7 {
		t.Fatalf("unexpected model sequence: %+v", sequence.Models)
	}
	if !reflect.DeepEqual(sequence.Searches, []int{2}) {
		t.Fatalf("unexpected search sequence: %+v", sequence.Searches)
	}
	if !reflect.DeepEqual(sequence.WebReads, []int{5}) {
		t.Fatalf("unexpected web-read sequence: %+v", sequence.WebReads)
	}
}

func TestResolveCompletedSearchBillingChargesAddsCompletedWebReadOnly(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := service.SetWebReadPricePerCallWithDB(db, 0.25); err != nil {
		t.Fatalf("set web-read price: %v", err)
	}
	usage := searchpkg.AISearchUsage{WebReadCalls: []searchpkg.AIWebReadCallUsage{
		{API: "https://uapis.cn/api/v1/web/tomarkdown/async", URL: "https://example.com/a", TaskID: "task-1", Status: "success", MarkdownBytes: 120, DurationMs: 90},
		{API: "https://uapis.cn/api/v1/web/tomarkdown/async", URL: "https://example.com/b", TaskID: "task-2", Status: "error", Error: "失败"},
	}}
	charges, _, err := resolveCompletedSearchBillingCharges(db, resolvedSearchRequestContext{SearchMode: searchModeAI}, "ai", model.SearchRequest{Question: "题目"}, usage, false, true)
	if err != nil {
		t.Fatalf("resolve web-read charges: %v", err)
	}
	if len(charges) != 1 || charges[0].Provider != "web_read" || charges[0].Amount != 0.25 {
		t.Fatalf("expected only successful web-read calls to be billed, got %+v", charges)
	}
	if got := service.ExtractBillingSnapshotField(charges[0].Snapshot, "webReadURL"); got != "https://example.com/a" {
		t.Fatalf("expected URL in snapshot, got %q", got)
	}

	if err := service.SetWebReadPricePerCallWithDB(db, 0); err != nil {
		t.Fatalf("disable web-read price: %v", err)
	}
	charges, _, err = resolveCompletedSearchBillingCharges(db, resolvedSearchRequestContext{SearchMode: searchModeAI}, "ai", model.SearchRequest{Question: "题目"}, usage, false, true)
	if err != nil {
		t.Fatalf("resolve zero-priced web-read calls: %v", err)
	}
	if len(charges) != 1 || charges[0].Provider != "web_read" || charges[0].Amount != 0 {
		t.Fatalf("zero-priced successful web-read must remain visible as a fee row, got %+v", charges)
	}
}

func TestResolveCompletedSearchBillingChargesAddsSuccessfulOCRIncludingCachedResults(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := service.SetImageOCRPricePerCallWithDB(db, 0.015); err != nil {
		t.Fatalf("set OCR price: %v", err)
	}
	usage := searchpkg.AISearchUsage{OCRCalls: []searchpkg.AIOCRCallUsage{
		{API: "https://uapis.cn/api/v1/image/ocr", ImageURL: "https://images.example/a.webp", Status: "success", TextChars: 12, DurationMs: 80},
		{API: "https://uapis.cn/api/v1/image/ocr", ImageURL: "https://images.example/b.png", Status: "error", Error: "服务不可用", DurationMs: 90},
		{API: "https://uapis.cn/api/v1/image/ocr", ImageURL: "https://images.example/c.png", Status: "success", Cached: true, TextChars: 12, DurationMs: 2},
	}}
	charges, _, err := resolveCompletedSearchBillingCharges(db, resolvedSearchRequestContext{SearchMode: searchModeAI}, "ai", model.SearchRequest{Question: "图片题"}, usage, false, true)
	if err != nil {
		t.Fatalf("resolve OCR charges: %v", err)
	}
	if len(charges) != 2 {
		t.Fatalf("expected successful OCR calls, including cached results, to be billed, got %+v", charges)
	}
	for index, charge := range charges {
		if charge.Provider != "ai_ocr" || charge.Amount != 0.015 {
			t.Fatalf("unexpected OCR fee %d: %+v", index, charge)
		}
	}
}

func TestResolveCompletedSearchBillingChargesSuccessfulTikuLookupIncludingNoHit(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := service.SetTikuSearchPricePerCallWithDB(db, 0.2); err != nil {
		t.Fatal(err)
	}
	usage := searchpkg.AISearchUsage{TikuSearchCalls: []searchpkg.AITikuSearchCallUsage{
		{Query: "命中题", Status: "success", MatchKind: "exact", CandidateCount: 2},
		{Query: "无命中题", Status: "success", MatchKind: "none", CandidateCount: 0},
		{Query: "失败题", Status: "error", Error: "db down"},
	}}
	charges, _, err := resolveCompletedSearchBillingCharges(db, resolvedSearchRequestContext{SearchMode: searchModeAI}, "ai", model.SearchRequest{Question: "原题"}, usage, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(charges) != 2 {
		t.Fatalf("expected hit and no-hit lookups to be charged, got %+v", charges)
	}
	for index, charge := range charges {
		if charge.Provider != "tiku_search" || charge.Amount != 0.2 {
			t.Fatalf("unexpected charge %d: %+v", index, charge)
		}
	}
	if got := service.ExtractBillingSnapshotField(charges[1].Snapshot, "candidate_count"); got != "0" {
		t.Fatalf("no-hit count missing: %q", got)
	}
	if err := service.SetTikuSearchPricePerCallWithDB(db, 0); err != nil {
		t.Fatal(err)
	}
	charges, _, err = resolveCompletedSearchBillingCharges(db, resolvedSearchRequestContext{SearchMode: searchModeAI}, "ai", model.SearchRequest{Question: "原题"}, usage, false, true)
	if err != nil || len(charges) != 2 || charges[0].Amount != 0 {
		t.Fatalf("zero-price successful lookup must remain visible: %+v err=%v", charges, err)
	}
}

func TestResolveCompletedSearchBillingChargesDoesNotChargeProviderWithoutAnswer(t *testing.T) {
	db := newSearchControllerTestDB(t)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1.5)
	if err := service.SetAISearchPricePerCallWithDB(db, 0.125); err != nil {
		t.Fatalf("set AI search price: %v", err)
	}

	resolved := resolvedSearchRequestContext{
		SearchMode:                 searchModeNormal,
		UsedDefaultSearchProviders: []string{"icodef"},
	}
	usage := searchpkg.AISearchUsage{SearchCalls: []searchpkg.AISearchCallUsage{{
		API: "https://search.example/api", Query: "检索词", Status: "success", ResultCount: 1,
	}}}
	charges, _, err := resolveCompletedSearchBillingCharges(db, resolved, "", model.SearchRequest{Question: "题目"}, usage, false, false)
	if err != nil {
		t.Fatalf("resolve no-answer search charges: %v", err)
	}
	if len(charges) != 1 || charges[0].Provider != "ai_search" || charges[0].Amount != 0.125 {
		t.Fatalf("expected only the completed web-search fee, got %+v", charges)
	}
}

func TestResolveCompletedSearchBillingChargesNormalizesInvalidHistoricalFlatPrice(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.Create(&entity.SearchAccountConfig{
		Provider:   "icodef",
		TokenKey:   "icodefToken",
		TokenValue: "legacy-token",
		Enabled:    true,
		FlatPrice:  -2.5,
	}).Error; err != nil {
		t.Fatalf("seed invalid historical search price: %v", err)
	}

	charges, _, err := resolveCompletedSearchBillingCharges(
		db,
		resolvedSearchRequestContext{SearchMode: searchModeNormal, UsedDefaultSearchProviders: []string{"icodef"}},
		"icodef",
		model.SearchRequest{Question: "题目"},
		searchpkg.AISearchUsage{},
		false,
		true,
	)
	if err != nil {
		t.Fatalf("resolve historical invalid price: %v", err)
	}
	if len(charges) != 1 || charges[0].Provider != "icodef" || charges[0].Amount != 0 {
		t.Fatalf("expected invalid historical flat price to normalize to zero charge, got %+v", charges)
	}
}

func TestSearchAllowsBillingToCreateNegativeBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 218, "search-final-balance-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 1).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-final-balance-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-search-final-balance",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", "req-search-final-balance").Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if billingCount != 1 {
		t.Fatalf("expected billing record to be created, got %d", billingCount)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != -1 {
		t.Fatalf("expected balance -1 after billing, got %v", storedUser.Balance)
	}
}

// 令牌累计额度只用于展示/统计：额度已用尽但账号余额 > 0 时，请求必须照常执行并计费。
func TestSearchAllowsRequestWhenAccessTokenQuotaExhausted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 220, "search-exhausted-quota-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-exhausted-quota-token", 5)
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", token.ID).Update("total_quota_limit", 5).Error; err != nil {
		t.Fatalf("seed access token quota: %v", err)
	}
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
			return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"额度耗尽但余额充足时应返回的答案"}}, nil
				},
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"额度已耗尽题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-exhausted-quota-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	if upstreamCalls != 1 {
		t.Fatalf("expected the request to reach the upstream, got %d calls", upstreamCalls)
	}

	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Where("access_token_id = ?", token.ID).Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if billingCount != 1 {
		t.Fatalf("expected one billing record, got %d", billingCount)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != 9 {
		t.Fatalf("expected balance 9 after one charge, got %v", storedUser.Balance)
	}

	var storedToken entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&storedToken).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if storedToken.TotalAmountUsed != 6 {
		t.Fatalf("expected token usage to exceed quota (6 > 5), got %v", storedToken.TotalAmountUsed)
	}
}

// 令牌累计额度不再拦截请求：余额充足时允许累计额度超出上限，并照常计费。
func TestSearchBillsBeyondAccessTokenQuotaWhenBalanceSufficient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 221, "search-quota-boundary-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-quota-boundary-token", 4.5)
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", token.ID).Update("total_quota_limit", 5).Error; err != nil {
		t.Fatalf("seed access token quota: %v", err)
	}
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
			return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"超出令牌额度也应记账的答案"}}, nil
				},
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"额度边界题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-quota-boundary-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	if upstreamCalls != 1 {
		t.Fatalf("expected the request to reach the upstream, got %d calls", upstreamCalls)
	}

	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Where("access_token_id = ?", token.ID).Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if billingCount != 1 {
		t.Fatalf("expected one billing record, got %d", billingCount)
	}

	var storedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&storedUser).Error; err != nil {
		t.Fatalf("load quota-boundary user: %v", err)
	}
	if storedUser.Balance != 9 {
		t.Fatalf("expected balance 9 after one charge, got %v", storedUser.Balance)
	}

	var storedToken entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&storedToken).Error; err != nil {
		t.Fatalf("load quota-boundary token: %v", err)
	}
	if storedToken.TotalAmountUsed != 5.5 {
		t.Fatalf("expected token usage to exceed the 5 quota (5.5), got %v", storedToken.TotalAmountUsed)
	}
}

func TestSearchPersistsCachedSourceNameInLocalBillingSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	ensureControllerTikuSourceColumn(t, db)
	user := seedControllerUser(t, db, 214, "search-local-source-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-local-source-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "local", 0.6)
	seedControllerLocalTiku(t, db, "本地缓存题目", []string{"A", "B"}, []string{"缓存答案"}, "", "", "万能题库")

	withStubSearchDependencies(t,
		searchpkg.GetDBSearch().SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"local"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              "req-search-local-source",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=local&noRecord=1", bytes.NewReader([]byte(`{"question":"本地缓存题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-local-source-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var record entity.BillingRecord
	if err := db.Where("request_id = ?", "req-search-local-source").First(&record).Error; err != nil {
		t.Fatalf("load local source billing record: %v", err)
	}
	if !strings.Contains(record.UnitPriceSnapshot, "sourceName=万能题库") {
		t.Fatalf("expected local billing snapshot to contain source name, got %+v", record)
	}
}

func TestSearchPersistsQuestionTextForPersonalUsageRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 216, "search-question-text-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-question-text-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1.25)

	questionText := "这是 带空格 的题目"

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				EnabledSearchProviders:     []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-search-question-text",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"这是 带空格 的题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-question-text-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	result, err := service.ListBillingRecordsWithDB(db, user, service.BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records with question text: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one billing record, got %+v", result.Data)
	}
	if result.Data[0].QuestionText != questionText {
		t.Fatalf("expected question text %q, got %q", questionText, result.Data[0].QuestionText)
	}
}

func TestSearchWritesLocalQuestionIDIntoAuditLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	localQuestion := seedControllerLocalTiku(t, db, "本地日志题目", []string{"A", "B"}, []string{"本地答案"}, "", "", "万能题库")
	user := seedControllerUser(t, db, 215, "search-local-log-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-local-log-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "local", 0.6)

	withStubSearchDependencies(t,
		searchpkg.GetDBSearch().SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"local"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              "req-search-local-log",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=local&noRecord=1", bytes.NewReader([]byte(`{"question":"本地日志题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-local-log-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var entry entity.Log
	if err := db.Where("module = ?", "search").Where("user_id = ?", user.ID).First(&entry).Error; err != nil {
		t.Fatalf("load local search audit log: %v", err)
	}
	if entry.Qid != localQuestion.ID {
		t.Fatalf("expected audit log qid %d, got %+v", localQuestion.ID, entry)
	}
	if !strings.Contains(entry.Description, fmt.Sprintf("localQuestionId=%d", localQuestion.ID)) {
		t.Fatalf("expected description to contain local question id, got %+v", entry)
	}
}

func TestSearchPersistsBillingForAdminAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	admin := seedControllerUser(t, db, 202, "search-billing-admin", "admin")
	if err := db.Model(&entity.User{}).Where("id = ?", admin.ID).Update("balance", 6.5).Error; err != nil {
		t.Fatalf("seed admin balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, admin, "search-admin-token", 0.5)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1.88)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     admin.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-search-billing-admin",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        admin,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ? AND access_token_id = ?", admin.ID, token.ID).Count(&count).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 admin billing record, got %d", count)
	}

	var stored entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&stored).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if stored.TotalAmountUsed != 2.38 {
		t.Fatalf("expected admin total amount used 2.38, got %v", stored.TotalAmountUsed)
	}

	var storedAdmin entity.User
	if err := db.Where("id = ?", admin.ID).First(&storedAdmin).Error; err != nil {
		t.Fatalf("load admin user: %v", err)
	}
	if storedAdmin.Balance != 4.62 {
		t.Fatalf("expected admin balance 4.62, got %v", storedAdmin.Balance)
	}

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", admin.ID, token.ID).First(&record).Error; err != nil {
		t.Fatalf("load admin billing record: %v", err)
	}
	if record.BalanceBefore != 6.5 || record.BalanceAfter != 4.62 {
		t.Fatalf("expected admin billing balance 6.5 -> 4.62, got %+v", record)
	}
}

func TestSearchWritesAuditLogForAuthenticatedSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 203, "search-log-user", "user")
	token := seedControllerTokenForUser(t, db, user, "search-log-token", 0)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-search-audit-log",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"答案A"}}}).SearchAnswer}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = "10.20.30.40:12345"
			request.Header.Set("X-Forwarded-For", "10.20.30.40")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var entry entity.Log
	if err := db.Where("module = ?", "search").Where("user_id = ?", user.ID).First(&entry).Error; err != nil {
		t.Fatalf("load search audit log: %v", err)
	}
	if entry.Action != logActionSearch {
		t.Fatalf("expected search action %d, got %+v", logActionSearch, entry)
	}
	if entry.ClientIP != "10.20.30.40" {
		t.Fatalf("expected client ip 10.20.30.40, got %+v", entry)
	}
	if !strings.Contains(entry.Description, "req-search-audit-log") {
		t.Fatalf("expected description to contain request id, got %+v", entry)
	}
	if !strings.Contains(entry.Description, "搜索") {
		t.Fatalf("expected description to mention search, got %+v", entry)
	}
}

func TestSearchWritesFailureAuditLogForProviderError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 204, "search-failure-user", "user")
	token := seedControllerTokenForUser(t, db, user, "search-failure-token", 0)
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:     searchModeNormal,
				UserID:         user.ID,
				AccessTokenID:  token.ID,
				RequestID:      "req-search-failure",
				Tokens:         map[string]string{"wanneng": "configured"},
				AccessTokenCtx: &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					return nil, errors.New("AI 接口连接超时")
				},
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1", bytes.NewReader([]byte(`{"question":"失败题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = "10.20.30.41:12345"
			ctx.Request = request

			Search(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var entry entity.Log
	if err := db.Where("module = ?", searchAuditModule).Where("user_id = ?", user.ID).Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load failure audit log: %v", err)
	}
	for _, expected := range []string{"搜索失败", "requestId=req-search-failure", "provider=wanneng", "AI 接口连接超时"} {
		if !strings.Contains(entry.Description, expected) {
			t.Fatalf("failure description %q does not contain %q", entry.Description, expected)
		}
	}
}

func TestSearchRejectsAnonymousLocalSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newSearchControllerTestDB(t)

	withStubSearchDependencies(t,
		func(model.SearchRequest) ([][]string, error) {
			return [][]string{{"本地答案"}}, nil
		},
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				RequestID:  "req-anonymous-local",
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=local&noRecord=1", bytes.NewReader([]byte(`{"question":"本地匿名题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
			}

			var got struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal unauthorized response: %v, body=%s", err, recorder.Body.String())
			}
			if !strings.Contains(got.Detail, "匿名") || !strings.Contains(got.Detail, "本地题库") {
				t.Fatalf("expected unauthorized detail to mention anonymous local restriction, got %q", got.Detail)
			}
		})
}

func TestSearchRejectsDisabledLocalSearchEvenForAuthenticatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "disabled-local-token")

	originalResolver := resolveSearchRequestContext
	originalClientBindings := buildSearchClientBindings
	defer func() {
		resolveSearchRequestContext = originalResolver
		buildSearchClientBindings = originalClientBindings
	}()
	resolveSearchRequestContext = func(_ context.Context, authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
		return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
	}
	buildSearchClientBindings = func(resolvedSearchRequestContext, string) []searchClientBinding {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=local&noRecord=1", bytes.NewReader([]byte(`{"question":"本地禁用题目","options":["A","B"]}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Basic disabled-local-token")
	ctx.Request = request

	Search(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal unauthorized response: %v, body=%s", err, recorder.Body.String())
	}
	if !strings.Contains(got.Detail, "本地题库") || !strings.Contains(got.Detail, "未启用") {
		t.Fatalf("expected unauthorized detail to mention disabled local provider, got %q", got.Detail)
	}
	if token.ID == 0 {
		t.Fatal("expected seeded access token to exist")
	}
}

func TestSearchAllowsAnonymousRemoteSearchWithoutBillingAndWritesFullAuditLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	rawBody := `{"question":"匿名远程题目","options":["A","B"]}`

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				RequestID:  "req-anonymous-remote",
				Tokens:     map[string]string{"wanneng": "plain-third-party-token"},
			}, nil
		},
		func(resolved resolvedSearchRequestContext, useQuery string) []func(model.SearchRequest) ([][]string, error) {
			if useQuery != "wanneng" {
				t.Fatalf("expected explicit provider wanneng, got %q", useQuery)
			}
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"远程答案"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&wannengToken=plain-third-party-token&noRecord=1", bytes.NewReader([]byte(rawBody)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "anonymous-test-client/1.0")
			request.RemoteAddr = "198.51.100.25:34567"
			request.Header.Set("X-Forwarded-For", "198.51.100.25")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if billingCount != 0 {
		t.Fatalf("expected anonymous remote search not to create billing records, got %d", billingCount)
	}

	var entry entity.Log
	if err := db.Where("module = ?", "search").First(&entry).Error; err != nil {
		t.Fatalf("load anonymous search audit log: %v", err)
	}
	if entry.Action != logActionSearch {
		t.Fatalf("expected search action %d, got %+v", logActionSearch, entry)
	}
	if entry.UserID != 0 {
		t.Fatalf("expected anonymous search log user id 0, got %+v", entry)
	}
	if entry.Username != "anonymous" {
		t.Fatalf("expected anonymous search username snapshot, got %+v", entry)
	}
	if !strings.Contains(entry.Description, "requestId=req-anonymous-remote") {
		t.Fatalf("expected description to contain request id, got %+v", entry)
	}
	if !strings.Contains(entry.Description, "hitProvider=wanneng") {
		t.Fatalf("expected description to contain actual hit provider, got %+v", entry)
	}
	if strings.Contains(entry.OldAnswer, "plain-third-party-token") || !strings.Contains(entry.OldAnswer, "wannengToken=plai...oken") {
		t.Fatalf("expected raw request log to mask explicit provider token, got %+v", entry)
	}
	if !strings.Contains(entry.OldAnswer, rawBody) {
		t.Fatalf("expected raw request log to contain request body, got %+v", entry)
	}
	if !strings.Contains(entry.NewAnswer, "远程答案") {
		t.Fatalf("expected response snapshot to contain answer content, got %+v", entry)
	}
}

func TestSearchWritesActualHitProviderIntoAuditLogAndBillsExplicitProviderSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 205, "search-hit-provider-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-hit-provider-token", 0.2)
	seedControllerDefaultSearchProviderConfig(t, db, "wanneng", 1.6)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"wanneng": "request-provider-token"},
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-hit-provider-log",
				AccessTokenCtx: &service.AccessTokenAuthContext{
					User:        user,
					AccessToken: token,
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"显式渠道答案"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&wannengToken=request-provider-token&noRecord=1", bytes.NewReader([]byte(`{"question":"显式渠道题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic search-hit-provider-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	var record entity.BillingRecord
	if err := db.Where("user_id = ?", user.ID).First(&record).Error; err != nil {
		t.Fatalf("load explicit provider billing record: %v", err)
	}
	if record.Provider != "wanneng" {
		t.Fatalf("expected billing provider wanneng, got %+v", record)
	}
	if record.Amount != 1.6 {
		t.Fatalf("expected billing amount 1.6, got %+v", record)
	}
	if record.UsedDefaultAccount {
		t.Fatalf("expected explicit provider billing record not to mark default account usage, got %+v", record)
	}
	if !strings.Contains(record.UnitPriceSnapshot, "provider=wanneng") {
		t.Fatalf("expected billing snapshot to contain explicit provider, got %+v", record)
	}

	var storedToken entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&storedToken).Error; err != nil {
		t.Fatalf("load explicit provider access token: %v", err)
	}
	if storedToken.TotalAmountUsed != 1.8 {
		t.Fatalf("expected explicit provider total amount used 1.8, got %v", storedToken.TotalAmountUsed)
	}

	var entry entity.Log
	if err := db.Where("module = ?", "search").Where("user_id = ?", user.ID).First(&entry).Error; err != nil {
		t.Fatalf("load explicit provider audit log: %v", err)
	}
	if !strings.Contains(entry.Description, "hitProvider=wanneng") {
		t.Fatalf("expected description to contain actual hit provider, got %+v", entry)
	}
	if !strings.Contains(entry.Description, "billingProvider=wanneng") {
		t.Fatalf("expected description to contain billing provider, got %+v", entry)
	}
}

func TestResolveSearchRequestContextWithDBAcceptsBearerAuthorizationCaseInsensitiveTokenValue(t *testing.T) {
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "tk_9c347abcdef88d82c")

	resolved, err := resolveSearchRequestContextWithDB(db, "Bearer TK_9C347ABCDEF88D82C", "203.0.113.10", url.Values{})
	if err != nil {
		t.Fatalf("expected bearer authorization with different token case to resolve, got %v", err)
	}
	if resolved.AccessTokenCtx == nil {
		t.Fatal("expected access token context for normalized bearer authorization")
	}
	if resolved.AccessTokenCtx.AccessToken.ID != token.ID {
		t.Fatalf("expected resolved token id %d, got %d", token.ID, resolved.AccessTokenCtx.AccessToken.ID)
	}
}

func TestResolveSearchRequestContextWithDBRejectsInvalidAuthorization(t *testing.T) {
	db := newSearchControllerTestDB(t)
	seedControllerAccessToken(t, db, "valid-token")

	_, err := resolveSearchRequestContextWithDB(db, "Basic invalid-token", "", url.Values{})
	if !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected invalid access token error, got %v", err)
	}
}

func TestResolveSearchRequestContextWithDBSkipsDefaultAccountsWithoutAuthorization(t *testing.T) {
	db := newSearchControllerTestDB(t)

	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}
	if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, err := resolveSearchRequestContextWithDB(db, "", "", url.Values{})
	if err != nil {
		t.Fatalf("resolve search context: %v", err)
	}

	if resolved.Tokens["icodef"] != "" {
		t.Fatalf("expected anonymous request not to use default search token, got %s", resolved.Tokens["icodef"])
	}
	if resolved.AIConfig.BaseURL != "" || resolved.AIConfig.APIKey != "" || resolved.AIConfig.Model != "" {
		t.Fatalf("expected anonymous request not to use default ai config, got %+v", resolved.AIConfig)
	}
	if resolved.AccessTokenCtx != nil {
		t.Fatalf("expected anonymous request not to have access token context, got %+v", resolved.AccessTokenCtx)
	}
	if resolved.UsedDefaultSearch {
		t.Fatal("expected anonymous request not to mark default search usage")
	}
	if resolved.UsedDefaultAI {
		t.Fatal("expected anonymous request not to mark default ai usage")
	}
	if len(resolved.UsedDefaultSearchProviders) != 0 {
		t.Fatalf("expected anonymous request not to have default search providers, got %+v", resolved.UsedDefaultSearchProviders)
	}
	if resolved.RequestID == "" {
		t.Fatal("expected anonymous request to have request id")
	}
	if resolved.SearchMode != searchModeNormal {
		t.Fatalf("expected anonymous request search mode %s, got %s", searchModeNormal, resolved.SearchMode)
	}
	if resolved.UserID != 0 {
		t.Fatalf("expected anonymous request user id 0, got %d", resolved.UserID)
	}
	if resolved.AccessTokenID != 0 {
		t.Fatalf("expected anonymous request access token id 0, got %d", resolved.AccessTokenID)
	}
}

func TestResolveSearchRequestContextWithDBRejectsDefaultAccountsWhenUserBalanceIsNegative(t *testing.T) {
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "negative-balance-token")
	if err := db.Model(&entity.User{}).Where("id = ?", token.UserID).Update("balance", -3).Error; err != nil {
		t.Fatalf("seed negative balance: %v", err)
	}

	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}
	if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	_, err := resolveSearchRequestContextWithDB(db, "Basic negative-balance-token", "198.51.100.24", url.Values{})
	if err == nil {
		t.Fatal("expected negative-balance authenticated request without explicit provider params to fail")
	}
	if !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}
	if !strings.Contains(err.Error(), "当前余额不足，系统题库已禁用，请充值后再使用") {
		t.Fatalf("expected negative-balance error to mention balance, got %q", err.Error())
	}
}

func TestResolveSearchRequestContextWithDBRejectsUserWithZeroBalance(t *testing.T) {
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "zero-balance-token")
	if err := db.Model(&entity.User{}).Where("id = ?", token.UserID).Update("balance", 0).Error; err != nil {
		t.Fatalf("seed zero balance: %v", err)
	}

	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}

	_, err := resolveSearchRequestContextWithDB(db, "Basic zero-balance-token", "198.51.100.24", url.Values{})
	if err == nil {
		t.Fatal("expected zero-balance authenticated request to fail")
	}
	if !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}
	if !strings.Contains(err.Error(), "当前余额不足，系统题库已禁用，请充值后再使用") {
		t.Fatalf("expected zero-balance error to mention balance, got %q", err.Error())
	}
}

func TestResolveSearchRequestContextWithDBAllowsReservedScriptTokenAtZeroBalance(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "script-user", "user")
	version := entity.ScriptVersion{Version: "v1", Filename: "script.exe", SHA256: strings.Repeat("a", 64), SizeBytes: 2, Data: []byte("MZ"), IsCurrent: true, CreatedBy: user.ID}
	if err := db.Create(&version).Error; err != nil {
		t.Fatalf("create script version: %v", err)
	}
	script := entity.UserScript{UserID: user.ID, VersionID: version.ID, Name: "script", Instruction: "powershell", Parameters: "{}", BaseDomain: "a.com", Scheme: "https", Enabled: true, BillingEnabled: true, UseTikuPrice: true, ShortCode: "a", AccessKey: "script-key", ReservedFee: 10}
	if err := db.Create(&script).Error; err != nil {
		t.Fatalf("create script: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "script-token", 0)
	if err := db.Model(&entity.UserScript{}).Where("id = ?", script.ID).Updates(map[string]any{"access_token_id": token.ID}).Error; err != nil {
		t.Fatalf("link script token: %v", err)
	}
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", token.ID).Update("script_id", script.ID).Error; err != nil {
		t.Fatalf("mark script token: %v", err)
	}
	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{TokenValue: "default-search-token", Enabled: true}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}
	resolved, err := resolveSearchRequestContextWithDB(db, "Basic script-token", "198.51.100.25", url.Values{})
	if err != nil {
		t.Fatalf("reserved script token should bypass the normal balance gate: %v", err)
	}
	if resolved.ScriptID != script.ID || !resolved.UsedDefaultSearch || resolved.Tokens["icodef"] != "default-search-token" {
		t.Fatalf("expected reserved script context to use defaults, got %+v", resolved)
	}
}

func TestResolveSearchRequestContextWithDBRejectsExplicitProviderParamsWhenUserBalanceIsNegative(t *testing.T) {
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "negative-explicit-provider-token")
	if err := db.Model(&entity.User{}).Where("id = ?", token.UserID).Update("balance", -3).Error; err != nil {
		t.Fatalf("seed negative balance: %v", err)
	}

	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}
	if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	_, err := resolveSearchRequestContextWithDB(db, "Basic negative-explicit-provider-token", "198.51.100.24", url.Values{
		"use":          []string{"wanneng,ai"},
		"wannengToken": []string{"request-provider-token"},
		"url":          []string{"https://override.example.com/v1"},
		"key":          []string{"override-key"},
		"model":        []string{"override-model"},
	})
	if err == nil {
		t.Fatal("expected explicit provider parameters not to bypass the negative-balance guard")
	}
	if !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}
	if !strings.Contains(err.Error(), "当前余额不足，系统题库已禁用，请充值后再使用") {
		t.Fatalf("expected negative-balance error to mention balance, got %q", err.Error())
	}
}

func TestSearchRejectsNegativeBalanceUserWithExplicitProviderParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 219, "search-negative-explicit-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", -3).Error; err != nil {
		t.Fatalf("seed negative balance: %v", err)
	}
	_ = seedControllerTokenForUser(t, db, user, "search-negative-explicit-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "wanneng", 1.6)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
			return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"显式渠道答案"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&wannengToken=request-provider-token&noRecord=1", bytes.NewReader([]byte(`{"question":"显式渠道题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic "+"search-negative-explicit-token")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("unmarshal unauthorized response: %v, body=%s", err, recorder.Body.String())
			}
			if response.Code != "INSUFFICIENT_BALANCE" || response.Detail != "当前余额不足，系统题库已禁用，请充值后再使用" {
				t.Fatalf("unexpected balance rejection: %+v", response)
			}
		})
}

func TestSearchRejectsNegativeBalanceUserWithoutExplicitProviderParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "negative-balance-search-token")
	if err := db.Model(&entity.User{}).Where("id = ?", token.UserID).Update("balance", -3).Error; err != nil {
		t.Fatalf("seed negative balance: %v", err)
	}

	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}

	originalResolver := resolveSearchRequestContext
	originalClientBindings := buildSearchClientBindings
	defer func() {
		resolveSearchRequestContext = originalResolver
		buildSearchClientBindings = originalClientBindings
	}()
	resolveSearchRequestContext = func(_ context.Context, authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
		return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
	}
	buildSearchClientBindings = func(resolved resolvedSearchRequestContext, useQuery string) []searchClientBinding {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Basic negative-balance-search-token")
	ctx.Request = request

	Search(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal unauthorized response: %v, body=%s", err, recorder.Body.String())
	}
	if got.Detail != "当前余额不足，系统题库已禁用，请充值后再使用" {
		t.Fatalf("expected negative-balance unauthorized detail to mention balance, got %q", got.Detail)
	}
	var publicFields map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &publicFields); err != nil {
		t.Fatal(err)
	}
	if publicFields["code"] != "INSUFFICIENT_BALANCE" || publicFields["errCode"] != float64(401) || publicFields["message"] != "访问令牌鉴权失败" {
		t.Fatalf("balance rejection must add a stable code and preserve legacy fields: %s", recorder.Body.String())
	}
}

// 回归：余额不足这类「客户端可自行修复」的 401 只能写 warning，不能写 error。
//
// 线上事故：同一 IP 因余额不足反复调用，1.5 小时往 Tiku-error.txt 写了 656 行，
// 把真正的系统异常淹没了。
func TestSearchUnauthorizedLogsBusinessRejectionAsWarning(t *testing.T) {
	dir := t.TempDir()
	originalLogDir := *logger.LogDir
	*logger.LogDir = dir
	t.Cleanup(func() {
		_ = logger.Close()
		*logger.LogDir = originalLogDir
	})
	if err := logger.SetupGinLog(); err != nil {
		t.Fatalf("setup logger: %v", err)
	}

	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "warning-level-token")
	if err := db.Model(&entity.User{}).Where("id = ?", token.UserID).Update("balance", 0).Error; err != nil {
		t.Fatalf("seed zero balance: %v", err)
	}
	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}

	originalResolver := resolveSearchRequestContext
	originalClientBindings := buildSearchClientBindings
	defer func() {
		resolveSearchRequestContext = originalResolver
		buildSearchClientBindings = originalClientBindings
	}()
	resolveSearchRequestContext = func(_ context.Context, authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
		return resolveSearchRequestContextWithDB(db, authorization, clientIP, query)
	}
	buildSearchClientBindings = func(resolved resolvedSearchRequestContext, useQuery string) []searchClientBinding {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Basic warning-level-token")
	ctx.Request = request

	Search(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	errorLog := readLogFile(t, filepath.Join(dir, "Tiku-error.txt"))
	if strings.Contains(errorLog, "搜索接口鉴权失败") {
		t.Fatalf("business rejection must not be written to the error log:\n%s", errorLog)
	}
	warnLog := readLogFile(t, filepath.Join(dir, "Tiku-warning.txt"))
	if !strings.Contains(warnLog, "搜索接口鉴权失败") || !strings.Contains(warnLog, "当前余额不足") {
		t.Fatalf("expected the business rejection in the warning log, got:\n%s", warnLog)
	}
}

// readLogFile 读日志文件；文件不存在时按空处理（例如没有任何 error 级日志）。
func readLogFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func TestResolveSearchRequestContextWithDBAllowsDefaultAccountsWithValidAuthorization(t *testing.T) {
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "valid-token")

	if _, err := service.UpdateSearchAccountConfigWithDB(db, "icodef", service.UpdateSearchAccountConfigInput{
		TokenValue: "default-search-token",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed search config: %v", err)
	}
	if _, err := service.UpdateSearchAccountConfigWithDB(db, "local", service.UpdateSearchAccountConfigInput{
		Enabled:   true,
		FlatPrice: 0.6,
	}); err != nil {
		t.Fatalf("seed local config: %v", err)
	}
	if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, err := resolveSearchRequestContextWithDB(db, "Basic valid-token", "198.51.100.24", url.Values{})
	if err != nil {
		t.Fatalf("resolve search context: %v", err)
	}

	if resolved.Tokens["icodef"] != "default-search-token" {
		t.Fatalf("expected default search token, got %s", resolved.Tokens["icodef"])
	}
	if resolved.AIConfig.BaseURL != "https://default.example.com/v1" || resolved.AIConfig.APIKey != "default-key" || resolved.AIConfig.Model != "default-model" {
		t.Fatalf("expected default ai config, got %+v", resolved.AIConfig)
	}
	if resolved.AccessTokenCtx == nil {
		t.Fatal("expected access token context for authenticated request")
	}
	if resolved.AccessTokenCtx.AccessToken.ID != token.ID {
		t.Fatalf("expected access token id %d, got %d", token.ID, resolved.AccessTokenCtx.AccessToken.ID)
	}
	if !resolved.UsedDefaultSearch {
		t.Fatal("expected authenticated request to mark default search usage")
	}
	if !resolved.UsedDefaultAI {
		t.Fatal("expected authenticated request to mark default ai usage")
	}
	if len(resolved.UsedDefaultSearchProviders) != 1 || resolved.UsedDefaultSearchProviders[0] != "icodef" {
		t.Fatalf("expected default search providers [icodef], got %+v", resolved.UsedDefaultSearchProviders)
	}
	if !containsString(resolved.EnabledSearchProviders, "icodef") || !containsString(resolved.EnabledSearchProviders, "local") {
		t.Fatalf("expected enabled search providers to include icodef and local, got %+v", resolved.EnabledSearchProviders)
	}
	if resolved.RequestID == "" {
		t.Fatal("expected authenticated request to have request id")
	}
	if resolved.SearchMode != searchModeNormal {
		t.Fatalf("expected authenticated request search mode %s, got %s", searchModeNormal, resolved.SearchMode)
	}
	if resolved.UserID != token.UserID {
		t.Fatalf("expected authenticated request user id %d, got %d", token.UserID, resolved.UserID)
	}
	if resolved.AccessTokenID != token.ID {
		t.Fatalf("expected authenticated request access token id %d, got %d", token.ID, resolved.AccessTokenID)
	}
}

func TestResolveSearchRequestContextWithDBUpdatesAccessTokenUsageMetadata(t *testing.T) {
	db := newSearchControllerTestDB(t)
	token := seedControllerAccessToken(t, db, "usage-token")
	before := time.Now().Add(-time.Second)

	resolved, err := resolveSearchRequestContextWithDB(db, "Basic usage-token", "203.0.113.9", url.Values{})
	if err != nil {
		t.Fatalf("resolve search context: %v", err)
	}
	if resolved.AccessTokenCtx == nil {
		t.Fatal("expected access token context for authenticated request")
	}

	var stored entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&stored).Error; err != nil {
		t.Fatalf("load access token: %v", err)
	}
	if stored.LastUsedAt == nil {
		t.Fatal("expected last used at to be updated")
	}
	if stored.LastUsedAt.Before(before) {
		t.Fatalf("expected last used at after %v, got %v", before, stored.LastUsedAt)
	}
	if stored.LastUsedIP != "203.0.113.9" {
		t.Fatalf("expected last used ip 203.0.113.9, got %s", stored.LastUsedIP)
	}
}

func TestResolveSearchRequestContextWithDBMarksAISearchMode(t *testing.T) {
	db := newSearchControllerTestDB(t)
	resolved, err := resolveSearchRequestContextWithDB(db, "", "", url.Values{"use": []string{"ai"}})
	if err != nil {
		t.Fatalf("resolve search context: %v", err)
	}
	if resolved.SearchMode != searchModeAI {
		t.Fatalf("expected ai search mode %s, got %s", searchModeAI, resolved.SearchMode)
	}
	if resolved.RequestID == "" {
		t.Fatal("expected ai search mode request to have request id")
	}
}

func TestShouldUseAISearchRequiresUsableConfigAndAllowsDefaultSearchMix(t *testing.T) {
	config := service.AISearchRequestConfig{
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	}

	if !shouldUseAISearch("", config) {
		t.Fatal("expected empty use query to enable ai search when config is usable")
	}
	if !shouldUseAISearch("icodef", config) {
		t.Fatal("expected normal provider use query to keep ai search enabled for mixed search")
	}
	if !shouldUseAISearch("ai", config) {
		t.Fatal("expected explicit ai use query to enable ai search")
	}
	if shouldUseAISearch("", service.AISearchRequestConfig{}) {
		t.Fatal("expected unusable ai config not to enable ai search")
	}
}

func TestSearchUsesAIFlowDuringDefaultSearchWhenConfigUsable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	aiCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				RequestID:                  "req-handler-default-mixed",
				EnabledSearchProviders:     []string{"stub-0"},
				UsedDefaultSearchProviders: []string{"stub-0"},
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "default-model",
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){(&stubSearchClient{enable: true, answer: [][]string{{"普通答案"}}}).SearchAnswer}
		},
		func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			aiCalls++
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}

			var resp model.SearchResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			flat := make([]string, 0)
			for _, group := range resp.Answer.AllAnswer {
				flat = append(flat, group...)
			}
			if !containsString(flat, "AI答案") {
				t.Fatalf("expected ai raw answer to be included during default search, got %+v", resp.Answer)
			}
			if !containsString(flat, "普通答案") {
				t.Fatalf("expected normal provider raw answer to be included during default search, got %+v", resp.Answer)
			}
		})

	if aiCalls != 1 {
		t.Fatalf("expected ai flow to execute once during default mixed search, got %d", aiCalls)
	}
}

func TestSearchPreservesChaoxingCompletionStyleTextAnswerFromAI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				RequestID:  "req-type2-ai-text",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "default-model",
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			if req.Type != 2 {
				t.Fatalf("expected completion/line style request type 2, got %d", req.Type)
			}
			if len(req.Options) != 0 {
				t.Fatalf("expected no objective options for completion style request, got %v", req.Options)
			}
			return [][]string{{"左侧术语对应右侧案例"}}, searchpkg.AISearchUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"请完成连线","type":2}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}

			var resp model.SearchResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if got := resp.Answer.AnswerText; got != "左侧术语对应右侧案例" {
				t.Fatalf("expected completion style answer text to be preserved, got %q", got)
			}
			if !containsString(resp.Answer.BestAnswer, "左侧术语对应右侧案例") {
				t.Fatalf("expected best answer to preserve ai text answer, got %+v", resp.Answer)
			}
			if len(resp.Answer.AnswerKey) != 0 {
				t.Fatalf("expected no objective answer keys for completion style ai answer, got %+v", resp.Answer)
			}
		})
}

func TestSearchPreservesChaoxingReaderStyleTextAnswerFromAIWithOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				RequestID:  "req-type4-ai-text",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "default-model",
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			if req.Type != 4 {
				t.Fatalf("expected reader/unknown style request type 4, got %d", req.Type)
			}
			return [][]string{{"先分析材料，再给出结论"}}, searchpkg.AISearchUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"请阅读材料后作答","type":4,"options":["材料一","材料二"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}

			var resp model.SearchResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if got := resp.Answer.AnswerText; got != "先分析材料，再给出结论" {
				t.Fatalf("expected reader style answer text to be preserved, got %q", got)
			}
			if !containsString(resp.Answer.BestAnswer, "先分析材料，再给出结论") {
				t.Fatalf("expected best answer to preserve reader style ai text answer, got %+v", resp.Answer)
			}
			if len(resp.Answer.AnswerKey) != 0 {
				t.Fatalf("expected no objective answer keys for reader style ai answer, got %+v", resp.Answer)
			}
		})
}

func TestSearchLogsTypeAwareAIRequestAndResponseSummaries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalWriter := gin.DefaultWriter
	originalErrorWriter := gin.DefaultErrorWriter
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	gin.DefaultWriter = &stdout
	gin.DefaultErrorWriter = &stderr
	defer func() {
		gin.DefaultWriter = originalWriter
		gin.DefaultErrorWriter = originalErrorWriter
	}()

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				RequestID:  "req-log-type2-ai",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "default-model",
				},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return nil
		},
		func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"左侧术语对应右侧案例"}}, searchpkg.AISearchUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(`{"question":"请完成连线","type":2}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
		})

	logs := stdout.String()
	if strings.Contains(stderr.String(), "渠道搜题失败") {
		t.Fatalf("expected no channel search failures, got stderr=%s", stderr.String())
	}
	checks := []string{
		"搜题路由判定 requestId=req-log-type2-ai",
		"normalModeReason=default_provider_mix_with_ai",
		"搜题请求摘要 requestId=req-log-type2-ai type=2 typeLabel=填空题/连线题",
		"渠道搜题命中 requestId=req-log-type2-ai provider=ai",
		"搜题响应摘要 requestId=req-log-type2-ai hitProvider=ai usedLocal=false usedCache=false aiEnabled=true type=2 typeLabel=填空题/连线题",
		"answerText=左侧术语对应右侧案例",
	}
	for _, check := range checks {
		if !strings.Contains(logs, check) {
			t.Fatalf("expected logs %q to contain %q", logs, check)
		}
	}
}

func TestSearchSupportsChaoxingCompressedQuestionTypesEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name                     string
		requestBody              string
		expectedType             int
		expectedInboundQuestion  string
		expectedResponseQuestion string
		expectedInboundOptions   []string
		expectedResponseOptions  []string
		expectedAnswerText       string
		expectedBestAnswer       []string
	}{
		{
			name:                     "completion maps into type 2 text workflow",
			requestBody:              `{"qid":"work-1","plat":-1,"question":"  请完成连线。 ","options":["A. 左侧术语","B. 右侧案例"],"type":2,"courseName":"创新方法学","extra":"来自 chaoxing_plus OCR"}`,
			expectedType:             2,
			expectedInboundQuestion:  "  请完成连线。 ",
			expectedResponseQuestion: "请完成连线.",
			expectedInboundOptions:   []string{"A. 左侧术语", "B. 右侧案例"},
			expectedResponseOptions:  []string{"左侧术语", "右侧案例"},
			expectedAnswerText:       "左侧术语对应右侧案例",
			expectedBestAnswer:       []string{"左侧术语对应右侧案例"},
		},
		{
			name:                     "reader and unknown map into type 4 text workflow",
			requestBody:              `{"qid":"work-2","plat":-1,"question":"请阅读材料后作答","options":["材料一","材料二"],"type":4,"courseName":"大学语文","extra":"调用侧原始类型 reader"}`,
			expectedType:             4,
			expectedInboundQuestion:  "请阅读材料后作答",
			expectedResponseQuestion: "请阅读材料后作答",
			expectedInboundOptions:   []string{"材料一", "材料二"},
			expectedResponseOptions:  []string{"材料一", "材料二"},
			expectedAnswerText:       "先分析材料，再给出结论",
			expectedBestAnswer:       []string{"先分析材料，再给出结论"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStubSearchDependencies(t,
				(&stubSearchClient{}).SearchAnswer,
				func(string, string, url.Values) (resolvedSearchRequestContext, error) {
					return resolvedSearchRequestContext{
						SearchMode: searchModeNormal,
						RequestID:  "req-chaoxing-e2e",
						AIConfig: service.AISearchRequestConfig{
							BaseURL: "https://default.example.com/v1",
							APIKey:  "default-key",
							Model:   "default-model",
						},
					}, nil
				},
				func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
					return nil
				},
				func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
					if req.Type != tt.expectedType {
						t.Fatalf("expected type %d, got %d", tt.expectedType, req.Type)
					}
					if req.Question != tt.expectedInboundQuestion {
						t.Fatalf("expected inbound question %q, got %q", tt.expectedInboundQuestion, req.Question)
					}
					if strings.TrimSpace(req.CourseName) == "" {
						t.Fatal("expected courseName to be passed through")
					}
					if strings.TrimSpace(req.Extra) == "" {
						t.Fatal("expected extra to be passed through")
					}
					if len(req.Options) != len(tt.expectedInboundOptions) {
						t.Fatalf("expected inbound options %v, got %v", tt.expectedInboundOptions, req.Options)
					}
					for i := range tt.expectedInboundOptions {
						if req.Options[i] != tt.expectedInboundOptions[i] {
							t.Fatalf("expected inbound option %d to be %q, got %q", i, tt.expectedInboundOptions[i], req.Options[i])
						}
					}
					return [][]string{tt.expectedBestAnswer}, searchpkg.AISearchUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
				},
				func() {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?noRecord=1", bytes.NewReader([]byte(tt.requestBody)))
					request.Header.Set("Content-Type", "application/json")
					ctx.Request = request

					Search(ctx)

					if recorder.Code != http.StatusOK {
						t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
					}

					var resp model.SearchResponse
					if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
						t.Fatalf("unmarshal response: %v", err)
					}
					if resp.Type != tt.expectedType {
						t.Fatalf("expected response type %d, got %d", tt.expectedType, resp.Type)
					}
					if resp.Question != tt.expectedResponseQuestion {
						t.Fatalf("expected response question %q, got %q", tt.expectedResponseQuestion, resp.Question)
					}
					if len(resp.Options) != len(tt.expectedResponseOptions) {
						t.Fatalf("expected response options %v, got %v", tt.expectedResponseOptions, resp.Options)
					}
					for i := range tt.expectedResponseOptions {
						if resp.Options[i] != tt.expectedResponseOptions[i] {
							t.Fatalf("expected response option %d to be %q, got %q", i, tt.expectedResponseOptions[i], resp.Options[i])
						}
					}
					if resp.Answer.AnswerText != tt.expectedAnswerText {
						t.Fatalf("expected answer text %q, got %q", tt.expectedAnswerText, resp.Answer.AnswerText)
					}
					if !reflect.DeepEqual(resp.Answer.BestAnswer, tt.expectedBestAnswer) {
						t.Fatalf("expected best answer %v, got %v", tt.expectedBestAnswer, resp.Answer.BestAnswer)
					}
					if len(resp.Answer.AnswerKey) != 0 || len(resp.Answer.AnswerIndex) != 0 {
						t.Fatalf("expected no objective answer mapping for text workflow, got %+v", resp.Answer)
					}
				})
		})
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestShouldEnableNormalSearchProviderUsesSystemEnabledProvidersWhenUseQueryEmpty(t *testing.T) {
	resolved := resolvedSearchRequestContext{
		EnabledSearchProviders:     []string{"wanneng", "icodef"},
		UsedDefaultSearchProviders: []string{"wanneng"},
		Tokens: map[string]string{
			"wanneng": "default-wanneng-token",
			"icodef":  "default-icodef-token",
		},
	}

	if !shouldEnableNormalSearchProvider(resolved, "", "wanneng") {
		t.Fatal("expected empty use query to enable enabled wanneng provider")
	}
	if !shouldEnableNormalSearchProvider(resolved, "", "icodef") {
		t.Fatal("expected empty use query to enable enabled icodef provider")
	}
	if shouldEnableNormalSearchProvider(resolved, "", "local") {
		t.Fatal("expected local provider to stay disabled when use query is empty")
	}
}

func TestShouldEnableNormalSearchProviderRejectsExplicitCredentialProviderWithoutResolvedCredential(t *testing.T) {
	resolved := resolvedSearchRequestContext{
		EnabledSearchProviders:     []string{"icodef", "wanneng"},
		UsedDefaultSearchProviders: []string{"wanneng"},
		Tokens: map[string]string{
			"wanneng": "default-wanneng-token",
		},
	}

	if shouldEnableNormalSearchProvider(resolved, "icodef", "icodef") {
		t.Fatal("expected explicit credential-based provider without resolved credential to stay disabled")
	}
	if shouldEnableNormalSearchProvider(resolved, "icodef", "wanneng") {
		t.Fatal("expected explicit use query to disable non-matching provider")
	}
}

func TestShouldEnableNormalSearchProviderAllowsExplicitCredentialProviderWithResolvedCredential(t *testing.T) {
	resolved := resolvedSearchRequestContext{
		EnabledSearchProviders: []string{"icodef"},
		Tokens: map[string]string{
			"icodef": "request-token",
		},
	}

	if !shouldEnableNormalSearchProvider(resolved, "icodef", "icodef") {
		t.Fatal("expected explicit credential-based provider with resolved credential to be enabled")
	}
}

func TestShouldEnableNormalSearchProviderRejectsExplicitDisabledCredentialProviderWithoutRequestCredential(t *testing.T) {
	resolved := resolvedSearchRequestContext{
		EnabledSearchProviders: []string{"icodef"},
		Tokens: map[string]string{
			"icodef": "default-icodef-token",
		},
	}

	if shouldEnableNormalSearchProvider(resolved, "enncy", "enncy") {
		t.Fatal("expected disabled credential-based provider without request credential to stay disabled")
	}
}

func TestShouldEnableNormalSearchProviderAllowsExplicitCredentialProviderWithRequestCredentialEvenWhenSystemDisabled(t *testing.T) {
	resolved := resolvedSearchRequestContext{
		Tokens: map[string]string{
			"enncy": "request-enncy-token",
		},
	}

	if !shouldEnableNormalSearchProvider(resolved, "enncy", "enncy") {
		t.Fatal("expected explicit credential-based provider with request credential to be enabled even when system default is disabled")
	}
}

func TestShouldEnableNormalSearchProviderRequiresSystemEnablementForLocal(t *testing.T) {
	if shouldEnableNormalSearchProvider(resolvedSearchRequestContext{}, "local", "local") {
		t.Fatal("expected local provider to stay disabled when system config does not enable it")
	}

	resolved := resolvedSearchRequestContext{
		EnabledSearchProviders: []string{"local"},
	}
	if !shouldEnableNormalSearchProvider(resolved, "local", "local") {
		t.Fatal("expected local provider to be enabled after system config enables it")
	}
}

func TestDidUseDefaultNormalSearchProviderOnlyWhenEnabledProviderMatches(t *testing.T) {
	if didUseDefaultNormalSearchProvider("lemon", []string{"icodef"}) {
		t.Fatal("expected non-matching default provider not to count as executed")
	}
	if !didUseDefaultNormalSearchProvider("icodef", []string{"icodef"}) {
		t.Fatal("expected matching default provider to count as executed")
	}
	if !didUseDefaultNormalSearchProvider("", []string{"icodef"}) {
		t.Fatal("expected empty use query to include enabled default providers")
	}
}

func TestBuildSearchBillingContextMarksAuthenticatedNormalDefaultExecutionBillable(t *testing.T) {
	ctx := buildSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:                 searchModeNormal,
		UsedDefaultSearchProviders: []string{"icodef"},
		UserID:                     12,
		AccessTokenID:              34,
		RequestID:                  "req-normal",
	}, "icodef", false, false)

	if !ctx.UsedDefaultAccount {
		t.Fatal("expected normal request to mark used default account")
	}
	if !ctx.Billable {
		t.Fatal("expected authenticated normal default execution to be billable")
	}
	if ctx.UserID != 12 || ctx.AccessTokenID != 34 || ctx.RequestID != "req-normal" {
		t.Fatalf("expected billing context identity to be preserved, got %+v", ctx)
	}
}

func TestBuildSearchBillingContextSkipsNormalBillingWhenDefaultProviderDidNotExecute(t *testing.T) {
	ctx := buildSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:                 searchModeNormal,
		UsedDefaultSearchProviders: []string{"icodef"},
		UserID:                     12,
		AccessTokenID:              34,
		RequestID:                  "req-normal-skip",
	}, "lemon", false, false)

	if ctx.UsedDefaultAccount {
		t.Fatal("expected unmatched normal provider not to mark used default account")
	}
	if ctx.Billable {
		t.Fatal("expected unmatched normal provider not to be billable")
	}
}

func TestBuildSearchBillingContextMarksAuthenticatedAIDefaultExecutionBillable(t *testing.T) {
	ctx := buildSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:    searchModeAI,
		UsedDefaultAI: true,
		UserID:        56,
		AccessTokenID: 78,
		RequestID:     "req-ai",
	}, "ai", true, false)

	if !ctx.UsedDefaultAccount {
		t.Fatal("expected ai request to mark used default account")
	}
	if !ctx.Billable {
		t.Fatal("expected authenticated ai default execution to be billable")
	}
}

func TestBuildSearchBillingContextSkipsAIBillingWithoutDefaultAI(t *testing.T) {
	ctx := buildSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:    searchModeAI,
		UsedDefaultAI: false,
		UserID:        56,
		AccessTokenID: 78,
		RequestID:     "req-ai-explicit",
	}, "ai", true, false)

	if ctx.UsedDefaultAccount {
		t.Fatal("expected explicit ai config not to mark used default account")
	}
	if ctx.Billable {
		t.Fatal("expected explicit ai config not to be billable")
	}
}

func TestBuildSearchBillingContextSkipsAnonymousRequests(t *testing.T) {
	ctx := buildSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:                 searchModeNormal,
		UsedDefaultSearchProviders: []string{"icodef"},
		RequestID:                  "req-anon",
	}, "icodef", false, false)

	if ctx.UsedDefaultAccount {
		t.Fatal("expected anonymous request not to mark used default account")
	}
	if ctx.Billable {
		t.Fatal("expected anonymous request not to be billable")
	}
}

func TestShouldBillCompletedSearchForNormalModeWithAnswers(t *testing.T) {
	ctx := searchBillingContext{
		SearchMode: searchModeNormal,
		Billable:   true,
	}

	if !shouldBillCompletedSearch(ctx, [][]string{{"答案A"}}, false) {
		t.Fatal("expected normal mode with answers to be billable after execution")
	}
}

func TestShouldBillCompletedSearchSkipsNormalModeWithoutAnswers(t *testing.T) {
	ctx := searchBillingContext{
		SearchMode: searchModeNormal,
		Billable:   true,
	}

	if shouldBillCompletedSearch(ctx, nil, false) {
		t.Fatal("expected normal mode without answers not to be billable after execution")
	}
}

func TestShouldBillCompletedSearchForAIModeWithAnswersAndUsage(t *testing.T) {
	ctx := searchBillingContext{
		SearchMode: searchModeAI,
		Billable:   true,
	}

	if !shouldBillCompletedSearch(ctx, [][]string{{"答案A"}}, true) {
		t.Fatal("expected ai mode with answers and usage to be billable after execution")
	}
}

func TestShouldBillCompletedSearchSkipsAIModeWithoutUsage(t *testing.T) {
	ctx := searchBillingContext{
		SearchMode: searchModeAI,
		Billable:   true,
	}

	if shouldBillCompletedSearch(ctx, [][]string{{"答案A"}}, false) {
		t.Fatal("expected ai mode without usage not to be billable after execution")
	}
}

func TestShouldBillCompletedSearchSkipsNonBillableRequestContext(t *testing.T) {
	ctx := searchBillingContext{
		SearchMode: searchModeNormal,
		Billable:   false,
	}

	if shouldBillCompletedSearch(ctx, [][]string{{"答案A"}}, false) {
		t.Fatal("expected non-billable request context to remain non-billable after execution")
	}
}

func TestFinalizeSearchBillingContextPreservesEligibleNormalBilling(t *testing.T) {
	ctx := searchBillingContext{
		UserID:             12,
		AccessTokenID:      34,
		RequestID:          "req-final-normal",
		SearchMode:         searchModeNormal,
		UsedDefaultAccount: true,
		Billable:           true,
	}

	finalized := finalizeSearchBillingContext(ctx, [][]string{{"答案A"}}, false)

	if !finalized.Billable {
		t.Fatal("expected eligible normal billing context to remain billable")
	}
	if !finalized.UsedDefaultAccount {
		t.Fatal("expected finalized context to preserve default account usage fact")
	}
	if finalized.UserID != 12 || finalized.AccessTokenID != 34 || finalized.RequestID != "req-final-normal" {
		t.Fatalf("expected finalized billing context identity to be preserved, got %+v", finalized)
	}
}

func TestFinalizeSearchBillingContextDowngradesAIBillingWithoutUsage(t *testing.T) {
	ctx := searchBillingContext{
		UserID:             56,
		AccessTokenID:      78,
		RequestID:          "req-final-ai",
		SearchMode:         searchModeAI,
		UsedDefaultAccount: true,
		Billable:           true,
	}

	finalized := finalizeSearchBillingContext(ctx, [][]string{{"答案A"}}, false)

	if finalized.Billable {
		t.Fatal("expected ai billing context without usage to be downgraded after execution")
	}
	if !finalized.UsedDefaultAccount {
		t.Fatal("expected finalized ai context to preserve default account usage fact")
	}
	if finalized.UserID != 56 || finalized.AccessTokenID != 78 || finalized.RequestID != "req-final-ai" {
		t.Fatalf("expected finalized ai billing context identity to be preserved, got %+v", finalized)
	}
}

func TestFinalizeSearchBillingContextKeepsNonBillableContextFalse(t *testing.T) {
	ctx := searchBillingContext{
		UserID:             90,
		AccessTokenID:      12,
		RequestID:          "req-final-skip",
		SearchMode:         searchModeNormal,
		UsedDefaultAccount: false,
		Billable:           false,
	}

	finalized := finalizeSearchBillingContext(ctx, [][]string{{"答案A"}}, false)

	if finalized.Billable {
		t.Fatal("expected finalized non-billable context to remain false")
	}
	if finalized.UserID != 90 || finalized.AccessTokenID != 12 || finalized.RequestID != "req-final-skip" {
		t.Fatalf("expected finalized non-billable context identity to be preserved, got %+v", finalized)
	}
}

func TestBuildCompletedSearchBillingContextMarksNormalDefaultExecutionWithAnswersBillable(t *testing.T) {
	ctx := buildCompletedSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:                 searchModeNormal,
		UsedDefaultSearchProviders: []string{"icodef"},
		UserID:                     12,
		AccessTokenID:              34,
		RequestID:                  "req-complete-normal",
	}, "icodef", false, [][]string{{"答案A"}}, false, false)

	if !ctx.UsedDefaultAccount {
		t.Fatal("expected completed normal billing context to record default account usage")
	}
	if !ctx.Billable {
		t.Fatal("expected completed normal billing context with answers to be billable")
	}
	if ctx.UserID != 12 || ctx.AccessTokenID != 34 || ctx.RequestID != "req-complete-normal" {
		t.Fatalf("expected completed normal billing context identity to be preserved, got %+v", ctx)
	}
}

func TestBuildCompletedSearchBillingContextDowngradesAIDefaultExecutionWithoutUsage(t *testing.T) {
	ctx := buildCompletedSearchBillingContext(resolvedSearchRequestContext{
		SearchMode:    searchModeAI,
		UsedDefaultAI: true,
		UserID:        56,
		AccessTokenID: 78,
		RequestID:     "req-complete-ai",
	}, "ai", true, [][]string{{"答案A"}}, false, false)

	if !ctx.UsedDefaultAccount {
		t.Fatal("expected completed ai billing context to preserve default account usage")
	}
	if ctx.Billable {
		t.Fatal("expected completed ai billing context without usage not to be billable")
	}
	if ctx.UserID != 56 || ctx.AccessTokenID != 78 || ctx.RequestID != "req-complete-ai" {
		t.Fatalf("expected completed ai billing context identity to be preserved, got %+v", ctx)
	}
}

func TestReportCompletedSearchBillingPublishesFinalizedContext(t *testing.T) {
	original := searchBillingReporter
	defer func() {
		searchBillingReporter = original
	}()

	var reported searchBillingContext
	searchBillingReporter = func(ctx searchBillingContext) {
		reported = ctx
	}

	reportCompletedSearchBilling(resolvedSearchRequestContext{
		SearchMode:                 searchModeNormal,
		UsedDefaultSearchProviders: []string{"icodef"},
		UserID:                     12,
		AccessTokenID:              34,
		RequestID:                  "req-report-normal",
	}, "icodef", false, [][]string{{"答案A"}}, false, false)

	if !reported.UsedDefaultAccount {
		t.Fatal("expected reported billing context to preserve default account usage")
	}
	if !reported.Billable {
		t.Fatal("expected reported billing context to be billable for eligible normal search")
	}
	if reported.UserID != 12 || reported.AccessTokenID != 34 || reported.RequestID != "req-report-normal" {
		t.Fatalf("expected reported billing context identity to be preserved, got %+v", reported)
	}
}

func TestReportCompletedSearchBillingPublishesDowngradedAIBillingContext(t *testing.T) {
	original := searchBillingReporter
	defer func() {
		searchBillingReporter = original
	}()

	var reported searchBillingContext
	searchBillingReporter = func(ctx searchBillingContext) {
		reported = ctx
	}

	reportCompletedSearchBilling(resolvedSearchRequestContext{
		SearchMode:    searchModeAI,
		UsedDefaultAI: true,
		UserID:        56,
		AccessTokenID: 78,
		RequestID:     "req-report-ai",
	}, "ai", true, [][]string{{"答案A"}}, false, false)

	if !reported.UsedDefaultAccount {
		t.Fatal("expected reported ai billing context to preserve default account usage")
	}
	if reported.Billable {
		t.Fatal("expected reported ai billing context without usage not to be billable")
	}
	if reported.UserID != 56 || reported.AccessTokenID != 78 || reported.RequestID != "req-report-ai" {
		t.Fatalf("expected reported ai billing context identity to be preserved, got %+v", reported)
	}
}
