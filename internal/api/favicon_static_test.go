package api

import (
	"embed"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	faviconmissing "github.com/itihey/tikuAdapter/internal/api/testdata/faviconmissing"
	faviconstatic "github.com/itihey/tikuAdapter/internal/api/testdata/faviconstatic"
)

// 本文件钉住 GET /favicon.ico 的三态语义，重点是「磁盘覆盖优先」这条分支。
//
// 为什么必须真跑 HTTP：favicon 是否生效不在 embed 层、而在**路由注册顺序**
// （必须早于 static.Serve 与 NoRoute）。只断言函数存在、或只断言 embed 里有
// favicon.ico，都测不到「实际访问 /favicon.ico 会返回什么」。

const faviconTestIndex = `<!DOCTYPE html><html><body>index</body></html>`

func faviconTestFS() embed.FS { return faviconstatic.Files }

// faviconWithoutIconFS 返回一个 dist 下**没有** favicon.ico 的 embed.FS。
func faviconWithoutIconFS() (embed.FS, error) { return faviconmissing.Files, nil }

func newFaviconTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetWebRouter(faviconTestFS(), []byte(faviconTestIndex), router)
	return router
}

// withFaviconOverride 临时设置磁盘覆盖路径（或空串表示不覆盖），返回还原函数。
func withFaviconOverride(t *testing.T, path string) {
	t.Helper()
	original := faviconDiskPath
	faviconDiskPath = func() string { return path }
	t.Cleanup(func() { faviconDiskPath = original })
}

func getFavicon(router *gin.Engine) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	router.ServeHTTP(recorder, request)
	return recorder
}

// ① 内嵌版本存在且内容非空（且真的是 ICO，不是 index.html 兜底）。
func TestFaviconServesEmbeddedICO(t *testing.T) {
	withFaviconOverride(t, "")
	recipient := getFavicon(newFaviconTestRouter(t))

	if recipient.Code != http.StatusOK {
		t.Fatalf("内嵌 favicon 应返回 200，实际 %d", recipient.Code)
	}
	if got := recipient.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Fatalf("Content-Type 应为 image/x-icon，实际 %q", got)
	}
	body := recipient.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("内嵌 favicon 内容为空")
	}
	// ICO 魔数 00 00 01 00 —— 这条同时排除「NoRoute 把 index.html 当图标返回」。
	if len(body) < 4 || body[0] != 0x00 || body[1] != 0x00 || body[2] != 0x01 || body[3] != 0x00 {
		t.Fatalf("返回内容不是 ICO（前 4 字节 % x），很可能落到了 SPA 兜底", body[:minInt(4, len(body))])
	}
}

// ② 磁盘覆盖文件存在时，必须优先返回磁盘版（内容与磁盘逐字节一致）。
func TestFaviconPrefersDiskOverride(t *testing.T) {
	diskContent := []byte("\x00\x00\x01\x00DISK-OVERRIDE-FAVICON-PAYLOAD")
	diskPath := filepath.Join(t.TempDir(), "custom-favicon.ico")
	if err := os.WriteFile(diskPath, diskContent, 0o600); err != nil {
		t.Fatalf("写入磁盘 favicon 失败: %v", err)
	}
	withFaviconOverride(t, diskPath)

	recipient := getFavicon(newFaviconTestRouter(t))
	if recipient.Code != http.StatusOK {
		t.Fatalf("磁盘覆盖应返回 200，实际 %d", recipient.Code)
	}
	if string(recipient.Body.Bytes()) != string(diskContent) {
		t.Fatalf("未优先返回磁盘版本：got %q", string(recipient.Body.Bytes()))
	}
}

// ③ 覆盖路径配了但文件不存在时，回落内嵌版本（不能 500、也不能丢图标）。
func TestFaviconFallsBackWhenDiskOverrideMissing(t *testing.T) {
	withFaviconOverride(t, filepath.Join(t.TempDir(), "does-not-exist.ico"))
	recipient := getFavicon(newFaviconTestRouter(t))

	if recipient.Code != http.StatusOK {
		t.Fatalf("覆盖文件缺失时应回落内嵌版本（200），实际 %d", recipient.Code)
	}
	body := recipient.Body.Bytes()
	if len(body) < 4 || body[2] != 0x01 || body[3] != 0x00 {
		t.Fatalf("回落内容不是 ICO：% x", body[:minInt(4, len(body))])
	}
}

// ④ 内嵌与磁盘都没有时返回 404，且**不能**返回 500。
func TestFaviconReturns404WhenNeitherExists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 空文件系统：dist 下没有任何 favicon.ico。
	emptyFS, err := faviconWithoutIconFS()
	if err != nil {
		t.Fatalf("构造空 embed 失败: %v", err)
	}
	withFaviconOverride(t, "")

	router := gin.New()
	SetWebRouter(emptyFS, []byte(faviconTestIndex), router)
	recipient := getFavicon(router)

	if recipient.Code == http.StatusInternalServerError {
		t.Fatal("图标缺失不能返回 500")
	}
	if recipient.Code != http.StatusNotFound {
		t.Fatalf("两者都不存在时应返回 404，实际 %d", recipient.Code)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
