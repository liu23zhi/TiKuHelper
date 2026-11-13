package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// ────────────────────────────────────────────────────────────────────────────
// 接线层（task_doc/121 §4.4）：上传 / 删除 / 定时清理三处把 blob 去重真正用起来。
//
// 分成三个「一次性、自洽」的入口，供上层调用：
//
//	LinkUploadedTicketAttachmentBlobWithDB   —— 上传路径（新建附件行之后）
//	ReleaseDeletedTicketAttachmentBlobWithDB —— 删除路径（删掉附件行之后）
//	RunTicketAttachmentBlobPurgeWorker       —— 定时任务（延迟删除）
//
// 三者共同的不变式：**ref_count 恒等于引用行数**。做法统一是「写完指向之后
// 在同一事务内 COUNT 重算」，不做「+1/-1」的增量猜测。
// ────────────────────────────────────────────────────────────────────────────

// TicketAttachmentBlobUploadLink 描述一次上传要去重写入的字节。
//
// Plain 是**重编码之后**的字节（与 ticket_attachments.ciphertext 里的明文完全一致），
// 不是原始上传字节 —— 否则同一张图只因 EXIF 不同就会被算成两份 sha256，
// 去重彻底失效。
type TicketAttachmentBlobUploadLink struct {
	// AttachmentID 是刚建好的 ticket_attachments.id。
	AttachmentID int64
	// SHA256 是 Plain 的小写十六进制指纹；为空时由 Plain 现算。
	SHA256 string
	// MediaType 是重编码得到的 image/png 或 image/jpeg。
	MediaType string
	// Plain 是重编码后的明文；写入 blobs 表时会被独立加密一次。
	Plain []byte
}

// ticketAttachmentBlobSHA256Of 算明文指纹（小写 hex）。
//
// 单独抽出来是为了让「上传路径」与「回填路径」用同一个算法：
// 回填用的是 ticket_attachments.sha256 列（历史值），上传用的是重编码后的字节，
// 两者必须是同一种算法，否则同一张图会被当成两份。
func ticketAttachmentBlobSHA256Of(plain []byte) string {
	sum := sha256.Sum256(plain)
	return hex.EncodeToString(sum[:])
}

// LinkUploadedTicketAttachmentBlobWithDB 是**上传路径的接线点（按附件 ID）**。
//
// 这是 controller 唯一需要调用的函数：上传时 service 已经把明文重编码并加密写进
// ticket_attachments.nonce/ciphertext，controller 手里**没有**那份重编码后的明文
// （拿原始上传字节去算 sha256 会因 EXIF 差异把同一张图算成两份）。
// 因此这里反过来：读回该附件行 → 用它自己的 AAD 解密 → 得到与落库逐字节一致的明文
// → 算 sha256 → 去重写入 blobs → 回填指向 → 重算 ref_count，全部在一个事务里。
//
// 返回 linked=false 表示「本次没有参与去重」（缺表/缺列，或行残缺），此时**不报错**：
// 旧列一个版本内不删（121 §4.4 第 4 点），没有去重不等于上传失败。
func LinkUploadedTicketAttachmentBlobWithDB(db *gorm.DB, attachmentID int64) (refCount int32, linked bool, err error) {
	if db == nil {
		return 0, false, errors.New("数据库未初始化")
	}
	if attachmentID <= 0 {
		return 0, false, ErrTicketAttachmentNotFound
	}
	if !ticketAttachmentBlobReady(db) || !ticketAttachmentBlobLinkReady(db) {
		return 0, false, nil
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var row entity.TicketAttachment
		if readErr := tx.Session(&gorm.Session{NewDB: true}).
			Where("id = ?", attachmentID).First(&row).Error; readErr != nil {
			if errors.Is(readErr, gorm.ErrRecordNotFound) {
				return ErrTicketAttachmentNotFound
			}
			return readErr
		}
		plain, openErr := openTicketAttachmentPlain(row)
		if openErr != nil {
			// 残缺行（旧版「先插占位行、再 UPDATE 密文」崩在中间的产物）：
			// 不搬进 blobs 表 —— 放一份解不开的密文只会让人以为「已经搬过去了」。
			logger.SysWarn(fmt.Sprintf("ticket_attachment_blob_link_skipped attachmentId=%d reason=%v", attachmentID, openErr))
			return nil
		}
		sha := ticketAttachmentBlobSHA256Of(plain)
		// 与票面 sha256 列核对：不一致说明这行的 sha256 列与实际字节脱钩。
		// 以**实际字节**为准（去重判据是内容），并留下可追溯的日志。
		if recorded := NormalizeTicketAttachmentBlobSHA256(row.SHA256); recorded != "" && recorded != sha {
			logger.SysWarn(fmt.Sprintf(
				"ticket_attachment_blob_sha_mismatch attachmentId=%d recorded=%s computed=%s（以实际字节为准）",
				attachmentID, recorded, sha))
		}
		nonce, ciphertext, sealErr := sealTicketAttachmentBlob(plain, sha)
		if sealErr != nil {
			return sealErr
		}
		_, created, storeErr := StoreTicketAttachmentBlobInTx(tx, sha, row.MediaType, int64(len(plain)), nonce, ciphertext)
		if storeErr != nil {
			return storeErr
		}
		if created {
			logger.SysLog(fmt.Sprintf("ticket_attachment_blob_created sha256=%s byteSize=%d", sha, len(plain)))
		}
		count, linkErr := LinkTicketAttachmentBlobInTx(tx, attachmentID, sha)
		if linkErr != nil {
			return linkErr
		}
		refCount, linked = count, true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return refCount, linked, nil
}

// openTicketAttachmentPlain 用**附件自身的 AAD** 解出该行的明文。
//
// 单独抽出来是因为解密参数（id + userID + mediaType）只有这里能拼出来，
// 且上传路径与后续可能的补齐路径都要用它。
func openTicketAttachmentPlain(row entity.TicketAttachment) ([]byte, error) {
	aead, err := ticketAttachmentCipher()
	if err != nil {
		return nil, err
	}
	if row.ID <= 0 || len(row.Nonce) != aead.NonceSize() || len(row.Ciphertext) == 0 {
		return nil, ErrTicketAttachmentInvalid
	}
	plain, openErr := aead.Open(nil, row.Nonce, row.Ciphertext,
		ticketAttachmentAAD(row.ID, row.UserID, row.MediaType))
	if openErr != nil {
		return nil, ErrTicketAttachmentInvalid
	}
	return plain, nil
}

// LinkUploadedTicketAttachmentBlobWithContentWithDB 是上传路径的**显式内容变体**：
// 调用方手里已经有一份「与落库逐字节一致」的重编码明文时用它（例如批量导入、
// 或测试里直接构造场景）。
//
// 语义（对应判据 ①：同一张图上传两次 → blobs 1 行、attachments 2 行、ref_count=2）：
//  1. 按 sha256 去重写入 blobs（同内容只落一份密文）；
//  2. 把附件行的 blob_sha256 指向它；
//  3. 在同一事务内用 COUNT 重算 ref_count。
//
// ⚠️ 为什么是一个事务而不是三段：ref_count 与「写指向」必须原子地一起变。
// 先写指向、后重算若中途崩了，ref_count 会偏小 → 24 小时延迟删除会删掉仍被引用的 blob。
//
// ⚠️ 为什么每个 sha256 一个事务：几万张图放进一个大事务会把整张表锁住。
// 本函数只处理**一个**附件（一张图），事务边界天然是单图的。
func LinkUploadedTicketAttachmentBlobWithContentWithDB(db *gorm.DB, link TicketAttachmentBlobUploadLink) (refCount int32, linked bool, err error) {
	if db == nil {
		return 0, false, errors.New("数据库未初始化")
	}
	if link.AttachmentID <= 0 {
		return 0, false, ErrTicketAttachmentNotFound
	}
	if len(link.Plain) == 0 {
		return 0, false, ErrTicketAttachmentBlobInvalid
	}
	computed := ticketAttachmentBlobSHA256Of(link.Plain)
	sha := NormalizeTicketAttachmentBlobSHA256(link.SHA256)
	if sha == "" {
		sha = computed
	}
	// 自检：调用方给的指纹与字节必须一致。不一致就意味着「去重判据与实际内容脱钩」，
	// 那会让两张不同的图被当成同一份（或被当成两份）。
	if sha != computed {
		return 0, false, fmt.Errorf("%w: sha256 与明文不匹配（给定 %s，实际 %s）",
			ErrTicketAttachmentBlobInvalid, sha, computed)
	}
	if !ticketAttachmentBlobReady(db) || !ticketAttachmentBlobLinkReady(db) {
		return 0, false, nil
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		// 加密在这里做（而不是让 controller 做）：blob 的 AAD 域前缀是 blob 自己的，
		// 与附件侧 AAD 不同，两套密文不可互换解密。
		nonce, ciphertext, sealErr := sealTicketAttachmentBlob(link.Plain, sha)
		if sealErr != nil {
			return sealErr
		}
		_, created, storeErr := StoreTicketAttachmentBlobInTx(tx, sha, link.MediaType, int64(len(link.Plain)), nonce, ciphertext)
		if storeErr != nil {
			return storeErr
		}
		if created {
			logger.SysLog(fmt.Sprintf("ticket_attachment_blob_created sha256=%s byteSize=%d", sha, len(link.Plain)))
		}
		count, linkErr := LinkTicketAttachmentBlobInTx(tx, link.AttachmentID, sha)
		if linkErr != nil {
			return linkErr
		}
		refCount, linked = count, true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return refCount, linked, nil
}

// ReleaseDeletedTicketAttachmentBlobWithDB 是**删除路径的独立接线点**
// （调用方自己没有事务、或已经把行删掉了时用）。
//
// 前提（必须由调用方保证）：引用该 blob 的 ticket_attachments 行**已经被删掉**了。
// 本函数只负责把计数收敛到事实值：先 Decrease（含归零刷新 updated_at），
// 再 Recompute（真值覆盖，消掉历史漂移）—— 两步都有的理由见 releaseBlobRefCountInTx。
//
// 返回 released=false 表示「没有可释放的」（缺表/缺列，或该附件没有 blob 指向）。
//
// ⚠️ 与清理任务的接缝：**递减 ref_count 的责任在删除附件的那个事务里**，
// 由本函数承担；定时清理（RunTicketAttachmentBlobPurge）只读 ref_count，
// 绝不自己递减。两者顺序必须是「先删引用行并收敛计数，再让清理按 ref_count<=0 判龄」。
func ReleaseDeletedTicketAttachmentBlobWithDB(db *gorm.DB, sha256Hex string) (refCount int32, released bool, err error) {
	if db == nil {
		return 0, false, errors.New("数据库未初始化")
	}
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		// 空指向 = 这张附件从未参与去重（迁移前的行、或迁移没跑），没什么可释放的。
		return 0, false, nil
	}
	if !ticketAttachmentBlobReady(db) || !ticketAttachmentBlobLinkReady(db) {
		return 0, false, nil
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		count, ok, releaseErr := releaseBlobRefCountInTx(tx, sha)
		if releaseErr != nil {
			return releaseErr
		}
		refCount, released = count, ok
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return refCount, released, nil
}

// DeleteTicketAttachmentForUserWithDB 按**调用者身份**删除一张附件。
//
// 这是 controller 唯一需要调用的删除入口：把「谁能删」的规则收在 service 层，
// 与读图路径共用同一条判定（isTicketAdmin），避免 handler 与 service 各写一份。
//
//   - 管理员：可以删任何附件；
//   - 普通用户：只能删自己的（按 user_id 收窄，删不到时返回 deleted=false）。
//
// deleted=false 对两种情况都返回 false 且不报错：不存在、或不属于调用者 ——
// 由 controller 统一翻成 404，不泄露「这张图是否存在」。
func DeleteTicketAttachmentForUserWithDB(db *gorm.DB, user *entity.User, attachmentID int64) (bool, error) {
	if user == nil {
		return false, ErrTicketForbidden
	}
	admin := isTicketAdmin(user)
	return DeleteTicketAttachmentWithDB(db, attachmentID, func(tx *gorm.DB) *gorm.DB {
		if admin {
			return tx
		}
		return tx.Where("user_id = ?", user.ID)
	})
}

// DeleteTicketAttachmentWithDB 删除一张附件并同步收敛 blob 引用计数。
//
// # 为什么放在 service 层而不是让 handler 直接 Delete
//
// 「删附件行」与「递减 ref_count」必须原子：分两步做而中间崩溃，ref_count 就会
// 永久多算这一次引用 → 该 blob **永远不会归零** → 永久占空间（泄漏）。
// 反过来先递减再删行，中途崩溃会让 ref_count 偏小 → 清理误删仍在被引用的字节。
//
// 参数 scope 是**额外的归属条件**（如 `user_id = ? AND message_id IS NULL`），
// 由调用方给出；本函数只保证「删掉的行数」与「计数变化」一致。
//
// 返回 deleted=false 表示没有匹配的行（无权删 / 已删）。
func DeleteTicketAttachmentWithDB(db *gorm.DB, attachmentID int64, scope func(*gorm.DB) *gorm.DB) (bool, error) {
	if db == nil {
		return false, errors.New("数据库未初始化")
	}
	if attachmentID <= 0 {
		return false, ErrTicketAttachmentNotFound
	}
	deleted := false
	err := db.Transaction(func(tx *gorm.DB) error {
		// 先读出指向：删行之后就查不到 sha 了。
		var blobSHA string
		if ticketAttachmentBlobLinkReady(tx) {
			var row struct {
				BlobSHA256 string `gorm:"column:blob_sha256"`
			}
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Table(entity.TableNameTicketAttachment).
				Select("COALESCE(blob_sha256, '') AS blob_sha256").
				Where("id = ?", attachmentID).
				Take(&row).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			blobSHA = NormalizeTicketAttachmentBlobSHA256(row.BlobSHA256)
		}
		query := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", attachmentID)
		if scope != nil {
			query = scope(query)
		}
		result := query.Delete(&entity.TicketAttachment{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		deleted = true
		// 引用行已删，再收敛计数（同一事务）——见本函数注释里的顺序理由。
		_, _, releaseErr := releaseBlobRefCountInTx(tx, blobSHA)
		return releaseErr
	})
	return deleted, err
}

// releaseBlobRefCountInTx 是事务内的收敛实现，供 DeleteTicketAttachmentWithDB 调用。
//
// # 为什么既 Decrease 又 Recompute
//
// 两步都必须有，各管一件事：
//
//	Decrease  —— 增量递减（下限 0），归零时刷新 updated_at（延迟删除的判龄起点）。
//	             这是「一条引用消失」的直接表达，也保证 updated_at 一定被刷新。
//	Recompute —— 用 COUNT 真值覆盖，防止历史上任何一次增量漂移被 24 小时延迟删除
//	             放大成数据丢失（121 §4.4 第 1 点：ref_count 必须恒等于引用行数）。
//
// 只做 Decrease 会累积漂移；只做 Recompute 则失去「归零刷新 updated_at」这一步
// （Recompute 在同值时故意不写库，避免乱动 updated_at）。
func releaseBlobRefCountInTx(tx *gorm.DB, sha256Hex string) (int32, bool, error) {
	sha := NormalizeTicketAttachmentBlobSHA256(sha256Hex)
	if sha == "" {
		return 0, false, nil
	}
	if !ticketAttachmentBlobReady(tx) || !ticketAttachmentBlobLinkReady(tx) {
		return 0, false, nil
	}
	if _, err := DecreaseTicketAttachmentBlobRefCountInTx(tx, sha); err != nil {
		return 0, false, err
	}
	count, err := RecomputeTicketAttachmentBlobRefCountInTx(tx, sha)
	if err != nil {
		return 0, false, err
	}
	return count, true, nil
}

// 定时清理的默认参数。
const (
	// TicketAttachmentBlobPurgeInterval 是延迟删除的轮询周期。
	//
	// 与 log retention 同一取向：间隔不必短（判据是 24 小时缓冲期），
	// 但必须存在 —— 没有定时任务，ref_count 归零的 blob 会永久占着盘。
	TicketAttachmentBlobPurgeInterval = time.Hour
	// ticketAttachmentBlobPurgeMaxBatches 是单轮最多扫的批数，防止异常状态下无限循环。
	ticketAttachmentBlobPurgeMaxBatches = 50
)

// RunTicketAttachmentBlobPurgeWorker 是**定时任务的唯一接线点**。
//
// 每轮：RunTicketAttachmentBlobPurge（分批删除 ref_count<=0 且超过缓冲期的 blob）。
// 轮询用 time.Timer 而不是 Ticker，并每轮检查 ctx：Ticker 在长事务下会把
// 未处理的 tick 堆积起来（连续触发多轮删除）。
//
// interval <= 0 时用 TicketAttachmentBlobPurgeInterval。
func RunTicketAttachmentBlobPurgeWorker(ctx context.Context, db *gorm.DB, interval time.Duration) {
	if db == nil {
		return
	}
	if interval <= 0 {
		interval = TicketAttachmentBlobPurgeInterval
	}
	for {
		if ctx.Err() != nil {
			return
		}
		if deleted, err := RunTicketAttachmentBlobPurge(db, time.Now(), ticketAttachmentBlobPurgeMaxBatches); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_attachment_blob_purge_failed error=%v", err))
		} else if deleted > 0 {
			logger.SysLog(fmt.Sprintf("ticket_attachment_blob_purge_done deleted=%d", deleted))
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// StartTicketAttachmentBlobPurgeWorker 起后台协程并返回「退出后关闭」的 channel。
//
// 与 StartTicketAttachmentScanWorker 同构，便于 main 里统一等待；
// db 为 nil 时立即关闭，不泄漏协程。
func StartTicketAttachmentBlobPurgeWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		RunTicketAttachmentBlobPurgeWorker(ctx, db, TicketAttachmentBlobPurgeInterval)
	}()
	return done
}

// RunTicketAttachmentBlobPurgeOnce 执行**一轮**延迟删除（供注册/一次启动补跑用）。
//
// 与 worker 的区别：不循环、不看 ctx。启动时先跑一次能让「上次进程退出后
// 归零的 blob」不必等满一个周期才被清掉。
func RunTicketAttachmentBlobPurgeOnce(db *gorm.DB) (int64, error) {
	return RunTicketAttachmentBlobPurge(db, time.Now(), ticketAttachmentBlobPurgeMaxBatches)
}
