package service

import (
	"encoding/json"
	"errors"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

func TestRiskResponseEnforcementDecisionAttribution(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	w := responseTestWarning(t, db, "login_bruteforce", "192.0.2.7", "ip", "")
	a := RiskResponseInput{Action: "ip", DurationMinutes: 30, Reason: "first review"}
	v, err := ApplyRiskAlertResponse(db, w.ID, 11, a)
	if err != nil {
		t.Fatal(err)
	}
	a.Reason = "independent review"
	second, err := ApplyRiskAlertResponse(db, w.ID, 22, a)
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&entity.RiskWarningResponse{}).Where("warning_id = ?", w.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("first_actor=%d second_requested_actor=22 second_recorded_actor=%d second_recorded_reason=%q ledger_rows=%d same_expiry=%t", v.Result.ActorID, second.Result.ActorID, second.Result.Reason, count, second.Result.ExpiresAt.Equal(*v.Result.ExpiresAt))
	if second.Result.ActorID != 22 || second.Result.Reason != a.Reason || count != 2 || second.Result.Status != "existing" || !second.Result.ExpiresAt.Equal(*v.Result.ExpiresAt) {
		t.Fatal("independent admin decision was not recorded without extending the ban")
	}
	thirdInput := a
	thirdInput.Reason = "Independent review" // case alone still distinguishes audit intent on MySQL
	third, err := ApplyRiskAlertResponse(db, w.ID, 22, thirdInput)
	if err != nil {
		t.Fatal(err)
	}
	if third.Result.ID == second.Result.ID || third.Result.Reason != thirdInput.Reason || !third.Result.ExpiresAt.Equal(*v.Result.ExpiresAt) {
		t.Fatal("different reason was collapsed or extended the ban")
	}
	fourth, err := ApplyRiskAlertResponse(db, w.ID, 33, a)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Result.ID == second.Result.ID || fourth.Result.ActorID != 33 {
		t.Fatal("different operator was collapsed")
	}
	replay, err := ApplyRiskAlertResponse(db, w.ID, 22, a)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Result.ID != second.Result.ID || len(replay.History) != 4 {
		t.Fatal("identical retry created another decision")
	}

}

func TestRiskResponseEnforcementSameNamedCities(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	w := responseTestWarning(t, db, "device_register_burst", "192.0.2.12", "registration_browser", "single")
	for _, row := range []entity.LocationCache{
		{Kind: entity.LocationKindIP, LookupKey: w.IP, Address: "Country North Springfield Carrier", RawJSON: `{"region":"Country North Springfield","llc":"Carrier"}`},
		{Kind: entity.LocationKindIP, LookupKey: "198.51.100.17", Address: "Country South Springfield Carrier", RawJSON: `{"region":"Country South Springfield","llc":"Carrier"}`},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	view, err := GetRiskAlertResponse(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyRiskAlertResponse(db, w.ID, 11, RiskResponseInput{Action: "city_isp", DurationMinutes: 15, Reason: "scope review", ExpectedTarget: view.CityISP, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatal(err)
	}
	lookup := NewBlacklistRegionCacheLooker(db)
	_, north := EvaluateBlacklistRegion(cache, lookup, w.IP)
	_, south := EvaluateBlacklistRegion(cache, lookup, "198.51.100.17")
	t.Logf("approved_target=%q stored_pattern=%q source_location_matched=%t other_province_same_city_name_matched=%t", view.CityISP, out.Result.Target, north == BlacklistRegionHit, south == BlacklistRegionHit)
	if !strings.Contains(view.CityISP, "North") || !strings.Contains(view.CityISP, "Country") || !strings.Contains(out.Result.Target, "North") {
		t.Fatal("confirmation, audit, and rule must include the full geographic scope")
	}
	for _, pair := range []string{"Other North Springfield", "Country South Springfield", "Country North Other", "Country N/A Springfield", "Country North Springfield ambiguous"} {
		if _, hit := cache.Snapshot().MatchRegionLocation(pair, "Carrier", ""); hit {
			t.Fatalf("matched outside confirmed geography: %q", pair)
		}
	}
	if north != BlacklistRegionHit || south == BlacklistRegionHit {
		t.Error("region decision includes a separate city outside the confirmed geography")
	}
}

func TestRiskResponseEnforcementDurationBeginsAtEnforcement(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	if _, e := PutRiskResponsePolicy(db, "device_register_burst", "temp_ip", 1, 11); e != nil {
		t.Fatal(e)
	}
	w := responseTestWarning(t, db, "device_register_burst", "192.0.2.25", "registration_browser", "single")
	evaluationStarted := time.Now().Add(-2 * time.Minute)
	w.CreatedAt = evaluationStarted
	if e := db.Model(&w).Update("created_at", evaluationStarted).Error; e != nil {
		t.Fatal(e)
	}
	if e := respondToNewWarning(db, "device_register_burst", &w); e != nil {
		t.Fatal(e)
	}
	var row entity.RiskWarningResponse
	if e := db.Where("warning_id = ?", w.ID).First(&row).Error; e != nil {
		t.Fatal(e)
	}
	cache := NewBlacklistCache()
	if e := cache.Reload(db); e != nil {
		t.Fatal(e)
	}
	_, hit := cache.Snapshot().MatchIP(w.IP)
	t.Logf("policy_minutes=1 elapsed_evaluation_minutes=2 result=%q active_at_commit=%t", row.Status, hit)
	if !hit {
		t.Error("new one-minute action was already expired before it was committed")
	}
}

func TestRiskResponseEnforcementRejectsStaleAndAmbiguousGeo(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	w := responseTestWarning(t, db, "device_register_burst", "192.0.2.12", "registration_browser", "single")
	row := entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: w.IP, Address: "Country North Springfield Carrier", RawJSON: `{"region":"Country North Springfield","llc":"Carrier"}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	first, err := GetRiskAlertResponse(db, w.ID)
	if err != nil || first.CityISP == "" {
		t.Fatal("missing complete scope")
	}
	for _, region := range []string{"Other North Springfield", "Country South Springfield", "Country N/A Springfield", "Country North", "Country North Springfield extra"} {
		raw, _ := json.Marshal(map[string]string{"region": region, "llc": "Carrier"})
		if err = db.Model(&row).Update("raw_json", string(raw)).Error; err != nil {
			t.Fatal(err)
		}
		preview, err := GetRiskAlertResponse(db, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ApplyRiskAlertResponse(db, w.ID, 11, RiskResponseInput{Action: "city_isp", DurationMinutes: 15, Reason: "review", ExpectedTarget: first.CityISP, Confirmed: true})
		if !errors.Is(err, ErrRiskResponseInvalid) {
			t.Fatalf("stale scope accepted: %s", region)
		}
		if (strings.Contains(region, "N/A") || region == "Country North" || strings.HasSuffix(region, "extra")) && preview.CityISP != "" {
			t.Fatalf("ambiguous region offered: %s", region)
		}
	}
}

// Advance a controlled execution clock *after* INSERT acquired its unique key.
// A timestamp captured before the warning/blacklist DB locks would expire early.
func TestRiskResponseEnforcementClockAfterRuleInsert(t *testing.T) {
	d := responseTestDB(t)
	setBlacklistEnabled(t, d, "true")
	w := responseTestWarning(t, d, "login_bruteforce", "192.0.2.52", "ip", "")
	start := time.Now().Truncate(time.Second)
	clock := start
	d = d.Session(&gorm.Session{NowFunc: func() time.Time { return clock }})
	err := d.Callback().Create().After("gorm:create").Register("fixture:rule-lock-wait", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBlacklistEntry {
			clock = start.Add(2 * time.Minute)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyRiskAlertResponse(d, w.ID, 7, RiskResponseInput{Action: "ip", DurationMinutes: 1, Reason: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if !clock.After(start) || !got.Result.CreatedAt.Equal(clock) || got.Result.ExpiresAt == nil || !got.Result.ExpiresAt.Equal(clock.Add(time.Minute)) {
		t.Fatal("lease consumed lock-wait time")
	}
}
