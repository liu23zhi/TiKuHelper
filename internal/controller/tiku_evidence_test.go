package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestUploadTikuAnswerEvidenceEndpointUsesAuthenticatedUserAndIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 876, "evidence-upload-user", "user")
	token := seedControllerTokenForUser(t, db, user, "evidence-upload-token", 0)
	originalDBProvider := tikuAnswerEvidenceDB
	tikuAnswerEvidenceDB = func() *gorm.DB { return db }
	t.Cleanup(func() { tikuAnswerEvidenceDB = originalDBProvider })
	router := gin.New()
	router.POST("/adapter-service/tiku/evidence", AuthOrAccessToken, UploadTikuAnswerEvidence)
	anonymous := httptest.NewRecorder()
	router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/adapter-service/tiku/evidence", bytes.NewBufferString(`{"question":"unauthorized"}`)))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous evidence upload must be rejected, got %d", anonymous.Code)
	}
	payload, _ := json.Marshal(service.TikuAnswerEvidenceInput{QID: "q-1", Question: "2+2=?", Type: -1, Options: []string{"4"}, Answer: json.RawMessage(`"4"`), Source: "chaoxing-reviewed", Evidence: []byte(`{"page":"review"}`)})
	post := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/adapter-service/tiku/evidence", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+token.TokenValue)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	epoch := searchResultCache.currentEpoch()
	first := post()
	createdEpoch := searchResultCache.currentEpoch()
	second := post()
	if createdEpoch == epoch || searchResultCache.currentEpoch() != createdEpoch {
		t.Fatal("only new evidence must invalidate answer caches")
	}
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("responses=%d/%d %s %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var firstBody, secondBody map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if firstBody["created"] != true || secondBody["created"] != false || firstBody["status"] != "unverified" {
		t.Fatalf("unexpected idempotency response: %+v %+v", firstBody, secondBody)
	}
	var rows []entity.TikuAnswerEvidence
	if err := db.Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("expected one evidence row, rows=%+v err=%v", rows, err)
	}
}
