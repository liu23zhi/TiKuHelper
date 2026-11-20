package service

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const registrationBrowserWindow = time.Hour
const registrationBrowserEventRetention = 24 * time.Hour

// RecordRegistrationBrowserEventWithDB is called only after a successful
// registration commit, never on attempts or on the authenticated device path.
// Replays cannot inflate the count or overwrite the first source/timestamp.
func RecordRegistrationBrowserEventWithDB(db *gorm.DB, userID int32, browserHash, clientIP string, at time.Time) error {
	if db == nil || userID <= 0 || len(browserHash) != 64 || at.IsZero() {
		return errors.New("invalid registration browser evidence")
	}
	if _, err := hex.DecodeString(browserHash); err != nil {
		return errors.New("invalid registration browser digest")
	}
	event := entity.RegistrationBrowserEvent{
		UserID:         userID,
		BrowserHash:    strings.ToLower(browserHash),
		ClientIP:       attributableRiskSourceIP(clientIP),
		RegisteredUnix: at.Unix(),
	}
	return db.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error
}

// Runs from the evaluation worker even if the rule is disabled/deleted.
func pruneRegistrationBrowserEvents(db *gorm.DB, now time.Time) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameRegistrationBrowserEvent) {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true}).Where("registered_unix <= ?", now.Add(-registrationBrowserEventRetention).Unix()).Delete(&entity.RegistrationBrowserEvent{}).Error
}

func collectDeviceRegisterBurstCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	if !db.Migrator().HasTable(entity.TableNameRegistrationBrowserEvent) {
		return nil, nil
	}
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(entity.RiskRuleIDDeviceRegisterBurst))
	start := now.Add(-registrationBrowserWindow)
	var groups []struct {
		BrowserHash                    string
		Accounts, KnownIPs, MissingIPs int64
		FirstIP                        string
	}
	// UserID is the primary key: one row means one distinct committed account.
	err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RegistrationBrowserEvent{}).
		Select("browser_hash, COUNT(*) AS accounts, COUNT(DISTINCT NULLIF(client_ip, '')) AS known_ips, SUM(CASE WHEN client_ip = '' THEN 1 ELSE 0 END) AS missing_ips, MIN(client_ip) AS first_ip").
		Where("registered_unix > ? AND registered_unix <= ?", start.Unix(), now.Unix()).
		Group("browser_hash").Having("COUNT(*) >= ?", threshold).Order("browser_hash").Scan(&groups).Error
	if err != nil {
		return nil, err
	}
	out := make([]riskWarningCandidate, 0, len(groups))
	for _, g := range groups {
		subject := "browser:" + g.BrowserHash
		ip, attribution := "", "unknown"
		if g.KnownIPs > 1 {
			attribution = "multiple"
		} else if g.KnownIPs == 1 && g.MissingIPs == 0 {
			if ip = attributableRiskSourceIP(g.FirstIP); ip != "" {
				attribution = "single"
			}
		}
		detail := map[string]any{
			"ruleId":        rule.RuleID,
			"dimension":     "registration_browser",
			"ipAttribution": attribution,
			"threshold":     threshold,
			"value":         g.Accounts,
			"accountCount":  g.Accounts,
			"windowKind":    "rolling",
			"windowMinutes": int(registrationBrowserWindow / time.Minute),
			"windowStart":   start.Format(time.RFC3339),
			"windowEnd":     now.Format(time.RFC3339),
			"evidenceNote":  "同一浏览器回传本站签发的注册标识，不代表同一物理设备或同一人。仅当窗口内所有注册均可归因于同一来源 IP 时记录 IP。",
		}
		if ip != "" {
			detail["ip"] = ip
		}
		b, err := json.Marshal(detail)
		if err != nil {
			return nil, err
		}
		out = append(out, riskWarningCandidate{Rule: rule, Subject: subject, Severity: rule.Severity, Detail: string(b), IP: ip})
	}
	return out, nil
}
