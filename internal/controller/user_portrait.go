package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 用户画像的两个 HTTP 入口（task_doc/98 §0，用户 2026-11-13 决策）。
//
//	GET /adapter-service/admin/portrait/overview      全站整体画像（侧边栏「用户画像」页默认视图）
//	GET /adapter-service/admin/users/:id/portrait     单个用户画像（用户管理操作列「画像」按钮）
//
// # 两个入口，一个页面
//
// 用户要的是「一个页面的两种视角」，不是两个独立页面：侧边栏的「用户画像」页默认
// 展示整体画像，页面上的用户搜索框选中某人后切到该用户画像。这两个 handler 返回
// **同一个结构体**（service.UserPortrait）的两种 scope，前端因此只写一份渲染，
// 不存在「整体页和单用户页口径不一致」这类只能靠肉眼发现的问题。
//
// # 四层权限的第 ② 层（handler 内二次校验）
//
// `internal/api/router.go` 里这两条路由都挂了 `controller.Auth` + `middleware.RequireAdmin`，
// 但 98 号 §0 明确要求 handler **进入时再校验一次**，不能只靠路由中间件 ——
// 理由是路由一旦被复制/新增到别的 group（本项目已有先例：33 个 admin handler
// 已实现但未挂路由），少挂一个中间件就等于把这些数据对外开放，且不报错。
// 变异验证见 TestPortraitHandlersRecheckRoleInsideHandler。

const portraitForbiddenMessage = "仅管理员可查看用户画像"

// requirePortraitAdmin 是 handler 层的角色二次校验。
//
// 返回 false 表示已经写过响应，调用方必须立即 return。
//
// ⚠️ 不复用 requireAdmin：那个 helper 在写 403 之后返回 false，调用方若漏写 return
// 会继续执行；本函数语义相同但文案独立（98 号要求画像的拒绝文案可辨认）。
func requirePortraitAdmin(c *gin.Context) bool {
	operator, ok := currentOperator(c)
	if !ok {
		// currentOperator 已回过 401。
		return false
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": portraitForbiddenMessage})
		return false
	}
	return true
}

// portraitDatabase 取画像用的数据库句柄。
//
// 抽成可替换变量：单测要能注入 sqlite 内存库，且与 blacklistAdminDB / referralDB
// 同一套约定（没有 manager 时返回 nil，不 panic）。
var portraitDatabase = func() *gorm.DB {
	return dao.User.UnderlyingDB()
}

// portraitSession 返回一个低优先级、带请求上下文的会话。
//
// 用 PriorityLow：画像是纯运营查询，不该抢答题链路的数据库连接。
func portraitSession(c *gin.Context) (*gorm.DB, bool) {
	db := portraitDatabase()
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库未初始化"})
		return nil, false
	}
	return withDatabasePriority(db, databaseRequestContext(c), dbqueue.PriorityLow), true
}

// AdminUserPortraitOverview 全站整体画像。
//
// GET /adapter-service/admin/portrait/overview
func AdminUserPortraitOverview(c *gin.Context) {
	if !requirePortraitAdmin(c) {
		return
	}
	db, ok := portraitSession(c)
	if !ok {
		return
	}
	portrait, err := service.BuildUserPortraitWithDB(db, service.PortraitScope{})
	if err != nil {
		logger.SysError("读取整体用户画像失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取用户画像失败"})
		return
	}
	c.JSON(http.StatusOK, portrait)
}

// AdminUserPortrait 单个用户画像。
//
// GET /adapter-service/admin/users/:id/portrait
//
// ⚠️ 路径参数是**目标用户**，与当前登录用户无关。service 层签名里没有「操作者」
// 这个概念（98 号 §0 第 ③ 层），这里也不传 —— 从签名上就断掉「查自己」的误用路径。
func AdminUserPortrait(c *gin.Context) {
	if !requirePortraitAdmin(c) {
		return
	}
	id, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户参数错误"})
		return
	}
	db, ok := portraitSession(c)
	if !ok {
		return
	}
	portrait, err := service.BuildUserPortraitWithDB(db, service.PortraitScope{UserID: int32(id)})
	switch {
	case errors.Is(err, service.ErrPortraitUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	case errors.Is(err, service.ErrPortraitScopeInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户参数错误"})
		return
	case err != nil:
		logger.SysError("读取用户画像失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取用户画像失败"})
		return
	}
	c.JSON(http.StatusOK, portrait)
}
