package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestDeviceRegistrationDetectorUsesOnlyUniformConfirmedSource(t *testing.T) {
	const kind = "device_register_burst"
	now := time.Date(2026, 11, 20, 15, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	for _, tc := range []struct{ name, ip2, status, ip string }{
		{"uniform", "203.0.113.41", "single", "203.0.113.41"},
		{"mapped IPv4", "::ffff:203.0.113.41", "single", "203.0.113.41"},
		{"two sources", "203.0.113.42", "multiple", ""},
		{"shared internal proxy", "172.18.0.1", "unknown", ""},
		{"carrier NAT", "100.64.0.1", "unknown", ""},
		{"missing source", "", "unknown", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newRiskAlertTestDB(t)
			// Use the public detector registry and windowed DB evidence, not a stub.
			detector, ok := riskDetectorByType(kind)
			if !ok {
				t.Fatal("device-specific detector missing")
			}
			if err := db.AutoMigrate(&entity.RegistrationBrowserEvent{}); err != nil {
				t.Fatal(err)
			}
			add := func(id int32, hash, ip string, at time.Time) {
				t.Helper()
				if err := RecordRegistrationBrowserEventWithDB(db, id, hash, ip, at); err != nil {
					t.Fatal(err)
				}
			}
			a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
			add(1, a, "203.0.113.41", now.Add(-45*time.Minute))
			add(1, b, "203.0.113.77", now) // replay must not change account 1's original browser or IP
			add(2, a, tc.ip2, now.Add(-5*time.Minute))
			add(3, b, "203.0.113.41", now.Add(-time.Minute)) // same NAT, different browser
			add(4, a, "203.0.113.99", now.Add(-time.Hour))
			add(5, a, "203.0.113.99", now.Add(time.Second))
			rule := entity.RiskRule{RuleID: "fixture_device_check", Type: kind, Threshold: 3, Severity: "medium", ThrottleSeconds: 21600}
			rows, err := detector(db, rule, now)
			if err != nil || len(rows) != 0 {
				t.Fatalf("mixed browsers or out-of-window evidence counted: %+v, %v", rows, err)
			}
			add(6, a, "203.0.113.41", now)
			rows, err = detector(db, rule, now)
			if err != nil || len(rows) != 1 {
				t.Fatalf("want one browser, got %+v, %v", rows, err)
			}
			c := rows[0]
			var d map[string]any
			if err := json.Unmarshal([]byte(c.Detail), &d); err != nil {
				t.Fatal(err)
			}
			if c.Rule.RuleID != rule.RuleID || c.UserID != nil || d["dimension"] != "registration_browser" || d["accountCount"] != float64(3) || d["threshold"] != float64(3) || d["ipAttribution"] != tc.status || c.IP != tc.ip {
				t.Fatalf("incorrect device evidence: %+v %+v", c, d)
			}
			ip, has := d["ip"]
			if has != (tc.ip != "") || (has && ip != tc.ip) {
				t.Fatalf("unexpected detail.ip: %v (%v)", ip, has)
			}
			if tc.ip == "" && strings.Contains(c.Detail, "203.0.113.") {
				t.Fatal("ambiguous source should not export IP samples")
			}
		})
	}
}

func TestDeviceRegistrationThresholdIsPositiveAccountCount(t *testing.T) {
	spec, ok := riskThresholdSpecByKey("risk_rule_device_register_burst_threshold")
	if !ok || spec.Kind != "integer" || spec.Unit != "个" || !spec.SyncRuleColumn {
		t.Fatalf("missing integer account-count threshold: %+v", spec)
	}
	for _, v := range []float64{0, 1.5, 10001} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			if err := ValidateRiskThresholdValueForKey(spec.Key, spec.Kind, v); err == nil {
				t.Fatalf("accepted %v", v)
			}
		})
	}
}

func TestDeviceRegistrationWorkerPrunesOldEvidenceWithRuleDisabled(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.RegistrationBrowserEvent{}); err != nil {
		t.Fatal(err)
	}
	seedRiskRules(t, db)
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "device_register_burst").UpdateColumn("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, at := range []time.Time{now.Add(-25 * time.Hour), now.Add(-23 * time.Hour)} {
		if err := RecordRegistrationBrowserEventWithDB(db, int32(i+1), strings.Repeat("a", 64), "127.0.0.1", at); err != nil {
			t.Fatal(err)
		}
	}
	stop := StartRiskAlertEvaluationWorker(db, 25*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var rows []entity.RegistrationBrowserEvent
		if err := db.Order("user_id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 && rows[0].UserID == 2 && rows[0].ClientIP == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not prune only the expired registration, independent of rule state")
}
