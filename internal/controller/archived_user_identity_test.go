package controller

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestArchivedIdentityRequiresExplicitAdministratorRead(t *testing.T) {
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 990, "identity-admin")
	seedArchivedUser(t, db, 991, "identity-archived", entity.UserArchivedReasonAdminDelete, time.Now())
	for i, item := range []struct{ kind, value, state string }{
		{entity.BindingKindPhone, "13800001111", entity.BindingStateReleased},
		{entity.BindingKindWechat, "SYNTHETIC_WECHAT_ID", entity.BindingStateReleased},
		{entity.BindingKindPhone, "13900002222", "voided"},
		{"access_token", "NEVER_EXPOSE_THIS_SECRET", entity.BindingStateReleased},
	} {
		binding := entity.UserBindingRecord{UserID: 991, Kind: item.kind, Value: item.value, ValueHash: strings.Repeat(string(rune('a'+i)), 64), State: item.state}
		if err := db.Create(&binding).Error; err != nil {
			t.Fatal(err)
		}
	}
	params := gin.Params{{Key: "id", Value: "991"}}
	masked := doArchiveRequest(t, ArchivedUserDetail, http.MethodGet, "/api/admin/archived-users/991", nil, admin, params)
	if masked.Code != 200 || strings.Contains(masked.Body.String(), "13800001111") {
		t.Fatalf("default detail must stay masked: %s", masked.Body.String())
	}
	revealed := doArchiveRequest(t, ArchivedUserIdentity, http.MethodGet, "/api/admin/archived-users/991/identity", nil, admin, params)
	if revealed.Code != 200 || revealed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("identity read status/cache: %d %v", revealed.Code, revealed.Header())
	}
	body := revealed.Body.String()
	for _, expected := range []string{"13800001111", "SYNTHETIC_WECHAT_ID"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing authorized identity %s", expected)
		}
	}
	for _, forbidden := range []string{"13900002222", "NEVER_EXPOSE_THIS_SECRET", "synthetic-user-pw-hash", "valueHash", "loginSessionId", "password"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("identity response leaked %s", forbidden)
		}
	}
	user := &entity.User{ID: 992, Role: "user"}
	for _, actor := range []*entity.User{nil, user} {
		denied := doArchiveRequest(t, ArchivedUserIdentity, http.MethodGet, "/api/admin/archived-users/991/identity", nil, actor, params)
		if denied.Code != 403 {
			t.Fatalf("non-admin identity read: %d", denied.Code)
		}
	}
	active := doArchiveRequest(t, ArchivedUserIdentity, http.MethodGet, "/api/admin/archived-users/990/identity", nil, admin, gin.Params{{Key: "id", Value: "990"}})
	if active.Code != 404 {
		t.Fatalf("active identity must not be reachable through archives: %d", active.Code)
	}
}
