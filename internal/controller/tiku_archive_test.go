package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件补 task_doc/45 §0.26.2（Q77-1 题库软删除）的回归钉子。
//
// r11-tikuarch 断线前只写了两条（迁移列/索引、search 排除归档），
// 这里补齐「归档 / 恢复 / 原因校验」三条行为级用例。

func openTikuArchiveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:tiku-archive-" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.Tiku{}, &entity.Log{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	// 归档四列由 ensureTikuArchiveSchema 用显式 ALTER 补齐（生产路径），
	// 测试里跑同一个函数，保证测的是真实迁移而不是 AutoMigrate 的隐式行为。
	// 归档四列由 ensureTikuArchiveSchema 用显式 ALTER 补齐（生产路径）。
	// registry 里那是包内私有函数，controller 测试无法直接调；
	// 用 entity.TikuArchive 声明同一张表的四列做等价补列（三列 + deleted_at）。
	if err := db.AutoMigrate(&entity.TikuArchive{}); err != nil {
		t.Fatalf("migrate tiku archive columns: %v", err)
	}
	dao.SetDefault(db)
	return db
}

// TestArchiveTikuQuestionWritesSnapshotAndHidesRow 覆盖：
//   - 归档后默认查询看不到（软删除生效）
//   - Unscoped 仍能查到（数据没真删）
//   - deleted_at / archived_reason / archived_by_user_id 三个归档列都写对了
//   - logs 表留下一行 Module=tiku / Action=59 的记录，OldAnswer 能解析回原内容
func TestArchiveTikuQuestionWritesSnapshotAndHidesRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTikuArchiveTestDB(t)

	question := &entity.Tiku{Question: "归档用例题目", Answer: "归档用例答案", Type: 0}
	if err := db.Create(question).Error; err != nil {
		t.Fatalf("create tiku: %v", err)
	}
	operator := &entity.User{ID: 7, Username: "admin7", Role: "admin", Status: "enabled"}

	archived, err := archiveTikuQuestion(operator, question.ID, "重复题库")
	if err != nil {
		t.Fatalf("archive tiku: %v", err)
	}
	if archived == nil || archived.ID != question.ID {
		t.Fatalf("archive must return the archived question, got %+v", archived)
	}

	// 默认查询必须看不到（gorm.DeletedAt 生效）。
	var visible int64
	if err := db.Model(&entity.Tiku{}).Where("id = ?", question.ID).Count(&visible).Error; err != nil {
		t.Fatalf("count visible: %v", err)
	}
	if visible != 0 {
		t.Fatalf("archived question must be hidden from default query, got count %d", visible)
	}

	// Unscoped 必须能看到（数据没真删）。
	var row entity.Tiku
	if err := db.Unscoped().Where("id = ?", question.ID).Take(&row).Error; err != nil {
		t.Fatalf("archived row must still exist: %v", err)
	}

	// 三个归档列声明在 entity.TikuArchive 上（同一张表），用它读。
	var meta entity.TikuArchive
	if err := db.Unscoped().Where("id = ?", question.ID).Take(&meta).Error; err != nil {
		t.Fatalf("archived meta must be readable: %v", err)
	}
	if meta.DeletedAt == nil {
		t.Fatal("archived question must carry a non-null deleted_at")
	}
	// 预设「重复题库」归一化为稳定代码 duplicate（见 archiveReasonCode），
	// 不是笼统的 admin_delete —— admin_delete 只用于自由文本。
	if meta.ArchivedReason == nil || *meta.ArchivedReason != "duplicate" {
		t.Fatalf("expected archived reason %q, got %v", "duplicate", meta.ArchivedReason)
	}
	// 操作者是管理员，不是题库本身。
	if meta.ArchivedByUserID == nil || *meta.ArchivedByUserID != operator.ID {
		t.Fatalf("expected archived by %d, got %v", operator.ID, meta.ArchivedByUserID)
	}

	// 日志必须留下可重建的快照。
	var entry entity.Log
	if err := db.Where("module = ? AND action = ? AND qid = ?", "tiku", TikuActionArchive, question.ID).
		Take(&entry).Error; err != nil {
		t.Fatalf("archive must write a log row: %v", err)
	}
	if entry.UserID != operator.ID {
		t.Fatalf("log UserID must be the operator %d, got %d", operator.ID, entry.UserID)
	}
	if !strings.Contains(entry.Description, "重复题库") {
		t.Fatalf("log description must carry the reason, got %q", entry.Description)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(entry.OldAnswer), &snapshot); err != nil {
		t.Fatalf("OldAnswer must be valid JSON: %v（值=%q）", err, entry.OldAnswer)
	}
	if snapshot["reason"] != "重复题库" {
		t.Fatalf("snapshot must carry the reason, got %v", snapshot["reason"])
	}
	if _, ok := snapshot["before"]; !ok {
		t.Fatalf("snapshot must carry a before object, got keys %v", snapshot)
	}
}

// TestRestoreArchivedTikuQuestionClearsArchiveColumns 覆盖恢复：
//   - 恢复后默认查询又能看到
//   - 三个归档列全部清空
//   - 留下 Action=60 的日志
func TestRestoreArchivedTikuQuestionClearsArchiveColumns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTikuArchiveTestDB(t)

	question := &entity.Tiku{Question: "恢复用例题目", Answer: "恢复用例答案", Type: 0}
	if err := db.Create(question).Error; err != nil {
		t.Fatalf("create tiku: %v", err)
	}
	operator := &entity.User{ID: 8, Username: "admin8", Role: "admin", Status: "enabled"}

	if _, err := archiveTikuQuestion(operator, question.ID, "误删"); err != nil {
		t.Fatalf("archive tiku: %v", err)
	}
	if _, err := restoreArchivedTikuQuestion(operator, question.ID, "确认误删"); err != nil {
		t.Fatalf("restore tiku: %v", err)
	}

	var visible int64
	if err := db.Model(&entity.Tiku{}).Where("id = ?", question.ID).Count(&visible).Error; err != nil {
		t.Fatalf("count visible: %v", err)
	}
	if visible != 1 {
		t.Fatalf("restored question must be visible again, got count %d", visible)
	}

	var row entity.Tiku
	if err := db.Where("id = ?", question.ID).Take(&row).Error; err != nil {
		t.Fatalf("take restored: %v", err)
	}
	if row.DeletedAt.Valid {
		t.Fatal("restored question must have a null deleted_at")
	}

	// 三个归档列声明在 entity.TikuArchive 上（同一张表）。
	var meta entity.TikuArchive
	if err := db.Unscoped().Where("id = ?", question.ID).Take(&meta).Error; err != nil {
		t.Fatalf("restored meta must be readable: %v", err)
	}
	if meta.ArchivedReason != nil && strings.TrimSpace(*meta.ArchivedReason) != "" {
		t.Fatalf("restored question must clear archived_reason, got %q", *meta.ArchivedReason)
	}
	if meta.ArchivedByUserID != nil && *meta.ArchivedByUserID != 0 {
		t.Fatalf("restored question must clear archived_by_user_id, got %d", *meta.ArchivedByUserID)
	}

	var entry entity.Log
	if err := db.Where("module = ? AND action = ? AND qid = ?", "tiku", TikuActionRestore, question.ID).
		Take(&entry).Error; err != nil {
		t.Fatalf("restore must write a log row: %v", err)
	}
	if entry.UserID != operator.ID {
		t.Fatalf("restore log UserID must be the operator %d, got %d", operator.ID, entry.UserID)
	}
}

// TestArchiveReasonRuneLengthGatesShortReasons 覆盖原因强制校验（Q77-6 选项 C）：
// archivedReasonRuneLength 是归档 handler 的唯一闸门，它必须按「去空格后的
// **字符数**」判定（不是字节数 —— 中文一个字符 3 字节，按字节算会让单字原因
// 也通过）。
func TestArchiveReasonRuneLengthGatesShortReasons(t *testing.T) {
	cases := []struct {
		reason string
		pass   bool
	}{
		{"", false},
		{"   ", false},
		{"删", false},     // 1 个字符，不足 2
		{"  删  ", false}, // 去空格后仍是 1 个字符
		{"重复", true},     // 2 个字符，恰好达标
		{"重复题库", true},
		{" 重复题库 ", true}, // 首尾空格应被裁掉，不影响判定
	}
	for _, c := range cases {
		got := archivedReasonRuneLength(c.reason)
		if c.pass && got < archiveReasonMinLength {
			t.Fatalf("reason %q must pass (len=%d, need >=%d)", c.reason, got, archiveReasonMinLength)
		}
		if !c.pass && got >= archiveReasonMinLength {
			t.Fatalf("reason %q must be rejected (len=%d, need <%d)", c.reason, got, archiveReasonMinLength)
		}
	}
	// 中文按字符数计：4 个汉字应得 4，而不是 12 字节。
	if got := archivedReasonRuneLength("重复题库"); got != 4 {
		t.Fatalf("archivedReasonRuneLength must count runes not bytes, got %d for 4 汉字", got)
	}
}

// TestArchiveReasonCodeNormalizesPresets 覆盖原因归一化：
// 前端下拉的预设文本要落成可筛选的分类代码，自由文本不丢
// （自由文本本身写进 logs.Description 与快照 reason）。
func TestArchiveReasonCodeNormalizesPresets(t *testing.T) {
	if got := archiveReasonCode("重复题库"); got != "duplicate" {
		t.Fatalf("preset 重复题库 must normalize to duplicate, got %q", got)
	}
	if got := archiveReasonCode("违规内容"); got != "violation" {
		t.Fatalf("preset 违规内容 must normalize to violation, got %q", got)
	}
	if got := archiveReasonCode("内容错误"); got != "content_error" {
		t.Fatalf("preset 内容错误 must normalize to content_error, got %q", got)
	}
	if got := archiveReasonCode("其他"); got != "other" {
		t.Fatalf("preset 其他 must normalize to other, got %q", got)
	}
	// 未知自由文本必须落到 admin_delete（不能是空串，否则按 reason 筛会漏）。
	if got := archiveReasonCode("某个临时理由"); got != entity.TikuArchivedReasonAdminDelete {
		t.Fatalf("free-text reason must normalize to %q, got %q",
			entity.TikuArchivedReasonAdminDelete, got)
	}
}

// TestArchiveTikuKeepsRowCountStable 是「不真删除」的行为级钉子：
// 归档前后 tikus 的物理行数必须不变。这条比断言 deleted_at 非空更贴近用户要求
// （用户的原话是「数据不可真删除」）。
func TestArchiveTikuKeepsRowCountStable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTikuArchiveTestDB(t)

	question := &entity.Tiku{Question: "行数用例题目", Answer: "答案", Type: 0}
	if err := db.Create(question).Error; err != nil {
		t.Fatalf("create tiku: %v", err)
	}

	var before int64
	if err := db.Unscoped().Model(&entity.Tiku{}).Count(&before).Error; err != nil {
		t.Fatalf("count before: %v", err)
	}

	operator := &entity.User{ID: 9, Username: "admin9", Role: "admin", Status: "enabled"}
	if _, err := archiveTikuQuestion(operator, question.ID, "行数用例"); err != nil {
		t.Fatalf("archive tiku: %v", err)
	}

	var after int64
	if err := db.Unscoped().Model(&entity.Tiku{}).Count(&after).Error; err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("归档不得物理删除：Unscoped 行数必须不变，before=%d after=%d", before, after)
	}
}
