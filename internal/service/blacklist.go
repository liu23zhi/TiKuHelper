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
	"gorm.io/gorm"
)

// 黑名单的四类粒度（46-黑名单设计.md §1）。
//
// 四类都已接入：ip 在 BL-2（中间件）、phone / wechat 在 BL-3（注册/登录/换绑）、
// region 在 BL-4（结构化三级匹配，见 blacklist_region.go）。
const (
	BlacklistKindIP     = "ip"
	BlacklistKindPhone  = "phone"
	BlacklistKindWechat = "wechat"
	BlacklistKindRegion = "region"
)

// 匹配方式（46-黑名单设计.md §1.1）。
//
// ip 的 prefix 只是**输入侧**的简化写法，归一化后一律变成 cidr ——
// 判定永远走 net.IPNet，不做字符串前缀比较。
const (
	BlacklistMatchExact  = "exact"
	BlacklistMatchCIDR   = "cidr"
	BlacklistMatchPrefix = "prefix"
)

// 命中动作（46-黑名单设计.md §2.2 / §3.4）。
//
// logged_only 是 fail-open 的留痕动作：只记录不拦截。它与 blocked 分列，
// 是为了让运营能一眼分开「真的拦了」和「规则本该拦但当时查不到库」。
const (
	BlacklistActionBlocked    = "blocked"
	BlacklistActionLoggedOnly = "logged_only"
)

// BlacklistEnabledSettingKey 是黑名单总开关，**默认 false**。
//
// 缺表、缺记录、值非法一律按 false 处理：配置读不出来绝不能变成「悄悄开始全站拦人」。
// 开关关闭时中间件零查询、零写日志，全站行为与未引入黑名单时完全一致。
const BlacklistEnabledSettingKey = "blacklist_enabled"

// DefaultBlacklistEnabled 默认关闭（46-黑名单设计.md 红线）。
const DefaultBlacklistEnabled = false

// GetBlacklistEnabledWithDB 返回黑名单总开关，**默认关闭**。
//
// 缺表、缺记录、值非法一律按关闭处理：配置读不出来时绝不能变成「悄悄开始全站拦人」。
// 这与赠金判定的 fail-closed 方向相反（46-*.md §3.4）：
// 发的是钱 → 宁可误拒；拦的是服务 → 查不到就放行。
func GetBlacklistEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, BlacklistEnabledSettingKey)
}

// BlacklistHitAggregateWindow 是命中日志的聚合窗口（46-黑名单设计.md §2.2）。
//
// 同一 entry + 同一客户端 IP 在窗口内只落一行，后续命中累加 hit_count。
// 没有这个窗口，被刷 1000 次就是 1000 行，表会被撑爆。
const BlacklistHitAggregateWindow = 60 * time.Second

// blacklistPatternMaxRunes 与 blacklist_entries.pattern 的 varchar(191) 对齐。
//
// MySQL 的 varchar(n) 按**字符**计数、SQLite 不强制长度，因此这里统一按 rune 校验
// 并在两侧都成立（判定与切片的单位必须一致）。
const blacklistPatternMaxRunes = 191

// 黑名单归一化 / 校验错误（都是 400，不是 500）。
var (
	// ErrInvalidBlacklistKind 类型不在四类粒度内。
	ErrInvalidBlacklistKind = errors.New("黑名单类型必须是 ip / phone / wechat / region")
	// ErrInvalidBlacklistMatchType 匹配方式不在 exact / cidr / prefix 内。
	ErrInvalidBlacklistMatchType = errors.New("黑名单匹配方式必须是 exact / cidr / prefix")
	// ErrInvalidBlacklistPattern 模式为空、超长、或无法解析成对应类型的规范形式。
	ErrInvalidBlacklistPattern = errors.New("黑名单匹配模式不合法")
	// ErrBlacklistRegionNotImplemented 已废弃：BL-4 起地区规则正常入库并参与判定。
	//
	// 保留这个符号只为两件事：① 外部（含尚未同步的调用方/客户端）引用它时不至于编译不过；
	// ② 让「这里曾经拒绝过 region」在代码历史里可查。
	// 新的非法地区模式一律返回 ErrInvalidBlacklistPattern（400），见 ParseBlacklistRegionPattern。
	//
	// Deprecated: 地区规则已接入判定，不再返回这个错误。
	ErrBlacklistRegionNotImplemented = errors.New("地区黑名单（BL-4）已接入判定，该错误不再返回")
)

// NormalizedBlacklistValue 是一条规则归一化后的全部落库字段。
type NormalizedBlacklistValue struct {
	Kind        string
	Pattern     string
	MatchType   string
	ValueHash   string
	ValueMasked string
}

// NormalizeBlacklistValue 按类型把运营输入归一化成可入库的规范形式。
//
// 各类型的归一化依据（46-黑名单设计.md §1.2）：
//
//	ip     → net.ParseIP / net.ParseCIDR 校验并规范化（**不写字符串前缀比较**）
//	phone  → NormalizeMainlandPhoneInput，得到 11 位大陆号码
//	wechat → strings.TrimSpace，**不转小写**（OpenID 大小写敏感）
//	region → ParseBlacklistRegionPattern，得到结构化的「地区 城市 运营商」规范写法
//
// matchType 为空时按类型给默认值：ip 含 "/" 或 prefix 写法 → cidr，否则 exact；
// phone / wechat 恒为 exact；region 恒为 prefix。
func NormalizeBlacklistValue(kind string, raw string, matchType string) (NormalizedBlacklistValue, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	matchType = strings.ToLower(strings.TrimSpace(matchType))
	switch kind {
	case BlacklistKindIP:
		return normalizeBlacklistIP(raw, matchType)
	case BlacklistKindPhone:
		return normalizeBlacklistPhone(raw, matchType)
	case BlacklistKindWechat:
		return normalizeBlacklistWechat(raw, matchType)
	case BlacklistKindRegion:
		// BL-4：地区规则**接受落库**（改自 BL-1/BL-2 的「一口拒绝」）。
		//
		// 为什么现在可以接受：46-*.md §2.4 的地区匹配歧义已按 45-*.md §0.4 的
		// Q-B3 结论解决 —— 不做字符串前缀匹配，改为结构化三级条件
		// （地区 / 城市 / 运营商，见 blacklist_region.go 的 ParseBlacklistRegionPattern）。
		// 当时一口拒绝的理由是「静默存下来但不生效比明确失败更糟」；
		// 现在判定已接入（MatchRegionLocation），落库即生效，那条理由不再成立。
		return normalizeBlacklistRegion(raw, matchType)
	default:
		return NormalizedBlacklistValue{}, ErrInvalidBlacklistKind
	}
}

func normalizeBlacklistIP(raw string, matchType string) (NormalizedBlacklistValue, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return NormalizedBlacklistValue{}, ErrInvalidBlacklistPattern
	}
	if matchType == "" {
		switch {
		case strings.Contains(value, "/"):
			matchType = BlacklistMatchCIDR
		case strings.HasSuffix(value, "."):
			matchType = BlacklistMatchPrefix
			// 兼容省略末尾点的写法（"203.0.113"）：段数不足 4 且各段是纯数字时按前缀处理。
		case !strings.Contains(value, ":") && len(strings.Split(value, ".")) < 4:
			matchType = BlacklistMatchPrefix
		default:
			matchType = BlacklistMatchExact
		}
	}

	var pattern string
	switch matchType {
	case BlacklistMatchExact:
		ip := net.ParseIP(value)
		if ip == nil {
			return NormalizedBlacklistValue{}, fmt.Errorf("%w：%q 不是合法 IP", ErrInvalidBlacklistPattern, value)
		}
		// 规范化：IPv4-mapped IPv6（::ffff:203.0.113.7）统一收敛成 IPv4 文本形式，
		// 避免「同一个地址有两种写法、只命中其中一种」。
		pattern = ip.String()
	case BlacklistMatchCIDR, BlacklistMatchPrefix:
		network, err := parseBlacklistIPNetwork(value, matchType)
		if err != nil {
			return NormalizedBlacklistValue{}, err
		}
		pattern = network.String()
		matchType = BlacklistMatchCIDR
	default:
		return NormalizedBlacklistValue{}, ErrInvalidBlacklistMatchType
	}

	if utf8.RuneCountInString(pattern) > blacklistPatternMaxRunes {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：长度超过 %d 字符", ErrInvalidBlacklistPattern, blacklistPatternMaxRunes)
	}
	return NormalizedBlacklistValue{
		Kind:        BlacklistKindIP,
		Pattern:     pattern,
		MatchType:   matchType,
		ValueHash:   BlacklistPatternHashHex(pattern),
		ValueMasked: MaskBlacklistValue(BlacklistKindIP, pattern, matchType),
	}, nil
}

// parseBlacklistIPNetwork 把 CIDR 或前缀写法归一化成 *net.IPNet（host 位清零）。
//
// 前缀写法只支持 IPv4（"203." / "203.0." / "203.0.113."），因为 IPv6 的文本前缀
// 没有"段"的概念、只有位长概念，含混的简写会误伤整片地址；IPv6 必须显式写 /NN。
func parseBlacklistIPNetwork(value string, matchType string) (*net.IPNet, error) {
	if matchType == BlacklistMatchCIDR || strings.Contains(value, "/") {
		_, network, err := net.ParseCIDR(strings.TrimSpace(value))
		if err != nil || network == nil {
			return nil, fmt.Errorf("%w：%q 不是合法 CIDR", ErrInvalidBlacklistPattern, value)
		}
		return network, nil
	}

	trimmed := strings.TrimSuffix(value, ".")
	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return nil, fmt.Errorf("%w：前缀只支持 1–3 段 IPv4（%q）", ErrInvalidBlacklistPattern, value)
	}
	bits := len(parts) * 8
	padded := make([]string, 0, 4)
	padded = append(padded, parts...)
	for len(padded) < 4 {
		padded = append(padded, "0")
	}
	_, network, err := net.ParseCIDR(strings.Join(padded, ".") + fmt.Sprintf("/%d", bits))
	if err != nil || network == nil {
		return nil, fmt.Errorf("%w：%q 不是合法的 IPv4 前缀", ErrInvalidBlacklistPattern, value)
	}
	return network, nil
}

func normalizeBlacklistPhone(raw string, matchType string) (NormalizedBlacklistValue, error) {
	if matchType != "" && matchType != BlacklistMatchExact {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：手机号只支持 exact", ErrInvalidBlacklistMatchType)
	}
	pattern, err := NormalizeMainlandPhoneInput(raw)
	if err != nil {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：%v", ErrInvalidBlacklistPattern, err)
	}
	return NormalizedBlacklistValue{
		Kind:        BlacklistKindPhone,
		Pattern:     pattern,
		MatchType:   BlacklistMatchExact,
		ValueHash:   BlacklistPatternHashHex(pattern),
		ValueMasked: MaskBlacklistValue(BlacklistKindPhone, pattern, BlacklistMatchExact),
	}, nil
}

func normalizeBlacklistWechat(raw string, matchType string) (NormalizedBlacklistValue, error) {
	if matchType != "" && matchType != BlacklistMatchExact {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：微信只支持 exact", ErrInvalidBlacklistMatchType)
	}
	// 只 TrimSpace：OpenID 大小写敏感，ToLower 会把两个不同账号并成一条规则。
	pattern := strings.TrimSpace(raw)
	if pattern == "" {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：微信 OpenID 不能为空", ErrInvalidBlacklistPattern)
	}
	if utf8.RuneCountInString(pattern) > blacklistPatternMaxRunes {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：长度超过 %d 字符", ErrInvalidBlacklistPattern, blacklistPatternMaxRunes)
	}
	return NormalizedBlacklistValue{
		Kind:        BlacklistKindWechat,
		Pattern:     pattern,
		MatchType:   BlacklistMatchExact,
		ValueHash:   BlacklistPatternHashHex(pattern),
		ValueMasked: MaskBlacklistValue(BlacklistKindWechat, pattern, BlacklistMatchExact),
	}, nil
}

// normalizeBlacklistRegion 归一化地区规则（BL-4）。
//
// 只支持 prefix 一种 match_type（与 46-*.md §2.1 的「region 固定 prefix」一致）：
// 地区规则的本质就是「按层级前缀收窄」，不存在 exact/CIDR 的对应物。
func normalizeBlacklistRegion(raw string, matchType string) (NormalizedBlacklistValue, error) {
	if matchType != "" && matchType != BlacklistMatchPrefix {
		return NormalizedBlacklistValue{}, fmt.Errorf("%w：地区只支持 prefix", ErrInvalidBlacklistMatchType)
	}
	parsed, err := ParseBlacklistRegionPattern(raw)
	if err != nil {
		return NormalizedBlacklistValue{}, err
	}
	return NormalizedBlacklistValue{
		Kind:        BlacklistKindRegion,
		Pattern:     parsed.Canonical,
		MatchType:   BlacklistMatchPrefix,
		ValueHash:   BlacklistPatternHashHex(parsed.Canonical),
		ValueMasked: MaskBlacklistValue(BlacklistKindRegion, parsed.Canonical, BlacklistMatchPrefix),
	}, nil
}

// BlacklistPatternHashHex 返回 sha256(归一化模式) 的小写十六进制串。
//
// 查询与唯一索引都走 value_hash：黑名单是运营数据，比绑定记录更不该被明文扫出来；
// 同时固定宽度（64）让 MySQL 的索引长度可控。
func BlacklistPatternHashHex(pattern string) string {
	sum := sha256.Sum256([]byte(pattern))
	return hex.EncodeToString(sum[:])
}

// BlacklistPatternHashBytes 返回 [32]byte 形式的摘要，供进程内缓存当 map key 用。
//
// [32]byte 是可比较类型，直接当 key 比每次比较字符串更省；与
// BlacklistPatternHashHex 用的是同一个 sha256，两者永远一致。
func BlacklistPatternHashBytes(pattern string) [32]byte {
	return sha256.Sum256([]byte(pattern))
}

// MaskBlacklistValue 生成展示用的脱敏值（46-黑名单设计.md §2.1）。
//
//	cidr → 原样（它本来就是一个网段，不是个人标识）
//	exact IPv4 → 203.0.113.*
//	exact IPv6 → 2001:db8:*（只留前两组）
//	phone → 138****5678
//	wechat → o***ABC
//	region → 云南*
//
// 展示值只用于「命中记录」与「管理页列表」；判定从不使用它。
func MaskBlacklistValue(kind string, pattern string, matchType string) string {
	switch kind {
	case BlacklistKindIP:
		if matchType == BlacklistMatchCIDR || strings.Contains(pattern, "/") {
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
	case BlacklistKindPhone:
		runes := []rune(pattern)
		if len(runes) != 11 {
			return "***"
		}
		return string(runes[:3]) + "****" + string(runes[7:])
	case BlacklistKindWechat:
		runes := []rune(pattern)
		switch {
		case len(runes) > 4:
			return string(runes[:1]) + "***" + string(runes[len(runes)-3:])
		case len(runes) > 0:
			return string(runes[:1]) + "***"
		default:
			return "***"
		}
	case BlacklistKindRegion:
		// 地区规则不是个人标识，不需要像手机号那样遮中间；保留原样的可读性
		// 对运营更有用（他要能一眼看出是「云南」还是「云南 玉溪 移动」）。
		//
		// 末尾补 `*` 表示「前缀收窄」，但两种已有写法不再重复补：
		//   - 已以 `*` 结尾的（`云南 *`）；
		//   - 纯运营商规则（`*移动`），它本身就以通配开头，再补一个会让展示变噪音。
		if pattern == "" || strings.HasSuffix(pattern, "*") || strings.HasPrefix(pattern, "*") {
			return pattern
		}
		return pattern + "*"
	default:
		return "***"
	}
}

// CreateBlacklistEntryInput 是新建一条黑名单规则的入参（运营输入，未归一化）。
type CreateBlacklistEntryInput struct {
	Kind      string
	Pattern   string
	MatchType string
	Reason    string
	Enabled   bool
	ExpiresAt *time.Time
	CreatedBy int32
}

// CreateBlacklistEntryWithDB 是**新建黑名单规则的唯一入口**。
//
// 它做三件事，缺一不可：
//
//  1. 归一化 + 校验（NormalizeBlacklistValue）：拒绝非法 IP/CIDR/手机号/地区模式。
//     运营填错不会变成一条永不生效的脏规则。
//  2. **绕开 GORM 的 default:true 零值陷阱**。`enabled` 列带 `default:true` 标签，
//     GORM 在 ConvertToCreateValues 阶段会把**等于默认值的字段从 INSERT 省略**，
//     于是 `db.Create(&entry{Enabled: false})` 会被静默写成 true ——
//     连 `Select("*")` 也绕不过（已实测）。
//     本函数因此拆成两步：先按 Enabled=true 建行，若入参要求 false 再
//     `Update("enabled", false)`。这样「管理页点了停用」不会变成「显示已停用但仍在拦人」。
//  3. 归一化后的 pattern / value_hash / value_masked 一律由本函数计算，
//     调用方**不允许**自己填这三个字段 —— 否则可能出现「pattern 与 value_hash 对不上」，
//     规则存进去但永远命中不了。
//
// ⚠️ BL-5（管理页）与 BL-6（导入）必须只调这个函数，不要直接
// `db.Create(&entity.BlacklistEntry{...})`：注释只能防读到注释的人，封装能防所有调用方。
//
// 返回落库后的实体（含 ID 与最终的 Enabled 值），调用方可以据此回显。
func CreateBlacklistEntryWithDB(db *gorm.DB, input CreateBlacklistEntryInput) (entity.BlacklistEntry, error) {
	if db == nil {
		return entity.BlacklistEntry{}, ErrBlacklistCacheNoDatabase
	}
	normalized, err := NormalizeBlacklistValue(input.Kind, input.Pattern, input.MatchType)
	if err != nil {
		return entity.BlacklistEntry{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		// 46-*.md §2.1：拉黑原因必填，运营要能解释。
		return entity.BlacklistEntry{}, fmt.Errorf("%w：拉黑原因不能为空", ErrInvalidBlacklistPattern)
	}
	reason = truncateBlacklistRunes(reason, 255)

	entry := entity.BlacklistEntry{
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
	if err := db.Create(&entry).Error; err != nil {
		return entity.BlacklistEntry{}, fmt.Errorf("新建黑名单规则失败：%w", err)
	}
	if !input.Enabled {
		// 第二步：把 enabled 落成 false（见函数注释第 2 条）。
		if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).
			Update("enabled", false).Error; err != nil {
			// 建出来了但没停用成功：删掉刚建的行，避免留下一条「本该停用却生效」的规则。
			if delErr := db.Delete(&entity.BlacklistEntry{}, entry.ID).Error; delErr != nil {
				logger.SysWarn(fmt.Sprintf("黑名单规则 %d 停用失败且回滚删除也失败，需人工处理：%v", entry.ID, delErr))
			}
			return entity.BlacklistEntry{}, fmt.Errorf("新建黑名单规则后置 enabled=false 失败：%w", err)
		}
		entry.Enabled = false
	}
	return entry, nil
}
