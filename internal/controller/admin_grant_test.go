package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉住「赠送/回滚入口全部要求管理员」与「超阈值返回 428」两条契约。
//
// # 为什么这两条必须在这里测
//
// 服务层有自己的 isAdminUser 兜底，因此「普通用户调接口会不会被拒」在服务层
// 是测不出来的 —— 服务层拒了，controller 却可能返回 200（因为它先把错误吞了，
// 或者干脆没写 requireAdmin 而 currentOperator 恰好放行了一个 role=user 的会话）。
// 只有跑真实的 gin 上下文才能确认 **HTTP 层**的响应码。
//
// 同理 428：「需要二次确认」必须是一个**可编程区分**的状态码。用 400 的话
// 前端只能靠 message 文案判断，一旦文案改字，确认流程就静默失效。

// newAdminGrantControllerDB 复用 controller 包的测试库构造器（含全部所需表）。
func newAdminGrantControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return newUserControllerTestDB(t)
}

// adminGrantTestContext 构造一个带路径参数的 gin 上下文。
func adminGrantTestContext(t *testing.T, method, target string, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	if body == "" {
		ctx.Request = httptest.NewRequest(method, target, nil)
	} else {
		ctx.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	}
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.RemoteAddr = "198.51.100.9:1234"
	ctx.Params = params
	return ctx, recorder
}

// TestAdminGrantHandlerRequiresAdmin 逐个 handler 验「普通用户一律 403」。
//
// 表驱动，每个 handler 单独一行 —— 新增一个 handler 忘了加 requireAdmin 时，
// 这里会缺一行，而不是被别的行掩盖。
func TestAdminGrantHandlerRequiresAdmin(t *testing.T) {
	handlers := []struct {
		name string
		call func(*gin.Context)
		body string
	}{
		{"AdminGrant", AdminGrant, `{"amount":1}`},
		{"AdminGrantReverse", AdminGrantReverse, `{"amount":1}`},
		{"AdminGrantBatchPreview", AdminGrantBatchPreview, `{"selector":{"userIds":[2]},"amount":1}`},
		{"AdminGrantBatchExecute", AdminGrantBatchExecute, `{"selector":{"userIds":[2]},"amount":1,"confirmed":true}`},
		{"AdminGrantBatchRollback", AdminGrantBatchRollback, `{"reason":"x"}`},
		{"AdminGrantBatchProgress", AdminGrantBatchProgress, ""},
		{"AdminGrantBatchPause", AdminGrantBatchPause, ""},
		{"AdminGrantBatchAbort", AdminGrantBatchAbort, ""},
		{"AdminGrantConfirmThreshold", AdminGrantConfirmThreshold, ""},
	}
	for _, handler := range handlers {
		t.Run(handler.name, func(t *testing.T) {
			db := newAdminGrantControllerDB(t)
			ordinary := entity.User{ID: 2, Username: "ordinary", Role: "user", Status: "enabled"}
			if err := db.Create(&ordinary).Error; err != nil {
				t.Fatal(err)
			}
			ctx, recorder := adminGrantTestContext(t, http.MethodPost, "/admin/user/2/grant", handler.body,
				gin.Params{{Key: "id", Value: "2"}, {Key: "batchId", Value: "b1"}})
			ctx.Set("user", &ordinary)

			handler.call(ctx)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("%s must reject non-admin with 403, got %d %s",
					handler.name, recorder.Code, recorder.Body.String())
			}
			// 403 之后不能落任何流水（哪怕服务层会再拦一次，HTTP 层也不该往下走）。
			var bills int64
			if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
				t.Fatal(err)
			}
			if bills != 0 {
				t.Fatalf("%s wrote %d bills for a non-admin", handler.name, bills)
			}
		})
	}
}

// TestAdminGrantHandlerReturns428WhenConfirmationNeeded 钉住「超阈值 → 428」。
//
// 两段：先不确认拿 428，再确认拿 200 —— 只断前半段的话，一个「永远返回 428」
// 的实现也能通过。
func TestAdminGrantHandlerReturns428WhenConfirmationNeeded(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		name := "unconfirmed"
		if confirmed {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			db := newAdminGrantControllerDB(t)
			admin := entity.User{ID: 1, Username: "grant_admin", Role: "admin", Status: "enabled"}
			target := entity.User{ID: 2, Username: "grant_target", Role: "user", Status: "enabled"}
			if err := db.Create(&[]entity.User{admin, target}).Error; err != nil {
				t.Fatal(err)
			}

			payload, _ := json.Marshal(map[string]any{
				"amount": 20, "reason": "补偿", "confirmed": confirmed, "anchor": "single-handler",
			})
			ctx, recorder := adminGrantTestContext(t, http.MethodPost, "/admin/user/2/grant", string(payload),
				gin.Params{{Key: "id", Value: "2"}})
			ctx.Set("user", &admin)

			AdminGrant(ctx)

			want := adminGrantConfirmHTTPStatus
			if confirmed {
				want = http.StatusOK
			}
			if recorder.Code != want {
				t.Fatalf("confirmed=%t: status %d, want %d (%s)",
					confirmed, recorder.Code, want, recorder.Body.String())
			}
			if !confirmed {
				var body map[string]any
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				// 前端靠这个布尔决定弹不弹确认框，不能只靠 message。
				if body["confirmRequired"] != true {
					t.Fatalf("428 body must set confirmRequired=true, got %v", body)
				}
			}
		})
	}
}

// TestAdminGrantHandlerReportsShortfallColumns 钉住回滚接口的「应扣/实扣/差额」三列。
//
// 118-*.md §6.1 要求界面上明确显示这三列 —— 接口不返回，界面就只能显示应扣额，
// 而用户实际被扣的钱少于它，管理员会以为追缴失败。
func TestAdminGrantHandlerReportsShortfallColumns(t *testing.T) {
	db := newAdminGrantControllerDB(t)
	admin := entity.User{ID: 1, Username: "rev_admin", Role: "admin", Status: "enabled"}
	// 本金 50、赠金 7：应追缴 10 → 赠金扣 7、本金扣 3。
	target := entity.User{ID: 2, Username: "rev_target", Role: "user", Status: "enabled", Balance: 50, GiftBalance: 7}
	if err := db.Create(&[]entity.User{admin, target}).Error; err != nil {
		t.Fatal(err)
	}

	ctx, recorder := adminGrantTestContext(t, http.MethodPost, "/admin/user/2/grant-reversal",
		`{"amount":10,"anchor":"batch-handler","reason":"撤销"}`,
		gin.Params{{Key: "id", Value: "2"}})
	ctx.Set("user", &admin)

	AdminGrantReverse(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("reversal: %d %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["expected"] != 10.0 || body["fromGift"] != 7.0 || body["fromBalance"] != 3.0 || body["shortfall"] != 3.0 {
		t.Fatalf("handler must expose 应扣/实扣/差额: %v", body)
	}
	if body["balance"] != 47.0 || body["giftBalance"] != 0.0 {
		t.Fatalf("handler must expose the post-state buckets: %v", body)
	}
}
