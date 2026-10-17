package search

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// 临时回放：把线上「bestAnswerCount=0」的搜题请求用模型原始回答重放一遍，
// 统计修复后能产出非空答案的比例。验证完即删除。
func TestTempReplayEmptyAnswers(t *testing.T) {
	dir := os.Getenv("TIKU_LOG_DIR")
	if dir == "" {
		t.Skip("TIKU_LOG_DIR 未设置")
	}
	mainLines := readLines(t, filepath.Join(dir, "Tiku.txt"))
	thinkingLines := readLines(t, filepath.Join(dir, "Tiku-thinking.txt"))

	summaryRe := regexp.MustCompile(`搜题响应摘要 requestId=(\S+) hitProvider=ai .*?type=(-?\d+).*?bestAnswerCount=0`)
	requestRe := regexp.MustCompile(`搜题请求摘要 requestId=(\S+) type=(-?\d+) .*?question=(.*)$`)

	type emptyCase struct {
		requestID string
		question  string
		typeCode  int
	}
	cases := make([]emptyCase, 0)
	pending := map[string]emptyCase{}
	for _, line := range mainLines {
		if match := requestRe.FindStringSubmatch(line); match != nil {
			code, _ := strconv.Atoi(match[2])
			pending[match[1]] = emptyCase{requestID: match[1], question: strings.TrimSpace(match[3]), typeCode: code}
		}
		if match := summaryRe.FindStringSubmatch(line); match != nil {
			if item, ok := pending[match[1]]; ok {
				cases = append(cases, item)
			}
		}
	}

	previewRe := regexp.MustCompile(`requestId=(\S+).*?contentPreview=(.*?) error=`)
	previews := map[string]string{}
	for _, line := range thinkingLines {
		if match := previewRe.FindStringSubmatch(line); match != nil && strings.TrimSpace(match[2]) != "" {
			previews[match[1]] = match[2]
		}
	}

	afterOK, total := 0, 0
	for _, item := range cases {
		content := previews[item.requestID]
		if content == "" {
			continue
		}
		options := optionsFromPrompt(thinkingLines, item.question)
		total++

		newAnswers := parseAISearchAnswers(content, options, item.typeCode)
		newResp := util.FillAnswerResponse([][]string{newAnswers}, &model.SearchRequest{
			Options: options, Type: item.typeCode,
		})
		if len(newResp.Answer.BestAnswer) > 0 {
			afterOK++
		} else {
			t.Logf("仍为空: type=%d preview=%s options=%v parsed=%v", item.typeCode, truncateForLog(content, 80), options, newAnswers)
		}
	}
	t.Logf("线上空答案请求回放：共 %d 条，修复后可产出非空答案 %d 条", total, afterOK)
}

func truncateForLog(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func optionsFromPrompt(lines []string, question string) []string {
	if question == "" {
		return nil
	}
	prefix := question[:minInt(12, len(question))]
	for _, line := range lines {
		if !strings.HasPrefix(line, `{"model"`) || !strings.Contains(line, prefix) {
			continue
		}
		text := line
		index := strings.Index(text, `选项：\n`)
		if index < 0 {
			return nil
		}
		rest := text[index+len(`选项：\n`):]
		if end := strings.Index(rest, `\n作答要求`); end >= 0 {
			rest = rest[:end]
		}
		parts := strings.Split(rest, `\n`)
		options := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				options = append(options, trimmed)
			}
		}
		return options
	}
	return nil
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(string(content), "\n")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
