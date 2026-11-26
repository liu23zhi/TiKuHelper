package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func ruleResponseFixture(mode, scope string, minutes int) entity.RiskRule {
	p, _ := encodeRuleResponse(entity.RiskRuleIDDeviceRegisterBurst, &RiskRuleResponseInput{
		RiskRuleResponsePolicy: RiskRuleResponsePolicy{Mode: mode, Scope: scope, DurationMinutes: minutes}, Confirmed: true,
	}, "")
	return entity.RiskRule{RuleID: entity.RiskRuleIDDeviceRegisterBurst, ResponsePolicyJSON: p, Enabled: true}
}

func TestRiskRuleExactSegmentAndAutomaticRevoke(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	const ip = "192.0.2.40"
	w := responseTestWarning(t, db, entity.RiskRuleIDDeviceRegisterBurst, ip, "registration_browser", "single")
	cache := entity.LocationCache{Kind: entity.LocationKindIP, Source: "standard", LookupKey: ip, RawJSON: `{"beginip":"192.0.2.37","endip":"192.0.2.52","apiKey":"never-in-audit"}`}
	if err := db.Create(&cache).Error; err != nil {
		t.Fatal(err)
	}
	p := ruleResponseFixture(riskRulePermanent, "segment", 0)
	if err := respondToNewWarning(db, p, &w); err != nil {
		t.Fatal(err)
	}
	view, err := GetRiskAlertResponse(db, w.ID)
	if err != nil || len(view.History) != 1 {
		t.Fatalf("history=%+v err=%v", view.History, err)
	}
	d := view.History[0]
	if !d.CanRevoke || d.EntryID == 0 || d.EntryVersion == nil || d.Status != "applied" || d.ExpiresAt != nil ||
		d.RuleID != w.RuleID || d.Target != "192.0.2.37-192.0.2.52" || d.SourceIPMasked != "192.0.2.*" {
		t.Fatalf("unexpected audit: %+v", d)
	}
	snapshot, err := LoadBlacklistSnapshot(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]bool{"192.0.2.36": false, "192.0.2.37": true, "192.0.2.52": true, "192.0.2.53": false, "2001:db8::40": false} {
		got, hit := snapshot.MatchIP(key)
		if hit != want || (hit && (got.EntryID != d.EntryID || got.ValueMasked != "192.0.2.*–192.0.2.*")) {
			t.Fatalf("wrong exact boundary %s: hit=%t matched=%+v", key, hit, got)
		}
	}
	other := responseTestWarning(t, db, p.RuleID, "192.0.2.41", "registration_browser", "single")
	if _, err := RevokeAutomaticRiskResponse(db, other.ID, d.ID, 11); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-warning revoke %v", err)
	}
	if _, err := RevokeAutomaticRiskResponse(db, w.ID, d.ID, 0); !errors.Is(err, ErrRiskResponseInvalid) {
		t.Fatalf("unauthorized revoke %v", err)
	}
	for i := 0; i < 2; i++ {
		dead, err := RevokeAutomaticRiskResponse(db, w.ID, d.ID, 11)
		if err != nil || len(dead.History) != 2 || dead.Result.Status != "revoked" || dead.Result.LinkedResponseID != d.ID || dead.History[1].CanRevoke {
			t.Fatalf("revoke %d: %+v err=%v", i, dead, err)
		}
	}
	if err := respondToNewWarning(db, ruleResponseFixture(riskRuleTemporary, "ip", 30), &w); err != nil {
		t.Fatal(err)
	}
	snapshot, err = LoadBlacklistSnapshot(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, hit := snapshot.MatchIP(ip); hit || snapshot.RuleCount != 0 {
		t.Fatalf("revoke did not stick; ruleCount=%d match=%t", snapshot.RuleCount, hit)
	}
	t.Log("inclusive non-CIDR endpoints matched; neighboring IPs passed; one permanent row; scoped revoke idempotent; repeated warning after strategy change could not rearm")
}

func TestRiskRuleActionsFailOpenAndDoNotSlide(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	rule := ruleResponseFixture(riskRuleTemporary, "ip", 1)
	now := time.Now().Truncate(time.Second)
	db = db.Session(&gorm.Session{NowFunc: func() time.Time { return now }})
	check := func(w entity.RiskWarning) entity.RiskWarningResponse {
		t.Helper()
		var d entity.RiskWarningResponse
		if err := db.Where("warning_id = ?", w.ID).Take(&d).Error; err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, tc := range []struct{ name, ip, raw, want string }{
		{"not cached", "192.0.2.62", "", "segment_not_cached"},
		{"missing fields", "192.0.2.63", `{"beginip":"192.0.2.60"}`, "segment_untrusted"},
		{"reversed", "192.0.2.64", `{"beginip":"192.0.2.68","endip":"192.0.2.60"}`, "segment_untrusted"},
		{"different families", "192.0.2.65", `{"beginip":"192.0.2.60","endip":"2001:db8::b"}`, "segment_untrusted"},
		{"out of interval", "192.0.2.66", `{"beginip":"192.0.2.60","endip":"192.0.2.62"}`, "segment_untrusted"},
		{"unsupported IPv6", "2001:db8::7", "", "segment_family_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.raw != "" {
				if err := db.Create(&entity.LocationCache{Kind: entity.LocationKindIP, Source: "standard", LookupKey: tc.ip, RawJSON: tc.raw}).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := responseTestWarning(t, db, rule.RuleID, tc.ip, "registration_browser", "single")
			if err := respondToNewWarning(db, ruleResponseFixture(riskRulePermanent, "segment", 0), &w); err != nil {
				t.Fatal(err)
			}
			d := check(w)
			if d.Status != "skipped" || d.OutcomeCode != tc.want || d.EntryID != 0 || d.Target != "" {
				t.Fatalf("unsafe fallback: %+v", d)
			}
			if err := respondToNewWarning(db, rule, &w); err != nil {
				t.Fatal(err)
			}
			var n int64
			db.Model(&entity.RiskWarningResponse{}).Where("warning_id = ?", w.ID).Count(&n)
			if n != 1 {
				t.Fatalf("decision duplicated: %d", n)
			}
		})
	}
	const white = "192.0.2.75"
	if _, err := CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{Kind: "ip", Pattern: white, MatchType: "exact", Reason: "synthetic", Enabled: true, CreatedBy: 12}); err != nil {
		t.Fatal(err)
	}
	whiteWarning := responseTestWarning(t, db, rule.RuleID, white, "registration_browser", "single")
	if err := respondToNewWarning(db, rule, &whiteWarning); err != nil {
		t.Fatal(err)
	}
	if d := check(whiteWarning); d.Status != "skipped" || d.OutcomeCode != "whitelisted" || d.EntryID != 0 {
		t.Fatalf("whitelist ignored: %+v", d)
	}
	var before int64
	db.Model(&entity.BlacklistEntry{}).Count(&before)
	if before != 0 {
		t.Fatalf("skip created blacklist rows=%d", before)
	}
	w := responseTestWarning(t, db, rule.RuleID, "192.0.2.80", "registration_browser", "single")
	w.Severity = entity.RiskSeverityLow
	if err := respondToNewWarning(db, rule, &w); err != nil {
		t.Fatal(err)
	}
	d := check(w)
	if d.Status != "applied" || d.ExpiresAt == nil || !d.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("wrong first result %+v", d)
	}
	oldSnapshot, err := LoadBlacklistSnapshot(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := oldSnapshot.MatchIP(w.IP); !ok {
		t.Fatal("temporary IP did not enforce")
	}
	now = now.Add(2 * time.Minute)
	w.Severity = entity.RiskSeverityHigh
	if err := respondToNewWarning(db, ruleResponseFixture(riskRulePermanent, "ip", 0), &w); err != nil {
		t.Fatal(err)
	}
	var entry entity.BlacklistEntry
	if err := db.First(&entry, d.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	if !sameResponseExpiry(d.ExpiresAt, entry.ExpiresAt) {
		t.Fatal("expiry changed despite duplicate")
	}
	snapshot, err := LoadBlacklistSnapshot(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.MatchIP(w.IP); ok {
		t.Fatal("expired rule still enforced")
	}
	var n int64
	db.Model(&entity.RiskWarningResponse{}).Where("warning_id = ?", w.ID).Count(&n)
	if n != 1 {
		t.Fatalf("rule reapplied: %d", n)
	}
	fresh := responseTestWarning(t, db, rule.RuleID, "192.0.2.81", "registration_browser", "single")
	fresh.Severity = entity.RiskSeverityHigh
	if err := respondToNewWarning(db, rule, &fresh); err != nil {
		t.Fatal(err)
	}
	f := check(fresh)
	if f.Status != "applied" || f.ExpiresAt == nil || !f.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("severity changed configured action %+v", f)
	}
	t.Log("trusted-only scope, whitelist first, temporary expiry not slid or upgraded after warning replay, low/high execute the same selected action")
}

func TestRiskRuleSegmentEvidenceRequiresConsistentBounds(t *testing.T) {
	db := responseTestDB(t)
	const ip = "198.51.100.40"
	const lo = "198.51.100.31"
	const hi = "198.51.100.78"
	begin, _ := ipv4ToUint32(lo)
	end, _ := ipv4ToUint32(hi)
	row := entity.IPSegment{BeginIP: begin, EndIP: end, BeginText: lo, EndText: hi, SampleIP: "198.51.100.50", Source: "standard"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if got, reason := autoEvidenceSegment(db, ip); got != lo+"-"+hi || reason != "" {
		t.Fatalf("trusted segment table not reusable: %q %q", got, reason)
	}
	if err := db.Model(&row).Update("sample_ip", "198.51.100.80").Error; err != nil {
		t.Fatal(err)
	}
	if got, reason := autoEvidenceSegment(db, ip); got != "" || reason != "segment_untrusted" {
		t.Fatalf("invalid sample evidence: %q %q", got, reason)
	}
	if err := db.Model(&row).Updates(map[string]any{"sample_ip": ip, "begin_text": "198.51.100.30"}).Error; err != nil {
		t.Fatal(err)
	}
	if got, reason := autoEvidenceSegment(db, ip); got != "" || reason != "segment_untrusted" {
		t.Fatalf("inconsistent text/numeric evidence: %q %q", got, reason)
	}
	if err := db.Model(&row).Update("begin_text", lo).Error; err != nil {
		t.Fatal(err)
	}
	conflict := entity.IPSegment{BeginIP: begin + 1, EndIP: end + 1, BeginText: "198.51.100.32", EndText: "198.51.100.79", SampleIP: ip, Source: "standard"}
	if err := db.Create(&conflict).Error; err != nil {
		t.Fatal(err)
	}
	if got, reason := autoEvidenceSegment(db, ip); got != "" || reason != "segment_untrusted" {
		t.Fatalf("different tied ranges selected: %q %q", got, reason)
	}
	t.Log("trusted segment cache can be reused; sample mismatch, inconsistent boundaries, and tied conflicting ranges are rejected")
}
