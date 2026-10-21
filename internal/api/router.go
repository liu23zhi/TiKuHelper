package api

import (
	"embed"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/controller"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"io/fs"
	"net/http"
)

// SetAPIRouter 设置API路由
func SetAPIRouter(router *gin.Engine) {
	router.Use(middleware.CORS, middleware.SecurityHeaders)

	router.Any("/sqp/*path", middleware.BodyLimit(16<<20), controller.Proxy)

	apiRouter := router.Group("/adapter-service")

	apiRouter.Use(middleware.GlobalAPIRateLimit, middleware.BodyLimit(16<<20)) // 全局限流

	apiRouter.POST("/upload", controller.Auth, middleware.RequireAdmin, controller.UploadFile)

	// UApiPro 归属地查询（管理员专用）：用户管理 / 日志列表的归属地展示。
	// 密钥从系统设置读取，不下发到前端，也不出现在 URL 里。
	apiRouter.GET("/uapi/phoneinfo", controller.Auth, middleware.RequireAdmin, controller.UAPIPhoneInfo)
	apiRouter.GET("/uapi/ipinfo", controller.Auth, middleware.RequireAdmin, controller.UAPIIPInfo)
	apiRouter.GET("/uapi/my-locations", controller.Auth, controller.UAPIMyLocations)
	apiRouter.GET("/location-cache", controller.Auth, middleware.RequireAdmin, controller.LocationCacheList)
	apiRouter.DELETE("/location-cache", controller.Auth, middleware.RequireAdmin, controller.LocationCacheDelete)
	apiRouter.POST("/location-cache/batch-delete", controller.Auth, middleware.RequireAdmin, controller.LocationCacheBatchDelete)
	apiRouter.POST("/location-cache/refresh", controller.Auth, middleware.RequireAdmin, controller.LocationCacheRefresh)
	apiRouter.POST("/location-cache/manual", controller.Auth, middleware.RequireAdmin, controller.LocationCacheManualLookup)
	apiRouter.GET("/uapi/search/engines", controller.Auth, middleware.RequireAdmin, controller.UAPISearchEngines)
	apiRouter.GET("/uapi/status/usage", controller.Auth, middleware.RequireAdmin, controller.UAPIStatusUsage)
	apiRouter.POST("/uapi/search/aggregate", controller.Auth, middleware.RequireAdmin, controller.UAPISearchAggregate)

	apiRouter.POST("/search", controller.Search)
	apiRouter.POST("/search/tasks", controller.AuthOrAccessToken, controller.CreateSearchTask)
	apiRouter.GET("/search/status", controller.AuthOrAccessToken, controller.GetSearchTaskStatus)
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

	apiRouter.GET("/logs", controller.Auth, controller.LogList) // 日志列表
	apiRouter.GET("/billing-records", controller.Auth, controller.BillingRecordList)
	apiRouter.PUT("/billing-records/:id/statement-visibility", controller.Auth, middleware.RequireAdmin, controller.UpdateBillingStatementVisibility)
	apiRouter.GET("/recharge-orders", controller.Auth, controller.RechargeOrderList)
	apiRouter.POST("/recharge-orders", controller.Auth, controller.CreateRechargeOrder)
	apiRouter.Any("/payment/notify", controller.RechargePaymentNotify)
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
	apiRouter.POST("/ai-search-config/test", controller.Auth, controller.TestAISearchConfig)
	apiRouter.PUT("/ai-search-config", controller.Auth, middleware.RequireAdmin, controller.UpdateAISearchConfig)
	// 模型绑定（ai-model-architectures）已移除：模型列表由 AI 搜索配置维护，
	// 计费由模型价格表按「模型 ID / 显示名称」匹配。
	apiRouter.GET("/ai-model-prices", controller.Auth, controller.AIModelPriceList)
	apiRouter.POST("/ai-model-prices", controller.Auth, middleware.RequireAdmin, controller.CreateAIModelPrice)
	// 静态路径必须注册在 :id 之前，避免 gin 路由冲突。
	apiRouter.POST("/ai-model-prices/reset", controller.Auth, middleware.RequireAdmin, controller.ResetAIModelPriceDefaults)
	apiRouter.PUT("/ai-model-prices/:id", controller.Auth, middleware.RequireAdmin, controller.UpdateAIModelPrice)
	apiRouter.DELETE("/ai-model-prices/:id", controller.Auth, middleware.RequireAdmin, controller.DeleteAIModelPrice)
	apiRouter.GET("/system-settings", controller.Auth, middleware.RequireAdmin, controller.SystemSettingDetail)
	apiRouter.PUT("/system-settings", controller.Auth, middleware.RequireAdmin, controller.UpdateSystemSetting)

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

// SetWebRouter 设置web路由
func SetWebRouter(buildFS embed.FS, indexPage []byte, router *gin.Engine) {
	router.Use(static.Serve("/", embedFolder(buildFS, "dist")))
	router.NoRoute(func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPage)
	})
}
