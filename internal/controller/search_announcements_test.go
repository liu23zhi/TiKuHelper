package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

// 故障公告下发链路的端到端钉子（N4 / T-08）。
//
// 这组用例全部**走真实 HTTP 搜题路径**（Search → executeSearch → 响应组装），
// 而不是直接调 attachSearchAnnouncements。理由：本项最容易出错的地方是
// **接线**，不是投影函数本身。只测投影函数的话，把 attach 调用删掉、或者
// 换成会返回 error 的 ActiveIncidents，用例照样全绿。
//
// 四条判据分属四个不同的过滤层，互相不冗余：
//
//	① kind=incident  → service.ActiveIncidents 的 WHERE kind
//	② 表缺失 fail-open → 接线点用的是 ActiveIncidentsOrEmpty 而不是 ActiveIncidents
//	③ enabled=true   → WHERE enabled（且实体刻意不写 gorm default:true）
//	④ 时间窗         → WHERE starts_at <= now AND (ends_at IS NULL OR ends_at > now)

// newAnnouncementSearchTestDB 建一个含公告表的控制器测试库。
//
// 与 newSearchControllerTestDB 迁移同一组表（搜题主链路要用的那张列表），
// 另加 Announcement。**刻意不复用后者并往里加表**：那会改变整个
// controller 包里 200+ 个既有用例的库结构，而本任务只需要自己的库里多一张表。
//
// DSN 带 t.Name()：每个用例一个独立内存库，避免 shared cache 互相污染。
func newAnnouncementSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:announcement-search-%s?mode=memory&cache=shared", name)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.SearchAccountConfig{},
		&entity.AISearchConfig{},
		&entity.AIModelPrice{},
		&entity.SystemSetting{},
		&entity.User{},
		&entity.AccessToken{},
		&entity.BillingRecord{},
		&entity.Log{},
		&entity.Tiku{},
		&entity.TikuAnswerEvidence{},
		&entity.ScriptVersion{},
		&entity.UserScript{},
		&entity.ScriptExchangeToken{},
		&entity.ScriptRunSession{},
		&entity.SensitiveWordCache{},
		&entity.Announcement{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	dao.SetDefault(db)
	return db
}

// withAnnouncementSearchStubs 把搜题主链路的上游依赖全部替换成确定性 stub。
//
// 只桩「渠道调用」与「请求上下文解析」两层：公告查询走的是真实数据库，
// 这是本组用例唯一的被测对象，不能被桩掉。
func withAnnouncementSearchStubs(t *testing.T, run func()) {
	t.Helper()

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				RequestID:  "req-announcement",
				Tokens:     map[string]string{"wanneng": "announcement-test-token"},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"A"}}}).SearchAnswer,
			}
		},
		nil,
		run)
}

// runAnnouncementSearch 打一次真实搜题请求，返回原始 recorder。
//
// 返回 recorder（而不是解析后的 SearchResponse）是刻意的：omitempty 的
// 「字段整个不出现」与「字段出现但为 null」在解析后无法区分，而
// 「announcements 为空」的判据必须精确到 JSON 键是否存在 —— 既有客户端
// 拿到的响应形状不能因为这次改动而变化。
//
// noRecord=1 跳过本地题库回填；匿名请求被排除在结果缓存之外，因此每次
// 调用都是真实执行，不会复用上一次的响应。
func runAnnouncementSearch(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost,
		"/api/search?use=wanneng&wannengToken=announcement-test-token&noRecord=1",
		bytes.NewReader([]byte(`{"question":"公告下发测试题","options":["A","B"]}`)))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.77:45678"
	request.Header.Set("X-Forwarded-For", "198.51.100.77")
	ctx.Request = request

	Search(ctx)
	return recorder
}

// decodeAnnouncementSearchBody 把响应体解成通用 map，用于区分「键不存在」与「空数组」。
func decodeAnnouncementSearchBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal search response: %v (body=%s)", err, recorder.Body.String())
	}
	return body
}

// decodeAnnouncementPayloads 取出并解析 announcements 字段。
func decodeAnnouncementPayloads(t *testing.T, body map[string]json.RawMessage) []model.AnnouncementPayload {
	t.Helper()

	raw, exists := body["announcements"]
	if !exists {
		t.Fatalf("expected announcements key to be present, body=%v", body)
	}
	var payloads []model.AnnouncementPayload
	if err := json.Unmarshal(raw, &payloads); err != nil {
		t.Fatalf("unmarshal announcements: %v (raw=%s)", err, raw)
	}
	return payloads
}

// seedAnnouncement 用真实写库落一条公告。
//
// 必须真实写库再读回：本仓在 review_rebate / blacklist 上实测过 gorm `default`
// 的坑 —— 带 `default:true` 时 GORM 会把等于默认值的字段从 INSERT 里省掉，
// 于是「Enabled: false」被静默写成 true。手搓 struct 自比永远绿。
func seedAnnouncement(t *testing.T, db *gorm.DB, row entity.Announcement) entity.Announcement {
	t.Helper()

	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed announcement %q: %v", row.Title, err)
	}
	return row
}

// 判据 ①③④ 合一：一次搜题里，只有「incident + enabled + 时间窗内」的那条出现。
//
// 五条 seed 覆盖四个过滤层。任何一层被去掉，这条用例都会红：
//   - 去掉 kind 过滤 → 「系统维护通知」（normal）出现；
//   - 去掉 enabled 过滤 → 「已关闭的故障」出现；
//   - 去掉时间窗过滤 → 「未来故障」或「过期故障」出现。
//
// 同时断言投影边界：CreatedBy / CreatedAt / UpdatedAt / Enabled 不得下发给客户端。
func TestSearchResponseDeliversOnlyEffectiveIncidentAnnouncements(t *testing.T) {
	db := newAnnouncementSearchTestDB(t)
	now := time.Now().UTC()

	live := seedAnnouncement(t, db, entity.Announcement{
		Kind:      entity.AnnouncementKindIncident,
		Title:     "题库服务异常",
		Content:   "题库源连接超时，正在抢修，预计 30 分钟恢复",
		Severity:  entity.AnnouncementSeverityCritical,
		Enabled:   true,
		StartsAt:  now.Add(-time.Minute),
		EndsAt:    announcementEndsAt(now.Add(30 * time.Minute)),
		CreatedBy: 7,
	})
	// normal 绝不下发（113 §8.1）：它只在 TIKU WebUI 显示。
	seedAnnouncement(t, db, entity.Announcement{
		Kind:     entity.AnnouncementKindNormal,
		Title:    "系统维护通知",
		Content:  "11-20 凌晨维护",
		Severity: entity.AnnouncementSeverityInfo,
		Enabled:  true,
		StartsAt: now.Add(-time.Minute),
	})
	// 停用行不下发（判据 ③）。
	seedAnnouncement(t, db, entity.Announcement{
		Kind:     entity.AnnouncementKindIncident,
		Title:    "已关闭的故障",
		Content:  "x",
		Severity: entity.AnnouncementSeverityWarning,
		Enabled:  false,
		StartsAt: now.Add(-time.Minute),
	})
	// 窗口还没开始（判据 ④）。
	seedAnnouncement(t, db, entity.Announcement{
		Kind:     entity.AnnouncementKindIncident,
		Title:    "未来故障",
		Content:  "x",
		Severity: entity.AnnouncementSeverityInfo,
		Enabled:  true,
		StartsAt: now.Add(time.Hour),
	})
	// 窗口已结束（判据 ④）。
	seedAnnouncement(t, db, entity.Announcement{
		Kind:     entity.AnnouncementKindIncident,
		Title:    "过期故障",
		Content:  "x",
		Severity: entity.AnnouncementSeverityInfo,
		Enabled:  true,
		StartsAt: now.Add(-2 * time.Hour),
		EndsAt:   announcementEndsAt(now.Add(-time.Hour)),
	})

	var recorder *httptest.ResponseRecorder
	withAnnouncementSearchStubs(t, func() {
		recorder = runAnnouncementSearch(t)
	})

	body := decodeAnnouncementSearchBody(t, recorder)
	payloads := decodeAnnouncementPayloads(t, body)

	if len(payloads) != 1 {
		t.Fatalf("只应下发 1 条生效的 incident 公告，实际 %d 条：%+v", len(payloads), payloads)
	}
	got := payloads[0]
	if got.ID != live.ID || got.Title != "题库服务异常" || got.Kind != entity.AnnouncementKindIncident ||
		got.Severity != entity.AnnouncementSeverityCritical {
		t.Fatalf("下发了错误的公告：%+v（期望 id=%d 题库服务异常 incident critical）", got, live.ID)
	}
	if got.Content != "题库源连接超时，正在抢修，预计 30 分钟恢复" {
		t.Fatalf("公告正文被改写：%q", got.Content)
	}
	if got.StartsAt.IsZero() || got.EndsAt == nil {
		t.Fatalf("公告时间窗没有下发：%+v", got)
	}

	// 投影边界：内部审计字段一律不得出现在响应里。
	//
	// 用原始 JSON 断言而不是结构体断言 —— 结构体里根本没有这些字段，
	// 拿结构体断言是恒真的（字段不存在自然「不包含」）。
	rawAnnouncement := string(body["announcements"])
	for _, forbidden := range []string{"createdBy", "createdAt", "updatedAt", "enabled"} {
		if strings.Contains(rawAnnouncement, forbidden) {
			t.Fatalf("公告下发泄漏了内部字段 %q：%s", forbidden, rawAnnouncement)
		}
	}

	// ServiceStatus 本次不填（T-18 才做），不得出现该键。
	if _, exists := body["serviceStatus"]; exists {
		t.Fatalf("T-08 阶段不应下发 serviceStatus，实际：%s", body["serviceStatus"])
	}
}

// 判据 ②（本项最重要的红线）：公告表不存在 → 搜题照常 200，announcements 为空。
//
// 这是 fail-open 的唯一判据。变异方向：把接线点的 ActiveIncidentsOrEmpty
// 换成会返回 error 的 ActiveIncidents、并把错误写进响应 / 改成非 200，
// 这条用例必红。
//
// 用 DropTable 模拟「表还没迁移好」：这是生产上真实会发生的形态
// （公告迁移是九张表里唯一允许降级的那张，见 registry/db.go 的 SysLog 分支）。
func TestSearchResponseStaysOKWhenAnnouncementTableMissing(t *testing.T) {
	db := newAnnouncementSearchTestDB(t)
	if err := db.Migrator().DropTable(&entity.Announcement{}); err != nil {
		t.Fatalf("drop announcements table: %v", err)
	}
	if db.Migrator().HasTable(&entity.Announcement{}) {
		t.Fatal("announcements 表应已被删除（否则本用例测不到 fail-open）")
	}

	var recorder *httptest.ResponseRecorder
	withAnnouncementSearchStubs(t, func() {
		recorder = runAnnouncementSearch(t)
	})

	body := decodeAnnouncementSearchBody(t, recorder) // 内部已断言 200

	// 「announcements 为空」在这里必须是键整个不出现（omitempty），
	// 而不是出现一个 null 或 []。两者对既有客户端的含义不同。
	if _, exists := body["announcements"]; exists {
		t.Fatalf("公告表不可用时应完全不出现 announcements 键，实际：%s", body["announcements"])
	}

	// 答案本身必须照常返回 —— 否则「查不到公告」把「搜题」一起带崩了。
	var parsed model.SearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal search response: %v", err)
	}
	if parsed.Error != nil {
		t.Fatalf("公告查询失败不得污染搜题响应，实际 error=%+v", parsed.Error)
	}
	if parsed.Answer.AnswerText != "A" {
		t.Fatalf("公告查询失败后搜题答案丢失：%+v", parsed.Answer)
	}
}

// 判据 ③ 单独成钉：Enabled=false 的行不下发，且该值经真实写库后确实仍是 false。
//
// 后半句是这条用例的真正价值：gorm `default:true` 的坑会让「停用」被静默
// 写成「启用」。实体刻意没写 default（见 entity/announcement.go 的注释），
// 但这条性质只能靠真实写库+读回来守 —— 一旦有人"顺手"补上 default:true，
// 这里读回的就是 true，用例立刻红，而不是等到生产上「关不掉的故障公告」。
func TestSearchResponseSkipsDisabledIncidentAnnouncement(t *testing.T) {
	db := newAnnouncementSearchTestDB(t)
	now := time.Now().UTC()

	disabled := seedAnnouncement(t, db, entity.Announcement{
		Kind:     entity.AnnouncementKindIncident,
		Title:    "已停用的故障公告",
		Content:  "x",
		Severity: entity.AnnouncementSeverityCritical,
		Enabled:  false,
		StartsAt: now.Add(-time.Minute),
	})

	// 真实读回：确认落库的 enabled 真的是 false（守 gorm default 的坑）。
	var reloaded entity.Announcement
	if err := db.Where("id = ?", disabled.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload announcement: %v", err)
	}
	if reloaded.Enabled {
		t.Fatalf("Enabled=false 被静默写成 true（entity 上是否被加了 gorm default:true？）：%+v", reloaded)
	}

	// 同时有一条启用中的公告，证明「空结果」不是因为整条链路没查。
	seedAnnouncement(t, db, entity.Announcement{
		Kind:     entity.AnnouncementKindIncident,
		Title:    "启用中的故障公告",
		Content:  "y",
		Severity: entity.AnnouncementSeverityWarning,
		Enabled:  true,
		StartsAt: now.Add(-time.Minute),
	})

	var recorder *httptest.ResponseRecorder
	withAnnouncementSearchStubs(t, func() {
		recorder = runAnnouncementSearch(t)
	})

	payloads := decodeAnnouncementPayloads(t, decodeAnnouncementSearchBody(t, recorder))
	if len(payloads) != 1 {
		t.Fatalf("只应下发启用中的那 1 条，实际 %d 条：%+v", len(payloads), payloads)
	}
	if payloads[0].Title != "启用中的故障公告" {
		t.Fatalf("停用行被下发了：%+v", payloads[0])
	}
}

// 判据 ④ 单独成钉：时间窗两端都不下发，窗内下发。
//
// 未来起点 / 已过期终点两条 seed 各管一个比较符：
//   - 只删 starts_at 条件 → 「未来故障」出现；
//   - 只删 ends_at 条件 → 「过期故障」出现。
//
// 单条 seed 无法覆盖两个比较符，所以必须两条都在。
func TestSearchResponseRespectsAnnouncementTimeWindow(t *testing.T) {
	db := newAnnouncementSearchTestDB(t)
	now := time.Now().UTC()

	seedAnnouncement(t, db, entity.Announcement{
		Kind: entity.AnnouncementKindIncident, Title: "未来故障", Content: "x",
		Severity: entity.AnnouncementSeverityInfo, Enabled: true,
		StartsAt: now.Add(time.Hour),
	})
	seedAnnouncement(t, db, entity.Announcement{
		Kind: entity.AnnouncementKindIncident, Title: "过期故障", Content: "x",
		Severity: entity.AnnouncementSeverityInfo, Enabled: true,
		StartsAt: now.Add(-2 * time.Hour),
		EndsAt:   announcementEndsAt(now.Add(-time.Hour)),
	})
	// 永久有效（EndsAt 为空）且已开始 —— 这是最常见的形态，不能被窗口条件误杀。
	seedAnnouncement(t, db, entity.Announcement{
		Kind: entity.AnnouncementKindIncident, Title: "永久故障", Content: "x",
		Severity: entity.AnnouncementSeverityInfo, Enabled: true,
		StartsAt: now.Add(-time.Minute),
	})

	var recorder *httptest.ResponseRecorder
	withAnnouncementSearchStubs(t, func() {
		recorder = runAnnouncementSearch(t)
	})

	payloads := decodeAnnouncementPayloads(t, decodeAnnouncementSearchBody(t, recorder))
	if len(payloads) != 1 {
		t.Fatalf("只应下发窗口内的那 1 条，实际 %d 条：%+v", len(payloads), payloads)
	}
	if payloads[0].Title != "永久故障" {
		t.Fatalf("时间窗过滤失效，下发了：%+v", payloads)
	}
	if payloads[0].EndsAt != nil {
		t.Fatalf("永久有效的公告不应带 endsAt：%+v", payloads[0])
	}
}

// announcementEndsAt 返回时间指针（entity.Announcement.EndsAt 是可空的）。
func announcementEndsAt(value time.Time) *time.Time {
	return &value
}
