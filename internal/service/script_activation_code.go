package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 管理员预生成一次性脚本激活码的服务层。
//
// 与既有的 ScriptActivationEntitlement 并存、不替换：
//   - Entitlement 走「用户自购 → HMAC 派生码 → 兑换绑定脚本 → 开 run session」，
//     码的明文永不落库；
//   - 本文件管理的是**管理员预生成**的那批码（明文可查，供客服核对）。
//
// 兑换时二者交汇：管理员码被兑换后，会把结果记进本表（UsedAt/UsedBy/
// UsedEntitlementID），客服因此能从「用户报上来的码」一路追到实际会话。

const (
	// AdminScriptActivationCodePrefix 让码在视觉上可辨识，也便于格式校验。
	AdminScriptActivationCodePrefix = "TKA-"
	// AdminScriptActivationCodeBodyHex 是随机体长度（16 字节 = 32 hex 字符）。
	AdminScriptActivationCodeBodyHex = 32
	// MaxAdminScriptActivationCodeBatch 是单次批量生成上限。
	//
	// 存在的理由不是性能而是**防误操作**：管理员一次手滑输错位数就可能生成
	// 十万个码，清理起来既要逐条停用又会污染审计。100 够一次活动用，
	// 再多就该分批（并有意识地承担后果）。
	MaxAdminScriptActivationCodeBatch = 100
)

var (
	ErrAdminScriptActivationCodeNotFound   = errors.New("激活码不存在")
	ErrAdminScriptActivationCodeInvalid    = errors.New("激活码格式不正确")
	ErrAdminScriptActivationCodeUsed       = errors.New("激活码已被使用")
	ErrAdminScriptActivationCodeDisabled   = errors.New("激活码已被禁用")
	ErrAdminScriptActivationCodeExpired    = errors.New("激活码已过期")
	ErrAdminScriptActivationCodeBatchRange = fmt.Errorf("单次批量生成数量必须在 1 到 %d 之间", MaxAdminScriptActivationCodeBatch)
)

// AdminScriptActivationCodeUsageRecord 是一条使用记录。
//
// 领取、绑定和运行会话分别使用各自保存的时间。
type AdminScriptActivationCodeUsageRecord struct {
	// Kind 取值：redeem（领取）/ bind（绑定脚本）/ run（脚本实际运行）。
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	UserID     int32     `json:"userId"`
	Username   string    `json:"username"`
	ScriptID   *int32    `json:"scriptId,omitempty"`
	ScriptName string    `json:"scriptName,omitempty"`
	// Detail 是可读补充（例如 run session 的到期时刻）。
	Detail string `json:"detail,omitempty"`
}

// AdminScriptActivationCodeDetail 是详情响应：码本体 + 全部使用记录。
type AdminScriptActivationCodeDetail struct {
	Code   entity.AdminScriptActivationCode       `json:"code"`
	Usages []AdminScriptActivationCodeUsageRecord `json:"usages"`
	// HasUsage 显式给出「有没有被用过」，避免前端靠 len(usages)==0 推断
	// （usages 可能因为关联行被清理而变空，但码本身确实用过）。
	HasUsage bool `json:"hasUsage"`
}

// GenerateAdminScriptActivationCodes 批量生成激活码。
//
// 原子性说明：整批在一个事务里插入，任一失败整批回滚 —— 部分成功会让管理员
// 不知道哪些码真的存在（而码是凭据，漏发/重发都是事故）。
func GenerateAdminScriptActivationCodes(db *gorm.DB, operatorID int32, count int, remark string, expiresAt *time.Time) ([]entity.AdminScriptActivationCode, error) {
	if db == nil || operatorID <= 0 {
		return nil, ErrAdminScriptActivationCodeInvalid
	}
	if count < 1 || count > MaxAdminScriptActivationCodeBatch {
		return nil, ErrAdminScriptActivationCodeBatchRange
	}
	remark = strings.TrimSpace(remark)
	if len(remark) > 255 {
		remark = remark[:255]
	}

	rows := make([]entity.AdminScriptActivationCode, 0, count)
	for index := 0; index < count; index++ {
		code, err := newAdminScriptActivationCode()
		if err != nil {
			return nil, err
		}
		rows = append(rows, entity.AdminScriptActivationCode{
			Code:      code,
			Status:    entity.ScriptActivationCodeStatusUnused,
			Remark:    remark,
			CreatedBy: operatorID,
			ExpiresAt: expiresAt,
		})
	}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		return tx.Create(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// newAdminScriptActivationCode 生成 TKA-<32 hex>。
//
// 用 crypto/rand 而不是时间戳/自增：激活码是可直接兑换的凭据，可预测的码
// 等于公开的码。
func newAdminScriptActivationCode() (string, error) {
	buffer := make([]byte, AdminScriptActivationCodeBodyHex/2)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return AdminScriptActivationCodePrefix + hex.EncodeToString(buffer), nil
}

// ProjectAdminScriptActivationCodeStatus 根据冻结期限和码的有效期计算对外状态。
func ProjectAdminScriptActivationCodeStatus(code entity.AdminScriptActivationCode, now time.Time) string {
	if code.Status == entity.ScriptActivationCodeStatusUsed {
		if !adminScriptActivationIsReservation(code) {
			return entity.ScriptActivationCodeStatusUsed
		}
		if code.UsedAt.Add(ScriptActivationCodeFreezeDuration).After(now) {
			return ScriptActivationCodeStatusFrozen
		}
	}
	if code.Status == entity.ScriptActivationCodeStatusDisabled {
		return entity.ScriptActivationCodeStatusDisabled
	}
	if code.ExpiresAt != nil && !code.ExpiresAt.After(now) {
		return entity.ScriptActivationCodeStatusExpired
	}
	return entity.ScriptActivationCodeStatusUnused
}

func adminScriptActivationIsReservation(code entity.AdminScriptActivationCode) bool {
	return code.Status == entity.ScriptActivationCodeStatusUsed && code.UsedAt != nil && code.UsedEntitlementID != nil && code.UsedScriptID == nil
}

func projectAdminScriptActivationCode(code entity.AdminScriptActivationCode, now time.Time) entity.AdminScriptActivationCode {
	if adminScriptActivationIsReservation(code) && !code.UsedAt.Add(ScriptActivationCodeFreezeDuration).After(now) {
		code.Status = entity.ScriptActivationCodeStatusUnused
		code.UsedAt, code.UsedBy, code.UsedEntitlementID = nil, nil, nil
	}
	return code
}

// AdminScriptActivationCodeListQuery 是列表筛选条件。
type AdminScriptActivationCodeListQuery struct {
	Status   string
	UsedBy   int32
	Page     int
	PageSize int
}

// AdminScriptActivationCodeListResult 是分页结果。
type AdminScriptActivationCodeListResult struct {
	Items    []entity.AdminScriptActivationCode `json:"items"`
	Total    int64                              `json:"total"`
	Page     int                                `json:"page"`
	PageSize int                                `json:"pageSize"`
}

// ListAdminScriptActivationCodes 分页列出激活码。
//
// 状态筛选含 expired 时不能直接 WHERE status='expired'（库里不存该值），
// 必须翻译成「未使用、未禁用且已过期」的谓词 —— 否则筛「已过期」永远空。
func ListAdminScriptActivationCodes(db *gorm.DB, query AdminScriptActivationCodeListQuery) (AdminScriptActivationCodeListResult, error) {
	if db == nil {
		return AdminScriptActivationCodeListResult{}, nil
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 200 {
		query.PageSize = 20
	}

	now := db.NowFunc().UTC()
	freezeCutoff := now.Add(-ScriptActivationCodeFreezeDuration)
	const reservation = "(used_script_id IS NULL AND used_entitlement_id IS NOT NULL AND used_at IS NOT NULL)"
	const elapsedReservation = "(status = ? AND " + reservation + " AND used_at <= ?)"
	unclaimed := func(tx *gorm.DB) *gorm.DB {
		return tx.Where("(status = ? OR "+elapsedReservation+")", entity.ScriptActivationCodeStatusUnused, entity.ScriptActivationCodeStatusUsed, freezeCutoff)
	}
	// 用两个**独立的**查询构造器，而不是复用同一个 scope：
	// `Session(&gorm.Session{NewDB:true})` 会清掉 Statement 上的 Model，
	// 复用它去 Count 会报 "Table not set"（实测）。
	build := func() *gorm.DB {
		tx := db.Session(&gorm.Session{NewDB: true}).Model(&entity.AdminScriptActivationCode{})
		if query.UsedBy > 0 {
			tx = tx.Where("used_by = ? AND NOT "+elapsedReservation, query.UsedBy, entity.ScriptActivationCodeStatusUsed, freezeCutoff)
		}
		switch strings.TrimSpace(query.Status) {
		case entity.ScriptActivationCodeStatusExpired:
			return unclaimed(tx).Where("expires_at IS NOT NULL AND expires_at <= ?", now)
		case entity.ScriptActivationCodeStatusUnused:
			return unclaimed(tx).Where("(expires_at IS NULL OR expires_at > ?)", now)
		case ScriptActivationCodeStatusFrozen:
			return tx.Where("status = ? AND "+reservation+" AND used_at > ?", entity.ScriptActivationCodeStatusUsed, freezeCutoff)
		case entity.ScriptActivationCodeStatusUsed:
			return tx.Where("status = ? AND NOT "+reservation, entity.ScriptActivationCodeStatusUsed)
		case "":
			return tx
		default:
			return tx.Where("status = ?", query.Status)
		}
	}

	var total int64
	if err := build().Count(&total).Error; err != nil {
		return AdminScriptActivationCodeListResult{}, err
	}

	items := make([]entity.AdminScriptActivationCode, 0, query.PageSize)
	err := build().
		Order("id DESC").
		Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Find(&items).Error
	if err != nil {
		return AdminScriptActivationCodeListResult{}, err
	}
	for i := range items {
		items[i] = projectAdminScriptActivationCode(items[i], now)
	}
	return AdminScriptActivationCodeListResult{
		Items: items, Total: total, Page: query.Page, PageSize: query.PageSize,
	}, nil
}

// GetAdminScriptActivationCodeByID 读单个码。
func GetAdminScriptActivationCodeByID(db *gorm.DB, id int64) (entity.AdminScriptActivationCode, error) {
	if db == nil || id <= 0 {
		return entity.AdminScriptActivationCode{}, ErrAdminScriptActivationCodeNotFound
	}
	var code entity.AdminScriptActivationCode
	err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).First(&code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.AdminScriptActivationCode{}, ErrAdminScriptActivationCodeNotFound
	}
	return projectAdminScriptActivationCode(code, db.NowFunc().UTC()), err
}

// AdminScriptActivationCodeUpdate 是修改入参（指针语义：nil = 不修改）。
type AdminScriptActivationCodeUpdate struct {
	Remark    *string
	Status    *string
	ExpiresAt *time.Time
	// ClearExpiry 显式表达「改成永不过期」，因为 ExpiresAt=nil 同时是
	// 「不修改」的语义，两者必须能区分。
	ClearExpiry bool
}

// UpdateAdminScriptActivationCode 修改有效期/备注/启用状态。
//
// 冻结中的码和已永久使用的码保留原状态。
func UpdateAdminScriptActivationCode(db *gorm.DB, id int64, update AdminScriptActivationCodeUpdate) (entity.AdminScriptActivationCode, error) {
	if db == nil || id <= 0 {
		return entity.AdminScriptActivationCode{}, ErrAdminScriptActivationCodeNotFound
	}

	fields := map[string]any{}
	if update.Remark != nil {
		remark := strings.TrimSpace(*update.Remark)
		if len(remark) > 255 {
			remark = remark[:255]
		}
		fields["remark"] = remark
	}
	if update.Status != nil {
		next := strings.TrimSpace(*update.Status)
		if next != entity.ScriptActivationCodeStatusUnused && next != entity.ScriptActivationCodeStatusDisabled {
			return entity.AdminScriptActivationCode{}, ErrAdminScriptActivationCodeInvalid
		}
		fields["status"] = next
	}
	if update.ClearExpiry {
		fields["expires_at"] = nil
	} else if update.ExpiresAt != nil {
		fields["expires_at"] = *update.ExpiresAt
	}
	var current entity.AdminScriptActivationCode
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAdminScriptActivationCodeNotFound
		}
		if err != nil {
			return err
		}
		now := tx.NowFunc().UTC()
		status := ProjectAdminScriptActivationCodeStatus(current, now)
		if update.Status != nil && (status == entity.ScriptActivationCodeStatusUsed || status == ScriptActivationCodeStatusFrozen) {
			return ErrAdminScriptActivationCodeUsed
		}
		if update.Status != nil && adminScriptActivationIsReservation(current) {
			fields["used_at"], fields["used_by"], fields["used_entitlement_id"] = nil, nil, nil
		}
		if len(fields) == 0 {
			return nil
		}
		if err := tx.Model(&current).Updates(fields).Error; err != nil {
			return err
		}
		return tx.First(&current, id).Error
	})
	if err != nil {
		return entity.AdminScriptActivationCode{}, err
	}
	return projectAdminScriptActivationCode(current, db.NowFunc().UTC()), nil
}

// ConsumeAdminScriptActivationCode 原子兑换一个管理员预生成的激活码。
//
// **这是本任务的核心不变量**：并发兑换同一个码只能有一个成功。
//
// 做法是条件更新，不是先查后写：
//
//	UPDATE ... SET used_at=?, used_by=? WHERE code=? AND used_at IS NULL AND status='unused'
//
// 只有一个事务能让 RowsAffected==1；其余拿到 0，据此区分「已被用/过期/禁用」。
// 先 `SELECT ... WHERE used_at IS NULL` 再 `UPDATE` 会让两个并发请求都读到
// NULL 然后双双写入 —— 那就是「一次性」被打破的经典形态。
//
// 过期与禁用判定放在 UPDATE 之后（而不是之前）：先做写入竞争，再解释失败原因，
// 这样「竞争失败」与「本来就不能用」不会混为一谈。
func ConsumeAdminScriptActivationCode(db *gorm.DB, code string, userID int32, scriptID *int32, entitlementID *int64) (entity.AdminScriptActivationCode, error) {
	code = strings.TrimSpace(code)
	if db == nil || userID <= 0 || code == "" {
		return entity.AdminScriptActivationCode{}, ErrAdminScriptActivationCodeInvalid
	}
	now := time.Now().UTC().Truncate(time.Second)

	var consumed entity.AdminScriptActivationCode
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		// 条件更新：唯一赢家判定点。
		update := tx.Model(&entity.AdminScriptActivationCode{}).
			Where("code = ? AND used_at IS NULL AND status = ?",
				code, entity.ScriptActivationCodeStatusUnused).
			Where("expires_at IS NULL OR expires_at > ?", now).
			Updates(map[string]any{
				"used_at":             now,
				"used_by":             userID,
				"used_script_id":      scriptID,
				"used_entitlement_id": entitlementID,
				"status":              entity.ScriptActivationCodeStatusUsed,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			// 输家：读一次行来解释失败的**具体**原因。这里读到的状态只用于
			// 报错文案，不参与任何判定 —— 判定已经由上面的 WHERE 做完了。
			var existing entity.AdminScriptActivationCode
			readErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("code = ?", code).First(&existing).Error
			if errors.Is(readErr, gorm.ErrRecordNotFound) {
				return ErrAdminScriptActivationCodeInvalid
			}
			if readErr != nil {
				return readErr
			}
			switch {
			case existing.UsedAt != nil:
				return ErrAdminScriptActivationCodeUsed
			case existing.Status == entity.ScriptActivationCodeStatusDisabled:
				return ErrAdminScriptActivationCodeDisabled
			case existing.ExpiresAt != nil && !existing.ExpiresAt.After(now):
				return ErrAdminScriptActivationCodeExpired
			default:
				return ErrAdminScriptActivationCodeUsed
			}
		}
		return tx.Where("code = ?", code).First(&consumed).Error
	})
	if err != nil {
		return entity.AdminScriptActivationCode{}, err
	}
	return consumed, nil
}

// ListAdminScriptActivationCodeUsages 返回该码的使用记录。
//
// 领取记录来自码本身，绑定和运行记录来自关联的资格与会话。
func ListAdminScriptActivationCodeUsages(db *gorm.DB, code entity.AdminScriptActivationCode) ([]AdminScriptActivationCodeUsageRecord, error) {
	if db == nil {
		return nil, nil
	}
	usages := make([]AdminScriptActivationCodeUsageRecord, 0, 3)
	code = projectAdminScriptActivationCode(code, db.NowFunc().UTC())
	if code.UsedAt == nil {
		return usages, nil
	}

	userID := int32(0)
	if code.UsedBy != nil {
		userID = *code.UsedBy
	}
	username := ""
	if userID > 0 {
		var user entity.User
		if err := db.Session(&gorm.Session{NewDB: true}).
			Select("id", "username").Where("id = ?", userID).First(&user).Error; err == nil {
			username = user.Username
		}
	}

	scriptName := ""
	if code.UsedScriptID != nil {
		var script entity.UserScript
		if err := db.Session(&gorm.Session{NewDB: true}).
			Select("id", "name").Where("id = ?", *code.UsedScriptID).First(&script).Error; err == nil {
			scriptName = script.Name
		}
	}

	// ① 兑换事件。
	usages = append(usages, AdminScriptActivationCodeUsageRecord{
		Kind:       "redeem",
		At:         *code.UsedAt,
		UserID:     userID,
		Username:   username,
		ScriptID:   code.UsedScriptID,
		ScriptName: scriptName,
		Detail:     "领取激活码",
	})

	// ② 实际运行事件（由 entitlement 串到 run session）。
	if code.UsedEntitlementID != nil {
		var entitlement entity.ScriptActivationEntitlement
		err := db.Session(&gorm.Session{NewDB: true}).
			Where("id = ?", *code.UsedEntitlementID).First(&entitlement).Error
		if err == nil && entitlement.ScriptBoundAt != nil && entitlement.ScriptID != nil {
			usages = append(usages, AdminScriptActivationCodeUsageRecord{
				Kind:       "bind",
				At:         *entitlement.ScriptBoundAt,
				UserID:     userID,
				Username:   username,
				ScriptID:   entitlement.ScriptID,
				ScriptName: scriptName,
				Detail:     "绑定脚本",
			})
		}
		if err == nil && entitlement.RunSessionID != nil {
			var session entity.ScriptRunSession
			sessionErr := db.Session(&gorm.Session{NewDB: true}).
				Where("id = ?", *entitlement.RunSessionID).First(&session).Error
			if sessionErr == nil {
				sessionScriptID := session.ScriptID
				usages = append(usages, AdminScriptActivationCodeUsageRecord{
					Kind:       "run",
					At:         session.ActivatedAt,
					UserID:     session.UserID,
					Username:   username,
					ScriptID:   &sessionScriptID,
					ScriptName: scriptName,
					Detail:     fmt.Sprintf("脚本运行会话 #%d，到期 %s", session.ID, session.ExpiresAt.Format(time.RFC3339)),
				})
			}
		}
	}
	return usages, nil
}
