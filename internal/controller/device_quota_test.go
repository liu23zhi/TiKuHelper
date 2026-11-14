package controller

// GET /api/user/device-quota 的回归测试（141 §6.1 / C1-3）。
//
// ## 本文件守的四件事（每件对应一种会静默失效的实现错误）
//
//  1. **鉴权**：路由必须挂 controller.AuthOrAccessToken。exe 只持 adapter key，
//     挂 Auth 会 401 —— 而表现只是「exe 首页可用设备数一直空」，不报错。
//     这条在 internal/api 的 device_quota_router_test.go 里做源码级断言，
//     本文件的用例只覆盖 handler 行为（身份由中间件注入）。
//  2. **扁平响应体**：limit/used/available/limitSource 在**顶层**，不套 data。
//     调用方是 exe 的 C 代码，会按 141 §6.1 逐字段取值。
//  3. **available 三态**：limit=0 ⇒ -1（不限）；limit>0 ⇒ max(0, limit-used)；
//     占满 ⇒ 0。三者混成一个 0 会让不限档显示成「已满」。
//  4. **used 是槽位口径不是行数**：一台 exe 开 3 个会话时 len(devices)=1
//     但 used=3。把两者搞混会让 exe 首页的数字与「我的设备」列表自相矛盾。
//
// 期望值一律写字面量（不引用 DeviceQuotaUnlimitedAvailable 等常量）：期望值与
// 被测实现共享常量会让断言恒真（本仓已交付过这类假钉子）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// deviceQuotaTestDB 建一个含 users + 等级 + 绑定表的最小库。
//
// 复用 device_user_test.go 的 newDeviceUserTestDB（它已经把 deviceUserDBOverride
// 设好并注册了 Cleanup），本文件只在此基础上补 session_count / client_instance
// 两列的 ALTER —— 与 registry 的迁移保持同一份 DDL 片段。
func deviceQuotaTestDB(t *testing.T, withSessionCount bool) *gorm.DB {
	t.Helper()
	db := newDeviceUserTestDB(t, true)
	if withSessionCount {
		for _, ddl := range []string{
			"client_instance VARCHAR(64) NOT NULL DEFAULT ''",
			"session_count INTEGER NOT NULL DEFAULT 0",
		} {
			column := ddl[:indexOfSpace(ddl)]
			if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
				continue
			}
			if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
				" ADD COLUMN " + ddl).Error; err != nil {
				t.Fatalf("add binding column %s: %v", column, err)
			}
		}
	}
	return db
}

func indexOfSpace(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return i
		}
	}
	return len(s)
}

// deviceQuotaRouter 组装只有配额端点的路由（身份由中间件注入）。
func deviceQuotaRouter(operator *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if operator != nil {
			c.Set("user", operator)
		}
	})
	router.GET("/api/user/device-quota", DeviceQuota)
	return router
}

// deviceQuotaBody 是扁平响应体的解码结构（刻意不带 data 包裹）。
type deviceQuotaBody struct {
	Limit       *int32           `json:"limit"`
	Used        *int             `json:"used"`
	Available   *int             `json:"available"`
	LimitSource string           `json:"limitSource"`
	Devices     []map[string]any `json:"devices"`
	SchemaReady *bool            `json:"schemaReady"`
}

// doDeviceQuota 发一次 GET 并解码。
func doDeviceQuota(t *testing.T, router *gin.Engine, header map[string]string) (int, deviceQuotaBody, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "/api/user/device-quota", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range header {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	raw := recorder.Body.String()
	var body deviceQuotaBody
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, raw)
		}
	}
	return recorder.Code, body, raw
}

// seedDeviceQuotaBinding 插一行绑定，带 session_count 与撤销状态。
func seedDeviceQuotaBinding(t *testing.T, db *gorm.DB, userID int32, deviceID string,
	sessionCount int32, revoked bool) {
	t.Helper()
	row := entity.AccountDeviceBinding{
		UserID:       userID,
		DeviceID:     deviceID,
		FirstSeenAt:  time.Now(),
		LastSeenAt:   time.Now(),
		SessionCount: sessionCount,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding %s: %v", deviceID, err)
	}
	updates := map[string]any{"device_label": "标签-" + deviceID}
	if revoked {
		updates["revoked_at"] = time.Now()
		updates["revoked_reason"] = entity.DeviceBindingRevokeReasonUserRemoved
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		t.Fatalf("patch binding %s: %v", deviceID, err)
	}
}

// ─────────────────────── 契约：字段齐备且扁平 ───────────────────────

// 响应必须在**顶层**给出 limit / used / available / limitSource / devices，
// 不套 data 包裹（141 §6.1 的响应体就是平的）。
//
// 变异「改成 gin.H{"data": view}」⇒ 本用例红：exe 的 C 代码按文档取顶层字段，
// 包一层后它读到的全是零值，表现为「首页显示 0 / 0 且设备列表空」。
func TestDeviceQuotaResponseIsFlatWithDocumentedFields(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-flat", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-flat-a", 0, false)

	code, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（body=%s）", code, raw)
	}
	if body.Limit == nil {
		t.Fatalf("响应顶层必须直接有 limit 字段（不得套 data 包裹），实际 body=%s", raw)
	}
	if body.Used == nil {
		t.Fatalf("响应顶层必须直接有 used 字段，实际 body=%s", raw)
	}
	if body.Available == nil {
		t.Fatalf("响应顶层必须直接有 available 字段，实际 body=%s", raw)
	}
	if body.LimitSource == "" {
		t.Fatalf("limitSource 不得为空（exe 要据此区分「不限」与「没配」），实际 body=%s", raw)
	}
	if body.Devices == nil {
		t.Fatalf("devices 必须是数组（空时是 []，不是 null），实际 body=%s", raw)
	}
	// 反向断言：不得出现 data 包裹。
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, wrapped := envelope["data"]; wrapped {
		t.Fatalf("配额端点响应不得套 data 包裹（141 §6.1 给的是扁平结构，调用方是 exe 的 C 代码）")
	}
}

// ─────────────────────── available 三态 ───────────────────────

// limit=0（不限）⇒ available 必须是 **-1**，不是 0。
//
// 这条最容易被「顺手 max(0, limit-used)」写成 0：四个种子等级的 device_limit
// 全是 0，那等于上线瞬间所有 exe 首页都显示「设备已满」。
func TestDeviceQuotaUnlimitedReturnsNegativeOne(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 0) // 0 = 不限
	user := seedDeviceUserUser(t, db, "quota-unlimited", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-unl-a", 0, false)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-unl-b", 3, false)

	code, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（body=%s）", code, raw)
	}
	if *body.Limit != 0 {
		t.Fatalf("不限档的 limit 必须是 0，实际 %d", *body.Limit)
	}
	if *body.Available != -1 {
		t.Fatalf("limit=0 时 available 必须是 -1（表示不限），实际 %d。"+
			"若为 0，UI 会把不限档显示成「已满」—— 而四个种子等级全是 0", *body.Available)
	}
	// used 仍然要如实报（1 + 3 = 4），不能被「不限」吞掉。
	if *body.Used != 4 {
		t.Fatalf("不限档也必须如实报 used（1 + 3 = 4），实际 %d", *body.Used)
	}
}

// limit>0 且未满 ⇒ available = limit - used。
func TestDeviceQuotaLimitedReturnsRemaining(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-remaining", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-rem-a", 0, false) // → 1
	seedDeviceQuotaBinding(t, db, user.ID, "dev-rem-b", 1, false) // → 1

	_, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if *body.Limit != 3 {
		t.Fatalf("limit 必须是等级的 3，实际 %d（body=%s）", *body.Limit, raw)
	}
	if *body.Used != 2 {
		t.Fatalf("used 必须是 2（max(1,0)+max(1,1)），实际 %d", *body.Used)
	}
	if *body.Available != 1 {
		t.Fatalf("available 必须是 3-2=1，实际 %d", *body.Available)
	}
}

// 已满 / 超出 ⇒ available 必须 clamp 到 0，不能是负数。
//
// 负剩余数会让 UI 显示「可用 -1 台」这种无法解释的文案；而「超了」这个事实
// 由 used > limit 表达，不需要 available 也变负。
func TestDeviceQuotaOverLimitClampsAvailableToZero(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 2)
	user := seedDeviceUserUser(t, db, "quota-over", levelID, -1)
	// 5 个会话 ⇒ used=5 > limit=2（exe 会话数不受服务端拦截，R8「提醒不拦截」）。
	seedDeviceQuotaBinding(t, db, user.ID, "dev-over-a", 5, false)

	_, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if *body.Used != 5 {
		t.Fatalf("used 必须如实报 5（R8 允许超额），实际 %d（body=%s）", *body.Used, raw)
	}
	if *body.Available != 0 {
		t.Fatalf("超限时 available 必须 clamp 到 0（不是 -3），实际 %d", *body.Available)
	}
}

// ─────────────────────── used 是槽位口径 ───────────────────────

// 一台 exe 开 3 个会话：devices 只有 1 条，但 used 必须是 3。
//
// 这条钉的是「槽位口径 vs 行数口径」。把 used 写成 len(devices) 的实现
// 在本用例上给 1，而 UI 会显示「可用 2 台」—— 与设备池判定的实际行为矛盾。
func TestDeviceQuotaUsedCountsSessionsNotRows(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 4)
	user := seedDeviceUserUser(t, db, "quota-sessions", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "exe-host-3", 3, false)

	_, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if len(body.Devices) != 1 {
		t.Fatalf("exe 的 3 个会话共用一个 device_id ⇒ devices 必须只有 1 条，实际 %d（body=%s）",
			len(body.Devices), raw)
	}
	if *body.Used != 3 {
		t.Fatalf("used 必须是 3（槽位口径 Σ max(1,session_count)），实际 %d。"+
			"若为 1，说明 used 退化成了行数 —— UI 会显示「可用 3 台」而实际已满", *body.Used)
	}
	// devices 里的 sessionCount 也要如实带出（exe 首页要显示「N 个浏览器会话」）。
	item := body.Devices[0]
	if count, _ := item["sessionCount"].(float64); int(count) != 3 {
		t.Fatalf("devices[0].sessionCount 必须是 3，实际 %v", item["sessionCount"])
	}
}

// devices 每项必须带 deviceId / label / sessionCount / lastSeenAt（141 §6.1）。
func TestDeviceQuotaDeviceItemFields(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-fields", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-fields-a", 0, false)

	_, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if len(body.Devices) != 1 {
		t.Fatalf("期望 1 条设备，实际 %d（body=%s）", len(body.Devices), raw)
	}
	item := body.Devices[0]
	for _, field := range []string{"deviceId", "label", "sessionCount", "lastSeenAt"} {
		if _, ok := item[field]; !ok {
			t.Fatalf("devices[] 每项必须带 %s 字段（141 §6.1），实际键 = %v", field, item)
		}
	}
	if id, _ := item["deviceId"].(string); id != "dev-fields-a" {
		t.Fatalf("deviceId 必须是 dev-fields-a，实际 %v", item["deviceId"])
	}
	if label, _ := item["label"].(string); label != "标签-dev-fields-a" {
		t.Fatalf("label 必须取 device_label 列，实际 %v", item["label"])
	}
}

// 已撤销的设备不出现在 devices 里，也不占 used（137 §3.2 + 141 §2）。
//
// 把撤销行列进「当前占用」会让 len(devices) 与 used 的关系彻底失真，
// 而且用户会在 exe 首页看到自己已经移除的设备。
func TestDeviceQuotaExcludesRevokedRows(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-revoked", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-live", 0, false)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-gone", 3, true)

	_, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if len(body.Devices) != 1 {
		t.Fatalf("已撤销的设备不得出现在配额视图里，实际 %d 条（body=%s）", len(body.Devices), raw)
	}
	if id, _ := body.Devices[0]["deviceId"].(string); id != "dev-live" {
		t.Fatalf("唯一一条应当是 dev-live，实际 %v", body.Devices[0]["deviceId"])
	}
	if *body.Used != 1 {
		t.Fatalf("撤销行不占槽位：used 必须是 1，实际 %d", *body.Used)
	}
}

// X-Device-Id 命中时要标 isCurrent（exe 首页要能高亮「本 exe」）。
//
// 取不到 header 时恒 false —— 不猜「大概是你这台」。
func TestDeviceQuotaMarksCurrentDevice(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-current", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-me", 0, false)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-other", 0, false)

	_, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), map[string]string{"X-Device-Id": "dev-me"})
	if len(body.Devices) != 2 {
		t.Fatalf("期望 2 条设备，实际 %d（body=%s）", len(body.Devices), raw)
	}
	current, other := 0, 0
	for _, item := range body.Devices {
		if flag, _ := item["isCurrent"].(bool); flag {
			current++
			if id, _ := item["deviceId"].(string); id != "dev-me" {
				t.Fatalf("isCurrent 只应标在 dev-me 上，实际标在 %v", item["deviceId"])
			}
		} else {
			other++
		}
	}
	if current != 1 || other != 1 {
		t.Fatalf("应当恰好一条 isCurrent（dev-me），实际 current=%d other=%d", current, other)
	}

	// 不带 header：全部 isCurrent=false（不猜）。
	_, noHeader, _ := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	for _, item := range noHeader.Devices {
		if flag, _ := item["isCurrent"].(bool); flag {
			t.Fatalf("请求不带 X-Device-Id 时不得标记 isCurrent，实际标在 %v", item["deviceId"])
		}
	}
}

// ─────────────────────── 降级态 ───────────────────────

// session_count 列还没补时，配额端点仍要能工作（退化为每行 1）。
//
// 这是「代码先上、迁移后跑」的部署窗口期形态。返回 503 会让 exe 首页
// 在那几分钟里永久空白，而 200 + used=1 才是正确的降级语义。
func TestDeviceQuotaWithoutSessionCountColumn(t *testing.T) {
	db := deviceQuotaTestDB(t, false) // 不补第二批两列
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-pre-migration", levelID, -1)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-legacy-a", 0, false)
	seedDeviceQuotaBinding(t, db, user.ID, "dev-legacy-b", 0, false)

	code, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if code != http.StatusOK {
		t.Fatalf("列缺失时配额端点不得 503（会让 exe 首页在部署窗口期空白），实际 %d（body=%s）",
			code, raw)
	}
	if *body.Used != 2 {
		t.Fatalf("列缺失时每行应算 1（等价 max(1,0)），期望 2 实际 %d（body=%s）", *body.Used, raw)
	}
	if *body.Available != 1 {
		t.Fatalf("available 必须是 3-2=1，实际 %d", *body.Available)
	}
}

// 绑定表整个不存在时：200 + used=0 + schemaReady=false（fail-open 降级）。
//
// 与 UserDevices 对同一情形的处置一致。返回 503 会让全新部署永远没有配额。
func TestDeviceQuotaWithoutBindingTable(t *testing.T) {
	db := deviceQuotaTestDB(t, true)
	if err := db.Migrator().DropTable(entity.TableNameAccountDeviceBinding); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	levelID := seedDeviceUserLevel(t, db, 1, 3)
	user := seedDeviceUserUser(t, db, "quota-no-table", levelID, -1)

	code, body, raw := doDeviceQuota(t, deviceQuotaRouter(user), nil)
	if code != http.StatusOK {
		t.Fatalf("表缺失时必须是 200（设备池整体处于 fail-open 降级态），实际 %d（body=%s）",
			code, raw)
	}
	if body.SchemaReady == nil || *body.SchemaReady {
		t.Fatalf("schemaReady 必须是 false，让客户端能区分「真的没有设备」与「算不出来」，实际 body=%s", raw)
	}
	if *body.Used != 0 {
		t.Fatalf("表缺失时 used 必须是 0，实际 %d", *body.Used)
	}
	if len(body.Devices) != 0 {
		t.Fatalf("表缺失时 devices 必须是空数组，实际 %d 条", len(body.Devices))
	}
	// limit 仍要从等级读出（不受绑定表影响）。
	if *body.Limit != 3 {
		t.Fatalf("绑定表缺失不影响上限读取，期望 3 实际 %d", *body.Limit)
	}
}

// 未登录（中间件没注入 user）必须 401，不能 200 空数据。
func TestDeviceQuotaRequiresIdentity(t *testing.T) {
	deviceQuotaTestDB(t, true)
	code, _, raw := doDeviceQuota(t, deviceQuotaRouter(nil), nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("没有身份时必须 401，实际 %d（body=%s）", code, raw)
	}
}

// ─────────────────────── available 纯函数 ───────────────────────

// deviceQuotaAvailable 的三档取值直接断言（不经 HTTP，失败信息更精确）。
func TestDeviceQuotaAvailableFunction(t *testing.T) {
	cases := []struct {
		name  string
		limit int32
		used  int
		want  int
	}{
		{"不限档", 0, 5, -1},
		{"未满", 3, 1, 2},
		{"刚好满", 3, 3, 0},
		{"超额", 2, 5, 0},
		{"禁止档", -1, 0, 0},
		{"禁止档且已有设备", -1, 2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deviceQuotaAvailable(c.limit, c.used); got != c.want {
				t.Fatalf("deviceQuotaAvailable(%d, %d) = %d，期望 %d", c.limit, c.used, got, c.want)
			}
		})
	}
}
