package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

type AccountAuditEvent struct {
	UserID        int32
	Username      string
	Module        string
	Action        int32
	Description   string
	ClientIP      string
	CorrelationID string
	Request       map[string]any
	Result        map[string]any
}

// Account events use an allowlist so credentials cannot enter audit snapshots.
func accountAuditFields(fields map[string]any) map[string]any {
	allowed := map[string]bool{
		"status": true, "message": true, "code": true, "scene": true, "phone": true,
		"provider": true, "orderNo": true, "amount": true, "paymentType": true,
		"targetUserId": true, "tokenId": true, "ownerId": true, "name": true,
		"enabled": true, "scope": true, "remark": true, "username": true, "nickname": true, "role": true,
		"before": true, "after": true, "changedFields": true, "method": true,
		"requestId": true, "durationMs": true, "smsRecordId": true, "billingId": true,
		"totalQuotaLimit": true, "perMinuteLimit": true, "perDayLimit": true,
		// 额度周期重置（P14）：只有 epoch / baseline / 历史累计三个数字，
		// 没有任何凭据字段，可以进审计快照。
		"epoch": true, "baselineAmount": true, "totalAmountUsed": true, "quotaEpoch": true,
	}
	result := make(map[string]any)
	for key, value := range fields {
		if !allowed[key] {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			result[key] = accountAuditFields(nested)
		} else {
			switch value.(type) {
			case string, bool, int, int32, int64, float64, []string, nil:
				result[key] = value
			}
		}
	}
	return result
}

func WriteAccountAuditWithDB(db *gorm.DB, event AccountAuditEvent) error {
	if db == nil {
		return fmt.Errorf("审计数据库不可用")
	}
	request, err := json.Marshal(accountAuditFields(event.Request))
	if err != nil {
		return err
	}
	result, err := json.Marshal(accountAuditFields(event.Result))
	if err != nil {
		return err
	}
	description := []rune(strings.TrimSpace(event.Description))
	if len(description) > 240 {
		description = append(description[:237], '.', '.', '.')
	}
	entry := entity.Log{
		UserID: event.UserID, Username: strings.TrimSpace(event.Username),
		Module: event.Module, Action: event.Action, Description: string(description),
		ClientIP: event.ClientIP, CorrelationID: event.CorrelationID,
		OldAnswer: string(request), NewAnswer: string(result), CreateTime: time.Now().In(logger.ShanghaiLocation()),
	}
	return db.Session(&gorm.Session{NewDB: true}).Create(&entry).Error
}

func MaskAuditPhone(phone string) string {
	runes := []rune(strings.TrimSpace(phone))
	if len(runes) > 7 {
		return string(runes[:3]) + "****" + string(runes[len(runes)-4:])
	}
	return ""
}

// Only exact evidence consumed by a successful account flow may acquire an owner.
func BindAccountVerificationLogsWithDB(db *gorm.DB, user entity.User, token, scene string, evidence SMSVerificationEvidence) error {
	if db == nil || user.ID <= 0 {
		return fmt.Errorf("验证码日志关联身份无效")
	}
	correlations := make([]string, 0, 3)
	if strings.TrimSpace(token) != "" {
		correlation, err := ConsumedCaptchaCorrelationIDWithDB(db, token, scene)
		if err != nil {
			return err
		}
		if correlation != "" {
			correlations = append(correlations, correlation)
		}
	}
	if evidence.CaptchaCorrelationID != "" {
		correlations = append(correlations, evidence.CaptchaCorrelationID)
	}
	if evidence.SMSRecordID > 0 {
		correlations = append(correlations, fmt.Sprintf("sms:%d", evidence.SMSRecordID))
	}
	if len(correlations) == 0 {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.Log{}).
		Where("user_id = ? AND module IN ? AND correlation_id IN ?", 0, []string{"captcha", "sms"}, correlations).
		Updates(map[string]any{"user_id": user.ID, "username": user.Username}).Error
}
