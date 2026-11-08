package service

import (
	"os"
	"testing"
)

// 用 db 目录里真实解密出来的库做一次端到端校验（没有该文件时跳过）。
// 这是唯一能证明「脱机工具产出的库能被服务端读懂」的用例。
func TestInspectUserExamDatabaseReadsRealDecryptedFixture(t *testing.T) {
	path := os.Getenv("CX_USEREXAM_DECRYPTED_FIXTURE")
	if path == "" {
		t.Skip("设置 CX_USEREXAM_DECRYPTED_FIXTURE 指向真实解密库后运行")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture 不存在: %v", err)
	}
	result, err := InspectUserExamDatabase(path)
	if err != nil {
		t.Fatalf("inspect real fixture: %v", err)
	}
	if len(result.Tables) == 0 {
		t.Fatalf("真实库未解析出任何已知表: %v", result.OtherTables)
	}
	t.Logf("已知表 %v，行数 %v，其它表 %v", keysOf(result.Tables), result.RowCounts, result.OtherTables)
}

func keysOf(tables map[string]UserExamTable) []string {
	out := make([]string, 0, len(tables))
	for name := range tables {
		out = append(out, name)
	}
	return out
}
