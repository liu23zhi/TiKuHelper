package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件实现「管理员赠送」与「追缴式回滚」（109-*.md §1 / 117-*.md §1 / 118-*.md §1§5）。
//
// # 与既有资金核心的关系（**只调用，不重写**）
//
//	入账   CreditGiftBuckets   —— 规则 3：先抵欠款、剩余进赠金桶（wallet_bucket.go:69）
//	扣减   DeductGiftBucket    —— 赠金桶 clamp 到 0，返回「实扣 / 扣后余额」（wallet_bucket.go:117）
//	取整   util.RoundMoney     —— 6 位小数口径
//	并发闸 ClaimIdempotencyWithDB / CompleteIdempotencyWithDB（idempotency.go）
//	审计   createBalanceAuditLog（user_balance.go，同包复用）
//
// 这四条是现有资金核心的**唯一正确实现**，本文件不得复制其算式。一旦这里出现第二份
// 「先抵欠款」或「赠金 clamp」的算法，两处就会各自漂移，而资损只会在生产上暴露。
//
// # 三条不可协商的口径
//
//  1. **赠送入账必须先抵欠款再进赠金桶**：balance=-1 获赠 2 → balance=0 / gift=1
//     （43-*.md 算例 B）。直接 gift += amount 会让欠款用户「赠金越攒越多」。
//  2. **gift_balance 下限 0 永不为负；balance 不 clamp 可为负**（欠款载体）。
//  3. **追缴式回滚不设上限**（118-*.md §2）：赠金扣到 0 后，**差额必须继续从本金扣**，
//     本金可以变成负数。静默丢弃差额 = 对账时凭空少一笔（117-*.md §1.2）。
//
// # 幂等
//
// 持久锚点是 `billing_records.request_id` 本身，**不是** `idempotency_records`：
// 后者有 24h 保留期（idempotencyRetention），过期后同一动作会被当成新请求重放 ——
// 回滚重放等于对同一个用户追缴两次。`idempotency_records` 在这里只作**并发闸**
// （唯一索引挡住同一瞬间两个事务），与 registration_clawback.go 同构。

const (
	// AdminGrantBillingRecordType 是「管理员赠送」的账单类型（109-*.md §1 Q3e-1）。
	//
	// 与 balance_adjustment 的语义分界：那个是**技术性调账**（可正可负、用于修复数据），
	// 这个是**业务性赠送**（只能为正、默认进赠金桶、出现在「赠送记录」里给用户看）。
	AdminGrantBillingRecordType = "admin_grant"

	// AdminGrantReversalBillingRecordType 是「追缴式回滚」的账单类型（118-*.md §5.2）。
	//
	// 回滚**不删除、不改小**原 admin_grant 行：它是一条独立的反向流水（direction=expense）。
	// 账本因此可以完整对账，也不会因改历史数据产生歧义（⑤ 关键约束）。
	AdminGrantReversalBillingRecordType = "admin_grant_reversal"

	// AdminGrantConfirmThresholdSettingKey 是单笔二次确认阈值（109-*.md Q3e-b / 112 N3-c）。
	//
	// 默认 10 元：单笔金额 **严格大于** 阈值才要求二次确认（恰好等于不触发，112 §3.7 例 1）。
	AdminGrantConfirmThresholdSettingKey = "admin_grant_confirm_threshold"

	// defaultAdminGrantConfirmThreshold 是阈值缺省值（元）。
	defaultAdminGrantConfirmThreshold = 10.0

	// adminGrantAnchorKindBatch 是批量赠送的 anchor 前缀（118-*.md 的 batchId 形态）。
	adminGrantAnchorKindBatch = "batch-"
	// adminGrantAnchorKindSingle 是单次赠送的 anchor 前缀。
	adminGrantAnchorKindSingle = "single-"

	// adminGrantUserStatusBanned 是「已封禁」账号的状态值（controller 侧同值，见 controller/closure.go）。
	//
	// service 包里没有 normalizeUserStatus（它在 controller 包），因此这里只做大小写无关比较。
	adminGrantUserStatusBanned = "banned"
)

// 本文件的三个哨兵错误。
var (
	// ErrInvalidAdminGrant 表示赠送/回滚参数不合法（金额、锚点、用户）。
	ErrInvalidAdminGrant = errors.New("赠送参数无效")
	// ErrAdminGrantConfirmRequired 表示金额超过阈值但调用方没有确认（⑨ 关键约束）。
	ErrAdminGrantConfirmRequired = errors.New("本次赠送金额超过二次确认阈值，需要确认后重试")
	// ErrAdminGrantUserUnavailable 表示目标用户不存在 / 已归档 / 已封禁。
	ErrAdminGrantUserUnavailable = errors.New("该用户不存在、已归档或已封禁，不能赠送或追缴")
)

// AdminGrantRequest 是一次「管理员赠送」的入参。
//
// 赠送的是**赠金**（gift_balance），不是可退本金：否则管理员一送钱，用户就能立刻
// 充值-退款套现（41-*.md 的「赠送不可退」口径）。
type AdminGrantRequest struct {
	// Operator 是执行赠送的管理员，必须 isAdminUser 通过。
	Operator entity.User
	UserID   int32
	Amount   float64
	// Reason 可空（109-*.md §1：「也可以不填写原因」）。
	Reason string
	// Anchor 是幂等锚点里的业务令牌，形如 `batch-7f3a` / `single-<uuid>`。
	//
	// 空值时按单次赠送生成一个随机 anchor —— 因此「同一请求重放」必须由调用方**传入相同
	// anchor**，否则会被当成两笔新赠送。批量执行时 anchor 就是 batchId。
	Anchor string
	// Confirmed 由调用方在用户完成二次确认后置 true。
	Confirmed bool
	ClientIP  string
}

// AdminGrantResult 是一次赠送的实际结果。
type AdminGrantResult struct {
	UserID int32
	// Amount 是本次应赠金额（幂等命中时为历史上那笔的金额）。
	Amount float64
	// DebtOffset 是本次用于抵扣欠款的部分；Amount-DebtOffset 才是真正进赠金桶的钱。
	DebtOffset    float64
	BalanceBefore float64
	BalanceAfter  float64
	GiftBefore    float64
	GiftAfter     float64
	// BillingRecordID 是本次落账的流水 ID（幂等命中时为已存在那行的 ID）。
	BillingRecordID int32
	RequestID       string
	// AlreadyGranted 表示该 anchor 已赠送过，本次**没有扣/加任何钱**。
	AlreadyGranted bool
}

// AdminGrantReverseRequest 是一次「追缴式回滚」的入参。
type AdminGrantReverseRequest struct {
	Operator entity.User
	UserID   int32
	// Amount 是**应追缴额**（原本赠送出去多少）。
	Amount float64
	// Anchor 与赠送时的 anchor 对齐，用于两笔流水的幂等键（118-*.md §5.3）。
	Anchor string
	// Reason 写进流水备注（可空）。
	Reason   string
	ClientIP string
}

// AdminGrantReverseResult 是一次回滚的实际结果。
//
// Expected − FromGift 就是「赠金桶扣不动、被迫从本金扣」的差额（追缴式回滚的核心）。
type AdminGrantReverseResult struct {
	UserID int32
	// Expected 是应追缴额。
	Expected float64
	// FromGift 是赠金桶**实际**扣减额（受「下限 0」限制，可能小于 Expected）。
	FromGift float64
	// FromBalance 是本金桶实际扣减额（不 clamp，可以让 balance 变负）。
	FromBalance float64
	// Shortfall = Expected − FromGift（= FromBalance），差额明细同时写进 remark。
	Shortfall     float64
	BalanceBefore float64
	BalanceAfter  float64
	GiftBefore    float64
	GiftAfter     float64
	// GiftRecordID / PrincipalRecordID 为 0 表示**这一笔金额为 0、按约定没有写流水**（118 §5.4）。
	GiftRecordID      int32
	PrincipalRecordID int32
	// Skipped 表示两笔流水都已存在（同一 anchor 重放），本次没有任何资金动作。
	Skipped bool
}

// AdminGrantConfirmThresholdWithDB 读取单笔二次确认阈值（元，默认 10）。
//
// 缺表 / 缺记录 / 值非法一律回落默认值：配置坏了绝不能变成「不确认就送钱」。
func AdminGrantConfirmThresholdWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return defaultAdminGrantConfirmThreshold
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, AdminGrantConfirmThresholdSettingKey, ""))
	if raw == "" {
		return defaultAdminGrantConfirmThreshold
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return defaultAdminGrantConfirmThreshold
	}
	return util.RoundMoney(value)
}

// SetAdminGrantConfirmThresholdWithDB 写入单笔二次确认阈值（管理员系统设置用）。
func SetAdminGrantConfirmThresholdWithDB(db *gorm.DB, threshold float64) error {
	if threshold < 0 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return fmt.Errorf("%w：确认阈值必须是非负数", ErrInvalidAdminGrant)
	}
	return saveSystemSettingValue(db, AdminGrantConfirmThresholdSettingKey,
		balanceMoneyText(util.RoundMoney(threshold)), "管理员单笔赠送超过该金额时要求二次确认")
}

// AdminGrantConfirmRequired 判断一次赠送是否需要二次确认（⑨ 关键约束）。
//
// 口径是**严格大于**阈值：恰好等于阈值不触发（112 §3.7 例 1：单人 10 元、阈值 10 元 → 不触发）。
func AdminGrantConfirmRequired(db *gorm.DB, amount float64) bool {
	threshold := AdminGrantConfirmThresholdWithDB(db)
	return util.RoundMoney(amount) > threshold
}

// BuildAdminGrantAnchor 生成幂等锚点令牌。
//
// 批量赠送传 batchId，单次赠送传空（自动生成 single-<uuid>）。
func BuildAdminGrantAnchor(batchID string) string {
	if trimmed := strings.TrimSpace(batchID); trimmed != "" {
		return adminGrantAnchorKindBatch + trimmed
	}
	return adminGrantAnchorKindSingle + uuid.NewString()
}

// AdminGrantRequestID 是赠送流水的 request_id，同时是**持久幂等锚点**。
//
// 形态：`admin_grant_<userID>_<anchor>`。回滚按 `_<anchor>` 后缀反查该批次的全部赠送行，
// 因此 anchor 里**不允许出现下划线**（batch-xxx / single-xxx 都只有短横线）。
func AdminGrantRequestID(userID int32, anchor string) string {
	return fmt.Sprintf("%s_%d_%s", AdminGrantBillingRecordType, userID, anchor)
}

// AdminGrantReversalGiftRequestID 是回滚「赠金部分」流水的 request_id。
func AdminGrantReversalGiftRequestID(userID int32, anchor string) string {
	return fmt.Sprintf("%s_gift_%d_%s", AdminGrantReversalBillingRecordType, userID, anchor)
}

// AdminGrantReversalPrincipalRequestID 是回滚「本金部分」流水的 request_id。
//
// ⚠️ 必须与赠金那笔**分开**（118-*.md §5.3）：共用的话，「赠金笔已存在」会让本金笔
// 一起被跳过，而上一次可能恰好在本金步之前中断 —— 那部分钱就永久追不回来了。
func AdminGrantReversalPrincipalRequestID(userID int32, anchor string) string {
	return fmt.Sprintf("%s_principal_%d_%s", AdminGrantReversalBillingRecordType, userID, anchor)
}

// normalizeAdminGrantAmount 校验并取整赠送/追缴金额。
func normalizeAdminGrantAmount(amount float64) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 ||
		amount >= 1e12 || util.HasMoreDecimalsThan(amount, util.MoneyPrecision) {
		return 0, fmt.Errorf("%w：金额必须是 0 以上、最多 %d 位小数的有效数值", ErrInvalidAdminGrant, util.MoneyPrecision)
	}
	return util.RoundMoney(amount), nil
}

// normalizeAdminGrantAnchor 校验幂等锚点：只允许字母、数字、短横线。
//
// 为什么用白名单而不是「只禁下划线」：anchor 会进 SQL 的 LIKE 模式
// （loadBatchGrantRecordsWithDB 用 `%_batch-<id>` 反查批次），因此
// **`%` 与 `\` 也必须挡住** —— 只转义 `_` 而放过 `%`，一个 batchId 为 `a%b` 的批次
// 会匹配到 `aXb` 的流水，批次边界同样失真。
//
// 白名单同时排除了下划线（request_id 用 `_` 分隔字段，见 AdminGrantRequestID）。
func normalizeAdminGrantAnchor(anchor string) (string, error) {
	trimmed := strings.TrimSpace(anchor)
	if trimmed == "" {
		return BuildAdminGrantAnchor(""), nil
	}
	if len(trimmed) > 48 {
		return "", fmt.Errorf("%w：幂等锚点过长", ErrInvalidAdminGrant)
	}
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9', char == '-':
		default:
			return "", fmt.Errorf("%w：幂等锚点只能包含字母、数字与短横线（不能含下划线、百分号或反斜杠）", ErrInvalidAdminGrant)
		}
	}
	return trimmed, nil
}

// AdminGrant 执行一次管理员赠送（单用户）。
//
// 事务边界：加锁读用户 → 幂等判定 → 双桶入账 → 写流水 → 写审计日志，全部在同一事务，
// 任何一步失败都不留下半截状态。
func AdminGrant(db *gorm.DB, req AdminGrantRequest) (AdminGrantResult, error) {
	var result AdminGrantResult
	if db == nil {
		return result, fmt.Errorf("%w：数据库不可用", ErrInvalidAdminGrant)
	}
	if !isAdminUser(req.Operator) {
		return result, ErrBalanceAdjustmentForbidden
	}
	if req.UserID <= 0 {
		return result, fmt.Errorf("%w：用户参数错误", ErrInvalidAdminGrant)
	}
	amount, err := normalizeAdminGrantAmount(req.Amount)
	if err != nil {
		return result, err
	}
	anchor, err := normalizeAdminGrantAnchor(req.Anchor)
	if err != nil {
		return result, err
	}
	// ⑨ 二次确认在服务层兜底：不能只靠 controller 校验，否则任何新入口都能绕过。
	if !req.Confirmed && AdminGrantConfirmRequired(db, amount) {
		return result, ErrAdminGrantConfirmRequired
	}
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		return adminGrantInTx(tx, req, anchor, amount, &result)
	})
	return result, err
}

// adminGrantInTx 是赠送的唯一实现（事务内）。
func adminGrantInTx(tx *gorm.DB, req AdminGrantRequest, anchor string, amount float64, result *AdminGrantResult) error {
	tx = tx.Session(&gorm.Session{NewDB: true})
	requestID := AdminGrantRequestID(req.UserID, anchor)
	result.RequestID = requestID

	// ① 持久幂等：该 anchor 已赠送过 → 直接返回历史值，不动任何余额。
	if existing, found, err := loadAdminGrantRecord(tx, requestID); err != nil {
		return err
	} else if found {
		result.AlreadyGranted = true
		result.UserID = existing.UserID
		result.Amount = util.RoundMoney(existing.Amount)
		result.BalanceBefore = util.RoundMoney(existing.BalanceBefore)
		result.BalanceAfter = util.RoundMoney(existing.BalanceAfter)
		result.BillingRecordID = existing.ID
		if giftBefore, giftAfter, ok := parseGiftSnapshot(existing.UnitPriceSnapshot); ok {
			result.GiftBefore, result.GiftAfter = giftBefore, giftAfter
			result.DebtOffset = util.RoundMoney(result.BalanceAfter - result.BalanceBefore)
		}
		return nil
	}

	var user entity.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, req.UserID).Error; err != nil {
		return err
	}
	if err := ensureAdminGrantUserAvailable(user); err != nil {
		return err
	}

	// ② 并发闸：同一瞬间两个相同 anchor 的请求，唯一索引只放行一个。
	request, err := json.Marshal(map[string]any{"userId": req.UserID, "amount": amount, "anchor": anchor})
	if err != nil {
		return err
	}
	idem, fresh, err := ClaimIdempotencyWithDB(tx, fmt.Sprintf("user:%d", req.Operator.ID), "admin-grant", requestID, request)
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return ErrIdempotencyConflict
		}
		return err
	}
	if !fresh {
		result.AlreadyGranted = true
		result.UserID = user.ID
		result.Amount = amount
		result.RequestID = requestID
		if idem.Status != IdempotencyStatusSucceeded {
			return ErrIdempotencyPending
		}
		return nil
	}

	balanceBefore := util.RoundMoney(user.Balance)
	giftBefore := util.RoundMoney(user.GiftBalance)
	// ③ 入账：规则 3 —— 先抵欠款，剩余才进赠金桶（43-*.md 算例 B）。
	balanceAfter, giftAfter := CreditGiftBuckets(balanceBefore, giftBefore, amount)
	if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"balance":      balanceAfter,
		"gift_balance": giftAfter,
	}).Error; err != nil {
		return err
	}

	reason := strings.TrimSpace(req.Reason)
	subject := "管理员赠送 ¥" + balanceMoneyText(amount)
	if reason != "" {
		subject = subject + "：" + reason
	}
	record := entity.BillingRecord{
		UserID:           user.ID,
		Type:             AdminGrantBillingRecordType,
		Provider:         "admin",
		Amount:           amount,
		BalanceBefore:    balanceBefore,
		BalanceAfter:     balanceAfter,
		OperatorID:       req.Operator.ID,
		OperatorUsername: req.Operator.Username,
		RequestID:        requestID,
		Remark:           subject,
		ClientIP:         strings.TrimSpace(req.ClientIP),
		// 赠送走赠金桶，本金前后值原样保留（billing_records 没有 gift 列，
		// 赠金前后值按既有 key=value 快照约定进 unit_price_snapshot）。
		UnitPriceSnapshot: fmt.Sprintf(
			"walletBucket=%s giftBalanceBefore=%s giftBalanceAfter=%s debtOffset=%s anchor=%s",
			WalletBucketGift, balanceMoneyText(giftBefore), balanceMoneyText(giftAfter),
			balanceMoneyText(util.RoundMoney(balanceAfter-balanceBefore)), anchor),
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		return err
	}

	before, _ := json.Marshal(map[string]any{
		"userId": user.ID, "username": user.Username, "balance": balanceBefore, "giftBalance": giftBefore,
	})
	after, _ := json.Marshal(map[string]any{
		"userId": user.ID, "username": user.Username, "balance": balanceAfter, "giftBalance": giftAfter,
		"amount": amount, "reason": reason, "billingRecordId": record.ID, "anchor": anchor,
	})
	description := fmt.Sprintf("管理员 %s(ID=%d) 赠送用户 %s(ID=%d) ¥%s（赠金桶 %s → %s；抵欠款 ¥%s）",
		auditBalanceName(req.Operator.Username), req.Operator.ID, auditBalanceName(user.Username), user.ID,
		balanceMoneyText(amount), balanceMoneyText(giftBefore), balanceMoneyText(giftAfter),
		balanceMoneyText(util.RoundMoney(balanceAfter-balanceBefore)))
	if err := createBalanceAuditLog(tx, req.Operator, "user", description, req.ClientIP, string(before), string(after)); err != nil {
		return err
	}

	response, err := json.Marshal(map[string]any{
		"userId": user.ID, "amount": amount, "balanceAfter": balanceAfter,
		"giftBalanceAfter": giftAfter, "billingRecordId": record.ID, "anchor": anchor,
	})
	if err != nil {
		return err
	}
	if err := CompleteIdempotencyWithDB(tx, idem.ID, 200, response); err != nil {
		return err
	}

	result.UserID = user.ID
	result.Amount = amount
	result.DebtOffset = util.RoundMoney(balanceAfter - balanceBefore)
	result.BalanceBefore = balanceBefore
	result.BalanceAfter = balanceAfter
	result.GiftBefore = giftBefore
	result.GiftAfter = giftAfter
	result.BillingRecordID = record.ID
	return nil
}

// ensureAdminGrantUserAvailable 挡住已归档与已封禁用户（批量赠送的排除口径同此）。
func ensureAdminGrantUserAvailable(user entity.User) error {
	if user.DeletedAt.Valid {
		return fmt.Errorf("%w：用户已归档", ErrAdminGrantUserUnavailable)
	}
	if strings.EqualFold(strings.TrimSpace(user.Status), adminGrantUserStatusBanned) {
		return fmt.Errorf("%w：用户已封禁", ErrAdminGrantUserUnavailable)
	}
	return nil
}

// loadAdminGrantRecord 按 request_id 取赠送流水（持久幂等层）。
func loadAdminGrantRecord(tx *gorm.DB, requestID string) (entity.BillingRecord, bool, error) {
	var record entity.BillingRecord
	if strings.TrimSpace(requestID) == "" {
		return record, false, nil
	}
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Where("request_id = ? AND type = ?", requestID, AdminGrantBillingRecordType).
		Order("id asc").First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	return record, true, nil
}

// parseGiftSnapshot 从 key=value 快照里取赠金桶前后值。
//
// ⚠️ 用 ExtractBillingSnapshotField 而不是 parseBillingSnapshotPairs：本仓的
// unit_price_snapshot 是**空格分隔**的 `k=v k=v`（user_balance.go:221 / referral.go:746 /
// new_user_bonus.go:91 都这么写，测试也用 ExtractBillingSnapshotField 断言），
// 而 parseBillingSnapshotPairs 按**逗号**切 —— 混用会静默解析失败（实测：
// ok=false，赠金前后值全读成 0，回滚时会把本金多扣一遍）。
//
// 解析失败返回 false —— 历史行没有快照时**不能当成 0**（43-*.md 的历史行口径）。
func parseGiftSnapshot(snapshot string) (before, after float64, ok bool) {
	beforeRaw := strings.TrimSpace(ExtractBillingSnapshotField(snapshot, "giftBalanceBefore"))
	afterRaw := strings.TrimSpace(ExtractBillingSnapshotField(snapshot, "giftBalanceAfter"))
	if beforeRaw == "" || afterRaw == "" {
		return 0, 0, false
	}
	beforeValue, err := strconv.ParseFloat(beforeRaw, 64)
	if err != nil {
		return 0, 0, false
	}
	afterValue, err := strconv.ParseFloat(afterRaw, 64)
	if err != nil {
		return 0, 0, false
	}
	return util.RoundMoney(beforeValue), util.RoundMoney(afterValue), true
}

// planAdminGrantReversal 计算追缴的两桶分配（纯函数，资金守恒测试直接钉死它）。
//
// ③ 追缴式回滚的三步（117-*.md §1.2 / 118-*.md §6.1）：
//
//	fromGift    = min(gift, amount)        // 赠金扣到 0 为止（下限 0，不可为负）
//	shortfall   = amount - fromGift        // 赠金桶扣不动的部分
//	newBalance  = balance - shortfall      // **不设上限**，本金可为负（欠款载体）
//
// 差额必须**继续从本金扣**，不能丢弃：丢弃等于这笔钱既没追回也没留痕。
func planAdminGrantReversal(balance, gift, amount float64) (fromGift, fromBalance, newGift, newBalance float64) {
	gift = util.RoundMoney(gift)
	balance = util.RoundMoney(balance)
	amount = util.RoundMoney(amount)
	if amount <= 0 {
		return 0, 0, gift, balance
	}
	deducted, remainingGift := DeductGiftBucket(gift, amount)
	shortfall := util.RoundMoney(amount - deducted)
	return deducted, shortfall, remainingGift, util.RoundMoney(balance - shortfall)
}

// AdminGrantReverse 执行一次追缴式回滚（单用户）。
//
// 两笔独立流水、两个独立幂等键（118-*.md §5.3）；金额为 0 的那笔**不写**（§5.4）。
func AdminGrantReverse(db *gorm.DB, req AdminGrantReverseRequest) (AdminGrantReverseResult, error) {
	var result AdminGrantReverseResult
	if db == nil {
		return result, fmt.Errorf("%w：数据库不可用", ErrInvalidAdminGrant)
	}
	if !isAdminUser(req.Operator) {
		return result, ErrBalanceAdjustmentForbidden
	}
	if req.UserID <= 0 {
		return result, fmt.Errorf("%w：用户参数错误", ErrInvalidAdminGrant)
	}
	amount, err := normalizeAdminGrantAmount(req.Amount)
	if err != nil {
		return result, err
	}
	anchor, err := normalizeAdminGrantAnchor(req.Anchor)
	if err != nil {
		return result, err
	}
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		return adminGrantReverseInTx(tx, req, anchor, amount, &result)
	})
	return result, err
}

// adminGrantReverseInTx 是回滚的唯一实现（事务内）。
//
// 断点续跑语义：赠金笔与本金笔各自判重，上一次只写了赠金笔也能从这里接着把本金笔补上。
func adminGrantReverseInTx(tx *gorm.DB, req AdminGrantReverseRequest, anchor string, amount float64, result *AdminGrantReverseResult) error {
	tx = tx.Session(&gorm.Session{NewDB: true})
	giftRequestID := AdminGrantReversalGiftRequestID(req.UserID, anchor)
	principalRequestID := AdminGrantReversalPrincipalRequestID(req.UserID, anchor)
	result.UserID = req.UserID
	result.Expected = amount

	giftRecord, giftDone, err := loadAdminGrantReversalRecord(tx, giftRequestID)
	if err != nil {
		return err
	}
	principalRecord, principalDone, err := loadAdminGrantReversalRecord(tx, principalRequestID)
	if err != nil {
		return err
	}

	var user entity.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, req.UserID).Error; err != nil {
		return err
	}
	balanceBefore := util.RoundMoney(user.Balance)
	giftBefore := util.RoundMoney(user.GiftBalance)

	if giftDone && principalDone {
		// 两笔都在：整笔已追缴完，本次零资金动作（同一 anchor 重放必然走到这里）。
		result.Skipped = true
		result.FromGift = util.RoundMoney(giftRecord.Amount)
		result.FromBalance = util.RoundMoney(principalRecord.Amount)
		result.Shortfall = util.RoundMoney(amount - result.FromGift)
		result.BalanceBefore = balanceBefore
		result.BalanceAfter = balanceBefore
		result.GiftBefore = giftBefore
		result.GiftAfter = giftBefore
		result.GiftRecordID = giftRecord.ID
		result.PrincipalRecordID = principalRecord.ID
		return nil
	}

	// ① 赠金部分。
	//
	// 若赠金笔已写过，赠金桶**已经**被扣过了，这里绝不能再扣一次
	// （重放场景下 giftBefore 就是扣后的值，再算一遍会把本金也扣穿）。
	fromGift := util.RoundMoney(giftRecord.Amount)
	giftAfter := giftBefore
	shortfall := util.RoundMoney(amount - fromGift)
	if !giftDone {
		var plannedGiftAfter float64
		fromGift, shortfall, plannedGiftAfter, _ = planAdminGrantReversal(balanceBefore, giftBefore, amount)
		giftAfter = plannedGiftAfter
	}

	// ② 本金部分：不设上限，允许变负。同理，本金笔已写过就不再重复扣。
	fromBalance := util.RoundMoney(principalRecord.Amount)
	balanceAfter := balanceBefore
	if !principalDone {
		fromBalance = shortfall
		balanceAfter = util.RoundMoney(balanceBefore - shortfall)
	}

	// applied 表示本次真的动了钱。它是 Skipped 的唯一判据：
	// 「赠金笔已写、且没有任何差额需要走本金」也是完整追缴完毕（金额为 0 的那笔本来就不写）。
	applied := (!giftDone && fromGift > 0) || (!principalDone && fromBalance > 0)
	if !applied {
		// 纯重放：一个字节都没动，连审计日志也不写（否则每次轮询式重试都会留下一条
		// 「管理员追缴了 0 元」的噪音记录，掩盖真实操作）。
		result.FromGift = fromGift
		result.FromBalance = fromBalance
		result.Shortfall = util.RoundMoney(amount - fromGift)
		result.BalanceBefore = balanceBefore
		result.BalanceAfter = balanceBefore
		result.GiftBefore = giftBefore
		result.GiftAfter = giftBefore
		result.Skipped = true
		if giftDone {
			result.GiftRecordID = giftRecord.ID
		}
		if principalDone {
			result.PrincipalRecordID = principalRecord.ID
		}
		return nil
	}
	if giftAfter != giftBefore || balanceAfter != balanceBefore {
		if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"balance":      balanceAfter,
			"gift_balance": giftAfter,
		}).Error; err != nil {
			return err
		}
	}

	reason := strings.TrimSpace(req.Reason)
	// ③ 赠金笔：金额为 0 不写（118 §5.4 —— 否则会产生大量 amount=0 的垃圾流水）。
	if !giftDone && fromGift > 0 {
		record := entity.BillingRecord{
			UserID:           user.ID,
			Type:             AdminGrantReversalBillingRecordType,
			Provider:         "admin",
			Amount:           fromGift,
			BalanceBefore:    balanceBefore,
			BalanceAfter:     balanceBefore,
			OperatorID:       req.Operator.ID,
			OperatorUsername: req.Operator.Username,
			RequestID:        giftRequestID,
			Remark:           buildAdminGrantReversalGiftRemark(reason, amount, fromGift, shortfall),
			ClientIP:         strings.TrimSpace(req.ClientIP),
			UnitPriceSnapshot: fmt.Sprintf("walletBucket=%s giftBalanceBefore=%s giftBalanceAfter=%s expected=%s shortfall=%s anchor=%s",
				WalletBucketGift, balanceMoneyText(giftBefore), balanceMoneyText(giftAfter),
				balanceMoneyText(amount), balanceMoneyText(shortfall), anchor),
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
			return err
		}
		result.GiftRecordID = record.ID
	} else if giftDone {
		result.GiftRecordID = giftRecord.ID
	}

	// ④ 本金笔：同样金额为 0 不写。
	if !principalDone && fromBalance > 0 {
		record := entity.BillingRecord{
			UserID:           user.ID,
			Type:             AdminGrantReversalBillingRecordType,
			Provider:         "admin",
			Amount:           fromBalance,
			BalanceBefore:    balanceBefore,
			BalanceAfter:     balanceAfter,
			OperatorID:       req.Operator.ID,
			OperatorUsername: req.Operator.Username,
			RequestID:        principalRequestID,
			Remark:           buildAdminGrantReversalPrincipalRemark(reason, amount, fromGift, fromBalance, balanceAfter),
			ClientIP:         strings.TrimSpace(req.ClientIP),
			UnitPriceSnapshot: fmt.Sprintf("walletBucket=%s expected=%s giftDeducted=%s principalDeducted=%s anchor=%s",
				WalletBucketPrincipal, balanceMoneyText(amount), balanceMoneyText(fromGift),
				balanceMoneyText(fromBalance), anchor),
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
			return err
		}
		result.PrincipalRecordID = record.ID
	} else if principalDone {
		result.PrincipalRecordID = principalRecord.ID
	}

	before, _ := json.Marshal(map[string]any{
		"userId": user.ID, "username": user.Username, "balance": balanceBefore, "giftBalance": giftBefore,
	})
	after, _ := json.Marshal(map[string]any{
		"userId": user.ID, "username": user.Username, "balance": balanceAfter, "giftBalance": giftAfter,
		"expected": amount, "fromGift": fromGift, "fromBalance": fromBalance, "anchor": anchor,
	})
	description := fmt.Sprintf("管理员 %s(ID=%d) 追缴用户 %s(ID=%d) 赠送 ¥%s：赠金扣 ¥%s、本金扣 ¥%s（本金 %s → %s）",
		auditBalanceName(req.Operator.Username), req.Operator.ID, auditBalanceName(user.Username), user.ID,
		balanceMoneyText(amount), balanceMoneyText(fromGift), balanceMoneyText(fromBalance),
		balanceMoneyText(balanceBefore), balanceMoneyText(balanceAfter))
	if err := createBalanceAuditLog(tx, req.Operator, "user", description, req.ClientIP, string(before), string(after)); err != nil {
		return err
	}

	result.FromGift = fromGift
	result.FromBalance = fromBalance
	result.Shortfall = util.RoundMoney(amount - fromGift)
	result.BalanceBefore = balanceBefore
	result.BalanceAfter = balanceAfter
	result.GiftBefore = giftBefore
	result.GiftAfter = giftAfter
	// skipped 的语义是「本次没有产生任何新的资金动作」，它必须由 applied 决定而不是
	// 由「两笔都判重」决定 —— 否则「赠金笔已写、本金差额为 0」的完整追缴会被误报成
	// 未跳过（实测：重放时 skipped=false，而实际上一个字节都没动）。
	result.Skipped = !applied
	return nil
}

// loadAdminGrantReversalRecord 按 request_id 取回滚流水（两笔各用各的键）。
func loadAdminGrantReversalRecord(tx *gorm.DB, requestID string) (entity.BillingRecord, bool, error) {
	var record entity.BillingRecord
	if strings.TrimSpace(requestID) == "" {
		return record, false, nil
	}
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Where("request_id = ? AND type = ?", requestID, AdminGrantReversalBillingRecordType).
		Order("id asc").First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	return record, true, nil
}

// buildAdminGrantReversalGiftRemark 拼赠金笔备注。
//
// **差额必须留痕**（任务书 B 变异点）：`应扣 10 / 实扣 7 / 差额 3` 三件事缺一不可，
// 静默吞掉差额等于对账时凭空少一笔。
const adminGrantRemarkMaxRunes = 120

func buildAdminGrantReversalGiftRemark(reason string, expected, fromGift, shortfall float64) string {
	parts := []string{fmt.Sprintf("赠送回滚（赠金部分）：应扣 ¥%s，赠金实扣 ¥%s",
		balanceMoneyText(expected), balanceMoneyText(fromGift))}
	if shortfall > 0 {
		parts = append(parts, fmt.Sprintf("差额 ¥%s 转由本金扣减", balanceMoneyText(shortfall)))
	}
	if reason != "" {
		parts = append(parts, reason)
	}
	return truncateAdminGrantRemark(strings.Join(parts, "；"))
}

// buildAdminGrantReversalPrincipalRemark 拼本金笔备注。
func buildAdminGrantReversalPrincipalRemark(reason string, expected, fromGift, fromBalance, balanceAfter float64) string {
	head := fmt.Sprintf("赠送回滚（本金部分）：赠金不足，本金扣 ¥%s", balanceMoneyText(fromBalance))
	if fromGift > 0 {
		head = fmt.Sprintf("赠送回滚（本金部分）：应扣 ¥%s，赠金已扣 ¥%s，本金扣 ¥%s",
			balanceMoneyText(expected), balanceMoneyText(fromGift), balanceMoneyText(fromBalance))
	}
	parts := []string{head}
	if balanceAfter < 0 {
		parts = append(parts, fmt.Sprintf("已形成欠款 ¥%s", balanceMoneyText(-balanceAfter)))
	}
	if reason != "" {
		parts = append(parts, reason)
	}
	return truncateAdminGrantRemark(strings.Join(parts, "；"))
}

// truncateAdminGrantRemark 把备注裁到 remark 列（varchar(255)）安全长度内。
func truncateAdminGrantRemark(text string) string {
	runes := []rune(text)
	if len(runes) <= adminGrantRemarkMaxRunes {
		return text
	}
	return string(runes[:adminGrantRemarkMaxRunes-3]) + "..."
}
