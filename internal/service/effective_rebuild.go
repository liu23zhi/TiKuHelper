package service

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	run := func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, true, applying); err != nil {
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
		levels, err := readEffectiveLevelDefinitions(levelsQ)
		if err != nil {
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
			if totals.RequiresReview() {
				out.Skipped++
				r.New = r.Old
				for k, n := range totals.Issues {
					out.Issues[k] += n
				}
			} else {
				level, ok := resolveEffectiveLevel(levels, totals)
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
		var previous int64
		if err := tx.Model(&entity.EffectiveRebuildAudit{}).Where("operator_id = ? AND after_id = ?", actor.ID, in.AfterID).Select("COALESCE(MAX(id),0)").Scan(&previous).Error; err != nil {
			return err
		}
		b, err := json.Marshal([]any{actor.ID, in.AfterID, in.Limit, previous, out.HasMore, levels, rows})
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
		a := entity.EffectiveRebuildAudit{OperatorID: actor.ID, AfterID: in.AfterID, NextID: out.NextID, Examined: out.Examined, Changed: out.Changed, Skipped: out.Skipped, Digest: out.Digest, Reason: reason}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		out.AuditID = a.ID
		for _, r := range rows {
			if r.New == r.Old {
				continue
			}
			if _, e := persistUserLevelFacts(tx, r.ID, r.Old, r.New, entity.UserLevelTriggerReconcile, r.Totals, levels, fmt.Sprintf("effective:%d:%d", a.ID, r.ID)); e != nil {
				return e
			}
		}
		return nil
	}
	var err error
	if applying {
		err = ticketRefundTransaction(db, run)
	} else if db.Dialector.Name() == "mysql" {
		err = userLevelSession(db).Transaction(run, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	} else {
		err = userLevelSession(db).Transaction(run)
	}
	if err != nil {
		return EffectiveRebuildResult{}, err
	}
	return out, nil
}
