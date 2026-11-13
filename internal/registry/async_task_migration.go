package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// asyncTaskSchemaReadyKey 标记「异步任务表与索引已就绪」（121 §4.10 / §1.4 的 `_schema_v1` 后缀）。
const asyncTaskSchemaReadyKey = "async_task_schema_v1"

// EnsureAsyncTaskSchema 建立异步任务所需的表结构（121 §4.10，任务书 ⑥）。
//
// FLAG 型迁移（121 §2 / §1.2）：与 blacklist_migration.go 同构，四条纪律逐条对齐：
//
//  1. `db == nil` 直接返回 nil（121 §1.2 规范 2，28/29 个迁移都这么写）；
//  2. **索引补建放在标记判断之前**（121 §2.2）。老库可能已经跑过标记（表建好了），
//     后来新增的索引等不到 AutoMigrate；只把索引放在标记之后会让**已经标记过的库
//     永远补不上新索引**，而索引缺失的表现是「查询慢」，不报错、几乎不可发现；
//  3. **建表失败不写标记**（121 §2.1）。先写标记再建表、或建表失败也写标记，
//     会让这张表永远不会被创建，而且没有任何报错（标记说"已完成"）。
//     因此只有 applied=true（本次真的建/structure 就绪）才写标记；
//  4. 标记写失败不算迁移失败：结构已建好，下次启动重跑是幂等的（121 §1.2 规范 6）；
//  5. 绝不 DROP TABLE（121 §1.2 规范 8）：任务表里有运营审计数据。
//
// 空库首次启动时（连 system_settings 都没有）什么都不建、也不写标记 ——
// 否则 system_settings 建出来之后本迁移会被永远跳过（121 §2.1 的坑）。
//
// ⚠️ 本函数是**导出**的：db.go 的改动不在本任务范围内，接线由集成方调用。
// 未接线时表不存在，异步任务服务会明确报「表结构未就绪」，不会静默失败。
func EnsureAsyncTaskSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 索引补建放在标记判断**之前**：HasIndex 幂等，代价只有一次元数据查询。
	if db.Migrator().HasTable(entity.TableNameAsyncTask) {
		if err := ensureAsyncTaskIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, asyncTaskSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureAsyncTaskSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 极端空库首次启动（连 system_settings 都没有）：什么都不建，也绝不写标记。
		return nil
	}
	if err := setSystemSettingFlag(db, asyncTaskSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入异步任务迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureAsyncTaskSchema 在外层兜一层 recover：脏数据不能让服务反复崩溃（121 §1.2 规范 7）。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureAsyncTaskSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyAsyncTaskSchema(db)
}

// applyAsyncTaskSchema 建表 + 建索引。SQLite 与 MySQL 两套分支都要能跑。
func applyAsyncTaskSchema(db *gorm.DB) (bool, error) {
	if err := db.AutoMigrate(&entity.AsyncTask{}); err != nil {
		// 建表失败 → 返回错误（外层 FATAL 看见），**不写标记**。
		return false, fmt.Errorf("create async_tasks fail: %w", err)
	}
	if err := ensureAsyncTaskIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureAsyncTaskIndexes 保证三列索引存在（任务书 ⑥：type / status / expires_at）。
//
// 这三条都不是「多此一举」：
//   - type：管理页按类型筛任务（导出的任务数量远多于批量赠送，不建索引就是全表扫）；
//   - status：启动恢复只捞 pending/running，清理只捞终态且已过期 —— 两条都靠它；
//   - expires_at：清理 goroutine 每个周期都要 `expires_at <= now` 扫一遍，
//     任务表只增不减时这是最高频的查询。
//
// 另外补一条 task_id 唯一索引：对外按 taskId 查单条，且**不允许重复**（重复说明
// 生成逻辑有 bug，该在写入时就撞出来，而不是留下两条同 ID 的任务互相覆盖）。
func ensureAsyncTaskIndexes(db *gorm.DB) error {
	indexes := []asyncTaskIndexSpec{
		{name: "uk_async_task_task_id", table: entity.TableNameAsyncTask, columns: []string{"task_id"}, unique: true},
		{name: "idx_async_task_type", table: entity.TableNameAsyncTask, columns: []string{"type"}},
		{name: "idx_async_task_status", table: entity.TableNameAsyncTask, columns: []string{"status"}},
		{name: "idx_async_task_expires_at", table: entity.TableNameAsyncTask, columns: []string{"expires_at"}},
	}
	return ensureAsyncTaskIndexSpecs(db, indexes)
}

type asyncTaskIndexSpec struct {
	name    string
	table   string
	columns []string
	unique  bool
}

// ensureAsyncTaskIndexSpecs 逐个补建缺失的索引，SQLite / MySQL 分别出 DDL。
//
// 幂等策略与 ensureBlacklistIndexes 完全一致（复用同一套原因）：SQLite 支持
// CREATE INDEX IF NOT EXISTS，MySQL **不支持**（会报 1064），必须先用元数据判断。
func ensureAsyncTaskIndexSpecs(db *gorm.DB, specs []asyncTaskIndexSpec) error {
	if db == nil {
		return nil
	}
	dialect := db.Dialector.Name()
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		statement := asyncTaskIndexDDL(dialect, spec)
		if statement == "" {
			return fmt.Errorf("不支持的数据库方言 %q：无法为 %s 建索引 %s", dialect, spec.table, spec.name)
		}
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动的两个实例可能同时判定「索引不存在」，其中一个会撞 already exists。
			// 此时再核一次元数据：索引确实在，就算成功（不掩盖其它错误）。
			if db.Migrator().HasIndex(spec.table, spec.name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", spec.name, spec.table, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上索引 %s", spec.table, spec.name))
	}
	return nil
}

// asyncTaskIndexDDL 按方言生成建索引语句；未知方言返回空串（由调用方报错）。
//
// 抽成纯函数是为了能直接断言两套分支的语句，不必真的连一个 MySQL 服务器
// （与 blacklistIndexDDL / billing_record_indexes_mysql_test.go 的思路一致）。
//
// 标识符统一用反引号引用：SQLite 与 MySQL 都接受反引号。
func asyncTaskIndexDDL(dialect string, spec asyncTaskIndexSpec) string {
	kind := "INDEX"
	if spec.unique {
		kind = "UNIQUE INDEX"
	}
	quotedColumns := make([]string, len(spec.columns))
	for i, column := range spec.columns {
		quotedColumns[i] = quoteBillingIdentifier(column)
	}
	columns := strings.Join(quotedColumns, ", ")
	name := quoteBillingIdentifier(spec.name)
	table := quoteBillingIdentifier(spec.table)

	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "sqlite":
		return fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)", kind, name, table, columns)
	case "mysql":
		// MySQL 没有 IF NOT EXISTS；幂等由 ensureAsyncTaskIndexSpecs 的 HasIndex 前置判断保证。
		return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, name, table, columns)
	default:
		return ""
	}
}
