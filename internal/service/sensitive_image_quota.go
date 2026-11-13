package service

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 敏感图片检测的**双额度闸门**（107 Q1 → 109 §2.1 → 111 Q1-g → 121 §4.2 的最终口径）。
//
// 两道闸门，都可配置、默认开启：
//
//	① 每工单 50 张 —— 防止把一个工单当图床
//	② 每人每日 20 张 —— 防止单个账号刷计费外呼
//
// ⚠️ 超额**不拦图片、不拦工单**（107 Q1 ② 拍板）：图片照常落库、工单照常提交，
// 只是这张图被标记成 sensitive_by_quota（默认按敏感处理、不外呼）。
// 额度是「省钱闸」不是「准入闸」，把提交拦掉会让用户无法开单申辩。
const (
	// DefaultSensitiveImagePerTicketLimit 是每工单默认上限。
	DefaultSensitiveImagePerTicketLimit = 50
	// DefaultSensitiveImageDailyUserLimit 是每人每日默认上限。
	DefaultSensitiveImageDailyUserLimit = 20
)

// 三个 system_settings 键名（108-*.md §1.5，管理员可改）。
const (
	SensitiveImageQuotaEnabledSettingKey   = "sensitive_image_quota_enabled"
	SensitiveImagePerTicketLimitSettingKey = "sensitive_image_per_ticket_limit"
	SensitiveImageDailyUserLimitSettingKey = "sensitive_image_daily_user_limit"
)

// sensitiveImageQuotaDateLayout 是自然日键的格式。用字符串而不是 DATE：
// SQLite 没有真正的日期类型，char(10) 让两种方言的存储与比较行为完全一致
// （字典序即时间序），也避免时区在驱动层被二次解释。
const sensitiveImageQuotaDateLayout = "2006-01-02"

var (
	// ErrSensitiveImageQuotaInvalidLimit 表示配置里的上限不是正整数。
	ErrSensitiveImageQuotaInvalidLimit = errors.New("敏感图片检测额度上限必须是正整数")
	// errSensitiveImageQuotaNoDatabase 只在内部使用：所有 *WithDB 入口统一返回它。
	errSensitiveImageQuotaNoDatabase = errors.New("数据库未初始化")
)

// SensitiveImageQuotaPolicy 是一次判定要用的额度配置快照。
//
// 快照而不是每次读库：一次上传要连续问「工单够不够、当日够不够」，
// 两次读之间管理员改配置会让同一次判定看到两套口径。
type SensitiveImageQuotaPolicy struct {
	// Enabled 是两个闸门的总开关。关闭时既不检查也不计数（完全不碰额度表）。
	Enabled bool
	// PerTicketLimit 每工单上限。<=0 表示「不限制工单维度」，只留每日闸门。
	PerTicketLimit int
	// DailyUserLimit 每人每日上限。<=0 表示「不限制每日维度」。
	DailyUserLimit int
}

// SensitiveImageQuotaDate 把时刻折算成**自然日**键（Asia/Shanghai）。
//
// 必须是自然日而不是滚动 24 小时（107 Q1 ⑤ 拍板）：管理员要能按日期对账，
// 而且「跨零点归零」对用户是可解释的。也必须是上海时区而不是 UTC ——
// 用 UTC 会让「今天」在北京时间 08:00 才翻篇，用户会看到额度在早上八点重置。
func SensitiveImageQuotaDate(at time.Time) string {
	return at.In(logger.ShanghaiLocation()).Format(sensitiveImageQuotaDateLayout)
}

// LoadSensitiveImageQuotaPolicyWithDB 读配置，缺失/非法值回落到默认（默认开启）。
//
// 三个键各自独立回落：管理员只改了一个键时，另外两个仍取默认值，
// 不会因为「另一个键不存在」把整个闸门关掉（缺一个键就静默关闭限额，
// 会让计费外呼失去全部保护）。
//
// ⚠️ **默认值只有一处定义**：它是 sensitiveImageQuotaEnabledWithDB 里
// 「键缺失/空值 ⇒ true」那一条，以及 sensitiveImageLimitSetting 的 defaultValue 参数。
// 不要在这里的结构体初值里再写一遍 —— 下面第 87 行是无条件覆盖，
// 初值写了也不生效，读者会误以为改它有用（这条曾经真的骗过我一次：
// 把初值 Enabled 翻成 false 做变异，行为层一条都没红）。
// 因此这里刻意**不写**初值，让「默认值在哪」只有唯一答案。
func LoadSensitiveImageQuotaPolicyWithDB(db *gorm.DB) SensitiveImageQuotaPolicy {
	if db == nil {
		// 数据库不可用时按「闸门开启 + 默认上限」处理（fail-closed）：
		// 返回全零值等于静默变成「无额度限制」，那正是最糟的失败方向。
		return SensitiveImageQuotaPolicy{
			Enabled:        true,
			PerTicketLimit: DefaultSensitiveImagePerTicketLimit,
			DailyUserLimit: DefaultSensitiveImageDailyUserLimit,
		}
	}
	policy := SensitiveImageQuotaPolicy{
		Enabled:        sensitiveImageQuotaEnabledWithDB(db),
		PerTicketLimit: sensitiveImageLimitSetting(db, SensitiveImagePerTicketLimitSettingKey, DefaultSensitiveImagePerTicketLimit),
		DailyUserLimit: sensitiveImageLimitSetting(db, SensitiveImageDailyUserLimitSettingKey, DefaultSensitiveImageDailyUserLimit),
	}
	return policy
}

// sensitiveImageQuotaEnabledWithDB 解析开关。
//
// 默认**开启**（109-*.md §2.1「默认开启 ✅」），与 ticket_image_moderation_enabled
// 的默认关闭相反 —— 两者的成本模型不同：审核开关关着就没有任何调用，
// 而敏感图检测一旦没有额度闸门，就是无上限的计费外呼。
//
// 取值口径与仓内其它布尔开关一致（"true"/"1"），但**显式的 false/0 才关闭**：
// 空值/缺失一律视为默认开启。
func sensitiveImageQuotaEnabledWithDB(db *gorm.DB) bool {
	raw := strings.ToLower(strings.TrimSpace(getSystemSettingStringWithDefault(db, SensitiveImageQuotaEnabledSettingKey, "")))
	switch raw {
	case "false", "0", "off", "no":
		return false
	case "":
		return true
	default:
		return true
	}
}

// sensitiveImageLimitSetting 读一个正整数上限；缺失或非法一律回落到默认值。
func sensitiveImageLimitSetting(db *gorm.DB, key string, defaultValue int) int {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.Itoa(defaultValue)))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

// SensitiveImageQuotaUsageWithDB 读某用户某自然日的已用次数。
//
// 没有行 = 0（当日从 0 起算，不做回填）。读失败返回 error 让调用方决定：
// 调用方一律按「读不到就当成已用满」处理（fail-closed）—— 读不到额度就外呼，
// 等于把额度闸门在整个数据库故障期间彻底关掉。
func SensitiveImageQuotaUsageWithDB(db *gorm.DB, userID int32, date string) (int32, error) {
	if db == nil {
		return 0, errSensitiveImageQuotaNoDatabase
	}
	if userID <= 0 || strings.TrimSpace(date) == "" {
		return 0, nil
	}
	var row entity.SensitiveImageDailyQuota
	err := db.Where(map[string]any{"user_id": userID, "quota_date": date}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if row.UsedCount < 0 {
		return 0, nil
	}
	return row.UsedCount, nil
}

// ConsumeSensitiveImageDailyQuotaWithDB 原子地把「当日已用」加一，满额则不动。
//
// 返回 consumed 表示**本次真的扣到了额度**；used 是扣减之后（或被拒之后）的读数。
//
// 为什么不是「先查再写」：两个并发上传会读到同一个 used_count 再各自 +1，
// 结果是 both consumed=true 但实际只该放行一个 —— 额度被超发。
// 这里靠一条带条件的 UPDATE（WHERE used_count < limit）让数据库做判定，
// 落败方的 RowsAffected 为 0。
//
// 为什么先确保行存在：SQL 的 UPDATE 不会创建行，首次上传必须先把
// (user_id, quota_date) 这一行插出来。插入用 OnConflict DoNothing（MySQL 上是
// INSERT IGNORE），并发重复插只会有一个生效，因此这里天然幂等。
func ConsumeSensitiveImageDailyQuotaWithDB(db *gorm.DB, userID int32, date string, limit int) (consumed bool, used int32, err error) {
	if db == nil {
		return false, 0, errSensitiveImageQuotaNoDatabase
	}
	if userID <= 0 || strings.TrimSpace(date) == "" {
		return false, 0, nil
	}
	if limit <= 0 {
		// 不限制：仍然计数（管理员事后能看用量），但没有上限。
		limit = int(^uint32(0) >> 1)
	}
	now := time.Now().In(logger.ShanghaiLocation())
	seed := entity.SensitiveImageDailyQuota{UserID: userID, QuotaDate: date, UsedCount: 0, UpdatedAt: now}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		return false, 0, err
	}
	update := db.Model(&entity.SensitiveImageDailyQuota{}).
		Where(map[string]any{"user_id": userID, "quota_date": date}).
		Where("used_count < ?", limit).
		Updates(map[string]any{
			"used_count": gorm.Expr("used_count + 1"),
			"updated_at": now,
		})
	if update.Error != nil {
		return false, 0, update.Error
	}
	consumed = update.RowsAffected > 0
	used, err = SensitiveImageQuotaUsageWithDB(db, userID, date)
	if err != nil {
		// 扣减已经成功但读不回来：如实返回 consumed=true 与 used=0，让调用方
		// 不会因为「读失败」重复扣一次。上限判定不依赖这个读数。
		return consumed, 0, err
	}
	return consumed, used, nil
}

// SensitiveImageTicketImageCountWithDB 数一个工单下已有的图片数量（每工单闸门的输入）。
//
// 只读 ticket_attachments（不新增列、不改结构）：每工单上限针对的是
// 「这个工单上被检测的图片有多少张」，与附件表一一对应。
// ticketID <= 0（建单前的草稿图）返回 0：草稿阶段按上传者走每日闸门，
// 不按工单算 —— 那时还没有工单。
func SensitiveImageTicketImageCountWithDB(db *gorm.DB, ticketID int64) (int64, error) {
	if db == nil {
		return 0, errSensitiveImageQuotaNoDatabase
	}
	if ticketID <= 0 {
		return 0, nil
	}
	var count int64
	err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticketID).Count(&count).Error
	return count, err
}

// SensitiveImageQuotaDecision 是一次额度判定的结果。
type SensitiveImageQuotaDecision struct {
	// Allowed 表示可以外呼检测。false 时调用方必须走 sensitive_by_quota 分支：
	// 图片照存、工单照提、不外呼。
	Allowed bool
	// Charged 表示本次**消耗**了 1 次每日额度。缓存命中且额度充足时为 true；
	// 缓存命中但额度已耗尽时为 false（111-*.md Q1-g 的核心语义）。
	Charged bool
	// UsedCount 判定之后的当日已用次数；DailyLimit 为当日上限（0 表示不限制）。
	UsedCount  int32
	DailyLimit int
	// TicketCount / PerTicketLimit 是工单闸门的读数（未启用/无工单时为 0）。
	TicketCount    int64
	PerTicketLimit int
	// Reason 是超额原因（中文，可直接给管理员看）；Allowed=true 时为空。
	Reason string
}

// ReserveSensitiveImageQuotaWithDB 做两道闸门判定，并在放行时原子扣减每日额度。
//
// 顺序：先工单闸门（本地计数，零成本），再每日闸门（一次条件 UPDATE）。
// 反过来的话「工单已满」也要先扣一次每日额度，等于把额度白送给被拦下的请求。
//
// charge=false 用于「缓存命中且额度已满」这一种**只需要判定、不需要扣减**的场景：
// 此时我们只想知道「每日闸门是否还有余量」来决定是否记 1 次消耗，
// 答案是否也就不扣（111-*.md §1.4 明确接受这个后果：已缓存的图可以无限次上传，
// 额度限的是「新图的外呼」而不是「重复上传这种行为本身」）。
func ReserveSensitiveImageQuotaWithDB(
	db *gorm.DB,
	policy SensitiveImageQuotaPolicy,
	userID int32,
	ticketID int64,
	date string,
	charge bool,
) (SensitiveImageQuotaDecision, error) {
	decision := SensitiveImageQuotaDecision{DailyLimit: policy.DailyUserLimit, PerTicketLimit: policy.PerTicketLimit}
	if db == nil {
		return decision, errSensitiveImageQuotaNoDatabase
	}
	if !policy.Enabled {
		// 闸门关闭：不检查、不计数。Allowed 恒真，Charged 恒假 ——
		// 后者让调用方不把「没扣额度」误报成「超额」。
		decision.Allowed = true
		return decision, nil
	}

	if ticketID > 0 && policy.PerTicketLimit > 0 {
		count, err := SensitiveImageTicketImageCountWithDB(db, ticketID)
		if err != nil {
			return decision, err
		}
		decision.TicketCount = count
		if count >= int64(policy.PerTicketLimit) {
			decision.Reason = "该工单图片数量已达检测上限"
			return decision, nil
		}
	}

	if !charge {
		// 只判定不扣减：读一次当日已用，足以回答「还有余量吗」。
		used, err := SensitiveImageQuotaUsageWithDB(db, userID, date)
		if err != nil {
			return decision, err
		}
		decision.UsedCount = used
		decision.Allowed = policy.DailyUserLimit <= 0 || int(used) < policy.DailyUserLimit
		if !decision.Allowed {
			decision.Reason = "今日敏感图片检测额度已用完"
		}
		return decision, nil
	}

	consumed, used, err := ConsumeSensitiveImageDailyQuotaWithDB(db, userID, date, policy.DailyUserLimit)
	decision.UsedCount = used
	if err != nil {
		return decision, err
	}
	decision.Allowed = consumed
	decision.Charged = consumed
	if !consumed {
		decision.Reason = "今日敏感图片检测额度已用完"
	}
	return decision, nil
}

// SetSensitiveImageQuotaPolicyWithDB 写三个配置键（管理员页面用）。
//
// nil 字段表示「不修改」，与仓内其它 *WithDB 配置写入保持同一套指针语义；
// 非 nil 但 <=0 的数值直接拒绝 —— 静默当成「不限制」会把计费外呼的上限撞穿。
func SetSensitiveImageQuotaPolicyWithDB(db *gorm.DB, enabled *bool, perTicket, dailyLimit *int) error {
	if db == nil {
		return errSensitiveImageQuotaNoDatabase
	}
	for _, value := range []*int{perTicket, dailyLimit} {
		if value != nil && *value <= 0 {
			return ErrSensitiveImageQuotaInvalidLimit
		}
	}
	if enabled != nil {
		value := "false"
		if *enabled {
			value = "true"
		}
		if err := saveSystemSettingValue(db, SensitiveImageQuotaEnabledSettingKey, value, "是否启用敏感图片检测额度"); err != nil {
			return err
		}
	}
	if perTicket != nil {
		if err := saveSystemSettingValue(db, SensitiveImagePerTicketLimitSettingKey, strconv.Itoa(*perTicket), "敏感图片检测每工单上限"); err != nil {
			return err
		}
	}
	if dailyLimit != nil {
		if err := saveSystemSettingValue(db, SensitiveImageDailyUserLimitSettingKey, strconv.Itoa(*dailyLimit), "敏感图片检测每人每日上限"); err != nil {
			return err
		}
	}
	return nil
}
