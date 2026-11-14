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
	group := router.Group("/api/admin/userexam")
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
			{http.MethodGet, "/api/admin/userexam/tool/version", nil},
			{http.MethodGet, "/api/admin/userexam/tool", nil},
			{http.MethodPost, "/api/admin/userexam/inspect", bytes.NewBufferString("")},
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
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/userexam/tool/version", nil))

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
	router.ServeHTTP(versionResponse, httptest.NewRequest(http.MethodGet, "/api/admin/userexam/tool/version", nil))
	var version userExamToolVersion
	if err := json.Unmarshal(versionResponse.Body.Bytes(), &version); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if !version.Available || version.SizeBytes != int64(len(payload)) || len(version.SHA256) != 64 {
		t.Fatalf("版本自描述不正确: %+v", version)
	}

	downloadResponse := httptest.NewRecorder()
	router.ServeHTTP(downloadResponse, httptest.NewRequest(http.MethodGet, "/api/admin/userexam/tool", nil))
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
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/userexam/tool", nil))
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
	request := httptest.NewRequest(http.MethodPost, "/api/admin/userexam/inspect", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestUserExamInspectRejectsMissingFile(t *testing.T) {
	router := newUserExamToolRouter(adminUser())
	request := httptest.NewRequest(http.MethodPost, "/api/admin/userexam/inspect", bytes.NewBufferString(""))
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
	group := router.Group("/api/admin/userexam")
	group.Use(Auth, middleware.RequireAdmin)
	group.GET("/tool/version", UserExamToolVersion)
	group.GET("/tool", UserExamToolDownload)
	group.POST("/inspect", middleware.BodyLimit(MaxUserExamInspectUploadBytes), UserExamInspect)

	cases := []struct {
		method, path string
		body         *bytes.Buffer
	}{
		{http.MethodGet, "/api/admin/userexam/tool/version", nil},
		{http.MethodGet, "/api/admin/userexam/tool", nil},
		{http.MethodPost, "/api/admin/userexam/inspect", bytes.NewBufferString("")},
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
	// Windows 上必须先关闭句柄，否则 TempDir 清理会失败。
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

// 下载文件名必须与构建脚本实际产出的文件名一致。
//
// 这条钉子守的是行为而不是字面量：spec 里 name 写成 userexam-crypto、下载头却写
// userexam_crypto，管理员保存下来的文件名对不上文档和 run.cmd，双击就找不到东西。
// 所以直接读 spec 的 name= 与 build-exe.ps1 里的 dist 路径，两边都得和常量相等。
func TestUserExamToolNameMatchesBuildArtifact(t *testing.T) {
	toolDir := filepath.Join("..", "..", "tools", "userexam-crypto")

	spec, err := os.ReadFile(filepath.Join(toolDir, "userexam_crypto.spec"))
	if err != nil {
		t.Fatalf("读取 spec 失败: %v", err)
	}
	specName := specExecutableName(t, string(spec))
	if specName+".exe" != userExamToolName {
		t.Fatalf("spec 的 EXE name=%q 与下发文件名 %q 不一致", specName, userExamToolName)
	}

	build, err := os.ReadFile(filepath.Join(toolDir, "build-exe.ps1"))
	if err != nil {
		t.Fatalf("读取 build-exe.ps1 失败: %v", err)
	}
	wantPath := `dist\` + userExamToolName
	if !strings.Contains(string(build), wantPath) {
		t.Fatalf("build-exe.ps1 未产出 %s（下发文件名会与构建产物不符）", wantPath)
	}
}

// specExecutableName 取 EXE(name="...") 里的名字；取不到就让用例失败。
func specExecutableName(t *testing.T, spec string) string {
	t.Helper()
	const marker = `name="`
	index := strings.Index(spec, marker)
	if index < 0 {
		t.Fatal("spec 里找不到 EXE(name=...)")
	}
	rest := spec[index+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("spec 的 name= 没有闭合引号")
	}
	return rest[:end]
}

// 自描述版本号必须与 Python 工具本身一致，否则管理台显示的版本是假的。
func TestUserExamToolVersionMatchesPythonTool(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "tools", "userexam-crypto", "userexam_crypto.py"))
	if err != nil {
		t.Fatalf("读取工具源码失败: %v", err)
	}
	if !strings.Contains(string(source), `TOOL_VERSION = "`+userExamToolVersionLabel+`"`) {
		t.Fatalf("工具源码的 TOOL_VERSION 与后端自描述 %q 不一致", userExamToolVersionLabel)
	}
}

// 路径 A 的硬边界：服务端不得持有解密能力。
//
// 一旦有人把口令或 SQLite3MC 绑定塞进服务端，这个工具就变成了远程解密服务，
// 上传的密文库会以明文落在服务器上。检查的是**能力**而不是出现过的字眼：
// 注释里提到 SQLite3MC 不算能力，能真正解密的依赖/运行库/密钥才算。
func TestUserExamServerHoldsNoCiphercapability(t *testing.T) {
	root := filepath.Join("..", "..")

	// 1) 依赖层：Go 模块图里不许出现 SQLite 加密绑定。
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		lower := strings.ToLower(string(content))
		for _, needle := range []string{"sqlite3mc", "wxsqlite3", "sqlcipher"} {
			if strings.Contains(lower, needle) {
				t.Fatalf("%s 引入了服务端不该有的加密依赖 %q", name, needle)
			}
		}
	}

	// 2) 运行库层：服务端源码树里不许出现 SQLite3MC 的原生扩展。
	// tools/userexam-crypto/_vendor 属于脱机工具（不进 exe 之外的分发），
	// 不是服务端构建输入，所以不在扫描范围。
	for _, directory := range []string{"internal", "pkg", "cmd"} {
		walkRoot := filepath.Join(root, directory)
		walkErr := filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			name := strings.ToLower(entry.Name())
			if strings.HasSuffix(name, ".pyd") || strings.HasSuffix(name, ".dll") || strings.Contains(name, "sqlite3mc") {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				t.Fatalf("服务端源码树里出现了原生加密运行库：%s", relative)
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("扫描 %s 的运行库失败: %v", directory, walkErr)
		}
	}

	// 3) 密钥层：历史口令的两种常见编码都不许出现在非测试 Go 源码里。
	// 这里只比对「明文口令的字节序列」，不写出可读形式，避免测试自己泄漏。
	forbiddenLiterals := []string{
		"mimayrlshenme",
		"6d696d6179726c7368656e6d65",
	}
	for _, directory := range []string{"internal", "pkg", "cmd"} {
		walkRoot := filepath.Join(root, directory)
		walkErr := filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lower := strings.ToLower(string(content))
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			for _, needle := range forbiddenLiterals {
				if strings.Contains(lower, needle) {
					t.Fatalf("%s 出现了服务端不该持有的口令字面量", relative)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("扫描 %s 失败: %v", directory, walkErr)
		}
	}
}

// 三个 userexam 路由的**源码级**鉴权断言。
//
// 为什么行为测试不够：TestUserExamToolEndpointsRejectNonAdmin 用的是本文件里
// 自建的 router（newUserExamToolRouter），中间件链是测试自己写的，不是
// internal/api/router.go 里的那一份；而三个 handler 内部又各有一道
// requireAdmin（纵深防御）。两条叠加的结果是——把 router.go 里三条
// middleware.RequireAdmin 全删掉，行为测试**依然全绿**（已实测确认）。
//
// 所以这里直接读 router.go 源码，断言「注册行里确实有 RequireAdmin，且顺序在
// handler 之前、在 controller.Auth 之后」。删掉任何一条中间件都会变红。
func TestUserExamRoutesMountRequireAdminInRouterSource(t *testing.T) {
	routerSource, err := os.ReadFile(filepath.Join("..", "api", "router.go"))
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	lines := strings.Split(string(routerSource), "\n")

	// 逐条路由断言：方法、路径、中间件顺序。
	cases := []struct {
		method string
		path   string
		handle string
	}{
		{http.MethodGet, "/admin/userexam/tool/version", "controller.UserExamToolVersion"},
		{http.MethodGet, "/admin/userexam/tool", "controller.UserExamToolDownload"},
		{http.MethodPost, "/admin/userexam/inspect", "controller.UserExamInspect"},
	}

	for _, testCase := range cases {
		marker := `"` + testCase.path + `"`
		var matched []string
		for _, line := range lines {
			if strings.Contains(line, testCase.method+"(") && strings.Contains(line, marker) {
				matched = append(matched, line)
			}
		}
		if len(matched) != 1 {
			t.Fatalf("%s %s 应恰好注册 1 条，实际 %d 条：%v",
				testCase.method, testCase.path, len(matched), matched)
		}
		line := matched[0]

		// ① 必须挂 controller.Auth
		if !strings.Contains(line, "controller.Auth") {
			t.Fatalf("%s 未挂 controller.Auth：%s", testCase.path, strings.TrimSpace(line))
		}
		// ② 必须挂 middleware.RequireAdmin
		authIndex := strings.Index(line, "middleware.RequireAdmin")
		if authIndex < 0 {
			t.Fatalf("%s 未挂 middleware.RequireAdmin：%s", testCase.path, strings.TrimSpace(line))
		}
		// ③ RequireAdmin 必须排在 handler 之前（否则中间件不生效，只是把符号写在后面）
		handlerIndex := strings.Index(line, testCase.handle)
		if handlerIndex < 0 {
			t.Fatalf("%s 未指向 %s：%s", testCase.path, testCase.handle, strings.TrimSpace(line))
		}
		if authIndex > handlerIndex {
			t.Fatalf("%s 的 RequireAdmin 排在 handler 之后，不会生效：%s", testCase.path, strings.TrimSpace(line))
		}
		// ④ Auth 必须在 RequireAdmin 之前：RequireAdmin 依赖 Auth 写入的 user
		if strings.Index(line, "controller.Auth") > authIndex {
			t.Fatalf("%s 的 controller.Auth 排在 RequireAdmin 之后，RequireAdmin 拿不到身份：%s",
				testCase.path, strings.TrimSpace(line))
		}
		// ⑤ 注册在 apiRouter 上（与同文件其它 /admin/* 一致）
		if !strings.Contains(line, "apiRouter.") {
			t.Fatalf("%s 未注册在 apiRouter 上：%s", testCase.path, strings.TrimSpace(line))
		}
	}
}
