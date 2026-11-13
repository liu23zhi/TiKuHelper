package registry

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// newTicketAttachmentBlobMigrationDB 建立回填测试库。
//
// 只建 system_settings（标记）与 ticket_attachments（数据源）两张表，
// **不建 blobs 表** —— 它必须由迁移自己建出来，否则测试等于自证。
func newTicketAttachmentBlobMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	// 附件表用**真实实体**建：回填要读 nonce / ciphertext / scan_state 等列，
	// 表结构与线上必须一致（这里不是「模拟老库缺列」，缺的只是 blob_sha256）。
	if err := db.AutoMigrate(&entity.TicketAttachment{}); err != nil {
		t.Fatalf("migrate ticket attachments: %v", err)
	}
	return db
}

// seedLegacyAttachment 直接插一行「加 blob_sha256 列之前」的附件行。
//
// 用 Exec 而不是 Create：Create 会带上实体里将来可能新增的列，
// 而本测试要断言的核心恰恰是「迁移之前这些行没有 blob_sha256 指向」。
func seedLegacyAttachment(t *testing.T, db *gorm.DB, ticketID int64, userID int32, sha string, nonce, ciphertext []byte, scanState string) int64 {
	t.Helper()
	now := time.Now()
	if err := db.Exec(
		"INSERT INTO `ticket_attachments` (`ticket_id`, `user_id`, `media_type`, `byte_size`, `sha256`, `nonce`, `ciphertext`, `scan_state`, `attempt_count`, `created_at`, `updated_at`) VALUES (?, ?, 'image/png', ?, ?, ?, ?, ?, 0, ?, ?)",
		ticketID, userID, len(ciphertext), sha, nonce, ciphertext, scanState, now, now,
	).Error; err != nil {
		t.Fatalf("insert legacy attachment: %v", err)
	}
	var id int64
	if err := db.Raw("SELECT id FROM `ticket_attachments` WHERE sha256 = ? AND ticket_id = ? ORDER BY id DESC LIMIT 1", sha, ticketID).Scan(&id).Error; err != nil {
		t.Fatalf("read back id: %v", err)
	}
	return id
}

// countBlobs 返回 blobs 表的行数。
func countBlobs(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&n).Error; err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	return n
}

// TestTicketAttachmentBlobBackfillDeduplicatesAndCountsRefs 是**回填的核心钉子**。
//
// 场景（任务书要求）：造 3 行 ticket_attachments，其中 2 行 sha256 相同
// → 迁移后 blobs 只有 2 行，相同那个 ref_count=2。
//
// 为什么这一条是核心：ref_count 算错不会报错，只会让 24 小时延迟清理
// 删掉仍在被引用的 blob（用户看到图片永久不显示）。它必须被**行为**钉住，
// 而不是只看「表建出来了」。
func TestTicketAttachmentBlobBackfillDeduplicatesAndCountsRefs(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)

	shaA := strings.Repeat("a", 64)
	shaB := strings.Repeat("b", 64)
	// 3 行：A、A（重复）、B —— 期望 blobs=2，A 的 ref_count=2，B 的 ref_count=1。
	idA1 := seedLegacyAttachment(t, db, 100, 1, shaA, []byte("nonce-aaaaaaaaaaaa"), []byte("cipher-A-1"), entity.TicketAttachmentScanApproved)
	idA2 := seedLegacyAttachment(t, db, 200, 1, shaA, []byte("nonce-aaaaaaaaaaaa"), []byte("cipher-A-2"), entity.TicketAttachmentScanApproved)
	idB1 := seedLegacyAttachment(t, db, 300, 2, shaB, []byte("nonce-bbbbbbbbbbbb"), []byte("cipher-B-1"), entity.TicketAttachmentScanPending)

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// ① blobs 只有 2 行（去重生效）。
	if got := countBlobs(t, db); got != 2 {
		t.Fatalf("去重后 blobs 必须只有 2 行（三行附件、两个不同 sha256），实际 %d 行", got)
	}

	// ② 相同 sha256 的那个 ref_count 必须等于引用行数 2。
	var blobA entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", shaA).First(&blobA).Error; err != nil {
		t.Fatalf("blob A must exist: %v", err)
	}
	if blobA.RefCount != 2 {
		t.Fatalf("sha256=%s 被 2 行附件引用，ref_count 必须等于 2（少算会让 24 小时延迟清理删掉仍在用的 blob），实际 %d", shaA, blobA.RefCount)
	}

	// ③ 只被引用一次的 blob ref_count=1。
	var blobB entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", shaB).First(&blobB).Error; err != nil {
		t.Fatalf("blob B must exist: %v", err)
	}
	if blobB.RefCount != 1 {
		t.Fatalf("sha256=%s 只被 1 行引用，ref_count 必须为 1，实际 %d", shaB, blobB.RefCount)
	}

	// ④ 每一行的 blob_sha256 都回填到自己的 sha256 上。
	for _, want := range []struct {
		id  int64
		sha string
	}{
		{idA1, shaA}, {idA2, shaA}, {idB1, shaB},
	} {
		var got string
		if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE id = ?", want.id).Scan(&got).Error; err != nil {
			t.Fatalf("read blob_sha256 of %d: %v", want.id, err)
		}
		if got != want.sha {
			t.Fatalf("附件 %d 的 blob_sha256 必须是 %s，实际 %q", want.id, want.sha, got)
		}
	}

	// ⑤ 密文被搬进 blobs 表（不是空壳）。
	if len(blobA.Ciphertext) == 0 || len(blobA.Nonce) == 0 {
		t.Fatalf("blob 必须带上密文与 nonce，实际 nonce=%d ciphertext=%d 字节", len(blobA.Nonce), len(blobA.Ciphertext))
	}

	// ⑥ 旧列一个版本内不删（112 N1-c）。
	for _, column := range []string{"nonce", "ciphertext", "sha256"} {
		if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, column) {
			t.Fatalf("旧列 %q 必须保留（一个版本内不删，回滚时旧代码照旧读它）", column)
		}
	}

	// ⑦ 标记写入。
	if !systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		t.Fatal("回填成功后必须写标记")
	}
}

// injectBlobBackfillFailure 注入「第 failFrom 次 blob 插入起失败」的回调，返回解除函数与计数器。
//
// 用 Create 回调而不是改数据，是为了让**结构阶段照常成功**（那部分必须 FATAL），
// 只有数据阶段失败 —— 这正是要验的「结构成功、回填失败」组合。
func injectBlobBackfillFailure(t *testing.T, db *gorm.DB, failFrom int) *int {
	t.Helper()
	attempts := new(int)
	name := fmt.Sprintf("test:fail_blob_backfill_from_%d", failFrom)
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		if tx.Statement.Schema.Table != entity.TableNameTicketAttachmentBlob {
			return
		}
		*attempts++
		if *attempts >= failFrom {
			_ = tx.AddError(errors.New("模拟回填中途失败"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
	return attempts
}

// TestTicketAttachmentBlobBackfillFailureDoesNotWriteMarker 验证「回填失败不写标记」。
//
// 单独成一条测试（而不是和「重跑能补齐」挤在一起）：两者是**不同的失败含义**。
// 若只写一条，「写标记」的断言会先失败，把「重跑没补齐」的诊断信息永久
// 挡在后面 —— 而后者才是真正的数据丢失形态。
func TestTicketAttachmentBlobBackfillFailureDoesNotWriteMarker(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)

	shaA := strings.Repeat("c", 64)
	shaB := strings.Repeat("d", 64)
	seedLegacyAttachment(t, db, 100, 1, shaA, []byte("nonce-cccccccccccc"), []byte("cipher-C"), entity.TicketAttachmentScanApproved)
	seedLegacyAttachment(t, db, 200, 1, shaB, []byte("nonce-dddddddddddd"), []byte("cipher-D"), entity.TicketAttachmentScanApproved)

	injectBlobBackfillFailure(t, db, 2)

	// 结构成功、回填中途失败。**必须返回 nil**（不阻断启动）。
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("回填失败不得让迁移返回错误（只记日志、不阻断启动），得到 %v", err)
	}
	// 结构必须已就位。
	if !db.Migrator().HasTable(entity.TableNameTicketAttachmentBlob) {
		t.Fatal("结构阶段成功时 blobs 表必须已创建")
	}
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn) {
		t.Fatal("结构阶段成功时 blob_sha256 列必须已补上")
	}
	// 关键断言：标记**没有**被写入。
	if systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		t.Fatal("回填失败时绝不能写标记 —— 写了标记会让这次失败永久固化，下次启动不再重试")
	}
	// 且确实有数据没搬完（否则「重跑能补齐」那条断言是空的）。
	var movedBeforeRetry int64
	if err := db.Raw("SELECT COUNT(*) AS n FROM `ticket_attachments` WHERE COALESCE(blob_sha256,'') <> ''").Scan(&movedBeforeRetry).Error; err != nil {
		t.Fatal(err)
	}
	if movedBeforeRetry >= 2 {
		t.Fatalf("前置条件失效：本应有未回填的行，实际已回填 %d 行（注入没生效，重跑断言将无意义）", movedBeforeRetry)
	}
}

// TestTicketAttachmentBlobBackfillRetryCompletesWithoutDuplicates 验证
// 「回填失败后重跑能补齐且不重复插」（121 §4.4 第 3 点）。
//
// 这条是幂等性唯一的**行为**判据：主键 + OnConflict 的效果只能通过
// 「重跑后每份 blob 恰好 1 行、所有行都有指向」来证明。
func TestTicketAttachmentBlobBackfillRetryCompletesWithoutDuplicates(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)

	shaA := strings.Repeat("c", 64)
	shaB := strings.Repeat("d", 64)
	seedLegacyAttachment(t, db, 100, 1, shaA, []byte("nonce-cccccccccccc"), []byte("cipher-C"), entity.TicketAttachmentScanApproved)
	seedLegacyAttachment(t, db, 200, 1, shaB, []byte("nonce-dddddddddddd"), []byte("cipher-D"), entity.TicketAttachmentScanApproved)

	injectBlobBackfillFailure(t, db, 2)
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 解除注入后重跑（Cleanup 会更彻底地解除，这里显式解除以便中途重跑）。
	if err := db.Callback().Create().Remove("test:fail_blob_backfill_from_2"); err != nil {
		t.Fatalf("remove callback: %v", err)
	}
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("重跑：%v", err)
	}

	// ① 补齐了。
	if got := countBlobs(t, db); got != 2 {
		t.Fatalf("重跑后必须补齐到 2 份 blob，实际 %d 份（重跑没补上说明幂等判据把「已部分完成」误判成完成）", got)
	}
	// ② 不重复插（每份只 1 行，靠 sha256 主键）。
	for _, sha := range []string{shaA, shaB} {
		var n int64
		if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", sha).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("sha256=%s 在重跑后必须恰好 1 行（幂等），实际 %d 行", sha, n)
		}
	}
	// ③ 指向都补齐了。
	var unfilled int64
	if err := db.Raw("SELECT COUNT(*) AS n FROM `ticket_attachments` WHERE COALESCE(blob_sha256,'') = ''").Scan(&unfilled).Error; err != nil {
		t.Fatal(err)
	}
	if unfilled != 0 {
		t.Fatalf("重跑后所有附件行都必须有 blob_sha256 指向，仍有 %d 行为空", unfilled)
	}
	// ④ 重跑成功后写标记。
	if !systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		t.Fatal("重跑成功后必须写标记")
	}
	// ⑤ ref_count 仍等于引用行数（重跑不得把计数重复累加）。
	for _, sha := range []string{shaA, shaB} {
		var blob entity.TicketAttachmentBlob
		if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
			t.Fatalf("blob %s must exist: %v", sha, err)
		}
		if blob.RefCount != 1 {
			t.Fatalf("sha256=%s 只被 1 行引用，重跑后 ref_count 必须仍是 1（重跑不得重复累加），实际 %d", sha, blob.RefCount)
		}
	}
}

// TestTicketAttachmentBlobBackfillFailureKeepsServiceStartable 验证
// 「新表建成功但回填失败 → 只记日志、服务能起、标记未写」。
//
// 与上一条的区别：这里让**每一行**都失败（从第 1 次插入就注入），
// 因此 blobs 表最终是空的 —— 断言「空表 + 无标记 + 不报错」这个三态。
func TestTicketAttachmentBlobBackfillFailureKeepsServiceStartable(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	sha := strings.Repeat("e", 64)
	seedLegacyAttachment(t, db, 100, 1, sha, []byte("nonce-eeeeeeeeeeee"), []byte("cipher-E"), entity.TicketAttachmentScanApproved)

	var attempts int
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_all_blob_backfill", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != entity.TableNameTicketAttachmentBlob {
			return
		}
		attempts++
		_ = tx.AddError(errors.New("模拟 blob 写入全失败"))
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("回填全失败也不得返回错误（只记日志、服务照常起），得到 %v", err)
	}
	if attempts == 0 {
		t.Fatal("前置条件失效：注入的回调从未被调用，回填根本没走到插入")
	}
	// 表建好了（结构 FATAL 与回填 SysLog 的分界）。
	if !db.Migrator().HasTable(entity.TableNameTicketAttachmentBlob) {
		t.Fatal("结构阶段必须成功（这是它与回填阶段的分界）")
	}
	// blobs 表是空的（全部失败）。
	if got := countBlobs(t, db); got != 0 {
		t.Fatalf("全失败时 blobs 必须为空，实际 %d 行", got)
	}
	// 标记未写 —— 这是「下次重试」的唯一依据。
	if systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		t.Fatal("回填失败时绝不能写标记")
	}
}

// TestTicketAttachmentBlobMigrationSecondStartupSkipsBackfill 验证「二次启动不重跑回填」。
//
// 判据必须能区分「跳过了」与「跑了但没活干」：两者结果相同，所以这里用
// **计数回调**统计 blobs 表上的 INSERT 次数 —— 二次启动时必须是 0 次，
// 而不是「跑了但因为 OnConflict 而 0 行受影响」。
func TestTicketAttachmentBlobMigrationSecondStartupSkipsBackfill(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	sha := strings.Repeat("f", 64)
	seedLegacyAttachment(t, db, 100, 1, sha, []byte("nonce-ffffffffffff"), []byte("cipher-F"), entity.TicketAttachmentScanApproved)

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := countBlobs(t, db); got != 1 {
		t.Fatalf("首次运行必须回填 1 份 blob，实际 %d", got)
	}

	// 首次运行后再挂计数器，确保只统计二次启动的行为。
	var insertAttempts int
	if err := db.Callback().Create().Before("gorm:create").Register("test:count_blob_inserts", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != entity.TableNameTicketAttachmentBlob {
			return
		}
		insertAttempts++
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("second run must be a no-op, got %v", err)
	}
	if insertAttempts != 0 {
		t.Fatalf("二次启动必须整段跳过回填（标记已写），实际仍尝试插入 %d 次", insertAttempts)
	}
	if got := countBlobs(t, db); got != 1 {
		t.Fatalf("二次启动后 blobs 仍应是 1 行，实际 %d", got)
	}
	// 标记只有一行（重复写入要幂等）。
	var markers int64
	if err := db.Model(&entity.SystemSetting{}).Where(map[string]any{"key": ticketAttachmentBlobSchemaReadyKey}).Count(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("标记应恰好一行，实际 %d 行", markers)
	}
}

// TestTicketAttachmentBlobBackfillSkipsIncompleteRows 验证残缺行（旧版「先插占位行、
// 再 UPDATE 密文」崩在中间的产物）不会被搬成一份解不开的 blob。
//
// 判据：残缺行不计入 blobs，但仍会拿到 blob_sha256 指向吗？—— **不会**。
// 让它保持空串是刻意的：读取路径对它仍走旧列（失败即 ErrTicketAttachmentInvalid），
// 与迁移前行为一致，不会因为迁移而变成「看起来有 blob」。
func TestTicketAttachmentBlobBackfillSkipsIncompleteRows(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	good := strings.Repeat("1", 64)
	broken := strings.Repeat("2", 64)
	seedLegacyAttachment(t, db, 100, 1, good, []byte("nonce-111111111111"), []byte("cipher-good"), entity.TicketAttachmentScanApproved)
	// 残缺：nonce 为空（模拟加密前崩溃）。
	brokenID := seedLegacyAttachment(t, db, 200, 1, broken, nil, nil, entity.TicketAttachmentScanPending)

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if got := countBlobs(t, db); got != 1 {
		t.Fatalf("只有 1 行是完整的，blobs 必须是 1 份，实际 %d 份", got)
	}
	var pointer string
	if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE id = ?", brokenID).Scan(&pointer).Error; err != nil {
		t.Fatal(err)
	}
	if pointer != "" {
		t.Fatalf("残缺行不得建立 blob 指向（会让人误以为已迁移），实际 %q", pointer)
	}
	// 完整的行照常指向。
	var goodPointer string
	if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE sha256 = ?", good).Scan(&goodPointer).Error; err != nil {
		t.Fatal(err)
	}
	if goodPointer != good {
		t.Fatalf("完整行必须回填指向，实际 %q", goodPointer)
	}
}

// TestTicketAttachmentBlobBackfillRepairsMisdirectedPointer 是**指针核对**的钉子。
//
// # 构造的真实故障形态（不是假想）
//
// 某行 blob_sha256 非空、但指向另一个 sha（这里 = 一个不存在的 phantom）。
// 旧的「非空就跳过」语义会这样连锁：
//
//	① 回填按 row.sha256 建出自己那份 blob（bbbb）
//	② 指针非空 → 跳过 → 指针仍是 cccc
//	③ Recompute(bbbb) 数 COUNT(blob_sha256='bbbb') = 0
//	④ 自己那份 blob 的 ref_count 被归 0
//	⑤ 24 小时后延迟清理删掉它 —— 图片永久不显示且不报错
//
// 判据（按任务书）：迁移后指针必须等于 row.sha256、且那份 blob 的 ref_count=1。
// 同时断言 phantom 的计数没有被凭空造出来。
func TestTicketAttachmentBlobBackfillRepairsMisdirectedPointer(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	rowSHA := strings.Repeat("b", 64)
	phantomSHA := strings.Repeat("c", 64)

	id := seedLegacyAttachment(t, db, 100, 1, rowSHA,
		[]byte("nonce-bbbbbbbbbbbb"), []byte("cipher-B"), entity.TicketAttachmentScanApproved)

	// 先正常迁移一次，让它建出 bbbb 这份 blob。
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// 制造错乱指针：指向一个不存在的 sha。
	if err := db.Exec("UPDATE `ticket_attachments` SET `blob_sha256` = ? WHERE `id` = ?", phantomSHA, id).Error; err != nil {
		t.Fatalf("misdirect pointer: %v", err)
	}
	// 清标记，让回填重跑（真实场景里这对应「发现数据坏了、手工清标记重跑」）。
	if err := db.Exec("DELETE FROM `system_settings` WHERE `key` = ?", ticketAttachmentBlobSchemaReadyKey).Error; err != nil {
		t.Fatal(err)
	}

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}

	// ① 指针被修回 row.sha256。
	var pointer string
	if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE id = ?", id).Scan(&pointer).Error; err != nil {
		t.Fatal(err)
	}
	if pointer != rowSHA {
		t.Fatalf("错乱指针必须被修回 row.sha256=%s，实际 %q —— "+
			"不修的话该行自己的 blob 会被 Recompute 归 0，24 小时后连自己的数据一起被删", rowSHA, pointer)
	}

	// ② 自己那份 blob 的 ref_count 必须是 1（不是 0）。
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", rowSHA).First(&blob).Error; err != nil {
		t.Fatalf("row 自己的 blob 必须存在: %v", err)
	}
	if blob.RefCount != 1 {
		t.Fatalf("row.sha256 那份 blob 的 ref_count 必须是 1，实际 %d —— "+
			"归 0 意味着 24 小时后它会被当垃圾删掉", blob.RefCount)
	}

	// ③ phantom 不该被凭空造出 blob。
	var phantomCount int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", phantomSHA).Count(&phantomCount).Error; err != nil {
		t.Fatal(err)
	}
	if phantomCount != 0 {
		t.Fatalf("幻影 sha 不得被建出 blob（否则会多存一份永不释放的密文），实际 %d 份", phantomCount)
	}
	// ④ blobs 总数仍是 1（不多不少）。
	if got := countBlobs(t, db); got != 1 {
		t.Fatalf("修复后 blobs 必须恰好 1 份，实际 %d 份", got)
	}
}

// TestTicketAttachmentBlobPointerAlignmentRecomputesStaleSHA 验证纠正指针时
// **旧指向那份 blob 的引用计数也被重算**。
//
// # 为什么这条必须单独钉
//
// 只把指针改回 row.sha256 是不够的：旧 blob（oldSHA）原本算着这一行引用，
// 指针一改它就该少一次。若不重算，它的 ref_count 会永久停在错误的高位 ——
// 表现为「一份已经没人用的 20 MB 密文，永远不被清理」。这是与「误删」方向相反、
// 但同样真实的资源泄漏。
//
// 构造：两行附件，一行指向 oldSHA（真实存在且被它引用），另一行正常。
// 迁移后 oldSHA 那份因为不再被任何行引用，必须收敛到 0。
func TestTicketAttachmentBlobPointerAlignmentRecomputesStaleSHA(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	rowSHA := strings.Repeat("d", 64)
	oldSHA := strings.Repeat("e", 64)

	// 两行内容不同（各自 sha 不同），先让它们各自正常建出 blob。
	firstID := seedLegacyAttachment(t, db, 100, 1, oldSHA,
		[]byte("nonce-eeeeeeeeeeee"), []byte("cipher-E"), entity.TicketAttachmentScanApproved)
	secondID := seedLegacyAttachment(t, db, 200, 1, rowSHA,
		[]byte("nonce-dddddddddddd"), []byte("cipher-D"), entity.TicketAttachmentScanApproved)
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// 前置条件：oldSHA 的 ref_count 是 1。
	var before entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", oldSHA).First(&before).Error; err != nil {
		t.Fatalf("oldSHA blob must exist: %v", err)
	}
	if before.RefCount != 1 {
		t.Fatalf("前置条件失效：oldSHA 的 ref_count 应为 1，实际 %d", before.RefCount)
	}

	// 把 second 行的指针错指到 oldSHA（此时 oldSHA 被 2 行「指向」）。
	if err := db.Exec("UPDATE `ticket_attachments` SET `blob_sha256` = ? WHERE `id` = ?", oldSHA, secondID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM `system_settings` WHERE `key` = ?", ticketAttachmentBlobSchemaReadyKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}

	// ① second 行的指针被修回 rowSHA。
	var ptr string
	if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE id = ?", secondID).Scan(&ptr).Error; err != nil {
		t.Fatal(err)
	}
	if ptr != rowSHA {
		t.Fatalf("second 行的指针必须修回 %s，实际 %q", rowSHA, ptr)
	}
	// ② oldSHA 的计数必须收敛到 1（只剩 first 行引用），而不是停在 2。
	var after entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", oldSHA).First(&after).Error; err != nil {
		t.Fatalf("oldSHA blob must still exist: %v", err)
	}
	if after.RefCount != 1 {
		t.Fatalf("旧指向 oldSHA 的 ref_count 必须重算为 1（只有 first 行还引用它），实际 %d —— "+
			"不重算会让它永久多算一次引用、永不释放", after.RefCount)
	}
	// ③ rowSHA 的计数必须是 1。
	var rowBlob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", rowSHA).First(&rowBlob).Error; err != nil {
		t.Fatalf("rowSHA blob must exist: %v", err)
	}
	if rowBlob.RefCount != 1 {
		t.Fatalf("rowSHA 的 ref_count 必须是 1，实际 %d", rowBlob.RefCount)
	}
	// ④ first 行指针没被动过。
	var firstPtr string
	if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE id = ?", firstID).Scan(&firstPtr).Error; err != nil {
		t.Fatal(err)
	}
	if firstPtr != oldSHA {
		t.Fatalf("first 行的指针本来就正确，不得被改动，实际 %q", firstPtr)
	}
}

// TestTicketAttachmentBlobPointerAlignmentNormalizesNonCanonicalSHA 验证
// **非规范形态**的指针（这里是大写 sha）也会被改写成规范形态。
//
// # 为什么这条必须存在（实测过的真实故障）
//
// 指针存的是大写的同一个 sha 时，「归一化后相等」会骗过校验：
//
//	① 归一化(current) == sha → 判定「已对齐」提前返回，库里仍是大写
//	② Recompute 的 COUNT 用**小写** sha 去数
//	③ SQLite 的 TEXT 比较**大小写敏感** → COUNT = 0
//	④ 该 blob 的 ref_count 被归 0
//	⑤ 24 小时后延迟清理把它删掉 —— 图片永久不显示，且不报任何错
//
// 所以判据必须是「库里存的**原始值**字节级等于 sha」，不是「归一化后相等」。
// 这条测试在「用归一化值做提前返回」的实现下必红。
func TestTicketAttachmentBlobPointerAlignmentNormalizesNonCanonicalSHA(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	sha := strings.Repeat("a", 64)
	upper := strings.ToUpper(sha)

	id := seedLegacyAttachment(t, db, 100, 1, sha,
		[]byte("nonce-aaaaaaaaaaaa"), []byte("cipher-A"), entity.TicketAttachmentScanApproved)
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// 制造非规范形态：指针是大写（同一 sha 的不同书写）。
	if err := db.Exec("UPDATE `ticket_attachments` SET `blob_sha256` = ? WHERE `id` = ?", upper, id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM `system_settings` WHERE `key` = ?", ticketAttachmentBlobSchemaReadyKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}

	// ① 指针必须被规范成小写。
	var stored string
	if err := db.Raw("SELECT blob_sha256 FROM `ticket_attachments` WHERE id = ?", id).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != sha {
		t.Fatalf("非规范（大写）指针必须被规范成小写 sha，实际 %q —— "+
			"不规范化会让 Recompute 的小写 COUNT 数出 0", stored)
	}
	// ② 该 blob 的 ref_count 必须是 1（不是 0）。
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatalf("blob must exist: %v", err)
	}
	if blob.RefCount != 1 {
		t.Fatalf("规范化后 ref_count 必须是 1，实际 %d —— 归 0 意味着 24 小时后被误删", blob.RefCount)
	}
}

// TestTicketAttachmentBlobMigrationSkipsMissingAttachmentTable 验证空库首次启动
// 不会因缺 ticket_attachments 报错，也绝不写标记。
func TestTicketAttachmentBlobMigrationSkipsMissingAttachmentTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		t.Fatal("前置条件错误：ticket_attachments 不应存在")
	}
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("缺前置表时必须静默跳过，得到 %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameTicketAttachmentBlob) {
		t.Fatal("前置表缺失时不得建 blobs 表（否则回填目标存在但没有数据源，标记还会被误写）")
	}
	if systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		t.Fatal("缺前置表时绝不能写标记，否则附件表建好后本迁移被永久跳过")
	}
	if err := EnsureTicketAttachmentBlobSchema(nil); err != nil {
		t.Fatalf("nil db 必须安全返回，得到 %v", err)
	}
}

// TestTicketAttachmentBlobBackfillIsSingleRowTransaction 断言回填的事务粒度是
// **一行一个事务**，而不是一个事务包住整批（121 §4.4 第 2 点）。
//
// 为什么用行为判据而不是数事务次数：`db.Transaction` 直接调 `Begin()`，
// 不经过 GORM 回调，数回调数的做法测不到它。这里用**提交存活**判据：
// 让第 2 行失败，若第 1 行已独立提交，它必须留下来；若整批一个事务，
// 第 2 行的失败会把第 1 行一起回滚 —— 那正是「几万张图锁表数分钟」的写法。
func TestTicketAttachmentBlobBackfillIsSingleRowTransaction(t *testing.T) {
	db := newTicketAttachmentBlobMigrationDB(t)
	shas := []string{
		strings.Repeat("3", 64),
		strings.Repeat("4", 64),
		strings.Repeat("5", 64),
	}
	for i, sha := range shas {
		seedLegacyAttachment(t, db, int64(100+i), 1, sha,
			[]byte("nonce-333333333333"), []byte(fmt.Sprintf("cipher-%d", i)), entity.TicketAttachmentScanApproved)
	}

	// 让第 2 次（及以后）blob 插入失败。
	var attempts int
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_second_blob", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != entity.TableNameTicketAttachmentBlob {
			return
		}
		attempts++
		if attempts >= 2 {
			_ = tx.AddError(errors.New("模拟第 2 行起失败"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("回填失败不得让迁移返回错误，得到 %v", err)
	}
	if attempts < 2 {
		t.Fatalf("前置条件失效：注入回调只被调用 %d 次，没有走到第 2 行", attempts)
	}

	// 核心判据：第 1 行的 blob **必须已经独立提交**并留下。
	// 批级事务会把它连同第 2 行的失败一起回滚，这里就会是 0。
	var first int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", shas[0]).Count(&first).Error; err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Fatalf("第 1 行必须已独立提交（一张图一个事务），实际 blobs 里 %d 行 —— "+
			"回到 0 说明回填被包成了整批/整表一个大事务，几万张图会锁表数分钟", first)
	}
	// 第 2、3 行没搬成（失败被忽略、整轮判失败、不写标记）。
	if got := countBlobs(t, db); got != 1 {
		t.Fatalf("第 2 行起失败后 blobs 应只剩第 1 行的 1 份，实际 %d 份", got)
	}
	if systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		t.Fatal("整轮有失败就不得写标记（下次启动重试）")
	}

	// 解除注入后重跑：补齐且不重复。
	if err := db.Callback().Create().Remove("test:fail_second_blob"); err != nil {
		t.Fatalf("remove callback: %v", err)
	}
	if err := EnsureTicketAttachmentBlobSchema(db); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := countBlobs(t, db); got != 3 {
		t.Fatalf("重跑后必须补齐到 3 份 blob，实际 %d 份", got)
	}
	for _, sha := range shas {
		var n int64
		if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", sha).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("sha256=%s 重跑后必须恰好 1 行，实际 %d 行", sha, n)
		}
	}
}
