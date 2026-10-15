package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// locationLookupTimeout 单次归属地查询的上游超时上限。
	locationLookupTimeout = 10 * time.Second
	// locationLookupMaxAttempts 单个归属地查询的最大尝试次数。
	//
	// 3 次是为了扛住偶发网络抖动；同时它是「密钥配错」的判定阈值 ——
	// 连续 3 次全失败就认为不是抖动，而是配置问题。
	locationLookupMaxAttempts = 3
)

// 接口地址在包级变量里，便于测试用 httptest 替换。
// 生产环境保持 uapi 包的默认完整地址（含 /api/v1/）。
var (
	locationPhoneInfoURL = uapi.DefaultPhoneInfoURL
	locationIPInfoURL    = uapi.DefaultIPInfoURL
)

// newLocationClient 构造归属地客户端并应用可覆盖的接口地址。
//
// **关掉客户端内部重试**（SetMaxRetries(0)）：重试统一由 lookupLocationsWithRetry 编排。
// 两边都重试会相乘 —— 3（本层）× 4（客户端）= 12 次请求，既违背「连续 3 次失败就放弃」
// 的意图，也会在最坏情况下把一个号码打成十几次上游调用。
func newLocationClient(apiKey string) *uapi.Client {
	return uapi.NewClient(apiKey).
		SetPhoneInfoURL(locationPhoneInfoURL).
		SetIPInfoURL(locationIPInfoURL).
		SetMaxRetries(0)
}

// lookupLocationsWithRetry 顺序查询一组 key，并在判定「密钥/权限/配额」问题时**整批放弃**。
//
// 为什么需要这个保护：密钥配错（或积分耗尽）时，上游对**每一个** key 都返回鉴权失败。
// 一页 100 个手机号就是 100 次注定失败的请求，既刷满无意义的失败日志，
// 也把管理页拖到几十秒。用户诉求：**连续 3 次都错误就放弃，包括后面的数据**。
//
// 两个正交的判断，刻意分开：
//
//  1. **每个 key 最多尝试 locationLookupMaxAttempts 次**（扛偶发网络抖动）；
//     3 次都失败才算「这个 key 彻底失败」。
//  2. 某 key 彻底失败后，若错误属于 abort 类（401/402/403/429 ——
//     对同一密钥的所有请求都会复现）→ **立即整批放弃**，后面的数据一次都不查。
//     若只是单条数据问题（404 该号码无记录、5xx 抖动）→ 只跳过这一条继续查，
//     不让一个查不到的号码让整页白干。
//
// onSuccess 只在确实拿到归属地时回调，避免把空结果写进持久化缓存。
func lookupLocationsWithRetry(
	keys []string,
	kind string,
	lookup func(key string) locationFlightResult,
	onSuccess func(key string, result locationFlightResult),
) {
	for _, key := range keys {
		var result locationFlightResult
		succeeded := false

		for attempt := 1; attempt <= locationLookupMaxAttempts; attempt++ {
			result = lookup(key)
			if result.ok {
				succeeded = true
				break
			}
		}

		if succeeded {
			onSuccess(key, result)
			continue
		}

		if result.abort {
			logger.SysLog(fmt.Sprintf(
				"归属地查询连续 %d 次失败且属于密钥/配额类错误，放弃本批剩余的 %s 查询（请检查系统设置中的 UApiPro API 密钥与积分）",
				locationLookupMaxAttempts, kind))
			return
		}
	}
}

// isLocationAbortError 判断失败是否属于「换 key 才能解决」的类别。
//
// 这类错误对同一密钥的所有请求都会复现，继续查后面的数据纯属浪费：
//   - 401 鉴权失败：密钥错误或过期；
//   - 402 积分/配额不足：充值前查什么都会失败；
//   - 403 请求被拒绝：密钥无权访问该接口；
//   - 429 触发限流：再打只会更糟。
//
// 其余（404 该号码无记录、5xx 上游抖动、网络超时）都只是单条数据或瞬时问题，
// 交给「每个 key 重试 N 次」处理即可，不该直接放弃整页。
func isLocationAbortError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, uapi.ErrUnauthorized) ||
		errors.Is(err, uapi.ErrInsufficientCredits) ||
		errors.Is(err, uapi.ErrForbidden) ||
		errors.Is(err, uapi.ErrRateLimited)
}

// locationLookupGroup 合并「同一 key 的并发归属地查询」，见 location_flight.go。
var locationLookupGroup = newLocationFlightGroup()

// PhoneLocation 是手机号归属地的对外结果。
type PhoneLocation struct {
	Province string `json:"province"`
	City     string `json:"city"`
	SP       string `json:"sp"`
	AreaCode string `json:"areaCode"`
	// Address 是展示文本，例如「云南玉溪 移动」。
	Address string `json:"address"`
}

// IPLocation 是 IP 归属地的对外结果。
type IPLocation struct {
	IP     string `json:"ip"`
	Region string `json:"region"`
	ISP    string `json:"isp"`
	LLC    string `json:"llc"`
	ASN    string `json:"asn"`
	// Address 是展示文本；IP 直接用上游的 region（文档定义其格式为国家 省份 城市）。
	Address string `json:"address"`
}

// LocationQueryOptions 控制归属地查询行为。
type LocationQueryOptions struct {
	// UAPIKey 为空表示未配置：直接返回空结果、**不发起外呼**。
	UAPIKey string
	// Source 传给 IP 查询接口的 source 参数；仅支持 "commercial"。
	Source string
}

// FormatPhoneLocationAddress 把手机号归属地拼成展示文本。
//
// 规则：`{province}{city} {sp}`；city 为空或与 province 相同时只取 province；
// sp 为空则省略末段。全部为空时返回空串，由调用方降级为只显示原始号码。
func FormatPhoneLocationAddress(province, city, sp string) string {
	province = strings.TrimSpace(province)
	city = strings.TrimSpace(city)
	sp = strings.TrimSpace(sp)

	region := province
	if city != "" && city != province {
		region += city
	}
	if region == "" {
		return sp
	}
	if sp == "" {
		return region
	}
	return region + " " + sp
}

// NormalizeLocationPhone 归一化手机号，明显非法时返回空串。
//
// 只做长度与字符校验（11 位、1 开头、全数字）：归属地查询本身不校验号段是否存在，
// 这里也不该比上游更严格，否则会把能查到的号码挡在门外。
func NormalizeLocationPhone(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != 11 || trimmed[0] != '1' {
		return ""
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] < '0' || trimmed[i] > '9' {
			return ""
		}
	}
	return trimmed
}

// IsPrivateOrReservedIP 判断是否为内网/保留地址。
//
// 这类地址在公网归属地库里必然查不到（上游返回 404），
// 查了既浪费积分又白占速率配额，因此直接跳过。
// 非 IP 的输入（域名）返回 false —— 它可能解析到公网地址，应当照常查询。
func IsPrivateOrReservedIP(candidate string) bool {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return true
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 255.0.0.0/8 广播段与 0.0.0.0/8 保留段。
		if v4[0] == 255 || v4[0] == 0 {
			return true
		}
	}
	return false
}

// normalizeIPLookupKey 归一化 IP/域名查询键，非法时返回空串。
func normalizeIPLookupKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 253 {
		return ""
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == ':':
		default:
			return ""
		}
	}
	return trimmed
}

// normalizePhoneLookupKeys 归一化并去重手机号，剔除非法值。
func normalizePhoneLookupKeys(phones []string) []string {
	seen := make(map[string]struct{}, len(phones))
	keys := make([]string, 0, len(phones))
	for _, raw := range phones {
		phone := NormalizeLocationPhone(raw)
		if phone == "" {
			continue
		}
		if _, exists := seen[phone]; exists {
			continue
		}
		seen[phone] = struct{}{}
		keys = append(keys, phone)
	}
	return keys
}

// normalizeIPLookupKeys 归一化并去重 IP/域名。
//
// 回环地址与私有地址不能交给公网归属地 API 查询，但它们仍然是日志中
// 合法且常见的来源 IP，后续会由 lookupLocationUpstream 生成本地结果。
func normalizeIPLookupKeys(ips []string) []string {
	seen := make(map[string]struct{}, len(ips))
	keys := make([]string, 0, len(ips))
	for _, raw := range ips {
		candidate := normalizeIPLookupKey(raw)
		if candidate == "" || shouldSkipIPLookup(candidate) {
			continue
		}
		lower := strings.ToLower(candidate)
		if _, exists := seen[lower]; exists {
			continue
		}
		seen[lower] = struct{}{}
		keys = append(keys, candidate)
	}
	return keys
}

func shouldSkipIPLookup(candidate string) bool {
	ip := net.ParseIP(candidate)
	if ip == nil {
		return false
	}
	// 回环与 RFC1918/ULA 私有地址会由本地合成结果，不能跳过。
	if ip.IsLoopback() || ip.IsPrivate() {
		return false
	}
	return IsPrivateOrReservedIP(candidate)
}

// syntheticIPLocation 为回环/私有 IP 生成不需要外呼的本地归属地结果。
// 这类地址没有公网运营商归属地，使用明确的中文标签比返回空值更便于排查。
func syntheticIPLocation(key string) (locationFlightResult, bool) {
	ip := net.ParseIP(strings.TrimSpace(key))
	if ip == nil {
		return locationFlightResult{}, false
	}

	region := ""
	switch {
	case ip.IsLoopback():
		region = "本地地址"
	case ip.IsPrivate():
		region = "私有地址"
	default:
		return locationFlightResult{}, false
	}

	payload := struct {
		IP        string  `json:"ip"`
		Region    string  `json:"region"`
		ISP       string  `json:"isp"`
		ASN       string  `json:"asn"`
		LLC       string  `json:"llc"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}{
		IP:        key,
		Region:    region,
		ISP:       "N/A",
		ASN:       "N/A",
		Latitude:  0,
		Longitude: 0,
	}
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return locationFlightResult{err: err}, true
	}
	return locationFlightResult{address: region, rawJSON: string(rawJSON), ok: true}, true
}

// locationCacheLookupKey 生成持久化缓存主键的 lookup_key 部分（统一小写）。
func locationCacheLookupKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// normalizeLocationSource validates the source dimension used by the cache.
// Phone lookups never carry a source; IP lookups support the default and
// commercial upstream modes.
func normalizeLocationSource(kind string, raw string) (string, error) {
	source := strings.TrimSpace(raw)
	switch kind {
	case entity.LocationKindPhone:
		if source != "" {
			return "", ErrLocationInvalidKey
		}
	case entity.LocationKindIP:
		if source != "" && source != uapi.SourceCommercial {
			return "", ErrLocationInvalidKey
		}
	default:
		return "", ErrLocationInvalidKey
	}
	return source, nil
}

// loadLocationCacheRows 永久复用已有有效地址；管理员显式刷新时才重新查询。
func loadLocationCacheRows(db *gorm.DB, kind string, source string, lookupKeys []string) map[string]entity.LocationCache {
	result := make(map[string]entity.LocationCache, len(lookupKeys))
	if db == nil || len(lookupKeys) == 0 {
		return result
	}

	normalized := make([]string, 0, len(lookupKeys))
	for _, key := range lookupKeys {
		if lower := locationCacheLookupKey(key); lower != "" {
			normalized = append(normalized, lower)
		}
	}
	if len(normalized) == 0 {
		return result
	}

	rows := make([]entity.LocationCache, 0, len(normalized))
	if err := db.Where("kind = ? AND source = ? AND lookup_key IN ?", kind, source, normalized).Find(&rows).Error; err != nil {
		// 缓存读失败不影响正确性：回源查询即可。
		logger.SysLog(fmt.Sprintf("读取归属地缓存失败（将回源查询）：%v", err))
		return result
	}

	for _, row := range rows {
		if strings.TrimSpace(row.Address) == "" {
			continue
		}
		result[locationCacheLookupKey(row.LookupKey)] = row
	}
	return result
}

// storeLocationCacheRows 写入/刷新持久化缓存。写失败只记日志，不影响本次返回。
func storeLocationCacheRows(db *gorm.DB, rows []entity.LocationCache) error {
	if db == nil || len(rows) == 0 {
		return nil
	}

	now := time.Now()
	for i := range rows {
		rows[i].FetchedAt = now
	}

	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}, {Name: "lookup_key"}, {Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"address", "raw_json", "fetched_at", "updated_at"}),
	}).Create(&rows).Error; err != nil {
		logger.SysLog(fmt.Sprintf("写入归属地缓存失败（不影响本次结果）：%v", err))
		return err
	}
	return nil
}

// LookupPhoneLocationsWithDB 批量查询手机号归属地。
//
// 返回 map 的键是归一化后的手机号。查不到的号码（未配置密钥、格式非法、上游无记录）
// 不会出现在结果里，调用方据此回退为只显示原始号码。
func LookupPhoneLocationsWithDB(db *gorm.DB, phones []string, options LocationQueryOptions) map[string]PhoneLocation {
	result, _ := LookupPhoneLocationsDetailedWithDB(db, phones, options)
	return result
}

// LookupPhoneLocationsDetailedWithDB 为管理员提供失败原因，普通页面可继续静默降级。
func LookupPhoneLocationsDetailedWithDB(db *gorm.DB, phones []string, options LocationQueryOptions) (map[string]PhoneLocation, error) {
	result := make(map[string]PhoneLocation)

	normalizedKeys := normalizePhoneLookupKeys(phones)
	if len(normalizedKeys) == 0 {
		return result, nil
	}

	// ① 先读持久化缓存（命中即零积分消耗）。
	cached := loadLocationCacheRows(db, entity.LocationKindPhone, "", normalizedKeys)
	for _, rawKey := range normalizedKeys {
		row, ok := cached[locationCacheLookupKey(rawKey)]
		if !ok {
			continue
		}
		result[rawKey] = phoneLocationFromCacheRow(row)
	}

	// ② 未命中的部分回源查询。
	missing := missingPhoneKeys(normalizedKeys, cached)
	apiKey := strings.TrimSpace(options.UAPIKey)
	if len(missing) == 0 {
		return result, nil
	}
	if apiKey == "" {
		return result, ErrLocationNotConfigured
	}

	rows, err := queryLocationRows(db, newLocationClient(apiKey), entity.LocationKindPhone, missing, "", false)

	// ③ 落库并并入结果。
	for _, row := range rows {
		result[row.LookupKey] = phoneLocationFromCacheRow(row)
	}
	return result, err
}

// LookupIPLocationsWithDB 批量查询 IP 归属地。
func LookupIPLocationsWithDB(db *gorm.DB, ips []string, options LocationQueryOptions) map[string]IPLocation {
	result, _ := LookupIPLocationsDetailedWithDB(db, ips, options)
	return result
}

// LookupIPLocationsDetailedWithDB 返回部分成功数据，同时保留可供管理端诊断的错误。
func LookupIPLocationsDetailedWithDB(db *gorm.DB, ips []string, options LocationQueryOptions) (map[string]IPLocation, error) {
	result := make(map[string]IPLocation)

	source, sourceErr := normalizeLocationSource(entity.LocationKindIP, options.Source)
	if sourceErr != nil {
		return result, sourceErr
	}
	normalizedKeys := normalizeIPLookupKeys(ips)
	if len(normalizedKeys) == 0 {
		return result, nil
	}

	cached := loadLocationCacheRows(db, entity.LocationKindIP, source, normalizedKeys)
	for _, rawKey := range normalizedKeys {
		row, ok := cached[locationCacheLookupKey(rawKey)]
		if !ok {
			continue
		}
		result[rawKey] = ipLocationFromCacheRow(row)
	}

	missing := missingIPKeys(normalizedKeys, cached)
	if len(missing) == 0 {
		return result, nil
	}

	// 回环/私有 IP 由本地合成并持久化，不需要密钥，也不应因未配置密钥而
	// 让日志列表退化为空。公网地址再走 UApiPro，并保持原有错误语义。
	syntheticKeys, upstreamKeys := splitSyntheticIPKeys(missing)
	if len(syntheticKeys) > 0 {
		rows, err := queryLocationRows(db, nil, entity.LocationKindIP, syntheticKeys, source, false)
		for _, row := range rows {
			result[row.LookupKey] = ipLocationFromCacheRow(row)
		}
		if err != nil && len(upstreamKeys) == 0 {
			return result, err
		}
	}
	if len(upstreamKeys) == 0 {
		return result, nil
	}

	apiKey := strings.TrimSpace(options.UAPIKey)
	if apiKey == "" {
		return result, ErrLocationNotConfigured
	}

	rows, err := queryLocationRows(db, newLocationClient(apiKey), entity.LocationKindIP, upstreamKeys, source, false)

	for _, row := range rows {
		result[row.LookupKey] = ipLocationFromCacheRow(row)
	}
	return result, err
}

func splitSyntheticIPKeys(keys []string) (synthetic []string, upstream []string) {
	for _, key := range keys {
		if _, ok := syntheticIPLocation(key); ok {
			synthetic = append(synthetic, key)
			continue
		}
		upstream = append(upstream, key)
	}
	return synthetic, upstream
}

// phoneLocationFromCacheRow 把缓存行还原成手机号归属地。
// 结构化字段从 RawJSON 里解出来，避免为每个字段单独建列。
func phoneLocationFromCacheRow(row entity.LocationCache) PhoneLocation {
	info := parsePhoneInfoRawJSON(row.RawJSON)
	return PhoneLocation{
		Province: info.Province,
		City:     info.City,
		SP:       info.SP,
		AreaCode: info.AreaCode,
		Address:  row.Address,
	}
}

// ipLocationFromCacheRow 把缓存行还原成 IP 归属地。
func ipLocationFromCacheRow(row entity.LocationCache) IPLocation {
	info := parseIPInfoRawJSON(row.RawJSON)
	return IPLocation{
		IP:      info.IP,
		Region:  info.Region,
		ISP:     info.ISP,
		LLC:     info.LLC,
		ASN:     info.ASN,
		Address: row.Address,
	}
}

// parsePhoneInfoRawJSON 从原始响应里解出手机归属地字段；解析失败返回零值。
func parsePhoneInfoRawJSON(raw string) uapi.PhoneInfo {
	info, err := uapi.ParsePhoneInfo([]byte(raw))
	if err != nil {
		return uapi.PhoneInfo{}
	}
	return info
}

// parseIPInfoRawJSON 从原始响应里解出 IP 归属地字段；解析失败返回零值。
func parseIPInfoRawJSON(raw string) uapi.IPInfo {
	info, err := uapi.ParseIPInfo([]byte(raw))
	if err != nil {
		return uapi.IPInfo{}
	}
	return info
}

func missingPhoneKeys(keys []string, cached map[string]entity.LocationCache) []string {
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := cached[locationCacheLookupKey(key)]; ok {
			continue
		}
		missing = append(missing, key)
	}
	return missing
}

func missingIPKeys(keys []string, cached map[string]entity.LocationCache) []string {
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := cached[locationCacheLookupKey(key)]; ok {
			continue
		}
		missing = append(missing, key)
	}
	return missing
}

// queryLocationRows 将二次查缓存、重试与落库放在同一并发合并区间，避免落库前重复付费。
func queryLocationRows(db *gorm.DB, client *uapi.Client, kind string, keys []string, source string, refresh bool) ([]entity.LocationCache, error) {
	rows := make([]entity.LocationCache, 0, len(keys))
	var firstErr error
	normalizedSource, sourceErr := normalizeLocationSource(kind, source)
	if sourceErr != nil {
		return rows, sourceErr
	}
	for _, key := range keys {
		lookupKey := locationCacheLookupKey(key)
		result := locationLookupGroup.do(kind+"|"+normalizedSource+"|"+lookupKey, func() locationFlightResult {
			if !refresh {
				if row, ok := loadLocationCacheRows(db, kind, normalizedSource, []string{lookupKey})[lookupKey]; ok {
					return locationFlightResult{address: row.Address, rawJSON: row.RawJSON, ok: true}
				}
			}
			var fetched locationFlightResult
			for attempt := 1; attempt <= locationLookupMaxAttempts; attempt++ {
				fetched = lookupLocationUpstream(client, kind, lookupKey, source)
				if fetched.ok {
					row := entity.LocationCache{Kind: kind, LookupKey: lookupKey, Source: normalizedSource, Address: fetched.address, RawJSON: fetched.rawJSON}
					fetched.err = storeLocationCacheRows(db, []entity.LocationCache{row})
					return fetched
				}
				logKey := lookupKey
				if kind == entity.LocationKindPhone {
					logKey = maskPhoneForLog(logKey)
				}
				logger.SysLog(fmt.Sprintf("归属地查询失败 kind=%s key=%s attempt=%d/%d error=%v", kind, logKey, attempt, locationLookupMaxAttempts, fetched.err))
			}
			return fetched
		})
		if result.ok {
			rows = append(rows, entity.LocationCache{Kind: kind, LookupKey: lookupKey, Source: normalizedSource, Address: result.address, RawJSON: result.rawJSON})
		}
		if firstErr == nil && result.err != nil {
			firstErr = result.err
		}
		if result.abort {
			logger.SysLog(fmt.Sprintf("归属地查询属于密钥/权限/配额类错误，停止本批剩余查询 kind=%s", kind))
			break
		}
	}
	return rows, firstErr
}

func lookupLocationUpstream(client *uapi.Client, kind string, key string, source string) locationFlightResult {
	if kind == entity.LocationKindIP {
		if synthetic, ok := syntheticIPLocation(key); ok {
			return synthetic
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), locationLookupTimeout)
	defer cancel()
	var address, rawJSON string
	var err error
	if kind == entity.LocationKindPhone {
		var info uapi.PhoneInfo
		info, err = client.GetPhoneInfo(ctx, key)
		address, rawJSON = FormatPhoneLocationAddress(info.Province, info.City, info.SP), info.RawJSON
	} else {
		var info uapi.IPInfo
		info, err = client.GetIPInfo(ctx, key, source)
		address, rawJSON = strings.TrimSpace(info.Region), info.RawJSON
	}
	if err != nil {
		return locationFlightResult{err: err, abort: isLocationAbortError(err)}
	}
	if strings.TrimSpace(address) == "" {
		return locationFlightResult{err: ErrLocationEmptyResponse}
	}
	return locationFlightResult{address: address, rawJSON: rawJSON, ok: true}
}

// compactLocationRows 去掉未填充的占位行。
func compactLocationRows(rows []entity.LocationCache) []entity.LocationCache {
	compacted := make([]entity.LocationCache, 0, len(rows))
	for _, row := range rows {
		if row.LookupKey == "" || strings.TrimSpace(row.Address) == "" {
			continue
		}
		compacted = append(compacted, row)
	}
	return compacted
}

// maskPhoneForLog 只保留手机号后 4 位，避免日志里出现完整号码。
func maskPhoneForLog(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	return "****" + phone[len(phone)-4:]
}
