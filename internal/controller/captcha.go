package controller

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/service"
)

var createCaptchaChallengeWithDB = func(scene string, requestIP string) (service.CaptchaChallengeResult, error) {
	return service.CreateCaptchaChallengeWithDB(dao.User.UnderlyingDB(), scene, requestIP)
}

var verifyCaptchaChallengeWithDB = func(challengeID string, scene string, captchaCode string, requestIP string) (service.CaptchaTokenResult, error) {
	return service.VerifyCaptchaChallengeWithDB(dao.User.UnderlyingDB(), challengeID, scene, captchaCode, requestIP)
}

var verifyRemoteCaptchaWithDB = func(input service.RemoteCaptchaVerifyInput) (service.CaptchaTokenResult, error) {
	return service.VerifyRemoteCaptchaWithDB(dao.User.UnderlyingDB(), input)
}

var loadCaptchaSettings = func() (service.SystemSettingDetail, error) {
	return service.GetSystemSettingDetailWithDB(dao.User.UnderlyingDB())
}

func CreateCaptchaChallenge(c *gin.Context) {
	var payload struct {
		Scene string `json:"scene"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	result, err := createCaptchaChallengeWithDB(strings.TrimSpace(payload.Scene), strings.TrimSpace(c.ClientIP()))
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	c.JSON(200, result)
}

func VerifyRemoteCaptcha(c *gin.Context) {
	var payload struct {
		Scene     string `json:"scene"`
		CaptchaID string `json:"captchaId"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	settings, err := loadCaptchaSettings()
	if err != nil {
		c.JSON(400, gin.H{"message": "验证码配置读取失败"})
		return
	}
	result, err := verifyRemoteCaptchaWithDB(service.RemoteCaptchaVerifyInput{
		Scene:      strings.TrimSpace(payload.Scene),
		CaptchaID:  strings.TrimSpace(payload.CaptchaID),
		RequestIP:  strings.TrimSpace(c.ClientIP()),
		APIBaseURL: settings.CaptchaAPIBaseURL,
	})
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	c.JSON(200, result)
}

func VerifyCaptchaChallenge(c *gin.Context) {
	var payload struct {
		ChallengeID string `json:"challengeId"`
		Scene       string `json:"scene"`
		CaptchaCode string `json:"captchaCode"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	result, err := verifyCaptchaChallengeWithDB(strings.TrimSpace(payload.ChallengeID), strings.TrimSpace(payload.Scene), strings.TrimSpace(payload.CaptchaCode), strings.TrimSpace(c.ClientIP()))
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	c.JSON(200, result)
}
