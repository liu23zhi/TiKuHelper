package service

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 用户画像聚合层的行为契约（T-17 / task_doc/98 §1–§3）。
//
// 本文件刻意**全部用真 sqlite 内存库 + 真 GORM 查询**，不 mock：
// 待测点就是「这条 SQL 算出来对不对」，换成 mock 等于只验证「我们调了一个函数」。
//
// 四组测试对应任务书四条判据：
//
//	A 组（数据源可追溯）—— 每个数字都能指回一条 SQL；含时区陷阱与方向口径。
//	B 组（空数据语义）—— 空库返回空数组 / null，不是 0。
//	C 组（口径一致）  —— 整体画像 == 各单用户画像之和（共用同一套聚合函数）。
//	D 组（权限第 ③ 层）—— service 层只接受目标用户 ID，签名里没有「当前用户」。

const (
	// 写死字面量而不是引用常量：种子与断言都用同一个常量时，常量改坏会同步漂移，
	// 测试照样全绿（本项目已记录过这个陷阱）。
	portraitTestNormalSearchType = "normal_search"
	portraitTestRechargeType     = "recharge"
)

// newPortraitTestDB 建独立内存库（按 t.Name() 隔离），含画像会读到的四张表。
func newPortraitTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:portrait-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.User{},
		&entity.BillingRecord{},
		&entity.Ticket{},
		&entity.LocationCache{},
		&entity.IPSegment{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// portraitSeedUser 建一个账户。
func portraitSeedUser(t *testing.T, db *gorm.DB, id int32, username string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       username,
		Password:       "hashed",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

// portraitSeedBilling 直接落一条账单行。
//
// 不调真实业务函数：那需要满足登录/额度/价格的完整前置条件，会让「聚合算得对不对」
// 这个待测点被前置条件淹没。本文件测的就是聚合层，输入就是账单行本身。
func portraitSeedBilling(t *testing.T, db *gorm.DB, record entity.BillingRecord) {
	t.Helper()

	if record.RequestID == "" {
		// 不建唯一约束，但保持可读：便于失败时定位是哪一条种子行。
		record.RequestID = fmt.Sprintf("portrait_%d_%d", record.UserID, time.Now().UnixNano())
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("seed billing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// A 组：数据源可追溯 —— 每个数字都能指向具体 SQL
// ---------------------------------------------------------------------------

// TestPortraitHourBucketsUseWallClockHour 钉住「时间段分布按写入时的本地小时」。
//
// ⚠️ 这是本文件最重要的一条，因为它的失败**肉眼看不出来**：
// billing_records.created_at 在 SQLite 里存成 `2026-11-13 14:00:00+08:00` 这种带偏移的
// 文本，直接 `strftime('%H', created_at)` 会按 UTC 解析 → 14 点被算成 6 点。
// 整张「时间段分布」会整体偏移 8 小时，而图看上去完全正常。
//
// 变异方向：把 portraitHourExpr 里的 `substr(created_at, 1, 19)` 去掉 → 必红。
func TestPortraitHourBucketsUseWallClockHour(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 11, "portrait-hour")

	// 刻意取 14:00（UTC 6 点）与 23:00（UTC 15 点）：两者在 UTC 下都不等于自身。
	afternoon := time.Date(2026, 11, 13, 14, 0, 0, 0, portraitTestLocation(t))
	lateNight := time.Date(2026, 11, 13, 23, 0, 0, 0, portraitTestLocation(t))
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 1, CreatedAt: afternoon})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 2, CreatedAt: afternoon.Add(time.Minute)})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 4, CreatedAt: lateNight})

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}

	got := make(map[int]PortraitTimeBucket, len(portrait.Hours))
	for _, bucket := range portrait.Hours {
		got[bucket.Bucket] = bucket
	}
	if _, exists := got[14]; !exists {
		t.Fatalf("14:00 的两条账单必须落在 14 点桶（UTC 解析会落到 6 点桶），实际桶: %v", portraitBucketKeys(portrait.Hours))
	}
	if got[14].Count != 2 {
		t.Fatalf("14 点桶应有 2 条，实际 %d", got[14].Count)
	}
	if got[14].Amount != 3 {
		t.Fatalf("14 点桶金额应为 1+2=3，实际 %v", got[14].Amount)
	}
	if _, exists := got[23]; !exists {
		t.Fatalf("23:00 的账单必须落在 23 点桶，实际桶: %v", portraitBucketKeys(portrait.Hours))
	}
	if _, exists := got[6]; exists {
		t.Fatalf("出现 6 点桶 ⇒ created_at 被按 UTC 解析（时区陷阱复发），实际桶: %v", portraitBucketKeys(portrait.Hours))
	}
	// 桶总数必须是「真有数据的 2 个」，不是 24 个补零桶（判据 ②）。
	if len(portrait.Hours) != 2 {
		t.Fatalf("有数据时也只返回非空桶，期望 2 个，实际 %d: %v", len(portrait.Hours), portraitBucketKeys(portrait.Hours))
	}
}

func portraitBucketKeys(buckets []PortraitTimeBucket) []int {
	keys := make([]int, 0, len(buckets))
	for _, bucket := range buckets {
		keys = append(keys, bucket.Bucket)
	}
	return keys
}

// TestPortraitWeekdayBucketsMatchCalendarWeekday 钉住星期桶与真实日历一致。
//
// 2026-11-13 是周五：SQLite `%w` 与设计口径都是 0=周日…6=周六，所以应为 5。
// 若有人把 MySQL 分支的 DAYOFWEEK 直搬过来（1=周日…7=周六），会变成 6。
func TestPortraitWeekdayBucketsMatchCalendarWeekday(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 12, "portrait-weekday")

	friday := time.Date(2026, 11, 13, 10, 0, 0, 0, portraitTestLocation(t))
	if friday.Weekday() != time.Friday {
		t.Fatalf("前置假设错误：2026-11-13 应为周五，实际 %s", friday.Weekday())
	}
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 1, CreatedAt: friday})

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}
	if len(portrait.Weekdays) != 1 {
		t.Fatalf("应只有 1 个星期桶，实际 %d", len(portrait.Weekdays))
	}
	if portrait.Weekdays[0].Bucket != 5 {
		t.Fatalf("周五应落 bucket=5（0=周日），实际 %d（DAYOFWEEK 直搬会得 6）", portrait.Weekdays[0].Bucket)
	}
	if portrait.Weekdays[0].Label != "周五" {
		t.Fatalf("周五的文案应为「周五」，实际 %q", portrait.Weekdays[0].Label)
	}
}

// TestPortraitBillingTypesFollowSharedDirectionHelper 钉住消费类型分布与 direction 口径。
//
// direction 必须来自 buildBillingRecordDirection（账单明细页同一个函数），
// 不能在本文件里另写一套「哪些 type 算收入」的列表 —— 两套列表必然漂移。
func TestPortraitBillingTypesFollowSharedDirectionHelper(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 13, "portrait-type")
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, portraitTestLocation(t))

	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 2, CreatedAt: at})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 3, CreatedAt: at})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestRechargeType, Amount: 100, CreatedAt: at})

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}

	byType := make(map[string]PortraitTypeSlice, len(portrait.BillingTypes))
	for _, item := range portrait.BillingTypes {
		byType[item.Type] = item
	}
	expense, ok := byType["normal_search"]
	if !ok {
		t.Fatalf("normal_search 必须出现在消费类型分布里，实际 %v", portraitTypeKeys(portrait.BillingTypes))
	}
	if expense.Direction != "expense" {
		t.Fatalf("normal_search 的方向必须是 expense，实际 %q", expense.Direction)
	}
	if expense.Count != 2 || expense.Amount != 5 {
		t.Fatalf("normal_search 期望 2 条 / 金额 5，实际 %d 条 / %v", expense.Count, expense.Amount)
	}
	if expense.Label != "题目消费" {
		t.Fatalf("normal_search 的文案必须复用 buildBillingRecordTypeLabel（「题目消费」），实际 %q", expense.Label)
	}
	income, ok := byType["recharge"]
	if !ok || income.Direction != "income" {
		t.Fatalf("recharge 必须出现且方向为 income，实际 %+v", income)
	}

	// 消费行数必须只数支出方向：2 条 normal_search，不含那 1 条充值。
	if portrait.Totals.ConsumptionCount == nil || *portrait.Totals.ConsumptionCount != 2 {
		t.Fatalf("消费行数应为 2（不含充值），实际 %s", portraitInt(portrait.Totals.ConsumptionCount))
	}
	// 账单总行数含充值：3 条。
	if portrait.Totals.RecordCount == nil || *portrait.Totals.RecordCount != 3 {
		t.Fatalf("账单行数应为 3，实际 %s", portraitInt(portrait.Totals.RecordCount))
	}
	// 金额合计含全部方向：2+3+100=105。
	if portrait.Totals.Amount == nil || *portrait.Totals.Amount != 105 {
		t.Fatalf("金额合计应为 105，实际 %s", portraitFloat(portrait.Totals.Amount))
	}
}

func portraitTypeKeys(items []PortraitTypeSlice) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Type)
	}
	return keys
}

// TestPortraitRegionsResolveFromLocalLocationTables 钉住「区域分布来自本地归属地库」。
//
// 断言三件事：
//  1. 单 IP 缓存命中 → 用缓存的 address；
//  2. 缓存未命中但段表命中 → 用段信息（不外呼上游）；
//  3. 两者都没有的 IP → 单独归入「未知地区」桶，**不能丢弃**（否则区域合计小于账单行数）。
func TestPortraitRegionsResolveFromLocalLocationTables(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 14, "portrait-region")
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, portraitTestLocation(t))

	// ① 单 IP 缓存：中国 广东 中山 移动。
	cached := entity.LocationCache{
		Kind:      entity.LocationKindIP,
		LookupKey: "120.239.173.110",
		Source:    "",
		Address:   "中国 广东 中山",
		RawJSON:   `{"ip":"120.239.173.110","region":"中国 广东 中山","llc":"移动","isp":"中国移动"}`,
		FetchedAt: at,
	}
	if err := db.Create(&cached).Error; err != nil {
		t.Fatalf("seed location cache: %v", err)
	}

	// ② 段表：182.48.98.61 落在 182.48.98.0/24 段（中国 北京 联通）。
	segment := entity.IPSegment{
		BeginIP:   0, // 由 IPToUint 计算
		EndIP:     0,
		BeginText: "182.48.98.0",
		EndText:   "182.48.98.255",
		SampleIP:  "182.48.98.61",
		Region:    "中国 北京",
		ISP:       "中国联通",
		LLC:       "联通",
		Source:    "",
		FetchedAt: at,
	}
	begin, okBegin := ipv4ToUint32("182.48.98.0")
	end, okEnd := ipv4ToUint32("182.48.98.255")
	if !okBegin || !okEnd {
		t.Fatalf("前置：182.48.98.0/255 必须是合法 IPv4")
	}
	segment.BeginIP, segment.EndIP = begin, end
	if err := db.Create(&segment).Error; err != nil {
		t.Fatalf("seed ip segment: %v", err)
	}

	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 1, ClientIP: "120.239.173.110", CreatedAt: at})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 2, ClientIP: "182.48.98.61", CreatedAt: at})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: user.ID, Type: portraitTestNormalSearchType, Amount: 4, ClientIP: "203.0.113.9", CreatedAt: at})

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}
	if len(portrait.Regions) != 3 {
		t.Fatalf("三个 IP 应落在三个区域桶（含未知地区），实际 %d: %v", len(portrait.Regions), portraitRegionLabels(portrait.Regions))
	}
	byRegion := make(map[string]PortraitRegionSlice, len(portrait.Regions))
	for _, item := range portrait.Regions {
		byRegion[item.Label] = item
	}
	// 断言**具体文案**：这证明数据确实来自本地缓存/段表，而不是随手拼的 IP。
	if _, ok := byRegion["中国 广东 中山 移动"]; !ok {
		t.Fatalf("缓存命中区应解析为「中国 广东 中山 移动」，实际 %v", portraitRegionLabels(portrait.Regions))
	}
	if _, ok := byRegion["中国 北京 联通"]; !ok {
		t.Fatalf("段表命中区应解析为「中国 北京 联通」，实际 %v", portraitRegionLabels(portrait.Regions))
	}
	unknown, ok := byRegion["未知地区"]
	if !ok {
		t.Fatalf("未命中任何本地归属地库的 IP 必须单独成「未知地区」桶（不能丢弃），实际 %v", portraitRegionLabels(portrait.Regions))
	}
	if unknown.Count != 1 || unknown.Amount != 4 {
		t.Fatalf("未知地区桶应为 1 条 / 金额 4，实际 %d 条 / %v", unknown.Count, unknown.Amount)
	}
	// 区域合计必须等于账单行数：证明没有丢行。
	var total int64
	for _, item := range portrait.Regions {
		total += item.Count
	}
	if total != 3 {
		t.Fatalf("区域分布合计必须等于账单行数 3，实际 %d", total)
	}
}

func portraitRegionLabels(items []PortraitRegionSlice) []string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.Label)
	}
	return labels
}

// TestPortraitTicketsAndRegisterSources 钉住工单量与注册来源两条数据源。
func TestPortraitTicketsAndRegisterSources(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 15, "portrait-ticket")
	other := portraitSeedUser(t, db, 16, "portrait-ticket-other")
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, portraitTestLocation(t))

	tickets := []entity.Ticket{
		{UserID: user.ID, UserName: user.Username, Subject: "a", Status: entity.TicketStatusOpen, Priority: "normal", LastMessageAt: at},
		{UserID: user.ID, UserName: user.Username, Subject: "b", Status: entity.TicketStatusOpen, Priority: "normal", LastMessageAt: at},
		{UserID: user.ID, UserName: user.Username, Subject: "c", Status: entity.TicketStatusClosed, Priority: "normal", LastMessageAt: at},
		{UserID: other.ID, UserName: other.Username, Subject: "d", Status: entity.TicketStatusOpen, Priority: "normal", LastMessageAt: at},
	}
	for i := range tickets {
		if err := db.Create(&tickets[i]).Error; err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
	}

	single, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build single portrait: %v", err)
	}
	if single.Totals.TicketCount == nil || *single.Totals.TicketCount != 3 {
		t.Fatalf("该用户工单数应为 3（不含另一个用户的），实际 %s", portraitInt(single.Totals.TicketCount))
	}
	if len(single.Tickets) != 2 {
		t.Fatalf("该用户有两种工单状态，实际 %d 种: %+v", len(single.Tickets), single.Tickets)
	}
	// 固定枚举顺序：open 在 closed 之前。
	if single.Tickets[0].Status != entity.TicketStatusOpen || single.Tickets[0].Count != 2 {
		t.Fatalf("待处理应排第一且为 2 条，实际 %+v", single.Tickets[0])
	}
	if single.Tickets[0].Label != "待处理" {
		t.Fatalf("待处理文案应为「待处理」，实际 %q", single.Tickets[0].Label)
	}

	overall, err := BuildUserPortraitWithDB(db, PortraitScope{})
	if err != nil {
		t.Fatalf("build overall portrait: %v", err)
	}
	if overall.Totals.TicketCount == nil || *overall.Totals.TicketCount != 4 {
		t.Fatalf("整体工单数应为 4，实际 %s", portraitInt(overall.Totals.TicketCount))
	}
	// 注册来源：两个用户都是 public。
	if len(overall.RegisterSources) != 1 || overall.RegisterSources[0].Source != "public" || overall.RegisterSources[0].Count != 2 {
		t.Fatalf("整体注册来源应为 public×2，实际 %+v", overall.RegisterSources)
	}
	if overall.RegisterSources[0].Label != "公开注册" {
		t.Fatalf("public 的文案应为「公开注册」，实际 %q", overall.RegisterSources[0].Label)
	}
}

// ---------------------------------------------------------------------------
// B 组：空数据语义 —— 空数组 / null，不是 0
// ---------------------------------------------------------------------------

// TestPortraitEmptyDataReturnsEmptySlicesAndNullTotals 钉住判据 ②。
//
// 「没有数据」与「数据是 0」在饼图上完全不同：`[]` 显示「暂无数据」，
// `[{bucket:0,count:0},…24 项]` 会画出 24 个零值扇区。
// 标量同理：null（暂无数据）不能让前端渲染成 0 元消费。
func TestPortraitEmptyDataReturnsEmptySlicesAndNullTotals(t *testing.T) {
	db := newPortraitTestDB(t)
	portraitSeedUser(t, db, 21, "portrait-empty")

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: 21})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}

	assertEmptySlice(t, "hours", len(portrait.Hours))
	assertEmptySlice(t, "weekdays", len(portrait.Weekdays))
	assertEmptySlice(t, "regions", len(portrait.Regions))
	assertEmptySlice(t, "billingTypes", len(portrait.BillingTypes))
	assertEmptySlice(t, "tickets", len(portrait.Tickets))
	assertEmptySlice(t, "registerRegions", len(portrait.RegisterRegions))

	if portrait.Totals.RecordCount != nil {
		t.Fatalf("无账单时 recordCount 必须是 null，实际 %d", *portrait.Totals.RecordCount)
	}
	if portrait.Totals.Amount != nil {
		t.Fatalf("无账单时 amount 必须是 null（不能是 0），实际 %v", *portrait.Totals.Amount)
	}
	if portrait.Totals.ConsumptionCount != nil {
		t.Fatalf("无账单时 consumptionCount 必须是 null，实际 %d", *portrait.Totals.ConsumptionCount)
	}
	if portrait.Totals.TicketCount != nil {
		t.Fatalf("无工单时 ticketCount 必须是 null，实际 %d", *portrait.Totals.TicketCount)
	}
	// 序列化形态也要钉住：nil 指针 → JSON null，空切片 → []，不是 {} 或省略。
	if !strings.Contains(portraitJSON(t, portrait), `"hours":[]`) {
		t.Fatalf("hours 的空值必须序列化成 []（否则前端拿到 null 会崩）")
	}
}

func assertEmptySlice(t *testing.T, name string, length int) {
	t.Helper()
	if length != 0 {
		t.Fatalf("%s 在无数据时必须是长度 0 的切片（不是补零桶），实际 %d", name, length)
	}
}

// TestPortraitOverallWithNoBillingStillReportsUserCount 钉住「有用户、无账单」的混合态。
//
// 这条覆盖的是最容易被写错的一格：UserCount 有值（确实数过），而 RecordCount 是 null
// （确实没有账单）。两者不能都归成 0，否则「站里还没人用」与「站里一个人都没有」同形。
func TestPortraitOverallWithNoBillingStillReportsUserCount(t *testing.T) {
	db := newPortraitTestDB(t)
	portraitSeedUser(t, db, 22, "portrait-overall-empty")

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{})
	if err != nil {
		t.Fatalf("build overall portrait: %v", err)
	}
	if portrait.Scope != "overall" || portrait.TargetUserID != 0 {
		t.Fatalf("整体画像的 scope/targetUserId 不对: %q / %d", portrait.Scope, portrait.TargetUserID)
	}
	if portrait.Totals.UserCount == nil || *portrait.Totals.UserCount != 1 {
		t.Fatalf("用户总数应为 1（确实数过），实际 %s", portraitInt(portrait.Totals.UserCount))
	}
	if portrait.Totals.RecordCount != nil {
		t.Fatalf("无账单时 recordCount 仍必须是 null，实际 %d", *portrait.Totals.RecordCount)
	}
	if portrait.Totals.ActiveUserCount != nil {
		t.Fatalf("无账单时活跃用户数必须是 null，实际 %d", *portrait.Totals.ActiveUserCount)
	}
}

// TestPortraitRejectsMissingTargetUser 钉住「查错人」与「这个人没行为」必须可区分。
//
// 不存在的 ID 若返回空画像，前端会显示「该用户暂无数据」——运营会以为账号是空的，
// 实际是 ID 传错了。所以必须是 404（service 层 ErrPortraitUserNotFound）。
func TestPortraitRejectsMissingTargetUser(t *testing.T) {
	db := newPortraitTestDB(t)

	_, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: 999})
	if err == nil {
		t.Fatalf("不存在的目标用户必须报错，不能返回空画像")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("错误语义应为「用户不存在」，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// C 组：口径一致 —— 整体画像 == 各单用户画像之和
// ---------------------------------------------------------------------------

// TestPortraitOverallEqualsSumOfUsers 钉住判据 ④（整体与单用户共用同一套聚合函数）。
//
// 断言形式刻意选「守恒」而不是「等于某个绝对值」：绝对值只证明这一份种子数据算对了，
// 守恒才能证明**两条入口走的是同一套 SQL**。
//
// 变异方向：让整体画像改走另一条 SQL（例如整体用 COUNT(DISTINCT user_id)、
// 或整体多加一个 WHERE status='enabled'）→ 守恒立刻被打破，本用例必红。
func TestPortraitOverallEqualsSumOfUsers(t *testing.T) {
	db := newPortraitTestDB(t)
	first := portraitSeedUser(t, db, 31, "portrait-sum-a")
	second := portraitSeedUser(t, db, 32, "portrait-sum-b")

	at := time.Date(2026, 11, 13, 15, 0, 0, 0, portraitTestLocation(t))
	// 两个用户、两个不同小时、两种不同类型，保证任何「少算某一维」都会破坏守恒。
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: first.ID, Type: portraitTestNormalSearchType, Amount: 1.5, ClientIP: "120.239.173.110", CreatedAt: at})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: first.ID, Type: portraitTestNormalSearchType, Amount: 2.5, ClientIP: "120.239.173.110", CreatedAt: at})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: second.ID, Type: portraitTestRechargeType, Amount: 40, ClientIP: "182.48.98.61", CreatedAt: at.Add(2 * time.Hour)})
	portraitSeedBilling(t, db, entity.BillingRecord{UserID: second.ID, Type: portraitTestNormalSearchType, Amount: 0.5, ClientIP: "203.0.113.9", CreatedAt: at.Add(2 * time.Hour)})

	overall, err := BuildUserPortraitWithDB(db, PortraitScope{})
	if err != nil {
		t.Fatalf("build overall: %v", err)
	}
	singleA, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: first.ID})
	if err != nil {
		t.Fatalf("build single A: %v", err)
	}
	singleB, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: second.ID})
	if err != nil {
		t.Fatalf("build single B: %v", err)
	}

	// ① 标量守恒。
	if overall.Totals.RecordCount == nil || *overall.Totals.RecordCount != 4 {
		t.Fatalf("整体账单行数应为 4，实际 %s", portraitInt(overall.Totals.RecordCount))
	}
	sumRecords := *singleA.Totals.RecordCount + *singleB.Totals.RecordCount
	if sumRecords != *overall.Totals.RecordCount {
		t.Fatalf("账单行数不守恒：整体 %d != 单用户之和 %d（说明两条入口没共用同一套查询）",
			*overall.Totals.RecordCount, sumRecords)
	}
	if *overall.Totals.Amount != *singleA.Totals.Amount+*singleB.Totals.Amount {
		t.Fatalf("金额不守恒：整体 %s != %s + %s", portraitFloat(overall.Totals.Amount), portraitFloat(singleA.Totals.Amount), portraitFloat(singleB.Totals.Amount))
	}

	// ② 小时桶守恒：逐桶比较（只比出现过的桶，两边都只返回非空桶）。
	overallHours := make(map[int]int64, len(overall.Hours))
	for _, bucket := range overall.Hours {
		overallHours[bucket.Bucket] = bucket.Count
	}
	sumHours := make(map[int]int64, len(overall.Hours))
	for _, single := range []UserPortrait{singleA, singleB} {
		for _, bucket := range single.Hours {
			sumHours[bucket.Bucket] += bucket.Count
		}
	}
	if !reflect.DeepEqual(overallHours, sumHours) {
		t.Fatalf("小时桶不守恒：整体 %v != 单用户之和 %v", overallHours, sumHours)
	}

	// ③ 类型桶守恒。
	overallTypes := make(map[string]int64, len(overall.BillingTypes))
	for _, item := range overall.BillingTypes {
		overallTypes[item.Type] = item.Count
	}
	sumTypes := make(map[string]int64, len(overall.BillingTypes))
	for _, single := range []UserPortrait{singleA, singleB} {
		for _, item := range single.BillingTypes {
			sumTypes[item.Type] += item.Count
		}
	}
	if !reflect.DeepEqual(overallTypes, sumTypes) {
		t.Fatalf("消费类型桶不守恒：整体 %v != 单用户之和 %v", overallTypes, sumTypes)
	}

	// ④ 整体画像不该带单用户的 Base。
	if overall.Base != nil {
		t.Fatalf("整体画像的 base 必须是 null，实际 %+v", overall.Base)
	}
	if singleA.Base == nil || singleA.Base.Username != "portrait-sum-a" {
		t.Fatalf("单用户画像必须带自己的 base（username=portrait-sum-a），实际 %+v", singleA.Base)
	}
}

// ---------------------------------------------------------------------------
// D 组：权限第 ③ 层 —— service 层只接受「目标用户 ID」
// ---------------------------------------------------------------------------

// TestPortraitScopeAcceptsOnlyTargetUserID 是 98 号 §0 第 ③ 层的**签名审查**用例。
//
// 要求：查询函数的入参里不能有「当前用户 ID / 操作者」这种东西，否则迟早有人
// 把「查自己」和「查别人」写成同一个调用点。做法是把范围收成一个单字段结构体，
// 并用反射钉住它只有 UserID 一个字段。
//
// 变异方向：给 PortraitScope 加一个 OperatorUserID / CurrentUserID 字段 → 必红。
func TestPortraitScopeAcceptsOnlyTargetUserID(t *testing.T) {
	scopeType := reflect.TypeOf(PortraitScope{})
	if scopeType.NumField() != 1 {
		fields := make([]string, 0, scopeType.NumField())
		for i := 0; i < scopeType.NumField(); i++ {
			fields = append(fields, scopeType.Field(i).Name)
		}
		t.Fatalf("PortraitScope 必须只有 UserID 一个字段（防止被误用成「查自己」），实际 %v", fields)
	}
	if name := scopeType.Field(0).Name; name != "UserID" {
		t.Fatalf("PortraitScope 的唯一字段必须是 UserID（目标用户），实际 %q", name)
	}

	// 查询函数的签名必须只有 (db, scope)：没有第三个「操作者」参数。
	buildType := reflect.TypeOf(BuildUserPortraitWithDB)
	if buildType.NumIn() != 2 {
		t.Fatalf("BuildUserPortraitWithDB 必须只接受 (db, scope) 两个参数，实际 %d 个", buildType.NumIn())
	}
	if buildType.In(1) != scopeType {
		t.Fatalf("第二个参数必须是 PortraitScope，实际 %v", buildType.In(1))
	}
	// 且不得存在「只有自己」的快捷函数：那等于把「查自己」和「查别人」写成同一个入口。
	// 这条用**源码扫描**而不是反射：Go 的反射查不到「某个函数不存在」，
	// 写成一个恒 false 的 map 就是假钉子（永远绿）。这里真的去读包目录里的 .go 文件。
	for _, forbidden := range []string{"BuildSelfPortrait", "BuildMyPortrait", "BuildPortraitForCurrentUser"} {
		if found, file := servicePackageDeclaresFunc(t, forbidden); found {
			t.Fatalf("service 包不得声明 %s（出现在 %s）：那是「查自己」的入口，98 号 §0 禁止", forbidden, file)
		}
	}
}

// servicePackageDeclaresFunc 扫描本包目录下所有非测试 .go 文件，判断是否声明了
// `func <name>(` 或 `func (..) <name>(`。
//
// 为什么要真扫源码：判据是「这个符号不存在」，而「不存在」无法用语言内建断言表达。
// 一旦写成 `knownForbidden[name] = false` 那种恒假表，测试永远绿、永远无用。
func servicePackageDeclaresFunc(t *testing.T, name string) (bool, string) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 service 包目录失败: %v", err)
	}
	methodPattern := regexp.MustCompile(`func\s+\([^)]*\)\s+` + regexp.QuoteMeta(name) + `\s*\(`)
	funcPattern := regexp.MustCompile(`func\s+` + regexp.QuoteMeta(name) + `\s*\(`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		content, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", entry.Name(), err)
		}
		text := string(content)
		if funcPattern.MatchString(text) || methodPattern.MatchString(text) {
			return true, entry.Name()
		}
	}
	return false, ""
}

// portraitJSON 把画像序列化一次，用于断言「空值到底是 [] 还是 null」。
func portraitJSON(t *testing.T, portrait UserPortrait) string {
	t.Helper()

	encoded, err := json.Marshal(portrait)
	if err != nil {
		t.Fatalf("序列化画像失败: %v", err)
	}
	return string(encoded)
}

// portraitInt / portraitFloat 把画像里的「可空标量」打成可读文案。
//
// 直接 `%v` 打指针会输出地址（实测 "实际 0xcb3bfbd2fa0"），失败信息毫无用处 ——
// 一个看不懂的失败信息等于这条断言在排查时不存在。
func portraitInt(value *int64) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatInt(*value, 10)
}

func portraitFloat(value *float64) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

// portraitTestLocation 返回画像测试专用的**显式时区**（上海）。
//
// ⚠️ 为什么不能用 time.Local（这是 CI 上抓出来的真实缺陷）：
//
// 画像的时段/星期桶按 `datetime(created_at,'+8 hours')` 换算到上海时区。
// 旧测试用 `time.Date(..., time.Local)` 起种子 —— GORM 按**进程本地时区**序列化：
//
//	开发机 CST：种出 `2026-11-13 14:00:00+08:00` → +8 后仍是 14 点 → 断言过
//	CI   UTC ：种出 `2026-11-13 14:00:00+00:00` → +8 后变成 22 点 → 断言红
//
// 也就是说这些用例的**期望值会随运行环境漂移**，本地永远绿、CI 才会露出来。
// 显式指定上海时区后，种子与断言在两种环境里都一致。
func portraitTestLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}
	return loc
}
