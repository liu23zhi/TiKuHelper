package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 把历史 `users.status='disabled'` 的账号迁移成封禁记录（user_bans）。
//
// # 用户需求原文
//
//	「请添加迁移，原先旧的禁用的账号要迁移到封禁账号。」
//
// # 为什么要迁移：disabled 与 banned 在**准入**上等效，在**运维**上不等效
//
// 两者的准入效果完全相同 —— isUserEnabled / isUserSessionAllowed /
// isUserSpendable 都只认 enabled，disabled 与 banned 一律 false。所以**迁移不改变
// 任何人的登录/消费能力**，它不是一次行为变更，而是一次**可运维性**补齐：
//
//	                disabled（旧）              banned + user_bans（新）
//	谁能看见           没人（管理页只有状态标签）  封禁管理页（/admin/user-bans）
//	为什么封          不可知                     reason 必填
//	谁封的            不可知                     banned_by
//	能否解封          只能改 status（无痕）        有解封入口（Unban）+ 解封留痕
//	到期自动解除      不支持                     支持（expires_at）
//
// 迁移后这批账号第一次出现在封禁管理页，运营才能回答「这些人为啥被禁」并走正规解封流程。
//
// # 关键设计决策：只写 user_bans，**不改 users.status**（任务书红线）
//
// 任务书明确要求「不删不改原 users.status（保留原始信息便于回滚）」。这不影响
// 判定生效：
//
//	IsBannedAt → userStatusIsBanned('disabled') = false
//	           → hasUnexpiredUserBan：存在 expires_at IS NULL 的行 → true ✅
//
// 也就是说迁移后封禁判定**由 user_bans 这一路独立成立**，不依赖 status 改动。
// 保留 status='disabled' 的好处：回滚只需删掉迁移插入的行（用 migration_batch 定位），
// 账号立刻回到迁移前的状态；而如果改了 status，回滚就得猜原值。
//
// ⚠️ 这与 user_bans_migration.go:45-48 的「不回填 users.status='banned'」**不矛盾**：
// 那里拒绝的是「给已有的 banned 账号凭空造 reason / banned_by / banned_at」——
// 那会产出假审计记录。本迁移的对象是 **disabled** 账号，它们当前**没有任何封禁记录**，
// 我们不是伪造已有记录，而是**首次登记**它们「处于被限制状态」这件事，
// 并用 FromLegacyDisabled 标记这一行的来源，使审计可区分「迁移」与「人工封禁」。
//
// # 幂等的三层保证
//
//	a) system_settings 标记（userBinding... 同款键名风格）：一次性闸门。
//	b) **每条插入前按 (user_id, 来源) 查重**：标记写入失败或迁移中途崩溃时，
//	   下次启动会重跑；此时已迁移过的用户不能被再插一行（否则管理页上
//	   同一个人像被封了两次，而解封只处理其中一条的语义会混乱）。
//	c) 迁移是**幂等收敛**的：每次重跑只补「还没登记过的 disabled 用户」，
//	   不会重复插入，也不会因为部分成功而永久漏掉剩下的。
//
// 之所以不用「user_bans 唯一索引」做第二道保险：user_bans 上没有唯一约束，
// 而且**不应该加** —— 同一用户被解封后再封禁是合法历史（多条行是审计事实，
// 见 entity.UserBan 注释「已过期的历史行不动，新记录是新的一行」）。
// 加唯一约束会把这条业务语义彻底堵死。所以查重只能按「来源标记」精确判。
const (
	// legacyDisabledBanMigrationKey 是一次性标记键。
	// 带 _v1 与 user_bans_schema_v1 同款：将来若需重跑，换 _v2 即可。
	legacyDisabledBanMigrationKey = "legacy_disabled_to_ban_v1"

	// legacyDisabledBanSource 写进 user_bans.reason 的前缀，用于**精确识别迁移来源**。
	//
	// 必须可 grep、可回滚：运维要能一条 SQL 捞出「哪些封禁是迁移来的」。
	// 用固定前缀而不是独立列，是因为 user_bans 没有 source 列，且
	// 「新增审计列」的收益抵不上额外迁移成本 —— 前缀足以判等与展示。
	legacyDisabledBanSource = "历史禁用账号迁移"

	// legacyDisabledBanBatchSize 控制单批处理的用户数。
	// 与 userBindingRecordBackfillBatchSize 同量级：够大以免逐行往返，
	// 又不会一次性把几十万用户装进内存。
	legacyDisabledBanBatchSize = 500
)

// EnsureLegacyDisabledUsersMigratedToBan 把 status='disabled' 的账号登记为永久封禁。
//
// 幂等；失败只记日志不阻断启动（数据类回填失败与结构性失败的区别，与
// user_binding_record_migration.go 的纪律一致：结构没建好必须 FATAL，
// 数据没迁完可以下次再来）。
//
// 返回本次**新插入**的行数（便于测试与日志；重复调用第二次返回 0）。
func EnsureLegacyDisabledUsersMigratedToBan(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	// 前置：两张表都得在，否则无从迁移。缺表**不报错**（不是结构性失败，
	// 而是本迁移的输入还不存在），下次启动会重跑。
	if !db.Migrator().HasTable(entity.TableNameUser) ||
		!db.Migrator().HasTable(entity.TableNameUserBan) ||
		!db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return 0, nil
	}
	if systemSettingFlagIsSet(db, legacyDisabledBanMigrationKey) {
		return 0, nil
	}

	migrated, err := migrateLegacyDisabledUsers(db)
	if err != nil {
		// 数据类失败：不写标记、不阻断启动，下次启动重试。
		// 幂等收敛保证重试只补漏掉的部分。
		return migrated, err
	}
	if err := setSystemSettingFlag(db, legacyDisabledBanMigrationKey); err != nil {
		// 标记写失败不算迁移失败：行已经插进去了，下次启动会重跑，
		// 而重跑被查重逻辑挡住（不产生重复行）—— 只多一次扫描。
		logger.SysLog(fmt.Sprintf("写入「历史禁用账号迁移」标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return migrated, nil
}

// migrateLegacyDisabledUsers 分批把 disabled 账号写进 user_bans。
//
// 只选**真正被禁用**的：`LOWER(TRIM(status)) = 'disabled'` 精确匹配，
// 不用 LIKE、不把 enabled / closure_pending / banned / 空串扫进来。
// 大小写与首尾空格按 normalizeUserStatus 的口径归一后比较
// （写入口已归一，但历史脏数据可能带空格）。
func migrateLegacyDisabledUsers(db *gorm.DB) (int64, error) {
	now := time.Now()
	var total int64
	var cursor int32

	for {
		var candidates []entity.User
		if err := db.Session(&gorm.Session{NewDB: true}).
			Select("id", "username", "status").
			Where("LOWER(TRIM(COALESCE(status, ''))) = ?", "disabled").
			Where("id > ?", cursor).
			Order("id asc").
			Limit(legacyDisabledBanBatchSize).
			Find(&candidates).Error; err != nil {
			return total, fmt.Errorf("查询待迁移的禁用账号失败: %w", err)
		}
		if len(candidates) == 0 {
			break
		}
		// 查重改成**每批一次查询**：先取出本批里已迁移过的 user_id 集合，
		// 再逐条判。逐用户 Count 会变成 N+1（500 个用户 = 501 次往返），
		// 迁移在启动路径上，多一次往返就是多一份启动延迟。
		batchIDs := make([]int32, 0, len(candidates))
		for _, user := range candidates {
			batchIDs = append(batchIDs, user.ID)
		}
		var alreadyMigrated []int32
		if err := db.Session(&gorm.Session{NewDB: true}).
			Model(&entity.UserBan{}).
			Where("user_id IN ?", batchIDs).
			Where("reason = ?", legacyDisabledBanSource).
			Pluck("user_id", &alreadyMigrated).Error; err != nil {
			return total, fmt.Errorf("检查本批用户是否已迁移失败: %w", err)
		}
		skip := make(map[int32]struct{}, len(alreadyMigrated))
		for _, id := range alreadyMigrated {
			skip[id] = struct{}{}
		}

		for _, user := range candidates {
			cursor = user.ID
			if _, done := skip[user.ID]; done {
				continue
			}
			if err := insertLegacyDisabledBan(db, user, now); err != nil {
				return total, err
			}
			total++
		}
		if len(candidates) < legacyDisabledBanBatchSize {
			break
		}
	}

	// 迁移数量必须写日志：这是运维唯一能确认「迁移真的跑了、跑了多少」的锚点。
	// 文案固定 —— 测试与运维都会按它判读，改文案需同步改测试。
	logger.SysLog(fmt.Sprintf("历史禁用账号迁移：本次新增 %d 条封禁记录（来源标记=%q，原 users.status 保持不变）",
		total, legacyDisabledBanSource))
	return total, nil
}

// insertLegacyDisabledBan 为一个 disabled 用户插入封禁登记行。
//
// ⚠️ 查重在调用方按批做完（不是这里逐条查）：见 migrateLegacyDisabledUsers 里
// 「N+1」的说明。本函数只负责插入。
func insertLegacyDisabledBan(db *gorm.DB, user entity.User, now time.Time) error {
	row := entity.UserBan{
		UserID: user.ID,
		Reason: legacyDisabledBanSource,
		// BannedAt 用迁移时刻：我们无法得知「当年什么时候被禁用的」，
		// users 表也没有记录禁用时间。用零值（0001-01-01）会显得像脏数据，
		// 用迁移时刻至少语义诚实：「这条封禁记录是迁移时建立的」。
		// 真实封禁起始时间已不可考，不能编 —— 编一个近似的比对账更糟。
		BannedAt: now,
		// ExpiresAt = nil ⇒ 永久封禁。用户原话是「迁移到封禁账号」，
		// 没有说期限；disabled 的既有语义也是「一直禁用直到管理员手动改」，
		// 迁移成永久的才是语义等价的，设成临时会让这批账号自动解封（行为变更）。
		ExpiresAt: nil,
		// BannedBy = 0 ⇒ 系统自动（entity.UserBan 注释：0 表示系统/风控触发）。
		// 迁移不是任何管理员的操作，记成 0 是唯一正确的归因。
		BannedBy: 0,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&row).Error; err != nil {
		return fmt.Errorf("为禁用账号 %d 写入封禁记录失败: %w", user.ID, err)
	}
	return nil
}

// legacyDisabledBanRows 返回本迁移插入的行数（测试用；生产不调用）。
//
// 单独抽出来是为了让测试不必重复写 reason 前缀常量：测试若手写字面量，
// 常量一改测试就与实现一起漂移 —— 那是「自证式假钉子」的典型形态。
func legacyDisabledBanRows(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	var count int64
	err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.UserBan{}).
		Where("reason = ?", strings.TrimSpace(legacyDisabledBanSource)).
		Count(&count).Error
	return count, err
}
