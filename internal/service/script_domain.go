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

// ScriptDomainSettings 是管理员配置的**域名格式列表**（2026-11-14 语法重写）。
//
// 与上一版的差别（破坏性，不做兼容）：
//   - 去掉了 AllowWildcard：泛域名能否使用不再是一个独立开关，而是由「列表里有没有
//     `*.a.com` 这一条」直接表达。两个开关表达同一件事必然出现互相矛盾的状态
//     （列表里有 `*.a.com` 但开关关着），那种状态在 UI 上无法解释。
//   - Domains 里的每一条都必须是新语法（`*.a.com` 或 `a.com/*`），旧格式显式报错。
//
// 列表为空 = 不限制（保持既有语义：没配置过就不阻塞脚本创建）。
type ScriptDomainSettings struct {
	Domains []string `json:"domains"`
}

var (
	ErrScriptDomainUnavailable = errors.New("脚本域名不在管理员允许的列表中")
)

// GetScriptDomainSettingsWithDB 读取配置。
//
// ⚠️ 旧格式在这里**不报错**，只是读不出来 —— 真正的拦截点是写入与脚本创建。
// 原因：GET 是纯读接口，若它因为存量脏数据返回 500，管理员连
// 「看到问题、改掉它」的入口都没有。
func GetScriptDomainSettingsWithDB(db *gorm.DB) (ScriptDomainSettings, error) {
	settings := ScriptDomainSettings{Domains: []string{}}
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
		Domains []string `json:"domains"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return ScriptDomainSettings{}, err
	}
	settings.Domains = normalizeScriptDomainList(stored.Domains)
	return settings, nil
}

// UpdateScriptDomainSettingsWithDB 保存配置。
//
// 逐条硬校验：任何一条是旧格式或不合法，整批拒绝并返回**指向新语法的**错误。
// 不做「跳过坏条目继续保存」——那会让管理员以为规则生效了，实际少了一条。
func UpdateScriptDomainSettingsWithDB(db *gorm.DB, input ScriptDomainSettings) (ScriptDomainSettings, error) {
	if db == nil {
		return ScriptDomainSettings{}, errors.New("数据库未初始化")
	}
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
	if err := saveSystemSettingValue(db, ScriptDomainSettingsKey, string(encoded), "脚本可用域名格式列表"); err != nil {
		return ScriptDomainSettings{}, err
	}
	return GetScriptDomainSettingsWithDB(db)
}

// ScriptDomainFormatsWithDB 把配置解析成结构化模板，供控制器直接返回给前端。
func ScriptDomainFormatsWithDB(db *gorm.DB) ([]ScriptDomainFormat, error) {
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		return nil, err
	}
	formats := make([]ScriptDomainFormat, 0, len(settings.Domains))
	for _, entry := range settings.Domains {
		format, ok, err := parseScriptDomainFormat(entry)
		if err != nil || !ok {
			// 存量脏数据（旧语法）读不出来就跳过：GET 不该因为脏数据 500，
			// 详见 GetScriptDomainSettingsWithDB 的注释。
			continue
		}
		formats = append(formats, format)
	}
	return formats, nil
}

// validateScriptDomainEntry 保存配置时的逐条校验。
func validateScriptDomainEntry(value string) error {
	_, ok, err := parseScriptDomainFormat(value)
	if err != nil {
		return err
	}
	if !ok {
		return ErrScriptDomainFormatInvalid
	}
	return nil
}

func normalizeScriptDomainList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		format, ok, err := parseScriptDomainFormat(value)
		if err != nil || !ok {
			// 非法条目在写入路径已被拒绝；这里是读路径，跳过以保持列表可用。
			continue
		}
		normalized := format.String()
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

// ValidateScriptDomainWithDB 校验**脚本自身的具体域名**是否被某条格式覆盖。
//
// 语义（新语法下重新定义）：
//   - 列表为空 → 放行（保持「没配置就不限制」的既有行为）；
//   - `*.a.com` 覆盖 `x.a.com`（任意深度子域），**不覆盖** `a.com` 本身；
//   - `a.com/*` 覆盖主机恰好是 `a.com` 的脚本（路径部分由脚本绑定决定，
//     不在这里判定 —— 创建脚本时还没分配具体路径）。
func ValidateScriptDomainWithDB(db *gorm.DB, domain string) error {
	formats, err := ScriptDomainFormatsWithDB(db)
	if err != nil {
		return err
	}
	if len(formats) == 0 {
		return nil
	}
	host := normalizeScriptDomain(domain)
	if host == "" {
		return ErrScriptDomainUnavailable
	}
	for _, format := range formats {
		// 复用 scriptDomainWildcardCovers / scriptDomainPathCovers —— 泛域名的 Q2 边界
		// 只在一个地方定义，避免创建期校验与运行期解析各写一份而漂移。
		if scriptDomainWildcardCovers(format, host) || scriptDomainPathCovers(format, host) {
			return nil
		}
	}
	return ErrScriptDomainUnavailable
}

// defaultScriptUseWildcardWithDB 根据**管理员配置的域名格式**推出脚本公开地址的形态。
//
// 返回 true 表示脚本住在 `<shortCode>.<baseDomain>` 独立主机上（根路径即入口）；
// false 表示脚本住在共享主机 baseDomain 下、靠 `/<shortCode>` 路径段区分。
//
// 判据优先级：`*.a.com` 覆盖到的 baseDomain 走泛域名；否则若 baseDomain 恰好是
// 某条 `a.com/*` 的固定主机，则必须走路径形态 —— 否则它的公开地址是根路径 "",
// 而路径规则要求至少一段，脚本将永远解析不到。
//
// 没有任何配置（空列表）时保持历史默认 true（泛域名短址）。
func defaultScriptUseWildcardWithDB(db *gorm.DB, baseDomain string) bool {
	formats, err := ScriptDomainFormatsWithDB(db)
	if err != nil || len(formats) == 0 {
		return true
	}
	host := normalizeScriptDomain(baseDomain)
	if host == "" {
		return true
	}
	coveredByPath := false
	for _, format := range formats {
		// 同样复用共享判据：泛域名覆盖 ⇒ 独立主机形态；路径格式覆盖 ⇒ 共享主机形态。
		if scriptDomainWildcardCovers(format, host) {
			return true
		}
		if scriptDomainPathCovers(format, host) {
			coveredByPath = true
		}
	}
	if coveredByPath {
		return false
	}
	return true
}
