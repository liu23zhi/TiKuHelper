package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 工单图片附件的保留策略（用户 2026-11-13 决策：
// 「清理逻辑**参考日志文件**，添加**按照时间清理**，**按照大小清理**」）。
//
// # 参照模板与本文件的对应关系
//
//	service/log_retention.go      → 本文件（三个参数 + 默认值 + 后台循环的模式）
//	pkg/logger.pruneLogFilesByAge → PruneTicketAttachmentsByAge
//	pkg/logger.pruneLogFiles      → PruneTicketAttachmentsBySize
//	StartLogRetentionCleanup      → StartTicketAttachmentRetentionCleanup
//
// 口径一致的两点必须保住：① 三个参数全部落在 system_settings 里，默认值写成本包常量；
// ② 后台循环 interval<=0 时**每轮重读配置**（用 time.Timer 而不是 time.Ticker），
// 否则管理员改周期必须重启进程才生效。
//
// # 与既有的 PurgeExpiredTicketAttachmentBlobs 是什么关系（不要混为一谈）
//
// ticket_attachment_blob.go 的 PurgeExpiredTicketAttachmentBlobs 做的是
// 「ref_count<=0 且超过 24 小时」的**延迟删除**，它删的是 blobs 表里的字节本体，
// 判据里没有任何「时间跨度」或「总容量」。本文件的清理对象是
// **ticket_attachments 业务行**，判据是「创建时间超过 max_age_days」与
// 「全表密文总量超过 max_total_gib」。两者互补：
//
//	本文件把附件行标记为已清理（purged）→ 顺带收敛 blob 的 ref_count
//	→ 归零的 blob 交给 PurgeExpiredTicketAttachmentBlobs 在缓冲期后删除字节。
//
// # 三个不可协商点（本文件的全部价值）
//
//  ① **按大小清理必须从最老的删起**（`ORDER BY created_at ASC, id ASC`）。
//     乱序删除会让同一份数据两次运行得到不同结果，清理不可复现，
//     而且会把刚上传的图删掉、留下几年前的老图 —— 与保留策略的意图相反。
//
//  ② **清理附件必须同时收敛 blob 的 ref_count**。blob 表的 ref_count 是
//     「有多少个附件行指向这份 blob」的缓存（事实来源是 COUNT，见
//     TicketAttachmentBlobRefCountWithDB），它决定 24 小时延迟删除会不会
//     误删仍在被引用的字节。所以「置空密文」与「清掉 blob_sha256 指向 +
//     重算 ref_count」必须发生在**同一个事务**里，否则出现两种事故：
//     指针还指着但计数没减 → blob 永久泄漏（永不释放）；
//     计数减了但指针还在 → 下一轮重算又把计数加回去 → 迟迟不释放。
//
//  ③ **只清密文，绝不删工单与工单消息**。本文件只有 UPDATE，没有一条 DELETE
//     打到 tickets / ticket_messages，也没有 DELETE 打到 ticket_attachments
//     （见下一节的保留行语义）。清理是「内容降级」，不是「业务记录删除」。
//
// # 保留行的语义（为什么不物理删附件行）
//
// 清掉图片后**留一行 ticket_attachments 记录**，只把 ciphertext / nonce 置空并写
// purged_at，byte_size / media_type / sha256 / message_id 原样保留。理由：
//
//   - message_id 是「这张图属于哪条回复」的唯一绑定。物理删行会让历史消息里
//     出现无法解释的空洞（前端拿到 n 条消息却只有 n-1 条带图，且无法区分
//     「当时没发图」与「图被清理了」）；
//   - 保留 media_type / byte_size 才能在前端显示「图片已按保留策略清理」这种
//     可解释的占位，而不是让客户端以为工单数据损坏；
//   - 保留 sha256 让「同图重新上传」仍然命中 blob 去重语义（sha256 是内容指纹，
//     不是密文），也让排查时能说清「当时那张图是什么」。
//
// 读取路径对此必须 fail-closed 且给出独立状态：purged_at 非空的行没有密文可解，
// 任何「按 id 直接解密」的入口都要显式拒绝（现有 ReadTicketAttachmentWithDB 会因
// nonce/ciphertext 长度检查返回 ErrTicketAttachmentInvalid，语义正确但提示不友好，
// 展示层应改为按 purged_at 给出「已清理」文案 —— 那段属于 ticket_attachment.go，
// 不在本文件的写入范围）。

const (
	// TicketAttachmentRetentionKeyPrefix 是全部工单图片保留策略键的统一前缀。
	TicketAttachmentRetentionKeyPrefix = "ticket_attachment_retention_"
	// TicketAttachmentRetentionMaxAgeDaysSettingKey 是按时间清理的天数（>0）。
	TicketAttachmentRetentionMaxAgeDaysSettingKey = "ticket_attachment_retention_max_age_days"
	// TicketAttachmentRetentionMaxTotalGiBSettingKey 是全部附件密文总量上限，单位 **GiB**。
	TicketAttachmentRetentionMaxTotalGiBSettingKey = "ticket_attachment_retention_max_total_gib"
	// TicketAttachmentRetentionCleanupIntervalMinutesSettingKey 是后台清理间隔（分钟）。
	TicketAttachmentRetentionCleanupIntervalMinutesSettingKey = "ticket_attachment_retention_cleanup_interval_minutes"
)

// 默认值（用户决策：按时间 180 天、按大小 20 GiB、每 60 分钟跑一次）。
const (
	// DefaultTicketAttachmentRetentionMaxAgeDays 默认保留 180 天。
	//
	// 比日志的 30 天长得多，因为两者性质不同：日志是可重生成的运行轨迹，
	// 工单图片是用户提供的**申诉证据**，删早了会让历史工单失去上下文。
	DefaultTicketAttachmentRetentionMaxAgeDays = 180
	// DefaultTicketAttachmentRetentionMaxTotalGiB 默认 20 GiB。
	DefaultTicketAttachmentRetentionMaxTotalGiB int64 = 20
	// DefaultTicketAttachmentRetentionCleanupIntervalMinutes 默认每 60 分钟一次。
	DefaultTicketAttachmentRetentionCleanupIntervalMinutes = 60
)

// 上限只为挡住明显离谱的输入，不是业务限制（与 log_retention.go 同口径）。
const (
	MaxTicketAttachmentRetentionAgeDays               = 3650
	MaxTicketAttachmentRetentionTotalGiB        int64 = 1024
	MaxTicketAttachmentRetentionCleanupInterval       = 1440
)

// TicketAttachmentRetentionBatchSize 是单批处理的附件行数。
//
// 分批而不是一条大 UPDATE 全扫：一次「按大小清理」可能涉及几千行，
// 单条大事务会长时间持写锁（SQLite 上直接是 database is locked）。
// 每批一个短事务，批与批之间让出写锁。
const TicketAttachmentRetentionBatchSize = 200

// ticketAttachmentRetentionGiB 是 GiB → 字节的唯一换算因子（本文件内）。
//
// 库里存 GiB 整数而不是字节：管理员在页面上填的就是 20，存字节会出现
// 21474836480 这种值，手滑少一位就是 10 倍差距而页面上都显示「20 GB」量级。
const ticketAttachmentRetentionGiB int64 = 1 << 30

// ticketAttachmentRetentionMaxBatches 防止异常状态下无限循环
// （例如候选行一直存在但一行都清不掉）。
const ticketAttachmentRetentionMaxBatches = 50

// ErrTicketAttachmentRetentionNoDatabase 表示没有数据库连接，无法读写配置或清理。
var ErrTicketAttachmentRetentionNoDatabase = errors.New("工单图片保留策略需要数据库连接")

// TicketAttachmentRetentionSettings 是配置的读写形态。
//
// 单位写进字段名后缀（GiB / Days / Minutes），前端不用猜；MaxTotalBytes 是
// 只读派生值，供页面显示「当前占用 3.2 GB / 20 GiB」这类精确文案。
type TicketAttachmentRetentionSettings struct {
	// MaxAgeDays 是按时间清理的天数（>0）。
	MaxAgeDays int `json:"maxAgeDays"`
	// MaxTotalGiB 是全部附件密文总量上限，单位 GiB。
	MaxTotalGiB int64 `json:"maxTotalGiB"`
	// CleanupIntervalMinutes 是后台清理间隔，单位分钟。
	CleanupIntervalMinutes int `json:"cleanupIntervalMinutes"`
	// MaxTotalBytes 是 MaxTotalGiB 换算出的字节数（只读，不参与写入）。
	MaxTotalBytes int64 `json:"maxTotalBytes"`
}

// TicketAttachmentRetentionConfig 是「已换算成字节与 Duration」的生效形态。
//
// 存在意义：按大小清理的用例需要 KiB 级阈值（真造 20 GiB 密文不现实），
// 所以清理入口直接吃字节，换算只发生一次。
type TicketAttachmentRetentionConfig struct {
	MaxAgeDays      int
	MaxTotalBytes   int64
	CleanupInterval time.Duration
}

// Config 把设置形态换算成生效形态（唯一的 GiB → 字节换算点）。
func (s TicketAttachmentRetentionSettings) Config() TicketAttachmentRetentionConfig {
	return TicketAttachmentRetentionConfig{
		MaxAgeDays:      s.MaxAgeDays,
		MaxTotalBytes:   s.MaxTotalGiB * ticketAttachmentRetentionGiB,
		CleanupInterval: time.Duration(s.CleanupIntervalMinutes) * time.Minute,
	}
}

// TicketAttachmentRetentionResult 汇总一次清理的产出。
//
// 三个计数分开报而不是一个「清理了 N 张」，因为「按时间」与「按大小」是两条
// 独立的策略：只有分开才看得出「是谁删的」，也才能让判据 ③（顺序确定）可验证。
type TicketAttachmentRetentionResult struct {
	// AgePurged 是按时间策略清掉的附件行数。
	AgePurged int64 `json:"agePurged"`
	// SizePurged 是**按时间清理之后**仍超总量、再按大小清掉的附件行数。
	SizePurged int64 `json:"sizePurged"`
	// BlobsPurged 是本轮顺带回收的 blob 字节份数（延迟删除的产出）。
	BlobsPurged int64 `json:"blobsPurged"`
	// LiveBytesAfter 是本轮结束时的存留密文总量（字节），供管理页显示收敛结果。
	LiveBytesAfter int64 `json:"liveBytesAfter"`
}

// GetTicketAttachmentRetentionSettingsWithDB 读取保留策略配置。
//
// 缺记录、缺字段、解析失败、越界一律回落默认值（180 天 / 20 GiB / 60 分钟）。
// 回落而不是返回 0：0 在这里是「非法」而**不是**「不限制」——把读失败当成 0
// 会让按大小清理退化成「删光全部附件」。
func GetTicketAttachmentRetentionSettingsWithDB(db *gorm.DB) TicketAttachmentRetentionSettings {
	settings := TicketAttachmentRetentionSettings{
		MaxAgeDays:             DefaultTicketAttachmentRetentionMaxAgeDays,
		MaxTotalGiB:            DefaultTicketAttachmentRetentionMaxTotalGiB,
		CleanupIntervalMinutes: DefaultTicketAttachmentRetentionCleanupIntervalMinutes,
	}
	if db != nil {
		settings.MaxAgeDays = int(getTicketAttachmentRetentionInt64Setting(
			db, TicketAttachmentRetentionMaxAgeDaysSettingKey,
			DefaultTicketAttachmentRetentionMaxAgeDays, MaxTicketAttachmentRetentionAgeDays))
		settings.MaxTotalGiB = getTicketAttachmentRetentionInt64Setting(
			db, TicketAttachmentRetentionMaxTotalGiBSettingKey,
			DefaultTicketAttachmentRetentionMaxTotalGiB, MaxTicketAttachmentRetentionTotalGiB)
		settings.CleanupIntervalMinutes = int(getTicketAttachmentRetentionInt64Setting(
			db, TicketAttachmentRetentionCleanupIntervalMinutesSettingKey,
			DefaultTicketAttachmentRetentionCleanupIntervalMinutes, MaxTicketAttachmentRetentionCleanupInterval))
	}
	settings.MaxTotalBytes = settings.MaxTotalGiB * ticketAttachmentRetentionGiB
	return settings
}

// ResolveTicketAttachmentRetentionConfigWithDB 是读取 + 换算的组合入口。
func ResolveTicketAttachmentRetentionConfigWithDB(db *gorm.DB) TicketAttachmentRetentionConfig {
	return GetTicketAttachmentRetentionSettingsWithDB(db).Config()
}

// getTicketAttachmentRetentionInt64Setting 读一个正整数设置，越界或解析失败回落默认值。
func getTicketAttachmentRetentionInt64Setting(db *gorm.DB, key string, defaultValue int64, maxValue int64) int64 {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.FormatInt(defaultValue, 10)))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > maxValue {
		return defaultValue
	}
	return value
}

// ticketAttachmentPurgedAtReady 判断 ticket_attachments.purged_at 列是否已就绪。
//
// 迁移还没跑时**什么都不做**而不是报错：清理是后台任务，缺列时它唯一的正确行为
// 是「这一轮跳过」，把「迁移没跑」升级成任务失败只会刷日志。
// 注意不能退回用「ciphertext 为空」当已清理判据 —— 那会把「加密事务崩在中间的
// 残缺行」与「按策略清理过的行」混为一谈，前端就无法给出不同提示。
func ticketAttachmentPurgedAtReady(db *gorm.DB) bool {
	return db != nil && db.Migrator().HasColumn(entity.TableNameTicketAttachment, "purged_at")
}

// PruneTicketAttachmentsByAge 按创建时间清理附件密文。
//
// 判据严格是「purged_at IS NULL **且** created_at 早于 now-maxAgeDays」：
// 已清理的行不重复处理（幂等），未到期的行一张都不碰。
//
// maxAgeDays <= 0 表示「不按时间清理」，直接返回 0。这里**不用 0 表示不限**，
// 因为 0 天在业务上等于「全部清掉」，把两种极端语义压在同一个值上迟早出事。
//
// 返回本轮实际清理的附件行数。分批提交，每批是独立短事务。
func PruneTicketAttachmentsByAge(db *gorm.DB, now time.Time, maxAgeDays int) (int64, error) {
	if db == nil {
		return 0, ErrTicketAttachmentRetentionNoDatabase
	}
	if maxAgeDays <= 0 {
		return 0, nil
	}
	if !ticketAttachmentPurgedAtReady(db) {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-time.Duration(maxAgeDays) * 24 * time.Hour)

	var total int64
	for i := 0; i < ticketAttachmentRetentionMaxBatches; i++ {
		ids, err := selectTicketAttachmentRetentionCandidates(db,
			"purged_at IS NULL AND created_at < ? AND COALESCE(length(ciphertext), 0) > 0", cutoff)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		purged, _, err := pruneTicketAttachmentIDs(db, ids, now, 0)
		total += purged
		if err != nil {
			return total, err
		}
		if purged == 0 {
			// 候选非空但一行都没清掉（被并发清理掉了）：避免空转。
			break
		}
	}
	if total > 0 {
		logger.SysLog(fmt.Sprintf(
			"工单图片按时间清理：清理 %d 张图片密文（保留 %d 天，截止时间 %s，工单与工单消息不受影响）",
			total, maxAgeDays, cutoff.Format(time.RFC3339)))
	}
	return total, nil
}

// PruneTicketAttachmentsBySize 按存留密文总量清理附件，**从最老的删起**。
//
// maxTotalBytes <= 0 表示「不按大小清理」，直接返回 0（与 maxAgeDays 同口径：
// 不用 0 表示「不限」，0 字节上限在业务上等于删光）。
//
// # 为什么顺序必须是 created_at ASC, id ASC
//
// 保留策略的意图是「留下近期的、清掉陈年的」。若改成随机或按 id 正序
// （id 正序≈上传顺序，看似相同，但迁移/导入过的库 id 与 created_at 不同序），
// 会出现「刚上传的图被删、三年前的老图留下」这种完全违背意图的结果，
// 而且同一份数据两次运行产出不同结果 —— 清理结果不可复现，无法验收。
//
// # 为什么按 attachment 行的 byte_size 统计，而不是 blobs 表的实际字节
//
// blob 是跨附件去重的，一行字节可能被 100 个附件行引用。若按 blob 字节统计，
// 「清掉 1 个附件行」通常**不会**让总量下降（blob 仍被别的行引用），于是循环
// 只能一直删到最后一根引用才收敛，很容易一次删掉整个工单的图片。
// 按附件行统计让「删一行、总量减一行」的因果是确定的、可解释的，
// 也与「哪张图该被清理」这个决策单元一致。
// 字节本体的真正回收仍由 blobs 表的延迟删除负责（本文件只负责把 ref_count 收敛到 0）。
func PruneTicketAttachmentsBySize(db *gorm.DB, now time.Time, maxTotalBytes int64) (int64, error) {
	if db == nil {
		return 0, ErrTicketAttachmentRetentionNoDatabase
	}
	if maxTotalBytes <= 0 {
		return 0, nil
	}
	if !ticketAttachmentPurgedAtReady(db) {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	total, err := ticketAttachmentLiveBytesWithDB(db)
	if err != nil {
		return 0, err
	}
	var purgedTotal int64
	for i := 0; i < ticketAttachmentRetentionMaxBatches && total > maxTotalBytes; i++ {
		ids, err := selectTicketAttachmentRetentionCandidates(db,
			"purged_at IS NULL AND COALESCE(length(ciphertext), 0) > 0")
		if err != nil {
			return purgedTotal, err
		}
		if len(ids) == 0 {
			break
		}
		// 预算 = 需要释放的字节数。传 0（不设预算）会让一批 200 行全被清掉，
		// 远超「删到回到上限以内」所需的量（实测过：3 张各 1000 字节、上限 1000
		// 时会清掉全部 3 张）。预算让「删够就停」成为确定行为。
		budget := total - maxTotalBytes
		if budget <= 0 {
			break
		}
		purged, freed, err := pruneTicketAttachmentIDs(db, ids, now, budget)
		purgedTotal += purged
		if err != nil {
			return purgedTotal, err
		}
		if purged == 0 || freed <= 0 {
			// 一行都没清掉，或者清掉的行 byte_size 合计为 0（异常数据）：
			// 继续循环只会空转，交给下一轮。
			break
		}
		total -= freed
	}
	if total < 0 {
		total = 0
	}
	if purgedTotal > 0 {
		logger.SysLog(fmt.Sprintf(
			"工单图片按大小清理：清理 %d 张图片密文（上限 %d 字节，剩余约 %d 字节，从最老的删起，工单与工单消息不受影响）",
			purgedTotal, maxTotalBytes, total))
	}
	return purgedTotal, nil
}

// RunTicketAttachmentRetentionWithDB 执行一轮完整清理。
//
// # 顺序是硬约定：先按时间、再按大小
//
// 不能是「两个 goroutine 谁先跑」，也不能是「按大小先跑」：
//
//   - 先按时间：陈年数据本来就该走，先清掉它能让按大小的压力最小；
//   - 再按大小：判据读的是**先按时间清理之后**的存留总量，于是「时间清理
//     已经把总量压到上限以下」时按大小一行都不删（这个可观测的差异，
//     就是判据 ③ 能验证顺序的原因 —— 反过来先跑大小，会看到 SizePurged>0
//     而 AgePurged=0）。
//
// 同一份数据两次运行得到同一个结果，所以整条链是可复现的。
func RunTicketAttachmentRetentionWithDB(db *gorm.DB, now time.Time) (TicketAttachmentRetentionResult, error) {
	var result TicketAttachmentRetentionResult
	if db == nil {
		return result, ErrTicketAttachmentRetentionNoDatabase
	}
	if now.IsZero() {
		now = time.Now()
	}
	cfg := ResolveTicketAttachmentRetentionConfigWithDB(db)

	agePurged, err := PruneTicketAttachmentsByAge(db, now, cfg.MaxAgeDays)
	result.AgePurged = agePurged
	if err != nil {
		return result, fmt.Errorf("按时间清理工单图片失败: %w", err)
	}
	// 注意：这里**重新**统计总量（PruneTicketAttachmentsBySize 内部会读），
	// 所以「先时间、后大小」的顺序是数据层面的，不是调用顺序层面的巧合。
	sizePurged, err := PruneTicketAttachmentsBySize(db, now, cfg.MaxTotalBytes)
	result.SizePurged = sizePurged
	if err != nil {
		return result, fmt.Errorf("按大小清理工单图片失败: %w", err)
	}
	// 字节本体的回收：ref_count 已经被上面两步收敛，归零的 blob 到期即可删除。
	// 不调用它，ref_count 归零只是「标记」，blobs 表里的密文永远不会消失。
	if ticketAttachmentBlobReady(db) {
		blobsPurged, purgeErr := RunTicketAttachmentBlobPurge(db, now, 0)
		result.BlobsPurged = blobsPurged
		if purgeErr != nil {
			// blob 回收失败不影响附件侧的清理结论，也不影响主服务。
			logger.SysWarn("工单图片 blob 延迟回收失败（不影响主服务，下个周期重试）：" + purgeErr.Error())
		}
	}
	if live, liveErr := ticketAttachmentLiveBytesWithDB(db); liveErr == nil {
		result.LiveBytesAfter = live
	}
	return result, nil
}

// StartTicketAttachmentRetentionCleanup 起一个独立的清理 goroutine，返回停止函数。
//
// interval > 0：固定间隔（调用方显式指定，测试用）。
// interval <= 0：**每轮重新从 system_settings 解析**，于是管理员改「清理间隔」
// 后不需要重启进程就能生效。为此循环用 time.Timer 而不是 NewTicker：
// ticker 的周期在创建时就固定了，改配置只能重启（与 StartLogRetentionCleanup 同理）。
//
// 与日志清理的一处刻意差异：本循环**启动后先立即跑一轮**再进入定时。
// 理由是图片清理由「超过 180 天」这种长效判据驱动，服务重启往往发生在
// 长时间停机之后（正是积压最多的时刻），先等一个周期（默认 60 分钟）再清理
// 会让积压在重启后继续存在一小时。立即跑一轮也让「配置真的被读到」可被验证，
// 而不是只能靠等待。
//
// 独立性：清理跑在自己的 goroutine 里，失败只记 logger.SysWarn，绝不影响主服务 ——
// 清理失败的后果是「图片占更多空间」，而把服务搞挂是更大的事故。
func StartTicketAttachmentRetentionCleanup(db *gorm.DB, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if db == nil {
			// 没有数据库时直接返回一个「停不掉也不干活」的停函数没有意义，
			// 这里保留 goroutine 退出，让调用方的 stop() 立即返回。
			return
		}
		if _, err := RunTicketAttachmentRetentionWithDB(db, time.Now()); err != nil {
			logger.SysWarn("工单图片保留清理任务失败（不影响主服务，下个周期重试）：" + err.Error())
		}
		for {
			timer := time.NewTimer(ticketAttachmentRetentionWaitResolver(db, interval))
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			if _, err := RunTicketAttachmentRetentionWithDB(db, time.Now()); err != nil {
				logger.SysWarn("工单图片保留清理任务失败（不影响主服务，下个周期重试）：" + err.Error())
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

// ticketAttachmentRetentionWaitResolver 是「下一轮等多久」的实现，供测试替换。
//
// # 为什么需要一个可替换的变量（不是为了让测试好写，而是别无他法）
//
// 「interval<=0 时每轮重读配置」这条语义的可观测周期最短是**1 分钟**（配置粒度
// 是分钟）。用例不可能等两轮，于是只能靠「解析器被调用了多少次」来判断循环
// 是否每轮重算。若循环在自己的体内直接调 Resolve...，测试就只能等真实周期；
// 若把读取提到循环外，一个只断言「解析器每次调用都读库」的用例**仍然全绿** ——
// 那正是「实现对了、接线错了」的假钉子形态（本仓已记录过多次）。
//
// 因此把它抽成包内变量，让用例可以替换成 1ms 的桩并数调用次数。
// 生产路径永远指向 ticketAttachmentRetentionNextWait，没有任何代码修改它。
var ticketAttachmentRetentionWaitResolver = ticketAttachmentRetentionNextWait

// ticketAttachmentRetentionNextWait 算出下一轮要等多久。
//
// 这是「interval<=0 每轮重读配置」这条语义的**唯一实现点**，抽出来是为了让它
// 可以被直接验证：只靠「跑一轮 60 分钟的循环」验证成本太高，而如果这条语义
// 藏在 goroutine 里，谁都无法证明它真的每轮重读（改成读一次固定值同样能跑）。
//
// 返回前做了两道兜底：配置解析出 <=0 时回落默认 60 分钟，避免 time.NewTimer(0)
// 变成忙循环。
func ticketAttachmentRetentionNextWait(db *gorm.DB, interval time.Duration) time.Duration {
	wait := interval
	if wait <= 0 {
		wait = ResolveTicketAttachmentRetentionConfigWithDB(db).CleanupInterval
	}
	if wait <= 0 {
		wait = time.Duration(DefaultTicketAttachmentRetentionCleanupIntervalMinutes) * time.Minute
	}
	return wait
}

// selectTicketAttachmentRetentionCandidates 取一批待清理的附件主键。
//
// 排序是**最老的先删**（created_at ASC, id ASC）：
//
//   - created_at 是清理年龄判据本身，第一排序键是它才可能选出「最老的」；
//   - id 只是**确定性**的第二排序键：同一秒内创建的多行（批量导入、脚本造数）
//     若没有 tiebreak，数据库返回顺序不保证稳定，两次运行就可能清掉不同行。
//
// 这里只取主键，不取密文：一次按大小清理最多涉及几千行，每行密文可达 20 MB，
// 把整行读出来会在内存里堆出几百 MB 到几 GB。密文是否还在用 SQL 里的
// length() 判断（见 pruneTicketAttachmentIDs）。
func selectTicketAttachmentRetentionCandidates(db *gorm.DB, where string, args ...any) ([]int64, error) {
	var ids []int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachment{}).
		Where(where, args...).
		Order("created_at ASC, id ASC").
		Limit(TicketAttachmentRetentionBatchSize).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// pruneTicketAttachmentIDs 在一个事务里清理指定附件行，返回清理行数与释放的字节数。
//
// freeBudgetBytes 是「本批最多释放多少字节」的预算：
//
//   - 0 表示不设预算（按时间清理、按大小清理的最后一轮之外的场合）；
//   - >0 时达到预算就**立刻停止**（按大小清理用它实现「删够就停」）。
//
// # 为什么必须有这个预算（实测踩过的错）
//
// 按大小清理的正确语义是「删到总量回到上限以内为止」。若只是「选一批候选 →
// 全删 → 重算总量」，一批 200 行会把远超所需的行删掉：实测种子为
// 「3 张各 1000 字节、上限 1000」时，第一批就把 3 张全清了（应当只清 2 张）。
// 候选集是**按最老优先排好的序列**，但「排好序」不等于「该全部删掉」——
// 判断要逐行做，删够为止。
//
// # 单事务的理由
//
// 「置空密文」与「收敛 blob 的 ref_count」必须原子：中间崩溃会留下
// 「密文没了但 ref_count 还记着这次引用」的永久泄漏（blob 再也归不了零）。
//
// # 为什么先读后写、并且写的时候再带一次条件
//
// 候选行是在事务外选出来的，选完之后可能有并发（另一次清理、管理员手删）。
// 所以真正的写入条件是 `id = ? AND purged_at IS NULL`，RowsAffected=0 就跳过 ——
// 不能只按主键更新，否则会把别人已经处理过的行重复计数。
func pruneTicketAttachmentIDs(db *gorm.DB, ids []int64, now time.Time, freeBudgetBytes int64) (purged int64, freedBytes int64, err error) {
	if db == nil || len(ids) == 0 {
		return 0, 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	blobLinkReady := ticketAttachmentBlobLinkReady(db)
	blobReady := ticketAttachmentBlobReady(db)

	// 只读清理决策需要的列。**绝不 SELECT ciphertext**：一批 200 行的密文
	// 合计可达数 GB，那会直接把内存打爆（见本函数头部注释）。
	type candidateRow struct {
		ID         int64      `gorm:"column:id"`
		ByteSize   int64      `gorm:"column:byte_size"`
		PurgedAt   *time.Time `gorm:"column:purged_at"`
		PayloadLen int64      `gorm:"column:payload_len"`
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			if freeBudgetBytes > 0 && freedBytes >= freeBudgetBytes {
				// 预算已用完：剩下的候选留给下一轮（它们仍然是「最老的」，
				// 下一轮如果还超限会继续从它们开始，顺序不变）。
				return nil
			}
			var row candidateRow
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Table(entity.TableNameTicketAttachment).
				Select("id, byte_size, purged_at, COALESCE(length(ciphertext), 0) AS payload_len").
				Where("id = ?", id).
				Take(&row).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if row.PurgedAt != nil || row.PayloadLen <= 0 {
				// 已经清过（幂等），或者本来就没有密文（残缺行）：都不动。
				continue
			}
			var linkedSHA string
			if blobLinkReady {
				var link entity.TicketAttachmentBlobLink
				if err := tx.Session(&gorm.Session{NewDB: true}).
					Where("id = ?", id).Take(&link).Error; err != nil {
					if !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
				} else {
					linkedSHA = NormalizeTicketAttachmentBlobSHA256(link.BlobSHA256)
				}
			}
			// 只清密文：byte_size / media_type / sha256 / message_id / ticket_id 全部保留，
			// 前端据此显示「图片已按保留策略清理」，历史消息也不会出现空洞。
			result := tx.Session(&gorm.Session{NewDB: true}).
				Model(&entity.TicketAttachment{}).
				Where("id = ? AND purged_at IS NULL", id).
				Updates(map[string]any{
					"ciphertext": nil,
					"nonce":      nil,
					"purged_at":  now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			purged++
			freedBytes += row.ByteSize

			// ② 收敛 ref_count：先摘掉指针，再按事实值重算。
			//
			// 顺序不能反：ref_count 的事实来源是 `COUNT(*) WHERE blob_sha256 = ?`
			// （TicketAttachmentBlobRefCountWithDB），指针还在时重算得到的仍是旧值，
			// 等于什么都没做。
			//
			// 用 Release（重算）而不是 Decrease（减一）：增量维护会随崩溃/并发漂移，
			// 而漂移的代价是 24 小时延迟删除误删仍在被引用的 blob（图片永久不显示）。
			if linkedSHA != "" && blobReady {
				if err := tx.Session(&gorm.Session{NewDB: true}).
					Model(&entity.TicketAttachment{}).
					Where("id = ?", id).
					UpdateColumn("blob_sha256", "").Error; err != nil {
					return err
				}
				if _, err := ReleaseTicketAttachmentBlobInTx(tx, linkedSHA); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return purged, freedBytes, err
	}
	return purged, freedBytes, nil
}

// ticketAttachmentLiveBytesWithDB 统计**仍占着存储**的附件密文总量（字节）。
//
// # 两个排除项，各自防止一种失控
//
//   - `purged_at IS NULL`：清理过的行虽然保留 byte_size（给前端显示占位），
//     但字节已经不在库里。若把它计入总量，按大小清理会被「已经删掉的数据」
//     永久占满配额，从此再也清不到真正占空间的那些行。
//   - `COALESCE(length(ciphertext), 0) > 0`：**残缺行**（加密事务崩在中间，
//     nonce/ciphertext 为空但 byte_size 有值）同样不占存储。把它计入会制造
//     「永远超限、但候选集里一行都清不掉」的死循环 —— 因为这类行既不该被
//     标记 purged（它本来就没清理过内容），也无法通过清理释放任何字节。
//
// 换句话说：这个函数的口径必须与**清理候选集的口径完全一致**，否则
// 「总量 > 上限」与「有可清理的行」两个判据会互相矛盾。这条一致性是本文件
// 里最容易漂移的地方（候选集的 SQL 与本函数的 SQL 必须同进同退）。
func ticketAttachmentLiveBytesWithDB(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, ErrTicketAttachmentRetentionNoDatabase
	}
	if !ticketAttachmentPurgedAtReady(db) {
		return 0, nil
	}
	var row struct {
		Total int64 `gorm:"column:total"`
	}
	if err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.TicketAttachment{}).
		Where("purged_at IS NULL AND COALESCE(length(ciphertext), 0) > 0").
		Select("COALESCE(SUM(byte_size), 0) AS total").
		Scan(&row).Error; err != nil {
		return 0, err
	}
	return row.Total, nil
}
