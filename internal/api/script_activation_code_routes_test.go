package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 管理员脚本激活码四条路由的**源码级**鉴权断言。
//
// 为什么行为测试不够：handler 内部各有一道 requireAdmin（纵深防御），
// 所以把 router.go 里这几条 middleware.RequireAdmin 删掉，只发请求的行为
// 测试**依然全绿** —— 测试用的是自己拼的 router，中间件链不是生产那一份。
// 这条断言直接读 router.go 源码，删掉任何一条都会变红。
//
// 同一纪律在 user_exam_tool_test.go 与 blacklist 相关测试里也有，
// 原因一致：本仓「handler 内二次判权」的惯例会掩盖路由层漏挂。
func TestScriptActivationCodeRoutesMountRequireAdmin(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("router.go"))
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	lines := strings.Split(string(source), "\n")

	cases := []struct {
		method string
		path   string
		handle string
	}{
		{"POST", "/admin/script-activation-codes", "controller.ScriptActivationCodeCreate"},
		{"GET", "/admin/script-activation-codes", "controller.ScriptActivationCodeList"},
		{"GET", "/admin/script-activation-codes/:id", "controller.ScriptActivationCodeDetail"},
		{"PUT", "/admin/script-activation-codes/:id", "controller.ScriptActivationCodeUpdate"},
	}

	for _, testCase := range cases {
		marker := `"` + testCase.path + `"`
		var matched []string
		for _, line := range lines {
			if !strings.Contains(line, "apiRouter.") {
				continue
			}
			if strings.Contains(line, testCase.method+"(") && strings.Contains(line, marker) {
				matched = append(matched, line)
			}
		}
		if len(matched) != 1 {
			t.Fatalf("%s %s 应恰好注册 1 条，实际 %d 条：%v",
				testCase.method, testCase.path, len(matched), matched)
		}
		line := matched[0]

		if !strings.Contains(line, "controller.Auth") {
			t.Fatalf("%s %s 未挂 controller.Auth：%s", testCase.method, testCase.path, strings.TrimSpace(line))
		}
		requireAdminAt := strings.Index(line, "middleware.RequireAdmin")
		if requireAdminAt < 0 {
			t.Fatalf("%s %s 未挂 middleware.RequireAdmin：%s", testCase.method, testCase.path, strings.TrimSpace(line))
		}
		handlerAt := strings.Index(line, testCase.handle)
		if handlerAt < 0 {
			t.Fatalf("%s %s 未指向 %s：%s", testCase.method, testCase.path, testCase.handle, strings.TrimSpace(line))
		}
		// 中间件必须排在 handler 之前，否则不生效（只是把符号写在后面）。
		if requireAdminAt > handlerAt {
			t.Fatalf("%s %s 的 RequireAdmin 排在 handler 之后，不会生效：%s",
				testCase.method, testCase.path, strings.TrimSpace(line))
		}
		// Auth 必须早于 RequireAdmin：后者依赖前者写入的 user。
		if strings.Index(line, "controller.Auth") > requireAdminAt {
			t.Fatalf("%s %s 的 controller.Auth 排在 RequireAdmin 之后，RequireAdmin 拿不到身份：%s",
				testCase.method, testCase.path, strings.TrimSpace(line))
		}
	}
}
