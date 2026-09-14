package controller

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
)

var sendPublicSMSCodeWithDB = func(input service.SendSMSCodeInput) error {
	return service.SendSMSCodeWithDB(dao.User.UnderlyingDB(), input)
}

var verifySMSCodeWithDB = func(phone string, scene string, smsCode string) error {
	return service.VerifySMSCodeWithDB(dao.User.UnderlyingDB(), phone, scene, smsCode)
}

var consumeCaptchaTokenWithDB = func(token string, scene string, requestIP string) error {
	return service.ConsumeCaptchaTokenWithDB(dao.User.UnderlyingDB(), token, scene, requestIP)
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

func applyUserBalanceUpdate(currentBalance float64, amount float64, mode string) (float64, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "override":
		return amount, nil
	case "increase":
		return currentBalance + amount, nil
	case "decrease":
		return currentBalance - amount, nil
	case "":
		return currentBalance, nil
	default:
		return 0, fmt.Errorf("不支持的余额调整模式")
	}
}

func validatePasswordComplexity(password string) error {
	trimmed := strings.TrimSpace(password)
	if trimmed == "" {
		return fmt.Errorf("密码不能为空")
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

// Auth 鉴权
func Auth(c *gin.Context) {
	claims, err := util.ParseJwtWithClaims(c.Request.Header.Get("Authorization"))

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

	userID, _ := strconv.Atoi(subject)

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
	c.Set("user", user)
}

// UserInfo 获取用户信息
func UserInfo(c *gin.Context) {
	v, _ := c.Get(`user`)
	c.JSON(200, v.(*entity.User))
}

// UserLogin 用户登录
func UserLogin(c *gin.Context) {
	captchaToken := strings.TrimSpace(c.Query("captchaToken"))
	if err := consumeCaptchaTokenWithDB(captchaToken, service.CaptchaSceneLoginPassword, strings.TrimSpace(c.ClientIP())); err != nil {
		c.JSON(400, gin.H{
			"message": err.Error(),
		})
		return
	}

	us := dao.User
	user, err := us.Where(us.Username.Eq(c.Query("username")), us.Password.Eq(c.Query("password"))).First()
	if err != nil {
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
	jwt := util.GenerateJwt(user.ID)

	if jwt == "" {
		c.JSON(400, gin.H{
			"message": "jwt生成失败",
		})
	} else {
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
		writeAuditLog(user, "auth", logActionLogin, "用户登录成功", clientIP, 0, "", "")
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
		Username     string `json:"username"`
		Password     string `json:"password"`
		Nickname     string `json:"nickname"`
		Phone        string `json:"phone"`
		SMSCode      string `json:"smsCode"`
		CaptchaToken string `json:"captchaToken"`
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
	rawPhone := strings.TrimSpace(payload.Phone)
	smsCode := strings.TrimSpace(payload.SMSCode)
	if username == "" || password == "" {
		c.JSON(400, gin.H{
			"message": "用户名和密码不能为空",
		})
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
	phone, _, err := service.NormalizePhone(rawPhone)
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	if err := consumeCaptchaTokenWithDB(strings.TrimSpace(payload.CaptchaToken), service.CaptchaSceneRegisterSubmit, strings.TrimSpace(c.ClientIP())); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
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
	if err := verifySMSCodeWithDB(phone, service.SMSSceneRegister, smsCode); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	now := time.Now()
	user := entity.User{
		Username:       username,
		Password:       password,
		Nickname:       nickname,
		Role:           "user",
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "public",
		ParentID:       0,
		Perms:          "",
		Phone:          &phone,
		PhoneBoundAt:   &now,
	}

	if err := dao.User.Create(&user); err != nil {
		c.JSON(400, gin.H{
			"message": "注册失败",
		})
		return
	}
	c.JSON(200, user)
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
	captchaScene := service.CaptchaSceneSMSSendRegister
	if strings.TrimSpace(payload.Scene) == service.SMSSceneLogin {
		captchaScene = service.CaptchaSceneSMSSendLogin
	}
	if err := consumeCaptchaTokenWithDB(strings.TrimSpace(payload.CaptchaToken), captchaScene, strings.TrimSpace(c.ClientIP())); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	if err := sendPublicSMSCodeWithDB(service.SendSMSCodeInput{
		Phone:     strings.TrimSpace(payload.Phone),
		Scene:     strings.TrimSpace(payload.Scene),
		RequestIP: strings.TrimSpace(c.ClientIP()),
	}); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "发送成功，请注意查收"})
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
	phone, _, err := service.NormalizePhone(payload.Phone)
	if err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	if err := consumeCaptchaTokenWithDB(strings.TrimSpace(payload.CaptchaToken), service.CaptchaSceneLoginSMS, strings.TrimSpace(c.ClientIP())); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	var user entity.User
	if err := dao.User.UnderlyingDB().Where("phone = ?", phone).First(&user).Error; err != nil {
		c.JSON(400, gin.H{"message": "该手机号未绑定账号，请先注册"})
		return
	}
	if !isUserEnabled(&user) {
		c.JSON(400, gin.H{"message": "用户已被禁用"})
		return
	}
	if err := verifySMSCodeWithDB(phone, service.SMSSceneLogin, payload.SMSCode); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	jwt := util.GenerateJwt(user.ID)
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
	writeAuditLog(&user, "auth", logActionLogin, "用户短信登录成功", clientIP, 0, "", "")
	c.JSON(200, gin.H{"message": "user login", "jwt": jwt})
}

// UserList 用户列表
func UserList(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{
			"message": "无权限查看用户",
		})
		return
	}

	users, err := dao.User.Find()
	if err != nil {
		c.JSON(400, gin.H{
			"message": "获取用户列表失败",
		})
		return
	}
	c.JSON(200, users)
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

	var user entity.User

	err := c.ShouldBindJSON(&user)
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	user.Username = strings.TrimSpace(user.Username)
	user.Password = strings.TrimSpace(user.Password)
	user.Nickname = strings.TrimSpace(user.Nickname)
	if user.Username == "" || user.Password == "" {
		c.JSON(400, gin.H{
			"message": "用户名和密码不能为空",
		})
		return
	}
	if err := validatePasswordComplexity(user.Password); err != nil {
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

	err = dao.User.Create(&user)
	if err != nil {
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
		Nickname           string   `json:"nickname"`
		Role               string   `json:"role"`
		Status             string   `json:"status"`
		Balance            *float64 `json:"balance"`
		BalanceMode        string   `json:"balanceMode"`
		Phone              string   `json:"phone"`
		ForceReassignPhone bool     `json:"forceReassignPhone"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	target.Nickname = strings.TrimSpace(payload.Nickname)
	target.Role = normalizeUserRole(payload.Role)
	target.Status = normalizeUserStatus(payload.Status)
	if payload.Balance != nil {
		updatedBalance, err := applyUserBalanceUpdate(target.Balance, *payload.Balance, payload.BalanceMode)
		if err != nil {
			c.JSON(400, gin.H{
				"message": "余额调整模式错误",
			})
			return
		}
		target.Balance = updatedBalance
	}
	if updatedUser, err := service.UpdateUserPhoneWithDB(dao.User.UnderlyingDB(), target.ID, payload.Phone, payload.ForceReassignPhone); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	} else {
		target.Phone = updatedUser.Phone
		target.PhoneBoundAt = updatedUser.PhoneBoundAt
	}

	if err := dao.User.Save(target); err != nil {
		c.JSON(400, gin.H{
			"message": "更新用户失败",
		})
		return
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
	if err := validatePasswordComplexity(password); err != nil {
		c.JSON(400, gin.H{
			"message": err.Error(),
		})
		return
	}

	target.Password = password
	if err := dao.User.Save(target); err != nil {
		c.JSON(400, gin.H{
			"message": "重置密码失败",
		})
		return
	}

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
