package service

// 10 天闲置自动解绑（137 §7，判据 D10 / D11）。
//
// ## 为什么是「写 revoked_at」而不是删行
//
// 与 137 §3.2 同理：删行会让「这台设备什么时候被谁移除」永远查不到。
// 更重要的一层：用户下次请求会**静默重绑**（拍板 B），行被复用，
// 只把 revoked_at 清空 —— 这样不产生新行、不涨表。
// 删行的话每次「闲置 10 天 → 重绑」都写一行新记录，表会按设备使用史线性膨胀。
//
// ## 幂等判据（137 §7）
//
// 「第二次跑同一批数据返回 0」—— 因为第一次已经写了 revoked_at，
// 而查询条件带 `revoked_at IS NULL`，第二次自然查不到这批行。
// 这不是「顺手加的条件」，而是幂等的**唯一**来源：
// 若条件里只有 last_seen_at，第二次会重复处理同一批行并反复写库。

import (
	"fmt"
	"strconv"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// DeviceIdleRevokeAfter 是「多久没活跃就自动解绑」（137 §7：10 天）。
//
// ⚠️ 判据 D10 的变异方向是「把 240h 改成 240 分钟」：那会让 10 天变成 4 小时，
// 正常用户一天内的第二次使用就被解绑。这个常量必须只有一处定义，
// 任何把阈值写成字面量 240 或 time.Hour*24*10 的地方都会与这里漂移。
const DeviceIdleRevokeAfter = 240 * time.Hour

// DeviceIdleRevokeBatchSize 是单批处理行数（137 §7：分批 500）。
//
// 为什么必须分批：一次 UPDATE 全表会在 MySQL 上持有大量行锁并写满 undo log，
// 表现为「后台任务跑一次，前台请求全部超时」。500 是 137 指定的值。
const DeviceIdleRevokeBatchSize = 500

// RunDeviceIdleRevokeOnce 把 last_seen_at < now-240h 且 revoked_at IS NULL 的行
// 标记为 revoked_reason='idle_10d'，返回本次处理数（137 §7）。
//
// 语义细节：
//   - **不删行**，只写 revoked_at + revoked_reason；
//   - 已撤销的行不重复处理（`revoked_at IS NULL` 条件），因此第二次调用返回 0；
//   - 分批 500，循环到某批处理数 < 500 为止（说明没有更多可处理行）。
//     注意：**不能**用「处理数 == 500 就继续」这种写法当终止条件 —— 那会因为
//     最后一批恰好 500 行而多跑一次空查询（无害但会多一次全表扫描）。
//     这里用「本批实际影响行数 < 批量」作为终止条件，最后一批正好满 500 时
//     多跑一次空批，代价可接受且逻辑简单。
//   - 表或列不存在时返回 (0, nil)：这是 fail-open 的一部分（设备池是权益限制，
//     不是安全边界，后台任务更没有理由因为构造差异把服务打红）。
func RunDeviceIdleRevokeOnce(db *gorm.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if !devicePoolSchemaAvailable(db) {
		return 0, nil
	}
	// revoked_at 列不存在 ⇒ 无法表达「已撤销」，直接跳过（等价于功能未上线）。
	// 若不判断，SQL 会报 `no such column: revoked_at`，而调用方（启动 worker）
	// 会把它当成持续故障反复重试。
	if !devicePoolRevokedColumnAvailable(db) {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-DeviceIdleRevokeAfter)

	var total int64
	for {
		// 先取一批 id 再按 id 更新：直接 `UPDATE ... LIMIT 500` 在 MySQL 上
		// 合法但 SQLite 不支持（后者需要编译选项），用 id 列表可两方言通用。
		var ids []int64
		if err := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
			Where("revoked_at IS NULL AND last_seen_at < ?", cutoff).
			Order("id asc").
			Limit(DeviceIdleRevokeBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}

		// ⚠️ 更新条件里**再写一次** `revoked_at IS NULL`：
		// 从取 id 到更新之间可能被别的进程处理过（多实例部署、手动解绑），
		// 靠它保证「已撤销的行不会被二次写」（重复写会把 revoked_at 往后推，
		// 变成「明明 10 天前就该解绑，时间却一直变新」）。
		result := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
			Where("id IN ? AND revoked_at IS NULL", ids).
			UpdateColumns(map[string]any{
				"revoked_at":     now,
				"revoked_reason": entity.DeviceBindingRevokeReasonIdle10d,
			})
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected

		// 终止条件：本批可取到的 id 少于批量 ⇒ 已经没有更多候选行。
		if len(ids) < DeviceIdleRevokeBatchSize {
			return total, nil
		}
		// 本批一行都没改动（全被别人处理了）⇒ 继续下一轮；
		// 但不能无限循环：如果这一批 id 全部拿不到更新（例如行被删），
		// 下一轮会取到同样的 id。用「本轮改动 0 行」直接返回避免死循环。
		if result.RowsAffected == 0 {
			return total, nil
		}
	}
}

// StartDeviceIdleRevokeWorker 周期性地跑闲置解绑（137 §7「启动接线」）。
//
// interval <= 0 时用 1 小时（137 §7 指定）。返回值是停止函数（供优雅退出与测试）。
//
// ## 为什么用 time.Timer 而不是 time.Ticker
//
// 137 要求「日志里能看到每轮处理数」，而 Ticker 会让「上一轮还没跑完下一轮就到」：
// 大表上一轮可能要几分钟，Ticker 会堆积多个并发批次，互相之间还会抢同一批 id。
// Timer 形态保证**串行**：跑完再定下一次。
func StartDeviceIdleRevokeWorker(db *gorm.DB, interval time.Duration) (stop func()) {
	if db == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = time.Hour
	}
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-timer.C:
				processed, err := RunDeviceIdleRevokeOnce(db, time.Now())
				if err != nil {
					logger.SysLog("设备闲置解绑任务失败（下轮重试，幂等）：" + logger.RedactSecrets(err.Error()))
				} else if processed > 0 {
					logger.SysLog(fmt.Sprintf("设备闲置解绑：本轮撤销 %s 行（阈值 %s，原因 %s）",
						strconv.FormatInt(processed, 10),
						DeviceIdleRevokeAfter.String(),
						entity.DeviceBindingRevokeReasonIdle10d))
				}
				timer.Reset(interval)
			}
		}
	}()
	return func() { close(done) }
}
