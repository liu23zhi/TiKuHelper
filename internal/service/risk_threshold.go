package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 风控阈值的**写入路径**（96 §2.2 的「阈值可配」落地）。
//
// # 为什么需要这个文件
//
// 在此之前，`risk_rule_<ruleId>_threshold` 这些 system_settings 键**没有任何写入路径**：
//   - `UpdateSystemSettingInput` 是固定 struct，不含风控阈值字段；
//   - `risk_admin.go` 没有 risk_rules 的写接口；
//   - 前端故意不做输入框（做了也打不到后端）。
//
// 而 `riskRuleThreshold` 的三层兜底第一层读的就是这些键 ⇒ 那一层**永远读不到值**，
// 实际生效的永远是代码默认值。这是「配置项存在但没有任何 UI/API 能写」的形态：
// 运营以为能调，改了不生效，且不报错。
//
// # 写入语义（两处都写，缺一会漂移）
//
// 有效阈值 = system_settings 的键（第一层）> risk_rules.threshold 列（第二层）> 代码默认值。
// 写入时**同时**更新两处：
//   - system_settings：立即生效（判定每次现读）；
//   - risk_rules.threshold：与设置键保持一致，否则将来某天设置的键被删/迁移，
//     阈值会**静默跳回**表里那个旧值 —— 而运营以为还是自己设的那个数。

// 风控阈值条目的「类型」——决定取值范围，不是装饰字段。
const (
	// RiskThresholdKindAbsolute 绝对数（金额/次数/账号数），必须 > 0。
	RiskThresholdKindAbsolute = "absolute"
	// RiskThresholdKindInteger 正整数（R16 的「连续 N 天」），必须 ≥ 1 且无小数。
	RiskThresholdKindInteger = "integer"
	// RiskThresholdKindRatio 占比（R16 的凌晨占比），必须 (0, 1]。
	//
	// ⚠️ 它和 absolute 语义完全不同：占比填成 3 会让 `share < 3` 恒真判据永不触发
	// （share 最大 1.0），表现为「规则静默失效」。所以必须单独校验，不能共用一个输入框的规则。
	RiskThresholdKindRatio = "ratio"
)

// RiskThresholdSettingKeyOffhoursShare 是 R16「凌晨占比」的设置键。
//
// 它**不是** `risk_rule_<id>_threshold` 形态（那个键存的是「连续天数」），
// 但同属运营可调，因此和规则阈值走同一套读写接口，靠 Kind 区分校验规则。
const RiskThresholdSettingKeyOffhoursShare = riskOffhoursShareSettingKey

var (
	// ErrRiskThresholdInvalid 覆盖「≤0」「非数」「占比越界」等全部取值非法。
	ErrRiskThresholdInvalid = errors.New("风控阈值取值非法")
	// ErrRiskThresholdUnknown 表示请求的键不在代码登记的白名单里。
	ErrRiskThresholdUnknown = errors.New("未知的风控阈值键")
)

// RiskThresholdItem 是一个可调的阈值项（读接口的返回元素，也是写接口的白名单元素）。
type RiskThresholdItem struct {
	// Key 是 system_settings 的键，同时是写接口的路径参数。
	Key string `json:"key"`
	// RuleID 是对应的规则 id；占比项为 R16 的规则 id（便于前端分组管理）。
	RuleID string `json:"ruleId"`
	// Label 是给运营看的中文名。
	Label string `json:"label"`
	// Kind 见 RiskThresholdKind*。
	Kind string `json:"kind"`
	// Value 是**当前生效值**（三层兜底后的结果）。
	Value float64 `json:"value"`
	// DefaultValue 是代码默认值，用于「重置为默认」与界面提示。
	DefaultValue float64 `json:"defaultValue"`
	// Source 说明当前值来自哪一层：system_settings / risk_rules / code。
	Source string `json:"source"`
	// Configured 表示是否存在显式配置（system_settings 有行）。
	//
	// 它与 Source 不同：「读不到 → 用默认值」没有行，而「显式配成默认值」有行。
	// 两者 Value 相同但语义不同，重置能力也依赖它。
	Configured bool `json:"configured"`
	// Unit 是展示单位（如「次」「元」「天」「比例」）。
	Unit string `json:"unit"`
}

// 阈值来源标记。
const (
	RiskThresholdSourceSetting = "system_settings"
	RiskThresholdSourceColumn  = "risk_rules"
	RiskThresholdSourceCode    = "code"
)

// riskThresholdSpec 是代码内的阈值登记表 —— 唯一真相。
//
// 写接口只接受这里登记过的 Key（白名单），不接受任意字符串：
// 否则一个手滑的请求就能往 system_settings 写任意键。
type riskThresholdSpec struct {
	Key          string
	RuleID       string
	Label        string
	Kind         string
	DefaultValue float64
	Unit         string
	// SyncRuleColumn 表示这一项要不要同步写 risk_rules.threshold 列。
	//
	// ⚠️ 这个字段是必须的，不能一律 true（第一版就是一律 true，被测试抓到）：
	// R16 有**两个**可调项 —— `risk_rule_offhours_activity_threshold`（连续天数）
	// 与 `risk_offhours_share_threshold`（凌晨占比）。两者 RuleID 相同但语义完全不同。
	// 若占比项也去写 risk_rules.threshold，就会把「连续天数」列覆盖成 0.9，
	// 而 `consecutiveDays < 1` 的判断会让 R16 **直接短路永不告警** ——
	// 表现为「改了占比阈值后规则彻底不响」，且没有任何报错。
	//
	// 所以：只有 `risk_rule_<id>_threshold` 形态的项才同步列；占比项不碰它。
	SyncRuleColumn bool
	// MaxValue 是该阈值的**上界**（0 = 不设上界，仅用于理论上无上限的项）。
	//
	// # 为什么必须有上界（t193 审计实测的缺陷）
	//
	// 原始实现只有下界（>0 / ≥1 / (0,1]），没有上界。实测把 R16 的连续天数
	// 填成 1440（两年）→ 规则**静默失效**（0 告警、评估耗时 2ms），而管理页
	// 显示的「阈值 = 1440」看起来完全正常 —— 运营会以为风控在工作。
	//
	// ⇒ 「填得进去」不等于「填得有意义」。上界的职责是把「必然让规则失效的
	// 极端值」挡在写入之前，而不是限制运营的正当调整空间。所以上界要**明显
	// 宽松于任何现实配置**（见各项注释里的依据），只拦「一眼就是打错」的值。
	MaxValue float64
}

// riskThresholdSpecs 按「欠款 → 行为 R1/R8/R11/R12 → R16 两项」顺序返回。
//
// 默认值一律与 entity.DefaultRiskRules() 保持一致；这里写字面量而不是引用常量，
// 是为了让「代码默认值被改动」在这张表上**暴露出来**（引用常量会让两边同步漂移，
// 测试测不出差异）。两者一致性由 TestRiskThresholdSpecsMatchEntityDefaults 钉住。
// RiskThresholdSettingKeyTokenMultiIP 是 R12「同一 token 被多少不同 IP 使用」的阈值键。
//
// 不带 `risk_rule_` 前缀：那个前缀由 riskRuleThresholdSettingKey() 自动生成，
// 专用于「每条规则一个主阈值」。附加判据用独立键 —— 与 `risk_offhours_share_threshold`
// （R16 的占比项）同构。
const RiskThresholdSettingKeyTokenMultiIP = "risk_token_abuse_multi_ip_threshold"

func riskThresholdSpecs() []riskThresholdSpec {
	return []riskThresholdSpec{
		{Key: "risk_rule_debt_user_threshold", RuleID: entity.RiskRuleIDDebtUser,
			Label: "单用户欠款阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 100, Unit: "元", SyncRuleColumn: true, MaxValue: 1_000_000},
		{Key: "risk_rule_debt_batch_threshold", RuleID: entity.RiskRuleIDDebtBatch,
			Label: "单批次欠款阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 1000, Unit: "元", SyncRuleColumn: true, MaxValue: 10_000_000},
		{Key: "risk_rule_debt_system_threshold", RuleID: entity.RiskRuleIDDebtSystem,
			Label: "系统总欠款突增阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 5000, Unit: "元", SyncRuleColumn: true, MaxValue: 10_000_000},
		{Key: "risk_rule_multi_account_ip_threshold", RuleID: entity.RiskRuleIDMultiAccountIP,
			Label: "同 IP 账号数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 5, Unit: "个", SyncRuleColumn: true, MaxValue: 10_000},
		{Key: "risk_rule_ticket_flood_threshold", RuleID: entity.RiskRuleIDTicketFlood,
			Label: "24h 建单数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 5, Unit: "张", SyncRuleColumn: true, MaxValue: 10_000},
		{Key: "risk_rule_abnormal_register_pattern_threshold", RuleID: entity.RiskRuleIDAbnormalRegisterPattern,
			Label: "同前缀连续用户名阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 3, Unit: "个", SyncRuleColumn: true, MaxValue: 10_000},
		{Key: "risk_rule_token_abuse_threshold", RuleID: entity.RiskRuleIDTokenAbuse,
			Label: "配额消耗倍数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 3, Unit: "倍", SyncRuleColumn: true, MaxValue: 1_000},
		// 「同一 token 被多个不同 IP 使用」是**独立判据**，必须有自己的键。
		//
		// ⚠️ 为什么必须拆（t193 审计实测的问题）：这两个语义的合理默认值差很远 ——
		//   · 配额超 3 倍 = 明显滥用（一个 token 花掉 3 倍配额）
		//   · 3 个 IP 共用同一 token = **很可能是正常场景**（手机 + 电脑 + 平板）
		// 共用一个键时，运营想把 IP 维度放宽到 5 就必然把配额倍数也放宽到 5
		// （5 倍配额才告警 = 严重漏报）；反之收紧配额会让 2 个 IP 共用就告警（误报）。
		//
		// # 命名为什么不带 risk_rule_ 前缀
		//
		// `risk_rule_<id>_threshold` 是 **riskRuleThresholdSettingKey() 自动生成**的
		// （risk_alert.go:423），它是「每条规则一个主阈值」的约定。
		// 一条规则的第二、第三个可调项不带这个前缀 —— 仓内先例是
		// `risk_offhours_share_threshold`（R16 的占比项，见 risk_alert.go:1607）。
		// 所以这里叫 `risk_token_abuse_multi_ip_threshold`，与先例同构。
		//
		// # 为什么不 SyncRuleColumn
		//
		// `risk_rules.threshold` **只有一列**，只能承载「该规则的主阈值」。
		// 配额倍数是主判据（97 号 L321 原文只给了配额口径：「调用量 > 其配额 3 倍」），
		// 它占用这一列；IP 个数是附加判据，不同步。
		// 若两个都同步，后写的会覆盖先写的 —— 与 R16 占比污染连续天数同款缺陷。
		{Key: RiskThresholdSettingKeyTokenMultiIP, RuleID: entity.RiskRuleIDTokenAbuse,
			Label: "同一 token 的不同 IP 数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 5, Unit: "个", SyncRuleColumn: false, MaxValue: 100},
		{Key: "risk_rule_offhours_activity_threshold", RuleID: entity.RiskRuleIDOffhoursActivity,
			Label: "连续天数阈值", Kind: RiskThresholdKindInteger, DefaultValue: 3, Unit: "天", SyncRuleColumn: true, MaxValue: 365},
		{Key: "risk_rule_binding_churn_threshold", RuleID: entity.RiskRuleIDBindingChurn,
			Label: "24h 主动换绑次数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 2, Unit: "次", SyncRuleColumn: true, MaxValue: 1_000},
		{Key: "risk_rule_wechat_phone_share_threshold", RuleID: entity.RiskRuleIDWechatPhoneShare,
			Label: "同号关联账号数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 3, Unit: "个", SyncRuleColumn: true, MaxValue: 10_000},
		{Key: "risk_rule_impossible_travel_threshold", RuleID: entity.RiskRuleIDImpossibleTravel,
			Label: "异地瞬移速度上限", Kind: RiskThresholdKindAbsolute, DefaultValue: 900, Unit: "km/h", SyncRuleColumn: true, MaxValue: 40_000},
		{Key: "risk_rule_credential_stuffing_threshold", RuleID: entity.RiskRuleIDCredentialStuffing,
			Label: "30 分钟内不同账号数阈值", Kind: RiskThresholdKindAbsolute, DefaultValue: 20, Unit: "个", SyncRuleColumn: true, MaxValue: 10_000},
		{Key: "risk_rule_login_bruteforce_threshold", RuleID: entity.RiskRuleIDLoginBruteforce,
			Label: "10 分钟内失败次数阈值（账号维度与 IP 维度共用此值）", Kind: RiskThresholdKindAbsolute, DefaultValue: 10, Unit: "次", SyncRuleColumn: true, MaxValue: 10_000},
		// 占比项：**不**同步 risk_rules.threshold（见 SyncRuleColumn 的注释）。
		{Key: RiskThresholdSettingKeyOffhoursShare, RuleID: entity.RiskRuleIDOffhoursActivity,
			Label: "凌晨活动占比阈值", Kind: RiskThresholdKindRatio, DefaultValue: 0.8, Unit: "比例", SyncRuleColumn: false, MaxValue: 1},
	}
}

// riskThresholdSpecByKey 查登记项。
func riskThresholdSpecByKey(key string) (riskThresholdSpec, bool) {
	trimmed := strings.TrimSpace(key)
	for _, spec := range riskThresholdSpecs() {
		if spec.Key == trimmed {
			return spec, true
		}
	}
	return riskThresholdSpec{}, false
}

// ValidateRiskThresholdValue 校验取值是否合法（写接口与前端共用同一套规则）。
//
// 这是本功能最关键的一道闸：`riskRuleThreshold` 的注释写明「一旦把无效值当成 0，
// `|balance| >= 0` 会让每个用户都告警」。所以非法值必须**拒绝**，不能静默夹紧。
func ValidateRiskThresholdValue(kind string, value float64) error {
	return ValidateRiskThresholdValueForKey("", kind, value)
}

// ValidateRiskThresholdValueForKey 是带键的完整校验：类型规则 + 该项的**上界**。
//
// # 为什么需要上界（t193 审计实测的缺陷）
//
// 原实现只有下界。实测把 R16 的连续天数填成 1440（两年）→ 规则**静默失效**
// （0 告警、评估 2ms），而管理页显示的「阈值 = 1440」看起来完全正常。
// 填得进去 ≠ 填得有意义，所以上界是把「必然让规则失效的极端值」挡在写入之前。
//
// # 上界怎么定的（原则：明显宽松于任何现实配置）
//
// 只拦「一眼就是打错」的值，不限制正当调整空间：
//   - 计数量（账号数 / 次数 / 建单数）：10000。风控规则的含义是「超过就该看」，
//     阈值调到一万以上等于关掉这条规则 —— 而关规则有专门的启停开关，不必用阈值。
//   - 连续天数：365。超过一年不可能有运营意义，且历史扫描量会失控。
//   - 占比：1（硬上限，(0,1] 本就如此）。
//   - 速度：40000 km/h。这是**临近地球逃逸速度量级**的荒谬值上界（民航 900、
//     战斗机 3000、洲际导弹 25000）。只挡住「把 km/h 当成 m/s 手滑多打三个零」。
//   - 金额：单用户 100 万、单批次/系统 1000 万。超出这个量级的欠款说明数据已损坏。
//
// ⚠️ 上界**不写进数据库**，它是代码里的规格（`riskThresholdSpec.MaxValue`）。
// 改上界不需要迁移，与「默认值写在代码里」是同一套理由（96 §2.2）。
func ValidateRiskThresholdValueForKey(key, kind string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%w：必须是有限数", ErrRiskThresholdInvalid)
	}
	switch kind {
	case RiskThresholdKindRatio:
		// 占比必须落在 (0, 1]。填 3 会让 `share < 3` 恒真、规则永不触发。
		if value <= 0 || value > 1 {
			return fmt.Errorf("%w：占比必须大于 0 且不超过 1（当前 %v）", ErrRiskThresholdInvalid, value)
		}
	case RiskThresholdKindInteger:
		// 「连续 N 天」必须是正整数；2.5 会被 int() 截断成 2，语义漂移。
		if value < 1 || value != math.Trunc(value) {
			return fmt.Errorf("%w：连续天数必须是不小于 1 的整数（当前 %v）", ErrRiskThresholdInvalid, value)
		}
	default:
		// absolute
		if value <= 0 {
			return fmt.Errorf("%w：阈值必须大于 0（当前 %v）", ErrRiskThresholdInvalid, value)
		}
	}
	// 上界：只在**能查到规格**时检查。
	//
	// 不认识的 key 不加额外限制（保持向后兼容：ValidateRiskThresholdValue 的
	// 单参数版本传空 key，老调用方的行为一字不变）。
	if spec, ok := riskThresholdSpecByKey(key); ok && spec.MaxValue > 0 && value > spec.MaxValue {
		return fmt.Errorf("%w：%s 不能超过 %v（当前 %v）。"+
			"阈值调到这个量级会让规则实际失效 —— 若要停用规则请用启停开关",
			ErrRiskThresholdInvalid, spec.Label, spec.MaxValue, value)
	}
	return nil
}

// RiskThresholds 返回全部可调阈值项及其当前生效值。
func RiskThresholds(db *gorm.DB) ([]RiskThresholdItem, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	specs := riskThresholdSpecs()
	items := make([]RiskThresholdItem, 0, len(specs))
	for _, spec := range specs {
		items = append(items, riskThresholdItem(db, spec))
	}
	return items, nil
}

// riskThresholdItem 解析单项的三层兜底结果。
func riskThresholdItem(db *gorm.DB, spec riskThresholdSpec) RiskThresholdItem {
	item := RiskThresholdItem{
		Key:          spec.Key,
		RuleID:       spec.RuleID,
		Label:        spec.Label,
		Kind:         spec.Kind,
		DefaultValue: spec.DefaultValue,
		Unit:         spec.Unit,
		Value:        spec.DefaultValue,
		Source:       RiskThresholdSourceCode,
	}
	// 第一层：system_settings。
	raw := getSystemSettingStringWithDefault(db, spec.Key, "")
	if strings.TrimSpace(raw) != "" {
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && value > 0 {
			item.Value = value
			item.Source = RiskThresholdSourceSetting
			item.Configured = true
			return item
		}
	}
	// 第二层：risk_rules.threshold 列。
	if db.Migrator().HasTable(entity.TableNameRiskRule) {
		var row entity.RiskRule
		err := db.Session(&gorm.Session{NewDB: true}).
			Where("rule_id = ?", spec.RuleID).First(&row).Error
		if err == nil && row.Threshold > 0 {
			item.Value = row.Threshold
			item.Source = RiskThresholdSourceColumn
			return item
		}
	}
	return item
}

// RiskThresholdUpdateResult 是写接口的返回（含写入后的生效值，便于前端回显）。
type RiskThresholdUpdateResult struct {
	Item RiskThresholdItem `json:"item"`
	// Reset 表示本次是「重置为默认」而不是显式设值。
	Reset bool `json:"reset"`
}

// UpdateRiskThresholdWithDB 设置某个阈值的显式值（两处都写）。
func UpdateRiskThresholdWithDB(db *gorm.DB, key string, value float64) (RiskThresholdUpdateResult, error) {
	if db == nil {
		return RiskThresholdUpdateResult{}, fmt.Errorf("数据库未初始化")
	}
	spec, ok := riskThresholdSpecByKey(key)
	if !ok {
		return RiskThresholdUpdateResult{}, fmt.Errorf("%w：%s", ErrRiskThresholdUnknown, key)
	}
	if err := ValidateRiskThresholdValueForKey(spec.Key, spec.Kind, value); err != nil {
		return RiskThresholdUpdateResult{}, err
	}
	encoded := formatRiskThresholdValue(value)
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := saveSystemSettingValue(tx, spec.Key, encoded, "风控规则阈值："+spec.Label); err != nil {
			return err
		}
		return writeRiskRuleThresholdColumn(tx, spec, value)
	})
	if err != nil {
		return RiskThresholdUpdateResult{}, err
	}
	return RiskThresholdUpdateResult{Item: riskThresholdItem(db, spec)}, nil
}

// ResetRiskThresholdWithDB 把阈值重置为代码默认值。
//
// 语义：删掉 system_settings 的行（回到「未配置」），并把 risk_rules.threshold
// 写回代码默认值。**两处都要动** —— 只删行不重置列，会让第二层兜底接手上一个旧值。
func ResetRiskThresholdWithDB(db *gorm.DB, key string) (RiskThresholdUpdateResult, error) {
	if db == nil {
		return RiskThresholdUpdateResult{}, fmt.Errorf("数据库未初始化")
	}
	spec, ok := riskThresholdSpecByKey(key)
	if !ok {
		return RiskThresholdUpdateResult{}, fmt.Errorf("%w：%s", ErrRiskThresholdUnknown, key)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(entity.TableNameSystemSetting) {
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Where("key = ?", spec.Key).Delete(&entity.SystemSetting{}).Error; err != nil {
				return err
			}
		}
		return writeRiskRuleThresholdColumn(tx, spec, spec.DefaultValue)
	})
	if err != nil {
		return RiskThresholdUpdateResult{}, err
	}
	return RiskThresholdUpdateResult{Item: riskThresholdItem(db, spec), Reset: true}, nil
}

// writeRiskRuleThresholdColumn 把 risk_rules.threshold 列同步成同一个值。
//
// 行不存在时**不建行**：`loadRiskRules` 会用代码默认值补齐缺失行，
// 这里凭空建行反而会让「停用/启用」等列被默认值覆盖。
func writeRiskRuleThresholdColumn(tx *gorm.DB, spec riskThresholdSpec, value float64) error {
	// 占比项不碰这一列（详见 riskThresholdSpec.SyncRuleColumn 的注释：
	// 覆盖它会把「连续天数」改成 0.9，导致 R16 直接短路永不告警）。
	if !spec.SyncRuleColumn {
		return nil
	}
	if !tx.Migrator().HasTable(entity.TableNameRiskRule) {
		return nil
	}
	return tx.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).
		Where("rule_id = ?", spec.RuleID).
		UpdateColumn("threshold", value).Error
}

// formatRiskThresholdValue 把数值编码进 system_settings（字符串列）。
//
// 整数类型不带小数点，避免 `3` 存成 `3.000000` 让人工排查时困惑；
// 其余用最短可往返表示（strconv 的 -1 精度）。
func formatRiskThresholdValue(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// SortedRiskThresholdKeys 返回全部已登记键（稳定顺序，供测试与诊断使用）。
func SortedRiskThresholdKeys() []string {
	specs := riskThresholdSpecs()
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		keys = append(keys, spec.Key)
	}
	sort.Strings(keys)
	return keys
}

// ════════════════════════════════════════════════════════════════════
// 规则的启用/停用写路径
//
// 为什么与阈值放同一个文件：两者都是 `risk_rules` 行上「运营该能改」的列
// （entity/risk.go 的注释原文：「规则行只承载运营可调的部分：是否启用、级别、
// 节流、阈值」）。此前这两列**都没有写接口**，运营改不了。
//
// 接线的关键位置：`EvaluateDebtAlerts` 用 `enabledRules` 过滤
// `if !rule.Enabled { continue }` —— 所以停用后该规则不再进入候选收集。
// 这条接线必须有用例证明（本项目反复在「实现对了但没接」这一层出问题）。
// ════════════════════════════════════════════════════════════════════

// RiskRuleState 是一条规则的运营可见状态。
type RiskRuleState struct {
	RuleID string `json:"ruleId"`
	Name   string `json:"name"`
	// Enabled 是**当前生效**的启用状态（表里没有行时为代码默认值）。
	Enabled bool `json:"enabled"`
	// Severity / Threshold 也是生效值，便于前端一次拿全。
	Severity  string  `json:"severity"`
	Threshold float64 `json:"threshold"`
	// Exists 表示 risk_rules 表里有这一行（false = 用的是代码默认值补齐）。
	Exists bool `json:"exists"`
	// Description 给运营看的说明（含「需 3 天历史」这类重要前提）。
	Description string `json:"description"`
	// Unit 是生效阈值的单位（次 / 个 / 天 / 元 / km/h / 倍·个 / 比例；空串 = 该项无可调阈值）。
	//
	// ⚠️ 为什么由后端给而不是前端映射：
	// `GET /risk/rules` 原本只返回 threshold 数值，前端要显示「生效阈值：10 次」
	// 就得自己写一份 ruleId → 单位的映射表 —— 那是**第二份真相**，与
	// riskThresholdSpecs 的 Unit 必然漂移（新增规则时忘改前端就会出现错单位）。
	// 所以这里直接从规格表取：同一个 Unit 字段，只有一个来源。
	Unit string `json:"unit"`
}

// riskThresholdUnitForRule 按 RuleID 取该规则的阈值单位（供 GET /risk/rules 展示用）。
//
// 返回第一个匹配项的 Unit。同一规则有多个可调项时（R16 的「连续天数」与「占比」），
// 取到的会是**规格表里排在前面**的那个 —— 当前是「连续天数（天）」，
// 这与 RiskRuleState.Threshold 的口径一致（Threshold 也取 `riskRuleThreshold`，
// 即 `risk_rule_<id>_threshold` 那一项，不是占比项）。
//
// 找不到返回空串（表示这条规则没有可调阈值，或规格表漏登记 —— 后者由
// TestRiskRuleAndThresholdSpecsCoverSameRuleIDs 兜住）。
func riskThresholdUnitForRule(ruleID string) string {
	for _, spec := range riskThresholdSpecs() {
		if spec.RuleID == ruleID {
			return spec.Unit
		}
	}
	return ""
}

// RiskRuleStates 返回全部规则的当前状态（含未在表里建行的）。
func RiskRuleStates(db *gorm.DB) ([]RiskRuleState, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	// loadRiskRules 已经把「缺失行用默认值补齐」的逻辑做对了，直接复用，
	// 避免这里再写一份（两份必然漂移）。
	rules, err := loadRiskRules(db)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	if db.Migrator().HasTable(entity.TableNameRiskRule) {
		var ids []string
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).
			Pluck("rule_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			existing[id] = true
		}
	}
	defaults := map[string]entity.RiskRuleDefault{}
	for _, d := range entity.DefaultRiskRules() {
		defaults[d.RuleID] = d
	}
	states := make([]RiskRuleState, 0, len(rules))
	for _, rule := range rules {
		state := RiskRuleState{
			RuleID:      rule.RuleID,
			Name:        rule.Name,
			Enabled:     rule.Enabled,
			Severity:    rule.Severity,
			Threshold:   riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID)),
			Exists:      existing[rule.RuleID],
			Description: rule.Description,
			Unit:        riskThresholdUnitForRule(rule.RuleID),
		}
		if d, ok := defaults[rule.RuleID]; ok && state.Description == "" {
			state.Description = d.Description
		}
		states = append(states, state)
	}
	return states, nil
}

// SetRiskRuleEnabledWithDB 启用/停用某条规则。
//
// 表里没有行时**建一行**（与阈值不同）：停用一条「仅靠代码默认值补齐」的规则
// 必须留下显式记录，否则下次 loadRiskRules 又按默认启用，表现为「停用了但没生效」。
func SetRiskRuleEnabledWithDB(db *gorm.DB, ruleID string, enabled bool) (RiskRuleState, error) {
	if db == nil {
		return RiskRuleState{}, fmt.Errorf("数据库未初始化")
	}
	trimmed := strings.TrimSpace(ruleID)
	if !isKnownRiskRuleID(trimmed) {
		return RiskRuleState{}, fmt.Errorf("%w：%s", ErrRiskRuleUnknown, trimmed)
	}
	if !db.Migrator().HasTable(entity.TableNameRiskRule) {
		return RiskRuleState{}, fmt.Errorf("风控规则表不存在")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var row entity.RiskRule
		findErr := tx.Session(&gorm.Session{NewDB: true}).
			Where("rule_id = ?", trimmed).First(&row).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			// 建行：其余列用代码默认值填充，避免出现「threshold=0」这种
			// 会让判定退化成 `>= 0` 刷屏的行。
			defaults, ok := riskRuleDefaultByID(trimmed)
			if !ok {
				return fmt.Errorf("%w：%s", ErrRiskRuleUnknown, trimmed)
			}
			created := entity.RiskRule{
				RuleID:          defaults.RuleID,
				Name:            defaults.Name,
				Severity:        defaults.Severity,
				Enabled:         enabled,
				Threshold:       defaults.Threshold,
				ThrottleSeconds: defaults.ThrottleSeconds,
				Description:     defaults.Description,
			}
			// ⚠️ 这里必须「Create 之后再 UpdateColumn」两步，不能只靠 Create。
			//
			// 实测（探针 TestProbeEnabledZeroValueCreate 的结论）：
			// Enabled 列带 `default:true`，GORM 对「bool 零值(false) + 有 default tag」
			// 会**无条件用数据库默认值**，而且 `Select("enabled")` 也**覆盖不了**
			// （实测：裸 Create → true；Select 显式含 enabled → 仍 true）。
			// 于是「停用」会被静默写成 true —— 表现为「点了停用、提示保存成功、
			// 规则照旧告警」，且没有任何报错。
			//
			// UpdateColumn 走的是普通 UPDATE，不经过 default 逻辑，实测能正确写 false。
			if err := tx.Session(&gorm.Session{NewDB: true}).Create(&created).Error; err != nil {
				return err
			}
			if !enabled {
				return tx.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).
					Where("rule_id = ?", trimmed).
					UpdateColumn("enabled", false).Error
			}
			return nil
		}
		if findErr != nil {
			return findErr
		}
		return tx.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).
			Where("rule_id = ?", trimmed).
			UpdateColumn("enabled", enabled).Error
	})
	if err != nil {
		return RiskRuleState{}, err
	}
	states, err := RiskRuleStates(db)
	if err != nil {
		return RiskRuleState{}, err
	}
	for _, state := range states {
		if state.RuleID == trimmed {
			return state, nil
		}
	}
	return RiskRuleState{}, fmt.Errorf("%w：%s", ErrRiskRuleUnknown, trimmed)
}

// ErrRiskRuleUnknown 表示 ruleId 不在代码登记表里。
var ErrRiskRuleUnknown = errors.New("未知的风控规则")

// isKnownRiskRuleID 判断 ruleId 是否已登记（白名单，防手滑写任意值）。
func isKnownRiskRuleID(ruleID string) bool {
	_, ok := riskRuleDefaultByID(ruleID)
	return ok
}

func riskRuleDefaultByID(ruleID string) (entity.RiskRuleDefault, bool) {
	for _, d := range entity.DefaultRiskRules() {
		if d.RuleID == ruleID {
			return d, true
		}
	}
	return entity.RiskRuleDefault{}, false
}
