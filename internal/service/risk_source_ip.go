package service

import (
	"net/netip"
	"strings"
)

var riskSharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// attributableRiskSourceIP canonicalizes only individually attributable addresses.
// Shared proxy infrastructure is not a usable enforcement target.
func attributableRiskSourceIP(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || addr.Zone() != "" {
		return ""
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || IsPrivateOrReservedIP(addr.String()) || riskSharedAddressSpace.Contains(addr) {
		return ""
	}
	return addr.String()
}
