package service

import (
	"errors"
	"strings"

	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 本文件实现 `19-二次注册判定与绑定记录.md` §3 的**四个 no_bonus 开关**与判定，
// 口径按 `44-二次注册四情形与追缴定案.md` §2 / §5.5 的权威编号。
//
// # 这四个开关管什么（与追缴的分工）
//
//	四开关  → 决定「**注册那一刻**带着旧值」时是否赠送（情形 1 / 2 的注册侧）
//	追缴开关 → 决定「注册时合法拿到、**事后换绑**到旧值」时是否追回（情形 2 / 4 的换绑侧）
//
// 两者**独立可任意组合**，谁都不隐含谁。
//
// # 键名与默认值
//
// 全小写 + 下划线，与 `recharge_promotion_enabled` / `review_rebate_enabled` 同风格；
// **不带 `enabled` 后缀**：这些不是「功能总开关」，关掉只是「不拦截」，注册与赠送照旧。
// 四个默认值**全部 false** —— 与项目红线一致（配置丢失不能等于悄悄改变发放规则）。
const (
	// HistoryPhoneNoBonusSettingKey：注册手机号命中**换绑释放**过的值 → 不赠送。
	HistoryPhoneNoBonusSettingKey = "history_phone_no_bonus"
	// HistoryWechatNoBonusSettingKey：注册微信命中换绑释放过的值 → 不赠送。
	HistoryWechatNoBonusSettingKey = "history_wechat_no_bonus"
	// CurrentPhoneNoBonusSettingKey：注册手机号命中**注销释放**的值 → 不赠送。
	CurrentPhoneNoBonusSettingKey = "current_phone_no_bonus"
	// CurrentWechatNoBonusSettingKey：注册微信命中注销释放的值 → 不赠送。
	CurrentWechatNoBonusSettingKey = "current_wechat_no_bonus"
)

// RegistrationBonusSwitches 是四个 no_bonus 开关的取值快照。
//
// 抽成结构体而不是四次读库：判定必须是**同一时刻的一致快照**，否则管理员在
// 两次读取之间改设置，会出现「手机号按新设置、微信按旧设置」的混合态。
type RegistrationBonusSwitches struct {
	HistoryPhone  bool
	HistoryWechat bool
	CurrentPhone  bool
	CurrentWechat bool
}

// AnyEnabled 表示至少有一个开关打开。全关时判定可以整段短路（零查询）。
func (s RegistrationBonusSwitches) AnyEnabled() bool {
	return s.HistoryPhone || s.HistoryWechat || s.CurrentPhone || s.CurrentWechat
}

// GetRegistrationBonusSwitchesWithDB 读取四个开关。缺表 / 缺记录 / 值非法一律 false。
func GetRegistrationBonusSwitchesWithDB(db *gorm.DB) RegistrationBonusSwitches {
	return RegistrationBonusSwitches{
		HistoryPhone:  getBoolSystemSetting(db, HistoryPhoneNoBonusSettingKey),
		HistoryWechat: getBoolSystemSetting(db, HistoryWechatNoBonusSettingKey),
		CurrentPhone:  getBoolSystemSetting(db, CurrentPhoneNoBonusSettingKey),
		CurrentWechat: getBoolSystemSetting(db, CurrentWechatNoBonusSettingKey),
	}
}

// SetRegistrationBonusSwitchWithDB 写入单个开关（键名必须是四个之一）。
//
// 未知键直接报错而不是静默忽略：写错键名会让运营以为自己打开了拦截，
// 而实际什么都没发生 —— 这类「静默无效」是本功能最危险的失败模式。
func SetRegistrationBonusSwitchWithDB(db *gorm.DB, key string, enabled bool) error {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case HistoryPhoneNoBonusSettingKey, HistoryWechatNoBonusSettingKey,
		CurrentPhoneNoBonusSettingKey, CurrentWechatNoBonusSettingKey:
	default:
		return ErrInvalidRegistrationBonusSwitch
	}
	return saveSystemSettingValue(db, normalized, boolText(enabled), registrationBonusSwitchRemark(normalized))
}

// boolText 与项目其它设置写入保持同一表示（strconv 的 "true"/"false"）。
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func registrationBonusSwitchRemark(key string) string {
	switch key {
	case HistoryPhoneNoBonusSettingKey:
		return "注册手机号命中换绑释放过的值时不赠送新用户赠送"
	case HistoryWechatNoBonusSettingKey:
		return "注册微信命中换绑释放过的值时不赠送新用户赠送"
	case CurrentPhoneNoBonusSettingKey:
		return "注册手机号命中注销释放的值时不赠送新用户赠送"
	default:
		return "注册微信命中注销释放的值时不赠送新用户赠送"
	}
}

// ErrInvalidRegistrationBonusSwitch 表示传入的开关键名不属于这四个。
var ErrInvalidRegistrationBonusSwitch = errors.New("二次注册开关键名无效（只能是 history_/current_ × phone/wechat）")

// RegistrationBonusDecision 是注册时的一次判定结果。
//
// 保留**命中项清单**而不只是一个 bool：42-*.md §3.2 要求扣减记录的 remark
// 写明**全部**命中项（用「；」连接），只写第一个会让运营看不出还有别的触发条件。
type RegistrationBonusDecision struct {
	// Suppressed 表示命中任一开启的开关 → 不赠送。
	Suppressed bool
	// Reasons 是命中项的固定文案（顺序：手机号历史 → 微信历史 → 手机号当前 → 微信当前）。
	Reasons []string
}

// EvaluateRegistrationBonus 按 `44-*.md` §2 计算「是否赠送」。
//
// 逻辑只有一层 OR（19 §3.3 / 44 §2 明确「四条件是 OR」）：
//
//	suppressed = (historyPhone && Hp) || (historyWechat && Hw) || (currentPhone && Cp) || (currentWechat && Cw)
//
// 注意开关的**短路语义**：开关关闭时对应条件直接为 false，**不查表**。
// 调用方应先看 `switches.AnyEnabled()`，全关时连 Lookup 都不用做（默认配置的零成本路径）。
func EvaluateRegistrationBonus(switches RegistrationBonusSwitches, lookup BindingLookup) RegistrationBonusDecision {
	var decision RegistrationBonusDecision
	// 顺序固定，保证 remark 文案稳定可断言（测试与运营文档都依赖这个顺序）。
	if switches.HistoryPhone && lookup.HistoryPhone {
		decision.Reasons = append(decision.Reasons, "检测到手机号存在已注册记录，不再进行新用户赠送")
	}
	if switches.HistoryWechat && lookup.HistoryWechat {
		decision.Reasons = append(decision.Reasons, "检测到微信存在已注册记录，不再进行新用户赠送")
	}
	if switches.CurrentPhone && lookup.CurrentPhone {
		decision.Reasons = append(decision.Reasons, "检测到手机号在已注销账号上绑定过，不再进行新用户赠送")
	}
	if switches.CurrentWechat && lookup.CurrentWechat {
		decision.Reasons = append(decision.Reasons, "检测到微信在已注销账号上绑定过，不再进行新用户赠送")
	}
	decision.Suppressed = len(decision.Reasons) > 0
	return decision
}

// RegistrationBonusDecisionRemark 把命中项拼成一条 remark（42-*.md §3.2）。
func RegistrationBonusDecisionRemark(decision RegistrationBonusDecision) string {
	parts := make([]string, 0, len(decision.Reasons))
	for _, reason := range decision.Reasons {
		if trimmed := strings.TrimSpace(reason); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "；")
}

// RegistrationBonusSuppressionReasonCode 把命中项折成稳定的代码串（供筛选与审计）。
//
// 账单 remark 是给人看的；这里给机器一个可稳定比对的标识，避免将来靠中文文案做匹配。
func RegistrationBonusSuppressionReasonCode(decision RegistrationBonusDecision) string {
	codes := make([]string, 0, len(decision.Reasons))
	for _, reason := range decision.Reasons {
		switch {
		case strings.Contains(reason, "手机号存在已注册记录"):
			codes = append(codes, "history_phone")
		case strings.Contains(reason, "微信存在已注册记录"):
			codes = append(codes, "history_wechat")
		case strings.Contains(reason, "手机号在已注销账号"):
			codes = append(codes, "current_phone")
		case strings.Contains(reason, "微信在已注销账号"):
			codes = append(codes, "current_wechat")
		}
	}
	return strings.Join(codes, ",")
}

// ResolveRegistrationBonusDecisionWithDB 是一次完整的注册侧判定（读开关 + 查绑定 + 求值）。
//
// 三个早退点，都对应「零额外查询」：
//  1. 四个开关全关 → 直接返回不抑制（默认配置，生产上等于这段代码不执行）；
//  2. 同时没有手机号与微信 → 无可判定输入；
//  3. 绑定记录表还不存在 → 一律视为未命中（功能没就绪不该改变发放行为）。
//
// ⚠️ **本分支没有把它接进注册链路**：注册事务在 `internal/controller/user.go`，
// 该文件由另一位成员独占写入（第 8 轮范围隔离）。本函数与四开关的就绪状态、
// 以及 42-*.md 的「先发再扣」记账形态（`registration_bonus_revoked`）都属于
// 同一条链路的**下一段**，接线时的落点是 `CreateUserWithNewUserBonusInTx`
// 在 `db.Create(user)` 之后、写 `registration_bonus` 之前取判定快照。
func ResolveRegistrationBonusDecisionWithDB(db *gorm.DB, phone, wechat string) (RegistrationBonusDecision, error) {
	switches := GetRegistrationBonusSwitchesWithDB(db)
	if !switches.AnyEnabled() {
		return RegistrationBonusDecision{}, nil
	}
	if strings.TrimSpace(phone) == "" && strings.TrimSpace(wechat) == "" {
		return RegistrationBonusDecision{}, nil
	}
	lookup, err := LookupBindingHistoryWithDB(db, phone, wechat)
	if err != nil {
		return RegistrationBonusDecision{}, err
	}
	return EvaluateRegistrationBonus(switches, lookup), nil
}

// RoundRegistrationBonusAmount 把赠送额收敛到钱包精度（6 位小数）。
// 抽出来是为了让追缴额与赠送额用**同一个取整口径**，否则会出现「追 1.000001 但发的是 1」。
func RoundRegistrationBonusAmount(amount float64) float64 {
	return util.RoundMoney(amount)
}
