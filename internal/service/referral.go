package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
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
	// ErrReferralRebateDisabled 总开关关闭时管理员接口不可用。
	ErrReferralRebateDisabled = errors.New("邀请返佣功能未启用")
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
	// Enabled 是总开关状态；关闭时 Code 可能仍为空（历史账户未生成）。
	Enabled bool `json:"enabled"`
	// Code 是本人邀请码，开关关闭且从未生成过时为空字符串。
	Code string `json:"code"`
	// Rate 是当前生效的返佣比例快照（只用于展示，不代表历史记录的比例）。
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
		// 把整个注册事务打回滚（见 AGENTS.md 第 26.4 条）。
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
	if err := referralSession(tx).Model(&entity.User{}).Where("id = ? AND COALESCE(invited_by_user_id, 0) = 0", invitedUser.ID).
		Updates(map[string]any{
			"invited_by_user_id": invite.UserID,
			"invited_at":         &now,
		}).Error; err != nil {
		return err
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
// 开关关闭时直接返回，不产生任何行（需求：关闭时注册不生成邀请码）。
// 生成失败不阻断注册：邀请码是附加权益，不能让注册整体失败。
func RegisterReferralInviteForNewUser(tx *gorm.DB, userID int32) (entity.ReferralInvite, error) {
	if !GetReferralRebateEnabledWithDB(tx) {
		return entity.ReferralInvite{}, nil
	}
	return EnsureReferralInviteWithDB(tx, userID)
}

// referralRebateBaseAmount 计算返佣基数。
//
// **基数 = 订单实付本金，不含赠金**：1688 充值赠 3376、到账 5064，
// 返佣基数仍然是 1688 而不是 5064。当前充值链路只有 users.balance 一个钱包，
// 赠金没有单独入账字段，因此 order.Amount 就是实付本金 —— 这个函数存在的意义
// 是把「口径」写在一个地方，将来引入 gift_balance 时只改这里。
func referralRebateBaseAmount(order entity.RechargeOrder) float64 {
	return util.RoundMoneyToDecimals(order.Amount, util.RechargePrecision)
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

// CreateReferralRebateInTx 在充值到账事务内产生一条待结算返佣。
//
// 幂等由 referral_rebates.recharge_order_id 的唯一索引保证：
// 重复支付回调第二次插入会撞唯一键，这里用 DoNothing + RowsAffected==0 静默跳过，
// 既不会重复发钱，也不会把已经提交的到账事务打回滚。
func CreateReferralRebateInTx(tx *gorm.DB, order entity.RechargeOrder) (*entity.ReferralRebate, error) {
	if tx == nil {
		return nil, nil
	}
	// 开关关闭 → 不产生返佣（需求：关闭时支付回调不产生返佣）。
	if !GetReferralRebateEnabledWithDB(tx) {
		return nil, nil
	}
	// 表/列还没迁移 → 降级，不影响充值到账。
	if !referralSchemaAvailable(tx) {
		return nil, nil
	}
	if order.ID <= 0 || order.UserID <= 0 {
		return nil, nil
	}

	var invited entity.User
	if err := referralSession(tx).Model(&entity.User{}).Select("id", "invited_by_user_id").Where("id = ?", order.UserID).Take(&invited).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	// 没有被邀请过 → 没有受益人，不产生返佣。
	if invited.InvitedByUserID <= 0 || invited.InvitedByUserID == invited.ID {
		return nil, nil
	}

	rate := GetReferralRebateRateWithDB(tx)
	base := referralRebateBaseAmount(order)
	amount := util.RoundMoney(base * rate)
	rebate := entity.ReferralRebate{
		InviterUserID:   invited.InvitedByUserID,
		InvitedUserID:   invited.ID,
		RechargeOrderID: order.ID,
		OrderNo:         order.OrderNo,
		BaseAmount:      base,
		Rate:            rate,
		Amount:          amount,
		Status:          entity.ReferralRebateStatusPending,
		PolicyVersion:   ReferralRebatePolicyVersion,
		Remark:          "充值到账产生待结算返佣",
	}
	result := referralSession(tx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rebate)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// 这笔订单已经有一条返佣（重复回调）→ 返回既有记录，不新建。
		var existing entity.ReferralRebate
		if err := referralSession(tx).Model(&entity.ReferralRebate{}).Where("recharge_order_id = ?", order.ID).Take(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return &existing, nil
	}
	return &rebate, nil
}

// ReverseReferralRebateForOrderInTx 在退款成功的事务内冲回对应返佣。
//
// 语义（对应 04 稿 3.6 节「退款按同一来源可追踪冲回」）：
//   - 找到原订单的返佣记录 → 状态置 reversed + reversed_at；
//   - 已经 settled 的也照样标记（只改状态，不动任何钱包 —— 返佣是独立账本）；
//   - 已经是 reversed → 幂等返回，不重复写时间戳；
//   - 没有对应返佣（没开活动 / 没被邀请）→ 什么都不做。
func ReverseReferralRebateForOrderInTx(tx *gorm.DB, orderID int32, remark string) (*entity.ReferralRebate, error) {
	if tx == nil || orderID <= 0 {
		return nil, nil
	}
	// 表没迁移过 → 一定没有返佣可冲回，直接降级（退款链路不能被返佣拖住）。
	if !referralSchemaAvailable(tx) {
		return nil, nil
	}
	var rebate entity.ReferralRebate
	if err := referralSession(tx).Model(&entity.ReferralRebate{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("recharge_order_id = ?", orderID).Take(&rebate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if rebate.Status == entity.ReferralRebateStatusReversed {
		return &rebate, nil
	}
	now := time.Now()
	updates := map[string]any{
		"status":      entity.ReferralRebateStatusReversed,
		"reversed_at": &now,
	}
	if trimmed := strings.TrimSpace(remark); trimmed != "" {
		updates["remark"] = trimmed
	}
	if err := referralSession(tx).Model(&entity.ReferralRebate{}).Where("id = ?", rebate.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	rebate.Status = entity.ReferralRebateStatusReversed
	rebate.ReversedAt = &now
	if trimmed := strings.TrimSpace(remark); trimmed != "" {
		rebate.Remark = trimmed
	}
	return &rebate, nil
}

// SettleReferralRebateWithDB 把一条 pending 返佣置为 settled。
//
// **只改状态，不写任何钱包**：返佣是独立账本，入账需要另开接口（本期不做）。
// 已经 settled → 幂等返回；reversed → 状态冲突（不允许把冲回的记录再结算）。
func SettleReferralRebateWithDB(db *gorm.DB, admin entity.User, id int32) (entity.ReferralRebate, error) {
	if db == nil || id <= 0 {
		return entity.ReferralRebate{}, ErrReferralRebateNotFound
	}
	if !GetReferralRebateEnabledWithDB(db) {
		return entity.ReferralRebate{}, ErrReferralRebateDisabled
	}
	var rebate entity.ReferralRebate
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := referralSession(tx).Model(&entity.ReferralRebate{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).Take(&rebate).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReferralRebateNotFound
			}
			return err
		}
		switch rebate.Status {
		case entity.ReferralRebateStatusSettled:
			return nil
		case entity.ReferralRebateStatusReversed:
			return ErrReferralRebateState
		}
		now := time.Now()
		remark := "管理员结算"
		if admin.Username != "" {
			remark = "管理员结算：" + admin.Username
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

// GetReferralSummaryWithDB 汇总「我的邀请」数据。
//
// 口径：
//   - InvitedCount 只数直接一级（invited_by_user_id = 本人），不递归下线；
//   - 金额按 status 分桶，reversed 单独统计且不计入应付；
//   - 全部按账户计，不按 Key 或设备数。
func GetReferralSummaryWithDB(db *gorm.DB, userID int32) (ReferralSummary, error) {
	summary := ReferralSummary{
		Enabled: GetReferralRebateEnabledWithDB(db),
		Rate:    GetReferralRebateRateWithDB(db),
	}
	if db == nil || userID <= 0 {
		return summary, nil
	}

	var invite entity.ReferralInvite
	if err := referralSession(db).Model(&entity.ReferralInvite{}).Where("user_id = ?", userID).Take(&invite).Error; err == nil {
		summary.Code = invite.Code
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ReferralSummary{}, err
	}

	if err := referralSession(db).Model(&entity.User{}).Where("invited_by_user_id = ?", userID).Count(&summary.InvitedCount).Error; err != nil {
		return ReferralSummary{}, err
	}

	type rebateAggregateRow struct {
		Status   string  `gorm:"column:status"`
		Total    float64 `gorm:"column:total"`
		BaseSum  float64 `gorm:"column:base_sum"`
		RowCount int64   `gorm:"column:row_count"`
	}
	var rows []rebateAggregateRow
	if err := referralSession(db).Model(&entity.ReferralRebate{}).
		Select("status, COALESCE(SUM(amount), 0) AS total, COALESCE(SUM(base_amount), 0) AS base_sum, COUNT(*) AS row_count").
		Where("inviter_user_id = ?", userID).
		Group("status").
		Scan(&rows).Error; err != nil {
		return ReferralSummary{}, err
	}
	for _, row := range rows {
		summary.RebateCount += row.RowCount
		summary.TotalBaseAmount += row.BaseSum
		switch row.Status {
		case entity.ReferralRebateStatusPending:
			summary.PendingAmount += row.Total
		case entity.ReferralRebateStatusSettled:
			summary.SettledAmount += row.Total
		case entity.ReferralRebateStatusReversed:
			summary.ReversedAmount += row.Total
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
// 否则会出现「列表 1 行、合计却是全量」的口径漂移（AGENTS.md 第 3 条）。
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
		Status  string  `gorm:"column:status"`
		Total   float64 `gorm:"column:total"`
		BaseSum float64 `gorm:"column:base_sum"`
	}
	var aggregates []rebateAggregateRow
	if err := baseQuery.Session(&gorm.Session{}).
		Select("referral_rebates.status, COALESCE(SUM(referral_rebates.amount), 0) AS total, COALESCE(SUM(referral_rebates.base_amount), 0) AS base_sum").
		Group("referral_rebates.status").
		Scan(&aggregates).Error; err != nil {
		return ReferralRebateListResult{}, err
	}
	for _, row := range aggregates {
		result.Summary.TotalBaseAmount += row.BaseSum
		switch row.Status {
		case entity.ReferralRebateStatusPending:
			result.Summary.PendingAmount += row.Total
		case entity.ReferralRebateStatusSettled:
			result.Summary.SettledAmount += row.Total
		case entity.ReferralRebateStatusReversed:
			result.Summary.ReversedAmount += row.Total
		}
	}
	result.Summary.PendingAmount = util.RoundMoney(result.Summary.PendingAmount)
	result.Summary.SettledAmount = util.RoundMoney(result.Summary.SettledAmount)
	result.Summary.ReversedAmount = util.RoundMoney(result.Summary.ReversedAmount)
	result.Summary.TotalBaseAmount = util.RoundMoney(result.Summary.TotalBaseAmount)

	offset := (page - 1) * pageSize
	if err := baseQuery.
		Select("referral_rebates.*, COALESCE(inviter.username, '') AS inviter_username, COALESCE(invited.username, '') AS invited_username").
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
