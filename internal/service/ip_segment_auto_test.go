package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// task_doc/71 的「自动沉淀」口径。
//
// # 用户口径（2026-11-13 明确）
//
// IP 段归属从「管理员手动点按钮批量导入」改成**自动导入**。因此：
//   - 不存在任何手动导入端点（反向断言见 internal/api/ip_segment_router_test.go）；
//   - 「新发生的查询」由上游成功分支实时沉淀（既有行为）；
//   - 「先查过、后来才上线段表」的存量由**缓存命中路径 + 启动回填**自动沉淀。

// ipSegmentAutoTestDB 建立带 ip_segments 与 location_cache_entries 两表的测试库。
func ipSegmentAutoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("resolve sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.LocationCache{}, &entity.IPSegment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// ipSegmentTestUpstream 起一个返回固定段信息的假上游。
//
// 返回的请求计数器用于断言「第二次查询真的命中了缓存、没有回源」——
// 没有这条断言，「缓存命中路径也沉淀」的用例可能实际走的是上游分支，
// 于是测的不是它自称测的那条路径。
func ipSegmentTestUpstream(t *testing.T) (*atomic.Int64, string) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ip": "120.239.173.110", "region": "中国 广东 中山",
			"isp": "China Mobile Communications Group Co., Ltd.", "llc": "移动", "asn": "AS9808",
			"beginip": "120.239.173.102", "endip": "120.239.173.112",
		})
	}))
	t.Cleanup(server.Close)
	previousIPURL := locationIPInfoURL
	locationIPInfoURL = server.URL + "/api/v1/network/ipinfo"
	t.Cleanup(func() {
		locationIPInfoURL = previousIPURL
		locationLookupGroup.reset()
	})
	return &requests, server.URL
}

// 判据③（核心）：缓存命中路径也必须沉淀段归属。
//
// 场景与任务书完全一致：
//
//	① 先查 IP → 写缓存（此时段表被清空，模拟「段表后来才上线」）
//	② 清空 ip_segments
//	③ 再查同一 IP → **命中缓存**（不回源）
//	④ 段表必须重新出现该段
//
// 这条用例必须能捕获「把缓存命中处的 Persist 调用删掉」这个变异 —— 见变异 a。
func TestIPSegmentPersistedOnCacheHitPath(t *testing.T) {
	requests, _ := ipSegmentTestUpstream(t)
	db := ipSegmentAutoTestDB(t)

	const ip = "120.239.173.110"

	// ① 首次查询：走上游，写缓存 + 写段表。
	first := LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if first[ip].Region == "" {
		t.Fatalf("first lookup must resolve, got %+v", first[ip])
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("first lookup must hit the upstream exactly once, got %d", got)
	}
	var segmentCount int64
	if err := db.Model(&entity.IPSegment{}).Count(&segmentCount).Error; err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if segmentCount != 1 {
		t.Fatalf("first lookup must persist exactly one segment, got %d", segmentCount)
	}

	// ② 清空段表：模拟「这个 IP 在段表上线之前就已经查过、缓存里已有数据」。
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&entity.IPSegment{}).Error; err != nil {
		t.Fatalf("clear segments: %v", err)
	}
	if err := db.Model(&entity.IPSegment{}).Count(&segmentCount).Error; err != nil {
		t.Fatalf("count after clear: %v", err)
	}
	if segmentCount != 0 {
		t.Fatalf("segments must be cleared before the cache-hit lookup, got %d", segmentCount)
	}

	// ③ 再查同一 IP：必须**命中缓存**（不回源），同时补写段表。
	second := LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if second[ip].Region == "" {
		t.Fatalf("second lookup must still resolve from cache, got %+v", second[ip])
	}
	// 关键前置断言：如果这里回源了，下面的段表断言证明的就不是缓存命中路径。
	if got := requests.Load(); got != 1 {
		t.Fatalf("second lookup must be served from cache (no extra upstream call), got %d requests", got)
	}

	// ④ 段表必须重新出现该段。
	segment, err := FindIPSegmentWithDB(db, ip)
	if err != nil {
		t.Fatalf("find segment: %v", err)
	}
	if segment == nil {
		t.Fatal("缓存命中路径必须补写段归属，但段表里没有该段（这是 task_doc/71 的真缺口）")
	}
	if segment.BeginText != "120.239.173.102" || segment.EndText != "120.239.173.112" {
		t.Fatalf("backfilled segment has wrong range: %+v", segment)
	}
	if segment.ASN != "AS9808" || segment.LLC != "移动" {
		t.Fatalf("backfilled segment lost upstream fields: %+v", segment)
	}
}

// 判据③的加严版：缓存命中补写必须是**只补缺**，不是「每次都 UPDATE」。
//
// 理由：缓存命中在日志列表每翻一页都会走一次。若用
// PersistIPSegmentsFromCacheRows（无条件 Upsert），每次翻页都会刷新段表的
// fetched_at/updated_at —— 等于把读变成写，还丢掉了「这条归属地是什么时候
// 从上游查到的」这个语义。
func TestIPSegmentCacheHitDoesNotRewriteExistingSegment(t *testing.T) {
	ipSegmentTestUpstream(t)
	db := ipSegmentAutoTestDB(t)

	const ip = "120.239.173.110"
	LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{UAPIKey: "uapi-key"})

	var before entity.IPSegment
	if err := db.Where("sample_ip = ?", ip).First(&before).Error; err != nil {
		t.Fatalf("read segment: %v", err)
	}
	// 人为把 fetched_at 改成很久以前：若补写路径执行了 UPDATE，它会被刷新成 now。
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.Model(&entity.IPSegment{}).Where("id = ?", before.ID).
		UpdateColumn("fetched_at", oldTime).Error; err != nil {
		t.Fatalf("backdate fetched_at: %v", err)
	}

	// 缓存命中（多次）：都不得改写已存在的段。
	for i := 0; i < 3; i++ {
		LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{UAPIKey: "uapi-key"})
	}

	var after entity.IPSegment
	if err := db.Where("id = ?", before.ID).First(&after).Error; err != nil {
		t.Fatalf("re-read segment: %v", err)
	}
	if !after.FetchedAt.Equal(oldTime) {
		t.Fatalf("缓存命中路径不得改写已存在的段：fetched_at 被刷成了 %v（期望保持 %v）",
			after.FetchedAt, oldTime)
	}
	// 也不得新增行。
	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if count != 1 {
		t.Fatalf("缓存命中路径不得新增段行，got %d rows", count)
	}
}

// 判据⑥：一个 IP 只产出一个段。
//
// ParseIPSegmentFromInfo 已有此语义（一次响应只有一个 beginip/endip），
// 这里钉住它不被「按 source 拆成多条」之类的改动破坏。
func TestIPSegmentOnePerIPIncludingCacheHitPath(t *testing.T) {
	ipSegmentTestUpstream(t)
	db := ipSegmentAutoTestDB(t)

	const ip = "120.239.173.110"
	// 反复查同一个 IP（首次走上游，之后全部命中缓存），并夹一次段表清空。
	LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&entity.IPSegment{}).Error; err != nil {
		t.Fatalf("clear segments: %v", err)
	}
	for i := 0; i < 3; i++ {
		LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{UAPIKey: "uapi-key"})
	}

	var rows []entity.IPSegment
	if err := db.Where("sample_ip = ?", ip).Find(&rows).Error; err != nil {
		t.Fatalf("list segments: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("一个 IP 只能产出一个段，got %d rows: %+v", len(rows), rows)
	}
	if rows[0].BeginText != "120.239.173.102" {
		t.Fatalf("unexpected segment: %+v", rows[0])
	}
}

// 判据⑤的服务侧一半：解析失败/不适用的行必须**按原因分别计数**且不产生段，
// 而不是让整个调用失败。
//
// 这条同时钉住「三处解析共用同一个入口」这个设计：分类口径一旦分叉，
// 回填侧的计数就会与这里不一致。
func TestParseIPSegmentFromCacheRowClassifiesSkips(t *testing.T) {
	testCases := []struct {
		name    string
		row     entity.LocationCache
		wantOK  bool
		wantWhy string
	}{
		{
			name:    "手机号行不产生段",
			row:     entity.LocationCache{Kind: entity.LocationKindPhone, LookupKey: "13800138000", RawJSON: `{}`},
			wantWhy: IPSegmentSkipNotIP,
		},
		{
			name:    "raw_json 为空",
			row:     entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "1.2.3.4", RawJSON: ""},
			wantWhy: IPSegmentSkipRawJSONEmpty,
		},
		{
			name:    "raw_json 只有空白",
			row:     entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "1.2.3.4", RawJSON: "   \n\t "},
			wantWhy: IPSegmentSkipRawJSONEmpty,
		},
		{
			name:    "raw_json 不可解析",
			row:     entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "1.2.3.4", RawJSON: `{"region":`},
			wantWhy: IPSegmentSkipRawJSONUnparsable,
		},
		{
			name: "raw_json 是合法 JSON 但没有 beginip/endip",
			row: entity.LocationCache{
				Kind: entity.LocationKindIP, LookupKey: "1.2.3.4",
				RawJSON: `{"ip":"1.2.3.4","region":"美国"}`,
			},
			wantWhy: IPSegmentSkipNoUsableRange,
		},
		{
			name: "IPv6 段不进表",
			row: entity.LocationCache{
				Kind: entity.LocationKindIP, LookupKey: "2001:db8::1",
				RawJSON: `{"beginip":"2001:db8::1","endip":"2001:db8::ffff"}`,
			},
			wantWhy: IPSegmentSkipNoUsableRange,
		},
		{
			name: "可用段正常解析",
			row: entity.LocationCache{
				Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: "standard",
				RawJSON: `{"beginip":"8.8.8.0","endip":"8.8.8.255","region":"美国","asn":"AS15169"}`,
			},
			wantOK: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			segment, reason, ok := ParseIPSegmentFromCacheRow(testCase.row)
			if ok != testCase.wantOK {
				t.Fatalf("ok=%v (reason=%q), want ok=%v", ok, reason, testCase.wantOK)
			}
			if !testCase.wantOK {
				if reason != testCase.wantWhy {
					t.Fatalf("skip reason=%q, want %q", reason, testCase.wantWhy)
				}
				return
			}
			if reason != "" {
				t.Fatalf("successful parse must return empty reason, got %q", reason)
			}
			if segment.BeginIP != 0x08080800 || segment.EndIP != 0x080808FF {
				t.Fatalf("wrong numeric range: %+v", segment)
			}
			if segment.SampleIP != "8.8.8.8" || segment.Source != "standard" {
				t.Fatalf("sample/source lost: %+v", segment)
			}
		})
	}
}

// collectIPSegmentsFromCacheRows 必须按唯一键去重（同一批里两条同段行是常态），
// 并给出分项跳过计数。
func TestCollectIPSegmentsDeduplicatesAndCountsSkips(t *testing.T) {
	const sameSegment = `{"beginip":"8.8.8.0","endip":"8.8.8.255","region":"美国"}`
	rows := []entity.LocationCache{
		{Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: "standard", RawJSON: sameSegment},
		// 同一段的第二个 IP：必须合并成一条段记录。
		{Kind: entity.LocationKindIP, LookupKey: "8.8.8.1", Source: "standard", RawJSON: sameSegment},
		// 同段但 source 不同：按唯一键 (begin,end,source) 是**另一条**，要保留。
		{Kind: entity.LocationKindIP, LookupKey: "8.8.8.2", Source: "commercial", RawJSON: sameSegment},
		{Kind: entity.LocationKindIP, LookupKey: "1.1.1.1", RawJSON: ""},
		{Kind: entity.LocationKindIP, LookupKey: "1.1.1.2", RawJSON: `not json`},
		{Kind: entity.LocationKindPhone, LookupKey: "13800138000", RawJSON: `{}`},
		{Kind: entity.LocationKindIP, LookupKey: "9.9.9.9", RawJSON: `{"region":"美国"}`},
	}
	segments, skipped := collectIPSegmentsFromCacheRows(rows)

	if len(segments) != 2 {
		t.Fatalf("must dedupe by (begin,end,source): expected 2 segments, got %d: %+v", len(segments), segments)
	}
	if skipped[IPSegmentSkipRawJSONEmpty] != 1 {
		t.Fatalf("empty raw_json must be counted once, got %d", skipped[IPSegmentSkipRawJSONEmpty])
	}
	if skipped[IPSegmentSkipRawJSONUnparsable] != 1 {
		t.Fatalf("unparsable raw_json must be counted once, got %d", skipped[IPSegmentSkipRawJSONUnparsable])
	}
	if skipped[IPSegmentSkipNotIP] != 1 {
		t.Fatalf("phone row must be counted as not_ip, got %d", skipped[IPSegmentSkipNotIP])
	}
	if skipped[IPSegmentSkipNoUsableRange] != 1 {
		t.Fatalf("row without range must be counted, got %d", skipped[IPSegmentSkipNoUsableRange])
	}
}

// 判据⑤的服务侧另一半：坏行必须跳过，**不能让整批失败**。
//
// 一条 raw_json 损坏的行混在好行中间时，好行仍必须写进段表。
func TestPersistIPSegmentsSkipsBadRowsWithoutFailingBatch(t *testing.T) {
	db := ipSegmentAutoTestDB(t)
	rows := []entity.LocationCache{
		{Kind: entity.LocationKindIP, LookupKey: "1.1.1.1", RawJSON: `{"truncated":`},
		{Kind: entity.LocationKindIP, LookupKey: "2.2.2.2", RawJSON: ""},
		{Kind: entity.LocationKindIP, LookupKey: "1.1.1.2", RawJSON: `{"region":"美国"}`},
		{
			Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: "standard",
			RawJSON: `{"beginip":"8.8.8.0","endip":"8.8.8.255","region":"美国","asn":"AS15169"}`,
		},
	}
	PersistIPSegmentsFromCacheRows(db, rows)

	var segments []entity.IPSegment
	if err := db.Find(&segments).Error; err != nil {
		t.Fatalf("list segments: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("bad rows must be skipped without failing the batch: expected 1 segment, got %d: %+v", len(segments), segments)
	}
	if segments[0].SampleIP != "8.8.8.8" {
		t.Fatalf("the surviving segment is the wrong one: %+v", segments[0])
	}
}

// PersistMissingIPSegmentsFromCacheRows 的返回值语义：返回「本次真正补写的段数」。
//
// 判据②（回填幂等）在 service 侧的同构钉子：第二次调用必须返回 0。
func TestPersistMissingIPSegmentsReturnsZeroWhenAlreadyPresent(t *testing.T) {
	db := ipSegmentAutoTestDB(t)
	rows := []entity.LocationCache{{
		Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: "standard",
		RawJSON: `{"beginip":"8.8.8.0","endip":"8.8.8.255","region":"美国"}`,
	}}

	if got := PersistMissingIPSegmentsFromCacheRows(db, rows); got != 1 {
		t.Fatalf("first call must write 1 segment, got %d", got)
	}
	if got := PersistMissingIPSegmentsFromCacheRows(db, rows); got != 0 {
		t.Fatalf("second call must write 0 segments (already present), got %d", got)
	}
	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("idempotent write must keep 1 row, got %d", count)
	}
}

// 段表不存在时补写路径必须静默返回 0（老库首次启动的顺序差异），
// 绝不能 panic 或让归属地读取失败。
func TestPersistMissingIPSegmentsToleratesMissingSegmentTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("resolve sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// 只建缓存表，**不建段表**。
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatalf("migrate cache: %v", err)
	}
	rows := []entity.LocationCache{{
		Kind: entity.LocationKindIP, LookupKey: "8.8.8.8",
		RawJSON: `{"beginip":"8.8.8.0","endip":"8.8.8.255"}`,
	}}
	if got := PersistMissingIPSegmentsFromCacheRows(db, rows); got != 0 {
		t.Fatalf("missing segment table must yield 0, got %d", got)
	}
	// 顺带确认整表没被意外建出来（那会让「等迁移就绪」的前提失效）。
	if db.Migrator().HasTable(entity.TableNameIPSegment) {
		t.Fatal("补写路径不得自行建表")
	}
}
