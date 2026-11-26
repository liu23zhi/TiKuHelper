package controller

import (
	"net"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

const agreementSourceKey = "agreement_verified_source"
const agreementGatewayMark = "by=_tiku_agreement_gateway"

func agreementRoute(path string) bool {
	return strings.HasPrefix(path, "/api/agreements/") || strings.HasPrefix(path, "/api/admin/agreements/")
}
func agreementPeer(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}
	}
	return ip.Unmap()
}
func agreementSource(c *gin.Context) string {
	if value, ok := c.Get(agreementSourceKey); ok {
		s, _ := value.(string)
		return s
	}
	// Even if this handler is mounted alone, never inherit Gin's legacy trust-all.
	if p := agreementPeer(c.Request.RemoteAddr); p.IsValid() {
		return p.String()
	}
	return ""
}

// AgreementSourcePolicy uses the explicitly configured peers, not Gin's
// compatibility defaults. Only a single incoming IP from a trusted immediate
// proxy is usable; a chain or malformed value cannot establish its origin.
func AgreementSourcePolicy(explicit []string, gateway bool) gin.HandlerFunc {
	var trusted []netip.Prefix
	for _, s := range explicit {
		if ip, e := netip.ParseAddr(s); e == nil {
			ip = ip.Unmap()
			trusted = append(trusted, netip.PrefixFrom(ip, ip.BitLen()))
		} else if n, e := netip.ParsePrefix(s); e == nil && n.Bits() > 0 {
			trusted = append(trusted, n)
		}
	}
	return func(c *gin.Context) {
		if !agreementRoute(c.Request.URL.Path) {
			c.Next()
			return
		}
		peer := agreementPeer(c.Request.RemoteAddr)
		source := ""
		permitted := false
		if peer.IsValid() {
			source = peer.String()
			for _, p := range trusted {
				if p.Contains(peer) {
					permitted = true
					break
				}
			}
		}
		if gateway {
			// On an old bundled gateway 'Forwarded' is always stripped. Never use
			// its legacy reconstructed XFF while falling back to an old generation.
			source = ""
			marks := c.Request.Header.Values("Forwarded")
			permitted = permitted && peer.IsLoopback() && len(marks) == 1 && marks[0] == agreementGatewayMark
		}
		if permitted {
			values := c.Request.Header.Values("X-Forwarded-For")
			if len(values) == 1 {
				if ip, e := netip.ParseAddr(strings.TrimSpace(values[0])); e == nil && ip.Zone() == "" {
					source = ip.Unmap().String()
				}
			}
		}
		c.Set(agreementSourceKey, source)
		c.Next()
	}
}
