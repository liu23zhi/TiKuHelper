package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// RecordLoginFailureEventWithDB 写入一条登录失败事件（R9 的数据源）。
//
// # 这是一条**旁路审计**，绝不改变登录响应
//
// 与 `user_binding_records` 的取舍**刻意相反**：
//   - 那次是「必须写、写失败就是缺陷」（主线业务的一部分）；
//   - 这里是**旁路**：写失败只记 `SysLog`，`return` 一个 error 供调用方记录，
//     但**调用方不得据此改变 HTTP 响应**。401/429 的结论已经由 loginGate 算出，
//     不该因为「审计写不进去」而变化。
//
// 为什么这条契约重要：若写失败会改变响应，攻击者就能通过「制造写失败」（如
// 打满磁盘、注入超长 username 触发约束）来探测审计是否在工作，甚至利用它反推系统状态。
//
// # 为什么是同步写入而不是异步队列
//
//   - 失败登录相对搜题是低频路径，同步延迟可接受；
//   - 异步队列会在「进程退出时丢事件」，而 R9 的判据窗口只有 30 分钟 ——
//     丢事件直接影响判据（撞库往往就是几十秒内打完的）；
//   - 调用方应使用 `dbqueue.PriorityLow` 会话，避免抢占答题链路。
//
// # 什么情况下不写（返回 nil，不报错）
//
//   - `db == nil`（未初始化）
//   - 表不存在（老库尚未迁移）—— 此时 R9 也无法工作，但登录不该因此受影响
//   - `clientIP` 为空 —— 空 IP 对 R9 无意义（无法按 IP 聚合）
//   - `createdAt` 为零值 —— 时间缺失的行无法参与 30 分钟窗口判定
//
// 这四种都是「跳过而非失败」，调用方不需要区分。
func RecordLoginFailureEventWithDB(db *gorm.DB, event entity.LoginFailureEvent) error {
	if db == nil {
		return nil
	}
	clientIP := strings.TrimSpace(event.ClientIP)
	if clientIP == "" {
		return nil
	}
	if event.CreatedAt.IsZero() {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginFailureEvent) {
		return nil
	}
	// username 过长时截断而不是报错：撞库时攻击者会故意发超长串，
	// 若因长度失败，等于让攻击者用「超长账号名」把审计打哑。
	username := strings.TrimSpace(event.Username)
	if len(username) > loginFailureUsernameMaxLength {
		username = username[:loginFailureUsernameMaxLength]
	}
	record := entity.LoginFailureEvent{
		CreatedAt: event.CreatedAt,
		ClientIP:  clientIP,
		UserID:    event.UserID,
		Username:  username,
		Scene:     strings.TrimSpace(event.Scene),
		Reason:    strings.TrimSpace(event.Reason),
		Locked:    event.Locked,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		// 旁路契约：只记录，不传播。
		logger.SysWarn(fmt.Sprintf(
			"login_failure_event_write_failed ip=%s scene=%s reason=%s error=%v",
			maskLoginFailureIP(clientIP), record.Scene, record.Reason, err))
		return err
	}
	return nil
}

// loginFailureUsernameMaxLength 与 entity 的 varchar(255) 对齐。
//
// 截断而非拒绝的理由见 RecordLoginFailureEventWithDB 的注释。
const loginFailureUsernameMaxLength = 255

// maskLoginFailureIP 对日志里的 IP 做轻度脱敏（保留前两段）。
//
// 为什么日志要脱敏而库里存原文：库里存原文是 R9 判据必需的（要按 IP 聚合）；
// 而 SysLog 会进日志文件、可能被导出/分享，IP 属于个人信息，按最小必要原则脱敏。
func maskLoginFailureIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ip
	}
	return parts[0] + "." + parts[1] + ".x.x"
}

// LoginFailureEventRetentionPolicy 是失败登录流水的保留策略（双闸）。
//
// ⚠️ **两个数字都是拍的**（无实测依据），但**双闸这个形态是硬的**：
//   - 只按时间删：被撞库攻击时会在 90 天内写入天文数字，先把磁盘打满；
//   - 只按行数删：一次大规模攻击会把「上个月的正常审计」挤掉，历史静默消失。
//
// 这个形态判断来自 `log_retention.go` 的实测经验（MaxAgeDays + MaxTotalGiB 双闸）。
type LoginFailureEventRetentionPolicy struct {
	// MaxAgeDays 超过多少天的行删除。默认 90。
	MaxAgeDays int
	// MaxRows 总行数上限，超了按 created_at 升序删最老行。默认 500 万。
	MaxRows int64
}

// 系统设置键（运营可调）。
const (
	// LoginFailureRetentionDaysSettingKey 保留天数（默认 90，**拍的**）。
	LoginFailureRetentionDaysSettingKey = "login_failure_retention_days"
	// LoginFailureMaxRowsSettingKey 行数上限（默认 500 万，**拍的**）。
	LoginFailureMaxRowsSettingKey = "login_failure_max_rows"
)

// 代码默认值（96 号文档未给，本仓自定，均标注为拍的）。
const (
	DefaultLoginFailureRetentionDays = 90
	DefaultLoginFailureMaxRows       = int64(5_000_000)
)

// ResolveLoginFailureEventRetention 读取保留策略（缺/非法一律回落默认）。
func ResolveLoginFailureEventRetention(db *gorm.DB) LoginFailureEventRetentionPolicy {
	policy := LoginFailureEventRetentionPolicy{
		MaxAgeDays: DefaultLoginFailureRetentionDays,
		MaxRows:    DefaultLoginFailureMaxRows,
	}
	if db == nil {
		return policy
	}
	if raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, LoginFailureRetentionDaysSettingKey, "")); raw != "" {
		if value, err := parsePositiveInt(raw); err == nil && value > 0 {
			policy.MaxAgeDays = value
		}
	}
	if raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, LoginFailureMaxRowsSettingKey, "")); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value > 0 {
			policy.MaxRows = value
		}
	}
	return policy
}

// ApplyLoginFailureEventRetentionWithDB 执行一次清理（双闸）。
//
// 顺序固定：**先按时间删，再按行数删**。
// 反过来的话，行数闸会先删掉一批「本该被时间闸删掉的老行」，让时间闸的
// 统计数字失真（日志里报的「按时间删除 N 行」就不是真实的时间闸效果）。
func ApplyLoginFailureEventRetentionWithDB(db *gorm.DB, now time.Time) (aged, trimmed int64, err error) {
	if db == nil {
		return 0, 0, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginFailureEvent) {
		return 0, 0, nil
	}
	policy := ResolveLoginFailureEventRetention(db)

	// 闸一：按时间。
	cutoff := now.AddDate(0, 0, -policy.MaxAgeDays)
	deleted := db.Session(&gorm.Session{NewDB: true}).
		Where("created_at < ?", cutoff).
		Delete(&entity.LoginFailureEvent{})
	if deleted.Error != nil {
		return 0, 0, deleted.Error
	}
	aged = deleted.RowsAffected

	// 闸二：按行数（删最老的）。
	var total int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.LoginFailureEvent{}).
		Count(&total).Error; err != nil {
		return aged, 0, err
	}
	if total <= policy.MaxRows {
		return aged, 0, nil
	}
	excess := total - policy.MaxRows
	// 取「第 excess 老」的那一行的 **id** 作为分界，再按 `id <= 该 id` 批量删。
	//
	// ⚠️ 两个曾经写错的地方（都有用例钉住）：
	//  1. **必须用 id 而不是 created_at 做分界**：created_at 可能重复（撞库时
	//     同一秒多条），按 `created_at < boundary` 删会把「与分界同秒的行」
	//     全部留下或全部删掉，两种都错。id 严格递增，`id <= boundaryID` 精确。
	//  2. **删除条件是 `<=` 不是 `<`**：分界行本身就是要删的那一行。
	//     写成 `<` 会少删 1 行（实测：10 行超上限 4 时只删 5、应删 6）。
	//
	// 不用 LIMIT 子查询（SQLite 的 DELETE ... LIMIT 需编译开关，MySQL 语法也不同）。
	//
	// ⚠️ 第三个坑（实测）：**id 与 created_at 的顺序可能相反** —— 测试里常按
	// 「先插最新的」构造，于是 id 最大的反而是时间最早的。所以**不能用 `id <=`
	// 删**（那会删掉时间上最新的那批）。必须用 `(created_at, id)` 的组合条件：
	// 先按时间取分界，再用「严格更早」+「时间相同则 id 更小」精确表达。
	var boundary []struct {
		CreatedAt time.Time
		ID        int64
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.LoginFailureEvent{}).
		Select("created_at, id").
		Order("created_at ASC, id ASC").
		Limit(1).Offset(int(excess) - 1).
		Scan(&boundary).Error; err != nil {
		return aged, 0, err
	}
	if len(boundary) == 0 {
		return aged, 0, nil
	}
	trimmedResult := db.Session(&gorm.Session{NewDB: true}).
		Where("created_at < ? OR (created_at = ? AND id <= ?)",
			boundary[0].CreatedAt, boundary[0].CreatedAt, boundary[0].ID).
		Delete(&entity.LoginFailureEvent{})
	if trimmedResult.Error != nil {
		return aged, 0, trimmedResult.Error
	}
	return aged, trimmedResult.RowsAffected, nil
}

// StartLoginFailureEventRetentionCleanup 起一个独立的清理 goroutine，返回停止函数。
//
// 与 `StartLogRetentionCleanup` 同构，`interval <= 0` = **每轮从 system_settings
// 重新解析**（管理员改配置后不必重启）。为此用 time.Timer 而非 time.Ticker：
// ticker 的周期在创建时固定，改配置只能重启。
//
// ⚠️ 独立性：清理失败只记 `SysWarn`，绝不影响主服务 —— 日志清理失败的后果是
// 「占更多磁盘」，而把服务搞挂是更大的事故，两者不能挂钩。
func StartLoginFailureEventRetentionCleanup(db *gorm.DB, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			wait := interval
			if wait <= 0 {
				wait = loginFailureCleanupInterval
			}
			if wait <= 0 {
				wait = time.Hour
			}
			timer := time.NewTimer(wait)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			aged, trimmed, err := ApplyLoginFailureEventRetentionWithDB(db, time.Now())
			if err != nil {
				logger.SysWarn("失败登录流水清理失败（不影响主服务，下个周期重试）：" + err.Error())
				continue
			}
			if aged > 0 || trimmed > 0 {
				logger.SysLog(fmt.Sprintf(
					"失败登录流水清理：按时间删除 %d 行，按容量删除 %d 行", aged, trimmed))
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// loginFailureCleanupInterval 是清理周期（一小时）。
//
// 失败登录量级远小于答题请求，且清理本身只有两条 DELETE。
// **刻意不做成可配**：多一个配置项就多一个「配错了没人发现」的面，
// 而这个值调快调慢都不影响正确性（只影响磁盘回收的及时性）。
const loginFailureCleanupInterval = time.Hour
