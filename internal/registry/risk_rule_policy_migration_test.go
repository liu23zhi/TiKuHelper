package registry

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

func TestRiskRulePolicyLegacyMigrationSQLite(t *testing.T) {
	checkRulePolicyLegacyMigration(t, newRiskMigrationTestDB(t))
}

func TestMariaDBRiskRulePolicyLegacyMigration(t *testing.T) {
	db := mysqlTestFreshDB(t)
	t.Logf("server version %s", mariaDBServerVersion(t, db))
	checkRulePolicyLegacyMigration(t, db)
}

func checkRulePolicyLegacyMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.AutoMigrate(&entity.SystemSetting{}, &entity.RiskRule{}, &entity.RiskRuleAudit{}, &entity.RiskWarning{}, &entity.RiskWarningResponse{}, &entity.BlacklistEntry{}))
	// Recreate the schema state from deployments with v2 rules and a response ledger.
	must(db.Exec("ALTER TABLE risk_rules DROP COLUMN response_policy_json").Error)
	must(db.Exec("ALTER TABLE risk_warning_responses DROP COLUMN entry_version").Error)
	must(setSystemSettingFlag(db, riskRulesSchemaReadyKey))
	must(setSystemSettingFlag(db, riskWarningsSchemaReadyKey))
	const login = entity.RiskRuleIDLoginBruteforce
	const registration = entity.RiskRuleIDDeviceRegisterBurst
	now := time.Now()
	for _, row := range []entity.RiskRule{
		{RuleID: "custom_hot", Type: login, Source: "custom", Enabled: true, Threshold: 10},
		{RuleID: "custom_cold", Type: registration, Source: "custom", Enabled: true, Threshold: 3},
		{RuleID: "custom_deleted", Type: login, Source: "custom", DeletedAt: &now, Enabled: true, Threshold: 10},
	} {
		must(db.Omit("ResponsePolicyJSON").Create(&row).Error)
	}
	if _, err := service.PutRiskResponsePolicy(db, login, "temp_ip", 27, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PutRiskResponsePolicy(db, registration, "notify", 30, 11); err != nil {
		t.Fatal(err)
	}
	must(db.Create(&entity.RiskWarning{RuleID: login, IP: "192.0.2.40", CreatedAt: now}).Error)
	must(db.Omit("EntryVersion").Create(&entity.RiskWarningResponse{WarningID: 1, Action: "ip", Target: "192.0.2.40", ActorID: 0, Status: "skipped"}).Error)
	must(EnsureRiskSchema(db))
	if !systemSettingFlagIsSet(db, service.RiskRuleResponseMigrationKey) || !db.Migrator().HasColumn(&entity.RiskWarningResponse{}, "entry_version") {
		t.Fatal("schema migration not recorded")
	}
	if _, err := service.PutRiskResponsePolicy(db, login, "temp_ip", 28, 11); !errors.Is(err, service.ErrLegacyRiskResponsePolicyReadOnly) {
		t.Fatalf("legacy update unexpectedly accepted: %v", err)
	}
	for _, pass := range []int{1, 2} {
		wantCustom := "temporary"
		if pass == 2 {
			must(db.Model(&entity.RiskRule{}).Where("rule_id = ?", "custom_hot").UpdateColumn("response_policy_json", `{"mode":"notify"}`).Error)
			must(EnsureRiskSchema(db))
			wantCustom = "notify"
		}
		rules, err := service.ListRiskRules(db)
		must(err)
		got := map[string]service.RiskRuleRecord{}
		for _, rule := range rules {
			got[rule.RuleID] = rule
		}
		for id, mode := range map[string]string{login: "temporary", registration: "notify", "custom_hot": wantCustom, "custom_cold": "notify"} {
			if rv := got[id]; rv.ResponsePolicy.Mode != mode || rv.EffectiveResponsePolicy.Mode != "notify" || (mode == "temporary" && (rv.ResponsePolicy.Scope != "ip" || rv.ResponsePolicy.DurationMinutes != 27)) {
				t.Fatalf("pass=%d id=%s rule=%+v", pass, id, rv)
			}
		}
		if _, seen := got["custom_deleted"]; seen {
			t.Fatal("migration restored a tombstone")
		}
	}
	// The same type saved after migration must not inherit the old population's policy.
	fresh, err := service.CreateRiskRule(db, service.RiskRuleInput{Name: "new synthetic rule", Type: login, Severity: "medium", Threshold: 10, Enabled: true}, 11)
	if err != nil || fresh.ResponsePolicy.Mode != "notify" {
		t.Fatalf("new policy leaked %+v %v", fresh, err)
	}
	var old entity.RiskWarningResponse
	must(db.First(&old, 1).Error)
	if old.Target != "192.0.2.40" || old.EntryVersion != nil {
		t.Fatalf("legacy history changed: %+v", old)
	}
	var n int64
	must(db.Model(&entity.BlacklistEntry{}).Count(&n).Error)
	if n != 0 {
		t.Fatalf("migration reprocessed historical warnings: entries=%d", n)
	}
	t.Log("legacy temporary single IP stayed at 27 minutes; old notify / new same-type notify / tombstones / historical warnings preserved; global default unchanged; repeated startup is idempotent")
}

func TestMariaDBRiskRuleAutomaticSegmentRevoke(t *testing.T) {
	db := mysqlTestFreshDB(t)
	for _, m := range []any{&entity.SystemSetting{}, &entity.BlacklistEntry{}, &entity.WhitelistEntry{}, &entity.LocationCache{}, &entity.LoginFailureEvent{}} {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatal(err)
	}
	const login = entity.RiskRuleIDLoginBruteforce
	if err := db.Model(&entity.RiskRule{}).Where("rule_id <> ?", login).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: service.BlacklistEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateRiskRule(db, login, service.RiskRuleInput{Name: "synthetic MariaDB rule", Severity: "low", Threshold: 10, ThrottleSeconds: 600, ResponsePolicy: &service.RiskRuleResponseInput{RiskRuleResponsePolicy: service.RiskRuleResponsePolicy{Mode: "permanent", Scope: "segment"}, Confirmed: true}}, 7); err != nil {
		t.Fatal(err)
	}
	const ip = "203.0.113.44"
	if err := db.Create(&entity.LocationCache{Kind: entity.LocationKindIP, Source: "standard", LookupKey: ip, RawJSON: `{"beginip":"203.0.113.33","endip":"203.0.113.57"}`}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Existing MariaDB risk queries compare Shanghai wall-clock DATETIME, but
	// CI fixture DSNs use loc=UTC. Bind the stored wall-clock, not a time.Time
	// silently converted by the driver, so the fixture models production rows.
	wallClock := now.In(logger.ShanghaiLocation()).Add(-2 * time.Second).Format("2006-01-02 15:04:05")
	for i := 0; i < 10; i++ {
		if err := db.Exec("INSERT INTO "+entity.TableNameLoginFailureEvent+" (client_ip, scene, reason, created_at) VALUES (?, ?, ?, ?)",
			ip, entity.LoginFailureScenePassword, entity.LoginFailureReasonBadPassword, wallClock).Error; err != nil {
			t.Fatal(err)
		}
	}
	eval, err := service.EvaluateDebtAlerts(db, now)
	if err != nil {
		t.Fatal(err)
	}
	var applied entity.RiskWarningResponse
	if err := db.Where("status = ?", "applied").Take(&applied).Error; err != nil {
		var ledger []struct{ Status, OutcomeCode string }
		_ = db.Model(&entity.RiskWarningResponse{}).Select("status", "outcome_code").Find(&ledger).Error
		var warnings, events int64
		_ = db.Model(&entity.RiskWarning{}).Count(&warnings).Error
		_ = db.Model(&entity.LoginFailureEvent{}).Count(&events).Error
		t.Fatalf("missing applied receipt: eval=%+v ledger=%+v warnings=%d events=%d: %v", eval, ledger, warnings, events, err)
	}
	if applied.ExpiresAt != nil || applied.Target != "203.0.113.33-203.0.113.57" || applied.EntryVersion == nil {
		t.Fatalf("bad permanent action: %+v", applied)
	}
	v, err := service.GetRiskAlertResponse(db, applied.WarningID)
	if err != nil || len(v.History) != 1 || !v.History[0].CanRevoke {
		t.Fatalf("MariaDB lost revocable ownership: %+v %v", v.History, err)
	}
	v, err = service.RevokeAutomaticRiskResponse(db, applied.WarningID, applied.ID, 7)
	if err != nil || v.Result == nil || v.Result.Status != "revoked" {
		t.Fatalf("not revoked: %+v %v", v.Result, err)
	}
	var entry entity.BlacklistEntry
	if err := db.First(&entry, applied.EntryID).Error; err != nil || entry.Enabled {
		t.Fatalf("not disabled: %t %v", entry.Enabled, err)
	}
	if _, err := service.EvaluateDebtAlerts(db, now); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&entity.RiskWarningResponse{}).Where("warning_id = ?", applied.WarningID).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("unexpected replay: %d %v", n, err)
	}
	t.Log("MariaDB exact-range auto action wrote ownership timestamp; revoke disabled only that entry and replay did not recreate it")
}
