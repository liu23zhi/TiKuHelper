package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

// enableSensitiveWordCheck 打开开关并写入一个形态合法的 UAPI 密钥。
//
// 密钥是 synthetic 占位值：真正的外呼被 service.SetSensitiveWordCheckURLForTesting
// 指向本地 httptest，不会打到 uapis.cn。
func enableSensitiveWordCheck(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 计费需要一条渠道配置，否则落账会在 resolveCompletedSearchBillingCharges 报 record not found；
	// 那是既有行为，与本功能无关，这里只为让测试专注于答题链路本身。
	if err := db.Where("provider = ?", "wanneng").First(&entity.SearchAccountConfig{}).Error; err != nil {
		if err := db.Create(&entity.SearchAccountConfig{
			Provider: "wanneng", TokenKey: "wannengToken", TokenValue: "stub", Enabled: true, FlatPrice: 0,
		}).Error; err != nil {
			t.Fatalf("seed search account config: %v", err)
		}
	}
	for _, setting := range []entity.SystemSetting{
		{Key: service.SensitiveWordCheckEnabledSettingKey, Value: "true"},
		{Key: service.UAPIAPIKeySettingKey, Value: "uapi-synthetic-sensitive-word-only"},
	} {
		entry := setting
		if err := db.Where("key = ?", entry.Key).Assign(entity.SystemSetting{Value: entry.Value}).
			FirstOrCreate(&entry).Error; err != nil {
			t.Fatalf("seed setting %s: %v", entry.Key, err)
		}
	}
}

// seedSensitiveLogColumns 用显式 ALTER 补上 logs 的四列。
//
// 生产路径由 internal/registry 的 ensureSensitiveWordSchema 完成；测试库只跑了
// AutoMigrate(Log)，而 logs.gen.go 是生成文件、不含这四列，所以这里手动补。
func seedSensitiveLogColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, column := range []struct{ name, ddl string }{
		{"sensitive_flag", "ALTER TABLE logs ADD COLUMN sensitive_flag boolean NOT NULL DEFAULT 0"},
		{"sensitive_words", "ALTER TABLE logs ADD COLUMN sensitive_words text"},
		{"sensitive_checked_at", "ALTER TABLE logs ADD COLUMN sensitive_checked_at datetime"},
		{"sensitive_check_error", "ALTER TABLE logs ADD COLUMN sensitive_check_error varchar(255)"},
	} {
		// 先查后建：同一进程里 `file:...?mode=memory&cache=shared` 的同名库共享，
		// 子测试会重复执行到这里。
		if db.Migrator().HasColumn("logs", column.name) {
			continue
		}
		if err := db.Exec(column.ddl).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				continue
			}
			t.Fatalf("add sensitive column %s: %v", column.name, err)
		}
	}
}

// sensitiveStubServer 起一个可控的检测端点。
//
// delay 用来模拟「上游超时 5 秒」；status 用来模拟 500。
// calls 计数用于证明「开关关闭时零外呼」与「一次请求只检测两次（题目 + 答案）」。
func sensitiveStubServer(t *testing.T, delay time.Duration, status int, body string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func sensitiveSeed(t *testing.T, db *gorm.DB, id int32, username string) (entity.User, entity.AccessToken) {
	t.Helper()
	user := seedControllerUser(t, db, id, username, "user")
	token := seedControllerTokenForUser(t, db, user, "token-"+username, 0)
	return user, token
}

// TestSensitiveWordCheckDoesNotBlockAnswerResponse 是本功能最重要的一条：
// 上游检测卡住时，答题响应不能跟着等。
//
// 手法：用**闸门**代替真实的 5 秒延迟 —— 检测端点一直挂着直到测试放行，
// 于是「答题已经返回」这件事和「检测还没完成」可以同时被观察到，
// 而不必真的睡 5 秒（既快又更严格：串行实现在这里会直接死锁）。
func TestSensitiveWordCheckDoesNotBlockAnswerResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, db)
	enableSensitiveWordCheck(t, db)

	var calls int32
	gate := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"forbidden","original_text":"x","masked_text":"**","forbidden_words":["敏感"]}`))
	}))
	t.Cleanup(server.Close)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()

	user, token := sensitiveSeed(t, db, 401, "sensitive-gated-user")

	var answerBody []byte
	returnedBeforeDetectionFinished := false
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"wanneng"},
				Tokens:                 map[string]string{"wanneng": "stub-token"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              "req-sensitive-gated",
				AccessTokenCtx:         &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"A"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1",
				bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request

			started := time.Now()
			Search(ctx)
			elapsed := time.Since(started)
			answerBody = append([]byte(nil), recorder.Body.Bytes()...)

			if elapsed > 2*time.Second {
				t.Fatalf("答题响应被检测阻塞：耗时 %v", elapsed)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("答题必须成功：status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})

	// 此刻检测端点仍卡在闸门后（一次都没放行），而答题已经返回 ——
	// 这就是「旁路而非串行」的直接证据：串行实现在上面的 Search 里就会死锁。
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	returnedBeforeDetectionFinished = atomic.LoadInt32(&calls) >= 1
	if !returnedBeforeDetectionFinished {
		t.Fatalf("检测必须已经发起（卡在上游闸门后），实际 %d 次", atomic.LoadInt32(&calls))
	}

	// 放行检测，确认它最终真的跑完并写回标记。
	close(gate)
	if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
		t.Fatalf("等待旁路任务结束失败：%v", err)
	}
	// 题目 + 答案，各一次外呼（两次都被闸门拦住后又一起放行）。
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("题目与答案必须各检测一次，实际 %d 次", got)
	}

	var entry entity.Log
	if err := db.Where("module = ? AND description LIKE ?", "search", "%req-sensitive-gated%").
		Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load audit log: %v", err)
	}
	preview, err := loadSensitiveWordPreview(db, entry.ID)
	if err != nil {
		t.Fatalf("load sensitive preview: %v", err)
	}
	if !preview.Flag {
		t.Fatalf("命中敏感词时必须打上标记：%+v", preview)
	}
	if !strings.Contains(preview.Words, "answer") {
		t.Fatalf("标记明细必须按 scope 分组：%q", preview.Words)
	}

	// 最关键的证据：答案原文仍在响应里，没有被屏蔽、没有被替换。
	var resp model.SearchResponse
	if err := json.Unmarshal(answerBody, &resp); err != nil {
		t.Fatalf("响应体必须是合法 JSON：%v", err)
	}
	if strings.Join(resp.Answer.BestAnswer, ",") != "A" {
		t.Fatalf("答案必须原样返回：%+v", resp.Answer)
	}
}

// TestSensitiveWordCheckFailureDoesNotFailAnswer：上游 500 时答题照常成功且不误标。
func TestSensitiveWordCheckFailureDoesNotFailAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, db)
	enableSensitiveWordCheck(t, db)

	server, calls := sensitiveStubServer(t, 0, http.StatusInternalServerError, `{"message":"boom"}`)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()

	user, token := sensitiveSeed(t, db, 411, "sensitive-500-user")

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"wanneng"},
				Tokens:                 map[string]string{"wanneng": "stub-token"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              "req-sensitive-500",
				AccessTokenCtx:         &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"A"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1",
				bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request
			Search(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("检测失败绝不能让答题失败：status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var resp model.SearchResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
				t.Fatalf("响应体必须是合法 JSON：%v", err)
			}
			if len(resp.Answer.BestAnswer) == 0 {
				t.Fatalf("答案必须照常返回：%+v", resp)
			}
			if resp.Error != nil {
				t.Fatalf("检测失败不得污染响应 error 字段：%+v", resp.Error)
			}
		})

	if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
		t.Fatalf("等待旁路任务结束失败：%v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("题目与答案必须各尝试一次，实际 %d 次", got)
	}

	var entry entity.Log
	if err := db.Where("module = ? AND description LIKE ?", "search", "%req-sensitive-500%").
		Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load audit log: %v", err)
	}
	preview, err := loadSensitiveWordPreview(db, entry.ID)
	if err != nil {
		t.Fatalf("load sensitive preview: %v", err)
	}
	if preview.Flag {
		t.Fatalf("检测失败不得误标为命中：%+v", preview)
	}
	if !strings.Contains(preview.Error, "500") {
		t.Fatalf("必须记录失败原因供排障：%q", preview.Error)
	}
}

// TestSensitiveWordCheckDisabledMakesNoExternalCall：开关关闭时零外呼、零字段写入。
func TestSensitiveWordCheckDisabledMakesNoExternalCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, db)
	// 只配密钥、不开开关：这正是「默认关闭」的语义。
	if err := db.Create(&entity.SystemSetting{Key: service.UAPIAPIKeySettingKey, Value: "uapi-synthetic"}).Error; err != nil {
		t.Fatalf("seed key: %v", err)
	}

	server, calls := sensitiveStubServer(t, 0, http.StatusOK,
		`{"status":"forbidden","forbidden_words":["敏感"]}`)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()

	user, token := sensitiveSeed(t, db, 421, "sensitive-off-user")

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"wanneng"},
				Tokens:                 map[string]string{"wanneng": "stub-token"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              "req-sensitive-off",
				AccessTokenCtx:         &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"A"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1",
				bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request
			Search(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("答题必须成功：%d", recorder.Code)
			}
		})

	if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
		t.Fatalf("等待旁路任务结束失败：%v", err)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("开关关闭时绝不能外呼，实际 %d 次", got)
	}

	var entry entity.Log
	if err := db.Where("module = ? AND description LIKE ?", "search", "%req-sensitive-off%").
		Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load audit log: %v", err)
	}
	var row struct {
		Flag      *bool
		CheckedAt *time.Time
	}
	if err := db.Raw("SELECT sensitive_flag AS flag, sensitive_checked_at AS checked_at FROM logs WHERE id = ?", entry.ID).
		Scan(&row).Error; err != nil {
		t.Fatalf("read sensitive columns: %v", err)
	}
	if row.CheckedAt != nil {
		t.Fatalf("开关关闭时不得写检测时间：%+v", row)
	}
	preview, err := loadSensitiveWordPreview(db, entry.ID)
	if err != nil {
		t.Fatalf("load sensitive preview: %v", err)
	}
	if preview.Flag || strings.TrimSpace(preview.Words) != "" || strings.TrimSpace(preview.Error) != "" {
		t.Fatalf("开关关闭时不得写任何 sensitive 字段：%+v", preview)
	}
}

// TestSensitiveWordCheckKeepsAnswerResponseShapeIdentical 是最关键的对比测试：
// 开检测与关检测，答题响应的 JSON 结构必须完全一致。
func TestSensitiveWordCheckKeepsAnswerResponseShapeIdentical(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 关检测
	offDB := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, offDB)
	offUser, offToken := sensitiveSeed(t, offDB, 431, "shape-off-user")
	offBody := runShapeSearch(t, offDB, offUser, offToken, "req-shape-off")

	// 开检测（上游命中敏感词，确保走了「打标」路径）
	onDB := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, onDB)
	enableSensitiveWordCheck(t, onDB)
	server, _ := sensitiveStubServer(t, 0, http.StatusOK,
		`{"status":"forbidden","original_text":"题目","masked_text":"**","forbidden_words":["敏感"]}`)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()
	onUser, onToken := sensitiveSeed(t, onDB, 432, "shape-on-user")
	onBody := runShapeSearch(t, onDB, onUser, onToken, "req-shape-on")
	if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
		t.Fatalf("等待旁路任务结束失败：%v", err)
	}

	var offJSON, onJSON map[string]json.RawMessage
	if err := json.Unmarshal(offBody, &offJSON); err != nil {
		t.Fatalf("关检测响应不是合法 JSON：%v (%s)", err, offBody)
	}
	if err := json.Unmarshal(onBody, &onJSON); err != nil {
		t.Fatalf("开检测响应不是合法 JSON：%v (%s)", err, onBody)
	}

	offKeys := sortedJSONKeys(offJSON)
	onKeys := sortedJSONKeys(onJSON)
	if strings.Join(offKeys, ",") != strings.Join(onKeys, ",") {
		t.Fatalf("开/关检测的顶层字段必须完全一致：off=%v on=%v", offKeys, onKeys)
	}

	// 逐字段结构比对（忽略随请求变化的值：requestId 不在这里，响应体本身没有它）。
	if !bytes.Equal(offBody, onBody) {
		t.Fatalf("开检测改变了答题响应体：\n off=%s\n on =%s", offBody, onBody)
	}

	var offAnswer, onAnswer map[string]json.RawMessage
	if err := json.Unmarshal(offJSON["answer"], &offAnswer); err != nil {
		t.Fatalf("answer 不是对象：%v", err)
	}
	if err := json.Unmarshal(onJSON["answer"], &onAnswer); err != nil {
		t.Fatalf("answer 不是对象：%v", err)
	}
	if strings.Join(sortedJSONKeys(offAnswer), ",") != strings.Join(sortedJSONKeys(onAnswer), ",") {
		t.Fatalf("answer 字段集合必须一致：off=%v on=%v", sortedJSONKeys(offAnswer), sortedJSONKeys(onAnswer))
	}
}

func runShapeSearch(t *testing.T, db *gorm.DB, user entity.User, token entity.AccessToken, requestID string) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var body []byte
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				EnabledSearchProviders: []string{"wanneng"},
				Tokens:                 map[string]string{"wanneng": "stub-token"},
				UserID:                 user.ID,
				AccessTokenID:          token.ID,
				RequestID:              requestID,
				AccessTokenCtx:         &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"A"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1",
				bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
			request.Header.Set("Content-Type", "application/json")
			ctx.Request = request
			Search(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("答题必须成功：status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			body = append([]byte(nil), recorder.Body.Bytes()...)
		})
	return body
}

func sortedJSONKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	// 稳定输出，便于失败时对比。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// TestBuildSensitiveWordAnswerTextCoversReturnedAnswerOnly 覆盖「检测对象」的选取。
func TestBuildSensitiveWordAnswerTextCoversReturnedAnswerOnly(t *testing.T) {
	resp := model.SearchResponse{
		Answer: model.Answer{
			AnswerKeyText: "AB",
			AnswerText:    "答案一#答案二",
			BestAnswer:    []string{"答案一", "答案二"},
			// allAnswer 里是被聚合淘汰的候选，不该参与送检。
			AllAnswer: [][]string{{"答案一", "答案二"}, {"被淘汰的候选"}},
		},
	}
	text := buildSensitiveWordAnswerText(resp)
	for _, expected := range []string{"AB", "答案一", "答案二"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("送检文本必须覆盖返回的答案 %q：%q", expected, text)
		}
	}
	if strings.Contains(text, "被淘汰的候选") {
		t.Fatalf("被聚合淘汰的候选答案不该送检：%q", text)
	}
}

// TestStartSensitiveWordCheckWithoutAuditLogSkipsWrite 证明「审计行拿不到就放弃回写」，
// 而不是一直挂着等。
func TestStartSensitiveWordCheckWithoutAuditLogSkipsWrite(t *testing.T) {
	db := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, db)
	enableSensitiveWordCheck(t, db)
	server, calls := sensitiveStubServer(t, 0, http.StatusOK, `{"status":"clean","forbidden_words":[]}`)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()

	task := startSensitiveWordCheck(resolvedSearchRequestContext{RequestID: "req-no-log"},
		model.SearchRequest{Question: "题目"}, model.SearchResponse{Question: "题目"})
	task.attachLogID(0)
	if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
		t.Fatalf("等待旁路任务结束失败：%v", err)
	}
	if got := atomic.LoadInt32(calls); got == 0 {
		t.Fatalf("检测应当已经发起")
	}
	var count int64
	if err := db.Model(&entity.LogSensitive{}).Where("sensitive_checked_at IS NOT NULL").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("没有审计行时不得写任何行，实际 %d", count)
	}
}

// TestLogListExposesSensitiveFields 覆盖列表接口的新增字段（前端与 chaoxing 面板读这些）。
func TestLogListExposesSensitiveFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	admin := &entity.User{ID: 501, Username: "admin-sensitive", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	checkedAt := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	hit := entity.Log{
		UserID: admin.ID, Username: admin.Username, Module: "search", Action: 3,
		Description: "搜索完成 requestId=req-hit", NewAnswer: `{"answer":"敏感答案"}`,
		CreateTime: checkedAt,
	}
	if err := db.Create(&hit).Error; err != nil {
		t.Fatalf("seed hit log: %v", err)
	}
	clean := entity.Log{
		UserID: admin.ID, Username: admin.Username, Module: "search", Action: 3,
		Description: "搜索完成 requestId=req-clean", NewAnswer: `{"answer":"干净答案"}`,
		CreateTime: checkedAt,
	}
	if err := db.Create(&clean).Error; err != nil {
		t.Fatalf("seed clean log: %v", err)
	}

	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		true, `[{"scope":"answer","status":"forbidden","forbidden":true,"words":["敏感"]}]`, checkedAt, hit.ID).Error; err != nil {
		t.Fatalf("mark hit log: %v", err)
	}
	// clean 行检测通过：flag=false + checked_at 有值。
	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		false, `[{"scope":"answer","status":"clean","forbidden":false,"words":[]}]`, checkedAt, clean.ID).Error; err != nil {
		t.Fatalf("mark clean log: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
	ctx.Set("user", admin)
	LogList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data []struct {
			ID                  int32      `json:"id"`
			NewAnswer           string     `json:"newAnswer"`
			SensitiveFlag       *bool      `json:"sensitiveFlag"`
			SensitiveWords      string     `json:"sensitiveWords"`
			SensitiveCheckedAt  *time.Time `json:"sensitiveCheckedAt"`
			SensitiveCheckError string     `json:"sensitiveCheckError"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := make(map[int32]int)
	for i, row := range payload.Data {
		byID[row.ID] = i
	}

	hitRow := payload.Data[byID[hit.ID]]
	if hitRow.SensitiveFlag == nil || !*hitRow.SensitiveFlag {
		t.Fatalf("命中行必须下发 sensitiveFlag=true：%+v", hitRow)
	}
	if !strings.Contains(hitRow.SensitiveWords, "answer") {
		t.Fatalf("命中行必须下发分组明细：%q", hitRow.SensitiveWords)
	}
	if hitRow.SensitiveCheckedAt == nil {
		t.Fatalf("命中行必须下发检测时间")
	}
	// 原有字段必须原样保留：只新增，不改语义。
	if hitRow.NewAnswer != `{"answer":"敏感答案"}` {
		t.Fatalf("答案原文被改写：%q", hitRow.NewAnswer)
	}

	cleanRow := payload.Data[byID[clean.ID]]
	if cleanRow.SensitiveFlag == nil || *cleanRow.SensitiveFlag {
		t.Fatalf("检测通过的行必须下发 false 而不是省略：%+v", cleanRow)
	}

	// 从未检测的行：一个 sensitive 字段都不该出现（前端据此显示「未检测」）。
	untested := entity.Log{UserID: admin.ID, Username: admin.Username, Module: "auth", Action: 10,
		Description: "用户登录成功", CreateTime: checkedAt}
	if err := db.Create(&untested).Error; err != nil {
		t.Fatalf("seed untested log: %v", err)
	}
	recorder2 := httptest.NewRecorder()
	ctx2, _ := gin.CreateTestContext(recorder2)
	ctx2.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
	ctx2.Set("user", admin)
	LogList(ctx2)
	var payload2 struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder2.Body.Bytes(), &payload2); err != nil {
		t.Fatalf("decode2: %v", err)
	}
	for _, row := range payload2.Data {
		if id, _ := row["id"].(float64); int32(id) != untested.ID {
			continue
		}
		for _, key := range []string{"sensitiveFlag", "sensitiveWords", "sensitiveCheckedAt", "sensitiveCheckError"} {
			if _, exists := row[key]; exists {
				t.Fatalf("从未检测的行不得下发 %s：%+v", key, row)
			}
		}
	}
}

// TestAttachSensitiveWordFieldsUsesSingleBatchQuery 守住「一次 IN 查询」的性能契约。
func TestAttachSensitiveWordFieldsUsesSingleBatchQuery(t *testing.T) {
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	logs := make([]*entity.Log, 0, 5)
	for i := 0; i < 5; i++ {
		entry := &entity.Log{Module: "search", Action: 3, Description: fmt.Sprintf("搜索完成 %d", i), CreateTime: time.Now()}
		if err := db.Create(entry).Error; err != nil {
			t.Fatalf("seed log: %v", err)
		}
		logs = append(logs, entry)
	}
	items := make([]logListItem, len(logs))
	for i := range logs {
		items[i] = logListItem{Log: logs[i], BillingIDs: []int32{}}
	}

	var queries int32
	counted := db.Session(&gorm.Session{})
	if err := counted.Callback().Query().Before("gorm:query").Register("test:count_sensitive", func(*gorm.DB) {
		atomic.AddInt32(&queries, 1)
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	attachSensitiveWordFields(counted, logs, items)

	if got := atomic.LoadInt32(&queries); got != 1 {
		t.Fatalf("必须只查一次（行数不放大查询次数），实际 %d 次", got)
	}
}

// TestSensitiveWordCheckReusesCacheAcrossRequests 是这次需求的**集成证据**：
// 同一道题被两个不同请求搜到，上游只该被外呼一次（题目 + 答案各一次），
// 第二次完全命中缓存。
//
// 为什么必须在 controller 层测：缓存是否接通取决于 startSensitiveWordCheck
// 有没有把 db 传进 service。漏传不会报错，只会静默退化回「每次都外呼」——
// 单测（service 层直接给 db）看不出来，只有这里能守住。
func TestSensitiveWordCheckReusesCacheAcrossRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, db)
	enableSensitiveWordCheck(t, db)

	server, calls := sensitiveStubServer(t, 0, http.StatusOK,
		`{"status":"forbidden","forbidden_words":["敏感"]}`)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()

	user, token := sensitiveSeed(t, db, 441, "sensitive-cache-user")

	// 两个请求的题目与答案完全相同（同一道题被搜两次）。
	for _, requestID := range []string{"req-cache-first", "req-cache-second"} {
		id := requestID
		withStubSearchDependencies(t,
			(&stubSearchClient{}).SearchAnswer,
			func(string, string, url.Values) (resolvedSearchRequestContext, error) {
				return resolvedSearchRequestContext{
					SearchMode:             searchModeNormal,
					EnabledSearchProviders: []string{"wanneng"},
					Tokens:                 map[string]string{"wanneng": "stub-token"},
					UserID:                 user.ID,
					AccessTokenID:          token.ID,
					RequestID:              id,
					AccessTokenCtx:         &service.AccessTokenAuthContext{User: user, AccessToken: token},
				}, nil
			},
			func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
				return []func(model.SearchRequest) ([][]string, error){
					(&stubSearchClient{enable: true, answer: [][]string{{"A"}}}).SearchAnswer,
				}
			},
			nil,
			func() {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				request := httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1",
					bytes.NewReader([]byte(`{"question":"题目","options":["A","B"]}`)))
				request.Header.Set("Content-Type", "application/json")
				ctx.Request = request
				Search(ctx)
				if recorder.Code != http.StatusOK {
					t.Fatalf("答题必须成功：status=%d body=%s", recorder.Code, recorder.Body.String())
				}
			})
		if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
			t.Fatalf("等待旁路任务结束失败：%v", err)
		}
	}

	// 两个请求各含题目 + 答案，但文本只有两段 → 只该外呼两次。
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("同一道题被搜两次时上游只该外呼 2 次（题目+答案），实际 %d 次", got)
	}
	// 两条审计行都要打上标记：缓存命中不能丢掉命中结论。
	for _, requestID := range []string{"req-cache-first", "req-cache-second"} {
		var entry entity.Log
		if err := db.Where("module = ? AND description LIKE ?", "search", "%"+requestID+"%").
			Order("id DESC").First(&entry).Error; err != nil {
			t.Fatalf("load audit log %s: %v", requestID, err)
		}
		preview, err := loadSensitiveWordPreview(db, entry.ID)
		if err != nil {
			t.Fatalf("load sensitive preview %s: %v", requestID, err)
		}
		if !preview.Flag {
			t.Fatalf("%s 命中敏感词却未打标（缓存命中丢结论）：%+v", requestID, preview)
		}
	}
}
