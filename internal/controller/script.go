package controller

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

func ScriptDomainSettings(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	settings, err := service.GetScriptDomainSettingsWithDB(scriptDB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取脚本域名设置失败"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func UpdateScriptDomainSettings(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input service.ScriptDomainSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	settings, err := service.UpdateScriptDomainSettingsWithDB(scriptDB(), input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, settings)
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
	created, err := service.UploadScriptVersionWithDB(scriptDB(), operator, version, file.Filename, data,
		c.PostForm("nativeLeaseProtocol") == "true")
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
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	if input.BillingMode == "" {
		input.BillingMode = entity.ScriptBillingModeEscrow
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
	var confirmedSessionID int64
	if value, supplied := c.GetQuery("confirmActiveSessionId"); supplied {
		confirmedSessionID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || confirmedSessionID <= 0 || strconv.FormatInt(confirmedSessionID, 10) != value {
			c.JSON(http.StatusBadRequest, gin.H{"message": "客户端确认参数错误"})
			return
		}
	}
	result, err := service.CloseUserScriptWithDB(scriptDB(), operator, int32(id), confirmedSessionID)
	if errors.Is(err, service.ErrScriptActiveCloseConfirmationRequired) {
		c.JSON(http.StatusConflict, gin.H{
			"code":               "SCRIPT_ACTIVE_LEASE_CONFIRM_REQUIRED",
			"message":            "原生客户端正在使用此脚本，需要再次确认关闭",
			"activeRunSessionId": strconv.FormatInt(result.ActiveRunSessionID, 10),
		})
		return
	}
	if err != nil {
		writeScriptError(c, err, "删除脚本失败")
		return
	}
	if result.Status == service.ScriptCloseStatusSettling {
		message := "Key 已吊销，正在等待在途请求结清"
		if result.NeedsReconciliation {
			message = "Key 已吊销；历史运行缺少完整结算记录，冻结金待核对"
		}
		c.JSON(http.StatusAccepted, gin.H{"state": "settling", "message": message, "needsReconciliation": result.NeedsReconciliation})
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": "closed", "message": "脚本已关闭", "refundedMicros": result.RefundedMicros})
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
	c.Header("Cache-Control", "no-store")
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
	c.Header("Cache-Control", "no-store")
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

// ActivateScriptRunSession exchanges the short-lived launcher ticket for a
// 150-minute session bound to the executable's generated identity code.
func ActivateScriptRunSession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	activationToken := strings.TrimSpace(c.GetHeader("X-Script-Activation"))
	if activationToken == "" {
		activationToken = strings.TrimSpace(c.GetHeader("X-Script-Token"))
	}
	identity := strings.TrimSpace(c.GetHeader("X-Script-Identity"))
	durationMinutes := 0
	if activationToken == "" || identity == "" || c.Request.ContentLength > 0 {
		var body struct {
			ActivationToken string `json:"activationToken"`
			Identity        string `json:"identity"`
			DurationMinutes int    `json:"durationMinutes"`
		}
		if c.ShouldBindJSON(&body) == nil {
			durationMinutes = body.DurationMinutes
			if activationToken == "" {
				activationToken = strings.TrimSpace(body.ActivationToken)
			}
			if identity == "" {
				identity = strings.TrimSpace(body.Identity)
			}
		}
	}
	grant, err := service.ActivateLeasedScriptRunSessionWithDB(scriptDB(), c.Param("shortCode"), activationToken, identity, c.ClientIP(), durationMinutes)
	if err != nil {
		writeScriptError(c, err, "脚本运行会话激活失败")
		return
	}
	c.JSON(http.StatusOK, grant)
}

func HeartbeatScriptRunSession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	expiresAt, err := service.HeartbeatScriptRunSessionWithDB(scriptDB(), c.Param("shortCode"),
		c.GetHeader("Authorization"), c.GetHeader("X-Script-Identity"), c.ClientIP())
	if err != nil {
		writeScriptError(c, err, "脚本会话心跳失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"expiresAt": expiresAt})
}

// ScriptBootstrap returns a PowerShell 2 compatible launcher with a newly
// issued one-time exchange token in its response body.
func ScriptBootstrap(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	db := scriptDB()
	shortCode := strings.TrimSpace(c.Param("shortCode"))
	// The short code is intentionally compact, so the explicit compatibility
	// route must still require the published wildcard host. Otherwise anyone
	// could guess /script-command/a/bootstrap and mint a ticket for another
	// user's script. Non-wildcard deployments use the root NoRoute path and do
	// not pass through this compatibility route.
	script, err := service.ResolvePublicScriptWithDB(db, c.Request.Host, "/")
	if err != nil || script.ShortCode != shortCode {
		writeScriptError(c, service.ErrScriptNotFound, "脚本不存在")
		return
	}
	grant, err := service.IssuePublicScriptExchangeTokenWithDB(db, shortCode, c.GetHeader(service.ScriptLauncherHeader), service.ScriptExchangeTokenTTL)
	if err != nil {
		writeScriptError(c, err, "生成脚本兑换令牌失败")
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(buildScriptBootstrap(service.ScriptCommandOrigin(script), shortCode, grant.Token, script.BillingMode != "")))
}

// ScriptExchangeScript consumes a one-time exchange token and emits the final
// PowerShell 2 launcher. It only carries the one-time activation ticket; the
// long-lived dedicated access key is never returned to the client.
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
	settings["common.settings.tiku-adapter.baseurl"] = result.LaunchOrigin
	settings["common.settings.tiku-adapter.script-activation-token"] = result.ActivationToken
	settings["common.settings.tiku-adapter.script-session-url"] = result.LaunchOrigin + "/api/script-command/" + shortCode + "/activate"
	configValues := map[string]any{
		"settings":                     settings,
		"noDiskWrite":                  true,
		"executionMode":                "delete-suspend",
		"retainData":                   false,
		"logFileEnabled":               false,
		"quiet":                        true,
		"noDiagnose":                   true,
		"noScanCache":                  true,
		"wipeOwnedFilesOnStart":        true,
		"selfDestructOnFullscreenExit": true,
		"selfDestructConfirm":          false,
	}
	if result.BillingMode != "" {
		// Managed launches use only the one-time activation ticket, never a
		// normal Key or process/disk cleanup switches inherited from legacy.
		delete(settings, "common.settings.tiku-adapter.key")
		settings["common.settings.tiku-adapter.script-notify-on-denied"] = result.NotifyOnDenied
		configValues = map[string]any{"settings": settings, "noDiskWrite": true, "retainData": false}
	} else {
		settings["common.settings.tiku-adapter.key"] = result.ActivationToken
	}
	config, err := json.Marshal(configValues)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "脚本配置生成失败"})
		return
	}
	encodedConfig := base64.StdEncoding.EncodeToString(config)
	content := buildScriptPayload(result.LaunchOrigin, shortCode, result.DownloadURL, result.ActivationToken, encodedConfig)
	if result.BillingMode != "" {
		content, err = buildManagedScriptPayload(result.LaunchOrigin, result.DownloadURL, result.ExecutableSHA256, result.ActivationToken, encodedConfig)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "脚本客户端版本校验信息不可用"})
			return
		}
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(content))
}

// ScriptBinaryDownload serves the executable only after validating the
// one-time activation ticket. It avoids placing any long-lived key in a URL or
// command line. X-Script-Key is retained as a compatibility header name for
// older launchers, but its value is now always the activation ticket.
func ScriptBinaryDownload(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	activationToken := strings.TrimSpace(c.GetHeader("X-Script-Activation"))
	if activationToken == "" {
		activationToken = strings.TrimSpace(c.GetHeader("X-Script-Key"))
	}
	if activationToken == "" {
		activationToken = strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	}
	version, err := service.DownloadScriptVersionByActivationTokenWithDB(scriptDB(), c.Param("shortCode"), activationToken)
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
	// 两个 API 前缀都要排除（2026-11-14 改名 /adapter-service → /api）：
	// 它们是接口路径，不是脚本短码。只排新前缀会让旧路径的未知请求被当成
	// 短码去查库（多一次无谓查询，且可能命中一个同名短码）。
	if c.Request.Method != http.MethodGet {
		return false
	}
	for _, prefix := range []string{"/api/", "/adapter-service/"} {
		if strings.HasPrefix(c.Request.URL.Path, prefix) {
			return false
		}
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
	grant, err := service.IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, c.GetHeader(service.ScriptLauncherHeader), service.ScriptExchangeTokenTTL)
	c.Header("Cache-Control", "no-store")
	if err != nil {
		writeScriptError(c, err, "生成脚本兑换令牌失败")
		return true
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(buildScriptBootstrap(service.ScriptCommandOrigin(script), script.ShortCode, grant.Token, script.BillingMode != "")))
	return true
}

func scriptRunActorScope(c *gin.Context) string {
	if value, ok := c.Get("script_session"); ok {
		if session, ok := value.(entity.ScriptRunSession); ok && session.ID > 0 && session.IdentityHash != "" {
			return fmt.Sprintf("script:%d:%d:%s", session.ScriptID, session.AccessTokenID, session.IdentityHash)
		}
	}
	return ""
}

func buildScriptBootstrap(baseURL, shortCode, token string, managed ...bool) string {
	exchangeURL := strings.TrimRight(baseURL, "/") + "/api/script-command/" + shortCode + "/exchange-script"
	if len(managed) == 1 && managed[0] {
		return service.BuildManagedScriptTextLoader(exchangeURL, "X-Script-Token", token)
	}
	return "$w=New-Object Net.WebClient;$w.Headers.Add('X-Script-Token','" + escapePowerShellSingleQuoted(token) + "');iex ($w.DownloadString('" + escapePowerShellSingleQuoted(exchangeURL) + "'))"
}

func buildScriptPayload(baseURL, shortCode, downloadURL, activationToken, encodedConfig string) string {
	if strings.HasPrefix(downloadURL, "/") {
		downloadURL = strings.TrimRight(baseURL, "/") + downloadURL
	}
	exePath := "$env:TEMP+'\\tikuAdapter-'+ '" + escapePowerShellSingleQuoted(shortCode) + "'+'.exe'"
	// Keep the launcher payload in the child process environment. The EXE
	// consumes it at startup, activates an in-memory 150-minute session, and
	// never receives a persistent config file or a long-lived access key.
	return "$ErrorActionPreference='Stop';$wc=New-Object Net.WebClient;$wc.Headers.Add('X-Script-Activation','" + escapePowerShellSingleQuoted(activationToken) + "');$wc.Headers.Add('X-Script-Key','" + escapePowerShellSingleQuoted(activationToken) + "');$e=" + exePath + ";$wc.DownloadFile('" + escapePowerShellSingleQuoted(downloadURL) + "',$e);$env:CXPLUS_SCRIPT_BOOTSTRAP_B64='" + escapePowerShellSingleQuoted(encodedConfig) + "';$p=Start-Process $e -PassThru;$p.WaitForExit();Remove-Item -LiteralPath $e -Force -ErrorAction SilentlyContinue;Remove-Item Env:CXPLUS_SCRIPT_BOOTSTRAP_B64 -ErrorAction SilentlyContinue"
}

func buildManagedScriptPayload(baseURL, downloadURL, sha256Hex, ticket, encodedConfig string) (string, error) {
	digest, err := hex.DecodeString(sha256Hex)
	if err != nil || len(digest) != 32 || !strings.HasPrefix(baseURL, "https://") || !strings.HasPrefix(downloadURL, "/api/script-command/") {
		return "", errors.New("invalid managed script executable reference")
	}
	address := strings.TrimRight(baseURL, "/") + downloadURL
	// Generate and clean only this launcher's own random temporary file.
	// Verify the published artifact before giving it the one-time bootstrap.
	return "$ErrorActionPreference='Stop';$e=[IO.Path]::Combine([IO.Path]::GetTempPath(),('tikuhelper-'+[Guid]::NewGuid().ToString('N')+'.exe'));" +
		"try{$q=[Net.HttpWebRequest]::Create('" + escapePowerShellSingleQuoted(address) +
		"');$q.AllowAutoRedirect=$false;$q.Timeout=15000;$q.ReadWriteTimeout=15000;$q.Headers.Add('X-Script-Activation','" +
		escapePowerShellSingleQuoted(ticket) + "');$r=$q.GetResponse();" +
		"try{if([int]$r.StatusCode -ne 200){throw 'EXE response rejected'};$s=$r.GetResponseStream();$o=[IO.File]::Open($e,[IO.FileMode]::CreateNew);try{$b=New-Object byte[] 65536;$total=0;while(($n=$s.Read($b,0,$b.Length)) -gt 0){$total+=$n;if($total -gt 67108864){throw 'EXE exceeds size limit'};$o.Write($b,0,$n)}}finally{$o.Close();$s.Close()}}finally{$r.Close()};" +
		"$f=[IO.File]::OpenRead($e);try{$h=[Security.Cryptography.SHA256]::Create();try{$actual=[BitConverter]::ToString($h.ComputeHash($f)).Replace('-','').ToLowerInvariant()}finally{$h.Dispose()}}finally{$f.Close()};" +
		"if($actual -ne '" + strings.ToLower(sha256Hex) + "'){throw 'EXE SHA256 mismatch'};" +
		"$env:CXPLUS_SCRIPT_BOOTSTRAP_B64='" + escapePowerShellSingleQuoted(encodedConfig) +
		"';Start-Process -FilePath $e -Wait}finally{Remove-Item Env:CXPLUS_SCRIPT_BOOTSTRAP_B64 -ErrorAction SilentlyContinue;Remove-Item -LiteralPath $e -Force -ErrorAction SilentlyContinue}", nil
}

func scriptTokenFromRequest(c *gin.Context) string {
	token := strings.TrimSpace(c.GetHeader("X-Script-Token"))
	if token == "" {
		token = strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	}
	return token
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
	case errors.Is(err, service.ErrIdempotencyConflict), errors.Is(err, service.ErrIdempotencyPending):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, service.ErrIdempotencyKeyInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrScriptBindingsRequired):
		c.JSON(http.StatusForbidden, gin.H{"code": "SCRIPT_BINDINGS_REQUIRED", "message": service.ErrScriptBindingsRequired.Error()})
		return
	case errors.Is(err, service.ErrScriptDeviceBusy):
		c.JSON(http.StatusConflict, gin.H{"code": "SCRIPT_DEVICE_BUSY", "message": err.Error()})
		return
	case errors.Is(err, service.ErrScriptEscrowClosePending):
		c.JSON(http.StatusConflict, gin.H{"code": "SCRIPT_ESCROW_CLOSE_PENDING", "message": err.Error()})
		return
	case errors.Is(err, service.ErrScriptActivationEntitlementUnavailable), errors.Is(err, service.ErrScriptActivationRunConsumed):
		c.JSON(http.StatusConflict, gin.H{"code": "SCRIPT_ACTIVATION_UNAVAILABLE", "message": err.Error()})
		return
	case errors.Is(err, service.ErrScriptEscrowInsufficient):
		c.JSON(http.StatusPaymentRequired, gin.H{"code": "SCRIPT_ESCROW_INSUFFICIENT", "message": err.Error()})
		return
	case errors.Is(err, service.ErrScriptSourceUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "SCRIPT_SOURCE_UNAVAILABLE", "message": err.Error()})
		return
	case errors.Is(err, service.ErrScriptForbidden):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, service.ErrScriptNotFound), errors.Is(err, service.ErrScriptVersionNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrScriptNameRequired), errors.Is(err, service.ErrScriptDomainRequired), errors.Is(err, service.ErrScriptVersionRequired), errors.Is(err, service.ErrScriptVersionInvalid), errors.Is(err, service.ErrScriptTokenRequired), errors.Is(err, service.ErrScriptDomainUnavailable):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrScriptBalanceInsufficient), errors.Is(err, service.ErrScriptTokenBalanceInsufficient), errors.Is(err, service.ErrScriptExchangeAlreadyIssued):
		status, message = http.StatusPaymentRequired, err.Error()
	case errors.Is(err, service.ErrScriptDurationInvalid), errors.Is(err, service.ErrScriptRunIdentityRequired), errors.Is(err, service.ErrScriptNativeVersionRequired),
		errors.Is(err, service.ErrScriptBillingModeInvalid), errors.Is(err, service.ErrScriptEscrowInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrScriptTokenInvalid), errors.Is(err, service.ErrInvalidAccessTokenAuthorization):
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
