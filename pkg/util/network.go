package util

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// NewSafeHTTPClient creates a client that blocks loopback, private, link-local,
// multicast, and unspecified destinations unless TIKU_ALLOW_PRIVATE_OUTBOUND=true.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(network string, address string, _ syscall.RawConn) error {
			if allowPrivateOutbound() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("outbound destination is not an IP address")
			}
			if isRestrictedOutboundIP(ip) {
				return fmt.Errorf("outbound destination is not allowed")
			}
			return nil
		},
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

func allowPrivateOutbound() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TIKU_ALLOW_PRIVATE_OUTBOUND")), "true")
}

func isRestrictedOutboundIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.Equal(net.ParseIP("169.254.169.254")) ||
		ip.Equal(net.ParseIP("100.100.100.200"))
}
