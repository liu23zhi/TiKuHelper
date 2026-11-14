package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"gorm.io/gorm"
)

// 存储状态端点（task_doc/108:297、109:474/486；Q7-a 按「WARN + 前端悬浮」实现）。
//
//	GET /api/system/storage-status
//
// # 为什么这个端点**不**要 RequireAdmin
//
// 警告的对象是**所有人**，不是运维。108:297 的措辞是「当使用 Sqlite 启动时候，
// 需要给与警告」—— 没有区分角色。若挂 RequireAdmin，普通用户永远看不到横幅，
// 这条需求就只兑现给了管理员一个人。
//
// 但它仍然挂 `controller.Auth`（不是完全公开），两条理由：
//  1. 悬浮条挂在已登录布局里，未登录用户本来就没有承载位置；
//  2. 公开暴露「这台实例用的是 SQLite」等于免费给出一个指纹，而这条信息对
//     攻击者的价值（「可以试试 SQLite 特有面」）大于对匿名访客的价值。
//
// # 响应一个字段都不能多
//
// 见 registry.StorageStatus 的注释：只回 driver / warning / production。
// **不回**数据库文件路径、DSN、账号、版本号 —— 前三个是凭据面，
// 版本号是额外指纹且与本需求无关。
//
// # 降级
//
// manager 未初始化（单测、启动失败路径）→ 200 + driver=mysql + 空 warning。
// 不返回 5xx：一个「查不到存储状态」的接口把整个前端弄成错误态，
// 代价远大于收益；而且真的处于 SQLite 时 manager 必然已初始化。
func StorageStatus(c *gin.Context) {
	status := registry.StorageStatusForDB(storageStatusDB())
	c.JSON(http.StatusOK, status)
}

// storageStatusDB 返回当前数据库句柄；未初始化时返回 nil（registry 侧按 mysql 降级）。
//
// 做成包级变量是为了单测能替换（与 securityStatusDB / blacklistAdminDB 同构）。
var storageStatusDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}
