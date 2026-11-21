package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestTicketListSelfScopeRestrictsAdminWithoutChangingPermissions(t *testing.T) {
	db := newTicketTestDB(t)
	admin := &entity.User{ID: 1, Username: "admin", Role: "admin"}
	user := &entity.User{ID: 2, Username: "owner", Role: "user"}
	for _, actor := range []*entity.User{admin, user} {
		if err := db.Create(actor).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []entity.Ticket{
		{UserID: admin.ID, Subject: "own feedback", Status: "open", Priority: "normal", Origin: "user", LastMessageAt: time.Now()},
		{UserID: admin.ID, Subject: "own system", Status: "open", Priority: "normal", Origin: "system", LastMessageAt: time.Now()},
		{UserID: user.ID, Subject: "other feedback", Status: "open", Priority: "normal", Origin: "user", LastMessageAt: time.Now()},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		actor *entity.User
		query TicketListQuery
		want  int64
	}{
		{"admin-management", admin, TicketListQuery{}, 3},
		{"admin-self", admin, TicketListQuery{SelfOnly: true}, 2},
		{"user-default", user, TicketListQuery{}, 1},
		{"user-self", user, TicketListQuery{SelfOnly: true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ListTicketsWithDB(db, tc.actor, tc.query, 1, 100)
			if err != nil || result.Total != tc.want || len(result.Items) != int(tc.want) {
				t.Fatalf("list: %+v, %v", result, err)
			}
			if tc.query.SelfOnly || tc.actor == user {
				for _, row := range result.Items {
					if row.UserID != tc.actor.ID {
						t.Fatalf("other user's ticket exposed: %+v", row)
					}
				}
			}
		})
	}
	if _, err := CreateTicketForUserWithDB(db, admin, CreateTicketInput{Subject: "admin feedback", Body: "own report"}, nil); err != nil {
		t.Fatalf("self list scope must not prevent administrator feedback creation: %v", err)
	}
}
