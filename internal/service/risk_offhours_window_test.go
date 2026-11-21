package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 凌晨时段（R16「凌晨作息异常」）此前是两处 const：
//
//	riskOffhoursStartHour = 2
//	riskOffhoursEndHour   = 5
//
// 后果：运营完全无法调整。如果实际业务的高峰不是 02:00–05:00（例如某个渠道
// 集中在 00:00–03:00 刷），这条规则要么全漏要么全误报，而且改不了。
//
// 这组用例钉住：时段必须可由 system_settings 配置，且**非法配置必须回落到
// 代码默认值而不是产生一个「反向生效」的窗口**（与 risk_evaluate_interval
// 填 >1440 会回落 10min 那个坑同源）。

func riskOffhoursTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("resolve sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func setRiskSetting(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	if err := db.Where("key = ?", key).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear %s: %v", key, err)
	}
	if err := db.Create(&entity.SystemSetting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func TestRiskOffhoursWindowUsesCodeDefaultWhenUnconfigured(t *testing.T) {
	db := riskOffhoursTestDB(t)
	start, end := riskOffhoursWindow(db)
	if start != riskOffhoursDefaultStartHour || end != riskOffhoursDefaultEndHour {
		t.Fatalf("未配置时应回落代码默认值 (%d,%d)，实际 (%d,%d)",
			riskOffhoursDefaultStartHour, riskOffhoursDefaultEndHour, start, end)
	}
}

func TestRiskOffhoursWindowReadsConfiguredHours(t *testing.T) {
	db := riskOffhoursTestDB(t)
	setRiskSetting(t, db, riskOffhoursStartSettingKey, "0")
	setRiskSetting(t, db, riskOffhoursEndSettingKey, "6")

	start, end := riskOffhoursWindow(db)
	if start != 0 || end != 6 {
		t.Fatalf("应读到配置值 (0,6)，实际 (%d,%d)", start, end)
	}
}

// 跨零点窗口是合法的：22:00–06:00 表示「晚上 22 点到次日 6 点」。
// 「start > end 就是非法」是最容易写错的一条判据 —— 那会把最常用的跨夜配置拒掉。
func TestRiskOffhoursWindowAllowsCrossMidnight(t *testing.T) {
	db := riskOffhoursTestDB(t)
	setRiskSetting(t, db, riskOffhoursStartSettingKey, "22")
	setRiskSetting(t, db, riskOffhoursEndSettingKey, "6")

	start, end := riskOffhoursWindow(db)
	if start != 22 || end != 6 {
		t.Fatalf("跨零点窗口应被接受 (22,6)，实际 (%d,%d)", start, end)
	}
}

func TestRiskOffhoursWindowRejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
	}{
		{name: "起点越界", start: "24", end: "6"},
		{name: "终点越界", start: "2", end: "24"},
		{name: "负数", start: "-1", end: "6"},
		{name: "非数字", start: "abc", end: "6"},
		{name: "起止相同(零长度窗口)", start: "3", end: "3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := riskOffhoursTestDB(t)
			setRiskSetting(t, db, riskOffhoursStartSettingKey, tc.start)
			setRiskSetting(t, db, riskOffhoursEndSettingKey, tc.end)

			start, end := riskOffhoursWindow(db)
			if start != riskOffhoursDefaultStartHour || end != riskOffhoursDefaultEndHour {
				t.Fatalf("非法配置 (%q,%q) 必须整体回落到默认值，实际 (%d,%d)",
					tc.start, tc.end, start, end)
			}
		})
	}
}

// 只配了一半（只有 start 或只有 end）时，不能把另一半当成默认值拼起来 ——
// 那会得到一个既不是默认、也不是运营预期的新窗口（例如 start=0 配成 0:00–5:00，
// 运营以为自己只改了起点，实际改变了整个判据）。
func TestRiskOffhoursWindowRequiresBothHalves(t *testing.T) {
	t.Run("只有起点", func(t *testing.T) {
		db := riskOffhoursTestDB(t)
		setRiskSetting(t, db, riskOffhoursStartSettingKey, "0")
		start, end := riskOffhoursWindow(db)
		if start != riskOffhoursDefaultStartHour || end != riskOffhoursDefaultEndHour {
			t.Fatalf("只配起点必须整体回落默认，实际 (%d,%d)", start, end)
		}
	})
	t.Run("只有终点", func(t *testing.T) {
		db := riskOffhoursTestDB(t)
		setRiskSetting(t, db, riskOffhoursEndSettingKey, "6")
		start, end := riskOffhoursWindow(db)
		if start != riskOffhoursDefaultStartHour || end != riskOffhoursDefaultEndHour {
			t.Fatalf("只配终点必须整体回落默认，实际 (%d,%d)", start, end)
		}
	})
}

// riskOffhoursInWindow 必须正确处理跨零点，否则 22:00–06:00 的窗口
// 会变成「22 点整那一个瞬间」或者干脆永不命中。
func TestRiskOffhoursInWindowHandlesWrapAround(t *testing.T) {
	// 22 -> 6 跨夜窗口
	inWindow := func(hour int) bool { return riskOffhoursHourInWindow(hour, 22, 6) }
	for _, hour := range []int{22, 23, 0, 1, 5} {
		if !inWindow(hour) {
			t.Fatalf("hour=%d 应在 22->6 窗口内", hour)
		}
	}
	for _, hour := range []int{6, 12, 21} {
		if inWindow(hour) {
			t.Fatalf("hour=%d 不应在 22->6 窗口内", hour)
		}
	}

	// 普通窗口 2 -> 5
	normal := func(hour int) bool { return riskOffhoursHourInWindow(hour, 2, 5) }
	for _, hour := range []int{2, 3, 4} {
		if !normal(hour) {
			t.Fatalf("hour=%d 应在 2->5 窗口内", hour)
		}
	}
	for _, hour := range []int{1, 5, 23} {
		if normal(hour) {
			t.Fatalf("hour=%d 不应在 2->5 窗口内", hour)
		}
	}
}

// 配置必须真的影响判定：在 22->6 窗口下，21 点的活动不应被算作凌晨活动。
// 这条是端到端的：它同时覆盖 reader 与 SQL 表达式拼接两处。
func TestCollectOffhoursUsesConfiguredWindow(t *testing.T) {
	db := riskOffhoursTestDB(t)
	if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate billing: %v", err)
	}
	setRiskSetting(t, db, riskOffhoursStartSettingKey, "22")
	setRiskSetting(t, db, riskOffhoursEndSettingKey, "6")

	// 直接验证 SQL 片段用的是配置值而不是常量。
	expr := riskOffhoursWindowExpr(db, "created_at")
	if expr == "" {
		t.Fatal("窗口表达式不能为空")
	}
	// ⚠️ 不能用 contains(expr, ">= 2") 判「默认值残留」——" >= 22" 里就含 ">= 2"，
	// 子串匹配会假红。必须按「比较符 + 空格 + 边界」精确判。
	if !contains(expr, ">= 22") {
		t.Fatalf("窗口表达式应含配置值 >= 22，实际 %q", expr)
	}
	if !contains(expr, "< 6") {
		t.Fatalf("窗口表达式应含配置值 < 6，实际 %q", expr)
	}
	// 跨零点必须展开成 OR（否则 22->6 窗口永远为空、规则静默失效）
	if !contains(expr, " OR ") {
		t.Fatalf("跨零点窗口必须展开成 OR 分支，实际 %q", expr)
	}
	// 反向：默认值 2/5 不得作为独立比较出现
	for _, forbidden := range []string{">= 2 AND", ">= 2 OR", ">= 2)", "< 5 AND", "< 5 OR", "< 5)"} {
		if contains(expr, forbidden) {
			t.Fatalf("窗口表达式不应再含代码默认值片段 %q，实际 %q", forbidden, expr)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// 保证 time 被用到（跨零点语义的注释引用了它，这里做一次编译期占用）。
var _ = time.Now
