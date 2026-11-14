package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestScriptSearchPaidIdempotencyReplayAfterDebtAndHardQuota(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	user := seedControllerUser(t, db, 0, "script-paid-fixture", "user")
	admin := seedControllerUser(t, db, 0, "script-admin-fixture", "admin")
	if err := db.Model(&user).Updates(map[string]any{"balance": 20, "phone": "13800000000", "wechat_id": "synthetic-search-owner"}).Error; err != nil {
		t.Fatal(err)
	}
	version, err := service.UploadScriptVersionWithDB(db, admin, "fixture-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := service.CreateUserScriptWithDB(db, user, service.CreateUserScriptInput{Name: "fixture", VersionID: version.ID, BaseDomain: "fixture.test"})
	if err != nil {
		t.Fatal(err)
	}
	issue := func(identity string) string {
		t.Helper()
		grant, err := service.IssueScriptExchangeTokenWithDB(db, user, script.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		exchange, err := service.ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token)
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ActivateScriptRunSessionWithDB(db, script.ShortCode, exchange.ActivationToken, identity)
		if err != nil {
			t.Fatal(err)
		}
		return session.Token
	}
	firstToken, renewedToken, otherToken := issue("fixture-a"), issue("fixture-a"), issue("fixture-b")
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2.5)
	if err := service.SetTikuSearchPricePerCallWithDB(db, 2.5); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&user).Update("balance", 0.25).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", script.AccessTokenID).Update("total_quota_limit", 2.499999).Error; err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			t.Error("script credential must not downgrade to ordinary/anonymous resolver")
			return resolvedSearchRequestContext{}, service.ErrInvalidAccessTokenAuthorization
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				providerCalls.Add(1)
				return [][]string{{"A"}}, nil
			}}
		},
		nil,
		func() {
			router := gin.New()
			router.POST("/api/search", SearchWithIdempotency)
			call := func(token, identity, idempotencyKey string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/api/search?use=icodef&noRecord=1", strings.NewReader(`{"question":"fixture","options":["A","B"],"skipCache":true}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("X-Script-Identity", identity)
				req.Header.Set("Idempotency-Key", idempotencyKey)
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)
				return res
			}
			underLimit := call(firstToken, "fixture-a", "fixed-fee-does-not-fit-fixture")
			if underLimit.Code != http.StatusUnauthorized || providerCalls.Load() != 0 {
				t.Fatalf("known fee one millionth above the Key limit must fail before execution, HTTP %d calls=%d", underLimit.Code, providerCalls.Load())
			}
			if err := db.Model(&entity.AccessToken{}).Where("id = ?", script.AccessTokenID).Update("total_quota_limit", 2.5).Error; err != nil {
				t.Fatal(err)
			}
			first := call(firstToken, "fixture-a", "paid-fixture")
			if first.Code != http.StatusOK || providerCalls.Load() != 1 {
				t.Fatalf("first admitted execution must deliver, got HTTP %d, provider calls=%d", first.Code, providerCalls.Load())
			}
			replayed := call(renewedToken, "fixture-a", "paid-fixture")
			if replayed.Code != first.Code || replayed.Body.String() != first.Body.String() {
				t.Fatalf("same identity with a renewed session must receive the same paid response, got HTTP %d", replayed.Code)
			}
			for _, result := range []*httptest.ResponseRecorder{
				call(firstToken, "fixture-b", "paid-fixture"),
				call(otherToken, "fixture-b", "paid-fixture"),
				call(firstToken, "fixture-a", "new-spend-fixture"),
			} {
				if result.Code != http.StatusUnauthorized {
					t.Fatalf("different identity or new spend may not replay paid result, got HTTP %d", result.Code)
				}
			}
			var stored entity.User
			if err := db.First(&stored, user.ID).Error; err != nil || stored.Balance != -2.25 {
				t.Fatalf("actual frozen fee must settle despite debt; balance=%.6f err=%v", stored.Balance, err)
			}
			if err := db.Model(&user).Update("balance", 1).Error; err != nil {
				t.Fatal(err)
			}
			exhausted := call(firstToken, "fixture-a", "new-hard-limit-fixture")
			var bills int64
			if err := db.Model(&entity.BillingRecord{}).Where("access_token_id = ?", script.AccessTokenID).Count(&bills).Error; err != nil {
				t.Fatal(err)
			}
			if exhausted.Code != http.StatusUnauthorized || bills != 1 || providerCalls.Load() != 1 {
				t.Fatalf("exhausted Key cannot execute again, got HTTP %d, bills=%d, provider calls=%d", exhausted.Code, bills, providerCalls.Load())
			}
		},
	)
}

func TestScriptSearchTaskScopeDoesNotBypassOwnerOrReplayAdmission(t *testing.T) {
	store := newSearchTaskStore(time.Minute, time.Now)
	var executions atomic.Int32
	run := func() (model.SearchResponse, error) {
		executions.Add(1)
		return model.SearchResponse{}, nil
	}
	task, reused, err := store.createWithActorAdmission(1, "fixture-actor-a", "same-key", "request", nil, run)
	if err != nil || reused {
		t.Fatalf("create initial script task: reused=%v err=%v", reused, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := searchExecutionOwners.wait(ctx); err != nil {
		t.Fatal(err)
	}
	replay, reused, err := store.createWithActorAdmission(1, "fixture-actor-a", "same-key", "request", func() error {
		return service.ErrInsufficientSearchBalance
	}, run)
	if err != nil || !reused || task != replay || executions.Load() != 1 {
		t.Fatalf("owner-bound replay must not run admission/work twice, reused=%v executions=%d err=%v", reused, executions.Load(), err)
	}
	if _, err := store.getWithActor(task, 1, "fixture-actor-b"); err != errSearchTaskForbidden {
		t.Fatalf("same account's unrelated script identity may not read the task: %v", err)
	}
	if _, err := store.getWithActor(task, 2, "fixture-actor-a"); err != errSearchTaskForbidden {
		t.Fatalf("different account may not read the task: %v", err)
	}
}
