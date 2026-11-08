package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestScriptPricingMigrationIsAdditiveAndPreservesFixedAndZeroPrices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/scripts.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE user_scripts (id INTEGER PRIMARY KEY, answer_price DECIMAL(18,6) NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO user_scripts VALUES (1, 0.123456), (2, 0)").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureScriptPriceMultiplierSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE user_scripts SET answer_price_multiplier = 2.125 WHERE id = 2").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureScriptPriceMultiplierSchema(db); err != nil {
		t.Fatal(err)
	}
	type priceRow struct{ AnswerPrice, AnswerPriceMultiplier float64 }
	var rows []priceRow
	if err := db.Table("user_scripts").Select("answer_price, answer_price_multiplier").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != (priceRow{AnswerPrice: 0.123456}) || rows[1] != (priceRow{AnswerPriceMultiplier: 2.125}) {
		t.Fatalf("legacy fixed/zero prices and explicit multiplier must not be rewritten: %+v", rows)
	}
}
