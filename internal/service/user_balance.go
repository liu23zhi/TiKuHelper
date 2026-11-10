package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const balanceAdjustmentBillingRecordType = "balance_adjustment"

// 管理员调整余额时可以选择的钱包桶（43-*.md / 用户 2026-11-09 明确要求
// 「让管理员可以给用户新增赠送金额还是普通金额」）。
const (
	// WalletBucketPrincipal 是充值本金桶 users.balance：可消费、**可退款**、允许为负（欠款）。
	WalletBucketPrincipal = "principal"
	// WalletBucketGift 是赠送金额桶 users.gift_balance：可消费、**不可退款**、下限 0。
	WalletBucketGift = "gift"
)

// WalletBucketLabel 返回桶的中文名（用于账单备注与前端展示）。
func WalletBucketLabel(bucket string) string {
	if bucket == WalletBucketGift {
		return "赠送金额"
	}
	return "充值本金"
}

// NormalizeWalletBucket 规范化桶名：空值默认本金桶（保持既有调用方的行为不变）。
func NormalizeWalletBucket(bucket string) string {
	normalized := strings.ToLower(strings.TrimSpace(bucket))
	if normalized == WalletBucketGift {
		return WalletBucketGift
	}
	return WalletBucketPrincipal
}

var (
	ErrBalanceAdjustmentForbidden = errors.New("仅管理员可修改余额或账单流水可见性")
	ErrInvalidBalanceAdjustment   = errors.New("余额调整参数无效")
	ErrBalanceChanged             = errors.New("余额已发生变化，请刷新后重试")
)

type UserBalanceAdjustmentInput struct {
	Mode            string  `json:"mode"`
	Amount          float64 `json:"amount"`
	ShowInStatement *bool   `json:"showInStatement"`
	// Bucket 选择调整哪个钱包桶：principal=充值本金（默认）/ gift=赠送金额。
	// 空值等价于 principal，保证既有客户端（不传该字段）行为不变。
	Bucket   string `json:"bucket"`
	ClientIP string `json:"-"`
}

func NormalizeUserBalanceAdjustment(input UserBalanceAdjustmentInput) (UserBalanceAdjustmentInput, error) {
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	if input.Mode != "override" && input.Mode != "increase" && input.Mode != "decrease" {
		return input, fmt.Errorf("%w：余额调整模式错误", ErrInvalidBalanceAdjustment)
	}
	rawBucket := strings.ToLower(strings.TrimSpace(input.Bucket))
	if rawBucket != "" && rawBucket != WalletBucketPrincipal && rawBucket != WalletBucketGift {
		return input, fmt.Errorf("%w：钱包桶只能是 principal 或 gift", ErrInvalidBalanceAdjustment)
	}
	input.Bucket = NormalizeWalletBucket(rawBucket)
	if math.IsNaN(input.Amount) || math.IsInf(input.Amount, 0) || math.Abs(input.Amount) >= 1e12 || util.HasMoreDecimalsThan(input.Amount, util.MoneyPrecision) {
		return input, fmt.Errorf("%w：金额必须为最多 6 位小数的有效余额", ErrInvalidBalanceAdjustment)
	}
	if input.Mode != "override" && input.Amount < 0 {
		return input, fmt.Errorf("%w：添加或减少金额不能为负数", ErrInvalidBalanceAdjustment)
	}
	// 赠送桶下限 0（用户明确要求「赠送金额只能为 0，不能为负数」），
	// 所以 override 到负数与 decrease 越界都在这里拒绝 —— 管理员也不该造出负赠金。
	if input.Bucket == WalletBucketGift && input.Mode == "override" && input.Amount < 0 {
		return input, fmt.Errorf("%w：赠送金额不能为负数", ErrInvalidBalanceAdjustment)
	}
	input.Amount = util.RoundMoney(input.Amount)
	return input, nil
}

func AdjustUserBalanceWithDB(db *gorm.DB, operator entity.User, userID int32, input UserBalanceAdjustmentInput) (entity.User, error) {
	var user entity.User
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var err error
		user, err = AdjustUserBalanceInTx(tx, operator, userID, input)
		return err
	})
	return user, err
}

func AdjustUserBalanceIdempotentWithDB(db *gorm.DB, operator entity.User, userID int32, input UserBalanceAdjustmentInput, key string) (entity.User, error) {
	var result entity.User
	request, err := json.Marshal(struct {
		UserID int32                      `json:"userId"`
		Input  UserBalanceAdjustmentInput `json:"input"`
	}{UserID: userID, Input: input})
	if err != nil {
		return result, err
	}
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		record, fresh, err := ClaimIdempotencyWithDB(tx, fmt.Sprintf("user:%d", operator.ID), "balance-adjustment", key, request)
		if err != nil {
			return err
		}
		if !fresh {
			if record.Status != IdempotencyStatusSucceeded {
				return ErrIdempotencyPending
			}
			var saved struct {
				ID          int32   `json:"id"`
				Balance     float64 `json:"balance"`
				GiftBalance float64 `json:"giftBalance"`
			}
			if err := json.Unmarshal([]byte(record.ResponseJSON), &saved); err != nil {
				return err
			}
			result.ID, result.Balance, result.GiftBalance = saved.ID, saved.Balance, saved.GiftBalance
			return nil
		}
		result, err = AdjustUserBalanceInTx(tx, operator, userID, input)
		if err != nil {
			return err
		}
		response, err := json.Marshal(map[string]any{
			"id": result.ID, "balance": result.Balance, "giftBalance": result.GiftBalance,
		})
		if err != nil {
			return err
		}
		return CompleteIdempotencyWithDB(tx, record.ID, 200, response)
	})
	return result, err
}

// Balance, permanent ledger details and audit log have one transaction owner.
func AdjustUserBalanceInTx(tx *gorm.DB, operator entity.User, userID int32, input UserBalanceAdjustmentInput) (entity.User, error) {
	tx = tx.Session(&gorm.Session{NewDB: true})
	var user entity.User
	if !isAdminUser(operator) {
		return user, ErrBalanceAdjustmentForbidden
	}
	input, err := NormalizeUserBalanceAdjustment(input)
	if err != nil {
		return user, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userID).Error; err != nil {
		return user, err
	}
	// 按桶分别处理：本金桶允许为负（欠款载体），赠送桶下限 0。
	bucket := NormalizeWalletBucket(input.Bucket)
	principalBefore := util.RoundMoney(user.Balance)
	giftBefore := util.RoundMoney(user.GiftBalance)
	before, after := principalBefore, principalBefore
	if bucket == WalletBucketGift {
		before, after = giftBefore, giftBefore
	}
	switch input.Mode {
	case "increase":
		after = util.RoundMoney(before + input.Amount)
	case "decrease":
		after = util.RoundMoney(before - input.Amount)
	default:
		after = util.RoundMoney(input.Amount)
	}
	// 赠送桶永不为负（用户明确要求）：减到 0 为止，不做欠账。
	// 本金桶**不 clamp** —— 它是欠款的载体，允许管理员把它改成负数。
	if bucket == WalletBucketGift && after < 0 {
		after = 0
	}
	delta := util.RoundMoney(after - before)
	if math.IsNaN(after) || math.IsInf(after, 0) || math.Abs(after) >= 1e12 || math.Abs(delta) >= 1e10 {
		return user, fmt.Errorf("%w：余额或变动金额超出存储范围", ErrInvalidBalanceAdjustment)
	}
	if after != before {
		updates := map[string]any{"balance": after}
		condition := "id = ? AND balance = ?"
		args := []any{user.ID, user.Balance}
		if bucket == WalletBucketGift {
			updates = map[string]any{"gift_balance": after}
			condition = "id = ? AND gift_balance = ?"
			args = []any{user.ID, util.RoundMoney(user.GiftBalance)}
		}
		updated := tx.Model(&entity.User{}).Where(condition, args...).Updates(updates)
		if updated.Error != nil {
			return user, updated.Error
		}
		if updated.RowsAffected != 1 {
			return user, ErrBalanceChanged
		}
	}
	principalAfter := principalBefore
	giftAfterValue := giftBefore
	if bucket == WalletBucketGift {
		giftAfterValue = after
	} else {
		principalAfter = after
	}
	show := input.ShowInStatement == nil || *input.ShowInStatement
	subject := buildBalanceAdjustmentSubject(input.Mode, input.Amount)
	// 调整赠送桶时在文案里标明桶名，否则账单上「管理员添加了 ¥5」看不出进的是哪一桶。
	if bucket == WalletBucketGift {
		subject = "赠送金额：" + subject
	}
	record := entity.BillingRecord{
		UserID: user.ID, Type: balanceAdjustmentBillingRecordType, Provider: "admin",
		Amount: math.Abs(delta), BalanceBefore: principalBefore, BalanceAfter: principalAfter,
		OperatorID: operator.ID, OperatorUsername: operator.Username, AdjustmentMode: input.Mode,
		HiddenFromStatement: !show, ClientIP: strings.TrimSpace(input.ClientIP),
		RequestID: "balance_" + uuid.NewString(), Remark: subject,
	}
	if bucket == WalletBucketGift {
		// 本金桶没动：流水里两个本金字段保持原值，赠送桶前后值进快照。
		record.UnitPriceSnapshot = fmt.Sprintf("walletBucket=%s giftBalanceBefore=%s giftBalanceAfter=%s",
			WalletBucketGift, balanceMoneyText(giftBefore), balanceMoneyText(giftAfterValue))
	} else {
		record.UnitPriceSnapshot = fmt.Sprintf("walletBucket=%s", WalletBucketPrincipal)
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		return user, err
	}
	oldSnapshot, _ := json.Marshal(map[string]any{"userId": user.ID, "username": user.Username, "balance": principalBefore, "giftBalance": giftBefore})
	newSnapshot, _ := json.Marshal(map[string]any{"userId": user.ID, "username": user.Username, "balance": principalAfter, "giftBalance": giftAfterValue, "mode": input.Mode, "amount": input.Amount, "bucket": bucket, "showInStatement": show, "billingRecordId": record.ID})
	description := fmt.Sprintf("管理员 %s(ID=%d) 修改用户 %s(ID=%d) 的%s：%s；%s → %s；流水展示=%t",
		auditBalanceName(operator.Username), operator.ID, auditBalanceName(user.Username), user.ID,
		WalletBucketLabel(bucket), subject, balanceMoneyText(before), balanceMoneyText(after), show)
	if err := createBalanceAuditLog(tx, operator, "user", description, input.ClientIP, string(oldSnapshot), string(newSnapshot)); err != nil {
		return user, err
	}
	user.Balance = principalAfter
	user.GiftBalance = giftAfterValue
	return user, nil
}

func balanceMoneyText(value float64) string {
	return strconv.FormatFloat(util.RoundMoney(value), 'f', -1, 64)
}

func buildBalanceAdjustmentSubject(mode string, amount float64) string {
	switch mode {
	case "increase":
		return "管理员添加了 ¥ " + balanceMoneyText(amount)
	case "decrease":
		return "管理员减少了 ¥ " + balanceMoneyText(amount)
	default:
		return "管理员将余额覆盖成了 ¥ " + balanceMoneyText(amount)
	}
}

func auditBalanceName(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 40 {
		return string(runes[:40]) + "..."
	}
	return string(runes)
}

func createBalanceAuditLog(tx *gorm.DB, operator entity.User, module, description, clientIP, before, after string) error {
	runes := []rune(description)
	if len(runes) > 240 {
		description = string(runes[:237]) + "..."
	}
	entry := entity.Log{UserID: operator.ID, Username: operator.Username, Module: module, Action: 1,
		Description: description, ClientIP: strings.TrimSpace(clientIP), OldAnswer: before, NewAnswer: after,
		CreateTime: time.Now().In(logger.ShanghaiLocation()),
	}
	return tx.Session(&gorm.Session{NewDB: true}).Create(&entry).Error
}

func SetBillingStatementVisibilityWithDB(db *gorm.DB, operator entity.User, recordID int32, show bool, clientIP string) error {
	if !isAdminUser(operator) {
		return ErrBalanceAdjustmentForbidden
	}
	return db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var record entity.BillingRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, recordID).Error; err != nil {
			return err
		}
		if record.HiddenFromStatement == !show {
			return nil
		}
		query := tx.Model(&entity.BillingRecord{}).Where("id = ?", record.ID)
		if record.Type == normalSearchBillingRecordType && strings.TrimSpace(record.RequestID) != "" {
			// The displayed search row represents all charges for this request.
			query = tx.Model(&entity.BillingRecord{}).Where("user_id = ? AND type = ? AND request_id = ?", record.UserID, record.Type, record.RequestID)
		}
		if err := query.Update("hidden_from_statement", !show).Error; err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"billingRecordId": record.ID, "userId": record.UserID, "showInStatement": !record.HiddenFromStatement})
		after, _ := json.Marshal(map[string]any{"billingRecordId": record.ID, "userId": record.UserID, "showInStatement": show})
		description := fmt.Sprintf("管理员 %s(ID=%d) 修改用户 ID=%d 的账单 #%d 流水展示：%t → %t", auditBalanceName(operator.Username), operator.ID, record.UserID, record.ID, !record.HiddenFromStatement, show)
		return createBalanceAuditLog(tx, operator, "billing", description, clientIP, string(before), string(after))
	})
}
