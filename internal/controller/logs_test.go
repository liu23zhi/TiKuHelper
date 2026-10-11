package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"net/url"
)

func newLogControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:log-controller-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.User{}, &entity.Log{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	dao.SetDefault(db)
	return db
}

func TestUserLoginWritesAuditLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	if err := db.Create(&entity.User{
		ID:             201,
		Username:       "audit-login-user",
		Password:       "123456",
		Status:         "enabled",
		Role:           "user",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	originalConsume := consumeCaptchaTokenWithDB
	defer func() { consumeCaptchaTokenWithDB = originalConsume }()
	consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/user/login", bytes.NewBufferString(`{"username":"audit-login-user","password":"123456","captchaToken":"token-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.20.30.40:12345"
	request.Header.Set("X-Forwarded-For", "10.20.30.40")
	ctx.Request = request

	UserLogin(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var entry entity.Log
	if err := db.Where("module = ?", "auth").Where("user_id = ?", 201).First(&entry).Error; err != nil {
		t.Fatalf("load login audit log: %v", err)
	}
	if entry.Username != "audit-login-user" {
		t.Fatalf("expected username snapshot audit-login-user, got %+v", entry)
	}
	if entry.ClientIP != "10.20.30.40" {
		t.Fatalf("expected client ip 10.20.30.40, got %+v", entry)
	}
	if entry.Action != 10 {
		t.Fatalf("expected login action 10, got %+v", entry)
	}
	if !strings.Contains(entry.Description, "登录") {
		t.Fatalf("expected login description, got %+v", entry)
	}
}

func TestLogListAdminSeesAllAuditLogsWithMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 301, Username: "admin-log", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 302, Username: "member-log", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}

	baseTime := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	entries := []entity.Log{
		{
			UserID:      member.ID,
			Username:    member.Username,
			Module:      "auth",
			Action:      10,
			Description: "用户登录成功",
			ClientIP:    "127.0.0.9",
			CreateTime:  baseTime,
		},
		{
			UserID:      admin.ID,
			Username:    admin.Username,
			Module:      "question",
			Action:      1,
			Description: "更新题目答案",
			ClientIP:    "127.0.0.1",
			Qid:         77,
			OldAnswer:   "A",
			NewAnswer:   "B",
			CreateTime:  baseTime.Add(time.Minute),
		},
	}
	for _, entry := range entries {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
	ctx.Request = request
	ctx.Set("user", admin)

	LogList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode log list response: %v", err)
	}
	if response.Total != 2 || len(response.Data) != 2 {
		t.Fatalf("expected admin to see 2 logs, got total=%d data=%d", response.Total, len(response.Data))
	}

	for _, item := range response.Data {
		if item["module"] != "auth" {
			continue
		}
		if item["username"] != "member-log" {
			t.Fatalf("expected username member-log, got %#v", item["username"])
		}
		if item["clientIp"] != "127.0.0.9" {
			t.Fatalf("expected clientIp 127.0.0.9, got %#v", item["clientIp"])
		}
		if item["description"] != "用户登录成功" {
			t.Fatalf("expected login description, got %#v", item["description"])
		}
		return
	}

	t.Fatal("expected auth module log in response")
}

func TestLogListResolvesUsernameForLegacyRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 311, Username: "admin-legacy-log", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 312, Username: "legacy-member", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := db.Create(&entity.Log{
		UserID:      member.ID,
		Username:    "",
		Module:      "question",
		Action:      3,
		Description: "查询题目",
		ClientIP:    "127.0.0.10",
		CreateTime:  time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC),
	}).Error; err != nil {
		t.Fatalf("seed legacy log: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
	ctx.Set("user", admin)

	LogList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode log list response: %v", err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one legacy log, got %d", len(response.Data))
	}
	if response.Data[0]["username"] != "legacy-member" {
		t.Fatalf("expected username legacy-member, got %#v", response.Data[0]["username"])
	}
}

func TestLogListAdminCanFilterByModuleAndAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 401, Username: "admin-filter", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 402, Username: "member-filter", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}

	baseTime := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	entries := []entity.Log{
		{UserID: member.ID, Username: member.Username, Module: "auth", Action: 10, Description: "登录成功", ClientIP: "127.0.0.9", CreateTime: baseTime},
		{UserID: admin.ID, Username: admin.Username, Module: "question", Action: 1, Description: "更新题目", ClientIP: "127.0.0.1", CreateTime: baseTime.Add(time.Minute)},
	}
	for _, entry := range entries {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10&module=auth&action=10", nil)
	ctx.Request = request
	ctx.Set("user", admin)

	LogList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode log list response: %v", err)
	}
	if response.Total != 1 || len(response.Data) != 1 {
		t.Fatalf("expected filtered result to contain 1 log, got total=%d data=%d", response.Total, len(response.Data))
	}
	if response.Data[0]["module"] != "auth" {
		t.Fatalf("expected auth module, got %#v", response.Data[0]["module"])
	}
	if response.Data[0]["action"] != float64(10) {
		t.Fatalf("expected login action 10, got %#v", response.Data[0]["action"])
	}
}

func TestLogListSearchesByKeyword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 411, Username: "admin-search", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 412, Username: "member-search", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}

	baseTime := time.Date(2026, 5, 7, 9, 0, 0, 0, time.UTC)
	entries := []entity.Log{
		{UserID: member.ID, Username: member.Username, Module: "question", Action: 3, Description: "搜索答案，题目：牛顿第二定律 requestId=req-abc-123", ClientIP: "203.0.113.7", CreateTime: baseTime},
		{UserID: admin.ID, Username: admin.Username, Module: "auth", Action: 10, Description: "登录成功", ClientIP: "127.0.0.1", CreateTime: baseTime.Add(time.Minute)},
		{UserID: member.ID, Username: member.Username, Module: "ai", Action: 11, Description: "AI 回退完成", ClientIP: "203.0.113.9", CreateTime: baseTime.Add(2 * time.Minute)},
	}
	for _, entry := range entries {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}

	cases := []struct {
		name        string
		keyword     string
		expectedIDs []string
	}{
		{name: "matches description", keyword: "牛顿第二定律", expectedIDs: []string{"req-abc-123"}},
		{name: "matches request id fragment", keyword: "req-abc", expectedIDs: []string{"req-abc-123"}},
		{name: "matches client ip", keyword: "203.0.113.9", expectedIDs: []string{"AI 回退完成"}},
		{name: "matches username case insensitively", keyword: "MEMBER-SEARCH", expectedIDs: []string{"AI 回退完成", "req-abc-123"}},
		{name: "no match", keyword: "不存在的关键词", expectedIDs: nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10&keyword="+url.QueryEscape(testCase.keyword), nil)
			ctx.Set("user", admin)

			LogList(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}

			var response struct {
				Data  []map[string]any `json:"data"`
				Total int              `json:"total"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode log list response: %v", err)
			}
			if response.Total != len(testCase.expectedIDs) {
				t.Fatalf("expected %d matches, got total=%d data=%d", len(testCase.expectedIDs), response.Total, len(response.Data))
			}

			// 日志按 id DESC 返回，比对时忽略顺序差异。
			got := make(map[string]struct{}, len(response.Data))
			for _, item := range response.Data {
				description, _ := item["description"].(string)
				if strings.Contains(description, "req-abc-123") {
					got["req-abc-123"] = struct{}{}
				} else {
					got[description] = struct{}{}
				}
			}
			for _, want := range testCase.expectedIDs {
				if _, ok := got[want]; !ok {
					t.Fatalf("expected %q in results, got %+v", want, response.Data)
				}
			}
		})
	}
}

func TestLogListKeywordTreatsLikeWildcardsLiterally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 421, Username: "wild-log-admin", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	baseTime := time.Date(2026, 5, 8, 9, 0, 0, 0, time.UTC)
	entries := []entity.Log{
		{UserID: admin.ID, Username: admin.Username, Module: "question", Action: 3, Description: "普通描述", ClientIP: "127.0.0.2", CreateTime: baseTime},
		{UserID: admin.ID, Username: admin.Username, Module: "question", Action: 3, Description: "含_下划线", ClientIP: "127.0.0.3", CreateTime: baseTime.Add(time.Minute)},
	}
	for _, entry := range entries {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}

	// "%" 必须按字面量处理，否则会匹配到全部日志。
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?keyword="+url.QueryEscape("%"), nil)
	ctx.Set("user", admin)
	LogList(ctx)

	var response struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode log list response: %v", err)
	}
	if response.Total != 0 {
		t.Fatalf("expected %% to match nothing, got %d", response.Total)
	}
}
