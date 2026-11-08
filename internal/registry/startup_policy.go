package registry

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// ResolveApplicationMode is deliberately independent of GIN_MODE.
func ResolveApplicationMode(configured string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "", "production":
		return "production", nil
	case "development":
		return "development", nil
	default:
		return "", errors.New("TIKU_APP_MODE must be production or development")
	}
}

type adminBootstrapPlan struct {
	exists             bool
	original, password string
}

// Keep the same 8-rune/ASCII four-category policy as account password changes.
// bcrypt's separate 72-byte boundary must be checked before hashing.
func validateBootstrapPassword(password string) error {
	if len(password) > 72 {
		return errors.New("admin password exceeds bcrypt's 72-byte limit")
	}
	if len([]rune(password)) < 8 {
		return errors.New("admin bootstrap password requires at least 8 characters and upper/lower/digit/special")
	}
	var upper, lower, digit, special bool
	for _, ch := range password {
		switch {
		case ch >= 'A' && ch <= 'Z':
			upper = true
		case ch >= 'a' && ch <= 'z':
			lower = true
		case ch >= '0' && ch <= '9':
			digit = true
		default:
			special = true
		}
	}
	if !upper || !lower || !digit || !special {
		return errors.New("admin bootstrap password requires at least 8 characters and upper/lower/digit/special")
	}
	return nil
}

// This function only reads. Even existing default plaintext is rejected before
// any migration/account-default write in production; environment never resets it.
func resolveAdminBootstrap(database *gorm.DB, mode, environment string) (adminBootstrapPlan, error) {
	var plan adminBootstrapPlan
	if database != nil && database.Migrator().HasTable(entity.TableNameUser) {
		var row struct{ Password string }
		err := database.Table(entity.TableNameUser).Select("password").Where("id = ?", 1).Take(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return plan, fmt.Errorf("read admin credential: %w", err)
		}
		plan.exists = err == nil
		plan.original = row.Password
	}
	if plan.exists && plan.original != "" {
		if matched, _ := util.VerifyPassword(plan.original, "123456"); mode == "production" && matched {
			return plan, errors.New("production refuses the known default admin credential; change it through the existing account workflow")
		}
		if strings.HasPrefix(plan.original, "$2a$") || strings.HasPrefix(plan.original, "$2b$") || strings.HasPrefix(plan.original, "$2y$") {
			return plan, nil
		}
		hashed, err := util.HashPassword(plan.original)
		if err != nil {
			return plan, fmt.Errorf("upgrade existing admin credential: %w", err)
		}
		plan.password = hashed
		return plan, nil
	}
	password := strings.TrimSpace(environment)
	if mode == "production" {
		if err := validateBootstrapPassword(password); err != nil {
			return plan, err
		}
	} else if password == "" {
		password = "123456"
	}
	hashed, err := util.HashPassword(password)
	if err != nil {
		return plan, fmt.Errorf("hash admin bootstrap credential: %w", err)
	}
	plan.password = hashed
	return plan, nil
}

func applyAdminBootstrap(database *gorm.DB, plan adminBootstrapPlan) error {
	if plan.exists {
		if plan.password == "" {
			return nil
		}
		// Credential versions are byte identities, not text values under the
		// database's potentially case-insensitive collation.
		var predicate string
		switch database.Dialector.Name() {
		case "sqlite":
			predicate = "CAST(password AS BLOB) = CAST(? AS BLOB)"
		case "mysql":
			predicate = "CAST(password AS BINARY) = CAST(? AS BINARY)"
		default:
			return errors.New("unsupported driver for admin credential compare-and-swap")
		}
		result := database.Model(&entity.User{}).Where("id = ?", 1).
			Where("("+predicate+" OR (password IS NULL AND ? = ''))", plan.original, plan.original).
			Update("password", plan.password)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("admin credential changed during bootstrap; refusing overwrite")
		}
		return nil
	}
	return database.Create(&entity.User{ID: 1, Username: "admin", Password: plan.password, Nickname: "管理员", Role: "admin", Status: "enabled", Balance: 0, RegisterSource: "system"}).Error
}

// SQLite file URI paths are decoded exactly once. url.Parse already decodes
// Path; only Opaque (file:relative) requires unescaping. The same identity feeds
// read-only preflight and directory creation before the original DSN is opened.
func sqliteFilesystemPath(configured string) (string, bool, error) {
	if configured == ":memory:" {
		return "", true, nil
	}
	if !strings.HasPrefix(configured, "file:") {
		return configured, false, nil
	}
	u, err := url.Parse(configured)
	if err != nil || u.Host != "" || u.Fragment != "" {
		return "", false, errors.New("invalid SQLite database URI")
	}
	if u.Query().Get("mode") == "memory" || u.Opaque == ":memory:" {
		return "", true, nil
	}
	path := u.Path
	if u.Opaque != "" {
		path, err = url.PathUnescape(u.Opaque)
		if err != nil {
			return "", false, errors.New("invalid SQLite database URI")
		}
	}
	if path == "" {
		return "", false, errors.New("invalid SQLite database URI")
	}
	return path, false, nil
}

// Inspect existing SQLite credentials through mode=ro. Absent databases are not
// created (nor their directories) until bootstrap validation succeeds.
func preflightAdminBootstrap(config configs.Config, mode, environment string) (adminBootstrapPlan, error) {
	if db != nil {
		return resolveAdminBootstrap(db, mode, environment)
	}
	dsn := config.Mysql
	if dsn == "" {
		dsn = os.Getenv("SQL_DSN")
	}
	var conn gorm.Dialector
	if dsn != "" {
		conn = mysql.Open(dsn)
	} else {
		path := config.Database.Path
		if path == "" {
			path = "tiku.db"
		}
		if path == ":memory:" {
			return resolveAdminBootstrap(nil, mode, environment)
		}
		filename, memory, err := sqliteFilesystemPath(path)
		if err != nil {
			return adminBootstrapPlan{}, err
		}
		if memory {
			return resolveAdminBootstrap(nil, mode, environment)
		}
		path = filename
		_, err = os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return resolveAdminBootstrap(nil, mode, environment)
		}
		if err != nil {
			return adminBootstrapPlan{}, errors.New("cannot inspect SQLite database path")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return adminBootstrapPlan{}, errors.New("invalid SQLite database path")
		}
		readonly := (&url.URL{Scheme: "file", Path: absolute}).String() + "?mode=ro"
		conn = sqlite.Open(readonly)
	}
	inspection, err := gorm.Open(conn, &gorm.Config{Logger: newDatabaseLogger()})
	if err != nil {
		return adminBootstrapPlan{}, errors.New("cannot open database for admin credential preflight")
	}
	sqlDB, err := inspection.DB()
	if err != nil {
		return adminBootstrapPlan{}, errors.New("cannot access admin credential preflight")
	}
	defer sqlDB.Close()
	return resolveAdminBootstrap(inspection, mode, environment)
}

// RegisterDBWithError returns policy failures before the legacy migration path.
func RegisterDBWithError(config configs.Config) (*gorm.DB, error) {
	mode, err := ResolveApplicationMode(os.Getenv("TIKU_APP_MODE"))
	if err != nil {
		return nil, err
	}
	plan, err := preflightAdminBootstrap(config, mode, os.Getenv("TIKU_ADMIN_PASSWORD"))
	if err != nil {
		return nil, err
	}
	return registerPreparedDB(config, plan), nil
}
