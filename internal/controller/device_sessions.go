package controller

// exe 会话数上报端点（141 §6.2，判据 S9 的服务端落库部分）。
//
//	POST /api/user/device-sessions   [AuthOrAccessToken]
//	body: { "deviceId": "<uuid>", "count": 3 }
//	→ 204 No Content
//
// # 为什么鉴权必须是 AuthOrAccessToken（不是 Auth）
//
// exe 手里只有 adapter key（`config.json` 的 `common.settings.tiku-adapter.key`，
// 也就是 access_tokens 表里的令牌），它没有登录 JWT、也没有 cookie。挂 `controller.Auth`
// 会让这个端点在真实客户端上恒 401 —— 而 401 的表现是「exe 永远不上报会话数」，
// 即 R5/R6/R7 的槽位语义全部失效，且没有任何报错。141 §6.1 的「为什么不能复用
// /user/devices」是同一件事的另一半：那组端点挂的是 Auth。
//
// # 四条红线在代码里的落点
//
//  1. **不泄露存在性**：归属校验用 `service.ReportDeviceSessionCount`，它对
//     「不存在 / 不是你的 / 已撤销」返回**同一个** error → 统一 404。
//     绝不能分成 403 + 404：403 等于告诉攻击者「这个 deviceId 存在但不属于你」。
//  2. **容忍重复上报与乱序上报**：协议里没有客户端时间戳（141 §6.2 只有
//     {deviceId, count}），所以服务端语义是「后到者覆盖」。同一 count 重复上报幂等；
//     乱序到达的旧值会被下一次心跳自然纠正。**不返回冲突、不返回 409** ——
//     上报是后台心跳，报错只会让 exe 记一条永远看不懂的日志。
//  3. **count 是闭区间 0..8**：0 是合法值（exe 全部会话关闭 / 心跳归零），
//     8 来自 HOME_MAX_SESSIONS。负数与超界一律 400（这是可修正的输入错误，
//     不是权限问题，用 400 而不是 403 —— 与 UserDeviceLimitPut 同一取舍）。
//  4. **只写会话与心跳，不动配额**：本端点不调 CountDevices、不写 revoked_at。
//     它刷新 session_count + last_seen_at（+ session_reported_at 若列存在），
//     这与 C1 的计槽逻辑互不覆盖（141 §5.4 的「读时视为 0」正是为了避免写竞争）。
//
// # 与本文件里的 WriteDeviceConflict 的分工
//
// WriteDeviceConflict 是 141 §6.3 那 409 响应的**唯一发出点**；它不参与本端点
// （上报不存在「设备标识冲突」这种失败 —— count 越界是 400，归属问题是 404）。
// 那个函数供**设备登记路径**（登录 / 换绑 / 将来的配额端点）在拿到
// service.DecideDeviceRegistration 的冲突结论时调用，本文件只是它的落点。

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// deviceSessionDBOverride 允许测试注入数据库（nil = 走正常解析路径）。
//
// 与 deviceUserDBOverride / devicePoolDBOverride 同一动机：本包大量用例会改写 dao
// 全局单例，注入点让「写到哪个库」是确定的，而不是取决于用例执行顺序。
//
// 刻意用**独立变量**而不是复用 deviceUserDBOverride：那个变量的生命周期属于
// 「我的设备」六条接口（另一批次的独占文件），共用会让两批用例互相干扰。
var deviceSessionDBOverride *gorm.DB

// deviceSessionDB 返回本端点使用的数据库句柄。
//
// 每一档都 `Session(&gorm.Session{NewDB: true})`：清掉生成式 DAO 残留的
// model/schema，否则拿 dao.Log 的句柄去写 account_device_bindings 会
// `panic: reflect: Field index out of range`（本仓实测踩过）。
func deviceSessionDB(c *gin.Context) *gorm.DB {
	if deviceSessionDBOverride != nil {
		return deviceSessionDBOverride.Session(&gorm.Session{NewDB: true})
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		return nil
	}
	return withDatabasePriority(db, databaseRequestContext(c), dbqueue.PriorityLow)
}

// deviceSessionRequestBody 是上报请求体。
//
// Count 用指针：区分钟「没传」与「传了 0」。两者在业务上完全不同 ——
// 没传是客户端写错了（400，否则 exe 会以为自己成功上报了 0 个会话，
// 于是配额永远按 1 算），传了 0 是「全部会话已关闭」（合法）。
type deviceSessionRequestBody struct {
	DeviceID string `json:"deviceId"`
	Count    *int   `json:"count"`
}

// ReportDeviceSessions 接收一次会话数上报（exe 心跳）。
//
//	POST /api/user/device-sessions   body: {"deviceId":"...","count":2}
//
// 成功恒 204（不返回 body）：上报是后台心跳，响应体没有消费方；
// 返回 200 + JSON 只会让 exe 侧多写一段永远不读的解析代码。
func ReportDeviceSessions(c *gin.Context) {
	if !requireOrdinaryDeviceCredential(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input deviceSessionRequestBody
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式错误"})
		return
	}
	if input.Count == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "缺少会话数 count",
			"max":     service.DeviceSessionMaxCount,
		})
		return
	}
	count := *input.Count
	if count < 0 || count > service.DeviceSessionMaxCount {
		// 把上界回给客户端：8 是 HOME_MAX_SESSIONS，前端/日志里能看到真实边界，
		// 否则只能靠猜「到底几算超」（与 UserDeviceLimitPut 回 limit 同一做法）。
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "会话数必须在 0 到 " + strconv.Itoa(service.DeviceSessionMaxCount) + " 之间",
			"max":     service.DeviceSessionMaxCount,
		})
		return
	}

	db := deviceSessionDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法上报会话数"})
		return
	}

	err := service.ReportDeviceSessionCount(db, operator.ID, input.DeviceID, count, time.Now())
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, service.ErrDeviceSessionBindingNotFound):
		// 统一 404：不存在 / 不属于本账号 / 已撤销 三者对外完全同一（红线 1）。
		// 文案不含 deviceId，也不区分原因。
		c.JSON(http.StatusNotFound, gin.H{"message": "设备记录不存在"})
	case errors.Is(err, service.ErrDeviceSessionCountOutOfRange):
		// service 层再校验一次（纵深防御）：handler 的校验是给用户看的，
		// service 的是给将来其它调用方看的，两处口径必须一致。
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "会话数必须在 0 到 " + strconv.Itoa(service.DeviceSessionMaxCount) + " 之间",
			"max":     service.DeviceSessionMaxCount,
		})
	case errors.Is(err, service.ErrDeviceSessionSchemaUnavailable):
		// 列还没迁移好：503 而不是 500，也不能假装 204 ——
		// 假装成功会让 exe 永远收不到「这个功能在服务端还没就绪」的信号。
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "会话上报功能尚未就绪（数据表待迁移）"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存会话数失败"})
	}
}

// WriteDeviceConflict 写 141 §6.3 的 409 冲突响应（本仓该响应的**唯一发出点**）。
//
//	409 { "code": "DEVICE_ID_CONFLICT", "message": "设备标识冲突，请重启以重新生成" }
//
// # 为什么抽成函数而不是在每个登记入口内联一份
//
// `code` 是协议字段：客户端据它决定「重新生成 device_id + client_instance 并
// **重试一次**」（141 §4.2）。分散内联必然出现某个入口漏写 code（只剩 message），
// 那样客户端只能靠文案猜，而文案改一个字就静默失效 —— 判据 S7（重试仅 1 次）
// 也会变成「客户端不知道要不要重试」。
//
// # 与 enforceAccountDeviceLimit（403）的区别
//
//	403 DEVICE_LIMIT_EXCEEDED —— 槽位用完了，用户要**去删设备**（可自救）
//	409 DEVICE_ID_CONFLICT    —— 这个 device_id 归别人，客户端要**换 ID**（自动可恢复）
//
// 两者绝不可混：把冲突报成 403，客户端会把「换个 ID 就好」的事当成用户额度问题，
// 于是永远换不出新 ID、用户看到「设备数已满」却删无可删。
//
// # 调用约定
//
// 只在 `service.DecideDeviceRegistration(...).Allowed == false` 且
// `Reason == service.DevicePoolReasonDeviceIDConflict` 时调用；调用方随后必须 `return`
// （本函数已 Abort，继续执行会写第二个响应）。
func WriteDeviceConflict(c *gin.Context) {
	if c == nil {
		return
	}
	body := service.DeviceConflictDecisionBody()
	c.AbortWithStatusJSON(http.StatusConflict, gin.H{
		"code":    body["code"],
		"message": body["message"],
	})
}
