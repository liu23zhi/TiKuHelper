package service

import (
	"bytes"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 工单图片保留策略（用户 2026-11-13 决策：「清理逻辑参考日志文件，
// 添加按照时间清理，按照大小清理」）。
//
// 本文件按任务书的七条判据逐条落用例，每条都是**独立用例**（不共用断言链），
// 因为这几条各自对应一个可以独立坏掉的实现点：
//
//	① 按时间：200 天前的附件被清理
//	② 按大小：超限时**最老的先删**（断言具体 ID）
//	③ 两者同时超限：先按时间、再判大小（顺序确定，可观测）
//	④ 变异：改成 ORDER BY id DESC → ② 必红（② 的种子让 id 序与时间序相反）
//	⑤ 变异：删附件不收敛 ref_count → 「blob 应被清理」用例红
//	⑥ 清理后工单与工单消息一条都不能少（且附件行不物理删除）
//	⑦ interval<=0 时每轮重读配置
//
// # 一个刻意的测试手法：用 byte_size 列造 GiB 级体量
//
// 「按大小清理」的真实阈值以 GiB 为单位（默认 20 GiB），真造 20 GiB 密文不现实。
// 因此用例**只造小的 ciphertext，但把 byte_size 写成 1 GiB**：清理的判据与记账
// 全部读 byte_size（SUM / 递减），所以这条路径被完整执行，而内存占用是常数。
// 这不是绕过实现，而是让「GiB 级阈值」这条业务判据可测。

// newTicketAttachmentRetentionTestDB 建保留策略测试库。
//
// 注意两步：
//   - AutoMigrate 建表时**已经带上** purged_at（实体里有该字段），所以本文件的用例
//     测的是「列存在」的正常路径；
//   - blob_sha256 不是实体字段（它是迁移补的缓存列），必须显式 ALTER 加上，
//     否则 ticketAttachmentBlobLinkReady 恒 false，⑤ 那条接缝根本走不到。
func newTicketAttachmentRetentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:ticket-attachment-retention-%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Ticket{},
		&entity.TicketMessage{}, &entity.TicketAttachment{}, &entity.TicketAttachmentBlob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment +
		" ADD COLUMN `blob_sha256` char(64) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatalf("add blob_sha256 column: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedTicketAttachmentRetentionTicket 造一张工单 + 一条消息，返回二者。
func seedTicketAttachmentRetentionTicket(t *testing.T, db *gorm.DB, ownerID int32) (entity.Ticket, entity.TicketMessage) {
	t.Helper()
	ticket := entity.Ticket{
		UserID: ownerID, UserName: "owner", Subject: "保留策略工单", Status: entity.TicketStatusOpen,
		Priority: entity.TicketPriorityNormal, LastMessageAt: time.Now(),
	}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	message := entity.TicketMessage{
		TicketID: ticket.ID, UserID: ownerID, AuthorName: "owner", Body: "带图的历史消息",
		AuthorRole: entity.TicketAuthorRoleUser,
	}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	return ticket, message
}

// retentionAttachmentSeed 描述一张待造附件。
type retentionAttachmentSeed struct {
	TicketID   int64
	MessageID  *int64
	UserID     int32
	ByteSize   int64
	SHA256     string
	BlobSHA256 string
	CreatedAt  time.Time
}

// seedRetentionAttachment 造一张附件，并把 created_at 强制成指定时刻。
//
// created_at 必须用 UpdateColumn 回写：GORM 的 autoCreateTime 只在字段为零值时
// 才填当前时间，但一旦其它路径（如批量导入）参与进来就不能保证，显式回写让
// 「最老的先删」这条断言不依赖 GORM 的插入行为。
func seedRetentionAttachment(t *testing.T, db *gorm.DB, seed retentionAttachmentSeed) entity.TicketAttachment {
	t.Helper()
	sha := seed.SHA256
	if sha == "" {
		sha = fmt.Sprintf("%064d", seed.ByteSize)
	}
	row := entity.TicketAttachment{
		TicketID: seed.TicketID, MessageID: seed.MessageID, UserID: seed.UserID,
		MediaType: "image/png", ByteSize: seed.ByteSize, SHA256: sha,
		Nonce:      bytes.Repeat([]byte{0x01}, 12),
		Ciphertext: bytes.Repeat([]byte{0x02}, 32),
		ScanState:  entity.TicketAttachmentScanApproved,
	}
	if seed.CreatedAt.IsZero() {
		seed.CreatedAt = time.Now()
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", row.ID).
		UpdateColumn("created_at", seed.CreatedAt).Error; err != nil {
		t.Fatalf("force created_at: %v", err)
	}
	if seed.BlobSHA256 != "" {
		if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", row.ID).
			UpdateColumn("blob_sha256", seed.BlobSHA256).Error; err != nil {
			t.Fatalf("force blob_sha256: %v", err)
		}
	}
	var reloaded entity.TicketAttachment
	if err := db.Where("id = ?", row.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload attachment: %v", err)
	}
	return reloaded
}

// reloadRetentionAttachment 重新读一行（断言用）。
func reloadRetentionAttachment(t *testing.T, db *gorm.DB, id int64) entity.TicketAttachment {
	t.Helper()
	var row entity.TicketAttachment
	if err := db.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("reload attachment %d: %v", id, err)
	}
	return row
}

// purgedAttachmentIDs 返回「已被清理」的附件 ID 集合。
func purgedAttachmentIDs(t *testing.T, db *gorm.DB) map[int64]bool {
	t.Helper()
	var rows []entity.TicketAttachment
	if err := db.Where("purged_at IS NOT NULL").Find(&rows).Error; err != nil {
		t.Fatalf("list purged attachments: %v", err)
	}
	got := make(map[int64]bool, len(rows))
	for _, row := range rows {
		got[row.ID] = true
	}
	return got
}

// compareIDSet 比对 ID 集合，输出「多/少」而不是只说 false —— 断言失败时
// 必须能直接看出删除顺序错在哪（④ 的变异诊断完全依赖这一点）。
func compareIDSet(t *testing.T, label string, got map[int64]bool, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 清理集合大小不符，got=%v want=%v", label, keysOfIDSet(got), want)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("%s: 应当清理 %d 却没清理，got=%v want=%v", label, id, keysOfIDSet(got), want)
		}
	}
}

func keysOfIDSet(set map[int64]bool) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// ── ① 按时间清理 ───────────────────────────────────────────────────────────

// TestPruneTicketAttachmentsByAgePurgesExpiredOnly 造一张 200 天前的附件与一张
// 10 天前的附件，断言只有前者被清理，且清理只降级内容、不删业务行。
func TestPruneTicketAttachmentsByAgePurgesExpiredOnly(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket, message := seedTicketAttachmentRetentionTicket(t, db, user.ID)
	now := time.Now()

	expired := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, MessageID: &message.ID, UserID: user.ID,
		ByteSize: 4096, SHA256: strings.Repeat("a", 64), CreatedAt: now.Add(-200 * 24 * time.Hour),
	})
	fresh := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, MessageID: &message.ID, UserID: user.ID,
		ByteSize: 4096, SHA256: strings.Repeat("b", 64), CreatedAt: now.Add(-10 * 24 * time.Hour),
	})

	purged, err := PruneTicketAttachmentsByAge(db, now, DefaultTicketAttachmentRetentionMaxAgeDays)
	if err != nil {
		t.Fatalf("prune by age: %v", err)
	}
	if purged != 1 {
		t.Fatalf("应当只清理 1 张（200 天前），got %d", purged)
	}

	gotExpired := reloadRetentionAttachment(t, db, expired.ID)
	if gotExpired.PurgedAt == nil {
		t.Fatal("200 天前的附件必须被标记 purged_at")
	}
	if len(gotExpired.Ciphertext) != 0 || len(gotExpired.Nonce) != 0 {
		t.Fatalf("密文与 nonce 必须置空，got ciphertext=%d nonce=%d",
			len(gotExpired.Ciphertext), len(gotExpired.Nonce))
	}
	// 保留行的语义：业务绑定与展示所需的列一个都不能丢。
	if gotExpired.MessageID == nil || *gotExpired.MessageID != message.ID {
		t.Fatal("清理后 message_id 绑定必须保留，否则历史消息出现空洞")
	}
	if gotExpired.ByteSize != 4096 || gotExpired.MediaType != "image/png" || gotExpired.SHA256 != expired.SHA256 {
		t.Fatalf("清理后 byte_size/media_type/sha256 必须保留，got %+v", gotExpired)
	}

	gotFresh := reloadRetentionAttachment(t, db, fresh.ID)
	if gotFresh.PurgedAt != nil {
		t.Fatal("10 天前的附件在 180 天保留期外不应被清理")
	}
	if len(gotFresh.Ciphertext) == 0 {
		t.Fatal("未到期附件的密文不得被动过")
	}
}

// TestPruneTicketAttachmentsByAgeIgnoresNonPositiveAge 断言 maxAgeDays<=0 是
// 「不按时间清理」，而不是「全部清掉」——把两种极端语义压在 0 上迟早误删。
func TestPruneTicketAttachmentsByAgeIgnoresNonPositiveAge(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	row := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 1024,
		CreatedAt: time.Now().Add(-3000 * 24 * time.Hour),
	})
	for _, age := range []int{0, -1} {
		purged, err := PruneTicketAttachmentsByAge(db, time.Now(), age)
		if err != nil {
			t.Fatalf("age=%d: %v", age, err)
		}
		if purged != 0 {
			t.Fatalf("age=%d 必须视为「不按时间清理」，实际清理 %d 张", age, purged)
		}
	}
	if reloadRetentionAttachment(t, db, row.ID).PurgedAt != nil {
		t.Fatal("age<=0 时不得清理任何附件")
	}
}

// ── ② 按大小清理：最老的先删 ────────────────────────────────────────────────

// TestPruneTicketAttachmentsBySizeDeletesOldestFirst 断言「按大小清理从最老的删起」，
// 并且**断言删掉的具体 ID**而不是「删了几个」。
//
// 种子刻意让 id 序与时间序**一致**（先建最老的 → id 最小），这样：
//   - 正确实现（ORDER BY created_at ASC）删掉 id 1、2；
//   - 变异成 ORDER BY id DESC 会删掉 id 3、2 —— 集合不同，必红（判据 ④）。
func TestPruneTicketAttachmentsBySizeDeletesOldestFirst(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	now := time.Now()

	oldest := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 1000, CreatedAt: now.Add(-30 * 24 * time.Hour),
	})
	middle := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 1000, CreatedAt: now.Add(-20 * 24 * time.Hour),
	})
	newest := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 1000, CreatedAt: now.Add(-1 * 24 * time.Hour),
	})
	if !(oldest.ID < middle.ID && middle.ID < newest.ID) {
		t.Fatalf("种子前提被破坏：id 序必须与时间序一致，got %d/%d/%d",
			oldest.ID, middle.ID, newest.ID)
	}

	// 总量 3000 → 上限 1000：必须删掉最老的两张，只剩最新那张。
	purged, err := PruneTicketAttachmentsBySize(db, now, 1000)
	if err != nil {
		t.Fatalf("prune by size: %v", err)
	}
	if purged != 2 {
		t.Fatalf("上限 1000、总量 3000 时必须清理 2 张，got %d", purged)
	}
	compareIDSet(t, "按大小清理顺序", purgedAttachmentIDs(t, db), []int64{oldest.ID, middle.ID})

	if reloadRetentionAttachment(t, db, newest.ID).PurgedAt != nil {
		t.Fatal("最新的一张必须保留")
	}
	if len(reloadRetentionAttachment(t, db, newest.ID).Ciphertext) == 0 {
		t.Fatal("最新一张的密文不得被动过")
	}
}

// TestPruneTicketAttachmentsBySizeNoopWhenUnderLimit 断言未超限时一张都不动。
func TestPruneTicketAttachmentsBySizeNoopWhenUnderLimit(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	row := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 100,
	})
	purged, err := PruneTicketAttachmentsBySize(db, time.Now(), 1<<30)
	if err != nil {
		t.Fatalf("prune by size: %v", err)
	}
	if purged != 0 {
		t.Fatalf("未超限时不得清理，got %d", purged)
	}
	if reloadRetentionAttachment(t, db, row.ID).PurgedAt != nil {
		t.Fatal("未超限时不得标记 purged_at")
	}
}

// ── ③ 两条策略的顺序：先按时间、再按大小 ────────────────────────────────────

// TestRunTicketAttachmentRetentionAgeRunsBeforeSize 是本任务最强的顺序判据。
//
// # 为什么这个种子能区分顺序（不是「看起来对的断言」）
//
// 四张附件，byte_size 各 1 GiB（用列值造体量，见文件头注释），上限设成 1 GiB：
//
//		id1  t-200d（超 180 天）  id2  t-190d（超 180 天）  id3  t-10d  id4  t-1d
//
//	  - **先时间后大小**（正确）：age 删 id1/id2 → 剩 2 GiB → size 删最老的 id3
//	    → 剩 1 GiB。结果 AgePurged=2、SizePurged=1、留下 id4。
//	  - **先大小后时间**（错误）：size 先删最老的 id1 → 剩 3 GiB → 再删 id2
//	    → 剩 2 GiB → 再删 id3 → 剩 1 GiB 停止；随后 age 已无候选。
//	    结果 AgePurged=0、SizePurged=3、留下 id4。
//
// 两种顺序的 (AgePurged, SizePurged) 完全不同，所以这条断言真的在测顺序，
// 而不是在测「总数对」。
func TestRunTicketAttachmentRetentionAgeRunsBeforeSize(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	now := time.Now()
	const gib = int64(1 << 30)

	seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1,
		ByteSize: gib, CreatedAt: now.Add(-200 * 24 * time.Hour)})
	seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1,
		ByteSize: gib, CreatedAt: now.Add(-190 * 24 * time.Hour)})
	third := seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1,
		ByteSize: gib, CreatedAt: now.Add(-10 * 24 * time.Hour)})
	newest := seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1,
		ByteSize: gib, CreatedAt: now.Add(-1 * 24 * time.Hour)})

	if err := saveSystemSettingValue(db, TicketAttachmentRetentionMaxAgeDaysSettingKey, "180", "test"); err != nil {
		t.Fatalf("seed max age: %v", err)
	}
	if err := saveSystemSettingValue(db, TicketAttachmentRetentionMaxTotalGiBSettingKey, "1", "test"); err != nil {
		t.Fatalf("seed max total: %v", err)
	}

	result, err := RunTicketAttachmentRetentionWithDB(db, now)
	if err != nil {
		t.Fatalf("run retention: %v", err)
	}
	if result.AgePurged != 2 {
		t.Fatalf("按时间必须先清掉 2 张超龄附件，got %d（顺序被改变时会变成 0）", result.AgePurged)
	}
	if result.SizePurged != 1 {
		t.Fatalf("按时间之后仍超限，按大小必须再清 1 张，got %d（顺序被改变时会变成 3）", result.SizePurged)
	}
	purged := purgedAttachmentIDs(t, db)
	if purged[newest.ID] {
		t.Fatal("最新的一张必须保留")
	}
	if !purged[third.ID] {
		t.Fatalf("按大小清理必须从**剩余的**最老那张删起（id=%d）", third.ID)
	}
	// 存留总量应当收敛到上限以内（这是「清理真的生效」的另一种表述）。
	if result.LiveBytesAfter > gib {
		t.Fatalf("清理后总量必须 ≤ 上限 %d，got %d", gib, result.LiveBytesAfter)
	}
}

// ── ⑤ 与 blob 去重任务的接缝：清理必须收敛 ref_count ────────────────────────

// TestPruneTicketAttachmentsReleasesBlobRefCount 断言「清理附件后 blob 能被真正回收」。
//
// # 这是本任务最容易漏的一条，也是与 blob 去重任务的强耦合点
//
// blob_attachment_blobs.ref_count 的语义是「有多少个附件行指向这份字节」。
// 清理附件行时若不递减（也不摘指针），会留下两种事故：
//   - 指针还在但密文没了 → 下一轮重算又把计数加回去 → blob 永不释放（泄漏）；
//   - 只减计数不摘指针 → 延迟删除误删仍在被引用的 blob（数据丢失）。
//
// 所以断言必须走完整链路：清理附件 → ref_count 归零 → 过了 24 小时缓冲期
// 后 RunTicketAttachmentBlobPurge 真的把那份字节删掉。
// 变异「清理时不碰 ref_count」会让最后那一步删 0 份，本用例必红。
func TestPruneTicketAttachmentsReleasesBlobRefCount(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	now := time.Now()
	blobSHA := strings.Repeat("c", 64)

	// 一份被引用的 blob：ref_count=1，updated_at 在两天前（已过缓冲期）。
	blob := entity.TicketAttachmentBlob{
		SHA256: blobSHA, ByteSize: 2048, MediaType: "image/png",
		Nonce:      bytes.Repeat([]byte{0x03}, 12),
		Ciphertext: bytes.Repeat([]byte{0x04}, 64),
		RefCount:   1, ScanState: entity.TicketAttachmentScanApproved,
	}
	if err := db.Create(&blob).Error; err != nil {
		t.Fatalf("create blob: %v", err)
	}
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", blobSHA).
		UpdateColumn("updated_at", now.Add(-48*time.Hour)).Error; err != nil {
		t.Fatalf("force blob updated_at: %v", err)
	}

	attachment := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 2048, SHA256: blobSHA,
		BlobSHA256: blobSHA, CreatedAt: now.Add(-200 * 24 * time.Hour),
	})

	if _, err := PruneTicketAttachmentsByAge(db, now, DefaultTicketAttachmentRetentionMaxAgeDays); err != nil {
		t.Fatalf("prune by age: %v", err)
	}
	if reloadRetentionAttachment(t, db, attachment.ID).PurgedAt == nil {
		t.Fatal("前提失败：附件应当已被清理")
	}

	var reloadedBlob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", blobSHA).First(&reloadedBlob).Error; err != nil {
		t.Fatalf("reload blob: %v", err)
	}
	if reloadedBlob.RefCount != 0 {
		t.Fatalf("清理附件后 blob 的 ref_count 必须归零（否则字节永远删不掉），got %d",
			reloadedBlob.RefCount)
	}
	// 指针也要摘掉：否则下一轮 Recompute 会把 ref_count 再加回去。
	var pointer struct {
		BlobSHA256 string `gorm:"column:blob_sha256"`
	}
	if err := db.Table(entity.TableNameTicketAttachment).
		Select("blob_sha256").Where("id = ?", attachment.ID).Take(&pointer).Error; err != nil {
		t.Fatalf("reload pointer: %v", err)
	}
	if pointer.BlobSHA256 != "" {
		t.Fatalf("清理附件后必须摘掉 blob_sha256 指向，got %q", pointer.BlobSHA256)
	}

	// 缓冲期过后的延迟删除：这一份字节应当被真正删掉。
	// 用 now+25h 作为「当前时间」，cutoff=now+1h，而 updated_at 刚被刷成 now。
	deleted, err := RunTicketAttachmentBlobPurge(db, now.Add(25*time.Hour), 0)
	if err != nil {
		t.Fatalf("purge blobs: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("ref_count 归零且过了缓冲期后，blob 字节必须被回收，实际删除 %d 份", deleted)
	}
	var remaining int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", blobSHA).
		Count(&remaining).Error; err != nil {
		t.Fatalf("count blob: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("blob 应当已被删除，仍剩 %d 行", remaining)
	}
}

// ── ⑥ 只清密文，绝不删工单与工单消息 ───────────────────────────────────────

// TestRunTicketAttachmentRetentionNeverDeletesTicketsOrMessages 是「清理不能
// 越界」的护栏：工单、工单消息、附件业务行三者一条都不能少。
//
// 变异方向（任务书判据 ⑥）：把清理改成级联删工单 → 本用例必红。
func TestRunTicketAttachmentRetentionNeverDeletesTicketsOrMessages(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket, message := seedTicketAttachmentRetentionTicket(t, db, user.ID)
	now := time.Now()

	// 三张图：两张超龄、一张未到期，覆盖两条策略同时生效。
	for i, age := range []int{300, 200, 5} {
		seedRetentionAttachment(t, db, retentionAttachmentSeed{
			TicketID: ticket.ID, MessageID: &message.ID, UserID: user.ID,
			ByteSize: 4096, SHA256: fmt.Sprintf("%064d", i),
			CreatedAt: now.Add(-time.Duration(age) * 24 * time.Hour),
		})
	}

	result, err := RunTicketAttachmentRetentionWithDB(db, now)
	if err != nil {
		t.Fatalf("run retention: %v", err)
	}
	if result.AgePurged != 2 {
		t.Fatalf("应当清理 2 张超龄附件，got %d", result.AgePurged)
	}

	var ticketCount, messageCount, attachmentCount int64
	if err := db.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Count(&ticketCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketMessage{}).Where("ticket_id = ?", ticket.ID).Count(&messageCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).
		Count(&attachmentCount).Error; err != nil {
		t.Fatal(err)
	}
	if ticketCount != 1 {
		t.Fatalf("工单必须保留，got %d 行", ticketCount)
	}
	if messageCount != 1 {
		t.Fatalf("工单消息必须保留，got %d 行", messageCount)
	}
	if attachmentCount != 3 {
		t.Fatalf("附件行必须保留（只清密文，不物理删行），got %d 行", attachmentCount)
	}
}

// ── ⑦ interval<=0 时每轮重读配置 ──────────────────────────────────────────

// TestTicketAttachmentRetentionWaitResolverReReadsConfig 断言「下一轮等多久」的
// 唯一实现点确实每次调用都重新读库：interval>0 用固定值、interval<=0 读配置、
// 改配置后立刻反映、非法配置回落默认值。
//
// 前半段（解析器本身）与后半段（循环每轮调用解析器）必须分别验证 ——
// 只看其中一半都无法证明「改配置后不重启即生效」。
func TestTicketAttachmentRetentionWaitResolverReReadsConfig(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)

	// interval > 0：调用方显式指定，不读配置。
	if got := ticketAttachmentRetentionNextWait(db, 5*time.Second); got != 5*time.Second {
		t.Fatalf("interval>0 时必须原样使用，got %s", got)
	}

	// interval <= 0：读配置。
	if err := saveSystemSettingValue(db, TicketAttachmentRetentionCleanupIntervalMinutesSettingKey, "5", "test"); err != nil {
		t.Fatal(err)
	}
	if got := ticketAttachmentRetentionNextWait(db, 0); got != 5*time.Minute {
		t.Fatalf("interval=0 时必须读库拿到 5 分钟，got %s", got)
	}
	// 改配置（不重启）后必须立刻反映 —— 这条就是「每轮重读」的可观测形态。
	if err := saveSystemSettingValue(db, TicketAttachmentRetentionCleanupIntervalMinutesSettingKey, "12", "test"); err != nil {
		t.Fatal(err)
	}
	if got := ticketAttachmentRetentionNextWait(db, 0); got != 12*time.Minute {
		t.Fatalf("改配置后必须重读到 12 分钟，got %s", got)
	}
	// 非法值（越界 / 非数字）回落默认 60 分钟，绝不返回 0（那会变成忙循环）。
	if err := saveSystemSettingValue(db, TicketAttachmentRetentionCleanupIntervalMinutesSettingKey, "0", "test"); err != nil {
		t.Fatal(err)
	}
	if got := ticketAttachmentRetentionNextWait(db, 0); got != 60*time.Minute {
		t.Fatalf("配置越界时必须回落默认 60 分钟，got %s", got)
	}
	if err := saveSystemSettingValue(db, TicketAttachmentRetentionCleanupIntervalMinutesSettingKey, "abc", "test"); err != nil {
		t.Fatal(err)
	}
	if got := ticketAttachmentRetentionNextWait(db, 0); got != 60*time.Minute {
		t.Fatalf("配置无法解析时必须回落默认 60 分钟，got %s", got)
	}
}

// TestStartTicketAttachmentRetentionCleanupRunsEveryRound 断言后台循环
// **每一轮都调用等待解析器**（而不是启动时算一次就焊死）。
//
// # 为什么需要替换解析器这个测试接缝
//
// 真实配置的粒度是**分钟**，最小 1 分钟：一个用例不可能等到第二轮。若只测
// 「解析器每次调用都重读配置」，把循环改成「启动时读一次」仍然全绿 ——
// 那正是「实现对了但接线错了」的假钉子形态。所以这里把解析器换成一个
// 1 毫秒的桩并数它被调用了几次：
//
//   - 正确实现（循环体内调用）：单次 stop 之前至少能跑 3 轮以上；
//   - 变异（把读取提到循环外）：只会被调用 1 次，用例必红。
func TestStartTicketAttachmentRetentionCleanupRunsEveryRound(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	expired := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 512,
		CreatedAt: time.Now().Add(-300 * 24 * time.Hour),
	})

	var rounds int32
	previous := ticketAttachmentRetentionWaitResolver
	ticketAttachmentRetentionWaitResolver = func(*gorm.DB, time.Duration) time.Duration {
		atomic.AddInt32(&rounds, 1)
		return time.Millisecond
	}
	t.Cleanup(func() { ticketAttachmentRetentionWaitResolver = previous })

	stop := StartTicketAttachmentRetentionCleanup(db, 0)

	// 立即跑一轮：超龄附件在**没有等待任何周期**的情况下就该被清理。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if reloadRetentionAttachment(t, db, expired.ID).PurgedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台清理必须在启动后立即跑一轮（不能先等一个周期）")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 再多等几轮，确认循环持续调用解析器。
	deadline = time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&rounds) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("循环必须每轮调用等待解析器，5s 内只调用了 %d 次", atomic.LoadInt32(&rounds))
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if got := atomic.LoadInt32(&rounds); got < 3 {
		t.Fatalf("每轮都必须重算等待时长，实际只调用 %d 次", got)
	}
}

// TestStartTicketAttachmentRetentionCleanupStopsCleanly 断言停止函数能立即返回
// （默认 60 分钟周期的场景下，stop 不能等到定时器到点）。
func TestStartTicketAttachmentRetentionCleanupStopsCleanly(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	stop := StartTicketAttachmentRetentionCleanup(db, 0)
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop 必须立即返回，不能等一个清理周期")
	}
	// 二次调用必须幂等（内部 once）。
	stop()
}

// ── 配置层：默认值与回落 ───────────────────────────────────────────────────

// TestGetTicketAttachmentRetentionSettingsDefaults 断言三个默认值是
// 用户决策的那三个数（180 天 / 20 GiB / 60 分钟）。
//
// 期望值写字面量而不是引用常量：常量漂移时本用例必须红（否则就是
// 「期望与被测共享同一常量」的恒真断言）。
func TestGetTicketAttachmentRetentionSettingsDefaults(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	settings := GetTicketAttachmentRetentionSettingsWithDB(db)
	if settings.MaxAgeDays != 180 {
		t.Fatalf("默认保留天数必须是 180，got %d", settings.MaxAgeDays)
	}
	if settings.MaxTotalGiB != 20 {
		t.Fatalf("默认总量上限必须是 20 GiB，got %d", settings.MaxTotalGiB)
	}
	if settings.CleanupIntervalMinutes != 60 {
		t.Fatalf("默认清理间隔必须是 60 分钟，got %d", settings.CleanupIntervalMinutes)
	}
	if settings.MaxTotalBytes != 20*(1<<30) {
		t.Fatalf("GiB → 字节换算必须是 1024 进制，got %d", settings.MaxTotalBytes)
	}
	// nil db 也必须回落默认值而不是 panic / 返回 0（0 会让按大小清理退化成删光）。
	if nilSettings := GetTicketAttachmentRetentionSettingsWithDB(nil); nilSettings.MaxTotalBytes != 20*(1<<30) {
		t.Fatalf("nil db 必须回落默认值，got %d", nilSettings.MaxTotalBytes)
	}
}

// TestPruneTicketAttachmentsRequiresPurgedAtColumn 断言迁移没跑时（缺 purged_at 列）
// 清理**什么都不做**而不是报错刷日志、更不能退化成「按 ciphertext 是否为空判断」。
func TestPruneTicketAttachmentsRequiresPurgedAtColumn(t *testing.T) {
	db := newTicketAttachmentRetentionTestDB(t)
	ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
	row := seedRetentionAttachment(t, db, retentionAttachmentSeed{
		TicketID: ticket.ID, UserID: 1, ByteSize: 100,
		CreatedAt: time.Now().Add(-300 * 24 * time.Hour),
	})
	// 模拟「老库还没跑迁移」：把列删掉（SQLite 3.35+ 支持 DROP COLUMN）。
	if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment + " DROP COLUMN `purged_at`").Error; err != nil {
		t.Skipf("当前 SQLite 不支持 DROP COLUMN，跳过缺列用例: %v", err)
	}
	if ticketAttachmentPurgedAtReady(db) {
		t.Fatal("前提失败：purged_at 列应当已不存在")
	}
	purged, err := PruneTicketAttachmentsByAge(db, time.Now(), 180)
	if err != nil {
		t.Fatalf("缺列时必须静默跳过而不是报错: %v", err)
	}
	if purged != 0 {
		t.Fatalf("缺列时不得清理任何行，got %d", purged)
	}
	_ = row
}
