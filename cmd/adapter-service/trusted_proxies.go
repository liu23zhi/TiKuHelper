package main

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

const (
	// trustedProxiesEnv 直接指定可信代理（支持多种写法，见 parseTrustedProxyEntries）。
	trustedProxiesEnv = "TRUSTED_PROXIES"
	// trustedProxiesFileEnv 可选：自定义 trusted_proxies.txt 的路径。
	trustedProxiesFileEnv = "TRUSTED_PROXIES_FILE"
	// trustedProxiesFileName 默认从工作目录读取该文件。
	trustedProxiesFileName = "trusted_proxies.txt"
	// trustAllProxiesIPv4 / IPv6：未配置任何可信代理时的取值 —— 信任所有来源。
	trustAllProxiesIPv4 = "0.0.0.0/0"
	trustAllProxiesIPv6 = "::/0"
)

// defaultTrustedProxies 是「局域网 + 本地」的隐式默认可信来源。
//
// 为什么默认信任它们：这些地址只可能来自本机（宿主机反代 / Docker 网关）
// 或内网（1Panel、宝塔、内网 Nginx、其它容器），公网客户端根本连不上。
// 让它们免配置即可透传 X-Forwarded-For，能省掉一大类「整站只看到一个代理地址」的坑。
//
// 这条默认值只在用户配置了可信代理时作为补充；用户完全没配置时仍是「信任所有来源」，
// 见 resolveTrustedProxies —— 不要为了收紧默认值而破坏那条行为。
var defaultTrustedProxies = []string{
	"127.0.0.0/8",    // IPv4 本地回环：宿主机上的反向代理
	"::1/128",        // IPv6 本地回环
	"10.0.0.0/8",     // RFC1918 局域网
	"172.16.0.0/12",  // RFC1918 局域网（含 Docker 默认网段 172.17-172.31）
	"192.168.0.0/16", // RFC1918 局域网
	"169.254.0.0/16", // IPv4 链路本地
	"fc00::/7",       // IPv6 ULA
	"fe80::/10",      // IPv6 链路本地
}

// trustedProxyConfig 是最终生效的可信代理配置。
type trustedProxyConfig struct {
	List            []string
	Source          string // "env"、文件路径，或空字符串（未配置）
	Invalid         []string
	ConfiguredCount int // 用户显式配置的条目数（不含隐式默认值）
}

// trustedProxyKeyPrefixes 是各种配置/指令写法里的键名，解析时整段剥掉。
// 覆盖本项目 compose、YAML/JSON、以及 nginx 的 real_ip / allow 配置。
var trustedProxyKeyPrefixes = []string{
	"trusted_proxies", "trustedproxies", "trusted_proxy", "trustedproxy",
	"proxies", "proxy", "ips", "ip",
	"set_real_ip_from", "real_ip_from", "allow", "deny", "from",
}

// loadTrustedProxies 读取可信代理配置。
//
// 优先级：环境变量 TRUSTED_PROXIES > 文件（TRUSTED_PROXIES_FILE 指定的路径，
// 或工作目录下的 trusted_proxies.txt）。
// 两者都为空 → 信任所有来源的转发头（见 resolveTrustedProxies）。
//
// 只有「来源写了内容、却一条都解析不出来」才返回错误 —— 这时静默降级成
// 「信任所有来源」等于把管理员的安全设置悄悄关掉，必须让人看到。
func loadTrustedProxies() (trustedProxyConfig, error) {
	envValue := os.Getenv(trustedProxiesEnv)
	path := strings.TrimSpace(os.Getenv(trustedProxiesFileEnv))
	if path == "" {
		path = trustedProxiesFileName
	}
	fileValue := readTrustedProxiesFile(path)

	envEntries, envInvalid := parseTrustedProxyEntries(envValue)
	fileEntries, fileInvalid := parseTrustedProxyEntries(fileValue)

	switch {
	case len(envEntries) > 0:
		return newTrustedProxyConfig(envEntries, trustedProxiesEnv, envInvalid), nil
	case len(envInvalid) > 0:
		return trustedProxyConfig{}, fmt.Errorf("环境变量 %s 里没有可解析的 IP/CIDR：%s", trustedProxiesEnv, strings.Join(envInvalid, "、"))
	case len(fileEntries) > 0:
		return newTrustedProxyConfig(fileEntries, path, fileInvalid), nil
	case len(fileInvalid) > 0:
		return trustedProxyConfig{}, fmt.Errorf("%s 里没有可解析的 IP/CIDR：%s（清空该文件即信任所有来源）", path, strings.Join(fileInvalid, "、"))
	default:
		return trustedProxyConfig{List: []string{trustAllProxiesIPv4, trustAllProxiesIPv6}}, nil
	}
}

// newTrustedProxyConfig 组装最终配置：用户条目 + 「局域网/本地」默认值。
func newTrustedProxyConfig(configured []string, source string, invalid []string) trustedProxyConfig {
	return trustedProxyConfig{
		List:            composeTrustedProxies(configured),
		Source:          source,
		Invalid:         invalid,
		ConfiguredCount: len(configured),
	}
}

// composeTrustedProxies 把用户配置与「局域网 + 本地」默认值合并（去重，配置项在前）。
//
// 用户显式写了全信任（0.0.0.0/0 / ::/0）时不再补默认值 —— 那是冗余噪音。
func composeTrustedProxies(configured []string) []string {
	seen := make(map[string]struct{}, len(configured)+len(defaultTrustedProxies))
	list := make([]string, 0, len(configured)+len(defaultTrustedProxies))
	for _, entry := range configured {
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		list = append(list, entry)
	}
	for _, entry := range list {
		if isTrustAllProxyEntry(entry) {
			return list
		}
	}
	for _, entry := range defaultTrustedProxies {
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		list = append(list, entry)
	}
	return list
}

// isTrustAllProxyEntry 判断某条配置本身是否就是「信任所有来源」。
func isTrustAllProxyEntry(entry string) bool {
	switch strings.TrimSpace(entry) {
	case trustAllProxiesIPv4, trustAllProxiesIPv6, "0/0":
		return true
	default:
		return false
	}
}

// resolveTrustedProxies 计算最终的可信代理列表。
//
//   - 环境变量或文件任一有内容 → 只信任列出的 IP/CIDR；
//   - 两者都为空 → 返回全信任列表（0.0.0.0/0 + ::/0），
//     即「不配置就读取所有代理转发头（X-Forwarded-For / X-Real-IP）」。
//
// 这个函数只做「选哪一份 + 空则全信任」的宽松判断（供 HTTP 层测试使用）；
// 真正的配置校验、无效条目告警与「全是错的就报错」在 loadTrustedProxies 里。
func resolveTrustedProxies(envValue, fileValue string) []string {
	if entries, _ := parseTrustedProxyEntries(envValue); len(entries) > 0 {
		return composeTrustedProxies(entries)
	}
	if entries, _ := parseTrustedProxyEntries(fileValue); len(entries) > 0 {
		return composeTrustedProxies(entries)
	}
	return []string{trustAllProxiesIPv4, trustAllProxiesIPv6}
}

// parseTrustedProxyEntries 解析可信代理配置文本，返回 (可用的 IP/CIDR, 无法解析的条目)。
//
// 支持的写法（可混用）：
//
//	172.26.0.3                 一行一个
//	172.26.0.3, 172.18.0.0/16  逗号 / 分号分隔
//	- 172.26.0.3               YAML 列表项
//	["172.26.0.3"]             JSON 数组
//	trusted_proxies: 10.0.0.1  键值写法（键名会被剥掉）
//	set_real_ip_from 10.0.0.1; nginx real_ip / allow 指令
//	172.26.0.3 反向代理容器    条目后面的文字视为说明，直接忽略
//	10.0.0.0/255.0.0.0         掩码写法，自动换算成 /8
//	172.26.0.0-172.26.255.255  区间写法，自动换算成最小 CIDR 集合
//	*  /  any  /  all          显式信任所有来源
//	# 注释、// 注释、CRLF、UTF-8 BOM 都会被忽略
func parseTrustedProxyEntries(raw string) (entries []string, invalid []string) {
	seen := make(map[string]struct{})
	invalidSeen := make(map[string]struct{})
	add := func(value string) {
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		entries = append(entries, value)
	}
	addInvalid := func(value string) {
		if _, ok := invalidSeen[value]; ok {
			return
		}
		invalidSeen[value] = struct{}{}
		invalid = append(invalid, value)
	}

	for _, rawLine := range strings.Split(raw, "\n") {
		line := stripTrustedProxyLine(rawLine)
		if line == "" {
			continue
		}
		// 逗号/分号切分条目；同一段里「第一个空白前的部分」是条目，后面是说明文字。
		for _, segment := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' }) {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			entry := trimProxyDecoration(strings.Fields(segment)[0])
			if entry == "" {
				continue
			}
			if isTrustAllProxyKeyword(entry) {
				add(trustAllProxiesIPv4)
				add(trustAllProxiesIPv6)
				continue
			}
			if normalized, ok := normalizeProxyEntry(entry); ok {
				add(normalized)
				continue
			}
			if expanded := expandIPv4Range(entry); len(expanded) > 0 {
				for _, item := range expanded {
					add(item)
				}
				continue
			}
			addInvalid(entry)
		}
	}
	return entries, invalid
}

// stripTrustedProxyLine 去掉 BOM、行内注释、YAML 列表符号与键名/指令前缀。
func stripTrustedProxyLine(rawLine string) string {
	line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rawLine), "\ufeff"))
	if index := strings.Index(line, "#"); index >= 0 {
		line = line[:index]
	}
	if index := strings.Index(line, "//"); index >= 0 {
		line = line[:index]
	}
	line = strings.TrimSpace(line)
	// YAML 列表项 "- 1.2.3.4" / "* 1.2.3.4"
	if len(line) > 1 && (line[0] == '-' || line[0] == '*') {
		if rest := strings.TrimSpace(line[1:]); rest != "" {
			line = rest
		}
	}
	return strings.TrimSpace(stripTrustedProxyKeyPrefix(line))
}

// stripTrustedProxyKeyPrefix 剥掉 "trusted_proxies:" / "allow " 这类键名或指令前缀。
func stripTrustedProxyKeyPrefix(line string) string {
	lower := strings.ToLower(line)
	for _, prefix := range trustedProxyKeyPrefixes {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := line[len(prefix):]
		if rest == "" {
			return ""
		}
		// 前缀后面必须是分隔符，避免把 "proxy.example" 这类内容当成键名吃掉。
		switch rest[0] {
		case ':', '=', ' ', '\t':
		default:
			continue
		}
		return strings.TrimLeft(rest, ":=\t ")
	}
	return line
}

// trimProxyDecoration 去掉 JSON/引号/方括号等装饰，只留条目本体。
func trimProxyDecoration(value string) string {
	return strings.Trim(strings.TrimSpace(value), "[]{}\"'`")
}

func isTrustAllProxyKeyword(entry string) bool {
	switch strings.ToLower(strings.TrimSpace(entry)) {
	case "*", "any", "all", "0/0":
		return true
	default:
		return false
	}
}

// normalizeProxyEntry 校验并归一化单个条目（裸 IP、CIDR、掩码写法）。
func normalizeProxyEntry(entry string) (string, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", false
	}
	if ip := net.ParseIP(entry); ip != nil {
		return entry, true
	}
	if _, _, err := net.ParseCIDR(entry); err == nil {
		return entry, true
	}
	// 掩码写法：10.0.0.0/255.0.0.0 → 10.0.0.0/8
	parts := strings.SplitN(entry, "/", 2)
	if len(parts) != 2 {
		return "", false
	}
	ip := net.ParseIP(strings.TrimSpace(parts[0]))
	mask := net.ParseIP(strings.TrimSpace(parts[1]))
	if ip == nil || mask == nil || ip.To4() == nil {
		return "", false
	}
	mask4 := net.IPMask(mask.To4())
	ones, total := mask4.Size()
	if total != 32 || ones < 0 {
		return "", false
	}
	return fmt.Sprintf("%s/%d", ip.String(), ones), true
}

// expandIPv4Range 把 "172.26.0.10-172.26.0.20" 这类区间换算成最小 CIDR 集合。
// 非区间、跨规范（一端 IPv4 一端 IPv6）或顺序颠倒时返回 nil。
func expandIPv4Range(entry string) []string {
	parts := strings.Split(entry, "-")
	if len(parts) != 2 {
		return nil
	}
	startIP := net.ParseIP(strings.TrimSpace(parts[0]))
	endIP := net.ParseIP(strings.TrimSpace(parts[1]))
	if startIP == nil || endIP == nil {
		return nil
	}
	start := startIP.To4()
	end := endIP.To4()
	if start == nil || end == nil {
		return nil
	}
	first := binary.BigEndian.Uint32(start)
	last := binary.BigEndian.Uint32(end)
	if last < first {
		return nil
	}

	cidrs := make([]string, 0, 4)
	for current := first; current <= last; {
		trailing := 32
		if current != 0 {
			trailing = bits.TrailingZeros32(current)
		}
		span := uint64(last) - uint64(current) + 1
		if available := bits.Len64(span) - 1; available < trailing {
			trailing = available
		}
		if trailing < 0 {
			trailing = 0
		}
		cidrs = append(cidrs, fmt.Sprintf("%s/%d", uint32ToIPv4(current), 32-trailing))
		current += uint32(1) << uint(trailing)
	}
	return cidrs
}

func uint32ToIPv4(value uint32) string {
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value)).String()
}

// readTrustedProxiesFile 读取可信代理文件；不存在或读不到时按「空」处理。
func readTrustedProxiesFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

// logTrustedProxies 把最终生效的可信代理配置写进启动日志。
//
// 「信任所有来源」是显式选择的默认行为，但必须让人看见：
// 它意味着任何客户端都能伪造 X-Forwarded-For。
func logTrustedProxies(config trustedProxyConfig) {
	if len(config.Invalid) > 0 {
		logger.SysWarn("可信代理里有无法解析的条目（已忽略）：" + strings.Join(config.Invalid, "、"))
	}
	if config.Source == "" {
		logger.SysWarn("未配置可信代理（TRUSTED_PROXIES / trusted_proxies.txt 均为空）：将读取所有来源的 X-Forwarded-For，如需收紧请设置其中之一")
		return
	}
	message := "可信代理来源=" + config.Source + " 配置=" + strconv.Itoa(config.ConfiguredCount) + " 条"
	if defaults := len(config.List) - config.ConfiguredCount; defaults > 0 {
		message += " 默认局域网/本地=+" + strconv.Itoa(defaults) + " 条"
	}
	logger.SysLog(message + " list=" + strings.Join(config.List, ","))
}
