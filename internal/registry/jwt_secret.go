package registry

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const JWTSecretSettingKey = "jwt_secret"

// ResolveJWTSecretForDB resolves the JWT secret from environment, config, or DB.
// A valid environment value has highest priority and is persisted to the DB.
func ResolveJWTSecretForDB(db *gorm.DB, configured string) (string, error) {
	return resolveJWTSecret(db, configured, os.Getenv("JWT_SECRET"))
}

func resolveJWTSecret(db *gorm.DB, configured string, environment string) (string, error) {
	if db == nil {
		return "", errors.New("database is unavailable")
	}

	envSecret := strings.TrimSpace(environment)
	if envSecret != "" {
		if err := util.ValidateJWTSecret(envSecret); err == nil {
			return persistJWTSecret(db, envSecret)
		}
		logger.SysLog("JWT_SECRET 环境变量不符合要求，将自动生成随机密钥")
		return generateAndPersistJWTSecret(db)
	}

	configuredSecret := strings.TrimSpace(configured)
	if configuredSecret != "" {
		if err := util.ValidateJWTSecret(configuredSecret); err == nil {
			return persistJWTSecret(db, configuredSecret)
		}
		logger.SysLog("配置中的 jwtSecret 不符合要求，将自动生成随机密钥")
		return generateAndPersistJWTSecret(db)
	}

	storedSecret, err := loadStoredJWTSecret(db)
	if err != nil {
		return "", err
	}
	if err := util.ValidateJWTSecret(storedSecret); err == nil {
		return storedSecret, nil
	}

	return generateAndPersistJWTSecret(db)
}

func loadStoredJWTSecret(db *gorm.DB) (string, error) {
	var setting entity.SystemSetting
	err := db.Where(map[string]any{"key": JWTSecretSettingKey}).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(setting.Value), nil
}

func generateAndPersistJWTSecret(db *gorm.DB) (string, error) {
	generated, err := util.GenerateJWTSecret()
	if err != nil {
		return "", fmt.Errorf("generate JWT secret: %w", err)
	}
	return persistJWTSecret(db, generated)
}

func persistJWTSecret(db *gorm.DB, secret string) (string, error) {
	resolved := strings.TrimSpace(secret)
	if err := util.ValidateJWTSecret(resolved); err != nil {
		return "", err
	}

	var setting entity.SystemSetting
	err := db.Where(map[string]any{"key": JWTSecretSettingKey}).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		setting = entity.SystemSetting{
			Key:    JWTSecretSettingKey,
			Value:  resolved,
			Remark: "JWT 签名密钥",
		}
		if err := db.Create(&setting).Error; err != nil {
			return "", err
		}
		return resolved, nil
	}
	if err != nil {
		return "", err
	}

	setting.Value = resolved
	if err := db.Save(&setting).Error; err != nil {
		return "", err
	}
	return resolved, nil
}
