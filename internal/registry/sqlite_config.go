package registry

import (
	"strings"

	"github.com/itihey/tikuAdapter/pkg/util"
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
//
// 参数定义已下沉到 pkg/util（SQLiteConcurrentDSN / SQLiteProductionDSN）：
// 并发测试与生产必须共用同一份定义，否则会悄悄漂移 —— 手写 DSN 的测试
// 在被锁挡下时断言照样是绿的，覆盖的却是错误分支（见
// task_doc/47-并发测试SQLite-DSN必须与生产一致.md）。这里只做委托，
// 保留函数名与既有调用点不变。
func sqliteProductionDSN(path string) string {
	return util.SQLiteProductionDSN(path)
}

func isSQLiteMemoryPath(path string) bool {
	return util.IsSQLiteMemoryDSN(path)
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
