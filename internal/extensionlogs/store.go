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
	versionRE    = regexp.MustCompile(`^(dev|[0-9]{1,6}(\.[0-9]{1,6}){1,3})$`)
	deviceRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	launchRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	fileDeviceRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	segmentRE    = regexp.MustCompile(`^([0-9]{19})_u[0-9]{1,10}(_m(extension|native_browser|native_exam))?_d[A-Za-z0-9_-]{1,65}(_b[A-Za-z0-9_-]{1,65})?_s[0-9a-f-]{36}\.jsonl$`)
)

const MaxBatch = 50

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
	name              string
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
	files      map[string]*segment
	latest     map[sessionKey]*segment
	total, seq int64
	nextSweep  time.Time
}

func New(dir string) *Store {
	return &Store{dir: dir, now: time.Now, segmentBytes: 4 << 20, unitBytes: 1_000_000_000}
}

var Default = New(filepath.Join("logs", "extension"))

func (s *Store) policy() (Policy, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, "settings.json"))
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
func (s *Store) Policy() (Policy, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.policy() }
func (s *Store) SetPolicy(p Policy) error {
	if !p.valid() {
		return ErrPolicy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".settings-*")
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
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "settings.json")); err != nil {
		return err
	}
	return s.prune(p, s.now().UTC(), 0, true)
}
func (s *Store) load() error {
	if s.files != nil {
		return nil
	}
	ls, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files := map[string]*segment{}
	latest := map[sessionKey]*segment{}
	var total, seq int64
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
		path := filepath.Join(s.dir, it.Name())
		f, err := os.Open(path)
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
		if !first.Session.valid() || first.UserID <= 0 || first.Seq != stamp || last.Seq < first.Seq || !last.Session.valid() || last.Session.key(last.UserID) != first.Session.key(first.UserID) {
			return errors.New("inconsistent diagnostic segment")
		}
		key := first.Session.key(first.UserID)
		x := &segment{it.Name(), key, first.Seq, last.Seq, fi.Size(), first.ReceivedAt, last.ReceivedAt}
		files[x.name] = x
		if cur := latest[key]; cur == nil || cur.last < x.last {
			latest[key] = x
		}
		if x.last > seq {
			seq = x.last
		}
		total += x.size
	}
	s.files, s.latest, s.seq, s.total = files, latest, seq, total
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
	if err := s.load(); err != nil {
		return err
	}
	p, err := s.policy()
	if err != nil {
		return err
	}
	return s.prune(p, s.now().UTC(), 0, true)
}
func (s *Store) RunCleanup(ctx context.Context, onError func(error)) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if err := s.Sweep(); err != nil && onError != nil {
				onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
func deviceSlug(id string) string {
	if fileDeviceRE.MatchString(id) {
		return id
	}
	return fmt.Sprintf("h%x", sha256.Sum256([]byte(id)))
}
func (s *Store) Start(owner int32, meta Session) (bool, error) {
	if owner <= 0 || !meta.valid() {
		return false, ErrBatch
	}
	return s.write(owner, meta, nil, true)
}
func (s *Store) Append(owner int32, b Batch) error {
	if owner <= 0 || !b.valid() {
		return ErrBatch
	}
	_, err := s.write(owner, b.Session, b.Entries, false)
	return err
}
func (s *Store) write(owner int32, meta Session, entries []Entry, signal bool) (bool, error) {
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
	meta.LaunchID = strings.ToLower(meta.LaunchID)
	meta.Mode = EffectiveMode(meta.Mode)
	key := meta.key(owner)
	if err = s.prune(p, now, 0, false); err != nil {
		return false, err
	}
	if signal {
		if s.latest[key] != nil {
			return false, nil
		}
		entries = []Entry{{Time: now, Level: "info", Event: "session.start"}}
	} else if s.latest[key] == nil {
		return false, ErrLaunch
	}
	// Unique arrival sequence survives restarts, so pagination is stable across interleaved devices.
	seq := now.UnixNano()
	if seq <= s.seq {
		seq = s.seq + 1
	}
	first := seq
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, entry := range entries {
		entry.Time = entry.Time.UTC()
		if err = enc.Encode(Record{entry, meta, now, owner, seq}); err != nil {
			return false, err
		}
		seq++
	}
	n := int64(buf.Len())
	if err = s.prune(p, now, n, false); err != nil {
		return false, err
	}
	x := s.latest[key]
	if x == nil || x.size+n > s.segmentBytes || now.Sub(x.began) >= time.Hour {
		browser := ""
		if meta.Mode == ModeBrowser {
			browser = "_b" + deviceSlug(meta.BrowserID)
		}
		name := fmt.Sprintf("%019d_u%d_m%s_d%s%s_s%s.jsonl", first, owner, meta.Mode, deviceSlug(meta.DeviceID), browser, meta.LaunchID)
		x = &segment{name: name, key: key, first: first, began: now}
	}
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, x.name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return false, err
	}
	w, err := f.Write(buf.Bytes())
	if err != nil { // retain the last complete record if a write fails part way through
		_ = f.Truncate(x.size)
		_ = f.Close()
		return false, err
	}
	cerr := f.Close()
	if cerr != nil {
		s.files = nil
		return false, cerr
	}
	x.size += int64(w)
	x.updated = now
	x.last = seq - 1
	s.seq = x.last
	s.total += int64(w)
	s.files[x.name] = x
	s.latest[key] = x
	return true, nil
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
	cut := s.now().UTC().AddDate(0, 0, -p.RetentionDays)
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
