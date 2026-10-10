package service

import (
	"errors"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var ErrAIModelArchitectureNameRequired = errors.New("模型架构名称不能为空")
var ErrAIModelArchitectureNotFound = errors.New("模型架构不存在")

// CreateAIModelArchitectureInput 只描述「模型绑定」信息。
// 模型单价已经统一迁移到模型价格表（ai_model_prices），架构表不再承担计费职责。
type CreateAIModelArchitectureInput struct {
	Name      string `json:"name"`
	ModelName string `json:"modelName"`
	Enabled   bool   `json:"enabled"`
	Sort      int    `json:"sort"`
	Remark    string `json:"remark"`
}

type UpdateAIModelArchitectureInput struct {
	Name      string `json:"name"`
	ModelName string `json:"modelName"`
	Enabled   bool   `json:"enabled"`
	Sort      int    `json:"sort"`
	Remark    string `json:"remark"`
}

func ListAIModelArchitecturesWithDB(db *gorm.DB, includeDisabled bool) ([]entity.AIModelArchitecture, error) {
	query := db.Model(&entity.AIModelArchitecture{}).Order("sort asc, id asc")
	if !includeDisabled {
		query = query.Where("enabled = ?", true)
	}

	var items []entity.AIModelArchitecture
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func CreateAIModelArchitectureWithDB(db *gorm.DB, input CreateAIModelArchitectureInput) (entity.AIModelArchitecture, error) {
	item, err := normalizeAIModelArchitectureInput(input.Name, input.ModelName, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.AIModelArchitecture{}, err
	}
	if err := db.Create(&item).Error; err != nil {
		return entity.AIModelArchitecture{}, err
	}
	return item, nil
}

func UpdateAIModelArchitectureWithDB(db *gorm.DB, architectureID int32, input UpdateAIModelArchitectureInput) (entity.AIModelArchitecture, error) {
	item, err := loadAIModelArchitectureByID(db, architectureID)
	if err != nil {
		return entity.AIModelArchitecture{}, err
	}

	normalized, err := normalizeAIModelArchitectureInput(input.Name, input.ModelName, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.AIModelArchitecture{}, err
	}

	item.Name = normalized.Name
	item.ModelName = normalized.ModelName
	item.Enabled = normalized.Enabled
	item.Sort = normalized.Sort
	item.Remark = normalized.Remark

	if err := db.Save(&item).Error; err != nil {
		return entity.AIModelArchitecture{}, err
	}
	return item, nil
}

func DeleteAIModelArchitectureWithDB(db *gorm.DB, architectureID int32) error {
	item, err := loadAIModelArchitectureByID(db, architectureID)
	if err != nil {
		return err
	}
	return db.Delete(&item).Error
}

func normalizeAIModelArchitectureInput(name string, modelName string, enabled bool, sort int, remark string) (entity.AIModelArchitecture, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" {
		return entity.AIModelArchitecture{}, ErrAIModelArchitectureNameRequired
	}
	if sort < 0 {
		sort = 0
	}

	return entity.AIModelArchitecture{
		Name:      normalizedName,
		ModelName: strings.TrimSpace(modelName),
		Enabled:   enabled,
		Sort:      sort,
		Remark:    strings.TrimSpace(remark),
	}, nil
}

func loadAIModelArchitectureByID(db *gorm.DB, architectureID int32) (entity.AIModelArchitecture, error) {
	var item entity.AIModelArchitecture
	if err := db.Where("id = ?", architectureID).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.AIModelArchitecture{}, ErrAIModelArchitectureNotFound
		}
		return entity.AIModelArchitecture{}, err
	}
	return item, nil
}
