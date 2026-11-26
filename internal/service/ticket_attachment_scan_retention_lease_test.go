package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestT12RetentionLiveReferenceUsesRemainingBudgetAndKeepsOldBytesHidden(t *testing.T) {
	db, owner := t12ReviewSetup(t)
	other := wiringNewUser(t, db, 2)
	oldTicket := seedTicketAttachmentTicket(t, db, owner.ID)
	freshTicket := seedTicketAttachmentTicket(t, db, owner.ID)
	image := ticketAttachmentTestPNG(t, 13, 15)
	oldID, _ := wiringUpload(t, db, owner, image)
	now := time.Now()
	if err := db.Exec("UPDATE ticket_attachments SET ticket_id = ?, created_at = ?, attempt_count = 2 WHERE id = ?",
		oldTicket.ID, now.Add(-48*time.Hour), oldID).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := PruneTicketAttachmentsByAge(db, now, 1); err != nil || count != 1 {
		t.Fatalf("retention count=%d err=%v", count, err)
	}
	freshID, _ := wiringUpload(t, db, owner, image)
	if err := db.Exec("UPDATE ticket_attachments SET ticket_id = ? WHERE id = ?", freshTicket.ID, freshID).Error; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "{\"is_nsfw\":false}")
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	old, fresh := t12ReviewRead(t, db, oldID), t12ReviewRead(t, db, freshID)
	if calls.Load() != 1 || fresh.ScanState != "approved" || fresh.AttemptCount != 3 ||
		old.PurgedAt == nil || len(old.Ciphertext) != 0 || len(old.Nonce) != 0 {
		t.Fatalf("calls=%d fresh.state=%s fresh.attempts=%d old.purged=%t old.bytes=%d",
			calls.Load(), fresh.ScanState, fresh.AttemptCount, old.PurgedAt != nil, len(old.Ciphertext))
	}
	if _, body, _, err := ReadTicketAttachmentWithDB(db, owner, oldID); len(body) != 0 || err == nil {
		t.Fatal("owner could retrieve purged original through its old ticket")
	}
	if _, body, _, err := ReadTicketAttachmentWithDB(db, owner, freshID); len(body) == 0 || err != nil {
		t.Fatalf("owner cannot retrieve newly approved original: %v", err)
	}
	if _, body, _, err := ReadTicketAttachmentWithDB(db, other, freshID); len(body) != 0 || err == nil {
		t.Fatal("cross-owner original became accessible")
	}
}

func TestT12RetentionNoLiveReferenceDoesNotReadDormantBlob(t *testing.T) {
	db, owner := t12ReviewSetup(t)
	id, _ := wiringUpload(t, db, owner, ticketAttachmentTestPNG(t, 12, 15))
	now := time.Now()
	if err := db.Exec("UPDATE ticket_attachments SET created_at = ? WHERE id = ?", now.Add(-48*time.Hour), id).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := PruneTicketAttachmentsByAge(db, now, 1); err != nil || count != 1 {
		t.Fatalf("retention count=%d err=%v", count, err)
	}
	row := t12ReviewRead(t, db, id)
	if _, plain, err := ReadTicketAttachmentBlobWithDB(db, row.SHA256); err != nil || len(plain) == 0 {
		t.Fatalf("fixture requires the delayed-purge blob still exists: %v", err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "{\"is_nsfw\":false}")
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	row = t12ReviewRead(t, db, id)
	entry, found, err := lookupSensitiveImageCacheWithDB(db, row.SHA256)
	if err != nil || !found || entry.Status != "error" || row.ScanState != "pending" ||
		row.AttemptCount != 1 || row.PurgedAt == nil || calls.Load() != 0 {
		t.Fatalf("calls=%d state=%s attempts=%d purged=%t cache=%s found=%t err=%v",
			calls.Load(), row.ScanState, row.AttemptCount, row.PurgedAt != nil, entry.Status, found, err)
	}
}

func TestT12GenuinelyExpiredWorkerLeaseIsRecoverable(t *testing.T) {
	db, owner := t12ReviewSetup(t)
	id, _ := wiringUpload(t, db, owner, ticketAttachmentTestPNG(t, 14, 15))
	start := time.Now().Add(time.Second)
	oldClaim, err := claimTicketImageWorker(db, t12ReviewRead(t, db, id), start, "IMG-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || !oldClaim.owned {
		t.Fatalf("initial lease owned=%t err=%v", oldClaim.owned, err)
	}
	if count, err := RecoverStaleTicketAttachmentScans(db, start.Add(119*time.Second)); err != nil || count != 0 {
		t.Fatalf("recovered before original 120s threshold: count=%d err=%v", count, err)
	}
	at := start.Add(121 * time.Second)
	if count, err := RecoverStaleTicketAttachmentScans(db, at); err != nil || count != 1 {
		t.Fatalf("did not recover actually stranded lease: count=%d err=%v", count, err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "{\"is_nsfw\":false}")
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, at); err != nil {
		t.Fatal(err)
	}
	result, err := completeTicketImageWorker(db, oldClaim, TicketAttachmentScanResult{State: "sensitive"}, at.Add(time.Second))
	row := t12ReviewRead(t, db, id)
	if err != nil || result.event != "detect_superseded" || result.state != "approved" ||
		calls.Load() != 1 || row.ScanState != "approved" {
		t.Fatalf("stale completion=%+v calls=%d state=%s err=%v", result, calls.Load(), row.ScanState, err)
	}
}

func TestT12LateBatchUsesTheItemClaimClock(t *testing.T) {
	db, owner := t12ReviewSetup(t)
	id, _ := wiringUpload(t, db, owner, ticketAttachmentTestPNG(t, 15, 16))
	row := t12ReviewRead(t, db, id)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	finish := func() { released.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		fmt.Fprint(w, "{\"is_nsfw\":false}")
	}))
	defer server.Close()
	defer finish()
	withTicketAttachmentNSFWURL(t, server.URL)
	poll := time.Now().Add(-125 * time.Second)
	done := make(chan error, 1)
	go func() { done <- processTicketAttachmentScan(context.Background(), db, row, poll) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never reached loopback")
	}
	current := t12ReviewRead(t, db, id)
	entry, found, err := lookupSensitiveImageCacheWithDB(db, row.SHA256)
	if err != nil || !found || !current.UpdatedAt.Equal(entry.UpdatedAt) || !entry.UpdatedAt.After(poll.Add(2*time.Minute)) {
		t.Fatalf("attachment claim=%v cache claim=%v found=%t err=%v", current.UpdatedAt, entry.UpdatedAt, found, err)
	}
	at := time.Now()
	if count, err := RecoverStaleTicketAttachmentScans(db, at); err != nil || count != 0 {
		t.Fatalf("fresh lease recovered=%d err=%v", count, err)
	}
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, at); err != nil {
		t.Fatal(err)
	}
	finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
	if calls.Load() != 1 {
		t.Fatalf("late batch item sent %d network calls, want one", calls.Load())
	}
}
