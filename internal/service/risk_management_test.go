package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskRuleManagementCustomRuleIsAuditedAndDispatched(t *testing.T) {
	db := newRiskAlertTestDB(t)
	created, err := CreateRiskRule(db, RiskRuleInput{
		Name: "自定义欠款规则", Description: "用于测试自定义检测器实例",
		Type: "debt_user", Config: json.RawMessage(`{}`), Severity: entity.RiskSeverityHigh,
		Enabled: false, Threshold: 50, ThrottleSeconds: 3600,
	}, 42)
	if err != nil {
		t.Fatalf("create custom rule: %v", err)
	}
	if created.Source != "custom" || created.Enabled || created.UpdatedBy != 42 {
		t.Fatalf("created rule did not preserve source, disabled state, and actor: %+v", created)
	}
	if created.RuleID == "" || created.Type != "debt_user" || created.Threshold != 50 {
		t.Fatalf("created custom rule has invalid identity/type/threshold: %+v", created)
	}
	if created.Unit != "元" {
		t.Fatalf("custom detector unit=%q, want 元", created.Unit)
	}
	states, err := RiskRuleStates(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.RuleID == created.RuleID && state.Unit != "元" {
			t.Fatalf("legacy custom unit=%q, want 元", state.Unit)
		}
	}

	if err := db.Create(&entity.User{Username: "risk-custom-debt", Balance: -100}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate disabled custom rule: %v", err)
	}
	var count int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ?", created.RuleID).Count(&count)
	if count != 0 {
		t.Fatalf("disabled custom rule produced %d warnings", count)
	}
	if _, err := SetRiskRuleEnabledManaged(db, created.RuleID, true, 7); err != nil {
		t.Fatalf("enable custom rule: %v", err)
	}
	if result, err := EvaluateDebtAlerts(db, now.Add(time.Minute)); err != nil || result.Created != 1 {
		t.Fatalf("custom debt_user detector did not execute: result=%+v err=%v", result, err)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", created.RuleID).First(&warning).Error; err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["ruleName"] != "自定义欠款规则" {
		t.Fatalf("warning name snapshot missing: %s", warning.Detail)
	}
	markdown, err := buildRiskWarningMarkdown(warning)
	if err != nil || !strings.Contains(markdown, "自定义欠款规则") {
		t.Fatalf("markdown did not use stored rule name: %s, %v", markdown, err)
	}
	var auditCount int64
	db.Model(&entity.RiskRuleAudit{}).Where("rule_id = ?", created.RuleID).Count(&auditCount)
	if auditCount != 2 {
		t.Fatalf("audit records = %d, want create and enable actions", auditCount)
	}
}

func TestRiskRuleManagementAuxiliarySettingsTrackActorAndAudit(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if _, err := UpdateRiskThresholdWithActor(db, RiskThresholdSettingKeyTokenMultiIP, 8, 31); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateRiskConfigWithActor(db, RiskThresholdSettingKeyProxyASNList, "AS15169", 32); err != nil {
		t.Fatal(err)
	}
	for id, actor := range map[string]int32{"token_abuse": 31, "proxy_ip_login": 32} {
		var row entity.RiskRule
		if err := db.Where("rule_id = ?", id).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.UpdatedBy != actor {
			t.Fatalf("%s actor=%d, want %d", id, row.UpdatedBy, actor)
		}
		var audit entity.RiskRuleAudit
		if err := db.Where("rule_id = ?", id).First(&audit).Error; err != nil {
			t.Fatal(err)
		}
		if audit.ActorID != actor {
			t.Fatalf("%s audit actor=%d, want %d", id, audit.ActorID, actor)
		}
		if !strings.Contains(audit.SnapshotJSON, `"settings"`) {
			t.Fatalf("%s configuration audit snapshot missing: %s", id, audit.SnapshotJSON)
		}
	}
	var tokenRule entity.RiskRule
	if err := db.Where("rule_id = ?", "token_abuse").First(&tokenRule).Error; err != nil {
		t.Fatal(err)
	}
	if tokenRule.Threshold != 3 {
		t.Fatalf("auxiliary criterion overwrote primary threshold: %v", tokenRule.Threshold)
	}
}

func TestRiskRuleManagementRejectsUnknownConfigAndSoftDeletesBuiltins(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if _, err := CreateRiskRule(db, RiskRuleInput{
		Name: "unsafe", Type: "debt_user", Config: json.RawMessage(`{"expression":"true"}`),
		Severity: entity.RiskSeverityLow, Enabled: true, Threshold: 1,
	}, 1); err == nil {
		t.Fatal("unregistered config field must be rejected")
	}
	seedRiskRules(t, db)
	if err := DeleteRiskRule(db, entity.RiskRuleIDDebtUser, 9); err != nil {
		t.Fatalf("soft-delete builtin: %v", err)
	}
	rules, err := loadRiskRules(db)
	if err != nil {
		t.Fatalf("load active rules: %v", err)
	}
	for _, rule := range rules {
		if rule.RuleID == entity.RiskRuleIDDebtUser {
			t.Fatal("deleted builtin was restored by evaluator rule loading")
		}
	}
	var row entity.RiskRule
	if err := db.Unscoped().Where("rule_id = ?", entity.RiskRuleIDDebtUser).First(&row).Error; err != nil {
		t.Fatalf("deleted builtin row should remain for seed protection/audit history: %v", err)
	}
	if row.DeletedAt == nil || row.UpdatedBy != 9 {
		t.Fatalf("soft-delete metadata missing: %+v", row)
	}
}

func TestRiskRuleManagementBuiltinMetadataAndThresholdCompatibility(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	updated, err := UpdateRiskRule(db, "debt_user", RiskRuleInput{
		Name: "欠款复核", Description: "人工编辑的说明", Type: "debt_user", Config: json.RawMessage(`{}`),
		Severity: entity.RiskSeverityHigh, Enabled: true, Threshold: 125, ThrottleSeconds: 600,
	}, 23)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "欠款复核" || updated.Threshold != 125 || updated.ThrottleSeconds != 600 || updated.UpdatedBy != 23 {
		t.Fatalf("metadata edits were not persisted: %+v", updated)
	}
	items, err := RiskThresholds(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := riskThresholdItemByKey(t, items, "risk_rule_debt_user_threshold").Value; got != 125 {
		t.Fatalf("legacy threshold view=%v, want 125", got)
	}
	if _, err := UpdateRiskThresholdWithActor(db, "risk_rule_debt_user_threshold", 150, 24); err != nil {
		t.Fatal(err)
	}
	rules, err := ListRiskRules(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.RuleID == "debt_user" && (rule.Threshold != 150 || rule.UpdatedBy != 24) {
			t.Fatalf("CRUD did not observe legacy threshold edit: %+v", rule)
		}
	}
	if _, err := ResetRiskThresholdWithActor(db, "risk_rule_debt_user_threshold", 25); err != nil {
		t.Fatal(err)
	}
	var row entity.RiskRule
	if err := db.Where("rule_id = ?", "debt_user").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Threshold != 100 || row.UpdatedBy != 25 {
		t.Fatalf("reset did not update record/audit: %+v", row)
	}
	if row.Name != "欠款复核" || row.Severity != "high" || row.ThrottleSeconds != 600 {
		t.Fatalf("legacy threshold overwrote edited metadata: %+v", row)
	}
}

func TestRiskRuleManagementTypeValidationAndCatalog(t *testing.T) {
	db := newRiskAlertTestDB(t)
	for _, tc := range []struct {
		name, kind string
		config     json.RawMessage
		threshold  float64
		throttle   int
	}{
		{"script_type", "javascript", json.RawMessage(`{}`), 1, 60},
		{"fractional_days", "offhours_activity", json.RawMessage(`{}`), 2.5, 60},
		{"negative_throttle", "debt_user", json.RawMessage(`{}`), 1, -1},
		{"array_config", "debt_user", json.RawMessage(`[]`), 1, 60},
		{"null_config", "debt_user", json.RawMessage(`null`), 1, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateRiskRule(db, RiskRuleInput{Name: "非法规则", Type: tc.kind, Config: tc.config, Severity: "low", Threshold: tc.threshold, ThrottleSeconds: tc.throttle}, 1)
			if err == nil {
				t.Fatal("expected invalid configuration to be rejected")
			}
		})
	}
	for _, kind := range RiskRuleTypes() {
		if !riskDetectorRegistered(kind.Type) {
			t.Fatalf("catalog advertises unregistered detector %s", kind.Type)
		}
	}
}

func TestRiskRuleManagementDeletedBuiltinSettingsAreHiddenWithoutLosingSharedConfig(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	custom, err := CreateRiskRule(db, RiskRuleInput{Name: "自定义令牌规则", Type: "token_abuse", Config: json.RawMessage(`{}`), Severity: "medium", Enabled: true, Threshold: 4, ThrottleSeconds: 60}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteRiskRule(db, "token_abuse", 2); err != nil {
		t.Fatal(err)
	}
	items, err := RiskThresholds(db)
	if err != nil {
		t.Fatal(err)
	}
	seenShared := false
	for _, item := range items {
		if item.Key == "risk_rule_token_abuse_threshold" {
			t.Fatal("deleted builtin's primary threshold remains editable")
		}
		if item.Key == RiskThresholdSettingKeyTokenMultiIP {
			seenShared = true
		}
	}
	if !seenShared {
		t.Fatal("live custom token rule lost its shared IP configuration")
	}
	if _, err := UpdateRiskThresholdWithActor(db, "risk_rule_token_abuse_threshold", 7, 3); !errors.Is(err, ErrRiskRuleDeleted) {
		t.Fatalf("stale deleted-rule write should be rejected, got %v", err)
	}
	if err := DeleteRiskRule(db, custom.RuleID, 4); err != nil {
		t.Fatal(err)
	}
	items, err = RiskThresholds(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.RuleID == "token_abuse" {
			t.Fatal("deleted detector settings remain editable without active instances")
		}
	}
	if err := DeleteRiskRule(db, "proxy_ip_login", 5); err != nil {
		t.Fatal(err)
	}
	configs, err := RiskConfigs(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range configs {
		if item.Key == RiskThresholdSettingKeyProxyASNList {
			t.Fatal("deleted proxy detector's ASN config remains editable")
		}
	}
}
