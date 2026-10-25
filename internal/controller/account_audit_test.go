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
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestPasswordLoginBindsOnlySuccessfulCaptcha(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, succeeds := range []bool{false, true} {
		t.Run(fmt.Sprint(succeeds), func(t *testing.T) {
			db := newUserControllerTestDB(t)
			password, err := util.HashPassword("Abc12345!")
			if err != nil {
				t.Fatal(err)
			}
			user := entity.User{Username: "anonymous", Password: password, Status: "enabled"}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			token := entity.CaptchaToken{Token: "login-token", Scene: service.CaptchaSceneLoginPassword, ChallengeID: "remote:used", Status: service.CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}
			if err := db.Create(&token).Error; err != nil {
				t.Fatal(err)
			}
			logs := []entity.Log{{Module: "captcha", CorrelationID: "remote:used"}, {Module: "captcha", CorrelationID: "remote:unused"}}
			if err := db.Create(&logs).Error; err != nil {
				t.Fatal(err)
			}
			inputPassword := "wrong-password"
			if succeeds {
				inputPassword = "Abc12345!"
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/user/login", strings.NewReader(fmt.Sprintf(`{"username":"anonymous","password":%q,"captchaToken":"login-token"}`, inputPassword)))
			ctx.Request.Header.Set("Content-Type", "application/json")
			UserLogin(ctx)
			if succeeds != (recorder.Code == http.StatusOK) {
				t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
			}
			var result []entity.Log
			if err := db.Where("module = ?", "captcha").Order("id").Find(&result).Error; err != nil {
				t.Fatal(err)
			}
			wantID := int32(0)
			if succeeds {
				wantID = user.ID
			}
			if result[0].UserID != wantID || result[1].UserID != 0 {
				t.Fatalf("wrong association: %+v", result)
			}
			if succeeds && result[0].Username != "anonymous" {
				t.Fatalf("real anonymous username lost: %+v", result[0])
			}
		})
	}
}

func TestRegistrationBindsExactSMSAndBothCaptchaSteps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	token := entity.CaptchaToken{Token: "register-token", Scene: service.CaptchaSceneRegisterSubmit, ChallengeID: "local:submit", Status: service.CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	sms := entity.SMSVerificationCode{Phone: "13800000000", Scene: service.SMSSceneRegister, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute), CaptchaChallengeID: "remote:before-sms"}
	if err := db.Create(&sms).Error; err != nil {
		t.Fatal(err)
	}
	logs := []entity.Log{{Module: "captcha", CorrelationID: "local:submit"}, {Module: "captcha", CorrelationID: "remote:before-sms"}, {Module: "sms", CorrelationID: fmt.Sprintf("sms:%d", sms.ID)}, {Module: "captcha", CorrelationID: "remote:unused"}}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/user/register", strings.NewReader(`{"username":"new_user","password":"Abc12345!","phone":"13800000000","smsCode":"123456","captchaToken":"register-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	UserRegister(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("registration failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var user entity.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	var rows []entity.Log
	if err := db.Where("module IN ?", []string{"captcha", "sms"}).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if rows[i].UserID != user.ID || rows[i].Username != user.Username {
			t.Fatalf("unbound actual evidence: %+v", rows[i])
		}
	}
	if rows[3].UserID != 0 {
		t.Fatal("unused evidence acquired owner")
	}
	var events []entity.Log
	if err := db.Where("module = ? AND action = ?", "auth", 12).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || bytes.Contains([]byte(events[0].OldAnswer+events[0].NewAnswer), []byte("123456")) {
		t.Fatalf("invalid registration audit: %+v", events)
	}
}

func TestLogoutAlwaysClearsCookieAndOnlyAuditsKnownIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	user := entity.User{Username: "logout_user", Password: "hashed-password", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "expired.invalid", util.GenerateJwtForUser(user.ID, user.Password)} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/user/logout", nil)
		ctx.Request.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
		UserLogout(ctx)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Set-Cookie"), "Max-Age=0") {
			t.Fatalf("logout must clear cookie: %d %s", recorder.Code, recorder.Header())
		}
	}
	var events []entity.Log
	if err := db.Where("module = ? AND action = ?", "auth", 13).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].UserID != user.ID {
		t.Fatalf("logout must only audit authenticated user once: %+v", events)
	}
}

func TestSMSLoginBindsOnlyActualConsumedVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	phone := "13800000000"
	user := entity.User{Username: "sms_user", Password: "password-hash", Status: "enabled", Phone: &phone}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.CaptchaToken{Token: "sms-login-token", Scene: service.CaptchaSceneLoginSMS, ChallengeID: "remote:final", Status: service.CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	sms := entity.SMSVerificationCode{Phone: phone, Scene: service.SMSSceneLogin, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute), CaptchaChallengeID: "remote:before-sms"}
	if err := db.Create(&sms).Error; err != nil {
		t.Fatal(err)
	}
	logs := []entity.Log{{Module: "captcha", CorrelationID: "remote:final"}, {Module: "captcha", CorrelationID: "remote:before-sms"}, {Module: "sms", CorrelationID: fmt.Sprintf("sms:%d", sms.ID)}, {Module: "sms", CorrelationID: "sms:999"}}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/user/login/sms", strings.NewReader(`{"phone":"13800000000","smsCode":"123456","captchaToken":"sms-login-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	UserSMSLogin(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("SMS login failed: %d %s", recorder.Code, recorder.Body.String())
	}
	for i, entry := range logs {
		var result entity.Log
		if err := db.First(&result, entry.ID).Error; err != nil {
			t.Fatal(err)
		}
		want := user.ID
		if i == 3 {
			want = 0
		}
		if result.UserID != want {
			t.Fatalf("wrong SMS ownership: %+v", result)
		}
	}
}

func TestRegistrationRollbackNeverClaimsVerificationLogs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, useWechat := range []bool{false, true} {
		t.Run(fmt.Sprint(useWechat), func(t *testing.T) {
			db := newUserControllerTestDB(t)
			if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
				t.Fatal(err)
			}
			phone, openid := "13800000000", "already-bound-openid"
			existing := entity.User{Username: "existing", Phone: &phone, WechatID: &openid}
			if err := db.Create(&existing).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entity.CaptchaToken{Token: "register-fail", Scene: service.CaptchaSceneRegisterSubmit, ChallengeID: "remote:submit", Status: service.CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}).Error; err != nil {
				t.Fatal(err)
			}
			sms := entity.SMSVerificationCode{Phone: phone, Scene: service.SMSSceneRegister, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute), CaptchaChallengeID: "local:before-sms"}
			if err := db.Create(&sms).Error; err != nil {
				t.Fatal(err)
			}
			logs := []entity.Log{{Module: "captcha", CorrelationID: "remote:submit"}, {Module: "captcha", CorrelationID: "local:before-sms"}, {Module: "sms", CorrelationID: fmt.Sprintf("sms:%d", sms.ID)}}
			if err := db.Create(&logs).Error; err != nil {
				t.Fatal(err)
			}
			flowToken := ""
			if useWechat {
				originalStore := wechatRegisterFlowStore
				wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
				defer func() { wechatRegisterFlowStore = originalStore }()
				flow, err := wechatRegisterFlowStore.CreateOrResume("", time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if err := wechatRegisterFlowStore.Stage(flow.Token, openid, time.Now()); err != nil {
					t.Fatal(err)
				}
				flowToken = flow.Token
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/user/register", strings.NewReader(fmt.Sprintf(`{"username":"failed_user","password":"Abc12345!","phone":"13800000000","smsCode":"123456","captchaToken":"register-fail","registrationFlowToken":%q}`, flowToken)))
			ctx.Request.Header.Set("Content-Type", "application/json")
			UserRegister(ctx)
			if recorder.Code == http.StatusOK {
				t.Fatal("conflicting registration should fail")
			}
			var bound, registered int64
			if err := db.Model(&entity.Log{}).Where("module IN ? AND user_id <> 0", []string{"captcha", "sms"}).Count(&bound).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&entity.Log{}).Where("module = ? AND action = ?", "auth", 12).Count(&registered).Error; err != nil {
				t.Fatal(err)
			}
			if bound != 0 || registered != 0 {
				t.Fatalf("failed flow claimed logs: bound=%d registered=%d", bound, registered)
			}
		})
	}
}

func TestSMSRequestPreservesServerValidatedCaptchaEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.CaptchaToken{Token: "before-send", Scene: service.CaptchaSceneSMSSendLogin, ChallengeID: "remote:send", Status: service.CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	originalSend := sendPublicSMSCodeWithDB
	defer func() { sendPublicSMSCodeWithDB = originalSend }()
	calls := 0
	sendPublicSMSCodeWithDB = func(input service.SendSMSCodeInput) error {
		calls++
		if input.CaptchaCorrelationID != "remote:send" {
			t.Fatalf("missing server evidence: %+v", input)
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/public/sms-code", strings.NewReader(`{"phone":"13800000000","scene":"login","captchaToken":"before-send","captchaCorrelationId":"forged"}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Request.Header.Set("Idempotency-Key", "send-with-proof")
		SendPublicSMSCode(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("send failed: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("SMS replay sent again: %d", calls)
	}
}

func TestAccountMutationAuditUsesActorAndSafeChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	phone := "13800000000"
	admin := entity.User{Username: "admin_actor", Role: "admin", Status: "enabled"}
	target := entity.User{Username: "target", Role: "user", Status: "enabled", Phone: &phone}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	resetRecorder := httptest.NewRecorder()
	resetCtx, _ := gin.CreateTestContext(resetRecorder)
	resetCtx.Set("user", &admin)
	resetCtx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(target.ID)}}
	resetCtx.Request = httptest.NewRequest(http.MethodPut, "/user/password", strings.NewReader(`{"password":"New-Password123!"}`))
	resetCtx.Request.Header.Set("Content-Type", "application/json")
	ResetUserPassword(resetCtx)
	if resetRecorder.Code != 200 {
		t.Fatalf("reset failed: %s", resetRecorder.Body.String())
	}
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set("user", &target)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/user/profile", strings.NewReader(`{"username":"target","nickname":"新昵称"}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		UpdateCurrentUserProfile(ctx)
		if recorder.Code != 200 {
			t.Fatalf("profile failed: %s", recorder.Body.String())
		}
		if err := db.First(&target, target.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	var events []entity.Log
	if err := db.Where("module = ?", "user").Order("id").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].UserID != admin.ID || events[0].Action != 14 || events[1].UserID != target.ID || events[1].Action != 16 {
		t.Fatalf("incorrect actors or no-op audit: %+v", events)
	}
	if strings.Contains(events[0].NewAnswer+events[0].OldAnswer, "Password") {
		t.Fatal("password leaked to audit")
	}
	if !strings.Contains(events[0].NewAnswer, fmt.Sprintf(`"targetUserId":%d`, target.ID)) {
		t.Fatal("missing target")
	}
	for _, role := range []string{"user", "admin"} {
		viewer := target
		viewer.Role = role
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set("user", &viewer)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/logs?module=user", nil)
		LogList(ctx)
		var response struct {
			Total int64 `json:"total"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		want := int64(1)
		if role == "admin" {
			want = 2
		}
		if recorder.Code != 200 || response.Total != want {
			t.Fatalf("audit scope mismatch: %s", recorder.Body.String())
		}
	}
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set("user", &admin)
		ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(target.ID)}}
		ctx.Request = httptest.NewRequest(http.MethodPut, "/user", strings.NewReader(`{"nickname":"管理员修改","role":"user","status":"enabled","phone":"13900000000"}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		UpdateUser(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("admin update failed: %s", recorder.Body.String())
		}
	}
	var adminChanges []entity.Log
	if err := db.Where("module = ? AND user_id = ? AND action IN ?", "user", admin.ID, []int{15, 16}).Order("id").Find(&adminChanges).Error; err != nil {
		t.Fatal(err)
	}
	if len(adminChanges) != 2 || adminChanges[0].Action != 15 || adminChanges[1].Action != 16 {
		t.Fatalf("missing changes or duplicate no-op audit: %+v", adminChanges)
	}
	if strings.Contains(adminChanges[0].NewAnswer, "13900000000") || !strings.Contains(adminChanges[0].NewAnswer, "139****0000") {
		t.Fatalf("phone audit must be masked: %+v", adminChanges[0])
	}
}

func TestRechargeAuditLinksBillsWithoutExposingOtherOrHiddenBills(t *testing.T) {
	db := newUserControllerTestDB(t)
	entries := []*entity.Log{{UserID: 1, Module: "recharge", NewAnswer: `{"orderNo":"order-1","billingId":999}`, CorrelationID: "order-1"}, {UserID: 2, Module: "recharge", OldAnswer: `{"orderNo":"order-1"}`}}
	bills := []entity.BillingRecord{{UserID: 1, RequestID: "order-1"}, {UserID: 1, RequestID: "order-1", HiddenFromStatement: true}, {UserID: 2, RequestID: "order-1"}}
	if err := db.Create(&bills).Error; err != nil {
		t.Fatal(err)
	}
	self, err := logListItemsWithBillingIDs(db, &entity.User{ID: 1, Role: "user"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(self[0].BillingIDs) != 1 || self[0].BillingIDs[0] != bills[0].ID || len(self[1].BillingIDs) != 0 {
		t.Fatalf("incorrect personal bill associations: %+v", self)
	}
	admin, err := logListItemsWithBillingIDs(db, &entity.User{ID: 3, Role: "admin"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin[0].BillingIDs) != 2 || len(admin[1].BillingIDs) != 1 || admin[1].BillingIDs[0] != bills[2].ID {
		t.Fatalf("incorrect admin bill associations: %+v", admin)
	}
}
