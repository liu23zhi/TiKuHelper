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
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// SetAPIRouter 设置API路由
func SetAPIRouter(router *gin.Engine) {
	router.Use(middleware.RequestCorrelation, middleware.CORS, middleware.SecurityHeaders)

	router.Any("/sqp/*path", middleware.BodyLimit(16<<20), controller.Auth, middleware.RequireAdmin, controller.Proxy)

	apiRouter := router.Group("/adapter-service")

	apiRouter.Use(middleware.GlobalAPIRateLimit, middleware.BodyLimit(16<<20)) // 全局限流

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
	}
	apiRouter.Use(middleware.BlacklistIP())

	apiRouter.POST("/upload", controller.Auth, middleware.RequireAdmin, controller.UploadFile)
	// EXE uploads need a larger multipart envelope than ordinary API requests.
	scriptUploadRouter := router.Group("/adapter-service/script-versions")
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
	apiRouter.DELETE("/user/:id", controller.Auth, controller.DeleteUser)                    // 删除用户

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
	apiRouter.POST("/ticket-attachments", middleware.BodyLimit((2<<20)+65536), controller.Auth, controller.UploadTicketAttachment)
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
	blacklistAdminRouter := router.Group("/adapter-service")
	blacklistAdminRouter.Use(middleware.GlobalAPIRateLimit, middleware.BodyLimit(16<<20))
	blacklistAdminRouter.GET("/blacklist", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryList)
	blacklistAdminRouter.POST("/blacklist", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryCreate)
	blacklistAdminRouter.PUT("/blacklist/:id/enabled", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryToggle)
	blacklistAdminRouter.DELETE("/blacklist/:id", controller.Auth, middleware.RequireAdmin, controller.BlacklistEntryDelete)
	blacklistAdminRouter.GET("/blacklist/hits", controller.Auth, middleware.RequireAdmin, controller.BlacklistHitList)

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
		if path == "/adapter-service" || strings.HasPrefix(path, "/adapter-service/") {
			c.JSON(http.StatusNotFound, gin.H{"code": "NOT_FOUND", "message": "接口不存在"})
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPage)
	})
}
