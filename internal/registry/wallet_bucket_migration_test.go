package registry

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newWalletBucketSplitTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	return db
}

// 方案 A（用户 2026-11-09 拍板）：存量 gift_balance 全部清零，balance 保持不变。
//
// 为什么必须清零：旧模型的 gift_balance 是「累计发放额」（只增不减），
// 里面包含早已被消费掉的部分。新模型下它变成**可用余额**，
// 不清零 = 让用户凭一个历史计数器再花一次钱。
func TestWalletBucketSplitZeroesLegacyGiftBalance(t *testing.T) {
	db := newWalletBucketSplitTestDB(t)

	// 存量三态：有赠金、无赠金、本金为负（欠款）。
	if err := db.Create(&entity.User{ID: 1, Username: "has-gift", Balance: 100, GiftBalance: 40}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.User{ID: 2, Username: "no-gift", Balance: 5, GiftBalance: 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.User{ID: 3, Username: "debt", Balance: -2, GiftBalance: 7}).Error; err != nil {
		t.Fatal(err)
	}
	beforePrincipal := sumUserColumn(t, db, "balance")

	if err := ensureWalletBucketSplitSchema(db); err != nil {
		t.Fatalf("split migration: %v", err)
	}

	// 赠金全部归零。
	var nonZero int64
	if err := db.Model(&entity.User{}).Where("gift_balance <> 0").Count(&nonZero).Error; err != nil {
		t.Fatal(err)
	}
	if nonZero != 0 {
		t.Fatalf("all legacy gift_balance must be zeroed, %d rows remain", nonZero)
	}
	// 本金一列一个字节都不动（含负数）。
	if after := sumUserColumn(t, db, "balance"); after != beforePrincipal {
		t.Fatalf("principal must be untouched: %v -> %v", beforePrincipal, after)
	}
	var debt entity.User
	if err := db.Where("id = ?", 3).First(&debt).Error; err != nil {
		t.Fatal(err)
	}
	if debt.Balance != -2 {
		t.Fatalf("negative principal (debt) must be preserved, got %v", debt.Balance)
	}
	// 一次性标记必须写入。
	if !systemSettingFlagIsSet(db, walletBucketSplitGiftZeroKey) {
		t.Fatal("one-shot flag must be written after a successful split")
	}
}

// 标记写过之后再产生的赠金**不能**被再次清零 —— 这条防的是
// 「把一次性操作写成每次启动都跑」，那会把新模型下真实发放的赠金反复抹掉。
func TestWalletBucketSplitDoesNotClearNewGiftAfterFlag(t *testing.T) {
	db := newWalletBucketSplitTestDB(t)
	if err := db.Create(&entity.User{ID: 1, Username: "u1", Balance: 10, GiftBalance: 3}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureWalletBucketSplitSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// 迁移之后，新模型正常发放一笔赠金。
	if err := db.Model(&entity.User{}).Where("id = ?", 1).Update("gift_balance", 8).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureWalletBucketSplitSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var stored entity.User
	if err := db.Where("id = ?", 1).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.GiftBalance != 8 {
		t.Fatalf("new gift issued after the split must survive re-runs, got %v", stored.GiftBalance)
	}
}

// 空库（没有任何赠金）也要能跑通并写标记，不能因为「没有要改的行」就跳过标记。
func TestWalletBucketSplitWritesFlagOnEmptyGift(t *testing.T) {
	db := newWalletBucketSplitTestDB(t)
	if err := db.Create(&entity.User{ID: 1, Username: "clean", Balance: 10, GiftBalance: 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureWalletBucketSplitSchema(db); err != nil {
		t.Fatalf("empty split: %v", err)
	}
	if !systemSettingFlagIsSet(db, walletBucketSplitGiftZeroKey) {
		t.Fatal("flag must be written even when nothing needed clearing")
	}
}

func sumUserColumn(t *testing.T, db *gorm.DB, column string) float64 {
	t.Helper()

	var row struct {
		Total float64 `gorm:"column:total"`
	}
	if err := db.Model(&entity.User{}).Select("COALESCE(SUM(" + column + "), 0) AS total").Scan(&row).Error; err != nil {
		t.Fatalf("sum %s: %v", column, err)
	}
	return row.Total
}
