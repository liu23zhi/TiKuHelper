package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ════════════════════════════════════════════════════════════════════
// R9 写入点与旁路契约（login_gate.go）
//
// 两条最容易做成假钉子的判据：
//  ① 写入点必须在「触发锁定的 return」**之前** —— 否则最密集的撞库阶段没有记录。
//  ② 写失败**绝不影响登录响应** —— 且失败必须**真实注入**（drop 表），
//     不能用「假设会失败」蒙过去。
// ════════════════════════════════════════════════════════════════════

// newLoginGateTestDB 建一个含失败登录流水表 + 限流表的内存库。
func newLoginGateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:login-gate-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.LoginFailureEvent{}, &entity.LoginAttempt{}, &entity.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// newLoginGateContext 造一个可读 ClientIP 与 URL 的 gin 上下文。
func newLoginGateContext(t *testing.T, method, path, clientIP string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = clientIP + ":12345"
	c.Request = req
	return c
}

// newTestLoginGate 造一个只依赖给定 policy 的 gate（不走 beginLoginRateLimit，
// 因为它需要 gin 上下文与全局 DB 注入）。
func newTestLoginGate(t *testing.T, db *gorm.DB, path, clientIP string,
	ipPolicy service.LoginRateLimitPolicy) *loginGate {
	t.Helper()
	c := newLoginGateContext(t, http.MethodPost, path, clientIP)
	return &loginGate{
		c:         c,
		db:        db,
		now:       time.Now(),
		ipKey:     clientIP,
		ipApplies: true,
		ipPolicy:  ipPolicy,
	}
}

func countLoginFailureEvents(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var total int64
	if err := db.Model(&entity.LoginFailureEvent{}).Count(&total).Error; err != nil {
		t.Fatalf("count login_failure_events: %v", err)
	}
	return total
}

// 判据①：**写入点在「触发锁定的 return」之前**。
//
// 构造：IP policy 阈值设为 1 —— 第一次失败就 `recordIPFailure` 返回 locked=true，
// `failAccount` 随即 `return`（写 429）。若写入点写在 return 之后，表里 0 行。
//
// 变异方向：把 `g.recordLoginFailureEvent(...)` 移到 `if lockout, locked := ...{ return }`
// 之后 → 本用例必红。
func TestLoginGateRecordsFailureEvenWhenItTriggersLockout(t *testing.T) {
	db := newLoginGateTestDB(t)
	// 阈值 1、启用 → 第一次失败就锁定。
	ipPolicy := service.LoginRateLimitPolicy{
		Scope:          entity.LoginAttemptScopeIP,
		Enabled:        true,
		MaxAttempts:    1,
		WindowMinutes:  30,
		LockoutMinutes: 30,
	}
	gate := newTestLoginGate(t, db, "/user/login", "203.0.113.77", ipPolicy)

	gate.failAccount("someuser", "用户名或密码错误")

	// 前置自检：确认这次失败**确实触发了锁定**（否则本用例测不到「return 之前」）。
	var attempts []entity.LoginAttempt
	if err := db.Find(&attempts).Error; err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	if len(attempts) == 0 {
		t.Fatalf("前置条件失效：本次失败没有写入 login_attempts，无法证明触发了锁定路径")
	}
	locked := false
	for _, attempt := range attempts {
		if attempt.LockedUntil != nil && attempt.LockedUntil.After(time.Now()) {
			locked = true
		}
	}
	if !locked {
		t.Fatalf("前置条件失效：阈值 1 时本次失败应触发锁定，实际未锁定（attempts=%+v）", attempts)
	}

	if got := countLoginFailureEvents(t, db); got != 1 {
		t.Fatalf("恰好触发锁定的那次失败必须被记录（写入点要在 return 之前），实际 %d 行。"+
			"若为 0，说明写入点写在 recordIPFailure 的 return 之后 —— "+
			"而那正是撞库最密集的阶段", got)
	}
}

// 判据②：**写失败绝不影响登录响应**（旁路契约）。
//
// ⚠️ 必须**注入真实写失败**：`DropTable` 让 INSERT 真实报 "no such table"。
// 用 mock 返回 error 是假钉子 —— 它走的是与真实失败路径无关的代码。
//
// 变异方向：让 RecordLoginFailureEventWithDB 的错误影响响应 → 本用例必红。
func TestLoginGateWriteFailureDoesNotAffectResponse(t *testing.T) {
	db := newLoginGateTestDB(t)
	// **真实注入失败**：删表。
	if err := db.Migrator().DropTable(entity.TableNameLoginFailureEvent); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameLoginFailureEvent) {
		t.Fatalf("前置条件失效：表仍存在，真实失败未注入")
	}
	// 再自证一次「写真的会失败」：直接调写入函数必须返回 error。
	probeErr := service.RecordLoginFailureEventWithDB(db, entity.LoginFailureEvent{
		CreatedAt: time.Now(), ClientIP: "203.0.113.200", Username: "probe",
	})
	if probeErr == nil {
		// 表不存在时 service 层会「跳过而非失败」（设计如此：老库未迁移不该影响登录）。
		// 所以这里不 fail，只记录：真实验证放在下面对「不 panic + 响应已写」的断言。
		t.Logf("注意：表不存在时写入被跳过了（service 层设计如此，返回 nil）")
	}

	ipPolicy := service.LoginRateLimitPolicy{
		Scope: entity.LoginAttemptScopeIP, Enabled: true,
		MaxAttempts: 10, WindowMinutes: 30, LockoutMinutes: 30,
	}
	gate := newTestLoginGate(t, db, "/user/login", "203.0.113.88", ipPolicy)

	var panicked bool
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicked = true
				t.Logf("panic: %v", recovered)
			}
		}()
		gate.failAccount("writefailuser", "用户名或密码错误")
	}()
	if panicked {
		t.Fatalf("写审计失败不得 panic（旁路契约）")
	}
	// 主流程必须照常写出失败响应（审计失败不打断它）。
	if gate.c.Writer.Size() <= 0 {
		t.Fatalf("写审计失败后主流程仍必须写出失败响应，实际响应体为空")
	}
}

// 判据②b：**表存在但写入被数据库拒绝**时的旁路契约（更强的注入）。
//
// 上面那条因为「表不存在 → service 层跳过」而没真正走到 INSERT 失败分支。
// 这条用**唯一索引冲突**让 INSERT 真实报错，覆盖那条分支。
func TestLoginGateInsertFailureDoesNotAffectResponse(t *testing.T) {
	db := newLoginGateTestDB(t)
	// 埋一个会让 INSERT 真实报错的条件：把 id 列改成必须有值但插入 NULL。
	// 更简单可靠的做法：加一个 CHECK 约束让所有插入失败。
	if err := db.Exec(
		"CREATE TRIGGER fail_login_failure_insert BEFORE INSERT ON login_failure_events " +
			"BEGIN SELECT RAISE(ABORT, 'injected write failure'); END").Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	// 自证：直接插入必须失败。
	if err := db.Create(&entity.LoginFailureEvent{
		CreatedAt: time.Now(), ClientIP: "203.0.113.201", Username: "probe",
	}).Error; err == nil {
		t.Fatalf("前置条件失效：触发器没起作用，插入未失败")
	} else {
		t.Logf("真实注入失败已生效：%v", err)
	}

	ipPolicy := service.LoginRateLimitPolicy{
		Scope: entity.LoginAttemptScopeIP, Enabled: true,
		MaxAttempts: 10, WindowMinutes: 30, LockoutMinutes: 30,
	}
	gate := newTestLoginGate(t, db, "/user/login", "203.0.113.89", ipPolicy)

	var panicked bool
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicked = true
				t.Logf("panic: %v", recovered)
			}
		}()
		gate.failAccount("insertfailuser", "用户名或密码错误")
	}()
	if panicked {
		t.Fatalf("INSERT 失败不得 panic（旁路契约）")
	}
	if gate.c.Writer.Size() <= 0 {
		t.Fatalf("INSERT 失败后主流程仍必须写出失败响应，实际响应体为空")
	}
	if got := countLoginFailureEvents(t, db); got != 0 {
		t.Fatalf("前置条件失效：注入失败后不该有行，实际 %d", got)
	}
}

// 判据③：`failIPOnly`（/admin-login）也写流水，username 为空串，scene 正确。
func TestLoginGateRecordsFailureForAdminEntry(t *testing.T) {
	db := newLoginGateTestDB(t)
	ipPolicy := service.LoginRateLimitPolicy{
		Scope: entity.LoginAttemptScopeIP, Enabled: true,
		MaxAttempts: 10, WindowMinutes: 30, LockoutMinutes: 30,
	}
	gate := newTestLoginGate(t, db, "/admin-login", "203.0.113.99", ipPolicy)

	gate.failIPOnly("该入口仅限管理员使用")

	var events []entity.LoginFailureEvent
	if err := db.Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("failIPOnly 必须写 1 行流水，实际 %d 行", len(events))
	}
	if events[0].Username != "" {
		t.Fatalf("failIPOnly 拿不到账号名，username 必须为空串，实际 %q", events[0].Username)
	}
	if events[0].Scene != entity.LoginFailureSceneAdminPassword {
		t.Fatalf("scene 应为 admin_password（按 URL 推断），实际 %q", events[0].Scene)
	}
	if events[0].ClientIP != "203.0.113.99" {
		t.Fatalf("client_ip = %q，want 203.0.113.99", events[0].ClientIP)
	}
}

// 判据④：**user_id 正确区分「账号不存在」与「密码错误」**（D3 的落地前提）。
func TestLoginGateMarksNonexistentAccount(t *testing.T) {
	db := newLoginGateTestDB(t)
	realUser := entity.User{Username: "realuser", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&realUser).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ipPolicy := service.LoginRateLimitPolicy{
		Scope: entity.LoginAttemptScopeIP, Enabled: true,
		MaxAttempts: 10, WindowMinutes: 30, LockoutMinutes: 30,
	}
	newTestLoginGate(t, db, "/user/login", "203.0.113.101", ipPolicy).
		failAccount("ghostuser", "用户名或密码错误")
	newTestLoginGate(t, db, "/user/login", "203.0.113.102", ipPolicy).
		failAccount("realuser", "用户名或密码错误")

	var events []entity.LoginFailureEvent
	if err := db.Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("应有 2 行流水，实际 %d 行", len(events))
	}
	if events[0].UserID != 0 {
		t.Fatalf("不存在的账号 user_id 必须为 0，实际 %d", events[0].UserID)
	}
	if events[0].Reason != entity.LoginFailureReasonUserNotFound {
		t.Fatalf("不存在的账号 reason 应为 user_not_found，实际 %q", events[0].Reason)
	}
	if events[1].UserID != realUser.ID {
		t.Fatalf("存在的账号 user_id 应为 %d，实际 %d", realUser.ID, events[1].UserID)
	}
	if events[1].Reason != entity.LoginFailureReasonBadPassword {
		t.Fatalf("存在的账号 reason 应为 bad_password，实际 %q", events[1].Reason)
	}
}
