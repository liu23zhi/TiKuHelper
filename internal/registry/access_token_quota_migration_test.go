package registry

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newAccessTokenQuotaMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:quota-repair-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.AccessToken{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestEnsureAccessTokenQuotaLimitRepairBatchesAndMarksComplete(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	const outOfRangeCount = accessTokenQuotaRepairBatchSize + 1
	tokens := make([]entity.AccessToken, 0, outOfRangeCount+4)
	for index := 0; index < outOfRangeCount; index++ {
		quota := 1_000_000_000_000 + float64(index)
		if index == outOfRangeCount-1 {
			quota = 1.1111111111111111e24
		}
		tokens = append(tokens, entity.AccessToken{
			UserID: 1, Name: fmt.Sprintf("overflow-%d", index), TokenHash: fmt.Sprintf("%064x", index+1),
			TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: quota,
		})
	}
	tokens = append(tokens,
		entity.AccessToken{UserID: 1, Name: "ordinary", TokenHash: fmt.Sprintf("%064x", outOfRangeCount+1), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: 5},
		entity.AccessToken{UserID: 1, Name: "unlimited", TokenHash: fmt.Sprintf("%064x", outOfRangeCount+2), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: 0},
		entity.AccessToken{UserID: 1, Name: "negative", TokenHash: fmt.Sprintf("%064x", outOfRangeCount+3), TokenPrefix: "tk_test", Status: "enabled", TotalQuotaLimit: -2},
		entity.AccessToken{UserID: 1, Name: "null", TokenHash: fmt.Sprintf("%064x", outOfRangeCount+4), TokenPrefix: "tk_test", Status: "enabled"},
	)
	if err := db.CreateInBatches(&tokens, 100).Error; err != nil {
		t.Fatalf("seed access tokens: %v", err)
	}
	if err := db.Table(entity.TableNameAccessToken).Where("name = ?", "null").Update("total_quota_limit", nil).Error; err != nil {
		t.Fatalf("set null quota: %v", err)
	}

	if err := ensureAccessTokenQuotaLimitData(db); err != nil {
		t.Fatalf("repair access token quotas: %v", err)
	}
	if !systemSettingFlagIsSet(db, accessTokenQuotaLimitRepairedKey) {
		t.Fatal("expected repair marker to be set")
	}

	var repaired int64
	if err := db.Model(&entity.AccessToken{}).Where("name LIKE ? AND total_quota_limit = 0", "overflow-%").Count(&repaired).Error; err != nil {
		t.Fatalf("count repaired rows: %v", err)
	}
	if repaired != outOfRangeCount {
		t.Fatalf("repaired out-of-range rows = %d, want %d", repaired, outOfRangeCount)
	}
	var ordinary entity.AccessToken
	if err := db.Where("name = ?", "ordinary").First(&ordinary).Error; err != nil {
		t.Fatalf("load ordinary quota: %v", err)
	}
	if ordinary.TotalQuotaLimit != 5 {
		t.Fatalf("ordinary quota changed to %v, want 5", ordinary.TotalQuotaLimit)
	}
	var negative entity.AccessToken
	if err := db.Where("name = ?", "negative").First(&negative).Error; err != nil {
		t.Fatalf("load negative quota: %v", err)
	}
	if negative.TotalQuotaLimit != 0 {
		t.Fatalf("negative quota = %v, want 0", negative.TotalQuotaLimit)
	}
	var nullQuota *float64
	if err := db.Table(entity.TableNameAccessToken).Select("total_quota_limit").Where("name = ?", "null").Scan(&nullQuota).Error; err != nil {
		t.Fatalf("load null quota: %v", err)
	}
	if nullQuota != nil {
		t.Fatalf("null quota changed to %v, want nil", *nullQuota)
	}

	updates := 0
	if err := db.Callback().Update().Before("gorm:update").Register("count_quota_repair_updates", func(tx *gorm.DB) {
		updates++
	}); err != nil {
		t.Fatalf("register update counter: %v", err)
	}
	if err := ensureAccessTokenQuotaLimitData(db); err != nil {
		t.Fatalf("rerun completed repair: %v", err)
	}
	if updates != 0 {
		t.Fatalf("completed repair performed %d updates, want 0", updates)
	}
}

func TestEnsureAccessTokenQuotaLimitRepairRunsAfterLegacyMarker(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	token := entity.AccessToken{
		UserID: 1, Name: "legacy-negative", TokenHash: fmt.Sprintf("%064x", 1), TokenPrefix: "tk_test",
		Status: "enabled", TotalQuotaLimit: -2,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("seed access token: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{Key: "access_token_quota_limit_repaired", Value: "1"}).Error; err != nil {
		t.Fatalf("seed legacy repair marker: %v", err)
	}

	if err := ensureAccessTokenQuotaLimitData(db); err != nil {
		t.Fatalf("repair access token quotas after legacy marker: %v", err)
	}
	if !systemSettingFlagIsSet(db, accessTokenQuotaLimitRepairedKey) {
		t.Fatal("expected version 2 repair marker to be set")
	}
	if !systemSettingFlagIsSet(db, "access_token_quota_limit_repaired") {
		t.Fatal("legacy repair marker must be preserved")
	}
	var stored entity.AccessToken
	if err := db.First(&stored, token.ID).Error; err != nil {
		t.Fatalf("load repaired token: %v", err)
	}
	if stored.TotalQuotaLimit != 0 {
		t.Fatalf("repaired quota = %v, want 0", stored.TotalQuotaLimit)
	}
}

func TestEnsureAccessTokenQuotaLimitDataRetriesAfterUpdateFailure(t *testing.T) {
	db := newAccessTokenQuotaMigrationDB(t)
	token := entity.AccessToken{
		UserID: 1, Name: "overflow", TokenHash: fmt.Sprintf("%064x", 1), TokenPrefix: "tk_test",
		Status: "enabled", TotalQuotaLimit: 1_000_000_000_000,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("seed access token: %v", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("fail_quota_repair_update", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameAccessToken {
			tx.AddError(fmt.Errorf("injected update failure"))
		}
	}); err != nil {
		t.Fatalf("register failure callback: %v", err)
	}

	if err := ensureAccessTokenQuotaLimitData(db); err == nil {
		t.Fatal("expected repair error so startup can defer access token schema migration")
	}
	if systemSettingFlagIsSet(db, accessTokenQuotaLimitRepairedKey) {
		t.Fatal("repair marker must not be set after a failed update")
	}
	if err := db.Callback().Update().Remove("fail_quota_repair_update"); err != nil {
		t.Fatalf("remove failure callback: %v", err)
	}

	if err := ensureAccessTokenQuotaLimitData(db); err != nil {
		t.Fatalf("retry access token quota repair: %v", err)
	}
	if !systemSettingFlagIsSet(db, accessTokenQuotaLimitRepairedKey) {
		t.Fatal("expected repair marker after a successful retry")
	}
	var stored entity.AccessToken
	if err := db.First(&stored, token.ID).Error; err != nil {
		t.Fatalf("load repaired token: %v", err)
	}
	if stored.TotalQuotaLimit != 0 {
		t.Fatalf("repaired quota = %v, want 0", stored.TotalQuotaLimit)
	}
}
