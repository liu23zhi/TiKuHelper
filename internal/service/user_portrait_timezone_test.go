package service

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 本文件钉住画像「时段/星期分布」的**时区口径**。
//
// # 为什么既有用例抓不到
//
// user_portrait_test.go 的 TestPortraitHourBucketsUseWallClockHour 用
// `time.Date(..., time.Local)` 起种子 —— GORM 按**进程本地时区**序列化，
// 开发机是 CST 就写成 `2026-11-13 14:00:00+08:00`，`substr(...)` 恰好栽对这种形态。
// CI 跑 UTC 时种子跟着变 UTC 文本，于是 substr 又恰好「按 UTC 取小时」，
// 而断言期望值也是从同一个 UTC 种子推出来的 —— **自证式通过**。
//
// 真实生产数据不是这样：本机库 billing_records.created_at 是
// `2026-05-08 13:15:29.939932211+00:00`（**UTC**），substr 截出来的就是 UTC 小时。
// 实测 53 行里 **52 行的小时值错误、1 行星期错误**，画像页把「晚上 23 点」的峰值
// 显示成「下午 15 点」，形状看着完全合理。
//
// 所以本文件的种子**必须绕过 GORM 直接写原始时间文本**，显式构造 `+00:00` 行。

// TestPortraitHourBucketsNormalizeUTCOffset 是核心判据。
func TestPortraitHourBucketsNormalizeUTCOffset(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 9101, "portrait-tz-hour")

	// 三条 UTC 行，换算到上海后分别落 21 / 23 / 13 点。
	//
	//	UTC 13:00 → 上海 21:00
	//	UTC 15:00 → 上海 23:00
	//	UTC 05:00 → 上海 13:00
	insertBillingWithRawCreatedAt(t, db, user.ID, 1, "2026-11-14 13:00:00+00:00")
	insertBillingWithRawCreatedAt(t, db, user.ID, 2, "2026-11-14 15:00:00+00:00")
	insertBillingWithRawCreatedAt(t, db, user.ID, 4, "2026-11-14 05:00:00+00:00")

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}
	got := make(map[int]PortraitTimeBucket, len(portrait.Hours))
	for _, b := range portrait.Hours {
		got[b.Bucket] = b
	}

	// 逐条断言「落在上海小时」，而不是断言 UTC 小时。
	for _, want := range []struct {
		hour   int
		count  int64
		amount float64
	}{
		{21, 1, 1}, {23, 1, 2}, {13, 1, 4},
	} {
		bucket, exists := got[want.hour]
		if !exists {
			t.Fatalf("%d 点桶缺失（UTC 行未换算到上海时区，整张分布左移 8 小时）。实际桶: %v",
				want.hour, portraitBucketKeys(portrait.Hours))
		}
		if bucket.Count != want.count {
			t.Fatalf("%d 点桶 Count 期望 %d，实际 %d", want.hour, want.count, bucket.Count)
		}
		if bucket.Amount != want.amount {
			t.Fatalf("%d 点桶 Amount 期望 %v，实际 %v", want.hour, want.amount, bucket.Amount)
		}
	}
	// 反向防线：错误的 UTC 桶一个都不许出现。
	for _, wrongHour := range []int{13 - 8 + 8, 5, 15} {
		if _, exists := got[wrongHour]; exists && wrongHour != 13 && wrongHour != 21 && wrongHour != 23 {
			t.Fatalf("%d 点桶不该存在（那是 UTC 小时，不是上海小时）", wrongHour)
		}
	}
}

// TestPortraitWeekdayBucketsNormalizeUTCOffset 钉住星期的跨日界情形。
//
// UTC 的周日 17:00 == 上海周一 01:00。substr 会把它算成周日（错）。
func TestPortraitWeekdayBucketsNormalizeUTCOffset(t *testing.T) {
	db := newPortraitTestDB(t)
	user := portraitSeedUser(t, db, 9102, "portrait-tz-weekday")

	// 2026-11-15 是周日。UTC 17:00 → 上海 2026-11-16 01:00（周一）。
	insertBillingWithRawCreatedAt(t, db, user.ID, 1, "2026-11-15 17:00:00+00:00")

	portrait, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: user.ID})
	if err != nil {
		t.Fatalf("build portrait: %v", err)
	}
	got := make(map[int]PortraitTimeBucket, len(portrait.Weekdays))
	for _, b := range portrait.Weekdays {
		got[b.Bucket] = b
	}
	// 1 = 周一（SQLite %w：0=周日）。必须在周一桶，不能在周日的 0 桶。
	if _, exists := got[1]; !exists {
		t.Fatalf("UTC 周日 17:00 应为上海周一，实际桶: %v", portraitBucketKeys(portrait.Weekdays))
	}
	if _, exists := got[0]; exists {
		t.Fatalf("UTC 周日 17:00 不该落在周日桶 —— 跨日界未换算时区")
	}
}

// ── 辅助 ─────────────────────────────────────────────────────────────

// insertBillingWithRawCreatedAt 绕过 GORM 直接写原始时间文本。
//
// 必须这样做的理由见文件头：用 db.Create(&entity.BillingRecord{CreatedAt: t}）
// 时 GORM 按进程本地时区序列化，永远种不出 `+00:00` 形态。
func insertBillingWithRawCreatedAt(t *testing.T, db *gorm.DB, userID int32, amount float64, rawCreatedAt string) {
	t.Helper()
	reqID := fmt.Sprintf("portrait_tz_%d_%d", userID, time.Now().UnixNano())
	if err := db.Exec(
		"INSERT INTO billing_records (user_id, type, amount, request_id, created_at)"+
			" VALUES (?, ?, ?, ?, ?)",
		userID, portraitTestNormalSearchType, amount, reqID, rawCreatedAt).Error; err != nil {
		t.Fatalf("insert billing: %v", err)
	}
}
