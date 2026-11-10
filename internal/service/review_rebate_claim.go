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
}

// ReviewRebateProgramView 是用户端的活动视图（脱敏）。
//
// 与 admin 视图的区别：
//   - 不下发 ExtensionID（对用户没有意义，且是内部抓取范围标识）；
//   - 不下发 PolicyVersion 的原始值以外的东西（保留是给用户看「按哪版规则」）；
//   - 带上我的已用次数/已发金额/剩余额度，让用户知道还能不能提交。
type ReviewRebateProgramView struct {
	Program ReviewRebateProgramSummary `json:"program"`
	// Rules 是规则表（脱敏后），让用户能自己看明白「写什么评价能拿多少」。
	Rules []ReviewRebateRuleView `json:"rules"`
	// Usage 是本人额度使用情况。
	Usage ReviewRebateUsage `json:"usage"`
	// AuthorshipVerified 恒为 false：系统**不做**作者归属断言（16-*.md §1.5），
	// 界面上要明确告知用户「只核验公开评价是否存在且内容一致」。
	AuthorshipVerified bool `json:"authorshipVerified"`
	// Notice 是给用户的边界说明，前端直接展示。
	Notice string `json:"notice"`
}

// ReviewRebateProgramSummary 是活动的用户可见字段。
type ReviewRebateProgramSummary struct {
	ID int32 `json:"id"`
	// Enabled 是活动开关：用户端靠它决定「显示规则表与提交表单」还是「功能未开启」。
	//
	// 必须下发：总开关打开但活动行被禁用时，用户端若拿不到这个字段就会一直显示
	// （或一直不显示）错误的状态，而入口本身是拒绝的 —— 界面与后端结论会打架。
	Enabled            bool    `json:"enabled"`
	Name               string  `json:"name"`
	StoreURL           string  `json:"storeUrl"`
	MaxClaimsPerUser   int     `json:"maxClaimsPerUser"`
	MaxAmountPerUser   float64 `json:"maxAmountPerUser"`
	MinAgentConfidence float64 `json:"minAgentConfidence"`
	PolicyVersion      string  `json:"policyVersion"`
	// TutorialVersionID 是关联图文教程版本（0 = 无）。
	TutorialVersionID int32 `json:"tutorialVersionId"`
}

// ReviewRebateRuleView 是规则的脱敏视图（去掉 remark 里的内部信息不必，remark 本身就是给人看的）。
type ReviewRebateRuleView struct {
	ID              int32   `json:"id"`
	MinStars        int     `json:"minStars"`
	MaxStars        int     `json:"maxStars"`
	MinContentScore int     `json:"minContentScore"`
	MaxContentScore int     `json:"maxContentScore"`
	Amount          float64 `json:"amount"`
	Priority        int     `json:"priority"`
	Remark          string  `json:"remark"`
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
	ApprovedAmount float64 `json:"approvedAmount"`
	// PendingCount 是审核中的条数（不占额度，但展示出来避免用户以为没提交上）。
	PendingCount int64 `json:"pendingCount"`
	// RejectedCount 是被拒绝的条数（展示用，不占额度）。
	RejectedCount int64 `json:"rejectedCount"`
	// RemainingClaims/RemainingAmount 是剩余额度（负数截断为 0）。
	RemainingClaims int     `json:"remainingClaims"`
	RemainingAmount float64 `json:"remainingAmount"`
}

// reviewRebateAuthorshipNotice 是给用户看的归属边界说明。
const reviewRebateAuthorshipNotice = "系统只核验「公开评价里存在一条与截图内容一致的记录」，" +
	"不做作者归属断言；置信度不足时由人工复核，结果与金额由运营规则表决定。"

// GetReviewRebateProgramViewWithDB 组装用户端的活动视图。
//
// 开关关闭 / 活动不存在 / 表未就绪 → 返回业务错误（controller 映射 4xx）。
func GetReviewRebateProgramViewWithDB(db *gorm.DB, userID int32) (ReviewRebateProgramView, error) {
	program, err := GetReviewRebateProgramWithDB(db)
	if err != nil {
		return ReviewRebateProgramView{}, err
	}
	rules, err := ListReviewRebateRulesWithDB(db, program.ID)
	if err != nil {
		return ReviewRebateProgramView{}, err
	}
	usage, err := GetReviewRebateUsageWithDB(db, program.ID, userID)
	if err != nil {
		return ReviewRebateProgramView{}, err
	}
	view := ReviewRebateProgramView{
		Program: ReviewRebateProgramSummary{
			ID:                 program.ID,
			Enabled:            program.Enabled,
			Name:               program.Name,
			StoreURL:           program.StoreURL,
			MaxClaimsPerUser:   program.MaxClaimsPerUser,
			MaxAmountPerUser:   util.RoundMoney(program.MaxAmountPerUser),
			MinAgentConfidence: program.MinAgentConfidence,
			PolicyVersion:      program.PolicyVersion,
			TutorialVersionID:  program.TutorialVersionID,
		},
		Rules:              make([]ReviewRebateRuleView, 0, len(rules)),
		Usage:              usage,
		AuthorshipVerified: false,
		Notice:             reviewRebateAuthorshipNotice,
	}
	for _, rule := range rules {
		if !rule.Enabled {
			// 停用规则不给用户看：看到却拿不到比看不到更让人困惑。
			continue
		}
		view.Rules = append(view.Rules, ReviewRebateRuleView{
			ID:              rule.ID,
			MinStars:        rule.MinStars,
			MaxStars:        rule.MaxStars,
			MinContentScore: rule.MinContentScore,
			MaxContentScore: rule.MaxContentScore,
			Amount:          util.RoundMoney(rule.Amount),
			Priority:        rule.Priority,
			Remark:          rule.Remark,
		})
	}
	return view, nil
}

// GetReviewRebateUsageWithDB 统计本人在某活动口径下的额度使用情况。
//
// 口径（明确写在这里，避免「次数」被两处各算一套）：
//   - ClaimedCount/ApprovedAmount 只统计 **approved**：未批准的提交不消耗额度，
//     否则用户提交一次被拒就先丢一次机会，体验与运营意图都不对；
//   - **按 user 跨活动累计，不按 program 分账** —— 这是 16-*.md §1.2
//     「活动 ID（换版不重置已领次数）」的直接要求：换版如果重置次数，
//     用户只要等一次改版就能无限领。去重才按 program 计（见唯一索引），
//     额度按账号计。
//   - PendingCount 统计审核中的（submitted/scraping/scored/pending_review）；
//   - duplicate/limit_reached/below_threshold/rejected 都不占额度。
//
// programID 只用来读该活动的上限与默认值，不参与「已用」的过滤。
func GetReviewRebateUsageWithDB(db *gorm.DB, programID int32, userID int32) (ReviewRebateUsage, error) {
	usage := ReviewRebateUsage{}
	if programID <= 0 || userID <= 0 {
		return usage, nil
	}
	if !ReviewRebateSchemaAvailable(db) {
		return usage, ErrReviewRebateSchemaUnavailable
	}
	type statusAggregate struct {
		Status string  `gorm:"column:status"`
		Total  float64 `gorm:"column:total"`
		Rows   int64   `gorm:"column:row_count"`
	}
	var rows []statusAggregate
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateClaim{}).
		Select("status, COALESCE(SUM(award_amount), 0) AS total, COUNT(*) AS row_count").
		Where("user_id = ?", userID).
		Group("status").Scan(&rows).Error; err != nil {
		return usage, err
	}
	for _, row := range rows {
		switch row.Status {
		case entity.ReviewRebateStatusApproved:
			usage.ClaimedCount += row.Rows
		case entity.ReviewRebateStatusSubmitted, entity.ReviewRebateStatusScraping,
			entity.ReviewRebateStatusScored, entity.ReviewRebateStatusPendingReview:
			usage.PendingCount += row.Rows
		case entity.ReviewRebateStatusRejected:
			usage.RejectedCount += row.Rows
		}
	}
	// 两个键同值：ClaimedCount 是额度口径的历史名字，ApprovedCount 是界面读的名字。
	usage.ApprovedCount = usage.ClaimedCount
	// 金额以发放流水（review_rebate_awards）为准，而不是 claim 的候选金额：
	// 只有 award 行代表钱真的进过钱包，这也是唯一能与账单对上的口径。
	var awardTotal float64
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateAward{}).
		Select("COALESCE(SUM(amount), 0)").Where("user_id = ?", userID).
		Scan(&awardTotal).Error; err != nil {
		return usage, err
	}
	usage.ApprovedAmount = util.RoundMoney(awardTotal)

	program, err := getReviewRebateProgramByID(db, programID)
	if err != nil && !errors.Is(err, ErrReviewRebateProgramNotFound) {
		return usage, err
	}
	usage.RemainingClaims = program.MaxClaimsPerUser - int(usage.ClaimedCount)
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
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateDisabled
	}
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
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
		program, err := getReviewRebateProgramByID(tx, input.ProgramID)
		if err != nil {
			return err
		}
		if !program.Enabled {
			return ErrReviewRebateDisabled
		}
		canonical := BuildReviewRebateCanonicalClaimID(stars, digest, input.ClaimedReviewURL)

		// 闸 3：同账号同评价去重。先查一次给出干净的 duplicate 结论，
		// 再靠唯一索引兜住并发（并发时第二个事务会在 Create 上撞唯一键）。
		var existingCount int64
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).
			Where("program_id = ? AND user_id = ? AND canonical_claim_id = ?",
				input.ProgramID, input.UserID, canonical).Count(&existingCount).Error; err != nil {
			return err
		}
		if existingCount > 0 {
			return ErrReviewRebateDuplicate
		}

		// 闸 4：额度。**先锁用户行**再统计 —— 这是并发正确性的关键。
		//
		// 为什么不能只对 claim 行加 FOR UPDATE：当「一条 approved 都没有」时
		// （正是「还剩最后 1 次」的场景），FOR UPDATE 锁的是**空集合**，
		// 两个并发事务都拿到空集、都通过检查、都插入成功 —— 次数就被突破了。
		// 锁 users 里那一行是唯一一个「一定存在」的互斥点：第二个事务在这里排队，
		// 等第一个提交后再统计，就能看到刚批准的那条。
		//
		// 统**按 user 跨活动累计**（不加 program_id 过滤）：这是 16-*.md §1.2
		// 「活动 ID（换版不重置已领次数）」的直接要求 —— 换版若重置次数，
		// 用户等一次改版就能无限领。去重才按 program 计，额度按账号计。
		var walletRow entity.User
		if err := reviewRebateSession(tx).Model(&entity.User{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", input.UserID).
			Take(&walletRow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: 用户不存在", ErrReviewRebateInvalidInput)
			}
			return err
		}
		var approvedRows []entity.ReviewRebateClaim
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).
			Where("user_id = ? AND status = ?", input.UserID, entity.ReviewRebateStatusApproved).
			Find(&approvedRows).Error; err != nil {
			return err
		}
		approvedCount := len(approvedRows)
		if approvedCount >= program.MaxClaimsPerUser {
			return ErrReviewRebateLimitReached
		}
		approvedAmount := 0.0
		for _, row := range approvedRows {
			approvedAmount += row.AwardAmount
		}
		approvedAmount = util.RoundMoney(approvedAmount)
		if approvedAmount >= util.RoundMoney(program.MaxAmountPerUser) {
			return ErrReviewRebateLimitReached
		}

		claim := entity.ReviewRebateClaim{
			ProgramID:        input.ProgramID,
			UserID:           input.UserID,
			ScreenshotPath:   strings.TrimSpace(input.ScreenshotPath),
			ScreenshotSHA256: digest,
			ClaimedStars:     stars,
			ClaimedReviewURL: strings.TrimSpace(input.ClaimedReviewURL),
			CanonicalClaimID: canonical,
			Status:           entity.ReviewRebateStatusSubmitted,
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
const ReviewRebateAdminAPIPrefix = "/adapter-service"

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
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateDisabled
	}
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
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
	case entity.ReviewRebateStatusApproved, entity.ReviewRebateStatusRejected:
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
		case entity.ReviewRebateStatusApproved, entity.ReviewRebateStatusRejected:
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
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateDisabled
	}
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateClaim{}, ErrReviewRebateSchemaUnavailable
	}
	if reviewer.ID <= 0 {
		return entity.ReviewRebateClaim{}, ErrReviewRebateReviewerInvalid
	}

	var decided entity.ReviewRebateClaim
	err := db.Transaction(func(tx *gorm.DB) error {
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
		if claim.Status == entity.ReviewRebateStatusApproved || claim.Status == entity.ReviewRebateStatusRejected {
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

		// 唯一键防重复发放：DoNothing + RowsAffected==0 表示已经发过。
		award := entity.ReviewRebateAward{
			ClaimID:       claim.ID,
			UserID:        claim.UserID,
			Amount:        amount,
			GiftSource:    entity.ReviewRebateGiftSource,
			PolicyVersion: program.PolicyVersion,
		}
		result := reviewRebateSession(tx).Clauses(clause.OnConflict{DoNothing: true}).Create(&award)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// 已经有一条 award（并发/重放）→ 不重复加钱，但把 claim 状态补齐。
			if err := reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).
				Updates(map[string]any{
					"status":              entity.ReviewRebateStatusApproved,
					"review_note":         trimmedNote,
					"reviewed_by_user_id": reviewer.ID,
					"reviewed_at":         &now,
				}).Error; err != nil {
				return err
			}
			claim.Status = entity.ReviewRebateStatusApproved
			claim.ReviewNote = trimmedNote
			claim.ReviewedByUserID = reviewer.ID
			claim.ReviewedAt = &now
			decided = claim
			return nil
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
