package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxTicketRefundProofBytes = 2 << 20

var (
	ErrTicketRefundProofDisabled = errors.New("退款凭证加密未配置")
	ErrTicketRefundProofInvalid  = errors.New("凭证必须是有效的 PNG/JPEG 图片，且不超过 2 MiB")
	ErrTicketRefundProofExists   = errors.New("凭证已提交，不可覆盖")
	ErrTicketRefundProofRequired = errors.New("人工退款审核前须核验受限凭证")
)

func ticketRefundProofCipher() (cipher.AEAD, error) {
	raw := strings.TrimSpace(os.Getenv("TIKU_REFUND_PROOF_KEY"))
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(key) != 32 {
		return nil, ErrTicketRefundProofDisabled
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrTicketRefundProofDisabled
	}
	return cipher.NewGCM(block)
}

func safeTicketRefundProof(data []byte) (string, []byte, error) {
	if len(data) == 0 || len(data) > MaxTicketRefundProofBytes {
		return "", nil, ErrTicketRefundProofInvalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 4_000_000 {
		return "", nil, ErrTicketRefundProofInvalid
	}
	img, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded != format {
		return "", nil, ErrTicketRefundProofInvalid
	}
	// Re-encode, eliminating embedded metadata and appended content.
	var out bytes.Buffer
	mimeType := "image/png"
	if format == "jpeg" {
		mimeType = "image/jpeg"
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 92})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil || out.Len() > MaxTicketRefundProofBytes {
		return "", nil, ErrTicketRefundProofInvalid
	}
	return mimeType, out.Bytes(), nil
}

func refundProofAAD(id int64, ownerID int32, mimeType string) []byte {
	return []byte(fmt.Sprintf("tikuhelper-refund-proof-v1:%d:%d:%s", id, ownerID, mimeType))
}

func StoreTicketRefundProofWithDB(db *gorm.DB, actor entity.User, id int64, data []byte) error {
	// Authorize before expending work decoding hostile content.
	refund, err := GetTicketRefundWithDB(db, actor, id)
	if err != nil {
		return err
	}
	if refund.UserID != actor.ID || refund.Kind != TicketRefundKindManual {
		return ErrTicketRefundForbidden
	}
	if refund.Status != TicketRefundStatusPendingReview {
		return ErrTicketRefundState
	}
	aead, err := ticketRefundProofCipher()
	if err != nil {
		return err
	}
	mimeType, safe, err := safeTicketRefundProof(data)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := aead.Seal(nil, nonce, safe, refundProofAAD(id, actor.ID, mimeType))
	return db.Transaction(func(tx *gorm.DB) error {
		var current entity.TicketRefund
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND kind = ?", id, actor.ID, TicketRefundKindManual).First(&current).Error
		if err != nil {
			return err
		}
		if current.Status != TicketRefundStatusPendingReview {
			return ErrTicketRefundState
		}
		var owner entity.User
		if err := tx.Select("id", "status").First(&owner, actor.ID).Error; err != nil {
			return err
		}
		if owner.Status != "enabled" {
			return ErrTicketRefundForbidden
		}
		var count int64
		if err := tx.Model(&entity.TicketRefundProof{}).Where("refund_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrTicketRefundProofExists
		}
		return tx.Create(&entity.TicketRefundProof{RefundID: id, UserID: actor.ID,
			MediaType: mimeType, Nonce: nonce, Ciphertext: ciphertext}).Error
	})
}

func ReadTicketRefundProofWithDB(db *gorm.DB, actor entity.User, id int64) (string, []byte, error) {
	refund, err := GetTicketRefundWithDB(db, actor, id)
	if err != nil {
		return "", nil, err
	}
	var proof entity.TicketRefundProof
	err = db.Where("refund_id = ? AND user_id = ?", id, refund.UserID).First(&proof).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil, ErrTicketRefundNotFound
	}
	if err != nil {
		return "", nil, err
	}
	aead, err := ticketRefundProofCipher()
	if err != nil {
		return "", nil, err
	}
	if len(proof.Nonce) != aead.NonceSize() || len(proof.Ciphertext) > MaxTicketRefundProofBytes+aead.Overhead() ||
		(proof.MediaType != "image/png" && proof.MediaType != "image/jpeg") {
		return "", nil, ErrTicketRefundProofInvalid
	}
	body, err := aead.Open(nil, proof.Nonce, proof.Ciphertext, refundProofAAD(id, refund.UserID, proof.MediaType))
	if err != nil {
		return "", nil, ErrTicketRefundProofInvalid
	}
	return proof.MediaType, body, nil
}
