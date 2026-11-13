package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 日志文件保留策略的配置化（task_doc/107 Q14 → 108 §8 / 110 §2 / 111 §2）。
//
// # 边界（Q14 明确口径，不要越界）
//
// 本文件**只处理应用日志文件**（logs/ 下的 Tiku* 与 logs/archive/*.zip），
// 不清理数据库：数据库里的日志表（blacklist_hits / logs 等）属于各自的业务
// 清理策略，Q14-1 已明确「数据库日志较小，不需要清理」。所以这里没有任何
// DELETE / VACUUM，只有对日志目录的检查与对 logger 策略的应用。
//
// # 为什么本文件不实现删除逻辑
//
// 清理能力（按年龄删归档、按总体积删最旧归档）在 pkg/logger 里已经完整实现，
// 且有一个**必须保住的正确设计**：pruneLogFiles / pruneLogFilesByAge 只收集
// 归档文件（isManagedLogArchive / isManagedLogBatchArchive），
// **从不删正在写入的活动文件**（Tiku.txt 等）——删正在写的文件会让写入直接失败。
//
// 本任务的口径是「把已存在的清理能力配置化」，因此这里只做三件事：
//  1. 从 system_settings 读三个参数（天数 / 总体积 / 单文件）；
//  2. 校验并落库（非法值拒绝写入，不静默 clamp）；
//  3. 通过 logger 的两个已导出的运行期 setter 把策略应用到运行中的 logger
//     （两个 setter 都会顺手做一次清理，这就是「立即清理一次」的实现）。
//
// 不在这里重新实现一遍候选文件挑选：那会让「哪些文件算归档」出现两份实现，
// 而 pkg/logger 的 managedLogPrefixes 注释里已经记录过这类漂移的后果
// （只改一处 → 轮转归档不再被清理 → 磁盘无限增长）。

// 系统设置键名。统一前缀 log_retention_（Q14 落地约定）：
//
//	log_retention_enabled                    "true" / "false"，默认 true（默认启用）
//	log_retention_max_age_days               保留天数，默认 30
//	log_retention_max_total_gib              总体积上限，单位 **GiB**，默认 10
//	log_retention_max_file_mib               单文件上限，单位 **MiB**，默认 100
//	log_retention_cleanup_interval_minutes   后台清理间隔（分钟），默认 60
//
// 与既有的 max_log_size_bytes（字节）/ max_ai_search_* 并存且互不影响：
// 旧键仍由 system_setting.go 负责，新键只由本文件读写。
const (
	// LogRetentionKeyPrefix 是全部日志清理键的统一前缀（Q14⑤）。
	//
	// 测试与文档用它做「有没有漏前缀」的断言，因此必须是常量而不是散落在
	// 各处的字面量拼接。
	LogRetentionKeyPrefix = "log_retention_"
	// LogRetentionEnabledSettingKey 是日志清理总开关。默认 true（Q14「默认启用」）。
	//
	// 关闭时 ApplyLogRetentionConfig 不做任何动作：既不清理，也不把已经生效的
	// 年龄闸门改成 0。已有归档保持原样，管理员重新打开后继续按新配置收敛。
	LogRetentionEnabledSettingKey = "log_retention_enabled"
	// LogRetentionMaxAgeDaysSettingKey 是归档保留天数（>0，单位天）。
	LogRetentionMaxAgeDaysSettingKey = "log_retention_max_age_days"
	// LogRetentionMaxTotalGiBSettingKey 是全部日志文件总大小上限，单位 **GiB**。
	LogRetentionMaxTotalGiBSettingKey = "log_retention_max_total_gib"
	// LogRetentionMaxFileMiBSettingKey 是单个日志文件大小上限，单位 **MiB**。
	LogRetentionMaxFileMiBSettingKey = "log_retention_max_file_mib"
	// LogRetentionCleanupIntervalMinutesSettingKey 是后台清理 goroutine 的间隔（分钟）。
	LogRetentionCleanupIntervalMinutesSettingKey = "log_retention_cleanup_interval_minutes"
)

// 字节单位口径（Q14 定稿，**按二进制单位落地**）。
//
// 「10 GB」在系统设置里存成 **10 GiB**：「总体积上限 = 10 GiB」= 10 × 1024³ =
// 10737418240 字节。同理「单文件 100 MB」存成 **100 MiB** = 104857600 字节。
//
// 为什么库里存 GiB / MiB 的整数而不是字节：
//   - 管理员在页面上填的就是 10 / 100，库里保持一致，日志与排查时可直接对照；
//   - 存字节会出现 10737418240 这种值，一次手滑改成 1073741824 就是 10 倍差距，
//     而两种写法在页面上都显示成「10 GB」量级，肉眼无法发现。
//
// 换算只发生在 LogRetentionSettings.Config()（→ 字节）这一处，
// 其它地方一律不自己乘 1024，避免出现两套换算。
const (
	logRetentionGiB int64 = 1 << 30 // 1 GiB = 1073741824 B
	logRetentionMiB int64 = 1 << 20 // 1 MiB = 1048576 B
)

// 默认值与边界。默认值来自 Q14 / 110 §2 / 111 §2.3。
const (
	// DefaultLogRetentionEnabled 默认启用（Q14④ 默认启用，与 logger 包里的
	// DefaultMaxLogAgeDays = 0「默认关闭」不同 —— 0 是那个包的常量语义，
	// 本层显式给出 30 天并默认打开，才符合 Q14 的决定）。
	DefaultLogRetentionEnabled = true
	// DefaultLogRetentionMaxAgeDays 默认 30 天。
	DefaultLogRetentionMaxAgeDays = 30
	// DefaultLogRetentionMaxTotalGiB 默认 10 GiB（全部日志文件总大小）。
	DefaultLogRetentionMaxTotalGiB int64 = 10
	// DefaultLogRetentionMaxFileMiB 默认 100 MiB（单文件轮转阈值）。
	DefaultLogRetentionMaxFileMiB int64 = 100
	// DefaultLogRetentionCleanupIntervalMinutes 默认每 60 分钟清理一次。
	DefaultLogRetentionCleanupIntervalMinutes = 60
)

// 上限只为挡住明显离谱的输入（防 1e18 之类把换算与磁盘统计撑爆），
// 不是业务限制。
const (
	MaxLogRetentionAgeDays               = 3650
	MaxLogRetentionTotalGiB        int64 = 1024
	MaxLogRetentionFileMiB         int64 = 10240
	MaxLogRetentionCleanupInterval       = 1440
)

// 日志清理配置的输入校验错误。全部是 400 而不是 500：
// 这些是管理员填错，不是服务故障。
var (
	// ErrLogRetentionAgeDaysOutOfRange 保留天数不在 (0, MaxLogRetentionAgeDays]。
	ErrLogRetentionAgeDaysOutOfRange = errors.New("日志保留天数必须大于 0 天且不超过 3650 天")
	// ErrLogRetentionTotalSizeOutOfRange 总体积上限不在 (0, MaxLogRetentionTotalGiB]。
	ErrLogRetentionTotalSizeOutOfRange = errors.New("日志总大小上限必须大于 0 GiB 且不超过 1024 GiB")
	// ErrLogRetentionMaxFileSizeOutOfRange 单文件上限不在 (0, MaxLogRetentionFileMiB]。
	ErrLogRetentionMaxFileSizeOutOfRange = errors.New("日志单文件大小上限必须大于 0 MiB 且不超过 10240 MiB")
	// ErrLogRetentionIntervalOutOfRange 后台清理间隔不在 (0, MaxLogRetentionCleanupInterval]。
	ErrLogRetentionIntervalOutOfRange = errors.New("日志清理间隔必须是大于 0 且不超过 1440 的分钟数")
	// ErrLogRetentionTotalBelowTwoFiles 总上限装不下两个单文件。
	//
	// 这是一条**跨字段**校验：单文件上限决定轮转阈值，总上限若小于两倍单文件，
	// 刚轮转出来的归档会在下一次清理时立刻被删 —— 「按大小清理」退化成
	// 「只保留当前活动文件」，等于把日志删光。
	ErrLogRetentionTotalBelowTwoFiles = errors.New("日志总大小上限必须至少能容纳两个单文件（总上限 ≥ 单文件 × 2）")
	// ErrLogRetentionNoDatabase 没有数据库连接，无法读写配置。
	ErrLogRetentionNoDatabase = errors.New("日志清理配置需要数据库连接")
)

// LogRetentionSettings 是管理 API 的读写形态。
//
// 三个业务参数（MaxAgeDays / MaxTotalGiB / MaxFileMiB）全部对外暴露，单位写进
// 字段名后缀，前端不用猜；MaxTotalBytes / MaxFileBytes 是只读派生值，
// 供页面显示「当前占用 2.3 GB / 10 GiB」这类精确文案，**不参与写入**。
type LogRetentionSettings struct {
	// Enabled 是日志清理总开关，默认 true。
	Enabled bool `json:"enabled"`
	// MaxAgeDays 是归档保留天数（>0）。
	MaxAgeDays int `json:"maxAgeDays"`
	// MaxTotalGiB 是全部日志文件总大小上限，单位 GiB。
	MaxTotalGiB int64 `json:"maxTotalGiB"`
	// MaxFileMiB 是单文件大小上限，单位 MiB。
	MaxFileMiB int64 `json:"maxFileMiB"`
	// CleanupIntervalMinutes 是后台清理间隔，单位分钟。
	CleanupIntervalMinutes int `json:"cleanupIntervalMinutes"`
	// MaxTotalBytes / MaxFileBytes 是上两项按 1024 进制换算出的字节数（只读）。
	MaxTotalBytes int64 `json:"maxTotalBytes"`
	MaxFileBytes  int64 `json:"maxFileBytes"`
}

// UpdateLogRetentionSettingsInput 是 PUT 的入参。
//
// 全部字段用指针：nil = 本次没有提交该字段 = 保持库中现值。
//
// 若用裸值，任何「只改天数」的请求都会把体积上限写成 0（未提交字段的零值），
// 而 0 又被校验拒绝 —— 于是「改一个参数」永远失败，或者更糟：
// 若某个版本放宽了 0，就会把体积上限静默改成无限。
type UpdateLogRetentionSettingsInput struct {
	Enabled                *bool  `json:"enabled"`
	MaxAgeDays             *int   `json:"maxAgeDays"`
	MaxTotalGiB            *int64 `json:"maxTotalGiB"`
	MaxFileMiB             *int64 `json:"maxFileMiB"`
	CleanupIntervalMinutes *int   `json:"cleanupIntervalMinutes"`
}

// LogRetentionConfig 是「已经换算成字节与 Duration」的生效形态。
//
// 存在意义：测试与内部调用不应该被迫构造 GiB 整数。按大小清理的用例需要
// 千字节级的阈值（真造 10 GiB 文件不现实），所以应用层入口直接吃字节。
type LogRetentionConfig struct {
	Enabled         bool
	MaxAgeDays      int
	MaxTotalBytes   int64
	MaxFileBytes    int64
	CleanupInterval time.Duration
}

// Config 把设置形态换算成生效形态（唯一的 GiB/MiB → 字节换算点）。
func (s LogRetentionSettings) Config() LogRetentionConfig {
	return LogRetentionConfig{
		Enabled:         s.Enabled,
		MaxAgeDays:      s.MaxAgeDays,
		MaxTotalBytes:   s.MaxTotalGiB * logRetentionGiB,
		MaxFileBytes:    s.MaxFileMiB * logRetentionMiB,
		CleanupInterval: time.Duration(s.CleanupIntervalMinutes) * time.Minute,
	}
}

// Settings 把生效形态换回设置形态（供「应用后回显」与测试断言使用）。
func (c LogRetentionConfig) Settings() LogRetentionSettings {
	settings := LogRetentionSettings{
		Enabled:                c.Enabled,
		MaxAgeDays:             c.MaxAgeDays,
		MaxTotalGiB:            c.MaxTotalBytes / logRetentionGiB,
		MaxFileMiB:             c.MaxFileBytes / logRetentionMiB,
		CleanupIntervalMinutes: int(c.CleanupInterval / time.Minute),
	}
	settings.MaxTotalBytes = settings.MaxTotalGiB * logRetentionGiB
	settings.MaxFileBytes = settings.MaxFileMiB * logRetentionMiB
	return settings
}

// LogRetentionApplyResult 报告「这次到底把哪些参数应用到了运行中的 logger」。
//
// 分成三个 applied 布尔而不是一个「成功/失败」，因为三个参数的生效方式不同：
// 天数与总体积有运行期 setter（立刻生效并顺手清一次），单文件上限没有
// （见 FileSizePendingRestart）。糊成一个字段会让页面显示「已生效」而实际
// 单文件阈值还是启动时的旧值 —— 那是会让人排查错方向的错误信息。
type LogRetentionApplyResult struct {
	Enabled          bool  `json:"enabled"`
	Applied          bool  `json:"applied"`
	MaxAgeDays       int   `json:"maxAgeDays"`
	MaxTotalBytes    int64 `json:"maxTotalBytes"`
	MaxFileBytes     int64 `json:"maxFileBytes"`
	AgeApplied       bool  `json:"ageApplied"`
	TotalSizeApplied bool  `json:"totalSizeApplied"`
	// FileSizePendingRestart 表示单文件上限要等进程重启才生效。
	//
	// 为什么：rotatingFile.maxBytes 在 openRotatingFile 时绑定，pkg/logger 只导出了
	// SetMaxTotalLogBytes 与 SetMaxLogAgeDays 两个运行期 setter，没有改 maxBytes 的入口。
	// 本次任务明确「logger 包只准读」，所以这里如实报告「待重启」而不是假装已生效。
	FileSizePendingRestart bool `json:"fileSizePendingRestart"`
}

// GetLogRetentionSettingsWithDB 读取日志清理配置。
//
// 缺记录、缺字段、解析失败、越界一律回落默认值（30 天 / 10 GiB / 100 MiB）。
// 回落而不是返回 0：0 在这里是「非法」而**不是**「不限制」，
// 把读失败当成 0 会让上层把清理关掉或按 0 字节上限删光全部归档。
func GetLogRetentionSettingsWithDB(db *gorm.DB) LogRetentionSettings {
	settings := LogRetentionSettings{
		Enabled:                DefaultLogRetentionEnabled,
		MaxAgeDays:             DefaultLogRetentionMaxAgeDays,
		MaxTotalGiB:            DefaultLogRetentionMaxTotalGiB,
		MaxFileMiB:             DefaultLogRetentionMaxFileMiB,
		CleanupIntervalMinutes: DefaultLogRetentionCleanupIntervalMinutes,
	}
	if db != nil {
		settings.Enabled = getLogRetentionBoolSetting(db, LogRetentionEnabledSettingKey, DefaultLogRetentionEnabled)
		settings.MaxAgeDays = int(getLogRetentionInt64Setting(db, LogRetentionMaxAgeDaysSettingKey, DefaultLogRetentionMaxAgeDays, MaxLogRetentionAgeDays))
		settings.MaxTotalGiB = getLogRetentionInt64Setting(db, LogRetentionMaxTotalGiBSettingKey, DefaultLogRetentionMaxTotalGiB, MaxLogRetentionTotalGiB)
		settings.MaxFileMiB = getLogRetentionInt64Setting(db, LogRetentionMaxFileMiBSettingKey, DefaultLogRetentionMaxFileMiB, MaxLogRetentionFileMiB)
		settings.CleanupIntervalMinutes = int(getLogRetentionInt64Setting(db, LogRetentionCleanupIntervalMinutesSettingKey, DefaultLogRetentionCleanupIntervalMinutes, MaxLogRetentionCleanupInterval))
	}
	settings.MaxTotalBytes = settings.MaxTotalGiB * logRetentionGiB
	settings.MaxFileBytes = settings.MaxFileMiB * logRetentionMiB
	return settings
}

// ResolveLogRetentionConfigWithDB 是读取 + 换算的组合入口。
func ResolveLogRetentionConfigWithDB(db *gorm.DB) LogRetentionConfig {
	return GetLogRetentionSettingsWithDB(db).Config()
}

// UpdateLogRetentionSettingsWithDB 校验并写入日志清理配置。
//
// 顺序固定：**先合并现值 → 再整体校验 → 最后才写库**。
// 非法值必须一步都不写：三个参数之间有跨字段约束（总上限 ≥ 单文件 × 2），
// 「逐项校验通过就逐项写库」会在中途留下「总上限已改成 1 GiB、单文件仍是
// 100 MiB」的不一致状态，且接口返回 400 时管理员会以为什么都没改。
func UpdateLogRetentionSettingsWithDB(db *gorm.DB, input UpdateLogRetentionSettingsInput) (LogRetentionSettings, error) {
	if db == nil {
		return LogRetentionSettings{}, ErrLogRetentionNoDatabase
	}
	next := GetLogRetentionSettingsWithDB(db)
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.MaxAgeDays != nil {
		next.MaxAgeDays = *input.MaxAgeDays
	}
	if input.MaxTotalGiB != nil {
		next.MaxTotalGiB = *input.MaxTotalGiB
	}
	if input.MaxFileMiB != nil {
		next.MaxFileMiB = *input.MaxFileMiB
	}
	if input.CleanupIntervalMinutes != nil {
		next.CleanupIntervalMinutes = *input.CleanupIntervalMinutes
	}
	if err := validateLogRetentionSettings(next); err != nil {
		return LogRetentionSettings{}, err
	}

	rows := []struct {
		key    string
		value  string
		remark string
	}{
		{LogRetentionEnabledSettingKey, strconv.FormatBool(next.Enabled), "启用日志文件清理（默认启用）"},
		{LogRetentionMaxAgeDaysSettingKey, strconv.Itoa(next.MaxAgeDays), "日志归档保留天数（天，默认 30）"},
		{LogRetentionMaxTotalGiBSettingKey, strconv.FormatInt(next.MaxTotalGiB, 10), "日志文件总大小上限（GiB，默认 10）"},
		{LogRetentionMaxFileMiBSettingKey, strconv.FormatInt(next.MaxFileMiB, 10), "日志单文件大小上限（MiB，默认 100）"},
		{LogRetentionCleanupIntervalMinutesSettingKey, strconv.Itoa(next.CleanupIntervalMinutes), "日志清理任务执行间隔（分钟，默认 60）"},
	}
	for _, row := range rows {
		if err := saveSystemSettingValue(db, row.key, row.value, row.remark); err != nil {
			return LogRetentionSettings{}, err
		}
	}
	return GetLogRetentionSettingsWithDB(db), nil
}

// validateLogRetentionSettings 校验一份完整配置（不是单字段）。
//
// 两个方向都拒绝：<=0（会把清理关掉或删光）与 > 上限（换算/统计会溢出）。
func validateLogRetentionSettings(settings LogRetentionSettings) error {
	if settings.MaxAgeDays <= 0 || settings.MaxAgeDays > MaxLogRetentionAgeDays {
		return ErrLogRetentionAgeDaysOutOfRange
	}
	if settings.MaxTotalGiB <= 0 || settings.MaxTotalGiB > MaxLogRetentionTotalGiB {
		return ErrLogRetentionTotalSizeOutOfRange
	}
	if settings.MaxFileMiB <= 0 || settings.MaxFileMiB > MaxLogRetentionFileMiB {
		return ErrLogRetentionMaxFileSizeOutOfRange
	}
	if settings.CleanupIntervalMinutes <= 0 || settings.CleanupIntervalMinutes > MaxLogRetentionCleanupInterval {
		return ErrLogRetentionIntervalOutOfRange
	}
	// 跨字段：总上限按 GiB 存、单文件按 MiB 存，统一换算成 MiB 再比。
	// 乘法不会溢出：两侧上限分别 1024 GiB 与 10240 MiB。
	if settings.MaxTotalGiB*(logRetentionGiB/logRetentionMiB) < settings.MaxFileMiB*2 {
		return ErrLogRetentionTotalBelowTwoFiles
	}
	return nil
}

// ApplyLogRetentionConfig 把一份已换算好的配置应用到运行中的 logger。
//
// 两个 setter 都会在应用后立刻做一次清理（logger.SetMaxTotalLogBytes 内部调
// pruneLogFilesByAge + pruneLogFiles，SetMaxLogAgeDays 内部调 pruneLogFilesByAge），
// 所以「保存配置」与「立即清理一次」走的是同一个入口，不会出现两套清理路径。
//
// 错误处理：两个 setter **都尝试**，用 errors.Join 汇总。
// 若第一个失败就返回，第二个策略会永远停在旧值上，而返回值只提示了一个错误，
// 管理员会以为「另一个参数也没问题」。
func ApplyLogRetentionConfig(cfg LogRetentionConfig) (LogRetentionApplyResult, error) {
	result := LogRetentionApplyResult{
		Enabled:                cfg.Enabled,
		MaxAgeDays:             cfg.MaxAgeDays,
		MaxTotalBytes:          cfg.MaxTotalBytes,
		MaxFileBytes:           cfg.MaxFileBytes,
		FileSizePendingRestart: true,
	}
	if !cfg.Enabled {
		// 关闭：一次 setter 都不调。
		//
		// 不能「用 MaxAgeDays=0 表示关闭」——那是 logger 包的开关语义，
		// 但本层的 0 是非法值；也不能用 MaxTotalBytes=0，那个 setter 明确拒绝 <=0。
		// 关闭的唯一语义就是「不再触发清理」，已有归档保持原样。
		return result, nil
	}
	if err := ValidateLogRetentionConfigBytes(cfg); err != nil {
		return result, err
	}

	var errs []error
	if err := logger.SetMaxLogAgeDays(cfg.MaxAgeDays); err != nil {
		errs = append(errs, fmt.Errorf("应用日志保留天数失败: %w", err))
	} else {
		result.AgeApplied = true
	}
	if err := logger.SetMaxTotalLogBytes(cfg.MaxTotalBytes); err != nil {
		errs = append(errs, fmt.Errorf("应用日志总体积上限失败: %w", err))
	} else {
		result.TotalSizeApplied = true
	}
	result.Applied = result.AgeApplied && result.TotalSizeApplied
	return result, errors.Join(errs...)
}

// ValidateLogRetentionConfigBytes 校验生效形态（字节 / Duration），
// 供直接调用 ApplyLogRetentionConfig 的入口与测试使用。
func ValidateLogRetentionConfigBytes(cfg LogRetentionConfig) error {
	if cfg.MaxAgeDays <= 0 {
		return ErrLogRetentionAgeDaysOutOfRange
	}
	if cfg.MaxTotalBytes <= 0 {
		return ErrLogRetentionTotalSizeOutOfRange
	}
	if cfg.MaxFileBytes <= 0 {
		return ErrLogRetentionMaxFileSizeOutOfRange
	}
	if cfg.MaxTotalBytes < cfg.MaxFileBytes*2 {
		return ErrLogRetentionTotalBelowTwoFiles
	}
	return nil
}

// ApplyLogRetentionWithDB 读取配置并应用（管理 API 的「保存」与「立即清理一次」都走这里）。
func ApplyLogRetentionWithDB(db *gorm.DB) (LogRetentionApplyResult, error) {
	if db == nil {
		return LogRetentionApplyResult{}, ErrLogRetentionNoDatabase
	}
	return ApplyLogRetentionConfig(ResolveLogRetentionConfigWithDB(db))
}

// StartLogRetentionCleanup 起一个独立的清理 goroutine，返回停止函数。
//
// 独立性（Q14⑦）：清理跑在自己的 goroutine 里，失败只记 logger.SysWarn，
// 绝不影响主服务 —— 磁盘清理失败的后果是「日志占更多空间」，而把服务搞挂是
// 更大的事故，两者不能挂钩。
//
// interval > 0：固定间隔（调用方显式指定，测试用）。
// interval <= 0：**每轮重新从 system_settings 解析**，于是管理员改「清理间隔」
// 后不需要重启进程就能生效。为此循环用 time.Timer 而不是一次性 NewTicker：
// ticker 的周期在创建时就固定了，改配置只能重启。
func StartLogRetentionCleanup(db *gorm.DB, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			wait := interval
			if wait <= 0 {
				wait = ResolveLogRetentionConfigWithDB(db).CleanupInterval
			}
			if wait <= 0 {
				wait = time.Duration(DefaultLogRetentionCleanupIntervalMinutes) * time.Minute
			}
			timer := time.NewTimer(wait)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			if _, err := ApplyLogRetentionWithDB(db); err != nil {
				logger.SysWarn("日志清理任务失败（不影响主服务，下个周期重试）：" + err.Error())
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

// getLogRetentionInt64Setting 读一个正整数设置，越界或解析失败回落默认值。
func getLogRetentionInt64Setting(db *gorm.DB, key string, defaultValue int64, maxValue int64) int64 {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.FormatInt(defaultValue, 10)))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > maxValue {
		return defaultValue
	}
	return value
}

// getLogRetentionBoolSetting 读一个布尔设置。
//
// 与 getLogRetentionInt64Setting 同构：**无法解析时回落默认值，而不是 false**。
// 只认明确的 true/false 写法（含 1/0、yes/no、on/off），其余字符串一律当「读不出来」。
func getLogRetentionBoolSetting(db *gorm.DB, key string, defaultValue bool) bool {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, ""))
	if raw == "" {
		return defaultValue
	}
	switch strings.ToLower(raw) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return defaultValue
}
