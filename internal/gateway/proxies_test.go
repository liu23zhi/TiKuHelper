package gateway

import (
	"strings"
	"testing"
)

func TestProxyPolicySeparatesClientIPCompatibilityFromHTTPSPeerTrust(t *testing.T) {
	for _, raw := range []string{"", "none"} {
		config, e := ProxyConfig(raw)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(config, "set_real_ip_from 0.0.0.0/0;") || strings.Contains(config, "0.0.0.0/0 1;") {
			t.Fatalf("empty/none must preserve ClientIP compatibility but not trust HTTPS headers: %s", config)
		}
	}
	config, e := ProxyConfig("trusted_proxies: [192.0.2.9, 198.51.100.0/255.255.255.0]\n10.0.0.1-10.0.0.2 # fixture")
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range []string{"192.0.2.9/32 1;", "198.51.100.0/24 1;", "set_real_ip_from 127.0.0.0/8;", "geo $realip_remote_addr $tiku_trusted_proto"} {
		if !strings.Contains(config, line) {
			t.Fatalf("missing %q in %s", line, config)
		}
	}
	if strings.Contains(config, "127.0.0.0/8 1;") {
		t.Fatal("implicit LAN defaults must not grant HTTPS header trust")
	}
	for _, raw := range []string{"none,127.0.0.1", "*", "0.0.0.0/1,128.0.0.0/1", "bad-address"} {
		if _, e := ProxyConfig(raw); e == nil {
			t.Fatalf("invalid proxy policy accepted: %s", raw)
		}
	}
}
