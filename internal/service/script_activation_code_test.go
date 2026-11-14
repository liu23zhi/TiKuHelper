package service

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newActivationCodeTestDB 建一个**文件**库。
//
// 必须是文件库，不能用 `file:x?mode=memory&cache=shared`：
// 内存共享库不支持 WAL，而 `busy_timeout` 在 cache=shared 下对写锁的等待
// 行为与文件库不同 —— 实测并发兑换时落败方直接拿到
// `database table is locked (262)`，而不是我们想验证的「已被使用」业务错误。
// 那样即使实现退化成「先查后写」，用例也会因为被锁挡住而只看到 1 个成功，
// 把真缺陷掩盖成假绿。
//
// DSN 走 util.SQLiteConcurrentDSN（与生产 sqliteProductionDSN 同源，不手写
// _txlock / busy_timeout），理由见 task_doc/47。
func newActivationCodeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(filepath.Join(t.TempDir(), "activation-code.db"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 每个模型单独一次 AutoMigrate（合并建表实测会缺列）。
	for _, model := range []any{
		&entity.AdminScriptActivationCode{},
		&entity.User{},
		&entity.UserScript{},
		&entity.ScriptActivationEntitlement{},
		&entity.ScriptRunSession{},
	} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	if err := ensureActivationCodeIndexes(db); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	// 并发用例需要真实并发连接；-race 下尤其要给它足够连接数，
	// 否则第二个 goroutine 会排队等连接池，测不出真并发。
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// ensureActivationCodeIndexes 建 code 唯一索引。
//
// 这是「一次性」的物理保障，所以测试库必须建它：没有唯一索引时，
// 并发兑换的正确性只依赖条件更新里的 WHERE，覆盖面会小一圈。
func ensureActivationCodeIndexes(db *gorm.DB) error {
	return db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_script_activation_code ON " +
		entity.TableNameAdminScriptActivationCode + " (code)").Error
}

func seedActivationCodeUser(t *testing.T, db *gorm.DB, id int32, username string) {
	t.Helper()
	user := entity.User{ID: id, Username: username, Role: "user", Status: "enabled", Password: "x"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user %d: %v", id, err)
	}
}

// ① 一次性：同一码兑换两次，第二次必须失败。
func TestConsumeAdminScriptActivationCodeIsOneTime(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 11, "user-a")

	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "工单 1001", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	code := codes[0].Code

	first, err := ConsumeAdminScriptActivationCode(db, code, 11, nil, nil)
	if err != nil {
		t.Fatalf("首次兑换应成功: %v", err)
	}
	if first.UsedAt == nil || first.UsedBy == nil || *first.UsedBy != 11 {
		t.Fatalf("首次兑换必须写入 used_at/used_by，实际 %+v", first)
	}
	if first.Status != entity.ScriptActivationCodeStatusUsed {
		t.Fatalf("状态应为 used，实际 %q", first.Status)
	}

	_, err = ConsumeAdminScriptActivationCode(db, code, 11, nil, nil)
	if err == nil {
		t.Fatal("同一码第二次兑换必须失败（一次性语义）")
	}
	if !strings.Contains(err.Error(), "已被使用") {
		t.Fatalf("失败原因应是「已被使用」，实际 %v", err)
	}
}

// ② 并发：两个 goroutine 同时兑换同一个码，只能有一个成功。
//
// 这是本任务的核心不变量。用真实文件库 + 生产 DSN 跑真并发。
//
// ⚠️ **这条用例单独存在是不够的**：`_txlock=immediate` 会让写事务在 BEGIN 时
// 就串行化，于是「先查后写」的错误实现也能侥幸只成功一次（实测：把原子更新
// 退化成先查后写，本用例仍然全绿）。所以另有一条不依赖事务模式的确定性用例
// （TestConsumeAdminScriptActivationCodeWinnerIsDecidedByConditionalUpdate）
// 专门钉「赢家只能由 WHERE used_at IS NULL 决出」。两条互补，缺一都会漏。
func TestConsumeAdminScriptActivationCodeIsAtomicUnderConcurrency(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 21, "racer-a")
	seedActivationCodeUser(t, db, 22, "racer-b")

	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "并发用例", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	code := codes[0].Code

	const racers = 2
	userIDs := []int32{21, 22}
	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, racers)
	for index := 0; index < racers; index++ {
		waitGroup.Add(1)
		go func(slot int) {
			defer waitGroup.Done()
			<-start
			_, consumeErr := ConsumeAdminScriptActivationCode(db, code, userIDs[slot], nil, nil)
			results[slot] = consumeErr
		}(index)
	}
	close(start)
	waitGroup.Wait()

	succeeded := 0
	for slot, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		// 落败方必须是「已被使用」这类业务错误，不能是锁超时 ——
		// 锁超时说明测的是 SQLite 的锁行为而不是一次性语义。
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "busy") {
			t.Fatalf("第 %d 个并发请求被锁挡住（应拿到业务错误）：%v", slot, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发兑换同一个码必须恰好 1 个成功，实际 %d 个（results=%v）", succeeded, results)
	}

	// 落库复核：used_at 只被写一次，used_by 只落一个用户。
	var stored entity.AdminScriptActivationCode
	if err := db.Where("code = ?", code).First(&stored).Error; err != nil {
		t.Fatalf("回读: %v", err)
	}
	if stored.UsedAt == nil || stored.UsedBy == nil {
		t.Fatalf("成功的那次必须留下 used_at/used_by，实际 %+v", stored)
	}
	if *stored.UsedBy != 21 && *stored.UsedBy != 22 {
		t.Fatalf("used_by 必须是两个竞争者之一，实际 %d", *stored.UsedBy)
	}
}

// ②b 确定性原子性：第二次兑换必须被**业务判据**拒绝（不是被事务模式救下）。
//
// 设计要点（踩过一次坑，写在这里防止退化）：
//
// 初版这条用例直接在测试里手写两遍 UPDATE ... WHERE used_at IS NULL，把
// RowsAffected 当判据。问题是通过的只是「我抄的那句 SQL 语义对不对」，与
// **被测函数**无关 —— 把 ConsumeAdminScriptActivationCode 的 WHERE 里的
// used_at IS NULL 删掉，用例照样绿（实测）。那是典型的「测了副本、没测实现」。
//
// 现在改为直接调被测函数两次，并断言：
//
//	a) 第二次必须返回 ErrAdminScriptActivationCodeUsed（业务判据），
//	b) 且落库的 used_by 仍是第一次那位（第二次没覆盖写）。
//
// 判据 b 是关键：光看 err 无法区分「条件更新没命中所以报错」与「先查到已用
// 所以报错」；但「第二次有没有写进去」能区分 —— 若实现退化成无条件 UPDATE，
// used_by 会被第二次覆盖。
func TestConsumeAdminScriptActivationCodeSecondCallCannotOverwrite(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 61, "first-winner")
	seedActivationCodeUser(t, db, 62, "second-loser")

	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	code := codes[0].Code

	// 第一次：61 号赢。
	if _, err := ConsumeAdminScriptActivationCode(db, code, 61, nil, nil); err != nil {
		t.Fatalf("第一次兑换应成功: %v", err)
	}
	// 第二次：62 号必须失败，且原因是一词不差的「已被使用」。
	_, err = ConsumeAdminScriptActivationCode(db, code, 62, nil, nil)
	if err == nil {
		t.Fatal("第二次兑换必须失败")
	}
	if !errors.Is(err, ErrAdminScriptActivationCodeUsed) {
		t.Fatalf("失败原因必须是 ErrAdminScriptActivationCodeUsed，实际 %v", err)
	}

	// 落库复核：used_by 必须仍是 61。若实现漏了 used_at IS NULL 守卫，
	// 第二次会把它改成 62 —— 这条断言是「一次性」的行为级判据。
	var stored entity.AdminScriptActivationCode
	if err := db.Where("code = ?", code).First(&stored).Error; err != nil {
		t.Fatalf("回读: %v", err)
	}
	if stored.UsedBy == nil || *stored.UsedBy != 61 {
		t.Fatalf("used_by 必须保持第一次的 61（第二次不得覆盖），实际 %v", stored.UsedBy)
	}
}

// ③ 过期码兑换失败。
func TestConsumeAdminScriptActivationCodeRejectsExpired(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 31, "expired-user")

	past := time.Now().UTC().Add(-time.Hour)
	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "过期用例", &past)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	_, err = ConsumeAdminScriptActivationCode(db, codes[0].Code, 31, nil, nil)
	if err == nil {
		t.Fatal("过期码兑换必须失败")
	}
	if !strings.Contains(err.Error(), "过期") {
		t.Fatalf("失败原因应是「已过期」，实际 %v", err)
	}
	// 过期码不能被「用掉」：兑换失败不该写 used_at。
	var stored entity.AdminScriptActivationCode
	if err := db.Where("code = ?", codes[0].Code).First(&stored).Error; err != nil {
		t.Fatalf("回读: %v", err)
	}
	if stored.UsedAt != nil {
		t.Fatal("兑换失败的码不能留下 used_at")
	}
}

// ④ 禁用的码兑换失败。
func TestConsumeAdminScriptActivationCodeRejectsDisabled(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 41, "disabled-user")

	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "禁用用例", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	disabled := entity.ScriptActivationCodeStatusDisabled
	if _, err := UpdateAdminScriptActivationCode(db, codes[0].ID, AdminScriptActivationCodeUpdate{Status: &disabled}); err != nil {
		t.Fatalf("禁用: %v", err)
	}

	_, err = ConsumeAdminScriptActivationCode(db, codes[0].Code, 41, nil, nil)
	if err == nil {
		t.Fatal("禁用码兑换必须失败")
	}
	if !strings.Contains(err.Error(), "禁用") {
		t.Fatalf("失败原因应是「已被禁用」，实际 %v", err)
	}
}

// ⑤ 批量生成上限生效。
func TestGenerateAdminScriptActivationCodesEnforcesBatchLimit(t *testing.T) {
	db := newActivationCodeTestDB(t)

	if _, err := GenerateAdminScriptActivationCodes(db, 1, MaxAdminScriptActivationCodeBatch+1, "", nil); err == nil {
		t.Fatalf("超过上限 %d 必须被拒绝", MaxAdminScriptActivationCodeBatch)
	}
	if _, err := GenerateAdminScriptActivationCodes(db, 1, 0, "", nil); err == nil {
		t.Fatal("生成 0 个必须被拒绝")
	}
	// 边界值本身要允许。
	codes, err := GenerateAdminScriptActivationCodes(db, 1, MaxAdminScriptActivationCodeBatch, "", nil)
	if err != nil {
		t.Fatalf("上限值本身应允许: %v", err)
	}
	if len(codes) != MaxAdminScriptActivationCodeBatch {
		t.Fatalf("应生成 %d 个，实际 %d", MaxAdminScriptActivationCodeBatch, len(codes))
	}
	// 码必须唯一且带前缀。
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if !strings.HasPrefix(code.Code, AdminScriptActivationCodePrefix) {
			t.Fatalf("码应带前缀 %q，实际 %q", AdminScriptActivationCodePrefix, code.Code)
		}
		if _, duplicate := seen[code.Code]; duplicate {
			t.Fatalf("生成了重复的码 %q", code.Code)
		}
		seen[code.Code] = struct{}{}
	}
}

// ⑥ 详情接口必须返回真实使用记录（兑换 + 运行/下载各一条）。
//
// 这就是用户说的「方便客服处理工单」：客服要能从一个码看到它被谁用、
// 换了哪个脚本、什么时候跑的。
func TestListAdminScriptActivationCodeUsagesReturnsRealRecords(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 51, "ticket-user")

	script := entity.UserScript{ID: 7, UserID: 51, Name: "题库脚本", ShortCode: "abc123"}
	if err := db.Create(&script).Error; err != nil {
		t.Fatalf("seed script: %v", err)
	}

	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "工单 2024", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	scriptID := int32(7)

	// 先造一条已绑定脚本的 entitlement + run session，模拟真实兑换链路。
	boundAt := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	activatedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	entitlement := entity.ScriptActivationEntitlement{
		UserID:          51,
		PurchaseKeyHash: strings.Repeat("a", 64),
		PurchaseNonce:   "nonce-1",
		CodeHash:        strings.Repeat("b", 64),
		PurchasePrice:   1.68,
		ScriptID:        &scriptID,
		ScriptBoundAt:   &boundAt,
	}
	if err := db.Create(&entitlement).Error; err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	session := entity.ScriptRunSession{
		ScriptID: 7, UserID: 51, AccessTokenID: 1,
		TokenHash: strings.Repeat("c", 64), IdentityHash: strings.Repeat("d", 64),
		ActivatedAt: activatedAt, ExpiresAt: activatedAt.Add(time.Hour),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// 把 entitlement 指向 session（模拟 ConsumeScriptActivationEntitlementRunInTx 的结果）。
	if err := db.Model(&entity.ScriptActivationEntitlement{}).
		Where("id = ?", entitlement.ID).
		Updates(map[string]any{"run_session_id": session.ID}).Error; err != nil {
		t.Fatalf("link session: %v", err)
	}
	entitlementID := entitlement.ID

	consumed, err := ConsumeAdminScriptActivationCode(db, codes[0].Code, 51, &scriptID, &entitlementID)
	if err != nil {
		t.Fatalf("兑换: %v", err)
	}

	usages, err := ListAdminScriptActivationCodeUsages(db, consumed)
	if err != nil {
		t.Fatalf("读使用记录: %v", err)
	}
	kinds := make(map[string]AdminScriptActivationCodeUsageRecord, len(usages))
	for _, usage := range usages {
		kinds[usage.Kind] = usage
	}
	for _, required := range []string{"redeem", "download", "run"} {
		if _, ok := kinds[required]; !ok {
			t.Fatalf("使用记录缺少 %q，实际 kinds=%v", required, keysOfActivationUsage(kinds))
		}
	}
	if kinds["redeem"].Username != "ticket-user" {
		t.Fatalf("兑换记录必须带用户名，实际 %q", kinds["redeem"].Username)
	}
	if kinds["run"].ScriptName != "题库脚本" {
		t.Fatalf("运行记录必须带脚本名，实际 %q", kinds["run"].ScriptName)
	}
	if !kinds["download"].At.Equal(boundAt) {
		t.Fatalf("下载记录时间应等于 ScriptBoundAt，实际 %v want %v", kinds["download"].At, boundAt)
	}
}

func keysOfActivationUsage(m map[string]AdminScriptActivationCodeUsageRecord) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

// ⑦ 未使用的码没有任何使用记录（防止详情恒返回假记录）。
func TestListAdminScriptActivationCodeUsagesEmptyForUnused(t *testing.T) {
	db := newActivationCodeTestDB(t)
	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "未使用", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	usages, err := ListAdminScriptActivationCodeUsages(db, codes[0])
	if err != nil {
		t.Fatalf("读使用记录: %v", err)
	}
	if len(usages) != 0 {
		t.Fatalf("未使用的码不应有使用记录，实际 %+v", usages)
	}
}

// ⑧ 列表的 expired 筛选必须翻译成谓词（库里不存 expired 这个值）。
func TestListAdminScriptActivationCodesFiltersExpired(t *testing.T) {
	db := newActivationCodeTestDB(t)

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	if _, err := GenerateAdminScriptActivationCodes(db, 1, 1, "过期", &past); err != nil {
		t.Fatalf("generate expired: %v", err)
	}
	if _, err := GenerateAdminScriptActivationCodes(db, 1, 1, "未过期", &future); err != nil {
		t.Fatalf("generate valid: %v", err)
	}

	result, err := ListAdminScriptActivationCodes(db, AdminScriptActivationCodeListQuery{
		Status: entity.ScriptActivationCodeStatusExpired, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("按 expired 筛选应命中 1 条，实际 %d", result.Total)
	}
	if result.Items[0].Remark != "过期" {
		t.Fatalf("命中的应是过期那条，实际 remark=%q", result.Items[0].Remark)
	}
}

// ⑨ 已使用的码不能再改状态（否则可重复兑换）。
func TestUpdateAdminScriptActivationCodeRejectsStatusChangeAfterUse(t *testing.T) {
	db := newActivationCodeTestDB(t)
	seedActivationCodeUser(t, db, 91, "used-user")

	codes, err := GenerateAdminScriptActivationCodes(db, 1, 1, "", nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := ConsumeAdminScriptActivationCode(db, codes[0].Code, 91, nil, nil); err != nil {
		t.Fatalf("兑换: %v", err)
	}
	unused := entity.ScriptActivationCodeStatusUnused
	if _, err := UpdateAdminScriptActivationCode(db, codes[0].ID, AdminScriptActivationCodeUpdate{Status: &unused}); err == nil {
		t.Fatal("已使用的码不允许改回 unused")
	}
	// 但改备注应该被允许。
	remark := "补充说明"
	if _, err := UpdateAdminScriptActivationCode(db, codes[0].ID, AdminScriptActivationCodeUpdate{Remark: &remark}); err != nil {
		t.Fatalf("已使用的码应允许改备注: %v", err)
	}
}

// ⑩ 掩码只露后 4 位。
func TestAdminScriptActivationCodeMask(t *testing.T) {
	code := entity.AdminScriptActivationCode{Code: "TKA-0123456789abcdef"}
	masked := code.Mask()
	if masked != "****cdef" {
		t.Fatalf("掩码应为 ****cdef，实际 %q", masked)
	}
	if strings.Contains(masked, "0123456789") {
		t.Fatalf("掩码不能泄漏中段，实际 %q", masked)
	}
}
