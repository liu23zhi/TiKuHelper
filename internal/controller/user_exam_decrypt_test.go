package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本文件覆盖 2026-11-14 新增的「服务器解密加密库」链路在 controller 层的行为：
//  1. 加密库能上传 → 自动解密 → 巡检成功（核心需求）；
//  2. 四种错误必须**各自可区分**（不是数据库 / 密钥不对 / 密钥未配置 / 算法不支持）；
//  3. 后台未配密钥 → 用默认密钥仍能解；
//  4. 后台配了自定义密钥 → 用自定义的；
//  5. 明文库仍能直接巡检（**不能破坏现有能力**）；
//  6. 临时文件在请求结束后被删除（防泄漏）；
//  7. 密钥接口不回显密钥。

const (
	// 与 service 层测试夹具一致：默认密钥加密的库。
	ueDefaultFixture = "../service/testdata/default_key.enc.db"
	ueCustomFixture  = "../service/testdata/custom_key.enc.db"
	uePlainFixture   = "../service/testdata/default_key.plain.db"
	// 夹具使用的自定义密钥（假密钥，仅测试用）。
	ueCustomKey = "MY-CUSTOM-KEY-9f3a"
)

func ueRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取夹具 %s 失败: %v", path, err)
	}
	return content
}

// ueTestDB 建一个只有 system_settings 的测试库（密钥读写要用）。
func ueTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:userexam-ctl-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// AutoMigrate 逐个调用（一次传多个模型会合并建表语句 —— 已知事故）。
	for _, model := range []any{&entity.SystemSetting{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	return db
}

// installUserExamToolDB 把 controller 的 DB 钩子指向测试库。
func installUserExamToolDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := userExamToolDB
	userExamToolDB = func() *gorm.DB { return db }
	t.Cleanup(func() { userExamToolDB = previous })
}

// userExamDecryptRouter 组装包含新密钥接口的完整路由。
func userExamDecryptRouter(user *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/admin/userexam")
	group.Use(stubUser(user), middleware.RequireAdmin)
	group.POST("/inspect", middleware.BodyLimit(MaxUserExamInspectUploadBytes), UserExamInspect)
	group.GET("/decrypt-key", UserExamDecryptKeyDetail)
	group.PUT("/decrypt-key", UserExamDecryptKeyUpdate)
	return router
}

// ─────────────────────────── 核心：加密库端到端 ───────────────────────────

// 加密库 → 服务器解密 → 巡检成功，且返回真实业务数据。
//
// 这是本次需求的核心判据。断言到 `schoolId=196586` 而不只是「200 OK」——
// 只断状态码时，一个「解密出垃圾但没报错」的实现也能过。
func TestUserExamInspectDecryptsEncryptedUpload(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", ueRead(t, ueDefaultFixture)))
	if response.Code != http.StatusOK {
		t.Fatalf("加密库应被自动解密并返回 200，实际 %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Tables map[string]struct {
			Rows []map[string]any `json:"rows"`
		} `json:"tables"`
		Decrypted *bool `json:"decrypted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows := payload.Tables["baseInfo"].Rows
	if len(rows) != 1 {
		t.Fatalf("baseInfo 行数 = %d, want 1", len(rows))
	}
	if got, _ := rows[0]["schoolId"].(string); got != "196586" {
		t.Fatalf("schoolId = %v, want 196586（解密数据不对）", rows[0]["schoolId"])
	}
	// 必须告诉前端「这次解密过」，UI 才能显示「已自动解密」。
	if payload.Decrypted == nil || !*payload.Decrypted {
		t.Fatalf("decrypted = %v, want true", payload.Decrypted)
	}
}

// 后台配了自定义密钥 → 必须用自定义密钥解开自定义夹具。
func TestUserExamInspectUsesConfiguredKey(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	if _, err := service.SetUserExamDecryptKeyWithDB(db, ueCustomKey); err != nil {
		t.Fatalf("set key: %v", err)
	}
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", ueRead(t, ueCustomFixture)))
	if response.Code != http.StatusOK {
		t.Fatalf("自定义密钥应能解开自定义夹具，实际 %d body=%s", response.Code, response.Body.String())
	}

	// 反向断言：配了自定义密钥后，默认密钥加密的库**解不开**了。
	//
	// 这一条是「优先读 system_settings」的真正判据 —— 如果实现恒用默认密钥，
	// 上面那条（用自定义夹具）会红；如果实现恒用配置值，这条会红。
	// 两条一起才能钉住「优先级」而不是「某一个能解」。
	response2 := httptest.NewRecorder()
	router.ServeHTTP(response2, buildInspectRequest(t, "UserExamInfo.db", ueRead(t, ueDefaultFixture)))
	if response2.Code != http.StatusBadRequest {
		t.Fatalf("默认密钥的库在配置自定义密钥后应解不开，实际 %d body=%s",
			response2.Code, response2.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response2.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reason, _ := payload["reason"].(string); reason != "wrong-key" {
		t.Fatalf("reason = %q, want wrong-key", reason)
	}
}

// 后台未配密钥 → 用内置默认密钥仍能解（开箱即用，用户明确要求）。
func TestUserExamInspectFallsBackToDefaultKey(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db) // 空库：没有任何密钥配置
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", ueRead(t, ueDefaultFixture)))
	if response.Code != http.StatusOK {
		t.Fatalf("未配置密钥时必须用默认密钥解开，实际 %d body=%s", response.Code, response.Body.String())
	}
}

// 明文库仍能直接巡检（**现有能力不得破坏**）。
func TestUserExamInspectStillAcceptsPlaintext(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.decrypted.db", ueRead(t, uePlainFixture)))
	if response.Code != http.StatusOK {
		t.Fatalf("明文库必须继续可用，实际 %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Tables map[string]struct {
			Rows []map[string]any `json:"rows"`
		} `json:"tables"`
		Decrypted *bool `json:"decrypted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Tables["baseInfo"].Rows) != 1 {
		t.Fatalf("明文库应解析出 1 行 baseInfo")
	}
	// 明文库没走解密路径，decrypted 必须是 false（区分「解过」与「本来就是明文」）。
	if payload.Decrypted == nil || *payload.Decrypted {
		t.Fatalf("decrypted = %v, want false（明文直通不应标记为已解密）", payload.Decrypted)
	}
}

// ─────────────────────────── 四种错误必须可区分 ───────────────────────────

// 密钥不对 → reason=wrong-key，且文案指向「去改密钥」而不是让人换工具。
func TestUserExamInspectWrongKeyReason(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	// 设一个与夹具不匹配的密钥
	if _, err := service.SetUserExamDecryptKeyWithDB(db, "A-COMPLETELY-DIFFERENT-KEY"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", ueRead(t, ueDefaultFixture)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("密钥错应 400，实际 %d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	reason, _ := payload["reason"].(string)
	message, _ := payload["message"].(string)
	if reason != "wrong-key" {
		t.Fatalf("reason = %q, want wrong-key", reason)
	}
	// 文案必须给出可执行动作（去后台改密钥），不能只是「解密失败」。
	if !strings.Contains(message, "密钥") {
		t.Fatalf("文案应提到密钥，实际 %q", message)
	}
	if strings.Contains(message, "脱机工具") {
		t.Fatalf("密钥错不应推给脱机工具：%q", message)
	}
}

// 同一个上传内容，**密钥错**与**不是数据库**必须是不同 reason —— 这是分流的判据。
//
// 用「结构像加密库但密钥错」 vs 「完全不是库」两个输入对照，
// 断言 reason 不同且 message 不同。若实现把两者合并（历史缺陷形态），这条会红。
func TestUserExamInspectDistinguishesWrongKeyFromNotDatabase(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	if _, err := service.SetUserExamDecryptKeyWithDB(db, "A-COMPLETELY-DIFFERENT-KEY"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	router := userExamDecryptRouter(adminUser())

	collect := func(content []byte) (string, string) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", content))
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		reason, _ := payload["reason"].(string)
		message, _ := payload["message"].(string)
		return reason, message
	}

	wrongKeyReason, wrongKeyMsg := collect(ueRead(t, ueDefaultFixture))
	notDBReason, notDBMsg := collect([]byte("this is definitely not a database file"))

	if wrongKeyReason == notDBReason {
		t.Fatalf("两种失败原因被合并了（都是 %q）——用户无法判断该改密钥还是换文件", wrongKeyReason)
	}
	if wrongKeyMsg == notDBMsg {
		t.Fatalf("两种失败文案相同：%q", wrongKeyMsg)
	}
	if notDBReason != "not-database" {
		t.Fatalf("非数据库 reason = %q, want not-database", notDBReason)
	}
}

// ─────────────────────────── 密钥接口（不回显 + 权限） ───────────────────────────

// 密钥查询接口**绝不回显密钥**，只给状态与弱指纹。
func TestUserExamDecryptKeyDetailNeverEchoesKey(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	const secret = "MY-VERY-SECRET-KEY-XYZ"
	if _, err := service.SetUserExamDecryptKeyWithDB(db, secret); err != nil {
		t.Fatalf("set key: %v", err)
	}
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/userexam/decrypt-key", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	// 最强判据：响应体全文不得包含密钥原文（含子串也不行）。
	if strings.Contains(body, secret) {
		t.Fatalf("响应体泄漏了密钥原文：%s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 必须有配置状态与弱指纹（管理员靠它确认「改的值生效了」）。
	if configured, _ := payload["configured"].(bool); !configured {
		t.Fatal("configured 应为 true")
	}
	hint, _ := payload["keyHint"].(string)
	if len(hint) != 8 {
		t.Fatalf("keyHint = %q, want 8 位弱指纹", hint)
	}
	if hint == secret {
		t.Fatal("keyHint 不能等于密钥")
	}
	// 结构里不应存在任何名为 key/password 的字段（防止将来「顺手加上」）。
	for _, forbidden := range []string{"key", "password", "secret", "decryptKey"} {
		if _, exists := payload[forbidden]; exists {
			t.Fatalf("响应结构不得包含 %q 字段（泄漏风险）", forbidden)
		}
	}
}

// 未配置时，状态必须明确告诉管理员「正在用内置默认密钥」。
func TestUserExamDecryptKeyDetailReportsDefault(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	router := userExamDecryptRouter(adminUser())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/userexam/decrypt-key", nil))
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if usingDefault, _ := payload["usingDefault"].(bool); !usingDefault {
		t.Fatal("空库必须报告 usingDefault=true")
	}
	if configured, _ := payload["configured"].(bool); configured {
		t.Fatal("空库 configured 应为 false")
	}
	// 响应仍不能泄漏默认密钥原文。
	if strings.Contains(response.Body.String(), service.UserExamDefaultDecryptKey) {
		t.Fatal("响应体泄漏了内置默认密钥")
	}
}

// 更新密钥：成功返回新状态，且**响应里不含密钥**。
func TestUserExamDecryptKeyUpdatePersists(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	router := userExamDecryptRouter(adminUser())

	body := bytes.NewBufferString(`{"key":"NEW-KEY-abc123"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/admin/userexam/decrypt-key", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "NEW-KEY-abc123") {
		t.Fatal("更新响应不得回显密钥")
	}
	// 落库验证：重新读出来必须是新值（这是持久化的判据）。
	key, usingDefault := service.GetUserExamDecryptKeyWithDB(db)
	if usingDefault || key != "NEW-KEY-abc123" {
		t.Fatalf("key = %q usingDefault=%v, want NEW-KEY-abc123/false", key, usingDefault)
	}
}

// 超长密钥必须 400（明确拒绝而不是截断后静默生效）。
func TestUserExamDecryptKeyUpdateRejectsTooLong(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	router := userExamDecryptRouter(adminUser())

	body := bytes.NewBufferString(`{"key":"` + strings.Repeat("x", 40) + `"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/admin/userexam/decrypt-key", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", response.Code, response.Body.String())
	}
}

// 密钥接口对非管理员必须 403（与巡检接口同一条门禁）。
func TestUserExamDecryptKeyRoutesRejectNonAdmin(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	for _, user := range []*entity.User{ordinaryUser(), nil} {
		router := userExamDecryptRouter(user)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			request := httptest.NewRequest(method, "/api/admin/userexam/decrypt-key", bytes.NewBufferString(`{"key":"x"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s 非管理员 status = %d, want 403", method, response.Code)
			}
		}
	}
}

// ─────────────────────────── 临时文件泄漏防护 ───────────────────────────

// 请求结束后临时目录里不得残留 userexam-inspect-*.db（明文库不留副本）。
//
// 判据用「请求前后 os.TempDir 里的 userexam-inspect-* 文件集合不增长」——
// 比「断言某个具体路径不存在」更可靠（临时文件名是随机的，拿不到）。
// 只统计本前缀，不会受其它测试/进程干扰。
func TestUserExamInspectLeavesNoTempCopy(t *testing.T) {
	db := ueTestDB(t)
	installUserExamToolDB(t, db)
	router := userExamDecryptRouter(adminUser())

	countTemp := func() int {
		entries, err := os.ReadDir(os.TempDir())
		if err != nil {
			t.Fatalf("read temp dir: %v", err)
		}
		count := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "userexam-inspect-") {
				count++
			}
		}
		return count
	}

	before := countTemp()
	// 覆盖两条会产生临时文件的路径：加密库（解密后覆盖写）与明文库。
	for _, content := range [][]byte{
		ueRead(t, ueDefaultFixture),
		ueRead(t, uePlainFixture),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, buildInspectRequest(t, "UserExamInfo.db", content))
		if response.Code != http.StatusOK {
			t.Fatalf("上传应成功，实际 %d body=%s", response.Code, response.Body.String())
		}
	}
	after := countTemp()
	if after > before {
		// 列出残留文件帮助定位（只在失败时打印）。
		entries, _ := os.ReadDir(os.TempDir())
		leftovers := []string{}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "userexam-inspect-") {
				leftovers = append(leftovers, entry.Name())
			}
		}
		t.Fatalf("请求结束后残留了临时文件 %d → %d：%v（明文库泄漏）", before, after, leftovers)
	}
}

// 确认真实夹具存在，避免「夹具路径写错 → 测试静默跳过」的假绿。
//
// 这三条是「加密夹具真的在仓库里」的钉子：如果夹具被误删，
// 上面的端到端用例会因为读取失败而明确报错；这条额外给出可读的定位信息。
func TestUserExamDecryptFixturesExist(t *testing.T) {
	for _, path := range []string{ueDefaultFixture, ueCustomFixture, uePlainFixture} {
		info, err := os.Stat(filepath.Clean(path))
		if err != nil {
			t.Fatalf("夹具缺失 %s: %v（真实加密夹具是端到端用例的前提）", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("夹具为空 %s", path)
		}
	}
}

// 明文夹具必须是明文 SQLite 头；加密夹具必须不是 —— 防止「夹具被换成明文」
// 导致加密用例实际上在测明文直通（那会让整组用例失去意义）。
func TestUserExamDecryptFixturesHaveExpectedShape(t *testing.T) {
	plain := ueRead(t, uePlainFixture)
	if !service.IsPlaintextUserExamDatabase(plain) {
		t.Fatal("plain 夹具必须是明文 SQLite")
	}
	enc := ueRead(t, ueDefaultFixture)
	if service.IsPlaintextUserExamDatabase(enc) {
		t.Fatal("enc 夹具不能是明文 SQLite（否则加密路径没被测到）")
	}
	if !service.UserExamDecryptLooksEncrypted(enc) {
		t.Fatal("enc 夹具必须被识别为 aes256cbc 加密库")
	}
}
