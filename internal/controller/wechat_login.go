package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
)

var loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
	return service.GetSystemSettingDetailWithDB(withDatabasePriority(dao.User.UnderlyingDB(), context.Background(), dbqueue.PriorityLow))
}

var createWechatLoginQRCodeWithDB = func(client service.WechatLoginClient) (service.WechatQRCodeResult, error) {
	return service.CreateWechatLoginQRCodeWithDB(dao.User.UnderlyingDB(), client)
}

var checkWechatLoginStatusWithDB = func(input service.WechatLoginStatusInput) (service.WechatLoginStatusResult, error) {
	return service.CheckWechatLoginStatusWithDB(dao.User.UnderlyingDB(), input)
}

var probeWechatScanStatusWithDB = func(input service.WechatScanProbeInput) (service.WechatScanProbeResult, error) {
	return service.ProbeWechatScanStatusWithDB(dao.User.UnderlyingDB(), input)
}

var loginWechatByCodeWithDB = func(input service.WechatCodeLoginInput) (service.WechatLoginStatusResult, error) {
	return service.LoginWechatByCodeWithDB(dao.User.UnderlyingDB(), input)
}

var bindWechatLoginWithDB = func(input service.WechatBindInput) (service.WechatBindResult, error) {
	return service.BindWechatLoginWithDB(dao.User.UnderlyingDB(), input)
}

var fetchWechatUserIDByCode = func(client service.WechatLoginClient, code string) (string, error) {
	result, err := service.ResolveWechatUserIDByCode(client, code)
	if err != nil {
		return "", err
	}
	return result.WechatID, nil
}

var resolveAdminWechatScanID = func(client service.WechatLoginClient, loginToken string) (service.WechatScanIDResult, error) {
	return service.ResolveWechatScanID(client, loginToken)
}

var wechatWebSocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool {
		return true
	},
}

type wechatSocketMessage struct {
	Type             string `json:"type"`
	Status           string `json:"status,omitempty"`
	RemainingSeconds int64  `json:"remainingSeconds,omitempty"`
	Timestamp        int64  `json:"timestamp,omitempty"`
}

func buildWechatLoginClientFromSettings(settings service.SystemSettingDetail) service.WechatLoginClient {
	return service.WechatLoginClient{
		BaseURL: settings.WechatAPIBaseURL,
		Token:   settings.WechatAPIBearerToken,
	}
}

func CreateWechatLoginQRCode(c *gin.Context) {
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatScanLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
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
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatScanLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录未开启或配置未完成"})
		return
	}
	bindOnly := strings.EqualFold(strings.TrimSpace(c.Query("purpose")), "bind")
	result, err := checkWechatLoginStatusWithDB(service.WechatLoginStatusInput{
		Client:     buildWechatLoginClientFromSettings(settings),
		LoginToken: strings.TrimSpace(c.Query("loginToken")),
		RequestIP:  strings.TrimSpace(c.ClientIP()),
		BindOnly:   bindOnly,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if bindOnly {
		result.JWT = ""
	} else if strings.TrimSpace(result.JWT) != "" {
		setAuthCookie(c, result.JWT)
	}
	c.JSON(http.StatusOK, result)
}

func WechatLoginWebSocket(c *gin.Context) {
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatScanLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录未开启或配置未完成"})
		return
	}
	loginToken := strings.TrimSpace(c.Query("loginToken"))
	session, err := service.DefaultWechatLoginSessionStore.Resolve(loginToken, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	conn, err := wechatWebSocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	runWechatLoginWebSocket(
		conn,
		session.ExpiresAt,
		buildWechatLoginClientFromSettings(settings),
		loginToken,
	)
}

func runWechatLoginWebSocket(conn *websocket.Conn, expiresAt time.Time, client service.WechatLoginClient, loginToken string) {
	conn.SetReadLimit(1024)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	inbound := make(chan wechatSocketMessage, 8)
	readClosed := make(chan struct{})
	go func() {
		defer close(readClosed)
		for {
			var message wechatSocketMessage
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			select {
			case inbound <- message:
			case <-readClosed:
				return
			}
		}
	}()

	statusTicker := time.NewTicker(2 * time.Second)
	heartbeatTicker := time.NewTicker(25 * time.Second)
	expiryTimer := time.NewTimer(time.Until(expiresAt))
	defer statusTicker.Stop()
	defer heartbeatTicker.Stop()
	defer expiryTimer.Stop()

	lastStatus := ""
	writeMessage := func(message wechatSocketMessage) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(message) == nil
	}
	sendStatus := func(status string, repeat bool) bool {
		status = strings.TrimSpace(status)
		if status == "" || !repeat && status == lastStatus {
			return true
		}
		remaining := int64(time.Until(expiresAt).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		if !writeMessage(wechatSocketMessage{
			Type:             "status",
			Status:           status,
			RemainingSeconds: remaining,
		}) {
			return false
		}
		lastStatus = status
		return true
	}

	probeAndSend := func(repeat bool) bool {
		result, err := probeWechatScanStatusWithDB(service.WechatScanProbeInput{
			Client:     client,
			LoginToken: loginToken,
		})
		if err != nil {
			return true
		}
		if !sendStatus(result.Status, repeat) {
			return false
		}
		return result.Status != service.WechatLoginStatusSuccess &&
			result.Status != service.WechatLoginStatusNeedBind &&
			result.Status != service.WechatLoginStatusExpired
	}

	if !sendStatus(service.WechatLoginStatusPending, false) {
		return
	}
	if !probeAndSend(false) {
		return
	}

	for {
		select {
		case message := <-inbound:
			if message.Type != "ping" {
				continue
			}
			if !writeMessage(wechatSocketMessage{Type: "pong", Timestamp: message.Timestamp}) {
				return
			}
		case <-heartbeatTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case <-statusTicker.C:
			if !probeAndSend(true) {
				return
			}
		case <-expiryTimer.C:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = conn.WriteJSON(wechatSocketMessage{Type: "status", Status: service.WechatLoginStatusExpired})
			return
		case <-readClosed:
			return
		}
	}
}

func LoginWechatByCode(c *gin.Context) {
	var payload struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatCodeLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录未开启或配置未完成"})
		return
	}
	result, err := loginWechatByCodeWithDB(service.WechatCodeLoginInput{
		Client:    buildWechatLoginClientFromSettings(settings),
		Code:      strings.TrimSpace(payload.Code),
		RequestIP: strings.TrimSpace(c.ClientIP()),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if strings.TrimSpace(result.JWT) != "" {
		setAuthCookie(c, result.JWT)
	}
	c.JSON(http.StatusOK, result)
}

func ResolveAdminWechatIDByScan(c *gin.Context) {
	operator, _ := c.Get("user")
	currentUser, _ := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限获取微信 ID"})
		return
	}
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatScanLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信扫码登录未开启或配置未完成"})
		return
	}
	result, err := resolveAdminWechatScanID(
		buildWechatLoginClientFromSettings(settings),
		strings.TrimSpace(c.Query("loginToken")),
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func ResolveAdminWechatIDByCode(c *gin.Context) {
	operator, _ := c.Get("user")
	currentUser, _ := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限获取微信 ID"})
		return
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatCodeLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信验证码登录未开启或配置未完成"})
		return
	}
	wechatID, err := fetchWechatUserIDByCode(
		buildWechatLoginClientFromSettings(settings),
		strings.TrimSpace(payload.Code),
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"wechatId": strings.TrimSpace(wechatID)})
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
	if strings.TrimSpace(result.JWT) != "" {
		setAuthCookie(c, result.JWT)
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
