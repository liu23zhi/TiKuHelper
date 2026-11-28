package extensionlogs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func assertNoReceiptProof(t *testing.T, j *ReceiptJournal, owner int32, meta Session, batch ReceiptBatch, want error) {
	t.Helper()
	got, err := j.LookupReceiptProof(context.Background(), owner, meta, batch)
	if !errors.Is(err, want) || !reflect.DeepEqual(got, ReceiptProof{}) {
		t.Fatalf("expected no proof and %v, got %+v (err=%v)", want, got, err)
	}
}

func TestReceiptJournalLookupReceiptProofColdAndDeduplicated(t *testing.T) {
	const owner int32 = 31
	root := t.TempDir()
	writer := fixtureJournal(root)
	meta := receiptFixtureMeta()
	meta.Mode, meta.BrowserID, meta.LaunchID = ModeBrowser, "owned-test-browser", strings.ToUpper(meta.LaunchID)
	a, b := receiptFixtureEvent(1, fixtureTime), receiptFixtureEvent(2, fixtureTime)
	a.EventID = strings.ToUpper(a.EventID)
	first := receiptFixtureBatch(101, a, b)
	first.BatchID = strings.ToUpper(first.BatchID)
	fixtureAppend(t, writer, owner, meta, first)

	const later = "2026-12-03T11:25:00.000Z"
	writer.now = func() time.Time { instant, _ := time.Parse(time.RFC3339Nano, later); return instant }
	dedup := receiptFixtureBatch(102, b, a)
	fixtureAppend(t, writer, owner, meta, dedup)

	path, err := writer.path(false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	cold := NewReceiptJournal(root, ReceiptOptions{Enabled: true})
	wantScope := ReceiptProofScope{owner, ModeBrowser,
		"904b3a11f0289d4956929c87350f770e47b21e6250f5bbe0910c0f55cfea3ab3",
		"7b40246b55fda742ce0987b0e5280f0f4b1a6cdbd811a2eeebce2e8dc71a3335",
		fixtureLaunch}
	for _, tc := range []struct {
		name  string
		batch ReceiptBatch
		want  Receipt
		ids   []string
	}{
		{"new", first, Receipt{receiptFixtureID(101), 2, 2, receiveTime}, []string{receiptFixtureID(1), receiptFixtureID(2)}},
		{"all-dedup-reversed", dedup, Receipt{receiptFixtureID(102), 2, 0, later}, []string{receiptFixtureID(2), receiptFixtureID(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cold.LookupReceiptProof(context.Background(), owner, meta, tc.batch)
			if err != nil || got.Receipt != tc.want || got.Scope != wantScope || !slices.Equal(got.AcceptedEventIDs, tc.ids) {
				t.Fatalf("proof=%+v err=%v; want receipt=%+v scope=%+v ids=%v", got, err, tc.want, wantScope, tc.ids)
			}
			t.Logf("cold lookup: %+v; actual ordered IDs=%v", got.Receipt, got.AcceptedEventIDs)
		})
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("lookup changed journal bytes (read error=%v)", err)
	}
	if files, err := os.ReadDir(filepath.Dir(path)); err != nil || len(files) != 1 || files[0].Name() != receiptFilename {
		t.Fatalf("lookup left sidecars: files=%v err=%v", files, err)
	}
}

func TestReceiptJournalLookupReceiptProofRejectsWrongInput(t *testing.T) {
	const owner int32 = 31
	j := fixtureJournal(t.TempDir())
	meta := receiptFixtureMeta()
	browser := meta
	browser.Mode, browser.BrowserID = ModeBrowser, "owned-test-browser"
	batch := receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime), receiptFixtureEvent(2, fixtureTime))
	fixtureAppend(t, j, owner, meta, batch)
	fixtureAppend(t, j, owner, browser, batch)
	for _, tc := range []struct {
		name   string
		scope  Session
		mutate func(*int32, *Session, *ReceiptBatch)
		want   error
	}{
		{"owner", meta, func(o *int32, _ *Session, _ *ReceiptBatch) { *o = 32 }, sql.ErrNoRows},
		{"mode", meta, func(_ *int32, m *Session, _ *ReceiptBatch) { m.Mode = ModeExam }, sql.ErrNoRows},
		{"device", meta, func(_ *int32, m *Session, _ *ReceiptBatch) { m.DeviceID = "other-owned-device" }, sql.ErrNoRows},
		{"browser", browser, func(_ *int32, m *Session, _ *ReceiptBatch) { m.BrowserID = "other-owned-browser" }, sql.ErrNoRows},
		{"launch", meta, func(_ *int32, m *Session, _ *ReceiptBatch) { m.LaunchID = receiptFixtureID(400) }, sql.ErrNoRows},
		{"version", meta, func(_ *int32, m *Session, _ *ReceiptBatch) { m.Version = "1.2.3" }, ErrReceiptConflict},
		{"order", meta, func(_ *int32, _ *Session, b *ReceiptBatch) { b.Entries[0], b.Entries[1] = b.Entries[1], b.Entries[0] }, ErrReceiptConflict},
		{"content", meta, func(_ *int32, _ *Session, b *ReceiptBatch) { b.Entries[1].Level = "warn" }, ErrReceiptConflict},
		{"batch", meta, func(_ *int32, _ *Session, b *ReceiptBatch) { b.BatchID = receiptFixtureID(999) }, sql.ErrNoRows},
		{"invalid-owner", meta, func(o *int32, _ *Session, _ *ReceiptBatch) { *o = 0 }, ErrBatch},
		{"invalid-input", meta, func(_ *int32, _ *Session, b *ReceiptBatch) { b.Entries = nil }, ErrBatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, m, b := owner, tc.scope, batch
			b.Entries = slices.Clone(b.Entries)
			tc.mutate(&o, &m, &b)
			assertNoReceiptProof(t, j, o, m, b, tc.want)
		})
	}
}

func TestReceiptJournalLookupReceiptProofRejectsDamagedRows(t *testing.T) {
	const owner int32 = 31
	const firstBatch, laterBatch = 101, 102
	for _, tc := range []struct{ name, stmt string }{
		{"missing-event", "DELETE FROM receipt_events WHERE event_id=?"},
		{"event-id", "UPDATE receipt_events SET event_id='wrong' WHERE event_id=?"},
		{"event-seq", "UPDATE receipt_events SET event_seq=42 WHERE event_id=?"},
		{"event-digest", "UPDATE receipt_events SET digest=zeroblob(32) WHERE event_id=?"},
		{"event-scope", "UPDATE receipt_events SET owner=32 WHERE event_id=?"},
		{"event-level", "UPDATE receipt_events SET level='warn' WHERE event_id=?"},
		{"event-version", "UPDATE receipt_events SET version='1.2.3' WHERE event_id=?"},
		{"event-null-instead-of-zero", "UPDATE receipt_events SET count=NULL WHERE event_id=?"},
		{"event-later-origin", "UPDATE receipt_events SET segment_id=(SELECT max(id) FROM receipt_segments) WHERE event_id=?"},
		{"segment-stored", "UPDATE receipt_segments SET stored=1 WHERE id=(SELECT segment_id FROM receipt_events WHERE event_id=?)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixtureJournal(t.TempDir())
			meta := receiptFixtureMeta()
			a, b := receiptFixtureEvent(1, fixtureTime), receiptFixtureEvent(2, fixtureTime)
			zero := int64(0)
			b.Count = &zero
			batch := receiptFixtureBatch(firstBatch, a, b)
			fixtureAppend(t, j, owner, meta, batch)
			fixtureAppend(t, j, owner, meta, receiptFixtureBatch(laterBatch, a, b))
			db, err := j.open(false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(tc.stmt, b.EventID); err != nil {
				t.Fatal(err)
			}
			assertNoReceiptProof(t, j, owner, meta, batch, ErrReceiptConflict)
		})
	}
}

func TestReceiptJournalLookupReceiptProofRejectsUncommittedSegment(t *testing.T) {
	ctx := context.Background()
	j := fixtureJournal(t.TempDir())
	meta := receiptFixtureMeta()
	event := receiptFixtureEvent(1, fixtureTime)
	committed, pending := receiptFixtureBatch(101, event), receiptFixtureBatch(102, event)
	fixtureAppend(t, j, 31, meta, committed)
	b, err := receiptBatch(meta, pending)
	if err != nil {
		t.Fatal(err)
	}
	db, err := j.open(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	insert, err := tx.ExecContext(ctx, `INSERT INTO receipt_segments (owner,mode,device,browser,launch,batch_id,digest,received_ms,accepted,stored)
		SELECT owner,mode,device,browser,launch,?,?,received_ms,1,0 FROM receipt_segments WHERE batch_id=?`, b.id, b.digest[:], committed.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := insert.RowsAffected(); n != 1 || err != nil {
		t.Fatalf("seed uncommitted row: affected=%d err=%v", n, err)
	}
	assertNoReceiptProof(t, j, 31, meta, pending, sql.ErrNoRows)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertNoReceiptProof(t, j, 31, meta, pending, sql.ErrNoRows)
}

func TestReceiptJournalLookupReceiptProofDoesNotCreate(t *testing.T) {
	root := t.TempDir()
	meta, batch := receiptFixtureMeta(), receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime))
	off := NewReceiptJournal(root, ReceiptOptions{})
	assertNoReceiptProof(t, off, 31, meta, batch, ErrDisabled)
	assertNoReceiptProof(t, fixtureJournal(""), 31, meta, batch, ErrReceiptPath)
	j := fixtureJournal(root)
	assertNoReceiptProof(t, j, 31, meta, batch, sql.ErrNoRows)
	if files, err := os.ReadDir(root); err != nil || len(files) != 0 {
		t.Fatalf("disabled or absent lookup created storage: files=%v err=%v", files, err)
	}
	dir := filepath.Join(root, "client", receiptFolder)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	assertNoReceiptProof(t, j, 31, meta, batch, sql.ErrNoRows)
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatalf("lookup created database or sidecars: files=%v err=%v", files, err)
	}
}
