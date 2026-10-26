package service

import (
	"errors"
	"net"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrLocationNotConfigured = errors.New("尚未配置 UApiPro API 密钥")
	ErrLocationEmptyResponse = errors.New("上游响应没有可识别的归属地信息")
	ErrLocationInvalidKey    = errors.New("归属地类型或查询值不合法")
	ErrLocationForbidden     = errors.New("只能查询与当前账号关联的 IP")
	ErrLocationUnavailable   = errors.New("归属地数据库不可用")
)

// LocationCacheListQuery 是管理员查看归属地缓存的筛选条件。
type LocationCacheListQuery struct {
	Kind    string
	Keyword string
	Source  string
}

// LocationCacheListResult 是归属地缓存管理列表响应。
type LocationCacheListResult struct {
	Data       []entity.LocationCache   `json:"data"`
	Total      int64                    `json:"total"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"pageSize"`
	Configured bool                     `json:"configured"`
	Summary    LocationCacheListSummary `json:"summary"`
}

type LocationCacheListSummary struct {
	Total          int64 `json:"total"`
	WithAddress    int64 `json:"withAddress"`
	WithoutAddress int64 `json:"withoutAddress"`
}

type LocationCacheDeleteItem struct {
	LookupKey string `json:"lookupKey"`
	Source    string `json:"source"`
}

type LocationCacheBatchDeleteRequest struct {
	Kind    string                    `json:"kind"`
	Keyword string                    `json:"keyword"`
	Source  string                    `json:"source"`
	Items   []LocationCacheDeleteItem `json:"items"`
	All     bool                      `json:"all"`
	Except  []LocationCacheDeleteItem `json:"except"`
}

// Source identifies stored rows; QuerySource selects the source for this lookup.
type LocationCacheBatchRefreshRequest struct {
	Kind        string                    `json:"kind"`
	Keyword     string                    `json:"keyword"`
	Source      string                    `json:"source"`
	Items       []LocationCacheDeleteItem `json:"items"`
	All         bool                      `json:"all"`
	Except      []LocationCacheDeleteItem `json:"except"`
	QuerySource string                    `json:"querySource"`
}

type LocationCacheRefreshFailure struct {
	LookupKey string `json:"lookupKey"`
	Source    string `json:"source"`
	Message   string `json:"message"`
}

type LocationCacheBatchRefreshResult struct {
	Requested int                           `json:"requested"`
	Refreshed int                           `json:"refreshed"`
	Failed    int                           `json:"failed"`
	Failures  []LocationCacheRefreshFailure `json:"failures"`
}

// ListLocationCacheWithDB 按类型和关键字分页读取已持久化的归属地信息。
func ListLocationCacheWithDB(db *gorm.DB, query LocationCacheListQuery, page int, pageSize int) (LocationCacheListResult, error) {
	if db == nil {
		return LocationCacheListResult{}, ErrLocationUnavailable
	}
	if query.Kind != "" && query.Kind != entity.LocationKindIP && query.Kind != entity.LocationKindPhone {
		return LocationCacheListResult{}, ErrLocationInvalidKey
	}
	if query.Source != "" {
		if query.Kind == "" {
			return LocationCacheListResult{}, ErrLocationInvalidKey
		}
		if _, err := normalizeLocationRecordSource(query.Kind, query.Source); err != nil {
			return LocationCacheListResult{}, err
		}
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	base := db.Model(&entity.LocationCache{})
	if kind := strings.TrimSpace(query.Kind); kind == entity.LocationKindIP || kind == entity.LocationKindPhone {
		base = base.Where("kind = ?", kind)
	}
	if source := strings.TrimSpace(query.Source); source != "" {
		base = base.Where("source = ?", source)
	}
	if condition, args := util.BuildInstrCondition(query.Keyword,
		"COALESCE(lookup_key, '')",
		"COALESCE(address, '')",
	); condition != "" {
		base = base.Where(condition, args...)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return LocationCacheListResult{}, err
	}
	withAddress := int64(0)
	withoutAddress := int64(0)
	if err := base.Session(&gorm.Session{}).Where("COALESCE(address, '') <> ''").Count(&withAddress).Error; err != nil {
		return LocationCacheListResult{}, err
	}
	if err := base.Session(&gorm.Session{}).Where("COALESCE(address, '') = ''").Count(&withoutAddress).Error; err != nil {
		return LocationCacheListResult{}, err
	}
	rows := make([]entity.LocationCache, 0)
	if err := base.Order("fetched_at DESC, lookup_key ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		return LocationCacheListResult{}, err
	}
	// 展示文本统一用 RawJSON 重新拼：历史行的 address 只有 region，缺运营商。
	// 只改返回值，不动数据库里的 address 列（关键字筛选仍按已存列匹配）。
	for index := range rows {
		rows[index].Address = displayAddressFromCacheRow(rows[index])
	}
	return LocationCacheListResult{
		Data: rows, Total: total, Page: page, PageSize: pageSize,
		Configured: GetUAPIAPIKeyWithDB(db) != "",
		Summary:    LocationCacheListSummary{Total: total, WithAddress: withAddress, WithoutAddress: withoutAddress},
	}, nil
}

// DeleteLocationCacheWithDB 删除一条归属地缓存，使下一次展示请求重新查询上游。
func DeleteLocationCacheWithDB(db *gorm.DB, kind string, lookupKey string, source ...string) error {
	kind, lookupKey, err := normalizeLocationAdminKey(kind, lookupKey)
	if err != nil {
		return err
	}
	if db == nil {
		return ErrLocationUnavailable
	}
	requestedSource := ""
	if len(source) > 0 {
		requestedSource = source[0]
	}
	normalizedSource, err := normalizeLocationRecordSource(kind, requestedSource)
	if err != nil {
		return err
	}
	return db.Where("kind = ? AND lookup_key = ? AND source = ?", kind, lookupKey, normalizedSource).Delete(&entity.LocationCache{}).Error
}

// DeleteLocationCacheBatchWithDB 批量删除显式选择，或删除当前筛选结果的全部记录（排除例外）。
// All 模式必须携带 kind，且 keyword/source 只收窄范围，不允许空条件删除两类缓存。
func DeleteLocationCacheBatchWithDB(db *gorm.DB, request LocationCacheBatchDeleteRequest) (int64, error) {
	if db == nil {
		return 0, ErrLocationUnavailable
	}
	kind := strings.TrimSpace(request.Kind)
	if kind != entity.LocationKindIP && kind != entity.LocationKindPhone {
		return 0, ErrLocationInvalidKey
	}
	if request.All && len(request.Items) > 0 {
		return 0, ErrLocationInvalidKey
	}
	if !request.All && len(request.Items) == 0 {
		return 0, ErrLocationInvalidKey
	}
	if len(request.Items) > 1000 || len(request.Except) > 10000 {
		return 0, ErrLocationInvalidKey
	}

	if !request.All {
		var deleted int64
		for _, item := range request.Items {
			itemKind, key, err := normalizeLocationAdminKey(kind, item.LookupKey)
			if err != nil || itemKind != kind {
				return deleted, ErrLocationInvalidKey
			}
			source, err := normalizeLocationRecordSource(kind, item.Source)
			if err != nil {
				return deleted, err
			}
			result := db.Where("kind = ? AND lookup_key = ? AND source = ?", kind, key, source).Delete(&entity.LocationCache{})
			if result.Error != nil {
				return deleted, result.Error
			}
			deleted += result.RowsAffected
		}
		return deleted, nil
	}

	query := db.Where("kind = ?", kind)
	if source := strings.TrimSpace(request.Source); source != "" {
		normalizedSource, err := normalizeLocationRecordSource(kind, source)
		if err != nil {
			return 0, err
		}
		query = query.Where("source = ?", normalizedSource)
	}
	if condition, args := util.BuildInstrCondition(request.Keyword, "COALESCE(lookup_key, '')", "COALESCE(address, '')"); condition != "" {
		query = query.Where(condition, args...)
	}
	if len(request.Except) > 0 {
		conditions := make([]string, 0, len(request.Except))
		args := make([]any, 0, len(request.Except)*2)
		for _, item := range request.Except {
			itemKind, key, err := normalizeLocationAdminKey(kind, item.LookupKey)
			if err != nil || itemKind != kind {
				return 0, ErrLocationInvalidKey
			}
			exceptSource, err := normalizeLocationRecordSource(kind, item.Source)
			if err != nil {
				return 0, err
			}
			conditions = append(conditions, "NOT (lookup_key = ? AND source = ?)")
			args = append(args, key, exceptSource)
		}
		query = query.Where(strings.Join(conditions, " AND "), args...)
	}
	result := query.Delete(&entity.LocationCache{})
	return result.RowsAffected, result.Error
}

// RefreshLocationCacheBatchWithDB 按查询目标合并重查，成功后转换所选记录来源。
// 同一 key 只查询一次；目标来源已有记录时合并，未选中的其他来源不变。
func RefreshLocationCacheBatchWithDB(db *gorm.DB, request LocationCacheBatchRefreshRequest, options LocationQueryOptions) (LocationCacheBatchRefreshResult, error) {
	result := LocationCacheBatchRefreshResult{Failures: make([]LocationCacheRefreshFailure, 0)}
	if db == nil {
		return result, ErrLocationUnavailable
	}
	kind := strings.TrimSpace(request.Kind)
	if kind != entity.LocationKindIP && kind != entity.LocationKindPhone {
		return result, ErrLocationInvalidKey
	}
	querySource, err := normalizeLocationSource(kind, request.QuerySource)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(options.UAPIKey) == "" && querySource != "" {
		return result, ErrLocationNotConfigured
	}
	if request.All && len(request.Items) > 0 {
		return result, ErrLocationInvalidKey
	}
	if !request.All && len(request.Items) == 0 {
		return result, ErrLocationInvalidKey
	}
	if len(request.Items) > 1000 || len(request.Except) > 10000 {
		return result, ErrLocationInvalidKey
	}

	rows := make([]entity.LocationCache, 0)
	if request.All {
		query := db.Model(&entity.LocationCache{}).Where("kind = ?", kind)
		if source := strings.TrimSpace(request.Source); source != "" {
			normalizedSource, err := normalizeLocationRecordSource(kind, source)
			if err != nil {
				return result, err
			}
			query = query.Where("source = ?", normalizedSource)
		}
		if condition, args := util.BuildInstrCondition(request.Keyword, "COALESCE(lookup_key, '')", "COALESCE(address, '')"); condition != "" {
			query = query.Where(condition, args...)
		}
		if len(request.Except) > 0 {
			conditions := make([]string, 0, len(request.Except))
			args := make([]any, 0, len(request.Except)*2)
			for _, item := range request.Except {
				itemKind, key, err := normalizeLocationAdminKey(kind, item.LookupKey)
				if err != nil || itemKind != kind {
					return result, ErrLocationInvalidKey
				}
				exceptSource, err := normalizeLocationRecordSource(kind, item.Source)
				if err != nil {
					return result, err
				}
				conditions = append(conditions, "NOT (lookup_key = ? AND source = ?)")
				args = append(args, key, exceptSource)
			}
			query = query.Where(strings.Join(conditions, " AND "), args...)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return result, err
		}
		if count > 1000 {
			return result, ErrLocationInvalidKey
		}
		if err := query.Order("lookup_key ASC, source ASC").Find(&rows).Error; err != nil {
			return result, err
		}
	} else {
		seen := make(map[string]struct{}, len(request.Items))
		for _, item := range request.Items {
			itemKind, key, err := normalizeLocationAdminKey(kind, item.LookupKey)
			if err != nil || itemKind != kind {
				return result, ErrLocationInvalidKey
			}
			source, err := normalizeLocationRecordSource(kind, item.Source)
			if err != nil {
				return result, err
			}
			identity := key + "\x00" + source
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			rows = append(rows, entity.LocationCache{Kind: kind, LookupKey: key, Source: source})
		}
	}

	result.Requested = len(rows)
	type refreshTarget struct {
		key    string
		source string
		rows   []entity.LocationCache
	}
	targets := make(map[string]*refreshTarget, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		identity := row.LookupKey + "\x00" + querySource
		target, exists := targets[identity]
		if !exists {
			target = &refreshTarget{key: row.LookupKey, source: querySource}
			targets[identity] = target
			order = append(order, identity)
		}
		target.rows = append(target.rows, row)
	}
	var abortErr error
	for _, identity := range order {
		target := targets[identity]
		err := abortErr
		if err == nil {
			recordSources := make([]string, 0, len(target.rows))
			for _, row := range target.rows {
				recordSources = append(recordSources, row.Source)
			}
			_, err = refreshLocationCacheRecordSourcesWithDB(db, kind, target.key, recordSources, LocationQueryOptions{UAPIKey: options.UAPIKey, Source: target.source})
			if isLocationAbortError(err) {
				abortErr = err
			}
		}
		if err != nil {
			result.Failed += len(target.rows)
			for _, row := range target.rows {
				result.Failures = append(result.Failures, LocationCacheRefreshFailure{
					LookupKey: row.LookupKey,
					Source:    row.Source,
					Message:   LocationQueryErrorMessage(err),
				})
			}
			continue
		}
		result.Refreshed += len(target.rows)
	}
	return result, nil
}

// 历史来源可用于标识已有记录，不能作为上游查询来源。
func normalizeLocationRecordSource(kind string, source string) (string, error) {
	if kind == entity.LocationKindIP && strings.TrimSpace(source) == entity.LocationSourceLegacy {
		return entity.LocationSourceLegacy, nil
	}
	return normalizeLocationSource(kind, source)
}

func normalizeLocationAdminKey(kind string, key string) (string, string, error) {
	kind, key = strings.TrimSpace(kind), locationCacheLookupKey(key)
	switch kind {
	case entity.LocationKindPhone:
		key = NormalizeLocationPhone(key)
	case entity.LocationKindIP:
		key = normalizeIPLookupKey(key)
	default:
		return "", "", ErrLocationInvalidKey
	}
	if key == "" {
		return "", "", ErrLocationInvalidKey
	}
	return kind, key, nil
}

// RefreshLocationCacheWithDB 只在新结果成功查询且落库后替换旧行，不先删缓存。
func RefreshLocationCacheWithDB(db *gorm.DB, kind string, key string, options LocationQueryOptions) (entity.LocationCache, error) {
	return refreshLocationCacheRecordSourcesWithDB(db, kind, key, nil, options)
}

// RefreshLocationCacheRecordWithDB 成功后将指定原记录转换为本次查询来源，未选来源不变。
func RefreshLocationCacheRecordWithDB(db *gorm.DB, kind string, key string, recordSource string, options LocationQueryOptions) (entity.LocationCache, error) {
	return refreshLocationCacheRecordSourcesWithDB(db, kind, key, []string{recordSource}, options)
}

func refreshLocationCacheRecordSourcesWithDB(db *gorm.DB, kind string, key string, recordSources []string, options LocationQueryOptions) (entity.LocationCache, error) {
	kind, key, err := normalizeLocationAdminKey(kind, key)
	if err != nil || (kind == entity.LocationKindIP && IsPrivateOrReservedIP(key)) {
		return entity.LocationCache{}, ErrLocationInvalidKey
	}
	if db == nil {
		return entity.LocationCache{}, ErrLocationUnavailable
	}
	options.Source, err = normalizeLocationSource(kind, options.Source)
	if err != nil {
		return entity.LocationCache{}, err
	}
	if strings.TrimSpace(options.UAPIKey) == "" && options.Source != "" {
		return entity.LocationCache{}, ErrLocationNotConfigured
	}
	oldSources := make([]string, 0, len(recordSources))
	seenSources := make(map[string]bool, len(recordSources))
	for _, source := range recordSources {
		source, err = normalizeLocationRecordSource(kind, source)
		if err != nil {
			return entity.LocationCache{}, err
		}
		if source != options.Source && !seenSources[source] {
			seenSources[source] = true
			oldSources = append(oldSources, source)
		}
	}
	var persist func(entity.LocationCache, bool) error
	if len(recordSources) > 0 {
		persist = func(row entity.LocationCache, writeResult bool) error {
			return db.Transaction(func(tx *gorm.DB) error {
				if writeResult {
					if err := storeLocationCacheRows(tx, []entity.LocationCache{row}); err != nil {
						return err
					}
				} else {
					var target entity.LocationCache
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("kind = ? AND lookup_key = ? AND source = ?", kind, key, options.Source).First(&target).Error; err != nil {
						return err
					}
				}
				if len(oldSources) == 0 {
					return nil
				}
				return tx.Where("kind = ? AND lookup_key = ? AND source IN ?", kind, key, oldSources).Delete(&entity.LocationCache{}).Error
			})
		}
	}
	rows, err := queryLocationRowsWithPersistence(db, newLocationClient(options.UAPIKey), kind, []string{key}, options.Source, true, persist)
	if err != nil {
		return entity.LocationCache{}, err
	}
	if len(rows) == 0 {
		return entity.LocationCache{}, ErrLocationEmptyResponse
	}
	var row entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", kind, key, options.Source).First(&row).Error; err != nil {
		return entity.LocationCache{}, err
	}
	return row, nil
}

// ManualLookupLocationCacheWithDB 查询一个管理员手动输入的 IP/手机号，并在成功后写入持久化缓存。
//
// 该函数复用显式刷新的校验和 upsert 语义：非法输入、私网 IP、历史来源不发起查询；
// 手机号和普通 IP 来源可无密钥查询，商业增强需要密钥。成功才会替换同 kind/key/source 的记录。这样管理页手动添加和已有
// 「重新查询」不会形成两套容易漂移的安全规则。
func ManualLookupLocationCacheWithDB(db *gorm.DB, kind string, key string, options LocationQueryOptions) (entity.LocationCache, error) {
	return RefreshLocationCacheWithDB(db, kind, key, options)
}

// ResolveOwnLocationIPsWithDB 只接受当前账号字段、账单或审计中真实关联的 IP。
// 即使操作者是管理员，个人接口也不放宽成任意付费批量查询。
func ResolveOwnLocationIPsWithDB(db *gorm.DB, operator entity.User, requested []string) ([]string, error) {
	if operator.ID <= 0 {
		return nil, ErrLocationForbidden
	}
	owned := make(map[string]bool)
	for _, ip := range []string{operator.LastLoginIP, operator.RegisterIP} {
		if key := locationCacheLookupKey(ip); key != "" {
			owned[key] = true
		}
	}
	if len(requested) == 0 {
		return []string{operator.LastLoginIP, operator.RegisterIP}, nil
	}
	keys := make([]string, 0, len(requested))
	for _, raw := range requested {
		key := locationCacheLookupKey(raw)
		if net.ParseIP(key) == nil {
			return nil, ErrLocationInvalidKey
		}
		keys = append(keys, key)
	}
	if db == nil {
		return nil, ErrLocationUnavailable
	}
	for _, model := range []any{&entity.BillingRecord{}, &entity.Log{}} {
		if !db.Migrator().HasTable(model) {
			continue
		}
		var stored []string
		if err := db.Model(model).Where("user_id = ? AND LOWER(TRIM(client_ip)) IN ?", operator.ID, keys).Distinct().Pluck("client_ip", &stored).Error; err != nil {
			return nil, err
		}
		for _, ip := range stored {
			owned[locationCacheLookupKey(ip)] = true
		}
	}
	for _, key := range keys {
		if !owned[key] {
			return nil, ErrLocationForbidden
		}
	}
	return keys, nil
}

// LocationQueryErrorMessage 只返回固定中文原因，避免上游报文泄露密钥或其他敏感数据。
func LocationQueryErrorMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrLocationNotConfigured):
		return ErrLocationNotConfigured.Error()
	case errors.Is(err, ErrLocationEmptyResponse):
		return ErrLocationEmptyResponse.Error()
	case errors.Is(err, ErrLocationInvalidKey):
		return ErrLocationInvalidKey.Error()
	case errors.Is(err, ErrLocationForbidden):
		return ErrLocationForbidden.Error()
	case errors.Is(err, uapi.ErrUnauthorized):
		return "UApiPro 密钥无效或已过期，请检查系统设置"
	case errors.Is(err, uapi.ErrInsufficientCredits):
		return "UApiPro 积分或配额不足"
	case errors.Is(err, uapi.ErrForbidden):
		return "UApiPro 密钥没有此接口的访问权限"
	case errors.Is(err, uapi.ErrRateLimited):
		return "UApiPro 查询频率受限，请稍后重试"
	case errors.Is(err, uapi.ErrNotFound):
		return "UApiPro 未找到对应的归属地信息"
	case errors.Is(err, uapi.ErrTimeout), errors.Is(err, uapi.ErrNetwork):
		return "UApiPro 网络请求失败，请稍后重试"
	case errors.Is(err, uapi.ErrUpstream):
		return "UApiPro 服务异常，请稍后重试"
	default:
		return "归属地查询或保存失败，请检查服务日志"
	}
}
