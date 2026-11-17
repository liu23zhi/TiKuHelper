package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestCreateScriptRequiresBindingWithStableHTTPError(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "script-binding-fixture", "user")
	admin := seedControllerUser(t, db, 0, "script-binding-uploader", "admin")
	phone, wechat := "13800000000", "synthetic-script-wechat"
	if err := db.Model(&owner).Updates(map[string]any{"balance": 50, "phone": phone}).Error; err != nil {
		t.Fatal(err)
	}
	// The login projection is stale: both bindings are asserted by the caller
	// context, but the transaction's current account only has its phone.
	owner.Phone, owner.WechatID = &phone, &wechat
	version, err := service.UploadScriptVersionWithDB(db, admin, "fixture", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	router := gin.New()
	router.POST("/api/scripts", func(c *gin.Context) { c.Set("user", &owner) }, CreateScript)
	req := httptest.NewRequest(http.MethodPost, "/api/scripts", strings.NewReader(fmt.Sprintf(
		`{"name":"fixture","baseDomain":"fixture.test","versionId":%d}`, version.ID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "fixture-binding-create")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil ||
		len(payload) != 2 || payload["code"] != "SCRIPT_BINDINGS_REQUIRED" || payload["message"] != "创建脚本前请绑定手机号和微信" {
		t.Error("want only the stable business code and generic message")
	}
}

// ─────────────────────── 管理员手动跳过绑定门槛（2026-11-17） ───────────────────────

// TestCreateScriptAdminSkipBindingGate 钉住「仅管理员可手动跳过绑定门槛」。
//
// # 用户口径
//
//	「请添加一个按钮，在我的脚本里面，该按钮为仅管理员可见，用于跳过这一限制」
//
// # 改动前的现状
//
// 唯一放行条件是 `isAdminUser && c.Query("scope") == "all"` —— 管理员切到
// 「显示全部脚本」时才跳过。在「我的脚本」（scope=self）里创建自己的脚本
// 仍然被 403。本轮把这个手动开关交给管理员：加一个请求字段。
//
// # 为什么用请求字段而不是 query 参数
//
// 前端按钮点击是明确的一次性意图，塞在 body 里与其它表单字段同源；
// query 参数容易被「分享链接」之类路径意外带上，变成隐式放行。
//
// # 三条判据（第 2、3 条是对照组，缺了就漏掉两类错误实现）
//
//  1. 管理员显式 skip → 放行（功能）
//  2. **非管理员**显式 skip → 仍然 403（越权；否则任何人都能自己解禁）
//  3. 管理员**不传** → 仍然 403（不是「管理员一律放行」，那等于把门槛删了）
func TestCreateScriptAdminSkipBindingGate(t *testing.T) {
	// newBindingGateFixture 造一套「用户已绑手机但没绑微信」的场景 ——
	// 这正是现状下会被拦的形态（缺一边就够了）。
	newBindingGateFixture := func(t *testing.T) (*gorm.DB, entity.User, entity.User, int32) {
		t.Helper()
		db := newSearchControllerTestDB(t)
		if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
			t.Fatal(err)
		}
		owner := seedControllerUser(t, db, 0, "gate-owner", "user")
		admin := seedControllerUser(t, db, 0, "gate-admin", "admin")
		phone := "13800000001"
		if err := db.Model(&owner).Updates(map[string]any{"balance": 50, "phone": phone}).Error; err != nil {
			t.Fatal(err)
		}
		// 关键：只设 phone，WechatID 保持 nil —— 缺一边就会被门槛拦。
		owner.Phone = &phone
		owner.WechatID = nil
		version, err := service.UploadScriptVersionWithDB(db, admin, "gate", "gate.exe", []byte("MZgate"))
		if err != nil {
			t.Fatal(err)
		}
		previous := scriptDB
		scriptDB = func() *gorm.DB { return db }
		t.Cleanup(func() { scriptDB = previous })
		return db, owner, admin, version.ID
	}

	doCreate := func(t *testing.T, actor entity.User, body string, query string) *httptest.ResponseRecorder {
		t.Helper()
		router := gin.New()
		path := "/api/scripts"
		if query != "" {
			path += "?" + query
		}
		router.POST("/api/scripts", func(c *gin.Context) { c.Set("user", &actor) }, CreateScript)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "gate-"+actor.Username)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("管理员显式 skip → 放行", func(t *testing.T) {
		_, _, admin, versionID := newBindingGateFixture(t)
		rec := doCreate(t, admin, fmt.Sprintf(
			`{"name":"gate","baseDomain":"gate.test","versionId":%d,"skipBindingCheck":true}`, versionID), "")
		if rec.Code == http.StatusForbidden {
			t.Fatalf("管理员显式跳过绑定门槛仍被 403：body=%s。"+
				"这正是本次要修的行为 —— 按钮点了却没用", rec.Body.String())
		}
	})

	t.Run("非管理员显式 skip → 仍然 403（越权对照组）", func(t *testing.T) {
		_, owner, _, versionID := newBindingGateFixture(t)
		rec := doCreate(t, owner, fmt.Sprintf(
			`{"name":"gate","baseDomain":"gate.test","versionId":%d,"skipBindingCheck":true}`, versionID), "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("普通用户传 skipBindingCheck=true 必须仍然 403（否则任何人可自行解禁），实际 %d body=%s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("管理员不传 → 仍然 403（不是一律放行）", func(t *testing.T) {
		_, _, admin, versionID := newBindingGateFixture(t)
		rec := doCreate(t, admin, fmt.Sprintf(
			`{"name":"gate","baseDomain":"gate.test","versionId":%d}`, versionID), "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("管理员不显式跳过时必须仍然被拦（门槛还在，只是可手动跳过），实际 %d body=%s",
				rec.Code, rec.Body.String())
		}
	})
}
