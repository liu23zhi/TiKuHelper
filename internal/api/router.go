package api

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/controller"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// SetAPIRouter 设置API路由
func SetAPIRouter(router *gin.Engine) {
	router.Use(middleware.RequestCorrelation, middleware.CORS, middleware.SecurityHeaders)

	router.Any("/sqp/*path", middleware.BodyLimit(16<<20), controller.Auth, middleware.RequireAdmin, controller.Proxy)

	apiRouter := router.Group("/api")

	// 全局请求体上限必须**大于**单张工单图片的上限（20 MiB）再加 multipart 信封开销，
	// 否则单条路由上的 BodyLimit((20<<20)+65536) 永远走不到就被这里拦下 —— 症状是
	// 「传 8 MiB 图正常、传 12 MiB 图 413，但错误信息指向路由而非全局」。
	// 加 envelope 余量而不是正好相等：multipart 的 boundary/头部不可忽略。
	apiRouter.Use(middleware.GlobalAPIRateLimit, middleware.BodyLimit((24<<20)+65536)) // 全局限流

	// IP 黑名单（46-黑名单设计.md §3）。开关 blacklist_enabled **默认 false**：
	// 关闭时中间件零查询零写日志，全站行为与引入前完全一致。
	//
	// 挂 API 链而不是全局链，理由：
	//  1. 全局链会连静态资源（SPA 的 index.html / js / css）一起拦，于是被拉黑的
	//     管理员连登录页都加载不出来 —— 这正是 §3.3 要防的自锁；
	//  2. 全局链还覆盖 /sqp/* 代理与扩展下载，那些路径的处置不在本期验收范围。
	//
	// 顺序：本行位于 GlobalAPIRateLimit / BodyLimit 之后（先限流后判定，被限流的请求
	// 不记黑名单命中，见 §8），并且**先于每条路由的 controller.Auth**。gin 的组级
	// 中间件必然跑在按路由注册的 handler 之前，这一点无法用组链改成「Auth 之后」；
	// 之所以可以放在 Auth 之前，是因为本次选了**路径豁免**而不是 isAdminUser 豁免
	// （见 middleware.BlacklistExemptPath），判定完全不读 c.Get("user")，
	// 因而不需要 owner 信息；反过来，在 Auth 之前拒绝还省掉了对恶意 IP 的
	// JWT 解析与用户查询开销。若将来改成 owner 维度（BL-3 手机号/微信）判定，
	// 必须把对应判定挂到各路由 Auth 之后。
	if mgr := manager.GetManager(); mgr != nil {
		// 启动时同步加载一次快照并起 60s 后台重载（多实例收敛）。
		service.EnsureDefaultBlacklistCacheStarted(mgr.GetDB())
		// 白名单同理：首次同步加载一次，之后 60s 重载。
		// 不加载的后果是 WhitelistIP() 永远拿到 nil 快照 → 按「零白名单」处理 →
		// 功能静默失效（判定仍走黑名单，看起来一切正常）。
		service.EnsureDefaultWhitelistCacheStarted(mgr.GetDB())
	}
	// ⚠️ 这里挂的是 WhitelistIP() 而**不是** BlacklistIP()。
	//
	// 两者不能并列挂：gin 的 c.Abort() 会把路由 handler 一起停掉，无法表达
	// 「命中白名单就跳过后面那个中间件但继续执行 handler」。所以白名单中间件
	// **内部持有**黑名单判定闭包 —— 命中白名单时根本不调用它（不写 blacklist_hits、
	// 不查地区归属、不碰聚合器），未命中时逐字交给黑名单，行为与只挂 BlacklistIP() 完全一致。
	//
	// 白名单本身没有开关：快照拿不到时按「零白名单」处理（不放行任何人），
	// 方向与黑名单的 fail-open 相反 —— 拿不到规则时凭空给出豁免是错的。
	apiRouter.Use(middleware.WhitelistIP())

	apiRouter.POST("/upload", controller.Auth, middleware.RequireAdmin, controller.UploadFile)
	// EXE uploads need a larger multipart envelope than ordinary API requests.
	scriptUploadRouter := router.Group("/api/script-versions")
	scriptUploadRouter.Use(middleware.GlobalAPIRateLimit, middleware.BodyLimit(65<<20))
	scriptUploadRouter.POST("/upload", controller.Auth, middleware.RequireAdmin, controller.UploadScriptVersion)
	scriptUploadRouter.POST("", controller.Auth, middleware.RequireAdmin, controller.UploadScriptVersion)
	// 用户脚本：版本由管理员上传，脚本实例按用户隔离；exchange 不把专用 key 放进 URL。
	apiRouter.GET("/script-versions", controller.Auth, controller.ScriptVersionList)
	apiRouter.GET("/script-domain-settings", controller.Auth, middleware.RequireAdmin, controller.ScriptDomainSettings)
	apiRouter.PUT("/script-domain-settings", controller.Auth, middleware.RequireAdmin, controller.UpdateScriptDomainSettings)
	apiRouter.GET("/script-escrow-policy", controller.Auth, controller.GetScriptEscrowPolicy)
	apiRouter.PUT("/script-escrow-policy", controller.Auth, middleware.RequireAdmin, controller.PutScriptEscrowPolicy)
	apiRouter.GET("/script-activation-code-price", controller.Auth, controller.GetScriptActivationCodePrice)
	apiRouter.PUT("/script-activation-code-price", controller.Auth, middleware.RequireAdmin, controller.PutScriptActivationCodePrice)
	apiRouter.POST("/script-activation-codes/purchase", controller.Auth, controller.PurchaseScriptActivationCode)
	apiRouter.POST("/script-activation-codes/redeem", controller.Auth, controller.RedeemScriptActivationCode)
	apiRouter.GET("/script-versions/:id/download", controller.Auth, controller.DownloadScriptVersion)
	apiRouter.GET("/scripts", controller.Auth, controller.ScriptList)
	apiRouter.POST("/scripts", controller.Auth, controller.CreateScript)
	apiRouter.PUT("/scripts/:id", controller.Auth, controller.UpdateScript)
	apiRouter.DELETE("/scripts/:id", controller.Auth, controller.DeleteScript)
	apiRouter.POST("/scripts/:id/exchange-token", controller.Auth, controller.IssueScriptExchangeToken)
	apiRouter.POST("/scripts/:id/token", controller.Auth, controller.IssueScriptExchangeToken)
	apiRouter.POST("/script-command/:shortCode/exchange", controller.ExchangeScriptToken)
	apiRouter.GET("/script-command/:shortCode/bootstrap", controller.ScriptBootstrap)
	apiRouter.GET("/script-command/:shortCode/exchange-script", controller.ScriptExchangeScript)
	apiRouter.POST("/script-command/:shortCode/activate", controller.ActivateScriptRunSession)
	apiRouter.POST("/script-command/:shortCode/heartbeat", controller.HeartbeatScriptRunSession)
	apiRouter.GET("/script-command/:shortCode/file", controller.ScriptBinaryDownload)
	apiRouter.GET("/scripts/:shortCode/bootstrap", controller.ScriptBootstrap)
	apiRouter.POST("/scripts/command/:shortCode/exchange", controller.ExchangeScriptToken)

	// UApiPro 归属地查询（管理员专用）：用户管理 / 日志列表的归属地展示。
	// 密钥从系统设置读取，不下发到前端，也不出现在 URL 里。
	apiRouter.GET("/uapi/phoneinfo", controller.Auth, middleware.RequireAdmin, controller.UAPIPhoneInfo)
	apiRouter.GET("/uapi/ipinfo", controller.Auth, middleware.RequireAdmin, controller.UAPIIPInfo)
	apiRouter.GET("/uapi/my-locations", controller.Auth, controller.UAPIMyLocations)
	apiRouter.GET("/location-cache", controller.Auth, middleware.RequireAdmin, controller.LocationCacheList)
	apiRouter.DELETE("/location-cache", controller.Auth, middleware.RequireAdmin, controller.LocationCacheDelete)
	apiRouter.POST("/location-cache/batch-delete", controller.Auth, middleware.RequireAdmin, controller.LocationCacheBatchDelete)
	apiRouter.POST("/location-cache/batch-refresh", controller.Auth, middleware.RequireAdmin, controller.LocationCacheBatchRefresh)
	apiRouter.POST("/location-cache/refresh", controller.Auth, middleware.RequireAdmin, controller.LocationCacheRefresh)
	apiRouter.POST("/location-cache/manual", controller.Auth, middleware.RequireAdmin, controller.LocationCacheManualLookup)
	// IP 段归属信息管理（task_doc/71 §3.5）。与 location-cache 的区别：
	// 后者是「查过这个键」的单 IP 缓存，前者是「这个网段属于谁」的段表，
	// 一次上游查询可覆盖整段，段内后续 IP 零调用命中。
	// 路径必须是复数 ip-segments，避免与单 IP 的 ip-location-cache 混淆。
	// 五条全部 Auth + RequireAdmin（列表也含原始段数据与样本 IP，不对普通用户开放）。
	apiRouter.GET("/admin/ip-segments", controller.Auth, middleware.RequireAdmin, controller.IPSegmentList)
	apiRouter.GET("/admin/ip-segments/lookup", controller.Auth, middleware.RequireAdmin, controller.IPSegmentLookup)
	apiRouter.POST("/admin/ip-segments/batch-delete", controller.Auth, middleware.RequireAdmin, controller.IPSegmentBatchDelete)
	apiRouter.DELETE("/admin/ip-segments/:id", controller.Auth, middleware.RequireAdmin, controller.IPSegmentDelete)
	apiRouter.POST("/admin/ip-segments/:id/refresh", controller.Auth, middleware.RequireAdmin, controller.IPSegmentRefresh)

	// 等级体系（Q75-3 / Q75-6 / Q75-7，94-*.md §2 C1）。
	//
	// 六条管理员接口一律 Auth + RequireAdmin：等级定义决定谁能拿到什么设备上限
	// （C3 会读 user_levels.device_limit），不能对普通用户开放（读也不行 ——
	// 列表里含门槛配置，等于把「怎么刷级」写出来）。
	//
	// 用户侧只有一条只读接口 /user/level（返回自己的等级与两个口径值），
	// 因为它非管理员专属，所以挂在 /user 前缀下、只要求 Auth。
	apiRouter.GET("/admin/user-levels", controller.Auth, middleware.RequireAdmin, controller.UserLevelList)
	apiRouter.POST("/admin/user-levels", controller.Auth, middleware.RequireAdmin, controller.UserLevelCreate)
	// reevaluate 必须注册在 /:id 之前？不需要 —— gin 按方法分树，
	// POST /admin/user-levels/reevaluate 与 PUT/DELETE /admin/user-levels/:id
	// 方法不同不会抢节点。
	apiRouter.POST("/admin/user-levels/reevaluate", controller.Auth, middleware.RequireAdmin, controller.UserLevelReevaluate)
	apiRouter.PUT("/admin/user-levels/:id", controller.Auth, middleware.RequireAdmin, controller.UserLevelUpdate)
	apiRouter.DELETE("/admin/user-levels/:id", controller.Auth, middleware.RequireAdmin, controller.UserLevelDelete)
	apiRouter.GET("/user/level", controller.Auth, controller.UserLevelCurrent)

	// 充值阶梯赠送的可配置档位（Q75-2，94-*.md §2 C2）。
	//
	// 表空时服务层回退到 DefaultRechargePromotionTiers，因此本接口先上线不影响发放。
	// 整体替换（PUT）而不是逐条增删改：档位是一张阶梯，「同门槛不同比例」的中间态
	// 在选档语义上不合法，必须让「校验通过」与「生效」是同一瞬间。
	apiRouter.GET("/admin/recharge-promotion-tiers", controller.Auth, middleware.RequireAdmin, controller.RechargePromotionTierList)
	apiRouter.PUT("/admin/recharge-promotion-tiers", controller.Auth, middleware.RequireAdmin, controller.RechargePromotionTierReplace)
	apiRouter.POST("/admin/recharge-promotion-tiers/preview", controller.Auth, middleware.RequireAdmin, controller.RechargePromotionTierPreview)

	apiRouter.GET("/uapi/search/engines", controller.Auth, middleware.RequireAdmin, controller.UAPISearchEngines)
	apiRouter.GET("/uapi/status/usage", controller.Auth, middleware.RequireAdmin, controller.UAPIStatusUsage)
	apiRouter.POST("/uapi/search/aggregate", controller.Auth, middleware.RequireAdmin, controller.UAPISearchAggregate)

	apiRouter.POST("/search", controller.SearchWithIdempotency)
	apiRouter.POST("/tiku/evidence", controller.AuthOrAccessToken, controller.UploadTikuAnswerEvidence)
	// Browser extension packages are publicly readable so native clients can
	// bootstrap before login; only administrators can force an upstream refresh.
	apiRouter.GET("/extensions/:source/latest", controller.ExtensionLatest)
	apiRouter.GET("/extensions/:source/download", controller.ExtensionDownload)
	apiRouter.POST("/extensions/:source/refresh", controller.Auth, middleware.RequireAdmin, controller.RefreshExtension)
	apiRouter.GET("/extensions/config", controller.Auth, middleware.RequireAdmin, controller.ExtensionConfiguration)
	apiRouter.PUT("/extensions/config", controller.Auth, middleware.RequireAdmin, controller.UpdateExtensionConfiguration)
	apiRouter.POST("/search/tasks", controller.AuthAccessToken, controller.CreateSearchTask)
	apiRouter.GET("/search/health", controller.AuthAccessToken, controller.GetSearchTaskHealth)
	apiRouter.GET("/search/status", controller.AuthAccessToken, controller.GetSearchTaskStatus)

	// 旧版本扩展兼容（2026-11-14 路径改名 /adapter-service → /api）。
	//
	// # 只保留这 4 条搜索接口，**不是**整组别名
	//
	// 已安装的浏览器扩展与原生宿主里写死了 `/adapter-service/search*` 四个路径
	// （`chaoxing_plus` 的 `tiku-adapter-config.ts:126/131/136/144`、
	// `adapter-account.c:297`），它们升级前无法改 URL。其余端点
	// （`ai-fallback` / `tiku/evidence` / `captcha` / `user/login`）没有做兼容，
	// 扩展侧同步升级到 `/api` 后即可，不做长期双挂。
	//
	// 中间件必须与主组一致：漏了 GlobalAPIRateLimit 会让旧路径成为无限流的后门，
	// 漏了 WhitelistIP/黑名单会让被拦的 IP 从旧路径进来。
	legacySearchRouter := router.Group("/adapter-service")
	legacySearchRouter.Use(
		middleware.GlobalAPIRateLimit,
		middleware.BodyLimit((24<<20)+65536),
		middleware.WhitelistIP(),
	)
	legacySearchRouter.POST("/search", controller.SearchWithIdempotency)
	legacySearchRouter.POST("/search/tasks", controller.AuthAccessToken, controller.CreateSearchTask)
	legacySearchRouter.GET("/search/health", controller.AuthAccessToken, controller.GetSearchTaskHealth)
	legacySearchRouter.GET("/search/status", controller.AuthAccessToken, controller.GetSearchTaskStatus)
	apiRouter.POST("/ai-fallback", controller.AuthOrAccessToken, controller.AIFallback)
	apiRouter.POST("/ai-fallback/tasks", controller.AuthOrAccessToken, controller.CreateAIFallbackTask)
	apiRouter.GET("/ai-fallback/status", controller.AuthOrAccessToken, controller.GetAIFallbackTaskStatus)
	apiRouter.POST("/parser", controller.Auth, middleware.RequireAdmin, controller.Parser)

	apiRouter.GET("/courses", controller.Auth, middleware.RequireAdmin, controller.Courses)
	apiRouter.POST("/questions/search", controller.Auth, middleware.RequireAdmin, controller.GetQuestions)
	apiRouter.POST("/questions", controller.Auth, middleware.RequireAdmin, controller.CreateQuestion)
	apiRouter.POST("/questions/import", controller.Auth, middleware.RequireAdmin, controller.ImportQuestions)
	apiRouter.PUT("/questions/:id", controller.Auth, middleware.RequireAdmin, controller.UpdateQuestions)
	apiRouter.DELETE("/questions/:id", controller.Auth, middleware.RequireAdmin, controller.DeleteQuestion)

	// 题库归档（Q77-1，见 77-*.md §11.3）：
	//   GET  /admin/tikus/archived       归档查看（唯一允许读出已归档题库的视图，
	//                                    实现里必须用 Unscoped/裸表查询，否则 total 恒 0）
	//   POST /admin/tikus/:id/restore    从归档恢复
	// 两条都挂 Auth + RequireAdmin：归档里可能含被下架的违规内容，
	// 不能让普通账号通过该视图绕过「已删除」的事实。
	apiRouter.GET("/admin/tikus/archived", controller.Auth, middleware.RequireAdmin, controller.ListArchivedTikus)
	apiRouter.POST("/admin/tikus/:id/restore", controller.Auth, middleware.RequireAdmin, controller.RestoreArchivedTiku)

	apiRouter.POST("/user/login", controller.UserLogin)        // 登录接口
	apiRouter.POST("/user/login/sms", controller.UserSMSLogin) // 短信登录接口
	// 管理员专用登录入口（45-*.md §0.1.2 Q63 定稿）。
	//
	// 为什么需要它：按 Q63，普通登录现在**会被 IP 黑名单拦** —— 被拉黑 IP 打开网页
	// 只能看到拉黑页面。若没有这个入口，管理员一旦把自己的 CIDR 拉黑就再也进不来。
	//
	// 三条约束（详见 controller.AdminLogin 的注释）：
	//   1. 仅账号密码 + 强制人机验证（不提供短信/微信，否则「要登录先发短信、
	//      发短信被拦」形成死锁）
	//   2. 只放行管理员（否则它就成了绕过黑名单的通用登录入口）
	//   3. 失败锁定（该路径豁免黑名单，是唯一暴露面，必须自带防爆破）
	//
	// 该路径已加入 middleware.BlacklistExemptPath 的豁免表；连同人机验证的
	// /public/captcha/* 一起豁免 —— 否则管理员会卡在验证码上。
	apiRouter.POST("/admin-login", controller.AdminLogin)
	// 登录锁定管理（管理端「解除锁定」）。失败锁定本身是 DoS 面：
	// 攻击者知道路径/用户名就能故意失败把真人挡在门外，必须给运营兜底出口。
	// 两套限流各自可解（45-*.md §0.1.4 五-8：「解除账号锁定」与「解除 IP 锁定」两个操作）。
	apiRouter.GET("/admin/login-lockouts", controller.Auth, middleware.RequireAdmin, controller.LoginLockoutList)
	apiRouter.DELETE("/admin/login-lockout/:scope/:key", controller.Auth, middleware.RequireAdmin, controller.ClearLoginLockout)
	apiRouter.POST("/user/logout", controller.UserLogout)     // 清理浏览器会话
	apiRouter.POST("/user/register", controller.UserRegister) // 公开注册接口
	apiRouter.POST("/public/captcha/challenge", controller.CreateCaptchaChallenge)
	apiRouter.POST("/public/captcha/verify", controller.VerifyCaptchaChallenge)
	apiRouter.Any("/public/captcha/remote-gen", controller.ProxyRemoteCaptchaGenerate)
	apiRouter.POST("/public/captcha/remote-check", controller.ProxyRemoteCaptchaCheck)
	apiRouter.POST("/public/captcha/remote-token", controller.VerifyRemoteCaptcha)
	apiRouter.POST("/public/sms-code", controller.SendPublicSMSCode) // 公开短信验证码接口
	apiRouter.GET("/public/register-status", controller.PublicRegisterStatus)
	apiRouter.POST("/public/wechat-register/session", controller.PublicWechatRegisterSession)
	apiRouter.POST("/public/wechat-register/qrcode", controller.PublicWechatRegisterQRCode)
	apiRouter.GET("/public/wechat-register/status", controller.PublicWechatRegisterStatus)
	apiRouter.POST("/public/wechat-register/code", controller.PublicWechatRegisterCode)
	apiRouter.POST("/public/wechat-register/release", controller.PublicWechatRegisterRelease)
	apiRouter.POST("/public/wechat-login/qrcode", controller.CreateWechatLoginQRCode)
	apiRouter.GET("/public/wechat-login/status", controller.CheckWechatLoginStatus)
	apiRouter.GET("/public/wechat-login/ws", controller.WechatLoginWebSocket)
	apiRouter.POST("/public/wechat-login/code", controller.LoginWechatByCode)
	apiRouter.POST("/public/wechat-login/bind", controller.BindWechatLogin)
	apiRouter.GET("/public/wechat/user", controller.LegacyWechatUserByCode)
	apiRouter.GET("/user/info", controller.Auth, controller.UserInfo) // 当前登录用户
	apiRouter.GET("/user/session", controller.Auth, controller.UserSession)
	apiRouter.PUT("/user/profile", controller.Auth, controller.UpdateCurrentUserProfile)
	apiRouter.POST("/user/phone/sms-code", controller.Auth, controller.SendUserPhoneChangeSMSCode)
	apiRouter.PUT("/user/phone", controller.Auth, controller.UpdateCurrentUserPhone)
	apiRouter.POST("/user/wechat/bind", controller.Auth, controller.BindCurrentUserWechat)
	apiRouter.POST("/user/wechat/bind-code", controller.Auth, controller.BindCurrentUserWechatByCode)
	apiRouter.DELETE("/user/wechat", controller.Auth, controller.UnbindCurrentUserWechat)
	apiRouter.GET("/user/wechat/scan-id", controller.Auth, middleware.RequireAdmin, controller.ResolveAdminWechatIDByScan)
	apiRouter.POST("/user/wechat/code-id", controller.Auth, middleware.RequireAdmin, controller.ResolveAdminWechatIDByCode)
	apiRouter.GET("/user", controller.Auth, controller.UserList)       // 自己创建的用户列表
	apiRouter.POST("/user", controller.Auth, controller.CreateUser)    // 创建用户
	apiRouter.PUT("/user/:id", controller.Auth, controller.UpdateUser) // 更新用户
	apiRouter.POST("/user/:id/balance", controller.Auth, middleware.RequireAdmin, controller.UpdateUserBalance)
	apiRouter.PUT("/user/:id/reset-password", controller.Auth, controller.ResetUserPassword) // 重置密码
	apiRouter.DELETE("/user/:id", controller.Auth, controller.DeleteUser)                    // 删除用户（归档，task_doc/76 §3.2）

	// 绑定历史（task_doc/70 §3.6 / §3.7）。管理员专属，四条全部 Auth + RequireAdmin。
	//
	// 三个「读」入口是需求 5a/6 的分工：
	//   - /admin/users/:id/bindings 单用户弹窗（用户管理操作列「查看」）；
	//   - /admin/bindings?kind= 侧边栏两个页面共用的列表（70 §3.6 明确不写两份）；
	//   - kind 只允许 wechat / phone，服务层做白名单校验，不接受任意字符串。
	//
	// 两个「写」入口是需求 7：作废（state='voided'）与手工补录。
	// ⚠️ 作废会同时把 active_key 置 NULL —— 只改 state 会让该行继续占唯一索引，
	// 表现为「别人再绑同值时报唯一约束冲突」，而报错文案与手机号毫无字面关系。
	apiRouter.GET("/admin/users/:id/bindings", controller.Auth, middleware.RequireAdmin, controller.GetAdminUserBindings)
	apiRouter.GET("/admin/bindings", controller.Auth, middleware.RequireAdmin, controller.ListAdminBindings)
	apiRouter.POST("/admin/bindings", controller.Auth, middleware.RequireAdmin, controller.CreateAdminBindingHistory)
	apiRouter.POST("/admin/bindings/:id/void", controller.Auth, middleware.RequireAdmin, controller.VoidAdminBinding)

	// 注销申请（task_doc/76 §3.3.5）。三条都是**用户本人**接口，Auth 即可：
	// 目标账号一律取「当前登录用户」，不接受前端传 userID（否则可替别人申请注销）。
	// 申请/撤销是写操作，handler 内复用登录失败计数做限流（防反复试探密码）。
	apiRouter.POST("/user/closure-request", controller.Auth, controller.ClosureRequest)
	apiRouter.POST("/user/closure-cancel", controller.Auth, controller.ClosureCancel)
	apiRouter.GET("/user/closure-status", controller.Auth, controller.ClosureStatus)

	// 已归档账号管理（task_doc/76 §3.2.3）。管理员专属：
	// 列表/详情/恢复三条都要 Auth + RequireAdmin，恢复还会二次确认用户名冲突。
	// ⚠️ handler 内部一律用 Unscoped 查（users.deleted_at 是 GORM 软删除列，
	// 默认查询会把这些行过滤掉，列表永远是空的）。
	apiRouter.GET("/admin/archived-users", controller.Auth, middleware.RequireAdmin, controller.ArchivedUserList)
	apiRouter.GET("/admin/archived-users/:id", controller.Auth, middleware.RequireAdmin, controller.ArchivedUserDetail)
	apiRouter.POST("/admin/archived-users/:id/restore", controller.Auth, middleware.RequireAdmin, controller.ArchivedUserRestore)

	apiRouter.GET("/tickets", controller.Auth, controller.TicketList)
	apiRouter.POST("/tickets", controller.Auth, controller.CreateTicket)
	apiRouter.GET("/tickets/config", controller.Auth, middleware.RequireAdmin, controller.TicketWecomSettings)
	apiRouter.PUT("/tickets/config", controller.Auth, middleware.RequireAdmin, controller.UpdateTicketWecomSettings)
	apiRouter.POST("/tickets/config/test", controller.Auth, middleware.RequireAdmin, controller.TestTicketWecomWebhook)
	apiRouter.GET("/tickets/:id", controller.Auth, controller.TicketDetail)
	apiRouter.POST("/tickets/:id/messages", controller.Auth, controller.AddTicketMessage)
	apiRouter.PUT("/tickets/:id", controller.Auth, middleware.RequireAdmin, controller.UpdateTicket)
	// 工单图片附件：先上传（本地校验 + 加密落库 + 标记待检测），再异步做敏感检测。
	// 用户端读图只拿得到「非敏感」的字节；敏感图返回占位状态（403 + 明确 code）。
	// 管理端读图单独一条路由，必须 RequireAdmin，且 handler 内写账户审计。
	// 上传的 ticketId 走 query：不带 = 建单前上传的草稿图（建单时认领）。
	//
	// 单请求体上限 = 单张图上限（20 MiB，service.MaxTicketAttachmentBytes）+ multipart 信封余量。
	// 加余量而不是正好相等：boundary 与 part 头部约几百字节，正好相等会让「恰好 20 MiB 的图」
	// 在 MaxBytesReader 上先被截断，报错发生在解析而非校验，文案与真实原因无关。
	apiRouter.POST("/ticket-attachments", middleware.BodyLimit((20<<20)+65536), controller.Auth, controller.UploadTicketAttachment)
	apiRouter.GET("/tickets/:id/attachments", controller.Auth, controller.ListTicketAttachments)
	apiRouter.GET("/ticket-attachments/:attachmentId", controller.Auth, controller.DownloadTicketAttachment)
	apiRouter.GET("/ticket-attachments/:attachmentId/admin", controller.Auth, middleware.RequireAdmin, controller.AdminDownloadTicketAttachment)
	apiRouter.POST("/tickets/:id/notifications/:notificationId/retry", controller.Auth, middleware.RequireAdmin, controller.RetryTicketNotification)
	apiRouter.GET("/ticket-refunds/eligible-orders", controller.Auth, controller.EligibleTicketRefundOrders)
	apiRouter.GET("/ticket-refunds", controller.Auth, controller.TicketRefundList)
	apiRouter.POST("/ticket-refunds", controller.Auth, controller.CreateTicketRefund)
	apiRouter.GET("/ticket-refunds/:id", controller.Auth, controller.TicketRefundDetail)
	apiRouter.POST("/ticket-refunds/:id/decision", controller.Auth, middleware.RequireAdmin, controller.ReviewTicketRefund)
	apiRouter.POST("/ticket-refunds/:id/process", controller.Auth, middleware.RequireAdmin, controller.ProcessTicketRefund)
	apiRouter.POST("/ticket-refunds/:id/reconcile", controller.Auth, middleware.RequireAdmin, controller.ReconcileManualTicketRefund)
	apiRouter.POST("/ticket-refunds/:id/proof", middleware.BodyLimit((2<<20)+65536), controller.Auth, controller.UploadTicketRefundProof)
	apiRouter.GET("/ticket-refunds/:id/proof", controller.Auth, controller.DownloadTicketRefundProof)

	apiRouter.GET("/logs", controller.Auth, controller.LogList) // 日志列表
	// 敏感原文：单独接口 + 显式管理员校验。列表接口对非管理员只下发遮挡文本，
	// 原文只能从这里取（需求：「管理员有按钮可以显示原文」）。
	apiRouter.GET("/logs/:id/sensitive-original", controller.Auth, middleware.RequireAdmin, controller.LogSensitiveOriginal)
	apiRouter.GET("/billing-records", controller.Auth, controller.BillingRecordList)
	apiRouter.PUT("/billing-records/:id/statement-visibility", controller.Auth, middleware.RequireAdmin, controller.UpdateBillingStatementVisibility)
	apiRouter.GET("/recharge-orders", controller.Auth, controller.RechargeOrderList)
	apiRouter.POST("/recharge-orders", controller.Auth, controller.CreateRechargeOrder)
	apiRouter.Any("/payment/notify", controller.RechargePaymentNotify)
	// 邀请码与充值返佣。本人只能看自己的汇总（handler 内固定用当前账户），
	// 返佣列表与结算一律管理员（Auth + RequireAdmin）。
	apiRouter.GET("/user/referral", controller.Auth, controller.ReferralSummary)
	apiRouter.GET("/admin/referral-rebates", controller.Auth, middleware.RequireAdmin, controller.ReferralRebateList)
	apiRouter.POST("/admin/referral-rebates/:id/settle", controller.Auth, middleware.RequireAdmin, controller.SettleReferralRebate)
	// 管理员手动补录返佣（无需充值订单的一笔），与结算同为管理员权限。
	apiRouter.POST("/admin/referral-rebates", controller.Auth, middleware.RequireAdmin, controller.CreateReferralRebate)
	// 赠送记录：注册赠送 / 评价返现 / 邀请返佣 / 充值赠金 四类聚合账本（Q75-1：仅管理员）。
	apiRouter.GET("/admin/gift-records", controller.Auth, middleware.RequireAdmin, controller.GiftRecordList)
	apiRouter.GET("/admin/gift-records/sources", controller.Auth, middleware.RequireAdmin, controller.GiftRecordSourceList)
	// 评价返现（16-*.md §1.8）。用户端只碰自己的数据（handler 内固定用当前账户），
	// 管理端一律 Auth + RequireAdmin；开关默认关闭，关闭时入口返回 400。
	apiRouter.GET("/user/review-rebate/program", controller.Auth, controller.ReviewRebateProgram)
	apiRouter.POST("/user/review-rebate/claims", controller.Auth, controller.CreateReviewRebateClaim)
	apiRouter.GET("/user/review-rebate/claims", controller.Auth, controller.MyReviewRebateClaims)
	apiRouter.GET("/admin/review-rebate/programs", controller.Auth, middleware.RequireAdmin, controller.AdminReviewRebatePrograms)
	apiRouter.PUT("/admin/review-rebate/programs/:id", controller.Auth, middleware.RequireAdmin, controller.AdminUpdateReviewRebateProgram)
	apiRouter.GET("/admin/review-rebate/rules", controller.Auth, middleware.RequireAdmin, controller.AdminReviewRebateRules)
	apiRouter.POST("/admin/review-rebate/rules", controller.Auth, middleware.RequireAdmin, controller.AdminCreateReviewRebateRule)
	apiRouter.PUT("/admin/review-rebate/rules/:id", controller.Auth, middleware.RequireAdmin, controller.AdminUpdateReviewRebateRule)
	apiRouter.DELETE("/admin/review-rebate/rules/:id", controller.Auth, middleware.RequireAdmin, controller.AdminDeleteReviewRebateRule)
	apiRouter.GET("/admin/review-rebate/claims", controller.Auth, middleware.RequireAdmin, controller.AdminReviewRebateClaims)
	apiRouter.POST("/admin/review-rebate/claims/:id/decision", controller.Auth, middleware.RequireAdmin, controller.AdminDecideReviewRebateClaim)
	// Agent 打分重跑（占位打分器，不真的调模型）：只写 claim，不发放。
	apiRouter.POST("/admin/review-rebate/claims/:id/score", controller.Auth, middleware.RequireAdmin, controller.AdminScoreReviewRebateClaim)
	// 截图回显：截图落在私有目录、没有可直连 URL，管理员复核必须经这个鉴权入口取图。
	apiRouter.GET("/admin/review-rebate/claims/:id/screenshot", controller.Auth, middleware.RequireAdmin, controller.AdminReviewRebateScreenshot)
	apiRouter.GET("/access-tokens", controller.Auth, controller.AccessTokenList)
	apiRouter.POST("/access-tokens", controller.Auth, controller.CreateAccessToken)
	apiRouter.PUT("/access-tokens/:id", controller.Auth, controller.UpdateAccessToken)
	apiRouter.DELETE("/access-tokens/:id", controller.Auth, controller.DeleteAccessToken)
	// 额度周期重置（P14）：只追加一条 epoch 叠加层，不清零 total_amount_used、不动钱包。
	// 管理员接口必须同时挂 Auth 与 RequireAdmin。
	apiRouter.POST("/access-tokens/:id/reset-quota-window", controller.Auth, middleware.RequireAdmin, controller.ResetAccessTokenQuotaWindow)
	apiRouter.GET("/payment-methods", controller.Auth, controller.PaymentMethodList)
	apiRouter.POST("/payment-methods", controller.Auth, controller.CreatePaymentMethod)
	apiRouter.PUT("/payment-methods/:id", controller.Auth, controller.UpdatePaymentMethod)
	apiRouter.DELETE("/payment-methods/:id", controller.Auth, controller.DeletePaymentMethod)
	apiRouter.GET("/search-account-configs", controller.Auth, middleware.RequireAdmin, controller.SearchAccountConfigList)
	apiRouter.PUT("/search-account-configs/:provider", controller.Auth, middleware.RequireAdmin, controller.UpdateSearchAccountConfig)
	apiRouter.GET("/ai-search-config", controller.Auth, middleware.RequireAdmin, controller.AISearchConfigDetail)
	apiRouter.GET("/ai-search-config/models", controller.Auth, middleware.RequireAdmin, controller.AISearchModels)
	apiRouter.GET("/ai-search-config/pricing-status", controller.Auth, middleware.RequireAdmin, controller.AISearchConfigPricingStatus)
	apiRouter.GET("/ai-search-config/ocr-cache", controller.Auth, middleware.RequireAdmin, controller.AIOCRCacheStatus)
	apiRouter.GET("/ai-search-config/ocr-cache/entries", controller.Auth, middleware.RequireAdmin, controller.AIOCRCacheList)
	apiRouter.GET("/ai-search-config/ocr-cache/entries/:url_hash/image", controller.Auth, middleware.RequireAdmin, controller.AIOCRCacheImage)
	apiRouter.POST("/ai-search-config/ocr-cache/batch-delete", controller.Auth, middleware.RequireAdmin, controller.AIOCRCacheBatchDelete)
	apiRouter.DELETE("/ai-search-config/ocr-cache", controller.Auth, middleware.RequireAdmin, controller.ClearAIOCRCache)
	apiRouter.POST("/ai-search-config/test", controller.Auth, controller.TestAISearchConfig)
	apiRouter.POST("/ai-search-config/tool-test", controller.Auth, middleware.RequireAdmin, controller.TestAIToolCall)
	apiRouter.PUT("/ai-search-config", controller.Auth, middleware.RequireAdmin, controller.UpdateAISearchConfig)
	// 模型绑定（ai-model-architectures）已移除：模型列表由 AI 搜索配置维护，
	// 计费由模型价格表按「模型 ID / 显示名称」匹配。
	apiRouter.GET("/ai-model-prices", controller.Auth, controller.AIModelPriceList)
	apiRouter.POST("/ai-model-prices", controller.Auth, middleware.RequireAdmin, controller.CreateAIModelPrice)
	// 静态路径必须注册在 :id 之前，避免 gin 路由冲突。
	apiRouter.POST("/ai-model-prices/reset", controller.Auth, middleware.RequireAdmin, controller.ResetAIModelPriceDefaults)
	apiRouter.PUT("/ai-model-prices/:id", controller.Auth, middleware.RequireAdmin, controller.UpdateAIModelPrice)
	apiRouter.DELETE("/ai-model-prices/:id", controller.Auth, middleware.RequireAdmin, controller.DeleteAIModelPrice)
	apiRouter.GET("/system-metrics", controller.Auth, middleware.RequireAdmin, controller.GetSystemMetrics)
	apiRouter.GET("/system-settings", controller.Auth, middleware.RequireAdmin, controller.SystemSettingDetail)
	apiRouter.PUT("/system-settings", controller.Auth, middleware.RequireAdmin, controller.UpdateSystemSetting)

	// 黑名单管理页（BL-5，46-黑名单设计.md §6）。全部 Auth + RequireAdmin：
	// 规则原文（pattern）是「攻击者换 IP 的边界」，只有管理员能看。
	//
	// ⚠️ 为什么这五条**单独用一个 group**、而不是挂在 apiRouter 上：
	// apiRouter 上有 middleware.BlacklistIP()，而这些接口恰恰必须是「被拉黑也能打通的」——
	// 管理员误把自己所在 CIDR 拉黑后，他仍要能打开管理页把规则停用，
	// 否则就是 46-*.md §3.3 的自锁（页面能进、接口 403，比完全进不去更难排查）。
	//
	// 豁免做成**结构性**的（这一组不挂 BlacklistIP），而不是往 BlacklistExemptPath
	// 里加字符串：豁免表的变化只影响这一处挂载点，读 router.go 就能看到全貌，
	// 也不会与同时在改 middleware/blacklist.go 的分支冲突。
	// 代价是这里必须显式重挂 GlobalAPIRateLimit 与 BodyLimit —— 不能因为「反正管理员用」
	// 就省掉限流，那会让这一组成为无速率上限的鉴权暴力面。
	//
	// 方法/路径不冲突：:id 只出现在 PUT /blacklist/:id/enabled 与 DELETE /blacklist/:id，
	// GET /blacklist/hits 是 GET 树上的独立静态路径，gin 按方法分树，不会互相抢占。
	blacklistAdminRouter := router.Group("/api")
	blacklistAdminRouter.Use(middleware.GlobalAPIRateLimit, middleware.BodyLimit(16<<20))
	blacklistAdminRouter.GET("/blacklist", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryList)
	blacklistAdminRouter.POST("/blacklist", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryCreate)
	blacklistAdminRouter.PUT("/blacklist/:id/enabled", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryToggle)
	blacklistAdminRouter.DELETE("/blacklist/:id", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryDelete)
	blacklistAdminRouter.GET("/blacklist/hits", controller.Auth, middleware.RequireAdmin, controller.BlacklistHitList)

	// 白名单管理（T-04 / T-05）。与黑名单**同一套结构**：单独 group、不挂 WhitelistIP/BlacklistIP。
	//
	// 理由与黑名单组完全相同 —— 被误封的管理员必须能打开白名单页把自己加进去自救。
	// 若这一组也挂 IP 判定，就变成「只有已经在白名单里的人才能编辑白名单」，
	// 那是比黑名单更硬的死锁：白名单没有开关，没有任何旁路。
	//
	// 写操作之后 handler 内部会调 service.ReloadDefaultWhitelistCache，
	// 否则会「页面显示已启用、中间件还按旧快照判定」。
	blacklistAdminRouter.GET("/whitelist", controller.Auth, middleware.RequireAdmin, controller.WhitelistEntryList)
	blacklistAdminRouter.POST("/whitelist", controller.Auth, middleware.RequireAdmin, controller.WhitelistEntryCreate)
	blacklistAdminRouter.PUT("/whitelist/:id/enabled", controller.Auth, middleware.RequireAdmin, controller.WhitelistEntryToggle)
	blacklistAdminRouter.DELETE("/whitelist/:id", controller.Auth, middleware.RequireAdmin, controller.WhitelistEntryDelete)
	blacklistAdminRouter.GET("/whitelist/exempt-ips", controller.Auth, middleware.RequireAdmin, controller.WhitelistExemptIPsGet)
	blacklistAdminRouter.PUT("/whitelist/exempt-ips", controller.Auth, middleware.RequireAdmin, controller.WhitelistExemptIPsPut)

	// 账号封禁管理（121 §4.8 / 121 决策 6）。
	//
	// ⚠️ 挂在这个「不挂 IP 判定」的管理员组上，而不是 apiRouter —— 理由与黑名单
	// **完全相同、但更硬**：
	//
	//	apiRouter 上挂了 middleware.WhitelistIP()（内部持有黑名单判定闭包）。
	//	封禁是**按账号**的，而被拦截是按 IP 的，两者本可以各自独立；
	//	但一旦把封禁页也挂到 apiRouter，就会出现这样的死锁：
	//	某个管理员所在的 IP 段被误加入黑名单 → 他连封禁页都打不开 →
	//	无法给自己解封，也无法解封别人 → 只能等另一个「没被封的」管理员来救。
	//	如果全站只有他一个管理员，那就是**永久锁死**。
	//
	//	挂在不带 IP 判定的组上，这一点变成**结构性保证**：读 router.go 就能看到
	//	「这一组不挂 IP 判定」，而不是依赖 middleware.BlacklistExemptPath 里
	//	某个字符串常量恰好包含了这条路径（那种豁免很容易在下一次改路径时悄悄失效）。
	//
	// 代价与黑名单组相同：必须显式重挂 GlobalAPIRateLimit 与 BodyLimit，
	// 不能因为「反正管理员用」就省掉限流。
	//
	// 方法/路径不冲突：`/admin/user-bans` 与 `/blacklist`、`/whitelist` 是不同静态前缀；
	// DELETE 的 `:userId` 只出现在 DELETE 树上，gin 按方法分树，不会与 GET 抢占。
	blacklistAdminRouter.GET("/admin/user-bans", controller.Auth, middleware.RequireAdmin, controller.UserBanAdminList)
	blacklistAdminRouter.POST("/admin/user-bans", controller.Auth, middleware.RequireAdmin, controller.UserBanAdminCreate)
	// 解封路径用 :userId（不是 user_bans 主键 id）：service.Unban 的语义是
	// 「解除这个用户的封禁」（处理该用户全部未到期行），不是「作废某一条记录」。
	// 按记录 id 解封会让调用方误以为只解了一条，且传错 id 会解封到另一个账号。
	blacklistAdminRouter.DELETE("/admin/user-bans/:userId", controller.Auth, middleware.RequireAdmin, controller.UserBanAdminRevoke)

	// 考试客户端本地库工具：仅管理员。服务端不解密，只发工具 + 只读预览明文库。
	apiRouter.GET("/admin/userexam/tool/version", controller.Auth, middleware.RequireAdmin, controller.UserExamToolVersion)
	apiRouter.GET("/admin/userexam/tool", controller.Auth, middleware.RequireAdmin, controller.UserExamToolDownload)
	apiRouter.POST("/admin/userexam/inspect", middleware.BodyLimit(controller.MaxUserExamInspectUploadBytes), controller.Auth, middleware.RequireAdmin, controller.UserExamInspect)

	// 管理员账号安全状态：仅管理员，只读，只回布尔。
	//
	// 存在原因（68-默认管理员密码处理设定.md）：库里仍是默认密码 123456 时
	// **不再拒绝启动**（那会造成「进不去后台改密码」的死锁），
	// 改由本接口把事实告诉前端，让管理台显示告警引导管理员自行修改。
	// 一条不可协商的性质：只回布尔，不回任何凭据内容（含 hash）。
	apiRouter.GET("/admin/security-status", controller.Auth, middleware.RequireAdmin, controller.AdminSecurityStatus)

	// ── 第 8 轮落地模块的管理端接线 ────────────────────────────────────────────
	//
	// 这一整段都是「handler 已实现、service 已有测试、只差把路由挂上」的模块。
	// 不挂的后果分两类，都很隐蔽：
	//   · 页面能打开但所有请求 404（风控 / 画像 / 敏感词 / 图片缓存 / 白名单）；
	//   · 后台 goroutine 起了却没人能配置它（日志清理 / 工单图片清理）。
	// 本项目此前已有 33 个 handler 处于「实现了但未挂」的状态，所以这一段刻意集中
	// 放一起并逐条注明数据源，便于下次核对「到底还有没有漏的」。

	// 公告管理（N4，112 §4.3）。五条全部 Auth + RequireAdmin。
	// 管理员可发故障公告；公告本身通过搜题响应下发（见 controller/search.go 的 attachSearchAnnouncements），
	// 因此这里只有写入口 —— 用户端不存在「拉公告列表」的接口，避免多一份口径。
	apiRouter.GET("/admin/announcements", controller.Auth, middleware.RequireAdmin, controller.AnnouncementAdminList)
	apiRouter.POST("/admin/announcements", controller.Auth, middleware.RequireAdmin, controller.AnnouncementAdminCreate)
	apiRouter.PUT("/admin/announcements/:id", controller.Auth, middleware.RequireAdmin, controller.AnnouncementAdminUpdate)
	apiRouter.PUT("/admin/announcements/:id/enabled", controller.Auth, middleware.RequireAdmin, controller.AnnouncementAdminToggle)
	apiRouter.DELETE("/admin/announcements/:id", controller.Auth, middleware.RequireAdmin, controller.AnnouncementAdminDelete)

	// 风控告警（96 / 118 §2）。**只读告警**：列表与详情没有任何写路径，
	// 处置动作一律走黑名单/封禁自己的接口，人工再确认一次（96 C1）。
	// 路径用 /risk/* 而不是 /admin/risk/*：前端 api/RiskAdminApi.js 的 RISK_BASE
	// 就是 '/adapter-service/risk'，改名会让已写好的页面全部 404。
	apiRouter.GET("/risk/alerts", controller.Auth, middleware.RequireAdmin, controller.RiskWarningList)
	apiRouter.GET("/risk/alerts/:id", controller.Auth, middleware.RequireAdmin, controller.RiskWarningDetail)
	apiRouter.GET("/risk/webhook", controller.Auth, middleware.RequireAdmin, controller.RiskWecomSettings)
	apiRouter.PUT("/risk/webhook", controller.Auth, middleware.RequireAdmin, controller.UpdateRiskWecomSettings)
	apiRouter.POST("/risk/webhook/test", controller.Auth, middleware.RequireAdmin, controller.TestRiskWecomWebhook)
	// 风控阈值读写。此前 `risk_rule_<ruleId>_threshold` 这些键**没有任何写入路径**，
	// 而 riskRuleThreshold 的三层兜底第一层读的就是它们 ⇒ 那一层永远读不到值。
	// 路由必须挂上，否则又是一批「handler 已实现但 router 未接线」的死代码。
	// `:key` 在 service 层走白名单校验，不接受任意 system_settings 键。
	apiRouter.GET("/risk/thresholds", controller.Auth, middleware.RequireAdmin, controller.RiskThresholdList)
	apiRouter.PUT("/risk/thresholds/:key", controller.Auth, middleware.RequireAdmin, controller.UpdateRiskThreshold)
	apiRouter.DELETE("/risk/thresholds/:key", controller.Auth, middleware.RequireAdmin, controller.ResetRiskThreshold)
	// 规则的启用/停用（risk_rules.enabled）。此前这一列同样没有写接口。
	// 接线点：EvaluateDebtAlerts 的 enabledRules 过滤 `if !rule.Enabled { continue }`。
	apiRouter.GET("/risk/rules", controller.Auth, middleware.RequireAdmin, controller.RiskRuleStateList)
	apiRouter.PUT("/risk/rules/:ruleId/enabled", controller.Auth, middleware.RequireAdmin, controller.SetRiskRuleEnabled)

	// 用户画像（98 §0 四层权限的第 ① 层）。第 ② 层在 handler 内的 requirePortraitAdmin。
	// 两条入口共用同一个响应结构，前端只写一份渲染。
	apiRouter.GET("/admin/portrait/overview", controller.Auth, middleware.RequireAdmin, controller.AdminUserPortraitOverview)
	apiRouter.GET("/admin/users/:id/portrait", controller.Auth, middleware.RequireAdmin, controller.AdminUserPortrait)

	// 敏感图片检测管理页（Q1）。手动触发**不经过额度闸门** —— 复核是运营动作。
	apiRouter.GET("/sensitive-image/entries", controller.Auth, middleware.RequireAdmin, controller.SensitiveImageEntryList)
	apiRouter.GET("/sensitive-image/quota", controller.Auth, middleware.RequireAdmin, controller.SensitiveImageQuotaStatus)
	apiRouter.POST("/sensitive-image/quota", controller.Auth, middleware.RequireAdmin, controller.SensitiveImageQuotaUpdateHandler)
	apiRouter.POST("/sensitive-image/detect", controller.Auth, middleware.RequireAdmin, controller.SensitiveImageManualDetect)

	// 敏感词检测记录（T-27）。只读三态筛选，不写、不清空、不重试缓存行。
	apiRouter.GET("/admin/sensitive-word-cache", controller.Auth, middleware.RequireAdmin, controller.SensitiveWordCacheList)
	apiRouter.GET("/admin/sensitive-word-cache/status", controller.Auth, middleware.RequireAdmin, controller.SensitiveWordCacheStatus)

	// 工单图片缓存管理（T-12）。删除受 ref_count 保护：仍被引用的 blob 拒绝删除。
	apiRouter.GET("/admin/ticket-image-cache", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheList)
	apiRouter.DELETE("/admin/ticket-image-cache/:sha256", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheDelete)
	apiRouter.POST("/admin/ticket-image-cache/batch-delete", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheBatchDelete)
	apiRouter.POST("/admin/ticket-image-cache/clean", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheCleanNow)

	// 日志保留策略（Q14）。三条：读现状 / 改配置 / 立即清理一次。
	// GET 用的是**无副作用**的 DescribeLogRetentionCapability —— 会被人手刷新、
	// 浏览器预取、监控探针调用，若它顺手清理日志，一次 GET 就等于删审计日志。
	apiRouter.GET("/admin/log-settings", controller.Auth, middleware.RequireAdmin, controller.LogRetentionSettingsDetail)
	apiRouter.PUT("/admin/log-settings", controller.Auth, middleware.RequireAdmin, controller.UpdateLogRetentionSettings)
	apiRouter.POST("/admin/log-settings/clean", controller.Auth, middleware.RequireAdmin, controller.CleanLogRetentionNow)

	// 服务状态（T-18）。纯内存环形缓冲，1440 槽，重启归零。
	apiRouter.GET("/admin/service-status", controller.Auth, middleware.RequireAdmin, controller.GetServiceStatus)
	// 非管理员的服务健康**摘要**（PLUS 面板的状态 Tab 用这个）。
	//
	// 为什么不直接放宽上面那条：那条返回逐分钟 points，能反推出「什么时段有多少请求」，
	// 属运维可见信息（见 controller.GetServiceStatus 的注释）。本条**根本不返回 points**，
	// 只给「现在好不好」——前端想画明细也拿不到数据。
	//
	// 鉴权用 AuthOrAccessToken 而不是 Auth：PLUS 只持 adapter key（access_tokens 表里
	// 存的是 sha256(token) 的 hex，不是 JWT），走 Auth 会拿到 401「请重新登录」，
	// 表现为状态页恒显「暂无采样数据」——既不报错也不显示无权限，是那种「看起来在工作、
	// 实际一次没成功」的形态。
	apiRouter.GET("/service-status/summary", controller.AuthOrAccessToken, controller.GetServiceStatusSummary)

	// 工单附件删除（T-13 的配套入口）。删除与 blob ref_count 递减在同一事务里，
	// 漏挂这条路由会让「上传能传、删不掉」—— 用户只能看着自己的图片占着配额。
	apiRouter.DELETE("/ticket-attachments/:attachmentId", controller.Auth, controller.DeleteTicketAttachment)

	// 异步任务导入导出（Q12 / N3）。七个 handler 全部 RequireAdmin。
	// 收尾判定必须处理「一个任务都没执行」：提交在未注入 manager 时返回 503，
	// 而不是假装成功（见 controller/async_task.go:199 的注释）。
	apiRouter.POST("/export/tasks", controller.Auth, middleware.RequireAdmin, controller.ExportTaskCreate)
	apiRouter.POST("/import/tasks", controller.Auth, middleware.RequireAdmin, controller.ImportTaskCreate)
	apiRouter.GET("/export/tasks", controller.Auth, middleware.RequireAdmin, controller.ExportTaskList)
	apiRouter.GET("/export/tasks/:taskId", controller.Auth, middleware.RequireAdmin, controller.ExportTaskDetail)
	apiRouter.GET("/export/tasks/:taskId/download", controller.Auth, middleware.RequireAdmin, controller.ExportTaskDownload)
	apiRouter.GET("/export/tasks/:taskId/errors", controller.Auth, middleware.RequireAdmin, controller.ExportTaskErrorReport)
	apiRouter.GET("/import/template", controller.Auth, middleware.RequireAdmin, controller.ImportTemplate)

	// 管理员赠送 / 批量赠送 / 追缴式回滚（44 / T-30 的前置）。九个 handler 全部
	// 先过 requireAdmin：赠送是直接产生钱的入口，漏一个等于开了印钞机。
	//
	// ⚠️ 静态段必须能压过参数段：gin 按段数分树，`/admin/user/:id/grant` 只吃 4 段，
	// `/admin/user/grant/batch` 也是 4 段但第三段是字面量 'grant' —— 实测两者共存时
	// 静态优先，`/admin/user/grant/batch` 不会被当成 id='grant'（已用探针验证 6 条路径）。
	apiRouter.POST("/admin/user/:id/grant", controller.Auth, middleware.RequireAdmin, controller.AdminGrant)
	apiRouter.POST("/admin/user/:id/grant-reversal", controller.Auth, middleware.RequireAdmin, controller.AdminGrantReverse)
	apiRouter.POST("/admin/user/grant/batch/preview", controller.Auth, middleware.RequireAdmin, controller.AdminGrantBatchPreview)
	apiRouter.POST("/admin/user/grant/batch", controller.Auth, middleware.RequireAdmin, controller.AdminGrantBatchExecute)
	apiRouter.POST("/admin/user/grant/batch/:batchId/rollback", controller.Auth, middleware.RequireAdmin, controller.AdminGrantBatchRollback)
	apiRouter.GET("/admin/user/grant/batch/:batchId", controller.Auth, middleware.RequireAdmin, controller.AdminGrantBatchProgress)
	apiRouter.POST("/admin/user/grant/batch/:batchId/pause", controller.Auth, middleware.RequireAdmin, controller.AdminGrantBatchPause)
	apiRouter.POST("/admin/user/grant/batch/:batchId/abort", controller.Auth, middleware.RequireAdmin, controller.AdminGrantBatchAbort)
	apiRouter.GET("/admin/grant/confirm-threshold", controller.Auth, middleware.RequireAdmin, controller.AdminGrantConfirmThreshold)

	// ── 脚本公开入口的「保留前缀」自动枚举（45-*.md §0.25.2，Q72-2）────────────
	//
	// 必须放在**函数最末尾**（所有路由注册完之后）：
	// 放到中途只会扫到一半前缀，表现为「某些公开路径今天能通配、明天不能」，
	// 是那种上线后极难排查的偶发缺口。
	//
	// 前提：本仓路由都是**静态注册**（main 里 SetAPIRouter / SetWebRouter 各调用一次），
	// 因此这里得到的是「启动时快照」；将来若有运行期动态注册的路由，
	// 必须重新调用一次 collectReservedPrefixes + service.SetReservedScriptPrefixes。
	//
	// 为什么按「路由第一段」枚举：脚本公开入口只可能出现在任意 host 的根路径下，
	// 保留判定也是按第一段做的（`/adapter-service/...` → `adapter-service`）。
	prefixes, scannedRoutes := collectReservedPrefixes(router)
	service.SetReservedScriptPrefixes(prefixes)
	if scannedRoutes < scriptReservedPrefixWarnBelow {
		logger.SysWarn(fmt.Sprintf(
			"脚本保留前缀自检：Routes() 只扫到 %d 条路由（阈值 %d），疑似在路由注册中途调用了 collectReservedPrefixes；已枚举 %d 个第一段前缀",
			scannedRoutes, scriptReservedPrefixWarnBelow, len(prefixes)))
	}
}

// scriptReservedPrefixWarnBelow 低于这个路由条数就说明枚举时机不对（本仓约 200 条）。
// 阈值取 20：远小于真实路由数，又足以在「只注册了几条路由就枚举」时报警。
const scriptReservedPrefixWarnBelow = 20

// collectReservedPrefixes 枚举路由表里出现过的**第一段路径**，返回 (前缀集合, 路由条数)。
//
// 路由条数是判断「调用时机是否正确」的唯一可靠信号：前缀集合本身很小
// （本仓所有 /adapter-service/* 路由共享同一个第一段），单看条数会误判。
// 首段是参数（`:id`）或通配（`*filepath`）的路由不产生保留前缀 —— 那不是
// 一个稳定的固定路径段，拦它只会误伤。
func collectReservedPrefixes(router *gin.Engine) ([]string, int) {
	if router == nil {
		return nil, 0
	}
	routes := router.Routes()
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if segment := firstRouteSegment(route.Path); segment != "" {
			seen[segment] = struct{}{}
		}
	}
	prefixes := make([]string, 0, len(seen))
	for segment := range seen {
		prefixes = append(prefixes, segment)
	}
	sort.Strings(prefixes)
	return prefixes, len(routes)
}

// firstRouteSegment 取路由第一段；首段是 gin 参数/通配则返回空串。
func firstRouteSegment(routePath string) string {
	for _, part := range strings.Split(strings.TrimPrefix(routePath, "/"), "/") {
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, ":") || strings.HasPrefix(part, "*") {
			return ""
		}
		return strings.ToLower(part)
	}
	return ""
}

type embedFileSystem struct {
	http.FileSystem
}

// Exists 判断文件是否存在
func (e embedFileSystem) Exists(_ string, path string) bool {
	_, err := e.Open(path)
	return err == nil
}

func embedFolder(fsEmbed embed.FS, targetPath string) static.ServeFileSystem {
	efs, err := fs.Sub(fsEmbed, targetPath)
	if err != nil {
		panic(err)
	}
	return embedFileSystem{
		FileSystem: http.FS(efs),
	}
}

// Only canonical Vue CLI content-hash JS/CSS names are immutable public assets.
// Non-hashed bootstrap/HTML, missing paths, directories and non-GET/HEAD requests
// continue through the original static/SPA/API handling below.
var immutableWebAssetPath = regexp.MustCompile(`^/(?:js/[^/]+\.[0-9a-f]{8}\.js|css/[^/]+\.[0-9a-f]{8}\.css)$`)

// The index is router-local and read-only after construction. Only the two
// public flat Vue CLI asset directories are inspected; embedded bytes cannot
// change during the process lifetime, so requests never need to rehash them.
func serveImmutableWebAssets(files static.ServeFileSystem) gin.HandlerFunc {
	etags := make(map[string]string)
	for _, folder := range []string{"js", "css"} {
		directory, err := files.Open("/" + folder)
		if err != nil {
			continue
		}
		entries, readErr := directory.Readdir(-1)
		closeErr := directory.Close()
		if readErr != nil || closeErr != nil {
			continue
		}
		for _, entry := range entries {
			assetPath := "/" + folder + "/" + entry.Name()
			if !entry.Mode().IsRegular() || !immutableWebAssetPath.MatchString(assetPath) {
				continue
			}
			file, err := files.Open(assetPath)
			if err != nil {
				continue
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				_ = file.Close()
				continue
			}
			digest := sha256.New()
			_, readErr := io.Copy(digest, file)
			closeErr := file.Close()
			if readErr == nil && closeErr == nil {
				etags[assetPath] = fmt.Sprintf("\"%x\"", digest.Sum(nil))
			}
		}
	}
	fileServer := http.FileServer(files)
	return func(c *gin.Context) {
		req := c.Request
		if (req.Method != http.MethodGet && req.Method != http.MethodHead) || req.URL.RawPath != "" {
			return
		}
		etag, exists := etags[req.URL.Path]
		if !exists {
			return
		}
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("ETag", etag)
		// Keep stdlib conditional/range processing. Only successful content or
		// revalidation responses may retain the newly introduced cache policy.
		fileServer.ServeHTTP(immutableWebAssetWriter{c.Writer}, req)
		c.Abort()
	}
}

type immutableWebAssetWriter struct{ http.ResponseWriter }

func (w immutableWebAssetWriter) WriteHeader(status int) {
	if status != http.StatusOK && status != http.StatusPartialContent && status != http.StatusNotModified {
		w.Header().Del("Cache-Control")
		w.Header().Del("ETag")
	}
	w.ResponseWriter.WriteHeader(status)
}

// SetWebRouter 设置web路由
func SetWebRouter(buildFS embed.FS, indexPage []byte, router *gin.Engine) {
	router.Use(controller.ScriptPublicHostMiddleware)
	files := embedFolder(buildFS, "dist")
	router.Use(serveImmutableWebAssets(files), static.Serve("/", files))
	router.NoRoute(func(c *gin.Context) {
		if controller.ScriptPublicPath(c) {
			return
		}
		path := c.Request.URL.Path
		// 未命中的 API 路径返回 JSON 404，不能落到 SPA 的 index.html：
		// 前端拿到 200 + HTML 会把它当成功响应解析，错误被掩盖成「数据格式异常」。
		//
		// 两个前缀都要判（2026-11-14 改名 /adapter-service → /api）：
		// 只判旧前缀会让 `/api/__missing__` 返回 SPA，只判新前缀会让旧客户端的
		// 未知路径拿到 HTML。
		for _, prefix := range []string{"/api", "/adapter-service"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				c.JSON(http.StatusNotFound, gin.H{"code": "NOT_FOUND", "message": "接口不存在"})
				return
			}
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPage)
	})
}
