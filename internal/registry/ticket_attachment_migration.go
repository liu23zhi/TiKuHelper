package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// ticketAttachmentSchemaReadyKey 标记「ticket_attachments 表已就绪」。
	ticketAttachmentSchemaReadyKey = "ticket_attachment_schema_ready"
)

// ensureTicketAttachmentSchema 建立工单图片附件表。
//
// 只建**新表**，不改 tickets / ticket_messages：工单文本与图片解耦，
// 老数据完全不受影响，也不需要回填（历史工单没有附件，读取时天然得到空列表）。
//
// 纪律：
//   - 结构性建表失败 → 返回错误，让启动 FATAL 看见；
//   - 本迁移没有数据回填；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动幂等重跑；
//   - 不做任何 DROP TABLE。
//
// 复杂度：一次 AutoMigrate + 一次标记读写；不扫任何业务表。
func ensureTicketAttachmentSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, ticketAttachmentSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureTicketAttachmentSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	if err := setSystemSettingFlag(db, ticketAttachmentSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入工单附件迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureTicketAttachmentSchema 外层兜一层 recover：脏数据不能让服务反复崩溃。
func safeEnsureTicketAttachmentSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	// 建表本身不依赖任何既有表（ticket_id 只是普通列，不建外键约束），
	// 所以这里无条件执行；只有「写标记」需要 system_settings 存在，
	// 而 setSystemSettingFlag 在缺表时返回错误，调用方只记日志。
	if err := db.AutoMigrate(&entity.TicketAttachment{}); err != nil {
		return false, fmt.Errorf("create ticket_attachments fail: %w", err)
	}
	return true, nil
}
