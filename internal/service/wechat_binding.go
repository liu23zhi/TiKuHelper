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
			// 用户主动解绑：先释放活跃绑定记录，再清 users 列（顺序不能反，见 phone_binding.go）。
			if err := releaseBindingForUser(tx, updated.ID, entity.BindingKindWechat,
				entity.BindingReleaseReasonRebind, entity.BindingReleaseSourceUserUnbind,
				updated, 0); err != nil {
				return err
			}
			updated.WechatID = nil
			updated.WechatBoundAt = nil
			return tx.Model(&entity.User{}).Where("id = ?", targetUserID).
				Select("wechat_id", "wechat_bound_at").Updates(&updated).Error
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
			// 抢号：先释放被抢方的活跃绑定（二分类 rebind → 归 history_*），再清它的 users 列。
			if err := releaseBindingForUser(tx, occupied.ID, entity.BindingKindWechat,
				entity.BindingReleaseReasonRebind, entity.BindingReleaseSourceForceReassign,
				occupied, 0); err != nil {
				return err
			}
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
		// 换绑：先释放本账号旧值（rebind → history_*），再占新值。
		if err := releaseBindingForUser(tx, targetUserID, entity.BindingKindWechat,
			entity.BindingReleaseReasonRebind, entity.BindingReleaseSourceUserUnbind,
			updated, 0); err != nil {
			return err
		}
		updated.WechatID = &wechatID
		updated.WechatBoundAt = &now
		if err := tx.Model(&entity.User{}).Where("id = ?", targetUserID).
			Select("wechat_id", "wechat_bound_at").Updates(&updated).Error; err != nil {
			return err
		}
		if err := recordActiveWechatBinding(tx, updated, wechatID, now); err != nil {
			return err
		}
		// 换绑追缴（44-*.md §3.1 / §5.2）：微信维度同样覆盖 rebind 与 closure 两种释放。
		_, err = clawbackAfterRebindInTx(tx, targetUserID, entity.BindingKindWechat, wechatID)
		return err
	})
	return updated, err
}
