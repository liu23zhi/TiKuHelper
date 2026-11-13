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
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/user/login", bytes.NewReader(payload))
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
		_ = registerAccountDeviceBindingNow(userID, deviceID)
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

	rows := waitForDeviceRows(t, db, user.ID, 1)
	expected := service.NormalizeDevicePoolDeviceID(
		BuildDevicePoolDeviceID(service.ClientKindUnknown, devicePoolTestUserAgent))
	if rows[0].DeviceID != expected {
		t.Fatalf("落库的设备标识不对：期望 %q，实际 %q", expected, rows[0].DeviceID)
	}
	if !rows[0].FirstSeenAt.Equal(rows[0].LastSeenAt) {
		t.Fatalf("首次登记时 first_seen_at 必须等于 last_seen_at：first=%v last=%v",
			rows[0].FirstSeenAt, rows[0].LastSeenAt)
	}
}

// ─────────────────────── 判据 ② ───────────────────────

// 判据 ②：同设备二次登录 —— 行数不变、last_seen_at 前进、first_seen_at 不变。
//
// ⚠️ 这是最容易做成假钉子的一条：只断「行数 == 1」会让「每次登录都新建一行、
// 但被唯一索引挡掉」与「真的幂等」看起来一样。所以这里三个维度都断，
// 并且 first_seen_at / last_seen_at 都要求**逐值**比较（不是「非零」）。
func TestDevicePoolSecondLoginKeepsSingleRowAndAdvancesLastSeen(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-second", 0)
	stubCtrlCaptcha(t, nil)

	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("第一次登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	first := waitForDeviceRows(t, db, user.ID, 1)[0]

	// 等待一个可观测的时间差，确保 last_seen_at 的「前进」有可测量的余地。
	time.Sleep(20 * time.Millisecond)

	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("第二次登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	drainDeviceWrites(t, user.ID, first.DeviceID)

	rows := waitForDeviceRows(t, db, user.ID, 1)
	if len(rows) != 1 {
		t.Fatalf("同一设备二次登录必须只有 1 行，实际 %d 行", len(rows))
	}
	second := rows[0]
	if second.ID != first.ID {
		t.Fatalf("二次登录不该换行（同一 (user_id, device_id) 是同一行）：%d -> %d", first.ID, second.ID)
	}
	if !second.FirstSeenAt.Equal(first.FirstSeenAt) {
		t.Fatalf("first_seen_at 必须保持不变：%v -> %v", first.FirstSeenAt, second.FirstSeenAt)
	}
	if !second.LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("last_seen_at 必须前进：%v -> %v", first.LastSeenAt, second.LastSeenAt)
	}
}

// ─────────────────────── 判据 ③ ───────────────────────

// 判据 ③：不同设备登录 —— 行数 +1。
func TestDevicePoolDifferentDeviceAddsRow(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-third", 0)
	stubCtrlCaptcha(t, nil)

	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("设备 A 登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	rows := waitForDeviceRows(t, db, user.ID, 1)
	firstDevice := rows[0].DeviceID

	// 换一个 UA = 换一台设备。
	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent+" Firefox/121"); recorder.Code != http.StatusOK {
		t.Fatalf("设备 B 登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	rows = waitForDeviceRows(t, db, user.ID, 2)
	if len(rows) != 2 {
		t.Fatalf("两台设备必须 2 行，实际 %d 行", len(rows))
	}
	if rows[0].DeviceID == rows[1].DeviceID {
		t.Fatalf("两台设备不该合并成同一行：%q", rows[0].DeviceID)
	}
	if rows[0].DeviceID != firstDevice {
		t.Fatalf("第一行不该被改写：%q -> %q", firstDevice, rows[0].DeviceID)
	}
	// 另一维度：换 client_kind（同一 UA）也必须是新设备。
	recorder := loginWithUserAgentWithKind(t, user.Username, password, devicePoolTestUserAgent, service.ClientKindExtension)
	if recorder.Code != http.StatusOK {
		t.Fatalf("扩展入口登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	waitForDeviceRows(t, db, user.ID, 3)
}

// loginWithUserAgentWithKind 与 loginWithUserAgent 同源，额外带上 X-Client-Kind。
func loginWithUserAgentWithKind(t *testing.T, username, password, userAgent, kind string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"username": username, "password": password, "captchaToken": "tok"})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/user/login", bytes.NewReader(payload))
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
func TestDevicePoolWriteFailureDoesNotBreakLogin(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-failopen", 0)
	stubCtrlCaptcha(t, nil)

	injected := atomic.Int64{}
	if err := db.Callback().Create().Before("gorm:create").Register("devpool-fail", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != entity.TableNameAccountDeviceBinding {
			return
		}
		injected.Add(1)
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

	// 证明注入真的生效了（否则这条用例是空转：登录成功可能只是因为压根没写）。
	//
	// ⚠️ 必须**等**：设备登记是异步 goroutine（判据⑤ 要求），handler 返回时
	// 回调很可能还没被调用。直接检查会误判成「注入没生效」。
	deadline := time.Now().Add(3 * time.Second)
	for injected.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if injected.Load() == 0 {
		t.Fatal("注入的写入失败回调从未被调用 —— 说明设备登记根本没走到 Create，用例前置不成立")
	}
	deviceID := service.NormalizeDevicePoolDeviceID(
		BuildDevicePoolDeviceID(service.ClientKindUnknown, devicePoolTestUserAgent))
	drainDeviceWrites(t, user.ID, deviceID)
	// 设备表应当为空（写入被拒），但用户/日志表仍有落库。
	var count int64
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if count != 0 {
		t.Fatalf("注入失败时不该有设备行，实际 %d 行", count)
	}
	// 反向对照：登录产生的操作日志仍应落库（证明只是设备表写失败，不是 DB 全坏）。
	var logCount int64
	if err := db.Model(&entity.Log{}).Where("user_id = ?", user.ID).Count(&logCount).Error; err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logCount == 0 {
		t.Fatal("登录成功应写下操作日志 —— 日志也没有说明注入影响面过大，用例语义被污染")
	}
}

// 判据 ④ 的对照组：不注入时同一路径确实写成功（证明上一条不是「永远不写」）。
func TestDevicePoolWriteSuccessControlGroup(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-control", 0)
	stubCtrlCaptcha(t, nil)
	if recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent); recorder.Code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", recorder.Code, recorder.Body.String())
	}
	waitForDeviceRows(t, db, user.ID, 1)
}

// ─────────────────────── 判据 ⑤ ───────────────────────

// 判据 ⑤：设备写入**不阻塞**主路径 —— 断言「登录响应先于设备行落库」。
//
// 时序证据由 gorm 回调采集：
//   - respondedAt：UserLogin 返回（即响应已写出）之后立刻记的时刻；
//   - writtenAt：后台 goroutine 真正执行 Insert 的时刻（回调里记）。
//
// 异步（go func）时，回调只可能在 handler 返回之后触发 ⇒ respondedAt < writtenAt。
// 同步（变异成 await）时，handler 必须等回调跑完才能返回 ⇒ 顺序必然反转。
// 这与机器快慢无关：即使整台机器慢 10 倍，拓扑顺序不变。
func TestDevicePoolWriteDoesNotBlockLogin(t *testing.T) {
	db := newDevicePoolHookDB(t)
	user, password := seedDevicePoolHookUser(t, db, "devpool-nonblock", 0)
	stubCtrlCaptcha(t, nil)

	const injectedDelay = 200 * time.Millisecond
	var writtenAt atomic.Int64 // UnixNano；0 = 还没发生
	var delayApplied atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register("devpool-delay", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != entity.TableNameAccountDeviceBinding {
			return
		}
		delayApplied.Store(true)
		time.Sleep(injectedDelay)
		writtenAt.Store(time.Now().UnixNano())
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	recorder := loginWithUserAgent(t, user.Username, password, devicePoolTestUserAgent)
	respondedAt := time.Now().UnixNano()
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录应成功，实际 %d %s", recorder.Code, recorder.Body.String())
	}

	// 等写入落地（最多 5s；注入的延迟只有 200ms，留足余量）。
	deadline := time.Now().Add(5 * time.Second)
	for writtenAt.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if writtenAt.Load() == 0 {
		t.Fatal("设备写入从未发生（回调没被调用），判据⑤ 无法判定")
	}
	if !delayApplied.Load() {
		t.Fatal("注入的 200ms 延迟没生效，用例前置不成立")
	}
	if respondedAt >= writtenAt.Load() {
		t.Fatalf("登录响应必须先于设备行落库（写入不得阻塞主路径）："+
			"respondedAt=%d writtenAt=%d（差 %dms，同步写入才会出现这个顺序）",
			respondedAt, writtenAt.Load(), (writtenAt.Load()-respondedAt)/int64(time.Millisecond))
	}
	waitForDeviceRows(t, db, user.ID, 1)
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
func TestDevicePoolHookWiredAtAllSuccessPaths(t *testing.T) {
	cases := []struct {
		name string
		fn   string
		want int
	}{
		{"密码登录 UserLogin", "func UserLogin(c *gin.Context) {", 1},
		{"短信登录 UserSMSLogin", "func UserSMSLogin(c *gin.Context) {", 1},
		{"换绑 BindCurrentUserWechat", "func BindCurrentUserWechat(c *gin.Context) {", 1},
		{"换绑 BindCurrentUserWechatByCode", "func BindCurrentUserWechatByCode(c *gin.Context) {", 1},
	}
	source := readControllerSource(t, "user.go")
	for _, tc := range cases {
		body := functionBodyOf(t, source, tc.fn)
		if got := strings.Count(body, "recordAccountDeviceBinding("); got != tc.want {
			t.Fatalf("%s 里必须恰好调用 %d 次 recordAccountDeviceBinding，实际 %d 次",
				tc.name, tc.want, got)
		}
	}
}

// 设备登记**绝不能**出现在失败分支里（否则撞库会顺带把账号设备额度打满）。
//
// 判据：每处调用都必须在函数体的**后半段**（成功路径在尾部），
// 且调用前最近的 `return` 之后不应再有别的 return 把调用包进失败分支。
// 这里用可读性更好的等价判据：调用点之后到函数结束之间不得出现 `c.JSON(http.Status` 之外的
// 失败写响应（成功路径的 c.JSON(StatusOK) 除外）。
func TestDevicePoolHookNotOnFailurePaths(t *testing.T) {
	source := readControllerSource(t, "user.go")
	for _, fn := range []string{
		"func UserLogin(c *gin.Context) {",
		"func UserSMSLogin(c *gin.Context) {",
		"func BindCurrentUserWechat(c *gin.Context) {",
		"func BindCurrentUserWechatByCode(c *gin.Context) {",
	} {
		body := functionBodyOf(t, source, fn)
		index := strings.Index(body, "recordAccountDeviceBinding(")
		if index < 0 {
			t.Fatalf("%s 缺少设备登记调用", fn)
		}
		tail := body[index:]
		// 调用之后只允许出现成功响应，不允许再出现 4xx/5xx 写响应。
		for _, failure := range []string{
			"http.StatusBadRequest", "http.StatusUnauthorized",
			"http.StatusForbidden", "http.StatusConflict",
		} {
			if strings.Contains(tail, failure) {
				t.Fatalf("%s 的设备登记之后仍可能走到失败响应（%s）—— "+
					"登记必须只在成功路径上，失败登录不该在设备表留痕", fn, failure)
			}
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
