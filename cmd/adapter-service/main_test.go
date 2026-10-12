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

	"github.com/itihey/tikuAdapter/pkg/logger"
)

func TestResolveTrustedProxiesTrustsAllWhenEmpty(t *testing.T) {
	got := resolveTrustedProxies("")
	expected := []string{"0.0.0.0/0", "::/0"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected trust-all proxies %v, got %v", expected, got)
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
	got := resolveTrustedProxies(" 10.0.0.10, 172.18.0.0/16 ")
	expected := []string{"10.0.0.10", "172.18.0.0/16"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected configured proxies %v, got %v", expected, got)
	}
}
