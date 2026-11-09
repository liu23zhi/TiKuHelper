package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAccessTokenQuotaEpochConflict 表示同一令牌的并发重置被唯一索引挡下。
// 调用方据此返回 409，而不是把「只成功一次」伪装成成功。
var ErrAccessTokenQuotaEpochConflict = errors.New("额度周期已被并发重置，请重试")

// ErrAccessTokenQuotaEpochAdminRequired 表示重置额度周期是管理员操作。
// 路由上已挂 middleware.RequireAdmin；服务层再判一次，避免有人绕过路由直接调服务。
var ErrAccessTokenQuotaEpochAdminRequired = errors.New("仅管理员可重置额度周期")

// ErrAccessTokenQuotaWindowEmpty 表示当前本轮已用已经是 0，重置没有意义。
//
// 这条判定同时承担「并发重置只成功一次」的语义：同一批并发请求都看到
// 「本轮已用 12」，算出的都是 epoch+1 与同一个 baseline，唯一索引只放行一方；
// 晚到的一方读到的已经是最新 epoch、本轮已用 0，于是被这里拒绝。
// 它也能挡住「连点两下」产生的空 epoch 行 —— 那种行的 baseline 与前一轮完全相同，
// 没有任何信息量，只会污染审计与对账。
var ErrAccessTokenQuotaWindowEmpty = errors.New("当前额度周期已为 0，无需重置")

// AccessTokenQuotaWindow 是「当前额度周期」的只读视图。
//
// 语义（P14 决策 #16）：
//
//	WindowUsed    = TotalAmountUsed - BaselineAmount（本轮已用）
//	TotalAmountUsed 始终是历史累计，重置不会让它变小
type AccessTokenQuotaWindow struct {
	// Epoch 为 0 表示该令牌从未重置过，此时 BaselineAmount 也是 0。
	Epoch          int32     `json:"epoch"`
	BaselineAmount float64   `json:"baselineAmount"`
	WindowUsed     float64   `json:"windowUsed"`
	ResetAt        time.Time `json:"resetAt,omitempty"`
}

// ResetAccessTokenQuotaWindowResult 是重置接口的返回体。
//
// 刻意把「重置后的本轮已用」「历史累计」「钱包」「限额」四个数字放在同一层，
// 因为验收样例就是这四个数一起看的：
// 余额 8 / 限额 20 / 已用 12 → 重置后：本轮已用 0、历史累计仍 12、余额仍 8、限额仍 20。
type ResetAccessTokenQuotaWindowResult struct {
	AccessToken       entity.AccessToken     `json:"accessToken"`
	QuotaWindow       AccessTokenQuotaWindow `json:"quotaWindow"`
	Balance           float64                `json:"balance"`
	TotalQuotaLimit   float64                `json:"totalQuotaLimit"`
	HistoryAmountUsed float64                `json:"historyAmountUsed"`
	EventID           string                 `json:"eventId"`
}

// LoadAccessTokenQuotaWindowWithDB 读取令牌当前的额度周期视图。
//
// 没有 epoch 行时返回 Epoch=0 / Baseline=0 / WindowUsed=历史累计 ——
// 这正是「重置功能上线前」的行为，因此对既有额度判定逻辑的行为变化为零。
func LoadAccessTokenQuotaWindowWithDB(db *gorm.DB, token entity.AccessToken) (AccessTokenQuotaWindow, error) {
	epoch, found, err := latestAccessTokenQuotaEpochWithDB(db, token.ID)
	if err != nil {
		return AccessTokenQuotaWindow{}, err
	}
	return buildAccessTokenQuotaWindow(token, epoch, found), nil
}

// buildAccessTokenQuotaWindow 是「本轮已用」唯一的计算入口。
//
// 本轮已用 = TotalAmountUsed - 当前 epoch 的 baseline_amount
//
// 三处细节：
//   - 减法结果按 6 位口径取整（util.RoundMoney），与余额/账单口径一致；
//   - 负数（理论上只可能来自人工改库）按 0 处理，不让「本轮已用」出现负值；
//   - 这里**只读**，绝不写 TotalAmountUsed —— 它是历史累计，永不清零。
func buildAccessTokenQuotaWindow(token entity.AccessToken, epoch entity.AccessTokenQuotaEpoch, found bool) AccessTokenQuotaWindow {
	window := AccessTokenQuotaWindow{}
	if !found {
		// 从未重置：本轮已用 = 历史累计。
		window.WindowUsed = util.RoundMoney(token.TotalAmountUsed)
		return window
	}
	window.Epoch = epoch.Epoch
	window.BaselineAmount = util.RoundMoney(epoch.BaselineAmount)
	window.ResetAt = epoch.ResetAt
	used := util.RoundMoney(token.TotalAmountUsed) - util.RoundMoney(epoch.BaselineAmount)
	if used < 0 {
		used = 0
	}
	window.WindowUsed = util.RoundMoney(used)
	return window
}

// ResetAccessTokenQuotaWindowWithDB 重置令牌的额度周期。
//
// 做法是**追加一行 epoch**，不是把 access_tokens.total_amount_used 清零：
//   - baseline_amount = 重置那一刻的 total_amount_used（历史累计的快照）；
//   - epoch = 当前最大 epoch + 1（首次为 1）；
//   - total_amount_used 一列不动 —— 这是本设计的红线。
//
// 并发语义：两个管理员同时重置会算出同一个 epoch，唯一索引 (token_id, epoch)
// 只放行一方；另一方拿到唯一冲突并返回 ErrAccessTokenQuotaEpochConflict，
// 不会插入两行、也不会把 baseline 写两次。
func ResetAccessTokenQuotaWindowWithDB(db *gorm.DB, operator entity.User, tokenID int32, clientIP string) (ResetAccessTokenQuotaWindowResult, error) {
	if !isAdminUser(operator) {
		return ResetAccessTokenQuotaWindowResult{}, ErrAccessTokenQuotaEpochAdminRequired
	}
	if db == nil {
		return ResetAccessTokenQuotaWindowResult{}, errors.New("数据库不可用")
	}
	eventID := "quota-epoch-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var result ResetAccessTokenQuotaWindowResult
	err := db.Transaction(func(tx *gorm.DB) error {
		accessToken, err := loadAccessibleAccessToken(tx.Clauses(clause.Locking{Strength: "UPDATE"}), operator, tokenID)
		if err != nil {
			return err
		}
		latest, found, err := latestAccessTokenQuotaEpochWithDB(tx, accessToken.ID)
		if err != nil {
			return err
		}
		nextEpoch := int32(1)
		if found {
			nextEpoch = latest.Epoch + 1
		}
		// 本轮已用已经是 0 时拒绝重置：既挡住「连点两下」的空 epoch，
		// 也构成并发重置的第二道闸（第一道是唯一索引）。
		if buildAccessTokenQuotaWindow(accessToken, latest, found).WindowUsed == 0 {
			return ErrAccessTokenQuotaWindowEmpty
		}
		// 历史累计的快照。**这里只读 total_amount_used，绝不写它。**
		baseline := util.RoundMoney(accessToken.TotalAmountUsed)
		resetAt := time.Now().In(logger.ShanghaiLocation())
		epochRow := entity.AccessTokenQuotaEpoch{
			TokenID:        accessToken.ID,
			Epoch:          nextEpoch,
			ResetAt:        resetAt,
			BaselineAmount: baseline,
			OperatorID:     operator.ID,
			EventID:        eventID,
		}
		if err := tx.Create(&epochRow).Error; err != nil {
			// 唯一索引 (token_id, epoch) 冲突 = 并发重置，只有一方能成功。
			if isUniqueConstraintViolation(err) {
				return ErrAccessTokenQuotaEpochConflict
			}
			return err
		}
		// 额度判定读的是 total_amount_used，这里不动它；
		// 本轮已用由 baseline 相减得到，所以重置后立刻是 0。
		if err := writeAccessTokenQuotaEpochAudit(tx, operator, accessToken, epochRow, clientIP); err != nil {
			return err
		}
		var owner entity.User
		if err := tx.Where("id = ?", accessToken.UserID).First(&owner).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		result = ResetAccessTokenQuotaWindowResult{
			AccessToken:       accessToken,
			QuotaWindow:       buildAccessTokenQuotaWindow(accessToken, epochRow, true),
			Balance:           util.RoundMoney(owner.Balance),
			TotalQuotaLimit:   util.RoundMoney(accessToken.TotalQuotaLimit),
			HistoryAmountUsed: util.RoundMoney(accessToken.TotalAmountUsed),
			EventID:           eventID,
		}
		return nil
	})
	if err != nil {
		return ResetAccessTokenQuotaWindowResult{}, err
	}
	return result, nil
}

// latestAccessTokenQuotaEpochWithDB 取该令牌当前（最大）的 epoch 行。
//
// 用 epoch 降序取第一条而不是「按 reset_at 排序」：epoch 是唯一索引的一部分，
// 单调递增且不受时钟回拨影响；reset_at 只是展示用的业务时间。
func latestAccessTokenQuotaEpochWithDB(db *gorm.DB, tokenID int32) (entity.AccessTokenQuotaEpoch, bool, error) {
	var rows []entity.AccessTokenQuotaEpoch
	if err := db.Where("token_id = ?", tokenID).Order("epoch desc").Limit(1).Find(&rows).Error; err != nil {
		return entity.AccessTokenQuotaEpoch{}, false, err
	}
	if len(rows) == 0 {
		return entity.AccessTokenQuotaEpoch{}, false, nil
	}
	return rows[0], true, nil
}

// isUniqueConstraintViolation 识别唯一约束冲突。
//
// 不引入驱动专属错误类型（sqlite 与 mysql 的驱动包在这里都要保持轻依赖），
// 因此按错误文本判定；两种驱动的原文分别是
// "UNIQUE constraint failed: ..." 与 "Error 1062 (23000): Duplicate entry ..."。
func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "error 1062")
}

// writeAccessTokenQuotaEpochAudit 记录重置审计：operator_id + event_id 落在同一行，
// 审计日志里带上 event_id，便于把「谁在什么时候把谁的额度周期推到第几轮」串起来。
func writeAccessTokenQuotaEpochAudit(tx *gorm.DB, operator entity.User, accessToken entity.AccessToken, epochRow entity.AccessTokenQuotaEpoch, clientIP string) error {
	return WriteAccountAuditWithDB(tx, AccountAuditEvent{
		UserID:        operator.ID,
		Username:      operator.Username,
		Module:        "access_token",
		Action:        3, // 3 = 重置额度周期（1 更新 / 2 删除 / 0 创建）
		Description:   fmt.Sprintf("重置访问令牌额度周期（第 %d 轮）", epochRow.Epoch),
		ClientIP:      strings.TrimSpace(clientIP),
		CorrelationID: epochRow.EventID,
		Request: map[string]any{
			"tokenId":         accessToken.ID,
			"targetUserId":    accessToken.UserID,
			"totalQuotaLimit": accessToken.TotalQuotaLimit,
			"before": map[string]any{
				"epoch":           epochRow.Epoch - 1,
				"totalAmountUsed": accessToken.TotalAmountUsed,
			},
		},
		Result: map[string]any{
			"status":          "success",
			"tokenId":         accessToken.ID,
			"targetUserId":    accessToken.UserID,
			"totalQuotaLimit": accessToken.TotalQuotaLimit,
			"after": map[string]any{
				"epoch":          epochRow.Epoch,
				"baselineAmount": epochRow.BaselineAmount,
				// 历史累计在重置前后必须完全相同，写进审计便于事后核对。
				"totalAmountUsed": accessToken.TotalAmountUsed,
			},
			"changedFields": []string{"quotaEpoch"},
		},
	})
}
