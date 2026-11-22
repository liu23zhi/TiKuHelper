package gateway

// Input syntax and restricted-range validation mirror the backend startup policy.
// HTTPS forwarding deliberately uses only explicitly configured peer ranges.
import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/bits"
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/itihey/tikuAdapter/internal/proxysource"
)

const trustAllProxiesIPv4 = "0.0.0.0/0"
const trustAllProxiesIPv6 = "::/0"

var trustedProxyKeyPrefixes = []string{"trusted_proxies", "trustedproxies", "trusted_proxy", "trustedproxy", "proxies", "proxy", "ips", "ip", "set_real_ip_from", "real_ip_from", "allow", "deny", "from"}
var defaultTrustedProxies = []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "fc00::/7", "fe80::/10"}

func ProxyConfig(raw string) (string, error) {
	raw = proxysource.Clean(raw)
	var explicit []string
	if strings.TrimSpace(raw) != "" && !strings.EqualFold(strings.TrimSpace(raw), "none") {
		entries, invalid := parseTrustedProxyEntries(raw)
		if len(invalid) > 0 || len(entries) == 0 {
			return "", fmt.Errorf("TRUSTED_PROXIES contains invalid IP/CIDR entries")
		}
		var err error
		explicit, err = resolveStartupTrustedProxies("production", strings.Join(entries, ","))
		if err != nil {
			return "", err
		}
	}
	client := append([]string{}, explicit...)
	if len(explicit) == 0 {
		client = []string{trustAllProxiesIPv4, trustAllProxiesIPv6}
	} else {
		client = append(client, defaultTrustedProxies...)
	}
	var b strings.Builder
	for _, entry := range client {
		fmt.Fprintf(&b, "set_real_ip_from %s;\n", entry)
	}
	b.WriteString("real_ip_header X-Forwarded-For;\nreal_ip_recursive on;\ngeo $realip_remote_addr $tiku_trusted_proto {\n default 0;\n")
	for _, entry := range explicit {
		if addr, e := netip.ParseAddr(entry); e == nil {
			entry = netip.PrefixFrom(addr, addr.BitLen()).String()
		}
		fmt.Fprintf(&b, " %s 1;\n", entry)
	}
	b.WriteString("}\nmap \"$scheme:$tiku_trusted_proto:$http_x_forwarded_proto\" $tiku_forwarded_proto {\n default $scheme;\n ~*^http:1:https$ https;\n}\n")
	return b.String(), nil
}
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
	for cursor := uint64(first); cursor <= uint64(last); {
		current := uint32(cursor)
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
		cursor += uint64(1) << uint(trailing)
	}
	return cidrs
}

func uint32ToIPv4(value uint32) string {
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value)).String()
}

type proxyRange struct{ lo, hi *big.Int }

func proxyAddressRange(prefix netip.Prefix) proxyRange {
	prefix = prefix.Masked()
	addr := prefix.Addr()
	var raw []byte
	bits := 128
	if addr.Is4() {
		a := addr.As4()
		raw = a[:]
		bits = 32
	} else {
		a := addr.As16()
		raw = a[:]
	}
	lo := new(big.Int).SetBytes(raw)
	size := new(big.Int).Lsh(big.NewInt(1), uint(bits-prefix.Bits()))
	hi := new(big.Int).Sub(new(big.Int).Add(new(big.Int).Set(lo), size), big.NewInt(1))
	return proxyRange{lo, hi}
}
func proxyRangesTrustAll(ranges []proxyRange, bits uint) bool {
	if len(ranges) == 0 {
		return false
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo.Cmp(ranges[j].lo) < 0 })
	next := big.NewInt(0)
	limit := new(big.Int).Lsh(big.NewInt(1), bits)
	for _, r := range ranges {
		if r.lo.Cmp(next) > 0 {
			return false
		}
		candidate := new(big.Int).Add(r.hi, big.NewInt(1))
		if candidate.Cmp(next) > 0 {
			next = candidate
		}
		if next.Cmp(limit) >= 0 {
			return true
		}
	}
	return false
}

// resolveStartupTrustedProxies 校验并规范化「用户显式填写的」可信代理条目。
//
// 语义以 task_doc/67-TRUSTED_PROXIES语义定义.md 为准：
//
//	空 或 none  → 视为「没有输入」，返回全信任（0.0.0.0/0 + ::/0）
//	具体 IP/CIDR → 逐条校验后返回规范化条目
//
// 注意：本函数只处理**用户条目**，不负责前置「局域网/本地」默认段 ——
// 那是调用方（loadTrustedProxies）用 composeTrustedProxies 补的。
// 顺序不能反：默认段本身含 /8、/12 这类宽段，混进来会被整族信任检查误判。
func resolveStartupTrustedProxies(mode, configured string) ([]string, error) {
	value := strings.TrimSpace(configured)
	if value == "" || strings.EqualFold(value, "none") {
		// 定义一个口径：空与 none 等价，都视为「没有输入」→ 信任所有来源。
		return []string{trustAllProxiesIPv4, trustAllProxiesIPv6}, nil
	}
	var values []string
	var ipv4, ipv6 []proxyRange
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, errors.New("TRUSTED_PROXIES contains an empty entry")
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			addr, parseErr := netip.ParseAddr(item)
			if parseErr != nil || addr.Zone() != "" {
				return nil, errors.New("TRUSTED_PROXIES contains an invalid IP/CIDR")
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
			item = addr.String()
		} else {
			if prefix.Addr().Is4In6() {
				if prefix.Bits() < 96 {
					return nil, errors.New("TRUSTED_PROXIES mapped range is unrestricted")
				}
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			prefix = prefix.Masked()
			item = prefix.String()
		}
		if prefix.Addr().Is4() {
			ipv4 = append(ipv4, proxyAddressRange(prefix))
		} else {
			ipv6 = append(ipv6, proxyAddressRange(prefix))
		}
		values = append(values, item)
	}
	if proxyRangesTrustAll(ipv4, 32) || proxyRangesTrustAll(ipv6, 128) {
		return nil, errors.New("TRUSTED_PROXIES must not trust an entire address family")
	}
	return values, nil
}
