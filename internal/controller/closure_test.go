package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 注销申请三条接口 + 状态归一化的行为级回归（task_doc/76 §3.3）。
//
// 最关键的一条是 TestClosurePendingStatusSurvivesAdminProfileUpdate：
// normalizeUserStatus 原本只认 enabled / disabled，其它值一律落回 enabled。
// 不变它的话，closure_pending 会在「管理员改资料」这条路径上被静默改回 enabled
// （用户看到的注销申请实际没生效）。这条用例就是那把尺子 ——
// 把 normalizeUserStatus 改回只认两个值，它必须变红。

func seedClosureUser(t *testing.T, db *gorm.DB, id int32, username string, phone *string, balance, gift float64) *entity.User {
	t.Helper()
	user := &entity.User{
		ID: id, Username: username, Password: "synthetic-closure-pw",
		Nickname: username + "-nick", Role: "user", Status: "enabled",
		RegisterSource: "public", Balance: balance, GiftBalance: gift, Phone: phone,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create closure user %s: %v", username, err)
	}
	return user
}

func phonePtr(value string) *string { return &value }

func closureUserStatus(t *testing.T, db *gorm.DB, id int32) string {
	t.Helper()
	stored, err := dao.User.Where(dao.User.ID.Eq(id)).First()
	if err != nil {
		t.Fatalf("load user %d: %v", id, err)
	}
	return stored.Status
}

// TestClosureRequestMarksPendingAndWritesAccountLog 申请注销必须真的落库成
// closure_pending，并写两条日志通道（Action 51，UserID = 本人）。
func TestClosureRequestMarksPendingAndWritesAccountLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	user := seedClosureUser(t, db, 800, "closure-requester", phonePtr("13900000001"), 3.5, 1.5)

	recorder := doArchiveRequest(t, ClosureRequest, http.MethodPost, "/adapter-service/user/closure-request",
		[]byte(`{"password":"synthetic-closure-pw","acknowledge":true}`), user, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if status := closureUserStatus(t, db, 800); status != userStatusClosurePending {
		t.Fatalf("status must be %q after request, got %q", userStatusClosurePending, status)
	}
	payload := decodeJSONBody(t, recorder)
	data, _ := payload["data"].(map[string]any)
	if data["pending"] != true {
		t.Fatalf("response must report pending=true, got %v", payload["data"])
	}
	if waitDays, _ := data["waitDays"].(float64); int(waitDays) != closureWaitingDays {
		t.Fatalf("expected waitDays=%d, got %v", closureWaitingDays, data["waitDays"])
	}
	// 余额 3.5 + 1.5 = 5 ≤ 10 ⇒ 不需要人工工单。
	if data["requiresTicket"] != false {
		t.Fatalf("balance under threshold must not require a ticket, got %v", data["requiresTicket"])
	}

	rows := accountLogRows(t, db, AccountActionClosure)
	if len(rows) != 2 {
		t.Fatalf("expected 2 log rows (logs + audit) for closure request, got %d", len(rows))
	}
	for _, row := range rows {
		if row.UserID != user.ID {
			t.Fatalf("self-service closure must record the user themselves, got user_id=%d", row.UserID)
		}
		if strings.Contains(row.OldAnswer, "synthetic-closure-pw") || strings.Contains(row.NewAnswer, "synthetic-closure-pw") {
			t.Fatal("log snapshot must not carry the password")
		}
	}
}

// TestClosureRequestRejectsWrongPasswordAndLeavesStatusUntouched 密码错必须拒绝，
// 且状态一点都不能动（否则等于谁都能替别人注销）。
func TestClosureRequestRejectsWrongPasswordAndLeavesStatusUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	user := seedClosureUser(t, db, 801, "closure-wrongpw", phonePtr("13900000002"), 0, 0)

	recorder := doArchiveRequest(t, ClosureRequest, http.MethodPost, "/adapter-service/user/closure-request",
		[]byte(`{"password":"not-the-password","acknowledge":true}`), user, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong password, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if status := closureUserStatus(t, db, 801); status != "enabled" {
		t.Fatalf("wrong password must not change the status, got %q", status)
	}
	if len(accountLogRows(t, db, AccountActionClosure)) != 0 {
		t.Fatal("rejected request must not write a closure log")
	}

	// 未勾选知情确认也必须拒绝。
	recorder = doArchiveRequest(t, ClosureRequest, http.MethodPost, "/adapter-service/user/closure-request",
		[]byte(`{"password":"synthetic-closure-pw"}`), user, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without acknowledgement, got %d", recorder.Code)
	}
	if status := closureUserStatus(t, db, 801); status != "enabled" {
		t.Fatalf("unacknowledged request must not change the status, got %q", status)
	}
}

// TestClosureRequestRejectsAccountWithoutPhone 无手机号账号不允许自助注销
// （45 号 Q1 / 76 §3.3.4）：不是隐藏按钮，而是后端也要拦。
func TestClosureRequestRejectsAccountWithoutPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	user := seedClosureUser(t, db, 802, "closure-nophone", nil, 0, 0)

	recorder := doArchiveRequest(t, ClosureRequest, http.MethodPost, "/adapter-service/user/closure-request",
		[]byte(`{"password":"synthetic-closure-pw","acknowledge":true}`), user, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without phone, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "手机号") {
		t.Fatalf("rejection must explain the phone requirement, got %s", recorder.Body.String())
	}
	if status := closureUserStatus(t, db, 802); status != "enabled" {
		t.Fatalf("status must stay enabled, got %q", status)
	}
}

// TestClosureRequestCreatesTicketAboveThreshold 45 号 Q8：余额 > 10 元时
// 自动建工单（冻结由 closure_pending 本身承担）。
func TestClosureRequestCreatesTicketAboveThreshold(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	user := seedClosureUser(t, db, 803, "closure-rich", phonePtr("13900000003"), 80, 5.5)

	recorder := doArchiveRequest(t, ClosureRequest, http.MethodPost, "/adapter-service/user/closure-request",
		[]byte(`{"password":"synthetic-closure-pw","acknowledge":true}`), user, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeJSONBody(t, recorder)
	data, _ := payload["data"].(map[string]any)
	if data["requiresTicket"] != true {
		t.Fatalf("balance above threshold must require a ticket, got %v", data["requiresTicket"])
	}
	if data["ticketId"] == nil {
		t.Fatalf("a ticket must be created for an over-threshold balance, got %s", mustJSON(t, payload))
	}
	var ticketCount int64
	if err := db.Model(&entity.Ticket{}).Where("user_id = ?", 803).Count(&ticketCount).Error; err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if ticketCount != 1 {
		t.Fatalf("expected exactly 1 auto-created ticket, got %d", ticketCount)
	}
	var ticket entity.Ticket
	if err := db.Where("user_id = ?", 803).Take(&ticket).Error; err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if !strings.Contains(ticket.Subject, "注销") {
		t.Fatalf("auto ticket subject must explain the closure, got %q", ticket.Subject)
	}
}

// TestClosureCancelRestoresEnabledAndWritesLog 撤销必须能回到 enabled 并留痕（Action 52）。
func TestClosureCancelRestoresEnabledAndWritesLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	user := seedClosureUser(t, db, 804, "closure-cancel", phonePtr("13900000004"), 0, 0)
	if err := db.Model(&entity.User{}).Where("id = ?", 804).
		Update("status", userStatusClosurePending).Error; err != nil {
		t.Fatalf("pin pending status: %v", err)
	}
	// Auth 中间件每次请求都会重新读库，所以上下文里的 user 是**最新**状态；
	// 测试里手工置位后必须同步内存副本，否则测的是「陈旧状态」这条不存在的路径。
	user.Status = userStatusClosurePending

	recorder := doArchiveRequest(t, ClosureCancel, http.MethodPost, "/adapter-service/user/closure-cancel", nil, user, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if status := closureUserStatus(t, db, 804); status != userStatusEnabled {
		t.Fatalf("cancel must restore enabled, got %q", status)
	}
	if rows := accountLogRows(t, db, AccountActionCancel); len(rows) != 2 {
		t.Fatalf("expected 2 log rows (logs + audit) for cancel, got %d", len(rows))
	}

	// 没有待处理申请时撤销必须 400，而不是假装成功。
	if recorder := doArchiveRequest(t, ClosureCancel, http.MethodPost, "/adapter-service/user/closure-cancel", nil, user, nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("cancel without a pending request must be 400, got %d", recorder.Code)
	}
}

// TestClosureStatusReportsRemainingDays 剩余天数从**申请日志**推导（不加新列）。
func TestClosureStatusReportsRemainingDays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	user := seedClosureUser(t, db, 805, "closure-status", phonePtr("13900000005"), 0, 0)
	if err := db.Model(&entity.User{}).Where("id = ?", 805).
		Update("status", userStatusClosurePending).Error; err != nil {
		t.Fatalf("pin pending status: %v", err)
	}
	requestedAt := time.Now().Add(-48 * time.Hour)
	if err := db.Create(&entity.Log{
		Module: accountLogModule, Action: AccountActionClosure, UserID: user.ID, Username: user.Username,
		Description: "用户申请注销账号", CreateTime: requestedAt,
	}).Error; err != nil {
		t.Fatalf("seed closure log: %v", err)
	}

	recorder := doArchiveRequest(t, ClosureStatus, http.MethodGet, "/adapter-service/user/closure-status", nil, user, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeJSONBody(t, recorder)
	data, _ := payload["data"].(map[string]any)
	if data["pending"] != true {
		t.Fatalf("expected pending=true, got %v", data["pending"])
	}
	// 7 天等待期、已过 2 天 ⇒ 剩 5 天。
	if remaining, _ := data["remainingDays"].(float64); int(remaining) != closureWaitingDays-2 {
		t.Fatalf("expected remainingDays=%d, got %v", closureWaitingDays-2, data["remainingDays"])
	}
	if data["requestedAt"] == nil {
		t.Fatal("status must expose when the closure was requested")
	}

	// 已撤销的账号：pending=false 且剩余天数为 0（不是残留旧值）。
	if err := db.Model(&entity.User{}).Where("id = ?", 805).Update("status", userStatusEnabled).Error; err != nil {
		t.Fatalf("reset status: %v", err)
	}
	recorder = doArchiveRequest(t, ClosureStatus, http.MethodGet, "/adapter-service/user/closure-status", nil, user, nil)
	data, _ = decodeJSONBody(t, recorder)["data"].(map[string]any)
	if data["pending"] != false {
		t.Fatalf("a cancelled account must report pending=false, got %v", data["pending"])
	}
	if remaining, _ := data["remainingDays"].(float64); remaining != 0 {
		t.Fatalf("a cancelled account must report 0 remaining days, got %v", data["remainingDays"])
	}
}

// TestClosurePendingStatusSurvivesAdminProfileUpdate 是本任务最关键的一条
// （红字要求）：管理员通过 PUT /user/:id 改资料时，closure_pending 不能被
// normalizeUserStatus 静默改回 enabled。
//
// 变异验证的靶子就是这里：把 normalizeUserStatus 改回只认 enabled / disabled，
// 这条会以「expected closure_pending, got enabled」变红。
func TestClosurePendingStatusSurvivesAdminProfileUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 810, "closure-update-admin")
	target := seedClosureUser(t, db, 811, "closure-update-target", phonePtr("13900000011"), 0, 0)
	if err := db.Model(&entity.User{}).Where("id = ?", target.ID).
		Update("status", userStatusClosurePending).Error; err != nil {
		t.Fatalf("pin pending status: %v", err)
	}

	recorder := doArchiveRequest(t, UpdateUser, http.MethodPut, "/adapter-service/user/811",
		[]byte(`{"nickname":"新昵称","role":"user","status":"closure_pending"}`), admin,
		gin.Params{{Key: "id", Value: "811"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if status := closureUserStatus(t, db, 811); status != userStatusClosurePending {
		t.Fatalf("closure_pending must survive the profile update, got %q (normalizeUserStatus is dropping it)", status)
	}

	// 归一化函数本身的行为也直接钉住（防止有人只改了 handler 绕开它）。
	if got := normalizeUserStatus("closure_pending"); got != userStatusClosurePending {
		t.Fatalf("normalizeUserStatus must keep closure_pending, got %q", got)
	}
	if got := normalizeUserStatus(" CLOSURE_PENDING "); got != userStatusClosurePending {
		t.Fatalf("normalizeUserStatus must be case/space insensitive, got %q", got)
	}
	if got := normalizeUserStatus("disabled"); got != "disabled" {
		t.Fatalf("disabled must stay disabled, got %q", got)
	}
	if got := normalizeUserStatus(""); got != "enabled" {
		t.Fatalf("empty status must default to enabled, got %q", got)
	}
}

// TestClosureSessionAllowedButSpendingDenied 钉住「会话」与「消费」两个判据的分离：
//
//	closure_pending：会话可以存在（否则用户被踢出登录态，撤销入口点不到）
//	                 但不可消费（76 §3.3.3「与封禁一致」）
//
// 两个布尔承载一种意图就会出事：把 closure_pending 塞进 isUserEnabled，
// 会让「可登录」与「可消费」同时放开；反过来只认 enabled，又会让撤销入口失效。
func TestClosureSessionAllowedButSpendingDenied(t *testing.T) {
	pending := &entity.User{ID: 1, Status: userStatusClosurePending}
	if !isUserSessionAllowed(pending) {
		t.Fatal("closure_pending must be able to hold a session so the user can cancel")
	}
	if isUserEnabled(pending) {
		t.Fatal("closure_pending is not a normal enabled account")
	}
	if isUserSpendable(pending) {
		t.Fatal("closure_pending must not be allowed to spend during the waiting period")
	}

	enabled := &entity.User{ID: 2, Status: "enabled"}
	if !isUserSessionAllowed(enabled) || !isUserEnabled(enabled) || !isUserSpendable(enabled) {
		t.Fatal("enabled must pass every predicate")
	}

	disabled := &entity.User{ID: 3, Status: "disabled"}
	if isUserSessionAllowed(disabled) || isUserEnabled(disabled) || isUserSpendable(disabled) {
		t.Fatal("disabled must fail every predicate")
	}

	legacy := &entity.User{ID: 4, Status: ""}
	if !isUserSessionAllowed(legacy) || !isUserEnabled(legacy) {
		t.Fatal("empty status means a normal account (historical rows)")
	}
	// 钱的路上一律从严：空串不算可消费（沿用扣费路径既有口径）。
	if isUserSpendable(legacy) {
		t.Fatal("empty status must not be spendable: the billing path requires an explicit enabled")
	}

	for name, predicate := range map[string]func(*entity.User) bool{
		"isUserSessionAllowed": isUserSessionAllowed, "isUserEnabled": isUserEnabled, "isUserSpendable": isUserSpendable,
	} {
		if predicate(nil) {
			t.Fatalf("%s(nil) must be false", name)
		}
	}
}

// TestClosurePendingCannotSpendOnSearchAdmission 是**消费路径**的钉子：
// 注销等待期账号带着充足余额与有效令牌，也必须被搜题准入拒绝。
//
// 这条不依赖 Auth（会话是放行的），所以它证明的是「准入门自己拦」，
// 而不是「整个会话被 401 干掉了」—— 后者会连撤销一起废掉。
func TestClosurePendingCannotSpendOnSearchAdmission(t *testing.T) {
	token := entity.AccessToken{ID: 1, UserID: 700, Status: "enabled"}
	wallet := entity.User{
		ID: 700, Status: userStatusClosurePending, Balance: 50, GiftBalance: 10,
	}
	err := checkSearchSpendingAdmission(wallet, token)
	if err == nil {
		t.Fatal("a closure_pending account must be rejected by the search spending admission")
	}
	if !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("expected ErrInvalidAccessTokenAuthorization, got %v", err)
	}

	// 同一份钱包与令牌，只把状态改成 enabled ⇒ 必须放行。
	wallet.Status = "enabled"
	if err := checkSearchSpendingAdmission(wallet, token); err != nil {
		t.Fatalf("an enabled account with balance must pass the admission, got %v", err)
	}

	// 对照：这是**准入层**在拦，不是「没钱」。把余额清零后错误类型不变
	// （状态判定在钱包判定之前），说明拒绝的依据是账号状态而不是余额。
	wallet.Status = userStatusClosurePending
	wallet.Balance, wallet.GiftBalance = 0, 0
	if err := checkSearchSpendingAdmission(wallet, token); !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("status check must run before the wallet gate, got %v", err)
	}
}

// TestClosureStatusRequiresAuthenticatedUser 保底：状态接口必须走 Auth（无当前用户时 401）。
func TestClosureStatusRequiresAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = newArchiveClosureTestDB(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/user/closure-status", nil)
	ClosureStatus(ctx)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a session, got %d", recorder.Code)
	}
}

// TestClosureStatusNotifiesEligibility 状态接口要把「能否自助注销」的理由下发，
// 前端据此置灰按钮并说明原因（76 §3.3.4：置灰而不是隐藏）。
func TestClosureStatusNotifiesEligibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	withPhone := seedClosureUser(t, db, 820, "closure-eligible", phonePtr("13900000020"), 20, 0)
	withoutPhone := seedClosureUser(t, db, 821, "closure-ineligible", nil, 0, 0)

	recorder := doArchiveRequest(t, ClosureStatus, http.MethodGet, "/adapter-service/user/closure-status", nil, withPhone, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	data, _ := decodeJSONBody(t, recorder)["data"].(map[string]any)
	if data["eligible"] != true || data["phoneBound"] != true {
		t.Fatalf("phone-bound account must be eligible, got %s", mustJSON(t, data))
	}
	if data["requiresTicket"] != true {
		t.Fatalf("20 元余额必须提示需要人工工单，got %v", data["requiresTicket"])
	}

	recorder = doArchiveRequest(t, ClosureStatus, http.MethodGet, "/adapter-service/user/closure-status", nil, withoutPhone, nil)
	data, _ = decodeJSONBody(t, recorder)["data"].(map[string]any)
	if data["eligible"] != false {
		t.Fatalf("account without phone must be ineligible, got %s", mustJSON(t, data))
	}
	if reason, _ := data["ineligibleReason"].(string); !strings.Contains(reason, "手机号") {
		t.Fatalf("ineligible reason must mention the missing phone, got %q", reason)
	}
}
