package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func TestScriptActivationCodeKeepsOnlyTheFirstClientAfterRecordedSearch(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}, &entity.ScriptActivationEntitlement{}, &entity.ScriptSubsidizedUsage{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "one-run-owner", "user")
	admin := seedControllerUser(t, db, 0, "one-run-uploader", "admin")
	if err := db.Model(&owner).Updates(map[string]any{"balance": 1, "phone": "13800000000", "wechat_id": "one-run-owner"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetScriptActivationCodePriceWithDB(db, admin.ID, "1"); err != nil {
		t.Fatal(err)
	}
	purchase, err := service.PurchaseScriptActivationCodeWithDB(db, owner.ID, "one-run-purchase", []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	entitlement, err := service.RedeemScriptActivationCodeWithDB(db, owner.ID, purchase.Code)
	if err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "one-run-v1", "fixture.exe", []byte("MZfixture"), true)
	if err != nil {
		t.Fatal(err)
	}
	const host = "console.test"
	script, err := service.CreateUserScriptWithDB(db, owner, service.CreateUserScriptInput{
		Name: "one-run", VersionID: version.ID, BillingMode: entity.ScriptBillingModeActivation,
		EntitlementID: entitlement.ID, LauncherOrigin: &service.ScriptLauncherOrigin{Host: host, HTTPS: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := service.ConfigureScriptShortAuthorizationInput{Enabled: true, Address: host + "/short-entry", RequestHost: host, DurationMinutes: 10}
	short, err := service.ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	shortAddress, err := url.Parse(short.Address)
	if err != nil {
		t.Fatal(err)
	}
	fullAddress, err := url.Parse(script.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := service.IssuePublicScriptExchangeTokenByLocatorWithDB(db, fullAddress.Host, fullAddress.Path, 0)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := service.ExchangeScriptTokenWithDB(db, script.ShortCode, exchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, shortExchange, err := service.ConsumeScriptShortAuthorizationWithDB(db, shortAddress.Host, shortAddress.Path)
	if err != nil {
		t.Fatal(err)
	}
	pendingTicket, err := service.ExchangeScriptTokenWithDB(db, script.ShortCode, shortExchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	const ip = "127.0.0.1"
	firstID, secondID := strings.Repeat("12", 32), strings.Repeat("34", 32)
	session, err := service.ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, ticket.ActivationToken, firstID, ip, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2.5)
	providerCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			t.Error("script credential must use the script resolver")
			return resolvedSearchRequestContext{}, service.ErrInvalidAccessTokenAuthorization
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				providerCalls++
				return [][]string{{"A"}}, nil
			}}
		},
		nil,
		func() {
			router := gin.New()
			router.POST("/api/search", SearchWithIdempotency)
			search := func(identity, requestID string, expectedStatus int) {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/api/search?use=icodef&noRecord=1", strings.NewReader(`{"question":"fixture","options":["A","B"],"skipCache":true}`))
				req.RemoteAddr = ip + ":12345"
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+session.Token)
				req.Header.Set("X-Script-Identity", identity)
				req.Header.Set("Idempotency-Key", requestID)
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)
				if res.Code != expectedStatus {
					t.Fatalf("search %s: status=%d, want %d, body=%s", requestID, res.Code, expectedStatus, res.Body)
				}
			}
			search(firstID, "first-search", http.StatusOK)
			var logged int64
			if err := db.Model(&entity.Log{}).Where("module = ? AND user_id = ? AND action = ?", "search", owner.ID, logActionSearch).Count(&logged).Error; err != nil || logged != 1 {
				t.Fatalf("first search did not write its audit record: count=%d, err=%v", logged, err)
			}
			search(secondID, "second-device-search", http.StatusUnauthorized)
			search(firstID, "continued-search", http.StatusOK)
			if providerCalls != 2 {
				t.Fatalf("only the original client should execute searches, calls=%d", providerCalls)
			}
		},
	)
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", script.ID).Updates(map[string]any{"lease_slot": nil, "expires_at": time.Now().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{firstID, secondID} {
		if _, err := service.ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, ticket.ActivationToken, identity, ip, 0); !errors.Is(err, service.ErrScriptTokenInvalid) {
			t.Fatalf("spent activation ticket started a session after release: %v", err)
		}
	}
	if _, err := service.ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, pendingTicket.ActivationToken, secondID, ip, 0); !errors.Is(err, service.ErrScriptActivationRunConsumed) {
		t.Fatalf("pending ticket for another device started after release: %v", err)
	}
	if _, err := service.IssuePublicScriptExchangeTokenByLocatorWithDB(db, fullAddress.Host, fullAddress.Path, 0); !errors.Is(err, service.ErrScriptActivationRunConsumed) {
		t.Fatalf("full command launched after release: %v", err)
	}
	if _, _, err := service.ConsumeScriptShortAuthorizationWithDB(db, shortAddress.Host, shortAddress.Path); !errors.Is(err, service.ErrScriptActivationRunConsumed) {
		t.Fatalf("short command launched after release: %v", err)
	}
	now := time.Now()
	input.Windows = []service.ScriptAuthorizationWindow{
		{StartsAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)},
		{StartsAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(3 * time.Minute)},
	}
	input.DurationMinutes = 0
	configure := []func(*gorm.DB, entity.User, int32, service.ConfigureScriptShortAuthorizationInput) (service.ScriptShortAuthorizationView, error){
		service.ConfigureScriptLauncherAuthorizationWithDB, service.ConfigureScriptShortAuthorizationWithDB,
	}
	for _, update := range configure {
		if _, err := update(db, owner, script.ID, input); !errors.Is(err, service.ErrScriptActivationRunConsumed) {
			t.Fatalf("multi-window reauthorization restored the spent entitlement: %v", err)
		}
	}
}
