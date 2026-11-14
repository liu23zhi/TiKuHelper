package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	// adminScriptActivationSchemaReadyKey 标记「管理员预生成激活码表已就绪」。
	adminScriptActivationSchemaReadyKey = "admin_script_activation_code_schema_v1"
)

// EnsureAdminScriptActivationCodeSchema 建立管理员预生成脚本激活码所需的表结构。
//
// 三条纪律（与 ensureWhitelistSchema 对齐）：
//   - 建表/建索引失败 → 返回错误，让启动 FATAL 看见，不静默跳过；
//   - **没有任何数据回填、不 DROP 任何表**：激活码是运营凭据，删了就没法给客服
//     核对历史；
//   - 用 system_settings 标记保证只跑一次；失败不写标记，下次启动幂等重跑。
//
// 索引策略：Code 上的唯一索引是**一次性语义的物理保障**（并发兑换靠唯一约束
// + 条件更新决出唯一赢家），所以它必须由建表语句一并建立，不能等 AutoMigrate
// 的「补建索引」分支 —— 老库若已跑过标记却缺这个索引，兑换就不再原子。
func EnsureAdminScriptActivationCodeSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, adminScriptActivationSchemaReadyKey) {
		// 标记已写不代表索引一定在（AutoMigrate 曾失败、或索引被人工删除），
		// 这里做一次幂等补建：HasIndex 只是一次元数据查询。
		if db.Migrator().HasTable(entity.TableNameAdminScriptActivationCode) {
			return ensureAdminScriptActivationCodeIndexes(db)
		}
	}
	if err := applyAdminScriptActivationCodeSchema(db); err != nil {
		return err
	}
	if err := setSystemSettingFlag(db, adminScriptActivationSchemaReadyKey); err != nil {
		return err
	}
	return nil
}

func applyAdminScriptActivationCodeSchema(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("建立管理员脚本激活码表时发生 panic：%v", recovered)
		}
	}()
	if err := db.AutoMigrate(&entity.AdminScriptActivationCode{}); err != nil {
		return fmt.Errorf("create %s fail: %w", entity.TableNameAdminScriptActivationCode, err)
	}
	return ensureAdminScriptActivationCodeIndexes(db)
}

// ensureAdminScriptActivationCodeIndexes 幂等补建索引。
//
// 显式建而不是只靠 AutoMigrate 的 tag：唯一索引是「一次性」的物理保障，
// 若某个老库在 AutoMigrate 期间建索引失败而表已建好，后续启动必须能补上。
func ensureAdminScriptActivationCodeIndexes(db *gorm.DB) error {
	table := entity.TableNameAdminScriptActivationCode
	if !db.Migrator().HasTable(table) {
		return nil
	}
	indexes := []struct {
		name   string
		column string
		unique bool
	}{
		// Code 唯一：防重复生成，同时让并发兑换只剩一个事务能成功。
		{"idx_admin_script_activation_code", "code", true},
		{"idx_admin_script_activation_status", "status", false},
		{"idx_admin_script_activation_used_by", "used_by", false},
	}
	for _, index := range indexes {
		if db.Migrator().HasIndex(table, index.name) {
			continue
		}
		unique := ""
		if index.unique {
			unique = "UNIQUE "
		}
		statement := fmt.Sprintf(
			"CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			unique, index.name, table, index.column,
		)
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create index %s on %s fail: %w", index.name, table, err)
		}
	}
	return nil
}
