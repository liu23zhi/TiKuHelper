package service

import (
	"errors"
	"maps"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 设备池上限判定（121 §4.9 / 107 Q15-Q17 / 137 设备体系设计）。
//
// 这是 user_levels.device_limit 与 users.device_limit_override 的**唯一判定点**。
// 在此之前全仓只有配置与下发（user_level.go 的编辑/摘要、system_setting.go 的开关、
// user_level_migration.go 的种子），判定点是 0 —— 也就是说「设备上限」这一档权益
// 在代码里从未生效过。
//
// ## 四个不可协商的口径
//
//  1. **device_limit = 0 ⇒ 不限**（放行，不拒绝）。
//     0 是本仓「该维度不设门槛」的统一写法（MinRechargeTotal / MinConsumptionTotal
//     的 0 也是这个语义），四个种子等级全都是 0 —— 若把 0 解释成「禁止」，
//     上线瞬间全站用户都无法登录，且没有任何配置是他们自己改坏的。
//
//  2. **判定 fail-open**（107 Q17 已定）。
//     表坏了 / 查不到 / 参数缺失 ⇒ **放行**。设备池是权益限制，不是安全边界：
//     fail-closed 的代价是「数据库抖一下全站登录失败」，fail-open 的代价只是
//     「这段时间上限不生效」。两者不对称，所以必须选后者，并且**留痕**
//     （logger.SysLog，否则上限长期失效无人知）。
//     与迁移侧一致：registry.ensureAccountDeviceBindingsSchema 失败时 db.go 也只 SysLog。
//
//  3. **Q16 遗留：「禁止该等级用任何设备」无法用 0 表达**。
//     0 已被「不限」占用，因此本文件用**负数**表达「禁止」：
//     device_limit < 0 ⇒ 一律拒绝（DevicePoolForbiddenLimit）。
//     ⚠️ 现状约束（必须知道）：user_level.go 的 validateUserLevelInput 会把负值
//     **夹紧成 0**（`if input.DeviceLimit < 0 { input.DeviceLimit = 0 }`），
//     所以通过管理接口永远写不出 -1，只有直接改库才能表达「禁止」。
//     本文件照此实现（判定侧已就绪）。
//
//  4. **level_id = 0 时 EffectiveDeviceLimit 必须返回 (0, "level_missing", nil)**
//     —— 返回错误会让「未评估用户」立刻无法登录（137 红线 1）。
//
// ## 无历史回填
//
// 上线前的历史请求没有设备指纹，因此「已用设备数」从零起算。这意味着
// **上线初期每个用户的实际可用设备数 = 等级上限**，而不是「上限减去历史」。
//
// ## 与 user_level.go 的契约
//
// 只读它的 DeviceLimit 字段（经 users.level_id → user_levels.device_limit），
// 不改它的任何函数、不改 user_levels 表结构、不复制它的判定逻辑：
//   - 「用户当前是哪一级」沿用 CurrentUserLevelIDWithDB（单一实现，不重算）；
//   - 等级表就绪判定沿用 userLevelSchemaAvailable；
//   - 会话清理沿用 userLevelSession。
//
// 换句话说：等级切换（降级/停用/删除）**立即**改变设备上限，因为本文件每次都现读
// level_id 对应的 device_limit，不做任何缓存。缓存会让「管理员改了上限」不生效，
// 而这类 bug 的表现是「配置看起来改了但行为没变」，排查成本极高。

// 设备上限的两种特殊取值。
const (
	// DevicePoolUnlimited 是 device_limit = 0 的语义：**不限**。
	DevicePoolUnlimited int32 = 0
	// DevicePoolForbiddenLimit 是「禁止该等级使用任何设备」的取值（Q16 遗留的出口）。
	// 判定侧把它实现为「任何 device_limit < 0 都拒绝」，因此 -1 / -2 等价。
	DevicePoolForbiddenLimit int32 = -1
)

// DeviceLimitOverrideNotSet 是 users.device_limit_override 的「不覆盖」标记（137 §3.1）。
//
// 为什么用 -1 而不是 NULL（137 §3.1 原文）：NULL 在 GORM 里要指针类型，且
// `WHERE device_limit_override >= 0` 与 `IS NOT NULL` 两种判据容易写混。
// -1 是单值的、可比较的、跨库一致的。
const DeviceLimitOverrideNotSet int32 = -1

// EffectiveDeviceLimit 返回值的 source 取值（137 §4）。
//
// ⚠️ 用例里故意写**字面量**而不是引用这些常量：常量改了名字或值，
// 若测试也引用常量，期望值会跟着一起漂移，等于这条断言恒真
// （本仓已因「期望值与被测实现共享常量」交付过假钉子）。
const (
	// DeviceLimitSourceUserOverride 用户自设覆盖（users.device_limit_override >= 0）。
	DeviceLimitSourceUserOverride = "user_override"
	// DeviceLimitSourceLevel 等级上的 device_limit（非 0）。
	DeviceLimitSourceLevel = "level"
	// DeviceLimitSourceLevelMissing 没有可用等级（level_id=0 / 等级被删 / 表未就绪）。
	DeviceLimitSourceLevelMissing = "level_missing"
	// DeviceLimitSourceUnlimited 等级上的 device_limit = 0（不限）。
	DeviceLimitSourceUnlimited = "unlimited"
)

// DevicePoolUsedUnknown 表示「本次判定没有统计已用设备数」。
//
// 为什么不是 0：0 是「已登记 0 台」这个真实状态。把「没统计」也写成 0，
// 日志里就分不清「不限档没统计」和「真的还没有任何设备」。
const DevicePoolUsedUnknown = -1

// 判定原因（稳定字符串，可直接落日志/上报，也便于用例断言）。
const (
	// DevicePoolReasonUnlimited：device_limit = 0，放行。
	DevicePoolReasonUnlimited = "unlimited"
	// DevicePoolReasonNoLimitConfigured：用户没有等级 / 等级未设上限，放行。
	DevicePoolReasonNoLimitConfigured = "no-limit-configured"
	// DevicePoolReasonFailOpen：表缺失 / 查询出错 / 参数不全，按 fail-open 放行。
	DevicePoolReasonFailOpen = "fail-open"
	// DevicePoolReasonForbidden：device_limit < 0，拒绝任何设备。
	DevicePoolReasonForbidden = "forbidden"
	// DevicePoolReasonAlreadyRegistered：该设备已绑定且未撤销，不占新槽，放行。
	DevicePoolReasonAlreadyRegistered = "already-registered"
	// DevicePoolReasonWithinLimit：新设备且未达上限，放行。
	DevicePoolReasonWithinLimit = "within-limit"
	// DevicePoolReasonLimitExceeded：新设备且已达上限，拒绝。
	DevicePoolReasonLimitExceeded = "limit-exceeded"
	// DevicePoolReasonSwitchOff：总开关关闭（仅 WithSwitch 入口），放行。
	DevicePoolReasonSwitchOff = "switch-off"
	// DevicePoolReasonDeviceBlacklisted：该 device_id 在全局黑名单（kind=device）。
	// 用户**不可**自行解除（137 §5 第 1 步）。
	DevicePoolReasonDeviceBlacklisted = "device-blacklisted"
	// DevicePoolReasonUserBlocked：该用户自己拉黑了这台设备（user_device_blocks）。
	// 用户**可**自行解除（137 §5 第 2 步）。
	DevicePoolReasonUserBlocked = "user-blocked"
)

// DevicePoolDeviceIDMaxLength 是设备指纹的截断长度（与 entity 列的 varchar(191) 对齐）。
const DevicePoolDeviceIDMaxLength = 191

// DevicePoolBlacklistKindDevice 是黑名单的 device 粒度（137 §3.4）。
//
// 直接写这个字面量而不是引用 service.BlacklistKind* 常量：该常量组属于
//
//	A3（黑名单设备粒度）的落地范围，本批次不新增它，但判定必须能查到已有数据。
//	等 A3 落了常量后这里应改成引用它（同一份语义，两处字面量会漂移）。
const DevicePoolBlacklistKindDevice = "device"

// ErrDevicePoolSchemaUnavailable 表示绑定表还没迁移好。
//
// 只在 CountDevices 这类「显式查询」入口返回；CanUseDevice / RegisterDevice
// 遇到它一律按 fail-open 放行（见文件头第 2 条）。
var ErrDevicePoolSchemaUnavailable = errors.New("设备池表尚未就绪")

// DevicePoolDecision 是一次设备池判定的结论。
//
// 结构体而不是裸 bool：判定必须能解释。运营投诉「为什么说满了」时，
// 日志里要能看到「上限 2 / 已用 2 / 这台是新设备」这三项，
// 否则只能靠猜（本仓黑名单命中留痕是同一动机）。
type DevicePoolDecision struct {
	// Allowed 是最终结论。
	Allowed bool `json:"allowed"`
	// Reason 是稳定原因串（见上面的常量）。
	Reason string `json:"reason"`
	// Limit 是生效的上限：0=不限，<0=禁止，>0=具体台数。
	Limit int32 `json:"limit"`
	// Used 是已绑定（未撤销）设备数；DevicePoolUsedUnknown(-1) 表示本次没有统计。
	Used int `json:"used"`
	// Enforced 表示本次**真的做了上限比较**。
	//
	// false 的四种情况：不限档（0）、没配上限（无等级）、fail-open、黑名单直拒。
	// 这几种都放行/直拒，但语义完全不同，混在一个 bool 里会让排障失去方向。
	Enforced bool `json:"enforced"`
	// LimitSource 是上限的来源（user_override / level / level_missing / unlimited）。
	LimitSource string `json:"limitSource,omitempty"`
	// Message 是**给用户看的拒绝文案**（仅在 Allowed=false 时有意义）。
	//
	// 137 §5：「第 6 步的文案是产品需求，不是细节」—— 用户被拒后必须知道
	// **去哪自救**。没有这句话，用户只会反复重试。
	// 文案在这里生成而不是在 HTTP 层拼，是为了让「改文案」只有一个改动点。
	Message string `json:"message,omitempty"`
	// BlacklistEntryID 命中全局黑名单时给出规则 id（排障/申诉用），0 = 未命中。
	BlacklistEntryID int64 `json:"blacklistEntryId,omitempty"`
}

// NormalizeDevicePoolDeviceID 归一化设备指纹：去首尾空白、截断到 191。
//
// 不转小写、不哈希：
//   - 不转小写 —— 指纹本身大小写敏感（同 user_binding_records.value 的取舍）；
//   - 不哈希 —— 它已是不可读随机串，没有脱敏需求，而保留原值让运营能直接
//     比对「用户说的那台设备」。
//
// 返回空串表示「这个值不能标识设备」，调用方按 fail-open 处理。
func NormalizeDevicePoolDeviceID(deviceID string) string {
	trimmed := strings.TrimSpace(deviceID)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > DevicePoolDeviceIDMaxLength {
		return string(runes[:DevicePoolDeviceIDMaxLength])
	}
	return trimmed
}

// NormalizeDeviceClientIP 归一化来源 IP 为可落库的文本，非法/空返回空串。
//
// ## 为什么用 net.ParseIP 而不是只 TrimSpace
//
// 同一个客户端可能以 `::ffff:203.0.113.7`、`203.0.113.7`、`  203.0.113.7 ` 三种
// 写法出现（IPv4-mapped IPv6 是双栈监听下的常态）。IP.String() 把它们收敛成
// 同一个字面量，这样「同一台设备换了写法」不会被显示成两个不同的地址。
// 与 blacklist_cache.go:147 的取舍一致（那里明确禁止字符串前缀比较）。
//
// ## 为什么非法值返回空串而不是原样返回
//
// 本列是**展示用**。把 `unknown`、`<nil>`、`192.168.1.1:8080`、代理塞进来的
// 乱码原样落库，会让用户以为自己的设备从某个奇怪的地方连进来 —— 那是误报，
// 比「未记录」更糟。所以宁可不显示。
//
// ## 为什么保留内网/回环地址
//
// 「我的设备」是给**设备主人**看的，不是对外泄露面：用户需要看到
// 「这台设备从 192.168.1.7 连的」来判断是不是自己家里的机器。
// 与 location.go 的 IsPrivateOrReservedIP 用途相反 —— 那个是「省一次付费查询」，
// 这个是「让用户认得出自己的设备」，不能共用同一条过滤。
func NormalizeDeviceClientIP(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// devicePoolSession 清掉继承的模型（同 userLevelSession 的成因）。
//
// ⚠️ 不是洁癖：业务代码会把 `dao.User.UnderlyingDB()` 传进来，那个句柄的
// Statement 已解析成 users 模型；拿它 Create(&entity.AccountDeviceBinding{})
// 会 panic: reflect: Field index out of range（本仓实测踩过）。
func devicePoolSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// devicePoolSchemaAvailable 判断绑定表是否可用。
//
// 除了 HasTable，还核 device_id 列：半建成的表（表在、列缺）在查询时会报
// `no such column`，那属于基础设施故障，应当走 fail-open 而不是把错误抛给登录链路。
//
// ⚠️ revoked_at 列**不在**这里的判据里：它是本批次新增的列，老库在迁移跑完前
// 确实没有它。查询侧用 devicePoolRevokedColumnAvailable 单独判断，
// 这样「表在但列还没补」时不会整条链路 fail-open（那会让刚上的限制直接失效）。
func devicePoolSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		return false
	}
	if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "user_id") {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_id")
}

// devicePoolRevokedColumnAvailable 判断 revoked_at 列是否已就绪（本批次新增列）。
//
// 未就绪时的语义：**所有行都视为未撤销**（等价于「没有撤销功能」），
// 这与迁移前的旧行为一致，不会因为部署顺序（先上代码后跑迁移）而误伤。
func devicePoolRevokedColumnAvailable(db *gorm.DB) bool {
	if db == nil || !devicePoolSchemaAvailable(db) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "revoked_at")
}

// ActiveOrdinaryDeviceBindings is the same selection used for slot usage
// and its device list. Existing account rules predate atomic revocation; they
// already block use and must not consume a slot merely because of that old row.
func ActiveOrdinaryDeviceBindings(db, query *gorm.DB) *gorm.DB {
	if scope, args := devicePoolActiveScope(db); scope != "" {
		return query.Where(scope, args...)
	}
	return query
}

func devicePoolActiveScope(db *gorm.DB) (string, []any) {
	var predicates []string
	if devicePoolRevokedColumnAvailable(db) {
		predicates = append(predicates, "revoked_at IS NULL")
	}
	if db.Migrator().HasTable(&entity.UserDeviceBlock{}) {
		exact := exactDeviceIDSQL(db, "b.device_id", "account_device_bindings.device_id")
		predicates = append(predicates,
			"NOT EXISTS (SELECT 1 FROM user_device_blocks AS b WHERE b.user_id = account_device_bindings.user_id AND "+exact+")")
	}
	return strings.Join(predicates, " AND "), nil
}

// CountDevices 返回某账号**当前占用槽位**的设备数（141 §2 的核心公式）。
//
// 口径（141 §2，唯一的表达方式，不要再想别的写法）：
//
//	used = Σ over 每个已绑定且未撤销的 device_id:  max(1, session_count)
//
// 为什么是 max(1, ·) 而不是 `if is_exe` 特判（141 §2 原文）：
//
//	普通浏览器 session_count 恒 0 → max(1,0)=1 → R4「绑定即占 1」
//	exe 启动未开会话      恒 0 → max(1,0)=1 → R5「exe 自己占 1」
//	exe 全关             恒 0 → max(1,0)=1 → R7「回落到 1」
//	exe 开 3 个会话        3 → 3             → R6
//
// 四种场景**共用一条规则**，所以没有分支就意味没有「某个分支忘写」的漏面。
// 特别注意 R4 与 R7 是**同一个 0 值**的两种业务解释，实现上必须同解 ——
// 一旦有人给其中一种加特判，另一种就会静默错。
//
// ## 为什么在 Go 侧逐行累加，而不是 SUM(MAX(...))
//
// SQLite 的 `MAX(a,b)` 在有两个参数时是**标量函数**，而 MySQL 的 `MAX()` 在没有
// GROUP BY 时是**聚合函数**，`MAX(1, session_count)` 在 MySQL 上是聚合函数调用
// 语法错误。写成 `CASE WHEN session_count < 1 THEN 1 ELSE session_count END`
// 两库都能过，但那只是把口径分散到 SQL 字符串里，将来改口径要改两地方言分支。
// 逐行取回 + Go 侧累加让「一个式子」只有一处实现（devicePoolSlotWeight）。
//
// 代价是一次返回 N 行而不是一个标量。设备行数量级是「个位数~几十」，可接受。
//
// ## 撤销的行不占槽位
//
// 必须保留 `revoked_at IS NULL`（devicePoolActiveScope）。若改成统计全部行，
// 用户移除设备后槽位永远不释放（137 §3.2 的「保留行不删」正是为了让撤销
// 可审计，而不是让撤销失效）。想看含撤销的历史行数用 CountAllDevices。
//
// ## session_count 列还没就绪时（部署顺序「代码先上、迁移后跑」）
//
// 退化为「每行算 1」——那恰好等于 max(1, 0)，与迁移跑完后的口径**完全一致**
// （该列默认值就是 0）。因此不存在「部署窗口期内口径不同」的阶段。
//
// 表未就绪时返回 ErrDevicePoolSchemaUnavailable（调用方 CanUseDevice 会把它
// 当成 fail-open；显式查询的调用方则应把它视为「暂时算不出来」）。
func CountDevices(db *gorm.DB, userID int32) (int, error) {
	if db == nil || userID <= 0 {
		return 0, nil
	}
	if !devicePoolSchemaAvailable(db) {
		return 0, ErrDevicePoolSchemaUnavailable
	}
	query := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).Where("user_id = ?", userID)
	query = ActiveOrdinaryDeviceBindings(db, query)
	selectClause := "id AS id"
	if devicePoolSessionCountColumnAvailable(db) {
		selectClause = "id AS id, session_count AS session_count"
	}
	var rows []devicePoolSessionCountRow
	if err := query.Select(selectClause).Find(&rows).Error; err != nil {
		return 0, err
	}
	total := 0
	for _, row := range rows {
		total += devicePoolSlotWeight(row.SessionCount)
	}
	return total, nil
}

// devicePoolSessionCountRow 是计数口径的一次取行（只需要 id 与 session_count）。
type devicePoolSessionCountRow struct {
	ID int64 `gorm:"column:id"`
	// SessionCount 用指针：老库/手建表可能允许 NULL，而 NULL 在此处的语义
	// 与 0 相同（都是「没有活跃会话」）。直接扫进 int32 会在 NULL 上报错 ——
	// 那样计数失败会让 CanUseDevice fail-open，等于设备上限整体静默失效。
	SessionCount *int32 `gorm:"column:session_count"`
}

// DevicePoolMinSlotWeight 是每台已绑定设备贡献的最小槽位数（141 §2 的 `1`）。
//
// 导出是因为前端/文档要解释「为什么我只开了一个浏览器也显示占用 1」；
// 但**取值本身不可配置**：它是 R4/R5/R7 的共同解，做成配置项就等于允许
// 把「普通浏览器占 1」调成 0，那会让设备上限对普通浏览器彻底失效。
const DevicePoolMinSlotWeight = 1

// devicePoolSlotWeight 是核心公式的**单行实现**：max(1, sessionCount)。
//
// 全仓「一台设备占几个槽位」只有这一处判断（CountDevices 调它）。
// 负数按 1 处理：脏数据（手工改库/未来字段语义变化）不该让某台设备贡献 0 或
// 负槽位 —— 那会让 used 变小，表现为「槽位凭空多出来」。
func devicePoolSlotWeight(sessionCount *int32) int {
	if sessionCount == nil {
		return DevicePoolMinSlotWeight
	}
	if *sessionCount < DevicePoolMinSlotWeight {
		return DevicePoolMinSlotWeight
	}
	return int(*sessionCount)
}

// devicePoolSessionCountColumnAvailable 判断 session_count 列是否已就绪（141 §3.1 新增列）。
//
// 未就绪时的语义见 CountDevices 的说明：等价于每行 max(1, 0)。
// 与 devicePoolRevokedColumnAvailable 同一纪律 —— 逐列判，不因为缺一列就整体 fail-open。
func devicePoolSessionCountColumnAvailable(db *gorm.DB) bool {
	if db == nil || !devicePoolSchemaAvailable(db) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_count")
}

// CountAllDevices 返回某账号的设备行总数（**含已撤销**，用于「我的设备」列表）。
//
// 与 CountDevices 的分工必须写清：前者是配额口径，后者是历史展示口径。
// 用错会让「移除设备后上限立刻多一台可用」这个行为变得不可解释。
//
// ⚠️ 本函数**不套用 max(1, session_count)**：它是「行数」而不是「槽位数」。
// 一台 exe 开 3 个会话仍然只有 1 行 —— 这正是「会话是临时的、宿主是常驻的」
// 在数据上的表达（141 §5.2）。
func CountAllDevices(db *gorm.DB, userID int32) (int, error) {
	if db == nil || userID <= 0 {
		return 0, nil
	}
	if !devicePoolSchemaAvailable(db) {
		return 0, ErrDevicePoolSchemaUnavailable
	}
	var count int64
	if err := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// activeDeviceBindingID 判断该账号是否已绑定这台设备且**未被撤销**。
//
// 这是「不占新槽」的判据来源：已绑定且未撤销的设备重复登录必须放行，
// 否则上限 2 的用户用同一台设备登录第 3 次就会被自己拒掉。
//
// 返回 id>0 表示命中（调用方据此刷新 last_seen_at）。
// **必须带 revoked_at IS NULL**：撤销后的行如果也命中，用户移除设备后
// 静默重绑就变成了「直接放行且不重新写回绑定」——槽位与展示都会错乱。
func activeDeviceBindingID(db *gorm.DB, userID int32, normalizedDeviceID string) (int64, bool, error) {
	var row struct {
		ID int64 `gorm:"column:id"`
	}
	query := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Select("id AS id").
		Where("user_id = ? AND device_id = ?", userID, normalizedDeviceID)
	if scope, args := devicePoolActiveScope(db); scope != "" {
		query = query.Where(scope, args...)
	}
	if err := query.Order("id asc").Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return row.ID, true, nil
}

// devicePoolUserBlocked 判断该用户是否自己拉黑了这台设备（user_device_blocks，137 §5 第 2 步）。
//
// 表不存在时返回 false + nil（不是错误）：本表由 A2 的迁移建出，A1 阶段
// 可能还没有它。把它当错误会让整个判定 fail-open，连上限也一起失效 ——
// 而「拉黑表缺失」的正确语义只是「还没有人拉黑过任何设备」。
func devicePoolUserBlocked(db *gorm.DB, userID int32, normalizedDeviceID string) bool {
	if db == nil || userID <= 0 {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUserDeviceBlock) {
		return false
	}
	var count int64
	if err := devicePoolSession(db).Model(&entity.UserDeviceBlock{}).
		Where("user_id = ? AND device_id = ?", userID, normalizedDeviceID).
		Count(&count).Error; err != nil {
		// 查询失败按「没拉黑」处理：拉黑是用户自管功能，读不到时的
		// 最坏后果是「这次没拦住自己的设备」，远小于「全站 fail-open 或不让人登录」。
		devicePoolFailOpenLog(userID, "查询用户设备拉黑失败："+err.Error())
		return false
	}
	return count > 0
}

// devicePoolGlobalDeviceBlacklistEntry 查全局黑名单里的 device 粒度条目（137 §5 第 1 步）。
//
// 返回 (entryID, hit)。表不存在 ⇒ (0,false)（A3 之前黑名单表已存在，这只防极简测试库）。
//
// ⚠️ 这里直接读库而不是走 BlacklistSnapshot：A3（黑名单设备粒度 + 缓存 MatchDevice）
// 还没落地，而 A1 的判定顺序**必须**先把黑名单放在最前面（137 §5.1）。
// A3 落地后应把本函数替换为快照查询（同样签名），届时只需改这一处。
//
// 过期语义与缓存一致：ExpiresAt 非空且已过 ⇒ 不命中。
func devicePoolGlobalDeviceBlacklistEntry(db *gorm.DB, normalizedDeviceID string, now time.Time) (int64, bool) {
	if db == nil || normalizedDeviceID == "" {
		return 0, false
	}
	if !db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		return 0, false
	}
	var row struct {
		ID        int64      `gorm:"column:id"`
		ExpiresAt *time.Time `gorm:"column:expires_at"`
	}
	// value_hash 与运营侧写入用的是同一个哈希（BlacklistPatternHashHex），
	// 否则「建了规则但永不命中」且不报错。
	err := devicePoolSession(db).Model(&entity.BlacklistEntry{}).
		Select("id AS id, expires_at AS expires_at").
		Where("kind = ? AND value_hash = ? AND enabled = ?",
			DevicePoolBlacklistKindDevice, BlacklistPatternHashHex(normalizedDeviceID), true).
		Order("id asc").Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false
		}
		// 查不到（表结构异常等）按未命中处理：黑名单另有中间件层把关，
		// 这里只是「已绑定设备也可能被封禁」的第二道保险。
		logger.SysLog("设备黑名单查询失败（按未命中继续）：" + logger.RedactSecrets(err.Error()))
		return 0, false
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(now) {
		return 0, false
	}
	return row.ID, true
}

// EffectiveDeviceLimit 是设备上限的**唯一权威计算**（137 §4）。
//
// 所有判定点（准入、页面展示、用户自设校验）都必须调它 ——
// 任何一处自己写 `if user.XXX else level.XXX` 都会与别处漂移，
// 表现为「页面显示 1 台、实际拦 3 台」这种不可能排查的 bug。
//
// 取值顺序：
//  1. users.device_limit_override >= 0 ⇒ 用它（source=user_override）；
//  2. 否则用当前等级的 user_levels.device_limit：
//     >0 ⇒ (N, "level")；==0 ⇒ (0, "unlimited")；<0 ⇒ (N, "level")（禁止档）；
//  3. 等级缺失（level_id=0 / 等级被删 / 等级表未就绪）⇒ **(0, "level_missing", nil)**。
//
// ⚠️ 第 3 条必须返回 **(0, nil)** 而不是错误（137 红线 1）：
// 新注册用户在被评估之前 level_id 就是 0，返回错误会让未评估用户
// **立刻无法登录**（注册刚成功、第一次登录就被挡），而且是全量性的。
func EffectiveDeviceLimit(db *gorm.DB, userID int32) (limit int32, source string, err error) {
	if db == nil || userID <= 0 {
		return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, nil
	}

	// ── 1. 用户级覆盖 ──
	// 列不存在（老库、迁移未跑）时按「不覆盖」处理，绝不能因此报错。
	if db.Migrator().HasTable(entity.TableNameUser) &&
		db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		var row struct {
			Override int32 `gorm:"column:device_limit_override"`
		}
		queryErr := devicePoolSession(db).Table(entity.TableNameUser).
			Select("device_limit_override AS device_limit_override").
			Where("id = ?", userID).Take(&row).Error
		switch {
		case queryErr == nil:
			if row.Override >= 0 {
				return row.Override, DeviceLimitSourceUserOverride, nil
			}
		case errors.Is(queryErr, gorm.ErrRecordNotFound):
			// 用户不存在：等价于「没有等级」，按不限处理（fail-open）。
			return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, nil
		default:
			return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, queryErr
		}
	}

	// ── 2/3. 等级值 ──
	if !userLevelSchemaAvailable(db) {
		return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, nil
	}
	levelID, levelErr := CurrentUserLevelIDWithDB(db, userID)
	if levelErr != nil {
		return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, levelErr
	}
	if levelID <= 0 {
		// 0 = 尚未评估 / 等级被删后回落。无等级的用户不设上限：
		// 强行给一个默认上限会让「等级表为空」的库把所有用户锁死。
		return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, nil
	}
	var levelRow struct {
		DeviceLimit int32 `gorm:"column:device_limit"`
	}
	if err := devicePoolSession(db).Table(entity.TableNameUserLevel).
		Select("device_limit AS device_limit").
		Where("id = ?", levelID).Take(&levelRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// level_id 指向一个已删除的等级：脏数据，按无上限处理（fail-open 的一种）。
			return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, nil
		}
		return DevicePoolUnlimited, DeviceLimitSourceLevelMissing, err
	}
	if levelRow.DeviceLimit == DevicePoolUnlimited {
		return DevicePoolUnlimited, DeviceLimitSourceUnlimited, nil
	}
	return levelRow.DeviceLimit, DeviceLimitSourceLevel, nil
}

// BuildDevicePoolLimitExceededMessage 生成「设备数已满」的用户文案（137 §5 第 6 步）。
//
// **文案是产品需求，不是细节**：用户被拒后必须知道去哪自救。
// 因此它放在 service 层作为唯一来源，HTTP 层直接取用而不是自己拼。
func BuildDevicePoolLimitExceededMessage(limit int32, used int) string {
	return "设备数已满（" + strconv.Itoa(used) + "/" + strconv.Itoa(int(limit)) +
		"），请到「我的设备」移除不用的设备"
}

// CanUseDevice 判定「该账号还能不能用这台设备」。
//
// 判定顺序（137 §5，**顺序本身就是需求**）：
//
//  0. 参数不全（db nil / userID<=0 / 设备指纹为空） → fail-open 放行
//  1. 绑定表不可用                                  → fail-open 放行 + SysLog 留痕
//  2. **全局黑名单 kind=device**                     → 拒绝（用户不可解除）
//  3. **用户拉黑 user_device_blocks**               → 拒绝（用户可自行解除）
//  4. 有效上限（EffectiveDeviceLimit）：
//     level_missing                              → 放行（no-limit-configured）
//     == 0（不限）                                → 放行 + 调用方登记（unlimited）
//     < 0（禁止档）                               → 拒绝（forbidden）
//  5. 已绑定且 revoked_at IS NULL                   → 刷新 last_seen_at，放行
//  6. 已用台数 < 上限                               → 放行（within-limit）
//  7. 已满                                          → 拒绝 + 文案（limit-exceeded）
//
// ## 为什么第 2 步必须在第 5 步之前（137 §5.1，判据 D5）
//
// 如果把「已绑定 → 放行」放前面，那么**封禁一台已经在用的设备**后，
// 该设备的旧绑定行仍然命中第 5 步 → 放行 → 封禁失效。
// 用户拍板了「封禁设备 → 立即撤销所有旧绑定」，但撤销是异步的、可能失败的，
// 而黑名单判定是同步的、必然生效的 —— 两道保险，顺序不能反。
func CanUseDevice(db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
	return CanUseDeviceWithClientIP(db, userID, deviceID, "")
}

// CanUseDeviceWithClientIP 在设备通过准入时同步刷新最近来源 IP。
// 已有设备的请求热路径只需一次 UPDATE；未采到合法 IP 时保留原地址。
func CanUseDeviceWithClientIP(db *gorm.DB, userID int32, deviceID string, clientIP string) DevicePoolDecision {
	return inspectDeviceAccess(db, userID, deviceID, clientIP, true)
}

// InspectDeviceAccessWithDB evaluates the same admission rules without updating
// activity or registering a device. Configuration tests must not consume a slot.
func InspectDeviceAccessWithDB(db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
	return inspectDeviceAccess(db, userID, deviceID, "", false)
}

// CheckDeviceBlockWithDB checks explicit bans independently of capacity settings.
// Unlimited accounts and a disabled capacity switch must not bypass a device ban.
func CheckDeviceBlockWithDB(db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if db == nil || userID <= 0 || normalized == "" {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	if entryID, hit := devicePoolGlobalDeviceBlacklistEntry(db, normalized, time.Now()); hit {
		return DevicePoolDecision{Allowed: false, Reason: DevicePoolReasonDeviceBlacklisted,
			Used: DevicePoolUsedUnknown, Message: "该设备已被限制使用", BlacklistEntryID: entryID}
	}
	if devicePoolUserBlocked(db, userID, normalized) {
		return DevicePoolDecision{Allowed: false, Reason: DevicePoolReasonUserBlocked,
			Used: DevicePoolUsedUnknown, Message: "你已拉黑该设备"}
	}
	return DevicePoolDecision{Allowed: true, Used: DevicePoolUsedUnknown}
}

func inspectDeviceAccess(db *gorm.DB, userID int32, deviceID string, clientIP string, recordActivity bool) DevicePoolDecision {
	if db == nil || userID <= 0 {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" {
		// 没有设备指纹就无法判定「是不是新设备」。拦掉等于对没有指纹的
		// 旧客户端一次性全封，那是升级事故而不是权益限制。
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	if !devicePoolSchemaAvailable(db) {
		devicePoolFailOpenLog(userID, "account_device_bindings 未就绪")
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}

	now := time.Now()

	// 显式拉黑优先于额度与已绑定豁免。
	if blocked := CheckDeviceBlockWithDB(db, userID, normalized); !blocked.Allowed {
		return blocked
	}

	// ── ③ 有效上限 ──
	//
	// 等级体系整体不可读（表没建 / users.level_id 列缺）与「用户还没评过级」
	// 是两种不同的事实，必须分开：前者是基础设施故障（fail-open + 留痕），
	// 后者是正常状态（no-limit-configured，不记日志，否则日志会被新注册用户刷满）。
	if !userLevelSchemaAvailable(db) {
		devicePoolFailOpenLog(userID, "等级体系未就绪（user_levels 或 users.level_id 缺失）")
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	limit, source, err := EffectiveDeviceLimit(db, userID)
	if err != nil {
		devicePoolFailOpenLog(userID, "读取设备上限失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	if source == DeviceLimitSourceLevelMissing {
		// 没有等级 ⇒ 不设上限。与 ResolveUserLevel 的 rank=0「无等级」一致。
		return DevicePoolDecision{
			Allowed:     true,
			Reason:      DevicePoolReasonNoLimitConfigured,
			Used:        DevicePoolUsedUnknown,
			LimitSource: source,
		}
	}
	if limit == DevicePoolUnlimited {
		// 不可协商点 ①：0 = 不限。这里**先于**任何计数返回，既省一次查询，
		// 也让「随便第 100 台都放行」在实现上是结构性的，而不是靠比较符号凑出来。
		return DevicePoolDecision{
			Allowed:     true,
			Reason:      DevicePoolReasonUnlimited,
			Limit:       DevicePoolUnlimited,
			Used:        DevicePoolUsedUnknown,
			LimitSource: source,
		}
	}

	used, err := CountDevices(db, userID)
	if err != nil {
		devicePoolFailOpenLog(userID, "统计设备数失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Limit: limit, Used: DevicePoolUsedUnknown, LimitSource: source}
	}

	if limit < 0 {
		// Q16 遗留出口：0 已被「不限」占用，负数才是「禁止任何设备」。
		return DevicePoolDecision{
			Allowed:     false,
			Reason:      DevicePoolReasonForbidden,
			Limit:       limit,
			Used:        used,
			Enforced:    true,
			LimitSource: source,
			Message:     "当前账号等级不允许在任何设备上使用，请联系管理员",
		}
	}

	// ── ④ 已绑定且未撤销：放行并刷新最近使用时间和 IP ──
	if id, bound, err := activeDeviceBindingID(db, userID, normalized); err != nil {
		devicePoolFailOpenLog(userID, "查询设备绑定失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Limit: limit, Used: DevicePoolUsedUnknown, LimitSource: source}
	} else if bound {
		// 刷新 last_seen_at：它是「10 天闲置解绑」的唯一数据源，
		// 只靠登录路径的 RegisterDevice 刷新会让「同一设备高频请求」的活跃度失真。
		// 用 UpdateColumns 而不是 Updates：后者会顺带刷新 updated_at，
		// 把「行被改过」与「设备被使用过」两种事实混在一起。
		if recordActivity {
			activity := map[string]any{"last_seen_at": now}
			if ip := NormalizeDeviceClientIP(clientIP); ip != "" &&
				db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "last_ip") {
				activity["last_ip"] = ip
			}
			update := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
				Where("id = ? AND (last_seen_at IS NULL OR last_seen_at <= ?)", id, now)
			if devicePoolRevokedColumnAvailable(db) {
				update = update.Where("revoked_at IS NULL")
			}
			if err := update.UpdateColumns(activity).Error; err != nil {
				devicePoolFailOpenLog(userID, "刷新设备活跃时间失败："+err.Error())
			}
		}
		return DevicePoolDecision{
			Allowed:     true,
			Reason:      DevicePoolReasonAlreadyRegistered,
			Limit:       limit,
			Used:        used,
			Enforced:    true,
			LimitSource: source,
		}
	}

	// ── ⑤/⑥ 槽位 ──
	if used >= int(limit) {
		return DevicePoolDecision{
			Allowed:     false,
			Reason:      DevicePoolReasonLimitExceeded,
			Limit:       limit,
			Used:        used,
			Enforced:    true,
			LimitSource: source,
			Message:     BuildDevicePoolLimitExceededMessage(limit, used),
		}
	}
	return DevicePoolDecision{
		Allowed:     true,
		Reason:      DevicePoolReasonWithinLimit,
		Limit:       limit,
		Used:        used,
		Enforced:    true,
		LimitSource: source,
	}
}

// CanUseDeviceWithSwitch 保留接线层的历史入口，现与 CanUseDevice 使用同一判定。
//
// system_settings.device_limit_enabled 是兼容旧客户端的历史字段，服务端始终按启用处理。
// 等级 device_limit=0 表示该等级不限设备数；其它正数按设备池上限判定。
//
// 不再读取旧开关，避免旧客户端留下的 false 绕过当前等级上限。
func CanUseDeviceWithSwitch(db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
	return CanUseDevice(db, userID, deviceID)
}

// DeviceBindingRegisterOptions 是登记设备的可选元信息（137 §3.2 的 owner_kind / token_id / device_label）。
//
// 零值即最保守的语义：owner_kind=login、token_id=0、label 空 ——
// 与既有调用方（登录路径）的行为完全一致，不需要改任何调用点。
type DeviceBindingRegisterOptions struct {
	OwnerKind   string
	TokenID     int32
	DeviceLabel string
	// ClientInstance 是本次运行实例的标识（141 §4 的 client_instance）。
	//
	// 空串 = 调用方没带（旧客户端 / 扩展还没接 X-Device-Instance）：
	// 此时**不做冲突判定**（没有可比对的东西），行为与加这个字段之前完全一致。
	//
	// 它只在**首次登记**那一刻参与冲突判定，之后同一设备的重连不再比对
	// —— 理由见 device_conflict.go 文件头（写反了会让用户每次重开浏览器都被判冲突）。
	ClientInstance string
	// ClientIP 是本次请求的来源 IP（已归一化或未归一化都可，内部会再归一，
	// 见 NormalizeDeviceClientIP）。空串 = 调用方没采/采不到，此时**不覆盖**
	// 库里已有的 last_ip，避免一次缺 IP 的请求把已知地址抹成空。
	ClientIP string
}

// RegisterDevice 登记「该账号用过这台设备」，并把判定结论一并返回。
//
// 返回 (decision, err)：
//   - decision.Allowed=false 时**没有任何写入**，err 为 nil（拒绝是正常业务结果，
//     不是错误 —— 用 error 传拒绝会让调用方把它记成 5xx）；
//   - err 只在「判定放行但写入失败」时非 nil，此时调用方**应当继续放行**
//     （写不进去只是这次没记上，不该把用户挡在门外；下次登录会再记一次）。
//
// ## 写入形态：**UPDATE 优先，行不存在才 INSERT**（137 §7.1，判据 D11）
//
// 唯一索引是 (user_id, device_id)，所以「撤销后重绑」**绝不能 Create**：
// 会撞唯一索引 → 整个请求 500。正确形态是复用旧行，并把
// **revoked_at 与 revoked_reason 两个一起清掉** —— 只清一个的话，
// 另一个仍在，判定逻辑照样拒（137 §7.1 的判据原文）。
//
// 先 UPDATE 再 INSERT 而不是反过来（insert-on-conflict + 无条件 update）：
// 后者在「行已存在」时也执行一次 CREATE，语义上是「先试图插一行不该插的行」，
// 一旦有人删掉 OnConflict 子句就变成偶发 500。UPDATE 优先的形态让
// 「重绑不 Create」这条约束是结构性的。
//
// 并发首次登记：两个请求都 UPDATE 到 0 行 → 都 INSERT → 唯一索引挡住一个，
// 被挡的那一方把错误吞掉并重试一次 UPDATE（此时行已存在）。
func RegisterDevice(db *gorm.DB, userID int32, deviceID string) (DevicePoolDecision, error) {
	return RegisterDeviceWithOptions(db, userID, deviceID, DeviceBindingRegisterOptions{})
}

// RegisterDeviceWithOptions 同 RegisterDevice，额外写入 owner_kind / token_id / device_label，
// 并在**首次登记**时做设备标识冲突判定（141 §4，判据 S5/S6）。
//
// ## 冲突判定为什么排在最前面（且在 CanUseDevice 之前）
//
// 顺序是需求，不是风格：
//  1. 判为冲突时**一行都不写** —— 冲突的设备不该占用槽位，否则客户端「重新生成后重试
//     一次」的那次请求会发现自己第一次请求已经把槽位吃掉了（141 §4.2 的重试上限是 1 次）。
//  2. 冲突比「槽位已满」更具体：它告诉客户端「换 ID」而不是「删设备」。若让 CanUseDevice
//     先按槽位拒绝，客户端会把 409 之外的原因当成用户额度问题，永远换不出新 ID。
//  3. CanUseDevice 在「已登记」分支会刷新 last_seen_at（一次写操作）。冲突判定要是排在它后面，
//     被拒的设备也被刷了一次活跃时间 —— 「被封/冲突的设备仍在活跃」会让 10 天闲置清理失效。
//
// 判定通过后**照原样**继续走 CanUseDevice，不改写它的任何结论：
// 加上判定之前的行为逐字保留（已有 30+ 条 device_pool 用例依赖这一点）。
func RegisterDeviceWithOptions(db *gorm.DB, userID int32, deviceID string, options DeviceBindingRegisterOptions) (DevicePoolDecision, error) {
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if db == nil || normalized == "" || userID <= 0 || !devicePoolSchemaAvailable(db) {
		return CanUseDevice(db, userID, normalized), nil
	}
	var decision DevicePoolDecision
	err := withDeviceRegistrationWrite(db, userID, normalized, false, func(tx *gorm.DB) error {
		var err error
		decision, err = registerDeviceWithOptions(tx, userID, normalized, options)
		if err == nil && !decision.Allowed {
			return errDeviceRegistrationDenied
		}
		return err
	})
	if errors.Is(err, errDeviceRegistrationDenied) {
		err = nil
	}
	return decision, err
}

func registerDeviceWithOptions(db *gorm.DB, userID int32, normalized string, options DeviceBindingRegisterOptions) (DevicePoolDecision, error) {
	// 设备标识冲突（141 §4）：只在「首次登记」判一次，不带 instance 时整体跳过。
	if conflict := DecideDeviceRegistration(db, userID, normalized, options.ClientInstance); !conflict.Allowed {
		return conflict, nil
	}
	decision := InspectDeviceAccessWithDB(db, userID, normalized)
	if !decision.Allowed {
		return decision, nil
	}
	return decision, persistDeviceBinding(db, userID, normalized, options)
}

func persistDeviceBinding(db *gorm.DB, userID int32, normalized string, options DeviceBindingRegisterOptions) error {
	now := time.Now()
	session := devicePoolSession(db)
	ownerKind := strings.TrimSpace(options.OwnerKind)
	if ownerKind == "" {
		ownerKind = entity.DeviceBindingOwnerKindLogin
	}
	deviceLabel := truncateDeviceLabel(options.DeviceLabel)

	// ① 先尝试复用已有行（含已撤销的行 —— 那正是「静默重绑」）。
	updates := map[string]any{
		"last_seen_at": now,
	}
	hasRevocation := devicePoolRevokedColumnAvailable(db)
	if hasRevocation {
		// 必须**同时**清两列（137 §7.1）：只清 revoked_at 时 revoked_reason
		// 仍是 idle_10d/user_removed，展示层与排障会读到矛盾的「未撤销但有撤销原因」。
		updates["revoked_at"] = nil
		updates["revoked_reason"] = ""
	}
	if hasDeviceHistoryColumn(db) {
		updates["history_hidden_at"] = nil
	}
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "owner_kind") {
		updates["owner_kind"] = ownerKind
	}
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "token_id") {
		updates["token_id"] = options.TokenID
	}
	if deviceLabel != "" && db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_label") {
		// label 为空时**不覆盖**：老客户端不报 label，不该把已有名字抹掉。
		updates["device_label"] = deviceLabel
	}
	// client_instance 由 C1 的迁移加列，本文件不加实体字段（C1 独占范围），因此现判 HasColumn。
	//
	// 只在非空时写：空串表示「这次没带 instance」，写进去会把已有的占用者抹成
	// 「不知道」，等于让镜像复制的那台设备重新获得抢占机会。
	//
	// 覆盖写（而不是「只在首次登记写」）是刻意的：库里存的是**当前**运行实例，
	// 跨账号占用判定（DecideDeviceRegistration 第 ② 步）据此反映现状而不是陈旧值。
	// 它不会引起误判 —— 本账号已有活跃行时判定在第 ① 步就放行了，根本不看这一列。
	if instance := NormalizeDeviceClientInstance(options.ClientInstance); instance != "" &&
		db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "client_instance") {
		updates["client_instance"] = instance
	}
	// last_ip：只在本次真的采到合法 IP 时才写。
	//
	// ⚠️ **不能**无条件写（含空串）：daemon 重启、请求来自 unix socket、
	// 反向代理没配 TrustedProxies 都会让 c.ClientIP() 拿到空/不可解析的值，
	// 无条件写等于「偶尔一次缺 IP 就把用户已经看习惯的地址抹掉」，用户会以为
	// 设备换人了。与 device_label 的「空不覆盖」同一纪律。
	if clientIP := NormalizeDeviceClientIP(options.ClientIP); clientIP != "" &&
		db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "last_ip") {
		updates["last_ip"] = clientIP
	}

	updateExisting := func() (bool, error) {
		query := func() *gorm.DB {
			return session.Model(&entity.AccountDeviceBinding{}).Where("user_id = ? AND device_id = ?", userID, normalized)
		}
		update := query()
		if hasRevocation {
			update = update.Where("revoked_at IS NULL")
		}
		res := update.Updates(updates)
		if res.Error != nil || res.RowsAffected > 0 || !hasRevocation {
			return res.RowsAffected > 0, res.Error
		}

		// Do not erase a reason on rebind until the recorded revocation
		// survives in the independent audit. The common active update above
		// does not pay this read; the row lock also orders the idle worker.
		var existing deviceHistoryBinding
		err := query().Select("id, user_id, device_id, revoked_at, revoked_reason").
			Clauses(clause.Locking{Strength: "UPDATE"}).Take(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if err := preserveDeviceRevocation(db, existing); err != nil {
			return false, err
		}
		rebindUpdates := updates
		if existing.RevokedAt != nil {
			// Revocation ended the previous process's claim on this binding.
			// Do not resurrect its browser count when a new host claims one slot.
			rebindUpdates = maps.Clone(updates)
			if devicePoolSessionCountColumnAvailable(db) {
				rebindUpdates["session_count"] = 0
			}
			if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_reported_at") {
				rebindUpdates["session_reported_at"] = nil
			}
		}
		return true, query().Where("id = ?", existing.ID).Updates(rebindUpdates).Error
	}
	updated, err := updateExisting()
	if err != nil || updated {
		return err
	}

	// ② 行不存在：首次登记。FirstSeenAt 只在这里写一次。
	//
	// ⚠️ 用 map 而不是 struct 落库：struct 会**无条件**把 owner_kind / token_id /
	// device_label 列写进 INSERT，而老库（迁移未跑）没有这些列，会直接报
	// `table ... has no column named owner_kind` —— 于是「部署顺序是代码先上」
	// 那一刻的每一次登录登记都失败（且因为是 fail-open，只有 SysLog 能看到）。
	// 逐列判 HasColumn 的写法让登记在任何部署顺序下都成立。
	createValues := map[string]any{
		"user_id":       userID,
		"device_id":     normalized,
		"first_seen_at": now,
		"last_seen_at":  now,
		"created_at":    now,
		"updated_at":    now,
	}
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "owner_kind") {
		createValues["owner_kind"] = ownerKind
	}
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "token_id") {
		createValues["token_id"] = options.TokenID
	}
	if deviceLabel != "" && db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_label") {
		createValues["device_label"] = deviceLabel
	}
	// 首次登记时把占用者的实例一并落库：跳过的话，这台设备的 client_instance 永远是 '',
	// 于是「跨账号镜像复制」在第二次复制时无从识别（历史行 '' 被 DecideDeviceRegistration
	// 当「未知」跳过）。这是 141 §4.3 那套检测赖以工作的第一个锚点。
	if instance := NormalizeDeviceClientInstance(options.ClientInstance); instance != "" &&
		db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "client_instance") {
		createValues["client_instance"] = instance
	}
	// 首次登记同样只在有合法 IP 时写；列缺失时不写（老库，展示层会显示「未记录」）。
	if clientIP := NormalizeDeviceClientIP(options.ClientIP); clientIP != "" {
		for _, column := range []string{"first_ip", "last_ip"} {
			if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
				createValues[column] = clientIP
			}
		}
	}
	if err := session.Model(&entity.AccountDeviceBinding{}).Create(createValues).Error; err != nil {
		// 并发首次登记：另一个请求刚插入了同一行。这不是错误，
		// 再走一次 UPDATE 把 last_seen_at 刷新即可（语义与成功分支一致）。
		if updated, retryErr := updateExisting(); retryErr != nil || updated {
			return retryErr
		}
		return err
	}
	return nil
}

// truncateDeviceLabel 把设备展示名截断到列宽（varchar(64)，按字符而不是字节）。
func truncateDeviceLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return trimmed
}

// RevokeDeviceBinding 撤销某账号某台设备的绑定（用户移除 / 管理员解绑，137 §3.2）。
//
// **不删行**：删了之后「这台设备什么时候被谁移除」永远查不到，而封禁申诉需要它。
// 返回是否真的改动了行（false = 本来就没有活跃绑定，调用方据此给 404/幂等成功）。
func RevokeDeviceBinding(db *gorm.DB, userID int32, deviceID string, reason string) (bool, error) {
	if db == nil || userID <= 0 {
		return false, nil
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" {
		return false, nil
	}
	if !devicePoolSchemaAvailable(db) || !devicePoolRevokedColumnAvailable(db) {
		return false, ErrDevicePoolSchemaUnavailable
	}
	now := time.Now()
	result := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Where("user_id = ? AND device_id = ? AND revoked_at IS NULL", userID, normalized).
		Updates(map[string]any{
			"revoked_at":     now,
			"revoked_reason": reason,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// RevokeAllDeviceBindings 撤销某台设备在**所有账号**下的绑定（封禁设备时用，137 §2 决定 2）。
//
// 与 RevokeDeviceBinding 的分工：那个按 (user, device) 精确撤销，这个按 device 全量撤销。
// 封禁是全局事实，所以必须横扫所有账号 —— 只撤销当前账号会让「换个账号继续用」成立。
// 返回撤销行数。
func RevokeAllDeviceBindings(db *gorm.DB, deviceID string, reason string) (int64, error) {
	if db == nil {
		return 0, nil
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" {
		return 0, nil
	}
	if !devicePoolSchemaAvailable(db) || !devicePoolRevokedColumnAvailable(db) {
		return 0, ErrDevicePoolSchemaUnavailable
	}
	now := time.Now()
	result := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Where("device_id = ? AND revoked_at IS NULL", normalized).
		Updates(map[string]any{
			"revoked_at":     now,
			"revoked_reason": reason,
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// devicePoolFailOpenLog 是 fail-open 的统一留痕出口。
//
// 必须记：否则「上限长期未生效」与「上限正常生效但没人超限」在日志上完全同形，
// 运营会以为自己在限制设备而实际上全站放行（黑名单 fail-open 留痕是同一动机）。
// 用 SysLog 而不是 Error：这是既定降级策略，不是故障升级信号。
func devicePoolFailOpenLog(userID int32, detail string) {
	logger.SysLog("设备池判定降级放行（fail-open）：userID=" + strconv.FormatInt(int64(userID), 10) + "；" + detail)
}
