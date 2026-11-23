package controller

import (
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

const scriptLaunchResolvePath = "/api/script-launch/resolve"

func ResolveScriptLaunch(c *gin.Context) {
	resolveScriptLaunch(scriptDB())(c)
}

func resolveScriptLaunch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		var input struct {
			Address  string `json:"address"`
			Identity string `json:"identity"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "无效的脚本启动参数"})
			return
		}
		_, identityErr := hex.DecodeString(input.Identity)
		if identityErr != nil || len(input.Identity) != 64 || strings.ToLower(input.Identity) != input.Identity {
			writeScriptError(c, service.ErrScriptRunIdentityRequired, "无效的脚本身份")
			return
		}
		address, err := url.Parse(input.Address)
		scheme := "http"
		if c.Request.TLS != nil || c.GetBool(scriptProxyHTTPSKey) {
			scheme = "https"
		}
		if err != nil || address.Hostname() == "" || address.User != nil ||
			address.RawPath != "" || address.ForceQuery || address.RawQuery != "" || strings.Contains(input.Address, "#") ||
			address.Scheme != scheme || (scheme == "http" && !service.IsLocalSiteAuthority(address.Host)) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "无效的脚本地址"})
			return
		}
		if !strings.EqualFold(address.Host, c.Request.Host) {
			writeScriptError(c, service.ErrScriptNotFound, "脚本地址不可用")
			return
		}
		var grant service.ScriptRunSessionGrant
		var origin string
		var parameters map[string]any
		err = db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			_, issued, err := service.ConsumeScriptShortAuthorizationWithDB(tx, address.Host, address.Path)
			if errors.Is(err, service.ErrScriptNotFound) && !errors.Is(err, service.ErrScriptShortAuthorizationInactive) {
				issued, err = service.IssuePublicScriptExchangeTokenByLocatorWithDB(tx, address.Host, address.Path, 0)
			}
			if err != nil {
				return err
			}
			if !strings.EqualFold(issued.LaunchOrigin, address.Scheme+"://"+address.Host) {
				return service.ErrScriptNotFound
			}
			exchanged, err := service.ExchangeScriptTokenWithDB(tx, issued.ShortCode, issued.Token)
			if err != nil {
				return err
			}
			grant, err = service.ActivateLeasedScriptRunSessionWithDB(tx, issued.ShortCode, exchanged.ActivationToken, input.Identity, c.ClientIP(), 0)
			origin = issued.LaunchOrigin
			parameters = exchanged.Parameters
			return err
		})
		if err != nil {
			writeScriptError(c, err, "脚本启动授权失败")
			return
		}
		if parameters == nil {
			parameters = map[string]any{}
		}
		c.JSON(http.StatusOK, gin.H{
			"origin": origin, "token": grant.Token, "identity": input.Identity,
			"expiresAt": grant.ExpiresAt, "heartbeatUrl": grant.HeartbeatURL, "parameters": parameters,
		})
	}
}
