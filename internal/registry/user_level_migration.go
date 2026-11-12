package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
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
	if err := db.AutoMigrate(&entity.UserLevel{}); err != nil {
		return fmt.Errorf("create user_levels fail: %w", err)
	}
	if err := db.AutoMigrate(&entity.UserLevelHistory{}); err != nil {
		return fmt.Errorf("create user_level_histories fail: %w", err)
	}
	if err := ensureUserLevelIDColumn(db); err != nil {
		return fmt.Errorf("add users.level_id fail: %w", err)
	}
	seedDefaultUserLevels(db)
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
