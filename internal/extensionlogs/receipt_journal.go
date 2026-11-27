package extensionlogs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite" // repository's existing SQLite driver
)

const (
	receiptTimeFormat    = "2006-01-02T15:04:05.000Z"
	receiptFolder        = ".receipt-journal-v2"
	receiptFilename      = "journal.sqlite"
	maxReceiptRows       = 100000
	receiptAppID         = 0x54494b38 // "TIK8"
	receiptSchemaVersion = 1
)

var (
	ErrReceiptConflict = errors.New("receipt or event identifier conflicts with committed content")
	ErrReceiptCapacity = errors.New("receipt journal capacity exhausted")
	ErrReceiptPath     = errors.New("receipt journal requires its own private storage path")
)

// ReceiptJournal is a disconnected, default-off prototype. It does not share
// v1 policy, auth, files, retention, cursors, routes or background tasks. The
// future adapter must supply already authorized owner/session metadata and
// enforce consent before calling Append; a receipt is not an access grant.
type ReceiptJournal struct {
	root string
	opts ReceiptOptions
	now  func() time.Time
}

type ReceiptOptions struct {
	Enabled               bool
	MaxEvents, MaxBatches int // zero means the prototype default, not unlimited
}

type ReceiptEvent struct {
	EventID    string `json:"event_id"`
	EventSeq   int64  `json:"event_seq"`
	Time       string `json:"time"`
	Level      string `json:"level"`
	Event      string `json:"event"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	Count      *int64 `json:"count,omitempty"`
}

// ReceiptBatch contains only identifiers and v1-allowlisted event fields; no
// owner, request headers, raw messages, arbitrary properties or content.
type ReceiptBatch struct {
	BatchID string         `json:"batch_id"`
	Entries []ReceiptEvent `json:"entries"`
}

type Receipt struct {
	BatchID    string `json:"batch_id"`
	Accepted   int    `json:"accepted"`
	Stored     int    `json:"stored"`
	ReceivedAt string `json:"received_at"`
}

// ReceiptProof is a local committed receipt and its accepted IDs in batch
// order. It neither grants access nor defines a wire format.
type ReceiptProof struct {
	Receipt
	AcceptedEventIDs []string
	Scope            ReceiptProofScope
}

// ReceiptProofScope is the persisted scope: hashed device/browser keys and a
// canonical launch UUID, never the raw device/browser identifiers.
type ReceiptProofScope struct {
	OwnerID                               int32
	Mode, DeviceKey, BrowserKey, LaunchID string
}

type ReceiptRecord struct {
	ReceiptEvent
	OwnerID        int32  `json:"user_id"`
	Mode           string `json:"mode"`
	DeviceKey      string `json:"device_key"`
	BrowserKey     string `json:"browser_key,omitempty"`
	LaunchID       string `json:"launch_id"`
	Version        string `json:"version"`
	ReceivedAt     string `json:"received_at"`
	ClockRegressed bool   `json:"clock_regressed"`
}

type ReceiptPage struct {
	Entries []ReceiptRecord
	Next    *ReceiptCursor `json:"-"`
}

// Private typed cursor, bound to a database generation and a filter. A
// later HTTP adapter must design its own authorized cursor representation.
type ReceiptCursor struct {
	database  string
	filter    sessionKey
	watermark int64
	offset    int
}

func NewReceiptJournal(logRoot string, opts ReceiptOptions) *ReceiptJournal {
	if opts.MaxEvents == 0 {
		opts.MaxEvents = maxReceiptRows
	}
	if opts.MaxBatches == 0 {
		opts.MaxBatches = maxReceiptRows
	}
	return &ReceiptJournal{root: logRoot, opts: opts, now: time.Now}
}

// This is a local, owner-controlled filesystem path, not a request field.
// Reject symlinks at the journal's own boundaries; the configured parent
// and its ancestors are trusted and must not be replaced concurrently.
func (j *ReceiptJournal) path(create bool) (string, error) {
	if j.root == "" {
		return "", ErrReceiptPath
	}
	root, err := filepath.Abs(j.root)
	if err != nil {
		return "", err
	}
	for i, dir := range []string{root, filepath.Join(root, "client"), filepath.Join(root, "client", receiptFolder)} {
		fi, err := os.Lstat(dir)
		if i > 0 && errors.Is(err, os.ErrNotExist) && create {
			if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			fi, err = os.Lstat(dir)
		}
		if err != nil {
			return "", err
		}
		if !fi.IsDir() || fi.Mode().Perm()&0022 != 0 || (i == 2 && fi.Mode().Perm()&0077 != 0) {
			return "", ErrReceiptPath
		}
	}
	path := filepath.Join(root, "client", receiptFolder, receiptFilename)
	fi, err := os.Lstat(path)
	if create && errors.Is(err, os.ErrNotExist) {
		f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if e == nil {
			if e = f.Close(); e != nil {
				return "", e
			}
		} else if !errors.Is(e, os.ErrExist) {
			return "", e
		}
		fi, err = os.Lstat(path)
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return "", ErrReceiptPath
	}
	return path, nil
}

func (j *ReceiptJournal) open(create bool) (*sql.DB, error) {
	path, err := j.path(create)
	if err != nil {
		return nil, err
	}
	return openReceiptSQLite(path, url.Values{
		"mode":    {"rw"},
		"_txlock": {"immediate"},
		"_pragma": {"busy_timeout(2000)", "foreign_keys(ON)", "synchronous(FULL)", "max_page_count(32768)"},
	})
}

func openReceiptSQLite(path string, q url.Values) (*sql.DB, error) {
	name := filepath.ToSlash(path)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	dsn := (&url.URL{Scheme: "file", Path: name, RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

const receiptSchema = `
CREATE TABLE IF NOT EXISTS receipt_identity (singleton INTEGER PRIMARY KEY CHECK (singleton=1), id BLOB NOT NULL);
INSERT OR IGNORE INTO receipt_identity (singleton,id) VALUES (1,randomblob(16));

CREATE TABLE IF NOT EXISTS receipt_segments (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 owner INTEGER NOT NULL, mode TEXT NOT NULL, device TEXT NOT NULL, browser TEXT NOT NULL, launch TEXT NOT NULL,
 batch_id TEXT NOT NULL, digest BLOB NOT NULL,
 received_ms INTEGER NOT NULL, accepted INTEGER NOT NULL, stored INTEGER NOT NULL,
 UNIQUE (owner,mode,device,browser,launch,batch_id)
);
CREATE TABLE IF NOT EXISTS receipt_events (
 owner INTEGER NOT NULL, mode TEXT NOT NULL, device TEXT NOT NULL, browser TEXT NOT NULL, launch TEXT NOT NULL,
 event_id TEXT NOT NULL, event_seq INTEGER NOT NULL,
 event_ms INTEGER NOT NULL, level TEXT NOT NULL, event TEXT NOT NULL, duration_ms INTEGER, count INTEGER, version TEXT NOT NULL,
 digest BLOB NOT NULL, segment_id INTEGER NOT NULL REFERENCES receipt_segments(id),
 UNIQUE (owner,mode,device,browser,launch,event_id),
 UNIQUE (owner,mode,device,browser,launch,event_seq)
);
CREATE INDEX IF NOT EXISTS receipt_events_order
ON receipt_events(event_ms,owner,mode,device,browser,launch,event_seq,event_id);
`

type receiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Recognize only this journal's schema; never adopt an existing unrelated DB
// or a WAL database whose sidecars aren't part of this prototype's budget.
func checkReceiptDB(ctx context.Context, q receiptQuery) (bool, error) {
	var appID, version int
	var mode string
	for _, probe := range []struct {
		pragma string
		value  any
	}{{"PRAGMA application_id", &appID}, {"PRAGMA user_version", &version}, {"PRAGMA journal_mode", &mode}} {
		if err := q.QueryRowContext(ctx, probe.pragma).Scan(probe.value); err != nil {
			return false, err
		}
	}
	if mode != "delete" || !((appID == 0 && version == 0) || (appID == receiptAppID && version == receiptSchemaVersion)) {
		return false, ErrReceiptPath
	}
	return appID == receiptAppID, nil
}

func initReceiptDB(ctx context.Context, tx *sql.Tx) error {
	ready, err := checkReceiptDB(ctx, tx)
	if err != nil || ready {
		return err
	}
	var objects int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
		return err
	}
	if objects != 0 {
		return ErrReceiptPath
	}
	if _, err := tx.ExecContext(ctx, receiptSchema); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "PRAGMA application_id="+strconv.Itoa(receiptAppID)+"; PRAGMA user_version="+strconv.Itoa(receiptSchemaVersion))
	return err
}

func receiptKey(kind, raw string) string {
	if raw == "" {
		return ""
	}
	d := sha256.Sum256([]byte("tiku-receipts-v2/" + kind + "\x00" + raw))
	return hex.EncodeToString(d[:])
}

func receiptScope(owner int32, meta Session) sessionKey {
	return sessionKey{owner, meta.Mode, receiptKey("device", meta.DeviceID), receiptKey("browser", meta.BrowserID), strings.ToLower(meta.LaunchID)}
}

func journalTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(receiptTimeFormat) }

type validatedReceiptEvent struct {
	ReceiptEvent
	ms     int64
	digest [sha256.Size]byte
}
type validatedReceiptBatch struct {
	id     string
	events []validatedReceiptEvent
	digest [sha256.Size]byte
}

func copyReceiptNumber(n *int64) *int64 {
	if n == nil {
		return nil
	}
	value := *n
	return &value
}

func receiptBatch(meta Session, b ReceiptBatch) (validatedReceiptBatch, error) {
	if !KnownMode(meta.Mode) || !meta.valid() || !launchRE.MatchString(b.BatchID) ||
		len(b.Entries) == 0 || len(b.Entries) > MaxBatch {
		return validatedReceiptBatch{}, ErrBatch
	}
	keyed := ReceiptBatch{BatchID: strings.ToLower(b.BatchID), Entries: make([]ReceiptEvent, 0, len(b.Entries))}
	out := validatedReceiptBatch{id: keyed.BatchID, events: make([]validatedReceiptEvent, 0, len(b.Entries))}
	legacy := Batch{Session: meta, Entries: make([]Entry, 0, len(b.Entries))}
	ids, seqs := map[string]bool{}, map[int64]bool{}
	for _, e := range b.Entries {
		if len(e.Time) != len(receiptTimeFormat) || !launchRE.MatchString(e.EventID) || e.EventSeq <= 0 {
			return validatedReceiptBatch{}, ErrBatch
		}
		when, err := time.Parse(receiptTimeFormat, e.Time)
		if err != nil || when.UTC().Format(receiptTimeFormat) != e.Time {
			return validatedReceiptBatch{}, ErrBatch
		}
		e.EventID = strings.ToLower(e.EventID)
		if ids[e.EventID] || seqs[e.EventSeq] {
			return validatedReceiptBatch{}, ErrBatch
		}
		ids[e.EventID], seqs[e.EventSeq] = true, true
		e.DurationMS, e.Count = copyReceiptNumber(e.DurationMS), copyReceiptNumber(e.Count)
		legacy.Entries = append(legacy.Entries, Entry{when, e.Level, e.Event, e.DurationMS, e.Count})
		keyed.Entries = append(keyed.Entries, e)
		encoded, err := json.Marshal(struct {
			Version string
			ReceiptEvent
		}{meta.Version, e})
		if err != nil {
			return validatedReceiptBatch{}, ErrBatch
		}
		out.events = append(out.events, validatedReceiptEvent{e, when.UnixMilli(), sha256.Sum256(encoded)})
	}
	if !legacy.valid() {
		return validatedReceiptBatch{}, ErrBatch
	}
	encoded, err := json.Marshal(struct {
		Version string
		ReceiptBatch
	}{meta.Version, keyed})
	if err != nil {
		return validatedReceiptBatch{}, ErrBatch
	}
	out.digest = sha256.Sum256(encoded)
	return out, nil
}

// Append commits the original receipt and all new events together. Each
// receipt is a logical batch segment. No eviction: deleting a batch/event
// uniqueness key here would silently reopen the duplicate-write window.
func (j *ReceiptJournal) Append(ctx context.Context, owner int32, meta Session, batch ReceiptBatch) (Receipt, error) {
	if !j.opts.Enabled {
		return Receipt{}, ErrDisabled
	}
	if j.opts.MaxEvents < 1 || j.opts.MaxEvents > maxReceiptRows || j.opts.MaxBatches < 1 || j.opts.MaxBatches > maxReceiptRows {
		return Receipt{}, ErrPolicy
	}
	b, err := receiptBatch(meta, batch)
	if err != nil || owner <= 0 {
		return Receipt{}, ErrBatch
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	db, err := j.open(true)
	if err != nil {
		return Receipt{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback() // also on conflicts, capacity, etc.
	if err := initReceiptDB(ctx, tx); err != nil {
		return Receipt{}, err
	}
	k := receiptScope(owner, meta)
	args := []any{k.owner, k.mode, k.device, k.browser, k.launch}
	const sameScope = "owner=? AND mode=? AND device=? AND browser=? AND launch=?"
	var h []byte
	var received int64
	r := Receipt{BatchID: b.id}
	err = tx.QueryRowContext(ctx, "SELECT digest,received_ms,accepted,stored FROM receipt_segments WHERE "+sameScope+" AND batch_id=?", append(args, b.id)...).
		Scan(&h, &received, &r.Accepted, &r.Stored)
	if err == nil {
		if !bytes.Equal(h, b.digest[:]) {
			return Receipt{}, ErrReceiptConflict
		}
		r.ReceivedAt = journalTime(received)
		return r, nil // deferred rollback releases the read/lock without change
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}

	newEvents := make([]validatedReceiptEvent, 0, len(b.events))
	for _, event := range b.events {
		var digest []byte
		err := tx.QueryRowContext(ctx, "SELECT digest FROM receipt_events WHERE "+sameScope+" AND (event_id=? OR event_seq=?)",
			append(args, event.EventID, event.EventSeq)...).Scan(&digest)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			newEvents = append(newEvents, event)
		case err != nil:
			return Receipt{}, err
		case !bytes.Equal(digest, event.digest[:]):
			return Receipt{}, ErrReceiptConflict
		}
	}
	var events, batches int
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM receipt_events), (SELECT count(*) FROM receipt_segments)").Scan(&events, &batches); err != nil {
		return Receipt{}, err
	}
	if events+len(newEvents) > j.opts.MaxEvents || batches >= j.opts.MaxBatches {
		return Receipt{}, ErrReceiptCapacity
	}
	received = j.now().UnixMilli()
	r.Accepted, r.Stored, r.ReceivedAt = len(b.events), len(newEvents), journalTime(received)
	res, err := tx.ExecContext(ctx, `INSERT INTO receipt_segments
		(owner,mode,device,browser,launch,batch_id,digest,received_ms,accepted,stored)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, append(args, b.id, b.digest[:], received, r.Accepted, r.Stored)...)
	if err != nil {
		return Receipt{}, err
	}
	segment, err := res.LastInsertId()
	if err != nil {
		return Receipt{}, err
	}
	for _, e := range newEvents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO receipt_events
			(owner,mode,device,browser,launch,event_id,event_seq,event_ms,level,event,duration_ms,count,version,digest,segment_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, e.EventID, e.EventSeq, e.ms, e.Level, e.Event, e.DurationMS, e.Count, meta.Version, e.digest[:], segment)...); err != nil {
			return Receipt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// LookupReceiptProof verifies an entire ordered batch against committed
// journal rows in one read-only snapshot. Callers must supply already
// authorized owner/session metadata. No match returns sql.ErrNoRows; a
// mismatched or incomplete proof returns ErrReceiptConflict.
func (j *ReceiptJournal) LookupReceiptProof(ctx context.Context, owner int32, meta Session, batch ReceiptBatch) (ReceiptProof, error) {
	if !j.opts.Enabled {
		return ReceiptProof{}, ErrDisabled
	}
	b, err := receiptBatch(meta, batch)
	if err != nil || owner <= 0 {
		return ReceiptProof{}, ErrBatch
	}
	if err := ctx.Err(); err != nil {
		return ReceiptProof{}, err
	}
	path, err := j.path(false)
	if errors.Is(err, os.ErrNotExist) {
		return ReceiptProof{}, sql.ErrNoRows
	}
	if err != nil {
		return ReceiptProof{}, err
	}
	db, err := openReceiptSQLite(path, url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(2000)"}})
	if err != nil {
		return ReceiptProof{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReceiptProof{}, err
	}
	defer tx.Rollback()
	if ready, err := checkReceiptDB(ctx, tx); err != nil {
		return ReceiptProof{}, err
	} else if !ready {
		return ReceiptProof{}, ErrReceiptPath
	}

	k := receiptScope(owner, meta)
	args := []any{k.owner, k.mode, k.device, k.browser, k.launch}
	var p ReceiptProof
	var segment, received int64
	var digest []byte
	err = tx.QueryRowContext(ctx, `SELECT id,owner,mode,device,browser,launch,batch_id,digest,received_ms,accepted,stored
		FROM receipt_segments WHERE (owner,mode,device,browser,launch)=(?,?,?,?,?) AND batch_id=?`, append(args, b.id)...).
		Scan(&segment, &p.Scope.OwnerID, &p.Scope.Mode, &p.Scope.DeviceKey, &p.Scope.BrowserKey, &p.Scope.LaunchID,
			&p.BatchID, &digest, &received, &p.Accepted, &p.Stored)
	if err != nil {
		return ReceiptProof{}, err
	}
	if !bytes.Equal(digest, b.digest[:]) || p.Accepted != len(b.events) || p.Stored < 0 || p.Stored > p.Accepted {
		return ReceiptProof{}, ErrReceiptConflict
	}

	// The batch digest fixes order; an event may originate in an earlier
	// segment of the same scope (including when this receipt stored zero).
	// Require the actual row and payload, not just its stored digest.
	const eventRow = `SELECT e.event_id,e.segment_id
		FROM receipt_events e JOIN receipt_segments origin ON origin.id=e.segment_id
		AND (origin.owner,origin.mode,origin.device,origin.browser,origin.launch)=(e.owner,e.mode,e.device,e.browser,e.launch)
		WHERE (e.owner,e.mode,e.device,e.browser,e.launch)=(?,?,?,?,?)
		AND e.event_id=? AND e.event_seq=? AND e.digest=? AND e.segment_id<=?
		AND e.event_ms=? AND e.level=? AND e.event=? AND e.duration_ms IS ? AND e.count IS ? AND e.version=?`
	ids := make([]string, 0, p.Accepted)
	stored := 0
	for _, e := range b.events {
		var id string
		var origin int64
		err := tx.QueryRowContext(ctx, eventRow, append(args, e.EventID, e.EventSeq, e.digest[:], segment,
			e.ms, e.Level, e.Event, e.DurationMS, e.Count, meta.Version)...).Scan(&id, &origin)
		if errors.Is(err, sql.ErrNoRows) {
			return ReceiptProof{}, ErrReceiptConflict
		}
		if err != nil {
			return ReceiptProof{}, err
		}
		if origin == segment {
			stored++
		}
		ids = append(ids, id)
	}
	if stored != p.Stored {
		return ReceiptProof{}, ErrReceiptConflict
	}
	p.ReceivedAt, p.AcceptedEventIDs = journalTime(received), ids
	return p, nil
}

// Read applies the full logical-segment merge before LIMIT/OFFSET. New
// commits, including delayed earlier events, never perturb an existing
// cursor: that cursor is bounded to the watermark of its original read.
func (j *ReceiptJournal) Read(ctx context.Context, f Filter, limit int, cursor *ReceiptCursor) (ReceiptPage, error) {
	if !j.opts.Enabled {
		return ReceiptPage{}, ErrDisabled
	}
	if !f.Valid() || limit < 1 || limit > 100 {
		return ReceiptPage{}, ErrCursor
	}
	filter := sessionKey{f.UserID, f.Mode, receiptKey("device", f.DeviceID), receiptKey("browser", f.BrowserID), strings.ToLower(f.LaunchID)}
	page := ReceiptPage{Entries: []ReceiptRecord{}}
	db, err := j.open(false)
	if errors.Is(err, os.ErrNotExist) {
		if cursor != nil {
			return ReceiptPage{}, ErrCursor
		}
		return page, nil
	}
	if err != nil {
		return ReceiptPage{}, err
	}
	defer db.Close()
	if ready, err := checkReceiptDB(ctx, db); err != nil {
		return ReceiptPage{}, err
	} else if !ready {
		return ReceiptPage{}, ErrReceiptPath
	}
	var watermark int64
	var database string
	if err := db.QueryRowContext(ctx, `SELECT hex(id), (SELECT coalesce(max(id),0) FROM receipt_segments)
		FROM receipt_identity WHERE singleton=1`).Scan(&database, &watermark); err != nil {
		return ReceiptPage{}, err
	}
	var offset int
	if cursor != nil {
		if cursor.database != database || cursor.filter != filter || cursor.watermark > watermark ||
			cursor.watermark <= 0 || cursor.offset < 1 || cursor.offset > maxReceiptRows {
			return ReceiptPage{}, ErrCursor
		}
		watermark, offset = cursor.watermark, cursor.offset
	}
	where := []string{"e.segment_id<=?"}
	args := []any{watermark, watermark}
	if filter.owner != 0 {
		where = append(where, "e.owner=?")
		args = append(args, filter.owner)
	}
	for _, field := range []struct{ col, val string }{
		{"mode", filter.mode}, {"device", filter.device}, {"browser", filter.browser}, {"launch", filter.launch},
	} {
		if field.val != "" {
			where = append(where, "e."+field.col+"=?")
			args = append(args, field.val)
		}
	}
	// Regression is only relative to an earlier observed event_seq of this
	// stream. Delay between event time and server receipt isn't clock evidence.
	q := `SELECT e.owner,e.mode,e.device,e.browser,e.launch,e.event_id,e.event_seq,e.event_ms,e.level,e.event,e.duration_ms,e.count,e.version,s.received_ms,
		EXISTS(SELECT 1 FROM receipt_events p WHERE p.owner=e.owner AND p.mode=e.mode
			AND p.device=e.device AND p.browser=e.browser AND p.launch=e.launch AND p.segment_id<=?
			AND p.event_seq<e.event_seq AND p.event_ms>e.event_ms)
		FROM receipt_events e JOIN receipt_segments s ON s.id=e.segment_id WHERE ` + strings.Join(where, " AND ") +
		" ORDER BY e.event_ms,e.owner,e.mode,e.device,e.browser,e.launch,e.event_seq,e.event_id LIMIT ? OFFSET ?"
	rows, err := db.QueryContext(ctx, q, append(args, limit+1, offset)...)
	if err != nil {
		return ReceiptPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ReceiptRecord
		var event, received int64
		if err := rows.Scan(&e.OwnerID, &e.Mode, &e.DeviceKey, &e.BrowserKey, &e.LaunchID, &e.EventID, &e.EventSeq, &event, &e.Level,
			&e.Event, &e.DurationMS, &e.Count, &e.Version, &received, &e.ClockRegressed); err != nil {
			return ReceiptPage{}, err
		}
		e.Time, e.ReceivedAt = journalTime(event), journalTime(received)
		page.Entries = append(page.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return ReceiptPage{}, err
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.Next = &ReceiptCursor{database, filter, watermark, offset + limit}
	}
	return page, nil
}
