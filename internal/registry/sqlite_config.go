package registry

import (
	"strings"

	"gorm.io/gorm"
)

func prepareStatementsFor(mysqlDSN, sqlDSN string) bool {
	return mysqlDSN != "" || sqlDSN != ""
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
	return trimmed + separator + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
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
