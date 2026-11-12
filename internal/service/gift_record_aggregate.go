package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 「赠送记录」四类来源的聚合（task_doc/70 需求 1 / 75 号 Q75-1：聚合四类，仅管理员可见）。
//
// # 为什么聚合落在 billing_records 上，而不是把四张源表 UNION 起来
//
// 四类赠送各有一张**字段完全不同**的来源表：
//
//	来源       源表                            金额字段      入账时间字段
//	注册赠送   （无独立表，直接写 users+账单）  —             billing.created_at
//	评价返现   review_rebate_awards            amount        awards.created_at
//	邀请返佣   referral_rebates                amount        **settled_at**（created_at 只是产生时间）
//	充值赠金   recharge_promotion_grants       gift_amount   grants.created_at
//
// 关键在于：四张表里只有 billing_records 是**每一类赠送都必然写一行**的账本
// （new_user_bonus.go:83 / review_rebate 的批准路径 / referral.go 的 creditReferralGiftInTx
// / recharge_promotion.go:244），而且只有它有一个统一语义的**入账时刻**。
// 邀请返佣尤其能说明问题：referral_rebates.created_at 是「产生待结算记录」的时间，
// 真正进钱包是 settled_at —— 拿 created_at 去 UNION 排序会把一笔隔了三天才结算的返佣
// 排到它入账之前的位置，赠送记录的时间轴就错了。
//
// 因此这里用单表查询 + 统一 ORDER BY + 单次 LIMIT/OFFSET：
//   - 分页天然正确（COUNT 与取数共用同一套 WHERE），不存在「四张表各自 limit
//     再合并」导致的跨表错序与总数偏小；
//   - 不需要为先取 ID 再回表拼两轮查询（那是 UNION 方案才需要的妥协）。
//
// 代价（必须认）：
//   - 历史行如果某个来源当时**没写账单**（迁移前的老数据），这里看不到；
//   - 冲回/追缴（referral_rebate_reversal / registration_bonus_clawback /
//     registration_bonus_revoked）不在这四类里，不进「赠送记录」——它们属于负向调整，
//     在「账单明细」页可见。这个边界刻意写死：赠送记录只回答「送了什么」，
//     把冲回混进来会让「合计」看起来比用户实际拿到的手感金额更少。
const (
	// GiftRecordSourceRegistrationBonus 注册赠送。
	GiftRecordSourceRegistrationBonus = registrationBonusBillingRecordType
	// GiftRecordSourceReviewRebate 评价返现。
	GiftRecordSourceReviewRebate = ReviewRebateBillingRecordType
	// GiftRecordSourceReferralRebate 邀请返佣（含管理员手动补录）。
	GiftRecordSourceReferralRebate = ReferralRebateBillingRecordType
	// GiftRecordSourceRechargePromotion 充值阶梯赠送。
	GiftRecordSourceRechargePromotion = rechargePromotionBillingRecordType
)

// ErrGiftRecordSourceInvalid 来源筛选值不合法。
var ErrGiftRecordSourceInvalid = errors.New("赠送来源筛选值无效")

// giftRecordSourceOrder 是来源的固定展示顺序（汇总表与筛选下拉共用）。
var giftRecordSourceOrder = []string{
	GiftRecordSourceRegistrationBonus,
	GiftRecordSourceReviewRebate,
	GiftRecordSourceReferralRebate,
	GiftRecordSourceRechargePromotion,
}

// giftRecordSourceLabels 是来源的中文名。
//
// 前端不应自己再维护一份：文案一旦两处各写一份，改一处漏一处的老问题必然出现。
var giftRecordSourceLabels = map[string]string{
	GiftRecordSourceRegistrationBonus: "注册赠送",
	GiftRecordSourceReviewRebate:      "评价返现",
	GiftRecordSourceReferralRebate:    "邀请返佣",
	GiftRecordSourceRechargePromotion: "充值赠金",
}

// GiftRecordListQuery 是赠送记录的筛选条件。
//
// 用户与时间范围是「管理员对账」的刚需：按人查「这个月到底送了多少」、
// 按时间段查「某次活动发了多少」。来源筛选是 Q75-1 的直接要求。
type GiftRecordListQuery struct {
	// Source 精确匹配四类之一（空 = 全部）。
	Source string
	// UserID 精确匹配受益人（0 = 不限）。
	UserID int32
	// CreatedFrom / CreatedTo 是入账时间的半开区间 [From, To)。
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// GiftRecordItem 是赠送记录的一行。
type GiftRecordItem struct {
	ID       int32  `json:"id"`
	UserID   int32  `json:"userId"`
	Username string `json:"username"`
	// Source 是机器可读的来源标识，SourceLabel 是中文名。
	Source      string  `json:"source"`
	SourceLabel string  `json:"sourceLabel"`
	Amount      float64 `json:"amount"`
	// BalanceBefore/After 是**本金桶**的前后值。
	// 欠款状态下这笔赠送会先抵欠款，所以 Amount 与 balance 的增量可以不等。
	BalanceBefore float64 `json:"balanceBefore"`
	BalanceAfter  float64 `json:"balanceAfter"`
	// GiftBalanceBefore/After 是赠金桶前后值，来自 unit_price_snapshot。
	// 指针为 nil 表示历史行没有快照（不能当成 0，见 43-*.md 的历史行口径）。
	GiftBalanceBefore *float64 `json:"giftBalanceBefore,omitempty"`
	GiftBalanceAfter  *float64 `json:"giftBalanceAfter,omitempty"`
	// DebtOffset 是本次赠送里用于抵扣欠款的部分（0 = 没抵）。
	// = max(0, BalanceAfter - BalanceBefore)，让「送 2 元只涨 1 元」在列表里自解释。
	DebtOffset float64 `json:"debtOffset"`
	Remark     string  `json:"remark"`
	RequestID  string  `json:"requestId"`
	// RelatedOrderID 为 0 表示无关联充值订单（注册赠送、手动补录返佣都是这样）。
	RelatedOrderID int32     `json:"relatedOrderId"`
	CreatedAt      time.Time `json:"createdAt"`

	// UnitPriceSnapshot 只用于接住 Scan 出来的账单快照，解析成
	// GiftBalanceBefore/After 之后就没用了，因此 json:"-" 不下发。
	//
	// 为什么必须是**导出字段**而不是内部字段：GORM 的 Scan 用反射赋值，
	// 未导出字段赋不进去（会静默留空），赠金桶前后值就会永远显示为「—」。
	// 为什么合并进同一行而不是另建 Scan 载体再回表：多一轮回表会让分页
	// LIMIT 与解析结果对不上（两轮之间可能有新行插入）。
	UnitPriceSnapshot string `gorm:"column:unit_price_snapshot" json:"-"`
}

// GiftRecordSourceSummary 是某一个来源的合计。
type GiftRecordSourceSummary struct {
	Source string  `json:"source"`
	Label  string  `json:"label"`
	Count  int64   `json:"count"`
	Amount float64 `json:"amount"`
}

// GiftRecordSummary 是当前筛选条件下的金额合计。
//
// 与列表共用同一套 WHERE：否则会出现「列表只剩 3 行、合计还是全量」的口径漂移
// （与 ListReferralRebatesWithDB 的处理一致）。
type GiftRecordSummary struct {
	TotalAmount float64                   `json:"totalAmount"`
	BySource    []GiftRecordSourceSummary `json:"bySource"`
}

// GiftRecordListResult 是赠送记录列表结果。
type GiftRecordListResult struct {
	Data     []GiftRecordItem  `json:"data"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"pageSize"`
	Summary  GiftRecordSummary `json:"summary"`
}

// GiftRecordSourceOptions 返回四类来源（标识 + 中文名），供前端渲染筛选与图例。
func GiftRecordSourceOptions() []GiftRecordSourceSummary {
	options := make([]GiftRecordSourceSummary, 0, len(giftRecordSourceOrder))
	for _, source := range giftRecordSourceOrder {
		options = append(options, GiftRecordSourceSummary{Source: source, Label: giftRecordSourceLabels[source]})
	}
	return options
}

// normalizeGiftRecordSource 校验来源筛选值。
func normalizeGiftRecordSource(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if _, ok := giftRecordSourceLabels[trimmed]; !ok {
		return "", fmt.Errorf("%w: 只能是 %s 之一",
			ErrGiftRecordSourceInvalid, strings.Join(giftRecordSourceOrder, " / "))
	}
	return trimmed, nil
}

// ListGiftRecordsWithDB 返回「赠送记录」列表（管理员，四类来源聚合）。
func ListGiftRecordsWithDB(db *gorm.DB, filter GiftRecordListQuery, page int, pageSize int) (GiftRecordListResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	source, err := normalizeGiftRecordSource(filter.Source)
	if err != nil {
		return GiftRecordListResult{}, err
	}
	result := GiftRecordListResult{
		Page:     page,
		PageSize: pageSize,
		Data:     []GiftRecordItem{},
		Summary: GiftRecordSummary{
			BySource: []GiftRecordSourceSummary{},
		},
	}

	// JOIN 放进基础查询构造器：Count / 汇总 / 取数三处必须共用同一套条件。
	base := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Joins("LEFT JOIN users AS gift_users ON gift_users.id = billing_records.user_id").
		Where("billing_records.type IN ?", giftRecordSourceOrder)
	if source != "" {
		base = base.Where("billing_records.type = ?", source)
	}
	if filter.UserID > 0 {
		base = base.Where("billing_records.user_id = ?", filter.UserID)
	}
	if filter.CreatedFrom != nil {
		base = base.Where("billing_records.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		base = base.Where("billing_records.created_at < ?", *filter.CreatedTo)
	}

	if err := base.Session(&gorm.Session{}).Count(&result.Total).Error; err != nil {
		return GiftRecordListResult{}, err
	}

	type giftAggregateRow struct {
		Source   string  `gorm:"column:source"`
		Total    float64 `gorm:"column:total"`
		RowCount int64   `gorm:"column:row_count"`
	}
	var aggregates []giftAggregateRow
	if err := base.Session(&gorm.Session{}).
		Select("billing_records.type AS source, COALESCE(SUM(billing_records.amount), 0) AS total, COUNT(*) AS row_count").
		Group("billing_records.type").
		Scan(&aggregates).Error; err != nil {
		return GiftRecordListResult{}, err
	}
	// 汇总按固定顺序输出，并且**四个来源都出现**（没有记录的补 0）：
	// 缺项会让前端图例/合计在不同筛选下抖动，也会把「某类一条都没有」
	// 和「这类不存在」混成同一种表现。
	counts := make(map[string]giftAggregateRow, len(aggregates))
	for _, row := range aggregates {
		counts[row.Source] = row
	}
	for _, candidate := range giftRecordSourceOrder {
		row := counts[candidate]
		amount := util.RoundMoney(row.Total)
		result.Summary.BySource = append(result.Summary.BySource, GiftRecordSourceSummary{
			Source: candidate,
			Label:  giftRecordSourceLabels[candidate],
			Count:  row.RowCount,
			Amount: amount,
		})
		result.Summary.TotalAmount = util.RoundMoney(result.Summary.TotalAmount + amount)
	}

	if err := base.Session(&gorm.Session{}).
		Select("billing_records.id, billing_records.user_id, billing_records.type AS source, billing_records.amount, " +
			"billing_records.balance_before, billing_records.balance_after, billing_records.unit_price_snapshot, " +
			"billing_records.request_id, billing_records.remark, billing_records.related_order_id, billing_records.created_at, " +
			"COALESCE(gift_users.username, '') AS username").
		Order("billing_records.created_at desc, billing_records.id desc").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&result.Data).Error; err != nil {
		return GiftRecordListResult{}, err
	}
	if result.Data == nil {
		result.Data = []GiftRecordItem{}
	}
	for index := range result.Data {
		item := &result.Data[index]
		item.SourceLabel = giftRecordSourceLabels[item.Source]
		if item.SourceLabel == "" {
			// 四类之外不会进来（WHERE 已经限死），留兜底只为不显示空白标签。
			item.SourceLabel = item.Source
		}
		item.Amount = util.RoundMoney(item.Amount)
		item.BalanceBefore = util.RoundMoney(item.BalanceBefore)
		item.BalanceAfter = util.RoundMoney(item.BalanceAfter)
		if offset := util.RoundMoney(item.BalanceAfter - item.BalanceBefore); offset > 0 {
			item.DebtOffset = offset
		}
		item.GiftBalanceBefore = giftSnapshotAmount(item.UnitPriceSnapshot, "giftBalanceBefore")
		item.GiftBalanceAfter = giftSnapshotAmount(item.UnitPriceSnapshot, "giftBalanceAfter")
	}
	return result, nil
}

// giftSnapshotAmount 从账单快照里取一个金额字段；缺失或不可解析时返回 nil。
//
// 返回指针而不是 0：历史行没有快照与「快照里的值正好是 0」是两件事，
// 前者界面该显示「—」，后者是真实的 0。
func giftSnapshotAmount(snapshot string, key string) *float64 {
	raw := strings.TrimSpace(ExtractBillingSnapshotField(snapshot, key))
	if raw == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	value := util.RoundMoney(parsed)
	return &value
}
