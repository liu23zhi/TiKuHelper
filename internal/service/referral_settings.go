package service

import (
	"strconv"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type ReferralSettings struct {
	Enabled bool    `json:"enabled"`
	Rate    float64 `json:"rate"`
}

func GetReferralSettingsWithDB(db *gorm.DB) ReferralSettings {
	return ReferralSettings{GetReferralRebateEnabledWithDB(db), GetReferralRebateRateWithDB(db)}
}
func SaveReferralSettingsWithDB(db *gorm.DB, settings ReferralSettings) error {
	rate, err := normalizeReferralRebateRate(settings.Rate)
	if err != nil {
		return err
	}
	return db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := saveSystemSettingValue(tx, ReferralRebateEnabledSettingKey, strconv.FormatBool(settings.Enabled), "启用邀请返佣"); err != nil {
			return err
		}
		return saveSystemSettingValue(tx, ReferralRebateRateSettingKey, strconv.FormatFloat(rate, 'f', -1, 64), "邀请返佣比例")
	})
}

type ReferralInviteAdminItem struct {
	UserID       int32      `json:"userId"`
	Username     string     `json:"username"`
	Nickname     string     `json:"nickname"`
	Code         string     `json:"code"`
	CreatedAt    *time.Time `json:"createdAt"`
	InvitedCount int64      `json:"invitedCount"`
}
type ReferralInviteAdminResult struct {
	Data     []ReferralInviteAdminItem `json:"data"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"pageSize"`
}

func ListReferralInvitesWithDB(db *gorm.DB, keyword string, page, pageSize int) (ReferralInviteAdminResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	out := ReferralInviteAdminResult{Data: []ReferralInviteAdminItem{}, Page: page, PageSize: pageSize}
	query := referralSession(db).Table(entity.TableNameUser + " AS users").Joins("LEFT JOIN referral_invites AS invite ON users.id=invite.user_id")
	if keyword != "" {
		query = query.Where("invite.code LIKE ? OR users.username LIKE ? OR users.nickname LIKE ?", "%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
	}
	if err := query.Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select("users.id AS user_id, users.username, users.nickname, COALESCE(invite.code,'') AS code, invite.created_at, (SELECT COUNT(*) FROM users referred WHERE referred.invited_by_user_id=users.id) AS invited_count").Order("users.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out.Data).Error
	return out, err
}
