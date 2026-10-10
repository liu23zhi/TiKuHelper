package controller

import (
	"bytes"
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
	"gorm.io/gorm"
)

func seedControllerAIModelPrice(t *testing.T, db *gorm.DB, modelName string, inputPrice float64, cachedPrice float64, outputPrice float64) entity.AIModelPrice {
	t.Helper()

	created, err := service.CreateAIModelPriceWithDB(db, service.CreateAIModelPriceInput{
		ModelName:   modelName,
		InputPrice:  inputPrice,
		CachedPrice: cachedPrice,
		OutputPrice: outputPrice,
		Status:      "enabled",
	})
	if err != nil {
		t.Fatalf("seed ai model price: %v", err)
	}
	return created
}

func TestSearchBillsAIUsageFromModelPriceTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 260, "search-price-table-billing-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 100.0).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "search-price-table-billing-token", 0)
	seedControllerAIModelPrice(t, db, "GPT-5.5", 5.00, 0.50, 30.00)

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeAI,
				UsedDefaultAI: true,
				UserID:        user.ID,
				AccessTokenID: token.ID,
				RequestID:     "req-search-price-table-billing",
				AIConfig: service.AISearchRequestConfig{
					BaseURL: "https://default.example.com/v1",
					APIKey:  "default-key",
					Model:   "GPT-5.5",
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
			// 1M 输入（其中 400k 命中缓存）+ 200k 输出
			// = 600k*$5/M + 400k*$0.5/M + 200k*$30/M = 3.0 + 0.2 + 6.0 = 9.2
			return [][]string{{"AI答案"}}, searchpkg.AISearchUsage{
				InputTokens:       1000000,
				CachedInputTokens: 400000,
				OutputTokens:      200000,
				TotalTokens:       1200000,
			}, nil
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
			if !strings.Contains(recorder.Body.String(), "AI答案") {
				t.Fatalf("expected AI answer in response body, got %s", recorder.Body.String())
			}
		})

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND access_token_id = ?", user.ID, token.ID).First(&record).Error; err != nil {
		t.Fatalf("load ai billing record: %v", err)
	}
	if record.Provider != searchModeAI {
		t.Fatalf("expected ai provider, got %+v", record)
	}
	if record.Amount != 9.2 {
		t.Fatalf("expected price-table amount 9.2, got %v (snapshot %q)", record.Amount, record.UnitPriceSnapshot)
	}
	if record.ModelName != "GPT-5.5" {
		t.Fatalf("expected model name GPT-5.5, got %q", record.ModelName)
	}
	if record.InputTokens != 1000000 || record.CachedInputTokens != 400000 || record.OutputTokens != 200000 {
		t.Fatalf("expected token counts to be persisted as-is, got %+v", record)
	}

	for _, part := range []string{
		"provider=ai",
		"model=GPT-5.5",
		"pricing=model_price_table",
		"inputPrice=5.0000",
		"cachedPrice=0.5000",
		"outputPrice=30.0000",
		"inputTokens=600000",
		"cachedInputTokens=400000",
		"outputTokens=200000",
		"amount=9.20",
	} {
		if !strings.Contains(record.UnitPriceSnapshot, part) {
			t.Fatalf("expected price-table snapshot to contain %q, got %q", part, record.UnitPriceSnapshot)
		}
	}
}
