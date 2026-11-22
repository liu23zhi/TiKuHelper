package service

import (
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type RiskAlertResponseView struct {
	Eligible         bool                         `json:"eligible"`
	IP               string                       `json:"ip"`
	Segment          string                       `json:"segment"`
	CityISP          string                       `json:"cityIsp"`
	BlacklistEnabled bool                         `json:"blacklistEnabled"`
	Message          string                       `json:"message,omitempty"`
	History          []entity.RiskWarningResponse `json:"history"`
	Result           *entity.RiskWarningResponse  `json:"result,omitempty"`
}

// Both the alert column and the detector-specific evidence must identify one source.
// Login warnings scoped to an account are not attributable to a single IP, even if they show an IP sample.
func riskResponseIP(w entity.RiskWarning, detector string, automatic bool) string {
	var d struct {
		Dimension, IP, IPAttribution string
		PasswordFailureQualified     bool
		IPCount                      int
		SampleIPs                    []string
	}
	if json.Unmarshal([]byte(w.Detail), &d) != nil || d.IPCount > 1 {
		return ""
	}
	switch detector {
	case entity.RiskRuleIDLoginBruteforce:
		if d.Dimension != "ip" || (automatic && !d.PasswordFailureQualified) {
			return ""
		}
	case entity.RiskRuleIDDeviceRegisterBurst:
		if d.Dimension != "registration_browser" || d.IPAttribution != "single" {
			return ""
		}
	default:
		return ""
	}
	ip := attributableRiskSourceIP(w.IP)
	if ip == "" || attributableRiskSourceIP(d.IP) != ip {
		return ""
	}
	for _, raw := range d.SampleIPs {
		if attributableRiskSourceIP(raw) != ip {
			return ""
		}
	}
	return ip
}

func warningResponseDetector(db *gorm.DB, w entity.RiskWarning) (string, error) {
	if riskResponseDetector(w.RuleID) {
		return w.RuleID, nil
	}
	var rule entity.RiskRule
	err := db.Select("rule_id", "rule_type").Where("rule_id = ?", w.RuleID).Take(&rule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return effectiveRiskRuleType(rule), err
}

// Largest aligned CIDR containing the observed IP and wholly inside the recorded interval.
// No IPv6 boundaries, /32-only result, missing row or catch-all => no segment action.
func evidenceSegment(ip string, row *entity.IPSegment) string {
	v, ok := ipv4ToUint32(ip)
	if !ok || row == nil || v < row.BeginIP || v > row.EndIP || (row.BeginIP == 0 && row.EndIP == ^uint32(0)) {
		return ""
	}
	for bits := 1; bits < 32; bits++ {
		mask := ^uint32(0) << (32 - bits)
		if v&mask >= row.BeginIP && v|^mask <= row.EndIP {
			a := netip.MustParseAddr(ip)
			return netip.PrefixFrom(a, bits).Masked().String()
		}
	}
	return ""
}

// Use the very same offline location as the production region gate.
func evidenceCityISP(db *gorm.DB, ip string) string {
	loc, ok := NewBlacklistRegionCacheLooker(db)(ip)
	if !ok {
		return ""
	}
	return loc.exactScope()
}

func responseTargets(db *gorm.DB, w entity.RiskWarning, detector string, now time.Time, automatic bool) (RiskAlertResponseView, error) {
	v := RiskAlertResponseView{BlacklistEnabled: GetBlacklistEnabledWithDB(db), IP: riskResponseIP(w, detector, automatic), History: []entity.RiskWarningResponse{}}
	if v.IP == "" {
		v.Message = "该告警没有可用于处置的单一来源 IP"
		return v, nil
	}
	wl, err := LoadWhitelistSnapshot(db, now)
	if err != nil {
		return v, err
	}
	if _, hit := wl.MatchIP(v.IP); hit {
		v.Message = "来源 IP 在白名单内，未拦截"
		return v, nil
	}
	v.Eligible = true
	if !automatic {
		if db.Migrator().HasTable(&entity.IPSegment{}) {
			segment, err := FindIPSegmentWithDB(db, v.IP)
			if err != nil {
				return v, err
			}
			v.Segment = evidenceSegment(v.IP, segment)
		}
		v.CityISP = evidenceCityISP(db, v.IP)
	}
	if !v.BlacklistEnabled {
		v.Message = "黑名单总开关已关闭，未拦截"
	}
	return v, nil
}
func loadResponseHistory(db *gorm.DB, warningID int64, v *RiskAlertResponseView) error {
	return db.Where("warning_id = ?", warningID).Order("id DESC").Find(&v.History).Error
}
func GetRiskAlertResponse(db *gorm.DB, warningID int64) (RiskAlertResponseView, error) {
	var w entity.RiskWarning
	if err := db.First(&w, warningID).Error; err != nil {
		return RiskAlertResponseView{}, err
	}
	kind, err := warningResponseDetector(db, w)
	if err != nil {
		return RiskAlertResponseView{}, err
	}
	v, err := responseTargets(db, w, kind, time.Now(), false)
	if err != nil {
		return v, err
	}
	err = loadResponseHistory(db, w.ID, &v)
	return v, err
}
