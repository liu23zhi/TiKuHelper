package middleware

// 设备池准入中间件的回归测试（141 §9 / §9.5 判据 S11、S12、S13a–S13e）。
//
// # 本文件存在的唯一理由：钉住「顺序」
//
// 这是全批唯一会改变**所有现存客户端**行为的改动。写错的后果是
// 「上线瞬间全站 403」——因为 4 个种子等级的 device_limit 默认全是 0，
// 而无头检查若排在 limit==0 豁免之前，所有不限量账号会当场被拒（141 §9.2）。
//
// 所以判据不能只按分支写（S13a/b/c 各一条），必须有一条专门测**顺序**的
// 用例：S13d。只写分支判据会被「顺序写反但恰好在某条路径正确」的实现骗过。
//
// # 两条贯穿全文件的纪律
//
//  1. **期望值写字面量**（403 / "level" / 0…），不引用被测常量 ——
//     常量改名或改值时断言会同步漂移 = 恒真（本仓已交付过这类假钉子）。
//  2. **每条拒绝用例都要配一条「同链路的放行用例」**，否则「一律放行」这种
//     坏实现能让全部拒绝用例……不，是反过来：一律放行会让拒绝用例红；
//     但「一律拒绝」会让放行用例红。两侧都有用例，才能钉住真正的判定。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var deviceLimitTestDBSeq atomic.Int64

// newDeviceLimitTestDB 建一个带「用户 + 等级 + 绑定表 + 拉黑表 + 系统设置」的最小库。
//
// level_id 与 device_limit_override 两列走显式 ALTER：`internal/entity/users.gen.go`
// 是 gorm.io/gen 生成文件，上面没有这两个字段（生产由 registry 的迁移补），
// 这里照做，让库结构与生产一致。
func newDeviceLimitTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:device-limit-mw-%s-%d?mode=memory&cache=shared", name, deviceLimitTestDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.AccountDeviceBinding{},
		&entity.UserDeviceBlock{}, &entity.BlacklistEntry{}, &entity.SystemSetting{},
		&entity.AccessToken{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN device_limit_override integer NOT NULL DEFAULT -1").Error; err != nil {
			t.Fatalf("add device_limit_override: %v", err)
		}
	}
	return db
}

// setDeviceLimitSwitch 写 system_settings.device_limit_enabled（默认关闭，必须显式打开）。
func setDeviceLimitSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	if err := db.Where(map[string]any{"key": "device_limit_enabled"}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear device_limit_enabled: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{
		Key: "device_limit_enabled", Value: value, Remark: "测试：账户设备池上限开关",
	}).Error; err != nil {
		t.Fatalf("set device_limit_enabled=%s: %v", value, err)
	}
}

// seedDeviceLimitLevel 造一个等级并返回 id。deviceLimit=0 即「不限」。
func seedDeviceLimitLevel(t *testing.T, db *gorm.DB, rank int32, deviceLimit int32) int32 {
	t.Helper()
	level := entity.UserLevel{
		Name:        fmt.Sprintf("门禁档-%d", rank),
		Rank:        rank,
		Enabled:     true,
		DeviceLimit: deviceLimit,
	}
	if err := db.Create(&level).Error; err != nil {
		t.Fatalf("create level rank=%d limit=%d: %v", rank, deviceLimit, err)
	}
	return level.ID
}

// seedDeviceLimitUser 造一个用户；levelID=0 表示「未评估」（level_missing）。
// override=-1 表示「不覆盖」，>=0 表示用户自设上限。
func seedDeviceLimitUser(t *testing.T, db *gorm.DB, username string, levelID int32, override int32) *entity.User {
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
	if override >= 0 {
		if err := db.Table(entity.TableNameUser).Where("id = ?", user.ID).
			Update("device_limit_override", override).Error; err != nil {
			t.Fatalf("set override: %v", err)
		}
	}
	return &user
}

// seedDeviceLimitBinding 预先登记一台设备（模拟「已用 1 台」）。
func seedDeviceLimitBinding(t *testing.T, db *gorm.DB, userID int32, deviceID string) {
	t.Helper()
	row := entity.AccountDeviceBinding{UserID: userID, DeviceID: deviceID}
	row.OwnerKind = entity.DeviceBindingOwnerKindLogin
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed binding %s: %v", deviceID, err)
	}
}

// seedDeviceLimitAccessToken 落一个可用的访问令牌（扩展/脚本用的那种），返回明文。
//
// 明文不能从库里读回（只存 sha256），所以由本函数生成并返回。
func seedDeviceLimitAccessToken(t *testing.T, db *gorm.DB, userID int32, name string) string {
	t.Helper()
	plain := "tk_" + strings.ReplaceAll(name, "-", "") + "0123456789"
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(plain))))
	token := entity.AccessToken{
		UserID:          userID,
		Name:            name,
		TokenHash:       hex.EncodeToString(sum[:]),
		TokenPrefix:     plain[:12],
		TokenValue:      plain,
		Status:          "enabled",
		TotalQuotaLimit: 0,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create access token %s: %v", name, err)
	}
	return plain
}

// deviceLimitTestRequest 是一次经过真实中间件链的请求。
type deviceLimitTestRequest struct {
	// user 非 nil 时由前置中间件写进上下文（模拟 controller.Auth 的效果）。
	user *entity.User
	// scriptSession 为真时前置中间件同时写 "script_session"（模拟 AuthAccessToken）。
	scriptSession bool
	// deviceID 为空 = 不带 X-Device-Id 头（「无头请求」）。
	deviceID string
	// clientInstance 为空 = 不带 X-Device-Instance 头（旧客户端 / 冲突判定跳过）。
	//
	// 非空时才会驱动 141 §4 的冲突检测：同 device_id + 不同 instance ⇒ 409。
	clientInstance string
	// authorization / scriptIdentity 走中间件自己的凭据解析路径（模拟 /api/search 这种无鉴权中间件的路由）。
	authorization  string
	scriptIdentity string
}

// doDeviceLimitRequest 用**真实的** DeviceLimitWith 组一条路由并发一次请求。
//
// 不直接调 handler 函数：那会绕开 gin 的 c.Next()/Abort 语义，
// 而「中间件放行了但链路仍被打断」正是 gin 里最容易出错的形态。
func doDeviceLimitRequest(t *testing.T, db *gorm.DB, req deviceLimitTestRequest) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if req.user != nil {
			c.Set("user", req.user)
		}
		if req.scriptSession {
			// AuthAccessToken 的写法是 c.Set("script_session", session)（user.go:328）。
			// 这里放一个非空结构体足以驱动「存在性」判定。
			c.Set("script_session", struct{ ID int32 }{ID: 7})
		}
		c.Next()
	})
	router.Use(DeviceLimitWith(db))
	router.POST("/probe", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "reached-handler"})
	})

	httpReq := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(`{}`))
	httpReq.Header.Set("Content-Type", "application/json")
	if req.deviceID != "" {
		httpReq.Header.Set(DeviceIDHeader, req.deviceID)
	}
	if req.clientInstance != "" {
		httpReq.Header.Set(DeviceInstanceHeader, req.clientInstance)
	}
	if req.authorization != "" {
		httpReq.Header.Set("Authorization", req.authorization)
	}
	if req.scriptIdentity != "" {
		httpReq.Header.Set("X-Script-Identity", req.scriptIdentity)
	}
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httpReq)
	return out
}

// assertDeviceLimitAllowed 断言放行（链路走到 handler）。
func assertDeviceLimitAllowed(t *testing.T, out *httptest.ResponseRecorder, what string) {
	t.Helper()
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), "reached-handler") {
		t.Fatalf("%s 应当放行到 handler，实际 status=%d body=%s", what, out.Code, out.Body.String())
	}
}

// assertDeviceLimitHeadlessRejected 断言被「无头」拒绝（403 + DEVICE_ID_REQUIRED）。
func assertDeviceLimitHeadlessRejected(t *testing.T, out *httptest.ResponseRecorder, what string) {
	t.Helper()
	if out.Code != http.StatusForbidden {
		t.Fatalf("%s 应当 403（缺 X-Device-Id），实际 status=%d body=%s", what, out.Code, out.Body.String())
	}
	if !strings.Contains(out.Body.String(), "DEVICE_ID_REQUIRED") {
		t.Fatalf("%s 的 403 必须带 code=DEVICE_ID_REQUIRED，实际 body=%s", what, out.Body.String())
	}
	if !strings.Contains(out.Body.String(), "请升级客户端以支持设备识别") {
		t.Fatalf("%s 的文案必须是「请升级客户端以支持设备识别」（用户要知道去升级），实际 body=%s",
			what, out.Body.String())
	}
}

// ─────────────────────── 前置：总开关 ───────────────────────

// 判据：开关关闭（默认）时，限设备账号的无头请求必须放行。
//
// 这是「代码先上、开关后开」的部署前提：关着的时候全站行为必须与引入本中间件
// 之前**完全一致**，否则上线本 PR 就等于上线限制。
func TestDeviceLimitSwitchOffAllowsEverything(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "switch-off", levelID, -1)
	// 不写 device_limit_enabled ⇒ 默认关闭。
	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user})
	assertDeviceLimitAllowed(t, out, "开关关闭 + 限设备账号 + 无 device 头")
}

// 判据：开关打开 + 限设备账号 + 无 device 头 ⇒ 403（S12）。
//
// 与上一条成对：一条证明「关着不拦」，一条证明「开着会拦」。
// 只写其中一条，把开关读反了也不会变红。
func TestDeviceLimitSwitchOnRejectsHeadlessLimitedAccount(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "limited-headless", levelID, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user})
	assertDeviceLimitHeadlessRejected(t, out, "开关打开 + 限设备账号 + 无 device 头（S12）")
}

// ─────────────────────── 第 0 步：limit == 0 的三种来源（S13a/b/c） ───────────────────────

// S13a：等级本身不限（device_limit=0，4 个种子等级的默认值）+ 无头 ⇒ **放行**。
func TestDeviceLimitUnlimitedFromLevelAllowsHeadless(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 0)
	user := seedDeviceLimitUser(t, db, "unlimited-level", levelID, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user})
	assertDeviceLimitAllowed(t, out, "limit=0（source=level）+ 无 device 头（S13a）")
}

// S13b：用户自设 override=0 + 无头 ⇒ **放行**。
//
// 这一条专门抓「豁免条件写成 source=="level" && limit==0」的实现：
// 那种写法下 override=0 的账号在这里会被 403。
func TestDeviceLimitUnlimitedFromUserOverrideAllowsHeadless(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	// 等级是限 1 台，但用户自己放宽到不限 —— 生效值是 override。
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "unlimited-override", levelID, 0)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user})
	assertDeviceLimitAllowed(t, out, "limit=0（source=user_override）+ 无 device 头（S13b）")
}

// S13c：等级缺失（level_id=0 未评估，所有新注册用户）+ 无头 ⇒ **放行**。
//
// 这一条最容易被漏（141 §9.3 第 3 条）：若把 level_missing 当成「上限 0 台」而拒绝，
// **所有新注册用户当场无法搜题**。
func TestDeviceLimitUnlimitedFromLevelMissingAllowsHeadless(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	// levelID=0：从未被评级（新注册用户的真实状态）。
	user := seedDeviceLimitUser(t, db, "unlimited-level-missing", 0, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user})
	assertDeviceLimitAllowed(t, out, "limit=0（source=level_missing）+ 无 device 头（S13c）")
}

// ─────────────────────── S13d：顺序防线（本文件最重要的一条） ───────────────────────

// TestDeviceLimitOrderLimitExemptionPrecedesHeadlessCheck 是**专测顺序**的判据。
//
// # 它为什么必须这样构造
//
// 单看 S13a/b/c 三条，一个「顺序写反」的实现也可能全绿 —— 只要它在某条路径上
// 恰好先判了 limit（比如先走了一次 EffectiveDeviceLimit 做别的用途）。
// 反过来，单看 S12（限设备账号无头 ⇒ 403），一个「完全没有无头检查」的实现
// 也能通过……不，会红。所以两侧成对是必要的，但仍不足以钉顺序。
//
// 本用例在**同一个 router、同一次测试**里同时发两个请求：
//
//	A. limit=0 账号 + 无 device 头 → 必须 200（顺序写反 ⇒ 这里变 403）
//	B. 限 1 台账号 + 无 device 头 → 必须 403（无头检查被删 ⇒ 这里变 200）
//
// A 排除「无头检查排到 limit 之前」，B 排除「无头检查不存在」。
// 两条同时成立，才唯一地锁定「先豁免、后无头检查、且无头检查真的在」。
func TestDeviceLimitOrderLimitExemptionPrecedesHeadlessCheck(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)

	unlimitedLevel := seedDeviceLimitLevel(t, db, 1, 0)
	unlimited := seedDeviceLimitUser(t, db, "order-unlimited", unlimitedLevel, -1)

	limitedLevel := seedDeviceLimitLevel(t, db, 2, 1)
	limited := seedDeviceLimitUser(t, db, "order-limited", limitedLevel, -1)

	// A：不限量账号的无头请求必须放行。
	// 变异「把无头检查挪到 limit 检查之前」⇒ 本行变 403（全站 403 的那个事故形态）。
	outA := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: unlimited})
	if outA.Code != http.StatusOK {
		t.Fatalf("S13d-A 失败：limit=0 账号的无头请求被拒（status=%d body=%s）。"+
			"这就是「顺序写反 ⇒ 上线瞬间全站 403」的确切形态 —— "+
			"无头检查必须排在 limit==0 豁免**之后**", outA.Code, outA.Body.String())
	}

	// B：对照组。同一条链路、同样无头，但账号限设备 ⇒ 必须 403。
	// 变异「删掉无头检查」⇒ 本行变 200。
	outB := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: limited})
	if outB.Code != http.StatusForbidden {
		t.Fatalf("S13d-B 失败：限设备账号的无头请求被放行（status=%d body=%s）—— "+
			"无头检查不存在或没生效（S12）", outB.Code, outB.Body.String())
	}
}

// TestDeviceLimitSourceKeepsSingleCondition 是 S13d 的**源码级**补充防线。
//
// 行为用例能证明「当前实现顺序正确」，但抓不住一种更隐蔽的退化：
// 有人把豁免条件从 `limit == 0` 改成 `source == "level" && limit == 0`。
// 那种写法在 S13a 上仍是绿的（level 正是它的来源），只有 b/c 会红 ——
// 若将来 b/c 被误删，顺序就悄无声息地退化了一次。
//
// # 断言形态为什么是「结构」而不是「字符串黑名单」
//
// 初版这里写了一串 `source == "level"` / `source == "user_override"` 的字符串
// 黑名单，实测被同一个变异骗过：把变量改名成 `limitSource` 就完全绕开了
// 黑名单（黑名单是枚举，而枚举必漏 —— 与 141 §9.3 是同一个道理）。
//
// 所以改成两条**非枚举**的结构判据：
//
//	① 豁免那行的条件里不得出现 `&&` / `||`（单一条件，不许组合）
//	② 豁免那行的条件里不得出现任何含 "source" 的标识符（大小写不敏感）
//
// 这两条与变量怎么命名无关，也与 source 有多少种取值无关。
func TestDeviceLimitSourceKeepsSingleCondition(t *testing.T) {
	source, err := os.ReadFile("device_limit.go")
	if err != nil {
		t.Fatalf("读取 device_limit.go 失败：%v", err)
	}
	text := stripGoCommentsForDeviceLimit(string(source))

	// 判据 0：必须存在单一条件豁免（DevicePoolUnlimited 即 0）。
	const marker = "if limit == service.DevicePoolUnlimited"
	index := strings.Index(text, marker)
	if index < 0 {
		t.Fatalf("device_limit.go 里找不到 `%s` 这个单一条件豁免 —— "+
			"141 §9.3 要求判据只写 limit == 0，不枚举 source", marker)
	}
	// 取出该 if 的条件部分（`if` 之后到 `{` 之前）。
	rest := text[index+len(marker):]
	brace := strings.Index(rest, "{")
	if brace < 0 {
		t.Fatalf("找不到豁免语句的行尾 `{`，源码结构已变，本用例的路径假设失效")
	}
	condition := "limit == service.DevicePoolUnlimited" + rest[:brace]

	// 判据 ①：单一条件，不得组合。
	for _, op := range []string{"&&", "||"} {
		if strings.Contains(condition, op) {
			t.Fatalf("豁免条件里出现了逻辑运算符 %q，实际条件 = %q —— "+
				"必须是单一 `limit == 0`，组合条件必然要枚举 source（141 §9.3）", op, condition)
		}
	}
	// 判据 ②：条件里不得引用 source（无论变量叫什么名字）。
	if strings.Contains(strings.ToLower(condition), "source") {
		t.Fatalf("豁免条件里引用了 source，实际条件 = %q —— "+
			"判据只能是 `limit == 0`，与「这个 0 来自哪个 source」无关；"+
			"枚举 source 必漏将来新增的来源（141 §9.3）", condition)
	}
}

// stripGoCommentsForDeviceLimit 去掉 // 与 /* */ 注释，避免把反例说明误判成实际代码。
//
// 本仓踩过这个坑：注释里写着「严禁写成 `source == \"level\" && limit == 0`」，
// 不剥注释时上面那条断言必然假红。
func stripGoCommentsForDeviceLimit(source string) string {
	var out strings.Builder
	inLineComment := false
	inBlockComment := false
	runes := []rune(source)
	for i := 0; i < len(runes); i++ {
		switch {
		case inLineComment:
			if runes[i] == '\n' {
				inLineComment = false
				out.WriteRune(runes[i])
			}
		case inBlockComment:
			if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlockComment = false
				i++
			}
		case runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '/':
			inLineComment = true
			i++
		case runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '*':
			inBlockComment = true
			i++
		default:
			out.WriteRune(runes[i])
		}
	}
	return out.String()
}

// ─────────────────────── S11 / S13e：脚本豁免 ───────────────────────

// S13e（判据 S11 的中间件侧）：有 script_session + 限设备 + 无头 ⇒ **放行**。
//
// 用户原话：「考试脚本不受到设备数量影响」（137 §5.2）。考试脚本是一次性运行
// 会话，不参与设备池 —— 它本来也不会带 X-Device-Id（那是扩展的 header）。
//
// 变异「去掉 script_session 跳过」⇒ 本用例必红。
func TestDeviceLimitScriptSessionBypassesHeadlessCheck(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "script-session", levelID, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{
		user:          user,
		scriptSession: true,
	})
	assertDeviceLimitAllowed(t, out, "有 script_session + 限设备账号 + 无 device 头（S11/S13e）")
}

// 反向对照：同一个限设备账号，**没有** script_session 时同样无头请求必须 403。
//
// 与上一条成对，排除「脚本豁免写成无条件放行」的实现。
func TestDeviceLimitScriptSessionFlagIsWhatAllows(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "script-session-control", levelID, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user, scriptSession: false})
	assertDeviceLimitHeadlessRejected(t, out, "无 script_session 的同一账号（对照）")
}

// ─────────────────────── 第 2/3 步：带 device 头的正常判定 ───────────────────────

// 判据：限 1 台、已用 1 台、再来一台**新的** ⇒ 403 DEVICE_LIMIT_EXCEEDED。
//
// 断言的是 service 层判定真的被调用（而不是中间件自己比较数字）：
// reason 必须来自 DevicePoolReasonLimitExceeded，message 来自 service 的文案。
func TestDeviceLimitRejectsNewDeviceWhenFull(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "full-account", levelID, -1)
	seedDeviceLimitBinding(t, db, user.ID, "already-used")

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user, deviceID: "brand-new"})
	if out.Code != http.StatusForbidden {
		t.Fatalf("已满账号的新设备应当 403，实际 status=%d body=%s", out.Code, out.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应体失败：%v body=%s", err, out.Body.String())
	}
	if body["reason"] != "limit-exceeded" {
		t.Fatalf("reason 必须是 service 的稳定原因串 limit-exceeded（证明判定走的是 device_pool.go），实际 %v",
			body["reason"])
	}
	if body["code"] != "DEVICE_LIMIT_EXCEEDED" {
		t.Fatalf("code 必须是 DEVICE_LIMIT_EXCEEDED，实际 %v", body["code"])
	}
}

// 判据：已登记的设备在额度满时仍必须放行（不占新槽）。
//
// 反例后果很重：用户会被自己的设备锁在门外。
func TestDeviceLimitAllowsAlreadyBoundDeviceWhenFull(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "bound-device", levelID, -1)
	seedDeviceLimitBinding(t, db, user.ID, "known-device")

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user, deviceID: "known-device"})
	assertDeviceLimitAllowed(t, out, "已登记设备 + 额度已满")
}

// 判据：有空槽时放行**并且落一条绑定行**。
//
// 不落库的后果：已用台数恒为 0，上限形同虚设（每次请求都被判成「新设备且未满」）。
func TestDeviceLimitRegistersNewDeviceWithinLimit(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 2)
	user := seedDeviceLimitUser(t, db, "register-new", levelID, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user, deviceID: "fresh-device"})
	assertDeviceLimitAllowed(t, out, "新设备 + 有空槽")

	var count int64
	if err := db.Model(&entity.AccountDeviceBinding{}).
		Where("user_id = ? AND device_id = ?", user.ID, "fresh-device").Count(&count).Error; err != nil {
		t.Fatalf("查询绑定行失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("放行后必须落 1 条绑定行（否则已用台数恒为 0、上限失效），实际 %d 条", count)
	}
}

// 判据：禁止档（device_limit < 0）⇒ 403 forbidden。
//
// Q16 遗留出口：0 已被「不限」占用，负数才是「禁止任何设备」。
func TestDeviceLimitRejectsForbiddenLevel(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, -1)
	user := seedDeviceLimitUser(t, db, "forbidden-level", levelID, -1)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user, deviceID: "any-device"})
	if out.Code != http.StatusForbidden {
		t.Fatalf("禁止档应当 403，实际 status=%d body=%s", out.Code, out.Body.String())
	}
	if !strings.Contains(out.Body.String(), "forbidden") {
		t.Fatalf("reason 必须是 forbidden，实际 body=%s", out.Body.String())
	}
}

// 判据：取不到数据库句柄时 fail-open 放行。
//
// 设备池是权益限制不是安全边界：DB 抖一下不该让全站搜不了题。
func TestDeviceLimitFailOpenWithoutDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user", &entity.User{ID: 42, Username: "no-db", Role: "user", Status: "enabled"})
		c.Next()
	})
	// nil ⇒ 走 manager 单例；单测里 manager 为 nil ⇒ 句柄为空 ⇒ fail-open。
	router.Use(DeviceLimitWith(nil))
	router.POST("/probe", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"message": "reached-handler"}) })

	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(`{}`)))
	assertDeviceLimitAllowed(t, out, "取不到数据库句柄（fail-open）")
}

// 判据：辨识不到账号（匿名）时放行，**不写 401**。
//
// /api/search 支持匿名默认账号，在这里 401 会把该功能打死。
// 鉴权语义属于后续 handler。
func TestDeviceLimitAnonymousRequestIsNotRejected(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)

	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{})
	assertDeviceLimitAllowed(t, out, "匿名请求（无 user、无凭据）")
}

// 判据：中间件自己解析凭据（模拟 /api/search 这种没有鉴权中间件的路由）。
//
// 这条路径缺失的后果：设备门禁在最主要的搜题入口上完全不生效。
func TestDeviceLimitResolvesCredentialWhenAuthMiddlewareAbsent(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "credential-path", levelID, -1)
	plain := seedDeviceLimitAccessToken(t, db, user.ID, "credential-token")

	// 有效凭据 + 无头 ⇒ 403（证明凭据真的被解析了：若解析失败会按匿名放行）。
	out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{authorization: "Bearer " + plain})
	assertDeviceLimitHeadlessRejected(t, out, "自带凭据解析 + 限设备账号 + 无头")

	// 无效凭据 ⇒ 按匿名放行（交给 handler 给 401，而不是在这里 403）。
	bad := doDeviceLimitRequest(t, db, deviceLimitTestRequest{authorization: "Bearer not-a-real-token"})
	assertDeviceLimitAllowed(t, bad, "无效凭据（按匿名放行）")
}

// ─────────────── 冲突检测的接线（409）与同实例重连 ───────────────
//
// 这一组是本轮**补的接线钉子**。C2 实现了完整的 DecideDeviceRegistration，
// 单测也全绿，但 device_limit.go 里把它返回的 decision 用 `_` 丢掉了 ——
// 于是真实请求路径上**永远发不出 409**，客户端只会看到「设备没登记上」，
// 下次又被判新设备，槽位静默不增长。
//
// 形态与 124 号记的「代码都在、只差接线」完全一致，所以两条都必须测**接线**，
// 不能只测 service 层的判定函数（那层一直是绿的，发现不了这个洞）。

// S5 接线：跨账号同 device_id + 不同 instance ⇒ 409 + code=DEVICE_ID_CONFLICT。
func TestDeviceLimitEmits409OnDeviceIDConflict(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)

	// 账号 A 先把 device_id 登记走（同一台设备、instance=a-run）。
	levelA := seedDeviceLimitLevel(t, db, 1, 5)
	userA := seedDeviceLimitUser(t, db, "conflict-a", levelA, -1)
	first := doDeviceLimitRequest(t, db, deviceLimitTestRequest{
		user: userA, deviceID: "shared-device-xyz", clientInstance: "run-a",
	})
	assertDeviceLimitAllowed(t, first, "账号 A 首次登记该 device_id 应当放行")

	// 账号 B 拿着**同一个 device_id**、但 instance 不同（＝镜像复制出来的另一份）
	// 来登记 ⇒ 必须 409，且 code 精确等于协议字段。
	levelB := seedDeviceLimitLevel(t, db, 2, 5)
	userB := seedDeviceLimitUser(t, db, "conflict-b", levelB, -1)
	second := doDeviceLimitRequest(t, db, deviceLimitTestRequest{
		user: userB, deviceID: "shared-device-xyz", clientInstance: "run-b",
	})

	if second.Code != http.StatusConflict {
		t.Fatalf("跨账号同 device_id + 不同 instance 应当 409，实际 status=%d body=%s",
			second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), `"code":"DEVICE_ID_CONFLICT"`) {
		t.Fatalf("409 必须带 code=DEVICE_ID_CONFLICT（客户端据它决定重新生成并重试），实际 body=%s",
			second.Body.String())
	}
	// 反向断言：冲突绝不能被报成 403。403 是「槽位用完，用户去删设备」，
	// 409 是「这个 ID 归别人，客户端换 ID」—— 混了用户会删无可删。
	if second.Code == http.StatusForbidden {
		t.Fatal("冲突必须报 409 而不是 403：两者对客户端的下一步动作要求相反")
	}
}

// S6 对照组：同账号 + 同 device_id + **同** instance ⇒ 正常（不冲突）。
//
// 没有这一条，「只要见过 device_id 就判冲突」的实现也能让 S5 变绿，
// 但那会让用户每次重开浏览器都被判冲突、每次换 ID、槽位迅速耗尽。
func TestDeviceLimitAllowsSameInstanceReconnect(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	levelID := seedDeviceLimitLevel(t, db, 1, 5)
	user := seedDeviceLimitUser(t, db, "same-instance", levelID, -1)

	for i := 0; i < 3; i++ {
		out := doDeviceLimitRequest(t, db, deviceLimitTestRequest{
			user: user, deviceID: "stable-device", clientInstance: "same-run",
		})
		assertDeviceLimitAllowed(t, out,
			"同账号同 device_id 同 instance 的重复请求必须放行（S6 对照组）")
	}
}

// 无 instance 头的旧客户端：冲突判定整体跳过，行为与加这个字段之前一致。
func TestDeviceLimitSkipsConflictWhenInstanceMissing(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)

	levelA := seedDeviceLimitLevel(t, db, 1, 5)
	userA := seedDeviceLimitUser(t, db, "noinst-a", levelA, -1)
	assertDeviceLimitAllowed(t, doDeviceLimitRequest(t, db, deviceLimitTestRequest{
		user: userA, deviceID: "legacy-device",
	}), "旧客户端（无 instance）首次登记放行")

	levelB := seedDeviceLimitLevel(t, db, 2, 5)
	userB := seedDeviceLimitUser(t, db, "noinst-b", levelB, -1)
	assertDeviceLimitAllowed(t, doDeviceLimitRequest(t, db, deviceLimitTestRequest{
		user: userB, deviceID: "legacy-device",
	}), "旧客户端没有可比对的 instance，不得判冲突（否则老客户端集体失效）")
}
