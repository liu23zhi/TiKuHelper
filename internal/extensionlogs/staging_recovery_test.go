package extensionlogs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func stageFixture(t *testing.T, dir string, data []byte) *os.File {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, ".segment-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestColdLoadRecoversOnlyManagedStagingFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	sessions := []Session{
		{Mode: ModeExtension, Version: "1.2.3", DeviceID: "fixture-extension", LaunchID: launchA},
		{Mode: ModeBrowser, Version: "1.2.3", DeviceID: "fixture-host", BrowserID: "fixture-browser", LaunchID: launchA},
		{Mode: ModeExam, Version: "1.2.3", DeviceID: "fixture-script", LaunchID: launchA},
	}
	legacy := stageFixture(t, filepath.Join(dir, "extension"), []byte("not a new-layout staging file"))
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	oldTime := now.Add(-time.Minute)
	oldSeq := oldTime.UnixNano()
	old := testSession("legacy-fixture", launchB)
	oldRow, err := json.Marshal(Record{Session: old, UserID: 17, Seq: oldSeq, ReceivedAt: oldTime, Entry: Entry{Event: "session.start", Time: oldTime, Level: "info"}})
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, "extension", fmt.Sprintf("%019d_u17_dlegacy-fixture_s%s.jsonl", oldSeq, launchB))
	if err := os.WriteFile(oldPath, append(oldRow, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	st := New(dir)
	st.now = func() time.Time { return now }
	for _, s := range sessions {
		if _, err := st.Start(17, s); err != nil {
			t.Fatal(err)
		}
		if err := st.Append(17, testBatch(s, now, "extension.ready")); err != nil {
			t.Fatal(err)
		}
	}

	preserved := map[string][]byte{legacy.Name(): []byte("not a new-layout staging file")}
	formal := diagnosticFiles(t, dir)
	if len(formal) != 4 {
		t.Fatalf("expected three new segments and one legacy segment: %v", formal)
	}
	var orphaned []string
	for i, path := range formal {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		preserved[path] = raw
		if path == oldPath {
			continue
		}
		folder := filepath.Dir(path)
		// A process may stop after writing a full/partial batch, or before the first byte.
		size := len(raw)
		switch i {
		case 0:
			size = 0
		case 1:
			size /= 2
		}
		f := stageFixture(t, folder, raw[:size])
		orphaned = append(orphaned, f.Name())
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".segment-note", ".segment-042", ".segment-4294967296", "operator.log"} {
			unmanaged := filepath.Join(folder, name)
			text := []byte("unrelated fixture: " + name)
			if err := os.WriteFile(unmanaged, text, 0600); err != nil {
				t.Fatal(err)
			}
			preserved[unmanaged] = text
		}
	}
	other := stageFixture(t, filepath.Join(dir, "client", "Unmanaged"), []byte("unmanaged directory"))
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	preserved[other.Name()] = []byte("unmanaged directory")

	// Restart from the same root. No write/renewal is needed to recover the disk.
	st = New(dir)
	st.now = func() time.Time { return now }
	p, err := st.Read("", 20, Filter{UserID: 17})
	if err != nil || len(p.Entries) != 4 {
		t.Fatalf("restart should keep all formal records: %d %v", len(p.Entries), err)
	}
	for _, name := range orphaned {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Errorf("abandoned managed staging file retained: %s (%v)", name, err)
		}
	}
	for path, want := range preserved {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("existing managed or unrelated file changed: %s (%v)", path, err)
		}
	}
}

func TestRecoveryDoesNotFollowStagingOrDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink fixture")
	}
	logs, outside := t.TempDir(), t.TempDir()
	outsideDir := filepath.Join(outside, "Browser_extensions")
	abandoned := stageFixture(t, outsideDir, []byte("outside contents"))
	if err := abandoned.Close(); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(logs, "client")
	if err := os.Symlink(outside, client); err != nil {
		t.Fatal(err)
	}
	if _, err := New(logs).Read("", 1, Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abandoned.Name()); err != nil {
		t.Fatalf("followed managed-directory symlink: %v", err)
	}

	realRoot := t.TempDir()
	dir := filepath.Join(realRoot, "client", "Browser_extensions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".segment-42")
	if err := os.Symlink(abandoned.Name(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(realRoot).Read("", 1, Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("staging pattern is not permission to remove a symlink: %v", err)
	}
	if got, err := os.ReadFile(abandoned.Name()); err != nil || string(got) != "outside contents" {
		t.Fatal(string(got), err)
	}
}

func TestWarmSweepNeverDeletesAnInFlightStagingFile(t *testing.T) {
	logs := t.TempDir()
	st := New(logs)
	if _, err := st.Read("", 1, Filter{}); err != nil {
		t.Fatal(err)
	}
	f := stageFixture(t, filepath.Join(logs, "client", "Browser_extensions"), []byte("first half"))
	if err := st.Sweep(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(" second half")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(f.Name()); err != nil || string(got) != "first half second half" {
		t.Fatalf("warm sweep interfered with staging: %q %v", got, err)
	}
}
