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
//  1. `*.a.com` **不匹配** `a.com`。这是通配 DNS / 通配证书的标准语义，
//     也是本仓既有 `scriptDomainHostMatch` 的带点后缀语义。若放行裸域，
//     任何买了 `a.com` 的人都能用 `*.a.com` 规则把裸域也纳进来 —— 与
//     「泛域名」的字面意思不符。
//  2. `a.com/*` **匹配多段路径**（`/p/q` 命中）。用户明确给的例子就是
//     `a.com/*` + 输入 `p/q` → `a.com/p/q`。旧的段级 glob（`*` 恰好一段、
//     `**` 多段）在这套语法里不再存在。
//  3. `a.com/*` **不匹配** `a.com`（没有路径）。`/*` 要求至少一段。
//     否则 `a.com` 会被路径规则与泛域名规则同时覆盖，管理员无法表达
//     「只开放某个路径」。
//
// 旧格式（裸 `a.com`、`a.com/a/*`、`a.com/**`、带 scheme 的 URL……）**一律拒绝**，
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
// 占位输入框的位置由 Kind 决定，调用方据此渲染：
//
//	wildcard → 「[输入框].a.com」
//	path     → 「a.com/[输入框]」
type ScriptDomainFormat struct {
	// Raw 是归一化后的模板文本，也是落库形态。
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
	// scheme 一律不接受：新语法是域名模板，不是 URL。
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, " \t\r\n\\'\"?#") {
		return ScriptDomainFormat{}, true, ErrScriptDomainFormatLegacy
	}
	lowered := strings.ToLower(trimmed)

	// 路径形态：`host/*` 是新语法，其余带路径的写法要分清是**旧格式**还是**畸形新语法**。
	//
	// 这个区分对管理员很重要，因为两种错误给出的下一步动作相反：
	//   - 旧格式（`a.com/a/*`、`a.com/**`）：他照旧文档写的 ⇒ 提示「请改用新写法」；
	//   - 畸形新语法（`a.com/*/x`、`a.com/**`）：他已经在新写法上了，只是写错 ⇒
	//     必须报 Invalid（「格式不合法」），报 Legacy 等于让他把同一件事再做一遍。
	//
	// 判据：`*` 出现在**除 `/*` 之外**的位置 ⇒ 他在尝试通配但位置/数量不对 ⇒ Invalid。
	// 完全没有 `*` 的路径（`a.com/a`、`a.com/a.com`）⇒ 旧格式。
	if slash := strings.IndexByte(lowered, '/'); slash >= 0 {
		host := lowered[:slash]
		pathPart := lowered[slash:]
		if pathPart == "/*" && !strings.Contains(host, "*") {
			if !validScriptDomainHost(host) {
				return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
			}
			return ScriptDomainFormat{Raw: host + "/*", Host: host, Kind: ScriptDomainFormatPath}, true, nil
		}
		// 带 `*` 但不是合法的 `host/*`：用户在新语法上写错了。
		if strings.Contains(lowered, "*") {
			return ScriptDomainFormat{}, true, ErrScriptDomainFormatInvalid
		}
		// 完全无 `*` 的路径写法：旧格式。
		return ScriptDomainFormat{}, true, ErrScriptDomainFormatLegacy
	}

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
// 至少两级（`a.com`）：单标签主机无法与泛域名 `*.` 前缀区分，也无法表达
// 「子域」这个概念。这条同时挡住 `.a.com`、`a..com`、`a.com.` 之类的畸形输入。
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

// scriptDomainWildcardCovers 判断一条泛域名格式是否覆盖给定的主机名。
//
// ⚠️ 这是**唯一**一处泛域名匹配实现。之前 `scriptDomainTemplateMatches`、
// `ValidateScriptDomainWithDB`、`defaultScriptUseWildcardWithDB` 各手写了一遍
// `len(host) > len(suffix) && HasSuffix(host, suffix)`，变异验证时暴露出
// 「改掉一处只红一条用例、另外两处继续用旧语义」——三份实现必然漂移。
// 现在三处都调它，Q2 的边界只在这里定义一次。
//
// 语义（Q2，严格）：`*.a.com` 覆盖任意深度的子域，但**不覆盖** `a.com` 本身。
// 必须比后缀 `.a.com` 更长，否则裸域会命中。
func scriptDomainWildcardCovers(format ScriptDomainFormat, host string) bool {
	if format.Kind != ScriptDomainFormatWildcard || format.Host == "" {
		return false
	}
	suffix := "." + format.Host
	return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
}

// scriptDomainPathCovers 判断一条路径格式是否覆盖给定主机（只看主机维度）。
//
// 路径格式要求主机**全等**：`a.com/*` 只负责 a.com 自己的路径，
// 不把 subdomain 拉进来。路径段的判定见 scriptDomainTemplateMatches。
func scriptDomainPathCovers(format ScriptDomainFormat, host string) bool {
	return format.Kind == ScriptDomainFormatPath && strings.EqualFold(host, format.Host)
}

// scriptDomainTemplateMatches 判断一条模板是否覆盖给定的「脚本 baseDomain + 请求路径」。
//
// ⚠️ 第二个参数是**脚本的 baseDomain**（库里的 base_domain），**不是**请求的完整 host。
//
// 为什么：脚本的公开地址恒为 `<shortCode>.<baseDomain>`（见 ScriptCommandOrigin），
// 而 baseDomain 正是创建期被管理员格式校验过的那个值。用完整 host 去比会把
// shortCode 那一级当成「子域」再要求多一层，于是 `*.a.com` 配 baseDomain=x.a.com 时
// 需要 host=sc.x.a.com 才命中 —— 而 a.com/* 的 EqualFold 又要求 host 恰好等于 a.com，
// 两者不可能同时成立。传 baseDomain 才是与创建期校验同一口径。
//
// cleanPath 已由 cleanScriptRequestPath 规范化。
func scriptDomainTemplateMatches(format ScriptDomainFormat, baseDomain, cleanPath string) bool {
	baseDomain = strings.TrimRight(strings.ToLower(strings.TrimSpace(baseDomain)), ".")
	if baseDomain == "" {
		return false
	}
	if scriptDomainWildcardCovers(format, baseDomain) {
		return true
	}
	if scriptDomainPathCovers(format, baseDomain) {
		// `/*` 要求至少一段；根路径不落在路径规则上（见文件头 Q3）。
		return len(scriptPathSegments(cleanPath)) > 0
	}
	return false
}

// normalizeScriptDomainSlot 归一化用户填进占位框的内容。
//
// 泛域名槽位：点分标签，每段与主机标签同规则。允许填多级（`x.y` ⇒ `x.y.a.com`），
// 因为 `*.a.com` 本来就覆盖任意深度的子域。
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
		if strings.ContainsAny(value, "/\\* \t\r\n?#") {
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
// 会让泛域名槽位产出主从颠倒的地址（`a.com/x` 而不是 `x.a.com`）。
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
