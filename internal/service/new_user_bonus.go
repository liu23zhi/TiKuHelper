package service

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	RegistrationBonusSourcePublicRegister = "public_register"
	RegistrationBonusSourceAdminCreate    = "admin_create"
)

type NewUserBonusOptions struct {
	Enabled bool
	Amount  float64
	Source  string
}

func CreateUserWithNewUserBonusWithDB(db *gorm.DB, user *entity.User, options NewUserBonusOptions) error {
	if user == nil {
		return fmt.Errorf("用户不能为空")
	}
	var bonusAmount float64
	if options.Enabled {
		normalized, err := normalizeNewUserBonusAmount(options.Amount)
		if err != nil {
			return err
		}
		bonusAmount = normalized
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		if !options.Enabled || bonusAmount <= 0 {
			return nil
		}

		balanceBefore := util.RoundMoney(user.Balance)
		balanceAfter := util.RoundMoney(balanceBefore + bonusAmount)
		if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", balanceAfter).Error; err != nil {
			return err
		}
		user.Balance = balanceAfter

		record := entity.BillingRecord{
			UserID:            user.ID,
			Type:              registrationBonusBillingRecordType,
			Provider:          "system",
			Amount:            bonusAmount,
			BalanceBefore:     balanceBefore,
			BalanceAfter:      balanceAfter,
			RequestID:         fmt.Sprintf("registration_bonus_%d", user.ID),
			UnitPriceSnapshot: fmt.Sprintf("source=%s", options.Source),
			Remark:            "新用户赠送",
		}
		return tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error
	})
}
