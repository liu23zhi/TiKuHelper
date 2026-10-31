package registry

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"net/url"
	"strings"
	"unicode/utf8"
)

type legacyUserWithoutPhone struct {
	ID             int32      `gorm:"primaryKey;autoIncrement"`
	Username       string     `gorm:"column:username;type:varchar(255)"`
	Password       string     `gorm:"column:password;type:varchar(255)"`
	Perms          string     `gorm:"column:perms;type:varchar(255)"`
	ParentID       int32      `gorm:"column:parent_id;type:int(11)"`
	Nickname       string     `gorm:"column:nickname;type:varchar(255)"`
	Role           string     `gorm:"column:role;type:varchar(32);not null;default:user"`
	Status         string     `gorm:"column:status;type:varchar(32);not null;default:enabled"`
	Balance        float64    `gorm:"column:balance;type:decimal(18,2);not null;default:0"`
	RegisterSource string     `gorm:"column:register_source;type:varchar(32);not null;default:legacy"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime"`
	LastLoginAt    *time.Time `gorm:"column:last_login_at"`
	LastLoginIP    string     `gorm:"column:last_login_ip;type:varchar(64)"`
}

func (*legacyUserWithoutPhone) TableName() string {
	return entity.TableNameUser
}

type legacySMSVerificationCodeWithoutScene struct {
	ID         int32      `gorm:"primaryKey;autoIncrement"`
	Phone      string     `gorm:"column:phone;type:varchar(32);not null"`
	CodeHash   string     `gorm:"column:code_hash;type:char(64);not null"`
	Status     string     `gorm:"column:status;type:varchar(32);not null"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null"`
	ConsumedAt *time.Time `gorm:"column:consumed_at"`
	RequestIP  string     `gorm:"column:request_ip;type:varchar(64)"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (*legacySMSVerificationCodeWithoutScene) TableName() string {
	return entity.TableNameSMSVerificationCode
}

func newLegacySMSVerificationSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:registry-legacy-sms-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy sms db: %v", err)
	}
	if err := db.AutoMigrate(&legacySMSVerificationCodeWithoutScene{}); err != nil {
		t.Fatalf("migrate legacy sms schema: %v", err)
	}
	return db
}

func newLegacyUsersSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:registry-legacy-users-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy users db: %v", err)
	}
	if err := db.AutoMigrate(&legacyUserWithoutPhone{}); err != nil {
		t.Fatalf("migrate legacy users schema: %v", err)
	}
	return db
}

func resetRegistryDBState(t *testing.T) {
	t.Helper()
	if db != nil {
		_ = CloseDB()
	}
	db = nil
	once = sync.Once{}
}

func openRegistryTestDB(t *testing.T) string {
	t.Helper()
	resetRegistryDBState(t)
	dbPath := filepath.Join(t.TempDir(), "registry-test.db")
	t.Cleanup(func() {
		resetRegistryDBState(t)
	})
	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: dbPath}})
	return dbPath
}

func TestRegisterDBAddsAccountDomainTables(t *testing.T) {
	openRegistryTestDB(t)

	tables := []string{
		"access_tokens",
		"system_settings",
		"price_rules",
		"payment_methods",
		"recharge_orders",
		"billing_records",
	}

	for _, table := range tables {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s to be migrated", table)
		}
	}
}

func TestRegisterDBUsesSharedDatabaseScheduler(t *testing.T) {
	openRegistryTestDB(t)

	if _, ok := db.ConnPool.(*dbqueue.Pool); !ok {
		t.Fatalf("db.ConnPool type = %T, want *dbqueue.Pool", db.ConnPool)
	}
	if _, ok := db.Statement.ConnPool.(*dbqueue.Pool); !ok {
		t.Fatalf("db.Statement.ConnPool type = %T, want *dbqueue.Pool", db.Statement.ConnPool)
	}
	if _, ok := db.Config.ConnPool.(*dbqueue.Pool); !ok {
		t.Fatalf("db.Config.ConnPool type = %T, want *dbqueue.Pool", db.Config.ConnPool)
	}
}

func TestRegisterDBAddsUserAccountColumnsAndSeedsAdminDefaults(t *testing.T) {
	openRegistryTestDB(t)

	columns := []string{"role", "status", "balance", "register_source"}
	for _, column := range columns {
		if !db.Migrator().HasColumn("users", column) {
			t.Fatalf("expected users.%s column to exist", column)
		}
	}

	var (
		username       string
		role           string
		status         string
		balance        float64
		registerSource sql.NullString
	)
	row := db.Raw("SELECT username, role, status, balance, register_source FROM users WHERE id = ?", 1).Row()
	if err := row.Scan(&username, &role, &status, &balance, &registerSource); err != nil {
		t.Fatalf("scan seeded admin: %v", err)
	}

	if username != "admin" {
		t.Fatalf("expected seeded admin username, got %s", username)
	}
	if role != "admin" {
		t.Fatalf("expected seeded admin role=admin, got %s", role)
	}
	if status != "enabled" {
		t.Fatalf("expected seeded admin status=enabled, got %s", status)
	}
	if balance != 0 {
		t.Fatalf("expected seeded admin balance=0, got %v", balance)
	}
	if !registerSource.Valid || registerSource.String != "system" {
		t.Fatalf("expected seeded admin register_source=system, got %+v", registerSource)
	}
}

func TestRegisterDBUsesDefaultAdminPasswordWhenEnvironmentIsEmpty(t *testing.T) {
	t.Setenv("TIKU_ADMIN_PASSWORD", "")
	openRegistryTestDB(t)

	var admin entity.User
	if err := db.Where("id = ?", 1).First(&admin).Error; err != nil {
		t.Fatalf("load default admin: %v", err)
	}
	if matched, _ := util.VerifyPassword(admin.Password, "123456"); !matched {
		t.Fatalf("expected default admin password 123456, got %q", admin.Password)
	}
}

func TestRegisterDBUsesConfiguredAdminPasswordOnBootstrap(t *testing.T) {
	t.Setenv("TIKU_ADMIN_PASSWORD", "Admin123!")
	openRegistryTestDB(t)

	var admin entity.User
	if err := db.Where("id = ?", 1).First(&admin).Error; err != nil {
		t.Fatalf("load configured admin: %v", err)
	}
	if matched, _ := util.VerifyPassword(admin.Password, "Admin123!"); !matched {
		t.Fatalf("expected configured admin password, got %q", admin.Password)
	}
}

func TestEnsureUserPhoneBindingSchemaAllowsCurrentUserAutoMigrateOnLegacySQLiteTable(t *testing.T) {
	legacyDB := newLegacyUsersSchemaDB(t)
	if err := legacyDB.Create(&legacyUserWithoutPhone{
		ID:             1,
		Username:       "legacy-admin",
		Password:       "123456",
		Nickname:       "管理员",
		Role:           "admin",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "legacy",
	}).Error; err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}

	if legacyDB.Migrator().HasColumn("users", "phone") {
		t.Fatal("expected legacy users table to start without phone column")
	}

	if err := ensureUserPhoneBindingSchema(legacyDB); err != nil {
		t.Fatalf("prepare legacy users phone binding schema: %v", err)
	}
	if err := legacyDB.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate current user schema after phone preparation: %v", err)
	}

	if !legacyDB.Migrator().HasColumn("users", "phone") {
		t.Fatal("expected users.phone column after migration")
	}
	if !legacyDB.Migrator().HasColumn("users", "phone_bound_at") {
		t.Fatal("expected users.phone_bound_at column after migration")
	}
	if !legacyDB.Migrator().HasIndex("users", "idx_users_phone") {
		t.Fatal("expected users phone unique index after migration")
	}

	var got struct {
		Username string
		Phone    sql.NullString
	}
	if err := legacyDB.Raw("SELECT username, phone FROM users WHERE id = ?", 1).Scan(&got).Error; err != nil {
		t.Fatalf("load migrated user: %v", err)
	}
	if got.Username != "legacy-admin" {
		t.Fatalf("expected migrated username legacy-admin, got %s", got.Username)
	}
	if got.Phone.Valid {
		t.Fatalf("expected migrated phone to remain NULL, got %+v", got.Phone)
	}
}

func TestEnsureUserPhoneBindingSchemaUsesGenericMigratorPathForMySQLCompatibleDrivers(t *testing.T) {
	legacyDB := newLegacyUsersSchemaDB(t)
	if err := legacyDB.Create(&legacyUserWithoutPhone{
		ID:             2,
		Username:       "legacy-mysql-user",
		Password:       "123456",
		Nickname:       "兼容用户",
		Role:           "user",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "legacy",
	}).Error; err != nil {
		t.Fatalf("seed mysql-compatible legacy user: %v", err)
	}

	if legacyDB.Migrator().HasColumn("users", "phone") {
		t.Fatal("expected legacy users table to start without phone column")
	}

	if err := ensureUserPhoneBindingSchemaForDriver(legacyDB, "mysql"); err != nil {
		t.Fatalf("prepare mysql-compatible users phone binding schema: %v", err)
	}
	if err := ensureUserWechatBindingSchemaForDriver(legacyDB, "mysql"); err != nil {
		t.Fatalf("prepare mysql-compatible users wechat binding schema: %v", err)
	}
	if err := legacyDB.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate current user schema after mysql-compatible preparation: %v", err)
	}

	if !legacyDB.Migrator().HasColumn("users", "phone") {
		t.Fatal("expected users.phone column after generic migrator preparation")
	}
	if !legacyDB.Migrator().HasColumn("users", "phone_bound_at") {
		t.Fatal("expected users.phone_bound_at column after generic migrator preparation")
	}
	if !legacyDB.Migrator().HasIndex("users", "idx_users_phone") {
		t.Fatal("expected users phone unique index after generic migrator preparation")
	}
	if !legacyDB.Migrator().HasColumn("users", "wechat_id") {
		t.Fatal("expected users.wechat_id column after generic migrator preparation")
	}
	if !legacyDB.Migrator().HasColumn("users", "wechat_bound_at") {
		t.Fatal("expected users.wechat_bound_at column after generic migrator preparation")
	}
	if !legacyDB.Migrator().HasIndex("users", "idx_users_wechat_id") {
		t.Fatal("expected users wechat unique index after generic migrator preparation")
	}
}

func TestEnsureSMSVerificationSchemaAllowsCurrentAutoMigrateOnLegacySQLiteTable(t *testing.T) {
	legacyDB := newLegacySMSVerificationSchemaDB(t)
	if err := legacyDB.Create(&legacySMSVerificationCodeWithoutScene{
		Phone:     "13800000000",
		CodeHash:  "legacy-code-hash",
		Status:    "pending",
		ExpiresAt: time.Now().Add(5 * time.Minute),
		RequestIP: "127.0.0.1",
	}).Error; err != nil {
		t.Fatalf("seed legacy sms verification code: %v", err)
	}

	if legacyDB.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
		t.Fatal("expected legacy sms_verification_codes table to start without scene column")
	}

	if err := legacyDB.AutoMigrate(&entity.SMSVerificationCode{}); err == nil {
		t.Fatal("expected direct automigrate on legacy sqlite sms_verification_codes table to fail before compatibility preparation")
	}

	if err := ensureSMSVerificationSchema(legacyDB); err != nil {
		t.Fatalf("prepare legacy sms verification schema: %v", err)
	}
	if err := legacyDB.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatalf("migrate current sms verification schema after preparation: %v", err)
	}

	if !legacyDB.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
		t.Fatal("expected sms_verification_codes.scene column after migration")
	}
}

func TestRegisterDBMigratesLegacySMSVerificationTableWithSceneColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-sms-verification.db")

	legacyDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy sqlite db: %v", err)
	}
	if err := legacyDB.AutoMigrate(&legacySMSVerificationCodeWithoutScene{}); err != nil {
		t.Fatalf("migrate legacy sms verification schema on file db: %v", err)
	}
	if err := legacyDB.Create(&legacySMSVerificationCodeWithoutScene{
		Phone:     "13800000000",
		CodeHash:  "legacy-code-hash",
		Status:    "pending",
		ExpiresAt: time.Now().Add(5 * time.Minute),
		RequestIP: "127.0.0.1",
	}).Error; err != nil {
		t.Fatalf("seed legacy file db sms code: %v", err)
	}
	legacySQLDB, err := legacyDB.DB()
	if err != nil {
		t.Fatalf("get legacy sms sql db handle: %v", err)
	}
	if err := legacySQLDB.Close(); err != nil {
		t.Fatalf("close legacy sms sql db handle: %v", err)
	}

	resetRegistryDBState(t)
	t.Cleanup(func() {
		resetRegistryDBState(t)
	})

	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: dbPath}})

	if !db.Migrator().HasColumn(entity.TableNameSMSVerificationCode, "scene") {
		t.Fatal("expected sms_verification_codes.scene column after RegisterDB migration")
	}
}

func TestRegisterDBMigratesLegacyUsersTableWithNullablePhoneBindingColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-users.db")

	legacyDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy sqlite db: %v", err)
	}
	if err := legacyDB.AutoMigrate(&legacyUserWithoutPhone{}); err != nil {
		t.Fatalf("migrate legacy users schema on file db: %v", err)
	}
	if err := legacyDB.Create(&legacyUserWithoutPhone{
		ID:             1,
		Username:       "legacy-admin",
		Password:       "123456",
		Nickname:       "管理员",
		Role:           "admin",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "legacy",
	}).Error; err != nil {
		t.Fatalf("seed legacy file db user: %v", err)
	}
	legacySQLDB, err := legacyDB.DB()
	if err != nil {
		t.Fatalf("get legacy sql db handle: %v", err)
	}
	if err := legacySQLDB.Close(); err != nil {
		t.Fatalf("close legacy sql db handle: %v", err)
	}

	resetRegistryDBState(t)
	t.Cleanup(func() {
		resetRegistryDBState(t)
	})

	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: dbPath}})

	if !db.Migrator().HasColumn("users", "phone") {
		t.Fatal("expected users.phone column after migration")
	}
	if !db.Migrator().HasColumn("users", "phone_bound_at") {
		t.Fatal("expected users.phone_bound_at column after migration")
	}
	if !db.Migrator().HasIndex("users", "idx_users_phone") {
		t.Fatal("expected users phone unique index after migration")
	}

	var got struct {
		Username string
		Phone    sql.NullString
	}
	if err := db.Raw("SELECT username, phone FROM users WHERE id = ?", 1).Scan(&got).Error; err != nil {
		t.Fatalf("load migrated user: %v", err)
	}
	if got.Username != "legacy-admin" {
		t.Fatalf("expected migrated username legacy-admin, got %s", got.Username)
	}
	if got.Phone.Valid {
		t.Fatalf("expected migrated phone to remain NULL, got %+v", got.Phone)
	}
}

// legacyBillingRecordWithoutQuestionText 模拟没有 question_text 列的历史账单表。
type legacyBillingRecordWithoutQuestionText struct {
	ID                int32     `gorm:"primaryKey;autoIncrement"`
	UserID            int32     `gorm:"column:user_id;type:int(11);not null"`
	Type              string    `gorm:"column:type;type:varchar(32);not null"`
	Provider          string    `gorm:"column:provider;type:varchar(64)"`
	Amount            float64   `gorm:"column:amount;type:decimal(18,8);not null;default:0"`
	UnitPriceSnapshot string    `gorm:"column:unit_price_snapshot;type:text"`
	RequestID         string    `gorm:"column:request_id;type:varchar(64)"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (*legacyBillingRecordWithoutQuestionText) TableName() string {
	return entity.TableNameBillingRecord
}

func TestEnsureBillingQuestionTextSchemaBackfillsFromSnapshotOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:billing-question-backfill?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	if err := db.AutoMigrate(&legacyBillingRecordWithoutQuestionText{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate legacy billing table: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameBillingRecord, "question_text") {
		t.Fatal("expected legacy billing table to start without question_text column")
	}

	// 历史行：题目以 URL 编码形式埋在快照里。
	legacy := legacyBillingRecordWithoutQuestionText{
		UserID:            1,
		Type:              "normal_search",
		Provider:          "icodef",
		Amount:            1.25,
		UnitPriceSnapshot: "provider=icodef question=" + url.QueryEscape("历史账单的题目"),
		RequestID:         "req-legacy",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy billing record: %v", err)
	}
	// 不带 question= 的行不应该被写入内容。
	withoutQuestion := legacyBillingRecordWithoutQuestionText{
		UserID:            1,
		Type:              "recharge",
		Provider:          "epay",
		Amount:            50,
		UnitPriceSnapshot: "provider=epay flatPrice=50.00",
		RequestID:         "req-recharge",
	}
	if err := db.Create(&withoutQuestion).Error; err != nil {
		t.Fatalf("seed legacy recharge record: %v", err)
	}

	if err := ensureBillingQuestionTextSchema(db); err != nil {
		t.Fatalf("prepare billing question text schema: %v", err)
	}

	if !db.Migrator().HasColumn(entity.TableNameBillingRecord, "question_text") {
		t.Fatal("expected question_text column after preparation")
	}

	var questionText sql.NullString
	if err := db.Raw("SELECT question_text FROM billing_records WHERE id = ?", legacy.ID).Scan(&questionText).Error; err != nil {
		t.Fatalf("load backfilled record: %v", err)
	}
	if !questionText.Valid || questionText.String != "历史账单的题目" {
		t.Fatalf("expected backfilled question text 历史账单的题目, got %+v", questionText)
	}

	var rechargeText sql.NullString
	if err := db.Raw("SELECT question_text FROM billing_records WHERE id = ?", withoutQuestion.ID).Scan(&rechargeText).Error; err != nil {
		t.Fatalf("load recharge record: %v", err)
	}
	if rechargeText.Valid && rechargeText.String != "" {
		t.Fatalf("expected recharge record to stay empty, got %+v", rechargeText)
	}

	// 回溯只跑一次：把已填内容改掉后再次调用不应被覆盖。
	if err := db.Exec("UPDATE billing_records SET question_text = ? WHERE id = ?", "手工修改", legacy.ID).Error; err != nil {
		t.Fatalf("override question text: %v", err)
	}
	if err := ensureBillingQuestionTextSchema(db); err != nil {
		t.Fatalf("second preparation: %v", err)
	}
	if err := db.Raw("SELECT question_text FROM billing_records WHERE id = ?", legacy.ID).Scan(&questionText).Error; err != nil {
		t.Fatalf("reload record: %v", err)
	}
	if questionText.String != "手工修改" {
		t.Fatalf("expected the one-shot backfill to not run again, got %q", questionText.String)
	}
}

// 回归：中文题目「字符数 < 512 但字节数 > 512」时，
// 早期实现用 len() 判长度、却按 []rune 切片，导致 slice bounds out of range 并让服务反复崩溃重启。
func TestBackfillBillingQuestionTextHandlesLongChineseQuestions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:billing-question-cjk?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&legacyBillingRecordWithoutQuestionText{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate billing table: %v", err)
	}

	// 200 个汉字 = 200 字符 / 600 字节，正好命中旧代码的越界条件。
	longQuestion := strings.Repeat("题", 200)
	if len([]rune(longQuestion)) >= 512 || len(longQuestion) <= 512 {
		t.Fatalf("test fixture must be <512 runes but >512 bytes, got runes=%d bytes=%d", len([]rune(longQuestion)), len(longQuestion))
	}

	row := legacyBillingRecordWithoutQuestionText{
		UserID:            1,
		Type:              "normal_search",
		Provider:          "icodef",
		Amount:            1,
		UnitPriceSnapshot: "provider=icodef question=" + url.QueryEscape(longQuestion),
		RequestID:         "req-cjk",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed billing record: %v", err)
	}

	if err := ensureBillingQuestionTextSchema(db); err != nil {
		t.Fatalf("prepare schema must not fail: %v", err)
	}

	var questionText sql.NullString
	if err := db.Raw("SELECT question_text FROM billing_records WHERE id = ?", row.ID).Scan(&questionText).Error; err != nil {
		t.Fatalf("load backfilled record: %v", err)
	}
	if questionText.String != longQuestion {
		t.Fatalf("expected the full %d-rune question to survive, got %d runes", len([]rune(longQuestion)), len([]rune(questionText.String)))
	}
}

// 超过列上限的长题目必须按「字符数」截断，且不产生乱码。
func TestBackfillBillingQuestionTextTruncatesByRunes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:billing-question-truncate?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&legacyBillingRecordWithoutQuestionText{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate billing table: %v", err)
	}

	// 700 个汉字 = 2100 字节，远超 512。
	overlongQuestion := strings.Repeat("答", 700)
	row := legacyBillingRecordWithoutQuestionText{
		UserID:            1,
		Type:              "normal_search",
		Provider:          "icodef",
		Amount:            1,
		UnitPriceSnapshot: "provider=icodef question=" + url.QueryEscape(overlongQuestion),
		RequestID:         "req-truncate",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed billing record: %v", err)
	}

	if err := ensureBillingQuestionTextSchema(db); err != nil {
		t.Fatalf("prepare schema must not fail: %v", err)
	}

	var questionText sql.NullString
	if err := db.Raw("SELECT question_text FROM billing_records WHERE id = ?", row.ID).Scan(&questionText).Error; err != nil {
		t.Fatalf("load backfilled record: %v", err)
	}
	if got := len([]rune(questionText.String)); got != billingQuestionTextMaxLength {
		t.Fatalf("expected %d runes after truncation, got %d", billingQuestionTextMaxLength, got)
	}
	if !utf8.ValidString(questionText.String) {
		t.Fatal("expected truncated question to stay valid UTF-8")
	}
}

// 单条脏数据不能让回溯整体失败，更不能让服务启动崩溃。
func TestSafeBackfillBillingQuestionTextSurvivesPanic(t *testing.T) {
	// 直接验证 recover 包装存在：nil DB 会返回错误而不是 panic。
	if err := safeBackfillBillingQuestionText(nil); err == nil {
		t.Fatal("expected an error for a nil db instead of a panic")
	}
}

func TestTruncateRunesKeepsMultibyteCharactersIntact(t *testing.T) {
	assertTruncate := func(input string, maxRunes int, want string) {
		t.Helper()
		if got := truncateRunes(input, maxRunes); got != want {
			t.Fatalf("truncateRunes(%q, %d) = %q, want %q", input, maxRunes, got, want)
		}
	}

	assertTruncate("短", 10, "短")
	assertTruncate("题目内容", 3, "题目内")
	assertTruncate(strings.Repeat("题", 600), 512, strings.Repeat("题", 512))
	assertTruncate("abc", 0, "")
	assertTruncate("abc", -1, "")
	assertTruncate("", 5, "")
	assertTruncate("a题b", 2, "a题")
}
