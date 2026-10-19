package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/service"
)

var wechatRegisterFlowStore = service.DefaultWechatRegisterFlowStore

var createWechatRegisterQRCode = func(client service.WechatLoginClient) (service.WechatQRCodeResult, error) {
	return service.CreateWechatRegisterQRCodeWithDB(dao.User.UnderlyingDB(), client)
}

var pollWechatRegisterQRCode = func(client service.WechatLoginClient, sessionID string) (service.WechatRegisterQRCodeStatus, error) {
	return service.PollWechatRegisterQRCode(client, sessionID)
}

var fetchWechatRegisterUserIDByCode = func(client service.WechatLoginClient, code string) (string, error) {
	result, err := service.ResolveWechatUserIDByCode(client, code)
	if err != nil {
		return "", err
	}
	return result.WechatID, nil
}

type wechatRegisterFlowPayload struct {
	RegistrationFlowToken string `json:"registrationFlowToken"`
	Refresh               bool   `json:"refresh"`
}

type wechatRegisterCodePayload struct {
	RegistrationFlowToken string `json:"registrationFlowToken"`
	Code                  string `json:"code"`
}

func PublicWechatRegisterSession(c *gin.Context) {
	var payload struct {
		RegistrationFlowToken string `json:"registrationFlowToken"`
	}
	if c.Request.Body != nil && c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
			return
		}
	}
	snapshot, err := wechatRegisterFlowStore.CreateOrResume(payload.RegistrationFlowToken, time.Now())
	if err != nil {
		writeWechatRegisterError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func PublicWechatRegisterQRCode(c *gin.Context) {
	var payload wechatRegisterFlowPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatScanLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信注册绑定未开启或配置未完成"})
		return
	}
	client := buildWechatLoginClientFromSettings(settings)
	now := time.Now()
	qr, err := wechatRegisterFlowStore.GetOrCreateQRCode(payload.RegistrationFlowToken, now, payload.Refresh, func() (service.WechatRegisterQRCode, error) {
		result, err := createWechatRegisterQRCode(client)
		if err != nil {
			return service.WechatRegisterQRCode{}, err
		}
		expiresAt := time.UnixMilli(result.ExpiresAtUnix)
		if result.ExpiresAtUnix <= 0 {
			ttl := time.Duration(result.ExpireSeconds) * time.Second
			if ttl <= 0 {
				ttl = service.WechatQRCodeTTL
			}
			expiresAt = now.Add(ttl)
		}
		return service.WechatRegisterQRCode{
			SessionID:     result.LoginToken,
			DisplayData:   result.QRCodeImageData,
			ExpiresAt:     expiresAt,
			ExpiresAtUnix: expiresAt.UnixMilli(),
		}, nil
	})
	if err != nil {
		writeWechatRegisterError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"sessionId":     qr.SessionID,
		"displayData":   qr.DisplayData,
		"expiresAtUnix": qr.ExpiresAtUnix,
	})
}

func PublicWechatRegisterStatus(c *gin.Context) {
	settings, settingsErr := loadWechatLoginSettings()
	if settingsErr != nil || !settings.WechatLoginEnabled || !settings.WechatScanLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信注册绑定未开启或配置未完成"})
		return
	}
	sessionID := strings.TrimSpace(c.Query("sessionId"))
	now := time.Now()
	snapshot, err := wechatRegisterFlowStore.StatusByQRCodeSession(sessionID, now)
	if err != nil {
		writeWechatRegisterError(c, err)
		return
	}
	if snapshot.Status == service.WechatRegisterStatusPending {
		upstream, pollErr := pollWechatRegisterQRCode(buildWechatLoginClientFromSettings(settings), sessionID)
		if pollErr == nil && upstream.Status == service.WechatLoginStatusExpired {
			expiredSnapshot, expireErr := wechatRegisterFlowStore.ExpireQRCodeSession(sessionID, now)
			if expireErr != nil {
				writeWechatRegisterError(c, expireErr)
				return
			}
			if expiredSnapshot.Status == service.WechatRegisterStatusStaged {
				c.JSON(http.StatusOK, gin.H{"status": expiredSnapshot.Status, "expiresAtUnix": expiredSnapshot.ExpiresAtUnix})
				return
			}
			c.JSON(http.StatusGone, gin.H{"status": service.WechatRegisterStatusExpired, "expiresAtUnix": expiredSnapshot.ExpiresAtUnix})
			return
		}
		if pollErr == nil && upstream.Status == service.WechatLoginStatusSuccess {
			wechatRegisterFlowStore.StageByQRCodeSession(sessionID, upstream.WechatID, now)
			stagedSnapshot, statusErr := wechatRegisterFlowStore.StatusByQRCodeSession(sessionID, now)
			if statusErr != nil {
				writeWechatRegisterError(c, statusErr)
				return
			}
			snapshot = stagedSnapshot
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":        snapshot.Status,
		"expiresAtUnix": snapshot.ExpiresAtUnix,
	})
}

func PublicWechatRegisterCode(c *gin.Context) {
	var payload wechatRegisterCodePayload
	if err := c.ShouldBindJSON(&payload); err != nil || strings.TrimSpace(payload.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if _, err := wechatRegisterFlowStore.Status(payload.RegistrationFlowToken, time.Now()); err != nil {
		writeWechatRegisterError(c, err)
		return
	}
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatCodeLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信验证码绑定未开启或配置未完成"})
		return
	}
	if err := wechatRegisterFlowStore.StageByCode(payload.RegistrationFlowToken, time.Now(), func() (string, error) {
		return fetchWechatRegisterUserIDByCode(buildWechatLoginClientFromSettings(settings), strings.TrimSpace(payload.Code))
	}); err != nil {
		writeWechatRegisterError(c, err)
		return
	}
	snapshot, statusErr := wechatRegisterFlowStore.Status(payload.RegistrationFlowToken, time.Now())
	if statusErr != nil {
		writeWechatRegisterError(c, statusErr)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": snapshot.Status, "expiresAtUnix": snapshot.ExpiresAtUnix})
}

func PublicWechatRegisterRelease(c *gin.Context) {
	var payload struct {
		RegistrationFlowToken string `json:"registrationFlowToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	_ = wechatRegisterFlowStore.Release(payload.RegistrationFlowToken)
	c.JSON(http.StatusOK, gin.H{"released": true})
}

func writeWechatRegisterError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	message := err.Error()
	if errors.Is(err, service.ErrWechatRegisterFlowExpired) {
		status = http.StatusGone
	} else if errors.Is(err, service.ErrWechatRegisterQRCodeExpired) {
		status = http.StatusGone
	} else if errors.Is(err, service.ErrWechatRegisterFlowInvalid) || errors.Is(err, service.ErrWechatRegisterQRInvalid) {
		status = http.StatusUnauthorized
	}
	c.JSON(status, gin.H{"message": message})
}
