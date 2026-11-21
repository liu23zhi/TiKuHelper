// Package extensionlogs stores opt-in extension diagnostics independently of server and audit logs.
package extensionlogs

import (
	"bytes"
	"context"
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
	ErrBatch  = errors.New("invalid diagnostic batch")
	ErrCursor = errors.New("invalid diagnostic cursor")
	ErrPolicy = errors.New("invalid retention policy")
	versionRE = regexp.MustCompile(`^(dev|[0-9]{1,6}(\.[0-9]{1,6}){1,3})$`)
	segmentRE = regexp.MustCompile(`^([0-9]{8}-[0-9]{2})-([0-9]{6})\.jsonl$`)
)

const hourFormat = "20060102-15"
const MaxBatch = 50

type Policy struct {
	RetentionDays int `json:"retention_days"`
	MaxGB         int `json:"max_gb"`
}

func (p Policy) valid() bool {
	return p.RetentionDays >= 1 && p.RetentionDays <= 3650 && p.MaxGB >= 1 && p.MaxGB <= 1024
}

type Entry struct {
	Time       time.Time `json:"time"`
	Level      string    `json:"level"`
	Event      string    `json:"event"`
	DurationMS *int64    `json:"duration_ms,omitempty"`
	Count      *int64    `json:"count,omitempty"`
}
type Batch struct {
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
}

// No generic message, URL, exception, page, token or arbitrary attributes are accepted.
func (b Batch) valid() bool {
	if !versionRE.MatchString(b.Version) || len(b.Entries) == 0 || len(b.Entries) > MaxBatch {
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
	ReceivedAt time.Time `json:"received_at"`
	UserID     int32     `json:"user_id"`
	Version    string    `json:"version"`
}
type Page struct {
	Entries []Record `json:"entries"`
	Next    string   `json:"next_cursor"`
}
type Store struct {
	dir                     string
	mu                      sync.Mutex
	now                     func() time.Time
	segmentBytes, unitBytes int64
}

func New(dir string) *Store {
	return &Store{dir: dir, now: time.Now, segmentBytes: 4 << 20, unitBytes: 1_000_000_000}
}

// Default's directory is a subdirectory of the existing ./logs Compose volume.
var Default = New(filepath.Join("logs", "extension"))

func (s *Store) policy() (Policy, error) {
	path := filepath.Join(s.dir, "settings.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Policy{7, 10}, nil
	}
	var p Policy
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil || !p.valid() {
		return p, ErrPolicy
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
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(p)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "settings.json")); err != nil {
		return err
	}
	_, err = s.prune(p, s.now().UTC(), 0)
	return err
}

type segment struct {
	name string
	hour time.Time
	seq  int
	size int64
}

func (s *Store) segments() ([]segment, error) {
	list, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []segment
	for _, it := range list {
		m := segmentRE.FindStringSubmatch(it.Name())
		if m == nil || !it.Type().IsRegular() {
			continue
		}
		t, err := time.Parse(hourFormat, m[1])
		if err != nil {
			continue
		}
		fi, err := it.Info()
		if err != nil {
			return nil, err
		}
		n, _ := strconv.Atoi(m[2])
		out = append(out, segment{it.Name(), t, n, fi.Size()})
	}
	// os.ReadDir orders by filename; UTC hour and fixed-width part sort chronologically.
	return out, nil
}
func (s *Store) prune(p Policy, now time.Time, incoming int64) ([]segment, error) {
	segs, err := s.segments()
	if err != nil {
		return nil, err
	}
	limit := int64(p.MaxGB) * s.unitBytes
	if incoming > limit {
		return nil, ErrBatch
	}
	cutoff := now.AddDate(0, 0, -p.RetentionDays)
	total := incoming
	for _, seg := range segs {
		total += seg.size
	}
	i := 0
	for i < len(segs) {
		seg := segs[i]
		if seg.hour.Add(time.Hour).After(cutoff) && total <= limit {
			break
		}
		if err = os.Remove(filepath.Join(s.dir, seg.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		total -= seg.size
		i++
	}
	return segs[i:], nil
}
func (s *Store) Sweep() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, e := s.policy()
	if e != nil {
		return e
	}
	_, e = s.prune(p, s.now().UTC(), 0)
	return e
}

// RunCleanup does not need incoming traffic to enforce retention after a restart or during inactivity.
func (s *Store) RunCleanup(ctx context.Context, onError func(error)) {
	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			if err := s.Sweep(); err != nil && onError != nil {
				onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
func (s *Store) Append(owner int32, b Batch) error {
	if owner <= 0 || !b.valid() {
		return ErrBatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, x := range b.Entries {
		x.Time = x.Time.UTC()
		if err := enc.Encode(Record{x, now, owner, b.Version}); err != nil {
			return err
		}
	}
	p, err := s.policy()
	if err != nil {
		return err
	}
	segs, err := s.prune(p, now, int64(buf.Len()))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	hour := now.Truncate(time.Hour)
	seq := 0
	for i := len(segs) - 1; i >= 0; i-- {
		x := segs[i]
		if x.hour.Before(hour) {
			break
		}
		if x.hour.Equal(hour) {
			seq = x.seq
			if x.size+int64(buf.Len()) > s.segmentBytes {
				seq++
			}
			break
		}
	}
	if seq > 999999 {
		return errors.New("too many log segments")
	}
	name := fmt.Sprintf("%s-%06d.jsonl", hour.Format(hourFormat), seq)
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(buf.Bytes())
	cerr := f.Close()
	if err != nil {
		return err
	}
	return cerr
}
func parseCursor(token string) (string, int, error) {
	parts := strings.Split(token, "~")
	if len(parts) != 2 || !segmentRE.MatchString(parts[0]) || len(parts[1]) > 12 {
		return "", 0, ErrCursor
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return "", 0, ErrCursor
	}
	return parts[0], n, nil
}

// Read uses an exclusive segment/byte cursor, so concurrent appends do not duplicate records.
// owner=0 is only passed by the administrator API. Bounded scanning also limits a sparse tenant's page.
func (s *Store) Read(cursor string, limit int, owner int32) (Page, error) {
	out := Page{Entries: make([]Record, 0)}
	if limit < 1 || limit > 100 || owner < 0 {
		return out, ErrCursor
	}
	var mark string
	var pos int
	var err error
	if cursor != "" {
		mark, pos, err = parseCursor(cursor)
		if err != nil {
			return out, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.policy()
	if err != nil {
		return out, err
	}
	segs, err := s.segments()
	if err != nil {
		return out, err
	}
	cutoff := s.now().UTC().AddDate(0, 0, -p.RetentionDays)
	start := len(segs) - 1
	if mark != "" {
		start = sort.Search(len(segs), func(i int) bool { return segs[i].name > mark }) - 1
	}
	scanned := 0
	for i := start; i >= 0; i-- {
		x := segs[i]
		if !x.hour.Add(time.Hour).After(cutoff) {
			break
		}
		end := x.size
		if x.name == mark {
			if int64(pos) > end {
				return out, ErrCursor
			}
			end = int64(pos)
		}
		if end == 0 {
			continue
		}
		f, er := os.Open(filepath.Join(s.dir, x.name))
		if er != nil {
			return out, er
		}
		// Only read the newest 96 KiB of the requested window: no page loads an entire 10 GB archive.
		const window int64 = 96 << 10
		for end > 0 {
			from := end - window
			if from < 0 {
				from = 0
			}
			data, er := io.ReadAll(io.NewSectionReader(f, from, end-from))
			if er != nil {
				f.Close()
				return out, er
			}
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
				} // finish the boundary-spanning line in the next window
				var row Record
				if er = json.Unmarshal(data[j:k], &row); er != nil {
					f.Close()
					return out, errors.New("invalid extension log record")
				}
				k = j
				end = from + int64(j)
				scanned++
				if !row.ReceivedAt.Before(cutoff) && (owner == 0 || row.UserID == owner) {
					out.Entries = append(out.Entries, row)
				}
				if len(out.Entries) == limit || scanned >= 1000 {
					if end > 0 || i > 0 {
						out.Next = fmt.Sprintf("%s~%d", x.name, end)
					}
					f.Close()
					return out, nil
				}
			}
			if end == from+int64(len(data)) {
				f.Close()
				return out, errors.New("oversized extension log record")
			}
		}
		f.Close()
	}
	return out, nil
}
