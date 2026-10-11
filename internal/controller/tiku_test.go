package controller

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func newTikuControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:tiku-controller-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.Tiku{}, &entity.User{}, &entity.Log{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	dao.SetDefault(db)
	return db
}

func ensureTikuControllerSourceColumn(t *testing.T, db *gorm.DB) {
	t.Helper()

	if err := db.Exec("ALTER TABLE tiku ADD COLUMN source_name TEXT").Error; err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			t.Fatalf("add tiku source_name column: %v", err)
		}
	}
}

func seedTikuControllerUser(t *testing.T, db *gorm.DB, id int32, username string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       username,
		Password:       "123456",
		Role:           "admin",
		Status:         "enabled",
		RegisterSource: "system",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedTikuControllerQuestion(t *testing.T, db *gorm.DB, id int32, question string, sourceName string) {
	t.Helper()

	payloadOptions, _ := json.Marshal([]string{"A", "B"})
	payloadAnswer, _ := json.Marshal([]string{"答案"})
	row := entity.Tiku{
		ID:       id,
		Question: question,
		Type:     0,
		Options:  string(payloadOptions),
		Answer:   string(payloadAnswer),
		Plat:     1,
	}
	middleware.FillHash(&row)
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create tiku row: %v", err)
	}
	if err := db.Exec("UPDATE tiku SET source_name = ? WHERE id = ?", sourceName, id).Error; err != nil {
		t.Fatalf("update tiku source_name: %v", err)
	}
}

// cacheEntryCount 返回全局搜题结果缓存里的条目数（测试辅助）。
func cacheEntryCount() int {
	defaultSearchResultCache.mu.Lock()
	defer defaultSearchResultCache.mu.Unlock()
	return len(defaultSearchResultCache.entries)
}

// seedSearchResultCacheEntry 直接塞一条缓存，模拟「已经缓存过某个问题的答案」。
func seedSearchResultCacheEntry(t *testing.T, question string) {
	t.Helper()

	key, epoch := buildSearchResultCacheKey(101, model.SearchRequest{Question: question})
	defaultSearchResultCache.put(key, [][]string{{"旧答案"}}, epoch)
	if cacheEntryCount() == 0 {
		t.Fatal("expected the seed to write a cache entry")
	}
}

// 回归：题目被人工修改后，必须立刻作废搜题结果缓存。
//
// 历史实现只有 TTL 没有失效入口，改完答案后用户最长 10 分钟仍拿到旧答案。
func TestUpdateQuestionInvalidatesSearchResultCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")
	seedTikuControllerQuestion(t, db, 11, "待修正题目", "人工导入")
	seedSearchResultCacheEntry(t, "待修正题目")
	defer invalidateSearchResultCache("")

	payload := []byte(`{"question":"待修正题目","type":0,"options":"[\"A\",\"B\"]","answer":"[\"新答案\"]","plat":1}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPut, "/adapter-service/questions/11", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	// 路径参数需要显式注入，httptest 不会解析 :id。
	ctx.Params = gin.Params{{Key: "id", Value: "11"}}
	ctx.Set("user", &user)

	UpdateQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if got := cacheEntryCount(); got != 0 {
		t.Fatalf("expected the cache to be invalidated after editing a question, still %d entries", got)
	}
}

// 回归：题目被删除后同样必须作废缓存。
func TestDeleteQuestionInvalidatesSearchResultCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")
	seedTikuControllerQuestion(t, db, 12, "待删除题目", "人工导入")
	seedSearchResultCacheEntry(t, "待删除题目")
	defer invalidateSearchResultCache("")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/adapter-service/questions/12", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "12"}}
	ctx.Set("user", &user)

	DeleteQuestion(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if got := cacheEntryCount(); got != 0 {
		t.Fatalf("expected the cache to be invalidated after deleting a question, still %d entries", got)
	}
}

// 回归：新增题目后必须作废缓存。
func TestCreateQuestionInvalidatesSearchResultCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")
	seedSearchResultCacheEntry(t, "已有缓存题目")
	defer invalidateSearchResultCache("")

	payload := []byte(`[{"question":"新导入题目","type":0,"options":"[\"A\",\"B\"]","answer":"[\"答案\"]","plat":1}]`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", &user)

	CreateQuestion(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if got := cacheEntryCount(); got != 0 {
		t.Fatalf("expected the cache to be invalidated after creating a question, still %d entries", got)
	}
}

// dryRun 导入不写库，不应该白白清空缓存。
func TestImportQuestionsDryRunKeepsSearchResultCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	seedSearchResultCacheEntry(t, "已有缓存题目")
	defer invalidateSearchResultCache("")

	payload := []byte(`{"dryRun":true,"items":[{"question":"预演题目","type":0,"options":"[\"A\",\"B\"]","answer":"[\"答案\"]","plat":1}]}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request

	ImportQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if got := cacheEntryCount(); got == 0 {
		t.Fatal("expected a dry-run import to keep the cache")
	}
}

func TestCreateQuestionDefaultsSourceNameToManualImport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`[{"question":"人工导入题目","type":0,"options":"[\"A\",\"B\"]","answer":"[\"答案\"]","plat":1}]`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", &user)

	CreateQuestion(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var sourceName sql.NullString
	if err := db.Raw("SELECT source_name FROM tiku LIMIT 1").Row().Scan(&sourceName); err != nil {
		t.Fatalf("scan source_name: %v", err)
	}
	if strings.TrimSpace(sourceName.String) != "人工导入" {
		t.Fatalf("expected source_name 人工导入, got %q", sourceName.String)
	}
}

func TestGetQuestionsAuditLogStoresUsername(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	user := seedTikuControllerUser(t, db, 1, "topic-admin")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/adapter-service/questions/search",
		bytes.NewReader([]byte(`{"pageNo":0,"pageSize":11}`)),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &user)

	GetQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var entry entity.Log
	if err := db.Where("user_id = ? AND action = ?", user.ID, logActionSearch).First(&entry).Error; err != nil {
		t.Fatalf("load question query log: %v", err)
	}
	if entry.Username != "topic-admin" {
		t.Fatalf("expected username topic-admin, got %q", entry.Username)
	}
}

func TestImportQuestionsDryRunReturnsExistingIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")
	seedTikuControllerQuestion(t, db, 7, "已存在题目", "万能题库")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"dryRun":true,"items":[{"id":7,"question":"已存在题目","type":0,"options":"[\"A\"]","answer":"[\"答案\"]","plat":1},{"id":9,"question":"新增题目","type":0,"options":"[\"A\"]","answer":"[\"答案\"]","plat":1}]}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", &user)

	ImportQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"existingIds":[7]`) {
		t.Fatalf("expected existingIds [7], got %s", recorder.Body.String())
	}
}

func TestImportQuestionsAddsOnlyMissingRowsWhenOverwriteDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")
	seedTikuControllerQuestion(t, db, 7, "旧题目", "万能题库")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"overwrite":false,"items":[{"id":7,"question":"覆盖失败题目","type":0,"options":"[\"A\"]","answer":"[\"新答案\"]","plat":1,"source_name":"人工导入"},{"id":9,"question":"新增题目","type":0,"options":"[\"A\"]","answer":"[\"新增答案\"]","plat":1}]}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", &user)

	ImportQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var existingQuestion string
	if err := db.Raw("SELECT question FROM tiku WHERE id = ?", 7).Row().Scan(&existingQuestion); err != nil {
		t.Fatalf("scan existing question: %v", err)
	}
	if existingQuestion != "旧题目" {
		t.Fatalf("expected existing question to stay unchanged, got %q", existingQuestion)
	}

	var newQuestion string
	var newSourceName sql.NullString
	if err := db.Raw("SELECT question, source_name FROM tiku WHERE id = ?", 9).Row().Scan(&newQuestion, &newSourceName); err != nil {
		t.Fatalf("scan new question: %v", err)
	}
	if newQuestion != "新增题目" {
		t.Fatalf("expected new question 新增题目, got %q", newQuestion)
	}
	if strings.TrimSpace(newSourceName.String) != "人工导入" {
		t.Fatalf("expected new source_name 人工导入, got %q", newSourceName.String)
	}
}

func TestImportQuestionsOverwritesExistingRowsWhenRequested(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")
	seedTikuControllerQuestion(t, db, 7, "旧题目", "万能题库")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"overwrite":true,"items":[{"id":7,"question":"覆盖后的题目","type":0,"options":"[\"A\"]","answer":"[\"覆盖答案\"]","plat":1,"source_name":"人工导入"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", &user)

	ImportQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var question string
	var sourceName sql.NullString
	if err := db.Raw("SELECT question, source_name FROM tiku WHERE id = ?", 7).Row().Scan(&question, &sourceName); err != nil {
		t.Fatalf("scan overwritten row: %v", err)
	}
	if question != "覆盖后的题目" {
		t.Fatalf("expected overwritten question 覆盖后的题目, got %q", question)
	}
	if strings.TrimSpace(sourceName.String) != "人工导入" {
		t.Fatalf("expected overwritten source_name 人工导入, got %q", sourceName.String)
	}
}

func TestImportQuestionsNormalizesBlankSourceNameToManualImport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTikuControllerTestDB(t)
	ensureTikuControllerSourceColumn(t, db)
	user := seedTikuControllerUser(t, db, 1, "admin")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"items":[{"id":11,"question":"空来源题目","type":0,"options":"[\"A\"]","answer":"[\"答案\"]","plat":1,"source_name":"   "}]}`)
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/questions/import", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set("user", &user)

	ImportQuestions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var sourceName sql.NullString
	if err := db.Raw("SELECT source_name FROM tiku WHERE id = ?", 11).Row().Scan(&sourceName); err != nil {
		t.Fatalf("scan imported source_name: %v", err)
	}
	if strings.TrimSpace(sourceName.String) != "人工导入" {
		t.Fatalf("expected imported source_name 人工导入, got %q", sourceName.String)
	}
}
