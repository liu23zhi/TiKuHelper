package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestAgreementLifecycle(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	if e := db.AutoMigrate(&entity.AgreementHead{}, &entity.AgreementDraft{}, &entity.AgreementRelease{}, &entity.AgreementDevice{}, &entity.AgreementConsent{}, &entity.Log{}); e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&entity.AgreementHead{ID: 1}).Error; e != nil {
		t.Fatal(e)
	}
	s := AgreementStore{DB: db}
	a, e := s.SaveDraft(0, 0, entity.AgreementContent{Version: "v1", Summary: "one", Body: "first"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.SaveDraft(0, 0, entity.AgreementContent{Version: "v2", Summary: "two", Body: "second"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Current(); !errors.Is(e, gorm.ErrRecordNotFound) {
		t.Fatalf("unpublished %v", e)
	}
	var wg sync.WaitGroup
	out := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Publish(a.ID, 1); out <- e }()
	}
	wg.Wait()
	close(out)
	for e := range out {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int64
	db.Model(&entity.AgreementRelease{}).Count(&n)
	if n != 1 {
		t.Fatalf("retries made %d releases", n)
	}
	if _, e = s.SaveDraft(b.ID, 1, entity.AgreementContent{Version: "v2", Summary: "revised", Body: "corrected"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Publish(b.ID, 1); !errors.Is(e, ErrAgreementConflict) {
		t.Fatalf("published stale preview: %v", e)
	}
	if e = s.DeleteDraft(a.ID); e != nil {
		t.Fatal(e)
	}
	cur, e := s.Current()
	if e != nil || cur.Body != "first" {
		t.Fatalf("published body lost: %v", e)
	}
	dev, secret, e := s.Enroll()
	if e != nil {
		t.Fatal(e)
	}
	device := AgreementCredentials{Secret: secret, IP: "127.0.0.1"}
	receipt, e := s.Accept(device, "v1")
	if e != nil || receipt.UserID != nil || receipt.DeviceID != dev.ID || receipt.IP != "127.0.0.1" || receipt.AcceptedAt.IsZero() {
		t.Fatalf("unbound receipt: %+v %v", receipt, e)
	}
	if _, e = s.Accept(AgreementCredentials{Secret: dev.ID, IP: device.IP}, "v1"); !errors.Is(e, ErrAgreementProof) {
		t.Fatalf("identifier accepted: %v", e)
	}
	if _, e = s.Publish(b.ID, 2); e != nil {
		t.Fatal(e)
	}
	st, e := s.Status(device)
	if e != nil || !st.NeedsAcceptance || st.Version != "v2" {
		t.Fatalf("stale approval: %+v %v", st, e)
	}
	if _, e = s.Accept(device, "v1"); !errors.Is(e, ErrAgreementConflict) {
		t.Fatalf("stale submit: %v", e)
	}
	insertLeaseTicket(t, db, item, "agreement-fixture")
	identity := strings.Repeat("37", 32)
	run, e := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "agreement-fixture", identity, device.IP, 5)
	if e != nil {
		t.Fatal(e)
	}
	device.Authorization = "Bearer " + run.Token
	device.Identity = identity
	for i := 0; i < 2; i++ {
		got, e := s.Accept(device, "v2")
		if e != nil || got.UserID == nil || *got.UserID != item.UserID || got.ClientKind != ClientKindExamClient {
			t.Fatalf("native window %d: %+v %v", i, got, e)
		}
	}
	var old entity.AgreementConsent
	if e = db.First(&old, receipt.ID).Error; e != nil || old.UserID == nil || *old.UserID != item.UserID || old.IP != receipt.IP || !old.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatalf("backfill rewrote first audit: %+v %v", old, e)
	}
	db.Model(&entity.AgreementConsent{}).Count(&n)
	if n != 2 {
		t.Fatalf("want one per version/device, got %d", n)
	}
	if _, e = s.Status(AgreementCredentials{Secret: secret, IP: device.IP}); !errors.Is(e, ErrAgreementProof) {
		t.Errorf("bound device without account: %v", e)
	}
	// A valid but DIFFERENT account is still denied, even WITH the device secret.
	wrong := entity.User{Username: "other-fixture-owner", Status: "enabled"}
	if e = db.Create(&wrong).Error; e != nil {
		t.Fatal(e)
	}
	key := entity.AccessToken{UserID: wrong.ID, TokenHash: hashAccessTokenValue("wrong-owner-key"), Status: "enabled", TotalQuotaLimit: 20}
	if e = db.Create(&key).Error; e != nil {
		t.Fatal(e)
	}
	otherOwner := device
	otherOwner.Identity = ""
	otherOwner.Authorization = "Bearer wrong-owner-key"
	if _, e = s.Status(otherOwner); !errors.Is(e, ErrAgreementProof) {
		t.Fatalf("foreign account read device: %v", e)
	}
	if _, e = s.Accept(otherOwner, "v2"); !errors.Is(e, ErrAgreementProof) {
		t.Fatalf("foreign account overwrote device: %v", e)
	}
	device.Identity = strings.Repeat("ab", 32)
	if _, e = s.Status(device); !errors.Is(e, ErrAgreementProof) {
		t.Errorf("invalid lease identity: %v", e)
	}
	_, secret2, e := s.Enroll()
	if e != nil {
		t.Fatal(e)
	}
	other := AgreementCredentials{Secret: secret2, IP: "127.0.0.2"}
	now := time.Now()
	out = make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.Accept(other, "v2")
			if e == nil && (r.IP != other.IP || r.AcceptedAt.Before(now)) {
				e = fmt.Errorf("wrong audit")
			}
			out <- e
		}()
	}
	wg.Wait()
	close(out)
	for e := range out {
		if e != nil {
			t.Fatal(e)
		}
	}
	db.Model(&entity.AgreementConsent{}).Count(&n)
	if n != 3 {
		t.Fatalf("duplicate consent: %d", n)
	}
	// Two distinct drafts race to publish; both snapshots survive, one head wins.
	ids := []int64{}
	for _, v := range []string{"v3", "v4"} {
		d, e := s.SaveDraft(0, 0, entity.AgreementContent{Version: v, Summary: v, Body: v})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, d.ID)
	}
	out = make(chan error, 2)
	start := make(chan struct{})
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); <-start; _, e := s.Publish(id, 1); out <- e }(id)
	}
	close(start)
	wg.Wait()
	close(out)
	for err := range out {
		if err != nil {
			t.Fatal(err)
		}
	}
	if e = db.Model(&entity.AgreementRelease{}).Count(&n).Error; e != nil || n != 4 {
		t.Fatalf("immutable history %d: %v", n, e)
	}
	final, e := s.Current()
	if e != nil || (final.Version != "v3" && final.Version != "v4") {
		t.Fatalf("unexpected head: %+v %v", final, e)
	}

}
