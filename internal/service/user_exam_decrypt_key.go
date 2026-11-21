package service

// 考试客户端数据库的**服务器侧解密密钥**管理。
//
// ════════════════════════════════════════════════════════════════════════════
// 与旧约束的关系（重要，勿删）
// ════════════════════════════════════════════════════════════════════════════
//
// 本文件的存在本身就是一次**方向变更**的产物。旧口径（
// tools/userexam-crypto/userexam_crypto.py:5-9 / internal/service/user_exam_inspect.go:18-19
// / internal/controller/user_exam_tool.go:24-27）明确写着：
//
//	「the TIKU service deliberately has no SQLite3MC code and no copy of the cipher key」
//	「服务端**不持有口令、不含 SQLite3MC**」
//
// 2026-11-14 用户拍板推翻（原话「请修改 客户端数据库工具，允许上传加密数据库，
// 使用服务器进行解密。」），并要求「默认可开箱即用、管理员可改」。⇒ 服务端现在
// **确实持有**一份默认密钥，同时允许管理员在后台覆盖。
//
// 保留的合理内核（不是形式主义，是这四条让「持有密钥」不至变成「远程解密服务」）：
//   - 密钥**永不回显**：任何 API 只回答「是否已配置 / 是否仍是默认值」，不回传值；
//   - 密钥**永不进日志**：全部路径都不打印，连错误信息里也不带；
//   - 仅管理员可达：解密入口挂在 Auth + RequireAdmin 上；
//   - 明文只在临时文件，请求结束即删。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// UserExamDecryptKeySettingKey 是「考试客户端数据库解密密钥」在 system_settings
// 里的键名（与 uapi_api_key 同一机制：只从库里读，不硬编码到 handler）。
const UserExamDecryptKeySettingKey = "userexam_db_key"

// UserExamDefaultDecryptKey 是**内置默认密钥**，让功能开箱即用。
//
// ⚠️ 这是一把**公开的、写在仓库里的**密钥，只用于「用户没配自己的密钥时也能用」。
// 生产环境强烈建议管理员在后台「系统设置 → 考试客户端数据库密钥」里改掉：
// 任何拿到本仓库的人都能用它解开未改密钥的服务器上传的库。
//
// 为什么仍然要有一个默认值（而不是强制先配置）：用户明确要求「默认可开箱即用」。
// 强制配置会让「上传即报未配置密钥」，与需求相悖，且会让新部署的实例功能形同虚设。
//
// 值的选取：一个长度适中、能被 32 字节补位规则完整容纳的 ASCII 串。
// 不要改成中文/超长 —— userExamPadPassword 会在 32 字节处**截断**，
// 超长密钥会静默丢尾部（与 SQLite3MC 行为一致，但会让「改了密钥却没变化」难以察觉）。
const UserExamDefaultDecryptKey = "tiku-userexam-default-key-2026"

// UserExamDecryptKeyState 是「密钥状态」的对外视图。
//
// **刻意不含密钥本身**：响应结构里连字段都没有，杜绝「顺手加上」。
type UserExamDecryptKeyState struct {
	// Configured 表示管理员是否在后台配过（false = 正在使用内置默认值）。
	Configured bool `json:"configured"`
	// UsingDefault 表示当前生效的是不是内置默认密钥。
	UsingDefault bool `json:"usingDefault"`
	// KeyHint 是给管理员确认「配置生效了没有」用的**弱指纹**：口令的 SHA-256
	// 前 8 个十六进制字符。
	//
	// 不是密钥、不可逆、不泄漏原文，但足以回答「我改的和生效的是不是同一个」。
	// 没有它的话，管理员改完密钥在界面上完全看不出差别（因为不回显），
	// 只能靠「上传一个库试试」来验证，体验很差。
	KeyHint string `json:"keyHint"`
	// DefaultKeyHint 是内置默认密钥的同一个弱指纹，供对照。
	DefaultKeyHint string `json:"defaultKeyHint"`
}

// ErrUserExamDecryptKeyInvalid 表示后台提交的密钥不可用（空 / 过长）。
var ErrUserExamDecryptKeyInvalid = errors.New("解密密钥不合法")

// userExamKeyHint 计算密钥的弱指纹（SHA-256 前 8 位十六进制）。
//
// 绝不等于密钥：SHA-256 前 32 bit 无法反推原文；它的唯一用途是
// 「让管理员确认自己改的值确实生效了」。
func userExamKeyHint(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(trimmed))
	hexed := hex.EncodeToString(digest[:])
	if len(hexed) > 8 {
		return hexed[:8]
	}
	return hexed
}

// GetUserExamDecryptKeyWithDB 读取当前生效的解密密钥。
//
// 优先级（用户明确要求）：system_settings 有值就用它；否则用内置默认值。
//
// 返回值第二项是「是否来自默认值」，供 UI 提示与审计使用；
// **调用方不得把它写进日志或响应**（除了布尔位）。
func GetUserExamDecryptKeyWithDB(db *gorm.DB) (string, bool) {
	if db == nil {
		return UserExamDefaultDecryptKey, true
	}
	// 复用既有 helper：缺表/缺行/空值都会回落到默认值，
	// 与「配置读不出来不能用空密钥硬解」的要求一致。
	configured := getSystemSettingStringWithDefault(db, UserExamDecryptKeySettingKey, "")
	if configured == "" {
		return UserExamDefaultDecryptKey, true
	}
	return configured, false
}

// GetUserExamDecryptKeyStateWithDB 返回密钥状态（不含密钥本身），供管理台展示。
func GetUserExamDecryptKeyStateWithDB(db *gorm.DB) UserExamDecryptKeyState {
	key, usingDefault := GetUserExamDecryptKeyWithDB(db)
	return UserExamDecryptKeyState{
		Configured:     !usingDefault,
		UsingDefault:   usingDefault,
		KeyHint:        userExamKeyHint(key),
		DefaultKeyHint: userExamKeyHint(UserExamDefaultDecryptKey),
	}
}

// SetUserExamDecryptKeyWithDB 写入管理员自定义密钥。
//
// 空字符串表示**清空并回落到内置默认值**（不是「禁用功能」）——
// 「清空 = 恢复出厂」比「清空 = 功能不可用」更符合直觉，也让管理员有退路。
//
// 校验：
//   - 去掉首尾空白后不得超过 32 字节（SQLite3MC 的 padPassword 会在 32 字节截断，
//     超长会让「密钥看起来设成功了、实际生效的是前 32 字节」，属于必须明确拒绝的坑）；
//   - 不得含控制字符（避免不可见差异导致「同样的密钥解不开」）。
func SetUserExamDecryptKeyWithDB(db *gorm.DB, key string) (UserExamDecryptKeyState, error) {
	if db == nil {
		return UserExamDecryptKeyState{}, ErrUserExamDecryptKeyInvalid
	}
	trimmed := strings.TrimSpace(key)
	if len([]byte(trimmed)) > userExamAES256KeyLength {
		return UserExamDecryptKeyState{}, fmt.Errorf("%w：长度不得超过 %d 字节（SQLite3MC 会在此处截断）",
			ErrUserExamDecryptKeyInvalid, userExamAES256KeyLength)
	}
	for _, ch := range trimmed {
		if ch < 0x20 || ch == 0x7f {
			return UserExamDecryptKeyState{}, fmt.Errorf("%w：不得包含控制字符",
				ErrUserExamDecryptKeyInvalid)
		}
	}
	if err := saveSystemSettingValue(db, UserExamDecryptKeySettingKey, trimmed,
		"考试客户端数据库（UserExamInfo.db）的解密密钥；留空则使用内置默认值"); err != nil {
		return UserExamDecryptKeyState{}, err
	}
	return GetUserExamDecryptKeyStateWithDB(db), nil
}
