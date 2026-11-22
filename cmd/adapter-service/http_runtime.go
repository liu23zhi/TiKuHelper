package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type httpRuntime struct {
	Address string
	Proxies trustedProxyConfig
}

// The bundled gateway owns the external trust policy. Its child accepts only
// loopback traffic so the gateway can rebuild client IP and scheme once.
func loadHTTPRuntime() (httpRuntime, error) {
	internal := false
	if raw := strings.TrimSpace(os.Getenv("TIKU_INTERNAL_GATEWAY")); raw != "" {
		var err error
		internal, err = strconv.ParseBool(raw)
		if err != nil {
			return httpRuntime{}, fmt.Errorf("TIKU_INTERNAL_GATEWAY 须为 true/false 或 1/0")
		}
	}
	address := strings.TrimSpace(os.Getenv("TIKU_LISTEN_ADDRESS"))
	if address == "" {
		address = "0.0.0.0:8060"
		if internal {
			address = "127.0.0.1:8061"
		}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return httpRuntime{}, fmt.Errorf("TIKU_LISTEN_ADDRESS 须为 IP:端口: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 || (host != "" && net.ParseIP(host) == nil) {
		return httpRuntime{}, fmt.Errorf("TIKU_LISTEN_ADDRESS 须为有效的 IP:端口（1–65535）")
	}
	config := httpRuntime{Address: address}
	if internal {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return httpRuntime{}, fmt.Errorf("内置网关的 Go 服务只能监听回环 IP")
		}
		config.Proxies = trustedProxyConfig{
			List: []string{"127.0.0.1/32", "::1/128"}, Source: "内置 Nginx（仅回环）", ConfiguredCount: 2,
		}
		return config, nil
	}
	config.Proxies, err = loadTrustedProxies()
	return config, err
}
