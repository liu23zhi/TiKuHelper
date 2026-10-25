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
func resolveStartupTrustedProxies(mode, configured string) ([]string, error) {
	value := strings.TrimSpace(configured)
	if strings.EqualFold(value, "none") {
		return nil, nil
	}
	if value == "" {
		return nil, errors.New("startup requires explicit TRUSTED_PROXIES=none or restricted IP/CIDR entries")
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
