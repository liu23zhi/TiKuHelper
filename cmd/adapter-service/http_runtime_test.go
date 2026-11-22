package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/controller"
)

func TestHTTPRuntimeListenDefaultsAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name, address, gateway, want string
		invalid                      bool
	}{
		{name: "standalone", want: "0.0.0.0:8060"},
		{name: "standalone custom", address: "127.0.0.1:9999", want: "127.0.0.1:9999"},
		{name: "gateway", gateway: "1", want: "127.0.0.1:8061"},
		{name: "gateway ipv6", gateway: "true", address: "[::1]:9999", want: "[::1]:9999"},
		{name: "gateway refuses public listener", gateway: "1", address: "0.0.0.0:8061", invalid: true},
		{name: "gateway refuses hostname", gateway: "1", address: "localhost:8061", invalid: true},
		{name: "invalid port", address: "127.0.0.1:70000", invalid: true},
		{name: "zero port", address: "127.0.0.1:0", invalid: true},
		{name: "url", address: "http://127.0.0.1:8060", invalid: true},
		{name: "invalid flag", gateway: "maybe", invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TIKU_LISTEN_ADDRESS", tt.address)
			t.Setenv("TIKU_INTERNAL_GATEWAY", tt.gateway)
			t.Setenv("TIKU_APP_MODE", "production")
			t.Setenv("TRUSTED_PROXIES", "none")
			t.Setenv("TRUSTED_PROXIES_FILE", "")
			config, err := loadHTTPRuntime()
			if tt.invalid {
				if err == nil {
					t.Fatalf("accepted invalid listener %q, gateway %q", tt.address, tt.gateway)
				}
				return
			}
			if err != nil || config.Address != tt.want {
				t.Fatalf("address=%q error=%v, want %q", config.Address, err, tt.want)
			}
		})
	}
}

func TestHTTPRuntimeGatewayTrustsOnlyLocalNginx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TIKU_LISTEN_ADDRESS", "")
	t.Setenv("TIKU_INTERNAL_GATEWAY", "1")
	// External proxy configuration belongs to the gateway; it must not broaden
	// the Go child's trust or require that child to read the external file.
	t.Setenv("TRUSTED_PROXIES", "198.51.100.1")
	t.Setenv("TRUSTED_PROXIES_FILE", "missing-fixture-file")
	config, err := loadHTTPRuntime()
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	if err := router.SetTrustedProxies(config.Proxies.List); err != nil {
		t.Fatal(err)
	}
	router.Use(controller.ScriptProxyHTTPS(config.Proxies.List[:config.Proxies.ConfiguredCount]))
	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ip": c.ClientIP(), "https": c.GetBool("script_proxy_https")})
	})
	for _, tt := range []struct {
		peer, wantIP string
		wantHTTPS    bool
	}{
		{"127.0.0.1:5555", "203.0.113.9", true},
		{"[::1]:5555", "203.0.113.9", true},
		{"198.51.100.1:5555", "198.51.100.1", false},
		{"172.18.0.1:5555", "172.18.0.1", false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tt.peer
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		req.Header.Set("X-Forwarded-Proto", "https")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		want := `"https":false`
		if tt.wantHTTPS {
			want = `"https":true`
		}
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"ip":"`+tt.wantIP+`"`) || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("peer %s: %s", tt.peer, response.Body.String())
		}
	}
}

func TestHTTPRuntimeStillValidatesStandaloneProxyPolicy(t *testing.T) {
	t.Setenv("TIKU_LISTEN_ADDRESS", "")
	t.Setenv("TIKU_INTERNAL_GATEWAY", "")
	t.Setenv("TIKU_APP_MODE", "production")
	t.Setenv("TRUSTED_PROXIES", "invalid fixture")
	t.Setenv("TRUSTED_PROXIES_FILE", "")
	if _, err := loadHTTPRuntime(); err == nil {
		t.Fatal("standalone startup bypassed trusted proxy validation")
	}
}
