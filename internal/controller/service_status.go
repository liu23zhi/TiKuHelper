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
func GetServiceStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, buildServiceStatusResponse(ServiceStatusSampler()))
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
