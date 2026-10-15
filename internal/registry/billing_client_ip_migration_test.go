package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newBillingClientIPTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := "file:billing-client-ip-" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 旧库（无 client_ip 列）必须被补上该列。
func TestEnsureBillingClientIPSchemaAddsColumn(t *testing.T) {
	db := newBillingClientIPTestDB(t)

	// 模拟加列之前的老表结构。
	if err := db.Exec(`CREATE TABLE billing_records (
		id integer PRIMARY KEY AUTOINCREMENT,
		user_id integer NOT NULL,
		type varchar(32) NOT NULL,
		amount decimal(18,8) NOT NULL DEFAULT 0,
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameBillingRecord, "client_ip") {
		t.Fatal("test setup invalid: legacy table should not have client_ip")
	}

	if err := ensureBillingClientIPSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameBillingRecord, "client_ip") {
		t.Fatal("expected the client_ip column to be added")
	}

	// 已有数据不能丢。
	if err := db.Exec(`INSERT INTO billing_records (user_id, type, amount) VALUES (1, 'normal_search', 0.5)`).Error; err != nil {
		t.Fatalf("insert after migration: %v", err)
	}
	var count int64
	if err := db.Table(entity.TableNameBillingRecord).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the row to survive, got %d", count)
	}
}

// 幂等：重复调用不再尝试加列。
func TestEnsureBillingClientIPSchemaIsIdempotent(t *testing.T) {
	db := newBillingClientIPTestDB(t)

	if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := ensureBillingClientIPSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := ensureBillingClientIPSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameBillingRecord, "client_ip") {
		t.Fatal("expected the column to exist")
	}
}

// billing_records 表不存在时必须直接返回，不报错。
func TestEnsureBillingClientIPSchemaSkipsWhenTableMissing(t *testing.T) {
	db := newBillingClientIPTestDB(t)

	if err := ensureBillingClientIPSchema(db); err != nil {
		t.Fatalf("expected no error when the table is missing, got %v", err)
	}
}

// 新落账的账单必须能存下来源 IP。
func TestBillingClientIPRoundTrips(t *testing.T) {
	db := newBillingClientIPTestDB(t)

	if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	record := entity.BillingRecord{
		UserID:   1,
		Type:     "normal_search",
		Amount:   0.01,
		ClientIP: "112.111.55.91",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var loaded entity.BillingRecord
	if err := db.First(&loaded, record.ID).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ClientIP != "112.111.55.91" {
		t.Fatalf("expected the client ip to round-trip, got %q", loaded.ClientIP)
	}
}

func TestEnsureBillingClientIPSchemaBackfillsByExactRequestID(t *testing.T) {
	db := newBillingClientIPTestDB(t)
	if err := db.AutoMigrate(&entity.BillingRecord{}, &entity.Log{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	requestID := "req-billing-ip-001"
	if err := db.Create(&entity.BillingRecord{
		UserID:    1,
		Type:      "normal_search",
		Amount:    0.5,
		RequestID: requestID,
		ClientIP:  "",
	}).Error; err != nil {
		t.Fatalf("create billing record: %v", err)
	}
	if err := db.Create(&entity.Log{
		Module:      "search",
		Description: "搜索完成 requestId=" + requestID,
		ClientIP:    "127.0.0.1",
	}).Error; err != nil {
		t.Fatalf("create matching log: %v", err)
	}

	if err := ensureBillingClientIPSchema(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var loaded entity.BillingRecord
	if err := db.First(&loaded).Error; err != nil {
		t.Fatalf("load billing record: %v", err)
	}
	if loaded.ClientIP != "127.0.0.1" {
		t.Fatalf("expected client ip to be backfilled from the exact request id, got %q", loaded.ClientIP)
	}
	if !systemSettingFlagIsSet(db, billingClientIPBackfilledKey) {
		t.Fatal("expected the backfill completion flag to be set")
	}

	// 相似但不同的 requestId 不得误关联。
	if err := db.Create(&entity.BillingRecord{
		UserID:    1,
		Type:      "normal_search",
		Amount:    0.5,
		RequestID: requestID + "-other",
	}).Error; err != nil {
		t.Fatalf("create unmatched billing record: %v", err)
	}
	if err := ensureBillingClientIPSchema(db); err != nil {
		t.Fatalf("idempotent backfill: %v", err)
	}
	var unmatched entity.BillingRecord
	if err := db.Where("request_id = ?", requestID+"-other").First(&unmatched).Error; err != nil {
		t.Fatalf("load unmatched billing record: %v", err)
	}
	if unmatched.ClientIP != "" {
		t.Fatalf("expected unmatched request id to remain empty, got %q", unmatched.ClientIP)
	}
}
