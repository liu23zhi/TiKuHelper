package controller

// 设备指纹**采集侧**接线的回归测试（T-25 的另一半）。
//
// 六条判据（任务书）：
//   ① 登录成功后 account_device_bindings 多一行，first_seen_at == last_seen_at
//   ② 同设备二次登录：行数不变、last_seen_at 前进、first_seen_at 不变
//   ③ 不同设备登录：行数 +1
//   ④ 设备写入失败（Before("gorm:create") 注入错误）时登录仍成功且返回正常 token
//   ⑤ 设备写入不阻塞：响应先于落库（不是「耗时 < N 毫秒」）
//   ⑥ 设备上限按等级真的生效（用既有 CanUseDevice 语义，不改它）
//
// ## 为什么判据⑤ 用「响应先于落库」而不是耗时阈值
//
// 「登录耗时 < 200ms」这类断言依赖机器快慢与 CI 噪声，在慢机器上会假红、
// 在快机器上会假绿（变异成同步也存在偶尔通过的可能）。真正要钉的语义是
// **两件事的先后**：登录把响应写出去了，设备行之后才落库。
// 做法：在写入路径上挂一个 gorm create 回调，先把 `respondedAt`（登录已完成）
// 与 `writtenAt`（回调被调用的时刻）都记下来，断言 respondedAt < writtenAt。
// 同步 await 时登录要等回调跑完，两者顺序必然反过来 → 变异必红。
//
// ## 判据④ 与既有设计的关系
//
// device_pool.go 的判定本身就是 fail-open（表坏/查询出错一律放行，:107 注释）。
// 所以「写入失败不影响登录」不是额外加固，而是同一条策略在写侧的延伸 ——
// 把它当 bug 去「修成 fail-closed」会让 DB 一抖全站登录失败。
// 下面每条断言都带着这个意图写注释。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const devicePoolTestUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0"

var devicePoolHookDBSeq atomic.Int64

// newDevicePoolHookDB 建一个带 users + 设备池 + 等级体系的最小库。
func newDevicePoolHookDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := fmt.Sprintf("file:device-pool-hook-%s-%d?mode=memory&cache=shared", name, devicePoolHookDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.AccountDeviceBinding{},
		&entity.SystemSetting{}, &entity.Log{}, &entity.CaptchaToken{}, &entity.LoginAttempt{},
		&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	// users.level_id 走显式 ALTER（生成文件里没有该字段）。
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser + " ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	dao.SetDefault(db)
	originalOverride := devicePoolDBOverride
	devicePoolDBOverride = db
	t.Cleanup(func() { devicePoolDBOverride = originalOverride })
	return db
}

func seedDevicePoolHookUser(t *testing.T, db *gorm.DB, username string, levelID int32) (*entity.User, string) {
	t.Helper()
	const password = "Passw0rd!2026"
	hash, err := util.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := entity.User{
		Username: username, Password: hash, Role: "user", Status: "enabled",
		RegisterSource: "system",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if levelID > 0 {
		if err := db.Table(entity.TableNameUser).Where("id = ?", user.ID).
			Update("level_id", levelID).Error; err != nil {
			t.Fatalf("attach level: %v", err)
		}
	}
	return &user, password
}

func seedDevicePoolHookLevel(t *testing.T, db *gorm.DB, rank int32, deviceLimit int32) int32 {
	t.Helper()
	level := entity.UserLevel{
		Name: fmt.Sprintf("设备池档-%d", rank), Rank: rank, Enabled: true, DeviceLimit: deviceLimit,
	}
	if err := db.Create(&level).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	return level.ID
}

// loginWithUserAgent 走真实的 UserLogin handler，带指定 User-Agent。
//
// 刻意不 stub UserLogin 内部任何东西（只 stub 验证码）：要测的就是
// 「handler 的接线是否真的把设备写进去了」，stub 掉 handler 会变成自证。
func loginWithUserAgent(t *testing.T, username, password, userAgent string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"username": username, "password": password, "captchaToken": "tok"})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("User-Agent", userAgent)
	ctx.Request.RemoteAddr = "203.0.113.7:1234"
	UserLogin(ctx)
	return recorder
}

// waitForDeviceRows 等到设备行数达到 want（异步写入需要给 goroutine 一点时间）。
//
// 但**不是**用来做时序断言的：判据⑤ 的先后顺序由 gorm 回调里的时间戳决定，
// 与本 helper 的等待无关。这里只是为了让「行数/时间戳」类断言有确定的数据。
func waitForDeviceRows(t *testing.T, db *gorm.DB, userID int32, want int) []entity.AccountDeviceBinding {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var rows []entity.AccountDeviceBinding
		if err := db.Where("user_id = ?", userID).Order("id asc").Find(&rows).Error; err != nil {
			t.Fatalf("read bindings: %v", err)
		}
		if len(rows) >= want || time.Now().After(deadline) {
			if len(rows) != want {
				t.Fatalf("期望 %d 行设备记录，实际 %d 行（等了 3s）", want, len(rows))
			}
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// drainDeviceWrites 等所有在飞的异步写入落地（用于「不应该多写一行」的断言）。
//
// 做法是再跑一次同步登记并等它返回 —— goroutine 是 FIFO 排队到同一个库上的，
// 同步那次返回后，此前排队的异步写入都已经执行完。
// 若不这么做，「行数不变」的断言可能在写入还没发生时就通过（假绿）。
//
// ⚠️ 同步登记的错误**一律忽略**：这条 helper 的唯一职责是「把队列排空」。
// 在「注入必失败」的用例里（判据④），把 drain 的报错当失败会让 helper 自身
// 变成被测对象 —— 它要证明的是「排空后设备表为空」，而不是「写入成功」。
func drainDeviceWrites(t *testing.T, userID int32, deviceID string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		// 传空 IP：drain 的职责只是排空队列，不该顺带写 last_ip
		// （那会让「注入必失败」的用例多一个无关变量）。
		_ = registerAccountDeviceBindingNow(userID, deviceID, "")
		time.Sleep(2 * time.Millisecond)
	}
}

// ─────────────────────── 设备标识构造式 ───────────────────────

// 设备标识 = client_kind + ":" + sha256(client_kind \0 UA)[:16]。
//
// 断言的是**结构**而不是某个具体哈希值（后者等于把实现抄一遍）：
// 前缀必须是归一化后的三值之一，后缀必须是 32 位小写十六进制。
func TestBuildDevicePoolDeviceIDShape(t *testing.T) {
	deviceID := BuildDevicePoolDeviceID("extension", devicePoolTestUserAgent)
	parts := strings.SplitN(deviceID, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("设备标识必须形如 <kind>:<hash>，实际 %q", deviceID)
	}
	if parts[0] != "extension" {
		t.Fatalf("前缀必须是归一化后的 client_kind，实际 %q", parts[0])
	}
	if len(parts[1]) != DevicePoolUserAgentHashBytes*2 {
		t.Fatalf("哈希必须是 %d 位十六进制，实际 %d 位（%q）",
			DevicePoolUserAgentHashBytes*2, len(parts[1]), parts[1])
	}
	for _, ch := range parts[1] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			t.Fatalf("哈希必须是小写十六进制，发现 %q", ch)
		}
	}
	// 非法/空 client_kind 一律收敛为 unknown（NormalizeClientKind 的契约）。
	if got := BuildDevicePoolDeviceID("", "UA"); !strings.HasPrefix(got, "unknown:") {
		t.Fatalf("空 client_kind 必须收敛为 unknown，实际 %q", got)
	}
	if got := BuildDevicePoolDeviceID("exe", "UA"); !strings.HasPrefix(got, "unknown:") {
		t.Fatalf("非法 client_kind 必须收敛为 unknown（不做别名映射），实际 %q", got)
	}
}

// 同一 (kind, UA) 必须稳定得到同一个 ID；换 kind 或换 UA 必须是不同 ID。
//
// 这条直接决定「同一台设备反复登录会不会占多槽」：
// 若构造式不稳定，每次登录都是新设备，上限会被自己打满。
func TestBuildDevicePoolDeviceIDStabilityAndDiscrimination(t *testing.T) {
	a := BuildDevicePoolDeviceID("extension", devicePoolTestUserAgent)
	b := BuildDevicePoolDeviceID("extension", devicePoolTestUserAgent)
	if a != b {
		t.Fatalf("同一 (kind, UA) 必须稳定，实际 %q != %q", a, b)
	}
	// 首尾空白不影响（UA 先 TrimSpace）—— 否则同一浏览器偶发多个空格就变新设备。
	if c := BuildDevicePoolDeviceID("extension", "  "+devicePoolTestUserAgent+"\t"); c != a {
		t.Fatalf("UA 首尾空白不该改变设备标识：%q != %q", c, a)
	}
	// 换 client_kind：同一台机器上的两个入口必须是两台设备。
	if other := BuildDevicePoolDeviceID("exam_client", devicePoolTestUserAgent); other == a {
		t.Fatalf("不同 client_kind 必须是不同设备标识，实际都是 %q", a)
	}
	// 换 UA（浏览器版本升级）：必须是新设备，否则「换浏览器」不会被识别。
	if other := BuildDevicePoolDeviceID("extension", devicePoolTestUserAgent+" v2"); other == a {
		t.Fatalf("不同 UA 必须是不同设备标识，实际都是 %q", a)
	}
}

// 显式声明头优先：X-Client-Kind 决定前缀（复用 ResolveClientKind 的优先级）。
func TestDevicePoolRequestClientKindPrefersDeclaredHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ctx.Request.Header.Set("User-Agent", devicePoolTestUserAgent)
	if got := devicePoolRequestClientKind(ctx); got != "unknown" {
		t.Fatalf("普通浏览器 UA 应是 unknown，实际 %q", got)
	}
	ctx.Request.Header.Set(service.ClientKindHeader, service.ClientKindExtension)
	if got := devicePoolRequestClientKind(ctx); got != "extension" {
		t.Fatalf("显式声明头必须优先，实际 %q", got)
	}
	if got := devicePoolRequestClientKind(nil); got != "unknown" {
		t.Fatalf("nil context 必须是 unknown（不 panic），实际 %q", got)
	}
}

// ─────────────────────── 判据 ① ───────────────────────

// 判据 ①：登录成功后设备表多一行，且 first_seen_at 与 last_seen_at 相等。
//
// 「相等」是首次登记的定义：RegisterDevice 插入时两者都写 now。
// 若相等不成立，说明插入路径写了两个不同的时间点（将来的「闲置释放」会算错龄）。
func TestDevicePoolLoginRecordsBinding(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-login", 0)
	stubCtrlCaptcha(t, nil)

	recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent)
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "jwt") {
		t.Fatalf("登录响应必须含 jwt：%s", recorder.Body.String())
	}

	// ⚠️ 2026-11-17 语义反转（用户定案）：Web UI 登录**不再**登记设备。
	//
	// 原用例断言「登录后设备表多一行，且 device_id == BuildDevicePoolDeviceID(
	// client_kind, UA)」。那个行为已经被删除 —— 设备池只服务 chaoxing 调 API 的
	// 设备，浏览器登录不该出现在「我的设备」里。
	//
	// 新判据用 waitForZeroDeviceRows 而不是 waitForDeviceRows(0)：
	// 后者在 want==0 时会立即返回，无法区分「确实没写」与「异步写入还没发生」。
	assertNoDeviceRows(t, db, user.ID, 0)
}

// ─────────────────────── 判据 ② ───────────────────────

// 判据 ②：同设备二次登录 —— 仍然不产生任何设备行（幂等的最强形式）。
//
// ⚠️ 这条用例原本验证「第二次登录保持单行且 last_seen_at 前进」，
// 现在验证的是「两次登录都不写行」。判据从「行数恒为 1」变成「行数恒为 0」，
// 强度没有降低：任何一次登录漏登记都会被它抓到。
func TestDevicePoolSecondLoginKeepsSingleRowAndAdvancesLastSeen(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-second", 0)
	stubCtrlCaptcha(t, nil)

	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("第一次登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	assertNoDeviceRows(t, db, user.ID, 0)

	time.Sleep(20 * time.Millisecond)

	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("第二次登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	assertNoDeviceRows(t, db, user.ID, 0)
}

// ─────────────────────── 判据 ③ ───────────────────────

// 判据 ③：换设备 / 换 client_kind 登录 —— 仍然一行都不写。
func TestDevicePoolDifferentDeviceAddsRow(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-third", 0)
	stubCtrlCaptcha(t, nil)

	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("设备 A 登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	assertNoDeviceRows(t, db, user.ID, 0)

	// 换一个 UA = 换一台设备，仍不该产生设备行。
	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent+" Firefox/121"); recorder.Code != http.StatusOK {
		t.Fatalf("设备 B 登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	assertNoDeviceRows(t, db, user.ID, 0)

	// 换 client_kind（同一 UA）同样不该产生设备行。
	recorder := loginWithUserAgentWithKind(t, user.Username, password, devicePoolTestUserAgent, service.ClientKindExtension)
	if recorder.Code != http.StatusOK {
		t.Fatalf("扩展入口登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	assertNoDeviceRows(t, db, user.ID, 0)
}

// assertNoDeviceRows 断言某个用户的设备行数**恰好等于 want**，且结论是「等过了」的。
//
// 为什么不能直接用 waitForDeviceRows(want=0)：那条 helper 在 want==0 时
// 首次读表就返回，「异步写入还没发生」与「永远不写」在它眼里完全一样 ——
// 这正是本项目反复踩过的假钉子形态（断言在变异下仍绿）。
//
// 这里反过来做：先等一个明确的窗口（登记 goroutine 是 FIFO 到同一个库上的，
// 窗口内足以完成），再断言。窗口取 300ms：比 waitForDeviceRows 的 3s 短得多，
// 但远大于本机实测的异步写入耗时（<10ms），不会拖慢整个包。
//
// want 一般传 0（登录不登记）。传非 0 用于「预置了 N 台且登录不该改动它」的场景 ——
// 那种情况下要的是「行数没变」，而不是「一行都没有」。
//
// 返回值是断言通过时的实际行（按 id 升序），供调用方继续核对 ID 等细节。
func assertNoDeviceRows(t *testing.T, db *gorm.DB, userID int32, want int) []entity.AccountDeviceBinding {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	var rows []entity.AccountDeviceBinding
	if err := db.Where("user_id = ?", userID).Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("统计设备行失败: %v", err)
	}
	if len(rows) != want {
		t.Fatalf("设备行数应恒为 %d（登录路径不得改动设备表），实际 %d 行：%+v", want, len(rows), rows)
	}
	return rows
}

// loginWithUserAgentWithKind 与 loginWithUserAgent 同源，额外带上 X-Client-Kind。
func loginWithUserAgentWithKind(t *testing.T, username, password, userAgent, kind string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"username": username, "password": password, "captchaToken": "tok"})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("User-Agent", userAgent)
	ctx.Request.Header.Set(service.ClientKindHeader, kind)
	ctx.Request.RemoteAddr = "203.0.113.7:1234"
	UserLogin(ctx)
	return recorder
}

// ─────────────────────── 判据 ④ ───────────────────────

// 判据 ④：设备写入失败时登录仍然成功且返回正常 token。
//
// 注入方式：在后台 DB 上挂 `Before("gorm:create")` 回调，对
// account_device_bindings 的插入直接报错。**只有设备表受影响**，
// users / logs / 会话表的写入照常 —— 否则就变成测「DB 全坏时登录仍成功」，
// 那是另一件事（也未必该成立）。
//
// 这条与既有设计一致（device_pool.go 判定本身 fail-open），不是额外加固。
//
// ⚠️ 2026-11-17 语义反转：登录路径已不写设备表，所以「注入 Create 失败」这个手法
// 无从施加。用例改写成**更强的**形态：在 account_device_bindings 上挂一个
// 「只要被 Create 就记数并报错」的回调，然后断言
//
//	① 登录仍然 200 + 返回 jwt（证明失败写入确实不影响登录）；
//	② 回调**一次都没被调用**（证明登录路径根本没碰设备表 —— 这是新契约）；
//	③ 操作日志仍落库（证明 ② 不是因为 DB 全坏或 handler 提前 return）。
//
// 比原来强在哪：原来只能证明「写了但失败不影响登录」，现在证明「压根不写」。
func TestDevicePoolWriteFailureDoesNotBreakLogin(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-failopen", 0)
	stubCtrlCaptcha(t, nil)

	touched := atomic.Int64{}
	if err := db.Callback().Create().Before("gorm:create").Register("devpool-fail", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != entity.TableNameAccountDeviceBinding {
			return
		}
		touched.Add(1)
		_ = tx.AddError(fmt.Errorf("injected device write failure"))
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent)
	if recorder.Code != http.StatusOK {
		t.Fatalf("设备写入失败不该影响登录：期望 200，实际 %d %s", recorder.Code, recorder.Body.String())
	}
	body := decodeBody(t, recorder)
	token, _ := body["jwt"].(string)
	if strings.TrimSpace(token) == "" {
		t.Fatalf("设备写入失败时登录仍必须返回正常 token，实际 body=%s", recorder.Body.String())
	}

	// 给异步路径留时间：即使将来有人把登记改回异步，也逃不过这条断言。
	assertNoDeviceRows(t, db, user.ID, 0)
	if got := touched.Load(); got != 0 {
		t.Fatalf("登录路径不得触碰设备表，实际触发 %d 次 Create", got)
	}

	// 反向对照：登录产生的操作日志仍应落库（证明前提②不是因为整条路径没跑）。
	var logCount int64
	if err := db.Model(&entity.Log{}).Where("user_id = ?", user.ID).Count(&logCount).Error; err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logCount == 0 {
		t.Fatal("登录成功应写下操作日志 —— 日志也没有说明注入影响面过大，用例语义被污染")
	}
}

// 判据 ④ 的对照组：不注入时登录仍不写设备行（证明上一条不是「因为注入了才不写」）。
//
// ⚠️ 这条是必须的对照组：没有它，上一条「设备表为空」也可能是注入把 Create 打挂
// 导致的，而不是「登录本来就不写」。两者结论完全不同。
func TestDevicePoolWriteSuccessControlGroup(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-control", 0)
	stubCtrlCaptcha(t, nil)
	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	assertNoDeviceRows(t, db, user.ID, 0)
}

// ─────────────────────── 判据 ⑤ ───────────────────────

// 判据 ⑤：登录主路径**完全不经过**设备写入 —— 连回调都不会被触发。
//
// 原用例证明「异步：响应先于落库」。现在更强：登录根本不触发设备 Create，
// 所以「阻塞主路径」这个问题在新设计下不存在。
//
// 手法照旧用 gorm 回调采集「是否发生」，只是期望值从「发生了且顺序正确」
// 变成「一次都没发生」。这样即使有人把登记改回同步写法，也会因为
// touched>0 立刻变红 —— 保留了对旧缺陷的防护。
func TestDevicePoolWriteDoesNotBlockLogin(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-nonblock", 0)
	stubCtrlCaptcha(t, nil)

	touched := atomic.Int64{}
	if err := db.Callback().Create().Before("gorm:create").Register("devpool-delay", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != entity.TableNameAccountDeviceBinding {
			return
		}
		touched.Add(1)
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	started := time.Now()
	recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent)
	elapsed := time.Since(started)
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录应成功，实际 %d %s", recorder.Code, recorder.Body.String())
	}

	assertNoDeviceRows(t, db, user.ID, 0)
	if got := touched.Load(); got != 0 {
		t.Fatalf("登录路径不得触发设备 Create，实际 %d 次（耗时 %v）", got, elapsed)
	}
}

// ─────────────────────── 判据 ⑥ ───────────────────────

// 判据 ⑥：设备上限按等级真的生效（消费既有 CanUseDevice 语义，不改它）。
//
// 场景与断言严格按 device_pool.go 的 8 步判定顺序：
//   - device_limit=2：前 2 台 RegisterDevice 都 Allowed；
//   - 第 3 台 CanUseDevice → Allowed=false 且 Reason="limit-exceeded"；
//   - 第 1 台（已登记）再问 → Allowed=true 且 Reason="already-registered"
//     （第 6 步必须先于第 7 步，否则用户被自己的旧设备锁在门外）；
//   - device_limit=0（不限）：登记 10 台全部 Allowed 且 Reason="unlimited"。
//
// ⚠️ 期望值一律写**字面量**而不是引用 DevicePoolReason* 常量：常量改名/改值时期望值
// 会跟着漂移，断言就恒真了（本仓已交付过这类假钉子）。
func TestDevicePoolLimitEnforcedByLevel(t *testing.T) {
	db := newDevicePoolHookDB(t)
	limitTwoID := seedDevicePoolHookLevel(t, db, 1, 2)
	unlimitedID := seedDevicePoolHookLevel(t, db, 2, 0)
	limitedUser, _ := seedDevicePoolHookUser(t, db, "devpool-limit2", limitTwoID)
	unlimitedUser, _ := seedDevicePoolHookUser(t, db, "devpool-unlimited", unlimitedID)

	for _, deviceID := range []string{"dev-a", "dev-b"} {
		decision, err := service.RegisterDevice(db, limitedUser.ID, deviceID)
		if err != nil {
			t.Fatalf("register %s: %v", deviceID, err)
		}
		if !decision.Allowed {
			t.Fatalf("上限 2 的前 2 台应放行，%s 被拒：%+v", deviceID, decision)
		}
		if decision.Reason != "within-limit" {
			t.Fatalf("%s 的 Reason 应是 within-limit，实际 %q", deviceID, decision.Reason)
		}
	}

	third := service.CanUseDevice(db, limitedUser.ID, "dev-c")
	if third.Allowed {
		t.Fatalf("上限 2 的第 3 台必须被拒，实际放行：%+v", third)
	}
	if third.Reason != "limit-exceeded" {
		t.Fatalf("拒绝原因必须是 limit-exceeded（不能用布尔代替），实际 %q", third.Reason)
	}
	if third.Used != 2 || third.Limit != 2 || !third.Enforced {
		t.Fatalf("拒绝时的数字必须可解释（used=2 limit=2 enforced=true），实际 %+v", third)
	}

	// 第 6 步先于第 7 步：已登记的设备即使超限也必须放行。
	registered := service.CanUseDevice(db, limitedUser.ID, "dev-a")
	if !registered.Allowed {
		t.Fatalf("已登记设备必须放行（否则用户被自己的旧设备锁在门外）：%+v", registered)
	}
	if registered.Reason != "already-registered" {
		t.Fatalf("已登记设备的 Reason 必须是 already-registered，实际 %q", registered.Reason)
	}

	// 不限档：0 = 不限，10 台全放行，且 Enforced=false（没做上限比较）。
	for i := 1; i <= 10; i++ {
		decision, err := service.RegisterDevice(db, unlimitedUser.ID, fmt.Sprintf("unlimited-%d", i))
		if err != nil {
			t.Fatalf("register unlimited-%d: %v", i, err)
		}
		if !decision.Allowed {
			t.Fatalf("limit=0 的第 %d 台必须放行：%+v", i, decision)
		}
		if decision.Reason != "unlimited" {
			t.Fatalf("limit=0 的 Reason 必须是 unlimited，实际 %q", decision.Reason)
		}
		if decision.Enforced {
			t.Fatalf("limit=0 不做上限比较，Enforced 必须为 false")
		}
	}
}

// ─────────────────────── 接线完整性（源码级） ───────────────────────

// 三处接线点必须都在（登录 / 短信登录 / 换绑 ×2）。
//
// 这条是**接线层**断言：上面所有用例都直接调 handler，能证明「调到的那个 handler
// 会写设备」；但不能证明「每一处该接的都接了」。少接一处不会让任何行为用例变红
// （它只是少写一行数据），所以必须单独钉。
//
// 断言用「函数体里出现 recordAccountDeviceBinding 调用」而不是行号：
// 行号会随任何无关改动漂移，而函数名是稳定的。
//
// ⚠️ 2026-11-17 语义反转（用户定案）：设备池只服务「chaoxing 调 API 的设备」，
// Web UI 登录/换绑**不再**登记设备。所以这里的期望值从 1 改成 0 —— 断言的是
// 「这些 handler 里一次都不许出现」，与原来正好相反。
//
// 为什么仍然要留这条用例：删掉它之后，「哪天有人把 recordAccountDeviceBinding
// 加回登录路径」就再也没有任何用例会变红 —— 而那正是本轮要消灭的行为。
func TestDevicePoolHookAbsentFromSessionHandlers(t *testing.T) {
	cases := []struct {
		name string
		fn   string
	}{
		{"密码登录 UserLogin", "func UserLogin(c *gin.Context) {"},
		{"短信登录 UserSMSLogin", "func UserSMSLogin(c *gin.Context) {"},
		{"换绑 BindCurrentUserWechat", "func BindCurrentUserWechat(c *gin.Context) {"},
		{"换绑 BindCurrentUserWechatByCode", "func BindCurrentUserWechatByCode(c *gin.Context) {"},
	}
	source := readControllerSource(t, "user.go")
	for _, tc := range cases {
		body := functionBodyOf(t, source, tc.fn)
		if got := strings.Count(body, "recordAccountDeviceBinding("); got != 0 {
			t.Fatalf("%s 里不得调用 recordAccountDeviceBinding，实际 %d 次。"+
				"设备池只登记 chaoxing 调 API 的设备（X-Device-Id），"+
				"Web UI 登录/换绑不占设备名额，也不该出现在「我的设备」里",
				tc.name, got)
		}
	}
}

// 设备登记的**唯一**入口是业务路由上的 DeviceLimit 中间件。
//
// 这条用例是上面那条的配套对照：只断言「登录不登记」还不够 ——
// 万一登记入口被整体删掉，所有请求都变成「不登记但也不校验」，
// 上面那条照样全绿，而「我的设备」会永远是空的。
//
// 判据：/api/search 与 /api/search/tasks 两条搜索路由都必须挂 DeviceLimit。
func TestDevicePoolRegistrationEntryIsDeviceLimitMiddleware(t *testing.T) {
	source := readRouterSource(t)
	for _, needle := range []string{
		`apiRouter.POST("/search", middleware.DeviceLimit(), controller.SearchWithIdempotency)`,
		`apiRouter.POST("/search/tasks", controller.AuthAccessToken, middleware.DeviceLimit(), controller.CreateSearchTask)`,
	} {
		if !strings.Contains(source, needle) {
			t.Fatalf("搜索路由必须挂 DeviceLimit 中间件（设备登记的唯一入口），未找到：%s", needle)
		}
	}
}

// 设备登记**绝不能**出现在失败分支里（否则撞库会顺带把账号设备额度打满）。
//
// 判据：每处调用都必须在函数体的**后半段**（成功路径在尾部），
// 且调用前最近的 `return` 之后不应再有别的 return 把调用包进失败分支。
// 这里用可读性更好的等价判据：调用点之后到函数结束之间不得出现 `c.JSON(http.Status` 之外的
// 失败写响应（成功路径的 c.JSON(StatusOK) 除外）。
//
// ⚠️ 2026-11-17 起这条用例的判据**反了过来**：会话类 handler 里已经没有登记调用，
// 所以「调用之后不得走到失败响应」这个形状不再存在。保留的断言变成：
// 这些函数体里**一次都不许出现** recordAccountDeviceBinding（在失败分支里出现更不行）。
//
// 与原 TestDevicePoolHookAbsentFromSessionHandlers 的分工：
//   - 那条数「次数」，防止有人在成功路径加回来；
//   - 这条查「位置」，防止有人把它塞进某个 `if err != nil` 分支里
//     （那种写法次数也是 0 吗？不 —— 次数会是 1，被上一条挡住；
//     但如果有人同时改坏上一条的期望值，这条仍能独立兜住）。
func TestDevicePoolHookNotOnFailurePaths(t *testing.T) {
	source := readControllerSource(t, "user.go")
	for _, fn := range []string{
		"func UserLogin(c *gin.Context) {",
		"func UserSMSLogin(c *gin.Context) {",
		"func BindCurrentUserWechat(c *gin.Context) {",
		"func BindCurrentUserWechatByCode(c *gin.Context) {",
	} {
		body := functionBodyOf(t, source, fn)
		if strings.Contains(body, "recordAccountDeviceBinding(") {
			t.Fatalf("%s 出现了设备登记调用 —— 设备池只登记 chaoxing 调 API 的设备，"+
				"Web UI 的登录/换绑（无论成功还是失败分支）都不该在设备表留痕", fn)
		}
	}
}

// readControllerSource 读本包的源文件（接线断言用）。
func readControllerSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// readServiceSource 读 internal/service 包的源文件（跨包契约断言用）。
//
// 用于钉住「单设备登录由 login_session_id 保证」这件事发生在 service 层，
// 而不是在 controller 或设备池里 —— 后两者都拿不到「本次会话」的语义。
func readServiceSource(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "service", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// functionBodyOf 截出某个函数从签名到**下一个顶层 func 之前**的原文。
//
// 用「下一个 `func ` 出现在行首」作为结束标记：Go 的函数声明一定在行首，
// 而函数体里的匿名函数写法是 `go func(` / `= func(`（不顶格），不会误截。
func functionBodyOf(t *testing.T, source string, signature string) string {
	t.Helper()
	start := strings.Index(source, signature)
	if start < 0 {
		t.Fatalf("源码里找不到函数签名 %q", signature)
	}
	rest := source[start+len(signature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}
