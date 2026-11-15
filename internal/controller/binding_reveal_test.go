package controller

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 绑定值明文揭示（GET /admin/bindings/:id/reveal）的行为级回归。
//
// 用户 2026-11-15 拍板的方案是「加一个「显示完整值」按钮并记审计日志，
// 而不是默认明文」。这组用例守的就是那半句括号：
//
//  1. 揭示真的返回明文（否则按钮没有意义）；
//  2. 非管理员被拒，且**不写**审计（被拒绝的请求没有「谁看了什么」可记）；
//  3. 每次揭示都写审计，且**日志文本里不含明文** —— 这是本任务最容易被
//     「顺手把 record.Value 塞进快照」破坏的一条；
//  4. 不存在的 binding id 返回 404（不能 200 + 空值，那会让前端把「没这条」
//     显示成「这条的值是空的」）。
//
// # 为什么明文不含断言要查三个字段
//
// 一条日志的可见文本分散在 Description / OldAnswer / NewAnswer 三处
// （entity.Log 的定义见 logs 表）。只断 Description 会漏掉快照：
// writeAccountOperationLog 把 snapshot 序列化进 OldAnswer，
// RecordAccountAudit 把 Result 白名单序列化进 NewAnswer。
// 任一处漏了明文，明文就永久留在日志里（logs 长期保留、管理员可查）。

// seedRevealBinding 建一个已归档账号 + 一条绑定记录，返回该绑定的 id。
//
// 走真实的归档路径（seedArchivedUser 内部调 service.ReleaseAllBindingsForArchiveInTx），
// 让「归档释放」这一列也真实落地 —— 与 archived_user_test.go 同一口径。
func seedRevealBinding(t *testing.T, db *gorm.DB, userID int32, username, kind, value string) int64 {
	t.Helper()
	seedArchivedUser(t, db, userID, username, entity.UserArchivedReasonAdminDelete, time.Now())
	releasedAt := time.Now()
	record := &entity.UserBindingRecord{
		UserID: userID, UsernameSnapshot: username, Kind: kind,
		Value: value, ValueHash: strings.Repeat("b", 64),
		State: entity.BindingStateReleased,
		// released 行必须 active_key = NULL（否则继续占唯一索引，见 entity 注释）。
		ActiveKey:     nil,
		ReleaseReason: entity.BindingReleaseReasonClosure,
		ReleaseSource: entity.BindingReleaseSourceAdminDelete,
		ReleasedAt:    &releasedAt,
	}
	if err := db.Create(record).Error; err != nil {
		t.Fatalf("create binding record: %v", err)
	}
	return record.ID
}

// revealLogRows 取 reveal 动作写出的 logs 行（module=account）。
func revealLogRows(t *testing.T, db *gorm.DB) []entity.Log {
	t.Helper()
	var rows []entity.Log
	if err := db.Where("module = ? AND action = ?", accountLogModule, AccountActionBindingReveal).
		Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("read reveal logs: %v", err)
	}
	return rows
}

// TestRevealBindingValueReturnsPlaintextForAdmin 是本组的主用例：
// 管理员拿到的是**明文**，且响应里带脱敏值做自校验。
func TestRevealBindingValueReturnsPlaintextForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 3001, "reveal-admin")
	const plain = "13800001111"
	bindingID := seedRevealBinding(t, db, 3011, "reveal-user", entity.BindingKindPhone, plain)

	recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/"+itoa64(bindingID)+"/reveal", nil, admin,
		gin.Params{{Key: "id", Value: itoa64(bindingID)}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	payload := decodeJSONBody(t, recorder)
	// 单条语义：data 必须是**对象**，不是数组（接口不存在任何批量形态）。
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("reveal must return a single object under data, got %s", mustJSON(t, payload))
	}
	if data["value"] != plain {
		t.Fatalf("reveal must return the plaintext value, got %v", data["value"])
	}
	if data["valueMasked"] != "138****1111" {
		t.Fatalf("reveal must also carry the masked form, got %v", data["valueMasked"])
	}
	if data["kind"] != entity.BindingKindPhone {
		t.Fatalf("reveal must carry kind, got %v", data["kind"])
	}
	if id, _ := data["id"].(float64); int64(id) != bindingID {
		t.Fatalf("reveal must echo the binding id %d, got %v", bindingID, data["id"])
	}

	// 明文不得被缓存：同一个 URL 反复请求只会不断追加审计。
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("reveal response must not be cacheable, Cache-Control=%q", got)
	}
}

// TestRevealBindingValueRejectsNonAdminWithoutAudit 非管理员 403，且不写审计。
//
// 「被拒绝也写审计」是错的：审计要回答「谁看了什么」，
// 一次没有发生的读取不该在日志里留下明文可见的记录。
func TestRevealBindingValueRejectsNonAdminWithoutAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	plain := &entity.User{ID: 3021, Username: "not-admin", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(plain).Error; err != nil {
		t.Fatalf("create plain user: %v", err)
	}
	bindingID := seedRevealBinding(t, db, 3031, "guard-me", entity.BindingKindPhone, "13800002222")

	recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/"+itoa64(bindingID)+"/reveal", nil, plain,
		gin.Params{{Key: "id", Value: itoa64(bindingID)}})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	// 响应里绝不能带明文（否则 403 就成了读数通道）。
	if strings.Contains(recorder.Body.String(), "13800002222") {
		t.Fatalf("rejected reveal leaked the plaintext: %s", recorder.Body.String())
	}
	if rows := revealLogRows(t, db); len(rows) != 0 {
		t.Fatalf("a rejected reveal must not write an audit row, got %d", len(rows))
	}
}

// TestRevealBindingValueWritesAuditWithoutPlaintext 是本任务的核心钉子：
// 揭示必须留痕，且**日志文本里不含明文**。
//
// 三个可见字段全查（Description / OldAnswer / NewAnswer）：
// 只查 Description 会漏掉「快照里塞了 Value」这一类回退。
func TestRevealBindingValueWritesAuditWithoutPlaintext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 3041, "audit-admin")
	const plain = "13800003333"
	bindingID := seedRevealBinding(t, db, 3051, "audit-user", entity.BindingKindPhone, plain)

	recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/"+itoa64(bindingID)+"/reveal", nil, admin,
		gin.Params{{Key: "id", Value: itoa64(bindingID)}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	// 前置断言：响应确实带明文。没有这一条，下面的「日志不含明文」在
	// 「接口压根没返回明文」的实现下也会绿 —— 那是假钉子。
	if !strings.Contains(recorder.Body.String(), plain) {
		t.Fatalf("precondition failed: response must carry the plaintext, got %s", recorder.Body.String())
	}

	// 双通道都写 ⇒ 同一 action 两行（与恢复 / 作废同一约定）。
	rows := revealLogRows(t, db)
	if len(rows) != 2 {
		t.Fatalf("expected 2 log rows (logs + audit) for reveal, got %d", len(rows))
	}

	for _, row := range rows {
		if row.UserID != admin.ID {
			t.Fatalf("log user_id must be the operator %d, got %d", admin.ID, row.UserID)
		}
		// ── 红线：三处可见文本都不能含明文 ──
		for field, text := range map[string]string{
			"Description": row.Description,
			"OldAnswer":   row.OldAnswer,
			"NewAnswer":   row.NewAnswer,
		} {
			if strings.Contains(text, plain) {
				t.Fatalf("log %s leaked the plaintext %q: %s", field, plain, text)
			}
		}
		// 正向：脱敏值、账号 id、绑定 id 都要能定位到这一行。
		if !strings.Contains(row.Description, "3051") || !strings.Contains(row.Description, itoa64(bindingID)) {
			t.Fatalf("description must name the account and binding, got %q", row.Description)
		}
		if !strings.Contains(row.Description, "138****3333") {
			t.Fatalf("description must carry the masked value (audit needs it to be identifiable), got %q", row.Description)
		}
	}

	// 至少一行是「操作日志」格式：OldAnswer 里带 recordId / valueMasked。
	if !strings.Contains(rows[0].OldAnswer, `"recordId"`) || !strings.Contains(rows[0].OldAnswer, `"valueMasked"`) {
		t.Fatalf("operation log must record recordId/valueMasked, got %s", rows[0].OldAnswer)
	}
	if strings.Contains(rows[0].OldAnswer, `"value":`) {
		t.Fatalf("operation log snapshot must not carry a value field: %s", rows[0].OldAnswer)
	}
}

// TestRevealBindingValueUnknownBindingReturns404 不存在的 binding id 必须 404。
func TestRevealBindingValueUnknownBindingReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 3061, "missing-admin")
	// 建一条真实记录做对照：证明 404 不是因为「表里一行都没有」。
	seedRevealBinding(t, db, 3071, "has-binding", entity.BindingKindPhone, "13800004444")

	recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/999999/reveal", nil, admin,
		gin.Params{{Key: "id", Value: "999999"}})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown binding id, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if rows := revealLogRows(t, db); len(rows) != 0 {
		t.Fatalf("a 404 must not write an audit row, got %d", len(rows))
	}

	// 参数非法走 400（与 /void 同一口径）。
	if recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/0/reveal", nil, admin,
		gin.Params{{Key: "id", Value: "0"}}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for id=0, got %d", recorder.Code)
	}
}

// TestRevealBindingValueMasksShortWechatValue 太短的值必须整体掩码。
//
// 这条钉住「复用唯一脱敏实现」：maskArchivedBindingValue 对 ≤4 rune 的值
// 返回全星号。若有人为了「reveal 要更完整」而另写一份保留首尾的脱敏，
// 审计里就会出现 OpenID 的真实片段。
func TestRevealBindingValueMasksShortWechatValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 3081, "wechat-admin")
	const plain = "oAbC"
	bindingID := seedRevealBinding(t, db, 3091, "wechat-user", entity.BindingKindWechat, plain)

	recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/"+itoa64(bindingID)+"/reveal", nil, admin,
		gin.Params{{Key: "id", Value: itoa64(bindingID)}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	payload := decodeJSONBody(t, recorder)
	data, _ := payload["data"].(map[string]any)
	if data["value"] != plain {
		t.Fatalf("admin reveal must still return the plaintext, got %v", data["value"])
	}
	if data["valueMasked"] != "****" {
		t.Fatalf("short value must be fully masked, got %v", data["valueMasked"])
	}
	rows := revealLogRows(t, db)
	if len(rows) != 2 {
		t.Fatalf("expected 2 log rows, got %d", len(rows))
	}
	for _, row := range rows {
		blob, _ := json.Marshal([]string{row.Description, row.OldAnswer, row.NewAnswer})
		if strings.Contains(string(blob), plain) {
			t.Fatalf("short wechat value leaked into the log: %s", blob)
		}
	}
}

// TestRevealBindingValueWorksForActiveUserBinding 是本组最关键的一条**集成语义**：
// 揭示必须对**活跃账号**的绑定记录也生效。
//
// # 为什么必须单独钉住
//
// 按钮加在用户管理页「查看」弹窗（GET /admin/users/:id/bindings），
// 那个弹窗服务的是**活跃**账号，不是归档账号。
// 如果实现不小心把查询限定在「已归档账号」（例如复用 loadArchivedUser 的
// `deleted_at IS NOT NULL` 条件），归档用例会全绿，而真实使用场景 100% 失效
// —— 表现为「点了按钮提示绑定记录不存在」，且不报任何错。
//
// 这条用例用**未归档**的普通账号建绑定，钉死那条不必要的限制不存在。
func TestRevealBindingValueWorksForActiveUserBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 3101, "active-admin")
	// 注意：这里**不**调 seedArchivedUser —— 账号是活跃的（deleted_at 为 NULL）。
	if err := db.Create(&entity.User{
		ID: 3111, Username: "active-user", Role: "user", Status: "enabled", RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create active user: %v", err)
	}
	const plain = "13800005555"
	boundAt := time.Now()
	record := &entity.UserBindingRecord{
		UserID: 3111, UsernameSnapshot: "active-user", Kind: entity.BindingKindPhone,
		Value: plain, ValueHash: strings.Repeat("c", 64),
		State: entity.BindingStateActive, BoundAt: &boundAt,
	}
	if err := db.Create(record).Error; err != nil {
		t.Fatalf("create active binding: %v", err)
	}
	// 前置断言：该账号确实没被归档（否则这条用例退化成上一条）。
	var deletedAtCount int64
	if err := db.Unscoped().Model(&entity.User{}).Where("id = ? AND deleted_at IS NOT NULL", 3111).
		Count(&deletedAtCount).Error; err != nil {
		t.Fatalf("probe deleted_at: %v", err)
	}
	if deletedAtCount != 0 {
		t.Fatalf("precondition failed: user 3111 must be active, got %d archived rows", deletedAtCount)
	}

	recorder := doArchiveRequest(t, RevealBindingValue, http.MethodGet,
		"/api/admin/bindings/"+itoa64(record.ID)+"/reveal", nil, admin,
		gin.Params{{Key: "id", Value: itoa64(record.ID)}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("reveal must work for active-account bindings (UserList dialog path), "+
			"got %d with body %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeJSONBody(t, recorder)
	data, _ := payload["data"].(map[string]any)
	if data["value"] != plain {
		t.Fatalf("active binding reveal must return plaintext, got %v", data["value"])
	}
	if data["state"] != entity.BindingStateActive {
		t.Fatalf("reveal must echo the real state, got %v", data["state"])
	}
	// 活跃绑定揭示同样要留痕（「谁看了谁的手机号」与归档态没有区别）。
	if rows := revealLogRows(t, db); len(rows) != 2 {
		t.Fatalf("active binding reveal must also write 2 audit rows, got %d", len(rows))
	}
}

// itoa64 拼路径参数（主键都是 int64）。
func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
