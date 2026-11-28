package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if strings.Contains(mine.Body.String(), "agentScoreState") || strings.Contains(mine.Body.String(), "agentScoreRun") {
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
