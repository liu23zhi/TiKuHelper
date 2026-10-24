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

var (
	ErrBalanceAdjustmentForbidden = errors.New("仅管理员可修改余额或账单流水可见性")
	ErrInvalidBalanceAdjustment   = errors.New("余额调整参数无效")
	ErrBalanceChanged             = errors.New("余额已发生变化，请刷新后重试")
)

type UserBalanceAdjustmentInput struct {
	Mode            string  `json:"mode"`
	Amount          float64 `json:"amount"`
	ShowInStatement *bool   `json:"showInStatement"`
	ClientIP        string  `json:"-"`
}

func NormalizeUserBalanceAdjustment(input UserBalanceAdjustmentInput) (UserBalanceAdjustmentInput, error) {
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	if input.Mode != "override" && input.Mode != "increase" && input.Mode != "decrease" {
		return input, fmt.Errorf("%w：余额调整模式错误", ErrInvalidBalanceAdjustment)
	}
	if math.IsNaN(input.Amount) || math.IsInf(input.Amount, 0) || math.Abs(input.Amount) >= 1e12 || util.HasMoreDecimalsThan(input.Amount, util.MoneyPrecision) {
		return input, fmt.Errorf("%w：金额必须为最多 6 位小数的有效余额", ErrInvalidBalanceAdjustment)
	}
	if input.Mode != "override" && input.Amount < 0 {
		return input, fmt.Errorf("%w：添加或减少金额不能为负数", ErrInvalidBalanceAdjustment)
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
				ID      int32   `json:"id"`
				Balance float64 `json:"balance"`
			}
			if err := json.Unmarshal([]byte(record.ResponseJSON), &saved); err != nil {
				return err
			}
			result.ID, result.Balance = saved.ID, saved.Balance
			return nil
		}
		result, err = AdjustUserBalanceInTx(tx, operator, userID, input)
		if err != nil {
			return err
		}
		response, err := json.Marshal(map[string]any{"id": result.ID, "balance": result.Balance})
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
	before := util.RoundMoney(user.Balance)
	after := input.Amount
	if input.Mode == "increase" {
		after = util.RoundMoney(before + input.Amount)
	}
	if input.Mode == "decrease" {
		after = util.RoundMoney(before - input.Amount)
	}
	delta := util.RoundMoney(after - before)
	if math.IsNaN(after) || math.IsInf(after, 0) || math.Abs(after) >= 1e12 || math.Abs(delta) >= 1e10 {
		return user, fmt.Errorf("%w：余额或变动金额超出存储范围", ErrInvalidBalanceAdjustment)
	}
	if after != before {
		updated := tx.Model(&entity.User{}).Where("id = ? AND balance = ?", user.ID, user.Balance).Update("balance", after)
		if updated.Error != nil {
			return user, updated.Error
		}
		if updated.RowsAffected != 1 {
			return user, ErrBalanceChanged
		}
	}
	show := input.ShowInStatement == nil || *input.ShowInStatement
	subject := buildBalanceAdjustmentSubject(input.Mode, input.Amount)
	record := entity.BillingRecord{
		UserID: user.ID, Type: balanceAdjustmentBillingRecordType, Provider: "admin",
		Amount: math.Abs(delta), BalanceBefore: before, BalanceAfter: after,
		OperatorID: operator.ID, OperatorUsername: operator.Username, AdjustmentMode: input.Mode,
		HiddenFromStatement: !show, ClientIP: strings.TrimSpace(input.ClientIP),
		RequestID: "balance_" + uuid.NewString(), Remark: subject,
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		return user, err
	}
	oldSnapshot, _ := json.Marshal(map[string]any{"userId": user.ID, "username": user.Username, "balance": before})
	newSnapshot, _ := json.Marshal(map[string]any{"userId": user.ID, "username": user.Username, "balance": after, "mode": input.Mode, "amount": input.Amount, "showInStatement": show, "billingRecordId": record.ID})
	description := fmt.Sprintf("管理员 %s(ID=%d) 修改用户 %s(ID=%d) 余额：%s；%s → %s；流水展示=%t", auditBalanceName(operator.Username), operator.ID, auditBalanceName(user.Username), user.ID, subject, balanceMoneyText(before), balanceMoneyText(after), show)
	if err := createBalanceAuditLog(tx, operator, "user", description, input.ClientIP, string(oldSnapshot), string(newSnapshot)); err != nil {
		return user, err
	}
	user.Balance = after
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
