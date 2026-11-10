package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

// 账户删除 / 归档类操作的日志写入（task_doc/77 §3.2）。
//
// # 为什么需要这个文件
//
// 题库删除早就写了日志（tiku.go:286-296 先 dao.Log.Create 再删），
// 但同类高危的用户删除**什么都没写** —— 删完无从追查是谁删的、删的是谁。
// 本文件把这条补上，并统一账户类操作的 Module / Action 取值。
//
// # Action 编号登记（写库后不可重排，task_doc/77 §3.2.2）
//
//	50 管理员删除用户（归档）
//	51 用户申请注销
//	52 撤销注销申请
//	53 注销到期归档
//	54 恢复已归档用户
//	55 管理员封禁用户
//	56 管理员解封用户
//	57 管理员调整余额
//	58 管理员修改赠送金额
//
// ⚠️ 用数字而不是字符串：logs.action 是 tinyint(4)（logs.gen.go:22），
// 改列类型会破坏既有数据与查询。新增动作只能往后追加，不能重排。
const (
	accountLogModule = "account"

	AccountActionDelete  int32 = 50
	AccountActionClosure int32 = 51
	AccountActionCancel  int32 = 52
	AccountActionArchive int32 = 53
	AccountActionRestore int32 = 54
	AccountActionDisable int32 = 55
	AccountActionEnable  int32 = 56
	AccountActionBalance int32 = 57
	AccountActionGiftSet int32 = 58
)

// recordAccountDelete 记录一次「管理员删除用户（归档）」。
//
// # 两个通道都写（task_doc/77 §3.2.4）
//
//   - logs 表         → LogList 页可见。这是用户明确要求的「日志列表」。
//   - AccountAuditEvent → 结构化的账户域审计，带 Result 字段。
//
// 两者不互斥：logs 面向运营查看，AccountAudit 面向结构化查询。
//
// # 一条不可协商的语义
//
// logs.user_id 的列注释是「谁执行的操作」（logs.gen.go:19）。
// 所以 UserID 必须是**操作者（管理员）**，被删用户的信息进 OldAnswer。
// 反过来写就无法回答「是谁删的」——这是审计最基本的问题。
func recordAccountDelete(c *gin.Context, operator *entity.User, target entity.User, reason string) {
	if operator == nil {
		return
	}
	// 快照显式挑字段，不含 password hash（service.ArchiveUserSnapshot 已按此实现）。
	snapshot := service.ArchiveUserSnapshot(target)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		encoded = []byte("{}")
	}

	reasonText := "管理员删除"
	if strings.TrimSpace(reason) == entity.UserArchivedReasonUserClosure {
		reasonText = "用户注销"
	}
	description := fmt.Sprintf("%s并归档账号：%s(ID=%d)", reasonText, target.Username, target.ID)

	// 1) 写 logs 表（LogList 页可见）。
	//    写失败只记 SysError，不回滚已完成的归档 —— 归档本身是用户可见的结果，
	//    日志是辅助信息，不该让「日志写不进去」变成「删除失败」。
	log := entity.Log{
		Qid:         0, // 非题库操作
		Module:      accountLogModule,
		Action:      AccountActionDelete,
		UserID:      operator.ID,
		Username:    operator.Username,
		Description: description,
		OldAnswer:   string(encoded),
		NewAnswer:   "",
		ClientIP:    strings.TrimSpace(c.ClientIP()),
		CreateTime:  time.Now(),
	}
	if err := dao.Log.Create(&log); err != nil {
		logger.SysError("写账户删除日志失败: " + logger.RedactSecrets(err.Error()))
	}

	// 2) 写结构化账户审计。
	recordAccountAudit(c, operator, accountLogModule, AccountActionDelete, description, map[string]any{
		"targetUserId":   target.ID,
		"targetUsername": target.Username,
		"archivedReason": reason,
		"deleted":        true,
	})
}
