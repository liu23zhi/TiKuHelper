package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEmbeddedWebDistIsNotStale 防止「前端已修、后端 embed 的产物还是旧的」。
//
// # 为什么需要它（2026-11-14 真实事故）
//
// 后端用 `//go:embed dist` 把前端构建产物编译进二进制（cmd/adapter-service/main.go），
// 而 `cmd/adapter-service/dist/` 被 .gitignore 忽略，只有 index.html 被强制跟踪。
// 本地 `go run` / `go build` **不会**把 `web/dist` 同步过去 —— 只有 Dockerfile 第 17 行
// `COPY --from=builder1 /build/dist ./cmd/adapter-service/dist` 会做。
//
// 后果：源码改完、单测全绿、CI 全绿，但**用户跑本地二进制时看到的还是旧前端**。
// 本轮真实踩过：侧边栏入口拆分做了两遍用户仍复现旧行为，因为
// cmd/adapter-service/dist 停留在 2026-10-12 的构建（早一个多月）。
//
// # 判据为什么逐个点名三个页面 chunk
//
// 第一版判据是「扫到的页面 chunk 都含 viewMode 就算过」。**变异拷不动**：
// 删掉 route-tickets 后，剩下两个仍满足条件，测试照绿。
//
// 正确判据是**每个入口各自成立**：缺任何一个 chunk、或它的内容里没有 viewMode，
// 就意味着那个入口退化成「按角色判视图」—— 正是用户报的现象。
func TestEmbeddedWebDistIsNotStale(t *testing.T) {
	root := filepath.Join("..", "..")
	srcRouter := filepath.Join(root, "web", "src", "router", "router.js")
	distJS := filepath.Join(root, "cmd", "adapter-service", "dist", "js")

	routerSource, err := os.ReadFile(srcRouter)
	if err != nil {
		t.Skipf("源码不在预期位置（可能是打包后的环境）：%v", err)
	}
	// 只有源码真的在用 viewMode 时才检查产物 —— 将来若移除该机制，本用例自动失效，
	// 不会变成一条永远挡人的死断言。
	if !strings.Contains(string(routerSource), "viewMode") {
		t.Skip("router.js 里没有 viewMode，本用例不适用")
	}

	// ⚠️ 产物目录不存在 → **跳过**，不是失败。
	//
	// 理由：`cmd/adapter-service/dist/` 被 .gitignore 忽略，新克隆里只有 index.html。
	// CI 的 Go 作业也不会先构建前端（它只跑 go test），所以那里同样没有 js。
	// 若在这里 Fatalf，会让 CI 永远红 —— 那是「假红」，比漏检更糟。
	//
	// 本用例守的是「产物**存在但过期**」这一种情况（本地开发的高频形态），
	// 而不是「产物不存在」（那是全新克隆的正常状态，由 Dockerfile 负责补）。
	entries, err := os.ReadDir(distJS)
	if err != nil {
		t.Skipf("embed 的 js 目录不存在（新克隆或 CI 后端作业的正常状态）：%v。"+
			"Docker 构建会自动补齐；本地开发请先 npm run build 并复制到 cmd/adapter-service/dist", err)
	}
	if len(entries) == 0 {
		t.Skip("embed 的 js 目录为空（同上：新克隆的正常状态）")
	}

	// chunk 名来自 webpackChunkName（见 router.js 的 `/* webpackChunkName: ... */`），
	// 带内容哈希后缀，所以按前缀匹配。
	pageChunk := regexp.MustCompile(`^route-(tickets|billing-record-list|script-management)\.[0-9a-f]+\.js$`)

	// 三个入口各自必须有产物。缺一个 = 那个入口是旧行为。
	wanted := []string{"route-tickets", "route-billing-record-list", "route-script-management"}
	available := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !pageChunk.MatchString(name) {
			continue
		}
		for _, prefix := range wanted {
			if strings.HasPrefix(name, prefix+".") {
				available[prefix] = name
			}
		}
	}

	const fixHint = "修复：cd web && npm run build，然后把 web/dist/* 复制到 cmd/adapter-service/dist/。\n" +
		"（Docker 构建会自动做这一步，但本地 go run/go build 不会。）"

	for _, prefix := range wanted {
		name, ok := available[prefix]
		if !ok {
			t.Fatalf("embed 的 js 目录里缺少页面 chunk %q —— **后端 embed 的前端产物已过期或不完整**。\n%s",
				prefix, fixHint)
		}
		body, readErr := os.ReadFile(filepath.Join(distJS, name))
		if readErr != nil {
			t.Fatalf("读不到 %s：%v", name, readErr)
		}
		if !strings.Contains(string(body), "viewMode") {
			t.Fatalf("页面 chunk %s 里没有 viewMode —— **该入口的产物已过期**。\n"+
				"这个 chunk 对应的页面会退化成「按角色判视图」，即用户报的\n"+
				"「管理员点工单反馈看到的是工单管理」。\n%s", name, fixHint)
		}
	}
}
