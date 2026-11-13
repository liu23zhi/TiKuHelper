package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 本文件实现「批量赠送 + 批量回滚」（112-*.md N3 / 117-*.md §4.4 / 118-*.md §6.1）。
//
// # 为什么不新建任务表
//
// 批次状态**全部从 billing_records 反推**：
//
//	该批次送了谁   request_id = 'admin_grant_<uid>_<anchor>'
//	送了多少       amount
//	谁失败了       「名单里有、但账单里没有」
//
// 这与 ⑥ 关键约束是同一件事：**幂等锚点是 billing_records.request_id 本身**，
// `idempotency_records` 只有 24h 保留期，拿它当批次状态的来源会让「昨天失败的批次
// 今天重试」直接丢失全部历史。副作用是「只重试失败项」变成了自然结果 ——
// 重放同一 batchId 时已成功的用户走幂等跳过、失败的用户才真正执行。
//
// # 八个行为口径（⑧ 关键约束）
//
//	排除已封禁与已归档    ensureAdminGrantUserAvailable + 预览阶段的显式统计
//	>100 人走异步         batchGrantAsyncThreshold
//	只重试失败项          同上，靠 request_id 幂等
//	暂停/中止不回滚       Pause / Abort 只停止后续处理，已赠送的不动
//	回滚不可中断          回滚循环**不读**任何暂停/中止标志
//	限速 10/s             batchGrantInterval（测试可置 0）
//	单笔 >10 元二次确认   AdminGrant 内部兜底
//	批次上限 1000 人      N3-e

const (
	// batchGrantAsyncThreshold 是「超过即强制异步」的人数门槛（117-*.md §4.4 N3-h）。
	batchGrantAsyncThreshold = 100

	// batchGrantMaxUsers 是单批次人数上限（112-*.md §3.8 N3-e）。
	batchGrantMaxUsers = 1000

	// batchGrantMaxTotalAmount 是单批次**总额**阈值（117-*.md §4.4 N3-g 的漏洞修补）。
	//
	// 只设单人阈值不够：1000 人 × 1 元 = 1000 元，单人 1 元永远不触发确认。
	batchGrantMaxTotalAmount = 1000.0

	// 批次状态机取值。
	batchGrantStatusRunning     = "running"
	batchGrantStatusPaused      = "paused"
	batchGrantStatusAborted     = "aborted"
	batchGrantStatusCompleted   = "completed"
	batchGrantStatusFailed      = "failed"
	batchGrantStatusRollingBack = "rolling_back"
	batchGrantStatusRolledBack  = "rolled_back"
)

// batchGrantInterval 是批量处理的限速间隔（10/s）。
//
// 测试里把它置 0：否则 100 人同步批次要跑 10 秒，而限速本身不属于被测语义。
// 用 var 而不是 const，正是为了让测试能改这一条。
var batchGrantInterval = 100 * time.Millisecond

// 批量赠送的三个哨兵错误。
var (
	// ErrBatchGrantInvalid 表示批次参数不合法。
	ErrBatchGrantInvalid = errors.New("批量赠送参数无效")
	// ErrBatchGrantConfirmRequired 表示批次触发二次确认但调用方未确认。
	ErrBatchGrantConfirmRequired = errors.New("本批次触发二次确认，需要确认后重试")
	// ErrBatchGrantNotFound 表示批次不存在（没有任何赠送流水）。
	ErrBatchGrantNotFound = errors.New("批次不存在")
)

// BatchGrantSelector 是批量赠送的用户来源（模式 A 手动列表 + 模式 B 条件筛选）。
//
// 两种模式互斥：UserIDs 非空时按名单，否则按条件筛。All=true 表示「筛选条件下的全选」。
type BatchGrantSelector struct {
	// UserIDs 是模式 A 的名单（去重后处理）。
	UserIDs []int32
	// All 为 true 时按下面的条件筛选全库（模式 B）。
	All bool
	// Statuses 是状态白名单；空 = 允许 enabled + disabled（banned 永远排除，见 N3-f）。
	Statuses []string
	// LevelID 非 0 时只取当前等级等于该值的用户（user_level_histories 最新一条）。
	LevelID int32
	// MinBalance / MaxBalance 按**可用合计**（balance + gift_balance）过滤，nil = 不限。
	//
	// 用合计而不是只看 balance：双桶模型下 balance=0 / gift=1 的账户是「有钱」的，
	// 按 balance 筛会把它们错分到「余额 0」档（43-*.md §2）。
	MinBalance *float64
	MaxBalance *float64
	// RegisteredFrom / RegisteredTo 是注册时间半开区间 [From, To)。
	RegisteredFrom *time.Time
	RegisteredTo   *time.Time
	// LastLoginFrom / LastLoginTo 是最后登录时间半开区间。从未登录（NULL）不命中。
	LastLoginFrom *time.Time
	LastLoginTo   *time.Time
}

// BatchGrantTarget 是预览里的一个可赠送用户。
type BatchGrantTarget struct {
	UserID   int32   `json:"userId"`
	Username string  `json:"username"`
	Balance  float64 `json:"balance"`
	Gift     float64 `json:"giftBalance"`
	// Available 是可用合计（balance + gift_balance）。
	Available float64 `json:"available"`
	// AlreadyGranted 表示该批次已给此人送过（重试场景），本次会走幂等跳过。
	AlreadyGranted bool `json:"alreadyGranted"`
}

// BatchGrantPreviewResult 是批量赠送的预览结果。
//
// 排除项必须**分开计数**：只报「排除 3 人」无法让管理员判断该不该放宽条件。
type BatchGrantPreviewResult struct {
	BatchID string `json:"batchId"`
	// Eligible 是最终会收到钱的人（已按 UserID 升序）。
	Eligible []BatchGrantTarget `json:"eligible"`
	// Total 是 Eligible 的人数。
	Total int `json:"total"`
	// UnitAmount / TotalAmount 是每人金额与合计金额。
	UnitAmount  float64 `json:"unitAmount"`
	TotalAmount float64 `json:"totalAmount"`
	// 排除统计。
	ExcludedBanned   int `json:"excludedBanned"`
	ExcludedArchived int `json:"excludedArchived"`
	ExcludedMissing  int `json:"excludedMissing"`
	// AlreadyGranted 是该批次已赠送过的人数（重试时 >0）。
	AlreadyGranted int `json:"alreadyGranted"`
	// ConfirmRequired / ConfirmReasons 是三层确认的判定结果（N3-g）。
	ConfirmRequired bool     `json:"confirmRequired"`
	ConfirmReasons  []string `json:"confirmReasons"`
	// Async 表示执行时会走异步（人数 > 100）。
	Async bool `json:"async"`
}

// BatchGrantExecuteRequest 是一次批量赠送的入参。
type BatchGrantExecuteRequest struct {
	Operator entity.User
	// BatchID 为空时自动生成；重试时必须传**同一个** BatchID。
	BatchID string
	// Selector 与预览时一致。
	Selector BatchGrantSelector
	Amount   float64
	Reason   string
	// Confirmed 由管理员完成二次确认后置 true。
	Confirmed bool
	// OnlyFailed 为 true 时只处理「上次失败」的人（名单里已成功的不再进循环）。
	OnlyFailed bool
	ClientIP   string
}

// BatchGrantFailure 是一个失败项。
type BatchGrantFailure struct {
	UserID int32  `json:"userId"`
	Reason string `json:"reason"`
}

// BatchGrantProgress 是批次进度（同步执行时即最终结果）。
type BatchGrantProgress struct {
	BatchID   string `json:"batchId"`
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Processed int    `json:"processed"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	// Amount 是**已实际送出**的金额合计（失败项不计）。
	Amount        float64             `json:"amount"`
	FailedDetails []BatchGrantFailure `json:"failedDetails,omitempty"`
	StartedAt     time.Time           `json:"startedAt"`
	FinishedAt    *time.Time          `json:"finishedAt,omitempty"`
}

// BatchGrantRollbackRequest 是一次批量回滚的入参。
type BatchGrantRollbackRequest struct {
	Operator entity.User
	BatchID  string
	Reason   string
	ClientIP string
}

// BatchGrantRollbackItem 是一次回滚里的单用户结果。
type BatchGrantRollbackItem struct {
	UserID int32 `json:"userId"`
	// Expected 是应追缴额，FromGift / FromBalance 是两桶实扣额。
	Expected    float64 `json:"expected"`
	FromGift    float64 `json:"fromGift"`
	FromBalance float64 `json:"fromBalance"`
	// Shortfall 是「赠金扣不动、被迫从本金扣」的差额（= FromBalance）。
	Shortfall    float64 `json:"shortfall"`
	BalanceAfter float64 `json:"balanceAfter"`
	GiftAfter    float64 `json:"giftAfter"`
	Debt         float64 `json:"debt"`
	Skipped      bool    `json:"skipped"`
	Error        string  `json:"error,omitempty"`
}

// BatchGrantRollbackResult 是一次批量回滚的汇总。
type BatchGrantRollbackResult struct {
	BatchID string `json:"batchId"`
	// Total / Succeeded / Failed 是人数口径。
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	// ExpectedTotal / GiftTotal / PrincipalTotal 是金额口径。
	ExpectedTotal  float64 `json:"expectedTotal"`
	GiftTotal      float64 `json:"giftTotal"`
	PrincipalTotal float64 `json:"principalTotal"`
	// DebtTotal 是本次回滚**新产生**的欠款合计（余额变负的用户）。
	DebtTotal float64 `json:"debtTotal"`
	// DebtUsers 是产生欠款的人数（118-*.md §6.1 报告里的「200 人服务已停用」）。
	DebtUsers int                      `json:"debtUsers"`
	Items     []BatchGrantRollbackItem `json:"items"`
}

// batchGrantState 是内存里的批次进度。
//
// ⚠️ 它只承载**进度与暂停标志**，不是账本：进程重启后丢的只是进度显示，
// 「谁已经送过」始终以 billing_records 为准（见文件头注释）。
type batchGrantState struct {
	progress BatchGrantProgress
	paused   bool
	aborted  bool
}

var (
	batchGrantStatesMu sync.Mutex
	batchGrantStates   = map[string]*batchGrantState{}
)

// resetBatchGrantStatesForTest 清空内存批次表（测试用，避免跨用例串台）。
func resetBatchGrantStatesForTest() {
	batchGrantStatesMu.Lock()
	defer batchGrantStatesMu.Unlock()
	batchGrantStates = map[string]*batchGrantState{}
}

// batchGrantStateOf 取或建批次状态。
func batchGrantStateOf(batchID string) *batchGrantState {
	batchGrantStatesMu.Lock()
	defer batchGrantStatesMu.Unlock()
	state, ok := batchGrantStates[batchID]
	if !ok {
		state = &batchGrantState{progress: BatchGrantProgress{BatchID: batchID, Status: batchGrantStatusRunning}}
		batchGrantStates[batchID] = state
	}
	return state
}

// updateBatchGrantProgress 在锁内改进度并回传快照。
func updateBatchGrantProgress(batchID string, apply func(*BatchGrantProgress)) BatchGrantProgress {
	batchGrantStatesMu.Lock()
	defer batchGrantStatesMu.Unlock()
	state, ok := batchGrantStates[batchID]
	if !ok {
		state = &batchGrantState{progress: BatchGrantProgress{BatchID: batchID, Status: batchGrantStatusRunning}}
		batchGrantStates[batchID] = state
	}
	apply(&state.progress)
	snapshot := state.progress
	snapshot.FailedDetails = append([]BatchGrantFailure(nil), state.progress.FailedDetails...)
	return snapshot
}

// BatchGrantProgressWithDB 返回批次进度。
//
// 内存里没有（进程重启）时，用 billing_records 反推一个**只读的最小进度**：
// 已赠送人数与金额是确定事实，失败明细则无从得知（不猜）。
func BatchGrantProgressWithDB(db *gorm.DB, batchID string) (BatchGrantProgress, error) {
	trimmed := strings.TrimSpace(batchID)
	if trimmed == "" {
		return BatchGrantProgress{}, fmt.Errorf("%w：批次号不能为空", ErrBatchGrantInvalid)
	}
	batchGrantStatesMu.Lock()
	state, ok := batchGrantStates[trimmed]
	var snapshot BatchGrantProgress
	if ok {
		snapshot = state.progress
		snapshot.FailedDetails = append([]BatchGrantFailure(nil), state.progress.FailedDetails...)
	}
	batchGrantStatesMu.Unlock()
	if ok {
		return snapshot, nil
	}
	granted, amount, err := countBatchGrantRecordsWithDB(db, trimmed)
	if err != nil {
		return BatchGrantProgress{}, err
	}
	if granted == 0 {
		return BatchGrantProgress{}, ErrBatchGrantNotFound
	}
	return BatchGrantProgress{
		BatchID: trimmed, Status: batchGrantStatusCompleted,
		Total: granted, Processed: granted, Succeeded: granted, Amount: amount,
	}, nil
}

// BatchGrantPause 暂停批次：只停止**后续**处理，已赠送的不回滚（⑧）。
//
// 对**已完成**的批次调用是合法的无害操作：暂停标志会置位（后续任何重跑都会被它挡住），
// 但状态**不降级**成 paused —— 把一个已经送完的批次标成「已暂停」会让界面上的
// 「已处理 N/N」自相矛盾，管理员会以为还有活没干。
func BatchGrantPause(batchID string) error {
	trimmed := strings.TrimSpace(batchID)
	if trimmed == "" {
		return fmt.Errorf("%w：批次号不能为空", ErrBatchGrantInvalid)
	}
	batchGrantStatesMu.Lock()
	defer batchGrantStatesMu.Unlock()
	state, ok := batchGrantStates[trimmed]
	if !ok {
		return ErrBatchGrantNotFound
	}
	state.paused = true
	if state.progress.Status != batchGrantStatusCompleted {
		state.progress.Status = batchGrantStatusPaused
	}
	return nil
}

// BatchGrantAbort 中止批次：之后不再处理任何人，已赠送的不回滚（⑧）。
//
// 与 Pause 同款：标志永远置位，状态只在未完成时改成 aborted。
func BatchGrantAbort(batchID string) error {
	trimmed := strings.TrimSpace(batchID)
	if trimmed == "" {
		return fmt.Errorf("%w：批次号不能为空", ErrBatchGrantInvalid)
	}
	batchGrantStatesMu.Lock()
	defer batchGrantStatesMu.Unlock()
	state, ok := batchGrantStates[trimmed]
	if !ok {
		return ErrBatchGrantNotFound
	}
	state.aborted = true
	if state.progress.Status != batchGrantStatusCompleted {
		state.progress.Status = batchGrantStatusAborted
	}
	return nil
}

// batchGrantShouldStop 返回「是否应该停止继续处理」。
//
// 只读暂停/中止标志 —— **回滚循环绝不调用它**，这是「回滚不可中断」的实现方式。
func batchGrantShouldStop(batchID string) bool {
	batchGrantStatesMu.Lock()
	defer batchGrantStatesMu.Unlock()
	state, ok := batchGrantStates[batchID]
	if !ok {
		return false
	}
	return state.paused || state.aborted
}

// throttleBatchGrant 按 batchGrantInterval 限速（默认 10/s）。
func throttleBatchGrant() {
	if batchGrantInterval > 0 {
		time.Sleep(batchGrantInterval)
	}
}

// BuildBatchGrantID 生成批次号（短横线形态，作为 anchor 后缀，不含下划线）。
func BuildBatchGrantID() string {
	return strings.TrimPrefix(BuildAdminGrantAnchor(""), adminGrantAnchorKindSingle)
}

// BatchGrantConfirmReasons 是 batchGrantConfirmReasons 的导出包装，供 controller 在
// 预览阶段（此时还没有批次状态）算出「执行时会要求确认的几条原因」。
func BatchGrantConfirmReasons(db *gorm.DB, unitAmount float64, total int, allUsers bool) []string {
	return batchGrantConfirmReasons(db, unitAmount, total, allUsers)
}

// batchGrantConfirmReasons 计算三层确认的触发原因（N3-g）。
//
// L1 单人金额超阈值 / L2 人数 > 100 / L3 全选（All=true 且人数与库内总数一致）。
func batchGrantConfirmReasons(db *gorm.DB, unitAmount float64, total int, allUsers bool) []string {
	reasons := make([]string, 0, 3)
	if AdminGrantConfirmRequired(db, unitAmount) {
		reasons = append(reasons, fmt.Sprintf("单人金额 ¥%s 超过阈值 ¥%s",
			balanceMoneyText(unitAmount), balanceMoneyText(AdminGrantConfirmThresholdWithDB(db))))
	}
	totalAmount := util.RoundMoney(unitAmount * float64(total))
	if totalAmount >= batchGrantMaxTotalAmount {
		reasons = append(reasons, fmt.Sprintf("批次总额 ¥%s 达到阈值 ¥%s",
			balanceMoneyText(totalAmount), balanceMoneyText(batchGrantMaxTotalAmount)))
	}
	if total > batchGrantAsyncThreshold {
		reasons = append(reasons, fmt.Sprintf("人数 %d 超过 %d，将转为异步执行", total, batchGrantAsyncThreshold))
	}
	if allUsers {
		reasons = append(reasons, "已选择「全部用户」")
	}
	return reasons
}

// BatchGrantPreview 计算「这批人会不会收到钱、合计多少、要不要确认」。
//
// 预览是**只读**的：不写流水、不改余额、不创建批次状态。名单模式会逐人查库判断
// 排除原因，因此排除统计是真实值而不是估算。
func BatchGrantPreview(db *gorm.DB, sel BatchGrantSelector) (BatchGrantPreviewResult, error) {
	var result BatchGrantPreviewResult
	if db == nil {
		return result, fmt.Errorf("%w：数据库不可用", ErrBatchGrantInvalid)
	}
	result.BatchID = BuildBatchGrantID()
	users, excluded, err := resolveBatchGrantUsers(db, sel)
	if err != nil {
		return result, err
	}
	result.ExcludedBanned = excluded.banned
	result.ExcludedArchived = excluded.archived
	result.ExcludedMissing = excluded.missing
	result.UnitAmount = 0
	result.Total = len(users)
	if len(users) > batchGrantMaxUsers {
		return result, fmt.Errorf("%w：单批次最多 %d 人，本次 %d 人", ErrBatchGrantInvalid, batchGrantMaxUsers, len(users))
	}
	result.Eligible = make([]BatchGrantTarget, 0, len(users))
	for _, user := range users {
		result.Eligible = append(result.Eligible, BatchGrantTarget{
			UserID: user.ID, Username: user.Username,
			Balance: util.RoundMoney(user.Balance), Gift: util.RoundMoney(user.GiftBalance),
			Available: WalletAvailableTotal(user.Balance, user.GiftBalance),
		})
	}
	result.Async = len(users) > batchGrantAsyncThreshold
	return result, nil
}

// batchGrantExclusions 是预览的排除统计。
type batchGrantExclusions struct {
	banned   int
	archived int
	missing  int
}

// resolveBatchGrantUsers 解析出最终可赠送的用户（已排除封禁 / 归档 / 不存在）。
//
// 名单模式按 UserID 逐个判定；条件模式把排除条件直接写进 SQL（banned 与
// deleted_at IS NULL 都是确定的列条件），两种模式的最终判定在这里统一复核一次。
func resolveBatchGrantUsers(db *gorm.DB, sel BatchGrantSelector) ([]entity.User, batchGrantExclusions, error) {
	var excluded batchGrantExclusions
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{})

	ids := normalizeBatchGrantUserIDs(sel.UserIDs)
	if len(ids) == 0 && !sel.All {
		return nil, excluded, fmt.Errorf("%w：请提供用户名单或选择按条件筛选", ErrBatchGrantInvalid)
	}
	if len(ids) > batchGrantMaxUsers {
		return nil, excluded, fmt.Errorf("%w：单批次最多 %d 人，本次 %d 人", ErrBatchGrantInvalid, batchGrantMaxUsers, len(ids))
	}
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	}
	// 归档用户在 GORM 下已被软删除条件挡住；这里显式再写一次，
	// 让「排除已归档」在不依赖模型软删除语义时也成立。
	query = query.Where("deleted_at IS NULL")
	statuses := normalizeBatchGrantStatuses(sel.Statuses)
	if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	}
	if sel.LevelID > 0 {
		query = query.Where("id IN (?)", db.Session(&gorm.Session{NewDB: true}).
			Model(&entity.UserLevelHistory{}).
			Select("user_id").Where("to_level_id = ?", sel.LevelID))
	}
	if sel.RegisteredFrom != nil {
		query = query.Where("created_at >= ?", *sel.RegisteredFrom)
	}
	if sel.RegisteredTo != nil {
		query = query.Where("created_at < ?", *sel.RegisteredTo)
	}
	if sel.LastLoginFrom != nil {
		query = query.Where("last_login_at >= ?", *sel.LastLoginFrom)
	}
	if sel.LastLoginTo != nil {
		query = query.Where("last_login_at < ?", *sel.LastLoginTo)
	}
	if sel.MinBalance != nil || sel.MaxBalance != nil {
		// 双桶合计口径：只看 balance 会把「本金 0、赠金 1」错分到 0 档（43-*.md §2）。
		if sel.MinBalance != nil {
			query = query.Where("(balance + gift_balance) >= ?", *sel.MinBalance)
		}
		if sel.MaxBalance != nil {
			query = query.Where("(balance + gift_balance) <= ?", *sel.MaxBalance)
		}
	}

	var users []entity.User
	if err := query.Order("id asc").Find(&users).Error; err != nil {
		return nil, excluded, err
	}

	// 名单模式下「查不到的人」要单独计数（112 §3.6 的 failedDetails 场景）。
	if len(ids) > 0 {
		found := make(map[int32]struct{}, len(users))
		for _, user := range users {
			found[user.ID] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := found[id]; ok {
				continue
			}
			// 再查一次以区分「不存在 / 已归档 / 已封禁」。
			var probe entity.User
			err := db.Session(&gorm.Session{NewDB: true}).Unscoped().
				Model(&entity.User{}).Where("id = ?", id).Take(&probe).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				excluded.missing++
			case err != nil:
				return nil, excluded, err
			case probe.DeletedAt.Valid:
				excluded.archived++
			default:
				excluded.banned++
			}
		}
	}

	eligible := make([]entity.User, 0, len(users))
	for _, user := range users {
		if user.DeletedAt.Valid {
			excluded.archived++
			continue
		}
		if strings.EqualFold(strings.TrimSpace(user.Status), adminGrantUserStatusBanned) {
			excluded.banned++
			continue
		}
		eligible = append(eligible, user)
	}
	return eligible, excluded, nil
}

// normalizeBatchGrantUserIDs 去重并去掉非正数 ID。
func normalizeBatchGrantUserIDs(ids []int32) []int32 {
	seen := make(map[int32]struct{}, len(ids))
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// normalizeBatchGrantStatuses 归一化状态白名单，**永远剔除 banned**（N3-f）。
func normalizeBatchGrantStatuses(statuses []string) []string {
	allowed := make([]string, 0, len(statuses))
	for _, status := range statuses {
		trimmed := strings.ToLower(strings.TrimSpace(status))
		if trimmed == "" || trimmed == adminGrantUserStatusBanned || trimmed == "archived" {
			continue
		}
		allowed = append(allowed, trimmed)
	}
	return allowed
}

// BatchGrantExecute 执行批量赠送。
//
// 同步 / 异步由人数决定（>100 → 异步，立即返回 running 态进度）。
// 每个用户一个独立事务（复用 AdminGrant）：一个人失败不影响其他人。
func BatchGrantExecute(db *gorm.DB, req BatchGrantExecuteRequest) (BatchGrantProgress, error) {
	var result BatchGrantProgress
	if db == nil {
		return result, fmt.Errorf("%w：数据库不可用", ErrBatchGrantInvalid)
	}
	if !isAdminUser(req.Operator) {
		return result, ErrBalanceAdjustmentForbidden
	}
	unitAmount, err := normalizeAdminGrantAmount(req.Amount)
	if err != nil {
		return result, err
	}
	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" {
		batchID = BuildBatchGrantID()
	}
	// 批次号会进 SQL 的 LIKE 模式（loadBatchGrantRecordsWithDB 用 `%_batch-<id>` 反查），
	// 所以必须走与 anchor 同一套白名单校验 —— 只挡 `_` 而放过 `%` 会让批次边界失真。
	if _, err := normalizeAdminGrantAnchor(adminGrantAnchorKindBatch + batchID); err != nil {
		return result, fmt.Errorf("%w：批次号只能包含字母、数字与短横线", ErrBatchGrantInvalid)
	}

	users, _, err := resolveBatchGrantUsers(db, req.Selector)
	if err != nil {
		return result, err
	}
	if len(users) == 0 {
		return result, fmt.Errorf("%w：没有符合条件的用户", ErrBatchGrantInvalid)
	}

	// 二次确认：单笔 / 总额 / 人数 / 全选任一触发（N3-g）。
	if !req.Confirmed {
		if reasons := batchGrantConfirmReasons(db, unitAmount, len(users), req.Selector.All); len(reasons) > 0 {
			return result, fmt.Errorf("%w：%s", ErrBatchGrantConfirmRequired, strings.Join(reasons, "；"))
		}
	}

	targets := make([]int32, 0, len(users))
	for _, user := range users {
		targets = append(targets, user.ID)
	}
	if req.OnlyFailed {
		filtered, err := filterBatchGrantFailedUsersWithDB(db, batchID, targets)
		if err != nil {
			return result, err
		}
		targets = filtered
	}

	now := time.Now()
	progress := updateBatchGrantProgress(batchID, func(p *BatchGrantProgress) {
		p.Status = batchGrantStatusRunning
		p.Total = len(targets)
		p.Processed, p.Succeeded, p.Failed, p.Amount = 0, 0, 0, 0
		p.FailedDetails = nil
		p.StartedAt = now
		p.FinishedAt = nil
	})

	async := len(targets) > batchGrantAsyncThreshold
	if async {
		go runBatchGrant(db, batchID, targets, unitAmount, req)
		return progress, nil
	}
	return runBatchGrant(db, batchID, targets, unitAmount, req), nil
}

// runBatchGrant 是批量赠送的唯一执行体（同步与异步共用）。
func runBatchGrant(db *gorm.DB, batchID string, targets []int32, unitAmount float64, req BatchGrantExecuteRequest) BatchGrantProgress {
	for _, userID := range targets {
		if batchGrantShouldStop(batchID) {
			break
		}
		throttleBatchGrant()
		granted, err := AdminGrant(db, AdminGrantRequest{
			Operator: req.Operator, UserID: userID, Amount: unitAmount,
			Reason: req.Reason, Anchor: adminGrantAnchorKindBatch + batchID,
			Confirmed: true, ClientIP: req.ClientIP,
		})
		progress := updateBatchGrantProgress(batchID, func(p *BatchGrantProgress) {
			p.Processed++
			switch {
			case err != nil:
				p.Failed++
				p.FailedDetails = append(p.FailedDetails, BatchGrantFailure{UserID: userID, Reason: err.Error()})
			default:
				p.Succeeded++
				if !granted.AlreadyGranted {
					p.Amount = util.RoundMoney(p.Amount + granted.Amount)
				}
			}
		})
		_ = progress
	}
	return updateBatchGrantProgress(batchID, func(p *BatchGrantProgress) {
		if p.Status == batchGrantStatusPaused || p.Status == batchGrantStatusAborted {
			return
		}
		if p.Failed > 0 && p.Succeeded == 0 {
			p.Status = batchGrantStatusFailed
		} else {
			p.Status = batchGrantStatusCompleted
		}
		finished := time.Now()
		p.FinishedAt = &finished
	})
}

// filterBatchGrantFailedUsersWithDB 只保留「该批次还没送成功」的人（N3-i 只重试失败项）。
func filterBatchGrantFailedUsersWithDB(db *gorm.DB, batchID string, targets []int32) ([]int32, error) {
	done, err := batchGrantGrantedUserIDsWithDB(db, batchID)
	if err != nil {
		return nil, err
	}
	filtered := make([]int32, 0, len(targets))
	for _, userID := range targets {
		if _, ok := done[userID]; ok {
			continue
		}
		filtered = append(filtered, userID)
	}
	return filtered, nil
}

// batchGrantGrantedUserIDsWithDB 返回该批次已赠送成功的用户集合。
func batchGrantGrantedUserIDsWithDB(db *gorm.DB, batchID string) (map[int32]struct{}, error) {
	records, err := loadBatchGrantRecordsWithDB(db, batchID)
	if err != nil {
		return nil, err
	}
	done := make(map[int32]struct{}, len(records))
	for _, record := range records {
		done[record.UserID] = struct{}{}
	}
	return done, nil
}

// loadBatchGrantRecordsWithDB 取某批次的全部赠送流水。
//
// 用 `suffix` 匹配而不是 `LIKE prefix%`：request_id 的形态是
// `admin_grant_<uid>_<anchor>`，anchor 在**尾部**（prefix 里的 uid 是变量）。
//
// ⚠️ 两层防护，缺一层批次边界都会失真：
//
//  1. **白名单校验 batchID**（normalizeAdminGrantAnchor）：`_` `%` `\` 都不允许出现。
//     放在这里而不是只放在 BatchGrantExecute —— 本函数有 3 个调用方
//     （Execute / Progress / Rollback），只在 Execute 校验会让另外两个入口
//     把带 `%` 的批次号直接拼进 LIKE。
//  2. **转义 `_` 并声明 ESCAPE**：`_` 在 LIKE 里是单字符通配符，不转义时
//     `%_batch-7f3a` 还会匹配 `admin_grant_21Xbatch-7f3a`。
func loadBatchGrantRecordsWithDB(db *gorm.DB, batchID string) ([]entity.BillingRecord, error) {
	trimmedBatchID := strings.TrimSpace(batchID)
	if _, err := normalizeAdminGrantAnchor(adminGrantAnchorKindBatch + trimmedBatchID); err != nil {
		return nil, fmt.Errorf("%w：批次号只能包含字母、数字与短横线", ErrBatchGrantInvalid)
	}
	suffix := strings.ReplaceAll(
		fmt.Sprintf("_%s%s", adminGrantAnchorKindBatch, trimmedBatchID), "_", `\_`)
	var records []entity.BillingRecord
	err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Where(`type = ? AND request_id LIKE ? ESCAPE '\'`, AdminGrantBillingRecordType, "%"+suffix).
		Order("id asc").Find(&records).Error
	return records, err
}

// countBatchGrantRecordsWithDB 返回批次的赠送人数与金额合计。
func countBatchGrantRecordsWithDB(db *gorm.DB, batchID string) (int, float64, error) {
	records, err := loadBatchGrantRecordsWithDB(db, batchID)
	if err != nil {
		return 0, 0, err
	}
	total := 0.0
	for _, record := range records {
		total = util.RoundMoney(total + util.RoundMoney(record.Amount))
	}
	return len(records), total, nil
}

// BatchGrantRollback 执行追缴式回滚。
//
// **不可中断**（⑧）：本函数**不读**任何暂停/中止标志，一旦开始就把名单跑完。
// 原因是中断的回滚会让账本停在「一半人追缴、一半人没追」的中间态，而重放又依赖
// 每人各自的 request_id —— 管理员无法从报告里看出到底停在哪。
//
// 逐用户复用 AdminGrantReverse：两笔流水、两个幂等键、金额为 0 的那笔不写。
func BatchGrantRollback(db *gorm.DB, req BatchGrantRollbackRequest) (BatchGrantRollbackResult, error) {
	var result BatchGrantRollbackResult
	if db == nil {
		return result, fmt.Errorf("%w：数据库不可用", ErrBatchGrantInvalid)
	}
	if !isAdminUser(req.Operator) {
		return result, ErrBalanceAdjustmentForbidden
	}
	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" {
		return result, fmt.Errorf("%w：批次号不能为空", ErrBatchGrantInvalid)
	}
	records, err := loadBatchGrantRecordsWithDB(db, batchID)
	if err != nil {
		return result, err
	}
	if len(records) == 0 {
		return result, ErrBatchGrantNotFound
	}
	result.BatchID = batchID
	updateBatchGrantProgress(batchID, func(p *BatchGrantProgress) { p.Status = batchGrantStatusRollingBack })

	// 同一用户可能在同一批次里有多条赠送行（历史重试），按用户聚合应追缴额。
	amounts := make(map[int32]float64, len(records))
	order := make([]int32, 0, len(records))
	for _, record := range records {
		if _, ok := amounts[record.UserID]; !ok {
			order = append(order, record.UserID)
		}
		amounts[record.UserID] = util.RoundMoney(amounts[record.UserID] + util.RoundMoney(record.Amount))
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	result.Items = make([]BatchGrantRollbackItem, 0, len(order))
	for _, userID := range order {
		// ⚠️ 这里**没有** batchGrantShouldStop 检查 —— 回滚不可中断。
		throttleBatchGrant()
		reversed, err := AdminGrantReverse(db, AdminGrantReverseRequest{
			Operator: req.Operator, UserID: userID, Amount: amounts[userID],
			Anchor: adminGrantAnchorKindBatch + batchID, Reason: req.Reason, ClientIP: req.ClientIP,
		})
		item := BatchGrantRollbackItem{UserID: userID, Expected: amounts[userID]}
		result.Total++
		if err != nil {
			item.Error = err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		result.Succeeded++
		item.FromGift = reversed.FromGift
		item.FromBalance = reversed.FromBalance
		item.Shortfall = reversed.Shortfall
		item.BalanceAfter = reversed.BalanceAfter
		item.GiftAfter = reversed.GiftAfter
		item.Skipped = reversed.Skipped
		if reversed.BalanceAfter < 0 {
			item.Debt = util.RoundMoney(-reversed.BalanceAfter)
			result.DebtUsers++
		}
		result.ExpectedTotal = util.RoundMoney(result.ExpectedTotal + amounts[userID])
		result.GiftTotal = util.RoundMoney(result.GiftTotal + reversed.FromGift)
		result.PrincipalTotal = util.RoundMoney(result.PrincipalTotal + reversed.FromBalance)
		result.DebtTotal = util.RoundMoney(result.DebtTotal + item.Debt)
		result.Items = append(result.Items, item)
	}

	updateBatchGrantProgress(batchID, func(p *BatchGrantProgress) {
		p.Status = batchGrantStatusRolledBack
		finished := time.Now()
		p.FinishedAt = &finished
	})
	return result, nil
}

// MarshalBatchGrantProgress 把进度序列化成审计/接口可回放的 JSON。
func MarshalBatchGrantProgress(progress BatchGrantProgress) string {
	raw, err := json.Marshal(progress)
	if err != nil {
		return ""
	}
	return string(raw)
}

// batchGrantAvailableTotal 是筛选口径的辅助函数，暴露给测试钉死「合计而非单桶」。
func batchGrantAvailableTotal(balance, gift float64) float64 {
	if math.IsNaN(balance) || math.IsInf(balance, 0) {
		balance = 0
	}
	if math.IsNaN(gift) || math.IsInf(gift, 0) {
		gift = 0
	}
	return WalletAvailableTotal(balance, gift)
}
