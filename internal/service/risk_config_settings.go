package service

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 风控的**非数值**可调配置的读写路径。
//
// # 为什么需要这个文件（与 risk_threshold.go 的分工）
//
// `risk_threshold.go` 管的是「每条规则一个 float64 阈值」：`riskThresholdSpecs()` 里
// 每一项都是 `float64` + 上下界校验，前端用 a-input-number 输入。
//
// 但风控判定的输入里还有两类**不是数值**、却同样必须由运营配置的项，
// 它们**进不了**那张表，而在此之前也**都没有任何写入路径**：
//
//	risk_proxy_asn_list              R10「代理IP登录」的 ASN 名单（字符串列表）
//	risk_evaluate_interval_minutes   判定任务的评估间隔（整数分钟）
//
// 两者的共同后果是同一种失效形态 —— **规则静默不工作，且没有任何报错**：
//
//   - `risk_proxy_asn_list` 为空时 `collectProxyIPLoginCandidates` 直接 return，
//     连查询都不做（这是刻意的「宁可漏报不要误报」）。而它没有任何写接口
//     ⇒ R10 **永远**不会告警。更糟的是 `ValidateProxyASNList` 当时**零生产调用方**
//     （只有 _test.go 调它），也就是说「保存时严格校验」这条设计根本没接线。
//   - `risk_evaluate_interval_minutes` 只被 `resolveRiskEvaluateInterval` 读。
//     运营想调整评估频率只能直接改数据库；填错的边界（>1440）只会打一条 SysWarn。
//
// # 为什么不做成 /risk/thresholds 的第二种类型
//
// `UpdateRiskThresholdWithDB(db, key, value float64)` 的签名是数值，
// `updateRiskThresholdRequest` 是 `Value *float64`。把字符串塞进去要改这条
// 已上线接口的语义（`"AS15169"` 会被 JSON 解析成 400 而不是「非法值」），
// 而它已经有 15 个调用场景在用。所以另开一组 `/risk/config[/:key]`：
// **数值归阈值，文本归配置**，两条路径各自有自己的一套校验，不互相污染。

// 非数值配置的取值类型 —— 决定校验规则，不是装饰字段。
const (
	// RiskConfigKindASNList 是 ASN 名单（逗号/分号/换行/空格分隔，形如 AS15169）。
	//
	// ⚠️ 空串是**合法**值：它表示「关闭 R10」（名单为空即不告警）。
	// 这与数值阈值的「必须 > 0」完全相反，所以不能用同一套校验。
	RiskConfigKindASNList = "asn_list"
	// RiskConfigKindPositiveInt 是「1..1440 的整数」（评估间隔，单位分钟）。
	RiskConfigKindPositiveInt = "positive_int"
)

var (
	// ErrRiskConfigInvalid 覆盖全部取值非法（ASN 坏项、间隔越界/非整数）。
	ErrRiskConfigInvalid = errors.New("风控配置取值非法")
	// ErrRiskConfigUnknown 表示请求的键不在代码登记的白名单里。
	ErrRiskConfigUnknown = errors.New("未知的风控配置键")
)

// RiskConfigItem 是一项非数值配置的当前生效值（读接口返回元素，也是写接口的白名单元素）。
type RiskConfigItem struct {
	// Key 是 system_settings 的键，同时是写接口的路径参数。
	Key string `json:"key"`
	// RuleID 是对应的规则 id（评估间隔这类全局项留空）。
	RuleID string `json:"ruleId"`
	// Label 是给运营看的中文名。
	Label string `json:"label"`
	// Kind 见 RiskConfigKind*。前端据此选输入控件（文本域 vs 数字框）。
	Kind string `json:"kind"`
	// Value 是**当前生效值**（原样字符串：名单保留归一化后的 AS 形式，间隔是十进制串）。
	Value string `json:"value"`
	// DefaultValue 是代码默认值，用于「重置为默认」与界面提示。
	DefaultValue string `json:"defaultValue"`
	// Source 说明当前值来自哪一层：system_settings / code。
	Source string `json:"source"`
	// Configured 表示是否存在显式配置（system_settings 有非空行）。
	Configured bool `json:"configured"`
	// Unit 是展示单位（如「分钟」；名单类为空）。
	Unit string `json:"unit"`
	// Help 是给运营看的说明，**必须含「空值意味着什么」**（见各项注释）。
	Help string `json:"help"`
	// MinValue / MaxValue 是正整数类的合法区间（ASN 名单类为 0，表示不适用）。
	MinValue float64 `json:"minValue"`
	MaxValue float64 `json:"maxValue"`
}

// 配置来源标记（与 RiskThresholdSource* 同构，复用同一组常量语义）。
const (
	RiskConfigSourceSetting = RiskThresholdSourceSetting
	RiskConfigSourceCode    = RiskThresholdSourceCode
)

// riskConfigSpec 是代码内的非数值配置登记表 —— 唯一真相（白名单来源）。
type riskConfigSpec struct {
	Key          string
	RuleID       string
	Label        string
	Kind         string
	DefaultValue string
	Unit         string
	Help         string
	MinValue     float64
	MaxValue     float64
}

// riskConfigSpecs 返回全部可调的非数值配置。
//
// 默认值**一律引用既有常量**而不是写字面量：这两项的默认值分别由
// `riskProxyASNDefault` 与 `DefaultRiskEvaluateIntervalMinutes` 定义，
// 写字面量就是「第二份真相」，判定链改常量后界面会显示一个已经不对的默认值。
// （数值阈值那张表相反 —— 那里刻意写字面量，理由见 riskThresholdSpecs 的注释。）
func riskConfigSpecs() []riskConfigSpec {
	return []riskConfigSpec{
		{
			Key:          RiskThresholdSettingKeyProxyASNList,
			RuleID:       entity.RiskRuleIDProxyIPLogin,
			Label:        "代理/数据中心 ASN 名单",
			Kind:         RiskConfigKindASNList,
			DefaultValue: riskProxyASNDefault,
			Help: "逗号/分号/空格分隔，形如 AS15169 或 15169。" +
				"⚠️ 留空 = 关闭本规则（名单为空时该规则完全不告警），这是有意的「宁可漏报不要误报」",
		},
		{
			Key:          RiskEvaluateIntervalSettingKey,
			Label:        "判定评估间隔",
			Kind:         RiskConfigKindPositiveInt,
			DefaultValue: strconv.Itoa(DefaultRiskEvaluateIntervalMinutes),
			Unit:         "分钟",
			Help: fmt.Sprintf("1–1440 的整数分钟，留空回到默认 %d 分钟。"+
				"⚠️ 填超过 1440 的值会让评估**比预期更频繁**（不是更少）：越界值会被解析器拒绝并回落到默认值",
				DefaultRiskEvaluateIntervalMinutes),
			MinValue: 1,
			MaxValue: 1440,
		},
	}
}

// riskConfigSpecByKey 查登记项（写接口的白名单判定）。
func riskConfigSpecByKey(key string) (riskConfigSpec, bool) {
	trimmed := strings.TrimSpace(key)
	for _, spec := range riskConfigSpecs() {
		if spec.Key == trimmed {
			return spec, true
		}
	}
	return riskConfigSpec{}, false
}

// ValidateRiskConfigValueForKey 校验某项配置的取值（写接口与前端共用同一套规则）。
//
// 两类取值的语义**完全相反**，这是本函数存在的核心理由：
//
//	asn_list      空串合法（= 关闭规则）；坏项必须拒绝
//	positive_int  空串 = 未配置（服务端按「删除该行」处理，不是存空串）；越界必须拒绝
func ValidateRiskConfigValueForKey(key, value string) error {
	spec, ok := riskConfigSpecByKey(key)
	if !ok {
		return fmt.Errorf("%w：%s", ErrRiskConfigUnknown, strings.TrimSpace(key))
	}
	return validateRiskConfigValue(spec, value)
}

func validateRiskConfigValue(spec riskConfigSpec, value string) error {
	switch spec.Kind {
	case RiskConfigKindASNList:
		// 复用保存时的严格校验：坏项 → 400，避免「保存成功但名单里那条静默失效」。
		if err := ValidateProxyASNList(value); err != nil {
			return fmt.Errorf("%w：%s 只能是逗号/空格分隔的 ASN 编号（形如 AS15169 或 15169）",
				ErrRiskConfigInvalid, spec.Label)
		}
		return nil
	case RiskConfigKindPositiveInt:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		// 只接受纯数字：`1.5` / `1e3` / `+5` 一律拒绝。
		//
		// ⚠️ 不能直接 strconv.Atoi：它会接受 `+5` 与 `-5`，
		// 而 `-5` 作为间隔在 time.NewTimer 里等价于「立即触发」= 忙循环。
		for _, r := range trimmed {
			if r < '0' || r > '9' {
				return fmt.Errorf("%w：%s 必须是 %v–%v 之间的整数（当前 %q）",
					ErrRiskConfigInvalid, spec.Label, spec.MinValue, spec.MaxValue, trimmed)
			}
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || float64(parsed) < spec.MinValue || float64(parsed) > spec.MaxValue {
			return fmt.Errorf("%w：%s 必须是 %v–%v 之间的整数（当前 %q）",
				ErrRiskConfigInvalid, spec.Label, spec.MinValue, spec.MaxValue, trimmed)
		}
		return nil
	default:
		return fmt.Errorf("%w：%s 的类型 %q 没有校验实现", ErrRiskConfigInvalid, spec.Label, spec.Kind)
	}
}

// normalizeRiskConfigValue 把取值编码成落库形态。
//
// 名单类**归一化后入库**（`as15169` → `AS15169`，去重、统一分隔符）：
// 读取侧本来宽容，但落库形态统一后人工查库排障不用再自己脑内归一化。
func normalizeRiskConfigValue(spec riskConfigSpec, value string) string {
	switch spec.Kind {
	case RiskConfigKindASNList:
		list := ParseProxyASNList(value)
		return strings.Join(list, ",")
	case RiskConfigKindPositiveInt:
		return strings.TrimSpace(value)
	default:
		return strings.TrimSpace(value)
	}
}

// RiskConfigs 返回全部非数值配置项及其当前生效值。
func RiskConfigs(db *gorm.DB) ([]RiskConfigItem, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	specs := riskConfigSpecs()
	items := make([]RiskConfigItem, 0, len(specs))
	for _, spec := range specs {
		items = append(items, riskConfigItem(db, spec))
	}
	return items, nil
}

// riskConfigItem 解析单项的当前生效值（显式配置 > 代码默认值）。
func riskConfigItem(db *gorm.DB, spec riskConfigSpec) RiskConfigItem {
	item := RiskConfigItem{
		Key:          spec.Key,
		RuleID:       spec.RuleID,
		Label:        spec.Label,
		Kind:         spec.Kind,
		Value:        spec.DefaultValue,
		DefaultValue: spec.DefaultValue,
		Source:       RiskConfigSourceCode,
		Unit:         spec.Unit,
		Help:         spec.Help,
		MinValue:     spec.MinValue,
		MaxValue:     spec.MaxValue,
	}
	if db == nil {
		return item
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, spec.Key, ""))
	if raw == "" {
		// 空行与「没有行」同义：都不能当成「显式配成空」。
		// 否则界面上「已配置」会恒为 true，重置按钮永远不出现。
		return item
	}
	item.Value = raw
	item.Source = RiskConfigSourceSetting
	item.Configured = true
	return item
}

// RiskConfigUpdateResult 是写接口的返回（含写入后的生效值，便于前端回显）。
type RiskConfigUpdateResult struct {
	Item  RiskConfigItem `json:"item"`
	Reset bool           `json:"reset"`
}

// UpdateRiskConfigWithDB 设置某项非数值配置。
//
// ⚠️ 空值一律按「删除该行」处理（与 ResetRiskConfigWithDB 同一实现）：
// 存一个空串行会让 `Configured` 恒 true 而 `Value` 恒为空，
// 界面上表现为「已配置但没有值」，而且重置按钮不再出现 —— 无法退出该状态。
func UpdateRiskConfigWithDB(db *gorm.DB, key, value string) (RiskConfigUpdateResult, error) {
	if db == nil {
		return RiskConfigUpdateResult{}, fmt.Errorf("数据库未初始化")
	}
	spec, ok := riskConfigSpecByKey(key)
	if !ok {
		return RiskConfigUpdateResult{}, fmt.Errorf("%w：%s", ErrRiskConfigUnknown, strings.TrimSpace(key))
	}
	if err := validateRiskConfigValue(spec, value); err != nil {
		return RiskConfigUpdateResult{}, err
	}
	encoded := normalizeRiskConfigValue(spec, value)
	if encoded == "" {
		return ResetRiskConfigWithDB(db, key)
	}
	if err := saveSystemSettingValue(db, spec.Key, encoded, "风控配置："+spec.Label); err != nil {
		return RiskConfigUpdateResult{}, err
	}
	return RiskConfigUpdateResult{Item: riskConfigItem(db, spec)}, nil
}

// ResetRiskConfigWithDB 把某项配置清回「未配置」（回到代码默认值）。
//
// 语义：**删掉 system_settings 的行**而不是写默认值。
// 这与 ResetRiskThresholdWithDB 同一条纪律 —— 写默认值会让「显式配成默认值」
// 与「没配」变得不可区分，而 `Configured` 字段正是靠行是否存在来区分的。
func ResetRiskConfigWithDB(db *gorm.DB, key string) (RiskConfigUpdateResult, error) {
	if db == nil {
		return RiskConfigUpdateResult{}, fmt.Errorf("数据库未初始化")
	}
	spec, ok := riskConfigSpecByKey(key)
	if !ok {
		return RiskConfigUpdateResult{}, fmt.Errorf("%w：%s", ErrRiskConfigUnknown, strings.TrimSpace(key))
	}
	if db.Migrator().HasTable(entity.TableNameSystemSetting) {
		if err := db.Session(&gorm.Session{NewDB: true}).
			Where("key = ?", spec.Key).Delete(&entity.SystemSetting{}).Error; err != nil {
			return RiskConfigUpdateResult{}, err
		}
	}
	return RiskConfigUpdateResult{Item: riskConfigItem(db, spec), Reset: true}, nil
}

// SortedRiskConfigKeys 返回全部已登记键（稳定顺序，供测试与诊断使用）。
func SortedRiskConfigKeys() []string {
	specs := riskConfigSpecs()
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		keys = append(keys, spec.Key)
	}
	sort.Strings(keys)
	return keys
}
