package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 邀请码与返佣的哨兵错误。
//
// 这些是**预期业务结果**（用户填错码、重复结算），调用方按 4xx 处理即可，
// 不要写 error 日志。
var (
	// ErrReferralCodeInvalid 邀请码不存在或格式不合法。
	ErrReferralCodeInvalid = errors.New("邀请码无效")
	// ErrReferralSelfInvite 用自己的邀请码注册自己。
	ErrReferralSelfInvite = errors.New("不能使用自己的邀请码")
	// ErrReferralAlreadyBound 账户已经绑定过邀请人（注册后不可改绑）。
	ErrReferralAlreadyBound = errors.New("该账户已绑定邀请人，不能再次绑定")
	// ErrReferralRebateNotFound 返佣记录不存在。
	ErrReferralRebateNotFound = errors.New("返佣记录不存在")
	// ErrReferralRebateState 返佣记录当前状态不允许该操作。
	ErrReferralRebateState = errors.New("返佣记录状态不允许该操作")
	// ErrReferralRebateDuplicate 同一受益人、同一金额、同一补录日期的手动记录已存在。
	//
	// 这是**幂等闸**而不是数据损坏：管理员重复点提交时必须明确告知「这笔已经记过」，
	// 而不是静默返回一条旧记录 —— 后者会让管理员以为刚记的是另一笔，接着去核对钱包。
	ErrReferralRebateDuplicate = errors.New("该受益人在该日期已有同金额的手动返佣记录")
	// ErrReferralRebateManualInvalid 手动补录的入参不合法（金额/日期）。
	ErrReferralRebateManualInvalid = errors.New("返佣补录参数无效")
	// ErrReferralRebateSchemaUnavailable 结算/冲回需要的表或列还没迁移到位。
	//
	// 与创建返佣时的「静默降级」不同：结算是管理员显式发钱的动作，
	// 结构没准备好时必须明确报错（503），不能假装成功。
	ErrReferralRebateSchemaUnavailable = errors.New("邀请返佣结算所需的表结构未就绪")
)

// referralCodeLength 是邀请码长度。
const referralCodeLength = 8

// referralCodeAlphabet 去掉易混字符 0/O/1/I/L 后的 31 个字符。
//
// 邀请码是要念给朋友、手抄在纸上的东西，0 与 O、1 与 I 与 L 的误读成本
// 远高于多几个字符带来的熵收益。
const referralCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// referralCodeMaxAttempts 是邀请码唯一冲突时的最大重试次数。
// 31^8 ≈ 8.5e11，正常情况下一次就成；重试只为兜住极端碰撞。
const referralCodeMaxAttempts = 8

// ReferralSummary 是「我的邀请」页的汇总。
//
// 全部字段按**本人**口径：InvitedCount 只数直接邀请的一级账户，
// 金额只统计 pending/settled（reversed 已冲回，不计入应付）。
type ReferralSummary struct {
	// Enabled 表示已配置且启用了自动返佣规则；邀请码不依赖规则开关。
	Enabled bool `json:"enabled"`
	// Code 是本人邀请码，开关关闭且从未生成过时为空字符串。
	Code string `json:"code"`
	// Rate 是旧版响应兼容字段；分档政策没有单一比例。
	Rate float64 `json:"rate"`
	// InvitedCount 是按账户计的一级受邀人数，不按 Key 或设备数累加。
	InvitedCount int64 `json:"invitedCount"`
	// RebateCount 是本人作为受益人的返佣记录条数（含已冲回）。
	RebateCount int64 `json:"rebateCount"`
	// PendingAmount 是待结算金额合计。
	PendingAmount float64 `json:"pendingAmount"`
	// SettledAmount 是已结算金额合计。
	SettledAmount float64 `json:"settledAmount"`
	// ReversedAmount 是已冲回金额合计（仅作对账展示，不计入应付）。
	ReversedAmount float64 `json:"reversedAmount"`
	// TotalBaseAmount 是产生返佣的实付本金合计（不含赠金）。
	TotalBaseAmount float64 `json:"totalBaseAmount"`
}

// ReferralRebateListQuery 是返佣列表的筛选条件。
type ReferralRebateListQuery struct {
	// Status 精确匹配 pending / settled / reversed。
	Status string
	// InviterUserID 精确匹配受益人。
	InviterUserID int32
	// InvitedUserID 精确匹配受邀人。
	InvitedUserID int32
	// OrderNo 精确匹配原充值订单号（不做模糊匹配，避免全表扫描）。
	OrderNo string
}

// ReferralRebateListItem 是返佣列表的一行，带上双方用户名便于对账。
type ReferralRebateListItem struct {
	entity.ReferralRebate
	InviterUsername string `json:"inviterUsername"`
	InvitedUsername string `json:"invitedUsername"`
	NetAmount       string `json:"netAmount" gorm:"column:net_amount"`
}

// ReferralRebateListSummary 是返佣列表的金额统计。
//
// 与列表共用同一套筛选条件，避免「筛选后列表只剩一行但合计还是全量」。
type ReferralRebateListSummary struct {
	PendingAmount   float64 `json:"pendingAmount"`
	SettledAmount   float64 `json:"settledAmount"`
	ReversedAmount  float64 `json:"reversedAmount"`
	TotalBaseAmount float64 `json:"totalBaseAmount"`
}

type ReferralRebateListResult struct {
	Data     []ReferralRebateListItem  `json:"data"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"pageSize"`
	Summary  ReferralRebateListSummary `json:"summary"`
}

// generateReferralCode 用 crypto/rand 生成一个邀请码。
//
// 用 crypto/rand 而不是 math/rand：邀请码是公开可枚举的字符串，
// 但一旦被猜到就能把别人的新注册归因到自己名下，没必要留可预测性。
func generateReferralCode() (string, error) {
	var builder strings.Builder
	builder.Grow(referralCodeLength)
	limit := big.NewInt(int64(len(referralCodeAlphabet)))
	for i := 0; i < referralCodeLength; i++ {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		builder.WriteByte(referralCodeAlphabet[index.Int64()])
	}
	return builder.String(), nil
}

// normalizeReferralCode 规范化用户输入的邀请码：去空白、转大写。
func normalizeReferralCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// referralSession 返回一个「干净语句」的会话。
//
// 为什么必须这样：邀请/返佣的入口经常是 `dao.User.UnderlyingDB()`（注册事务）
// 或 `dao.*` 派生出来的会话，其 Statement 里还挂着上一个模型（例如 User）——
// 直接 `db.Where("code = ?").Take(&ReferralInvite{})` 会生成
// `SELECT users.id, users.user_id ... FROM users` 这种跨表查询（实测报
// `no such column: users.user_id`）。NewDB 清掉继承的模型，同时保留事务连接。
func referralSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// EnsureReferralInviteWithDB 保证账户有邀请码并返回它。
//
// 语义：
//   - 已有邀请码 → 原样返回（幂等，绝不重新生成，避免已分享出去的码失效）；
//   - 没有 → 生成一个唯一码并插入；唯一冲突时重试。
//
// 只在总开关开启时由注册链路调用；开关关闭时不产生任何行。
func EnsureReferralInviteWithDB(db *gorm.DB, userID int32) (entity.ReferralInvite, error) {
	if db == nil || userID <= 0 {
		return entity.ReferralInvite{}, ErrReferralCodeInvalid
	}
	var existing entity.ReferralInvite
	err := referralSession(db).Model(&entity.ReferralInvite{}).Where("user_id = ?", userID).Take(&existing).Error
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.ReferralInvite{}, err
	}

	var lastErr error
	for attempt := 0; attempt < referralCodeMaxAttempts; attempt++ {
		code, err := generateReferralCode()
		if err != nil {
			return entity.ReferralInvite{}, err
		}
		invite := entity.ReferralInvite{UserID: userID, Code: code}
		// DoNothing + RowsAffected==0：并发注册同一用户时不会因为唯一键冲突
		// 把整个注册事务打回滚（唯一键冲突跳过并完成）。
		result := referralSession(db).Clauses(clause.OnConflict{DoNothing: true}).Create(&invite)
		if result.Error != nil {
			return entity.ReferralInvite{}, result.Error
		}
		if result.RowsAffected == 0 {
			// 唯一键撞上了：可能是 code 撞了，也可能是这个用户已经被别人插了一行。
			// 两种情况都先回读一次，回读到就收工，回读不到就换码重试。
			var current entity.ReferralInvite
			if err := referralSession(db).Model(&entity.ReferralInvite{}).Where("user_id = ?", userID).Take(&current).Error; err == nil {
				return current, nil
			}
			lastErr = fmt.Errorf("邀请码生成冲突")
			continue
		}
		return invite, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("邀请码生成冲突")
	}
	return entity.ReferralInvite{}, lastErr
}

// ResolveReferralInviterWithDB 把用户填写的邀请码解析成邀请人 user_id。
//
// 找不到 → ErrReferralCodeInvalid（控制器映射 400「邀请码无效」）。
// 这里不判断「能不能绑」，只负责解析，绑定时序由 BindReferralInviterInTx 负责。
func ResolveReferralInviterWithDB(db *gorm.DB, code string) (entity.ReferralInvite, error) {
	normalized := normalizeReferralCode(code)
	if normalized == "" {
		return entity.ReferralInvite{}, ErrReferralCodeInvalid
	}
	var invite entity.ReferralInvite
	if err := referralSession(db).Model(&entity.ReferralInvite{}).Where("code = ?", normalized).Take(&invite).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.ReferralInvite{}, ErrReferralCodeInvalid
		}
		return entity.ReferralInvite{}, err
	}
	return invite, nil
}

// BindReferralInviterInTx 在注册事务内写入归因快照。
//
// 规则（对应 04 稿 3.6 节）：
//   - 按账户计，不按 Key 或设备数：一个账户只有一个 invited_by_user_id；
//   - 只在注册时生效，注册后不可改绑（已有绑定 → ErrReferralAlreadyBound）；
//   - 禁止自邀：邀请人就是本人时直接拒绝；
//   - 同一 IP 不单独定罪（校园 NAT / 家庭共用），但同一 phone / wechat 已绑定的
//     账户不能互邀 —— 那种情况下号本身已经是同一个人。
//
// 入参 invitedUser 必须已经由 db.Create 写过（有 ID），否则无法写归因列。
func BindReferralInviterInTx(tx *gorm.DB, invitedUser *entity.User, invite entity.ReferralInvite) error {
	if tx == nil || invitedUser == nil || invitedUser.ID <= 0 {
		return ErrReferralCodeInvalid
	}
	if invite.ID <= 0 || invite.UserID <= 0 {
		return ErrReferralCodeInvalid
	}
	// 表/列没就绪时无法写归因：按「邀请码无效」拒绝，让注册保持旧行为可继续。
	if !referralSchemaAvailable(tx) {
		return ErrReferralCodeInvalid
	}
	if invite.UserID == invitedUser.ID {
		return ErrReferralSelfInvite
	}
	if invitedUser.InvitedByUserID != 0 {
		if invitedUser.InvitedByUserID == invite.UserID {
			return nil
		}
		return ErrReferralAlreadyBound
	}

	// 邀请人必须是一个真实存在、且不是自己的账户。
	var inviter entity.User
	if err := referralSession(tx).Model(&entity.User{}).Select("id", "phone", "wechat_id").Where("id = ?", invite.UserID).Take(&inviter).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferralCodeInvalid
		}
		return err
	}
	if inviter.ID == invitedUser.ID {
		return ErrReferralSelfInvite
	}
	if err := ensureReferralPairNotSameIdentity(tx, inviter, *invitedUser); err != nil {
		return err
	}

	now := time.Now()
	claim := referralSession(tx).Model(&entity.User{}).Where("id = ? AND COALESCE(invited_by_user_id, 0) = 0", invitedUser.ID).
		Updates(map[string]any{
			"invited_by_user_id": invite.UserID,
			"invited_at":         &now,
		})
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected != 1 {
		return ErrReferralAlreadyBound
	}
	invitedUser.InvitedByUserID = invite.UserID
	invitedUser.InvitedAt = &now
	return nil
}

// ensureReferralPairNotSameIdentity 拒绝「同一个手机号/微信」的两个账户互邀。
//
// 说明：手机号与微信在 users 表上各自唯一，所以理论上不会出现同一手机号两行；
// 但历史上存在「先按微信注册、再绑手机」与「先按手机注册、再绑微信」两条路径，
// 这里仍然显式比较一次，把「同一个人换个入口又注册一个号」的情况挡在归因之外。
// 同一 IP 不在此列 —— 校园 NAT 与家庭共用 IP 是正常情况，不单独定罪。
func ensureReferralPairNotSameIdentity(_ *gorm.DB, inviter entity.User, invited entity.User) error {
	if inviter.Phone != nil && invited.Phone != nil &&
		strings.TrimSpace(*inviter.Phone) != "" &&
		strings.TrimSpace(*inviter.Phone) == strings.TrimSpace(*invited.Phone) {
		return ErrReferralSelfInvite
	}
	if inviter.WechatID != nil && invited.WechatID != nil &&
		strings.TrimSpace(*inviter.WechatID) != "" &&
		strings.TrimSpace(*inviter.WechatID) == strings.TrimSpace(*invited.WechatID) {
		return ErrReferralSelfInvite
	}
	return nil
}

// RegisterReferralInviteForNewUser 在注册事务里为新用户生成自己的邀请码。
//
// 邀请码属于账户标识，注册时始终生成；开关只控制返佣发放。
// 生成失败不阻断注册：邀请码是附加权益，不能让注册整体失败。
func RegisterReferralInviteForNewUser(tx *gorm.DB, userID int32) (entity.ReferralInvite, error) {
	return EnsureReferralInviteWithDB(tx, userID)
}

// referralSchemaAvailable 判断返佣相关表/列是否已就绪。
//
// 存在的意义：充值回调与退款链路是**既有核心路径**，不能因为返佣迁移还没跑过
// （老库升级、单测里的最小 schema）就整体报错。结构没准备好时一律降级为
// 「本次不产生/不冲回返佣」，绝不阻断充值与退款。
func referralSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameReferralRebate) {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameUser, "invited_by_user_id")
}

// CreateReferralRebateInTx snapshots the first matching explicit tier after
// the original order has a matching principal credit in the same transaction.
func CreateReferralRebateInTx(tx *gorm.DB, order entity.RechargeOrder) (*entity.ReferralRebate, error) {
	if tx == nil || order.ID <= 0 || order.UserID <= 0 || !referralSchemaAvailable(tx) {
		return nil, nil
	}
	policy, err := GetReferralAutoRulesWithDB(tx)
	if errors.Is(err, ErrReferralAutoRulesInvalid) {
		// Malformed optional policy must not pay a default rate or stop
		// the successful principal payment. Admin GET surfaces the error.
		logger.SysWarn("自动返佣规则无效，充值不产生自动返佣")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !policy.active() {
		return nil, nil
	}

	var credited struct {
		OrderNo string
		Amount  microColumn
	}
	if err := referralSession(tx).Table("recharge_orders").Select("order_no, amount").
		Where("id = ? AND user_id = ? AND status = ? AND "+entity.RechargeCreditEvidenceSQL, order.ID, order.UserID, rechargeOrderStatusPaid).
		Take(&credited).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !credited.Amount.known || credited.Amount.n <= 0 || credited.Amount.n > maxEffectiveMicros {
		return nil, nil
	}
	rate := policy.rateFor(credited.Amount.n)
	if rate == 0 {
		return nil, nil
	}
	amount := referralRatioMicros(credited.Amount.n, rate, referralRateScale)
	if amount == 0 {
		return nil, nil
	}

	var invited entity.User
	if err := referralSession(tx).Model(&entity.User{}).Select("id", "invited_by_user_id").Where("id = ?", order.UserID).Take(&invited).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if invited.InvitedByUserID <= 0 || invited.InvitedByUserID == invited.ID {
		return nil, nil
	}

	// Persist exact DECIMAL snapshots instead of rounding through float64.
	// The unique (source, recharge_order_id) key handles a replay.
	if err := referralSession(tx).Model(&entity.ReferralRebate{}).Clauses(clause.OnConflict{DoNothing: true}).Create(map[string]any{
		"source": entity.ReferralRebateSourceAuto, "inviter_user_id": invited.InvitedByUserID,
		"invited_user_id": invited.ID, "recharge_order_id": order.ID, "order_no": credited.OrderNo,
		"base_amount": fixedInt(credited.Amount.n, 6), "rate": fixedInt(rate, 8), "amount": fixedInt(amount, 6),
		"status": entity.ReferralRebateStatusPending, "policy_version": referralTierRebatePolicyVersion,
		"remark": "单笔有效充值产生待结算返佣", "created_at": time.Now(),
	}).Error; err != nil {
		return nil, err
	}
	var rebate entity.ReferralRebate
	err = referralSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("source = ? AND recharge_order_id = ?", entity.ReferralRebateSourceAuto, order.ID).Take(&rebate).Error
	if err != nil {
		return nil, err
	}
	return &rebate, nil
}

// ReverseReferralRebateForOrderInTx applies the original route's confirmed
// successful amount, not the requested amount, hold, or a manual order link.
// The current refund model admits a single confirmed refund per order.
// Serialize in the same order as settlement: inviter wallet, then rebate row.
func ReverseReferralRebateForOrderInTx(tx *gorm.DB, orderID int32, refundCents int64) (*entity.ReferralRebate, error) {
	if tx == nil || orderID <= 0 || !referralSchemaAvailable(tx) {
		return nil, nil
	}
	var head entity.ReferralRebate
	if err := referralSession(tx).Select("id", "inviter_user_id").
		Where("source = ? AND recharge_order_id = ?", entity.ReferralRebateSourceAuto, orderID).Take(&head).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if refundCents <= 0 || refundCents > maxReferralRuleCents {
		return nil, ErrReferralRebateState
	}
	wallet, err := lockReferralWalletInTx(tx, head.InviterUserID)
	noWallet := errors.Is(err, ErrReferralRebateNotFound)
	if err != nil && !noWallet {
		return nil, err
	}
	row, err := lockExactReferralRebateInTx(tx, head.ID)
	if err != nil {
		return nil, err
	}
	rebate := row.ReferralRebate
	if rebate.RefundedCents != 0 {
		if rebate.RefundedCents != refundCents {
			return nil, ErrReferralRebateState
		}
		return &rebate, nil
	}
	if rebate.Status == entity.ReferralRebateStatusReversed {
		// Historical full reversals are final, not replayed or backfilled.
		return &rebate, nil
	}
	if (rebate.Status != entity.ReferralRebateStatusPending && rebate.Status != entity.ReferralRebateStatusSettled) ||
		!row.ExactBase.known || !row.ExactAward.known || row.ExactBase.n <= 0 ||
		row.ExactAward.n < 0 || row.ExactAward.n > maxEffectiveMicros {
		return nil, ErrReferralRebateState
	}
	refundMicros := min(refundCents*ticketRefundMicrosPerCent, row.ExactBase.n)
	amountMicros := referralRatioMicros(row.ExactAward.n, refundMicros, row.ExactBase.n)
	amount := fixedFloat(fixedInt(amountMicros, 6))
	now := time.Now()
	remark := "原路实退，按比例取消待结算返佣"
	if rebate.Status == entity.ReferralRebateStatusSettled {
		if noWallet && amountMicros > 0 {
			// Refunding the payer must not resurrect an archived inviter or
			// depend on that wallet. Keep the gross reduction and actual zero.
			remark = fmt.Sprintf("原订单退款，受益人账户不可用，实际冲回 ¥0，差额 ¥%s 记为不可回收", referralMicrosText(amountMicros))
		} else if !noWallet {
			deducted, err := debitReferralRebateWalletInTx(tx, wallet, rebate, amountMicros)
			if err != nil {
				return nil, err
			}
			remark = buildReferralReversalRemark(amountMicros, deducted)
		}
	}
	if amountMicros >= row.ExactAward.n {
		rebate.Status = entity.ReferralRebateStatusReversed
	}
	if err := referralSession(tx).Model(&entity.ReferralRebate{}).Where("id = ?", rebate.ID).Updates(map[string]any{
		"status": rebate.Status, "refunded_cents": refundCents, "reversed_amount": fixedInt(amountMicros, 6), "reversed_at": &now, "remark": remark,
	}).Error; err != nil {
		return nil, err
	}
	rebate.RefundedCents = refundCents
	rebate.ReversedAmount = amount
	rebate.ReversedAt = &now
	rebate.Remark = remark
	return &rebate, nil
}

// buildReferralReversalRemark 生成冲回备注；赠金不足（clamp 到 0）时把差额如实写出来。
//
// 差额留痕是硬要求：应冲回 X 但只扣到 Y 时，不能静默吞掉 X-Y，
// 否则「这笔返佣作废了，钱却还在用户手上」这件事在对账里查不到。
func buildReferralReversalRemark(required, actual int64) string {
	if actual >= required {
		return "原订单退款，返佣已按实退比例冲回"
	}
	return fmt.Sprintf("原订单退款，返佣按比例冲回（应冲回 ¥%s，赠金余额不足实际冲回 ¥%s，差额 ¥%s 记为不可回收）",
		referralMicrosText(required), referralMicrosText(actual), referralMicrosText(required-actual))
}

func referralMicrosText(micros int64) string {
	return strings.TrimRight(strings.TrimRight(fixedInt(micros, 6), "0"), ".")
}

// moneyTextForReferral 把金额格式化成不带多余零的十进制文本（仅用于备注/日志）。
func moneyTextForReferral(amount float64) string {
	return strconv.FormatFloat(util.RoundMoney(amount), 'f', -1, 64)
}

// SettleReferralRebateWithDB 结算一条 pending 返佣：**改状态 + 入赠送余额，同一个事务**。
//
// 结算即入余额（16-*.md §2.5 / 用户 2026-11-09 两次修正）：
//  1. 锁 rebate 行（FOR UPDATE），校验 status ∈ {pending}；
//  2. **users.gift_balance += rebate.Amount**（赠送余额桶：可消费、不可退）；
//  3. billing_records 一条 type=referral_rebate，含本金桶 balanceBefore/balanceAfter
//     （结算不动本金桶，因此两者相等）与赠送桶前后快照；
//  4. rebate.status=settled、settled_at=now。
//
// **users.balance 一个字节都不动**：balance 是充值本金、可退款，返佣进这里
// 就等于把「送的钱」变成可退现金（A 邀 B、B 邀 C 层层套现）。
//
// 幂等（三道闸，任一命中都不会重复加钱）：
//   - 行锁（SELECT ... FOR UPDATE）串行化同一 rebate 的并发结算；
//   - 状态判断：已 settled → 直接返回成功，不再加钱；
//   - 流水兜底：request_id = referral_rebate_<id> 的结算流水已存在 → 只补状态，不加钱。
//
// 口径红线：返佣基数 BaseAmount 不动（只认实付本金，不含赠金/返现）。
func SettleReferralRebateWithDB(db *gorm.DB, admin entity.User, id int32) (entity.ReferralRebate, error) {
	if db == nil || id <= 0 {
		return entity.ReferralRebate{}, ErrReferralRebateNotFound
	}
	if !referralSettlementSchemaAvailable(db) {
		return entity.ReferralRebate{}, ErrReferralRebateSchemaUnavailable
	}
	var rebate entity.ReferralRebate
	err := db.Transaction(func(tx *gorm.DB) error {
		// 先无锁读一次只为拿到受益人 ID：锁顺序必须是 users → referral_rebates
		// （与退款冲回链路一致，见 ReverseReferralRebateForOrderInTx 的说明）。
		var head entity.ReferralRebate
		if err := referralSession(tx).Model(&entity.ReferralRebate{}).
			Select("id", "inviter_user_id").Where("id = ?", id).Take(&head).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReferralRebateNotFound
			}
			return err
		}
		wallet, err := lockReferralWalletInTx(tx, head.InviterUserID)
		if err != nil {
			return err
		}

		row, err := lockExactReferralRebateInTx(tx, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReferralRebateNotFound
		}
		if err != nil {
			return err
		}
		rebate = row.ReferralRebate
		switch rebate.Status {
		case entity.ReferralRebateStatusSettled:
			// 幂等：已经结算过，直接返回，**不重复加钱**。
			return nil
		case entity.ReferralRebateStatusReversed:
			// 冲回后不得再结算：钱已经退回去了，再结算等于凭空发钱。
			return ErrReferralRebateState
		case entity.ReferralRebateStatusPending:
		default:
			// 未知状态（历史脏数据 / 未来新状态）一律拒绝，不猜语义。
			return ErrReferralRebateState
		}

		if !row.ExactAward.known || !row.ExactReversed.known || row.ExactReversed.n < 0 ||
			row.ExactAward.n < row.ExactReversed.n || row.ExactAward.n > maxEffectiveMicros {
			return ErrReferralRebateState
		}
		amount := row.ExactAward.n - row.ExactReversed.n
		now := time.Now()
		remark := "管理员结算"
		if admin.Username != "" {
			remark = "管理员结算：" + admin.Username
		}

		if amount > 0 {
			if err := creditReferralRebateWalletInTx(tx, wallet, rebate, amount, remark); err != nil {
				return err
			}
		}

		if err := referralSession(tx).Model(&entity.ReferralRebate{}).Where("id = ?", rebate.ID).Updates(map[string]any{
			"status":     entity.ReferralRebateStatusSettled,
			"settled_at": &now,
			"remark":     remark,
		}).Error; err != nil {
			return err
		}
		rebate.Status = entity.ReferralRebateStatusSettled
		rebate.SettledAt = &now
		rebate.Remark = remark
		return nil
	})
	if err != nil {
		return entity.ReferralRebate{}, err
	}
	return rebate, nil
}

// referralSettlementSchemaAvailable 判断「结算/冲回」需要的表与列是否就绪。
//
// 比 referralSchemaAvailable 更严：结算要写钱包与账单，所以还要
// users.gift_balance 与 billing_records。缺任何一项都明确报错（503），
// 不能像「创建返佣」那样静默降级 —— 那会让管理员以为钱已经发了。
func referralSettlementSchemaAvailable(db *gorm.DB) bool {
	if !referralSchemaAvailable(db) {
		return false
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "gift_balance") {
		return false
	}
	return db.Migrator().HasTable(entity.TableNameBillingRecord)
}

// Keep the DECIMAL read under the same row lock as the write. A float64
// representation cannot preserve six places across the allowed money range.
type lockedReferralWallet struct {
	ID          int32
	Balance     microColumn
	GiftBalance microColumn
}

func lockReferralWalletInTx(tx *gorm.DB, userID int32) (lockedReferralWallet, error) {
	var wallet lockedReferralWallet
	err := referralSession(tx).Model(&entity.User{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "balance", "gift_balance").Where("id = ?", userID).Take(&wallet).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return wallet, ErrReferralRebateNotFound
	}
	if err != nil {
		return wallet, err
	}
	if !wallet.Balance.known || !wallet.GiftBalance.known || wallet.Balance.n < -maxEffectiveMicros ||
		wallet.Balance.n > maxEffectiveMicros || wallet.GiftBalance.n > maxEffectiveMicros {
		return wallet, ErrReferralRebateState
	}
	return wallet, nil
}

type exactReferralRebate struct {
	entity.ReferralRebate
	ExactBase     microColumn `gorm:"column:exact_base"`
	ExactAward    microColumn `gorm:"column:exact_award"`
	ExactReversed microColumn `gorm:"column:exact_reversed"`
}

func lockExactReferralRebateInTx(tx *gorm.DB, id int32) (exactReferralRebate, error) {
	var row exactReferralRebate
	err := referralSession(tx).Model(&entity.ReferralRebate{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("referral_rebates.*, base_amount AS exact_base, amount AS exact_award, reversed_amount AS exact_reversed").
		Where("id = ?", id).Take(&row).Error
	return row, err
}

// creditReferralRebateWalletInTx 把结算金额加进邀请人的**赠送余额**，并写一条结算流水。
//
// 钱包口径（2026-11-09 用户修正后的双桶模型）：
//   - `balance`      = **仅充值本金**，可退；返佣**不进**这一桶；
//   - `gift_balance` = 赠送金额，**可消费但不可退**；返佣进这里；
//   - 消费顺序：先扣 gift_balance，扣完再扣 balance（见消费链路）。
//
// 因此结算只写 gift_balance，balance 一个字节都不动 —— 否则「邀请返佣」会被算成
// 可退本金，A 邀请 B、B 邀请 C 就能层层套出现金。
//
// 流水：type=referral_rebate。billing_records 只有 balance_before/after 两列，
// 记的是**本金桶**的前后值（结算时不变），赠送桶的前后值写进 unit_price_snapshot
// （沿用既有的快照键值约定），这样对账时两边都能看到。
//
// 幂等兜底：request_id = referral_rebate_<rebateID> 的流水已存在 → 只当已加过钱，
// 直接返回（行锁 + 状态判断之外的最后一道闸）。
func creditReferralRebateWalletInTx(tx *gorm.DB, wallet lockedReferralWallet, rebate entity.ReferralRebate, amount int64, remark string) error {
	return creditReferralGiftInTx(tx, wallet, amount,
		referralRebateBillingRequestID(rebate.ID),
		referralRebateRelatedOrderID(rebate),
		referralBillingRemark("邀请返佣（入赠送余额）", rebate, remark))
}

// creditReferralGiftInTx 是「返佣类赠送入账」的唯一实现：写赠金桶 + 写一条 billing_records。
//
// requestID 是幂等锚点（billing_records.request_id 已存在就不再加钱）：
//   - 结算路径用 referral_rebate_<rebateID>（rebate 行本身就是持久锚点）；
//   - 管理员手动补录的 rebate 行是**在同一个事务里新建**的，入账前还拿不到 ID，
//     因此用 (受益人, 金额, 日期) 派生的稳定键，见 manualReferralRebateRequestID。
//
// ⚠️ 手动补录的幂等为什么必须在 billing 层做：referral_rebates 的唯一索引是
// (source, recharge_order_id)，而 manual 行的 recharge_order_id 为 NULL，
// **NULL 不参与唯一性判定** —— 表层面拦不住重复，只能靠这个 request_id 前置查询，
// 并用 users 行锁把并发提交串行化（见 CreateManualReferralRebateWithDB）。
func creditReferralGiftInTx(tx *gorm.DB, wallet lockedReferralWallet, amount int64, requestID string, relatedOrderID int32, remark string) error {
	exists, err := referralBillingRecordExists(tx, requestID)
	if err != nil || exists {
		return err
	}
	before, giftBefore := wallet.Balance.n, wallet.GiftBalance.n
	// Same two-bucket rule as CreditGiftBuckets, using exact wallet micros.
	offset := min(max(-before, 0), amount)
	after, giftAfter := before+offset, max(giftBefore, 0)+amount-offset
	if giftAfter > maxEffectiveMicros {
		return ErrReferralRebateState
	}
	if err := referralSession(tx).Model(&entity.User{}).Where("id = ?", wallet.ID).
		Updates(map[string]any{"balance": fixedInt(after, 6), "gift_balance": fixedInt(giftAfter, 6)}).Error; err != nil {
		return err
	}
	if offset > 0 {
		remark = joinReferralNotes(remark, fmt.Sprintf("其中 ¥%s 用于抵扣欠款", referralMicrosText(offset)))
	}
	return writeExactReferralBillInTx(tx, wallet.ID, ReferralRebateBillingRecordType, amount, before, after, giftBefore, giftAfter, requestID, relatedOrderID, remark)
}

func writeExactReferralBillInTx(tx *gorm.DB, uid int32, kind string, amount, before, after, giftBefore, giftAfter int64, requestID string, orderID int32, remark string) error {
	return referralSession(tx).Model(&entity.BillingRecord{}).Create(map[string]any{
		"user_id": uid, "type": kind, "provider": "system", "amount": fixedInt(amount, 6),
		"balance_before": fixedInt(before, 6), "balance_after": fixedInt(after, 6),
		"unit_price_snapshot": fmt.Sprintf("giftBalanceBefore=%s giftBalanceAfter=%s", referralMicrosText(giftBefore), referralMicrosText(giftAfter)),
		"request_id":          requestID, "related_order_id": orderID, "remark": remark, "created_at": time.Now(),
	}).Error
}

// referralRebateRelatedOrderID 取返佣记录关联的充值订单 ID。
//
// 自动记录（source=auto）一定有关联订单；管理员手动补录（source=manual）
// 没有订单，此时返回 0 —— 不能拿 nil 去写 BillingRecord.RelatedOrderID
// （该列是 int32，语义上 0 表示「无关联订单」）。
func referralRebateRelatedOrderID(rebate entity.ReferralRebate) int32 {
	if rebate.RechargeOrderID == nil {
		return 0
	}
	return *rebate.RechargeOrderID
}

// joinReferralNotes 把两段说明拼成一句，空段自动跳过。
func joinReferralNotes(notes ...string) string {
	parts := make([]string, 0, len(notes))
	for _, note := range notes {
		if trimmed := strings.TrimSpace(note); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "，")
}

// referralDebtOffsetNote 在本次赠送用于抵欠款时给出一句说明。
//
// 用户体验要求（43-*.md §6 最后一条）：算例 B 的用户会看到「我获得了 2 元赠送，
// 但余额只涨了 1 元」，不解释会被当成 bug。这句话进账单备注，用户能自己看懂。
func referralDebtOffsetNote(balanceBefore, balanceAfter float64) string {
	if util.RoundMoney(balanceBefore) >= 0 {
		return ""
	}
	offset := util.RoundMoney(util.RoundMoney(balanceAfter) - util.RoundMoney(balanceBefore))
	if offset <= 0 {
		return ""
	}
	return fmt.Sprintf("其中 ¥%s 用于抵扣欠款", moneyTextForReferral(offset))
}

// referralGiftSnapshot 把赠送余额桶的前后值写进账单快照。
//
// billing_records 没有 gift 列（加列要走迁移，见 t20），而结算流水必须能对账
// 「赠送余额从多少变成多少」。沿用既有的 key=value 快照约定，读取端用
// ExtractBillingSnapshotField 取，不需要改表。
func referralGiftSnapshot(giftBefore, giftAfter float64) string {
	return fmt.Sprintf("giftBalanceBefore=%s giftBalanceAfter=%s",
		moneyTextForReferral(giftBefore), moneyTextForReferral(giftAfter))
}

// debitReferralRebateWalletInTx 把已结算的返佣从邀请人**赠送余额**里扣回，并写一条冲回流水。
//
// 扣减策略（2026-11-09 双桶模型 + 用户明确要求「赠送金额只能为 0，不能为负数」）：
//   - 只扣 gift_balance，**不动 balance** —— balance 是充值本金，返佣从来没进过它；
//   - **clamp 到 0**：赠金可能已被消费，能扣多少扣多少，gift_balance 永不为负；
//   - **差额必须留痕**：应冲回 X、实际只扣到 Y（Y < X）时，流水 amount 记**实际扣减额 Y**，
//     remark 里写明「应冲回 X，赠金余额不足，实际冲回 Y，差额 X-Y 记为不可回收」。
//     静默吞掉差额等于对账时凭空少一笔，绝不允许；
//   - 业务上这笔返佣**照样作废**（status=reversed）—— 扣不到钱不等于记录还能用；
//   - 不删行：原 rebate 记录保留。
//
// 返回实际扣减金额（可能小于应扣金额，见上）。
func debitReferralRebateWalletInTx(tx *gorm.DB, wallet lockedReferralWallet, rebate entity.ReferralRebate, amount int64) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	requestID := referralRebateReversalBillingRequestID(rebate.ID)
	var existing struct{ Amount microColumn }
	err := referralSession(tx).Model(&entity.BillingRecord{}).Select("amount").Where("request_id = ?", requestID).Take(&existing).Error
	if err == nil {
		if !existing.Amount.known {
			return 0, ErrReferralRebateState
		}
		return existing.Amount.n, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	giftBefore := wallet.GiftBalance.n
	deducted := min(max(giftBefore, 0), amount)
	giftAfter := max(giftBefore, 0) - deducted
	// Gift only; principal is a snapshot and never updated by clawback.
	if err := referralSession(tx).Model(&entity.User{}).Where("id = ?", wallet.ID).Update("gift_balance", fixedInt(giftAfter, 6)).Error; err != nil {
		return 0, err
	}
	remark := referralBillingRemark("邀请返佣冲回（扣赠送余额）", rebate, buildReferralReversalRemark(amount, deducted))
	return deducted, writeExactReferralBillInTx(tx, wallet.ID, ReferralRebateReversalBillingRecordType, deducted, wallet.Balance.n, wallet.Balance.n, giftBefore, giftAfter, requestID, referralRebateRelatedOrderID(rebate), remark)
}

// referralBillingRecordExists 判断某条流水（按 request_id）是否已存在。
func referralBillingRecordExists(tx *gorm.DB, requestID string) (bool, error) {
	var count int64
	if err := referralSession(tx).Model(&entity.BillingRecord{}).
		Where("request_id = ?", requestID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// referralRebateBillingRequestID 是结算流水的 request_id，同时充当幂等锚点。
func referralRebateBillingRequestID(rebateID int32) string {
	return fmt.Sprintf("%s_%d", ReferralRebateBillingRecordType, rebateID)
}

// referralRebateReversalBillingRequestID 是冲回流水的 request_id。
func referralRebateReversalBillingRequestID(rebateID int32) string {
	return fmt.Sprintf("%s_%d", ReferralRebateReversalBillingRecordType, rebateID)
}

// referralBillingRemark 拼出账单备注：默认标签 + 原充值订单号 + 附加说明。
// 订单号进备注是为了对账时能一眼看出「这笔钱来自哪一笔充值」，
// 而不是只看到一个孤零零的返佣 ID。
func referralBillingRemark(label string, rebate entity.ReferralRebate, extra string) string {
	parts := []string{label}
	if orderNo := strings.TrimSpace(rebate.OrderNo); orderNo != "" {
		parts = append(parts, "订单 "+orderNo)
	}
	if note := strings.TrimSpace(extra); note != "" {
		parts = append(parts, note)
	}
	return strings.Join(parts, "；")
}

// ManualReferralRebateInput 是管理员手动补录一笔返佣的入参。
type ManualReferralRebateInput struct {
	// BeneficiaryUserID 是**受益人**（拿钱的一方），不是「下单人」。
	// 手动补录没有订单，因此没有受邀人可言 —— InvitedUserID 记 0。
	BeneficiaryUserID int32
	// Amount 是本笔返佣金额（6 位小数口径）。
	Amount float64
	// Day 是补录归属日（YYYY-MM-DD，本地时区；空 = 今天）。
	//
	// 它同时是幂等键的一部分：同一受益人 + 同一金额 + 同一日期只允许一条。
	// 用「日期」而不是精确时间戳，是为了让「管理员手抖点两次提交」与
	// 「隔天真的想再补一笔同额」能够区分开。
	Day string
	// Remark 是附言，写进返佣记录与账单备注。
	Remark string
	// OperatorUsername 只用于留痕（写进备注），不参与任何判定。
	OperatorUsername string
}

const (
	// manualReferralRebateMaxAmount 是单笔手动补录上限（防误输多一个零）。
	// 真正需要更大金额时应拆成多笔或改配置走自动路径，而不是把闸门抬掉。
	manualReferralRebateMaxAmount = 1000000.0
	// manualReferralRebateDayLayout 是补录日期的格式。
	manualReferralRebateDayLayout = "2006-01-02"
)

// CreateManualReferralRebateWithDB 管理员手动补录一笔返佣，**创建即入账**（source=manual）。
//
// 与自动路径（CreateReferralRebateInTx，pending 等结算）的区别：
//   - 自动路径是「充值产生了应付奖励」，所以落 pending，等管理员结算才动钱包；
//   - 手动补录是「管理员现在就要给这个人补一笔」，语义上等同于「建单 + 立即结算」，
//     因此直接落 settled 并在同一事务内入账 —— 否则界面上会出现一条要再点一次
//     结算的手动记录，与「补录」的意图相反。
//
// 入账口径（与自动结算**完全一致**，复用同一条 creditReferralGiftInTx）：
//   - 金额进 gift_balance（可消费、不可退款），**不进** balance 本金；
//   - 欠款状态下先抵欠款再进赠金桶（43-*.md 规则 3）；
//   - 写一条 billing_records(type=referral_rebate)，用户可见的账单明细与赠送记录都能对上。
//
// 幂等（两道闸，任一命中都不会重复入账）：
//  1. referral_rebates 的唯一索引管不到 manual 行（recharge_order_id 为 NULL，
//     NULL 不参与唯一性判定），所以锚点放 billing_records.request_id：
//     键 = referral_rebate_manual_<受益人>_<金额>_<日期>；
//  2. 事务内先锁 users 行（与结算同一锁序 users → referral_rebates），
//     并发的两次提交在这里串行化，第二个进来时一定看得到第一条的账单行。
func CreateManualReferralRebateWithDB(db *gorm.DB, input ManualReferralRebateInput) (entity.ReferralRebate, error) {
	if db == nil || input.BeneficiaryUserID <= 0 {
		return entity.ReferralRebate{}, fmt.Errorf("%w: 受益人必须是正整数账户 ID", ErrReferralRebateManualInvalid)
	}
	amount := util.RoundMoney(input.Amount)
	if amount <= 0 || amount > manualReferralRebateMaxAmount {
		return entity.ReferralRebate{}, fmt.Errorf("%w: 金额必须大于 0 且不超过 %s",
			ErrReferralRebateManualInvalid, moneyTextForReferral(manualReferralRebateMaxAmount))
	}
	day, err := parseManualReferralRebateDay(input.Day)
	if err != nil {
		return entity.ReferralRebate{}, err
	}
	// 总开关关闭时不允许新增返佣（与自动路径一致：关闭就不产生返佣）。
	// 入账要写钱包与账单，结构没就绪必须明确报错（不能假装成功）。
	if !referralSettlementSchemaAvailable(db) {
		return entity.ReferralRebate{}, ErrReferralRebateSchemaUnavailable
	}

	remark := "管理员手动补录返佣"
	if note := strings.TrimSpace(input.Remark); note != "" {
		remark = remark + "：" + note
	}
	if operator := strings.TrimSpace(input.OperatorUsername); operator != "" {
		remark = joinReferralNotes(remark, "操作人 "+operator)
	}
	var exactAmount microColumn
	if err := exactAmount.Scan(strconv.FormatFloat(amount, 'f', 6, 64)); err != nil || exactAmount.n <= 0 || exactAmount.n > maxEffectiveMicros {
		return entity.ReferralRebate{}, ErrReferralRebateManualInvalid
	}
	requestID := manualReferralRebateRequestID(input.BeneficiaryUserID, amount, day)

	var created entity.ReferralRebate
	err = db.Transaction(func(tx *gorm.DB) error {
		wallet, lockErr := lockReferralWalletInTx(tx, input.BeneficiaryUserID)
		if lockErr != nil {
			return lockErr
		}
		exists, existsErr := referralBillingRecordExists(tx, requestID)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			return ErrReferralRebateDuplicate
		}
		now := time.Now()
		rebate := entity.ReferralRebate{
			Source:        entity.ReferralRebateSourceManual,
			InviterUserID: input.BeneficiaryUserID,
			// 手动补录没有受邀人：0 表示「无注册归因」，不是「用户 0」。
			InvitedUserID:   0,
			RechargeOrderID: nil,
			OrderNo:         "",
			// BaseAmount = Amount、Rate = 1 让列表上的「基数 × 比例 = 金额」自洽，
			// 而不是留一个 0 让运营以为数据缺了。
			BaseAmount:    amount,
			Rate:          1,
			Amount:        amount,
			Status:        entity.ReferralRebateStatusSettled,
			PolicyVersion: ReferralRebatePolicyVersion,
			Remark:        remark,
			SettledAt:     &now,
		}
		if createErr := referralSession(tx).Create(&rebate).Error; createErr != nil {
			return createErr
		}
		if creditErr := creditReferralGiftInTx(tx, wallet, exactAmount.n, requestID, 0,
			referralBillingRemark("邀请返佣手动补录（入赠送余额）", rebate, "")); creditErr != nil {
			return creditErr
		}
		created = rebate
		return nil
	})
	if err != nil {
		return entity.ReferralRebate{}, err
	}
	return created, nil
}

// parseManualReferralRebateDay 解析补录日期（空 = 今天零点，本地时区）。
func parseManualReferralRebateDay(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	}
	parsed, err := time.ParseInLocation(manualReferralRebateDayLayout, trimmed, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: 日期必须是 %s 格式", ErrReferralRebateManualInvalid, manualReferralRebateDayLayout)
	}
	return parsed, nil
}

// manualReferralRebateRequestID 是手动补录的幂等键（同时写进 billing_records.request_id）。
//
// 三个维度都必须进键：只放 userID 会让「同一个人当天两笔不同金额」被当成重复；
// 只放 userID+金额会让「隔天再补同额」被永久挡住。
// 审计字段（操作人、备注）**不进键** —— 每次都不同，进键等于让幂等失效。
func manualReferralRebateRequestID(userID int32, amount float64, day time.Time) string {
	return fmt.Sprintf("referral_rebate_manual_%d_%s_%s",
		userID, moneyTextForReferral(amount), day.Format(manualReferralRebateDayLayout))
}

// GetReferralSummaryWithDB 汇总「我的邀请」数据。
//
// 口径：
//   - InvitedCount 只数直接一级（invited_by_user_id = 本人），不递归下线；
//   - 金额按 status 分桶，reversed 单独统计且不计入应付；
//   - 全部按账户计，不按 Key 或设备数。
func GetReferralSummaryWithDB(db *gorm.DB, userID int32) (ReferralSummary, error) {
	var summary ReferralSummary
	if db == nil || userID <= 0 {
		return summary, nil
	}
	policy, err := GetReferralAutoRulesWithDB(db)
	if err != nil && !errors.Is(err, ErrReferralAutoRulesInvalid) {
		return summary, err
	}
	summary.Enabled = policy.active()

	var invite entity.ReferralInvite
	if err := referralSession(db).Model(&entity.ReferralInvite{}).Where("user_id = ?", userID).Take(&invite).Error; err == nil {
		summary.Code = invite.Code
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ReferralSummary{}, err
	} else {
		generated, err := EnsureReferralInviteWithDB(db, userID)
		if err != nil {
			return ReferralSummary{}, err
		}
		summary.Code = generated.Code
	}

	if err := referralSession(db).Model(&entity.User{}).Where("invited_by_user_id = ?", userID).Count(&summary.InvitedCount).Error; err != nil {
		return ReferralSummary{}, err
	}

	type rebateAggregateRow struct {
		Status   string  `gorm:"column:status"`
		Total    float64 `gorm:"column:total"`
		Reversed float64 `gorm:"column:reversed"`
		BaseSum  float64 `gorm:"column:base_sum"`
		RowCount int64   `gorm:"column:row_count"`
	}
	var rows []rebateAggregateRow
	if err := referralSession(db).Model(&entity.ReferralRebate{}).
		Select("status, COALESCE(SUM(amount - reversed_amount), 0) AS total, COALESCE(SUM(CASE WHEN status = 'reversed' AND reversed_amount = 0 THEN amount ELSE reversed_amount END), 0) AS reversed, COALESCE(SUM(base_amount), 0) AS base_sum, COUNT(*) AS row_count").
		Where("inviter_user_id = ?", userID).
		Group("status").
		Scan(&rows).Error; err != nil {
		return ReferralSummary{}, err
	}
	for _, row := range rows {
		summary.RebateCount += row.RowCount
		summary.TotalBaseAmount += row.BaseSum
		summary.ReversedAmount += row.Reversed
		switch row.Status {
		case entity.ReferralRebateStatusPending:
			summary.PendingAmount += row.Total
		case entity.ReferralRebateStatusSettled:
			summary.SettledAmount += row.Total
		}
	}
	summary.PendingAmount = util.RoundMoney(summary.PendingAmount)
	summary.SettledAmount = util.RoundMoney(summary.SettledAmount)
	summary.ReversedAmount = util.RoundMoney(summary.ReversedAmount)
	summary.TotalBaseAmount = util.RoundMoney(summary.TotalBaseAmount)
	return summary, nil
}

// buildReferralRebateQuery 构造列表与统计共用的基础查询（含权限范围）。
//
// JOIN 放在这里而不是取数据时才加：Count 与 Find 必须共用同一套条件，
// 否则会出现「列表 1 行、合计却是全量」的口径漂移。
func buildReferralRebateQuery(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.ReferralRebate{}).
		Joins("LEFT JOIN users AS inviter ON inviter.id = referral_rebates.inviter_user_id").
		Joins("LEFT JOIN users AS invited ON invited.id = referral_rebates.invited_user_id")
}

func applyReferralRebateFilters(query *gorm.DB, filter ReferralRebateListQuery) *gorm.DB {
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("referral_rebates.status = ?", status)
	}
	if filter.InviterUserID > 0 {
		query = query.Where("referral_rebates.inviter_user_id = ?", filter.InviterUserID)
	}
	if filter.InvitedUserID > 0 {
		query = query.Where("referral_rebates.invited_user_id = ?", filter.InvitedUserID)
	}
	if orderNo := strings.TrimSpace(filter.OrderNo); orderNo != "" {
		query = query.Where("referral_rebates.order_no = ?", orderNo)
	}
	return query
}

// ListReferralRebatesWithDB 返回返佣列表（管理员）。
func ListReferralRebatesWithDB(db *gorm.DB, filter ReferralRebateListQuery, page int, pageSize int) (ReferralRebateListResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	result := ReferralRebateListResult{Page: page, PageSize: pageSize}
	baseQuery := applyReferralRebateFilters(buildReferralRebateQuery(db), filter)

	if err := baseQuery.Count(&result.Total).Error; err != nil {
		return ReferralRebateListResult{}, err
	}

	type rebateAggregateRow struct {
		Status   string  `gorm:"column:status"`
		Total    float64 `gorm:"column:total"`
		Reversed float64 `gorm:"column:reversed"`
		BaseSum  float64 `gorm:"column:base_sum"`
	}
	var aggregates []rebateAggregateRow
	if err := baseQuery.Session(&gorm.Session{}).
		Select("referral_rebates.status, COALESCE(SUM(referral_rebates.amount - referral_rebates.reversed_amount), 0) AS total, COALESCE(SUM(CASE WHEN referral_rebates.status = 'reversed' AND referral_rebates.reversed_amount = 0 THEN referral_rebates.amount ELSE referral_rebates.reversed_amount END), 0) AS reversed, COALESCE(SUM(referral_rebates.base_amount), 0) AS base_sum").
		Group("referral_rebates.status").
		Scan(&aggregates).Error; err != nil {
		return ReferralRebateListResult{}, err
	}
	for _, row := range aggregates {
		result.Summary.TotalBaseAmount += row.BaseSum
		result.Summary.ReversedAmount += row.Reversed
		switch row.Status {
		case entity.ReferralRebateStatusPending:
			result.Summary.PendingAmount += row.Total
		case entity.ReferralRebateStatusSettled:
			result.Summary.SettledAmount += row.Total
		}
	}
	result.Summary.PendingAmount = util.RoundMoney(result.Summary.PendingAmount)
	result.Summary.SettledAmount = util.RoundMoney(result.Summary.SettledAmount)
	result.Summary.ReversedAmount = util.RoundMoney(result.Summary.ReversedAmount)
	result.Summary.TotalBaseAmount = util.RoundMoney(result.Summary.TotalBaseAmount)

	offset := (page - 1) * pageSize
	if err := baseQuery.
		Select("referral_rebates.*, CASE WHEN referral_rebates.status = 'reversed' THEN 0 ELSE referral_rebates.amount-referral_rebates.reversed_amount END AS net_amount, COALESCE(inviter.username, '') AS inviter_username, COALESCE(invited.username, '') AS invited_username").
		Order("referral_rebates.id desc").
		Limit(pageSize).Offset(offset).
		Scan(&result.Data).Error; err != nil {
		return ReferralRebateListResult{}, err
	}
	if result.Data == nil {
		result.Data = []ReferralRebateListItem{}
	}
	return result, nil
}
