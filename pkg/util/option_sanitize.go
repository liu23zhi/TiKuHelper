package util

import (
	"regexp"
	"strings"
)

// editorChromeMarkers 是富文本编辑器（UEditor 及其工具栏）在页面上留下的特征串。
//
// 抓取选项时会把编辑器的工具栏按钮文字与「上传」占位一起带进来，例如：
//
//	段落格式字体字号点击上传x<考生实际作答内容>
//
// 这类前缀不是题目内容，会严重干扰 AI 判题（实测让模型直接放弃作答、
// 或把考生草稿当题干照抄）。这里在**不删除考生内容**的前提下剥掉它。
var editorChromeMarkers = []string{
	"段落格式字体字号", // UEditor 工具栏按钮
	"点击上传",     // 图片上传占位
}

// editorChromePrefixRegex 兜底匹配「工具栏词组 + x/× 分隔符」形态。
var editorChromePrefixRegex = regexp.MustCompile(`^(?:段落格式|字体字号|点击上传|插入公式|插入图片|加粗斜体下划线|[，,、x×\s])+`)

// StripEditorOptionChrome 剥掉选项开头的富文本编辑器外壳，保留考生/答案内容。
//
// 只处理**开头**的已知外壳，且**只有当剩余内容足够长时**才采用结果：
// 若剥完几乎不剩内容，说明我们认错了，宁可原样返回也不要把选项删空。
//
// 这是**保守**的净化：不确定就保留原文。选项一旦被误删，AI 会直接判错题。
func StripEditorOptionChrome(option string) string {
	trimmed := strings.TrimSpace(option)
	if trimmed == "" {
		return option
	}

	stripped := trimmed
	for _, marker := range editorChromeMarkers {
		if !strings.HasPrefix(stripped, marker) {
			continue
		}
		stripped = strings.TrimSpace(strings.TrimPrefix(stripped, marker))
		// 剥掉紧跟的分隔符：`x` / `×` / `X` / 空白。
		// 实测形态是「段落格式字体字号点击上传x我列举的…」，x 紧贴内容。
		stripped = strings.TrimLeft(stripped, "xX×*·-— \t")
	}
	// 连续多个工具栏词组的兜底（如「段落格式字体字号」+「点击上传」）。
	sanitized := editorChromePrefixRegex.ReplaceAllString(stripped, "")
	if strings.TrimSpace(sanitized) != "" {
		stripped = strings.TrimSpace(sanitized)
	}

	// 至少剩下 8 个字符才认为确实剥掉了外壳；
	// 否则保留原文（宁可带噪声，也不能把选项删空）。
	if len([]rune(stripped)) < 8 || stripped == trimmed {
		return trimmed
	}
	return stripped
}

// SanitizeOptionsForAI 净化要送进 AI 的选项列表。
//
// 做两件事，都是**保守**的（宁可选原文，不误删）：
//  1. 剥掉每个选项开头的编辑器外壳（见 StripEditorOptionChrome）；
//  2. 整条丢弃「纯代码行」——编辑器初始化脚本混进选项时会挤掉真实选项、
//     让模型分心甚至放弃作答。判定标准很窄：必须是 JS 语句或 UEditor/jQuery 调用。
//
// 关键安全网：若净化后**一条不剩**，回退为原始列表。
// 整题选项恰好长得像代码是可能的，那种情况下宁可让模型看到原文。
//
// 与 chaoxing_plus 客户端侧的过滤同源：两边都过滤，是因为**老版本客户端仍在运行**，
// 服务端不能假设对方已经升级。
func SanitizeOptionsForAI(options []string) []string {
	if len(options) == 0 {
		return options
	}

	cleaned := make([]string, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		stripped := StripEditorOptionChrome(option)
		if IsEditorScriptOption(stripped) {
			continue
		}
		if _, exists := seen[stripped]; exists {
			continue
		}
		seen[stripped] = struct{}{}
		cleaned = append(cleaned, stripped)
	}

	if len(cleaned) == 0 {
		return options
	}
	return cleaned
}

// IsEditorScriptOption 判断某个选项是否就是编辑器脚本，应当整条丢弃。
//
// 判定分两层，**先看有没有强特征，再看是否含中文**：
//
//	强特征（无歧义，含中文也丢）：UEDITOR_CONFIG、.edui-、getEditor(、addListener(、
//	                        jQuery 选择器调用 $( / .parent( / .find( / .removeClass(
//	弱特征（可能是正常选项）：以 var/if/else 等开头 → 只有**不含中文**时才丢
//
// 第二层的理由：真实选项可能写成「if 条件成立」「变量 var 的值」，
// 它们含中文且确实是内容；而编辑器脚本行不含中文。
func IsEditorScriptOption(option string) bool {
	trimmed := strings.TrimSpace(option)
	if trimmed == "" {
		return true
	}

	// 纯符号行（只剩括号、分号、花括号等）不是内容。
	if !hasAlphanumericOrCJK(trimmed) {
		return true
	}

	// 第一层：无歧义的编辑器/jQuery 特征，含中文也丢弃。
	if editorStrongFeatureRegex.MatchString(trimmed) {
		return true
	}

	// 第二层：以 JS 关键字开头的行，仅在不含中文时丢弃。
	if !containsCJK(trimmed) && editorWeakFeatureRegex.MatchString(trimmed) {
		return true
	}
	return false
}

// editorStrongFeatureRegex 是无歧义的编辑器/jQuery 特征。
var editorStrongFeatureRegex = regexp.MustCompile(
	`UEDITOR_CONFIG|\.edui-|getEditor\s*\(|addListener\s*\(|pureWordCount\s*\(|` +
		`\$\s*\(|\.parent\s*\(|\.find\s*\(|\.removeClass\s*\(|\.addClass\s*\(|\.css\s*\(|` +
		`window\.|document\.`)

// editorWeakFeatureRegex 是「像 JS 语句」但不排除是正常文本的特征。
var editorWeakFeatureRegex = regexp.MustCompile(
	`^\s*[}{]?\s*(var|let|const|if|else|for|while|return|function)\b|` +
		`^\s*[{}();,]*\s*$|^\s*//|function\s|\{\s*$`)

// hasAlphanumericOrCJK 判断文本是否含字母、数字或中日韩文字。
func hasAlphanumericOrCJK(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return true
		case r >= 0x2E80:
			return true
		}
	}
	return false
}

// containsCJK 判断文本是否含中日韩文字。
func containsCJK(value string) bool {
	for _, r := range value {
		if r >= 0x2E80 && r <= 0x9FFF {
			return true
		}
		if r >= 0xF900 && r <= 0xFAFF {
			return true
		}
	}
	return false
}
