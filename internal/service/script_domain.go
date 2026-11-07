package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

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
		return settings, nil
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, ScriptDomainSettingsKey, ""))
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

func ValidateScriptDomainWithDB(db *gorm.DB, domain string, wildcard bool) error {
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		return err
	}
	if wildcard && !settings.AllowWildcard {
		return ErrScriptWildcardDisabled
	}
	if len(settings.Domains) == 0 {
		return nil
	}
	normalized := normalizeScriptDomain(domain)
	for _, allowed := range settings.Domains {
		if strings.EqualFold(normalized, allowed) {
			return nil
		}
	}
	return ErrScriptDomainUnavailable
}

func normalizeScriptDomainList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeScriptDomain(value)
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
