package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAgreementProof    = errors.New("agreement device or owner proof required")
	ErrAgreementConflict = errors.New("agreement revision or version changed")
	ErrAgreementInput    = errors.New("invalid agreement input")
	agreementVersion     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)
)

type AgreementStore struct{ DB *gorm.DB }
type AgreementCredentials struct{ Secret, Authorization, Identity, IP string }
type AgreementStatus struct {
	Version         string `json:"version"`
	DeviceID        string `json:"deviceId"`
	NeedsAcceptance bool   `json:"needsAcceptance"`
	ContentHash     string `json:"content_hash"`
}

// AgreementContentHash hashes the exact UTF-8 body of a published release.
func AgreementContentHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (s AgreementStore) SaveDraft(id, rev int64, content entity.AgreementContent) (d entity.AgreementDraft, err error) {
	if !agreementVersion.MatchString(content.Version) || strings.TrimSpace(content.Summary) == "" || len(content.Summary) > 8192 || strings.TrimSpace(content.Body) == "" || len(content.Body) > 256<<10 {
		return d, ErrAgreementInput
	}
	if id == 0 {
		if rev != 0 {
			return d, ErrAgreementInput
		}
		d = entity.AgreementDraft{AgreementContent: content, Revision: 1}
		err = s.DB.Create(&d).Error
		return
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; e != nil {
			return e
		}
		if d.Revision != rev {
			return ErrAgreementConflict
		}
		var published int64
		if e := tx.Model(&entity.AgreementRelease{}).Where("draft_id = ?", id).Count(&published).Error; e != nil {
			return e
		}
		if published != 0 {
			return ErrAgreementConflict
		}
		d.Revision++
		d.AgreementContent = content
		return tx.Save(&d).Error
	})
	return
}
func (s AgreementStore) DeleteDraft(id int64) error {
	r := s.DB.Delete(&entity.AgreementDraft{}, id)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
func (s AgreementStore) Publish(id, rev int64) (r entity.AgreementRelease, err error) {
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		var h entity.AgreementHead
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, 1).Error; e != nil {
			return e
		}
		// The immutable release is the idempotency receipt even after draft deletion.
		var prior []entity.AgreementRelease
		if e := tx.Where("draft_id = ?", id).Limit(1).Find(&prior).Error; e != nil {
			return e
		}
		if len(prior) > 0 {
			r = prior[0]
			if r.Revision != rev {
				return ErrAgreementConflict
			}
			return nil
		}
		var d entity.AgreementDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; e != nil {
			return e
		}
		if rev != d.Revision {
			return ErrAgreementConflict
		}
		var used int64
		if e := tx.Model(&entity.AgreementRelease{}).Where("version = ?", d.Version).Count(&used).Error; e != nil {
			return e
		}
		if used != 0 {
			return ErrAgreementConflict
		}
		r = entity.AgreementRelease{DraftID: id, Revision: rev, Version: d.Version, Summary: d.Summary, Body: d.Body, PublishedAt: time.Now().UTC()}
		if e := tx.Create(&r).Error; e != nil {
			return e
		}
		return tx.Model(&h).Update("release_id", r.ID).Error
	})
	return
}
func (s AgreementStore) Current() (r entity.AgreementRelease, err error) {
	err = s.DB.Joins("JOIN agreement_heads h ON h.id = 1 AND h.release_id = agreement_releases.id").First(&r).Error
	return
}

// The public ID is never accepted as proof. Only this independently random, scoped
// secret authorizes a server-issued logical installation (not a physical fingerprint).
func (s AgreementStore) Enroll() (d entity.AgreementDevice, secret string, err error) {
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return
	}
	id, e := uuid.NewRandom()
	if e != nil {
		return d, "", e
	}
	secret = "agd_" + base64.RawURLEncoding.EncodeToString(raw[:])
	d = entity.AgreementDevice{ID: id.String(), SecretHash: agreementDigest(secret), ClientKind: ClientKindUnknown}
	if err = s.DB.Create(&d).Error; err != nil {
		return d, "", err
	}
	return
}
func agreementDigest(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// Allow an anonymous installation, or require an actual account/lease proof. A
// bound installation always needs BOTH proofs; an invalid supplied proof never
// falls back to anonymous. Device registration, heartbeat and billing are not run.
func agreementPrincipal(tx *gorm.DB, c AgreementCredentials, write bool) (d entity.AgreementDevice, owner *int32, kind string, err error) {
	if !strings.HasPrefix(c.Secret, "agd_") || len(c.Secret) != 47 {
		return d, nil, "", ErrAgreementProof
	}
	q := tx
	if write {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Where("secret_hash = ?", agreementDigest(c.Secret)).First(&d).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			e = ErrAgreementProof
		}
		return d, nil, "", e
	}
	kind = ClientKindUnknown
	if c.Authorization != "" || c.Identity != "" {
		parts := strings.Fields(c.Authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return d, nil, "", ErrAgreementProof
		}
		var id int32
		var e error
		if c.Identity != "" {
			id, e = ResolveLeasedDiagnosticOwner(tx, c.Authorization, c.Identity, c.IP)
			kind = ClientKindExamClient
		} else {
			var ctx AccessTokenAuthContext
			ctx, e = ResolveAccessTokenFromBasicAuth(tx, c.Authorization)
			id = ctx.User.ID
			// A normal key proves the account, not the software that sent it.
		}
		if e != nil {
			if errors.Is(e, ErrInvalidAccessTokenAuthorization) {
				e = ErrAgreementProof
			}
			return d, nil, "", e
		}
		if id <= 0 {
			return d, nil, "", ErrAgreementProof
		}
		owner = &id
	}
	if d.UserID != nil && (owner == nil || *owner != *d.UserID) {
		return d, nil, "", ErrAgreementProof
	}
	return
}
func (s AgreementStore) Status(c AgreementCredentials) (out AgreementStatus, err error) {
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		d, _, _, e := agreementPrincipal(tx, c, false)
		if e != nil {
			return e
		}
		r, e := (AgreementStore{tx}).Current()
		if e != nil {
			return e
		}
		var n int64
		if e = tx.Model(&entity.AgreementConsent{}).Where("release_id = ? AND device_id = ?", r.ID, d.ID).Count(&n).Error; e != nil {
			return e
		}
		out = AgreementStatus{Version: r.Version, DeviceID: d.ID, NeedsAcceptance: n == 0, ContentHash: AgreementContentHash(r.Body)}
		return nil
	})
	return
}
func (s AgreementStore) Accept(c AgreementCredentials, version string) (receipt entity.AgreementConsent, err error) {
	if !agreementVersion.MatchString(version) {
		return receipt, ErrAgreementInput
	}
	ip, e := netip.ParseAddr(c.IP)
	if e != nil || ip.Zone() != "" {
		return receipt, ErrAgreementInput
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		// Shared read lock prevents the current version from advancing between check
		// and consent commit; lock the device before binding it or reading its receipt.
		var h entity.AgreementHead
		if e := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&h, 1).Error; e != nil {
			return e
		}
		d, owner, kind, e := agreementPrincipal(tx, c, true)
		if e != nil {
			return e
		}
		if h.ReleaseID == 0 {
			return gorm.ErrRecordNotFound
		}
		var r entity.AgreementRelease
		if e = tx.First(&r, h.ReleaseID).Error; e != nil {
			return e
		}
		if r.Version != version {
			return ErrAgreementConflict
		}
		if d.UserID == nil && owner != nil {
			if e = tx.Model(&d).Updates(map[string]any{"user_id": *owner, "client_kind": kind}).Error; e != nil {
				return e
			}
			if e = tx.Model(&entity.AgreementConsent{}).Where("device_id = ? AND user_id IS NULL", d.ID).Update("user_id", *owner).Error; e != nil {
				return e
			}
		}
		if kind == ClientKindExamClient && d.ClientKind != kind {
			if e = tx.Model(&d).Update("client_kind", kind).Error; e != nil {
				return e
			}
		}
		var found []entity.AgreementConsent
		if e = tx.Where("release_id = ? AND device_id = ?", r.ID, d.ID).Limit(1).Find(&found).Error; e != nil {
			return e
		}
		if len(found) > 0 {
			receipt = found[0]
			return nil
		}
		receipt = entity.AgreementConsent{ReleaseID: r.ID, Version: r.Version, DeviceID: d.ID, UserID: owner, ClientKind: kind, IP: ip.Unmap().String(), AcceptedAt: time.Now().UTC()}
		if e = tx.Create(&receipt).Error; e != nil {
			return e
		}
		// Read back database timestamp precision so a response-lost retry is identical.
		return tx.First(&receipt, receipt.ID).Error
	})
	return
}
