package service

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 设备池上限判定（121 §4.9 / 107 Q15-Q17 / Q16 遗留）。
//
// 这是 user_levels.device_limit 的**唯一判定点**。在此之前全仓只有配置与下发
// （user_level.go 的编辑/摘要、system_setting.go 的开关、user_level_migration.go 的种子），
// 判定点是 0 —— 也就是说「设备上限」这一档权益在代码里从未生效过。
//
// ## 三个不可协商的口径
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
//     本文件照此实现（判定侧已就绪），是否放开写入侧属 user_level.go 的决策，
//     不在本任务范围内（该文件本任务禁改）。
//
// ## 无历史回填
//
// 上线前的历史请求没有设备指纹，因此「已用设备数」从零起算。这意味着
// **上线初期每个用户的实际可用设备数 = 等级上限**，而不是「上限减去历史」。
// 换一个角度看，这对用户是**放宽**（不会因为历史而突然超限），
// 因此不需要任何过渡期告警；但运营口径文档里必须写清这一点。
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

// DevicePoolUsedUnknown 表示「本次判定没有统计已用设备数」。
//
// 为什么不是 0：0 是「已登记 0 台」这个真实状态。把「没统计」也写成 0，
// 日志里就分不清「不限档没统计」和「真的还没有任何设备」。
const DevicePoolUsedUnknown = -1

// 判定原因（稳定字符串，可直接落日志/上报，也便于用例断言）。
//
// ⚠️ 用例里故意写**字面量**而不是引用这些常量：常量改了名字或值，
// 若测试也引用常量，期望值会跟着一起漂移，等于这条断言恒真
// （本仓已因「期望值与被测实现共享常量」交付过假钉子）。
const (
	// DevicePoolReasonUnlimited：device_limit = 0，放行。
	DevicePoolReasonUnlimited = "unlimited"
	// DevicePoolReasonNoLimitConfigured：用户没有等级 / 等级未设上限，放行。
	DevicePoolReasonNoLimitConfigured = "no-limit-configured"
	// DevicePoolReasonFailOpen：表缺失 / 查询出错 / 参数不全，按 fail-open 放行。
	DevicePoolReasonFailOpen = "fail-open"
	// DevicePoolReasonForbidden：device_limit < 0，拒绝任何设备。
	DevicePoolReasonForbidden = "forbidden"
	// DevicePoolReasonAlreadyRegistered：该设备已登记过，不占新槽，放行。
	DevicePoolReasonAlreadyRegistered = "already-registered"
	// DevicePoolReasonWithinLimit：新设备且未达上限，放行。
	DevicePoolReasonWithinLimit = "within-limit"
	// DevicePoolReasonLimitExceeded：新设备且已达上限，拒绝。
	DevicePoolReasonLimitExceeded = "limit-exceeded"
	// DevicePoolReasonSwitchOff：总开关关闭（仅 WithSwitch 入口），放行。
	DevicePoolReasonSwitchOff = "switch-off"
)

// DevicePoolDeviceIDMaxLength 是设备指纹的截断长度（与 entity 列的 varchar(191) 对齐）。
const DevicePoolDeviceIDMaxLength = 191

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
	// Used 是已登记设备数；DevicePoolUsedUnknown(-1) 表示本次没有统计。
	Used int `json:"used"`
	// Enforced 表示本次**真的做了上限比较**。
	//
	// false 的三种情况：不限档（0）、没配上限（无等级）、fail-open。
	// 这三种都放行，但语义完全不同，混在一个 bool 里会让排障失去方向。
	Enforced bool `json:"enforced"`
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

// devicePoolSession 清掉继承的模型（同 userLevelSession / userLevelSession 的成因）。
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

// CountDevices 返回某账号已登记的设备数。
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
	var count int64
	if err := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// hasDevice 判断该账号是否已经登记过这台设备。
//
// 这是「不占新槽」的判据来源：已登记的设备重复登录必须放行，
// 否则上限 2 的用户用同一台设备登录第 3 次就会被自己拒掉。
func hasDevice(db *gorm.DB, userID int32, normalizedDeviceID string) (bool, error) {
	var count int64
	if err := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Where("user_id = ? AND device_id = ?", userID, normalizedDeviceID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// resolveDevicePoolLimit 读出该账号当前等级的 device_limit。
//
// 返回 known=false 表示「没有可用的上限」（用户还没评估等级 / 等级被删），
// 此时按 0（不限）的语义放行 —— 与 ResolveUserLevel 的 rank=0「无等级」一致。
func resolveDevicePoolLimit(db *gorm.DB, userID int32) (limit int32, known bool, err error) {
	if !userLevelSchemaAvailable(db) {
		return 0, false, ErrUserLevelSchemaUnavailable
	}
	levelID, err := CurrentUserLevelIDWithDB(db, userID)
	if err != nil {
		return 0, false, err
	}
	if levelID <= 0 {
		// 0 = 尚未评估 / 等级被删后回落。无等级的用户不设上限：
		// 强行给一个默认上限会让「等级表为空」的库把所有用户锁死。
		return 0, false, nil
	}
	var row struct {
		DeviceLimit int32
	}
	if err := userLevelSession(db).Table(entity.TableNameUserLevel).
		Select("device_limit AS device_limit").
		Where("id = ?", levelID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// level_id 指向一个已删除的等级：脏数据，按无上限处理（fail-open 的一种）。
			return 0, false, nil
		}
		return 0, false, err
	}
	return row.DeviceLimit, true, nil
}

// CanUseDevice 判定「该账号还能不能用这台设备」。
//
// 判定顺序（每一步为什么在这个位置都写在下面）：
//
//  0. 参数不全（db nil / userID<=0 / 设备指纹为空） → fail-open 放行
//  1. 绑定表不可用                                  → fail-open 放行 + SysLog 留痕
//  2. 等级体系读不出来                              → fail-open 放行
//  3. 没有等级（level_id=0）                        → 不限，放行
//  4. device_limit = 0                              → **不限**，放行（不可协商点 ①）
//  5. device_limit < 0                              → **拒绝**（禁止档，Q16 遗留出口）
//  6. 同一台设备已登记                              → 放行（不占新槽，判据 ④）
//  7. 已用数 >= 上限                                → 拒绝（判据 ①）
//  8. 其余                                          → 放行
//
// 第 6 步必须在第 7 步之前：顺序反了会让「已登记设备因超限被拒」，
// 表现为用户被自己的旧设备锁在门外。
func CanUseDevice(db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
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

	limit, known, err := resolveDevicePoolLimit(db, userID)
	if err != nil {
		devicePoolFailOpenLog(userID, "读取等级上限失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	if !known {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonNoLimitConfigured, Used: DevicePoolUsedUnknown}
	}
	if limit == DevicePoolUnlimited {
		// 不可协商点 ①：0 = 不限。这里**先于**任何计数返回，既省一次查询，
		// 也让「随便第 100 台都放行」在实现上是结构性的，而不是靠比较符号凑出来。
		return DevicePoolDecision{
			Allowed: true,
			Reason:  DevicePoolReasonUnlimited,
			Limit:   DevicePoolUnlimited,
			Used:    DevicePoolUsedUnknown,
		}
	}

	used, err := CountDevices(db, userID)
	if err != nil {
		devicePoolFailOpenLog(userID, "统计设备数失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Limit: limit, Used: DevicePoolUsedUnknown}
	}

	if limit < 0 {
		// Q16 遗留出口：0 已被「不限」占用，负数才是「禁止任何设备」。
		return DevicePoolDecision{Allowed: false, Reason: DevicePoolReasonForbidden, Limit: limit, Used: used, Enforced: true}
	}

	registered, err := hasDevice(db, userID, normalized)
	if err != nil {
		devicePoolFailOpenLog(userID, "查询设备登记失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Limit: limit, Used: DevicePoolUsedUnknown}
	}
	if registered {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonAlreadyRegistered, Limit: limit, Used: used, Enforced: true}
	}
	if used >= int(limit) {
		return DevicePoolDecision{Allowed: false, Reason: DevicePoolReasonLimitExceeded, Limit: limit, Used: used, Enforced: true}
	}
	return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonWithinLimit, Limit: limit, Used: used, Enforced: true}
}

// CanUseDeviceWithSwitch 是 CanUseDevice 的**开关感知**版本（接线层用）。
//
// system_settings.device_limit_enabled 默认 false（"启用账户设备池上限（默认关闭）"，
// 见 system_setting.go:1071）。关闭时整档限制不生效、零查询直接放行 ——
// 与黑名单总开关「关闭时零查询」的同款纪律。
//
// 为什么不在 CanUseDevice 内部直接读开关：核心判定要被单测直接驱动，
// 而开关是运营配置（默认关）。把两件事揉在一起，会让「等级上限算错了」
// 与「开关没打开」这两种完全不同的故障在测试里长得一模一样。
func CanUseDeviceWithSwitch(db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
	if !GetDeviceLimitEnabledWithDB(db) {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonSwitchOff, Used: DevicePoolUsedUnknown}
	}
	return CanUseDevice(db, userID, deviceID)
}

// RegisterDevice 登记「该账号用过这台设备」，并把判定结论一并返回。
//
// 返回 (decision, err)：
//   - decision.Allowed=false 时**没有任何写入**，err 为 nil（拒绝是正常业务结果，
//     不是错误 —— 用 error 传拒绝会让调用方把它记成 5xx）；
//   - err 只在「判定放行但写入失败」时非 nil，此时调用方**应当继续放行**
//     （写不进去只是这次没记上，不该把用户挡在门外；下次登录会再记一次）。
//
// 幂等（判据 ④）：先 insert-on-conflict-do-nothing，再无条件刷新 last_seen_at。
//   - 同一设备重复登记 → 只有一行，不占新槽；
//   - 并发首次登记 → 唯一索引挡住其中一个，两边都继续刷新 last_seen_at，
//     不会因为「撞唯一键」把并发登录判成失败。
//
// FirstSeenAt 只由插入时写一次；LastSeenAt 每次刷新 —— 后者是将来
// 「闲置设备自动释放」的唯一数据来源，现在不采就永远补不回来。
func RegisterDevice(db *gorm.DB, userID int32, deviceID string) (DevicePoolDecision, error) {
	decision := CanUseDevice(db, userID, deviceID)
	if !decision.Allowed {
		return decision, nil
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" || userID <= 0 {
		// 判定阶段已按 fail-open 放行，这里没有可落库的指纹。
		return decision, nil
	}
	if !devicePoolSchemaAvailable(db) {
		// 表不可用时判定已 fail-open 放行；登记失败也不影响本次请求。
		return decision, nil
	}

	now := time.Now()
	session := devicePoolSession(db)
	row := entity.AccountDeviceBinding{
		UserID:      userID,
		DeviceID:    normalized,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	// 冲突即忽略：不更新任何列（尤其是 first_seen_at），保证「首次」语义不被覆盖。
	if err := session.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return decision, err
	}
	if err := session.Model(&entity.AccountDeviceBinding{}).
		Where("user_id = ? AND device_id = ?", userID, normalized).
		Update("last_seen_at", now).Error; err != nil {
		return decision, err
	}
	return decision, nil
}

// devicePoolFailOpenLog 是 fail-open 的统一留痕出口。
//
// 必须记：否则「上限长期未生效」与「上限正常生效但没人超限」在日志上完全同形，
// 运营会以为自己在限制设备而实际上全站放行（黑名单 fail-open 留痕是同一动机）。
// 用 SysLog 而不是 Error：这是既定降级策略，不是故障升级信号。
func devicePoolFailOpenLog(userID int32, detail string) {
	logger.SysLog("设备池判定降级放行（fail-open）：userID=" + strconv.FormatInt(int64(userID), 10) + "；" + detail)
}
