package registry

import (
	"strings"

	"gorm.io/gorm"
)

func prepareStatementsFor(mysqlDSN, sqlDSN string) bool {
	return mysqlDSN != "" || sqlDSN != ""
}

// 数据库连接来源的标识，仅用于启动日志（不能记录 DSN 本身，里面有密码）。
const (
	DSNSourceEnv    = "SQL_DSN 环境变量"
	DSNSourceConfig = "config.yaml 的 mysql 字段"
	DSNSourceSQLite = "SQLite"
)

// resolveDatabaseDSN 决定最终连哪个库：环境变量最高优先级。
//
// 优先级：SQL_DSN 环境变量 > config.yaml 的 mysql 字段 > SQLite。
// 为什么环境变量优先：容器部署时同一个镜像要换库只能改 environment，
// 若配置文件能压过它，改镜像/挂载点的成本反而更高，也容易出现
// 「改了环境变量却没生效」这种难查的错觉。返回值第二项是来源，供启动日志打印。
func resolveDatabaseDSN(mysqlConfig, sqlDSN string) (string, string) {
	if trimmed := strings.TrimSpace(sqlDSN); trimmed != "" {
		return trimmed, DSNSourceEnv
	}
	if trimmed := strings.TrimSpace(mysqlConfig); trimmed != "" {
		return trimmed, DSNSourceConfig
	}
	return "", DSNSourceSQLite
}

// sqliteProductionDSN adds connection-local pragmas supported by glebarez's
// SQLite driver. In-memory databases cannot use WAL, so test DSNs are kept
// untouched apart from the driver's own defaults.
func sqliteProductionDSN(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || isSQLiteMemoryPath(trimmed) {
		return trimmed
	}
	separator := "?"
	if strings.Contains(trimmed, "?") {
		separator = "&"
	}
	// BEGIN IMMEDIATE reserves the write lock when a write transaction starts.
	// With SQLite's default deferred mode, a transaction that first reads and
	// then writes can fail immediately with SQLITE_BUSY_SNAPSHOT while upgrading
	// its read snapshot; busy_timeout cannot help that upgrade. Reserving the
	// lock at BEGIN makes the configured busy timeout effective and keeps the
	// whole transaction retryable at its boundary.
	return trimmed + separator + "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

func isSQLiteMemoryPath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return strings.Contains(lower, ":memory:") || strings.Contains(lower, "mode=memory")
}

func configureSQLiteConnection(db *gorm.DB, path string) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		return err
	}
	if isSQLiteMemoryPath(path) {
		return nil
	}
	if err := db.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		return err
	}
	return db.Exec("PRAGMA synchronous = NORMAL").Error
}
