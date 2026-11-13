package service

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 公告 service 测试（N4，task_doc/112 §4.3 + 113 + 121 §4.5）。
//
// 断言的重心在三处**容易写错且错了不报错**的地方：
//
//  1. 下发只带 kind=incident —— 把 normal 带出去会让 PLUS 弹出「系统维护通知」，
//     而 113 §8.1 明确 normal 不下发；
//  2. 时间窗边界 —— starts_at 闭、ends_at 开。写反会让公告早生效或晚失效，
//     表现是「管理员设了 14:00–14:30，14:29 就没提示了」；
//  3. Enabled 零值语义 —— 实体上刻意不写 `default:true`（本仓在 review_rebate /
//     blacklist 上实测过：带 default 时 GORM 会把 false 从 INSERT 里省掉，
//     于是「停用」被静默写成「启用」）。这条必须用**真实写库再读回**来验，
//     手搓 struct 自比永远绿。

// announcementDBSeq 保证每个用例拿到独立内存库（同名 shared cache 会互相污染）。
var announcementDBSeq atomic.Int64

// newAnnouncementTestDB 建一个只含 announcements 的内存库。
//
// DSN 用 util.SQLiteConcurrentDSN：与生产 sqliteProductionDSN 共用同一份定义
// （本仓的既有纪律）。本文件的用例不含并发，但「用默认 DSN 测出假绿」这个坑
// 一旦有人后续加并发用例就会踩到，所以从一开始就对齐生产。
func newAnnouncementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(
		"file:announcement-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
			"-" + strconv.FormatInt(announcementDBSeq.Add(1), 10) + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.Announcement{}); err != nil {
		t.Fatalf("migrate announcements: %v", err)
	}
	return db
}

// announcementPtr 返回值的指针（避免各处重复写局部变量）。
func announcementPtr[T any](value T) *T { return &value }

// announcementAt 是测试里的固定「现在」。
//
// 用固定时刻而不是 time.Now()：窗口边界断言必须确定性，否则用例在
// 毫秒级边界上会偶发变红（本仓在 epoch 窗口相关用例上踩过）。
var announcementAt = time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)

// 下发只带 kind=incident、enabled=true、且落在时间窗内的公告。
func TestAnnouncementActiveIncidentsOnlyReturnsEnabledIncidentInWindow(t *testing.T) {
	db := newAnnouncementTestDB(t)
	now := announcementAt

	seed := []entity.Announcement{
		// 命中：incident + enabled + 窗口内（starts 在过去，ends 在未来）
		{Kind: entity.AnnouncementKindIncident, Title: "题库服务异常", Content: "正在抢修",
			Severity: entity.AnnouncementSeverityCritical, Enabled: true,
			StartsAt: now.Add(-time.Minute), EndsAt: announcementPtr(now.Add(30 * time.Minute))},
		// 不命中：normal 不下发（113 §8.1）
		{Kind: entity.AnnouncementKindNormal, Title: "系统维护通知", Content: "11-20 维护",
			Severity: entity.AnnouncementSeverityInfo, Enabled: true, StartsAt: now.Add(-time.Minute)},
		// 不命中：停用
		{Kind: entity.AnnouncementKindIncident, Title: "已关闭的故障", Content: "x",
			Severity: entity.AnnouncementSeverityWarning, Enabled: false, StartsAt: now.Add(-time.Minute)},
		// 不命中：窗口还没开始
		{Kind: entity.AnnouncementKindIncident, Title: "未来故障", Content: "x",
			Severity: entity.AnnouncementSeverityInfo, Enabled: true, StartsAt: now.Add(time.Minute)},
		// 不命中：窗口已结束
		{Kind: entity.AnnouncementKindIncident, Title: "过期故障", Content: "x",
			Severity: entity.AnnouncementSeverityInfo, Enabled: true,
			StartsAt: now.Add(-time.Hour), EndsAt: announcementPtr(now.Add(-time.Second))},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	rows, err := ActiveIncidents(db, now)
	if err != nil {
		t.Fatalf("ActiveIncidents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("只应下发 1 条 incident，实际 %d 条：%+v", len(rows), rows)
	}
	if rows[0].Title != "题库服务异常" {
		t.Fatalf("下发了错误的公告：%q", rows[0].Title)
	}
}

// 时间窗边界：starts_at 闭区间（等于起点即生效），ends_at 开区间（等于终点即失效）。
//
// 写反的后果不对称：ends_at 变成闭区间会让公告在结束时刻多生效一秒，
// 表现是「14:30 关掉的公告，14:30:00 还在弹」——没人会怀疑到边界比较符上。
func TestAnnouncementActiveIncidentsWindowBoundaries(t *testing.T) {
	db := newAnnouncementTestDB(t)
	start := announcementAt
	end := start.Add(30 * time.Minute)

	if err := db.Create(&entity.Announcement{
		Kind: entity.AnnouncementKindIncident, Title: "边界公告", Content: "x",
		Severity: entity.AnnouncementSeverityInfo, Enabled: true,
		StartsAt: start, EndsAt: &end,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		at      time.Time
		visible bool
	}{
		{"起点前一秒不可见", start.Add(-time.Second), false},
		{"起点当刻可见（starts_at 闭区间）", start, true},
		{"窗口中间可见", start.Add(15 * time.Minute), true},
		{"终点当刻不可见（ends_at 开区间）", end, false},
		{"终点后一秒不可见", end.Add(time.Second), false},
	}
	for _, tc := range cases {
		rows, err := ActiveIncidents(db, tc.at)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := len(rows) == 1
		if got != tc.visible {
			t.Fatalf("%s：期望 visible=%v，实际 %v（rows=%d）", tc.name, tc.visible, got, len(rows))
		}
	}
}

// ends_at 为 nil 表示永久有效（112 §4.3）。
func TestAnnouncementActiveIncidentsNilEndsAtIsPermanent(t *testing.T) {
	db := newAnnouncementTestDB(t)
	if err := db.Create(&entity.Announcement{
		Kind: entity.AnnouncementKindIncident, Title: "永久故障", Content: "x",
		Severity: entity.AnnouncementSeverityInfo, Enabled: true,
		StartsAt: announcementAt.Add(-24 * time.Hour), EndsAt: nil,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := ActiveIncidents(db, announcementAt.Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("ActiveIncidents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ends_at 为空应当永久有效，实际 %d 条", len(rows))
	}
}

// 排序：critical 必须排在 info 之前 —— 因为 PLUS 只弹第一条（113 §6 N4-g）。
//
// 这条**不能**只靠 Go 侧排序断言：Limit 在 SQL 里生效，若先取 10 条再排序，
// 一旦生效公告超过 10 条，critical 就会被后插入的 info 挤出窗口。
// 所以用例要造「超过 Limit 条 info + 1 条 critical」。
func TestAnnouncementActiveIncidentsOrdersCriticalFirst(t *testing.T) {
	db := newAnnouncementTestDB(t)
	// 先插 12 条 info（超过 ActiveIncidentsLimit），最后插 1 条 critical。
	// 若排序不在 SQL 里，critical 会被 LIMIT 截掉。
	for i := 0; i < 12; i++ {
		if err := db.Create(&entity.Announcement{
			Kind: entity.AnnouncementKindIncident, Title: "普通故障 " + strconv.Itoa(i), Content: "x",
			Severity: entity.AnnouncementSeverityInfo, Enabled: true, StartsAt: announcementAt.Add(-time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed info %d: %v", i, err)
		}
	}
	if err := db.Create(&entity.Announcement{
		Kind: entity.AnnouncementKindIncident, Title: "严重故障", Content: "x",
		Severity: entity.AnnouncementSeverityCritical, Enabled: true, StartsAt: announcementAt.Add(-time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed critical: %v", err)
	}

	rows, err := ActiveIncidents(db, announcementAt)
	if err != nil {
		t.Fatalf("ActiveIncidents: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("应当有条目")
	}
	if rows[0].Severity != entity.AnnouncementSeverityCritical {
		t.Fatalf("第一条必须是 critical（PLUS 只弹第一条），实际 %q", rows[0].Severity)
	}
	if len(rows) > ActiveIncidentsLimit {
		t.Fatalf("下发条数不得超过 %d，实际 %d", ActiveIncidentsLimit, len(rows))
	}
}

// fail-open：查不到（表不存在）时返回空而不是把错误扔给搜题主链路。
//
// 113 §4 的反面：公告是附加信息，绝不能因为「通知查不到」让用户搜不了题。
func TestAnnouncementActiveIncidentsOrEmptyFailsOpenWhenTableMissing(t *testing.T) {
	dsn := util.SQLiteConcurrentDSN(
		"file:announcement-failopen-" + strconv.FormatInt(announcementDBSeq.Add(1), 10) + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 刻意不建表：模拟「迁移还没跑」或「库抖动」。
	if _, err := ActiveIncidents(db, announcementAt); err == nil {
		t.Fatal("ActiveIncidents 必须把错误交出来（结构性问题要能被看见）")
	}
	if rows := ActiveIncidentsOrEmpty(db, announcementAt); len(rows) != 0 {
		t.Fatalf("ActiveIncidentsOrEmpty 必须 fail-open 返回空，实际 %d 条", len(rows))
	}
	if rows := ActiveIncidentsOrEmpty(nil, announcementAt); rows != nil {
		t.Fatalf("nil 库必须返回 nil，实际 %+v", rows)
	}
}

// Enabled=false 必须真的落库为 false。
//
// 这是本仓踩过三次的坑（review_rebate / blacklist）：实体上带 `default:true`
// 时 GORM 会把「等于默认值」的字段从 INSERT 里省掉，于是停用的行被写成启用。
// 本用例必须走真实 Create + 真实读回，不能手搓 struct 自比。
func TestCreateAnnouncementPersistsDisabledAsDisabled(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "停用的故障", Content: "x",
		Enabled: announcementPtr(false),
	}, 7, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Enabled {
		t.Fatal("Created 返回值里 enabled 应为 false")
	}
	var reloaded entity.Announcement
	if err := db.Where("id = ?", created.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Enabled {
		t.Fatal("Enabled=false 必须真的落库为 false —— 出现 true 说明实体上带了 default:true 标签" +
			"把零值从 INSERT 里省掉了（本仓 review_rebate / blacklist 踩过的同一个坑）")
	}
	if reloaded.CreatedBy != 7 {
		t.Fatalf("created_by 未落库：%d", reloaded.CreatedBy)
	}
	// 停用的行不该进下发。
	if rows, err := ActiveIncidents(db, announcementAt); err != nil || len(rows) != 0 {
		t.Fatalf("停用公告不该下发：rows=%d err=%v", len(rows), err)
	}
}

// 新建缺省值：kind=normal、severity=info、enabled=true、startsAt=now、endsAt=nil。
func TestCreateAnnouncementDefaults(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Title: "  默认公告  ", Content: "  正文  ",
	}, 0, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Kind != entity.AnnouncementKindNormal {
		t.Fatalf("kind 缺省应为 normal（不填就不要影响 PLUS），实际 %q", created.Kind)
	}
	if created.Severity != entity.AnnouncementSeverityInfo {
		t.Fatalf("severity 缺省应为 info，实际 %q", created.Severity)
	}
	if !created.Enabled {
		t.Fatal("enabled 缺省应为 true（建了就该生效）")
	}
	if !created.StartsAt.Equal(announcementAt) {
		t.Fatalf("startsAt 缺省应为 now，实际 %v", created.StartsAt)
	}
	if created.EndsAt != nil {
		t.Fatal("endsAt 缺省应为 nil（永久有效）")
	}
	if created.Title != "默认公告" || created.Content != "正文" {
		t.Fatalf("标题/正文的首尾空白应被去掉：%q / %q", created.Title, created.Content)
	}
	// normal 不下发（113 §8.1）——缺省 kind 恰好是最安全的那一个。
	//
	// ⚠️ 这条断言在本函数里**只是纵深防御**，不能作为「默认值安全」的唯一防线：
	// 它排在六条字段断言之后，只要 kind 的缺省值被改成 incident，
	// L267 就会先 t.Fatalf 退出 —— 下面这条行为断言**永远执行不到**。
	// 真正的行为钉子见 TestAnnouncementDefaultKindNeverReachesSearchResponse。
	if rows, err := ActiveIncidents(db, announcementAt); err != nil || len(rows) != 0 {
		t.Fatalf("normal 公告不该下发：rows=%d err=%v", len(rows), err)
	}
}

// 默认值安全边界：**新建公告不传 kind 时，它绝不能进入搜题响应**。
//
// 为什么必须单独一条，而不是靠 TestCreateAnnouncementDefaults 的末尾那条断言：
// 那个函数先断 6 个字段（kind/severity/enabled/startsAt/endsAt/trim），
// 全部用 t.Fatalf。一旦 kind 的缺省值被改成 incident，第一条字段断言就退出，
// 末尾的「不该下发」根本执行不到 —— 于是**真正的安全边界在行为层没有任何断言**
// （变异实测：只红 1 条，且红的是字段断言而非行为断言）。
//
// 本条用例的设计约束（这就是它比上一条强的全部原因）：
//   - 只断**行为**，不断任何字段 —— 缺省 kind 是 normal 还是别的东西都不关心；
//   - 唯一判据是「搜题响应里有没有它」；
//   - 因此在「缺省 kind 被改成 incident」的变异下必然红。
//
// 这是 113 §8.1「常规公告绝不下发」的唯一守门员：
// 把缺省值翻成 incident = 每一条新建公告都自动外发到所有用户的搜题响应。
func TestAnnouncementDefaultKindNeverReachesSearchResponse(t *testing.T) {
	db := newAnnouncementTestDB(t)

	// 完全不传 kind / severity / enabled / startsAt / endsAt：这是「最省事的新建」，
	// 也是运营最可能实际提交的形态。
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Title: "随手发的一条公告", Content: "这条不该出现在 PLUS 的搜题响应里",
	}, 0, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("create 未落库：%+v", created)
	}

	// 唯一断言：下发查询必须拿不到它。
	// 时间点取「刚建好之后」，确保不是因为窗口没生效才查不到。
	rows, err := ActiveIncidents(db, announcementAt.Add(time.Second))
	if err != nil {
		t.Fatalf("ActiveIncidents: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("缺省 kind 的公告被下发到了搜题响应（%d 条，首条 kind=%q title=%q）—— "+
			"「不填 kind」必须是最安全的默认（不进 PLUS），"+
			"否则每一条随手发的常规公告都会自动外发给所有用户（113 §8.1）",
			len(rows), rows[0].Kind, rows[0].Title)
	}

	// 反向对照：同一条公告若能通过显式 incident 下发，说明「查不到」不是
	// 因为查询本身坏了（否则本条用例在「ActiveIncidents 恒返回空」的实现下也会绿）。
	incident, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "真故障", Content: "这条该下发",
	}, 0, announcementAt)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	rows, err = ActiveIncidents(db, announcementAt.Add(time.Second))
	if err != nil {
		t.Fatalf("ActiveIncidents (incident): %v", err)
	}
	if len(rows) != 1 || rows[0].ID != incident.ID {
		t.Fatalf("显式 incident 的公告必须能下发（对照组）：rows=%d", len(rows))
	}
}

// 校验：五类非法输入必须各自返回对应哨兵错误，绝不静默改正。
func TestAnnouncementValidation(t *testing.T) {
	db := newAnnouncementTestDB(t)
	now := announcementAt
	cases := []struct {
		name  string
		input AnnouncementInput
		want  error
	}{
		{"空标题", AnnouncementInput{Title: "  ", Content: "x"}, ErrAnnouncementTitleRequired},
		{"空正文", AnnouncementInput{Title: "t", Content: "  "}, ErrAnnouncementContentRequired},
		{"非法 kind", AnnouncementInput{Title: "t", Content: "x", Kind: "maintenance"}, ErrAnnouncementKindInvalid},
		{"非法 severity", AnnouncementInput{Title: "t", Content: "x", Severity: "urgent"}, ErrAnnouncementSeverityInvalid},
		{"结束早于开始", AnnouncementInput{
			Title: "t", Content: "x",
			StartsAt: announcementPtr(now), EndsAt: announcementPtr(now.Add(-time.Minute)),
		}, ErrAnnouncementWindowInvalid},
		{"零长度窗口", AnnouncementInput{
			Title: "t", Content: "x",
			StartsAt: announcementPtr(now), EndsAt: announcementPtr(now),
		}, ErrAnnouncementWindowInvalid},
	}
	for _, tc := range cases {
		if _, err := CreateAnnouncement(db, tc.input, 1, now); !errors.Is(err, tc.want) {
			t.Fatalf("%s：期望 %v，实际 %v", tc.name, tc.want, err)
		}
	}
}

// kind/severity 大小写与空白要被归一化，而不是报错。
//
// 管理页的下拉框可能给出 "INCIDENT" 或 " incident "；这些语义明确，应当接受。
func TestAnnouncementNormalizesKindAndSeverityCase(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: " INCIDENT ", Title: "t", Content: "x", Severity: " Critical ",
	}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Kind != entity.AnnouncementKindIncident {
		t.Fatalf("kind 应归一化为 incident，实际 %q", created.Kind)
	}
	if created.Severity != entity.AnnouncementSeverityCritical {
		t.Fatalf("severity 应归一化为 critical，实际 %q", created.Severity)
	}
}

// 编辑：不传 enabled 时保持原值；传了就覆盖。
//
// 「编辑正文时不该顺手把开关改掉」是运营最常见的手误来源。
func TestUpdateAnnouncementKeepsEnabledWhenOmitted(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "原标题", Content: "原正文",
		Enabled: announcementPtr(false),
	}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 不传 Enabled 的编辑
	kept, err := UpdateAnnouncement(db, created.ID, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "新标题", Content: "新正文",
	}, announcementAt)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if kept.Enabled {
		t.Fatal("编辑未传 enabled 时不得改动开关（原值为 false）")
	}
	if kept.Title != "新标题" {
		t.Fatalf("标题应被更新，实际 %q", kept.Title)
	}
	// 显式传 true 覆盖
	enabled, err := UpdateAnnouncement(db, created.ID, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "新标题", Content: "新正文",
		Enabled: announcementPtr(true),
	}, announcementAt)
	if err != nil {
		t.Fatalf("update enabled: %v", err)
	}
	if !enabled.Enabled {
		t.Fatal("显式传 enabled=true 必须生效")
	}
}

// 编辑：不传时间窗时保留原窗口（不能把已设的结束时间抹成永久）。
func TestUpdateAnnouncementKeepsWindowWhenOmitted(t *testing.T) {
	db := newAnnouncementTestDB(t)
	endsAt := announcementAt.Add(30 * time.Minute)
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "t", Content: "x",
		StartsAt: announcementPtr(announcementAt.Add(-time.Hour)), EndsAt: &endsAt,
	}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := UpdateAnnouncement(db, created.ID, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "t2", Content: "x2",
	}, announcementAt)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.EndsAt == nil {
		t.Fatal("编辑未传 endsAt 时不得把结束时间抹成「永久有效」")
	}
	if !updated.EndsAt.Equal(endsAt) {
		t.Fatalf("结束时间被改动：%v != %v", updated.EndsAt, endsAt)
	}
	if !updated.StartsAt.Equal(announcementAt.Add(-time.Hour)) {
		t.Fatalf("开始时间被改动：%v", updated.StartsAt)
	}
}

// 时间一律归一到 UTC 且截断到秒 —— 否则 SQLite 的字符串比较会给出错误先后关系。
func TestAnnouncementTimesNormalizedToUTCSeconds(t *testing.T) {
	db := newAnnouncementTestDB(t)
	shanghai := time.FixedZone("CST", 8*3600)
	local := time.Date(2026, 11, 13, 22, 7, 33, 123456789, shanghai) // = 14:07:33.123 UTC
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "t", Content: "x",
		StartsAt: &local,
	}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := created.StartsAt.Location(); got != time.UTC {
		t.Fatalf("时间应归一化为 UTC，实际 location=%v", got)
	}
	if created.StartsAt.Nanosecond() != 0 {
		t.Fatalf("时间应截断到秒，实际纳秒=%d", created.StartsAt.Nanosecond())
	}
	want := time.Date(2026, 11, 13, 14, 7, 33, 0, time.UTC)
	if !created.StartsAt.Equal(want) {
		t.Fatalf("时刻不对：%v != %v", created.StartsAt, want)
	}
	// 归一化后仍在窗口内：22:07:33 CST 就是 14:07:33 UTC，故 14:08 UTC 已生效。
	// 若哪天有人把归一化去掉（写入侧留 +08:00），SQLite 的字符串比较会让
	// 「14:00 <= 22:07:33」判成真 —— 公告提前 7 小时生效，而这条用例变红。
	rows, err := ActiveIncidents(db, announcementAt.Add(8*time.Minute))
	if err != nil {
		t.Fatalf("ActiveIncidents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("归一化后应在 14:08 UTC 生效，实际 %d 条", len(rows))
	}
	// 反向对照：14:07:33 之前不可见。这一条才是时区归一化的真正判据 ——
	// 没有它，「提前生效 7 小时」这种错误在正向断言里看不出来。
	if rows, err := ActiveIncidents(db, announcementAt.Add(7*time.Minute)); err != nil || len(rows) != 0 {
		t.Fatalf("14:07 UTC 时公告尚未开始，不该下发：rows=%d err=%v", len(rows), err)
	}
}

// 启停：只改 enabled，不动其它列。
func TestSetAnnouncementEnabledOnlyTouchesFlag(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := CreateAnnouncement(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "原标题", Content: "原正文",
	}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := SetAnnouncementEnabled(db, created.ID, false, announcementAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if updated.Enabled {
		t.Fatal("启停未生效")
	}
	if updated.Title != "原标题" || updated.Content != "原正文" {
		t.Fatalf("启停不得改动标题/正文：%q / %q", updated.Title, updated.Content)
	}
	// 停用后立刻从下发里消失（无须等缓存，读的是同一张表）。
	if rows, err := ActiveIncidents(db, announcementAt.Add(2*time.Minute)); err != nil || len(rows) != 0 {
		t.Fatalf("停用后不该下发：rows=%d err=%v", len(rows), err)
	}
}

// 删除：真删（列表里查不到），且再删同一条报 NotFound。
func TestDeleteAnnouncement(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := CreateAnnouncement(db, AnnouncementInput{Title: "t", Content: "x"}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := DeleteAnnouncement(db, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := DeleteAnnouncement(db, created.ID); !errors.Is(err, ErrAnnouncementNotFound) {
		t.Fatalf("重复删除应返回 NotFound，实际 %v", err)
	}
	rows, err := ListAllAnnouncements(db, AnnouncementListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("删除后列表应为空，实际 %d 条", len(rows))
	}
}

// 编辑不存在的公告返回 NotFound（而不是静默成功）。
func TestUpdateAnnouncementNotFound(t *testing.T) {
	db := newAnnouncementTestDB(t)
	_, err := UpdateAnnouncement(db, 9999, AnnouncementInput{Title: "t", Content: "x"}, announcementAt)
	if !errors.Is(err, ErrAnnouncementNotFound) {
		t.Fatalf("期望 ErrAnnouncementNotFound，实际 %v", err)
	}
}

// 列表：管理页必须能看到停用与已过期的行（隐藏它们会让人以为公告被删了）。
func TestListAllAnnouncementsIncludesDisabledAndExpired(t *testing.T) {
	db := newAnnouncementTestDB(t)
	seed := []entity.Announcement{
		{Kind: entity.AnnouncementKindNormal, Title: "生效中", Content: "x", Severity: "info",
			Enabled: true, StartsAt: announcementAt.Add(-time.Minute)},
		{Kind: entity.AnnouncementKindIncident, Title: "已停用", Content: "x", Severity: "info",
			Enabled: false, StartsAt: announcementAt.Add(-time.Minute)},
		{Kind: entity.AnnouncementKindIncident, Title: "已过期", Content: "x", Severity: "info",
			Enabled: true, StartsAt: announcementAt.Add(-time.Hour),
			EndsAt: announcementPtr(announcementAt.Add(-time.Minute))},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	rows, err := ListAllAnnouncements(db, AnnouncementListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("管理端列表应包含停用与过期行（共 3 条），实际 %d 条", len(rows))
	}
	// 新的在前
	if rows[0].Title != "已过期" {
		t.Fatalf("应按 id 倒序（新的在前），实际首条 %q", rows[0].Title)
	}
}

// 列表筛选：kind / enabled / keyword / activeAt 各一条。
func TestListAllAnnouncementsFilters(t *testing.T) {
	db := newAnnouncementTestDB(t)
	seed := []entity.Announcement{
		{Kind: entity.AnnouncementKindNormal, Title: "维护通知", Content: "11-20 系统维护",
			Severity: "info", Enabled: true, StartsAt: announcementAt.Add(-time.Minute)},
		{Kind: entity.AnnouncementKindIncident, Title: "题库异常", Content: "题库源超时",
			Severity: "critical", Enabled: true, StartsAt: announcementAt.Add(-time.Hour),
			EndsAt: announcementPtr(announcementAt.Add(time.Hour))},
		{Kind: entity.AnnouncementKindIncident, Title: "旧故障", Content: "已修复",
			Severity: "warning", Enabled: false, StartsAt: announcementAt.Add(-time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	check := func(name string, filter AnnouncementListFilter, wantTitles ...string) {
		t.Helper()
		rows, err := ListAllAnnouncements(db, filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rows) != len(wantTitles) {
			t.Fatalf("%s：期望 %d 条 %v，实际 %d 条", name, len(wantTitles), wantTitles, len(rows))
		}
		byTitle := map[string]bool{}
		for _, row := range rows {
			byTitle[row.Title] = true
		}
		for _, title := range wantTitles {
			if !byTitle[title] {
				t.Fatalf("%s：缺少 %q（实际 %v）", name, title, byTitle)
			}
		}
	}

	check("按 kind", AnnouncementListFilter{Kind: "incident"}, "题库异常", "旧故障")
	check("按 enabled=false", AnnouncementListFilter{Enabled: announcementPtr(false)}, "旧故障")
	check("按 enabled=true", AnnouncementListFilter{Enabled: announcementPtr(true)}, "维护通知", "题库异常")
	// 关键词命中正文（不只是标题）
	check("关键词命中正文", AnnouncementListFilter{Keywords: "题库源"}, "题库异常")
	check("关键词命中标题", AnnouncementListFilter{Keywords: "维护"}, "维护通知")
	// activeAt：只返回那一刻生效的（enabled + 窗口内）
	check("activeAt", AnnouncementListFilter{ActiveAt: &announcementAt}, "维护通知", "题库异常")
	check("activeAt 窗口外", AnnouncementListFilter{ActiveAt: announcementPtr(announcementAt.Add(2 * time.Hour))}, "维护通知")
}

// nil 库统一返回 ErrAnnouncementDatabaseUnavailable，不 panic。
func TestAnnouncementServiceNilDB(t *testing.T) {
	if _, err := ActiveIncidents(nil, announcementAt); !errors.Is(err, ErrAnnouncementDatabaseUnavailable) {
		t.Fatalf("ActiveIncidents(nil)：%v", err)
	}
	if _, err := ListAllAnnouncements(nil, AnnouncementListFilter{}); !errors.Is(err, ErrAnnouncementDatabaseUnavailable) {
		t.Fatalf("ListAllAnnouncements(nil)：%v", err)
	}
	if _, err := CreateAnnouncement(nil, AnnouncementInput{Title: "t", Content: "x"}, 1, announcementAt); !errors.Is(err, ErrAnnouncementDatabaseUnavailable) {
		t.Fatalf("CreateAnnouncement(nil)：%v", err)
	}
	if _, err := UpdateAnnouncement(nil, 1, AnnouncementInput{Title: "t", Content: "x"}, announcementAt); !errors.Is(err, ErrAnnouncementDatabaseUnavailable) {
		t.Fatalf("UpdateAnnouncement(nil)：%v", err)
	}
	if err := DeleteAnnouncement(nil, 1); !errors.Is(err, ErrAnnouncementDatabaseUnavailable) {
		t.Fatalf("DeleteAnnouncement(nil)：%v", err)
	}
}

// 短名别名必须与长名行为一致（任务书的签名清单走的就是短名）。
func TestAnnouncementShortAliases(t *testing.T) {
	db := newAnnouncementTestDB(t)
	created, err := Create(db, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "短名", Content: "x",
	}, 1, announcementAt)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rows, err := ListAll(db, AnnouncementListFilter{Kind: "incident"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAll：rows=%d err=%v", len(rows), err)
	}
	updated, err := Update(db, created.ID, AnnouncementInput{
		Kind: entity.AnnouncementKindIncident, Title: "短名2", Content: "x",
	}, announcementAt)
	if err != nil || updated.Title != "短名2" {
		t.Fatalf("Update：%v %q", err, updated.Title)
	}
	if err := Delete(db, created.ID); err != nil {
		t.Fatalf("Delete：%v", err)
	}
}

// 超长标题按**字符**截断（列宽 varchar(200) 的语义是字符不是字节）。
func TestAnnouncementTitleTruncatedByRunes(t *testing.T) {
	db := newAnnouncementTestDB(t)
	long := strings.Repeat("字", 400)
	created, err := CreateAnnouncement(db, AnnouncementInput{Title: long, Content: "x"}, 1, announcementAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n := len([]rune(created.Title)); n != announcementTitleMaxRunes {
		t.Fatalf("标题应截断到 %d 个字符，实际 %d", announcementTitleMaxRunes, n)
	}
}
