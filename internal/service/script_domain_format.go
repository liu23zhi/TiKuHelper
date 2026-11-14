package service

// 脚本可用域名的**新语法**（2026-11-14 用户裁决，破坏性变更）。
//
// 只有两种形态，别的全部是非法：
//
//	*.a.com    泛域名：任意子域（至少一级），**不包含** a.com 本身
//	a.com/*    路径通配：a.com 下的任意非空路径（可多段）
//
// 为什么把边界定成这样（这三条都是「不做兼容」之后必须明确表态的地方）：
//
//  1. `*.a.com` **不匹配** `a.com`。这是通配证书/通配 DNS 的标准语义，
//     也是本仓既有实现 `scriptDomainHostMatch` 的带点后缀语义（`.a.com` 比
//     host 短才命中）。如果放行 `a.com` 本身，那么任何一个买了 `a.com`
//     的人都能用 `*.a.com` 规则把裸域也纳进来 —— 与「泛域名」的字面意思不符。
//  2. `a.com/*` **匹配多段路径**（`/p/q` 命中）。用户明确给的例子就是
//     `a.com/*` + 输入 `p/q` → `a.com/p/q`。旧的段级 glob（`*` 恰好一段、
//     `**` 多段）在这套语法里不再存在。
//  3. `a.com/*` **不匹配** `a.com`（没有路径）。`/*` 要求至少一段。
//     否则 `a.com` 会被路径规则和泛域名规则同时覆盖，管理员无法表达
//     「只开放某个路径」。
//
// 旧格式（裸 `a.com`、`a.com/a/*`、`a.com/**`、正则……）**一律拒绝**，
// 且是**显式报错**而不是静默失效：静默失效会让管理员以为规则还在生效。

import (
	"errors"
	"strings"
)

const (
	// ScriptDomainFormatWildcard 是 `*.a.com` 形态。
	ScriptDomainFormatWildcard = "wildcard"
	// ScriptDomainFormatPath 是 `a.com/*` 形态。
	ScriptDomainFormatPath = "path"
)

var (
	// ErrScriptDomainFormatLegacy 旧格式（不做兼容）。错误文案必须直接告诉管理员
	// 该改成什么，否则他只会看到「保存失败」而无从下手。
	ErrScriptDomainFormatLegacy = errors.New("脚本域名语法已变更：旧格式不再支持，请改用 *.a.com（泛域名）或 a.com/*（路径通配）")
	// ErrScriptDomainFormatInvalid 新语法形态不合法。
	ErrScriptDomainFormatInvalid = errors.New("脚本域名格式不合法：只支持 *.a.com（泛域名）与 a.com/*（路径通配）两种写法")
	// ErrScriptDomainSlotInvalid 用户填的占位内容不合法。
	ErrScriptDomainSlotInvalid = errors.New("域名占位内容不合法：子域只能含字母、数字与连字符；路径不能含 *、空格、反斜杠，也不能有 . 或 .. 段")
)

// ScriptDomainFormat 是一条**管理员配置的域名格式模板**。
//
// Slot 位置由 Kind 决定，调用方据此渲染占位输入框：
//
//	wildcard → 「[输入框].a.com」
//	path     → 「a.com/[输入框]」
type ScriptDomainFormat struct {
	// Raw 是原样（归一化后）的模板文本，也是落库形态。
	Raw string
	// Host 是模板里的固定主机部分（不含 `*`）。
	Host string
	// Kind 取值 ScriptDomainFormatWildcard / ScriptDomainFormatPath。
	Kind string
}

// String 返回落库形态。
func (f ScriptDomainFormat) String() string { return f.Raw }

// SlotIsPrefix 表示占位输入框在主机**前面**（`*.a.com`）。
func (f ScriptDomainFormat) SlotIsPrefix() bool { return f.Kind == ScriptDomainFormatWildcard }

// parseScriptDomainFormat 解析一条模板。空串返回 ok=false（调用方决定是跳过还是报错）。
func parseScriptDomainFormat(raw string) (ScriptDomainFormat, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ScriptDomainFormat{}, false, nil
	}
	// scheme 一律不接受：新语法是域名模板，不是 URL。带 `://` 的一律按旧格式拒。
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, " \t\r\n\\'\"?#") {
		return ScriptDomainFormat{}, true, ErrScriptDomainFormatLegacy
	}
	lowered := strings.ToLower(trimmed)
	// 先判「有没有 `/`」：带路径却**不是** `a.com/*` 形态的，都是上一版的段级 glob
	// （`a.com/a/*`、`a.com/a/**`、`a.com/api/*`）。它们属于旧语法 ⇒ Legacy，
	// 而不是「新语法写错了」—— 管理员是照旧文档写的，错误文案要指向新写法。
	if slash := strings.IndexByte(lowered, '/'); slash >= 0 {
		host := lowered[:slash]
		pathPart := lowered[slash:]
		if pathPart == "/*" && !strings.Contains(host, "*") {
			if !validScriptDomainHost(host) {
				return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
			}
			return ScriptDomainFormat{Raw: host + "/*", Host: host, Kind: ScriptDomainFormatPath}, true, nil
		}
		// ⚠️ 这里要区分「旧格式」与「新语法写错」——两者给管理员的指引完全相反：
		//   旧格式  → 他照旧文档写的，要告诉他「请改用 *.a.com 或 a.com/*」
		//   写错新格式 → 他已经知道新语法，再让他「改用新语法」等于没说
		//
		// 判据：路径段里**出现了 `*`** 说明他在尝试新语法（只是位置/形态不对，
		// 如 `a.com/*/x`、`a.com/x/y*`）；`*` 只出现在中间或末尾都不合法 ⇒ Invalid。
		// 不带 `*` 的多段路径（`a.com/a/*`、`a.com/a/**`）是上一版的段级 glob ⇒ Legacy。
		// 判据要同时满足两组既有用例：
		//   `a.com/a/*`、`a.com/**`、`a.com/a/**/x` → Legacy（旧段级 glob，管理员照旧文档写的）
		//   `a.com/*/x`                          → Invalid（新语法写错位置，已在用新写法）
		//
		// 区分点不是「路径里有没有 *」，而是**`*` 是否单独占路径的最后一个整段**：
		//   - `/*`           → 合法（上面已返回）
		//   - `/*/x`         → `*` 是独立段但后面还有段 ⇒ 新语法写错 ⇒ Invalid
		//   - `/a/*`、`/**`  → `*` 不独占首段（前面还有固定段，或整段是 `**`）⇒ 旧格式 ⇒ Legacy
		segments := strings.Split(strings.TrimPrefix(pathPart, "/"), "/")
		if len(segments) > 0 && segments[0] == "*" {
			return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
		}
		return ScriptDomainFormat{}, true, ErrScriptDomainFormatLegacy
	}
	// 必须先判「`*` 是否恰好出现一次」，否则 `a.*.a.com`、`**.a.com` 会走进下面的分支。
	if strings.Count(lowered, "*") != 1 {
		if strings.Contains(lowered, "*") {
			return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
		}
		// 完全没有 `*` ⇒ 旧格式的裸域名。
		return ScriptDomainFormat{}, true, ErrScriptDomainFormatLegacy
	}
	if strings.HasPrefix(lowered, "*.") {
		host := strings.TrimPrefix(lowered, "*.")
		if !validScriptDomainHost(host) {
			return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
		}
		return ScriptDomainFormat{Raw: "*." + host, Host: host, Kind: ScriptDomainFormatWildcard}, true, nil
	}
	// 有 `*` 但既不是 `*.` 前缀也不是 `/*` 后缀：`a.*.a.com`、`*a.com`。
	return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
}

// validScriptDomainHost 校验固定主机部分。
//
// 至少两级（`a.com`）：单标签主机（`localhost`、`intranet`）无法与泛域名
// `*.` 前缀区分，也无法表达「子域」这个概念。这条同时挡住 `.a.com`、`a..com`、
// `a.com.` 之类的畸形输入。
func validScriptDomainHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			char := label[i]
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
	}
	// 顶级域必须是纯字母且至少两位（挡住 `a.1` 与 `a.c`）。
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return false
	}
	for i := 0; i < len(tld); i++ {
		if tld[i] < 'a' || tld[i] > 'z' {
			return false
		}
	}
	return true
}

// scriptDomainTemplateMatches 判断一条模板是否覆盖给定的「主机 + 路径」。
//
// host 应为**具体主机名**（已小写、无端口），cleanPath 已由 cleanScriptRequestPath 规范化。
func scriptDomainTemplateMatches(format ScriptDomainFormat, host, cleanPath string) bool {
	host = strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	switch format.Kind {
	case ScriptDomainFormatWildcard:
		suffix := "." + format.Host
		// 必须**长于**后缀：`a.com` 比 `.a.com` 短 ⇒ 不匹配（见文件头第 1 条）。
		return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
	case ScriptDomainFormatPath:
		if !strings.EqualFold(host, format.Host) {
			return false
		}
		// `/*` 要求至少一段；根路径不落在路径规则上（见文件头第 3 条）。
		return len(scriptPathSegments(cleanPath)) > 0
	default:
		return false
	}
}

// normalizeScriptDomainSlot 归一化用户填进占位框的内容。
//
// 泛域名槽位：点分标签，每段与主机标签同规则（不能以 - 开头/结尾、只能 [a-z0-9-]）。
// 允许填多级（`x.y` ⇒ `x.y.a.com`），因为 `*.a.com` 本来就覆盖任意深度的子域。
//
// 路径槽位：`/` 分段，每段非空、不能是 `.`/`..`、不能含 `*`、空格、反斜杠、
// `?`、`#`（这些要么是穿越，要么会被 HTTP 层切走，拼进 URL 只会得到 404）。
func normalizeScriptDomainSlot(kind, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", ErrScriptDomainSlotInvalid
	}
	switch kind {
	case ScriptDomainFormatWildcard:
		// ⚠️ 不能先 Trim(".") 再校验：那会把 `.x`、`x.` 悄悄变成合法值，
		// 用户输入 `.x` 得到的域名是 `x.a.com`，与他看到的字面不是同一个东西。
		// 点只能出现在标签**之间**，前导/尾随都算非法。
		value = strings.ToLower(value)
		if value == "" || strings.ContainsAny(value, "/\\* \t\r\n?#") {
			return "", ErrScriptDomainSlotInvalid
		}
		if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
			return "", ErrScriptDomainSlotInvalid
		}
		for _, label := range strings.Split(value, ".") {
			if label == "" || len(label) > 63 {
				return "", ErrScriptDomainSlotInvalid
			}
			if label[0] == '-' || label[len(label)-1] == '-' {
				return "", ErrScriptDomainSlotInvalid
			}
			for i := 0; i < len(label); i++ {
				char := label[i]
				if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
					continue
				}
				return "", ErrScriptDomainSlotInvalid
			}
		}
		return value, nil
	case ScriptDomainFormatPath:
		value = strings.Trim(value, "/")
		if value == "" {
			return "", ErrScriptDomainSlotInvalid
		}
		if strings.ContainsAny(value, "\\* \t\r\n?#") {
			return "", ErrScriptDomainSlotInvalid
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return "", ErrScriptDomainSlotInvalid
			}
			for i := 0; i < len(segment); i++ {
				char := segment[i]
				if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
					(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == '~' {
					continue
				}
				return "", ErrScriptDomainSlotInvalid
			}
		}
		return value, nil
	default:
		return "", ErrScriptDomainSlotInvalid
	}
}

// BuildScriptDomainFromFormat 把「模板 + 占位内容」拼成最终域名。
//
//	wildcard + "x"    → "x.a.com"
//	path     + "p/q"  → "a.com/p/q"
//
// ⚠️ 拼接顺序由模板种类决定，**不是**「输入框永远在前面」。把这里写反
// （`a.com/x` 而不是 `x.a.com`）会让泛域名槽位产出主从颠倒的地址。
func BuildScriptDomainFromFormat(format ScriptDomainFormat, slot string) (string, error) {
	normalized, err := normalizeScriptDomainSlot(format.Kind, slot)
	if err != nil {
		return "", err
	}
	if format.SlotIsPrefix() {
		return normalized + "." + format.Host, nil
	}
	return format.Host + "/" + normalized, nil
}

// SplitScriptDomainForFormat 是 BuildScriptDomainFromFormat 的逆运算：
// 给定一条具体域名和一条模板，取回槽位内容。不匹配返回 ok=false。
//
// UI 回填编辑态时用它：库里存的是 `x.a.com`，但界面要显示成 `[x].a.com`。
func SplitScriptDomainForFormat(format ScriptDomainFormat, domain string) (string, bool) {
	value := strings.TrimSpace(strings.ToLower(domain))
	if value == "" {
		return "", false
	}
	if format.SlotIsPrefix() {
		suffix := "." + format.Host
		if len(value) <= len(suffix) || !strings.HasSuffix(value, suffix) {
			return "", false
		}
		slot := strings.TrimSuffix(value, suffix)
		if normalized, err := normalizeScriptDomainSlot(format.Kind, slot); err == nil {
			return normalized, true
		}
		return "", false
	}
	prefix := format.Host + "/"
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	slot := strings.TrimPrefix(value, prefix)
	if normalized, err := normalizeScriptDomainSlot(format.Kind, slot); err == nil {
		return normalized, true
	}
	return "", false
}
