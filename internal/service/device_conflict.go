package service

// 设备标识冲突检测（141 §4，判据 S5/S6）与会话数上报/心跳（141 §5.4 / §6.2，判据 S9）。
//
// # 一句话口径
//
// 冲突**只在一个 (user_id, device_id) 首次登记那一刻判一次**；之后同一设备的重连
// 一律放行，不比 instance。会话数的「存活」靠心跳：超过 3 分钟没上报就**读时**视为 0
// （不写回 0 —— 写回会与 CountDevices 抢同一列的写入权）。
//
// # 为什么「只判一次」是红线，而不是优化
//
// 141 §4.3 的补丁要求 `client_instance` **每次浏览器启动重新生成**（否则镜像复制
// 识别不出来）。这意味着同一台设备、同一个账号，第二次开机时 instance 一定变了。
// 若把判定写成「每次都比对 instance」，用户在**完全正常的日常使用**下每次重开浏览器
// 都会被判冲突 → 客户端换 device_id → 旧槽位不释放、新槽位又占一个 → 上限很快被打满，
// 而整个过程没有任何报错。这是 141 §12 第 2 条点名的「本设计最容易写错的一处」。
//
// 因此本文件的判定顺序是**结构性**的：
//
//	① 该 (user_id, device_id) 已有活跃行 → 放行，**根本不读 instance**
//	② 没有活跃行（首次登记）→ 才去查「这个 device_id 是否已被别的 instance 占用」
//
// 第 ① 步先返回，是保证「重开浏览器不误判」的唯一写法；靠后面的条件凑不出来。
//
// # 「被别的 instance 占用」为什么必须跨账号查
//
// device_id 的唯一索引是 `(user_id, device_id)`（141 §3.2 明确**不把** client_instance
// 加进索引，否则 exe 三个会话会变成三行）。所以在同一个账号内，「该 device_id 已登记」
// 与「该 (user_id, device_id) 已有行」是同一件事 —— 第 ① 步已经把它放行了。
// 剩下唯一可能有意义的问题就是：**这个 device_id 是不是已经被另一个账号/另一个实例
// 抢先占掉了**。这正是镜像复制（141 §4.3）的形态：同一份 profile 被复制给两个使用者。
// 所以本文件的占用查询是 `WHERE device_id = ?`（**不带 user_id**），
// 只排除 `revoked_at IS NOT NULL` 的行（撤销 = 释放，141 §3.2）。
//
// 已知局限（必须知道，不要当成 bug 修）：**同一账号内**的镜像复制识别不出来 ——
// 两份镜像属于同一账号时，第 ① 步就直接放行了，永远不会走到 instance 比对。
// 要识别它就必须每次都判，而那会把正常重开浏览器判成冲突（见上）。
// 两害相权取轻：宁可漏掉同账号镜像，也不能让日常使用天天冲突。
//
// # 历史行的 instance 是空串
//
// 141 之前登记的行没有 client_instance（列默认 ''）。空串表示「不知道这个 device_id
// 当初是谁登记的」，**不构成冲突证据**：拿 '' 与真实 UUID 比较必然不等，
// 若把它算成冲突，上线瞬间所有老设备都会把别的账号顶掉。因此空串一律跳过。
// 同理，本次请求没带 instance（旧客户端）时也不判 —— 没有可比对的东西。

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// DeviceClientInstanceMaxLength 与 141 §3.1 的 client_instance VARCHAR(64) 对齐。
	DeviceClientInstanceMaxLength = 64

	// DeviceConflictCode 是 409 响应体里的稳定 code（141 §6.3）。
	//
	// 客户端据此决定「重新生成 device_id + client_instance 并重试一次」，
	// 所以它是协议字段，不能跟着文案改。
	DeviceConflictCode = "DEVICE_ID_CONFLICT"

	// DeviceConflictMessage 是给用户看的文案（141 §6.3）。
	DeviceConflictMessage = "设备标识冲突，请重启以重新生成"

	// DevicePoolReasonDeviceIDConflict 是 DevicePoolDecision.Reason 的取值，
	// 与其它 *Reason 同一命名族（稳定字符串，可直接落日志/断言）。
	DevicePoolReasonDeviceIDConflict = "device-id-conflict"

	// DevicePoolReasonInstanceClear 表示「首次登记且没有任何实例占用证据」。
	//
	// 不复用 DevicePoolReasonWithinLimit：那个名字说的是「槽位还有空」，而本判定的
	// 结论是「这个 device_id 不归别人」，两者在排障时是完全不同的信息。
	DevicePoolReasonInstanceClear = "instance-clear"

	// DeviceSessionMaxCount 是单台设备会话数的上界。
	//
	// 8 来自 exe 的 HOME_MAX_SESSIONS（chaoxing_plus windows/native-source/home.c:14）。
	// 写死字面量而不是引用那个仓库的常量：两个仓库之间没有编译期依赖，
	// 而 141 §12 第 5 条已经写明「将来改上限要同步改这里」。
	DeviceSessionMaxCount = 8

	// DeviceSessionHeartbeatTTL 是心跳超时阈值（141 §5.4）。
	//
	// 3 分钟 = 允许漏报 2 次（exe 有 1 分钟级的刷新循环）。取更短会在网络抖动时
	// 把活着的会话判死，取更长则崩溃后的虚高值保留太久。
	DeviceSessionHeartbeatTTL = 3 * time.Minute
)

var (
	// ErrDeviceSessionBindingNotFound 表示该设备不属于当前账号、或已被撤销、或根本不存在。
	//
	// 三种情况**必须同一个错误**：调用方据此统一回 404，不泄露「这条记录存在但不属于你」
	// （与 device_user.go 红线 1 同一动机 —— 403 等于告诉攻击者 id 空间里有什么）。
	ErrDeviceSessionBindingNotFound = errors.New("设备记录不存在")

	// ErrDeviceSessionSchemaUnavailable 表示会话相关列还没迁移好（503，不是 500）。
	ErrDeviceSessionSchemaUnavailable = errors.New("设备会话列尚未就绪")

	// ErrDeviceSessionCountOutOfRange 表示 count 不在 0..DeviceSessionMaxCount。
	ErrDeviceSessionCountOutOfRange = errors.New("会话数超出允许范围")
)

// NormalizeDeviceClientInstance 归一化实例标识：去首尾空白 + 截断到 64 字符。
//
// 与 NormalizeDevicePoolDeviceID 同一取舍（按 rune 截断而不是字节，否则中文/emoji
// 会被截成半个字符，写进 varchar(64) 后 MySQL 报错、SQLite 静默存垃圾）。
//
// 返回空串表示「这次请求没有实例标识」—— 调用方**不据此判冲突**（见文件头最后一段）。
func NormalizeDeviceClientInstance(instance string) string {
	trimmed := strings.TrimSpace(instance)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > DeviceClientInstanceMaxLength {
		return string(runes[:DeviceClientInstanceMaxLength])
	}
	return trimmed
}

// deviceConflictInstanceColumnAvailable 判断 client_instance 列是否已就绪。
//
// 列由 141 §3.1 的迁移补（C1 批次），本文件**不加实体字段**（那是 C1 的独占范围），
// 一律用原生列名读写。列缺失时冲突检测整体降级为「不判」（fail-open）：
// 设备池是权益限制不是安全边界（device_pool.go 文件头第 2 条），
// 而「因为少一列就让所有登录 500」是不可接受的代价。
func deviceConflictInstanceColumnAvailable(db *gorm.DB) bool {
	if db == nil || !devicePoolSchemaAvailable(db) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "client_instance")
}

// deviceConflictClaimedInstances 返回某 device_id 名下**已占用实例**的集合。
//
// 口径（见文件头第 3 段）：
//   - **跨账号**：不带 user_id 条件 —— 同账号的情况在调用方第 ① 步已经放行；
//   - 只算**未撤销**的行：撤销 = 释放槽位，撤销后别人可以正常接手；
//   - 空串照原样返回（调用方跳过），因为「不知道」不等于「冲突」。
//
// 返回去重后的实例列表；表/列缺失时返回空切片（= 没有占用证据）。
func deviceConflictClaimedInstances(db *gorm.DB, normalizedDeviceID string) []string {
	if normalizedDeviceID == "" || !deviceConflictInstanceColumnAvailable(db) {
		return nil
	}
	query := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Select("client_instance AS client_instance").
		Where("device_id = ?", normalizedDeviceID)
	if scope, args := devicePoolActiveScope(db); scope != "" {
		query = query.Where(scope, args...)
	}
	var rows []struct {
		ClientInstance string `gorm:"column:client_instance"`
	}
	if err := query.Find(&rows).Error; err != nil {
		// 查询失败按「没有占用证据」处理并留痕：这条判定出错时的最坏后果是
		// 「这次没判出冲突」，而把错误抛上去会让登录直接失败。
		deviceConflictLogFailOpen(0, "查询 "+normalizedDeviceID+" 的实例占用失败："+err.Error())
		return nil
	}
	instances := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if seen[row.ClientInstance] {
			continue
		}
		seen[row.ClientInstance] = true
		instances = append(instances, row.ClientInstance)
	}
	return instances
}

// DecideDeviceRegistration 判定「(userID, deviceID, clientInstance) 这次登记是否会冲突」。
//
// **只读，不写任何行**。返回的 decision.Allowed=false 且 Reason=DevicePoolReasonDeviceIDConflict
// 时，调用方（RegisterDeviceWithOptions）**必须不写任何行** —— 冲突的设备不该占槽位，
// 否则「换个 instance 重试」的第二次请求会发现槽位已经被自己第一次请求吃掉了。
//
// 判定顺序（顺序本身是需求，见文件头）：
//
//	① 参数不全 / 表未就绪 / 没带 instance / 列未就绪 → 放行（fail-open）
//	② 该 (user_id, device_id) 已有活跃行            → 放行，**不比对 instance**（红线 1）
//	③ 该 device_id 已被某个**不同的非空** instance 占用 → 冲突
//	④ 其余                                          → 放行（首次登记）
//
// 同 instance 命中占用集合时放行（S6 对照组）：那是「同一份 profile 被同一实例重连」，
// 141 §4.2 明确是正常情形。
func DecideDeviceRegistration(db *gorm.DB, userID int32, deviceID string, clientInstance string) DevicePoolDecision {
	if db == nil || userID <= 0 {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}
	instance := NormalizeDeviceClientInstance(clientInstance)
	if instance == "" || !deviceConflictInstanceColumnAvailable(db) {
		// 没有可比对的实例（旧客户端 / 列还没补）→ 不做冲突判定。
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	}

	// ── ① 先看本账号是否已登记该设备：这是「只判首次」的落点 ──
	//
	// ⚠️ 这里**不能**顺手比 instance。141 §4.3 要求 instance 每次启动重新生成，
	// 同一账号重开浏览器时 instance 必然不同；在此比对 = 日常使用天天被判冲突。
	if _, bound, err := activeDeviceBindingID(db, userID, normalized); err != nil {
		deviceConflictLogFailOpen(userID, "查询本账号绑定失败："+err.Error())
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonFailOpen, Used: DevicePoolUsedUnknown}
	} else if bound {
		return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonAlreadyRegistered, Used: DevicePoolUsedUnknown}
	}

	// ── ② 首次登记：查这个 device_id 是否已被别的实例占用（跨账号口径） ──
	for _, claimed := range deviceConflictClaimedInstances(db, normalized) {
		if claimed == "" || claimed == instance {
			// '' = 历史行实例未知，不构成证据；== instance = 同一实例重连（S6）。
			continue
		}
		return DevicePoolDecision{
			Allowed: false,
			Reason:  DevicePoolReasonDeviceIDConflict,
			Used:    DevicePoolUsedUnknown,
			Message: DeviceConflictMessage,
		}
	}
	return DevicePoolDecision{Allowed: true, Reason: DevicePoolReasonInstanceClear, Used: DevicePoolUsedUnknown}
}

// RegisterDeviceWithInstance 是 RegisterDeviceWithOptions 的显式实例版（便于调用方阅读）。
//
// 存在的意义只有一个：让「登记时带了 instance」这件事在调用点可见。
// 冲突判定的实现不在这里 —— 它在 RegisterDeviceWithOptions 内部（device_pool.go），
// 这样任何登记入口（包括将来新增的）都自动受同一道判定保护，而不是靠每个调用方记得调。
func RegisterDeviceWithInstance(db *gorm.DB, userID int32, deviceID string, clientInstance string) (DevicePoolDecision, error) {
	return RegisterDeviceWithOptions(db, userID, deviceID, DeviceBindingRegisterOptions{
		ClientInstance: clientInstance,
	})
}

// deviceSessionColumnState 记录会话相关列在当前库上是否存在。
//
// 每次现判而不是启动时缓存：迁移是运行中由启动流程跑的，单测也会先建表再加列；
// 缓存会让第二种场景永远看不到新列（与 device_user.go 的 deviceUserColumnState 同一理由）。
type deviceSessionColumnState struct {
	// hasSessionCount：session_count 列（141 §3.1，C1 批次负责加）。
	hasSessionCount bool
	// hasReportedAt：session_reported_at 列（141 §5.4 的心跳时间戳）。
	//
	// 这一列**不在** 141 §3.1 的两列里，也还没有任何迁移建它。缺列时心跳退化为
	// 用 last_seen_at 当心跳时间戳 —— 合法理由：本文件的
	// ReportDeviceSessionCount 每次上报都会刷新 last_seen_at，
	// 所以「该行最后一次被上报」这件事在 last_seen_at 上成立。
	// 等迁移补上该列后自动切过去（HasColumn 现判），无需改调用方。
	hasReportedAt bool
}

func deviceSessionColumnsAvailable(db *gorm.DB) deviceSessionColumnState {
	var state deviceSessionColumnState
	if db == nil || !devicePoolSchemaAvailable(db) {
		return state
	}
	state.hasSessionCount = db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_count")
	state.hasReportedAt = db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_reported_at")
	return state
}

// effectiveDeviceSessionCount 是心跳超时的**唯一判定**（纯函数，不碰库）。
//
// 语义（141 §5.4）：
//   - stored <= 0                → 0（普通浏览器 / exe 全关）
//   - stored >  上限             → 夹到上限（脏数据防御：超界的值不该把配额算爆）
//   - heartbeatAt == nil         → 0（没有任何上报时间 = 无法证明这些会话还活着）
//   - now - heartbeatAt > TTL    → 0（exe 崩溃后停在上报前的值，视为已归零）
//   - 其余                        → stored
//
// ⚠️ 关键：**只影响读取结果，绝不写回库**。写回 0 会与 CountDevices（C1 独占）抢
// 同一列的写入权，表现为「计数忽大忽小」这种无法归因的抖动；而「读时视为 0」
// 是可逆的（下一次心跳到了自然恢复），也不需要定时任务。
func effectiveDeviceSessionCount(stored int, heartbeatAt *time.Time, now time.Time) int {
	if stored <= 0 {
		return 0
	}
	if stored > DeviceSessionMaxCount {
		stored = DeviceSessionMaxCount
	}
	if heartbeatAt == nil {
		return 0
	}
	if now.Sub(*heartbeatAt) > DeviceSessionHeartbeatTTL {
		return 0
	}
	return stored
}

// DeviceSessionReadResult 是一次「会话数读取」的结果。
//
// 结构体而不是裸 int：调用方（配额端点 / 列表）要能区分「确实是 0 个会话」与
// 「心跳过期所以按 0 算」—— 前者是 exe 全关的正常态，后者是 exe 可能崩了。
// 把两者糊成一个 0 会让排障失去方向（device_pool.go 的 DevicePoolDecision 同一动机）。
type DeviceSessionReadResult struct {
	// Count 是生效的会话数（已应用心跳超时）。
	Count int `json:"count"`
	// Stored 是库里的原始值（排障用）。
	Stored int `json:"stored"`
	// ReportedAt 是心跳时间戳（nil = 该行从未上报过）。
	ReportedAt *time.Time `json:"reportedAt"`
	// Fresh=true 表示上报时间在 TTL 内（Count == Stored 且 Stored > 0 的前提）。
	Fresh bool `json:"fresh"`
	// ColumnsReady=false 表示会话列还没迁移好，本次返回的 0 不代表「没有会话」。
	ColumnsReady bool `json:"columnsReady"`
}

// EffectiveDeviceSessionCount 是**读取路径**的会话数入口（141 §5.4，判据 S9）。
//
// 必须在读取侧调用它，而不是直接读 session_count 列 —— 直接读会把 exe 崩溃前的
// 虚高值当成事实（S9 的反向变异：去掉超时判定 → 崩溃场景虚高，必红）。
//
// 返回 (result, error)：error 只表示基础设施故障（查询失败）。
// 列未就绪时返回 ColumnsReady=false 且 Count=0、error=nil —— 那是「还不知道」，
// 不是故障（与 CountDevices 返回 ErrDevicePoolSchemaUnavailable 的分工不同：
// 那个在显式查询里要报错，这里被上层用来算配额，返回错误会让客户端的配额页 500）。
func EffectiveDeviceSessionCount(db *gorm.DB, userID int32, deviceID string, now time.Time) (DeviceSessionReadResult, error) {
	result := DeviceSessionReadResult{}
	if db == nil || userID <= 0 {
		return result, nil
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" {
		return result, nil
	}
	state := deviceSessionColumnsAvailable(db)
	if !state.hasSessionCount {
		return result, nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	columns := "session_count AS session_count, last_seen_at AS last_seen_at"
	if state.hasReportedAt {
		columns += ", session_reported_at AS session_reported_at"
	}
	var row struct {
		SessionCount     int        `gorm:"column:session_count"`
		LastSeenAt       *time.Time `gorm:"column:last_seen_at"`
		SessionReportedA *time.Time `gorm:"column:session_reported_at"`
	}
	query := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Select(columns).
		Where("user_id = ? AND device_id = ?", userID, normalized)
	if scope, args := devicePoolActiveScope(db); scope != "" {
		query = query.Where(scope, args...)
	}
	if err := query.Order("id asc").Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 没登记过该设备：0 个会话，列是就绪的（区别于 ColumnsReady=false）。
			result.ColumnsReady = true
			return result, nil
		}
		return result, err
	}

	heartbeat := row.LastSeenAt
	if state.hasReportedAt && row.SessionReportedA != nil {
		heartbeat = row.SessionReportedA
	}
	result.ColumnsReady = true
	result.Stored = row.SessionCount
	result.ReportedAt = heartbeat
	result.Count = effectiveDeviceSessionCount(row.SessionCount, heartbeat, now)
	result.Fresh = result.Count > 0 && result.Count == clampDeviceSessionCount(row.SessionCount)
	return result, nil
}

// clampDeviceSessionCount 把存量值夹到 0..上限（与 effectiveDeviceSessionCount 的夹取一致）。
func clampDeviceSessionCount(stored int) int {
	if stored <= 0 {
		return 0
	}
	if stored > DeviceSessionMaxCount {
		return DeviceSessionMaxCount
	}
	return stored
}

// ReportDeviceSessionCount 写入一次会话数上报（141 §6.2 的服务端落库部分）。
//
// ## 幂等与乱序
//
// 请求体里**没有客户端时间戳**（141 §6.2 只有 {deviceId, count}），所以服务端只能
// 采取「后到者覆盖」：重复上报同一 count 是幂等的（写同一个值），乱序到达的旧值会被
// 下一次心跳自然纠正。这不是偷懒 —— 要做到「拒绝旧值」就必须有可比的客户端时刻，
// 而 exe 侧的时间不可信（用户可改系统时钟）。
//
// ## 404 口径
//
// 归属 / 撤销 / 不存在三种情况走**同一个**错误（ErrDeviceSessionBindingNotFound）：
// 用 `revoked_at IS NULL` 作为查询条件，RowsAffected 语义被完全绕开 —— 不区分
// 「不存在」与「不是你的」是红线（141 §6.2「否则 404 不泄露存在性」）。
//
// ## 为什么不用 RowsAffected 判成功
//
// MySQL 的 UPDATE 默认返回「**实际改变**的行数」，值相同时返回 0。若拿 RowsAffected==0
// 当「设备不存在」，重复上报（count 不变）会被误判成 404。因此这里先按归属查一次 id，
// 再按 id 更新，更新影响 0 行只说明并发删改，不改变响应。
func ReportDeviceSessionCount(db *gorm.DB, userID int32, deviceID string, count int, now time.Time) error {
	if count < 0 || count > DeviceSessionMaxCount {
		return ErrDeviceSessionCountOutOfRange
	}
	if db == nil {
		return ErrDeviceSessionSchemaUnavailable
	}
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	if normalized == "" || userID <= 0 {
		return ErrDeviceSessionBindingNotFound
	}
	state := deviceSessionColumnsAvailable(db)
	if !state.hasSessionCount {
		return ErrDeviceSessionSchemaUnavailable
	}
	if now.IsZero() {
		now = time.Now()
	}

	id, bound, err := activeDeviceBindingID(db, userID, normalized)
	if err != nil {
		return err
	}
	if !bound {
		return ErrDeviceSessionBindingNotFound
	}

	// UpdateColumns（不是 Updates）：只写这三列，**不**顺带刷新 updated_at。
	// 心跳是「这台设备还活着」的业务事实，不是「这行被编辑过」的元信息；
	// 混在一起会让「按 updated_at 判龄」的后续逻辑把心跳当成编辑（本仓踩过）。
	updates := map[string]any{
		"session_count": count,
		"last_seen_at":  now,
	}
	if state.hasReportedAt {
		updates["session_reported_at"] = now
	}
	res := devicePoolSession(db).Model(&entity.AccountDeviceBinding{}).
		Where("id = ?", id).
		UpdateColumns(updates)
	if res.Error != nil {
		return res.Error
	}
	// res.RowsAffected == 0：并发场景下行刚被撤销/删除。用户的意图（上报）已经无法
	// 落到这台设备上，但报错也没有意义 —— 下一次心跳会按 404 如实反馈。
	return nil
}

// DeviceConflictDecisionBody 返回 409 响应体（141 §6.3）。
//
// 放在服务层是为了让「code 的值」只有一个来源：客户端拿 code 决定是否重新生成
// device_id，code 与文案分离，文案改了不会破坏协议。
func DeviceConflictDecisionBody() map[string]any {
	return map[string]any{
		"code":    DeviceConflictCode,
		"message": DeviceConflictMessage,
	}
}

// deviceConflictLogFailOpen 是冲突判定留痕的统一出口（同 devicePoolFailOpenLog 的动机）。
//
// 必须记：否则「冲突检测长期未生效」与「冲突检测正常但没人冲突」在日志上完全同形。
// userID 传 0 表示「尚未定位到具体账号」（例如占用集合查询，它按 device_id 跨账号查）。
// 密码/令牌一类的值一律过 RedactSecrets：这里会拼数据库错误原文。
func deviceConflictLogFailOpen(userID int32, detail string) {
	scope := "n/a"
	if userID > 0 {
		scope = strconv.FormatInt(int64(userID), 10)
	}
	logger.SysLog("设备冲突检测降级（fail-open）：userID=" + scope + "；" + logger.RedactSecrets(detail))
}
