package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestBannedUserCanLogInToSubmitAppeal(t *testing.T) {
	db := newUserControllerTestDB(t)
	user := entity.User{Username: "banned-login", Password: "123456", Status: "banned"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	previous := consumeCaptchaTokenWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB = previous })
	consumeCaptchaTokenWithDB = func(string, string, string) error { return nil }
	out := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", bytes.NewBufferString(`{"username":"banned-login","password":"123456","captchaToken":"synthetic"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	UserLogin(c)
	if out.Code != 200 {
		t.Fatalf("banned password login should issue restricted session: %d %s", out.Code, out.Body.String())
	}
}

func TestBannedSessionAllowsOnlySelfAppealAndReadsCurrentBan(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	user := entity.User{Username: "appeal-only", Password: "password", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.UserBan{UserID: user.ID, Reason: "测试封禁", BannedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	token := util.GenerateJwtForUser(user.ID, user.Password)
	router := gin.New()
	for _, path := range []string{"/api/user/session", "/api/user/info", "/api/tickets", "/api/tickets/12", "/api/user", "/api/admin/user-bans", "/api/tickets/config", "/api/tickets/12/refund/review"} {
		router.GET(path, Auth, func(c *gin.Context) { v, _ := c.Get("user"); c.JSON(200, gin.H{"status": v.(*entity.User).Status}) })
	}
	for _, tc := range []struct {
		path string
		code int
	}{{"/api/user/session", 200}, {"/api/user/info", 200}, {"/api/tickets?scope=self", 200}, {"/api/tickets/12", 200}, {"/api/tickets?scope=all", 403}, {"/api/user", 403}, {"/api/admin/user-bans", 403}, {"/api/tickets/config", 403}, {"/api/tickets/12/refund/review", 403}} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", token)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != tc.code {
			t.Errorf("%s: got %d %s want %d", tc.path, out.Code, out.Body.String(), tc.code)
		}
	}
}

func TestBanAppealSessionDoesNotMakeBannedAccountSpendable(t *testing.T) {
	banned := &entity.User{Status: "banned"}
	if !isUserAppealSessionAllowed(banned) || isUserEnabled(banned) || isUserSpendable(banned) {
		t.Fatal("banned account needs only a restricted session")
	}
	if isUserAppealSessionAllowed(&entity.User{Status: "disabled"}) {
		t.Fatal("disabled must remain denied")
	}
}

func TestBannedAppealAttachmentDeleteUsesActualRouteAndRetainsRoleClamp(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	user := entity.User{Username: "banned-admin-attachment", Password: "password", Role: "admin", Status: "banned"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := util.GenerateJwtForUser(user.ID, user.Password)
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		router.Handle(method, "/api/ticket-attachments/:id", Auth, func(c *gin.Context) {
			current, _ := c.Get("user")
			if current.(*entity.User).Role != "user" || !c.GetBool("account_banned") {
				t.Error("appeal inherited administrative privileges")
			}
			c.Status(http.StatusNoContent)
		})
	}
	router.GET("/api/ticket-attachments/:id/admin", Auth, func(c *gin.Context) {
		t.Error("banned administrator reached private attachment handler")
		c.Status(http.StatusNoContent)
	})
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/api/ticket-attachments/12", 204}, {"DELETE", "/api/ticket-attachments/12", 204}, {"GET", "/api/ticket-attachments/12/admin", 403}} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set("Authorization", token)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, request)
		if out.Code != tc.code {
			t.Errorf("%s %s = %d %s", tc.method, tc.path, out.Code, out.Body.String())
		}
	}
}

func TestBannedAdministratorCanCreateActualAppealButCannotReadForeignTicket(t *testing.T) {
	db := newUserControllerTestDB(t)
	for _, model := range []any{&entity.UserBan{}, &entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}, &entity.SystemSetting{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	withTicketControllerDB(t, db)
	actor := entity.User{Username: "banned-admin-actual-appeal", Password: "password", Role: "admin", Status: "banned"}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	foreign := entity.Ticket{UserID: actor.ID + 1, Subject: "FOREIGN_PRIVATE_TICKET", Status: "open", Priority: "normal", LastMessageAt: time.Now()}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	token := util.GenerateJwtForUser(actor.ID, actor.Password)
	router := gin.New()
	router.POST("/api/tickets", Auth, CreateTicket)
	router.GET("/api/tickets/:id", Auth, TicketDetail)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	appeal := request("POST", "/api/tickets", `{"ticketType":"account","subject":"账号申诉","body":"请核对本次封禁"}`)
	if appeal.Code != http.StatusOK {
		t.Fatalf("actual appeal failed: %d %s", appeal.Code, appeal.Body.String())
	}
	var count int64
	if err := db.Model(&entity.Ticket{}).Where("user_id = ? AND category = ? AND appeal_target_user_id = ?", actor.ID, entity.TicketCategoryBlacklistAppeal, actor.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("appeal not committed: %d %v", count, err)
	}
	denied := request("GET", fmt.Sprintf("/api/tickets/%d", foreign.ID), "")
	if denied.Code != http.StatusNotFound || strings.Contains(denied.Body.String(), foreign.Subject) {
		t.Fatalf("banned administrator exposed foreign ticket: %d %s", denied.Code, denied.Body.String())
	}
	for _, kind := range []string{"refund", "general"} {
		response := request("POST", "/api/tickets", fmt.Sprintf(`{"ticketType":%q,"subject":"非申诉","body":"禁止"}`, kind))
		if response.Code != http.StatusForbidden {
			t.Fatalf("nonappeal %s allowed: %d %s", kind, response.Code, response.Body.String())
		}
	}
}

func TestBanRecordCannotPromoteDisabledOrUnknownAccountIntoAppealSession(t *testing.T) {
	for _, status := range []string{"disabled", "  DISABLED  ", "unknown_state", "closure_pending"} {
		t.Run(status, func(t *testing.T) {
			db := newUserControllerTestDB(t)
			if err := db.AutoMigrate(&entity.UserBan{}); err != nil {
				t.Fatal(err)
			}
			user := entity.User{Username: "ban-status-priority", Password: "password", Status: status}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entity.UserBan{UserID: user.ID, Reason: "生效封禁", BannedAt: time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
			if err := refreshSessionBan(&user); err != nil {
				t.Fatal(err)
			}
			if user.Status != status {
				t.Fatalf("ban must preserve original %q status; got %q", status, user.Status)
			}
			token := util.GenerateJwtForUser(user.ID, user.Password)
			router := gin.New()
			router.GET("/api/user/session", Auth, func(c *gin.Context) {
				current, _ := c.Get("user")
				if current.(*entity.User).Status != status {
					t.Error("original account status lost")
				}
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/user/session", nil)
			req.Header.Set("Authorization", token)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			expected := http.StatusUnauthorized
			if status == "closure_pending" {
				expected = http.StatusNoContent
			}
			if out.Code != expected {
				t.Fatalf("%q session=%d %s want %d", status, out.Code, out.Body.String(), expected)
			}
		})
	}
}
