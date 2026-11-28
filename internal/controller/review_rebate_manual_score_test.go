package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
)

type manualReviewFixtureScorer func(entity.ReviewRebateClaim) (service.ReviewRebateScore, error)

func (score manualReviewFixtureScorer) Score(claim entity.ReviewRebateClaim) (service.ReviewRebateScore, error) {
	return score(claim)
}

func TestReviewRebateAdminManualScoreHTTPPermissionsFailureAndRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	installReviewRebateTestDB(t, db)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 700, "t09-admin-fixture", "admin")
	member := seedReviewRebateControllerUser(t, db, 701, "t09-member-fixture", "user")
	claim, err := service.CreateReviewRebateClaimWithDB(db, service.ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: member.ID, ScreenshotSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}

	originalScore := scoreReviewRebateClaim
	t.Cleanup(func() { scoreReviewRebateClaim = originalScore })
	calls := 0
	scoreReviewRebateClaim = func(id int32) (entity.ReviewRebateClaim, error) {
		return service.ScoreReviewRebateClaimManuallyWithDB(db, id, manualReviewFixtureScorer(func(entity.ReviewRebateClaim) (service.ReviewRebateScore, error) {
			calls++
			if calls == 1 {
				return service.ReviewRebateScore{}, fmt.Errorf("%w: synthetic-private-provider-detail", service.ErrReviewRebateScoringFailed)
			}
			return service.ReviewRebateScore{Stars: 4, ContentScore: 8, Confidence: .8, Rationale: "synthetic result", Scorer: "agent"}, nil
		}))
	}
	request := func(actor *entity.User, method string, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		router := reviewRebateContractRouter(actor)
		if actor == nil {
			// Use the production auth middleware rather than the actor injection
			// shim for missing credentials (which otherwise skips the 401 gate).
			router = gin.New()
			router.POST("/admin/review-rebate/claims/:id/score", Auth, middleware.RequireAdmin, AdminScoreReviewRebateClaim)
		}
		router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	scorePath := fmt.Sprintf("/admin/review-rebate/claims/%d/score", claim.ID)
	request(nil, http.MethodPost, scorePath, http.StatusUnauthorized)
	request(&member, http.MethodPost, scorePath, http.StatusForbidden)
	if calls != 0 {
		t.Fatalf("unauthorized calls must not reach the scorer, got %d", calls)
	}
	adminRow := func(state string) map[string]any {
		t.Helper()
		w := request(&admin, http.MethodGet, "/admin/review-rebate/claims", http.StatusOK)
		row := decodeJSONObject(t, w.Body.Bytes())["data"].([]any)[0].(map[string]any)
		if row["agentScoreState"] != state {
			t.Fatalf("expected persisted scoring state %q, got %v", state, row["agentScoreState"])
		}
		if strings.Contains(strings.ToLower(w.Body.String()), "agent_score_run") || strings.Contains(w.Body.String(), "agentScoreRun") {
			t.Fatal("internal run fence must not be returned by any queue")
		}
		return row
	}
	adminRow("unscored")
	failed := request(&admin, http.MethodPost, scorePath, http.StatusBadGateway)
	if strings.Contains(failed.Body.String(), "synthetic-private-provider-detail") {
		t.Fatal("provider failure detail must not leak in HTTP error")
	}
	row := adminRow("failed")
	if row["status"] != "submitted" || row["agentStars"] != float64(0) || row["awardAmount"] != float64(0) {
		t.Fatalf("failure must preserve business state without scoring: %v", row)
	}
	scored := request(&admin, http.MethodPost, scorePath, http.StatusOK)
	if strings.Contains(scored.Body.String(), "agentScoreRun") {
		t.Fatal("scoring HTTP response must not leak the internal fence")
	}
	row = adminRow("scored")
	if row["status"] != "scored" || row["agentStars"] != float64(4) {
		t.Fatalf("confirmed result is missing: %v", row)
	}
	request(&admin, http.MethodPost, scorePath, http.StatusConflict)
	if calls != 2 {
		t.Fatalf("success must prevent duplicate scoring, got %d calls", calls)
	}

	mine := request(&member, http.MethodGet, "/user/review-rebate/claims", http.StatusOK)
	if strings.Contains(mine.Body.String(), "agentScoreState") || strings.Contains(mine.Body.String(), "agentScoreRun") ||
		strings.Contains(mine.Body.String(), "approvalSnapshot") {
		t.Fatal("score status/fence must stay outside the member list")
	}
	var awards, bills int64
	if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&member, member.ID).Error; err != nil {
		t.Fatal(err)
	}
	if awards != 0 || bills != 0 || member.Balance != 0 || member.GiftBalance != 0 {
		t.Fatalf("manual scoring must not pay: awards=%d bills=%d balance=%v gift=%v", awards, bills, member.Balance, member.GiftBalance)
	}
}

func TestReviewRebateApprovalRejectsStaleQuoteAfterDelayedScoring(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	installReviewRebateTestDB(t, db)
	setReviewRebateControllerSwitch(t, db, true)
	program, rule := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 790, "synthetic-stale-approval-admin", "admin")
	member := seedReviewRebateControllerUser(t, db, 791, "synthetic-stale-approval-member", "user")
	claim, err := service.CreateReviewRebateClaimWithDB(db, service.ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: member.ID, ScreenshotSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	router := reviewRebateContractRouter(&admin)
	get := func() map[string]any {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/review-rebate/claims", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		return decodeJSONObject(t, w.Body.Bytes())["data"].([]any)[0].(map[string]any)
	}
	post := func(snapshot string) *httptest.ResponseRecorder {
		input := map[string]string{"action": "approve", "note": "synthetic confirmed quote"}
		if snapshot != "" {
			input["approvalSnapshot"] = snapshot
		}
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		path := fmt.Sprintf("/admin/review-rebate/claims/%d/decision", claim.ID)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, request)
		return w
	}
	assertPayment := func(wantCount int64, wantGift float64) {
		var awards, bills int64
		if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
			t.Fatal(err)
		}
		var wallet entity.User
		if err := db.First(&wallet, member.ID).Error; err != nil {
			t.Fatal(err)
		}
		if awards != wantCount || bills != wantCount || wallet.GiftBalance != wantGift {
			t.Fatalf("unexpected actual payment: awards=%d bills=%d gift=%g; want %d/%g", awards, bills, wallet.GiftBalance, wantCount, wantGift)
		}
	}

	before := get()
	if before["awardAmount"] != float64(0) {
		t.Fatalf("the initial dialog must have seen no award: %v", before)
	}
	oldSnapshot, _ := before["approvalSnapshot"].(string)
	if oldSnapshot == "" {
		oldSnapshot = "synthetic-client-stale-snapshot"
	}

	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := service.ScoreReviewRebateClaimManuallyWithDB(db, claim.ID,
			manualReviewFixtureScorer(func(entity.ReviewRebateClaim) (service.ReviewRebateScore, error) {
				close(entered)
				<-release
				return service.ReviewRebateScore{Stars: 4, ContentScore: 7, Confidence: .9, Scorer: "agent"}, nil
			}))
		result <- err
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("scoring never started: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("scoring never started")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	after := get()
	if after["awardAmount"] != rule.Amount {
		t.Fatalf("new score did not establish a different award: %v", after)
	}
	response := post(oldSnapshot)
	if response.Code != http.StatusConflict {
		t.Errorf("outdated 0-yuan approval must return 409 instead of paying %g, got status %d: %s", rule.Amount, response.Code, response.Body.String())
		assertPayment(0, 0)
		return
	}
	if noQuote := post(""); noQuote.Code != http.StatusConflict {
		t.Fatalf("post-scoring approval must require a fresh quote, got %d", noQuote.Code)
	}
	assertPayment(0, 0)
	fresh, _ := after["approvalSnapshot"].(string)
	if fresh == "" || fresh == oldSnapshot {
		t.Fatal("must return a changed authoritative approval snapshot")
	}
	for i := 0; i < 2; i++ {
		if response := post(fresh); response.Code != http.StatusOK {
			t.Fatalf("current approval and its replay must succeed: %d %s", response.Code, response.Body.String())
		}
	}
	assertPayment(1, rule.Amount)
}

func TestReviewRebateApprovalKeepsUntouchedLegacyDecisionCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	installReviewRebateTestDB(t, db)
	setReviewRebateControllerSwitch(t, db, true)
	program, rule := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 792, "synthetic-legacy-admin", "admin")
	member := seedReviewRebateControllerUser(t, db, 793, "synthetic-legacy-member", "user")
	claim := entity.ReviewRebateClaim{
		ProgramID: program.ID, UserID: member.ID, Source: "edge",
		CanonicalClaimID: "synthetic-untouched-legacy", Status: entity.ReviewRebateStatusScored,
		AwardAmount: rule.Amount, MatchedRuleID: rule.ID,
	}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	body := strings.NewReader(fmt.Sprintf("{%q:%q}", "decision", "approve"))
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/review-rebate/claims/%d/decision", claim.ID), body)
	r.Header.Set("Content-Type", "application/json")
	reviewRebateContractRouter(&admin).ServeHTTP(w, r)
	if w.Code != http.StatusOK || decodeJSONObject(t, w.Body.Bytes())["awardAmount"] != rule.Amount {
		t.Fatalf("untouched historical quote must keep the old route alias: %d %s", w.Code, w.Body.String())
	}
}
