package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 管理员账号安全状态（只读）。
//
// 背景（2026-11-10 用户拍板）：
// 早期实现会在 production 直接拒绝启动 —— 只要 users.id=1 的密码还是已知默认值
// 123456 就 errors.New 退出。这造成一个死锁：用默认库一键起 docker 的人
// **进不去后台**，也就无法通过后台改密码。
//
// 现在的处置：
//   - 启动照常进行（registry.startup_policy.go 不再返回错误）
//   - 由本接口把「仍是默认密码」这一事实告诉前端
//   - WebUI 在管理台顶部显示醒目告警，引导管理员去改密码
//
// 一条不可协商的性质：**本接口只回布尔，不回任何凭据内容**。
// 即使是管理员，也没有必要让密码（或它的 hash）出现在 HTTP 响应里 ——
// hash 泄漏后可以被离线爆破，而管理员只需要知道「要不要去改」。

// securityStatusDB 返回当前数据库句柄；未初始化时返回 nil（调用方降级）。
// 与 blacklistAdminDB / referralDB 同构：单测里没有 manager 时不能 panic。
var securityStatusDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// defaultAdminPassword 是历史遗留的已知默认密码。
//
// 它只用于**比较**，不用于生成 —— 新库的管理员密码来自 TIKU_ADMIN_PASSWORD
// 或开发模式的 123456（见 registry.resolveAdminBootstrap）。
const defaultAdminPassword = "123456"

// AdminSecurityStatus 返回管理员账号的安全告警位。
//
//	GET /adapter-service/admin/security-status   （Auth + RequireAdmin）
//
// 响应：
//
//	{
//	  "defaultAdminCredential": true,   // 当前管理员仍用默认密码 123456
//	  "defaultAdminUserId": 1           // 固定 1；字段存在是为了让前端文案能指名
//	}
//
// 降级：数据库未初始化时返回 200 + defaultAdminCredential=false。
// 不返回 5xx —— 一个「查不到安全状态」的接口把整个管理台首页弄红，
// 代价远大于收益；真正的异常由日志记录。
func AdminSecurityStatus(c *gin.Context) {
	response := gin.H{
		"defaultAdminCredential": false,
		"defaultAdminUserId":     1,
	}
	db := securityStatusDB()
	if db == nil {
		logger.SysWarn("admin security status: database not initialized; reporting no warning")
		c.JSON(http.StatusOK, response)
		return
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		c.JSON(http.StatusOK, response)
		return
	}
	var row struct{ Password string }
	err := db.Table(entity.TableNameUser).Select("password").Where("id = ?", 1).Take(&row).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			// 只记日志，不把错误细节回给前端（可能含 SQL/DSN 片段）。
			logger.SysWarn("admin security status: read admin credential failed: " + err.Error())
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if row.Password != "" {
		if matched, _ := util.VerifyPassword(row.Password, defaultAdminPassword); matched {
			response["defaultAdminCredential"] = true
		}
	}
	c.JSON(http.StatusOK, response)
}
