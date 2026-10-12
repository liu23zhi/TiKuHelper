package controller

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestWriteAuditLogLimitsDescriptionByCharacters(t *testing.T) {
	db := newLogControllerTestDB(t)
	writeAuditLog(nil, "search", logActionSearch, strings.Repeat("日志", 200), "", 0, "request", "complete response")
	var entry entity.Log
	if err := db.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if len([]rune(entry.Description)) > 240 {
		t.Fatalf("description over varchar limit: %d", len([]rune(entry.Description)))
	}
	if entry.NewAnswer != "complete response" {
		t.Fatalf("response was truncated: %q", entry.NewAnswer)
	}
}

func TestWriteAuditLogReportsDatabaseWriteFailure(t *testing.T) {
	db := newLogControllerTestDB(t)
	if err := db.Migrator().DropTable(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	originalWriter := gin.DefaultErrorWriter
	var output bytes.Buffer
	gin.DefaultErrorWriter = &output
	t.Cleanup(func() { gin.DefaultErrorWriter = originalWriter })
	writeAuditLog(nil, "search", logActionSearch, "搜索失败", "", 0, "", "")
	if !strings.Contains(output.String(), "审计日志写入失败") || !strings.Contains(output.String(), "module=search") {
		t.Fatalf("audit database failure was silent: %s", output.String())
	}
}
