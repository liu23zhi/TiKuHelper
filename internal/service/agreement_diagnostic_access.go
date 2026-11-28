package service

import (
	"errors"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDiagnosticConsent = errors.New("current diagnostic agreement not accepted")

// WithDiagnosticConsent resolves a current account or lease and a real
// installation receipt. The published head cannot advance before use completes.
// No registration, accounting, lease renewal or consent write is performed.
func (s AgreementStore) WithDiagnosticConsent(c AgreementCredentials, use func(owner int32) error) error {
	if c.Secret == "" || c.Authorization == "" {
		return ErrAgreementProof
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var head entity.AgreementHead
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&head, 1).Error; err != nil {
			return err
		}
		d, owner, kind, err := agreementPrincipal(tx, c, false)
		if err != nil {
			return err
		}
		if owner == nil {
			return ErrAgreementProof
		}
		if head.ReleaseID == 0 {
			return ErrDiagnosticConsent
		}
		var release entity.AgreementRelease
		if err := tx.Select("id", "version", "body").Take(&release, head.ReleaseID).Error; err != nil {
			return err
		}
		if !IsApprovedDiagnosticAgreement(release.Version, AgreementContentHash(release.Body)) {
			return ErrDiagnosticConsent
		}
		var consent entity.AgreementConsent
		err = tx.Select("user_id", "client_kind").Where("release_id = ? AND device_id = ?", head.ReleaseID, d.ID).Take(&consent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDiagnosticConsent
		}
		if err != nil {
			return err
		}
		// Anonymous plain-extension consent remains scoped to the same private
		// installation proof. A lease must have expressly accepted as that owner.
		if (consent.UserID != nil && *consent.UserID != *owner) ||
			(kind == ClientKindExamClient && (consent.UserID == nil || consent.ClientKind != kind)) ||
			(kind != ClientKindExamClient && consent.ClientKind == ClientKindExamClient) {
			return ErrDiagnosticConsent
		}
		return use(*owner)
	})
}
