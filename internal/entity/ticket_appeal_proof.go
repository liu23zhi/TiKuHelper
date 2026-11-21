package entity

import "time"

// TicketAppealProof holds the current short-lived verification for a submitter.
type TicketAppealProof struct {
	ActorID              int32     `gorm:"column:actor_id;primaryKey;autoIncrement:false" json:"-"`
	SessionHash          string    `gorm:"column:session_hash;type:char(64);not null" json:"-"`
	TargetUserID         int32     `gorm:"column:target_user_id;not null" json:"-"`
	TargetCredentialHash string    `gorm:"column:target_credential_hash;type:char(64);not null;default:''" json:"-"`
	ProofHash            string    `gorm:"column:proof_hash;type:char(64);not null" json:"-"`
	ExpiresAt            time.Time `gorm:"column:expires_at;not null" json:"-"`
}

func (*TicketAppealProof) TableName() string { return "ticket_appeal_proofs" }
