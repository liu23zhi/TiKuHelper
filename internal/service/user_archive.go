package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 用户归档（软删除）的核心逻辑，task_doc/76 §3.2 + task_doc/77。
//
// # 为什么不再物理删除
//
// 旧实现是 `dao.User.Delete(user)`，用户行直接消失，造成三个不可逆后果：
//  1. 该用户的 user_binding_records 变成孤儿行（user_id 指向不存在的用户），
//     保留区无从回看「这个手机号以前属于谁」；
//  2. 二次注册判定（19/44 号四个开关）与换绑追缴失去数据源；
//  3. 已发放的赠金 / 返佣 / 评价返现无法追溯来源。
//
// # 为什么用 gorm.DeletedAt
//
// 官方软删除类型会让 dao.User 的所有既有查询**自动**排除已归档用户，
// 不需要在每一处查询里手补 `WHERE deleted_at IS NULL`。
// 自造列的话漏改任何一处，已归档用户就会重新出现在用户列表里。
//
// 代价：用 Unscoped() 的地方仍会看到已归档用户 —— 归档相关代码要用它，
// 其余地方不要。

// ArchiveUserInTx 把一个用户标记为已归档。
//
// 幂等：已归档的用户再次调用时只更新归档原因与操作者，不重复置时间戳
// （保留首次归档时点，那才是事实）。
//
// ⚠️ 该方法**不释放绑定** —— 调用方必须先在**同一个事务**里调用
// ReleaseAllBindingsForArchiveInTx，否则活跃绑定会继续占
// (kind, active_key) 唯一索引，别人再绑同一个手机号会撞唯一约束。
func ArchiveUserInTx(tx *gorm.DB, user entity.User, reason string, operatorUserID int32) error {
	if tx == nil {
		return fmt.Errorf("数据库不可用")
	}
	if user.ID <= 0 {
		return fmt.Errorf("归档用户缺少用户 ID")
	}
	reason = strings.TrimSpace(reason)
	switch reason {
	case entity.UserArchivedReasonAdminDelete, entity.UserArchivedReasonUserClosure:
	default:
		// 拒绝未知原因：archived_reason 直接决定审计口径与 release_source，
		// 随便写一个值会让事后统计失效且无从修复。
		return fmt.Errorf("归档原因非法：%s", reason)
	}

	now := time.Now()
	updates := map[string]any{"deleted_at": now}
	// 逐列探测后只写存在的列。
	//
	// 为什么要这么麻烦：ensureUserArchiveSchema 建的列与业务代码可能不同步
	// （单测精简库、只跑 AutoMigrate 的旧部署、迁移标记被误删后又回滚版本）。
	// 硬写不存在的列会让**归档整个失败** —— 结果就是「用户删不掉」，
	// 这比少记一个字段严重得多。
	//
	// 生产环境四列都在（迁移保证），这条探测在真实部署里总是全通过。
	if tx.Migrator().HasColumn(entity.TableNameUser, "archived_reason") {
		updates["archived_reason"] = reason
	}
	if tx.Migrator().HasColumn(entity.TableNameUser, "archived_by_user_id") {
		updates["archived_by_user_id"] = operatorUserID
	}
	// 只在首次归档时写 archived_at：重复归档不该改掉「第一次是什么时候」。
	if tx.Migrator().HasColumn(entity.TableNameUser, "archived_at") {
		var existing entity.UserArchive
		err := tx.Session(&gorm.Session{NewDB: true}).Unscoped().
			Select("id", "archived_at").
			Where("id = ?", user.ID).Take(&existing).Error
		if err != nil {
			return fmt.Errorf("读取用户归档状态失败: %w", err)
		}
		if existing.ArchivedAt == nil {
			updates["archived_at"] = now
		}
	}

	result := tx.Session(&gorm.Session{NewDB: true}).Unscoped().
		Model(&entity.UserArchive{}).
		Where("id = ?", user.ID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("归档用户失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("归档用户失败：用户不存在")
	}
	return nil
}

// ReleaseAllBindingsForArchiveInTx 释放某用户当前所有活跃绑定。
//
// 归档原因决定 release_source，两者不可混用（task_doc/76 §3.2.4）：
//
//	admin_delete → admin_delete
//	user_closure → account_close
//
// release_reason 一律是 'closure' —— 对二次注册判定来说，两种归档
// 都是「注销释放」，都该归入 current_* 命中。
//
// 为什么不复用 ReleaseBindingInTx：那个函数按 kind 单次释放，
// 这里要「释放该用户的全部活跃绑定」，且要在 kind 未知时也能工作。
// 内部仍走同一套 state/active_key 翻转逻辑，保证一致。
func ReleaseAllBindingsForArchiveInTx(tx *gorm.DB, userID int32, archivedReason string, operatorUserID int32) error {
	if tx == nil {
		return fmt.Errorf("数据库不可用")
	}
	if userID <= 0 {
		return fmt.Errorf("释放绑定缺少用户 ID")
	}
	// 绑定表在极端场景下可能还没建好（空库首次启动、单测精简库）。
	// 此时**跳过释放**而不是让归档失败：绑定的价值是「让别人能重新绑同一个值」，
	// 表都没有意味着这个机制本身还没上线，不该阻断「把用户移出活跃列表」。
	if !tx.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil
	}
	releaseSource := entity.BindingReleaseSourceAdminDelete
	if strings.TrimSpace(archivedReason) == entity.UserArchivedReasonUserClosure {
		releaseSource = entity.BindingReleaseSourceAccountClose
	}

	var rows []entity.UserBindingRecord
	// `state != 'voided'` 显式写出（需求 7）：归档只释放 active 行，
	// 但作废行的隔离必须是可 grep 的约定，不能靠「另一个条件刚好排除了它」。
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Where("user_id = ? AND state = ? AND state != 'voided'", userID, entity.BindingStateActive).
		Order("id asc").Find(&rows).Error; err != nil {
		return fmt.Errorf("读取活跃绑定失败: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	now := time.Now()
	for i := range rows {
		// active_key 必须同时置 NULL：只改 state 会继续占着唯一索引，
		// 别人再绑这个值就撞唯一约束（报错文案与手机号毫无字面关系）。
		updated := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
			Where("id = ? AND state = ?", rows[i].ID, entity.BindingStateActive).
			Updates(map[string]any{
				"state":            entity.BindingStateReleased,
				"active_key":       nil,
				"released_at":      now,
				"release_reason":   entity.BindingReleaseReasonClosure,
				"release_source":   releaseSource,
				"operator_user_id": operatorUserID,
			})
		if updated.Error != nil {
			return fmt.Errorf("释放绑定 %d 失败: %w", rows[i].ID, updated.Error)
		}
	}
	return nil
}

// ArchiveUserSnapshot 是写进操作日志的账号快照。
//
// ⚠️ 显式挑选字段，**不做整体序列化** —— 整体序列化会把 password hash
// 一起写进 logs 表，而凭据不该在多个地方留存（task_doc/77 §3.2.3）。
//
// 这些字段够管理员判断「删的是谁、当时什么状态」，
// 不足以还原凭据或冒充登录。
func ArchiveUserSnapshot(user entity.User) map[string]any {
	return map[string]any{
		"userId":      user.ID,
		"username":    user.Username,
		"nickname":    user.Nickname,
		"role":        user.Role,
		"status":      user.Status,
		"balance":     user.Balance,
		"giftBalance": user.GiftBalance,
		"createdAt":   user.CreatedAt,
	}
}
