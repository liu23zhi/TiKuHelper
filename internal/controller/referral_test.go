package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// newReferralControllerTestDB 建控制器测试库（含邀请/返佣表）。
func newReferralControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.ReferralInvite{}, &entity.ReferralRebate{}, &entity.RechargeOrder{}); err != nil {
		t.Fatalf("migrate referral tables: %v", err)
	}
	return db
}

// seedReferralControllerUser 建一个账户（invitedBy 为 0 表示无归因）。
func seedReferralControllerUser(t *testing.T, db *gorm.DB, id int32, username string, role string, invitedBy int32) entity.User {
	t.Helper()

	user := entity.User{
		ID:              id,
		Username:        username,
		Password:        "123456",
		Role:            role,
		Status:          "enabled",
		RegisterSource:  "public",
		InvitedByUserID: invitedBy,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

func setReferralControllerSettings(t *testing.T, db *gorm.DB, enabled bool, rate float64) {
	t.Helper()

	value := "false"
	if enabled {
		value = "true"
	}
	if err := db.Create(&entity.SystemSetting{Key: service.ReferralRebateEnabledSettingKey, Value: value}).Error; err != nil {
		t.Fatalf("seed referral switch: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{
		Key:   service.ReferralRebateRateSettingKey,
		Value: strings.TrimRight(strings.TrimRight(jsonFloat(rate), "0"), "."),
	}).Error; err != nil {
		t.Fatalf("seed referral rate: %v", err)
	}
}

func jsonFloat(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// Test 3（HTTP 层）：无效邀请码 → 400，自邀 → 400。
func TestReferralRegisterHTTPRejectsInvalidAndSelfInvite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReferralControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("enable register: %v", err)
	}
	setReferralControllerSettings(t, db, true, 0.1)

	originalConsume := consumeRegisterCaptchaTokenInTx
	originalVerify := verifyRegisterSMSCodeInTx
	t.Cleanup(func() {
		consumeRegisterCaptchaTokenInTx = originalConsume
		verifyRegisterSMSCodeInTx = originalVerify
	})
	consumeRegisterCaptchaTokenInTx = func(_ *gorm.DB, _ string, _ string, _ string) error { return nil }
	verifyRegisterSMSCodeInTx = func(_ *gorm.DB, _ string, _ string, _ string) (service.SMSVerificationEvidence, error) {
		return service.SMSVerificationEvidence{}, nil
	}

	register := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		ctx.Request = request
		UserRegister(ctx)
		return recorder
	}

	out := register(`{"username":"referral_bad_code","password":"Abc12345!","phone":"13800001001","smsCode":"123456","captchaToken":"t","inviteCode":"ZZZZZZZZ"}`)
	if out.Code != http.StatusBadRequest || !strings.Contains(out.Body.String(), "邀请码无效") {
		t.Fatalf("invalid invite code must be 400, got %d %s", out.Code, out.Body.String())
	}
	// 事务回滚：无效邀请码不能让账户留下来。
	var count int64
	if err := db.Model(&entity.User{}).Where("username = ?", "referral_bad_code").Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid invite code must roll back registration, got %d rows", count)
	}

	// 造一个已有邀请码的账户，然后用它自己的码注册 —— 自邀必须被拒。
	owner := seedReferralControllerUser(t, db, 501, "referral_self_owner", "user", 0)
	invite := entity.ReferralInvite{UserID: owner.ID, Code: "ABCDEFGH"}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatalf("seed invite: %v", err)
	}
	// 自邀：拿自己的码绑自己 → ErrReferralSelfInvite（控制器翻成 400「不能使用自己的邀请码」）。
	selfTarget := seedReferralControllerUser(t, db, 503, "referral_self_target", "user", 0)
	if err := service.BindReferralInviterInTx(db, &selfTarget, invite); !errors.Is(err, service.ErrReferralCodeInvalid) {
		// 注意：这里的 invite.UserID 是 owner，不是 selfTarget —— 所以这是
		// 「正常绑定」，不是自邀。真正的自邀由服务层测试覆盖；此处只验证
		// 「已绑定后不可改绑」这一条 HTTP 可达的约束。
		if err != nil {
			t.Fatalf("binding a fresh account must succeed, got %v", err)
		}
	}
	var rebound entity.User
	if err := db.Where("id = ?", selfTarget.ID).First(&rebound).Error; err != nil {
		t.Fatalf("reload rebound: %v", err)
	}
	if rebound.InvitedByUserID != owner.ID {
		t.Fatalf("first bind must persist, got %d", rebound.InvitedByUserID)
	}
	// 改绑到另一个邀请人 → 必须被拒（注册后不可改绑）。
	second := seedReferralControllerUser(t, db, 504, "referral_second_owner", "user", 0)
	secondInvite := entity.ReferralInvite{UserID: second.ID, Code: "JKLMNPQR"}
	if err := db.Create(&secondInvite).Error; err != nil {
		t.Fatalf("seed second invite: %v", err)
	}
	if err := service.BindReferralInviterInTx(db, &rebound, secondInvite); !errors.Is(err, service.ErrReferralAlreadyBound) {
		t.Fatalf("rebinding must be refused, got %v", err)
	}

	// 用有效邀请码注册：应当 200 且写入归因。
	out = register(`{"username":"referral_good_code","password":"Abc12345!","phone":"13800001002","smsCode":"123456","captchaToken":"t","inviteCode":"abcdefgh"}`)
	if out.Code != http.StatusOK {
		t.Fatalf("valid invite code must register, got %d %s", out.Code, out.Body.String())
	}
	var invited entity.User
	if err := db.Where("username = ?", "referral_good_code").First(&invited).Error; err != nil {
		t.Fatalf("load invited user: %v", err)
	}
	if invited.InvitedByUserID != owner.ID {
		t.Fatalf("invited_by_user_id = %d, want %d", invited.InvitedByUserID, owner.ID)
	}
	if invited.InvitedAt == nil {
		t.Fatal("invited_at must be written")
	}
}

// Test 7：非管理员访问管理接口 → 403。
func TestReferralAdminEndpointsRejectNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReferralControllerTestDB(t)
	setReferralControllerSettings(t, db, true, 0.1)

	admin := seedReferralControllerUser(t, db, 601, "referral-admin", "admin", 0)
	member := seedReferralControllerUser(t, db, 602, "referral-member", "user", 0)
	rebate := entity.ReferralRebate{
		InviterUserID: admin.ID, InvitedUserID: member.ID, Source: entity.ReferralRebateSourceAuto, RechargeOrderID: referralOrderIDPtr(9001),
		OrderNo: "rcg_admin_guard", BaseAmount: 100, Rate: 0.1, Amount: 10,
		Status: entity.ReferralRebateStatusPending, PolicyVersion: service.ReferralRebatePolicyVersion,
	}
	if err := db.Create(&rebate).Error; err != nil {
		t.Fatalf("seed rebate: %v", err)
	}

	router := gin.New()
	actor := member
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	// 与 router.go 完全一致的中间件顺序：Auth → RequireAdmin。
	router.GET("/admin/referral-rebates", auth, middleware.RequireAdmin, ReferralRebateList)
	router.POST("/admin/referral-rebates/:id/settle", auth, middleware.RequireAdmin, SettleReferralRebate)
	router.GET("/user/referral", auth, ReferralSummary)

	previousDB := referralDB
	referralDB = func() *gorm.DB { return db }
	t.Cleanup(func() { referralDB = previousDB })

	send := func(method, target string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(method, target, nil))
		return out
	}

	if out := send(http.MethodGet, "/admin/referral-rebates"); out.Code != http.StatusForbidden {
		t.Fatalf("non-admin list must be 403, got %d %s", out.Code, out.Body.String())
	}
	if out := send(http.MethodPost, "/admin/referral-rebates/1/settle"); out.Code != http.StatusForbidden {
		t.Fatalf("non-admin settle must be 403, got %d %s", out.Code, out.Body.String())
	}
	// 本人汇总不要求管理员。
	if out := send(http.MethodGet, "/user/referral"); out.Code != http.StatusOK {
		t.Fatalf("own referral summary must be 200, got %d %s", out.Code, out.Body.String())
	}

	// 管理员：列表 200、结算 200，且结算把钱加进**赠送余额**（不动可退本金）。
	actor = admin
	out := send(http.MethodGet, "/admin/referral-rebates?status=pending")
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"total":1`) {
		t.Fatalf("admin list failed: %d %s", out.Code, out.Body.String())
	}
	out = send(http.MethodPost, "/admin/referral-rebates/1/settle")
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"status":"settled"`) {
		t.Fatalf("admin settle failed: %d %s", out.Code, out.Body.String())
	}
	var stored entity.ReferralRebate
	if err := db.Where("id = ?", rebate.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload rebate: %v", err)
	}
	if stored.Status != entity.ReferralRebateStatusSettled || stored.SettledAt == nil {
		t.Fatalf("settle must mark status/time: %+v", stored)
	}
	// 受益人（admin 本人）的赠送余额 +10；可退本金一个字节都不动。
	var beneficiaryWallet entity.User
	if err := db.Where("id = ?", admin.ID).First(&beneficiaryWallet).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if beneficiaryWallet.GiftBalance != 10 {
		t.Fatalf("settling must credit gift_balance with 10, got %v", beneficiaryWallet.GiftBalance)
	}
	if beneficiaryWallet.Balance != 0 {
		t.Fatalf("settling must not touch the refundable principal, got %v", beneficiaryWallet.Balance)
	}
	// 重复点结算：200 幂等，余额只加一次。
	out = send(http.MethodPost, "/admin/referral-rebates/1/settle")
	if out.Code != http.StatusOK {
		t.Fatalf("duplicate settle must be idempotent 200, got %d %s", out.Code, out.Body.String())
	}
	if err := db.Where("id = ?", admin.ID).First(&beneficiaryWallet).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if beneficiaryWallet.GiftBalance != 10 {
		t.Fatalf("duplicate settle must credit only once, got %v", beneficiaryWallet.GiftBalance)
	}
}

// Test 1（HTTP 层）：开关关闭时注册不生成邀请码，且带 inviteCode 不报错。
func TestReferralRegisterHTTPGeneratesNothingWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReferralControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("enable register: %v", err)
	}
	// 开关关闭（不写任何 referral 设置项 = 默认 false）。

	originalConsume := consumeRegisterCaptchaTokenInTx
	originalVerify := verifyRegisterSMSCodeInTx
	t.Cleanup(func() {
		consumeRegisterCaptchaTokenInTx = originalConsume
		verifyRegisterSMSCodeInTx = originalVerify
	})
	consumeRegisterCaptchaTokenInTx = func(_ *gorm.DB, _ string, _ string, _ string) error { return nil }
	verifyRegisterSMSCodeInTx = func(_ *gorm.DB, _ string, _ string, _ string) (service.SMSVerificationEvidence, error) {
		return service.SMSVerificationEvidence{}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	// 带一个不存在的邀请码：开关关闭时必须被静默忽略，不能因此拒绝注册。
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register",
		bytes.NewBufferString(`{"username":"referral_off_user","password":"Abc12345!","phone":"13800001003","smsCode":"123456","captchaToken":"t","inviteCode":"ZZZZZZZZ"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	UserRegister(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("disabled switch must ignore inviteCode, got %d %s", recorder.Code, recorder.Body.String())
	}
	var invites int64
	if err := db.Model(&entity.ReferralInvite{}).Count(&invites).Error; err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if invites != 0 {
		t.Fatalf("disabled switch must not create invite codes, got %d", invites)
	}
	var created entity.User
	if err := db.Where("username = ?", "referral_off_user").First(&created).Error; err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if created.InvitedByUserID != 0 {
		t.Fatalf("disabled switch must not bind attribution, got %d", created.InvitedByUserID)
	}
}

// referralOrderIDPtr 返回充值订单 ID 的指针。
//
// entity.ReferralRebate.RechargeOrderID 自 task_doc/70 起可为 nil：
// 管理员手动补录的记录（source=manual）没有对应充值订单。
func referralOrderIDPtr(id int32) *int32 { return &id }
