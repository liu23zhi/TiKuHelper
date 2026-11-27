package service

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RiskResponseInput struct {
	Action          string `json:"action"`
	DurationMinutes int    `json:"durationMinutes"`
	Reason          string `json:"reason"`
	Confirmed       bool   `json:"confirmed"`
	ExpectedTarget  string `json:"expectedTarget,omitempty"`
}

func validateRiskResponse(input RiskResponseInput) error {
	switch input.Action {
	case "ip", "segment", "city_isp":
	default:
		return invalidRiskResponse("处置范围只能是单 IP、已知网段或城市和运营商")
	}
	if input.DurationMinutes < 0 || input.DurationMinutes > riskResponseManualMaxMinutes {
		return invalidRiskResponse("期限应为 0（永久）或 1–525600 分钟")
	}
	if (input.Action != "ip" || input.DurationMinutes == 0) && !input.Confirmed {
		return invalidRiskResponse("范围或永久处置必须明确确认")
	}
	if input.Reason == "" || utf8.RuneCountInString(input.Reason) > 255 {
		return invalidRiskResponse("必须填写 1–255 字的原因")
	}
	return nil
}

func ApplyRiskAlertResponse(db *gorm.DB, id int64, actorID int32, in RiskResponseInput) (RiskAlertResponseView, error) {
	if actorID <= 0 {
		return RiskAlertResponseView{}, invalidRiskResponse("缺少操作管理员")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if err := validateRiskResponse(in); err != nil {
		return RiskAlertResponseView{}, err
	}
	return applyRiskResponse(db, id, actorID, in)
}

func applyRiskResponse(db *gorm.DB, id int64, actor int32, in RiskResponseInput) (RiskAlertResponseView, error) {
	var v RiskAlertResponseView
	var changed bool
	err := db.Transaction(func(tx *gorm.DB) error {
		var warning entity.RiskWarning
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&warning, id).Error; err != nil {
			return err
		}
		kind, err := warningResponseDetector(tx, warning)
		if err != nil {
			return err
		}
		v, err = responseTargets(tx, warning, kind, tx.NowFunc(), actor == 0)
		if err != nil {
			return err
		}
		if v.IP == "" {
			return invalidRiskResponse(v.Message)
		}
		// Region data may change between the preview and the click. The supplied
		// label is only a precondition, never the target used to build the ban.
		if (in.Action == "segment" && (v.Segment == "" || in.ExpectedTarget != v.Segment)) ||
			(in.Action == "city_isp" && (v.CityISP == "" || in.ExpectedTarget != v.CityISP)) {
			return invalidRiskResponse("归属范围已改变或不可用，请刷新处置目标后重新确认")
		}
		decision := entity.RiskWarningResponse{WarningID: id, RuleID: warning.RuleID, SourceIPMasked: MaskBlacklistValue(BlacklistKindIP, v.IP, BlacklistMatchExact), Action: in.Action, ActorID: actor, DurationMinutes: in.DurationMinutes, Reason: in.Reason, CreatedAt: tx.NowFunc()}
		bk, pattern, mt := BlacklistKindIP, v.IP, BlacklistMatchExact
		switch in.Action {
		case "segment":
			pattern, mt = v.Segment, BlacklistMatchCIDR
		case "city_isp":
			bk, pattern, mt = BlacklistKindRegion, v.CityISP, blacklistMatchRegionScope
		}
		if pattern == "" {
			return invalidRiskResponse("没有该范围的完整证据，未创建规则")
		}
		decision.Target = pattern
		if !v.Eligible || !v.BlacklistEnabled {
			decision.Status, decision.Message = "skipped", v.Message
		} else {
			decision, changed, err = placeResponseEntry(tx, decision, bk, mt)
			if err != nil {
				return err
			}
		}
		if decision.ID == 0 {
			// Duplicated skips / attempts on an existing external rule are idempotent too.
			var prior entity.RiskWarningResponse
			err := sameDecision(tx, decision).Where("status = ?", decision.Status).Order("id DESC").Take(&prior).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if !changed && err == nil && prior.Message == decision.Message && sameResponseExpiry(prior.ExpiresAt, decision.ExpiresAt) {
				decision = prior
			} else if err := tx.Create(&decision).Error; err != nil {
				return err
			}
		}
		v.Result = &decision
		v.Message = decision.Message
		if actor != 0 {
			return loadResponseHistory(tx, id, &v)
		}
		return nil
	})
	if err != nil {
		return RiskAlertResponseView{}, err
	}
	if changed {
		if err := ReloadDefaultBlacklistCache(db); err != nil {
			v.Message += "；规则已保存但本机缓存刷新失败，请核查"
			logger.SysWarn(fmt.Sprintf("risk_response_cache_failed warningId=%d entryId=%d error=%v", id, v.Result.EntryID, err))
		}
	}
	return v, nil
}
func sameResponseExpiry(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// Match actor and exact user text, including on case-insensitive MySQL databases.
func sameDecision(tx *gorm.DB, d entity.RiskWarningResponse) *gorm.DB {
	q := tx.Where("warning_id = ? AND action = ? AND entry_id = ? AND duration_minutes = ? AND actor_id = ?",
		d.WarningID, d.Action, d.EntryID, d.DurationMinutes, d.ActorID)
	if tx.Dialector.Name() == "mysql" {
		return q.Where("BINARY target = BINARY ? AND BINARY reason = BINARY ?", d.Target, d.Reason)
	}
	return q.Where("target = ? COLLATE BINARY AND reason = ? COLLATE BINARY", d.Target, d.Reason)
}

func responseExpiry(at time.Time, minutes int) *time.Time {
	if minutes == 0 {
		return nil
	}
	t := at.Add(time.Duration(minutes) * time.Minute)
	return &t
}

// Only create ordinary blacklist entries. Existing external bans and deliberately disabled rows
// are never overwritten; an explicitly extended or renewed lease keeps the original creator/reason.
func placeResponseEntry(tx *gorm.DB, d entity.RiskWarningResponse, kind, matchType string) (entity.RiskWarningResponse, bool, error) {
	var n NormalizedBlacklistValue
	var err error
	if kind == BlacklistKindIP && matchType == blacklistMatchIPv4Range && d.ActorID == 0 && d.Action == "segment" {
		n, err = normalizeAutomaticBlacklistIPv4Range(d.Target)
	} else {
		n, err = NormalizeBlacklistValue(kind, d.Target, matchType)
	}
	if err != nil {
		return d, false, err
	}
	var entry entity.BlacklistEntry
	find := func() error {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind = ? AND value_hash = ? AND match_type = ?", n.Kind, n.ValueHash, n.MatchType).Take(&entry).Error
	}
	err = find()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		entry, err = createNormalizedBlacklistEntryWithDB(tx, CreateBlacklistEntryInput{Reason: d.Reason, Enabled: true, CreatedBy: d.ActorID}, n)
		if err == nil {
			// INSERT has acquired the unique key. Date the lease only now: any
			// wait for a concurrent writer cannot consume its TTL before publication.
			d.EntryID, d.Status, d.Message = entry.ID, "applied", "已创建黑名单规则"
			return writeLeaseExpiry(tx, d)
		}
		// A racing insert is an existing rule, never our new lease. A real insert error remains visible.
		createErr := err
		if err = find(); err != nil {
			return d, false, createErr
		}
	}
	if err != nil {
		return d, false, err
	}
	d.EntryID, d.ExpiresAt = entry.ID, entry.ExpiresAt
	if !entry.Enabled {
		d.Status, d.Message = "skipped", "已有规则被手动停用，未重新启用"
		return d, false, nil
	}
	var prior entity.RiskWarningResponse
	err = sameDecision(tx, d).Where("status IN ?", []string{"applied", "existing"}).Order("id DESC").Take(&prior).Error
	if err == nil {
		// True retries replay their original receipt, even after expiry. A new
		// decision must not impersonate another reviewer or slide the lease.
		return prior, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return d, false, err
	}
	now := tx.NowFunc() // both the warning and the rule are locked
	d.CreatedAt = now
	active := entry.ExpiresAt == nil || now.Before(*entry.ExpiresAt)
	if active {
		d.Status, d.Message = "existing", "已有有效封禁，保留原规则和到期时间"
		if d.ActorID == 0 || entry.ExpiresAt == nil {
			return d, false, nil
		}
		var owner entity.RiskWarningResponse
		err = tx.Where("entry_id = ? AND status = ?", entry.ID, "applied").Order("id DESC").Take(&owner).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !sameResponseExpiry(owner.ExpiresAt, entry.ExpiresAt)) {
			d.Message = "已保留其他渠道的手工封禁；未更改期限"
			return d, false, nil
		}
		if err != nil {
			return d, false, err
		}
		if until := responseExpiry(now, d.DurationMinutes); until != nil &&
			(d.DurationMinutes <= owner.DurationMinutes || !until.After(*entry.ExpiresAt)) {
			return d, false, nil // only an explicit longer tier or permanent request can upgrade
		}
		d.Message = "已升级本流程封禁的期限"
	} else {
		d.Message = "原规则已到期；已按本次处置续期，原创建人及原因保留"
	}
	d.Status = "applied"
	return writeLeaseExpiry(tx, d)
}

func writeLeaseExpiry(tx *gorm.DB, d entity.RiskWarningResponse) (entity.RiskWarningResponse, bool, error) {
	d.CreatedAt = tx.NowFunc()
	d.ExpiresAt = responseExpiry(d.CreatedAt, d.DurationMinutes)
	if err := tx.Model(&entity.BlacklistEntry{}).Where("id = ?", d.EntryID).Update("expires_at", d.ExpiresAt).Error; err != nil {
		return d, false, err
	}
	if d.ActorID == 0 {
		// Read the DB-rounded timestamp and expiry, not the pre-write value.
		var entry entity.BlacklistEntry
		if err := tx.Select("updated_at", "expires_at").First(&entry, d.EntryID).Error; err != nil {
			return d, false, err
		}
		d.EntryVersion, d.ExpiresAt = &entry.UpdatedAt, entry.ExpiresAt
	}
	return d, true, nil
}
