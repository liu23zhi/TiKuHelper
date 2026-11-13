package service

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件钉住「风控判定的定时驱动器」。
//
// 它存在的理由：EvaluateDebtAlerts 曾有 58 条单测全绿，却**零生产调用方** ——
// 规则一条都不会触发，risk_warnings 永远空，而没有任何报错。所以这里的判据
// 不能只测「函数能跑」，必须测「它真的会被定期调用」+「间隔配置真的生效」。

// TestRiskEvaluateIntervalResolvesFromSettings 是间隔解析的主判据。
func TestRiskEvaluateIntervalResolvesFromSettings(t *testing.T) {
	db := newRiskAlertTestDB(t)

	// 未配置 → 默认 10 分钟。
	if got := resolveRiskEvaluateInterval(db); got != 10*time.Minute {
		t.Fatalf("未配置时期望 10 分钟，got=%v", got)
	}

	// 配置成 3 分钟 → 必须读出来（证明 config 真的被读，不是恒返回默认值）。
	seedRiskSetting(t, db, RiskEvaluateIntervalSettingKey, "3")
	if got := resolveRiskEvaluateInterval(db); got != 3*time.Minute {
		t.Fatalf("配置 3 分钟时期望 3m，got=%v", got)
	}
}

// TestRiskEvaluateIntervalRejectsInvalidValues 是防止「忙循环」的防线。
//
// 把无效值当成 0 会让 time.NewTimer(0) 变成每毫秒一次，直接把 CPU 和
// risk_warnings 表打爆 —— 这是本功能最严重的失效形态（不是漏报而是打爆）。
func TestRiskEvaluateIntervalRejectsInvalidValues(t *testing.T) {
	db := newRiskAlertTestDB(t)

	for _, raw := range []string{"0", "-5", "", "abc", "1.5", "  ", "99999"} {
		t.Run("value="+raw, func(t *testing.T) {
			seedRiskSetting(t, db, RiskEvaluateIntervalSettingKey, raw)
			got := resolveRiskEvaluateInterval(db)
			if got != 10*time.Minute {
				t.Fatalf("无效值 %q 必须回落到默认 10 分钟，got=%v（0 会变成忙循环）", raw, got)
			}
		})
	}
}

// TestParsePositiveIntSetting 是解析函数的单元判据。
func TestParsePositiveIntSetting(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"1", 1, false},
		{"10", 10, false},
		{"1440", 1440, false},
		{"0", 0, true},
		{"", 0, true},
		{"-1", 0, true},
		{"1.5", 0, true},
		{"abc", 0, true},
		{"1441", 0, true}, // 超过一天上限
		{" 10", 0, true},  // 前导空格不放过（宁可回落默认值）
	}
	for _, c := range cases {
		got, err := parsePositiveIntSetting(c.raw)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q 期望报错，got=%d nil", c.raw, got)
			}
			if got != 0 {
				t.Fatalf("%q 报错时返回值必须为 0，got=%d", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q 期望成功，got err=%v", c.raw, err)
		}
		if got != c.want {
			t.Fatalf("%q 期望 %d，got=%d", c.raw, c.want, got)
		}
	}
}

// TestStartRiskAlertEvaluationWorkerEvaluatesAndWrites 是行为级主判据。
//
// 它证明 worker **真的会调 EvaluateDebtAlerts 并落库**，而不只是启动了一个
// 空转的 goroutine。做法：造一个必然命中的欠款用户，用极短间隔启动 worker，
// 等到 risk_warnings 出现行。
func TestStartRiskAlertEvaluationWorkerEvaluatesAndWrites(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedDebtUserForWorker(t, db, "worker-debt-user", -500)

	before := countRiskWarnings(t, db)
	if before != 0 {
		t.Fatalf("前置：期望 0 条告警，got=%d", before)
	}

	// 间隔 30ms：够快，但不会忙循环。
	stop := StartRiskAlertEvaluationWorker(db, 30*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countRiskWarnings(t, db) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("worker 启动后 5 秒内没有写入任何告警 —— 说明它没真正调用 EvaluateDebtAlerts" +
		"（这正是本文件要防的「goroutine 空转」形态）")
}

// TestStartRiskAlertEvaluationWorkerStopIsBlocking 钉住停止函数的语义。
//
// 停止函数必须**阻塞到 goroutine 真正退出**，否则 main.go 的 defer 顺序会
// 出现「先关数据库、worker 还在写」的竞态。
func TestStartRiskAlertEvaluationWorkerStopIsBlocking(t *testing.T) {
	db := newRiskAlertTestDB(t)
	stop := StartRiskAlertEvaluationWorker(db, time.Hour)

	start := time.Now()
	stop()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stop 耗时 %v —— 阻塞语义失效（小时级间隔应能立刻被 stop 打断）", elapsed)
	}
	// 二次调用必须安全（sync.Once）。
	stop()
}

// TestMainWiresRiskEvaluationWorker 是**接线层**源码判据。
//
// 上面三条都直接调 StartRiskAlertEvaluationWorker，只能证明「它能工作」，
// 不能证明 main.go 调了它。少了这一条，「实现全对但没人调」这个缺陷可以
// 在全部单测绿灯的情况下再次发生 —— 这正是本文件诞生时的真实场景。
func TestMainWiresRiskEvaluationWorker(t *testing.T) {
	// 路径是 ../../cmd/...：go test 把工作目录设在**包目录**（internal/service），
	// 所以 `..` 是 internal/，要到仓库根还要再上一层。本仓先例：
	// payment_icon_test.go 读 ../../web/... 同理，而 risk_alert_test.go 读
	// ../controller/logs.go（同在内层）也印证了这个相对基准。
	raw, err := os.ReadFile("../../cmd/adapter-service/main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败：%v", err)
	}
	source := string(raw)

	if !strings.Contains(source, "StartRiskAlertEvaluationWorker(") {
		t.Fatal("main.go 没有启动风控判定 worker —— EvaluateDebtAlerts 将再次无人调用，" +
			"所有规则（R1/R8/R11/debt_*）一条都不会触发，且不报错")
	}
	// 必须 defer 停止：否则优雅关机时 worker 还在写 risk_warnings。
	if !strings.Contains(source, "defer stopRiskEvaluation()") {
		t.Fatal("main.go 启动了风控 worker 但没有 defer 停止 —— 关机时可能写入已关闭的数据库")
	}
}

// ── 测试辅助 ──────────────────────────────────────────────────────────

func seedRiskSetting(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	// 用 OnConflict 覆盖：同 key 多次 seed（子用例循环）不该撞唯一索引。
	if err := db.Session(&gorm.Session{NewDB: true}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).
		Create(&entity.SystemSetting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("seed setting %s=%s: %v", key, value, err)
	}
}

func countRiskWarnings(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.RiskWarning{}).Count(&n).Error; err != nil {
		t.Fatalf("count risk_warnings: %v", err)
	}
	return n
}

// seedDebtUserForWorker 造一个 |balance| 必然超过阈值的用户。
func seedDebtUserForWorker(t *testing.T, db *gorm.DB, username string, balance float64) {
	t.Helper()
	user := entity.User{
		Username: username, Password: "x", Role: "user", Status: "enabled",
		RegisterSource: "system", Balance: balance,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&user).Error; err != nil {
		t.Fatalf("seed debt user: %v", err)
	}
}
