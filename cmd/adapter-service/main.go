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
	"github.com/itihey/tikuAdapter/internal/controller"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
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
	// Initialize once after timezone setup, before manager/server goroutines.
	diagnostics.Default = diagnostics.NewCollector(nil)
	defer func() {
		if value := recover(); value != nil {
			logger.PanicLog(value, string(debug.Stack()))
			_ = logger.Close()
			panic(value)
		}
		_ = logger.Close()
	}()
	trustedProxies, err := loadTrustedProxies()
	if err != nil {
		logger.FatalLog(err)
	}
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
	if err := server.SetTrustedProxies(trustedProxies.List); err != nil {
		logger.FatalLog(err)
	}
	logTrustedProxies(trustedProxies)
	api.SetAPIRouter(server)
	healthDB, err := mg.GetDB().DB()
	if err != nil {
		logger.FatalLog(err)
	}
	api.SetHealthRouter(server, healthDB.PingContext)
	api.SetWebRouter(buildFS, indexPage, server)
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	extensionStore, err := service.DefaultExtensionStoreWithDB(mg.GetDB())
	if err != nil {
		logger.FatalLog(err)
	}
	extensionDone := startExtensionUpdates(shutdownContext, extensionStore.Run, func(source string, updateErr error) {
		if shutdownContext.Err() != nil {
			return
		}
		if updateErr != nil && !errors.Is(updateErr, service.ErrExtensionUnavailable) {
			logger.SysError("扩展更新检查失败 source=" + source + " error=" + updateErr.Error())
			return
		}
		if updateErr == nil {
			logger.SysLog("扩展更新检查完成 source=" + source)
		}
	})
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
	notificationWorkerDone := service.StartTicketNotificationWorker(shutdownContext, mg.GetDB())
	scriptEscrowWorkerDone := service.StartScriptEscrowCloseWorker(shutdownContext, mg.GetDB())
	// Retry settlement stays alive until admitted producers finish within the same shutdown budget.
	billingRetryContext, cancelBillingRetries := context.WithCancel(context.Background())
	defer cancelBillingRetries()
	billingRetryWorkerDone := controller.StartSearchBillingRetryWorker(billingRetryContext)
	shutdownDone := shutdownHTTPServerOnCancel(shutdownContext, httpServer)
	logger.SysLog("Tiku 服务启动 address=" + httpServer.Addr)
	logTimeBase()
	err = httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.FatalLog(err)
	}
	stop()
	<-shutdownDone
	cancelBillingRetries()
	<-extensionDone
	<-notificationWorkerDone
	<-scriptEscrowWorkerDone
	<-billingRetryWorkerDone
	logger.SysLog("Tiku 服务已停止")
}

func startExtensionUpdates(ctx context.Context, run func(context.Context, func(string, error)), report func(string, error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, report)
	}()
	return done
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
		// Detached HTTP observers no longer hold their admitted producer open in
		// Shutdown. Drain those owners before DB/log close, within this same 10s.
		if err := controller.WaitForSearchExecutions(ctx); err != nil {
			logger.SysError("搜题已接纳执行尚未完成，服务关闭预算已结束")
		}
		if err := controller.WaitForSearchBillingRetries(ctx); err != nil {
			logger.SysError("搜题待结算重试尚未排空，服务关闭预算已结束；内存队列不保证跨进程恢复")
		}
	}()
	return shutdownDone
}
