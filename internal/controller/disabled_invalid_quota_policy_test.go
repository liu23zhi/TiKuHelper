package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
)

func TestDisabledInvalidQuotaPolicyRealJSONHandler(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       int
	}{{"omitted", `{"name":"disabled_bad","remark":"after"}`, 200}, {"missing_enable", `{"name":"disabled_bad","status":"enabled"}`, 400}, {"null_enable", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":null}`, 400}, {"explicit_zero", `{"name":"disabled_bad","status":"enabled","totalQuotaLimit":0}`, 200}} {
		t.Run(c.name, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			u := seedControllerUser(t, db, 705, "dq_http", "user")
			k := seedControllerTokenForUser(t, db, u, "tk_synthetic_disabled_http", 0)
			if err := db.Model(&k).Updates(map[string]any{"name": "disabled_bad", "status": "disabled", "total_quota_limit": -2}).Error; err != nil {
				t.Fatal(err)
			}
			old := updateAccessToken
			updateAccessToken = func(op entity.User, id int32, in service.UpdateAccessTokenInput) (entity.AccessToken, error) {
				return service.UpdateAccessTokenWithDB(db, op, id, in)
			}
			defer func() { updateAccessToken = old }()
			out := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(out)
			ctx.Set("user", &u)
			ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(k.ID))}}
			ctx.Request = httptest.NewRequest(http.MethodPut, "/api/access-tokens/"+strconv.Itoa(int(k.ID)), bytes.NewBufferString(c.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			UpdateAccessToken(ctx)
			if out.Code != c.want {
				t.Errorf("handler status=%d want=%d", out.Code, c.want)
			}
			var after entity.AccessToken
			if err := db.First(&after, k.ID).Error; err != nil {
				t.Fatal(err)
			}
			if c.name != "explicit_zero" && (after.TotalQuotaLimit != -2 || after.Status != "disabled") {
				t.Error("omitted/null quota became unlimited or enabled")
			}
			if c.name == "explicit_zero" && (after.TotalQuotaLimit != 0 || after.Status != "enabled") {
				t.Error("explicit legal zero correction did not enable")
			}
			var payload any
			if err := json.Unmarshal(out.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDisabledInvalidQuotaPolicyFreshSpendingGuards(t *testing.T) {
	for _, limit := range []float64{-2, 1e24, math.NaN(), math.Inf(1)} {
		k := entity.AccessToken{TotalQuotaLimit: limit}
		if !accessTokenQuotaExhausted(k) || !accessTokenQuotaWouldExceed(k, 0.01) {
			t.Error("invalid limit treated as unlimited")
		}
	}
	db := newSearchControllerTestDB(t)
	u := seedControllerUser(t, db, 706, "dq_billing", "user")
	if err := db.Model(&u).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	u.Balance = 10
	k := seedControllerTokenForUser(t, db, u, "tk_synthetic_disabled_billing", 0)
	if err := db.Model(&k).Update("total_quota_limit", -2).Error; err != nil {
		t.Fatal(err)
	}
	var before entity.AccessToken
	if err := db.First(&before, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	err := persistSearchBillingRecordsWithDB(db, searchBillingContext{UserID: u.ID, AccessTokenID: k.ID, RequestID: "dq-invalid-fresh"}, []searchBillingCharge{{Provider: "icodef", Amount: 0.25}})
	if !errors.Is(err, errAccessTokenQuotaExceeded) {
		t.Errorf("fresh invalid quota not rejected: %v", err)
	}
	var after entity.AccessToken
	var wallet entity.User
	db.First(&after, k.ID)
	db.First(&wallet, u.ID)
	var rows int64
	db.Model(&entity.BillingRecord{}).Count(&rows)
	if !reflect.DeepEqual(before, after) || wallet.Balance != 10 || rows != 0 {
		t.Error("invalid fresh quota mutated ledger/wallet/token")
	}
}
