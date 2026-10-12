package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/api"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/timer"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

//go:embed dist
var buildFS embed.FS

//go:embed dist/index.html
var indexPage []byte

func main() {
	flag.Parse()
	defer func() {
		if value := recover(); value != nil {
			logger.PanicLog(value, string(debug.Stack()))
			_ = logger.Close()
			panic(value)
		}
		_ = logger.Close()
	}()
	mg, err := manager.CreateManager()
	if err != nil {
		logger.FatalLog(err)
	}
	defer func(mg *manager.Manager) {
		err := mg.CloseManager()
		if err != nil {
			logger.FatalLog(err)
		}
	}(mg)

	server := gin.Default()
	trustedProxies := resolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err := server.SetTrustedProxies(trustedProxies); err != nil {
		logger.FatalLog(err)
	}
	api.SetAPIRouter(server)
	api.SetWebRouter(buildFS, indexPage, server)
	timer.StartTimer(mg.GetDB(), mg.GetEs(), mg.GetConfig().Elasticsearch.Addresses)
	httpServer := &http.Server{
		Addr:              "0.0.0.0:8060",
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      210 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := shutdownHTTPServerOnCancel(shutdownContext, httpServer)
	logger.SysLog("Tiku 服务启动 address=" + httpServer.Addr)
	err = httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.FatalLog(err)
	}
	stop()
	<-shutdownDone
	logger.SysLog("Tiku 服务已停止")
}

func shutdownHTTPServerOnCancel(shutdownContext context.Context, httpServer *http.Server) <-chan struct{} {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-shutdownContext.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.SysError("HTTP 服务关闭失败：" + err.Error())
			_ = httpServer.Close()
		}
	}()
	return shutdownDone
}

func resolveTrustedProxies(configured string) []string {
	if strings.TrimSpace(configured) == "" {
		return []string{"0.0.0.0/0", "::/0"}
	}
	trustedProxies := make([]string, 0)
	for _, item := range strings.Split(configured, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			trustedProxies = append(trustedProxies, trimmed)
		}
	}
	return trustedProxies
}
