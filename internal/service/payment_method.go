package service

import (
	"errors"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var ErrPaymentMethodNameRequired = errors.New("支付方式名称不能为空")
var ErrPaymentMethodEPayTypeRequired = errors.New("易支付通道值不能为空")
var ErrPaymentMethodNotFound = errors.New("支付方式不存在")

type CreatePaymentMethodInput struct {
	Name     string `json:"name"`
	IconSVG  string `json:"iconSvg"`
	EPayType string `json:"epayType"`
	Enabled  bool   `json:"enabled"`
	Sort     int    `json:"sort"`
	Remark   string `json:"remark"`
}

type UpdatePaymentMethodInput struct {
	Name     string `json:"name"`
	IconSVG  string `json:"iconSvg"`
	EPayType string `json:"epayType"`
	Enabled  bool   `json:"enabled"`
	Sort     int    `json:"sort"`
	Remark   string `json:"remark"`
}

func ListPaymentMethodsWithDB(db *gorm.DB, includeDisabled bool) ([]entity.PaymentMethod, error) {
	query := db.Model(&entity.PaymentMethod{}).Order("sort asc, id asc")
	if !includeDisabled {
		query = query.Where("enabled = ?", true)
	}

	var methods []entity.PaymentMethod
	if err := query.Find(&methods).Error; err != nil {
		return nil, err
	}
	for i := range methods {
		if _, err := sanitizePaymentMethodIconSVG(methods[i].IconSVG); err != nil {
			methods[i].IconSVG = ""
		}
	}
	return methods, nil
}

func CreatePaymentMethodWithDB(db *gorm.DB, input CreatePaymentMethodInput) (entity.PaymentMethod, error) {
	method, err := normalizePaymentMethodInput(input.Name, input.IconSVG, input.EPayType, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.PaymentMethod{}, err
	}
	if err := db.Create(&method).Error; err != nil {
		return entity.PaymentMethod{}, err
	}
	return method, nil
}

func UpdatePaymentMethodWithDB(db *gorm.DB, paymentMethodID int32, input UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
	normalized, err := normalizePaymentMethodInput(input.Name, input.IconSVG, input.EPayType, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.PaymentMethod{}, err
	}
	method, err := loadPaymentMethodByID(db, paymentMethodID)
	if err != nil {
		return entity.PaymentMethod{}, err
	}

	method.Name = normalized.Name
	method.IconSVG = normalized.IconSVG
	method.EPayType = normalized.EPayType
	method.Enabled = normalized.Enabled
	method.Sort = normalized.Sort
	method.Remark = normalized.Remark

	if err := db.Save(&method).Error; err != nil {
		return entity.PaymentMethod{}, err
	}
	return method, nil
}

func DeletePaymentMethodWithDB(db *gorm.DB, paymentMethodID int32) error {
	method, err := loadPaymentMethodByID(db, paymentMethodID)
	if err != nil {
		return err
	}
	return db.Delete(&method).Error
}

func normalizePaymentMethodInput(name string, iconSVG string, epayType string, enabled bool, sort int, remark string) (entity.PaymentMethod, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" {
		return entity.PaymentMethod{}, ErrPaymentMethodNameRequired
	}

	normalizedEPayType := strings.TrimSpace(epayType)
	if normalizedEPayType == "" {
		return entity.PaymentMethod{}, ErrPaymentMethodEPayTypeRequired
	}

	if err := ValidatePaymentMethodIcon(iconSVG); err != nil {
		return entity.PaymentMethod{}, err
	}

	return entity.PaymentMethod{
		Name:     normalizedName,
		IconSVG:  strings.TrimSpace(iconSVG),
		EPayType: normalizedEPayType,
		Enabled:  enabled,
		Sort:     sort,
		Remark:   strings.TrimSpace(remark),
	}, nil
}

func loadPaymentMethodByID(db *gorm.DB, paymentMethodID int32) (entity.PaymentMethod, error) {
	var method entity.PaymentMethod
	if err := db.Where("id = ?", paymentMethodID).First(&method).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.PaymentMethod{}, ErrPaymentMethodNotFound
		}
		return entity.PaymentMethod{}, err
	}
	return method, nil
}
