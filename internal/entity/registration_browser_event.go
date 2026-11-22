package entity

const TableNameRegistrationBrowserEvent = "registration_browser_events"

// A committed public registration has at most one event. Separate from the
// authenticated device quota/bindings: no hardware fingerprints or raw cookies.
type RegistrationBrowserEvent struct {
	UserID      int32  `gorm:"column:user_id;primaryKey;autoIncrement:false"`
	BrowserHash string `gorm:"column:browser_hash;type:char(64);not null"`
	// Empty means unknown/unattributable; those accounts still count for the browser.
	ClientIP string `gorm:"column:client_ip;type:varchar(45);not null;default:''"`
	// Epoch seconds keep the rolling window indexable on SQLite and MySQL.
	RegisteredUnix int64 `gorm:"column:registered_unix;not null;index:idx_registration_browser_time"`
}

func (RegistrationBrowserEvent) TableName() string { return TableNameRegistrationBrowserEvent }
