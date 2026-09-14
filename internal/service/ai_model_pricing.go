package service

import (
	"errors"
	"math"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var ErrAIModelArchitectureNameRequired = errors.New("模型架构名称不能为空")
var ErrAIModelArchitectureNotFound = errors.New("模型架构不存在")

type CreateAIModelArchitectureInput struct {
	Name              string  `json:"name"`
	ModelName         string  `json:"modelName"`
	InputTokenPrice   float64 `json:"inputTokenPrice"`
	OutputTokenPrice  float64 `json:"outputTokenPrice"`
	BillingMultiplier float64 `json:"billingMultiplier"`
	Enabled           bool    `json:"enabled"`
	Sort              int     `json:"sort"`
	Remark            string  `json:"remark"`
}

type UpdateAIModelArchitectureInput struct {
	Name              string  `json:"name"`
	ModelName         string  `json:"modelName"`
	InputTokenPrice   float64 `json:"inputTokenPrice"`
	OutputTokenPrice  float64 `json:"outputTokenPrice"`
	BillingMultiplier float64 `json:"billingMultiplier"`
	Enabled           bool    `json:"enabled"`
	Sort              int     `json:"sort"`
	Remark            string  `json:"remark"`
}

type ResolvedAIModelPrice struct {
	ModelName                  string  `json:"modelName"`
	ArchitectureID             int32   `json:"architectureId"`
	MappingID                  int32   `json:"mappingId"`
	InputTokenPrice            float64 `json:"inputTokenPrice"`
	OutputTokenPrice           float64 `json:"outputTokenPrice"`
	ExchangeRate               float64 `json:"exchangeRate"`
	UseGlobalBillingMultiplier bool    `json:"useGlobalBillingMultiplier"`
	GlobalBillingMultiplier    float64 `json:"globalBillingMultiplier"`
	ModelBillingMultiplier     float64 `json:"modelBillingMultiplier"`
	BillingMultiplier          float64 `json:"billingMultiplier"`
	Source                     string  `json:"source"`
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
	item, err := normalizeAIModelArchitectureInput(input.Name, input.ModelName, input.InputTokenPrice, input.OutputTokenPrice, input.BillingMultiplier, input.Enabled, input.Sort, input.Remark)
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

	normalized, err := normalizeAIModelArchitectureInput(input.Name, input.ModelName, input.InputTokenPrice, input.OutputTokenPrice, input.BillingMultiplier, input.Enabled, input.Sort, input.Remark)
	if err != nil {
		return entity.AIModelArchitecture{}, err
	}

	item.Name = normalized.Name
	item.ModelName = normalized.ModelName
	item.InputTokenPrice = normalized.InputTokenPrice
	item.OutputTokenPrice = normalized.OutputTokenPrice
	item.BillingMultiplier = normalized.BillingMultiplier
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

func ResolveAIModelPriceWithDB(db *gorm.DB, modelName string) (ResolvedAIModelPrice, error) {
	trimmedModelName := strings.TrimSpace(modelName)
	config, configExists, err := getAISearchConfigRecord(db)
	if err != nil {
		return ResolvedAIModelPrice{}, err
	}

	exchangeRate := float64(1)
	if configExists {
		exchangeRate = normalizeAISearchPositiveMultiplier(config.ExchangeRate)
		if config.UseGlobalBillingMultiplier {
			globalMultiplier := normalizeAISearchPositiveMultiplier(config.GlobalBillingMultiplier)
			return ResolvedAIModelPrice{
				ModelName:                  trimmedModelName,
				InputTokenPrice:            config.InputTokenPrice,
				OutputTokenPrice:           config.OutputTokenPrice,
				ExchangeRate:               exchangeRate,
				UseGlobalBillingMultiplier: true,
				GlobalBillingMultiplier:    globalMultiplier,
				ModelBillingMultiplier:     1,
				BillingMultiplier:          normalizeAIModelEffectiveBillingMultiplier(exchangeRate, globalMultiplier),
				Source:                     "global",
			}, nil
		}
	}

	if trimmedModelName != "" {
		var architecture entity.AIModelArchitecture
		if err := db.Where("model_name = ? AND enabled = ?", trimmedModelName, true).Order("sort asc, id asc").First(&architecture).Error; err == nil {
			modelMultiplier := normalizeAIModelBillingMultiplier(architecture.BillingMultiplier, architecture.InputTokenPrice, architecture.OutputTokenPrice)
			return ResolvedAIModelPrice{
				ModelName:                  trimmedModelName,
				ArchitectureID:             architecture.ID,
				InputTokenPrice:            architecture.InputTokenPrice,
				OutputTokenPrice:           architecture.OutputTokenPrice,
				ExchangeRate:               exchangeRate,
				UseGlobalBillingMultiplier: false,
				GlobalBillingMultiplier:    normalizeAISearchPositiveMultiplier(config.GlobalBillingMultiplier),
				ModelBillingMultiplier:     modelMultiplier,
				BillingMultiplier:          normalizeAIModelEffectiveBillingMultiplier(exchangeRate, modelMultiplier),
				Source:                     "architecture",
			}, nil
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return ResolvedAIModelPrice{}, err
		}
	}

	legacyMultiplier := float64(1)
	var inputTokenPrice float64
	var outputTokenPrice float64
	if configExists {
		inputTokenPrice = config.InputTokenPrice
		outputTokenPrice = config.OutputTokenPrice
		legacyMultiplier = normalizeAIModelBillingMultiplier(0, config.InputTokenPrice, config.OutputTokenPrice)
	}
	return ResolvedAIModelPrice{
		ModelName:                  trimmedModelName,
		InputTokenPrice:            inputTokenPrice,
		OutputTokenPrice:           outputTokenPrice,
		ExchangeRate:               exchangeRate,
		UseGlobalBillingMultiplier: false,
		GlobalBillingMultiplier:    normalizeAISearchPositiveMultiplier(config.GlobalBillingMultiplier),
		ModelBillingMultiplier:     legacyMultiplier,
		BillingMultiplier:          normalizeAIModelEffectiveBillingMultiplier(exchangeRate, legacyMultiplier),
		Source:                     "legacy",
	}, nil
}

func normalizeAIModelArchitectureInput(name string, modelName string, inputTokenPrice float64, outputTokenPrice float64, billingMultiplier float64, enabled bool, sort int, remark string) (entity.AIModelArchitecture, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" {
		return entity.AIModelArchitecture{}, ErrAIModelArchitectureNameRequired
	}

	return entity.AIModelArchitecture{
		Name:              normalizedName,
		ModelName:         strings.TrimSpace(modelName),
		InputTokenPrice:   inputTokenPrice,
		OutputTokenPrice:  outputTokenPrice,
		BillingMultiplier: normalizeAIModelBillingMultiplier(billingMultiplier, inputTokenPrice, outputTokenPrice),
		Enabled:           enabled,
		Sort:              sort,
		Remark:            strings.TrimSpace(remark),
	}, nil
}

func normalizeAIModelBillingMultiplier(multiplier float64, legacyInputPrice float64, legacyOutputPrice float64) float64 {
	if multiplier > 0 {
		return multiplier
	}
	if legacyInputPrice > 0 || legacyOutputPrice > 0 {
		if legacyInputPrice > legacyOutputPrice {
			return legacyInputPrice
		}
		return legacyOutputPrice
	}
	return 1
}

func normalizeAIModelEffectiveBillingMultiplier(exchangeRate float64, multiplier float64) float64 {
	return math.Round(normalizeAISearchPositiveMultiplier(exchangeRate)*normalizeAISearchPositiveMultiplier(multiplier)*10000) / 10000
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
