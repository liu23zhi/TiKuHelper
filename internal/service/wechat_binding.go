package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func UpdateUserWechatBindingWithDB(db *gorm.DB, targetUserID int32, rawWechatID string, forceReassign bool) (entity.User, error) {
	var updated entity.User
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", targetUserID).First(&updated).Error; err != nil {
			return err
		}

		wechatID := strings.TrimSpace(rawWechatID)
		if wechatID == "" {
			updated.WechatID = nil
			updated.WechatBoundAt = nil
			return tx.Save(&updated).Error
		}
		if updated.WechatID != nil && strings.TrimSpace(*updated.WechatID) == wechatID {
			return nil
		}

		var occupied entity.User
		err := tx.Where("wechat_id = ? AND id <> ?", wechatID, targetUserID).First(&occupied).Error
		if err == nil && !forceReassign {
			return fmt.Errorf("该微信已绑定其他账号")
		}
		if err == nil && forceReassign {
			if err := tx.Model(&entity.User{}).Where("id = ?", occupied.ID).Updates(map[string]any{
				"wechat_id":       nil,
				"wechat_bound_at": nil,
			}).Error; err != nil {
				return err
			}
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		now := time.Now()
		updated.WechatID = &wechatID
		updated.WechatBoundAt = &now
		return tx.Save(&updated).Error
	})
	return updated, err
}
