package controller

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func recordAccountAudit(c *gin.Context, user *entity.User, module string, action int32, description string, result map[string]any) {
	if dao.User == nil {
		return
	}
	event := service.AccountAuditEvent{Module: module, Action: action, Description: description, ClientIP: strings.TrimSpace(c.ClientIP()), Result: result}
	if user != nil {
		event.UserID, event.Username = user.ID, user.Username
	}
	if err := service.WriteAccountAuditWithDB(dao.User.UnderlyingDB(), event); err != nil {
		logger.SysError("账户审计日志写入失败: " + logger.RedactSecrets(err.Error()))
	}
}

func bindSuccessfulVerification(user entity.User, token, scene string, evidence service.SMSVerificationEvidence) {
	if err := service.BindAccountVerificationLogsWithDB(dao.User.UnderlyingDB(), user, token, scene, evidence); err != nil {
		logger.SysError("账户验证码日志关联失败: " + logger.RedactSecrets(err.Error()))
	}
}

func consumeSMSCaptcha(token, scene, clientIP string, input *service.SendSMSCodeInput) error {
	if err := consumeCaptchaTokenWithDB(strings.TrimSpace(token), scene, clientIP); err != nil {
		return err
	}
	correlation, err := service.ConsumedCaptchaCorrelationIDWithDB(dao.User.UnderlyingDB(), token, scene)
	if err != nil {
		logger.SysError("短信前置验证码证据读取失败: " + logger.RedactSecrets(err.Error()))
		return nil
	}
	input.CaptchaCorrelationID = correlation
	return nil
}

// Logout remains available for anonymous/expired sessions to clear their cookie.
func optionalLogoutUser(c *gin.Context) *entity.User {
	if current, ok := c.Get("user"); ok {
		if user, ok := current.(*entity.User); ok && user != nil && user.ID > 0 {
			return user
		}
	}
	if dao.User == nil {
		return nil
	}
	token := strings.TrimSpace(c.GetHeader("Authorization"))
	if token == "" {
		token, _ = c.Cookie(authCookieName)
	}
	claims, err := util.ParseJwtWithClaims(token)
	if err != nil {
		return nil
	}
	subject, err := claims.GetSubject()
	if err != nil {
		return nil
	}
	id, err := strconv.Atoi(subject)
	if err != nil || id <= 0 {
		return nil
	}
	user, err := dao.User.Where(dao.User.ID.Eq(int32(id))).First()
	if err != nil || !isUserEnabled(user) || !tokenCredentialVersionMatches(claims, user.Password) {
		return nil
	}
	return user
}

func auditPhoneValue(phone *string) string {
	if phone == nil {
		return ""
	}
	return service.MaskAuditPhone(*phone)
}

func samePhone(a, b *string) bool {
	if a == nil {
		return b == nil || *b == ""
	}
	if b == nil {
		return *a == ""
	}
	return *a == *b
}
