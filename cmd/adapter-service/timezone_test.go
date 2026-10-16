package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

// 回归：Gin 访问日志必须用上海时间，不能跟着容器 TZ 变成 UTC。
//
// 线上事故形态：业务日志 12:59:20、同一请求的 [GIN] 行却是 04:59:20。
func TestGinAccessLogUsesShanghaiTime(t *testing.T) {
	original := time.Local
	defer func() { time.Local = original }()
	useShanghaiTime()

	gin.SetMode(gin.TestMode)
	buffer := &bytes.Buffer{}
	originalWriter := gin.DefaultWriter
	gin.DefaultWriter = buffer
	defer func() { gin.DefaultWriter = originalWriter }()

	server := gin.Default()
	server.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	server.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	matched := regexp.MustCompile(`\[GIN\].*?(\d{4})/(\d{2})/(\d{2}) - (\d{2}):`).FindStringSubmatch(buffer.String())
	if matched == nil {
		t.Fatalf("unexpected gin log line: %q", buffer.String())
	}
	year, _ := strconv.Atoi(matched[1])
	month, _ := strconv.Atoi(matched[2])
	day, _ := strconv.Atoi(matched[3])
	hour, _ := strconv.Atoi(matched[4])

	shanghai := time.Now().In(logger.ShanghaiLocation())
	// 允许跨小时边界的极小误差：对比前后两分钟的合法小时集合。
	allowed := map[int]bool{shanghai.Hour(): true, shanghai.Add(-2 * time.Minute).Hour(): true, shanghai.Add(2 * time.Minute).Hour(): true}
	if !allowed[hour] {
		t.Fatalf("gin log hour %d is not Shanghai hour, line=%q", hour, buffer.String())
	}
	if year != shanghai.Year() || time.Month(month) != shanghai.Month() || day != shanghai.Day() {
		t.Fatalf("gin log date %04d/%02d/%02d is not Shanghai date %s, line=%q",
			year, month, day, shanghai.Format("2006/01/02"), buffer.String())
	}

	// 顺带钉住「不会再变回 UTC」：偏移必须是 +8。
	if _, offset := time.Now().Zone(); offset != 8*3600 {
		t.Fatalf("expected +08:00 process time zone, got offset %d", offset)
	}
}
