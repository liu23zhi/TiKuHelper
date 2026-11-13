package controller

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 本文件是**控制器层接线**的回归钉子（task_doc/121 §4.4）：
// 证明 HTTP 上传接口真的把附件挂到了 blobs 表（而不是「service 里有函数但没人调」），
// 且删除接口真的在同一个事务里递减 ref_count。
//
// 这一层必须单独测：service 层的用例直接调接线函数，所以「controller 忘了调用」
// 在那些用例里完全看不出来 —— 这正是本项目最常见的一类缺陷形态（代码都在、只差接线）。
//
// ⚠️ 期望值一律写字面量（1 / 2 / 0），不引用实现常量，避免变异时同步漂移成恒真。

// newBlobWiringControllerDB 建立带 blob 表与 blob_sha256 列的控制器测试库。
func newBlobWiringControllerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newTicketAttachmentControllerTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate blob table: %v", err)
	}
	// 与 registry 侧 addTicketAttachmentBlobLinkColumn 的 SQLite 分支一致：
	// 显式 ALTER 补列，不用 AutoMigrate（那会合并建表语句、把表结构改坏）。
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, "blob_sha256") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment +
			" ADD COLUMN `blob_sha256` char(64) NOT NULL DEFAULT ''").Error; err != nil {
			t.Fatalf("add blob_sha256: %v", err)
		}
	}
	return db
}

// uploadViaController 走**真实 HTTP handler** 上传一张图，返回附件 ID。
func uploadViaController(t *testing.T, db *gorm.DB, user *entity.User, ticketID int64, png []byte) int64 {
	t.Helper()
	target := "/ticket-attachments?ticketId=" + strconv.FormatInt(ticketID, 10)
	recorder := performUploadRequest(t, user, target, "file", "shot.png", png, UploadTicketAttachment)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data service.TicketAttachmentInfo `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body.String())
	}
	if payload.Data.ID <= 0 {
		t.Fatalf("upload must return an attachment id, got %+v", payload.Data)
	}
	return payload.Data.ID
}

// TestUploadTicketAttachmentWiresBlobDedup 是**判据 ①在 HTTP 层的版本**：
// 同一张图经上传接口传两次 → blobs 1 行、attachments 2 行、ref_count=2、两行都指向同一 sha。
//
// 变异判据（判据 ④）：把 LinkTicketAttachmentBlobInTx 里的 ref_count 重算短路掉，
// 或把 UploadTicketAttachment 里的接线调用删掉，本用例必红。
func TestUploadTicketAttachmentWiresBlobDedup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlobWiringControllerDB(t)
	withTicketControllerDB(t, db)

	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	png := ticketAttachmentControllerPNG(t)

	firstID := uploadViaController(t, db, user, ticket.ID, png)
	secondID := uploadViaController(t, db, user, ticket.ID, png)
	if firstID == secondID {
		t.Fatalf("两次上传必须是两个附件行，实际都得到 id=%d", firstID)
	}

	// ① blobs 表 1 行（去重生效）。
	var blobCount int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&blobCount).Error; err != nil {
		t.Fatal(err)
	}
	if blobCount != 1 {
		t.Fatalf("同一张图经上传接口传两次只该有 1 份 blob，实际 %d 行 —— "+
			"blobs 表是 0 说明 controller 根本没调接线入口", blobCount)
	}

	// ② attachments 2 行。
	var attachmentCount int64
	if err := db.Model(&entity.TicketAttachment{}).Count(&attachmentCount).Error; err != nil {
		t.Fatal(err)
	}
	if attachmentCount != 2 {
		t.Fatalf("两次上传必须有 2 行附件，实际 %d 行", attachmentCount)
	}

	// ③ ref_count 是字面量 2。
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 2 {
		t.Fatalf("ref_count 必须是字面量 2，实际 %d（重算被短路会是 0/1，重复加一会是 3+）", blob.RefCount)
	}

	// ④ 两行附件的 blob_sha256 都指向它。
	var directed int64
	if err := db.Table(entity.TableNameTicketAttachment).
		Where("blob_sha256 = ?", blob.SHA256).Count(&directed).Error; err != nil {
		t.Fatal(err)
	}
	if directed != 2 {
		t.Fatalf("两行附件都必须指向同一份 blob，实际 %d 行", directed)
	}

	// ⑤ 旧列必须完好（红线：ciphertext/nonce 一个版本内不删）。
	var rows []entity.TicketAttachment
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if len(row.Nonce) == 0 || len(row.Ciphertext) == 0 {
			t.Fatalf("旧列 nonce/ciphertext 必须完好（id=%d）—— 一个版本内不删", row.ID)
		}
	}
}

// TestDeleteTicketAttachmentWiresRefCountDecrease 是**判据 ②③在 HTTP 层的版本**：
// 经删除接口删一张 → ref_count=1 且 blob 仍在；再删一张 → ref_count=0 但 blob 仍不被立刻删。
//
// 变异判据（判据 ⑤）：把 ref_count 写成「引用行数 +1」，第一次删完会是 2、
// 第二次删完会是 1 —— 本用例的两处字面量断言都会红。
func TestDeleteTicketAttachmentWiresRefCountDecrease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlobWiringControllerDB(t)
	withTicketControllerDB(t, db)

	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	png := ticketAttachmentControllerPNG(t)

	firstID := uploadViaController(t, db, user, ticket.ID, png)
	secondID := uploadViaController(t, db, user, ticket.ID, png)

	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	sha := blob.SHA256
	if blob.RefCount != 2 {
		t.Fatalf("前置条件错误：上传两张后 ref_count 必须是字面量 2，实际 %d", blob.RefCount)
	}

	deleteViaController := func(id int64) {
		t.Helper()
		params := gin.Params{{Key: "attachmentId", Value: strconv.FormatInt(id, 10)}}
		recorder := performTicketControllerRequestWithParams(t, user, http.MethodDelete,
			"/ticket-attachments/id", params, DeleteTicketAttachment)
		if recorder.Code != http.StatusOK {
			t.Fatalf("delete id=%d status=%d body=%s", id, recorder.Code, recorder.Body.String())
		}
	}

	// 删第一张 → ref_count=1，blob 仍在。
	deleteViaController(firstID)
	var afterFirst entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&afterFirst).Error; err != nil {
		t.Fatalf("删一张后 blob 必须仍在: %v", err)
	}
	if afterFirst.RefCount != 1 {
		t.Fatalf("删一张后 ref_count 必须是字面量 1，实际 %d", afterFirst.RefCount)
	}

	// 删第二张 → ref_count=0，但 blob **此时还在**（要等缓冲期 + 清理）。
	deleteViaController(secondID)
	var afterSecond entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", sha).First(&afterSecond).Error; err != nil {
		t.Fatalf("归零后 blob 不该立刻消失（要等缓冲期）: %v", err)
	}
	if afterSecond.RefCount != 0 {
		t.Fatalf("删完两张后 ref_count 必须是字面量 0，实际 %d —— "+
			"写成「引用行数 +1」时这里会是 1，blob 将永远不删", afterSecond.RefCount)
	}

	// 附件行全部删光。
	var remaining int64
	if err := db.Model(&entity.TicketAttachment{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("两张都删掉后必须没有附件行，实际 %d 行", remaining)
	}
}

// TestDeleteTicketAttachmentRejectsOtherUsersAttachment 验证删除接口的归属判定：
// 别人的图删不到（404），且**不产生任何 ref_count 变化**（原子性）。
func TestDeleteTicketAttachmentRejectsOtherUsersAttachment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlobWiringControllerDB(t)
	withTicketControllerDB(t, db)

	owner := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	other := &entity.User{ID: 2, Username: "bob", Role: "user", Status: "enabled"}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, owner.ID)
	attachmentID := uploadViaController(t, db, owner, ticket.ID, ticketAttachmentControllerPNG(t))

	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob).Error; err != nil {
		t.Fatal(err)
	}
	if blob.RefCount != 1 {
		t.Fatalf("前置条件错误：ref_count 必须是字面量 1，实际 %d", blob.RefCount)
	}

	params := gin.Params{{Key: "attachmentId", Value: strconv.FormatInt(attachmentID, 10)}}
	recorder := performTicketControllerRequestWithParams(t, other, http.MethodDelete,
		"/ticket-attachments/id", params, DeleteTicketAttachment)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("别人的图必须删不到（404），实际 status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// ref_count 不许变。
	var after entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", blob.SHA256).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.RefCount != 1 {
		t.Fatalf("删不到行时 ref_count 不许变化，实际变成 %d", after.RefCount)
	}
	var remaining int64
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", attachmentID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("删不到行时附件必须还在")
	}

	// 管理员可以删（同一接口，身份不同）：证明上面的 404 来自归属判定而不是接口坏了。
	admin := &entity.User{ID: 3, Username: "root", Role: "admin", Status: "enabled"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	recorder = performTicketControllerRequestWithParams(t, admin, http.MethodDelete,
		"/ticket-attachments/id", params, DeleteTicketAttachment)
	if recorder.Code != http.StatusOK {
		t.Fatalf("管理员必须能删任何图，实际 status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var adminAfter entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", blob.SHA256).First(&adminAfter).Error; err != nil {
		t.Fatal(err)
	}
	if adminAfter.RefCount != 0 {
		t.Fatalf("管理员删掉唯一引用后 ref_count 必须归 0，实际 %d", adminAfter.RefCount)
	}
}

// TestUploadTicketAttachmentSurvivesMissingBlobTable 验证红线 ④ 的边界：
// blobs 表不存在（迁移没跑）时，上传接口**照旧 201**，附件行与旧列完好。
//
// 若接线失败会让上传整体回滚，这条会红 —— 那是必须避免的回归：
// 一次迁移疏漏不该让「上传图片」这个功能整体不可用。
func TestUploadTicketAttachmentSurvivesMissingBlobTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlobWiringControllerDB(t)
	withTicketControllerDB(t, db)

	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	if err := db.Migrator().DropTable(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatalf("drop blobs table: %v", err)
	}

	attachmentID := uploadViaController(t, db, user, ticket.ID, ticketAttachmentControllerPNG(t))
	if attachmentID <= 0 {
		t.Fatal("缺 blobs 表时上传必须照旧返回附件 ID")
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", attachmentID).First(&row).Error; err != nil {
		t.Fatalf("附件行必须落库: %v", err)
	}
	if len(row.Nonce) == 0 || len(row.Ciphertext) == 0 {
		t.Fatal("旧列 nonce/ciphertext 必须完好（红线：一个版本内不删）")
	}
	// 用裸查询读 blob_sha256（entity.TicketAttachment 不带该字段，理由见 entity 注释：
	// 加进既有实体会让 AutoMigrate 试图改动已存在的表结构）。
	var directed int64
	if err := db.Table(entity.TableNameTicketAttachment).
		Where("id = ? AND COALESCE(blob_sha256,'') <> ''", attachmentID).Count(&directed).Error; err != nil {
		t.Fatal(err)
	}
	if directed != 0 {
		t.Fatal("缺 blobs 表时不该写入 blob_sha256 指向")
	}
}

// TestDeleteTicketAttachmentMissingIDReturnsNotFound 验证已删除的 ID 再删返回 404 而不是 200。
func TestDeleteTicketAttachmentMissingIDReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlobWiringControllerDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	params := gin.Params{{Key: "attachmentId", Value: "9999"}}
	recorder := performTicketControllerRequestWithParams(t, user, http.MethodDelete,
		"/ticket-attachments/id", params, DeleteTicketAttachment)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的附件必须 404，实际 status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
