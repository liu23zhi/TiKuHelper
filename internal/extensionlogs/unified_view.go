package extensionlogs

import (
	"cmp"
	"container/heap"
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const MaxDiagnosticExport = 5000

var ErrDiagnosticExportLimit = errors.New("narrow diagnostic export filter")

type unifiedKey struct {
	Sec                           int64
	Nano                          int
	Owner                         int32
	Mode, Device, Browser, Launch string
	Source                        int
	Ordinal, Seq                  int64
	File                          string
}

func compareUnified(a, b unifiedKey) int {
	return cmp.Or(cmp.Compare(a.Sec, b.Sec), cmp.Compare(a.Nano, b.Nano), cmp.Compare(a.Owner, b.Owner),
		cmp.Compare(a.Mode, b.Mode), cmp.Compare(a.Device, b.Device), cmp.Compare(a.Browser, b.Browser),
		cmp.Compare(a.Launch, b.Launch), cmp.Compare(a.Source, b.Source), cmp.Compare(a.Ordinal, b.Ordinal),
		cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.File, b.File))
}

type unifiedEntry struct {
	row DiagnosticReceiptRow
	key unifiedKey
}
type unifiedMax []unifiedEntry

func (h unifiedMax) Len() int           { return len(h) }
func (h unifiedMax) Less(i, j int) bool { return compareUnified(h[i].key, h[j].key) > 0 }
func (h unifiedMax) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *unifiedMax) Push(x any)        { *h = append(*h, x.(unifiedEntry)) }
func (h *unifiedMax) Pop() any {
	a := *h
	n := len(a) - 1
	v := a[n]
	a[n] = unifiedEntry{}
	*h = a[:n]
	return v
}
func (h *unifiedMax) offer(x unifiedEntry, n int) {
	if len(*h) < n {
		heap.Push(h, x)
		return
	}
	if compareUnified(x.key, (*h)[0].key) < 0 {
		(*h)[0] = x
		heap.Fix(h, 0)
	}
}

type unifiedWindow struct {
	Days             int
	At, Expires      int64
	Filter           Filter
	Legacy, Database string
	Watermark        int64
	After            *unifiedKey
}
type UnifiedDiagnosticPage struct {
	Entries  []DiagnosticReceiptRow `json:"entries"`
	Next     string                 `json:"next_cursor"`
	Snapshot string                 `json:"snapshot"`
}

func normalizedDiagnosticFilter(f Filter) Filter {
	f.DeviceID = receiptFilterKey("device", f.DeviceID)
	f.BrowserID = receiptFilterKey("browser", f.BrowserID)
	f.LaunchID = strings.ToLower(f.LaunchID)
	return f
}
func matchesUnifiedFilter(f Filter, k unifiedKey) bool {
	return (f.UserID == 0 || f.UserID == k.Owner) && (f.Mode == "" || f.Mode == k.Mode) &&
		(f.DeviceID == "" || f.DeviceID == k.Device) && (f.BrowserID == "" || f.BrowserID == k.Browser) &&
		(f.LaunchID == "" || f.LaunchID == k.Launch)
}

func encodeUnified(w unifiedWindow) (string, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	sig := cursorSignature(append([]byte("unified-v1\x00"), b...))
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
func decodeUnified(token string) (unifiedWindow, error) {
	var w unifiedWindow
	if len(token) > 2200 {
		return w, ErrCursor
	}
	a, b, ok := strings.Cut(token, ".")
	if !ok {
		return w, ErrCursor
	}
	body, e := base64.RawURLEncoding.DecodeString(a)
	sig, f := base64.RawURLEncoding.DecodeString(b)
	if e != nil || f != nil || !hmac.Equal(sig, cursorSignature(append([]byte("unified-v1\x00"), body...))) ||
		json.Unmarshal(body, &w) != nil {
		return w, ErrCursor
	}
	return w, nil
}
func maskedDiagnosticID(s string) string {
	if s == "" {
		return ""
	}
	return "h:" + s
}

func (s *Store) ReadUnified(ctx context.Context, f Filter, limit int, token string) (UnifiedDiagnosticPage, error) {
	if limit < 1 || limit > 100 {
		return UnifiedDiagnosticPage{}, ErrCursor
	}
	entries, w, err := s.selectUnified(ctx, f, limit+1, token, false)
	if err != nil {
		return UnifiedDiagnosticPage{}, err
	}
	page := UnifiedDiagnosticPage{Entries: make([]DiagnosticReceiptRow, 0, min(len(entries), limit))}
	if len(entries) > limit {
		last := entries[limit-1].key
		w.After = &last
		page.Next, err = encodeUnified(w)
		if err != nil {
			return UnifiedDiagnosticPage{}, err
		}
		entries = entries[:limit]
	}
	for _, v := range entries {
		page.Entries = append(page.Entries, v.row)
	}
	w.After = nil
	page.Snapshot, err = encodeUnified(w)
	return page, err
}

func (s *Store) ExportReceipts(ctx context.Context, f Filter, token string) ([]DiagnosticReceiptRow, error) {
	entries, _, err := s.selectUnified(ctx, f, MaxDiagnosticExport+1, token, true)
	if err != nil {
		return nil, err
	}
	if len(entries) > MaxDiagnosticExport {
		return nil, ErrDiagnosticExportLimit
	}
	rows := make([]DiagnosticReceiptRow, 0, len(entries))
	for _, v := range entries {
		rows = append(rows, v.row)
	}
	return rows, nil
}

func (s *Store) selectUnified(ctx context.Context, f Filter, limit int, token string, whole bool) ([]unifiedEntry, unifiedWindow, error) {
	var w unifiedWindow
	if !f.Valid() {
		return nil, w, ErrCursor
	}
	f = normalizedDiagnosticFilter(f)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.policy()
	if err != nil {
		return nil, w, err
	}
	p = cappedDiagnosticPolicy(p)
	now := s.now().UTC()
	age := time.Duration(p.RetentionDays) * 24 * time.Hour
	cut := now.Add(-age)
	root, files, signature, err := s.legacyFiles()
	if err != nil {
		return nil, w, err
	}
	if root != nil {
		defer root.Close()
	}
	if token == "" {
		w = unifiedWindow{Days: p.RetentionDays, At: now.UnixMilli(), Expires: now.Add(10 * time.Minute).UnixMilli(), Filter: f, Legacy: signature}
	} else {
		w, err = decodeUnified(token)
		if err != nil || w.Filter != f || w.Legacy != signature || w.Days != p.RetentionDays ||
			now.UnixMilli() < w.At || now.UnixMilli() >= w.Expires || whole && w.After != nil {
			return nil, w, ErrCursor
		}
	}
	legacy := make(unifiedMax, 0, min(limit, 100))
	for _, file := range files {
		err = file.visit(ctx, root, func(r Record, _ []byte) error {
			if !r.ReceivedAt.After(cut) {
				return nil
			}
			k := unifiedKey{Sec: r.Time.Unix(), Nano: r.Time.Nanosecond(), Owner: r.UserID, Mode: r.Mode,
				Device: receiptKey("device", r.DeviceID), Browser: receiptKey("browser", r.BrowserID),
				Launch: strings.ToLower(r.LaunchID), Seq: r.Seq, File: file.id}
			if !matchesUnifiedFilter(f, k) {
				return nil
			}
			w.Expires = min(w.Expires, r.ReceivedAt.Add(age).UnixMilli())
			if w.After != nil && compareUnified(k, *w.After) <= 0 {
				return nil
			}
			meta := r.Session
			meta.DeviceID = maskedDiagnosticID(k.Device)
			meta.BrowserID = maskedDiagnosticID(k.Browser)
			meta.LaunchID = k.Launch
			row := DiagnosticReceiptRow{Session: meta, Time: r.Time.UTC().Format(time.RFC3339Nano), ReceivedAt: r.ReceivedAt.UTC().Format(time.RFC3339Nano),
				Level: r.Level, Event: r.Event, DurationMS: r.DurationMS, Count: r.Count, UserID: r.UserID, Seq: r.Seq}
			legacy.offer(unifiedEntry{row, k}, limit)
			return nil
		})
		if err != nil {
			return nil, w, err
		}
	}
	journal, err := s.receiptJournal(p).unifiedReceipts(ctx, &w, limit, cut, token != "")
	if err != nil {
		return nil, w, err
	}
	out := append([]unifiedEntry(legacy), journal...)
	sort.Slice(out, func(i, j int) bool { return compareUnified(out[i].key, out[j].key) < 0 })
	return out[:min(limit, len(out))], w, nil
}

func (j *ReceiptJournal) unifiedReceipts(ctx context.Context, w *unifiedWindow, limit int, cut time.Time, existing bool) ([]unifiedEntry, error) {
	if existing && w.Database == "" {
		return nil, nil
	}
	path, err := j.path(false)
	if errors.Is(err, os.ErrNotExist) {
		if existing {
			return nil, ErrCursor
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	db, err := openReceiptSQLite(path, url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(2000)"}})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if ready, e := checkReceiptDB(ctx, tx); e != nil {
		return nil, e
	} else if !ready {
		return nil, ErrReceiptPath
	}
	var id string
	var water int64
	if e := tx.QueryRowContext(ctx, "SELECT hex(id),(SELECT coalesce(max(id),0) FROM receipt_segments) FROM receipt_identity WHERE singleton=1").Scan(&id, &water); e != nil {
		return nil, e
	}
	if existing {
		if w.Database != id || w.Watermark > water {
			return nil, ErrCursor
		}
	} else {
		w.Database = id
		w.Watermark = water
	}
	var oldest sql.NullInt64
	if e := tx.QueryRowContext(ctx, "SELECT min(received_ms) FROM receipt_segments WHERE id<=? AND received_ms>?", w.Watermark, cut.UnixMilli()).Scan(&oldest); e != nil {
		return nil, e
	}
	if oldest.Valid {
		w.Expires = min(w.Expires, oldest.Int64+int64(w.Days)*86400000)
	}

	where := []string{"e.segment_id<=?", "s.received_ms>?"}
	args := []any{w.Watermark, w.Watermark, cut.UnixMilli()}
	if w.Filter.UserID != 0 {
		where = append(where, "e.owner=?")
		args = append(args, w.Filter.UserID)
	}
	for _, x := range []struct{ col, val string }{{"e.mode", w.Filter.Mode}, {"e.device", w.Filter.DeviceID}, {"e.browser", w.Filter.BrowserID}, {"e.launch", w.Filter.LaunchID}} {
		if x.val != "" {
			where = append(where, x.col+"=?")
			args = append(args, x.val)
		}
	}
	if a := w.After; a != nil {
		ms := a.Sec*1000 + int64(a.Nano)/1000000
		if a.Nano%1000000 != 0 {
			where = append(where, "e.event_ms>?")
			args = append(args, ms)
		} else {
			where = append(where, "(e.event_ms,e.owner,e.mode,e.device,e.browser,e.launch,1,e.event_seq,e.seq)>(?,?,?,?,?,?,?,?,?)")
			args = append(args, ms, a.Owner, a.Mode, a.Device, a.Browser, a.Launch, a.Source, a.Ordinal, a.Seq)
		}
	}
	q := `SELECT e.seq,e.owner,e.mode,e.device,e.browser,e.launch,e.event_seq,e.event_ms,e.level,e.event,e.duration_ms,e.count,e.version,s.received_ms,
	EXISTS(SELECT 1 FROM receipt_events p WHERE p.owner=e.owner AND p.mode=e.mode AND p.device=e.device AND p.browser=e.browser
		AND p.launch=e.launch AND p.segment_id<=? AND p.event_seq<e.event_seq AND p.event_ms>e.event_ms)
	FROM receipt_events e JOIN receipt_segments s ON s.id=e.segment_id WHERE ` + strings.Join(where, " AND ") +
		" ORDER BY e.event_ms,e.owner,e.mode,e.device,e.browser,e.launch,e.event_seq,e.seq LIMIT ?"
	rows, err := tx.QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]unifiedEntry, 0, min(limit, 100))
	for rows.Next() {
		var row DiagnosticReceiptRow
		var k unifiedKey
		var event, received int64
		if err = rows.Scan(&k.Seq, &k.Owner, &k.Mode, &k.Device, &k.Browser, &k.Launch, &k.Ordinal, &event, &row.Level, &row.Event,
			&row.DurationMS, &row.Count, &row.Version, &received, &row.ClockRegressed); err != nil {
			return nil, err
		}
		tm := time.UnixMilli(event)
		k.Sec, k.Nano, k.Source = tm.Unix(), tm.Nanosecond(), 1
		row.UserID, row.Mode, row.Seq, row.LaunchID = k.Owner, k.Mode, k.Seq, k.Launch
		row.DeviceID, row.BrowserID = maskedDiagnosticID(k.Device), maskedDiagnosticID(k.Browser)
		row.Time, row.ReceivedAt = journalTime(event), journalTime(received)
		out = append(out, unifiedEntry{row, k})
	}
	return out, rows.Err()
}
