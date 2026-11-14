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
//
// S4（138 §4.4）：分桶必须**两个端点都返回**。这一条不是「顺手加个字段」——
// 摘要端点给出「搜题接口现在好不好」，序列端点给出「什么时候开始不好的」，
// 只给一边就会出现「曲线显示异常但我看不出是哪个接口族」或者相反。
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
	SuccessRateAvailable bool  `json:"successRateAvailable"`
	TotalRequests        int64 `json:"totalRequests"`
	SuccessRequests      int64 `json:"successRequests"`
	// ⚠️ 2026-11-15 移除 FailedRequests：本端点是 AuthOrAccessToken（普通用户 + adapter key），
	// 而 series 端点（同鉴权）从建立起就不给失败数。两个公开端点口径必须一致，
	// 否则「summary 有失败数、series 没有」本身就是矛盾。
	//
	// 为什么失败数不该给普通用户（与 publicEndpointCounters / publicStatusPoint 同一条理由）：
	// 失败数 = totalRequests - successRequests，给了是冗余；但显式给出等于告诉用户
	// 「这里有个失败率指标值得盯」，而「搜题桶全失败」直接就是攻击指纹。
	// 需要失败数的场景（运维排障）走 admin 端点 /admin/service-status，那里是全字段。
	AvgLatencyMs  int64  `json:"avgLatencyMs"`
	WindowMinutes int    `json:"windowMinutes"`
	Retained      int    `json:"retained"`
	Capacity      int    `json:"capacity"`
	RestartedAt   string `json:"restartedAt"`

	// Endpoints 是按接口族分桶的聚合（138 §4.2.2 / S4）。
	//
	// 放进**摘要**端点是本设计最直接的价值：顶层的 successRate 是全站口径，
	// 会被健康检查、管理页、静态资源这些「other」请求稀释 —— 搜题链路整个挂掉时
	// 只要 other 还在跑，顶层成功率仍可能是 healthy。分桶之后前端才能单独回答
	// 「搜题接口现在健康吗」。
	//
	// 它不违反「摘要端点不返回时间轴」的分层：endpoints 里没有 minute，也不含
	// 任何路由字符串，只有两个固定枚举桶的计数与 samples。
	//
	// 类型是**脱敏结构** publicEndpoints（不是 service.StatusCountersByEndpoint）：
	// 后者自带 failedRequests，直接暴露会让普通用户看到「搜题桶失败 N 次」。
	Endpoints publicEndpoints `json:"endpoints"`
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
		AvgLatencyMs:         summary.AvgLatencyMs,
		WindowMinutes:        full.WindowMinutes,
		Retained:             full.Retained,
		Capacity:             full.Capacity,
		RestartedAt:          full.RestartedAt,
		// 分桶必须是**脱敏**版本（publicEndpoints），不能直接用 service.StatusCountersByEndpoint。
		//
		// ⚠️ 2026-11-15 修：原来这里写的是 `Endpoints: summary.Endpoints`（service 层全字段结构），
		// 它自带 `failedRequests`，于是普通用户端点的分桶把失败数泄漏了出去
		// —— 而同一文件的 series 端点用的是 publicEndpoints 白名单，两个端点口径不一致。
		//
		// 为什么分桶的失败数比顶层更敏感（publicEndpointCounters 的注释已写）：
		// 顶层失败数会被 other（健康检查、静态资源）稀释；而「搜题桶失败 100 次」
		// 直接就是「有人在打搜题接口且全部失败」，是攻击指纹。
		//
		// 判据（TestPublicEndpointsHideFailedRequests）：summary 与 series 的 JSON
		// 都不得出现 "failedRequests"，含分桶内。该用例同时反向断言种子带着非零失败数，
		// 所以它不会是空断言。
		Endpoints: publicEndpoints{
			Search: toPublicEndpointCounters(summary.Endpoints.Search),
			Other:  toPublicEndpointCounters(summary.Endpoints.Other),
		},
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

// publicStatusPoint 是面向普通用户的**脱敏**分钟点。
//
// # 这不是「删字段」，是决定公开哪些聚合数字
//
// service.StatusPoint 本身不含任何标识信息（没有 IP、路径、user_id、上游名），
// 所以「脱敏」在这里的意思是：**这些聚合数字各自向普通用户暴露了什么**。
// 逐项取舍见下方字段注释；被刻意剔除的字段集中列在类型定义之后。
type publicStatusPoint struct {
	// Minute 是该点所属的分钟（Unix 时间戳 / 60）。保留绝对时间：
	// 曲线要有横轴才有意义，去掉时间戳只能看出一堆无名柱状图。
	// 代价是「故障发生在哪一分钟」对普通用户可见 —— 但状态页存在的意义
	// 就是让用户看到「刚才是不是挂了」，隐藏时间会让这个页面失去用途。
	Minute int64 `json:"minute"`

	// TotalRequests 是该分钟的总请求数。保留：曲线的纵轴。
	// 它确实是全站业务量（能推出 QPS 曲线），但本产品的用户即付费客户，
	// 业务量对他们不是秘密 —— 而且一个「只有成功率的曲线」无法解释
	// 「样本太少所以成功率抖动」，反而更容易误导。
	TotalRequests int64 `json:"totalRequests"`

	// SuccessRequests 是该分钟的成功数。保留：与 TotalRequests 一起构成成功率。
	SuccessRequests int64 `json:"successRequests"`

	// AvgLatencyMs 是该分钟的平均耗时。保留：运维价值高、竞争情报价值低。
	// 用户能据此判断「现在是不是慢」，这正是他们需要的。
	AvgLatencyMs int64 `json:"avgLatencyMs"`

	// ⚠️ 刻意**不**包含的字段，每一项都有具体理由：
	//
	//	FailedRequests  —— 可由 TotalRequests - SuccessRequests 推出，给了是冗余；
	//	                  显式给出反而暗示「这里有个失败率指标值得盯」。
	//	UpstreamFailures —— 暴露上游依赖的稳定性。第三方竞品/供应商能据此
	//	                   判断你换了哪个题源、什么时候在降级。
	//	TimeoutFailures  —— 暴露上游超时模式，等价于告诉你慢在哪一跳。
	//	AuthFailures     —— **本条最关键**：这个计数在「有人在爆破/撞库」时会尖峰。
	//	                   把它给普通用户，等于把「攻击正在生效」实时回显给攻击者本人。
	//	                   他打一下，看一眼自己的战果 —— 这是唯一一条我认为
	//	                   不该公开的（其余都是「给了也行」）。
	//	QuotaFailures    —— 暴露计费/配额的内部状态，与普通用户的「现在好不好」
	//	                   判断无关。
}

// publicEndpointCounters 是面向普通用户的**脱敏**接口族桶。
//
// 与 service.EndpointCounters 的差别只有一处：**不含 FailedRequests**。
//
// 理由与 publicStatusPoint 一致（那边也是刻意不给 failedRequests）：
// 失败数可由 totalRequests - successRequests 推出，给了是冗余；显式给出反而
// 暗示「这里有个失败率指标值得盯」。分桶之后它更敏感 —— 「搜题桶失败 100 次」
// 直接就是「有人在打搜题接口并全部失败」，而顶层失败数会被 other 稀释掉。
//
// samples 必须保留：它是前端判「要不要显灰」的唯一依据（138 §4.2.2）。
type publicEndpointCounters struct {
	TotalRequests   int64 `json:"totalRequests"`
	SuccessRequests int64 `json:"successRequests"`
	AvgLatencyMs    int64 `json:"avgLatencyMs"`
	Samples         int64 `json:"samples"`
}

// publicEndpoints 是公开序列端点顶层的分桶聚合。字段固定两个，不可扩展。
type publicEndpoints struct {
	Search publicEndpointCounters `json:"search"`
	Other  publicEndpointCounters `json:"other"`
}

// toPublicEndpointCounters 把内部桶压成公开桶（丢掉 FailedRequests）。
func toPublicEndpointCounters(bucket service.EndpointCounters) publicEndpointCounters {
	return publicEndpointCounters{
		TotalRequests:   bucket.TotalRequests,
		SuccessRequests: bucket.SuccessRequests,
		AvgLatencyMs:    bucket.AvgLatencyMs,
		Samples:         bucket.Samples,
	}
}

// serviceStatusSeriesResponse 是 GET /api/service-status/series 的响应体。
//
// # 为什么独立定义而不复用 serviceStatusResponse
//
// serviceStatusResponse 是 admin 端点的契约，带 points（全字段）+ summary。
// 如果普通用户端点直接复用它，将来 admin 侧「顺手加一个字段」（比如有人想把
// 上游标识或错误码加进 StatusPoint）就会**自动**泄漏给每一个持 adapter key 的用户，
// 且没有任何测试会红 —— 因为没人改过这个端点。
//
// 独立结构体 = 新增 admin 字段时普通用户端点**默认不给**。
// 这条比任何注释都硬：想让普通用户看到新字段，必须显式改这里并过白名单测试。
//
// S4（138 §4.4）：分桶在**两个端点都要返回**。序列端点只给**聚合**分桶
// （窗内计数 + samples），不给「每分钟每桶」的时序 —— 那会把「什么时段搜题量大」
// 这种运营情报从曲线里透出来，而序列端点的既定暴露面只到「逐分钟总量」。
type serviceStatusSeriesResponse struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Points        []publicStatusPoint `json:"points"`
	Endpoints     publicEndpoints     `json:"endpoints"`
	Capacity      int                 `json:"capacity"`
	Retained      int                 `json:"retained"`
	RestartedAt   string              `json:"restartedAt"`
}

// GetServiceStatusSeries 返回**脱敏后的**逐分钟序列，供普通用户绘制曲线。
//
// 与两条既有端点的关系：
//
//	admin   /admin/service-status       → 全字段（含失败四分类 + summary），运维用
//	普通     /service-status/summary     → 只有聚合数字，没有时间轴
//	普通     /service-status/series      → 脱敏逐分钟（本条），能画曲线但不能定位攻击
//
// 鉴权必须是 AuthOrAccessToken（与 summary 一致），不能是 Auth + RequireAdmin：
//   - PLUS 只持 adapter key（access_tokens 存 sha256 hex，不是 JWT），走 Auth 会 401；
//   - 用户已拍板「让普通用户也能看曲线」，挂 RequireAdmin 等于需求只兑现给管理员。
func GetServiceStatusSeries(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, buildServiceStatusSeriesResponse(ServiceStatusSampler()))
}

// buildServiceStatusSeriesResponse 把采样点脱敏成公开序列。抽出来供测试注入采样器。
//
// 空窗口时 Points 是**长度 0 的非 nil 切片**（序列化成 []），
// 不补 1440 个零点 —— 补零会让前端画出一条「全站零流量」的假曲线，
// 与「服务没起来」在视觉上完全同形。
func buildServiceStatusSeriesResponse(sampler *service.ServiceStatusSampler) serviceStatusSeriesResponse {
	points := sampler.Points()
	summary := sampler.Summary()
	public := make([]publicStatusPoint, 0, len(points))
	for _, point := range points {
		public = append(public, publicStatusPoint{
			Minute:          point.Minute,
			TotalRequests:   point.TotalRequests,
			SuccessRequests: point.SuccessRequests,
			AvgLatencyMs:    point.AvgLatencyMs,
			// 失败四分类与 FailedRequests 刻意不拷贝 —— 见 publicStatusPoint 的注释。
		})
	}
	return serviceStatusSeriesResponse{
		SchemaVersion: 1,
		Points:        public,
		// S4：序列端点也必须带分桶，否则「曲线显示异常但看不出是哪个接口族」
		// 与「摘要说搜题正常但曲线全红」这两种矛盾就没人能解。
		// 口径与摘要端点完全一致（直接取同一个 summary.Endpoints），不另算一套。
		Endpoints: publicEndpoints{
			Search: toPublicEndpointCounters(summary.Endpoints.Search),
			Other:  toPublicEndpointCounters(summary.Endpoints.Other),
		},
		Capacity:    service.DefaultServiceStatusRing.Capacity(),
		Retained:    len(public),
		RestartedAt: sampler.StartedAt().Format(time.RFC3339),
	}
}
