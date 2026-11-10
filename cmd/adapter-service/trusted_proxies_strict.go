package main

import (
	"errors"
	"math/big"
	"net/netip"
	"sort"
	"strings"
)

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
