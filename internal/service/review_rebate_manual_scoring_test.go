package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newManualScoreClaim(t *testing.T) (*gorm.DB, entity.ReviewRebateClaim) {
	t.Helper()
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 501, "synthetic-t09-user", "user")
	program := seedReviewRebateProgram(t, db, true, 5, 6, 0.6)
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: user.ID, ScreenshotSHA256: reviewRebateFixtureDigest("09"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, claim
}

func TestReviewRebateManualScoreRejectsDuplicateScorerCalls(t *testing.T) {
	db, claim := newManualScoreClaim(t)
	started, release := make(chan struct{}), make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, err := ScoreReviewRebateClaimManuallyWithDB(db, claim.ID, ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
			close(started)
			<-release
			return ReviewRebateScore{Stars: 4, ContentScore: 8, Confidence: .8, Scorer: "agent"}, nil
		}))
		firstResult <- err
	}()
	select {
	case <-started:
	case err := <-firstResult:
		t.Fatalf("first scorer did not start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first scorer did not start")
	}

	duplicates := 0
	duplicate := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		duplicates++
		return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 1, Scorer: "agent"}, nil
	})
	_, concurrentErr := ScoreReviewRebateClaimManuallyWithDB(db, claim.ID, duplicate)
	close(release)
	if err := <-firstResult; err != nil {
		t.Errorf("first run: %v", err)
	}
	if _, err := ScoreReviewRebateClaimManuallyWithDB(db, claim.ID, duplicate); !errors.Is(err, ErrReviewRebateClaimState) {
		t.Errorf("completed results must not invoke the scorer again, got %v", err)
	}
	if !errors.Is(concurrentErr, ErrReviewRebateClaimState) || duplicates != 0 {
		t.Fatalf("duplicate requests must conflict without calling scorer, err=%v calls=%d", concurrentErr, duplicates)
	}
	got, err := GetReviewRebateClaimWithDB(db, claim.ID)
	if err != nil || got.AgentStars != 4 {
		t.Fatalf("first score replaced: %+v / %v", got, err)
	}
}

func TestReviewRebateManualScoreExpiredRunDoesNotOverwriteRetry(t *testing.T) {
	db, claim := newManualScoreClaim(t)
	started, release := make(chan struct{}), make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, err := ScoreReviewRebateClaimManuallyWithDB(db, claim.ID, ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
			close(started)
			<-release
			return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 1, Scorer: "agent"}, nil
		}))
		firstResult <- err
	}()
	select {
	case <-started:
	case err := <-firstResult:
		t.Fatalf("first scorer did not start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first scorer did not start")
	}
	defer func() {
		close(release)
		if err := <-firstResult; !errors.Is(err, ErrReviewRebateClaimState) {
			t.Errorf("stale provider result must be rejected by its fence: %v", err)
		}
		latest, err := GetReviewRebateClaimWithDB(db, claim.ID)
		if err != nil || latest.AgentStars != 4 || ReviewRebateAgentScoreState(latest) != "scored" {
			t.Errorf("retry result was replaced: %+v / %v", latest, err)
		}
	}()

	running, err := GetReviewRebateClaimWithDB(db, claim.ID)
	if err != nil || ReviewRebateAgentScoreState(running) != "running" {
		t.Fatalf("expected durable in-progress state: %+v / %v", running, err)
	}
	// Simulate expiry from a stalled worker, without an elapsed-time sleep.
	if err := db.Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).
		UpdateColumn("agent_score_run", "running:1:synthetic-expired-attempt").Error; err != nil {
		t.Fatal(err)
	}
	expired, err := GetReviewRebateClaimWithDB(db, claim.ID)
	if err != nil || ReviewRebateAgentScoreState(expired) != "failed" {
		t.Fatalf("expected expired attempt to allow retry: %+v / %v", expired, err)
	}
	if _, err := ScoreReviewRebateClaimManuallyWithDB(db, claim.ID, ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 4, ContentScore: 8, Confidence: .8, Scorer: "agent"}, nil
	})); err != nil {
		t.Fatalf("retry after expiry: %v", err)
	}
}

func TestReviewRebateManualScoreCannotOverrideHumanRejection(t *testing.T) {
	db, claim := newManualScoreClaim(t)
	admin := seedReviewRebateUser(t, db, 502, "synthetic-t09-admin", "admin")
	_, err := ScoreReviewRebateClaimManuallyWithDB(db, claim.ID, ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		if _, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, false, "synthetic review"); err != nil {
			t.Fatalf("manual rejection during provider call: %v", err)
		}
		return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 1, Scorer: "agent"}, nil
	}))
	if !errors.Is(err, ErrReviewRebateClaimState) {
		t.Fatalf("score finishing after manual decision should conflict, got %v", err)
	}
	rejected, err := GetReviewRebateClaimWithDB(db, claim.ID)
	if err != nil || rejected.Status != entity.ReviewRebateStatusRejected || rejected.AgentStars != 0 ||
		rejected.ReviewNote != "synthetic review" || rejected.AwardAmount != 0 {
		t.Fatalf("late result changed rejection: %+v / %v", rejected, err)
	}
}
