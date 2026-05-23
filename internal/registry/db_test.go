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
	"gorm.io/gorm"
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
