package entity

import "time"

// Only drafts are editable; releases contain their own complete copy.
type AgreementContent struct {
	Version string `gorm:"size:48;not null" json:"version"`
	Summary string `gorm:"type:text;not null" json:"summary"`
	Body    string `gorm:"type:mediumtext;not null" json:"body"`
}
type AgreementDraft struct {
	ID               int64 `gorm:"primaryKey" json:"id"`
	Revision         int64 `gorm:"not null" json:"revision"`
	AgreementContent `gorm:"embedded"`
}
type AgreementRelease struct {
	ID          int64     `gorm:"primaryKey" json:"id"`
	DraftID     int64     `gorm:"uniqueIndex;not null" json:"draftId"`
	Revision    int64     `gorm:"not null" json:"revision"`
	Version     string    `gorm:"size:48;not null;uniqueIndex" json:"version"`
	Summary     string    `gorm:"type:text;not null" json:"summary"`
	Body        string    `gorm:"type:mediumtext;not null" json:"body"`
	PublishedAt time.Time `gorm:"not null" json:"publishedAt"`
}

// One seeded row, never a default publication.
type AgreementHead struct {
	ID        int   `gorm:"primaryKey;autoIncrement:false"`
	ReleaseID int64 `gorm:"not null;default:0"`
}
type AgreementDevice struct {
	ID         string `gorm:"primaryKey;size:36" json:"deviceId"`
	SecretHash string `gorm:"size:64;not null;uniqueIndex" json:"-"`
	UserID     *int32 `gorm:"index" json:"userId"`
	ClientKind string `gorm:"size:24;not null" json:"clientKind"`
}
type AgreementConsent struct {
	ID         int64     `gorm:"primaryKey" json:"id"`
	ReleaseID  int64     `gorm:"not null;uniqueIndex:uk_agreement_device_version" json:"releaseId"`
	DeviceID   string    `gorm:"size:36;not null;uniqueIndex:uk_agreement_device_version" json:"deviceId"`
	UserID     *int32    `gorm:"index" json:"userId"`
	Version    string    `gorm:"size:48;not null" json:"version"`
	ClientKind string    `gorm:"size:24;not null" json:"clientKind"`
	AcceptedAt time.Time `gorm:"not null" json:"acceptedAt"`
	IP         string    `gorm:"size:45;not null" json:"ip"`
}
