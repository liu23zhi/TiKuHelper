package registry

import (
	"time"

	"github.com/itihey/tikuAdapter/configs"
	"gorm.io/gorm"
)

const (
	defaultMySQLMaxOpenConns    = 32
	defaultMySQLMaxIdleConns    = 8
	defaultMySQLConnMaxLifetime = 30 * time.Minute
	defaultMySQLConnMaxIdleTime = 5 * time.Minute
)

type mysqlPoolSettings struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func resolveMySQLPoolSettings(config configs.MySQLPoolConfig) mysqlPoolSettings {
	settings := mysqlPoolSettings{
		MaxOpenConns:    config.MaxOpenConns,
		MaxIdleConns:    config.MaxIdleConns,
		ConnMaxLifetime: configuredPoolDuration(config.ConnMaxLifetimeSeconds, defaultMySQLConnMaxLifetime),
		ConnMaxIdleTime: configuredPoolDuration(config.ConnMaxIdleTimeSeconds, defaultMySQLConnMaxIdleTime),
	}
	if settings.MaxOpenConns <= 0 {
		settings.MaxOpenConns = defaultMySQLMaxOpenConns
	}
	if settings.MaxIdleConns <= 0 {
		settings.MaxIdleConns = defaultMySQLMaxIdleConns
	}
	if settings.MaxIdleConns > settings.MaxOpenConns {
		settings.MaxIdleConns = settings.MaxOpenConns
	}
	return settings
}

func configuredPoolDuration(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func configureMySQLConnection(db *gorm.DB, config configs.MySQLPoolConfig) (mysqlPoolSettings, error) {
	settings := resolveMySQLPoolSettings(config)
	sqlDB, err := db.DB()
	if err != nil {
		return mysqlPoolSettings{}, err
	}
	sqlDB.SetMaxOpenConns(settings.MaxOpenConns)
	sqlDB.SetMaxIdleConns(settings.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(settings.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(settings.ConnMaxIdleTime)
	return settings, nil
}
