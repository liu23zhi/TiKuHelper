package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestDatabasePasswordProviderReadsCandidatesWithoutReturningSecretsInErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiku.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, e := db.DB()
		if e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: service.GatewayCertificatePasswordsSettingKey, Value: `["first","","first","second"]`}).Error; err != nil {
		t.Fatal(err)
	}
	provider := &DatabasePasswordProvider{config: configs.Config{Database: configs.DatabaseConfig{Path: path}}}
	load := provider.Load
	t.Cleanup(func() { _ = provider.Close() })
	got, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestDatabasePasswordProviderReturnsEmptyForMissingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	provider := &DatabasePasswordProvider{config: configs.Config{Database: configs.DatabaseConfig{Path: path}}}
	load := provider.Load
	t.Cleanup(func() { _ = provider.Close() })
	if _, err := load(); err == nil {
		t.Fatal("missing system_settings table should be reported so the caller can retain its previous value")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only provider must not create a missing sqlite file, stat error=%v", err)
	}
}

func TestManagerRetainsPasswordsWhenProviderFails(t *testing.T) {
	manager := &Manager{PasswordProvider: func() ([]string, error) { return nil, errors.New("database unavailable") }}
	manager.certificatePasswords = []string{"known"}
	loaded, providerErr := manager.PasswordProvider()
	if providerErr == nil || loaded != nil {
		t.Fatalf("unexpected provider result: %#v %v", loaded, providerErr)
	}
	if len(manager.certificatePasswords) != 1 || manager.certificatePasswords[0] != "known" {
		t.Fatalf("manager lost last-known passwords: %#v", manager.certificatePasswords)
	}
}
