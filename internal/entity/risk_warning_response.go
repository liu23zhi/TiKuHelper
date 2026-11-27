package entity

import "time"

// RiskWarningResponse records the requested decision and actual outcome; actor 0 is the detector policy.
// EntryID points to the ordinary blacklist rule, even after it expires or is deleted.
type RiskWarningResponse struct {
	RuleID           string     `gorm:"type:varchar(64);not null;default:''" json:"ruleId,omitempty"`
	SourceIPMasked   string     `gorm:"type:varchar(64);not null;default:''" json:"sourceIpMasked,omitempty"`
	OutcomeCode      string     `gorm:"type:varchar(32);not null;default:''" json:"outcomeCode,omitempty"`
	LinkedResponseID int64      `gorm:"not null;default:0" json:"linkedResponseId,omitempty"`
	EntryVersion     *time.Time `json:"-"`
	CanRevoke        bool       `gorm:"-" json:"canRevoke,omitempty"`
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	WarningID        int64      `gorm:"not null;index:idx_risk_response_warning" json:"warningId"`
	Action           string     `gorm:"type:varchar(16);not null" json:"action"`
	Target           string     `gorm:"type:varchar(191);not null" json:"target"`
	DurationMinutes  int        `gorm:"not null" json:"durationMinutes"`
	ActorID          int32      `gorm:"not null" json:"actorId"`
	Reason           string     `gorm:"type:varchar(255);not null" json:"reason"`
	Status           string     `gorm:"type:varchar(16);not null" json:"status"`
	Message          string     `gorm:"type:varchar(255);not null" json:"message"`
	EntryID          int64      `gorm:"index:idx_risk_response_entry;not null;default:0" json:"entryId,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func (*RiskWarningResponse) TableName() string { return "risk_warning_responses" }
