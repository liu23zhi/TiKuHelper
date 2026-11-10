package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const ScriptDomainSettingsKey = "script_domain_settings"

type ScriptDomainSettings struct {
	// Domains is empty for backwards-compatible allow-all behavior. Once an
	// administrator configures it, script creation is limited to these roots.
	Domains       []string `json:"domains"`
	AllowWildcard bool     `json:"allowWildcard"`
}

var (
	ErrScriptDomainUnavailable = errors.New("脚本域名不在管理员允许的列表中")
	ErrScriptWildcardDisabled  = errors.New("管理员未启用泛域名脚本")
)

func GetScriptDomainSettingsWithDB(db *gorm.DB) (ScriptDomainSettings, error) {
	settings := ScriptDomainSettings{AllowWildcard: true}
	if db == nil {
		return ScriptDomainSettings{}, errors.New("数据库未初始化")
	}
	var row entity.SystemSetting
	err := db.Session(&gorm.Session{NewDB: true}).Where(&entity.SystemSetting{Key: ScriptDomainSettingsKey}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return settings, nil
	}
	if err != nil {
		return ScriptDomainSettings{}, err
	}
	raw := strings.TrimSpace(row.Value)
	if raw == "" {
		return settings, nil
	}
	var stored struct {
		Domains       []string `json:"domains"`
		AllowWildcard *bool    `json:"allowWildcard"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return ScriptDomainSettings{}, err
	}
	settings.Domains = stored.Domains
	// Older settings may omit allowWildcard; preserve the backwards-compatible
	// default instead of interpreting an omitted JSON bool as false.
	if stored.AllowWildcard != nil {
		settings.AllowWildcard = *stored.AllowWildcard
	}
	settings.Domains = normalizeScriptDomainList(settings.Domains)
	return settings, nil
}

func UpdateScriptDomainSettingsWithDB(db *gorm.DB, input ScriptDomainSettings) (ScriptDomainSettings, error) {
	if db == nil {
		return ScriptDomainSettings{}, errors.New("数据库未初始化")
	}
	// 逐条校验后再归一化：非法条目（`a.*.a.com`、`x.com/adapter-service/*` …）
	// 必须当场报错，而不是被 normalizeScriptDomainList 静默丢掉。
	for _, value := range input.Domains {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := validateScriptDomainEntry(value); err != nil {
			return ScriptDomainSettings{}, err
		}
	}
	input.Domains = normalizeScriptDomainList(input.Domains)
	encoded, err := json.Marshal(input)
	if err != nil {
		return ScriptDomainSettings{}, err
	}
	if err := saveSystemSettingValue(db, ScriptDomainSettingsKey, string(encoded), "脚本可用域名与泛域名开关"); err != nil {
		return ScriptDomainSettings{}, err
	}
	return GetScriptDomainSettingsWithDB(db)
}

// ValidateScriptDomainWithDB 校验「脚本自身」的 base domain 是否被管理员允许。
//
// 这里的 `wildcard` 是 `UseWildcardDomain`（短码做子域）开关，与域名里的 `*`
// 是两个不同的概念：域名 `*` 由 ScriptDomainSettings.Domains 的条目形态表达。
func ValidateScriptDomainWithDB(db *gorm.DB, domain string, wildcard bool) error {
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		return err
	}
	if wildcard && !settings.AllowWildcard {
		return ErrScriptWildcardDisabled
	}
	normalized := normalizeScriptDomain(domain)
	if normalized == "" {
		return ErrScriptDomainUnavailable
	}
	// Q72-3：域名里的 `*` 只允许最左。中间形态在这里当场拒绝，不留到匹配阶段。
	if err := validateScriptDomainWildcardPlacement(normalized); err != nil {
		return err
	}
	if len(settings.Domains) == 0 {
		return nil
	}
	// 通配匹配：`*.a.zelly.cn` 允许 base domain `x.a.zelly.cn`，
	// 但**不允许** `a.zelly.cn`（点前必须有段），也不允许 `evila.zelly.cn`。
	for _, entry := range settings.Domains {
		rule, err := compileScriptDomainRule(entry)
		if err != nil {
			continue
		}
		if scriptDomainHostMatch(rule.HostPattern, normalized) {
			return nil
		}
	}
	return ErrScriptDomainUnavailable
}

// normalizeScriptDomainEntry 归一化「一条配置项」：可以只写域名，也可以带路径段
// （`c.zelly.cn/a/*`）。返回空串表示该条目不合法，由调用方决定跳过还是报错。
func normalizeScriptDomainEntry(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	rule, err := compileScriptDomainRule(trimmed)
	if err != nil {
		return ""
	}
	return rule.String()
}

// validateScriptDomainEntry 用于**保存配置时**的校验：非法条目必须当场报错，
// 否则管理员会以为规则生效了，实际在读取阶段被静默跳过。
func validateScriptDomainEntry(value string) error {
	rule, err := compileScriptDomainRule(value)
	if err != nil {
		return err
	}
	// 保留前缀必须在**配置创建时**拒绝（45-*.md §0.25.2 要点 3）：
	// 运行时再判定的话，管理员保存成功、上线才发现路径被系统入口吞掉。
	if scriptDomainRuleConflictsReservedPrefix(rule) {
		return ErrScriptReservedPrefix
	}
	return nil
}

func normalizeScriptDomainList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeScriptDomainEntry(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}
