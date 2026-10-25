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
	return token.TotalQuotaLimit > 0 && util.RoundMoney(token.TotalAmountUsed) >= util.RoundMoney(token.TotalQuotaLimit)
}
func accessTokenQuotaWouldExceed(token entity.AccessToken, amount float64) bool {
	return token.TotalQuotaLimit > 0 && amount > 0 && util.RoundMoney(token.TotalAmountUsed+amount) > util.RoundMoney(token.TotalQuotaLimit)
}

// Admission is for a NEW spending request, never owner-scoped paid-task delivery.
// A zero configured limit remains unlimited, not a second wallet.
func checkSearchSpendingAdmission(user entity.User, token entity.AccessToken) error {
	if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") || !strings.EqualFold(strings.TrimSpace(token.Status), "enabled") || token.UserID != user.ID {
		return fmt.Errorf("%w: 访问令牌或所属用户已失效", service.ErrInvalidAccessTokenAuthorization)
	}
	if user.Balance <= 0 {
		return service.ErrInsufficientSearchBalance
	}
	if accessTokenQuotaExhausted(token) {
		return errAccessTokenQuotaExceeded
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
	auth, err := service.ResolveAccessTokenFromBasicAuth(db, authorization)
	if err != nil {
		return err
	}
	return checkSearchSpendingAdmission(auth.User, auth.AccessToken)
}

func buildAIFallbackSpendingRejection(err error) service.AIFallbackResult {
	if errors.Is(err, errAccessTokenQuotaExceeded) {
		return service.BuildAIFallbackQuotaExceededResult(err.Error())
	}
	return service.BuildAIFallbackInsufficientBalanceResult(err.Error())
}
