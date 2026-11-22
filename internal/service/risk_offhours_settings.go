package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RiskOffhoursWindow is the effective half-open interval in Beijing time.
// Configured is true only when both stored bounds form a valid pair.
type RiskOffhoursWindow struct {
	StartHour        int  `json:"startHour"`
	EndHour          int  `json:"endHour"`
	DefaultStartHour int  `json:"defaultStartHour"`
	DefaultEndHour   int  `json:"defaultEndHour"`
	Configured       bool `json:"configured"`
}

func riskOffhoursSettingKeys() clause.IN {
	return clause.IN{Column: clause.Column{Name: "key"}, Values: []any{riskOffhoursStartSettingKey, riskOffhoursEndSettingKey}}
}

func validRiskOffhoursPair(start, end int) bool {
	return start >= 0 && start < 24 && end >= 0 && end < 24 && start != end
}

// Read both rows in one statement: two independent SELECTs could combine
// the start from an old window with the end from a concurrently committed one.
func GetRiskOffhoursWindow(db *gorm.DB) (RiskOffhoursWindow, error) {
	w := RiskOffhoursWindow{StartHour: riskOffhoursDefaultStartHour, EndHour: riskOffhoursDefaultEndHour,
		DefaultStartHour: riskOffhoursDefaultStartHour, DefaultEndHour: riskOffhoursDefaultEndHour}
	if db == nil {
		return w, fmt.Errorf("数据库未初始化")
	}
	var rows []entity.SystemSetting
	if err := db.Session(&gorm.Session{NewDB: true}).Select("key", "value").Where(riskOffhoursSettingKeys()).Find(&rows).Error; err != nil {
		return w, err
	}
	start, end := -1, -1
	for _, row := range rows {
		hour, err := strconv.Atoi(strings.TrimSpace(row.Value))
		if err != nil {
			return w, nil
		}
		switch row.Key {
		case riskOffhoursStartSettingKey:
			start = hour
		case riskOffhoursEndSettingKey:
			end = hour
		}
	}
	if validRiskOffhoursPair(start, end) {
		w.StartHour, w.EndHour, w.Configured = start, end, true
	}
	return w, nil
}

func UpdateRiskOffhoursWindow(db *gorm.DB, start, end int, actorID int32) (RiskOffhoursWindow, error) {
	if !validRiskOffhoursPair(start, end) {
		return RiskOffhoursWindow{}, fmt.Errorf("%w：起止小时必须在 0–23 之间且不能相同，可以跨零点", ErrRiskConfigInvalid)
	}
	return writeRiskOffhoursWindow(db, &RiskOffhoursWindow{StartHour: start, EndHour: end}, actorID)
}

func ResetRiskOffhoursWindow(db *gorm.DB, actorID int32) (RiskOffhoursWindow, error) {
	return writeRiskOffhoursWindow(db, nil, actorID)
}

func writeRiskOffhoursWindow(db *gorm.DB, pair *RiskOffhoursWindow, actorID int32) (RiskOffhoursWindow, error) {
	if db == nil {
		return RiskOffhoursWindow{}, fmt.Errorf("数据库未初始化")
	}
	var result RiskOffhoursWindow
	err := db.Transaction(func(tx *gorm.DB) error {
		rules, err := loadRiskRules(tx)
		if err != nil {
			return err
		}
		var affected []entity.RiskRule
		for _, rule := range rules {
			if effectiveRiskRuleType(rule) == entity.RiskRuleIDOffhoursActivity {
				affected = append(affected, rule)
			}
		}
		if len(affected) == 0 {
			return fmt.Errorf("%w：%s", ErrRiskRuleDeleted, entity.RiskRuleIDOffhoursActivity)
		}
		action := "config_reset"
		if pair == nil {
			if err := tx.Where(riskOffhoursSettingKeys()).Delete(&entity.SystemSetting{}).Error; err != nil {
				return err
			}
		} else {
			action = "config"
			rows := []entity.SystemSetting{
				{Key: riskOffhoursStartSettingKey, Value: strconv.Itoa(pair.StartHour), Remark: "风控配置：凌晨时段起点"},
				{Key: riskOffhoursEndSettingKey, Value: strconv.Itoa(pair.EndHour), Remark: "风控配置：凌晨时段终点"},
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).Create(&rows).Error; err != nil {
				return err
			}
		}
		for _, rule := range affected {
			if err := auditRiskInstanceSettingChange(tx, rule, actorID, action); err != nil {
				return err
			}
		}
		result, err = GetRiskOffhoursWindow(tx)
		return err
	})
	return result, err
}
