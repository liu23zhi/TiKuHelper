package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	if err := db.AutoMigrate(&entity.User{}, &entity.Log{}, &entity.BillingRecord{}); err != nil {
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

func TestLogListRejectsReversedCreatedTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 301, Role: "admin", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?createdFrom=2026-06-21&createdTo=2026-06-20", nil)
	LogList(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "创建时间范围无效") {
		t.Fatalf("reversed log range must return explicit 400, status=%d body=%s", recorder.Code, recorder.Body.String())
	}
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

func TestLogListAdminCanFilterByDateUserQuestionAndIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 421, Username: "admin-filter-details", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 422, Username: "member-filter-details", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	baseTime := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	entries := []entity.Log{
		{UserID: member.ID, Username: member.Username, Module: "question", Action: 3, Qid: 77, Description: "修改题目", ClientIP: "203.0.113.20", CreateTime: baseTime},
		{UserID: member.ID, Username: member.Username, Module: "question", Action: 3, Qid: 78, Description: "修改其他题目", ClientIP: "203.0.113.20", CreateTime: baseTime},
		{UserID: member.ID, Username: member.Username, Module: "question", Action: 3, Qid: 77, Description: "其他 IP", ClientIP: "203.0.113.21", CreateTime: baseTime},
		{UserID: member.ID, Username: member.Username, Module: "question", Action: 3, Qid: 77, Description: "其他日期", ClientIP: "203.0.113.20", CreateTime: baseTime.Add(24 * time.Hour)},
		{UserID: admin.ID, Username: admin.Username, Module: "question", Action: 3, Qid: 77, Description: "其他用户", ClientIP: "203.0.113.20", CreateTime: baseTime},
	}
	for _, entry := range entries {
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10&createdFrom=2026-05-06&createdTo=2026-05-06&userId=422&qid=77&clientIp=203.0.113.20", nil)
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
		t.Fatalf("decode filtered log list response: %v", err)
	}
	if response.Total != 1 || len(response.Data) != 1 || response.Data[0]["description"] != "修改题目" {
		t.Fatalf("expected one matching log, got total=%d data=%+v", response.Total, response.Data)
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

func TestLogListAssociatesBillingIDsWithinUserAndVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 431, Username: "billing-log-admin", Role: "admin", Status: "enabled"}
	member := &entity.User{ID: 432, Username: "billing-log-member", Role: "user", Status: "enabled"}
	other := &entity.User{ID: 433, Username: "billing-log-other", Role: "user", Status: "enabled"}
	for _, user := range []*entity.User{admin, member, other} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}

	logs := []entity.Log{
		{UserID: member.ID, Username: member.Username, Module: "search", Description: "搜索完成 requestId=shared-req mode=normal"},
		{UserID: other.ID, Username: other.Username, Module: "search", Description: "搜索完成 requestId=shared-req mode=normal"},
		{UserID: member.ID, Username: member.Username, Module: "search", Description: "搜索无结果 requestId=no-bill"},
		{UserID: 0, Username: "anonymous", Module: "search", Description: "搜索完成 requestId=shared-req"},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}
	bills := []entity.BillingRecord{
		{ID: 44, UserID: member.ID, RequestID: "shared-req", Type: "normal_search", HiddenFromStatement: true},
		{ID: 42, UserID: member.ID, RequestID: "shared-req", Type: "normal_search"},
		{ID: 41, UserID: member.ID, RequestID: "shared-req", Type: "normal_search"},
		{ID: 43, UserID: other.ID, RequestID: "shared-req", Type: "normal_search"},
		{ID: 45, UserID: member.ID, RequestID: "other-req", Type: "normal_search"},
	}
	for i := range bills {
		if err := db.Create(&bills[i]).Error; err != nil {
			t.Fatalf("seed bill: %v", err)
		}
	}

	request := func(user *entity.User) map[int32][]int32 {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
		ctx.Set("user", user)
		LogList(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("log list status %d: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data []struct {
				ID          int32   `json:"id"`
				BillingIDs  []int32 `json:"billingIds"`
				Description string  `json:"description"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode log list: %v", err)
		}
		result := make(map[int32][]int32, len(response.Data))
		for _, item := range response.Data {
			if item.Description == "" || item.BillingIDs == nil {
				t.Fatalf("expected existing description and non-nil billingIds: %+v", item)
			}
			result[item.ID] = item.BillingIDs
		}
		return result
	}

	adminResult := request(admin)
	if len(adminResult) != 4 {
		t.Fatalf("admin expected all 4 logs, got %v", adminResult)
	}
	for logIndex, want := range map[int][]int32{
		0: {41, 42, 44},
		1: {43},
		2: {},
		3: {},
	} {
		if got := adminResult[logs[logIndex].ID]; !reflect.DeepEqual(got, want) {
			t.Fatalf("admin log %d billingIds = %v, want %v", logIndex, got, want)
		}
	}

	memberResult := request(member)
	if len(memberResult) != 2 {
		t.Fatalf("member expected own 2 logs, got %v", memberResult)
	}
	if got := memberResult[logs[0].ID]; !reflect.DeepEqual(got, []int32{41, 42}) {
		t.Fatalf("member visible billingIds = %v, want [41 42]", got)
	}
	if got := memberResult[logs[2].ID]; !reflect.DeepEqual(got, []int32{}) {
		t.Fatalf("member unbilled log billingIds = %v, want []", got)
	}
}

func TestLogListQueriesBillingIDsOnceForCurrentPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 441, Username: "billing-page-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	for _, requestID := range []string{"older", "current-a", "current-b"} {
		entry := entity.Log{UserID: admin.ID, Username: admin.Username, Module: "search", Description: "搜索完成 requestId=" + requestID}
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
		bill := entity.BillingRecord{UserID: admin.ID, RequestID: requestID, Type: "normal_search"}
		if err := db.Create(&bill).Error; err != nil {
			t.Fatalf("seed bill: %v", err)
		}
	}

	billingQueries := 0
	const callbackName = "test:count-log-list-billing-queries"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == entity.TableNameBillingRecord {
			billingQueries++
		}
	}); err != nil {
		t.Fatalf("register query counter: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=2", nil)
	ctx.Set("user", admin)
	LogList(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("log list status %d: %s", recorder.Code, recorder.Body.String())
	}
	if billingQueries != 1 {
		t.Fatalf("expected one billing query for two logs, got %d", billingQueries)
	}
	var response struct {
		Data []struct {
			Description string  `json:"description"`
			BillingIDs  []int32 `json:"billingIds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode log list: %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("expected two current-page logs, got %d", len(response.Data))
	}
	for _, item := range response.Data {
		if strings.Contains(item.Description, "older") || len(item.BillingIDs) != 1 {
			t.Fatalf("unexpected current-page log: %+v", item)
		}
	}
}
