package registry

import (
	"fmt"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

// startupMigrationSlowThreshold 是启动迁移「算慢」的阈值。
//
// 启动迁移是串行的，任何一步超标都会让容器启动肉眼可见地变慢。这里主动记一条日志，
// 避免下次又要靠「感觉启动好慢」才发现问题（见 AGENTS.md 第 18b 条）。
const startupMigrationSlowThreshold = 1 * time.Second

// runStartupMigration 执行一个启动迁移并记录耗时。
//
// 只负责观测：错误原样返回，由调用方决定是 Fatal 还是仅记日志。
func runStartupMigration(name string, fn func() error) error {
	if fn == nil {
		return nil
	}
	startedAt := time.Now()
	err := fn()
	if message, slow := startupMigrationSlowLog(name, time.Since(startedAt), startupMigrationSlowThreshold); slow {
		logger.SysLog(message)
	}
	return err
}

// startupMigrationSlowLog 判断是否需要为这次迁移告警，并给出文案。
// 抽成纯函数是为了能直接断言阈值行为，不必去读日志文件。
func startupMigrationSlowLog(name string, elapsed, threshold time.Duration) (string, bool) {
	if threshold <= 0 || elapsed < threshold {
		return "", false
	}
	return fmt.Sprintf("启动迁移 %s 耗时 %dms（超过 %dms，检查是否退化成逐行扫表，见 AGENTS.md 第 18b 条）",
		name, elapsed.Milliseconds(), threshold.Milliseconds()), true
}
