package controller

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

var scriptDB = func() *gorm.DB {
	currentManager := manager.GetManager()
	if currentManager == nil {
		return nil
	}
	return currentManager.GetDB()
}

func ScriptVersionList(c *gin.Context) {
	if _, ok := currentOperator(c); !ok {
		return
	}
	items, err := service.ListScriptVersionsWithDB(scriptDB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取脚本版本失败"})
		return
	}
	if items == nil {
		items = []entity.ScriptVersion{}
	}
	c.JSON(http.StatusOK, items)
}

func UploadScriptVersion(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	version := strings.TrimSpace(c.PostForm("version"))
	if version == "" {
		version = strings.TrimSpace(c.PostForm("versionName"))
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请上传 EXE 文件"})
		return
	}
	if file.Size <= 1 || file.Size > service.ScriptVersionMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "EXE 文件大小超出限制"})
		return
	}
	if !strings.EqualFold(filepathExtForScript(file.Filename), ".exe") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "只允许上传 EXE 文件"})
		return
	}
	opened, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "读取 EXE 文件失败"})
		return
	}
	defer opened.Close()
	data, err := io.ReadAll(io.LimitReader(opened, service.ScriptVersionMaxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "读取 EXE 文件失败"})
		return
	}
	created, err := service.UploadScriptVersionWithDB(scriptDB(), operator, version, file.Filename, data)
	if err != nil {
		writeScriptError(c, err, "上传脚本版本失败")
		return
	}
	c.JSON(http.StatusOK, created)
}

func ScriptList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	all := strings.EqualFold(strings.TrimSpace(c.Query("scope")), "all")
	items, err := service.ListUserScriptsWithDB(scriptDB(), operator, all)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取脚本失败"})
		return
	}
	if items == nil {
		items = []service.ScriptListItem{}
	}
	c.JSON(http.StatusOK, items)
}

func CreateScript(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input service.CreateUserScriptInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	item, err := service.CreateUserScriptWithDB(scriptDB(), operator, input)
	if err != nil {
		writeScriptError(c, err, "创建脚本失败")
		return
	}
	c.JSON(http.StatusOK, item)
}

func DeleteScript(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "脚本参数错误"})
		return
	}
	if err := service.DeleteUserScriptWithDB(scriptDB(), operator, int32(id)); err != nil {
		writeScriptError(c, err, "删除脚本失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func UpdateScript(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "脚本参数错误"})
		return
	}
	var input service.UpdateUserScriptInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	item, err := service.UpdateUserScriptWithDB(scriptDB(), operator, int32(id), input)
	if err != nil {
		writeScriptError(c, err, "更新脚本失败")
		return
	}
	c.JSON(http.StatusOK, item)
}

func IssueScriptExchangeToken(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "脚本参数错误"})
		return
	}
	ttl := service.ScriptExchangeTokenTTL
	if raw := strings.TrimSpace(c.Query("ttl")); raw != "" {
		if seconds, parseErr := strconv.Atoi(raw); parseErr == nil {
			ttl = time.Duration(seconds) * time.Second
		}
	}
	grant, err := service.IssueScriptExchangeTokenWithDB(scriptDB(), operator, int32(id), ttl)
	if err != nil {
		writeScriptError(c, err, "生成脚本兑换令牌失败")
		return
	}
	c.JSON(http.StatusOK, grant)
}

func ExchangeScriptToken(c *gin.Context) {
	token := strings.TrimSpace(c.GetHeader("X-Script-Token"))
	if token == "" {
		token = strings.TrimSpace(c.GetHeader("Authorization"))
		token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	}
	if token == "" {
		var body struct {
			Token string `json:"token"`
		}
		if c.ShouldBindJSON(&body) == nil {
			token = strings.TrimSpace(body.Token)
		}
	}
	result, err := service.ExchangeScriptTokenWithDB(scriptDB(), c.Param("shortCode"), token)
	if err != nil {
		writeScriptError(c, err, "脚本兑换令牌无效")
		return
	}
	c.JSON(http.StatusOK, result)
}

// ScriptBootstrap returns a PowerShell 2 compatible launcher with a newly
// issued one-time exchange token in its response body.
func ScriptBootstrap(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	shortCode := c.Param("shortCode")
	grant, err := service.IssuePublicScriptExchangeTokenWithDB(scriptDB(), shortCode, service.ScriptExchangeTokenTTL)
	if err != nil {
		writeScriptError(c, err, "生成脚本兑换令牌失败")
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(buildScriptBootstrap(scriptPublicOrigin(c), shortCode, grant.Token)))
}

// ScriptExchangeScript consumes a one-time exchange token and emits the final
// PowerShell 2 launcher. The long-lived script key is sent as a header to the
// executable download and is written only into the local configuration file.
func ScriptExchangeScript(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token := scriptTokenFromRequest(c)
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": service.ErrScriptTokenRequired.Error()})
		return
	}
	shortCode := c.Param("shortCode")
	result, err := service.ExchangeScriptTokenWithDB(scriptDB(), shortCode, token)
	if err != nil {
		writeScriptError(c, err, "脚本兑换令牌无效")
		return
	}
	settings := make(map[string]any, len(result.Parameters)+1)
	for key, value := range result.Parameters {
		settings[key] = value
	}
	settings["common.settings.tiku-adapter.baseurl"] = scriptPublicOrigin(c)
	settings["common.settings.tiku-adapter.key"] = result.AccessKey
	config, err := json.Marshal(map[string]any{"settings": settings})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "脚本配置生成失败"})
		return
	}
	encodedConfig := base64.StdEncoding.EncodeToString(config)
	content := buildScriptPayload(scriptPublicOrigin(c), shortCode, result.DownloadURL, result.AccessKey, encodedConfig)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(content))
}

// ScriptBinaryDownload serves the executable only after validating the
// dedicated script key. It avoids placing the key in a URL or command line.
func ScriptBinaryDownload(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	key := strings.TrimSpace(c.GetHeader("X-Script-Key"))
	if key == "" {
		key = strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	}
	version, err := service.DownloadPublicScriptVersionWithDB(scriptDB(), c.Param("shortCode"), key)
	if err != nil {
		writeScriptError(c, err, "脚本下载令牌无效")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+safeScriptDownloadName(version.Filename)+`"`)
	c.Header("X-Script-Version", version.Version)
	c.Header("ETag", `"`+version.SHA256+`"`)
	c.Data(http.StatusOK, "application/vnd.microsoft.portable-executable", version.Data)
}

// ScriptPublicHostMiddleware handles the root of a wildcard host before the
// embedded web app's static middleware gets a chance to serve index.html.
func ScriptPublicHostMiddleware(c *gin.Context) {
	if c.Request.Method == http.MethodGet && c.Request.URL.Path == "/" {
		if servePublicScriptBootstrap(c) {
			c.Abort()
			return
		}
	}
	c.Next()
}

// ScriptPublicPath is called from the web router's NoRoute handler for the
// non-wildcard `/shortCode` form.
func ScriptPublicPath(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet || strings.HasPrefix(c.Request.URL.Path, "/adapter-service/") {
		return false
	}
	return servePublicScriptBootstrap(c)
}

func servePublicScriptBootstrap(c *gin.Context) bool {
	db := scriptDB()
	if db == nil {
		return false
	}
	script, err := service.ResolvePublicScriptWithDB(db, c.Request.Host, c.Request.URL.Path)
	if err != nil {
		if errors.Is(err, service.ErrScriptNotFound) {
			return false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "脚本入口暂不可用"})
		return true
	}
	grant, err := service.IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, service.ScriptExchangeTokenTTL)
	if err != nil {
		writeScriptError(c, err, "生成脚本兑换令牌失败")
		return true
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(buildScriptBootstrap(scriptPublicOrigin(c), script.ShortCode, grant.Token)))
	return true
}

func buildScriptBootstrap(baseURL, shortCode, token string) string {
	exchangeURL := strings.TrimRight(baseURL, "/") + "/adapter-service/script-command/" + shortCode + "/exchange-script"
	return "$w=New-Object Net.WebClient;$w.Headers.Add('X-Script-Token','" + escapePowerShellSingleQuoted(token) + "');iex ($w.DownloadString('" + escapePowerShellSingleQuoted(exchangeURL) + "'))"
}

func buildScriptPayload(baseURL, shortCode, downloadURL, accessKey, encodedConfig string) string {
	if strings.HasPrefix(downloadURL, "/") {
		downloadURL = strings.TrimRight(baseURL, "/") + downloadURL
	}
	exePath := "$env:TEMP+'\\tikuAdapter-'+ '" + escapePowerShellSingleQuoted(shortCode) + "'+'.exe'"
	configPath := "$env:TEMP+'\\tikuAdapter-'+ '" + escapePowerShellSingleQuoted(shortCode) + "'+'.json'"
	return "$ErrorActionPreference='Stop';$wc=New-Object Net.WebClient;$wc.Headers.Add('X-Script-Key','" + escapePowerShellSingleQuoted(accessKey) + "');$e=" + exePath + ";$c=" + configPath + ";$wc.DownloadFile('" + escapePowerShellSingleQuoted(downloadURL) + "',$e);[IO.File]::WriteAllBytes($c,[Convert]::FromBase64String('" + escapePowerShellSingleQuoted(encodedConfig) + "'));Start-Process $e -ArgumentList ('--config',$c)"
}

func scriptTokenFromRequest(c *gin.Context) string {
	token := strings.TrimSpace(c.GetHeader("X-Script-Token"))
	if token == "" {
		token = strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	}
	return token
}

func scriptPublicOrigin(c *gin.Context) string {
	scheme := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])
	if scheme != "http" && scheme != "https" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(c.Request.Host)
	if host == "" || strings.ContainsAny(host, "\r\n") {
		host = "localhost"
	}
	return scheme + "://" + host
}

func escapePowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func DownloadScriptVersion(c *gin.Context) {
	if _, ok := currentOperator(c); !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "版本参数错误"})
		return
	}
	version, err := service.DownloadScriptVersionWithDB(scriptDB(), int32(id))
	if err != nil {
		writeScriptError(c, err, "下载脚本版本失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="`+safeScriptDownloadName(version.Filename)+`"`)
	c.Header("X-Script-Version", version.Version)
	c.Header("ETag", `"`+version.SHA256+`"`)
	c.Data(http.StatusOK, "application/vnd.microsoft.portable-executable", version.Data)
}

func writeScriptError(c *gin.Context, err error, fallback string) {
	status, message := http.StatusInternalServerError, fallback
	switch {
	case errors.Is(err, service.ErrScriptForbidden):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, service.ErrScriptNotFound), errors.Is(err, service.ErrScriptVersionNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrScriptNameRequired), errors.Is(err, service.ErrScriptDomainRequired), errors.Is(err, service.ErrScriptVersionRequired), errors.Is(err, service.ErrScriptVersionInvalid), errors.Is(err, service.ErrScriptTokenRequired):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrScriptBalanceInsufficient):
		status, message = http.StatusPaymentRequired, err.Error()
	case errors.Is(err, service.ErrScriptTokenInvalid):
		status, message = http.StatusUnauthorized, err.Error()
	}
	c.JSON(status, gin.H{"message": message})
}

func filepathExtForScript(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(strings.ReplaceAll(name, "\\", "/"), '.'); i >= 0 {
		return name[i:]
	}
	return ""
}
func safeScriptDownloadName(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "script.exe"
	}
	return strings.ReplaceAll(name, `"`, "")
}
