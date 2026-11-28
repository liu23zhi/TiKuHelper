package extensionlogs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func legacyOwnedRecord(t *testing.T, s *Store, owner int32, meta Session, event string, when time.Time) {
	t.Helper()
	if _, err := s.Start(owner, meta); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(owner, Batch{Session: meta, Entries: []Entry{{Time: when, Level: "info", Event: event}}}); err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedOwnedSourcesSortAcrossPagesByEventTime(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	now := time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	meta := receiptFixtureMeta()
	const owner = 19
	legacyOwnedRecord(t, s, owner, meta, "runtime.error", now.Add(-time.Hour))
	legacyOwnedRecord(t, s, owner, meta, "network.offline", now.Add(-3*time.Hour))
	newEvent := receiptFixtureEvent(1, "2026-01-02T14:00:00.000Z")
	newEvent.Event = "sync.success"
	if _, err := s.AppendReceipt(ctx, owner, meta, receiptFixtureBatch(410, newEvent)); err != nil {
		t.Fatal(err)
	}
	var got []string
	cursor := ""
	for i := 0; i < 5; i++ {
		rows, next, err := s.ReadReceipts(ctx, Filter{UserID: owner, DeviceID: meta.DeviceID}, 1, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			got = append(got, row.Event)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	want := []string{"network.offline", "sync.success", "runtime.error"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unified paged events=%v; want %v", got, want)
	}
}

func TestUnifiedCleanupRemovesExpiredRowsFromPartiallyLiveLegacySegment(t *testing.T) {
	s := New(t.TempDir())
	start := time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC)
	now := start
	s.now = func() time.Time { return now }
	meta := receiptFixtureMeta()
	legacyOwnedRecord(t, s, 21, meta, "settings.saved", start.Add(-time.Hour))
	now = start.Add(30 * time.Minute)
	legacyOwnedRecord(t, s, 21, meta, "sync.success", start.Add(-2*time.Hour))
	if len(s.files) != 1 {
		t.Fatal("fixture should contain one mixed-age segment")
	}
	var path string
	for k := range s.files {
		path = filepath.Join(s.dir, k)
	}
	now = start.Add(7 * 24 * time.Hour)
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	f, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f), "settings.saved") || !strings.Contains(string(f), "sync.success") {
		t.Fatalf("old record still on disk in live segment: %d bytes", len(f))
	}
	rows, _, err := s.ReadReceipts(context.Background(), Filter{UserID: 21}, 10, "")
	if err != nil || len(rows) != 1 || rows[0].Event != "sync.success" {
		t.Fatalf("invalid retained selection count=%d err=%v", len(rows), err)
	}
}

func TestUnifiedSnapshotExportUsesSameMixedOrder(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	now := time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	meta := receiptFixtureMeta()
	const owner = 31
	legacyOwnedRecord(t, s, owner, meta, "sync.failed", time.Date(2026, 1, 2, 14, 0, 0, 0, time.UTC))
	legacyOwnedRecord(t, s, owner, meta, "settings.saved", time.Date(2026, 1, 2, 12, 0, 0, 777, time.UTC))
	if _, err := s.AppendReceipt(ctx, owner, meta, receiptFixtureBatch(41, receiptFixtureEvent(1, "2026-01-02T14:00:00.000Z"))); err != nil {
		t.Fatal(err)
	}
	f := Filter{UserID: owner}
	first, err := s.ReadUnified(ctx, f, 1, "")
	if err != nil || first.Next == "" || first.Snapshot == "" {
		t.Fatal(err, first)
	}
	late := receiptFixtureEvent(2, "2026-01-02T10:00:00.000Z")
	late.Event = "extension.ready"
	if _, err = s.AppendReceipt(ctx, owner, meta, receiptFixtureBatch(42, late)); err != nil {
		t.Fatal(err)
	}
	paged := append([]DiagnosticReceiptRow(nil), first.Entries...)
	for cursor := first.Next; cursor != ""; {
		p, e := s.ReadUnified(ctx, f, 1, cursor)
		if e != nil {
			t.Fatal(e)
		}
		if len(paged) >= 3 {
			t.Fatal("cursor failed to end")
		}
		paged = append(paged, p.Entries...)
		cursor = p.Next
	}
	exported, err := s.ExportReceipts(ctx, f, first.Snapshot)
	if err != nil || len(exported) != 3 || !reflect.DeepEqual(paged, exported) ||
		exported[0].Event != "settings.saved" || exported[1].Event != "sync.failed" || exported[2].Event != "sync.success" {
		t.Fatalf("paged=%v export=%v err=%v", paged, exported, err)
	}
	fresh, err := s.ExportReceipts(ctx, f, "")
	if err != nil || len(fresh) != 4 || fresh[0].Event != "extension.ready" {
		t.Fatalf("fresh rows=%v err=%v", fresh, err)
	}
	if _, err = s.ExportReceipts(ctx, Filter{UserID: 32}, first.Snapshot); !errors.Is(err, ErrCursor) {
		t.Fatal("snapshot crossed filters")
	}
	legacyOwnedRecord(t, s, owner, meta, "network.offline", now)
	if _, err = s.ExportReceipts(ctx, f, first.Snapshot); !errors.Is(err, ErrCursor) {
		t.Fatal("snapshot accepted mutated legacy file")
	}
}

func TestUnifiedPressureKeepsReceiptAfterOlderPayloadRemoved(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	start := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	now := start
	s.now = func() time.Time { return now }
	meta := receiptFixtureMeta()
	const owner = 71
	var first ReceiptBatch
	var original ReceiptProof
	for i := int64(0); i < 12; i++ {
		events := make([]ReceiptEvent, 0, 50)
		for n := int64(1); n <= 50; n++ {
			events = append(events, receiptFixtureEvent(i*50+n, "2026-01-02T10:00:00.000Z"))
		}
		b := receiptFixtureBatch(400+i, events...)
		p, err := s.AppendReceipt(ctx, owner, meta, b)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first, original = b, p
		}
		now = now.Add(time.Second)
	}
	now = start.Add(time.Hour)
	legacyOwnedRecord(t, s, owner, meta, "settings.saved", start.Add(-3*time.Hour))
	j := s.receiptJournal(Policy{true, 7, 10})
	before, err := j.physicalSize()
	if err != nil {
		t.Fatal(err)
	}
	// Force the 10 GB arithmetic through the same path with a scaled-down
	// unit; the new source is older than this preserved legacy row.
	s.unitBytes = (before - 4096) / 10
	if err = s.Sweep(); err != nil {
		t.Fatal(err)
	}
	after, err := j.physicalSize()
	if err != nil {
		t.Fatal(err)
	}
	root, files, _, err := s.legacyFiles()
	if err != nil {
		t.Fatal(err)
	}
	if root != nil {
		root.Close()
	}
	if after+legacyBytes(files) > 10*s.unitBytes {
		t.Fatalf("actual bytes exceeded cap: %d", after+legacyBytes(files))
	}
	rows, err := s.ExportReceipts(ctx, Filter{UserID: owner}, "")
	if err != nil || len(rows) >= 601 || len(rows) < 2 || rows[0].Event != "settings.saved" {
		t.Fatalf("pressure did not keep the newest-received old record: rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.Event != "settings.saved" && r.Seq == 1 {
			t.Fatal("oldest payload was not removed")
		}
	}
	cold := New(s.dir)
	cold.now = s.now
	cold.unitBytes = s.unitBytes
	replay, err := cold.AppendReceipt(ctx, owner, meta, first)
	if err != nil || !reflect.DeepEqual(replay, original) {
		t.Fatalf("proof did not survive cold eviction retry: %v", err)
	}
	dup := receiptFixtureBatch(499, first.Entries...)
	if p, err := cold.AppendReceipt(ctx, owner, meta, dup); err != nil || p.Stored != 0 || p.Accepted != 50 {
		t.Fatalf("retired event identities admitted duplicates: %#v %v", p.Receipt, err)
	}
	bad := receiptFixtureBatch(501, first.Entries[0])
	bad.Entries[0].Event = "network.offline"
	if _, err = cold.AppendReceipt(ctx, owner, meta, bad); !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("retired content conflict was lost: %v", err)
	}
}

func TestUnifiedReadDoesNotRewriteV2AndUpgradePreservesExistingProof(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	clock := time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	m := receiptFixtureMeta()
	b := receiptFixtureBatch(911, receiptFixtureEvent(1, "2026-01-01T00:00:00.000Z"))
	orig, err := s.AppendReceipt(ctx, 77, m, b)
	if err != nil {
		t.Fatal(err)
	}
	j := s.receiptJournal(Policy{true, 7, 10})
	prepare := func(db *sql.DB) {
		t.Helper()
		if _, err = db.Exec("DROP TABLE receipt_retired_events; PRAGMA user_version=2"); err != nil {
			t.Fatal(err)
		}
	}
	db, err := j.open(false)
	if err != nil {
		t.Fatal(err)
	}
	prepare(db)
	db.Close()
	check := func(want int) {
		t.Helper()
		d, e := j.open(false)
		if e != nil {
			t.Fatal(e)
		}
		defer d.Close()
		var got int
		if e = d.QueryRow("PRAGMA user_version").Scan(&got); e != nil || got != want {
			t.Fatalf("schema=%d want=%d %v", got, want, e)
		}
	}
	page, e := s.ReadUnified(ctx, Filter{}, 10, "")
	if e != nil || len(page.Entries) != 1 {
		t.Fatal(e)
	}
	check(2)
	extra := receiptFixtureBatch(912, receiptFixtureEvent(2, "2026-01-01T00:00:01.000Z"))
	if _, e = s.AppendReceipt(ctx, 77, m, extra); e != nil {
		t.Fatal(e)
	}
	check(receiptSchemaVersion)
	if p, e := s.AppendReceipt(ctx, 77, m, b); e != nil || !reflect.DeepEqual(p, orig) {
		t.Fatalf("original proof changed: %v", e)
	}
}
