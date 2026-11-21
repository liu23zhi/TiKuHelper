package extensionlogs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStorePolicyPagingAndExpiry(t *testing.T) {
	root := t.TempDir()
	st := New(root)
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	p, err := st.Policy()
	if err != nil || p != (Policy{7, 10}) {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	b := Batch{Version: "1.2.3", Entries: []Entry{{Time: now, Level: "info", Event: "extension.ready"}, {Time: now, Level: "warn", Event: "network.offline"}}}
	if err = st.Append(17, b); err != nil {
		t.Fatal(err)
	}
	page, err := st.Read("", 1, 0)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Event != "network.offline" || page.Next == "" {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	older, err := st.Read(page.Next, 1, 0)
	if err != nil || len(older.Entries) != 1 || older.Entries[0].UserID != 17 || older.Entries[0].Event != "extension.ready" || older.Next != "" {
		t.Fatalf("page 2: %+v %v", older, err)
	}
	if _, err = st.Read("../x~9", 10, 0); err != ErrCursor {
		t.Fatalf("bad cursor: %v", err)
	}
	if err = os.WriteFile(filepath.Join(root, "unrelated.log"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = st.SetPolicy(Policy{1, 2}); err != nil {
		t.Fatal(err)
	}
	again := New(root)
	if q, err := again.Policy(); err != nil || q != (Policy{1, 2}) {
		t.Fatalf("persisted policy: %+v %v", q, err)
	}
	now = now.Add(25 * time.Hour)
	if err = st.Sweep(); err != nil {
		t.Fatal(err)
	}
	page, err = st.Read("", 30, 0)
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("expired: %+v %v", page, err)
	}
	if _, err = os.Stat(filepath.Join(root, "unrelated.log")); err != nil {
		t.Fatal("removed non-segment")
	}
	if segs, _ := filepath.Glob(filepath.Join(root, "*.jsonl")); len(segs) != 0 {
		t.Fatalf("retained expired segments %v", segs)
	}
}
func TestStorageCapacityEvictsOldestAndRejectsInvalidEvents(t *testing.T) {
	root := t.TempDir()
	st := New(root)
	st.segmentBytes = 160
	st.unitBytes = 600
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	if err := st.SetPolicy(Policy{7, 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		now = now.Add(time.Minute)
		if err := st.Append(int32(i+1), Batch{Version: "3.2.1", Entries: []Entry{{Time: now, Level: "info", Event: "settings.saved"}}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.Read("", 100, 0)
	if err != nil || len(page.Entries) < 1 || len(page.Entries) >= 10 || page.Entries[0].UserID != 10 {
		t.Fatalf("capacity: %+v %v", page, err)
	}
	var sz int64
	segs, _ := filepath.Glob(filepath.Join(root, "*.jsonl"))
	for _, f := range segs {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		sz += fi.Size()
	}
	if sz > 600 {
		t.Fatalf("cap exceeded %d", sz)
	}
	mine, err := st.Read("", 10, 10)
	if err != nil || len(mine.Entries) != 1 || mine.Entries[0].UserID != 10 {
		t.Fatalf("owner filter: %+v %v", mine, err)
	}
	bad := Batch{Version: "https://test.invalid", Entries: []Entry{{Time: now, Level: "debug", Event: "extension.ready"}}}
	if err = st.Append(42, bad); err != ErrBatch {
		t.Fatalf("accepted bad version")
	}
	bad.Version = "1.0"
	bad.Entries[0].Event = strings.Repeat("x", 30)
	if err = st.Append(42, bad); err != ErrBatch {
		t.Fatalf("accepted unknown event")
	}
}
