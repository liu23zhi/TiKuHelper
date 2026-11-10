package util

import (
	"github.com/gookit/goutil/strutil"
	"regexp"
	"strings"
)

// IsTrue 判断字符串是否是正确。
//
// ⚠️ 本函数是**选项侧的格式化开关**（FormatOptions / 题库导入 parse_xls 使用），
// 它的取值集合被既有测试精确钉住（"yes"/"TRUE" 必须保持原样、不得归一化），
// 因此**刻意**保持大小写敏感、且不接受小写之外的英文变体。
//
// 与答案侧的 JudgementPolarity 是两个不同职责：
//   - IsTrue/IsFalse：决定「选项文本要不要改写成 正确/错误」，只在选项侧调用；
//   - JudgementPolarity：决定「这段文本表达对还是错」，答案与选项两侧都用。
//
// 想扩充判断题的同义词，请改 polarity.go 的 judgementPolarityTable，
// 不要在这里加 —— 那会让选项被意外改写、破坏客户端看到的原文。
func IsTrue(s string) bool {
	return regexp.MustCompile(`^(正确|是|对|√|T|ri|true|A)$`).MatchString(s)
}

// IsFalse 判断字符串是否是错误。取值集合同样被既有测试钉住，见 IsTrue 的说明。
func IsFalse(s string) bool {
	return regexp.MustCompile(`^(错误|否|错|×|F|fa|false|B)$`).MatchString(s)
}

// FormatOptions 格式化选项
func FormatOptions(options []string, questionType int) []string {
	if options == nil || len(options) == 0 {
		return []string{}
	}

	options = formatOptions(options)
	if questionType == 3 && len(options) == 2 { // 判断题
		for i := range options {
			if IsTrue(options[i]) {
				options[i] = "正确"
			} else if IsFalse(options[i]) {
				options[i] = "错误"
			}
		}
	} else if questionType <= 1 { // 选择题的格式化暂时还没有实现
		for i := range options {
			options[i] = FormatString(options[i])
		}
	}
	return options
}

func formatOptions(options []string) []string {
	var formattedOptions []string
	re := regexp.MustCompile(`^[A-Z][.．:：、]\s?`)
	for _, option := range options {
		formattedOption := strutil.Trim(re.ReplaceAllString(option, ""), " \t\n\r")
		formattedOptions = append(formattedOptions, formattedOption)
	}
	return formattedOptions
}

// FormatString 格式化字符串
func FormatString(src string) string {
	// 全角转半角
	src = FullWidthStrToHalfWidthStr(src)
	// 中文常见符号转英文
	src = strings.ReplaceAll(src, "“", `"`)
	src = strings.ReplaceAll(src, "”", `"`)
	src = strings.ReplaceAll(src, "‘", "'")
	src = strings.ReplaceAll(src, "’", "'")
	src = strings.ReplaceAll(src, "。", ".")
	src = strings.ReplaceAll(src, "&nbsp;", " ")

	// 去除末尾的常见字符
	src = strings.TrimRightFunc(src, func(r rune) bool {
		return strings.ContainsRune(",.?:!;", r)
	})

	return strings.TrimSpace(src)
}

// FullWidthStrToHalfWidthStr 全角转半角
func FullWidthStrToHalfWidthStr(str string) string {
	var b strings.Builder
	b.Grow(len(str))
	for _, charCode := range str {
		insideCcode := charCode
		if insideCcode == 12288 {
			insideCcode = 32
		} else {
			insideCcode -= 65248
		}

		if insideCcode < 32 || insideCcode > 126 {
			b.WriteRune(charCode)
		} else {
			b.WriteRune(insideCcode)
		}
	}

	return b.String()
}
