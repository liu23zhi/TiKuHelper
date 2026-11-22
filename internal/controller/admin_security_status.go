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

// securityStatusDB 返回当前数据库句柄；未初始化时返回 nil（调用方降级）。
// 与 blacklistAdminDB / referralDB 同构：单测里没有 manager 时不能 panic。
var securityStatusDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

const defaultAdminPassword = util.FactoryAdminPassword

// AdminSecurityStatus 保留既有管理接口，只读查询管理员是否在使用默认密码；
// 这个状态不限制启动或登录，WebUI 不显示首次设置提示。
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
