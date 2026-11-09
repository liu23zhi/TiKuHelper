package api

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/controller"
	"github.com/itihey/tikuAdapter/internal/middleware"
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
	apiRouter.POST("/user/logout", controller.UserLogout)      // 清理浏览器会话
	apiRouter.POST("/user/register", controller.UserRegister)  // 公开注册接口
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

	// 考试客户端本地库工具：仅管理员。服务端不解密，只发工具 + 只读预览明文库。
	apiRouter.GET("/admin/userexam/tool/version", controller.Auth, middleware.RequireAdmin, controller.UserExamToolVersion)
	apiRouter.GET("/admin/userexam/tool", controller.Auth, middleware.RequireAdmin, controller.UserExamToolDownload)
	apiRouter.POST("/admin/userexam/inspect", middleware.BodyLimit(controller.MaxUserExamInspectUploadBytes), controller.Auth, middleware.RequireAdmin, controller.UserExamInspect)

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
