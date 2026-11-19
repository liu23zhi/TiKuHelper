package service

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTicketRefundInvalid             = errors.New("退款申请参数无效")
	ErrTicketRefundForbidden           = errors.New("无权操作退款工单")
	ErrTicketRefundNotFound            = errors.New("退款工单不存在")
	ErrTicketRefundOrderUnavailable    = errors.New("充值订单不可用于此次退款")
	ErrTicketRefundState               = errors.New("当前退款状态不允许执行该操作")
	ErrTicketRefundAmount              = errors.New("退款金额不合法或超出授权上限")
	ErrTicketRefundInsufficientBalance = errors.New("可用钱包余额不足，未冻结退款")
	// ErrTicketRefundExceedsRefundablePrincipal 可退本金不足：
	// 用户已用赠送金额消费掉的服务价值超过了剩余充值本金，超出部分不能退成现金。
	ErrTicketRefundExceedsRefundablePrincipal = errors.New("可退本金不足（赠送金额已消费的部分不能退成现金）")
	// ErrTicketRefundPayoutQrRequired 人工打款必须提供收款码。
	//
	// 2026-11-19 用户要求：「如果是不能原路退回的，一定要上传付款码」。
	// 理由：不能原路退回意味着钱只能人工打给用户，没有收款码管理员就无从转账 ——
	// 只会把申请卡在「已通过审核、无法打款」的状态。
	ErrTicketRefundPayoutQrRequired = errors.New("人工打款需先上传收款码")
)

const (
	TicketRefundKindOriginal = entity.TicketRefundKindOriginal
	TicketRefundKindManual   = entity.TicketRefundKindManual

	TicketRefundStatusPendingReview = entity.TicketRefundStatusPendingReview
	TicketRefundStatusRejected      = entity.TicketRefundStatusRejected
	TicketRefundStatusFrozen        = entity.TicketRefundStatusFrozen
	TicketRefundStatusProcessing    = entity.TicketRefundStatusProcessing
	TicketRefundStatusUnknown       = entity.TicketRefundStatusUnknown
	TicketRefundStatusSucceeded     = entity.TicketRefundStatusSucceeded
	TicketRefundStatusFailed        = entity.TicketRefundStatusFailed

	// A local cancellation proves that the sole dispatch claim was never
	// committed. This is not interchangeable with a gateway failure.
	TicketRefundOutcomeUnsentReference = "local-cancel-unsent"

	// 2026-11-19 用户拍板：可退窗口从 60 天改为 **6 个月**。
	//
	// 原值 60 天让「充值半年内但超过两个月」的订单直接不出现在可退列表里，
	// 用户看到的现象是「明明有这笔已支付订单，却没有退款按钮」，且没有任何
	// 解释。用户的诉求原文是「时间不超过 6 个月」。
	//
	// 用 183 天（6 个月按 30.5 天/月）而不是自然月：可退窗口是**滑动**的，
	// 自然月会让「2 月充值、8 月申请」这类边界随年份浮动，测试也无法钉住。
	// 183 天与 6 个月的最大偏差是 2 天，对退款业务无实质影响。
	ticketRefundAge             = 183 * 24 * time.Hour
	ticketRefundMicrosPerCent   = int64(10_000)
	ticketRefundMaxWalletMicros = int64(999_999_999_999_999_999)
	ticketRefundMaxCents        = ticketRefundMaxWalletMicros / ticketRefundMicrosPerCent

	ticketRefundLedgerFreeze  = "freeze"
	ticketRefundLedgerConsume = "consume"
	ticketRefundLedgerRelease = "release"
)

// TicketRefundOrderVerifier checks an already-verified original merchant
// association locally. It must not perform an external request in a DB
// transaction. Return an opaque stable channel ref; empty ref and
// ErrTicketRefundOrderUnavailable are fail-closed nonmatches.
type TicketRefundOrderVerifier func(*gorm.DB, entity.RechargeOrder) (string, error)

type TicketRefundOrder struct {
	ID                        int32     `json:"id"`
	OrderNo                   string    `json:"orderNo"`
	PaidCents                 int64     `json:"paidCents"`
	PaidAt                    time.Time `json:"paidAt"`
	PaymentMethodNameSnapshot string    `json:"paymentMethodNameSnapshot"`
	// OriginalSupported 表示这笔订单**可以走原路退回**（渠道指纹与原交易号齐全
	// 且渠道校验通过）。false 表示只能走人工退款 —— 前端据此在选项上标注，
	// 并禁止把它当成「原路退回」来提交。
	//
	// 为什么需要显式区分：2026-11-18 之前这类订单被**直接过滤掉**，
	// 用户看到的是「明明有订单，下拉里却一条都没有」，且没有任何提示。
	// 订单确实存在、钱确实收了，只是拿不到渠道凭证 —— 那是「只能人工」，
	// 不是「不存在」。
	OriginalSupported bool `json:"originalSupported"`
	// OriginalUnsupportedReason 说明「为什么不能原路退回」。
	//
	// 2026-11-19 用户要求：选择账单时，不能原路退款的账单也要显示，并写原因。
	//
	// 为什么需要文字原因而不是只给一个布尔：用户看到「仅人工退款」这四个字
	// 只会困惑「凭什么」，而真实原因可能差很远 —— 「渠道没配」是管理员该处理的，
	// 「这笔单的回调没带流水号」是数据问题，两者对用户的意义完全不同。
	// 空白表示可以原路退回。
	OriginalUnsupportedReason string `json:"originalUnsupportedReason,omitempty"`
}

// TicketRefundOrderAllocation 是「某一笔订单退多少」。
//
// 2026-11-19 用户要求：申请金额可以跨多笔订单分摊。例如退 10 元、手上有两笔
// 各 7.5 元的充值 —— 第一笔全额 7.5、第二笔部分 2.5。这在旧的「所有订单同额」
// 模型下表达不出来（RequestedCents 是共享的一个值）。
type TicketRefundOrderAllocation struct {
	OrderID int32 `json:"orderId"`
	Cents   int64 `json:"cents"`
}

type CreateTicketRefundInput struct {
	Kind    string `json:"kind"`
	OrderID *int32 `json:"orderId"`
	// OrderIDs 是多选提交（2026-11-18）。
	//
	// 为什么要单独一个字段而不是把 OrderID 改成数组：
	// ticket_refunds.order_id 上有 uniqueIndex（一个订单只能被一张退款单引用，
	// 这是防重复退款的硬约束），所以「多选」在数据层**必然是多张退款单**。
	// 保留 OrderID 让老的单选调用方（含测试）一字不改，新路径用 OrderIDs。
	//
	// 语义：OrderIDs 非空时优先，忽略 OrderID；每个订单各生成一张退款单。
	OrderIDs       []int32 `json:"orderIds"`
	RequestedCents int64   `json:"requestedCents"`
	Reason         string  `json:"reason"`
	// PayoutQrAttachmentID 是用户上传的收款码附件（ticket_attachments.id）。
	//
	// 2026-11-19 用户要求：「即便可以原路退回也可以要求用户设置退款账号」——
	// 所以它不是必填，而是**有机会就带上**：原路退回失败或走人工打款时，
	// 管理员据此扫码转账。
	//
	// 校验规则：给了就必须 > 0；真正的归属校验在绑定附件时做
	// （BindDraftTicketAttachmentsInTx 会核对 ticket_id=0 且 user_id 匹配），
	// 这里不做额外查询 —— 那会与事务内的检查重复，且事务外读有 TOCTOU 风险。
	PayoutQrAttachmentID *int64 `json:"payoutQrAttachmentId"`
	// Allocations 是每笔订单各自的退款金额（2026-11-19）。
	//
	// 语义分支：
	//   · 非空 → 以它为准，OrderIDs 与 RequestedCents 被忽略（分摊模式）
	//   · 空   → 沿用 OrderIDs + RequestedCents（所有订单同额，向后兼容）
	//
	// 为什么不把 OrderIDs 直接改成 [{orderId, cents}]：OrderIDs 是老字段，
	// 单笔调用方（含大量测试）用的是 OrderID，另一批用 OrderIDs。
	// 一次性改结构会把两条老路径全部打断，而它们都还对。
	Allocations []TicketRefundOrderAllocation `json:"allocations"`
}

func ListEligibleTicketRefundOrdersWithDB(db *gorm.DB, actor entity.User, verify TicketRefundOrderVerifier) ([]TicketRefundOrder, error) {
	if db == nil {
		return nil, ErrTicketRefundInvalid
	}
	if _, err := ticketRefundActor(db, actor.ID, false, false); err != nil {
		return nil, err
	}
	return listEligibleTicketRefundOrders(db, actor.ID, verify, time.Now(), 50)
}

func ListTicketRefundsWithDB(db *gorm.DB, actor entity.User) ([]entity.TicketRefund, error) {
	if db == nil {
		return nil, ErrTicketRefundInvalid
	}
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return nil, err
	}
	items := make([]entity.TicketRefund, 0)
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{})
	if !isAdminUser(fresh) {
		query = query.Where("user_id = ?", fresh.ID)
	}
	if err := query.Order("id DESC").Limit(100).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func GetTicketRefundWithDB(db *gorm.DB, actor entity.User, id int64) (entity.TicketRefund, error) {
	if db == nil || id <= 0 {
		return entity.TicketRefund{}, ErrTicketRefundNotFound
	}
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return entity.TicketRefund{}, err
	}
	query := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id)
	if !isAdminUser(fresh) {
		query = query.Where("user_id = ?", fresh.ID)
	}
	var row entity.TicketRefund
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.TicketRefund{}, ErrTicketRefundNotFound
		}
		return entity.TicketRefund{}, err
	}
	return row, nil
}

func CreateTicketRefundWithDB(db *gorm.DB, actor entity.User, input CreateTicketRefundInput, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	if db == nil || (input.Kind != entity.TicketRefundKindOriginal && input.Kind != entity.TicketRefundKindManual) ||
		(input.OrderID != nil && *input.OrderID <= 0) ||
		(input.Kind == entity.TicketRefundKindOriginal && input.OrderID == nil) ||
		!utf8.ValidString(input.Reason) || len(input.Reason) > 2000 || utf8.RuneCountInString(input.Reason) > 500 {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	if !validTicketRefundCents(input.RequestedCents) {
		return entity.TicketRefund{}, ErrTicketRefundAmount
	}
	// 收款码附件可选：给了就必须是正数 ID。
	//
	// 这里只做「形态」校验，真正的归属校验（ticket_id=0 且属于本人）在
	// 壳工单创建的绑定步骤里由 BindDraftTicketAttachmentsInTx 完成 ——
	// 在此处另查一次既重复又有 TOCTOU 风险（查到之后仍可能被别人用掉）。
	if input.PayoutQrAttachmentID != nil && *input.PayoutQrAttachmentID <= 0 {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	now := time.Now()
	row := entity.TicketRefund{
		UserID: actor.ID, Kind: input.Kind, Status: entity.TicketRefundStatusPendingReview,
		RequestedCents: input.RequestedCents, Reason: strings.TrimSpace(input.Reason), RefundNo: "tr-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now,
		PayoutQrAttachmentID: input.PayoutQrAttachmentID,
	}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var order entity.RechargeOrder
		if input.OrderID != nil {
			// Match the recharge callback's lock order: order, then user.
			var err error
			order, err = lockTicketRefundOrder(tx, actor.ID, *input.OrderID)
			if err != nil {
				return err
			}
		}
		if _, err := ticketRefundActor(tx, actor.ID, false, true); err != nil {
			return err
		}
		if input.OrderID == nil {
			eligible, err := listEligibleTicketRefundOrders(tx, actor.ID, verify, now, 1)
			if err != nil {
				return err
			}
			if len(eligible) != 0 {
				return ErrTicketRefundOrderUnavailable
			}
		} else {
			if err := ensureTicketRefundOrderUnused(tx, order.ID); err != nil {
				return err
			}
			paid, err := ticketRefundPaidOrderCents(order)
			if err != nil || order.PaidAt.After(now) {
				return ErrTicketRefundOrderUnavailable
			}
			// 金额上限（2026-11-19 用户要求：「用户申请退款的金额必须小于等于本金」）：
			//   上限 = min(本笔实付, 当前可退本金)
			//
			// 只卡「不超过实付」是不够的：用户可能已经把余额花掉了，
			// 这时再按订单实付放行，会在审批冻结时才失败 —— 那时用户已经
			// 填完表单、等了几分钟，体验与「提交时说清楚」差很远。
			//
			// 可退本金沿用 21-*.md §7.1 的口径（balance − 已消耗赠金），
			// 与审批冻结时用的是同一个函数，避免两处口径漂移。
			amountCap := paid
			if breakdown, principalErr := RefundablePrincipalWithDB(tx, actor.ID, paid*ticketRefundMicrosPerCent); principalErr == nil {
				// 可退本金**向上取整到分**（2026-11-19 用户要求）。
				//
				// 与订单金额的 floor 方向相反是刻意的：本金是用户自己的钱，
				// 差 1 分不到就少退一分属于「平台占用户便宜」；而订单金额向下取
				// 是「不超额退款」。两个方向各自对自己的风险负责。
				principalCents := refundablePrincipalCentsCeil(breakdown)
				if principalCents < amountCap {
					amountCap = principalCents
				}
			}
			// 本金为负或已花光时 amountCap 会是 0 或负数：一并拒掉，
			// 否则会放过一个「退 0 元」的申请。
			if amountCap <= 0 || input.RequestedCents > amountCap {
				return ErrTicketRefundAmount
			}
			ref, verifyErr := ticketRefundOriginalChannel(tx, order, verify, now)
			if input.Kind == entity.TicketRefundKindOriginal {
				if verifyErr != nil {
					return verifyErr
				}
				row.ChannelRef, row.OriginalTradeNo = ref, order.RefundOriginalTradeNo
			} else if verifyErr == nil {
				// Do not silently replace an eligible original-channel refund
				// with a manual payout.
				return ErrTicketRefundOrderUnavailable
			} else if !errors.Is(verifyErr, ErrTicketRefundOrderUnavailable) {
				return verifyErr
			}
			id := order.ID
			row.OrderID, row.OrderNo, row.OrderPaidCents = &id, order.OrderNo, paid
		}
		// The nullable unique order ID also enforces this on SQLite if a
		// concurrent request races the locking-read semantics.
		if err := tx.Create(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrTicketRefundOrderUnavailable
			}
			return err
		}
		// T-209：同一事务里建壳工单，让这笔退款立刻出现在工单列表里。
		// 与退款落库同事务是硬要求：退款成功但壳工单没建 = 该退款在列表里永久消失。
		return CreateTicketRefundShellInTx(tx, row, now)
	})
	if err != nil {
		return entity.TicketRefund{}, err
	}
	// 回读刚写入的 ticket_id（CreateTicketRefundShellInTx 用的是 UpdateColumn，
	// 只改库不影响内存里的 row）。
	if linked, err := readTicketRefundTicketID(db, row.ID); err == nil {
		row.TicketID = linked
	}
	return row, nil
}

// CreateTicketRefundsForOrdersWithDB 是多选提交的入口：一笔订单一张退款单。
//
// 为什么不是「一张退款单挂多个订单」：
// ticket_refunds.order_id 上有 uniqueIndex —— 一个订单只能被一张退款单引用，
// 这是防重复退款的硬约束（同笔充值退两次就是事故）。所以多选在数据层
// 必然展开成多张退款单，每张各自走原来的单笔流程。
//
// 事务语义：**全成或全败**。任一订单不可退（已退过 / 渠道校验失败 /
// 金额超实付）就整批回滚 —— 部分成功会让用户以为「选了 3 笔退了 3 笔」，
// 实际只退了 1 笔，对账时极难发现。
//
// 返回按入参顺序排列的退款行，调用方据此展示「哪些已提交」。
// plannedRefund 是一条已归一的「哪笔订单退多少」。
type plannedRefund struct {
	orderID int32
	cents   int64
}

// planTicketRefundAllocations 把两种入参形态归一成同一份计划。
//
// 为什么要归一：分摊模式与同额模式在这里分叉，下游只保留**一条**路径。
// 两条并行的建单路径迟早会漂移（已有的教训：只改一条会让另一条静默取错值）。
//
// 去重策略在两种模式下刻意不同：
//
//	· OrderIDs 重复 → 静默去重（同额无歧义，而唯一索引报错会指向用户没做过的事）
//	· Allocations 重复 → **拒绝**（同一订单两个金额是真矛盾，静默选一个会让用户看到自己没填的数）
func planTicketRefundAllocations(input CreateTicketRefundInput) ([]plannedRefund, error) {
	if len(input.Allocations) > 0 {
		seen := make(map[int32]struct{}, len(input.Allocations))
		plan := make([]plannedRefund, 0, len(input.Allocations))
		for _, alloc := range input.Allocations {
			if alloc.OrderID <= 0 {
				return nil, ErrTicketRefundInvalid
			}
			if alloc.Cents <= 0 {
				return nil, ErrTicketRefundAmount
			}
			if _, dup := seen[alloc.OrderID]; dup {
				return nil, ErrTicketRefundInvalid
			}
			seen[alloc.OrderID] = struct{}{}
			plan = append(plan, plannedRefund{orderID: alloc.OrderID, cents: alloc.Cents})
		}
		return plan, nil
	}

	if len(input.OrderIDs) == 0 {
		return nil, ErrTicketRefundInvalid
	}
	seen := make(map[int32]struct{}, len(input.OrderIDs))
	plan := make([]plannedRefund, 0, len(input.OrderIDs))
	for _, id := range input.OrderIDs {
		if id <= 0 {
			return nil, ErrTicketRefundInvalid
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		plan = append(plan, plannedRefund{orderID: id, cents: input.RequestedCents})
	}
	return plan, nil
}

// refundablePrincipalCentsCeil 把可退本金换算成“分”并**向上取整**。
//
// 向上是用户要求（2026-11-19）：本金是用户自己的钱，差 1 分不到就少退一分
// 属于平台占用户便宜。与订单金额的 floor 方向相反是刻意的。
//
// 抽成函数而不在两处各写一遍：单笔闸门与批次闸门必须同口径，
// 否则会出现「单笔放行、批次拒绝」这种用户无法理解的分裂。
func refundablePrincipalCentsCeil(breakdown RefundablePrincipalBreakdown) int64 {
	cents := breakdown.RefundableMicros / ticketRefundMicrosPerCent
	if breakdown.RefundableMicros%ticketRefundMicrosPerCent != 0 {
		cents++
	}
	return cents
}

func CreateTicketRefundsForOrdersWithDB(db *gorm.DB, actor entity.User, input CreateTicketRefundInput, verify TicketRefundOrderVerifier) ([]entity.TicketRefund, error) {
	plan, err := planTicketRefundAllocations(input)
	if err != nil {
		return nil, err
	}
	// 批次级本金校验（2026-11-19）。
	//
	// 为什么单笔闸门不够：每笔各自去比对「本详单实付 与 用户可退本金」，
	// 而本金是**用户级的一个池子**。本金 8 元、两笔各申请 7.5 与 2.5 时，
	// 单笔看都在额度内（7.5≤8、2.5≤8），合计 10 却超了 —— 这正是无单可查的那种漏洞。
	if len(plan) > 1 {
		total := int64(0)
		for _, item := range plan {
			total += item.cents
		}
		if breakdown, principalErr := RefundablePrincipalWithDB(db, actor.ID, 0); principalErr == nil {
			if total > refundablePrincipalCentsCeil(breakdown) {
				return nil, ErrTicketRefundAmount
			}
		}
	}

	created := make([]entity.TicketRefund, 0, len(plan))
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		for _, item := range plan {
			one := input
			one.OrderIDs = nil
			one.Allocations = nil
			orderID := item.orderID
			one.OrderID = &orderID
			// 每笔各自的金额覆盖入参：这正是分摊模式的语义。
			one.RequestedCents = item.cents
			// 借用单笔入口的完整校验与记账，保证两条路径口径一致。
			// 注意它内部会开自己的事务 —— GORM 的嵌套 Transaction 会退化成
			// SavePoint，外层回滚时一并撤销，所以「全败」语义仍然成立。
			row, err := CreateTicketRefundWithDB(tx, actor, one, verify)
			if err != nil {
				return err
			}
			created = append(created, row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// readTicketRefundTicketID 读回某笔退款的壳工单 ID（未关联返回 nil）。
func readTicketRefundTicketID(db *gorm.DB, refundID int64) (*int64, error) {
	if db == nil || refundID <= 0 {
		return nil, ErrTicketRefundInvalid
	}
	var row struct {
		TicketID *int64 `gorm:"column:ticket_id"`
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).
		Select("ticket_id").Where("id = ?", refundID).Take(&row).Error; err != nil {
		return nil, err
	}
	return row.TicketID, nil
}

// ReviewTicketRefundWithDB takes an atomic wallet hold. For an orderless
// manual request the administrator must review the private evidence before
// approving; no channel or payout is called from this method.
func ReviewTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, approve bool, amountCents int64, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	if (!approve && amountCents != 0) || (approve && !validTicketRefundCents(amountCents)) {
		return entity.TicketRefund{}, ErrTicketRefundAmount
	}
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Status != entity.TicketRefundStatusPendingReview {
			if (!approve && row.Status == entity.TicketRefundStatusRejected) ||
				(approve && row.ApprovedCents == amountCents && row.ReviewedAt != nil) {
				return nil
			}
			return ErrTicketRefundState
		}
		now := time.Now()
		if !approve {
			return updateTicketRefund(tx, row, map[string]any{
				"status": entity.TicketRefundStatusRejected, "reviewed_at": now, "reviewed_by": admin.ID,
				"resolved_at": now, "resolved_by": admin.ID,
			})
		}
		if amountCents > row.RequestedCents || (row.OrderID != nil && amountCents > row.OrderPaidCents) {
			return ErrTicketRefundAmount
		}
		switch row.Kind {
		case entity.TicketRefundKindOriginal:
			if err := verifyTicketRefundOriginalOrderInTx(tx, *row, verify); err != nil {
				return err
			}
		case entity.TicketRefundKindManual:
			if row.OrderID != nil {
				order, err := lockTicketRefundOrder(tx, row.UserID, *row.OrderID)
				if err != nil {
					return err
				}
				paid, err := ticketRefundPaidOrderCents(order)
				if err != nil || paid != row.OrderPaidCents || order.OrderNo != row.OrderNo {
					return ErrTicketRefundOrderUnavailable
				}
			}
		default:
			return ErrTicketRefundInvalid
		}
		wallet, err := lockTicketRefundWallet(tx, row.UserID)
		if err != nil {
			return err
		}
		if !wallet.enabled {
			return ErrTicketRefundForbidden
		}
		// 可退本金上限：用户只能退回「自己充值的、且还没被消费掉的本金」。
		//
		// 这一道闸堵的是 21-*.md §7.1 意图 (b) 的漏洞：
		// 充 100 → 赠 20 → 花掉 20 赠金 → 余额仍是 100 → 退 100 → 平台白送 20 元服务。
		// 口径推导与「为什么用 balance − 已消耗赠金」见 ticket_refund_refundable.go 顶部注释。
		//
		// 用**锁内**的权威钱包快照（wallet.micros）配账单聚合，避免读到过期余额。
		giftConsumed, err := giftConsumedMicrosWithDB(tx, row.UserID)
		if err != nil {
			return err
		}
		orderPaidMicros := row.OrderPaidCents * ticketRefundMicrosPerCent
		breakdown := computeRefundablePrincipal(wallet.micros, giftConsumed, orderPaidMicros)
		refundableCents := microsToCents(breakdown.RefundableMicros)
		if amountCents > refundableCents {
			return ErrTicketRefundExceedsRefundablePrincipal
		}
		amountMicros := amountCents * ticketRefundMicrosPerCent
		if wallet.micros < amountMicros {
			return ErrTicketRefundInsufficientBalance
		}
		after := wallet.micros - amountMicros
		if err := updateTicketRefundWallet(tx, row.UserID, wallet, after); err != nil {
			return err
		}
		if err := insertTicketRefundLedger(tx, *row, admin.ID, ticketRefundLedgerFreeze, amountMicros, wallet.micros, after); err != nil {
			return err
		}
		return updateTicketRefund(tx, row, map[string]any{
			"status": entity.TicketRefundStatusFrozen, "approved_cents": amountCents, "reviewed_at": now, "reviewed_by": admin.ID,
			// 审计：把「因已消耗赠金而扣掉的可退额度」写进申请单，事后可对账。
			// 没有它，用户只会看到「我充了 100 却只能退 80」，无法解释。
			"reason": appendTicketRefundPrincipalAudit(row.Reason, breakdown, refundableCents),
		})
	})
}

// BeginTicketRefundWithDB grants the right to submit a single original
// channel request. A process loss leaves processing/unknown recoverable via
// inquiry only; it never allows a blind second submission.
func BeginTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Status != entity.TicketRefundStatusFrozen || row.Kind != entity.TicketRefundKindOriginal {
			return ErrTicketRefundState
		}
		if err := verifyTicketRefundOriginalOrderInTx(tx, *row, verify); err != nil {
			return err
		}
		if _, err := ticketRefundHoldMicros(tx, *row); err != nil {
			return err
		}
		return updateTicketRefund(tx, row, map[string]any{"status": entity.TicketRefundStatusProcessing, "started_at": time.Now()})
	})
}

// CancelUnsentTicketRefundWithDB releases an original-channel hold only
// before a dispatch claim exists. The lock and state CAS compete with Begin:
// cancellation is never available for processing or unknown outcomes.
func CancelUnsentTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64) (entity.TicketRefund, error) {
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Kind != entity.TicketRefundKindOriginal || row.StartedAt != nil {
			return ErrTicketRefundState
		}
		if row.Status == entity.TicketRefundStatusFailed && row.OutcomeReference == TicketRefundOutcomeUnsentReference {
			return nil
		}
		if row.Status != entity.TicketRefundStatusFrozen {
			return ErrTicketRefundState
		}
		amountMicros, err := ticketRefundHoldMicros(tx, *row)
		if err != nil {
			return err
		}
		return finalizeTicketRefundInTx(tx, row, admin.ID, entity.TicketRefundStatusFailed,
			TicketRefundOutcomeUnsentReference, amountMicros)
	})
}

// ReconcileTicketRefundWithDB applies evidence, not an instruction to pay.
// Original-channel outcomes must come from the caller's verified channel
// response or inquiry. Manual terminal outcomes are staff confirmations
// of an out-of-band payment or its definitive failure.
func ReconcileTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, outcome string, reference ...string) (entity.TicketRefund, error) {
	if outcome != entity.TicketRefundStatusUnknown && outcome != entity.TicketRefundStatusSucceeded && outcome != entity.TicketRefundStatusFailed {
		return entity.TicketRefund{}, ErrTicketRefundState
	}
	ref := ""
	if len(reference) > 1 {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	if len(reference) == 1 {
		ref = strings.TrimSpace(reference[0])
		if ref != "" && !validTicketRefundReference(ref, 128) {
			return entity.TicketRefund{}, ErrTicketRefundInvalid
		}
	}
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Status == entity.TicketRefundStatusSucceeded || row.Status == entity.TicketRefundStatusFailed {
			if row.Status == outcome && (ref == "" || ref == row.OutcomeReference) {
				return nil
			}
			return ErrTicketRefundState
		}
		if row.Kind != entity.TicketRefundKindOriginal && row.Kind != entity.TicketRefundKindManual {
			return ErrTicketRefundInvalid
		}
		if row.Status != entity.TicketRefundStatusProcessing && row.Status != entity.TicketRefundStatusUnknown &&
			!(row.Kind == entity.TicketRefundKindManual && row.Status == entity.TicketRefundStatusFrozen) {
			return ErrTicketRefundState
		}
		amountMicros, err := ticketRefundHoldMicros(tx, *row)
		if err != nil {
			return err
		}
		if outcome == entity.TicketRefundStatusUnknown {
			if row.Status == outcome {
				return nil
			}
			return updateTicketRefund(tx, row, map[string]any{"status": outcome})
		}
		return finalizeTicketRefundInTx(tx, row, admin.ID, outcome, ref, amountMicros)
	})
}

func finalizeTicketRefundInTx(tx *gorm.DB, row *entity.TicketRefund, operatorID int32, outcome, ref string, amountMicros int64) error {
	// A disabled account does not invalidate a payout that already
	// happened, nor stop a definitive failure from restoring its hold.
	wallet, err := lockTicketRefundWallet(tx, row.UserID)
	if err != nil {
		return err
	}
	after, kind := wallet.micros, ticketRefundLedgerConsume
	if outcome == entity.TicketRefundStatusFailed {
		kind = ticketRefundLedgerRelease
		if wallet.micros > ticketRefundMaxWalletMicros-amountMicros {
			return ErrTicketRefundAmount
		}
		after += amountMicros
		if err := updateTicketRefundWallet(tx, row.UserID, wallet, after); err != nil {
			return err
		}
	}
	if err := insertTicketRefundLedger(tx, *row, operatorID, kind, amountMicros, wallet.micros, after); err != nil {
		return err
	}
	// 退款成功 → 冲回该订单产生的邀请返佣（同一事务，可追踪）。
	// 已 settled 的记录会从受益人**赠送余额**扣回（gift_balance，clamp 到 0，
	// 差额写进冲回流水备注）；未 settled 的只改状态。balance（可退本金）不受影响。
	if outcome == entity.TicketRefundStatusSucceeded && row.OrderID != nil && *row.OrderID > 0 {
		if _, err := ReverseReferralRebateForOrderInTx(tx, *row.OrderID, "原订单退款，返佣已冲回"); err != nil {
			return err
		}
	}
	return updateTicketRefund(tx, row, map[string]any{
		"status": outcome, "resolved_at": time.Now(), "resolved_by": operatorID, "outcome_reference": ref,
	})
}

func listEligibleTicketRefundOrders(db *gorm.DB, userID int32, verify TicketRefundOrderVerifier, now time.Time, want int) ([]TicketRefundOrder, error) {
	items := make([]TicketRefundOrder, 0)
	// verify == nil 表示支付渠道未配置（LoadTicketRefundEPayConfigWithDB 失败）。
	// 此时原路退回路走不通，但**订单本身仍要列出来**并标成只能人工退款 ——
	// 否则用户看到的是「有订单但列表全空」，而真实原因是渠道没配。
	// ticketRefundOriginalChannel 内部已经对 verify == nil 返回 Unavailable，
	// 所以这里不需要额外分支，让它自然落到 OriginalSupported=false 即可。
	var lastID int32
	for {
		var orders []entity.RechargeOrder
		// 2026-11-18：不再用 SQL 条件把「缺渠道指纹」的订单挡掉。
		// 把订单挡在 SQL 层。这类订单「已支付、钱已收，但拿不到渠道凭证」，
		// 正确处置是**列出来并标记只能人工退款**，而不是让它凭空消失 ——
		// 后者让用户看到「明明有订单却一条都选不了」且毫无解释。
		// 2026-11-19 用户拍板的四项退款资格条件，能下推到 SQL 的都下推
		// （paid_at 有索引，其余三项是列值非空判断，成本可忽略）。
		// 未在这里下推的两项在 Go 层用 ticketRefundOrderMeetsBasicCriteria 兜底：
		//   · 金额解析（decimal → 分）无法在 SQL 里安全表达；
		//   · SQL 与 Go 两处条件必须一致，Go 层是唯一真相，SQL 只是预筛。
		//
		// 第 5 条「每个订单只能退款一次」由下面的 NOT EXISTS 保证 ——
		// 它比在 Go 层查一遍更可靠：并发两次提交时由数据库做最终裁决。
		query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RechargeOrder{}).
			Where("user_id = ? AND status = ? AND payment_provider = ?", userID, rechargeOrderStatusPaid, DefaultPaymentProvider).
			Where("paid_at >= ? AND paid_at <= ?", now.Add(-ticketRefundAge), now).
			Where("order_no <> ''").
			Where("payment_method_name_snapshot <> ''").
			Where("third_party_order_no <> ''").
			Where("NOT EXISTS (SELECT 1 FROM ticket_refunds WHERE ticket_refunds.order_id = recharge_orders.id)")
		if lastID != 0 {
			query = query.Where("id < ?", lastID)
		}
		if err := query.Order("id DESC").Limit(64).Find(&orders).Error; err != nil {
			return nil, err
		}
		for _, order := range orders {
			// Go 层兜底：SQL 只做预筛，四项条件的**唯一真相**在这里。
			// 两处不一致时以 Go 为准（SQL 可能因为字段类型/空串语义差异放过一条），
			// 而且「能不能退」这个判断会被 CreateTicketRefundWithDB 复用，
			// 单一实现才不会出现「列表说能退、提交时说不能退」。
			if !ticketRefundOrderMeetsBasicCriteria(order, now) {
				continue
			}
			// 渠道校验失败只影响「能否原路退回」，不再影响「是否出现在列表里」。
			//
			// 但**真故障**仍要上抛：ticketRefundOriginalChannel 返回非 Unavailable 的
			// 错误意味着渠道调用本身炸了（网络/签名/下游 5xx），这时静默降级成
			// 「人工退款」会让用户以为系统正常、实际是原路通道坏了。所以保留 err 分支。
			// 2026-11-19：同时取「不能原路退回的原因」。
			// 真故障（err != nil）必须上抛，不能降级成人工退款 ——
			// 那会让「原路通道坏了」看起来像「这笔单本来就不能原路退」。
			originalOK := true
			originalReason := ""
			if _, reason, err := ticketRefundOriginalChannelDetail(db, order, verify, now); err != nil {
				return nil, err
			} else if reason != "" {
				originalOK = false
				originalReason = reason
			}
			paid, centsErr := ticketRefundPaidOrderCents(order)
			if centsErr != nil {
				// 金额解析不出来说明这条订单数据本身坏了（金额为空/非法），
				// 列出来也没法填退款金额，跳过。
				continue
			}
			item := TicketRefundOrder{
				ID: order.ID, OrderNo: order.OrderNo, PaidCents: paid, PaidAt: *order.PaidAt,
				PaymentMethodNameSnapshot: order.PaymentMethodNameSnapshot,
				OriginalSupported:         originalOK,
				OriginalUnsupportedReason: originalReason,
			}
			items = append(items, item)
			if len(items) == want {
				return items, nil
			}
		}
		if len(orders) < 64 {
			return items, nil
		}
		lastID = orders[len(orders)-1].ID
	}
}

// ticketRefundOrderMeetsBasicCriteria 判「这笔订单是否具备退款资格」的四项硬条件。
//
// 2026-11-19 用户明确列出（原文）：
//  1. 时间不超过 6 个月
//  2. 有订单号
//  3. 有支付方式
//  4. 有第三方单号
//  5. 第一次退款，每个订单只能退款一次
//
// 第 5 条不在这里判 —— 它由 ticket_refunds.order_id 的唯一索引 + 调用方的
// ensureTicketRefundOrderUnused 保证，放在这里会与事务外读产生 TOCTOU。
//
// 这四项决定「能不能退」，与「能不能原路退回」是两件事：
// 缺渠道凭证的订单仍可退（走人工打款），所以它们在 listEligible 里会以
// OriginalSupported=false 出现，而不是被过滤掉。
func ticketRefundOrderMeetsBasicCriteria(order entity.RechargeOrder, asOf time.Time) bool {
	if _, err := ticketRefundPaidOrderCents(order); err != nil {
		// 金额解析不出来 = 这条订单数据本身坏了，无法确定退款额度。
		return false
	}
	// ① 时间不超过 6 个月，且不在未来（未来的 paid_at 说明回调数据有误）。
	if order.PaidAt == nil || order.PaidAt.Before(asOf.Add(-ticketRefundAge)) || order.PaidAt.After(asOf) {
		return false
	}
	// ② 有订单号。
	if strings.TrimSpace(order.OrderNo) == "" {
		return false
	}
	// ③ 有支付方式。注意这是**下单时快照**的展示名（如「支付宝」），
	// 与 ④ 的第三方单号是不同来源：前者给用户看，后者给渠道对账。
	if strings.TrimSpace(order.PaymentMethodNameSnapshot) == "" {
		return false
	}
	// ④ 有第三方单号 —— 这是支付渠道返回的流水号，退款时用它定位原交易。
	//
	// ⚠️ 历史坑：这里原来只认 refund_original_trade_no（退款专用列，
	// 由支付回调额外写入），而前端「第三方单号」列展示的是 third_party_order_no。
	// 两者是不同的列，于是出现过「表里明明有第三方单号、却没有退款按钮」的
	// 现象（订单 5）。现在按用户口径统一到 third_party_order_no。
	if strings.TrimSpace(order.ThirdPartyOrderNo) == "" {
		return false
	}
	return true
}

// ticketRefundOriginalChannel 判断这笔订单能否**原路退回**，能则返回渠道引用。
//
// 与 ticketRefundOrderMeetsBasicCriteria 的分工：
// 前者答「能不能退」，这里答「能不能原路退」。后者是前者的子集 ——
// 原路退回额外要求渠道指纹与校验器，缺了就只能人工打款。
// ticketRefundOriginalChannelDetail 判断原路退回能力，并给出**不能退的原因**。
//
// 返回三元组，语义互斥：
//
//	ref != ""    → 可原路退回，ref 是渠道引用
//	reason != "" → 不可原路退回，reason 是给人看的原因（可走人工打款）
//	err != nil   → 真故障（渠道调用炸了），调用方必须上抛而不是降级成人工
//
// 为什么要把「原因」和「故障」分开：降级成人工打款是**正常业务分支**，
// 而渠道 5xx 是**故障**。混在一起会让「原路通道坏了」被悄悄当成
// 「这笔单本来就不能原路退」，运维永远发现不了。
func ticketRefundOriginalChannelDetail(db *gorm.DB, order entity.RechargeOrder, verify TicketRefundOrderVerifier, asOf time.Time) (string, string, error) {
	if !ticketRefundOrderMeetsBasicCriteria(order, asOf) {
		return "", "该订单不满足退款资格（超期或缺订单要素），无法原路退回", nil
	}
	if order.PaymentProvider != DefaultPaymentProvider {
		return "", "该订单的支付渠道不受支持，无法原路退回", nil
	}
	if verify == nil {
		return "", "支付渠道当前未配置，暂时无法原路退回", nil
	}
	// 渠道指纹由支付回调写入。为空说明回调没带上，或这笔单来自更早的版本。
	if !validTicketRefundReference(order.RefundChannelFingerprint, 255) {
		return "", "该订单缺少渠道校验信息（支付回调未写入），无法原路退回", nil
	}
	// 原交易流水号同样来自回调，退款时用它定位原交易。
	if !validTicketRefundReference(order.RefundOriginalTradeNo, 128) {
		return "", "该订单缺少原交易流水号（支付回调未写入），无法原路退回", nil
	}
	ref, err := verify(db, order)
	if err != nil {
		// 渠道明确说「不可用」→ 这是业务分支，给原因，不上抛。
		if errors.Is(err, ErrTicketRefundOrderUnavailable) {
			return "", "渠道校验未通过，无法原路退回", nil
		}
		// 其余（网络/签名/下游 5xx）是真故障。
		return "", "", err
	}
	if !validTicketRefundReference(ref, 255) {
		return "", "渠道未返回可用的退款凭据，无法原路退回", nil
	}
	return ref, "", nil
}

func ticketRefundOriginalChannel(db *gorm.DB, order entity.RechargeOrder, verify TicketRefundOrderVerifier, asOf time.Time) (string, error) {
	ref, reason, err := ticketRefundOriginalChannelDetail(db, order, verify, asOf)
	if err != nil {
		return "", err
	}
	if reason != "" {
		return "", ErrTicketRefundOrderUnavailable
	}
	return ref, nil
}

func verifyTicketRefundOriginalOrderInTx(tx *gorm.DB, row entity.TicketRefund, verify TicketRefundOrderVerifier) error {
	if row.OrderID == nil || row.CreatedAt.IsZero() {
		return ErrTicketRefundOrderUnavailable
	}
	order, err := lockTicketRefundOrder(tx, row.UserID, *row.OrderID)
	if err != nil {
		return err
	}
	ref, err := ticketRefundOriginalChannel(tx, order, verify, row.CreatedAt)
	if err != nil {
		return err
	}
	paid, _ := ticketRefundPaidOrderCents(order)
	if row.ChannelRef != ref || row.OrderNo != order.OrderNo || row.OriginalTradeNo != order.RefundOriginalTradeNo ||
		row.OrderPaidCents != paid {
		return ErrTicketRefundOrderUnavailable
	}
	return nil
}

func ticketRefundPaidOrderCents(order entity.RechargeOrder) (int64, error) {
	v := order.Amount
	if order.Status != rechargeOrderStatusPaid || order.PaidAt == nil || order.PaidAt.IsZero() ||
		!validTicketRefundReference(order.OrderNo, 64) || math.IsNaN(v) || math.IsInf(v, 0) ||
		v <= 0 || v > float64(ticketRefundMaxCents)/100 {
		return 0, ErrTicketRefundOrderUnavailable
	}
	// 超过两位小数的订单金额**向下取整到分**（2026-11-19 用户要求）。
	//
	// 原实现用 math.Round（四舍五入）**并且**要求金额恰好等于「分」的整数倍
	// （`v != float64(cents)/100` 即判不可用）。那等于把「18.005」这类
	// decimal(18,6) 订单直接排除在退款之外 —— 用户看到的是「有订单但不可退」，
	// 而真实原因是金额精度，没有任何提示。
	//
	// 现在是 floor：平台不会因为四舍五入多退给用户（这正是用户选「向下」的原因）。
	//
	// +1e-6 的用途是抵消浮点表示误差：0.29*100 = 28.999999999999996，
	// 直接 floor 会得到 28（少一分）。schema 是 decimal(18,6)，真实值最多
	// 6 位小数，乘 100 后最多 4 位小数，所以 1e-6 的量级既能修掉表示误差，
	// 又不可能把一个真正的 .9999 推过整数边界。
	cents := int64(math.Floor(v*100 + 1e-6))
	if !validTicketRefundCents(cents) {
		return 0, ErrTicketRefundOrderUnavailable
	}
	return cents, nil
}

func validTicketRefundCents(cents int64) bool {
	return cents > 0 && cents <= ticketRefundMaxCents
}

func validTicketRefundReference(raw string, max int) bool {
	if raw == "" || len(raw) > max || raw != strings.TrimSpace(raw) || !utf8.ValidString(raw) {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func lockTicketRefundOrder(tx *gorm.DB, userID, id int32) (entity.RechargeOrder, error) {
	var order entity.RechargeOrder
	err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", id, userID).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.RechargeOrder{}, ErrTicketRefundOrderUnavailable
	}
	return order, err
}

func ensureTicketRefundOrderUnused(tx *gorm.DB, orderID int32) error {
	var row entity.TicketRefund
	err := tx.Session(&gorm.Session{NewDB: true}).Select("id").Where("order_id = ?", orderID).Take(&row).Error
	if err == nil {
		return ErrTicketRefundOrderUnavailable
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func ticketRefundActor(db *gorm.DB, id int32, requireAdmin, lock bool) (entity.User, error) {
	if id <= 0 {
		return entity.User{}, ErrTicketRefundForbidden
	}
	var user entity.User
	query := db.Session(&gorm.Session{NewDB: true}).Select("id", "role", "status").Where("id = ?", id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.User{}, ErrTicketRefundForbidden
		}
		return entity.User{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") || (requireAdmin && !isAdminUser(user)) {
		return entity.User{}, ErrTicketRefundForbidden
	}
	return user, nil
}

func changeTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, mutate func(*gorm.DB, *entity.TicketRefund) error) (entity.TicketRefund, error) {
	if db == nil || id <= 0 {
		return entity.TicketRefund{}, ErrTicketRefundNotFound
	}
	var row entity.TicketRefund
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, admin.ID, true, false); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketRefundNotFound
			}
			return err
		}
		return mutate(tx, &row)
	})
	if err != nil {
		return entity.TicketRefund{}, err
	}
	// T-209：退款状态迁移成功后，把展示口径同步到壳工单。
	// **在事务之外**做：退款事务里已经在写钱包 hold 与 ledger，再塞一次 tickets 更新
	// 会把账务事务的失败面扩大（壳工单写失败会让打款回滚，那是最不该发生的事）。
	// 因此这里尽力而为，失败只记日志，下一次迁移或回填会修正。
	syncTicketRefundShellStatusInTx(db, row)
	return row, nil
}

func updateTicketRefund(tx *gorm.DB, row *entity.TicketRefund, updates map[string]any) error {
	result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).
		Where("id = ? AND status = ?", row.ID, row.Status).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTicketRefundState
	}
	return tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", row.ID).Take(row).Error
}

type ticketRefundWallet struct {
	raw     string
	micros  int64
	enabled bool
}

func lockTicketRefundWallet(tx *gorm.DB, userID int32) (ticketRefundWallet, error) {
	// SQL decimal text and verified write-back preserve the wallet's sixth
	// decimal on supported databases; float64 is not a ledger unit.
	var value struct {
		BalanceText sql.NullString
		Status      string
	}
	expr := "CAST(balance AS CHAR) AS balance_text"
	if tx.Dialector.Name() == "sqlite" {
		expr = "CAST(balance AS TEXT) AS balance_text"
	}
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).Select(expr+", status").
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).Take(&value).Error
	if err != nil {
		return ticketRefundWallet{}, err
	}
	if !value.BalanceText.Valid {
		return ticketRefundWallet{}, ErrTicketRefundAmount
	}
	// Reuse decimal arithmetic only, not the unrelated script escrow model,
	// release path, or transaction journal.
	micros, err := parseScriptEscrowMicros(value.BalanceText.String)
	if err != nil {
		return ticketRefundWallet{}, ErrTicketRefundAmount
	}
	return ticketRefundWallet{
		raw: value.BalanceText.String, micros: micros,
		enabled: strings.EqualFold(strings.TrimSpace(value.Status), "enabled"),
	}, nil
}

func updateTicketRefundWallet(tx *gorm.DB, userID int32, before ticketRefundWallet, after int64) error {
	if after < -ticketRefundMaxWalletMicros || after > ticketRefundMaxWalletMicros {
		return ErrTicketRefundAmount
	}
	result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Where("id = ? AND balance = ?", userID, before.raw).Update("balance", formatScriptEscrowMicros(after))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTicketRefundState
	}
	fresh, err := lockTicketRefundWallet(tx, userID)
	if err != nil {
		return err
	}
	if fresh.micros != after {
		return ErrTicketRefundAmount
	}
	return nil
}

func insertTicketRefundLedger(tx *gorm.DB, row entity.TicketRefund, operatorID int32, kind string, amount, before, after int64) error {
	return tx.Session(&gorm.Session{NewDB: true}).Create(&entity.TicketRefundLedger{
		RefundID: row.ID, UserID: row.UserID, Kind: kind, AmountMicros: amount,
		WalletBeforeMicros: before, WalletAfterMicros: after, OperatorID: operatorID,
	}).Error
}

func ticketRefundHoldMicros(tx *gorm.DB, row entity.TicketRefund) (int64, error) {
	if !validTicketRefundCents(row.ApprovedCents) || row.ApprovedCents > row.RequestedCents ||
		(row.OrderID != nil && row.ApprovedCents > row.OrderPaidCents) {
		return 0, ErrTicketRefundAmount
	}
	amount := row.ApprovedCents * ticketRefundMicrosPerCent
	var held entity.TicketRefundLedger
	err := tx.Session(&gorm.Session{NewDB: true}).Where("refund_id = ? AND kind = ?", row.ID, ticketRefundLedgerFreeze).Take(&held).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrTicketRefundState
	}
	if err != nil {
		return 0, err
	}
	if held.UserID != row.UserID || held.AmountMicros != amount || held.WalletBeforeMicros-held.WalletAfterMicros != amount {
		return 0, ErrTicketRefundState
	}
	return amount, nil
}

// LatestRefundPayoutQrAttachmentIDWithDB 取该用户最近一次退款申请用过的收款码附件 ID。
//
// 用途（2026-11-19 用户要求）：「提交的时候输入密码才能看到之前上传的收款码；
// 替换不需要。添加本地缓存，如果是本机上传的，直接加载，换了设备才需要密码
// 来恢复数据。」
//
// 所以本函数只负责「把上次用的那张找出来」，**鉴权在调用方**：
// controller 会先校验当前密码（并复用登录失败限流），通过后才调这里。
// 把密码校验放进 service 会让「查数据」和「验身份」耦合，且难以单独测试。
//
// 返回 0 表示该用户还没用过收款码（不是错误）—— 调用方据此提示「暂无记录」，
// 而不是让用户以为密码错了。
func LatestRefundPayoutQrAttachmentIDWithDB(db *gorm.DB, userID int32) (int64, error) {
	if db == nil || userID <= 0 {
		return 0, ErrTicketRefundInvalid
	}
	var row struct{ PayoutQrAttachmentID *int64 }
	// 只认已经提交过的退款单：草稿附件（未关联任何退款单）不算「上次用过」，
	// 否则用户上传后放弃提交，下次换设备也会把它当历史带出来 —— 那是误导。
	err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).
		Select("payout_qr_attachment_id").
		Where("user_id = ? AND payout_qr_attachment_id IS NOT NULL AND payout_qr_attachment_id > 0", userID).
		Order("id DESC").Limit(1).Scan(&row).Error
	if err != nil {
		return 0, err
	}
	if row.PayoutQrAttachmentID == nil {
		return 0, nil
	}
	return *row.PayoutQrAttachmentID, nil
}
