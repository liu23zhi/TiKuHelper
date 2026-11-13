package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 本文件钉住设备池的**准入判定**（T-25 判定侧）在 HTTP 层的接线。
//
// 为什么必须单独一个文件：`device_pool_hook_test.go` 的
// TestDevicePoolLimitEnforcedByLevel 直接调 service.CanUseDevice ——
// 它证明「判定逻辑对」，但**完全不能证明有任何 handler 调用它**。
//
// 实测就是这样漏的：service 层 12 条用例全绿、登记侧也接好了，
// 但请求路径上一次判定都没发生 —— 登记照写、计数照准，却永远不拦人。
// 这是本仓反复出现的「实现层对、接线层空」形态（与 router.go 那 33 个
// 未挂 handler 同源）。

// TestDevicePoolLoginBlockedWhenLimitExceeded 是这条接线的行为级主判据。
//
// 场景：等级上限 2 台，已登记 2 台，第 3 台登录必须被 403 挡住。
func TestDevicePoolLoginBlockedWhenLimitExceeded(t *testing.T) {
	db := newDevicePoolHookDB(t)
	stubCtrlCaptcha(t, nil)
	levelID := seedDevicePoolHookLevel(t, db, 1, 2)
	user, password := seedDevicePoolHookUser(t, db, "devpool-enforce", levelID)

	// 打开总开关：默认是关的（device_limit_enabled=false），
	// 不打开的话 CanUseDeviceWithSwitch 会直接放行，这条用例就测不到东西。
	enableDeviceLimit(t, db, true)

	// 先用两台设备登录，把额度用满。
	for _, ua := range []string{"Agent/one", "Agent/two"} {
		rec := loginWithUserAgent(t, user.Username, password, ua)
		if rec.Code != http.StatusOK {
			t.Fatalf("额度内的登录必须成功，UA=%s got %d body=%s", ua, rec.Code, rec.Body.String())
		}
		drainDeviceWrites(t, user.ID, "")
	}
	rows := waitForDeviceRows(t, db, user.ID, 2)
	if len(rows) != 2 {
		t.Fatalf("应先有 2 台已登记设备，实际 %d", len(rows))
	}

	// 第 3 台：必须被拒，且是 403 + 稳定 code。
	third := loginWithUserAgent(t, user.Username, password, "Agent/three")
	if third.Code != http.StatusForbidden {
		t.Fatalf("第 3 台设备必须被 403 拒绝，实际 %d body=%s", third.Code, third.Body.String())
	}
	body := third.Body.String()
	if !strings.Contains(body, "DEVICE_LIMIT_EXCEEDED") {
		t.Fatalf("拒绝响应必须带稳定 code DEVICE_LIMIT_EXCEEDED（前端据此引导清理设备），实际 %s", body)
	}
	// 拒绝时不得签发 JWT —— 否则「拒绝」只是安慰，用户已经拿到访问权。
	if strings.Contains(body, `"jwt"`) {
		t.Fatalf("被拒的登录不得签发 JWT，实际 %s", body)
	}

	// 被拒的那台不该留下设备行（拒绝路径不登记，否则用户试几次就把额度耗光）。
	after := waitForDeviceRows(t, db, user.ID, 2)
	if len(after) != 2 {
		t.Fatalf("被拒设备不得写入设备表，期望仍 2 行，实际 %d", len(after))
	}
}

// TestDevicePoolLoginAllowedWhenSwitchOff 是 fail-open 的同层对照。
//
// 开关关闭时一切照旧 —— 这是本功能的默认状态，必须证明它不会误伤。
func TestDevicePoolLoginAllowedWhenSwitchOff(t *testing.T) {
	db := newDevicePoolHookDB(t)
	stubCtrlCaptcha(t, nil)
	levelID := seedDevicePoolHookLevel(t, db, 1, 1)
	user, password := seedDevicePoolHookUser(t, db, "devpool-switchoff", levelID)

	enableDeviceLimit(t, db, false)

	// 上限 1 台，连登 3 台不同设备，全部必须成功。
	for _, ua := range []string{"Agent/a", "Agent/b", "Agent/c"} {
		rec := loginWithUserAgent(t, user.Username, password, ua)
		if rec.Code != http.StatusOK {
			t.Fatalf("开关关闭时登录不得被设备上限挡住，UA=%s got %d body=%s", ua, rec.Code, rec.Body.String())
		}
		drainDeviceWrites(t, user.ID, "")
	}
}

// TestDevicePoolAlreadyRegisteredDeviceStillAllowedWhenFull 是「第 6 步先于第 7 步」
// 在 HTTP 层的对照：额度用满后，**已登记**的那台设备仍必须能登录。
//
// 这一条防的是「用户被自己的旧设备锁在门外」—— 顺序反了会表现为
// 「换了个网络（UA 不变）就登不进去」，极难排查。
func TestDevicePoolAlreadyRegisteredDeviceStillAllowedWhenFull(t *testing.T) {
	db := newDevicePoolHookDB(t)
	stubCtrlCaptcha(t, nil)
	levelID := seedDevicePoolHookLevel(t, db, 1, 1)
	user, password := seedDevicePoolHookUser(t, db, "devpool-selflock", levelID)

	enableDeviceLimit(t, db, true)

	first := loginWithUserAgent(t, user.Username, password, "Agent/only")
	if first.Code != http.StatusOK {
		t.Fatalf("首台登录必须成功，got %d body=%s", first.Code, first.Body.String())
	}
	drainDeviceWrites(t, user.ID, "")
	waitForDeviceRows(t, db, user.ID, 1)

	// 同一台设备再次登录：额度已满，但它已登记 ⇒ 必须放行。
	again := loginWithUserAgent(t, user.Username, password, "Agent/only")
	if again.Code != http.StatusOK {
		t.Fatalf("已登记设备在额度满时仍须放行（否则用户被自己的设备锁在门外），got %d body=%s", again.Code, again.Body.String())
	}
	if !strings.Contains(again.Body.String(), `"jwt"`) {
		t.Fatalf("放行时必须签发 JWT，实际 %s", again.Body.String())
	}
}

// TestDevicePoolEnforceIsCalledBeforeIssuingToken 是源码级接线断言。
//
// 行为用例只能证明「被拒时没有 jwt」；但如果实现是「先 setAuthCookie 再判定然后
// 清 cookie」，行为上也可能表现正确。这里钉住**顺序**：
// enforceAccountDeviceLimit 的调用必须出现在 setAuthCookie 之前。
func TestDevicePoolEnforceIsCalledBeforeIssuingToken(t *testing.T) {
	source := readControllerSource(t, "user.go")

	// 两处「获得访问权」的入口：密码登录与短信登录。
	for _, marker := range []string{
		"func UserLogin(c *gin.Context) {",
		"func UserSMSLogin(c *gin.Context) {",
	} {
		start := strings.Index(source, marker)
		if start < 0 {
			t.Fatalf("找不到 %s —— 函数被改名了？本用例的路径假设已失效", marker)
		}
		end := len(source)
		if next := strings.Index(source[start+len(marker):], "\nfunc "); next >= 0 {
			end = start + len(marker) + next
		}
		body := source[start:end]
		enforceAt := strings.Index(body, "enforceAccountDeviceLimit(")
		cookieAt := strings.Index(body, "setAuthCookie(")
		if enforceAt < 0 {
			t.Fatalf("%s 没有调用 enforceAccountDeviceLimit —— 准入判定没接上，设备上限永远不拦人", marker)
		}
		if cookieAt < 0 {
			t.Fatalf("%s 找不到 setAuthCookie，本用例的路径假设已失效", marker)
		}
		if enforceAt > cookieAt {
			t.Fatalf("%s 里 enforceAccountDeviceLimit 出现在 setAuthCookie 之后 —— "+
				"后置判定的结论写不进已发出的响应，用户已经拿到 cookie 了", marker)
		}
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
