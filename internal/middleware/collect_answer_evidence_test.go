package middleware

import (
	"encoding/json"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestCollectAIAnswerCannotOverwritePlatformEvidence(t *testing.T) {
	db := newCollectAnswerTestDB(t)
	if err := db.AutoMigrate(&entity.TikuAnswerEvidence{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	evidence, created, err := service.SaveTikuAnswerEvidenceWithDB(db, service.TikuAnswerEvidenceInput{
		Question: "合成综合题", Type: 4, Options: []string{}, Answer: json.RawMessage(`"平台展示的答案"`), Source: "chaoxing-reviewed", Evidence: json.RawMessage(`{"label":"rightAnswerContent","text":"正确答案：平台展示的答案"}`),
	})
	if err != nil || !created {
		t.Fatalf("seed platform evidence: %t %v", created, err)
	}
	for _, answer := range []string{"模型答案甲", "模型答案乙"} {
		result := CollectAnswer(model.SearchResponse{Question: "合成综合题", Type: 4, Options: []string{}, Answer: model.Answer{BestAnswer: []string{answer}}}, "", "", "ai")
		if !result.Stored {
			t.Fatal("AI answer was not collected in legacy table")
		}
	}
	var stored entity.TikuAnswerEvidence
	if err := db.First(&stored, evidence.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.EvidenceKey != evidence.EvidenceKey || stored.Answer != evidence.Answer || stored.Source != evidence.Source || stored.Status != "unverified" || stored.Evidence != evidence.Evidence {
		t.Fatalf("AI collection changed platform evidence: %+v", stored)
	}
	var count int64
	if err := db.Model(&entity.TikuAnswerEvidence{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("platform evidence count=%d err=%v", count, err)
	}
}
