package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"
)

// CompleteLocalAdminSetup never creates a database, runs migrations, starts
// workers, or reconfigures an already personalized administrator.
func CompleteLocalAdminSetup(config configs.Config, current, replacement string) error {
	if !util.IsPublicAdminSetupPassword(current) || strings.TrimSpace(replacement) == "" ||
		util.IsPublicAdminSetupPassword(replacement) || util.ValidateBcryptPasswordBytes(replacement) != nil {
		return errors.New("请输入当前初始化口令和不同的非公共新密码（最多 72 个 UTF-8 字节）")
	}
	var dialector gorm.Dialector
	dsn, _ := resolveDatabaseDSN(config.Mysql, os.Getenv("SQL_DSN"))
	if dsn != "" {
		normalized, _ := normalizeMySQLDSN(dsn)
		dialector = openMySQLDialector(normalized)
	} else {
		configured := config.Database.Path
		if configured == "" {
			configured = "tiku.db"
		}
		filename, memory, err := sqliteFilesystemPath(configured)
		if err != nil {
			return errors.New("SQLite 数据库配置无效")
		}
		if memory {
			return errors.New("内存库不能从其他进程初始化；请在启动服务时设置 TIKU_ADMIN_PASSWORD")
		}
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return errors.New("无法确定 SQLite 数据库位置")
		}
		dialector = sqlite.Open(util.SQLiteConcurrentDSN(sqliteFileURI(absolute).String() + "?mode=rw"))
	}
	d, err := gorm.Open(dialector, &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		return errors.New("无法连接现有数据库；请在原服务工作目录或 Compose 容器内执行，并先启动服务")
	}
	pool, err := d.DB()
	if err != nil {
		return errors.New("无法访问现有数据库")
	}
	defer pool.Close()
	hash, err := util.HashPassword(replacement)
	if err != nil {
		return errors.New("新密码无效")
	}
	err = d.Transaction(func(tx *gorm.DB) error {
		var admin entity.User
		if err := tx.Select("id", "role", "password", "login_session_id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&admin, 1).Error; err != nil {
			return err
		}
		if match, _ := util.VerifyPassword(admin.Password, current); !match || !strings.EqualFold(strings.TrimSpace(admin.Role), "admin") {
			return errors.New("not an initial credential")
		}
		if err := writeAdminBootstrap(tx, adminBootstrapPlan{exists: true, original: admin.Password, password: hash}); err != nil {
			return err
		}
		// A successful change must also retire any prior session of a historical default account.
		return service.RevokeUserLoginSessionWithDB(tx, admin.ID, admin.LoginSessionID)
	})
	if err != nil {
		return errors.New("未更改管理员密码：仅能以正确的当前公共初始化口令设置一次；请核对数据库与账号状态")
	}
	return nil
}
