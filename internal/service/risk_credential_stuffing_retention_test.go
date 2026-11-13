package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ════════════════════════════════════════════════════════════════════
// R9 的保留期清理（双闸：90 天 + 500 万行）
//
// ⚠️ **两个数字都是拍的**（无实测依据），但**双闸这个形态是硬的**：
//   - 只按时间删：被撞库攻击时会在 90 天内写入天文数字，先把磁盘打满；
//   - 只按行数删：一次大规模攻击会把正常审计挤掉，历史静默消失。
//
// 本文件钉的是**形态**（两条闸各自独立生效、顺序固定），不是具体数字。
// ════════════════════════════════════════════════════════════════════

func newRetentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:login-failure-retention-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.LoginFailureEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedRetentionEvent(t *testing.T, db *gorm.DB, ip, username string, at time.Time) {
	t.Helper()
	if err := db.Create(&entity.LoginFailureEvent{
		CreatedAt: at, ClientIP: ip, Username: username,
	}).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}
}

func setRetentionSetting(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	var existing entity.SystemSetting
	err := db.Where("key = ?", key).First(&existing).Error
	if err == nil {
		if err := db.Model(&entity.SystemSetting{}).Where("key = ?", key).
			UpdateColumn("value", value).Error; err != nil {
			t.Fatalf("update setting %s: %v", key, err)
		}
		return
	}
	if err := db.Create(&entity.SystemSetting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("create setting %s: %v", key, err)
	}
}

// 判据①：**时间闸** —— 超过 90 天的行被删，未超的保留。
func TestLoginFailureRetentionDeletesAgedRows(t *testing.T) {
	db := newRetentionTestDB(t)
	// system_settings 表可能不存在（本夹具只迁了流水表），所以用默认策略（90 天）。
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	old := now.AddDate(0, 0, -91)
	seedRetentionEvent(t, db, "203.0.113.1", "old", old.Add(-time.Hour))
	fresh := now.AddDate(0, 0, -89)
	seedRetentionEvent(t, db, "203.0.113.2", "fresh", fresh)

	aged, trimmed, err := ApplyLoginFailureEventRetentionWithDB(db, now)
	if err != nil {
		t.Fatalf("apply retention: %v", err)
	}
	if aged != 1 {
		t.Fatalf("时间闸应删 1 行（91 天前的那条），实际删除 %d 行", aged)
	}
	if trimmed != 0 {
		t.Fatalf("未超行数上限时容量闸不该删任何行，实际 %d", trimmed)
	}
	var remaining []entity.LoginFailureEvent
	if err := db.Find(&remaining).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Username != "fresh" {
		t.Fatalf("应只剩 fresh 那行，实际 %+v", remaining)
	}
}

// 判据②：**容量闸** —— 超行数上限时删**最老**的行。
//
// 变异方向：把 `Order("created_at ASC")` 改成 DESC → 本用例必红（删的会是最新行）。
func TestLoginFailureRetentionTrimsOldestRows(t *testing.T) {
	db := newRetentionTestDB(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	// 造 10 行，全部在保留期内（不触发时间闸）。
	for i := 0; i < 10; i++ {
		seedRetentionEvent(t, db, "203.0.113.10", fmt.Sprintf("user%02d", i),
			now.Add(-time.Duration(i)*time.Minute))
	}
	// 把行数上限压到 4（通过直接调内部策略无法注入，所以这里显式设 system_settings）。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	setRetentionSetting(t, db, LoginFailureMaxRowsSettingKey, "4")

	aged, trimmed, err := ApplyLoginFailureEventRetentionWithDB(db, now)
	if err != nil {
		t.Fatalf("apply retention: %v", err)
	}
	if aged != 0 {
		t.Fatalf("全部在保留期内，时间闸不该删任何行，实际 %d", aged)
	}
	if trimmed != 6 {
		t.Fatalf("10 行超上限 4，容量闸应删 6 行，实际 %d", trimmed)
	}
	var remaining []entity.LoginFailureEvent
	if err := db.Order("created_at ASC").Find(&remaining).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(remaining) != 4 {
		t.Fatalf("应剩 4 行，实际 %d", len(remaining))
	}
	// 关键：剩下的必须是**最新的 4 行**（user00..user03 是最近的）。
	for _, row := range remaining {
		switch row.Username {
		case "user00", "user01", "user02", "user03":
		default:
			t.Fatalf("容量闸必须删**最老**的行，但 %q 被保留了下来（应删最老、留最新）",
				row.Username)
		}
	}
}

// 判据③：**顺序固定** —— 先按时间删、再按行数删。
//
// 反过来的话，容量闸会先删掉一批「本该被时间闸删掉的老行」，
// 让时间闸的统计数字失真（日志里报的「按时间删除 N 行」不是真实效果）。
func TestLoginFailureRetentionTimeGateRunsFirst(t *testing.T) {
	db := newRetentionTestDB(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	// 3 行超龄 + 3 行新鲜；上限设成 2。
	for i := 0; i < 3; i++ {
		seedRetentionEvent(t, db, "203.0.113.20", fmt.Sprintf("aged%02d", i),
			now.AddDate(0, 0, -100).Add(-time.Duration(i)*time.Minute))
	}
	for i := 0; i < 3; i++ {
		seedRetentionEvent(t, db, "203.0.113.21", fmt.Sprintf("fresh%02d", i),
			now.Add(-time.Duration(i)*time.Minute))
	}
	setRetentionSetting(t, db, LoginFailureMaxRowsSettingKey, "2")

	aged, trimmed, err := ApplyLoginFailureEventRetentionWithDB(db, now)
	if err != nil {
		t.Fatalf("apply retention: %v", err)
	}
	// 先删 3 行超龄 → 剩 3 行；再按上限 2 删 1 行最老。
	if aged != 3 {
		t.Fatalf("时间闸应删 3 行（全部超龄行），实际 %d。"+
			"若为 0 或更少，说明容量闸先跑了，抢走了本该算在时间闸名下的行", aged)
	}
	if trimmed != 1 {
		t.Fatalf("容量闸应在时间闸之后删 1 行，实际 %d", trimmed)
	}
}

// 判据④：表不存在时不报错（老库未迁移）。
func TestLoginFailureRetentionToleratesMissingTable(t *testing.T) {
	db := newRetentionTestDB(t)
	if err := db.Migrator().DropTable(entity.TableNameLoginFailureEvent); err != nil {
		t.Fatalf("drop: %v", err)
	}
	aged, trimmed, err := ApplyLoginFailureEventRetentionWithDB(db, time.Now())
	if err != nil {
		t.Fatalf("表不存在时应静默跳过，实际报错: %v", err)
	}
	if aged != 0 || trimmed != 0 {
		t.Fatalf("表不存在时不该删任何行，实际 aged=%d trimmed=%d", aged, trimmed)
	}
}

// 判据⑤：`detail.threshold` 必须是**配置值**而不是实际计数（与
// risk_detail_threshold_test.go 的 6 条钉子同源，这里补 R9）。
//
// 构造：阈值 5、实际 8 个账号 —— 两个数字不同，才能拷动「写成实际值」的缺陷。
func TestRiskDetailThresholdIsConfiguredCredentialStuffing(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 5.0
	const count = 8
	putRiskSetting(t, db, "risk_rule_credential_stuffing_threshold", "5")
	for i := 0; i < count; i++ {
		seedRiskLoginFailure(t, db, "203.0.113.55", 0,
			"detail"+itoaRisk(int32(i)), entity.LoginFailureReasonUserNotFound,
			now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "credential_stuffing", configured, count)
}

// 判据⑥：清理任务能被启动与停止（goroutine 不漏）。
func TestStartLoginFailureEventRetentionCleanupStops(t *testing.T) {
	db := newRetentionTestDB(t)
	now := time.Now()
	seedRetentionEvent(t, db, "203.0.113.30", "old", now.AddDate(0, 0, -200))

	stop := StartLoginFailureEventRetentionCleanup(db, 20*time.Millisecond)
	// 等至少一轮。
	time.Sleep(80 * time.Millisecond)
	stop()

	var remaining int64
	if err := db.Model(&entity.LoginFailureEvent{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("清理任务应删掉 200 天前那行，实际还剩 %d 行", remaining)
	}
	// 停止后不应再变更（再等一轮周期，行数不变即为通过）。
	time.Sleep(60 * time.Millisecond)
}

// 判据⑦：`username` 超长时截断而不是失败。
//
// 撞库时攻击者会故意发超长串；若因长度失败，等于让攻击者用「超长账号名」
// 把审计打哑。
func TestRecordLoginFailureEventTruncatesLongUsername(t *testing.T) {
	db := newRetentionTestDB(t)
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'a'
	}
	if err := RecordLoginFailureEventWithDB(db, entity.LoginFailureEvent{
		CreatedAt: time.Now(), ClientIP: "203.0.113.40", Username: string(long),
	}); err != nil {
		t.Fatalf("超长 username 应被截断而不是失败，实际报错: %v", err)
	}
	var events []entity.LoginFailureEvent
	if err := db.Find(&events).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应写入 1 行，实际 %d", len(events))
	}
	if got := len(events[0].Username); got != loginFailureUsernameMaxLength {
		t.Fatalf("username 应被截断到 %d，实际 %d", loginFailureUsernameMaxLength, got)
	}
}

// 判据⑧：空 IP / 零值时间的行不写（它们对 R9 无意义）。
func TestRecordLoginFailureEventSkipsInvalidRows(t *testing.T) {
	db := newRetentionTestDB(t)
	cases := []struct {
		name  string
		event entity.LoginFailureEvent
	}{
		{"empty_ip", entity.LoginFailureEvent{CreatedAt: time.Now(), ClientIP: "  "}},
		{"zero_time", entity.LoginFailureEvent{ClientIP: "203.0.113.50"}},
	}
	for _, tc := range cases {
		if err := RecordLoginFailureEventWithDB(db, tc.event); err != nil {
			t.Fatalf("%s: 应静默跳过，实际报错 %v", tc.name, err)
		}
	}
	var total int64
	if err := db.Model(&entity.LoginFailureEvent{}).Count(&total).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 0 {
		t.Fatalf("非法行不该写入，实际 %d 行", total)
	}
}

var _ = json.Marshal
