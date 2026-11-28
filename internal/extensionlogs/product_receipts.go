package extensionlogs

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// Receipt views expose only the approved diagnostic fields and a derived flag.
// The device/browser keys are pseudonyms and the wire IDs stay in the journal.
type DiagnosticReceiptRow struct {
	Session
	Time           string `json:"time"`
	Level          string `json:"level"`
	Event          string `json:"event"`
	DurationMS     *int64 `json:"duration_ms,omitempty"`
	Count          *int64 `json:"count,omitempty"`
	UserID         int32  `json:"user_id"`
	ReceivedAt     string `json:"received_at"`
	Seq            int64  `json:"seq,string"`
	ClockRegressed bool   `json:"clock_regressed,omitempty"`
}

func (s *Store) receiptJournal(p Policy) *ReceiptJournal {
	// Older on-disk policy values may predate the approved caps.
	age := time.Duration(min(p.RetentionDays, 7)) * 24 * time.Hour
	j := NewReceiptJournal(s.dir, ReceiptOptions{Enabled: true, MaxAge: age})
	j.folder = ".diagnostic-receipts"
	j.now = s.now
	return j
}

func (s *Store) AppendReceipt(ctx context.Context, owner int32, meta Session, batch ReceiptBatch) (ReceiptProof, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.policy()
	if err != nil {
		return ReceiptProof{}, err
	}
	if !p.Enabled {
		return ReceiptProof{}, ErrDisabled
	}
	if _, err = receiptBatch(meta, batch); err != nil || owner <= 0 {
		return ReceiptProof{}, ErrBatch
	}
	if err = os.Mkdir(s.dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ReceiptProof{}, err
	}
	j := s.receiptJournal(p)
	if _, err = j.Append(ctx, owner, meta, batch); err != nil {
		return ReceiptProof{}, err
	}
	// v1 is read-only after the upgrade. Its files are older than the new
	// journal; reclaim them first if its combined steady-state size needs it.
	if err = s.sweepReceiptBudget(p, s.now().UTC(), false); err != nil {
		return ReceiptProof{}, err
	}
	return j.LookupReceiptProof(ctx, owner, meta, batch)
}

func (s *Store) sweepReceiptBudget(p Policy, now time.Time, force bool) error {
	p = cappedDiagnosticPolicy(p)
	j := s.receiptJournal(p)
	path, err := j.path(false)
	if errors.Is(err, os.ErrNotExist) {
		return s.prune(p, now, 0, force)
	}
	if err != nil {
		return err
	}
	if err = s.load(); err != nil {
		return err
	}
	if force {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = j.sweepExpired(ctx); err != nil {
			return err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return s.prune(p, now, info.Size(), force)
}

func (j *ReceiptJournal) sweepExpired(ctx context.Context) error {
	db, err := j.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ready, err := checkReceiptDB(ctx, tx)
	if err != nil || !ready {
		return err
	}
	if err = j.pruneExpired(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (j *ReceiptJournal) pruneExpired(ctx context.Context, tx *sql.Tx) error {
	if j.opts.MaxAge <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	cut := j.now().Add(-j.opts.MaxAge).UnixMilli()
	// Read visibility uses the same strict received-time cutoff even between
	// cleanup runs. Secure-delete zeroes what used to hold deleted values.
	if _, err := tx.ExecContext(ctx, "DELETE FROM receipt_events WHERE segment_id IN (SELECT id FROM receipt_segments WHERE received_ms<=?)", cut); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, "DELETE FROM receipt_segments WHERE received_ms<=?", cut)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		// An offset against the previous generation must not return a partial
		// page/export after an earlier row has been evicted.
		_, err = tx.ExecContext(ctx, "UPDATE receipt_identity SET id=randomblob(16) WHERE singleton=1")
		return err
	}
	return nil
}

type receiptCursorWire struct {
	DB                            string `json:"d"`
	Owner                         int32  `json:"u"`
	Mode, Device, Browser, Launch string
	Watermark                     int64 `json:"w"`
	Offset                        int   `json:"o"`
	Expiry                        int64 `json:"x"`
	Age                           int64 `json:"a"`
}

var receiptCursorKey = func() [32]byte {
	var k [32]byte
	_, _ = rand.Read(k[:])
	return k
}()

func cursorSignature(b []byte) []byte {
	h := hmac.New(sha256.New, receiptCursorKey[:])
	_, _ = h.Write(b)
	return h.Sum(nil)
}
func encodeReceiptCursor(c *ReceiptCursor) (string, error) {
	if c == nil {
		return "", nil
	}
	k := c.filter
	b, err := json.Marshal(receiptCursorWire{c.database, k.owner, k.mode, k.device, k.browser, k.launch, c.watermark, c.offset, c.expiresAt, c.age})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(cursorSignature(b)), nil
}
func decodeReceiptCursor(token string) (*ReceiptCursor, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) > 1400 {
		return nil, ErrCursor
	}
	a, b, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrCursor
	}
	body, e := base64.RawURLEncoding.DecodeString(a)
	signature, f := base64.RawURLEncoding.DecodeString(b)
	if e != nil || f != nil || !hmac.Equal(signature, cursorSignature(body)) {
		return nil, ErrCursor
	}
	var x receiptCursorWire
	if json.Unmarshal(body, &x) != nil {
		return nil, ErrCursor
	}
	return &ReceiptCursor{x.DB, sessionKey{x.Owner, x.Mode, x.Device, x.Browser, x.Launch}, x.Watermark, x.Offset, x.Expiry, x.Age}, nil
}
func (s *Store) ReadReceipts(ctx context.Context, f Filter, limit int, token string) ([]DiagnosticReceiptRow, string, error) {
	cursor, err := decodeReceiptCursor(token)
	if err != nil {
		return nil, "", err
	}
	p, err := s.EffectivePolicy()
	if err != nil {
		return nil, "", err
	}
	page, err := s.receiptJournal(p).Read(ctx, f, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	next, err := encodeReceiptCursor(page.Next)
	if err != nil {
		return nil, "", err
	}
	rows := make([]DiagnosticReceiptRow, 0, len(page.Entries))
	for _, e := range page.Entries {
		browser := ""
		if e.BrowserKey != "" {
			browser = "h:" + e.BrowserKey
		}
		rows = append(rows, DiagnosticReceiptRow{
			Session: Session{Mode: e.Mode, BrowserID: browser, Version: e.Version, DeviceID: "h:" + e.DeviceKey, LaunchID: e.LaunchID},
			Time:    e.Time, Level: e.Level, Event: e.Event, DurationMS: e.DurationMS, Count: e.Count,
			UserID: e.OwnerID, ReceivedAt: e.ReceivedAt, Seq: e.Seq, ClockRegressed: e.ClockRegressed,
		})
	}
	return rows, next, nil
}
