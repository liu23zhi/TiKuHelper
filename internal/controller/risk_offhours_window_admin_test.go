package controller

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskOffhoursWindowAdminRoundTrip(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	const path = "/risk/offhours-window"
	admin := &entity.User{ID: 91, Role: "admin"}
	user := &entity.User{ID: 92, Role: "user"}
	for method, handler := range map[string]gin.HandlerFunc{
		http.MethodGet: RiskOffhoursWindowGet, http.MethodPut: RiskOffhoursWindowPut, http.MethodDelete: RiskOffhoursWindowReset,
	} {
		resp := performRiskRequest(t, user, method, path, "", nil, handler)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("%s for user: %d", method, resp.Code)
		}
	}
	row := entity.RiskRule{RuleID: "offhours_activity", Type: "offhours_activity", Name: "凌晨", Threshold: 3}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: "risk_offhours_start_hour", Value: "19"}).Error; err != nil {
		t.Fatal(err)
	}
	check := func(method, body string, handler gin.HandlerFunc, start, end int, configured bool) {
		t.Helper()
		resp := performRiskRequest(t, admin, method, path, body, nil, handler)
		var got struct {
			Data struct {
				StartHour, EndHour, DefaultStartHour, DefaultEndHour int
				Configured                                           bool
			}
		}
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &got) != nil {
			t.Fatalf("%s: %d %s", method, resp.Code, resp.Body)
		}
		if w := got.Data; w.StartHour != start || w.EndHour != end || w.DefaultStartHour != 2 || w.DefaultEndHour != 5 || w.Configured != configured {
			t.Fatalf("%s: unexpected window %+v", method, w)
		}
	}
	// Do not splice a legacy half-configuration with a default.
	check(http.MethodGet, "", RiskOffhoursWindowGet, 2, 5, false)
	check(http.MethodPut, `{"startHour":22,"endHour":0}`, RiskOffhoursWindowPut, 22, 0, true)
	for _, body := range []string{`{"startHour":2}`, `{"endHour":3}`, `{"startHour":1,"endHour":1}`, `{"startHour":24,"endHour":2}`, `{"startHour":2.25,"endHour":4}`} {
		resp := performRiskRequest(t, admin, http.MethodPut, path, body, nil, RiskOffhoursWindowPut)
		if resp.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, resp.Code, resp.Body)
		}
	}
	// If the second bound fails the old pair and audit must stay intact.
	if err := db.Exec(`CREATE TRIGGER fail_end BEFORE UPDATE OF value ON system_settings WHEN OLD.key='risk_offhours_end_hour' BEGIN SELECT RAISE(ABORT,'injected fixture error'); END;`).Error; err != nil {
		t.Fatal(err)
	}
	failed := performRiskRequest(t, admin, http.MethodPut, path, `{"startHour":21,"endHour":6}`, nil, RiskOffhoursWindowPut)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("database failure: %d %s", failed.Code, failed.Body)
	}
	check(http.MethodGet, "", RiskOffhoursWindowGet, 22, 0, true)
	check(http.MethodDelete, "", RiskOffhoursWindowReset, 2, 5, false)
	var audits []entity.RiskRuleAudit
	if err := db.Where("rule_id = ?", row.RuleID).Order("id").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 {
		t.Fatalf("%d audits, want one for PUT and one for DELETE", len(audits))
	}
	for i, audit := range audits {
		var snapshot struct{ Settings map[string]string }
		if err := json.Unmarshal([]byte(audit.SnapshotJSON), &snapshot); err != nil {
			t.Fatal(err)
		}
		want := [][2]string{{"22", "0"}, {"2", "5"}}[i]
		if audit.ActorID != admin.ID || snapshot.Settings["risk_offhours_start_hour"] != want[0] || snapshot.Settings["risk_offhours_end_hour"] != want[1] {
			t.Fatalf("partial/wrong-actor audit %d: %+v", i, audit)
		}
	}
}

func TestRiskRuleCatalogReportsDetectorInputs(t *testing.T) {
	resp := performRiskRequest(t, &entity.User{ID: 91, Role: "admin"}, http.MethodGet, "/risk/rule-types", "", nil, RiskRuleTypeList)
	var result struct {
		Data []struct {
			Type, ThresholdKind, DefaultSeverity string
			DefaultThrottleSeconds               *int
			ThresholdMax                         *float64
			HasThreshold                         *bool
		}
	}
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &result) != nil {
		t.Fatalf("catalog: %d %s", resp.Code, resp.Body)
	}
	found := 0
	for _, item := range result.Data {
		if item.Type != "proxy_ip_login" && item.Type != "offhours_activity" && item.Type != "debt_batch" {
			continue
		}
		found++
		if item.DefaultSeverity == "" || item.DefaultThrottleSeconds == nil || item.HasThreshold == nil {
			t.Fatalf("missing defaults: %+v", item)
		}
		if item.Type == "debt_batch" && (item.DefaultSeverity != "high" || *item.DefaultThrottleSeconds != 0) {
			t.Fatalf("batch defaults: %+v", item)
		}
		if item.Type == "offhours_activity" && (item.DefaultSeverity != "low" || !*item.HasThreshold || item.ThresholdKind != "integer" || item.ThresholdMax == nil || *item.ThresholdMax != 365) {
			t.Fatalf("days metadata: %+v", item)
		}
		if item.Type == "proxy_ip_login" && (item.DefaultSeverity != "medium" || *item.DefaultThrottleSeconds != 86400 || *item.HasThreshold || item.ThresholdKind != "" || item.ThresholdMax != nil) {
			t.Fatalf("ASN-only catalog advertised a number: %+v", item)
		}
	}
	if found != 3 {
		t.Fatalf("expected three detectors, found %d", found)
	}
}
