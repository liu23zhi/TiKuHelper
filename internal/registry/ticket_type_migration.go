package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// ticketTypeColumnsReadyKey 标记「tickets 的 origin / category / context 三列已就位」。
	ticketTypeColumnsReadyKey = "ticket_type_columns_schema_ready"
	// ticketTypeOriginBackfilledKey 标记「存量工单的 origin/category 已回填」。
	//
	// 与结构标记分开的原因：结构建好不等于数据回填完。回填失败时不能写这个标记，
	// 只能下次启动重试 —— 否则「旧单全部漏出用户列表」会永久固化。
	ticketTypeOriginBackfilledKey = "ticket_type_origin_backfilled"
	// ticketRefundTicketIDReadyKey 标记「ticket_refunds.ticket_id 关联列已就位」。
	ticketRefundTicketIDReadyKey = "ticket_refund_ticket_id_schema_ready"
	// ticketRefundBackfilledKey 标记「存量退款已补出壳工单并建立关联」。
	//
	// 与结构标记分开：结构建好不等于历史退款都能在工单列表里看到。
	// 回填失败时不写标记，下次启动重试。
	ticketRefundBackfilledKey = "ticket_refund_ticket_backfilled"
)

// ensureTicketTypeSchema 给 tickets 补上多类型三维度所需的 origin / category / context 三列，
// 并把存量行回填成「普通用户工单」（69-*.md §3.1 / §3.2 / §七.5）。
//
// # 为什么必须有存量回填
//
// 列上的 `default:user` / `default:general` **只作用于 INSERT**：老库 ALTER 出来的行
// 可能是空串（不同方言/存储引擎对「ADD COLUMN 是否把默认值写进已有行」的行为并不一致，
// 也挡不住其它代码路径的裸 SQL 插入）。不回填的话：
//   - 按 origin 分类展示/统计的老工单全部落到「未知来源」；
//   - 任何将来按 `origin = 'user'` 取「用户主动发起」的判定都会漏掉全部历史单。
//
// 本仓的真实判据是「回填后 `COUNT(*) WHERE origin='user'` 等于存量行数」，
// 迁移测试里删掉回填这一步必须变红。
//
// # 纪律（改这里之前先读）
//
//   - 结构性失败 → 返回错误，让启动 FATAL 看见（缺列会让 origin 过滤静默漏单）；
//   - 数据类回填失败 → 只记日志、**不写标记**，下次启动重试；
//   - HasColumn 幂等：每次都核对三列，老库补列、新库跳过；
//   - 两个一次性标记（结构 / 回填）互不复用；
//   - 不做任何 DROP TABLE。
func ensureTicketTypeSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameTicket) {
		// 空库首次启动：tickets 还没建出来，交给后续 AutoMigrate，下次启动重试。
		return nil
	}

	// ① 结构性部分：显式 ALTER 补三列（幂等，每次都核）。
	applied, err := safeEnsureTicketTypeSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}

	// ② 数据类部分：回填存量行。失败只记日志，不写标记（下次启动重试）。
	if !systemSettingFlagIsSet(db, ticketTypeOriginBackfilledKey) {
		if err := backfillTicketTypeOrigin(db); err != nil {
			logger.SysLog(fmt.Sprintf("工单 origin/category 存量回填失败（下次启动会重试）：%v", err))
			return nil
		}
		if err := setSystemSettingFlag(db, ticketTypeOriginBackfilledKey); err != nil {
			logger.SysLog(fmt.Sprintf("写入工单 origin 回填标记失败（下次启动会重跑，幂等）：%v", err))
		}
	}

	if systemSettingFlagIsSet(db, ticketTypeColumnsReadyKey) {
		return nil
	}
	if err := setSystemSettingFlag(db, ticketTypeColumnsReadyKey); err != nil {
		// 标记写失败不算迁移失败：列已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入工单多类型迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// ensureTicketRefundTicketSchema 给 ticket_refunds 补上指向壳工单的 ticket_id，
// 并给**存量退款**补建壳工单（T-209：退款工单合并进工单列表）。
//
// # 为什么必须回填存量
//
// 用户要求「退款工单也显示在工单列表」。只加列不回填的话，历史退款在列表里
// 一条都看不到 —— 那正是本次要消除的现象。回填把每一笔**还没有壳工单**的退款
// 补成一张 `category=refund` 的工单并写回 ticket_id。
//
// # 纪律（与 applyTicketTypeSchema 一致）
//
//   - 结构失败 → 返回错误（缺列会让退款全部不出现在列表里，属静默漏单）；
//   - 回填失败 → 只记日志、**不写标记**，下次启动重试（回填是幂等的：
//     只处理 ticket_id IS NULL 的行）；
//   - 只做 ADD COLUMN 与 INSERT，**不删不改任何退款业务字段** ——
//     退款状态机、金额、ledger 一律不动，所以回滚只需 DROP 这一列与停用壳工单；
//   - 不回填 `tickets` 的 origin/category 已有数据的行（那属于上一个迁移）。
func ensureTicketRefundTicketSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable("ticket_refunds") || !db.Migrator().HasTable(entity.TableNameTicket) {
		// 空库首次启动：表还没建出来，交给后续 AutoMigrate，下次启动重试。
		return nil
	}

	applied, err := safeEnsureTicketRefundTicketSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}

	if !systemSettingFlagIsSet(db, ticketRefundBackfilledKey) {
		if err := backfillTicketRefundTickets(db); err != nil {
			logger.SysLog(fmt.Sprintf("退款壳工单存量回填失败（下次启动会重试）：%v", err))
			return nil
		}
		if err := setSystemSettingFlag(db, ticketRefundBackfilledKey); err != nil {
			logger.SysLog(fmt.Sprintf("写入退款壳工单回填标记失败（下次启动会重跑，幂等）：%v", err))
		}
	}

	if systemSettingFlagIsSet(db, ticketRefundTicketIDReadyKey) {
		return nil
	}
	if err := setSystemSettingFlag(db, ticketRefundTicketIDReadyKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入退款关联列迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureTicketRefundTicketSchema 外层兜 recover：脏数据不能让服务反复崩溃。
func safeEnsureTicketRefundTicketSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyTicketRefundTicketSchema(db)
}

// applyTicketRefundTicketSchema 用显式 ALTER 补 ticket_id 列 + 唯一索引。
//
// 列定义必须与 entity.TicketRefund 的 gorm tag 一致（bigint + 可空 + 唯一索引），
// 否则 AutoMigrate 每次启动都会尝试改列。可空是刻意的：未关联的退款可以有多个
// （唯一索引允许多个 NULL），已关联的保证一笔退款只挂一张单。
func applyTicketRefundTicketSchema(db *gorm.DB) (bool, error) {
	const table = "ticket_refunds"
	if !db.Migrator().HasColumn(table, "ticket_id") {
		// 不带 NOT NULL / DEFAULT：老行在补列瞬间是 NULL，正是「未关联」这个语义。
		statement := "ALTER TABLE `" + table + "` ADD COLUMN `ticket_id` bigint"
		if err := db.Exec(statement).Error; err != nil {
			if !db.Migrator().HasColumn(table, "ticket_id") {
				return false, fmt.Errorf("add %s.ticket_id fail: %w", table, err)
			}
		} else {
			logger.SysLog(fmt.Sprintf("已为 %s 补上 ticket_id 列", table))
		}
	}
	if err := ensureTicketRefundTicketIndex(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureTicketRefundTicketIndex 补建 ticket_id 的唯一索引（与 GORM 从 tag 推导的名字一致）。
func ensureTicketRefundTicketIndex(db *gorm.DB) error {
	const (
		table = "ticket_refunds"
		name  = "idx_ticket_refunds_ticket_id"
	)
	if db.Migrator().HasIndex(table, name) {
		return nil
	}
	dialect := strings.ToLower(strings.TrimSpace(db.Dialector.Name()))
	var statement string
	switch dialect {
	case "sqlite":
		statement = fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s` ON `%s` (`ticket_id`)", name, table)
	case "mysql":
		statement = fmt.Sprintf("CREATE UNIQUE INDEX `%s` ON `%s` (`ticket_id`)", name, table)
	default:
		logger.SysLog(fmt.Sprintf("未知数据库方言 %q，跳过 %s 索引 %s", db.Dialector.Name(), table, name))
		return nil
	}
	if err := db.Exec(statement).Error; err != nil {
		if db.Migrator().HasIndex(table, name) {
			return nil
		}
		return fmt.Errorf("create index %s on %s fail: %w", name, table, err)
	}
	return nil
}

// backfillTicketRefundTickets 给「还没有壳工单」的存量退款补建工单并建立关联。
//
// # 幂等判据是 `ticket_id IS NULL`，不是「全部退款」
//
// 每轮只处理未关联的行，所以重跑安全（上一次中途崩了，下一次接着做）。
//
// # 为什么逐条而不是一条 INSERT ... SELECT
//
// 一致性优先：一条批量插入无法把新生成的 tickets.id 写回对应的 refunds 行
// （不同方言回填自增 ID 的语法不一致）。逐条在一个事务里
// 「插工单 → 更新 refunds.ticket_id」是最稳的，且回填量本来就不大
// （只影响迁移前的老数据）。
//
// # 壳工单的字段口径
//
//   - status 按退款当前状态映射成工单 4 态（见 ticketRefundShellStatus）；
//     映射是**展示口径**，退款真状态仍在 ticket_refunds.status；
//   - origin=user（发起人是用户本人），category=refund，
//     priority=normal（与 69-*.md §2.3 对 refund 的建议一致：常规业务）；
//   - subject 用可读中文，正文写一句来源说明 —— 管理员在列表里点开就知道
//     真正的操作要去退款详情做。
func backfillTicketRefundTickets(db *gorm.DB) error {
	var pending []entity.TicketRefund
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("ticket_id IS NULL").
		Order("id ASC").
		Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	var created int
	for _, refund := range pending {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return service.CreateTicketRefundShellInTx(tx, refund, time.Time{})
		}); err != nil {
			return err
		}
		created++
	}
	logger.SysLog(fmt.Sprintf("退款壳工单存量回填：为 %d 笔历史退款补建了工单", created))
	return nil
}

// safeEnsureTicketTypeSchema 外层兜一层 recover：脏数据不能让服务反复崩溃。
func safeEnsureTicketTypeSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyTicketTypeSchema(db)
}

// applyTicketTypeSchema 用**显式 ALTER** 补三列，不依赖 GORM AutoMigrate 的自动补列
// （AutoMigrate 在「模型被临时排除」或保留旧 schema 的路径上不保证执行，而这三列是
// 工单可见性的判据，缺一列就是静默漏单）。
//
// 列定义必须与 entity.Ticket 的 gorm tag 完全一致（varchar 长度 + NOT NULL + default），
// 否则 AutoMigrate 每次启动都会尝试改列默认值。
func applyTicketTypeSchema(db *gorm.DB) (bool, error) {
	ddl := []struct {
		name      string
		statement string
	}{
		{"origin", "ALTER TABLE `" + entity.TableNameTicket + "` ADD COLUMN `origin` varchar(16) NOT NULL DEFAULT 'user'"},
		{"category", "ALTER TABLE `" + entity.TableNameTicket + "` ADD COLUMN `category` varchar(32) NOT NULL DEFAULT 'general'"},
		{"context", "ALTER TABLE `" + entity.TableNameTicket + "` ADD COLUMN `context` text"},
	}
	for _, column := range ddl {
		if db.Migrator().HasColumn(entity.TableNameTicket, column.name) {
			continue
		}
		if err := db.Exec(column.statement).Error; err != nil {
			// 并发启动或方言差异：列已经在了就当成功。
			if db.Migrator().HasColumn(entity.TableNameTicket, column.name) {
				continue
			}
			return false, fmt.Errorf("add %s.%s fail: %w", entity.TableNameTicket, column.name, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上 %s 列", entity.TableNameTicket, column.name))
	}
	if err := ensureTicketTypeIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureTicketTypeIndexes 补建 origin / category 两条索引。
//
// 索引名与 GORM 从 `index` tag 推导出的名字一致（idx_tickets_origin / idx_tickets_category），
// 避免同一列上出现两条重复索引。SQLite 支持 CREATE INDEX IF NOT EXISTS；
// MySQL 不支持，必须先查再说（与 ensureUserBindingRecordIndexes 同构）。
func ensureTicketTypeIndexes(db *gorm.DB) error {
	columns := []string{"origin", "category"}
	dialect := strings.ToLower(strings.TrimSpace(db.Dialector.Name()))
	table := "`" + entity.TableNameTicket + "`"
	for _, column := range columns {
		name := "idx_" + entity.TableNameTicket + "_" + column
		if db.Migrator().HasIndex(entity.TableNameTicket, name) {
			continue
		}
		var statement string
		switch dialect {
		case "sqlite":
			statement = fmt.Sprintf("CREATE INDEX IF NOT EXISTS `%s` ON %s (`%s`)", name, table, column)
		case "mysql":
			statement = fmt.Sprintf("CREATE INDEX `%s` ON %s (`%s`)", name, table, column)
		default:
			logger.SysLog(fmt.Sprintf("未知数据库方言 %q，跳过 %s 索引 %s", db.Dialector.Name(), entity.TableNameTicket, name))
			continue
		}
		if err := db.Exec(statement).Error; err != nil {
			if db.Migrator().HasIndex(entity.TableNameTicket, name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", name, entity.TableNameTicket, err)
		}
	}
	return nil
}

// backfillTicketTypeOrigin 把「origin/category 为空」的存量工单回填成 user/general。
//
// 为什么用 UpdateColumn 而不是 Update：GORM 的 Update 会顺带刷新 updated_at，
// 那会把历史工单的「最近更新」时间和 updatedAt 区间的筛选结果一起改掉 ——
// 迁移只该补列值，不该伪造业务时间。
//
// 只有空值被改写：已经是 system / appeal 的行（新库才有）必须原样保留 ——
// 回填把 system 行写成 user 会让「系统自动创建」的标识和按 origin 的统计一起失真
// （系统单的隔离靠 user_id，不靠 origin；但 origin 仍是展示与分类的事实来源）。
func backfillTicketTypeOrigin(db *gorm.DB) error {
	originResult := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Ticket{}).
		Where("COALESCE(origin, '') = ''").
		UpdateColumn("origin", entity.TicketOriginUser)
	if originResult.Error != nil {
		return originResult.Error
	}
	categoryResult := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Ticket{}).
		Where("COALESCE(category, '') = ''").
		UpdateColumn("category", entity.TicketCategoryGeneral)
	if categoryResult.Error != nil {
		return categoryResult.Error
	}
	if originResult.RowsAffected > 0 || categoryResult.RowsAffected > 0 {
		logger.SysLog(fmt.Sprintf(
			"工单多类型存量回填：origin→user %d 行、category→general %d 行",
			originResult.RowsAffected, categoryResult.RowsAffected))
	}
	return nil
}
