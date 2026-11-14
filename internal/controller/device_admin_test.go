package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 设备管理页控制器测试（137 §8.2 / §9.2）。
//
// # 全部走真 sqlite 内存库、不 mock service
//
// 「封禁后缓存必须立刻命中」只有在真实 GORM 写库 + 真实缓存重载链路上才有意义。
// mock 掉 service 就退化成「我们调用了一个函数」，而本项最容易漏的恰恰是
// 「handler 调了 service，但没刷新缓存」（要等 60 秒才生效，页面提示却是「封禁成功」）。
//
// # 路由形态必须与生产一致
//
// 刻意用 `/api/admin/device-bans`（独立静态前缀）而不是 `/api/admin/devices/ban`：
// 后者与 `POST /api/admin/devices/:id/unbind` 在 gin 同一方法树上会 panic。
// 测试路由若写成前者能跑绿，生产挂后者会启动即崩 —— 那种「测试绿、生产崩」
// 的偏差必须在这一层就消掉。

func newDeviceAdminControllerTestDB(t *testing.T, withRevocationColumns bool) *gorm.DB {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}, &entity.BlacklistHit{},
		&entity.AccountDeviceBinding{}); err != nil {
		t.Fatalf("migrate device admin tables: %v", err)
	}
	if withRevocationColumns {
		// ⚠️ 逐列判断已存在再补（2026-11-15 合入 A1 后修）：
		// `entity.AccountDeviceBinding` 在 A1 落地后自带这 5 个字段，AutoMigrate 会建出来。
		// 无条件 ALTER 会报 `duplicate column name: owner_kind`，让本文件 8 条用例全红。
		for _, col := range []struct{ name, ddl string }{
			{"owner_kind", "ALTER TABLE account_device_bindings ADD COLUMN owner_kind VARCHAR(16) NOT NULL DEFAULT 'login'"},
			{"token_id", "ALTER TABLE account_device_bindings ADD COLUMN token_id INTEGER NOT NULL DEFAULT 0"},
			{"device_label", "ALTER TABLE account_device_bindings ADD COLUMN device_label VARCHAR(64) NOT NULL DEFAULT ''"},
			{"revoked_at", "ALTER TABLE account_device_bindings ADD COLUMN revoked_at DATETIME NULL"},
			{"revoked_reason", "ALTER TABLE account_device_bindings ADD COLUMN revoked_reason VARCHAR(32) NOT NULL DEFAULT ''"},
		} {
			if db.Migrator().HasColumn(&entity.AccountDeviceBinding{}, col.name) {
				continue
			}
			if err := db.Exec(col.ddl).Error; err != nil {
				t.Fatalf("add revocation column (%s): %v", col.ddl, err)
			}
		}
	}
	return db
}

// installDeviceAdminTestDB 把控制器的数据库取用点换成测试库，并同步缓存刷新链路。
func installDeviceAdminTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	previousDB := deviceAdminDB
	previousReload := reloadBlacklistCache
	deviceAdminDB = func() *gorm.DB { return db }
	reloadBlacklistCache = func(target *gorm.DB) error {
		if target == nil {
			return service.ErrBlacklistCacheNoDatabase
		}
		return service.ReloadDefaultBlacklistCache(target)
	}
	t.Cleanup(func() {
		deviceAdminDB = previousDB
		reloadBlacklistCache = previousReload
	})
}

// deviceAdminTestRouter 组装与生产 router.go 完全一致的中间件顺序与路径。
func deviceAdminTestRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/api/admin/devices", auth, middleware.RequireAdmin, AdminDeviceList)
	router.POST("/api/admin/devices/:id/unbind", auth, middleware.RequireAdmin, AdminDeviceUnbind)
	router.POST("/api/admin/device-bans", auth, middleware.RequireAdmin, AdminDeviceBan)
	router.DELETE("/api/admin/device-bans/:deviceId", auth, middleware.RequireAdmin, AdminDeviceUnban)
	return router
}

func deviceAdminRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func seedDeviceAdminActor(t *testing.T, db *gorm.DB, id int32, username string) entity.User {
	t.Helper()

	actor := entity.User{
		ID: id, Username: username, Password: "123456",
		Role: "admin", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create device admin actor: %v", err)
	}
	return actor
}

func seedDeviceAdminBinding(t *testing.T, db *gorm.DB, userID int32, deviceID string) entity.AccountDeviceBinding {
	t.Helper()

	now := time.Now().UTC()
	row := entity.AccountDeviceBinding{
		UserID:      userID,
		DeviceID:    deviceID,
		FirstSeenAt: now.Add(-time.Hour),
		LastSeenAt:  now,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create device binding: %v", err)
	}
	return row
}

// TestDeviceAdminRoutesAvoidGinWildcardConflict 是本文件最重要的一条。
//
// # ⚠️ 实测更正（2026-11-15，gin v1.12.0）：137 §8.3 与任务书里写的 panic **不会发生**
//
// 本机实测（真 gin.New + AddRoute）：
//
//	POST /api/admin/devices/:id/unbind
//	POST /api/admin/devices/ban          → 注册成功，不 panic（routes 两条都在）
//	POST /api/admin/devices/:id/unbind
//	POST /api/admin/devices/:deviceId    → panic: ':deviceId' ... conflicts with existing wildcard ':id'
//
// 也就是说 gin 1.12 允许「静态段与参数段同层」（`devices/ban` 与 `devices/:id` 共存时
// 静态优先匹配），**真正**的硬限制是「同层两个不同名的通配符」。
//
// 即便如此，封禁/解封**仍然**放在 `/admin/device-bans` 前缀下（137 §8.3 与任务书红线 5）：
//   - 该限制在旧版 gin 上成立，仓库可能因依赖降级而复现；
//   - 语义上也更清楚：ban 的对象是 device_id（跨账号），unbind 的对象是绑定行 id，
//     两者不是同一类资源，共用一个前缀只会让调用方混淆。
//
// 因此本用例钉两件事：
//  1. 生产形态的四条路由能注册且能匹配（含 `:` 编码的 deviceId）；
//  2. gin 的真实硬限制仍然存在（两个不同名通配符同层必 panic）——
//     若哪天这条不 panic 了，说明 gin 行为又变了，那时才需要重新评估前缀方案。
func TestDeviceAdminRoutesAvoidGinWildcardConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// ① 生产形态：四条路由 + 真打一次（AddRoute 成功不等于能匹配）。
	router := gin.New()
	router.GET("/api/admin/devices", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/api/admin/devices/:id/unbind", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/api/admin/device-bans", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.DELETE("/api/admin/device-bans/:deviceId", func(c *gin.Context) { c.Status(http.StatusOK) })

	probes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/admin/devices"},
		{http.MethodPost, "/api/admin/devices/42/unbind"},
		{http.MethodPost, "/api/admin/device-bans"},
		{http.MethodDelete, "/api/admin/device-bans/exe%3Aa1b2"},
	}
	for _, probe := range probes {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(probe.method, probe.path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s 必须能匹配到已注册路由，got %d", probe.method, probe.path, recorder.Code)
		}
	}

	// ② gin 的真实硬限制：同层两个不同名通配符必 panic。
	// 用 POST 树（与生产同一棵树）以确保不是「换个方法就绕过」。
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				t.Fatalf("POST /api/admin/devices/:id/unbind 存在时注册 POST /api/admin/devices/:deviceId " +
					"本应 panic（gin 同层不同名通配符冲突），实际没有 —— gin 的路由限制已变，需重新评估")
			}
			message := strings.ToLower(toString(recovered))
			if !strings.Contains(message, "conflict") && !strings.Contains(message, "wildcard") {
				t.Fatalf("panic 内容不是路由冲突（说明是别的原因），got %q", message)
			}
		}()
		conflicting := gin.New()
		conflicting.POST("/api/admin/devices/:id/unbind", func(c *gin.Context) {})
		conflicting.POST("/api/admin/devices/:deviceId", func(c *gin.Context) {})
	}()
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case error:
		return typed.Error()
	default:
		return ""
	}
}

// TestDeviceAdminRejectsNonAdminOnEveryRoute：四条路由在普通用户下全部 403 且零副作用。
func TestDeviceAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)

	normal := entity.User{
		ID: 9351, Username: "SYNTHETIC_DEVICE_NORMAL", Password: "123456",
		Role: "user", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&normal).Error; err != nil {
		t.Fatalf("create normal user: %v", err)
	}
	binding := seedDeviceAdminBinding(t, db, 9352, "exe:denied000001")
	router := deviceAdminTestRouter(&normal)

	probes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/admin/devices", ""},
		{http.MethodPost, "/api/admin/devices/" + strconv.FormatInt(binding.ID, 10) + "/unbind", ""},
		{http.MethodPost, "/api/admin/device-bans", `{"deviceId":"exe:denied000001","reason":"x"}`},
		{http.MethodDelete, "/api/admin/device-bans/exe%3Adenied000001", ""},
	}
	for _, probe := range probes {
		recorder := deviceAdminRequest(router, probe.method, probe.path, probe.body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s 必须 403，got %d (%s)", probe.method, probe.path, recorder.Code, recorder.Body.String())
		}
	}

	// 零副作用：没有黑名单规则、绑定没被撤销。
	var entryCount int64
	if err := db.Model(&entity.BlacklistEntry{}).
		Where("kind = ?", service.BlacklistKindDevice).Count(&entryCount).Error; err != nil {
		t.Fatalf("count device entries: %v", err)
	}
	if entryCount != 0 {
		t.Fatalf("被拒的请求不得写入任何设备规则，实际 %d 条", entryCount)
	}
	var row entity.AccountDeviceBinding
	if err := db.Where("id = ?", binding.ID).Take(&row).Error; err != nil {
		t.Fatalf("reload binding: %v", err)
	}
	var revokedAt *time.Time
	if err := db.Raw("SELECT revoked_at FROM account_device_bindings WHERE id = ?", binding.ID).
		Row().Scan(&revokedAt); err != nil {
		t.Fatalf("read revoked_at: %v", err)
	}
	if revokedAt != nil {
		t.Fatalf("被拒的请求不得撤销绑定")
	}
}

// TestDeviceAdminListReturnsItemsAndOrphans：列表 handler 的响应形态。
func TestDeviceAdminListReturnsItemsAndOrphans(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 9361, "SYNTHETIC_DEVICE_ADMIN")
	owner := entity.User{
		ID: 9362, Username: "alice", Password: "123456",
		Role: "user", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	seedDeviceAdminBinding(t, db, owner.ID, "exe:list000000001")
	router := deviceAdminTestRouter(&actor)

	recorder := deviceAdminRequest(router, http.MethodGet, "/api/admin/devices?keyword=ali", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表必须 200，got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var result service.AdminDeviceListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode list: %v (%s)", err, recorder.Body.String())
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("按用户名搜索应命中 1 行，got total=%d data=%d", result.Total, len(result.Data))
	}
	if result.Data[0].Username != "alice" {
		t.Fatalf("username 必须回填，got %q", result.Data[0].Username)
	}
	if result.Data[0].DeviceLabel != "" {
		t.Fatalf("device_label 未上报时必须是空串（前端显示「—」），got %q", result.Data[0].DeviceLabel)
	}
	if !result.RevocationSupported {
		t.Fatalf("撤销列齐备时 revocationSupported 必须为 true")
	}
	if result.OrphanBans == nil {
		t.Fatalf("orphanBans 必须是 []，不能是 nil（前端把 null 与 [] 当两种状态）")
	}
}

// TestDeviceAdminBanThenUnbanRoundTrip 是 A3-4 的端到端主判据（含缓存立即生效）。
func TestDeviceAdminBanThenUnbanRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	// ⚠️ 必须先把黑名单总开关打开：LoadBlacklistSnapshot 在开关为 false 时
	// **只读一次开关就返回空快照**，于是「封禁后缓存立即命中」这条断言永远不可能成立
	// （那不是 bug，是「开关默认关闭」的语义；测试必须显式打开）。
	setBlacklistAdminSwitch(t, db, true)
	actor := seedDeviceAdminActor(t, db, 9371, "SYNTHETIC_DEVICE_ADMIN2")
	first := seedDeviceAdminBinding(t, db, 9372, "exe:roundtrip0001")
	second := seedDeviceAdminBinding(t, db, 9373, "exe:roundtrip0001")
	router := deviceAdminTestRouter(&actor)

	// ① 封禁：201 + 撤销两行 + 缓存立即可命中。
	recorder := deviceAdminRequest(router, http.MethodPost, "/api/admin/device-bans",
		`{"deviceId":"exe:roundtrip0001","reason":"工作室批量刷题"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("封禁必须 201，got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var ban service.DeviceBanResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &ban); err != nil {
		t.Fatalf("decode ban: %v (%s)", err, recorder.Body.String())
	}
	if ban.RevokedBindings != 2 {
		t.Fatalf("封禁必须撤销该 device 的全部 2 行在用绑定，实际 %d", ban.RevokedBindings)
	}
	if !ban.RevocationApplied {
		t.Fatalf("撤销列齐备时必须真的执行撤销")
	}
	if ban.EntryID <= 0 {
		t.Fatalf("必须返回 entryId，got %d", ban.EntryID)
	}

	// 缓存必须**立即**命中：这是红线 4（写操作后必须 Invalidate/Reload）。
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchDevice("exe:roundtrip0001"); !hit {
		t.Fatalf("封禁后缓存必须立即命中（否则要等 60s 才生效，页面提示是假的）")
	}

	// ② 解绑：解除另一台设备的一行，且不得封设备。
	otherBinding := seedDeviceAdminBinding(t, db, 9372, "exe:otherbind0001")
	recorder = deviceAdminRequest(router, http.MethodPost,
		"/api/admin/devices/"+strconv.FormatInt(otherBinding.ID, 10)+"/unbind", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("解绑必须 200，got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var item service.AdminDeviceItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode unbind: %v (%s)", err, recorder.Body.String())
	}
	if item.RevokedReason != service.DeviceBindingRevokedReasonAdminUnbound {
		t.Fatalf("解绑原因必须是 admin_unbound，got %q", item.RevokedReason)
	}
	if item.Status != service.AdminDeviceStatusRevoked {
		t.Fatalf("解绑后状态必须是 revoked，got %q", item.Status)
	}
	// 解绑不封设备：库里不得出现该设备的规则。
	var entries int64
	if err := db.Model(&entity.BlacklistEntry{}).
		Where("kind = ? AND value_hash = ?", service.BlacklistKindDevice,
			service.BlacklistPatternHashHex("exe:otherbind0001")).Count(&entries).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if entries != 0 {
		t.Fatalf("解绑不得写黑名单规则，实际 %d 条 —— 解绑 ≠ 封禁", entries)
	}

	// ③ 解封：200 + deleted=1 + 缓存立即不命中。
	recorder = deviceAdminRequest(router, http.MethodDelete, "/api/admin/device-bans/exe%3Aroundtrip0001", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("解封必须 200，got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var unban map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &unban); err != nil {
		t.Fatalf("decode unban: %v (%s)", err, recorder.Body.String())
	}
	if deleted, _ := unban["deleted"].(float64); deleted != 1 {
		t.Fatalf("解封必须删掉 1 条规则，got %v", unban["deleted"])
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchDevice("exe:roundtrip0001"); hit {
		t.Fatalf("解封后缓存必须立即不命中")
	}
	// 解封不恢复绑定（撤销是既成事实）。
	var revokedRows int64
	if err := db.Raw("SELECT COUNT(*) FROM account_device_bindings WHERE device_id = ? AND revoked_at IS NOT NULL",
		"exe:roundtrip0001").Row().Scan(&revokedRows); err != nil {
		t.Fatalf("count revoked: %v", err)
	}
	if revokedRows != 2 {
		t.Fatalf("解封不得恢复绑定（撤销是审计事实），期望仍 2 行已撤销，实际 %d", revokedRows)
	}
	_ = first
	_ = second
}

// TestDeviceAdminBanValidatesInput：三类非法输入的 400 与零副作用。
func TestDeviceAdminBanValidatesInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 9381, "SYNTHETIC_DEVICE_ADMIN3")
	router := deviceAdminTestRouter(&actor)

	cases := []struct {
		name string
		body string
	}{
		{"空 deviceId", `{"deviceId":"   ","reason":"x"}`},
		{"空 reason", `{"deviceId":"exe:validate0001","reason":"  "}`},
		{"负时长", `{"deviceId":"exe:validate0001","reason":"x","durationAmount":-1}`},
		{"过去到期时间", `{"deviceId":"exe:validate0001","reason":"x","expiresAt":"2020-01-01T00:00:00Z"}`},
		{"非法时长单位", `{"deviceId":"exe:validate0001","reason":"x","durationAmount":3,"durationUnit":"fortnight"}`},
	}
	for _, tc := range cases {
		recorder := deviceAdminRequest(router, http.MethodPost, "/api/admin/device-bans", tc.body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 必须 400，got %d (%s)", tc.name, recorder.Code, recorder.Body.String())
		}
	}
	var entries int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&entries).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if entries != 0 {
		t.Fatalf("被拒的封禁不得留下规则，实际 %d 条", entries)
	}
}

// TestDeviceAdminUnbindWithoutRevocationColumnsReturns503：缺列时如实 503。
func TestDeviceAdminUnbindWithoutRevocationColumnsReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, false)
	// 同 service 层那两条：A1 合入后实体自带撤销列，必须显式 DROP 才构造得出「老库缺列」。
	for _, col := range []string{"revoked_at", "revoked_reason"} {
		if err := db.Migrator().DropColumn(&entity.AccountDeviceBinding{}, col); err != nil {
			t.Fatalf("drop column %s: %v", col, err)
		}
	}
	if db.Migrator().HasColumn(&entity.AccountDeviceBinding{}, "revoked_at") {
		t.Fatalf("前置失效：revoked_at 仍未删除，本用例测不到「缺列」分支")
	}
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 9391, "SYNTHETIC_DEVICE_ADMIN4")
	// ⚠️ 不能调 seedDeviceAdminBinding（GORM Create 会按实体拼 INSERT，带上已 DROP 的列）。
	// 缺列场景必须用原生 SQL 只写存在的列。
	now := time.Now().UTC()
	ins := db.Exec(
		`INSERT INTO account_device_bindings (user_id, device_id, first_seen_at, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		9392, "exe:nocol00000001", now, now, now, now,
	)
	if ins.Error != nil {
		t.Fatalf("原生插入老库形态绑定行: %v", ins.Error)
	}
	var binding entity.AccountDeviceBinding
	if err := db.Raw(`SELECT id FROM account_device_bindings WHERE user_id = ? AND device_id = ?`, 9392, "exe:nocol00000001").
		Scan(&binding).Error; err != nil {
		t.Fatalf("读回绑定行 id: %v", err)
	}
	router := deviceAdminTestRouter(&actor)

	recorder := deviceAdminRequest(router, http.MethodPost,
		"/api/admin/devices/"+strconv.FormatInt(binding.ID, 10)+"/unbind", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺撤销列时必须 503（不能假装成功），got %d (%s)", recorder.Code, recorder.Body.String())
	}

	// 列表仍要能打开，并明确标记 revocationSupported=false。
	listRecorder := deviceAdminRequest(router, http.MethodGet, "/api/admin/devices", "")
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("缺撤销列时列表仍必须 200（页面要能渲染），got %d", listRecorder.Code)
	}
	var result service.AdminDeviceListResult
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if result.RevocationSupported {
		t.Fatalf("缺列时 revocationSupported 必须为 false（前端据此禁用解绑按钮）")
	}
}

// TestDeviceAdminUnbindRejectsInvalidID：非数字 id 必须 400（不查库）。
func TestDeviceAdminUnbindRejectsInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 9395, "SYNTHETIC_DEVICE_ADMIN5")
	router := deviceAdminTestRouter(&actor)

	for _, raw := range []string{"abc", "0", "-3"} {
		recorder := deviceAdminRequest(router, http.MethodPost, "/api/admin/devices/"+raw+"/unbind", "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("id=%q 必须 400，got %d (%s)", raw, recorder.Code, recorder.Body.String())
		}
	}
}

// TestDeviceAdminUnbindNotFoundReturns404：不存在的绑定行 404。
func TestDeviceAdminUnbindNotFoundReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 9397, "SYNTHETIC_DEVICE_ADMIN6")
	router := deviceAdminTestRouter(&actor)

	recorder := deviceAdminRequest(router, http.MethodPost, "/api/admin/devices/987654/unbind", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的绑定行必须 404，got %d (%s)", recorder.Code, recorder.Body.String())
	}
}

// TestDeviceAdminNoDatabaseReturns503：manager 未初始化时四条路由都不能 500/panic。
func TestDeviceAdminNoDatabaseReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 9399, "SYNTHETIC_DEVICE_ADMIN7")

	previous := deviceAdminDB
	deviceAdminDB = func() *gorm.DB { return nil }
	t.Cleanup(func() { deviceAdminDB = previous })
	router := deviceAdminTestRouter(&actor)

	for _, probe := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/admin/devices", ""},
		{http.MethodPost, "/api/admin/devices/1/unbind", ""},
		{http.MethodPost, "/api/admin/device-bans", `{"deviceId":"exe:abc","reason":"x"}`},
		{http.MethodDelete, "/api/admin/device-bans/exe%3Aabc", ""},
	} {
		recorder := deviceAdminRequest(router, probe.method, probe.path, probe.body)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 无数据库时必须 503，got %d (%s)",
				probe.method, probe.path, recorder.Code, recorder.Body.String())
		}
	}
}
