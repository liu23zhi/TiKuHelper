package extensionlogs

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func diagnosticFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".jsonl" {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestStartOnlyRegistersUntilFirstRealEvent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(dir)
	st.now = func() time.Time { return now }
	s := testSession("fixture-device", launchA)
	if fresh, err := st.Start(17, s); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	if fresh, err := st.Start(17, s); fresh || err != nil {
		t.Fatal(fresh, err)
	}
	if err := st.Append(17, Batch{Session: s}); err != ErrBatch {
		t.Fatal(err)
	}
	if err := st.Append(17, testBatch(s, now, "session.start")); err != ErrBatch {
		t.Fatal(err)
	}
	if files := diagnosticFiles(t, dir); len(files) != 0 {
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("start without any real event created %d file(s); first row: %s", len(files), raw)
	}
	if p, err := st.Read("", 10, Filter{}); err != nil || len(p.Entries) != 0 {
		t.Fatal(p, err)
	}

	// A server restart loses only pending registrations. The same launch can be
	// re-registered, and a subsequent restart finds its durable segment.
	st = New(dir)
	st.now = func() time.Time { return now }
	eventTime := now.Add(-2 * time.Minute)
	b := testBatch(s, eventTime, "extension.ready")
	if err := st.Append(17, b); err != ErrLaunch {
		t.Fatalf("lost pending start must be retried: %v", err)
	}
	if fresh, err := st.Start(17, s); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	if err := st.Append(17, b); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "client", "Browser_extensions", fmt.Sprintf("%019d_u17_mextension_dfixture-device_s%s.jsonl", now.UnixNano(), launchA))
	fileMatches := diagnosticFiles(t, dir)
	if len(fileMatches) != 1 || fileMatches[0] != want {
		t.Fatalf("first batch published at %v, want %s", fileMatches, want)
	}
	raw, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	var first Record
	if err := json.Unmarshal(raw, &first); err != nil || first.Event != "extension.ready" || !first.Time.Equal(eventTime) || !first.ReceivedAt.Equal(now) {
		t.Fatalf("first file must contain just a complete real record with both timestamps, got %q (%v)", raw, err)
	}
	st = New(dir)
	now = now.Add(time.Minute)
	st.now = func() time.Time { return now }
	if fresh, err := st.Start(17, s); fresh || err != nil {
		t.Fatal(fresh, err)
	}
	if err := st.Append(17, testBatch(s, now, "network.offline")); err != nil {
		t.Fatal(err)
	}
	if files := diagnosticFiles(t, dir); len(files) != 1 || files[0] != want {
		t.Fatalf("append after restart did not reuse first file: %v", files)
	}
	if p, err := st.Read("", 10, Filter{UserID: 17, Mode: ModeExtension, DeviceID: s.DeviceID, LaunchID: launchA}); err != nil || len(p.Entries) != 2 || p.Entries[0].Event != "network.offline" || p.Entries[1].Event != "extension.ready" {
		t.Fatal(p, err)
	}
}

func TestPendingRegistrationsExpireWithoutDuplicateRefreshAndDisableClears(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(dir)
	st.now = func() time.Time { return now }
	s := testSession("fixture-device", launchA)
	if fresh, err := st.Start(17, s); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	now = now.Add(59 * time.Minute)
	if fresh, err := st.Start(17, s); fresh || err != nil {
		t.Fatal(fresh, err)
	}
	now = now.Add(time.Minute)
	if err := st.Append(17, testBatch(s, now, "extension.ready")); err != ErrLaunch {
		t.Fatalf("expired pending start authorized write: %v", err)
	}
	if fresh, err := st.Start(17, s); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	if err := st.SetPolicy(Policy{false, 7, 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Start(17, s); err != ErrDisabled {
		t.Fatal(err)
	}
	if err := st.SetPolicy(Policy{true, 7, 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(17, testBatch(s, now, "extension.ready")); err != ErrLaunch {
		t.Fatalf("disable retained pending authorization: %v", err)
	}
	if files := diagnosticFiles(t, dir); len(files) != 0 {
		t.Fatal(files)
	}
}

func TestPendingAdmissionsAreBoundedAndFreedOnWrite(t *testing.T) {
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(t.TempDir())
	st.now = func() time.Time { return now }
	s := testSession("fixture-device", launchA)
	for owner := int32(1); owner <= 4096; owner++ {
		if fresh, err := st.Start(owner, s); !fresh || err != nil {
			t.Fatal(owner, fresh, err)
		}
	}
	if fresh, err := st.Start(4097, s); fresh || err == nil {
		t.Fatalf("full pending set did not reject a new start: %t %v", fresh, err)
	}
	if fresh, err := st.Start(1, s); fresh || err != nil {
		t.Fatalf("capacity affected a duplicate: %t %v", fresh, err)
	}
	if err := st.Append(1, testBatch(s, now, "extension.ready")); err != nil {
		t.Fatal(err)
	}
	if fresh, err := st.Start(4097, s); !fresh || err != nil {
		t.Fatalf("successful first write did not free pending capacity: %t %v", fresh, err)
	}
}

func TestFailedFirstWriteKeepsPendingWithoutPublishingPartialFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(dir)
	st.now = func() time.Time { return now }
	s := testSession("fixture-device", launchA)
	if fresh, err := st.Start(17, s); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	folder := filepath.Join(dir, "client", "Browser_extensions")
	want := filepath.Join(folder, fmt.Sprintf("%019d_u17_mextension_dfixture-device_s%s.jsonl", now.UnixNano(), launchA))
	// Occupying the target with a directory forces publication to fail after the
	// temp file has been written; no completed .jsonl can be visible.
	if err := os.MkdirAll(want, 0700); err != nil {
		t.Fatal(err)
	}
	b := testBatch(s, now, "extension.ready")
	if err := st.Append(17, b); err == nil || errors.Is(err, ErrLaunch) {
		t.Fatalf("expected publication failure instead of success or lost pending: %v", err)
	}
	if err := os.Remove(want); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(folder); err != nil || len(files) != 0 {
		t.Fatalf("failed write left files behind: %v %v", files, err)
	}
	if err := st.Append(17, b); err != nil {
		t.Fatalf("retry could not use pending: %v", err)
	}
	if files := diagnosticFiles(t, dir); len(files) != 1 || files[0] != want {
		t.Fatal(files)
	}
}

func TestPendingIsScopedToOwnerModeDeviceBrowserAndLaunch(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(dir)
	st.now = func() time.Time { return now }
	base := Session{Mode: ModeBrowser, Version: "1.2.3", DeviceID: "fixture-host", BrowserID: "fixture-browser", LaunchID: launchA}
	if fresh, err := st.Start(17, base); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	for _, tc := range []struct {
		owner int32
		meta  Session
	}{
		{18, base},
		{17, Session{Mode: ModeExtension, Version: base.Version, DeviceID: base.DeviceID, LaunchID: launchA}},
		{17, Session{Mode: ModeExam, Version: base.Version, DeviceID: base.DeviceID, LaunchID: launchA}},
		{17, Session{Mode: base.Mode, Version: base.Version, DeviceID: "other-device", BrowserID: base.BrowserID, LaunchID: launchA}},
		{17, Session{Mode: base.Mode, Version: base.Version, DeviceID: base.DeviceID, BrowserID: "other-browser", LaunchID: launchA}},
		{17, Session{Mode: base.Mode, Version: base.Version, DeviceID: base.DeviceID, BrowserID: base.BrowserID, LaunchID: launchB}},
	} {
		if err := st.Append(tc.owner, testBatch(tc.meta, now, "extension.ready")); err != ErrLaunch {
			t.Fatalf("wrong session received start authorization: %+v %v", tc, err)
		}
	}
	if files := diagnosticFiles(t, dir); len(files) != 0 {
		t.Fatal(files)
	}
	if err := st.Append(17, testBatch(base, now, "extension.ready")); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentsRotateBySizeAndAgeWithinSameLaunch(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(dir)
	st.now = func() time.Time { return now }
	st.segmentBytes = 500 // One synthetic row fits; two do not.
	s := Session{Mode: ModeBrowser, Version: "1.2.3", DeviceID: "fixture-host", BrowserID: "fixture-browser", LaunchID: launchA}
	if fresh, err := st.Start(17, s); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	for _, event := range []string{"extension.ready", "settings.saved"} {
		if err := st.Append(17, testBatch(s, now, event)); err != nil {
			t.Fatal(err)
		}
	}
	if files := diagnosticFiles(t, dir); len(files) != 2 {
		t.Fatalf("expected rotation by size, got %v", files)
	}
	st = New(dir)
	now = now.Add(time.Hour)
	st.now = func() time.Time { return now }
	if err := st.Append(17, testBatch(s, now, "network.offline")); err != nil {
		t.Fatal(err)
	}
	if files := diagnosticFiles(t, dir); len(files) != 3 {
		t.Fatalf("expected rotation by age after restart, got %v", files)
	}
	p, err := st.Read("", 10, Filter{UserID: 17, Mode: ModeBrowser, DeviceID: s.DeviceID, BrowserID: s.BrowserID, LaunchID: launchA})
	if err != nil || len(p.Entries) != 3 || p.Entries[0].Event != "network.offline" || p.Entries[1].Event != "settings.saved" || p.Entries[2].Event != "extension.ready" {
		t.Fatal(p, err)
	}
}

func TestLegacyNativeLogsShareCapacityWithNewLayout(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	s := Session{Mode: ModeBrowser, Version: "1.2.3", DeviceID: "old:host.with-punctuation", BrowserID: "old:instance", LaunchID: launchA}
	device := fmt.Sprintf("h%x", sha256.Sum256([]byte(s.DeviceID)))
	browser := fmt.Sprintf("h%x", sha256.Sum256([]byte(s.BrowserID)))
	if err := os.Mkdir(filepath.Join(dir, "extension"), 0700); err != nil {
		t.Fatal(err)
	}
	oldTime := now.Add(-time.Hour)
	seq := oldTime.UnixNano()
	oldPath := filepath.Join(dir, "extension", fmt.Sprintf("%019d_u17_mnative_browser_d%s_b%s_s%s.jsonl", seq, device, browser, launchA))
	raw, err := json.Marshal(Record{Session: s, UserID: 17, Seq: seq, ReceivedAt: oldTime, Entry: Entry{Event: "session.start", Level: "info", Time: oldTime}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(oldPath, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	st := New(dir)
	st.now = func() time.Time { return now }
	st.unitBytes = int64(len(raw) + 100) // Either segment fits individually, but not both together.
	if err = st.SetPolicy(Policy{true, 7, 1}); err != nil {
		t.Fatal(err)
	}
	filter := Filter{UserID: 17, Mode: ModeBrowser, DeviceID: s.DeviceID, BrowserID: s.BrowserID, LaunchID: launchA}
	if page, err := st.Read("", 10, filter); err != nil || len(page.Entries) != 1 || page.Entries[0].Event != "session.start" {
		t.Fatal(page, err)
	}
	if err = st.Append(17, testBatch(s, now, "extension.ready")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "client", "Browser_extensions", "Multi-browser_mode", device, fmt.Sprintf("%019d_u17_mnative_browser_d%s_b%s_s%s.jsonl", now.UnixNano(), device, browser, launchA))
	if got := diagnosticFiles(t, dir); len(got) != 1 || got[0] != want {
		t.Fatalf("joint quota did not evict legacy segment or host routing was unsafe: %v", got)
	}
	st = New(dir)
	st.now = func() time.Time { return now }
	if page, err := st.Read("", 10, filter); err != nil || len(page.Entries) != 1 || page.Entries[0].Event != "extension.ready" {
		t.Fatal(page, err)
	}
}

func TestBrowserFilenameSeparatesIdentityDelimiters(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	st := New(dir)
	st.now = func() time.Time { return now }
	session := Session{Mode: ModeBrowser, Version: "1.2.3", DeviceID: "fixture_bhost", BrowserID: "fixture_bbrowser", LaunchID: launchA}
	if fresh, err := st.Start(17, session); !fresh || err != nil {
		t.Fatal(fresh, err)
	}
	if err := st.Append(17, testBatch(session, now, "extension.ready")); err != nil {
		t.Fatal(err)
	}
	device := fmt.Sprintf("h%x", sha256.Sum256([]byte(session.DeviceID)))
	browser := fmt.Sprintf("h%x", sha256.Sum256([]byte(session.BrowserID)))
	want := filepath.Join(dir, "client", "Browser_extensions", "Multi-browser_mode", device, fmt.Sprintf("%019d_u17_mnative_browser_d%s_b%s_s%s.jsonl", now.UnixNano(), device, browser, launchA))
	if got := diagnosticFiles(t, dir); len(got) != 1 || got[0] != want {
		t.Fatalf("identity bytes make the _b delimiter ambiguous: %v", got)
	}
}
