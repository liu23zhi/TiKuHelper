package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newLoginRateLimitControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:login-rate-ctrl-%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Log{}, &entity.CaptchaToken{},
		&entity.LoginAttempt{}, &entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	dao.SetDefault(db)
	// 注入数据库，避免依赖 dao 全局单例（同包其它用例会改写它）。
	original := adminLoginDBOverride
	adminLoginDBOverride = db
	t.Cleanup(func() { adminLoginDBOverride = original })
	return db
}

func seedLoginUser(t *testing.T, db *gorm.DB, username, role string) string {
	t.Helper()
	const password = "Passw0rd!2026"
	hash, err := util.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := entity.User{
		Username: username, Password: hash, Role: role, Status: "enabled",
		RegisterSource: "system",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return password
}

func setCtrlSetting(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	if err := db.Create(&entity.SystemSetting{Key: key, Value: value, Remark: "test"}).Error; err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

func stubCtrlCaptcha(t *testing.T, err error) {
	t.Helper()
	original := consumeCaptchaTokenWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB = original })
	consumeCaptchaTokenWithDB = func(string, string, string) error { return err }
}

func postAdminLogin(t *testing.T, body map[string]any, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin-login", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = remoteAddr
	AdminLogin(ctx)
	return recorder
}

func postUserLogin(t *testing.T, body map[string]any, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = remoteAddr
	UserLogin(ctx)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, recorder.Body.String())
	}
	return body
}

// ===========================================================================
// /admin-login：IP 维度（不可关闭）
// ===========================================================================

// 管理员正确登录 → 200 + JWT。
func TestAdminLoginSucceedsForAdmin(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	password := seedLoginUser(t, db, "admin-user", "admin")
	stubCtrlCaptcha(t, nil)

	recorder := postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": password, "captchaToken": "ok",
	}, "203.0.113.7:1234")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if token, _ := decodeBody(t, recorder)["jwt"].(string); strings.TrimSpace(token) == "" {
		t.Fatal("response must contain a jwt")
	}
}

// 非管理员必须被拒绝，且**计入失败**。
func TestAdminLoginRejectsNonAdmin(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	password := seedLoginUser(t, db, "normal-user", "user")
	stubCtrlCaptcha(t, nil)

	recorder := postAdminLogin(t, map[string]any{
		"username": "normal-user", "password": password, "captchaToken": "ok",
	}, "203.0.113.7:1234")
	if recorder.Code == http.StatusOK {
		t.Fatalf("non-admin must be rejected, got 200 (body=%s)", recorder.Body.String())
	}
	var row entity.LoginAttempt
	if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeIP, "203.0.113.7").
		First(&row).Error; err != nil {
		t.Fatalf("non-admin attempt must be counted: %v", err)
	}
	if row.FailCount != 1 {
		t.Fatalf("fail count = %d, want 1", row.FailCount)
	}
}

// **5 次连续失败 → 锁 30 分钟**（默认值）。
func TestAdminLoginLocksAfterFiveConsecutiveFailures(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "admin-user", "admin")
	stubCtrlCaptcha(t, nil)

	for i := 1; i <= 4; i++ {
		recorder := postAdminLogin(t, map[string]any{
			"username": "admin-user", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, "203.0.113.7:1234")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("failure #%d status = %d, want 400", i, recorder.Code)
		}
		body := decodeBody(t, recorder)
		if got, _ := body["remainingAttempts"].(float64); got != float64(5-i) {
			t.Fatalf("failure #%d remainingAttempts = %v, want %d", i, got, 5-i)
		}
	}
	// 第 5 次触发锁定。
	recorder := postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "203.0.113.7:1234")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("5th failure status = %d, want 429 (body=%s)", recorder.Code, recorder.Body.String())
	}
	body := decodeBody(t, recorder)
	message, _ := body["message"].(string)
	if !strings.Contains(message, "登录失败次数过多") || !strings.Contains(message, "30 分钟") {
		t.Fatalf("message = %q, want 30-minute lockout", message)
	}
	if minutes, _ := body["remainingMinutes"].(float64); minutes != 30 {
		t.Fatalf("remainingMinutes = %v, want 30", minutes)
	}
	if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "1800" {
		t.Fatalf("Retry-After = %q, want 1800", retryAfter)
	}

	// 第 6 次即使密码正确也必须被拒（锁定期间不验证凭据）。
	recorder = postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": "Passw0rd!2026", "captchaToken": "x",
	}, "203.0.113.7:1234")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password during lockout status = %d, want 429", recorder.Code)
	}

	// 另一个 IP 不受影响（按 IP 锁定）。
	recorder = postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": "Passw0rd!2026", "captchaToken": "x",
	}, "203.0.113.8:1234")
	if recorder.Code != http.StatusOK {
		t.Fatalf("another IP must still log in, status = %d (body=%s)", recorder.Code, recorder.Body.String())
	}
}

// 管理员入口**不可关闭**：即使 login_rate_limit_enabled=false 仍然锁定。
func TestAdminLoginLockoutCannotBeDisabled(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "admin-user", "admin")
	stubCtrlCaptcha(t, nil)
	// 关闭普通模式限流。
	setCtrlSetting(t, db, service.LoginRateLimitEnabledSettingKey, "false")

	for i := 0; i < 5; i++ {
		postAdminLogin(t, map[string]any{
			"username": "admin-user", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, "203.0.113.7:1234")
	}
	recorder := postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": "Passw0rd!2026", "captchaToken": "x",
	}, "203.0.113.7:1234")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("管理员入口锁定不可被 login_rate_limit_enabled 关闭，status = %d (body=%s)",
			recorder.Code, recorder.Body.String())
	}
}

// 锁定检查在验证码之前（锁定期内不消耗验证码）。
func TestAdminLoginLockoutCheckedBeforeCaptcha(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "admin-user", "admin")

	captchaCalls := 0
	original := consumeCaptchaTokenWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB = original })
	consumeCaptchaTokenWithDB = func(string, string, string) error { captchaCalls++; return nil }

	for i := 0; i < 5; i++ {
		postAdminLogin(t, map[string]any{
			"username": "admin-user", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, "203.0.113.7:1234")
	}
	before := captchaCalls
	recorder := postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "203.0.113.7:1234")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if captchaCalls != before {
		t.Fatalf("锁定期间不得消耗验证码：calls %d → %d", before, captchaCalls)
	}
}

// 管理员入口**只受 IP 维度**，不受账号维度约束（45-*.md §0.1.4 五-6）。
//
// 反证：把账号维度阈值设成 1，用错误密码打 /admin-login 一次 —— 若它同时受
// 账号维度约束，第二次就会被账号维度锁；实际应只累积 IP 维度。
func TestAdminLoginNotBoundByAccountScope(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "admin-user", "admin")
	stubCtrlCaptcha(t, nil)
	// 账号维度阈值 1（若被应用，第二次就锁）。
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "1")
	// IP 维度阈值保持默认 5，确保 IP 维度不会先锁。
	postAdminLogin(t, map[string]any{
		"username": "admin-user", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "203.0.113.7:1234")

	// 账号维度**不应**有行（管理员入口不写 account scope）。
	var accountRows int64
	if err := db.Model(&entity.LoginAttempt{}).
		Where("scope = ?", entity.LoginAttemptScopeAccount).Count(&accountRows).Error; err != nil {
		t.Fatalf("count account rows: %v", err)
	}
	if accountRows != 0 {
		t.Fatalf("管理员入口不得写 account 维度计数，got %d 行", accountRows)
	}
	// IP 维度有行。
	var ipRows int64
	if err := db.Model(&entity.LoginAttempt{}).
		Where("scope = ?", entity.LoginAttemptScopeIP).Count(&ipRows).Error; err != nil {
		t.Fatalf("count ip rows: %v", err)
	}
	if ipRows != 1 {
		t.Fatalf("管理员入口必须写 ip 维度计数，got %d 行", ipRows)
	}
}

// ===========================================================================
// /user/login：账号维度（可关闭）
// ===========================================================================

// 普通登录连续失败 10 次 → 锁 15 分钟（默认值）。
func TestUserLoginLocksAfterTenConsecutiveFailures(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "alice", "user")
	stubCtrlCaptcha(t, nil)

	for i := 1; i <= 9; i++ {
		recorder := postUserLogin(t, map[string]any{
			"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, "198.51.100.1:1234")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("failure #%d status = %d, want 400 (body=%s)", i, recorder.Code, recorder.Body.String())
		}
	}
	// 第 10 次触发锁定。
	recorder := postUserLogin(t, map[string]any{
		"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "198.51.100.1:1234")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("10th failure status = %d, want 429 (body=%s)", recorder.Code, recorder.Body.String())
	}
	body := decodeBody(t, recorder)
	message, _ := body["message"].(string)
	if !strings.Contains(message, "15 分钟") {
		t.Fatalf("message = %q, want 15-minute lockout", message)
	}
	// 锁定后即使密码正确也拒。
	recorder = postUserLogin(t, map[string]any{
		"username": "alice", "password": "Passw0rd!2026", "captchaToken": "x",
	}, "198.51.100.1:1234")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password during lockout status = %d, want 429", recorder.Code)
	}
	// 另一个账号不受影响。
	seedLoginUser(t, db, "bob", "user")
	recorder = postUserLogin(t, map[string]any{
		"username": "bob", "password": "Passw0rd!2026", "captchaToken": "x",
	}, "198.51.100.2:1234")
	if recorder.Code != http.StatusOK {
		t.Fatalf("another account must still log in, status = %d (body=%s)", recorder.Code, recorder.Body.String())
	}
}

// 普通模式限流可被管理员**完全关闭**：50 次失败也不锁、不写行。
func TestUserLoginRateLimitCanBeDisabled(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "alice", "user")
	stubCtrlCaptcha(t, nil)
	setCtrlSetting(t, db, service.LoginRateLimitEnabledSettingKey, "false")

	for i := 0; i < 50; i++ {
		recorder := postUserLogin(t, map[string]any{
			"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, "198.51.100.1:1234")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("attempt #%d status = %d, want 400（关闭后不得锁定）", i, recorder.Code)
		}
	}
	var count int64
	if err := db.Model(&entity.LoginAttempt{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("关闭普通模式限流后不得写计数行，got %d", count)
	}
}

// 账号不存在**也计数**（不查库，key 用输入值）—— 且**不暴露账号存在性**。
func TestUserLoginUnknownAccountCountsAndDoesNotRevealExistence(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "alice", "user")
	stubCtrlCaptcha(t, nil)

	// 不存在的账号失败一次。
	unknown := postUserLogin(t, map[string]any{
		"username": "ghost", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "198.51.100.1:1234")
	// 存在的账号失败一次。
	existing := postUserLogin(t, map[string]any{
		"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "198.51.100.2:1234")

	// 两者都必须计数。
	for _, name := range []string{"ghost", "alice"} {
		var row entity.LoginAttempt
		if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeAccount, name).
			First(&row).Error; err != nil {
			t.Fatalf("account %q must be counted: %v", name, err)
		}
		if row.FailCount != 1 {
			t.Fatalf("account %q fail count = %d, want 1", name, row.FailCount)
		}
	}
	// **不暴露账号存在性**：文案与结构完全一致。
	if unknown.Body.String() != existing.Body.String() {
		t.Fatalf("账号存在与不存在的响应必须一致（防枚举）:\n unknown=%s\n existing=%s",
			unknown.Body.String(), existing.Body.String())
	}
}

// 短信登录也计数（45-*.md §0.1.4 五-3）。
func TestUserSMSLoginCountsFailures(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	stubCtrlCaptcha(t, nil)

	payload, _ := json.Marshal(map[string]any{
		"phone": "13800001111", "smsCode": "123456", "captchaToken": "x",
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/login/sms", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = "198.51.100.1:1234"
	UserSMSLogin(ctx)

	if recorder.Code == http.StatusOK {
		t.Fatal("未绑定的手机号不得登录成功")
	}
	// 手机号归一化后作为 account 维度的 key。
	var row entity.LoginAttempt
	if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeAccount, "13800001111").
		First(&row).Error; err != nil {
		t.Fatalf("短信登录失败必须计数: %v", err)
	}
	if row.FailCount != 1 {
		t.Fatalf("fail count = %d, want 1", row.FailCount)
	}
}

// 登录成功清零账号维度（含 window_start 重置）。
func TestUserLoginSuccessClearsAccountCounter(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	password := seedLoginUser(t, db, "alice", "user")
	stubCtrlCaptcha(t, nil)

	// 先失败 3 次。
	for i := 0; i < 3; i++ {
		postUserLogin(t, map[string]any{
			"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, "198.51.100.1:1234")
	}
	// 成功一次。
	recorder := postUserLogin(t, map[string]any{
		"username": "alice", "password": password, "captchaToken": "x",
	}, "198.51.100.1:1234")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var row entity.LoginAttempt
	if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeAccount, "alice").
		First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.FailCount != 0 {
		t.Fatalf("after success failCount = %d, want 0", row.FailCount)
	}
	if row.WindowStart == nil {
		t.Fatal("成功时必须重置 window_start（「连续」语义）")
	}
}

// 被拉黑 IP 从普通入口登录时**同时受 IP 维度约束**（45-*.md §0.1.4 四-1）。
//
// 这条用例需要黑名单缓存处于启用状态，因此直接构造缓存并注入默认单例。
func TestUserLoginOnBlacklistedIPAlsoBoundByIPScope(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	seedLoginUser(t, db, "alice", "user")
	stubCtrlCaptcha(t, nil)

	// 建黑名单规则并让默认缓存加载它（启用开关 + 一条 0.0.0.0/0 规则）。
	if err := db.Create(&entity.BlacklistEntry{
		Kind: service.BlacklistKindIP, Pattern: "0.0.0.0/0", MatchType: service.BlacklistMatchCIDR,
		ValueHash: service.BlacklistPatternHashHex("0.0.0.0/0"), ValueMasked: "0.0.0.0/0",
		Reason: "测试", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create blacklist entry: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{
		Key: service.BlacklistEnabledSettingKey, Value: "true", Remark: "test",
	}).Error; err != nil {
		t.Fatalf("enable blacklist: %v", err)
	}
	originalCache := service.DefaultBlacklistCache()
	t.Cleanup(func() { _ = originalCache.Reload(db) })
	if err := originalCache.Reload(db); err != nil {
		t.Fatalf("reload blacklist cache: %v", err)
	}

	// IP 维度阈值设 2，账号维度设 10（确保先撞 IP 维度）。
	setCtrlSetting(t, db, service.BlacklistAdminLoginMaxAttemptsSettingKey, "2")
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "10")

	const remote = "203.0.113.7:1234"
	// 第 1 次失败：不锁。
	first := postUserLogin(t, map[string]any{
		"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, remote)
	if first.Code != http.StatusBadRequest {
		t.Fatalf("1st failure status = %d, want 400", first.Code)
	}
	// 第 2 次失败：撞 IP 维度阈值 → 锁。
	second := postUserLogin(t, map[string]any{
		"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, remote)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd failure on blacklisted IP status = %d, want 429 (body=%s)",
			second.Code, second.Body.String())
	}
	// IP 维度确实写了行。
	var ipRow entity.LoginAttempt
	if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeIP, "203.0.113.7").
		First(&ipRow).Error; err != nil {
		t.Fatalf("blacklisted IP must be counted in ip scope: %v", err)
	}
	// 关闭黑名单开关后，同一 IP 不再受 IP 维度约束（只有账号维度）。
	if err := db.Model(&entity.SystemSetting{}).
		Where("key = ?", service.BlacklistEnabledSettingKey).Update("value", "false").Error; err != nil {
		t.Fatalf("disable blacklist: %v", err)
	}
	if err := originalCache.Reload(db); err != nil {
		t.Fatalf("reload after disable: %v", err)
	}
}

// 普通模式锁定后，管理端可手动解除（DoS 兜底）。
func TestClearLoginLockoutUnlocksAccount(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	password := seedLoginUser(t, db, "alice", "user")
	stubCtrlCaptcha(t, nil)
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "1")

	// 一次失败就锁。
	postUserLogin(t, map[string]any{
		"username": "alice", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "198.51.100.1:1234")
	if recorder := postUserLogin(t, map[string]any{
		"username": "alice", "password": password, "captchaToken": "x",
	}, "198.51.100.1:1234"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("precondition: status = %d, want 429", recorder.Code)
	}

	// 手动解除（直接调服务层，等价于管理端按钮）。
	if err := service.ClearLoginAttemptsFullyWithDB(db, entity.LoginAttemptScopeAccount, "alice"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if recorder := postUserLogin(t, map[string]any{
		"username": "alice", "password": password, "captchaToken": "x",
	}, "198.51.100.1:1234"); recorder.Code != http.StatusOK {
		t.Fatalf("after manual clear status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
}

// 请求体非法 → 400 且不 panic。
func TestAdminLoginRejectsMalformedBody(t *testing.T) {
	_ = newLoginRateLimitControllerDB(t)
	stubCtrlCaptcha(t, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin-login",
		bytes.NewReader([]byte("{not json")))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = "203.0.113.7:1234"
	AdminLogin(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// 时间辅助：确保 time 包被使用（部分子用例只用到 now 的字面量形式）。
var _ = time.Now

// ===========================================================================
// 普通 IP 横向撞库（password spraying）—— 45-*.md Q67
// ===========================================================================

// **这是本次修复的目的场景**：10 个不同账号各失败 3 次（共 30 次），
// 每个账号都不到账号维度阈值（10），但 IP 维度阈值（默认 20）会被撞破。
//
// 修复前：全部放行（IP 维度只在黑名单 IP 上生效）。
func TestPasswordSprayingAcrossAccountsIsBlocked(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	// 账号维度阈值设高（100），确保本用例只测 IP 维度。
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "100")
	// 10 个账号都真实存在（否则会先撞「账号不存在」但也计数，不影响判定）。
	for i := 0; i < 10; i++ {
		seedLoginUser(t, db, fmt.Sprintf("victim-%02d", i), "user")
	}
	stubCtrlCaptcha(t, nil)

	const remote = "198.51.100.7:1234"
	blocked := false
	attempts := 0
	// 每个账号试 3 次，共 30 次。
	for round := 0; round < 3 && !blocked; round++ {
		for i := 0; i < 10; i++ {
			attempts++
			recorder := postUserLogin(t, map[string]any{
				"username": fmt.Sprintf("victim-%02d", i), "password": "WrongPassw0rd!", "captchaToken": "x",
			}, remote)
			if recorder.Code == http.StatusTooManyRequests {
				blocked = true
				break
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("attempt #%d status = %d, want 400 or 429 (body=%s)",
					attempts, recorder.Code, recorder.Body.String())
			}
		}
	}
	if !blocked {
		t.Fatalf("横向撞库必须被拦住：%d 次失败、10 个账号，IP 维度阈值 %d 未触发",
			attempts, service.DefaultLoginIPMaxAttempts)
	}
	// 断言是 **IP 维度**锁的（而不是账号维度）。
	var ipRow entity.LoginAttempt
	if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeIP, "198.51.100.7").
		First(&ipRow).Error; err != nil {
		t.Fatalf("必须写 ip 维度计数: %v", err)
	}
	// 触发点在 20 次左右（默认阈值），远早于 30 次。
	if attempts > service.DefaultLoginIPMaxAttempts+1 {
		t.Fatalf("应该在 %d 次左右触发，实际第 %d 次才拦", service.DefaultLoginIPMaxAttempts, attempts)
	}
}

// 普通 IP 用**宽松**阈值（默认 20）：19 次不锁、20 次锁。
func TestNormalIPUsesLooseThreshold(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "100")
	seedLoginUser(t, db, "u1", "user")
	stubCtrlCaptcha(t, nil)

	const remote = "198.51.100.9:1234"
	for i := 1; i <= 19; i++ {
		recorder := postUserLogin(t, map[string]any{
			"username": "u1", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, remote)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次（<20）status = %d, want 400", i, recorder.Code)
		}
	}
	// 第 20 次触发。
	recorder := postUserLogin(t, map[string]any{
		"username": "u1", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, remote)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("第 20 次 status = %d, want 429（普通 IP 宽松阈值 20）", recorder.Code)
	}
}

// 被拉黑 IP 用**严格**阈值（默认 5）。
func TestBlacklistedIPUsesStrictThreshold(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "100")
	seedLoginUser(t, db, "u2", "user")
	stubCtrlCaptcha(t, nil)

	// 造黑名单规则并让默认缓存生效。
	if err := db.Create(&entity.BlacklistEntry{
		Kind: service.BlacklistKindIP, Pattern: "0.0.0.0/0", MatchType: service.BlacklistMatchCIDR,
		ValueHash: service.BlacklistPatternHashHex("0.0.0.0/0"), ValueMasked: "0.0.0.0/0",
		Reason: "测试", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create blacklist entry: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{
		Key: service.BlacklistEnabledSettingKey, Value: "true", Remark: "test",
	}).Error; err != nil {
		t.Fatalf("enable blacklist: %v", err)
	}
	cache := service.DefaultBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload blacklist cache: %v", err)
	}

	const remote = "203.0.113.55:1234"
	for i := 1; i <= 4; i++ {
		recorder := postUserLogin(t, map[string]any{
			"username": "u2", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, remote)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次（<5）status = %d, want 400", i, recorder.Code)
		}
	}
	// 第 5 次触发（严格阈值）。
	recorder := postUserLogin(t, map[string]any{
		"username": "u2", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, remote)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("第 5 次 status = %d, want 429（被拉黑 IP 严格阈值 5）", recorder.Code)
	}
}

// IP 白名单完全不参与 IP 维度（失败 100 次也不锁）。
func TestExemptIPSkipsIPScope(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "100000")
	setCtrlSetting(t, db, service.LoginRateLimitExemptIPsSettingKey, "198.51.100.0/24")
	seedLoginUser(t, db, "u3", "user")
	stubCtrlCaptcha(t, nil)

	const remote = "198.51.100.42:1234"
	for i := 0; i < 100; i++ {
		recorder := postUserLogin(t, map[string]any{
			"username": "u3", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, remote)
		if recorder.Code == http.StatusTooManyRequests {
			t.Fatalf("白名单 IP 第 %d 次就被锁了，应完全不参与 IP 维度", i+1)
		}
	}
	// 断言没有写 ip 维度行。
	var ipRows int64
	if err := db.Model(&entity.LoginAttempt{}).
		Where("scope = ?", entity.LoginAttemptScopeIP).Count(&ipRows).Error; err != nil {
		t.Fatalf("count ip rows: %v", err)
	}
	if ipRows != 0 {
		t.Fatalf("白名单 IP 不得写 ip 维度计数，got %d 行", ipRows)
	}
}

// 白名单 IP 仍受**账号维度**约束（白名单只豁免 IP 维度）。
func TestExemptIPStillBoundByAccountScope(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "10")
	setCtrlSetting(t, db, service.LoginRateLimitExemptIPsSettingKey, "198.51.100.0/24")
	seedLoginUser(t, db, "u4", "user")
	stubCtrlCaptcha(t, nil)

	const remote = "198.51.100.43:1234"
	for i := 1; i <= 9; i++ {
		postUserLogin(t, map[string]any{
			"username": "u4", "password": "WrongPassw0rd!", "captchaToken": "x",
		}, remote)
	}
	// 第 10 次撞账号维度阈值。
	recorder := postUserLogin(t, map[string]any{
		"username": "u4", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, remote)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("白名单 IP 仍应受账号维度约束，第 10 次 status = %d, want 429", recorder.Code)
	}
	// 断言是 account 维度锁的。
	var accountRows int64
	if err := db.Model(&entity.LoginAttempt{}).
		Where("scope = ?", entity.LoginAttemptScopeAccount).Count(&accountRows).Error; err != nil {
		t.Fatalf("count account rows: %v", err)
	}
	if accountRows != 1 {
		t.Fatalf("应有且仅有 1 条 account 维度行，got %d", accountRows)
	}
}

// 响应体只能写一次（防「400 与 429 双写」回归）。
//
// 实测缺陷：failIP 在非锁定时也写 400，账号维度随后再写 429，
// 响应体变成 `{"message":...}{"code":429,...}`，前端无法解析。
func TestLoginFailureWritesExactlyOneResponse(t *testing.T) {
	db := newLoginRateLimitControllerDB(t)
	setCtrlSetting(t, db, service.LoginMaxAttemptsSettingKey, "1") // 1 次就锁
	seedLoginUser(t, db, "u5", "user")
	stubCtrlCaptcha(t, nil)

	recorder := postUserLogin(t, map[string]any{
		"username": "u5", "password": "WrongPassw0rd!", "captchaToken": "x",
	}, "198.51.100.50:1234")

	body := recorder.Body.String()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应体必须是单个合法 JSON（双写会让解析失败）：%v\nbody=%q", err, body)
	}
	if strings.Count(body, `"code"`) > 1 || (strings.Contains(body, `"message"`) && payload["code"] == nil && recorder.Code == http.StatusTooManyRequests) {
		t.Fatalf("响应体疑似双写：%q", body)
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
}
