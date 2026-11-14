package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 管理员专用的「考试客户端本地库」工具。
//
// ════════════════════════════════════════════════════════════════════════════
// ⚠️ 方向变更（2026-11-14，用户拍板）—— 旧约束已被推翻，勿按旧注释行事
// ════════════════════════════════════════════════════════════════════════════
//
// 本文件此前的注释写着「边界（路径 A：加解密只在管理员本机发生）」「服务端
// **不持有口令、不含 SQLite3MC**」「只接受已解密的明文库」，并解释了
// **为什么不做服务端代加解密**：
//
//	「那需要把内置口令放进服务端进程、把上传的密文库落地到服务器临时目录并解出
//	  明文，等于把『离线工具』变成一个远程解密服务。」
//
// 用户随后要求：「请修改 客户端数据库工具，允许上传加密数据库，使用服务器进行解密。」
// 并明确选择「服务器内置密钥直接解」（否决了浏览器 WASM / 只改文案两个备选）。
// ⇒ 上述约束**已被推翻**：服务端现在确实持有密钥、确实在服务器上解密。
//
// 保留的四条边界（它们才是「持有密钥」与「变成公开解密服务」的分界，不得放松）：
//   - 全部接口要求管理员（Auth + RequireAdmin），普通用户一律 403；
//   - 密钥**永不回显**（只返回「是否已配置」+ 弱指纹），**永不进日志**；
//   - 明文只存在于**临时文件**，请求结束立即删除（defer os.Remove）；
//   - 不落业务目录、不保留副本、不做批量/离线解密。
//
// 解密是**纯 Go 实现**（service/user_exam_decrypt.go），不引入 cgo：
// Dockerfile 写死 CGO_ENABLED=0，实测 mattn/go-sqlite3 在该模式下编译通过
// （exit 0）但产物是 stub，运行时直接报 "requires cgo to work"。

// userExamToolDB 返回当前数据库句柄；未初始化时返回 nil（调用方降级）。
//
// 与 blacklistAdminDB / referralDB / securityStatusDB 同构：单测里没有 manager
// 时不能 panic。独立成变量而不是复用别处的，是因为本文件需要被单测替换成
// 临时 SQLite 库（密钥读写要真落库才能验证「优先读 system_settings」）。
var userExamToolDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// userExamToolName 必须与 tools/userexam-crypto/ 构建产物同名，否则下发给
// 管理员的文件名与 pyinstaller 实际生成的文件不一致（下载后双击即报错）。
// 该一致性由 TestUserExamToolNameMatchesBuildArtifact 守着。
const userExamToolName = "userexam-crypto.exe"

// originalUserExamToolPath 是 userExamToolPath 的未替换实现。
//
// 保留它是为了让测试能验证「环境变量优先」这条语义：其它用例都把
// userExamToolPath 整体 stub 掉了，若没有这个别名，就没有任何地方能观察到
// os.Getenv 的真实解析结果（Docker 的 `-e USEREXAM_TOOL_PATH=…` 覆盖就靠它）。
func originalUserExamToolPath() string {
	if configured := strings.TrimSpace(os.Getenv("USEREXAM_TOOL_PATH")); configured != "" {
		return configured
	}
	return ""
}

var userExamToolPath = originalUserExamToolPath

// userExamToolVersion 是工具与加密参数的自描述，供管理台展示。
type userExamToolVersion struct {
	Available  bool   `json:"available"`
	Filename   string `json:"filename"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	Cipher     string `json:"cipher"`
	LegacyMode string `json:"legacy"`
	KDFIter    int    `json:"kdfIter"`
	// Reason 只在不可用时出现，说明为什么下载不到。
	Reason string `json:"reason,omitempty"`
}

const (
	// userExamToolVersionLabel 必须与 tools/userexam-crypto/userexam_crypto.py
	// 的 TOOL_VERSION 一致（管理台按它判断手上那份 exe 是不是最新）。
	userExamToolVersionLabel = "1.1.0"
	userExamToolCipher       = "aes256cbc"
	userExamToolLegacy       = "0"
	userExamToolKDFIter      = 4001
)

func UserExamToolVersion(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	c.JSON(http.StatusOK, describeUserExamTool())
}

func describeUserExamTool() userExamToolVersion {
	info := userExamToolVersion{
		Filename:   userExamToolName,
		Version:    userExamToolVersionLabel,
		Cipher:     userExamToolCipher,
		LegacyMode: userExamToolLegacy,
		KDFIter:    userExamToolKDFIter,
	}
	path := userExamToolPath()
	if path == "" {
		info.Reason = "服务端未部署脱机工具（未配置 USEREXAM_TOOL_PATH）"
		return info
	}
	file, err := os.Open(path)
	if err != nil {
		info.Reason = "脱机工具不可读"
		return info
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		info.Reason = "脱机工具不是常规文件"
		return info
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		info.Reason = "脱机工具校验失败"
		return info
	}
	info.Available = true
	info.SizeBytes = stat.Size()
	info.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return info
}

func UserExamToolDownload(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	path := userExamToolPath()
	if path == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "服务端未部署脱机工具"})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "脱机工具不可读"})
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "脱机工具不可读"})
		return
	}

	// 下载行为要留审计：谁在什么时候取走了这个工具。
	auditUserExamAction(c, 1, "下载考试客户端数据库工具", map[string]any{
		"name":   userExamToolName,
		"status": "success",
	})

	c.Header("Content-Disposition", `attachment; filename="`+userExamToolName+`"`)
	c.Header("Content-Type", "application/vnd.microsoft.portable-executable")
	http.ServeContent(c.Writer, c.Request, userExamToolName, stat.ModTime(), file)
}

// MaxUserExamInspectUploadBytes 是 multipart 信封上限，比明文库上限多留一点头部余量。
const MaxUserExamInspectUploadBytes = service.MaxUserExamInspectBytes + 65536

// UserExamInspect 只读预览考试客户端数据库（**明文或加密均可**）。
//
// 分流（2026-11-14 起支持加密库）：
//  1. 前 16 字节是 "SQLite format 3\0" → 明文，走原有巡检路径（**能力不得破坏**）；
//  2. 否则尝试用当前生效密钥解密 → 成功则巡检解密结果；
//  3. 解密失败按**四种可区分的原因**分别回应（见下），不再统一报「请先用脱机工具解密」。
//
// 四种错误文案的设计目标：让用户一眼看出下一步该做什么。
//
//	① 不是数据库         → 「不是有效的 SQLite 数据库」（用户传错文件）
//	② 密钥不对           → 「密钥不正确」+ 提示去后台改密钥（管理员能自助）
//	③ 密钥未配置         → 理论上不会出现（有内置默认值），但保留分支：
//	                        若默认值被清空且后台没配，明确提示去配置
//	④ 算法不支持         → 「仅支持 SQLite3MC aes256cbc」（换方案或换工具）
//
// 审计只记「是否解密过 / 是否用了默认密钥」，**绝不记密钥**。
func UserExamInspect(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请选择要预览的 UserExamInfo.db 文件（明文或加密均可）"})
		return
	}
	if header.Size > service.MaxUserExamInspectBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "数据库文件超过只读预览允许的大小"})
		return
	}

	source, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "上传文件无法读取"})
		return
	}
	defer source.Close()

	// 只在临时文件上解析：不落业务目录、不保留副本，请求结束即删除。
	//
	// 加密库会在内存里解密后**覆盖写同一个临时文件**（而不是再建一个），
	// 这样「明文临时文件」始终只有一个、生命周期由同一个 defer 收口。
	staging, err := os.CreateTemp("", "userexam-inspect-*.db")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "临时文件创建失败"})
		return
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath)
	defer staging.Close()

	written, err := io.Copy(staging, io.LimitReader(source, service.MaxUserExamInspectBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "上传文件写入失败"})
		return
	}
	if written > service.MaxUserExamInspectBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "数据库文件超过只读预览允许的大小"})
		return
	}
	if err := staging.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "临时文件写入失败"})
		return
	}

	content, err := os.ReadFile(stagingPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "临时文件读取失败"})
		return
	}

	// ── 分流 1：明文直通（现有能力，不能被本次改动破坏）
	decrypted := false
	usedDefaultKey := false
	if !service.IsPlaintextUserExamDatabase(content) {
		// ── 分流 2：尝试解密
		key, isDefault := service.GetUserExamDecryptKeyWithDB(userExamToolDB())
		usedDefaultKey = isDefault
		plain, decryptErr := service.UserExamDecryptDatabase(content, service.UserExamDecryptParams{Password: key})
		switch {
		case decryptErr == nil:
			decrypted = true
			content = plain
			// 覆盖写临时文件：后续巡检从磁盘读，避免把明文再多留一份在内存里过久。
			if err := os.WriteFile(stagingPath, plain, 0o600); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "解密结果写入临时文件失败"})
				return
			}
		case errors.Is(decryptErr, service.ErrUserExamDecryptWrongKey):
			// ② 密钥不对：这是管理员自己能解决的，必须给出明确动作。
			writeUserExamInspectDecryptError(c, http.StatusBadRequest,
				"该数据库是加密的，但用当前密钥解密失败（密钥不正确）。"+
					"请到「系统设置 → 考试客户端数据库密钥」核对或更换密钥后重试。"+
					"如果你从未改过密钥，说明这个库是用别处的密钥加密的。", decrypted, usedDefaultKey, isDefault)
			return
		case errors.Is(decryptErr, service.ErrUserExamDecryptKeyMissing):
			// ③ 密钥未配置（默认值被清空且后台没配）
			writeUserExamInspectDecryptError(c, http.StatusBadRequest,
				"该数据库是加密的，但服务端尚未配置解密密钥。"+
					"请在「系统设置 → 考试客户端数据库密钥」填写密钥后重试。", decrypted, usedDefaultKey, isDefault)
			return
		case errors.Is(decryptErr, service.ErrUserExamDecryptUnsupported):
			// ④ 算法不支持 / 不是数据库
			//
			// 这一支同时覆盖「用户传了个非数据库文件」与「加密方案不同」两种情况。
			// 用「看起来像不像加密库」再分一层，让文案更准：
			if service.UserExamDecryptLooksEncrypted(content) {
				writeUserExamInspectDecryptError(c, http.StatusBadRequest,
					"该数据库使用的加密方案不在支持范围内（仅支持 SQLite3MC aes256cbc）。"+
						"请用脱机工具确认库的加密参数，或先在本机解密后再上传明文库。", decrypted, usedDefaultKey, isDefault)
				return
			}
			// ① 不是数据库
			writeUserExamInspectDecryptError(c, http.StatusBadRequest,
				"该文件不是有效的 SQLite 数据库（既不是明文库，也不像本工具支持的加密库）。"+
					"请确认上传的是考试客户端的 UserExamInfo.db。", decrypted, usedDefaultKey, isDefault)
			return
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库解密失败"})
			return
		}
	}

	result, err := service.InspectUserExamDatabase(stagingPath)
	switch {
	case errors.Is(err, service.ErrUserExamTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "数据库文件超过只读预览允许的大小"})
		return
	case errors.Is(err, service.ErrUserExamNotPlaintext):
		// 走到这里说明解密「成功」但解出来的不是合法 SQLite：
		// 只可能是密钥碰巧通过了页头校验（概率极低）或库损坏。
		// 仍按「密钥不对」提示，因为这是管理员唯一能尝试的动作。
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "解密后的数据不是有效的 SQLite 数据库（可能是密钥不正确或文件已损坏）。",
		})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库解析失败"})
		return
	}

	// 在响应体里告诉前端「这次是否解密过」，便于 UI 显示「已自动解密」。
	if result.Decrypted != nil {
		*result.Decrypted = decrypted
	} else {
		result.Decrypted = &decrypted
	}

	auditUserExamAction(c, 2, "只读预览考试客户端数据库", map[string]any{
		"name":   filepath.Base(header.Filename),
		"status": "success",
		// 只记两个布尔位：审计要能回答「这次用过服务器密钥吗」，
		// 但**绝不能**记录密钥本身。
		"decrypted":      decrypted,
		"usedDefaultKey": usedDefaultKey,
	})

	c.JSON(http.StatusOK, result)
}

// writeUserExamInspectDecryptError 输出解密失败的分类错误。
//
// 响应体带 `reason` 字段（machine-readable），让前端可以做差异化引导
// （例如 reason=wrong-key 时直接给「去改密钥」的按钮）。
func writeUserExamInspectDecryptError(c *gin.Context, status int, message string, decrypted bool, usedDefaultKey bool, usingDefault bool) {
	reason := "not-database"
	switch {
	case strings.Contains(message, "密钥不正确"):
		reason = "wrong-key"
	case strings.Contains(message, "尚未配置解密密钥"):
		reason = "key-missing"
	case strings.Contains(message, "加密方案不在支持范围"):
		reason = "unsupported-cipher"
	}
	auditUserExamAction(c, 2, "只读预览考试客户端数据库", map[string]any{
		"status":         "failed",
		"reason":         reason,
		"decrypted":      decrypted,
		"usedDefaultKey": usedDefaultKey,
	})
	c.JSON(status, gin.H{
		"message":        message,
		"reason":         reason,
		"usedDefaultKey": usingDefault,
	})
}

// userExamDecryptKeyResponse 是密钥状态接口的响应。
//
// **结构与 service.UserExamDecryptKeyState 一致，但显式再声明一次**：
// 这样「响应里没有密钥字段」这件事在 controller 层也是可见的，
// 而不是靠 service 的类型定义间接保证。
type userExamDecryptKeyResponse struct {
	Configured     bool   `json:"configured"`
	UsingDefault   bool   `json:"usingDefault"`
	KeyHint        string `json:"keyHint"`
	DefaultKeyHint string `json:"defaultKeyHint"`
	// Message 是给管理员的一句说明（如「当前使用内置默认密钥」）。
	Message string `json:"message"`
}

// UserExamDecryptKeyDetail 返回当前解密密钥的**状态**（管理员）。
//
// 绝不返回密钥本身：响应结构里连那个字段都没有。
func UserExamDecryptKeyDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	state := service.GetUserExamDecryptKeyStateWithDB(userExamToolDB())
	c.JSON(http.StatusOK, buildUserExamDecryptKeyResponse(state))
}

// UserExamDecryptKeyUpdate 设置解密密钥（管理员）。
//
// 请求体：{"key":"..."}。空字符串 = 清空并回落到内置默认值。
// 校验（长度 ≤32 字节、无控制字符）在 service 层，这里只做映射。
func UserExamDecryptKeyUpdate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db := userExamToolDB()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库暂不可用"})
		return
	}
	var input struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	state, err := service.SetUserExamDecryptKeyWithDB(db, input.Key)
	if err != nil {
		if errors.Is(err, service.ErrUserExamDecryptKeyInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存解密密钥失败"})
		return
	}
	// 审计：只记「改过 + 新状态」，**永不记密钥值**。
	auditUserExamAction(c, 3, "更新考试客户端数据库解密密钥", map[string]any{
		"configured":   state.Configured,
		"usingDefault": state.UsingDefault,
	})
	c.JSON(http.StatusOK, buildUserExamDecryptKeyResponse(state))
}

// buildUserExamDecryptKeyResponse 组装密钥状态响应（含给人看的说明）。
func buildUserExamDecryptKeyResponse(state service.UserExamDecryptKeyState) userExamDecryptKeyResponse {
	message := "当前使用内置默认密钥（开箱即用）；如需更安全，请改成自己的密钥。"
	if state.Configured {
		message = "当前使用管理员配置的密钥。"
	}
	return userExamDecryptKeyResponse{
		Configured:     state.Configured,
		UsingDefault:   state.UsingDefault,
		KeyHint:        state.KeyHint,
		DefaultKeyHint: state.DefaultKeyHint,
		Message:        message,
	}
}

// auditUserExamAction 写一条审计；操作者已由 RequireAdmin 保证存在，
// 这里仍做一次判空，避免将来接口被复用时 panic。
func auditUserExamAction(c *gin.Context, action int32, description string, fields map[string]any) {
	if operator, ok := currentOperator(c); ok {
		recordAccountAudit(c, &operator, "userexam_tool", action, description, fields)
	}
}
