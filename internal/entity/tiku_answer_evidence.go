package entity

import "time"

const TableNameTikuAnswerEvidence = "tiku_answer_evidence"

// TikuAnswerEvidence stores independently sourced answers. It intentionally
// does not update the legacy tiku row, so contradictory evidence can coexist.
type TikuAnswerEvidence struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	EvidenceKey  string    `gorm:"column:evidence_key;type:char(64);not null;uniqueIndex" json:"evidenceKey"`
	QuestionHash string    `gorm:"column:question_hash;type:char(32);index" json:"questionHash"`
	Question     string    `gorm:"column:question;type:longtext;not null" json:"question"`
	Type         int       `gorm:"column:type;not null;index" json:"type"`
	Options      string    `gorm:"column:options;type:longtext;not null" json:"options"`
	Answer       string    `gorm:"column:answer;type:longtext;not null" json:"answer"`
	CourseName   string    `gorm:"column:course_name;type:varchar(255);not null;default:'';index" json:"courseName"`
	Source       string    `gorm:"column:source;type:varchar(128);not null;index" json:"source"`
	Evidence     string    `gorm:"column:evidence;type:longtext;not null" json:"evidence"`
	Status       string    `gorm:"column:status;type:varchar(32);not null;default:'unverified';index" json:"status"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (*TikuAnswerEvidence) TableName() string { return TableNameTikuAnswerEvidence }
