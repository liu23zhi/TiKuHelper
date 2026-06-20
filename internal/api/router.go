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
	router.Use(middleware.CORS)

	router.Any("/sqp/*path", controller.Proxy)

	apiRouter := router.Group("/adapter-service")

	apiRouter.Use(middleware.GlobalAPIRateLimit) // 全局限流

	apiRouter.POST("/upload", controller.UploadFile)
	apiRouter.GET("/plat", controller.Plat)

	apiRouter.POST("/search", controller.Search)
	apiRouter.POST("/ai-fallback", controller.AIFallback)
	apiRouter.POST("/parser", controller.Parser)

	apiRouter.GET("/courses", controller.Auth, controller.Courses)
	apiRouter.POST("/questions/search", controller.Auth, controller.GetQuestions)
	apiRouter.POST("/questions", controller.Auth, controller.CreateQuestion)
	apiRouter.POST("/questions/import", controller.Auth, controller.ImportQuestions)
	apiRouter.PUT("/questions/:id", controller.Auth, controller.UpdateQuestions)
	apiRouter.DELETE("/questions/:id", controller.Auth, controller.DeleteQuestion)

	apiRouter.GET("/user/login", controller.UserLogin)         // 登录接口
	apiRouter.POST("/user/login/sms", controller.UserSMSLogin) // 短信登录接口
	apiRouter.POST("/user/register", controller.UserRegister)  // 公开注册接口
	apiRouter.POST("/public/captcha/challenge", controller.CreateCaptchaChallenge)
	apiRouter.POST("/public/captcha/verify", controller.VerifyCaptchaChallenge)
	apiRouter.Any("/public/captcha/remote-gen", controller.ProxyRemoteCaptchaGenerate)
	apiRouter.POST("/public/captcha/remote-check", controller.ProxyRemoteCaptchaCheck)
	apiRouter.POST("/public/captcha/remote-token", controller.VerifyRemoteCaptcha)
	apiRouter.POST("/public/sms-code", controller.SendPublicSMSCode) // 公开短信验证码接口
	apiRouter.GET("/public/register-status", controller.PublicRegisterStatus)
	apiRouter.POST("/public/wechat-login/qrcode", controller.CreateWechatLoginQRCode)
	apiRouter.GET("/public/wechat-login/status", controller.CheckWechatLoginStatus)
	apiRouter.POST("/public/wechat-login/bind", controller.BindWechatLogin)
	apiRouter.GET("/public/wechat/user", controller.LegacyWechatUserByCode)
	apiRouter.GET("/user/info", controller.Auth, controller.UserInfo)                        // 当前登录用户
	apiRouter.GET("/user", controller.Auth, controller.UserList)                             // 自己创建的用户列表
	apiRouter.POST("/user", controller.Auth, controller.CreateUser)                          // 创建用户
	apiRouter.PUT("/user/:id", controller.Auth, controller.UpdateUser)                       // 更新用户
	apiRouter.PUT("/user/:id/reset-password", controller.Auth, controller.ResetUserPassword) // 重置密码
	apiRouter.DELETE("/user/:id", controller.Auth, controller.DeleteUser)                    // 删除用户

	apiRouter.GET("/logs", controller.Auth, controller.LogList) // 日志列表
	apiRouter.GET("/billing-records", controller.Auth, controller.BillingRecordList)
	apiRouter.POST("/recharge-orders", controller.Auth, controller.CreateRechargeOrder)
	apiRouter.POST("/payment/notify", controller.RechargePaymentNotify)
	apiRouter.GET("/access-tokens", controller.Auth, controller.AccessTokenList)
	apiRouter.POST("/access-tokens", controller.Auth, controller.CreateAccessToken)
	apiRouter.PUT("/access-tokens/:id", controller.Auth, controller.UpdateAccessToken)
	apiRouter.DELETE("/access-tokens/:id", controller.Auth, controller.DeleteAccessToken)
	apiRouter.GET("/payment-methods", controller.Auth, controller.PaymentMethodList)
	apiRouter.POST("/payment-methods", controller.Auth, controller.CreatePaymentMethod)
	apiRouter.PUT("/payment-methods/:id", controller.Auth, controller.UpdatePaymentMethod)
	apiRouter.DELETE("/payment-methods/:id", controller.Auth, controller.DeletePaymentMethod)
	apiRouter.GET("/search-account-configs", controller.Auth, controller.SearchAccountConfigList)
	apiRouter.PUT("/search-account-configs/:provider", controller.Auth, controller.UpdateSearchAccountConfig)
	apiRouter.GET("/ai-search-config", controller.Auth, controller.AISearchConfigDetail)
	apiRouter.GET("/ai-search-config/models", controller.Auth, controller.AISearchModels)
	apiRouter.POST("/ai-search-config/test", controller.Auth, controller.TestAISearchConfig)
	apiRouter.PUT("/ai-search-config", controller.Auth, controller.UpdateAISearchConfig)
	apiRouter.GET("/ai-model-architectures", controller.Auth, controller.AIModelArchitectureList)
	apiRouter.POST("/ai-model-architectures", controller.Auth, controller.CreateAIModelArchitecture)
	apiRouter.PUT("/ai-model-architectures/:id", controller.Auth, controller.UpdateAIModelArchitecture)
	apiRouter.DELETE("/ai-model-architectures/:id", controller.Auth, controller.DeleteAIModelArchitecture)
	apiRouter.GET("/system-settings", controller.Auth, controller.SystemSettingDetail)
	apiRouter.PUT("/system-settings", controller.Auth, controller.UpdateSystemSetting)

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
