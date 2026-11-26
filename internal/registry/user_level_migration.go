package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// defaultUserLevels 是等级表的初始数据（只在新库/空表时写入一次）。
//
// 为什么要有初始数据：等级判定必须有一个「谁都能进」的最低级，
// 否则新用户 level_id=0 就是「无等级」，前端要么显示空白要么到处特判。
// rank 从 1 起，0 一律表示「无等级」（等级被删或全部停用时的兜底）。
//
// device_limit 全部留 0：C3（设备池）本轮不实现，这一列只是契约占位，
// 没有任何业务读取它。C3 落地时改这一列即可，不必再动表结构。
var defaultUserLevels = []entity.UserLevel{
	{Name: "基础会员", Rank: 1, MinRechargeTotal: 0, MinConsumptionTotal: 0, DeviceLimit: 0, Enabled: true, Description: "注册即有，不设门槛"},
	{Name: "进阶会员", Rank: 2, MinRechargeTotal: 100, MinConsumptionTotal: 0, DeviceLimit: 0, Enabled: true, Description: "累计充值满 100"},
	{Name: "资深会员", Rank: 3, MinRechargeTotal: 500, MinConsumptionTotal: 100, DeviceLimit: 0, Enabled: true, Description: "累计充值满 500 且累计消费满 100"},
	{Name: "核心会员", Rank: 4, MinRechargeTotal: 2000, MinConsumptionTotal: 500, DeviceLimit: 0, Enabled: true, Description: "累计充值满 2000 且累计消费满 500"},
}

// ensureUserLevelSchema 建等级体系的两张表，并给 users 补 level_id 列。
//
// 三件事、三处失败语义：
//  1. 建表失败 → 返回错误（结构性故障必须让人看见，外层 FATAL）；
//  2. users.level_id 加列失败 → 返回错误（判定没有落点，等于核心需求静默失效）；
//  3. 初始数据写入失败 → 只记日志，不影响启动（表已建好，管理员可在后台补）。
//
// ⚠️ users.level_id **必须走显式 ALTER**，不能靠 AutoMigrate：
// internal/entity/users.gen.go 是 gorm.io/gen 生成文件（DO NOT EDIT），
// 本轮不手改它，因此 entity.User 上没有 LevelID 字段，
// `db.Migrator().AddColumn(&entity.User{}, "LevelID")` 无从调用。
// 读侧同样用 Table("users").Select("level_id") 直接取列，不依赖实体字段。
//
// 列用 integer + NOT NULL DEFAULT 0：0 表示「无等级」，
// 历史用户不需要任何回填 —— 他们落 0，下一次触发评估时会自然升到最低级。
func ensureUserLevelSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// 连 users 都没有的库（极简单测 schema）不该在这里建表：
		// 加列无从谈起，建出来的等级表也没有落点。
		return nil
	}
	if err := ensureEffectiveAuditSchema(db); err != nil {
		return fmt.Errorf("create effective ledger audit: %w", err)
	}
	if err := db.AutoMigrate(&entity.UserLevel{}); err != nil {
		return fmt.Errorf("create user_levels fail: %w", err)
	}

	if err := ensureUserLevelIDColumn(db); err != nil {
		return fmt.Errorf("add users.level_id fail: %w", err)
	}
	// 137 §3.1：用户级设备上限覆盖列（A1-1）。放在本函数里而不是新开一个迁移：
	// 它与 level_id 是同一张表上「设备上限」的两级配置（用户覆盖 / 等级值），
	// 分开会导致「level_id 补齐了但 override 没有」这种半成品状态需要另一次部署修。
	if err := ensureUserDeviceLimitOverrideColumn(db); err != nil {
		return fmt.Errorf("add users.device_limit_override fail: %w", err)
	}
	seedDefaultUserLevels(db)
	// 137 §10.3 / A1-6：给存量 level_id=0 的用户补一次评级。
	// 必须排在 seedDefaultUserLevels 之后 —— 等级表为空时评估会直接早退
	// （EvaluateUserLevelWithDB 的 len(levels)==0 分支），回填等于白跑一趟
	// 且标记会被写掉（详见 backfillUserLevelsForRegister 的注释）。
	backfillUserLevelsForRegister(db)
	return nil
}

// ensureUserDeviceLimitOverrideColumn 给 users 补 device_limit_override 列（137 §3.1，幂等）。
//
// 口径（137 §3.1 原表）：
//
//	-1  不覆盖，用等级值（默认）
//	 0  不限
//	N>0 具体台数
//
// ⚠️ 必须 `NOT NULL DEFAULT -1` 而不是可空：NULL 在 GORM 里要指针类型，且
// `WHERE device_limit_override >= 0` 与 `IS NOT NULL` 两种判据容易写混。
// 存量行一律落 -1（= 不覆盖），因此本迁移**没有数据回填**，且对既有行为零影响。
func ensureUserDeviceLimitOverrideColumn(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameUser) {
		return nil
	}
	if db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		return nil
	}
	if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
		" ADD COLUMN device_limit_override integer NOT NULL DEFAULT -1").Error; err != nil {
		return err
	}
	logger.SysLog("已为 users 补上 device_limit_override 列（-1 = 不覆盖等级上限）")
	return nil
}

// ensureUserLevelIDColumn 给 users 补 level_id 列（幂等）。
func ensureUserLevelIDColumn(db *gorm.DB) error {
	if db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		return nil
	}
	// 方言无关的最小 DDL：MySQL 与 SQLite 都接受 integer NOT NULL DEFAULT 0。
	// 不用 db.Migrator().AddColumn 的原因见 ensureUserLevelSchema 的注释
	// （生成文件里没有 LevelID 字段，AddColumn 拿不到列定义）。
	if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
		" ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
		return err
	}
	logger.SysLog("已为 users 补上 level_id 列（等级体系，0 = 无等级）")
	return nil
}

// seedDefaultUserLevels 只在空表时写入初始等级。
//
// 幂等判定用 COUNT：`First` 会因软删除之外的任何残留行改变行为，
// 而 COUNT 能准确回答「有没有人配过」这个原始问题。
// 失败只记日志 —— 初始数据不是结构性依赖，管理员可以在后台补。
func seedDefaultUserLevels(db *gorm.DB) {
	var count int64
	if err := db.Model(&entity.UserLevel{}).Count(&count).Error; err != nil {
		logger.SysLog(fmt.Sprintf("读取等级表行数失败（跳过初始数据）：%v", err))
		return
	}
	if count > 0 {
		return
	}
	if err := db.Create(&defaultUserLevels).Error; err != nil {
		logger.SysLog(fmt.Sprintf("写入初始等级失败（管理员可在后台补）：%v", err))
		return
	}
	logger.SysLog(fmt.Sprintf("已写入 %d 个初始等级", len(defaultUserLevels)))
}

// userLevelBackfillSchemaKey 标记「存量 level_id=0 的用户已经补评估过」（A1-6）。
//
// 为什么需要标记：回填是「扫全部 level_id=0 的用户逐个评估」，而 level_id=0
// 在**每次新注册但还没评估时**也是这个值。没有标记的话每次启动都会把
// 「刚好在注册与评估之间」的用户重扫一遍 —— 数量小、幂等无害，但会掩盖
// 「回填根本没跑成功」这件事（因为下次启动会重来，看起来一直正常工作）。
//
// 键名沿用 EnsureXxxSchema 的命名习惯，**不要改名**：改名等于让已回填过的库重跑一遍。
const userLevelBackfillSchemaKey = "user_level_register_backfill_v1"

// backfillUserLevelsForRegister 给存量 level_id=0 的用户补一次注册级评估（137 §10.3 / A1-6）。
//
// ## 为什么必须回填
//
// 137 §10.3 / §12.0 的推论：设备上限只在「用户有等级」时才可能生效
// （EffectiveDeviceLimit 在 level_id=0 时返回不限）。而本批次之前
// **没有任何一条路径会评估新用户** —— 充值/消费/退款三个触发器都要求
// 用户已经有账单。⇒ 存量用户（以及所有新注册用户）level_id 恒为 0，
// 设备上限对他们是**永久不生效**，且不报任何错。
//
// ## 幂等
//
// 用 system_settings 标记（FLAG 型）+ 回填成功后写标记。失败**不写标记**，
// 下次启动会重跑（与其它 FLAG 型迁移一致）。
//
// 单条用户评估失败不阻断整体：记 SysLog 后继续下一个人（回填是逐用户的，
// 一个用户的脏数据没有理由让其余几万人都不被评级）。
// 但**有失败时不写标记** —— 否则下次启动不会重试，那批用户永久无等级。
func backfillUserLevelsForRegister(db *gorm.DB) {
	if db == nil {
		return
	}
	if !userLevelSchemaAvailableForRegistry(db) {
		return
	}
	if systemSettingFlagIsSet(db, userLevelBackfillSchemaKey) {
		return
	}
	// 等级表为空时不做任何事，也**不写标记**：
	// EvaluateUserLevelWithDB 在 len(levels)==0 时早退（不评估、不降级），
	// 此时回填等于一次全表扫描零收益；等管理员配好等级后下次启动再跑。
	var levelCount int64
	if err := db.Model(&entity.UserLevel{}).Count(&levelCount).Error; err != nil {
		logger.SysLog(fmt.Sprintf("读取等级表行数失败（跳过注册级回填）：%v", err))
		return
	}
	if levelCount == 0 {
		return
	}

	var ids []int32
	if err := db.Table(entity.TableNameUser).
		Where("level_id = ?", 0).Order("id asc").Pluck("id", &ids).Error; err != nil {
		logger.SysLog(fmt.Sprintf("读取待回填用户失败（下次启动会重试）：%v", err))
		return
	}
	if len(ids) == 0 {
		// 没有待回填用户：直接写标记，避免每次启动都扫一遍全表。
		if err := setSystemSettingFlag(db, userLevelBackfillSchemaKey); err != nil {
			logger.SysLog(fmt.Sprintf("写入注册级回填标记失败（下次启动会重扫，幂等）：%v", err))
		}
		return
	}

	failed := 0
	changed := 0
	for _, userID := range ids {
		result, err := service.EvaluateUserLevelWithDB(db, userID, entity.UserLevelTriggerRegister)
		if err != nil {
			failed++
			logger.SysLog(fmt.Sprintf("注册级回填评估失败（userID=%d，下次启动会重试）：%v", userID, err))
			continue
		}
		if !result.Evaluated {
			failed++
			continue
		} // Missing evidence is not a completed registration evaluation.
		if result.Changed {
			changed++
		}
	}
	logger.SysLog(fmt.Sprintf("注册级回填补评估完成：候选 %d 人，变更 %d 人，失败 %d 人", len(ids), changed, failed))
	if failed > 0 {
		// 有失败 ⇒ 不写标记，下次启动重跑（已评估过的人 level_id 不再是 0，
		// 因此重跑的候选集只会包含失败的那批，幂等且收敛）。
		return
	}
	if err := setSystemSettingFlag(db, userLevelBackfillSchemaKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入注册级回填标记失败（下次启动会重扫，幂等）：%v", err))
	}
}

// userLevelSchemaAvailableForRegistry 判断等级体系是否就绪（registry 侧视角）。
//
// 为什么不直接调 service.userLevelSchemaAvailable：那是**未导出**函数，
// registry 包不能访问（service 的同包私有）。这里只做结构判断，
// 语义与 service 侧一致：users 表 + level_id 列 + user_levels 表。
//
// ⚠️ 判断逻辑两处重复是刻意的：registry 不能 import service 的私有函数，
// 而把 userLevelSchemaAvailable 导出会让它变成跨包 API（改签名要考虑两处）。
// 保持「结构判断各写一份、口径一致」比导出一个只被 migration 用的函数更稳。
func userLevelSchemaAvailableForRegistry(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUserLevel) {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameUser, "level_id")
}

// Shared by the startup migration and the isolated restored-copy verifier.
// Only creates the append-only audit schema; never evaluates existing users.
func ensureEffectiveAuditSchema(db *gorm.DB) error {
	// Early unpublished candidates used the default Go field spelling as a
	// database identifier; preserve any opt-in trial receipts across the rename.
	if db.Migrator().HasTable(&entity.EffectiveRebuildAudit{}) && db.Migrator().HasColumn("effective_rebuild_audits", "examined") && !db.Migrator().HasColumn("effective_rebuild_audits", "scanned_count") {
		if err := db.Migrator().RenameColumn("effective_rebuild_audits", "examined", "scanned_count"); err != nil {
			return err
		}
	}
	// SQLite startup intentionally suppresses generic column alterations. Only
	// this unpublished audit table needs an exact integer -> text conversion;
	// unlike REAL, both old INTEGER and the new digit strings are lossless.
	audit := &entity.EffectiveAdjustment{}
	if db.Dialector.Name() == "sqlite" && db.Migrator().HasTable(audit) {
		cols, err := db.Migrator().ColumnTypes(audit)
		if err != nil {
			return err
		}
		for _, col := range cols {
			var field string
			switch col.Name() {
			case "old_micros":
				field = "OldMicros"
			case "new_micros":
				field = "NewMicros"
			default:
				continue
			}
			if strings.Contains(strings.ToUpper(col.DatabaseTypeName()), "INT") {
				if err := db.Migrator().AlterColumn(audit, field); err != nil {
					return err
				}
			}
		}
	}
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}, &entity.EffectiveRebuildAudit{}); err != nil {
		return err
	}
	return db.AutoMigrate(&entity.UserLevelHistory{})
}
