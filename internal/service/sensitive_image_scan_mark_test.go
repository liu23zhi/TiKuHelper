package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ===================== scan_marked_by：区分「真敏感」与「额度兜底」 =====================
//
// 缺陷背景（本文件所有用例存在的理由）：
//
//	额度耗尽 + 缓存未命中时 runSensitiveImageDetection 把 result.ScanState 置成
//	entity.TicketAttachmentScanSensitive —— 与**上游真判违规**完全同值。
//	ticket_attachments 里原本没有任何列能区分这两者，于是管理员在「全部检测记录」
//	里看到一批 scan_state="sensitive" 时无法分辨：
//
//	  - 上游判违规  → 不该放行、要留存证据；
//	  - 额度耗尽兜底 → **从未检测过**，补额度后重检即可，本该是正常图片。
//
//	两类处置动作完全相反：误封正常工单图片，或漏掉真违规。
//
// 为什么每条用例都要**从库里读回**而不是只看返回值：
// 返回值是内存里的结构，本缺陷的真实形态就发生在「落库后两类行完全同形」。
// 只断 result 字段等于只测了「算得对」，没测「写对了」—— 那正是本项目记录过的
// 「实现层与接线层是两个作用域」的坑。
//
// ⚠️ 所有期望值写**字面量**（"sensitive" / "approved" / "quota" / "upstream"）：
// 若写成 entity.TicketAttachmentMarkedByQuota 之类，实现改常量时期望值同步漂移 →
// 断言恒真 → 假钉子（第 4 类假钉子形态）。

// seedAttachmentForScanMark 预置一行将被 detect 写入的附件。
//
// 必须有这一行：额度耗尽分支写库走 SyncSensitiveImageScanStateWithDB，
// 它是 `WHERE sha256 = ?` 的 UPDATE —— 没有落地的附件行就是 0 行受影响，
// 用例会「通过」但实际什么都没验证（这正是需要防的假绿）。
func seedAttachmentForScanMark(t *testing.T, db *gorm.DB, sha string) entity.TicketAttachment {
	t.Helper()
	row := entity.TicketAttachment{
		TicketID:  77,
		UserID:    101,
		MediaType: "image/png",
		ByteSize:  128,
		SHA256:    sha,
		ScanState: entity.TicketAttachmentScanPending,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	return row
}

// readScanMark 从库里读回 (scan_state, scan_marked_by)。
//
// 直接 Select 两列而不是 First(&entity.TicketAttachment{}) 再取字段：
// 本用例要证明的是**物理列里的值**，走实体只多一层可能被 gorm 默认值掩盖的路径。
func readScanMark(t *testing.T, db *gorm.DB, id int64) (scanState string, markedBy string) {
	t.Helper()
	var got struct {
		ScanState    string `gorm:"column:scan_state"`
		ScanMarkedBy string `gorm:"column:scan_marked_by"`
	}
	if err := db.Table(entity.TableNameTicketAttachment).
		Select("scan_state, scan_marked_by").
		Where("id = ?", id).
		Scan(&got).Error; err != nil {
		t.Fatalf("read scan mark of attachment %d: %v", id, err)
	}
	return got.ScanState, got.ScanMarkedBy
}

// 判据①：额度耗尽 → scan_state="sensitive" 且标记为「由额度标记」。
//
// 两个断言必须同时成立：单看 scan_state 与真敏感无法区分（这正是缺陷本身），
// 单看标记则证明不了「展示行为没变」（sensitive 仍然隐藏，属于 fail-closed 契约）。
func TestSensitiveImageQuotaExhaustedMarksAttachmentAsQuota(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	limit := DefaultSensitiveImageDailyUserLimit
	data := sensitiveImageTestData("mark-quota-exhausted")
	seedSensitiveImageQuotaUsage(t, db, 201, date, int32(limit))
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	attachment := seedAttachmentForScanMark(t, db, HashSensitiveImage(data))

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 201, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	// 前置：确认真的走到了额度耗尽分支（否则本用例测的是别的路径）。
	if result.State != SensitiveImageDetectByQuota {
		t.Fatalf("precondition: expected state %q, got %q", SensitiveImageDetectByQuota, result.State)
	}
	if *calls != 0 || result.UpstreamCalled {
		t.Fatalf("precondition: quota-exhausted must not call upstream (calls=%d)", *calls)
	}

	// 返回值侧：展示状态仍是 sensitive（fail-closed 契约未变）。
	if result.ScanState != "sensitive" {
		t.Fatalf("result.ScanState = %q, want \"sensitive\"", result.ScanState)
	}
	// 字面量 "quota"：常量漂移时这里必红。
	if result.ScanMarkedBy != "quota" {
		t.Fatalf("result.ScanMarkedBy = %q, want \"quota\"", result.ScanMarkedBy)
	}

	// ⭐ 落库侧（这才是缺陷发生的层）：两类 sensitive 必须靠这一列区分开。
	scanState, markedBy := readScanMark(t, db, attachment.ID)
	if scanState != "sensitive" {
		t.Fatalf("persisted scan_state = %q, want \"sensitive\"", scanState)
	}
	if markedBy != "quota" {
		t.Fatalf("persisted scan_marked_by = %q, want \"quota\" (quota-exhausted images were never detected)", markedBy)
	}
}

// presetScanMark 把某行的标记改成一个**非默认值**，用于构造「翻转」场景。
//
// 为什么需要它：本列的默认值是 "upstream"，若用例只在默认行上断言
// 「真敏感 → upstream」，那条断言在实现完全没写标记时**照样绿** ——
// 典型的「默认值断言」假钉子。必须先把它压成 quota，再验证它被真实结论改回来。
func presetScanMark(t *testing.T, db *gorm.DB, id int64, mark string) {
	t.Helper()
	if err := db.Table(entity.TableNameTicketAttachment).
		Where("id = ?", id).
		Update("scan_marked_by", mark).Error; err != nil {
		t.Fatalf("preset scan_marked_by of attachment %d: %v", id, err)
	}
	// 前置自检：确认真的写进去了，否则后面的断言在默认值上恒真。
	if _, got := readScanMark(t, db, id); got != mark {
		t.Fatalf("precondition: attachment %d mark = %q, want %q", id, got, mark)
	}
}

// 判据②：真敏感（上游判定）→ scan_state="sensitive" 且标记**非**额度标记。
//
// 与判据①构成对照：两行的 scan_state 完全相同，唯一区别就是标记。
// 若实现让两条路径写同一个标记值，本用例与①必有一个变红。
//
// 起始状态刻意压成 quota（模拟这张图此前被额度兜底过）：
// 这样「被真实结论覆写回 upstream」才是被验证的行为，而不是默认值的副产品。
func TestSensitiveImageUpstreamVerdictMarksAttachmentAsUpstream(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("mark-upstream-sensitive")
	sha := HashSensitiveImage(data)
	stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: true, Reason: "上游判定为敏感图片"}, nil)
	attachment := seedAttachmentForScanMark(t, db, sha)
	presetScanMark(t, db, attachment.ID, "quota")

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 202, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.State != SensitiveImageDetectSensitive {
		t.Fatalf("precondition: expected state %q, got %q", SensitiveImageDetectSensitive, result.State)
	}
	// 前置：额度够（否则这条测到的是额度分支）。
	if !result.UpstreamCalled || !result.Charged {
		t.Fatalf("precondition: expected a real upstream call, got called=%v charged=%v", result.UpstreamCalled, result.Charged)
	}

	if result.ScanState != "sensitive" {
		t.Fatalf("result.ScanState = %q, want \"sensitive\"", result.ScanState)
	}
	if result.ScanMarkedBy != "upstream" {
		t.Fatalf("result.ScanMarkedBy = %q, want \"upstream\"", result.ScanMarkedBy)
	}

	// ⭐ 落库侧：真敏感结论经 Sync 写回，必须把 quota 洗成 upstream。
	if _, err := SyncSensitiveImageScanStateWithDB(db, sha, string(result.State)); err != nil {
		t.Fatalf("sync: %v", err)
	}
	scanState, markedBy := readScanMark(t, db, attachment.ID)
	if scanState != "sensitive" {
		t.Fatalf("persisted scan_state = %q, want \"sensitive\"", scanState)
	}
	if markedBy != "upstream" {
		t.Fatalf("persisted scan_marked_by = %q, want \"upstream\" (真敏感不能被标成额度兜底)", markedBy)
	}
}

// 判据③：正常干净 → scan_state="approved"，标记为「非额度标记」。
//
// 起始标记同样压成 quota：clean 结论必须把它改回 upstream，
// 而不是靠「默认值本来就是 upstream」蒙过去。
func TestSensitiveImageCleanVerdictMarksAttachmentAsUpstream(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("mark-clean")
	sha := HashSensitiveImage(data)
	stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	attachment := seedAttachmentForScanMark(t, db, sha)
	presetScanMark(t, db, attachment.ID, "quota")

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 203, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.State != SensitiveImageDetectClean {
		t.Fatalf("precondition: expected state %q, got %q", SensitiveImageDetectClean, result.State)
	}
	if result.ScanState != "approved" {
		t.Fatalf("result.ScanState = %q, want \"approved\"", result.ScanState)
	}
	if result.ScanMarkedBy != "upstream" {
		t.Fatalf("result.ScanMarkedBy = %q, want \"upstream\"", result.ScanMarkedBy)
	}

	// 落库侧：clean 结论经 Sync 写回后必须是 approved + upstream。
	// 这里显式走一次同步（模拟上传路径/管理页的落库动作），断言两个列同时正确。
	if _, err := SyncSensitiveImageScanStateWithDB(db, sha, string(result.State)); err != nil {
		t.Fatalf("sync: %v", err)
	}
	scanState, markedBy := readScanMark(t, db, attachment.ID)
	if scanState != "approved" {
		t.Fatalf("persisted scan_state = %q, want \"approved\"", scanState)
	}
	if markedBy != "upstream" {
		t.Fatalf("persisted scan_marked_by = %q, want \"upstream\"", markedBy)
	}
}

// 判据⑦：Sync 的既有调用方行为不变 —— clean→approved、sensitive→sensitive，
// 且「别张图不受影响」这条隔离性质照旧。
//
// 既有调用方（controller 手动重判、按工单批量重检）传的是
// SensitiveImageDetectClean / SensitiveImageDetectSensitive 两个字面值；
// 本列加入不能改变它们的落库结果。
func TestSyncSensitiveImageScanStateKeepsExistingCallerSemantics(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	sha := strings.Repeat("1", 64)
	other := strings.Repeat("2", 64)
	first := seedAttachmentForScanMark(t, db, sha)
	second := seedAttachmentForScanMark(t, db, sha)
	untouched := seedAttachmentForScanMark(t, db, other)
	// 三行的标记都压成 quota（非默认值）：clean 同步若真按列写 upstream，
	// 这条翻转就是判别性的；若实现根本没写这一列，它会停在 quota 上必红。
	presetScanMark(t, db, first.ID, "quota")
	presetScanMark(t, db, second.ID, "quota")
	// untouched 那张也要非默认：下面断言它**完全没被碰过**，
	// 若它保持默认值 upstream，「没被碰」与「被重置成默认值」就分不清了。
	presetScanMark(t, db, untouched.ID, "quota")
	previous := SensitiveImageQuotaNow
	SensitiveImageQuotaNow = func() time.Time { return now }
	t.Cleanup(func() { SensitiveImageQuotaNow = previous })

	// clean：两行同 sha 都变 approved。
	updated, err := SyncSensitiveImageScanStateWithDB(db, sha, string(SensitiveImageDetectClean))
	if err != nil {
		t.Fatalf("sync clean: %v", err)
	}
	if updated != 2 {
		t.Fatalf("expected exactly the 2 attachments sharing the sha256 to be updated, got %d", updated)
	}
	for _, id := range []int64{first.ID, second.ID} {
		scanState, markedBy := readScanMark(t, db, id)
		if scanState != "approved" {
			t.Fatalf("attachment %d scan_state = %q, want \"approved\"", id, scanState)
		}
		if markedBy != "upstream" {
			t.Fatalf("attachment %d scan_marked_by = %q, want \"upstream\"", id, markedBy)
		}
	}
	// 不同 sha256 的行必须完全没被动过（连标记都不该变）。
	otherState, otherMark := readScanMark(t, db, untouched.ID)
	if otherState != "pending" {
		t.Fatalf("a different sha256 must be untouched, got scan_state %q", otherState)
	}
	if otherMark != "quota" {
		t.Fatalf("a different sha256 must keep its own mark \"quota\", got %q", otherMark)
	}

	// sensitive（既有调用方传真敏感结论）：回到 sensitive + upstream。
	updated, err = SyncSensitiveImageScanStateWithDB(db, sha, string(SensitiveImageDetectSensitive))
	if err != nil {
		t.Fatalf("sync sensitive: %v", err)
	}
	if updated != 2 {
		t.Fatalf("expected 2 rows updated on re-sync, got %d", updated)
	}
	for _, id := range []int64{first.ID, second.ID} {
		scanState, markedBy := readScanMark(t, db, id)
		if scanState != "sensitive" {
			t.Fatalf("attachment %d scan_state = %q, want \"sensitive\"", id, scanState)
		}
		if markedBy != "upstream" {
			t.Fatalf("attachment %d scan_marked_by = %q, want \"upstream\" (真敏感不能被标成额度兜底)", id, markedBy)
		}
	}
}

// 判据①的另一半：额度兜底之后管理员补了额度并重检 → 标记必须回到 upstream。
//
// 这是本列的业务价值所在：它必须能被后续真实结论**覆盖**，否则一次额度耗尽
// 会把这张图永久钉在「未检测」档（管理员补完额度也洗不掉）。
func TestQuotaMarkIsOverwrittenByLaterRealVerdict(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	limit := DefaultSensitiveImageDailyUserLimit
	data := sensitiveImageTestData("mark-quota-then-real")
	sha := HashSensitiveImage(data)
	seedSensitiveImageQuotaUsage(t, db, 204, date, int32(limit))
	stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	attachment := seedAttachmentForScanMark(t, db, sha)

	if _, err := DetectSensitiveImageWithDB(context.Background(), db, 204, 0, data, "image/png", now); err != nil {
		t.Fatalf("quota-exhausted detect: %v", err)
	}
	_, markedBy := readScanMark(t, db, attachment.ID)
	if markedBy != "quota" {
		t.Fatalf("precondition: expected quota mark, got %q", markedBy)
	}

	// 管理员手动重判（绕过额度闸门），结论 clean。
	manual, err := ManualDetectSensitiveImageWithDB(context.Background(), db, data, "image/png", now)
	if err != nil {
		t.Fatalf("manual detect: %v", err)
	}
	if _, err := SyncSensitiveImageScanStateWithDB(db, sha, string(manual.State)); err != nil {
		t.Fatalf("sync after manual detect: %v", err)
	}
	scanState, afterMark := readScanMark(t, db, attachment.ID)
	if scanState != "approved" {
		t.Fatalf("after manual recheck scan_state = %q, want \"approved\"", scanState)
	}
	if afterMark != "upstream" {
		t.Fatalf("after manual recheck scan_marked_by = %q, want \"upstream\"", afterMark)
	}
}

// 判据④的核心对照：同一张图、同一份数据，**两条路径产出的行必须可区分**。
//
// 这是「标记真能区分」的最小证明：把两次结果放在一起看，scan_state 相同、
// marked_by 不同。任何让两条路径写同一值的实现都会在此变红。
func TestQuotaAndUpstreamSensitiveRowsAreDistinguishable(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	limit := DefaultSensitiveImageDailyUserLimit

	// 第一张图：额度耗尽。
	quotaData := sensitiveImageTestData("distinguish-quota")
	seedSensitiveImageQuotaUsage(t, db, 205, date, int32(limit))
	stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: true, Reason: "上游判定为敏感图片"}, nil)
	quotaRow := seedAttachmentForScanMark(t, db, HashSensitiveImage(quotaData))
	if _, err := DetectSensitiveImageWithDB(context.Background(), db, 205, 0, quotaData, "image/png", now); err != nil {
		t.Fatalf("quota detect: %v", err)
	}

	// 第二张图：额度内、上游真判敏感。
	// 起始标记压成 quota：否则「真敏感 → upstream」可能只是默认值的副产品。
	realData := sensitiveImageTestData("distinguish-upstream")
	realRow := seedAttachmentForScanMark(t, db, HashSensitiveImage(realData))
	presetScanMark(t, db, realRow.ID, "quota")
	real, err := DetectSensitiveImageWithDB(context.Background(), db, 206, 0, realData, "image/png", now)
	if err != nil {
		t.Fatalf("real detect: %v", err)
	}
	if _, err := SyncSensitiveImageScanStateWithDB(db, HashSensitiveImage(realData), string(real.State)); err != nil {
		t.Fatalf("sync real: %v", err)
	}

	quotaState, quotaMark := readScanMark(t, db, quotaRow.ID)
	realState, realMark := readScanMark(t, db, realRow.ID)

	// 两行的展示状态**必须相同** —— 这正是缺陷的成因，也是本列存在的理由。
	if quotaState != "sensitive" || realState != "sensitive" {
		t.Fatalf("precondition: both rows must be scan_state \"sensitive\", got quota=%q real=%q", quotaState, realState)
	}
	// 而标记必须**不同**。
	if quotaMark != "quota" {
		t.Fatalf("quota-exhausted row mark = %q, want \"quota\"", quotaMark)
	}
	if realMark != "upstream" {
		t.Fatalf("upstream-verdict row mark = %q, want \"upstream\"", realMark)
	}
	if quotaMark == realMark {
		t.Fatalf("the two sensitive kinds must be distinguishable, both are %q", quotaMark)
	}
}
