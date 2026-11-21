package controller

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestTicketControllerSelfScopeIsExplicitAndCannotWidenUserVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketControllerTestDB(t)
	withTicketControllerDB(t, db)
	for _, row := range []entity.Ticket{
		{UserID: 1, Subject: "administrator's feedback", Status: "open", Priority: "normal", Origin: "user", LastMessageAt: time.Now()},
		{UserID: 2, Subject: "user feedback", Status: "open", Priority: "normal", Origin: "user", LastMessageAt: time.Now()},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, role, query string
		want              int64
	}{
		{"admin-management", "admin", "", 2},
		{"admin-self", "admin", "?scope=self", 1},
		{"user-default", "user", "", 1},
		{"user-self", "user", "?scope=self", 1},
		{"user-cannot-widen", "user", "?scope=all", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := &entity.User{ID: 1, Role: tc.role, Status: "enabled"}
			recorder := performTicketControllerRequest(t, actor, http.MethodGet, "/tickets"+tc.query, "", nil, TicketList)
			var body struct {
				Data service.TicketListResult `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || body.Data.Total != tc.want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if tc.want == 1 && (len(body.Data.Items) != 1 || body.Data.Items[0].UserID != actor.ID) {
				t.Fatalf("self scope leaked another owner: %+v", body.Data)
			}
		})
	}
}
