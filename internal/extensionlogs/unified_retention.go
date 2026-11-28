package extensionlogs

import (
	"cmp"
	"container/heap"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func legacyBytes(files []legacyFile) int64 {
	var n int64
	for _, f := range files {
		n += f.info.Size()
	}
	return n
}

func (f legacyFile) keep(ctx context.Context, root *os.Root, keep func(Record) bool) error {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(f.name), retainedPrefix+hex.EncodeToString(suffix[:]))
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	defer out.Close()
	var written int64
	err = f.visitRows(ctx, root, true, func(r Record, raw []byte) error {
		if r.ReceivedAt.IsZero() || !keep(r) {
			return nil
		}
		n, e := out.Write(raw)
		if e != nil {
			return e
		}
		if n != len(raw) {
			return io.ErrShortWrite
		}
		written += int64(n)
		return nil
	})
	if err != nil {
		return err
	}
	if written == f.info.Size() {
		return nil
	}
	if written != 0 {
		if err = out.Sync(); err != nil {
			return err
		}
	}
	if err = out.Close(); err != nil {
		return err
	}
	current, err := root.Lstat(f.name)
	if err != nil || !os.SameFile(current, f.info) || current.Size() != f.info.Size() ||
		!current.ModTime().Equal(f.info.ModTime()) {
		return errLegacyChanged
	}
	if written == 0 {
		return root.Remove(f.name)
	}
	return root.Rename(tmp, f.name)
}

type retentionEntry struct {
	at          time.Time
	file        legacyFile
	seq, weight int64
}

func compareRetention(a, b retentionEntry) int {
	if n := a.at.Compare(b.at); n != 0 {
		return n
	}
	if (a.file.name == "") != (b.file.name == "") {
		if a.file.name == "" {
			return 1
		}
		return -1
	}
	return cmp.Or(cmp.Compare(a.seq, b.seq), cmp.Compare(a.file.name, b.file.name))
}

type retentionMax []retentionEntry

func (h retentionMax) Len() int           { return len(h) }
func (h retentionMax) Less(i, j int) bool { return compareRetention(h[i], h[j]) > 0 }
func (h retentionMax) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *retentionMax) Push(x any)        { *h = append(*h, x.(retentionEntry)) }
func (h *retentionMax) Pop() any {
	a := *h
	n := len(a) - 1
	v := a[n]
	a[n] = retentionEntry{}
	*h = a[:n]
	return v
}
func (h *retentionMax) offer(x retentionEntry, n int) {
	if len(*h) < n {
		heap.Push(h, x)
		return
	}
	if compareRetention(x, (*h)[0]) < 0 {
		(*h)[0] = x
		heap.Fix(h, 0)
	}
}

func (j *ReceiptJournal) physicalSize() (int64, error) {
	path, err := j.path(false)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (j *ReceiptJournal) oldestPayloads(ctx context.Context, n int) ([]retentionEntry, error) {
	db, err := j.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT e.seq,s.received_ms
		FROM receipt_events e JOIN receipt_segments s ON s.id=e.segment_id
		ORDER BY s.received_ms,e.seq LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []retentionEntry
	for rows.Next() {
		var a retentionEntry
		var at int64
		if err = rows.Scan(&a.seq, &at); err != nil {
			return nil, err
		}
		a.at = time.UnixMilli(at).UTC()
		a.weight = 192 // planning estimate; remeasure actual disk use after reclaiming
		out = append(out, a)
	}
	return out, rows.Err()
}

func (j *ReceiptJournal) compactReceipt(ctx context.Context, keepIfFull bool) error {
	db, err := j.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer db.Close()
	var mode, free int
	if err = db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return err
	}
	if free == 0 && keepIfFull {
		return nil
	}
	if mode != 2 {
		// Older databases have no pointer maps. Convert them only when quota
		// reclamation is needed. This preserves both rows and receipt identity.
		if _, err = db.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, "VACUUM")
		return err
	}
	if free == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, "PRAGMA incremental_vacuum")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

func (j *ReceiptJournal) oldestReceiptTime(ctx context.Context) (time.Time, error) {
	db, err := j.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	defer db.Close()
	ready, err := checkReceiptDB(ctx, db)
	if err != nil || !ready {
		return time.Time{}, err
	}
	var ms sql.NullInt64
	if err = db.QueryRowContext(ctx, "SELECT min(received_ms) FROM receipt_segments").Scan(&ms); err != nil {
		return time.Time{}, err
	}
	if !ms.Valid {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms.Int64).UTC(), nil
}

func notifyEarlier(next *time.Time, r, d time.Time) {
	if !r.IsZero() && r.Before(d) && (next.IsZero() || r.Before(*next)) {
		*next = r
	}
}

func (s *Store) sweepReceiptBudget(p Policy, now time.Time, force bool) error {
	p = cappedDiagnosticPolicy(p)
	j := s.receiptJournal(p)
	budget := int64(p.MaxGB) * s.unitBytes
	journal, err := j.physicalSize()
	if err != nil {
		return err
	}
	// Use the cached byte count only as a hot-path hint. No legacy payload
	// parsing can prevent the independent receipt cleanup.
	if !force && now.Before(s.nextSweep) && s.legacyBudgetBytes+journal < budget {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err = j.sweepExpired(ctx); err != nil {
		return err
	}
	age := time.Duration(p.RetentionDays) * 24 * time.Hour
	cut := now.Add(-age)
	root, files, _, err := s.collectLegacyFiles(true)
	if err != nil {
		return err
	}
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	var next time.Time
	oldestLimit := now.Add(time.Hour)
	s.legacyBudgetBytes = legacyBytes(files)
	if !force && s.legacyBudgetBytes+journal < budget {
		// Ingestion below quota need not parse malformed legacy history;
		// the scheduled cleanup below handles it independently.
		receiptTime, e := j.oldestReceiptTime(ctx)
		if e != nil {
			return e
		}
		if s.nextSweep.IsZero() || !now.Before(s.nextSweep) {
			s.nextSweep = oldestLimit
		}
		if at := receiptTime.Add(age); !receiptTime.IsZero() && at.Before(s.nextSweep) {
			s.nextSweep = at
		}
		return nil
	}
	for _, f := range files {
		expired := false
		err = f.visitRows(ctx, root, true, func(r Record, _ []byte) error {
			if !r.ReceivedAt.After(cut) {
				expired = true
			} else {
				notifyEarlier(&next, r.ReceivedAt.Add(age), oldestLimit)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if expired {
			s.files = nil
			if err = f.keep(ctx, root, func(r Record) bool { return r.ReceivedAt.After(cut) }); err != nil {
				return err
			}
		}
	}
	receiptTime, err := j.oldestReceiptTime(ctx)
	if err != nil {
		return err
	}
	if !receiptTime.IsZero() {
		notifyEarlier(&next, receiptTime.Add(age), oldestLimit)
	}
	if next.IsZero() {
		next = oldestLimit
	}
	s.nextSweep = next
	refresh := func() error {
		if root != nil {
			_ = root.Close()
		}
		root, files, _, err = s.legacyFiles()
		if err != nil {
			return err
		}
		s.legacyBudgetBytes = legacyBytes(files)
		journal, err = j.physicalSize()
		return err
	}
	if err = refresh(); err != nil {
		return err
	}
	if legacyBytes(files)+journal < budget {
		return nil
	}
	if err = j.compactReceipt(ctx, true); err != nil {
		return err
	}
	if journal, err = j.physicalSize(); err != nil {
		return err
	}
	for excess := legacyBytes(files) + journal - budget + 1; excess > 0; excess = legacyBytes(files) + journal - budget + 1 {
		// Select a bounded oldest prefix from *both* stores. If SQLite page
		// packing freed fewer bytes than estimated, remeasure and continue in
		// the same chronological order rather than dropping a newer segment.
		take := int(min(8192, max(1, (excess+95)/96)))
		queue := make(retentionMax, 0, min(take, 256))
		for _, f := range files {
			err = f.visit(ctx, root, func(r Record, line []byte) error {
				queue.offer(retentionEntry{at: r.ReceivedAt, file: f, seq: r.Seq, weight: int64(len(line))}, take)
				return nil
			})
			if err != nil {
				return err
			}
		}
		journalRows, err := j.oldestPayloads(ctx, take)
		if err != nil {
			return err
		}
		for _, r := range journalRows {
			queue.offer(r, take)
		}
		if len(queue) == 0 {
			// Protected receipt digests alone consumed the remaining allowance.
			// Do not discard them to acknowledge an unrepeatable write.
			return ErrReceiptCapacity
		}
		slices.SortFunc(queue, compareRetention)
		var estimate int64
		n := 0
		for _, r := range queue {
			n++
			estimate += r.weight
			if estimate >= excess {
				break
			}
		}
		// Apply a chronological prefix, including on failure. A group is
		// either one contiguous journal run, or one contiguous legacy file.
		// Do not erase a younger legacy file first just because it is simpler.
		compact := false
		for a := 0; a < n; {
			z := a + 1
			for z < n && queue[z].file.name == queue[a].file.name {
				z++
			}
			part := queue[a:z]
			if part[0].file.name == "" {
				seqs := make([]int64, 0, len(part))
				for _, r := range part {
					seqs = append(seqs, r.seq)
				}
				if err = j.retirePayloads(ctx, seqs); err != nil {
					return err
				}
				compact = true
			} else {
				f := part[0].file
				if fi, e := root.Lstat(f.name); e != nil {
					return e
				} else if fi.Size() != f.info.Size() {
					// A previous run within this sweep may already have
					// rewritten this same file; it now has a verified size.
					for _, done := range queue[:a] {
						if done.file.name == f.name {
							f.info = fi
							break
						}
					}
				}
				drop := make(map[int64]bool, len(part))
				for _, r := range part {
					drop[r.seq] = true
				}
				s.files = nil
				if err = f.keep(ctx, root, func(r Record) bool { return !drop[r.Seq] }); err != nil {
					return err
				}
			}
			a = z
		}
		if compact {
			if err = j.compactReceipt(ctx, false); err != nil {
				return err
			}
		}
		if err = refresh(); err != nil {
			return err
		}
	}
	return nil
}
