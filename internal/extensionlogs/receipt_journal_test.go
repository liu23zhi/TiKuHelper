package extensionlogs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

const (
	fixtureLaunch = "96e2b690-3319-425b-92d1-2350af303700"
	fixtureTime   = "2026-11-27T03:02:00.123Z"
	receiveTime   = "2026-12-02T11:25:00.000Z"
)

func receiptFixtureMeta() Session {
	return Session{Mode: ModeExtension, Version: "dev", DeviceID: "owned-test-device", LaunchID: fixtureLaunch}
}
func receiptFixtureID(n int64) string {
	return fmt.Sprintf("e5573627-ccca-4e93-89d0-%012x", n)
}
func receiptFixtureEvent(n int64, when string) ReceiptEvent {
	return ReceiptEvent{Time: when, Level: "info", Event: "sync.success", EventID: receiptFixtureID(n), EventSeq: n}
}
func receiptFixtureBatch(n int64, entries ...ReceiptEvent) ReceiptBatch {
	return ReceiptBatch{BatchID: receiptFixtureID(n), Entries: entries}
}
func fixtureJournal(root string) *ReceiptJournal {
	j := NewReceiptJournal(root, ReceiptOptions{Enabled: true})
	j.now = func() time.Time { t, _ := time.Parse(time.RFC3339Nano, receiveTime); return t }
	return j
}
func fixtureAppend(t *testing.T, j *ReceiptJournal, owner int32, meta Session, b ReceiptBatch) Receipt {
	t.Helper()
	r, err := j.Append(context.Background(), owner, meta, b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func fixturePages(t *testing.T, j *ReceiptJournal, f Filter, n int) []ReceiptRecord {
	t.Helper()
	var rows []ReceiptRecord
	var c *ReceiptCursor
	for i := 0; i < 30; i++ {
		page, err := j.Read(context.Background(), f, n, c)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, page.Entries...)
		if page.Next == nil {
			return rows
		}
		c = page.Next
	}
	t.Fatal("unbounded or repeating cursor")
	return nil
}

func TestReceiptJournalDefaultOffAndInvalidEventDoesNotCreate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	j := NewReceiptJournal(root, ReceiptOptions{})
	good := receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime))
	if _, err := j.Append(ctx, 1, receiptFixtureMeta(), good); !errors.Is(err, ErrDisabled) {
		t.Fatalf("default must be off: %v", err)
	}
	if _, err := j.Read(ctx, Filter{}, 2, nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("default read must be off: %v", err)
	}
	j = fixtureJournal(root)
	if rows := fixturePages(t, j, Filter{}, 2); len(rows) != 0 {
		t.Fatal("an empty journal cannot contain records")
	}
	for _, badTime := range []string{"", "2026-11-27T03:02:00Z", "2026-11-27T11:02:00.123+08:00", "2026-02-30T03:02:00.123Z", "2026-11-27T03:02:00.1234Z"} {
		if _, err := j.Append(ctx, 1, receiptFixtureMeta(), receiptFixtureBatch(101, receiptFixtureEvent(1, badTime))); !errors.Is(err, ErrBatch) {
			t.Fatalf("invalid/noncanonical time accepted: %v", err)
		}
	}
	for _, bad := range []ReceiptEvent{
		{EventID: "arbitrary-id", EventSeq: 1, Time: fixtureTime, Level: "info", Event: "sync.success"},
		{EventID: receiptFixtureID(1), EventSeq: 0, Time: fixtureTime, Level: "info", Event: "sync.success"},
		{EventID: receiptFixtureID(1), EventSeq: 1, Time: fixtureTime, Level: "info", Event: "not.declared"},
	} {
		if _, err := j.Append(ctx, 1, receiptFixtureMeta(), receiptFixtureBatch(101, bad)); !errors.Is(err, ErrBatch) {
			t.Fatalf("unallowlisted event accepted: %v", err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("disabled, read-only or invalid input created a journal")
	}
}

func TestReceiptJournalLostReceiptSurvivesProcessExit(t *testing.T) {
	const worker = "OWNED_T08_RECEIPT_WORKER"
	if owned := os.Getenv(worker); owned != "" {
		cwd, err := os.Getwd()
		if err != nil || cwd != owned {
			t.Fatal("worker must operate only in its owned directory")
		}
		// Intentionally discard the receipt before returning to the parent.
		fixtureAppend(t, fixtureJournal(owned), 1, receiptFixtureMeta(), receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime)))
		return
	}
	root := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-test.run=^TestReceiptJournalLostReceiptSurvivesProcessExit$")
	cmd.Dir = root
	cmd.Env = []string{worker + "=" + root, "PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("owned process: %v (%s)", err, out)
	}
	reopened := NewReceiptJournal(root, ReceiptOptions{Enabled: true})
	first := receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime))
	r := fixtureAppend(t, reopened, 1, receiptFixtureMeta(), first)
	if r.BatchID != first.BatchID || r.Stored != 1 || r.Accepted != 1 || r.ReceivedAt != receiveTime {
		t.Fatalf("lost acknowledgement changed: %+v", r)
	}
	b := fixtureAppend(t, reopened, 1, receiptFixtureMeta(), receiptFixtureBatch(102, first.Entries...))
	if b.Stored != 0 || b.Accepted != 1 {
		t.Fatalf("cross-batch identical event written again: %+v", b)
	}
	rows := fixturePages(t, reopened, Filter{}, 1)
	if len(rows) != 1 || rows[0].Time != fixtureTime || rows[0].ReceivedAt != receiveTime {
		t.Fatal("process restart or duplicate changed row contents")
	}
	t.Log("lost-receipt replay: identical original receipt after independent process, one durable event")
}

func TestReceiptJournalConflictsAndRollbackDoNotOverwrite(t *testing.T) {
	ctx := context.Background()
	j := fixtureJournal(t.TempDir())
	meta := receiptFixtureMeta()
	one := receiptFixtureEvent(1, fixtureTime)
	original := fixtureAppend(t, j, 1, meta, receiptFixtureBatch(101, one))
	two := receiptFixtureEvent(2, fixtureTime)
	otherSeqOne := receiptFixtureEvent(3, fixtureTime)
	otherSeqOne.EventSeq = 1
	sameIDDifferentEvent := one
	sameIDDifferentEvent.Event = "runtime.error"
	for _, batch := range []ReceiptBatch{
		receiptFixtureBatch(101, two),
		receiptFixtureBatch(102, two, otherSeqOne),
		receiptFixtureBatch(103, two, sameIDDifferentEvent),
	} {
		if _, err := j.Append(ctx, 1, meta, batch); !errors.Is(err, ErrReceiptConflict) {
			t.Fatalf("wrong conflict outcome: %v", err)
		}
	}
	db, err := j.open(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER fixture_event_failure BEFORE INSERT ON receipt_events WHEN NEW.event_seq = 3 BEGIN SELECT RAISE(ABORT, 'owned fixture interruption'); END"); err != nil {
		t.Fatal(err)
	}
	retry := receiptFixtureBatch(104, two, receiptFixtureEvent(3, fixtureTime))
	if _, err := j.Append(ctx, 1, meta, retry); err == nil {
		t.Fatal("injected write failure incorrectly acknowledged")
	}
	if rows := fixturePages(t, j, Filter{}, 1); len(rows) != 1 || rows[0].EventID != one.EventID {
		t.Fatal("conflict or failed transaction partially wrote or overwrote an event")
	}
	if _, err := db.Exec("DROP TRIGGER fixture_event_failure"); err != nil {
		t.Fatal(err)
	}
	if r := fixtureAppend(t, j, 1, meta, retry); r.Accepted != 2 || r.Stored != 2 {
		t.Fatal("failure left a partial receipt or event")
	}
	if got := fixtureAppend(t, j, 1, meta, receiptFixtureBatch(101, one)); got != original {
		t.Fatal("conflict overwrote the original receipt")
	}
}

func TestReceiptJournalCapacityAndConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	opts := ReceiptOptions{Enabled: true, MaxEvents: 1, MaxBatches: 1}
	b := receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime))
	results := make(chan Receipt, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := NewReceiptJournal(root, opts)
			r, err := j.Append(ctx, 1, receiptFixtureMeta(), b)
			if err != nil {
				failures <- err
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if first, second := <-results, <-results; first != second || first.Stored != 1 {
		t.Fatal("concurrent same-id append did not return one persistent acknowledgement")
	}
	j := NewReceiptJournal(root, opts)
	if _, err := j.Append(ctx, 1, receiptFixtureMeta(), receiptFixtureBatch(102, b.Entries...)); !errors.Is(err, ErrReceiptCapacity) {
		t.Fatalf("no new receipts at capacity, even duplicate-only: %v", err)
	}
	if rows := fixturePages(t, j, Filter{}, 2); len(rows) != 1 {
		t.Fatal("capacity enforcement evicted existing events")
	}
	// With receipt slots free but no event slots, a mixed batch is all-or-none.
	j = NewReceiptJournal(root, ReceiptOptions{Enabled: true, MaxEvents: 1, MaxBatches: 3})
	if _, err := j.Append(ctx, 1, receiptFixtureMeta(), receiptFixtureBatch(103, b.Entries[0], receiptFixtureEvent(2, fixtureTime))); !errors.Is(err, ErrReceiptCapacity) {
		t.Fatalf("unique event cap was not enforced: %v", err)
	}
	fixtureAppend(t, j, 1, receiptFixtureMeta(), b)
}

func TestReceiptJournalOwnedCorpusAndStableSnapshotPages(t *testing.T) {
	type stream struct {
		Name, Mode, Device, Browser, Launch string
		Owner                               int32
	}
	type event struct {
		Name, Stream, Batch, ID, Event, Time string
		Seq                                  int64
	}
	var corpus struct {
		Streams    []stream
		Events     []event
		Deliveries []string
		Expected   []string `json:"intended_event_order"`
		Replay     string   `json:"after_reopen_replay"`
	}
	raw, err := os.ReadFile("testdata/receipt-journal-owned-corpus.json")
	if err != nil || json.Unmarshal(raw, &corpus) != nil {
		t.Fatalf("read owned corpus: %v", err)
	}
	lookup, streams := map[string]event{}, map[string]stream{}
	for _, st := range corpus.Streams {
		streams[st.Name] = st
	}
	for _, ev := range corpus.Events {
		lookup[ev.Name] = ev
	}
	root := t.TempDir()
	j := fixtureJournal(root)
	appendOwned := func(ev event) Receipt {
		st := streams[ev.Stream]
		// Logical fixture identifiers aren't a proposed wire syntax; map them
		// consistently to UUIDs and normalize b1's offset before v2 delivery.
		instant, err := time.Parse(time.RFC3339Nano, ev.Time)
		if err != nil {
			t.Fatal(err)
		}
		item := receiptFixtureEvent(ev.Seq, instant.UTC().Format(receiptTimeFormat))
		item.Event = ev.Event
		return fixtureAppend(t, j, st.Owner, Session{Mode: st.Mode, Version: "dev", DeviceID: st.Device, BrowserID: st.Browser, LaunchID: st.Launch},
			receiptFixtureBatch(100+ev.Seq, item))
	}
	for _, name := range corpus.Deliveries {
		if r := appendOwned(lookup[name]); r.Stored != 1 {
			t.Fatal("same literal IDs in independent streams were merged")
		}
	}
	j = fixtureJournal(root) // new store/connection; not an in-memory index
	if r := appendOwned(lookup[corpus.Replay]); r.Stored != 1 {
		t.Fatal("original receipt must be replayed, not recomputed")
	}
	ctx := context.Background()
	first, err := j.Read(ctx, Filter{}, 2, nil)
	if err != nil || first.Next == nil {
		t.Fatal("first page not captured")
	}
	meta := receiptFixtureMeta()
	fixtureAppend(t, j, 7, meta, receiptFixtureBatch(900, receiptFixtureEvent(900, "2026-11-26T01:00:00.000Z")))
	rows, c := first.Entries, first.Next
	for c != nil {
		page, err := j.Read(ctx, Filter{}, 2, c)
		if err != nil {
			t.Fatal(err)
		}
		rows, c = append(rows, page.Entries...), page.Next
	}
	var names []string
	for _, row := range rows {
		for _, candidate := range corpus.Events {
			st := streams[candidate.Stream]
			if row.OwnerID == st.Owner && row.Mode == st.Mode && row.EventSeq == candidate.Seq {
				names = append(names, candidate.Name)
				if row.ClockRegressed != (candidate.Name == "e2") {
					t.Fatalf("arrival time was confused with clock regression for %s", candidate.Name)
				}
			}
		}
	}
	if !reflect.DeepEqual(names, corpus.Expected) {
		t.Fatalf("sorted across logical segments then paginated: got %v want %v", names, corpus.Expected)
	}
	if _, err := j.Read(ctx, Filter{UserID: 41002}, 2, first.Next); !errors.Is(err, ErrCursor) {
		t.Fatal("cursor was reusable with a different scope")
	}
	filtered := fixturePages(t, j, Filter{UserID: 41002, Mode: ModeExam}, 1)
	if len(filtered) != 1 || filtered[0].Mode != ModeExam || filtered[0].BrowserKey != "" {
		t.Fatal("filter crossed scope")
	}
	if rows := fixturePages(t, j, Filter{}, 2); len(rows) != 7 || rows[0].EventSeq != 900 {
		t.Fatal("new snapshot does not include delayed event")
	}
	t.Logf("3 owned modes: paged merge=%v; late older row excluded from existing snapshot", names)
}

func TestReceiptJournalDoesNotTouchLegacyOrPersistRawDevice(t *testing.T) {
	root := t.TempDir()
	meta := receiptFixtureMeta()
	legacy := New(root)
	if _, err := legacy.Start(1, meta); err != nil {
		t.Fatal(err)
	}
	when, _ := time.Parse(time.RFC3339Nano, fixtureTime)
	if err := legacy.Append(1, Batch{Session: meta, Entries: []Entry{{Time: when, Level: "info", Event: "sync.success"}}}); err != nil {
		t.Fatal(err)
	}
	j := fixtureJournal(root)
	fixtureAppend(t, j, 1, meta, receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime)))
	p, err := legacy.Read("", 3, Filter{})
	if err != nil || len(p.Entries) != 1 {
		t.Fatal("legacy store must not read journal destination")
	}
	path, err := j.path(false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || bytes.Contains(before, []byte(meta.DeviceID)) {
		t.Fatal("raw device was durable")
	}
	legacy.now = func() time.Time { return time.Now().AddDate(0, 0, 30) }
	if err := legacy.Sweep(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || len(fixturePages(t, j, Filter{DeviceID: meta.DeviceID}, 1)) != 1 {
		t.Fatal("v1 retention touched journal or v2 filter lost the scope")
	}
}

func TestReceiptJournalRefusesLinkedDestinationOrUnrelatedDatabase(t *testing.T) {
	ctx := context.Background()
	root, other := t.TempDir(), t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, "client")); err != nil {
		t.Skipf("symlink fixture unsupported: %v", err)
	}
	b := receiptFixtureBatch(101, receiptFixtureEvent(1, fixtureTime))
	j := fixtureJournal(root)
	if _, err := j.Append(ctx, 1, receiptFixtureMeta(), b); !errors.Is(err, ErrReceiptPath) {
		t.Fatalf("linked path not refused: %v", err)
	}
	if files, err := os.ReadDir(other); err != nil || len(files) != 0 {
		t.Fatal("journal wrote through a linked path")
	}

	j = fixtureJournal(t.TempDir())
	db, err := j.open(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated (fixture_only INTEGER)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if _, err := j.Append(ctx, 1, receiptFixtureMeta(), b); !errors.Is(err, ErrReceiptPath) {
		t.Fatalf("journal adopted a database it did not initialize: %v", err)
	}
}

func TestReceiptJournalScopeAxesAndOptionalNumbers(t *testing.T) {
	j := fixtureJournal(t.TempDir())
	base := receiptFixtureMeta()
	rows := []struct {
		owner int32
		meta  Session
	}{
		{1, base},
		{2, base},
		{1, Session{Mode: base.Mode, Version: base.Version, DeviceID: "Owned-test-device", LaunchID: base.LaunchID}},
		{1, Session{Mode: base.Mode, Version: base.Version, DeviceID: base.DeviceID, LaunchID: receiptFixtureID(901)}},
		{1, Session{Mode: ModeBrowser, BrowserID: "owned-browser-a", Version: base.Version, DeviceID: base.DeviceID, LaunchID: base.LaunchID}},
		{1, Session{Mode: ModeBrowser, BrowserID: "owned-browser-b", Version: base.Version, DeviceID: base.DeviceID, LaunchID: base.LaunchID}},
		{1, Session{Mode: ModeExam, Version: base.Version, DeviceID: base.DeviceID, LaunchID: base.LaunchID}},
	}
	entry := receiptFixtureEvent(1, fixtureTime)
	dur, count := int64(8), int64(0)
	entry.DurationMS, entry.Count = &dur, &count
	b := receiptFixtureBatch(101, entry)
	for _, row := range rows {
		if r := fixtureAppend(t, j, row.owner, row.meta, b); r.Stored != 1 {
			t.Fatal("distinct axes must not fold a matching batch/event identifier")
		}
		got := fixturePages(t, j, Filter{UserID: row.owner, Mode: row.meta.Mode, DeviceID: row.meta.DeviceID, BrowserID: row.meta.BrowserID, LaunchID: row.meta.LaunchID}, 1)
		if len(got) != 1 || got[0].DurationMS == nil || *got[0].DurationMS != 8 || got[0].Count == nil || *got[0].Count != 0 {
			t.Fatal("scope or optional numeric fields did not round-trip")
		}
	}
	if got := fixturePages(t, j, Filter{}, 2); len(got) != len(rows) {
		t.Fatal("unexpected number of distinct event scopes")
	}
}
