package service

import (
	"errors"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"testing"
)

func TestFutureKeyPolicyReadbackFailureDoesNotUndoCommittedMetadataAndAudit(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner, initial := seedAccessTokenMetadataUsage(t, db, "enabled")
	failure := errors.New("post-commit readback unavailable")
	reads := 0
	db.Callback().Query().Before("gorm:query").Register("future:key_readback", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*entity.AccessToken); ok {
			reads++
			if reads == 3 {
				tx.AddError(failure)
			}
		}
	})
	updated, err := UpdateAccessTokenWithDB(db, owner, initial.ID, UpdateAccessTokenInput{Name: "after_readback", Status: "disabled", TotalQuotaLimit: 0})
	db.Callback().Query().Remove("future:key_readback")
	stored := loadAccessTokenMetadataUsage(t, db, initial.ID)
	var audits int64
	db.Model(&entity.Log{}).Where("module = ? AND action = ?", "access_token", 1).Count(&audits)
	t.Logf("key-readback reads=%d err=%v stored-name=%s status=%s usage=%.6f audits=%d", reads, err, stored.Name, stored.Status, stored.TotalAmountUsed, audits)
	if !errors.Is(err, failure) || updated != (entity.AccessToken{}) || stored.Name != "after_readback" || stored.Status != "disabled" || stored.TotalAmountUsed != initial.TotalAmountUsed || audits != 1 {
		t.Fatal("post-commit readback failure must preserve committed metadata+audit")
	}
}
func TestFutureKeyPolicyNoopStillReadsFreshWithoutAudit(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner, initial := seedAccessTokenMetadataUsage(t, db, "enabled")
	reads := 0
	db.Callback().Query().Before("gorm:query").Register("future:key_noop", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*entity.AccessToken); ok {
			reads++
		}
	})
	updated, err := UpdateAccessTokenWithDB(db, owner, initial.ID, UpdateAccessTokenInput{Name: initial.Name, Status: initial.Status, Remark: initial.Remark, TotalQuotaLimit: initial.TotalQuotaLimit})
	db.Callback().Query().Remove("future:key_noop")
	var audits int64
	db.Model(&entity.Log{}).Where("module = ?", "access_token").Count(&audits)
	t.Logf("key-noop reads=%d err=%v usage=%.6f audits=%d", reads, err, updated.TotalAmountUsed, audits)
	if err != nil || reads != 3 || updated.TotalAmountUsed != initial.TotalAmountUsed || audits != 0 {
		t.Fatal("no-op must still run final fresh read without fake mutation/audit")
	}
}
func TestFutureKeyPolicyFinalNotFoundIsTypedAndCommitted(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner, initial := seedAccessTokenMetadataUsage(t, db, "enabled")
	reads := 0
	db.Callback().Query().Before("gorm:query").Register("future:key_404", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*entity.AccessToken); ok {
			reads++
			if reads == 3 {
				tx.AddError(gorm.ErrRecordNotFound)
			}
		}
	})
	updated, err := UpdateAccessTokenWithDB(db, owner, initial.ID, UpdateAccessTokenInput{Name: "committed_before_404", Status: "enabled"})
	db.Callback().Query().Remove("future:key_404")
	stored := loadAccessTokenMetadataUsage(t, db, initial.ID)
	t.Logf("key-final404 reads=%d err=%v committed-name=%s", reads, err, stored.Name)
	if !errors.Is(err, ErrAccessTokenNotFound) || updated != (entity.AccessToken{}) || stored.Name != "committed_before_404" {
		t.Fatal("final missing row must keep typed404 without undoing prior commit")
	}
}

func TestFutureKeyPolicyZeroRowsForExistingChangedRecordDoesNotBecome404(t *testing.T) {
	db := newAccessTokenTestDB(t)
	owner, initial := seedAccessTokenMetadataUsage(t, db, "enabled")
	reports := 0
	db.Callback().Update().After("gorm:update").Register("future:key_zero_rows", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameAccessToken && tx.Error == nil {
			tx.RowsAffected = 0
			reports++
		}
	})
	updated, err := UpdateAccessTokenWithDB(db, owner, initial.ID, UpdateAccessTokenInput{Name: "changed_despite_zero_adapter_report", Status: "enabled"})
	db.Callback().Update().Remove("future:key_zero_rows")
	t.Logf("key-zero-row-report reports=%d err=%v name=%s usage=%.6f", reports, err, updated.Name, updated.TotalAmountUsed)
	if err != nil || reports != 1 || updated.Name != "changed_despite_zero_adapter_report" || updated.TotalAmountUsed != initial.TotalAmountUsed {
		t.Fatal("adapter zero rows for existing record must not be misreported as404")
	}
}

func TestFutureKeyPolicyFreshOwnerAndNameAreRevalidated(t *testing.T) {
	for _, scope := range []string{"owner", "legacy-name"} {
		t.Run(scope, func(t *testing.T) {
			db := newAccessTokenTestDB(t)
			owner, initial := seedAccessTokenMetadataUsage(t, db, "enabled")
			seedAccessTokenOwner(t, db, 3, "user")
			inputName := "new_metadata"
			if scope == "legacy-name" {
				inputName = "legacy.name"
				if err := db.Model(&entity.AccessToken{}).Where("id = ?", initial.ID).Update("name", inputName).Error; err != nil {
					t.Fatal(err)
				}
			}
			fired := false
			db.Callback().Query().After("gorm:query").Register("future:key_fresh_guard", func(tx *gorm.DB) {
				token, ok := tx.Statement.Dest.(*entity.AccessToken)
				if !ok || token.ID != initial.ID || fired || tx.Error != nil {
					return
				}
				fired = true
				if scope == "owner" {
					tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE access_tokens SET user_id = ? WHERE id = ?", 3, initial.ID).Error)
				} else {
					tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE access_tokens SET name = ? WHERE id = ?", "fresh_name", initial.ID).Error)
				}
			})
			updated, err := UpdateAccessTokenWithDB(db, owner, initial.ID, UpdateAccessTokenInput{Name: inputName, Status: "disabled"})
			db.Callback().Query().Remove("future:key_fresh_guard")
			stored := loadAccessTokenMetadataUsage(t, db, initial.ID)
			var audits int64
			db.Model(&entity.Log{}).Where("module = ? AND action = ?", "access_token", 1).Count(&audits)
			t.Logf("key-fresh-%s err=%v owner=%d name=%s audits=%d", scope, err, stored.UserID, stored.Name, audits)
			wanted := ErrAccessTokenForbidden
			if scope == "legacy-name" {
				wanted = ErrAccessTokenNameInvalid
			}
			if !fired || !errors.Is(err, wanted) || updated != (entity.AccessToken{}) || stored.Status != "enabled" || audits != 0 {
				t.Fatal("Tx fresh row must revalidate owner and legacy-name compatibility")
			}
		})
	}
}
