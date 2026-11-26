package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// TicketImageModerator 是**可注入**的敏感图内容审核通道。
//
// 为什么是函数类型而不是直接把网络调用写进 StoreTicketAttachmentWithDB：
// 与 TicketRefundOrderVerifier、uapi 的客户端注入同款理由 —— 审核判定必须能在
// 测试里确定性地复现（计数器断言「关闭时零外呼」、断言「违规打标」、断言
// 「审核没跑成则拒绝入库」），而这三件事都无法对着真实上游做。
//
// 通道只回答「上游说了什么」，**不回答「要不要拒绝用户」**：拒绝与否由
// moderateTicketAttachmentImage 的三态决策决定。这样「判定表」（judgeTicketAttachmentScan）
// 与「判定结果如何使用」彻底分离，改策略不用碰上游协议。
type TicketImageModerator func(ctx context.Context, data []byte, mimeType string) (TicketImageModeration, error)

// TicketImageModeration 是一次内容审核的判定结果。
//
// Reason 只在**拒绝/打标**时有意义，会原样拼进面向用户的提示；不得放上游原始
// 响应体（可能含模型细节），只放可解释的一句话。
type TicketImageModeration struct {
	Approved bool
	Reason   string
}

// TicketImageModerationState 是上传前同步审核的**三态**结论。
//
// 刻意用独立类型而不是 error 兼表业务语义：三态里只有 unavailable 是「出错」，
// flagged 是**成功判定出的业务结果**（图片确实违规），把它塞进 error 会让调用方
// 分不清「上游挂了」和「上游说这图违规」——而这两者的处理完全相反
// （前者拒绝入库，后者照常落库只打标）。
type TicketImageModerationState string

const (
	// TicketImageModerationPass 审核明确说正常 → 放行落库，scan_state 保持 pending，
	// 由 #32 的异步扫描继续复核（同步审核只做「提前拦截」，不取代异步链路）。
	TicketImageModerationPass TicketImageModerationState = "pass"
	// TicketImageModerationFlagged 审核明确判违规 → **照常落库**，scan_state 直接置
	// sensitive。用户端隐藏、管理端可见（复用 #32 已有的展示链路）。
	//
	// 为什么不拒绝入库：工单系统必须允许用户上传敏感图片与敏感词 —— 误判时用户
	// 仍要能开单说明情况，把图拦在门外等于让人无法申辩。拦截的责任交给展示层
	// （ticketAttachmentVisible 只放行 approved），不是上传层。
	TicketImageModerationFlagged TicketImageModerationState = "flagged"
	// TicketImageModerationUnavailable 审核**没跑成**（未配置通道 / 调用失败 /
	// 上游给了中间结论）→ **拒绝入库**，fail-closed。
	//
	// 这一类必须与 flagged 严格区分：放行等于审核可以被绕过（把上游打挂就能传图）。
	TicketImageModerationUnavailable TicketImageModerationState = "unavailable"
)

// TicketImageModerationDecision 是一次同步审核的完整结论。
//
// 只有 unavailable 会带 Reason 之外的后果（拒绝入库）；pass / flagged 都返回 nil error
// 让落库流程继续。
type TicketImageModerationDecision struct {
	State TicketImageModerationState
	// Reason 是给用户看的中文说明，已截断；不得含上游原始响应体。
	Reason string
}

// unavailableError 把 unavailable 决策翻译成调用方要返回的错误。
//
// 用方法而不是让 moderate 直接返回 error：三态本身不带 error，只有调用方需要
// 「面向用户的一句话」时才在这里拼。这样 moderate 的返回值是**纯业务结论**。
func (d TicketImageModerationDecision) unavailableError() error {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "审核结论不可用"
	}
	return fmt.Errorf("%w: %s", ErrTicketImageModerationUnavailable, reason)
}

const (
	// TicketImageModerationEnabledSettingKey 是内容审核的开关键。
	// 与其它商业化开关一样存在 system_settings，默认 false（缺失即关闭）。
	TicketImageModerationEnabledSettingKey = "ticket_image_moderation_enabled"
	// ticketImageModerationTimeout 单次审核外呼的超时。
	//
	// 上传路径**同步**调用审核（与后台 worker 不同）：只有在上传时就拿到判定，
	// 才能让违规图「落库即打标、用户端立刻隐藏」。所以这个值要短于用户可接受的
	// 等待，也不能短到把正常的 2 MiB 图片上传判成超时。
	ticketImageModerationTimeout = 15 * time.Second
)

var (
	// ErrTicketImageModerationUnavailable 表示审核**没跑成**（未配置 / 调用失败 / 超时 /
	// 结论不可用）。对应 fail-closed：**拒绝入库**，而不是放行。
	//
	// 注意它与「判定违规」不是一回事：违规图照常落库只打标，不会返回这个错误。
	ErrTicketImageModerationUnavailable = errors.New("图片内容审核暂时不可用")
)

// ticketImageModeratorOverride 只给测试注入**假通道**用。
//
// 生产恒为 nil，此时走 uapi 实现。测试用它
// 装「永远返回违规」「永远报错」以及纯计数器（验证关闭时零外呼）。
var ticketImageModeratorOverride TicketImageModerator

// TicketImageModerationEnabledWithDB 读开关。缺失 / 空值 / 无法解析 ⇒ false。
//
// 这是**默认关闭**的唯一实现位置：uapi 的 /image/nsfw 是计费的 POST，默认关闭
// 意味着升级上来的部署在管理员显式打开之前**一次外呼都不会发**。
func TicketImageModerationEnabledWithDB(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, TicketImageModerationEnabledSettingKey, ""))
	return strings.EqualFold(raw, "true") || raw == "1"
}

// SetTicketImageModerationEnabledWithDB 写开关。enabled 为 nil ⇒ 不修改（*bool 语义），
// 与 UpdateTicketWecomSettingsWithDB 的输入结构一致。
func SetTicketImageModerationEnabledWithDB(db *gorm.DB, enabled *bool) error {
	if db == nil {
		return errors.New("数据库未初始化")
	}
	if enabled == nil {
		return nil
	}
	return saveSystemSettingValue(db, TicketImageModerationEnabledSettingKey,
		map[bool]string{true: "true", false: "false"}[*enabled], "是否启用工单图片内容审核")
}

// moderateTicketAttachmentImage 在上传路径上同步做一次内容审核，返回**三态**结论。
//
// 调用方（StoreTicketAttachmentWithDB）的处置规则：
//   - pass        → 继续落库，scan_state 保持 pending（异步扫描继续复核）
//   - flagged     → 继续落库，scan_state 直接置 sensitive（用户端隐藏、管理端可见）
//   - unavailable → **拒绝入库**，返回 decision.unavailableError()（fail-closed）
//
// 返回决策而不是 error：三态里只有 unavailable 是失败，flagged 是成功判定出的
// 业务结果。用 error 兼表两者会让调用方分不清「上游挂了」和「这图确实违规」，
// 而这两者的处置恰好相反。
//
// 审核的是**重编码后**的字节（safeTicketAttachmentImage 的产物），而不是原始上传
// 字节：这样「藏在 EXIF/尾部」的东西在送给上游之前就已经被剥掉了，上游看到的
// 与用户最终能看到的不是同一份内容时，判定就没有意义。
func moderateTicketAttachmentImage(ctx context.Context, db *gorm.DB, ticketID int64, data []byte, mimeType string) TicketImageModerationDecision {
	trace := startImageDetectionTrace("upload", HashSensitiveImage(data), mimeType, int64(len(data)), 0, ticketID)
	unavailable := func(code string) TicketImageModerationDecision {
		trace.fail(code)
		reason := "审核调用失败"
		if code == imageDetectorUnconfigured {
			reason = "未配置图片审核通道"
		}
		return TicketImageModerationDecision{State: TicketImageModerationUnavailable, Reason: reason}
	}
	if ticketImageModeratorOverride != nil {
		verdict, err := ticketImageModeratorOverride(ctx, data, mimeType)
		if err != nil {
			return unavailable(trace.upstreamFailureCode(err))
		}
		if verdict.Approved && strings.TrimSpace(verdict.Reason) != "" {
			return unavailable(imageResponseVerdictMissing)
		}
		if verdict.Approved {
			trace.write("detect_result", "pass", "-")
			return TicketImageModerationDecision{State: TicketImageModerationPass}
		}
		trace.write("detect_result", "flagged", "-")
		return TicketImageModerationDecision{State: TicketImageModerationFlagged, Reason: "上游判定为敏感图片"}
	}
	scan, reason, code := fetchTicketAttachmentScan(ctx, GetUAPIAPIKeyWithDB(imageDetectionDB(db)), trace, data, mimeType, ticketImageModerationTimeout)
	if code != "" {
		return unavailable(code)
	}
	if scan.State == entity.TicketAttachmentScanSensitive {
		trace.write("detect_result", "flagged", "-")
		return TicketImageModerationDecision{State: TicketImageModerationFlagged, Reason: reason}
	}
	trace.write("detect_result", "pass", "-")
	return TicketImageModerationDecision{State: TicketImageModerationPass}
}
