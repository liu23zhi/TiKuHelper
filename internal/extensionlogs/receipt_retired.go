package extensionlogs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

// A capacity eviction is deliberate removal of a diagnostic payload, not
// loss of its acknowledgement. Retain only compact, scope-qualified hashes
// until the same receive-time window as the original receipt expires.
const retiredReceiptSchema = `
CREATE TABLE IF NOT EXISTS receipt_retired_events (
 event_key BLOB PRIMARY KEY NOT NULL,
 seq_key BLOB UNIQUE NOT NULL,
 digest BLOB NOT NULL,
 segment_id INTEGER NOT NULL REFERENCES receipt_segments(id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS receipt_retired_origin ON receipt_retired_events(segment_id);
`

func retiredKeys(k sessionKey, event string, seq int64) ([]byte, []byte) {
	scope := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00", k.owner, k.mode, k.device, k.browser, k.launch)
	e := sha256.Sum256([]byte("diagnostic-retired-event\x00" + scope + event))
	n := sha256.Sum256([]byte(fmt.Sprintf("diagnostic-retired-seq\x00%s%d", scope, seq)))
	return e[:], n[:]
}

func retiredProof(ctx context.Context, tx *sql.Tx, k sessionKey, e validatedReceiptEvent, watermark int64) (int64, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	if version < receiptSchemaVersion {
		return 0, sql.ErrNoRows
	}
	id, order := retiredKeys(k, e.EventID, e.EventSeq)
	var origin int64
	err := tx.QueryRowContext(ctx, `
		SELECT r.segment_id FROM receipt_retired_events r
		JOIN receipt_segments s ON s.id=r.segment_id
		WHERE r.event_key=? AND r.seq_key=? AND r.digest=? AND r.segment_id<=?
		  AND (s.owner,s.mode,s.device,s.browser,s.launch)=(?,?,?,?,?)`,
		id, order, e.digest[:], watermark, k.owner, k.mode, k.device, k.browser, k.launch).Scan(&origin)
	return origin, err
}

func (j *ReceiptJournal) retirePayloads(ctx context.Context, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	db, err := j.open(false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = initReceiptDB(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	for _, seq := range seqs {
		var k sessionKey
		var id string
		var ordinal, origin int64
		var digest []byte
		err = tx.QueryRowContext(ctx, `SELECT owner,mode,device,browser,launch,event_id,event_seq,digest,segment_id
			FROM receipt_events WHERE seq=?`, seq).Scan(&k.owner, &k.mode, &k.device, &k.browser, &k.launch, &id, &ordinal, &digest, &origin)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCursor
		}
		if err != nil {
			return err
		}
		if len(digest) != sha256.Size {
			return ErrReceiptPath
		}
		e, n := retiredKeys(k, id, ordinal)
		if _, err = tx.ExecContext(ctx, "INSERT INTO receipt_retired_events(event_key,seq_key,digest,segment_id) VALUES(?,?,?,?)", e, n, digest, origin); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM receipt_events WHERE seq=?", seq); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE receipt_identity SET id=randomblob(16) WHERE singleton=1"); err != nil {
		return err
	}
	return tx.Commit()
}
