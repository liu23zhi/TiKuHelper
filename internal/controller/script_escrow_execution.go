package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func beginScriptEscrowExecution(ctx context.Context, policy searchScriptBillingPolicy, scriptID int32, sessionID int64, requestID string) (bool, error) {
	if policy.mode != entity.ScriptBillingModeEscrow {
		return false, nil
	}
	if sessionID <= 0 {
		return false, service.ErrInvalidAccessTokenAuthorization
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, searchBillingRetryAttemptLimit)
	defer cancel()
	db := withDatabasePriority(resolveSearchPersistenceDB(), ctx, dbqueue.PriorityHigh)
	if db == nil {
		return false, service.ErrScriptEscrowMissing
	}
	if err := service.BeginScriptEscrowWorkWithDB(db, scriptID, requestID, sessionID); err != nil {
		if errors.Is(err, service.ErrScriptEscrowWorkDenied) || errors.Is(err, service.ErrScriptEscrowClosePending) {
			return false, fmt.Errorf("%w: 脚本已关停", service.ErrInvalidAccessTokenAuthorization)
		}
		return false, err
	}
	return true, nil
}

func escrowSettlementDB(ctx context.Context, tracked bool) (*gorm.DB, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	cancel := context.CancelFunc(func() {})
	if tracked {
		// A client deadline is not evidence that its already-admitted
		// provider call had no cost. Bound the independent financial write.
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), searchBillingRetryAttemptLimit)
	}
	return withDatabasePriority(resolveSearchPersistenceDB(), ctx, dbqueue.PriorityHigh), cancel
}

func completeSettledEscrowExecution(tx *gorm.DB, ctx searchBillingContext) error {
	if !ctx.escrowWorkTracked || ctx.escrowWorkUncertain {
		return nil
	}
	if ctx.ScriptBilling.mode != entity.ScriptBillingModeEscrow {
		return service.ErrScriptEscrowInvalid
	}
	return service.CompleteScriptEscrowWorkInTx(tx, ctx.ScriptID, ctx.RequestID)
}

func aiFallbackEscrowOutcomeUncertain(result service.AIFallbackResult) bool {
	if result.Success {
		return false
	}
	if result.Error == nil {
		return true
	}
	switch result.Error.Code {
	case service.AIFallbackErrorCodeAIUnavailable, service.AIFallbackErrorCodeInvalidInput,
		service.AIFallbackErrorCodeNoAnswer, service.AIFallbackErrorCodeUnsafeToAnswer:
		// These are explicit final no-provider or no-answer decisions.
		// A result can still carry known chargeable usage, which is billed.
		return false
	default:
		return true
	}
}
