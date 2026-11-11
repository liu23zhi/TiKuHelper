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
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 已归档账号管理的行为级回归（task_doc/76 §3.2.3 / §3.4）。
//
// 这些用例守的是四条容易被「看起来对」的实现架空的语义：
//  1. 列表必须 Unscoped —— 否则归档行被 GORM 软删除过滤掉，列表恒为空且不报错；
//  2. 用户名冲突必须主动检查 —— users.username 没有唯一索引，靠数据库报错兜底
//     会得到「两个同名用户」；
//  3. 冲突未确认必须 409 且不改数据，确认后必须真的改名；
//  4. 日志的 UserID 是**操作者**，OldAnswer 是被操作对象快照且不含 password hash。

// newArchiveClosureTestDB 在用户控制器测试库之上补齐归档 / 注销链路需要的表。
//
// 单独建一个 helper 而不是改 newUserControllerTestDB：那会改动其余 60 多个
// 用户用例的建库成本与表集合，收益为零。
func newArchiveClosureTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(
		&entity.UserBindingRecord{}, &entity.LoginAttempt{},
		&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{},
	); err != nil {
		t.Fatalf("migrate archive tables: %v", err)
	}
	return db
}

// seedArchiveAdmin 建一个管理员操作者。
func seedArchiveAdmin(t *testing.T, db *gorm.DB, id int32, username string) *entity.User {
	t.Helper()
	admin := &entity.User{ID: id, Username: username, Password: "synthetic-admin-pw", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin %s: %v", username, err)
	}
	return admin
}

// seedArchivedUser 建一个账号并**走真实归档路径**（释放绑定 + 写归档四列）。
//
// 走 service 而不是手写 SQL：手写 SQL 会让「归档到底改了哪几列」这件事
// 在测试里自成一套，实现改了测试照样绿。
func seedArchivedUser(t *testing.T, db *gorm.DB, id int32, username, reason string, archivedAt time.Time) *entity.User {
	t.Helper()
	user := &entity.User{
		ID: id, Username: username, Password: "synthetic-user-pw-hash", Nickname: username + "-nick",
		Role: "user", Status: "enabled", RegisterSource: "public", Balance: 1.5, GiftBalance: 0.5,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := service.ReleaseAllBindingsForArchiveInTx(tx, user.ID, reason, 0); err != nil {
			return err
		}
		return service.ArchiveUserInTx(tx, *user, reason, 999)
	}); err != nil {
		t.Fatalf("archive user %s: %v", username, err)
	}
	// 归档时点固定成用例给定的时间，让「按时间筛选」的断言可控。
	if err := db.Exec("UPDATE users SET deleted_at = ?, archived_at = ? WHERE id = ?",
		archivedAt, archivedAt, id).Error; err != nil {
		t.Fatalf("pin archive time for %s: %v", username, err)
	}
	return user
}

// doArchiveRequest 组装一次 handler 调用并返回响应。
//
// 必须把 handler 传进来而不是只返回 context：忘记真正调用 handler 时
// 响应体是空的、状态码默认 200，用例会以「读不出 JSON」或「状态没变」的形式
// 惨烈地假绿/假红 —— 让调用点在编译期就必须指名调用哪个 handler。
func doArchiveRequest(t *testing.T, handler gin.HandlerFunc, method, target string, body []byte, actor *entity.User, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	ctx.Request = request
	ctx.Params = params
	if actor != nil {
		ctx.Set("user", actor)
	}
	handler(ctx)
	return recorder
}

// accountLogRows 取某个 action 写出的 logs 行。
func accountLogRows(t *testing.T, db *gorm.DB, action int32) []entity.Log {
	t.Helper()
	var rows []entity.Log
	if err := db.Where("module = ? AND action = ?", accountLogModule, action).
		Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("read account logs for action %d: %v", action, err)
	}
	return rows
}

func decodeJSONBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response body %q: %v", recorder.Body.String(), err)
	}
	return payload
}

// TestArchivedUserListRequiresUnscopedAndFilters 是本文件最重要的一条：
// 归档列表必须能看见已归档行（Unscoped），并支持原因 / 时间 / 关键字筛选。
func TestArchivedUserListRequiresUnscopedAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 900, "archive-admin")

	if err := db.Create(&entity.User{ID: 901, Username: "still-active", Role: "user", Status: "enabled", RegisterSource: "public"}).Error; err != nil {
		t.Fatalf("create active user: %v", err)
	}
	seedArchivedUser(t, db, 902, "gone-by-admin", entity.UserArchivedReasonAdminDelete,
		time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local))
	seedArchivedUser(t, db, 903, "gone-by-closure", entity.UserArchivedReasonUserClosure,
		time.Date(2026, 11, 5, 10, 0, 0, 0, time.Local))

	// 对照：同一条件但**不带** Unscoped 时必须是 0 —— 这条钉住
	// 「Unscoped 是功能本体，不是可选优化」。如果有人把 Unscoped 去掉，
	// 列表用例会以「total=0」直接红。
	var scopedCount int64
	if err := db.Model(&archivedUserRow{}).Where("deleted_at IS NOT NULL").Count(&scopedCount).Error; err != nil {
		t.Fatalf("count scoped archived rows: %v", err)
	}
	if scopedCount != 0 {
		t.Fatalf("default query already sees archived rows (count=%d): the Unscoped assertion below would be vacuous", scopedCount)
	}

	list := func(query string) map[string]any {
		recorder := doArchiveRequest(t, ArchivedUserList, http.MethodGet, "/adapter-service/admin/archived-users"+query, nil, admin, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("list %q expected 200, got %d with body %s", query, recorder.Code, recorder.Body.String())
		}
		return decodeJSONBody(t, recorder)
	}

	all := list("")
	if total, _ := all["total"].(float64); int(total) != 2 {
		t.Fatalf("expected 2 archived users, got %v (%s)", all["total"], mustJSON(t, all))
	}
	ids := archivedRowIDs(all)
	// 按 deleted_at desc 排序：11-05 在 11-01 之前。
	if len(ids) != 2 || ids[0] != 903 || ids[1] != 902 {
		t.Fatalf("expected archived ids [903 902] ordered by archived time desc, got %v", ids)
	}

	closureOnly := list("?reason=" + entity.UserArchivedReasonUserClosure)
	if total, _ := closureOnly["total"].(float64); int(total) != 1 {
		t.Fatalf("reason filter must narrow to 1 row, got %v", closureOnly["total"])
	}
	if ids := archivedRowIDs(closureOnly); len(ids) != 1 || ids[0] != 903 {
		t.Fatalf("reason filter must return user 903, got %v", ids)
	}

	byKeyword := list("?keyword=admin")
	if ids := archivedRowIDs(byKeyword); len(ids) != 1 || ids[0] != 902 {
		t.Fatalf("keyword filter must match gone-by-admin only, got %v", ids)
	}

	byTime := list("?startTime=2026-11-04")
	if ids := archivedRowIDs(byTime); len(ids) != 1 || ids[0] != 903 {
		t.Fatalf("startTime filter must drop the 11-01 row, got %v", ids)
	}

	// 已归档用户绝不能出现在活跃用户列表里（软删除过滤没被破坏的对照）。
	var activeCount int64
	if err := db.Model(&entity.User{}).Count(&activeCount).Error; err != nil {
		t.Fatalf("count active users: %v", err)
	}
	if activeCount != 2 { // archive-admin + still-active
		t.Fatalf("active user list must exclude archived rows, got %d", activeCount)
	}

	// 非管理员一律 403。
	plain := &entity.User{ID: 904, Username: "plain-user", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(plain).Error; err != nil {
		t.Fatalf("create plain user: %v", err)
	}
	if recorder := doArchiveRequest(t, ArchivedUserList, http.MethodGet, "/adapter-service/admin/archived-users", nil, plain, nil); recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin must be rejected with 403, got %d", recorder.Code)
	}
}

func archivedRowIDs(payload map[string]any) []int32 {
	items, _ := payload["data"].([]any)
	ids := make([]int32, 0, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		if id, ok := row["id"].(float64); ok {
			ids = append(ids, int32(id))
		}
	}
	return ids
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// TestArchivedUserDetailReturnsBindingHistory 详情必须带该用户的绑定记录历史
// （含已释放行 —— 归档时活跃绑定已被释放，只看 active 会得到空列表）。
func TestArchivedUserDetailReturnsBindingHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 910, "detail-admin")
	seedArchivedUser(t, db, 911, "detail-user", entity.UserArchivedReasonAdminDelete, time.Now())

	binding := &entity.UserBindingRecord{
		UserID: 911, UsernameSnapshot: "detail-user", Kind: entity.BindingKindPhone,
		Value: "13800001111", ValueHash: strings.Repeat("a", 64),
		State: entity.BindingStateReleased, ReleaseReason: entity.BindingReleaseReasonClosure,
		ReleaseSource: entity.BindingReleaseSourceAdminDelete, OperatorUserID: 999,
	}
	if err := db.Create(binding).Error; err != nil {
		t.Fatalf("create binding record: %v", err)
	}

	recorder := doArchiveRequest(t, ArchivedUserDetail, http.MethodGet, "/adapter-service/admin/archived-users/911", nil, admin,
		gin.Params{{Key: "id", Value: "911"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	// 归档账号的 password hash 不得出现在详情响应里。
	if strings.Contains(recorder.Body.String(), "synthetic-user-pw-hash") {
		t.Fatal("detail response leaked the password hash")
	}
	payload := decodeJSONBody(t, recorder)
	bindings, _ := payload["bindings"].([]any)
	if len(bindings) != 1 {
		t.Fatalf("expected 1 binding history row, got %v (%s)", len(bindings), mustJSON(t, payload))
	}
	row, _ := bindings[0].(map[string]any)
	if row["valueMasked"] != "138****1111" {
		t.Fatalf("binding value must be masked, got %v", row["valueMasked"])
	}
	if row["releaseSource"] != entity.BindingReleaseSourceAdminDelete {
		t.Fatalf("admin delete must record release_source=admin_delete, got %v", row["releaseSource"])
	}
	data, _ := payload["data"].(map[string]any)
	if data["archivedReason"] != entity.UserArchivedReasonAdminDelete {
		t.Fatalf("detail must expose archived reason, got %v", data["archivedReason"])
	}

	// 活跃账号（未归档）不能通过这个接口读到：否则恢复接口会变成任意账号写入口。
	if recorder := doArchiveRequest(t, ArchivedUserDetail, http.MethodGet, "/adapter-service/admin/archived-users/910", nil, admin,
		gin.Params{{Key: "id", Value: "910"}}); recorder.Code != http.StatusNotFound {
		t.Fatalf("active account must be 404 on archive detail, got %d", recorder.Code)
	}
}

// TestArchivedUserRestoreWithoutConflictRestoresRow 验证无冲突恢复：
// 行回到默认查询里、归档四列清空、两条日志通道都写且 UserID 是操作者。
func TestArchivedUserRestoreWithoutConflictRestoresRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 920, "restore-admin")
	seedArchivedUser(t, db, 921, "restore-me", entity.UserArchivedReasonAdminDelete, time.Now())

	recorder := doArchiveRequest(t, ArchivedUserRestore, http.MethodPost, "/adapter-service/admin/archived-users/921/restore",
		[]byte(`{"confirmRename":false}`), admin, gin.Params{{Key: "id", Value: "921"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	// 默认查询（带软删除过滤）必须能读到了 —— 这就是「恢复生效」的行为判据。
	restored, err := dao.User.Where(dao.User.ID.Eq(921)).First()
	if err != nil {
		t.Fatalf("restored user must be visible to default queries: %v", err)
	}
	if restored.Username != "restore-me" {
		t.Fatalf("username must be unchanged without conflict, got %s", restored.Username)
	}
	var archive entity.UserArchive
	if err := db.Unscoped().Where("id = ?", 921).Take(&archive).Error; err != nil {
		t.Fatalf("read archive columns: %v", err)
	}
	if archive.DeletedAt.Valid {
		t.Fatal("deleted_at must be cleared after restore")
	}
	if archive.ArchivedReason != "" || archive.ArchivedByUserID != 0 || archive.ArchivedAt != nil {
		t.Fatalf("archive metadata must be cleared, got %+v", archive)
	}

	// 两个通道都写 ⇒ 同一 action 两行 logs（操作日志 + 结构化审计）。
	rows := accountLogRows(t, db, AccountActionRestore)
	if len(rows) != 2 {
		t.Fatalf("expected 2 log rows (logs + audit) for restore, got %d", len(rows))
	}
	for _, row := range rows {
		if row.UserID != admin.ID {
			t.Fatalf("log user_id must be the operator %d, got %d (%s)", admin.ID, row.UserID, row.Description)
		}
		if strings.Contains(row.OldAnswer, "synthetic-user-pw-hash") || strings.Contains(row.NewAnswer, "synthetic-user-pw-hash") {
			t.Fatal("log snapshot must not carry the password hash")
		}
	}
	// 至少一行是「操作日志」格式：OldAnswer 里带原用户名 / 新用户名两个字段。
	if !strings.Contains(rows[0].OldAnswer, `"originalUsername"`) || !strings.Contains(rows[0].OldAnswer, `"newUsername"`) {
		t.Fatalf("operation log must record originalUsername/newUsername, got %s", rows[0].OldAnswer)
	}
}

// TestArchivedUserRestoreUsernameConflictReturns409WithoutConfirm 冲突必须 409 + 建议名，
// 且**不得改任何数据**（二次确认的存在意义）。
func TestArchivedUserRestoreUsernameConflictReturns409WithoutConfirm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 930, "conflict-admin")
	seedArchivedUser(t, db, 931, "zhangsan", entity.UserArchivedReasonUserClosure, time.Now())
	// 归档时保留用户名（76 §3.4.6），别人可以注册同名。
	if err := db.Create(&entity.User{ID: 932, Username: "zhangsan", Role: "user", Status: "enabled", RegisterSource: "public"}).Error; err != nil {
		t.Fatalf("create occupying user: %v", err)
	}

	recorder := doArchiveRequest(t, ArchivedUserRestore, http.MethodPost, "/adapter-service/admin/archived-users/931/restore",
		[]byte(`{"confirmRename":false}`), admin, gin.Params{{Key: "id", Value: "931"}})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeJSONBody(t, recorder)
	if payload["conflict"] != "username" {
		t.Fatalf("expected conflict=username, got %v", payload["conflict"])
	}
	if payload["currentUsername"] != "zhangsan" {
		t.Fatalf("expected currentUsername=zhangsan, got %v", payload["currentUsername"])
	}
	if occupied, _ := payload["occupiedByUserId"].(float64); int32(occupied) != 932 {
		t.Fatalf("expected occupiedByUserId=932, got %v", payload["occupiedByUserId"])
	}
	suggested, _ := payload["suggestedUsername"].(string)
	if suggested != "zhangsan_restored_1" {
		t.Fatalf("expected suggestedUsername=zhangsan_restored_1, got %q", suggested)
	}

	// 未确认 ⇒ 数据一点都不能动。
	var archive entity.UserArchive
	if err := db.Unscoped().Where("id = ?", 931).Take(&archive).Error; err != nil {
		t.Fatalf("read archive row: %v", err)
	}
	var stillArchived entity.User
	if err := db.Unscoped().Where("id = ?", 931).Take(&stillArchived).Error; err != nil {
		t.Fatalf("read archived user: %v", err)
	}
	if !archive.DeletedAt.Valid || stillArchived.Username != "zhangsan" {
		t.Fatalf("unconfirmed restore must not touch the row, got %+v", stillArchived)
	}
	if len(accountLogRows(t, db, AccountActionRestore)) != 0 {
		t.Fatal("a 409 conflict probe must not write a restore log")
	}
}

// TestArchivedUserRestoreConfirmRenameUsesRestoredSuffix 确认后必须真的改名，
// 且候选名被占用时继续往后编号（这里 _restored_1 已被占，应得到 _restored_2）。
func TestArchivedUserRestoreConfirmRenameUsesRestoredSuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 940, "rename-admin")
	seedArchivedUser(t, db, 941, "lisi", entity.UserArchivedReasonUserClosure, time.Now())
	for id, name := range map[int32]string{942: "lisi", 943: "lisi_restored_1"} {
		if err := db.Create(&entity.User{ID: id, Username: name, Role: "user", Status: "enabled", RegisterSource: "public"}).Error; err != nil {
			t.Fatalf("create occupying user %s: %v", name, err)
		}
	}

	recorder := doArchiveRequest(t, ArchivedUserRestore, http.MethodPost, "/adapter-service/admin/archived-users/941/restore",
		[]byte(`{"confirmRename":true}`), admin, gin.Params{{Key: "id", Value: "941"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	restored, err := dao.User.Where(dao.User.ID.Eq(941)).First()
	if err != nil {
		t.Fatalf("restored user must be visible: %v", err)
	}
	if restored.Username != "lisi_restored_2" {
		t.Fatalf("expected renamed to lisi_restored_2 (skipping the occupied _restored_1), got %s", restored.Username)
	}
	payload := decodeJSONBody(t, recorder)
	if payload["renamed"] != true {
		t.Fatalf("response must flag that the username was renamed, got %v", payload["renamed"])
	}

	// 改名必须留痕（76 §3.4.5）：Description 与 OldAnswer 都要有新旧名字。
	rows := accountLogRows(t, db, AccountActionRestore)
	if len(rows) != 2 {
		t.Fatalf("expected 2 log rows for restore, got %d", len(rows))
	}
	if !strings.Contains(rows[0].Description, "lisi_restored_2") || !strings.Contains(rows[0].Description, "已被占用") {
		t.Fatalf("description must explain the rename, got %q", rows[0].Description)
	}
	if !strings.Contains(rows[0].OldAnswer, `"originalUsername":"lisi"`) {
		t.Fatalf("OldAnswer must keep the original username, got %s", rows[0].OldAnswer)
	}
}

// TestArchivedUserRestoreGivesUpAfter100Attempts 100 次候选名都被占用时
// 必须 500 —— 绝不静默用一个可能重复的名字（76 §3.4.4 / 红线 13）。
func TestArchivedUserRestoreGivesUpAfter100Attempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 950, "exhaust-admin")
	seedArchivedUser(t, db, 951, "wangwu", entity.UserArchivedReasonUserClosure, time.Now())
	if err := db.Create(&entity.User{ID: 952, Username: "wangwu", Role: "user", Status: "enabled", RegisterSource: "public"}).Error; err != nil {
		t.Fatalf("create occupying user: %v", err)
	}
	for attempt := 1; attempt <= archivedUserRestoreMaxAttempts; attempt++ {
		id := int32(1000 + attempt)
		name := fmt.Sprintf("wangwu_restored_%d", attempt)
		if err := db.Create(&entity.User{ID: id, Username: name, Role: "user", Status: "enabled", RegisterSource: "public"}).Error; err != nil {
			t.Fatalf("create occupying user %s: %v", name, err)
		}
	}

	recorder := doArchiveRequest(t, ArchivedUserRestore, http.MethodPost, "/adapter-service/admin/archived-users/951/restore",
		[]byte(`{"confirmRename":true}`), admin, gin.Params{{Key: "id", Value: "951"}})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when all 100 candidates are taken, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var archive entity.UserArchive
	if err := db.Unscoped().Where("id = ?", 951).Take(&archive).Error; err != nil {
		t.Fatalf("read archive row: %v", err)
	}
	if !archive.DeletedAt.Valid {
		t.Fatal("failed restore must leave the account archived")
	}
	if len(accountLogRows(t, db, AccountActionRestore)) != 0 {
		t.Fatal("failed restore must not write a success log")
	}
}

// TestArchivedUserRestoreRejectsNonAdmin 恢复是管理员操作。
func TestArchivedUserRestoreRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	seedArchivedUser(t, db, 960, "guard-me", entity.UserArchivedReasonAdminDelete, time.Now())
	plain := &entity.User{ID: 961, Username: "not-admin", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(plain).Error; err != nil {
		t.Fatalf("create plain user: %v", err)
	}

	recorder := doArchiveRequest(t, ArchivedUserRestore, http.MethodPost, "/adapter-service/admin/archived-users/960/restore",
		[]byte(`{"confirmRename":true}`), plain, gin.Params{{Key: "id", Value: "960"}})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", recorder.Code)
	}
	var archive entity.UserArchive
	if err := db.Unscoped().Where("id = ?", 960).Take(&archive).Error; err != nil {
		t.Fatalf("read archive row: %v", err)
	}
	if !archive.DeletedAt.Valid {
		t.Fatal("rejected restore must leave the account archived")
	}
}

// TestArchivedUserRestoreUnfreezesClosurePending 注销等待期内归档的账号，
// 恢复后必须回到 enabled —— 否则用户永远停在 closure_pending，
// 表现为「恢复了但仍然不能消费、界面仍显示已申请注销」。
func TestArchivedUserRestoreUnfreezesClosurePending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 970, "unfreeze-admin")
	seedArchivedUser(t, db, 971, "pending-user", entity.UserArchivedReasonUserClosure, time.Now())
	if err := db.Unscoped().Model(&entity.UserArchive{}).Where("id = ?", 971).
		Update("status", userStatusClosurePending).Error; err != nil {
		t.Fatalf("pin closure_pending status: %v", err)
	}

	recorder := doArchiveRequest(t, ArchivedUserRestore, http.MethodPost, "/adapter-service/admin/archived-users/971/restore",
		[]byte(`{}`), admin, gin.Params{{Key: "id", Value: "971"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	restored, err := dao.User.Where(dao.User.ID.Eq(971)).First()
	if err != nil {
		t.Fatalf("read restored user: %v", err)
	}
	if restored.Status != userStatusEnabled {
		t.Fatalf("restored closure_pending account must return to enabled, got %q", restored.Status)
	}
}

// TestBuildRestoredUsernameTruncatesLongBase 原名接近列上限时，后缀不能被截掉
// （截掉后缀就变成「随便一个名字」，客服更没法口述）。
func TestBuildRestoredUsernameTruncatesLongBase(t *testing.T) {
	long := strings.Repeat("a", 255)
	candidate := buildRestoredUsername(long, 1)
	if len([]rune(candidate)) > 255 {
		t.Fatalf("restored username exceeds 255 runes: %d", len([]rune(candidate)))
	}
	if !strings.HasSuffix(candidate, "_restored_1") {
		t.Fatalf("suffix must survive truncation, got %q", candidate[len(candidate)-20:])
	}
	if got := buildRestoredUsername("", 3); got != "user_restored_3" {
		t.Fatalf("empty base must fall back to user_restored_N, got %q", got)
	}
}
