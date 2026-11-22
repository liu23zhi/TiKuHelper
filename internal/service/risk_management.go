package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRiskRuleInvalid = errors.New("风控规则参数无效")
	ErrRiskRuleDeleted = errors.New("风控规则已删除")
)

// RiskRuleInput is the complete editable rule payload. Type and RuleID are immutable after creation.
type RiskRuleInput struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Type            string          `json:"type"`
	Config          json.RawMessage `json:"config"`
	Severity        string          `json:"severity"`
	Enabled         bool            `json:"enabled"`
	Threshold       float64         `json:"threshold"`
	ThrottleSeconds int             `json:"throttleSeconds"`
}

// RiskRuleRecord is the admin API shape, including both detector and audit metadata.
type RiskRuleRecord struct {
	RuleID          string          `json:"ruleId"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Type            string          `json:"type"`
	Config          json.RawMessage `json:"config"`
	Severity        string          `json:"severity"`
	Enabled         bool            `json:"enabled"`
	Source          string          `json:"source"`
	Threshold       float64         `json:"threshold"`
	ThrottleSeconds int             `json:"throttleSeconds"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	CreatedBy       int32           `json:"createdBy"`
	UpdatedBy       int32           `json:"updatedBy"`
	DeletedAt       *time.Time      `json:"deletedAt,omitempty"`
	Exists          bool            `json:"exists"`
	Unit            string          `json:"unit"`
	HasThreshold    bool            `json:"hasThreshold"`
}

// RiskRuleType describes a server-side detector available for rule instances.
type RiskRuleType struct {
	Type                   string         `json:"type"`
	Name                   string         `json:"name"`
	Description            string         `json:"description"`
	DefaultThreshold       float64        `json:"defaultThreshold"`
	ThresholdUnit          string         `json:"thresholdUnit"`
	HasThreshold           bool           `json:"hasThreshold"`
	ThresholdKind          string         `json:"thresholdKind,omitempty"`
	ThresholdMax           float64        `json:"thresholdMax,omitempty"`
	DefaultSeverity        string         `json:"defaultSeverity"`
	DefaultThrottleSeconds int            `json:"defaultThrottleSeconds"`
	ConfigSchema           map[string]any `json:"configSchema"`
}

func RiskRuleTypes() []RiskRuleType {
	defaults := entity.DefaultRiskRules()
	items := make([]RiskRuleType, 0, len(defaults))
	for _, item := range defaults {
		spec, hasThreshold := riskPrimaryThresholdSpec(item.RuleID)
		items = append(items, RiskRuleType{
			Type: item.RuleID, Name: item.Name, Description: item.Description,
			DefaultThreshold: item.Threshold, ThresholdUnit: spec.Unit, HasThreshold: hasThreshold,
			ThresholdKind: spec.Kind, ThresholdMax: spec.MaxValue, DefaultSeverity: item.Severity, DefaultThrottleSeconds: item.ThrottleSeconds,
			ConfigSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		})
	}
	return items
}

func ListRiskRules(db *gorm.DB) ([]RiskRuleRecord, error) {
	rules, err := loadRiskRules(db)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	if db != nil && db.Migrator().HasTable(entity.TableNameRiskRule) {
		var ids []string
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).Where("deleted_at IS NULL").Pluck("rule_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			existing[id] = true
		}
	}
	items := make([]RiskRuleRecord, 0, len(rules))
	for _, rule := range rules {
		items = append(items, riskRuleRecord(db, rule, existing[rule.RuleID]))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Source != items[j].Source {
			return items[i].Source == "builtin"
		}
		return items[i].RuleID < items[j].RuleID
	})
	return items, nil
}

func CreateRiskRule(db *gorm.DB, input RiskRuleInput, actorID int32) (RiskRuleRecord, error) {
	if db == nil {
		return RiskRuleRecord{}, fmt.Errorf("数据库未初始化")
	}
	typeDefault, ok := riskRuleDefaultByID(strings.TrimSpace(input.Type))
	if !ok {
		return RiskRuleRecord{}, fmt.Errorf("%w：不支持的规则类型", ErrRiskRuleInvalid)
	}
	config, err := prepareRiskRuleInput(&input, typeDefault)
	if err != nil {
		return RiskRuleRecord{}, err
	}
	ruleID := "custom_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	row := entity.RiskRule{RuleID: ruleID, Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description),
		Type: typeDefault.RuleID, ConfigJSON: config, Severity: strings.TrimSpace(input.Severity), Enabled: input.Enabled,
		Threshold: input.Threshold, ThrottleSeconds: input.ThrottleSeconds, Source: "custom", CreatedBy: actorID, UpdatedBy: actorID}
	err = db.Transaction(func(tx *gorm.DB) error {
		// GORM can replace an explicitly false bool with the column default on INSERT.
		row.Enabled = true
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if !input.Enabled {
			if err := tx.Model(&entity.RiskRule{}).Where("rule_id = ?", ruleID).UpdateColumn("enabled", false).Error; err != nil {
				return err
			}
			row.Enabled = false
		}
		if err := persistRiskRuleThreshold(tx, row); err != nil {
			return err
		}
		return writeRiskRuleAudit(tx, row, "create", actorID)
	})
	if err != nil {
		return RiskRuleRecord{}, err
	}
	return riskRuleRecord(db, row, true), nil
}

func UpdateRiskRule(db *gorm.DB, ruleID string, input RiskRuleInput, actorID int32) (RiskRuleRecord, error) {
	if db == nil {
		return RiskRuleRecord{}, fmt.Errorf("数据库未初始化")
	}
	if strings.TrimSpace(ruleID) == "" {
		return RiskRuleRecord{}, ErrRiskRuleUnknown
	}
	var updated entity.RiskRule
	err := db.Transaction(func(tx *gorm.DB) error {
		var row entity.RiskRule
		if err := tx.Where("rule_id = ? AND deleted_at IS NULL", ruleID).First(&row).Error; err != nil {
			return err
		}
		if strings.TrimSpace(input.Type) != "" && input.Type != row.Type {
			return fmt.Errorf("%w：规则类型创建后不可更改", ErrRiskRuleInvalid)
		}
		typeDefault, ok := riskRuleDefaultByID(row.Type)
		if !ok {
			return fmt.Errorf("%w：规则检测器不存在", ErrRiskRuleInvalid)
		}
		config, err := prepareRiskRuleInput(&input, typeDefault)
		if err != nil {
			return err
		}
		row.Name, row.Description, row.ConfigJSON = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), config
		row.Severity, row.Threshold, row.ThrottleSeconds = strings.TrimSpace(input.Severity), input.Threshold, input.ThrottleSeconds
		row.UpdatedBy = actorID
		if err := tx.Model(&entity.RiskRule{}).Where("rule_id = ?", ruleID).Updates(map[string]any{
			"name": row.Name, "description": row.Description, "config_json": row.ConfigJSON,
			"severity": row.Severity, "threshold": row.Threshold, "throttle_seconds": row.ThrottleSeconds,
			"updated_by": actorID, "updated_at": time.Now(),
		}).Error; err != nil {
			return err
		}
		if err := persistRiskRuleThreshold(tx, row); err != nil {
			return err
		}
		if err := tx.Where("rule_id = ?", ruleID).First(&updated).Error; err != nil {
			return err
		}
		return writeRiskRuleAudit(tx, updated, "update", actorID)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RiskRuleRecord{}, fmt.Errorf("%w：%s", ErrRiskRuleUnknown, ruleID)
		}
		return RiskRuleRecord{}, err
	}
	return riskRuleRecord(db, updated, true), nil
}

func SetRiskRuleEnabledManaged(db *gorm.DB, ruleID string, enabled bool, actorID int32) (RiskRuleRecord, error) {
	if db == nil {
		return RiskRuleRecord{}, fmt.Errorf("数据库未初始化")
	}
	var row entity.RiskRule
	err := db.Transaction(func(tx *gorm.DB) error {
		findErr := tx.Unscoped().Where("rule_id = ?", ruleID).First(&row).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			defaultRule, ok := riskRuleDefaultByID(ruleID)
			if !ok {
				return fmt.Errorf("%w：%s", ErrRiskRuleUnknown, ruleID)
			}
			row = entity.RiskRule{RuleID: ruleID, Name: defaultRule.Name, Description: defaultRule.Description,
				Type: ruleID, ConfigJSON: `{}`, Severity: defaultRule.Severity, Enabled: true, Threshold: defaultRule.Threshold,
				ThrottleSeconds: defaultRule.ThrottleSeconds, Source: "builtin"}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else if findErr != nil {
			return findErr
		}
		if row.DeletedAt != nil {
			return fmt.Errorf("%w：%s", ErrRiskRuleDeleted, ruleID)
		}
		if err := tx.Model(&entity.RiskRule{}).Where("rule_id = ?", ruleID).Updates(map[string]any{"enabled": enabled, "updated_by": actorID, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		row.Enabled, row.UpdatedBy, row.UpdatedAt = enabled, actorID, time.Now()
		if err := tx.Where("rule_id = ?", ruleID).First(&row).Error; err != nil {
			return err
		}
		return writeRiskRuleAudit(tx, row, "enable", actorID)
	})
	if err != nil {
		return RiskRuleRecord{}, err
	}
	return riskRuleRecord(db, row, true), nil
}

func DeleteRiskRule(db *gorm.DB, ruleID string, actorID int32) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var row entity.RiskRule
		if err := tx.Where("rule_id = ? AND deleted_at IS NULL", ruleID).First(&row).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&entity.RiskRule{}).Where("rule_id = ?", ruleID).Updates(map[string]any{"deleted_at": now, "enabled": false, "updated_by": actorID, "updated_at": now}).Error; err != nil {
			return err
		}
		row.DeletedAt, row.Enabled, row.UpdatedBy, row.UpdatedAt = &now, false, actorID, now
		return writeRiskRuleAudit(tx, row, "delete", actorID)
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w：%s", ErrRiskRuleUnknown, ruleID)
	}
	return err
}

func prepareRiskRuleInput(input *RiskRuleInput, typeDefault entity.RiskRuleDefault) (string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || len([]rune(input.Name)) > 128 || len([]rune(input.Description)) > 2048 {
		return "", fmt.Errorf("%w：名称必填且不超过 128 字，说明不超过 2048 字", ErrRiskRuleInvalid)
	}
	switch input.Severity {
	case entity.RiskSeverityLow, entity.RiskSeverityMedium, entity.RiskSeverityHigh:
	default:
		return "", fmt.Errorf("%w：severity 只能是 low、medium 或 high", ErrRiskRuleInvalid)
	}
	if typeDefault.RuleID == entity.RiskRuleIDProxyIPLogin {
		// Old clients send a number. The detector uses only its ASN list; never
		// interpret that number as a criterion, even when it differs from 1.
		input.Threshold = typeDefault.Threshold
	} else {
		spec, ok := riskPrimaryThresholdSpec(typeDefault.RuleID)
		if !ok {
			return "", fmt.Errorf("%w：检测器缺少主阈值定义", ErrRiskRuleInvalid)
		}
		if err := ValidateRiskThresholdValueForKey(spec.Key, spec.Kind, input.Threshold); err != nil {
			return "", fmt.Errorf("%w：%v", ErrRiskRuleInvalid, err)
		}
	}
	if input.ThrottleSeconds < 0 || input.ThrottleSeconds > 31*24*60*60 {
		return "", fmt.Errorf("%w：throttleSeconds 必须在 0 到 2678400 之间", ErrRiskRuleInvalid)
	}
	config := input.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(config, &object); err != nil || object == nil || len(object) != 0 {
		return "", fmt.Errorf("%w：该规则类型的 config 只能是空对象", ErrRiskRuleInvalid)
	}
	return `{}`, nil
}

func persistRiskRuleThreshold(tx *gorm.DB, row entity.RiskRule) error {
	key := riskRuleThresholdSettingKey(row.RuleID)
	if effectiveRiskRuleType(row) == entity.RiskRuleIDProxyIPLogin {
		// Older releases wrote this unused key. Drop it on save rather than
		// perpetuate a fictitious numeric configuration.
		return tx.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).Delete(&entity.SystemSetting{}).Error
	}
	return saveSystemSettingValue(tx, key, formatRiskThresholdValue(row.Threshold), "风控规则阈值："+row.Name)
}

func riskRuleRecord(db *gorm.DB, row entity.RiskRule, exists bool) RiskRuleRecord {
	spec, hasThreshold := riskPrimaryThresholdSpec(effectiveRiskRuleType(row))
	config := json.RawMessage(row.ConfigJSON)
	if len(config) == 0 || !json.Valid(config) {
		config = json.RawMessage(`{}`)
	}
	return RiskRuleRecord{RuleID: row.RuleID, Name: row.Name, Description: row.Description, Type: row.Type,
		Config: config, Severity: row.Severity, Enabled: row.Enabled, Source: row.Source,
		Threshold: riskRuleThreshold(db, row, riskDefaultThreshold(row.RuleID)), ThrottleSeconds: row.ThrottleSeconds,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		DeletedAt: row.DeletedAt, Exists: exists, Unit: spec.Unit, HasThreshold: hasThreshold}
}

func writeRiskRuleAudit(tx *gorm.DB, row entity.RiskRule, action string, actorID int32) error {
	record := riskRuleRecord(tx, row, true)
	settings := map[string]string{}
	for _, spec := range riskThresholdSpecs() {
		if spec.RuleID == effectiveRiskRuleType(row) {
			key := spec.Key
			if spec.SyncRuleColumn {
				key = riskRuleThresholdSettingKey(row.RuleID)
			}
			settings[key] = getSystemSettingStringWithDefault(tx, key, formatRiskThresholdValue(spec.DefaultValue))
		}
	}
	for _, spec := range riskConfigSpecs() {
		if spec.RuleID == effectiveRiskRuleType(row) {
			settings[spec.Key] = getSystemSettingStringWithDefault(tx, spec.Key, spec.DefaultValue)
		}
	}
	snapshot, err := json.Marshal(struct {
		RiskRuleRecord
		Settings map[string]string `json:"settings"`
	}{RiskRuleRecord: record, Settings: settings})
	if err != nil {
		return err
	}
	return tx.Create(&entity.RiskRuleAudit{RuleID: row.RuleID, Action: action, ActorID: actorID, SnapshotJSON: string(snapshot)}).Error
}

// Global auxiliary detector settings also affect custom instances. Update every
// active instance's actor/time and append its effective configuration snapshot.
func auditRiskDetectorSettingChange(tx *gorm.DB, detectorType string, actorID int32, action string) error {
	if detectorType == "" || !tx.Migrator().HasTable(entity.TableNameRiskRule) {
		return nil
	}
	var rows []entity.RiskRule
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("(rule_type = ? OR rule_id = ?) AND deleted_at IS NULL", detectorType, detectorType).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := auditRiskInstanceSettingChange(tx, row, actorID, action); err != nil {
			return err
		}
	}
	return nil
}

func auditRiskInstanceSettingChange(tx *gorm.DB, row entity.RiskRule, actorID int32, action string) error {
	row.UpdatedBy, row.UpdatedAt = actorID, time.Now()
	if row.ID != 0 {
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).Where("rule_id = ?", row.RuleID).
			Updates(map[string]any{"updated_by": row.UpdatedBy, "updated_at": row.UpdatedAt}).Error; err != nil {
			return err
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("rule_id = ?", row.RuleID).First(&row).Error; err != nil {
			return err
		}
	}
	// A missing built-in row can still be active via loadRiskRules. Record the
	// effective configuration change without creating a synthetic rule row.
	return writeRiskRuleAudit(tx, row, action, actorID)
}

func riskSettingVisibility(db *gorm.DB) (map[string]bool, error) {
	rules, err := loadRiskRules(db)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{"": true}
	for _, rule := range rules {
		visible[rule.RuleID] = true
		visible["type:"+effectiveRiskRuleType(rule)] = true
	}
	return visible, nil
}

func riskRuleSettingActive(visible map[string]bool, ruleID string, shared bool) bool {
	return visible[ruleID] || (shared && visible["type:"+ruleID])
}

func requireRiskRuleSettingActive(db *gorm.DB, ruleID string, shared bool) error {
	visible, err := riskSettingVisibility(db)
	if err != nil {
		return err
	}
	if !riskRuleSettingActive(visible, ruleID, shared) {
		return fmt.Errorf("%w：%s", ErrRiskRuleDeleted, ruleID)
	}
	return nil
}
