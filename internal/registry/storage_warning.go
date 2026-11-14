package registry

import (
	"os"
	"strings"

	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// SQLite 存储警告（task_doc/108:297、109:474/486）。
//
// # 依据
//
// 108-决策记录第二轮.md:297 原文：
//
//	「不是完全删除 Sqlite，是冻结，非必要不做 Sqlite 优化。Sqlite 目前只留作测试，
//	 不需要进行性能优化等。**当使用 Sqlite 启动时候，需要给与警告。**」
//
// 108:18 决策表 Q7-1：「删 SQLite 的范围 → 不是删除，是冻结 —— 只留测试用，启动时警告」
// 109:474：「SQLite | 冻结（启动警告 + 文档说明）」
// 109:486 待定项 Q7-a：「SQLite 启动警告的形态（WARN / WARN+响应头 / WARN+管理页横幅）？」
//
// Q7-a 至今未拍板。本实现按用户原话「使用 Sqlite 应该显示的**悬浮**警告」取
// **WARN（启动日志）+ 前端悬浮横幅** 这一档，即 109:486 的三个候选里最完整的一个：
// 日志让运维在启动时就看见，悬浮条让每一个使用者（含普通用户）都看见。
//
// # 一条不可协商的性质
//
// **警告文案只有一个来源**：后端这个常量。前端不再自己写一份中文 ——
// 否则「日志说 A、界面说 B」这种漂移必然发生（本仓已有先例：充值档位表清空后的
// 前端文案与实际行为相反）。端点直接下发本常量，前端原样展示。
//
// # 边界
//
//   - 「冻结」不是「禁止」：SQLite 照常启动、照常服务，这里只警告不阻断。
//   - 判定用 GORM 的 `Dialector.Name()`（这是连接事实），不是配置字符串 ——
//     配置里 `mysql:` 为空但 `SQL_DSN` 指向 sqlite、或反之，都会让「读配置」判错。
//   - 端点只回 driver 与文案，**不回 DSN / 文件路径 / 账号**：那三样都是凭据面
//     （文件路径能定位数据文件，DSN 里有账号密码），普通用户不该看到。

// StorageDriverSQLite / StorageDriverMySQL 是 storage-status 端点的 driver 取值。
//
// 用字面量而非 gorm 的 dialector 名直接透出：后端内部名（"sqlite"/"mysql"）
// 恰好一致，但一旦上游改名，前端契约不该跟着漂。这里显式钉住契约值。
const (
	StorageDriverSQLite = "sqlite"
	StorageDriverMySQL  = "mysql"
)

// sqliteStorageWarning 是 SQLite 启动警告的**唯一文案来源**（日志与端点共用）。
//
// 措辞要求（来自 108:297 的「只留作测试」）：
//  1. 说清当前处于什么状态（使用 SQLite）；
//  2. 说清它对不对（仅为测试用途，不受支持）；
//  3. 给出行动（生产环境改用 MySQL / MariaDB）。
//
// 不写「性能差」这类模糊说法 —— 文档的处置是「冻结（不再优化）」，
// 承诺具体的性能结论会与实际不符。
const sqliteStorageWarning = "当前使用 SQLite 存储，仅供测试用途（已冻结，不再做性能优化）。生产环境请改用 MySQL / MariaDB。"

// StorageStatus 是 GET /api/system/storage-status 的响应体。
//
// 字段是**白名单式**的：只有这三项。任何新增字段都要先问「普通用户看到它安全吗」。
// 特别是不要加 Path / DSN / Database / Version —— 那些属于运维面。
type StorageStatus struct {
	// Driver 是 "sqlite" 或 "mysql"，前端据此决定是否显示悬浮警告。
	Driver string `json:"driver"`
	// Warning 是与启动日志同源的警告文案；非 SQLite 时为空串（不是 null）。
	Warning string `json:"warning"`
	// Production 表示当前进程是否以生产模式运行（TIKU_APP_MODE）。
	//
	// 它不是「有没有风险」的判据 —— 危险组合是 Production=true + Driver=sqlite
	// （生产跑测试库）。前端文案可以据此加强措辞，但显示与否只看 Driver。
	Production bool `json:"production"`
}

// normalizeStorageDriver 把 gorm dialector 名归一成对外的 driver 契约值。
//
// 未知 dialector 一律回落 "mysql"：这是「不误报」的方向 —— 把未知驱动报成 sqlite
// 会让所有用户看到一条假警告；报成 mysql 只是不警告（而未知驱动在本仓不可能是
// sqlite，因为 sqlite 是显式分支）。
func normalizeStorageDriver(dialectorName string) string {
	if strings.EqualFold(strings.TrimSpace(dialectorName), "sqlite") {
		return StorageDriverSQLite
	}
	return StorageDriverMySQL
}

// storageStatusForDB 由连接事实推导 StorageStatus。
// nil db（单测/初始化失败路径）按 mysql 处理并附空警告：警告是「已知使用 SQLite」
// 才发的，判定不了时不发 —— 否则前端会在每个单测和每次启动失败时闪一条假横幅。
func storageStatusForDB(database *gorm.DB) StorageStatus {
	if database == nil || database.Dialector == nil {
		return StorageStatus{Driver: StorageDriverMySQL, Warning: "", Production: currentApplicationIsProduction()}
	}
	driver := normalizeStorageDriver(database.Dialector.Name())
	status := StorageStatus{Driver: driver, Warning: "", Production: currentApplicationIsProduction()}
	if driver == StorageDriverSQLite {
		status.Warning = sqliteStorageWarning
	}
	return status
}

// StorageStatusForDB 是 storageStatusForDB 的导出别名，供 controller 层调用。
//
// 为什么需要导出：判定逻辑（dialector 名归一 + 文案同源 + 白名单字段）必须只有
// 一份实现。controller 若自己再写一遍 `Dialector.Name() == "sqlite"`，
// 文案与判据就会各自漂移 —— 那正是本仓已踩过的坑（充值档位表清空后的
// 前端文案与实际行为相反）。导出这一个函数，controller 只做 HTTP 编解码。
func StorageStatusForDB(database *gorm.DB) StorageStatus {
	return storageStatusForDB(database)
}

// WarnIfSQLiteStorage 在启动时按连接事实判定并记一条 WARN。
//
// 这是 108:297「当使用 Sqlite 启动时候，需要给与警告」的直接落点。
// 只警告、不返回错误、不阻断启动 —— 文档的处置是冻结而非禁止。
//
// 返回是否真的处于 SQLite（便于启动流程与测试断言「判定本身」而不是「日志文本」）。
func WarnIfSQLiteStorage(database *gorm.DB) bool {
	if storageStatusForDB(database).Driver != StorageDriverSQLite {
		return false
	}
	// 用 SysWarn 而不是 SysError：这不该让任何监控把启动判成失败。
	// 文案直接取常量，日志与端点因此不可能漂移。
	logger.SysWarn(sqliteStorageWarning)
	return true
}

// currentApplicationIsProduction 读 TIKU_APP_MODE 并归一成布尔。
//
// 复用 ResolveApplicationMode 而不是自己 switch：非法值在那里返回 error，
// 这里按「无法判定 → 视为生产」处理（与 resolveApplicationMode 的空值默认一致）。
// 用 ResolveApplicationMode 的另一好处是：将来模式口径变化时这里自动跟随。
func currentApplicationIsProduction() bool {
	mode, err := ResolveApplicationMode(os.Getenv("TIKU_APP_MODE"))
	if err != nil {
		return true
	}
	return mode == "production"
}
