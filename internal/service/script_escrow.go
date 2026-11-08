package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ScriptEscrowPolicySettingKey = "script_escrow_policy_v1"
	ScriptEscrowMicrosPerYuan    = int64(1_000_000)
	// DECIMAL(18,6), the existing wallet's storage range.
	maxScriptEscrowMicros = int64(999_999_999_999_999_999)
	scriptEscrowFreeze    = "freeze"
	scriptEscrowTopUp     = "topup"
	scriptEscrowSettle    = "settle"
)

var (
	ErrScriptEscrowMissing      = errors.New("脚本未启用冻结金模式")
	ErrScriptEscrowInsufficient = errors.New("余额或脚本冻结金额不足")
	ErrScriptEscrowInvalid      = errors.New("脚本冻结金金额或参数无效")
	ErrScriptEscrowConflict     = errors.New("脚本冻结金状态已变化或操作与原请求不一致")
	ErrScriptEscrowTransaction  = errors.New("脚本冻结金操作必须在调用方事务内执行")
)

type ScriptEscrowPolicy struct {
	TargetMicros     int64 `json:"targetMicros"`
	MinimumMicros    int64 `json:"minimumMicros"`
	MultiplierMicros int64 `json:"multiplierMicros"`
}

func defaultScriptEscrowPolicy() ScriptEscrowPolicy {
	return ScriptEscrowPolicy{
		TargetMicros:     80 * ScriptEscrowMicrosPerYuan,
		MinimumMicros:    60 * ScriptEscrowMicrosPerYuan,
		MultiplierMicros: ScriptEscrowMicrosPerYuan,
	}
}

func validScriptEscrowPolicy(p ScriptEscrowPolicy) bool {
	return p.MinimumMicros > 0 && p.TargetMicros >= p.MinimumMicros &&
		p.TargetMicros <= maxScriptEscrowMicros && p.MultiplierMicros > 0 &&
		p.MultiplierMicros <= maxScriptEscrowMicros
}

// GetScriptEscrowPolicyWithDB defaults only when no setting was installed.
// Invalid configured policy fails closed instead of silently authorizing
// a lower deposit or resetting the administrator's multiplier.
func GetScriptEscrowPolicyWithDB(db *gorm.DB) (ScriptEscrowPolicy, error) {
	if db == nil {
		return ScriptEscrowPolicy{}, ErrScriptEscrowInvalid
	}
	var row entity.SystemSetting
	err := db.Session(&gorm.Session{NewDB: true}).Where(map[string]any{"key": ScriptEscrowPolicySettingKey}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultScriptEscrowPolicy(), nil
	}
	if err != nil {
		return ScriptEscrowPolicy{}, err
	}
	var policy ScriptEscrowPolicy
	if json.Unmarshal([]byte(row.Value), &policy) != nil || !validScriptEscrowPolicy(policy) {
		return ScriptEscrowPolicy{}, ErrScriptEscrowInvalid
	}
	return policy, nil
}

// UpdateScriptEscrowPolicyWithDB replaces the complete policy as one row;
// concurrent launch reads cannot observe a mismatched target/minimum pair.
func UpdateScriptEscrowPolicyWithDB(db *gorm.DB, operatorID int32, policy ScriptEscrowPolicy) error {
	if db == nil || !validScriptEscrowPolicy(policy) || operatorID <= 0 {
		return ErrScriptEscrowInvalid
	}
	payload, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var user entity.User
		err := tx.Select("id", "role", "status").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&user, operatorID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrScriptForbidden
		}
		if err != nil {
			return err
		}
		if !isAdminUser(user) || !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
			return ErrScriptForbidden
		}
		row := entity.SystemSetting{Key: ScriptEscrowPolicySettingKey, Value: string(payload), Remark: "付费脚本冻结金策略"}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).Create(&row).Error
	})
}

type ScriptEscrowMovement struct {
	AmountMicros       int64
	EscrowBeforeMicros int64
	EscrowAfterMicros  int64
	WalletBeforeMicros int64
	WalletAfterMicros  int64
	Replayed           bool
}

func ScriptEscrowYuan(micros int64) float64 {
	return float64(micros) / float64(ScriptEscrowMicrosPerYuan)
}

func movementFromEscrowEntry(row entity.ScriptEscrowEntry, replayed bool) ScriptEscrowMovement {
	return ScriptEscrowMovement{
		AmountMicros: row.AmountMicros, EscrowBeforeMicros: row.EscrowBeforeMicros,
		EscrowAfterMicros: row.EscrowAfterMicros, WalletBeforeMicros: row.WalletBeforeMicros,
		WalletAfterMicros: row.WalletAfterMicros, Replayed: replayed,
	}
}

// FreezeScriptEscrowOnCreateInTx runs after the new owned UserScript row has
// been inserted. The caller must abort the same transaction on any error.
// It never interprets or transfers the old script's ReservedFee.
func FreezeScriptEscrowOnCreateInTx(tx *gorm.DB, scriptID, ownerID int32) (ScriptEscrowMovement, error) {
	if err := requireScriptEscrowTx(tx, scriptID, ownerID); err != nil {
		return ScriptEscrowMovement{}, err
	}
	wallet, err := lockScriptEscrowWallet(tx, ownerID)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	operationHash := hashScriptSecret(scriptEscrowFreeze)
	if previous, err := scriptEscrowReceipt(tx, scriptID, ownerID, scriptEscrowFreeze, operationHash); err != nil {
		return ScriptEscrowMovement{}, err
	} else if previous != nil {
		return movementFromEscrowEntry(*previous, true), nil
	}
	var script entity.UserScript
	err = tx.Session(&gorm.Session{NewDB: true}).Select("id").Where("id = ? AND user_id = ? AND deleted_at IS NULL", scriptID, ownerID).Take(&script).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ScriptEscrowMovement{}, ErrScriptEscrowMissing
	}
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	policy, err := GetScriptEscrowPolicyWithDB(tx)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	if !wallet.enabled || wallet.micros < policy.TargetMicros {
		return ScriptEscrowMovement{}, ErrScriptEscrowInsufficient
	}
	row := entity.ScriptEscrow{ScriptID: scriptID, UserID: ownerID, AvailableMicros: policy.TargetMicros, Revision: 1}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&row).Error; err != nil {
		return ScriptEscrowMovement{}, err
	}
	return storeScriptEscrowMovement(tx, row, wallet, entity.ScriptEscrowEntry{
		OperationHash: operationHash, Kind: scriptEscrowFreeze, AmountMicros: policy.TargetMicros,
		EscrowAfterMicros: policy.TargetMicros, WalletAfterMicros: wallet.micros - policy.TargetMicros,
	}, false)
}

// PrepareScriptEscrowLaunchInTx checks a new lease and optionally replenishes
// it. The caller passes a stable operation key tied to that activation,
// claims the lease, and commits both changes in the same transaction.
// An existing lease's retry must be handled before requesting a new lease.
func PrepareScriptEscrowLaunchInTx(tx *gorm.DB, scriptID, ownerID int32, autoTopUp bool, operationKey string) (ScriptEscrowMovement, error) {
	if err := requireScriptEscrowTx(tx, scriptID, ownerID); err != nil {
		return ScriptEscrowMovement{}, err
	}
	operationHash, err := scriptEscrowOperationHash(scriptEscrowTopUp, operationKey)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	wallet, row, err := lockScriptEscrowAndWallet(tx, scriptID, ownerID)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	if !wallet.enabled || wallet.micros < 0 {
		return ScriptEscrowMovement{}, ErrScriptEscrowInsufficient
	}
	policy, err := GetScriptEscrowPolicyWithDB(tx)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	if previous, err := scriptEscrowReceipt(tx, scriptID, ownerID, scriptEscrowTopUp, operationHash); err != nil {
		return ScriptEscrowMovement{}, err
	} else if previous != nil {
		// Replaying an already funded operation may not grandfather a now
		// inadequate balance or apply a second top-up.
		if row.AvailableMicros < policy.MinimumMicros {
			return ScriptEscrowMovement{}, ErrScriptEscrowInsufficient
		}
		return movementFromEscrowEntry(*previous, true), nil
	}
	var topUp int64
	if autoTopUp && row.AvailableMicros < policy.TargetMicros {
		topUp = min(wallet.micros, policy.TargetMicros-row.AvailableMicros)
	}
	if row.AvailableMicros+topUp < policy.MinimumMicros {
		return ScriptEscrowMovement{}, ErrScriptEscrowInsufficient
	}
	entry := entity.ScriptEscrowEntry{
		OperationHash: operationHash, Kind: scriptEscrowTopUp, AmountMicros: topUp,
		EscrowBeforeMicros: row.AvailableMicros, EscrowAfterMicros: row.AvailableMicros + topUp,
		WalletBeforeMicros: wallet.micros, WalletAfterMicros: wallet.micros - topUp,
	}
	return storeScriptEscrowMovement(tx, row, wallet, entry, topUp != 0)
}

// SettleScriptEscrowInTx funds an already executed operation from its escrow,
// then from the wallet even when that produces debt. Admission and any
// AccessToken hard-limit still belong to the caller. The caller must abort
// the same transaction on any error, or if writing its own billing fails.
func SettleScriptEscrowInTx(tx *gorm.DB, scriptID, ownerID int32, requestID string, amount float64) (ScriptEscrowMovement, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return ScriptEscrowMovement{}, ErrScriptEscrowInvalid
	}
	micros, err := parseScriptEscrowMicros(strconv.FormatFloat(util.RoundMoney(amount), 'f', util.MoneyPrecision, 64))
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	return SettleScriptEscrowMicrosInTx(tx, scriptID, ownerID, requestID, micros)
}

// SettleScriptEscrowMicrosInTx avoids going through float64 when a caller
// has already quantized a multi-line charge to integer millionths.
func SettleScriptEscrowMicrosInTx(tx *gorm.DB, scriptID, ownerID int32, requestID string, micros int64) (ScriptEscrowMovement, error) {
	if err := requireScriptEscrowTx(tx, scriptID, ownerID); err != nil {
		return ScriptEscrowMovement{}, err
	}
	if micros < 0 || micros > maxScriptEscrowMicros {
		return ScriptEscrowMovement{}, ErrScriptEscrowInvalid
	}
	operationHash, err := scriptEscrowOperationHash(scriptEscrowSettle, requestID)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	wallet, row, err := lockScriptEscrowAndWallet(tx, scriptID, ownerID)
	if err != nil {
		return ScriptEscrowMovement{}, err
	}
	if previous, err := scriptEscrowReceipt(tx, scriptID, ownerID, scriptEscrowSettle, operationHash); err != nil {
		return ScriptEscrowMovement{}, err
	} else if previous != nil {
		if previous.AmountMicros != micros {
			return ScriptEscrowMovement{}, ErrScriptEscrowConflict
		}
		return movementFromEscrowEntry(*previous, true), nil
	}
	escrowDebit := min(row.AvailableMicros, micros)
	walletAfter := wallet.micros - (micros - escrowDebit)
	if walletAfter < -maxScriptEscrowMicros {
		return ScriptEscrowMovement{}, ErrScriptEscrowInvalid
	}
	return storeScriptEscrowMovement(tx, row, wallet, entity.ScriptEscrowEntry{
		OperationHash: operationHash, Kind: scriptEscrowSettle, AmountMicros: micros,
		EscrowBeforeMicros: row.AvailableMicros, EscrowAfterMicros: row.AvailableMicros - escrowDebit,
		WalletAfterMicros: walletAfter,
	}, escrowDebit != 0)
}

func requireScriptEscrowTx(tx *gorm.DB, scriptID, ownerID int32) error {
	if tx == nil || tx.Statement == nil {
		return ErrScriptEscrowTransaction
	}
	// GORM wraps prepared transactions, so require the transaction contract
	// rather than the concrete *sql.Tx type.
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrScriptEscrowTransaction
	}
	if scriptID <= 0 || ownerID <= 0 {
		return ErrScriptEscrowInvalid
	}
	return nil
}

type scriptEscrowWallet struct {
	raw     string
	micros  int64
	enabled bool
}

func lockScriptEscrowWallet(tx *gorm.DB, ownerID int32) (scriptEscrowWallet, error) {
	// Reading the SQL decimal as text avoids losing the sixth decimal via
	// the model's float64 on MySQL. SQLite's dynamic numeric column is read
	// back again after update, and rejects unsupported magnitudes.
	var projection struct {
		BalanceText sql.NullString
		Status      string
	}
	balanceCast := "CAST(balance AS CHAR) AS balance_text"
	if tx.Dialector.Name() == "sqlite" {
		balanceCast = "CAST(balance AS TEXT) AS balance_text"
	}
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).Select(balanceCast+", status").
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ownerID).Take(&projection).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return scriptEscrowWallet{}, ErrScriptEscrowMissing
	}
	if err != nil {
		return scriptEscrowWallet{}, err
	}
	if !projection.BalanceText.Valid {
		return scriptEscrowWallet{}, ErrScriptEscrowInvalid
	}
	micros, err := parseScriptEscrowMicros(projection.BalanceText.String)
	return scriptEscrowWallet{
		raw: projection.BalanceText.String, micros: micros,
		enabled: strings.EqualFold(strings.TrimSpace(projection.Status), "enabled"),
	}, err
}

func lockScriptEscrowAndWallet(tx *gorm.DB, scriptID, ownerID int32) (scriptEscrowWallet, entity.ScriptEscrow, error) {
	wallet, err := lockScriptEscrowWallet(tx, ownerID)
	if err != nil {
		return scriptEscrowWallet{}, entity.ScriptEscrow{}, err
	}
	var row entity.ScriptEscrow
	err = tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("script_id = ? AND user_id = ?", scriptID, ownerID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return scriptEscrowWallet{}, entity.ScriptEscrow{}, ErrScriptEscrowMissing
	}
	if err != nil {
		return scriptEscrowWallet{}, entity.ScriptEscrow{}, err
	}
	if row.AvailableMicros < 0 || row.AvailableMicros > maxScriptEscrowMicros || row.Revision < 1 {
		return scriptEscrowWallet{}, entity.ScriptEscrow{}, ErrScriptEscrowInvalid
	}
	return wallet, row, nil
}

func scriptEscrowOperationHash(kind, key string) (string, error) {
	if key == "" || key != strings.TrimSpace(key) || len(key) > 128 {
		return "", ErrScriptEscrowInvalid
	}
	return hashScriptSecret(kind + ":" + key), nil
}

func scriptEscrowReceipt(tx *gorm.DB, scriptID, ownerID int32, kind, operationHash string) (*entity.ScriptEscrowEntry, error) {
	var row entity.ScriptEscrowEntry
	err := tx.Session(&gorm.Session{NewDB: true}).Where("script_id = ? AND user_id = ? AND operation_hash = ?", scriptID, ownerID, operationHash).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.Kind != kind {
		return nil, ErrScriptEscrowConflict
	}
	return &row, nil
}

func storeScriptEscrowMovement(tx *gorm.DB, row entity.ScriptEscrow, wallet scriptEscrowWallet, entry entity.ScriptEscrowEntry, updateEscrow bool) (ScriptEscrowMovement, error) {
	if updateEscrow {
		result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.ScriptEscrow{}).
			Where("script_id = ? AND user_id = ? AND revision = ?", row.ScriptID, row.UserID, row.Revision).
			Updates(map[string]any{"available_micros": entry.EscrowAfterMicros, "revision": row.Revision + 1})
		if result.Error != nil {
			return ScriptEscrowMovement{}, result.Error
		}
		if result.RowsAffected != 1 {
			return ScriptEscrowMovement{}, ErrScriptEscrowConflict
		}
	}
	if entry.WalletAfterMicros != wallet.micros {
		result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
			Where("id = ? AND balance = ?", row.UserID, wallet.raw).
			Update("balance", formatScriptEscrowMicros(entry.WalletAfterMicros))
		if result.Error != nil {
			return ScriptEscrowMovement{}, result.Error
		}
		if result.RowsAffected != 1 {
			return ScriptEscrowMovement{}, ErrScriptEscrowConflict
		}
		observed, err := lockScriptEscrowWallet(tx, row.UserID)
		if err != nil {
			return ScriptEscrowMovement{}, err
		}
		if observed.micros != entry.WalletAfterMicros {
			return ScriptEscrowMovement{}, ErrScriptEscrowInvalid
		}
	}
	entry.ScriptID, entry.UserID, entry.WalletBeforeMicros = row.ScriptID, row.UserID, wallet.micros
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&entry).Error; err != nil {
		return ScriptEscrowMovement{}, err
	}
	return movementFromEscrowEntry(entry, false), nil
}

func parseScriptEscrowMicros(raw string) (int64, error) {
	if len(raw) == 0 || len(raw) > 32 {
		return 0, ErrScriptEscrowInvalid
	}
	for _, c := range raw {
		if (c < '0' || c > '9') && c != '.' && c != '-' && c != '+' && c != 'e' && c != 'E' {
			return 0, ErrScriptEscrowInvalid
		}
	}
	// Only the exponent forms emitted for ordinary numeric SQL columns are
	// needed. Bound it before constructing a rational.
	if exponent := strings.IndexAny(raw, "eE"); exponent >= 0 {
		value, err := strconv.Atoi(raw[exponent+1:])
		if err != nil || value < -30 || value > 30 {
			return 0, ErrScriptEscrowInvalid
		}
	}
	amount, ok := new(big.Rat).SetString(raw)
	if !ok {
		return 0, ErrScriptEscrowInvalid
	}
	amount.Mul(amount, big.NewRat(ScriptEscrowMicrosPerYuan, 1))
	if !amount.IsInt() || !amount.Num().IsInt64() {
		return 0, ErrScriptEscrowInvalid
	}
	micros := amount.Num().Int64()
	if micros < -maxScriptEscrowMicros || micros > maxScriptEscrowMicros {
		return 0, ErrScriptEscrowInvalid
	}
	return micros, nil
}

func formatScriptEscrowMicros(micros int64) string {
	prefix := ""
	if micros < 0 {
		prefix = "-"
		micros = -micros
	}
	return fmt.Sprintf("%s%d.%06d", prefix, micros/ScriptEscrowMicrosPerYuan, micros%ScriptEscrowMicrosPerYuan)
}
