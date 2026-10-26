package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func phoneControllerRequest(t *testing.T, method, path, body string, user *entity.User, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if user != nil {
		ctx.Set("user", user)
	}
	if method == http.MethodPut && user != nil {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		ctx.Params = gin.Params{{Key: "id", Value: parts[len(parts)-1]}}
	}
	handler(ctx)
	return recorder
}

func TestUserSMSLoginSupportsLegacyPhoneAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	phone := "+8613800000000"
	user := entity.User{Username: "legacy-phone-login", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &phone}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	originalConsume, originalVerify := consumeCaptchaTokenWithDB, verifySMSCodeWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB, verifySMSCodeWithDB = originalConsume, originalVerify })
	consumeCaptchaTokenWithDB = func(string, string, string) error { return nil }
	verifiedPhone := ""
	verifySMSCodeWithDB = func(phone, scene, code string) (service.SMSVerificationEvidence, error) {
		verifiedPhone = phone
		return service.SMSVerificationEvidence{}, nil
	}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/login/sms", `{"phone":"13800000000","smsCode":"123456","captchaToken":"captcha"}`, nil, UserSMSLogin)
	if response.Code != http.StatusOK || verifiedPhone != "13800000000" {
		t.Fatalf("legacy alias must login using canonical SMS number: status=%d phone=%q body=%s", response.Code, verifiedPhone, response.Body.String())
	}
}

func TestUserSMSLoginRejectsConflictingPhoneAliasesBeforeCodeConsumption(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	for index, phone := range []string{"13800000000", "+8613800000000"} {
		phone := phone
		if err := db.Create(&entity.User{Username: fmt.Sprintf("phone-conflict-%d", index), Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &phone}).Error; err != nil {
			t.Fatal(err)
		}
	}
	originalConsume, originalVerify := consumeCaptchaTokenWithDB, verifySMSCodeWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB, verifySMSCodeWithDB = originalConsume, originalVerify })
	consumeCaptchaTokenWithDB = func(string, string, string) error { return nil }
	verifyCalls := 0
	verifySMSCodeWithDB = func(string, string, string) (service.SMSVerificationEvidence, error) {
		verifyCalls++
		return service.SMSVerificationEvidence{}, nil
	}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/login/sms", `{"phone":"13800000000","smsCode":"123456","captchaToken":"captcha"}`, nil, UserSMSLogin)
	if response.Code != http.StatusBadRequest || verifyCalls != 0 || !strings.Contains(response.Body.String(), "历史绑定冲突") {
		t.Fatalf("ambiguous aliases must not login or consume code: status=%d verify=%d body=%s", response.Code, verifyCalls, response.Body.String())
	}
}

func TestPhoneControllersRejectInvalidNumbersBeforeSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	originalConsume, originalVerify := consumeCaptchaTokenWithDB, verifySMSCodeWithDB
	originalPublic, originalChange := sendPublicSMSCodeWithDB, sendUserPhoneChangeSMSCodeWithDB
	originalUpdate := updateUserPhoneWithVerificationWithDB
	originalRegisterConsume, originalRegisterVerify := consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx
	t.Cleanup(func() {
		consumeCaptchaTokenWithDB, verifySMSCodeWithDB = originalConsume, originalVerify
		sendPublicSMSCodeWithDB, sendUserPhoneChangeSMSCodeWithDB = originalPublic, originalChange
		updateUserPhoneWithVerificationWithDB = originalUpdate
		consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx = originalRegisterConsume, originalRegisterVerify
	})
	sideEffects := 0
	consumeCaptchaTokenWithDB = func(string, string, string) error { sideEffects++; return nil }
	verifySMSCodeWithDB = func(string, string, string) (service.SMSVerificationEvidence, error) {
		sideEffects++
		return service.SMSVerificationEvidence{}, nil
	}
	sendPublicSMSCodeWithDB = func(service.SendSMSCodeInput) error { sideEffects++; return nil }
	sendUserPhoneChangeSMSCodeWithDB = func(service.SendSMSCodeInput) error { sideEffects++; return nil }
	updateUserPhoneWithVerificationWithDB = func(int32, string, string) (entity.User, error) { sideEffects++; return entity.User{}, nil }
	consumeRegisterCaptchaTokenInTx = func(*gorm.DB, string, string, string) error { sideEffects++; return nil }
	verifyRegisterSMSCodeInTx = func(*gorm.DB, string, string, string) (service.SMSVerificationEvidence, error) {
		sideEffects++
		return service.SMSVerificationEvidence{}, nil
	}
	user := &entity.User{ID: 50, Role: "user", Status: "enabled"}
	for _, phone := range []string{"+1 13800000000", "1380000000a", "138000000000"} {
		for _, endpoint := range []struct {
			name    string
			handler gin.HandlerFunc
			body    string
			user    *entity.User
		}{
			{"public sms", SendPublicSMSCode, `{"phone":%q,"scene":"register","captchaToken":"captcha"}`, nil},
			{"change sms", SendUserPhoneChangeSMSCode, `{"phone":%q,"captchaToken":"captcha"}`, user},
			{"change phone", UpdateCurrentUserPhone, `{"phone":%q,"smsCode":"123456"}`, user},
			{"sms login", UserSMSLogin, `{"phone":%q,"smsCode":"123456","captchaToken":"captcha"}`, nil},
			{"register", UserRegister, `{"username":"invalid_phone_user","password":"Abc12345!","phone":%q,"smsCode":"123456","captchaToken":"captcha"}`, nil},
		} {
			response := phoneControllerRequest(t, http.MethodPost, "/test", fmt.Sprintf(endpoint.body, phone), endpoint.user, endpoint.handler)
			if response.Code != http.StatusBadRequest || sideEffects != 0 {
				t.Fatalf("%s accepted invalid phone %q or caused side effect: status=%d effects=%d body=%s", endpoint.name, phone, response.Code, sideEffects, response.Body.String())
			}
		}
	}
}

func TestPhoneControllersNormalizeSupportedPrefixBeforePassingToServices(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)
	originalConsume, originalPublic := consumeCaptchaTokenWithDB, sendPublicSMSCodeWithDB
	originalChange, originalUpdate := sendUserPhoneChangeSMSCodeWithDB, updateUserPhoneWithVerificationWithDB
	t.Cleanup(func() {
		consumeCaptchaTokenWithDB, sendPublicSMSCodeWithDB = originalConsume, originalPublic
		sendUserPhoneChangeSMSCodeWithDB, updateUserPhoneWithVerificationWithDB = originalChange, originalUpdate
	})
	consumeCaptchaTokenWithDB = func(string, string, string) error { return nil }
	phones := []string{}
	sendPublicSMSCodeWithDB = func(input service.SendSMSCodeInput) error { phones = append(phones, input.Phone); return nil }
	sendUserPhoneChangeSMSCodeWithDB = func(input service.SendSMSCodeInput) error { phones = append(phones, input.Phone); return nil }
	updateUserPhoneWithVerificationWithDB = func(_ int32, phone, _ string) (entity.User, error) {
		phones = append(phones, phone)
		return entity.User{}, nil
	}
	user := &entity.User{ID: 50, Role: "user", Status: "enabled"}
	for _, endpoint := range []struct {
		handler gin.HandlerFunc
		body    string
		user    *entity.User
	}{
		{SendPublicSMSCode, `{"phone":"+86 138 0000 0000","scene":"register","captchaToken":"captcha"}`, nil},
		{SendUserPhoneChangeSMSCode, `{"phone":"+86 138 0000 0000","captchaToken":"captcha"}`, user},
		{UpdateCurrentUserPhone, `{"phone":"+86 138 0000 0000","smsCode":"123456"}`, user},
	} {
		response := phoneControllerRequest(t, http.MethodPost, "/test", endpoint.body, endpoint.user, endpoint.handler)
		if response.Code != http.StatusOK {
			t.Fatalf("supported prefix should normalize: %d %s", response.Code, response.Body.String())
		}
	}
	if strings.Join(phones, ",") != "13800000000,13800000000,13800000000" {
		t.Fatalf("services must only receive canonical phone: %v", phones)
	}
}

func TestCreateUserRejectsHistoricalPhoneAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	legacyPhone := "+8613800000000"
	if err := db.Create(&entity.User{Username: "legacy-owner", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &legacyPhone}).Error; err != nil {
		t.Fatal(err)
	}
	admin := &entity.User{ID: 500, Role: "admin", Status: "enabled"}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user", `{"username":"duplicate_phone","password":"Abc12345!","phone":"13800000000","giftEnabled":false}`, admin, CreateUser)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "手机号") {
		t.Fatalf("admin create must reject occupied historical phone: %d %s", response.Code, response.Body.String())
	}
	var count int64
	db.Model(&entity.User{}).Where("username = ?", "duplicate_phone").Count(&count)
	if count != 0 {
		t.Fatalf("conflicting user was created: %d", count)
	}
}

func TestCreateUserNormalizesPhoneBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 500, Role: "admin", Status: "enabled"}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user", `{"username":"canonical_phone","password":"Abc12345!","phone":"+86 138 0000 0000","giftEnabled":false}`, admin, CreateUser)
	if response.Code != http.StatusOK {
		t.Fatalf("admin create should accept supported prefix: %d %s", response.Code, response.Body.String())
	}
	var stored entity.User
	if err := db.Where("username = ?", "canonical_phone").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Phone == nil || *stored.Phone != "13800000000" || stored.PhoneBoundAt == nil {
		t.Fatalf("admin create must store canonical phone and bind time: %+v", stored)
	}
}

func TestUserRegisterRejectsHistoricalPhoneAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	legacyPhone := "+8613800000000"
	if err := db.Create(&entity.User{Username: "register_legacy_owner", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &legacyPhone}).Error; err != nil {
		t.Fatal(err)
	}
	originalConsume, originalVerify := consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx
	t.Cleanup(func() { consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx = originalConsume, originalVerify })
	consumeRegisterCaptchaTokenInTx = func(*gorm.DB, string, string, string) error { return nil }
	verifyRegisterSMSCodeInTx = func(*gorm.DB, string, string, string) (service.SMSVerificationEvidence, error) {
		return service.SMSVerificationEvidence{}, nil
	}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/register", `{"username":"register_alias_conflict","password":"Abc12345!","phone":"13800000000","smsCode":"123456","captchaToken":"captcha"}`, nil, UserRegister)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "该手机号已被绑定") {
		t.Fatalf("register must reject historical alias: %d %s", response.Code, response.Body.String())
	}
	var count int64
	db.Model(&entity.User{}).Where("username = ?", "register_alias_conflict").Count(&count)
	if count != 0 {
		t.Fatalf("conflicting registered user was created: %d", count)
	}
}

func TestOrdinaryRegistrationHistoricalAliasConflictRollsBackVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	legacyPhone := "+8613800000000"
	if err := db.Create(&entity.User{Username: "ordinary_legacy_owner", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &legacyPhone}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	captcha := entity.CaptchaToken{Token: "ordinary-conflict-captcha", Scene: service.CaptchaSceneRegisterSubmit, ChallengeID: "local:ordinary-conflict", Status: service.CaptchaTokenStatusActive, ExpiresAt: now.Add(time.Minute)}
	if err := db.Create(&captcha).Error; err != nil {
		t.Fatal(err)
	}
	code := entity.SMSVerificationCode{Phone: "13800000000", Scene: service.SMSSceneRegister, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: now.Add(time.Minute), CaptchaChallengeID: "remote:ordinary-before-sms"}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	logs := []entity.Log{{Module: "captcha", CorrelationID: captcha.ChallengeID}, {Module: "captcha", CorrelationID: code.CaptchaChallengeID}, {Module: "sms", CorrelationID: fmt.Sprintf("sms:%d", code.ID)}}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/register", `{"username":"ordinary_alias_conflict","password":"Abc12345!","phone":"13800000000","smsCode":"123456","captchaToken":"ordinary-conflict-captcha"}`, nil, UserRegister)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "该手机号已被绑定") {
		t.Fatalf("ordinary registration must reject historical alias: %d %s", response.Code, response.Body.String())
	}
	var storedCode entity.SMSVerificationCode
	if err := db.First(&storedCode, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedCode.Status != service.SMSCodeStatusPending || storedCode.ConsumedAt != nil {
		t.Fatalf("failed ordinary registration consumed SMS code: %+v", storedCode)
	}
	var storedCaptcha entity.CaptchaToken
	if err := db.First(&storedCaptcha, captcha.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedCaptcha.Status != service.CaptchaTokenStatusActive || storedCaptcha.ConsumedAt != nil {
		t.Fatalf("failed ordinary registration consumed captcha: %+v", storedCaptcha)
	}
	var claimed, registered, users int64
	db.Model(&entity.Log{}).Where("module IN ? AND user_id <> 0", []string{"captcha", "sms"}).Count(&claimed)
	db.Model(&entity.Log{}).Where("module = ? AND action = ?", "auth", 12).Count(&registered)
	db.Model(&entity.User{}).Where("username = ?", "ordinary_alias_conflict").Count(&users)
	if claimed != 0 || registered != 0 || users != 0 {
		t.Fatalf("failed ordinary registration persisted side effects: claimed=%d registered=%d users=%d", claimed, registered, users)
	}
}

func TestOrdinaryRegistrationAtomicallyConsumesAndClaimsActualVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	captcha := entity.CaptchaToken{Token: "ordinary-success-captcha", Scene: service.CaptchaSceneRegisterSubmit, ChallengeID: "local:ordinary-success", Status: service.CaptchaTokenStatusActive, ExpiresAt: now.Add(time.Minute)}
	if err := db.Create(&captcha).Error; err != nil {
		t.Fatal(err)
	}
	code := entity.SMSVerificationCode{Phone: "+8613800000000", Scene: service.SMSSceneRegister, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: now.Add(time.Minute), CaptchaChallengeID: "remote:ordinary-success-before-sms"}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	logs := []entity.Log{{Module: "captcha", CorrelationID: captcha.ChallengeID}, {Module: "captcha", CorrelationID: code.CaptchaChallengeID}, {Module: "sms", CorrelationID: fmt.Sprintf("sms:%d", code.ID)}, {Module: "captcha", CorrelationID: "remote:ordinary-unused"}}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"username":"ordinary_atomic_user","password":"Abc12345!","phone":"13800000000","smsCode":"123456","captchaToken":"ordinary-success-captcha"}`
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/register", body, nil, UserRegister)
	if response.Code != http.StatusOK {
		t.Fatalf("ordinary registration should succeed: %d %s", response.Code, response.Body.String())
	}
	var user entity.User
	if err := db.Where("username = ?", "ordinary_atomic_user").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	var storedCode entity.SMSVerificationCode
	if err := db.First(&storedCode, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	var storedCaptcha entity.CaptchaToken
	if err := db.First(&storedCaptcha, captcha.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedCode.Status != service.SMSCodeStatusUsed || storedCode.ConsumedAt == nil || storedCaptcha.Status != service.CaptchaTokenStatusConsumed || storedCaptcha.ConsumedAt == nil {
		t.Fatalf("successful registration must consume both records: code=%+v captcha=%+v", storedCode, storedCaptcha)
	}
	for index, entry := range logs {
		var stored entity.Log
		if err := db.First(&stored, entry.ID).Error; err != nil {
			t.Fatal(err)
		}
		wantID := user.ID
		if index == 3 {
			wantID = 0
		}
		if stored.UserID != wantID || (wantID > 0 && stored.Username != user.Username) {
			t.Fatalf("wrong verification ownership: %+v", stored)
		}
	}
	var registered int64
	db.Model(&entity.Log{}).Where("module = ? AND action = ? AND user_id = ?", "auth", 12, user.ID).Count(&registered)
	if registered != 1 {
		t.Fatalf("expected one registration audit, got %d", registered)
	}
	repeated := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/register", body, nil, UserRegister)
	if repeated.Code == http.StatusOK {
		t.Fatalf("consumed verification must not register twice: %s", repeated.Body.String())
	}
}

func TestWechatRegisterHistoricalPhoneConflictRollsBackVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	legacyPhone := "+8613800000000"
	if err := db.Create(&entity.User{Username: "wechat_legacy_owner", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &legacyPhone}).Error; err != nil {
		t.Fatal(err)
	}
	store := service.NewWechatRegisterFlowStore()
	previousStore := wechatRegisterFlowStore
	wechatRegisterFlowStore = store
	t.Cleanup(func() { wechatRegisterFlowStore = previousStore })
	now := time.Now()
	flow, err := store.CreateOrResume("", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(flow.Token, "openid_phone_conflict", now); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.CaptchaToken{Token: "phone-conflict-captcha", Scene: service.CaptchaSceneRegisterSubmit, ChallengeID: "challenge", Status: service.CaptchaTokenStatusActive, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	code := entity.SMSVerificationCode{Phone: "13800000000", Scene: service.SMSSceneRegister, CodeHash: registerTestSMSCodeHash("123456"), Status: service.SMSCodeStatusPending, ExpiresAt: now.Add(time.Minute)}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"username":"wechat_alias_conflict","password":"Abc12345!","phone":"13800000000","smsCode":"123456","captchaToken":"phone-conflict-captcha","registrationFlowToken":%q}`, flow.Token)
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/register", body, nil, UserRegister)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "该手机号已被绑定") {
		t.Fatalf("wechat registration must reject historical alias: %d %s", response.Code, response.Body.String())
	}
	var storedCode entity.SMSVerificationCode
	if err := db.First(&storedCode, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedCode.Status != service.SMSCodeStatusPending {
		t.Fatalf("failed registration consumed SMS code: %s", storedCode.Status)
	}
	var captcha entity.CaptchaToken
	if err := db.Where("token = ?", "phone-conflict-captcha").First(&captcha).Error; err != nil {
		t.Fatal(err)
	}
	if captcha.Status != service.CaptchaTokenStatusActive {
		t.Fatalf("failed registration consumed captcha: %s", captcha.Status)
	}
}

func TestUpdateUserPreservesUnchangedLegacyPhoneAndBoundAt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	legacyPhone, boundAt := "+8613800000000", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	target := entity.User{Username: "unchanged-legacy-phone", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &legacyPhone, PhoneBoundAt: &boundAt}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	admin := &entity.User{ID: 500, Role: "admin", Status: "enabled"}
	response := phoneControllerRequest(t, http.MethodPut, fmt.Sprintf("/adapter-service/user/%d", target.ID), `{"nickname":"仅改资料","role":"user","status":"enabled","phone":"13800000000"}`, admin, UpdateUser)
	if response.Code != http.StatusOK {
		t.Fatalf("unchanged legacy binding should remain valid: %d %s", response.Code, response.Body.String())
	}
	var stored entity.User
	if err := db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Phone == nil || *stored.Phone != legacyPhone || stored.PhoneBoundAt == nil || !stored.PhoneBoundAt.Equal(boundAt) {
		t.Fatalf("unchanged legacy phone was rewritten: %+v", stored)
	}
}
