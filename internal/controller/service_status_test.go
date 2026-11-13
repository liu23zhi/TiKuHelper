package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// TestBuildServiceStatusResponseEmptyIsEmptyArray 是判据 ③ 的 HTTP 层：
// 空缓冲时 body 里的 points 必须是 []，不是 null，也不是 1440 个零点；
// summary.status 必须是 unknown，不能因为「0/0」被算成 100% healthy。
func TestBuildServiceStatusResponseEmptyIsEmptyArray(t *testing.T) {
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(), nil, nil)
	body := buildServiceStatusResponse(sampler)

	if body.Points == nil {
		t.Fatal("points 必须是非 nil 的空切片，否则序列化成 null")
	}
	if len(body.Points) != 0 {
		t.Fatalf("空缓冲必须返回 0 个点，got=%d（1440 个零点就是这条判据要挡的缺陷）", len(body.Points))
	}
	if body.Retained != 0 {
		t.Fatalf("retained 期望 0，got=%d", body.Retained)
	}
	if body.Capacity != 1440 {
		t.Fatalf("capacity 期望 1440，got=%d", body.Capacity)
	}
	if body.Status != "unknown" {
		t.Fatalf("空缓冲的状态必须是 unknown，got=%q", body.Status)
	}
	if body.WindowMinutes != 5 {
		t.Fatalf("windowMinutes 期望 5，got=%d", body.WindowMinutes)
	}
	if body.SchemaVersion != 1 {
		t.Fatalf("schemaVersion 期望 1，got=%d", body.SchemaVersion)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"points":[]`) {
		t.Fatalf("空缓冲必须序列化成 \"points\":[]，got=%s", raw)
	}
	if strings.Contains(string(raw), `"points":null`) {
		t.Fatalf("空缓冲不得序列化成 null: %s", raw)
	}
}

// TestGetServiceStatusHTTPResponse 走真实 gin 上下文，钉状态码/缓存头/字段可解析，
// 并确认路由不把查询串带进响应（与 /system-metrics 同一条纪律）。
func TestGetServiceStatusHTTPResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?range=24h&owner=PRIVATE_STATUS_SENTINEL", nil)
	GetServiceStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status 期望 200，got=%d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control 期望 no-store，got=%q", got)
	}
	if strings.Contains(w.Body.String(), "PRIVATE_STATUS_SENTINEL") {
		t.Fatal("查询串泄进了响应体")
	}
	var decoded serviceStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Capacity != service.ServiceStatusSlots || decoded.WindowMinutes != service.ServiceStatusWindowMinutes {
		t.Fatalf("响应元信息错: %+v", decoded)
	}
	if _, err := time.Parse(time.RFC3339, decoded.RestartedAt); err != nil {
		t.Fatalf("restartedAt 必须是 RFC3339（UI 要靠它标注曲线起点），got=%q err=%v", decoded.RestartedAt, err)
	}
}

// TestBuildServiceStatusResponseIncludesPendingMinute 钉「进行中的分钟也出现在序列里」：
// 否则状态页要等整整一分钟才有一条数据，114 §4.3 的例子在刚启动时看不到。
func TestBuildServiceStatusResponseIncludesPendingMinute(t *testing.T) {
	base := time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)
	now := base
	counters := service.StatusCounters{TotalRequests: 100, FailedRequests: 40}
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(),
		func() time.Time { return now }, func() service.StatusCounters { return counters })

	// 第一分钟
	sampler.Sample()
	// 翻页：第一分钟封存，第二分钟零流量进行中
	now = base.Add(time.Minute)
	sampler.Sample()

	body := buildServiceStatusResponse(sampler)
	if len(body.Points) != 1 {
		t.Fatalf("只有第一分钟有流量，points 期望 1 个，got=%d", len(body.Points))
	}
	if body.Points[0].TotalRequests != 100 || body.Points[0].FailedRequests != 40 {
		t.Fatalf("封存的点内容错: %+v", body.Points[0])
	}
	if body.Retained != 1 {
		t.Fatalf("retained 期望 1，got=%d", body.Retained)
	}
	// 100 次 60 成功 = 60% → down
	if body.Status != "down" {
		t.Fatalf("60%% 必须推导为 down，got=%q", body.Status)
	}
	if body.Summary.SuccessRate != 0.6 {
		t.Fatalf("successRate 期望 0.6，got=%v", body.Summary.SuccessRate)
	}
}

// TestServiceStatusResponseHasNoUserOrRequestData 钉隐私边界：
// 响应只含聚合数字，不含用户、题目、令牌、账务字段。
func TestServiceStatusResponseHasNoUserOrRequestData(t *testing.T) {
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(), nil, nil)
	sampler.Sample()
	raw, err := json.Marshal(buildServiceStatusResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"userId", "username", "question", "answer", "token", "amount", "balance", "requestId"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("响应不得包含 %q: %s", forbidden, raw)
		}
	}
}
