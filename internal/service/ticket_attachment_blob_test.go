package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// newTicketAttachmentBlobTestDB 建立 blob 测试库。
//
// 建的是**迁移之后**的形态：blobs 表 + 带 blob_sha256 列的附件表。
// 迁移本身的正确性由 registry 包的测试负责；这里只测 service 的去重/计数/清理语义。
//
// ⚠️ 关键：**绝不能**把 `entity.TicketAttachmentBlobLink` 传给 AutoMigrate。
// 它与 TicketAttachment 指向同一张物理表，而 GORM 在这次调用里会把两个模型
// **合并成一个建表语句**，结果表只剩 `id` + `blob_sha256` 两列（实测复现）。
// 生产代码因此只用 `Migrator().AddColumn(&entity.TicketAttachmentBlobLink{}, ...)`，
// 这里也必须用显式 ALTER 复现同样的形态。
func newTicketAttachmentBlobTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.TicketAttachment{},
		&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 显式 ALTER 补列，与 registry 侧 addTicketAttachmentBlobLinkColumn 的 SQLite 分支一致。
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, "blob_sha256") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment +
			" ADD COLUMN `blob_sha256` char(64) NOT NULL DEFAULT ''").Error; err != nil {
			t.Fatalf("add blob_sha256: %v", err)
		}
	}
	useTicketAttachmentTestKey(t)
	return db
}

// newAttachmentRow 插一行附件（blob_sha256 由调用方决定），返回 ID。
func newAttachmentRow(t *testing.T, db *gorm.DB, ticketID int64, blobSHA string) int64 {
	t.Helper()
	row := entity.TicketAttachment{
		TicketID: ticketID, UserID: 1, MediaType: "image/png", ByteSize: 16,
		SHA256: strings.TrimSpace(blobSHA), ScanState: entity.TicketAttachmentScanApproved,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	if blobSHA != "" {
		if err := db.Exec("UPDATE `ticket_attachments` SET `blob_sha256` = ? WHERE `id` = ?", blobSHA, row.ID).Error; err != nil {
			t.Fatalf("set blob_sha256: %v", err)
		}
	}
	return row.ID
}

// blobSHAOf 算一份明文的 sha256（小写 hex）。
func blobSHAOf(plain []byte) string {
	sum := sha256.Sum256(plain)
	return hex.EncodeToString(sum[:])
}

// TestTicketAttachmentBlobStoreDeduplicatesSameContent 验证**去重写入**：
// 同一份明文写两次只落 1 行，第二次不写密文。
func TestTicketAttachmentBlobStoreDeduplicatesSameContent(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	plain := []byte("同一张图的字节")
	sha := blobSHAOf(plain)

	first, created, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if !created {
		t.Fatal("首次写入必须 created=true")
	}

	second, created, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	if created {
		t.Fatal("同一份内容第二次写入必须 created=false（这就是去重省下的空间）")
	}
	if second.SHA256 != sha {
		t.Fatalf("返回的 sha256 必须是 %s，实际 %s", sha, second.SHA256)
	}

	var count int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("同一份内容只该有 1 行 blob，实际 %d 行", count)
	}
	// 密文只写了一份，且能解回原文（AAD 用 sha256 而不是自增 id）。
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blob.Ciphertext, first.Ciphertext) {
		t.Fatal("第二次写入不得覆盖既有密文")
	}
	mediaType, decoded, err := ReadTicketAttachmentBlobWithDB(db, sha)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if mediaType != "image/png" || !bytes.Equal(decoded, plain) {
		t.Fatalf("解密结果必须与原文一致，实际 mediaType=%s plain=%q", mediaType, decoded)
	}
}

// TestTicketAttachmentBlobRefCountEqualsReferenceRows 是**引用计数的核心钉子**
// （121 §4.4 第 1 点）。
//
// 判据：ref_count 必须恒等于引用行数。少算会让 24 小时延迟删除删掉仍在被引用的
// blob —— 用户看到图片永久不显示，而且不报任何错。
//
// 故意用 2 与 3 两个**非 1** 的值：若实现写成「恒为 1」，2 与 3 两组都会红。
func TestTicketAttachmentBlobRefCountEqualsReferenceRows(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	plain := []byte("被多行引用的图")
	sha := blobSHAOf(plain)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain); err != nil {
		t.Fatalf("store: %v", err)
	}

	// 两行引用 → ref_count 必须是 2。
	idA := newAttachmentRow(t, db, 100, "")
	idB := newAttachmentRow(t, db, 200, "")
	if got, err := LinkTicketAttachmentBlobInTx(db, idA, sha); err != nil || got != 1 {
		t.Fatalf("第一行链接后 ref_count 必须是 1，实际 %d（err=%v）", got, err)
	}
	if got, err := LinkTicketAttachmentBlobInTx(db, idB, sha); err != nil || got != 2 {
		t.Fatalf("第二行链接后 ref_count 必须是 2，实际 %d（err=%v）", got, err)
	}
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 2 {
		t.Fatalf("ref_count 必须等于引用行数 2（不是恒为 1），实际 %d", blob.RefCount)
	}

	// 再加一行 → 3。
	idC := newAttachmentRow(t, db, 300, "")
	if got, err := LinkTicketAttachmentBlobInTx(db, idC, sha); err != nil || got != 3 {
		t.Fatalf("第三行链接后 ref_count 必须是 3，实际 %d（err=%v）", got, err)
	}
	// 与「COUNT 事实」逐值对齐（这是判据本身，不是缓存）。
	count, err := TicketAttachmentBlobRefCountWithDB(db, sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != count || count != 3 {
		t.Fatalf("ref_count 必须等于 COUNT(*)：列值 %d、事实值 %d、期望 3", blob.RefCount, count)
	}
}

// TestTicketAttachmentBlobReleaseDecreasesToZeroAndRefreshesAge 验证释放路径：
// 递减到 0 后刷新 updated_at（延迟删除的判龄起点）。
func TestTicketAttachmentBlobReleaseDecreasesToZeroAndRefreshesAge(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	plain := []byte("会被释放的图")
	sha := blobSHAOf(plain)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain); err != nil {
		t.Fatalf("store: %v", err)
	}
	// 把 updated_at 拨到 30 天前，模拟「这份 blob 很久没被动过」。
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `updated_at` = ? WHERE `sha256` = ?", old, sha).Error; err != nil {
		t.Fatal(err)
	}

	id := newAttachmentRow(t, db, 100, "")
	if _, err := LinkTicketAttachmentBlobInTx(db, id, sha); err != nil {
		t.Fatalf("link: %v", err)
	}
	// 删掉引用行，再释放。
	if err := db.Where("id = ?", id).Delete(&entity.TicketAttachment{}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := ReleaseTicketAttachmentBlobInTx(db, sha)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if got != 0 {
		t.Fatalf("没有引用行后 ref_count 必须归 0，实际 %d", got)
	}

	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 0 {
		t.Fatalf("ref_count 必须归 0，实际 %d", blob.RefCount)
	}
	// 归零必须刷新 updated_at：否则用 30 天前的旧时间戳判龄，下一轮清理会立刻误删。
	if !blob.UpdatedAt.After(old.Add(24 * time.Hour)) {
		t.Fatalf("归零时 updated_at 必须刷新（缓冲期从现在起算），实际仍是 %v", blob.UpdatedAt)
	}
	// 缓冲期内不得删除。
	//
	// ⚠️ 这里必须用**明显晚于 updated_at** 的时刻，不能用 time.Now()。
	// 原因（实测）：grace 若被改成 0，判据退化为 `updated_at < now`；而
	// updated_at 就是刚才 Release 里 time.Now() 取的值，紧接着再取一次 time.Now()
	// 往往得到**完全相同**的纳秒时间戳（实测 delta=0s），于是 `updated_at < now`
	// 为假、不删、断言照样通过 —— 那是靠时间精度侥幸，不是 grace 生效。
	// 往后推 2 秒就与 grace 的取值彻底解耦：grace=24h 时仍远未过期（不删，绿）；
	// grace=0 时立刻过期（删，红）。
	deleted, err := PurgeExpiredTicketAttachmentBlobs(db, blob.UpdatedAt.Add(2*time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("刚归零的 blob 在 24 小时缓冲期内不得删除，实际删了 %d 份", deleted)
	}
	// 同一时刻下真过期的那份必须被删 —— 证明上面那条「不删」不是因为整段逻辑没跑。
	if deleted := purgeDeleteableFixture(t, db); deleted != 1 {
		t.Fatalf("对照组：真正过期的 blob 必须被删（期望 1 份，实际 %d）—— "+
			"否则上面的「不删」可能只是清理逻辑整个没生效", deleted)
	}
}

// purgeDeleteableFixture 造一份「归零且远超缓冲期」的 blob，并立即清理，返回删除行数。
//
// 它是「未过期不删」那条断言的正对照：只有确认清理**确实会删**，
// 「不删」才是有意义的结论，而不是「函数什么都没干」。
func purgeDeleteableFixture(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	staleSHA := strings.Repeat("8", 64)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, staleSHA, "image/png", []byte("对照组-早已过期")); err != nil {
		t.Fatalf("store stale fixture: %v", err)
	}
	// 归零 + 时间拨到缓冲期的 3 倍之前。
	stale := time.Now().Add(-3 * entity.TicketAttachmentBlobDedupGrace)
	if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `ref_count` = 0, `updated_at` = ? WHERE `sha256` = ?",
		stale, staleSHA).Error; err != nil {
		t.Fatalf("age stale fixture: %v", err)
	}
	deleted, err := PurgeExpiredTicketAttachmentBlobs(db, time.Now(), 0)
	if err != nil {
		t.Fatalf("purge stale fixture: %v", err)
	}
	return deleted
}

// TestTicketAttachmentBlobRefCountDriftIsCorrectedByRecompute 验证「增量漂移能被重算收敛」。
//
// 形态：模拟外部直接改库把 ref_count 写成 1（而事实是 3）。
// 必须能被 Recompute 修回 3 —— 这是「漂移不会累积成数据丢失」的保证。
func TestTicketAttachmentBlobRefCountDriftIsCorrectedByRecompute(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	plain := []byte("计数会漂移的图")
	sha := blobSHAOf(plain)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain); err != nil {
		t.Fatalf("store: %v", err)
	}
	for i := 0; i < 3; i++ {
		id := newAttachmentRow(t, db, int64(100+i), sha)
		_ = id
	}
	// 漂移：手工把列写成 1（事实是 3）。
	if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `ref_count` = 1 WHERE `sha256` = ?", sha).Error; err != nil {
		t.Fatal(err)
	}
	got, err := RecomputeTicketAttachmentBlobRefCountInTx(db, sha)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if got != 3 {
		t.Fatalf("重算必须拉回事实值 3，实际 %d", got)
	}
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 3 {
		t.Fatalf("列值必须被修成 3，实际 %d", blob.RefCount)
	}
}

// TestTicketAttachmentBlobDecreaseNeverGoesNegative 验证递减下限 0。
//
// 为什么单独钉：减成负数会让「ref_count <= 0」这个清理判据提前命中，
// 而且负值会掩盖「实际还有引用」的事实。
func TestTicketAttachmentBlobDecreaseNeverGoesNegative(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	plain := []byte("会被过度递减的图")
	sha := blobSHAOf(plain)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `ref_count` = 0 WHERE `sha256` = ?", sha).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := DecreaseTicketAttachmentBlobRefCountInTx(db, sha)
		if err != nil {
			t.Fatalf("decrease #%d: %v", i, err)
		}
		if got != 0 {
			t.Fatalf("已归零后再递减必须仍是 0，第 %d 次得到 %d", i+1, got)
		}
	}
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 0 {
		t.Fatalf("ref_count 不得为负，实际 %d", blob.RefCount)
	}
}

// TestPurgeExpiredTicketAttachmentBlobsHonorsGraceAndRefCount 验证延迟删除的两道闸：
// ① 未到 24 小时不删；② ref_count > 0 一律不删（哪怕时间已经很久）。
func TestPurgeExpiredTicketAttachmentBlobsHonorsGraceAndRefCount(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)

	// 三份 blob：归零且过期（应删）、归零但未过期（留）、仍被引用且过期（留）。
	expiredSHA := strings.Repeat("a", 64)
	freshSHA := strings.Repeat("b", 64)
	inUseSHA := strings.Repeat("c", 64)
	for _, pair := range []struct {
		sha string
		age time.Duration
		ref int
	}{
		{expiredSHA, 48 * time.Hour, 0},
		{freshSHA, time.Hour, 0},
		{inUseSHA, 48 * time.Hour, 1},
	} {
		if _, _, err := StoreTicketAttachmentBlobWithDB(db, pair.sha, "image/png", []byte("blob-"+pair.sha)); err != nil {
			t.Fatalf("store %s: %v", pair.sha, err)
		}
		mark := time.Now().Add(-pair.age)
		if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `ref_count` = ?, `updated_at` = ? WHERE `sha256` = ?",
			pair.ref, mark, pair.sha).Error; err != nil {
			t.Fatalf("age %s: %v", pair.sha, err)
		}
	}

	deleted, err := PurgeExpiredTicketAttachmentBlobs(db, time.Now(), 0)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("只有「归零且过期」的 1 份该被删，实际删了 %d 份", deleted)
	}
	remaining := make(map[string]bool)
	var blobs []entity.TicketAttachmentBlob
	if err := db.Find(&blobs).Error; err != nil {
		t.Fatal(err)
	}
	for _, blob := range blobs {
		remaining[blob.SHA256] = true
	}
	if remaining[expiredSHA] {
		t.Fatal("归零且超过 24 小时的 blob 必须被删除")
	}
	if !remaining[freshSHA] {
		t.Fatal("归零但未满 24 小时的 blob 必须保留（缓冲期）")
	}
	if !remaining[inUseSHA] {
		t.Fatal("仍被引用的 blob 一律不得删除 —— 删了用户会看到图片永久不显示")
	}
}

// TestTicketAttachmentBlobAADBindsContent 验证密文无法跨 sha256 行搬运
// （112 §1.6 方案 A 的安全性前提）。
func TestTicketAttachmentBlobAADBindsContent(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	plain := []byte("AAD 绑定测试")
	shaA := blobSHAOf(plain)
	shaB := strings.Repeat("9", 64)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, shaA, "image/png", plain); err != nil {
		t.Fatalf("store: %v", err)
	}
	// 把 A 的密文搬到 B 的行下：解密必须失败（AAD 含 sha256）。
	if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `sha256` = ? WHERE `sha256` = ?", shaB, shaA).Error; err != nil {
		t.Fatalf("move blob: %v", err)
	}
	if _, _, err := ReadTicketAttachmentBlobWithDB(db, shaB); !errors.Is(err, ErrTicketAttachmentBlobInvalid) {
		t.Fatalf("密文被搬到另一个 sha256 下必须解密失败（ErrTicketAttachmentBlobInvalid），实际 %v", err)
	}
}

// TestStoreTicketAttachmentBlobRejectsIncompleteInput 验证残缺输入被拒，
// 不会在 blobs 表里留下解不开的行。
func TestStoreTicketAttachmentBlobRejectsIncompleteInput(t *testing.T) {
	db := newTicketAttachmentBlobTestDB(t)
	cases := []struct {
		name string
		sha  string
		data []byte
	}{
		{name: "empty sha", sha: "", data: []byte("x")},
		{name: "empty data", sha: strings.Repeat("a", 64), data: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := StoreTicketAttachmentBlobWithDB(db, tc.sha, "image/png", tc.data); !errors.Is(err, ErrTicketAttachmentBlobInvalid) {
				t.Fatalf("必须返回 ErrTicketAttachmentBlobInvalid，实际 %v", err)
			}
		})
	}
	var count int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("残缺输入不得落库，实际 %d 行", count)
	}
}
