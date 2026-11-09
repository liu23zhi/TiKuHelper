package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// TestDumpSensitiveWordShapeDiff 把开/关检测的答题响应体写进文件，供汇报逐字节对比。
//
// 它不改断言逻辑：真正的断言在 TestSensitiveWordCheckKeepsAnswerResponseShapeIdentical。
// 这个测试只负责产出「可粘贴的原始证据」—— 测试代码自己证明自己不够，
// 汇报里要给出两个真实响应体。
func TestDumpSensitiveWordShapeDiff(t *testing.T) {
	dir := os.Getenv("SENSITIVE_WORD_DIFF_DIR")
	if dir == "" {
		t.Skip("未设置 SENSITIVE_WORD_DIFF_DIR，跳过 dump")
	}
	gin.SetMode(gin.TestMode)

	offDB := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, offDB)
	offUser, offToken := sensitiveSeed(t, offDB, 601, "dump-off-user")
	offBody := runShapeSearch(t, offDB, offUser, offToken, "req-dump-off")

	onDB := newSearchControllerTestDB(t)
	seedSensitiveLogColumns(t, onDB)
	enableSensitiveWordCheck(t, onDB)
	server, _ := sensitiveStubServer(t, 0, http.StatusOK,
		`{"status":"forbidden","original_text":"题目","masked_text":"**","forbidden_words":["敏感"]}`)
	restore := service.SetSensitiveWordCheckURLForTesting(server.URL)
	defer restore()
	onUser, onToken := sensitiveSeed(t, onDB, 602, "dump-on-user")
	onBody := runShapeSearch(t, onDB, onUser, onToken, "req-dump-on")
	if err := WaitForSensitiveWordChecks(context.Background()); err != nil {
		t.Fatalf("wait sensitive: %v", err)
	}

	var prettyOff, prettyOn bytes.Buffer
	if err := json.Indent(&prettyOff, offBody, "", "  "); err != nil {
		t.Fatalf("indent off: %v", err)
	}
	if err := json.Indent(&prettyOn, onBody, "", "  "); err != nil {
		t.Fatalf("indent on: %v", err)
	}
	if err := os.WriteFile(dir+"/response-off.json", prettyOff.Bytes(), 0o644); err != nil {
		t.Fatalf("write off: %v", err)
	}
	if err := os.WriteFile(dir+"/response-on.json", prettyOn.Bytes(), 0o644); err != nil {
		t.Fatalf("write on: %v", err)
	}
	fmt.Printf("identical=%t\n", bytes.Equal(offBody, onBody))
}
