package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 设备粒度黑名单 + 设备管理员端服务层（137-设备体系设计-授权与黑名单.md §3.4 / §5.1 / §8.2）。
//
// # 这一层解决的是什么
//
// 黑名单原本只有 ip / phone / wechat / region 四类粒度（46-黑名单设计.md §1），
// 它们都是「按请求来源」或「按账号凭据」拦人。设备体系引入了全新的判定维度：
// 一台设备可能被多个账号轮流使用（工作室批量的典型形态），
// 因此必须能在**设备维度**上一次性拦死，且不受「用户还能自己解绑」的影响。
//
// 两条口径差异必须一开始就分清，混在一起必然出错：
//
//	全局封禁（本文件的 BanDevice）：写 blacklist_entries(kind=device) + 撤销全部绑定，
//	                                用户**无法自行解除**（不在 /user/devices 的接口里）。
//	用户拉黑（A2 的 user_device_blocks）：只是「我不想用这台设备」，
//	                                用户自己随时能解除，对别人毫无影响。
//
// # 为什么不复用 CreateBlacklistEntryWithDB / ListBlacklistEntriesWithDB
//
// 那两个函数在本任务的可写范围之外（internal/service/blacklist.go 属既有文件），
// 而它们的类型校验与列表筛选都把 kind 硬编码成「四类之一」——
// `validateBlacklistKindFilter` 遇到 device 会直接返回 ErrInvalidBlacklistKind，
// `NormalizeBlacklistValue` 的 switch 也没有 device 分支。
// 强行调用只会让「封禁成功」变成 400。
//
// 所以在**不新建表**（红线 3：复用 blacklist_entries）的前提下，本文件自己实现
// device 这一类的归一化 / 落库 / 列表查询，并刻意遵守 blacklist.go 里那三条纪律：
//  1. value_hash = sha256(归一化后的 pattern)，pattern 存归一化原文（运营要能看懂）；
//  2. value_masked 由服务端算好，调用方不允许自己填（否则会出现「掩码与原文对不上」）；
//  3. Enabled 的 default:true 零值陷阱 —— 新建时一律 Enabled=true，
//     需要停用走单列 Update（本文件不提供「建一条停用的 device 规则」的入口，
//     因为封禁是即时生效动作，建成停用等于没封）。

// BlacklistKindDevice 是设备粒度的 kind 取值（137 §3.4）。
//
// ⚠️ 它必须与 blacklist.go 里的四个常量处于同一命名族，但**不能**加进
// `NormalizeBlacklistValue` 的 switch 里冒充第五类 —— 那会让管理页的通用新增框
// 也能提交 device 规则，而 device 的 pattern 归一化口径（去空白 + 191 截断）
// 与其它四类完全不同。设备封禁只走本文件的 BanDevice。
const BlacklistKindDevice = "device"

// 绑定行的撤销原因（与 137 §3.2 的 revoked_reason 取值表对齐）。
//
// 只在这里定义 device 相关的两个：
//
//	device_banned   —— 全局封禁触发的立即撤销（用户拍板 A：当场掉线）
//	admin_unbound   —— 管理员强制解绑（只断绑定，**不封设备**）
//
// ⚠️ 另外两个（user_removed / idle_10d）分别属于 A2 用户端与 A1 闲置清理，
// 不由本文件产生，因此不在这里定义 —— 定义未使用的常量会让「谁写这个值」
// 失去唯一性。
const (
	// DeviceBindingRevokedReasonBanned 是「因全局封禁而撤销」。
	DeviceBindingRevokedReasonBanned = "device_banned"
	// DeviceBindingRevokedReasonAdminUnbound 是「管理员强制解绑」。
	DeviceBindingRevokedReasonAdminUnbound = "admin_unbound"
)

// blacklistDeviceIDMaxLength 与 account_device_bindings.device_id 的 varchar(191) 对齐。
//
// 191 而不是 255：MySQL utf8mb4 下老版本索引前缀上限 767 字节（191*4=764）。
// 与 entity.AccountDeviceBinding 的注释同一取舍，判定与截断必须同单位（rune）。
const blacklistDeviceIDMaxLength = 191

// ErrInvalidDeviceID 表示设备 ID 为空/不可用（400，不是 500）。
var ErrInvalidDeviceID = errors.New("设备 ID 不能为空")

// ErrDeviceBindingNotFound 表示绑定行不存在（404）。
var ErrDeviceBindingNotFound = errors.New("设备绑定记录不存在")

// ErrDeviceBindingSchemaUnavailable 表示绑定表还没迁移好（503）。
var ErrDeviceBindingSchemaUnavailable = errors.New("设备绑定表尚未就绪")

// ErrDeviceRevocationUnavailable 表示绑定表缺少撤销列（A1-2 的五列尚未落地）。
//
// 它是**降级**信号而不是硬失败：黑名单判定（137 §5.1 第 1 步）优先于「已绑定」，
// 所以即使撤销没生效，封禁本身的拦截效果仍然成立。
// 但缺列时绝不能假装撤销成功 —— 那样 D12 的验收判据会被一套「什么都没做」的实现骗过。
var ErrDeviceRevocationUnavailable = errors.New("设备绑定表缺少撤销列（revoked_at / revoked_reason）")

// NormalizeBlacklistDeviceID 归一化设备指纹：去首尾空白 + 截断到 191 字符。
//
// **不转小写**：设备指纹（扩展生成的 UUID / `exe:<hash>`）大小写敏感，
// 转小写会把两台不同设备并成一台 —— 与 wechat OpenID 同一取舍（46-*.md §1.2）。
//
// 这个函数是「写入侧」与「判定侧」的**唯一**规范化入口：
// BanDevice 用它算 value_hash，MatchDevice 用它算查缓存的 key。
// 任何一侧绕过它直接比原文，都会表现为「封禁成功但从不命中」（D-变异：去掉 Normalize → 必红）。
func NormalizeBlacklistDeviceID(deviceID string) string {
	trimmed := strings.TrimSpace(deviceID)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > blacklistDeviceIDMaxLength {
		return string(runes[:blacklistDeviceIDMaxLength])
	}
	return trimmed
}

// MaskBlacklistDeviceID 生成设备 ID 的展示掩码（前 8 位 + 末 4 位）。
//
// 为什么不直接用 blacklist.go 的 MaskBlacklistValue：它的 default 分支返回 "***"，
// 对 device 没有任何信息量，管理员在看板/命中记录里无法核对「是不是这台设备」。
//
// 也不保留全文：device_id 是可用作设备识别的凭据级字符串，
// 命中记录会被导出排查（46-*.md §3.2 的同一动机）。
func MaskBlacklistDeviceID(deviceID string) string {
	normalized := NormalizeBlacklistDeviceID(deviceID)
	if normalized == "" {
		return "***"
	}
	runes := []rune(normalized)
	if len(runes) <= 8 {
		// 短 ID 只留首字符：留一半也可能被拼回去。
		return string(runes[:1]) + "***"
	}
	return string(runes[:8]) + "…" + string(runes[len(runes)-4:])
}

// deviceAdminSession 会话工具：清掉继承的模型，避免跨 model 复用 panic。
//
// ⚠️ 不是洁癖：controller 会把 `withDatabasePriority` 的会话传进来，
// 也可能有人拿 dao 的 UnderlyingDB 直接调本文件。那里 Statement 已解析成另一张表，
// 拿它 Create(&entity.AccountDeviceBinding{}) 会 panic: reflect: Field index out of range
// （本仓已实测踩过两次，见项目记忆）。
func deviceAdminSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// deviceBindingSchemaReady 判断绑定表结构可用（表 + 两列必备列）。
func deviceBindingSchemaReady(db *gorm.DB) bool {
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

// deviceRevocationColumnsAvailable 判断撤销列是否齐备（A1-2 的五列之一）。
//
// 不齐备时的行为见 ErrDeviceRevocationUnavailable：封禁照做、撤销如实报 0 + warning。
func deviceRevocationColumnsAvailable(db *gorm.DB) bool {
	if !deviceBindingSchemaReady(db) {
		return false
	}
	if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "revoked_at") {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "revoked_reason")
}

// DeviceBanRequest 是封禁一台设备的入参。
type DeviceBanRequest struct {
	// DeviceID 是运营输入（未归一化），BanDevice 内部归一化。
	DeviceID string
	// Reason 必填（与黑名单其余四类一致：运营要能解释为什么封）。
	Reason string
	// ExpiresAt 为 nil 表示永久封禁。
	ExpiresAt *time.Time
	// CreatedBy 是操作管理员 ID（0 = 系统）。
	CreatedBy int32
}

// DeviceBanResult 是一次封禁的结果。
//
// 每一项都是「运营需要看到的事实」，不是内部细节：
//   - RevokedBindings>0 才说明「用户当场掉线」；
//   - RevocationApplied=false 时前端必须提示「撤销未生效（表结构未就绪）」，
//     否则运营会以为设备已经踢下线，而实际上只是下次请求才被黑名单挡住。
type DeviceBanResult struct {
	EntryID     int64      `json:"entryId"`
	DeviceID    string     `json:"deviceId"`
	ValueMasked string     `json:"valueMasked"`
	Reason      string     `json:"reason"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Permanent   bool       `json:"permanent"`
	// AlreadyBanned 表示此前已有一条生效中的同 device 规则（本次是更新而非新建）。
	AlreadyBanned bool `json:"alreadyBanned"`
	// RevokedBindings 是本次被撤销的绑定行数（revoked_at IS NULL 的那些）。
	RevokedBindings int64 `json:"revokedBindings"`
	// RevocationApplied 表示本次**真的执行了**撤销语句。
	RevocationApplied bool `json:"revocationApplied"`
	// RevocationWarning 非空时说明撤销没做成，需要人工处理。
	RevocationWarning string `json:"revocationWarning"`
}

// BanDevice 封禁一台设备：写黑名单 + 撤销该 device 的全部在用绑定（D12）。
//
// # 为什么两件事必须在同一个事务里
//
// 用户拍板 A 的口径是「立即撤销，用户当场掉线」。如果只写黑名单不撤销绑定，
// 功能上仍能拦（137 §5.1 要求黑名单判定优先于「已绑定」），
// 但「已登录的会话」不会立刻失效 —— 而这正是拍板 A 要的效果。
// 两者分开提交时，中途失败会留下「黑名单有规则、绑定还在」的半成品状态，
// 且没有任何报错（D12 的变异就是这种形态）。
//
// # 幂等
//
// 同一台设备重复封禁不报错：已有规则就更新 reason / expiresAt / enabled，
// 已经把 revoked_at 写过的绑定行不会被再写一次（`revoked_at IS NULL` 判据），
// 因此第二次调用返回 RevokedBindings=0。
func BanDevice(db *gorm.DB, request DeviceBanRequest) (DeviceBanResult, error) {
	if db == nil {
		return DeviceBanResult{}, ErrBlacklistCacheNoDatabase
	}
	deviceID := NormalizeBlacklistDeviceID(request.DeviceID)
	if deviceID == "" {
		return DeviceBanResult{}, ErrInvalidDeviceID
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		// 400 而不是静默补默认值：审计里出现「无原因」等于没有审计（46-*.md §2.1）。
		return DeviceBanResult{}, fmt.Errorf("%w：封禁原因不能为空", ErrInvalidDeviceID)
	}
	reason = truncateBlacklistRunes(reason, 255)

	if !db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		return DeviceBanResult{}, fmt.Errorf("封禁设备失败：%w", ErrBlacklistCacheNoDatabase)
	}

	hash := BlacklistPatternHashHex(deviceID)
	now := time.Now()
	// 撤销列是否齐备要在外层判：迁移在跑，不该在事务里做 schema 探测。
	revocationReady := deviceRevocationColumnsAvailable(db)

	result := DeviceBanResult{
		DeviceID:          deviceID,
		ValueMasked:       MaskBlacklistDeviceID(deviceID),
		Reason:            reason,
		ExpiresAt:         request.ExpiresAt,
		Permanent:         request.ExpiresAt == nil,
		RevocationApplied: revocationReady,
	}

	tx := db.Begin()
	if tx.Error != nil {
		return DeviceBanResult{}, fmt.Errorf("封禁设备失败：%w", tx.Error)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	var existing entity.BlacklistEntry
	findErr := deviceAdminSession(tx).
		Where("kind = ? AND value_hash = ?", BlacklistKindDevice, hash).
		Take(&existing).Error
	switch {
	case findErr == nil:
		result.AlreadyBanned = existing.Enabled && (existing.ExpiresAt == nil || existing.ExpiresAt.After(now))
		// 更新为「生效 + 新原因 + 新到期时间」。
		//
		// 必须用 Updates(map) 修 4 列而不是 Save：实体带 `default:true` 标签，
		// Save 会把零值字段（ExpiresAt 为 nil）按默认值语义重排，历史坑已记录在
		// entity/blacklist.go 的注释里。
		if err := deviceAdminSession(tx).Model(&entity.BlacklistEntry{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"pattern":      deviceID,
				"value_masked": result.ValueMasked,
				"match_type":   BlacklistMatchExact,
				"reason":       reason,
				"enabled":      true,
				"expires_at":   request.ExpiresAt,
			}).Error; err != nil {
			return DeviceBanResult{}, fmt.Errorf("更新设备黑名单规则失败：%w", err)
		}
		result.EntryID = existing.ID
	case errors.Is(findErr, gorm.ErrRecordNotFound):
		entry := entity.BlacklistEntry{
			Kind:        BlacklistKindDevice,
			Pattern:     deviceID,
			ValueHash:   hash,
			ValueMasked: result.ValueMasked,
			MatchType:   BlacklistMatchExact,
			Reason:      reason,
			Enabled:     true,
			ExpiresAt:   request.ExpiresAt,
			CreatedBy:   request.CreatedBy,
		}
		if err := deviceAdminSession(tx).Create(&entry).Error; err != nil {
			// 并发封禁同一台设备会撞 (kind, value_hash, match_type) 唯一索引。
			// 这不是失败：对方已经把规则建好了，再读一次按「已存在」处理即可。
			var raced entity.BlacklistEntry
			if retryErr := deviceAdminSession(tx).
				Where("kind = ? AND value_hash = ?", BlacklistKindDevice, hash).
				Take(&raced).Error; retryErr != nil {
				return DeviceBanResult{}, fmt.Errorf("写入设备黑名单规则失败：%w", err)
			}
			result.EntryID = raced.ID
			result.AlreadyBanned = true
		} else {
			result.EntryID = entry.ID
		}
	default:
		return DeviceBanResult{}, fmt.Errorf("查询设备黑名单规则失败：%w", findErr)
	}

	// 第二件事：把该 device_id 的**所有在用绑定**撤销（D12）。
	if revocationReady {
		affected, err := revokeDeviceBindings(tx, deviceID, now, DeviceBindingRevokedReasonBanned)
		if err != nil {
			// 真错误必须回滚：留下「黑名单有规则、绑定还在」的半成品正是 D12 要防的。
			return DeviceBanResult{}, err
		}
		result.RevokedBindings = affected
	} else {
		result.RevocationApplied = false
		result.RevocationWarning = "设备绑定表缺少 revoked_at / revoked_reason 列，本次只写了黑名单；已登录会话不会立即失效（黑名单判定仍生效）"
		logger.SysWarn("封禁设备 " + result.ValueMasked + " 时未能撤销绑定：" + ErrDeviceRevocationUnavailable.Error())
	}

	if err := tx.Commit().Error; err != nil {
		return DeviceBanResult{}, fmt.Errorf("封禁设备失败：%w", err)
	}
	committed = true
	return result, nil
}

// revokeDeviceBindings 撤销某台设备的全部在用绑定，返回受影响行数。
//
// 判据必须是 `revoked_at IS NULL`：已撤销的行不重复写，
// 否则「第二次封禁」会把首次撤销的时间戳覆盖掉，而那是申诉时唯一的证据。
//
// 用 Updates(map) 而不是 Update：两列要一起写（时间戳 + 原因），
// 分两次写会在中间态存在「有 revoked_at 但没有原因」的行 —— 那种行在前端
// 只能显示成「已解绑（原因未知）」，运营无从判断是用户自己移除还是被封。
func revokeDeviceBindings(tx *gorm.DB, deviceID string, now time.Time, reason string) (int64, error) {
	if tx == nil {
		return 0, nil
	}
	if !deviceBindingSchemaReady(tx) {
		return 0, nil
	}
	res := deviceAdminSession(tx).Model(&entity.AccountDeviceBinding{}).
		Where("device_id = ? AND revoked_at IS NULL", deviceID).
		Updates(map[string]any{
			"revoked_at":     now,
			"revoked_reason": reason,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("撤销设备绑定失败：%w", res.Error)
	}
	return res.RowsAffected, nil
}

// DeviceUnbanResult 是一次解封的结果。
type DeviceUnbanResult struct {
	DeviceID string `json:"deviceId"`
	// Deleted 是删掉的黑名单规则条数（0 表示本来就没封）。
	Deleted int64 `json:"deleted"`
}

// UnbanDevice 解封一台设备（删掉 kind=device 的规则）。
//
// **不恢复绑定**：撤销是既成事实（谁在什么时候被踢下线，是审计数据）。
// 解封后该设备下次请求会按 137 §7 的「静默重绑」重新登记，
// 那一侧由 A1 的判定流程负责，本函数不重复实现（任务书：不实现 A1 范围逻辑）。
//
// 物理删除与黑名单其余四类一致（规则类不做软删除，46-*.md §2.1）。
// 命中记录（blacklist_hits）不跟着删，因此「这条规则当时拦了多少次」仍可查。
func UnbanDevice(db *gorm.DB, deviceID string) (DeviceUnbanResult, error) {
	if db == nil {
		return DeviceUnbanResult{}, ErrBlacklistCacheNoDatabase
	}
	normalized := NormalizeBlacklistDeviceID(deviceID)
	if normalized == "" {
		return DeviceUnbanResult{}, ErrInvalidDeviceID
	}
	result := DeviceUnbanResult{DeviceID: normalized}
	if !db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		return result, nil
	}
	hash := BlacklistPatternHashHex(normalized)
	res := deviceAdminSession(db).Where("kind = ? AND value_hash = ?", BlacklistKindDevice, hash).
		Delete(&entity.BlacklistEntry{})
	if res.Error != nil {
		return DeviceUnbanResult{}, fmt.Errorf("解封设备失败：%w", res.Error)
	}
	result.Deleted = res.RowsAffected
	return result, nil
}

// DeviceBanRecord 是一条生效中的设备封禁（列表展示 / 标记用）。
type DeviceBanRecord struct {
	EntryID     int64
	DeviceID    string
	ValueMasked string
	Reason      string
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

// LoadActiveDeviceBansWithDB 按 device_id 批量查生效中的封禁规则。
//
// 返回 map 的 key 是**归一化后的 device_id**（不是 hash）：
// 调用方手里有 device_id，让它们各自算 hash 会让「归一化口径」出现第二份实现。
//
// 生效口径与缓存完全一致（否则会出现「列表显示已封禁、实际不拦」）：
//
//	enabled = true 且 (expires_at IS NULL 或 expires_at > now)
func LoadActiveDeviceBansWithDB(db *gorm.DB, deviceIDs []string, now time.Time) (map[string]DeviceBanRecord, error) {
	bans := make(map[string]DeviceBanRecord)
	if db == nil || len(deviceIDs) == 0 {
		return bans, nil
	}
	if !db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		return bans, nil
	}
	hashes := make([]string, 0, len(deviceIDs))
	normalizedByHash := make(map[string]string, len(deviceIDs))
	for _, raw := range deviceIDs {
		normalized := NormalizeBlacklistDeviceID(raw)
		if normalized == "" {
			continue
		}
		hash := BlacklistPatternHashHex(normalized)
		hashes = append(hashes, hash)
		normalizedByHash[hash] = normalized
	}
	if len(hashes) == 0 {
		return bans, nil
	}
	var rows []entity.BlacklistEntry
	if err := deviceAdminSession(db).
		Where("kind = ? AND value_hash IN ? AND enabled = ?", BlacklistKindDevice, hashes, true).
		Find(&rows).Error; err != nil {
		return bans, fmt.Errorf("查询设备封禁规则失败：%w", err)
	}
	for _, row := range rows {
		// 过期判定放在 Go 里（SQLite 与 MySQL 的时间比较语义不一致，放一侧更稳，
		// 与 LoadBlacklistSnapshot 同一做法）。
		if row.ExpiresAt != nil && !now.Before(*row.ExpiresAt) {
			continue
		}
		deviceID := normalizedByHash[row.ValueHash]
		if deviceID == "" {
			deviceID = NormalizeBlacklistDeviceID(row.Pattern)
		}
		bans[deviceID] = DeviceBanRecord{
			EntryID:     row.ID,
			DeviceID:    deviceID,
			ValueMasked: row.ValueMasked,
			Reason:      row.Reason,
			ExpiresAt:   row.ExpiresAt,
			CreatedAt:   row.CreatedAt,
		}
	}
	return bans, nil
}

// OrphanDeviceBan 是「有封禁规则但没有任何绑定行」的设备。
//
// 为什么需要它：137 §9.2 的设计表格里有一行 `exe:e5f6…  —  已封禁  封禁中 [解封]`，
// 即设备已经被解绑/从未绑定，但封禁仍然生效。若列表只渲染绑定行，
// 那批设备在管理页上完全不可见 ⇒ 永远无法解封。
//
// 它们不进主分页（主分页按绑定行计），单独作为附加数组返回，
// 因此不会破坏「total 与 data 一一对应」这条分页不变量。
type OrphanDeviceBan struct {
	EntryID     int64      `json:"entryId"`
	DeviceID    string     `json:"deviceId"`
	ValueMasked string     `json:"valueMasked"`
	Reason      string     `json:"reason"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Permanent   bool       `json:"permanent"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// ListOrphanDeviceBansWithDB 返回没有任何绑定行的生效中封禁（最多 limit 条）。
//
// 用 `pattern NOT IN (SELECT device_id FROM account_device_bindings)`：
// 只排除「完全没有任何绑定」的设备；有历史（已撤销）绑定的设备仍在主列表里，
// 因为它的封禁状态在那一行上就能看到。
func ListOrphanDeviceBansWithDB(db *gorm.DB, now time.Time, limit int) ([]OrphanDeviceBan, error) {
	out := []OrphanDeviceBan{}
	if db == nil {
		return out, nil
	}
	if !db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		return out, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := deviceAdminSession(db).
		Where("kind = ? AND enabled = ?", BlacklistKindDevice, true).
		// ⚠️ 括号不能省：GORM 把多个 Where 用 AND 串起来，
		// 裸写 `... AND expires_at IS NULL OR expires_at > ?` 会被解析成
		// `(kind=? AND enabled=? AND expires_at IS NULL) OR expires_at > ?` ——
		// 于是**已停用的规则也会被返回**（第二个 OR 分支只要未到期就成立）。
		// 这是 SQL 优先级陷阱，不报错、只是结果错。
		Where("(expires_at IS NULL OR expires_at > ?)", now)
	if deviceBindingSchemaReady(db) {
		query = query.Where("pattern NOT IN (SELECT device_id FROM account_device_bindings)")
	}
	var rows []entity.BlacklistEntry
	if err := query.Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return out, fmt.Errorf("查询孤立设备封禁失败：%w", err)
	}
	for _, row := range rows {
		out = append(out, OrphanDeviceBan{
			EntryID:     row.ID,
			DeviceID:    NormalizeBlacklistDeviceID(row.Pattern),
			ValueMasked: row.ValueMasked,
			Reason:      row.Reason,
			ExpiresAt:   row.ExpiresAt,
			Permanent:   row.ExpiresAt == nil,
			CreatedAt:   row.CreatedAt,
		})
	}
	return out, nil
}

// 设备在管理页上的三态（稳定字符串，前端直接消费）。
const (
	// AdminDeviceStatusActive：绑定在用（revoked_at IS NULL）且未被全局封禁。
	AdminDeviceStatusActive = "active"
	// AdminDeviceStatusRevoked：绑定已撤销（用户移除 / 管理员解绑 / 闲置清理）。
	AdminDeviceStatusRevoked = "revoked"
	// AdminDeviceStatusBanned：该 device_id 命中全局封禁（优先级最高）。
	//
	// 优先级必须是 banned > revoked > active：一台被封禁的设备即使绑定还没被撤销，
	// 它在页面上也必须显示「封禁中」——否则运营会以为封禁没生效而去重复点击。
	AdminDeviceStatusBanned = "banned"
)

// AdminDeviceListQuery 是设备列表的筛选条件（137 §8.2 / §9.2）。
type AdminDeviceListQuery struct {
	// Keyword 同时匹配 device_id 与 username（137 §9.2 的搜索框文案就是这两个）。
	Keyword string
	// Status 取 active / revoked / banned；空 = 全部。
	Status string
	// UserID > 0 时只返回该账号的设备（A4-2：用户管理页的「设备」按钮）。
	//
	// 为什么必须做成**服务端过滤**而不是前端拉全量再筛：
	// 设备列表是分页的，前端只拿到当前页 20 条。在客户端筛「这个用户的设备」，
	// 结果会随「他的设备恰好落在第几页」而变 —— 用户在第 1 页看到 2 台、
	// 翻到第 2 页又看到 1 台，而他实际有 3 台。这类错误极难被用户描述清楚。
	//
	// 0 = 不过滤（设备管理页的行为，保持不变）。
	UserID int32
}

// AdminDeviceItem 是设备列表的一行。
type AdminDeviceItem struct {
	ID       int64  `json:"id"`
	UserID   int32  `json:"userId"`
	Username string `json:"username"`
	DeviceID string `json:"deviceId"`
	// DeviceLabel 是扩展上报的可读标签（如 `Chrome on Windows #a1b2`）。
	// A1-2 加列之前它恒为空串 —— 前端必须能显示「—」而不是崩。
	DeviceLabel string `json:"deviceLabel"`
	// OwnerKind 取 login / token（A1-2 加列前恒为空串）。
	OwnerKind     string     `json:"ownerKind"`
	FirstSeenAt   *time.Time `json:"firstSeenAt"`
	LastSeenAt    *time.Time `json:"lastSeenAt"`
	RevokedAt     *time.Time `json:"revokedAt"`
	RevokedReason string     `json:"revokedReason"`
	// Status 取 active / revoked / banned。
	Status string `json:"status"`
	// Banned 与 Ban* 是全局封禁的派生字段（与 Status 一致，但给出可展示的原因）。
	Banned       bool       `json:"banned"`
	BanEntryID   int64      `json:"banEntryId"`
	BanReason    string     `json:"banReason"`
	BanExpiresAt *time.Time `json:"banExpiresAt"`
	BanPermanent bool       `json:"banPermanent"`
}

// AdminDeviceListResult 是设备列表的响应体（与其它管理端列表同形态）。
type AdminDeviceListResult struct {
	Data     []AdminDeviceItem `json:"data"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"pageSize"`
	// OrphanBans 是「有封禁、无绑定」的设备（见 OrphanDeviceBan 注释）。
	OrphanBans []OrphanDeviceBan `json:"orphanBans"`
	// RevocationSupported=false 表示绑定表缺撤销列，页面必须据此隐藏/禁用
	// 「解绑」按钮并解释原因，而不是点了才 503。
	RevocationSupported bool `json:"revocationSupported"`
}

// adminDeviceBindingRow 是列表查询的扫描结构。
//
// 字段带显式 column tag：缺列时我们用 `” AS xxx` / `NULL AS xxx` 补位，
// 依赖命名策略映射在方言间不够稳定（SQLite 与 MySQL 的隐式别名大小写规则不同）。
type adminDeviceBindingRow struct {
	ID            int64      `gorm:"column:id"`
	UserID        int32      `gorm:"column:user_id"`
	DeviceID      string     `gorm:"column:device_id"`
	DeviceLabel   string     `gorm:"column:device_label"`
	OwnerKind     string     `gorm:"column:owner_kind"`
	FirstSeenAt   *time.Time `gorm:"column:first_seen_at"`
	LastSeenAt    *time.Time `gorm:"column:last_seen_at"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
	RevokedReason string     `gorm:"column:revoked_reason"`
}

// ListAdminDeviceBindingsWithDB 返回设备列表（137 §8.2 GET /admin/devices）。
//
// # 为什么不用 JOIN users 取用户名
//
// 本仓的列表一律「先查主表、再批量补名字」（user_ban_admin.go 同款）：
// JOIN 在两种方言下的别名/空值语义差异会引出「MySQL 有名字、SQLite 没名字」这类
// 只在 CI 上出现的红。而 keyword 需要按 username 过滤，那一段用**子查询**表达，
// 语义在两侧完全一致。
//
// # 缺列的处置
//
// device_label / owner_kind / revoked_at / revoked_reason 由 A1-2 加列。
// 本文件在缺列时用常量表达式补位（`” AS device_label` / `NULL AS revoked_at`），
// 于是页面在迁移没跑的库上仍能正常列出设备 —— 而不是整页 500。
func ListAdminDeviceBindingsWithDB(db *gorm.DB, query AdminDeviceListQuery, page int, pageSize int) (AdminDeviceListResult, error) {
	if db == nil {
		return AdminDeviceListResult{}, ErrBlacklistCacheNoDatabase
	}
	page, pageSize = normalizeBlacklistPage(page, pageSize)
	result := AdminDeviceListResult{
		Data:       []AdminDeviceItem{},
		Page:       page,
		PageSize:   pageSize,
		OrphanBans: []OrphanDeviceBan{},
	}
	if !deviceBindingSchemaReady(db) {
		// 表还没迁移：返回空列表 + 明确标记，而不是 500。
		// 管理页在旧库上显示「暂无设备」比整页报错更有用（写操作仍会如实报错）。
		result.RevocationSupported = false
		return result, nil
	}
	result.RevocationSupported = deviceRevocationColumnsAvailable(db)

	now := time.Now()
	hasLabel := db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_label")
	hasOwnerKind := db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "owner_kind")
	hasRevocation := result.RevocationSupported

	base := deviceAdminSession(db).Table(entity.TableNameAccountDeviceBinding)

	// 按账号过滤（A4-2：用户管理页的「设备」按钮）。
	//
	// 放在关键词之前：两个条件是 AND 关系，顺序不影响结果，但放最前面能让
	// 「只查某个用户」这个最常用的入口走上最窄的索引前缀
	// （uk_account_device_binding_user_device 的第一列就是 user_id）。
	if query.UserID > 0 {
		base = base.Where("user_id = ?", query.UserID)
	}

	// 关键词：device_id 子串 OR username 子串（后者走 users 子查询）。
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		clause := "INSTR(LOWER(device_id), LOWER(?)) > 0"
		args := []any{keyword}
		if db.Migrator().HasTable(entity.TableNameUser) &&
			db.Migrator().HasColumn(entity.TableNameUser, "username") {
			clause += " OR user_id IN (SELECT id FROM users WHERE INSTR(LOWER(username), LOWER(?)) > 0)"
			args = append(args, keyword)
		}
		base = base.Where("("+clause+")", args...)
	}

	// 状态筛选。
	switch strings.ToLower(strings.TrimSpace(query.Status)) {
	case "", "all":
		// 不筛选。
	case AdminDeviceStatusActive:
		if hasRevocation {
			base = base.Where("revoked_at IS NULL")
		}
	case AdminDeviceStatusRevoked:
		if hasRevocation {
			base = base.Where("revoked_at IS NOT NULL")
		} else {
			// 缺列时「已撤销」不存在：返回空集而不是「全部」——
			// 后者会让运营以为所有设备都被撤销过。
			base = base.Where("1 = 0")
		}
	case AdminDeviceStatusBanned:
		// 用子查询与 Go 侧的标记口径**逐字一致**（同一 kind/enabled/expires 条件），
		// 否则会出现「筛选出 3 条、其中 0 条标着已封禁」。
		if db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
			base = base.Where(
				"device_id IN (SELECT pattern FROM blacklist_entries WHERE kind = ? AND enabled = ? AND (expires_at IS NULL OR expires_at > ?))",
				BlacklistKindDevice, true, now)
		} else {
			base = base.Where("1 = 0")
		}
	default:
		return AdminDeviceListResult{}, fmt.Errorf("%w：设备状态必须是 active / revoked / banned", ErrInvalidBlacklistPattern)
	}

	if err := base.Session(&gorm.Session{}).Count(&result.Total).Error; err != nil {
		return AdminDeviceListResult{}, fmt.Errorf("统计设备失败：%w", err)
	}

	labelExpr := "'' AS device_label"
	if hasLabel {
		labelExpr = "COALESCE(device_label, '') AS device_label"
	}
	ownerExpr := "'' AS owner_kind"
	if hasOwnerKind {
		ownerExpr = "COALESCE(owner_kind, '') AS owner_kind"
	}
	revokedAtExpr := "NULL AS revoked_at"
	revokedReasonExpr := "'' AS revoked_reason"
	if hasRevocation {
		revokedAtExpr = "revoked_at"
		revokedReasonExpr = "COALESCE(revoked_reason, '') AS revoked_reason"
	}
	selectCols := strings.Join([]string{
		"id", "user_id", "device_id",
		labelExpr, ownerExpr,
		"first_seen_at", "last_seen_at",
		revokedAtExpr, revokedReasonExpr,
	}, ", ")

	var rows []adminDeviceBindingRow
	if err := base.Session(&gorm.Session{}).
		Select(selectCols).
		Order("last_seen_at desc").Order("id desc").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&rows).Error; err != nil {
		return AdminDeviceListResult{}, fmt.Errorf("查询设备列表失败：%w", err)
	}
	if len(rows) == 0 {
		// ⚠️ 按 userId 过滤时**不返回全局孤儿封禁**（A4-2）。
		//
		// OrphanBans 的语义是「device_id 没有任何绑定行的封禁」—— 它天然是
		// **跨账号**的（谁知道那台设备曾属于谁）。在用户管理页展示它，等于
		// 在「张三的设备」面板里列出一堆别人的封禁记录，用户无法判断哪条与他有关。
		//
		// 更糟的是「该用户一台设备都没有」与「他有一台被封禁的孤儿设备」在
		// 前端会长得一模一样（都是列表空 + 一堆孤儿行），而后者的正确处理是
		// 「去设备管理页解封」，前者是「他还没用过任何设备」。
		if query.UserID > 0 {
			return result, nil
		}
		orphans, orphanErr := ListOrphanDeviceBansWithDB(db, now, 50)
		if orphanErr != nil {
			logger.SysWarn("查询孤立设备封禁失败：" + orphanErr.Error())
		} else {
			result.OrphanBans = orphans
		}
		return result, nil
	}

	userIDs := make([]int32, 0, len(rows))
	deviceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
		deviceIDs = append(deviceIDs, row.DeviceID)
	}
	names, nameErr := loadUsernamesWithDB(db, userIDs)
	if nameErr != nil {
		// 名字查不到不该让整页 500：设备维度才是本页的主语，
		// 名字缺失只是「无法显示归属」。
		logger.SysWarn("查询设备归属用户名失败：" + nameErr.Error())
	}
	bans, banErr := LoadActiveDeviceBansWithDB(db, deviceIDs, now)
	if banErr != nil {
		logger.SysWarn("查询设备封禁状态失败：" + banErr.Error())
	}

	for _, row := range rows {
		item := AdminDeviceItem{
			ID:            row.ID,
			UserID:        row.UserID,
			Username:      names[row.UserID],
			DeviceID:      NormalizeBlacklistDeviceID(row.DeviceID),
			DeviceLabel:   row.DeviceLabel,
			OwnerKind:     row.OwnerKind,
			FirstSeenAt:   row.FirstSeenAt,
			LastSeenAt:    row.LastSeenAt,
			RevokedAt:     row.RevokedAt,
			RevokedReason: row.RevokedReason,
			Status:        AdminDeviceStatusActive,
		}
		if row.RevokedAt != nil {
			item.Status = AdminDeviceStatusRevoked
		}
		if ban, ok := bans[item.DeviceID]; ok {
			item.Status = AdminDeviceStatusBanned
			item.Banned = true
			item.BanEntryID = ban.EntryID
			item.BanReason = ban.Reason
			item.BanExpiresAt = ban.ExpiresAt
			item.BanPermanent = ban.ExpiresAt == nil
		}
		result.Data = append(result.Data, item)
	}

	orphans, orphanErr := ListOrphanDeviceBansWithDB(db, now, 50)
	if orphanErr != nil {
		logger.SysWarn("查询孤立设备封禁失败：" + orphanErr.Error())
	} else {
		result.OrphanBans = orphans
	}
	return result, nil
}

// loadUsernamesWithDB 批量取用户名（id → username）。
//
// 单独成函数：与 user_ban_admin.go 的 names 补齐同一动机（避免 JOIN 的方言差异），
// 且它允许 users 表缺失时返回空 map（旧库/裁剪部署）。
func loadUsernamesWithDB(db *gorm.DB, userIDs []int32) (map[int32]string, error) {
	names := make(map[int32]string, len(userIDs))
	if db == nil || len(userIDs) == 0 {
		return names, nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return names, nil
	}
	// 去重：同一账号可能有多台设备，IN 里重复 ID 不影响语义但会让 SQL 变长。
	seen := make(map[int32]struct{}, len(userIDs))
	unique := make([]int32, 0, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return names, nil
	}
	var rows []struct {
		ID       int32  `gorm:"column:id"`
		Username string `gorm:"column:username"`
	}
	if err := deviceAdminSession(db).Table(entity.TableNameUser).
		Select("id, username").
		Where("id IN ?", unique).
		Scan(&rows).Error; err != nil {
		return names, fmt.Errorf("查询用户名失败：%w", err)
	}
	for _, row := range rows {
		names[row.ID] = row.Username
	}
	return names, nil
}

// UnbindDeviceBindingWithDB 强制解绑一行绑定（137 §8.2 POST /admin/devices/:id/unbind）。
//
// 三条不可协商的性质：
//
//  1. **只断绑定，不封设备**：本函数绝不写 blacklist_entries。
//     写黑名单是 BanDevice 的职责，混进来会让「解绑」变成不可逆的封禁（用户无法自救）。
//  2. 不删行：撤销是写 revoked_at + revoked_reason，行保留（137 §3.2）。
//     删行会让「这台设备什么时候被谁移除」永远查不到，而封禁申诉正需要它。
//  3. 幂等：已经是撤销状态的行不重复写（否则会把首次撤销时间戳覆盖掉）。
//
// 返回解绑后的行（含 username），供前端原地更新而不必整页重载。
func UnbindDeviceBindingWithDB(db *gorm.DB, id int64, now time.Time) (AdminDeviceItem, error) {
	if db == nil {
		return AdminDeviceItem{}, ErrBlacklistCacheNoDatabase
	}
	if id <= 0 {
		return AdminDeviceItem{}, ErrDeviceBindingNotFound
	}
	if !deviceBindingSchemaReady(db) {
		return AdminDeviceItem{}, ErrDeviceBindingSchemaUnavailable
	}
	if !deviceRevocationColumnsAvailable(db) {
		return AdminDeviceItem{}, ErrDeviceRevocationUnavailable
	}
	if now.IsZero() {
		now = time.Now()
	}

	var row adminDeviceBindingRow
	if err := deviceAdminSession(db).Table(entity.TableNameAccountDeviceBinding).
		Select("id, user_id, device_id, COALESCE(device_label, '') AS device_label, COALESCE(owner_kind, '') AS owner_kind, first_seen_at, last_seen_at, revoked_at, COALESCE(revoked_reason, '') AS revoked_reason").
		Where("id = ?", id).
		Scan(&row).Error; err != nil {
		return AdminDeviceItem{}, fmt.Errorf("查询设备绑定失败：%w", err)
	}
	if row.ID == 0 {
		return AdminDeviceItem{}, ErrDeviceBindingNotFound
	}
	if row.RevokedAt == nil {
		res := deviceAdminSession(db).Model(&entity.AccountDeviceBinding{}).
			Where("id = ? AND revoked_at IS NULL", id).
			Updates(map[string]any{
				"revoked_at":     now,
				"revoked_reason": DeviceBindingRevokedReasonAdminUnbound,
			})
		if res.Error != nil {
			return AdminDeviceItem{}, fmt.Errorf("强制解绑失败：%w", res.Error)
		}
		if res.RowsAffected > 0 {
			row.RevokedAt = &now
			row.RevokedReason = DeviceBindingRevokedReasonAdminUnbound
		} else {
			// 并发：另一个请求刚撤销了它。重新读回真实状态，不假装成功。
			var reread adminDeviceBindingRow
			if err := deviceAdminSession(db).Table(entity.TableNameAccountDeviceBinding).
				Select("id, revoked_at, COALESCE(revoked_reason, '') AS revoked_reason").
				Where("id = ?", id).Scan(&reread).Error; err == nil && reread.ID != 0 {
				row.RevokedAt = reread.RevokedAt
				row.RevokedReason = reread.RevokedReason
			}
		}
	}

	names, nameErr := loadUsernamesWithDB(db, []int32{row.UserID})
	if nameErr != nil {
		logger.SysWarn("查询设备归属用户名失败：" + nameErr.Error())
	}
	deviceID := NormalizeBlacklistDeviceID(row.DeviceID)
	item := AdminDeviceItem{
		ID:            row.ID,
		UserID:        row.UserID,
		Username:      names[row.UserID],
		DeviceID:      deviceID,
		DeviceLabel:   row.DeviceLabel,
		OwnerKind:     row.OwnerKind,
		FirstSeenAt:   row.FirstSeenAt,
		LastSeenAt:    row.LastSeenAt,
		RevokedAt:     row.RevokedAt,
		RevokedReason: row.RevokedReason,
		Status:        AdminDeviceStatusActive,
	}
	if row.RevokedAt != nil {
		item.Status = AdminDeviceStatusRevoked
	}
	// 解绑不封设备，但该 device_id 可能**本来就被封着**（另一条路径封的）：
	// 此时状态必须如实显示「封禁中」，否则运营会以为解绑等于解封。
	bans, banErr := LoadActiveDeviceBansWithDB(db, []string{deviceID}, now)
	if banErr != nil {
		logger.SysWarn("查询设备封禁状态失败：" + banErr.Error())
	}
	if ban, ok := bans[deviceID]; ok {
		item.Status = AdminDeviceStatusBanned
		item.Banned = true
		item.BanEntryID = ban.EntryID
		item.BanReason = ban.Reason
		item.BanExpiresAt = ban.ExpiresAt
		item.BanPermanent = ban.ExpiresAt == nil
	}
	return item, nil
}
