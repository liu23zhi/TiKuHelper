package controller

import (
	"encoding/json"
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

func TestTicketAppealCreateRequiresVerifiedTarget(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}, &entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	withTicketControllerDB(t, db)
	actor, token := seedAuthSessionUser(t, db, "appeal-owner")
	if err := db.Model(&entity.User{}).Where("id = ?", actor.ID).Update("status", "banned").Error; err != nil {
		t.Fatal(err)
	}
	_, enabledToken := seedAuthSessionUser(t, db, "appeal-other")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/tickets", Auth, CreateTicket)
	for _, tc := range []struct {
		name, token, issue string
		status             int
	}{
		{"claimed target", enabledToken, `"accountIssue":"ban_appeal","appealTargetUserId":1,`, http.StatusBadRequest},
		{"invented proof", enabledToken, `"accountIssue":"ban_appeal","appealProof":"invalid",`, http.StatusBadRequest},
		{"banned account submits unrelated issue", token, `"accountIssue":"binding",`, http.StatusForbidden},
		{"banned account appeals itself", token, `"accountIssue":"ban_appeal",`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"ticketType":"account",` + tc.issue + `"subject":"申诉","body":"请核实封禁情况"}`
			out := serveAppealRequest(router, http.MethodPost, "/api/tickets", tc.token, body)
			if out.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", out.Code, tc.status, out.Body.String())
			}
		})
	}
}

func TestTicketAppealVerificationUsesExistingSessionAndVerifiedTarget(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}, &entity.UserBan{}, &entity.TicketAppealProof{}, &entity.LoginAttempt{}); err != nil {
		t.Fatal(err)
	}
	withTicketControllerDB(t, db)
	actor, actorToken := seedAuthSessionUser(t, db, "appeal-submitter")
	admin, adminToken := seedAuthSessionUser(t, db, "appeal-operator")
	target, targetToken := seedAuthSessionUser(t, db, "appeal-target")
	other, otherToken := seedAuthSessionUser(t, db, "appeal-unrelated")
	if err := db.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	const phone, wechat, reason = "13987656789", "private-wechat-openid", "测试封禁原因"
	const registerIP, lastLoginIP = "192.0.2.31", "192.0.2.32"
	bindingAt := time.Now().Add(-time.Hour).UTC()
	if err := db.Model(&target).Updates(map[string]any{
		"status": "banned", "phone": phone, "wechat_id": wechat, "phone_bound_at": bindingAt, "wechat_bound_at": bindingAt,
		"register_ip": registerIP, "last_login_ip": lastLoginIP, "last_login_at": bindingAt,
		"register_source": "public", "balance": 12.25, "gift_balance": 1.5, "invited_by_user_id": other.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	until := now.Add(time.Hour)
	if err := db.Create(&entity.UserBan{UserID: target.ID, Reason: reason, BannedAt: now, ExpiresAt: &until}).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST(ticketAppealVerificationPath, Auth, VerifyTicketAppealAccount)
	router.POST("/api/tickets", Auth, CreateTicket)
	router.GET("/api/tickets/:id", Auth, TicketDetail)
	router.POST("/api/tickets/:id/account-action", Auth, TicketAccountAction)
	router.GET("/api/user/info", Auth, UserInfo)
	router.GET("/api/user/session", Auth, UserSession)
	request := func(method, path, token, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		out := serveAppealRequest(router, method, path, token, body)
		if out.Code != status {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, out.Code, status, out.Body.String())
		}
		return out
	}
	verifyBody := fmt.Sprintf(`{"username":%q,"password":%q}`, target.Username, target.Password)
	request(http.MethodPost, ticketAppealVerificationPath, "", verifyBody, http.StatusUnauthorized)
	request(http.MethodPost, ticketAppealVerificationPath, targetToken, verifyBody, http.StatusForbidden)
	firstFailure := request(http.MethodPost, ticketAppealVerificationPath, actorToken, `{"username":"absent","password":"wrong"}`, http.StatusBadRequest)
	wrong := request(http.MethodPost, ticketAppealVerificationPath, actorToken, fmt.Sprintf(`{"username":%q,"password":"wrong"}`, target.Username), http.StatusBadRequest)
	if firstFailure.Body.String() != wrong.Body.String() {
		t.Fatal("credential errors expose account existence")
	}
	verify := func(token string) string {
		t.Helper()
		out := request(http.MethodPost, ticketAppealVerificationPath, token, verifyBody, http.StatusOK)
		var response struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != 4 || len(response.Data["proof"]) != 64 || response.Data["maskedPhone"] != "****6789" ||
			response.Data["registeredAt"] == "" || response.Data["expiresAt"] == "" || out.Header().Get("Set-Cookie") != "" {
			t.Fatalf("unexpected verification shape: %s", out.Body.String())
		}
		return response.Data["proof"]
	}
	create := func(token, proof string, status int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"ticketType":"account","accountIssue":"ban_appeal","appealProof":%q,"appealTargetUserId":%d,"subject":"封禁申诉","body":"请核实封禁情况"}`, proof, other.ID)
		return request(http.MethodPost, "/api/tickets", token, body, status)
	}

	proof := verify(actorToken)
	for _, identity := range []entity.User{actor, target} {
		var stored entity.User
		if err := db.First(&stored, identity.ID).Error; err != nil {
			t.Fatal(err)
		}
		lastLoginUnchanged := stored.LastLoginAt == nil
		if identity.ID == target.ID {
			lastLoginUnchanged = stored.LastLoginAt != nil && stored.LastLoginAt.Equal(bindingAt) && stored.LastLoginIP == lastLoginIP
		}
		if stored.LoginSessionID != identity.LoginSessionID || stored.Password != identity.Password || !lastLoginUnchanged {
			t.Fatalf("verification changed login state for %d", identity.ID)
		}
	}
	for _, endpoint := range []string{"/api/user/session", "/api/user/info"} {
		out := request(http.MethodGet, endpoint, targetToken, "", http.StatusOK)
		if !strings.Contains(out.Body.String(), reason) || !strings.Contains(out.Body.String(), "banExpiresAt") || strings.Contains(out.Body.String(), "bannedBy") {
			t.Fatalf("incorrect self ban notice: %s", out.Body.String())
		}
	}
	create(otherToken, proof, http.StatusBadRequest)
	actor.LoginSessionID = "actor-new-session"
	if err := db.Model(&actor).Update("login_session_id", actor.LoginSessionID).Error; err != nil {
		t.Fatal(err)
	}
	newActorToken := util.GenerateJwtForUserSession(actor.ID, actor.Password, actor.LoginSessionID)
	create(actorToken, proof, http.StatusUnauthorized)
	create(newActorToken, proof, http.StatusBadRequest)
	proof = verify(newActorToken)

	created := create(newActorToken, proof, http.StatusOK)
	var createdResponse struct {
		Data struct {
			Ticket entity.Ticket `json:"ticket"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdResponse); err != nil {
		t.Fatal(err)
	}
	ticket := createdResponse.Data.Ticket
	if ticket.ID <= 0 || ticket.UserID != actor.ID || ticket.Category != "blacklist_appeal" {
		t.Fatalf("incorrect appeal ticket: %s", created.Body.String())
	}
	create(newActorToken, proof, http.StatusBadRequest)
	path := fmt.Sprintf("/api/tickets/%d", ticket.ID)
	for _, out := range []*httptest.ResponseRecorder{created, request(http.MethodGet, path, newActorToken, "", http.StatusOK)} {
		for _, secret := range []string{"appealTarget", target.Username, phone, wechat, reason, proof, registerIP, lastLoginIP} {
			if strings.Contains(out.Body.String(), secret) {
				t.Fatalf("self view exposed target details: %s", out.Body.String())
			}
		}
	}
	request(http.MethodGet, path, targetToken, "", http.StatusNotFound)
	request(http.MethodGet, path+"?scope=self", adminToken, "", http.StatusNotFound)
	detail := request(http.MethodGet, path, adminToken, "", http.StatusOK)
	for _, field := range []string{"appealTarget", `"banned":true`, "banExpiresAt", target.Username, phone, wechat, reason} {
		if !strings.Contains(detail.Body.String(), field) {
			t.Fatalf("management detail did not include %q: %s", field, detail.Body.String())
		}
	}
	for _, credential := range []string{target.Password, target.LoginSessionID, proof} {
		if strings.Contains(detail.Body.String(), credential) {
			t.Fatalf("management detail exposed credentials: %s", detail.Body.String())
		}
	}
	var managementDetail struct {
		Data struct {
			AppealTarget map[string]any `json:"appealTarget"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &managementDetail); err != nil {
		t.Fatal(err)
	}
	account := managementDetail.Data.AppealTarget
	for key, want := range map[string]any{
		"registerIp": registerIP, "lastLoginIp": lastLoginIP, "registerSource": "public",
		"balance": 12.25, "giftBalance": 1.5, "invitedByUserId": float64(other.ID),
	} {
		if account[key] != want {
			t.Fatalf("management target %s = %v, want %v", key, account[key], want)
		}
	}
	for _, key := range []string{"createdAt", "lastLoginAt", "phoneBoundAt", "wechatBoundAt"} {
		if value, _ := account[key].(string); value == "" {
			t.Fatalf("management target has no %s", key)
		}
	}
	adminProof := verify(adminToken)
	adminCreated := create(adminToken, adminProof, http.StatusOK)
	if strings.Contains(adminCreated.Body.String(), "appealTarget") {
		t.Fatalf("admin create response exposed management detail: %s", adminCreated.Body.String())
	}
	request(http.MethodPost, path+"/account-action", newActorToken, `{"action":"unban","reason":"核验工单"}`, http.StatusForbidden)
	request(http.MethodPost, path+"/account-action", adminToken, fmt.Sprintf(`{"action":"unban","reason":"核验工单","userId":%d}`, other.ID), http.StatusOK)
	request(http.MethodPost, path+"/account-action", adminToken, `{"action":"phone","reason":"更新绑定","value":"13876543210"}`, http.StatusOK)
	var storedTarget, storedActor, storedOther entity.User
	for id, result := range map[int32]*entity.User{target.ID: &storedTarget, actor.ID: &storedActor, other.ID: &storedOther} {
		if err := db.First(result, id).Error; err != nil {
			t.Fatal(err)
		}
	}
	if storedTarget.Status != "enabled" || storedTarget.Phone == nil || *storedTarget.Phone != "13876543210" ||
		storedActor.Phone != nil || storedOther.Phone != nil {
		t.Fatal("management action did not exclusively affect verified target")
	}
	updated := request(http.MethodGet, path, adminToken, "", http.StatusOK)
	if strings.Contains(updated.Body.String(), reason) || strings.Contains(updated.Body.String(), `"banned":true`) ||
		!strings.Contains(updated.Body.String(), "13876543210") {
		t.Fatalf("management detail retained obsolete target state: %s", updated.Body.String())
	}
}

func TestTicketAppealVerificationLimitsAcrossActorsByNormalizedTarget(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.UserBan{}, &entity.TicketAppealProof{}, &entity.LoginAttempt{}); err != nil {
		t.Fatal(err)
	}
	withTicketControllerDB(t, db)
	if err := db.Create(&[]entity.SystemSetting{
		{Key: "login_max_attempts", Value: "3"},
		{Key: "login_rate_limit_enabled", Value: "false"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	target, _ := seedAuthSessionUser(t, db, "appeal-limited-target")
	if err := db.Model(&target).Update("status", "banned").Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST(ticketAppealVerificationPath, Auth, VerifyTicketAppealAccount)
	for i, tc := range []struct {
		username, password string
		status             int
	}{
		{"  APPEAL-limited-target  ", "wrong", http.StatusBadRequest},
		{target.Username, "wrong", http.StatusBadRequest},
		{target.Username, target.Password, http.StatusTooManyRequests},
	} {
		_, token := seedAuthSessionUser(t, db, fmt.Sprintf("appeal-rate-actor-%d", i))
		request := httptest.NewRequest(http.MethodPost, ticketAppealVerificationPath, strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, tc.username, tc.password)))
		request.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", 20+i)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, request)
		if out.Code != tc.status {
			t.Fatalf("attempt %d status=%d want=%d body=%s", i+1, out.Code, tc.status, out.Body.String())
		}
	}
	var attempt entity.LoginAttempt
	if err := db.Where("scope = ?", "appeal_target").First(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempt.SubjectKey) != 64 || strings.Contains(attempt.SubjectKey, target.Username) || attempt.LockedUntil == nil {
		t.Fatal("target limit did not persist a hashed key and lockout")
	}
}

func TestTicketAppealProofRetiresWhenTargetPasswordChanges(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}, &entity.UserBan{}, &entity.TicketAppealProof{}, &entity.LoginAttempt{}); err != nil {
		t.Fatal(err)
	}
	withTicketControllerDB(t, db)
	actor, actorToken := seedAuthSessionUser(t, db, "appeal-password-actor")
	target, _ := seedAuthSessionUser(t, db, "appeal-password-target")
	if err := db.Model(&target).Update("status", "banned").Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST(ticketAppealVerificationPath, Auth, VerifyTicketAppealAccount)
	router.POST("/api/tickets", Auth, CreateTicket)
	verify := func(password string) string {
		t.Helper()
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, target.Username, password)
		out := serveAppealRequest(router, http.MethodPost, ticketAppealVerificationPath, actorToken, body)
		var response struct {
			Data struct {
				Proof string `json:"proof"`
			} `json:"data"`
		}
		if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &response) != nil || response.Data.Proof == "" {
			t.Fatalf("verification failed: %d %s", out.Code, out.Body.String())
		}
		return response.Data.Proof
	}
	create := func(proof string, want int) {
		t.Helper()
		body := fmt.Sprintf(`{"ticketType":"account","accountIssue":"ban_appeal","appealProof":%q,"subject":"账号申诉","body":"请核实封禁情况"}`, proof)
		out := serveAppealRequest(router, http.MethodPost, "/api/tickets", actorToken, body)
		if out.Code != want {
			t.Fatalf("create status=%d want=%d body=%s", out.Code, want, out.Body.String())
		}
	}
	proof := verify(target.Password)
	const newPassword = "replacement-password"
	if err := db.Model(&target).Update("password", newPassword).Error; err != nil {
		t.Fatal(err)
	}
	create(proof, http.StatusBadRequest)
	create(verify(newPassword), http.StatusOK)
	for _, user := range []entity.User{actor, target} {
		var stored entity.User
		if err := db.First(&stored, user.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.LoginSessionID != user.LoginSessionID {
			t.Fatalf("appeal changed login session for user %d", user.ID)
		}
	}
}

func serveAppealRequest(router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	out := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	router.ServeHTTP(out, req)
	return out
}
