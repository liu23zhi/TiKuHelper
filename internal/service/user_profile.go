package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const UserNicknameMaxLength = 255

var ErrUserProfileUsernameTaken = errors.New("用户名已经被使用")

type UpdateUserProfileInput struct {
	Username string
	Nickname string
}

// UpdateCurrentUserProfileWithDB changes only the authenticated user's public profile fields.
// The transaction keeps the read, uniqueness check, and update together so a concurrent
// username change cannot bypass the application-level check before the unique index runs.
func UpdateCurrentUserProfileWithDB(db *gorm.DB, userID int32, input UpdateUserProfileInput) (entity.User, error) {
	username := strings.TrimSpace(input.Username)
	nickname := strings.TrimSpace(input.Nickname)
	if err := util.ValidateRestrictedName(username, util.UsernameMaxLength, "用户名"); err != nil {
		return entity.User{}, err
	}
	if utf8.RuneCountInString(nickname) > UserNicknameMaxLength {
		return entity.User{}, fmt.Errorf("昵称长度不能超过%d个字符", UserNicknameMaxLength)
	}

	var updated entity.User
	err := db.Transaction(func(tx *gorm.DB) error {
		var current entity.User
		if err := tx.Where("id = ?", userID).First(&current).Error; err != nil {
			return err
		}

		// Compare exactly. A case-only change is still a real change and the database
		// unique index remains the final authority for any concurrent update.
		if username != current.Username {
			var occupied entity.User
			err := tx.Where("username = ? AND id <> ?", username, userID).First(&occupied).Error
			switch err {
			case nil:
				return ErrUserProfileUsernameTaken
			case gorm.ErrRecordNotFound:
			default:
				return err
			}
		}

		if err := tx.Model(&entity.User{}).Where("id = ?", userID).Updates(map[string]any{
			"username": username,
			"nickname": nickname,
		}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", userID).First(&updated).Error
	})
	if err != nil {
		return entity.User{}, err
	}
	return updated, nil
}
