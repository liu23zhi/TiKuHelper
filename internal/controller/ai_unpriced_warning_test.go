package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// 未配置价格的模型：答案必须照常返回，同时带一条非阻断提示让管理员补价。
func TestSearchReturnsWarningWhenAIModelIsNotPriced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 270, "search-unpriced-ai-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10.0).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-unpriced-ai-token", 0)
	// 刻意不写价格表：模型名不在默认价格表里。
	const unpricedModel = "gpt-model-without-price"

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeAI,
				UsedDefaultAI: true,
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-search-unpriced-ai",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   unpricedModel,
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
			return [][]string{{"A"}}, searchpkg.AISearchUsage{InputTokens: 1000000, OutputTokens: 500000, TotalTokens: 1500000}, nil
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

			var response model.SearchResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v (body %s)", err, recorder.Body.String())
			}

			// 关键：未定价不阻断作答（带选项的选择题会被格式化成选项字母）。
			if len(response.Answer.AllAnswer) == 0 || len(response.Answer.AllAnswer[0]) == 0 ||
				response.Answer.AllAnswer[0][0] != "A" {
				t.Fatalf("expected raw AI answer to be preserved, got %+v", response.Answer.AllAnswer)
			}
			if response.Answer.AnswerText == "" {
				t.Fatalf("expected a usable answer text despite unpriced model, got %+v", response.Answer)
			}
			if len(response.Warnings) != 1 {
				t.Fatalf("expected exactly one warning, got %+v (body %s)", response.Warnings, recorder.Body.String())
			}
			warning := response.Warnings[0]
			for _, fragment := range []string{unpricedModel, "尚未配置价格", "模型价格表"} {
				if !strings.Contains(warning, fragment) {
					t.Fatalf("expected warning %q to contain %q", warning, fragment)
				}
			}
		})

	// 未定价模型按 0 计费：余额不变，且写入一条可审计的零金额账单。
	var stored entity.User
	if err := db.Where("id = ?", user.ID).First(&stored).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.Balance != 10 {
		t.Fatalf("expected balance to stay 10 for unpriced model, got %v", stored.Balance)
	}

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND provider = ?", user.ID, searchModeAI).First(&record).Error; err != nil {
		t.Fatalf("load zero-amount ai billing record: %v", err)
	}
	if record.Amount != 0 {
		t.Fatalf("expected zero amount, got %v", record.Amount)
	}
	if !strings.Contains(record.UnitPriceSnapshot, "pricing=unpriced_model") {
		t.Fatalf("expected unpriced marker in snapshot, got %q", record.UnitPriceSnapshot)
	}
}

// 已配置价格的模型不应出现任何提示。
func TestSearchOmitsWarningWhenAIModelIsPriced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 271, "search-priced-ai-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10.0).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-priced-ai-token", 0)
	seedControllerAIModelPrice(t, db, "GPT-6 Luna", 0.10, 0.01, 0.50)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeAI,
				UsedDefaultAI: true,
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-search-priced-ai",
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
			return nil
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1000000, OutputTokens: 500000, TotalTokens: 1500000}, nil
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			Search(ctx)

			var response model.SearchResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v (body %s)", err, recorder.Body.String())
			}
			if len(response.Warnings) != 0 {
				t.Fatalf("expected no warnings for priced model, got %+v", response.Warnings)
			}
			// 正常响应不应出现 warnings 字段，避免影响既有客户端。
			if strings.Contains(recorder.Body.String(), "warnings") {
				t.Fatalf("expected warnings field to be omitted, got %s", recorder.Body.String())
			}
		})
}

// 命中搜索结果缓存时不调用 AI，因此不应出现未定价提示。
func TestSearchOmitsUnpricedWarningOnResultCacheHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 273, "search-unpriced-cache-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10.0).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-unpriced-cache-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 1.0)

	aiCalls := 0
	body := []byte(`{"question":"同一道未定价缓存题","options":["A","B"]}`)

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
				RequestID:                  "req-search-unpriced-cache",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "gpt-model-without-price",
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
			aiCalls++
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{InputTokens: 1000000, OutputTokens: 500000, TotalTokens: 1500000}, nil
		},
		func() {
			// 第一次：真实执行，带未定价提示。
			first := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(first)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Basic "+token.TokenValue)
			ctx.Request = request
			Search(ctx)

			var firstResponse model.SearchResponse
			if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
				t.Fatalf("decode first response: %v (body %s)", err, first.Body.String())
			}
			if len(firstResponse.Warnings) != 1 {
				t.Fatalf("expected one warning on first call, got %+v (body %s)", firstResponse.Warnings, first.Body.String())
			}

			callsAfterFirst := aiCalls

			// 第二次：命中缓存，不调用 AI，也不应提示。
			second := httptest.NewRecorder()
			ctx2, _ := gin.CreateTestContext(second)
			request2 := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef", bytes.NewReader(body))
			request2.Header.Set("Content-Type", "application/json")
			request2.Header.Set("Authorization", "Basic "+token.TokenValue)
			ctx2.Request = request2
			Search(ctx2)

			var secondResponse model.SearchResponse
			if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
				t.Fatalf("decode second response: %v (body %s)", err, second.Body.String())
			}
			if aiCalls != callsAfterFirst {
				t.Fatalf("expected cached call to skip AI, aiCalls %d -> %d", callsAfterFirst, aiCalls)
			}
			if len(secondResponse.Warnings) != 0 {
				t.Fatalf("expected no warnings on cache hit, got %+v", secondResponse.Warnings)
			}
		})
}

// AI 兜底（chaoxing_plus 走的链路）同样要提示未定价模型。
func TestAIFallbackReturnsWarningWhenModelIsNotPriced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 272, "fallback-unpriced-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "fallback-unpriced-token", 0)

	const unpricedModel = "fallback-unpriced-model"
	original := runAIFallbackService
	defer func() { runAIFallbackService = original }()
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		return service.AIFallbackResult{
			Success: true,
			Result:  &service.AIFallbackAnswer{Question: req.Question, Answer: "2"},
			Model:   unpricedModel,
			Usage: searchpkg.AISearchUsage{
				InputTokens:  1000000,
				OutputTokens: 500000,
				TotalTokens:  1500000,
			},
		}
	}

	router := gin.New()
	router.POST("/adapter-service/ai-fallback", AuthOrAccessToken, AIFallback)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString(`{"question":"1+1=?","type":0,"options":["1","2"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.TokenValue)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var got service.AIFallbackResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !got.Success || got.Result == nil || got.Result.Answer != "2" {
		t.Fatalf("expected answer to still be returned, got %+v", got)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("expected one warning, got %+v (body %s)", got.Warnings, recorder.Body.String())
	}
	if !strings.Contains(got.Warnings[0], unpricedModel) {
		t.Fatalf("expected warning to name the model, got %q", got.Warnings[0])
	}
}
