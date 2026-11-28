package service

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	reviewRebateScoreUnscored = "unscored"
	reviewRebateScoreRunning  = "running"
	reviewRebateScoreFailed   = "failed"
	reviewRebateScoreScored   = "scored"
	reviewRebateScoreLease    = ReviewRebateScoringTimeout + time.Minute
)

func reviewRebateCanRunScorer(status string, manual bool) bool {
	if manual {
		return status == entity.ReviewRebateStatusSubmitted || status == entity.ReviewRebateStatusScraping
	}
	switch status {
	case entity.ReviewRebateStatusApproved, entity.ReviewRebateStatusRejected, entity.ReviewRebateStatusClawedBack:
		return false
	default:
		return true
	}
}

// The private marker fences late provider results after a lease is replaced.
// An expired attempt is displayed as failed without needing a cleanup worker.
func reviewRebateScoreRunActive(run string, now time.Time) bool {
	prefix, deadline, ok := strings.Cut(run, ":")
	if !ok || prefix != reviewRebateScoreRunning {
		return false
	}
	deadline, attempt, ok := strings.Cut(deadline, ":")
	until, err := strconv.ParseInt(deadline, 10, 64)
	return ok && attempt != "" && err == nil && until > now.UnixMilli()
}

// ReviewRebateAgentScoreState is for the administrator queue only. It keeps
// evidence of already scored legacy claims, including legitimate zero values.
func ReviewRebateAgentScoreState(claim entity.ReviewRebateClaim) string {
	switch strings.ToLower(strings.TrimSpace(claim.AgentScorer)) {
	case "agent", "stub":
		return reviewRebateScoreScored
	}
	switch claim.Status {
	case entity.ReviewRebateStatusScored, entity.ReviewRebateStatusPendingReview, entity.ReviewRebateStatusBelowThreshold:
		return reviewRebateScoreScored
	}
	if claim.AgentScoreRun == reviewRebateScoreScored || strings.TrimSpace(claim.AgentRationale) != "" {
		return reviewRebateScoreScored
	}
	if !reviewRebateCanRunScorer(claim.Status, true) || claim.AgentScoreRun == "" {
		return reviewRebateScoreUnscored
	}
	if reviewRebateScoreRunActive(claim.AgentScoreRun, time.Now()) {
		return reviewRebateScoreRunning
	}
	return reviewRebateScoreFailed
}

func beginReviewRebateScoreRun(db *gorm.DB, claim entity.ReviewRebateClaim, manual bool) (string, error) {
	now := time.Now()
	if !reviewRebateCanRunScorer(claim.Status, manual) || reviewRebateScoreRunActive(claim.AgentScoreRun, now) ||
		(manual && ReviewRebateAgentScoreState(claim) == reviewRebateScoreScored) {
		return "", ErrReviewRebateClaimState
	}
	run := fmt.Sprintf("running:%d:%s", now.Add(reviewRebateScoreLease).UnixMilli(), rand.Text())
	res := reviewRebateSession(db).Model(&entity.ReviewRebateClaim{}).
		Where("id = ? AND status = ? AND agent_score_run = ?", claim.ID, claim.Status, claim.AgentScoreRun).
		UpdateColumn("agent_score_run", run)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected != 1 {
		return "", ErrReviewRebateClaimState
	}
	return run, nil
}
