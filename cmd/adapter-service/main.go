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
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/api"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/timer"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

//go:embed dist
var buildFS embed.FS

//go:embed dist/index.html
var indexPage []byte

func main() {
	flag.Parse()
	// 时间一律固定上海（见 timezone.go 与 AGENTS.md 第 26 条）。
	useShanghaiTime()
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
	trustedProxies, err := loadTrustedProxies()
	if err != nil {
		logger.FatalLog(err)
	}
	if err := server.SetTrustedProxies(trustedProxies.List); err != nil {
		logger.FatalLog(err)
	}
	logTrustedProxies(trustedProxies)
	api.SetAPIRouter(server)
	api.SetWebRouter(buildFS, indexPage, server)
	stopWechatRegisterCleanup := service.DefaultWechatRegisterFlowStore.StartCleanup(time.Minute)
	defer stopWechatRegisterCleanup()
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
	notificationWorkerDone := service.StartTicketNotificationWorker(shutdownContext, mg.GetDB())
	shutdownDone := shutdownHTTPServerOnCancel(shutdownContext, httpServer)
	logger.SysLog("Tiku 服务启动 address=" + httpServer.Addr)
	logTimeBase()
	err = httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.FatalLog(err)
	}
	stop()
	<-shutdownDone
	<-notificationWorkerDone
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
