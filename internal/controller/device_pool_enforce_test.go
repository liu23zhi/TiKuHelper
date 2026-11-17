package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 本文件钉住「Web UI 登录**不受设备池约束**」这条契约（2026-11-17 用户定案）。
//
// # 背景：这里原来钉的是相反的东西
//
// 旧设计里登录路径调 enforceAccountDeviceLimit，用 UA 哈希当设备标识去判台数。
// 用户明确要求改成：
//
//	「Web UI 登录不需要查看设备上限，确保只能在一个设备登录即可」
//
// 两件事被拆开了：
//
//	设备池（X-Device-Id）  → 只约束 chaoxing 调 API 的台数，由 middleware.DeviceLimit 判定
//	Web UI 单设备登录      → 由 login_session_id（JWT 的 sid claim）保证
//
// # 为什么必须拆开
//
// 登录时拿不到 chaoxing 的 X-Device-Id，只能退化成 UA 哈希。那个哈希与 API
// 路径的 device_id 不是同一个键，于是会出现「登录被设备池拦下、但同一台机器调
// API 完全正常」的自相矛盾状态 —— 而且拦截依据是一台从未登记过的幽灵设备。
//
// # 这些用例为什么不能直接删
//
// 删掉就没人防「哪天有人把 enforceAccountDeviceLimit 加回登录路径」。
// 所以判据反转为「必须没有」，与上面 device_pool_hook_test.go 的接线断言配套。

// TestDevicePoolLoginIgnoresDeviceLimit 是这条契约的行为级主判据。
//
// 场景：等级上限 2 台、总开关打开、且该用户名下已经登记了 2 台设备（额度用满）。
// 此时从第 3 个「UA」（= 旧口径下的第 3 台设备）登录，**必须成功**。
//
// 关键点：旧的 TestDevicePoolLoginBlockedWhenLimitExceeded 在完全相同的前置下
// 要求 403。这条用例的存在本身就证明了行为已经反转，两者不可能同时为真。
func TestDevicePoolLoginIgnoresDeviceLimit(t *testing.T) {
	db := newDevicePoolHookDB(t)
	stubCtrlCaptcha(t, nil)
	levelID := seedDevicePoolHookLevel(t, db, 1, 2)
	user, password := seedDevicePoolHookUser(t, db, "devpool-enforce", levelID)

	// 打开总开关：证明「登录不查上限」不是因为开关关着，而是路径上根本没这段逻辑。
	enableDeviceLimit(t, db, true)

	// 预置 2 台已登记设备，把额度占满（直接写库，不经登录 —— 登录已不登记）。
	for _, deviceID := range []string{"preexisting-a", "preexisting-b"} {
		if _, err := service.RegisterDevice(db, user.ID, deviceID); err != nil {
			t.Fatalf("预置设备 %s 失败: %v", deviceID, err)
		}
	}
	rows := waitForDeviceRows(t, db, user.ID, 2)
	if len(rows) != 2 {
		t.Fatalf("前置不成立：应先有 2 台已登记设备，实际 %d", len(rows))
	}

	// 先确认这 2 台确实把额度占满了 —— 否则「登录放行」可能只是额度没满。
	third := service.CanUseDevice(db, user.ID, "brand-new-device")
	if third.Allowed {
		t.Fatalf("前置不成立：上限 2 台的账号，第 3 台应被判定为超限，实际 %+v", third)
	}
	if third.Reason != "limit-exceeded" {
		t.Fatalf("前置不成立：第 3 台的拒绝原因应是 limit-exceeded，实际 %q", third.Reason)
	}

	// 核心断言：额度已满，登录仍然成功并签发 JWT。
	rec := loginWithUserAgent(t, user.Username, password, "Agent/third")
	if rec.Code != http.StatusOK {
		t.Fatalf("Web UI 登录不受设备池约束：额度满也必须成功，实际 %d body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"jwt"`) {
		t.Fatalf("登录成功必须签发 JWT，实际 %s", rec.Body.String())
	}

	// 登录不得改动设备表：仍是预置的那 2 行，不多不少、ID 也没变。
	//
	// 传 want=2 而不是 0：这 2 行是上面故意预置的「已占满额度」的设备。
	// 断言「行数仍是 2」同时排除了两件事：登录新增行（会变 3）、
	// 以及登录把预置行删掉/重建（行数可能仍是 2，但 ID 会变 —— 下一行兜住）。
	after := assertNoDeviceRows(t, db, user.ID, 2)
	if after[0].ID != rows[0].ID || after[1].ID != rows[1].ID {
		t.Fatalf("登录不得重建已有设备行：IDs %v -> %v",
			[]int64{rows[0].ID, rows[1].ID}, []int64{after[0].ID, after[1].ID})
	}
}

// TestDevicePoolLoginAllowedWhenSwitchOff 是同一契约在「开关关闭」下的对照。
//
// 保留它是为了覆盖两个独立变量：开关开/关 × 登录放行。
// 只有开关关闭时放行，可能是「开关没生效」；只有开关打开时放行，
// 才说明登录路径真的不看设备池。
func TestDevicePoolLoginAllowedWhenSwitchOff(t *testing.T) {
	db := newDevicePoolHookDB(t)
	stubCtrlCaptcha(t, nil)
	levelID := seedDevicePoolHookLevel(t, db, 1, 1)
	user, password := seedDevicePoolHookUser(t, db, "devpool-switchoff", levelID)

	enableDeviceLimit(t, db, false)

	// 上限 1 台，连登 3 个不同 UA，全部必须成功。
	for _, ua := range []string{"Agent/a", "Agent/b", "Agent/c"} {
		rec := loginWithUserAgent(t, user.Username, password, ua)
		if rec.Code != http.StatusOK {
			t.Fatalf("开关关闭时登录不得被设备上限挡住，UA=%s got %d body=%s",
				ua, rec.Code, rec.Body.String())
		}
	}
	assertNoDeviceRows(t, db, user.ID, 0)
}

// TestDevicePoolLoginNeverRegistersDevice 把「登录不写设备表」钉在额度边界上。
//
// 与 TestDevicePoolLoginIgnoresDeviceLimit 的分工：那条在「额度已满」时断登录放行，
// 这条在「额度为空」时断登录不写 —— 两个方向都堵住，避免「额度满时才不写」
// 这类半对半错的实现蒙混过去。
func TestDevicePoolLoginNeverRegistersDevice(t *testing.T) {
	db := newDevicePoolHookDB(t)
	stubCtrlCaptcha(t, nil)
	levelID := seedDevicePoolHookLevel(t, db, 1, 1)
	user, password := seedDevicePoolHookUser(t, db, "devpool-selflock", levelID)

	enableDeviceLimit(t, db, true)

	// 额度 1 台、当前 0 台：这是旧设计下「必然会写一行」的场景。
	for round := 1; round <= 3; round++ {
		rec := loginWithUserAgent(t, user.Username, password, "Agent/only")
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次登录必须成功，got %d body=%s", round, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"jwt"`) {
			t.Fatalf("第 %d 次登录必须签发 JWT，实际 %s", round, rec.Body.String())
		}
	}
	// 连登 3 次后设备表仍然为空：登录路径一次都没碰设备池。
	assertNoDeviceRows(t, db, user.ID, 0)
}

// TestDevicePoolEnforceNotCalledFromSessionHandlers 是源码级接线断言（判据反转版）。
//
// 旧用例 TestDevicePoolEnforceIsCalledBeforeIssuingToken 要求
// enforceAccountDeviceLimit 出现在 setAuthCookie 之前。现在要求它**完全不出现**。
//
// 为什么行为用例不够：行为用例只能观察「这次请求没被拦」。若将来有人把
//   if !enforceAccountDeviceLimit(c, user.ID) { return }
// 加回去，只有在「额度刚好满 + 开关刚好开」的特定数据下才会表现异常，
// 常规回归跑不出来。源码断言是唯一能立刻变红的防线。
func TestDevicePoolEnforceNotCalledFromSessionHandlers(t *testing.T) {
	source := readControllerSource(t, "user.go")

	for _, marker := range []string{
		"func UserLogin(c *gin.Context) {",
		"func UserSMSLogin(c *gin.Context) {",
		"func BindCurrentUserWechat(c *gin.Context) {",
		"func BindCurrentUserWechatByCode(c *gin.Context) {",
	} {
		body := functionBodyOf(t, source, marker)
		if strings.Contains(body, "enforceAccountDeviceLimit(") {
			t.Fatalf("%s 调用了 enforceAccountDeviceLimit —— Web UI 登录/换绑不得查看设备上限"+
				"（设备池只管 chaoxing 调 API 的台数；单设备登录由 login_session_id 保证）", marker)
		}
	}
}

// TestWebUISingleSessionIsGuaranteedByLoginSessionID 钉住「只能一台设备登录」的
// 真实实现位置，防止它被误认为由设备池提供。
//
// 用户要求「确保只能在一个设备登录即可」。这个能力**已经存在**，机制是：
//
//	IssueUserLoginJWTWithDB 每次登录生成新的 login_session_id 写回 users，
//	JWT 里带 sid claim；Auth 中间件比对不一致即失效 ⇒ 旧设备自动踢下线。
//
// 这条用例断言这两个环节都还在。若哪天 login_session_id 被删掉，
// 「单设备登录」会静默失效（多设备都能用），而设备池接不接得住这个需求 —— 接不住。
func TestWebUISingleSessionIsGuaranteedByLoginSessionID(t *testing.T) {
	authSource := readServiceSource(t, "auth_session.go")
	if !strings.Contains(authSource, "login_session_id") {
		t.Fatal("auth_session.go 不再写 login_session_id —— 单设备登录会静默失效")
	}
	if !strings.Contains(authSource, "GenerateJwtForUserSession") {
		t.Fatal("auth_session.go 不再把 session 编进 JWT —— sid claim 缺失会让旧 JWT 无法作废")
	}
}

// enableDeviceLimit 写 device_limit_enabled 系统设置（值格式与 service 层一致："true"/"false"）。
func enableDeviceLimit(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	if err := db.Create(&entity.SystemSetting{
		Key: service.DeviceLimitEnabledSettingKey, Value: value,
		Remark: "测试：账户设备池上限开关",
	}).Error; err != nil {
		t.Fatalf("seed device_limit_enabled=%s: %v", value, err)
	}
}
