package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// phoneLocationTTL 手机号归属地有效期。
	//
	// 号段归属相对稳定（携号转网会改变运营商，但省市基本不变），
	// 30 天既省积分又不至于明显失真。
	phoneLocationTTL = 30 * 24 * time.Hour
	// ipLocationTTL IP 归属地有效期。IP 段会随运营商调整变动，比手机号保守。
	ipLocationTTL = 7 * 24 * time.Hour
	// locationLookupTimeout 单次归属地查询的上游超时上限。
	locationLookupTimeout = 10 * time.Second
	// locationQueryConcurrency 单次批量查询的最大并发数，避免瞬时打满上游速率限制。
	locationQueryConcurrency = 6
)

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

// normalizeIPLookupKeys 归一化并去重 IP/域名，跳过内网与保留地址。
func normalizeIPLookupKeys(ips []string) []string {
	seen := make(map[string]struct{}, len(ips))
	keys := make([]string, 0, len(ips))
	for _, raw := range ips {
		candidate := normalizeIPLookupKey(raw)
		if candidate == "" || IsPrivateOrReservedIP(candidate) {
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

// locationCacheLookupKey 生成持久化缓存主键的 lookup_key 部分（统一小写）。
func locationCacheLookupKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// loadLocationCacheRows 读取仍在 TTL 内的持久化缓存行，键为 lookup_key。
func loadLocationCacheRows(db *gorm.DB, kind string, lookupKeys []string) map[string]entity.LocationCache {
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
	if err := db.Where("kind = ? AND lookup_key IN ?", kind, normalized).Find(&rows).Error; err != nil {
		// 缓存读失败不影响正确性：回源查询即可。
		logger.SysLog(fmt.Sprintf("读取归属地缓存失败（将回源查询）：%v", err))
		return result
	}

	ttl := locationTTLForKind(kind)
	now := time.Now()
	for _, row := range rows {
		if !row.FetchedAt.IsZero() && now.Sub(row.FetchedAt) > ttl {
			continue
		}
		result[row.LookupKey] = row
	}
	return result
}

func locationTTLForKind(kind string) time.Duration {
	if kind == entity.LocationKindIP {
		return ipLocationTTL
	}
	return phoneLocationTTL
}

// storeLocationCacheRows 写入/刷新持久化缓存。写失败只记日志，不影响本次返回。
func storeLocationCacheRows(db *gorm.DB, rows []entity.LocationCache) {
	if db == nil || len(rows) == 0 {
		return
	}

	now := time.Now()
	for i := range rows {
		rows[i].FetchedAt = now
	}

	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}, {Name: "lookup_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"address", "raw_json", "fetched_at", "updated_at"}),
	}).Create(&rows).Error; err != nil {
		logger.SysLog(fmt.Sprintf("写入归属地缓存失败（不影响本次结果）：%v", err))
	}
}

// LookupPhoneLocationsWithDB 批量查询手机号归属地。
//
// 返回 map 的键是归一化后的手机号。查不到的号码（未配置密钥、格式非法、上游无记录）
// 不会出现在结果里，调用方据此回退为只显示原始号码。
func LookupPhoneLocationsWithDB(db *gorm.DB, phones []string, options LocationQueryOptions) map[string]PhoneLocation {
	result := make(map[string]PhoneLocation)

	normalizedKeys := normalizePhoneLookupKeys(phones)
	if len(normalizedKeys) == 0 {
		return result
	}

	// ① 先读持久化缓存（命中即零积分消耗）。
	cached := loadLocationCacheRows(db, entity.LocationKindPhone, normalizedKeys)
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
	if len(missing) == 0 || apiKey == "" {
		return result
	}

	rows := queryPhoneLocations(uapi.NewClient(apiKey), missing)

	// ③ 落库并并入结果。
	storeLocationCacheRows(db, rows)
	for _, row := range rows {
		result[row.LookupKey] = phoneLocationFromCacheRow(row)
	}
	return result
}

// LookupIPLocationsWithDB 批量查询 IP 归属地。
func LookupIPLocationsWithDB(db *gorm.DB, ips []string, options LocationQueryOptions) map[string]IPLocation {
	result := make(map[string]IPLocation)

	normalizedKeys := normalizeIPLookupKeys(ips)
	if len(normalizedKeys) == 0 {
		return result
	}

	cached := loadLocationCacheRows(db, entity.LocationKindIP, normalizedKeys)
	for _, rawKey := range normalizedKeys {
		row, ok := cached[locationCacheLookupKey(rawKey)]
		if !ok {
			continue
		}
		result[rawKey] = ipLocationFromCacheRow(row)
	}

	missing := missingIPKeys(normalizedKeys, cached)
	apiKey := strings.TrimSpace(options.UAPIKey)
	if len(missing) == 0 || apiKey == "" {
		return result
	}

	rows := queryIPLocations(uapi.NewClient(apiKey), missing, strings.TrimSpace(options.Source))

	storeLocationCacheRows(db, rows)
	for _, row := range rows {
		result[row.LookupKey] = ipLocationFromCacheRow(row)
	}
	return result
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
	if strings.TrimSpace(raw) == "" {
		return uapi.PhoneInfo{}
	}
	var payload struct {
		Province string `json:"province"`
		City     string `json:"city"`
		SP       string `json:"sp"`
		AreaCode string `json:"area_code"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return uapi.PhoneInfo{}
	}
	return uapi.PhoneInfo{
		Province: strings.TrimSpace(payload.Province),
		City:     strings.TrimSpace(payload.City),
		SP:       strings.TrimSpace(payload.SP),
		AreaCode: strings.TrimSpace(payload.AreaCode),
	}
}

// parseIPInfoRawJSON 从原始响应里解出 IP 归属地字段；解析失败返回零值。
func parseIPInfoRawJSON(raw string) uapi.IPInfo {
	if strings.TrimSpace(raw) == "" {
		return uapi.IPInfo{}
	}
	var payload struct {
		IP     string `json:"ip"`
		Region string `json:"region"`
		ISP    string `json:"isp"`
		LLC    string `json:"llc"`
		ASN    string `json:"asn"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return uapi.IPInfo{}
	}
	return uapi.IPInfo{
		IP:     strings.TrimSpace(payload.IP),
		Region: strings.TrimSpace(payload.Region),
		ISP:    strings.TrimSpace(payload.ISP),
		LLC:    strings.TrimSpace(payload.LLC),
		ASN:    strings.TrimSpace(payload.ASN),
	}
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

// queryPhoneLocations 并发查询手机号归属地，返回可落库的行。
//
// 并发而不是串行：一页 20 个号码串行按 10s 超时最坏要等 200s。
// 每个 key 都经过 locationLookupGroup 合并，重复 key 只真正调用一次。
func queryPhoneLocations(client *uapi.Client, phones []string) []entity.LocationCache {
	rows := make([]entity.LocationCache, len(phones))
	var wg sync.WaitGroup

	for i, phone := range phones {
		wg.Add(1)
		go func(index int, lookupPhone string) {
			defer wg.Done()

			result := locationLookupGroup.do(entity.LocationKindPhone+"|"+lookupPhone, func() locationFlightResult {
				ctx, cancel := context.WithTimeout(context.Background(), locationLookupTimeout)
				defer cancel()

				info, err := client.GetPhoneInfo(ctx, lookupPhone)
				if err != nil {
					logger.SysLog(fmt.Sprintf("查询手机号归属地失败 phone=%s error=%v", maskPhoneForLog(lookupPhone), err))
					return locationFlightResult{}
				}
				address := FormatPhoneLocationAddress(info.Province, info.City, info.SP)
				if address == "" {
					return locationFlightResult{}
				}
				return locationFlightResult{address: address, rawJSON: info.RawJSON, ok: true}
			})
			if !result.ok {
				return
			}
			rows[index] = entity.LocationCache{
				Kind:      entity.LocationKindPhone,
				LookupKey: locationCacheLookupKey(lookupPhone),
				Address:   result.address,
				RawJSON:   result.rawJSON,
			}
		}(i, phone)
	}
	wg.Wait()

	return compactLocationRows(rows)
}

// queryIPLocations 并发查询 IP 归属地，返回可落库的行。
func queryIPLocations(client *uapi.Client, ips []string, source string) []entity.LocationCache {
	rows := make([]entity.LocationCache, len(ips))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, locationQueryConcurrency)

	for i, candidate := range ips {
		wg.Add(1)
		go func(index int, lookupIP string) {
			defer wg.Done()

			// 限制并发，避免瞬时打满上游速率限制（429 会浪费一次往返）。
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := locationLookupGroup.do(entity.LocationKindIP+"|"+lookupIP, func() locationFlightResult {
				ctx, cancel := context.WithTimeout(context.Background(), locationLookupTimeout)
				defer cancel()

				info, err := client.GetIPInfo(ctx, lookupIP, source)
				if err != nil {
					logger.SysLog(fmt.Sprintf("查询 IP 归属地失败 ip=%s error=%v", lookupIP, err))
					return locationFlightResult{}
				}
				address := strings.TrimSpace(info.Region)
				if address == "" {
					return locationFlightResult{}
				}
				return locationFlightResult{address: address, rawJSON: info.RawJSON, ok: true}
			})
			if !result.ok {
				return
			}
			rows[index] = entity.LocationCache{
				Kind:      entity.LocationKindIP,
				LookupKey: locationCacheLookupKey(lookupIP),
				Address:   result.address,
				RawJSON:   result.rawJSON,
			}
		}(i, candidate)
	}
	wg.Wait()

	return compactLocationRows(rows)
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
