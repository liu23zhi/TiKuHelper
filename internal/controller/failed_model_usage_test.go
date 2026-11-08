package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func failedModelUsage() searchpkg.AISearchUsage {
	return searchpkg.AISearchUsage{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020,
		ModelCalls: []searchpkg.AIModelCallUsage{{Model: "failed-usage-model", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020}}}
}

func TestFailedModelUsageFallbackSettlesExecutedUsage(t *testing.T) {
	for _, executed := range []bool{true, false} {
		t.Run(fmt.Sprint(executed), func(t *testing.T) {
			db, user, key := integrationQuotaFixture(t, 0, 0, .0001)
			seedControllerAIModelPrice(t, db, "failed-usage-model", 1, .1, 2)
			staged := false
			if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &staged, nil); err != nil {
				t.Fatal(err)
			}
			result := service.AIFallbackResult{Success: false, Model: "failed-usage-model", Error: &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: "synthetic failure after summary"}}
			if executed {
				result.Usage = failedModelUsage()
			}
			billing := searchBillingContext{UserID: user.ID, AccessTokenID: key.ID, SearchMode: searchModeAI, Billable: true, UsedDefaultAccount: true}
			{
				_, got, err := persistAIFallbackBillingWithWarning(billing, "failed-usage-fallback", model.SearchRequest{Question: "mock context failure", Type: 4}, result)
				if err != nil || got.Success || got.Error == nil {
					t.Fatalf("failure result changed or settlement failed: %+v %v", got, err)
				}
			}
			var rows int64
			db.Model(&entity.BillingRecord{}).Where("request_id = ?", "failed-usage-fallback").Count(&rows)
			wantRows, wantBalance, wantUsed := int64(0), .0001, 0.0
			if executed {
				wantRows, wantBalance, wantUsed = 1, -.00085, .00095
			}
			walletSettlementAssertState(t, db, user.ID, key.ID, wantRows, wantBalance, wantUsed)
			t.Logf("FAILED_USAGE_FALLBACK executed=%t settlements=1 rows=%d wallet=%.6f used=%.6f", executed, rows, wantBalance, wantUsed)
		})
	}
}

func TestFailedModelUsageSearchRetainsActualUsageWithoutAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, user, key := integrationQuotaFixture(t, 0, 0, .0001)
	seedControllerAIModelPrice(t, db, "failed-usage-model", 1, .1, 2)
	staged := false
	if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &staged, nil); err != nil {
		t.Fatal(err)
	}
	calls := 0
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeAI, UsedDefaultAI: true, UserID: user.ID, AccessTokenID: key.ID, RequestID: "failed-usage-search",
				AIConfig:       service.AISearchRequestConfig{BaseURL: "https://example.invalid/v1", APIKey: "mock-no-outbound", Model: "failed-usage-model"},
				AccessTokenCtx: &service.AccessTokenAuthContext{User: user, AccessToken: key}}, nil
		}, nil,
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			calls++
			return nil, failedModelUsage(), errors.New("synthetic context failure after paid summary")
		}, func() {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewBufferString("{\"question\":\"synthetic context\",\"type\":4}"))
			ctx.Request.Header.Set("Content-Type", "application/json")
			Search(ctx)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "答案A") {
				t.Fatalf("unexpected search response %d %s", w.Code, w.Body.String())
			}
		})
	if calls != 1 {
		t.Fatalf("expected one mock model entry, got %d", calls)
	}
	walletSettlementAssertState(t, db, user.ID, key.ID, 1, -.00085, .00095)
	t.Log("FAILED_USAGE_SEARCH calls=1 answer=false rows=1 wallet=-0.000850 used=0.000950")
}

// Keep the pre-integration cancellation policy until a replacement is approved:
// one HTTP observer disappearing must not cancel admitted paid work.
func TestFailedModelUsageFallbackSyncRetainsAdmittedWorkPolicy(t *testing.T) {
	db, user, key := integrationQuotaFixture(t, 0, 0, .0001)
	seedControllerAIModelPrice(t, db, "failed-usage-model", 1, .1, 2)
	staged := false
	if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &staged, nil); err != nil {
		t.Fatal(err)
	}
	old := runAIFallbackService
	t.Cleanup(func() { runAIFallbackService = old })
	calls := 0
	runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
		if req.RequestContext != nil && req.RequestContext.Err() != nil {
			return service.AIFallbackResult{Success: false, Error: &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: "observer canceled"}}
		}
		calls++
		return service.AIFallbackResult{Success: false, Model: "failed-usage-model", Usage: failedModelUsage(), Error: &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: "synthetic summary failure"}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-fallback", bytes.NewBufferString("{\"question\":\"synthetic admitted fallback\",\"type\":4}")).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	AIFallback(c)
	if calls != 1 || w.Code != http.StatusOK {
		t.Fatalf("admitted work canceled: calls=%d HTTP=%d", calls, w.Code)
	}
	walletSettlementAssertState(t, db, user.ID, key.ID, 1, -.00085, 0)
	t.Log("FAILED_USAGE_SYNC observerCanceled=true executed=1 rows=1 wallet=-0.000850")
}
