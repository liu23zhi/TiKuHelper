package timer

import (
	"context"
	"strconv"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// StartTimer 启动定时器
func StartTimer(db *gorm.DB, es service.Elasticsearch, addresses []string) {
	if len(addresses) == 0 {
		return
	}

	c := cron.New(cron.WithSeconds())
	c.AddFunc("*/30 * * * * *", func() {
		syncElasticsearch(db, es)
	})
	c.Start()
}

func syncElasticsearch(db *gorm.DB, es service.Elasticsearch) {
	if db == nil || es == nil {
		return
	}

	var records []entity.Tiku
	if err := db.WithContext(dbqueue.WithPriority(context.Background(), dbqueue.PriorityLow)).Find(&records).Error; err != nil {
		return
	}
	for _, v := range records {
		err := es.Create("tiku", strconv.Itoa(int(v.ID)), v)
		if err != nil {
			logger.SysError(err.Error())
			return
		}
	}
}
