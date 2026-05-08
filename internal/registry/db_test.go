package registry

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/configs"
)

func resetRegistryDBState(t *testing.T) {
	t.Helper()
	if db != nil {
		_ = CloseDB()
	}
	db = nil
	once = sync.Once{}
}

func openRegistryTestDB(t *testing.T) string {
	t.Helper()
	resetRegistryDBState(t)
	dbPath := filepath.Join(t.TempDir(), "registry-test.db")
	t.Cleanup(func() {
		resetRegistryDBState(t)
	})
	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: dbPath}})
	return dbPath
}

func TestRegisterDBAddsAccountDomainTables(t *testing.T) {
	openRegistryTestDB(t)

	tables := []string{
		"access_tokens",
		"system_settings",
		"price_rules",
		"payment_methods",
		"recharge_orders",
		"billing_records",
	}

	for _, table := range tables {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s to be migrated", table)
		}
	}
}

func TestRegisterDBAddsUserAccountColumnsAndSeedsAdminDefaults(t *testing.T) {
	openRegistryTestDB(t)

	columns := []string{"role", "status", "balance", "register_source"}
	for _, column := range columns {
		if !db.Migrator().HasColumn("users", column) {
			t.Fatalf("expected users.%s column to exist", column)
		}
	}

	var (
		username       string
		role           string
		status         string
		balance        float64
		registerSource sql.NullString
	)
	row := db.Raw("SELECT username, role, status, balance, register_source FROM users WHERE id = ?", 1).Row()
	if err := row.Scan(&username, &role, &status, &balance, &registerSource); err != nil {
		t.Fatalf("scan seeded admin: %v", err)
	}

	if username != "admin" {
		t.Fatalf("expected seeded admin username, got %s", username)
	}
	if role != "admin" {
		t.Fatalf("expected seeded admin role=admin, got %s", role)
	}
	if status != "enabled" {
		t.Fatalf("expected seeded admin status=enabled, got %s", status)
	}
	if balance != 0 {
		t.Fatalf("expected seeded admin balance=0, got %v", balance)
	}
	if !registerSource.Valid || registerSource.String != "system" {
		t.Fatalf("expected seeded admin register_source=system, got %+v", registerSource)
	}
}
