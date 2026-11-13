package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 账号封禁（user_bans）与**双读判定**（task_doc/121 §4.8 + 121 决策 6 / 46 号 §5）。
//
// # 本文件唯一不能做错的事：双读
//
// 121 决策 6 明确要求：封禁判定必须同时读两处，**任一命中都算封禁**：
//
//	① users.status = 'banned'（既有口径，存量封禁全在这里）
//	② user_bans 里存在未过期行（expires_at 为空 = 永久；或 expires_at > now）
//
// 为什么不能只读一处：
//
//	只读 users.status → 临时封禁到期后永远解不开（除非有人手工改 status），
//	                    且 user_bans 写了也没有任何效果 —— 表现为「封禁点了没反应」。
//	只读 user_bans   → 所有存量封禁（迁移前的 'banned' 用户）立刻全部解封，
//	                    且不报错。这是本项最危险的缺陷形态：上线即静默解封一批人。
//
// 因此判定函数是 IsBannedAt（可注入时钟），IsBanned 只是它的 time.Now() 包装。
// 时钟必须可注入：到期判定用真实时间写测试只能造「1 秒后过期」这种脆用例，
// 而「已过期」与「未过期」两条分支恰恰是本项的核心判据。
//
// # 到期不删行
//
// 到期只让判定失效，user_bans 行永久保留：「谁在什么时候因为什么封了谁」是审计事实，
// 删了就再也解释不清楚（46 号 §5 与 blacklist_entries.expires_at 同一套口径）。
// Unban 同样不删行 —— 它把未过期行的 expires_at 置为当前时刻（等于「提前到期」），
// 记录与原因全部留下。
//
// # 无外键
//
// user_id 只是逻辑关联（users 是软删除表，外键对归档无意义），见 entity.UserBan 注释。

const (
	// UserBanStatusBanned 是 users.status 的「已封禁」取值。
	//
	// ⚠️ 本仓已有多份同名常量（controller/closure.go 的 userStatusBanned、
	// service/admin_grant.go 的 adminGrantUserStatusBanned），这里是第三份。
	// 不去统一是因为三处分属不同包/不同任务边界，改动会牵连控制器的
	// normalizeUserStatus 白名单；而**值**必须逐字节相同 —— 下面
	// TestUserBanStatusConstantMatchesExistingBannedValues 会把三处的字面值钉在一起，
	// 任何一处漂移就变红（这是拿字面量比，不是拿常量比常量）。
	UserBanStatusBanned = "banned"
	// UserBanStatusEnabled 是解封时恢复的状态值。
	UserBanStatusEnabled = "enabled"
)

// 本文件的三个哨兵错误。
var (
	// ErrUserBanInvalid 表示封禁/解封参数不合法。
	ErrUserBanInvalid = errors.New("封禁参数无效")
	// ErrUserBanUserNotFound 表示目标用户不存在（或已归档）。
	ErrUserBanUserNotFound = errors.New("目标用户不存在或已归档")
	// ErrUserBanDBUnavailable 表示数据库句柄不可用。
	ErrUserBanDBUnavailable = errors.New("数据库不可用")
)

// UserBanRequest 是一次封禁的入参。
type UserBanRequest struct {
	// UserID 是目标用户（users.id）。
	UserID int32
	// Reason 是封禁原因，必填。
	//
	// 为什么必填而不是给个默认值：封禁是风控动作，用户一定会来问「为什么」。
	// 一条 reason 为空的记录等于没有记录，而事后补 reason 是不可能的
	// （当时的事实已经无人记得）。这里拒绝空白，不做静默补默认值 ——
	// 静默补默认值会让「忘了填」永远不被发现。
	Reason string
	// ExpiresAt 为空 = 永久封禁；非空 = 到期自动解除（用户 2026-11-10 定案两者并存）。
	ExpiresAt *time.Time
	// OperatorUserID 是执行封禁的管理员 users.id；0 = 系统自动封禁。
	OperatorUserID int32
	// Now 是本次操作的时钟基准，零值 = time.Now()。
	//
	// 只给测试用：Ban 内部要判断「ExpiresAt 是否已经过去」，
	// 用真实时间写这条断言会随机器时钟抖动而 flaky。
	Now time.Time
}

// UserUnbanRequest 是一次解封的入参。
type UserUnbanRequest struct {
	UserID         int32
	OperatorUserID int32
	// Reason 是解封原因（可空，仅记日志，user_bans 无对应列 ——
	// 列集由 121 §4.8 固定，不额外加列；解封事实由 expires_at 被提前置位表达）。
	Reason string
	// Now 是解封时刻，零值 = time.Now()。未过期行会被置为这个时刻。
	Now time.Time
}

// userBanTableMissingWarnOnce 保证「user_bans 表不存在」只告警一次。
//
// 为什么只告警一次：判定在每一条受保护请求上都会跑，逐次告警会把日志刷爆，
// 而真正需要看到的是「第一次」—— 它说明迁移没跑（registry.db.go 的注册行缺失）。
var userBanTableMissingWarnOnce sync.Once

// IsBanned 判断用户当前是否处于封禁状态（双读：users.status 与 user_bans 任一命中）。
//
// now 取 time.Now()；需要确定性时钟的调用方（测试、批量判定）用 IsBannedAt。
func IsBanned(db *gorm.DB, userID int32) (bool, error) {
	return IsBannedAt(db, userID, time.Now())
}

// IsBannedAt 是封禁判定的**唯一实现**，now 显式传入。
//
// 返回值语义：
//
//	true  用户被封禁（状态命中 或 存在未过期封禁记录）
//	false 未被封禁
//	err   只代表**读失败**，不代表「没封禁」——调用方不能把 err 当 false 用。
//	      唯一的例外是 user_bans 表缺失：那是迁移没跑，此时降级为只读 status
//	      并只告警一次（见下），而不是让整个准入链路 500。
//
// 两条读的先后有意义：先读 users.status（单行主键查，最便宜且覆盖存量数据），
// 命中就直接返回，避免多一次 user_bans 查询。
func IsBannedAt(db *gorm.DB, userID int32, now time.Time) (bool, error) {
	if db == nil {
		return false, ErrUserBanDBUnavailable
	}
	if userID <= 0 {
		// 0 / 负数不可能是合法用户：不查库、不报错（匿名请求会走到这里）。
		return false, nil
	}
	bannedByStatus, err := userStatusIsBanned(db, userID)
	if err != nil {
		return false, err
	}
	if bannedByStatus {
		return true, nil
	}
	return hasUnexpiredUserBan(db, userID, now)
}

// userStatusIsBanned 读 users.status 并判断是否等于 banned（大小写无关）。
//
// 用户不存在（或已归档被软删除过滤掉）→ false, nil：判定层不替登录层报错，
// 登录层会自己处理「用户不存在」。
func userStatusIsBanned(db *gorm.DB, userID int32) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 表都没有 = 空库首次启动，判定无对象可判。
		return false, nil
	}
	var status string
	err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.User{}).
		Where("id = ?", userID).
		Limit(1).
		Pluck("status", &status).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("读取 users.status（id=%d）失败: %w", userID, err)
	}
	return strings.EqualFold(strings.TrimSpace(status), UserBanStatusBanned), nil
}

// hasUnexpiredUserBan 判断 user_bans 里是否存在**未过期**行。
//
// 未过期的定义（判定的第二条读，121 决策 6）：
//
//	expires_at IS NULL      → 永久封禁
//	expires_at >  now       → 尚未到期
//
// ⚠️ 边界是**严格大于**：expires_at == now 视为已到期。
// 取 >= 会让「到期时刻」那一瞬间仍然封禁，而 Unban 正是把 expires_at 置为 now ——
// 用 >= 会让解封在解封的同一瞬间无效。
func hasUnexpiredUserBan(db *gorm.DB, userID int32, now time.Time) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		// 迁移没跑（registry/db.go 缺注册行）：降级为只读 users.status 并告警一次。
		//
		// 为什么不 fail-closed（一律判封禁）：那会让每个用户在迁移缺失时都登录失败，
		// 故障面从「临时封禁失效」扩大到「全站不可用」。
		// 为什么不 fail-open 且不告警：那会让缺注册行这件事无人知晓。
		userBanTableMissingWarnOnce.Do(func() {
			logger.SysWarn(fmt.Sprintf("user_bans 表不存在，封禁判定降级为只读 users.status" +
				"（检查 registry 启动迁移是否登记 EnsureUserBansSchema）"))
		})
		return false, nil
	}
	var count int64
	err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.UserBan{}).
		Where("user_id = ?", userID).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Limit(1).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("读取 user_bans（user_id=%d）失败: %w", userID, err)
	}
	return count > 0, nil
}

// Ban 封禁一个用户：写一条 user_bans 记录，并把 users.status 置为 banned。
//
// # 为什么两处都写
//
// users.status 是既有准入谓词的读取点（controller 的 isUserEnabled 等），
// 改所有调用方去读新表风险远大于写两处。两处写入在同一个事务里，
// 因此不存在「表里有记录但 status 没变」的中间态。
//
// 判定的双读（IsBannedAt）保证即使只有一处写成功也不会漏判；
// 但写入仍坚持事务，因为审计与状态不一致会让运营看到互相矛盾的界面。
//
// # 幂等
//
// 同一用户已有**未过期**记录时，更新那条记录（reason / banned_by / banned_at /
// expires_at）而不是插新行。否则连续点两次「封禁」会留下两行，
// 管理页上看起来像被封了两次，而解除时只处理了其中一条的语义会很混乱。
//
// 已过期的历史行**不动**（它们是审计事实），新记录是新的一行。
func Ban(db *gorm.DB, req UserBanRequest) (entity.UserBan, error) {
	var record entity.UserBan
	if db == nil {
		return record, ErrUserBanDBUnavailable
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return record, fmt.Errorf("%w：封禁原因不能为空", ErrUserBanInvalid)
	}
	if len([]rune(reason)) > userBanReasonMaxRunes {
		return record, fmt.Errorf("%w：封禁原因不能超过 %d 个字符", ErrUserBanInvalid, userBanReasonMaxRunes)
	}
	if req.UserID <= 0 {
		return record, fmt.Errorf("%w：用户 ID 无效", ErrUserBanInvalid)
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
		// 到期时间在过去 = 封禁一写就失效。这种入参一定是调用方算错了时间，
		// 静默接受会让运营看到「封禁成功」而实际没人被封。
		return record, fmt.Errorf("%w：到期时间必须晚于当前时间", ErrUserBanInvalid)
	}
	if req.OperatorUserID < 0 {
		return record, fmt.Errorf("%w：操作者 ID 无效", ErrUserBanInvalid)
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return record, fmt.Errorf("%w：users 表不存在", ErrUserBanDBUnavailable)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var user entity.User
		if err := tx.Session(&gorm.Session{NewDB: true}).
			Select("id", "status").
			Where("id = ?", req.UserID).
			First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w：id=%d", ErrUserBanUserNotFound, req.UserID)
			}
			return fmt.Errorf("读取目标用户失败: %w", err)
		}

		// 先找未过期行做 upsert（幂等，见函数注释）。
		var existing entity.UserBan
		hasExisting := false
		if tx.Migrator().HasTable(entity.TableNameUserBan) {
			found, findErr := userBanUnexpiredRow(tx, req.UserID, now)
			if findErr != nil {
				return findErr
			}
			if found != nil {
				existing, hasExisting = *found, true
			}
		}

		row := entity.UserBan{
			UserID:    req.UserID,
			Reason:    reason,
			BannedAt:  now,
			ExpiresAt: req.ExpiresAt,
			BannedBy:  req.OperatorUserID,
		}
		if hasExisting {
			row.ID = existing.ID
			// 用 Updates 显式写列：ExpiresAt 为 nil 时必须真的落 NULL，
			// struct 更新会跳过零值字段，导致「改成永久封禁」静默失效。
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Model(&entity.UserBan{}).
				Where("id = ?", existing.ID).
				Updates(map[string]any{
					"reason":     row.Reason,
					"banned_at":  row.BannedAt,
					"expires_at": row.ExpiresAt,
					"banned_by":  row.BannedBy,
					"updated_at": now,
				}).Error; err != nil {
				return fmt.Errorf("更新封禁记录失败: %w", err)
			}
			row.CreatedAt = existing.CreatedAt
		} else {
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Create(&row).Error; err != nil {
				return fmt.Errorf("写入封禁记录失败: %w", err)
			}
		}

		if err := setUserStatusBanned(tx, req.UserID, now); err != nil {
			return err
		}
		record = row
		return nil
	})
	if err != nil {
		return entity.UserBan{}, err
	}
	logger.SysLog(fmt.Sprintf("已封禁用户 %d（操作者 %d）：%s",
		record.UserID, record.BannedBy, record.Reason))
	return record, nil
}

// userBanReasonMaxRunes 与 entity.UserBan.Reason 的 varchar(255) 对齐。
const userBanReasonMaxRunes = 255

// Unban 解除用户封禁：把未过期的 user_bans 行提前置为到期，并把 users.status 复位。
//
// # 为什么「置到期」而不是「删行」
//
// 删一行等于丢掉「谁在什么时候因为什么封过」，封禁是审计事实（46 号 §5）。
// 提前置到期既让双读判定立刻失效，又完整保留记录 —— 与「到期自然失效」同一条路径，
// 因此不存在「解封走的是另一套语义」这种隐性分支。
//
// # status 只在当前是 banned 时复位
//
// 无条件写 'enabled' 会把 closure_pending / disabled 等其它状态一并覆盖掉，
// 表现为「一次解封顺手把账号从注销流程里拽出来」—— 静默的状态丢失。
//
// # 幂等
//
// 没有未过期行且 status 不是 banned 时，什么都不做且不报错（重复点解封是正常操作）。
func Unban(db *gorm.DB, req UserUnbanRequest) error {
	if db == nil {
		return ErrUserBanDBUnavailable
	}
	if req.UserID <= 0 {
		return fmt.Errorf("%w：用户 ID 无效", ErrUserBanInvalid)
	}
	if req.OperatorUserID < 0 {
		return fmt.Errorf("%w：操作者 ID 无效", ErrUserBanInvalid)
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return fmt.Errorf("%w：users 表不存在", ErrUserBanDBUnavailable)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var user entity.User
		if err := tx.Session(&gorm.Session{NewDB: true}).
			Select("id", "status").
			Where("id = ?", req.UserID).
			First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w：id=%d", ErrUserBanUserNotFound, req.UserID)
			}
			return fmt.Errorf("读取目标用户失败: %w", err)
		}

		if tx.Migrator().HasTable(entity.TableNameUserBan) {
			// 只置「尚未到期」的行：已过期的历史行不再改动（改它会篡改审计事实）。
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Model(&entity.UserBan{}).
				Where("user_id = ?", req.UserID).
				Where("expires_at IS NULL OR expires_at > ?", now).
				Updates(map[string]any{"expires_at": now, "updated_at": now}).Error; err != nil {
				return fmt.Errorf("解除封禁记录失败: %w", err)
			}
		}

		// 只在当前确实是 banned 时复位，避免覆盖其它状态（见函数注释）。
		if strings.EqualFold(strings.TrimSpace(user.Status), UserBanStatusBanned) {
			if err := tx.Session(&gorm.Session{NewDB: true}).
				Model(&entity.User{}).
				Where("id = ?", req.UserID).
				Updates(map[string]any{"status": UserBanStatusEnabled}).Error; err != nil {
				return fmt.Errorf("复位用户状态失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	logger.SysLog(fmt.Sprintf("已解除用户 %d 的封禁（操作者 %d）：%s",
		req.UserID, req.OperatorUserID, strings.TrimSpace(req.Reason)))
	return nil
}

// userBanUnexpiredRow 返回该用户当前未过期的封禁行；没有则返回 nil。
//
// 多条同时未过期时取最新一行（banned_at 倒序 + id 倒序），保证连续封禁的
// upsert 目标确定 —— 没有 ORDER BY 时 GORM 返回哪一行是未定义的。
func userBanUnexpiredRow(tx *gorm.DB, userID int32, now time.Time) (*entity.UserBan, error) {
	var rows []entity.UserBan
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Model(&entity.UserBan{}).
		Where("user_id = ?", userID).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("banned_at DESC, id DESC").
		Limit(1).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取未过期封禁记录失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// setUserStatusBanned 把 users.status 置为 banned。
func setUserStatusBanned(tx *gorm.DB, userID int32, now time.Time) error {
	_ = now
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Model(&entity.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{"status": UserBanStatusBanned}).Error; err != nil {
		return fmt.Errorf("写入用户封禁状态失败: %w", err)
	}
	return nil
}
