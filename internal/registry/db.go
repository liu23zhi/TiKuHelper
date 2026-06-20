package registry

import (
	"fmt"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/dao"
	"gorm.io/driver/mysql"
	l "gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"sync"
)

var db *gorm.DB
var once sync.Once

// CloseDB close db
func CloseDB() error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	if err != nil {
		return err
	}
	return nil
}

func ensureAccountDomainDefaults(db *gorm.DB) {
	updates := []string{
		"UPDATE users SET role = 'admin' WHERE id = 1 AND (role IS NULL OR role = '')",
		"UPDATE users SET role = 'user' WHERE id <> 1 AND (role IS NULL OR role = '')",
		"UPDATE users SET status = 'enabled' WHERE status IS NULL OR status = ''",
		"UPDATE users SET register_source = 'system' WHERE id = 1 AND (register_source IS NULL OR register_source = '')",
		"UPDATE users SET register_source = 'legacy' WHERE id <> 1 AND (register_source IS NULL OR register_source = '')",
		"UPDATE users SET balance = 0 WHERE balance IS NULL",
	}
	for _, statement := range updates {
		if err := db.Exec(statement).Error; err != nil {
			logger.FatalLog(fmt.Errorf("initialize account-domain defaults fail: %w", err))
		}
	}
}

func ensureUserPhoneBindingSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return ensureUserPhoneBindingSchemaForDriver(db, db.Dialector.Name())
}

func ensureUserPhoneBindingSchemaForDriver(db *gorm.DB, driverName string) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil
	}

	normalizedDriver := strings.TrimSpace(strings.ToLower(driverName))
	if normalizedDriver == "" {
		normalizedDriver = strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	}

	if normalizedDriver == "sqlite" {
		if !db.Migrator().HasColumn(entity.TableNameUser, "phone") {
			if err := db.Exec("ALTER TABLE users ADD COLUMN phone varchar(32)").Error; err != nil {
				return err
			}
		}
		if !db.Migrator().HasColumn(entity.TableNameUser, "phone_bound_at") {
			if err := db.Exec("ALTER TABLE users ADD COLUMN phone_bound_at datetime").Error; err != nil {
				return err
			}
		}
		if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_phone") {
			if err := db.Exec("CREATE UNIQUE INDEX idx_users_phone ON users(phone)").Error; err != nil {
				return err
			}
		}
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "phone") {
		if err := db.Migrator().AddColumn(&entity.User{}, "Phone"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "phone_bound_at") {
		if err := db.Migrator().AddColumn(&entity.User{}, "PhoneBoundAt"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_phone") {
		if err := db.Migrator().CreateIndex(&entity.User{}, "Phone"); err != nil {
			return err
		}
	}
	return nil
}

func ensureSMSVerificationSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return ensureSMSVerificationSchemaForDriver(db, db.Dialector.Name())
}

func ensureSMSVerificationSchemaForDriver(db *gorm.DB, driverName string) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameSMSVerificationCode) {
		return nil
	}

	normalizedDriver := strings.TrimSpace(strings.ToLower(driverName))
	if normalizedDriver == "" {
		normalizedDriver = strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	}

	if normalizedDriver == "sqlite" {
		if !db.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
			legacyTableName := entity.TableNameSMSVerificationCode + "__legacy_scene"
			statements := []string{
				"ALTER TABLE sms_verification_codes RENAME TO sms_verification_codes__legacy_scene",
				"CREATE TABLE sms_verification_codes (`id` integer PRIMARY KEY AUTOINCREMENT,`phone` varchar(32) NOT NULL,`scene` varchar(32) NOT NULL DEFAULT '',`code_hash` char(64) NOT NULL,`status` varchar(32) NOT NULL,`expires_at` datetime NOT NULL,`consumed_at` datetime,`request_ip` varchar(64),`created_at` datetime,`updated_at` datetime)",
				"INSERT INTO sms_verification_codes (`id`,`phone`,`scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at`) SELECT `id`,`phone`,'' AS `scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at` FROM sms_verification_codes__legacy_scene",
				"DROP TABLE sms_verification_codes__legacy_scene",
			}
			for _, statement := range statements {
				if err := db.Exec(statement).Error; err != nil {
					_ = db.Exec("ALTER TABLE " + legacyTableName + " RENAME TO " + entity.TableNameSMSVerificationCode).Error
					return err
				}
			}
		}
		if !db.Migrator().HasIndex(entity.TableNameSMSVerificationCode, "idx_sms_verification_codes_scene") {
			if err := db.Exec("CREATE INDEX idx_sms_verification_codes_scene ON sms_verification_codes(scene)").Error; err != nil {
				return err
			}
		}
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
		if err := db.Migrator().AddColumn(&entity.SMSVerificationCode{}, "Scene"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameSMSVerificationCode, "idx_sms_verification_codes_scene") {
		if err := db.Migrator().CreateIndex(&entity.SMSVerificationCode{}, "Scene"); err != nil {
			return err
		}
	}
	return nil
}

// RegisterDB 注册数据库
func RegisterDB(config configs.Config) *gorm.DB {
	once.Do(func() {
		var err error
		var conn gorm.Dialector
		if config.Mysql != "" {
			conn = mysql.Open(config.Mysql)
		} else if os.Getenv("SQL_DSN") != "" {
			conn = mysql.Open(os.Getenv("SQL_DSN"))
		} else {
			// 使用 SQLite，从配置读取路径，默认为 tiku.db（向后兼容）
			dbPath := config.Database.Path
			if dbPath == "" {
				dbPath = "tiku.db"
			}

			// 确保数据库文件所在目录存在
			dbDir := filepath.Dir(dbPath)
			if dbDir != "." && dbDir != "" {
				if err := os.MkdirAll(dbDir, 0755); err != nil {
					logger.FatalLog(fmt.Errorf("create database directory fail: %w", err))
				}
			}

			conn = sqlite.Open(dbPath)
		}
		db, err = gorm.Open(conn, &gorm.Config{
			PrepareStmt: true,
			Logger:      l.Default.LogMode(l.Info),
		})
		if err != nil {
			logger.FatalLog(fmt.Errorf("open db fail: %w", err))
		}
		dao.SetDefault(db)
	})
	err := db.AutoMigrate(&entity.Tiku{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	if err := ensureUserPhoneBindingSchema(db); err != nil {
		logger.FatalLog(fmt.Errorf("prepare user phone binding schema fail: %w", err))
	}

	err = db.AutoMigrate(&entity.User{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	err = db.AutoMigrate(&entity.Log{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	err = db.AutoMigrate(&entity.SearchAccountConfig{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	err = db.AutoMigrate(&entity.AISearchConfig{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	if err := ensureSMSVerificationSchema(db); err != nil {
		logger.FatalLog(fmt.Errorf("prepare sms verification schema fail: %w", err))
	}

	err = db.AutoMigrate(
		&entity.AccessToken{},
		&entity.SystemSetting{},
		&entity.PriceRule{},
		&entity.PaymentMethod{},
		&entity.RechargeOrder{},
		&entity.BillingRecord{},
		&entity.AIModelArchitecture{},
		&entity.SMSVerificationCode{},
		&entity.CaptchaChallenge{},
		&entity.CaptchaToken{},
	)
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	ensureAccountDomainDefaults(db)

	// 首先需要注册一个默认用户
	user := entity.User{
		ID:             1,
		Username:       "admin",
		Password:       "123456",
		Nickname:       "管理员",
		Role:           "admin",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "system",
	}
	_ = dao.User.Create(&user)
	ensureAccountDomainDefaults(db)
	return db
}
