package service

import (
	"fmt"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 地区黑名单（BL-4）的四级匹配。
//
// 依据：46-黑名单设计.md §2.4「地区匹配的歧义（必须先解决）」+ 45-*.md §0.4
// 「Q-B3 的匹配规则（含直辖市处理）」。用户拍板的是**多级匹配**：
//
//	级别            依据字段                      例子
//	国家/地区       region 第 1 段                中国 / 美国
//	省              region 第 2 段                广东
//	市              region 第 3 段                中山
//	运营商          llc（优先）或 isp             移动 / GOOGLE
//	某市的某运营商  region 第 3 段 + 运营商        中山 + 移动
//
// # 为什么不做字符串前缀匹配（§2.4 的结论）
//
// 46-*.md §2.4 明确否掉了「用 Address LIKE '云南%'」：那样「云南」既会命中「云南玉溪」
// （正确），也会命中上游返回的任何「云南XXX」脏值（错误），而且无法区分「省」与「以省名开头的地名」。
// 因此这里改成**按段解析 + 结构化比较**，规约如下。
//
// # 规则语法（存进 blacklist_entries.pattern，最长 3 段，空格分隔）
//
//	云南                      → 地区段：国家 / 省 / 市 任一层级等于「云南」即命中（省级规则）
//	云南 玉溪                 → 地区段 + 市：必须命中云南省内的玉溪市（该市所有运营商）
//	云南 玉溪 移动            → 地区段 + 市 + 运营商：精确到「某市的某运营商」
//	云南 * 移动               → 地区段 + 市不限 + 运营商：云南省内的移动
//	*移动                     → 跨地域运营商规则（等价于 `* * 移动`），任何地区的移动都命中
//	美国                      → 单段即国家级（海外 IP 的 region 只有 1 段）
//
// 段值写 `*` 表示该级别不限；**通配符只能整段使用**（「云*南」这种段内通配一律报错，
// 因为它就是 §2.4 要避免的那种字符串前缀匹配）。
//
// # 直辖市
//
// 45-*.md §0.4 规定：region 对直辖市可能是 2 段（`中国 北京`）或 3 段且省市同名
// （`中国 北京 北京`）。解析按段数自适应 —— 2 段时 `city = province`，
// 于是 `中国 北京` 与 `中国 北京 北京` 解析出**完全相同**的结构，
// 规则 `北京` 与 `北京 北京` 对两种写法都命中。
//
// # 判定用哪个归属地
//
// 46-*.md **没有**写死「用请求来源 IP 的归属地还是手机号归属地」。§2.4 只说
// 「地区黑名单要先查归属地缓存」，§7 验收里写的是「地区规则命中时不产生 UApiPro 计费
// （只读缓存，不主动查询）」。本 PR 的取舍是：**用请求来源 IP 的归属地**（门禁在中间件里，
// 那里唯一可得的身份就是 `c.ClientIP()`），并且**只读 `location_cache_entries` 已有行**，
// 未命中就放行 —— 这与「不主动查询、不产生计费」一致。
// ⚠️ 这一条是推断而非文档规定，写在这里以便后续有人拍板不同口径时能一眼找到改动点。
const (
	// blacklistRegionWildcard 是「该级别不限」的段值。
	blacklistRegionWildcard = "*"
	// blacklistRegionMaxSegments 是规则的最大段数（地区 / 城市 / 运营商）。
	blacklistRegionMaxSegments = 3
	// blacklistRegionSegmentMaxRunes 是单个段的最大长度（防止脏值撑爆 pattern varchar(191)）。
	blacklistRegionSegmentMaxRunes = 64
)

// BlacklistRegionPattern 是一条地区规则解析后的结构化条件（空字段 = 该级别不限）。
type BlacklistRegionPattern struct {
	// Geo 是地区段原文（国家 / 省 / 市 任一层级比较）。
	Geo string
	// City 是市级段原文。
	City string
	// Operator 是运营商段原文。
	Operator string
	// Canonical 是归一化后的规范写法（去掉尾部通配段），落库与展示都用它。
	Canonical string
}

// IsWildcard 表示这条规则不限任何级别。
//
// 解析阶段已经拒绝全通配（见 ParseBlacklistRegionPattern），因此正常规则恒为 false。
// 保留这个判据是为了让「加载期来自脏行的规则」也不可能变成全站拉黑。
func (p BlacklistRegionPattern) IsWildcard() bool {
	return p.Geo == "" && p.City == "" && p.Operator == ""
}

// parseBlacklistRegionSegment 校验并归一化单个段值；`*` 归一成空串（= 不限）。
func parseBlacklistRegionSegment(raw string, label string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w：%s段不能为空", ErrInvalidBlacklistPattern, label)
	}
	if trimmed == blacklistRegionWildcard {
		return "", nil
	}
	if strings.Contains(trimmed, blacklistRegionWildcard) {
		return "", fmt.Errorf("%w：%s段 %q 不能在段内使用通配符（通配符只能整段写成 *）",
			ErrInvalidBlacklistPattern, label, trimmed)
	}
	if utf8.RuneCountInString(trimmed) > blacklistRegionSegmentMaxRunes {
		return "", fmt.Errorf("%w：%s段长度超过 %d 字符", ErrInvalidBlacklistPattern, label, blacklistRegionSegmentMaxRunes)
	}
	return trimmed, nil
}

// ParseBlacklistRegionPattern 把运营输入的地区规则解析成结构化条件。
//
// 语法见文件头注释。解析失败一律返回 ErrInvalidBlacklistPattern（400，不是 500）：
// 一条拼错的地区规则如果被「静默存下来但不生效」，运营会以为它已经生效 ——
// 这是最糟糕的失败方式（46-*.md §1.2 同一条原则）。
func ParseBlacklistRegionPattern(raw string) (BlacklistRegionPattern, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return BlacklistRegionPattern{}, fmt.Errorf("%w：地区规则不能为空", ErrInvalidBlacklistPattern)
	}
	// strings.Fields 按 Unicode 空白切分：全角空格（U+3000）也会被切开，
	// 而运营从表格里粘贴过来的往往就是全角空格。
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return BlacklistRegionPattern{}, fmt.Errorf("%w：地区规则不能为空", ErrInvalidBlacklistPattern)
	}
	if len(fields) > blacklistRegionMaxSegments {
		return BlacklistRegionPattern{}, fmt.Errorf("%w：地区规则最多 %d 段（地区 城市 运营商），实际 %d 段",
			ErrInvalidBlacklistPattern, blacklistRegionMaxSegments, len(fields))
	}

	// `*运营商`：跨地域运营商规则的简写，等价于 `* * 运营商`。
	if len(fields) == 1 && strings.HasPrefix(fields[0], blacklistRegionWildcard) &&
		utf8.RuneCountInString(fields[0]) > 1 {
		operator, err := parseBlacklistRegionSegment(strings.TrimPrefix(fields[0], blacklistRegionWildcard), "运营商")
		if err != nil {
			return BlacklistRegionPattern{}, err
		}
		if operator == "" {
			// 例如 "* " 这种只有通配符的写法。
			return BlacklistRegionPattern{}, fmt.Errorf("%w：地区规则不能全是通配符", ErrInvalidBlacklistPattern)
		}
		return BlacklistRegionPattern{
			Operator:  operator,
			Canonical: blacklistRegionWildcard + operator,
		}, nil
	}

	geoRaw := fields[0]
	cityRaw := ""
	operatorRaw := ""
	if len(fields) >= 2 {
		cityRaw = fields[1]
	}
	if len(fields) == blacklistRegionMaxSegments {
		operatorRaw = fields[2]
	}

	geo, err := parseBlacklistRegionSegment(geoRaw, "地区")
	if err != nil {
		return BlacklistRegionPattern{}, err
	}
	var city, operator string
	if cityRaw != "" {
		if city, err = parseBlacklistRegionSegment(cityRaw, "城市"); err != nil {
			return BlacklistRegionPattern{}, err
		}
	}
	if operatorRaw != "" {
		if operator, err = parseBlacklistRegionSegment(operatorRaw, "运营商"); err != nil {
			return BlacklistRegionPattern{}, err
		}
	}

	if geo == "" && city == "" && operator == "" {
		// `*` / `* *` / `* * *`：全站拉黑不该用地区规则表达。
		// 想全拦请用 ip 的 `0.0.0.0/0`（已有且能被 §7 验收直接覆盖），
		// 在这里放行等于给「一个字符打错就把全站 403」留了口子。
		return BlacklistRegionPattern{}, fmt.Errorf("%w：地区规则不能全是通配符（要全站拉黑请用 IP 规则 0.0.0.0/0）",
			ErrInvalidBlacklistPattern)
	}

	return BlacklistRegionPattern{
		Geo:       geo,
		City:      city,
		Operator:  operator,
		Canonical: buildBlacklistRegionCanonical(geo, city, operator),
	}, nil
}

// buildBlacklistRegionCanonical 生成规范写法：去掉尾部通配段，中间的通配保留为 `*`。
//
// 归一化让「云南」与「云南 *」不再成为两条语义相同的规则（唯一索引按 value_hash 判重，
// 不归一化就会并出两条一模一样的规则）。
func buildBlacklistRegionCanonical(geo string, city string, operator string) string {
	geoPart := geo
	if geoPart == "" {
		geoPart = blacklistRegionWildcard
	}
	switch {
	case geo == "" && city == "":
		// 纯运营商规则：`*移动`（与 ParseBlacklistRegionPattern 的简写形式互为逆运算）。
		return blacklistRegionWildcard + operator
	case operator != "":
		cityPart := city
		if cityPart == "" {
			cityPart = blacklistRegionWildcard
		}
		return geoPart + " " + cityPart + " " + operator
	case city != "":
		return geoPart + " " + city
	default:
		return geoPart
	}
}

// BlacklistRegionLocation 是一次归属地解析结果（占位值已归一成空串）。
type BlacklistRegionLocation struct {
	Country  string
	Province string
	City     string
	Operator string
}

// IsZero 表示这次归属地没有任何可用信息（此时任何规则都不该命中）。
func (l BlacklistRegionLocation) IsZero() bool {
	return l.Country == "" && l.Province == "" && l.City == "" && l.Operator == ""
}

// splitBlacklistRegionSegments 按空白切分 region 并剔除占位值。
//
// 剔除占位值（`N/A` / `-` / `未知` 等，见 isPlaceholderLocationValue）：
// 上游对查不到的地址会返回这些占位词，它们不是地理名称，
// 留在段里会把 `中国 N/A 中山` 解析成 country=中国 province=N/A city=中山。
func splitBlacklistRegionSegments(region string) []string {
	raw := strings.Fields(region)
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		if isPlaceholderLocationValue(segment) {
			continue
		}
		segments = append(segments, segment)
	}
	return segments
}

// ParseBlacklistRegionLocation 把 region 串 + 运营商字段解析成结构化归属地。
//
// 段数自适应（45-*.md §0.4）：
//
//	>= 3 段 → country=seg[0] province=seg[1] city=seg[2]
//	== 2 段 → country=seg[0] province=seg[1] city=seg[1]   # 直辖市：市=省
//	== 1 段 → country=seg[0]（海外 IP，如「美国」）
//
// 运营商优先取 llc（中文简称），为空或占位时回退 isp —— 与
// FormatIPLocationAddress 的口径完全一致，避免「展示里有运营商、判定里没有」。
func ParseBlacklistRegionLocation(region string, llc string, isp string) BlacklistRegionLocation {
	segments := splitBlacklistRegionSegments(region)
	location := BlacklistRegionLocation{}
	switch {
	case len(segments) >= 3:
		location.Country = segments[0]
		location.Province = segments[1]
		location.City = segments[2]
	case len(segments) == 2:
		location.Country = segments[0]
		location.Province = segments[1]
		// 直辖市：region 只有「国家 省」两段，省与市同名。
		location.City = segments[1]
	case len(segments) == 1:
		location.Country = segments[0]
	}

	operator := strings.TrimSpace(llc)
	if isPlaceholderLocationValue(operator) {
		operator = strings.TrimSpace(isp)
	}
	if isPlaceholderLocationValue(operator) {
		operator = ""
	}
	location.Operator = operator
	return location
}

// splitBlacklistRegionAddress 把归属地**展示串**拆成「region 串 + 运营商」。
//
// ⚠️ 只用于 MatchRegion(address) 这个便利入口（展示串 / 测试 / 排查）。
// 生产判定路径走 MatchRegionLocation：它直接吃 IPLocation{Region, LLC, ISP}，
// 不依赖展示串的段数推断 —— 手机号的展示串是「云南玉溪 移动」（省市之间没有空格），
// 按本函数拆会得到 country=云南玉溪 / province=移动，必然误判。
//
// 展示串的形态（§23「归属地展示」）：`{region} {运营商}`，region 自身含空格，
// 因此 >= 4 段时前 3 段是 region、第 4 段是运营商；3 段及以下按 region 处理。
func splitBlacklistRegionAddress(address string) (string, string) {
	fields := strings.Fields(address)
	switch {
	case len(fields) >= 4:
		return strings.Join(fields[:3], " "), fields[3]
	case len(fields) == 0:
		return "", ""
	default:
		return strings.Join(fields, " "), ""
	}
}

// MatchRegion 判定一个归属地**展示串**是否命中地区规则。
//
// 它把展示串拆成 region + 运营商后交给 MatchRegionLocation，口径见该函数的注释。
func (s *BlacklistSnapshot) MatchRegion(address string) (BlacklistMatch, bool) {
	if s == nil {
		return BlacklistMatch{}, false
	}
	region, operator := splitBlacklistRegionAddress(address)
	return s.MatchRegionLocation(region, operator, "")
}

// MatchRegionLocation 判定一个结构化归属地是否命中地区规则。
//
// 匹配顺序：按规则加载顺序取第一条命中的（规则条数是个位数到几十条量级，
// 线性扫可接受；与 ipCIDR 的线性扫同一取舍）。
//
// 比较一律大小写不敏感（strings.EqualFold）：国内地名没有大小写问题，
// 但运营商会是 `GOOGLE` / `Google` 这类英文名，区分大小写会让规则时灵时不灵。
func (s *BlacklistSnapshot) MatchRegionLocation(region string, llc string, isp string) (BlacklistMatch, bool) {
	if s == nil || len(s.region) == 0 {
		return BlacklistMatch{}, false
	}
	return s.matchRegionLocation(ParseBlacklistRegionLocation(region, llc, isp))
}

// matchRegionLocation 是已解析归属地后的判定内核（gate 与 MatchRegion 共用）。
func (s *BlacklistSnapshot) matchRegionLocation(location BlacklistRegionLocation) (BlacklistMatch, bool) {
	if s == nil || len(s.region) == 0 {
		return BlacklistMatch{}, false
	}
	// 没有任何可用归属地 → 任何规则都不命中。
	// 这条挡住的是「上游返回 N/A / 空 region」的情况：否则 `*移动` 这类通配规则
	// 会因为在空归属地上一律「通配成立」而误伤所有人。
	if location.IsZero() {
		return BlacklistMatch{}, false
	}
	for i := range s.region {
		rule := s.region[i]
		if rule.Rule == nil {
			continue
		}
		if blacklistRegionRuleMatches(rule, location) {
			return rule.Rule.toMatch(), true
		}
	}
	return BlacklistMatch{}, false
}

// blacklistRegionRuleMatches 判断一条已加载的地区规则是否命中给定归属地。
//
// 三个条件必须同时成立（空条件 = 该级别不限，跳过比较）：
//   - 运营商相等；
//   - 地区段在 国家 / 省 / 市 任一层级相等（单段规则因此同时覆盖国家级与省市级，
//     这正是「美国」与「云南」能用同一种写法表达的原因）；
//   - 市级段相等。
func blacklistRegionRuleMatches(rule BlacklistRegionRule, location BlacklistRegionLocation) bool {
	if rule.Geo == "" && rule.City == "" && rule.Operator == "" {
		// 全通配规则在解析阶段已被拒绝；万一从脏行进来了，也绝不生效。
		return false
	}
	if rule.Operator != "" && !strings.EqualFold(rule.Operator, location.Operator) {
		return false
	}
	if rule.Geo != "" && !blacklistRegionGeoMatches(rule.Geo, location) {
		return false
	}
	if rule.City != "" && !strings.EqualFold(rule.City, location.City) {
		return false
	}
	return true
}

// blacklistRegionGeoMatches 判断地区段是否命中：与 国家 / 省 / 市 任一层级相等即可。
//
// 为什么不区分「这条规则是国家级还是省级」：region 的段数本身就是自适应的
// （直辖市 2 段、海外 1 段），强行按段位绑定会让「北京」「美国」「云南」必须写成
// 三种不同语法。按层级集合比较既简单又不会误伤 —— 参见 §2.4 的反例：
// 真正的误伤来自字符串前缀匹配，而不是来自层级无关的等值比较。
func blacklistRegionGeoMatches(geo string, location BlacklistRegionLocation) bool {
	return strings.EqualFold(geo, location.Country) ||
		strings.EqualFold(geo, location.Province) ||
		strings.EqualFold(geo, location.City)
}

// BlacklistRegionLooker 按客户端 IP 取「已缓存的归属地」。
//
// ok=false 表示没有可用缓存（此时不判定、放行、**不发起上游查询**）。
type BlacklistRegionLooker func(clientIP string) (BlacklistRegionLocation, bool)

// NewBlacklistRegionCacheLooker 返回一个**只读 `location_cache_entries`** 的查表器。
//
// 为什么不用 LookupIPLocationsWithDB：那条路径在缓存缺失时会对回环/私有 IP
// 「本地合成并写库」，在配置了密钥时还会回源外呼 —— 两者都不该出现在**每个请求**的
// 判定链上（46-*.md §4 与 §7：「地区规则命中时不产生 UApiPro 计费（只读缓存，不主动查询）」）。
// 本函数只做一次按主键的索引点读，零外呼、零写入。
//
// ⚠️ 只读默认来源（source 为空串）：商业增强来源（commercial）的记录不参与地区判定，
// 因为运营开启商业来源只是为了让**管理页展示**更完整，不该顺带改变拦截口径。
func NewBlacklistRegionCacheLooker(db *gorm.DB) BlacklistRegionLooker {
	return func(clientIP string) (BlacklistRegionLocation, bool) {
		if db == nil {
			return BlacklistRegionLocation{}, false
		}
		key := normalizeIPLookupKey(strings.TrimSpace(clientIP))
		if key == "" || net.ParseIP(key) == nil {
			return BlacklistRegionLocation{}, false
		}
		rows := loadLocationCacheRows(db, entity.LocationKindIP, "", []string{key})
		row, ok := rows[locationCacheLookupKey(key)]
		if !ok {
			return BlacklistRegionLocation{}, false
		}
		info := parseIPInfoRawJSON(row.RawJSON)
		location := ParseBlacklistRegionLocation(info.Region, info.LLC, info.ISP)
		if location.IsZero() {
			// RawJSON 缺失（更早版本写入的行）时回退 address 列；
			// 与 displayAddressFromCacheRow 同一条兜底原则。
			region, operator := splitBlacklistRegionAddress(row.Address)
			location = ParseBlacklistRegionLocation(region, operator, "")
		}
		if location.IsZero() {
			return BlacklistRegionLocation{}, false
		}
		return location, true
	}
}

// BlacklistRegionOutcome 是地区判定的四种结果。
//
// 与 `(match, hit)` 双返回值并存的原因：中间件需要区分
// 「没命中（正常放行）」与「快照缺失（fail-open，必须留痕）」——
// 两者都返回 hit=false，糊成一个布尔会让 fail-open 留痕彻底消失（46-*.md §3.4）。
type BlacklistRegionOutcome int

const (
	// BlacklistRegionSkipped 没有判定：开关关 / 没有地区规则 / 没有查表器。
	BlacklistRegionSkipped BlacklistRegionOutcome = iota
	// BlacklistRegionMiss 判定过但没命中（含「没有归属地缓存」）。
	BlacklistRegionMiss
	// BlacklistRegionHit 命中。
	BlacklistRegionHit
	// BlacklistRegionFailOpen 开关开但快照缺失：放行 + 必须留痕。
	BlacklistRegionFailOpen
)

// EvaluateBlacklistRegion 是地区判定的唯一入口。
//
// 顺序不可交换（每一条都在保护一条红线）：
//
//  1. **先看开关**：关闭时零查询、零读库、零写日志（46-*.md 红线：开关默认 false → 行为与今天一致）。
//  2. **再看有没有地区规则**：一条都没有时同样零读库 —— 只开了 IP 黑名单的部署
//     不应该为每个请求多付一次归属地缓存读。
//  3. 没有任何归属地缓存 → 不判定、放行、**不主动查询**（§4：未命中不拦截且不主动查询）。
func EvaluateBlacklistRegion(cache *BlacklistCache, looker BlacklistRegionLooker, clientIP string) (BlacklistMatch, BlacklistRegionOutcome) {
	if cache == nil || !cache.Enabled() {
		return BlacklistMatch{}, BlacklistRegionSkipped
	}
	snapshot := cache.Snapshot()
	if snapshot == nil {
		return BlacklistMatch{}, BlacklistRegionFailOpen
	}
	if len(snapshot.region) == 0 || looker == nil {
		return BlacklistMatch{}, BlacklistRegionSkipped
	}
	trimmed := strings.TrimSpace(clientIP)
	if trimmed == "" {
		return BlacklistMatch{}, BlacklistRegionMiss
	}
	location, ok := looker(trimmed)
	if !ok {
		return BlacklistMatch{}, BlacklistRegionMiss
	}
	if match, hit := snapshot.matchRegionLocation(location); hit {
		return match, BlacklistRegionHit
	}
	return BlacklistMatch{}, BlacklistRegionMiss
}

// BlacklistRegionGate 判定请求来源 IP 的归属地是否命中地区黑名单（生产入口）。
//
// 第二个返回值是「是否命中」；需要区分 fail-open 时用 EvaluateBlacklistRegion。
func BlacklistRegionGate(db *gorm.DB, clientIP string) (BlacklistMatch, bool) {
	return BlacklistRegionGateWith(DefaultBlacklistCache(), NewBlacklistRegionCacheLooker(db), clientIP)
}

// BlacklistRegionGateWith 与 BlacklistRegionGate 相同，但显式注入缓存与查表器（测试/装配用）。
func BlacklistRegionGateWith(cache *BlacklistCache, looker BlacklistRegionLooker, clientIP string) (BlacklistMatch, bool) {
	match, outcome := EvaluateBlacklistRegion(cache, looker, clientIP)
	return match, outcome == BlacklistRegionHit
}
