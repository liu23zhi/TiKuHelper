package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// TicketAttachmentBlobPurgeBatchSize 是延迟删除单次最多删的行数。
	//
	// 分批而不是一条 DELETE 全扫：归零的 blob 可能一次积压几千行（一次批量删图/清库），
	// 单条大 DELETE 会长时间持锁。调用方按需循环，每批一个短事务。
	TicketAttachmentBlobPurgeBatchSize = 200
)

var (
	// ErrTicketAttachmentBlobInvalid 表示 blob 的结构不完整（缺密文/nonce/sha256）。
	ErrTicketAttachmentBlobInvalid = errors.New("工单图片 blob 数据不完整")
	// ErrTicketAttachmentBlobNotFound 表示该 sha256 在 blobs 表里不存在。
	ErrTicketAttachmentBlobNotFound = errors.New("工单图片 blob 不存在")
)

// TicketAttachmentBlobAAD 把 blob 身份绑进密文（task_doc/112 §1.6 方案 A）。
//
// 与 ticketAttachmentAAD 的关键差异：**不含自增 id**。blob 表没有自增 id
// （SHA256 即主键），所以换用内容指纹做绑定。保护强度不变：
// 把 A 的密文塞进 B 的 blob 行（不同 sha256）→ GCM 校验失败。
//
// ⚠️ 域前缀与 ticketAttachmentAAD 的 `tikuhelper-ticket-attachment-v1:` 不同，
// 两套密文因此无法互换解密 —— 这是有意的隔离，不是笔误。
func TicketAttachmentBlobAAD(sha256Hex string) []byte {
	return []byte("tikuhelper-ticket-attachment-blob-v1:" + NormalizeTicketAttachmentBlobSHA256(sha256Hex))
}

// NormalizeTicketAttachmentBlobSHA256 把 sha256 归一成小写去空白。
//
// 归一化必须在校验、写库、比较三处**都**走同一个函数：sha256 是主键，
// 大小写不同就是两个不同的 blob，会让同一张图被存两份且引用计数各算一半。
func NormalizeTicketAttachmentBlobSHA256(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ticketAttachmentBlobReady 判断 blobs 表是否已就绪。
// 迁移还没跑时所有读写路径都按「没有 blob」处理，让老库照旧可用。
func ticketAttachmentBlobReady(db *gorm.DB) bool {
	return db != nil && db.Migrator().HasTable(entity.TableNameTicketAttachmentBlob)
}

// ticketAttachmentBlobLinkReady 判断 ticket_attachments.blob_sha256 列是否已补上。
func ticketAttachmentBlobLinkReady(db *gorm.DB) bool {
	return db != nil && db.Migrator().HasColumn(entity.TableNameTicketAttachment, "blob_sha256")
}

// TicketAttachmentBlobRefCountWithDB 返回「当前有多少个附件行指向这份 blob」。
//
// 这是引用计数的**唯一事实来源**。ref_count 列只是它的缓存 —— 任何
// 「先加一再减一」的增量维护都可能因崩溃/并发偏离事实，而偏离的代价是
// 24 小时延迟删除会删掉仍在被引用的 blob（用户看到图片永久不显示）。
func TicketAttachmentBlobRefCountWithDB(db *gorm.DB, sha256Hex string) (int32, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, ErrTicketAttachmentBlobInvalid
	}
	if !ticketAttachmentBlobLinkReady(db) {
		// 列还没补（迁移尚未跑）：等价于「没有任何行指向它」。
		return 0, nil
	}
	var count int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachment{}).
		Where("blob_sha256 = ?", sha).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int32(count), nil
}

// RecomputeTicketAttachmentBlobRefCountInTx 在同一事务内重算 ref_count 并写回。
//
// 回填、释放、以及任何「计数可能不准」的场合都必须走这里，不要用增量猜测。
// 返回重算后的值（blob 不存在时返回 0 且不写）。
func RecomputeTicketAttachmentBlobRefCountInTx(tx *gorm.DB, sha256Hex string) (int32, error) {
	if tx == nil {
		return 0, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, ErrTicketAttachmentBlobInvalid
	}
	count, err := TicketAttachmentBlobRefCountWithDB(tx, sha)
	if err != nil {
		return 0, err
	}
	var blob entity.TicketAttachmentBlob
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if blob.RefCount == count {
		// 已经是事实值：不写库，避免无谓地刷新 updated_at
		// （updated_at 是延迟删除的判龄依据，不准乱动）。
		return count, nil
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
		Where("sha256 = ?", sha).
		UpdateColumn("ref_count", count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// StoreTicketAttachmentBlobInTx 是**去重写入**：同一份字节只落一行。
//
// 语义（112 §1.3 ③）：
//   - 不存在该 sha256 → 写入密文；
//   - 已存在该 sha256 → **不写密文**，返回 created=false。
//
// 幂等靠主键：OnConflict DoNothing。并发插入同一张图时数据库挡住第二个，
// 不需要「先查后插」的竞态窗口（那在 SQLite 上还会踩到表锁）。
//
// 返回 created 让调用方区分「省下了 N MB」与「新写了一份」—— 这个区分必须进日志，
// 否则线上永远无法确认去重是否真的生效。
func StoreTicketAttachmentBlobInTx(tx *gorm.DB, sha256Hex, mediaType string, byteSize int64, nonce, ciphertext []byte) (entity.TicketAttachmentBlob, bool, error) {
	if tx == nil {
		return entity.TicketAttachmentBlob{}, false, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" || len(nonce) == 0 || len(ciphertext) == 0 || byteSize <= 0 {
		return entity.TicketAttachmentBlob{}, false, ErrTicketAttachmentBlobInvalid
	}
	blob := entity.TicketAttachmentBlob{
		SHA256:     sha,
		ByteSize:   byteSize,
		MediaType:  mediaType,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		ScanState:  entity.TicketAttachmentScanPending,
	}
	result := tx.Session(&gorm.Session{NewDB: true}).
		Clauses(clause.OnConflict{DoNothing: true}).Create(&blob)
	if result.Error != nil {
		return entity.TicketAttachmentBlob{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		// 已存在：密文一个字都不写（这就是去重省下的空间）。
		return entity.TicketAttachmentBlob{SHA256: sha}, false, nil
	}
	return blob, true, nil
}

// LinkTicketAttachmentBlobInTx 把一份 blob 挂到一个附件行上，并重算引用计数。
//
// 为什么在**这里**重算而不是让调用方再加一：ref_count 必须恒等于引用行数
// （121 §4.4 第 1 点）。把「写指向」与「重算计数」放进同一个函数的同一事务内，
// 调用方就没有漏掉重算的机会。
func LinkTicketAttachmentBlobInTx(tx *gorm.DB, attachmentID int64, sha256Hex string) (int32, error) {
	if tx == nil {
		return 0, errors.New("数据库未初始化")
	}
	if attachmentID <= 0 {
		return 0, ErrTicketAttachmentNotFound
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, ErrTicketAttachmentBlobInvalid
	}
	if !ticketAttachmentBlobLinkReady(tx) {
		return 0, fmt.Errorf("%s 缺少 blob_sha256 列，无法建立 blob 指向", entity.TableNameTicketAttachment)
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachment{}).
		Where("id = ?", attachmentID).
		UpdateColumn("blob_sha256", sha).Error; err != nil {
		return 0, err
	}
	return RecomputeTicketAttachmentBlobRefCountInTx(tx, sha)
}

// DecreaseTicketAttachmentBlobRefCountInTx 把引用计数减一，**下限 0**。
//
// 只在「同一个事务里已经删掉了引用该 blob 的附件行」时使用。
// 归零时**刷新 updated_at**：延迟删除按 `ref_count<=0 AND updated_at < now-24h`
// 判龄，不刷新就会用旧时间戳立刻误删（刚删完又传回来的图会反复写盘）。
func DecreaseTicketAttachmentBlobRefCountInTx(tx *gorm.DB, sha256Hex string) (int32, error) {
	if tx == nil {
		return 0, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, ErrTicketAttachmentBlobInvalid
	}
	// ref_count > 0 是「下限 0」的实现：已归零时 RowsAffected=0，不会减成负数。
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
		Where("sha256 = ? AND ref_count > 0", sha).
		UpdateColumn("ref_count", gorm.Expr("ref_count - 1")).Error; err != nil {
		return 0, err
	}
	var blob entity.TicketAttachmentBlob
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if blob.RefCount <= 0 {
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
			Where("sha256 = ?", sha).
			UpdateColumn("updated_at", time.Now()).Error; err != nil {
			return 0, err
		}
	}
	return blob.RefCount, nil
}

// ReleaseTicketAttachmentBlobInTx 是删除附件后的**推荐入口**：核对事实并收敛。
//
// 与 Decrease 的分工：Decrease 是纯增量（快，但会漂移）；Release 用真值覆盖。
// 漂移一旦被 24 小时延迟删除撞上就是数据丢失，代价远大于一次 COUNT，
// 所以正常删除路径应当优先用 Release。
func ReleaseTicketAttachmentBlobInTx(tx *gorm.DB, sha256Hex string) (int32, error) {
	if tx == nil {
		return 0, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, ErrTicketAttachmentBlobInvalid
	}
	count, err := TicketAttachmentBlobRefCountWithDB(tx, sha)
	if err != nil {
		return 0, err
	}
	var blob entity.TicketAttachmentBlob
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// blob 行已经不存在：没什么可释放的。
			return 0, nil
		}
		return 0, err
	}
	updates := map[string]any{}
	if blob.RefCount != count {
		updates["ref_count"] = count
	}
	if count <= 0 {
		// 归零（含「本来就是 0」）：刷新判龄起点。
		updates["updated_at"] = time.Now()
	}
	if len(updates) == 0 {
		return count, nil
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
		Where("sha256 = ?", sha).
		Updates(updates).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// StoreTicketAttachmentBlobWithDB 是去重写入的**独立入口**（不在既有上传事务里时用）。
//
// 现网上传路径不改（旧列一个版本内不删），本入口供后续切换到 blobs 表时使用，
// 也让去重逻辑可被单测直接覆盖。
func StoreTicketAttachmentBlobWithDB(db *gorm.DB, sha256Hex, mediaType string, plain []byte) (entity.TicketAttachmentBlob, bool, error) {
	if db == nil {
		return entity.TicketAttachmentBlob{}, false, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" || len(plain) == 0 {
		return entity.TicketAttachmentBlob{}, false, ErrTicketAttachmentBlobInvalid
	}
	nonce, ciphertext, err := sealTicketAttachmentBlob(plain, sha)
	if err != nil {
		return entity.TicketAttachmentBlob{}, false, err
	}
	var blob entity.TicketAttachmentBlob
	var created bool
	err = db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		blob, created, txErr = StoreTicketAttachmentBlobInTx(tx, sha, mediaType, int64(len(plain)), nonce, ciphertext)
		return txErr
	})
	if err != nil {
		return entity.TicketAttachmentBlob{}, false, err
	}
	return blob, created, nil
}

// PurgeExpiredTicketAttachmentBlobs 执行延迟删除（112 §1.5 例 5 / 117 §4.1）。
//
// 判据严格是「ref_count <= 0 **且** updated_at 早于 now-24h」：
// 24 小时是缓冲期，避免「删了又传」造成同一份大密文被反复写盘。
//
// ⚠️ 判据依赖 ref_count 准确。这是本表最容易出事的耦合点：
// ref_count 若算少了，这里就会删掉还在被引用的 blob。所以回填/释放路径都强制
// 通过重算收敛，而不是纯粹依赖增量。
//
// 分批删除，返回本批实际删除行数；调用方按需循环，每批是独立的短事务。
func PurgeExpiredTicketAttachmentBlobs(db *gorm.DB, now time.Time, limit int) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	if limit <= 0 || limit > TicketAttachmentBlobPurgeBatchSize {
		limit = TicketAttachmentBlobPurgeBatchSize
	}
	if now.IsZero() {
		now = time.Now()
	}
	if !ticketAttachmentBlobReady(db) {
		return 0, nil
	}
	cutoff := now.Add(-entity.TicketAttachmentBlobDedupGrace)
	// 先取候选主键再按主键删：既便于把「删了哪些」记进日志，
	// 也让删除语句能二次核对 ref_count。
	var shas []string
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
		Where("ref_count <= 0 AND updated_at < ?", cutoff).
		Order("updated_at ASC").
		Limit(limit).
		Pluck("sha256", &shas).Error; err != nil {
		return 0, err
	}
	if len(shas) == 0 {
		return 0, nil
	}
	// 删除条件里**再次**带上 ref_count <= 0 与时间：候选与删除之间可能有新上传
	// 把计数加回去，不能只按主键删。
	result := db.Session(&gorm.Session{NewDB: true}).
		Where("sha256 IN ? AND ref_count <= 0 AND updated_at < ?", shas, cutoff).
		Delete(&entity.TicketAttachmentBlob{})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected > 0 {
		logger.SysLog(fmt.Sprintf("工单图片 blob 延迟清理：删除 %d 份不再被引用的图片（缓冲期 %s）",
			result.RowsAffected, entity.TicketAttachmentBlobDedupGrace))
	}
	return result.RowsAffected, nil
}

// RunTicketAttachmentBlobPurge 循环调用分批删除，直到没有可删的。
//
// maxBatches 防止某次异常状态下无限循环（例如删除一直 RowsAffected=0 但候选非空）。
func RunTicketAttachmentBlobPurge(db *gorm.DB, now time.Time, maxBatches int) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	if maxBatches <= 0 {
		maxBatches = 50
	}
	var total int64
	for i := 0; i < maxBatches; i++ {
		deleted, err := PurgeExpiredTicketAttachmentBlobs(db, now, TicketAttachmentBlobPurgeBatchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted == 0 {
			break
		}
	}
	return total, nil
}

// sealTicketAttachmentBlob 用 blob AAD 加密一份明文。
//
// 与附件侧 ticketAttachmentAAD 的差别：那边 AAD 含自增 id，必须「先插占位行再 UPDATE 密文」；
// 这边 sha256 在加密前已知，因此**一次插入即完整**，不存在残缺中间态。
func sealTicketAttachmentBlob(plain []byte, sha256Hex string) ([]byte, []byte, error) {
	aead, err := ticketAttachmentCipher()
	if err != nil {
		return nil, nil, err
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" || len(plain) == 0 {
		return nil, nil, ErrTicketAttachmentBlobInvalid
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plain, TicketAttachmentBlobAAD(sha)), nil
}

// openTicketAttachmentBlob 用 blob AAD 解密一份 blob 密文。
func openTicketAttachmentBlob(blob entity.TicketAttachmentBlob) ([]byte, error) {
	aead, err := ticketAttachmentCipher()
	if err != nil {
		return nil, err
	}
	if len(blob.Nonce) != aead.NonceSize() || len(blob.Ciphertext) == 0 {
		return nil, ErrTicketAttachmentBlobInvalid
	}
	plain, openErr := aead.Open(nil, blob.Nonce, blob.Ciphertext, TicketAttachmentBlobAAD(blob.SHA256))
	if openErr != nil {
		return nil, ErrTicketAttachmentBlobInvalid
	}
	return plain, nil
}

// ReadTicketAttachmentBlobWithDB 读出一份 blob 的明文。
//
// 与 ReadTicketAttachmentWithDB 的分工：本函数**不做权限判定**（它拿不到工单归属），
// 只负责「按 blobs 表解出字节」。权限与展示判定仍由附件层的既有入口负责
// （旧列一个版本内不删，因此现网读取路径不变）。
func ReadTicketAttachmentBlobWithDB(db *gorm.DB, sha256Hex string) (string, []byte, error) {
	if db == nil {
		return "", nil, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return "", nil, ErrTicketAttachmentBlobInvalid
	}
	if !ticketAttachmentBlobReady(db) {
		return "", nil, ErrTicketAttachmentBlobNotFound
	}
	var blob entity.TicketAttachmentBlob
	if err := db.Session(&gorm.Session{NewDB: true}).Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, ErrTicketAttachmentBlobNotFound
		}
		return "", nil, err
	}
	plain, err := openTicketAttachmentBlob(blob)
	if err != nil {
		return "", nil, err
	}
	return blob.MediaType, plain, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 工单图片缓存管理页的查询能力（T-12）
//
// 本段**只新增**，不改上面任何既有函数。分工：
//
//	ListTicketImageCacheWithDB    分页列表 + 四个必须显示的数字（总量/待清理/scан_state 分组/保留策略）
//	DeleteTicketImageCacheWithDB  单条删除，ref_count>0 必须拒绝
//	DeleteTicketImageCachesBatchWithDB 批量删除，同样遵守 ref_count 保护
//
// ⚠️ 清理逻辑不在本文件：按时间/按大小清理属于 T-11（ticket_attachment_retention.go）。
// 本文件只负责「读现状」与「删单条/批量」。
// ─────────────────────────────────────────────────────────────────────────────

// TicketImageCacheRefFilter 是列表页的引用计数筛选（按 ref_count 过滤）。
const (
	// TicketImageCacheRefAll 不按引用计数过滤。
	TicketImageCacheRefAll = ""
	// TicketImageCacheRefZero 只看 ref_count = 0 的待清理项。
	TicketImageCacheRefZero = "zero"
	// TicketImageCacheRefReferenced 只看 ref_count > 0 的仍在被引用的项。
	TicketImageCacheRefReferenced = "referenced"
)

// 保留策略的三个默认值（与 T-11 的 ticket_attachment_retention.go 保持一致）。
//
// ⚠️ 为什么要在这里重复一份：T-11 的 service.GetTicketAttachmentRetentionSettingsWithDB
// 尚未落地（本分支上 ticket_attachment_retention.go 不存在）。页面必须显示
// 「当前生效的三个保留策略值」，所以这里按**同一份键名与默认值**只读地读一遍。
// T-11 落地后应把 resolveTicketImageCacheRetention 替换成
// service.GetTicketAttachmentRetentionSettingsWithDB，并删掉本段的常量与解析函数。
//
// 常量名刻意加了 ticketImageCache 前缀而不是复用 T-11 的
// TicketAttachmentRetentionMaxAgeDaysSettingKey：本包同属 service，
// 名字撞车会让两个 PR 合并时直接编译失败。
const (
	ticketImageCacheRetentionMaxAgeDaysSettingKey                   = "ticket_attachment_retention_max_age_days"
	ticketImageCacheRetentionMaxTotalGiBSettingKey                  = "ticket_attachment_retention_max_total_gib"
	ticketImageCacheRetentionCleanupIntervalMinutesSettingKey       = "ticket_attachment_retention_cleanup_interval_minutes"
	defaultTicketImageCacheRetentionMaxAgeDays                int64 = 180
	defaultTicketImageCacheRetentionMaxTotalGiB               int64 = 20
	defaultTicketImageCacheRetentionCleanupIntervalMinutes    int64 = 60
	ticketImageCacheRetentionGiB                              int64 = 1 << 30
	// 上限只挡明显离谱的输入，与 T-11 的同名上限一致。
	maxTicketImageCacheRetentionAgeDays               int64 = 3650
	maxTicketImageCacheRetentionTotalGiB              int64 = 1024
	maxTicketImageCacheRetentionCleanupIntervalMinute int64 = 1440
)

// TicketImageCacheRetention 是页面显示的「当前生效的三个保留策略值」。
type TicketImageCacheRetention struct {
	// MaxAgeDays 是按时间清理的天数。
	MaxAgeDays int `json:"maxAgeDays"`
	// MaxTotalGiB 是总量上限，单位 GiB。
	MaxTotalGiB int64 `json:"maxTotalGiB"`
	// CleanupIntervalMinutes 是后台清理间隔，单位分钟。
	CleanupIntervalMinutes int `json:"cleanupIntervalMinutes"`
	// MaxTotalBytes 是 MaxTotalGiB 的字节形态（只读派生，供页面算占比）。
	MaxTotalBytes int64 `json:"maxTotalBytes"`
}

// TicketImageCacheScanCounts 是按 scan_state 分组的分项计数。
//
// 五个字段各自独立而不是一个 map：页面要固定显示 pending / approved /
// sensitive / failed 四项，用结构体让「漏掉某一项」在编译期就被发现。
// Scanning 是 pending 与结果之间的中间态（检测中的行），单列出来是为了让
// 四个固定项之和与总数不一致时能解释清楚差在哪。
type TicketImageCacheScanCounts struct {
	Pending   int64 `json:"pending"`
	Scanning  int64 `json:"scanning"`
	Approved  int64 `json:"approved"`
	Sensitive int64 `json:"sensitive"`
	Failed    int64 `json:"failed"`
	// Other 是**兜底**：库里出现未知 scan_state（历史数据、手工改库、未来新增状态）
	// 时必须被计数而不是被静默丢掉 —— 否则「分项之和 != 总数」会变成一个无法解释的缺口。
	Other int64 `json:"other"`
}

// Total 返回五项之和（含 Other）。
func (c TicketImageCacheScanCounts) Total() int64 {
	return c.Pending + c.Scanning + c.Approved + c.Sensitive + c.Failed + c.Other
}

// TicketImageCacheSummary 是列表页的汇总卡片。
//
// 全部字段都在**整个筛选范围**上统计，不随翻页变化 —— 只统计当前页会让
// 「总占用」在翻页时跳动，管理员无法用它判断该不该清理。
type TicketImageCacheSummary struct {
	// TotalCount 是筛选范围内的 blob 行数。
	TotalCount int64 `json:"totalCount"`
	// TotalBytes 是筛选范围内 byte_size 之和（**明文字节**，与 T-11 的
	// ticketAttachmentLiveBytes 同口径：判定与统计都用 byte_size，不含加密开销）。
	TotalBytes int64 `json:"totalBytes"`
	// ZeroRefCount 是 ref_count = 0 的待清理行数（延迟删除的候选集大小）。
	ZeroRefCount int64 `json:"zeroRefCount"`
	// ScanStates 是按 scan_state 分组的分项计数。
	ScanStates TicketImageCacheScanCounts `json:"scanStates"`
	// Retention 是当前生效的三个保留策略值。
	Retention TicketImageCacheRetention `json:"retention"`
}

// TicketImageCacheItem 是列表的一行。
//
// ⚠️ 刻意**不是** entity.TicketAttachmentBlob：那个实体带 Nonce / Ciphertext
// （json:"-" 只是挡住序列化，长度仍会进内存），也带 CheckError。
// 列表一次 20 行、每行密文可达 20 MB，把整行读出来会平白拉几百 MB 内存。
// 这里只 SELECT 展示需要的列，密文一列都不碰。
type TicketImageCacheItem struct {
	SHA256 string `json:"sha256"`
	// ByteSize 是明文字节数（同一份图去重后只有一行，这是它一次的占用）。
	ByteSize      int64      `json:"byteSize"`
	MediaType     string     `json:"mediaType"`
	RefCount      int32      `json:"refCount"`
	ScanState     string     `json:"scanState"`
	NSFWScore     *float64   `json:"nsfwScore,omitempty"`
	RiskLevel     string     `json:"riskLevel,omitempty"`
	Suggestion    string     `json:"suggestion,omitempty"`
	Label         string     `json:"label,omitempty"`
	CheckedAt     *time.Time `json:"checkedAt,omitempty"`
	AttemptCount  int        `json:"attemptCount"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	// Referenced 是派生字段：refCount > 0。页面据此直接禁用删除按钮，
	// 不必在前端重复「> 0」这个判据（两处判据必然漂移）。
	Referenced bool `json:"referenced"`
}

// TicketImageCacheListQuery 是列表筛选条件。
type TicketImageCacheListQuery struct {
	// MediaType 精确匹配（image/png、image/jpeg …）。
	MediaType string
	// ScanState 精确匹配（pending / scanning / approved / sensitive / failed）。
	ScanState string
	// RefFilter 取 TicketImageCacheRef* 之一。
	RefFilter string
	// Keyword 只做 sha256 前缀匹配：blob 的「内容」是密文，没有可全文搜索的文本列，
	// 而 sha256 是排查某张图时唯一可用的外部标识。
	Keyword string
}

// TicketImageCacheListResult 是分页结果。
type TicketImageCacheListResult struct {
	Items    []TicketImageCacheItem  `json:"items"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"pageSize"`
	Summary  TicketImageCacheSummary `json:"summary"`
}

// TicketImageCacheBlockedRef 是「这条 blob 删不掉，因为还在被引用」。
type TicketImageCacheBlockedRef struct {
	SHA256 string `json:"sha256"`
	// RefCount 是被多少个附件行（工单图片）引用。
	RefCount int32 `json:"refCount"`
	// Message 是给管理员看的人话，含具体数量（判据 ① 要求「说明被几张工单引用」）。
	Message string `json:"message"`
}

// errTicketImageCacheNoDatabase 与既有函数的「数据库未初始化」保持同一文案。
var errTicketImageCacheNoDatabase = errors.New("数据库未初始化")

// ticketImageCacheReady 判断 blobs 表是否已就绪。
//
// 未就绪时**所有查询返回空结果而不报错**（判据 ④：空库/未迁移时各项显示 0）：
// 管理页把一个「迁移没跑」的状态显示成红色报错，只会误导管理员去查业务问题。
func ticketImageCacheReady(db *gorm.DB) bool {
	return ticketAttachmentBlobReady(db)
}

// resolveTicketImageCacheRetention 读取三个保留策略值（缺记录/越界回落默认值）。
func resolveTicketImageCacheRetention(db *gorm.DB) TicketImageCacheRetention {
	settings := TicketImageCacheRetention{
		MaxAgeDays:             int(defaultTicketImageCacheRetentionMaxAgeDays),
		MaxTotalGiB:            defaultTicketImageCacheRetentionMaxTotalGiB,
		CleanupIntervalMinutes: int(defaultTicketImageCacheRetentionCleanupIntervalMinutes),
	}
	if db != nil {
		settings.MaxAgeDays = int(readTicketImageCacheRetentionInt64(
			db, ticketImageCacheRetentionMaxAgeDaysSettingKey,
			defaultTicketImageCacheRetentionMaxAgeDays, maxTicketImageCacheRetentionAgeDays))
		settings.MaxTotalGiB = readTicketImageCacheRetentionInt64(
			db, ticketImageCacheRetentionMaxTotalGiBSettingKey,
			defaultTicketImageCacheRetentionMaxTotalGiB, maxTicketImageCacheRetentionTotalGiB)
		settings.CleanupIntervalMinutes = int(readTicketImageCacheRetentionInt64(
			db, ticketImageCacheRetentionCleanupIntervalMinutesSettingKey,
			defaultTicketImageCacheRetentionCleanupIntervalMinutes,
			maxTicketImageCacheRetentionCleanupIntervalMinute))
	}
	settings.MaxTotalBytes = settings.MaxTotalGiB * ticketImageCacheRetentionGiB
	return settings
}

// readTicketImageCacheRetentionInt64 读一个正整数设置，越界或解析失败回落默认值。
//
// 回落而不是返回 0：0 在保留策略里是**非法值**而不是「不限制」，
// 把读失败当 0 会让页面显示「总量上限 0 字节」（看起来像「已经该删光」）。
func readTicketImageCacheRetentionInt64(db *gorm.DB, key string, defaultValue int64, maxValue int64) int64 {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.FormatInt(defaultValue, 10)))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > maxValue {
		return defaultValue
	}
	return value
}

// applyTicketImageCacheFilters 把筛选条件套到查询上。
func applyTicketImageCacheFilters(query *gorm.DB, filter TicketImageCacheListQuery) *gorm.DB {
	if mediaType := strings.TrimSpace(filter.MediaType); mediaType != "" {
		query = query.Where("media_type = ?", mediaType)
	}
	if scanState := strings.TrimSpace(filter.ScanState); scanState != "" {
		query = query.Where("scan_state = ?", scanState)
	}
	switch filter.RefFilter {
	case TicketImageCacheRefZero:
		query = query.Where("ref_count <= 0")
	case TicketImageCacheRefReferenced:
		query = query.Where("ref_count > 0")
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		// 前缀匹配而不是 %kw%：sha256 是定长十六进制串，LIKE 'abc%' 才能用上主键索引；
		// 两侧通配会让每次输入都全表扫描。
		query = query.Where("sha256 LIKE ?", NormalizeTicketAttachmentBlobSHA256(keyword)+"%")
	}
	return query
}

// ListTicketImageCacheWithDB 分页列出工单图片缓存。
//
// 返回的四组数字（对应页面四个必须显示的项，全部跨整个筛选范围）：
//
//	① TotalBytes            总占用字节（页面转人类可读）
//	② ZeroRefCount          ref_count = 0 的待清理数
//	③ ScanStates            按 scan_state 分组（pending/scanning/approved/sensitive/failed/other）
//	④ Retention             当前生效的三个保留策略值
func ListTicketImageCacheWithDB(db *gorm.DB, filter TicketImageCacheListQuery, page int, pageSize int) (TicketImageCacheListResult, error) {
	result := TicketImageCacheListResult{Items: []TicketImageCacheItem{}, Summary: TicketImageCacheSummary{}}
	if db == nil {
		return result, errTicketImageCacheNoDatabase
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	result.Page, result.PageSize = page, pageSize

	// 保留策略的读取与表是否存在无关：即使 blobs 表还没建，页面也要能显示配置值。
	result.Summary.Retention = resolveTicketImageCacheRetention(db)

	if !ticketImageCacheReady(db) {
		// 判据 ④：未就绪 = 空结果 + 全 0 + 无错误。
		return result, nil
	}

	base := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{})
	base = applyTicketImageCacheFilters(base, filter)
	if err := base.Count(&result.Total).Error; err != nil {
		return result, err
	}

	// 列表只 SELECT 展示列，**不碰 nonce / ciphertext / check_error**。
	rows := make([]TicketImageCacheItem, 0, pageSize)
	if err := applyTicketImageCacheFilters(
		db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}), filter).
		Select("sha256, byte_size, media_type, ref_count, scan_state, nsfw_score, risk_level, " +
			"suggestion, label, checked_at, attempt_count, next_attempt_at, created_at, updated_at").
		Order("created_at DESC, sha256 ASC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&rows).Error; err != nil {
		return result, err
	}
	for index := range rows {
		rows[index].Referenced = rows[index].RefCount > 0
	}
	result.Items = rows

	// 汇总独立算一次：只统计当前页会让「总占用」随翻页跳动。
	if err := buildTicketImageCacheSummary(db, filter, &result.Summary); err != nil {
		// 汇总失败不影响列表可用性（与 IPSegmentList 同口径）：页面仍能删单条。
		return result, nil
	}
	return result, nil
}

// buildTicketImageCacheSummary 在**整个筛选范围**上算四组数字。
func buildTicketImageCacheSummary(db *gorm.DB, filter TicketImageCacheListQuery, summary *TicketImageCacheSummary) error {
	summarize := func() *gorm.DB {
		return applyTicketImageCacheFilters(
			db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}), filter)
	}

	if err := summarize().Count(&summary.TotalCount).Error; err != nil {
		return err
	}

	var bytesRow struct {
		Total *int64 `gorm:"column:total"`
	}
	if err := summarize().
		Select("COALESCE(SUM(byte_size), 0) AS total").
		Scan(&bytesRow).Error; err != nil {
		return err
	}
	if bytesRow.Total != nil {
		summary.TotalBytes = *bytesRow.Total
	}

	var zeroRow struct {
		Total int64 `gorm:"column:total"`
	}
	if err := summarize().
		Where("ref_count <= 0").
		Select("COUNT(*) AS total").
		Scan(&zeroRow).Error; err != nil {
		return err
	}
	summary.ZeroRefCount = zeroRow.Total

	// 分组统计必须在**未加 scan_state 过滤**的同一筛选范围上算，否则选了
	// 「只看 approved」时分项就只剩 approved 一项，页面等于什么都没说。
	// 因此这里刻意用「除 scan_state 之外的条件」重建查询。
	groupFilter := filter
	groupFilter.ScanState = ""
	var groupRows []struct {
		State string `gorm:"column:state"`
		Total int64  `gorm:"column:total"`
	}
	if err := applyTicketImageCacheFilters(
		db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}), groupFilter).
		Select("scan_state AS state, COUNT(*) AS total").
		Group("scan_state").
		Scan(&groupRows).Error; err != nil {
		return err
	}
	counts := TicketImageCacheScanCounts{}
	for _, row := range groupRows {
		switch row.State {
		case entity.TicketAttachmentScanPending:
			counts.Pending = row.Total
		case entity.TicketAttachmentScanScanning:
			counts.Scanning = row.Total
		case entity.TicketAttachmentScanApproved:
			counts.Approved = row.Total
		case entity.TicketAttachmentScanSensitive:
			counts.Sensitive = row.Total
		case entity.TicketAttachmentScanFailed:
			counts.Failed = row.Total
		default:
			counts.Other += row.Total
		}
	}
	summary.ScanStates = counts
	return nil
}

// TicketImageCacheRefStateWithDB 返回一份 blob 的真实引用情况（事实值 + 缓存值取大）。
//
// # 为什么要取 max 而不是直接用 ref_count 列
//
// ref_count 是缓存，事实来源是 COUNT(*) WHERE blob_sha256 = ?（见
// TicketAttachmentBlobRefCountWithDB 的注释）。两者可能因为并发/崩溃而偏离。
// 删除是不可逆操作，所以这里取**较大者**：只要有一侧认为还被引用就拒绝删。
// 反过来（取较小者）会在计数少算时删掉仍在被工单引用的图 → 用户看到图片永久 404。
//
// 第二个返回值是事实计数（COUNT），便于把「列值漂移」显示出来排查。
func TicketImageCacheRefStateWithDB(db *gorm.DB, sha256Hex string) (effective int32, truth int32, err error) {
	if db == nil {
		return 0, 0, errTicketImageCacheNoDatabase
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, 0, ErrTicketAttachmentBlobInvalid
	}
	if !ticketImageCacheReady(db) {
		return 0, 0, nil
	}
	truth, err = TicketAttachmentBlobRefCountWithDB(db, sha)
	if err != nil {
		return 0, 0, err
	}
	var stored struct {
		RefCount int32 `gorm:"column:ref_count"`
	}
	found := false
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
		Select("ref_count").
		Where("sha256 = ?", sha).
		Take(&stored).Error; err == nil {
		found = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, err
	}
	effective = truth
	if found && stored.RefCount > effective {
		effective = stored.RefCount
	}
	return effective, truth, nil
}

// DeleteTicketImageCacheWithDB 删除**一条** blob，ref_count > 0 时拒绝。
//
// 判据 ①：拒绝时 blocked 带回被多少个附件行（工单图片）引用，controller 据此
// 返回 409 + 说明。**不允许强删**：一份 blob 被多个附件行共享，删掉字节会让
// 那些工单的图片全部变成 404，且不可恢复（密文已丢）。
//
// 返回 (deleted, blocked, err)：
//   - 删除成功：deleted=1, blocked=nil；
//   - 被引用：deleted=0, blocked 一条，err=nil（「拒绝」是正常业务结果，不是故障）；
//   - 不存在：deleted=0, blocked=nil, err=nil（幂等：重复点删除不该报错）。
func DeleteTicketImageCacheWithDB(db *gorm.DB, sha256Hex string) (int64, []TicketImageCacheBlockedRef, error) {
	return DeleteTicketImageCachesBatchWithDB(db, []string{sha256Hex})
}

// DeleteTicketImageCachesBatchWithDB 批量删除 blob，**逐条**遵守 ref_count 保护。
//
// 判据 ②：批量删除与单条删除走同一套保护。若批量走「不检查引用直接 DELETE」，
// 那么「单条删不掉的东西用批量就能删掉」，保护形同虚设。
//
// # 为什么逐条而不是一条 IN (...) DELETE
//
// 引用计数是**每一行**的，一条大 DELETE 无法表达「跳过还有引用的行」，
// 只能在应用层逐条判定。批量上限取 TicketAttachmentBlobPurgeBatchSize（200），
// 与延迟删除同口径：一次批量删除最多 200 条，避免长事务持写锁。
//
// 返回 (deleted, blocked, err)。blocked 逐条给出原因，页面据此展示「哪几条没删掉、为什么」。
func DeleteTicketImageCachesBatchWithDB(db *gorm.DB, sha256Hexes []string) (int64, []TicketImageCacheBlockedRef, error) {
	if db == nil {
		return 0, nil, errTicketImageCacheNoDatabase
	}
	var deleted int64
	blocked := make([]TicketImageCacheBlockedRef, 0)
	seen := make(map[string]struct{}, len(sha256Hexes))
	for _, raw := range sha256Hexes {
		sha := NormalizeTicketAttachmentBlobSHA256(raw)
		if sha == "" {
			return deleted, blocked, ErrTicketAttachmentBlobInvalid
		}
		if _, exists := seen[sha]; exists {
			continue
		}
		seen[sha] = struct{}{}

		effective, _, err := TicketImageCacheRefStateWithDB(db, sha)
		if err != nil {
			return deleted, blocked, err
		}
		if effective > 0 {
			// 判据 ① 的核心分支：**不删**，并带上「被几个引用」。
			blocked = append(blocked, TicketImageCacheBlockedRef{
				SHA256:   sha,
				RefCount: effective,
				Message: fmt.Sprintf("该图片仍被 %d 个工单图片引用，不能删除；请先删除对应工单图片或等待其过期",
					effective),
			})
			continue
		}
		// 删除条件里**再次**带上 ref_count <= 0：判定与删除之间可能有新上传把计数加回去，
		// 只按主键删会把刚被引用的字节删掉。
		result := db.Session(&gorm.Session{NewDB: true}).
			Where("sha256 = ? AND ref_count <= 0", sha).
			Delete(&entity.TicketAttachmentBlob{})
		if result.Error != nil {
			return deleted, blocked, result.Error
		}
		deleted += result.RowsAffected
	}
	return deleted, blocked, nil
}
