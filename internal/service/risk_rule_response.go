package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	autoRuleResponseReason = "触发风控规则，限制来源网络的访问"
	riskResponseRevoke     = "revoke_auto"
)

func respondToNewWarning(db *gorm.DB, rule entity.RiskRule, w *entity.RiskWarning) error {
	if w == nil || w.RuleID != rule.RuleID {
		return nil
	}
	p := resolveRuleResponse(rule)
	if p.Mode == riskRuleNotify {
		return nil
	}
	d := entity.RiskWarningResponse{WarningID: w.ID, RuleID: rule.RuleID, Action: p.Scope,
		DurationMinutes: p.DurationMinutes, Reason: autoRuleResponseReason}
	kind := effectiveRiskRuleType(rule)
	ip := riskResponseIP(*w, kind, true)
	if ip != "" {
		d.SourceIPMasked = MaskBlacklistValue(BlacklistKindIP, ip, BlacklistMatchExact)
	}
	base := d
	changed := false
	err := withFreshAutoResponse(db, w.ID, func(tx *gorm.DB) error {
		d.CreatedAt = tx.NowFunc()
		v, err := responseTargets(tx, *w, kind, d.CreatedAt, true)
		switch {
		case err != nil:
			d.Status, d.OutcomeCode, d.Message = "skipped", "whitelist_unavailable", "无法校验白名单，仅告警"
		case ip == "":
			d.Status, d.OutcomeCode, d.Message = "skipped", "source_unattributed", "没有可信的单一来源 IP，仅告警"
		case !v.Eligible:
			d.Status, d.OutcomeCode, d.Message = "skipped", "whitelisted", v.Message
		case !v.BlacklistEnabled:
			d.Status, d.OutcomeCode, d.Message = "skipped", "blacklist_disabled", v.Message
		default:
			d.Target = ip
			matchType := BlacklistMatchExact
			if p.Scope == "segment" {
				d.Target, d.OutcomeCode = autoEvidenceSegment(tx, ip)
				if d.Target == "" {
					d.Status, d.Message = "skipped", "没有可验证的完整 IPv4 网段，仅告警"
					break
				}
				matchType = blacklistMatchIPv4Range
			}
			d, changed, err = placeResponseEntry(tx, d, BlacklistKindIP, matchType)
			if err != nil {
				return err
			}
			switch d.Status {
			case "skipped":
				d.OutcomeCode = "entry_disabled"
			case "existing":
				d.OutcomeCode = "entry_existing"
			}
		}
		return tx.Create(&d).Error
	})
	if err != nil {
		// The write transaction was rolled back. Preserve a terminal redacted
		// receipt if the ledger is usable; never retry a historical warning.
		return withFreshAutoResponse(db, w.ID, func(tx *gorm.DB) error {
			base.CreatedAt = tx.NowFunc()
			base.Status, base.OutcomeCode, base.Message = "failed", "apply_failed", "黑名单未写入，仅告警"
			return tx.Create(&base).Error
		})
	}
	if changed {
		refreshAutoResponseCache(db, d)
	}
	return nil
}

// Locking the warning serializes attempts even when the entry is missing,
// changes scope after editing, expires, or has been administratively revoked.
func withFreshAutoResponse(db *gorm.DB, warningID int64, run func(*gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var warning entity.RiskWarning
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&warning, warningID).Error; err != nil {
			return err
		}
		var prior entity.RiskWarningResponse
		err := tx.Select("id").Where("warning_id = ? AND actor_id = 0", warningID).Take(&prior).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return run(tx)
	})
}

func refreshAutoResponseCache(db *gorm.DB, d entity.RiskWarningResponse) bool {
	if err := ReloadDefaultBlacklistCache(db); err != nil {
		// Database/provider payloads may contain private addresses or tokens.
		logger.SysWarn(fmt.Sprintf("risk_rule_response_cache_failed warningId=%d entryId=%d", d.WarningID, d.EntryID))
		return false
	}
	return true
}

func automaticResponseOwnsEntry(row entity.RiskWarningResponse, entry entity.BlacklistEntry) bool {
	mt := BlacklistMatchExact
	if row.Action == "segment" {
		mt = blacklistMatchIPv4Range
	}
	return row.ActorID == 0 && row.Status == "applied" && row.EntryVersion != nil &&
		entry.Enabled && entry.Kind == BlacklistKindIP && entry.MatchType == mt && entry.Pattern == row.Target &&
		entry.UpdatedAt.Equal(*row.EntryVersion) && sameResponseExpiry(entry.ExpiresAt, row.ExpiresAt)
}

func RevokeAutomaticRiskResponse(db *gorm.DB, warningID, responseID int64, actorID int32) (RiskAlertResponseView, error) {
	if actorID <= 0 || responseID <= 0 {
		return RiskAlertResponseView{}, invalidRiskResponse("缺少操作管理员或处置记录编号")
	}
	var receipt entity.RiskWarningResponse
	changed := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var warning entity.RiskWarning
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "rule_id").First(&warning, warningID).Error; err != nil {
			return err
		}
		var row entity.RiskWarningResponse
		if err := tx.Where("warning_id = ? AND id = ? AND actor_id = 0 AND status = ?", warningID, responseID, "applied").Take(&row).Error; err != nil {
			return err
		}
		err := tx.Where("warning_id = ? AND action = ? AND linked_response_id = ?", warningID, riskResponseRevoke, responseID).Take(&receipt).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var entry entity.BlacklistEntry
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&entry, row.EntryID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err != nil || !automaticResponseOwnsEntry(row, entry) ||
			(entry.ExpiresAt != nil && !tx.NowFunc().Before(*entry.ExpiresAt)) {
			return invalidRiskResponse("该自动封禁已失效，或已被其他操作修改，未撤销任何规则")
		}
		if err := tx.Model(&entry).Update("enabled", false).Error; err != nil {
			return err
		}
		receipt = entity.RiskWarningResponse{WarningID: warningID, RuleID: warning.RuleID, Action: riskResponseRevoke,
			EntryID: row.EntryID, LinkedResponseID: row.ID, Target: row.Target, SourceIPMasked: row.SourceIPMasked,
			ActorID: actorID, Reason: "管理员撤销自动封禁", Status: "revoked", Message: "已停用此自动封禁",
			ExpiresAt: row.ExpiresAt, CreatedAt: tx.NowFunc()}
		changed = true
		return tx.Create(&receipt).Error
	})
	if err != nil {
		return RiskAlertResponseView{}, err
	}
	if changed && !refreshAutoResponseCache(db, receipt) {
		receipt.Message += "；已保存，缓存刷新失败，请核查"
	}
	v, err := GetRiskAlertResponse(db, warningID)
	v.Result, v.Message = &receipt, receipt.Message
	return v, err
}

func annotateAutomaticRevoke(db *gorm.DB, now time.Time, rows []entity.RiskWarningResponse) error {
	var ids []int64
	revoked := make(map[int64]bool)
	for _, row := range rows {
		if row.Status == "revoked" {
			revoked[row.LinkedResponseID] = true
		}
		if row.ActorID == 0 && row.Status == "applied" && row.EntryVersion != nil {
			ids = append(ids, row.EntryID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var entries []entity.BlacklistEntry
	if err := db.Where("id IN ?", ids).Find(&entries).Error; err != nil {
		return err
	}
	for i, row := range rows {
		if revoked[row.ID] {
			continue
		}
		for _, entry := range entries {
			if entry.ID == row.EntryID && automaticResponseOwnsEntry(row, entry) && (entry.ExpiresAt == nil || now.Before(*entry.ExpiresAt)) {
				rows[i].CanRevoke = true
			}
		}
	}
	return nil
}
