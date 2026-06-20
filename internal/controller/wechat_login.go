package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/service"
)

var loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
	return service.GetSystemSettingDetailWithDB(dao.User.UnderlyingDB())
}

var createWechatLoginQRCodeWithDB = func(client service.WechatLoginClient) (service.WechatQRCodeResult, error) {
	return service.CreateWechatLoginQRCodeWithDB(dao.User.UnderlyingDB(), client)
}

var checkWechatLoginStatusWithDB = func(input service.WechatLoginStatusInput) (service.WechatLoginStatusResult, error) {
	return service.CheckWechatLoginStatusWithDB(dao.User.UnderlyingDB(), input)
}

var bindWechatLoginWithDB = func(input service.WechatBindInput) (service.WechatBindResult, error) {
	return service.BindWechatLoginWithDB(dao.User.UnderlyingDB(), input)
}

var fetchWechatUserIDByCode = func(client service.WechatLoginClient, code string) (string, error) {
	return service.FetchWechatUserIDByCode(client, code)
}

func buildWechatLoginClientFromSettings(settings service.SystemSettingDetail) service.WechatLoginClient {
	return service.WechatLoginClient{
		BaseURL: settings.WechatAPIBaseURL,
		Token:   settings.WechatAPIBearerToken,
	}
}

func CreateWechatLoginQRCode(c *gin.Context) {
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录未开启或配置未完成"})
		return
	}
	result, err := createWechatLoginQRCodeWithDB(buildWechatLoginClientFromSettings(settings))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func CheckWechatLoginStatus(c *gin.Context) {
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录未开启或配置未完成"})
		return
	}
	result, err := checkWechatLoginStatusWithDB(service.WechatLoginStatusInput{
		Client:     buildWechatLoginClientFromSettings(settings),
		LoginToken: strings.TrimSpace(c.Query("loginToken")),
		RequestIP:  strings.TrimSpace(c.ClientIP()),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func BindWechatLogin(c *gin.Context) {
	var payload struct {
		BindToken string `json:"bindToken"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	result, err := bindWechatLoginWithDB(service.WechatBindInput{
		BindToken: strings.TrimSpace(payload.BindToken),
		Username:  strings.TrimSpace(payload.Username),
		Password:  strings.TrimSpace(payload.Password),
		RequestIP: strings.TrimSpace(c.ClientIP()),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func LegacyWechatUserByCode(c *gin.Context) {
	settings, err := loadWechatLoginSettings()
	if err != nil || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录配置未完成"})
		return
	}
	wechatID, err := fetchWechatUserIDByCode(buildWechatLoginClientFromSettings(settings), strings.TrimSpace(c.Query("code")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"wechatId": wechatID})
}
