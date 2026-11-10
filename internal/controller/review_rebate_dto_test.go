package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"gorm.io/gorm"
)

// 评价返现「我的提交」与「提交管理」的**两套 DTO 契约**（45-*.md §8.4 / Q73-4）。
//
// 这一组钉的不是「字段值为空」，而是**字段键根本不存在**：
//
//	「我的提交」  → 响应里不许出现 agentRationale / screenshotUrl
//	「提交管理」  → 必须有 agentRationale（复核就要看它）
//
// 为什么必须断言 key 不存在而不是「值为空」：若 DTO 改用
// `json:"agentRationale,omitempty"`，未打分的行确实不会出现该键 ——
// 那种测试在**打了分**的行上照样会绿，等于什么都没测。所以这里的 fixture
// 特意让每个 claim 都带非空 rationale，任何「值被清空」的实现都会被抓住。
//
// 为什么这组测试要放在 controller 层而不是 service 层：service 层只负责
// 「填不填 AgentRationaleText」，真正决定**序列化成什么键**的是这里的 DTO。
// 两套 struct 是本轮改动的核心，测试必须落在能看见 JSON 键的那一层。

// seedReviewRebateClaimWithTime 直接落一条 claim，并指定 created_at。
//
// 不走 CreateReviewRebateClaimWithDB：那条路径会做去重/限额/截图校验，
// 时间戳由 autoCreateTime 决定、无法造「上个月的行」。这里要的正是可控时间。
func seedReviewRebateClaimWithTime(t *testing.T, db *gorm.DB, programID, userID int32, canonicalSuffix string, createdAt time.Time, rationale string) entity.ReviewRebateClaim {
	t.Helper()

	claim := entity.ReviewRebateClaim{
		ProgramID:         programID,
		UserID:            userID,
		ScreenshotPath:    "",
		ScreenshotSHA256:  "",
		ClaimedStars:      5,
		ClaimedReviewURL:  "https://example.invalid/review/" + canonicalSuffix,
		CanonicalClaimID:  "canonical-" + canonicalSuffix,
		Status:            "scored",
		AgentStars:        4,
		AgentContentScore: 8,
		AgentConfidence:   0.82,
		AgentRationale:    rationale,
		AgentScorer:       "agent",
		MatchedRuleID:     1,
		AwardAmount:       1,
		CreatedAt:         createdAt,
	}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatalf("create claim %s: %v", canonicalSuffix, err)
	}
	// created_at 在实体上是 autoCreateTime：显式赋值后 GORM 仍可能按当前时间写入，
	// 这里回写一次以确保「上个月的行」真的是上个月。
	if err := db.Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).
		UpdateColumn("created_at", createdAt).Error; err != nil {
		t.Fatalf("pin created_at for %s: %v", canonicalSuffix, err)
	}
	claim.CreatedAt = createdAt
	return claim
}

// decodeClaimList 解出 {data,total,page,pageSize} 信封。
func decodeClaimList(t *testing.T, body []byte) (rows []map[string]any, total int64) {
	t.Helper()

	var envelope struct {
		Data     []map[string]any `json:"data"`
		Total    int64            `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"pageSize"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode claim list %q: %v", string(body), err)
	}
	if envelope.Page < 1 || envelope.PageSize < 1 {
		t.Fatalf("claim list envelope must carry page/pageSize, got page=%d pageSize=%d", envelope.Page, envelope.PageSize)
	}
	if envelope.Data == nil {
		t.Fatalf("claim list data must be [] not null, got %q", string(body))
	}
	return envelope.Data, envelope.Total
}

// reviewRebateDTORouter 与 router.go 一致（这两条是本文件要测的唯一两条）。
//
// 管理页挂**真实的** middleware.RequireAdmin 而不是本文件里复刻一份：
// 403 这条契约属于权限链，用复刻版测等于「自己实现一遍再测自己对不对」——
// 一旦有人把 router.go 里的 RequireAdmin 摘掉，复刻版会继续绿。
func reviewRebateDTORouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/user/review-rebate/claims", auth, MyReviewRebateClaims)
	router.GET("/admin/review-rebate/claims", auth, middleware.RequireAdmin, AdminReviewRebateClaims)
	return router
}

// requireAdminForDTOFixture 已移除：管理页改挂真实的 middleware.RequireAdmin，
// 复刻版会让「router.go 摘掉 RequireAdmin」这类回归继续绿（测的是自己写的副本）。

// Test 1（核心）：管理员调「我的提交」→ 响应**不含** agentRationale 键。
//
// 这是 Q73-4 最容易做错的一条：直觉是「管理员当然能看理由」，但用户明确要求
// 「我的提交」所有角色都只看自己的那一份、且不看判断理由（避免用管理员账号
// 试出自己的评分口径）。
func TestReviewRebateMyClaimsDTOHidesRationaleForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 701, "rr-dto-admin", "admin")
	installReviewRebateTestDB(t, db)

	// 管理员自己的一条，**故意带非空 rationale**：这样任何「靠 omitempty
	// 把值清掉」的实现都会在这条上暴露。
	const secret = "模型观察到：截图正文被裁掉，星级不清。"
	seedReviewRebateClaimWithTime(t, db, program.ID, admin.ID, "admin-own", time.Now().Add(-time.Hour), secret)

	actor := admin
	router := reviewRebateDTORouter(&actor)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/user/review-rebate/claims", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin own claims: %d %s", recorder.Code, recorder.Body.String())
	}

	rows, _ := decodeClaimList(t, recorder.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("expected admin's own 1 claim, got %d", len(rows))
	}
	row := rows[0]

	// ① 键根本不存在（不是值为空、不是 null）。
	if _, leaked := row["agentRationale"]; leaked {
		t.Fatalf("「我的提交」不得出现 agentRationale 键（管理员身份也不行），实际: %v", row)
	}
	if _, leaked := row["screenshotUrl"]; leaked {
		t.Fatalf("「我的提交」不得出现 screenshotUrl 键，实际: %v", row)
	}
	// ② 原始响应体里连这个字符串都不许有 —— 抓「键被改名成别的什么」的漏。
	if strings.Contains(recorder.Body.String(), "agentRationale") {
		t.Fatalf("响应体不得包含 agentRationale 字样: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "正文被裁掉") {
		t.Fatalf("响应体泄露了判断理由原文: %s", recorder.Body.String())
	}
	// ③ 反向对照：该有的业务字段仍在（否则「什么都不下发」也能让上面全绿）。
	for _, key := range []string{"id", "programId", "userId", "status", "agentStars", "agentContentScore", "agentConfidence", "awardAmount"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("「我的提交」缺少应有字段 %q，实际: %v", key, row)
		}
	}
}

// Test 2：「我的提交」只返回当前登录者自己的 —— 即使库里有别人的行。
func TestReviewRebateMyClaimsDTOOnlyReturnsOwnRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	viewer := seedReviewRebateControllerUser(t, db, 711, "rr-dto-viewer", "user")
	other := seedReviewRebateControllerUser(t, db, 712, "rr-dto-other", "user")
	admin := seedReviewRebateControllerUser(t, db, 713, "rr-dto-admin2", "admin")
	installReviewRebateTestDB(t, db)

	now := time.Now()
	seedReviewRebateClaimWithTime(t, db, program.ID, viewer.ID, "viewer-1", now.Add(-3*time.Hour), "理由 V1")
	seedReviewRebateClaimWithTime(t, db, program.ID, other.ID, "other-1", now.Add(-2*time.Hour), "理由 O1")
	seedReviewRebateClaimWithTime(t, db, program.ID, admin.ID, "admin-2", now.Add(-time.Hour), "理由 A1")

	// 普通用户视角。
	actor := viewer
	router := reviewRebateDTORouter(&actor)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/user/review-rebate/claims", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("viewer claims: %d %s", recorder.Code, recorder.Body.String())
	}
	rows, total := decodeClaimList(t, recorder.Body.Bytes())
	if total != 1 || len(rows) != 1 {
		t.Fatalf("viewer must see exactly 1 own claim, got total=%d rows=%d", total, len(rows))
	}
	if got := int32(rows[0]["userId"].(float64)); got != viewer.ID {
		t.Fatalf("viewer claim userId=%d want %d", got, viewer.ID)
	}

	// 管理员视角：同样只看自己的 1 条，**不是全部 3 条**。
	// 并且传来的 userId 参数必须被忽略（否则就是一个越权面）。
	actor = admin
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/user/review-rebate/claims?userId=%d", viewer.ID), nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin own claims: %d %s", recorder.Code, recorder.Body.String())
	}
	adminRows, adminTotal := decodeClaimList(t, recorder.Body.Bytes())
	if adminTotal != 1 || len(adminRows) != 1 {
		t.Fatalf("admin's「我的提交」must only show own 1 claim (not all 3), got total=%d rows=%d", adminTotal, len(adminRows))
	}
	if got := int32(adminRows[0]["userId"].(float64)); got != admin.ID {
		t.Fatalf("admin claim userId=%d want %d (userId 参数必须被忽略)", got, admin.ID)
	}
}

// Test 3：管理员调「提交管理」→ 响应**含** agentRationale 与 screenshotUrl。
func TestReviewRebateAdminClaimsDTOExposesRationale(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 721, "rr-dto-member3", "user")
	admin := seedReviewRebateControllerUser(t, db, 722, "rr-dto-admin3", "admin")
	installReviewRebateTestDB(t, db)

	const rationale = "截图清晰，正文具体，星级一致。"
	claim := seedReviewRebateClaimWithTime(t, db, program.ID, member.ID, "member3", time.Now().Add(-time.Hour), rationale)

	actor := admin
	router := reviewRebateDTORouter(&actor)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/review-rebate/claims", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin claims: %d %s", recorder.Code, recorder.Body.String())
	}
	rows, _ := decodeClaimList(t, recorder.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("expected 1 claim for admin list, got %d", len(rows))
	}
	row := rows[0]

	// 键必须存在且值就是那条理由（不是空串、不是占位）。
	value, ok := row["agentRationale"]
	if !ok {
		t.Fatalf("「提交管理」必须下发 agentRationale 键，实际: %v", row)
	}
	if value != rationale {
		t.Fatalf("agentRationale = %v, want %q", value, rationale)
	}
	// screenshotUrl 是本轮 admin DTO 的第二个专属字段；无截图时是空串，
	// 但键必须在（前端按 `record.screenshotUrl` 判断显示「查看截图」还是「无图」）。
	if _, ok := row["screenshotUrl"]; !ok {
		t.Fatalf("「提交管理」必须下发 screenshotUrl 键（可为空串），实际: %v", row)
	}
	// 服务端私有路径永远不许出现在响应里。
	for _, forbidden := range []string{"screenshotPath", "screenshotSha256", "canonicalClaimId"} {
		if _, leaked := row[forbidden]; leaked {
			t.Fatalf("响应不得包含 %q: %v", forbidden, row)
		}
	}
	if claim.ID == 0 {
		t.Fatal("fixture claim must have an id")
	}
}

// Test 4：时间范围筛选真的生效，且**边界语义正确**（下界闭、上界开）。
//
// 造三条跨月的行，分别验证：
//   - 只给 createdFrom → 返回当月的两条；
//   - 只给 createdTo   → 返回更早的那条；
//   - 日期形态的 createdTo（YYYY-MM-DD）→ 覆盖**当天整天**，不漏 23:59 那条。
//
// 第三条是本轮最容易写错的地方：若服务层用 `<= 当天 00:00:00` 而不是
// `< 次日 00:00:00`，「查 11-10」会把当天所有数据漏掉。
func TestReviewRebateAdminClaimsDTOFiltersByCreatedRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 731, "rr-dto-range-member", "user")
	admin := seedReviewRebateControllerUser(t, db, 732, "rr-dto-range-admin", "admin")
	installReviewRebateTestDB(t, db)

	// 用固定日期，避免「跑测试跨月」时区漂移。
	location := time.Local
	oldRow := time.Date(2026, 9, 15, 10, 0, 0, 0, location)
	midRow := time.Date(2026, 10, 10, 23, 59, 30, 0, location) // 当天最后一分钟
	newRow := time.Date(2026, 10, 20, 8, 0, 0, 0, location)

	seedReviewRebateClaimWithTime(t, db, program.ID, member.ID, "range-old", oldRow, "九月理由")
	seedReviewRebateClaimWithTime(t, db, program.ID, member.ID, "range-mid", midRow, "十月早理由")
	seedReviewRebateClaimWithTime(t, db, program.ID, member.ID, "range-new", newRow, "十月晚理由")

	actor := admin
	router := reviewRebateDTORouter(&actor)

	fetch := func(query string) (int64, []map[string]any) {
		t.Helper()
		recorder := httptest.NewRecorder()
		target := "/admin/review-rebate/claims"
		if query != "" {
			target += "?" + query
		}
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("range query %q: %d %s", query, recorder.Code, recorder.Body.String())
		}
		rows, total := decodeClaimList(t, recorder.Body.Bytes())
		return total, rows
	}

	// ① 无筛选 → 3 条（基线，必须先证明 fixture 里的三条都真的在库里）。
	if total, rows := fetch(""); total != 3 || len(rows) != 3 {
		t.Fatalf("baseline must see all 3 seeded rows, got total=%d rows=%d", total, len(rows))
	}

	// ② createdFrom=2026-10-01 → 十月的两条。
	if total, _ := fetch("createdFrom=2026-10-01"); total != 2 {
		t.Fatalf("createdFrom=2026-10-01 must return 2 rows, got %d", total)
	}

	// ③ createdTo=2026-10-01（日期形态，上界开到次日零点）→ 只有九月那条。
	if total, _ := fetch("createdTo=2026-10-01"); total != 1 {
		t.Fatalf("createdTo=2026-10-01 must return only the September row, got %d", total)
	}

	// ④ **边界关键**：createdFrom=10-01 + createdTo=10-10 必须恰好命中
	//    10-10 23:59:30 那一条。若实现把上界写成 `<= 10-10 00:00:00`，
	//    这条会变成 0 条 —— 「查某天的数据」会整天漏掉。
	//    （只给 createdTo 不带下界时，九月那条也满足 < 10-11 00:00，属正确行为，
	//     所以这里必须配下界才能把边界判死。）
	if total, rows := fetch("createdFrom=2026-10-01&createdTo=2026-10-10"); total != 1 {
		t.Fatalf("createdFrom=2026-10-01&createdTo=2026-10-10 must hit the 23:59:30 row (date upper bound is exclusive at next midnight), got %d rows=%v", total, rows)
	}

	// ⑤ 区间组合：10-01 ~ 10-31 → 两条十月行。
	if total, _ := fetch("createdFrom=2026-10-01&createdTo=2026-10-31"); total != 2 {
		t.Fatalf("October range must return 2 rows, got %d", total)
	}

	// ⑥ 起止倒置 → 400（不是静默返回全部）。
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/admin/review-rebate/claims?createdFrom=2026-10-20&createdTo=2026-10-01", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("inverted range must be 400, got %d %s", recorder.Code, recorder.Body.String())
	}

	// ⑦ 时间筛选与其它筛选叠加：范围 + 不存在的 programId → 0 条。
	if total, _ := fetch("createdFrom=2026-10-01&programId=99999"); total != 0 {
		t.Fatalf("range + non-existent programId must intersect to 0, got %d", total)
	}
}

// Test 5：非管理员调「提交管理」→ 403。
//
// ⚠️ 这条**只证明行为对，不证明路由挂了 RequireAdmin**：handler 内部还有一道
// requireAdmin(c) 二次校验（纵深防御），所以即使 router.go 把
// middleware.RequireAdmin 摘掉，非管理员照样拿 403、这条照样绿。
// 「路由真的挂了中间件」由下面的 TestReviewRebateAdminClaimsRouteMountsRequireAdmin
// 用源码断言单独钉住 —— 两条缺一不可，不能互相替代。
func TestReviewRebateAdminClaimsDTORejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 741, "rr-dto-nonadmin", "user")
	installReviewRebateTestDB(t, db)

	actor := member
	router := reviewRebateDTORouter(&actor)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/review-rebate/claims", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin must get 403, got %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "agentRationale") {
		t.Fatalf("403 响应不得带出任何列表内容: %s", recorder.Body.String())
	}
}

// Test 6：router.go 里管理页那条路由**确实**挂了 middleware.RequireAdmin。
//
// 为什么必须是源码断言而不是行为断言：AdminReviewRebateClaims 内部有
// requireAdmin(c) 二次校验，所以「摘掉中间件」在行为层面完全看不出来
// （实测：把测试路由的 RequireAdmin 短路成放行，403 用例仍然 PASS）。
// 这是本轮唯一无法用行为测到的契约，只能读注册语句。
//
// 断言方式：逐行找同时含该路径与该 handler 的注册行，要求同一行里
// 同时出现 `middleware.RequireAdmin` 且它位于 handler 之前（gin 按注册顺序
// 执行，顺序错了等于没挂）。
func TestReviewRebateAdminClaimsRouteMountsRequireAdmin(t *testing.T) {
	routerSource, err := os.ReadFile(filepath.Join("..", "api", "router.go"))
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}

	const routePath = `"/admin/review-rebate/claims"`
	const handler = "controller.AdminReviewRebateClaims"
	var mounted []string
	for _, line := range strings.Split(string(routerSource), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, routePath) || !strings.Contains(trimmed, handler) {
			continue
		}
		mounted = append(mounted, trimmed)
	}
	if len(mounted) != 1 {
		t.Fatalf("expected exactly 1 registration of GET %s, got %d: %v", routePath, len(mounted), mounted)
	}
	registration := mounted[0]

	guardIndex := strings.Index(registration, "middleware.RequireAdmin")
	handlerIndex := strings.Index(registration, handler)
	if guardIndex < 0 {
		t.Fatalf("管理页路由必须挂 middleware.RequireAdmin（handler 内的二次校验不能替代路由级鉴权）: %s", registration)
	}
	if handlerIndex < 0 || guardIndex > handlerIndex {
		t.Fatalf("middleware.RequireAdmin 必须位于 handler 之前（gin 按注册顺序执行，放在后面等于没挂）: %s", registration)
	}
	// 反向钉子：确认这条真的是 GET（用 POST 之类的动词挂上去会让前端 405）。
	if !strings.Contains(registration, "apiRouter.GET(") {
		t.Fatalf("管理页列表必须是 GET 注册: %s", registration)
	}
}
