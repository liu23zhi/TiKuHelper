package controller

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
)

var errAccessTokenQuotaExceeded = errors.New("访问令牌累计额度不足")
var errSearchBillingCompareAndSwap = errors.New("计费账户已并发变更，请重试")

func accessTokenQuotaExhausted(token entity.AccessToken) bool {
	if !service.AccessTokenQuotaLimitValid(token.TotalQuotaLimit) {
		return true
	}
	return token.TotalQuotaLimit > 0 && util.RoundMoney(token.TotalAmountUsed) >= util.RoundMoney(token.TotalQuotaLimit)
}
func accessTokenQuotaWouldExceed(token entity.AccessToken, amount float64) bool {
	if !service.AccessTokenQuotaLimitValid(token.TotalQuotaLimit) {
		return true
	}
	return token.TotalQuotaLimit > 0 && amount > 0 && util.RoundMoney(token.TotalAmountUsed+amount) > util.RoundMoney(token.TotalQuotaLimit)
}

// Admission is for a NEW spending request, never owner-scoped paid-task delivery.
// A zero configured limit remains unlimited, not a second wallet.
//
// 双桶模型（43-*.md §2）：可花费的额度 = 本金 + 赠金。旧判据只看 Balance，
// 在「本金 0、赠金 1」时会把有钱的账户误拒；而本金可能为负（欠款），
// 只看 Balance 又会在「本金 −1、赠金 2」时错判 —— 两处都必须用合计。
func checkSearchSpendingAdmission(user entity.User, token entity.AccessToken) error {
	if err := checkSearchSpendingAccount(user, token); err != nil {
		return err
	}
	if service.WalletAvailableTotal(user.Balance, user.GiftBalance) <= 0 {
		return service.ErrInsufficientSearchBalance
	}
	if accessTokenQuotaExhausted(token) {
		return errAccessTokenQuotaExceeded
	}
	return nil
}

func checkSearchSpendingAccount(user entity.User, token entity.AccessToken) error {
	if !isUserSpendable(&user) || !strings.EqualFold(strings.TrimSpace(token.Status), "enabled") || token.UserID != user.ID {
		return fmt.Errorf("%w: 访问令牌或所属用户已失效", service.ErrInvalidAccessTokenAuthorization)
	}
	return nil
}

func checkNewSearchTaskSpending(c *gin.Context) error {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if authorization == "" {
		return fmt.Errorf("%w: 访问令牌不能为空", service.ErrInvalidAccessTokenAuthorization)
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		return errors.New("搜题服务暂不可用")
	}
	auth, session, err := service.ResolveSearchTokenWithScriptIdentity(db, authorization, c.GetHeader("X-Script-Identity"), c.ClientIP())
	if err != nil {
		return err
	}
	_, _, err = checkResolvedScriptSpendingAdmission(db, auth.User, auth.AccessToken, session)
	return err
}

func buildAIFallbackSpendingRejection(err error) service.AIFallbackResult {
	if errors.Is(err, errAccessTokenQuotaExceeded) {
		return service.BuildAIFallbackQuotaExceededResult(err.Error())
	}
	return service.BuildAIFallbackInsufficientBalanceResult(err.Error())
}
