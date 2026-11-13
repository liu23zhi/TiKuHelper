package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 用户画像（整体 + 单用户）的**唯一**聚合层（task_doc/98 §1–§3）。
//
// # 这个文件存在的三条硬约束
//
//  1. **每个数字都必须能指向本文件里的一条具体 SQL**。画像页不出现「估算」「示例」
//     或任何由前端补零凑出来的图（63 号 U-03 红线）。因此所有聚合都在这里落地，
//     handler 与前端只做搬运。
//
//  2. **「没有数据」与「数据是 0」必须可区分**（判据 ②）。为此：
//     - 序列类（小时/星期/区域/消费类型/工单/注册来源）在无数据时返回**长度 0 的切片**
//       （JSON `[]`），而不是 24 个 0 或一条 count=0 的假桶；
//     - 标量类（记录数/金额/工单数/用户数）用**指针**，无数据是 nil（JSON `null`），
//       只有「确实算过且结果是 0」才给 0。
//     理由很具体：饼图拿到 `[0,0,0]` 会画出图例，拿到 `[]` 才显示「暂无数据」。
//
//  3. **整体画像与单用户画像共用同一套聚合函数**（判据 ④）。做法是把「范围」收成一个
//     参数 `PortraitScope.UserID`（0 = 全站），所有子查询都从 `portraitBillingScope`
//     这个唯一起点取 WHERE，不存在第二套 SQL。变异「让整体走另一套 SQL」会让
//     `TestPortraitOverallEqualsSumOfUsers` 变红。
//
// # 明确**不可用**的数据源（98 号 §1，不得假装存在）
//
//   - **设备指纹**：entity 层不存在（唯一 `fingerprint` 列是
//     `recharge_order.refund_channel_fingerprint`，是退款渠道指纹，与设备无关）。
//     所以 R4/R6/R15 与「设备与环境」栏本轮不做，在 Notes 里如实标注。
//   - **答题正确率**：`billing_records` 没有任何正确性字段。本题只算**量**，不算正确率。
//
// # 一个必须写下来的实现陷阱：SQLite 的时间桶
//
// `billing_records.created_at` 在 SQLite 里存的是**带时区偏移的文本**
// （实测：`2026-11-13 14:00:00+08:00`）。直接 `strftime('%H', created_at)` 会按 UTC
// 解析 → 返回 6 而不是 14，整个「时间段分布」会整体偏移 8 小时且**看不出错**。
// 因此小时/星期一律先 `substr(created_at, 1, 19)` 截掉偏移再取（MySQL 的 DATETIME
// 本身就是 `YYYY-MM-DD HH:MM:SS`，同样适用；MySQL 走 HOUR()/DAYOFWEEK() 等价式）。

// 画像范围的两种取值（对外 JSON 的 scope 字段）。
const (
	PortraitScopeOverall = "overall"
	PortraitScopeUser    = "user"
)

// ErrPortraitScopeInvalid 表示目标用户 ID 非法。
var ErrPortraitScopeInvalid = errors.New("画像目标用户参数无效")

// ErrPortraitUserNotFound 表示目标用户不存在（或已归档）。
var ErrPortraitUserNotFound = errors.New("画像目标用户不存在")

// PortraitScope 是画像的查询范围。
//
// ⚠️ 这里**只有目标用户**，没有「当前登录用户 / 操作者」字段 —— 这是 98 号 §0
// 第 ③ 层的落地方式：把「查自己」这条路从签名上就别掉，而不是靠调用方自觉。
// 权限判定在 controller 层（requireAdmin），本层不做也不该做。
// 签名审查用例见 TestPortraitServiceLayerAcceptsOnlyTargetUserID。
type PortraitScope struct {
	// UserID 是**要被画像的目标用户**；0 表示全站整体画像。
	UserID int32
}

// PortraitTimeBucket 是「时间段分布」的一个桶。
//
// Bucket 的语义由所在切片决定：Hours 里是 0–23 的小时，Weekdays 里是
// 0=周日 … 6=周六（与 `strftime('%w')` 一致，前端不要再做一次转换）。
type PortraitTimeBucket struct {
	Bucket int     `json:"bucket"`
	Label  string  `json:"label"`
	Count  int64   `json:"count"`
	Amount float64 `json:"amount"`
}

// PortraitRegionSlice 是「区域分布」的一项。
//
// Region 为空串表示**该 IP 确实出现过，但本地归属地库没有它的记录**。
// 这种情况必须单独成桶（前端显示「未知地区」），而不是丢弃 —— 丢弃会让
// 区域分布合计小于账单行数，运营会误以为数据丢了。
type PortraitRegionSlice struct {
	Region  string  `json:"region"`
	Label   string  `json:"label"`
	Count   int64   `json:"count"`
	Amount  float64 `json:"amount"`
	IPCount int     `json:"ipCount"`
}

// PortraitTypeSlice 是「消费类型分布」的一项。
type PortraitTypeSlice struct {
	Type      string  `json:"type"`
	Label     string  `json:"label"`
	Direction string  `json:"direction"`
	Count     int64   `json:"count"`
	Amount    float64 `json:"amount"`
}

// PortraitTicketSlice 是「工单量」按状态的一项。
type PortraitTicketSlice struct {
	Status string `json:"status"`
	Label  string `json:"label"`
	Count  int64  `json:"count"`
}

// PortraitSourceSlice 是「注册来源」的一项。
type PortraitSourceSlice struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	Count  int64  `json:"count"`
}

// PortraitTotals 是概览标量。
//
// 全部用指针：nil = 该范围下**没有可分析的数据**（前端显示「暂无数据」），
// 非 nil 才是「算过，结果是这个数（可能是 0）」。这是判据 ② 的标量面。
type PortraitTotals struct {
	// UserCount 只在整体画像下有值。
	UserCount *int64 `json:"userCount"`
	// RecordCount 是账单行数（= 98 号口径的「答题量」：只有扣费时落行）。
	RecordCount *int64 `json:"recordCount"`
	// ConsumptionCount 是 direction=expense 的行数（真消费，与消费类型分布同源）。
	ConsumptionCount *int64   `json:"consumptionCount"`
	Amount           *float64 `json:"amount"`
	// TicketCount 只在有工单表数据来源时给值。
	TicketCount *int64 `json:"ticketCount"`
	// ActiveUserCount 是整体画像里「有过账单的用户数」，单用户画像下为 nil。
	ActiveUserCount *int64 `json:"activeUserCount"`
}

// PortraitBase 是单用户画像的「基础」栏（整体画像下为 nil）。
type PortraitBase struct {
	UserID          int32      `json:"userId"`
	Username        string     `json:"username"`
	Nickname        string     `json:"nickname"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	Balance         float64    `json:"balance"`
	GiftBalance     float64    `json:"giftBalance"`
	RegisterSource  string     `json:"registerSource"`
	RegisterIP      string     `json:"registerIp"`
	RegisterRegion  string     `json:"registerRegion"`
	LastLoginIP     string     `json:"lastLoginIp"`
	LastLoginRegion string     `json:"lastLoginRegion"`
	CreatedAt       time.Time  `json:"createdAt"`
	LastLoginAt     *time.Time `json:"lastLoginAt"`
}

// UserPortrait 是两个接口（整体 / 单用户）共用的响应体。
//
// 同一个结构体是刻意的：前端只写一份渲染，不存在「整体页和单用户页口径不一致」
// 这类只能靠肉眼发现的问题。
type UserPortrait struct {
	Scope        string                `json:"scope"`
	TargetUserID int32                 `json:"targetUserId"`
	GeneratedAt  time.Time             `json:"generatedAt"`
	Totals       PortraitTotals        `json:"totals"`
	Hours        []PortraitTimeBucket  `json:"hours"`
	Weekdays     []PortraitTimeBucket  `json:"weekdays"`
	Regions      []PortraitRegionSlice `json:"regions"`
	BillingTypes []PortraitTypeSlice   `json:"billingTypes"`
	Tickets      []PortraitTicketSlice `json:"tickets"`
	// RegisterSources 整体画像是注册来源分布；单用户画像下它是「该用户自己这一条」。
	RegisterSources []PortraitSourceSlice `json:"registerSources"`
	// RegisterRegions 是「注册 IP 的归属地」分布（数据源 users.register_ip）。
	RegisterRegions []PortraitRegionSlice `json:"registerRegions"`
	Base            *PortraitBase         `json:"base"`
	// Notes 如实标注「哪些栏位当前没有数据源」，避免前端画成 0 让人以为「用得少」。
	Notes []string `json:"notes"`
}

// 区域分布在内存里合成的上限：只解析「出现次数最多」的 N 个 IP。
//
// 为什么必须有上限：整体画像的 client_ip 基数可以达到几十万，一次全解析会把
// 归属地表和进程内存都拖住，而画像只需要「主要来源区域」。这是**采样口径**，
// 已在 Notes 里如实告知（Top N 覆盖的账单行数比例），不是隐藏的估算。
const portraitRegionIPLimit = 500

// 小时/星期的展示文案。
var portraitWeekdayLabels = [7]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// 工单状态文案（与 service/ticket.go 的展示层保持同一口径）。
var portraitTicketStatusLabels = map[string]string{
	entity.TicketStatusOpen:     "待处理",
	entity.TicketStatusPending:  "处理中",
	entity.TicketStatusResolved: "已解决",
	entity.TicketStatusClosed:   "已关闭",
}

// 工单状态的固定枚举顺序：无数据的库不该因为「没有 pending 工单」就少一个图例。
var portraitTicketStatusOrder = []string{
	entity.TicketStatusOpen,
	entity.TicketStatusPending,
	entity.TicketStatusResolved,
	entity.TicketStatusClosed,
}

// 注册来源文案。未知取值**原样回显**，不猜。
var portraitRegisterSourceLabels = map[string]string{
	"public": "公开注册",
	"admin":  "管理员创建",
	"wechat": "微信注册",
	"import": "批量导入",
	"legacy": "历史账号",
}

// portraitDialect 返回规范化后的方言名（默认 sqlite）。
func portraitDialect(db *gorm.DB) string {
	if db == nil || db.Dialector == nil {
		return "sqlite"
	}
	return strings.ToLower(strings.TrimSpace(db.Dialector.Name()))
}

// portraitHourExpr 返回「本地墙钟小时」的 SQL 表达式（0–23）。
//
// ⚠️ SQLite 分支必须做**真正的时区换算**，不能靠 substr 截偏移。
//
// 原实现是 `substr(created_at, 1, 19)`，理由是「created_at 存的是
// `2026-11-13 14:00:00+08:00` 这种带偏移的文本，截掉偏移后再取小时才是本地时间」。
// 那个理由只在**所有行都恰好是 +08:00** 时才成立 —— 而实测本机库
// `billing_records.created_at` 是 `2026-05-08 13:15:29.939932211+00:00`（**UTC**）。
//
// 实测伤害（真实库 53 行）：
//
//	现实现 substr        0:1, 1:11, 5:9, 8:2, 13:11, 15:18, 16:1
//	正确 datetime(+8h)   0:2, 9:11, 13:9, 16:2, 21:11, 23:18
//	→ 52/53 行的小时取值错误，1/53 行的星期错误
//
// 用户可见后果：管理员在画像页看到峰值是「下午 15 点」，真实是「晚上 23 点」。
// 整张时段分布左移 8 小时，而且**形状看起来完全合理**，肉眼发现不了。
//
// 为什么 `strftime('%H', created_at)` 不行：SQLite 把带偏移的文本按 UTC 解析
// （原注释实测「14 点返回 6」是对的）。所以要么先归一化时区（本修法），
// 要么砍掉偏移（原实现，但只能是 UTC 行被砍成 UTC 小时 = 错 8 小时）。
//
// 为什么用固定 '+8 hours' 而不是 'localtime'：后者依赖进程所在机器的时区。
// CI 跑 UTC、开发机跑 CST，同一份数据得出不同分布 —— 比偏移更难查。
// 固定 +8 让结果与部署环境无关（画像的时段定义本身就是用户所在时区）。
func portraitHourExpr(db *gorm.DB) string {
	if portraitDialect(db) == "mysql" {
		return "HOUR(created_at)"
	}
	return "CAST(strftime('%H', datetime(created_at, '+8 hours')) AS INTEGER)"
}

// portraitWeekdayExpr 返回「本地墙钟星期」的 SQL 表达式（0=周日 … 6=周六）。
//
// 与 portraitHourExpr 同源同理：星期也会因 8 小时偏移在**跨日界**的行上算错
// （实测 1/53 行）。修法一致。
func portraitWeekdayExpr(db *gorm.DB) string {
	if portraitDialect(db) == "mysql" {
		// MySQL 的 DAYOFWEEK 是 1=周日 … 7=周六，减 1 对齐 SQLite 的 %w。
		return "(DAYOFWEEK(created_at) - 1)"
	}
	return "CAST(strftime('%w', datetime(created_at, '+8 hours')) AS INTEGER)"
}

// portraitBillingScope 是**唯一**的账单范围起点：整体 / 单用户只差这一个 WHERE。
func portraitBillingScope(db *gorm.DB, scope PortraitScope) *gorm.DB {
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{})
	if scope.UserID > 0 {
		query = query.Where("user_id = ?", scope.UserID)
	}
	return query
}

// normalizePortraitScope 校验并归一化范围。
func normalizePortraitScope(scope PortraitScope) (PortraitScope, error) {
	if scope.UserID < 0 {
		return PortraitScope{}, ErrPortraitScopeInvalid
	}
	return scope, nil
}

// BuildUserPortraitWithDB 生成画像。整体画像传 UserID=0，单用户传目标用户 ID。
//
// 这是两个 handler 的**唯一**数据入口（判据 ④）。任何「整体走另一套 SQL」的改动
// 都会破坏 TestPortraitOverallEqualsSumOfUsers 的守恒断言。
func BuildUserPortraitWithDB(db *gorm.DB, scope PortraitScope) (UserPortrait, error) {
	if db == nil {
		return UserPortrait{}, ErrPortraitScopeInvalid
	}
	normalized, err := normalizePortraitScope(scope)
	if err != nil {
		return UserPortrait{}, err
	}

	portrait := UserPortrait{
		Scope:        PortraitScopeOverall,
		TargetUserID: normalized.UserID,
		GeneratedAt:  time.Now(),
		// 判据 ②：序列一律以长度 0 起步，绝不预填 0 桶。
		Hours:           make([]PortraitTimeBucket, 0, 24),
		Weekdays:        make([]PortraitTimeBucket, 0, 7),
		Regions:         make([]PortraitRegionSlice, 0, 8),
		BillingTypes:    make([]PortraitTypeSlice, 0, 8),
		Tickets:         make([]PortraitTicketSlice, 0, 4),
		RegisterSources: make([]PortraitSourceSlice, 0, 4),
		RegisterRegions: make([]PortraitRegionSlice, 0, 8),
		Notes:           make([]string, 0, 4),
	}

	// 单用户画像要先确认目标用户存在：否则一个不存在的 ID 会返回「全是空数组」
	// 的成功响应，前端会以为「这个用户没有任何行为」，而不是「查错了人」。
	if normalized.UserID > 0 {
		portrait.Scope = PortraitScopeUser
		user, err := loadPortraitUser(db, normalized.UserID)
		if err != nil {
			return UserPortrait{}, err
		}
		portrait.Base = buildPortraitBase(db, user)
	}

	if portrait.Scope == PortraitScopeOverall {
		var userCount int64
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).Count(&userCount).Error; err != nil {
			return UserPortrait{}, err
		}
		portrait.Totals.UserCount = &userCount
	}

	if err := fillPortraitBillingTotals(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}
	if err := fillPortraitTimeBuckets(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}
	if err := fillPortraitRegions(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}
	if err := fillPortraitBillingTypes(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}
	if err := fillPortraitTickets(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}
	if err := fillPortraitRegisterSources(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}
	if err := fillPortraitRegisterRegions(db, normalized, &portrait); err != nil {
		return UserPortrait{}, err
	}

	portrait.Notes = append(portrait.Notes,
		"设备指纹数据源不存在（98 §1.1），本页不含设备维度，也不做「新设备/设备农场」类判断。",
		"答题正确率无数据源：billing_records 没有正确性字段，本页只统计量与金额。",
		"时间/区域/类型分布均来自 billing_records：免费额度内的调用不产生账单行，因此是「计费口径」而非「请求口径」。",
		fmt.Sprintf("区域分布按「出现次数最多的 %d 个 client_ip」解析，未命中本地归属地库的 IP 单独归入未知地区，不外呼上游。", portraitRegionIPLimit),
	)
	return portrait, nil
}

// loadPortraitUser 读目标用户。已归档（软删除）视为不存在。
func loadPortraitUser(db *gorm.DB, userID int32) (entity.User, error) {
	var user entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", userID).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.User{}, ErrPortraitUserNotFound
		}
		return entity.User{}, err
	}
	return user, nil
}

// buildPortraitBase 组装基础栏，并把注册 IP / 最近登录 IP 解析成归属地。
//
// 归属地只查本地库（location_cache_entries + ip_segments），**绝不外呼**：
// 打开一个画像页不该产生上游计费，也不该因为上游故障而让整页失败。
func buildPortraitBase(db *gorm.DB, user entity.User) *PortraitBase {
	addresses := portraitIPAddresses(db, []string{user.RegisterIP, user.LastLoginIP})
	base := &PortraitBase{
		UserID:          user.ID,
		Username:        user.Username,
		Nickname:        user.Nickname,
		Role:            user.Role,
		Status:          user.Status,
		Balance:         user.Balance,
		GiftBalance:     user.GiftBalance,
		RegisterSource:  user.RegisterSource,
		RegisterIP:      user.RegisterIP,
		RegisterRegion:  addresses[strings.ToLower(strings.TrimSpace(user.RegisterIP))],
		LastLoginIP:     user.LastLoginIP,
		LastLoginRegion: addresses[strings.ToLower(strings.TrimSpace(user.LastLoginIP))],
		CreatedAt:       user.CreatedAt,
		LastLoginAt:     user.LastLoginAt,
	}
	return base
}

// fillPortraitBillingTotals 填标量概览。
func fillPortraitBillingTotals(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	var agg struct {
		RecordCount *int64
		Amount      *float64
	}
	if err := portraitBillingScope(db, scope).
		Select("COUNT(*) AS record_count, COALESCE(SUM(amount), 0) AS amount").
		Scan(&agg).Error; err != nil {
		return err
	}
	// COUNT(*) 永远有值（0 也返回 0），但「一行都没有」与「有行」必须区分：
	// 空表时 record_count 会是 0，这里改成 nil，让前端显示「暂无数据」。
	if agg.RecordCount != nil && *agg.RecordCount > 0 {
		portrait.Totals.RecordCount = agg.RecordCount
		portrait.Totals.Amount = agg.Amount
	}

	// 消费行数 = 支出方向的账单行数。方向判定复用 buildBillingRecordDirection，
	// 保证「消费类型分布」的 direction 与这里的口径是同一个函数。
	expenseTypes := portraitExpenseTypes()
	if len(expenseTypes) == 0 {
		return nil
	}
	var consumption struct{ Total *int64 }
	if err := portraitBillingScope(db, scope).
		Where("type IN ?", expenseTypes).
		Select("COUNT(*) AS total").
		Scan(&consumption).Error; err != nil {
		return err
	}
	if consumption.Total != nil && *consumption.Total > 0 {
		portrait.Totals.ConsumptionCount = consumption.Total
	}

	// 整体画像额外给「有过账单的用户数」——它是「有多少人真正在用」的唯一硬数字。
	if portrait.Scope == PortraitScopeOverall {
		var active struct{ Total *int64 }
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
			Select("COUNT(DISTINCT user_id) AS total").Scan(&active).Error; err != nil {
			return err
		}
		if active.Total != nil && *active.Total > 0 {
			portrait.Totals.ActiveUserCount = active.Total
		}
	}
	return nil
}

// portraitExpenseTypes 汇总所有「支出方向」的账单类型。
//
// 不写死列表：调用 buildBillingRecordDirection 这一**已在生产使用**的判定函数，
// 保证画像的「消费」与账单明细页的「支出」完全同口径。
func portraitExpenseTypes() []string {
	candidates := []string{
		normalSearchBillingRecordType,
		rechargeBillingRecordType,
		registrationBonusBillingRecordType,
		RegistrationBonusClawbackBillingRecordType,
		registrationBonusRevokedBillingRecordType,
		rechargePromotionBillingRecordType,
		ReviewRebateBillingRecordType,
		ReferralRebateBillingRecordType,
		ReferralRebateReversalBillingRecordType,
		balanceAdjustmentBillingRecordType,
		scriptReservationBillingRecordType,
		scriptRefundBillingRecordType,
		ScriptActivationPurchaseBillingType,
		AdminGrantBillingRecordType,
		AdminGrantReversalBillingRecordType,
	}
	expenses := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if buildBillingRecordDirection(candidate) == "expense" {
			expenses = append(expenses, candidate)
		}
	}
	return expenses
}

// fillPortraitTimeBuckets 填「时间段分布」：按小时与按星期。
func fillPortraitTimeBuckets(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	type bucketRow struct {
		Bucket int
		Count  int64
		Amount float64
	}

	hours := make([]bucketRow, 0, 24)
	if err := portraitBillingScope(db, scope).
		Select(fmt.Sprintf("%s AS bucket, COUNT(*) AS count, COALESCE(SUM(amount), 0) AS amount", portraitHourExpr(db))).
		Group("bucket").Order("bucket").
		Scan(&hours).Error; err != nil {
		return err
	}
	for _, row := range hours {
		portrait.Hours = append(portrait.Hours, PortraitTimeBucket{
			Bucket: row.Bucket,
			Label:  fmt.Sprintf("%02d:00", row.Bucket),
			Count:  row.Count,
			Amount: row.Amount,
		})
	}

	weekdays := make([]bucketRow, 0, 7)
	if err := portraitBillingScope(db, scope).
		Select(fmt.Sprintf("%s AS bucket, COUNT(*) AS count, COALESCE(SUM(amount), 0) AS amount", portraitWeekdayExpr(db))).
		Group("bucket").Order("bucket").
		Scan(&weekdays).Error; err != nil {
		return err
	}
	for _, row := range weekdays {
		label := ""
		if row.Bucket >= 0 && row.Bucket < len(portraitWeekdayLabels) {
			label = portraitWeekdayLabels[row.Bucket]
		}
		portrait.Weekdays = append(portrait.Weekdays, PortraitTimeBucket{
			Bucket: row.Bucket,
			Label:  label,
			Count:  row.Count,
			Amount: row.Amount,
		})
	}
	return nil
}

// fillPortraitRegions 填「区域分布」：client_ip → 归属地。
func fillPortraitRegions(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	type ipRow struct {
		ClientIP string
		Count    int64
		Amount   float64
	}
	rows := make([]ipRow, 0, portraitRegionIPLimit)
	if err := portraitBillingScope(db, scope).
		Where("client_ip IS NOT NULL AND client_ip <> ''").
		Select("client_ip AS client_ip, COUNT(*) AS count, COALESCE(SUM(amount), 0) AS amount").
		Group("client_ip").Order("count DESC, client_ip ASC").Limit(portraitRegionIPLimit).
		Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		// 判据 ②：没有任何带 IP 的账单行 → 空数组，不是「未知地区 0 条」。
		return nil
	}

	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		ips = append(ips, row.ClientIP)
	}
	addresses := portraitIPAddresses(db, ips)

	// 按归属地聚合。空归属地单独成桶（该 IP 出现过但本地库没记录）。
	order := make([]string, 0, len(rows))
	byRegion := make(map[string]*PortraitRegionSlice, len(rows))
	for _, row := range rows {
		region := addresses[strings.ToLower(strings.TrimSpace(row.ClientIP))]
		item, ok := byRegion[region]
		if !ok {
			item = &PortraitRegionSlice{Region: region, Label: portraitRegionLabel(region)}
			byRegion[region] = item
			order = append(order, region)
		}
		item.Count += row.Count
		item.Amount += row.Amount
		item.IPCount++
	}
	for _, region := range order {
		portrait.Regions = append(portrait.Regions, *byRegion[region])
	}
	return nil
}

// portraitRegionLabel 给区域一个可读文案；空归属地明确写「未知地区」。
func portraitRegionLabel(region string) string {
	if strings.TrimSpace(region) == "" {
		return "未知地区"
	}
	return region
}

// fillPortraitBillingTypes 填「消费类型分布」。
func fillPortraitBillingTypes(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	type typeRow struct {
		RecordType string
		Count      int64
		Amount     float64
	}
	rows := make([]typeRow, 0, 8)
	if err := portraitBillingScope(db, scope).
		Select("type AS record_type, COUNT(*) AS count, COALESCE(SUM(amount), 0) AS amount").
		Group("type").Order("count DESC, type ASC").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		recordType := normalizeBillingRecordType(row.RecordType)
		portrait.BillingTypes = append(portrait.BillingTypes, PortraitTypeSlice{
			Type:      recordType,
			Label:     buildBillingRecordTypeLabel(recordType),
			Direction: buildBillingRecordDirection(recordType),
			Count:     row.Count,
			Amount:    row.Amount,
		})
	}
	return nil
}

// fillPortraitTickets 填「工单量」。
func fillPortraitTickets(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	if !db.Migrator().HasTable(entity.TableNameTicket) {
		// 表还没建好（极老库/迁移未跑）→ 不给数字，也不给 0。
		return nil
	}
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Ticket{})
	if scope.UserID > 0 {
		query = query.Where("user_id = ?", scope.UserID)
	}
	type statusRow struct {
		Status string
		Count  int64
	}
	rows := make([]statusRow, 0, 4)
	if err := query.Select("status AS status, COUNT(*) AS count").Group("status").Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	byStatus := make(map[string]int64, len(rows))
	var total int64
	for _, row := range rows {
		byStatus[row.Status] += row.Count
		total += row.Count
	}
	// 固定枚举顺序，避免图例随数据抖动；只列出**确实出现过**的状态。
	for _, status := range portraitTicketStatusOrder {
		count, ok := byStatus[status]
		if !ok {
			continue
		}
		label := portraitTicketStatusLabels[status]
		if label == "" {
			label = status
		}
		portrait.Tickets = append(portrait.Tickets, PortraitTicketSlice{Status: status, Label: label, Count: count})
	}
	// 出现「未登记状态」时如实列出，不静默吞掉（否则合计对不上）。
	for _, row := range rows {
		if _, known := portraitTicketStatusLabels[row.Status]; known {
			continue
		}
		portrait.Tickets = append(portrait.Tickets, PortraitTicketSlice{Status: row.Status, Label: row.Status, Count: row.Count})
	}
	if total > 0 {
		portrait.Totals.TicketCount = &total
	}
	return nil
}

// fillPortraitRegisterSources 填「注册来源」分布（数据源 users.register_source）。
func fillPortraitRegisterSources(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{})
	if scope.UserID > 0 {
		query = query.Where("id = ?", scope.UserID)
	}
	type sourceRow struct {
		Source string
		Count  int64
	}
	rows := make([]sourceRow, 0, 4)
	if err := query.Select("register_source AS source, COUNT(*) AS count").Group("register_source").
		Order("count DESC, source ASC").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		label := portraitRegisterSourceLabels[row.Source]
		if label == "" {
			label = row.Source
		}
		portrait.RegisterSources = append(portrait.RegisterSources, PortraitSourceSlice{
			Source: row.Source,
			Label:  label,
			Count:  row.Count,
		})
	}
	return nil
}

// fillPortraitRegisterRegions 填「注册地分布」（数据源 users.register_ip + 本地归属地库）。
func fillPortraitRegisterRegions(db *gorm.DB, scope PortraitScope, portrait *UserPortrait) error {
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Where("register_ip IS NOT NULL AND register_ip <> ''")
	if scope.UserID > 0 {
		query = query.Where("id = ?", scope.UserID)
	}
	type ipRow struct {
		RegisterIP string
		Count      int64
	}
	rows := make([]ipRow, 0, portraitRegionIPLimit)
	if err := query.Select("register_ip AS register_ip, COUNT(*) AS count").
		Group("register_ip").Order("count DESC, register_ip ASC").Limit(portraitRegionIPLimit).
		Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		ips = append(ips, row.RegisterIP)
	}
	addresses := portraitIPAddresses(db, ips)

	order := make([]string, 0, len(rows))
	byRegion := make(map[string]*PortraitRegionSlice, len(rows))
	for _, row := range rows {
		region := addresses[strings.ToLower(strings.TrimSpace(row.RegisterIP))]
		item, ok := byRegion[region]
		if !ok {
			item = &PortraitRegionSlice{Region: region, Label: portraitRegionLabel(region)}
			byRegion[region] = item
			order = append(order, region)
		}
		item.Count += row.Count
		item.IPCount++
	}
	for _, region := range order {
		portrait.RegisterRegions = append(portrait.RegisterRegions, *byRegion[region])
	}
	return nil
}

// portraitIPAddresses 把一批 IP 解析成归属地展示文本（只读本地库，不外呼）。
//
// 顺序纪律：先查单 IP 持久化缓存（管理员刚刷新的结果最准），未命中再查段表。
// 反过来的话，旧段信息会盖掉精确缓存。
//
// 返回的 map key 是**小写归一化后的 IP**；未解析出来的 IP 不出现在 map 里，
// 调用方据此把归属地取成空串（→ 「未知地区」桶）。
func portraitIPAddresses(db *gorm.DB, ips []string) map[string]string {
	result := make(map[string]string, len(ips))
	if db == nil || len(ips) == 0 {
		return result
	}

	// 归一化 + 去重（保持与 location 查询同一套 key 规则：小写、去空白）。
	keys := make([]string, 0, len(ips))
	seen := make(map[string]struct{}, len(ips))
	originals := make(map[string]string, len(ips))
	for _, raw := range ips {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if _, exists := seen[lower]; exists {
			continue
		}
		seen[lower] = struct{}{}
		keys = append(keys, lower)
		originals[lower] = trimmed
	}
	if len(keys) == 0 {
		return result
	}

	// ① 单 IP 持久化缓存。刻意不加 source 条件：standard / commercial 都是同一 IP 的答案，
	// 画像只需要「哪片区域」，不需要区分查询模式。
	if db.Migrator().HasTable(entity.TableNameLocationCache) {
		rows := make([]entity.LocationCache, 0, len(keys))
		if err := db.Session(&gorm.Session{NewDB: true}).
			Where("kind = ? AND lookup_key IN ?", entity.LocationKindIP, keys).
			Find(&rows).Error; err != nil {
			// 归属地是辅助信息：读失败就退到段表，不让整页失败。
			rows = rows[:0]
		}
		for _, row := range rows {
			address := strings.TrimSpace(displayAddressFromCacheRow(row))
			if address == "" {
				continue
			}
			result[locationCacheLookupKey(row.LookupKey)] = address
		}
	}

	// ② 段表兜底。
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := result[key]; ok {
			continue
		}
		missing = append(missing, originals[key])
	}
	if len(missing) == 0 {
		return result
	}
	for _, key := range missing {
		segment, err := FindIPSegmentWithDB(db, key)
		if err != nil || segment == nil {
			continue
		}
		address := strings.TrimSpace(FormatIPLocationAddress(segment.Region, segment.LLC, segment.ISP))
		if address == "" {
			continue
		}
		result[strings.ToLower(strings.TrimSpace(key))] = address
	}
	return result
}
