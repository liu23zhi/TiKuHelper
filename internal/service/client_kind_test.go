package service

import (
	"strings"
	"testing"
)

// TestNormalizeClientKind 锁死「只认三个字面值，其余一律 unknown」。
//
// 特别覆盖 exe / plugin：早期文档草案用过这两个写法，但定案是
// exam_client / extension（task_doc/111 §6.2）。悄悄接受旧写法会让
// 「落库值到底是什么」不可判定，所以必须收敛成 unknown 而不是-best-effort 映射。
func TestNormalizeClientKind(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"exam client", "exam_client", "exam_client"},
		{"extension", "extension", "extension"},
		{"unknown", "unknown", "unknown"},
		{"exam client with noise", "  EXAM_CLIENT \t", "exam_client"},
		{"extension uppercase", "Extension", "extension"},
		{"empty", "", "unknown"},
		{"whitespace only", "   ", "unknown"},
		{"legacy exe alias is not accepted", "exe", "unknown"},
		{"legacy plugin alias is not accepted", "plugin", "unknown"},
		{"exe_non_exam must not exist", "exe_non_exam", "unknown"},
		{"arbitrary text", "Mozilla/5.0", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeClientKind(tc.input)
			if got != tc.want {
				t.Fatalf("NormalizeClientKind(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if got == "" {
				t.Fatalf("NormalizeClientKind(%q) returned an empty string", tc.input)
			}
		})
	}
}

// TestResolveClientKind 覆盖三态各自的多个样本 + 优先级 + 兜底。
func TestResolveClientKind(t *testing.T) {
	cases := []struct {
		name string
		in   ClientKindInput
		want string
	}{
		// ---- exam_client：至少两个来源不同的样本 ----
		{
			name: "exam client by product user agent",
			in:   ClientKindInput{UserAgent: "CXExam/4.1.4.25512 (Windows NT 10.0; Win64; x64)"},
			want: "exam_client",
		},
		{
			name: "exam client by chaoxing-plus user agent",
			in:   ClientKindInput{UserAgent: "chaoxing-plus/6.2.0 CEF"},
			want: "exam_client",
		},
		{
			name: "exam client by script identity header",
			in:   ClientKindInput{UserAgent: "Mozilla/5.0", ScriptIdentity: "script-run-session-42"},
			want: "exam_client",
		},
		{
			name: "exam client by script token header",
			in:   ClientKindInput{ScriptToken: "X-Script-Token-value"},
			want: "exam_client",
		},

		// ---- extension：两个来源不同的样本 ----
		{
			name: "extension by chrome origin",
			in:   ClientKindInput{Origin: "chrome-extension://abcdefghijklmnop", UserAgent: "Mozilla/5.0"},
			want: "extension",
		},
		{
			name: "extension by firefox origin",
			in:   ClientKindInput{Origin: "moz-extension://01234567-89ab"},
			want: "extension",
		},
		{
			name: "extension by updater user agent",
			in:   ClientKindInput{UserAgent: "tikuAdapter-extension-updater/1"},
			want: "extension",
		},

		// ---- unknown：至少两个「判不出来」的样本 ----
		{
			name: "plain browser user agent",
			in:   ClientKindInput{UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"},
			want: "unknown",
		},
		{
			name: "empty request",
			in:   ClientKindInput{},
			want: "unknown",
		},
		{
			name: "bare chromium marker is not an exam client",
			in:   ClientKindInput{UserAgent: "Mozilla/5.0 Chrome/75.0.3770.100 Safari/537.36 CEF"},
			want: "unknown",
		},
		{
			name: "site origin is not an extension",
			in:   ClientKindInput{Origin: "https://tiku.example.com", UserAgent: "Mozilla/5.0"},
			want: "unknown",
		},

		// ---- 优先级 ----
		{
			name: "declared header outranks token record",
			in:   ClientKindInput{DeclaredKind: ClientKindExtension, TokenKind: ClientKindExamClient},
			want: "extension",
		},
		{
			name: "token record outranks user agent",
			in:   ClientKindInput{TokenKind: ClientKindExamClient, UserAgent: "tikuAdapter-extension-updater/1"},
			want: "exam_client",
		},
		{
			name: "extension outranks script header",
			in:   ClientKindInput{Origin: "chrome-extension://abcdef", ScriptIdentity: "session-1"},
			want: "extension",
		},
		{
			name: "unknown declared value falls through to token record",
			in:   ClientKindInput{DeclaredKind: "exe", TokenKind: ClientKindExtension},
			want: "extension",
		},
		{
			name: "unknown token record falls through to user agent",
			in:   ClientKindInput{TokenKind: "", UserAgent: "CXExam/4.3.0.4"},
			want: "exam_client",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveClientKind(tc.in)
			if got != tc.want {
				t.Fatalf("ResolveClientKind(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// 契约硬约束：任何输入都不许返回空串，且返回值必须落在三值集合里。
//
// 这是「绝不返回空串」的穷举钉子：用一批刻意刁钻的输入（含 nul 字符、
// 超长串、纯符号、大小写混合）跑一遍集合校验。
func TestResolveClientKindNeverReturnsEmptyOrOutOfSet(t *testing.T) {
	// ⚠️ 这个集合必须写**字面量**，不能写常量：
	// 用常量建集合等于「拿漂移后的值给自己发白名单」，常量改成任何值时都恒通过。
	// 这正是第 4 类假钉子（默认值安全项薄断言）的形态。
	allowed := map[string]bool{
		"exam_client": true,
		"extension":   true,
		"unknown":     true,
	}
	inputs := []ClientKindInput{
		{},
		{DeclaredKind: "", TokenKind: "", UserAgent: "", Origin: ""},
		{DeclaredKind: "\x00", TokenKind: "\x00", UserAgent: "\x00", Origin: "\x00"},
		{DeclaredKind: strings.Repeat("x", 4096)},
		{UserAgent: "@@@///\\\\", Origin: "://"},
		{DeclaredKind: " ", TokenKind: " ", UserAgent: " ", Origin: " "},
		{Origin: "CHROME-EXTENSION://ABC"},
		{UserAgent: "cXeXaM"},
		{ScriptIdentity: " ", ScriptToken: " "},
	}
	for i, in := range inputs {
		got := ResolveClientKind(in)
		if got == "" {
			t.Fatalf("input #%d %+v resolved to an empty string", i, in)
		}
		if !allowed[got] {
			t.Fatalf("input #%d %+v resolved to out-of-set value %q", i, in, got)
		}
	}
}

// 判定必须是纯函数：同一输入重复调用结果恒定，且不修改入参。
func TestResolveClientKindIsPure(t *testing.T) {
	in := ClientKindInput{
		DeclaredKind:   "  EXAM_CLIENT  ",
		TokenKind:      ClientKindExtension,
		UserAgent:      "CXExam/4.1.4.25389",
		Origin:         "chrome-extension://abcdef",
		ScriptIdentity: "session-9",
		ScriptToken:    "token-9",
	}
	snapshot := in

	first := ResolveClientKind(in)
	for i := 0; i < 5; i++ {
		if got := ResolveClientKind(in); got != first {
			t.Fatalf("call #%d returned %q, first call returned %q", i, got, first)
		}
	}
	if in != snapshot {
		t.Fatalf("ResolveClientKind mutated its input: got %+v, want %+v", in, snapshot)
	}
	// 期望值写字面量：NormalizeClientKind 的 switch 用的是常量，
	// 常量漂移后 "  EXAM_CLIENT  " 会归一到漂移后的值 —— 拿常量比会恒等。
	if first != "exam_client" {
		t.Fatalf("declared header must win, want \"exam_client\", got %q", first)
	}
}

// 三值集合里不得出现 exe_non_exam（用户明确否掉的第四种取值）。
//
// 直接断言字面量集合，防止后续有人为了「更细」把它加回来。
func TestClientKindValueSetHasExactlyThreeValues(t *testing.T) {
	values := []string{ClientKindExamClient, ClientKindExtension, ClientKindUnknown}
	if len(values) != 3 {
		t.Fatalf("expected exactly 3 values, got %d", len(values))
	}
	for _, value := range values {
		if strings.EqualFold(value, "exe_non_exam") {
			t.Fatalf("exe_non_exam must not be part of the value set")
		}
	}
	if ClientKindUnknown != "unknown" || ClientKindExamClient != "exam_client" || ClientKindExtension != "extension" {
		t.Fatalf("value literals drifted: %q / %q / %q", ClientKindExamClient, ClientKindExtension, ClientKindUnknown)
	}
	if ClientKindColumn != "client_kind" {
		t.Fatalf("column name drifted: %q", ClientKindColumn)
	}
	if ClientKindHeader != "X-Client-Kind" {
		t.Fatalf("header name drifted: %q", ClientKindHeader)
	}
}
