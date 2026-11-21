package controller

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestTicketAccountActionOnlyTargetsOwnerAndRequiresAdmin(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	withTicketControllerDB(t, db)
	admin := entity.User{Username: "action-admin", Role: "admin", Status: "enabled"}
	target := entity.User{Username: "action-owner", Status: "banned"}
	other := entity.User{Username: "unrelated", Status: "banned"}
	for _, user := range []*entity.User{&admin, &target, &other} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	ticket := entity.Ticket{UserID: target.ID, Category: "account", Subject: "申诉"}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	request := func(actor entity.User, body string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		c.Set("user", &actor)
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(ticket.ID, 10)}}
		c.Request = httptest.NewRequest(http.MethodPost, "/api/tickets/1/account-action", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		TicketAccountAction(c)
		return out
	}
	if out := request(target, `{"action":"unban","reason":"review"}`); out.Code != 403 {
		t.Fatalf("nonadmin %d %s", out.Code, out.Body.String())
	}
	if out := request(admin, `{"action":"unban","userId":99999,"reason":"申诉核验通过"}`); out.Code != 200 {
		t.Fatalf("admin action %d %s", out.Code, out.Body.String())
	}
	var after entity.User
	db.First(&after, target.ID)
	if after.Status != "enabled" {
		t.Fatal("ticket owner must be unbanned")
	}
	after = entity.User{}
	db.First(&after, other.ID)
	if after.Status != "banned" {
		t.Fatal("unrelated account changed")
	}
}
