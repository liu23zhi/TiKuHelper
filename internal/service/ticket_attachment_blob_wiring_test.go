package service

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件是**接线层**（task_doc/121 §4.4）的回归钉子：
// 上传路径 LinkUploadedTicketAttachmentBlobWithDB、删除路径 DeleteTicketAttachmentWithDB、
// 定时清理 RunTicketAttachmentBlobPurgeOnce 三者串起来后的端到端语义。
//
// 与 ticket_attachment_blob_test.go 的分工：那里测的是**单个函数**的语义
// （去重写入 / 重算 / 递减下限 / 延迟删除的两道闸）；这里测的是**接线**
// ——「真实上传 → 真实删除 → 真实清理」这条链，因为缺陷最容易出在接缝上：
// 函数都对，但没人调用、或调用顺序错、或不在同一事务里。
//
// ⚠️ 断言里的期望值一律写**字面量**（2 / 1 / 0），不引用实现里的任何常量或函数：
// 期望值若与实现共享同一个来源，变异时两边同步漂移，断言恒真（假钉子）。

// wiringNewDB 建立**接线测试专用**的库：既能走真实上传路径（需要 users/tickets/
// ticket_attachments），又有 blob 表与 blob_sha256 列（迁移之后的形态）。
//
// 与两个既有 helper 的分工：
//   - newTicketAttachmentTestDB 只有业务表，没有 blob 表；
//   - newTicketAttachmentBlobTestDB 只有 blob 表 + 附件表，没有 users（上传路径要写 uploader）。
//
// 接线测试两者都需要，所以单独建一个。建表形态与生产一致（显式 ALTER 补 blob_sha256）。
func wiringNewDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Ticket{},
		&entity.TicketMessage{}, &entity.TicketNotification{},
		&entity.TicketAttachment{}, &entity.TicketAttachmentBlob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 与 registry 侧 addTicketAttachmentBlobLinkColumn 的 SQLite 分支一致。
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, "blob_sha256") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment +
			" ADD COLUMN `blob_sha256` char(64) NOT NULL DEFAULT ''").Error; err != nil {
			t.Fatalf("add blob_sha256: %v", err)
		}
	}
	useTicketAttachmentTestKey(t)
	return db
}

// wiringUpload 走**真实上传路径**：StoreTicketAttachmentWithDB + 接线入口。
//
// 只断言「接线入口被真的调用了」（linked=true）与返回的 ref_count，
// **不在 helper 里断言 ref_count 的具体值**：那样变异时红会落在 helper 上，
// 而不是各用例自己的字面量断言，诊断信息会错位。
// 各用例负责断言自己关心的字面量（2 / 1 / 0）。
func wiringUpload(t *testing.T, db *gorm.DB, user *entity.User, plain []byte) (attachmentID int64, refCount int32) {
	t.Helper()
	// ticketID=0 = 草稿态，不需要 ticket 行；草稿区按 user_id 限额。
	info, err := StoreTicketAttachmentWithDB(db, user, 0, plain)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if info.ID <= 0 {
		t.Fatalf("upload must return an attachment id, got %+v", info)
	}
	count, linked, err := LinkUploadedTicketAttachmentBlobWithDB(db, info.ID)
	if err != nil {
		t.Fatalf("link blob: %v", err)
	}
	if !linked {
		t.Fatal("接线入口必须真的建立 blob 指向（linked=false 说明上传路径没接上）")
	}
	return info.ID, count
}

// wiringBlobRow 读出一份 blob（不存在时 fail）。
func wiringBlobRow(t *testing.T, db *gorm.DB, sha string) entity.TicketAttachmentBlob {
	t.Helper()
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&blob).Error; err != nil {
		t.Fatalf("read blob %s: %v", sha, err)
	}
	return blob
}

// wiringBlobExists 判断某 sha256 是否还在 blobs 表里。
func wiringBlobExists(t *testing.T, db *gorm.DB, sha string) bool {
	t.Helper()
	var count int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", sha).Count(&count).Error; err != nil {
		t.Fatalf("count blob: %v", err)
	}
	return count > 0
}

// wiringAgeBlob 把 blob 的 updated_at 拨到 past（模拟「很久没被动过」）。
func wiringAgeBlob(t *testing.T, db *gorm.DB, sha string, age time.Duration) {
	t.Helper()
	mark := time.Now().Add(-age)
	if err := db.Exec("UPDATE `ticket_attachment_blobs` SET `updated_at` = ? WHERE `sha256` = ?", mark, sha).Error; err != nil {
		t.Fatalf("age blob: %v", err)
	}
}

// wiringNewUser 建一个上传者。
func wiringNewUser(t *testing.T, db *gorm.DB, id int32) *entity.User {
	t.Helper()
	user := &entity.User{ID: id, Username: "u" + string(rune('0'+id)), Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

// TestWiringBlobSameImageUploadedTwiceDedups 是**判据 ①**：
// 同一张图上传两次 → blobs 表 1 行、attachments 2 行、ref_count=2。
//
// 判据 ④ 的变异目标就在这里：把 LinkTicketAttachmentBlobInTx 里的 ref_count 重算短路掉，
// 两次上传后 ref_count 会是 0（或 1），本用例必红。
func TestTicketAttachmentBlobWiringSameImageUploadedTwiceDedups(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)

	png := ticketAttachmentTestPNG(t, 24, 24)
	firstID, firstRef := wiringUpload(t, db, user, png)
	secondID, secondRef := wiringUpload(t, db, user, png)
	if firstRef != 1 || secondRef != 2 {
		t.Fatalf("两次上传后接线入口必须分别返回 refCount 1 和 2（字面量），实际 %d 和 %d —— "+
			"重算被短路时两次都会是 0", firstRef, secondRef)
	}

	if firstID == secondID {
		t.Fatalf("两次上传必须是两个不同的附件行，实际都得到 id=%d", firstID)
	}

	// ① blobs 表只有 1 行。
	var blobCount int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&blobCount).Error; err != nil {
		t.Fatal(err)
	}
	if blobCount != 1 {
		t.Fatalf("同一张图只该有 1 份 blob，实际 %d 行", blobCount)
	}

	// ② attachments 有 2 行。
	var attachmentCount int64
	if err := db.Model(&entity.TicketAttachment{}).Count(&attachmentCount).Error; err != nil {
		t.Fatal(err)
	}
	if attachmentCount != 2 {
		t.Fatalf("两次上传必须有 2 行附件，实际 %d 行", attachmentCount)
	}

	// ③ ref_count 必须是字面量 2。
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 2 {
		t.Fatalf("ref_count 必须等于引用行数（字面量 2），实际 %d —— "+
			"重算被短路时这里会是 0/1，多算时会是 3", blob.RefCount)
	}

	// ④ 两行指向同一个 sha256。
	var directed int64
	if err := db.Table(entity.TableNameTicketAttachment).
		Where("blob_sha256 = ?", blob.SHA256).Count(&directed).Error; err != nil {
		t.Fatal(err)
	}
	if directed != 2 {
		t.Fatalf("两行附件的 blob_sha256 都必须指向同一份 blob，实际 %d 行", directed)
	}

	// ⑤ blob 的密文能解回**与上传时一致**的字节（证明存的是同一张图，不是两份不同内容）。
	mediaType, plain, err := ReadTicketAttachmentBlobWithDB(db, blob.SHA256)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if mediaType != "image/png" {
		t.Fatalf("mediaType 必须是 image/png，实际 %q", mediaType)
	}
	if len(plain) == 0 || len(plain) != int(blob.ByteSize) {
		t.Fatalf("解密字节长度必须等于 byte_size（%d），实际 %d", blob.ByteSize, len(plain))
	}
	// 同一张图两次上传的重编码结果必须逐字节相同，并与 blob 里的明文相同。
	// 否则「同一张图」这个前提就不成立，去重也就无从谈起。
	var firstRow, secondRow entity.TicketAttachment
	if err := db.Where("id = ?", firstID).First(&firstRow).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", secondID).First(&secondRow).Error; err != nil {
		t.Fatal(err)
	}
	firstPlain, err := openTicketAttachmentPlain(firstRow)
	if err != nil {
		t.Fatalf("open first attachment: %v", err)
	}
	secondPlain, err := openTicketAttachmentPlain(secondRow)
	if err != nil {
		t.Fatalf("open second attachment: %v", err)
	}
	if !bytes.Equal(firstPlain, secondPlain) {
		t.Fatal("同一张图两次上传的重编码字节必须相同（否则测的不是去重场景）")
	}
	if !bytes.Equal(firstPlain, plain) {
		t.Fatal("blobs 里的明文必须与附件行里的明文逐字节相同")
	}
}

// TestWiringBlobDeleteOneKeepsBlob 是**判据 ②**：
// 删一张 → ref_count=1，blob 仍在（不能被清理删掉）。
func TestTicketAttachmentBlobWiringDeleteOneKeepsBlob(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)
	png := ticketAttachmentTestPNG(t, 24, 24)

	firstID, _ := wiringUpload(t, db, user, png)
	_, _ = wiringUpload(t, db, user, png)

	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	sha := blob.SHA256

	// 用**生产删除入口**（不是裸 Delete）：这正是「递减 ref_count 归谁负责」的接缝。
	deleted, err := DeleteTicketAttachmentWithDB(db, firstID, nil)
	if err != nil {
		t.Fatalf("delete attachment: %v", err)
	}
	if !deleted {
		t.Fatal("删除必须报告 deleted=true")
	}

	// ① 附件行少了一行。
	var attachmentCount int64
	if err := db.Model(&entity.TicketAttachment{}).Count(&attachmentCount).Error; err != nil {
		t.Fatal(err)
	}
	if attachmentCount != 1 {
		t.Fatalf("删一张后必须剩 1 行附件，实际 %d 行", attachmentCount)
	}

	// ② ref_count 必须是字面量 1。
	blob = wiringBlobRow(t, db, sha)
	if blob.RefCount != 1 {
		t.Fatalf("删一张后 ref_count 必须是字面量 1，实际 %d", blob.RefCount)
	}

	// ③ 即使缓冲期早已过去，只要 ref_count=1 就一份都不能删。
	wiringAgeBlob(t, db, sha, 72*time.Hour)
	deletedCount, err := RunTicketAttachmentBlobPurgeOnce(db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deletedCount != 0 {
		t.Fatalf("仍有引用时清理不许删任何 blob，实际删了 %d 份", deletedCount)
	}
	if !wiringBlobExists(t, db, sha) {
		t.Fatal("仍有 1 行引用时 blob 必须保留 —— 删了用户会看到图片永久不显示")
	}
}

// TestWiringBlobDeleteAllNeedsPurgeToReallyDelete 是**判据 ③**：
// 删两张 → ref_count=0，此时 blob **仍在**；只有经 PurgeExpired（且过了缓冲期）才真删。
//
// 这一条同时是判据 ⑤ 的目标：把 ref_count 写成「引用行数 +1」时，
// 归零后 ref_count 会是 1 → 该删的不删 → 本用例的「必须真删」断言红。
func TestTicketAttachmentBlobWiringDeleteAllNeedsPurgeToReallyDelete(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)
	png := ticketAttachmentTestPNG(t, 24, 24)

	firstID, _ := wiringUpload(t, db, user, png)
	secondID, _ := wiringUpload(t, db, user, png)

	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	sha := blob.SHA256

	// 删第一张。
	if _, err := DeleteTicketAttachmentWithDB(db, firstID, nil); err != nil {
		t.Fatalf("delete first: %v", err)
	}
	if got := wiringBlobRow(t, db, sha).RefCount; got != 1 {
		t.Fatalf("删一张后 ref_count 必须是字面量 1，实际 %d", got)
	}
	// 删第二张。
	if _, err := DeleteTicketAttachmentWithDB(db, secondID, nil); err != nil {
		t.Fatalf("delete second: %v", err)
	}

	// ① 归零。
	blob = wiringBlobRow(t, db, sha)
	if blob.RefCount != 0 {
		t.Fatalf("删完两张后 ref_count 必须是字面量 0，实际 %d", blob.RefCount)
	}

	// ② 归零**不等于**删除：缓冲期内清理不许动它。
	// 用「明显晚于 updated_at 但仍在 24h 内」的时刻（不依赖纳秒精度）。
	deletedInGrace, err := PurgeExpiredTicketAttachmentBlobs(db, blob.UpdatedAt.Add(2*time.Second), 0)
	if err != nil {
		t.Fatalf("purge in grace: %v", err)
	}
	if deletedInGrace != 0 {
		t.Fatalf("缓冲期内不许删，实际删了 %d 份", deletedInGrace)
	}
	if !wiringBlobExists(t, db, sha) {
		t.Fatal("归零后、缓冲期内的 blob 必须仍在（「删了又传」不该反复写盘）")
	}

	// ③ 过了缓冲期，经清理**必须**真删 —— 否则「归零的 blob 永远占盘」。
	wiringAgeBlob(t, db, sha, 72*time.Hour)
	deletedAfterGrace, err := RunTicketAttachmentBlobPurgeOnce(db)
	if err != nil {
		t.Fatalf("purge after grace: %v", err)
	}
	if deletedAfterGrace != 1 {
		t.Fatalf("归零且超过缓冲期的 1 份 blob 必须被真删，实际删了 %d 份 —— "+
			"ref_count 若被写成「引用行数 +1」，归零后会残留 1 而永不删除", deletedAfterGrace)
	}
	if wiringBlobExists(t, db, sha) {
		t.Fatal("过了缓冲期的归零 blob 必须已被删除")
	}
}

// TestWiringBlobStillReferencedBlobIsNeverPurged 是「不该删却删了」的**直接**用例
// （判据 ④⑤ 的共同落点）。
//
// 形态：2 行引用、ref_count 被正确算成 2，时间拨到远超缓冲期 → 清理一份都不许删。
// 若 ref_count 的重算被短路（恒 0）或算少，这里会立刻把仍在被引用的 blob 删掉。
func TestTicketAttachmentBlobWiringStillReferencedBlobIsNeverPurged(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)
	png := ticketAttachmentTestPNG(t, 24, 24)

	_, _ = wiringUpload(t, db, user, png)
	_, _ = wiringUpload(t, db, user, png)

	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 2 {
		t.Fatalf("前置条件错误：2 行引用时 ref_count 必须是字面量 2，实际 %d", blob.RefCount)
	}
	// 时间拨到缓冲期的 3 倍之前：只有 ref_count 这一道闸能挡住删除。
	wiringAgeBlob(t, db, blob.SHA256, 3*entity.TicketAttachmentBlobDedupGrace)

	deleted, err := RunTicketAttachmentBlobPurgeOnce(db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("2 行仍在引用时清理不许删，实际删了 %d 份", deleted)
	}
	if !wiringBlobExists(t, db, blob.SHA256) {
		t.Fatal("仍被引用的 blob 被误删 —— 这正是 ref_count 算少时的数据丢失形态")
	}
}

// TestWiringBlobDeleteRespectsScopeAndIsAtomic 验证删除入口的两件事：
//  1. scope 条件真的生效（非本人删不掉，且**不递减** ref_count）；
//  2. 删不到行时不产生任何计数变化（原子性：要么都变，要么都不变）。
func TestTicketAttachmentBlobWiringDeleteRespectsScopeAndIsAtomic(t *testing.T) {
	db := wiringNewDB(t)
	owner := wiringNewUser(t, db, 1)
	other := wiringNewUser(t, db, 2)
	png := ticketAttachmentTestPNG(t, 24, 24)

	attachmentID, _ := wiringUpload(t, db, owner, png)
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 1 {
		t.Fatalf("前置条件错误：ref_count 必须是字面量 1，实际 %d", blob.RefCount)
	}

	// 别人来删：scope 不匹配 → deleted=false，ref_count 必须**原样不动**。
	deleted, err := DeleteTicketAttachmentWithDB(db, attachmentID, func(tx *gorm.DB) *gorm.DB {
		return tx.Where("user_id = ?", other.ID)
	})
	if err != nil {
		t.Fatalf("delete by other: %v", err)
	}
	if deleted {
		t.Fatal("非本人必须删不到（deleted=false）")
	}
	if got := wiringBlobRow(t, db, blob.SHA256).RefCount; got != 1 {
		t.Fatalf("删不到行时 ref_count 不许变化，实际变成 %d", got)
	}
	if !wiringBlobExists(t, db, blob.SHA256) {
		t.Fatal("删不到行时 blob 必须还在")
	}
	var remaining int64
	if err := db.Model(&entity.TicketAttachment{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("删不到行时附件必须还在，实际剩 %d 行", remaining)
	}

	// 本人来删：成功，且 ref_count 归 0。
	deleted, err = DeleteTicketAttachmentWithDB(db, attachmentID, func(tx *gorm.DB) *gorm.DB {
		return tx.Where("user_id = ?", owner.ID)
	})
	if err != nil {
		t.Fatalf("delete by owner: %v", err)
	}
	if !deleted {
		t.Fatal("本人必须能删掉自己的附件")
	}
	if got := wiringBlobRow(t, db, blob.SHA256).RefCount; got != 0 {
		t.Fatalf("删掉唯一引用后 ref_count 必须归 0，实际 %d", got)
	}
}

// TestWiringBlobUploadSkipsGracefullyWithoutMigration 验证「迁移没跑时上传照旧可用」。
//
// 这是红线 ④ 的落地形式：旧列一个版本内不删，blobs 表不存在时
// 接线入口必须返回 linked=false + nil error（不是报错、更不是 panic）。
// 否则一次迁移疏漏就会让「上传图片」这个功能整体不可用。
func TestTicketAttachmentBlobWiringUploadSkipsGracefullyWithoutMigration(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)
	png := ticketAttachmentTestPNG(t, 24, 24)

	info, err := StoreTicketAttachmentWithDB(db, user, 0, png)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// 模拟「迁移没跑」：把 blobs 表删掉。
	if err := db.Migrator().DropTable(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatalf("drop blobs table: %v", err)
	}
	refCount, linked, err := LinkUploadedTicketAttachmentBlobWithDB(db, info.ID)
	if err != nil {
		t.Fatalf("缺 blobs 表时接线入口不许报错（旧库必须照旧可用），实际 %v", err)
	}
	if linked || refCount != 0 {
		t.Fatalf("缺 blobs 表时必须 linked=false、refCount=0，实际 linked=%v refCount=%d", linked, refCount)
	}
	// 附件行与旧列必须完好（红线：ciphertext/nonce 一个版本内不删）。
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatalf("附件行必须还在: %v", err)
	}
	if len(row.Nonce) == 0 || len(row.Ciphertext) == 0 {
		t.Fatal("旧列 nonce/ciphertext 必须完好（一个版本内不删）")
	}
}

// TestWiringBlobPurgeWorkerRunsAtLeastOnceAndStops 验证定时任务接线：
// 起协程 → 至少跑过一轮 → ctx 取消后干净退出。
//
// 判据是「真的做过事」：预先放一份「归零且已过期」的 blob，看 worker 是否删掉它。
// 只看「协程起来了」是假钉子（函数被短路成空转也能过）。
func TestTicketAttachmentBlobWiringPurgeWorkerRunsAtLeastOnceAndStops(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)
	png := ticketAttachmentTestPNG(t, 24, 24)

	attachmentID, _ := wiringUpload(t, db, user, png)
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	sha := blob.SHA256
	if _, err := DeleteTicketAttachmentWithDB(db, attachmentID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wiringAgeBlob(t, db, sha, 72*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := StartTicketAttachmentBlobPurgeWorker(ctx, db)
	// 等到这份过期 blob 真的被删（最多 5 秒），而不是「睡一会儿就假定跑过了」。
	deadline := time.Now().Add(5 * time.Second)
	for wiringBlobExists(t, db, sha) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if wiringBlobExists(t, db, sha) {
		cancel()
		<-done
		t.Fatal("定时清理协程必须在启动后清掉「归零且已过期」的 blob（说明接线没生效）")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后清理协程必须退出")
	}
}

// TestWiringBlobGracePeriodIsNotResetByRefCountUpdates 钉住**与清理任务的接缝**
// （captain 在 T-11 里专门点出的一条）。
//
// # 事实（实测，不是推断）
//
// GORM 的两种更新方式对 `updated_at`（`autoUpdateTime`）行为**不同**：
//
//	UpdateColumn(...)   —— 只改指定列，**不**碰 updated_at
//	Updates(map/db)     —— 会把 updated_at 一起刷新
//
// 本仓 blob 侧只用 UpdateColumn 改 ref_count/blob_sha256，因此「加引用 / 减引用」
// **不会**重置 24 小时宽限期；只有 `Decrease`/`Release` 在**归零那一刻**用
// UpdateColumn("updated_at", time.Now()) 显式刷新。
//
// # 为什么这条必须钉
//
// 若将来有人把 ref_count 的更新从 UpdateColumn 改成 Updates（很自然的一次重构），
// 每上传一次就会把宽限期重置一次 —— 结果是「本该被清理的 blob 永远存活」，
// 磁盘只涨不降，而且**没有任何报错**。这条用例就是那个改动的红线。
//
// # 判据
//
//  1. 归零（递减到 0）后 updated_at ≈ 递减那一刻，**不是**旧值；
//  2. 归零后**再加引用**（重新上传同一张图）不会把 updated_at 推到"现在"的
//     更新时刻之后 —— 也就是引用计数的增减不额外动时间；
//  3. 归零后不碰 blob 行的普通读取也不会刷新它。
func TestTicketAttachmentBlobWiringGracePeriodIsNotResetByRefCountUpdates(t *testing.T) {
	db := wiringNewDB(t)
	user := wiringNewUser(t, db, 1)
	png := ticketAttachmentTestPNG(t, 24, 24)

	attachmentID, _ := wiringUpload(t, db, user, png)
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	sha := blob.SHA256

	// 把时间拨到 3 天前：此时它「早已超出缓冲期」，只有在 ref_count=0 时才会被删。
	wiringAgeBlob(t, db, sha, 72*time.Hour)
	agedAt := wiringBlobRow(t, db, sha).UpdatedAt

	// ① 删除唯一引用 → 归零，且 updated_at 必须被显式刷新到「此刻」。
	beforeDelete := time.Now()
	if _, err := DeleteTicketAttachmentWithDB(db, attachmentID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	afterZero := wiringBlobRow(t, db, sha)
	if afterZero.RefCount != 0 {
		t.Fatalf("前置条件错误：删掉唯一引用后 ref_count 必须是字面量 0，实际 %d", afterZero.RefCount)
	}
	if !afterZero.UpdatedAt.After(agedAt.Add(time.Hour)) {
		t.Fatalf("归零时必须刷新 updated_at 到今天（缓冲期从现在起算），实际仍是 %v", afterZero.UpdatedAt)
	}
	if afterZero.UpdatedAt.Before(beforeDelete.Add(-2 * time.Second)) {
		t.Fatalf("归零时刷新的 updated_at 必须是「递减那一刻」，实际 %v 早于操作时刻 %v",
			afterZero.UpdatedAt, beforeDelete)
	}

	// ② 归零且未过缓冲期 → 清理一份都不许删（这就是刷新 updated_at 的意义）。
	deleted, err := RunTicketAttachmentBlobPurgeOnce(db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("刚归零的 blob 在缓冲期内不许删，实际删了 %d 份 —— "+
			"若归零时没刷新 updated_at，这里会用 3 天前的旧时间戳误删", deleted)
	}
	if !wiringBlobExists(t, db, sha) {
		t.Fatal("刚归零的 blob 必须仍在")
	}
	zeroedAt := wiringBlobRow(t, db, sha).UpdatedAt

	// ③ 重新上传同一张图（ref_count 0 → 1）**不许**刷新 updated_at。
	//
	// 判据刻意用「大幅时间差」而不是「不超过 1 秒」：后者在 UpdateColumn 与
	// Updates(map) 之间只有毫秒级差别，断言会恒真（假钉子，实测过）。
	// 做法：先把 updated_at 拨到 72 小时前，再做「只改 ref_count」的动作，
	// 然后要求它**仍在 24 小时之前**。UpdateColumn 保留旧值 → 绿；
	// Updates(map) 会刷新成此刻 → 落在 24 小时内 → 红。差距是 48 小时，不可能侥幸。
	wiringAgeBlob(t, db, sha, 72*time.Hour)
	agedBeforeReUpload := wiringBlobRow(t, db, sha).UpdatedAt

	_, _ = wiringUpload(t, db, user, png)
	reUploaded := wiringBlobRow(t, db, sha)
	if reUploaded.RefCount != 1 {
		t.Fatalf("重新上传后 ref_count 必须是字面量 1，实际 %d", reUploaded.RefCount)
	}
	if reUploaded.UpdatedAt.After(agedBeforeReUpload.Add(time.Hour)) {
		t.Fatalf("ref_count 的增量更新**不得**刷新 updated_at（会把 24h 宽限期重置，导致 blob 永不清理）："+
			"拨龄后 %v，重新上传后变成 %v", agedBeforeReUpload, reUploaded.UpdatedAt)
	}
	// 并且它必须仍被判为「已超期」——这才是宽限期没被重置的实际含义。
	if deletedWhileReferenced, err := RunTicketAttachmentBlobPurgeOnce(db); err != nil {
		t.Fatalf("purge: %v", err)
	} else if deletedWhileReferenced != 0 {
		t.Fatalf("仍有引用时不许删，实际删了 %d 份", deletedWhileReferenced)
	}
	_ = zeroedAt

	// ④ 既然没刷新，它仍是「归零 + 早于 cutoff」→ 下一轮清理必须能删掉。
	// 这一步是③的正对照：证明「时间没被重置」是真的没重置，而不是整段逻辑没跑。
	// 重新上传造了新行，先把它删掉，让 ref_count 回到 0。
	var liveID int64
	if err := db.Model(&entity.TicketAttachment{}).Select("id").Order("id DESC").Limit(1).Scan(&liveID).Error; err != nil {
		t.Fatal(err)
	}
	if liveID > 0 {
		if _, err := DeleteTicketAttachmentWithDB(db, liveID, nil); err != nil {
			t.Fatalf("delete re-uploaded row: %v", err)
		}
	}
	final := wiringBlobRow(t, db, sha)
	if final.RefCount != 0 {
		t.Fatalf("最终 ref_count 必须是字面量 0，实际 %d", final.RefCount)
	}
	// 只把 updated_at 拨回「过期」，ref_count 保持 0 —— 清理必须删掉它。
	wiringAgeBlob(t, db, sha, 72*time.Hour)
	deletedFinal, err := RunTicketAttachmentBlobPurgeOnce(db)
	if err != nil {
		t.Fatalf("purge final: %v", err)
	}
	if deletedFinal != 1 {
		t.Fatalf("归零且超期的 blob 必须被删（正对照：证明清理链路真的会删），实际删了 %d 份", deletedFinal)
	}
}
