package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// userRegisterIPBackfilledKey 标记「users.register_ip 已尽力回填」。
	userRegisterIPBackfilledKey = "user_register_ip_backfilled"
)

// ensureUserRegisterIPSchema 为用户表补上 register_ip 列，并尽力回填历史数据。
//
// 回填只能「尽力」：早期注册没有记录来源 IP，只能从 logs 表里找该用户
// module='auth' 且 action=注册 的最早一条。日志可能已被清理，因此回填不保证完整，
// 拿不到就留空 —— 界面按「只显示原始值」降级。
//
// 与既有 ensure* 迁移一致：
//   - 建列失败返回错误（结构性故障要让人看见）；
//   - 回填失败只记日志、不阻断启动；
//   - 外层 recover 兜底。
func ensureUserRegisterIPSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "register_ip") {
		// SQLite 走原生 ALTER（与 ensureUserPhoneBindingSchemaForDriver 同构：
		// sqlite 驱动对 AddColumn 的支持不如其它驱动器稳定）。
		if strings.EqualFold(db.Dialector.Name(), "sqlite") {
			if err := db.Exec("ALTER TABLE " + entity.TableNameUser + " ADD COLUMN register_ip varchar(64)").Error; err != nil {
				return err
			}
		} else if err := db.Migrator().AddColumn(&entity.User{}, "RegisterIP"); err != nil {
			return err
		}
	}

	if systemSettingFlagIsSet(db, userRegisterIPBackfilledKey) {
		return nil
	}
	if err := safeBackfillUserRegisterIP(db); err != nil {
		logger.SysLog(fmt.Sprintf("注册 IP 回溯失败（不影响启动，将保留在下次启动重试）：%v", err))
		return nil
	}
	if err := setSystemSettingFlag(db, userRegisterIPBackfilledKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入注册 IP 回溯标记失败（下次启动会重新回溯）：%v", err))
	}
	return nil
}

// safeBackfillUserRegisterIP 在回填外面兜一层 recover。
func safeBackfillUserRegisterIP(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return backfillUserRegisterIP(db)
}

type userRegisterIPRow struct {
	ID         int32
	RegisterIP string
}

// backfillUserRegisterIP 从审计日志里尽力还原每个用户的注册 IP。
func backfillUserRegisterIP(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameLog) {
		// 没有日志表就无法回溯，标记为已完成避免每次启动重复尝试。
		return nil
	}

	rows := make([]userRegisterIPRow, 0, 500)
	if err := db.Table(entity.TableNameUser).
		Select("id, register_ip").
		Where("COALESCE(register_ip, '') = ''").
		Order("id asc").
		Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	updated := 0
	for _, row := range rows {
		var clientIP string
		// 取该用户最早一条带 IP 的认证日志作为注册来源。
		if err := db.Table(entity.TableNameLog).
			Select("client_ip").
			Where("user_id = ? AND module = ? AND COALESCE(client_ip, '') <> ''", row.ID, "auth").
			Order("create_time asc").
			Limit(1).
			Scan(&clientIP).Error; err != nil {
			return err
		}
		clientIP = strings.TrimSpace(clientIP)
		if clientIP == "" {
			continue
		}
		if err := db.Table(entity.TableNameUser).
			Where("id = ?", row.ID).
			Update("register_ip", clientIP).Error; err != nil {
			return err
		}
		updated++
	}

	if updated > 0 {
		logger.SysLog(fmt.Sprintf("注册 IP 已从审计日志回溯，共回填 %d 个用户（其余用户日志已清理，保持为空）", updated))
	}
	return nil
}
