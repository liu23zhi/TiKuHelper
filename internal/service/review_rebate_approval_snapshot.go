package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// ReviewRebateApprovalSnapshot binds a displayed review to the exact claim and
// monetary/score facts being approved. This is a version check, not a grant:
// only the administrator route publishes and accepts it.
func ReviewRebateApprovalSnapshot(claim entity.ReviewRebateClaim) string {
	data, err := json.Marshal([]any{
		"review-rebate-approval-v1",
		claim.ID, claim.UserID, claim.ProgramID, claim.Source, claim.Status,
		util.RoundMoney(claim.AwardAmount), claim.MatchedRuleID,
		claim.AgentStars, claim.AgentContentScore, claim.AgentConfidence,
		claim.AgentScorer, claim.AgentRationale, claim.AgentScoreRun, claim.ScreenshotSHA256,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
