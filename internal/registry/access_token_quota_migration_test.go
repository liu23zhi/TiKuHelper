package registry

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newAccessTokenQuotaMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:quota-inspect-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.AccessToken{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func quotaInspectionSnapshot(t *testing.T, db *gorm.DB) map[string][]map[string]any {
	t.Helper()
	result := make(map[string][]map[string]any)
	for _, table := range []string{entity.TableNameAccessToken, entity.TableNameSystemSetting} {
		var rows []map[string]any
		if err := db.Table(table).Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		result[table] = rows
	}
	return result
}

func TestEnsureAccessTokenQuotaLimitRejectsInvalidDataWithoutRepair(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	// Retain the upstream multi-batch-size corpus; none may be rewritten to unlimited.
	const outOfRangeCount = 501
	tokens := make([]entity.AccessToken, 0, outOfRangeCount+4)
	for index := 0; index < outOfRangeCount; index++ {
		quota := 1_000_000_000_000 + float64(index)
		if index == outOfRangeCount-1 {
			quota = 1.1111111111111111e24
		}
		tokens = append(tokens, entity.AccessToken{UserID: 1, Name: fmt.Sprintf("overflow-%d", index), TokenHash: fmt.Sprintf("%064x", index+1), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: quota})
	}
	for i, item := range []struct {
		name  string
		quota float64
	}{{"ordinary", 5}, {"unlimited", 0}, {"negative", -2}, {"null", 0}} {
		tokens = append(tokens, entity.AccessToken{UserID: 1, Name: item.name, TokenHash: fmt.Sprintf("%064x", outOfRangeCount+i+1), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: item.quota})
	}
	if err := db.CreateInBatches(&tokens, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(entity.TableNameAccessToken).Where("name = ?", "null").Update("total_quota_limit", nil).Error; err != nil {
		t.Fatal(err)
	}
	before := quotaInspectionSnapshot(t, db)
	writes := 0
	if err := db.Callback().Update().Before("gorm:update").Register("quota:forbid-update", func(tx *gorm.DB) { writes++; tx.AddError(errors.New("quota inspection attempted update")) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("quota:forbid-create", func(tx *gorm.DB) { writes++; tx.AddError(errors.New("quota inspection attempted create")) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register("quota:forbid-delete", func(tx *gorm.DB) { writes++; tx.AddError(errors.New("quota inspection attempted delete")) }); err != nil {
		t.Fatal(err)
	}
	for call := 0; call < 2; call++ {
		if err := ensureAccessTokenQuotaLimitData(db); !errors.Is(err, ErrAccessTokenQuotaLimitDataInvalid) {
			t.Fatalf("call %d: wanted explicit quota recovery error, got %v", call, err)
		}
		if !reflect.DeepEqual(before, quotaInspectionSnapshot(t, db)) {
			t.Fatal("quota inspection changed stored token/marker data")
		}
	}
	if writes != 0 {
		t.Fatalf("inspection attempted %d writes", writes)
	}
}

func TestEnsureAccessTokenQuotaLimitDoesNotTrustRepairMarkers(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	if err := db.Create(&entity.AccessToken{UserID: 1, Name: "legacy-negative", TokenHash: fmt.Sprintf("%064x", 1), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: -2}).Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"access_token_quota_limit_repaired", accessTokenQuotaLimitRepairedKey} {
		if err := db.Create(&entity.SystemSetting{Key: key, Value: "1"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := quotaInspectionSnapshot(t, db)
	for call := 0; call < 2; call++ {
		if err := ensureAccessTokenQuotaLimitData(db); !errors.Is(err, ErrAccessTokenQuotaLimitDataInvalid) {
			t.Fatalf("marker bypassed quota rejection: %v", err)
		}
	}
	if !reflect.DeepEqual(before, quotaInspectionSnapshot(t, db)) {
		t.Fatal("legacy marker or quota mutated")
	}
}

func TestEnsureAccessTokenQuotaLimitPropagatesInspectionFailure(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	injected := errors.New("synthetic quota inspection failure")
	if err := db.Callback().Query().Before("gorm:query").Register("quota:fail-query", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameAccessToken {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureAccessTokenQuotaLimitData(db); !errors.Is(err, injected) {
		t.Fatalf("inspection error was swallowed: %v", err)
	}
	if err := db.Callback().Query().Remove("quota:fail-query"); err != nil {
		t.Fatal(err)
	}
	before := quotaInspectionSnapshot(t, db)
	for call := 0; call < 2; call++ {
		if err := ensureAccessTokenQuotaLimitData(db); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, quotaInspectionSnapshot(t, db)) {
		t.Fatal("empty valid inspection wrote a marker")
	}
}

func TestEnsureAccessTokenQuotaLimitPreservesValidAndNullData(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	for i, quota := range []float64{0, 0.000001, 5, 999999999999.5} {
		if err := db.Create(&entity.AccessToken{UserID: 1, Name: fmt.Sprint(i), TokenHash: fmt.Sprintf("%064x", i+1), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: quota}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.AccessToken{}).Where("name = ?", "0").Update("total_quota_limit", nil).Error; err != nil {
		t.Fatal(err)
	}
	before := quotaInspectionSnapshot(t, db)
	for call := 0; call < 2; call++ {
		if err := ensureAccessTokenQuotaLimitData(db); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, quotaInspectionSnapshot(t, db)) {
		t.Fatal("valid/null limit data changed")
	}
}
