package controller

// 「我的设备」用户端接口的回归测试（137-设备体系设计 §3.3 / §4 / §8.1 / §9.1，批次 A2）。
//
// ## 每条用例对应哪条红线（不是「覆盖率」，是「这条断言能抓住哪种实现错误」）
//
//	红线 1  归属校验、越权 404：TestDeviceUserListOnlyOwnDevices、
//	        TestDeviceUserRemoveBlocksCrossTenantAccess、
//	        TestDeviceUserBlockRequiresOwnership、TestDeviceUserUnblockRequiresOwnership
//	红线 2  移除=软撤销不删行：TestDeviceUserRemoveSoftRevokesBindingRow
//	红线 3  拉黑用独立表 + 解除=删行：TestDeviceUserBlockWritesIndependentTable、
//	        TestDeviceUserUnblockDeletesRow（D7 变异目标）
//	红线 4  自设上限只能降不能升：TestDeviceUserLimitRejectsAboveLevel（D8 变异目标）
//	A2-1    列表含已撤销与已拉黑：TestDeviceUserListMarksRevokedAndBlocked（D6 变异目标）
//
// ## 为什么不断言 EffectiveDeviceLimit 的具体数值
//
// 本分支上它是**临时桩**（internal/service/device_limit_stub.go，STUB-followup），
// 合并后会换成批次 A1 的正式实现，其返回值取决于等级配置。断言 `limit == 0`
// 会在合并后必然变红并被误判成「A2 坏了」。所以这里只断言**结构与口径**：
// `limit` 字段存在且等于我们故意设置的那个 override、`source` 非空、
// `upperBound` 来自等级值 L。这些在任何一份实现下都成立。
//
// ## 为什么用表级 ALTER 造「A1 那 5 列」而不是 AutoMigrate 实体
//
// devA2 分支的 entity.AccountDeviceBindings.go 还没有 A1 的 5 列（那是 A1 的
// 独占文件）。本测试显式 ALTER 出来，正是**模拟 A1 迁移跑完之后**的库状态 ——
// 也正是本组接口在生产上的目标形态。另有一条用例专门覆盖「列还没补」的降级态。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var deviceUserTestDBSeq atomic.Int64

// newDeviceUserTestDB 建一个带 users + 等级 + 绑定表 + 拉黑表的最小库。
//
// withA1Columns=false 时**不**补 A1 的 5 列，用于覆盖「迁移未跑」的降级态。
func newDeviceUserTestDB(t *testing.T, withA1Columns bool) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:device-user-%s-%d?mode=memory&cache=shared", name, deviceUserTestDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{},
		&entity.AccountDeviceBinding{}, &entity.UserDeviceBlock{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	// users.level_id 不在生成文件里（由 registry 的显式 ALTER 补），这里照做。
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	// users.device_limit_override（137 §3.1）：A1 的迁移负责，本测试显式补上
	// —— PUT 接口在缺列时返回 503，那是另一条用例的事。
	if !db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN device_limit_override integer NOT NULL DEFAULT -1").Error; err != nil {
			t.Fatalf("add device_limit_override: %v", err)
		}
	}
	if withA1Columns {
		for _, ddl := range []string{
			"device_label VARCHAR(64) NOT NULL DEFAULT ''",
			"revoked_at DATETIME NULL",
			"revoked_reason VARCHAR(32) NOT NULL DEFAULT ''",
			// last_ip 是第三批新增列（「我的设备」显示来源 IP）。
			// 与前三列一起补，因为「A1 列就绪」在生产上意味着「迁移全跑完了」。
			"last_ip VARCHAR(45) NOT NULL DEFAULT ''",
		} {
			column := strings.Fields(ddl)[0]
			if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
				continue
			}
			if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
				" ADD COLUMN " + ddl).Error; err != nil {
				t.Fatalf("add binding column %s: %v", column, err)
			}
		}
	}

	original := deviceUserDBOverride
	deviceUserDBOverride = db
	t.Cleanup(func() { deviceUserDBOverride = original })
	return db
}

func seedDeviceUserUser(t *testing.T, db *gorm.DB, username string, levelID int32, override int32) *entity.User {
	t.Helper()
	user := entity.User{Username: username, Role: "user", Status: "enabled", RegisterSource: "system"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	if levelID > 0 {
		if err := db.Table(entity.TableNameUser).Where("id = ?", user.ID).
			Update("level_id", levelID).Error; err != nil {
			t.Fatalf("attach level: %v", err)
		}
	}
	if override != -1 {
		if err := db.Table(entity.TableNameUser).Where("id = ?", user.ID).
			Update("device_limit_override", override).Error; err != nil {
			t.Fatalf("set override: %v", err)
		}
	}
	return &user
}

func seedDeviceUserLevel(t *testing.T, db *gorm.DB, rank int32, deviceLimit int32) int32 {
	t.Helper()
	level := entity.UserLevel{
		Name: fmt.Sprintf("设备档-%d-%d", rank, deviceLimit), Rank: rank,
		Enabled: true, DeviceLimit: deviceLimit,
	}
	if err := db.Create(&level).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	return level.ID
}

// seedDeviceUserBinding 插一行绑定。revokedReason 非空 ⇒ 视为已撤销。
func seedDeviceUserBinding(t *testing.T, db *gorm.DB, userID int32, deviceID, label string,
	lastSeen time.Time, revokedReason string, revokedAt *time.Time) int64 {
	t.Helper()
	row := entity.AccountDeviceBinding{
		UserID: userID, DeviceID: deviceID,
		FirstSeenAt: lastSeen, LastSeenAt: lastSeen,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding %s: %v", deviceID, err)
	}
	updates := map[string]any{"device_label": label}
	if revokedReason != "" {
		updates["revoked_reason"] = revokedReason
		if revokedAt == nil {
			now := time.Now()
			revokedAt = &now
		}
		updates["revoked_at"] = *revokedAt
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		t.Fatalf("patch binding %s: %v", deviceID, err)
	}
	return row.ID
}

// deviceUserRouter 组装本组接口的路由（身份由中间件注入，模拟 controller.Auth 的效果）。
func deviceUserRouter(operator *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if operator != nil {
			c.Set("user", operator)
		}
	})
	group := router.Group("/api")
	group.GET("/user/devices", UserDevices)
	group.POST("/user/devices/:id/remove", UserDeviceRemove)
	group.DELETE("/user/devices/:id", UserDeviceDelete)
	group.PUT("/user/devices/:id/label", UserDeviceRename)
	group.POST("/user/devices/:id/block", UserDeviceBlock)
	group.DELETE("/user/devices/blocks/:id", UserDeviceUnblock)
	group.GET("/user/device-limit", UserDeviceLimitGet)
	group.PUT("/user/device-limit", UserDeviceLimitPut)
	group.GET("/user/device-expiry", UserDeviceExpiryGet)
	group.PUT("/user/device-expiry", UserDeviceExpiryPut)
	return router
}

func TestUserDeviceDeleteOnlyRemovesOwnRevokedBinding(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-delete-owner", 0, -1)
	other := seedDeviceUserUser(t, db, "devuser-delete-other", 0, -1)
	now := time.Now()
	revokedID := seedDeviceUserBinding(t, db, owner.ID, "extension:delete-revoked", "旧设备", now, "user_removed", &now)
	activeID := seedDeviceUserBinding(t, db, owner.ID, "extension:delete-active", "在用设备", now, "", nil)
	otherID := seedDeviceUserBinding(t, db, other.ID, "extension:delete-other", "别人的设备", now, "user_removed", &now)

	router := deviceUserRouter(owner)
	res, _ := doDeviceUser(t, router, http.MethodDelete, fmt.Sprintf("/api/user/devices/%d", revokedID), nil)
	if res.Code != http.StatusOK {
		t.Fatalf("已移除设备应允许删除，实际 %d：%s", res.Code, res.Body.String())
	}
	var count int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).Where("id = ?", revokedID).Count(&count).Error; err != nil {
		t.Fatalf("count deleted binding: %v", err)
	}
	if count != 0 {
		t.Fatal("已移除设备删除后不应保留绑定行")
	}

	active, _ := doDeviceUser(t, router, http.MethodDelete, fmt.Sprintf("/api/user/devices/%d", activeID), nil)
	if active.Code != http.StatusConflict {
		t.Fatalf("在用设备删除应返回 409，实际 %d：%s", active.Code, active.Body.String())
	}
	otherRes, _ := doDeviceUser(t, router, http.MethodDelete, fmt.Sprintf("/api/user/devices/%d", otherID), nil)
	if otherRes.Code != http.StatusNotFound {
		t.Fatalf("删除他人设备应返回 404，实际 %d：%s", otherRes.Code, otherRes.Body.String())
	}
}

func TestUserDeviceExpiryRejectsAboveAdminAndZeroWithoutPermission(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: service.DeviceIdleRevokeAfterDaysSettingKey, Value: "3"}).Error; err != nil {
		t.Fatal(err)
	}
	owner := seedDeviceUserUser(t, db, "devuser-expiry-owner", 0, -1)
	router := deviceUserRouter(owner)
	for _, days := range []int{-1, 0, 4} {
		res, _ := doDeviceUser(t, router, http.MethodPut, "/api/user/device-expiry", map[string]any{"days": days})
		if res.Code != http.StatusBadRequest {
			t.Fatalf("days=%d 应被拒绝，实际 %d %s", days, res.Code, res.Body.String())
		}
	}
	res, envelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-expiry", map[string]any{"days": 2})
	if res.Code != http.StatusOK || envelope.Data["days"] != float64(2) || envelope.Data["maxDays"] != float64(3) {
		t.Fatalf("允许的个人时长应保存并回填：%d %s", res.Code, res.Body.String())
	}
	if err := db.Create(&entity.SystemSetting{Key: service.DeviceIdleRevokeAllowUserDisableSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	res, envelope = doDeviceUser(t, router, http.MethodPut, "/api/user/device-expiry", map[string]any{"days": 0})
	if res.Code != http.StatusOK || envelope.Data["days"] != float64(0) {
		t.Fatalf("管理员允许后用户应能设 0：%d %s", res.Code, res.Body.String())
	}
}

func TestUserDeviceDeletePreservesBindingBlockedBeforeDeleteStatement(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-delete-race", 0, -1)
	now := time.Now()
	id := seedDeviceUserBinding(t, db, owner.ID, "race-block", "旧设备", now, "user_removed", &now)
	called := false
	if err := db.Callback().Delete().Before("gorm:delete").Register("test:block-before-delete", func(tx *gorm.DB) {
		if called {
			return
		}
		called = true
		if err := tx.Exec("INSERT INTO user_device_blocks (user_id, device_id, reason, created_at) VALUES (?, ?, ?, ?)", owner.ID, "race-block", "race", now).Error; err != nil {
			t.Errorf("insert concurrent block: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	res, _ := doDeviceUser(t, deviceUserRouter(owner), http.MethodDelete, fmt.Sprintf("/api/user/devices/%d", id), nil)
	if res.Code != http.StatusConflict {
		t.Fatalf("删除前被拉黑必须返回 409：%d %s", res.Code, res.Body.String())
	}
	var count int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).Where("id = ?", id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("并发拉黑的设备历史不得被删除")
	}
}

type deviceUserEnvelope struct {
	Data    map[string]any `json:"data"`
	Message string         `json:"message"`
}

// doDeviceUser 发一次请求并解出响应体（JSON 解析失败直接 fail，不静默返回空 map）。
func doDeviceUser(t *testing.T, router *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, deviceUserEnvelope) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var envelope deviceUserEnvelope
	if trimmed := strings.TrimSpace(recorder.Body.String()); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			t.Fatalf("decode response %s %s: %v (body=%s)", method, path, err, trimmed)
		}
	}
	return recorder, envelope
}

// deviceUserItems 从 data.devices 取出条目并断言类型（类型错了要立刻报，不要静默空切片）。
func deviceUserItems(t *testing.T, envelope deviceUserEnvelope) []map[string]any {
	t.Helper()
	raw, ok := envelope.Data["devices"]
	if !ok {
		t.Fatalf("响应缺少 devices 字段：%+v", envelope.Data)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("devices 必须是数组，实际 %T", raw)
	}
	items := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("devices 的元素必须是对象，实际 %T", entry)
		}
		items = append(items, item)
	}
	return items
}

func deviceUserItemByDeviceID(t *testing.T, items []map[string]any, deviceID string) map[string]any {
	t.Helper()
	for _, item := range items {
		if text, _ := item["deviceId"].(string); text == deviceID {
			return item
		}
	}
	t.Fatalf("列表里没有设备 %q（实际 %d 条）", deviceID, len(items))
	return nil
}

// ─────────────────────── A2-1 + 红线 1 ───────────────────────

// TestDeviceUserListOnlyOwnDevices：只返回自己的设备，且含已撤销（A2-1 / 红线 1）。
//
// 变异方向：把 `Where("user_id = ?")` 去掉 → 别人的设备出现在列表里 → 必红。
// 这是越权面最直接的形态：下拉列表里就能看到别人的设备指纹。
func TestDeviceUserListOnlyOwnDevices(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-owner", 0, -1)
	other := seedDeviceUserUser(t, db, "devuser-other", 0, -1)
	now := time.Now()
	seedDeviceUserBinding(t, db, owner.ID, "extension:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Chrome on Windows", now, "", nil)
	seedDeviceUserBinding(t, db, other.ID, "extension:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "别人的设备", now, "", nil)

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	items := deviceUserItems(t, envelope)
	if len(items) != 1 {
		t.Fatalf("只应返回自己的 1 台设备，实际 %d 台：%+v", len(items), items)
	}
	if got, _ := items[0]["deviceId"].(string); got != "extension:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("返回了别的设备：%q", got)
	}
	if text, _ := items[0]["deviceLabel"].(string); text != "Chrome on Windows" {
		t.Fatalf("设备标签必须透传，实际 %q", text)
	}
	if status, _ := items[0]["status"].(string); status != "active" {
		t.Fatalf("未撤销未拉黑必须是 active，实际 %q", status)
	}
	if text, _ := items[0]["statusText"].(string); text != "使用中" {
		t.Fatalf("状态文案必须是「使用中」，实际 %q", text)
	}
	if active, ok := envelope.Data["active"].(float64); !ok || int(active) != 1 {
		t.Fatalf("active 统计必须是 1，实际 %v", envelope.Data["active"])
	}
}

// TestDeviceUserListMarksRevokedAndBlocked：已撤销标「已移除」、已拉黑标「已拉黑」
// （A2-1 / D6 变异目标）。
//
// 变异方向（D6）：去掉对 user_device_blocks 的查询（deviceUserLoadBlocks 直接返回
// 空 map）→ 已拉黑的行会退化成「使用中」→ 必红。
//
// ⚠️ 这条用例故意让同一台设备**既已撤销又已拉黑**：拉黑必须压过撤销。
// 若实现写成 `if revoked { removed } else if blocked { blocked }`，
// 用户看到的会是「已移除」，于是找不到解除入口（界面上已移除的行没有拉黑按钮）——
// 一个用户自锁死、且必须找客服才能解开的形态。
func TestDeviceUserListMarksRevokedAndBlocked(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-marks", 0, -1)
	now := time.Now()
	removedAt := now.Add(-48 * time.Hour)

	seedDeviceUserBinding(t, db, owner.ID, "extension:11111111111111111111111111111111", "在用设备", now, "", nil)
	seedDeviceUserBinding(t, db, owner.ID, "extension:22222222222222222222222222222222", "已移除设备", now, "user_removed", &removedAt)
	seedDeviceUserBinding(t, db, owner.ID, "extension:33333333333333333333333333333333", "已拉黑设备", now, "user_removed", &removedAt)

	block := entity.UserDeviceBlock{
		UserID: owner.ID, DeviceID: "extension:33333333333333333333333333333333", Reason: "公共电脑",
	}
	if err := db.Create(&block).Error; err != nil {
		t.Fatalf("create block: %v", err)
	}

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	items := deviceUserItems(t, envelope)
	if len(items) != 3 {
		t.Fatalf("三台设备都要出现（含已移除/已拉黑），实际 %d 台", len(items))
	}

	active := deviceUserItemByDeviceID(t, items, "extension:11111111111111111111111111111111")
	if status, _ := active["status"].(string); status != "active" {
		t.Fatalf("在用设备状态必须是 active，实际 %q", status)
	}
	if removable, _ := active["removable"].(bool); !removable {
		t.Fatal("在用设备必须可移除")
	}

	removed := deviceUserItemByDeviceID(t, items, "extension:22222222222222222222222222222222")
	if status, _ := removed["status"].(string); status != "removed" {
		t.Fatalf("已撤销设备状态必须是 removed，实际 %q", status)
	}
	if text, _ := removed["statusText"].(string); text != "已移除" {
		t.Fatalf("已撤销设备文案必须是「已移除」，实际 %q", text)
	}
	if reason, _ := removed["revokedReason"].(string); reason != "user_removed" {
		t.Fatalf("撤销原因必须透传 user_removed，实际 %q", reason)
	}
	if removable, _ := removed["removable"].(bool); removable {
		t.Fatal("已撤销设备不该再可移除（重复撤销会覆盖历史原因）")
	}

	blocked := deviceUserItemByDeviceID(t, items, "extension:33333333333333333333333333333333")
	if status, _ := blocked["status"].(string); status != "blocked" {
		t.Fatalf("同时已撤销又已拉黑时必须显示 blocked（否则用户找不到解除入口），实际 %q", status)
	}
	if text, _ := blocked["statusText"].(string); text != "已拉黑" {
		t.Fatalf("文案必须是「已拉黑」，实际 %q", text)
	}
	if isBlocked, _ := blocked["blocked"].(bool); !isBlocked {
		t.Fatal("blocked 标记必须为 true")
	}
	if blockID, _ := blocked["blockId"].(float64); blockID <= 0 {
		t.Fatalf("必须给出解除拉黑要用的 blockId，实际 %v", blocked["blockId"])
	}

	if count, ok := envelope.Data["blocked"].(float64); !ok || int(count) != 1 {
		t.Fatalf("blocked 统计必须是 1，实际 %v", envelope.Data["blocked"])
	}
}

// TestDeviceUserListIncludesOrphanBlock：只有拉黑记录、没有绑定行的设备也要出现。
//
// 不出现的后果：用户看得到「已拉黑」，却没有任何行可以点「解除」——
// 只能找客服手工删库。这类孤儿行来自「绑定行被管理员物理删除」或历史数据。
func TestDeviceUserListIncludesOrphanBlock(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-orphan", 0, -1)
	block := entity.UserDeviceBlock{UserID: owner.ID, DeviceID: "extension:99999999999999999999999999999999"}
	if err := db.Create(&block).Error; err != nil {
		t.Fatalf("create block: %v", err)
	}

	router := deviceUserRouter(owner)
	_, envelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	items := deviceUserItems(t, envelope)
	if len(items) != 1 {
		t.Fatalf("孤儿拉黑记录必须出现在列表里，实际 %d 条", len(items))
	}
	if blockOnly, _ := items[0]["blockOnly"].(bool); !blockOnly {
		t.Fatal("孤儿拉黑记录必须标记 blockOnly=true（界面据此隐藏「移除」按钮）")
	}
	if blockID, _ := items[0]["blockId"].(float64); blockID <= 0 {
		t.Fatalf("孤儿行也必须给出 blockId（否则无法解除），实际 %v", items[0]["blockId"])
	}
}

// TestDeviceUserListDegradesWithoutA1Columns：A1 的 5 列还没补时，列表仍要能工作。
//
// 这是部署顺序问题：代码先上、迁移后跑（或迁移失败）。此时退化为旧行为
// （没有撤销概念，全部「使用中」），而不是 500 —— 否则「迁移没跑完」会让
// 用户彻底看不到自己的设备，而设备池本身那时也仍然是不限制的降级态。
func TestDeviceUserListDegradesWithoutA1Columns(t *testing.T) {
	db := newDeviceUserTestDB(t, false)
	owner := seedDeviceUserUser(t, db, "devuser-degraded", 0, -1)
	now := time.Now()
	row := entity.AccountDeviceBinding{
		UserID: owner.ID, DeviceID: "extension:dddddddddddddddddddddddddddddddd",
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("缺列时必须降级为 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	items := deviceUserItems(t, envelope)
	if len(items) != 1 {
		t.Fatalf("降级态也要返回设备，实际 %d 条", len(items))
	}
	if status, _ := items[0]["status"].(string); status != "active" {
		t.Fatalf("缺撤销列时一律视为 active，实际 %q", status)
	}
	if label, _ := items[0]["deviceLabel"].(string); label != "" {
		t.Fatalf("缺 device_label 列时标签必须是空串，实际 %q", label)
	}
}

// ─────────────────────── A2-2 + 红线 2 ───────────────────────

// TestDeviceUserRemoveSoftRevokesBindingRow：移除写 revoked_at，**行不删**（红线 2）。
//
// 变异方向：把 Updates 改成 Delete → 行数变 0 → 必红（这正是红线 2 要拦的）。
// 另一条变异：revoked_reason 写成 'user_blocked'（复用绑定行表示拉黑）→ 必红。
func TestDeviceUserRemoveSoftRevokesBindingRow(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-remove", 0, -1)
	now := time.Now()
	bindingID := seedDeviceUserBinding(t, db, owner.ID, "extension:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		"要移除的设备", now, "", nil)

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodPost,
		fmt.Sprintf("/api/user/devices/%d/remove", bindingID), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("移除应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if status, _ := envelope.Data["status"].(string); status != "removed" {
		t.Fatalf("响应状态必须是 removed，实际 %q", status)
	}
	if reason, _ := envelope.Data["revokedReason"].(string); reason != "user_removed" {
		t.Fatalf("撤销原因必须是 user_removed，实际 %q", reason)
	}

	// 红线 2：行必须还在，且只多出 revoked 信息。
	var rows []entity.AccountDeviceBinding
	if err := db.Where("user_id = ?", owner.ID).Find(&rows).Error; err != nil {
		t.Fatalf("read bindings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("移除必须只做软撤销，绑定行不能被删除；实际剩 %d 行", len(rows))
	}
	var stored struct {
		RevokedAt     *time.Time
		RevokedReason string
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at, revoked_reason AS revoked_reason").
		Where("id = ?", bindingID).Take(&stored).Error; err != nil {
		t.Fatalf("read revoked state: %v", err)
	}
	if stored.RevokedAt == nil {
		t.Fatal("移除后 revoked_at 必须非空")
	}
	if stored.RevokedReason != "user_removed" {
		t.Fatalf("库里的 revoked_reason 必须是 user_removed，实际 %q", stored.RevokedReason)
	}

	// 幂等：再点一次仍是 200 + alreadyRevoked，且**不覆盖**原时间。
	originalRevokedAt := *stored.RevokedAt
	time.Sleep(5 * time.Millisecond)
	second, secondEnvelope := doDeviceUser(t, router, http.MethodPost,
		fmt.Sprintf("/api/user/devices/%d/remove", bindingID), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("重复移除应幂等成功，实际 %d：%s", second.Code, second.Body.String())
	}
	if already, _ := secondEnvelope.Data["alreadyRevoked"].(bool); !already {
		t.Fatal("重复移除必须回报 alreadyRevoked=true")
	}
	var afterAnyway struct {
		RevokedAt *time.Time
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at").Where("id = ?", bindingID).Take(&afterAnyway).Error; err != nil {
		t.Fatalf("re-read revoked_at: %v", err)
	}
	if afterAnyway.RevokedAt == nil || !afterAnyway.RevokedAt.Equal(originalRevokedAt) {
		t.Fatalf("重复移除不得改写原撤销时间（历史会被静默篡改）：%v → %v",
			originalRevokedAt, afterAnyway.RevokedAt)
	}
}

// TestDeviceUserRemoveBlocksCrossTenantAccess：越权移除必须 404 且**不改任何行**（红线 1）。
func TestDeviceUserRemoveBlocksCrossTenantAccess(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-rm-owner", 0, -1)
	attacker := seedDeviceUserUser(t, db, "devuser-rm-attacker", 0, -1)
	now := time.Now()
	victimBindingID := seedDeviceUserBinding(t, db, owner.ID,
		"extension:ffffffffffffffffffffffffffffffff", "别人的设备", now, "", nil)

	router := deviceUserRouter(attacker)
	recorder, envelope := doDeviceUser(t, router, http.MethodPost,
		fmt.Sprintf("/api/user/devices/%d/remove", victimBindingID), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("越权移除必须 404（不泄露存在性），实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if envelope.Message != "设备记录不存在" {
		t.Fatalf("404 文案必须是中性的「设备记录不存在」，实际 %q", envelope.Message)
	}

	var stored struct {
		RevokedAt *time.Time
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at").Where("id = ?", victimBindingID).Take(&stored).Error; err != nil {
		t.Fatalf("read victim binding: %v", err)
	}
	if stored.RevokedAt != nil {
		t.Fatal("越权请求不得改动他人的绑定行")
	}

	// 不存在的 id 与「不是你的 id」必须是**同一个**响应（不可区分）。
	missing, missingEnvelope := doDeviceUser(t, router, http.MethodPost, "/api/user/devices/999999/remove", nil)
	if missing.Code != recorder.Code || missingEnvelope.Message != envelope.Message {
		t.Fatalf("「不存在」与「非本人」必须同形：%d/%v vs %d/%v",
			missing.Code, missingEnvelope.Message, recorder.Code, envelope.Message)
	}
}

// ─────────────────────── A2-4 + 红线 3 ───────────────────────

// TestDeviceUserBlockWritesIndependentTable：拉黑写独立表，且**不动绑定行**（红线 3）。
//
// 断言三件事，各自对应一种实现错误：
//  1. user_device_blocks 多一行（而不是在绑定行上写 revoked_reason）；
//  2. 绑定行的 revoked_at 仍为 NULL（拉黑 ≠ 撤销，撤销会释放槽位）；
//  3. 重复拉黑幂等（唯一索引 + OnConflict），不产生第二行。
func TestDeviceUserBlockWritesIndependentTable(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-block", 0, -1)
	now := time.Now()
	deviceID := "extension:44444444444444444444444444444444"
	bindingID := seedDeviceUserBinding(t, db, owner.ID, deviceID, "公司电脑", now, "", nil)

	router := deviceUserRouter(owner)
	path := fmt.Sprintf("/api/user/devices/%d/block", bindingID)
	recorder, envelope := doDeviceUser(t, router, http.MethodPost, path, map[string]any{"reason": "公共电脑"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("拉黑应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if status, _ := envelope.Data["status"].(string); status != "blocked" {
		t.Fatalf("响应状态必须是 blocked，实际 %q", status)
	}

	var blocks []entity.UserDeviceBlock
	if err := db.Where("user_id = ?", owner.ID).Find(&blocks).Error; err != nil {
		t.Fatalf("read blocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("拉黑必须写 user_device_blocks 一行，实际 %d 行", len(blocks))
	}
	if blocks[0].DeviceID != deviceID {
		t.Fatalf("拉黑行的 device_id 必须是归一化后的设备指纹：%q", blocks[0].DeviceID)
	}
	if blocks[0].Reason != "公共电脑" {
		t.Fatalf("拉黑原因必须落库，实际 %q", blocks[0].Reason)
	}

	// 红线 3：绑定行不得被撤销（撤销会释放槽位，语义完全不同）。
	var stored struct {
		RevokedAt     *time.Time
		RevokedReason string
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at, revoked_reason AS revoked_reason").
		Where("id = ?", bindingID).Take(&stored).Error; err != nil {
		t.Fatalf("read binding state: %v", err)
	}
	if stored.RevokedAt != nil || stored.RevokedReason != "" {
		t.Fatalf("拉黑不得改写绑定行的撤销字段：revokedAt=%v reason=%q",
			stored.RevokedAt, stored.RevokedReason)
	}

	// 幂等：再拉黑一次仍是 200，且不多行。
	second, _ := doDeviceUser(t, router, http.MethodPost, path, map[string]any{"reason": "again"})
	if second.Code != http.StatusOK {
		t.Fatalf("重复拉黑应幂等成功，实际 %d：%s", second.Code, second.Body.String())
	}
	var count int64
	if err := db.Table(entity.TableNameUserDeviceBlock).
		Where("user_id = ? AND device_id = ?", owner.ID, deviceID).Count(&count).Error; err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if count != 1 {
		t.Fatalf("(user_id, device_id) 必须唯一，实际 %d 行", count)
	}
}

// TestDeviceUserBlockRequiresOwnership：拉黑别人的设备必须 404 且不写行（红线 1）。
func TestDeviceUserBlockRequiresOwnership(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-blk-owner", 0, -1)
	attacker := seedDeviceUserUser(t, db, "devuser-blk-attacker", 0, -1)
	now := time.Now()
	victimID := seedDeviceUserBinding(t, db, owner.ID,
		"extension:55555555555555555555555555555555", "别人的设备", now, "", nil)

	router := deviceUserRouter(attacker)
	recorder, _ := doDeviceUser(t, router, http.MethodPost,
		fmt.Sprintf("/api/user/devices/%d/block", victimID), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("越权拉黑必须 404，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var count int64
	if err := db.Table(entity.TableNameUserDeviceBlock).Count(&count).Error; err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if count != 0 {
		t.Fatalf("越权请求不得写入任何拉黑行，实际 %d 行", count)
	}
}

// TestDeviceUserUnblockDeletesRow：解除拉黑必须**删行**（红线 3 / D7 变异目标）。
//
// 变异方向（D7）：把 Delete 换成「只改状态/清空 reason」的 UPDATE，
// 或干脆不执行 → 行仍在 → 本条必红。
//
// 为什么必须钉「行数归零」而不是「接口返回 200」：137 §5 第 2 步的判定
// **只看行是否存在**（A1 的 devicePoolUserBlocked 就是 Count>0）。
// 目标接口返回成功但行还在，用户看到「已解除」，下次请求却仍然被 403，
// 而且没有任何报错 —— 这正是 D7 描述的缺陷形态。
func TestDeviceUserUnblockDeletesRow(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-unblock", 0, -1)
	deviceID := "extension:66666666666666666666666666666666"
	block := entity.UserDeviceBlock{UserID: owner.ID, DeviceID: deviceID, Reason: "公共电脑"}
	if err := db.Create(&block).Error; err != nil {
		t.Fatalf("create block: %v", err)
	}

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodDelete,
		fmt.Sprintf("/api/user/devices/blocks/%d", block.ID), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("解除拉黑应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if removed, _ := envelope.Data["removed"].(bool); !removed {
		t.Fatal("响应必须说明记录已删除（removed=true）")
	}

	var count int64
	if err := db.Table(entity.TableNameUserDeviceBlock).
		Where("user_id = ? AND device_id = ?", owner.ID, deviceID).Count(&count).Error; err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if count != 0 {
		t.Fatalf("解除拉黑必须删除该行（判定只认行是否存在），实际仍剩 %d 行", count)
	}

	// 解除后列表里不该再有「已拉黑」。
	_, listEnvelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	items := deviceUserItems(t, listEnvelope)
	for _, item := range items {
		if isBlocked, _ := item["blocked"].(bool); isBlocked {
			t.Fatalf("解除拉黑后列表不该再标记 blocked：%+v", item)
		}
	}
}

// TestDeviceUserUnblockRequiresOwnership：解除别人的拉黑记录必须 404 且行仍在（红线 1）。
func TestDeviceUserUnblockRequiresOwnership(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-unb-owner", 0, -1)
	attacker := seedDeviceUserUser(t, db, "devuser-unb-attacker", 0, -1)
	block := entity.UserDeviceBlock{UserID: owner.ID, DeviceID: "extension:77777777777777777777777777777777"}
	if err := db.Create(&block).Error; err != nil {
		t.Fatalf("create block: %v", err)
	}

	router := deviceUserRouter(attacker)
	recorder, _ := doDeviceUser(t, router, http.MethodDelete,
		fmt.Sprintf("/api/user/devices/blocks/%d", block.ID), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("越权解除必须 404，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var count int64
	if err := db.Table(entity.TableNameUserDeviceBlock).Where("id = ?", block.ID).Count(&count).Error; err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if count != 1 {
		t.Fatalf("越权请求不得删除他人的拉黑记录，实际剩 %d 行", count)
	}
}

// ─────────────────────── A2-3 + 红线 4 ───────────────────────

// TestDeviceUserLimitRejectsAboveLevel：等级限 L 台时 N>L 必须 400（红线 4 / D8 变异目标）。
//
// 变异方向（D8）：去掉 `next > levelLimit` 那段校验 → PUT 5 会写成 override=5，
// 而 override>=0 时 EffectiveDeviceLimit 直接用它 ⇒ **用户把自己的上限从 3 升到 5**，
// 「只能降不能升」被完全绕过 → 本条必红。
//
// ⚠️ 断言的是**文案**与**库里的值**两件事。只断 400 不够：
// 一个「永远返回 400」的实现也能过（用户根本改不了上限）。
func TestDeviceUserLimitRejectsAboveLevel(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 2, 3)
	owner := seedDeviceUserUser(t, db, "devuser-limit-up", levelID, -1)

	router := deviceUserRouter(owner)

	// 边界内（3 = 等级值本身）必须成功：那仍是「不超过」。
	//
	// ⚠️ 这里断言 **override 原值**而不是**生效上限 limit**：
	// limit 来自 service.EffectiveDeviceLimit，本分支上它是临时桩（STUB-followup），
	// 恒返回 0。断言 limit 会变成「在测桩」而不是「在测接口」—— 合并换成 A1 的
	// 正式实现后又会因等级配置不同而变红，两头都不对。
	// 「limit 与 EffectiveDeviceLimit 一致」这条契约由下面的一致性断言覆盖。
	okRecorder, okEnvelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": 3})
	if okRecorder.Code != http.StatusOK {
		t.Fatalf("N == L 必须允许，实际 %d：%s", okRecorder.Code, okRecorder.Body.String())
	}
	if got, _ := okEnvelope.Data["override"].(float64); int(got) != 3 {
		t.Fatalf("override 原值应写成 3，实际 %v", okEnvelope.Data["override"])
	}

	// 越界必须 400，且文案带上等级值 L（137 §4 原文措辞）。
	recorder, envelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": 5})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("N > L 必须 400，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if envelope.Message != "不能超过当前等级上限 3 台" {
		t.Fatalf("400 文案必须是「不能超过当前等级上限 3 台」，实际 %q", envelope.Message)
	}

	// 库里不得被写成 5（这是「绕过」的真实形态：接口报错但值已落库）。
	var stored struct {
		Override int32 `gorm:"column:device_limit_override"`
	}
	if err := db.Table(entity.TableNameUser).
		Select("device_limit_override AS device_limit_override").
		Where("id = ?", owner.ID).Take(&stored).Error; err != nil {
		t.Fatalf("read override: %v", err)
	}
	if stored.Override != 3 {
		t.Fatalf("越界请求不得改动 override，期望仍为 3，实际 %d", stored.Override)
	}

	// 负数一律拒绝（不接受 -1 当「恢复默认」：那等价于允许升回等级值）。
	negative, _ := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": -1})
	if negative.Code != http.StatusBadRequest {
		t.Fatalf("负数必须 400，实际 %d：%s", negative.Code, negative.Body.String())
	}

	// 降级必须真的生效且被列表/上限接口读到（否则「能降」只是表面能力）。
	downRecorder, downEnvelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": 1})
	if downRecorder.Code != http.StatusOK {
		t.Fatalf("N < L 必须允许，实际 %d：%s", downRecorder.Code, downRecorder.Body.String())
	}
	if got, _ := downEnvelope.Data["override"].(float64); int(got) != 1 {
		t.Fatalf("override 原值应为 1，实际 %v", downEnvelope.Data["override"])
	}
	if got, _ := downEnvelope.Data["levelLimit"].(float64); int(got) != 3 {
		t.Fatalf("levelLimit 必须始终是等级值 3（PUT 的上界来源），实际 %v", downEnvelope.Data["levelLimit"])
	}

	// GET 必须与 PUT 的返回同口径（两处都用 deviceUserLimitViewFor）。
	getRecorder, getEnvelope := doDeviceUser(t, router, http.MethodGet, "/api/user/device-limit", nil)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("读上限应成功，实际 %d：%s", getRecorder.Code, getRecorder.Body.String())
	}
	if got, _ := getEnvelope.Data["override"].(float64); int(got) != 1 {
		t.Fatalf("GET 的 override 应为 1，实际 %v", getEnvelope.Data["override"])
	}
	// limit 必须与「唯一权威计算」一致：这里直接调同一个函数比对，而不是断言
	// 一个写死的数字。这样合并换成 A1 的正式实现后本条依然成立（口径未漂移），
	// 而「自己另算一份上限」的实现会立刻变红 —— 这正是 137 §4 要防的漂移。
	wantLimit, wantSource, err := service.EffectiveDeviceLimit(db, owner.ID)
	if err != nil {
		t.Fatalf("EffectiveDeviceLimit: %v", err)
	}
	if got, _ := getEnvelope.Data["limit"].(float64); int32(got) != wantLimit {
		t.Fatalf("GET 的 limit 必须等于 EffectiveDeviceLimit（%d），实际 %v", wantLimit, getEnvelope.Data["limit"])
	}
	if source, _ := getEnvelope.Data["source"].(string); source != wantSource {
		t.Fatalf("GET 的 source 必须等于 EffectiveDeviceLimit 的 source（%q），实际 %q", wantSource, source)
	}
	if upper, ok := getEnvelope.Data["upperBound"].(float64); !ok || int(upper) != 3 {
		t.Fatalf("upperBound 必须是等级值 3（前端输入框的 max），实际 %v", getEnvelope.Data["upperBound"])
	}
}

// TestDeviceUserLimitAllowsAnyValueWhenLevelUnlimited：等级不限（L=0）时允许 N>=0（红线 4）。
//
// 137 §4 第 2 条：`L == 0（等级不限）→ 允许 N >= 0`。
// 若实现写成「一律要求 N <= L」，等级不限的用户连设 10 台都会被拒 ——
// 「主动收紧」这个功能对最需要它的人（不限档）反而不可用。
func TestDeviceUserLimitAllowsAnyValueWhenLevelUnlimited(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 0)
	owner := seedDeviceUserUser(t, db, "devuser-limit-unlimited", levelID, -1)

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": 10})
	if recorder.Code != http.StatusOK {
		t.Fatalf("等级不限时 N=10 必须允许，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	// 同上：只断言 override（接口自己写的那个值），不断言 limit（它来自临时桩）。
	if got, _ := envelope.Data["override"].(float64); int(got) != 10 {
		t.Fatalf("override 原值应为 10，实际 %v", envelope.Data["override"])
	}
	if upper := envelope.Data["upperBound"]; upper != nil {
		t.Fatalf("等级不限时 upperBound 必须是 null（无上界），实际 %v", upper)
	}

	// 0 也是合法值（把等级「不限」主动收紧成「不限」= 显式 0），不该被当成「没传」。
	zeroRecorder, zeroEnvelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": 0})
	if zeroRecorder.Code != http.StatusOK {
		t.Fatalf("N=0 必须允许，实际 %d：%s", zeroRecorder.Code, zeroRecorder.Body.String())
	}
	if got, _ := zeroEnvelope.Data["override"].(float64); int(got) != 0 {
		t.Fatalf("override 必须写成 0（不是 -1），实际 %v", zeroEnvelope.Data["override"])
	}
}

// TestDeviceUserLimitRequiresExplicitValue：缺 `limit` 字段必须 400，不能静默当成 0。
//
// 用 400 而不是「默认 0」：0 表示「不限」的收紧/放宽是**语义动作**，
// 把漏传当成它等于凭空改写用户的上限，而且方向是放大（不限 = 最宽松）。
func TestDeviceUserLimitRequiresExplicitValue(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 3, 2)
	owner := seedDeviceUserUser(t, db, "devuser-limit-missing", levelID, -1)

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"somethingElse": 1})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 limit 字段必须 400，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if envelope.Message != "请提供要设置的设备上限" {
		t.Fatalf("400 文案不符，实际 %q", envelope.Message)
	}

	var stored struct {
		Override int32 `gorm:"column:device_limit_override"`
	}
	if err := db.Table(entity.TableNameUser).
		Select("device_limit_override AS device_limit_override").
		Where("id = ?", owner.ID).Take(&stored).Error; err != nil {
		t.Fatalf("read override: %v", err)
	}
	if stored.Override != -1 {
		t.Fatalf("缺字段请求不得改动 override（必须仍是 -1），实际 %d", stored.Override)
	}
}

// TestDeviceUserLimitTableColumnsMissing：device_limit_override 列缺失时 PUT 返回 503。
//
// 为什么是 503 而不是 500：这是「数据表待迁移」的基础设施状态，
// 前端据此提示「功能暂未就绪」而不是「系统故障」。
// 为什么不是 200：静默成功会让用户以为上限改了，而实际生效值没变。
func TestDeviceUserLimitTableColumnsMissing(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-limit-nocolumn", 0, -1)
	// SQLite 支持 DROP COLUMN（3.35+），glebarez/sqlite 的版本满足。
	if err := db.Exec("ALTER TABLE " + entity.TableNameUser + " DROP COLUMN device_limit_override").Error; err != nil {
		t.Fatalf("drop override column: %v", err)
	}

	router := deviceUserRouter(owner)
	recorder, _ := doDeviceUser(t, router, http.MethodPut, "/api/user/device-limit",
		map[string]any{"limit": 1})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺列时 PUT 必须 503，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 静态引用：确保 service 包的桩常量在本测试编译单元里**不被使用**（纪律 3）。
//
// 这里刻意只做编译期占位：`_ = service.EffectiveDeviceLimit` 让「删掉桩文件后
// 本文件仍能编译」这件事成立（因为它引用的是函数名而不是桩常量的值）。
var _ = service.EffectiveDeviceLimit

// ─────────────────── 来源 IP（last_ip）与重命名 ───────────────────

// 列表必须回传 last_ip，且缺列时降级成空串而不是报错。
//
// 变异方向「把 last_ip 从 SELECT 里删掉」→ 第一条断言必红（lastIp 恒空）。
// 变异方向「缺列时不返回空串而是 500」→ 第二条断言必红。
func TestDeviceUserListReturnsLastIP(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-lastip", 0, -1)
	id := seedDeviceUserBinding(t, db, owner.ID, "ip-device", "", time.Now(), "", nil)
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("id = ?", id).Update("last_ip", "203.0.113.7").Error; err != nil {
		t.Fatalf("set last_ip: %v", err)
	}

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	devices, _ := envelope.Data["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("期望 1 台设备，实际 %d：%s", len(devices), recorder.Body.String())
	}
	item, _ := devices[0].(map[string]any)
	if got, _ := item["lastIp"].(string); got != "203.0.113.7" {
		t.Fatalf("列表必须回传 lastIp，实际 %q（完整响应 %s）", got, recorder.Body.String())
	}
}

func TestDeviceUserListReturnsFirstAndLastUsage(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "device-usage", 0, -1)
	first := time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC)
	last := first.Add(24 * time.Hour)
	id := seedDeviceUserBinding(t, db, owner.ID, "usage-device", "", last, "", nil)
	if err := db.Table(entity.TableNameAccountDeviceBinding).Where("id = ?", id).Updates(map[string]any{
		"first_ip": "172.18.0.1", "last_ip": "203.0.113.7", "first_seen_at": first,
	}).Error; err != nil {
		t.Fatal(err)
	}
	w, body := doDeviceUser(t, deviceUserRouter(owner), http.MethodGet, "/api/user/devices", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	item := body.Data["devices"].([]any)[0].(map[string]any)
	if item["firstIp"] != "172.18.0.1" || item["lastIp"] != "203.0.113.7" {
		t.Fatalf("usage IPs: %#v", item)
	}
	for key, expected := range map[string]time.Time{"firstSeenAt": first, "lastSeenAt": last} {
		got, err := time.Parse(time.RFC3339Nano, item[key].(string))
		if err != nil || !got.Equal(expected) {
			t.Fatalf("%s=%v", key, item[key])
		}
	}
}

func TestRegisterDeviceFirstIPNeverChangesOrInventsHistory(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "device-first-ip", 0, -1)
	for _, ip := range []string{"::ffff:172.18.0.1", "198.51.100.4", ""} {
		if _, err := service.RegisterDeviceWithOptions(db, owner.ID, "new-device", service.DeviceBindingRegisterOptions{ClientIP: ip}); err != nil {
			t.Fatal(err)
		}
	}
	var got entity.AccountDeviceBinding
	if err := db.Where("user_id = ? AND device_id = ?", owner.ID, "new-device").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.FirstIP != "172.18.0.1" || got.LastIP != "198.51.100.4" {
		t.Fatalf("first/last IP changed incorrectly: %#v", got)
	}
	seedDeviceUserBinding(t, db, owner.ID, "old-device", "", time.Now().Add(-24*time.Hour), "", nil)
	if _, err := service.RegisterDeviceWithOptions(db, owner.ID, "old-device", service.DeviceBindingRegisterOptions{ClientIP: "198.51.100.4"}); err != nil {
		t.Fatal(err)
	}
	var legacy entity.AccountDeviceBinding
	if err := db.Where("user_id = ? AND device_id = ?", owner.ID, "old-device").First(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.FirstIP != "" || legacy.LastIP != "198.51.100.4" {
		t.Fatalf("legacy history was invented: %#v", legacy)
	}
}

func TestDeviceUserListToleratesMissingFirstIPColumn(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "device-old-schema", 0, -1)
	seedDeviceUserBinding(t, db, owner.ID, "legacy-device", "", time.Now(), "", nil)
	if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding + " DROP COLUMN first_ip").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterDeviceWithOptions(db, owner.ID, "legacy-device", service.DeviceBindingRegisterOptions{ClientIP: "172.18.0.1"}); err != nil {
		t.Fatal(err)
	}
	w, body := doDeviceUser(t, deviceUserRouter(owner), http.MethodGet, "/api/user/devices", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	item := body.Data["devices"].([]any)[0].(map[string]any)
	if item["firstIp"] != "" || item["lastIp"] != "172.18.0.1" {
		t.Fatalf("old schema: %#v", item)
	}
}

// 缺 last_ip 列（迁移未跑）时：列表照常 200，lastIp 退化为空串。
//
// 为什么要单独一条：加了 HasColumn 判断却没有这条用例，就没人能证明
// 「老库不会因为少一列而整页 500」。
func TestDeviceUserListToleratesMissingLastIPColumn(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-lastip-nocol", 0, -1)
	seedDeviceUserBinding(t, db, owner.ID, "no-ip-device", "", time.Now(), "", nil)
	if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
		" DROP COLUMN last_ip").Error; err != nil {
		t.Fatalf("drop last_ip: %v", err)
	}

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodGet, "/api/user/devices", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("缺 last_ip 列时列表仍须 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	devices, _ := envelope.Data["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("期望 1 台设备，实际 %d", len(devices))
	}
	item, _ := devices[0].(map[string]any)
	if got, ok := item["lastIp"].(string); !ok || got != "" {
		t.Fatalf("缺列时 lastIp 必须是空串（而不是缺失字段或伪造值），实际 %#v", item["lastIp"])
	}
}

// 重命名：写 device_label，且**只**写这一列（不碰 last_seen_at）。
//
// 为什么必须断言 last_seen_at 不变：用 Updates 而不是 UpdateColumn 会顺带
// 刷新 updated_at（不是 last_seen_at），但如果有人把它改成
// `Updates(map{label, last_seen_at})`，用户的「10 天闲置解绑」就被改名的动作
// 意外续命了 —— 界面上看不出来，只有数据能证明。
func TestDeviceUserRenameWritesLabelOnly(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-rename", 0, -1)
	// 用一个明显早于「现在」的时间，任何意外刷新都会让它变大。
	oldSeen := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	id := seedDeviceUserBinding(t, db, owner.ID, "rename-device", "", oldSeen, "", nil)

	router := deviceUserRouter(owner)
	recorder, envelope := doDeviceUser(t, router, http.MethodPut,
		fmt.Sprintf("/api/user/devices/%d/label", id), map[string]any{"label": "  家里的笔记本  "})
	if recorder.Code != http.StatusOK {
		t.Fatalf("重命名应 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if got, _ := envelope.Data["deviceLabel"].(string); got != "家里的笔记本" {
		t.Fatalf("响应里的 deviceLabel 必须是去空白后的值，实际 %q", got)
	}

	var stored struct {
		Label  string    `gorm:"column:device_label"`
		Seen   time.Time `gorm:"column:last_seen_at"`
		Detail time.Time `gorm:"column:updated_at"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("device_label AS device_label, last_seen_at AS last_seen_at, updated_at AS updated_at").
		Where("id = ?", id).Take(&stored).Error; err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if stored.Label != "家里的笔记本" {
		t.Fatalf("device_label 必须落库为「家里的笔记本」，实际 %q", stored.Label)
	}
	if delta := stored.Seen.Sub(oldSeen); delta > time.Minute || delta < -time.Minute {
		t.Fatalf("重命名**不得**改动 last_seen_at（它是 10 天闲置解绑的唯一数据源），"+
			"期望仍为 %v，实际 %v", oldSeen, stored.Seen)
	}
}

// 重命名必须校验归属：别人的设备一律 404（红线 1，与 remove/block 同口径）。
func TestDeviceUserRenameRequiresOwnership(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-rename-owner", 0, -1)
	intruder := seedDeviceUserUser(t, db, "devuser-rename-intruder", 0, -1)
	id := seedDeviceUserBinding(t, db, owner.ID, "owned-device", "原名", time.Now(), "", nil)

	router := deviceUserRouter(intruder)
	recorder, _ := doDeviceUser(t, router, http.MethodPut,
		fmt.Sprintf("/api/user/devices/%d/label", id), map[string]any{"label": "被别人改的"})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("改别人的设备必须 404（不能是 403，否则泄露存在性），实际 %d：%s",
			recorder.Code, recorder.Body.String())
	}
	var label string
	if err := db.Table(entity.TableNameAccountDeviceBinding).Select("device_label").
		Where("id = ?", id).Scan(&label).Error; err != nil {
		t.Fatalf("read label: %v", err)
	}
	if label != "原名" {
		t.Fatalf("越权请求不得改动设备名，实际 %q", label)
	}
}

// 重命名输入校验：控制字符/超长/空串。
//
// 空串是**合法**的（恢复「未命名的设备」显示），必须 200 且落库为空串 ——
// 这条与下面两个拒绝用例放在一起，防止有人把「拒绝一切非法输入」写成「拒绝空串」。
func TestDeviceUserRenameLabelValidation(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantCode  int
		wantLabel string
	}{
		{"空串恢复默认显示", "", http.StatusOK, ""},
		{"纯空白等价于空串", "   ", http.StatusOK, ""},
		{"含换行被拒", "第一行\n第二行", http.StatusBadRequest, ""},
		{"含制表符被拒", "名字\t带制表符", http.StatusBadRequest, ""},
		{"含 NUL 被拒", "名字\x00", http.StatusBadRequest, ""},
		{"恰 64 字符允许", strings.Repeat("名", 64), http.StatusOK, strings.Repeat("名", 64)},
		{"65 字符被拒", strings.Repeat("名", 65), http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newDeviceUserTestDB(t, true)
			owner := seedDeviceUserUser(t, db, "devuser-rename-"+tc.name, 0, -1)
			id := seedDeviceUserBinding(t, db, owner.ID, "validate-device", "旧名字", time.Now(), "", nil)

			router := deviceUserRouter(owner)
			recorder, _ := doDeviceUser(t, router, http.MethodPut,
				fmt.Sprintf("/api/user/devices/%d/label", id), map[string]any{"label": tc.input})
			if recorder.Code != tc.wantCode {
				t.Fatalf("期望 %d，实际 %d：%s", tc.wantCode, recorder.Code, recorder.Body.String())
			}
			var label string
			if err := db.Table(entity.TableNameAccountDeviceBinding).Select("device_label").
				Where("id = ?", id).Scan(&label).Error; err != nil {
				t.Fatalf("read label: %v", err)
			}
			if tc.wantCode == http.StatusOK {
				if label != tc.wantLabel {
					t.Fatalf("落库值应为 %q，实际 %q", tc.wantLabel, label)
				}
				return
			}
			// 被拒时**原值不得被改动**（不能先写后校验）。
			if label != "旧名字" {
				t.Fatalf("被拒的请求不得改动设备名，实际 %q", label)
			}
		})
	}
}

// 缺 device_label 列（迁移未跑）时重命名返回 503，而不是静默成功。
//
// 静默成功会让用户以为改了名，刷新后又变回「未命名的设备」—— 那是更难查的
// 缺陷形态（无报错、只有目视可见）。
func TestDeviceUserRenameRequiresLabelColumn(t *testing.T) {
	db := newDeviceUserTestDB(t, false) // withA1Columns=false
	owner := seedDeviceUserUser(t, db, "devuser-rename-nocol", 0, -1)
	row := entity.AccountDeviceBinding{
		UserID: owner.ID, DeviceID: "nocol-device",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	// withA1Columns=false 只是「不额外补」，而 AutoMigrate(&entity.AccountDeviceBinding{})
	// 会按实体 tag 把 device_label 建出来。要精确复现「迁移未跑」的库，必须显式删列。
	if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
		" DROP COLUMN device_label").Error; err != nil {
		t.Fatalf("drop device_label: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_label") {
		t.Fatal("前置不成立：device_label 应当已被删掉")
	}

	router := deviceUserRouter(owner)
	recorder, _ := doDeviceUser(t, router, http.MethodPut,
		fmt.Sprintf("/api/user/devices/%d/label", row.ID), map[string]any{"label": "新名字"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺 device_label 列时必须 503，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// NormalizeDeviceClientIP：IPv4-mapped IPv6 收敛、非法值返回空串、内网地址保留。
//
// 变异方向「非法值原样返回」→ 第 2 组必红；「过滤内网」→ 第 3 组必红。
func TestNormalizeDeviceClientIP(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"普通 IPv4", "203.0.113.7", "203.0.113.7"},
		{"首尾空白", "  203.0.113.7  ", "203.0.113.7"},
		{"IPv4-mapped IPv6 收敛为 IPv4", "::ffff:203.0.113.7", "203.0.113.7"},
		{"IPv6 原样", "2001:db8::1", "2001:db8::1"},
		// 内网与回环必须**保留**：这是给设备主人自己看的页面。
		{"私网保留", "192.168.1.7", "192.168.1.7"},
		{"回环保留", "127.0.0.1", "127.0.0.1"},
		{"空串", "", ""},
		{"纯空白", "   ", ""},
		{"非法文本", "unknown", ""},
		{"带端口的组合", "203.0.113.7:8080", ""},
		{"空尖括号", "<nil>", ""},
	}
	for _, tc := range cases {
		if got := service.NormalizeDeviceClientIP(tc.input); got != tc.want {
			t.Errorf("%s：NormalizeDeviceClientIP(%q) = %q，期望 %q", tc.name, tc.input, got, tc.want)
		}
	}
}

// 登记只在**采到合法 IP** 时写 last_ip，采不到时不得把已知地址抹成空。
//
// 变异方向「无条件写 updates["last_ip"]」→ 第二条断言必红。
// 它对应的真实故障：daemon 重启/请求来自 unix socket 时 c.ClientIP() 拿不到值，
// 用户会看到自己设备的 IP 忽然变成「未记录」。
func TestRegisterDeviceKeepsLastIPWhenClientIPMissing(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "devuser-register-ip", 0, -1)
	deviceID := "register-ip-device"

	// 第一次：带合法 IP → 落库。
	if _, err := service.RegisterDeviceWithOptions(db, owner.ID, deviceID,
		service.DeviceBindingRegisterOptions{ClientIP: "203.0.113.9"}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	readIP := func() string {
		var got string
		if err := db.Table(entity.TableNameAccountDeviceBinding).Select("last_ip").
			Where("user_id = ? AND device_id = ?", owner.ID, deviceID).Scan(&got).Error; err != nil {
			t.Fatalf("read last_ip: %v", err)
		}
		return got
	}
	if got := readIP(); got != "203.0.113.9" {
		t.Fatalf("首次登记必须写入 last_ip，实际 %q", got)
	}

	// 第二次：空 IP（模拟采不到）→ 必须保留原值。
	if _, err := service.RegisterDeviceWithOptions(db, owner.ID, deviceID,
		service.DeviceBindingRegisterOptions{ClientIP: ""}); err != nil {
		t.Fatalf("second register: %v", err)
	}
	if got := readIP(); got != "203.0.113.9" {
		t.Fatalf("采不到 IP 时不得抹掉已知地址，期望仍是 203.0.113.9，实际 %q", got)
	}

	// 第三次：非法 IP（代理塞进来的乱码）→ 同样不得覆盖。
	if _, err := service.RegisterDeviceWithOptions(db, owner.ID, deviceID,
		service.DeviceBindingRegisterOptions{ClientIP: "not-an-ip"}); err != nil {
		t.Fatalf("third register: %v", err)
	}
	if got := readIP(); got != "203.0.113.9" {
		t.Fatalf("非法 IP 不得覆盖已知地址，期望仍是 203.0.113.9，实际 %q", got)
	}

	// 第四次：换一个合法 IP → 必须更新（设备确实换网络了）。
	if _, err := service.RegisterDeviceWithOptions(db, owner.ID, deviceID,
		service.DeviceBindingRegisterOptions{ClientIP: "198.51.100.4"}); err != nil {
		t.Fatalf("fourth register: %v", err)
	}
	if got := readIP(); got != "198.51.100.4" {
		t.Fatalf("换网络后必须更新 last_ip，期望 198.51.100.4，实际 %q", got)
	}
}
