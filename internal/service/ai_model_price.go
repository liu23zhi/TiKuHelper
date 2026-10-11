package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	// AIModelPriceStatusEnabled 是价格行唯一的状态值。
	// 价格表不再有「停用」概念，保留该列只为兼容历史库结构。
	AIModelPriceStatusEnabled = "enabled"

	// aimodelPriceSeededSettingKey 记录上次同步时的默认表指纹，
	// 用于在默认表未变化时跳过逐行同步。
	aimodelPriceSeededSettingKey = "ai_model_price_defaults_seeded"
)

var aimodelPriceSeeded sync.Map

var ErrAIModelPriceNameRequired = errors.New("模型 ID 不能为空")
var ErrAIModelPriceNotFound = errors.New("模型价格不存在")

// defaultAIModelPrices 是内置的默认模型价格表。
//
// ModelName 是**模型 ID**（发送给上游的 model 字段，例如 gpt-6-astra），
// DisplayName 是可读名称（例如 GPT-6 Astra）。价格匹配同时接受两者，
// 因此默认模型填 ID 或填可读名都能命中同一行。
//
// 单价单位：人民币（元）/ 百万 token。数值沿用约定的报价表，美元数值按 1:1 视为人民币；
// 如上游按美元报价，请按当期汇率折算后在此维护。
//
// 口径为「Standard 短上下文」（≤272K 输入 token）。长上下文价格约为输入的 2 倍、
// 输出的 1.5 倍；Fast / Flex / Batch / Ultrafast 档位价格不同，如需按这些档位计费，
// 请另建一条带后缀的模型行（例如 gpt-6-astra-fast）。
// CacheWritePrice 为「缓存写入」单价；文档中为「-」的模型填 0。
var defaultAIModelPrices = []entity.AIModelPrice{
	// 旗舰模型
	{ModelName: "gpt-6-astra", DisplayName: "GPT-6 Astra", InputPrice: 10.00, CachedPrice: 1.00, CacheWritePrice: 12.50, OutputPrice: 50.00},
	{ModelName: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol", InputPrice: 2.00, CachedPrice: 0.10, CacheWritePrice: 2.50, OutputPrice: 10.00},
	{ModelName: "gpt-6-sol", DisplayName: "GPT-6 Sol", InputPrice: 2.00, CachedPrice: 0.20, CacheWritePrice: 2.50, OutputPrice: 10.00},
	{ModelName: "gpt-6-luna", DisplayName: "GPT-6 Luna", InputPrice: 0.10, CachedPrice: 0.01, CacheWritePrice: 0.125, OutputPrice: 0.50},
	{ModelName: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol", InputPrice: 4.00, CachedPrice: 0.40, CacheWritePrice: 5.00, OutputPrice: 20.00},
	{ModelName: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra", InputPrice: 2.00, CachedPrice: 0.20, CacheWritePrice: 2.50, OutputPrice: 12.00},
	{ModelName: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna", InputPrice: 0.20, CachedPrice: 0.02, CacheWritePrice: 0.25, OutputPrice: 1.20},
	{ModelName: "gpt-5.5", DisplayName: "GPT-5.5", InputPrice: 5.00, CachedPrice: 0.50, OutputPrice: 30.00},
	{ModelName: "gpt-5.5-pro", DisplayName: "GPT-5.5 Pro", InputPrice: 30.00, OutputPrice: 180.00},
	{ModelName: "gpt-5.4", DisplayName: "GPT-5.4", InputPrice: 2.50, CachedPrice: 0.25, OutputPrice: 15.00},
	{ModelName: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", InputPrice: 0.75, CachedPrice: 0.075, OutputPrice: 4.50},
	{ModelName: "gpt-5.4-nano", DisplayName: "GPT-5.4 Nano", InputPrice: 0.20, CachedPrice: 0.02, OutputPrice: 1.25},
	{ModelName: "gpt-5.4-pro", DisplayName: "GPT-5.4 Pro", InputPrice: 30.00, OutputPrice: 180.00},
	{ModelName: "gpt-5.2", DisplayName: "GPT-5.2", InputPrice: 1.75, CachedPrice: 0.175, OutputPrice: 14.00},
	{ModelName: "gpt-5.2-pro", DisplayName: "GPT-5.2 Pro", InputPrice: 21.00, OutputPrice: 168.00},
	{ModelName: "gpt-5.1", DisplayName: "GPT-5.1", InputPrice: 1.25, CachedPrice: 0.125, OutputPrice: 10.00},
	{ModelName: "gpt-5", DisplayName: "GPT-5", InputPrice: 1.25, CachedPrice: 0.125, OutputPrice: 10.00},
	{ModelName: "gpt-5-mini", DisplayName: "GPT-5 Mini", InputPrice: 0.25, CachedPrice: 0.025, OutputPrice: 2.00},
	{ModelName: "gpt-5-nano", DisplayName: "GPT-5 Nano", InputPrice: 0.05, CachedPrice: 0.005, OutputPrice: 0.40},
	{ModelName: "gpt-5-pro", DisplayName: "GPT-5 Pro", InputPrice: 15.00, OutputPrice: 120.00},
	// 通用模型
	{ModelName: "gpt-4.1", DisplayName: "GPT-4.1", InputPrice: 2.00, CachedPrice: 0.50, OutputPrice: 8.00},
	{ModelName: "gpt-4.1-mini", DisplayName: "GPT-4.1 Mini", InputPrice: 0.40, CachedPrice: 0.10, OutputPrice: 1.60},
	{ModelName: "gpt-4.1-nano", DisplayName: "GPT-4.1 Nano", InputPrice: 0.10, CachedPrice: 0.025, OutputPrice: 0.40},
	{ModelName: "gpt-4o", DisplayName: "GPT-4o", InputPrice: 2.50, CachedPrice: 1.25, OutputPrice: 10.00},
	{ModelName: "gpt-4o-2024-05-13", DisplayName: "GPT-4o 2024-05-13", InputPrice: 5.00, OutputPrice: 15.00},
	{ModelName: "gpt-4o-mini", DisplayName: "GPT-4o Mini", InputPrice: 0.15, CachedPrice: 0.075, OutputPrice: 0.60},
	{ModelName: "gpt-4-turbo-2024-04-09", DisplayName: "GPT-4 Turbo 2024-04-09", InputPrice: 10.00, OutputPrice: 30.00},
	{ModelName: "gpt-4-0613", DisplayName: "GPT-4 0613", InputPrice: 30.00, OutputPrice: 60.00},
	{ModelName: "gpt-3.5-turbo", DisplayName: "GPT-3.5 Turbo", InputPrice: 0.50, OutputPrice: 1.50},
	{ModelName: "gpt-3.5-turbo-0125", DisplayName: "GPT-3.5 Turbo 0125", InputPrice: 0.50, OutputPrice: 1.50},
	{ModelName: "gpt-3.5-turbo-1106", DisplayName: "GPT-3.5 Turbo 1106", InputPrice: 1.00, OutputPrice: 2.00},
	{ModelName: "gpt-3.5-turbo-instruct", DisplayName: "GPT-3.5 Turbo Instruct", InputPrice: 1.50, OutputPrice: 2.00},
	{ModelName: "davinci-002", DisplayName: "Davinci-002", InputPrice: 2.00, OutputPrice: 2.00},
	{ModelName: "babbage-002", DisplayName: "Babbage-002", InputPrice: 0.40, OutputPrice: 0.40},
	// 推理模型
	{ModelName: "o1", DisplayName: "o1", InputPrice: 15.00, CachedPrice: 7.50, OutputPrice: 60.00},
	{ModelName: "o1-pro", DisplayName: "o1 Pro", InputPrice: 150.00, OutputPrice: 600.00},
	{ModelName: "o3", DisplayName: "o3", InputPrice: 2.00, CachedPrice: 0.50, OutputPrice: 8.00},
	{ModelName: "o3-pro", DisplayName: "o3 Pro", InputPrice: 20.00, OutputPrice: 80.00},
	{ModelName: "o3-mini", DisplayName: "o3 Mini", InputPrice: 1.10, CachedPrice: 0.55, OutputPrice: 4.40},
	{ModelName: "o4-mini", DisplayName: "o4 Mini", InputPrice: 1.10, CachedPrice: 0.275, OutputPrice: 4.40},
	// Cyber（Daybreak）模型
	{ModelName: "gpt-5.6-cyber", DisplayName: "GPT-5.6 Cyber", InputPrice: 12.50, CachedPrice: 1.25, CacheWritePrice: 15.625, OutputPrice: 75.00},
	{ModelName: "gpt-5.5-cyber", DisplayName: "GPT-5.5 Cyber", InputPrice: 12.50, CachedPrice: 1.25, OutputPrice: 75.00},
	// 生命科学（Rosalind）模型
	{ModelName: "gpt-rosalind-research", DisplayName: "GPT-Rosalind-Research", InputPrice: 5.00, CachedPrice: 0.50, OutputPrice: 25.00},
	{ModelName: "gpt-rosalind-discovery", DisplayName: "GPT-Rosalind-Discovery", InputPrice: 5.00, CachedPrice: 0.50, OutputPrice: 25.00},
	// 专用模型
	{ModelName: "gpt-5.3-codex", DisplayName: "GPT-5.3-Codex", InputPrice: 1.75, CachedPrice: 0.175, OutputPrice: 14.00},
	{ModelName: "chat-latest", DisplayName: "Chat-Latest", InputPrice: 5.00, CachedPrice: 0.50, OutputPrice: 30.00},
	{ModelName: "gpt-5-search-api", DisplayName: "GPT-5 Search API", InputPrice: 1.25, CachedPrice: 0.125, OutputPrice: 10.00},
	{ModelName: "text-embedding-3-small", DisplayName: "Text-Embedding-3-Small", InputPrice: 0.02},
	{ModelName: "text-embedding-3-large", DisplayName: "Text-Embedding-3-Large", InputPrice: 0.13},
	{ModelName: "text-embedding-ada-002", DisplayName: "Text-Embedding-Ada-002", InputPrice: 0.10},
	{ModelName: "omni-moderation-latest", DisplayName: "Omni-Moderation-Latest"},
	// 别名行（与对应主模型同价）
	{ModelName: "daybreak-blue", DisplayName: "Daybreak Blue（GPT-5.6 Sol）", InputPrice: 4.00, CachedPrice: 0.40, CacheWritePrice: 5.00, OutputPrice: 20.00},
	{ModelName: "daybreak-red", DisplayName: "Daybreak Red", InputPrice: 12.50, CachedPrice: 1.25, CacheWritePrice: 15.625, OutputPrice: 75.00},
	{ModelName: "gpt-6-astra-law", DisplayName: "GPT-6 Astra Law *", InputPrice: 12.50, CachedPrice: 1.25, CacheWritePrice: 15.625, OutputPrice: 62.50},
}

type CreateAIModelPriceInput struct {
	ModelName       string  `json:"modelName"`
	DisplayName     string  `json:"displayName"`
	InputPrice      float64 `json:"inputPrice"`
	CachedPrice     float64 `json:"cachedPrice"`
	CacheWritePrice float64 `json:"cacheWritePrice"`
	OutputPrice     float64 `json:"outputPrice"`
	Multiplier      float64 `json:"multiplier"`
	Status          string  `json:"status"`
	Sort            int     `json:"sort"`
	Remark          string  `json:"remark"`
}

type UpdateAIModelPriceInput struct {
	ModelName       string  `json:"modelName"`
	DisplayName     string  `json:"displayName"`
	InputPrice      float64 `json:"inputPrice"`
	CachedPrice     float64 `json:"cachedPrice"`
	CacheWritePrice float64 `json:"cacheWritePrice"`
	OutputPrice     float64 `json:"outputPrice"`
	Multiplier      float64 `json:"multiplier"`
	Status          string  `json:"status"`
	Sort            int     `json:"sort"`
	Remark          string  `json:"remark"`
}

// ResolvedAIModelTokenPrice 是最终生效的模型单价，单位为「人民币（元）/ 百万 token」。
// 三个 UnitPrice 为「单价 × 倍率」之后的结果，计费直接用它们。
type ResolvedAIModelTokenPrice struct {
	ModelName           string  `json:"modelName"`
	DisplayName         string  `json:"displayName"`
	PriceID             int32   `json:"priceId"`
	InputPrice          float64 `json:"inputPrice"`
	CachedPrice         float64 `json:"cachedPrice"`
	CacheWritePrice     float64 `json:"cacheWritePrice"`
	OutputPrice         float64 `json:"outputPrice"`
	Multiplier          float64 `json:"multiplier"`
	InputUnitPrice      float64 `json:"inputUnitPrice"`
	CachedUnitPrice     float64 `json:"cachedUnitPrice"`
	CacheWriteUnitPrice float64 `json:"cacheWriteUnitPrice"`
	OutputUnitPrice     float64 `json:"outputUnitPrice"`
	MatchedBy           string  `json:"matchedBy"`
	EffectiveUnitName   string  `json:"effectiveUnitName"`
}

// ListAIModelPricesWithDB 返回模型价格表，并在首次访问时写入内置默认价格，
// 这样升级后不会出现「价格表为空 → AI 全部免费」的计费漏洞。
//
// 价格表不再有「状态」概念：所有行都参与计费，因此不再接受 includeDisabled 参数。
func ListAIModelPricesWithDB(db *gorm.DB) ([]entity.AIModelPrice, error) {
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		return nil, err
	}

	var items []entity.AIModelPrice
	if err := db.Model(&entity.AIModelPrice{}).Order("sort asc, id asc").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// EnsureDefaultAIModelPricesWithDB 幂等地同步内置默认价格，每个数据库只会执行一次。
//
// 行为：
//   - 默认表中新增的模型会自动补入（这样扩充默认价格表后，既有部署启动即生效）；
//   - 内置模型的单价会跟随默认表更新；
//   - 管理员手动改过的行（IsUserModified）与管理员删除的模型都不会被覆盖或复活。
//
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

	// 默认表内容未变化时跳过逐行同步，避免每次启动都做一轮全表比较。
	if current := aimodelPriceDefaultsFingerprint(); aimodelPriceSeededMarker(db) == current {
		aimodelPriceSeeded.Store(cacheKey, struct{}{})
		return nil
	}

	if err := syncDefaultAIModelPrices(db); err != nil {
		return err
	}

	storeAIModelPriceSeededMarker(db, aimodelPriceDefaultsFingerprint())
	aimodelPriceSeeded.Store(cacheKey, struct{}{})
	return nil
}

func syncDefaultAIModelPrices(db *gorm.DB) error {
	for index, seed := range defaultAIModelPrices {
		if err := syncOneDefaultAIModelPrice(db, seed, (index+1)*10); err != nil {
			return err
		}
	}
	return nil
}

// findExistingAIModelPriceForSeed 为一条默认价格找它对应的既有行。
//
// 先按模型 ID 匹配；未命中再按显示名称匹配（命中时第二个 bool 为 true）。
// 这样内置表从「可读名」改成「上游模型 ID」时，旧行会被原地认领并重命名，
// 而不是插入一条重复的新行。
//
// 注意：匹配是大小写不敏感的，所以模型 ID 分支也可能命中旧的可读名行
// （"GPT-5.5" 与 "gpt-5.5"）。调用方必须比较两者的实际值来判断是否需要重命名，
// 不能只看命中了哪个分支。
func findExistingAIModelPriceForSeed(db *gorm.DB, seed entity.AIModelPrice) (entity.AIModelPrice, bool, bool, error) {
	var existing entity.AIModelPrice
	err := db.Where("LOWER(model_name) = LOWER(?)", seed.ModelName).First(&existing).Error
	if err == nil {
		return existing, true, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.AIModelPrice{}, false, false, err
	}

	displayName := strings.TrimSpace(seed.DisplayName)
	if displayName == "" {
		return entity.AIModelPrice{}, false, false, nil
	}
	err = db.Where("LOWER(display_name) = LOWER(?)", displayName).Order("id asc").First(&existing).Error
	if err == nil {
		return existing, true, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.AIModelPrice{}, false, false, nil
	}
	return entity.AIModelPrice{}, false, false, err
}

func syncOneDefaultAIModelPrice(db *gorm.DB, seed entity.AIModelPrice, seedSort int) error {
	existing, found, _, err := findExistingAIModelPriceForSeed(db, seed)
	if err != nil {
		if isAIModelPriceTableMissingError(err) {
			return nil
		}
		return err
	}

	if !found {
		item := seed
		item.Status = AIModelPriceStatusEnabled
		item.Multiplier = normalizeAIModelPriceMultiplier(seed.Multiplier)
		item.Sort = seedSort
		item.IsSeed = true
		if createErr := db.Create(&item).Error; createErr != nil {
			// 并发初始化时可能被其他请求抢先插入，唯一索引冲突可以安全忽略。
			if isAIModelPriceUniqueConflictError(createErr) {
				return nil
			}
			if isAIModelPriceTableMissingError(createErr) {
				return nil
			}
			return createErr
		}
		return nil
	}

	updates := map[string]any{}
	// 旧行把可读名当作模型 ID，这里原地迁移为上游模型 ID，避免出现重复行。
	// 使用精确比较：strings.EqualFold 会把 "GPT-5.5" 与 "gpt-5.5" 视为相等
	// （Unicode 简单折叠里 '.' 与 '-' 等价），那样就漏掉了旧行需要改名的判断。
	if strings.TrimSpace(existing.ModelName) != seed.ModelName {
		updates["model_name"] = seed.ModelName
	}
	// 显示名只在「仍为空」或「仍是旧的默认值（等于旧模型 ID）」时补齐，
	// 不覆盖管理员自定义的显示名。
	trimmedDisplay := strings.TrimSpace(existing.DisplayName)
	if !existing.IsUserModified &&
		trimmedDisplay != seed.DisplayName &&
		(trimmedDisplay == "" || trimmedDisplay == strings.TrimSpace(existing.ModelName)) {
		updates["display_name"] = seed.DisplayName
	}

	// 管理员改过的价格保持不动（但仍然接受改名与默认排序）。
	if !existing.IsUserModified {
		updates["input_price"] = seed.InputPrice
		updates["cached_price"] = seed.CachedPrice
		updates["cache_write_price"] = seed.CacheWritePrice
		updates["output_price"] = seed.OutputPrice
		updates["multiplier"] = normalizeAIModelPriceMultiplier(seed.Multiplier)
		if existing.Sort == 0 || existing.IsSeed {
			updates["sort"] = seedSort
		}
	}
	if len(updates) == 0 {
		return nil
	}

	if err := db.Model(&entity.AIModelPrice{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
		if isAIModelPriceTableMissingError(err) || isAIModelPriceUniqueConflictError(err) {
			return nil
		}
		return err
	}
	return nil
}

// aimodelPriceDefaultsFingerprint 给默认价格表算一个稳定指纹。
// 默认表内容变化（新增模型或调整价格）时指纹随之变化，用于触发一次同步。
func aimodelPriceDefaultsFingerprint() string {
	payload, err := json.Marshal(defaultAIModelPrices)
	if err != nil {
		return ""
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write(payload)
	return strconv.FormatUint(hasher.Sum64(), 16)
}

// aimodelPriceSeededMarker 读取上次同步时记录的默认表指纹。
func aimodelPriceSeededMarker(db *gorm.DB) string {
	var setting entity.SystemSetting
	if err := db.Where("key = ?", aimodelPriceSeededSettingKey).First(&setting).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(setting.Value)
}

func storeAIModelPriceSeededMarker(db *gorm.DB, fingerprint string) {
	if err := db.Where("key = ?", aimodelPriceSeededSettingKey).Delete(&entity.SystemSetting{}).Error; err != nil {
		return
	}
	_ = db.Create(&entity.SystemSetting{
		Key:   aimodelPriceSeededSettingKey,
		Value: fingerprint,
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
	item, err := normalizeAIModelPriceInput(entity.AIModelPrice{
		ModelName:       input.ModelName,
		DisplayName:     input.DisplayName,
		InputPrice:      input.InputPrice,
		CachedPrice:     input.CachedPrice,
		CacheWritePrice: input.CacheWritePrice,
		OutputPrice:     input.OutputPrice,
		Multiplier:      input.Multiplier,
		Status:          input.Status,
		Sort:            input.Sort,
		Remark:          input.Remark,
	})
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

	normalized, err := normalizeAIModelPriceInput(entity.AIModelPrice{
		ModelName:       input.ModelName,
		DisplayName:     input.DisplayName,
		InputPrice:      input.InputPrice,
		CachedPrice:     input.CachedPrice,
		CacheWritePrice: input.CacheWritePrice,
		OutputPrice:     input.OutputPrice,
		Multiplier:      input.Multiplier,
		Status:          input.Status,
		Sort:            input.Sort,
		Remark:          input.Remark,
	})
	if err != nil {
		return entity.AIModelPrice{}, err
	}

	item.ModelName = normalized.ModelName
	item.DisplayName = normalized.DisplayName
	item.InputPrice = normalized.InputPrice
	item.CachedPrice = normalized.CachedPrice
	item.CacheWritePrice = normalized.CacheWritePrice
	item.OutputPrice = normalized.OutputPrice
	item.Multiplier = normalized.Multiplier
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

	// 单价 × 倍率，得到最终计费单价。
	multiplier := normalizeAIModelPriceMultiplier(item.Multiplier)
	return ResolvedAIModelTokenPrice{
		ModelName:           item.ModelName,
		DisplayName:         resolveAIModelPriceDisplayName(*item),
		PriceID:             item.ID,
		InputPrice:          item.InputPrice,
		CachedPrice:         item.CachedPrice,
		CacheWritePrice:     item.CacheWritePrice,
		OutputPrice:         item.OutputPrice,
		Multiplier:          multiplier,
		InputUnitPrice:      roundAIModelUnitPrice(item.InputPrice * multiplier),
		CachedUnitPrice:     roundAIModelUnitPrice(item.CachedPrice * multiplier),
		CacheWriteUnitPrice: roundAIModelUnitPrice(item.CacheWritePrice * multiplier),
		OutputUnitPrice:     roundAIModelUnitPrice(item.OutputPrice * multiplier),
		MatchedBy:           matchedBy,
		EffectiveUnitName:   "元/百万token",
	}, true, nil
}

func findAIModelPriceByModelName(db *gorm.DB, modelName string) (*entity.AIModelPrice, string, error) {
	candidates := []struct {
		query     string
		args      []interface{}
		matchedBy string
	}{
		{"LOWER(model_name) = LOWER(?)", []interface{}{modelName}, "model_name"},
		{"LOWER(display_name) = LOWER(?)", []interface{}{modelName}, "display_name"},
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
	if err := db.Order("sort asc, id asc").Find(&items).Error; err != nil {
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

func normalizeAIModelPriceInput(input entity.AIModelPrice) (entity.AIModelPrice, error) {
	normalizedName := strings.TrimSpace(input.ModelName)
	if normalizedName == "" {
		return entity.AIModelPrice{}, ErrAIModelPriceNameRequired
	}

	sortOrder := input.Sort
	if sortOrder < 0 {
		sortOrder = 0
	}

	return entity.AIModelPrice{
		ModelName:       normalizedName,
		DisplayName:     strings.TrimSpace(input.DisplayName),
		InputPrice:      normalizeAIModelPriceAmount(input.InputPrice),
		CachedPrice:     normalizeAIModelPriceAmount(input.CachedPrice),
		CacheWritePrice: normalizeAIModelPriceAmount(input.CacheWritePrice),
		OutputPrice:     normalizeAIModelPriceAmount(input.OutputPrice),
		Multiplier:      normalizeAIModelPriceMultiplier(input.Multiplier),
		// 价格表不再有状态概念，写入时固定为启用，避免出现静默不计费的行。
		Status: AIModelPriceStatusEnabled,
		Sort:   sortOrder,
		Remark: strings.TrimSpace(input.Remark),
	}, nil
}

// normalizeAIModelPriceMultiplier 把倍率收敛到合法范围：默认 1，负值/非法值回落到 1。
func normalizeAIModelPriceMultiplier(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	return value
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
