package controller

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
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
	c.Header("Cache-Control", "no-store")
	tokenValue := strings.TrimSpace(c.Request.Header.Get("Authorization"))
	cookieCredential := false
	if tokenValue == "" {
		cookieValue, err := c.Cookie(authCookieName)
		if err == nil {
			tokenValue = strings.TrimSpace(cookieValue)
			cookieCredential = tokenValue != ""
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
	if !tokenLoginSessionMatches(claims, user.LoginSessionID) {
		c.AbortWithStatusJSON(401, gin.H{"message": "账号已在其他设备登录，请重新登录"})
		return
	}
	if cookieCredential && !cookieRequestContextAllowed(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "不支持跨源或跨站 Cookie 请求"})
		return
	}
	c.Set("user", user)
	c.Next()
}

// AuthOrAccessToken accepts the access tokens used by browser integrations, while
// preserving the existing login-JWT behavior for other protected endpoints.
func AuthOrAccessToken(c *gin.Context) {
	authorization := strings.TrimSpace(c.Request.Header.Get("Authorization"))
	if identity := strings.TrimSpace(c.Request.Header.Get("X-Script-Identity")); identity != "" {
		authCtx, session, err := service.ResolveSearchTokenWithScriptIdentity(dao.User.UnderlyingDB(), authorization, identity, c.ClientIP())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "脚本运行会话无效或已过期"})
			return
		}
		user := authCtx.User
		c.Set("user", &user)
		c.Set("access_token", &authCtx.AccessToken)
		c.Set("script_session", session)
		c.Next()
		return
	}
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

	authCtx, session, err := service.ResolveSearchTokenWithScriptIdentity(dao.User.UnderlyingDB(), authorization, c.Request.Header.Get("X-Script-Identity"), c.ClientIP())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "请提供有效访问令牌"})
		return
	}

	user := authCtx.User
	c.Set("user", &user)
	c.Set("access_token", &authCtx.AccessToken)
	if session.ID > 0 {
		c.Set("script_session", session)
	}
	c.Next()
}

// UserSession is a read-only heartbeat; Auth already checked the current session.
func UserSession(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
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
	minimum := service.GetMinRechargeAmountWithDB(withDatabasePriority(dao.User.UnderlyingDB(), databaseRequestContext(c), dbqueue.PriorityLow))
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

	// 两套登录限流的前置检查（45-*.md §0.1.4 四-1）。
	//
	// 普通登录**同时受两个维度约束**：
	//   account —— 按 username 计数，可被管理员关闭（login_rate_limit_enabled）
	//   ip      —— **仅当该 IP 在黑名单中**时生效，按 IP 计数，不可关闭
	//
	// 为什么被拉黑 IP 从普通入口也要受 IP 维度约束：否则攻击者被拉黑后
	// 换普通登录入口继续爆破，就绕过了管理员入口的那层防护。
	// 两套任一超限即拒绝。
	loginGate := beginLoginRateLimit(c)
	if loginGate == nil {
		return
	}
	// 账号维度的前置锁定检查**必须在验证凭据之前**：否则已锁定的账号
	// 只要提交正确密码就能登录，锁定形同虚设（实测发现的第一版缺陷）。
	if loginGate.checkAccountLocked(payload.Username) {
		return
	}

	if err := util.ValidateBcryptPasswordBytes(payload.Password); err != nil {
		loginGate.failAccount(payload.Username, "用户名或密码错误")
		return
	}

	captchaToken := strings.TrimSpace(payload.CaptchaToken)
	if err := consumeCaptchaTokenWithDB(captchaToken, service.CaptchaSceneLoginPassword, strings.TrimSpace(c.ClientIP())); err != nil {
		loginGate.failAccount(payload.Username, err.Error())
		return
	}

	us := dao.User
	user, err := us.Where(us.Username.Eq(strings.TrimSpace(payload.Username))).First()
	if err != nil {
		// 账号不存在**也计数**：key 用输入的 username（归一化后），**不查库** ——
		// 否则可以靠「是否被锁」反推账号是否存在。
		loginGate.failAccount(payload.Username, "用户名或密码错误")
		return
	}
	passwordMatches, needsUpgrade := util.VerifyPassword(user.Password, payload.Password)
	if !passwordMatches {
		loginGate.failAccount(payload.Username, "用户名或密码错误")
		return
	}
	if !isUserEnabled(user) {
		loginGate.failAccount(payload.Username, "用户已被禁用")
		return
	}
	passwordUpgrade := ""
	if needsUpgrade {
		passwordUpgrade, err = util.HashPassword(payload.Password)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "登录信息更新失败"})
			return
		}
	}
	token, err := service.IssueUserLoginJWTWithDB(dao.User.UnderlyingDB().WithContext(c.Request.Context()), user, c.ClientIP(), passwordUpgrade)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "登录信息更新失败"})
		return
	}
	// 成功：两个维度都清零（含 window_start 重置，「连续」语义）。
	loginGate.succeed(payload.Username)
	bindSuccessfulVerification(*user, captchaToken, service.CaptchaSceneLoginPassword, service.SMSVerificationEvidence{})
	recordAccountAudit(c, user, "auth", logActionLogin, "用户登录成功", map[string]any{"status": "success", "message": "用户登录成功", "scene": "password"})
	setAuthCookie(c, token)
	c.JSON(http.StatusOK, gin.H{"message": "user login", "jwt": token})
}

// UserRegister 公开注册
func UserRegister(c *gin.Context) {
	enabled, err := service.GetRegisterEnabledWithDB(withDatabasePriority(dao.User.UnderlyingDB(), databaseRequestContext(c), dbqueue.PriorityLow))
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
		// InviteCode 是可选邀请码：只在注册时生效，注册后不可改绑。
		// 总开关关闭时该字段被静默忽略（不报错），老客户端不带它也能注册。
		InviteCode string `json:"inviteCode"`
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
	// 手机号黑名单（46-*.md §3.1，BL-3）：**必须在消费图形/短信验证码与建用户之前**。
	// 放到事务里判定就晚了 —— 那一步已经消耗掉一条短信验证码，命中后回滚也退不回来。
	// 判定只读进程内缓存快照（开关关闭时连读都没有），不产生业务库写。
	if rejectBlacklistedPhone(c, phone) {
		return
	}
	registrationFlowToken := strings.TrimSpace(payload.RegistrationFlowToken)
	inviteCode := strings.TrimSpace(payload.InviteCode)
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
			// 微信黑名单（46-*.md §3.1，BL-3）：微信注册同样要拦。
			//
			// 判定放在 consume 回调的**最前面**：此时 OpenID 刚从扫码/验证码流程
			// 里取到，而图形验证码与短信验证码还没被消耗、用户也还没建 ——
			// 被拉黑的人不会因此白烧一条短信。
			//
			// 命中后由外层统一 Release 掉这个注册流程（与手机号冲突分支一致）：
			// Consume 在回调返回错误时不会置 released，留着它只会让用户反复重试
			// 同一个被拉黑的扫码结果。
			if match, hit := service.BlacklistWechatGate(db, wechatID, blacklistGateMeta(c)); hit {
				return service.NewBlacklistBlockedError(match)
			}
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
				if err := service.CreateUserWithNewUserBonusInTx(tx, &user, service.NewUserBonusOptions{
					Enabled: true,
					Amount:  bonusAmount,
					Source:  service.RegistrationBonusSourcePublicRegister,
				}); err != nil {
					return err
				}
				return applyReferralOnRegister(tx, &user, inviteCode)
			})
		})
		if err != nil {
			// 微信黑名单命中：403（只回 reason），并释放注册流程。
			// 必须先于通用 400 分支判断，否则被拦的人看到的是 400 + reason，
			// 客户端会当成「参数错误」而不会当成「被封禁」。
			if match, blocked := service.ResolveBlacklistBlocked(err); blocked {
				_ = wechatRegisterFlowStore.Release(registrationFlowToken)
				respondBlacklistBlocked(c, match)
				return
			}
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
			return err
		}
		if err != nil {
			return err
		}
		if err := applyReferralOnRegister(tx, &user, inviteCode); err != nil {
			failureMessage = referralRegisterFailureMessage(err)
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": failureMessage})
		return
	}
	bindSuccessfulVerification(user, payload.CaptchaToken, service.CaptchaSceneRegisterSubmit, smsEvidence)
	recordAccountAudit(c, &user, "auth", 12, "用户注册成功", map[string]any{"status": "success", "message": "用户注册成功", "scene": "register"})
	c.JSON(200, user)
}

// applyReferralOnRegister 在注册事务内完成邀请归因并生成新用户自己的邀请码。
//
// 语义（对应 04 稿 3.6 节）：
//   - 总开关关闭：整段跳过，不生成邀请码、不绑定邀请人、也不因为带了
//     inviteCode 而报错（老客户端可能带着历史码）；
//   - inviteCode 为空：只生成自己的邀请码；
//   - inviteCode 非法 / 自邀：返回业务错误，整个注册事务回滚（用户必须改输入重来）；
//   - 生成邀请码失败：**不阻断注册** —— 邀请码是附加权益，下次可补发。
//
// 邀请码只在注册时生效，注册后不可改绑（绑定入口只有这里）。
func applyReferralOnRegister(tx *gorm.DB, user *entity.User, inviteCode string) error {
	if tx == nil || user == nil || user.ID <= 0 {
		return nil
	}
	if !service.GetReferralRebateEnabledWithDB(tx) {
		return nil
	}
	if code := strings.TrimSpace(inviteCode); code != "" {
		invite, err := service.ResolveReferralInviterWithDB(tx, code)
		if err != nil {
			return err
		}
		if err := service.BindReferralInviterInTx(tx, user, invite); err != nil {
			return err
		}
	}
	// 生成自己的邀请码失败不影响注册结果：记录日志后继续。
	if _, err := service.RegisterReferralInviteForNewUser(tx, user.ID); err != nil {
		logger.SysWarn("新用户邀请码生成失败（不影响注册，可稍后补发）：" + logger.RedactSecrets(err.Error()))
	}
	return nil
}

// referralRegisterFailureMessage 把邀请相关错误翻成给用户看的注册失败原因。
func referralRegisterFailureMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrReferralCodeInvalid):
		return "邀请码无效"
	case errors.Is(err, service.ErrReferralSelfInvite):
		return "不能使用自己的邀请码"
	case errors.Is(err, service.ErrReferralAlreadyBound):
		return "该账户已绑定邀请人，不能再次绑定"
	}
	return "注册失败"
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
	// 手机号黑名单（46-*.md §3.1，BL-3）：换绑判定的是**新手机号**，不是旧号。
	// 判定在 verifySMSCodeWithDB **之前**：被拉黑的新号不该消耗一条验证码，
	// 也不该让「该号已被拉黑」以外的分支先报错（否则运营无法解释拦在哪一步）。
	if rejectBlacklistedPhone(c, phone) {
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
		// 微信黑名单命中（46-*.md §3.1，BL-3）：403 而不是 400。
		if respondBlacklistBlockedError(c, err) {
			return
		}
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
		// 微信黑名单命中（46-*.md §3.1，BL-3）：403 而不是 400。
		if respondBlacklistBlockedError(c, err) {
			return
		}
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
	// 手机号黑名单（46-*.md §3.1，BL-3）。
	//
	// 位置：**在图形验证码与短信验证码之前**，也在 loginGate 计数之前。
	//   - 在验证码之前：被拉黑的号码不该消耗验证码（短信要花钱）；
	//   - 在 loginGate 之前：黑名单命中是**拒绝**而不是「一次登录失败」，
	//     不该记进爆破计数 —— 否则被拉黑者反复重试会把整个出口 IP 锁死，
	//     误伤同 NAT 后的正常用户（45-*.md §0.1.4 的分层语义）。
	if rejectBlacklistedPhone(c, phone) {
		return
	}
	// 短信登录**也要计数**（45-*.md §0.1.4 五-3）：它同样能撞号，
	// 且与密码登录共用同一个账号维度 key（手机号归一化后作为 username 的替代）。
	loginGate := beginLoginRateLimit(c)
	if loginGate == nil {
		return
	}
	// 账号维度前置检查（key 用归一化手机号，与密码登录共用同一维度）。
	if loginGate.checkAccountLocked(phone) {
		return
	}
	if err := consumeCaptchaTokenWithDB(strings.TrimSpace(payload.CaptchaToken), service.CaptchaSceneLoginSMS, strings.TrimSpace(c.ClientIP())); err != nil {
		loginGate.failAccount(phone, err.Error())
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
		loginGate.failAccount(phone, "该手机号未绑定账号，请先注册")
		return
	}
	user := users[0]
	if !isUserEnabled(&user) {
		loginGate.failAccount(phone, "用户已被禁用")
		return
	}
	smsEvidence, err := verifySMSCodeWithDB(phone, service.SMSSceneLogin, payload.SMSCode)
	if err != nil {
		loginGate.failAccount(phone, err.Error())
		return
	}
	token, err := service.IssueUserLoginJWTWithDB(dao.User.UnderlyingDB().WithContext(c.Request.Context()), &user, c.ClientIP(), "")
	if err != nil {
		c.JSON(400, gin.H{"message": "登录信息更新失败"})
		return
	}
	loginGate.succeed(phone)
	bindSuccessfulVerification(user, payload.CaptchaToken, service.CaptchaSceneLoginSMS, smsEvidence)
	recordAccountAudit(c, &user, "auth", logActionLogin, "用户短信登录成功", map[string]any{"status": "success", "message": "用户短信登录成功", "scene": "sms"})
	setAuthCookie(c, token)
	c.JSON(200, gin.H{"message": "user login", "jwt": token})
}

func UserLogout(c *gin.Context) {
	if user := optionalLogoutUser(c); user != nil {
		// Public logout still clears anonymous/expired cookies, but a valid
		// Cookie-selected session must obey the same origin boundary as Auth.
		// Explicit Authorization retains precedence and extension compatibility.
		if strings.TrimSpace(c.GetHeader("Authorization")) == "" {
			if value, err := c.Cookie(authCookieName); err == nil && strings.TrimSpace(value) != "" && !cookieRequestContextAllowed(c.Request) {
				c.JSON(http.StatusForbidden, gin.H{"message": "不支持跨源或跨站 Cookie 请求"})
				return
			}
		}
		if err := service.RevokeUserLoginSessionWithDB(dao.User.UnderlyingDB().WithContext(c.Request.Context()), user.ID, user.LoginSessionID); err != nil {
			logger.SysError("退出登录会话失效失败: " + logger.RedactSecrets(err.Error()))
		}
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

	query := buildUserListQuery(withDatabasePriority(dao.User.UnderlyingDB(), databaseRequestContext(c), dbqueue.PriorityLow).Model(&entity.User{}), c.Query("keyword"), c.Query("role"), c.Query("status"))

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
		Nickname        string   `json:"nickname"`
		Role            string   `json:"role"`
		Status          string   `json:"status"`
		Balance         *float64 `json:"balance"`
		BalanceMode     string   `json:"balanceMode"`
		ShowInStatement *bool    `json:"showInStatement"`
		// BalanceBucket 选择调整哪个钱包桶（task_doc/70 需求 5b）：
		// principal=充值本金（可退，默认）/ gift=赠送金额（不可退）。
		// 不传等价于 principal，保证既有客户端行为不变。
		//
		// ⚠️ 少了这个字段时，前端传 bucket='gift' 会被 ShouldBindJSON 静默丢弃，
		// 调整退化成本金桶 —— 管理员以为在改赠送金额，实际改了可退本金。
		// 这是「helper 算对了但中间层把字段丢了」的典型形态。
		BalanceBucket       string `json:"balanceBucket"`
		Phone               string `json:"phone"`
		ForceReassignPhone  bool   `json:"forceReassignPhone"`
		WechatID            string `json:"wechatId"`
		ForceReassignWechat bool   `json:"forceReassignWechat"`
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
			Mode: payload.BalanceMode, Amount: *payload.Balance, ShowInStatement: payload.ShowInStatement,
			Bucket: payload.BalanceBucket, ClientIP: c.ClientIP(),
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
	// 不允许管理员删自己：删完立即失去管理权限，且无法自查。
	// 这是既有行为之外新增的守卫（task_doc/76 §3.2）。
	if user.ID == currentUser.ID {
		c.JSON(400, gin.H{
			"message": "不能删除当前登录的账号",
		})
		return
	}

	// 归档原因可由调用方指定（前端传 deleteReason）；缺省 admin_delete。
	// 只接受两个合法值 —— 其它值会被写进 archived_reason 并影响审计口径。
	reason := strings.TrimSpace(c.Query("reason"))
	if reason != entity.UserArchivedReasonUserClosure {
		reason = entity.UserArchivedReasonAdminDelete
	}

	// 归档与绑定释放必须在**同一个事务**里完成（task_doc/76 §3.2.4）：
	// 只归档不释放绑定，活跃行会继续占 (kind, active_key) 唯一索引，
	// 别人再绑这个手机号会撞唯一约束，且报错文案与手机号毫无字面关系。
	archiveErr := dao.User.UnderlyingDB().Transaction(func(tx *gorm.DB) error {
		if err := service.ReleaseAllBindingsForArchiveInTx(tx, user.ID, reason, currentUser.ID); err != nil {
			return err
		}
		return service.ArchiveUserInTx(tx, *user, reason, currentUser.ID)
	})
	if archiveErr != nil {
		logger.SysError("归档用户失败: " + logger.RedactSecrets(archiveErr.Error()))
		c.JSON(500, gin.H{
			"message": "删除用户失败",
		})
		return
	}

	// 日志写在归档之后（task_doc/77 §4 第 9 条要求「先写日志再执行」，
	// 但删除类操作的日志内容依赖归档结果；因此这里用「归档成功才写」，
	// 失败路径由上面的 SysError 留痕）。
	//
	// 两个通道都写：
	//   logs 表       → LogList 页可见（用户明确要求的「日志列表」）
	//   AccountAudit  → 结构化的账户域审计
	recordAccountDelete(c, currentUser, *user, reason)
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

func tokenLoginSessionMatches(claims jwt.Claims, currentSessionID string) bool {
	currentSessionID = strings.TrimSpace(currentSessionID)
	if currentSessionID == "" {
		return true
	}
	mapClaims, ok := claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	tokenSessionID, ok := mapClaims["sid"].(string)
	if !ok || strings.TrimSpace(tokenSessionID) == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tokenSessionID), []byte(currentSessionID)) == 1
}

// cookieRequestContextAllowed applies only after actual Cookie credential selection
// and existing authentication. Missing context keeps approved legacy compatibility.
// Forwarded headers are intentionally ignored. Without direct TLS, public scheme
// may be hidden by a TLS terminator: only host/port are established in that case.
func cookieRequestContextAllowed(request *http.Request) bool {
	if request == nil {
		return false
	}
	for _, value := range request.Header.Values("Sec-Fetch-Site") {
		for _, site := range strings.Split(value, ",") {
			site = strings.TrimSpace(site)
			if strings.EqualFold(site, "cross-site") || strings.EqualFold(site, "same-site") {
				return false
			}
		}
	}
	origins := request.Header.Values("Origin")
	if len(origins) == 0 || len(origins) == 1 && strings.TrimSpace(origins[0]) == "" {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	originRaw := strings.TrimSpace(origins[0])
	if strings.Contains(originRaw, "#") {
		return false
	}
	origin, err := url.Parse(originRaw)
	if err != nil || origin == nil || origin.Opaque != "" || origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(origin.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	originHost, originPort, ok := cookieContextHost(origin.Host)
	if !ok {
		return false
	}
	if originPort == "" {
		if scheme == "https" {
			originPort = "443"
		} else {
			originPort = "80"
		}
	}
	targetHost, targetPort, ok := cookieContextHost(request.Host)
	if !ok || originHost != targetHost {
		return false
	}
	if request.TLS != nil {
		if targetPort == "" {
			targetPort = "443"
		}
		return scheme == "https" && originPort == targetPort
	}
	if targetPort != "" {
		return originPort == targetPort
	}
	// A no-port Host behind a TLS terminator can represent either public default
	// scheme. This compatibility allowance is not a complete cross-scheme defense.
	return scheme == "http" && originPort == "80" || scheme == "https" && originPort == "443"
}

func cookieContextHost(authority string) (string, string, bool) {
	if authority == "" || strings.TrimSpace(authority) != authority || strings.Contains(authority, "#") {
		return "", "", false
	}
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.Contains(host, "%") {
		return "", "", false
	}
	if strings.Contains(host, ":") || strings.HasPrefix(parsed.Host, "[") {
		if !strings.HasPrefix(parsed.Host, "[") || !strings.Contains(host, ":") || net.ParseIP(host) == nil {
			return "", "", false
		}
	} else {
		dns := strings.TrimSuffix(host, ".")
		if len(dns) == 0 || len(dns) > 253 {
			return "", "", false
		}
		for _, label := range strings.Split(dns, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", "", false
			}
			for _, char := range label {
				if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
					return "", "", false
				}
			}
		}
	}
	port := parsed.Port()
	if strings.HasSuffix(authority, ":") {
		return "", "", false
	}
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", "", false
		}
		port = strconv.Itoa(value)
	}
	return host, port, true
}
