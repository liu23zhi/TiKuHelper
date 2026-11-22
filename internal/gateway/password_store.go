package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DatabasePasswordProvider reads the certificate password candidates directly
// from system_settings. It deliberately does not run migrations or write the
// database; the application service owns schema creation and updates.
type DatabasePasswordProvider struct {
	mu     sync.Mutex
	config configs.Config
	sqlDSN string
	db     *gorm.DB
}

// Close releases the read connection. The container keeps a provider for its
// lifetime; tests and embedders can call Close when the provider is discarded.
func (p *DatabasePasswordProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked()
}

func (p *DatabasePasswordProvider) closeLocked() error {
	if p.db == nil {
		return nil
	}
	sqlDB, err := p.db.DB()
	p.db = nil
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// NewDatabasePasswordProvider loads the same config/env sources as the main
// service. The returned callback is suitable for Manager.PasswordProvider.
func NewDatabasePasswordProvider() func() ([]string, error) {
	config, err := registry.ConfigWithError()
	if err != nil {
		return func() ([]string, error) { return nil, err }
	}
	return NewDatabasePasswordProviderWithConfig(config, os.Getenv("SQL_DSN"))
}

// NewDatabasePasswordProviderWithConfig is exposed for deterministic tests and
// for embedders that already loaded the service configuration.
func NewDatabasePasswordProviderWithConfig(config configs.Config, sqlDSN string) func() ([]string, error) {
	provider := &DatabasePasswordProvider{config: config, sqlDSN: strings.TrimSpace(sqlDSN)}
	return provider.Load
}

func (p *DatabasePasswordProvider) open() (*gorm.DB, error) {
	configuredDSN := strings.TrimSpace(p.sqlDSN)
	if configuredDSN == "" {
		configuredDSN = strings.TrimSpace(p.config.Mysql)
	}
	if configuredDSN != "" {
		return gorm.Open(mysql.Open(configuredDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	}
	path := strings.TrimSpace(p.config.Database.Path)
	if path == "" {
		path = "tiku.db"
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("certificate password database unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("certificate password database path is not a regular file")
	}
	return gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}

// Load returns a normalized, de-duplicated candidate list. Empty candidates
// are ignored; password bytes are never included in errors or diagnostics.
func (p *DatabasePasswordProvider) Load() ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.db == nil {
		db, err := p.open()
		if err != nil {
			return nil, errors.New("open database for certificate passwords failed")
		}
		p.db = db
	}
	var setting entity.SystemSetting
	// Select only the value column so MySQL connections without parseTime=true
	// still work; this read path does not need audit timestamps.
	err := p.db.Select("value").Where("key = ?", service.GatewayCertificatePasswordsSettingKey).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []string{}, nil
	}
	if err != nil {
		_ = p.closeLocked()
		return nil, fmt.Errorf("read certificate password candidates: %w", err)
	}
	var raw []string
	if strings.TrimSpace(setting.Value) != "" {
		if err := json.Unmarshal([]byte(setting.Value), &raw); err != nil {
			_ = p.closeLocked()
			return nil, fmt.Errorf("decode certificate password candidates: %w", err)
		}
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}
