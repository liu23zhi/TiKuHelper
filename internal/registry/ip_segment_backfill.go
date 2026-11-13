package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// ip_segments 的启动回填（task_doc/71 补口）。
//
// # 这个迁移存在的全部理由
//
// `service/location.go` 有两条会走到「段信息」的路径：
//
//	① 上游查询成功 → PersistIPSegmentsFromCacheRows（实时写入）
//	② 缓存命中     → PersistMissingIPSegmentsFromCacheRows（读时补写）
//
// 两者都是**从本次请求出发**的。于是有一道只靠它们补不上的缝：
// 「先查过这些 IP（已落 location_cache_entries）、后来才上线段表」的那批历史行，
// 既不会再走上游（缓存永久有效），也不会触发读时补写（除非那个 IP 又被查一次）。
// 这批数据在段表里永远缺席 —— 它们每次都被当作单 IP 回答，段命中对该批永久失效，
// 而这种退化在日志里看不出来，只表现为上游账单变高。
//
// 本迁移就是补这一批：扫 location_cache_entries 的 kind='ip' 行，
// 逐批解析 raw_json 里的 beginip/endip 写进段表。
//
// # 为什么需要「标记键」而不是纯列检测
//
// 纯列检测（只判断表/列在不在）对**数据回填**是错的语义：表建好了但数据没搬完时，
// 列检测会判「已完成」并永久跳过。所以这里按 121 决策 1 的 A 规则变体：
// 结构由 ensureIPSegmentSchema 管（`ip_segment_schema_ready`），
// 数据由本标记管（`ip_segment_backfill_v1`），**两个标记分开**。
// 标记只在整轮跑完后写；失败不写 → 下次启动重跑，靠 Upsert 幂等收敛。
const (
	// ipSegmentBackfillKey 是本回填的一次性标记。
	//
	// 命名按 121 §1.4 的 `_v1` 后缀规律：将来若要重跑（例如上游新增字段），
	// 换 v2 即可，不需要手工删标记。
	ipSegmentBackfillKey = "ip_segment_backfill_v1"

	// ipSegmentBackfillBatchSize 是每批处理的缓存行数（判据④的硬上限）。
	//
	// 与 ticket_attachment_blob_migration.go:29 / ai_ocr_cache_migration.go 的
	// 500 一致：SQLite 上 500 行的单事务提交是毫秒级，MySQL 上一次 500 行的
	// INSERT 也不会长到让人重启。
	ipSegmentBackfillBatchSize = 500
)

// EnsureIPSegmentBackfill 把 location_cache_entries 里 kind='ip' 的存量行
// 回填进 ip_segments（task_doc/71）。
//
// # 四个必须点
//
//  1. **必须分批**（判据④）：按主键游标分页，每批 ipSegmentBackfillBatchSize 行。
//     一个大事务包全表会让 SQLite 长时间持写锁（`database is locked`），
//     MySQL 上表现为业务写入全阻塞。
//  2. **幂等**（判据②）：标记键 + UpsertIPSegmentsWithDB 的 ON CONFLICT。
//     连跑两次，第二次新增 0 行。
//  3. **单行失败不中断整批**（判据⑤）：解析不了的行只计入对应跳过项，
//     绝不让整批失败；只有**批写入**失败才停整轮。
//  4. **不碰任何业务主记录**：只读 location_cache_entries、只写 ip_segments，
//     没有 UPDATE / DELETE。
//
// # 失败策略
//
//   - 前置表缺失（location_cache_entries 或 ip_segments 不存在）→ 返回 nil
//     且**不写标记**，等表就绪后自然生效；
//   - 回填失败 → 返回 error 给调用方，**不写标记**，下次启动重跑（幂等）；
//   - 标记写失败 → 只记 SysLog（数据已回填好，重跑幂等）。
func EnsureIPSegmentBackfill(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("IP 段归属回填异常: %v", recovered)
		}
	}()
	if systemSettingFlagIsSet(db, ipSegmentBackfillKey) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLocationCache) {
		// 归属地缓存表还没建出来（极端空库首次启动）：不写标记，
		// 否则缓存表建好之后本回填会被永久跳过。
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		// 段表还没建（ensureIPSegmentSchema 失败或顺序差异）：不写标记。
		return nil
	}
	stats, backfillErr := BackfillIPSegmentsWithStats(db, nil)
	logIPSegmentBackfillStats(stats)
	if backfillErr != nil {
		return fmt.Errorf("回填 IP 段归属失败: %w", backfillErr)
	}
	if err := setSystemSettingFlag(db, ipSegmentBackfillKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入 IP 段归属回填标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// IPSegmentBackfillStats 是一次回填的结果统计。
//
// 分项计数不是装饰：`SkippedRawJSONUnparsable` 是**数据异常指示器**
// （正常库应为 0），而 `SkippedNoRange` 是正常的（IPv6 / 上游没给 beginip
// 的行本来就该跳过）。糊成一个 skipped 数字后，两条完全相反的结论就分不开了。
type IPSegmentBackfillStats struct {
	// Scanned 是扫描到的 kind='ip' 行数。
	Scanned int
	// NewlyCreated 是**批内**解析出的、段表里原本没有的段数（判据②的判据字段）。
	NewlyCreated int
	// Existing 是段表里已存在、本次不需要写的段数。
	Existing int
	// Batches 是实际执行的批数（判据④：验证「没有一个大事务包全表」）。
	Batches int
	// 以下三个是跳过项，与 service.IPSegmentSkip* 一一对应（判据⑤）。
	SkippedRawJSONEmpty      int
	SkippedRawJSONUnparsable int
	SkippedNoRange           int
}

// Skipped 返回全部跳过项之和。
func (s IPSegmentBackfillStats) Skipped() int {
	return s.SkippedRawJSONEmpty + s.SkippedRawJSONUnparsable + s.SkippedNoRange
}

// logIPSegmentBackfillStats 输出回填结果，并对「只能由数据损坏产生」的计数单独告警。
func logIPSegmentBackfillStats(stats IPSegmentBackfillStats) {
	if stats.Scanned == 0 {
		return
	}
	logger.SysLog(fmt.Sprintf(
		"IP 段归属回填完成：扫描 %d 行（%d 批）、新增 %d 段、已存在 %d 段、跳过（raw_json 空 %d / 不可解析 %d / 无可用区间 %d）",
		stats.Scanned, stats.Batches, stats.NewlyCreated, stats.Existing,
		stats.SkippedRawJSONEmpty, stats.SkippedRawJSONUnparsable, stats.SkippedNoRange))
	if stats.SkippedRawJSONUnparsable > 0 {
		// 单独一条、并给出追查入口：正常库里 raw_json 要么为空（历史占位行）、
		// 要么是上游原文（合法 JSON）。不可解析说明**数据损坏**或写入了半截响应，
		// 必须让人看见而不是混进正常统计里。
		logger.SysLog(fmt.Sprintf(
			"⚠️ IP 段归属回填跳过 %d 行「raw_json 不可解析」的记录 —— "+
				"这是一个**只能由数据损坏或半截写入产生**的计数，正常库应为 0。"+
				"请追查这些 location_cache_entries 行的 raw_json 是否被截断/改写。",
			stats.SkippedRawJSONUnparsable))
	}
}

// BackfillIPSegmentsWithStats 执行回填并返回统计。
//
// onBatch 可为 nil；每批处理完调用一次，参数是本批行数。
//
// 为什么导出并带 onBatch 钩子：判据④「每批不超 500 行」与判据②「连跑两次
// 第二次新增 0」都需要拿到中间量 —— 只看最终统计无法区分「分了 4 批各 500」
// 与「1 批 2000」。
func BackfillIPSegmentsWithStats(db *gorm.DB, onBatch func(batchSize int)) (IPSegmentBackfillStats, error) {
	stats := IPSegmentBackfillStats{}
	if db == nil {
		return stats, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLocationCache) ||
		!db.Migrator().HasTable(entity.TableNameIPSegment) {
		return stats, nil
	}

	// 游标分页：按**主键** (lookup_key, source) 升序推进。
	//
	// 为什么用主键游标而不是 OFFSET：
	//   - OFFSET 在大表上随偏移线性变慢（每页都要扫过前 N 行），而
	//     location_cache_entries 的增长没有上限（每个新 IP 一行）；
	//   - 主键是 (kind, lookup_key, source)，kind 恒为 'ip' 已固定，
	//     所以 (lookup_key > ? OR (lookup_key = ? AND source > ?)) 正好是
	//     PK 索引上的范围扫描 —— SQLite 与 MySQL 都吃这个索引。
	//
	// 为什么不用 rowid：`rowid` 是 SQLite 专有的隐式列，MySQL 上没有。
	// 本仓生产部署（run/）默认 SQLite，但 MySQL 部署是同仓支持的，
	// 用 rowid 会让回填在 MySQL 上直接报未知列。
	cursor := ipSegmentBackfillCursor{Set: false}
	for {
		rows := make([]ipSegmentBackfillRow, 0, ipSegmentBackfillBatchSize)
		query := db.Session(&gorm.Session{NewDB: true}).
			Table(entity.TableNameLocationCache).
			Select("kind, lookup_key, source, raw_json").
			Where("kind = ?", entity.LocationKindIP)
		if cursor.Set {
			query = query.Where("(lookup_key > ? OR (lookup_key = ? AND source > ?))",
				cursor.LookupKey, cursor.LookupKey, cursor.Source)
		}
		if err := query.
			Order("lookup_key asc, source asc").
			Limit(ipSegmentBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return stats, fmt.Errorf("读取待回填的归属地缓存失败: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		stats.Batches++
		stats.Scanned += len(rows)
		last := rows[len(rows)-1]
		cursor = ipSegmentBackfillCursor{LookupKey: last.LookupKey, Source: last.Source, Set: true}

		batchStats, err := backfillIPSegmentBatch(db, rows)
		stats.NewlyCreated += batchStats.NewlyCreated
		stats.Existing += batchStats.Existing
		stats.SkippedRawJSONEmpty += batchStats.SkippedRawJSONEmpty
		stats.SkippedRawJSONUnparsable += batchStats.SkippedRawJSONUnparsable
		stats.SkippedNoRange += batchStats.SkippedNoRange

		if onBatch != nil {
			onBatch(len(rows))
		}
		if err != nil {
			// 整批写入失败 → 停整轮、不写标记（下次启动重跑）。已提交的批不回退，
			// Upsert 幂等补齐。
			return stats, err
		}
	}
	return stats, nil
}

// ipSegmentBackfillCursor 是主键游标的进程内表示。
//
// Set 单独一位而不是靠「空串」判定：lookup_key 与 source 都可能是空串
// （source 对 IP 行可以是 ”），此时「空 = 未开始」的判断会退化成永远从头查，
// 导致死循环。
type ipSegmentBackfillCursor struct {
	LookupKey string
	Source    string
	Set       bool
}

// ipSegmentBackfillRow 是回填时从 location_cache_entries 读出的最小列集合。
//
// 用独立的影子结构而不是 entity.LocationCache：本回填只读 4 列，
// 而 LocationCache 带 address / fetched_at / updated_at 等无关列。
type ipSegmentBackfillRow struct {
	Kind      string `gorm:"column:kind"`
	LookupKey string `gorm:"column:lookup_key"`
	Source    string `gorm:"column:source"`
	RawJSON   string `gorm:"column:raw_json"`
}

// backfillIPSegmentBatch 处理一批缓存行，返回本批统计。
//
// 批内按唯一键去重后**一次 Upsert**：逐行写入是 500 次往返，而
// UpsertIPSegmentsWithDB 本来就是批量的。同一批里出现两条同段行是常态
// （上游对不同 IP 常常返回同一段）。
func backfillIPSegmentBatch(db *gorm.DB, rows []ipSegmentBackfillRow) (IPSegmentBackfillStats, error) {
	stats := IPSegmentBackfillStats{}
	candidates := make([]entity.IPSegment, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		// 复用 service 层的唯一解析入口，不在这里重写一遍 —— 三处
		// （实时写入 / 读时补写 / 回填）各写一份解析必然漂移。
		cacheRow := entity.LocationCache{
			Kind:      row.Kind,
			LookupKey: row.LookupKey,
			Source:    row.Source,
			RawJSON:   row.RawJSON,
		}
		segment, reason, ok := service.ParseIPSegmentFromCacheRow(cacheRow)
		if !ok {
			switch reason {
			case service.IPSegmentSkipRawJSONEmpty:
				stats.SkippedRawJSONEmpty++
			case service.IPSegmentSkipRawJSONUnparsable:
				stats.SkippedRawJSONUnparsable++
			default:
				// not_ip（本查询已过滤，理论不会出现）与 no_usable_range 合并计数：
				// 两者都是「这条行正常地不产生段」，无需分项。
				stats.SkippedNoRange++
			}
			continue
		}
		key := service.IPSegmentUniqueKey(segment.BeginIP, segment.EndIP, segment.Source)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, segment)
	}
	if len(candidates) == 0 {
		return stats, nil
	}
	// 先判定「哪些段表里还没有」，才能给出「新增 N」这个判据②的判据字段。
	missing, err := service.MissingIPSegmentsWithDB(db, candidates)
	if err != nil {
		return stats, err
	}
	stats.Existing = len(candidates) - len(missing)
	if len(missing) == 0 {
		return stats, nil
	}
	if err := service.UpsertIPSegmentsWithDB(db, missing); err != nil {
		return stats, err
	}
	stats.NewlyCreated = len(missing)
	return stats, nil
}
