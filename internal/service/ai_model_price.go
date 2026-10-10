package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	AIModelPriceStatusEnabled  = "enabled"
	AIModelPriceStatusDisabled = "disabled"

	// aimodelPriceSeededSettingKey 记录内置默认价格已经写入过，
	// 避免管理员删掉某个默认模型后，下一次请求又被自动补回来。
	aimodelPriceSeededSettingKey = "ai_model_price_defaults_seeded"
	aimodelPriceSeededValue      = "1"
)

var aimodelPriceSeeded sync.Map

var ErrAIModelPriceNameRequired = errors.New("模型名称不能为空")
var ErrAIModelPriceNotFound = errors.New("模型价格不存在")

// defaultAIModelPrices 是内置的默认模型价格表。
// 单价单位：人民币（元）/ 百万 token。
// 价格数值沿用约定的报价表；如上游按美元报价，请按当期汇率折算后在此维护人民币价格。
var defaultAIModelPrices = []entity.AIModelPrice{
	{ModelName: "GPT-6 Astra", InputPrice: 10.00, CachedPrice: 1.00, OutputPrice: 50.00},
	{ModelName: "GPT-6.1 Sol", InputPrice: 2.00, CachedPrice: 0.10, OutputPrice: 10.00},
	{ModelName: "GPT-6 Sol", InputPrice: 2.00, CachedPrice: 0.20, OutputPrice: 10.00},
	{ModelName: "GPT-6 Luna", InputPrice: 0.10, CachedPrice: 0.01, OutputPrice: 0.50},
	{ModelName: "GPT-5.6 Sol", InputPrice: 4.00, CachedPrice: 0.40, OutputPrice: 20.00},
	{ModelName: "GPT-5.6 Terra", InputPrice: 2.00, CachedPrice: 0.20, OutputPrice: 12.00},
	{ModelName: "GPT-5.6 Luna", InputPrice: 0.20, CachedPrice: 0.02, OutputPrice: 1.20},
	{ModelName: "GPT-Rosalind-Research", InputPrice: 5.00, CachedPrice: 0.50, OutputPrice: 25.00},
	{ModelName: "GPT-5.5", InputPrice: 5.00, CachedPrice: 0.50, OutputPrice: 30.00},
	{ModelName: "Daybreak Blue（GPT-5.6 Sol）", InputPrice: 4.00, CachedPrice: 0.40, OutputPrice: 20.00},
	{ModelName: "Daybreak Red", InputPrice: 12.50, CachedPrice: 1.25, OutputPrice: 75.00},
	{ModelName: "GPT-5.3-Codex", InputPrice: 1.75, CachedPrice: 0.175, OutputPrice: 14.00},
	{ModelName: "GPT-5.2", InputPrice: 1.75, CachedPrice: 0.175, OutputPrice: 14.00},
	{ModelName: "GPT-6 Astra Law *", InputPrice: 12.50, CachedPrice: 1.25, OutputPrice: 62.50},
}

type CreateAIModelPriceInput struct {
	ModelName   string  `json:"modelName"`
	DisplayName string  `json:"displayName"`
	InputPrice  float64 `json:"inputPrice"`
	CachedPrice float64 `json:"cachedPrice"`
	OutputPrice float64 `json:"outputPrice"`
	Status      string  `json:"status"`
	Sort        int     `json:"sort"`
	Remark      string  `json:"remark"`
}

type UpdateAIModelPriceInput struct {
	ModelName   string  `json:"modelName"`
	DisplayName string  `json:"displayName"`
	InputPrice  float64 `json:"inputPrice"`
	CachedPrice float64 `json:"cachedPrice"`
	OutputPrice float64 `json:"outputPrice"`
	Status      string  `json:"status"`
	Sort        int     `json:"sort"`
	Remark      string  `json:"remark"`
}

// ResolvedAIModelTokenPrice 是最终生效的模型单价，单位为「人民币（元）/ 百万 token」。
type ResolvedAIModelTokenPrice struct {
	ModelName         string  `json:"modelName"`
	DisplayName       string  `json:"displayName"`
	PriceID           int32   `json:"priceId"`
	InputPrice        float64 `json:"inputPrice"`
	CachedPrice       float64 `json:"cachedPrice"`
	OutputPrice       float64 `json:"outputPrice"`
	InputUnitPrice    float64 `json:"inputUnitPrice"`
	CachedUnitPrice   float64 `json:"cachedUnitPrice"`
	OutputUnitPrice   float64 `json:"outputUnitPrice"`
	MatchedBy         string  `json:"matchedBy"`
	EffectiveUnitName string  `json:"effectiveUnitName"`
}

// ListAIModelPricesWithDB 返回模型价格表，并在首次访问时写入内置默认价格，
// 这样升级后不会出现「价格表为空 → AI 全部免费」的计费漏洞。
func ListAIModelPricesWithDB(db *gorm.DB, includeDisabled bool) ([]entity.AIModelPrice, error) {
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		return nil, err
	}

	query := db.Model(&entity.AIModelPrice{}).Order("sort asc, id asc")
	if !includeDisabled {
		query = query.Where("status = ?", AIModelPriceStatusEnabled)
	}

	var items []entity.AIModelPrice
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// EnsureDefaultAIModelPricesWithDB 幂等地写入缺失的内置默认价格，每个数据库只会执行一次。
// 已存在（含被管理员改过）的记录不会被覆盖；管理员删除的默认模型也不会被重新补回。
// 数据库尚未建表时直接返回 nil：价格表缺失不应该让搜题链路整体失败。
func EnsureDefaultAIModelPricesWithDB(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	cacheKey := aimodelPriceCacheKey(db)
	if _, cached := aimodelPriceSeeded.Load(cacheKey); cached {
		return nil
	}

	seeded, err := aimodelPriceTableReady(db)
	if err != nil {
		return err
	}
	if !seeded {
		return nil
	}

	if aimodelPriceDefaultsAlreadySeeded(db) {
		aimodelPriceSeeded.Store(cacheKey, struct{}{})
		return nil
	}

	if err := insertDefaultAIModelPrices(db); err != nil {
		return err
	}

	markAIModelPriceDefaultsSeeded(db)
	aimodelPriceSeeded.Store(cacheKey, struct{}{})
	return nil
}

func insertDefaultAIModelPrices(db *gorm.DB) error {
	for index, seed := range defaultAIModelPrices {
		var existing entity.AIModelPrice
		err := db.Where("model_name = ?", seed.ModelName).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			if isAIModelPriceTableMissingError(err) {
				return nil
			}
			return err
		}

		item := seed
		item.DisplayName = seed.ModelName
		item.Status = AIModelPriceStatusEnabled
		item.Sort = (index + 1) * 10
		item.IsSeed = true
		if createErr := db.Create(&item).Error; createErr != nil {
			// 并发初始化时可能被其他请求抢先插入，唯一索引冲突可以安全忽略。
			if isAIModelPriceUniqueConflictError(createErr) {
				continue
			}
			if isAIModelPriceTableMissingError(createErr) {
				return nil
			}
			return createErr
		}
	}
	return nil
}

// aimodelPriceDefaultsAlreadySeeded 读取持久化标记，区分「尚未初始化」与「管理员删掉了默认项」。
func aimodelPriceDefaultsAlreadySeeded(db *gorm.DB) bool {
	var setting entity.SystemSetting
	err := db.Where("key = ?", aimodelPriceSeededSettingKey).First(&setting).Error
	if err == nil {
		return strings.TrimSpace(setting.Value) == aimodelPriceSeededValue
	}
	return false
}

func markAIModelPriceDefaultsSeeded(db *gorm.DB) {
	if err := db.Where("key = ?", aimodelPriceSeededSettingKey).Delete(&entity.SystemSetting{}).Error; err != nil {
		return
	}
	_ = db.Create(&entity.SystemSetting{
		Key:   aimodelPriceSeededSettingKey,
		Value: aimodelPriceSeededValue,
	}).Error
}

// aimodelPriceCacheKey 用底层连接池指针区分不同数据库（生产库与测试内存库）。
// 只用于进程内的「已初始化」快路径，真正的幂等性由 system_settings 标记保证。
func aimodelPriceCacheKey(db *gorm.DB) string {
	sqlDB, err := db.DB()
	if err != nil || sqlDB == nil {
		return "default"
	}
	return fmt.Sprintf("%p", sqlDB)
}

func aimodelPriceTableReady(db *gorm.DB) (bool, error) {
	ready := db.Migrator().HasTable(&entity.AIModelPrice{})
	if ready {
		return true, nil
	}

	// 生产环境由 registry 的 AutoMigrate 建表；这里兜底是为了兼容
	// 只初始化了历史表的数据库与测试用内存库，避免计费因缺表而中断。
	if err := db.Migrator().CreateTable(&entity.AIModelPrice{}); err != nil {
		if isAIModelPriceTableMissingError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func isAIModelPriceTableMissingError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrNotImplemented) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") ||
		strings.Contains(message, "doesn't exist") ||
		strings.Contains(message, "does not exist")
}

func isAIModelPriceUniqueConflictError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}

func CreateAIModelPriceWithDB(db *gorm.DB, input CreateAIModelPriceInput) (entity.AIModelPrice, error) {
	item, err := normalizeAIModelPriceInput(input.ModelName, input.DisplayName, input.InputPrice, input.CachedPrice, input.OutputPrice, input.Status, input.Sort, input.Remark)
	if err != nil {
		return entity.AIModelPrice{}, err
	}

	if err := db.Create(&item).Error; err != nil {
		return entity.AIModelPrice{}, err
	}
	return item, nil
}

func UpdateAIModelPriceWithDB(db *gorm.DB, priceID int32, input UpdateAIModelPriceInput) (entity.AIModelPrice, error) {
	item, err := loadAIModelPriceByID(db, priceID)
	if err != nil {
		return entity.AIModelPrice{}, err
	}

	normalized, err := normalizeAIModelPriceInput(input.ModelName, input.DisplayName, input.InputPrice, input.CachedPrice, input.OutputPrice, input.Status, input.Sort, input.Remark)
	if err != nil {
		return entity.AIModelPrice{}, err
	}

	item.ModelName = normalized.ModelName
	item.DisplayName = normalized.DisplayName
	item.InputPrice = normalized.InputPrice
	item.CachedPrice = normalized.CachedPrice
	item.OutputPrice = normalized.OutputPrice
	item.Status = normalized.Status
	item.Sort = normalized.Sort
	item.Remark = normalized.Remark
	item.IsUserModified = true

	if err := db.Save(&item).Error; err != nil {
		return entity.AIModelPrice{}, err
	}
	return item, nil
}

func DeleteAIModelPriceWithDB(db *gorm.DB, priceID int32) error {
	item, err := loadAIModelPriceByID(db, priceID)
	if err != nil {
		return err
	}
	return db.Delete(&item).Error
}

// ResetAIModelPricesWithDB 恢复内置默认价格表，用于管理员误改后回滚。
func ResetAIModelPricesWithDB(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := db.Where("1 = 1").Delete(&entity.AIModelPrice{}).Error; err != nil {
		return err
	}

	// 清除初始化标记与进程内缓存，让下一轮重新写入默认价格。
	if err := db.Where("key = ?", aimodelPriceSeededSettingKey).Delete(&entity.SystemSetting{}).Error; err != nil {
		return err
	}
	aimodelPriceSeeded.Delete(aimodelPriceCacheKey(db))

	return EnsureDefaultAIModelPricesWithDB(db)
}

// ResolveAIModelTokenPriceWithDB 按模型名解析生效单价。
// 匹配顺序：模型名精确匹配（忽略大小写）→ 显示名匹配 → 模型名去空格匹配。
// 未命中价格表时返回 found=false，由调用方决定降级策略。
func ResolveAIModelTokenPriceWithDB(db *gorm.DB, modelName string) (ResolvedAIModelTokenPrice, bool, error) {
	trimmed := strings.TrimSpace(modelName)
	if trimmed == "" {
		return ResolvedAIModelTokenPrice{}, false, nil
	}
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		return ResolvedAIModelTokenPrice{}, false, err
	}

	item, matchedBy, err := findAIModelPriceByModelName(db, trimmed)
	if err != nil {
		return ResolvedAIModelTokenPrice{}, false, err
	}
	if item == nil {
		return ResolvedAIModelTokenPrice{}, false, nil
	}

	return ResolvedAIModelTokenPrice{
		ModelName:         item.ModelName,
		DisplayName:       resolveAIModelPriceDisplayName(*item),
		PriceID:           item.ID,
		InputPrice:        item.InputPrice,
		CachedPrice:       item.CachedPrice,
		OutputPrice:       item.OutputPrice,
		InputUnitPrice:    roundAIModelUnitPrice(item.InputPrice),
		CachedUnitPrice:   roundAIModelUnitPrice(item.CachedPrice),
		OutputUnitPrice:   roundAIModelUnitPrice(item.OutputPrice),
		MatchedBy:         matchedBy,
		EffectiveUnitName: "元/百万token",
	}, true, nil
}

func findAIModelPriceByModelName(db *gorm.DB, modelName string) (*entity.AIModelPrice, string, error) {
	candidates := []struct {
		query     string
		args      []interface{}
		matchedBy string
	}{
		{"LOWER(model_name) = LOWER(?) AND status = ?", []interface{}{modelName, AIModelPriceStatusEnabled}, "model_name"},
		{"LOWER(display_name) = LOWER(?) AND status = ?", []interface{}{modelName, AIModelPriceStatusEnabled}, "display_name"},
	}

	for _, candidate := range candidates {
		var item entity.AIModelPrice
		err := db.Where(candidate.query, candidate.args...).Order("sort asc, id asc").First(&item).Error
		if err == nil {
			return &item, candidate.matchedBy, nil
		}
		if isAIModelPriceTableMissingError(err) {
			return nil, "", nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", err
		}
	}

	// 兜底：忽略空格差异，例如「GPT-5.6Sol」与「GPT-5.6 Sol」。
	normalized := normalizeAIModelPriceKey(modelName)
	if normalized == "" {
		return nil, "", nil
	}

	var items []entity.AIModelPrice
	if err := db.Where("status = ?", AIModelPriceStatusEnabled).Order("sort asc, id asc").Find(&items).Error; err != nil {
		if isAIModelPriceTableMissingError(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	for index := range items {
		if normalizeAIModelPriceKey(items[index].ModelName) == normalized ||
			normalizeAIModelPriceKey(resolveAIModelPriceDisplayName(items[index])) == normalized {
			return &items[index], "normalized", nil
		}
	}
	return nil, "", nil
}

func loadAIModelPriceByID(db *gorm.DB, priceID int32) (entity.AIModelPrice, error) {
	var item entity.AIModelPrice
	if err := db.Where("id = ?", priceID).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.AIModelPrice{}, ErrAIModelPriceNotFound
		}
		return entity.AIModelPrice{}, err
	}
	return item, nil
}

func normalizeAIModelPriceInput(modelName string, displayName string, inputPrice float64, cachedPrice float64, outputPrice float64, status string, sort int, remark string) (entity.AIModelPrice, error) {
	normalizedName := strings.TrimSpace(modelName)
	if normalizedName == "" {
		return entity.AIModelPrice{}, ErrAIModelPriceNameRequired
	}

	normalizedStatus := strings.ToLower(strings.TrimSpace(status))
	switch normalizedStatus {
	case AIModelPriceStatusDisabled:
		normalizedStatus = AIModelPriceStatusDisabled
	default:
		normalizedStatus = AIModelPriceStatusEnabled
	}

	if sort < 0 {
		sort = 0
	}

	return entity.AIModelPrice{
		ModelName:   normalizedName,
		DisplayName: strings.TrimSpace(displayName),
		InputPrice:  normalizeAIModelPriceAmount(inputPrice),
		CachedPrice: normalizeAIModelPriceAmount(cachedPrice),
		OutputPrice: normalizeAIModelPriceAmount(outputPrice),
		Status:      normalizedStatus,
		Sort:        sort,
		Remark:      strings.TrimSpace(remark),
	}, nil
}

func normalizeAIModelPriceAmount(value float64) float64 {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func resolveAIModelPriceDisplayName(item entity.AIModelPrice) string {
	if displayName := strings.TrimSpace(item.DisplayName); displayName != "" {
		return displayName
	}
	return strings.TrimSpace(item.ModelName)
}

func normalizeAIModelPriceKey(value string) string {
	replacer := strings.NewReplacer(" ", "", "　", "", "-", "", "_", "", "（", "(", "）", ")", "*", "")
	return strings.ToLower(replacer.Replace(strings.TrimSpace(value)))
}

func roundAIModelUnitPrice(value float64) float64 {
	return math.Round(value*100000000) / 100000000
}
