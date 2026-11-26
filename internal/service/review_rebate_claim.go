package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReviewRebateClaimCreateInput 是提交评价返现的入参。
//
// ScreenshotPath / ScreenshotSHA256 由 controller 侧的上传流程算好传进来：
// 服务层不碰文件系统，只落账。这样单测不需要真的写文件。
type ReviewRebateClaimCreateInput struct {
	ProgramID        int32
	UserID           int32
	ScreenshotPath   string
	ScreenshotSHA256 string
	ClaimedStars     int
	ClaimedReviewURL string
	Source           string
}

// ReviewRebateProgramView returns active stores and the member's progress.
type ReviewRebateProgramView struct {
	Program            ReviewRebateProgramSummary `json:"program"`
	Usage              ReviewRebateUsage          `json:"usage"`
	AuthorshipVerified bool                       `json:"authorshipVerified"`
	CanClaim           bool                       `json:"canClaim"`
	Stores             []ReviewRebateStoreView    `json:"stores"`
}

type ReviewRebateStoreView struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	StoreURL string `json:"storeUrl"`
	CanClaim bool   `json:"canClaim"`
}

// ReviewRebateProgramSummary 是活动的用户可见字段。
type ReviewRebateProgramSummary struct {
	ID     int32  `json:"id"`
	Source string `json:"source"`
	// Enabled 是活动开关：用户端靠它决定「显示规则表与提交表单」还是「功能未开启」。
	//
	// 必须下发：总开关打开但活动行被禁用时，用户端若拿不到这个字段就会一直显示
	// （或一直不显示）错误的状态，而入口本身是拒绝的 —— 界面与后端结论会打架。
	Enabled          bool   `json:"enabled"`
	Name             string `json:"name"`
	StoreURL         string `json:"storeUrl"`
	MaxClaimsPerUser int    `json:"maxClaimsPerUser"`
	// TutorialVersionID 是关联图文教程版本（0 = 无）。
	TutorialVersionID int32 `json:"tutorialVersionId"`
}

// ReviewRebateUsage 是本人额度使用情况。
type ReviewRebateUsage struct {
	// ClaimedCount 是**已批准**的条数（口径：只有批准才消耗次数）。
	ClaimedCount int64 `json:"claimedCount"`
	// ApprovedCount 与 ClaimedCount 同值。
	//
	// 为什么要两个名字：这个字段是「已通过条数」，管理端与用户端都按
	// `approvedCount/approvedAmount` 这对名字读；而 ClaimedCount 是额度口径的
	// 历史名字（"claimed" = 已领取）。两处各读一个名字会让同一份数据在
	// 界面上显示成两套口径，所以**两个键都下发、值恒等**，谁读都对。
	ApprovedCount int64 `json:"approvedCount"`
	// ApprovedAmount 是已发放金额合计（口径：以 review_rebate_awards 为准）。
	ApprovedAmount float64 `json:"-"`
	// PendingCount 是审核中的条数（不占额度，但展示出来避免用户以为没提交上）。
	PendingCount int64 `json:"pendingCount"`
	// RejectedCount 是被拒绝的条数（展示用，不占额度）。
	RejectedCount int64 `json:"rejectedCount"`
	// RemainingClaims/RemainingAmount 是剩余额度（负数截断为 0）。
	RemainingClaims int     `json:"remainingClaims"`
	RemainingAmount float64 `json:"-"`
}

// GetReviewRebateProgramViewWithDB 组装用户端的活动视图。
//
// 开关关闭 / 活动不存在 / 表未就绪 → 返回业务错误（controller 映射 4xx）。
func GetReviewRebateProgramViewWithDB(db *gorm.DB, userID int32) (ReviewRebateProgramView, error) {
	return GetReviewRebateStoreProgramViewWithDB(db, userID, entity.ReviewRebateSourceEdge)
}

func GetReviewRebateStoreProgramViewWithDB(db *gorm.DB, userID int32, source string) (ReviewRebateProgramView, error) {
	wantsDefault := strings.TrimSpace(source) == ""
	source, err := NormalizeReviewRebateSource(source)
	if err != nil {
		return ReviewRebateProgramView{}, err
	}
	programs, err := ListActiveReviewRebateStoreProgramsWithDB(db)
	if err != nil {
		return ReviewRebateProgramView{}, err
	}
	var program entity.ReviewRebateProgram
	stores := make([]ReviewRebateStoreView, 0, len(programs))
	var usage ReviewRebateUsage
	canClaim := false
	for _, candidate := range programs {
		currentUsage, err := GetReviewRebateUsageWithDB(db, candidate.ID, userID)
		if err != nil {
			return ReviewRebateProgramView{}, err
		}
		available := currentUsage.RemainingClaims > 0 && currentUsage.RemainingAmount > 0
		stores = append(stores, ReviewRebateStoreView{Source: candidate.Source, Name: candidate.Name, StoreURL: candidate.StoreURL, CanClaim: available})
		if candidate.Source == source || (wantsDefault && program.ID == 0) {
			program, usage, canClaim = candidate, currentUsage, available
		}
	}
	if program.ID == 0 {
		return ReviewRebateProgramView{}, ErrReviewRebateDisabled
	}
	view := ReviewRebateProgramView{
		Program: ReviewRebateProgramSummary{
			ID:                program.ID,
			Source:            program.Source,
			Enabled:           program.Enabled,
			Name:              program.Name,
			StoreURL:          program.StoreURL,
			MaxClaimsPerUser:  min(1, program.MaxClaimsPerUser),
			TutorialVersionID: program.TutorialVersionID,
		},
		Usage:              usage,
		AuthorshipVerified: false,
		CanClaim:           canClaim,
		Stores:             stores,
	}
	return view, nil
}

// GetReviewRebateUsageWithDB aggregates awards and claim progress across all
// programs for the selected store. Award rows survive a clawback.
func GetReviewRebateUsageWithDB(db *gorm.DB, programID int32, userID int32) (ReviewRebateUsage, error) {
	usage := ReviewRebateUsage{}
	if programID <= 0 || userID <= 0 {
		return usage, nil
	}
	if !ReviewRebateSchemaAvailable(db) {
		return usage, ErrReviewRebateSchemaUnavailable
	}
	program, err := getReviewRebateProgramByID(db, programID)
	if err != nil {
		return usage, err
	}
	type statusAggregate struct {
		Status string `gorm:"column:status"`
		Rows   int64  `gorm:"column:row_count"`
	}
	var rows []statusAggregate
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateClaim{}).
		Select("status, COUNT(*) AS row_count").
		Where("user_id = ? AND source = ?", userID, program.Source).
		Group("status").Scan(&rows).Error; err != nil {
		return usage, err
	}
	for _, row := range rows {
		switch row.Status {
		case entity.ReviewRebateStatusSubmitted, entity.ReviewRebateStatusScraping,
			entity.ReviewRebateStatusScored, entity.ReviewRebateStatusPendingReview, entity.ReviewRebateStatusBelowThreshold:
			usage.PendingCount += row.Rows
		case entity.ReviewRebateStatusRejected:
			usage.RejectedCount += row.Rows
		}
	}
	var awards struct {
		Count int64
		Total float64
	}
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateAward{}).
		Select("COUNT(*) AS count, COALESCE(SUM(amount), 0) AS total").Where("user_id = ? AND source = ?", userID, program.Source).
		Scan(&awards).Error; err != nil {
		return usage, err
	}
	usage.ClaimedCount = awards.Count
	usage.ApprovedCount = awards.Count
	usage.ApprovedAmount = util.RoundMoney(awards.Total)

	usage.RemainingClaims = min(1, program.MaxClaimsPerUser) - int(usage.ClaimedCount)
	if usage.RemainingClaims < 0 {
		usage.RemainingClaims = 0
	}
	usage.RemainingAmount = util.RoundMoney(program.MaxAmountPerUser - usage.ApprovedAmount)
	if usage.RemainingAmount < 0 {
		usage.RemainingAmount = 0
	}
	return usage, nil
}

// getReviewRebateProgramByID 按 ID 取活动（**不看开关**，供额度统计复用）。
func getReviewRebateProgramByID(db *gorm.DB, id int32) (entity.ReviewRebateProgram, error) {
	var program entity.ReviewRebateProgram
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateProgram{}).Where("id = ?", id).Take(&program).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.ReviewRebateProgram{}, ErrReviewRebateProgramNotFound
		}
		return entity.ReviewRebateProgram{}, err
	}
	return program, nil
}

func lockReviewRebateUser(tx *gorm.DB, userID int32) (entity.User, error) {
	var user entity.User
	err := reviewRebateSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return user, ErrReviewRebateInvalidInput
	}
	return user, err
}

func checkReviewRebateStoreAward(tx *gorm.DB, userID int32, program entity.ReviewRebateProgram, proposedAmount float64) error {
	if program.MaxClaimsPerUser <= 0 || program.MaxAmountPerUser <= 0 || proposedAmount > util.RoundMoney(program.MaxAmountPerUser) {
		return ErrReviewRebateLimitReached
	}
	// Retained award rows include historical grants and grants that were clawed back.
	var award entity.ReviewRebateAward
	err := reviewRebateSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("user_id = ? AND source = ?", userID, program.Source).Take(&award).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil
	case err != nil:
		return err
	default:
		return ErrReviewRebateLimitReached
	}
}

// CreateReviewRebateClaimWithDB 落一条提交单。
//
// 四道闸，顺序有讲究（先便宜后昂贵、先业务后账务）：
//  1. **开关**：总开关关闭 → ErrReviewRebateDisabled；活动被禁用 → 同样拒绝。
//     这就是「开关关闭 → 提交接口拒绝，规则表不生效」的落点。
//  2. **入参**：星数、截图摘要、活动存在性。
//  3. **同账号重复**：唯一键 (program_id, user_id, canonical_claim_id)。
//     命中已存在行 → ErrReviewRebateDuplicate（controller 映射 409）。
//  4. **次数/金额上限**：在事务内用 SELECT ... FOR UPDATE 锁住本人已批准行的统计，
//     避免「只剩 1 次时两单并发都成功」（16-*.md §1.4）。
//
// 关于 duplicate / limit_reached 两种状态为什么不落行：
//
//	duplicate 根本落不进去（唯一索引就是用来拒绝它的）；
//	limit_reached 落行会让表被「反复尝试」灌满，而且这些行永远没有金额，
//	没有任何对账价值。这两种情况用哨兵错误把「业务结论」返回给调用方，
//	由 controller 翻译成 HTTP 状态码与用户可读文案 —— 语义等价于规格里的状态值，
//	但不需要为「没发生的事」写一行账。
func CreateReviewRebateClaimWithDB(db *gorm.DB, input ReviewRebateClaimCreateInput) (entity.ReviewRebateClaim, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
	}
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateDisabled
	}
	if input.UserID <= 0 {
		return entity.ReviewRebateClaim{}, fmt.Errorf("%w: 用户无效", ErrReviewRebateInvalidInput)
	}
	stars, err := normalizeReviewRebateClaimStars(input.ClaimedStars)
	if err != nil {
		return entity.ReviewRebateClaim{}, err
	}
	digest := strings.ToLower(strings.TrimSpace(input.ScreenshotSHA256))
	if len(digest) != 64 {
		return entity.ReviewRebateClaim{}, fmt.Errorf("%w: 截图摘要无效", ErrReviewRebateInvalidInput)
	}

	var created entity.ReviewRebateClaim
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := lockReviewRebateUser(tx, input.UserID); err != nil {
			return err
		}
		program, err := getReviewRebateProgramByID(tx, input.ProgramID)
		if err != nil {
			return err
		}
		if !program.Enabled {
			return ErrReviewRebateDisabled
		}
		if input.Source != "" && input.Source != program.Source {
			return ErrReviewRebateDisabled
		}
		storeIDs, err := reviewRebateStoreIDs(tx)
		if err != nil {
			return err
		}
		if storeIDs[program.Source] == "" {
			return ErrReviewRebateDisabled
		}
		storeProgram := reviewRebateStoreProgram(program, storeIDs)
		canonical := BuildReviewRebateCanonicalClaimID(stars, digest, input.ClaimedReviewURL)

		var previous entity.ReviewRebateClaim
		err = reviewRebateSession(tx).Select("id", "status").
			Where("program_id = ? AND user_id = ?", input.ProgramID, input.UserID).
			Where("(canonical_claim_id = ? OR canonical_claim_id LIKE ?)", canonical, canonical+":%").
			Order("id DESC").Take(&previous).Error
		if err == nil {
			if previous.Status != entity.ReviewRebateStatusRejected {
				return ErrReviewRebateDuplicate
			}
			// A resubmission keeps its own row, linked by the rejected claim ID in the deduplication key.
			canonical = fmt.Sprintf("%s:%d", canonical, previous.ID)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if err := checkReviewRebateStoreAward(tx, input.UserID, program, 0); err != nil {
			return err
		}

		claim := entity.ReviewRebateClaim{
			ProgramID:           input.ProgramID,
			UserID:              input.UserID,
			Source:              program.Source,
			StoreURLSnapshot:    storeProgram.StoreURL,
			ExtensionIDSnapshot: storeProgram.ExtensionID,
			ScreenshotPath:      strings.TrimSpace(input.ScreenshotPath),
			ScreenshotSHA256:    digest,
			ClaimedStars:        stars,
			ClaimedReviewURL:    strings.TrimSpace(input.ClaimedReviewURL),
			CanonicalClaimID:    canonical,
			Status:              entity.ReviewRebateStatusSubmitted,
		}
		result := reviewRebateSession(tx).Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// 并发撞唯一键：另一路已经插进去了 → 语义就是 duplicate。
			return ErrReviewRebateDuplicate
		}
		created = claim
		return nil
	})
	if err != nil {
		return entity.ReviewRebateClaim{}, err
	}
	return created, nil
}

// ReviewRebateClaimListQuery 是提交单列表的筛选条件。
type ReviewRebateClaimListQuery struct {
	// ProgramID 精确匹配活动（0 = 不限）。
	ProgramID int32
	// UserID 精确匹配提交人（0 = 不限）。用户端 handler **强制**填当前账户。
	UserID int32
	// Status 精确匹配状态（空 = 不限）。
	Status string
	// CreatedFrom / CreatedTo 是提交时间范围（nil = 不限）。
	//
	// 边界语义与仓内其它列表**完全一致**（见 controller.parseListFilterTime 与
	// service.ticket.go:236-241）：CreatedFrom 是**闭**下界（>=），
	// CreatedTo 是**开**上界（<）。选开区间而不是 <=23:59:59 的理由：
	//   - created_at 是带毫秒/微秒的 datetime，`<= 23:59:59` 会把
	//     23:59:59.500 这类行漏掉 —— 「查今天」少一条，是最难被发现的那种错；
	//   - 日期形态的上界（2006-01-02）在 controller 侧已被 parseListFilterTime
	//     加到**次日零点**，因此「查 11-10 当天」实际传的是 11-11 00:00 的开上界，
	//     刚好覆盖整天。
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	// AdminView 决定是否下发只有复核人需要的字段（打分理由、截图取图地址）。
	//
	// 只有**管理员**列表置 true。理由：
	//   - 打分理由里写着模型对截图的观察（「正文被裁掉」「截图里没有星级」），
	//     给用户看等于把内部核对标准连同缺陷描述一起交出去；
	//   - 截图取图地址虽然本身鉴权、非管理员拿到 403，但用户端根本用不到它，
	//     下发只会多一个需要长期维护的契约面。
	AdminView bool
}

// ReviewRebateClaimListItem 是列表的一行，带用户名便于管理员对账。
type ReviewRebateClaimListItem struct {
	entity.ReviewRebateClaim
	Username string `json:"username"`
	// AgentRationaleText 是打分理由（人类可读），**仅管理员列表下发**。
	//
	// 字段名刻意与实体里的 AgentRationale 不同：GORM 的 Scan 会把
	// `agent_rationale` 列映射到同名字段，若这里也叫 AgentRationale
	// 就会遮蔽嵌入字段、导致实体那份读不到（或读到空）。
	// JSON 键仍是前端的 `agentRationale`，见 ReviewRebateClaimListQuery.IncludeRationale。
	AgentRationaleText string `json:"agentRationale,omitempty"`
	// ScreenshotURL 是**鉴权代理**地址，不是可直连的静态文件。
	//
	// 截图落盘在私有目录（0700/0600），上传入口刻意不返回任何可直连 URL。
	// 这个字段只是告诉界面「去哪儿取图」：真正读取要带上管理员凭据，
	// 非管理员访问会拿到 403，因此把地址放进列表不会泄露图片内容。
	//
	// 为空表示该行没有截图（历史行或落盘失败）。
	ScreenshotURL string `json:"screenshotUrl"`
}

// ReviewRebateClaimListResult 是列表结果。
type ReviewRebateClaimListResult struct {
	Data     []ReviewRebateClaimListItem `json:"data"`
	Total    int64                       `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"pageSize"`
}

// ListReviewRebateClaimsWithDB 返回提交单列表（分页）。
//
// 用 `data` 而不是 `nil` 初始化切片：前端拿 null 和 [] 的行为不同，
// 空列表必须是 []（与账单列表的约定一致）。
func ListReviewRebateClaimsWithDB(db *gorm.DB, filter ReviewRebateClaimListQuery, page int, pageSize int) (ReviewRebateClaimListResult, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return ReviewRebateClaimListResult{}, ErrReviewRebateSchemaUnavailable
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > reviewRebateMaxPageSize {
		pageSize = reviewRebateDefaultPageSize
	}
	result := ReviewRebateClaimListResult{Page: page, PageSize: pageSize, Data: []ReviewRebateClaimListItem{}}

	// JOIN 放进基础查询构造器：Count 与 Find 必须共用同一套条件。
	base := reviewRebateSession(db).Model(&entity.ReviewRebateClaim{}).
		Joins("LEFT JOIN users AS claim_users ON claim_users.id = review_rebate_claims.user_id")
	if filter.ProgramID > 0 {
		base = base.Where("review_rebate_claims.program_id = ?", filter.ProgramID)
	}
	if filter.UserID > 0 {
		base = base.Where("review_rebate_claims.user_id = ?", filter.UserID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		base = base.Where("review_rebate_claims.status = ?", status)
	}
	// 时间范围：下界闭、上界开。放在 Count 之前，确保 Count 与 Find 用同一套条件
	// （否则会出现「共 3 条」但只列出 1 条的自相矛盾分页）。
	if filter.CreatedFrom != nil {
		base = base.Where("review_rebate_claims.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		base = base.Where("review_rebate_claims.created_at < ?", *filter.CreatedTo)
	}

	if err := base.Session(&gorm.Session{}).Count(&result.Total).Error; err != nil {
		return ReviewRebateClaimListResult{}, err
	}
	if err := base.Session(&gorm.Session{}).
		Select("review_rebate_claims.*, COALESCE(claim_users.username, '') AS username").
		Order("review_rebate_claims.id desc").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&result.Data).Error; err != nil {
		return ReviewRebateClaimListResult{}, err
	}
	if result.Data == nil {
		result.Data = []ReviewRebateClaimListItem{}
	}
	// ScreenshotURL 与 AgentRationaleText 只在 AdminView 下填充，见 ReviewRebateClaimListQuery.AdminView。
	for index := range result.Data {
		if filter.AdminView {
			// 实体上是 json:"-"，这里显式透传一份给管理员列表。
			result.Data[index].AgentRationaleText = result.Data[index].ReviewRebateClaim.AgentRationale
			if strings.TrimSpace(result.Data[index].ScreenshotPath) != "" {
				result.Data[index].ScreenshotURL = fmt.Sprintf("%s/admin/review-rebate/claims/%d/screenshot",
					ReviewRebateAdminAPIPrefix, result.Data[index].ID)
			}
		}
		// ScreenshotPath/SHA256 无论如何都不下发（实体上是 json:"-"）；
		// 这里不清空内存字段，只保证序列化时不会出现。
	}
	return result, nil
}

// ReviewRebateAdminAPIPrefix 是管理端评价返现接口的路径前缀。
//
// 放在这里的理由：它是**前后端契约**的一部分（列表里的 screenshotUrl 要用它拼接，
// 前端页面要按它取图），两处各写一份字符串迟早会漂移。
// 前端 api/ReviewRebateApi.js 的 adminBase 必须与本值一致。
const ReviewRebateAdminAPIPrefix = "/api"

// GetReviewRebateClaimWithDB 按 ID 取一条提交单（管理员详情）。
func GetReviewRebateClaimWithDB(db *gorm.DB, id int32) (entity.ReviewRebateClaim, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
	}
	var claim entity.ReviewRebateClaim
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateClaim{}).Where("id = ?", id).Take(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.ReviewRebateClaim{}, ErrReviewRebateClaimNotFound
		}
		return entity.ReviewRebateClaim{}, err
	}
	return claim, nil
}

// ScoreReviewRebateClaimWithDB 让 Agent 打分并写入 claim，**不发放任何金额**。
//
// 语义（16-*.md §1.1 第 5/6 步 + §1.5）：
//   - 打分只写 AgentStars/AgentContentScore/AgentConfidence/AgentRationale；
//   - 再按规则表算出**候选**金额写进 AwardAmount + MatchedRuleID；
//   - 置信度 < program.MinAgentConfidence → 状态强制 pending_review，**且不算候选金额**；
//   - 置信度达标但规则未命中 → below_threshold + 金额 0；
//   - 置信度达标且规则命中 → scored（仍不发放，等管理员批准）。
//
// 打分失败（超时/网络/模型报错/输出不可解析）→ 返回 ErrReviewRebateScoringFailed，
// **不写入任何分数、不改状态**（保持 submitted）。理由：没有依据的分数比没有分数更危险，
// 一个"恰好看起来及格"的默认分数会让运营直接点批准。调用方（controller）把它翻成 502，
// 管理员可重试或直接人工判断。
//
// 为什么把打分调用放在事务**外面**（与早期实现不同）：
//
//	真实模型调用可能耗时到分钟级。在事务里调模型等于把 claim 的行锁 hold 住整段时间，
//	并发复核（批准/拒绝）会被拖到超时。因此先在事务外读 claim + 打分，再在事务内
//	**重新加锁并复核状态**后才写结果 —— 状态在这期间被别人改过（已批准/已拒绝）
//	就丢弃这次打分结果，绝不覆盖终态行。
//
// 幂等：已批准/已拒绝的行不再打分（否则会把已经入账的金额改掉，账就更乱了）。
func ScoreReviewRebateClaimWithDB(db *gorm.DB, id int32, scorer ReviewRebateScorer) (entity.ReviewRebateClaim, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
	}
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateDisabled
	}
	if scorer == nil {
		scorer = DefaultReviewRebateScorer
	}

	// 阶段一（事务外，可能很慢）：读快照 + 让打分器工作。
	snapshot, err := GetReviewRebateClaimWithDB(db, id)
	if err != nil {
		return entity.ReviewRebateClaim{}, err
	}
	switch snapshot.Status {
	case entity.ReviewRebateStatusApproved, entity.ReviewRebateStatusRejected, entity.ReviewRebateStatusClawedBack:
		return entity.ReviewRebateClaim{}, ErrReviewRebateClaimState
	}
	score, err := scorer.Score(snapshot)
	if err != nil {
		return entity.ReviewRebateClaim{}, err
	}

	// 打分结果先做取值域校验：越界即视为打分失败，绝不放宽成「夹到边界」。
	stars, scoreErr := normalizeReviewRebateScoredStars(score.Stars)
	if scoreErr != nil {
		return entity.ReviewRebateClaim{}, scoreErr
	}
	contentScore, scoreErr := normalizeReviewRebateContentScore(score.ContentScore)
	if scoreErr != nil {
		return entity.ReviewRebateClaim{}, scoreErr
	}
	confidence, scoreErr := normalizeReviewRebateConfidence(score.Confidence)
	if scoreErr != nil {
		return entity.ReviewRebateClaim{}, scoreErr
	}
	rationale := strings.TrimSpace(score.Rationale)
	if rationale == "" {
		// 兜一句可读说明：管理员看到空白理由时无法判断分数怎么来的。
		rationale = fmt.Sprintf("模型给出 %d 星 / %d 分（置信度 %.2f）；系统不核验评价作者归属，请结合截图判断。",
			stars, contentScore, confidence)
	}

	// 阶段二（事务内，很快）：重新加锁 → 复核状态 → 写分数与候选金额。
	var scored entity.ReviewRebateClaim
	txErr := db.Transaction(func(tx *gorm.DB) error {
		var claim entity.ReviewRebateClaim
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&claim).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReviewRebateClaimNotFound
			}
			return err
		}
		// 并发下别人可能刚好完成复核：终态行的分数与金额不可再改。
		switch claim.Status {
		case entity.ReviewRebateStatusApproved, entity.ReviewRebateStatusRejected, entity.ReviewRebateStatusClawedBack:
			return ErrReviewRebateClaimState
		}
		program, err := getReviewRebateProgramByID(tx, claim.ProgramID)
		if err != nil {
			return err
		}

		updates := map[string]any{
			"agent_stars":         stars,
			"agent_content_score": contentScore,
			"agent_confidence":    confidence,
			"agent_rationale":     rationale,
			// 来源标记必须与分数一起落库：只有写进去，管理端才能区分
			// 「模型看过截图」与「占位实现按哈希派生」。
			"agent_scorer": strings.TrimSpace(score.Scorer),
		}
		status := entity.ReviewRebateStatusScored
		amount := 0.0
		matchedRuleID := int32(0)
		switch {
		case confidence < program.MinAgentConfidence:
			// 置信度不足 → 强制人工，**既不套规则也不算金额**。
			// 算出一个候选金额会诱使运营「点一下批准就发」，那等于绕过人工判断。
			status = entity.ReviewRebateStatusPendingReview
		default:
			rules, err := ListReviewRebateRulesWithDB(tx, program.ID)
			if err != nil {
				return err
			}
			if rule := MatchReviewRebateRule(rules, stars, contentScore); rule != nil {
				amount = util.RoundMoney(rule.Amount)
				matchedRuleID = rule.ID
			} else {
				status = entity.ReviewRebateStatusBelowThreshold
			}
		}
		updates["status"] = status
		updates["award_amount"] = amount
		updates["matched_rule_id"] = matchedRuleID

		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).
			Where("id = ?", claim.ID).Updates(updates).Error; err != nil {
			return err
		}
		return reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).Take(&scored).Error
	})
	if txErr != nil {
		return entity.ReviewRebateClaim{}, txErr
	}
	// 成功路径的 trace 在这里写：此刻事务已提交，scored 是回读的落库行，
	// matchedRuleId / awardAmount 是**本次**算出来的真实值。
	//
	// 只有 agent 打分器有过程信息可写（占位打分器不发报告）；
	// 非 agent 实现（测试注入的假打分器）没有 lastTrace，不写 trace 是对的 ——
	// 伪造一条「没有提示词、没有耗时」的 ok 记录只会污染日志。
	if tracer, ok := scorer.(interface {
		reviewRebateTraceFor() (reviewRebateTraceContext, bool)
	}); ok {
		if ctx, has := tracer.reviewRebateTraceFor(); has {
			finalizeReviewRebateScoringTrace(ctx, scored, score)
		}
	}
	return scored, nil
}

// normalizeReviewRebateScoredStars 校验 Agent 给出的星数：必须 1–5。
//
// 与用户自填星数不同，这里 **0 不合法**：Agent 说不出星数就是「无法判定」，
// 应该返回 confidence=0 走强制人工，而不是拿 0 去套规则表。
func normalizeReviewRebateScoredStars(stars int) (int, error) {
	if stars < reviewRebateStarsMin || stars > reviewRebateStarsMax {
		return 0, fmt.Errorf("%w: Agent 星数必须在 %d–%d", ErrReviewRebateInvalidInput, reviewRebateStarsMin, reviewRebateStarsMax)
	}
	return stars, nil
}

// normalizeReviewRebateContentScore 校验 Agent 给出的内容分：必须 1–10。
func normalizeReviewRebateContentScore(score int) (int, error) {
	if score < reviewRebateContentScoreMin || score > reviewRebateContentScoreMax {
		return 0, fmt.Errorf("%w: Agent 内容分必须在 %d–%d",
			ErrReviewRebateInvalidInput, reviewRebateContentScoreMin, reviewRebateContentScoreMax)
	}
	return score, nil
}

// DecideReviewRebateClaimWithDB 管理员批准/拒绝一条提交单。
//
// 批准时的**同一事务**内做三件事（16-*.md §1.1 第 7/8 步）：
//  1. 写 review_rebate_award（claim_id 唯一索引防重复发放）；
//  2. 更新 claim 状态 + award_amount + 复核人 + 时间；
//  3. 累加 users.balance 与 users.gift_balance，并写一条 type=review_rebate 的 billing_records。
//
// 拒绝：只改状态与理由，不碰钱包、不写 award。
//
// 幂等/并发：
//   - 行锁（SELECT ... FOR UPDATE）串行化同一 claim 的并发决策；
//   - award 的 claim_id 唯一索引是最后一道闸：即使锁失效，第二次 Insert 也会撞键，
//     事务回滚 → 钱包不会被加两次。这是「重复批准只留一条 award」的机械保证，
//     不依赖调用方是否先查过状态。
func DecideReviewRebateClaimWithDB(db *gorm.DB, reviewer entity.User, id int32, approve bool, note string) (entity.ReviewRebateClaim, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
	}
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateDisabled
	}
	if reviewer.ID <= 0 {
		return entity.ReviewRebateClaim{}, ErrReviewRebateReviewerInvalid
	}
	original, err := GetReviewRebateClaimWithDB(db, id)
	if err != nil {
		return entity.ReviewRebateClaim{}, err
	}

	var decided entity.ReviewRebateClaim
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := lockReviewRebateUser(tx, original.UserID); err != nil {
			return err
		}
		var claim entity.ReviewRebateClaim
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&claim).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReviewRebateClaimNotFound
			}
			return err
		}
		// 已经是终态 → 不允许再改。重复批准走的是幂等返回而不是报错：
		// 运营点两次按钮不该看到 500，但也不能再发一次钱。
		if claim.Status == entity.ReviewRebateStatusApproved && approve {
			decided = claim
			return nil
		}
		if claim.Status == entity.ReviewRebateStatusApproved || claim.Status == entity.ReviewRebateStatusRejected || claim.Status == entity.ReviewRebateStatusClawedBack {
			return ErrReviewRebateClaimState
		}

		now := time.Now()
		trimmedNote := strings.TrimSpace(note)
		if !approve {
			if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).Updates(map[string]any{
				"status":              entity.ReviewRebateStatusRejected,
				"review_note":         trimmedNote,
				"reviewed_by_user_id": reviewer.ID,
				"reviewed_at":         &now,
			}).Error; err != nil {
				return err
			}
			claim.Status = entity.ReviewRebateStatusRejected
			claim.ReviewNote = trimmedNote
			claim.ReviewedByUserID = reviewer.ID
			claim.ReviewedAt = &now
			decided = claim
			return nil
		}

		// 批准：金额取**打分时快照的候选金额**，不重新套规则 ——
		// 规则被改过之后不能改已提交单的金额（否则运营改配置就能改写历史承诺）。
		program, err := getReviewRebateProgramByID(tx, claim.ProgramID)
		if err != nil {
			return err
		}
		amount := util.RoundMoney(claim.AwardAmount)
		if amount <= 0 {
			return fmt.Errorf("%w: 该提交没有可发放金额", ErrReviewRebateClaimState)
		}
		program.Source = claim.Source
		if err := checkReviewRebateStoreAward(tx, claim.UserID, program, amount); err != nil {
			return err
		}
		eligibilityKey := fmt.Sprintf("%s:%d", claim.Source, claim.UserID)

		// 唯一键防重复发放：DoNothing + RowsAffected==0 表示已经发过。
		award := entity.ReviewRebateAward{
			ClaimID:        claim.ID,
			UserID:         claim.UserID,
			Source:         claim.Source,
			EligibilityKey: &eligibilityKey,
			Amount:         amount,
			GiftSource:     entity.ReviewRebateGiftSource,
			PolicyVersion:  program.PolicyVersion,
		}
		result := reviewRebateSession(tx).Clauses(clause.OnConflict{DoNothing: true}).Create(&award)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrReviewRebateLimitReached
		}

		// 钱包：balance 与 gift_balance 同步增加（口径见 entity.User.GiftBalance 注释）。
		if err := creditReviewRebateGiftInTx(tx, claim.UserID, amount); err != nil {
			return err
		}

		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).Updates(map[string]any{
			"status":              entity.ReviewRebateStatusApproved,
			"award_amount":        amount,
			"review_note":         trimmedNote,
			"reviewed_by_user_id": reviewer.ID,
			"reviewed_at":         &now,
		}).Error; err != nil {
			return err
		}
		claim.Status = entity.ReviewRebateStatusApproved
		claim.AwardAmount = amount
		claim.ReviewNote = trimmedNote
		claim.ReviewedByUserID = reviewer.ID
		claim.ReviewedAt = &now
		decided = claim
		return nil
	})
	if err != nil {
		return entity.ReviewRebateClaim{}, err
	}
	return decided, nil
}

// creditReviewRebateGiftInTx 在事务内给账户加赠金并写一条账单流水。
//
// 三者关系（这是「赠金归口」的全部落点，2026-11-09 双桶模型）：
//   - users.gift_balance += amount  —— 赠送金额桶：**可消费、不可退款**，下限 0；
//   - users.balance 不动            —— balance 是充值本金、可退款，返现进这里
//     就等于把「送的钱」变成可退现金；
//   - billing_records 一条 type=review_rebate —— 账单流水，让用户在「账单流水」
//     里看得见这笔收入，也让管理员能按 type 汇总。
//
// 入账按规则 3（43-*.md）：账户若有欠款（balance<0），**先抵欠款**，剩余才进赠金桶。
// 否则会出现「balance<0 且 gift>0」这种违反不变量 I3 的状态。
//
// 钱包口径：赠金计入可用额度（本金 + 赠金）但不计入可退本金/净实付/返佣基数。
// 退款与邀请返佣只认充值实付本金，两者都不读 gift_balance —— 这正是把它单独记一列的原因。
func creditReviewRebateGiftInTx(tx *gorm.DB, userID int32, amount float64) error {
	amount = util.RoundMoney(amount)
	if amount <= 0 {
		return nil
	}
	var user entity.User
	if err := reviewRebateSession(tx).Model(&entity.User{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReviewRebateReviewerInvalid
		}
		return err
	}
	balanceBefore := util.RoundMoney(user.Balance)
	giftBefore := util.RoundMoney(user.GiftBalance)
	newBalance, giftAfter := CreditGiftBuckets(user.Balance, user.GiftBalance, amount)
	if err := reviewRebateSession(tx).Model(&entity.User{}).Where("id = ?", userID).Updates(map[string]any{
		"balance":      newBalance,
		"gift_balance": giftAfter,
	}).Error; err != nil {
		return err
	}
	record := entity.BillingRecord{
		UserID:            userID,
		Type:              ReviewRebateBillingRecordType,
		Provider:          "system",
		Amount:            amount,
		BalanceBefore:     balanceBefore,
		BalanceAfter:      newBalance,
		UnitPriceSnapshot: referralGiftSnapshot(giftBefore, giftAfter),
		RequestID:         fmt.Sprintf("review_rebate_%d", userID),
		Remark:            joinReferralNotes("评价返现", referralDebtOffsetNote(balanceBefore, newBalance)),
	}
	return reviewRebateSession(tx).Create(&record).Error
}
