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
	"gorm.io/gorm"
)

// logProjectRow 是列表响应里「项目」列相关的最小字段集。
type logProjectRow struct {
	ID          int32   `json:"id"`
	Module      string  `json:"module"`
	Description string  `json:"description"`
	Project     string  `json:"project"`
	BillingIDs  []int32 `json:"billingIds"`
}

func requestLogProjectRows(t *testing.T, user *entity.User, query string) []logProjectRow {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/logs?page=1&pageSize=50"+query, nil)
	ctx.Set("user", user)
	LogList(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("log list status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []logProjectRow `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode log list: %v", err)
	}
	return response.Data
}

// 每一行都必须带 project 字段，且永不为空字符串（兜底 "-"）。
func TestLogListAlwaysReturnsNonEmptyProject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 601, Username: "project-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	entries := []entity.Log{
		{UserID: admin.ID, Username: admin.Username, Module: "", Qid: 0, Description: "完全无依据"},
		{UserID: admin.ID, Username: admin.Username, Module: "", Qid: 42, Description: "题目增删改"},
		{UserID: admin.ID, Username: admin.Username, Module: "unknown_module", Description: "未知模块"},
	}
	for i := range entries {
		if err := db.Create(&entries[i]).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}

	rows := requestLogProjectRows(t, admin, "")
	if len(rows) != len(entries) {
		t.Fatalf("expected %d rows, got %d", len(entries), len(rows))
	}
	projects := make(map[int32]string, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Project) == "" {
			t.Fatalf("project must never be empty: %+v", row)
		}
		projects[row.ID] = row.Project
	}
	if got := projects[entries[0].ID]; got != "-" {
		t.Fatalf("row without any basis must fall back to -, got %q", got)
	}
	if got := projects[entries[1].ID]; got != "题目 #42" {
		t.Fatalf("qid-only row must expose the question id, got %q", got)
	}
	if got := projects[entries[2].ID]; got != "-" {
		t.Fatalf("unknown module without qid must fall back to -, got %q", got)
	}
}

// search 分支必须与账单明细的「项目」同口径，且不增加任何查询次数。
func TestLogListSearchProjectMatchesBillingSubjectOncePerPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	admin := &entity.User{ID: 611, Username: "project-search-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	logs := []entity.Log{
		{UserID: admin.ID, Username: admin.Username, Module: "search", Description: "搜索完成 requestId=req-with-bill mode=normal"},
		{UserID: admin.ID, Username: admin.Username, Module: "search", Description: "搜索完成 requestId=req-without-bill mode=normal"},
		{UserID: admin.ID, Username: admin.Username, Module: "search", Description: "搜索完成 mode=normal"},
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}
	bills := []entity.BillingRecord{
		{UserID: admin.ID, RequestID: "req-with-bill", Type: "normal_search", QuestionText: "牛顿第二定律"},
		{UserID: admin.ID, RequestID: "req-with-bill", Type: "normal_search", QuestionText: "牛顿第二定律"},
		{UserID: admin.ID, RequestID: "req-tool-test", Type: "normal_search", QuestionText: "AI 工具调用测试（联网搜索 + 网页浏览）：OpenAI"},
	}
	for i := range bills {
		if err := db.Create(&bills[i]).Error; err != nil {
			t.Fatalf("seed bill: %v", err)
		}
	}

	billingQueries := 0
	const callbackName = "test:count-log-list-project-billing-queries"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == entity.TableNameBillingRecord {
			billingQueries++
		}
	}); err != nil {
		t.Fatalf("register query counter: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	rows := requestLogProjectRows(t, admin, "")
	if billingQueries != 1 {
		t.Fatalf("project derivation must reuse the single billing query, got %d", billingQueries)
	}
	projects := make(map[int32]string, len(rows))
	billingIDs := make(map[int32][]int32, len(rows))
	for _, row := range rows {
		projects[row.ID] = row.Project
		billingIDs[row.ID] = row.BillingIDs
	}
	if got := projects[logs[0].ID]; got != "搜索答案，题目：牛顿第二定律" {
		t.Fatalf("search row with bill must reuse the billing subject, got %q", got)
	}
	if got := projects[logs[1].ID]; got != "搜题 requestId=req-without-bill" {
		t.Fatalf("search row without bill must degrade to requestId, got %q", got)
	}
	if got := projects[logs[2].ID]; got != "搜题" {
		t.Fatalf("search row without any id must degrade to 搜题, got %q", got)
	}
	// project 与 billingIds 必须共存且互不影响。
	if len(billingIDs[logs[0].ID]) != 2 || len(billingIDs[logs[1].ID]) != 0 {
		t.Fatalf("billingIds changed by project derivation: %+v", billingIDs)
	}
}

// 非管理员看不到隐藏账单的题目：project 不能泄露 hidden_from_statement=true 的内容。
func TestLogListProjectDoesNotLeakHiddenBillingQuestion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	member := &entity.User{ID: 621, Username: "project-member", Role: "user", Status: "enabled"}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	entry := entity.Log{UserID: member.ID, Username: member.Username, Module: "search", Description: "搜索完成 requestId=req-hidden"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}
	hidden := entity.BillingRecord{
		UserID: member.ID, RequestID: "req-hidden", Type: "normal_search",
		QuestionText: "隐藏流水的题目原文", HiddenFromStatement: true,
	}
	if err := db.Create(&hidden).Error; err != nil {
		t.Fatalf("seed hidden bill: %v", err)
	}

	rows := requestLogProjectRows(t, member, "")
	if len(rows) != 1 {
		t.Fatalf("expected the member's single log, got %+v", rows)
	}
	if strings.Contains(rows[0].Project, "隐藏流水的题目原文") {
		t.Fatalf("hidden billing question leaked into project: %q", rows[0].Project)
	}
	if len(rows[0].BillingIDs) != 0 {
		t.Fatalf("hidden bill must stay unassociated: %+v", rows[0].BillingIDs)
	}
	if rows[0].Project != "搜题 requestId=req-hidden" {
		t.Fatalf("expected requestId fallback, got %q", rows[0].Project)
	}
}

// 账户类模块（白名单结构化快照）与题目类日志的派生分支。
func TestBuildLogProjectAccountAndQuestionBranches(t *testing.T) {
	cases := []struct {
		name    string
		entry   *entity.Log
		billing *logBillingProjectMeta
		want    string
	}{
		{
			name:  "user target id from snapshot",
			entry: &entity.Log{Module: "user", NewAnswer: `{"userId":7,"username":"u7","balance":1.5}`},
			want:  "用户 #7",
		},
		{
			name:  "user falls back to description id",
			entry: &entity.Log{Module: "user", Description: "管理员 a(ID=1) 修改用户 b(ID=9) 余额：管理员添加了 ¥ 1"},
			want:  "用户 #9",
		},
		{
			name:  "access token name from snapshot",
			entry: &entity.Log{Module: "access_token", NewAnswer: `{"tokenId":12,"after":{"name":"openai-key"}}`},
			want:  "访问令牌 openai-key",
		},
		{
			name:  "access token id only",
			entry: &entity.Log{Module: "access_token", NewAnswer: `{"tokenId":12}`},
			want:  "令牌 #12",
		},
		{
			name:  "recharge order number",
			entry: &entity.Log{Module: "recharge", NewAnswer: `{"orderNo":"R20261109001","status":"success"}`},
			want:  "充值订单 R20261109001",
		},
		{
			name:  "recharge correlation fallback",
			entry: &entity.Log{Module: "recharge", CorrelationID: "R20261109002"},
			want:  "充值订单 R20261109002",
		},
		{
			name:  "billing record id from snapshot",
			entry: &entity.Log{Module: "billing", NewAnswer: `{"billingRecordId":88,"showInStatement":false}`},
			want:  "账单 #88",
		},
		{
			name:  "billing id from description",
			entry: &entity.Log{Module: "billing", Description: "管理员 a(ID=1) 修改用户 ID=3 的账单 #91 流水展示：true → false"},
			want:  "账单 #91",
		},
		{
			name:  "sms record id",
			entry: &entity.Log{Module: "sms", NewAnswer: `{"status":"success","smsRecordId":33}`},
			want:  "短信 #33",
		},
		{
			name:  "captcha correlation",
			entry: &entity.Log{Module: "captcha", CorrelationID: "remote:abc123"},
			want:  "远程验证码 abc123",
		},
		{
			name:  "captcha description fallback",
			entry: &entity.Log{Module: "captcha", Description: "远程验证码生成成功 captchaId=cap-9 result=success"},
			want:  "验证码 cap-9",
		},
		{
			name:  "auth login",
			entry: &entity.Log{Module: "auth", Action: 10},
			want:  "登录",
		},
		{
			name:  "auth unknown action",
			entry: &entity.Log{Module: "auth", Action: 99},
			want:  "账号",
		},
		{
			name:  "review rebate claim",
			entry: &entity.Log{Module: "review_rebate", NewAnswer: `{"claimId":5,"status":"pending"}`},
			want:  "评价返现申请 #5",
		},
		{
			name:  "review rebate without id",
			entry: &entity.Log{Module: "review_rebate", Description: "复核评价返现"},
			want:  "评价返现",
		},
		{
			name:  "user exam tool file name",
			entry: &entity.Log{Module: "userexam_tool", NewAnswer: `{"name":"exam.db","status":"success"}`},
			want:  "客户端数据库：exam.db",
		},
		{
			name:  "ai fallback request id",
			entry: &entity.Log{Module: "ai_fallback", NewAnswer: `{"requestId":"req-ai-1","status":"success"}`},
			want:  "AI 兜底 req-ai-1",
		},
		{
			name:  "search prefers billing question",
			entry: &entity.Log{Module: "search", Description: "搜索完成 requestId=req-1"},
			billing: &logBillingProjectMeta{
				Type: "normal_search", QuestionText: "AI 压缩历史回读测试",
			},
			want: "AI 压缩历史回读测试",
		},
		{
			name:    "search billing without question degrades to requestId",
			entry:   &entity.Log{Module: "search", Description: "搜索完成 requestId=req-2"},
			billing: &logBillingProjectMeta{Type: "normal_search"},
			want:    "搜题 requestId=req-2",
		},
		{
			// 关联到的账单不是搜题类时按账单口径取备注（与账单明细「项目」一致）。
			name:    "search billing with non-search type uses remark",
			entry:   &entity.Log{Module: "search", Description: "搜索完成 requestId=req-3"},
			billing: &logBillingProjectMeta{Type: "recharge", Remark: "账户充值", QuestionText: "不应出现"},
			want:    "账户充值",
		},
		{
			name:  "nil entry",
			entry: nil,
			want:  "-",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := buildLogProject(testCase.entry, testCase.billing); got != testCase.want {
				t.Fatalf("buildLogProject = %q, want %q", got, testCase.want)
			}
		})
	}
}

// buildLogProject 必须是纯函数：整页派生不触发任何数据库查询。
func TestFillLogProjectsDoesNotQueryDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	queries := 0
	const callbackName = "test:count-project-derivation-queries"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		queries++
	}); err != nil {
		t.Fatalf("register query counter: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	baseTime := time.Date(2026, 5, 8, 9, 0, 0, 0, time.UTC)
	items := []logListItem{
		{Log: &entity.Log{ID: 1, Module: "search", Description: "搜索完成 requestId=req-1", CreateTime: baseTime}, BillingIDs: []int32{11}},
		{Log: &entity.Log{ID: 2, Module: "user", NewAnswer: `{"userId":7}`, CreateTime: baseTime}, BillingIDs: []int32{}},
	}
	fillLogProjects(items, map[int32]logBillingProjectMeta{
		11: {Type: "normal_search", QuestionText: fmt.Sprintf("题目 %d", 11)},
	})

	if queries != 0 {
		t.Fatalf("project derivation must not touch the database, got %d queries", queries)
	}
	if items[0].Project != "搜索答案，题目：题目 11" {
		t.Fatalf("unexpected search project: %q", items[0].Project)
	}
	if items[1].Project != "用户 #7" {
		t.Fatalf("unexpected user project: %q", items[1].Project)
	}
}
