package service

// 可配置的设备闲置自动解绑；默认 10 天。
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

// DeviceIdleRevokeAfter 保留默认时长的兼容常量，实际策略从系统设置读取。
const DeviceIdleRevokeAfter = time.Duration(DefaultDeviceIdleRevokeAfterDays) * 24 * time.Hour

// DeviceIdleRevokeBatchSize 是单批处理行数（137 §7：分批 500）。
//
// 为什么必须分批：一次 UPDATE 全表会在 MySQL 上持有大量行锁并写满 undo log，
// 表现为「后台任务跑一次，前台请求全部超时」。500 是 137 指定的值。
const DeviceIdleRevokeBatchSize = 500

// RunDeviceIdleRevokeOnce 按管理员上限与用户个人时长标记闲置到期绑定，返回处理数。
//
// 语义细节：
//   - **不删行**，只写 revoked_at + revoked_reason；
//   - 已撤销的行不重复处理（`revoked_at IS NULL` 条件），因此第二次调用返回 0；
//   - 按 id 分批 500 扫描，未到期或个人关闭自动移除的设备不会阻塞后续批次；
//   - 更新时再次核对 last_seen_at，防止扫描后设备重新活跃却仍被撤销；
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
	maxDays := GetDeviceIdleRevokeAfterDaysWithDB(db)
	if maxDays == 0 {
		return 0, nil
	}
	// 用户可以设置比管理员上限更短的时长，候选必须从最短的 1 天取起，
	// 再逐条按个人策略判断；不能先按管理员天数过滤而漏掉提前到期的设备。
	minIdle := 24 * time.Hour

	var total int64
	var lastID int64
	userDays := map[int32]int{}
	for {
		// 先取一批 id 再按 id 更新：直接 `UPDATE ... LIMIT 500` 在 MySQL 上
		// 合法但 SQLite 不支持（后者需要编译选项），用 id 列表可两方言通用。
		var candidates []struct {
			ID       int64     `gorm:"column:id"`
			UserID   int32     `gorm:"column:user_id"`
			LastSeen time.Time `gorm:"column:last_seen_at"`
		}
		if err := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
			Where("id > ? AND revoked_at IS NULL AND last_seen_at < ?", lastID, now.Add(-minIdle)).
			Order("id asc").
			Limit(DeviceIdleRevokeBatchSize).
			Select("id, user_id, last_seen_at").Find(&candidates).Error; err != nil {
			return total, err
		}
		if len(candidates) == 0 {
			return total, nil
		}
		lastID = candidates[len(candidates)-1].ID
		idsByDays := map[int][]int64{}
		for _, candidate := range candidates {
			days, exists := userDays[candidate.UserID]
			if !exists {
				days, _ = GetUserDeviceIdleRevokeAfterDaysWithDB(db, candidate.UserID)
				userDays[candidate.UserID] = days
			}
			if days > 0 && candidate.LastSeen.Before(now.Add(-time.Duration(days)*24*time.Hour)) {
				idsByDays[days] = append(idsByDays[days], candidate.ID)
			}
		}
		if len(idsByDays) == 0 {
			if len(candidates) < DeviceIdleRevokeBatchSize {
				return total, nil
			}
			continue
		}

		// ⚠️ 更新条件里**再写一次** `revoked_at IS NULL`：
		// 从取 id 到更新之间可能被别的进程处理过（多实例部署、手动解绑），
		// 靠它保证「已撤销的行不会被二次写」（重复写会把 revoked_at 往后推，
		// 变成「明明 10 天前就该解绑，时间却一直变新」）。
		for days, ids := range idsByDays {
			result := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
				Where("id IN ? AND revoked_at IS NULL AND last_seen_at < ?", ids, now.Add(-time.Duration(days)*24*time.Hour)).
				UpdateColumns(map[string]any{"revoked_at": now, "revoked_reason": entity.DeviceBindingRevokeReasonIdleTimeout})
			if result.Error != nil {
				return total, result.Error
			}
			total += result.RowsAffected
		}

		// 终止条件：本批可取到的 id 少于批量 ⇒ 已经没有更多候选行。
		if len(candidates) < DeviceIdleRevokeBatchSize {
			return total, nil
		}
		// lastID 已推进，本批没有实际更新也能继续扫描后续候选，不会重复取同一批。
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
					logger.SysLog(fmt.Sprintf("设备闲置解绑：本轮撤销 %s 行（管理员上限 %d 天，原因 %s）",
						strconv.FormatInt(processed, 10),
						GetDeviceIdleRevokeAfterDaysWithDB(db),
						entity.DeviceBindingRevokeReasonIdleTimeout))
				}
				timer.Reset(interval)
			}
		}
	}()
	return func() { close(done) }
}
