package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 用户名释放（归档）+ 恢复时冲突改名（task_doc/106 Q4、task_doc/76 §3.4、
// task_doc/45 Q10）。
//
// # 一、「释放」在这里的准确含义
//
// 用户决策（106 §3 Q4）是「归档释放用户名，让原用户名可被新用户注册」。
// 落地形态**不是**把 users.username 改写成一个占位串，理由是三条事实：
//
//  1. users.username **没有唯一索引**（users.gen.go:18 只有 varchar(255)），
//     所以「占位」从来不是数据库层面的冲突来源 —— 真正拦住同名注册的是
//     应用层的重名检查（controller/user.go:608
//     `dao.User.Where(dao.User.Username.Eq(username)).First()`）。
//  2. dao.User 绑定的是 entity.User，而它在 users.gen.go:88 上有
//     `gorm.DeletedAt` —— 归档行的 deleted_at 非空，**任何**经 dao.User
//     的查询都会自动带上 `deleted_at IS NULL`，因此归档账号从归档那一刻起
//     就已经不再参与重名检查。
//  3. 反过来，改写 username 会**销毁恢复所需的信息**：归档行一旦被改名，
//     「这个账号原来叫什么」就只剩下日志可查，而恢复接口必须知道原名才能
//     判断冲突、也才能把名字还原给用户。
//
// 所以本文件的「释放」= **确认并留痕该用户名已不占位**。它有三个可验证的
// 效果（见 username_release_test.go）：
//
//   - 归档后同名新账号可以被创建（与注册路径使用**同一条**查询表达式）；
//   - 归档行仍能用 Unscoped 读到原用户名，供恢复使用；
//   - 释放动作在 logs 表留一条可追溯记录（用既有日志设施，不新建表）。
//
// 这条口径与 task_doc/76 §3.4.6「归档时保留原用户名 / 他人可注册同名 /
// 恢复时按 §3.4.2 处理冲突」完全一致，与 45 号 Q10「释放」不矛盾 ——
// 「释放」是实现效果，「保留原用户名」是实现手段。
//
// # 二、恢复时的冲突改名
//
//	归档行用户名为空            → 不做改名判断（保留现状，不凭空造名字）
//	原名未被未归档账号占用      → 保留原名
//	原名已被占用                → 生成「原名_restored_N」，N 从 1 递增
//	N 超过 UsernameReleaseMaxAttempts(10) → ErrUsernameReleaseExhausted
//
// 候选名每次都在真实库上查一次占用；耗尽时**不留半改状态** ——
// 本文件所有判定函数都是只读的，真正的写入由调用方在自己的事务里完成。

const (
	// UsernameReleaseMaxAttempts 是恢复改名时候选名的重试上限。
	//
	// 为什么是 10 而不是更大：候选名是「原名_restored_N」的**确定性递增序列**，
	// 连续 10 个都被占意味着该词干已被批量占用或数据被污染，再试下去只是把
	// 一次注定失败的恢复拖长（每次都要真实查库）。10 次足以覆盖
	// 「同名账号被反复注销 / 恢复」的真实场景。
	UsernameReleaseMaxAttempts = 10

	// UsernameRestoredMark 是恢复改名标记，格式「原名_restored_N」（76 §3.4.4）。
	//
	// 为什么不用纯随机名（user_a8f3k2 那种）：用户看到自己的账号名变成乱码
	// 会困惑，客服也无法在电话里口述。76 §3.4.4 明确否掉了纯随机方案。
	UsernameRestoredMark = "_restored_"

	// UsernameRestoredFallbackStem 是原用户名为空时的兜底词干。
	UsernameRestoredFallbackStem = "user"

	// usernameReleaseAuditModule / usernameReleaseAuditAction 让「释放」这个
	// 动作走既有 logs 表留痕（红线：不新建表）。
	//
	// action 取 53，与 controller.AccountActionArchive(53)「注销到期归档」对齐。
	// 这里独立声明常量而**不**反向 import controller：controller 已经 import
	// service，反向依赖会形成循环。
	usernameReleaseAuditModule          = "account"
	usernameReleaseAuditAction    int32 = 53
	usernameReleaseAuditDedicated       = "用户名释放"
)

var (
	// ErrUsernameReleaseExhausted 表示候选用户名在重试上限内全部被占用。
	//
	// 必须是**独立可判定**的错误（不是裸 fmt.Errorf）：调用方要把它映射成
	// 「500 + 请人工处理」，而把其它数据库错误映射成同一句提示会让真正的
	// 故障（表不存在 / 连接断）被误读成「名字被占满了」。
	ErrUsernameReleaseExhausted = errors.New("用户名冲突无法自动解决")

	// ErrUsernameReleaseInvalid 表示生成的候选用户名不符合既有用户名校验规则。
	ErrUsernameReleaseInvalid = errors.New("生成的用户名不合法")
)

// usernameReleaseCandidate 生成第 attempt 个候选用户名。
//
// 抽成包级变量是为了让测试能**注入撞库**：把前几次替换成一个已知被占用的
// 名字，才能真正验证「撞了就重试」，而不是「序号递增恰好没撞」。
//
// 生产路径永远指向 BuildRestoredUsername；测试注入后必须 defer 还原。
var usernameReleaseCandidate = func(original string, attempt int) string {
	return BuildRestoredUsername(original, attempt)
}

// BuildRestoredUsername 拼「原名_restored_N」。
//
// # 两条必须同时成立的约束
//
//  1. **总长 ≤ util.UsernameMaxLength**（255 rune，与 users.username
//     varchar(255) 一致）。原用户名接近上限时按 rune 截断原名前段，
//     **后缀必须保留** —— 截掉后缀就变成「随便一个名字」，客服更没法口述。
//  2. **必须满足 util.ValidateRestrictedName**（中文 / 英文 / 数字 / 下划线 / @）。
//     历史用户名里可能存在该规则之外的字符（users 加校验之前写进库的行，
//     例如带 `-` 的名字），直接拼接会产出一个**无法通过注册校验**的名字。
//     因此先把词干里的非法字符替换成 `_`，后缀本身只含小写字母 / 下划线 / 数字。
func BuildRestoredUsername(original string, attempt int) string {
	if attempt < 1 {
		attempt = 1
	}
	suffix := fmt.Sprintf("%s%d", UsernameRestoredMark, attempt)
	base := sanitizeUsernameStem(original)
	if base == "" {
		base = UsernameRestoredFallbackStem
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

// sanitizeUsernameStem 把词干里不符合用户名字符集的字符替换成下划线。
//
// 只做「替换」不做「删除」：删除会让两个不同原名收敛成同一个词干，
// 而替换保留了字符位置信息（用户与客服都还能认出是哪个名字）。
func sanitizeUsernameStem(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	for _, character := range trimmed {
		if character == '_' || character == '@' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			isHanCharacter(character) {
			builder.WriteRune(character)
			continue
		}
		builder.WriteRune('_')
	}
	return builder.String()
}

// isHanCharacter 判断是否汉字。
//
// 与 util.ValidateRestrictedName 内部的 unicode.Is(unicode.Han, ...) 是
// **同一个判定**（不是抄一份近似）——否则两处规则漂移后会产出
// 「生成时判合法、注册时判非法」的名字。
func isHanCharacter(character rune) bool {
	return unicode.Is(unicode.Han, character)
}

// ValidateUsernameCandidate 断言候选用户名满足既有用户名校验规则。
//
// 这是约束「随机新名必须合法」的**唯一**判据来源：直接复用注册 / 改资料
// 路径用的同一个函数（util.ValidateRestrictedName），不另写一套字符集。
func ValidateUsernameCandidate(name string) error {
	if err := util.ValidateRestrictedName(name, util.UsernameMaxLength, "用户名"); err != nil {
		return fmt.Errorf("%w: %v", ErrUsernameReleaseInvalid, err)
	}
	return nil
}

// UsernameOccupiedBy 返回占用该用户名的**未归档**账号 ID（0 表示未被占用）。
//
// ⚠️ 必须显式查询（76 §3.4.1）：users.username 没有唯一索引，两个同名用户可以
// 共存，数据库不会替我们报错。
//
// 查询走 entity.User（带 GORM 软删除过滤）⇒ 已归档账号不挡路。
// 这正是「归档即释放」的实现基础：归档行不再参与重名判定。
func UsernameOccupiedBy(db *gorm.DB, username string, selfID int32) (int32, error) {
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

// UsernameReleasedForRegistration 回答「这个用户名现在能不能被新账号注册」。
//
// 判据与注册路径（controller/user.go:608）**同一条查询**：
// 存在一个未归档的同名账号 → 不能注册。
func UsernameReleasedForRegistration(db *gorm.DB, username string) (bool, error) {
	occupied, err := UsernameOccupiedBy(db, username, 0)
	if err != nil {
		return false, err
	}
	return occupied == 0, nil
}

// NextAvailableRestoredUsernameWithDB 生成「原名_restored_N」并逐个校验占用。
//
// 每个候选都**当场在真实库上确认可用**（不能只生成不查：前端确认后仍可能
// 再撞一次）。返回第三个值 attempts 是实际尝试次数，便于日志与排障。
func NextAvailableRestoredUsernameWithDB(db *gorm.DB, original string, selfID int32) (string, int, error) {
	if db == nil {
		return "", 0, fmt.Errorf("数据库不可用")
	}
	for attempt := 1; attempt <= UsernameReleaseMaxAttempts; attempt++ {
		candidate := usernameReleaseCandidate(original, attempt)
		// 合法性先于占用检查：非法名永远查不到占用，会白耗重试并最终
		// 报出误导性的「冲突无法解决」。
		if err := ValidateUsernameCandidate(candidate); err != nil {
			return "", attempt, err
		}
		occupiedBy, err := UsernameOccupiedBy(db, candidate, selfID)
		if err != nil {
			return "", attempt, err
		}
		if occupiedBy == 0 {
			return candidate, attempt, nil
		}
	}
	return "", UsernameReleaseMaxAttempts, ErrUsernameReleaseExhausted
}

// UsernameRelease 描述一次「归档时释放用户名」的结果。
type UsernameRelease struct {
	// Username 是归档行上的用户名（释放前后同名，见文件头注释）。
	Username string
	// Released 表示该用户名在归档后确实已可被新账号注册。
	Released bool
	// OccupiedByUserID 非 0 表示归档**之前**就已经有活跃账号用了同一个名字
	// （历史遗留的同名数据）。此时 Released=false —— 必须如实上报，
	// 不能因为「归档成功了」就默认名字已被释放。
	OccupiedByUserID int32
}

// ReleaseUsernameForArchiveInTx 在归档事务内释放用户名并留痕。
//
// # 为什么必须在归档的**同一个事务**里调用
//
// 释放的判据是「归档行的 deleted_at 非空」（软删除过滤是释放机制本身）。
// 放到归档事务之外，中间会存在一个「已记释放、但行还没归档」的窗口，
// 此时同名注册仍会被拦下，而日志已经宣称释放成功。
//
// # 为什么日志写失败**不**回滚归档
//
// 与 service.ArchiveUserInTx / controller.writeAccountOperationLog 同一纪律：
// 日志是辅助信息，不该让「日志写不进去」变成「用户删不掉」。
// 写失败只记 SysError。
//
// # 为什么占用探测失败要返回错误
//
// 那是真的读库失败（表缺失 / 连接断），继续归档等于在未知状态下宣称释放成功。
func ReleaseUsernameForArchiveInTx(tx *gorm.DB, user entity.User, archivedReason string, operatorUserID int32) (UsernameRelease, error) {
	release := UsernameRelease{Username: strings.TrimSpace(user.Username)}
	if tx == nil {
		return release, fmt.Errorf("数据库不可用")
	}
	if user.ID <= 0 {
		return release, fmt.Errorf("释放用户名缺少用户 ID")
	}
	// 空用户名无需释放，也不该在日志里留下一条「释放了空名字」的噪声。
	if release.Username == "" {
		release.Released = true
		return release, nil
	}

	occupiedBy, err := UsernameOccupiedBy(tx, release.Username, user.ID)
	if err != nil {
		return release, fmt.Errorf("检查用户名占用失败: %w", err)
	}
	release.OccupiedByUserID = occupiedBy
	release.Released = occupiedBy == 0

	description := fmt.Sprintf("%s：%s(ID=%d) 归档后不再占用该用户名", usernameReleaseAuditDedicated, release.Username, user.ID)
	message := "归档后该用户名可被新账号注册"
	if !release.Released {
		// 同名活跃账号已存在：释放对该名字**没有**额外效果（本来就撞着）。
		// 如实写进审计，否则事后看日志会以为「释放成功但注册仍被拒」是 bug。
		description = fmt.Sprintf("%s：%s(ID=%d) 归档；该用户名在此之前已被活跃账号 %d 占用",
			usernameReleaseAuditDedicated, release.Username, user.ID, occupiedBy)
		message = fmt.Sprintf("归档前已存在同名活跃账号 %d，该用户名不可被注册", occupiedBy)
	}
	event := AccountAuditEvent{
		UserID:      operatorUserID,
		Username:    release.Username,
		Module:      usernameReleaseAuditModule,
		Action:      usernameReleaseAuditAction,
		Description: description,
		// 只用 accountAuditFields 白名单里的键：未列出的键会被静默丢弃，
		// 写一个白名单外的键等于什么都没记（见 account_audit.go:27-39）。
		Request: map[string]any{
			"targetUserId":   user.ID,
			"username":       release.Username,
			"archivedReason": archivedReason,
		},
		Result: map[string]any{
			"status":  "success",
			"message": message,
		},
	}
	if writeErr := WriteAccountAuditWithDB(tx, event); writeErr != nil {
		logger.SysError("写用户名释放日志失败: " + logger.RedactSecrets(writeErr.Error()))
	}
	return release, nil
}

// RestoreUsernameDecision 是「恢复时该用哪个用户名」的完整结论。
//
// 这个结构体是**只读判定**的产物：它不写库。调用方拿到结论后在自己
// 的事务里决定写不写 username 列 —— 这样「重试耗尽」这类失败不会留下
// 半改状态（用户名改了、deleted_at 没清）。
type RestoreUsernameDecision struct {
	// Archived 是归档行上当前的用户名（可能是空串）。
	Archived string
	// Original 是用于冲突判定的原名。
	Original string
	// Final 是恢复后应写入的用户名。
	//
	// 空串是**有含义**的：表示「不要动 username 列」——
	// 对应「归档行用户名为空」这种历史脏数据，此时凭空生成一个名字
	// 会让用户看到一个从未属于他的账号名。
	Final string
	// Renamed 表示 Final 与 Original 不同（确实因冲突改了名）。
	Renamed bool
	// OccupiedByUserID 是占用原名的活跃账号（0 = 未占用）。
	OccupiedByUserID int32
	// Attempts 是生成候选名时的实际尝试次数（未改名时为 0）。
	Attempts int
}

// ResolveRestoreUsernameWithDB 判定恢复时应使用的用户名。
//
// 语义（106 Q4 / 76 §3.4.3）：
//
//	归档行用户名为空      → Final 为空串（不动 username 列）
//	原名未被占用          → Final = 原名，Renamed=false
//	原名已被占用          → Final = 原名_restored_N，Renamed=true
//	候选名全部被占用      → 返回 ErrUsernameReleaseExhausted（调用方映射成 500）
//
// selfID 是**被恢复账号自己**的 ID：占用判定必须排除自身，否则恢复一个
// 从未改过名的归档账号（它自己的行仍 Unscoped 存在）会误判成「被自己占用」。
// 注意本查询走 entity.User 带软删除过滤，归档行本身不会被查出来；
// 排除 selfID 是防御将来有人把这里改成 Unscoped。
func ResolveRestoreUsernameWithDB(db *gorm.DB, selfID int32, archivedUsername string) (RestoreUsernameDecision, error) {
	decision := RestoreUsernameDecision{Archived: strings.TrimSpace(archivedUsername)}
	if db == nil {
		return decision, fmt.Errorf("数据库不可用")
	}
	if decision.Archived == "" {
		// 「用户名空着」：不做改名判断。返回空 Final = 调用方不写 username 列，
		// 其余字段照常恢复（不能因为这一列脏了就把整条恢复拒掉）。
		return decision, nil
	}
	decision.Original = decision.Archived

	occupiedBy, err := UsernameOccupiedBy(db, decision.Original, selfID)
	if err != nil {
		return decision, err
	}
	decision.OccupiedByUserID = occupiedBy
	if occupiedBy == 0 {
		decision.Final = decision.Original
		return decision, nil
	}

	candidate, attempts, err := NextAvailableRestoredUsernameWithDB(db, decision.Original, selfID)
	if err != nil {
		// 把 attempts 一并带出去：耗尽与「候选名非法」是两种不同故障，
		// 上层要能区分（前者交人工，后者是数据/代码问题）。
		decision.Attempts = attempts
		return decision, err
	}
	decision.Final = candidate
	decision.Renamed = true
	decision.Attempts = attempts
	return decision, nil
}
