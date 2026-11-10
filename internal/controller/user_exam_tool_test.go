package controller

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"

	_ "github.com/glebarez/go-sqlite"
)

// stubUser 把已认证用户塞进上下文，替代真实的 JWT 解析。
// 这样测试只验证「管理员门禁 + handler 行为」，不重复测登录链路。
func stubUser(user *entity.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user != nil {
			c.Set("user", user)
		}
		c.Next()
	}
}

func newUserExamToolRouter(user *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/adapter-service/admin/userexam")
	group.Use(stubUser(user), middleware.RequireAdmin)
	group.GET("/tool/version", UserExamToolVersion)
	group.GET("/tool", UserExamToolDownload)
	group.POST("/inspect", middleware.BodyLimit(MaxUserExamInspectUploadBytes), UserExamInspect)
	return router
}

func adminUser() *entity.User {
	return &entity.User{ID: 1, Username: "root", Role: "admin", Status: "enabled"}
}

func ordinaryUser() *entity.User {
	return &entity.User{ID: 2, Username: "someone", Role: "user", Status: "enabled"}
}

func TestUserExamToolEndpointsRejectNonAdmin(t *testing.T) {
	for _, user := range []*entity.User{ordinaryUser(), nil} {
		router := newUserExamToolRouter(user)
		cases := []struct {
			method, path string
			body         *bytes.Buffer
		}{
			{http.MethodGet, "/adapter-service/admin/userexam/tool/version", nil},
			{http.MethodGet, "/adapter-service/admin/userexam/tool", nil},
			{http.MethodPost, "/adapter-service/admin/userexam/inspect", bytes.NewBufferString("")},
		}
		for _, testCase := range cases {
			var request *http.Request
			if testCase.body != nil {
				request = httptest.NewRequest(testCase.method, testCase.path, testCase.body)
			} else {
				request = httptest.NewRequest(testCase.method, testCase.path, nil)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s %s: non-admin must get 403, got %d", testCase.method, testCase.path, response.Code)
			}
		}
	}
}

func TestUserExamToolVersionReportsUnavailableWithoutDeployment(t *testing.T) {
	original := userExamToolPath
	userExamToolPath = func() string { return "" }
	t.Cleanup(func() { userExamToolPath = original })

	router := newUserExamToolRouter(adminUser())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/adapter-service/admin/userexam/tool/version", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", response.Code)
	}
	var payload userExamToolVersion
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Available {
		t.Fatal("未部署时 available 必须是 false")
	}
	if payload.Reason == "" {
		t.Fatal("未部署时必须给出原因")
	}
	if payload.Cipher != userExamToolCipher || payload.KDFIter != userExamToolKDFIter {
		t.Fatalf("加密参数自描述缺失: %+v", payload)
	}
}

func TestUserExamToolDownloadServesConfiguredBinary(t *testing.T) {
	directory := t.TempDir()
	toolPath := filepath.Join(directory, "userexam_crypto.exe")
	payload := []byte("MZ fake portable executable payload")
	if err := os.WriteFile(toolPath, payload, 0o600); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	original := userExamToolPath
	userExamToolPath = func() string { return toolPath }
	t.Cleanup(func() { userExamToolPath = original })

	router := newUserExamToolRouter(adminUser())

	versionResponse := httptest.NewRecorder()
	router.ServeHTTP(versionResponse, httptest.NewRequest(http.MethodGet, "/adapter-service/admin/userexam/tool/version", nil))
	var version userExamToolVersion
	if err := json.Unmarshal(versionResponse.Body.Bytes(), &version); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if !version.Available || version.SizeBytes != int64(len(payload)) || len(version.SHA256) != 64 {
		t.Fatalf("版本自描述不正确: %+v", version)
	}

	downloadResponse := httptest.NewRecorder()
	router.ServeHTTP(downloadResponse, httptest.NewRequest(http.MethodGet, "/adapter-service/admin/userexam/tool", nil))
	if downloadResponse.Code != http.StatusOK {
		t.Fatalf("下载失败: %d", downloadResponse.Code)
	}
	if !bytes.Equal(downloadResponse.Body.Bytes(), payload) {
		t.Fatal("下载内容与工具本体不一致")
	}
	if got := downloadResponse.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("下载响应必须带 Content-Disposition")
	}
}

func TestUserExamToolDownloadUnavailableWhenNotDeployed(t *testing.T) {
	original := userExamToolPath
	userExamToolPath = func() string { return "" }
	t.Cleanup(func() { userExamToolPath = original })

	router := newUserExamToolRouter(adminUser())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/adapter-service/admin/userexam/tool", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("未部署时应返回 503，实际 %d", response.Code)
	}
}

func buildInspectRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/admin/userexam/inspect", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestUserExamInspectRejectsMissingFile(t *testing.T) {
	router := newUserExamToolRouter(adminUser())
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/admin/userexam/inspect", bytes.NewBufferString(""))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=none")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("缺少文件应返回 400，实际 %d", response.Code)
	}
}

func TestUserExamInspectRejectsEncryptedUpload(t *testing.T) {
	router := newUserExamToolRouter(adminUser())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", []byte("SQLite format 4\x00ciphertext")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("加密库应返回 400，实际 %d", response.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["message"] == "" {
		t.Fatal("拒绝时必须给出可读原因")
	}
	// 加密库必须给出「先用脱机工具解密」的可操作指引，而不是笼统失败。
	if !strings.Contains(payload["message"], "脱机工具") {
		t.Fatalf("加密库的拒绝原因应指向脱机工具，实际 %q", payload["message"])
	}
}

// 三个接口都挂在 controller.Auth 之后：没有凭证时必须是 401，而不是 403。
// 这里用的是真实 Auth 中间件（空 token 在验签前就失败，不触达数据库），
// 与上面用 stubUser 覆盖的 403 用例互补。
func TestUserExamToolEndpointsRejectUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/adapter-service/admin/userexam")
	group.Use(Auth, middleware.RequireAdmin)
	group.GET("/tool/version", UserExamToolVersion)
	group.GET("/tool", UserExamToolDownload)
	group.POST("/inspect", middleware.BodyLimit(MaxUserExamInspectUploadBytes), UserExamInspect)

	cases := []struct {
		method, path string
		body         *bytes.Buffer
	}{
		{http.MethodGet, "/adapter-service/admin/userexam/tool/version", nil},
		{http.MethodGet, "/adapter-service/admin/userexam/tool", nil},
		{http.MethodPost, "/adapter-service/admin/userexam/inspect", bytes.NewBufferString("")},
	}
	for _, testCase := range cases {
		var request *http.Request
		if testCase.body != nil {
			request = httptest.NewRequest(testCase.method, testCase.path, testCase.body)
		} else {
			request = httptest.NewRequest(testCase.method, testCase.path, nil)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: 未登录必须 401，实际 %d", testCase.method, testCase.path, response.Code)
		}
		var payload map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s %s: decode: %v", testCase.method, testCase.path, err)
		}
		if payload["message"] == "" {
			t.Fatalf("%s %s: 401 必须给出可读原因", testCase.method, testCase.path)
		}
	}
}

// writePlaintextUserExamFixture 造一个与考试客户端同构的明文库。
// 这是 HTTP 层的正向用例：管理员上传已解密的库，应拿到表结构与行数。
func writePlaintextUserExamFixture(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "UserExamInfo.decrypted.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	statements := []string{
		`CREATE TABLE baseInfo(Token TEXT, schoolId TEXT, schoolName TEXT, thirdFild INTEGER)`,
		`CREATE TABLE macAddress(Address TEXT, Flag Text)`,
		`INSERT INTO baseInfo VALUES('84261c02fd12a91ddb9f6f85abd7d5eb','196586','某某工程大学自考',0)`,
		`INSERT INTO macAddress VALUES('','wsh')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
	// Windows 上必须先关闭句柄，否则 TempDir 清理会失败（AGENTS.md 第 9 条）。
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return content
}

func TestUserExamInspectReturnsTableMetadataForPlaintextUpload(t *testing.T) {
	router := newUserExamToolRouter(adminUser())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.decrypted.db", writePlaintextUserExamFixture(t)))
	if response.Code != http.StatusOK {
		t.Fatalf("明文库应返回 200，实际 %d: %s", response.Code, response.Body.String())
	}

	var payload struct {
		Tables map[string]struct {
			Columns []string         `json:"columns"`
			Rows    []map[string]any `json:"rows"`
		} `json:"tables"`
		OtherTables []string       `json:"otherTables"`
		RowCounts   map[string]int `json:"rowCounts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Tables) != 2 {
		t.Fatalf("应只解析 baseInfo 与 macAddress，实际 %v", payload.Tables)
	}
	baseInfo, ok := payload.Tables["baseInfo"]
	if !ok {
		t.Fatal("baseInfo 缺失")
	}
	if len(baseInfo.Columns) != 4 {
		t.Fatalf("baseInfo 列数应为 4，实际 %v", baseInfo.Columns)
	}
	if len(baseInfo.Rows) != 1 {
		t.Fatalf("baseInfo 行数应为 1，实际 %d", len(baseInfo.Rows))
	}
	if got := baseInfo.Rows[0]["schoolId"]; got != "196586" {
		t.Fatalf("schoolId 应为 196586，实际 %v", got)
	}
	if payload.RowCounts["baseInfo"] != 1 || payload.RowCounts["macAddress"] != 1 {
		t.Fatalf("rowCounts 不正确: %v", payload.RowCounts)
	}
	if len(payload.OtherTables) != 0 {
		t.Fatalf("不应有其它表，实际 %v", payload.OtherTables)
	}
}

// 非 SQLite 文件必须是可读错误，不能让 handler 崩掉。
func TestUserExamInspectRejectsNonSQLiteUpload(t *testing.T) {
	router := newUserExamToolRouter(adminUser())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "not-a-database.db", []byte("this is not a database at all")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("非 SQLite 文件应返回 400，实际 %d", response.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["message"] == "" {
		t.Fatal("拒绝时必须给出可读原因")
	}
}
