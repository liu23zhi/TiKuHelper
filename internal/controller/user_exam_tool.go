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
	"github.com/itihey/tikuAdapter/internal/service"
)

// 管理员专用的「考试客户端本地库」工具。
//
// 边界（路径 A：加解密只在管理员本机发生）：
//   - 三个接口全部要求管理员（Auth + RequireAdmin），普通用户一律 403；
//   - 服务端**不持有口令、不含 SQLite3MC**，只把已构建好的 exe 原样发出去；
//   - 只接受已解密的明文库，且只在临时文件上只读解析，不落盘留存。
//
// 之所以不做服务端代加解密：那需要把内置口令放进服务端进程、把上传的密文库
// 落地到服务器临时目录并解出明文，等于把「离线工具」变成一个远程解密服务。
// 现有实现里连口令常量都不存在（在 internal/、pkg/、cmd/ 下搜 aes256cbc 只能
// 命中本文件的参数自描述），保持这一点。

// userExamToolName 必须与 tools/userexam-crypto/ 构建产物同名，否则下发给
// 管理员的文件名与 pyinstaller 实际生成的文件不一致（下载后双击即报错）。
// 该一致性由 TestUserExamToolNameMatchesBuildArtifact 守着。
const userExamToolName = "userexam-crypto.exe"

var userExamToolPath = func() string {
	if configured := strings.TrimSpace(os.Getenv("USEREXAM_TOOL_PATH")); configured != "" {
		return configured
	}
	return ""
}

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

func UserExamInspect(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请上传已解密的 UserExamInfo.db"})
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

	result, err := service.InspectUserExamDatabase(stagingPath)
	switch {
	case errors.Is(err, service.ErrUserExamTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "数据库文件超过只读预览允许的大小"})
		return
	case errors.Is(err, service.ErrUserExamNotPlaintext):
		c.JSON(http.StatusBadRequest, gin.H{"message": "该文件不是已解密的 SQLite 数据库；请先用脱机工具解密"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库解析失败"})
		return
	}

	auditUserExamAction(c, 2, "只读预览考试客户端数据库", map[string]any{
		"name":   filepath.Base(header.Filename),
		"status": "success",
	})

	c.JSON(http.StatusOK, result)
}

// auditUserExamAction 写一条审计；操作者已由 RequireAdmin 保证存在，
// 这里仍做一次判空，避免将来接口被复用时 panic。
func auditUserExamAction(c *gin.Context, action int32, description string, fields map[string]any) {
	if operator, ok := currentOperator(c); ok {
		recordAccountAudit(c, &operator, "userexam_tool", action, description, fields)
	}
}
