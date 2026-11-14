package service

// 脚本公开入口的通配匹配（45-*.md §0.25.2 / §0.25.3，72-*.md §3.2）。
//
// 三件事都在这个文件里：
//  1. 域名形态匹配：精确 / 最左通配（`*.a.zelly.cn`）/ 普通域名；
//  2. 路径**段级 glob**：`*` 匹配一段、`**` 匹配多段（Q72-4 明确允许 `*` 出现在路径中间）；
//  3. 保留前缀（`/adapter-service` `/sqp` `/assets` …）：运行期从 gin 路由表自动枚举，
//     命中即拒绝，且**优先于一切用户配置**（包括 `**`），否则 `a.com/a/*` 会吞掉整站 API。

import (
	"errors"
	"path"
	"sort"
	"strings"
	"sync"

	"gorm.io/gorm"
)

var (
	// ErrScriptDomainWildcardPlacement 域名里的 `*` 不在最左侧（如 `a.*.a.com`）。
	// Q72-3 定案：域名通配只允许 `*.a.zelly.cn` 这种最左形态。
	ErrScriptDomainWildcardPlacement = errors.New("脚本域名中的 * 只允许出现在最左侧（例如 *.a.zelly.cn），不允许 a.*.a.com")
	// ErrScriptDomainWildcardOnScript 脚本自身的域名必须是具体主机名。
	// 通配符属于「管理员允许的域名列表」，不属于单个脚本实例。
	ErrScriptDomainWildcardOnScript = errors.New("脚本自身的域名不能包含 *，通配符请写在管理员允许的域名列表里")
	// ErrScriptPathPatternInvalid 路径通配模式非法。
	ErrScriptPathPatternInvalid = errors.New("脚本路径通配规则非法：路径必须以 / 开头，* 与 ** 只能各自作为独立一段")
	// ErrScriptPathTraversal 请求路径含 `..` 段。
	ErrScriptPathTraversal = errors.New("脚本请求路径不允许包含 .. 段")
	// ErrScriptReservedPrefix 规则命中了系统保留路径。
	ErrScriptReservedPrefix = errors.New("脚本公开路径与系统保留路径冲突")
)

// ─────────────────────────── 保留前缀 ───────────────────────────

// baselineReservedScriptPrefixes 是兜底清单：gin 自带路径、前端静态资源目录、
// 以及本仓固定入口。运行期 `internal/api/router.go` 的 `SetAPIRouter` 末尾还会把
// 真实路由表里出现的第一段并进来（见 SetReservedScriptPrefixes）。
//
// 为什么要有兜底：枚举只覆盖「已经注册过的路由」。静态资源目录由 SetWebRouter 的
// static.Serve 注册，而 SetAPIRouter 可能先于它执行；gin 的 /health /metrics 在本仓
// 目前也不存在。兜底让这三类路径在任何调用顺序下都保持保留状态。
var baselineReservedScriptPrefixes = []string{
	"adapter-service",
	"sqp",
	"api",
	"assets",
	"static",
	"dist",
	"favicon.ico",
	"health",
	"metrics",
}

var (
	scriptReservedPrefixMu   sync.RWMutex
	scriptReservedPrefixSet  = buildReservedScriptPrefixes(nil)
	scriptReservedPrefixList = baselineReservedScriptPrefixes
)

func buildReservedScriptPrefixes(extra []string) map[string]bool {
	set := make(map[string]bool, len(baselineReservedScriptPrefixes)+len(extra))
	for _, prefix := range baselineReservedScriptPrefixes {
		set[prefix] = true
	}
	for _, prefix := range extra {
		if normalized := normalizeScriptPrefixSegment(prefix); normalized != "" {
			set[normalized] = true
		}
	}
	return set
}

// SetReservedScriptPrefixes 把启动时从 gin 路由表枚举出的第一段路径**合并**进保留前缀表。
//
// 只合并不替换：路由枚举是「启动时快照」，一旦某条路由在 SetWebRouter 里注册
// （静态资源），SetAPIRouter 的枚举就看不到它 —— 用合并语义才不会把兜底清单弄丢。
func SetReservedScriptPrefixes(prefixes []string) {
	next := buildReservedScriptPrefixes(prefixes)
	merged := make([]string, 0, len(next))
	for prefix := range next {
		merged = append(merged, prefix)
	}
	sort.Strings(merged)
	scriptReservedPrefixMu.Lock()
	scriptReservedPrefixSet = next
	scriptReservedPrefixList = merged
	scriptReservedPrefixMu.Unlock()
}

// ScriptReservedPrefixes 返回当前保留前缀的快照（升序），只用于诊断与测试。
func ScriptReservedPrefixes() []string {
	scriptReservedPrefixMu.RLock()
	defer scriptReservedPrefixMu.RUnlock()
	clone := make([]string, len(scriptReservedPrefixList))
	copy(clone, scriptReservedPrefixList)
	return clone
}

// scriptReservedPrefix 判断一个路径段是否属于系统保留前缀。
func scriptReservedPrefix(segment string) bool {
	normalized := normalizeScriptPrefixSegment(segment)
	if normalized == "" {
		return false
	}
	scriptReservedPrefixMu.RLock()
	defer scriptReservedPrefixMu.RUnlock()
	return scriptReservedPrefixSet[normalized]
}

// normalizeScriptPrefixSegment 归一化单个路径段；多段/空串一律判为「不是保留前缀」
// （宁可漏判一个非法输入，也不要让 `a/b` 这种值把两个前缀一起拉黑）。
func normalizeScriptPrefixSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "/")
	if value == "" || strings.Contains(value, "/") {
		return ""
	}
	return value
}

// ─────────────────────────── 请求路径规范化 ───────────────────────────

// cleanScriptRequestPath 把请求路径规范化成段级匹配可用的形态。
//
//   - `""` 与 `"/"` 都返回 `""`：语义是「只按 host 匹配」（泛域名 shortCode 模式）；
//   - **含 `..` 段直接报错**，而不是交给 path.Clean 抹平 —— Clean 会把
//     `/a/../adapter-service/x` 变成 `/adapter-service/x`，看起来更安全，
//     但那时我们已经无法区分「用户显式写了穿越」和「正常路径」，保留报错更可审计；
//   - 其余路径走 path.Clean 后再匹配（`/a/b/` → `/a/b`，`//a//b` → `/a/b`）。
func cleanScriptRequestPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if strings.ContainsAny(trimmed, "\\\x00\r\n") || !strings.HasPrefix(trimmed, "/") {
		return "", ErrScriptPathPatternInvalid
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", ErrScriptPathTraversal
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == "/" {
		return "", nil
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned, nil
}

// scriptPathSegments 按 `/` 切段并丢掉空段。
func scriptPathSegments(cleanPath string) []string {
	if cleanPath == "" {
		return nil
	}
	parts := strings.Split(cleanPath, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

// scriptFirstPathSegment 返回规范化路径的第一段；根路径返回空串。
func scriptFirstPathSegment(cleanPath string) string {
	segments := scriptPathSegments(cleanPath)
	if len(segments) == 0 {
		return ""
	}
	return segments[0]
}

// ─────────────────────────── 段级 glob ───────────────────────────

// scriptPathGlobMatch 用**段级 glob** 把一条配置路径模式和一个请求路径做比对。
//
// 精确语义（45-*.md §0.25.3 定案，也是本函数的契约）：
//
//   - 匹配**任意单段**（段内不含 `/`）
//     **  匹配**任意多段**（可以是零段）
//
//     /a/*/c    匹配 /a/b/c       ，不匹配 /a/b/x/c      （* = 一段）
//     /a/**/c   匹配 /a/b/c 与 /a/b/x/c                 （** = 多段）
//
// 两侧都先规范化：pattern 丢掉空段，requestPath 走 cleanScriptRequestPath。
// 任何一侧含 `..` 段 → 返回**错误**（不是 false），避免调用方把路径穿越当成
// 「普通不匹配」继续往下匹配下一条规则。
func scriptPathGlobMatch(pattern, requestPath string) (bool, error) {
	patternSegments, err := scriptPathPatternSegments(pattern)
	if err != nil {
		return false, err
	}
	cleaned, err := cleanScriptRequestPath(requestPath)
	if err != nil {
		return false, err
	}
	return scriptPathSegmentsMatch(patternSegments, scriptPathSegments(cleaned)), nil
}

// scriptPathPatternSegments 解析并校验路径模式。
func scriptPathPatternSegments(pattern string) ([]string, error) {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" || strings.ContainsAny(trimmed, "\\\x00\r\n") || !strings.HasPrefix(trimmed, "/") {
		return nil, ErrScriptPathPatternInvalid
	}
	segments := make([]string, 0, 8)
	for _, part := range strings.Split(trimmed, "/") {
		switch {
		case part == "":
			continue
		case part == "." || part == "..":
			return nil, ErrScriptPathPatternInvalid
		case part == "*" || part == "**":
			segments = append(segments, part)
		case strings.Contains(part, "*"):
			// 段内混写（`a*b`）既不是「一段」也不是「多段」，语义模糊 → 拒绝。
			return nil, ErrScriptPathPatternInvalid
		default:
			segments = append(segments, part)
		}
	}
	if len(segments) == 0 {
		return nil, ErrScriptPathPatternInvalid
	}
	return segments, nil
}

func scriptPathSegmentsMatch(patternSegments, requestSegments []string) bool {
	if len(patternSegments) == 0 {
		return len(requestSegments) == 0
	}
	head := patternSegments[0]
	if head == "**" {
		// 多段通配：吞 0..N 段后继续比对剩余模式。
		for consumed := 0; consumed <= len(requestSegments); consumed++ {
			if scriptPathSegmentsMatch(patternSegments[1:], requestSegments[consumed:]) {
				return true
			}
		}
		return false
	}
	if len(requestSegments) == 0 {
		return false
	}
	if head != "*" && head != requestSegments[0] {
		return false
	}
	return scriptPathSegmentsMatch(patternSegments[1:], requestSegments[1:])
}

// ─────────────────────────── 域名规则（新语法） ───────────────────────────

// scriptDomainRulesWithDB 读取管理员配置的域名格式列表。
//
// 语法重写后这里只返回**新语法**模板（`*.a.com` / `a.com/*`）；
// 存量旧格式条目在 ScriptDomainFormatsWithDB 里已被丢弃（GET 不 500），
// 因此公开入口对它们的表现是「这条规则不存在」，而不是「整站不可用」。
func scriptDomainRulesWithDB(db *gorm.DB) ([]ScriptDomainFormat, error) {
	return ScriptDomainFormatsWithDB(db)
}

// authorizeScriptPublicPath 判断「这条脚本能不能在 (baseDomain, cleanPath) 上被公开访问」。
//
// 语义（新语法）：
//   - 管理员没配任何格式 → 保持旧的「不限制」行为，但**只放行单段路径**：
//     多段公开路径必须由管理员显式配置 `a.com/*` 才开放（沿用上一版的安全取向）。
//   - 配了格式 → 任意一条覆盖 (host, path) 即放行：
//     · `*.a.com` 只看 host（任意深度子域），不约束 path；
//     · `a.com/*` 要求 host 全等且 path 至少一段。
func authorizeScriptPublicPath(formats []ScriptDomainFormat, baseDomain, cleanPath string) bool {
	if len(formats) == 0 {
		return len(scriptPathSegments(cleanPath)) <= 1
	}
	for _, format := range formats {
		if scriptDomainTemplateMatches(format, baseDomain, cleanPath) {
			return true
		}
	}
	return false
}

// ensureScriptDomainRulePrefixAvailable 在**创建/更新脚本时**确认域名字典可读。
//
// 新语法下 `a.com/*` 天然覆盖 `/api/...` 这类保留路径 —— 那是保留前缀闸门的职责
// （ResolvePublicScriptWithDB 第 2 步在任何通配匹配之前挡掉），
// 不是配置期能消除的。因此这里只需保证「读配置失败时不要放行」：
// 读取出错必须把错误抛上去，绝不能当成「没配置任何规则」。
func ensureScriptDomainRulePrefixAvailable(db *gorm.DB, baseDomain string) error {
	_, err := scriptDomainRulesWithDB(db)
	return err
}

// validateScriptBaseDomain 校验「脚本自身」的域名。
//
// 新语法下脚本自身的域名必须是**具体主机名**（`x.a.com` 或 `a.com`，不含 `*`）：
// 通配符属于管理员的「可用域名格式列表」，不属于单个脚本实例。
// 这一条在创建/更新脚本时拦住，而不是等匹配阶段悄悄不生效。
func validateScriptBaseDomain(domain string) error {
	if !strings.Contains(domain, "*") {
		return nil
	}
	return ErrScriptDomainWildcardOnScript
}
