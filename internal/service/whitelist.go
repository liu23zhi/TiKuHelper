package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 白名单的粒度与匹配方式（task_doc/123：规则模仿黑名单，优先级 白名单 > 黑名单）。
//
// 只保留 ip 一种粒度：中间件能看到的只有客户端 IP，phone / wechat / region
// 在白名单里没有落点（存下来也永不命中，比明确拒绝更糟）。
const (
	// WhitelistKindIP 是唯一支持的粒度。
	WhitelistKindIP = "ip"
)

// 匹配方式：与黑名单同名字面值（管理页与前端可以共用一套 label 映射）。
//
// ip 的 prefix 只是**输入侧**的简化写法，归一化后一律变成 cidr ——
// 判定永远走 net.IPNet，不做字符串前缀比较（与黑名单同一条纪律）。
const (
	WhitelistMatchExact  = "exact"
	WhitelistMatchCIDR   = "cidr"
	WhitelistMatchPrefix = "prefix"
)

// WhitelistExemptIPsSettingKey 是按 IP 白名单的 system_settings 文本域键（T-05）。
//
// 形态照抄 login_rate_limit_exempt_ips：逗号 / 分号 / 换行 / 空格分隔，每项是
// 单个 IP 或 CIDR；解析复用 ParseLoginRateLimitExemptIPs，不新造解析器。
//
// 它与 whitelist_entries 表是**两层白名单**：
//   - whitelist_entries：带原因 / 过期时间 / 启停 / 操作人，可审计的结构化规则；
//   - blacklist_exempt_ips：一次粘贴一整片网段的运维快捷键（对应 45-*.md 里
//     Q-B6「保留白名单，两层」的结论）。
//
// 两层都是「命中即完全不查黑名单」，没有优先级高低之分。
const WhitelistExemptIPsSettingKey = "blacklist_exempt_ips"

// 白名单归一化 / 校验错误（都是 400，不是 500）。
var (
	// ErrInvalidWhitelistKind 类型不是 ip（白名单只支持 IP）。
	ErrInvalidWhitelistKind = errors.New("白名单类型只支持 ip")
	// ErrInvalidWhitelistMatchType 匹配方式不在 exact / cidr / prefix 内。
	ErrInvalidWhitelistMatchType = errors.New("白名单匹配方式必须是 exact / cidr / prefix")
	// ErrInvalidWhitelistPattern 模式为空、超长、或无法解析成 IP / CIDR。
	ErrInvalidWhitelistPattern = errors.New("白名单匹配模式不合法")
	// ErrWhitelistEntryNotFound 规则不存在（404，不是 500）。
	ErrWhitelistEntryNotFound = errors.New("白名单规则不存在")
)

// whitelistPatternMaxRunes 与 whitelist_entries.pattern 的 varchar(191) 对齐。
//
// MySQL 的 varchar(n) 按**字符**计数、SQLite 不强制长度，因此统一按 rune 校验
// 并在两侧都成立（判定与切片的单位必须一致）。
const whitelistPatternMaxRunes = 191

// NormalizedWhitelistValue 是一条白名单规则归一化后的全部落库字段。
type NormalizedWhitelistValue struct {
	Kind        string
	Pattern     string
	MatchType   string
	ValueHash   string
	ValueMasked string
}

// NormalizeWhitelistValue 把运营输入归一化成可入库的规范形式。
//
//	ip + exact / 自动 → net.ParseIP 校验，IPv4-mapped IPv6 收敛成 IPv4 文本
//	ip + cidr/prefix  → net.ParseCIDR / 前缀补零，host 位清零
//
// matchType 为空时自动判定：含 "/" 或段数不足 4（"203." / "203.0.113"）→ cidr，否则 exact。
// 非法输入一律返回 ErrInvalidWhitelistPattern（**保存时拒绝**，见判据②），
// 不做「跳过坏项」的宽容 —— 那只属于读取老库时的行为。
func NormalizeWhitelistValue(kind string, raw string, matchType string) (NormalizedWhitelistValue, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = WhitelistKindIP
	}
	if kind != WhitelistKindIP {
		return NormalizedWhitelistValue{}, ErrInvalidWhitelistKind
	}
	matchType = strings.ToLower(strings.TrimSpace(matchType))

	value := strings.TrimSpace(raw)
	if value == "" {
		return NormalizedWhitelistValue{}, ErrInvalidWhitelistPattern
	}
	if matchType == "" {
		switch {
		case strings.Contains(value, "/"):
			matchType = WhitelistMatchCIDR
		case strings.HasSuffix(value, "."):
			matchType = WhitelistMatchPrefix
		case !strings.Contains(value, ":") && len(strings.Split(value, ".")) < 4:
			matchType = WhitelistMatchPrefix
		default:
			matchType = WhitelistMatchExact
		}
	}

	var pattern string
	switch matchType {
	case WhitelistMatchExact:
		ip := net.ParseIP(value)
		if ip == nil {
			return NormalizedWhitelistValue{}, fmt.Errorf("%w：%q 不是合法 IP", ErrInvalidWhitelistPattern, value)
		}
		// 归一化：IPv4-mapped IPv6（::ffff:203.0.113.7）统一收敛成 IPv4 文本形式，
		// 避免「同一个地址有两种写法、只命中其中一种」（判据③）。
		pattern = ip.String()
	case WhitelistMatchCIDR, WhitelistMatchPrefix:
		network, err := parseWhitelistIPNetwork(value, matchType)
		if err != nil {
			return NormalizedWhitelistValue{}, err
		}
		pattern = network.String()
		matchType = WhitelistMatchCIDR
	default:
		return NormalizedWhitelistValue{}, ErrInvalidWhitelistMatchType
	}

	if utf8.RuneCountInString(pattern) > whitelistPatternMaxRunes {
		return NormalizedWhitelistValue{}, fmt.Errorf("%w：长度超过 %d 字符", ErrInvalidWhitelistPattern, whitelistPatternMaxRunes)
	}
	return NormalizedWhitelistValue{
		Kind:        WhitelistKindIP,
		Pattern:     pattern,
		MatchType:   matchType,
		ValueHash:   WhitelistPatternHashHex(pattern),
		ValueMasked: MaskWhitelistValue(WhitelistKindIP, pattern, matchType),
	}, nil
}

// parseWhitelistIPNetwork 把 CIDR 或前缀写法归一化成 *net.IPNet（host 位清零）。
//
// 前缀写法只支持 IPv4（"203." / "203.0." / "203.0.113."）：IPv6 的文本前缀
// 没有「段」的概念，含混的简写会放行整片地址 —— 白名单是「放行」语义，
// 写错方向的代价比黑名单更大。
func parseWhitelistIPNetwork(value string, matchType string) (*net.IPNet, error) {
	if matchType == WhitelistMatchCIDR || strings.Contains(value, "/") {
		_, network, err := net.ParseCIDR(strings.TrimSpace(value))
		if err != nil || network == nil {
			return nil, fmt.Errorf("%w：%q 不是合法 CIDR", ErrInvalidWhitelistPattern, value)
		}
		return network, nil
	}
	trimmed := strings.TrimSuffix(value, ".")
	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return nil, fmt.Errorf("%w：前缀只支持 1–3 段 IPv4（%q）", ErrInvalidWhitelistPattern, value)
	}
	bits := len(parts) * 8
	padded := make([]string, 0, 4)
	padded = append(padded, parts...)
	for len(padded) < 4 {
		padded = append(padded, "0")
	}
	_, network, err := net.ParseCIDR(strings.Join(padded, ".") + fmt.Sprintf("/%d", bits))
	if err != nil || network == nil {
		return nil, fmt.Errorf("%w：%q 不是合法的 IPv4 前缀", ErrInvalidWhitelistPattern, value)
	}
	return network, nil
}

// WhitelistPatternHashHex 返回 sha256(归一化模式) 的小写十六进制串。
//
// 与黑名单同一个算法（sha256 hex）：唯一索引与查询都走 value_hash，
// 固定宽度（64）让 MySQL 的索引长度可控。
func WhitelistPatternHashHex(pattern string) string {
	sum := sha256.Sum256([]byte(pattern))
	return hex.EncodeToString(sum[:])
}

// MaskWhitelistValue 生成展示用脱敏值。
//
//	cidr → 原样（它本来就是一个网段，不是个人标识，运营要能一眼看出放行范围）
//	exact IPv4 → 203.0.113.*
//	exact IPv6 → 2001:db8:*（只留前两组）
//
// 展示值只用于管理页列表与审计；判定从不使用它。
func MaskWhitelistValue(kind string, pattern string, matchType string) string {
	if kind != WhitelistKindIP {
		return "***"
	}
	if matchType == WhitelistMatchCIDR || strings.Contains(pattern, "/") {
		return pattern
	}
	ip := net.ParseIP(pattern)
	if ip == nil {
		return "***"
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.*", v4[0], v4[1], v4[2])
	}
	groups := strings.Split(ip.String(), ":")
	if len(groups) >= 2 {
		return strings.Join(groups[:2], ":") + ":*"
	}
	return "***"
}

// CreateWhitelistEntryInput 是新建一条白名单规则的入参（运营输入，未归一化）。
type CreateWhitelistEntryInput struct {
	Kind      string
	Pattern   string
	MatchType string
	Reason    string
	Enabled   bool
	ExpiresAt *time.Time
	CreatedBy int32
}

// CreateWhitelistEntryWithDB 是**新建白名单规则的唯一入口**。
//
// 三件事，与 CreateBlacklistEntryWithDB 逐条对齐（缺一不可）：
//
//  1. 归一化 + 校验：非法 IP / CIDR 一律 400（判据②「CIDR 与单 IP 都能解析，
//     非法项拒绝保存」），不会存下一条永不生效的脏规则。
//  2. **绕开 GORM 的 default:true 零值陷阱**：`enabled` 带 `default:true` 标签时
//     `Create(&entry{Enabled:false})` 会被静默写成 true（连 Select("*") 也绕不过），
//     因此拆两步：先按 true 建行，需要 false 时再 `Update("enabled", false)`。
//  3. pattern / value_hash / value_masked 一律由本函数计算，调用方不得自填，
//     否则可能出现「pattern 与 value_hash 对不上」——规则存进去但永远命中不了。
func CreateWhitelistEntryWithDB(db *gorm.DB, input CreateWhitelistEntryInput) (entity.WhitelistEntry, error) {
	if db == nil {
		return entity.WhitelistEntry{}, ErrBlacklistCacheNoDatabase
	}
	normalized, err := NormalizeWhitelistValue(input.Kind, input.Pattern, input.MatchType)
	if err != nil {
		return entity.WhitelistEntry{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		// 与黑名单的 reason 必填同理：日后要能解释「这个网段为什么不必拦」。
		return entity.WhitelistEntry{}, fmt.Errorf("%w：放行原因不能为空", ErrInvalidWhitelistPattern)
	}
	reason = truncateBlacklistRunes(reason, 255)

	entry := entity.WhitelistEntry{
		Kind:        normalized.Kind,
		Pattern:     normalized.Pattern,
		ValueHash:   normalized.ValueHash,
		ValueMasked: normalized.ValueMasked,
		MatchType:   normalized.MatchType,
		Reason:      reason,
		Enabled:     true,
		ExpiresAt:   input.ExpiresAt,
		CreatedBy:   input.CreatedBy,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&entry).Error; err != nil {
		return entity.WhitelistEntry{}, fmt.Errorf("新建白名单规则失败：%w", err)
	}
	if !input.Enabled {
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.WhitelistEntry{}).
			Where("id = ?", entry.ID).Update("enabled", false).Error; err != nil {
			// 建出来了但没停用成功：删掉刚建的行，避免留下一条「本该停用却生效」的规则。
			if delErr := db.Session(&gorm.Session{NewDB: true}).
				Delete(&entity.WhitelistEntry{}, entry.ID).Error; delErr != nil {
				logger.SysWarn(fmt.Sprintf("白名单规则 %d 停用失败且回滚删除也失败，需人工处理：%v", entry.ID, delErr))
			}
			return entity.WhitelistEntry{}, fmt.Errorf("新建白名单规则后置 enabled=false 失败：%w", err)
		}
		entry.Enabled = false
	}
	return entry, nil
}

// WhitelistEntryListQuery 是管理页列表的筛选条件。
//
// 全部字段可选；Enabled 用指针区分「不筛选」与「只看停用」（裸 bool 的 false
// 会退化成「只看停用」，是 GORM 零值陷阱在查询参数上的翻版）。
type WhitelistEntryListQuery struct {
	// Kind 只能取 ip；空 = 全部（当前等价）。
	Kind string
	// Enabled 为 nil = 全部；true = 只在生效；false = 只看已停用。
	Enabled *bool
	// Keyword 同时匹配 ValueMasked 与 Reason（**不匹配 Pattern**，与黑名单同约定）。
	Keyword string
}

// WhitelistEntryListResult 是列表结果（data / total / page / pageSize）。
type WhitelistEntryListResult struct {
	Data     []entity.WhitelistEntry `json:"data"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"pageSize"`
}

// ListWhitelistEntriesWithDB 返回白名单规则列表（分页 + 筛选）。
//
// 排序固定 created_at desc, id desc：运营刚加的规则必须出现在第一行。
// 表不存在（迁移没跑 / 回滚到旧版本）时返回**空列表**而不是错误：
// 管理页显示「暂无规则」比整页 500 更有用，写操作仍会如实报错。
func ListWhitelistEntriesWithDB(db *gorm.DB, query WhitelistEntryListQuery, page int, pageSize int) (WhitelistEntryListResult, error) {
	if db == nil {
		return WhitelistEntryListResult{}, ErrBlacklistCacheNoDatabase
	}
	kind := strings.ToLower(strings.TrimSpace(query.Kind))
	switch kind {
	case "", WhitelistKindIP:
	default:
		return WhitelistEntryListResult{}, ErrInvalidWhitelistKind
	}
	page, pageSize = normalizeBlacklistPage(page, pageSize)
	result := WhitelistEntryListResult{Data: []entity.WhitelistEntry{}, Page: page, PageSize: pageSize}
	if !db.Migrator().HasTable(entity.TableNameWhitelistEntry) {
		return result, nil
	}

	base := db.Session(&gorm.Session{NewDB: true}).Model(&entity.WhitelistEntry{})
	if kind != "" {
		base = base.Where("kind = ?", kind)
	}
	if query.Enabled != nil {
		base = base.Where("enabled = ?", *query.Enabled)
	}
	// 关键词用 INSTR 子串匹配，不用 LIKE：SQLite 下 `LIKE ... ESCAPE` 会静默失效
	// （GORM 把转义符渲染成双引号，而 SQLite 里双引号是标识符），输入 "_" 会匹配全部。
	if condition, args := util.BuildInstrCondition(query.Keyword,
		"COALESCE(value_masked, '')", "COALESCE(reason, '')"); condition != "" {
		base = base.Where(condition, args...)
	}

	if err := base.Session(&gorm.Session{}).Count(&result.Total).Error; err != nil {
		return WhitelistEntryListResult{}, fmt.Errorf("统计白名单规则失败：%w", err)
	}
	if err := base.Session(&gorm.Session{}).
		Order("created_at desc").Order("id desc").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&result.Data).Error; err != nil {
		return WhitelistEntryListResult{}, fmt.Errorf("查询白名单规则失败：%w", err)
	}
	if result.Data == nil {
		result.Data = []entity.WhitelistEntry{}
	}
	return result, nil
}

// UpdateWhitelistEntryEnabledWithDB 启停一条规则。
//
// ⚠️ 必须用 `Update` 而不是 `Save`（default:true 的零值陷阱）：
// `Save(&entity.WhitelistEntry{ID: id, Enabled: false})` 写库结果与调用方意图不一致。
// 行不存在时返回 ErrWhitelistEntryNotFound（404）。
func UpdateWhitelistEntryEnabledWithDB(db *gorm.DB, id int64, enabled bool) (entity.WhitelistEntry, error) {
	if db == nil {
		return entity.WhitelistEntry{}, ErrBlacklistCacheNoDatabase
	}
	if id <= 0 {
		return entity.WhitelistEntry{}, ErrWhitelistEntryNotFound
	}
	if !db.Migrator().HasTable(entity.TableNameWhitelistEntry) {
		return entity.WhitelistEntry{}, ErrWhitelistEntryNotFound
	}
	var entry entity.WhitelistEntry
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Take(&entry).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.WhitelistEntry{}, ErrWhitelistEntryNotFound
		}
		return entity.WhitelistEntry{}, fmt.Errorf("读取白名单规则失败：%w", err)
	}
	if entry.Enabled == enabled {
		// 幂等：已是目标状态时不写库（调用方仍会刷新缓存）。
		return entry, nil
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.WhitelistEntry{}).
		Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return entity.WhitelistEntry{}, fmt.Errorf("更新白名单规则状态失败：%w", err)
	}
	entry.Enabled = enabled
	return entry, nil
}

// DeleteWhitelistEntryWithDB 删除一条规则（硬删除）。
//
// 实体没有 DeletedAt 列，与黑名单一致：物理删除。白名单没有自己的命中日志表，
// 因此不存在「删规则要不要连带删命中」的问题（它从不写命中行）。
func DeleteWhitelistEntryWithDB(db *gorm.DB, id int64) (entity.WhitelistEntry, error) {
	if db == nil {
		return entity.WhitelistEntry{}, ErrBlacklistCacheNoDatabase
	}
	if id <= 0 {
		return entity.WhitelistEntry{}, ErrWhitelistEntryNotFound
	}
	if !db.Migrator().HasTable(entity.TableNameWhitelistEntry) {
		return entity.WhitelistEntry{}, ErrWhitelistEntryNotFound
	}
	var entry entity.WhitelistEntry
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Take(&entry).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.WhitelistEntry{}, ErrWhitelistEntryNotFound
		}
		return entity.WhitelistEntry{}, fmt.Errorf("读取白名单规则失败：%w", err)
	}
	if err := db.Session(&gorm.Session{NewDB: true}).
		Delete(&entity.WhitelistEntry{}, id).Error; err != nil {
		return entity.WhitelistEntry{}, fmt.Errorf("删除白名单规则失败：%w", err)
	}
	return entry, nil
}

// ---------------------------------------------------------------------------
// T-05：blacklist_exempt_ips（system_settings 文本域，形态照抄
// login_rate_limit_exempt_ips）
// ---------------------------------------------------------------------------

// GetWhitelistExemptIPsWithDB 返回 blacklist_exempt_ips 的原始字符串（默认空）。
func GetWhitelistExemptIPsWithDB(db *gorm.DB) string {
	if db == nil || !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return ""
	}
	return strings.TrimSpace(getSystemSettingStringWithDefault(db, WhitelistExemptIPsSettingKey, ""))
}

// ParseWhitelistExemptIPs 把 blacklist_exempt_ips 解析成网段列表。
//
// **不新造解析器**：直接复用登录限流那套（ParseLoginRateLimitExemptIPs）——
// 同一套分隔符（逗号 / 分号 / 换行 / 空格）、同一套「单个 IP 按 /32 或 /128 处理」、
// 同一套「坏项跳过并告警」。两处各写一份必然在某天分叉。
func ParseWhitelistExemptIPs(raw string) []*net.IPNet {
	return ParseLoginRateLimitExemptIPs(raw)
}

// ValidateWhitelistExemptIPs 校验文本里的每一项都可解析（保存时用，**拒绝**坏项）。
//
// 读取时宽容（跳过坏项）、保存时严格（400）：否则运营填错一个网段会以为已经放行，
// 实际没生效 —— 白名单写错的后果是「该放行的仍被拦」，很难从现象反推。
func ValidateWhitelistExemptIPs(raw string) error {
	return ValidateLoginRateLimitExemptIPs(raw)
}

// IsWhitelistExemptIPWithDB 判断某个 IP 是否命中 blacklist_exempt_ips（一次读库）。
//
// 归一化用 net.ParseIP，因此 `::ffff:203.0.113.7` 与 `203.0.113.7` 被同等对待
// （判据③）。中间件走进程内快照，不用这个函数；它服务于一次性判定与测试。
func IsWhitelistExemptIPWithDB(db *gorm.DB, clientIP string) bool {
	ip := parseWhitelistClientIP(clientIP)
	if ip == nil {
		return false
	}
	for _, network := range ParseWhitelistExemptIPs(GetWhitelistExemptIPsWithDB(db)) {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// SaveWhitelistExemptIPsWithDB 保存 blacklist_exempt_ips（先校验，坏项直接拒绝）。
//
// 保留给管理端与测试；系统设置页那条通用写入路径仍可用（见 captain 侧
// system_setting.go 需要补的三行，本文件不越界改它）。
func SaveWhitelistExemptIPsWithDB(db *gorm.DB, raw string) error {
	if db == nil {
		return ErrBlacklistCacheNoDatabase
	}
	trimmed := strings.TrimSpace(raw)
	if err := ValidateWhitelistExemptIPs(trimmed); err != nil {
		return err
	}
	return saveSystemSettingValue(db, WhitelistExemptIPsSettingKey, trimmed,
		"按 IP 白名单（IP/CIDR，逗号或换行分隔；命中即完全不查黑名单）")
}

// parseWhitelistClientIP 归一化客户端 IP 文本。
//
// 空串 / 非法串一律返回 nil（→ 不命中），绝不 panic：
// 中间件拿到的是 c.ClientIP()，它可能是空串（缺 X-Forwarded-For 且 RemoteAddr 异常）。
func parseWhitelistClientIP(clientIP string) net.IP {
	trimmed := strings.TrimSpace(clientIP)
	if trimmed == "" {
		return nil
	}
	return net.ParseIP(trimmed)
}
