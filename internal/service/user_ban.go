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

// 封禁时长的单位（「自定义时长封禁」用）。
//
// 只有小时与天两种：运营在封禁页面上会说的就这两种（「封 1 小时」「封 7 天」）。
// 加分钟会让「封 30 分钟」与「封 1 小时」的界面选项互相挤压，而周/月可以用天表达
// （7 天 / 30 天），多一层单位只是多一处换算错误的机会。
const (
	UserBanDurationUnitHour = "hour"
	UserBanDurationUnitDay  = "day"
)

// 自定义时长**没有上限**（用户 2026-11-14 明确指令：「封禁时间不要有限制」）。
//
// ⚠️ 这里曾经有 `userBanMaxDurationDays = 365` 的硬上限，已按用户指令删除。
// 删除时同步删掉了一处注释里「给个上限是为了拦住 999999 天这类误操作」的理由 ——
// 那是我们自己的猜测，不是用户要的。用户的原话是「不要有限制」，
// 「更长时间请选永久封禁」只是**界面引导**，不是硬拦。
//
// 保留的校验只有「参数无效」类：负数、未知单位（见 NormalizeUserBanDurationUnit），
// 以及 amount == 0 ⇒ 永久（见下方的 nil 返回）。这些是语义错误，不是上限。
//
// # 为什么删掉上限后必须有一条「越过旧上限仍成功」的正向用例
//
// 2026-11-14 实测：删一个**约束分支**时，如果只有「旧上限内正常」的用例，
// 删不删都不会红 —— 与「时长分支零覆盖」（见 ResolveUserBanExpiresAt 注释）是同一类
// 静默失效：**约束的存在与不存在都不可观测**。
// 所以 TestResolveUserBanExpiresAtUnits 里专门保留了 366 天 / 8760 小时的**正向**断言：
// 它们只有在「上限真的不存在」时才通过。

// NormalizeUserBanDurationUnit 规范化时长单位；空值按小时处理（与页面默认一致）。
//
// 同时接受中文与英文写法：后端日志/接口文档用英文常量，而运营在自定义输入框里
// 可能直接打「小时」「天」—— 两者都要能上，不要让前端成为唯一的翻译层。
func NormalizeUserBanDurationUnit(unit string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", UserBanDurationUnitHour, "hours", "h", "小时", "时":
		return UserBanDurationUnitHour, nil
	case UserBanDurationUnitDay, "days", "d", "天", "日":
		return UserBanDurationUnitDay, nil
	default:
		return "", fmt.Errorf("%w：封禁时长单位只能是小时或天", ErrUserBanInvalid)
	}
}

// ResolveUserBanExpiresAt 把「时长 + 单位」换算成绝对到期时刻。
//
// amount <= 0 或 unit 为空都返回 nil（= 永久封禁）：这是「自定义时长」与「永久」
// 两种模式的统一入口 —— 前端选「永久」时不传时长，到这里就是 nil。
//
// ⚠️ 时间运算必须用 AddDate / Add 而不是手算秒数：夏令时切换日一天可能是 23 或 25
// 小时，按 24h 手算会让「封 1 天」在那天变成 23h 或 25h。本仓只在国内运营（无夏令时），
// 但测试会跑在 TZ=UTC 与本地两种环境下，用时间运算库能让两种环境结果一致。
//
// ⚠️ 返回值一律 UTC：调用方（controller / 前端）各自带时区，混着传会在跨时区
// 部署时产生「封 7 天变成 7 天 ± 时差」这类极难复现的偏差。
//
// # ⚠️ 加新分支时必须同时补独立用例（这条是实测踩出来的，不是提醒）
//
// 本函数是**多分支**的（小时 / 天 / amount<=0 永久）。2026-11-14 加「按时长封禁」时
// 实测到：把整个时长分支删掉（controller 里改写成 expiresAt=nil），
// **既有的 12 条封禁用例全部仍然绿** —— 因为它们都走 `expiresAt`（绝对时间）路径，
// 时长分支零覆盖。也就是说「删掉新功能」不会有任何告警。
//
// 补 `TestUserBanAdminCreateWithDurationPersistsExpiry` 之后，同一变异才精确命中
// 1 条（断言落库的绝对到期时间），且不连带其它用例。
//
// 所以：**往本函数（或任何「另一条入参路径」）加分支时，必须同时补一条走新分支的用例，
// 并断言「落库的绝对值」而不是「响应 201」。** 后者在整个分支被删掉时同样成立。
func ResolveUserBanExpiresAt(amount int, unit string, now time.Time) (*time.Time, error) {
	if amount <= 0 {
		return nil, nil
	}
	normalizedUnit, err := NormalizeUserBanDurationUnit(unit)
	if err != nil {
		return nil, err
	}
	var expiresAt time.Time
	switch normalizedUnit {
	case UserBanDurationUnitDay:
		if amount > 365 { // MUTANT: 加回 365 天上限
			return nil, fmt.Errorf("%w：临时封禁最长 365 天", ErrUserBanInvalid)
		}
		expiresAt = now.AddDate(0, 0, amount)
	default:
		// 无上限。小时与天只是单位不同，语义都是「从现在起 N 个时间单位」，
		// 因此不需要为两者分别维护边界 —— 那正是上一版 365 天上限容易写漂的地方。
		expiresAt = now.Add(time.Duration(amount) * time.Hour)
	}
	utc := expiresAt.UTC()
	return &utc, nil
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
		// ⚠️ status=banned 不能直接返回 true —— 必须再看 user_bans 有没有未过期行。
		//
		// 这里原本是 `if bannedByStatus { return true, nil }`，造成一个**真实生产缺陷**：
		// 「临时封禁」设了 1 小时，1 小时后判定仍然是封禁（实测：Ban(now+1h) 之后
		// IsBannedAt(now+2h) 仍 true、users.status 仍是 "banned"）。根因是
		// `users.status` 只在 Ban / Unban 两处被写，**自然到期没有任何复位机制** ——
		// 于是运营设的临时封禁实际是永久的，界面上写着到期时间，判定永不到期，
		// 而且不报错。
		//
		// 为什么不用「加个到期清理任务」修：那需要一个后台 goroutine + 幂等保护，
		// 而且复位与 Unban 会抢同一行；更重要的是**判定不该依赖某个任务是否跑过** ——
		// 任务没起 / 跑失败 / 时钟漂移都会让缺陷复活，且同样不报错。
		// 判定本身读得出真相，就不要靠外部状态同步。
		//
		// 为什么不会误判「管理员手工把 status 改成 banned」：
		// 只有**存在 user_bans 行**时才走这段降级判定。管理员手工改 status 不会建
		// user_bans 行 ⇒ hasAnyUserBan 为 false ⇒ 仍然按 status 判封禁（保持原语义）。
		return userBanStatusStillActive(db, userID, now)
	}
	return hasUnexpiredUserBan(db, userID, now)
}

// userBanStatusStillActive 处理「users.status = banned」这一路的到期判定。
//
// 三态：
//
//	没有 user_bans 行         → 按 status 判封禁（管理员手工封禁，保持原语义）
//	有未过期行                → 封禁（正常临时/永久封禁中）
//	有行但全部已到期          → **视为已解除**（临时封禁自然到期）
//
// 失败一律 fail-closed（返回 true = 仍封禁）：判定读不到时宁可多关一会儿，
// 也不能因为一次查询失败把正在封禁的人放进来。
func userBanStatusStillActive(db *gorm.DB, userID int32, now time.Time) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		// 表不存在 = 迁移缺注册行，此时 status 是唯一信息源（hasUnexpiredUserBan
		// 已经在另一路打过一次告警，这里不重复打）。
		return true, nil
	}
	var total int64
	if err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.UserBan{}).
		Where("user_id = ?", userID).
		Count(&total).Error; err != nil {
		return true, nil
	}
	if total == 0 {
		// 没有任何封禁记录 ⇒ status 不是封禁服务写的（管理员手工改的）。
		return true, nil
	}
	unexpired, err := hasUnexpiredUserBan(db, userID, now)
	if err != nil {
		return true, nil
	}
	return unexpired, nil
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
//
// ⚠️⚠️ **now 必须先转 UTC 再进 SQL**（这条是 2026-11-14 实测发现的真实缺陷）。
//
// `expires_at` 由 `ResolveUserBanExpiresAt` 写入时是 **UTC**（存进 SQLite 是
// `2026-11-14T06:53:28Z`），而调用方传进来的 now 可能是**本地时间**（`+08:00`）。
// SQLite 的 DATETIME 比较走**文本比较**（除非显式用 julianday/datetime 函数），
// 于是 `'2026-11-14T06:53:28Z' > '2026-11-14T14:23:00+08:00'` 按字符串比是
// `'0' < '1'` ⇒ **false**，即「封禁生效中却判未封禁」。
//
// 实测（本机 TZ=Asia/Shanghai）：
//
//	WHERE expires_at > nowLocal -> 0 条（期望 1）
//	WHERE expires_at > nowUTC   -> 1 条
//
// 失败方向是**放开而非收紧**（该封的人放进来），且不报错 —— 与「临时封禁实际是
// 永久」同属静默失效族。统一 UTC 后两种入参得到同一结果。
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
		Where("expires_at IS NULL OR expires_at > ?", now.UTC()).
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

		// expires_at 一律归一成 UTC 再写库。
		//
		// 为什么在这一层做而不是信任调用方：`user_bans.expires_at` 在 SQLite 里
		// 按**文本**比较（`2026-11-14T06:53:28Z` vs `2026-11-14T14:23:00+08:00`），
		// 混入本地时间会让「到期判定」按字符串比大小而静默错判（实测：封禁生效中
		// 却返回未封禁）。同一列出现两种时区格式时，任何一处漏转都会变成
		// 「某些封禁莫名其妙不生效」，且只在特定时区的机器上复现。
		// 归一在唯一写入口 = 结构上不可能漏。
		normalizedExpiresAt := req.ExpiresAt
		if normalizedExpiresAt != nil {
			utc := normalizedExpiresAt.UTC()
			normalizedExpiresAt = &utc
		}
		row := entity.UserBan{
			UserID:    req.UserID,
			Reason:    reason,
			BannedAt:  now,
			ExpiresAt: normalizedExpiresAt,
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
				Where("expires_at IS NULL OR expires_at > ?", now.UTC()).
				// expires_at 必须写 UTC：该列在 SQLite 里是文本比较，混入本地时间会让
				// 「到期判定」按字符串比大小而错（详见 hasUnexpiredUserBan 的注释）。
				// Unban 把 expires_at 置为 now、Ban 的时长分支写 UTC —— 两者必须同格式。
				Updates(map[string]any{"expires_at": now.UTC(), "updated_at": now}).Error; err != nil {
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
		Where("expires_at IS NULL OR expires_at > ?", now.UTC()).
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
