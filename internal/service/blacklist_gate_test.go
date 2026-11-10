package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// wechatUpstreamStub 起一个只回答「验证码 → OpenID」的上游桩。
//
// 只实现 /api/wechat/user：本用例要证明的是「拿到 OpenID 之后判定生效」，
// 不需要真实的微信配置与 Bearer 校验。
func wechatUpstreamStub(t *testing.T, openID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/wechat/user" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": openID})
	}))
}

// bindDefaultBlacklistCacheToTestDB 把**进程级单例**默认缓存绑到本用例的库。
//
// 微信链路的判定写在 service 内部，读的是 service.DefaultBlacklistCache()，
// 所以这类用例必须驱动单例本身（用局部缓存则测不到真实路径）。
//
// ⚠️ 收尾必须把单例置回「开关关闭 + 零规则」：单例是进程级的，留一个
// 「已启用、规则指向已关闭内存库」的快照给同包其它用例，会让它们莫名其妙地
// 被黑名单拦住（这类跨用例污染极难排查）。实现上 reload 一个**只建了
// system_settings 表**的空库 —— 开关读不到即 false，规则表不存在即空快照。
func bindDefaultBlacklistCacheToTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	cache := DefaultBlacklistCache()
	t.Cleanup(func() { resetDefaultBlacklistCacheForTest(t, cache) })
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload default blacklist cache: %v", err)
	}
}

// resetDefaultBlacklistCacheForTest 把给定缓存恢复成「默认关闭、零规则」。
func resetDefaultBlacklistCacheForTest(t *testing.T, cache *BlacklistCache) {
	t.Helper()
	if cache == nil {
		return
	}
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	tmp, err := gorm.Open(sqlite.Open("file:blacklist-gate-reset-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open reset db: %v", err)
	}
	if err := tmp.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate reset db: %v", err)
	}
	if err := cache.Reload(tmp); err != nil {
		t.Fatalf("reset blacklist cache: %v", err)
	}
}

// 手机号 / 微信黑名单判定入口（BL-3）的行为级测试。
//
// 全部用真实 sqlite 内存库 + 真实 BlacklistCache（Reload 真加载规则），
// 不手搓 struct 自比：判定链路的价值在「开关 → 快照 → 命中 → 留痕」四个环节串起来，
// 只断言结构体等于什么测不出任何一个环节坏掉。
//
// ⚠️ 这些用例必须「能变红」：把 blacklistValueGate 短路成 `return BlacklistMatch{}, false`
// 后，命中类用例全部失败；只有 fail-open 与开关关闭类仍绿 —— 那正是它们要证明的
// 「放行」行为。变异验证记录见 PR 正文。

func newBlacklistGateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:blacklist-gate-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.BlacklistEntry{}, &entity.BlacklistHit{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// blacklistGateHits 读回命中日志（按 id 升序，便于断言写入顺序）。
func blacklistGateHits(t *testing.T, db *gorm.DB) []entity.BlacklistHit {
	t.Helper()
	var rows []entity.BlacklistHit
	if err := db.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("load blacklist hits: %v", err)
	}
	return rows
}

// ---------------------------------------------------------------------------
// 开关关闭：零查询、零写日志、直接放行（46-*.md 红线）
// ---------------------------------------------------------------------------

func TestBlacklistPhoneGatePassesWithoutWritingWhenSwitchOff(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	// 规则确实存在，只是总开关是关的 —— 这样才能证明「拦不拦看开关」，
	// 而不是「恰好没有规则所以放行」。
	seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindPhone, Pattern: "13800001111", Enabled: true,
	})
	setBlacklistEnabled(t, db, "false")
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cache.Enabled() {
		t.Fatal("precondition: 开关应为关闭")
	}

	recorder := NewBlacklistHitRecorder()
	match, hit := BlacklistPhoneGateWith(cache, recorder, db, "13800001111", BlacklistGateMeta{
		ClientIP: "203.0.113.7", Path: "/adapter-service/user/register",
	})
	if hit {
		t.Fatalf("开关关闭时必须放行，却命中 %+v", match)
	}
	if rows := blacklistGateHits(t, db); len(rows) != 0 {
		t.Fatalf("开关关闭时不得写命中日志，实际 %d 行：%+v", len(rows), rows)
	}
	if slots := recorder.SlotCount(); slots != 0 {
		t.Fatalf("开关关闭时不得占用聚合槽位，实际 %d", slots)
	}
}

// ---------------------------------------------------------------------------
// 开关打开 + 命中：返回命中 + 写 blocked 留痕
// ---------------------------------------------------------------------------

func TestBlacklistPhoneGateBlocksAndRecordsBlockedHit(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	entry := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindPhone, Pattern: "13800001111", Enabled: true,
		Reason: "注册行为异常",
	})
	setBlacklistEnabled(t, db, "true")
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// 输入故意用 +86 与空格：判定必须走 NormalizeMainlandPhoneInput，
	// 否则「换个写法就绕过」。
	match, hit := BlacklistPhoneGateWith(cache, NewBlacklistHitRecorder(), db, " +86 13800001111 ", BlacklistGateMeta{
		ClientIP: "203.0.113.7", Path: "/adapter-service/user/register",
	})
	if !hit {
		t.Fatal("黑名单内的手机号必须命中")
	}
	if match.EntryID != entry.ID || match.Kind != BlacklistKindPhone {
		t.Fatalf("match = %+v, want entryID=%d kind=phone", match, entry.ID)
	}
	if strings.TrimSpace(match.Reason) != "注册行为异常" {
		t.Fatalf("命中必须带回 reason（给用户看的文案），got %q", match.Reason)
	}
	// 脱敏：显示 138****1111，绝不能是明文（46-*.md §2.1）。
	if match.ValueMasked != "138****1111" {
		t.Fatalf("valueMasked = %q, want 138****1111", match.ValueMasked)
	}

	rows := blacklistGateHits(t, db)
	if len(rows) != 1 {
		t.Fatalf("命中应写 1 行留痕，实际 %d 行：%+v", len(rows), rows)
	}
	row := rows[0]
	if row.Action != BlacklistActionBlocked || row.EntryID != entry.ID || row.Kind != BlacklistKindPhone {
		t.Fatalf("留痕行不符合预期：%+v", row)
	}
	if row.ClientIP != "203.0.113.7" || row.Path != "/adapter-service/user/register" {
		t.Fatalf("留痕行缺请求上下文：ip=%q path=%q", row.ClientIP, row.Path)
	}
	if row.ValueMasked != "138****1111" {
		t.Fatalf("留痕必须是脱敏值，got %q", row.ValueMasked)
	}
}

// 开关打开但未命中：放行且**不写任何行**（否则日志表会被正常流量刷满）。
func TestBlacklistPhoneGatePassesUnlistedValueWithoutWriting(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindPhone, Pattern: "13800001111", Enabled: true,
	})
	setBlacklistEnabled(t, db, "true")
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if _, hit := BlacklistPhoneGateWith(cache, NewBlacklistHitRecorder(), db, "13900002222", BlacklistGateMeta{}); hit {
		t.Fatal("未在黑名单内的手机号必须放行")
	}
	if rows := blacklistGateHits(t, db); len(rows) != 0 {
		t.Fatalf("未命中不得写日志，实际 %d 行", len(rows))
	}
}

// ---------------------------------------------------------------------------
// 开关打开 + 快照不可用：fail-open 放行 + logged_only 留痕（46-*.md §3.4）
// ---------------------------------------------------------------------------

func TestBlacklistPhoneGateFailsOpenAndLogsOnlyWhenSnapshotMissing(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	// 「开关为真但没有快照」= NewBlacklistCacheWithSwitch(true) 表达的状态：
	// DB 抖动 / 迁移未完成 / 快照尚未加载。生产路径不该出现，但必须是放行+留痕。
	cache := NewBlacklistCacheWithSwitch(true)
	if !cache.Enabled() {
		t.Fatal("precondition: 这个缓存应报告开关为开")
	}
	if cache.Snapshot() != nil {
		t.Fatal("precondition: 这个缓存不该有快照")
	}

	match, hit := BlacklistPhoneGateWith(cache, NewBlacklistHitRecorder(), db, "13800001111", BlacklistGateMeta{
		ClientIP: "203.0.113.9", Path: "/adapter-service/user/login/sms",
	})
	if hit {
		t.Fatalf("快照不可用时必须 fail-open 放行，却命中 %+v", match)
	}

	rows := blacklistGateHits(t, db)
	if len(rows) != 1 {
		t.Fatalf("fail-open 必须留痕 1 行，实际 %d 行：%+v", len(rows), rows)
	}
	if rows[0].Action != BlacklistActionLoggedOnly {
		t.Fatalf("fail-open 的动作必须是 logged_only，got %q", rows[0].Action)
	}
	if rows[0].EntryID != 0 {
		t.Fatalf("fail-open 不是真命中，entryID 应为 0，got %d", rows[0].EntryID)
	}
	if rows[0].Kind != BlacklistKindPhone || rows[0].ValueMasked != "138****1111" {
		t.Fatalf("fail-open 留痕应记录类型与脱敏值：%+v", rows[0])
	}
}

// 非法/空值不该被当成「命中」也不该留痕：那是参数问题，不是规则命中。
func TestBlacklistPhoneGateIgnoresInvalidValue(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	setBlacklistEnabled(t, db, "true")
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, raw := range []string{"", "   ", "abc", "12345"} {
		if _, hit := BlacklistPhoneGateWith(cache, NewBlacklistHitRecorder(), db, raw, BlacklistGateMeta{}); hit {
			t.Fatalf("非法手机号 %q 不该命中", raw)
		}
	}
	if rows := blacklistGateHits(t, db); len(rows) != 0 {
		t.Fatalf("非法值不得留痕，实际 %d 行", len(rows))
	}
}

// ---------------------------------------------------------------------------
// 微信：TrimSpace 但**大小写敏感**（46-*.md §1.2）
// ---------------------------------------------------------------------------

func TestBlacklistWechatGateIsCaseSensitiveAndTrimsSpace(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	entry := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindWechat, Pattern: "oABCdefGHIjkl", Enabled: true, Reason: "批量注册",
	})
	setBlacklistEnabled(t, db, "true")
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	match, hit := BlacklistWechatGateWith(cache, NewBlacklistHitRecorder(), db, "  oABCdefGHIjkl  ", BlacklistGateMeta{
		ClientIP: "203.0.113.7", Path: BlacklistGatePathWechatLogin,
	})
	if !hit || match.EntryID != entry.ID {
		t.Fatalf("大小写一致的 OpenID 必须命中，got (%+v, %v)", match, hit)
	}
	if match.ValueMasked != "o***jkl" {
		t.Fatalf("微信脱敏值 = %q, want o***jkl", match.ValueMasked)
	}

	// 小写变体是**另一个** OpenID，绝不能命中（ToLower 会把两个账号并成一条规则）。
	if _, hit := BlacklistWechatGateWith(cache, NewBlacklistHitRecorder(), db, "oabcdefghijkl", BlacklistGateMeta{}); hit {
		t.Fatal("微信 OpenID 必须大小写敏感")
	}
}

// ---------------------------------------------------------------------------
// 命中如何在 service 层传播：错误哨兵
// ---------------------------------------------------------------------------

func TestBlacklistBlockedErrorCarriesMatchAndUnwraps(t *testing.T) {
	match := BlacklistMatch{
		EntryID: 7, Kind: BlacklistKindWechat, MatchType: BlacklistMatchExact,
		ValueMasked: "o***jkl", Reason: "批量注册",
	}
	err := NewBlacklistBlockedError(match)

	if !errors.Is(err, ErrBlacklistValueBlocked) {
		t.Fatalf("命中错误必须能 errors.Is 到哨兵：%v", err)
	}
	resolved, ok := ResolveBlacklistBlocked(err)
	if !ok || resolved.EntryID != 7 {
		t.Fatalf("ResolveBlacklistBlocked = (%+v, %v)", resolved, ok)
	}
	if err.Error() != "批量注册" {
		t.Fatalf("错误文案必须只含 reason，got %q", err.Error())
	}
	// 没填 reason 时必须有兜底文案，不能是空串。
	if got := (&BlacklistBlockedError{}).Error(); got != BlacklistBlockedFallbackReason {
		t.Fatalf("空 match 的兜底文案 = %q", got)
	}
	// 与黑名单无关的错误不得被误判。
	if _, ok := ResolveBlacklistBlocked(errors.New("别的问题")); ok {
		t.Fatal("普通错误不该被识别成黑名单命中")
	}
}

// 默认聚合器必须是进程级单例（槽位表跨请求共享，两个实例会让聚合失效）。
func TestDefaultBlacklistGateHitRecorderIsSingleton(t *testing.T) {
	first := DefaultBlacklistGateHitRecorder()
	if first == nil {
		t.Fatal("默认聚合器不得为 nil")
	}
	if second := DefaultBlacklistGateHitRecorder(); second != first {
		t.Fatal("默认聚合器必须是同一个实例")
	}
}

// ---------------------------------------------------------------------------
// 微信链路：判定真的发生在 service 层（不只是控制器映射）
// ---------------------------------------------------------------------------

// 被拉黑的 OpenID 走验证码登录：必须返回命中错误，**且不得签发 JWT**。
//
// 这条用例的价值：控制器侧的「错误→403 映射」用例可以用假错误通过，
// 只有这条能证明 service 层真的调了 gate。
func TestLoginWechatByCodeRejectsBlacklistedOpenID(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	// 黑名单表之外，登录链路还要 users 表（用来找已绑定账号）。
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	openID := "oABCdefGHIjkl"
	seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindWechat, Pattern: openID, Enabled: true, Reason: "该微信已被拉黑",
	})
	setBlacklistEnabled(t, db, "true")
	// 生产上默认缓存由 EnsureDefaultBlacklistCacheStarted 绑定；这里手动绑到测试库。
	bindDefaultBlacklistCacheToTestDB(t, db)

	server := wechatUpstreamStub(t, openID)
	defer server.Close()

	_, err := LoginWechatByCodeWithDB(db, WechatCodeLoginInput{
		Client:    WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		Store:     NewWechatBindTokenStore(),
		Code:      "104014",
		RequestIP: "203.0.113.7",
	})
	match, blocked := ResolveBlacklistBlocked(err)
	if !blocked {
		t.Fatalf("被拉黑的 OpenID 必须返回黑名单命中错误，got %v", err)
	}
	if match.EntryID == 0 || match.Kind != BlacklistKindWechat {
		t.Fatalf("命中信息不完整：%+v", match)
	}
	if strings.Contains(err.Error(), openID) {
		t.Fatalf("错误文案泄漏了 OpenID：%v", err)
	}
}

// 把被拉黑的 OpenID 绑到当前账号：同样必须被拦。
func TestBindWechatToUserRejectsBlacklistedOpenID(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	openID := "oABCdefGHIjkl"
	seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindWechat, Pattern: openID, Enabled: true, Reason: "该微信已被拉黑",
	})
	setBlacklistEnabled(t, db, "true")
	bindDefaultBlacklistCacheToTestDB(t, db)

	user := entity.User{Username: "bind_target", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	store := NewWechatBindTokenStore()
	token := store.Create(openID, time.Now().Add(time.Minute))

	if _, err := BindWechatToUserWithDB(db, store, token, user.ID); err == nil {
		t.Fatal("被拉黑的微信不得绑定成功")
	} else if _, blocked := ResolveBlacklistBlocked(err); !blocked {
		t.Fatalf("必须是黑名单命中错误，got %v", err)
	}
	// 绑定失败后用户身上的 wechat_id 必须仍是空（不能留半成品）。
	var reloaded entity.User
	if err := db.Where("id = ?", user.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if reloaded.WechatID != nil && strings.TrimSpace(*reloaded.WechatID) != "" {
		t.Fatalf("命中后不得写入 wechat_id，got %q", *reloaded.WechatID)
	}
}

// 开关关闭时同一条规则必须放行（微信侧也要成对验证）。
func TestBindWechatToUserPassesWhenBlacklistSwitchOff(t *testing.T) {
	db := newBlacklistGateTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	openID := "oABCdefGHIjkl"
	seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindWechat, Pattern: openID, Enabled: true, Reason: "该微信已被拉黑",
	})
	setBlacklistEnabled(t, db, "false")
	bindDefaultBlacklistCacheToTestDB(t, db)

	user := entity.User{Username: "bind_switch_off", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	store := NewWechatBindTokenStore()
	token := store.Create(openID, time.Now().Add(time.Minute))

	updated, err := BindWechatToUserWithDB(db, store, token, user.ID)
	if err != nil {
		t.Fatalf("开关关闭时必须放行绑定，got %v", err)
	}
	if updated.WechatID == nil || *updated.WechatID != openID {
		t.Fatalf("绑定结果缺 wechat_id：%+v", updated)
	}
	if rows := blacklistGateHits(t, db); len(rows) != 0 {
		t.Fatalf("开关关闭时不得写命中日志，实际 %d 行", len(rows))
	}
}
