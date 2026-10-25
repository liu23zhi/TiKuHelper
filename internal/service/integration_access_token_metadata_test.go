package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"testing"
)

func TestIntegrationTokenMetadataPreservesConcurrentUsage(t *testing.T) {
	db := newAccessTokenTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	user := seedAccessTokenOwner(t, db, 1, "user")
	created, err := CreateAccessTokenWithDB(db, user, CreateAccessTokenInput{Name: "旧名称", TotalQuotaLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	if err := db.Callback().Update().Before("gorm:update").Register("integration:advance_usage", func(tx *gorm.DB) {
		if changed || tx.Statement.Table != entity.TableNameAccessToken {
			return
		}
		changed = true
		if err := tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE access_tokens SET total_amount_used = ?, last_used_ip = ? WHERE id = ?", 7.25, "203.0.113.8", created.AccessToken.ID).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove("integration:advance_usage")
	if _, err := UpdateAccessTokenWithDB(db, user, created.AccessToken.ID, UpdateAccessTokenInput{Name: "协作@新名称", Status: "disabled", Remark: "metadata", TotalQuotaLimit: 20}); err != nil {
		t.Fatal(err)
	}
	var got entity.AccessToken
	if err := db.First(&got, created.AccessToken.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !changed || got.TotalAmountUsed != 7.25 || got.LastUsedIP != "203.0.113.8" {
		t.Fatalf("METADATA_CLOBBER: concurrent fields overwritten: %+v", got)
	}
	if got.Name != "协作@新名称" || got.Status != "disabled" || got.TotalQuotaLimit != 20 {
		t.Fatalf("metadata not applied: %+v", got)
	}
}
