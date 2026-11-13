package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 账号封禁管理页控制器测试（121 §4.8 / 121 决策 6）。
//
// # 与任务书的三处偏差（实测确认，此处是权威口径）
//
//  1. `user_bans` **没有** `revoked_at` / `revoked_by` 列。解封由 `service.Unban`
//     把尚未到期行的 `expires_at` 提前置为当前时刻、并复位 `users.status` 来表达。
//     因此本文件的解封判据断言的是「行仍在 + expires_at 已到期 + status 回到 enabled」，
//     而不是「revoked_at 非空」。
//  2. `service.UserUnbanRequest` 的入参是 `UserID`，所以解封路由的 `:userId` 是用户 id
//     （不是 user_bans 主键 id）。
//  3. `service.UserBanRequest` **没有** `source` 字段，「来源」由 `OperatorUserID`
//     派生（0=系统，非0=管理员）。
//
// # 为什么全部用真 sqlite 内存库、不 mock service
//
// 「封禁后 IsBannedAt 必须 true」只有在真实 GORM 写库 + 真实双读判定链路上才有意义。
// 把 service 换成 mock 就退化成「我们调用了一个函数」—— 而那正是本项要防的
// 「代码都在、只差接线」的反面（接线接了但语义错了，mock 一样测不出来）。

func newUserBanAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.UserBan{}); err != nil {
		t.Fatalf("migrate user_bans: %v", err)
	}
	return db
}

// installUserBanAdminTestDB 把控制器的数据库取用点换成测试库。
func installUserBanAdminTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := userBanAdminDB
	userBanAdminDB = func() *gorm.DB { return db }
	t.Cleanup(func() { userBanAdminDB = previous })
}

// userBanAdminTestRouter 组装与 router.go 完全一致的中间件顺序。
//
// 刻意**不挂 middleware.WhitelistIP()**：三条路由生产上挂在不带 IP 判定的
// 独立 group 上（`blacklistAdminRouter`），理由是「被误封的管理员要能自己解封」。
// 测试路由结构必须与生产一致，否则「解封自己」这条路径永远不会被测到。
func userBanAdminTestRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/adapter-service/admin/user-bans", auth, middleware.RequireAdmin, UserBanAdminList)
	router.POST("/adapter-service/admin/user-bans", auth, middleware.RequireAdmin, UserBanAdminCreate)
	router.DELETE("/adapter-service/admin/user-bans/:userId", auth, middleware.RequireAdmin, UserBanAdminRevoke)
	return router
}

func userBanAdminRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func seedUserBanAdminActor(t *testing.T, db *gorm.DB, id int32, username string) entity.User {
	t.Helper()
	actor := entity.User{
		ID: id, Username: username, Password: "123456",
		Role: "admin", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	return actor
}

func seedUserBanTarget(t *testing.T, db *gorm.DB, username, status string) entity.User {
	t.Helper()
	target := entity.User{Username: username, Status: status, Role: "user"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	return target
}

func decodeUserBanList(t *testing.T, recorder *httptest.ResponseRecorder) UserBanAdminListResult {
	t.Helper()
	var result UserBanAdminListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode list: %v (%s)", err, recorder.Body.String())
	}
	return result
}

// 判据①：三条路由在非管理员下全部 403，且**零副作用**（不能封禁/解封成功）。
//
// 用「真非管理员 actor」而不是 nil actor：nil 走的是「未登录」分支（401/403），
// 那测不到「登录了但权限不够」这条最危险的分支（普通用户拿到管理员页面 URL 的情况）。
func TestUserBanAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	normal := entity.User{
		ID: 9301, Username: "SYNTHETIC_NORMAL", Password: "123456",
		Role: "user", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&normal).Error; err != nil {
		t.Fatalf("create normal user: %v", err)
	}
	target := seedUserBanTarget(t, db, "SYNTHETIC_TARGET", "enabled")

	router := userBanAdminTestRouter(&normal)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/adapter-service/admin/user-bans", ""},
		{http.MethodPost, "/adapter-service/admin/user-bans",
			`{"userId":` + strconv.Itoa(int(target.ID)) + `,"reason":"不应生效"}`},
		{http.MethodDelete, "/adapter-service/admin/user-bans/" + strconv.Itoa(int(target.ID)), ""},
	}
	for _, item := range cases {
		recorder := userBanAdminRequest(router, item.method, item.path, item.body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403 (%s)",
				item.method, item.path, recorder.Code, recorder.Body.String())
		}
	}
	// 零副作用：一条封禁行都不能写出来，target 状态也不能变。
	var rows int64
	if err := db.Model(&entity.UserBan{}).Count(&rows).Error; err != nil {
		t.Fatalf("count bans: %v", err)
	}
	if rows != 0 {
		t.Fatalf("non-admin must not write ban rows, got %d", rows)
	}
	var status string
	if err := db.Model(&entity.User{}).Where("id = ?", target.ID).Pluck("status", &status).Error; err != nil {
		t.Fatalf("reload status: %v", err)
	}
	if status != "enabled" {
		t.Fatalf("non-admin must not change users.status, got %q", status)
	}
}

// 判据②：reason 为空（含纯空白）必须 400，且不得写出任何行。
//
// 断言「400 而不是 500」很重要：500 会让运营以为是服务端故障去重试，
// 而真实原因是自己漏填了原因。
func TestUserBanAdminCreateRejectsBlankReasonWith400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9302, "SYNTHETIC_BAN_ADMIN")
	target := seedUserBanTarget(t, db, "SYNTHETIC_REASON_TARGET", "enabled")
	router := userBanAdminTestRouter(&admin)

	for _, body := range []string{
		`{"userId":` + strconv.Itoa(int(target.ID)) + `,"reason":""}`,
		`{"userId":` + strconv.Itoa(int(target.ID)) + `,"reason":"   "}`,
		`{"userId":` + strconv.Itoa(int(target.ID)) + `}`,
	} {
		recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (%s)", body, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "封禁原因不能为空") {
			t.Fatalf("body %s: message must explain the empty reason, got %s", body, recorder.Body.String())
		}
	}
	var rows int64
	if err := db.Model(&entity.UserBan{}).Count(&rows).Error; err != nil {
		t.Fatalf("count bans: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rejected create must not write ban rows, got %d", rows)
	}
}

// 判据③：expiresAt 空 = 永久；过去时间必须 400。
//
// 两条都要断言「落库的实际值」而不是只看响应码：
// 永久封禁的判据是 `expires_at IS NULL`（不是空串、不是零值时间）。
func TestUserBanAdminCreateExpiresAtSemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9303, "SYNTHETIC_EXPIRES_ADMIN")
	router := userBanAdminTestRouter(&admin)

	// a) 过去时间 → 400，且不落行。
	pastTarget := seedUserBanTarget(t, db, "SYNTHETIC_PAST_TARGET", "enabled")
	pastValue := time.Now().Add(-time.Hour).Format(time.RFC3339)
	recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(pastTarget.ID))+`,"reason":"过去时间","expiresAt":"`+pastValue+`"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("past expiresAt: status = %d, want 400 (%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "到期时间必须晚于当前时间") {
		t.Fatalf("past expiresAt: message must explain the rule, got %s", recorder.Body.String())
	}
	// ⚠️ 上面那条子串断言**不能**区分「controller 拦的」与「service 兜的」：
	// service 层有同一份校验，且 writeUserBanAdminError 会把 ErrUserBanInvalid
	// 映射成 400 —— 于是删掉 controller 那段校验，用例照样绿（实测确认）。
	//
	// 真正能区分的是**文案**：controller 的文案比 service 的具体（多出
	// 「（留空表示永久封禁）」这半句运营提示），而 service 只回哨兵包装的短句。
	// 钉住这半句，删掉 controller 校验后 400 会改由 service 产生 → 文案变短 → 本断言红。
	if !strings.Contains(recorder.Body.String(), "留空表示永久封禁") {
		t.Fatalf("past expiresAt: message must come from the controller-level check "+
			"(expected the operator hint「留空表示永久封禁」)，实际 %s。"+
			"若这里只剩 service 的短文案，说明 controller 的 400 校验被删了，"+
			"而 service 恰好兜住了 —— 那是「两层校验互相掩盖」，不是本用例想测的路径。",
			recorder.Body.String())
	}
	var pastRows int64
	if err := db.Model(&entity.UserBan{}).Where("user_id = ?", pastTarget.ID).Count(&pastRows).Error; err != nil {
		t.Fatalf("count past rows: %v", err)
	}
	if pastRows != 0 {
		t.Fatalf("rejected past expiresAt must not write rows, got %d", pastRows)
	}

	// b) 缺省 / 空串 → 永久封禁（expires_at 落 NULL）。
	for index, body := range []string{
		`{"userId":` + strconv.Itoa(int(seedUserBanTarget(t, db, "SYNTHETIC_FOREVER_A", "enabled").ID)) + `,"reason":"永久A"}`,
		`{"userId":` + strconv.Itoa(int(seedUserBanTarget(t, db, "SYNTHETIC_FOREVER_B", "enabled").ID)) + `,"reason":"永久B","expiresAt":""}`,
	} {
		recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans", body)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("permanent case %d: status = %d, want 201 (%s)", index, recorder.Code, recorder.Body.String())
		}
	}
	var permanentRows []entity.UserBan
	if err := db.Where("reason IN ?", []string{"永久A", "永久B"}).Find(&permanentRows).Error; err != nil {
		t.Fatalf("load permanent rows: %v", err)
	}
	if len(permanentRows) != 2 {
		t.Fatalf("expected 2 permanent ban rows, got %d", len(permanentRows))
	}
	for _, row := range permanentRows {
		if row.ExpiresAt != nil {
			t.Fatalf("permanent ban must have NULL expires_at, got %v (reason=%s)", *row.ExpiresAt, row.Reason)
		}
	}

	// c) 未来时间 → 201，且落库值晚于当前时刻。
	futureTarget := seedUserBanTarget(t, db, "SYNTHETIC_FUTURE_TARGET", "enabled")
	futureValue := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	recorder = userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(futureTarget.ID))+`,"reason":"临时封禁","expiresAt":"`+futureValue+`"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("future expiresAt: status = %d, want 201 (%s)", recorder.Code, recorder.Body.String())
	}
	var futureRow entity.UserBan
	if err := db.Where("user_id = ?", futureTarget.ID).First(&futureRow).Error; err != nil {
		t.Fatalf("load future row: %v", err)
	}
	if futureRow.ExpiresAt == nil || !futureRow.ExpiresAt.After(time.Now()) {
		t.Fatalf("future ban must keep a future expires_at, got %v", futureRow.ExpiresAt)
	}
}

// 判据④：解封是软撤销 —— 行仍在、expires_at 被提前置位、users.status 复位，
// 且解封后 IsBannedAt 返回 false。**绝不物理删行**。
//
// 三条断言缺一不可：
//
//	行仍在       → 抓「改成 Delete」
//	expires_at   → 抓「只复位 status 不动行」
//	users.status → 抓「只动行不复位 status」（第 4 个变异，captain 追加）
func TestUserBanAdminRevokeKeepsRowAndLiftsBan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9304, "SYNTHETIC_REVOKE_ADMIN")
	target := seedUserBanTarget(t, db, "SYNTHETIC_REVOKE_TARGET", "enabled")
	router := userBanAdminTestRouter(&admin)

	future := time.Now().Add(3 * time.Hour).Format(time.RFC3339)
	recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(target.ID))+`,"reason":"待解封","expiresAt":"`+future+`"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create ban: status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	// 前置：封禁确实生效（否则下面的「解封后不封」是空断言）。
	if banned, err := service.IsBanned(db, target.ID); err != nil || !banned {
		t.Fatalf("precondition: user must be banned before revoke (banned=%v err=%v)", banned, err)
	}

	beforeRevoke := time.Now()
	recorder = userBanAdminRequest(router, http.MethodDelete,
		"/adapter-service/admin/user-bans/"+strconv.Itoa(int(target.ID))+"?reason=申诉通过", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoke: status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}

	// ① 行仍在（不得物理删除）。
	var rows []entity.UserBan
	if err := db.Where("user_id = ?", target.ID).Find(&rows).Error; err != nil {
		t.Fatalf("load rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("revoke must keep the audit row, got %d rows", len(rows))
	}
	// ② expires_at 已到期（非空且不晚于解封时刻）。
	if rows[0].ExpiresAt == nil {
		t.Fatal("revoke must set expires_at (NULL means permanent, i.e. still banned)")
	}
	if rows[0].ExpiresAt.After(beforeRevoke) {
		t.Fatalf("expires_at = %v, want <= %v", *rows[0].ExpiresAt, beforeRevoke)
	}
	// ③ users.status 复位为 enabled（字面量断言，不引用常量）。
	var status string
	if err := db.Model(&entity.User{}).Where("id = ?", target.ID).Pluck("status", &status).Error; err != nil {
		t.Fatalf("reload status: %v", err)
	}
	if status != "enabled" {
		t.Fatalf("users.status = %q, want \"enabled\" after revoke (Unban must reset the status path too)", status)
	}
	// ④ 业务判定层：双读都必须不命中。
	if banned, err := service.IsBanned(db, target.ID); err != nil {
		t.Fatalf("IsBanned after revoke: %v", err)
	} else if banned {
		t.Fatal("the user must not be banned after revoke")
	}
}

// 判据⑤-前：封禁后 IsBannedAt 必须 true（永久与临时两种都验）。
func TestUserBanAdminCreateMakesUserBanned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9305, "SYNTHETIC_CREATE_ADMIN")
	router := userBanAdminTestRouter(&admin)

	permanent := seedUserBanTarget(t, db, "SYNTHETIC_PERM_TARGET", "enabled")
	temporary := seedUserBanTarget(t, db, "SYNTHETIC_TEMP_TARGET", "enabled")
	future := time.Now().Add(time.Hour).Format(time.RFC3339)

	if recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(permanent.ID))+`,"reason":"永久"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("permanent ban: status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(temporary.ID))+`,"reason":"临时","expiresAt":"`+future+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("temporary ban: status = %d (%s)", recorder.Code, recorder.Body.String())
	}

	for _, userID := range []int32{permanent.ID, temporary.ID} {
		banned, err := service.IsBanned(db, userID)
		if err != nil {
			t.Fatalf("IsBanned(%d): %v", userID, err)
		}
		if !banned {
			t.Fatalf("user %d must be banned right after a successful ban", userID)
		}
	}
}

// 判据⑤-后：临时封禁**到期后**必须不再判封禁。
//
// ⚠️ 这条用例当前 `t.Skip`，原因是**既有 service 层缺陷**，不是本 controller 的问题。
// 保留它（而不是删掉）是因为它是该缺陷唯一的可执行证据，修好后去掉 Skip 即成为回归钉。
//
// 实测缺陷（2026-11-14，本机真 sqlite 内存库）：
//
//	Ban(expiresAt=now+1h)  → users.status='banned' + user_bans(expires_at=now+1h)
//	IsBannedAt(now+2h)     → **true**（期望 false）
//	                且 users.status 仍是 'banned'
//
// 根因：双读的第一路读 `users.status='banned'`，而**自然到期没有任何复位机制**
// （全仓无到期清理任务，`git grep` 确认 user_bans 只在 Ban/Unban 两处被写）。
// 于是「临时封禁」在到期后仍因 status 命中而永久生效 —— 表现为「设了 1 小时，实际永久」。
//
// 为什么不在本任务修：修复点在 `internal/service/user_ban.go`（不在 T-BAN 的可写范围），
// 且有两种语义需要 captain 决策：
//
//	A) IsBannedAt 增加「status 由封禁写入且无未过期行 → 视为已解除」的判定
//	   （不改表、不加任务，但要在 users 上留「status 是谁写的」痕迹，否则会误判人工封禁）
//	B) 增加到期复位任务（更直观，但需要一个新的后台 goroutine + 幂等保护）
//
// 按 t134 的分界判据：「不改 service 就不可能通过」+ 非配置错 ⇒ 合法 skip。
// 本文件其余 10 条用例全部实际执行，不存在「整组 skip 的假绿」。
func TestUserBanAdminTemporaryBanExpiresAtBoundary(t *testing.T) {
	t.Skip("被既有 service 层缺陷阻塞：临时封禁自然到期后 users.status 无人复位，" +
		"IsBannedAt 仍返回 true（实测 users.status='banned'）。修复点在 " +
		"internal/service/user_ban.go（超出 T-BAN 可写范围），需 captain 决策方案 A/B 后解除本 Skip。")
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9306, "SYNTHETIC_EXPIRY_ADMIN")
	router := userBanAdminTestRouter(&admin)

	target := seedUserBanTarget(t, db, "SYNTHETIC_EXPIRY_TARGET", "enabled")
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	if recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(target.ID))+`,"reason":"临时到期","expiresAt":"`+future+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("ban: status = %d (%s)", recorder.Code, recorder.Body.String())
	}

	// 到期前后各判一次。用 IsBannedAt 显式注入时刻，不依赖真实时间流逝。
	now := time.Now()
	duringBan, err := service.IsBannedAt(db, target.ID, now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("IsBannedAt during ban: %v", err)
	}
	if !duringBan {
		t.Fatal("user must be banned while the temporary ban is still in effect")
	}

	afterExpiry, err := service.IsBannedAt(db, target.ID, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("IsBannedAt after expiry: %v", err)
	}

	// 把 status 一起读出来，让失败信息能直接指出是哪一路没解除。
	var status string
	if err := db.Model(&entity.User{}).Where("id = ?", target.ID).Pluck("status", &status).Error; err != nil {
		t.Fatalf("reload status: %v", err)
	}
	if afterExpiry {
		t.Fatalf("临时封禁到期后必须不再判封禁，实际 IsBannedAt=true、"+
			"users.status=%q。根因：service.Ban 会把 users.status 置为 banned，"+
			"而**自然到期没有任何复位机制**（全仓无到期清理 goroutine），"+
			"所以到期的临时封禁仍会因 status='banned' 命中双读的第一路而永久生效。"+
			"这是 service 层的既有缺口，不是本 controller 的缺陷；"+
			"修复需在 IsBannedAt/hasUnexpiredUserBan 增加「status 由封禁写入且无未过期行时视为已解除」"+
			"或加到期复位任务。", status)
	}
}

// 判据⑥：列表默认只返回生效中；显式 active=false 才返回全部。
//
// 断言「默认与 active=true 结果集完全相同」而不只是「数量变少」：
// 后者在「默认返回全部但恰好只有一条生效中」的数据集上也会通过。
func TestUserBanAdminListDefaultsToActiveOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9307, "SYNTHETIC_LIST_ADMIN")
	router := userBanAdminTestRouter(&admin)

	now := time.Now()
	activeTarget := seedUserBanTarget(t, db, "SYNTHETIC_ACTIVE", "enabled")
	expiredTarget := seedUserBanTarget(t, db, "SYNTHETIC_EXPIRED", "enabled")
	revokedTarget := seedUserBanTarget(t, db, "SYNTHETIC_REVOKED", "enabled")

	// 直接造数据（不走 Ban，避免 Ban 把 status 也置 banned 影响判定──
	// 列表的三态只看 user_bans 行的 expires_at / updated_at）。
	rows := []entity.UserBan{
		// 生效中：未到期。
		{UserID: activeTarget.ID, Reason: "生效中", BannedAt: now.Add(-time.Hour),
			ExpiresAt: timePtr(now.Add(time.Hour)), BannedBy: 1},
		// 已过期：expires_at 早已过去，且 updated_at 与它不等（从未被 Unban）。
		{UserID: expiredTarget.ID, Reason: "已过期", BannedAt: now.Add(-3 * time.Hour),
			ExpiresAt: timePtr(now.Add(-2 * time.Hour)), BannedBy: 1},
		// 已撤销：expires_at == updated_at（Unban 用同一个 now 写两列）。
		{UserID: revokedTarget.ID, Reason: "已撤销", BannedAt: now.Add(-time.Hour),
			ExpiresAt: timePtr(now.Add(-time.Minute)), BannedBy: 1},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	// 把「已撤销」那条的 updated_at 对齐到 expires_at，模拟 Unban 的写入形状。
	if err := db.Model(&entity.UserBan{}).Where("user_id = ?", revokedTarget.ID).
		UpdateColumn("updated_at", *rows[2].ExpiresAt).Error; err != nil {
		t.Fatalf("align revoked updated_at: %v", err)
	}

	// 默认（不传 active）：只有 1 条生效中。
	recorder := userBanAdminRequest(router, http.MethodGet, "/adapter-service/admin/user-bans", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list default: status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	def := decodeUserBanList(t, recorder)
	if len(def.Data) != 1 {
		t.Fatalf("default list must return only active bans, got %d rows: %+v", len(def.Data), def.Data)
	}
	if def.Data[0].UserID != activeTarget.ID {
		t.Fatalf("default list returned user %d, want the active one %d", def.Data[0].UserID, activeTarget.ID)
	}
	if def.Total != 1 {
		t.Fatalf("default total = %d, want 1", def.Total)
	}
	// 生效中的永久封禁也属于 active：ExpiresAt 为 nil 时状态必须是 active。
	if def.Data[0].Status != UserBanRecordStatusActive {
		t.Fatalf("status = %q, want %q", def.Data[0].Status, UserBanRecordStatusActive)
	}

	// 显式 active=true 必须与默认**逐行一致**（不是仅仅「也返回 1 条」）。
	explicit := decodeUserBanList(t, userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?active=true", ""))
	if len(explicit.Data) != len(def.Data) || explicit.Total != def.Total {
		t.Fatalf("active=true must match the default: explicit=%d/%d default=%d/%d",
			len(explicit.Data), explicit.Total, len(def.Data), def.Total)
	}

	// active=false：返回全部 3 条，且三态各一条。
	all := decodeUserBanList(t, userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?active=false", ""))
	if len(all.Data) != 3 {
		t.Fatalf("active=false must return all rows, got %d (%+v)", len(all.Data), all.Data)
	}
	if all.Total != 3 {
		t.Fatalf("active=false total = %d, want 3", all.Total)
	}
	statuses := map[string]int{}
	for _, item := range all.Data {
		statuses[item.Status]++
	}
	for _, want := range []string{UserBanRecordStatusActive, UserBanRecordStatusExpired, UserBanRecordStatusRevoked} {
		if statuses[want] != 1 {
			t.Fatalf("expected exactly one %q row, got statuses=%v", want, statuses)
		}
	}
}

// 永久封禁（expires_at 为 NULL）必须出现在默认 active 列表里，且 ExpiresAt 序列化为 null。
//
// 这是「三类状态里最容易被误读的一个」在后端的对应判据：
// 前端要显示「永久」，依赖的就是 ExpiresAt 为 null 这个信号。
func TestUserBanAdminListKeepsPermanentBanActive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9308, "SYNTHETIC_PERM_ADMIN")
	target := seedUserBanTarget(t, db, "SYNTHETIC_PERM_LIST", "enabled")
	router := userBanAdminTestRouter(&admin)

	if recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":`+strconv.Itoa(int(target.ID))+`,"reason":"永久封禁"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create permanent ban: status = %d (%s)", recorder.Code, recorder.Body.String())
	}

	recorder := userBanAdminRequest(router, http.MethodGet, "/adapter-service/admin/user-bans", "")
	list := decodeUserBanList(t, recorder)
	if len(list.Data) != 1 {
		t.Fatalf("permanent ban must appear in the default (active) list, got %d rows", len(list.Data))
	}
	if list.Data[0].ExpiresAt != nil {
		t.Fatalf("permanent ban must serialize expiresAt as null, got %v", *list.Data[0].ExpiresAt)
	}
	if list.Data[0].Status != UserBanRecordStatusActive {
		t.Fatalf("permanent ban status = %q, want %q", list.Data[0].Status, UserBanRecordStatusActive)
	}
	// 来源派生：管理员封禁 → admin（BannedBy 非 0）。
	if list.Data[0].Source != "admin" {
		t.Fatalf("source = %q, want \"admin\" for an operator-issued ban", list.Data[0].Source)
	}
	if list.Data[0].Username != "SYNTHETIC_PERM_LIST" {
		t.Fatalf("username = %q, want the joined username", list.Data[0].Username)
	}
}

// userId 筛选与 keyword 筛选都要在 **SQL 内**完成（否则 total 会与返回行数不符）。
func TestUserBanAdminListFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9309, "SYNTHETIC_FILTER_ADMIN")
	router := userBanAdminTestRouter(&admin)

	alice := seedUserBanTarget(t, db, "SYNTHETIC_ALICE", "enabled")
	bob := seedUserBanTarget(t, db, "SYNTHETIC_BOB", "enabled")
	now := time.Now()
	rows := []entity.UserBan{
		{UserID: alice.ID, Reason: "刷单行为", BannedAt: now, BannedBy: 1},
		{UserID: bob.ID, Reason: "恶意注册", BannedAt: now.Add(-time.Minute), BannedBy: 0},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// userId 精确筛选。
	byUser := decodeUserBanList(t, userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?userId="+strconv.Itoa(int(alice.ID)), ""))
	if len(byUser.Data) != 1 || byUser.Data[0].UserID != alice.ID {
		t.Fatalf("userId filter failed: %+v", byUser.Data)
	}

	// keyword 命中 reason。
	byReason := decodeUserBanList(t, userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?keyword=%E5%88%B7%E5%8D%95", "")) // 「刷单」
	if len(byReason.Data) != 1 || byReason.Data[0].UserID != alice.ID {
		t.Fatalf("keyword-by-reason failed: %+v", byReason.Data)
	}

	// keyword 命中用户名（SQL 侧 join 出来的 user_id 集合）。
	byName := decodeUserBanList(t, userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?keyword=SYNTHETIC_BOB", ""))
	if len(byName.Data) != 1 || byName.Data[0].UserID != bob.ID {
		t.Fatalf("keyword-by-username failed: %+v", byName.Data)
	}
	if byName.Total != 1 {
		t.Fatalf("keyword-by-username total = %d, want 1 (filtering must happen inside SQL)", byName.Total)
	}

	// 系统自动封禁（BannedBy=0）的 source 派生。
	if byName.Data[0].Source != "system" {
		t.Fatalf("source = %q, want \"system\" when bannedBy=0", byName.Data[0].Source)
	}

	// 非法 userId 明确 400。
	if recorder := userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?userId=abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid userId: status = %d, want 400", recorder.Code)
	}
	// 非法 active 明确 400（不静默当成 true）。
	if recorder := userBanAdminRequest(router, http.MethodGet,
		"/adapter-service/admin/user-bans?active=maybe", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid active: status = %d, want 400", recorder.Code)
	}
}

// 封禁不存在的用户 → 404（不是 500，也不是 201）。
func TestUserBanAdminCreateUnknownUserIs404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9310, "SYNTHETIC_404_ADMIN")
	router := userBanAdminTestRouter(&admin)

	recorder := userBanAdminRequest(router, http.MethodPost, "/adapter-service/admin/user-bans",
		`{"userId":999999,"reason":"不存在的用户"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown user: status = %d, want 404 (%s)", recorder.Code, recorder.Body.String())
	}
}

// 解封不存在的用户 → 404；非法 userId → 400。
func TestUserBanAdminRevokeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserBanAdminTestDB(t)
	installUserBanAdminTestDB(t, db)
	admin := seedUserBanAdminActor(t, db, 9311, "SYNTHETIC_REVOKE_VALIDATION")
	router := userBanAdminTestRouter(&admin)

	if recorder := userBanAdminRequest(router, http.MethodDelete,
		"/adapter-service/admin/user-bans/abc", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid userId: status = %d, want 400", recorder.Code)
	}
	if recorder := userBanAdminRequest(router, http.MethodDelete,
		"/adapter-service/admin/user-bans/999999", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown user: status = %d, want 404 (%s)", recorder.Code, recorder.Body.String())
	}
}

// timePtr 取 *time.Time 的小工具。
func timePtr(value time.Time) *time.Time { return &value }
