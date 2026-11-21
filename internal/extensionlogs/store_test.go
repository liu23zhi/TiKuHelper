package extensionlogs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const launchA = "11111111-1111-4111-8111-111111111111"
const launchB = "22222222-2222-4222-8222-222222222222"

func testSession(d, id string) Session { return Session{Version: "1.2.3", DeviceID: d, LaunchID: id} }
func testBatch(s Session, t time.Time, event string) Batch {
	return Batch{Session: s, Entries: []Entry{{Time: t, Level: "info", Event: event}}}
}
func TestFileStoreSessionsPolicyPagingAndExpiry(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	p, e := st.Policy()
	if e != nil || p != (Policy{true, 7, 10}) {
		t.Fatalf("bad defaults %+v %v", p, e)
	}
	a, b := testSession("device-fixture", launchA), testSession("device-fixture", launchB)
	if e = st.Append(17, testBatch(a, now, "settings.saved")); e != ErrLaunch {
		t.Fatal(e)
	}
	start := func(user int32, m Session, want bool) {
		t.Helper()
		created, e := st.Start(user, m)
		if e != nil || created != want {
			t.Fatalf("start: %t %v", created, e)
		}
	}
	start(17, a, true)
	start(17, a, false)
	start(17, b, true)
	start(21, a, true)
	other := testSession("historic:device.02", launchA)
	start(17, other, true)
	if e = st.Append(17, testBatch(a, now, "settings.saved")); e != nil {
		t.Fatal(e)
	}
	if e = st.Append(17, testBatch(b, now, "network.offline")); e != nil {
		t.Fatal(e)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 4 {
		t.Fatalf("expected four separate user/device/launch files, got %d", len(files))
	}
	page, e := st.Read("", 2, Filter{})
	if e != nil || len(page.Entries) != 2 || page.Entries[0].Event != "network.offline" || page.Entries[1].Event != "settings.saved" || page.Next == "" {
		t.Fatalf("recent across interleaved launches: %+v %v", page, e)
	}
	next, e := st.Read(page.Next, 2, Filter{})
	if e != nil || len(next.Entries) != 2 || next.Entries[0].DeviceID != other.DeviceID {
		t.Fatalf("page 2: %+v %v", next, e)
	}
	// Cold reopen: a duplicate signal cannot rotate or truncate; an active launch continues the same file.
	st = New(dir)
	st.now = func() time.Time { return now }
	start(17, a, false)
	if e = st.Append(17, testBatch(a, now, "sync.success")); e != nil {
		t.Fatal(e)
	}
	page, e = st.Read("", 10, Filter{17, a.DeviceID, a.LaunchID})
	if e != nil || len(page.Entries) != 3 || page.Entries[0].Event != "sync.success" || page.Entries[2].Event != "session.start" {
		t.Fatalf("specific launch after restart: %+v %v", page, e)
	}
	if _, e = st.Read("invalid", 10, Filter{}); e != ErrCursor {
		t.Fatal(e)
	}
	if _, e = st.Read("", 10, Filter{DeviceID: "../bad"}); e != ErrCursor {
		t.Fatal(e)
	}
	if e = st.SetPolicy(Policy{false, 1, 2}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.Start(17, testSession("would-not-open", launchB)); e != ErrDisabled {
		t.Fatal(e)
	}
	if e = st.Append(17, testBatch(b, now, "sync.success")); e != ErrDisabled {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "unrelated.log"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	st = New(dir)
	st.now = func() time.Time { return now }
	if q, e := st.Policy(); e != nil || q != (Policy{false, 1, 2}) {
		t.Fatal(q, e)
	}
	now = now.Add(25 * time.Hour)
	if e = st.Sweep(); e != nil {
		t.Fatal(e)
	}
	if lst, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(lst) != 0 {
		t.Fatal("expired log retained")
	}
	if _, e = os.Stat(filepath.Join(dir, "unrelated.log")); e != nil {
		t.Fatal(e)
	}
}
func TestCapacityAndUnexpectedInput(t *testing.T) {
	st := New(t.TempDir())
	st.unitBytes = 1900
	st.segmentBytes = 500
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	if e := st.SetPolicy(Policy{true, 7, 1}); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 12; i++ {
		now = now.Add(time.Minute)
		s := testSession("fixture", launchA)
		if _, e := st.Start(int32(i), s); e != nil {
			t.Fatal(e)
		}
		if e := st.Append(int32(i), testBatch(s, now, "settings.saved")); e != nil {
			t.Fatal(e)
		}
	}
	var bytes int64
	fs, _ := filepath.Glob(filepath.Join(st.dir, "*.jsonl"))
	for _, f := range fs {
		info, e := os.Stat(f)
		if e != nil {
			t.Fatal(e)
		}
		bytes += info.Size()
	}
	if bytes > 1900 {
		t.Fatalf("capacity exceeded %d", bytes)
	}
	page, e := st.Read("", 100, Filter{})
	if e != nil || len(page.Entries) == 0 || len(page.Entries) >= 24 || page.Entries[0].UserID != 12 {
		t.Fatalf("eviction %d %v", len(page.Entries), e)
	}
	for _, s := range []Session{testSession("../bad", launchA), testSession("abc", "not-a-launch"), {Version: "invalid-version", DeviceID: "abc", LaunchID: launchA}} {
		if _, e = st.Start(9, s); e != ErrBatch {
			t.Fatalf("invalid session accepted")
		}
	}
	if e = st.Append(12, testBatch(testSession("fixture", launchA), now, "arbitrary.message")); e != ErrBatch {
		t.Fatal(e)
	}
}
func TestStableCursorWhenASecondLaunchAppends(t *testing.T) {
	st := New(t.TempDir())
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	a, b := testSession("fixture", launchA), testSession("fixture", launchB)
	_, _ = st.Start(12, a)
	_, _ = st.Start(12, b)
	const total = 850
	for base := 0; base < total; base += 50 {
		m := a
		if base%100 != 0 {
			m = b
		}
		batch := Batch{Session: m}
		for j := base; j < base+50; j++ {
			v := int64(j)
			batch.Entries = append(batch.Entries, Entry{Time: now, Event: "sync.success", Level: "info", Count: &v})
		}
		if e := st.Append(12, batch); e != nil {
			t.Fatal(e)
		}
	}
	expected := total - 1
	cursor := ""
	starts := 0
	for {
		page, e := st.Read(cursor, 53, Filter{UserID: 12, DeviceID: "fixture"})
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range page.Entries {
			if r.Event == "session.start" {
				starts++
				continue
			}
			if r.Count == nil || int(*r.Count) != expected {
				t.Fatalf("sequence gap near %d", expected)
			}
			expected--
		}
		if cursor == "" {
			if e = st.Append(12, testBatch(a, now, "settings.saved")); e != nil {
				t.Fatal(e)
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if expected != -1 || starts != 2 {
		t.Fatal(expected, starts)
	}
}
