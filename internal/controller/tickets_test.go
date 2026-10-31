package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func newTicketControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func withTicketControllerDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := ticketControllerDB
	ticketControllerDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketControllerDB = previous })
}

func TestTicketListRejectsInvalidFilterTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 1, Role: "user"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/tickets?createdTo=invalid", nil)
	TicketList(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTicketControllerOwnListAndAdminConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketControllerTestDB(t)
	withTicketControllerDB(t, db)
	for _, ticket := range []entity.Ticket{
		{UserID: 1, UserName: "one", Subject: "my ticket", Status: "open", Priority: "normal", LastMessageAt: time.Now()},
		{UserID: 2, UserName: "two", Subject: "other ticket", Status: "open", Priority: "normal", LastMessageAt: time.Now()},
	} {
		if err := db.Create(&ticket).Error; err != nil {
			t.Fatal(err)
		}
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 1, Role: "user"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/tickets", nil)
	TicketList(ctx)
	var list struct {
		Data struct {
			Total int64 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || list.Data.Total != 1 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 1, Role: "user"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/tickets/config", nil)
	TicketWecomSettings(ctx)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 3, Role: "admin"})
	ctx.Request = httptest.NewRequest(http.MethodPut, "/tickets/config", strings.NewReader(`{"enabled":true,"webhookUrl":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=private","mentionedList":["a"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	UpdateTicketWecomSettings(ctx)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "private") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func performTicketControllerRequest(t *testing.T, user *entity.User, method, target, body string, params gin.Params, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", user)
	ctx.Params = params
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	return recorder
}

func TestTicketControllerCreateReplyAndPermissionIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	other := &entity.User{ID: 2, Username: "bob", Role: "user"}
	admin := &entity.User{ID: 3, Username: "admin", Role: "admin"}
	created := performTicketControllerRequest(t, user, http.MethodPost, "/tickets", `{"subject":"中文问题","body":"无法登录"}`, nil, CreateTicket)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var detail struct {
		Data struct {
			Ticket       entity.Ticket          `json:"ticket"`
			Messages     []entity.TicketMessage `json:"messages"`
			Notification interface{}            `json:"notification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Ticket.ID <= 0 || len(detail.Data.Messages) != 1 || detail.Data.Messages[0].Body != "无法登录" {
		t.Fatalf("invalid created detail: %+v", detail)
	}
	id := detail.Data.Ticket.ID
	params := gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	denied := performTicketControllerRequest(t, other, http.MethodGet, "/tickets/id", "", params, TicketDetail)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("other detail status=%d body=%s", denied.Code, denied.Body.String())
	}
	denied = performTicketControllerRequest(t, other, http.MethodPost, "/tickets/id/messages", `{"body":"不应允许"}`, params, AddTicketMessage)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("other reply status=%d body=%s", denied.Code, denied.Body.String())
	}
	denied = performTicketControllerRequest(t, user, http.MethodPut, "/tickets/id", `{"status":"closed"}`, params, UpdateTicket)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("user update status=%d body=%s", denied.Code, denied.Body.String())
	}
	reply := performTicketControllerRequest(t, admin, http.MethodPost, "/tickets/id/messages", `{"body":"请重试","status":"resolved"}`, params, AddTicketMessage)
	if reply.Code != http.StatusOK {
		t.Fatalf("admin reply status=%d body=%s", reply.Code, reply.Body.String())
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Ticket.Status != "resolved" || len(detail.Data.Messages) != 2 {
		t.Fatalf("unexpected admin reply: %+v", detail.Data)
	}
	reply = performTicketControllerRequest(t, user, http.MethodPost, "/tickets/id/messages", `{"body":"仍有问题"}`, params, AddTicketMessage)
	if reply.Code != http.StatusOK {
		t.Fatalf("user reply status=%d body=%s", reply.Code, reply.Body.String())
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Ticket.Status != "open" || len(detail.Data.Messages) != 3 {
		t.Fatalf("unexpected user reply: %+v", detail.Data)
	}
	if err := db.Create(&entity.TicketNotification{TicketID: id, EventType: "new_ticket", Status: "failed", LastError: "admin-only failure", Payload: "hidden"}).Error; err != nil {
		t.Fatal(err)
	}
	read := performTicketControllerRequest(t, user, http.MethodGet, "/tickets/id", "", params, TicketDetail)
	if strings.Contains(read.Body.String(), "notification") || strings.Contains(read.Body.String(), "admin-only failure") {
		t.Fatalf("notification leaked: %s", read.Body.String())
	}
}

func TestTicketControllerInvalidInputsReturn400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	for _, body := range []string{`{`, `{"subject":"","body":"内容"}`, `{"subject":"标题","body":""}`, `{"subject":"` + strings.Repeat("中", 121) + `","body":"内容"}`} {
		response := performTicketControllerRequest(t, user, http.MethodPost, "/tickets", body, nil, CreateTicket)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestTicketControllerWebhookTestUsesMockAndRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketControllerTestDB(t)
	withTicketControllerDB(t, db)
	previous := sendTicketControllerWebhook
	t.Cleanup(func() { sendTicketControllerWebhook = previous })
	called := 0
	sendTicketControllerWebhook = func(_ context.Context, config service.TicketWecomConfig, content string, mentioned []string) error {
		called++
		if config.WebhookURL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test" || !strings.Contains(content, "测试通知") {
			t.Fatalf("unexpected mock arguments: %+v %s", config, content)
		}
		return nil
	}
	for _, setting := range []entity.SystemSetting{{Key: service.TicketWecomEnabledSettingKey, Value: "true"}, {Key: service.TicketWecomWebhookURLSettingKey, Value: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"}} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	response := performTicketControllerRequest(t, &entity.User{ID: 1, Role: "user"}, http.MethodPost, "/tickets/config/test", "", nil, TestTicketWecomWebhook)
	if response.Code != http.StatusForbidden || called != 0 {
		t.Fatalf("user sent test: status=%d calls=%d", response.Code, called)
	}
	response = performTicketControllerRequest(t, &entity.User{ID: 2, Role: "admin"}, http.MethodPost, "/tickets/config/test", "", nil, TestTicketWecomWebhook)
	if response.Code != http.StatusOK || called != 1 {
		t.Fatalf("test status=%d body=%s calls=%d", response.Code, response.Body.String(), called)
	}
}

func TestTicketControllerRetryRequiresMatchingFailedNotification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketControllerTestDB(t)
	withTicketControllerDB(t, db)
	notice := entity.TicketNotification{TicketID: 1, EventType: "new_ticket", Payload: "test", Status: "sent"}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	params := gin.Params{{Key: "id", Value: "1"}, {Key: "notificationId", Value: strconv.FormatInt(notice.ID, 10)}}
	admin := &entity.User{ID: 3, Role: "admin"}
	response := performTicketControllerRequest(t, admin, http.MethodPost, "/tickets/1/notifications/1/retry", "", params, RetryTicketNotification)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	params[0].Value = "2"
	response = performTicketControllerRequest(t, admin, http.MethodPost, "/tickets/2/notifications/1/retry", "", params, RetryTicketNotification)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTicketControllersWithoutAuthenticationReturn401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, handler := range map[string]gin.HandlerFunc{
		"list": TicketList, "create": CreateTicket, "detail": TicketDetail,
		"reply": AddTicketMessage, "update": UpdateTicket,
		"configuration": TicketWecomSettings, "update configuration": UpdateTicketWecomSettings,
		"test configuration": TestTicketWecomWebhook, "retry": RetryTicketNotification,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/tickets", nil)
			handler(ctx)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestParseTicketTimeUsesExclusiveDateEnd(t *testing.T) {
	value, err := parseTicketTime("2026-10-31", true)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)
	if !value.Equal(want) {
		t.Fatalf("got %v, want %v", value, want)
	}
}

func TestParseTicketTimeRejectsInvalidValue(t *testing.T) {
	if _, err := parseTicketTime("not-a-time", false); err == nil {
		t.Fatal("expected invalid time error")
	}
}
