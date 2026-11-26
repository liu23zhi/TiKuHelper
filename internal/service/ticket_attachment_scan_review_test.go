package service

import (
	"context"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func t12ReviewSetup(t *testing.T) (*gorm.DB, *entity.User) {
	db := wiringNewDB(t)
	if e := db.AutoMigrate(&entity.SensitiveImageCacheEntry{}); e != nil {
		t.Fatal(e)
	}
	if e := saveSystemSettingValue(db, UAPIAPIKeySettingKey, imageFixtureKey, "fixture"); e != nil {
		t.Fatal(e)
	}
	return db, wiringNewUser(t, db, 1)
}
func t12ReviewRead(t *testing.T, db *gorm.DB, id int64) entity.TicketAttachment {
	var a entity.TicketAttachment
	if e := db.First(&a, id).Error; e != nil {
		t.Fatal(e)
	}
	return a
}
func TestT12ReviewContractSharedRows(t *testing.T) {
	db, u := t12ReviewSetup(t)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "{\"is_nsfw\":%t}", calls.Add(1) == 1) }))
	defer s.Close()
	withTicketAttachmentNSFWURL(t, s.URL)
	data := ticketAttachmentTestPNG(t, 8, 8)
	i, _ := wiringUpload(t, db, u, data)
	j, n := wiringUpload(t, db, u, data)
	if n != 2 {
		t.Fatal(n)
	}
	_, e := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now().Add(time.Second))
	if e != nil {
		t.Fatal(e)
	}
	a, b := t12ReviewRead(t, db, i), t12ReviewRead(t, db, j)
	blob := wiringBlobRow(t, db, a.SHA256)
	cache, ok, e := lookupSensitiveImageCacheWithDB(db, a.SHA256)
	if e != nil || !ok || a.SHA256 != b.SHA256 {
		t.Fatal(e)
	}
	t.Logf("sameSHA=true refs=%d attachments=%s,%s blob=%s cache=%s background_calls=%d", n, a.ScanState, b.ScanState, blob.ScanState, cache.Status, calls.Load())
	if a.ScanState != b.ScanState || b.ScanState != blob.ScanState {
		t.Fatal("shared state differs between references")
	}
}
func TestT12ReviewContractUploadFlagged(t *testing.T) {
	db, u := t12ReviewSetup(t)
	on := true
	if e := SetTicketImageModerationEnabledWithDB(db, &on); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, "{\"is_nsfw\":true}") }))
	defer s.Close()
	withTicketAttachmentNSFWURL(t, s.URL)
	i, _ := wiringUpload(t, db, u, ticketAttachmentTestPNG(t, 8, 8))
	a := t12ReviewRead(t, db, i)
	blob := wiringBlobRow(t, db, a.SHA256)
	cache, ok, e := lookupSensitiveImageCacheWithDB(db, a.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	n, e := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now().Add(time.Second))
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("attachment=%s visible=%t blob=%s cache.found=%t cache.status=%s worker.rows=%d calls=%d", a.ScanState, ticketAttachmentVisible(a), blob.ScanState, ok, cache.Status, n, calls.Load())
	if a.ScanState != blob.ScanState || !ok {
		t.Fatal("upload verdict did not reach shared status")
	}
}
func TestT12ReviewContractMaskedExport(t *testing.T) {
	db, _ := t12ReviewSetup(t)
	s := "synthetic historical diagnostic prose"
	seedSensitiveImageCache(t, db, strings.Repeat("c", 64), "error", s)
	rows, e := sensitiveImageExportLoader(false)(db, 10)
	if e != nil || len(rows) != 1 {
		t.Fatal(e)
	}
	leak := rows[0][2] == s
	t.Logf("masked.rows=%d image_bytes_present=%t historical_reason_passed_through=%t", len(rows), rows[0][4] != "", leak)
	if leak {
		t.Fatal("masked history export included original reason")
	}
}
func TestT12ReviewContractManualWorkerOverlap(t *testing.T) {
	dir := captureImageDiagnosticsLog(t)
	db, u := t12ReviewSetup(t)
	started := make(chan struct{})
	resume := make(chan struct{})
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			select {
			case <-resume:
			case <-time.After(5 * time.Second):
			}
		}
		fmt.Fprintf(w, "{\"is_nsfw\":%t}", n == 2)
	}))
	defer s.Close()
	withTicketAttachmentNSFWURL(t, s.URL)
	i, _ := wiringUpload(t, db, u, ticketAttachmentTestPNG(t, 9, 9))
	a := t12ReviewRead(t, db, i)
	done := make(chan error, 1)
	go func() {
		_, e := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now().Add(time.Second))
		done <- e
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach loopback")
	}
	_, e := ManualDetectSensitiveImageBySHAWithDB(context.Background(), db, a.SHA256)
	close(resume)
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	a = t12ReviewRead(t, db, i)
	b := wiringBlobRow(t, db, a.SHA256)
	c, ok, e := lookupSensitiveImageCacheWithDB(db, a.SHA256)
	if e != nil || !ok {
		t.Fatal(e)
	}
	txt, e := os.ReadFile(filepath.Join(dir, "Tiku-sensitive-image.txt"))
	if e != nil {
		t.Fatal(e)
	}
	claimed, discarded := false, false
	for _, line := range strings.Split(string(txt), "\n") {
		if strings.Contains(line, "channel=background") {
			claimed = claimed || strings.Contains(line, "state=approved")
			discarded = discarded || strings.Contains(line, "discard") || strings.Contains(line, "superseded")
		}
	}
	t.Logf("attachments=%s blob=%s cache=%s calls=%d; background.log.approved=%t log.discarded=%t", a.ScanState, b.ScanState, c.Status, calls.Load(), claimed, discarded)
	if a.ScanState != "sensitive" || b.ScanState != "sensitive" || c.Status != "sensitive" {
		t.Fatal("stale worker affected stored verdict")
	}
	if claimed && !discarded {
		t.Fatal("diagnostic lacks final superseded outcome")
	}
	byDiagnostic := make(map[string][]string)
	for _, line := range strings.Split(string(txt), "\n") {
		if !strings.Contains(line, "channel=background") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if diagnostic, ok := strings.CutPrefix(field, "diagnostic_id="); ok {
				byDiagnostic[diagnostic] = append(byDiagnostic[diagnostic], line)
				break
			}
		}
	}
	for diagnostic, lines := range byDiagnostic {
		upstreamApproved, finalSensitive := false, false
		for _, line := range lines {
			upstreamApproved = upstreamApproved || (strings.Contains(line, "event=detect_upstream_result") && strings.Contains(line, "state=approved "))
			finalSensitive = finalSensitive || (strings.Contains(line, "event=detect_superseded") && strings.Contains(line, "state=sensitive "))
			if strings.Contains(line, "event=detect_applied") {
				t.Fatal("stale worker falsely reported a committed verdict")
			}
		}
		if upstreamApproved && finalSensitive && localImageDiagnosticID.MatchString(diagnostic) {
			return
		}
	}
	t.Fatal("same background IMG must distinguish upstream approved from effective sensitive")
}

func TestT12ReviewTwoWorkersAndLaterReferenceUseOnePersistentClaim(t *testing.T) {
	db, user := t12ReviewSetup(t)
	data := ticketAttachmentTestPNG(t, 10, 10)
	first, _ := wiringUpload(t, db, user, data)
	started := make(chan struct{})
	release := make(chan struct{})
	var released sync.Once
	finish := func() { released.Do(func() { close(release) }) }
	defer finish()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		fmt.Fprintf(w, "{\"is_nsfw\":%t}", n == 1)
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	now := time.Now().Add(time.Second)
	done := make(chan error, 1)
	go func() {
		_, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach loopback")
	}
	second, _ := wiringUpload(t, db, user, data)
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db.Session(&gorm.Session{NewDB: true}), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("a second worker made a billable call before the first committed: %d", calls.Load())
	}
	sha := t12ReviewRead(t, db, first).SHA256
	cache, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found || cache.Status != "pending" {
		t.Fatalf("persistent claim during call: %s found=%t err=%v", cache.Status, found, err)
	}
	finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not commit promptly")
	}
	third, _ := wiringUpload(t, db, user, data)
	for _, id := range []int64{first, second, third} {
		if got := t12ReviewRead(t, db, id); got.ScanState != "sensitive" || ticketAttachmentVisible(got) {
			t.Fatalf("SHA reference %d: %s", id, got.ScanState)
		}
	}
	if got := wiringBlobRow(t, db, sha); got.ScanState != "sensitive" || got.RefCount != 3 {
		t.Fatalf("blob state=%s refs=%d", got.ScanState, got.RefCount)
	}
	if calls.Load() != 1 {
		t.Fatalf("later attachment caused a billable call: %d", calls.Load())
	}
}

func TestT12ReviewFlaggedUpdatesExistingBlobAndNewPassCannotOpenIt(t *testing.T) {
	db, user := t12ReviewSetup(t)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "{\"is_nsfw\":%t}", calls.Add(1) == 2)
	}))
	defer s.Close()
	withTicketAttachmentNSFWURL(t, s.URL)
	data := ticketAttachmentTestPNG(t, 11, 11)
	oldID, _ := wiringUpload(t, db, user, data)
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	old := t12ReviewRead(t, db, oldID)
	if old.ScanState != "approved" || wiringBlobRow(t, db, old.SHA256).ScanState != "approved" {
		t.Fatal("fixture must start with an already-approved existing blob")
	}
	enable := true
	if err := SetTicketImageModerationEnabledWithDB(db, &enable); err != nil {
		t.Fatal(err)
	}
	flaggedID, _ := wiringUpload(t, db, user, data)
	passID, _ := wiringUpload(t, db, user, data)
	for _, id := range []int64{oldID, flaggedID, passID} {
		if got := t12ReviewRead(t, db, id); ticketAttachmentVisible(got) || got.ScanState != "sensitive" {
			t.Fatalf("flagged ruling did not survive new pass: %d %s", id, got.ScanState)
		}
	}
	blob := wiringBlobRow(t, db, old.SHA256)
	cache, found, err := lookupSensitiveImageCacheWithDB(db, old.SHA256)
	if err != nil || !found || cache.Status != "sensitive" || blob.ScanState != "sensitive" || blob.RefCount != 3 {
		t.Fatalf("shared status: cache=%s blob=%s refs=%d found=%t err=%v", cache.Status, blob.ScanState, blob.RefCount, found, err)
	}
	if rows, err := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now().Add(time.Second)); err != nil || rows != 0 || calls.Load() != 3 {
		t.Fatalf("unneeded background scans: rows=%d calls=%d err=%v", rows, calls.Load(), err)
	}
}

func TestT12ReviewDuplicateSharesBackoff(t *testing.T) {
	db, user := t12ReviewSetup(t)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "{\"is_nsfw\":false}")
	}))
	defer s.Close()
	withTicketAttachmentNSFWURL(t, s.URL)
	data := ticketAttachmentTestPNG(t, 13, 13)
	first, _ := wiringUpload(t, db, user, data)
	now := time.Now().Add(time.Second)
	process := func(at time.Time) {
		t.Helper()
		if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, at); err != nil {
			t.Fatal(err)
		}
	}
	process(now)
	second, _ := wiringUpload(t, db, user, data)
	process(now.Add(30 * time.Second))
	if calls.Load() != 1 {
		t.Fatal("adding a new reference bypassed shared retry backoff")
	}
	process(now.Add(time.Minute))
	for _, id := range []int64{first, second} {
		if got := t12ReviewRead(t, db, id); got.ScanState != "approved" || got.AttemptCount != 2 {
			t.Fatalf("shared retry result for %d: state=%q attempts=%d", id, got.ScanState, got.AttemptCount)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one shared retry, got %d calls", calls.Load())
	}
}
