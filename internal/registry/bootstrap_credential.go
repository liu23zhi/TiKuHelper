package registry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Bind the file to the credential actually committed by this database, not the
// working directory or a reusable DSN. Never read a sidecar to choose a password.
func initialAdminFile(database *gorm.DB, hash string) (string, error) {
	dir := "data" // persisted by the Compose deployment; also separates in-memory instances by hash
	if database.Dialector.Name() == "sqlite" {
		var file string
		if err := database.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file).Error; err != nil {
			return "", err
		}
		if file != "" {
			dir = filepath.Dir(file)
		}
	}
	digest := sha256.Sum256([]byte(hash))
	return filepath.Abs(filepath.Join(dir, "admin-bootstrap-"+hex.EncodeToString(digest[:16])+".password"))
}

func logInitialAdminFile(path string) {
	logger.SysLog("初始管理员密码仅保存在本机文件：" + path + "；请在本机读取，登录改密后删除此文件。")
}

func reportInitialAdminFile(database *gorm.DB, hash string) {
	// A restart after commit (even before the first log line) identifies the
	// exact winning file. A changed password or an operator-deleted file is not recreated.
	path, err := initialAdminFile(database, hash)
	if err == nil {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			logInitialAdminFile(path)
		}
	}
}

func publishInitialAdminFile(path, password string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := createPrivateAdminFile(path)
	if err != nil {
		return err // do not open, truncate or remove someone else's file
	}
	_, err = file.WriteString(password + "\n")
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && runtime.GOOS != "windows" {
		// Persist the directory entry before committing a credential which needs it.
		var parent *os.File
		if parent, err = os.Open(dir); err == nil {
			err = parent.Sync()
			_ = parent.Close()
		}
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func generateInitialAdminCredential(database *gorm.DB, plan adminBootstrapPlan) error {
	var path, concurrentHash string
	err := database.Transaction(func(tx *gorm.DB) error {
		// Claim the PK *before* reading a missing row. Under InnoDB two
		// SELECT ... FOR UPDATE gap locks can coexist and then deadlock on INSERT.
		// This insert acquires the writer lock without mutating an existing credential.
		if !plan.exists {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).
				Create(initialAdministrator("")).Error; err != nil {
				return err
			}
		}
		var current struct{ Password string }
		err := tx.Table(entity.TableNameUser).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("password").Where("id = ?", 1).Take(&current).Error
		if err != nil {
			return err
		}
		if current.Password != "" {
			concurrentHash = current.Password
			return nil // another initializer or operator won; don't touch the account
		}
		plan.exists = true
		// rand.Text supplies at least 128 random bits; the prefix additionally
		// guarantees all four categories required by the password-change UI.
		password := "Aa1!" + rand.Text()
		hash, err := util.HashPassword(password)
		if err != nil {
			return err
		}
		plan.password = hash
		if err := writeAdminBootstrap(tx, plan); err != nil {
			return err
		}
		path, err = initialAdminFile(tx, hash)
		if err != nil {
			return err
		}
		// A failed publication rolls back the credential. An ambiguous COMMIT
		// never deletes a potentially live file; a future run uses the DB hash.
		return publishInitialAdminFile(path, password)
	})
	if err != nil {
		return fmt.Errorf("initialize private administrator credential: %w", err)
	}
	if path != "" {
		logInitialAdminFile(path)
	} else {
		reportInitialAdminFile(database, concurrentHash)
	}
	return nil
}
