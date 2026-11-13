package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉住「自然日窗口的时区口径」。
//
// # 为什么必须单独一个文件
//
// riskWindowColumnExpr 原来用裸 `substr(col,1,19)` 把时间列截成
// `YYYY-MM-DD HH:MM:SS` 再与上海时区窗口比较。那只在**所有行都恰好是本地偏移**
// 时才成立。实测本机库：
//
//	users.created_at      = 2026-05-08 13:10:30.573137548+00:00   （UTC）
//	logs.create_time      923/923 行带 +00:00                     （UTC）
//	substr(create_time,1,19) <> datetime(create_time,'+8 hours')  → 命中 923
//
// 即**全部 923 行都落在错误的自然日**，偏移 8 小时，而且不报错。
//
// # 既有测试为什么测不出来
//
// seedRiskRegisterUser 用 `Update("created_at", at)` 写 Go 的 time.Time，
// GORM 按**进程本地时区**序列化，所以种出来的行永远是 `+08:00`（在 CST 机器上），
// 恰好命中 substr 能处理的那一种形态。CI 跑在 UTC 时反而又变一种形态。
//
// 所以本文件的种子**必须绕过 GORM 直接写原始时间字符串**，显式构造 GO 生产环境
// 里真实存在的 `+00:00` 行 —— 这是唯一能把该缺陷钉死的办法。

// TestRiskWindowExprNormalizesUTCOffset 是核心判据：
// 一条 `+00:00` 的用户必须在**正确的上海自然日**里被算进窗口。
func TestRiskWindowExprNormalizesUTCOffset(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)

	// 选一个「UTC 与 CST 分属不同自然日」的时刻，这样偏移一定跨越日期边界。
	//
	//	上海 2026-11-14 02:00  ==  UTC 2026-11-13 18:00
	//
	// 若按 substr 截 UTC 得到 "2026-11-13"，落在**前一天**的窗口；
	// 正确口径应换算成 "2026-11-14"，落在当天窗口。
	now := time.Date(2026, 11, 14, 2, 0, 0, 0, mustShanghai(t))

	for _, name := range []string{"tzbatch1", "tzbatch2", "tzbatch3"} {
		insertUserWithRawCreatedAt(t, db, name, "2026-11-13 18:01:00+00:00")
	}

	// 先确认「原始 SQL 种进去的确实是 UTC 格式」，否则整个用例是空转。
	var stored string
	// ⚠️ 必须 CAST 成 TEXT：直接 Scan(&string) 会让 gorm 把时间列**重新格式化**
	// （实测原样存的是 "2026-11-13 18:01:00+00:00"，Scan 出来变成
	// "2026-11-13T18:01:00Z"），那样这条自检断言永远失败、且看起来像种子写错了。
	// 这是本仓已记录过的坑（Scan(&string) 会重格式化 datetime 列）。
	if err := db.Raw("SELECT CAST(created_at AS TEXT) FROM users WHERE username = ?", "tzbatch1").
		Scan(&stored).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored[:19] != "2026-11-13 18:01:00" {
		t.Fatalf("种子没写成预期的 UTC 文本，got=%q（用例前提失效）", stored)
	}

	candidates, err := collectAbnormalRegisterPatternCandidates(db, riskRuleNamed(t, db, entity.RiskRuleIDAbnormalRegisterPattern), now)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("上海 2026-11-14 02:00 应命中 3 个 UTC 18:01 的用户（同一上海自然日），"+
			"实际 %d 条 —— 若为 0 说明窗口按 UTC 算，偏移 8 小时", len(candidates))
	}
}

// TestRiskWindowExprExcludesAdjacentDay 是反向对照：
// 前一天（上海 11-13）的 UTC 行**不能**算进 11-14 的窗口。
//
// 没有这一条，「把所有行都算进来」的实现也能让上面那条通过。
func TestRiskWindowExprExcludesAdjacentDay(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)

	now := time.Date(2026, 11, 14, 2, 0, 0, 0, mustShanghai(t))
	// 上海 2026-11-13 23:30 == UTC 15:30，属**前一天**，必须排除。
	for _, name := range []string{"prevday1", "prevday2", "prevday3"} {
		insertUserWithRawCreatedAt(t, db, name, "2026-11-13 15:30:00+00:00")
	}

	candidates, err := collectAbnormalRegisterPatternCandidates(db, riskRuleNamed(t, db, entity.RiskRuleIDAbnormalRegisterPattern), now)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("上海 11-13 的用户不得算进 11-14 的窗口，实际 %d 条", len(candidates))
	}
}

// ── 辅助 ─────────────────────────────────────────────────────────────

func mustShanghai(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}
	return loc
}

// riskRuleNamed 从库里取出指定 rule_id 的规则行（loadRiskRules 会按代码默认值补齐缺失项）。
func riskRuleNamed(t *testing.T, db *gorm.DB, ruleID string) entity.RiskRule {
	t.Helper()
	rules, err := loadRiskRules(db)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	for _, r := range rules {
		if r.RuleID == ruleID {
			return r
		}
	}
	t.Fatalf("找不到规则 %s（DefaultRiskRules 里应已登记）", ruleID)
	return entity.RiskRule{}
}

// insertUserWithRawCreatedAt 绕过 GORM 直接写原始时间字符串。
//
// 必须这样做的理由见文件头：用 `Update("created_at", time.Time)` 时 GORM 会按
// 进程本地时区序列化，永远种不出 `+00:00` 形态，也就永远测不出本文件的缺陷。
func insertUserWithRawCreatedAt(t *testing.T, db *gorm.DB, username, rawCreatedAt string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO users (username, password, role, status, register_source, created_at)"+
			" VALUES (?, 'x', 'user', 'enabled', 'public', ?)",
		username, rawCreatedAt).Error; err != nil {
		t.Fatalf("insert %s: %v", username, err)
	}
}
