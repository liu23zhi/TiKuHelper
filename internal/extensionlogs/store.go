// Package extensionlogs keeps the administrator-managed extension diagnostics in private
// JSONL files. A browser launch has its own files, independent of server and audit logs.
package extensionlogs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrBatch     = errors.New("invalid diagnostic batch")
	ErrCursor    = errors.New("invalid filter or cursor")
	ErrPolicy    = errors.New("invalid retention policy")
	ErrDisabled  = errors.New("extension logging is disabled")
	ErrLaunch    = errors.New("start signal required")
	errPending   = errors.New("too many pending diagnostic launches")
	versionRE    = regexp.MustCompile(`^(dev|[0-9]{1,6}(\.[0-9]{1,6}){1,3})$`)
	deviceRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	launchRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	fileDeviceRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	hostHashRE   = regexp.MustCompile(`^h[0-9a-f]{64}$`)
	segmentRE    = regexp.MustCompile(`^([0-9]{19})_u[0-9]{1,10}(_m(extension|native_browser|native_exam))?_d[A-Za-z0-9_-]{1,65}(_b[A-Za-z0-9_-]{1,65})?_s[0-9a-f-]{36}\.jsonl$`)
)

const (
	MaxBatch = 50

	pendingLimit = 4096
	pendingTTL   = time.Hour

	legacyDir  = "extension"
	plainDir   = "client/Browser_extensions"
	browserDir = plainDir + "/Multi-browser_mode"
	examDir    = "client/Exam_Client_Injection"

	stagingPrefix = ".segment-"
)

type Policy struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
	MaxGB         int  `json:"max_gb"`
}

func (p Policy) valid() bool {
	return p.RetentionDays >= 1 && p.RetentionDays <= 3650 && p.MaxGB >= 1 && p.MaxGB <= 1024
}

const (
	ModeExtension = "extension"
	ModeBrowser   = "native_browser"
	ModeExam      = "native_exam"
)

// Missing mode on pre-upgrade traffic/files identifies only a plain extension.
func EffectiveMode(mode string) string {
	if mode == "" {
		return ModeExtension
	}
	return mode
}
func KnownMode(mode string) bool {
	return mode == ModeExtension || mode == ModeBrowser || mode == ModeExam
}

type Session struct {
	Mode      string `json:"mode"`
	BrowserID string `json:"browser_id,omitempty"`
	Version   string `json:"version"`
	DeviceID  string `json:"device_id"`
	LaunchID  string `json:"launch_id"`
}

func validDevice(s string) bool { return deviceRE.MatchString(s) && !strings.Contains(s, "..") }
func (s Session) valid() bool {
	mode := EffectiveMode(s.Mode)
	return KnownMode(mode) && ((mode == ModeBrowser && validDevice(s.BrowserID)) ||
		(mode != ModeBrowser && s.BrowserID == "")) && versionRE.MatchString(s.Version) && validDevice(s.DeviceID) && launchRE.MatchString(s.LaunchID)
}

type Entry struct {
	Time       time.Time `json:"time"`
	Level      string    `json:"level"`
	Event      string    `json:"event"`
	DurationMS *int64    `json:"duration_ms,omitempty"`
	Count      *int64    `json:"count,omitempty"`
}
type Batch struct {
	Session
	Entries []Entry `json:"entries"`
}

func (b Batch) valid() bool {
	if !b.Session.valid() || len(b.Entries) == 0 || len(b.Entries) > MaxBatch {
		return false
	}
	for _, x := range b.Entries {
		switch x.Level {
		case "info", "warn", "error", "debug":
		default:
			return false
		}
		switch x.Event {
		case "extension.ready", "settings.saved", "sync.success", "sync.failed", "network.timeout", "network.offline", "runtime.error":
		default:
			return false
		}
		if x.Time.IsZero() || x.Time.Year() < 2000 || x.Time.Year() > 9999 {
			return false
		}
		if x.DurationMS != nil && (*x.DurationMS < 0 || *x.DurationMS > 86400000) {
			return false
		}
		if x.Count != nil && (*x.Count < 0 || *x.Count > 1000000) {
			return false
		}
	}
	return true
}

type Record struct {
	Entry
	Session
	ReceivedAt time.Time `json:"received_at"`
	UserID     int32     `json:"user_id"`
	Seq        int64     `json:"seq,string"`
}
type Page struct {
	Entries []Record `json:"entries"`
	Next    string   `json:"next_cursor"`
}
type Filter struct {
	UserID             int32
	DeviceID, LaunchID string
	Mode, BrowserID    string
}

func (f Filter) Valid() bool {
	return (f.Mode == "" || KnownMode(f.Mode)) && (f.BrowserID == "" || (validDevice(f.BrowserID) && (f.Mode == "" || f.Mode == ModeBrowser))) && f.UserID >= 0 && (f.DeviceID == "" || validDevice(f.DeviceID)) && (f.LaunchID == "" || launchRE.MatchString(f.LaunchID))
}
func (f Filter) matches(k sessionKey) bool {
	return (f.Mode == "" || f.Mode == k.mode) && (f.BrowserID == "" || f.BrowserID == k.browser) && (f.UserID == 0 || f.UserID == k.owner) && (f.DeviceID == "" || f.DeviceID == k.device) && (f.LaunchID == "" || strings.EqualFold(f.LaunchID, k.launch))
}

type sessionKey struct {
	owner                         int32
	mode, device, browser, launch string
}

func (m Session) key(owner int32) sessionKey {
	return sessionKey{owner: owner, mode: EffectiveMode(m.Mode), device: m.DeviceID, browser: m.BrowserID, launch: strings.ToLower(m.LaunchID)}
}

type segment struct {
	name              string // path relative to the logs root
	key               sessionKey
	first, last, size int64
	began, updated    time.Time
}
type Store struct {
	dir                     string
	mu                      sync.Mutex
	now                     func() time.Time
	segmentBytes, unitBytes int64
	// Reconstructed from managed files once, including after a process restart.
	files             map[string]*segment
	latest            map[sessionKey]*segment
	total, seq        int64
	nextSweep         time.Time
	legacyBudgetBytes int64
	recoveredStaging  bool
	wakeCleanup       chan struct{}
	// A start signal does not count as a diagnostic event or survive restart.
	pending map[sessionKey]time.Time
}

// New accepts the logs root, containing both the legacy extension directory
// (including its policy) and the three client destinations.
func New(dir string) *Store {
	return &Store{dir: dir, now: time.Now, segmentBytes: 4 << 20, unitBytes: 1_000_000_000, pending: make(map[sessionKey]time.Time), wakeCleanup: make(chan struct{}, 1)}
}

var Default = New("logs")

func cappedDiagnosticPolicy(p Policy) Policy {
	p.RetentionDays, p.MaxGB = min(p.RetentionDays, 7), min(p.MaxGB, 10)
	return p
}

func (s *Store) policy() (Policy, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, legacyDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Policy{true, 7, 10}, nil
	}
	p := Policy{Enabled: true} // settings from before the switch was introduced
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil || !p.valid() {
		return Policy{}, ErrPolicy
	}
	return p, nil
}

// Policy reads the stored configuration, including pre-T06 values. Product
// reads and enforcement use EffectivePolicy so old files need no migration.
func (s *Store) Policy() (Policy, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.policy() }
func (s *Store) EffectivePolicy() (Policy, error) {
	p, err := s.Policy()
	return cappedDiagnosticPolicy(p), err
}
func (s *Store) SetPolicy(p Policy) error {
	if !p.valid() {
		return ErrPolicy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.dir, legacyDir), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Join(s.dir, legacyDir), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	e := json.NewEncoder(f).Encode(p)
	c := f.Close()
	if e != nil {
		return e
	}
	if c != nil {
		return c
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, legacyDir, "settings.json")); err != nil {
		return err
	}
	if !p.Enabled {
		clear(s.pending)
	}
	err = s.sweepReceiptBudget(p, s.now().UTC(), true)
	select {
	case s.wakeCleanup <- struct{}{}:
	default:
	}
	return err
}

// Only the three managed layouts and the old flat directory are scanned. A
// generated or hashed host slug is a single path component, never a pathname.
func (s *Store) segmentDirs() ([]string, error) {
	dirs := []string{legacyDir, filepath.FromSlash(plainDir), filepath.FromSlash(examDir)}
	hosts, err := os.ReadDir(filepath.Join(s.dir, browserDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, it := range hosts {
		if it.IsDir() && (fileDeviceRE.MatchString(it.Name()) || hostHashRE.MatchString(it.Name())) {
			dirs = append(dirs, filepath.Join(browserDir, it.Name()))
		}
	}
	return dirs, nil
}

// recoverStaging runs only while rebuilding the index under s.mu. Under the
// store's single-writer contract no writer can still be using these paths.
// The legacy directory never used a staging file and is excluded by the caller.
func (s *Store) recoverStaging(dir string, entries []os.DirEntry) error {
	var root *os.Root
	for _, entry := range entries {
		// The pinned Go CreateTemp generates canonical decimal uint32 suffixes.
		// Only this reserved namespace inside the managed layouts belongs to us.
		suffix, ok := strings.CutPrefix(entry.Name(), stagingPrefix)
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		id, err := strconv.ParseUint(suffix, 10, 32)
		if err != nil || suffix != strconv.FormatUint(id, 10) {
			continue
		}
		if root == nil {
			root, err = os.OpenRoot(s.dir)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			defer root.Close()
			// Do not reclaim anything reached via a linked destination or ancestor.
			var prefix string
			for _, part := range strings.Split(dir, string(filepath.Separator)) {
				prefix = filepath.Join(prefix, part)
				fi, err := root.Lstat(prefix)
				if errors.Is(err, os.ErrNotExist) || (err == nil && !fi.IsDir()) {
					return nil
				}
				if err != nil {
					return err
				}
			}
		}
		name := filepath.Join(dir, entry.Name())
		fi, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (s *Store) load() error {
	if s.files != nil {
		return nil
	}
	dirs, err := s.segmentDirs()
	if err != nil {
		return err
	}
	files := map[string]*segment{}
	latest := map[sessionKey]*segment{}
	var total, seq int64
	for _, dir := range dirs {
		ls, err := os.ReadDir(filepath.Join(s.dir, dir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !s.recoveredStaging && dir != legacyDir {
			if err := s.recoverStaging(dir, ls); err != nil {
				return err
			}
		}
		for _, it := range ls {
			m := segmentRE.FindStringSubmatch(it.Name())
			if m == nil || !it.Type().IsRegular() {
				continue
			}
			fi, err := it.Info()
			if err != nil {
				return err
			}
			if fi.Size() == 0 {
				continue
			}
			name := filepath.Join(dir, it.Name())
			f, err := os.Open(filepath.Join(s.dir, name))
			if err != nil {
				return err
			}
			first, _, err := rowAt(f, 0, 0, fi.Size())
			if err != nil {
				f.Close()
				return err
			}
			last, _, err := rowAt(f, fi.Size()-1, 0, fi.Size())
			f.Close()
			if err != nil {
				return err
			}
			stamp, _ := strconv.ParseInt(m[1], 10, 64)
			key := first.Session.key(first.UserID)
			if !first.Session.valid() || first.UserID <= 0 || first.Seq < stamp || last.Seq < first.Seq || !last.Session.valid() || last.Session.key(last.UserID) != key ||
				(dir != legacyDir && name != key.segmentName(stamp)) {
				return errors.New("inconsistent diagnostic segment")
			}
			x := &segment{name, key, first.Seq, last.Seq, fi.Size(), first.ReceivedAt, last.ReceivedAt}
			files[x.name] = x
			if cur := latest[key]; cur == nil || cur.last < x.last {
				latest[key] = x
			}
			if x.last > seq {
				seq = x.last
			}
			total += x.size
		}
	}
	s.files, s.latest, s.seq, s.total = files, latest, seq, total
	s.recoveredStaging = true
	return nil
}

// rowAt reads one bounded JSONL record containing pos and returns its exclusive end offset.
// All writer-generated rows are much smaller than 2 KiB; no user path is accepted.
func rowAt(f *os.File, pos, lo, hi int64) (Record, int64, error) {
	var row Record
	from := pos - 2048
	if from < lo {
		from = lo
	}
	to := pos + 2049
	if to > hi {
		to = hi
	}
	data, err := io.ReadAll(io.NewSectionReader(f, from, to-from))
	if err != nil {
		return row, 0, err
	}
	mid := int(pos - from)
	if mid < 0 || mid >= len(data) {
		return row, 0, errors.New("bad diagnostic offset")
	}
	left := bytes.LastIndexByte(data[:mid], '\n') + 1
	right := bytes.IndexByte(data[mid:], '\n')
	if right < 0 || (left == 0 && from != lo) {
		return row, 0, errors.New("oversized or incomplete diagnostic record")
	}
	end := mid + right
	if err = json.Unmarshal(data[left:end], &row); err != nil {
		return row, 0, errors.New("invalid diagnostic record")
	}
	return row, from + int64(end) + 1, nil
}
func (s *Store) prune(p Policy, now time.Time, incoming int64, force bool) error {
	cap := int64(p.MaxGB) * s.unitBytes
	if incoming > cap {
		return ErrBatch
	}
	if !force && now.Before(s.nextSweep) && s.total+incoming <= cap {
		return nil
	}
	items := make([]*segment, 0, len(s.files))
	for _, x := range s.files {
		items = append(items, x)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].last < items[j].last })
	cutoff := now.AddDate(0, 0, -p.RetentionDays)
	for _, x := range items {
		if x.updated.After(cutoff) && s.total+incoming <= cap {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, x.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if x.key.mode == ModeBrowser && filepath.Dir(x.name) != legacyDir {
			// A host directory with no remaining segments need not be retained.
			_ = os.Remove(filepath.Join(s.dir, filepath.Dir(x.name)))
		}
		s.total -= x.size
		delete(s.files, x.name)
		if s.latest[x.key] == x {
			delete(s.latest, x.key)
		}
	}
	s.nextSweep = now.Add(time.Hour)
	return nil
}
func (s *Store) Sweep() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.policy()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if p.Enabled {
		s.expirePending(now)
	} else {
		clear(s.pending)
	}
	return s.sweepReceiptBudget(p, now, true)
}
func (s *Store) RunCleanup(ctx context.Context, onError func(error)) {
	go func() {
		for ctx.Err() == nil {
			err := s.Sweep()
			if err != nil && onError != nil {
				onError(err)
			}
			s.mu.Lock()
			delay := s.nextSweep.Sub(s.now())
			s.mu.Unlock()
			if err != nil || delay <= 0 {
				delay = time.Minute
			}
			t := time.NewTimer(min(delay, time.Hour))
			select {
			case <-ctx.Done():
			case <-t.C:
			case <-s.wakeCleanup:
			}
			t.Stop()
		}
	}()
}

func deviceSlug(id string) string {
	// Keep UUIDs readable. Hash other legal identities so none can contain
	// the underscore delimiters separating fields in a segment name.
	if fileDeviceRE.MatchString(id) {
		return id
	}
	return fmt.Sprintf("h%x", sha256.Sum256([]byte(id)))
}
func (k sessionKey) segmentName(first int64) string {
	dir, browser := plainDir, ""
	switch k.mode {
	case ModeBrowser:
		dir = filepath.Join(browserDir, deviceSlug(k.device))
		browser = "_b" + deviceSlug(k.browser)
	case ModeExam:
		dir = examDir
	}
	name := fmt.Sprintf("%019d_u%d_m%s_d%s%s_s%s.jsonl", first, k.owner, k.mode, deviceSlug(k.device), browser, k.launch)
	return filepath.Join(dir, name)
}

func (s *Store) expirePending(now time.Time) {
	for key, expiry := range s.pending {
		if !now.Before(expiry) {
			delete(s.pending, key)
		}
	}
}

func (s *Store) Start(owner int32, meta Session) (bool, error) {
	if owner <= 0 || !meta.valid() {
		return false, ErrBatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.policy()
	if err != nil {
		return false, err
	}
	if !p.Enabled {
		return false, ErrDisabled
	}
	if err = s.load(); err != nil {
		return false, err
	}
	now := s.now().UTC()
	key := meta.key(owner)
	if x := s.latest[key]; x != nil && x.updated.After(now.AddDate(0, 0, -p.RetentionDays)) {
		return false, nil
	}
	if now.Before(s.pending[key]) {
		return false, nil
	}
	if len(s.pending) >= pendingLimit {
		s.expirePending(now)
		if len(s.pending) >= pendingLimit {
			return false, errPending
		}
	}
	s.pending[key] = now.Add(pendingTTL)
	return true, nil
}

func (s *Store) Append(owner int32, b Batch) error {
	if owner <= 0 || !b.valid() {
		return ErrBatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.policy()
	if err != nil {
		return err
	}
	if !p.Enabled {
		return ErrDisabled
	}
	if err = s.load(); err != nil {
		return err
	}
	now := s.now().UTC()
	key := b.Session.key(owner)
	if err := s.prune(p, now, 0, false); err != nil {
		return err
	}
	if s.latest[key] == nil && !now.Before(s.pending[key]) {
		delete(s.pending, key)
		return ErrLaunch
	}
	meta := b.Session
	meta.LaunchID = key.launch
	meta.Mode = key.mode
	// Preserve the unique arrival sequence and cursor order across restarts.
	seq := now.UnixNano()
	if seq <= s.seq {
		seq = s.seq + 1
	}
	first := seq
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, entry := range b.Entries {
		entry.Time = entry.Time.UTC()
		if err = enc.Encode(Record{entry, meta, now, owner, seq}); err != nil {
			return err
		}
		seq++
	}
	n := int64(buf.Len())
	if err = s.prune(p, now, n, false); err != nil {
		return err
	}
	x := s.latest[key]
	if x == nil || filepath.Dir(x.name) == legacyDir || x.size+n > s.segmentBytes || now.Sub(x.began) >= time.Hour {
		// Keep old files readable in place; an existing legacy launch can append
		// without another signal, but all newly published segments use the new layout.
		x = &segment{name: key.segmentName(first), key: key, first: first, began: now}
		if err = createSegment(filepath.Join(s.dir, x.name), buf.Bytes()); err != nil {
			return err
		}
	} else if err = s.appendSegment(x, buf.Bytes()); err != nil {
		return err
	}
	x.size += n
	x.updated = now
	x.last = seq - 1
	s.seq = x.last
	s.total += n
	s.files[x.name] = x
	s.latest[key] = x
	delete(s.pending, key)
	return nil
}

// A final .jsonl name never exposes an empty file or an incomplete first batch.
func createSegment(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, stagingPrefix+"*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// Append one batch under the store lock, rolling back any partial write. No
// O_CREATE: a vanished indexed path must not silently acquire a new first row.
func (s *Store) appendSegment(x *segment, data []byte) error {
	f, err := os.OpenFile(filepath.Join(s.dir, x.name), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Truncate(x.size)
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		s.files = nil
	}
	return err
}

// exclusiveEnd finds the byte boundary before an arrival cursor without scanning the file from the beginning.
func exclusiveEnd(f *os.File, x *segment, before int64) (int64, error) {
	if x.last < before {
		return x.size, nil
	}
	lo, hi := int64(0), x.size
	for hi-lo > 8192 {
		mid := lo + (hi-lo)/2
		row, end, err := rowAt(f, mid, lo, hi)
		if err != nil {
			return 0, err
		}
		if row.Seq < before {
			lo = end
		} else {
			begin := mid - 2048
			if begin < lo {
				begin = lo
			}
			d, e := io.ReadAll(io.NewSectionReader(f, begin, mid-begin))
			if e != nil {
				return 0, e
			}
			hi = begin + int64(bytes.LastIndexByte(d, '\n')+1)
		}
	}
	rd := bufio.NewReader(io.NewSectionReader(f, lo, hi-lo))
	off := lo
	for off < hi {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return 0, err
		}
		var r Record
		if err = json.Unmarshal(line, &r); err != nil {
			return 0, err
		}
		if r.Seq >= before {
			break
		}
		off += int64(len(line))
	}
	return off, nil
}
func recent(f *os.File, end int64, n int, cut time.Time) ([]Record, error) {
	out := make([]Record, 0, n)
	for end > 0 && len(out) < n {
		from := end - 96*1024
		if from < 0 {
			from = 0
		}
		data, err := io.ReadAll(io.NewSectionReader(f, from, end-from))
		if err != nil {
			return nil, err
		}
		original := end
		k := len(data)
		for k > 0 {
			if data[k-1] == '\n' {
				k--
				if k == 0 {
					end = from
					break
				}
			}
			j := bytes.LastIndexByte(data[:k], '\n') + 1
			if j == 0 && from != 0 {
				break
			}
			var r Record
			if err = json.Unmarshal(data[j:k], &r); err != nil {
				return nil, err
			}
			if r.ReceivedAt.Before(cut) {
				return out, nil
			}
			r.Mode = EffectiveMode(r.Mode) // old files lacked the field
			out = append(out, r)
			k = j
			end = from + int64(j)
			if len(out) == n {
				return out, nil
			}
		}
		if end == original {
			return nil, errors.New("incomplete diagnostic record")
		}
	}
	return out, nil
}
func (s *Store) Read(cursor string, limit int, filter Filter) (Page, error) {
	out := Page{Entries: []Record{}}
	if limit < 1 || limit > 100 || !filter.Valid() {
		return out, ErrCursor
	}
	before := int64(1<<63 - 1)
	if cursor != "" {
		var err error
		before, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || before <= 0 {
			return out, ErrCursor
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return out, err
	}
	p, err := s.policy()
	if err != nil {
		return out, err
	}
	cut := s.now().UTC().AddDate(0, 0, -cappedDiagnosticPolicy(p).RetentionDays)
	candidates := make([]*segment, 0)
	for _, x := range s.files {
		if x.first < before && filter.matches(x.key) && !x.updated.Before(cut) {
			candidates = append(candidates, x)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].last > candidates[j].last })
	n := limit + 1
	for _, x := range candidates {
		if len(out.Entries) == n && x.last <= out.Entries[n-1].Seq {
			break
		}
		f, err := os.Open(filepath.Join(s.dir, x.name))
		if err != nil {
			return out, err
		}
		end, err := exclusiveEnd(f, x, before)
		if err != nil {
			f.Close()
			return out, err
		}
		rows, err := recent(f, end, n, cut)
		f.Close()
		if err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, rows...)
		sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Seq > out.Entries[j].Seq })
		if len(out.Entries) > n {
			out.Entries = out.Entries[:n]
		}
	}
	if len(out.Entries) > limit {
		out.Entries = out.Entries[:limit]
		out.Next = strconv.FormatInt(out.Entries[limit-1].Seq, 10)
	}
	return out, nil
}
