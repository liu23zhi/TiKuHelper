package registry

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"gorm.io/gorm"
)

func TestResolveMySQLPoolSettingsUsesBoundedDefaults(t *testing.T) {
	got := resolveMySQLPoolSettings(configs.MySQLPoolConfig{})
	if got.MaxOpenConns != 32 || got.MaxIdleConns != 8 {
		t.Fatalf("connection limits = open:%d idle:%d, want open:32 idle:8", got.MaxOpenConns, got.MaxIdleConns)
	}
	if got.ConnMaxLifetime != 30*time.Minute || got.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("connection durations = lifetime:%s idle:%s, want 30m and 5m", got.ConnMaxLifetime, got.ConnMaxIdleTime)
	}
}

func TestResolveMySQLPoolSettingsClampsIdleAndUsesConfiguredValues(t *testing.T) {
	got := resolveMySQLPoolSettings(configs.MySQLPoolConfig{
		MaxOpenConns:           12,
		MaxIdleConns:           20,
		ConnMaxLifetimeSeconds: 900,
		ConnMaxIdleTimeSeconds: 120,
	})
	if got.MaxOpenConns != 12 || got.MaxIdleConns != 12 {
		t.Fatalf("connection limits = open:%d idle:%d, want open:12 idle:12", got.MaxOpenConns, got.MaxIdleConns)
	}
	if got.ConnMaxLifetime != 15*time.Minute || got.ConnMaxIdleTime != 2*time.Minute {
		t.Fatalf("connection durations = lifetime:%s idle:%s, want 15m and 2m", got.ConnMaxLifetime, got.ConnMaxIdleTime)
	}
}

func TestConfigureMySQLConnectionAppliesPoolSettings(t *testing.T) {
	dsn := "file:" + filepath.ToSlash(t.TempDir()) + "/pool-config.db"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	settings, err := configureMySQLConnection(db, configs.MySQLPoolConfig{
		MaxOpenConns:           6,
		MaxIdleConns:           4,
		ConnMaxLifetimeSeconds: 600,
		ConnMaxIdleTimeSeconds: 90,
	})
	if err != nil {
		t.Fatalf("configure connection pool: %v", err)
	}
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 6 {
		t.Fatalf("max open connections = %d, want 6", stats.MaxOpenConnections)
	}
	if settings.MaxOpenConns != stats.MaxOpenConnections {
		t.Fatalf("scheduler limit = %d, pool limit = %d", settings.MaxOpenConns, stats.MaxOpenConnections)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 6 {
		t.Fatalf("pool setting changed unexpectedly: max open = %d", got)
	}
}
