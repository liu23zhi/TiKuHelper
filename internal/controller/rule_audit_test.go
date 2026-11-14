package controller

// 规则类（黑名单 / 评价返现规则 / 支付方式）删除审计的控制器用例。
//
// 权威依据：task_doc/45-用户待确认问题清单.md §11.4（Q77-2）与 §11.8（Q77-6，选项 C）。
//
//	Q77-2 规则类**不软删除**，但「删除前必须把被删对象的完整内容序列化进 logs.OldAnswer」，
//	      且必须**同一事务内先写日志再 DELETE** —— 快照要能据此手工重建规则。
//	Q77-6 删除必须填原因，**后端独立校验**（去首尾空白后 ≥2 字符），空值 400。
//
// 这组用例刻意钉「行为」而不是「调用了哪个函数」：
//
//  1. 缺 reason / 纯空格 / 只有 1 个字符 → 400，**规则仍然存在**，且**一行审计都没写**。
//     校验与落库的顺序一旦反过来（先删再校验），这里必然先红。
//  2. 合法 reason → 200，规则被物理删除，logs 里恰好一行 action=61 的快照行，
//     OldAnswer 反序列化后能还原出被删规则的**全部业务字段**（「不软删除」的唯一补偿手段）。
//
// ⚠️ 断言不看实现内部状态（不读 tx、不读函数返回值），只看两个外部可观察事实：
// HTTP 状态码 + 数据库里的行。这样变异实现（去掉长度校验 / 调换写日志与删除的顺序）
// 一定会让其中一条变红，而不是「看起来在测、实际测不出」。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ruleDeleteTestReason 是合法原因（去空白后 5 个字符，满足 ≥2）。
const ruleDeleteTestReason = "测试删除原因"

// 下面三个是同一原因的三种退化形态，预先转义后拼到 URL 上，
// 避免中文字节直接进 request-line（httptest.NewRequest 不接受非 ASCII 的请求行）。
var (
	// ruleDeleteTestReasonQuery 是合法原因的 query 形态。
	ruleDeleteTestReasonQuery = url.QueryEscape(ruleDeleteTestReason)
	// ruleDeleteBlankReasonQuery 是「只有空格」的原因。
	ruleDeleteBlankReasonQuery = url.QueryEscape("   ")
	// ruleDeleteSingleRuneReasonQuery 是「去空白后只有 1 个字符」的原因（差 1 个字符不达标）。
	ruleDeleteSingleRuneReasonQuery = url.QueryEscape("删")
)

// ruleDeleteAuditPayload 是 logs.OldAnswer 的固定形状（§11.4 的 JSON 契约）。
//
// Before 用 map 而不是结构体：各类型字段清单不同，且这里要断言**键集合完全一致**
// （多一个派生列、少一个业务列都算快照不合格）。
type ruleDeleteAuditPayload struct {
	Before map[string]any `json:"before"`
	Reason string         `json:"reason"`
}

// ruleDeleteCase 是一类规则的删除入口 + 判定所需的一切。
type ruleDeleteCase struct {
	name   string
	module string
	router *gin.Engine
	db     *gorm.DB
	path   string
	// count 返回该规则当前在库里的行数（删除后应为 0，被拒后应仍为 1）。
	count func(t *testing.T) int64
	// expectedSnapshot 返回**测试侧独立构造**的期望快照（从删除前的库行读出来）。
	expectedSnapshot func(t *testing.T) map[string]any
}

// ruleDeleteDo 发一次 DELETE。
func ruleDeleteDo(router *gin.Engine, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, target, nil))
	return recorder
}

// ruleDeleteLogRows 取某 module 下所有删除审计行（action = RuleActionDelete）。
func ruleDeleteLogRows(t *testing.T, db *gorm.DB, module string) []entity.Log {
	t.Helper()

	var rows []entity.Log
	if err := db.Where("module = ? AND action = ?", module, RuleActionDelete).
		Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("读取删除审计日志失败: %v", err)
	}
	return rows
}

// TestRuleDeleteWithoutReasonIsRejectedAndKeepsRule 钉「缺原因必须 400，且规则还在、日志没写」。
//
// 三类规则各一条，每种退化形态都试一遍：
// 完全不带 reason / 纯空格 / 只有 1 个字符。
func TestRuleDeleteWithoutReasonIsRejectedAndKeepsRule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []ruleDeleteCase{
		newRuleDeleteBlacklistCase(t),
		newRuleDeleteReviewRuleCase(t),
		newRuleDeletePaymentMethodCase(t),
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			probes := []struct {
				label  string
				target string
			}{
				{"no-reason", item.path},
				{"blank-reason", item.path + "?reason=" + ruleDeleteBlankReasonQuery},
				{"single-rune-reason", item.path + "?reason=" + ruleDeleteSingleRuneReasonQuery},
			}
			for _, probe := range probes {
				recorder := ruleDeleteDo(item.router, probe.target)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("%s: want 400, got %d (%s)", probe.label, recorder.Code, recorder.Body.String())
				}
				if !strings.Contains(recorder.Body.String(), ruleDeleteReasonRequiredMessage) {
					t.Fatalf("%s: reject body = %s, want message %q",
						probe.label, recorder.Body.String(), ruleDeleteReasonRequiredMessage)
				}
				// 规则必须还在：被 400 拒掉的请求不能有副作用。
				if got := item.count(t); got != 1 {
					t.Fatalf("%s: rule rows after rejected delete = %d, want 1", probe.label, got)
				}
				// 也不能留一行「空快照」审计。
				if rows := ruleDeleteLogRows(t, item.db, item.module); len(rows) != 0 {
					t.Fatalf("%s: rejected delete wrote %d audit rows (first=%+v)",
						probe.label, len(rows), rows[0])
				}
			}
		})
	}
}

// TestRuleDeleteWithReasonWritesRebuildableSnapshot 钉「先写完整快照再物理删除」。
//
// 三类规则各一条：合法 reason → 200 + 规则消失 + logs 恰好一行 + OldAnswer 能还原全部字段。
func TestRuleDeleteWithReasonWritesRebuildableSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []ruleDeleteCase{
		newRuleDeleteBlacklistCase(t),
		newRuleDeleteReviewRuleCase(t),
		newRuleDeletePaymentMethodCase(t),
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			// 期望快照必须在删除前算：删除后那一行就查不到了 —— 这正是
			// 「先删后记」这个错误写法唯一能骗过自己的地方。
			want := item.expectedSnapshot(t)

			recorder := ruleDeleteDo(item.router, item.path+"?reason="+ruleDeleteTestReasonQuery)
			if recorder.Code != http.StatusOK {
				t.Fatalf("delete with reason: want 200, got %d (%s)", recorder.Code, recorder.Body.String())
			}
			if got := item.count(t); got != 0 {
				t.Fatalf("rule rows after delete = %d, want 0", got)
			}

			rows := ruleDeleteLogRows(t, item.db, item.module)
			if len(rows) != 1 {
				t.Fatalf("audit rows = %d, want exactly 1", len(rows))
			}
			row := rows[0]
			if row.NewAnswer != "" {
				t.Fatalf("delete audit row must leave NewAnswer empty, got %q", row.NewAnswer)
			}
			if !strings.Contains(row.Description, ruleDeleteTestReason) {
				t.Fatalf("Description = %q, must carry the reason", row.Description)
			}
			if strings.TrimSpace(row.Username) == "" {
				t.Fatalf("audit row lost the operator username")
			}

			var payload ruleDeleteAuditPayload
			if err := json.Unmarshal([]byte(row.OldAnswer), &payload); err != nil {
				t.Fatalf("OldAnswer 不能反序列化（快照不可重建）: %v (%s)", err, row.OldAnswer)
			}
			if payload.Reason != ruleDeleteTestReason {
				t.Fatalf("snapshot reason = %q, want %q", payload.Reason, ruleDeleteTestReason)
			}
			if len(payload.Before) == 0 {
				t.Fatalf("snapshot before is empty — 删完才取快照就会这样: %s", row.OldAnswer)
			}
			// 键集合与取值都要完全对得上，否则「能重建」是空话。
			if !reflect.DeepEqual(payload.Before, want) {
				t.Fatalf("snapshot before 与删除前的库行不一致:\n got=%#v\nwant=%#v", payload.Before, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 三类规则的 fixture
// ---------------------------------------------------------------------------

// newRuleDeleteBlacklistCase 建一条黑名单规则（走真实创建接口，拿到真实的脱敏值）。
func newRuleDeleteBlacklistCase(t *testing.T) ruleDeleteCase {
	t.Helper()

	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)
	admin := blacklistSeedAdmin(t, db)
	setBlacklistAdminSwitch(t, db, true)
	router := blacklistAdminRouter(&admin)

	create := blacklistRequest(router, http.MethodPost, "/api/blacklist",
		`{"kind":"ip","pattern":"203.0.113.61","reason":"SYNTHETIC_RULE_DELETE_AUDIT"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("seed blacklist rule: %d %s", create.Code, create.Body.String())
	}
	var created entity.BlacklistEntry
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode blacklist rule: %v", err)
	}

	return ruleDeleteCase{
		name:   "blacklist",
		module: ruleDeleteModuleBlacklist,
		router: router,
		db:     db,
		path:   "/api/blacklist/" + strconv.FormatInt(created.ID, 10),
		count: func(t *testing.T) int64 {
			t.Helper()
			var rows int64
			if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", created.ID).Count(&rows).Error; err != nil {
				t.Fatalf("count blacklist rule: %v", err)
			}
			return rows
		},
		expectedSnapshot: func(t *testing.T) map[string]any {
			t.Helper()
			var entry entity.BlacklistEntry
			if err := db.Where("id = ?", created.ID).Take(&entry).Error; err != nil {
				t.Fatalf("reload blacklist rule: %v", err)
			}
			snapshot := map[string]any{
				"kind":      entry.Kind,
				"pattern":   entry.Pattern,
				"matchType": entry.MatchType,
				"reason":    entry.Reason,
				"enabled":   entry.Enabled,
				"createdBy": float64(entry.CreatedBy),
				"createdAt": formatRuleSnapshotTime(entry.CreatedAt),
				"expiresAt": nil,
			}
			if entry.ExpiresAt != nil {
				snapshot["expiresAt"] = formatRuleSnapshotTime(*entry.ExpiresAt)
			}
			return snapshot
		},
	}
}

// newRuleDeleteReviewRuleCase 建一个活动 + 一条规则（复用既有 seed，保持与线上字段一致）。
func newRuleDeleteReviewRuleCase(t *testing.T) ruleDeleteCase {
	t.Helper()

	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	_, seeded := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 643, "rule-delete-admin", "admin")
	installReviewRebateTestDB(t, db)
	router := reviewRebateControllerRouter(&admin)

	return ruleDeleteCase{
		name:   "review_rule",
		module: ruleDeleteModuleReviewRule,
		router: router,
		db:     db,
		path:   fmt.Sprintf("/admin/review-rebate/rules/%d", seeded.ID),
		count: func(t *testing.T) int64 {
			t.Helper()
			var rows int64
			if err := db.Model(&entity.ReviewRebateRule{}).Where("id = ?", seeded.ID).Count(&rows).Error; err != nil {
				t.Fatalf("count review rebate rule: %v", err)
			}
			return rows
		},
		expectedSnapshot: func(t *testing.T) map[string]any {
			t.Helper()
			var rule entity.ReviewRebateRule
			if err := db.Where("id = ?", seeded.ID).Take(&rule).Error; err != nil {
				t.Fatalf("reload review rebate rule: %v", err)
			}
			return map[string]any{
				"programId":       float64(rule.ProgramID),
				"minStars":        float64(rule.MinStars),
				"maxStars":        float64(rule.MaxStars),
				"minContentScore": float64(rule.MinContentScore),
				"maxContentScore": float64(rule.MaxContentScore),
				"amount":          rule.Amount,
				"priority":        float64(rule.Priority),
				"enabled":         rule.Enabled,
				"remark":          rule.Remark,
				"createdAt":       formatRuleSnapshotTime(rule.CreatedAt),
			}
		},
	}
}

// newRuleDeletePaymentMethodCase 建一个支付方式。
//
// deletePaymentMethod 是包级注入点，默认实现要 manager，单测里 manager 为 nil，
// 因此这里把它接到真实快照实现 + 测试库上：被删的仍是 deletePaymentMethodWithSnapshot，
// 变异「先删后写日志」同样会让本用例变红。
func newRuleDeletePaymentMethodCase(t *testing.T) ruleDeleteCase {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.PaymentMethod{}); err != nil {
		t.Fatalf("migrate payment_methods: %v", err)
	}
	method := entity.PaymentMethod{
		Name:     "rule-delete-fixture",
		IconSVG:  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="currentColor" d="M2 2h20v20H2z"/></svg>`,
		EPayType: "alipay",
		Enabled:  true,
		Sort:     61,
		Remark:   "SYNTHETIC_RULE_DELETE_AUDIT",
	}
	if err := db.Create(&method).Error; err != nil {
		t.Fatalf("seed payment method: %v", err)
	}
	admin := entity.User{
		ID: 9201, Username: "rule-delete-pay-admin", Password: "123456",
		Role: "admin", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("seed payment admin: %v", err)
	}

	original := deletePaymentMethod
	deletePaymentMethod = func(user entity.User, paymentMethodID int32, reason, clientIP string) error {
		return deletePaymentMethodWithSnapshot(db, paymentMethodID, user, reason, clientIP)
	}
	t.Cleanup(func() { deletePaymentMethod = original })

	router := gin.New()
	auth := func(c *gin.Context) { c.Set("user", &admin) }
	router.DELETE("/api/payment-methods/:id", auth, DeletePaymentMethod)

	return ruleDeleteCase{
		name:   "payment_method",
		module: ruleDeleteModulePaymentMethod,
		router: router,
		db:     db,
		path:   "/api/payment-methods/" + strconv.Itoa(int(method.ID)),
		count: func(t *testing.T) int64 {
			t.Helper()
			var rows int64
			if err := db.Model(&entity.PaymentMethod{}).Where("id = ?", method.ID).Count(&rows).Error; err != nil {
				t.Fatalf("count payment method: %v", err)
			}
			return rows
		},
		expectedSnapshot: func(t *testing.T) map[string]any {
			t.Helper()
			var stored entity.PaymentMethod
			if err := db.Where("id = ?", method.ID).Take(&stored).Error; err != nil {
				t.Fatalf("reload payment method: %v", err)
			}
			return map[string]any{
				"name":      stored.Name,
				"iconSvg":   stored.IconSVG,
				"epayType":  stored.EPayType,
				"enabled":   stored.Enabled,
				"sort":      float64(stored.Sort),
				"remark":    stored.Remark,
				"createdAt": formatRuleSnapshotTime(stored.CreatedAt),
			}
		},
	}
}
