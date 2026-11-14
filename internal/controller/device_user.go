package controller

// 「我的设备」用户端接口（137-设备体系设计 §8.1 / A2 批次）。
//
// 六个端点：
//
//	GET    /user/devices                我的设备列表（含已撤销、标注拉黑状态）
//	POST   /user/devices/:id/remove     移除（**软撤销**：写 revoked_at，不删行）
//	POST   /user/devices/:id/block      拉黑该设备（写 user_device_blocks）
//	DELETE /user/devices/blocks/:id     解除拉黑（**删行**）
//	GET    /user/device-limit           当前生效上限 + 来源 + 等级值
//	PUT    /user/device-limit           自设上限（只能降不能升）
//
// ## 四条不可协商的红线（来自任务书，逐条对应到代码位置）
//
//  1. **归属校验，越权一律 404 而非 403**：`WHERE id = ? AND user_id = ?` 是唯一
//     查询入口；RowsAffected==0 与「行不存在」走同一个 404 文案，不泄露存在性。
//     用 403 等于告诉攻击者「这条记录存在但不属于你」—— 顺着 id 递增扫一遍
//     就能数出全站有多少设备。
//
//  2. **移除 = 软撤销**，绝不 DELETE 绑定行（137 §3.2）：删行会导致
//     「这台设备什么时候被谁移除」永远查不到，而封禁申诉需要这个。
//
//  3. **拉黑用独立表 `user_device_blocks`**，不复用 `revoked_reason`：
//     绑定行会被「静默重绑」清空 revoked_at，而拉黑状态必须持续存在 ——
//     混在一行里，重绑就等于解除拉黑（137 §3.3）。
//
//  4. **自设上限的口径必须与 EffectiveDeviceLimit 一致**：
//     `service.EffectiveDeviceLimit` 是上限的**唯一权威计算**，本文件的 GET 直接
//     调它；PUT 的上界校验用的是**等级值 L**（不是生效值），因为生效值可能已经
//     是本用户自己的 override —— 拿它当上界会让「先设 1 台就再也改不回 3 台」。
//
// ## 为什么不直接用 entity.AccountDeviceBinding 结构体查询
//
// A1 批次（d137-a1）负责给该实体加 `owner_kind` / `token_id` / `device_label` /
// `revoked_at` / `revoked_reason` 5 列，本批次**不碰那个文件**（独占范围）。
// 若这里引用 `row.RevokedAt`，在 A1 未落地的分支上直接编译失败。因此本文件
// 一律用**表级查询 + 匿名列别名**（`'' AS device_label` / `NULL AS revoked_at`），
// 并按 HasColumn 结果决定是取真列还是取常量 —— 这样：
//   - A1 落地前后都能编译（不依赖实体字段）；
//   - 老库（列还没补）时退化为「没有撤销功能」的旧行为，而不是报错。
//
// MERGE 后可以（但不必须）把它简化成实体字段访问；那是一次纯重构，风险自负。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 设备行的三种状态（137 §9.1 的「状态」列）。
//
// ⚠️ 与本仓其它枚举同一纪律：用例里写**字面量**，不引用这些常量。
// 期望值若引用被测常量，常量漂移时两侧同步漂移 → 断言恒真（本仓交付过假钉子）。
const (
	deviceUserStatusActive  = "active"  // 使用中（revoked_at IS NULL 且未被拉黑）
	deviceUserStatusRemoved = "removed" // 已移除（revoked_at 非空）
	deviceUserStatusBlocked = "blocked" // 已拉黑（user_device_blocks 命中，优先级最高）
)

// 撤销原因：用户自己移除（137 §3.2 的四个取值之一）。
//
// 刻意写死字面量而不引用 A1 的 entity.DeviceBindingRevokeReasonUserRemoved：
// 该常量属于 A1 的 entity 文件，本批次不可依赖（编译顺序问题同上）。
// 合并后应改成引用它，否则两处字面量会漂移。
const deviceUserRevokeReasonUserRemoved = "user_removed"

// EffectiveDeviceLimit 的 source 取值中「用户自设覆盖」那一个（137 §4）。
// 同样写字面量，理由同上（该常量由 A1 在 service 包内新增）。
const deviceUserLimitSourceUserOverride = "user_override"

// device_limit_override 的「不覆盖」标记（137 §3.1）。A1 的 entity/service 常量
// 在 devA2 上不存在，故此处同样写字面量。
const deviceUserLimitOverrideNotSet int32 = -1

// deviceUserDBOverride 允许测试注入数据库（nil = 走正常解析路径）。
//
// 与 devicePoolDBOverride / adminLoginDBOverride 同一动机：本包大量用例会改写
// dao 全局单例，注入点让「写到哪个库」是确定的，而不是取决于用例执行顺序。
var deviceUserDBOverride *gorm.DB

// deviceUserDB 返回本组接口使用的数据库句柄。
//
// 三档：测试注入 → resolveSearchPersistenceDB（dao/manager）→ nil。
// 每一档都 `NewDB: true`：清掉生成式 DAO 残留的 model/schema，
// 否则拿 dao.Log 的句柄去查 account_device_bindings 会
// `panic: reflect: Field index out of range`（本仓实测踩过）。
func deviceUserDB(c *gin.Context) *gorm.DB {
	if deviceUserDBOverride != nil {
		return deviceUserDBOverride.Session(&gorm.Session{NewDB: true})
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		return nil
	}
	return withDatabasePriority(db, databaseRequestContext(c), dbqueue.PriorityLow)
}

// deviceUserBindingRow 是「我的设备」列表的一行（表级查询用的扁平结构）。
//
// 不复用 entity.AccountDeviceBinding：见文件头的说明（A1 才会加那 5 列）。
type deviceUserBindingRow struct {
	ID            int64      `gorm:"column:id"`
	DeviceID      string     `gorm:"column:device_id"`
	DeviceLabel   string     `gorm:"column:device_label"`
	LastSeenAt    *time.Time `gorm:"column:last_seen_at"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
	RevokedReason string     `gorm:"column:revoked_reason"`
}

// deviceUserColumnState 记录 A1 那几列在当前库上是否存在。
//
// 「列在不在」必须在**每次请求**判（不是启动时缓存一次）：迁移是在运行中由
// 启动流程跑的，而单测会先建表再加列 —— 缓存会让第二种场景永远看不到新列。
type deviceUserColumnState struct {
	hasLabel   bool // device_label
	hasRevoked bool // revoked_at + revoked_reason
}

func deviceUserReadColumnState(db *gorm.DB) deviceUserColumnState {
	var state deviceUserColumnState
	if db == nil || !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		return state
	}
	state.hasLabel = db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_label")
	state.hasRevoked = db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "revoked_at") &&
		db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "revoked_reason")
	return state
}

// deviceUserBindingSelect 拼出 SELECT 列表：缺列时用常量占位，保证扫描不报错。
//
// 为什么用常量占位而不是「缺列就不选」：后者要求每处查询都带两套扫描结构，
// 而这里同一份 `deviceUserBindingRow` 就能覆盖两种库状态 —— 更少的漂移面。
func deviceUserBindingSelect(state deviceUserColumnState) string {
	label := "'' AS device_label"
	if state.hasLabel {
		label = "device_label AS device_label"
	}
	revokedAt, revokedReason := "NULL AS revoked_at", "'' AS revoked_reason"
	if state.hasRevoked {
		revokedAt = "revoked_at AS revoked_at"
		revokedReason = "revoked_reason AS revoked_reason"
	}
	return "id AS id, device_id AS device_id, " + label + ", last_seen_at AS last_seen_at, " +
		revokedAt + ", " + revokedReason
}

// deviceUserBlocksTableAvailable 判断拉黑表是否就绪。
//
// 缺表时两个读接口仍要能工作（列表里就不标「已拉黑」），
// 两个写接口返回 503 —— 不能静默返回成功，否则用户以为拉黑生效了。
func deviceUserBlocksTableAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	return db.Migrator().HasTable(entity.TableNameUserDeviceBlock)
}

// deviceUserDeviceItem 是响应里的单台设备。
type deviceUserDeviceItem struct {
	ID            int64      `json:"id"`
	DeviceID      string     `json:"deviceId"`
	DeviceLabel   string     `json:"deviceLabel"`
	LastSeenAt    *time.Time `json:"lastSeenAt"`
	Status        string     `json:"status"`
	StatusText    string     `json:"statusText"`
	RevokedAt     *time.Time `json:"revokedAt"`
	RevokedReason string     `json:"revokedReason"`
	// Blocked=是否为用户拉黑状态；BlockID 是解除拉黑要用的 id（0=未拉黑）。
	Blocked bool  `json:"blocked"`
	BlockID int64 `json:"blockId"`
	// BlockOnly=true 表示这条只有拉黑记录、没有绑定行（历史数据/绑定行被物理删除）。
	//
	// 为什么要在列表里出现：不出现的话用户看得到「已拉黑」却找不到解除入口，
	// 只能找客服。这类孤儿行必须可自救。
	BlockOnly bool `json:"blockOnly"`
	// Removable=当前是否可点「移除」（已撤销的行没有意义再撤销一次）。
	Removable bool `json:"removable"`
}

// deviceUserStatusText 把状态翻译成界面文案（137 §9.1：使用中 / 已移除 / 已拉黑）。
func deviceUserStatusText(status string) string {
	switch status {
	case deviceUserStatusBlocked:
		return "已拉黑"
	case deviceUserStatusRemoved:
		return "已移除"
	default:
		return "使用中"
	}
}

// deviceUserBlockRow 是拉黑表的一行。
type deviceUserBlockRow struct {
	ID        int64     `gorm:"column:id"`
	DeviceID  string    `gorm:"column:device_id"`
	Reason    string    `gorm:"column:reason"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// deviceUserLoadBlocks 取某用户全部拉黑记录（device_id → 行）。
func deviceUserLoadBlocks(db *gorm.DB, userID int32) (map[string]deviceUserBlockRow, error) {
	blocks := map[string]deviceUserBlockRow{}
	if db == nil || userID <= 0 || !deviceUserBlocksTableAvailable(db) {
		return blocks, nil
	}
	var rows []deviceUserBlockRow
	if err := db.Table(entity.TableNameUserDeviceBlock).
		Select("id AS id, device_id AS device_id, reason AS reason, created_at AS created_at").
		Where("user_id = ?", userID).
		Order("id desc").
		Find(&rows).Error; err != nil {
		return blocks, err
	}
	for _, row := range rows {
		blocks[row.DeviceID] = row
	}
	return blocks, nil
}

// deviceUserNotFound 统一 404 文案。
//
// ⚠️ 「不存在」与「不是你的」必须**同一个**文案与状态码（红线 1）：
// 任何区分都会把「这条记录存在」当成可用信息泄露出去。
func deviceUserNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"message": "设备记录不存在"})
}

// deviceUserParseID 解析路径参数 :id（正整数）。非法时写 404 并返回 false。
//
// 用 404 而不是 400：非法 id 与「不属于你的 id」对外必须无法区分（红线 1）。
func deviceUserParseID(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		deviceUserNotFound(c)
		return 0, false
	}
	return id, true
}

// deviceUserFindBinding 按 (id, user_id) 查一行绑定。
//
// 返回 (nil, nil) = 不存在或不属于当前用户 —— 调用方一律 404。
// 返回 (nil, err) = 基础设施故障（503/500），与上面的语义必须区分开：
// 把查询失败当成「不存在」会让用户以为自己的设备被删了。
func deviceUserFindBinding(db *gorm.DB, userID int32, id int64) (*deviceUserBindingRow, error) {
	if db == nil || userID <= 0 || id <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		return nil, nil
	}
	state := deviceUserReadColumnState(db)
	var row deviceUserBindingRow
	err := db.Table(entity.TableNameAccountDeviceBinding).
		Select(deviceUserBindingSelect(state)).
		Where("id = ? AND user_id = ?", id, userID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// deviceUserListTimeout 是列表查询的排序表达式：使用中在前，再按最近活跃倒序。
//
// 用 CASE 而不是「查两次再拼」：两次查询会在并发撤销时产生同一行出现在两侧的
// 瞬间，列表里就会出现重复设备（前端 key 冲突、按钮错位）。
func deviceUserListOrder(state deviceUserColumnState) string {
	active := ""
	if state.hasRevoked {
		active = "CASE WHEN revoked_at IS NULL THEN 0 ELSE 1 END, "
	}
	return active + "last_seen_at DESC, id DESC"
}

// UserDevices 返回**当前登录用户自己**的设备列表（137 §8.1）。
//
//	GET /api/user/devices
//
// 包含已撤销的设备（标注「已移除」）与已拉黑的设备（标注「已拉黑」）：
// 用户要能把它们找回来 —— 只显示「使用中」会让「我拉黑了哪些设备」无处可查，
// 也就无从解除（137 §9.1 的界面草图明确有「已失效」行）。
//
// 不接受任何 userId 参数：目标账号恒取当前登录用户（否则就是越权面）。
func UserDevices(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	db := deviceUserDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法读取设备列表"})
		return
	}
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		// 表还没迁移好：设备池本身也处于「不限制」的降级态，
		// 这里返回空列表而不是 503 —— 用户看到「还没有设备」是对的。
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"devices":     []deviceUserDeviceItem{},
			"total":       0,
			"active":      0,
			"blocked":     0,
			"schemaReady": false,
		}})
		return
	}

	state := deviceUserReadColumnState(db)
	blocks, err := deviceUserLoadBlocks(db, operator.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取设备拉黑记录失败"})
		return
	}

	var rows []deviceUserBindingRow
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select(deviceUserBindingSelect(state)).
		Where("user_id = ?", operator.ID).
		Order(deviceUserListOrder(state)).
		Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取设备列表失败"})
		return
	}

	devices := make([]deviceUserDeviceItem, 0, len(rows))
	active, blocked := 0, 0
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		seen[row.DeviceID] = true
		item := deviceUserDeviceItem{
			ID:            row.ID,
			DeviceID:      row.DeviceID,
			DeviceLabel:   row.DeviceLabel,
			LastSeenAt:    row.LastSeenAt,
			RevokedAt:     row.RevokedAt,
			RevokedReason: row.RevokedReason,
			Status:        deviceUserStatusActive,
		}
		if row.RevokedAt != nil {
			item.Status = deviceUserStatusRemoved
		}
		if block, hit := blocks[row.DeviceID]; hit {
			// 拉黑优先于撤销：一台已移除又被拉黑的设备，用户要看到的是
			// 「已拉黑」（因为解除拉黑才是可执行动作）。
			item.Status = deviceUserStatusBlocked
			item.Blocked = true
			item.BlockID = block.ID
			blocked++
		} else if row.RevokedAt == nil {
			active++
		}
		item.StatusText = deviceUserStatusText(item.Status)
		item.Removable = row.RevokedAt == nil
		devices = append(devices, item)
	}

	// 孤儿拉黑记录（有拉黑、无绑定行）：必须出现在列表里，否则无法解除。
	orphans := make([]deviceUserDeviceItem, 0, len(blocks))
	for deviceID, block := range blocks {
		if seen[deviceID] {
			continue
		}
		orphans = append(orphans, deviceUserDeviceItem{
			ID:         0,
			DeviceID:   deviceID,
			Status:     deviceUserStatusBlocked,
			StatusText: deviceUserStatusText(deviceUserStatusBlocked),
			Blocked:    true,
			BlockID:    block.ID,
			BlockOnly:  true,
			Removable:  false,
		})
		blocked++
	}
	// map 迭代无序 → 显式排序，让同一份数据两次请求得到同样的顺序（可复现）。
	sortDeviceUserItems(orphans)
	devices = append(devices, orphans...)

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"devices":     devices,
		"total":       len(devices),
		"active":      active,
		"blocked":     blocked,
		"schemaReady": true,
	}})
}

// sortDeviceUserItems 按 device_id 升序排（只用于孤儿行，保证顺序稳定）。
func sortDeviceUserItems(items []deviceUserDeviceItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].DeviceID < items[j-1].DeviceID; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// UserDeviceRemove 移除一台设备：**软撤销**（写 revoked_at + revoked_reason）。
//
//	POST /api/user/devices/:id/remove
//
// 红线 2：绝不 DELETE 行。理由见 137 §3.2 —— 删行后「这台设备什么时候被谁移除」
// 永远查不到，而申诉/封禁核对需要它。
//
// 幂等：已撤销的行再点一次返回 200 + alreadyRevoked=true，不覆盖原时间与原因
// （覆盖会让「10 天闲置」被改写成「用户移除」，历史失真）。
func UserDeviceRemove(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := deviceUserParseID(c)
	if !ok {
		return
	}
	db := deviceUserDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法移除设备"})
		return
	}
	row, err := deviceUserFindBinding(db, operator.ID, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取设备记录失败"})
		return
	}
	if row == nil {
		// 不存在 / 不属于自己 / 表未就绪 —— 对外一律 404（红线 1）。
		deviceUserNotFound(c)
		return
	}
	state := deviceUserReadColumnState(db)
	if !state.hasRevoked {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "设备表尚未就绪，暂时无法移除设备"})
		return
	}

	if row.RevokedAt != nil {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"id":             row.ID,
			"deviceId":       row.DeviceID,
			"status":         deviceUserStatusRemoved,
			"statusText":     deviceUserStatusText(deviceUserStatusRemoved),
			"revokedAt":      row.RevokedAt,
			"revokedReason":  row.RevokedReason,
			"alreadyRevoked": true,
		}})
		return
	}

	now := time.Now()
	result := db.Table(entity.TableNameAccountDeviceBinding).
		Where("id = ? AND user_id = ?", id, operator.ID).
		Where("revoked_at IS NULL").
		Updates(map[string]any{
			"revoked_at":     now,
			"revoked_reason": deviceUserRevokeReasonUserRemoved,
		})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "移除设备失败"})
		return
	}
	if result.RowsAffected == 0 {
		// 并发：查到时还没撤销，更新时已被另一次请求撤销（或行被删）。
		// 重新读一次给出当前真实状态，而不是报错 —— 用户的意图已经达成了。
		latest, err := deviceUserFindBinding(db, operator.ID, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "移除设备失败"})
			return
		}
		if latest == nil {
			deviceUserNotFound(c)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"id":             latest.ID,
			"deviceId":       latest.DeviceID,
			"status":         deviceUserStatusRemoved,
			"statusText":     deviceUserStatusText(deviceUserStatusRemoved),
			"revokedAt":      latest.RevokedAt,
			"revokedReason":  latest.RevokedReason,
			"alreadyRevoked": true,
		}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"id":             row.ID,
		"deviceId":       row.DeviceID,
		"status":         deviceUserStatusRemoved,
		"statusText":     deviceUserStatusText(deviceUserStatusRemoved),
		"revokedAt":      now,
		"revokedReason":  deviceUserRevokeReasonUserRemoved,
		"alreadyRevoked": false,
	}})
}

// UserDeviceBlock 拉黑一台设备（写 `user_device_blocks` 一行）。
//
//	POST /api/user/devices/:id/block   {"reason":"..."}
//
// 红线 3：**独立表**，不写 revoked_reason。也不撤销绑定行 —— 拉黑与撤销是两件事：
// 撤销=释放槽位（下次可能被静默重绑），拉黑=拒绝使用（任何绑定状态都不放行）。
// 若这里顺手撤销绑定，用户解除拉黑后会发现设备「重新出现但没被重绑」，反而更乱。
//
// 幂等：同一 (user_id, device_id) 有唯一索引；重复拉黑走 OnConflict DoNothing，
// 返回既有行而不是报错（用户连点两次不该看到 500）。
func UserDeviceBlock(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := deviceUserParseID(c)
	if !ok {
		return
	}
	db := deviceUserDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法拉黑设备"})
		return
	}
	row, err := deviceUserFindBinding(db, operator.ID, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取设备记录失败"})
		return
	}
	if row == nil {
		deviceUserNotFound(c)
		return
	}
	if !deviceUserBlocksTableAvailable(db) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "设备拉黑表尚未就绪，暂时无法拉黑设备"})
		return
	}

	var input struct {
		Reason string `json:"reason"`
	}
	// 允许空 body（等价 reason=''）：拉黑是核心动作，不该因为没填原因被拒。
	_ = c.ShouldBindJSON(&input)
	reason := strings.TrimSpace(input.Reason)
	if runes := []rune(reason); len(runes) > 255 {
		reason = string(runes[:255])
	}

	deviceID := service.NormalizeDevicePoolDeviceID(row.DeviceID)
	if deviceID == "" {
		// 空设备指纹无法拉黑（会被当成「全站拉黑」之类）。这属于脏数据。
		c.JSON(http.StatusBadRequest, gin.H{"message": "该设备记录缺少设备标识，无法拉黑"})
		return
	}

	payload := entity.UserDeviceBlock{UserID: operator.ID, DeviceID: deviceID, Reason: reason}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&payload).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "拉黑设备失败"})
		return
	}
	var stored deviceUserBlockRow
	if err := db.Table(entity.TableNameUserDeviceBlock).
		Select("id AS id, device_id AS device_id, reason AS reason, created_at AS created_at").
		Where("user_id = ? AND device_id = ?", operator.ID, deviceID).
		Take(&stored).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "拉黑设备失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"blockId":    stored.ID,
		"deviceId":   deviceID,
		"reason":     stored.Reason,
		"status":     deviceUserStatusBlocked,
		"statusText": deviceUserStatusText(deviceUserStatusBlocked),
	}})
}

// UserDeviceUnblock 解除拉黑：**删行**（不是改状态）。
//
//	DELETE /api/user/devices/blocks/:id
//
// 红线 3 的另一半：拉黑表**没有 status 列**，存在即拉黑、删行即解除。
// 「只把原因清空」或「写个 revoked 标记」都会让判定链（137 §5 第 2 步，
// 查的是行是否存在）继续命中 —— 用户点了「解除」，界面显示已解除，
// 但下次请求仍然 403，且没有任何报错（这正是 D7 要钉的东西）。
func UserDeviceUnblock(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := deviceUserParseID(c)
	if !ok {
		return
	}
	db := deviceUserDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法解除拉黑"})
		return
	}
	if !deviceUserBlocksTableAvailable(db) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "设备拉黑表尚未就绪，暂时无法解除拉黑"})
		return
	}

	var stored deviceUserBlockRow
	err := db.Table(entity.TableNameUserDeviceBlock).
		Select("id AS id, device_id AS device_id, reason AS reason, created_at AS created_at").
		Where("id = ? AND user_id = ?", id, operator.ID).
		Take(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		deviceUserNotFound(c)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取拉黑记录失败"})
		return
	}

	result := db.Table(entity.TableNameUserDeviceBlock).
		Where("id = ? AND user_id = ?", id, operator.ID).
		Delete(&entity.UserDeviceBlock{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "解除拉黑失败"})
		return
	}
	if result.RowsAffected == 0 {
		// 并发：读到之后、删之前被另一次请求删掉。语义上已经达成，仍按 404 处理
		// 更安全（前端刷新即可看到真实状态），但不要报 500。
		deviceUserNotFound(c)
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"blockId":  stored.ID,
		"deviceId": stored.DeviceID,
		"removed":  true,
	}})
}

// deviceUserLimitView 是 GET/PUT /user/device-limit 的响应体。
type deviceUserLimitView struct {
	// Limit/Source 来自 service.EffectiveDeviceLimit（唯一权威计算，137 §4）。
	Limit  int32  `json:"limit"`
	Source string `json:"source"`
	// Unlimited=true 表示「不限」（limit=0 且不是禁止档）。
	Unlimited bool `json:"unlimited"`
	// Override 是 users.device_limit_override 原值（-1 = 不覆盖）。
	Override int32 `json:"override"`
	// LevelLimit 是等级值 L（**不是**生效值），PUT 的上界就是它。
	LevelLimit int32  `json:"levelLimit"`
	LevelID    int32  `json:"levelId"`
	LevelName  string `json:"levelName"`
	// UpperBound 是自设上限能填的最大值；null = 等级本身不限（无上界）。
	//
	// 前端把它当输入框的 max，这是「只能降不能升」在界面层最直接的表达。
	UpperBound *int32 `json:"upperBound"`
	// Editable=false 时页面禁用输入框（表未就绪 / 等级表未就绪）。
	Editable bool `json:"editable"`
	// HookReady=false 表示 EffectiveDeviceLimit 还是本分支的临时桩（见说明）。
	HookReady bool `json:"hookReady"`
}

// deviceUserLimitSourceKnown 判断 source 是否落在 137 §4 定义的四个取值内。
//
// 存在理由：本分支上 service.EffectiveDeviceLimit 是**临时桩**（STUB-followup，
// 返回 "stub"）。把桩的返回值当成真值（"unlimited"）会让前端显示「不限」——
// 一个用户无法察觉的假状态。合并换成 A1 正式实现后本函数恒为 true。
func deviceUserLimitSourceKnown(source string) bool {
	switch source {
	case "user_override", "level", "level_missing", "unlimited":
		return true
	default:
		return false
	}
}

// deviceUserLimitViewFor 组装上限视图（GET 与 PUT 共用，避免两处口径漂移）。
func deviceUserLimitViewFor(db *gorm.DB, userID int32) (deviceUserLimitView, error) {
	view := deviceUserLimitView{Override: deviceUserLimitOverrideNotSet, HookReady: true}
	if db == nil || userID <= 0 {
		return view, nil
	}

	// ① 生效上限：唯一权威计算。它的第 3 条契约是「等级缺失 → (0, level_missing, nil)」，
	//    所以这里拿到 err 只可能是真故障。
	limit, source, err := service.EffectiveDeviceLimit(db, userID)
	if err != nil {
		return view, err
	}
	view.Limit, view.Source = limit, source
	// Unlimited 只对**已知来源**成立。source 落在四个取值之外（本分支上可能是
	// 临时桩的 "stub"）时不冒充「不限」：两者 limit 都是 0，只有 source 能区分，
	// 而把「算不出来」显示成「不限」会让用户以为自己的上限被放宽了。
	view.Unlimited = limit == 0 && (source == "unlimited" || source == "level_missing")
	view.HookReady = deviceUserLimitSourceKnown(source)

	// ② 覆盖原值：列不存在（老库）时按「不覆盖」处理，与 EffectiveDeviceLimit 一致。
	if db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		var row struct {
			Override int32 `gorm:"column:device_limit_override"`
		}
		queryErr := db.Table(entity.TableNameUser).
			Select("device_limit_override AS device_limit_override").
			Where("id = ?", userID).Take(&row).Error
		switch {
		case queryErr == nil:
			view.Override = row.Override
		case errors.Is(queryErr, gorm.ErrRecordNotFound):
			view.Override = deviceUserLimitOverrideNotSet
		default:
			return view, queryErr
		}
	}

	// ③ 等级值 L：用既有摘要入口读，**不重算**（user_level.go 是单一实现）。
	summary, err := service.GetUserLevelSummaryWithDB(db, userID)
	if err != nil {
		return view, err
	}
	view.LevelID = summary.LevelID
	view.LevelName = summary.Name
	view.LevelLimit = summary.DeviceLimit
	view.Editable = true
	if summary.LevelID > 0 && summary.DeviceLimit > 0 {
		upper := summary.DeviceLimit
		view.UpperBound = &upper
	}
	return view, nil
}

// UserDeviceLimitGet 返回当前生效上限与来源（137 §8.1）。
//
//	GET /api/user/device-limit
func UserDeviceLimitGet(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	db := deviceUserDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法读取设备上限"})
		return
	}
	view, err := deviceUserLimitViewFor(db, operator.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取设备上限失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": view})
}

// UserDeviceLimitPut 用户自设上限（**只能降不能升**，137 §4）。
//
//	PUT /api/user/device-limit   {"limit":2}
//
// 校验口径（与 137 §4 逐字对应）：
//
//	L == 0（等级不限）→ 允许 N >= 0
//	L >  0（等级限 L）→ 必须 0 <= N <= L，否则 400「不能超过当前等级上限 L 台」
//
// 上界必须用**等级值 L**（不是生效值）：生效值可能已经是本用户自己的 override，
// 拿它当上界会让「先设 1 台就再也改不回 3 台」—— 一个永久性的自我降级锁。
//
// 拒绝时返回 400 而不是 403：这是「你填的数超了」的可修正输入错误，
// 403 会让前端当成权限问题、不给用户改的机会。
func UserDeviceLimitPut(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	db := deviceUserDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未就绪，暂时无法修改设备上限"})
		return
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "设备上限功能尚未就绪（数据表待迁移）"})
		return
	}

	var input struct {
		// 指针：区分「没传」与「传了 0」。0 是合法值（等级不限时 = 不限），
		// 用 int 会把「没传」静默当成「设成不限」，那是把用户的上限放大。
		Limit *int32 `json:"limit"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Limit == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请提供要设置的设备上限"})
		return
	}
	next := *input.Limit

	summary, err := service.GetUserLevelSummaryWithDB(db, operator.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取等级上限失败"})
		return
	}
	levelID, levelLimit := summary.LevelID, summary.DeviceLimit

	if next < 0 {
		// 负数一律拒绝。特别地**不接受 -1 作为「恢复默认」**：
		// -1 在库里表示「不覆盖」，若放开，一个等级限 3 台的用户设过 1 之后
		// 就能用 -1 回到 3 台 —— 那正是「只能降不能升」要禁止的事。
		c.JSON(http.StatusBadRequest, gin.H{"message": "设备上限不能为负数"})
		return
	}
	if levelID > 0 && levelLimit > 0 && next > levelLimit {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "不能超过当前等级上限 " + strconv.FormatInt(int64(levelLimit), 10) + " 台",
			"limit":   levelLimit,
		})
		return
	}

	if err := db.Table(entity.TableNameUser).
		Where("id = ?", operator.ID).
		Update("device_limit_override", next).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存设备上限失败"})
		return
	}

	view, err := deviceUserLimitViewFor(db, operator.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存成功但读取新上限失败，请刷新页面"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": view})
}
