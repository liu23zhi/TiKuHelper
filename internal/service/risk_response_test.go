package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"testing"
	"time"
)

// Expiry is a property of the rule, not of the last successful cache reload.
func TestRiskResponseCachedRuleStopsAtExpiry(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	snap := newBlacklistSnapshot(past.Add(-time.Minute), true)
	for _, rule := range []BlacklistRule{
		{Kind: BlacklistKindIP, Pattern: "203.0.113.8", MatchType: BlacklistMatchExact, ExpiresAt: &past},
		{Kind: BlacklistKindIP, Pattern: "198.51.100.0/24", MatchType: BlacklistMatchCIDR, ExpiresAt: &past},
		{Kind: BlacklistKindRegion, Pattern: "* 中山 移动", MatchType: BlacklistMatchPrefix, ExpiresAt: &past},
	} {
		if !snap.addRule(&rule) {
			t.Fatalf("fixture rejected: %+v", rule)
		}
	}
	if _, hit := snap.MatchIP("203.0.113.8"); hit {
		t.Error("stale exact snapshot still blocks after expiry")
	}
	if _, hit := snap.MatchIP("198.51.100.3"); hit {
		t.Error("stale CIDR snapshot still blocks after expiry")
	}
	if _, hit := snap.MatchRegionLocation("中国 广东 中山", "移动", ""); hit {
		t.Error("stale region snapshot still blocks after expiry")
	}
}

func responseTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newRiskAlertTestDB(t)
	for _, m := range []any{&entity.RiskWarningResponse{}, &entity.BlacklistEntry{}, &entity.WhitelistEntry{}, &entity.LocationCache{}} {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
func responseTestWarning(t *testing.T, db *gorm.DB, ruleID, ip, dim, attribution string) entity.RiskWarning {
	t.Helper()
	d, _ := json.Marshal(map[string]string{"ip": ip, "dimension": dim, "ipAttribution": attribution})
	w := entity.RiskWarning{RuleID: ruleID, IP: ip, Detail: string(d), CreatedAt: time.Now()}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	return w
}
func TestRiskResponsePolicyDefaultAndCustomDetector(t *testing.T) {
	db := responseTestDB(t)
	const det = "login_bruteforce"
	p, err := GetRiskResponsePolicy(db, det)
	if err != nil || p.Mode != "notify" || p.BlacklistEnabled {
		t.Fatalf("unsafe default: %+v, %v", p, err)
	}
	if _, err := PutRiskResponsePolicy(db, det, "temp_ip", 0, 21); !errors.Is(err, ErrRiskResponseInvalid) {
		t.Fatalf("accepted permanent auto: %v", err)
	}
	if _, err := PutRiskResponsePolicy(db, det, "temp_ip", 30, 21); err != nil {
		t.Fatal(err)
	}
	var audit entity.RiskRuleAudit
	if err := db.Where("rule_id = ? AND actor_id = ?", det, 21).First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.RiskRule{RuleID: "custom_response", Source: "custom", Type: det, Enabled: true, Threshold: 10, ThrottleSeconds: 600}).Error; err != nil {
		t.Fatal(err)
	}
	// User-ID attribution can exist on an IP warning too. Only the detector dimension decides.
	seedRiskRules(t, db)
	if err := db.Model(&entity.RiskRule{}).Where("rule_id <> ?", "custom_response").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	setBlacklistEnabled(t, db, "true")
	now := time.Now().Add(-2 * time.Minute) // preserve the detector window; enforcement has a distinct clock
	for i := 0; i < 10; i++ {
		seedR2Failure(t, db, 157, "fixture", "203.0.113.54", now.Add(-time.Duration(i+1)*time.Second))
	}
	started := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatal(err)
		}
	}
	var rows []entity.RiskWarningResponse
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "applied" || rows[0].ActorID != 0 || rows[0].DurationMinutes != 30 || rows[0].Action != "ip" || rows[0].Target != "203.0.113.54" {
		t.Fatalf("auto reactions: %+v", rows)
	}
	if rows[0].ExpiresAt == nil || rows[0].CreatedAt.Before(started) || rows[0].CreatedAt.After(time.Now()) || !rows[0].ExpiresAt.Equal(rows[0].CreatedAt.Add(30*time.Minute)) {
		t.Fatalf("expiry=%v", rows[0].ExpiresAt)
	}
}

func TestRiskResponseTargetsRequireEvidenceAndConfirmation(t *testing.T) {
	db := responseTestDB(t)
	const ip = "203.0.113.94"
	w := responseTestWarning(t, db, "device_register_burst", ip, "registration_browser", "single")
	for _, tc := range []struct{ rule, dim, attribution, ip string }{
		{"device_register_burst", "registration_browser", "multiple", ip},
		{"device_register_burst", "registration_browser", "unknown", ip},
		{"login_bruteforce", "account", "", ip},
		{"login_bruteforce", "ip", "", "127.0.0.1"},
		{"other", "ip", "", ip},
	} {
		bad := responseTestWarning(t, db, tc.rule, tc.ip, tc.dim, tc.attribution)
		got, err := GetRiskAlertResponse(db, bad.ID)
		if err != nil || got.Eligible || got.IP != "" {
			t.Fatalf("unsafe target %+v: %+v %v", tc, got, err)
		}
	}
	got, err := GetRiskAlertResponse(db, w.ID)
	if err != nil || !got.Eligible || got.Segment != "" || got.CityISP != "" {
		t.Fatalf("guessed unrecorded range: %+v %v", got, err)
	}
	lo, _ := ipv4ToUint32("203.0.113.88")
	hi, _ := ipv4ToUint32("203.0.113.133")
	if err := db.Create(&entity.IPSegment{BeginIP: lo, EndIP: hi}).Error; err != nil {
		t.Fatal(err)
	}
	loc := entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: ip, Address: "中国 广东 未知 移动", RawJSON: `{"region":"中国 广东 未知","llc":"移动"}`}
	if err := db.Create(&loc).Error; err != nil {
		t.Fatal(err)
	}
	setBlacklistEnabled(t, db, "true")
	got, err = GetRiskAlertResponse(db, w.ID)
	if err != nil || got.Segment != "203.0.113.88/29" || got.CityISP != "" {
		t.Fatalf("range or unknown city: %+v %v", got, err)
	}
	if err := db.Model(&loc).Updates(map[string]any{"address": "中国 广东 中山 移动", "raw_json": `{"region":"中国 广东 中山","llc":"移动"}`}).Error; err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"segment", "city_isp", "ip"} {
		ttl := 15
		if action == "ip" {
			ttl = 0
		}
		_, err = ApplyRiskAlertResponse(db, w.ID, 9, RiskResponseInput{Action: action, DurationMinutes: ttl, Reason: "reviewed"})
		if !errors.Is(err, ErrRiskResponseInvalid) {
			t.Fatalf("unconfirmed %s accepted: %v", action, err)
		}
	}
	if _, stale := ApplyRiskAlertResponse(db, w.ID, 9, RiskResponseInput{Action: "city_isp", DurationMinutes: 10, Reason: "old preview", Confirmed: true, ExpectedTarget: "其他市 · 移动"}); !errors.Is(stale, ErrRiskResponseInvalid) {
		t.Fatalf("changed preview was accepted: %v", stale)
	}
	got, err = ApplyRiskAlertResponse(db, w.ID, 9, RiskResponseInput{Action: "city_isp", DurationMinutes: 10, Reason: "test review", Confirmed: true, ExpectedTarget: "中国 · 广东 · 中山 · 移动"})
	if err != nil || got.Result.Status != "applied" || got.CityISP == "" {
		t.Fatalf("%+v %v", got, err)
	}
	snap, err := LoadBlacklistSnapshot(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		place, isp string
		want       bool
	}{{"中国 广东 中山", "移动", true}, {"中国 广东 珠海", "移动", false}, {"中国 广东 中山", "联通", false}} {
		if _, matched := snap.MatchRegionLocation(tt.place, tt.isp, ""); matched != tt.want {
			t.Fatalf("place=%s isp=%s matched=%v", tt.place, tt.isp, matched)
		}
	}
}

func TestRiskResponseReplayRenewalAndProtection(t *testing.T) {
	db := responseTestDB(t)
	w := responseTestWarning(t, db, "login_bruteforce", "203.0.113.76", "ip", "")
	input := RiskResponseInput{Action: "ip", DurationMinutes: 30, Reason: "人工复核"}
	got, err := ApplyRiskAlertResponse(db, w.ID, 7, input)
	if err != nil || got.Result.Status != "skipped" || got.BlacklistEnabled {
		t.Fatalf("disabled gate %+v %v", got, err)
	}
	var n int64
	db.Model(&entity.BlacklistEntry{}).Count(&n)
	if n != 0 {
		t.Fatal("disabled gate wrote a latent ban")
	}
	setBlacklistEnabled(t, db, "true")
	first, err := ApplyRiskAlertResponse(db, w.ID, 7, input)
	if err != nil || first.Result.Status != "applied" || first.Result.ActorID != 7 {
		t.Fatalf("%+v %v", first, err)
	}
	replay, err := ApplyRiskAlertResponse(db, w.ID, 7, input)
	if err != nil || first.Result.ID != replay.Result.ID || !first.Result.ExpiresAt.Equal(*replay.Result.ExpiresAt) {
		t.Fatalf("replay extended ban: %+v, %v", replay, err)
	}
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", first.Result.EntryID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	next := responseTestWarning(t, db, "login_bruteforce", w.IP, "ip", "")
	renewed, err := ApplyRiskAlertResponse(db, next.ID, 8, input)
	if err != nil || renewed.Result.Status != "applied" || renewed.Result.EntryID != first.Result.EntryID || renewed.Result.ActorID != 8 {
		t.Fatalf("expired entry: %+v %v", renewed, err)
	}
	perm := RiskResponseInput{Action: "ip", DurationMinutes: 0, Confirmed: true, Reason: "人工升级"}
	upgraded, err := ApplyRiskAlertResponse(db, next.ID, 8, perm)
	if err != nil || upgraded.Result.Status != "applied" || upgraded.Result.ExpiresAt != nil {
		t.Fatalf("permanent upgrade: %+v %v", upgraded, err)
	}
	short := responseTestWarning(t, db, "login_bruteforce", w.IP, "ip", "")
	kept, err := ApplyRiskAlertResponse(db, short.ID, 9, input)
	if err != nil || kept.Result.Status != "existing" || kept.Result.ExpiresAt != nil {
		t.Fatalf("shortened permanent ban: %+v %v", kept, err)
	}
	outside := responseTestWarning(t, db, "login_bruteforce", "203.0.113.77", "ip", "")
	exp := time.Now().Add(5 * time.Minute)
	original, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{Kind: BlacklistKindIP, Pattern: outside.IP, Enabled: true, Reason: "其他渠道", CreatedBy: 42, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	kept, err = ApplyRiskAlertResponse(db, outside.ID, 9, perm)
	if err != nil || kept.Result.Status != "existing" {
		t.Fatalf("external rule was overwritten: %+v %v", kept, err)
	}
	var entry entity.BlacklistEntry
	if err := db.First(&entry, original.ID).Error; err != nil || entry.ExpiresAt == nil || !entry.ExpiresAt.Equal(exp) || entry.CreatedBy != 42 || entry.Reason != "其他渠道" {
		t.Fatalf("lost original rule: %+v %v", entry, err)
	}
	allow := responseTestWarning(t, db, "login_bruteforce", "192.0.2.89", "ip", "")
	putRiskSetting(t, db, WhitelistExemptIPsSettingKey, fmt.Sprintf("%s/32", allow.IP))
	got, err = ApplyRiskAlertResponse(db, allow.ID, 9, input)
	if err != nil || got.Result.Status != "skipped" || got.Result.EntryID != 0 {
		t.Fatalf("whitelisted target: %+v %v", got, err)
	}
}

// The evidence preview and the production lookup must agree even if only the segment was cached.
func TestRiskResponseRegionUsesContainingSegment(t *testing.T) {
	db := responseTestDB(t)
	setBlacklistEnabled(t, db, "true")
	const source = "192.0.2.34"
	w := responseTestWarning(t, db, "login_bruteforce", source, "ip", "")
	lo, _ := ipv4ToUint32("192.0.2.32")
	hi, _ := ipv4ToUint32("192.0.2.63")
	if err := db.Create(&entity.IPSegment{BeginIP: lo, EndIP: hi, Region: "中国 广东 中山", LLC: "移动"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: source, Address: "中国 广东", RawJSON: `{"region":"中国 广东","llc":"N/A"}`}).Error; err != nil {
		t.Fatal(err)
	}
	out, err := ApplyRiskAlertResponse(db, w.ID, 6, RiskResponseInput{Action: "city_isp", DurationMinutes: 10, Confirmed: true, Reason: "人工复核", ExpectedTarget: "中国 · 广东 · 中山 · 移动"})
	if err != nil || out.CityISP != "中国 · 广东 · 中山 · 移动" {
		t.Fatalf("incomplete single-IP cache with known segment: %+v %v", out, err)
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatal(err)
	}
	looker := NewBlacklistRegionCacheLooker(db)
	for _, tc := range []struct {
		ip   string
		want BlacklistRegionOutcome
	}{{source, BlacklistRegionHit}, {"192.0.2.61", BlacklistRegionHit}, {"192.0.2.64", BlacklistRegionMiss}} {
		_, got := EvaluateBlacklistRegion(cache, looker, tc.ip)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.ip, got, tc.want)
		}
	}
}
