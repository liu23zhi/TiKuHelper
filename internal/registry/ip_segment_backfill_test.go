package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ip_segments 启动回填（task_doc/71 补口）的钉子。
//
// 覆盖三条判据：
//
//	② 连跑两次，第二次「新增 0」（幂等）
//	④ 每批不超 500 行（不能用一个大事务包全表）
//	⑤ raw_json 为空或不可解析的行跳过并计数，不能让整批失败
//
// 判据③（缓存命中路径也沉淀）在 internal/service/ip_segment_auto_test.go；
// 判据①（不新增手动导入端点）的反向断言在 internal/api/ip_segment_router_test.go。

// newIPSegmentBackfillTestDB 建立回填测试库。
//
// 建 system_settings（标记）、location_cache_entries（数据源）与 ip_segments（目标）
// 三张表。目标表用 AutoMigrate 建：本回填不改结构，只写数据，
// 让迁移自己去建表会混淆「结构」与「数据」两层判据。
func newIPSegmentBackfillTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&entity.SystemSetting{},
		&entity.LocationCache{},
		&entity.IPSegment{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// ipSegmentRawJSON 拼一条可解析出段的上游响应。
//
// beginThird 允许同一段被一批行共享（这是真实情况：一个段覆盖几百个 IP，
// 上游对段内每个 IP 都返回同一对 beginip/endip）。判据⑥的「一个 IP 一个段」
// 与这里的「多 IP 一个段」是同一件事的两面。
func ipSegmentRawJSON(beginThird int) string {
	return `{"region":"中国 广东 中山","isp":"China Mobile Communications Group Co., Ltd.",` +
		`"llc":"移动","asn":"AS9808",` +
		`"beginip":"120.239.` + itoa(beginThird) + `.0","endip":"120.239.` + itoa(beginThird) + `.255"}`
}

// itoa 是测试里避免引入 strconv 的小工具（只处理 0-255）。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// seedIPCacheRow 插入一行归属地缓存。
func seedIPCacheRow(t *testing.T, db *gorm.DB, kind, lookupKey, source, rawJSON string) {
	t.Helper()
	if err := db.Create(&entity.LocationCache{
		Kind: kind, LookupKey: lookupKey, Source: source,
		Address: "中国 广东 中山 移动", RawJSON: rawJSON,
	}).Error; err != nil {
		t.Fatalf("seed cache row %s: %v", lookupKey, err)
	}
}

// 判据②：启动回填必须幂等 —— 连跑两次，第二次「新增 0」。
func TestIPSegmentBackfillIsIdempotent(t *testing.T) {
	db := newIPSegmentBackfillTestDB(t)
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.1.10", "", ipSegmentRawJSON(1))
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.2.10", "", ipSegmentRawJSON(2))
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.3.10", "", ipSegmentRawJSON(3))

	first, err := BackfillIPSegmentsWithStats(db, nil)
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if first.Scanned != 3 {
		t.Fatalf("first backfill must scan 3 ip rows, got %d", first.Scanned)
	}
	if first.NewlyCreated != 3 {
		t.Fatalf("first backfill must create 3 segments, got %d (stats=%+v)", first.NewlyCreated, first)
	}

	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("segment table must hold 3 rows after first backfill, got %d", count)
	}

	// 第二次：全部已存在 → 新增必须为 0，且总行数不变。
	second, err := BackfillIPSegmentsWithStats(db, nil)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.Scanned != 3 {
		t.Fatalf("second backfill must still scan 3 rows, got %d", second.Scanned)
	}
	if second.NewlyCreated != 0 {
		t.Fatalf("判据②：第二次回填「新增」必须为 0，got %d (stats=%+v)", second.NewlyCreated, second)
	}
	if second.Existing != 3 {
		t.Fatalf("second backfill must report 3 existing segments, got %d", second.Existing)
	}
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("recount: %v", err)
	}
	if count != 3 {
		t.Fatalf("idempotent backfill must keep 3 rows, got %d", count)
	}
}

// EnsureIPSegmentBackfill 的标记键语义：第一轮写标记，第二轮因标记而直接返回。
//
// 这是「启动入口」这一层的幂等（判据②的另一半）—— BackfillIPSegmentsWithStats
// 保证数据幂等，标记保证**不再扫表**。
func TestEnsureIPSegmentBackfillWritesFlagAndShortCircuits(t *testing.T) {
	db := newIPSegmentBackfillTestDB(t)
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.9.10", "", ipSegmentRawJSON(9))

	if err := EnsureIPSegmentBackfill(db); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !systemSettingFlagIsSet(db, ipSegmentBackfillKey) {
		t.Fatal("整轮回填跑完后必须写入标记键 ip_segment_backfill_v1")
	}
	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("backfill must write 1 segment, got %d", count)
	}

	// 标记已存在后再加一行新数据：第二轮**不应**处理它（标记短路），
	// 这样才证明第二轮真的走了标记分支而不是「恰好没有新数据」。
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.10.10", "", ipSegmentRawJSON(10))
	if err := EnsureIPSegmentBackfill(db); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("recount: %v", err)
	}
	if count != 1 {
		t.Fatalf("标记键存在时必须整轮短路，got %d rows（新增行说明标记未生效）", count)
	}
}

// 回填失败（批写入失败）时**不得写标记**，否则下次启动会永久跳过。
func TestEnsureIPSegmentBackfillDoesNotFlagOnFailure(t *testing.T) {
	db := newIPSegmentBackfillTestDB(t)
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.11.10", "", ipSegmentRawJSON(11))

	// 用 Before 回调制造确定性失败：只对 ip_segments 的 create 注入错误。
	injected := false
	if err := db.Callback().Create().Before("gorm:create").Register("fail_ip_segment_create", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameIPSegment {
			injected = true
			_ = tx.AddError(errTestIPSegmentWriteFailure)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	if err := EnsureIPSegmentBackfill(db); err == nil {
		t.Fatal("批写入失败时 EnsureIPSegmentBackfill 必须返回错误")
	}
	if !injected {
		t.Fatal("测试装置失效：注入的回调没有被触发")
	}
	if systemSettingFlagIsSet(db, ipSegmentBackfillKey) {
		t.Fatal("回填失败时绝不能写标记键，否则下次启动会永久跳过这批数据")
	}
}

// errTestIPSegmentWriteFailure 是注入用错误。定义在测试里，生产代码不感知。
var errTestIPSegmentWriteFailure = &ipSegmentTestError{"injected segment write failure"}

type ipSegmentTestError struct{ message string }

func (e *ipSegmentTestError) Error() string { return e.message }

// 判据④：回填必须分批，每批不超 ipSegmentBackfillBatchSize（500）行。
//
// 为什么不能只断言最终统计：最终统计无法区分「分了 4 批各 500」与
// 「1 批 2000」—— 后者正是「一个大事务包全表」，是判据④要禁止的形态。
// 所以用钩子逐批计数，断言每批上限与批数。
func TestIPSegmentBackfillBatchesEachAtMost500(t *testing.T) {
	db := newIPSegmentBackfillTestDB(t)

	// 1200 行 → 期望 500 + 500 + 200 = 3 批。
	const totalRows = 1200
	for index := 0; index < totalRows; index++ {
		// 四个不同的第三段轮换，使段表最终 4 行；行数仍是 1200。
		third := index % 4
		seedIPCacheRow(t, db, entity.LocationKindIP,
			lookupKeyForTest(index), "", ipSegmentRawJSON(third))
	}

	var batchSizes []int
	stats, err := BackfillIPSegmentsWithStats(db, func(batchSize int) {
		batchSizes = append(batchSizes, batchSize)
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Scanned != totalRows {
		t.Fatalf("must scan %d rows, got %d", totalRows, stats.Scanned)
	}
	if len(batchSizes) < 2 {
		t.Fatalf("判据④：回填必须分批，但只观察到 %d 批（%v）—— 一个大事务包全表", len(batchSizes), batchSizes)
	}
	if len(batchSizes) != stats.Batches {
		t.Fatalf("钩子观察到的批数 %d 与统计里的 %d 不一致", len(batchSizes), stats.Batches)
	}
	for index, size := range batchSizes {
		if size > ipSegmentBackfillBatchSize {
			t.Fatalf("判据④：第 %d 批有 %d 行，超过上限 %d", index+1, size, ipSegmentBackfillBatchSize)
		}
		if size <= 0 {
			t.Fatalf("第 %d 批行数为 %d，非法", index+1, size)
		}
	}
	// 1200 行按 500 分批必然是 500/500/200。
	want := []int{500, 500, 200}
	if len(batchSizes) != len(want) {
		t.Fatalf("expected %d batches %v, got %d batches %v", len(want), want, len(batchSizes), batchSizes)
	}
	for index := range want {
		if batchSizes[index] != want[index] {
			t.Fatalf("batch %d size=%d, want %d (all=%v)", index+1, batchSizes[index], want[index], batchSizes)
		}
	}
	// 4 个不同的段 → 4 行；同段的多行必须合并（判据⑥的批内形态）。
	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Fatalf("1200 行覆盖 4 个段，段表必须只有 4 行，got %d", count)
	}
	if stats.NewlyCreated != 4 {
		t.Fatalf("must report 4 newly created segments, got %d", stats.NewlyCreated)
	}
}

// lookupKeyForTest 生成 1200 个互不相同的 IP（120.239.x.y 形式，避开 0/255）。
func lookupKeyForTest(index int) string {
	// x = index/200 + 1（1..6），y = index%200 + 1（1..200）
	return "10." + itoa(index/200+1) + "." + itoa(index%200+1) + ".1"
}

// 判据⑤：raw_json 为空或不可解析的行必须**跳过并计数**，且不能让整批失败。
func TestIPSegmentBackfillSkipsBadRawJSONAndCounts(t *testing.T) {
	db := newIPSegmentBackfillTestDB(t)

	// 好行。
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.20.10", "", ipSegmentRawJSON(20))
	// raw_json 为空（历史占位行）。
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.21.10", "", "")
	// raw_json 不可解析（半截响应）。
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.22.10", "", `{"region":"中国 广东","beginip":`)
	// raw_json 合法但缺 beginip/endip。
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.23.10", "", `{"region":"美国"}`)
	// 手机号行：kind 过滤掉，不参与扫描。
	seedIPCacheRow(t, db, entity.LocationKindPhone, "13800138000", "", `{"province":"云南"}`)
	// 另一条好行（确认坏行没有中断后续处理）。
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.24.10", "", ipSegmentRawJSON(24))

	stats, err := BackfillIPSegmentsWithStats(db, nil)
	if err != nil {
		t.Fatalf("判据⑤：坏行不能让整批失败，但回填返回了错误: %v", err)
	}
	// 5 条 kind='ip' 行（手机号行被 WHERE 过滤）。
	if stats.Scanned != 5 {
		t.Fatalf("must scan 5 ip rows (phone row filtered), got %d", stats.Scanned)
	}
	if stats.SkippedRawJSONEmpty != 1 {
		t.Fatalf("raw_json 为空必须计 1，got %d (stats=%+v)", stats.SkippedRawJSONEmpty, stats)
	}
	if stats.SkippedRawJSONUnparsable != 1 {
		t.Fatalf("raw_json 不可解析必须计 1，got %d (stats=%+v)", stats.SkippedRawJSONUnparsable, stats)
	}
	if stats.SkippedNoRange != 1 {
		t.Fatalf("缺 beginip/endip 必须计 1，got %d (stats=%+v)", stats.SkippedNoRange, stats)
	}
	if stats.Skipped() != 3 {
		t.Fatalf("跳过项合计必须为 3，got %d (stats=%+v)", stats.Skipped(), stats)
	}
	if stats.NewlyCreated != 2 {
		t.Fatalf("两条好行必须都写进段表（坏行不得中断处理），got %d (stats=%+v)", stats.NewlyCreated, stats)
	}

	var segments []entity.IPSegment
	if err := db.Order("sample_ip asc").Find(&segments).Error; err != nil {
		t.Fatalf("list segments: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("段表必须只有 2 行，got %d: %+v", len(segments), segments)
	}
	if segments[0].SampleIP != "120.239.20.10" || segments[1].SampleIP != "120.239.24.10" {
		t.Fatalf("写入的段与预期不符: %+v", segments)
	}
}

// 段表不存在时回填必须静默返回、且**不写标记**（等表就绪后自然生效）。
func TestEnsureIPSegmentBackfillWithoutSegmentTableWritesNoFlag(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// 只建 system_settings 与缓存表，**不建 ip_segments**。
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.LocationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.30.10", "", ipSegmentRawJSON(30))

	if err := EnsureIPSegmentBackfill(db); err != nil {
		t.Fatalf("段表缺失时必须静默返回 nil, got %v", err)
	}
	if systemSettingFlagIsSet(db, ipSegmentBackfillKey) {
		t.Fatal("段表缺失时绝不能写标记键，否则表建好后本回填会被永久跳过")
	}
	if db.Migrator().HasTable(entity.TableNameIPSegment) {
		t.Fatal("回填不得自行建表（结构由 ensureIPSegmentSchema 负责）")
	}
}

// 缓存表不存在时同理：不写标记、不 panic。
func TestEnsureIPSegmentBackfillWithoutCacheTableWritesNoFlag(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.IPSegment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := EnsureIPSegmentBackfill(db); err != nil {
		t.Fatalf("缓存表缺失时必须静默返回 nil, got %v", err)
	}
	if systemSettingFlagIsSet(db, ipSegmentBackfillKey) {
		t.Fatal("缓存表缺失时绝不能写标记键")
	}
}

// 回填不碰业务主记录：缓存行一条不少、段表只有新增。
//
// 判据的变异方向是「把回填写成 DELETE + 重新 INSERT」或「先清空段表」——
// 那会让历史段数据的 fetched_at 全部丢失，也会在清理任务并发时产生窗口。
func TestIPSegmentBackfillDoesNotMutateCacheRows(t *testing.T) {
	db := newIPSegmentBackfillTestDB(t)
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.40.10", "", ipSegmentRawJSON(40))
	seedIPCacheRow(t, db, entity.LocationKindIP, "120.239.41.10", "commercial", ipSegmentRawJSON(41))
	seedIPCacheRow(t, db, entity.LocationKindPhone, "13800138001", "", `{"province":"云南"}`)

	var cacheBefore int64
	if err := db.Model(&entity.LocationCache{}).Count(&cacheBefore).Error; err != nil {
		t.Fatalf("count cache: %v", err)
	}
	if _, err := BackfillIPSegmentsWithStats(db, nil); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var cacheAfter int64
	if err := db.Model(&entity.LocationCache{}).Count(&cacheAfter).Error; err != nil {
		t.Fatalf("recount cache: %v", err)
	}
	if cacheBefore != cacheAfter {
		t.Fatalf("回填不得增删缓存行：before=%d after=%d", cacheBefore, cacheAfter)
	}

	// source 维度必须保住（同 range + 不同 source 是两条段）。
	//
	// 注意这里刻意让两条段的 range 不同，只靠 Source 区分是否被吞 ——
	// 全表按 source 升序时空串排在 commercial 之前，所以断言按下标取。
	var rows []entity.IPSegment
	if err := db.Order("source asc").Find(&rows).Error; err != nil {
		t.Fatalf("list segments: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 segments (standard + commercial), got %d: %+v", len(rows), rows)
	}
	sources := map[string]bool{}
	for _, row := range rows {
		sources[row.Source] = true
	}
	if !sources[""] || !sources["commercial"] {
		t.Fatalf("source 维度丢失：期望同时有 standard（空串）与 commercial，实际 %+v", rows)
	}
}
