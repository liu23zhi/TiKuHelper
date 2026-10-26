package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const authCookieName = "tiku_token"

var sendPublicSMSCodeWithDB = func(input service.SendSMSCodeInput) error {
	return service.SendSMSCodeWithDB(dao.User.UnderlyingDB(), input)
}

var sendUserPhoneChangeSMSCodeWithDB = func(input service.SendSMSCodeInput) error {
	return service.SendSMSCodeWithDB(dao.User.UnderlyingDB(), input)
}

var updateUserPhoneWithVerificationWithDB = func(userID int32, phone string, smsCode string) (entity.User, error) {
	return service.UpdateUserPhoneWithVerificationWithDB(dao.User.UnderlyingDB(), userID, phone, smsCode)
}

var bindCurrentUserWechatWithDB = func(bindToken string, userID int32) (entity.User, error) {
	return service.BindWechatToUserWithDB(dao.User.UnderlyingDB(), nil, bindToken, userID)
}

var bindCurrentUserWechatByCodeWithDB = func(input service.WechatCodeBindInput) (entity.User, error) {
	return service.BindWechatCodeToUserWithDB(dao.User.UnderlyingDB(), input.Client, input.Code, input.UserID)
}

var unbindCurrentUserWechatWithDB = func(userID int32) (entity.User, error) {
	return service.UnbindWechatFromUserWithDB(dao.User.UnderlyingDB(), userID)
}

var verifySMSCodeWithDB = func(phone string, scene string, smsCode string) (service.SMSVerificationEvidence, error) {
	return service.VerifySMSCodeDetailedWithDB(dao.User.UnderlyingDB(), phone, scene, smsCode)
}

var consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
	return service.ConsumeCaptchaTokenWithDB(dao.User.UnderlyingDB(), token, scene, requestIP)
}

var consumeRegisterCaptchaTokenInTx = func(tx *gorm.DB, token string, scene string, requestIP string) error {
	return service.ConsumeCaptchaTokenInTx(tx, token, scene, requestIP)
}

var verifyRegisterSMSCodeInTx = func(tx *gorm.DB, phone string, scene string, smsCode string) (service.SMSVerificationEvidence, error) {
	return service.VerifySMSCodeDetailedInTx(tx, phone, scene, smsCode)
}

func isUserEnabled(user *entity.User) bool {
	status := strings.TrimSpace(user.Status)
	return status == "" || strings.EqualFold(status, "enabled")
}

func isAdminUser(user *entity.User) bool {
	return user != nil && strings.EqualFold(strings.TrimSpace(user.Role), "admin")
}

func normalizeUserRole(role string) string {
	if strings.EqualFold(strings.TrimSpace(role), "admin") {
		return "admin"
	}
	return "user"
}

func normalizeUserStatus(status string) string {
	if strings.EqualFold(strings.TrimSpace(status), "disabled") {
		return "disabled"
	}
	return "enabled"
}

func validatePasswordComplexity(password string) error {
	trimmed := strings.TrimSpace(password)
	if trimmed == "" {
		return fmt.Errorf("密码不能为空")
	}
	if err := validateBcryptPasswordLength(trimmed); err != nil {
		return err
	}
	if len([]rune(trimmed)) < 8 {
		return fmt.Errorf("密码至少需要 8 位，且需包含大小写字母、数字和特殊字符")
	}

	hasUpper := false
	hasLower := false
	hasDigit := false
	hasSpecial := false
	for _, ch := range trimmed {
		switch {
		case ch >= 'A' && ch <= 'Z':
			hasUpper = true
		case ch >= 'a' && ch <= 'z':
			hasLower = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit || !hasSpecial {
		return fmt.Errorf("密码至少需要 8 位，且需包含大小写字母、数字和特殊字符")
	}
	return nil
}

func validateAdminManagedPassword(password string) error {
	trimmed := strings.TrimSpace(password)
	if trimmed == "" {
		return fmt.Errorf("密码不能为空")
	}
	return validateBcryptPasswordLength(trimmed)
}

func validateBcryptPasswordLength(password string) error {
	if err := util.ValidateBcryptPasswordBytes(password); err != nil {
		return fmt.Errorf("密码不能超过 %d 字节", util.BcryptMaxPasswordBytes)
	}
	return nil
}

// Auth 鉴权
func Auth(c *gin.Context) {
	tokenValue := strings.TrimSpace(c.Request.Header.Get("Authorization"))
	if tokenValue == "" {
		cookieValue, err := c.Cookie(authCookieName)
		if err == nil {
			tokenValue = strings.TrimSpace(cookieValue)
		}
	}
	claims, err := util.ParseJwtWithClaims(tokenValue)

	if err != nil {
		logger.SysLog(err.Error())
		c.AbortWithStatusJSON(401, gin.H{
			"message": "请重新登录",
		})
		return
	}

	subject, err := claims.GetSubject()
	if err != nil {
		logger.SysLog(err.Error())
		c.AbortWithStatusJSON(401, gin.H{
			"message": "请重新登录",
		})
		return
	}

	userID, err := strconv.Atoi(subject)
	if err != nil || userID <= 0 {
		c.AbortWithStatusJSON(401, gin.H{"message": "请重新登录"})
		return
	}

	user, err := dao.User.Where(dao.User.ID.Eq(int32(userID))).First()
	if err != nil {
		logger.SysLog(err.Error())
		c.AbortWithStatusJSON(401, gin.H{
			"message": "请重新登录",
		})
		return
	}
	if !isUserEnabled(user) {
		c.AbortWithStatusJSON(401, gin.H{
			"message": "用户已被禁用",
		})
		return
	}
	if !tokenCredentialVersionMatches(claims, user.Password) {
		c.AbortWithStatusJSON(401, gin.H{"message": "请重新登录"})
		return
	}
	c.Set("user", user)
	c.Next()
}

// AuthOrAccessToken accepts the access tokens used by browser integrations, while
// preserving the existing login-JWT behavior for other protected endpoints.
func AuthOrAccessToken(c *gin.Context) {
	authorization := strings.TrimSpace(c.Request.Header.Get("Authorization"))
	if authorization != "" {
		authCtx, err := service.ResolveAccessTokenFromBasicAuth(dao.User.UnderlyingDB(), authorization)
		if err == nil {
			user := authCtx.User
			c.Set("user", &user)
			c.Set("access_token", &authCtx.AccessToken)
			c.Next()
			return
		}
	}

	Auth(c)
}

// AuthAccessToken requires a valid access token and never falls back to login JWTs or cookies.
func AuthAccessToken(c *gin.Context) {
	authorization := strings.TrimSpace(c.Request.Header.Get("Authorization"))
	if authorization == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "请提供有效访问令牌"})
		return
	}

	authCtx, err := service.ResolveAccessTokenFromBasicAuth(dao.User.UnderlyingDB(), authorization)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "请提供有效访问令牌"})
		return
	}

	user := authCtx.User
	c.Set("user", &user)
	c.Set("access_token", &authCtx.AccessToken)
	c.Next()
}

// UserInfo 获取用户信息
func UserInfo(c *gin.Context) {
	v, _ := c.Get(`user`)
	user, ok := v.(*entity.User)
	if !ok || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}

	// 普通用户也需要知道当前生效的充值下限，避免管理员调整后页面仍按旧默认值提交。
	// 读取失败时使用服务层默认值，不影响用户信息接口本身。
	minimum := service.GetMinRechargeAmountWithDB(dao.User.UnderlyingDB())
	c.JSON(http.StatusOK, struct {
		*entity.User
		MinRechargeAmount float64 `json:"minRechargeAmount"`
	}{User: user, MinRechargeAmount: minimum})
}

// UserLogin 用户登录
func UserLogin(c *gin.Context) {
	var payload struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		CaptchaToken string `json:"captchaToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if err := util.ValidateBcryptPasswordBytes(payload.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户名或密码错误"})
		return
	}

	captchaToken := strings.TrimSpace(payload.CaptchaToken)
	if err := consumeCaptchaTokenWithDB(captchaToken, service.CaptchaSceneLoginPassword, strings.TrimSpace(c.ClientIP())); err != nil {
		c.JSON(400, gin.H{
			"message": err.Error(),
		})
		return
	}

	us := dao.User
	user, err := us.Where(us.Username.Eq(strings.TrimSpace(payload.Username))).First()
	if err != nil {
		c.JSON(400, gin.H{
			"message": "用户名或密码错误",
		})
		return
	}
	passwordMatches, needsUpgrade := util.VerifyPassword(user.Password, payload.Password)
	if !passwordMatches {
		c.JSON(400, gin.H{
			"message": "用户名或密码错误",
		})
		return
	}
	if !isUserEnabled(user) {
		c.JSON(400, gin.H{
			"message": "用户已被禁用",
		})
		return
	}
	jwt := util.GenerateJwtForUser(user.ID, user.Password)

	if jwt == "" {
		c.JSON(400, gin.H{
			"message": "jwt生成失败",
		})
	} else {
		if needsUpgrade {
			if hashedPassword, hashErr := util.HashPassword(payload.Password); hashErr == nil {
				_ = dao.User.UnderlyingDB().Model(&entity.User{}).Where("id = ?", user.ID).Update("password", hashedPassword).Error
				user.Password = hashedPassword
			}
		}
		now := time.Now()
		clientIP := strings.TrimSpace(c.ClientIP())
		if err := dao.User.UnderlyingDB().Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"last_login_at": now,
			"last_login_ip": clientIP,
		}).Error; err != nil {
			c.JSON(400, gin.H{
				"message": "登录信息更新失败",
			})
			return
		}
		user.LastLoginAt = &now
		user.LastLoginIP = clientIP
		bindSuccessfulVerification(*user, captchaToken, service.CaptchaSceneLoginPassword, service.SMSVerificationEvidence{})
		recordAccountAudit(c, user, "auth", logActionLogin, "用户登录成功", map[string]any{"status": "success", "message": "用户登录成功", "scene": "password"})
		setAuthCookie(c, jwt)
		c.JSON(200, gin.H{
			"message": "user login",
			"jwt":     jwt,
		})
	}
}

// UserRegister 公开注册
func UserRegister(c *gin.Context) {
	enabled, err := service.GetRegisterEnabledWithDB(dao.User.UnderlyingDB())
	if err != nil {
		c.JSON(400, gin.H{
			"message": "注册配置读取失败",
		})
		return
	}
	if !enabled {
		c.JSON(400, gin.H{
			"message": "未开放注册",
		})
		return
	}

	var payload struct {
		Username              string `json:"username"`
		Password              string `json:"password"`
		Nickname              string `json:"nickname"`
		Phone                 string `json:"phone"`
		SMSCode               string `json:"smsCode"`
		CaptchaToken          string `json:"captchaToken"`
		RegistrationFlowToken string `json:"registrationFlowToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}
	username := strings.TrimSpace(payload.Username)
	password := strings.TrimSpace(payload.Password)
	nickname := strings.TrimSpace(payload.Nickname)
	rawPhone := payload.Phone
	smsCode := strings.TrimSpace(payload.SMSCode)
	if username == "" || password == "" {
		c.JSON(400, gin.H{
			"message": "用户名和密码不能为空",
		})
		return
	}
	if err := validateAdminManagedPassword(password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err := util.ValidateRestrictedName(username, util.UsernameMaxLength, "用户名"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if rawPhone == "" || smsCode == "" {
		c.JSON(400, gin.H{
			"message": "手机号和验证码不能为空",
		})
		return
	}
	if err := validatePasswordComplexity(password); err != nil {
		c.JSON(400, gin.H{
			"message": err.Error(),
		})
		return
	}
	phone, err := service.NormalizeMainlandPhoneInput(rawPhone)
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	registrationFlowToken := strings.TrimSpace(payload.RegistrationFlowToken)
	var smsEvidence service.SMSVerificationEvidence
	if registrationFlowToken != "" {
		hashedPassword, err := util.HashPassword(password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "注册失败"})
			return
		}
		now := time.Now()
		user := entity.User{
			Username:       username,
			Password:       hashedPassword,
			Nickname:       nickname,
			Role:           "user",
			Status:         "enabled",
			Balance:        0,
			RegisterSource: "public",
			ParentID:       0,
			Perms:          "",
			Phone:          &phone,
			PhoneBoundAt:   &now,
			RegisterIP:     strings.TrimSpace(c.ClientIP()),
		}
		db := dao.User.UnderlyingDB()
		err = wechatRegisterFlowStore.Consume(registrationFlowToken, time.Now(), func(wechatID string) error {
			boundAt := time.Now()
			user.WechatID = &wechatID
			user.WechatBoundAt = &boundAt
			return db.Transaction(func(tx *gorm.DB) error {
				if err := service.ConsumeCaptchaTokenInTx(tx, strings.TrimSpace(payload.CaptchaToken), service.CaptchaSceneRegisterSubmit, strings.TrimSpace(c.ClientIP())); err != nil {
					return err
				}
				evidence, err := service.VerifySMSCodeDetailedInTx(tx, phone, service.SMSSceneRegister, smsCode)
				if err != nil {
					return err
				}
				smsEvidence = evidence
				bonusAmount, err := service.GetNewUserBonusAmountWithDB(tx)
				if err != nil {
					return err
				}
				return service.CreateUserWithNewUserBonusInTx(tx, &user, service.NewUserBonusOptions{
					Enabled: true,
					Amount:  bonusAmount,
					Source:  service.RegistrationBonusSourcePublicRegister,
				})
			})
		})
		if err != nil {
			status := http.StatusBadRequest
			message := err.Error()
			if errors.Is(err, service.ErrWechatRegisterFlowInvalid) {
				status = http.StatusUnauthorized
			} else if errors.Is(err, service.ErrWechatRegisterFlowExpired) {
				status = http.StatusGone
			} else if isPhoneBindingError(err) {
				_ = wechatRegisterFlowStore.Release(registrationFlowToken)
				status = http.StatusConflict
			} else if isUserRegisterConflictError(err) {
				_ = wechatRegisterFlowStore.Release(registrationFlowToken)
				status = http.StatusConflict
				message = "用户名、手机号或微信已绑定"
			}
			c.JSON(status, gin.H{"message": message})
			return
		}
		bindSuccessfulVerification(user, payload.CaptchaToken, service.CaptchaSceneRegisterSubmit, smsEvidence)
		recordAccountAudit(c, &user, "auth", 12, "用户注册成功", map[string]any{"status": "success", "message": "用户注册成功", "scene": "wechat_register"})
		c.JSON(http.StatusOK, user)
		return
	}
	// 检测用户名是否被使用
	_, err = dao.User.Where(dao.User.Username.Eq(username)).First()
	if err == nil {
		c.JSON(400, gin.H{
			"message": "用户名已经被使用",
		})
		return
	}
	now := time.Now()
	hashedPassword, err := util.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "注册失败"})
		return
	}
	user := entity.User{
		Username:       username,
		Password:       hashedPassword,
		Nickname:       nickname,
		Role:           "user",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "public",
		ParentID:       0,
		Perms:          "",
		Phone:          &phone,
		PhoneBoundAt:   &now,
		// 记录注册来源 IP，便于在用户管理里核验账号来源。
		RegisterIP: strings.TrimSpace(c.ClientIP()),
	}

	failureMessage := "注册失败"
	err = dao.User.UnderlyingDB().Transaction(func(tx *gorm.DB) error {
		if err := consumeRegisterCaptchaTokenInTx(tx, strings.TrimSpace(payload.CaptchaToken), service.CaptchaSceneRegisterSubmit, strings.TrimSpace(c.ClientIP())); err != nil {
			failureMessage = err.Error()
			return err
		}
		evidence, err := verifyRegisterSMSCodeInTx(tx, phone, service.SMSSceneRegister, smsCode)
		if err != nil {
			failureMessage = err.Error()
			return err
		}
		smsEvidence = evidence
		bonusAmount, err := service.GetNewUserBonusAmountWithDB(tx)
		if err != nil {
			failureMessage = "新用户赠送配置读取失败"
			return err
		}
		err = service.CreateUserWithNewUserBonusInTx(tx, &user, service.NewUserBonusOptions{
			Enabled: true,
			Amount:  bonusAmount,
			Source:  service.RegistrationBonusSourcePublicRegister,
		})
		if isPhoneBindingError(err) {
			failureMessage = err.Error()
		}
		return err
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": failureMessage})
		return
	}
	bindSuccessfulVerification(user, payload.CaptchaToken, service.CaptchaSceneRegisterSubmit, smsEvidence)
	recordAccountAudit(c, &user, "auth", 12, "用户注册成功", map[string]any{"status": "success", "message": "用户注册成功", "scene": "register"})
	c.JSON(200, user)
}

func isUserRegisterConflictError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unique constraint failed", // SQLite
		"duplicate entry",          // MySQL/MariaDB
		"duplicate key",            // MySQL/Postgres
		"unique violation",         // Postgres
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isPhoneBindingError(err error) bool {
	return errors.Is(err, service.ErrPhoneAlreadyBound) || errors.Is(err, service.ErrPhoneBindingConflict)
}

func SendPublicSMSCode(c *gin.Context) {
	var payload struct {
		Phone        string `json:"phone"`
		Scene        string `json:"scene"`
		CaptchaToken string `json:"captchaToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	phone, err := service.NormalizeMainlandPhoneInput(payload.Phone)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	captchaScene := service.CaptchaSceneSMSSendRegister
	if strings.TrimSpace(payload.Scene) == service.SMSSceneLogin {
		captchaScene = service.CaptchaSceneSMSSendLogin
	}
	input := service.SendSMSCodeInput{
		Phone:     phone,
		Scene:     strings.TrimSpace(payload.Scene),
		RequestIP: strings.TrimSpace(c.ClientIP()),
	}
	sendSMSWithIdempotency(c, "public-sms", "send-code", payload,
		func() error {
			return consumeSMSCaptcha(payload.CaptchaToken, captchaScene, input.RequestIP, &input)
		},
		func() error { return sendPublicSMSCodeWithDB(input) })
}

func SendUserPhoneChangeSMSCode(c *gin.Context) {
	operator, ok := c.Get("user")
	if !ok || operator == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	currentUser, ok := operator.(*entity.User)
	if !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}

	var payload struct {
		Phone        string `json:"phone"`
		CaptchaToken string `json:"captchaToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	phone, err := service.NormalizeMainlandPhoneInput(payload.Phone)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	input := service.SendSMSCodeInput{
		Phone:        phone,
		Scene:        service.SMSSceneChangePhone,
		RequestIP:    strings.TrimSpace(c.ClientIP()),
		TargetUserID: currentUser.ID,
	}
	sendSMSWithIdempotency(c, fmt.Sprintf("user:%d", currentUser.ID), "sms-change-phone", payload,
		func() error {
			return consumeSMSCaptcha(payload.CaptchaToken, service.CaptchaSceneSMSSendChangePhone, input.RequestIP, &input)
		},
		func() error { return sendUserPhoneChangeSMSCodeWithDB(input) })
}

func sendSMSWithIdempotency(c *gin.Context, scope, operation string, payload any, consumeCaptcha, send func() error) {
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		if err := consumeCaptcha(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		if err := send(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "发送成功，请注意查收"})
		return
	}
	request, err := json.Marshal(payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	db := dao.User.UnderlyingDB()
	record, fresh, err := service.ClaimIdempotencyWithDB(db, scope, operation, key, request)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrIdempotencyConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, service.ErrIdempotencyKeyInvalid) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"message": err.Error()})
		return
	}
	if !fresh {
		if record.Status == service.IdempotencyStatusSucceeded {
			c.Data(record.HTTPStatus, "application/json; charset=utf-8", []byte(record.ResponseJSON))
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "pending", "message": "短信发送结果待核对，请勿重复发送"})
		return
	}
	if err := consumeCaptcha(); err != nil {
		_ = service.ReleaseIdempotencyWithDB(db, record.ID)
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err := send(); err != nil {
		_ = service.MarkIdempotencyUnknownWithDB(db, record.ID)
		c.JSON(http.StatusAccepted, gin.H{"status": "pending", "message": "短信发送结果待核对，请勿重复发送"})
		return
	}
	response := []byte(`{"message":"发送成功，请注意查收"}`)
	if err := service.CompleteIdempotencyWithDB(db, record.ID, http.StatusOK, response); err != nil {
		_ = service.MarkIdempotencyUnknownWithDB(db, record.ID)
		c.JSON(http.StatusAccepted, gin.H{"status": "pending", "message": "短信发送结果待核对，请勿重复发送"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", response)
}

func UpdateCurrentUserPhone(c *gin.Context) {
	operator, ok := c.Get("user")
	if !ok || operator == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	currentUser, ok := operator.(*entity.User)
	if !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}

	var payload struct {
		Phone   string `json:"phone"`
		SMSCode string `json:"smsCode"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if strings.TrimSpace(payload.Phone) == "" || strings.TrimSpace(payload.SMSCode) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "手机号和验证码不能为空"})
		return
	}
	phone, err := service.NormalizeMainlandPhoneInput(payload.Phone)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	updated, err := updateUserPhoneWithVerificationWithDB(currentUser.ID, phone, strings.TrimSpace(payload.SMSCode))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if !samePhone(currentUser.Phone, updated.Phone) {
		recordAccountAudit(c, &updated, "user", 15, "用户修改手机号", map[string]any{"status": "success", "message": "手机号修改成功", "targetUserId": updated.ID, "before": map[string]any{"phone": auditPhoneValue(currentUser.Phone)}, "after": map[string]any{"phone": auditPhoneValue(updated.Phone)}})
	}
	c.JSON(http.StatusOK, updated)
}

// UpdateCurrentUserProfile updates only the authenticated user's username and nickname.
func UpdateCurrentUserProfile(c *gin.Context) {
	operator, ok := c.Get("user")
	if !ok || operator == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	currentUser, ok := operator.(*entity.User)
	if !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}

	var payload struct {
		Username string `json:"username"`
		Nickname string `json:"nickname"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	updated, err := service.UpdateCurrentUserProfileWithDB(dao.User.UnderlyingDB(), currentUser.ID, service.UpdateUserProfileInput{
		Username: payload.Username,
		Nickname: payload.Nickname,
	})
	if err != nil {
		if errors.Is(err, service.ErrUserProfileUsernameTaken) || isUserRegisterConflictError(err) {
			c.JSON(http.StatusConflict, gin.H{"message": "用户名已经被使用"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if currentUser.Username != updated.Username || currentUser.Nickname != updated.Nickname {
		recordAccountAudit(c, &updated, "user", 16, "用户修改资料", map[string]any{"status": "success", "message": "用户资料修改成功", "targetUserId": updated.ID, "before": map[string]any{"username": currentUser.Username, "nickname": currentUser.Nickname}, "after": map[string]any{"username": updated.Username, "nickname": updated.Nickname}})
	}
	c.JSON(http.StatusOK, updated)
}

func BindCurrentUserWechat(c *gin.Context) {
	operator, ok := c.Get("user")
	if !ok || operator == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	currentUser, ok := operator.(*entity.User)
	if !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	var payload struct {
		BindToken string `json:"bindToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	updated, err := bindCurrentUserWechatWithDB(strings.TrimSpace(payload.BindToken), currentUser.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

func BindCurrentUserWechatByCode(c *gin.Context) {
	operator, ok := c.Get("user")
	if !ok || operator == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	currentUser, ok := operator.(*entity.User)
	if !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || strings.TrimSpace(payload.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	settings, err := loadWechatLoginSettings()
	if err != nil || !settings.WechatLoginEnabled || !settings.WechatCodeLoginEnabled || strings.TrimSpace(settings.WechatAPIBaseURL) == "" || strings.TrimSpace(settings.WechatAPIBearerToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "微信登录未开启或配置未完成"})
		return
	}
	updated, err := bindCurrentUserWechatByCodeWithDB(service.WechatCodeBindInput{
		Client: buildWechatLoginClientFromSettings(settings),
		Code:   strings.TrimSpace(payload.Code),
		UserID: currentUser.ID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

func UnbindCurrentUserWechat(c *gin.Context) {
	operator, ok := c.Get("user")
	if !ok || operator == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	currentUser, ok := operator.(*entity.User)
	if !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	updated, err := unbindCurrentUserWechatWithDB(currentUser.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

func UserSMSLogin(c *gin.Context) {
	var payload struct {
		Phone        string `json:"phone"`
		SMSCode      string `json:"smsCode"`
		CaptchaToken string `json:"captchaToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	phone, err := service.NormalizeMainlandPhoneInput(payload.Phone)
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	if err := consumeCaptchaTokenWithDB(strings.TrimSpace(payload.CaptchaToken), service.CaptchaSceneLoginSMS, strings.TrimSpace(c.ClientIP())); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	users, err := service.FindUsersByPhoneAliases(dao.User.UnderlyingDB(), phone)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "手机号绑定查询失败"})
		return
	}
	if len(users) > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"message": service.ErrPhoneBindingConflict.Error()})
		return
	}
	if len(users) == 0 {
		c.JSON(400, gin.H{"message": "该手机号未绑定账号，请先注册"})
		return
	}
	user := users[0]
	if !isUserEnabled(&user) {
		c.JSON(400, gin.H{"message": "用户已被禁用"})
		return
	}
	smsEvidence, err := verifySMSCodeWithDB(phone, service.SMSSceneLogin, payload.SMSCode)
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	jwt := util.GenerateJwtForUser(user.ID, user.Password)
	if jwt == "" {
		c.JSON(400, gin.H{"message": "jwt生成失败"})
		return
	}
	clientIP := strings.TrimSpace(c.ClientIP())
	now := time.Now()
	if err := dao.User.UnderlyingDB().Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"last_login_at": now,
		"last_login_ip": clientIP,
	}).Error; err != nil {
		c.JSON(400, gin.H{"message": "登录信息更新失败"})
		return
	}
	user.LastLoginAt = &now
	user.LastLoginIP = clientIP
	bindSuccessfulVerification(user, payload.CaptchaToken, service.CaptchaSceneLoginSMS, smsEvidence)
	recordAccountAudit(c, &user, "auth", logActionLogin, "用户短信登录成功", map[string]any{"status": "success", "message": "用户短信登录成功", "scene": "sms"})
	setAuthCookie(c, jwt)
	c.JSON(200, gin.H{"message": "user login", "jwt": jwt})
}

func UserLogout(c *gin.Context) {
	if user := optionalLogoutUser(c); user != nil {
		recordAccountAudit(c, user, "auth", 13, "用户退出登录", map[string]any{"status": "success", "message": "用户退出登录"})
	}
	clearAuthCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

// UserList 用户列表
//
// 支持关键词搜索（用户名 / 昵称 / 手机号 / 微信 ID / 用户 ID）、角色与状态筛选，
// 并返回分页结果 {data, total, page, pageSize}。
func UserList(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{
			"message": "无权限查看用户",
		})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}

	query := buildUserListQuery(dao.User.UnderlyingDB().Model(&entity.User{}), c.Query("keyword"), c.Query("role"), c.Query("status"))

	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(400, gin.H{
			"message": "获取用户列表失败",
		})
		return
	}

	var users []entity.User
	offset := (page - 1) * pageSize
	if err := query.Order("id asc").Limit(pageSize).Offset(offset).Find(&users).Error; err != nil {
		c.JSON(400, gin.H{
			"message": "获取用户列表失败",
		})
		return
	}

	c.JSON(200, gin.H{
		"data":     users,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// buildUserListQuery 组装用户列表的筛选条件。
// 抽成独立函数便于单测覆盖关键词转义与筛选组合。
func buildUserListQuery(query *gorm.DB, keyword string, role string, status string) *gorm.DB {
	if trimmedRole := strings.TrimSpace(role); trimmedRole != "" {
		query = query.Where("role = ?", trimmedRole)
	}
	if trimmedStatus := strings.TrimSpace(status); trimmedStatus != "" {
		query = query.Where("status = ?", trimmedStatus)
	}

	// 关键词按「纯子串」匹配（不使用 LIKE 通配符），见 util.BuildInstrCondition。
	condition, args := util.BuildInstrCondition(keyword,
		"COALESCE(username, '')",
		"COALESCE(nickname, '')",
		"COALESCE(phone, '')",
		"COALESCE(wechat_id, '')",
	)
	if condition == "" {
		return query
	}
	return query.Where(condition, args...)
}

// CreateUser 创建用户
func CreateUser(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{
			"message": "无权限创建用户",
		})
		return
	}

	var payload struct {
		Username       string   `json:"username"`
		Password       string   `json:"password"`
		Nickname       string   `json:"nickname"`
		Role           string   `json:"role"`
		Status         string   `json:"status"`
		Balance        float64  `json:"balance"`
		RegisterSource string   `json:"registerSource"`
		Phone          *string  `json:"phone"`
		Perms          string   `json:"perms"`
		GiftEnabled    *bool    `json:"giftEnabled"`
		GiftAmount     *float64 `json:"giftAmount"`
	}
	err := c.ShouldBindJSON(&payload)
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	user := entity.User{
		Username:       payload.Username,
		Password:       payload.Password,
		Nickname:       payload.Nickname,
		Role:           payload.Role,
		Status:         payload.Status,
		Balance:        payload.Balance,
		RegisterSource: payload.RegisterSource,
		Phone:          payload.Phone,
		Perms:          payload.Perms,
		// 后台建号记录的是**管理员当时的 IP**；这与「账号来源」语义一致：
		// 该账号确实是在这个地址被创建的。
		RegisterIP: strings.TrimSpace(c.ClientIP()),
	}
	user.Username = strings.TrimSpace(user.Username)
	user.Password = strings.TrimSpace(user.Password)
	user.Nickname = strings.TrimSpace(user.Nickname)
	if user.Phone != nil {
		if strings.TrimSpace(*user.Phone) == "" {
			user.Phone = nil
		} else {
			phone, err := service.NormalizeMainlandPhoneInput(*user.Phone)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
				return
			}
			user.Phone = &phone
			now := time.Now()
			user.PhoneBoundAt = &now
		}
	}
	if user.Username == "" || user.Password == "" {
		c.JSON(400, gin.H{
			"message": "用户名和密码不能为空",
		})
		return
	}
	if err := util.ValidateRestrictedName(user.Username, util.UsernameMaxLength, "用户名"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err := validateAdminManagedPassword(user.Password); err != nil {
		c.JSON(400, gin.H{
			"message": err.Error(),
		})
		return
	}

	// 检测用户名是否被使用
	_, err = dao.User.Where(dao.User.Username.Eq(user.Username)).First()
	if err == nil {
		c.JSON(400, gin.H{
			"message": "用户名已经被使用",
		})
		return
	}

	user.ParentID = currentUser.ID
	user.Role = normalizeUserRole(user.Role)
	user.Status = normalizeUserStatus(user.Status)
	if strings.TrimSpace(user.RegisterSource) == "" {
		user.RegisterSource = "admin"
	}
	hashedPassword, err := util.HashPassword(user.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "创建用户失败"})
		return
	}
	user.Password = hashedPassword

	defaultBonusAmount, err := service.GetNewUserBonusAmountWithDB(dao.User.UnderlyingDB())
	if err != nil {
		c.JSON(400, gin.H{"message": "新用户赠送配置读取失败"})
		return
	}
	giftEnabled := true
	if payload.GiftEnabled != nil {
		giftEnabled = *payload.GiftEnabled
	}
	giftAmount := defaultBonusAmount
	if payload.GiftAmount != nil {
		giftAmount = *payload.GiftAmount
	}
	err = service.CreateUserWithNewUserBonusWithDB(dao.User.UnderlyingDB(), &user, service.NewUserBonusOptions{
		Enabled: giftEnabled,
		Amount:  giftAmount,
		Source:  service.RegistrationBonusSourceAdminCreate,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidNewUserBonusAmount) || isPhoneBindingError(err) {
			c.JSON(400, gin.H{"message": err.Error()})
			return
		}
		c.JSON(400, gin.H{
			"message": "创建用户失败",
		})
		return
	}
	c.JSON(200, user)
}

// UpdateUser 更新用户基础信息
func UpdateUser(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser := operator.(*entity.User)
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	target, err := dao.User.Where(dao.User.ID.Eq(int32(userID))).First()
	if err != nil {
		c.JSON(400, gin.H{
			"message": "用户不存在",
		})
		return
	}
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{
			"message": "无权限修改",
		})
		return
	}

	var payload struct {
		Nickname            string   `json:"nickname"`
		Role                string   `json:"role"`
		Status              string   `json:"status"`
		Balance             *float64 `json:"balance"`
		BalanceMode         string   `json:"balanceMode"`
		ShowInStatement     *bool    `json:"showInStatement"`
		Phone               string   `json:"phone"`
		ForceReassignPhone  bool     `json:"forceReassignPhone"`
		WechatID            string   `json:"wechatId"`
		ForceReassignWechat bool     `json:"forceReassignWechat"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	var balanceInput service.UserBalanceAdjustmentInput
	// Legacy requests without a balance mode only update profile fields.
	adjustBalance := payload.Balance != nil && strings.TrimSpace(payload.BalanceMode) != ""
	if adjustBalance {
		balanceInput, err = service.NormalizeUserBalanceAdjustment(service.UserBalanceAdjustmentInput{
			Mode: payload.BalanceMode, Amount: *payload.Balance, ShowInStatement: payload.ShowInStatement, ClientIP: c.ClientIP(),
		})
		if err != nil {
			c.JSON(400, gin.H{"message": err.Error()})
			return
		}
	}
	previousProfile := *target
	err = dao.User.UnderlyingDB().Transaction(func(tx *gorm.DB) error {
		if _, err := service.UpdateUserPhoneWithDB(tx, target.ID, payload.Phone, payload.ForceReassignPhone); err != nil {
			return err
		}
		if _, err := service.UpdateUserWechatBindingWithDB(tx, target.ID, payload.WechatID, payload.ForceReassignWechat); err != nil {
			return err
		}
		// Update only profile columns: an open edit dialog must not restore an old balance.
		if err := tx.Model(&entity.User{}).Where("id = ?", target.ID).Updates(map[string]any{
			"nickname": strings.TrimSpace(payload.Nickname), "role": normalizeUserRole(payload.Role), "status": normalizeUserStatus(payload.Status),
		}).Error; err != nil {
			return err
		}
		if adjustBalance {
			if _, err := service.AdjustUserBalanceInTx(tx, *currentUser, target.ID, balanceInput); err != nil {
				return err
			}
		}
		return tx.First(target, target.ID).Error
	})
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	if !samePhone(previousProfile.Phone, target.Phone) {
		recordAccountAudit(c, currentUser, "user", 15, "管理员修改用户手机号", map[string]any{"status": "success", "message": "用户手机号修改成功", "targetUserId": target.ID, "before": map[string]any{"phone": auditPhoneValue(previousProfile.Phone)}, "after": map[string]any{"phone": auditPhoneValue(target.Phone)}})
	}
	if previousProfile.Nickname != target.Nickname || previousProfile.Role != target.Role || previousProfile.Status != target.Status {
		recordAccountAudit(c, currentUser, "user", 16, "管理员修改用户资料", map[string]any{"status": "success", "message": "用户资料修改成功", "targetUserId": target.ID, "before": map[string]any{"nickname": previousProfile.Nickname, "role": previousProfile.Role, "status": previousProfile.Status}, "after": map[string]any{"nickname": target.Nickname, "role": target.Role, "status": target.Status}})
	}
	c.JSON(200, target)
}

// ResetUserPassword 管理员重置用户密码
func ResetUserPassword(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{
			"message": "无权限重置密码",
		})
		return
	}

	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	target, err := dao.User.Where(dao.User.ID.Eq(int32(userID))).First()
	if err != nil {
		c.JSON(400, gin.H{
			"message": "用户不存在",
		})
		return
	}

	var payload struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	password := strings.TrimSpace(payload.Password)
	if err := validateAdminManagedPassword(password); err != nil {
		c.JSON(400, gin.H{
			"message": err.Error(),
		})
		return
	}

	hashedPassword, err := util.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "重置密码失败"})
		return
	}
	target.Password = hashedPassword
	if err := dao.User.Save(target); err != nil {
		c.JSON(400, gin.H{
			"message": "重置密码失败",
		})
		return
	}
	recordAccountAudit(c, currentUser, "user", 14, "管理员重置用户密码", map[string]any{"status": "success", "message": "用户密码重置成功", "targetUserId": target.ID})

	c.JSON(200, gin.H{
		"message": "重置密码成功",
	})
}

// DeleteUser 删除用户
func DeleteUser(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{
			"message": "无权限删除用户",
		})
		return
	}

	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	user, err := dao.User.Where(dao.User.ID.Eq(int32(userID))).First()
	if err != nil {
		c.JSON(400, gin.H{
			"message": "用户不存在",
		})
		return
	}

	dao.User.Delete(user)
	c.JSON(200, gin.H{
		"message": "删除用户成功",
	})
}

func setAuthCookie(c *gin.Context, token string) {
	if c == nil || strings.TrimSpace(token) == "" {
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(
		authCookieName,
		token,
		int(util.JWTTTL.Seconds()),
		"/",
		"",
		requestIsHTTPS(c),
		true,
	)
}

func clearAuthCookie(c *gin.Context) {
	if c == nil {
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authCookieName, "", -1, "/", "", requestIsHTTPS(c), true)
}

func requestIsHTTPS(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}

func tokenCredentialVersionMatches(claims jwt.Claims, passwordHash string) bool {
	mapClaims, ok := claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	version, _ := mapClaims["ver"].(string)
	return strings.TrimSpace(version) != "" && version == util.CredentialVersion(passwordHash)
}
