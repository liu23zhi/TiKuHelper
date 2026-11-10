package controller

import (
	"time"

	"github.com/itihey/tikuAdapter/internal/service"
)

// 评价返现提交单的**两套 DTO**（45-用户待确认问题清单.md §8.4 / Q73-4）。
//
// 为什么必须是两个独立类型，而不是一个类型加 `omitempty`：
//
//	`agentRationale` 是审计数据 —— 模型对截图的观察结论（「正文被裁掉」
//	「截图里没有星级」）。「我的提交」页**对所有角色包括管理员**都不下发它，
//	理由是避免有人用管理员账号提交一条评价、再回到自己的「我的提交」页
//	把评分口径试出来。
//
//	用同一个结构体 + omitempty 的话，「不出现」只是「值为空时的副作用」：
//	只要某条记录恰好被打过分，键就会冒出来；而且它要求**每个调用方都记得
//	把字段清空** —— 这正是迟早会漏的地方，而且漏了不会有任何报错。
//
//	所以这里的选择是：类型层面就没有这个字段，漏不下发都做不到。
//
// 两个类型都**不嵌入 entity.ReviewRebateClaim**：嵌入会把实体上 json:"-" 的
// 字段（screenshotPath / screenshotSha256 / canonicalClaimId / agentRationale）
// 变成「跟着实体走」的隐式契约，实体一改标签就悄悄影响两个列表。逐字段显式
// 列出，改哪边一目了然。同理，admin 那份**也不嵌入** my 那份：嵌入未导出类型
// 的内嵌字段在 encoding/json 里是可用的，但一旦有人给 my 那份加字段，admin 会
// 被动跟着变 —— 两套契约必须能各自演化。

// myReviewRebateClaimDTO 是「我的提交」列表的一行。
//
// **不含 agentRationale、不含 screenshotUrl** —— 这不是疏忽，见上面的说明。
type myReviewRebateClaimDTO struct {
	ID                int32      `json:"id"`
	ProgramID         int32      `json:"programId"`
	UserID            int32      `json:"userId"`
	Username          string     `json:"username"`
	ClaimedStars      int        `json:"claimedStars"`
	ClaimedReviewURL  string     `json:"claimedReviewUrl"`
	Status            string     `json:"status"`
	AgentStars        int        `json:"agentStars"`
	AgentContentScore int        `json:"agentContentScore"`
	AgentConfidence   float64    `json:"agentConfidence"`
	AgentScorer       string     `json:"agentScorer"`
	MatchedRuleID     int32      `json:"matchedRuleId"`
	AwardAmount       float64    `json:"awardAmount"`
	ReviewNote        string     `json:"reviewNote"`
	ReviewedByUserID  int32      `json:"reviewedByUserId"`
	CreatedAt         time.Time  `json:"createdAt"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
}

// adminReviewRebateClaimDTO 是「评价返现提交管理」独立页的一行。
//
// 比「我的提交」多两项，都是复核必需、且只对管理员可见：
//   - agentRationale：模型给的理由，复核人判断「该不该批准」靠它；
//   - screenshotUrl：截图落在私有目录、没有可直连 URL，这个地址必须带管理员
//     凭据访问（非管理员拿到 403），所以放进列表不会泄露图片内容。
type adminReviewRebateClaimDTO struct {
	ID                int32      `json:"id"`
	ProgramID         int32      `json:"programId"`
	UserID            int32      `json:"userId"`
	Username          string     `json:"username"`
	ClaimedStars      int        `json:"claimedStars"`
	ClaimedReviewURL  string     `json:"claimedReviewUrl"`
	Status            string     `json:"status"`
	AgentStars        int        `json:"agentStars"`
	AgentContentScore int        `json:"agentContentScore"`
	AgentConfidence   float64    `json:"agentConfidence"`
	AgentRationale    string     `json:"agentRationale"`
	AgentScorer       string     `json:"agentScorer"`
	MatchedRuleID     int32      `json:"matchedRuleId"`
	AwardAmount       float64    `json:"awardAmount"`
	ScreenshotURL     string     `json:"screenshotUrl"`
	ReviewNote        string     `json:"reviewNote"`
	ReviewedByUserID  int32      `json:"reviewedByUserId"`
	CreatedAt         time.Time  `json:"createdAt"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
}

// myReviewRebateClaimList 是「我的提交」的信封（与既有分页形状一致）。
type myReviewRebateClaimList struct {
	Data     []myReviewRebateClaimDTO `json:"data"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
}

// adminReviewRebateClaimList 是「提交管理」独立页的信封。
type adminReviewRebateClaimList struct {
	Data     []adminReviewRebateClaimDTO `json:"data"`
	Total    int64                       `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"pageSize"`
}

// toMyReviewRebateClaimDTO 把服务层的一行转成「我的提交」行。
//
// 刻意**不读** item.AgentRationaleText：目标类型里没有落点，读不到也带不出去。
func toMyReviewRebateClaimDTO(item service.ReviewRebateClaimListItem) myReviewRebateClaimDTO {
	return myReviewRebateClaimDTO{
		ID:                item.ID,
		ProgramID:         item.ProgramID,
		UserID:            item.UserID,
		Username:          item.Username,
		ClaimedStars:      item.ClaimedStars,
		ClaimedReviewURL:  item.ClaimedReviewURL,
		Status:            item.Status,
		AgentStars:        item.AgentStars,
		AgentContentScore: item.AgentContentScore,
		AgentConfidence:   item.AgentConfidence,
		AgentScorer:       item.AgentScorer,
		MatchedRuleID:     item.MatchedRuleID,
		AwardAmount:       item.AwardAmount,
		ReviewNote:        item.ReviewNote,
		ReviewedByUserID:  item.ReviewedByUserID,
		CreatedAt:         item.CreatedAt,
		ReviewedAt:        item.ReviewedAt,
	}
}

// toAdminReviewRebateClaimDTO 把服务层的一行转成「提交管理」行。
//
// 只有服务层以 AdminView=true 查询时 AgentRationaleText / ScreenshotURL 才被填充；
// 本函数如实搬运，不做二次判断 —— 判断点在查询参数的构造处（handler 里）。
func toAdminReviewRebateClaimDTO(item service.ReviewRebateClaimListItem) adminReviewRebateClaimDTO {
	return adminReviewRebateClaimDTO{
		ID:                item.ID,
		ProgramID:         item.ProgramID,
		UserID:            item.UserID,
		Username:          item.Username,
		ClaimedStars:      item.ClaimedStars,
		ClaimedReviewURL:  item.ClaimedReviewURL,
		Status:            item.Status,
		AgentStars:        item.AgentStars,
		AgentContentScore: item.AgentContentScore,
		AgentConfidence:   item.AgentConfidence,
		AgentRationale:    item.AgentRationaleText,
		AgentScorer:       item.AgentScorer,
		MatchedRuleID:     item.MatchedRuleID,
		AwardAmount:       item.AwardAmount,
		ScreenshotURL:     item.ScreenshotURL,
		ReviewNote:        item.ReviewNote,
		ReviewedByUserID:  item.ReviewedByUserID,
		CreatedAt:         item.CreatedAt,
		ReviewedAt:        item.ReviewedAt,
	}
}

// toMyReviewRebateClaimDTOs 转换切片；nil 也返回空切片（序列化成 [] 而不是 null）。
func toMyReviewRebateClaimDTOs(items []service.ReviewRebateClaimListItem) []myReviewRebateClaimDTO {
	converted := make([]myReviewRebateClaimDTO, 0, len(items))
	for _, item := range items {
		converted = append(converted, toMyReviewRebateClaimDTO(item))
	}
	return converted
}

// toAdminReviewRebateClaimDTOs 转换切片；nil 也返回空切片。
func toAdminReviewRebateClaimDTOs(items []service.ReviewRebateClaimListItem) []adminReviewRebateClaimDTO {
	converted := make([]adminReviewRebateClaimDTO, 0, len(items))
	for _, item := range items {
		converted = append(converted, toAdminReviewRebateClaimDTO(item))
	}
	return converted
}
