package registry

import (
	"fmt"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/dao"
	"gorm.io/driver/mysql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"sync"
)

type userWechatBindingSchema struct {
	WechatID      *string    `gorm:"column:wechat_id;type:varchar(128)"`
	WechatBoundAt *time.Time `gorm:"column:wechat_bound_at"`
}

func (*userWechatBindingSchema) TableName() string {
	return entity.TableNameUser
}

var db *gorm.DB
var once sync.Once
var idempotencyCleanupStop func()

// CloseDB close db
func CloseDB() error {
	if idempotencyCleanupStop != nil {
		idempotencyCleanupStop()
		idempotencyCleanupStop = nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	if err != nil {
		return err
	}
	return nil
}

func ensureAccountDomainDefaults(db *gorm.DB) {
	updates := []string{
		"UPDATE users SET role = 'admin' WHERE id = 1 AND (role IS NULL OR role = '')",
		"UPDATE users SET role = 'user' WHERE id <> 1 AND (role IS NULL OR role = '')",
		"UPDATE users SET status = 'enabled' WHERE status IS NULL OR status = ''",
		"UPDATE users SET register_source = 'system' WHERE id = 1 AND (register_source IS NULL OR register_source = '')",
		"UPDATE users SET register_source = 'legacy' WHERE id <> 1 AND (register_source IS NULL OR register_source = '')",
		"UPDATE users SET balance = 0 WHERE balance IS NULL",
	}
	for _, statement := range updates {
		if err := db.Exec(statement).Error; err != nil {
			logger.FatalLog(fmt.Errorf("initialize account-domain defaults fail: %w", err))
		}
	}
}

func ensureUserPhoneBindingSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := ensureUserPhoneBindingSchemaForDriver(db, db.Dialector.Name()); err != nil {
		return err
	}
	return ensureUserWechatBindingSchemaForDriver(db, db.Dialector.Name())
}

func ensureUserPhoneBindingSchemaForDriver(db *gorm.DB, driverName string) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil
	}

	normalizedDriver := strings.TrimSpace(strings.ToLower(driverName))
	if normalizedDriver == "" {
		normalizedDriver = strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	}

	if normalizedDriver == "sqlite" {
		if !db.Migrator().HasColumn(entity.TableNameUser, "phone") {
			if err := db.Exec("ALTER TABLE users ADD COLUMN phone varchar(32)").Error; err != nil {
				return err
			}
		}
		if !db.Migrator().HasColumn(entity.TableNameUser, "phone_bound_at") {
			if err := db.Exec("ALTER TABLE users ADD COLUMN phone_bound_at datetime").Error; err != nil {
				return err
			}
		}
		if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_phone") {
			if err := db.Exec("CREATE UNIQUE INDEX idx_users_phone ON users(phone)").Error; err != nil {
				return err
			}
		}
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "phone") {
		if err := db.Migrator().AddColumn(&entity.User{}, "Phone"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "phone_bound_at") {
		if err := db.Migrator().AddColumn(&entity.User{}, "PhoneBoundAt"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_phone") {
		if err := db.Migrator().CreateIndex(&entity.User{}, "Phone"); err != nil {
			return err
		}
	}
	return nil
}

func ensureUserWechatBindingSchemaForDriver(db *gorm.DB, driverName string) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil
	}

	normalizedDriver := strings.TrimSpace(strings.ToLower(driverName))
	if normalizedDriver == "" {
		normalizedDriver = strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	}

	if normalizedDriver == "sqlite" {
		if !db.Migrator().HasColumn(entity.TableNameUser, "wechat_id") {
			if err := db.Exec("ALTER TABLE users ADD COLUMN wechat_id varchar(128)").Error; err != nil {
				return err
			}
		}
		if !db.Migrator().HasColumn(entity.TableNameUser, "wechat_bound_at") {
			if err := db.Exec("ALTER TABLE users ADD COLUMN wechat_bound_at datetime").Error; err != nil {
				return err
			}
		}
		if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_wechat_id") {
			if err := db.Exec("CREATE UNIQUE INDEX idx_users_wechat_id ON users(wechat_id)").Error; err != nil {
				return err
			}
		}
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "wechat_id") {
		if err := db.Migrator().AddColumn(&userWechatBindingSchema{}, "WechatID"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "wechat_bound_at") {
		if err := db.Migrator().AddColumn(&userWechatBindingSchema{}, "WechatBoundAt"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_wechat_id") {
		if err := db.Migrator().CreateIndex(&entity.User{}, "WechatID"); err != nil {
			return err
		}
	}
	return nil
}

func ensureSMSVerificationSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return ensureSMSVerificationSchemaForDriver(db, db.Dialector.Name())
}

func ensureSMSVerificationSchemaForDriver(db *gorm.DB, driverName string) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameSMSVerificationCode) {
		return nil
	}

	normalizedDriver := strings.TrimSpace(strings.ToLower(driverName))
	if normalizedDriver == "" {
		normalizedDriver = strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	}

	if normalizedDriver == "sqlite" {
		if !db.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
			legacyTableName := entity.TableNameSMSVerificationCode + "__legacy_scene"
			statements := []string{
				"ALTER TABLE sms_verification_codes RENAME TO sms_verification_codes__legacy_scene",
				"CREATE TABLE sms_verification_codes (`id` integer PRIMARY KEY AUTOINCREMENT,`phone` varchar(32) NOT NULL,`scene` varchar(32) NOT NULL DEFAULT '',`code_hash` char(64) NOT NULL,`status` varchar(32) NOT NULL,`expires_at` datetime NOT NULL,`consumed_at` datetime,`request_ip` varchar(64),`created_at` datetime,`updated_at` datetime)",
				"INSERT INTO sms_verification_codes (`id`,`phone`,`scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at`) SELECT `id`,`phone`,'' AS `scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at` FROM sms_verification_codes__legacy_scene",
				"DROP TABLE sms_verification_codes__legacy_scene",
			}
			for _, statement := range statements {
				if err := db.Exec(statement).Error; err != nil {
					_ = db.Exec("ALTER TABLE " + legacyTableName + " RENAME TO " + entity.TableNameSMSVerificationCode).Error
					return err
				}
			}
		}
		if !db.Migrator().HasIndex(entity.TableNameSMSVerificationCode, "idx_sms_verification_codes_scene") {
			if err := db.Exec("CREATE INDEX idx_sms_verification_codes_scene ON sms_verification_codes(scene)").Error; err != nil {
				return err
			}
		}
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
		if err := db.Migrator().AddColumn(&entity.SMSVerificationCode{}, "Scene"); err != nil {
			return err
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameSMSVerificationCode, "idx_sms_verification_codes_scene") {
		if err := db.Migrator().CreateIndex(&entity.SMSVerificationCode{}, "Scene"); err != nil {
			return err
		}
	}
	return nil
}

// billingQuestionTextBackfilledKey 记录历史账单的题目回溯只跑过一次。
const billingQuestionTextBackfilledKey = "billing_question_text_backfilled"

// billingQuestionTextBackfillBatchSize 控制回溯时的批大小，避免一次性把整表读进内存。
const billingQuestionTextBackfillBatchSize = 500

// billingQuestionTextMaxLength 是 question_text 列允许的最大字符数。
// 注意是「字符数」：varchar(512) 在 MySQL 下按字符计数。
const billingQuestionTextMaxLength = 512

// ensureBillingQuestionTextSchema 为账单表补上 question_text 列，
// 并把历史账单里 URL 编码在 unit_price_snapshot 中的题目解码后回填一次。
//
// 用 system_settings 标记保证只跑一次；中途失败不会写标记，下次启动会继续。
//
// 建列失败会返回错误（结构性故障必须让人看到）；但**回溯失败只记录日志**：
// 题目回填只是搜索增强，不能因为它让整个服务起不来。
func ensureBillingQuestionTextSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil
	}

	if !db.Migrator().HasColumn(&entity.BillingRecord{}, "question_text") {
		if err := db.Migrator().AddColumn(&entity.BillingRecord{}, "QuestionText"); err != nil {
			return err
		}
	}

	if billingQuestionTextBackfillDone(db) {
		return nil
	}

	if err := safeBackfillBillingQuestionText(db); err != nil {
		logger.SysLog(fmt.Sprintf("账单题目回溯失败（不影响启动，将保留在下次启动重试）：%v", err))
		return nil
	}
	if err := markBillingQuestionTextBackfilled(db); err != nil {
		logger.SysLog(fmt.Sprintf("写入账单题目回溯标记失败（下次启动会重新回溯）：%v", err))
	}
	return nil
}

// safeBackfillBillingQuestionText 在回溯外面兜一层 recover。
// 历史快照格式不可控，任何一条脏数据都不应该让服务反复崩溃重启。
func safeBackfillBillingQuestionText(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return backfillBillingQuestionText(db)
}

func billingQuestionTextBackfillDone(db *gorm.DB) bool {
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return false
	}
	var setting entity.SystemSetting
	if err := db.Where("key = ?", billingQuestionTextBackfilledKey).First(&setting).Error; err != nil {
		return false
	}
	return strings.TrimSpace(setting.Value) == "1"
}

func markBillingQuestionTextBackfilled(db *gorm.DB) error {
	_ = db.Where("key = ?", billingQuestionTextBackfilledKey).Delete(&entity.SystemSetting{}).Error
	return db.Create(&entity.SystemSetting{
		Key:   billingQuestionTextBackfilledKey,
		Value: "1",
	}).Error
}

// backfillBillingQuestionText 分批把历史账单的题目从快照里解出来写进新列。
func backfillBillingQuestionText(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("账单题目回溯：数据库未初始化")
	}

	for {
		var rows []entity.BillingRecord
		err := db.
			Select("id, unit_price_snapshot, question_text").
			Where("(question_text IS NULL OR question_text = '') AND unit_price_snapshot LIKE ?", "%question=%").
			Order("id asc").
			Limit(billingQuestionTextBackfillBatchSize).
			Find(&rows).Error
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") {
				return nil
			}
			return err
		}
		if len(rows) == 0 {
			return nil
		}

		updated := 0
		for _, row := range rows {
			question := service.ExtractBillingSnapshotField(row.UnitPriceSnapshot, "question")
			if question == "" {
				continue
			}
			// 必须按「字符数」而不是字节数截断：
			// question 多为中文，len() 是字节长度，直接切 []rune 会越界 panic。
			question = truncateRunes(question, billingQuestionTextMaxLength)
			if updateErr := db.Model(&entity.BillingRecord{}).
				Where("id = ?", row.ID).
				Update("question_text", question).Error; updateErr != nil {
				return updateErr
			}
			updated++
		}

		if updated == 0 {
			// 本批没有任何可解析的题目：直接结束，避免死循环。
			return nil
		}
		if len(rows) < billingQuestionTextBackfillBatchSize {
			return nil
		}
	}
}

// truncateRunes 按字符数截断字符串，返回值不会破坏多字节字符。
func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

// ensureAIModelPriceSchema 为历史价格表补上新增列。
// GORM 的 AutoMigrate 会处理新列，但旧库上先手动兜底可以避免
// 「列缺失 → 查询报错 → 计费整体失败」；已存在时是空操作。
func ensureAIModelPriceSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameAIModelPrice) {
		return nil
	}

	columns := []struct {
		field string
		name  string
	}{
		{field: "CacheWritePrice", name: "cache_write_price"},
		{field: "Multiplier", name: "multiplier"},
	}

	for _, column := range columns {
		if db.Migrator().HasColumn(entity.TableNameAIModelPrice, column.name) {
			continue
		}
		if err := db.Migrator().AddColumn(&entity.AIModelPrice{}, column.field); err != nil {
			return err
		}
	}

	// 历史行补默认倍率 1，避免出现 0 倍率导致"命中价格却按 0 计费"。
	return db.Model(&entity.AIModelPrice{}).
		Where("multiplier IS NULL OR multiplier <= 0").
		Update("multiplier", 1).Error
}

// moneyColumn 描述一个需要按 6 位小数存储的金额列。
type moneyColumn struct {
	Table  string
	Column string
}

// ensureMoneyPrecisionSchema 把历史 decimal(18,2) 金额列扩宽到 decimal(18,6)。
//
// 背景：余额与账务金额改为 6 位小数后，单次 AI 调用可能只有 0.000062 元，
// 若列本身仍是 2 位精度（MySQL 的 DECIMAL 会真实截断），这些扣费会被抹成 0。
// SQLite 只有 NUMERIC 亲和性、不做定标截断，且 AutoMigrate 会顺带改写建表语句，
// 因此这里只对真正会截断的驱动做显式迁移。
func ensureMoneyPrecisionSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	normalizedDriver := strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	if normalizedDriver == "sqlite" || normalizedDriver == "" {
		return nil
	}

	columns := []moneyColumn{
		{Table: entity.TableNameUser, Column: "Balance"},
		{Table: entity.TableNameBillingRecord, Column: "BalanceBefore"},
		{Table: entity.TableNameBillingRecord, Column: "BalanceAfter"},
		{Table: entity.TableNameAccessToken, Column: "TotalQuotaLimit"},
		{Table: entity.TableNameAccessToken, Column: "TotalAmountUsed"},
		{Table: entity.TableNameRechargeOrder, Column: "Amount"},
	}

	for _, item := range columns {
		narrow, err := moneyColumnNeedsWidening(db, item)
		if err != nil {
			return err
		}
		if !narrow {
			continue
		}
		if err := alterMoneyColumn(db, item); err != nil {
			return err
		}
		logger.SysLog(fmt.Sprintf("已将 %s.%s 的金额精度扩展到 6 位小数", item.Table, item.Column))
	}
	return nil
}

// moneyColumnNeedsWidening 只在列的小数位不足 6 位时才返回 true，
// 避免每次启动都对大表做一次 MODIFY COLUMN。
func moneyColumnNeedsWidening(db *gorm.DB, item moneyColumn) (bool, error) {
	if !db.Migrator().HasTable(item.Table) || !db.Migrator().HasColumn(item.Table, item.Column) {
		return false, nil
	}

	columnTypes, err := db.Migrator().ColumnTypes(item.Table)
	if err != nil {
		return false, err
	}
	for _, columnType := range columnTypes {
		if !strings.EqualFold(columnType.Name(), item.Column) {
			continue
		}
		scale, ok := parseDecimalScale(columnType.DatabaseTypeName())
		if !ok {
			// 不是 decimal(x,y) 形态（例如 SQLite 报 "decimal"），交给 AutoMigrate 处理。
			return false, nil
		}
		return scale < util.MoneyPrecision, nil
	}
	return false, nil
}

// parseDecimalScale 从 "decimal(18,2)" 这类类型名里取出小数位数。
func parseDecimalScale(databaseType string) (int, bool) {
	normalized := strings.ToLower(strings.TrimSpace(databaseType))
	open := strings.Index(normalized, "(")
	closeIdx := strings.Index(normalized, ")")
	if !strings.HasPrefix(normalized, "decimal") || open < 0 || closeIdx < open {
		return 0, false
	}

	parts := strings.Split(normalized[open+1:closeIdx], ",")
	if len(parts) != 2 {
		return 0, false
	}
	scale, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, false
	}
	return scale, true
}

func alterMoneyColumn(db *gorm.DB, item moneyColumn) error {
	var model any
	switch item.Table {
	case entity.TableNameUser:
		model = &entity.User{}
	case entity.TableNameBillingRecord:
		model = &entity.BillingRecord{}
	case entity.TableNameAccessToken:
		model = &entity.AccessToken{}
	case entity.TableNameRechargeOrder:
		model = &entity.RechargeOrder{}
	default:
		return nil
	}
	return db.Migrator().AlterColumn(model, item.Column)
}

// RegisterDB 注册数据库
func RegisterDB(config configs.Config) *gorm.DB {
	once.Do(func() {
		var err error
		var conn gorm.Dialector
		if config.Mysql != "" {
			conn = mysql.Open(config.Mysql)
		} else if os.Getenv("SQL_DSN") != "" {
			conn = mysql.Open(os.Getenv("SQL_DSN"))
		} else {
			// 使用 SQLite，从配置读取路径，默认为 tiku.db（向后兼容）
			dbPath := config.Database.Path
			if dbPath == "" {
				dbPath = "tiku.db"
			}

			// 确保数据库文件所在目录存在
			dbDir := filepath.Dir(dbPath)
			if dbDir != "." && dbDir != "" {
				if err := os.MkdirAll(dbDir, 0755); err != nil {
					logger.FatalLog(fmt.Errorf("create database directory fail: %w", err))
				}
			}

			// 包一层：SQLite 下不再让 AutoMigrate 逐列 AlterColumn（每次都会整表重建），
			// 详见 sqlite_migrate.go。
			conn = sqliteQuietDialector{Dialector: sqlite.Open(dbPath)}
		}
		db, err = gorm.Open(conn, &gorm.Config{
			PrepareStmt: true,
			Logger:      newDatabaseLogger(),
		})
		if err != nil {
			logger.FatalLog(fmt.Errorf("open db fail: %w", err))
		}
		dao.SetDefault(db)
	})
	err := db.AutoMigrate(&entity.Tiku{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	if err := ensureUserPhoneBindingSchema(db); err != nil {
		logger.FatalLog(fmt.Errorf("prepare user phone binding schema fail: %w", err))
	}

	err = db.AutoMigrate(&entity.User{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	// register_ip 必须在 users 表存在之后补列；回填依赖 logs 表，失败只记日志。
	if err := runStartupMigration("ensureUserRegisterIPSchema", func() error { return ensureUserRegisterIPSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare user register ip schema fail: %w", err))
	}

	err = db.AutoMigrate(&entity.Log{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	err = db.AutoMigrate(&entity.SearchAccountConfig{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	err = db.AutoMigrate(&entity.AISearchConfig{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	if err := ensureSMSVerificationSchema(db); err != nil {
		logger.FatalLog(fmt.Errorf("prepare sms verification schema fail: %w", err))
	}

	err = db.AutoMigrate(
		&entity.AccessToken{},
		&entity.SystemSetting{},
		&entity.PriceRule{},
		&entity.PaymentMethod{},
		&entity.RechargeOrder{},
		&entity.IdempotencyRecord{},
		&entity.BillingRecord{},
		// ai_model_architectures（模型绑定）已移除：旧库中的表保留但不再由 AutoMigrate 管理。
		&entity.AIModelPrice{},
		&entity.SMSVerificationCode{},
		&entity.CaptchaChallenge{},
		&entity.CaptchaToken{},
		// 归属地查询结果的持久化缓存（节省按积分计费的 UAPI 调用）。
		&entity.LocationCache{},
	)
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}

	if err := runStartupMigration("ensureMoneyPrecisionSchema", func() error { return ensureMoneyPrecisionSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare money precision schema fail: %w", err))
	}

	if err := runStartupMigration("ensureAIModelPriceSchema", func() error { return ensureAIModelPriceSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare ai model price schema fail: %w", err))
	}

	if err := runStartupMigration("ensureBillingQuestionTextSchema", func() error { return ensureBillingQuestionTextSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare billing question text schema fail: %w", err))
	}

	// 账单来源 IP（供账单明细展示归属地）。历史行无法回溯，只建列。
	if err := runStartupMigration("ensureBillingClientIPSchema", func() error { return ensureBillingClientIPSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare billing client ip schema fail: %w", err))
	}

	// 账单搜索模式（深度思考 / 普通搜索），供账单明细与账单流水展示。历史行只建列。
	if err := runStartupMigration("ensureBillingSearchModeSchema", func() error { return ensureBillingSearchModeSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare billing search mode schema fail: %w", err))
	}

	// source-aware 归属地缓存使用新表，旧表保留以兼容 SQLite 和回滚；
	// 回填失败只记日志，下一次启动会因未写标记而重试。
	if err := runStartupMigration("ensureLocationCacheEntriesSchema", func() error { return ensureLocationCacheEntriesSchema(db) }); err != nil {
		logger.SysLog(fmt.Sprintf("归属地缓存迁移失败（不影响启动）：%v", err))
	}

	// 题库 hash 迁移与 plat 列删除。两者都依赖 system_settings 表，
	// 因此必须放在上面的 AutoMigrate（含 SystemSetting）之后。
	// 失败只记日志、不阻断启动，标记未写入会在下次启动重试。
	if err := runStartupMigration("ensureTikuHashWithoutPlatSchema", func() error { return ensureTikuHashWithoutPlatSchema(db) }); err != nil {
		logger.SysLog(fmt.Sprintf("题库 hash 迁移失败（不影响启动）：%v", err))
	}
	if err := runStartupMigration("ensureTikuPlatColumnDropped", func() error { return ensureTikuPlatColumnDropped(db) }); err != nil {
		logger.SysLog(fmt.Sprintf("tiku.plat 列删除失败（不影响启动）：%v", err))
	}

	ensureAccountDomainDefaults(db)

	// 首先需要注册一个默认用户
	bootstrapPassword := strings.TrimSpace(os.Getenv("TIKU_ADMIN_PASSWORD"))
	if bootstrapPassword == "" {
		bootstrapPassword = "123456"
		logger.SysLog("未设置 TIKU_ADMIN_PASSWORD，管理员初始化密码使用 123456；请登录后立即修改密码")
	}
	hashedBootstrapPassword, err := util.HashPassword(bootstrapPassword)
	if err != nil {
		logger.FatalLog(fmt.Errorf("hash bootstrap admin password fail: %w", err))
	}
	bootstrapAdminAccount(db, hashedBootstrapPassword)
	ensureAccountDomainDefaults(db)
	if idempotencyCleanupStop == nil {
		idempotencyCleanupStop = service.StartIdempotencyCleanup(db, time.Hour)
	}
	return db
}

// bootstrapAdminAccount 保证 id=1 的管理员存在。
//
// 不存在 → 创建；已存在且密码仍是空或明文 123456 → 升级为新哈希；
// **已存在时绝不再次插入** —— 历史实现是无条件 Create，管理员已存在时
// 每次启动都会撞 `users.id` 唯一键，被数据库日志记成一条 ERROR
// （线上每次启动都能看到，见 AGENTS.md 第 26 条）。
func bootstrapAdminAccount(db *gorm.DB, hashedPassword string) {
	if db == nil {
		return
	}
	var adminCount int64
	if err := db.Model(&entity.User{}).Where("id = ?", 1).Count(&adminCount).Error; err != nil {
		logger.SysLog(fmt.Sprintf("检查管理员账号失败（不影响启动）：%v", err))
	}
	if adminCount > 0 {
		var existing entity.User
		if err := db.Where("id = ?", 1).First(&existing).Error; err == nil &&
			(existing.Password == "" || existing.Password == "123456") {
			if updateErr := db.Model(&entity.User{}).Where("id = ?", 1).
				Update("password", hashedPassword).Error; updateErr != nil {
				logger.FatalLog(fmt.Errorf("migrate legacy admin password fail: %w", updateErr))
			}
		}
		return
	}

	user := entity.User{
		ID:             1,
		Username:       "admin",
		Password:       hashedPassword,
		Nickname:       "管理员",
		Role:           "admin",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "system",
	}
	if err := db.Create(&user).Error; err != nil {
		logger.SysLog(fmt.Sprintf("初始化管理员账号失败（可能已存在，不影响启动）：%v", err))
	}
}
