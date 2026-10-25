package registry

import "gorm.io/gorm"

// Keep upstream quiet Migrator unchanged: only MigrateColumn is disabled.
// Forward the underlying SQLite driver's optional nested-transaction support.
func (d sqliteQuietDialector) SavePoint(tx *gorm.DB, name string) error {
	if driver, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return driver.SavePoint(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}
func (d sqliteQuietDialector) RollbackTo(tx *gorm.DB, name string) error {
	if driver, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return driver.RollbackTo(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}
