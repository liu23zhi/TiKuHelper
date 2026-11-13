package service

import (
	"crypto/rand"
	"errors"
	"fmt"
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
