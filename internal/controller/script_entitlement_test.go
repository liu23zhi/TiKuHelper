package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestScriptActivationHTTPChecksFreshAdminAndKeepsCodeOwnerBound(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.ScriptActivationEntitlement{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "activation-code-owner", "user")
	other := seedControllerUser(t, db, 0, "activation-code-other", "user")
	admin := seedControllerUser(t, db, 0, "activation-code-admin", "admin")
	if err := db.Model(&owner).Update("balance", 25).Error; err != nil {
		t.Fatal(err)
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	staleAdmin := owner
	staleAdmin.Role = "admin"

	router := gin.New()
	// Stub only the identity projection; exercise each real handler/DB path.
	router.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-Actor") {
		case "owner":
			c.Set("user", &owner)
		case "other":
			c.Set("user", &other)
		case "admin":
			c.Set("user", &admin)
		case "stale-admin":
			c.Set("user", &staleAdmin)
		}
	})
	const priceURL = "/activation-codes/price"
	const purchaseURL = "/activation-codes/purchase"
	const redeemURL = "/activation-codes/redeem"
	router.GET(priceURL, GetScriptActivationCodePrice)
	router.PUT(priceURL, PutScriptActivationCodePrice)
	router.POST(purchaseURL, PurchaseScriptActivationCode)
	router.POST(redeemURL, RedeemScriptActivationCode)

	request := func(method, path, actor, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Actor", actor)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s actor=%s status=%d want=%d cache=%q", method, path, actor, rec.Code, want, rec.Header().Get("Cache-Control"))
		}
		return rec
	}
	request(http.MethodPost, purchaseURL, "", "unit-test-purchase", "", http.StatusUnauthorized)
	request(http.MethodPut, priceURL, "owner", "", `{"price":"12.345678"}`, http.StatusForbidden)
	request(http.MethodPut, priceURL, "stale-admin", "", `{"price":12.345678}`, http.StatusForbidden)
	request(http.MethodPut, priceURL, "admin", "", `{"price":"1.0000001"}`, http.StatusBadRequest)
	request(http.MethodPut, priceURL, "admin", "", `{"price":12.345678}`, http.StatusOK)
	price := request(http.MethodGet, priceURL, "owner", "", "", http.StatusOK)
	if !strings.Contains(price.Body.String(), `"enabled":true`) || !strings.Contains(price.Body.String(), `"price":12.345678`) {
		t.Fatal("enabled purchase price not shown")
	}
	request(http.MethodPost, purchaseURL, "owner", "unit-test-purchase", "", http.StatusServiceUnavailable)
	signingKey := "synthetic-jwt-activation-key-do-not-deploy"
	if err := db.Create(&entity.SystemSetting{Key: registry.JWTSecretSettingKey, Value: signingKey}).Error; err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, purchaseURL, "owner", "", "", http.StatusBadRequest)
	changed := request(http.MethodPost, purchaseURL, "owner", "stale-quote", `{"quotedPrice":"13.000000"}`, http.StatusConflict)
	if !strings.Contains(changed.Body.String(), "SCRIPT_ACTIVATION_PRICE_CHANGED") {
		t.Fatal("mismatched quote did not fail closed")
	}
	purchase := request(http.MethodPost, purchaseURL, "owner", "unit-test-purchase", `{"quotedPrice":"12.345678"}`, http.StatusOK)
	retry := request(http.MethodPost, purchaseURL, "owner", "unit-test-purchase", `{"quotedPrice":"12.345678"}`, http.StatusOK)
	if purchase.Body.String() != retry.Body.String() || strings.Contains(purchase.Body.String(), signingKey) {
		t.Fatal("purchase retry changed response or exposed key")
	}
	var bought service.ScriptActivationCodePurchase
	if err := json.Unmarshal(purchase.Body.Bytes(), &bought); err != nil || bought.EntitlementID <= 0 || len(bought.Code) != 68 || bought.Price != 12.345678 {
		t.Fatal("invalid wallet purchase response")
	}
	redeemJSON, _ := json.Marshal(map[string]string{"code": bought.Code})
	wrong := request(http.MethodPost, redeemURL, "other", "", string(redeemJSON), http.StatusBadRequest)
	redeemed := request(http.MethodPost, redeemURL, "owner", "", string(redeemJSON), http.StatusOK)
	for _, rec := range []*httptest.ResponseRecorder{wrong, redeemed} {
		if strings.Contains(rec.Body.String(), bought.Code) || strings.Contains(rec.Body.String(), signingKey) || strings.Contains(rec.Body.String(), "CodeHash") {
			t.Fatal("redemption exposed private data")
		}
	}
	var grant struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(redeemed.Body.Bytes(), &grant); err != nil || grant.ID != bought.EntitlementID {
		t.Fatal("redemption did not return the bought entitlement")
	}
	var updated entity.User
	if err := db.First(&updated, owner.ID).Error; err != nil || updated.Balance != 12.654322 {
		t.Fatalf("wallet balance=%v err=%v; expected one debit", updated.Balance, err)
	}
	request(http.MethodPut, priceURL, "admin", "", `{"price":0}`, http.StatusOK)
	disabled := request(http.MethodGet, priceURL, "owner", "", "", http.StatusOK)
	if !strings.Contains(disabled.Body.String(), `"enabled":false`) {
		t.Fatal("zero did not close new sales")
	}
	request(http.MethodPost, purchaseURL, "owner", "different-new-purchase", "", http.StatusServiceUnavailable)
	request(http.MethodPost, purchaseURL, "owner", "unit-test-purchase", "", http.StatusOK)
}
