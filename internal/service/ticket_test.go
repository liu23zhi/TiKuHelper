package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newTicketTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestTicketCreateAndPermissions(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Nickname: "Alice", Role: "user"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "登录问题", Body: "无法登录"})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Ticket.Status != entity.TicketStatusOpen || len(detail.Messages) != 1 || detail.Messages[0].AuthorName != "Alice" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	other := &entity.User{ID: 3, Username: "bob", Role: "user"}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := GetTicketWithDB(db, other, detail.Ticket.ID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("expected not found for other user, got %v", err)
	}
	adminDetail, err := GetTicketWithDB(db, admin, detail.Ticket.ID)
	if err != nil || len(adminDetail.Messages) != 1 {
		t.Fatalf("admin detail: %+v %v", adminDetail, err)
	}
}

func TestTicketReplyStatusAndNotificationVisibility(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: TicketWecomEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: TicketWecomWebhookURLSettingKey, Value: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"}).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "首条"})
	if err != nil {
		t.Fatal(err)
	}
	var notice entity.TicketNotification
	if err := db.Where("ticket_id = ?", detail.Ticket.ID).First(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if notice.Status != ticketNotificationPending {
		t.Fatalf("expected pending, got %s", notice.Status)
	}
	if detail, err = GetTicketWithDB(db, user, detail.Ticket.ID); err != nil || detail.Notification != nil {
		t.Fatalf("user notification leaked: %+v %v", detail.Notification, err)
	}
	if _, err := AddTicketMessageWithDB(db, admin, detail.Ticket.ID, ReplyTicketInput{Body: "处理中"}); err != nil {
		t.Fatal(err)
	}
	adminDetail, err := GetTicketWithDB(db, admin, detail.Ticket.ID)
	if err != nil || adminDetail.Ticket.Status != entity.TicketStatusPending || adminDetail.Notification == nil || adminDetail.Notification.ID != notice.ID {
		t.Fatalf("admin reply detail: %+v %v", adminDetail, err)
	}
	closed := entity.TicketStatusClosed
	if _, err := AddTicketMessageWithDB(db, admin, detail.Ticket.ID, ReplyTicketInput{Body: "已解决", Status: &closed}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTicketMessageWithDB(db, user, detail.Ticket.ID, ReplyTicketInput{Body: "补充信息"}); err != nil {
		t.Fatal(err)
	}
	adminDetail, err = GetTicketWithDB(db, admin, detail.Ticket.ID)
	if err != nil || adminDetail.Ticket.Status != entity.TicketStatusOpen || adminDetail.Ticket.ClosedAt != nil {
		t.Fatalf("user reply should reopen: %+v %v", adminDetail.Ticket, err)
	}
}

func TestTicketListKeywordAndExclusiveEnd(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	ticket := entity.Ticket{UserID: user.ID, UserName: "alice", Subject: "网络问题", Status: entity.TicketStatusOpen, Priority: entity.TicketPriorityNormal, LastMessageAt: created, CreatedAt: created, UpdatedAt: created}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketMessage{TicketID: ticket.ID, UserID: user.ID, AuthorName: "alice", Body: "连接失败", AuthorRole: "user", CreatedAt: created}).Error; err != nil {
		t.Fatal(err)
	}
	end := created.Add(time.Nanosecond)
	result, err := ListTicketsWithDB(db, user, TicketListQuery{Keyword: "连接", CreatedTo: &end}, 1, 20)
	if err != nil || result.Total != 1 {
		t.Fatalf("keyword result: %+v %v", result, err)
	}
	result, err = ListTicketsWithDB(db, user, TicketListQuery{CreatedTo: &created}, 1, 20)
	if err != nil || result.Total != 0 {
		t.Fatalf("exclusive end result: %+v %v", result, err)
	}
}

func TestTicketListSearchesUsernameWithoutCrossingUserScope(t *testing.T) {
	db := newTicketTestDB(t)
	alice := &entity.User{ID: 1, Username: "alice_login", Nickname: "小明", Role: "user"}
	bob := &entity.User{ID: 2, Username: "bob_login", Nickname: "小红", Role: "user"}
	admin := &entity.User{ID: 3, Username: "admin", Role: "admin"}
	for _, user := range []*entity.User{alice, bob, admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	first, err := CreateTicketWithDB(db, alice, CreateTicketInput{Subject: "第一笔反馈", Body: "登录后页面异常"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTicketWithDB(db, bob, CreateTicketInput{Subject: "第二笔反馈", Body: "账单显示异常"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name    string
		user    *entity.User
		keyword string
		want    int64
	}{
		{"admin username", admin, "alice_login", 1},
		{"owner username", alice, "alice_login", 1},
		{"owner display name", alice, "小明", 1},
		{"other owner username", bob, "alice_login", 0},
		{"owner searches other username", alice, "bob_login", 0},
	} {
		t.Run(item.name, func(t *testing.T) {
			result, err := ListTicketsWithDB(db, item.user, TicketListQuery{Keyword: item.keyword}, 1, 20)
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != item.want || int64(len(result.Items)) != item.want {
				t.Fatalf("keyword=%q result=%+v", item.keyword, result)
			}
			if item.want == 1 && result.Items[0].ID != first.Ticket.ID {
				t.Fatalf("returned wrong owner: %+v", result.Items[0])
			}
		})
	}
}

func TestTicketNotificationWorkerClaimsAndDoesNotRetryFailed(t *testing.T) {
	db := newTicketTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: TicketWecomEnabledSettingKey, Value: "false"}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []entity.TicketNotification{
		{TicketID: 1, Status: ticketNotificationPending, Payload: "pending", NextAttemptAt: &now},
		{TicketID: 2, Status: ticketNotificationFailed, Payload: "failed", AttemptCount: 1, NextAttemptAt: &now},
		{TicketID: 3, Status: ticketNotificationSending, Payload: "crashed", UpdatedAt: now.Add(-3 * time.Minute)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	processed, err := ProcessPendingTicketNotifications(context.Background(), db, now)
	if err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	var pending, failed, skipped entity.TicketNotification
	if err := db.First(&pending, rows[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&failed, rows[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&skipped, rows[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != ticketNotificationPending || failed.Status != ticketNotificationFailed || skipped.Status != ticketNotificationSending {
		t.Fatalf("statuses: %s/%s/%s", pending.Status, failed.Status, skipped.Status)
	}
	if _, err := SkipPendingTicketNotifications(db, ErrTicketWebhookNotConfigured.Error(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverStaleTicketNotifications(db, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&pending, rows[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&skipped, rows[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != ticketNotificationSkipped || skipped.Status != ticketNotificationPending {
		t.Fatalf("explicit recovery/skip statuses: %s/%s", pending.Status, skipped.Status)
	}
}

func TestTicketNotificationEnqueueSkipsWhenWecomIsInvalid(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "内容"})
	if err != nil {
		t.Fatal(err)
	}
	var notice entity.TicketNotification
	if err := db.Where("ticket_id = ?", detail.Ticket.ID).First(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if notice.Status != ticketNotificationSkipped || !strings.Contains(notice.LastError, "未启用") {
		t.Fatalf("invalid configuration should skip: %+v", notice)
	}
}

func TestTicketNotificationEnqueueSkipsInvalidWebhookURLWithoutSending(t *testing.T) {
	db := newTicketTestDB(t)
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: "http://example.invalid/webhook"},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	user := &entity.User{ID: 2, Username: "alice", Role: "user"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "内容"})
	if err != nil {
		t.Fatal(err)
	}
	var notice entity.TicketNotification
	if err := db.Where("ticket_id = ?", detail.Ticket.ID).First(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if notice.Status != ticketNotificationSkipped || !strings.Contains(notice.LastError, "Webhook") {
		t.Fatalf("invalid URL should be skipped: %+v", notice)
	}
}

func TestRetryTicketNotificationKeepsFailedWhenWebhookIsInvalid(t *testing.T) {
	db := newTicketTestDB(t)
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: "not-a-webhook"},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	notice := entity.TicketNotification{TicketID: 1, EventType: "new_ticket", Payload: "test", Status: ticketNotificationFailed, AttemptCount: 2}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if err := RetryTicketNotificationWithDB(db, notice.ID); !errors.Is(err, ErrTicketWebhookInvalid) {
		t.Fatalf("retry error = %v, want invalid webhook", err)
	}
	var stored entity.TicketNotification
	if err := db.First(&stored, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != ticketNotificationFailed || stored.AttemptCount != 2 {
		t.Fatalf("failed notification changed: %+v", stored)
	}
}

func TestTicketNotificationConcurrentClaimsSendOnce(t *testing.T) {
	db := newTicketTestDB(t)
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	notice := entity.TicketNotification{TicketID: 1, EventType: "new_ticket", Payload: "hello", Status: ticketNotificationPending, NextAttemptAt: &now}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot entity.TicketNotification
	if err := db.First(&snapshot, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	oldClient := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = oldClient })
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: closeReader{Reader: strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)}, Header: make(http.Header)}, nil
	})}
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() { firstResult <- processTicketNotification(context.Background(), db, snapshot, now) }()
	<-entered
	go func() { secondResult <- processTicketNotification(context.Background(), db, snapshot, now) }()
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(release)
		t.Fatal("second claim was blocked while webhook was in flight")
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("webhook calls = %d, want 1", calls.Load())
	}
	var stored entity.TicketNotification
	if err := db.First(&stored, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != ticketNotificationSent {
		t.Fatalf("notification status = %s, want sent", stored.Status)
	}
}

func TestSendTicketWebhookPayloadAndStatus(t *testing.T) {
	old := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = old })
	var requestBody []byte
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestBody, _ = io.ReadAll(req.Body)
		return &http.Response{StatusCode: http.StatusOK, Body: closeReader{Reader: strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)}, Header: make(http.Header)}, nil
	})}
	err := SendTicketWebhook(context.Background(), TicketWecomConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"}, "hello", []string{"user1"})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["msgtype"] != "text" {
		t.Fatalf("payload: %s", requestBody)
	}
}

func TestTicketNotificationFailureLogRedactsWebhookURL(t *testing.T) {
	db := newTicketTestDB(t)
	webhookURL := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=private-test-secret"
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: webhookURL},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	notice := entity.TicketNotification{TicketID: 8, EventType: "new_ticket", Payload: "hello", Status: ticketNotificationPending, NextAttemptAt: &now}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	oldClient := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = oldClient })
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: closeReader{Reader: strings.NewReader("upstream returned " + webhookURL)}, Header: make(http.Header)}, nil
	})}
	oldWriter := gin.DefaultWriter
	var logs bytes.Buffer
	gin.DefaultWriter = &logs
	t.Cleanup(func() { gin.DefaultWriter = oldWriter })
	if _, err := ProcessPendingTicketNotifications(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), webhookURL) || strings.Contains(logs.String(), "private-test-secret") {
		t.Fatalf("webhook secret leaked into logs: %s", logs.String())
	}
	var stored entity.TicketNotification
	if err := db.First(&stored, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.LastError, webhookURL) || strings.Contains(stored.LastError, "private-test-secret") {
		t.Fatalf("webhook secret leaked into stored error: %s", stored.LastError)
	}
}

func TestTicketNotificationFailureLogRedactsStandaloneWebhookKey(t *testing.T) {
	db := newTicketTestDB(t)
	key := "standalone-private-test-key"
	webhookURL := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + key
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: webhookURL},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	notice := entity.TicketNotification{TicketID: 8, EventType: "new_ticket", Payload: "hello", Status: ticketNotificationPending, NextAttemptAt: &now}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	oldClient := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = oldClient })
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: closeReader{Reader: strings.NewReader("invalid key: " + key)}, Header: make(http.Header)}, nil
	})}
	oldWriter := gin.DefaultWriter
	var logs bytes.Buffer
	gin.DefaultWriter = &logs
	t.Cleanup(func() { gin.DefaultWriter = oldWriter })
	if _, err := ProcessPendingTicketNotifications(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), key) {
		t.Fatalf("standalone webhook key leaked into logs: %s", logs.String())
	}
	var stored entity.TicketNotification
	if err := db.First(&stored, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.LastError, key) || !strings.Contains(stored.LastError, "[密钥已隐藏]") {
		t.Fatalf("unsafe stored error: %s", stored.LastError)
	}
	apiMessage := TicketWebhookErrorMessage(errors.New("invalid key: "+key), webhookURL)
	if strings.Contains(apiMessage, key) || !strings.Contains(apiMessage, "[密钥已隐藏]") {
		t.Fatalf("unsafe API error: %s", apiMessage)
	}
}

func TestTicketCreateRollsBackWhenOutboxWriteFails(t *testing.T) {
	db := newTicketTestDB(t)
	failure := errors.New("outbox unavailable")
	if err := db.Callback().Create().Before("gorm:create").Register("fail_ticket_outbox", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameTicketNotification {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	if _, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "消息"}); !errors.Is(err, failure) {
		t.Fatalf("expected outbox failure, got %v", err)
	}
	for _, model := range []interface{}{&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%T persisted after transaction rollback: %d", model, count)
		}
	}
}

func TestTicketUserReplyRollsBackWhenOutboxWriteFails(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin"}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "消息"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTicketWithDB(db, admin, detail.Ticket.ID, UpdateTicketInput{Status: entity.TicketStatusClosed}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("outbox unavailable")
	if err := db.Callback().Create().Before("gorm:create").Register("fail_ticket_outbox", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameTicketNotification {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTicketMessageWithDB(db, user, detail.Ticket.ID, ReplyTicketInput{Body: "追问"}); !errors.Is(err, failure) {
		t.Fatalf("expected outbox failure, got %v", err)
	}
	reloaded, err := GetTicketWithDB(db, admin, detail.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Ticket.Status != entity.TicketStatusClosed || reloaded.Ticket.ClosedAt == nil {
		t.Fatalf("reply transaction partially committed: %+v", reloaded)
	}
	var count int64
	if err := db.Model(&entity.TicketNotification{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox count after rollback: %d", count)
	}
}

func TestTicketNotificationCancellationKeepsPendingAndAttempts(t *testing.T) {
	db := newTicketTestDB(t)
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	notices := []entity.TicketNotification{
		{TicketID: 1, Payload: "first", Status: ticketNotificationPending, AttemptCount: 2, NextAttemptAt: &now},
		{TicketID: 2, Payload: "second", Status: ticketNotificationPending, NextAttemptAt: &now},
	}
	if err := db.Create(&notices).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = old })
	calls := 0
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})}
	processed, err := ProcessPendingTicketNotifications(ctx, db, now)
	if err != nil || processed != 1 || calls != 1 {
		t.Fatalf("processed=%d calls=%d err=%v", processed, calls, err)
	}
	for index, expectedAttempts := range []int{2, 0} {
		var stored entity.TicketNotification
		if err := db.First(&stored, notices[index].ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != ticketNotificationPending || stored.AttemptCount != expectedAttempts {
			t.Fatalf("cancellation changed retry budget: %+v", stored)
		}
	}
	processed, err = ProcessPendingTicketNotifications(ctx, db, now)
	if err != nil || processed != 0 || calls != 1 {
		t.Fatalf("canceled worker kept claiming: processed=%d calls=%d err=%v", processed, calls, err)
	}
}

func TestTicketWebhookRequiresCompleteAcknowledgement(t *testing.T) {
	old := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = old })
	config := TicketWecomConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"}
	for _, item := range []struct {
		name, body string
		retryable  bool
	}{
		{"missing errcode", `{}`, false},
		{"permanent error", `{"errcode":93000,"errmsg":"invalid webhook"}`, false},
		{"rate limit", `{"errcode":45009,"errmsg":"api freq out of limit"}`, true},
		{"server busy", `{"errcode":-1,"errmsg":"busy"}`, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: closeReader{Reader: strings.NewReader(item.body)}, Header: make(http.Header)}, nil
			})}
			err := SendTicketWebhook(context.Background(), config, "hello", nil)
			var deliveryError *webhookError
			if !errors.As(err, &deliveryError) || deliveryError.retryable != item.retryable {
				t.Fatalf("error=%v retryable=%v", err, item.retryable)
			}
		})
	}
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingTicketBody{}, Header: make(http.Header)}, nil
	})}
	var deliveryError *webhookError
	if err := SendTicketWebhook(context.Background(), config, "hello", nil); !errors.As(err, &deliveryError) || !deliveryError.retryable {
		t.Fatalf("body read error should be retryable: %v", err)
	}
}

func TestTicketWebhookDoesNotFollowRedirects(t *testing.T) {
	old := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = old })
	calls := 0
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Body: closeReader{Reader: strings.NewReader("")}, Header: http.Header{"Location": []string{"https://untrusted.example/hook"}}}, nil
	})}
	if err := SendTicketWebhook(context.Background(), TicketWecomConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"}, "private ticket", nil); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if calls != 1 {
		t.Fatalf("followed credential-bearing redirect: %d calls", calls)
	}
}

func TestTicketNotificationStaleWorkerCannotBypassRetryBackoff(t *testing.T) {
	db := newTicketTestDB(t)
	for _, setting := range []entity.SystemSetting{
		{Key: TicketWecomEnabledSettingKey, Value: "true"},
		{Key: TicketWecomWebhookURLSettingKey, Value: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	notice := entity.TicketNotification{TicketID: 1, Status: ticketNotificationPending, Payload: "hello", NextAttemptAt: &now}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot entity.TicketNotification
	if err := db.First(&snapshot, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	oldClient := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = oldClient })
	calls := 0
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: closeReader{Reader: strings.NewReader("busy")}, Header: make(http.Header)}, nil
	})}
	if err := processTicketNotification(context.Background(), db, snapshot, now); err != nil {
		t.Fatal(err)
	}
	if err := processTicketNotification(context.Background(), db, snapshot, now); err != nil {
		t.Fatal(err)
	}
	var stored entity.TicketNotification
	if err := db.First(&stored, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stored.AttemptCount != 1 || stored.Status != ticketNotificationPending || stored.NextAttemptAt == nil || !stored.NextAttemptAt.After(time.Now()) {
		t.Fatalf("stale snapshot reclaimed retry: calls=%d notice=%+v", calls, stored)
	}
}

type failingTicketBody struct{}

func (failingTicketBody) Read([]byte) (int, error) { return 0, errors.New("connection interrupted") }
func (failingTicketBody) Close() error             { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type closeReader struct{ *strings.Reader }

func (r closeReader) Close() error { return nil }
