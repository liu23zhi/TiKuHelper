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
	return db.Transaction(func(tx *gorm.DB) error {
		return CreateUserWithNewUserBonusInTx(tx, user, options)
	})
}

func CreateUserWithNewUserBonusInTx(db *gorm.DB, user *entity.User, options NewUserBonusOptions) error {
	if user == nil {
		return fmt.Errorf("用户不能为空")
	}
	if err := util.ValidateRestrictedName(user.Username, util.UsernameMaxLength, "用户名"); err != nil {
		return err
	}
	if user.Phone != nil && *user.Phone != "" {
		phone, err := NormalizeMainlandPhoneInput(*user.Phone)
		if err != nil {
			return err
		}
		owners, err := FindUsersByPhoneAliases(db, phone)
		if err != nil {
			return err
		}
		if len(owners) > 1 {
			return ErrPhoneBindingConflict
		}
		if len(owners) > 0 {
			return ErrPhoneAlreadyBound
		}
		user.Phone = &phone
	}
	var bonusAmount float64
	if options.Enabled {
		normalized, err := normalizeNewUserBonusAmount(options.Amount)
		if err != nil {
			return err
		}
		bonusAmount = normalized
	}

	if err := db.Create(user).Error; err != nil {
		return err
	}
	if !options.Enabled || bonusAmount <= 0 {
		return nil
	}

	balanceBefore := util.RoundMoney(user.Balance)
	giftBefore := util.RoundMoney(user.GiftBalance)
	// 注册赠送是「送的钱」，按双桶模型进**赠送余额** gift_balance（可消费、不可退），
	// 不进可退本金 balance —— 否则新号注册就能立刻退款套现。
	// 入账按规则 3：先抵欠款，剩余才进赠金桶（43-*.md）。
	newBalance, giftAfter := CreditGiftBuckets(user.Balance, user.GiftBalance, bonusAmount)
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"balance":      newBalance,
		"gift_balance": giftAfter,
	}).Error; err != nil {
		return err
	}
	user.Balance = newBalance
	user.GiftBalance = giftAfter

	record := entity.BillingRecord{
		UserID:            user.ID,
		Type:              registrationBonusBillingRecordType,
		Provider:          "system",
		Amount:            bonusAmount,
		BalanceBefore:     balanceBefore,
		BalanceAfter:      newBalance,
		RequestID:         fmt.Sprintf("registration_bonus_%d", user.ID),
		UnitPriceSnapshot: fmt.Sprintf("source=%s giftBalanceBefore=%s giftBalanceAfter=%s", options.Source, moneyTextForReferral(giftBefore), moneyTextForReferral(giftAfter)),
		Remark:            "新用户赠送",
	}
	return db.Session(&gorm.Session{NewDB: true}).Create(&record).Error
}
