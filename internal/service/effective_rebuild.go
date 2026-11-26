package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"unicode/utf8"
)

type EffectiveRebuildInput struct {
	AfterID     int32  `json:"afterId"`
	Limit       int    `json:"limit"`
	ApplyDigest string `json:"applyDigest"`
	Reason      string `json:"reason"`
}
type EffectiveRebuildResult struct {
	NextID   int32            `json:"nextId"`
	HasMore  bool             `json:"hasMore"`
	Examined int              `json:"examined"`
	Changed  int              `json:"changed"`
	Skipped  int              `json:"skipped"`
	Issues   map[string]int64 `json:"issues,omitempty"`
	Digest   string           `json:"digest"`
	AuditID  int64            `json:"auditId,omitempty"`
}
type rebuildRow struct {
	ID, Old, New int32
	Totals       UserLevelTotals
}

// RebuildEffectiveLevels previews by default. An apply must replay exactly the
// previewed cohort, balances and entitlement configuration in one transaction.
// Missing evidence is not an instruction to downgrade a legacy account.
func RebuildEffectiveLevels(db *gorm.DB, actor entity.User, in EffectiveRebuildInput) (EffectiveRebuildResult, error) {
	var out EffectiveRebuildResult
	applying := in.ApplyDigest != ""
	reason := strings.TrimSpace(in.Reason)
	if db == nil || in.AfterID < 0 || in.Limit < 1 || in.Limit > 200 || applying && (len(in.ApplyDigest) != 64 || reason == "" || utf8.RuneCountInString(reason) > 255) {
		return out, ErrLedgerInput
	}
	err := userLevelSession(db).Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, true, false); err != nil {
			return err
		}
		if !userLevelSchemaAvailable(tx) || !tx.Migrator().HasTable(&entity.EffectiveRebuildAudit{}) {
			return ErrUserLevelSchemaUnavailable
		}
		q := tx.Model(&entity.User{}).Select("id,level_id").Where("id > ?", in.AfterID).Order("id asc").Limit(in.Limit + 1)
		levelsQ := tx.Model(&entity.UserLevel{}).Order("level_rank asc,id asc")
		if applying {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
			levelsQ = levelsQ.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var users []struct{ ID, LevelID int32 }
		if err := q.Find(&users).Error; err != nil {
			return err
		}
		var levels []entity.UserLevel
		if err := levelsQ.Find(&levels).Error; err != nil {
			return err
		}
		if len(levels) == 0 {
			return ErrUserLevelSchemaUnavailable
		}
		if len(users) > in.Limit {
			users = users[:in.Limit]
			out.HasMore = true
		}
		rows := make([]rebuildRow, 0, len(users))
		out.NextID = in.AfterID
		out.Issues = map[string]int64{}
		for _, u := range users {
			totals, err := ComputeUserLevelTotalsWithDB(tx, u.ID)
			if err != nil {
				return err
			}
			r := rebuildRow{ID: u.ID, Old: u.LevelID, Totals: totals}
			if len(totals.Issues) != 0 {
				out.Skipped++
				r.New = r.Old
				for k, n := range totals.Issues {
					out.Issues[k] += n
				}
			} else {
				level, ok := ResolveUserLevel(levels, totals.RechargeTotal, totals.ConsumptionTotal)
				if ok {
					r.New = level.ID
				}
				if r.New != r.Old {
					out.Changed++
				}
			}
			rows = append(rows, r)
			out.NextID = u.ID
		}
		out.Examined = len(rows)
		b, err := json.Marshal([]any{actor.ID, in.AfterID, in.Limit, out.HasMore, levels, rows})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out.Digest = hex.EncodeToString(sum[:])
		if !applying {
			return nil
		}
		if out.Digest != in.ApplyDigest || len(rows) == 0 {
			return ErrLedgerStale
		}
		for _, r := range rows {
			if r.New == r.Old {
				continue
			}
			ev, err := EvaluateUserLevelWithDB(tx, r.ID, entity.UserLevelTriggerReconcile)
			if err != nil {
				return err
			}
			if ev.ToLevelID != r.New {
				return ErrLedgerStale
			}
		}
		a := entity.EffectiveRebuildAudit{OperatorID: actor.ID, AfterID: in.AfterID, NextID: out.NextID, Examined: out.Examined, Changed: out.Changed, Skipped: out.Skipped, Digest: out.Digest, Reason: reason}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		out.AuditID = a.ID
		return nil
	})
	if err != nil {
		return EffectiveRebuildResult{}, err
	}
	return out, nil
}
