package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 手机号 / 微信黑名单的**端到端**接线测试（46-*.md §7 验收判据，BL-3）。
//
// 与 service 层用例的分工：那边证明判定本体正确（开关/快照/命中/留痕四态），
// 这边证明**四个入口真的调了它**，并且
//
//   - 命中回 403（不是 400/200）；
//   - 403 的文案只含 reason，**不含 Pattern**（46-*.md §3.2）；
//   - 判定发生在消费验证码与写库之前（被拉黑者不产生任何副作用）。
//
// 全部用真实 sqlite 内存库 + 真实 ReloadDefaultBlacklistCache，
// 与 internal/controller/admin_login_test.go 的
// TestUserLoginOnBlacklistedIPAlsoBoundByIPScope 同一套路。

// newBlacklistGateControllerDB 建一个带黑名单三张表的控制器测试库。
//
// 额外迁移 User / SMSVerificationCode / CaptchaToken / IdempotencyRecord：
// 注册与换绑的链路会碰到这些表，缺表时错误会先炸在「表不存在」上，
// 掩盖真正要测的黑名单分支。
func newBlacklistGateControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:blacklist-gate-ctrl-%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.User{}, &entity.SystemSetting{}, &entity.Log{},
		&entity.CaptchaToken{}, &entity.SMSVerificationCode{},
		&entity.IdempotencyRecord{}, &entity.LoginAttempt{},
		&entity.BlacklistEntry{}, &entity.BlacklistHit{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	dao.SetDefault(db)
	// adminLoginDB() 优先用覆盖值，短信登录的 loginGate 走这条路径。
	originalOverride := adminLoginDBOverride
	adminLoginDBOverride = db
	t.Cleanup(func() { adminLoginDBOverride = originalOverride })
	// 命中聚合槽位是进程级的：每个用例换一个新聚合器，否则上一个用例留下的
	// 槽位会让本次命中变成对旧库 rowID 的 UPDATE，新库里看不到行。
	originalRecorder := blacklistGateRecorder
	blacklistGateRecorder = func() *service.BlacklistHitRecorder {
		return service.NewBlacklistHitRecorder()
	}
	t.Cleanup(func() { blacklistGateRecorder = originalRecorder })
	return db
}

// enableBlacklistWithEntries 打开黑名单开关、落规则，并让**默认缓存**加载它们。
//
// 必须用默认缓存（service.DefaultBlacklistCache）：控制器接线读的就是它，
// 换成局部缓存就测不到真实路径。
//
// ⚠️ 收尾必须把单例置回「开关关闭 + 零规则」再 reload。只 reload 是不够的：
// 那样会把「已启用 + 规则指向本用例库」的快照留给同包其它用例，
// 于是几百个无关用例全部莫名其妙被 403（实测踩到过：phone_controller_test 里
// 一整片用例都报「旧号已被拉黑」）。
func enableBlacklistWithEntries(t *testing.T, db *gorm.DB, entries ...entity.BlacklistEntry) {
	t.Helper()
	for _, entry := range entries {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("create blacklist entry: %v", err)
		}
	}
	if err := db.Create(&entity.SystemSetting{
		Key: service.BlacklistEnabledSettingKey, Value: "true", Remark: "test",
	}).Error; err != nil {
		t.Fatalf("enable blacklist: %v", err)
	}
	cache := service.DefaultBlacklistCache()
	t.Cleanup(func() { resetDefaultBlacklistCache(t) })
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload blacklist cache: %v", err)
	}
}

// resetDefaultBlacklistCache 把进程级默认缓存恢复成「默认关闭、零规则」。
//
// 实现上直接构造一个**空快照且开关为 false** 的缓存内容：
// 不能靠「reload 当前测试库」——那会把本次的规则留下去。
func resetDefaultBlacklistCache(t *testing.T) {
	t.Helper()
	cache := service.DefaultBlacklistCache()
	// 用一个临时空库 reload：开关读不到 → false，规则表不存在 → 空快照。
	tmp, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:blacklist-gate-reset-%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open reset db: %v", err)
	}
	if err := tmp.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate reset db: %v", err)
	}
	if err := cache.Reload(tmp); err != nil {
		t.Fatalf("reset blacklist cache: %v", err)
	}
	if cache.Enabled() {
		t.Fatal("reset 后默认缓存必须处于关闭状态")
	}
}

// disableBlacklistAndReload 关掉开关并让默认缓存收敛（用于「开关关闭必须放行」）。
func disableBlacklistAndReload(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Create(&entity.SystemSetting{
		Key: service.BlacklistEnabledSettingKey, Value: "false", Remark: "test",
	}).Error; err != nil {
		t.Fatalf("disable blacklist: %v", err)
	}
	t.Cleanup(func() { resetDefaultBlacklistCache(t) })
	if err := service.DefaultBlacklistCache().Reload(db); err != nil {
		t.Fatalf("reload after disable: %v", err)
	}
}

func blacklistHitsInDB(t *testing.T, db *gorm.DB) []entity.BlacklistHit {
	t.Helper()
	var rows []entity.BlacklistHit
	if err := db.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("load blacklist hits: %v", err)
	}
	return rows
}

// assertBlockedResponse 断言 403 + 只含 reason。
//
// 第二条断言是这条用例的核心价值：一旦有人把 Pattern 拼进 message，
// 「规则边界泄漏」这个安全缺陷会立刻变红。
func assertBlockedResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantReason string, forbidden ...string) {
	t.Helper()
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v (raw=%s)", err, recorder.Body.String())
	}
	if payload["message"] != wantReason {
		t.Fatalf("message = %v, want %q", payload["message"], wantReason)
	}
	for _, secret := range forbidden {
		if secret == "" {
			continue
		}
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("响应体泄漏了规则细节 %q：%s", secret, recorder.Body.String())
		}
	}
}

// ===========================================================================
// 入口 1：公开注册 UserRegister
// ===========================================================================

// 被拉黑手机号注册：403 + 只回 reason + **不消费验证码** + 不建用户。
func TestUserRegisterRejectsBlacklistedPhoneBeforeConsumingCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistGateControllerDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("seed register setting: %v", err)
	}
	enableBlacklistWithEntries(t, db, entity.BlacklistEntry{
		Kind: service.BlacklistKindPhone, Pattern: "13800001111",
		ValueHash:   service.BlacklistPatternHashHex("13800001111"),
		MatchType:   service.BlacklistMatchExact,
		ValueMasked: "138****1111",
		Reason:      "该号码多次违规注册",
	})

	// 两个「消费侧」都改成计数桩：命中必须发生在它们之前，计数应保持 0。
	originalCaptcha, originalSMS := consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx
	t.Cleanup(func() {
		consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx = originalCaptcha, originalSMS
	})
	captchaCalls, smsCalls := 0, 0
	consumeRegisterCaptchaTokenInTx = func(*gorm.DB, string, string, string) error {
		captchaCalls++
		return nil
	}
	verifyRegisterSMSCodeInTx = func(*gorm.DB, string, string, string) (service.SMSVerificationEvidence, error) {
		smsCalls++
		return service.SMSVerificationEvidence{}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/register",
		bytes.NewBufferString(`{"username":"blocked_register","password":"Abc12345!","phone":"13800001111","smsCode":"123456","captchaToken":"t"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = "203.0.113.7:1234"
	UserRegister(ctx)

	assertBlockedResponse(t, recorder, "该号码多次违规注册", "13800001111", "138****1111")
	if captchaCalls != 0 || smsCalls != 0 {
		t.Fatalf("命中必须在消费验证码之前：captchaCalls=%d smsCalls=%d", captchaCalls, smsCalls)
	}
	var count int64
	if err := db.Model(&entity.User{}).Where("username = ?", "blocked_register").Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拉黑手机号不得建出用户，实际 %d 行", count)
	}
	rows := blacklistHitsInDB(t, db)
	if len(rows) != 1 || rows[0].Action != service.BlacklistActionBlocked || rows[0].Kind != service.BlacklistKindPhone {
		t.Fatalf("命中应写 1 行 blocked 留痕：%+v", rows)
	}
	if rows[0].Path != "/api/user/register" {
		t.Fatalf("留痕路径 = %q，应为路由模板", rows[0].Path)
	}
}

// 开关关闭：同一手机号 + 同一规则照样注册成功，且**零留痕**。
//
// 这条与上一条成对存在才有意义：只测「打开时拦住」证明不了默认行为没变。
func TestUserRegisterPassesSamePhoneWhenBlacklistSwitchOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistGateControllerDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("seed register setting: %v", err)
	}
	// 规则先建好，再**单独**把开关落成 false，证明拦不拦只看开关。
	if err := db.Create(&entity.BlacklistEntry{
		Kind: service.BlacklistKindPhone, Pattern: "13800001111",
		ValueHash: service.BlacklistPatternHashHex("13800001111"), MatchType: service.BlacklistMatchExact,
		ValueMasked: "138****1111", Reason: "该号码多次违规注册",
	}).Error; err != nil {
		t.Fatalf("create entry: %v", err)
	}
	disableBlacklistAndReload(t, db)

	originalCaptcha, originalSMS := consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx
	t.Cleanup(func() {
		consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx = originalCaptcha, originalSMS
	})
	consumeRegisterCaptchaTokenInTx = func(*gorm.DB, string, string, string) error { return nil }
	verifyRegisterSMSCodeInTx = func(*gorm.DB, string, string, string) (service.SMSVerificationEvidence, error) {
		return service.SMSVerificationEvidence{}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/register",
		bytes.NewBufferString(`{"username":"switch_off_register","password":"Abc12345!","phone":"13800001111","smsCode":"123456","captchaToken":"t"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = "203.0.113.7:1234"
	UserRegister(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("开关关闭时必须放行，status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if rows := blacklistHitsInDB(t, db); len(rows) != 0 {
		t.Fatalf("开关关闭时不得写命中日志，实际 %d 行：%+v", len(rows), rows)
	}
}

// ===========================================================================
// 入口 2：短信登录 UserSMSLogin
// ===========================================================================

func TestUserSMSLoginRejectsBlacklistedPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistGateControllerDB(t)
	phone := "13800001111"
	if err := db.Create(&entity.User{
		Username: "sms_blocked", Password: "hashed", Role: "user", Status: "enabled",
		RegisterSource: "public", Phone: &phone,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	enableBlacklistWithEntries(t, db, entity.BlacklistEntry{
		Kind: service.BlacklistKindPhone, Pattern: phone,
		ValueHash: service.BlacklistPatternHashHex(phone), MatchType: service.BlacklistMatchExact,
		ValueMasked: "138****1111", Reason: "账号异常，暂停登录",
	})

	originalConsume, originalVerify := consumeCaptchaTokenWithDB, verifySMSCodeWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB, verifySMSCodeWithDB = originalConsume, originalVerify })
	captchaCalls, smsCalls := 0, 0
	consumeCaptchaTokenWithDB = func(string, string, string) error { captchaCalls++; return nil }
	verifySMSCodeWithDB = func(string, string, string) (service.SMSVerificationEvidence, error) {
		smsCalls++
		return service.SMSVerificationEvidence{}, nil
	}

	recorder := phoneControllerRequest(t, http.MethodPost, "/api/user/login/sms",
		`{"phone":"13800001111","smsCode":"123456","captchaToken":"captcha"}`, nil, UserSMSLogin)

	assertBlockedResponse(t, recorder, "账号异常，暂停登录", "13800001111", "138****1111")
	if captchaCalls != 0 || smsCalls != 0 {
		t.Fatalf("命中必须在消费图形/短信验证码之前：captchaCalls=%d smsCalls=%d", captchaCalls, smsCalls)
	}
	// 黑名单命中是「拒绝」不是「一次登录失败」，不得记进爆破计数 ——
	// 否则被拉黑者反复重试会把整个出口 IP 锁死，误伤同 NAT 用户。
	var attempts int64
	if err := db.Model(&entity.LoginAttempt{}).Count(&attempts).Error; err != nil {
		t.Fatalf("count login attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("黑名单命中不得计入登录失败次数，实际 %d 行", attempts)
	}
}

// ===========================================================================
// 入口 3：登录用户换绑手机 UpdateCurrentUserPhone
// ===========================================================================

// 换绑判定的是**新手机号**：新号被拉黑 → 403 且不调更新函数。
func TestUpdateCurrentUserPhoneRejectsBlacklistedNewPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistGateControllerDB(t)
	enableBlacklistWithEntries(t, db, entity.BlacklistEntry{
		Kind: service.BlacklistKindPhone, Pattern: "13900002222",
		ValueHash: service.BlacklistPatternHashHex("13900002222"), MatchType: service.BlacklistMatchExact,
		ValueMasked: "139****2222", Reason: "该号码已被拉黑",
	})

	originalUpdate := updateUserPhoneWithVerificationWithDB
	t.Cleanup(func() { updateUserPhoneWithVerificationWithDB = originalUpdate })
	updateCalls := 0
	updateUserPhoneWithVerificationWithDB = func(int32, string, string) (entity.User, error) {
		updateCalls++
		return entity.User{}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 302, Username: "profile_user", Status: "enabled", Phone: strPtr("13800000000")})
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/user/phone",
		bytes.NewBufferString(`{"phone":"13900002222","smsCode":"123456"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	UpdateCurrentUserPhone(ctx)

	assertBlockedResponse(t, recorder, "该号码已被拉黑", "13900002222", "139****2222")
	if updateCalls != 0 {
		t.Fatal("命中必须在调用换绑实现之前（否则验证码已被消耗）")
	}
}

// 旧号被拉黑、新号干净 → 换绑**必须放行**。
//
// 反方向钉子：如果实现误判成「判定当前用户手机号」，这条会变红。
func TestUpdateCurrentUserPhoneIgnoresBlacklistedOldPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistGateControllerDB(t)
	enableBlacklistWithEntries(t, db, entity.BlacklistEntry{
		Kind: service.BlacklistKindPhone, Pattern: "13800000000",
		ValueHash: service.BlacklistPatternHashHex("13800000000"), MatchType: service.BlacklistMatchExact,
		ValueMasked: "138****0000", Reason: "旧号已被拉黑",
	})

	originalUpdate := updateUserPhoneWithVerificationWithDB
	t.Cleanup(func() { updateUserPhoneWithVerificationWithDB = originalUpdate })
	var receivedPhone string
	updateUserPhoneWithVerificationWithDB = func(userID int32, phone string, _ string) (entity.User, error) {
		receivedPhone = phone
		return entity.User{ID: userID, Username: "profile_user", Status: "enabled", Phone: &phone}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 302, Username: "profile_user", Status: "enabled", Phone: strPtr("13800000000")})
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/user/phone",
		bytes.NewBufferString(`{"phone":"13900002222","smsCode":"123456"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	UpdateCurrentUserPhone(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("旧号被拉黑不应阻止换到干净新号：status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if receivedPhone != "13900002222" {
		t.Fatalf("换绑实现收到的号码 = %q", receivedPhone)
	}
}

// ===========================================================================
// 入口 4：微信绑定 / 微信登录（判定在 service 层，控制器做状态码映射）
// ===========================================================================

// 绑定被拉黑的微信：控制器必须把 service 的命中错误翻成 403（不是 400）。
func TestBindCurrentUserWechatMapsBlacklistErrorTo403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newBlacklistGateControllerDB(t)

	originalBind := bindCurrentUserWechatWithDB
	t.Cleanup(func() { bindCurrentUserWechatWithDB = originalBind })
	bindCurrentUserWechatWithDB = func(string, int32) (entity.User, error) {
		return entity.User{}, service.NewBlacklistBlockedError(service.BlacklistMatch{
			EntryID: 3, Kind: service.BlacklistKindWechat, MatchType: service.BlacklistMatchExact,
			ValueMasked: "o***jkl", Reason: "该微信已被拉黑",
		})
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 303, Username: "profile_user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/wechat/bind",
		bytes.NewBufferString(`{"bindToken":"bind-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	BindCurrentUserWechat(ctx)

	assertBlockedResponse(t, recorder, "该微信已被拉黑", "oABCdefGHIjkl")
}

// 与黑名单无关的错误必须仍是 400（不能把所有错误都翻成 403）。
func TestBindCurrentUserWechatKeeps400ForNonBlacklistErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newBlacklistGateControllerDB(t)

	originalBind := bindCurrentUserWechatWithDB
	t.Cleanup(func() { bindCurrentUserWechatWithDB = originalBind })
	bindCurrentUserWechatWithDB = func(string, int32) (entity.User, error) {
		return entity.User{}, fmt.Errorf("该微信已绑定其他账号")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 303, Username: "profile_user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/wechat/bind",
		bytes.NewBufferString(`{"bindToken":"bind-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	BindCurrentUserWechat(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非黑名单错误应保持 400，got %d（body=%s）", recorder.Code, recorder.Body.String())
	}
}

func TestLoginWechatByCodeMapsBlacklistErrorTo403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newBlacklistGateControllerDB(t)

	originalSettings, originalLogin := loadWechatLoginSettings, loginWechatByCodeWithDB
	t.Cleanup(func() { loadWechatLoginSettings, loginWechatByCodeWithDB = originalSettings, originalLogin })
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatCodeLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	loginWechatByCodeWithDB = func(service.WechatCodeLoginInput) (service.WechatLoginStatusResult, error) {
		return service.WechatLoginStatusResult{}, service.NewBlacklistBlockedError(service.BlacklistMatch{
			EntryID: 4, Kind: service.BlacklistKindWechat, MatchType: service.BlacklistMatchExact,
			ValueMasked: "o***jkl", Reason: "该微信已被拉黑",
		})
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/public/wechat-login/code",
		bytes.NewBufferString(`{"code":"104014"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	LoginWechatByCode(ctx)

	assertBlockedResponse(t, recorder, "该微信已被拉黑", "oABCdefGHIjkl")
}

func strPtr(value string) *string { return &value }
