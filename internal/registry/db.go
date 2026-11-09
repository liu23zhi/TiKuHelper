package registry

import (
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
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
var databaseScheduler *dbqueue.Scheduler
var once sync.Once
var idempotencyCleanupStop func()

// CloseDB close db
func CloseDB() error {
	if idempotencyCleanupStop != nil {
		idempotencyCleanupStop()
		idempotencyCleanupStop = nil
	}
	if databaseScheduler != nil {
		databaseScheduler.Close()
		databaseScheduler = nil
	}
	if db == nil {
		return nil
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
	normalizedDriver := strings.TrimSpace(strings.ToLower(driverName))
	if normalizedDriver == "" {
		normalizedDriver = strings.TrimSpace(strings.ToLower(db.Dialector.Name()))
	}

	if !db.Migrator().HasTable(entity.TableNameSMSVerificationCode) {
		if normalizedDriver == "sqlite" {
			var legacyExists bool
			if err := db.Raw("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)", "sms_verification_codes__legacy_scene").Scan(&legacyExists).Error; err != nil {
				return fmt.Errorf("inspect SMS migration shadow metadata: %w", err)
			}
			if legacyExists {
				var legacyHasRows bool
				if err := db.Raw("SELECT EXISTS(SELECT 1 FROM sms_verification_codes__legacy_scene LIMIT 1)").Scan(&legacyHasRows).Error; err != nil {
					return fmt.Errorf("inspect SMS migration shadow records: %w", err)
				}
				if legacyHasRows {
					return errors.New("unfinished SMS verification migration: primary is absent and legacy shadow retains records; manual recovery required; tables left unchanged")
				}
			}
		}
		return nil
	}

	if normalizedDriver == "sqlite" {
		needsScene := !db.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene")
		needsIndex := !db.Migrator().HasIndex(entity.TableNameSMSVerificationCode, "idx_sms_verification_codes_scene")
		if !needsScene && !needsIndex {
			// An interrupted older rebuild can leave all records in the shadow.
			// Inspect only; recovery must be explicit and must preserve both tables.
			var legacyExists bool
			if err := db.Raw("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)", "sms_verification_codes__legacy_scene").Scan(&legacyExists).Error; err != nil {
				return fmt.Errorf("inspect SMS migration shadow metadata: %w", err)
			}
			if legacyExists {
				var rows struct {
					PrimaryHasRows bool `gorm:"column:primary_has_rows"`
					LegacyHasRows  bool `gorm:"column:legacy_has_rows"`
				}
				if err := db.Raw("SELECT EXISTS(SELECT 1 FROM sms_verification_codes LIMIT 1) AS primary_has_rows, EXISTS(SELECT 1 FROM sms_verification_codes__legacy_scene LIMIT 1) AS legacy_has_rows").Scan(&rows).Error; err != nil {
					return fmt.Errorf("inspect SMS migration shadow records: %w", err)
				}
				if !rows.PrimaryHasRows && rows.LegacyHasRows {
					return errors.New("unfinished SMS verification migration: primary is empty and legacy shadow retains records; manual recovery required; both tables preserved")
				}
			}
			return nil
		}
		return withSQLiteSMSMigrationBoundary(db, func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
				statements := []string{
					"ALTER TABLE sms_verification_codes RENAME TO sms_verification_codes__legacy_scene",
					"CREATE TABLE sms_verification_codes (`id` integer PRIMARY KEY AUTOINCREMENT,`phone` varchar(32) NOT NULL,`scene` varchar(32) NOT NULL DEFAULT '',`code_hash` char(64) NOT NULL,`status` varchar(32) NOT NULL,`expires_at` datetime NOT NULL,`consumed_at` datetime,`request_ip` varchar(64),`created_at` datetime,`updated_at` datetime)",
					"INSERT INTO sms_verification_codes (`id`,`phone`,`scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at`) SELECT `id`,`phone`,'' AS `scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at` FROM sms_verification_codes__legacy_scene",
					"DROP TABLE sms_verification_codes__legacy_scene",
				}
				for _, statement := range statements {
					if err := tx.Exec(statement).Error; err != nil {
						return err
					}
				}
			}
			if !tx.Migrator().HasIndex(entity.TableNameSMSVerificationCode, "idx_sms_verification_codes_scene") {
				return tx.Exec("CREATE INDEX idx_sms_verification_codes_scene ON sms_verification_codes(scene)").Error
			}
			return nil
		})
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
	// key 是 MySQL/MariaDB 的保留字，必须让 GORM 生成带反引号的标识符（见 map 条件）。
	if err := db.Where(map[string]any{"key": billingQuestionTextBackfilledKey}).First(&setting).Error; err != nil {
		return false
	}
	return strings.TrimSpace(setting.Value) == "1"
}

func markBillingQuestionTextBackfilled(db *gorm.DB) error {
	_ = db.Where(map[string]any{"key": billingQuestionTextBackfilledKey}).Delete(&entity.SystemSetting{}).Error
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
		{field: "ContextWindowTokens", name: "context_window_tokens"},
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
	return ensureMoneyPrecisionSchemaWithOptions(db, false)
}

func ensureMoneyPrecisionSchemaWithOptions(db *gorm.DB, skipAccessTokenQuotaLimit bool) error {
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
		if skipAccessTokenQuotaLimit && item.Table == entity.TableNameAccessToken && item.Column == "TotalQuotaLimit" {
			continue
		}
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
// RegisterDB preserves the legacy entry while policy failures stop before migration.
func RegisterDB(config configs.Config) *gorm.DB {
	database, err := RegisterDBWithError(config)
	if err != nil {
		logger.FatalLog(err)
	}
	return database
}

func registerPreparedDB(config configs.Config, plan adminBootstrapPlan) *gorm.DB {
	once.Do(func() {
		var err error
		var conn gorm.Dialector
		sqlDSN := os.Getenv("SQL_DSN")
		// 环境变量最高优先级：SQL_DSN > config.yaml 的 mysql > SQLite。
		mysqlDSN, dsnSource := resolveDatabaseDSN(config.Mysql, sqlDSN)
		if mysqlDSN != "" {
			// 补齐 parseTime/loc/charset，避免时间列扫描失败引发启动即崩（见 dsn.go）。
			normalizedDSN, dsnFixes := normalizeMySQLDSN(mysqlDSN)
			conn = mysql.Open(normalizedDSN)
			logger.SysLog("数据库连接来源：" + dsnSource)
			if len(dsnFixes) > 0 {
				logger.SysLog("MySQL DSN 已补齐参数：" + strings.Join(dsnFixes, ", ") +
					"（缺少 parseTime 会让时间列无法扫描并导致服务反复重启）")
			}
			if strings.TrimSpace(sqlDSN) != "" && strings.TrimSpace(config.Mysql) != "" && strings.TrimSpace(config.Mysql) != strings.TrimSpace(sqlDSN) {
				logger.SysWarn("SQL_DSN 环境变量与 config.yaml 的 mysql 同时存在且不一致，已按环境变量生效")
			}
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
			conn = sqliteQuietDialector{Dialector: sqlite.Open(sqliteProductionDSN(dbPath))}
		}
		prepareStmt := prepareStatementsFor(config.Mysql, sqlDSN)
		db, err = gorm.Open(conn, &gorm.Config{
			PrepareStmt: prepareStmt,
			Logger:      newDatabaseLogger(),
		})
		if err != nil {
			logger.FatalLog(fmt.Errorf("open db fail: %w", err))
		}
		maxActive := 1
		if db.Dialector.Name() == "sqlite" {
			if err := configureSQLiteConnection(db, config.Database.Path); err != nil {
				logger.FatalLog(fmt.Errorf("configure sqlite connection fail: %w", err))
			}
		} else {
			poolSettings, poolErr := configureMySQLConnection(db, config.Database.MySQLPool)
			if poolErr != nil {
				logger.FatalLog(fmt.Errorf("configure mysql connection pool fail: %w", poolErr))
			}
			maxActive = poolSettings.MaxOpenConns
			logger.SysLog(fmt.Sprintf("MySQL 连接池：最大连接=%d，最大空闲=%d，连接最长存活=%s，最大空闲时长=%s",
				poolSettings.MaxOpenConns, poolSettings.MaxIdleConns, poolSettings.ConnMaxLifetime, poolSettings.ConnMaxIdleTime))
		}
		databaseScheduler = dbqueue.NewScheduler(maxActive)
		managedPool := dbqueue.Wrap(db.ConnPool, databaseScheduler)
		db.ConnPool = managedPool
		db.Config.ConnPool = managedPool
		db.Statement.ConnPool = managedPool
		dao.SetDefault(db)
	})
	if err := validateExistingSQLiteBillingLookupIndex(db); err != nil {
		logger.FatalLog(err)
	}

	err := db.AutoMigrate(&entity.Tiku{})
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}
	if err := db.AutoMigrate(&entity.TikuAnswerEvidence{}); err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate tiku answer evidence fail: %w", err))
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

	// Create the marker table before repairing legacy quotas, but repair access_tokens
	// before its AutoMigrate can convert the column to DECIMAL(18,6) on MySQL.
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate system settings fail: %w", err))
	}
	// Historical invalid quota must never be silently converted to unlimited.
	preserveDisabledInvalidQuota := false
	if err := runStartupMigration("ensureAccessTokenQuotaLimitData", func() error {
		var inspectErr error
		preserveDisabledInvalidQuota, inspectErr = inspectAccessTokenQuotaLimitData(db)
		return inspectErr
	}); err != nil {
		logger.FatalLog(fmt.Errorf("validate access token quota fail; preserve data for manual recovery: %w", err))
	}

	if err := ensureSMSVerificationSchema(db); err != nil {
		logger.FatalLog(fmt.Errorf("prepare sms verification schema fail: %w", err))
	}

	accountDomainModels := []any{
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
		&entity.Ticket{},
		&entity.TicketMessage{},
		&entity.TicketNotification{},
		&entity.TicketRefund{},
		&entity.TicketRefundLedger{},
		&entity.TicketRefundProof{},
		&entity.AIOCRCache{},
		&entity.AIOCRCacheSequence{},
		&entity.ScriptVersion{},
		&entity.UserScript{},
		&entity.ScriptExchangeToken{},
		&entity.ScriptRunSession{},
		&entity.ScriptEscrow{},
		&entity.ScriptEscrowEntry{},
		&entity.ScriptEscrowWork{},
		&entity.ScriptActivationEntitlement{},
		&entity.ScriptSubsidizedUsage{},
	}
	// Keep the historical quota column byte-for-byte when its disabled values
	// cannot be converted. An additive column still lets ordinary Key queries
	// and the new script Key path coexist on that older token schema.
	if !preserveDisabledInvalidQuota {
		accountDomainModels = append([]any{&entity.AccessToken{}}, accountDomainModels...)
	} else if err := ensureScriptKeyLinkOnPreservedQuotaSchema(db); err != nil {
		logger.FatalLog(fmt.Errorf("prepare script Key link without changing quota: %w", err))
	}
	err = autoMigrateAccountDomainWithBillingLookupIndex(db, accountDomainModels)
	if err != nil {
		logger.FatalLog(fmt.Errorf("auto migrate fail: %w", err))
	}
	if err := runStartupMigration("ensureAIOCRCacheIDs", func() error { return ensureAIOCRCacheIDs(db) }); err != nil {
		logger.SysLog(fmt.Sprintf("OCR 缓存 ID 回填失败（不影响启动，将在下次启动重试）：%v", err))
	}

	if err := runStartupMigration("ensureBillingRecordIndexes", func() error { return ensureBillingRecordIndexes(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare billing record indexes fail: %w", err))
	}

	if err := ensureSQLiteBillingLookupIndex(db); err != nil {
		logger.FatalLog(err)
	}

	if err := runStartupMigration("ensureAuditCorrelationSchema", func() error { return ensureAuditCorrelationSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare audit correlation schema fail: %w", err))
	}

	if err := runStartupMigration("ensureMoneyPrecisionSchema", func() error {
		return ensureMoneyPrecisionSchemaWithOptions(db, preserveDisabledInvalidQuota)
	}); err != nil {
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
	if err := runStartupMigration("ensureScriptPriceMultiplierSchema", func() error { return ensureScriptPriceMultiplierSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare script answer price schema fail: %w", err))
	}

	// 邀请码与充值返佣：建 referral_invites / referral_rebates 表，并给 users 补归因两列。
	// 结构性失败 FATAL（要让人看见）；标记写失败只记日志，下次启动幂等重跑。
	if err := runStartupMigration("ensureReferralSchema", func() error { return ensureReferralSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare referral schema fail: %w", err))
	}

	// 评价返现：建四张表，并给 users 补 gift_balance（赠金来源标记）。
	// 结构性失败 FATAL（要让人看见）；标记写失败只记日志，下次启动幂等重跑。
	if err := runStartupMigration("ensureReviewRebateSchema", func() error { return ensureReviewRebateSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare review rebate schema fail: %w", err))
	}

	// 工单图片附件：只建新表 ticket_attachments，不动 tickets / ticket_messages，
	// 也没有数据回填。结构性失败 FATAL；标记写失败只记日志，下次启动幂等重跑。
	if err := runStartupMigration("ensureTicketAttachmentSchema", func() error { return ensureTicketAttachmentSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare ticket attachment schema fail: %w", err))
	}

	// 充值阶梯赠送：建 recharge_promotion_grants（赠送发放账本，recharge_order_id 唯一）。
	// 与开关解耦：开关默认 false，表照建，运营打开开关时结构已就位。
	// 必须排在 ensureReviewRebateSchema 之后 —— 它依赖 users.gift_balance 已存在。
	if err := runStartupMigration("ensureRechargePromotionSchema", func() error { return ensureRechargePromotionSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare recharge promotion schema fail: %w", err))
	}

	// 支付已入账唯一事件：建 payment_events（order_no 唯一），并按 paid_at 回填历史订单。
	// 这是充值加款幂等的锚点 —— 订单被改成 refunded 后，旧成功回调也不会再加款。
	// 结构性失败 FATAL；回填失败只记日志、不写标记，下次启动重试。
	if err := runStartupMigration("ensurePaymentEventSchema", func() error { return ensurePaymentEventSchema(db) }); err != nil {
		logger.FatalLog(fmt.Errorf("prepare payment event schema fail: %w", err))
	}

	// source-aware 归属地缓存使用新表，旧表保留以兼容 SQLite 和回滚；
	// 回填失败只记日志，下一次启动会因未写标记而重试。
	if err := runStartupMigration("ensureLocationCacheEntriesSchema", func() error { return ensureLocationCacheEntriesSchema(db) }); err != nil {
		logger.SysLog(fmt.Sprintf("归属地缓存迁移失败（不影响启动）：%v", err))
	}

	// 题库 hash 迁移与 plat 列删除。两者都依赖 system_settings 表，
	// 因此必须放在上面的 AutoMigrate（含 SystemSetting）之后。
	// hash 失败仍只记日志；plat 仅在确认 MySQL 危险残留列时阻断启动。
	// 其它失败维持原来的日志/下次重试行为。
	if err := runStartupMigration("ensureTikuHashWithoutPlatSchema", func() error { return ensureTikuHashWithoutPlatSchema(db) }); err != nil {
		logger.SysLog(fmt.Sprintf("题库 hash 迁移失败（不影响启动）：%v", err))
	}
	if err := runStartupMigration("ensureTikuPlatColumnDropped", func() error { return ensureTikuPlatColumnDropped(db) }); err != nil {
		var guardErr *tikuPlatStartupGuardError
		if errors.As(err, &guardErr) && guardErr != nil {
			logger.FatalLog(fmt.Errorf("prepare tiku.plat writable schema fail: %w", err))
		}
		// 非确认旧列的迁移错误（包括包装的 panic）维持原来的日志/继续策略。
		logger.SysLog(fmt.Sprintf("tiku.plat 列删除失败（不影响启动）：%v", err))
	}

	ensureAccountDomainDefaults(db)

	// Apply only the preflighted credential plan. Environment never resets an existing account.
	if err := applyAdminBootstrap(db, plan); err != nil {
		logger.FatalLog(fmt.Errorf("apply admin bootstrap policy fail: %w", err))
	}

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
