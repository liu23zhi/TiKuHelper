package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 管理员账号安全状态接口（admin_security_status.go）的回归测试。
//
// 背景：`GET /adapter-service/admin/security-status` 此前**全仓零覆盖**
// （`git grep -ln 'SecurityStatus' -- '*_test.go'` 零命中），而它承担一条
// 不可协商的性质（实现文件 L26-28）：
//
//	本接口只回布尔，不回任何凭据内容。hash 泄漏后可以被离线爆破。
//
// 四组断言，各自对应一个真实缺陷形态：
//
//	第 1 组 鉴权     —— 有人把路由的两条中间件摘掉（普通用户可读安全状态）
//	第 2 组 只回布尔 —— 有人「为了调试顺手」把密码 / hash 加进响应
//	第 3 组 判定     —— 比较逻辑短路或写反，告警永不触发
//	第 4 组 降级     —— 未初始化时改成 5xx，管理台首页被弄红
//
// # 第 1 组为什么同时做「源码级」和「行为级」
//
// 行为级用例必须在测试里自行组装路由：router.go 的 /adapter-service 组链首位是
// middleware.GlobalAPIRateLimit，它直接解引用 manager.GetManager()，单测无
// manager 时 nil panic（本仓已在 ip_segment_middleware_test.go 与
// archive_routes_middleware_test.go 的注释里实测确认过这一点）。
//
// 自行组装的代价是：它只证明「这套组装会拦」，不证明 router.go 真的这样组装。
// 实测反例：把 router.go 里五条 ip-segments 路由的 RequireAdmin 全删掉，
// 9 条既有测试依然全绿。
//
// 因此本文件**两者都做**：
//   - 行为级 TestAdminSecurityStatusAuthBoundary → 防 Auth / RequireAdmin 自身失效
//   - 源码级 TestAdminSecurityStatusRouteRequiresAuthAndAdmin → 防 router.go 漏挂中间件
//
// 源码级读 ../api/router.go 的做法与本包既有测试一致
// （review_rebate_dto_test.go:398、user_exam_tool_test.go:491）。

const (
	adminSecurityStatusRoutePath = "/adapter-service/admin/security-status"

	// adminSecurityStatusRouterSource 是 router.go 相对 internal/controller 包的路径。
	adminSecurityStatusRouterSource = "router.go"
)

// mustHashPassword 返回 bcrypt hash（生产库里的真实形态）。
func mustHashPassword(t *testing.T, password string) string {
	t.Helper()

	hashed, err := util.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return hashed
}

// installSecurityStatusDB 把 handler 的数据库取用点换成测试库（nil 即降级）。
func installSecurityStatusDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	previous := securityStatusDB
	securityStatusDB = func() *gorm.DB { return db }
	t.Cleanup(func() { securityStatusDB = previous })
}

// seedAdminSecurityStatusUser 建 users.id=1 并**原样写入** storedPassword，返回该值。
//
// 原样写入（而不是内部 hash）是有意的：第 3 组需要造出「库里存的是明文 123456」
// 这一历史形态，第 2 组需要拿库里真实字符串去比对响应体。
func seedAdminSecurityStatusUser(t *testing.T, db *gorm.DB, storedPassword string) string {
	t.Helper()

	user := entity.User{
		ID:             1,
		Username:       "SYNTHETIC_SECURITY_STATUS_ADMIN",
		Password:       storedPassword,
		Role:           "admin",
		Status:         "enabled",
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return storedPassword
}

// adminSecurityStatusGET 直接调 handler 并把响应收进 recorder。
//
// 用 gin.CreateTestContext 而非「建 router 再 ServeHTTP」：后者要注册路由，
// 而 handler 级用例不该顺带验证路由表。本仓 user_test.go 用的是同一手法。
func adminSecurityStatusGET(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, adminSecurityStatusRoutePath, nil)
	AdminSecurityStatus(ctx)
	return recorder
}

// decodeSecurityStatusBody 要求 200 并把响应体解成「原始值」映射（保留 JSON 类型）。
func decodeSecurityStatusBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200；body = %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON 对象：%v（body = %s）", err, recorder.Body.String())
	}
	return payload
}

// securityStatusCredential 取出 defaultAdminCredential 这个 **bool**。
func securityStatusCredential(t *testing.T, payload map[string]json.RawMessage) bool {
	t.Helper()

	raw, ok := payload["defaultAdminCredential"]
	if !ok {
		t.Fatalf("响应缺少 defaultAdminCredential；实际 keys = %v", securityStatusKeys(payload))
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("defaultAdminCredential 必须是 bool，实际 = %s", string(raw))
	}
	return value
}

// securityStatusUserID 取出 defaultAdminUserId 这个 **number** 并校验等于 want。
func securityStatusUserID(t *testing.T, payload map[string]json.RawMessage, want string) {
	t.Helper()

	raw, ok := payload["defaultAdminUserId"]
	if !ok {
		t.Fatalf("响应缺少 defaultAdminUserId；实际 keys = %v", securityStatusKeys(payload))
	}
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("defaultAdminUserId 必须是 number，实际 = %s", string(raw))
	}
	if value.String() != want {
		t.Fatalf("defaultAdminUserId = %s，期望 %s", value.String(), want)
	}
}

// securityStatusKeys 返回排序后的 key 列表（仅用于失败信息与集合比较）。
func securityStatusKeys(payload map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// ---------------------------------------------------------------------------
// 第 1 组：鉴权
// ---------------------------------------------------------------------------

// TestAdminSecurityStatusRouteRequiresAuthAndAdmin 是**源码级**断言：
// router.go 里这条路由必须同时挂 controller.Auth 与 middleware.RequireAdmin。
//
// 为什么需要它：下面 TestAdminSecurityStatusAuthBoundary 用的是测试里自行组装的
// 路由，把 router.go 那一行的两个中间件都删掉，它照样全绿。
func TestAdminSecurityStatusRouteRequiresAuthAndAdmin(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "api", adminSecurityStatusRouterSource))
	if err != nil {
		t.Fatalf("读取 ../api/%s 失败：%v", adminSecurityStatusRouterSource, err)
	}
	text := string(source)

	// 限定在同一行内匹配，避免跨行贪婪吃到邻近路由。
	pattern := regexp.MustCompile(
		`(?m)apiRouter\.GET\("` + regexp.QuoteMeta("/admin/security-status") +
			`",([^)\n]*controller\.AdminSecurityStatus[^)\n]*)\)`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		t.Fatalf("应当恰好有一条 `GET /admin/security-status → controller.AdminSecurityStatus` 的注册，实际匹配到 %d 条", len(matches))
	}
	chain := matches[0][1]

	if !strings.Contains(chain, "controller.Auth") {
		t.Fatalf("该路由缺少 controller.Auth —— 未登录即可读取管理员安全状态，实际链 = %q", chain)
	}
	if !strings.Contains(chain, "middleware.RequireAdmin") {
		t.Fatalf("该路由缺少 middleware.RequireAdmin —— 普通用户即可读取管理员安全状态，实际链 = %q", chain)
	}

	// 反向钉子：这条路径不得直接挂在公开 router 上（那会整体绕过 Auth 组）。
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "admin/security-status") {
			continue
		}
		if strings.HasPrefix(trimmed, "router.") {
			t.Fatalf("security-status 不得直接挂在公开 router 上（会绕过 Auth）：%s", trimmed)
		}
	}
}

// TestAdminSecurityStatusAuthBoundary 是**行为级**断言：未登录 401、普通用户 403、
// 管理员 200。
//
// 路由组装与 router.go:346 的中间件顺序一致（Auth → RequireAdmin → handler）。
// 组级的 GlobalAPIRateLimit / BodyLimit / BlacklistIP 刻意不挂：它们与本接口的
// 鉴权语义无关，且 GlobalAPIRateLimit 需要 manager.GetManager()（单测会 nil panic）。
func TestAdminSecurityStatusAuthBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newUserControllerTestDB(t)
	installSecurityStatusDB(t, db)

	adminHash := seedAdminSecurityStatusUser(t, db, mustHashPassword(t, defaultAdminPassword))
	regular := entity.User{
		ID:             2,
		Username:       "SYNTHETIC_SECURITY_STATUS_USER",
		Password:       adminHash,
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
	}
	if err := db.Create(&regular).Error; err != nil {
		t.Fatalf("create regular user: %v", err)
	}

	router := gin.New()
	router.GET(adminSecurityStatusRoutePath, Auth, middleware.RequireAdmin, AdminSecurityStatus)

	serve := func(token string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, adminSecurityStatusRoutePath, nil)
		if token != "" {
			request.Header.Set("Authorization", token)
		}
		router.ServeHTTP(recorder, request)
		return recorder
	}

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"a-anonymous", "", http.StatusUnauthorized},
		{"b-regular-user", util.GenerateJwtForUser(2, regular.Password), http.StatusForbidden},
		{"c-admin", util.GenerateJwtForUser(1, adminHash), http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := serve(testCase.token)

			// 核心判据：被保护的接口不得回落到 200。
			if testCase.want != http.StatusOK && recorder.Code == http.StatusOK {
				t.Fatalf("%s 竟然拿到 200 —— 鉴权完全失效；body = %s",
					testCase.name, recorder.Body.String())
			}
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d，期望 %d；body = %s",
					recorder.Code, testCase.want, recorder.Body.String())
			}
			if testCase.want != http.StatusOK {
				return
			}
			// 200 的那条必须证明真的走到了 handler（而不是别的路径回了个 200）。
			payload := decodeSecurityStatusBody(t, recorder)
			if got := len(payload); got != 2 {
				t.Fatalf("管理员响应应恰好有 2 个 key，实际 %d 个：%v",
					got, securityStatusKeys(payload))
			}
			if !securityStatusCredential(t, payload) {
				t.Fatal("管理员仍是默认密码，defaultAdminCredential 应为 true")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 第 2 组：只回布尔（核心价值）
// ---------------------------------------------------------------------------

// TestAdminSecurityStatusResponseCarriesNoCredentialMaterial 断言响应 **恰好**只有
// 两个 key、类型分别为 bool / number，且响应体里不含任何凭据材料。
//
// 触发条件取「管理员仍是默认密码」这一支：它是唯一需要读库并持有 row.Password
// 的路径 —— 想「顺手把 hash 加进响应」的回归只可能发生在这里。
func TestAdminSecurityStatusResponseCarriesNoCredentialMaterial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newUserControllerTestDB(t)
	installSecurityStatusDB(t, db)
	storedHash := seedAdminSecurityStatusUser(t, db, mustHashPassword(t, defaultAdminPassword))

	recorder := adminSecurityStatusGET(t)
	payload := decodeSecurityStatusBody(t, recorder)

	// (1) key 集合严格相等 —— 多一个 key 就红，无论它叫 debugHash 还是别的什么。
	want := []string{"defaultAdminCredential", "defaultAdminUserId"}
	got := securityStatusKeys(payload)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("响应 key 必须是 %v 且仅此两个，实际 = %v（body = %s）",
			want, got, recorder.Body.String())
	}

	// (2) 类型断言：布尔 + 数字。
	if err := json.Unmarshal(payload["defaultAdminCredential"], new(bool)); err != nil {
		t.Fatalf("defaultAdminCredential 必须是 bool，实际 = %s", string(payload["defaultAdminCredential"]))
	}
	securityStatusUserID(t, payload, "1")

	body := recorder.Body.String()

	// (3) 不得出现默认密码明文。
	if strings.Contains(body, defaultAdminPassword) {
		t.Fatalf("响应体含默认密码明文 %q：%s", defaultAdminPassword, body)
	}

	// (4) 不得出现库里真实的凭据值。这是最硬的一条：不看格式，只要原样回显就红。
	//     变异验证里加 "debugHash": <库中值> 正是被这条抓住。
	if storedHash == "" || strings.Contains(body, storedHash) {
		t.Fatalf("响应体含库中存储的凭据原文：%s", body)
	}

	// (5) 不得出现 64 位十六进制串（sha256 / 十六进制化 hash 的形态）。
	if matched := regexp.MustCompile(`[0-9a-fA-F]{64}`).FindString(body); matched != "" {
		t.Fatalf("响应体含 64 位十六进制串 %q（疑似 hash 泄漏）：%s", matched, body)
	}

	// (6) 不得出现 password / hash 这类凭据字段名。
	//     注意 defaultAdminCredential 本身含 "credential"，故只禁其复合形态。
	lowered := strings.ToLower(body)
	for _, banned := range []string{"password", "hash", "credentialvalue", "secret"} {
		if strings.Contains(lowered, banned) {
			t.Fatalf("响应体含疑似凭据字段名 %q：%s", banned, body)
		}
	}
}

// ---------------------------------------------------------------------------
// 第 3 组：默认密码判定
// ---------------------------------------------------------------------------

// TestAdminSecurityStatusDetectsDefaultCredential 覆盖三条判定：
// 默认密码（bcrypt / 历史明文两种存储形态）→ true；非默认密码 → false；
// defaultAdminUserId 恒为 1。
func TestAdminSecurityStatusDetectsDefaultCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		stored  func(t *testing.T) string
		want    bool
		comment string
	}{
		{
			name:   "default-password-bcrypt",
			stored: func(t *testing.T) string { return mustHashPassword(t, defaultAdminPassword) },
			want:   true,
		},
		{
			// 历史库可能仍是明文 —— util.VerifyPassword 有 legacy 分支专门认它。
			name:   "default-password-legacy-plaintext",
			stored: func(t *testing.T) string { return defaultAdminPassword },
			want:   true,
		},
		{
			name:   "changed-password",
			stored: func(t *testing.T) string { return mustHashPassword(t, "ChangedAdmin!Pass9") },
			want:   false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := newUserControllerTestDB(t)
			installSecurityStatusDB(t, db)
			seedAdminSecurityStatusUser(t, db, testCase.stored(t))

			payload := decodeSecurityStatusBody(t, adminSecurityStatusGET(t))

			if got := securityStatusCredential(t, payload); got != testCase.want {
				t.Fatalf("defaultAdminCredential = %v，期望 %v（用例：%s）",
					got, testCase.want, testCase.name)
			}
			securityStatusUserID(t, payload, "1")
		})
	}
}

// TestAdminSecurityStatusRequiresReadableAdminRow 是判定路径的对照钉子：
// users 表存在但**没有 id=1** 时必须 false；随后插入 id=1 且为默认密码时必须 true。
//
// 后半段是对照组：没有它，「这个接口永远返回 false」也能通过前半段。
func TestAdminSecurityStatusRequiresReadableAdminRow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newUserControllerTestDB(t)
	installSecurityStatusDB(t, db)

	// 只有 id=2，没有 id=1 → 走 ErrRecordNotFound 降级，必须 200 + false。
	other := entity.User{
		ID: 2, Username: "SYNTHETIC_NOT_ADMIN_ROW", Password: mustHashPassword(t, defaultAdminPassword),
		Role: "admin", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create non-admin row: %v", err)
	}
	if securityStatusCredential(t, decodeSecurityStatusBody(t, adminSecurityStatusGET(t))) {
		t.Fatal("users 表没有 id=1 时不得报告默认凭据为 true")
	}

	// 对照组：插入 id=1 且密码为默认值 → 必须 true。
	seedAdminSecurityStatusUser(t, db, mustHashPassword(t, defaultAdminPassword))
	if !securityStatusCredential(t, decodeSecurityStatusBody(t, adminSecurityStatusGET(t))) {
		t.Fatal("对照组失效：id=1 存在且为默认密码时应当报告 true")
	}
}

// ---------------------------------------------------------------------------
// 第 4 组：降级路径
// ---------------------------------------------------------------------------

// TestAdminSecurityStatusDegradesWithoutDatabase 断言 manager 未初始化
// （securityStatusDB() 返回 nil）时：**200 + false**，不是 5xx、不是 true、不是省略字段。
func TestAdminSecurityStatusDegradesWithoutDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)

	installSecurityStatusDB(t, nil)

	recorder := adminSecurityStatusGET(t)

	// (1) 必须 200：一个「查不到安全状态」的接口把管理台首页弄红，代价远大于收益
	//     （实现 L56-58 的设计说明）。
	if recorder.Code != http.StatusOK {
		t.Fatalf("数据库未初始化时 status = %d，期望 200（不得 5xx）；body = %s",
			recorder.Code, recorder.Body.String())
	}

	payload := decodeSecurityStatusBody(t, recorder)

	// (2) defaultAdminCredential 必须显式存在且为 false —— 不是 true，也不是被省略。
	if _, exists := payload["defaultAdminCredential"]; !exists {
		t.Fatalf("降级响应省略了 defaultAdminCredential；实际 keys = %v", securityStatusKeys(payload))
	}
	if securityStatusCredential(t, payload) {
		t.Fatal("数据库未初始化时 defaultAdminCredential 不得为 true —— 那会误报管理员用了默认密码")
	}

	// (3) defaultAdminUserId 仍需为 1（降级不该改变响应形状）。
	securityStatusUserID(t, payload, "1")

	// (4) 降级响应同样不得夹带凭据材料（与第 2 组同口径，独立于 DB 状态）。
	if matched := regexp.MustCompile(`[0-9a-fA-F]{64}`).FindString(recorder.Body.String()); matched != "" {
		t.Fatalf("降级响应含 64 位十六进制串 %q：%s", matched, recorder.Body.String())
	}
}

// TestAdminSecurityStatusDegradesWithoutUsersTable 是降级家族的第二支：
// DB 句柄非 nil 但 users 表不存在（Migrator().HasTable 为 false）时同样必须 200 + false。
//
// 单独存在的原因：它与 nil 分支是不同的代码路径（实现 L65-73 vs L74+），
// 变异「把 nil 分支改成 500」不会覆盖这一支，而它同样会把管理台首页弄红。
func TestAdminSecurityStatusDegradesWithoutUsersTable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	emptyDB := newUserControllerTestDB(t)
	if err := emptyDB.Migrator().DropTable(entity.TableNameUser); err != nil {
		t.Fatalf("drop users table: %v", err)
	}
	if emptyDB.Migrator().HasTable(entity.TableNameUser) {
		t.Fatal("前置条件不成立：users 表仍然存在")
	}
	installSecurityStatusDB(t, emptyDB)

	recorder := adminSecurityStatusGET(t)
	if recorder.Code != http.StatusOK {
		t.Fatalf("users 表不存在时 status = %d，期望 200；body = %s",
			recorder.Code, recorder.Body.String())
	}
	if securityStatusCredential(t, decodeSecurityStatusBody(t, recorder)) {
		t.Fatal("users 表不存在时 defaultAdminCredential 不得为 true")
	}
}
