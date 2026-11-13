package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// ticketAttachmentSchemaReadyKey 标记「ticket_attachments 表已就绪」。
	ticketAttachmentSchemaReadyKey = "ticket_attachment_schema_ready"

	// ticketAttachmentScanMarkedByColumn 是区分「真敏感」与「额度耗尽兜底」的列名。
	ticketAttachmentScanMarkedByColumn = "scan_marked_by"
)

// ticketAttachmentScanMarkedBySQLiteDDL 是 SQLite 上加 scan_marked_by 列用的裸 DDL。
//
// 为什么显式 ALTER 而不是让 entity 字段靠 AutoMigrate 补（与 blob_sha256 同一条纪律）：
// ensureTicketAttachmentSchema 有一次性标记，表建过一次之后永不重跑，所以**存量库
// 永远拿不到新列** —— 只有列检测型的显式 ALTER 才能补上。
//
// NOT NULL DEFAULT 'upstream' 是 121 决策 5 的硬要求（旧代码 INSERT 不带这一列），
// 同时承担存量行的语义：迁移前 scan_state 已是 sensitive 的行只能按「上游判定」
// 对待，反过来默认成 quota 等于把历史真违规降级成「未检测」。
//
// 长度与实体 tag 一致（varchar(32)）；SQLite 不区分 varchar 长度，但两边必须同源，
// 否则 MySQL 上实体与列定义会漂移。
const ticketAttachmentScanMarkedBySQLiteDDL = "`" + ticketAttachmentScanMarkedByColumn +
	"` varchar(32) NOT NULL DEFAULT '" + entity.TicketAttachmentMarkedByUpstream + "'"

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

const (
	// ticketAttachmentRetentionColumnReadyKey 标记「ticket_attachments.purged_at 已就位」。
	//
	// ⚠️ 必须与 ticketAttachmentSchemaReadyKey **分开**：那个标记在既有部署里早就写过了
	// （ensureTicketAttachmentSchema 是一次性标记），所以把新列塞进那条 AutoMigrate
	// 对现网完全无效 —— 老库会一直缺 purged_at，而缺列的表现是「工单图片保留清理
	// 任务每一轮都静默跳过」（service.ticketAttachmentPurgedAtReady 返回 false），
	// 也就是「功能看起来上线了但一张图都不会被清理」。这正是本仓反复踩过的
	// 「代码都在、只差接线」形态，因此这里用独立标记 + 独立显式 ALTER。
	ticketAttachmentRetentionColumnReadyKey = "ticket_attachment_retention_column_schema_ready"

	// ticketAttachmentPurgedAtColumn 是保留策略用来标记「已清理」的列名。
	ticketAttachmentPurgedAtColumn = "purged_at"
)

// ticketAttachmentPurgedAtSQLiteDDL 是 SQLite 上加 purged_at 列用的裸 DDL。
//
// 与 blob_sha256 的处理同构（见 ticketAttachmentBlobLinkSQLiteDDL 的注释）：
// SQLite 驱动的 AddColumn 在部分版本上会走整表 copy-rename，而
// `ticket_attachments` 的密文列可能有几十 KB × 几万行 —— 一次整表复制就是
// 长时间的写锁与磁盘翻倍。因此 SQLite 走裸 `ALTER TABLE ... ADD COLUMN`。
//
// 列**可空、无默认值**：NULL 就是「未清理」的权威语义，用空串/零值当哨兵
// 会和「残缺行」混在一起（见 entity.TicketAttachment.PurgedAt 的三态注释）。
const ticketAttachmentPurgedAtSQLiteDDL = "`" + ticketAttachmentPurgedAtColumn + "` datetime"

// EnsureTicketAttachmentRetentionSchema 给 ticket_attachments 补 purged_at 列。
//
// # 为什么单独一个迁移函数
//
// 用户 2026-11-13 决策「清理逻辑参考日志文件，按时间 + 按大小清理」。
// 清理的判据是 `purged_at IS NULL`，缺这列则清理任务与展示层都无法区分
// 「按策略清理过的行」与「加密事务崩在中间的残缺行」，前端也就无法给出
// 「图片已按保留策略清理」这种可解释的占位提示。
//
// 三条纪律（与 ticket_type_migration.go 的 applyTicketTypeSchema 一致）：
//   - 显式 ALTER 而不是依赖 AutoMigrate：AutoMigrate 被一次性标记挡住，
//     对既有库不会执行（见 ticketAttachmentRetentionColumnReadyKey 的注释）；
//   - 结构性失败 → 返回 error，让启动 FATAL 看见（缺列 = 清理功能完全静默失效）；
//   - HasColumn 幂等，不做任何 DROP。
//
// 不做数据回填：新列的含义是「什么时候被清理的」，迁移前不存在「已经清理过」
// 的历史数据（清理功能本身就随本迁移上线），NULL 就是正确的初值。
func EnsureTicketAttachmentRetentionSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		// 空库首次启动：附件表还没建出来，交给 ensureTicketAttachmentSchema。
		// 那个函数用 AutoMigrate 建表时会**带上** PurgedAt（实体已有字段），
		// 因此这里直接返回不会漏列。
		return nil
	}
	if err := addTicketAttachmentPurgedAtColumn(db); err != nil {
		return err
	}
	if systemSettingFlagIsSet(db, ticketAttachmentRetentionColumnReadyKey) {
		return nil
	}
	if err := setSystemSettingFlag(db, ticketAttachmentRetentionColumnReadyKey); err != nil {
		// 标记写失败不算迁移失败：列已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入工单图片保留策略迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// addTicketAttachmentPurgedAtColumn 给 ticket_attachments 补 purged_at 列。
//
// SQLite 走裸 DDL、MySQL/MariaDB 走 `Migrator().AddColumn`：MySQL 的 AddColumn
// 是原生 `ALTER TABLE ... ADD COLUMN`（代价小），而 SQLite 的 AddColumn 在部分
// 版本上会整表复制（见 ticketAttachmentPurgedAtSQLiteDDL 的注释）。
func addTicketAttachmentPurgedAtColumn(db *gorm.DB) error {
	if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentPurgedAtColumn) {
		return nil
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		statement := "ALTER TABLE " + entity.TableNameTicketAttachment + " ADD COLUMN " + ticketAttachmentPurgedAtSQLiteDDL
		if err := db.Exec(statement).Error; err != nil {
			if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentPurgedAtColumn) {
				return nil
			}
			return fmt.Errorf("新增 %s.%s 失败: %w", entity.TableNameTicketAttachment, ticketAttachmentPurgedAtColumn, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上 %s 列", entity.TableNameTicketAttachment, ticketAttachmentPurgedAtColumn))
		return nil
	}
	if err := db.Migrator().AddColumn(&entity.TicketAttachment{}, "PurgedAt"); err != nil {
		if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentPurgedAtColumn) {
			return nil
		}
		return fmt.Errorf("新增 %s.%s 失败: %w", entity.TableNameTicketAttachment, ticketAttachmentPurgedAtColumn, err)
	}
	return nil
}

// EnsureTicketAttachmentScanMarkedBySchema 给 ticket_attachments 补 scan_marked_by 列。
//
// # 这一列解决什么问题
//
// 额度耗尽 + 缓存未命中时，service 把附件标成 scan_state="sensitive"（与上游
// 真判定的值**完全相同**，见 runSensitiveImageDetection 的额度分支）。管理员在
// 「全部检测记录」里看到一批 sensitive 时分不清：
//
//   - 上游判违规 → 不该放行、要留存证据；
//   - 额度耗尽兜底 → 从未检测过，补额度后重检即可，本该是正常图片。
//
// 两者的处置动作完全相反。scan_marked_by 把「谁写的这个值」显式记下来。
//
// # 纪律（沿用 client_kind_migration 的列检测型，121 §5 决策 1 的 A 规则）
//
//   - db == nil 直接返回（导出函数可能被测试以 nil 调用）；
//   - 先 HasTable 再 HasColumn —— 双重检查，任一不满足都不重复加列；
//   - **不写 system_settings 标记**：本迁移没有任何数据回填，重复执行只是一次
//     HasColumn 元数据查询，代价可忽略；而标记型会在「标记已写但列被人为删掉」时
//     永久漏建。结构存在即完成。
//   - 结构性失败返回错误，让启动 FATAL 看见 —— 缺列会让「区分真敏感与额度兜底」
//     这个功能 100% 失效且无报错（这正是 121 §5 决策 2 说的「更难发现」那类故障）。
//   - 只建列不回填：DEFAULT 'upstream' 已让存量行落安全值，无需 UPDATE 全表。
func EnsureTicketAttachmentScanMarkedBySchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("工单附件扫描来源列迁移异常: %v", recovered)
		}
	}()
	if !db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		// 极端空库首次启动：附件表还没建出来（交给 ensureTicketAttachmentSchema），
		// 下次启动自然补齐。绝不在此建表。
		return nil
	}
	if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentScanMarkedByColumn) {
		return nil
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		statement := "ALTER TABLE " + entity.TableNameTicketAttachment + " ADD COLUMN " + ticketAttachmentScanMarkedBySQLiteDDL
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动或方言差异：列已经在了就当成功。
			if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentScanMarkedByColumn) {
				return nil
			}
			return fmt.Errorf("新增 %s.%s 失败: %w", entity.TableNameTicketAttachment, ticketAttachmentScanMarkedByColumn, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上 %s 列（取值 %s / %s，默认 %s）",
			entity.TableNameTicketAttachment, ticketAttachmentScanMarkedByColumn,
			entity.TicketAttachmentMarkedByUpstream, entity.TicketAttachmentMarkedByQuota,
			entity.TicketAttachmentMarkedByUpstream))
		return nil
	}
	// MySQL / MariaDB：列类型与默认值由 entity.TicketAttachment 的 gorm tag 提供。
	// ⚠️ 这里借用的就是主实体字段：本列已加进 TicketAttachment，AddColumn 的
	// LookUpField 能命中；不要另建影子结构（client_kind 那种影子结构只是因为
	// entity.Log 是生成文件不能改）。
	if err := db.Migrator().AddColumn(&entity.TicketAttachment{}, "ScanMarkedBy"); err != nil {
		if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentScanMarkedByColumn) {
			return nil
		}
		return fmt.Errorf("新增 %s.%s 失败: %w", entity.TableNameTicketAttachment, ticketAttachmentScanMarkedByColumn, err)
	}
	return nil
}
