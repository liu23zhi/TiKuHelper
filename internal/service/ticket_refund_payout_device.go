package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRefundPayoutQrDevice     = errors.New("收款码设备验证失败，请输入当前密码解锁")
	ErrRefundPayoutQrRegistered = errors.New("该收款码已登记，请重新上传图片")
)

type RefundPayoutQrStatus struct {
	HasPrevious    bool   `json:"hasPrevious"`
	KeyID          string `json:"keyId"`
	PreviewDataURL string `json:"previewDataUrl"`
}

type RefundPayoutQrRegistration struct {
	AttachmentID int64  `json:"attachmentId"`
	KeyID        string `json:"keyId"`
}

type RefundPayoutQrDeviceChallenge struct {
	ChallengeID string `json:"challengeId"`
	Challenge   string `json:"challenge"`
}

// An explicit zero ID is a deletion marker. Legacy refunds are consulted only
// before this user has ever registered or deleted their remembered image.
func currentRefundPayoutQr(db *gorm.DB, userID int32) (entity.RefundPayoutQrMemory, error) {
	if db == nil || userID <= 0 {
		return entity.RefundPayoutQrMemory{}, ErrTicketRefundInvalid
	}
	if db.Migrator().HasTable(&entity.RefundPayoutQrMemory{}) {
		var memory entity.RefundPayoutQrMemory
		err := db.Session(&gorm.Session{NewDB: true}).Where("user_id = ?", userID).Take(&memory).Error
		if err == nil {
			return memory, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return memory, err
		}
	}
	id, err := LatestRefundPayoutQrAttachmentIDWithDB(db, userID)
	return entity.RefundPayoutQrMemory{UserID: userID, AttachmentID: id}, err
}

func RefundPayoutQrStatusWithDB(db *gorm.DB, actor entity.User) (RefundPayoutQrStatus, error) {
	if _, err := ticketRefundActor(db, actor.ID, false, false); err != nil {
		return RefundPayoutQrStatus{}, err
	}
	memory, err := currentRefundPayoutQr(db, actor.ID)
	if err != nil || memory.AttachmentID == 0 {
		return RefundPayoutQrStatus{}, err
	}
	var row entity.TicketAttachment
	if err := db.Select("id").Where("id = ? AND user_id = ? AND purged_at IS NULL", memory.AttachmentID, actor.ID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RefundPayoutQrStatus{}, nil
		}
		return RefundPayoutQrStatus{}, err
	}
	status := RefundPayoutQrStatus{HasPrevious: true}
	if db.Migrator().HasTable(&entity.RefundPayoutQrBinding{}) {
		var binding entity.RefundPayoutQrBinding
		err := db.Where("attachment_id = ? AND user_id = ?", memory.AttachmentID, actor.ID).Take(&binding).Error
		if err == nil {
			status.KeyID = binding.KeyID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return status, err
		}
	}
	_, preview, err := RefundPayoutQrPreviewWithDB(db, actor)
	if err != nil {
		return status, err
	}
	if len(preview) > 0 {
		status.PreviewDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(preview)
	}
	return status, nil
}

func refundPayoutQrNonce() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func parseRefundPayoutQrPublicKey(encoded string) (*ecdsa.PublicKey, []byte, error) {
	if len(encoded) > 512 {
		return nil, nil, ErrTicketRefundInvalid
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, nil, ErrTicketRefundInvalid
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, nil, ErrTicketRefundInvalid
	}
	public, ok := key.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() || !public.Curve.IsOnCurve(public.X, public.Y) {
		return nil, nil, ErrTicketRefundInvalid
	}
	canonical, err := x509.MarshalPKIXPublicKey(public)
	return public, canonical, err
}

// The attachment's first device binding is immutable, even after replacement or
// deletion. Knowing an attachment ID must never allow changing its device key.
func RegisterRefundPayoutQrWithDB(db *gorm.DB, actor entity.User, attachmentID int64, publicKey string) (RefundPayoutQrRegistration, error) {
	if db == nil || attachmentID <= 0 {
		return RefundPayoutQrRegistration{}, ErrTicketRefundInvalid
	}
	_, der, err := parseRefundPayoutQrPublicKey(publicKey)
	if err != nil {
		return RefundPayoutQrRegistration{}, err
	}
	keyID, err := refundPayoutQrNonce()
	if err != nil {
		return RefundPayoutQrRegistration{}, err
	}
	version, err := refundPayoutQrNonce()
	if err != nil {
		return RefundPayoutQrRegistration{}, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, false, true); err != nil {
			return err
		}
		previous, err := currentRefundPayoutQr(tx, actor.ID)
		if err != nil {
			return err
		}
		var attachment entity.TicketAttachment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND ticket_id = 0 AND message_id IS NULL AND purged_at IS NULL", attachmentID, actor.ID).Take(&attachment).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketRefundInvalid
			}
			return err
		}
		if !ticketAttachmentVisible(attachment) && attachment.ScanState != entity.TicketAttachmentScanPending && attachment.ScanState != entity.TicketAttachmentScanScanning {
			return ErrTicketAttachmentHidden
		}
		protected, err := isHistoricalRefundPayoutQr(tx, attachmentID)
		if err != nil {
			return err
		}
		if protected {
			return ErrRefundPayoutQrRegistered
		}
		binding := entity.RefundPayoutQrBinding{AttachmentID: attachmentID, UserID: actor.ID, KeyID: keyID, PublicKey: der}
		if err := tx.Create(&binding).Error; err != nil {
			return err
		}
		memory := entity.RefundPayoutQrMemory{UserID: actor.ID, AttachmentID: attachmentID, Version: version}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"attachment_id", "version", "updated_at"})}).Create(&memory).Error; err != nil {
			return err
		}
		return purgeUnusedRefundPayoutQrInTx(tx, actor.ID, previous.AttachmentID)
	})
	return RefundPayoutQrRegistration{AttachmentID: attachmentID, KeyID: keyID}, err
}

func CreateRefundPayoutQrChallengeWithDB(db *gorm.DB, actor entity.User) (RefundPayoutQrDeviceChallenge, error) {
	if db == nil {
		return RefundPayoutQrDeviceChallenge{}, ErrTicketRefundInvalid
	}
	id, err := refundPayoutQrNonce()
	if err != nil {
		return RefundPayoutQrDeviceChallenge{}, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return RefundPayoutQrDeviceChallenge{}, err
	}
	now := time.Now()
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, false, true); err != nil {
			return err
		}
		memory, err := currentRefundPayoutQr(tx, actor.ID)
		if err != nil {
			return err
		}
		if memory.AttachmentID == 0 || memory.Version == "" {
			return ErrRefundPayoutQrDevice
		}
		var binding entity.RefundPayoutQrBinding
		if err := tx.Where("attachment_id = ? AND user_id = ?", memory.AttachmentID, actor.ID).Take(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRefundPayoutQrDevice
			}
			return err
		}
		// Independent browser tabs may be unlocking concurrently. Keep their live
		// challenges, while removing consumed and expired rows on the next open.
		if err := tx.Where("user_id = ? AND (expires_at <= ? OR used_at IS NOT NULL)", actor.ID, now).Delete(&entity.RefundPayoutQrChallenge{}).Error; err != nil {
			return err
		}
		row := entity.RefundPayoutQrChallenge{ID: id, UserID: actor.ID, AttachmentID: memory.AttachmentID, MemoryVersion: memory.Version, KeyID: binding.KeyID, Challenge: nonce[:], ExpiresAt: now.Add(2 * time.Minute)}
		return tx.Create(&row).Error
	})
	return RefundPayoutQrDeviceChallenge{ChallengeID: id, Challenge: base64.StdEncoding.EncodeToString(nonce[:])}, err
}

// The challenge is consumed even for an invalid signature. Consuming and
// checking the current memory version in one transaction prevents replay after
// a QR replacement, deletion, or successful unlock.
func UnlockRefundPayoutQrDeviceWithDB(db *gorm.DB, actor entity.User, challengeID, encodedSignature string) (int64, string, []byte, error) {
	if db == nil || len(challengeID) != 64 || len(encodedSignature) > 128 {
		return 0, "", nil, ErrRefundPayoutQrDevice
	}
	signature, decodeErr := base64.StdEncoding.DecodeString(encodedSignature)
	var attachmentID int64
	var mime string
	var body []byte
	var denied error
	now := time.Now()
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, false, true); err != nil {
			return err
		}
		var challenge entity.RefundPayoutQrChallenge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", challengeID, actor.ID).Take(&challenge).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				denied = ErrRefundPayoutQrDevice
				return nil
			}
			return err
		}
		consumed := tx.Model(&entity.RefundPayoutQrChallenge{}).Where("id = ? AND used_at IS NULL AND expires_at > ?", challenge.ID, now).Update("used_at", now)
		if consumed.Error != nil {
			return consumed.Error
		}
		if consumed.RowsAffected != 1 || decodeErr != nil || len(signature) != 64 {
			denied = ErrRefundPayoutQrDevice
			return nil
		}
		memory, err := currentRefundPayoutQr(tx, actor.ID)
		if err != nil {
			return err
		}
		if memory.Version != challenge.MemoryVersion || memory.AttachmentID != challenge.AttachmentID || memory.AttachmentID == 0 {
			denied = ErrRefundPayoutQrDevice
			return nil
		}
		var binding entity.RefundPayoutQrBinding
		if err := tx.Where("attachment_id = ? AND user_id = ? AND key_id = ?", challenge.AttachmentID, actor.ID, challenge.KeyID).Take(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				denied = ErrRefundPayoutQrDevice
				return nil
			}
			return err
		}
		key, err := x509.ParsePKIXPublicKey(binding.PublicKey)
		if err != nil {
			denied = ErrRefundPayoutQrDevice
			return nil
		}
		public, ok := key.(*ecdsa.PublicKey)
		digest := sha256.Sum256(challenge.Challenge)
		if !ok || public.Curve != elliptic.P256() || !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
			denied = ErrRefundPayoutQrDevice
			return nil
		}
		attachmentID, mime, body, denied = readCurrentRefundPayoutQr(tx, actor.ID, memory)
		return nil
	})
	if err != nil {
		return 0, "", nil, err
	}
	if denied != nil {
		return 0, "", nil, denied
	}
	return attachmentID, mime, body, nil
}

func DeleteRefundPayoutQrWithDB(db *gorm.DB, actor entity.User) error {
	if db == nil {
		return ErrTicketRefundInvalid
	}
	version, err := refundPayoutQrNonce()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, false, true); err != nil {
			return err
		}
		previous, err := currentRefundPayoutQr(tx, actor.ID)
		if err != nil {
			return err
		}
		memory := entity.RefundPayoutQrMemory{UserID: actor.ID, Version: version}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"attachment_id", "version", "updated_at"})}).Create(&memory).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", actor.ID).Delete(&entity.RefundPayoutQrChallenge{}).Error; err != nil {
			return err
		}
		return purgeUnusedRefundPayoutQrInTx(tx, actor.ID, previous.AttachmentID)
	})
}

// Replacing/deleting a memory releases only its unused upload source. Keep the
// immutable device binding and any submitted or otherwise-bound evidence. The
// memory update, ciphertext purge, and blob reference release commit together.
func purgeUnusedRefundPayoutQrInTx(tx *gorm.DB, userID int32, attachmentID int64) error {
	if attachmentID <= 0 {
		return nil
	}
	scope := func(db *gorm.DB) *gorm.DB {
		db = db.Where("id = ? AND user_id = ? AND ticket_id = 0 AND message_id IS NULL AND purged_at IS NULL", attachmentID, userID)
		if tx.Session(&gorm.Session{NewDB: true}).Migrator().HasTable(&entity.TicketRefund{}) {
			used := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).Select("id").Where("payout_qr_attachment_id = ? OR signature_attachment_id = ?", attachmentID, attachmentID)
			db = db.Where("NOT EXISTS (?)", used)
		}
		return excludeRememberedRefundPayoutQr(db)
	}
	var attachment entity.TicketAttachment
	if err := scope(tx.Session(&gorm.Session{NewDB: true})).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&attachment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	updates := map[string]any{"ciphertext": nil, "nonce": nil, "purged_at": time.Now()}
	var blobSHA string
	if ticketAttachmentBlobLinkReady(tx) {
		var link entity.TicketAttachmentBlobLink
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", attachmentID).Take(&link).Error; err != nil {
			return err
		}
		blobSHA = NormalizeTicketAttachmentBlobSHA256(link.BlobSHA256)
		updates["blob_sha256"] = ""
	}
	result := scope(tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachment{})).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 || blobSHA == "" || !ticketAttachmentBlobReady(tx) {
		return nil
	}
	_, err := ReleaseTicketAttachmentBlobInTx(tx, blobSHA)
	return err
}

func readCurrentRefundPayoutQr(db *gorm.DB, userID int32, memory entity.RefundPayoutQrMemory) (int64, string, []byte, error) {
	if memory.AttachmentID == 0 {
		return 0, "", nil, nil
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ? AND user_id = ? AND purged_at IS NULL", memory.AttachmentID, userID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, "", nil, ErrTicketRefundNotFound
		}
		return 0, "", nil, err
	}
	// A freshly uploaded remembered draft is useful immediately. All other
	// images still require approval, and flagged/failed images are never exposed.
	registeredDraft := memory.Version != "" && row.TicketID == 0 && row.MessageID == nil &&
		(row.ScanState == entity.TicketAttachmentScanPending || row.ScanState == entity.TicketAttachmentScanScanning)
	if !ticketAttachmentVisible(row) && !registeredDraft {
		return 0, "", nil, ErrTicketAttachmentHidden
	}
	body, mime, err := decryptTicketAttachmentForScan(db, row)
	return row.ID, mime, body, err
}
