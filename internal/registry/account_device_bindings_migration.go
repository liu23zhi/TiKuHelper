package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// accountDeviceBindingsSchemaV1Key 标记「设备池绑定表与唯一索引已就绪」。
	//
	// 键名由任务书 / 121 §4.9 指定，**不要改名**：改名等于让已迁移过的库
	// 重新走一遍（虽然幂等，但会把「已完成」的状态丢掉）。
	accountDeviceBindingsSchemaV1Key = "account_device_bindings_schema_v1"

	// accountDeviceBindingUserDeviceIndex 是唯一索引名。
	//
	// 与 entity.AccountDeviceBindingUniqueIndex 必须是同一个字面量：
	// 实体 tag 建一次、这里补建一次，两处名字不一致会让
	// HasIndex 永远返回 false（每次启动都去 CREATE 一遍）。
	accountDeviceBindingUserDeviceIndex = entity.AccountDeviceBindingUniqueIndex
)

// ensureAccountDeviceBindingsSchema 建立设备池所需的表结构（121 §4.9，107 Q15/Q16）。
//
// 覆盖：
//   - account_device_bindings（账号—设备绑定表）
//   - 唯一索引 (user_id, device_id) —— 幂等锚点，见 entity 里的长注释
//
// 照 blacklist_migration.go 的四条纪律，逐条对齐：
//
//  1. **结构性建表/建索引失败 → 返回 error**，让启动流程看见。
//     不在这里自己 FATAL/SysLog —— 处置策略归调用方（本迁移在 db.go 里的接线
//     用 SysLog 降级，见文件末尾的「失败策略」）。
//  2. **没有数据回填，不做任何 DROP TABLE**。历史请求没有设备指纹（121 §4.9
//     「数据回填：无」），伪造历史行等于凭空决定「某用户已占用 N 台设备」，
//     会把存量用户直接锁在上限之外且无法事后自证。设备池从上线起算。
//  3. **用 system_settings 标记保证只跑一次**；失败不写标记，下次启动幂等重跑。
//     标记写失败本身不算迁移失败（结构已建好，重跑幂等）。
//  4. **索引补建不受一次性标记约束**：补建逻辑放在标记判断**之前**。
//     老库可能已经跑过标记（表建好了），但后来新增/调整的索引等不到 AutoMigrate；
//     若只把索引放在 FLAG 判断之后，这类库永远补不到新索引，而索引缺失
//     只表现为「查询慢」，不报错、几乎不可发现。
//
// FLAG 型（标记键 account_device_bindings_schema_v1），不是列检测型：
// 本表无前置表依赖之外的判据差异，且任务书与 121 §4.9 都指定 FLAG + 该键名。
//
// 无历史回填 → **本文件里不允许出现任何 `UPDATE users`**（判据 ⑤ 会做源码级断言）。
// 这条不是形式主义：整张表的意义就是「从上线开始记录」，一旦有回填语句，
// 「无回填」这个前提就会被下一个人当成建议而不是约束。
//
// ## 失败策略（为什么是 fail-open）
//
// 设备池是**权益限制**，不是安全边界。表坏了/没建出来时如果 fail-closed，
// 结果是**全站用户都无法登录/无法答题**（因为每台新设备都会被当成超额）；
// 而 fail-open 的代价只是「这段时间设备上限不生效」。107 Q17 已拍板 fail-open，
// 所以 db.go 的接线必须用 logger.SysLog 而不是 FatalLog。
//
// 与之配套：service.CanUseDevice 内部还有一层 fail-open（查询出错也放行），
// 迁移失败与运行期查询失败是同一条策略的两段，不能只做一段。
//
// 幂等：两次运行不报错、不重复建表、不重复建索引。
func ensureAccountDeviceBindingsSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 纪律 4：索引补建放在标记判断之前。
	if db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		if err := ensureAccountDeviceBindingIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, accountDeviceBindingsSchemaV1Key) {
		return nil
	}
	applied, err := safeEnsureAccountDeviceBindingsSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 走到这里只有一种情况：建表/建索引被 panic 打断（被 safe 包装兜住了）。
		// 结构没建成就**绝不写标记**，否则这张表将永远建不出来且没有任何报错
		// （121 §2.1 的反直觉细节：标记说「已完成」，迁移就再也不会重试）。
		return nil
	}
	if err := setSystemSettingFlag(db, accountDeviceBindingsSchemaV1Key); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入设备池迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureAccountDeviceBindingsSchema 在外层兜一层 recover：脏元数据不能让服务反复崩溃。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureAccountDeviceBindingsSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyAccountDeviceBindingsSchema(db)
}

// applyAccountDeviceBindingsSchema 建表 + 建索引。SQLite 与 MySQL 两套分支都要能跑。
func applyAccountDeviceBindingsSchema(db *gorm.DB) (bool, error) {
	if err := db.AutoMigrate(&entity.AccountDeviceBinding{}); err != nil {
		return false, fmt.Errorf("create account_device_bindings fail: %w", err)
	}
	if err := ensureAccountDeviceBindingIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// accountDeviceBindingIndexSpec 是一条要补建的索引。
//
// 与 blacklistIndexSpec / announcementIndexSpec 同构但不复用：那两个类型及其构造函数
// 在名字上绑定了各自的表，跨表借用会让人以为两处共享生命周期。
// （真正的收敛做法是把「spec + 分方言 DDL」抽成不带业务名的公共 helper，
// 但那要改 blacklist_migration.go —— 属本任务禁改的既有文件，故留作后续。）
type accountDeviceBindingIndexSpec struct {
	name    string
	table   string
	columns []string
	unique  bool
}

// ensureAccountDeviceBindingIndexes 保证唯一索引 (user_id, device_id) 存在。
//
// 为什么这条索引必须显式补建、不能只信 AutoMigrate：GORM 在 SQLite 上对
// 复合唯一索引的创建行为随版本变化（历史上有过「只在表新建时建索引、老表不补」），
// 而这条索引是「同一设备重复登记不占新槽」的**唯一**保障。缺了它，
// 同一台设备登录 10 次会写 10 行，上限 2 的等级被自己正常使用打满，且不报错。
//
// 唯一索引同时是「按用户查设备」的查询索引（最左前缀 user_id），
// 因此不再额外建 (user_id) 单列索引 —— 那只是重复写放大。
func ensureAccountDeviceBindingIndexes(db *gorm.DB) error {
	specs := []accountDeviceBindingIndexSpec{
		{
			name:    accountDeviceBindingUserDeviceIndex,
			table:   entity.TableNameAccountDeviceBinding,
			columns: []string{"user_id", "device_id"},
			unique:  true,
		},
	}
	if db == nil {
		return nil
	}
	dialect := db.Dialector.Name()
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		statement := accountDeviceBindingIndexDDL(dialect, spec)
		if statement == "" {
			return fmt.Errorf("不支持的数据库方言 %q：无法为 %s 建索引 %s", dialect, spec.table, spec.name)
		}
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动的两个实例可能同时判定「索引不存在」，其中一个会撞 already exists。
			// 这种情况下再核一次元数据：索引确实在，就算成功（不掩盖其它错误）。
			if db.Migrator().HasIndex(spec.table, spec.name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", spec.name, spec.table, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上索引 %s", spec.table, spec.name))
	}
	return nil
}

// accountDeviceBindingIndexDDL 按方言生成建索引语句；未知方言返回空串（由调用方报错）。
//
// 抽成纯函数是为了能直接断言两套分支的语句，不必真的连一个 MySQL 服务器
// （与 billing_record_indexes_mysql_test.go 的思路一致）。
//
// 差异只在 "IF NOT EXISTS" 一句：SQLite 支持，MySQL 的 CREATE INDEX 不支持
// （幂等由调用方的 HasIndex 前置判断保证）。标识符统一用反引号，两方言都接受。
func accountDeviceBindingIndexDDL(dialect string, spec accountDeviceBindingIndexSpec) string {
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
		return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, name, table, columns)
	default:
		return ""
	}
}
