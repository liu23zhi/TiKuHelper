package registry

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newBillingWalletSplitTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	return db
}

// 账单分桶两列必须被补上，且历史行保持 0（未知，而不是「没扣本金」）。
//
// 这条钉住的是「只建列不回填」这个决定：旧模型没有分桶信息，
// 任何推断都会写错钱包口径，所以历史行必须留在 0 由读侧按未知处理。
func TestEnsureBillingWalletSplitSchemaAddsColumns(t *testing.T) {
	db := newBillingWalletSplitTestDB(t)
	// 模拟加列之前的 billing_records（故意不带两列）。
	if err := db.Exec(`CREATE TABLE billing_records (
		id integer PRIMARY KEY AUTOINCREMENT,
		user_id integer NOT NULL DEFAULT 0,
		access_token_id integer,
		type varchar(32) NOT NULL DEFAULT '',
		used_default_account numeric NOT NULL DEFAULT 0,
		provider varchar(64),
		model_name varchar(128),
		input_tokens integer NOT NULL DEFAULT 0,
		cached_input_tokens integer NOT NULL DEFAULT 0,
		output_tokens integer NOT NULL DEFAULT 0,
		total_tokens integer NOT NULL DEFAULT 0,
		unit_price_snapshot text,
		question_text varchar(512),
		amount numeric NOT NULL DEFAULT 0,
		balance_before numeric NOT NULL DEFAULT 0,
		balance_after numeric NOT NULL DEFAULT 0,
		hidden_from_statement numeric NOT NULL DEFAULT 0,
		operator_id integer NOT NULL DEFAULT 0,
		operator_username varchar(255),
		adjustment_mode varchar(16),
		request_id varchar(64),
		search_mode varchar(16),
		client_ip varchar(64),
		related_order_id integer,
		remark varchar(255),
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy billing table: %v", err)
	}
	if err := db.Exec(`INSERT INTO billing_records (user_id, type, amount, balance_before, balance_after, request_id)
		VALUES (1, 'normal_search', 2.5, 10, 7.5, 'legacy-request')`).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := ensureBillingWalletSplitSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}

	for _, column := range []string{"gift_charged", "principal_charged"} {
		if !db.Migrator().HasColumn(entity.TableNameBillingRecord, column) {
			t.Fatalf("billing_records.%s must be added", column)
		}
	}
	// 历史行不回填：两列都是 0。
	var stored entity.BillingRecord
	if err := db.Where("request_id = ?", "legacy-request").First(&stored).Error; err != nil {
		t.Fatalf("load legacy row: %v", err)
	}
	if stored.GiftCharged != 0 || stored.PrincipalCharged != 0 {
		t.Fatalf("historical rows must stay at 0, got gift=%v principal=%v",
			stored.GiftCharged, stored.PrincipalCharged)
	}
	// 原有金额不受影响。
	if stored.Amount != 2.5 {
		t.Fatalf("migration must not touch existing amounts, got %v", stored.Amount)
	}
}

// 幂等：列已存在时重跑不报错、不改数据。
func TestEnsureBillingWalletSplitSchemaIsIdempotent(t *testing.T) {
	db := newBillingWalletSplitTestDB(t)
	if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate billing: %v", err)
	}
	if err := db.Create(&entity.BillingRecord{
		UserID: 1, Type: "normal_search", Amount: 1, GiftCharged: 0.25, PrincipalCharged: 0.75,
		RequestID: "kept",
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	for run := 0; run < 2; run++ {
		if err := ensureBillingWalletSplitSchema(db); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	var stored entity.BillingRecord
	if err := db.Where("request_id = ?", "kept").First(&stored).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if stored.GiftCharged != 0.25 || stored.PrincipalCharged != 0.75 {
		t.Fatalf("re-run must not rewrite existing split data, got gift=%v principal=%v",
			stored.GiftCharged, stored.PrincipalCharged)
	}
}

// 空库（billing_records 还不存在）直接返回，不 panic。
func TestEnsureBillingWalletSplitSchemaSkipsWhenTableMissing(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := ensureBillingWalletSplitSchema(db); err != nil {
		t.Fatalf("missing billing table must not error: %v", err)
	}
}
