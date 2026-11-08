package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type searchScriptBillingPolicy struct {
	mode             string
	multiplierMicros int64
}

func (p searchScriptBillingPolicy) managed() bool {
	return p.mode == entity.ScriptBillingModeEscrow || p.mode == entity.ScriptBillingModeActivation
}

// checkResolvedScriptSpendingAdmission is used only at NEW execution
// admission. An owned, already paid idempotent receipt is resolved before
// this check; it must never be turned into a second provider execution.
func checkResolvedScriptSpendingAdmission(db *gorm.DB, user entity.User, token entity.AccessToken, session entity.ScriptRunSession) (searchScriptBillingPolicy, entity.UserScript, error) {
	denied := fmt.Errorf("%w: 脚本运行会话无效", service.ErrInvalidAccessTokenAuthorization)
	var script entity.UserScript
	var policy searchScriptBillingPolicy
	if session.ID <= 0 {
		if token.ScriptID != 0 {
			return policy, script, denied
		}
		return policy, script, checkSearchSpendingAdmission(user, token)
	}
	if db == nil || token.ScriptID <= 0 || session.ScriptID != token.ScriptID ||
		session.UserID != user.ID || session.AccessTokenID != token.ID || session.IdentityHash == "" {
		return policy, script, denied
	}
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("id = ? AND user_id = ? AND access_token_id = ? AND enabled = ? AND deleted_at IS NULL",
			session.ScriptID, user.ID, token.ID, true).Take(&script).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return policy, script, denied
	}
	if err != nil {
		return policy, script, err
	}
	policy.mode = script.BillingMode
	if policy.mode == "" {
		return policy, script, checkSearchSpendingAdmission(user, token)
	}
	// Never let a legacy/unmanaged session acquire a subsidy or paid-script
	// privileges. The service has already checked the credential and source.
	if !policy.managed() || session.LeaseSlot == nil || *session.LeaseSlot != script.ID ||
		session.SourceZoneHash == "" || session.LastHeartbeatAt == nil {
		return policy, script, denied
	}
	if err := checkSearchSpendingAccount(user, token); err != nil {
		return policy, script, err
	}
	if accessTokenQuotaExhausted(token) {
		return policy, script, errAccessTokenQuotaExceeded
	}
	if policy.mode == entity.ScriptBillingModeActivation {
		return policy, script, nil
	}
	var escrow entity.ScriptEscrow
	err = db.Session(&gorm.Session{NewDB: true}).Select("available_micros").
		Where("script_id = ? AND user_id = ?", script.ID, user.ID).Take(&escrow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return policy, script, service.ErrScriptEscrowMissing
	}
	if err != nil {
		return policy, script, err
	}
	if escrow.AvailableMicros < 0 {
		return policy, script, service.ErrScriptEscrowInvalid
	}
	if user.Balance < 0 || (user.Balance == 0 && escrow.AvailableMicros == 0) {
		return policy, script, service.ErrInsufficientSearchBalance
	}
	setting, err := service.GetScriptEscrowPolicyWithDB(db)
	if err != nil {
		return policy, script, err
	}
	policy.multiplierMicros = setting.MultiplierMicros
	return policy, script, nil
}

func multiplyScriptEscrowCharges(charges []searchBillingCharge, multiplierMicros int64) ([]searchBillingCharge, []int64, int64, error) {
	adjusted := append([]searchBillingCharge(nil), charges...)
	micros := make([]int64, len(charges))
	var total int64
	for i := range adjusted {
		base := charges[i].Amount
		n, err := service.ScriptChargeMicros(base, multiplierMicros)
		if err != nil || n > math.MaxInt64-total {
			return nil, nil, 0, service.ErrScriptEscrowInvalid
		}
		micros[i] = n
		total += n
		adjusted[i].Amount = service.ScriptEscrowYuan(n)
		adjusted[i].Snapshot = strings.TrimSpace(adjusted[i].Snapshot + fmt.Sprintf(
			" script_mode=escrow script_multiplier=%.6f script_base_amount=%.8f script_charge_amount=%.6f",
			service.ScriptEscrowYuan(multiplierMicros), base, adjusted[i].Amount))
	}
	return adjusted, micros, total, nil
}

type scriptInternalCostLine struct {
	Provider          string `json:"provider"`
	ModelName         string `json:"modelName,omitempty"`
	InputTokens       int64  `json:"inputTokens,omitempty"`
	CachedInputTokens int64  `json:"cachedInputTokens,omitempty"`
	OutputTokens      int64  `json:"outputTokens,omitempty"`
	TotalTokens       int64  `json:"totalTokens,omitempty"`
	CostMicros        int64  `json:"costMicros"`
}

type scriptInternalExecutionUsage struct {
	ModelResponses    int   `json:"modelResponses,omitempty"`
	InputTokens       int64 `json:"inputTokens,omitempty"`
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`
	OutputTokens      int64 `json:"outputTokens,omitempty"`
	TotalTokens       int64 `json:"totalTokens,omitempty"`
	SearchCalls       int   `json:"searchCalls,omitempty"`
	WebReadCalls      int   `json:"webReadCalls,omitempty"`
	OCRCalls          int   `json:"ocrCalls,omitempty"`
	TikuSearchCalls   int   `json:"tikuSearchCalls,omitempty"`
	CostUnavailable   bool  `json:"costUnavailable,omitempty"`
}

func (u scriptInternalExecutionUsage) present() bool {
	return u != (scriptInternalExecutionUsage{})
}

func (ctx *searchBillingContext) captureScriptExecutionUsage(usage search.AISearchUsage) {
	if ctx.ScriptBilling.mode != entity.ScriptBillingModeActivation {
		return
	}
	ctx.ScriptExecutionUsage = scriptInternalExecutionUsage{
		ModelResponses: len(usage.ModelCalls), InputTokens: usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens, OutputTokens: usage.OutputTokens,
		TotalTokens: usage.TotalTokens, SearchCalls: len(usage.SearchCalls),
		WebReadCalls: len(usage.WebReadCalls), OCRCalls: len(usage.OCRCalls),
		TikuSearchCalls: len(usage.TikuSearchCalls),
	}
}

func persistScriptSubsidizedUsageInTx(tx *gorm.DB, ctx searchBillingContext, charges []searchBillingCharge) error {
	lines := make([]scriptInternalCostLine, 0, len(charges))
	var total int64
	for _, charge := range charges {
		micros, err := service.ScriptChargeMicros(charge.Amount, service.ScriptEscrowMicrosPerYuan)
		if err != nil || micros > math.MaxInt64-total {
			return service.ErrScriptEscrowInvalid
		}
		total += micros
		lines = append(lines, scriptInternalCostLine{
			Provider: charge.Provider, ModelName: charge.ModelName, InputTokens: charge.InputTokens,
			CachedInputTokens: charge.CachedInputTokens, OutputTokens: charge.OutputTokens,
			TotalTokens: charge.TotalTokens, CostMicros: micros,
		})
	}
	payload, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	usage, err := json.Marshal(ctx.ScriptExecutionUsage)
	if err != nil {
		return err
	}
	row := entity.ScriptSubsidizedUsage{
		ScriptID: ctx.ScriptID, RequestID: ctx.RequestID, UserID: ctx.UserID,
		AccessTokenID: ctx.AccessTokenID, CostMicros: total, CostLinesJSON: string(payload), UsageJSON: string(usage),
	}
	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "script_id"}, {Name: "request_id"}}, DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	var existing entity.ScriptSubsidizedUsage
	if err := tx.Where("script_id = ? AND request_id = ?", row.ScriptID, row.RequestID).Take(&existing).Error; err != nil {
		return err
	}
	if existing.UserID != row.UserID || existing.AccessTokenID != row.AccessTokenID ||
		existing.CostMicros != row.CostMicros || existing.CostLinesJSON != row.CostLinesJSON || existing.UsageJSON != row.UsageJSON {
		return errSearchBillingRetryConflict
	}
	return nil
}
