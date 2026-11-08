package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// IssueUserLoginJWTWithDB atomically replaces the active login, metadata and
// optional legacy password hash. JWT signing happens before the short write
// transaction, so a failed login leaves the previous session intact.
func IssueUserLoginJWTWithDB(db *gorm.DB, user *entity.User, requestIP, passwordUpgrade string) (string, error) {
	if db == nil {
		return "", errors.New("数据库未初始化")
	}
	if user == nil || user.ID <= 0 {
		return "", errors.New("用户不存在")
	}
	sessionID, err := newLoginSessionID()
	if err != nil {
		return "", err
	}
	passwordHash := user.Password
	if passwordUpgrade != "" {
		passwordHash = passwordUpgrade
	}
	token := util.GenerateJwtForUserSession(user.ID, passwordHash, sessionID)
	if token == "" {
		return "", errors.New("jwt生成失败")
	}
	now, clientIP := time.Now(), strings.TrimSpace(requestIP)
	updates := map[string]any{"login_session_id": sessionID, "last_login_at": now, "last_login_ip": clientIP}
	if passwordUpgrade != "" {
		updates["password"] = passwordUpgrade
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		// Do not issue a session from credentials checked before a concurrent
		// password reset or account disable completed.
		// Preserve the local exact credential CAS across case-insensitive MySQL collations.
		var passwordPredicate string
		switch tx.Dialector.Name() {
		case "sqlite":
			passwordPredicate = "CAST(password AS BLOB) = CAST(? AS BLOB)"
		case "mysql":
			passwordPredicate = "CAST(password AS BINARY) = CAST(? AS BINARY)"
		default:
			return errors.New("unsupported exact login credential comparison driver")
		}
		result := tx.Model(&entity.User{}).Where("id = ?", user.ID).Where(passwordPredicate, user.Password).
			Where("LOWER(TRIM(COALESCE(status, ''))) IN ?", []string{"", "enabled"}).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	user.LoginSessionID, user.Password = sessionID, passwordHash
	user.LastLoginAt, user.LastLoginIP = &now, clientIP
	return token, nil
}

// RevokeUserLoginSessionWithDB uses the observed session as a condition: a
// delayed logout from an older device must not revoke a newer login.
func RevokeUserLoginSessionWithDB(db *gorm.DB, userID int32, expectedSessionID string) error {
	if db == nil {
		return errors.New("数据库未初始化")
	}
	tombstone, err := newLoginSessionID()
	if err != nil {
		return err
	}
	return db.Model(&entity.User{}).Where("id = ? AND COALESCE(login_session_id, '') = ?", userID, expectedSessionID).
		Update("login_session_id", tombstone).Error
}

func newLoginSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
