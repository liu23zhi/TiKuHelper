package service

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newIdempotencyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:idempotency-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestIdempotencyClaimReplayConflictAndExpiry(t *testing.T) {
	db := newIdempotencyTestDB(t)
	first, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "balance", "request-1", []byte(`{"amount":1}`))
	if err != nil || !fresh {
		t.Fatalf("first claim: fresh=%v err=%v", fresh, err)
	}
	if err := CompleteIdempotencyWithDB(db, first.ID, 200, []byte(`{"balance":11}`)); err != nil {
		t.Fatal(err)
	}
	replay, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "balance", "request-1", []byte(`{"amount":1}`))
	if err != nil || fresh || replay.HTTPStatus != 200 || replay.ResponseJSON != `{"balance":11}` {
		t.Fatalf("replay: %+v fresh=%v err=%v", replay, fresh, err)
	}
	if _, _, err := ClaimIdempotencyWithDB(db, "user:1", "balance", "request-1", []byte(`{"amount":2}`)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if _, fresh, err := ClaimIdempotencyWithDB(db, "user:2", "balance", "request-1", []byte(`{"amount":2}`)); err != nil || !fresh {
		t.Fatalf("other user should be independent: fresh=%v err=%v", fresh, err)
	}
	if err := db.Model(&entity.IdempotencyRecord{}).Where("id = ?", first.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "balance", "request-1", []byte(`{"amount":2}`)); err != nil || !fresh {
		t.Fatalf("expired terminal claim should be reusable: fresh=%v err=%v", fresh, err)
	}
}

func TestIdempotencyProcessingAndUnknownAreNotReplayed(t *testing.T) {
	db := newIdempotencyTestDB(t)
	record, _, err := ClaimIdempotencyWithDB(db, "user:1", "recharge", "request-1", []byte(`{"amount":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "recharge", "request-1", []byte(`{"amount":10}`)); err != nil || fresh {
		t.Fatalf("in-progress operation must not execute again: fresh=%v err=%v", fresh, err)
	}
	if err := MarkIdempotencyUnknownWithDB(db, record.ID); err != nil {
		t.Fatal(err)
	}
	if got, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "recharge", "request-1", []byte(`{"amount":10}`)); err != nil || fresh || got.Status != IdempotencyStatusUnknown {
		t.Fatalf("unknown result must not execute again: %+v fresh=%v err=%v", got, fresh, err)
	}
	if err := db.Model(&entity.IdempotencyRecord{}).Where("id = ?", record.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "recharge", "request-1", []byte(`{"amount":10}`)); err != nil || fresh {
		t.Fatalf("unknown operation must remain reserved after expiry: fresh=%v err=%v", fresh, err)
	}
}

func TestIdempotencyCleanupRemovesOnlyExpiredTerminalRecords(t *testing.T) {
	db := newIdempotencyTestDB(t)
	create := func(key string) entity.IdempotencyRecord {
		record, fresh, err := ClaimIdempotencyWithDB(db, "user:1", "cleanup", key, []byte(`{"value":1}`))
		if err != nil || !fresh {
			t.Fatalf("claim %s: fresh=%v err=%v", key, fresh, err)
		}
		return record
	}
	succeeded := create("succeeded")
	failed := create("failed")
	processing := create("processing")
	unknown := create("unknown")
	retained := create("retained")
	if err := CompleteIdempotencyWithDB(db, succeeded.ID, 200, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := FinishIdempotencyWithDB(db, failed.ID, IdempotencyStatusFailed, 400, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := CompleteIdempotencyWithDB(db, retained.ID, 200, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := MarkIdempotencyUnknownWithDB(db, unknown.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{succeeded.ID, failed.ID, processing.ID, unknown.ID} {
		if err := db.Model(&entity.IdempotencyRecord{}).Where("id = ?", id).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
	}
	stop := StartIdempotencyCleanup(db, 5*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int64
		db.Model(&entity.IdempotencyRecord{}).Where("id IN ?", []int64{succeeded.ID, failed.ID}).Count(&count)
		if count == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var records []entity.IdempotencyRecord
	if err := db.Order("id").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].ID != processing.ID || records[1].ID != unknown.ID || records[2].ID != retained.ID {
		t.Fatalf("cleanup removed active/unknown or recent records: %+v", records)
	}
}
