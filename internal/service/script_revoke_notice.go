package service

import (
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 通知区的三档级别。
//
// 只有两种会真的展示给用户（warning / info），`none` 表示「没什么要说的」——
// 刻意不用「空字符串」表达，因为空串在 JSON 里会被省略，
// 前端拿到 undefined 时无法区分「后端说没事」与「后端漏了这个字段」。
const (
	// ScriptNoticeNone 表示无需展示：仍在运行，或正常到寿。
	ScriptNoticeNone = "none"
	// ScriptNoticeInfo 表示展示为普通说明：管理员撤销、历史遗留行。
	ScriptNoticeInfo = "info"
	// ScriptNoticeWarning 表示需要用户注意：心跳中断。
	//
	// 三档里只有它代表「异常且需要用户行动」——用户得去看客户端是否崩溃/断网。
	// 其余两种情况用户知道了也不会做什么，所以不该抢注意力。
	ScriptNoticeWarning = "warning"
)

// ScriptRevokeNoticeInput 是判定所需的全部输入（纯数据，便于单测）。
type ScriptRevokeNoticeInput struct {
	// RevokedAt 为空表示**没有撤销**（仍在使用 / 正常到寿）。
	RevokedAt *time.Time
	// RevokedReason 为空有两种可能：
	//   - 没撤销（RevokedAt 也为空）
	//   - 历史遗留：本列加入之前就撤销的会话（RevokedAt 非空）
	RevokedReason string
	// ExpiresAt 是 150 分钟硬期限，用于区分「在跑」与「到寿」。
	ExpiresAt time.Time
	// Now 显式传入而不是内部取 time.Now()：便于确定性断言，
	// 也避免「测试写完后几小时自己变红」那类时钟腐烂。
	Now time.Time
}

// ScriptRevokeNotice 是给前端的展示结论。
type ScriptRevokeNotice struct {
	Level   string `json:"level"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// BuildScriptRevokeNotice 把会话状态翻译成通知区的一条提示（或"无需提示"）。
//
// # 判定顺序（顺序本身就是需求）
//
//	1. 没有 revoked_at      → none（在跑或到寿，都正常）
//	2. reason == heartbeat  → warning + 指向客户端的文案
//	3. reason == user_closed→ none（用户自己关的，不该再提醒他）
//	4. reason == admin      → info（不是你自己关的，需要知道）
//	5. 其它（空/未知）      → info，且**原样保留 reason**
//
// # 第 5 条为什么不猜
//
// 空 reason 出现在「本列加入之前撤销的历史行」上，我们确实不知道原因。
// 未知字符串出现在「将来后端新增原因、前端还没升级」时。
// 两种情况都必须显示「已撤销」，但**不能**编一个原因 ——
// 编错的原因比没有原因更糟：用户会去排查一个不存在的问题。
// 所以 Reason 原样透传，由前端决定怎么措辞。
//
// # 为什么到寿不算撤销
//
// 150 分钟正常结束是最常见的结局。若每次都提示「你的令牌已失效」，
// 用户一天会看到十几次，很快学会无视整个通知区 —— 那时真出心跳中断
// 反而看不见了。通知区的价值在于**稀少而准确**。
func BuildScriptRevokeNotice(in ScriptRevokeNoticeInput) ScriptRevokeNotice {
	if in.RevokedAt == nil {
		return ScriptRevokeNotice{Level: ScriptNoticeNone}
	}
	switch in.RevokedReason {
	case entity.ScriptRevokeReasonHeartbeatTimeout:
		return ScriptRevokeNotice{
			Level:   ScriptNoticeWarning,
			Reason:  in.RevokedReason,
			Message: "答题令牌已撤销：客户端心跳中断。请检查考试客户端是否仍在运行（可能已崩溃或断网），然后重新获取命令启动。",
		}
	case entity.ScriptRevokeReasonUserClosed:
		// 用户主动关闭，无需再次提醒。
		return ScriptRevokeNotice{Level: ScriptNoticeNone}
	case entity.ScriptRevokeReasonAdminRevoked:
		return ScriptRevokeNotice{
			Level:   ScriptNoticeInfo,
			Reason:  in.RevokedReason,
			Message: "本次答题令牌已被管理员撤销。",
		}
	default:
		// 历史遗留行或本端未识别的新值：如实说「已撤销」，不猜原因。
		return ScriptRevokeNotice{
			Level:   ScriptNoticeInfo,
			Reason:  in.RevokedReason,
			Message: "本次答题令牌已撤销。",
		}
	}
}
