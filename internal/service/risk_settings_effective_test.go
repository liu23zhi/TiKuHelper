package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskAuxiliaryThresholdReadsIndependentSettings(t *testing.T) {
	db := newRiskAlertTestDB(t)
	for _, row := range []entity.RiskRule{
		{RuleID: "offhours_activity", Threshold: 9}, {RuleID: "token_abuse", Threshold: 12},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		key      string
		fallback float64
		invalid  string
		max      float64
	}{
		{"risk_offhours_share_threshold", .8, "3", 1},
		{"risk_token_abuse_multi_ip_threshold", 5, "101", 100},
	} {
		spec, _ := riskThresholdSpecByKey(tc.key)
		item := riskThresholdItem(db, spec)
		if item.Scope != "detector" || item.MaxValue != tc.max {
			t.Errorf("bad shared criterion metadata: %+v", item)
		}
		if item.Value != tc.fallback || item.Source != "code" || item.Configured {
			t.Errorf("%s unconfigured: value=%v source=%s configured=%v", tc.key, item.Value, item.Source, item.Configured)
		}
		putRiskSetting(t, db, tc.key, tc.invalid)
		item = riskThresholdItem(db, spec)
		if item.Value != tc.fallback || item.Source != "code" || !item.Configured {
			t.Errorf("%s invalid override should remain resettable but ineffective: %+v", tc.key, item)
		}
	}
	if share := riskOffhoursShareThreshold(db); share != .8 {
		t.Errorf("runtime share=%v", share)
	}
	if ips := riskTokenMultiIPThreshold(db); ips != 5 {
		t.Errorf("runtime IP count=%v", ips)
	}
	primary, _ := riskThresholdSpecByKey("risk_rule_offhours_activity_threshold")
	item := riskThresholdItem(db, primary)
	if item.Scope != "instance" || item.Value != 9 || item.Source != "risk_rules" || item.Configured {
		t.Fatalf("stored rule column must be distinguished from a settings override: %+v", item)
	}
	putRiskSetting(t, db, primary.Key, "1.5")
	item = riskThresholdItem(db, primary)
	if item.Value != 9 || item.Source != "risk_rules" || !item.Configured {
		t.Fatalf("invalid primary override should fall back to the rule column: %+v", item)
	}
}

func TestRiskListDetectorIgnoresNumericPayload(t *testing.T) {
	db := newRiskAlertTestDB(t)
	input := RiskRuleInput{Name: "名单测试", Type: "proxy_ip_login", Severity: "medium"}
	created, err := CreateRiskRule(db, input, 10)
	if err != nil {
		t.Fatalf("create without threshold: %v", err)
	}
	// Simulate a leftover key from the previous CRUD API.
	putRiskSetting(t, db, riskRuleThresholdSettingKey(created.RuleID), "999")
	if created.HasThreshold || created.Threshold != 1 {
		t.Fatalf("list-only record advertised its placeholder as a criterion: %+v", created)
	}
	records, err := ListRiskRules(db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.RuleID == created.RuleID {
			found = true
			if record.HasThreshold || record.Threshold != 1 {
				t.Fatalf("legacy key leaked into effective record: %+v", record)
			}
		}
	}
	if !found {
		t.Fatal("list-only custom instance missing")
	}
	input.Threshold = 123
	if _, err := UpdateRiskRule(db, created.RuleID, input, 11); err != nil {
		t.Fatal(err)
	}
	var row entity.RiskRule
	if err := db.Where("rule_id = ?", created.RuleID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Threshold != 1 {
		t.Fatalf("stored placeholder=%v, want 1", row.Threshold)
	}
	var count int64
	if err := db.Model(&entity.SystemSetting{}).Where("key = ?", riskRuleThresholdSettingKey(created.RuleID)).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("list-only detector has %d numeric settings", count)
	}
}

func TestRiskOffhoursEvidenceUsesEvaluatedWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	putRiskSetting(t, db, riskOffhoursStartSettingKey, "22")
	putRiskSetting(t, db, riskOffhoursEndSettingKey, "6")
	now := riskR1TestTime()
	day, _ := riskLoginWindow(now)
	// 01:00 is inside the configured window but outside the old 02:00-05:00.
	// 06:00 is exactly the exclusive end; one out of two bills must count.
	seedRiskBillingAtRaw(t, db, 45, shanghaiToRatText(t, day.Add(time.Hour)))
	seedRiskBillingAtRaw(t, db, 45, shanghaiToRatText(t, day.Add(6*time.Hour)))
	putRiskSetting(t, db, riskOffhoursShareSettingKey, "0.5")
	candidates, err := collectOffhoursActivityCandidates(db, entity.RiskRule{RuleID: "offhours_activity", Threshold: 1}, now)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%d err=%v", len(candidates), err)
	}
	var detail struct {
		OffhourStart, OffhourEnd, OffhourBills, TotalBills int
		EvidenceNote                                       string
	}
	if err := json.Unmarshal([]byte(candidates[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.OffhourBills != 1 || detail.TotalBills != 2 || detail.OffhourStart != 22 || detail.OffhourEnd != 6 || !strings.Contains(detail.EvidenceNote, "22:00") || !strings.Contains(detail.EvidenceNote, "06:00") {
		t.Fatalf("window snapshot and operator note disagree: %+v", detail)
	}
}

func TestRiskOffhoursWindowAuditsImplicitBuiltin(t *testing.T) {
	db := newRiskAlertTestDB(t) // the evaluator uses a default rule even before a row has been seeded
	if _, err := UpdateRiskOffhoursWindow(db, 23, 4, 193); err != nil {
		t.Fatal(err)
	}
	var audits []entity.RiskRuleAudit
	if err := db.Where("rule_id = ?", "offhours_activity").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].ActorID != 193 {
		t.Fatalf("missing audit for effective builtin: %+v", audits)
	}
}
