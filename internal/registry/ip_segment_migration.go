package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const ipSegmentSchemaReadyKey = "ip_segment_schema_ready"

// ensureIPSegmentSchema 建立 ip_segments 表（task_doc/71）。
//
// 这张表让「一次上游查询覆盖一整段」真正生效：段内后续 IP 由区间查询命中，
// 不再各自调一次计费接口。
//
// 纪律（与 ensureReferralSchema 同构）：
//   - 建表失败 → 返回错误，让启动 FATAL 看见（表缺失会让段命中永久失效）；
//   - 只建结构、不写数据，因此没有「回填失败」这一层；
//   - 用 system_settings 标记保证只跑一次，失败不写标记；
//   - 不做任何 DROP TABLE。
//
// 注意：这里**不需要**给已有行回填任何字段 —— 段表是全新表，没有历史行。
// 段数据在运行时由 PersistIPSegmentsFromCacheRows 自然累积。
func ensureIPSegmentSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("IP 段归属迁移异常: %v", recovered)
		}
	}()
	if systemSettingFlagIsSet(db, ipSegmentSchemaReadyKey) {
		return nil
	}
	if err := db.AutoMigrate(&entity.IPSegment{}); err != nil {
		return fmt.Errorf("建立 IP 段归属表失败: %w", err)
	}
	if err := setSystemSettingFlag(db, ipSegmentSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：表已经建好，下次启动重跑 AutoMigrate 是幂等的。
		logger.SysLog(fmt.Sprintf("写入 IP 段归属迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}
