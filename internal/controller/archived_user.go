package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 已归档账号管理（task_doc/76 §3.2.3 与 §3.4）。
//
// 三个接口：
//
//	GET  /adapter-service/admin/archived-users                 列表（分页 + 归档原因 / 时间 / 关键字）
//	GET  /adapter-service/admin/archived-users/:id             详情（含该用户的绑定记录历史）
//	POST /adapter-service/admin/archived-users/:id/restore     恢复
//
// # 三条不可协商的前提
//
//  1. **所有归档查询必须 Unscoped**。users.deleted_at 是 gorm.DeletedAt，
//     dao.User 的默认查询会自动排除已归档行 —— 不加 Unscoped 这个列表永远是空的，
//     而且不报任何错（表现为「归档了但管理页看不到」）。
//  2. **用户名冲突必须主动检查**。users.username 没有数据库唯一索引
//     （只有 phone / wechat_id 有），两个同名用户可以共存；靠数据库报错兜底
//     的结果是「恢复成功、两个 zhangsan」，界面分不清谁是谁（76 §3.4.1）。
//  3. **冲突必须二次确认**。已在用的用户名不能被静默改名；409 响应里就要给出
//     已经校验过不重复的 suggestedUsername，让管理员确认前就看到「会变成什么」。
//
// # 日志
//
// 恢复是账户域操作，两个通道都写（task_doc/77 §3.2.4）：
// logs 表（LogList 页可见，Action 54）+ AccountAuditEvent。
// logs.user_id 是**操作者**，被操作账号的快照进 OldAnswer，且不含 password hash。

const (
	archivedUserDefaultPageSize = 10
	archivedUserMaxPageSize     = 100
	// archivedUserRestoreMaxAttempts 是随机用户名后缀的尝试上限（76 §3.4.4）。
	//
	// 100 次仍冲突说明数据异常，必须返回 500 让人介入 ——
	// 静默用一个可能重复的名字会把「谁是谁」的问题留给客服。
	archivedUserRestoreMaxAttempts = 100
	// archivedUserRestoredNameBase 是原用户名为空时的兜底词干。
	archivedUserRestoredNameBase = "user"
	// archivedUserRestoredNameMark 是改名标记。沿用 76 §3.4.4 的
	// 「原名_restored_N」格式：客服能口述，用户也能看出「是被恢复的旧号」。
	archivedUserRestoredNameMark = "_restored_"
)

// errArchivedUserUsernameExhausted 表示 100 次随机用户名全部被占用。
var errArchivedUserUsernameExhausted = errors.New("用户名冲突无法自动解决")

// archivedUserRow 是列表 / 详情的投影：users 的全部可见字段 + 归档三列。
//
// 为什么需要这个额外的结构体：归档元数据（原因 / 时点 / 操作者）声明在
// entity.UserArchive 上（它不进 entity.User，见 entity/user_archive.go 的注释），
// 直接查 entity.User 拿不到这三列。用匿名嵌入把 users.* 展开，
// GORM 会把同名的额外列填进外层字段；TableName 与实体别名一致，指向同一张表。
type archivedUserRow struct {
	entity.User
	ArchivedReason   string     `gorm:"column:archived_reason" json:"archivedReason"`
	ArchivedAt       *time.Time `gorm:"column:archived_at" json:"archivedAt,omitempty"`
	ArchivedByUserID int32      `gorm:"column:archived_by_user_id" json:"archivedByUserId"`
}

func (archivedUserRow) TableName() string { return entity.TableNameUser }

// archivedUserBindingView 是详情页里的一行绑定记录。
//
// Value 一律脱敏下发：绑定值是账号身份凭据，管理页只需要「能不能认出来」。
type archivedUserBindingView struct {
	ID               int64      `json:"id"`
	Kind             string     `json:"kind"`
	ValueMasked      string     `json:"valueMasked"`
	State            string     `json:"state"`
	ReleaseReason    string     `json:"releaseReason"`
	ReleaseSource    string     `json:"releaseSource"`
	UsernameSnapshot string     `json:"usernameSnapshot"`
	BoundAt          *time.Time `json:"boundAt,omitempty"`
	ReleasedAt       *time.Time `json:"releasedAt,omitempty"`
	OperatorUserID   int32      `json:"operatorUserId"`
	CreatedAt        time.Time  `json:"createdAt"`
}

// archivedUserDB 取一个干净的句柄。
//
// NewDB 是必需的：dao.User 的 Statement 带着 users 的 model/schema，
// 拿它去查另一个 model（binding record / log）会让 GORM 用错误的字段索引
// 反射取值并 panic（见 adminLoginDB 的同类注释）。
func archivedUserDB() *gorm.DB {
	if dao.User == nil {
		return nil
	}
	return dao.User.UnderlyingDB().Session(&gorm.Session{NewDB: true})
}

// ArchivedUserList 列出已归档账号（分页 + 筛选）。
func ArchivedUserList(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser, _ := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{"message": "无权限查看已归档账号"})
		return
	}
	db := archivedUserDB()
	if db == nil {
		c.JSON(500, gin.H{"message": "数据库不可用"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", strconv.Itoa(archivedUserDefaultPageSize)))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > archivedUserMaxPageSize {
		pageSize = archivedUserDefaultPageSize
	}
	startTime, err := parseArchivedUserTimeBound(c.Query("startTime"), false)
	if err != nil {
		c.JSON(400, gin.H{"message": "开始时间格式错误"})
		return
	}
	endTime, err := parseArchivedUserTimeBound(c.Query("endTime"), true)
	if err != nil {
		c.JSON(400, gin.H{"message": "结束时间格式错误"})
		return
	}

	query := buildArchivedUserListQuery(
		withDatabasePriority(db, databaseRequestContext(c), dbqueue.PriorityLow),
		c.Query("reason"), c.Query("keyword"), startTime, endTime,
	)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		logger.SysError("统计已归档账号失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(400, gin.H{"message": "获取已归档账号列表失败"})
		return
	}
	var rows []archivedUserRow
	if err := query.Order("deleted_at desc, id desc").Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		logger.SysError("查询已归档账号失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(400, gin.H{"message": "获取已归档账号列表失败"})
		return
	}
	c.JSON(200, gin.H{
		"data":     rows,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// buildArchivedUserListQuery 组装归档列表的筛选条件。
//
// ⚠️ Unscoped() 是**功能本体**而不是优化：users.deleted_at 是 gorm.DeletedAt，
// 默认查询会带上 `deleted_at IS NULL`，把这里要查的行全部过滤掉。
// 抽出独立函数是为了让单测能直接断言「用与不用 Unscoped 的差别」。
func buildArchivedUserListQuery(db *gorm.DB, reason, keyword string, from, to *time.Time) *gorm.DB {
	query := db.Session(&gorm.Session{NewDB: true}).Unscoped().
		Model(&archivedUserRow{}).
		Where("deleted_at IS NOT NULL")
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		query = query.Where("archived_reason = ?", trimmed)
	}
	if from != nil {
		query = query.Where("deleted_at >= ?", *from)
	}
	if to != nil {
		query = query.Where("deleted_at < ?", *to)
	}
	// 关键词按「纯子串」匹配（不使用 LIKE 通配符），与用户列表同约定。
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

// parseArchivedUserTimeBound 解析时间筛选边界。
//
// 接受 "2006-01-02" 与 RFC3339（前端日期选择器两种都可能给）。
// 日期型**上限**取次日零点并用 `<` 比较：否则「结束日期 = 今天」会把今天
// 归档的账号全部漏掉（时间戳带时分秒，永远大于当天零点）。
func parseArchivedUserTimeBound(raw string, endOfRange bool) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.Local); err == nil {
		if endOfRange {
			parsed = parsed.AddDate(0, 0, 1)
		}
		return &parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, time.Local); err == nil {
		if endOfRange {
			parsed = parsed.Add(time.Second)
		}
		return &parsed, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// loadArchivedUser 按 ID 取一个**已归档**账号。
//
// 两个条件缺一不可：Unscoped（否则查不到）+ deleted_at IS NOT NULL
// （否则能把活跃账号当归档账号「恢复」，等于把归档接口变成任意账号的写入口）。
func loadArchivedUser(db *gorm.DB, userID int32) (archivedUserRow, error) {
	var row archivedUserRow
	if db == nil || userID <= 0 {
		return row, gorm.ErrRecordNotFound
	}
	err := db.Session(&gorm.Session{NewDB: true}).Unscoped().
		Where("id = ? AND deleted_at IS NOT NULL", userID).Take(&row).Error
	return row, err
}

// ArchivedUserDetail 返回单个已归档账号 + 该用户的绑定记录历史。
func ArchivedUserDetail(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser, _ := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{"message": "无权限查看已归档账号"})
		return
	}
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	db := archivedUserDB()
	if db == nil {
		c.JSON(500, gin.H{"message": "数据库不可用"})
		return
	}
	row, err := loadArchivedUser(db, int32(userID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"message": "已归档账号不存在"})
		return
	}
	if err != nil {
		logger.SysError("读取已归档账号失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "读取已归档账号失败"})
		return
	}
	bindings, err := loadArchivedUserBindings(db, row.ID)
	if err != nil {
		logger.SysError("读取归档账号绑定记录失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "读取绑定记录失败"})
		return
	}
	c.JSON(200, gin.H{"data": row, "bindings": bindings})
}

// loadArchivedUserBindings 返回该用户的**全部**绑定记录（含已释放）。
//
// 详情页要回答的是「这个号以前绑过什么、怎么释放的」，
// 所以不能只看 active —— 归档时活跃绑定已经被释放成 released 了。
func loadArchivedUserBindings(db *gorm.DB, userID int32) ([]archivedUserBindingView, error) {
	views := make([]archivedUserBindingView, 0, 4)
	if db == nil || userID <= 0 {
		return views, nil
	}
	// 绑定表可能还没上线（空库首次启动 / 精简单测库）：返回空列表而不是 500，
	// 否则「详情打不开」会让人以为归档数据坏了。
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return views, nil
	}
	var rows []entity.UserBindingRecord
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("user_id = ?", userID).Order("id desc").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		views = append(views, archivedUserBindingView{
			ID: row.ID, Kind: row.Kind, ValueMasked: maskArchivedBindingValue(row.Kind, row.Value),
			State: row.State, ReleaseReason: row.ReleaseReason, ReleaseSource: row.ReleaseSource,
			UsernameSnapshot: row.UsernameSnapshot, BoundAt: row.BoundAt, ReleasedAt: row.ReleasedAt,
			OperatorUserID: row.OperatorUserID, CreatedAt: row.CreatedAt,
		})
	}
	return views, nil
}

// maskArchivedBindingValue 脱敏绑定值。
//
// 手机号复用账户审计的掩码（138****0000）保持口径一致；
// 其余（微信 ID 等）保留首尾各两位，中间固定四个星号。
func maskArchivedBindingValue(kind, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if kind == entity.BindingKindPhone {
		if masked := service.MaskAuditPhone(trimmed); masked != "" {
			return masked
		}
	}
	runes := []rune(trimmed)
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:2]) + "****" + string(runes[len(runes)-2:])
}

// ArchivedUserRestore 恢复一个已归档账号。
//
// 冲突语义（76 §3.4.3）：
//
//	用户名未占用            任意 confirmRename   → 200 恢复成功
//	用户名被占用            confirmRename=false  → 409 + 冲突信息（含建议用户名）
//	用户名被占用            confirmRename=true   → 200 恢复成功，用户名改为原名_restored_N
//	100 次候选名仍冲突      任意                 → 500（不静默用可能重复的名字）
func ArchivedUserRestore(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser, _ := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(403, gin.H{"message": "无权限恢复账号"})
		return
	}
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}

	var payload struct {
		// ConfirmRename 缺省 false：第一次请求只探冲突，不改任何数据。
		ConfirmRename bool `json:"confirmRename"`
	}
	// 允许空 body（前端首次探冲突时可能不带 body）：只有解析失败才算参数错误。
	if c.Request != nil && c.Request.Body != nil {
		if bindErr := c.ShouldBindJSON(&payload); bindErr != nil && !errors.Is(bindErr, io.EOF) {
			c.JSON(400, gin.H{"message": "参数错误"})
			return
		}
	}

	db := archivedUserDB()
	if db == nil {
		c.JSON(500, gin.H{"message": "数据库不可用"})
		return
	}
	target, err := loadArchivedUser(db, int32(userID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"message": "已归档账号不存在"})
		return
	}
	if err != nil {
		logger.SysError("读取已归档账号失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "恢复账号失败"})
		return
	}

	// 主动检查用户名占用 —— 不能等数据库报错（username 无唯一索引）。
	occupiedBy, err := usernameOccupiedBy(db, target.Username, target.ID)
	if err != nil {
		logger.SysError("检查用户名占用失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "恢复账号失败"})
		return
	}
	newUsername := target.Username
	renamed := false
	if occupiedBy > 0 {
		suggested, suggestErr := nextAvailableRestoredUsername(db, target.Username, target.ID)
		if suggestErr != nil {
			// 100 次仍冲突：数据异常，交给人处理。
			logger.SysError(fmt.Sprintf("恢复账号 %d 时用户名冲突无法自动解决: %s",
				target.ID, logger.RedactSecrets(suggestErr.Error())))
			c.JSON(500, gin.H{"message": "用户名冲突无法自动解决，请先手动修改"})
			return
		}
		if !payload.ConfirmRename {
			// 409 必须带**已校验不重复**的建议名：让管理员在确认框里就看到
			// 「会变成什么」，而不是确认后再撞一次。
			c.JSON(409, gin.H{
				"conflict":          "username",
				"message":           "原用户名已被其他账号使用，需要二次确认后改用建议用户名",
				"currentUsername":   target.Username,
				"occupiedByUserId":  occupiedBy,
				"suggestedUsername": suggested,
			})
			return
		}
		newUsername = suggested
		renamed = true
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return restoreArchivedUserInTx(tx, target, newUsername, renamed)
	}); err != nil {
		logger.SysError("恢复归档账号失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(500, gin.H{"message": "恢复账号失败"})
		return
	}

	// 日志写在恢复之后：OldAnswer 需要记录「原用户名 / 新用户名」这两个事实，
	// 它们只有恢复成功才成立。写日志失败只记 SysError，不回滚恢复。
	recordAccountRestore(c, currentUser, target, newUsername, renamed)

	restored := target.User
	restored.Username = newUsername
	if strings.EqualFold(strings.TrimSpace(restored.Status), userStatusClosurePending) {
		restored.Status = userStatusEnabled
	}
	// 重新读一次，保证响应里是库里的最终状态（含 status 复位）。
	if fresh, loadErr := loadActiveUser(db, target.ID); loadErr == nil {
		restored = fresh
	}
	c.JSON(200, gin.H{
		"message": "恢复账号成功",
		"data":    restored,
		"renamed": renamed,
	})
}

// restoreArchivedUserInTx 清掉归档标记，必要时改名 / 解冻注销等待期。
func restoreArchivedUserInTx(tx *gorm.DB, target archivedUserRow, newUsername string, renamed bool) error {
	if tx == nil {
		return errors.New("数据库不可用")
	}
	updates := map[string]any{"deleted_at": nil}
	// 逐列探测（与 service.ArchiveUserInTx 同一纪律）：缺列的部署里只恢复核心语义，
	// 不让「少一列」变成「恢复整个失败」。
	if tx.Migrator().HasColumn(entity.TableNameUser, "archived_reason") {
		updates["archived_reason"] = ""
	}
	if tx.Migrator().HasColumn(entity.TableNameUser, "archived_at") {
		updates["archived_at"] = nil
	}
	if tx.Migrator().HasColumn(entity.TableNameUser, "archived_by_user_id") {
		updates["archived_by_user_id"] = 0
	}
	if renamed {
		updates["username"] = newUsername
	}
	// 注销等待期内归档的账号恢复后回到 enabled：
	// 否则用户永远停在 closure_pending（不能消费、界面仍显示「已申请注销」），
	// 表现为「恢复了但没生效」。
	if strings.EqualFold(strings.TrimSpace(target.Status), userStatusClosurePending) {
		updates["status"] = userStatusEnabled
	}
	result := tx.Session(&gorm.Session{NewDB: true}).Unscoped().
		Model(&entity.UserArchive{}).
		Where("id = ? AND deleted_at IS NOT NULL", target.ID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// 并发恢复 / 已被别处恢复：不当作成功，避免重复写日志与重复改名。
		return errors.New("账号已不在归档状态")
	}
	return nil
}

// usernameOccupiedBy 返回占用该用户名的**未归档**账号 ID（0 表示未被占用）。
//
// ⚠️ 必须主动检查（76 §3.4.1 / 红线 10）：users.username **没有**唯一索引，
// 两个同名用户可以共存，数据库不会替我们报错。
//
// 查询走 entity.User（带 GORM 软删除过滤）⇒ 已归档账号不挡路。
// 这正是 76 §3.4.6「归档时保留原用户名」的直接结果：别人可以注册同名，
// 恢复时才需要处理冲突。
func usernameOccupiedBy(db *gorm.DB, username string, selfID int32) (int32, error) {
	name := strings.TrimSpace(username)
	if db == nil || name == "" {
		return 0, nil
	}
	var occupied entity.User
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("username = ? AND id <> ?", name, selfID).Take(&occupied).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return occupied.ID, nil
}

// nextAvailableRestoredUsername 生成「原名_restored_N」并逐个校验占用。
//
// 1..100 每次都在真实库上查一次：候选名必须**当场确认可用**，
// 否则前端确认后仍可能再撞一次（红线 12）。
func nextAvailableRestoredUsername(db *gorm.DB, original string, selfID int32) (string, error) {
	for attempt := 1; attempt <= archivedUserRestoreMaxAttempts; attempt++ {
		candidate := buildRestoredUsername(original, attempt)
		occupiedBy, err := usernameOccupiedBy(db, candidate, selfID)
		if err != nil {
			return "", err
		}
		if occupiedBy == 0 {
			return candidate, nil
		}
	}
	return "", errArchivedUserUsernameExhausted
}

// buildRestoredUsername 拼「原名_restored_N」，原名超长时按字符截断。
//
// 长度按 **rune** 计（与 util.UsernameMaxLength / varchar(255) 的口径一致）；
// 后缀只含小写字母、下划线与数字，符合既有用户名规则（中文/英文/数字/下划线/@）。
func buildRestoredUsername(original string, attempt int) string {
	suffix := fmt.Sprintf("%s%d", archivedUserRestoredNameMark, attempt)
	base := strings.TrimSpace(original)
	if base == "" {
		base = archivedUserRestoredNameBase
	}
	limit := util.UsernameMaxLength - utf8.RuneCountInString(suffix)
	if limit < 1 {
		limit = 1
	}
	runes := []rune(base)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + suffix
}

// loadActiveUser 读一个**未归档**账号（恢复后回读用）。
func loadActiveUser(db *gorm.DB, userID int32) (entity.User, error) {
	var user entity.User
	if db == nil || userID <= 0 {
		return user, gorm.ErrRecordNotFound
	}
	err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", userID).Take(&user).Error
	return user, err
}

// recordAccountRestore 记录一次「恢复已归档账号」（Action 54）。
//
// 两个通道都写（77 §3.2.4）；UserID 是**操作者（管理员）**，
// 被操作账号的信息进 OldAnswer，且不含 password hash。
//
// 改过名时 Description 与 OldAnswer 都要写明原用户名与新用户名（76 §3.4.5）：
// 用户下次登录会发现自己名字变了，没有留痕客服无法解释。
func recordAccountRestore(c *gin.Context, operator *entity.User, target archivedUserRow, newUsername string, renamed bool) {
	if operator == nil {
		return
	}
	snapshot := service.ArchiveUserSnapshot(target.User)
	snapshot["archivedReason"] = target.ArchivedReason
	if target.ArchivedAt != nil {
		snapshot["archivedAt"] = *target.ArchivedAt
	}
	snapshot["originalUsername"] = target.Username
	snapshot["newUsername"] = newUsername

	description := fmt.Sprintf("恢复已归档账号：%s(ID=%d)", target.Username, target.ID)
	if renamed {
		description = fmt.Sprintf("恢复已归档账号：%s(ID=%d)；原用户名 %s 已被占用，已改为 %s",
			target.Username, target.ID, target.Username, newUsername)
	}
	writeAccountOperationLog(c, operator, AccountActionRestore, description, snapshot)
	recordAccountAudit(c, operator, accountLogModule, AccountActionRestore, description, map[string]any{
		"targetUserId":   target.ID,
		"targetUsername": target.Username,
		"newUsername":    newUsername,
		"renamed":        renamed,
		"archivedReason": target.ArchivedReason,
		"restoredAt":     time.Now(),
	})
}

// writeAccountOperationLog 把一次账户域操作写进 logs 表（LogList 页可见）。
//
// 与 recordAccountAudit 的分工见 77 §3.2.4：
// logs 面向运营查看（用户明确要求的「日志列表」），AccountAudit 面向结构化查询。
// 写失败只记 SysError，**不回滚业务操作** —— 日志是辅助信息，
// 不该让「日志写不进去」变成「操作失败」。
func writeAccountOperationLog(c *gin.Context, operator *entity.User, action int32, description string, oldAnswer map[string]any) {
	if operator == nil {
		return
	}
	encoded := []byte("{}")
	if oldAnswer != nil {
		if payload, err := json.Marshal(oldAnswer); err == nil {
			encoded = payload
		}
	}
	clientIP := ""
	if c != nil {
		clientIP = strings.TrimSpace(c.ClientIP())
	}
	entry := entity.Log{
		Qid:    0, // 非题库操作
		Module: accountLogModule, Action: action,
		UserID: operator.ID, Username: operator.Username,
		Description: description, OldAnswer: string(encoded), NewAnswer: "",
		ClientIP: clientIP, CreateTime: time.Now(),
	}
	if err := dao.Log.Create(&entry); err != nil {
		logger.SysError("写账户操作日志失败: " + logger.RedactSecrets(err.Error()))
	}
}
