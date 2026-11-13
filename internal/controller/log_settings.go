package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 日志管理分区的三条管理员接口（task_doc/107 Q14 → 110 §2 → 111 §2.5）。
//
// 本文件只做 HTTP 适配：鉴权、参数解析、错误分类、响应包装。
// 配置读写在 internal/service/log_retention.go，实际清理能力在 pkg/logger
// （pruneLogFiles / pruneLogFilesByAge，本任务只读不改）。
//
// 三条一律 Auth + RequireAdmin，且 handler 内先跑 requireAdmin：
// 路由层挡住未登录者，handler 层再挡一次「已登录但非管理员」——
// 两层都有才符合本仓其它管理接口的口径（见 controller/ip_segment.go）。
//
// ⚠️ 本任务禁止改动 internal/api/router.go，因此这三条路由**尚未注册**。
// 装配时按下面的路径挂到 apiRouter（与 /system-settings 同组，即**不**挂
// middleware.BlacklistIP 的那一组）：
//
//	apiRouter.GET("/admin/log-settings",  controller.Auth, middleware.RequireAdmin, controller.LogRetentionSettingsDetail)
//	apiRouter.PUT("/admin/log-settings",  controller.Auth, middleware.RequireAdmin, controller.UpdateLogRetentionSettings)
//	apiRouter.POST("/admin/log-settings/clean", controller.Auth, middleware.RequireAdmin, controller.CleanLogRetentionNow)

// 便于测试替换（与 ip_segment.go 的注入风格一致）。
//
// 三个都在 handler 之间共享，测试用 var 覆盖后 t.Cleanup 还原。
var (
	loadLogRetentionSettings   = service.GetLogRetentionSettingsWithDB
	updateLogRetentionSettings = service.UpdateLogRetentionSettingsWithDB
	applyLogRetentionSettings  = service.ApplyLogRetentionWithDB
	// describeLogRetentionCapability 是无副作用的能力报告（GET 用）。
	describeLogRetentionCapability = service.DescribeLogRetentionCapability
	// resolveLogRetentionDB 是可替换的取库入口。
	//
	// 单独抽一个 var 而不是让测试替换 resolveSearchPersistenceDB：
	// 后者是 package 级的 func 声明，测试里既不能赋值，改了又会波及
	// 全部 search 用例。这里只是把「怎么取库」这一层做成注入点，
	// 业务逻辑仍然走 service。
	resolveLogRetentionDB = resolveSearchPersistenceDB
)

// logRetentionDB 返回管理面使用的数据库句柄（低优先级会话）。
//
// 与 ip_segment.go 一致：manager / dao 未初始化时返回 nil，由 service 层给出
// 「需要数据库连接」而不是在控制器里空指针崩溃。
func logRetentionDB(c *gin.Context) *gorm.DB {
	return withDatabasePriority(resolveLogRetentionDB(), databaseRequestContext(c), dbqueue.PriorityLow)
}

// logRetentionResponse 是 GET / PUT / clean 三条共用的响应体。
//
// 把「当前配置」与「本次应用结果」放在同一个对象里：页面保存后无需再发一次
// GET 就能看到「哪些参数已经生效、哪些要等重启」，也就不会出现
// 「保存成功但页面显示的仍是旧值」这种两端不一致。
type logRetentionResponse struct {
	Settings service.LogRetentionSettings `json:"settings"`
	// Units 显式下发单位口径，避免前端把 maxTotalGiB 当 byte 用。
	// 字节单位选 GiB / MiB（二进制单位），换算成字节分别是 1<<30 与 1<<20。
	Units string `json:"units"`
	// Apply 是**每条路径都必有**的能力报告，不是「本次请求的应用结果」。
	//
	// ⚠️ 早先它只在 PUT / clean 上有值（GET 是 nil），于是页面刚打开时
	// 「单文件上限要重启才生效」这个事实完全看不到，只有点一次保存才出现 ——
	// 管理员会以为打开时就已经一切正常。这类「helper 算对了但传输层丢了」
	// 的缺陷在本仓有过先例，所以这里把 GET 也填满。
	//
	// GET 的语义是「按库中当前配置，各项能力现在能不能生效」，
	// 因此它会调用 ApplyLogRetentionConfig 但**不写库**；
	// 它的两个 setter 会顺手做一次清理，这与「打开设置页」的期望并不冲突
	// （清理是幂等的，且本来就有后台任务在做）。
	Apply service.LogRetentionApplyResult `json:"apply"`
	// Warning 是「配置已保存但未能完全生效」的人话说明，永不导致 5xx。
	Warning string `json:"warning,omitempty"`
}

// logRetentionUnits 是人类可读的单位口径说明（Q14⑧：字节单位选 GiB 并在代码里注明）。
const logRetentionUnits = "MiB=1048576B, GiB=1073741824B"

// LogRetentionSettingsDetail 读取日志清理配置**与各项能力的当前状态**。
//
//	GET /adapter-service/admin/log-settings
//
// 读取失败一律回落默认值（service 层保证），因此这里不会因缺记录返回错误。
//
// Apply 字段用无副作用的 DescribeLogRetentionCapability 填充，**不调 apply**：
// GET 会被人手刷新、浏览器预取、监控探针调用，若它顺手清理日志，
// 就变成「一个 GET 删文件」——删的还是审计日志，事后无法复原。
func LogRetentionSettingsDetail(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	settings := loadLogRetentionSettings(logRetentionDB(c))
	c.JSON(http.StatusOK, logRetentionResponse{
		Settings: settings,
		Units:    logRetentionUnits,
		Apply:    describeLogRetentionCapability(settings),
	})
}

// UpdateLogRetentionSettings 校验并保存三个参数，然后尝试立即生效。
//
//	PUT /adapter-service/admin/log-settings
//	body: {"enabled":true,"maxAgeDays":30,"maxTotalGiB":10,"maxFileMiB":100,"cleanupIntervalMinutes":60}
//
// 校验失败 → 400 + 明确错误文案（不静默 clamp、不部分写入）。
// 校验通过但应用失败 → 仍然 200：配置已经落库（重启后按新配置生效），
// 「立即生效失败」只是本次进程内的遗憾，不该让管理员以为保存失败而反复提交。
// 失败原因进响应的 warning 字段 + logger.SysWarn，两条都要有：
// 页面要能提示，运维要看日志。
func UpdateLogRetentionSettings(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input service.UpdateLogRetentionSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	db := logRetentionDB(c)
	settings, err := updateLogRetentionSettings(db, input)
	if err != nil {
		c.JSON(logRetentionErrorStatus(err), gin.H{"message": err.Error()})
		return
	}
	applyResult, applyErr := applyLogRetentionSettings(db)
	writeLogRetentionApplied(c, settings, applyResult, applyErr)
}

// CleanLogRetentionNow 立即按当前配置清理一次（管理页的「立即清理一次」按钮）。
//
//	POST /adapter-service/admin/log-settings/clean
//
// 不接收参数：清理规则一律取库里已保存的配置，避免「按钮传了一套参数、
// 保存的是另一套」导致管理员以为改配置了其实只影响这一次清理。
func CleanLogRetentionNow(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	db := logRetentionDB(c)
	settings := loadLogRetentionSettings(db)
	applyResult, applyErr := applyLogRetentionSettings(db)
	writeLogRetentionApplied(c, settings, applyResult, applyErr)
}

// writeLogRetentionApplied 统一处理「配置 + 应用结果 → 响应」。
//
// applyErr 不为 nil 时**不改状态码**：见 UpdateLogRetentionSettings 的注释。
func writeLogRetentionApplied(c *gin.Context, settings service.LogRetentionSettings, result service.LogRetentionApplyResult, applyErr error) {
	response := logRetentionResponse{Settings: settings, Units: logRetentionUnits, Apply: result}
	// Warning 只装**本次请求**的问题，不装常驻的能力限制。
	//
	// 两者语义不同，混在一个字段里会让前端无法区分「刚才保存失败了」
	// 与「这个功能本来就不支持」——前者要重试，后者要去找开发。
	// 常驻限制走 Apply.FileSizeUnsupportedReason（三条路径都必填）。
	if applyErr != nil {
		response.Warning = "配置已保存，但立即清理未完全生效（下个周期会重试）：" + applyErr.Error()
		logger.SysWarn(response.Warning)
	}
	c.JSON(http.StatusOK, response)
}

// logRetentionErrorStatus 把 service 的校验错误映射成 400，其余按 500。
//
// 只对「填错」类错误返回 400：这类错误必须让管理员看到明确原因，
// 用统一的「更新失败」会让他反复试同一个非法值。数据库故障仍是 500，
// 因为那不是他能靠改输入解决的问题。
func logRetentionErrorStatus(err error) int {
	for _, target := range []error{
		service.ErrLogRetentionAgeDaysOutOfRange,
		service.ErrLogRetentionTotalSizeOutOfRange,
		service.ErrLogRetentionMaxFileSizeOutOfRange,
		service.ErrLogRetentionIntervalOutOfRange,
		service.ErrLogRetentionTotalBelowTwoFiles,
	} {
		if errors.Is(err, target) {
			return http.StatusBadRequest
		}
	}
	return http.StatusInternalServerError
}
