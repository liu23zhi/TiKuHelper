package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type TikuAnswerEvidenceInput struct {
	QID        string          `json:"qid"`
	Question   string          `json:"question"`
	Type       int             `json:"type"`
	Options    []string        `json:"options"`
	Answer     json.RawMessage `json:"answer"`
	CourseName string          `json:"courseName"`
	Source     string          `json:"source"`
	Evidence   json.RawMessage `json:"evidence"`
}

var ErrInvalidTikuAnswerEvidence = errors.New("invalid tiku answer evidence")

func invalidTikuAnswerEvidence(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidTikuAnswerEvidence, message)
}

func SaveTikuAnswerEvidenceWithDB(db *gorm.DB, input TikuAnswerEvidenceInput) (entity.TikuAnswerEvidence, bool, error) {
	if db == nil {
		return entity.TikuAnswerEvidence{}, false, errors.New("题库数据库未初始化")
	}
	input.QID = strings.TrimSpace(input.QID)
	input.Question = strings.TrimSpace(input.Question)
	input.CourseName = strings.TrimSpace(input.CourseName)
	input.Source = strings.TrimSpace(input.Source)
	answers, err := normalizeTikuEvidenceAnswers(input.Answer)
	if err != nil || input.Question == "" || input.Source == "" || len(answers) == 0 {
		return entity.TikuAnswerEvidence{}, false, invalidTikuAnswerEvidence("题干、来源和答案不能为空")
	}
	if len([]rune(input.Question)) > 12000 || len([]rune(input.QID)) > 255 || len([]rune(input.CourseName)) > 255 || len([]rune(input.Source)) > 128 || len(input.Options) > 100 || len(answers) > 100 {
		return entity.TikuAnswerEvidence{}, false, invalidTikuAnswerEvidence("题面、选项或来源超出允许长度")
	}
	for _, values := range [][]string{input.Options, answers} {
		for _, value := range values {
			if len([]rune(value)) > 12000 {
				return entity.TikuAnswerEvidence{}, false, invalidTikuAnswerEvidence("单项选项或答案超出12000个字符")
			}
		}
	}
	if input.Type < -4 || input.Type > 100 {
		return entity.TikuAnswerEvidence{}, false, invalidTikuAnswerEvidence("题型编号无效")
	}
	if len(input.Evidence) == 0 || !json.Valid(input.Evidence) || len(input.Evidence) > 64*1024 {
		return entity.TikuAnswerEvidence{}, false, invalidTikuAnswerEvidence("平台证据须为不超过64KB的有效JSON")
	}
	var proof map[string]any
	if json.Unmarshal(input.Evidence, &proof) != nil || len(proof) == 0 {
		return entity.TikuAnswerEvidence{}, false, invalidTikuAnswerEvidence("平台证据须为非空JSON对象")
	}
	optionsJSON, err := json.Marshal(input.Options)
	if err != nil {
		return entity.TikuAnswerEvidence{}, false, err
	}
	answerJSON, err := json.Marshal(answers)
	if err != nil {
		return entity.TikuAnswerEvidence{}, false, err
	}
	evidenceJSON, err := json.Marshal(proof)
	if err != nil {
		return entity.TikuAnswerEvidence{}, false, err
	}
	questionHash := util.BuildTikuHashCandidates(input.Question, input.Options, input.Type)[0]
	identity, err := json.Marshal(struct {
		QID, Question, Type, Options, Answer, CourseName, Source string
		Evidence                                                 json.RawMessage
	}{input.QID, input.Question, fmt.Sprint(input.Type), string(optionsJSON), string(answerJSON), input.CourseName, input.Source, evidenceJSON})
	if err != nil {
		return entity.TikuAnswerEvidence{}, false, err
	}
	digest := sha256.Sum256(identity)
	record := entity.TikuAnswerEvidence{
		EvidenceKey: hex.EncodeToString(digest[:]), QuestionHash: questionHash, Question: input.Question,
		Type: input.Type, Options: string(optionsJSON), Answer: string(answerJSON), CourseName: input.CourseName,
		Source: input.Source, Evidence: string(evidenceJSON), Status: "unverified",
	}
	// A unique-key insert distinguishes creation from a retry on both SQLite and
	// MySQL, including MySQL DSNs with clientFoundRows enabled.
	result := db.Create(&record)
	if result.Error != nil {
		var existing entity.TikuAnswerEvidence
		if err := db.Where("evidence_key = ?", record.EvidenceKey).First(&existing).Error; err == nil {
			return existing, false, nil
		}
		return entity.TikuAnswerEvidence{}, false, result.Error
	}
	return record, true, nil
}

func normalizeTikuEvidenceAnswers(raw json.RawMessage) ([]string, error) {
	var answers []string
	if err := json.Unmarshal(raw, &answers); err == nil {
		for index := range answers {
			answers[index] = strings.TrimSpace(answers[index])
			if answers[index] == "" {
				return nil, errors.New("answer is empty")
			}
		}
		return answers, nil
	}
	var answer string
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil, errors.New("answer is empty")
	}
	return []string{answer}, nil
}
