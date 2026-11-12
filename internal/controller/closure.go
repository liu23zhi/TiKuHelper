package controller

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 注销申请（task_doc/76 §3.3）。
//
// 状态机（76 §3.3.2）：
//
//	enabled ──用户申请──▶ closure_pending ──等待期到期 / 管理员处理──▶ 归档（deleted_at 非空）
//	                          └──用户撤销──▶ enabled
//
// 三个接口（76 §3.3.5）：
//
//	POST /adapter-service/user/closure-request   申请注销（需密码确认）
//	POST /adapter-service/user/closure-cancel    撤销申请
//	GET  /adapter-service/user/closure-status    当前状态与剩余天数
//
// # 三处最容易做错的地方
//
//  1. **application 必须真的落库**。normalizeUserStatus（user.go）原本只认
//     enabled / disabled，其它值一律落回 enabled —— 用户点完注销，状态被
//     静默改回 enabled，界面显示成功、实际没生效。本任务的 user.go 改动
//     就是为了这条（已补单测）。
//  2. **等待期内必须能撤销**（76 §3.3.3）：会话判据 isUserSessionAllowed 认
//     closure_pending（登录/持有会话），消费判据 isUserSpendable 不认 ——
//     两者必须分开，否则「可登录」与「可消费」会被同一个布尔的改动同时放开。
//  3. **申请/撤销必须限流**（76 §3.3.5 硬要求）：两个写操作都带凭据校验，
//     无限制可被用来反复试探密码。这里复用登录失败计数（同一份凭据，锁定口径一致）。

const (
	// userStatusClosurePending 是「已申请注销，等待期」的状态值（76 §3.3.2）。
	//
	// ⚠️ 必须同步登记在 normalizeUserStatus 里，否则会被改回 enabled。
	userStatusClosurePending = "closure_pending"
	userStatusEnabled        = "enabled"

	// userStatusBanned 是「已封禁」的状态值。
	//
	// ⚠️ 必须同步登记在 normalizeUserStatus 里，否则会被改回 enabled ——
	// 管理员点「封禁」界面提示成功、实际没封，且全程不报错。
	// 封禁是风控动作，静默失败比报错严重得多：运营以为已拦住，对方照常使用。
	//
	// 与 disabled 的区别：disabled 是「账号功能禁用」（业务层判断），
	// banned 是「风控封禁」（可由黑名单/违规触发）。两者在准入谓词上
	// 目前都是全拒（isUserEnabled / isUserSessionAllowed / isUserSpendable
	// 都不认），区别在语义与展示，以及将来 banned 可能需要单独的申诉流程。
	//
	// 本期只做「这个取值能否落库」，不新建 user_bans 表（见任务边界）。
	userStatusBanned = "banned"

	// closureWaitingDays 是注销等待期天数。
	//
	// 45 号文档 Q76-2 定「7 天，可配」；本轮固定 7 天：做成可配需要在系统管理页
	// 增加一项配置（含校验与回填），属独立改造，不在本任务范围。
	closureWaitingDays = 7

	// closureBalanceFreezeThreshold 是「有余额需人工处理」的阈值。
	//
	// 45 号 Q8：注销时账号仍有权益 → 二次确认；**> 10 元**（可配）→ 直接发工单
	// + 暂时冻结账号。取「严格大于」：正好 10 元不触发。
	closureBalanceFreezeThreshold = 10.0
)

// closureStatusView 是三条接口统一返回的状态视图。
type closureStatusView struct {
	Status   string `json:"status"`
	Pending  bool   `json:"pending"`
	WaitDays int    `json:"waitDays"`
	// RequestedAt 是**申请时刻**，从 logs 表里 Action=51 的最新一条取。
	//
	// 为什么不加 users.closure_requested_at 列：等待期是「一次申请的有效期」，
	// 而申请/撤销本身就是必须留痕的账户操作 —— 日志行已经是权威事实，
	// 再存一列会出现「日志与列不一致」时的第二真相。
	RequestedAt   *time.Time `json:"requestedAt,omitempty"`
	RemainingDays int        `json:"remainingDays"`
	BalanceTotal  float64    `json:"balanceTotal"`
	// RequiresTicket 表示余额是否超过阈值（前端据此显示「冻结 + 转人工」提示）。
	RequiresTicket bool `json:"requiresTicket"`
	// TicketID / TicketError 只在本次申请同步建单时出现（工单是异步副产品）。
	TicketID *int64 `json:"ticketId,omitempty"`
	// PhoneBound=false 时不能自助注销（45 号 Q1 / 76 §3.3.4）。
	PhoneBound bool `json:"phoneBound"`
	// Eligible + IneligibleReason 让前端不必重复实现一遍判定规则。
	Eligible         bool   `json:"eligible"`
	IneligibleReason string `json:"ineligibleReason,omitempty"`
}

// closureActor 取当前登录用户（Auth 中间件已保证存在）。
func closureActor(c *gin.Context) *entity.User {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(`user`)
	if !ok {
		return nil
	}
	user, _ := raw.(*entity.User)
	if user == nil || user.ID <= 0 {
		return nil
	}
	return user
}

// isUserClosurePending 判断账号是否处于注销等待期。
func isUserClosurePending(user *entity.User) bool {
	return user != nil && strings.EqualFold(strings.TrimSpace(user.Status), userStatusClosurePending)
}

// ClosureRequest 用户申请注销账号。
func ClosureRequest(c *gin.Context) {
	current := closureActor(c)
	if current == nil {
		c.JSON(401, gin.H{"message": "请重新登录"})
		return
	}
	db := archivedUserDB()
	if db == nil {
		c.JSON(500, gin.H{"message": "数据库不可用"})
		return
	}

	var payload struct {
		Password string `json:"password"`
		// Acknowledge 是「我已了解：注销后账号将移入归档，无法自行恢复」的勾选。
		// 不勾选直接 400：这是不可撤销操作的知情确认，不能由前端默默代填。
		Acknowledge bool `json:"acknowledge"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	password := strings.TrimSpace(payload.Password)
	if password == "" {
		c.JSON(400, gin.H{"message": "请输入当前密码"})
		return
	}
	if !payload.Acknowledge {
		c.JSON(400, gin.H{"message": "请先确认已了解注销后果"})
		return
	}

	// 限流：复用登录失败计数（同一份凭据被反复试探，锁定口径应当与登录一致）。
	policy := service.AccountLoginPolicy(db)
	subjectKey := service.NormalizeLoginAccountKey(current.Username)
	lock, err := service.CheckLoginLockoutWithDB(db, policy, subjectKey, time.Now())
	if err == nil && lock.Locked {
		c.JSON(429, gin.H{"message": fmt.Sprintf("密码错误次数过多，请 %d 分钟后再试", lock.RemainingMinutesCeil())})
		return
	}
	if matches, _ := util.VerifyPassword(current.Password, password); !matches {
		if _, recordErr := service.RecordLoginFailureWithDB(db, policy, subjectKey, time.Now()); recordErr != nil {
			logger.SysWarn("记录注销申请的密码失败次数失败: " + logger.RedactSecrets(recordErr.Error()))
		}
		c.JSON(400, gin.H{"message": "密码错误"})
		return
	}
	if clearErr := service.ClearLoginAttemptsOnSuccessWithDB(db, policy, subjectKey, time.Now()); clearErr != nil {
		logger.SysWarn("清除登录失败计数失败: " + logger.RedactSecrets(clearErr.Error()))
	}

	// 无手机号不允许自助注销（45 号 Q1 / 76 §3.3.4）：必须提工单由管理员处理。
	if current.Phone == nil || strings.TrimSpace(*current.Phone) == "" {
		c.JSON(400, gin.H{"message": "账号没有绑定手机号，不支持自助注销，请提交工单由管理员处理"})
		return
	}
	// 幂等：重复申请不重复写状态与日志，直接返回当前进度。
	if isUserClosurePending(current) {
		c.JSON(200, gin.H{"message": "账号已在注销等待期", "data": buildClosureStatus(db, *current)})
		return
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		// 申请路径不约束前置状态：账号能通过 Auth 就说明它当前是「可登录」状态，
		// 而目标状态与前置状态无关（disabled 的账号根本进不来）。
		return updateUserStatusInTx(tx, current.ID, userStatusClosurePending, "")
	}); err != nil {
		logger.SysError("设置注销等待状态失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "申请注销失败"})
		return
	}

	// 重新读一次：以库里的最终状态为准（并发下可能已被管理员改动）。
	fresh, loadErr := loadActiveUser(db, current.ID)
	if loadErr != nil {
		fresh = *current
		fresh.Status = userStatusClosurePending
	}
	view := buildClosureStatus(db, fresh)

	// 45 号 Q8：> 10 元直接发工单 + 冻结账号。
	// 冻结 = 状态 closure_pending（不可消费），工单让人工处理退款/结转。
	//
	// 工单是**副产品**：建单失败不回滚注销申请（申请本身已经生效且必须留痕），
	// 只在日志与响应里暴露出来，由管理员在归档页看到余额后跟进。
	if view.RequiresTicket {
		if ticketID, ticketErr := createClosureBalanceTicket(db, fresh, view.BalanceTotal); ticketErr != nil {
			logger.SysError("注销申请自动建工单失败: " + logger.RedactSecrets(ticketErr.Error()))
		} else {
			view.TicketID = &ticketID
		}
	}

	recordAccountClosureRequest(c, current, view)
	c.JSON(200, gin.H{"message": "已提交注销申请，等待期内可随时撤销", "data": view})
}

// ClosureCancel 撤销注销申请。
func ClosureCancel(c *gin.Context) {
	current := closureActor(c)
	if current == nil {
		c.JSON(401, gin.H{"message": "请重新登录"})
		return
	}
	db := archivedUserDB()
	if db == nil {
		c.JSON(500, gin.H{"message": "数据库不可用"})
		return
	}
	if !isUserClosurePending(current) {
		c.JSON(400, gin.H{"message": "当前账号没有待处理的注销申请"})
		return
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return updateUserStatusInTx(tx, current.ID, userStatusEnabled, userStatusClosurePending)
	}); err != nil {
		if errors.Is(err, errUserStatusChanged) {
			c.JSON(400, gin.H{"message": "账号状态已变更，请刷新后重试"})
			return
		}
		logger.SysError("撤销注销申请失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "撤销注销申请失败"})
		return
	}

	fresh, loadErr := loadActiveUser(db, current.ID)
	if loadErr != nil {
		fresh = *current
		fresh.Status = userStatusEnabled
	}
	recordAccountClosureCancel(c, current)
	c.JSON(200, gin.H{"message": "已撤销注销申请", "data": buildClosureStatus(db, fresh)})
}

// ClosureStatus 查询当前账号的注销状态与剩余天数。
func ClosureStatus(c *gin.Context) {
	current := closureActor(c)
	if current == nil {
		c.JSON(401, gin.H{"message": "请重新登录"})
		return
	}
	db := archivedUserDB()
	if db == nil {
		c.JSON(500, gin.H{"message": "数据库不可用"})
		return
	}
	// 重新读一次：前端手里的是登录时缓存的 currentUser，状态可能已经变了。
	fresh, err := loadActiveUser(db, current.ID)
	if err != nil {
		c.JSON(404, gin.H{"message": "账号不存在"})
		return
	}
	c.JSON(200, gin.H{"data": buildClosureStatus(db, fresh)})
}

// errUserStatusChanged 表示状态前置条件不满足（并发/已被别处改动）。
var errUserStatusChanged = errors.New("账号状态已变更")

// updateUserStatusInTx 做一次状态迁移，可选带前置状态约束。
//
// requireFrom 非空时加上 `status = ?` 条件，这是**撤销**路径的关键：
// 用户在等待期内双击「撤销」、或管理员同时把账号禁用，无条件的 UPDATE 会
// 把 disabled 覆盖成 enabled —— 等于撤销注销顺手解封了账号。
//
// RowsAffected=0 说明前置条件不满足（账号已不在期望状态），
// 返回 errUserStatusChanged 让调用方回 400 而不是假装成功。
func updateUserStatusInTx(tx *gorm.DB, userID int32, toStatus, requireFrom string) error {
	if tx == nil {
		return errors.New("数据库不可用")
	}
	if userID <= 0 {
		return errors.New("账号参数错误")
	}
	requireFrom = strings.TrimSpace(requireFrom)
	query := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).Where("id = ?", userID)
	if requireFrom != "" {
		query = query.Where("status = ?", requireFrom)
	}
	result := query.Updates(map[string]any{"status": toStatus})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errUserStatusChanged
	}
	return nil
}

// buildClosureStatus 组装状态视图（含剩余天数与资格判定）。
func buildClosureStatus(db *gorm.DB, user entity.User) closureStatusView {
	view := closureStatusView{
		Status:         strings.TrimSpace(user.Status),
		Pending:        isUserClosurePending(&user),
		WaitDays:       closureWaitingDays,
		BalanceTotal:   user.Balance + user.GiftBalance,
		PhoneBound:     user.Phone != nil && strings.TrimSpace(*user.Phone) != "",
		RequiresTicket: user.Balance+user.GiftBalance > closureBalanceFreezeThreshold,
	}
	view.Eligible = view.PhoneBound
	if !view.PhoneBound {
		view.IneligibleReason = "账号没有绑定手机号，不支持自助注销，请提交工单由管理员处理"
	}
	if view.Pending {
		if requestedAt := closureRequestedAt(db, user.ID); requestedAt != nil {
			view.RequestedAt = requestedAt
			view.RemainingDays = remainingClosureDays(*requestedAt, view.WaitDays, time.Now())
		} else {
			// 找不到申请日志（历史数据 / 日志被清理）：按「刚申请」处理，
			// 宁可让用户多看几天倒计时，也不要显示负剩余天数让人以为已经到期。
			view.RemainingDays = view.WaitDays
		}
	}
	return view
}

// remainingClosureDays 计算剩余天数，向上取整，最小 0。
//
// 向上取整是刻意的：剩余几小时时显示「1 天」而不是「0 天」——
// 后者看起来像已经到期，但流程并没有执行。
func remainingClosureDays(requestedAt time.Time, waitDays int, now time.Time) int {
	if waitDays <= 0 {
		return 0
	}
	deadline := requestedAt.AddDate(0, 0, waitDays)
	if !now.Before(deadline) {
		return 0
	}
	remaining := deadline.Sub(now)
	days := int(remaining.Hours() / 24)
	if remaining%(24*time.Hour) != 0 {
		days++
	}
	return days
}

// closureRequestedAt 从 logs 表取该用户最近一次「申请注销」的时刻。
//
// logs 行是申请事实的唯一权威来源（见 closureStatusView.RequestedAt 的注释）。
// 读不到（无表 / 无记录）返回 nil，由调用方给默认值。
func closureRequestedAt(db *gorm.DB, userID int32) *time.Time {
	if db == nil || userID <= 0 {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLog) {
		return nil
	}
	var entry entity.Log
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("module = ? AND action = ? AND user_id = ?", accountLogModule, AccountActionClosure, userID).
		Order("id desc").Take(&entry).Error
	if err != nil {
		return nil
	}
	createdAt := entry.CreateTime
	return &createdAt
}

// createClosureBalanceTicket 为「注销时仍有余额」自动建一张工单（45 号 Q8）。
//
// 工单内容刻意写清金额与调用方信息：管理员看到工单就知道要处理钱，
// 不必再回归档页对照。
func createClosureBalanceTicket(db *gorm.DB, user entity.User, total float64) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库不可用")
	}
	subject := "注销申请：账号存在余额，请人工处理"
	body := fmt.Sprintf(
		"用户 %s(ID=%d) 申请注销账号，当前余额合计 %.2f 元（本金 %.2f + 赠送 %.2f），超过自动处理阈值 %.0f 元。\n"+
			"账号已进入注销等待期（冻结消费）。请核对退款或结转方案后处理，处理完成再归档账号。",
		user.Username, user.ID, total, user.Balance, user.GiftBalance, closureBalanceFreezeThreshold,
	)
	detail, err := service.CreateTicketWithDB(db.Session(&gorm.Session{NewDB: true}), &user, service.CreateTicketInput{
		Subject: subject,
		Body:    body,
	})
	if err != nil {
		return 0, err
	}
	return detail.Ticket.ID, nil
}

// recordAccountClosureRequest 记录「用户申请注销」（Action 51）。
//
// 77 §5 第 5 条：自助操作的 operator 就是**本人**，所以 UserID 记用户自己。
func recordAccountClosureRequest(c *gin.Context, actor *entity.User, view closureStatusView) {
	if actor == nil {
		return
	}
	snapshot := service.ArchiveUserSnapshot(*actor)
	snapshot["waitDays"] = view.WaitDays
	snapshot["balanceTotal"] = view.BalanceTotal
	snapshot["requiresTicket"] = view.RequiresTicket
	if view.TicketID != nil {
		snapshot["ticketId"] = *view.TicketID
	}
	description := fmt.Sprintf("用户申请注销账号：%s(ID=%d)，等待期 %d 天", actor.Username, actor.ID, view.WaitDays)
	writeAccountOperationLog(c, actor, AccountActionClosure, description, snapshot)
	recordAccountAudit(c, actor, accountLogModule, AccountActionClosure, description, map[string]any{
		"targetUserId": actor.ID,
		"newStatus":    userStatusClosurePending,
		"waitDays":     view.WaitDays,
		"ticketId":     view.TicketID,
	})
}

// recordAccountClosureCancel 记录「撤销注销申请」（Action 52）。
func recordAccountClosureCancel(c *gin.Context, actor *entity.User) {
	if actor == nil {
		return
	}
	description := fmt.Sprintf("用户撤销注销申请：%s(ID=%d)", actor.Username, actor.ID)
	writeAccountOperationLog(c, actor, AccountActionCancel, description, service.ArchiveUserSnapshot(*actor))
	recordAccountAudit(c, actor, accountLogModule, AccountActionCancel, description, map[string]any{
		"targetUserId": actor.ID,
		"newStatus":    userStatusEnabled,
	})
}
