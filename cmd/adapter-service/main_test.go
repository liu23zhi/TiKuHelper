package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

func TestTrustedProxyClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name         string
		env          string
		file         string
		remoteAddr   string
		forwardedFor string
		realIP       string
		want         string
	}{
		{
			// 空配置 = 信任所有来源的转发头（产品约定，见 AGENTS.md 第 26 条）。
			name:       "empty configuration trusts forwarded for",
			remoteAddr: "198.51.100.10:32100", forwardedFor: "203.0.113.99", want: "203.0.113.99",
		},
		{
			name: "whitespace configuration still means empty", env: " \t ", file: "\n# 注释\n",
			remoteAddr: "198.51.100.10:32100", realIP: "203.0.113.99", want: "203.0.113.99",
		},
		{
			name:       "empty configuration trusts ipv6 forwarded for",
			remoteAddr: "[2001:db8::10]:32100", forwardedFor: "2001:db8::99", want: "2001:db8::99",
		},
		{
			// 全信任时取最左侧（真实客户端）那一跳。
			name:       "empty configuration takes the left-most hop",
			remoteAddr: "172.19.0.1:32100", forwardedFor: "203.0.113.99, 172.18.0.9", want: "203.0.113.99",
		},
		{
			name:       "file configuration is used when env is empty",
			file:       "10.0.0.10\n# 反代容器\n",
			remoteAddr: "10.0.0.10:32100", forwardedFor: "203.0.113.99, 198.51.100.20", want: "198.51.100.20",
		},
		{
			name:       "env configuration wins over file",
			env:        "10.0.0.11",
			file:       "10.0.0.10",
			remoteAddr: "10.0.0.11:32100", forwardedFor: "203.0.113.99", want: "203.0.113.99",
		},
		{
			name: "explicit trusted proxy takes the right-most untrusted hop", env: "10.0.0.10",
			remoteAddr: "10.0.0.10:32100", forwardedFor: "203.0.113.99, 198.51.100.20", want: "198.51.100.20",
		},
		{
			name: "explicit trusted proxy chain", env: " 10.0.0.10, 172.18.0.0/16 ",
			remoteAddr: "10.0.0.10:32100", forwardedFor: "203.0.113.99, 198.51.100.20, 172.18.0.8", want: "198.51.100.20",
		},
		{
			name: "untrusted peer cannot use configured proxy headers", env: "10.0.0.10",
			remoteAddr: "198.51.100.10:32100", forwardedFor: "203.0.113.99", realIP: "203.0.113.88", want: "198.51.100.10",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := gin.New()
			if err := server.SetTrustedProxies(resolveTrustedProxies(testCase.env, testCase.file)); err != nil {
				t.Fatal(err)
			}
			server.GET("/", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = testCase.remoteAddr
			request.Header.Set("X-Forwarded-For", testCase.forwardedFor)
			request.Header.Set("X-Real-IP", testCase.realIP)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if got := recorder.Body.String(); recorder.Code != http.StatusOK || got != testCase.want {
				t.Fatalf("ClientIP() = %q (status %d), want %q", got, recorder.Code, testCase.want)
			}
		})
	}
}

func TestGracefulShutdownClosesHTTPAndFileLogs(t *testing.T) {
	dir := t.TempDir()
	oldDir := *logger.LogDir
	*logger.LogDir = dir
	t.Cleanup(func() { _ = logger.Close(); *logger.LogDir = oldDir })
	if err := logger.SetupGinLog(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		logger.SysLog("startup-smoke request")
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := shutdownHTTPServerOnCancel(ctx, server.Config)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP shutdown did not finish")
	}
	if _, err := server.Client().Get(server.URL); err == nil {
		t.Fatal("HTTP listener remains open after shutdown")
	}
	logger.SysLog("shutdown-smoke complete")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Tiku.txt"))
	if err != nil || !strings.Contains(string(data), "startup-smoke request") || !strings.Contains(string(data), "shutdown-smoke complete") {
		t.Fatalf("startup/shutdown logs missing: %s, %v", data, err)
	}
	if err := os.Rename(dir, dir+"-closed"); err != nil {
		t.Fatalf("log handles remain open: %v", err)
	}
	if err := os.Rename(dir+"-closed", dir); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTrustedProxiesUsesConfiguredValues(t *testing.T) {
	got := resolveTrustedProxies(" 10.0.0.10, 172.18.0.0/16 ", "")
	expected := []string{"10.0.0.10", "172.18.0.0/16"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected configured proxies %v, got %v", expected, got)
	}
}
