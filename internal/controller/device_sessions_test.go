package controller

// exe 会话数上报端点（POST /api/user/device-sessions）的回归测试（141 §6.2）。
//
// # 每条用例对应哪条红线
//
//	红线 3（鉴权）  TestDeviceSessionsRouterUsesAuthOrAccessToken
//	               —— 读源码断言挂的是 AuthOrAccessToken：exe 只持 adapter key，
//	                  挂 Auth 会让真实客户端恒 401（表现为「从不上报」，无任何报错）。
//	                  ⚠️ 这条必须读**注册语句**而不是跑 handler：handler 内部
//	                  不区分鉴权方式，纯行为断言在把中间件换成 Auth 后仍然绿
//	                  （本仓 t60 踩过：RequireAdmin 的 behavior 断言被 handler 内
//	                  二次校验救活了）。
//
//	C2-3 count 0..8 TestDeviceSessionsRejectsOutOfRangeCount
//	                —— 闭区间 0..8；0 合法（exe 全关）、8 合法（HOME_MAX_SESSIONS）、
//	                   -1/9/100 ⇒ 400。变异「去掉上界校验」→ 9/100 两条红。
//
//	404 不泄露存在性 TestDeviceSessionsReturns404ForForeignAndUnknown
//	                —— 不存在 / 不是你的 / 已撤销 三者**同码同文案**。
//	                  变异「越权返回 403」→ 本条红。
//
//	C2-3 成功 204    TestDeviceSessionsReturns204AndPersistsCount
//	                —— 204 + 库里 session_count 真的变了 + last_seen_at 刷新。
//	                  变异「只返回 204 不落库」→ 本条红。
//
//	C2-4 容忍重复/乱序 TestDeviceSessionsToleratesRepeatedAndOutOfOrderReports
//	                —— 重复上报同一 count 恒 204；后到者覆盖（无客户端时间戳可比）。
//	                  变异「count 只能增不能减」→ 回落那一步红（R7 失效）。
//
// 期望值一律写字面量（不引用 service.DeviceSessionMaxCount 等被测常量）：
// 期望值与被测实现共享常量时，常量漂移会同步改变期望值 → 断言恒真（t150 教训）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var deviceSessionTestDBSeq atomic.Int64

// newDeviceSessionsTestDB 建一个带会话三列（client_instance / session_count /
// session_reported_at）的最小库。
//
// 三列都用原生 ALTER 补，**不加实体字段**：client_instance 与 session_count 属
// C1 的独占范围（entity 文件由其修改），本批次只按列名读写。
func newDeviceSessionsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:device-sessions-%s-%d?mode=memory&cache=shared", name, deviceSessionTestDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.AccountDeviceBinding{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	for _, ddl := range []string{
		"client_instance VARCHAR(64) NOT NULL DEFAULT ''",
		"session_count INTEGER NOT NULL DEFAULT 0",
		"session_reported_at DATETIME NULL",
	} {
		column := strings.Fields(ddl)[0]
		if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			continue
		}
		if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
			" ADD COLUMN " + ddl).Error; err != nil {
			t.Fatalf("add binding column %s: %v", column, err)
		}
	}
	original := deviceSessionDBOverride
	deviceSessionDBOverride = db
	t.Cleanup(func() { deviceSessionDBOverride = original })
	return db
}

// seedDeviceSessionsUser 造一个用户。
func seedDeviceSessionsUser(t *testing.T, db *gorm.DB, username string) *entity.User {
	t.Helper()
	user := entity.User{Username: username, Role: "user", Status: "enabled", RegisterSource: "system"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return &user
}

// seedDeviceSessionsBinding 插一行绑定（revoked=true 时置撤销列）。
func seedDeviceSessionsBinding(t *testing.T, db *gorm.DB, userID int32, deviceID string, revoked bool) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	row := entity.AccountDeviceBinding{
		UserID: userID, DeviceID: deviceID, FirstSeenAt: now, LastSeenAt: now,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding %s: %v", deviceID, err)
	}
	if revoked {
		if err := db.Table(entity.TableNameAccountDeviceBinding).
			Where("id = ?", row.ID).
			UpdateColumns(map[string]any{
				"revoked_at":     now,
				"revoked_reason": "user_removed",
			}).Error; err != nil {
			t.Fatalf("revoke binding %s: %v", deviceID, err)
		}
	}
}

// deviceSessionsRouter 组装本端点（身份由中间件注入，模拟 AuthOrAccessToken 的效果）。
func deviceSessionsRouter(operator *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if operator != nil {
			c.Set("user", operator)
		}
	})
	router.POST("/api/user/device-sessions", ReportDeviceSessions)
	return router
}

// postDeviceSession 发一次上报并返回 recorder（不解 body：204 没有 body）。
func postDeviceSession(t *testing.T, router *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/user/device-sessions", reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// readSessionCountDirect 直接读库里的会话三列（绕过实体字段）。
func readSessionCountDirect(t *testing.T, db *gorm.DB, userID int32, deviceID string) (count int, lastSeen *time.Time) {
	t.Helper()
	var row struct {
		SessionCount int        `gorm:"column:session_count"`
		LastSeenAt   *time.Time `gorm:"column:last_seen_at"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("session_count AS session_count, last_seen_at AS last_seen_at").
		Where("user_id = ? AND device_id = ?", userID, deviceID).Take(&row).Error; err != nil {
		t.Fatalf("read session count(%d,%s): %v", userID, deviceID, err)
	}
	return row.SessionCount, row.LastSeenAt
}

// ───────────────────── C2-3 主路径：204 + 落库 ─────────────────────

// TestDeviceSessionsReturns204AndPersistsCount 是主路径。
//
// 三件事同时断：状态码 204、库里 session_count 变了、last_seen_at 刷新了。
// 只断 204 会让「handler 直接 c.Status(204) 而不调 service」的实现通过 ——
// 那正是 141 §6.2「同时刷新 last_seen_at」漏掉的形态（心跳不刷 → S9 把所有会话判死）。
func TestDeviceSessionsReturns204AndPersistsCount(t *testing.T) {
	db := newDeviceSessionsTestDB(t)
	user := seedDeviceSessionsUser(t, db, "sessions-owner")
	seedDeviceSessionsBinding(t, db, user.ID, "session-device-1", false)

	// 先把 last_seen_at 拨到很久以前，才能证明「本次请求刷新了它」。
	old := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", user.ID, "session-device-1").
		Update("last_seen_at", old).Error; err != nil {
		t.Fatalf("backdate last_seen_at: %v", err)
	}

	recorder := postDeviceSession(t, deviceSessionsRouter(user), map[string]any{
		"deviceId": "session-device-1",
		"count":    3,
	})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("成功上报必须返回 204，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != "" {
		t.Fatalf("204 不得带 body，实际 %q", body)
	}

	count, lastSeen := readSessionCountDirect(t, db, user.ID, "session-device-1")
	if count != 3 {
		t.Fatalf("session_count 必须落库为 3，实际 %d", count)
	}
	if lastSeen == nil || !lastSeen.After(old) {
		t.Fatalf("上报必须刷新 last_seen_at（S9 的心跳前提），原值 %v，现值 %v", old, lastSeen)
	}
}

// ────────────────── C2-3 校验：count 闭区间 0..8 ──────────────────

// TestDeviceSessionsRejectsOutOfRangeCount 覆盖 0..8 的闭区间。
//
// 0 与 8 都必须合法：0 是「exe 全部会话关闭」（R7 的回落），8 是 HOME_MAX_SESSIONS。
// 把 0 当非法会让 exe 永远回落不到 1 个槽位（每个 exe 永久占住上限）。
func TestDeviceSessionsRejectsOutOfRangeCount(t *testing.T) {
	db := newDeviceSessionsTestDB(t)
	user := seedDeviceSessionsUser(t, db, "sessions-range")
	seedDeviceSessionsBinding(t, db, user.ID, "range-device", false)
	router := deviceSessionsRouter(user)

	for _, bad := range []int{-1, 9, 100} {
		recorder := postDeviceSession(t, router, map[string]any{
			"deviceId": "range-device",
			"count":    bad,
		})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("count=%d 必须被拒（400），实际 %d（body=%s）",
				bad, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"max":8`) &&
			!strings.Contains(recorder.Body.String(), `"max": 8`) {
			t.Fatalf("400 响应必须回带上界 8（否则调用方只能猜边界），实际 body=%s",
				recorder.Body.String())
		}
	}

	// 边界两端合法。
	for _, ok := range []int{0, 8} {
		recorder := postDeviceSession(t, router, map[string]any{
			"deviceId": "range-device",
			"count":    ok,
		})
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("count=%d 必须合法（闭区间 0..8），实际 %d（body=%s）",
				ok, recorder.Code, recorder.Body.String())
		}
		count, _ := readSessionCountDirect(t, db, user.ID, "range-device")
		if count != ok {
			t.Fatalf("count=%d 应落库为该值，实际 %d", ok, count)
		}
	}

	// 缺 count 字段也是 400（指针区分钟「没传」与「传了 0」）。
	recorder := postDeviceSession(t, router, map[string]any{"deviceId": "range-device"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 count 必须 400（否则会被当成「上报 0 个会话」静默生效），实际 %d", recorder.Code)
	}
}

// ──────────────── 404 不泄露存在性：三态同一响应 ────────────────

// TestDeviceSessionsReturns404ForForeignAndUnknown 覆盖 404 的三态同一口径。
//
// 「不存在」「不是你的」「已撤销」必须**逐字节相同**的响应。
// 任何区分（403 + 404、不同 message）都等于告诉攻击者 device_id 空间里有什么。
//
// 变异方向：越权改成 403 → 本条红。
func TestDeviceSessionsReturns404ForForeignAndUnknown(t *testing.T) {
	db := newDeviceSessionsTestDB(t)
	owner := seedDeviceSessionsUser(t, db, "sessions-real-owner")
	other := seedDeviceSessionsUser(t, db, "sessions-other")
	seedDeviceSessionsBinding(t, db, owner.ID, "owned-device", false)
	seedDeviceSessionsBinding(t, db, owner.ID, "revoked-device", true)

	otherRouter := deviceSessionsRouter(other)

	// ① 别的账号的设备。
	foreign := postDeviceSession(t, otherRouter, map[string]any{
		"deviceId": "owned-device", "count": 1,
	})
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("越权上报必须 404（不是 403：403 泄露「这条记录存在」），实际 %d", foreign.Code)
	}
	// ② 根本不存在的 device_id。
	unknown := postDeviceSession(t, otherRouter, map[string]any{
		"deviceId": "never-exists", "count": 1,
	})
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("不存在的设备必须 404，实际 %d", unknown.Code)
	}
	// ③ 已撤销的设备（属于本账号）。
	revoked := postDeviceSession(t, deviceSessionsRouter(owner), map[string]any{
		"deviceId": "revoked-device", "count": 1,
	})
	if revoked.Code != http.StatusNotFound {
		t.Fatalf("已撤销的设备必须 404（revoked_at IS NULL 是唯一口径），实际 %d", revoked.Code)
	}

	// 三态响应必须逐字节相同。
	if foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("越权与不存在的响应必须一致（否则可据此枚举 device_id）：\n越权=%s\n不存在=%s",
			foreign.Body.String(), unknown.Body.String())
	}
	if foreign.Body.String() != revoked.Body.String() {
		t.Fatalf("越权与已撤销的响应必须一致：\n越权=%s\n已撤销=%s",
			foreign.Body.String(), revoked.Body.String())
	}
	// 响应里不得回显 deviceId（否则「哪个 id 存在」这件事仍被回显出来）。
	if strings.Contains(foreign.Body.String(), "owned-device") {
		t.Fatalf("404 响应不得回显 deviceId，实际 %s", foreign.Body.String())
	}

	// 关键副作用：被拒的上报**不得**改动任何一行的会话数。
	count, _ := readSessionCountDirect(t, db, owner.ID, "owned-device")
	if count != 0 {
		t.Fatalf("越权上报不得写入 session_count，实际 %d", count)
	}
}

// ──────────── 红线 4：容忍重复上报与乱序上报 ────────────

// TestDeviceSessionsToleratesRepeatedAndOutOfOrderReports 覆盖「重复/乱序都恒 204」。
//
// 上报是后台心跳：报错只会让 exe 记一条永远看不懂的日志，而状态由下一次心跳自然校正。
// 特别地**不能**改成「count 只能增不能减」—— 那样 exe 关闭会话后同步不下来，
// R7 的「全关回落到 1」直接失效（本条最后一步就是钉这个）。
func TestDeviceSessionsToleratesRepeatedAndOutOfOrderReports(t *testing.T) {
	db := newDeviceSessionsTestDB(t)
	user := seedDeviceSessionsUser(t, db, "sessions-idempotent")
	seedDeviceSessionsBinding(t, db, user.ID, "idem-device", false)
	router := deviceSessionsRouter(user)

	// ① 重复上报同一 count：每次都 204（幂等）。
	for i := 0; i < 3; i++ {
		recorder := postDeviceSession(t, router, map[string]any{
			"deviceId": "idem-device", "count": 2,
		})
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("第 %d 次重复上报必须 204，实际 %d", i+1, recorder.Code)
		}
	}
	if count, _ := readSessionCountDirect(t, db, user.ID, "idem-device"); count != 2 {
		t.Fatalf("重复上报后应为 2，实际 %d", count)
	}

	// ② 乱序：先 3 再 1 ⇒ 后到者覆盖（协议无客户端时间戳可比，见 service 层注释）。
	if recorder := postDeviceSession(t, router, map[string]any{
		"deviceId": "idem-device", "count": 3,
	}); recorder.Code != http.StatusNoContent {
		t.Fatalf("上报 3 必须 204，实际 %d", recorder.Code)
	}
	if recorder := postDeviceSession(t, router, map[string]any{
		"deviceId": "idem-device", "count": 1,
	}); recorder.Code != http.StatusNoContent {
		t.Fatalf("上报 1 必须 204，实际 %d", recorder.Code)
	}
	if count, _ := readSessionCountDirect(t, db, user.ID, "idem-device"); count != 1 {
		t.Fatalf("后到者覆盖（1），实际 %d", count)
	}

	// ③ 回落：exe 全关上报 0 ⇒ 必须是 0（R7）。这条钉住「只能增不能减」的变异。
	if recorder := postDeviceSession(t, router, map[string]any{
		"deviceId": "idem-device", "count": 0,
	}); recorder.Code != http.StatusNoContent {
		t.Fatalf("上报 0 必须 204，实际 %d", recorder.Code)
	}
	if count, _ := readSessionCountDirect(t, db, user.ID, "idem-device"); count != 0 {
		t.Fatalf("exe 全关后必须回落到 0（R7），实际 %d", count)
	}
}

// ──────────────── 权限：不能写别的账号的行 ────────────────

// TestDeviceSessionsDoesNotTouchOtherUsersRows 覆盖「归属校验不是靠 deviceId 全局匹配」。
//
// 两个账号各自登记了**同一个** device_id（跨账号镜像，141 §4.3 的情形）时，
// A 的上报只能改 A 的行。若实现按 device_id 全局更新，B 的会话数会被 A 的 exe 覆盖 ——
// 表现为「别人的配额忽大忽小」，且极难归因。
func TestDeviceSessionsDoesNotTouchOtherUsersRows(t *testing.T) {
	db := newDeviceSessionsTestDB(t)
	userA := seedDeviceSessionsUser(t, db, "sessions-a")
	userB := seedDeviceSessionsUser(t, db, "sessions-b")
	seedDeviceSessionsBinding(t, db, userA.ID, "shared-device", false)
	seedDeviceSessionsBinding(t, db, userB.ID, "shared-device", false)

	recorder := postDeviceSession(t, deviceSessionsRouter(userA), map[string]any{
		"deviceId": "shared-device", "count": 5,
	})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("本人上报必须 204，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if countA, _ := readSessionCountDirect(t, db, userA.ID, "shared-device"); countA != 5 {
		t.Fatalf("A 的会话数应为 5，实际 %d", countA)
	}
	if countB, _ := readSessionCountDirect(t, db, userB.ID, "shared-device"); countB != 0 {
		t.Fatalf("B 的行不得被 A 的上报改动（按 (user_id, device_id) 定位），实际 %d", countB)
	}
}

// TestDeviceSessionsRequiresAuthenticatedUser 覆盖「没有身份时不写任何行」。
//
// currentOperator 会自己写 401 —— 本条钉的是「401 之后不再继续执行」：
// 若 handler 忘了 return，会带着 operator.ID=0 往下走，而 user_id=0 的行
// 会变成一张谁都不认领的孤儿记录。
func TestDeviceSessionsRequiresAuthenticatedUser(t *testing.T) {
	db := newDeviceSessionsTestDB(t)
	owner := seedDeviceSessionsUser(t, db, "sessions-anon-victim")
	seedDeviceSessionsBinding(t, db, owner.ID, "anon-device", false)

	recorder := postDeviceSession(t, deviceSessionsRouter(nil), map[string]any{
		"deviceId": "anon-device", "count": 2,
	})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录必须 401，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if count, _ := readSessionCountDirect(t, db, owner.ID, "anon-device"); count != 0 {
		t.Fatalf("未登录的上报不得改动任何行，实际 %d", count)
	}
}

// ─────────────────── 红线 3：路由鉴权必须是 AuthOrAccessToken ───────────────────

// TestDeviceSessionsRouterUsesAuthOrAccessToken 读 router.go 断言注册语句。
//
// ## 为什么必须读源码
//
// exe 手里只有 adapter key（access_tokens 表里的令牌），没有登录 JWT 也没有 cookie。
// 挂 `controller.Auth` 的真实后果是：**真实客户端恒 401**，于是会话数永远不上报，
// R5/R6/R7 的槽位语义全部静默失效。而只跑 handler 的用例对此完全无感 ——
// 身份是测试自己塞进 context 的，中间件根本不在链路里。
//
// 与 device_admin_router_test.go 的 TestDeviceAdminRouterSourceMountsAuthAndRequireAdmin
// 同一手法（本仓 t60 已确认：中间件链不会出现在 engine.Routes() 里，只能读源码）。
func TestDeviceSessionsRouterUsesAuthOrAccessToken(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "router.go"))
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	var registration string
	hits := 0
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if !strings.Contains(trimmed, `"/user/device-sessions"`) ||
			!strings.Contains(trimmed, "controller.ReportDeviceSessions") {
			continue
		}
		registration = trimmed
		hits++
	}
	if hits != 1 {
		t.Fatalf("期望恰好 1 条 /user/device-sessions 注册语句，实际 %d 条", hits)
	}
	if !strings.Contains(registration, "controller.AuthOrAccessToken") {
		t.Fatalf("exe 只持 adapter key，必须挂 controller.AuthOrAccessToken（挂 Auth 会让真实客户端恒 401）：%s",
			registration)
	}
	if strings.Contains(registration, "controller.Auth,") ||
		strings.Contains(registration, "controller.Auth)") {
		t.Fatalf("不得同时挂 controller.Auth（AuthOrAccessToken 内部已回退到 Auth）：%s", registration)
	}
	// 必须是 POST（body 里有 count，GET 无法承载；PUT/DELETE 语义不对 —— 上报不是资源替换）。
	if !strings.HasPrefix(registration, "apiRouter.POST(") {
		t.Fatalf("必须是 POST 注册：%s", registration)
	}
}

// ──────────────── C2-1 的 HTTP 形态：409 + code ────────────────

// TestWriteDeviceConflictEmits409WithStableCode 覆盖 141 §6.3 的 409 响应形态。
//
// ## 为什么这条不能只靠 service 层用例
//
// service 层断的是 `DevicePoolDecision{Allowed:false, Reason:"device-id-conflict"}` ——
// 那是**内部结论**。客户端看到的是 HTTP：状态码 409 + body.code == "DEVICE_ID_CONFLICT"。
// 中间这段转换（decision → 响应）没有任何压缩逻辑，但恰恰是它的缺失最隐蔽：
// 少了它，冲突检测「算对了」但客户端永远收不到，表现与没实现完全一样
// （截获不到 code 就不知道要重新生成 device_id，于是原地重试、无限撞同一个冲突）。
//
// 变异方向：把 409 改成 403 或把 code 去掉 → 本条红。
func TestWriteDeviceConflictEmits409WithStableCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var aborted bool
	var statusAfterAbort int
	router.GET("/conflict", func(c *gin.Context) {
		WriteDeviceConflict(c)
		// Abort 之后同一次请求里若再写第二个响应，gin 会 panic/告警 ——
		// 这里显式记录「有没有被 Abort」，钉住调用方必须 return 的前提。
		aborted = c.IsAborted()
		statusAfterAbort = c.Writer.Status()
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/conflict", nil))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("设备标识冲突必须返回 409（不是 403：403 会被客户端当成额度问题、永远换不出新 ID），实际 %d",
			recorder.Code)
	}
	if !aborted {
		t.Fatal("WriteDeviceConflict 必须 Abort（否则调用方忘了 return 时会写第二个响应）")
	}
	if statusAfterAbort != http.StatusConflict {
		t.Fatalf("Abort 后 writer 状态应为 409，实际 %d", statusAfterAbort)
	}

	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode 409 body: %v (body=%s)", err, recorder.Body.String())
	}
	// code 是协议字段（客户端据它决定是否重新生成 device_id），写字面量断言它没漂移。
	if got, want := envelope["code"], "DEVICE_ID_CONFLICT"; got != want {
		t.Fatalf("409 的 code 必须是 %q（客户端据它重新生成 ID），实际 %v", want, got)
	}
	message, _ := envelope["message"].(string)
	if message == "" {
		t.Fatal("409 必须带可展示的 message（141 §6.3）")
	}
}

// TestWriteDeviceConflictMatchesServiceDecision 覆盖「decision 与 HTTP 的口径一致」。
//
// 防止「service 判冲突的 reason 换了名字但 controller 还认旧名字」这类静默脱钩：
// 直接拿 service 常量拼一个冲突结论，要求 WriteDeviceConflict 与它同源。
func TestWriteDeviceConflictMatchesServiceDecision(t *testing.T) {
	if got, want := service.DevicePoolReasonDeviceIDConflict, "device-id-conflict"; got != want {
		t.Fatalf("service 的冲突 reason 必须是 %q（controller 与测试都认这个值），实际 %q", want, got)
	}
	if got, want := service.DeviceConflictCode, "DEVICE_ID_CONFLICT"; got != want {
		t.Fatalf("service 的冲突 code 必须是 %q，实际 %q", want, got)
	}
}
