package registry

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	// tikuHashWithoutPlatBackfilledKey 标记「题库 hash 已按新公式（不含 plat）回填」。
	tikuHashWithoutPlatBackfilledKey = "tiku_hash_without_plat_backfilled"
	// tikuPlatColumnDroppedKey 标记「tiku.plat 列已删除」。
	tikuPlatColumnDroppedKey = "tiku_plat_column_dropped"
	// tikuHashBackfillBatchSize 分批回填的批大小，避免整表进内存。
	tikuHashBackfillBatchSize = 500
)

// ensureTikuHashWithoutPlatSchema 把题库 hash 迁移到不含 plat 的新公式。
//
// 背景：plat 曾参与 hash（md5(题面 + 排序选项 + type + plat)），
// 但它在**所有调用方**恒为 -1，没有任何区分度，因此公式已去掉该维度。
// 公式一变，历史行的 hash 就与新查询对不上，题库会静默失联 —— 必须回填。
//
// 与既有 ensure* 迁移一致的三条纪律：
//  1. 用 system_settings 标记保证只跑一次，失败不写标记、下次启动自动重试；
//  2. **回填失败只记日志、不阻断启动**（题库匹配是功能增强，不该让服务起不来）；
//  3. 外层 recover 兜底，脏数据不能让服务反复崩溃重启。
func ensureTikuHashWithoutPlatSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameTiku) {
		return nil
	}
	if tikuHashBackfillDone(db) {
		return nil
	}

	if err := safeBackfillTikuHashWithoutPlat(db); err != nil {
		logger.SysLog(fmt.Sprintf("题库 hash 回填失败（不影响启动，将保留在下次启动重试）：%v", err))
		return nil
	}
	if err := markTikuHashBackfillDone(db); err != nil {
		logger.SysLog(fmt.Sprintf("写入题库 hash 回填标记失败（下次启动会重新回填）：%v", err))
	}
	return nil
}

// safeBackfillTikuHashWithoutPlat 在回填外面兜一层 recover。
func safeBackfillTikuHashWithoutPlat(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return backfillTikuHashWithoutPlat(db)
}

type tikuHashRow struct {
	ID       int32
	Question string
	Type     int32
	Options  string
	Hash     string
}

// backfillTikuHashWithoutPlat 分批把历史行的 hash 改写成新公式。
//
// ⚠️ 唯一键冲突必须「跳过并继续」，不能中止整个回填。
// 线上事故（2026-10-16）：第 3 批某行的新 hash 与同 course_name 的已有行撞
// `UNIQUE constraint failed: tiku.hash, tiku.course_name`，旧实现直接 return err，
// 标记因此永远写不上 → 每次启动都重跑、永远不收敛。
// 这类冲突说明两行本来就是同一道题（唯一索引不允许共存），
// 跳过即可，绝不能删任何数据。
func backfillTikuHashWithoutPlat(db *gorm.DB) error {
	lastID := int32(0)
	total := 0
	skipped := 0

	for {
		rows := make([]tikuHashRow, 0, tikuHashBackfillBatchSize)
		if err := db.Table(entity.TableNameTiku).
			Select("id, question, type, options, hash").
			Where("id > ?", lastID).
			Order("id asc").
			Limit(tikuHashBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			lastID = row.ID

			options := parseTikuOptions(row.Options)
			expected := util.BuildTikuHash(row.Question, options, int(row.Type))
			if strings.TrimSpace(row.Hash) == expected {
				// 已经是新公式，跳过（幂等）。
				continue
			}
			if err := db.Table(entity.TableNameTiku).
				Where("id = ?", row.ID).
				Update("hash", expected).Error; err != nil {
				if isUniqueConstraintViolation(err) {
					// 与同 course_name 的已有行撞唯一键：保留原 hash，跳过该行。
					skipped++
					continue
				}
				return err
			}
			total++
		}
	}

	if total > 0 || skipped > 0 {
		logger.SysLog(fmt.Sprintf("题库 hash 已迁移到不含 plat 的新公式，更新 %d 行，跳过 %d 行（与已有行唯一键冲突，保留原值）", total, skipped))
	}
	return nil
}

// isUniqueConstraintViolation 判断数据库错误是否是唯一键冲突。
//
// 覆盖 SQLite（UNIQUE constraint failed / 2067）与 MySQL（Duplicate entry / 1062）。
// GORM 只在开启 TranslateError 时才返回 ErrDuplicatedKey，本项目没开，因此按文本判断。
func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"unique constraint failed", "duplicate entry", "sqlite_constraint_unique"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// parseTikuOptions 解析入库的选项 JSON；坏数据按空选项处理（与 FillHash 行为一致）。
func parseTikuOptions(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	options := make([]string, 0, 4)
	if err := json.Unmarshal([]byte(trimmed), &options); err != nil {
		return nil
	}
	return options
}

func tikuHashBackfillDone(db *gorm.DB) bool {
	return systemSettingFlagIsSet(db, tikuHashWithoutPlatBackfilledKey)
}

func markTikuHashBackfillDone(db *gorm.DB) error {
	return setSystemSettingFlag(db, tikuHashWithoutPlatBackfilledKey)
}

// ensureTikuPlatColumnDropped 删除已无用的 tiku.plat 列。
//
// plat 已从模型彻底移除（历史取值恒为 -1，无区分度）。列本身是 NOT NULL 且无默认值，
// 留着会让每次 INSERT 都依赖列默认值；删掉更干净。
//
// 与 hash 回填分开标记：两者互不依赖，任一失败都不该拖累另一个。
func ensureTikuPlatColumnDropped(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameTiku) {
		return nil
	}
	if systemSettingFlagIsSet(db, tikuPlatColumnDroppedKey) {
		return nil
	}
	if !db.Migrator().HasColumn(entity.TableNameTiku, "plat") {
		// 全新库本来就没有该列，直接标记完成。
		if err := setSystemSettingFlag(db, tikuPlatColumnDroppedKey); err != nil {
			logger.SysLog(fmt.Sprintf("写入 tiku.plat 删除标记失败：%v", err))
		}
		return nil
	}

	// 列删除是结构性变更：失败要让人看见，但不能阻断启动（列留着不影响功能）。
	if err := db.Exec("ALTER TABLE " + entity.TableNameTiku + " DROP COLUMN plat").Error; err != nil {
		logger.SysLog(fmt.Sprintf("删除 tiku.plat 列失败（不影响启动，列将残留）：%v", err))
		return nil
	}
	logger.SysLog("已删除 tiku.plat 列（该字段恒为 -1，已从模型彻底移除）")

	if err := setSystemSettingFlag(db, tikuPlatColumnDroppedKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入 tiku.plat 删除标记失败：%v", err))
	}
	return nil
}

// systemSettingFlagIsSet 读取 system_settings 里的一次性布尔标记。
func systemSettingFlagIsSet(db *gorm.DB, key string) bool {
	if db == nil || !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return false
	}
	var setting entity.SystemSetting
	// key 是 MySQL/MariaDB 保留字：用 map 条件让 GORM 生成 `key`，否则 MySQL 上条件语法报错、
	// 标记永远读不到也写不进，启动迁移会反复重跑并 FATAL。
	if err := db.Where(map[string]any{"key": key}).First(&setting).Error; err != nil {
		return false
	}
	return strings.TrimSpace(setting.Value) == "1"
}

// setSystemSettingFlag 写入一次性布尔标记。
func setSystemSettingFlag(db *gorm.DB, key string) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return fmt.Errorf("system_settings 表不存在")
	}
	_ = db.Where(map[string]any{"key": key}).Delete(&entity.SystemSetting{}).Error
	return db.Create(&entity.SystemSetting{Key: key, Value: "1"}).Error
}
