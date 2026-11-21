package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newRefundPayoutDeviceTestDB(t *testing.T) (*gorm.DB, entity.User, entity.User) {
	t.Helper()
	db, owner, other, _ := newTicketRefundCoreTestDB(t)
	useTicketAttachmentTestKey(t)
	if err := db.AutoMigrate(&entity.TicketAttachment{}, &entity.Ticket{}, &entity.RefundPayoutQrMemory{}, &entity.RefundPayoutQrBinding{}, &entity.RefundPayoutQrChallenge{}); err != nil {
		t.Fatal(err)
	}
	return db, owner, other
}

func refundPayoutDeviceTestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, base64.StdEncoding.EncodeToString(der)
}

func refundPayoutDeviceTestSignature(t *testing.T, key *ecdsa.PrivateKey, challenge RefundPayoutQrDeviceChallenge) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	var raw [64]byte
	r.FillBytes(raw[:32])
	s.FillBytes(raw[32:])
	return base64.StdEncoding.EncodeToString(raw[:])
}

func refundPayoutDeviceTestUpload(t *testing.T, db *gorm.DB, owner entity.User) int64 {
	t.Helper()
	attachment, err := StoreTicketAttachmentWithDB(db, &owner, 0, ticketAttachmentTestPNG(t, 100, 80))
	if err != nil {
		t.Fatal(err)
	}
	return attachment.ID
}

func TestRefundPayoutDeviceRememberedBeforeRefundAndProofIsOneUse(t *testing.T) {
	db, owner, other := newRefundPayoutDeviceTestDB(t)
	status, err := RefundPayoutQrStatusWithDB(db, owner)
	if err != nil || status.HasPrevious {
		t.Fatalf("initial status=%+v err=%v", status, err)
	}
	id := refundPayoutDeviceTestUpload(t, db, owner)
	private, public := refundPayoutDeviceTestKey(t)
	registered, err := RegisterRefundPayoutQrWithDB(db, owner, id, public)
	if err != nil || registered.AttachmentID != id || registered.KeyID == "" {
		t.Fatalf("registration=%+v err=%v", registered, err)
	}
	status, err = RefundPayoutQrStatusWithDB(db.Session(&gorm.Session{NewDB: true}), owner)
	if err != nil || !status.HasPrevious || status.KeyID != registered.KeyID || status.PreviewDataURL == "" {
		t.Fatalf("unsubmitted image not remembered: %+v %v", status, err)
	}
	if foreign, err := RefundPayoutQrStatusWithDB(db, other); err != nil || foreign.HasPrevious || foreign.KeyID != "" {
		t.Fatalf("foreign status=%+v %v", foreign, err)
	}
	if _, _, _, err := ReadTicketAttachmentWithDB(db, &owner, id); err == nil {
		t.Fatal("draft source available through generic read")
	}
	challenge, err := CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	signature := refundPayoutDeviceTestSignature(t, private, challenge)
	if _, _, _, err := UnlockRefundPayoutQrDeviceWithDB(db, other, challenge.ChallengeID, signature); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("cross-account challenge accepted: %v", err)
	}
	gotID, mime, source, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, signature)
	if err != nil || gotID != id || mime != "image/png" || len(source) == 0 {
		t.Fatalf("device recovery failed: id=%d mime=%s err=%v", gotID, mime, err)
	}
	if status.PreviewDataURL == "data:image/png;base64,"+base64.StdEncoding.EncodeToString(source) {
		t.Fatal("status exposed source image")
	}
	if _, _, body, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, signature); !errors.Is(err, ErrRefundPayoutQrDevice) || len(body) != 0 {
		t.Fatalf("replay accepted: %v", err)
	}
	// Password-authorized recovery uses the same current image, without changing
	// the original uploader's binding or requiring a previously submitted refund.
	gotID, _, recovered, err := ReadLatestRefundPayoutQrWithDB(db, owner)
	if err != nil || gotID != id || !bytes.Equal(recovered, source) {
		t.Fatalf("password recovery source: %v", err)
	}
	after, err := RefundPayoutQrStatusWithDB(db, owner)
	if err != nil || after.KeyID != registered.KeyID {
		t.Fatal("password recovery changed device binding")
	}
}

func TestRefundPayoutDeviceRejectsForgedExpiredAndReplacedChallenges(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	private, public := refundPayoutDeviceTestKey(t)
	id := refundPayoutDeviceTestUpload(t, db, owner)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); err != nil {
		t.Fatal(err)
	}
	challenge, err := CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, _ := refundPayoutDeviceTestKey(t)
	if _, _, _, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, wrongKey, challenge)); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("forged proof: %v", err)
	}
	if _, _, _, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, private, challenge)); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("invalid proof was not consumed: %v", err)
	}
	challenge, err = CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.RefundPayoutQrChallenge{}).Where("id = ?", challenge.ChallengeID).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, private, challenge)); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("expired proof: %v", err)
	}
	challenge, err = CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	replacement := refundPayoutDeviceTestUpload(t, db, owner)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, replacement, public); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, private, challenge)); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("old image proof: %v", err)
	}
	_, differentPublic := refundPayoutDeviceTestKey(t)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, differentPublic); !errors.Is(err, ErrRefundPayoutQrRegistered) && !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("old image rebound: %v", err)
	}
	challenge, err = CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, private, challenge)); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("deleted image recovered: %v", err)
	}
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, replacement, public); !errors.Is(err, ErrRefundPayoutQrRegistered) && !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("deleted image rebound: %v", err)
	}
}

func TestRefundPayoutDeviceIndependentTabsKeepLiveChallenges(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	private, public := refundPayoutDeviceTestKey(t)
	id := refundPayoutDeviceTestUpload(t, db, owner)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); err != nil {
		t.Fatal(err)
	}
	first, err := CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateRefundPayoutQrChallengeWithDB(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, challenge := range []RefundPayoutQrDeviceChallenge{second, first} {
		gotID, _, body, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, private, challenge))
		if err != nil || gotID != id || len(body) == 0 {
			t.Fatalf("independent tab lost live challenge: %v", err)
		}
	}
}

func TestRefundPayoutDeviceLegacyAndDeletionNeverResurrectPreviousImage(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	id := refundPayoutDeviceTestUpload(t, db, owner)
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", id).Update("scan_state", entity.TicketAttachmentScanApproved).Error; err != nil {
		t.Fatal(err)
	}
	refund := entity.TicketRefund{UserID: owner.ID, Kind: TicketRefundKindManual, Status: TicketRefundStatusSucceeded, RefundNo: "legacy-payout-fixture", PayoutQrAttachmentID: &id}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatal(err)
	}
	status, err := RefundPayoutQrStatusWithDB(db, owner)
	if err != nil || !status.HasPrevious || status.KeyID != "" {
		t.Fatalf("legacy status=%+v %v", status, err)
	}
	if _, err := CreateRefundPayoutQrChallengeWithDB(db, owner); !errors.Is(err, ErrRefundPayoutQrDevice) {
		t.Fatalf("legacy image got device access: %v", err)
	}
	_, public := refundPayoutDeviceTestKey(t)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); !errors.Is(err, ErrRefundPayoutQrRegistered) {
		t.Fatalf("legacy image rebound: %v", err)
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	status, err = RefundPayoutQrStatusWithDB(db.Session(&gorm.Session{NewDB: true}), owner)
	if err != nil || status.HasPrevious {
		t.Fatalf("deletion resurrected legacy image: %+v %v", status, err)
	}
	gotID, _, body, err := ReadLatestRefundPayoutQrWithDB(db, owner)
	if err != nil || gotID != 0 || len(body) > 0 {
		t.Fatalf("deleted image password recovered: %d %v", gotID, err)
	}
	if err := db.First(&refund, refund.ID).Error; err != nil || refund.PayoutQrAttachmentID == nil || *refund.PayoutQrAttachmentID != id {
		t.Fatal("deletion altered historical refund evidence")
	}
}

func TestRefundPayoutDeviceOwnershipHiddenImagesAndKeyValidation(t *testing.T) {
	db, owner, other := newRefundPayoutDeviceTestDB(t)
	private, public := refundPayoutDeviceTestKey(t)
	id := refundPayoutDeviceTestUpload(t, db, owner)
	if _, err := RegisterRefundPayoutQrWithDB(db, other, id, public); !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("foreign registration: %v", err)
	}
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, "bad SPKI"); !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("invalid public key accepted: %v", err)
	}
	nonP256, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&nonP256.PublicKey)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, base64.StdEncoding.EncodeToString(der)); !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("unsupported curve accepted: %v", err)
	}
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{entity.TicketAttachmentScanSensitive, entity.TicketAttachmentScanFailed, "unknown"} {
		if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", id).Update("scan_state", state).Error; err != nil {
			t.Fatal(err)
		}
		challenge, err := CreateRefundPayoutQrChallengeWithDB(db, owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, body, err := UnlockRefundPayoutQrDeviceWithDB(db, owner, challenge.ChallengeID, refundPayoutDeviceTestSignature(t, private, challenge)); !errors.Is(err, ErrTicketAttachmentHidden) || len(body) > 0 {
			t.Fatalf("%s exposed through device proof: %v", state, err)
		}
		if _, _, body, err := ReadLatestRefundPayoutQrWithDB(db, owner); !errors.Is(err, ErrTicketAttachmentHidden) || len(body) > 0 {
			t.Fatalf("%s exposed through password recovery: %v", state, err)
		}
		status, err := RefundPayoutQrStatusWithDB(db, owner)
		if err != nil || !status.HasPrevious || status.PreviewDataURL != "" {
			t.Fatalf("%s preview=%+v %v", state, status, err)
		}
	}
}

func TestRefundPayoutDeviceBindingsBlockGenericReadsAfterBindingOrMemoryDeletion(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	id := refundPayoutDeviceTestUpload(t, db, owner)
	_, public := refundPayoutDeviceTestKey(t)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); err != nil {
		t.Fatal(err)
	}
	ticket := entity.Ticket{UserID: owner.ID, Subject: "remembered QR source", Status: entity.TicketStatusOpen}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", id).Updates(map[string]any{"ticket_id": ticket.ID, "scan_state": entity.TicketAttachmentScanApproved}).Error; err != nil {
		t.Fatal(err)
	}
	if _, body, _, err := ReadTicketAttachmentWithDB(db, &owner, id); !errors.Is(err, ErrTicketForbidden) || len(body) != 0 {
		t.Fatalf("registered source exposed via generic attachment read: %v", err)
	}
	var admin entity.User
	if err := db.First(&admin, 93).Error; err != nil {
		t.Fatal(err)
	}
	if _, body, _, err := ReadTicketAttachmentWithDB(db, &admin, id); err != nil || len(body) == 0 {
		t.Fatalf("administrator evidence read blocked: %v", err)
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	if _, body, _, err := ReadTicketAttachmentWithDB(db, &owner, id); !errors.Is(err, ErrTicketForbidden) || len(body) != 0 {
		t.Fatalf("deleted memory became generically readable: %v", err)
	}
}

func TestRefundPayoutDeviceMemorySurvivesRetentionUntilReplacedOrDeleted(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	_, public := refundPayoutDeviceTestKey(t)
	oldID := refundPayoutDeviceTestUpload(t, db, owner)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, oldID, public); err != nil {
		t.Fatal(err)
	}
	currentID := refundPayoutDeviceTestUpload(t, db, owner)
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, currentID, public); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id IN ?", []int64{oldID, currentID}).Update("created_at", time.Now().Add(-400*24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := PruneTicketAttachmentsByAge(db, time.Now(), 180); err != nil || count != 0 {
		t.Fatalf("age cleanup count=%d err=%v", count, err)
	}
	if count, err := PruneTicketAttachmentsBySize(db, time.Now(), 1); err != nil || count != 0 {
		t.Fatalf("size cleanup count=%d err=%v", count, err)
	}
	id, _, body, err := ReadLatestRefundPayoutQrWithDB(db, owner)
	if err != nil || id != currentID || len(body) == 0 {
		t.Fatalf("remembered source expired: %v", err)
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	if count, err := PruneTicketAttachmentsByAge(db, time.Now(), 180); err != nil || count != 0 {
		t.Fatalf("deleted memory exempted forever: count=%d err=%v", count, err)
	}
	var bindings int64
	if err := db.Model(&entity.RefundPayoutQrBinding{}).Count(&bindings).Error; err != nil || bindings != 2 {
		t.Fatalf("retention removed rebind protection: %d %v", bindings, err)
	}
}

func TestRefundPayoutDeviceReplacementsReleaseDraftQuotaAndCiphertext(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	_, public := refundPayoutDeviceTestKey(t)
	firstID := refundPayoutDeviceTestUpload(t, db, owner)
	var first entity.TicketAttachment
	if err := db.First(&first, firstID).Error; err != nil {
		t.Fatal(err)
	}
	// Enough room for a current image and its replacement, but a leaked third
	// source would make the next upload fail.
	if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, strconv.FormatInt(first.ByteSize*2, 10), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, firstID, public); err != nil {
		t.Fatal(err)
	}
	previousID := firstID
	for i := 0; i < 6; i++ {
		id := refundPayoutDeviceTestUpload(t, db, owner)
		if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); err != nil {
			t.Fatal(err)
		}
		var discarded entity.TicketAttachment
		if err := db.First(&discarded, previousID).Error; err != nil || discarded.PurgedAt == nil || len(discarded.Ciphertext) != 0 || len(discarded.Nonce) != 0 {
			t.Fatalf("old source not purged: id=%d err=%v", previousID, err)
		}
		used, err := ticketAttachmentUsageBytes(db, 0, owner.ID)
		if err != nil || used != first.ByteSize {
			t.Fatalf("replacement leaked quota: %d %v", used, err)
		}
		previousID = id
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	if used, err := ticketAttachmentUsageBytes(db, 0, owner.ID); err != nil || used != 0 {
		t.Fatalf("deletion leaked draft quota: %d %v", used, err)
	}
	_ = refundPayoutDeviceTestUpload(t, db, owner)
	// Bound tickets retain their existing lifetime logical quota, including
	// their policy-purged rows. This change applies only to unbound drafts.
	purged := time.Now()
	bound := entity.TicketAttachment{TicketID: 100, UserID: owner.ID, ByteSize: first.ByteSize, PurgedAt: &purged}
	if err := db.Create(&bound).Error; err != nil {
		t.Fatal(err)
	}
	if used, err := ticketAttachmentUsageBytes(db, 100, owner.ID); err != nil || used != first.ByteSize {
		t.Fatalf("bound ticket logical quota changed: %d %v", used, err)
	}
	var bindings int64
	if err := db.Model(&entity.RefundPayoutQrBinding{}).Count(&bindings).Error; err != nil || bindings != 7 {
		t.Fatalf("immutable bindings removed: %d %v", bindings, err)
	}
}

func TestRefundPayoutDeviceCleanupPreservesSubmittedAndBoundEvidence(t *testing.T) {
	for _, relationship := range []string{"ticket", "message", "refund-payout", "refund-signature"} {
		t.Run(relationship, func(t *testing.T) {
			db, owner, _ := newRefundPayoutDeviceTestDB(t)
			_, public := refundPayoutDeviceTestKey(t)
			id := refundPayoutDeviceTestUpload(t, db, owner)
			if _, err := RegisterRefundPayoutQrWithDB(db, owner, id, public); err != nil {
				t.Fatal(err)
			}
			if relationship == "ticket" {
				if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", id).Update("ticket_id", 123).Error; err != nil {
					t.Fatal(err)
				}
			} else if relationship == "message" {
				if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", id).Update("message_id", 456).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				row := entity.TicketRefund{UserID: owner.ID, Kind: TicketRefundKindManual, Status: TicketRefundStatusPendingReview, RefundNo: "cleanup-evidence"}
				if relationship == "refund-payout" {
					row.PayoutQrAttachmentID = &id
				} else {
					row.SignatureAttachmentID = &id
				}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			replacement := refundPayoutDeviceTestUpload(t, db, owner)
			if _, err := RegisterRefundPayoutQrWithDB(db, owner, replacement, public); err != nil {
				t.Fatal(err)
			}
			if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
				t.Fatal(err)
			}
			var evidence entity.TicketAttachment
			if err := db.First(&evidence, id).Error; err != nil || evidence.PurgedAt != nil || len(evidence.Ciphertext) == 0 || len(evidence.Nonce) == 0 {
				t.Fatalf("%s evidence was purged: %v", relationship, err)
			}
		})
	}
}

func TestRefundPayoutDeviceCleanupReleasesBlobReferenceAtomically(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE ticket_attachments ADD COLUMN blob_sha256 char(64) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatal(err)
	}
	_, public := refundPayoutDeviceTestKey(t)
	firstID := refundPayoutDeviceTestUpload(t, db, owner)
	if _, linked, err := LinkUploadedTicketAttachmentBlobWithDB(db, firstID); err != nil || !linked {
		t.Fatalf("first blob link: %v", err)
	}
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, firstID, public); err != nil {
		t.Fatal(err)
	}
	secondID := refundPayoutDeviceTestUpload(t, db, owner)
	if count, linked, err := LinkUploadedTicketAttachmentBlobWithDB(db, secondID); err != nil || !linked || count != 2 {
		t.Fatalf("second blob link: %d %v", count, err)
	}
	var first entity.TicketAttachment
	if err := db.First(&first, firstID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterRefundPayoutQrWithDB(db, owner, secondID, public); err != nil {
		t.Fatal(err)
	}
	var blob entity.TicketAttachmentBlob
	if err := db.Where("sha256 = ?", first.SHA256).Take(&blob).Error; err != nil || blob.RefCount != 1 {
		t.Fatalf("replacement blob count=%d err=%v", blob.RefCount, err)
	}
	var oldLink entity.TicketAttachmentBlobLink
	if err := db.Where("id = ?", firstID).Take(&oldLink).Error; err != nil || oldLink.BlobSHA256 != "" {
		t.Fatalf("discarded source still linked: %v", err)
	}
	// A failed blob release must also roll back the memory deletion and the
	// attachment purge, preserving the currently recoverable image.
	injected := errors.New("synthetic blob release failure")
	if err := db.Callback().Update().Before("gorm:update").Register("payout_blob_release_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameTicketAttachmentBlob {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := DeleteRefundPayoutQrWithDB(db, owner)
	_ = db.Callback().Update().Remove("payout_blob_release_failure")
	if !errors.Is(err, injected) {
		t.Fatalf("release failure not propagated: %v", err)
	}
	id, _, clear, err := ReadLatestRefundPayoutQrWithDB(db, owner)
	if err != nil || id != secondID || len(clear) == 0 {
		t.Fatalf("failed deletion did not roll back current source: %v", err)
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("sha256 = ?", first.SHA256).Take(&blob).Error; err != nil || blob.RefCount != 0 {
		t.Fatalf("deletion blob count=%d err=%v", blob.RefCount, err)
	}
	if count, err := PurgeExpiredTicketAttachmentBlobs(db, time.Now().Add(25*time.Hour), 100); err != nil || count != 1 {
		t.Fatalf("unused blob not reclaimable: %d %v", count, err)
	}
}

func TestRefundPayoutDeviceLegacyMemorySurvivesRetentionBeforeFirstStatus(t *testing.T) {
	db, owner, _ := newRefundPayoutDeviceTestDB(t)
	oldID := refundPayoutDeviceTestUpload(t, db, owner)
	latestID := refundPayoutDeviceTestUpload(t, db, owner)
	for index, id := range []int64{oldID, latestID} {
		row := entity.TicketRefund{UserID: owner.ID, Kind: TicketRefundKindManual, Status: TicketRefundStatusSucceeded, RefundNo: []string{"legacy-old", "legacy-latest"}[index], PayoutQrAttachmentID: &id}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id IN ?", []int64{oldID, latestID}).Updates(map[string]any{"created_at": time.Now().Add(-400 * 24 * time.Hour), "scan_state": entity.TicketAttachmentScanApproved}).Error; err != nil {
		t.Fatal(err)
	}
	// Cleanup can run before the first status request has materialized anything.
	if count, err := PruneTicketAttachmentsByAge(db, time.Now(), 180); err != nil || count != 1 {
		t.Fatalf("legacy age cleanup count=%d err=%v", count, err)
	}
	if count, err := PruneTicketAttachmentsBySize(db, time.Now(), 1); err != nil || count != 0 {
		t.Fatalf("legacy size cleanup count=%d err=%v", count, err)
	}
	id, _, body, err := ReadLatestRefundPayoutQrWithDB(db, owner)
	if err != nil || id != latestID || len(body) == 0 {
		t.Fatalf("legacy current source expired: %d %v", id, err)
	}
	if err := DeleteRefundPayoutQrWithDB(db, owner); err != nil {
		t.Fatal(err)
	}
	if count, err := PruneTicketAttachmentsByAge(db, time.Now(), 180); err != nil || count != 1 {
		t.Fatalf("legacy deletion still exempted: count=%d err=%v", count, err)
	}
}
