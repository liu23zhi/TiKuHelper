package registry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestDatabaseLoggerRoutesSQLSeverityAndOmitsParameters(t *testing.T) {
	var info, failures bytes.Buffer
	oldInfo, oldError := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &info, &failures
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldInfo, oldError })
	log := newDatabaseLogger().LogMode(gormlogger.Info)
	ctx := context.Background()
	trace := func() (string, int64) { return "SELECT ?", 1 }
	log.Trace(ctx, time.Now(), trace, nil)
	log.Trace(ctx, time.Now().Add(-time.Second), trace, nil)
	log.Trace(ctx, time.Now(), trace, errors.New("database-error"))
	if !strings.Contains(info.String(), "SELECT ?") {
		t.Fatalf("missing SQL info log: %s", info.String())
	}
	if !strings.Contains(info.String(), "[WARN]") || !strings.Contains(info.String(), "SLOW SQL") {
		t.Fatalf("missing SQL warning: %s", info.String())
	}
	if !strings.Contains(failures.String(), "database-error") {
		t.Fatalf("missing SQL error: %s", failures.String())
	}
	info.Reset()
	failures.Reset()
	log.Trace(ctx, time.Now(), trace, gorm.ErrRecordNotFound)
	if failures.Len() != 0 || !strings.Contains(info.String(), "SELECT ?") {
		t.Fatalf("expected record-not-found must be informational: info=%s error=%s", info.String(), failures.String())
	}
	filter := log.(interface {
		ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
	})
	sql, params := filter.ParamsFilter(ctx, "INSERT INTO system_settings VALUES (?, ?)", "uapi_pro_api_key", "real-secret")
	if len(params) != 0 || strings.Contains(sql, "real-secret") {
		t.Fatalf("SQL logger must not interpolate credentials: %s %v", sql, params)
	}
}
