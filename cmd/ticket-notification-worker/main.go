package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

func main() {
	flag.Parse()
	if location, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		time.Local = location
	}
	if err := logger.SetupGinLog(); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
	}
	defer func() { _ = logger.Close() }()

	config := registry.Config()
	db := registry.RegisterDB(config)
	defer func() {
		if err := registry.CloseDB(); err != nil {
			logger.SysError("通知 worker 关闭数据库失败：" + err.Error())
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.SysLog("工单通知 worker 已启动")
	service.RunTicketNotificationWorker(ctx, db)
	logger.SysLog("工单通知 worker 已停止")
}
