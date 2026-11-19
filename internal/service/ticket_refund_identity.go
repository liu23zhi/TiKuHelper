package service

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"golang.org/x/image/draw"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRefundAgreementRequired = errors.New("人工退款需阅读并重新签署当前协议")
	ErrRefundPhoneVerification = errors.New("请验证当前绑定手机号后再提交人工退款")
)

type RefundPhoneVerificationStatus struct {
	Required    bool   `json:"required"`
	PhoneMasked string `json:"phoneMasked"`
}

func RefundPhoneVerificationStatusWithDB(db *gorm.DB, actor entity.User) (RefundPhoneVerificationStatus, error) {
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return RefundPhoneVerificationStatus{}, err
	}
	return refundPhoneVerificationStatus(db, fresh)
}

func refundPhoneVerificationStatus(db *gorm.DB, actor entity.User) (RefundPhoneVerificationStatus, error) {
	status := RefundPhoneVerificationStatus{}
	if actor.Phone == nil {
		return status, nil
	}
	phone, err := NormalizeMainlandPhoneInput(*actor.Phone)
	if err != nil {
		return status, nil
	}
	status.PhoneMasked = MaskPhone(phone)
	values := make(map[string]string, 3)
	for _, key := range []string{SMSLoginEnabledSettingKey, SMSBaoUsernameSettingKey, SMSBaoSecretSettingKey} {
		setting, exists, err := getSystemSettingByKey(db, key)
		if err != nil {
			return status, err
		}
		if exists {
			values[key] = strings.TrimSpace(setting.Value)
		}
	}
	status.Required = strings.EqualFold(values[SMSLoginEnabledSettingKey], "true") && values[SMSBaoUsernameSettingKey] != "" && values[SMSBaoSecretSettingKey] != ""
	return status, nil
}

func SendRefundPhoneSMSCodeWithDB(db *gorm.DB, actor entity.User, requestIP string) error {
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return err
	}
	status, err := refundPhoneVerificationStatus(db, fresh)
	if err != nil {
		return err
	}
	if !status.Required {
		return ErrRefundPhoneVerification
	}
	return SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: *fresh.Phone, Scene: SMSSceneRefund, TargetUserID: fresh.ID, RequestIP: requestIP})
}

// All attachment checks share the creation transaction. A signature is a fresh
// draft that can be claimed exactly once; it cannot be borrowed from an old refund.
func validateRefundAttachmentsInTx(tx *gorm.DB, actor entity.User, input CreateTicketRefundInput, refund *entity.TicketRefund, now time.Time) error {
	if input.Kind == TicketRefundKindManual {
		if input.PayoutQrAttachmentID == nil {
			return ErrTicketRefundPayoutQrRequired
		}
		if input.SignatureAttachmentID == nil || strings.TrimSpace(input.AgreementText) != GetRefundAgreementTextWithDB(tx) {
			return ErrRefundAgreementRequired
		}
		if *input.SignatureAttachmentID == *input.PayoutQrAttachmentID {
			return ErrRefundAgreementRequired
		}
		status, err := refundPhoneVerificationStatus(tx, actor)
		if err != nil {
			return err
		}
		if status.Required {
			if err := VerifySMSCodeInTx(tx, *actor.Phone, SMSSceneRefund, input.PhoneSMSCode); err != nil {
				return ErrRefundPhoneVerification
			}
		}
		refund.AgreementText = strings.TrimSpace(input.AgreementText)
		refund.AgreementSignedAt = &now
	}
	for _, id := range []*int64{input.PayoutQrAttachmentID, input.SignatureAttachmentID} {
		if id == nil {
			continue
		}
		var attachment entity.TicketAttachment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND ticket_id = 0 AND message_id IS NULL AND purged_at IS NULL", *id, actor.ID).Take(&attachment).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketRefundInvalid
			}
			return err
		}
		var used int64
		if err := tx.Model(&entity.TicketRefund{}).Where("payout_qr_attachment_id = ? OR signature_attachment_id = ?", *id, *id).Count(&used).Error; err != nil {
			return err
		}
		if used != 0 {
			return ErrTicketRefundInvalid
		}
	}
	return nil
}

// ReadLatestRefundPayoutQrWithDB is only for the password-verified recovery
// endpoint. Never expose the returned source bytes through the preview endpoint.
func ReadLatestRefundPayoutQrWithDB(db *gorm.DB, actor entity.User) (int64, string, []byte, error) {
	if _, err := ticketRefundActor(db, actor.ID, false, false); err != nil {
		return 0, "", nil, err
	}
	memory, err := currentRefundPayoutQr(db, actor.ID)
	if err != nil {
		return 0, "", nil, err
	}
	return readCurrentRefundPayoutQr(db, actor.ID, memory)
}

func RefundPayoutQrPreviewWithDB(db *gorm.DB, actor entity.User) (bool, []byte, error) {
	if _, err := ticketRefundActor(db, actor.ID, false, false); err != nil {
		return false, nil, err
	}
	memory, err := currentRefundPayoutQr(db, actor.ID)
	if err != nil || memory.AttachmentID == 0 {
		return false, nil, err
	}
	_, _, body, err := ReadLatestRefundPayoutQrWithDB(db, actor)
	if errors.Is(err, ErrTicketAttachmentHidden) || errors.Is(err, ErrTicketRefundNotFound) {
		return true, nil, nil
	}
	if err != nil {
		return true, nil, err
	}
	preview, err := blurredRefundPayoutQr(body)
	return true, preview, err
}

// Reduce to a tiny thumbnail before Gaussian smoothing. No original pixels or
// metadata survive, so removing CSS filters cannot reveal a usable QR code.
func blurredRefundPayoutQr(body []byte) ([]byte, error) {
	source, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, ErrTicketAttachmentInvalid
	}
	const side = 12
	thumb := image.NewNRGBA(image.Rect(0, 0, side, side))
	draw.ApproxBiLinear.Scale(thumb, thumb.Bounds(), source, source.Bounds(), draw.Over, nil)
	blurred := image.NewNRGBA(thumb.Bounds())
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			var r, g, b, weight float64
			for oy := -5; oy <= 5; oy++ {
				for ox := -5; ox <= 5; ox++ {
					px, py := min(side-1, max(0, x+ox)), min(side-1, max(0, y+oy))
					c := thumb.NRGBAAt(px, py)
					w := math.Exp(-float64(ox*ox+oy*oy) / 8)
					// Composite transparent content on white, matching the visible image.
					a := float64(c.A) / 255
					r += (float64(c.R)*a + 255*(1-a)) * w
					g += (float64(c.G)*a + 255*(1-a)) * w
					b += (float64(c.B)*a + 255*(1-a)) * w
					weight += w
				}
			}
			blurred.SetNRGBA(x, y, color.NRGBA{R: uint8(r / weight), G: uint8(g / weight), B: uint8(b / weight), A: 255})
		}
	}
	output := image.NewNRGBA(image.Rect(0, 0, 240, 240))
	draw.BiLinear.Scale(output, output.Bounds(), blurred, blurred.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, output); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func isHistoricalRefundPayoutQr(db *gorm.DB, attachmentID int64) (bool, error) {
	if db.Migrator().HasTable(&entity.RefundPayoutQrBinding{}) {
		var count int64
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RefundPayoutQrBinding{}).Where("attachment_id = ?", attachmentID).Count(&count).Error; err != nil || count > 0 {
			return count > 0, err
		}
	}
	if !db.Migrator().HasTable(&entity.TicketRefund{}) {
		return false, nil
	}
	var count int64
	err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).Where("payout_qr_attachment_id = ?", attachmentID).Count(&count).Error
	return count > 0, err
}
