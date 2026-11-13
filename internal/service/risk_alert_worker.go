package service

import (
	"errors"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// errRiskSettingUnparsable 表示设置值解析不出正整数（调用方兜底默认值）。
//
// 用哨兵而非裸 error：parsePositiveIntSetting 的三个拒绝分支语义相同，
// 调用方不需要区分，但测试要能断言「这次确实走了解析失败这条路」——
// 否则「读不出配置就落默认值」这条行为无法被测出来（它和「配置就是默认值」
// 在端到端表现得一模一样）。
var errRiskSettingUnparsable = errors.New("risk evaluate interval 设置值不是正整数")

// 风控判定的**定时驱动器**。
//
// # 为什么必须有这个文件
//
// EvaluateDebtAlerts（risk_alert.go:505）实现了全部规则的判定与落库，也有完整单测，
// 但在本文件出现之前它**零生产调用方** —— 全仓 `git grep EvaluateDebtAlerts` 排除
// _test.go 后只剩定义行与注释。后果是：
//
//   - risk_warnings 表永远是空的 ⇒ 管理页永远显示「暂无告警」
//   - 所有规则（R1 multi_account_ip / R8 ticket_flood / R11 abnormal_register_pattern /
//     debt_* 三条）**一条都不会触发**，而单测全绿
//   - 不报错、不告警、不降级 —— 纯粹静默失效
//
// 这是本项目反复出现的形态：**实现层写完、接线层空**。router.go 曾一次积压 33 个
// 未挂 handler，同一个病。判据永远是「查调用方，不是查定义」。
//
// # 间隔为什么用 system_settings 而不是常量
//
// 与日志清理（log_retention.go 的 interval<=0 语义）同一取舍：运营调整评估频率
// 不该需要重启服务。interval<=0 时每轮重新读配置，用 time.Timer 而非 time.Ticker
// （Ticker 的周期在创建时就固定了，改配置不生效）。
const (
	// RiskEvaluateIntervalSettingKey 是评估间隔（分钟）的系统设置键。
	RiskEvaluateIntervalSettingKey = "risk_evaluate_interval_minutes"
	// DefaultRiskEvaluateIntervalMinutes 是未配置时的默认间隔。
	//
	// 为什么是 10 分钟而不是 1 分钟：绝大多数规则的窗口是小时/天级
	// （R1 自然日、R8 24h、debt_* 6~24h），1 分钟一次纯属浪费；而 10 分钟
	// 意味着最坏情况下告警延迟 10 分钟，对「只告警不拦截」的定位完全够用
	// （96 C1 明确风控不参与准入判定，所以延迟不影响用户）。
	DefaultRiskEvaluateIntervalMinutes = 10
)

// StartRiskAlertEvaluationWorker 启动风控判定定时任务，返回停止函数。
//
// 停止函数的语义与其它 Start* 一致：**阻塞到 goroutine 真正退出**才返回，
// 这样 main.go 的 defer 顺序能保证「先停 worker、再关数据库」，不会出现
// 关闭后再写 risk_warnings 的竞态。
func StartRiskAlertEvaluationWorker(db *gorm.DB, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			wait := interval
			if wait <= 0 {
				wait = resolveRiskEvaluateInterval(db)
			}
			timer := time.NewTimer(wait)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			// 失败只告警、不退出循环：一次评估失败（表未迁移/数据库抖动）不该让
			// 风控永久停摆。下个周期重试。
			if _, err := EvaluateDebtAlerts(db, time.Now()); err != nil {
				logger.SysWarn("风控判定任务失败（不影响主服务，下个周期重试）：" + err.Error())
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

// resolveRiskEvaluateInterval 从系统设置读评估间隔，读不出就落到默认值。
//
// 三层解析里任何一层出现「非正数」都继续往下走 —— 与 riskRuleThreshold 同一条纪律：
// 把无效值当成 0 会让 time.NewTimer(0) 变成忙循环，每毫秒评估一次，直接把
// CPU 和 risk_warnings 表打爆。所以这里必须显式排除 0 与负数。
func resolveRiskEvaluateInterval(db *gorm.DB) time.Duration {
	if db != nil {
		raw := getSystemSettingStringWithDefault(db, RiskEvaluateIntervalSettingKey, "")
		if minutes, err := parsePositiveIntSetting(raw); err == nil {
			return time.Duration(minutes) * time.Minute
		}
	}
	return time.Duration(DefaultRiskEvaluateIntervalMinutes) * time.Minute
}

// parsePositiveIntSetting 解析设置值，非正整数一律报错（由调用方兜底默认值）。
func parsePositiveIntSetting(raw string) (int, error) {
	trimmed := raw
	if trimmed == "" {
		return 0, errRiskSettingUnparsable
	}
	value := 0
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, errRiskSettingUnparsable
		}
		value = value*10 + int(r-'0')
		if value > 1440 {
			// 上限一天：超过就不像是「评估间隔」了，多半是填错单位。
			return 0, errRiskSettingUnparsable
		}
	}
	if value <= 0 {
		return 0, errRiskSettingUnparsable
	}
	return value, nil
}
