package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// ticketAttachmentBlobSchemaReadyKey 标记「blobs 表结构已就绪且数据已回填」。
	//
	// 命名按 121 §1.4 的 `_schema_v1` 后缀规律。
	//
	// ⚠️ 结构（建表 + 加列）与回填**共用一个标记**：本表的结构就位本身没有意义 ——
	// 建了表不回填，`ref_count` 全是 0，24 小时延迟清理会把刚建出来的行全删掉。
	// 因此「标记存在」必须等价于「结构与数据都完成」。
	ticketAttachmentBlobSchemaReadyKey = "ticket_attachment_blobs_v1"

	// ticketAttachmentBlobBackfillBatchSize 是回填时每批处理的唯一 sha256 个数。
	//
	// 与 ai_ocr_cache_migration.go:35 的 batchSize=500 一致：SQLite 上 500 行的
	// 单事务提交耗时在毫秒级，MySQL 上一次 500 行的 INSERT 也不会长到让人重启。
	ticketAttachmentBlobBackfillBatchSize = 500

	// ticketAttachmentBlobLinkColumn 是 ticket_attachments 上指向 blob 的列名。
	ticketAttachmentBlobLinkColumn = "blob_sha256"
)

// ticketAttachmentBlobLinkSQLiteDDL 是 SQLite 上加 blob_sha256 列用的裸 DDL。
//
// 为什么必须显式 ALTER 而不是依赖 AutoMigrate（121 §1.5）：
//   - 把字段加进 entity.TicketAttachment 会让 AutoMigrate 每次都试图改已有表，
//     而 SQLite 的 AlterColumn 是整表 copy-rename（2026-10-16 启动慢事故的根因）；
//   - 本仓约定「新列由显式 ensure*Schema 补」。
//
// `NOT NULL DEFAULT ”`：空串同时充当「还没回填」的哨兵，回填判据是
// `COALESCE(blob_sha256, ”) = ”`。给已有行加 NOT NULL 列时必须有默认值，
// 否则 SQLite/MySQL 严格模式直接失败。
const ticketAttachmentBlobLinkSQLiteDDL = "`" + ticketAttachmentBlobLinkColumn + "` char(64) NOT NULL DEFAULT ''"

// EnsureTicketAttachmentBlobSchema 建立工单图片 blob 表并把存量附件回填进去
// （task_doc/112 N1 + task_doc/121 §4.4）。
//
// # 这是十张新表里**唯一有真实数据回填**的迁移，风险最高
//
// 它要在运行时搬动可能几万行的图片密文，并维护一个**会被延迟删除任务信任**的
// 引用计数。四个必须点（121 §4.4）逐条对应到实现：
//
//  1. **ref_count 必须等于引用行数** —— 见 backfillTicketAttachmentBlobs 里
//     每处理完一个 sha256 就重算 `COUNT(*) WHERE blob_sha256 = ?`，而不是「插一行记 1」
//     之后累加。少算会让 24 小时延迟删除删掉还在用的 blob。
//  2. **一张图一个事务** —— 每个 sha256 单独 `db.Transaction`。几万张图包在一个
//     事务里会长时间持写锁（MySQL 上表现为业务写入全阻塞，SQLite 上更直接是
//     `database is locked`）。
//  3. **幂等** —— 靠 sha256 主键 + OnConflict DoNothing：中途崩了重跑不会重复插，
//     也不会因为「已存在的 sha256」而把回填标记误判成完成（标记只在整轮跑完后写）。
//  4. **旧列一个版本内不删** —— 本迁移只 ADD COLUMN 与 INSERT，绝不 DROP
//     ticket_attachments.nonce / ciphertext，回滚时旧代码照旧读旧列。
//
// # 失败策略（121 §4.4 / §1.2）
//
//   - 结构性失败（建表、加列）→ 返回 error，外层 FATAL（缺表则去重完全不可用，
//     而且回填会把数据写进不存在的表）；
//   - 回填失败 → 只 SysLog 且**不写标记**，下次启动重跑（幂等）；
//   - 标记写失败 → 只 SysLog（结构已就位，重跑幂等）。
//
// # 复杂度
//
// ticket_attachments 按 id 游标**只扫一遍**（每批 500 个唯一 sha256），
// 内存与批大小同量级，不随附件总数增长；每个 sha256 额外做一次 COUNT（走
// blob_sha256 索引）。不在按行循环里再查其它大表。
func EnsureTicketAttachmentBlobSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, ticketAttachmentBlobSchemaReadyKey) {
		return nil
	}

	// ① 结构性部分：建表 + 给 ticket_attachments 补 blob_sha256 列。
	//    失败 FATAL —— 回填必须写在结构已就位的前提上。
	applied, err := safeEnsureTicketAttachmentBlobStructure(db)
	if err != nil {
		return err
	}
	if !applied {
		// 极端空库首次启动（ticket_attachments 都还不存在）：什么都不建，也绝不写标记，
		// 否则附件表建出来之后本迁移会被永久跳过（121 §2.1）。
		return nil
	}

	// ② 数据回填：失败只记日志、不写标记，下次启动重试。
	if err := safeBackfillTicketAttachmentBlobs(db); err != nil {
		logger.SysLog(fmt.Sprintf("工单图片 blob 回填失败（不影响启动，下次启动会重试）：%v", err))
		return nil
	}

	// ③ 只有整轮回填跑完才写标记。
	if err := setSystemSettingFlag(db, ticketAttachmentBlobSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：数据已经回填好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入工单图片 blob 迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureTicketAttachmentBlobStructure 外层兜 recover：脏数据不能让服务反复崩溃。
//
// 返回值 applied 表示「前置表存在、本次可以继续回填」；只有 applied 为 true 才应回填。
func safeEnsureTicketAttachmentBlobStructure(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyTicketAttachmentBlobStructure(db)
}

// applyTicketAttachmentBlobStructure 建 blobs 表、补 blob_sha256 列与索引。
//
// 顺序固定：先建 blob 表（回填的目标），再给附件表加列（回填的桥梁），
// 最后补索引（否则回填时的 COUNT 会全表扫）。
func applyTicketAttachmentBlobStructure(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		// 附件表还没建出来：交给 ensureTicketAttachmentSchema，
		// 下次启动附件表就存在了。
		return false, nil
	}
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		return false, fmt.Errorf("create %s fail: %w", entity.TableNameTicketAttachmentBlob, err)
	}
	if err := addTicketAttachmentBlobLinkColumn(db); err != nil {
		return false, err
	}
	if err := ensureTicketAttachmentBlobIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// addTicketAttachmentBlobLinkColumn 用显式 ALTER 给 ticket_attachments 补 blob_sha256。
//
// SQLite 走裸 DDL、MySQL/MariaDB 走 `Migrator().AddColumn`（照
// user_archive_migration.go:94 的 addUserArchiveColumn 手法）。
//
// 为什么两侧不等价也必须分开：SQLite 驱动的 AddColumn 在部分版本上会走
// 整表复制，而 MySQL 的 AddColumn 是原生 `ALTER TABLE ... ADD COLUMN`，代价小。
func addTicketAttachmentBlobLinkColumn(db *gorm.DB) error {
	if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn) {
		return nil
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		statement := "ALTER TABLE " + entity.TableNameTicketAttachment + " ADD COLUMN " + ticketAttachmentBlobLinkSQLiteDDL
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动或方言差异：列已经在了就当成功。
			if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn) {
				return nil
			}
			return fmt.Errorf("新增 %s.%s 失败: %w", entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上 %s 列", entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn))
		return nil
	}
	// MySQL / MariaDB：列类型由 entity.TicketAttachmentBlobLink 的 tag 提供，这里只借用字段名。
	if err := db.Migrator().AddColumn(&entity.TicketAttachmentBlobLink{}, "BlobSHA256"); err != nil {
		if db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn) {
			return nil
		}
		return fmt.Errorf("新增 %s.%s 失败: %w", entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn, err)
	}
	return nil
}

// ensureTicketAttachmentBlobIndexes 补建两张表上的索引。
//
// ⚠️ 索引补建放在 FLAG 判断**之后**也可以（本表是新表，不存在「老库跑过标记但缺索引」），
// 但放在结构阶段里能保证「第一次创建表时就带上索引」——回填期的 COUNT 依赖
// ticket_attachments(blob_sha256) 索引，缺它就是几万行全表扫 × 几万次。
//
// 索引名与 GORM 从 `index` tag 推导出的名字一致，避免同列出现两条重复索引。
func ensureTicketAttachmentBlobIndexes(db *gorm.DB) error {
	type indexSpec struct {
		table  string
		name   string
		column string
	}
	specs := []indexSpec{
		// 回填与引用计数 COUNT 的判据列。
		{table: entity.TableNameTicketAttachment, name: "idx_ticket_attachments_blob_sha256", column: ticketAttachmentBlobLinkColumn},
		// 延迟清理的判据列（ref_count <= 0 AND updated_at < cutoff）。
		{table: entity.TableNameTicketAttachmentBlob, name: "idx_ticket_attachment_blobs_updated_at", column: "updated_at"},
	}
	dialect := strings.ToLower(strings.TrimSpace(db.Dialector.Name()))
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		var statement string
		switch dialect {
		case "sqlite":
			statement = fmt.Sprintf("CREATE INDEX IF NOT EXISTS `%s` ON `%s` (`%s`)", spec.name, spec.table, spec.column)
		case "mysql":
			statement = fmt.Sprintf("CREATE INDEX `%s` ON `%s` (`%s`)", spec.name, spec.table, spec.column)
		default:
			logger.SysLog(fmt.Sprintf("未知数据库方言 %q，跳过 %s 索引 %s", db.Dialector.Name(), spec.table, spec.name))
			continue
		}
		if err := db.Exec(statement).Error; err != nil {
			if db.Migrator().HasIndex(spec.table, spec.name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", spec.name, spec.table, err)
		}
	}
	return nil
}

// safeBackfillTicketAttachmentBlobs 外层兜 recover。
func safeBackfillTicketAttachmentBlobs(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return backfillTicketAttachmentBlobs(db)
}

// ticketAttachmentBlobBackfillRow 是回填时从 ticket_attachments 读出的最小列集合。
//
// 只 SELECT 参与回填的列：密文可能有几十 KB × 几万行，把不需要的列
// （check_error / label / nsfw_score …）一起读出来纯属浪费内存与带宽。
type ticketAttachmentBlobBackfillRow struct {
	ID         int64  `gorm:"column:id"`
	TicketID   int64  `gorm:"column:ticket_id"`
	UserID     int32  `gorm:"column:user_id"`
	MediaType  string `gorm:"column:media_type"`
	ByteSize   int64  `gorm:"column:byte_size"`
	SHA256     string `gorm:"column:sha256"`
	Nonce      []byte `gorm:"column:nonce"`
	Ciphertext []byte `gorm:"column:ciphertext"`
	ScanState  string `gorm:"column:scan_state"`

	NSFWScore    *float64 `gorm:"column:nsfw_score"`
	RiskLevel    string   `gorm:"column:risk_level"`
	Suggestion   string   `gorm:"column:suggestion"`
	Label        string   `gorm:"column:label"`
	CheckError   string   `gorm:"column:check_error"`
	CheckedAt    *time.Time
	AttemptCount int        `gorm:"column:attempt_count"`
	NextAttempt  *time.Time `gorm:"column:next_attempt_at"`

	// BlobSHA256 是**回填前**该行已有的指向，用于识别悬空/错乱指针。
	//
	// 必须读出来：只看「空 vs 非空」无法区分「指向正确」与「指向了别的不存在的 sha」
	// （后者会让 Recompute 把自己的 ref_count 算成 0，24 小时后误删自己的 blob）。
	BlobSHA256 string `gorm:"column:blob_sha256"`
}

// backfillTicketAttachmentBlobs 把 ticket_attachments 的图片搬进 blobs 表并回填指向。
//
// # 算法（按 sha256 去重，一张图一个事务）
//
//	ticket_attachments 按 id 升序游标分批扫（每批 batchSize 行）
//	  └─ 对每一行：
//	       ├─ 该 sha256 的 blob 已存在 → 只回填 blob_sha256 指向
//	       └─ 不存在 → 开一个事务：插 blob（OnConflict DoNothing）+ 回填指向 + 重算 ref_count
//
// 事务粒度是**行级**而不是批级：121 §4.4 第 2 点原文要求「一张图一次事务」，
// 理由是几万张图包在大事务里会锁表数分钟。批只是读取的滑动窗口，
// 提交仍然逐行发生 —— 因此中途崩溃时最多丢掉「当前这一行」的进度，
// 已提交的行不会回退，重跑靠主键幂等补齐。
//
// # 幂等
//
//   - blob 插入用 OnConflict DoNothing（sha256 是主键）；
//   - 指针回填用「指针与 row.sha256 不等才改写」，所以已对齐的行
//     RowsAffected=0，重跑不重复写、也不重复重算；
//   - ref_count 每次都用 COUNT 重算，重复执行得到同一个值。
//
// 三者叠加保证「中途崩了重跑不重复插、结果一致」。另外指针核对会把
// 「非空但指向错」的行拉回 row.sha256（见 alignTicketAttachmentBlobPointerInTx），
// 这是唯一会**覆盖**既有列值的分支，理由写在那里。
func backfillTicketAttachmentBlobs(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameTicketAttachment) ||
		!db.Migrator().HasTable(entity.TableNameTicketAttachmentBlob) {
		return nil
	}
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn) {
		return fmt.Errorf("%s 缺少 %s 列，无法回填", entity.TableNameTicketAttachment, ticketAttachmentBlobLinkColumn)
	}

	var (
		lastID   int64
		created  int
		linked   int
		scanned  int
		skipped  int
		fixed    int
		firstErr error
	)
	for {
		rows := make([]ticketAttachmentBlobBackfillRow, 0, ticketAttachmentBlobBackfillBatchSize)
		if err := db.Session(&gorm.Session{NewDB: true}).
			Table(entity.TableNameTicketAttachment).
			Select("id, ticket_id, user_id, media_type, byte_size, sha256, nonce, ciphertext, scan_state, "+
				"nsfw_score, risk_level, suggestion, label, check_error, checked_at, attempt_count, next_attempt_at, "+
				ticketAttachmentBlobLinkColumn).
			Where("id > ?", lastID).
			Order("id asc").
			Limit(ticketAttachmentBlobBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return fmt.Errorf("读取待回填的工单附件失败: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			lastID = row.ID
			scanned++
			sha := service.NormalizeTicketAttachmentBlobSHA256(row.SHA256)
			if sha == "" || len(row.Nonce) == 0 || len(row.Ciphertext) == 0 {
				// 残缺行（旧版「先插占位行、再 UPDATE 密文」崩在中间的产物）：
				// 不搬进 blobs 表 —— blobs 里放一份残缺密文只会让读取路径
				// 拿到同样解不开的数据，却让人以为「已经搬过去了」。
				skipped++
				continue
			}
			// 一张图一个事务（121 §4.4 第 2 点）。
			row := row
			if err := db.Transaction(func(tx *gorm.DB) error {
				blobCreated, txErr := ensureTicketAttachmentBlobRow(tx, row, sha)
				if txErr != nil {
					return txErr
				}
				if blobCreated {
					created++
				}
				// 指针核对：把 blob_sha256 对齐到该行自己的 sha256。
				// 返回值 staleSHA 是「被纠正掉的那个旧指向」，它的引用计数也必须重算 ——
				// 否则旧 blob 会永久多算这一次引用，永远归不了零（永不释放）。
				pointerChanged, staleSHA, txErr := alignTicketAttachmentBlobPointerInTx(tx, row, sha)
				if txErr != nil {
					return txErr
				}
				if pointerChanged {
					linked++
					if staleSHA != "" && staleSHA != sha {
						fixed++
					}
				}
				// ref_count 必须等于引用行数（121 §4.4 第 1 点）：
				// 每处理完一行就重算，而不是「插的时候记 1、以后累加」。
				if _, txErr = service.RecomputeTicketAttachmentBlobRefCountInTx(tx, sha); txErr != nil {
					return txErr
				}
				// 旧指向的那份 blob 也要收敛：它刚刚少了这一行引用。
				if staleSHA != "" && staleSHA != sha {
					if _, txErr = service.RecomputeTicketAttachmentBlobRefCountInTx(tx, staleSHA); txErr != nil {
						return txErr
					}
				}
				return nil
			}); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				// 单行失败不中断整轮：一张坏图不该让其余几万张都搬不过去。
				// 但整轮最终会被判为失败（返回 firstErr）→ 不写标记 → 下次重试。
				continue
			}
		}
	}
	if scanned > 0 {
		logger.SysLog(fmt.Sprintf(
			"工单图片 blob 回填完成：扫描 %d 行、新建 blob %d 份、回填指向 %d 行、跳过残缺 %d 行",
			scanned, created, linked, skipped))
	}
	// ⚠️ fixed 是**数据损坏指示器**，不是进度数字：正常库（全新库，或老库首次回填）
	// 恒为 0。非零说明库里已经存在坏数据 —— 某行 blob_sha256 指向了别的 sha，
	// 成因是手工改库或某处 bug 写入了错误/非规范形态的指针。
	// 它单独一条日志、并给出追查入口，就是为了让人看见后去查而不是当成正常现象。
	if fixed > 0 {
		logger.SysLog(fmt.Sprintf(
			"⚠️ 工单图片 blob 回填发现并纠正了 %d 行错乱/非规范的 blob_sha256 指针 —— "+
				"这是一个**只能由数据损坏产生**的计数，正常库应为 0。"+
				"请追查：该行 blob_sha256 原本指向的 sha 是否还存在、是否有手工改库或代码路径写错指针。"+
				"若不修，受影响的 blob 会被重算成 ref_count=0，24 小时后被延迟清理误删（图片永久不显示且不报错）。",
			fixed))
	}
	return firstErr
}

// ensureTicketAttachmentBlobRow 保证该 sha256 在 blobs 表里有一行（幂等）。
//
// 返回值 created 表示本次真的插入了新行（用于日志统计「省下多少空间」）。
//
// 密文取**该 sha256 任一行的副本**（112 §1.5 步骤 2 的语义）：同一 sha256 意味着
// 明文完全相同，因此任一行用不同随机 nonce 加密出来的密文都能解出同样的明文 ——
// 密文本身不必相同。取第一行的好处是「不改动任何既有数据」。
func ensureTicketAttachmentBlobRow(tx *gorm.DB, row ticketAttachmentBlobBackfillRow, sha string) (bool, error) {
	var existing int64
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).
		Where("sha256 = ?", sha).Count(&existing).Error; err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}
	scanState := strings.TrimSpace(row.ScanState)
	if scanState == "" {
		// 空 scan_state 在展示判定里是 fail-closed（不显示），保持这个语义。
		scanState = entity.TicketAttachmentScanPending
	}
	blob := entity.TicketAttachmentBlob{
		SHA256:        sha,
		ByteSize:      row.ByteSize,
		MediaType:     row.MediaType,
		Nonce:         row.Nonce,
		Ciphertext:    row.Ciphertext,
		ScanState:     scanState,
		NSFWScore:     row.NSFWScore,
		RiskLevel:     row.RiskLevel,
		Suggestion:    row.Suggestion,
		Label:         row.Label,
		CheckError:    row.CheckError,
		CheckedAt:     row.CheckedAt,
		AttemptCount:  row.AttemptCount,
		NextAttemptAt: row.NextAttempt,
	}
	// OnConflict DoNothing 是幂等的第二道保险：上面 Count 通过之后、
	// 插入之前可能有并发插入同一 sha256（多实例启动）。
	result := tx.Session(&gorm.Session{NewDB: true}).
		Clauses(clause.OnConflict{DoNothing: true}).Create(&blob)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// alignTicketAttachmentBlobPointerInTx 把 blob_sha256 对齐到该行自己的 sha256。
//
// 返回值 changed 表示指针被改写；staleSHA 是被纠正掉的旧指向（「」表示原本为空）。
//
// # 三种状态（这是本函数存在的全部理由）
//
//	空串            → 首次回填，写入 sha
//	等于 sha        → 已经正确，**不改写**（重跑时不动它，也不刷新 updated_at）
//	等于别的 sha    → 悬空/错乱指针，**改回 sha**（否则会踩下面那个坑）
//
// # 第三种状态为什么必须处理（实测过的真实故障）
//
// 若某行 blob_sha256 指向一个不存在（或不该指向）的 sha，回填的旧语义是
// 「非空就跳过」，于是出现这样的连锁：
//
//	① 回填按 row.sha256 建出/保留了自己那份 blob（bbbb）
//	② 指针非空 → 跳过 → 指针仍是 cccc
//	③ Recompute(bbbb) 数 COUNT(blob_sha256='bbbb') = 0
//	④ 把自己那份 blob 的 ref_count 归 0
//	⑤ 24 小时后延迟清理把它删掉 —— 图片永久不显示，且**不报任何错**
//
// 也就是说：一个错乱的指针会连累它自己那份完好的数据。这正是
// 112 §1.5 步骤 4「切到读 blobs 表」之后最难查的故障形态，必须在回填里就修掉。
//
// # 为什么 row.sha256 是权威
//
// 旧写入路径 StoreTicketAttachmentWithDB 存的就是 sha256(重编码后的明文)，
// 与 blobs 表主键同一口径；而 blob_sha256 只是迁移新增的**缓存列**，
// 没有独立语义。所以「对齐到 row.sha256」不是猜测，是把缓存拉回事实。
//
// # 幂等
//
// 条件写成「指针与目标不等」，所以第二次运行时 already-correct 的行
// RowsAffected=0，不会被重复改写，也不会让 Recompute 无意义地跑第二次。
//
// 注意**不刷新 updated_at**：迁移只补列值，不许伪造业务时间（与
// backfillTicketTypeOrigin 用 UpdateColumn 的理由一致）。
func alignTicketAttachmentBlobPointerInTx(tx *gorm.DB, row ticketAttachmentBlobBackfillRow, sha string) (changed bool, staleSHA string, err error) {
	raw := strings.TrimSpace(row.BlobSHA256)
	if raw == sha {
		// 已经是规范形态且指向正确：不动它。这是重跑时的常态，也是幂等的关键。
		return false, "", nil
	}
	// ⚠️ 判据必须用**原始值**而不是归一化值。反例（实测过）：库里存的是大写的
	// 同一个 sha，归一化后 == sha，若据此提前返回，就会留下一个非规范形态的指针；
	// 而 Recompute 的 COUNT 用的是小写 sha，SQLite 的 TEXT 比较**大小写敏感**，
	// 于是数出 0 → 把这份 blob 的 ref_count 归 0 → 24 小时后它被当垃圾删掉
	// （图片永久不显示且不报错）。所以「大写/带空白」这类非规范形态也必须改写。
	current := service.NormalizeTicketAttachmentBlobSHA256(row.BlobSHA256)
	result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachment{}).
		Where("id = ? AND COALESCE("+ticketAttachmentBlobLinkColumn+", '') <> ?", row.ID, sha).
		UpdateColumn(ticketAttachmentBlobLinkColumn, sha)
	if result.Error != nil {
		return false, "", result.Error
	}
	if result.RowsAffected == 0 {
		// 并发下被别人改成了 sha：不算变更，也不必重算旧指向。
		return false, "", nil
	}
	return true, current, nil
}
