// 临时验证程序：用真实启动迁移路径连远端 MySQL，确认结构/回填迁移可直接跑通。
// 验证完成后删除，不进入提交。
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

func main() {
	time.Local = logger.ShanghaiLocation()
	dsn := os.Getenv("VERIFY_MYSQL_DSN")
	if dsn == "" {
		fmt.Println("缺少 VERIFY_MYSQL_DSN")
		os.Exit(2)
	}
	if err := logger.SetupGinLog(); err != nil {
		fmt.Println("日志初始化失败（继续）：", err)
	}

	started := time.Now()
	db := registry.RegisterDB(configs.Config{Mysql: dsn})
	fmt.Printf("RegisterDB 完成 dialector=%s 耗时=%s\n", db.Dialector.Name(), time.Since(started).Round(time.Millisecond))

	var users, tiku, billing, logs int64
	db.Model(&entity.User{}).Count(&users)
	db.Model(&entity.Tiku{}).Count(&tiku)
	db.Model(&entity.BillingRecord{}).Count(&billing)
	db.Model(&entity.Log{}).Count(&logs)
	fmt.Printf("计数 users=%d tiku=%d billing_records=%d logs=%d\n", users, tiku, billing, logs)

	type marker struct {
		Key   string `gorm:"column:key"`
		Value string `gorm:"column:value"`
	}
	var markers []marker
	db.Raw("SELECT `key`, `value` FROM system_settings WHERE `key` LIKE 'migration%' OR `key` LIKE '%migrat%' ORDER BY `key`").Scan(&markers)
	fmt.Printf("迁移标记 %d 条:\n", len(markers))
	for _, item := range markers {
		fmt.Printf("  %s = %s\n", item.Key, item.Value)
	}

	var maxQuestionText int
	db.Raw("SELECT COALESCE(MAX(CHAR_LENGTH(question_text)),0) FROM billing_records").Scan(&maxQuestionText)
	fmt.Printf("question_text 最长字符数=%d\n", maxQuestionText)

	var globalMultiplier float64
	db.Raw("SELECT COALESCE(MAX(global_billing_multiplier),0) FROM ai_search_configs").Scan(&globalMultiplier)
	fmt.Printf("ai_search_configs.global_billing_multiplier=%v\n", globalMultiplier)

	if err := registry.CloseDB(); err != nil {
		fmt.Println("关闭数据库失败：", err)
		os.Exit(1)
	}
	fmt.Println("验证完成：真实启动迁移链路在本库上跑通")
}
