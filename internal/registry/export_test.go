package registry

import (
	"testing"

	"gorm.io/gorm"
)

// This test-only bridge lets external HTTP tests use the production dialector
// without adding a public database factory to the application.
func NewQuietSQLiteTransactionTestDB(t *testing.T) *gorm.DB {
	return newQuietSQLiteTransactionTestDB(t)
}
