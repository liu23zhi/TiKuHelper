package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestUpdateAccessTokenWithDBSelectedFieldsCompatibility(t *testing.T) {
	updateFailure := errors.New("synthetic metadata update failure")
	cases := []struct {
		name, initialStatus, operator         string
		input                                 UpdateAccessTokenInput
		wantName, wantStatus, wantRemark      string
		wantLimit                             float64
		wantError                             error
		missing, zeroAffectedRows, failUpdate bool
	}{
		{name: "owner_clears_remark_and_zero_limit", initialStatus: "enabled", operator: "owner",
			input:    UpdateAccessTokenInput{Name: " edited ", Status: " disabled ", Remark: "  ", TotalQuotaLimit: 0},
			wantName: "edited", wantStatus: "disabled", wantRemark: "", wantLimit: 0},
		{name: "owner_reenables_disabled_token", initialStatus: "disabled", operator: "owner",
			input:    UpdateAccessTokenInput{Name: " enabled ", Status: " eNaBlEd ", Remark: " note ", TotalQuotaLimit: 0.3},
			wantName: "enabled", wantStatus: "enabled", wantRemark: "note", wantLimit: 0.3},
		{name: "owner_unknown_status_remains_enabled", initialStatus: "disabled", operator: "owner",
			input:    UpdateAccessTokenInput{Name: " fallback ", Status: "unknown", Remark: " unknown ", TotalQuotaLimit: 0.4},
			wantName: "fallback", wantStatus: "enabled", wantRemark: "unknown", wantLimit: 0.4},
		{name: "owner_negative_limit_normalizes_to_zero", initialStatus: "enabled", operator: "owner",
			input:    UpdateAccessTokenInput{Name: "negative", Status: "disabled", Remark: " zero ", TotalQuotaLimit: -1},
			wantName: "negative", wantStatus: "disabled", wantRemark: "zero", wantLimit: 0},
		{name: "admin_updates_another_users_token", initialStatus: "enabled", operator: "admin",
			input:    UpdateAccessTokenInput{Name: " admin_edit ", Status: " disabled ", Remark: " admin-note ", TotalQuotaLimit: 0},
			wantName: "admin_edit", wantStatus: "disabled", wantRemark: "admin-note", wantLimit: 0},
		{name: "existing_noop_with_zero_affected_rows_succeeds", initialStatus: "enabled", operator: "owner", zeroAffectedRows: true,
			input:    UpdateAccessTokenInput{Name: "token_a", Status: "enabled", Remark: "before", TotalQuotaLimit: 0.1},
			wantName: "token_a", wantStatus: "enabled", wantRemark: "before", wantLimit: 0.1},
		{name: "non_owner_rejected", initialStatus: "enabled", operator: "other",
			input: UpdateAccessTokenInput{Name: "forbidden", Status: "disabled", TotalQuotaLimit: 0}, wantError: ErrAccessTokenForbidden},
		{name: "blank_name_rejected", initialStatus: "enabled", operator: "owner",
			input: UpdateAccessTokenInput{Name: " \t ", Status: "disabled", TotalQuotaLimit: 0}, wantError: ErrAccessTokenNameRequired},
		{name: "missing_token_rejected", initialStatus: "enabled", operator: "owner", missing: true,
			input: UpdateAccessTokenInput{Name: "missing", Status: "disabled", TotalQuotaLimit: 0}, wantError: ErrAccessTokenNotFound},
		{name: "update_error_returns_without_readback", initialStatus: "enabled", operator: "owner", failUpdate: true,
			input: UpdateAccessTokenInput{Name: "not_written", Status: "disabled", TotalQuotaLimit: 0}, wantError: updateFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newAccessTokenTestDB(t)
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sqlDB.Close(); err != nil {
					t.Errorf("close test database: %v", err)
				}
			})
			owner, initial := seedAccessTokenMetadataUsage(t, db, tc.initialStatus)
			operator := owner
			switch tc.operator {
			case "admin":
				operator = entity.User{ID: 9, Role: " admin "}
			case "other":
				operator = entity.User{ID: 3, Role: "user"}
			}
			reads := 0
			if err := db.Callback().Query().Before("gorm:query").Register("metadata_compatibility_reads", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*entity.AccessToken); ok {
					reads++
				}
			}); err != nil {
				t.Fatal(err)
			}
			zeroCountReports := 0
			if tc.zeroAffectedRows {
				// Simulate an adapter reporting zero for an existing logical no-op. This is
				// not a native MySQL test, and the row must still be read back successfully.
				if err := db.Callback().Update().After("gorm:update").Register("metadata_zero_rows", func(tx *gorm.DB) {
					if tx.Error == nil {
						tx.RowsAffected = 0
						zeroCountReports++
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failUpdate {
				if err := db.Callback().Update().Before("gorm:update").Register("metadata_update_error", func(tx *gorm.DB) {
					tx.AddError(updateFailure)
				}); err != nil {
					t.Fatal(err)
				}
			}
			id := initial.ID
			if tc.missing {
				id += 99
			}
			updated, err := UpdateAccessTokenWithDB(db, operator, id, tc.input)
			readsAfterCall := reads
			stored := loadAccessTokenMetadataUsage(t, db, initial.ID)
			assertAccessTokenMetadataIdentity(t, stored, initial)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("expected %v, got %v", tc.wantError, err)
				}
				if readsAfterCall != 1 {
					t.Fatalf("rejected update performed readback: queries=%d", readsAfterCall)
				}
				if stored.Name != initial.Name || stored.Status != initial.Status || stored.Remark != initial.Remark ||
					stored.TotalQuotaLimit != initial.TotalQuotaLimit || stored.TotalAmountUsed != initial.TotalAmountUsed ||
					!stored.UpdatedAt.Equal(initial.UpdatedAt) {
					t.Fatalf("rejected update changed stored token: %+v", stored)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if readsAfterCall != 2 {
				t.Fatalf("successful update must load and read back: queries=%d", readsAfterCall)
			}
			if tc.zeroAffectedRows && zeroCountReports != 1 {
				t.Fatalf("zero-row adapter result was not exercised: reports=%d", zeroCountReports)
			}
			for _, token := range []entity.AccessToken{updated, stored} {
				assertAccessTokenMetadataIdentity(t, token, initial)
				if token.Name != tc.wantName || token.Status != tc.wantStatus || token.Remark != tc.wantRemark ||
					token.TotalQuotaLimit != tc.wantLimit || token.TotalAmountUsed != initial.TotalAmountUsed ||
					token.UpdatedAt.IsZero() || token.UpdatedAt.Before(initial.UpdatedAt) {
					t.Fatalf("selected fields lost zero values or changed protected fields: %+v", token)
				}
			}
		})
	}
}

func TestUpdateAccessTokenWithDBDoesNotRecreateConcurrentlyDeletedToken(t *testing.T) {
	a, b := newAccessTokenMetadataUsagePair(t)
	owner, initial := seedAccessTokenMetadataUsage(t, a, "enabled")
	deleted, deletedRows, absentBeforeResume := false, int64(-1), int64(-1)
	var deleteError error
	if err := a.Callback().Query().After("gorm:query").Register("metadata_delete_after_load", func(tx *gorm.DB) {
		token, ok := tx.Statement.Dest.(*entity.AccessToken)
		if tx.Error != nil || !ok || token.ID != initial.ID || deleted {
			return
		}
		deleted = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		deletion := b.WithContext(ctx).Delete(&entity.AccessToken{}, initial.ID)
		cancel()
		deletedRows, deleteError = deletion.RowsAffected, deletion.Error
		if deleteError != nil {
			tx.AddError(deleteError)
			return
		}
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		deleteError = b.WithContext(ctx).Model(&entity.AccessToken{}).Where("id = ?", initial.ID).Count(&absentBeforeResume).Error
		cancel()
		if deleteError != nil {
			tx.AddError(deleteError)
		}
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	updated, err := UpdateAccessTokenWithDB(a.WithContext(ctx), owner, initial.ID,
		UpdateAccessTokenInput{Name: "after_delete", Status: "disabled", Remark: "", TotalQuotaLimit: 0})
	cancel()
	if !deleted || deleteError != nil || deletedRows != 1 || absentBeforeResume != 0 {
		t.Fatalf("independent deletion not committed before metadata update: deleted=%t rows=%d count=%d error=%v", deleted, deletedRows, absentBeforeResume, deleteError)
	}
	var remaining int64
	if e := b.Model(&entity.AccessToken{}).Where("id = ?", initial.ID).Count(&remaining).Error; e != nil {
		t.Fatal(e)
	}
	t.Logf("deleted token after metadata update: rows=%d returned=%+v error=%v", remaining, updated, err)
	if remaining != 0 {
		t.Fatal("metadata update recreated concurrently deleted token")
	}
	if !errors.Is(err, ErrAccessTokenNotFound) {
		t.Fatalf("expected not-found after concurrent delete, got %v", err)
	}
	if updated != (entity.AccessToken{}) {
		t.Fatalf("not-found returned a stale token: %+v", updated)
	}
}

func TestUpdateAccessTokenWithDBReadbackFailureDoesNotUndoMetadataWrite(t *testing.T) {
	db := newAccessTokenTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	owner, initial := seedAccessTokenMetadataUsage(t, db, "enabled")
	readbackFailure := errors.New("synthetic metadata readback failure")
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register("metadata_readback_error", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*entity.AccessToken); !ok {
			return
		}
		reads++
		if reads == 2 {
			tx.AddError(readbackFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateAccessTokenWithDB(db, owner, initial.ID,
		UpdateAccessTokenInput{Name: " after_readback_failure ", Status: "disabled", Remark: "", TotalQuotaLimit: 0})
	if reads != 2 || !errors.Is(err, readbackFailure) {
		t.Fatalf("expected original readback error on second query, reads=%d error=%v", reads, err)
	}
	if updated != (entity.AccessToken{}) {
		t.Fatalf("readback failure returned stale token: %+v", updated)
	}
	if e := db.Callback().Query().Remove("metadata_readback_error"); e != nil {
		t.Fatal(e)
	}
	stored := loadAccessTokenMetadataUsage(t, db, initial.ID)
	assertAccessTokenMetadataIdentity(t, stored, initial)
	if stored.Name != "after_readback_failure" || stored.Status != "disabled" || stored.Remark != "" || stored.TotalQuotaLimit != 0 ||
		stored.TotalAmountUsed != initial.TotalAmountUsed || stored.UpdatedAt.IsZero() || stored.UpdatedAt.Before(initial.UpdatedAt) {
		t.Fatalf("readback error must not be misreported as a metadata-write rollback: %+v", stored)
	}
	t.Log("readback failed after the metadata write had already completed")
}
