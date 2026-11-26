package service

import (
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

type AgreementFilter struct {
	DeviceID, Version, ClientKind string
	UserID                        *int32
	Unbound                       bool
	Accepted                      *bool
	Limit, Offset                 int
}
type AgreementRecord struct {
	DeviceID   string     `json:"deviceId"`
	UserID     *int32     `json:"userId"`
	Version    string     `json:"version"`
	ClientKind string     `json:"clientKind"`
	Accepted   bool       `json:"accepted"`
	AcceptedAt *time.Time `json:"acceptedAt"`
	IP         *string    `json:"ip"`
}

// One row per enrolled device for the selected version. Absence is not a
// rejection event: it has no invented timestamp/IP. Default version is current.
func (s AgreementStore) Records(f AgreementFilter) (rows []AgreementRecord, err error) {
	if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 {
		return nil, ErrAgreementInput
	}
	var r entity.AgreementRelease
	if f.Version == "" {
		r, err = s.Current()
	} else {
		err = s.DB.Where("version = ?", f.Version).First(&r).Error
	}
	if err != nil {
		return nil, err
	}
	q := s.DB.Table("agreement_devices AS d").
		Select("d.id AS device_id, d.user_id, ? AS version, COALESCE(c.client_kind,d.client_kind) AS client_kind, (c.id IS NOT NULL) AS accepted, c.accepted_at, c.ip", r.Version).
		Joins("LEFT JOIN agreement_consents c ON c.device_id = d.id AND c.release_id = ?", r.ID)
	if f.DeviceID != "" {
		q = q.Where("d.id = ?", f.DeviceID)
	}
	if f.Unbound {
		q = q.Where("d.user_id IS NULL")
	} else if f.UserID != nil {
		q = q.Where("d.user_id = ?", *f.UserID)
	}
	if f.ClientKind != "" {
		q = q.Where("COALESCE(c.client_kind,d.client_kind) = ?", f.ClientKind)
	}
	if f.Accepted != nil {
		if *f.Accepted {
			q = q.Where("c.id IS NOT NULL")
		} else {
			q = q.Where("c.id IS NULL")
		}
	}
	rows = []AgreementRecord{}
	err = q.Order("d.id").Limit(f.Limit).Offset(f.Offset).Scan(&rows).Error
	return
}
