package controller

import (
	"bytes"
	"database/sql"
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
)

func newUserControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:user-controller-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Log{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	dao.SetDefault(db)
	return db
}

func TestUserLoginRejectsDisabledUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.User{
		Username:       "disabled",
		Password:       "123456",
		Status:         "disabled",
		Role:           "user",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/login", bytes.NewBufferString(`{"username":"disabled","password":"123456","captchaToken":"token-1"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	UserLogin(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("用户已被禁用")) {
		t.Fatalf("expected disabled user message, got %s", body)
	}
}

func TestUserLoginStoresLastLoginMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.User{
		ID:             66,
		Username:       "login-user",
		Password:       "123456",
		Status:         "enabled",
		Role:           "user",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/login", bytes.NewBufferString(`{"username":"login-user","password":"123456","captchaToken":"token-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.20.30.40:12345"
	request.Header.Set("X-Forwarded-For", "10.20.30.40")
	ctx.Request = request

	UserLogin(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var lastLoginAt sql.NullTime
	var lastLoginIP sql.NullString
	row := db.Raw(`SELECT last_login_at, last_login_ip FROM users WHERE id = ?`, 66).Row()
	if err := row.Scan(&lastLoginAt, &lastLoginIP); err != nil {
		t.Fatalf("scan login metadata: %v", err)
	}
	if !lastLoginAt.Valid {
		t.Fatal("expected last_login_at to be written on successful login")
	}
	if strings.TrimSpace(lastLoginIP.String) != "10.20.30.40" {
		t.Fatalf("expected last_login_ip to be 10.20.30.40, got %q", lastLoginIP.String)
	}
}

func TestAuthRejectsDisabledUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.User{
		ID:             88,
		Username:       "disabled-auth",
		Password:       "123456",
		Status:         "disabled",
		Role:           "user",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	token := util.GenerateJwtForUser(88, "123456")
	if token == "" {
		t.Fatal("expected jwt token")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, "/adapter-service/user", nil)
	request.Header.Set("Authorization", token)
	ctx.Request = request

	Auth(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", recorder.Code)
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("用户已被禁用")) {
		t.Fatalf("expected disabled auth message, got %s", body)
	}
}

func TestAuthRejectsTokenAfterPasswordChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.User{
		ID:             89,
		Username:       "password-version-user",
		Password:       "Old12345!",
		Status:         "enabled",
		Role:           "user",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	token := util.GenerateJwtForUser(89, "Old12345!")
	newPassword, err := util.HashPassword("New12345!")
	if err != nil {
		t.Fatalf("hash new password: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", 89).Update("password", newPassword).Error; err != nil {
		t.Fatalf("update password: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, "/adapter-service/user/info", nil)
	request.Header.Set("Authorization", token)
	ctx.Request = request

	Auth(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 after password change, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestUserRegisterRequiresRegisterEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"new-user","password":"123456","nickname":"新用户","captchaToken":"token-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	UserRegister(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("未开放注册")) {
		t.Fatalf("expected register disabled message, got %s", body)
	}
}

func TestUserRegisterRejectsWeakPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: "register_enabled", Value: "true"}).Error; err != nil {
		t.Fatalf("seed system setting: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"weak-user","password":"12345678","nickname":"弱密码","phone":"13800000000","smsCode":"123456","captchaToken":"token-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	UserRegister(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("密码")) {
		t.Fatalf("expected password complexity message, got %s", body)
	}

	count, err := dao.User.Where(dao.User.Username.Eq("weak-user")).Count()
	if err != nil {
		t.Fatalf("count weak user: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected weak-password registration to be rejected, got count %d", count)
	}
}

func TestUserRegisterCreatesPublicUserWithDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: "register_enabled", Value: "true"}).Error; err != nil {
		t.Fatalf("seed system setting: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() {
		consumeCaptchaTokenWithDB = originalConsume
	}()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	original := verifySMSCodeWithDB
	defer func() {
		verifySMSCodeWithDB = original
	}()
	verifySMSCodeWithDB = func(phone string, scene string, smsCode string) error {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"new-user","password":"Abc12345!","nickname":"新用户","phone":"13800000000","smsCode":"123456","captchaToken":"token-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	UserRegister(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response entity.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Role != "user" {
		t.Fatalf("expected role user, got %s", response.Role)
	}
	if response.Status != "enabled" {
		t.Fatalf("expected status enabled, got %s", response.Status)
	}
	if response.RegisterSource != "public" {
		t.Fatalf("expected register source public, got %s", response.RegisterSource)
	}
	if response.Phone == nil || *response.Phone != "13800000000" {
		t.Fatalf("expected response phone 13800000000, got %+v", response.Phone)
	}

	stored, err := dao.User.Where(dao.User.Username.Eq("new-user")).First()
	if err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if stored.Role != "user" || stored.Status != "enabled" || stored.RegisterSource != "public" {
		t.Fatalf("unexpected stored user: %+v", stored)
	}
	if stored.Phone == nil || *stored.Phone != "13800000000" || stored.PhoneBoundAt == nil {
		t.Fatalf("expected stored phone binding, got %+v", stored)
	}
}

func TestUserLoginRequiresCaptchaToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/user/login", bytes.NewBufferString(`{"username":"admin","password":"123456"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	UserLogin(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without captcha token, got %d", recorder.Code)
	}
}

func TestSendPublicSMSCodeRequiresCaptchaToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/sms-code", bytes.NewBufferString(`{"phone":"13800000000","scene":"login"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	SendPublicSMSCode(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without captcha token, got %d", recorder.Code)
	}
}

func TestUpdateUserUpdatesOwnedUserFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	parent := &entity.User{ID: 10, Username: "owner", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	child := &entity.User{ID: 11, Username: "child", Password: "123456", ParentID: 10, Nickname: "旧昵称", Role: "user", Status: "enabled", Balance: 12.5, RegisterSource: "admin"}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"nickname":"新昵称","role":"admin","status":"disabled","balance":30.25,"balanceMode":"override"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/11", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "11"}}
	ctx.Set("user", parent)

	UpdateUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(11)).First()
	if err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if stored.Nickname != "新昵称" {
		t.Fatalf("expected nickname updated, got %s", stored.Nickname)
	}
	if stored.Role != "admin" {
		t.Fatalf("expected role admin, got %s", stored.Role)
	}
	if stored.Status != "disabled" {
		t.Fatalf("expected status disabled, got %s", stored.Status)
	}
	if stored.Balance != 30.25 {
		t.Fatalf("expected balance override to 30.25, got %v", stored.Balance)
	}
	if stored.Username != "child" {
		t.Fatalf("expected username unchanged, got %s", stored.Username)
	}
}

func TestUpdateUserAddsBalanceAmount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 70, Username: "admin-add", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 71, Username: "target-add", Password: "123456", ParentID: 70, Role: "user", Status: "enabled", Balance: 10.5, RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"nickname":"","role":"user","status":"enabled","balance":2.75,"balanceMode":"increase"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/71", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "71"}}
	ctx.Set("user", admin)

	UpdateUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(71)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if stored.Balance != 13.25 {
		t.Fatalf("expected balance to increase to 13.25, got %v", stored.Balance)
	}
}

func TestUpdateUserReducesBalanceAmount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 72, Username: "admin-reduce", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 73, Username: "target-reduce", Password: "123456", ParentID: 72, Role: "user", Status: "enabled", Balance: 10.5, RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"nickname":"","role":"user","status":"enabled","balance":3.25,"balanceMode":"decrease"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/73", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "73"}}
	ctx.Set("user", admin)

	UpdateUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(73)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if stored.Balance != 7.25 {
		t.Fatalf("expected balance to decrease to 7.25, got %v", stored.Balance)
	}
}

func TestUpdateUserRejectsUnknownBalanceMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 74, Username: "admin-invalid-mode", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 75, Username: "target-invalid-mode", Password: "123456", ParentID: 74, Role: "user", Status: "enabled", Balance: 10.5, RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"nickname":"","role":"user","status":"enabled","balance":3.25,"balanceMode":"multiply"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/75", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "75"}}
	ctx.Set("user", admin)

	UpdateUser(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("余额调整模式")) {
		t.Fatalf("expected invalid balance mode message, got %s", body)
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(75)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if stored.Balance != 10.5 {
		t.Fatalf("expected balance unchanged on invalid mode, got %v", stored.Balance)
	}
}

func TestUserListAdminSeesAllUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 30, Username: "admin", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	userA := &entity.User{ID: 31, Username: "user-a", Password: "123456", ParentID: 30, Role: "user", Status: "enabled", RegisterSource: "admin"}
	userB := &entity.User{ID: 32, Username: "user-b", Password: "123456", ParentID: 999, Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(userA).Error; err != nil {
		t.Fatalf("create userA: %v", err)
	}
	if err := db.Create(userB).Error; err != nil {
		t.Fatalf("create userB: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", admin)

	UserList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var users []entity.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected admin to see all 3 users, got %d", len(users))
	}
}

func TestUserListIncludesCreatedAndLastLoginMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Exec(`INSERT INTO users (id, username, password, role, status, register_source, created_at, last_login_at, last_login_ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		90, "listed-user", "123456", "user", "enabled", "admin", time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC), time.Date(2026, 5, 6, 9, 30, 0, 0, time.UTC), "127.0.0.9").Error; err != nil {
		t.Fatalf("insert listed user: %v", err)
	}
	admin := &entity.User{ID: 91, Username: "admin-user", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", admin)

	UserList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var users []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode user list: %v", err)
	}

	for _, item := range users {
		if item["username"] != "listed-user" {
			continue
		}
		if _, ok := item["createdAt"]; !ok {
			t.Fatal("expected createdAt field in user list response")
		}
		if _, ok := item["lastLoginAt"]; !ok {
			t.Fatal("expected lastLoginAt field in user list response")
		}
		if item["lastLoginIp"] != "127.0.0.9" {
			t.Fatalf("expected lastLoginIp 127.0.0.9, got %#v", item["lastLoginIp"])
		}
		return
	}

	t.Fatal("expected listed-user to appear in response")
}

func TestUserListRejectsNonAdminOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	operator := &entity.User{ID: 35, Username: "normal-user", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	child := &entity.User{ID: 36, Username: "child", Password: "123456", ParentID: 35, Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(operator).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", operator)

	UserList(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("无权限查看用户")) {
		t.Fatalf("expected forbidden list message, got %s", body)
	}
}

func TestCreateUserRejectsNonAdminOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	operator := &entity.User{ID: 40, Username: "normal-user", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(operator).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"created-by-user","password":"123456","nickname":"test"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", operator)

	CreateUser(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("无权限创建用户")) {
		t.Fatalf("expected forbidden create message, got %s", body)
	}

	count, err := dao.User.Where(dao.User.Username.Eq("created-by-user")).Count()
	if err != nil {
		t.Fatalf("count created user: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no user created, got %d", count)
	}
}

func TestCreateUserRejectsWeakPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 41, Username: "admin-user", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"weak-created-user","password":"12345678","nickname":"test"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", admin)

	CreateUser(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("密码")) {
		t.Fatalf("expected password complexity message, got %s", body)
	}

	count, err := dao.User.Where(dao.User.Username.Eq("weak-created-user")).Count()
	if err != nil {
		t.Fatalf("count weak created user: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected weak-password user creation to be rejected, got count %d", count)
	}
}

func TestCreateUserAcceptsStrongPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 42, Username: "admin-create", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"created-strong-user","password":"Abc12345!","nickname":"test","role":"user","status":"enabled"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", admin)

	CreateUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := dao.User.Where(dao.User.Username.Eq("created-strong-user")).First()
	if err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if stored.ParentID != admin.ID {
		t.Fatalf("expected parent id %d, got %d", admin.ID, stored.ParentID)
	}
	if stored.Role != "user" || stored.Status != "enabled" {
		t.Fatalf("unexpected stored user: %+v", stored)
	}
}

func TestUpdateUserAdminCanManageAnyUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 45, Username: "admin", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 46, Username: "foreign-user", Password: "123456", ParentID: 999, Nickname: "旧昵称", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"nickname":"管理员修改","role":"admin","status":"disabled"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/46", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "46"}}
	ctx.Set("user", admin)

	UpdateUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(46)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if stored.Nickname != "管理员修改" || stored.Role != "admin" || stored.Status != "disabled" {
		t.Fatalf("expected target updated by admin, got %+v", stored)
	}
}

func TestResetUserPasswordRejectsNonAdminOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	operator := &entity.User{ID: 60, Username: "normal-user", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	target := &entity.User{ID: 61, Username: "target-user", Password: "Old12345!", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(operator).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"password":"New12345!"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/61/reset-password", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "61"}}
	ctx.Set("user", operator)

	ResetUserPassword(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("无权限重置密码")) {
		t.Fatalf("expected forbidden reset message, got %s", body)
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(61)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if stored.Password != "Old12345!" {
		t.Fatalf("expected password unchanged, got %s", stored.Password)
	}
}

func TestResetUserPasswordRejectsWeakPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 62, Username: "admin-reset", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 63, Username: "target-user", Password: "Old12345!", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"password":"12345678"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/63/reset-password", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "63"}}
	ctx.Set("user", admin)

	ResetUserPassword(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("密码")) {
		t.Fatalf("expected password complexity message, got %s", body)
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(63)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if stored.Password != "Old12345!" {
		t.Fatalf("expected password unchanged, got %s", stored.Password)
	}
}

func TestResetUserPasswordAdminCanResetPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 64, Username: "admin-reset-ok", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 65, Username: "target-user", Password: "Old12345!", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"password":"New12345!"}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/65/reset-password", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "65"}}
	ctx.Set("user", admin)

	ResetUserPassword(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := dao.User.Where(dao.User.ID.Eq(65)).First()
	if err != nil {
		t.Fatalf("load target user: %v", err)
	}
	if matched, _ := util.VerifyPassword(stored.Password, "New12345!"); !matched {
		t.Fatalf("expected password updated to a valid hash, got %s", stored.Password)
	}
}

func TestDeleteUserRejectsNonAdminOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	operator := &entity.User{ID: 50, Username: "normal-user", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	target := &entity.User{ID: 51, Username: "managed-user", Password: "123456", ParentID: 50, Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(operator).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodDelete, "/adapter-service/user/51", nil)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "51"}}
	ctx.Set("user", operator)

	DeleteUser(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("无权限删除用户")) {
		t.Fatalf("expected forbidden delete message, got %s", body)
	}

	count, err := dao.User.Where(dao.User.ID.Eq(51)).Count()
	if err != nil {
		t.Fatalf("count target user: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected target user to remain, got count %d", count)
	}
}

func TestDeleteUserAdminCanDeleteAnyUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 55, Username: "admin", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	target := &entity.User{ID: 56, Username: "foreign-user", Password: "123456", ParentID: 999, Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodDelete, "/adapter-service/user/56", nil)
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "56"}}
	ctx.Set("user", admin)

	DeleteUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	count, err := dao.User.Where(dao.User.ID.Eq(56)).Count()
	if err != nil {
		t.Fatalf("count target user: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected target user deleted, got count %d", count)
	}
}

func TestSendPublicSMSCodeDelegatesToService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	original := sendPublicSMSCodeWithDB
	defer func() {
		sendPublicSMSCodeWithDB = original
	}()

	var captured service.SendSMSCodeInput
	sendPublicSMSCodeWithDB = func(input service.SendSMSCodeInput) error {
		captured = input
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/public/sms-code", bytes.NewBufferString(`{"phone":"13800000000","scene":"login","captchaToken":"token-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.20.30.40:12345"
	request.Header.Set("X-Forwarded-For", "10.20.30.40")
	ctx.Request = request

	SendPublicSMSCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if captured.Phone != "13800000000" || captured.Scene != service.SMSSceneLogin || captured.RequestIP != "10.20.30.40" {
		t.Fatalf("unexpected captured input: %+v", captured)
	}
}

func TestUserSMSLoginSucceedsAndStoresLoginMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	phone := "13800000000"
	if err := db.Create(&entity.User{ID: 101, Username: "sms-user", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &phone}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	original := verifySMSCodeWithDB
	defer func() {
		verifySMSCodeWithDB = original
	}()
	received := ""
	verifySMSCodeWithDB = func(phone string, scene string, smsCode string) error {
		received = phone + ":" + scene + ":" + smsCode
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/login/sms", bytes.NewBufferString(`{"phone":"13800000000","smsCode":"123456","captchaToken":"token-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.0.0.1:12345"
	request.Header.Set("X-Forwarded-For", "10.0.0.1")
	ctx.Request = request

	UserSMSLogin(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if received != "13800000000:login:123456" {
		t.Fatalf("unexpected verify input %s", received)
	}

	var lastLoginAt sql.NullTime
	var lastLoginIP sql.NullString
	row := db.Raw(`SELECT last_login_at, last_login_ip FROM users WHERE id = ?`, 101).Row()
	if err := row.Scan(&lastLoginAt, &lastLoginIP); err != nil {
		t.Fatalf("scan login metadata: %v", err)
	}
	if !lastLoginAt.Valid || strings.TrimSpace(lastLoginIP.String) != "10.0.0.1" {
		t.Fatalf("unexpected login metadata: valid=%v ip=%q", lastLoginAt.Valid, lastLoginIP.String)
	}
}

func TestUserRegisterRequiresPhoneAndSMSCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("seed system setting: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"new-user","password":"Abc12345!","nickname":"新用户"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	UserRegister(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestUserRegisterCreatesPublicUserWithBoundPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: service.RegisterEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("seed system setting: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	original := verifySMSCodeWithDB
	defer func() {
		verifySMSCodeWithDB = original
	}()
	received := ""
	verifySMSCodeWithDB = func(phone string, scene string, smsCode string) error {
		received = phone + ":" + scene + ":" + smsCode
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"phone-user","password":"Abc12345!","nickname":"新用户","phone":"13800000000","smsCode":"123456","captchaToken":"token-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/register", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	UserRegister(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if received != "13800000000:register:123456" {
		t.Fatalf("unexpected verify input %s", received)
	}

	stored, err := dao.User.Where(dao.User.Username.Eq("phone-user")).First()
	if err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if stored.Phone == nil || *stored.Phone != "13800000000" {
		t.Fatalf("expected stored phone 13800000000, got %+v", stored.Phone)
	}
	if stored.PhoneBoundAt == nil {
		t.Fatal("expected phone bound time to be written")
	}
}

func TestUpdateUserForceReassignsPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := &entity.User{ID: 201, Username: "admin-phone", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	ownerPhone := "13800000000"
	owner := &entity.User{ID: 202, Username: "owner-phone", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &ownerPhone}
	target := &entity.User{ID: 203, Username: "target-phone", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"nickname":"目标用户","role":"user","status":"enabled","phone":"13800000000","forceReassignPhone":true}`)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/user/203", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Params = gin.Params{{Key: "id", Value: "203"}}
	ctx.Set("user", admin)

	UpdateUser(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	storedOwner, err := dao.User.Where(dao.User.ID.Eq(202)).First()
	if err != nil {
		t.Fatalf("load owner: %v", err)
	}
	storedTarget, err := dao.User.Where(dao.User.ID.Eq(203)).First()
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	if storedOwner.Phone != nil {
		t.Fatalf("expected owner phone cleared, got %+v", storedOwner.Phone)
	}
	if storedTarget.Phone == nil || *storedTarget.Phone != "13800000000" {
		t.Fatalf("expected target phone reassigned, got %+v", storedTarget.Phone)
	}
}

func TestSendUserPhoneChangeSMSCodeUsesAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	originalConsume := consumeCaptchaTokenWithDB
	originalSend := sendUserPhoneChangeSMSCodeWithDB
	defer func() {
		consumeCaptchaTokenWithDB = originalConsume
		sendUserPhoneChangeSMSCodeWithDB = originalSend
	}()

	var sent service.SendSMSCodeInput
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		if token != "captcha-token" || scene != service.CaptchaSceneSMSSendChangePhone {
			t.Fatalf("unexpected captcha consume token=%s scene=%s", token, scene)
		}
		return nil
	}
	sendUserPhoneChangeSMSCodeWithDB = func(input service.SendSMSCodeInput) error {
		sent = input
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 301, Username: "profile-user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/user/phone/sms-code", bytes.NewBufferString(`{"phone":"13900000000","captchaToken":"captcha-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	SendUserPhoneChangeSMSCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if sent.Phone != "13900000000" || sent.Scene != service.SMSSceneChangePhone || sent.TargetUserID != 301 {
		t.Fatalf("unexpected sms input %+v", sent)
	}
}

func TestUpdateCurrentUserPhoneUsesAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	original := updateUserPhoneWithVerificationWithDB
	defer func() { updateUserPhoneWithVerificationWithDB = original }()
	var receivedUserID int32
	var receivedPhone string
	var receivedCode string
	updateUserPhoneWithVerificationWithDB = func(userID int32, phone string, smsCode string) (entity.User, error) {
		receivedUserID = userID
		receivedPhone = phone
		receivedCode = smsCode
		return entity.User{ID: userID, Username: "profile-user", Phone: &phone}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 302, Username: "profile-user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/user/phone", bytes.NewBufferString(`{"phone":"13900000000","smsCode":"123456"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateCurrentUserPhone(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if receivedUserID != 302 || receivedPhone != "13900000000" || receivedCode != "123456" {
		t.Fatalf("unexpected update input user=%d phone=%s code=%s", receivedUserID, receivedPhone, receivedCode)
	}
}

func TestBindAndUnbindCurrentUserWechatUseAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newUserControllerTestDB(t)

	originalBind := bindCurrentUserWechatWithDB
	originalUnbind := unbindCurrentUserWechatWithDB
	defer func() {
		bindCurrentUserWechatWithDB = originalBind
		unbindCurrentUserWechatWithDB = originalUnbind
	}()

	var boundUserID int32
	var bindToken string
	bindCurrentUserWechatWithDB = func(token string, userID int32) (entity.User, error) {
		bindToken = token
		boundUserID = userID
		wechatID := "openid-profile"
		return entity.User{ID: userID, Username: "profile-user", WechatID: &wechatID}, nil
	}
	var unboundUserID int32
	unbindCurrentUserWechatWithDB = func(userID int32) (entity.User, error) {
		unboundUserID = userID
		return entity.User{ID: userID, Username: "profile-user"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 303, Username: "profile-user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/user/wechat/bind", bytes.NewBufferString(`{"bindToken":"bind-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	BindCurrentUserWechat(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected bind status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if boundUserID != 303 || bindToken != "bind-token" {
		t.Fatalf("unexpected bind input user=%d token=%s", boundUserID, bindToken)
	}

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 303, Username: "profile-user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/adapter-service/user/wechat", nil)

	UnbindCurrentUserWechat(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected unbind status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if unboundUserID != 303 {
		t.Fatalf("unexpected unbind user %d", unboundUserID)
	}
}

func TestBindCurrentUserWechatByCodeUsesAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalSettings := loadWechatLoginSettings
	originalBind := bindCurrentUserWechatByCodeWithDB
	defer func() {
		loadWechatLoginSettings = originalSettings
		bindCurrentUserWechatByCodeWithDB = originalBind
	}()

	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatCodeLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	var receivedInput service.WechatCodeBindInput
	bindCurrentUserWechatByCodeWithDB = func(input service.WechatCodeBindInput) (entity.User, error) {
		receivedInput = input
		wechatID := "openid-profile-code"
		return entity.User{ID: input.UserID, Username: "profile-user", WechatID: &wechatID}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 304, Username: "profile-user", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/user/wechat/bind-code", bytes.NewBufferString(`{"code":"104016"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	BindCurrentUserWechatByCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected bind status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if receivedInput.UserID != 304 || receivedInput.Code != "104016" {
		t.Fatalf("unexpected bind input user=%d code=%s", receivedInput.UserID, receivedInput.Code)
	}
	if receivedInput.Client.BaseURL != "https://wechat.example.com" || receivedInput.Client.Token != "token" {
		t.Fatalf("unexpected client: %+v", receivedInput.Client)
	}
}
