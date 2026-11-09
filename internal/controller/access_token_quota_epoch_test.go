package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// accessTokenQuotaEpochRouter 组装与 router.go 完全一致的中间件顺序：
// POST /access-tokens/:id/reset-quota-window 挂 controller.Auth + middleware.RequireAdmin。
func accessTokenQuotaEpochRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.POST("/access-tokens/:id/reset-quota-window", auth, middleware.RequireAdmin, ResetAccessTokenQuotaWindow)
	return router
}

func postResetQuotaWindow(router *gin.Engine, tokenID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/access-tokens/"+tokenID+"/reset-quota-window", nil)
	request.RemoteAddr = "192.0.2.30:12345"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestResetAccessTokenQuotaWindowRejectsNonAdminWith403 普通账号调该接口 → 403。
//
// 断言的是**路由层**的鉴权：中间件链里缺了 middleware.RequireAdmin 这条就会变成 200，
// 所以这里不替换服务取库点 —— 请求根本不该走到控制器。
func TestResetAccessTokenQuotaWindowRejectsNonAdminWith403(t *testing.T) {
	gin.SetMode(gin.TestMode)

	called := false
	original := resetAccessTokenQuotaWindow
	defer func() { resetAccessTokenQuotaWindow = original }()
	resetAccessTokenQuotaWindow = func(entity.User, int32, string) (service.ResetAccessTokenQuotaWindowResult, error) {
		called = true
		return service.ResetAccessTokenQuotaWindowResult{}, nil
	}

	for _, role := range []string{"user", ""} {
		actor := &entity.User{ID: 7, Username: "plain-user", Role: role, Status: "enabled"}
		recorder := postResetQuotaWindow(accessTokenQuotaEpochRouter(actor), "3")
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("role %q must get 403, got %d with body %s", role, recorder.Code, recorder.Body.String())
		}
	}
	if called {
		t.Fatal("non-admin request must be rejected before reaching the controller")
	}
}

// TestResetAccessTokenQuotaWindowReturnsWindowAndHistory 管理员调用 → 200，
// 响应里同时给出「本轮已用 / 历史累计 / 余额 / 限额」四个数字。
func TestResetAccessTokenQuotaWindowReturnsWindowAndHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := resetAccessTokenQuotaWindow
	defer func() { resetAccessTokenQuotaWindow = original }()
	resetAccessTokenQuotaWindow = func(user entity.User, tokenID int32, clientIP string) (service.ResetAccessTokenQuotaWindowResult, error) {
		if user.ID != 1 || user.Role != "admin" || tokenID != 3 || clientIP != "192.0.2.30" {
			t.Fatalf("unexpected seam input: user=%d role=%q token=%d ip=%q", user.ID, user.Role, tokenID, clientIP)
		}
		return service.ResetAccessTokenQuotaWindowResult{
			AccessToken: entity.AccessToken{ID: tokenID, TotalQuotaLimit: 20, TotalAmountUsed: 12},
			QuotaWindow: service.AccessTokenQuotaWindow{Epoch: 1, BaselineAmount: 12, WindowUsed: 0},
			Balance:     8, TotalQuotaLimit: 20, HistoryAmountUsed: 12, EventID: "quota-epoch-fixture",
		}, nil
	}

	actor := &entity.User{ID: 1, Username: "admin", Role: "admin", Status: "enabled"}
	recorder := postResetQuotaWindow(accessTokenQuotaEpochRouter(actor), "3")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response service.ResetAccessTokenQuotaWindowResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.QuotaWindow.WindowUsed != 0 || response.HistoryAmountUsed != 12 ||
		response.Balance != 8 || response.TotalQuotaLimit != 20 || response.EventID != "quota-epoch-fixture" {
		t.Fatalf("acceptance numbers mismatch: %+v", response)
	}
}

// TestResetAccessTokenQuotaWindowLeavesQuotaAdmissionUnchanged 行为变化为零的回归钉子。
//
// **这是纯行为级断言**（captain 要求）：不检查源码里有没有出现 baseline_amount / epoch
// 之类的字面量 —— 那种源码级断言会在别人正当重构时误报。这里只做一件事：
// 拿真实数据库跑一次真实重置，把重置**前**与重置**后**（从库里重新读回的令牌行）
// 分别喂给真实的准入函数 accessTokenQuotaExhausted / accessTokenQuotaWouldExceed，
// 断言两边的判定结果完全相同。
//
// 因此这条测试只在**行为真的变了**时才红：如果有人把「本轮已用」
// （total_amount_used - baseline_amount）接进准入判定，那么重置后读回的行
// 会让 12/20 从「未超额」变成别的结果，断言立刻失败。
// 那时改动者必须显式决策「重置是否应该放宽硬限额」，而不是顺手改掉。
func TestResetAccessTokenQuotaWindowLeavesQuotaAdmissionUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newQuotaEpochControllerTestDB(t)
	admin := seedQuotaEpochControllerAdmin(t, db)
	token := seedQuotaEpochControllerToken(t, db, 8, 20, 12)

	// 重置前的真实准入判定（令牌行来自数据库，不是手搓的 struct）。
	before := loadQuotaEpochControllerToken(t, db, token.ID)
	exhaustedBefore := accessTokenQuotaExhausted(before)
	wouldExceedBefore := accessTokenQuotaWouldExceed(before, 5)
	if exhaustedBefore {
		t.Fatal("12/20 must not be exhausted; this fixture is the section-4 acceptance sample")
	}
	if wouldExceedBefore {
		t.Fatal("12+5=17 must stay within the 20 limit")
	}

	// 真实重置：追加 epoch 叠加层，不动 total_amount_used、不动钱包。
	if _, err := service.ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.31"); err != nil {
		t.Fatalf("reset quota window: %v", err)
	}

	// 重置后从库里重新读回令牌行，再跑同一套准入判定。
	after := loadQuotaEpochControllerToken(t, db, token.ID)
	if after.TotalAmountUsed != 12 {
		t.Fatalf("history must survive the reset: %v want 12", after.TotalAmountUsed)
	}
	if accessTokenQuotaExhausted(after) != exhaustedBefore {
		t.Fatalf("reset must not change quota exhaustion: before=%v after=%v",
			exhaustedBefore, accessTokenQuotaExhausted(after))
	}
	if accessTokenQuotaWouldExceed(after, 5) != wouldExceedBefore {
		t.Fatalf("reset must not change would-exceed: before=%v after=%v",
			wouldExceedBefore, accessTokenQuotaWouldExceed(after, 5))
	}

	// 边界仍然按历史累计判：历史累计打满时，重置也不会让准入放行。
	full := seedQuotaEpochControllerTokenNamed(t, db, "quota-epoch-full-token", 20, 20)
	if _, err := service.ResetAccessTokenQuotaWindowWithDB(db, admin, full.ID, "192.0.2.31"); err != nil {
		t.Fatalf("reset exhausted token: %v", err)
	}
	fullAfter := loadQuotaEpochControllerToken(t, db, full.ID)
	if fullAfter.TotalAmountUsed != 20 {
		t.Fatalf("history at the limit must survive the reset: %v want 20", fullAfter.TotalAmountUsed)
	}
	if !accessTokenQuotaExhausted(fullAfter) {
		t.Fatal("history at the limit must still be exhausted after the overlay change")
	}
	if !accessTokenQuotaWouldExceed(fullAfter, 0.000001) {
		t.Fatal("any further charge must still be rejected at the limit")
	}
}

// newQuotaEpochControllerTestDB 建一个带 quota_epochs 的内存库（库名带 t.Name() 隔离）。
//
// DSN 并发参数走 pkg/util.SQLiteConcurrentDSN，与生产共用同一份定义 —— 不手写字符串，
// 避免「生产改了测试没跟着改」的漂移（见 task_doc/47 号文档）。
func newQuotaEpochControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := util.SQLiteConcurrentDSN(
		"file:quota-epoch-ctrl-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.Log{}, &entity.AccessTokenQuotaEpoch{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func seedQuotaEpochControllerAdmin(t *testing.T, db *gorm.DB) entity.User {
	t.Helper()

	admin := entity.User{
		ID: 1, Username: "admin-quota-epoch-ctrl", Password: "123456", Role: "admin",
		Status: "enabled", RegisterSource: "admin",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return admin
}

func seedQuotaEpochControllerToken(t *testing.T, db *gorm.DB, balance, limit, used float64) entity.AccessToken {
	t.Helper()

	return seedQuotaEpochControllerTokenNamed(t, db, "quota-epoch-ctrl-token", limit, used)
}

func seedQuotaEpochControllerTokenNamed(t *testing.T, db *gorm.DB, name string, limit, used float64) entity.AccessToken {
	t.Helper()

	owner := entity.User{
		ID: 2, Username: "user-quota-epoch-ctrl", Password: "123456", Role: "user",
		Status: "enabled", RegisterSource: "admin", Balance: 8,
	}
	if err := db.Where("id = ?", owner.ID).First(&entity.User{}).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		if err := db.Create(&owner).Error; err != nil {
			t.Fatalf("create owner: %v", err)
		}
	}
	token := entity.AccessToken{
		UserID: owner.ID, Name: name,
		TokenHash:   quotaEpochControllerHash(name),
		TokenPrefix: "tk_synthetic", TokenValue: "tk_synthetic_" + name,
		Status: "enabled", TotalQuotaLimit: limit, TotalAmountUsed: used,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}
	return token
}

func quotaEpochControllerHash(name string) string {
	sum := sha256.Sum256([]byte("tk_synthetic_" + name))
	return hex.EncodeToString(sum[:])
}

func loadQuotaEpochControllerToken(t *testing.T, db *gorm.DB, id int32) entity.AccessToken {
	t.Helper()

	var token entity.AccessToken
	if err := db.First(&token, id).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	return token
}

// TestResetAccessTokenQuotaWindowMapsConflictsTo409 并发/空窗口冲突返回 409，不是 500。
func TestResetAccessTokenQuotaWindowMapsConflictsTo409(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := resetAccessTokenQuotaWindow
	defer func() { resetAccessTokenQuotaWindow = original }()

	for _, target := range []error{
		service.ErrAccessTokenQuotaEpochConflict,
		service.ErrAccessTokenQuotaWindowEmpty,
	} {
		resetAccessTokenQuotaWindow = func(entity.User, int32, string) (service.ResetAccessTokenQuotaWindowResult, error) {
			return service.ResetAccessTokenQuotaWindowResult{}, target
		}
		actor := &entity.User{ID: 1, Username: "admin", Role: "admin", Status: "enabled"}
		recorder := postResetQuotaWindow(accessTokenQuotaEpochRouter(actor), "3")
		if recorder.Code != http.StatusConflict {
			t.Fatalf("%v must map to 409, got %d with body %s", target, recorder.Code, recorder.Body.String())
		}
	}
}
