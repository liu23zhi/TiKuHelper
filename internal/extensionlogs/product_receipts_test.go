package extensionlogs

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestProductReceiptsExpireByServerTimeAndInvalidateSnapshot(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	start := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	now := start
	s.now = func() time.Time { return now }
	meta := receiptFixtureMeta()
	first := receiptFixtureBatch(310, receiptFixtureEvent(1, "2026-01-02T08:00:00.000Z"))
	second := receiptFixtureBatch(311, receiptFixtureEvent(2, "2026-01-02T06:00:00.000Z"))
	p1, err := s.AppendReceipt(ctx, 31, meta, first)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.AppendReceipt(ctx, 31, meta, first); err != nil || !reflect.DeepEqual(p1, replay) {
		t.Fatal("lost response did not produce the same proof", err)
	}
	now = now.Add(time.Hour)
	if _, err = s.AppendReceipt(ctx, 31, meta, second); err != nil {
		t.Fatal(err)
	}
	rows, cursor, err := s.ReadReceipts(ctx, Filter{UserID: 31}, 1, "")
	if err != nil || len(rows) != 1 || rows[0].Time != second.Entries[0].Time || cursor == "" || rows[0].DeviceID == meta.DeviceID {
		t.Fatalf("unexpected initial page: rows=%d cursor=%v err=%v", len(rows), cursor != "", err)
	}
	if filtered, _, err := s.ReadReceipts(ctx, Filter{DeviceID: rows[0].DeviceID}, 10, ""); err != nil || len(filtered) != 2 {
		t.Fatalf("filtered pseudonymous ID: %d %v", len(filtered), err)
	}
	if _, _, err = s.ReadReceipts(ctx, Filter{UserID: 32}, 1, cursor); !errors.Is(err, ErrCursor) {
		t.Fatal("cursor replayed across owner")
	}
	now = start.Add(7 * 24 * time.Hour)
	if _, _, err = s.ReadReceipts(ctx, Filter{UserID: 31}, 1, cursor); !errors.Is(err, ErrCursor) {
		t.Fatal("expired page returned partial results")
	}
	if rows, _, err = s.ReadReceipts(ctx, Filter{}, 3, ""); err != nil || len(rows) != 1 || rows[0].Time != second.Entries[0].Time {
		t.Fatal("strict receipt cutoff not applied")
	}
	if err = s.Sweep(); err != nil {
		t.Fatal(err)
	}
	j := s.receiptJournal(Policy{true, 7, 10})
	db, err := j.open(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var batches, events int
	if err = db.QueryRow("SELECT (SELECT count(*) FROM receipt_segments), (SELECT count(*) FROM receipt_events)").Scan(&batches, &events); err != nil || batches != 1 || events != 1 {
		t.Fatalf("expired records not physically removed: %d/%d %v", batches, events, err)
	}
}

func TestProductReceiptsDoNotCreateStorageWhileDisabledOrOnRead(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	if rows, _, err := s.ReadReceipts(ctx, Filter{}, 1, ""); err != nil || len(rows) != 0 {
		t.Fatal(err)
	}
	if err := s.SetPolicy(Policy{false, 7, 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendReceipt(ctx, 31, receiptFixtureMeta(), receiptFixtureBatch(300, receiptFixtureEvent(1, fixtureTime))); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled append did not fail closed", err)
	}
	if _, err := s.receiptJournal(Policy{true, 7, 10}).path(false); !os.IsNotExist(err) {
		t.Fatal("created journal before a real allowed event", err)
	}
}

func TestProductReceiptSequenceIsAssignedByServerAndRetainsOldCap(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	if err := s.SetPolicy(Policy{true, 365, 220}); err != nil {
		t.Fatal(err)
	}
	if p, e := s.Policy(); e != nil || p.RetentionDays != 7 || p.MaxGB != 10 {
		t.Fatalf("wrong effective caps: %+v %v", p, e)
	}
	a, b := receiptFixtureMeta(), receiptFixtureMeta()
	b.DeviceID = "different-owned-device"
	for _, meta := range []Session{a, b} {
		if _, err := s.AppendReceipt(ctx, 991, meta, receiptFixtureBatch(901, receiptFixtureEvent(1234, fixtureTime))); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := s.ReadReceipts(ctx, Filter{UserID: 991}, 5, "")
	if err != nil || len(rows) != 2 || rows[0].Seq == 1234 || rows[1].Seq == 1234 || rows[0].Seq == rows[1].Seq {
		t.Fatal("operator view reused client-supplied ordering as server sequence", err)
	}
}
