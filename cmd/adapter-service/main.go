package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	// 时间一律固定上海（见 timezone.go）。
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
	// 工单图片的敏感检测：上传只标 pending，检测在此异步完成（上游故障/无密钥 → fail-closed）。
	ticketAttachmentWorkerDone := service.StartTicketAttachmentScanWorker(shutdownContext, mg.GetDB())
	scriptEscrowWorkerDone := service.StartScriptEscrowCloseWorker(shutdownContext, mg.GetDB())

	// ── 第 8 轮新增的三个后台任务 ───────────────────────────────────────────
	//
	// 三者的共同点：service 层实现完整、有单测，但**没有任何生产调用方**时
	// 功能等于不存在，且不报错。这类「代码在、没人调」是本项目反复出现的形态，
	// 所以这里逐条注明「不接线的可见后果」。
	//
	// 日志文件清理（Q14：30 天 / 10 GiB / 单文件 100 MiB）。
	// interval 传 0 = **每轮从 system_settings 重新解析**，管理员改「清理间隔」
	// 后不用重启即可生效（用 Timer 而非 Ticker 的全部理由）。传固定值会把周期焊死。
	stopLogRetention := service.StartLogRetentionCleanup(mg.GetDB(), 0)
	defer stopLogRetention()

	// 工单图片清理（T-11：按时间 180 天 + 按总占用 20 GiB）。
	// 与日志清理的一处刻意差异：启动后**先立即跑一轮**再进入定时 —— 服务重启往往
	// 发生在长时间停机之后，正是积压最多的时刻，等一个周期会让积压继续存在一小时。
	stopTicketImageRetention := service.StartTicketAttachmentRetentionCleanup(mg.GetDB(), 0)
	defer stopTicketImageRetention()

	// 工单图片 blob 的延迟删除（T-13）：ref_count 归零的行先留着，过了宽限期才真删。
	// 不跑这个 worker 的后果是 blob 只增不减（上传能成功、删除只递减计数），
	// 磁盘会缓慢占满而没有任何报错。
	ticketBlobPurgeDone := service.StartTicketAttachmentBlobPurgeWorker(shutdownContext, mg.GetDB())

	// 服务状态采样（T-18）：纯内存 1440 槽环形缓冲，每分钟一格。
	// 不启动的后果是「状态页永远空」—— points 是 [] 而不是 1440 个零点，
	// 界面上看是「暂无数据」而不是异常，容易长期不被发现。
	service.StartServiceStatusSampling(controller.ServiceStatusSampler(), shutdownContext.Done())

	// 风控判定定时任务（96 §2 / 118 §2.4 的全部行为与欠款规则）。
	//
	// ⚠️ 这一行是整条风控链路的**唯一驱动器**。在它出现之前，
	// service.EvaluateDebtAlerts 有完整实现、有 58 条单测，但**零生产调用方** ——
	// 全仓 `git grep EvaluateDebtAlerts` 排除 _test.go 后只剩定义行与注释。
	// 后果是静默且彻底的：
	//   · risk_warnings 表永远为空 ⇒ 管理页恒显示「暂无告警」
	//   · R1 multi_account_ip / R8 ticket_flood / R11 abnormal_register_pattern /
	//     debt_user / debt_batch / debt_system **一条规则都不会触发**
	//   · 单测全绿、编译通过、启动无报错、日志无异常
	//
	// 判据纪律：**判断某功能是否完成，查调用方，不查定义。** 这个仓已经在这上面
	// 栽过两次（router.go 一次积压 33 个未挂 handler；三个后台组件各只有 2 处命中
	// 且都在注释里）。
	//
	// interval 传 0 = 每轮从 system_settings 重新读间隔，运营改完不用重启
	// （与日志清理同一条取舍，用 Timer 而非 Ticker 的全部理由）。
	stopRiskEvaluation := service.StartRiskAlertEvaluationWorker(mg.GetDB(), 0)
	defer stopRiskEvaluation()

	// 异步任务管理器（Q12 / N3，导入导出）：必须同时 Start() 与注入 controller，
	// 否则 /export/tasks 提交接口返回 503（刻意不假装成功）。
	// BaseDir 用系统临时目录下的固定子目录：结果文件是可下载的临时产物，
	// 不应与数据库或配置放在一起。
	asyncTaskBaseDir := filepath.Join(os.TempDir(), "tiku-adapter-async-tasks")
	asyncTaskManager := service.NewAsyncTaskManager(mg.GetDB(), asyncTaskBaseDir)
	asyncTaskManager.RegisterDefaultAsyncTaskExecutors()
	// 上次进程退出时停在 running 的任务不会自己恢复，必须显式收敛成 failed，
	// 否则它们会永远占着「运行中」的状态（前端一直转圈）。
	if err := asyncTaskManager.RecoverInterruptedTasks(); err != nil {
		logger.SysWarn("恢复中断的异步任务失败（不影响启动，相关任务会保持 interrupted 状态）：" + err.Error())
	}
	asyncTaskManager.Start()
	controller.SetAsyncTaskManager(asyncTaskManager)
	defer asyncTaskManager.Stop()

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
	<-ticketAttachmentWorkerDone
	<-scriptEscrowWorkerDone
	<-ticketBlobPurgeDone
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
		// 敏感词打标是旁路任务：它从不参与答题，但必须在日志/DB 关闭前排空，
		// 否则最后几行标记会写到半个库上。同一份 10s 预算，失败只记日志。
		if err := controller.WaitForSensitiveWordChecks(ctx); err != nil {
			logger.SysError("敏感词打标任务尚未完成，服务关闭预算已结束")
		}
		if err := controller.WaitForSearchBillingRetries(ctx); err != nil {
			logger.SysError("搜题待结算重试尚未排空，服务关闭预算已结束；内存队列不保证跨进程恢复")
		}
	}()
	return shutdownDone
}
