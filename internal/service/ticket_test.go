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
	"unicode/utf8"

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

func TestTicketManagementDetailIncludesFeedbackUserSummary(t *testing.T) {
	db := newTicketTestDB(t)
	lastLogin := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	user := &entity.User{ID: 11, Username: "ticket-user", Nickname: "普通用户aa", Role: "user", Status: "enabled", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), LastLoginAt: &lastLogin, LastLoginIP: "192.0.2.10"}
	admin := &entity.User{ID: 12, Username: "ticket-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "首条"})
	if err != nil {
		t.Fatal(err)
	}
	management, err := GetTicketWithDBForView(db, admin, detail.Ticket.ID, false)
	if err != nil || management.User == nil {
		t.Fatalf("management detail user summary missing: %+v %v", management.User, err)
	}
	if management.User.Nickname != "普通用户aa" || management.User.ID != user.ID || management.User.LastLoginAt == nil || management.User.LastLoginIP != "192.0.2.10" {
		t.Fatalf("unexpected management user summary: %+v", management.User)
	}
	feedback, err := GetTicketWithDBForView(db, user, detail.Ticket.ID, true)
	if err != nil || feedback.User != nil {
		t.Fatalf("feedback view must not expose user summary: %+v %v", feedback.User, err)
	}
}

func TestTicketDetailReadsCurrentNicknamesWithoutChangingAuditSnapshots(t *testing.T) {
	db := newTicketTestDB(t)
	owner := &entity.User{ID: 41, Username: "nickname-owner", Nickname: "旧用户昵称", Role: "user"}
	admin := &entity.User{ID: 42, Username: "nickname-admin", Nickname: "旧管理员昵称", Role: "admin"}
	for _, actor := range []*entity.User{owner, admin} {
		if err := db.Create(actor).Error; err != nil {
			t.Fatal(err)
		}
	}
	detail, err := CreateTicketWithDB(db, owner, CreateTicketInput{Subject: "昵称回归", Body: "首条"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddTicketMessageWithDB(db, admin, detail.Ticket.ID, ReplyTicketInput{Body: "回复"}); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[int32]string{owner.ID: "现用户昵称", admin.ID: "现管理员昵称"} {
		if err := db.Model(&entity.User{}).Where("id = ?", id).Update("nickname", name).Error; err != nil {
			t.Fatal(err)
		}
	}
	management, err := GetTicketWithDB(db, admin, detail.Ticket.ID)
	if err != nil || management.Ticket.UserName != "现用户昵称" || management.Messages[0].AuthorName != "现用户昵称" || management.Messages[1].AuthorName != "现管理员昵称" {
		t.Fatalf("detail must read current names: %+v %v", management, err)
	}
	feedback, err := GetTicketWithDBForView(db, owner, detail.Ticket.ID, true)
	if err != nil || feedback.Messages[0].AuthorName != "现用户昵称" || feedback.Messages[1].AuthorName != "管理员" || feedback.Messages[1].UserID != 0 {
		t.Fatalf("feedback current nickname/privacy: %+v %v", feedback.Messages, err)
	}
	var stored entity.TicketMessage
	if err := db.First(&stored, management.Messages[0].ID).Error; err != nil || stored.AuthorName != "旧用户昵称" {
		t.Fatalf("audit snapshot changed: %+v %v", stored, err)
	}
}

func TestTicketFeedbackViewHidesAdministratorIdentityAndUsesUserReplies(t *testing.T) {
	db := newTicketTestDB(t)
	owner := &entity.User{ID: 21, Username: "feedback-owner", Nickname: "反馈昵称", Role: "admin"}
	responder := &entity.User{ID: 22, Username: "private-admin-name", Nickname: "具体管理员", Role: "admin"}
	for _, user := range []*entity.User{owner, responder} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	detail, err := CreateTicketWithDB(db, owner, CreateTicketInput{Subject: "管理员自助反馈", Body: "首条"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddTicketMessageWithDBForView(db, responder, detail.Ticket.ID, ReplyTicketInput{Body: "管理回复"}, false); err != nil {
		t.Fatal(err)
	}
	feedback, err := GetTicketWithDBForView(db, owner, detail.Ticket.ID, true)
	if err != nil || len(feedback.Messages) != 2 {
		t.Fatalf("feedback detail: %+v %v", feedback, err)
	}
	if reply := feedback.Messages[1]; reply.AuthorRole != "admin" || reply.AuthorName != "管理员" || reply.UserID != 0 {
		t.Fatalf("feedback response leaked administrator identity: %+v", reply)
	}
	closed := entity.TicketStatusClosed
	feedback, err = AddTicketMessageWithDBForView(db, owner, detail.Ticket.ID, ReplyTicketInput{Body: "自助追加", Status: &closed}, true)
	if err != nil || len(feedback.Messages) != 3 {
		t.Fatalf("feedback reply: %+v %v", feedback, err)
	}
	if reply := feedback.Messages[2]; reply.AuthorRole != "user" || reply.AuthorName != "反馈昵称" || reply.UserID != owner.ID {
		t.Fatalf("self-view reply must use feedback user identity: %+v", reply)
	}
	if feedback.Ticket.Status != entity.TicketStatusOpen {
		t.Fatalf("self-view reply must reopen as a user, not apply administrator status: %s", feedback.Ticket.Status)
	}
	management, err := GetTicketWithDBForView(db, responder, detail.Ticket.ID, false)
	if err != nil || management.Messages[1].AuthorName != "具体管理员" || management.Messages[1].UserID != responder.ID {
		t.Fatalf("management must retain stored administrator identity: %+v %v", management.Messages, err)
	}
	var notice entity.TicketNotification
	if err := db.Where("ticket_id = ? AND event_type = ?", detail.Ticket.ID, "user_reply").First(&notice).Error; err != nil {
		t.Fatalf("self-view reply must notify managers as user reply: %v", err)
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

func TestTicketListKeywordSearchKeepsAdministratorIdentityInManagementView(t *testing.T) {
	db := newTicketTestDB(t)
	owner := &entity.User{ID: 31, Username: "feedback-search-user", Nickname: "反馈用户昵称", Role: "user"}
	adminOwner := &entity.User{ID: 32, Username: "feedback-search-admin", Nickname: "自助反馈昵称", Role: "admin"}
	responder := &entity.User{ID: 33, Username: "staff-search-login", Nickname: "只在署名出现的管理员", Role: "admin"}
	for _, user := range []*entity.User{owner, adminOwner, responder} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, actor := range []*entity.User{owner, adminOwner} {
		detail, err := CreateTicketWithDB(db, actor, CreateTicketInput{Subject: "可见反馈标题", Body: "用户可见问题正文"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AddTicketMessageWithDBForView(db, responder, detail.Ticket.ID, ReplyTicketInput{Body: "用户可见答复正文"}, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name     string
		actor    *entity.User
		selfOnly bool
		keyword  string
		want     int64
	}{
		{"ordinary user cannot infer admin name", owner, false, responder.Nickname, 0},
		{"ordinary self scope cannot infer partial admin name", owner, true, "署名出现", 0},
		{"administrator self scope cannot infer admin name", adminOwner, true, responder.Nickname, 0},
		{"management can search admin name", adminOwner, false, responder.Nickname, 2},
		{"management can search partial admin name", responder, false, "署名出现", 2},
		{"ordinary user can search ticket title", owner, false, "可见反馈标题", 1},
		{"ordinary user can search own body", owner, false, "问题正文", 1},
		{"ordinary user can search admin reply body", owner, false, "答复正文", 1},
		{"ordinary user can search own nickname", owner, false, owner.Nickname, 1},
		{"ordinary user can search own login name", owner, false, owner.Username, 1},
		{"administrator self scope can search own nickname", adminOwner, true, adminOwner.Nickname, 1},
		{"administrator self scope can search admin reply body", adminOwner, true, "答复正文", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ListTicketsWithDB(db, tc.actor, TicketListQuery{Keyword: tc.keyword, SelfOnly: tc.selfOnly}, 1, 20)
			if err != nil || result.Total != tc.want || int64(len(result.Items)) != tc.want {
				t.Fatalf("keyword=%q selfOnly=%v result=%+v err=%v", tc.keyword, tc.selfOnly, result, err)
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
	if payload["msgtype"] != "markdown" {
		t.Fatalf("payload: %s", requestBody)
	}
	markdown, ok := payload["markdown"].(map[string]interface{})
	if !ok || markdown["content"] != "hello\n\n<@user1>" {
		t.Fatalf("markdown payload: %s", requestBody)
	}
	if _, ok := markdown["mentioned_list"]; ok {
		t.Fatalf("markdown must use inline mentions: %s", requestBody)
	}
}

func TestBuildTicketNotificationContentUsesMarkdownDetails(t *testing.T) {
	content := buildTicketNotificationContent(entity.Ticket{ID: 1, Subject: "系统太卡了", UserName: "admin2"}, "系统太卡了", "new_ticket")
	if strings.Contains(content, "/adapter/tickets/1") {
		t.Fatalf("notification must not contain ticket route: %s", content)
	}
	for _, expected := range []string{"## 新工单", "**工单 #1**", "**标题：** 系统太卡了", "**用户：** admin2", "**内容：**", "系统太卡了"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("notification missing %q: %s", expected, content)
		}
	}
}

func TestTicketMarkdownPreservesDetailsAndEscapesUserText(t *testing.T) {
	body := "第一行\n第二行 " + strings.Repeat("详细内容", 180)
	content := buildTicketNotificationContent(entity.Ticket{ID: 2, Subject: "*标题* <@all>", UserName: "[用户](https://example.com)", Status: entity.TicketStatusPending}, body, "user_reply")
	if !strings.Contains(content, body) || !strings.HasPrefix(content, "## 用户追问") || !strings.Contains(content, "**状态：** 处理中") {
		t.Fatalf("details lost: %s", content)
	}
	if strings.Contains(content, "<@all>") || strings.Contains(content, "*标题*") || strings.Contains(content, "[用户](") {
		t.Fatalf("user text can inject markdown/mentions: %s", content)
	}
}

func TestTicketReplyNotificationShowsUpdatedStatusAndTime(t *testing.T) {
	db := newTicketTestDB(t)
	user := &entity.User{ID: 1, Username: "alice"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin"}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "重新打开工单", Body: "最初消息"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTicketWithDB(db, admin, detail.Ticket.ID, UpdateTicketInput{Status: entity.TicketStatusClosed}); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := db.Model(&entity.Ticket{}).Where("id = ?", detail.Ticket.ID).Update("last_message_at", oldTime).Error; err != nil {
		t.Fatal(err)
	}
	replied, err := AddTicketMessageWithDB(db, user, detail.Ticket.ID, ReplyTicketInput{Body: "补充新的详情"})
	if err != nil {
		t.Fatal(err)
	}
	var notice entity.TicketNotification
	if err := db.Where("ticket_id = ? AND event_type = ?", detail.Ticket.ID, "user_reply").First(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice.Payload, "**状态：** 待处理") || !strings.Contains(notice.Payload, replied.Ticket.LastMessageAt.Format("2006-01-02 15:04:05")) || !strings.Contains(notice.Payload, "补充新的详情") {
		t.Fatalf("reply notification has stale metadata: %s", notice.Payload)
	}
}

func TestTicketMarkdownRespectsUTF8ByteLimitAndKeepsMentions(t *testing.T) {
	old := ticketWebhookHTTPClient
	t.Cleanup(func() { ticketWebhookHTTPClient = old })
	ticketWebhookHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		content := payload.Markdown.Content
		if len(content) > 4096 || !utf8.ValidString(content) || !strings.Contains(content, "内容过长，已截断") || !strings.HasSuffix(content, "<@user1> <@all>") {
			t.Fatalf("invalid long markdown (%d bytes): %s", len(content), content)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: closeReader{Reader: strings.NewReader(`{"errcode":0}`)}, Header: make(http.Header)}, nil
	})}
	if err := SendTicketWebhook(context.Background(), TicketWecomConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"}, strings.Repeat("工单详情", 1500), []string{"user1", "@all", "user1"}); err != nil {
		t.Fatal(err)
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
