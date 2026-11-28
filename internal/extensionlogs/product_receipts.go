package extensionlogs

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
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
	j.physicalCap = int64(min(p.MaxGB, 10)) * s.unitBytes
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
	// Previously durable confirmations do not depend on headroom. In
	// particular an evicted payload can still prove an exact lost response.
	proof, err := j.LookupReceiptProof(ctx, owner, meta, batch)
	if err == nil {
		return proof, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReceiptProof{}, err
	}
	if err = s.sweepReceiptBudget(p, s.now().UTC(), false); err != nil {
		return ReceiptProof{}, err
	}
	if _, err = j.Append(ctx, owner, meta, batch); err != nil {
		return ReceiptProof{}, err
	}
	if err = s.sweepReceiptBudget(p, s.now().UTC(), false); err != nil {
		return ReceiptProof{}, err
	}
	return j.LookupReceiptProof(ctx, owner, meta, batch)
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
	if err = initReceiptDB(ctx, tx); err != nil {
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM receipt_retired_events WHERE segment_id IN (SELECT id FROM receipt_segments WHERE received_ms<=?)", cut); err != nil {
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
func (s *Store) ReadReceipts(ctx context.Context, f Filter, limit int, token string) ([]DiagnosticReceiptRow, string, error) {
	page, err := s.ReadUnified(ctx, f, limit, token)
	return page.Entries, page.Next, err
}
