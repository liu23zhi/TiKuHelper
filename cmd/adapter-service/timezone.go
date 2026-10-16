package main

import (
	"fmt"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

// useShanghaiTime 把整个进程的时区固定为 Asia/Shanghai。
//
// 为什么必须显式固定：容器基础镜像默认 TZ=UTC，Gin 的访问日志（gin.Logger 用
// time.Now()）和 GORM 的时间写入都会变成 UTC，而业务日志固定用 +8，
// 同一个请求在同一个日志文件里差 8 小时（线上实测 12:59:20 vs 04:59:20）。
//
// 影响范围与注意点：
//   - Gin 访问日志、GORM 自动时间戳（created_at 等）、任何 time.Now() 都变成 +8；
//   - 历史行里已写入的时间自带偏移（东八区或 UTC 标记），读取时按原偏移解析，
//     因此不会因为这次改动发生位移；
//   - 不要再把它改回 UTC，也不要依赖容器 TZ。见 AGENTS.md 第 26 条。
func useShanghaiTime() {
	time.Local = logger.ShanghaiLocation()
}

// logTimeBase 把生效的时间基准写进启动日志，便于核对。
func logTimeBase() {
	now := time.Now()
	name, offset := now.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	logger.SysLog(fmt.Sprintf("时间基准 location=%s zone=%s offset=%s%02d:%02d",
		now.Location(), name, sign, offset/3600, offset%3600/60))
}
