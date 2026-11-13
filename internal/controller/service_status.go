package controller

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// serviceStatusResponse 是 GET /adapter-service/admin/service-status 的响应体。
//
// 数据源是**纯内存**环形缓冲（task_doc/114 §3）：不落库、重启归零。
// RestartedAt 告诉 UI 曲线的起点，这样前端能显示「数据自 <时间> 起（服务重启于该时刻）」。
// 空缓冲时 points 是 []（不是 null，也不是 1440 个零点）。
type serviceStatusResponse struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Status        string                       `json:"status"`
	WindowMinutes int                          `json:"windowMinutes"`
	Points        []service.StatusPoint        `json:"points"`
	Summary       service.ServiceStatusSummary `json:"summary"`
	Capacity      int                          `json:"capacity"`
	Retained      int                          `json:"retained"`
	RestartedAt   string                       `json:"restartedAt"`
}

// defaultServiceStatusSampler 是进程内默认采样器。用 sync.OnceValue 惰性构造，
// 避免包初始化顺序把 time.Now 之类的东西提前固定下来。
var defaultServiceStatusSampler = sync.OnceValue(func() *service.ServiceStatusSampler {
	return service.NewDefaultServiceStatusSampler()
})

// ServiceStatusSampler 返回进程内默认采样器，供启动侧接线（StartServiceStatusSampling）。
func ServiceStatusSampler() *service.ServiceStatusSampler {
	return defaultServiceStatusSampler()
}

// GetServiceStatus 返回最近 24 小时的分钟序列 + 当前状态推导。
//
// 为什么放在 admin 路由下：曲线暴露出「什么时间段有多少请求、成功多少、平均延迟」，
// 属运维可见信息，与 /system-metrics 同级。前端状态页走管理员会话。
//
// ⚠️ 非管理员的客户端（如 PLUS 扩展，只持 adapter key）**不该**拿到逐分钟明细 ——
// 那会把「什么时段有多少请求」暴露给每一个装扩展的用户。这类调用方走
// GetServiceStatusSummary（见下），只拿聚合数字。
func GetServiceStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, buildServiceStatusResponse(ServiceStatusSampler()))
}

// serviceStatusSummaryResponse 是 GET /adapter-service/service-status/summary 的响应体。
//
// # 为什么要单独一个端点，而不是把 admin 那条的鉴权放宽
//
// 放宽 /admin/service-status 会让每个持 adapter key 的用户都能画出一整天
// 「每分钟多少请求、成功多少、平均延迟」的曲线 —— 那是竞争情报级的运营数据，
// 而 GetServiceStatus 的注释早已把它定位成「属运维可见信息」。
//
// 但 PLUS 的面板确实需要知道「现在好不好」：它的状态 Tab 是给普通用户看的
// 服务健康度指示，不是运维看板。二者的差别正好是「逐分钟明细」。
//
//	admin  /admin/service-status          → 全部字段（含 points）
//	普通    /service-status/summary        → 只有聚合数字（无 points）
//
// 分层不是靠前端「不渲染」实现的 —— 摘要端点**根本不返回** points，
// 前端想画也画不出来。
type serviceStatusSummaryResponse struct {
	SchemaVersion int     `json:"schemaVersion"`
	Status        string  `json:"status"`
	SuccessRate   float64 `json:"successRate"`
	// SuccessRateAvailable 区分「成功率真的是 0」与「窗口内没有请求所以算不出」。
	//
	// 只给 successRate 时前端只能靠 ==0 猜，而「全失败」和「没请求」在用户看来
	// 是两件完全不同的事（前者要报警，后者是正常夜间）。
	SuccessRateAvailable bool   `json:"successRateAvailable"`
	TotalRequests        int64  `json:"totalRequests"`
	SuccessRequests      int64  `json:"successRequests"`
	FailedRequests       int64  `json:"failedRequests"`
	AvgLatencyMs         int64  `json:"avgLatencyMs"`
	WindowMinutes        int    `json:"windowMinutes"`
	Retained             int    `json:"retained"`
	Capacity             int    `json:"capacity"`
	RestartedAt          string `json:"restartedAt"`
}

// GetServiceStatusSummary 返回**无明细**的服务健康摘要，供非管理员客户端使用。
//
// 刻意不返回 points / breakdown / startMinute / endMinute：
// 那些字段能反推出请求的时间分布，属运维可见信息（见 GetServiceStatus 注释）。
func GetServiceStatusSummary(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, buildServiceStatusSummaryResponse(ServiceStatusSampler()))
}

// buildServiceStatusSummaryResponse 把采样器状态压成摘要。抽出来供测试注入采样器。
func buildServiceStatusSummaryResponse(sampler *service.ServiceStatusSampler) serviceStatusSummaryResponse {
	full := buildServiceStatusResponse(sampler)
	summary := full.Summary
	return serviceStatusSummaryResponse{
		SchemaVersion: full.SchemaVersion,
		Status:        full.Status,
		SuccessRate:   summary.SuccessRate,
		// 「有没有算得出成功率的数据」= 窗口内至少有一个请求。
		// 用 TotalRequests 判而不是用 SuccessRate!=0 判：全失败时 SuccessRate 也是 0，
		// 但那时恰恰是最该报警的状态，不能被当成「无数据」。
		SuccessRateAvailable: summary.TotalRequests > 0,
		TotalRequests:        summary.TotalRequests,
		SuccessRequests:      summary.SuccessRequests,
		FailedRequests:       summary.FailedRequests,
		AvgLatencyMs:         summary.AvgLatencyMs,
		WindowMinutes:        full.WindowMinutes,
		Retained:             full.Retained,
		Capacity:             full.Capacity,
		RestartedAt:          full.RestartedAt,
	}
}

// buildServiceStatusResponse 把采样器状态转成响应体。抽出来是为了让测试能注入
// 独立采样器，不去动进程级单例。
func buildServiceStatusResponse(sampler *service.ServiceStatusSampler) serviceStatusResponse {
	points := sampler.Points()
	summary := sampler.Summary()
	return serviceStatusResponse{
		SchemaVersion: 1,
		Status:        summary.Status,
		WindowMinutes: summary.WindowMinutes,
		Points:        points,
		Summary:       summary,
		Capacity:      service.DefaultServiceStatusRing.Capacity(),
		Retained:      len(points),
		RestartedAt:   sampler.StartedAt().Format(time.RFC3339),
	}
}
